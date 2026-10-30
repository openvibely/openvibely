package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/transcript"
)

// BenchmarkTranscriptPath exercises the complete persisted transcript path with
// production Recorder and repository APIs (see scripts/replay-memory-bench/README.md).
// Phases: stream writes and finalize,
// reconnect tail reads, provider replay checkpoints, compaction checkpoint
// replacement, and cold recovery of replay and checkpoint state.
func BenchmarkTranscriptPath(b *testing.B) {
	for _, items := range []int{100, 1000} {
		b.Run(fmt.Sprintf("items=%d", items), func(b *testing.B) { benchmarkTranscriptPath(b, items) })
	}
}

type transcriptBench struct {
	repo *ExecutionRepo
	task *models.Task
	ctx  context.Context
}

func newTranscriptBench(b *testing.B) *transcriptBench {
	db := testutil.NewTestDB(b)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "transcript bench", Category: models.CategoryChat, Status: models.StatusRunning}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		b.Fatal(err)
	}
	return &transcriptBench{repo: NewExecutionRepo(db), task: task, ctx: ctx}
}

func (t *transcriptBench) execution(b *testing.B) string {
	ex := &models.Execution{TaskID: t.task.ID, Status: models.ExecRunning}
	if err := t.repo.Create(t.ctx, ex); err != nil {
		b.Fatal(err)
	}
	return ex.ID
}

// benchTranscriptChunk alternates text and tool markers like a tool-using run.
func benchTranscriptChunk(i int) string {
	if i%5 == 4 {
		return fmt.Sprintf("\n[Using tool: bash | step %d]\n[Tool bash done]\nresult %d\n[/Tool]\n", i, i)
	}
	return fmt.Sprintf("token chunk %d %s ", i, strings.Repeat("é", 64))
}

func benchReplayHistory(n int) []any {
	history := make([]any, n)
	for i := range history {
		history[i] = map[string]any{"role": "user", "content": fmt.Sprintf("message %d %s", i, strings.Repeat("x", 512))}
	}
	return history
}

func benchmarkTranscriptPath(b *testing.B, items int) {
	b.Run("stream_and_finalize", func(b *testing.B) {
		t := newTranscriptBench(b)
		b.ReportAllocs()
		for b.Loop() {
			id := t.execution(b)
			var output strings.Builder
			for i := 0; i < items; i++ {
				chunk := benchTranscriptChunk(i)
				output.WriteString(chunk)
				if err := t.repo.AppendOutput(t.ctx, id, chunk); err != nil {
					b.Fatal(err)
				}
			}
			if err := t.repo.FinalizeOutput(t.ctx, id, output.String()); err != nil {
				b.Fatal(err)
			}
			if err := t.repo.Complete(t.ctx, id, models.ExecCompleted, output.String(), "", 0, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("reconnect_tail_100_bytes", func(b *testing.B) {
		t := newTranscriptBench(b)
		id := t.execution(b)
		for i := 0; i < items; i++ {
			if err := t.repo.AppendOutput(t.ctx, id, benchTranscriptChunk(i)); err != nil {
				b.Fatal(err)
			}
		}
		full, err := t.repo.ReadOutputAfter(t.ctx, id, 0)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			got, err := t.repo.ReadOutputAfter(t.ctx, id, full.Total-100)
			if err != nil || len(got.Output) < 100 || len(got.Output) > 103 {
				b.Fatal(err, len(got.Output))
			}
		}
	})
	b.Run("replay_checkpoint_append_2", func(b *testing.B) {
		t := newTranscriptBench(b)
		id := t.execution(b)
		history := benchReplayHistory(items)
		recorder := transcript.NewRecorder(t.repo, models.ConversationOwner{ExecutionID: id})
		header := transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_input"}
		if err := recorder.Save(t.ctx, header, history); err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		for b.Loop() {
			history = append(history, map[string]any{"role": "assistant", "content": "reply"}, map[string]any{"role": "user", "content": "next"})
			if err := recorder.Save(t.ctx, header, history); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("compaction_and_recovery", func(b *testing.B) {
		t := newTranscriptBench(b)
		id := t.execution(b)
		history := benchReplayHistory(items)
		recorder := transcript.NewRecorder(t.repo, models.ConversationOwner{ExecutionID: id})
		header := transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_input"}
		if err := recorder.Save(t.ctx, header, history); err != nil {
			b.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{"responses_input": history[max(0, len(history)-40):]})
		if err != nil {
			b.Fatal(err)
		}
		retained := []models.Execution{{ID: id, TaskID: t.task.ID, Status: models.ExecCompleted, Output: "retained", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}}}
		b.ReportAllocs()
		n := 0
		for b.Loop() {
			n++
			checkpoint := models.ChatCompactionCheckpoint{ScopeType: "task", ScopeID: t.task.ID, SourceExecutionID: id, Strategy: "local_summary", Summary: fmt.Sprintf("summary %d", n), History: retained}
			if err := t.repo.UpsertChatCompactionCheckpoint(t.ctx, checkpoint); err != nil {
				b.Fatal(err)
			}
			restored, err := t.repo.GetChatCompactionCheckpoint(t.ctx, "task", t.task.ID)
			if err != nil || restored == nil || len(restored.History) != 1 {
				b.Fatal(err)
			}
			replay, err := t.repo.ReplayMessagesByExecutionIDs(t.ctx, []string{id})
			if err != nil || len(replay[id]) != 1 {
				b.Fatal(err)
			}
		}
	})
}
