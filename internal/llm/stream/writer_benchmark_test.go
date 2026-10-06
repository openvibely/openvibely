package stream

import (
	"bytes"
	"context"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

const (
	benchmarkTranscriptBytes = 120 * 1024
	benchmarkSnapshots       = 120
)

type benchmarkOutputRepo struct {
	repo         *repository.ExecutionRepo
	payloadBytes int64
	writeTime    time.Duration
}

func (r *benchmarkOutputRepo) GetByID(ctx context.Context, id string) (*models.Execution, error) {
	return r.repo.GetByID(ctx, id)
}

func (r *benchmarkOutputRepo) AppendOutput(ctx context.Context, id string, output string) error {
	started := time.Now()
	err := r.repo.AppendOutput(ctx, id, output)
	r.writeTime += time.Since(started)
	r.payloadBytes += int64(len(output))
	return err
}

func (r *benchmarkOutputRepo) FinalizeOutput(ctx context.Context, id string, output string) error {
	started := time.Now()
	err := r.repo.FinalizeOutput(ctx, id, output)
	r.writeTime += time.Since(started)
	r.payloadBytes += int64(len(output))
	return err
}

func BenchmarkWriterOutputPersistence(b *testing.B) {
	b.Run("120KiB/current-full-snapshot", func(b *testing.B) {
		benchmarkWriterOutputPersistence(b, benchmarkTranscriptBytes, benchmarkSnapshots, false)
	})
	b.Run("120KiB/append-delta", func(b *testing.B) {
		benchmarkWriterOutputPersistence(b, benchmarkTranscriptBytes, benchmarkSnapshots, true)
	})
	b.Run("1MiB/append-delta", func(b *testing.B) {
		benchmarkWriterOutputPersistence(b, 1024*1024, 256, true)
	})
}

func benchmarkWriterOutputPersistence(b *testing.B, transcriptBytes, snapshots int, appendDeltas bool) {
	b.Helper()
	ctx := context.Background()
	_, writer, _ := testutil.NewFileBackedSplitStatementCountingTestDB(b)
	execRepo := repository.NewExecutionRepo(writer)
	taskRepo := repository.NewTaskRepo(writer, nil)
	task := &models.Task{
		ProjectID: "default",
		Title:     "Stream writer benchmark",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "benchmark",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		b.Fatalf("create benchmark task: %v", err)
	}
	llmConfig, err := repository.NewLLMConfigRepo(writer).GetDefault(ctx)
	if err != nil || llmConfig == nil {
		b.Fatalf("load benchmark agent config: config=%v err=%v", llmConfig, err)
	}
	largeExec := &models.Execution{TaskID: task.ID, AgentConfigID: llmConfig.ID, Status: models.ExecRunning, PromptSent: "benchmark"}
	smallExec := &models.Execution{TaskID: task.ID, AgentConfigID: llmConfig.ID, Status: models.ExecRunning, PromptSent: "small write"}
	if err := execRepo.Create(ctx, largeExec); err != nil {
		b.Fatalf("create large execution: %v", err)
	}
	if err := execRepo.Create(ctx, smallExec); err != nil {
		b.Fatalf("create small execution: %v", err)
	}

	chunkBytes := transcriptBytes / snapshots
	if chunkBytes*snapshots != transcriptBytes {
		b.Fatalf("transcript size %d is not evenly divisible into %d snapshots", transcriptBytes, snapshots)
	}
	chunk := bytes.Repeat([]byte{'x'}, chunkBytes)
	chunks := make([][]byte, snapshots)
	for i := range chunks {
		chunks[i] = chunk
	}
	var expected bytes.Buffer
	expected.Grow(transcriptBytes)
	for range snapshots {
		expected.Write(chunk)
	}
	wantOutput := expected.String()
	snapshotAllocations := measureSnapshotAllocations(chunks, appendDeltas)

	var submittedBytes int64
	var sqliteWriteTime time.Duration
	var smallWriteLatencies []time.Duration
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		if err := execRepo.UpdateOutput(ctx, largeExec.ID, ""); err != nil {
			b.Fatalf("reset large execution output: %v", err)
		}
		b.StartTimer()

		if appendDeltas {
			measuredRepo := &benchmarkOutputRepo{repo: execRepo}
			sw := newWriterWithOutputRepo(largeExec.ID, task.ID, measuredRepo, ctx, time.Hour, nil)
			for _, part := range chunks {
				latency, err := benchmarkSmallWriteAlongside(ctx, execRepo, smallExec.ID, func() error {
					if _, err := sw.Write(part); err != nil {
						return err
					}
					sw.flushPeriodicOnce()
					return nil
				})
				if err != nil {
					sw.Stop()
					b.Fatalf("append-delta stream write: %v", err)
				}
				smallWriteLatencies = append(smallWriteLatencies, latency)
			}
			sw.Flush()
			sw.Stop()
			submittedBytes += measuredRepo.payloadBytes
			sqliteWriteTime += measuredRepo.writeTime
		} else {
			var output bytes.Buffer
			output.Grow(transcriptBytes)
			for _, part := range chunks {
				latency, err := benchmarkSmallWriteAlongside(ctx, execRepo, smallExec.ID, func() error {
					if _, err := output.Write(part); err != nil {
						return err
					}
					snapshot := output.String()
					submittedBytes += int64(len(snapshot))
					started := time.Now()
					_, err := execRepo.DB().ExecContext(ctx,
						`UPDATE executions SET output = ? WHERE id = ? AND status = 'running'`, snapshot, largeExec.ID)
					sqliteWriteTime += time.Since(started)
					return err
				})
				if err != nil {
					b.Fatalf("current-path stream write: %v", err)
				}
				smallWriteLatencies = append(smallWriteLatencies, latency)
			}
			finalOutput := output.String()
			submittedBytes += int64(len(finalOutput))
			started := time.Now()
			if _, err := execRepo.DB().ExecContext(ctx,
				`UPDATE executions SET output = ? WHERE id = ? AND status = 'running'`, finalOutput, largeExec.ID); err != nil {
				b.Fatalf("current-path final flush: %v", err)
			}
			sqliteWriteTime += time.Since(started)
		}

		persisted, err := execRepo.GetByID(ctx, largeExec.ID)
		if err != nil {
			b.Fatalf("read persisted output: %v", err)
		}
		if persisted.Output != wantOutput {
			b.Fatalf("persisted output length = %d, want %d", len(persisted.Output), len(wantOutput))
		}
	}
	b.StopTimer()

	operations := float64(b.N)
	b.ReportMetric(float64(submittedBytes)/operations, "payload-B/op")
	b.ReportMetric(float64(snapshotAllocations), "snapshot-alloc-B/op")
	b.ReportMetric(float64(sqliteWriteTime.Nanoseconds())/operations, "sqlite-write-ns/op")
	if len(smallWriteLatencies) > 0 {
		b.ReportMetric(float64(percentileDuration(smallWriteLatencies, 0.50).Nanoseconds()), "small-write-p50-ns/op")
		b.ReportMetric(float64(percentileDuration(smallWriteLatencies, 0.95).Nanoseconds()), "small-write-p95-ns/op")
	}
}

func benchmarkSmallWriteAlongside(ctx context.Context, repo *repository.ExecutionRepo, execID string, largeWrite func() error) (time.Duration, error) {
	ready := make(chan struct{})
	start := make(chan struct{})
	type result struct {
		latency time.Duration
		err     error
	}
	finished := make(chan result, 1)
	go func() {
		close(ready)
		<-start
		started := time.Now()
		err := repo.UpdateOutput(ctx, execID, "small")
		finished <- result{latency: time.Since(started), err: err}
	}()
	<-ready
	close(start)
	largeErr := largeWrite()
	smallResult := <-finished
	if largeErr != nil {
		return smallResult.latency, largeErr
	}
	return smallResult.latency, smallResult.err
}

func measureSnapshotAllocations(chunks [][]byte, appendDeltas bool) uint64 {
	var output bytes.Buffer
	total := 0
	for _, chunk := range chunks {
		total += len(chunk)
	}
	output.Grow(total)
	snapshots := make([]string, len(chunks)+1)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	offset := 0
	for i, chunk := range chunks {
		_, _ = output.Write(chunk)
		if appendDeltas {
			snapshots[i] = pendingOutputDelta(&output, offset)
			offset = output.Len()
		} else {
			snapshots[i] = output.String()
		}
	}
	snapshots[len(chunks)] = output.String() // final full transcript materialization
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(snapshots)
	return after.TotalAlloc - before.TotalAlloc
}

func percentileDuration(values []time.Duration, percentile float64) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)-1) * percentile)
	return sorted[index]
}
