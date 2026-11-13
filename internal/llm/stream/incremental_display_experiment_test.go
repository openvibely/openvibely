package stream

import (
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

// This experiment deliberately does not change the production writer. It tests
// whether existing structured storage can replace the accumulated display buffer
// for complete groups. Partial-message recovery and adapter return values still
// need integration before this is a production alternative.
func TestIncrementalDisplayExperiment(t *testing.T) {
	if os.Getenv("OV_INCREMENTAL_DISPLAY_EXPERIMENT") != "1" {
		t.Skip("opt-in file-backed display persistence experiment")
	}
	for _, mode := range []string{"buffered", "structured"} {
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
			if mode == "buffered" {
				writer = NewWriterWithPublisher(exec.ID, task.ID, env.Executions, ctx, time.Hour, nil)
				defer writer.Stop()
			}
			expected := sha256.New()
			runtime.GC()
			var before, live, end runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			var writeTime time.Duration
			total, position := 0, int64(0)
			for i := 0; i < groups; i++ {
				group := fmt.Sprintf("answer %d\n[Using tool: shell | test]\n[Tool shell done]\n%s\n[/Tool]\n", i, body)
				expected.Write([]byte(group))
				total += len(group)
				at := time.Now()
				if writer != nil {
					writer.WriteString(group)
					writer.flushPeriodicOnce()
				} else {
					records, err := transcript.DisplayRecords(transcript.ParseDisplay(group))
					if err != nil {
						t.Fatal(err)
					}
					for j := range records {
						records[j].Position = position
						records[j].ItemID = fmt.Sprintf("incremental:%012d", position)
						records[j].MutationID = records[j].ItemID
						position++
					}
					if _, err := env.Executions.AppendConversationChanges(ctx, owner, records); err != nil {
						t.Fatal(err)
					}
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
			runtime.GC()
			runtime.ReadMemStats(&live)
			runtime.KeepAlive(writer)
			finishStart := time.Now()
			if writer != nil {
				writer.Flush()
			}
			if err := env.Executions.Complete(ctx, exec.ID, models.ExecCompleted, "", "", 0, 0); err != nil {
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
			t.Logf("mode=%s bytes=%d groups=%d write_ms=%.2f finalize_ms=%.2f retained_delta_mib=%.2f allocations_mib=%.2f elapsed_ms=%.2f", mode, total, groups, float64(writeTime.Microseconds())/1000, float64(finishTime.Microseconds())/1000, float64(int64(live.HeapAlloc)-int64(before.HeapAlloc))/(1<<20), float64(end.TotalAlloc-before.TotalAlloc)/(1<<20), float64(time.Since(start).Microseconds())/1000)
			runtime.KeepAlive(writer)
		})
	}
}
