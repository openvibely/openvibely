package stream

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/transcript"
)

// Compare the legacy writer, two storage prototypes, and the integrated writer.
// This measures completed-group storage; separate tests exercise the background
// timer, partial messages, cancellation, and uncertain acknowledgements.
func TestIncrementalDisplayExperiment(t *testing.T) {
	if os.Getenv("OV_INCREMENTAL_DISPLAY_EXPERIMENT") != "1" {
		t.Skip("opt-in file-backed display persistence experiment")
	}
	for _, mode := range []string{"buffered", "structured", "direct-events", "integrated"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			env := teststores.New(t, teststores.WithProductionTopology())
			task := &models.Task{ProjectID: "default", Title: "display experiment", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "synthetic"}
			if err := env.Tasks.Create(ctx, task); err != nil {
				t.Fatal(err)
			}
			config, err := env.LLMConfigs.GetDefault(ctx)
			if err != nil || config == nil {
				t.Fatalf("default config: %v", err)
			}
			exec := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning, PromptSent: "synthetic"}
			if err := env.Executions.Create(ctx, exec); err != nil {
				t.Fatal(err)
			}
			owner := models.ConversationOwner{ExecutionID: exec.ID}
			const groups = 128
			body := strings.Repeat("synthetic tool result café < value\n", 8192)
			var writer *Writer
			measuredRepo := &benchmarkOutputRepo{repo: env.Executions}
			if mode == "buffered" {
				writer = newWriterWithOutputRepo(exec.ID, task.ID, measuredRepo, ctx, time.Hour, nil)
				defer writer.Stop()
			}
			if mode == "integrated" {
				writer = newWriter(exec.ID, task.ID, &structuredBenchmarkRepo{benchmarkOutputRepo: measuredRepo, store: env.Executions.(StructuredOutputStore)}, ctx, time.Hour, nil, true)
				defer writer.Stop()
				if writer.structured == nil {
					t.Fatal("integrated path disabled")
				}
			}
			expected := sha256.New()
			runtime.GC()
			var before, live, end runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			var writeTime, databaseTime time.Duration
			total, position := 0, int64(0)
			for i := 0; i < groups; i++ {
				group := fmt.Sprintf("answer %d\n[Using tool: shell | test]\n[Tool shell done]\n%s\n[/Tool]\n", i, body)
				expected.Write([]byte(group))
				total += len(group)
				at := time.Now()
				if writer != nil {
					if mode == "integrated" {
						for _, event := range []Event{{Type: EventTextDelta, Text: fmt.Sprintf("answer %d", i)}, {Type: EventToolUse, ToolName: "shell", Secondary: "test"}, {Type: EventToolResult, ToolName: "shell", Output: body}} {
							WriteEvent(writer, event, false)
						}
					} else {
						writer.WriteString(group)
					}
					writer.flushPeriodicOnce()
				} else {
					var items []transcript.DisplayItem
					if mode == "direct-events" {
						items, err = directDisplayExperiment([]Event{
							{Type: EventTextDelta, Text: fmt.Sprintf("answer %d", i)},
							{Type: EventToolUse, ToolName: "shell", Secondary: "test"},
							{Type: EventToolResult, ToolName: "shell", Output: body},
						})
						if err != nil {
							t.Fatal(err)
						}
					} else {
						items = transcript.ParseDisplay(group)
					}
					records, err := transcript.DisplayRecords(items)
					if err != nil {
						t.Fatal(err)
					}
					for j := range records {
						records[j].Position = position
						records[j].ItemID = fmt.Sprintf("incremental:%012d", position)
						records[j].MutationID = records[j].ItemID
						position++
					}
					dbStarted := time.Now()
					if _, err := env.Executions.AppendConversationChanges(ctx, owner, records); err != nil {
						t.Fatal(err)
					}
					databaseTime += time.Since(dbStarted)
				}
				writeTime += time.Since(at)
				// Reconnect must observe exactly the last persisted group, in byte offsets.
				if i == 0 || i == groups-1 {
					replay, err := env.Executions.ReadOutputAfter(ctx, exec.ID, total-len(group))
					if err != nil {
						t.Fatal(err)
					}
					if replay.Output != group || replay.Total != total {
						t.Fatalf("reconnect mismatch at group %d", i)
					}
				}
			}
			if writer != nil {
				databaseTime = measuredRepo.writeTime
			}
			runtime.GC()
			runtime.ReadMemStats(&live)
			runtime.KeepAlive(writer)
			finishStart := time.Now()
			if writer != nil {
				writer.Flush()
			}
			var completionOutput string
			if writer != nil {
				completionOutput, err = writer.Output()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := env.Executions.Complete(ctx, exec.ID, models.ExecCompleted, completionOutput, "", 0, 0); err != nil {
				t.Fatal(err)
			}
			finishTime := time.Since(finishStart)
			runtime.ReadMemStats(&end)
			// Validate durable output after completion without depending on writer RAM.
			stored, err := env.Executions.GetByID(ctx, exec.ID)
			if err != nil {
				t.Fatal(err)
			}
			actual := sha256.Sum256([]byte(stored.Output))
			if fmt.Sprintf("%x", actual) != fmt.Sprintf("%x", expected.Sum(nil)) {
				t.Fatal("stored transcript differs")
			}
			t.Logf("mode=%s bytes=%d groups=%d write_ms=%.2f database_ms=%.2f prepare_ms=%.2f finalize_ms=%.2f retained_delta_mib=%.2f allocations_mib=%.2f elapsed_ms=%.2f", mode, total, groups, float64(writeTime.Microseconds())/1000, float64(databaseTime.Microseconds())/1000, float64((writeTime-databaseTime).Microseconds())/1000, float64(finishTime.Microseconds())/1000, float64(int64(live.HeapAlloc)-int64(before.HeapAlloc))/(1<<20), float64(end.TotalAlloc-before.TotalAlloc)/(1<<20), float64(time.Since(start).Microseconds())/1000)
			runtime.KeepAlive(writer)
		})
	}
}

type structuredBenchmarkRepo struct {
	*benchmarkOutputRepo
	store StructuredOutputStore
}

func (r *structuredBenchmarkRepo) StartStructuredOutput(ctx context.Context, id string) (int64, error) {
	return r.store.StartStructuredOutput(ctx, id)
}
func (r *structuredBenchmarkRepo) SealStructuredOutput(ctx context.Context, id string, digest []byte, total int) error {
	return r.store.SealStructuredOutput(ctx, id, digest, total)
}
func (r *structuredBenchmarkRepo) AppendStructuredOutput(ctx context.Context, id string, c []models.ConversationChange) ([]models.ConversationChange, error) {
	start := time.Now()
	result, err := r.store.AppendStructuredOutput(ctx, id, c)
	r.writeTime += time.Since(start)
	return result, err
}
