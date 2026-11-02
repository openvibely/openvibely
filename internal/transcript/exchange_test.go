package transcript_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestExchangeBoundaryAcrossProviderFormats(t *testing.T) {
	for _, format := range []string{"responses_delta", "messages", "array"} {
		for _, sourceKind := range []string{"completed", "synthetic", "current", "running", "failed", "cross-task"} {
			t.Run(fmt.Sprintf("%s/%s", format, sourceKind), func(t *testing.T) {
				ctx := context.Background()
				stores := teststores.New(t).Stores
				task := &models.Task{ProjectID: "default", Title: "exchange", Category: models.CategoryCompleted, Status: models.StatusCompleted}
				require.NoError(t, stores.Tasks.Create(ctx, task))
				source := models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
				if sourceKind == "running" {
					source.Status = models.ExecRunning
				}
				if sourceKind == "failed" {
					source.Status = models.ExecFailed
				}
				require.NoError(t, stores.Executions.Create(ctx, &source))
				current := models.Execution{TaskID: task.ID, Status: models.ExecRunning}
				require.NoError(t, stores.Executions.Create(ctx, &current))
				header := transcript.ReplayHeader{Format: "envelope", ItemsKey: format}
				if format == "array" {
					header.Format = "array"
					header.ItemsKey = ""
				}
				replacement := header
				if format == "responses_delta" {
					replacement.ItemsKey = "responses_input"
				}
				prefix := []any{map[string]any{"role": "user", "content": "EARLIER_CURRENT_WORK"}}
				original := transcript.NewRecorder(stores.Executions, models.ConversationOwner{ExecutionID: current.ID})
				require.NoError(t, original.Save(ctx, header, prefix))
				require.NoError(t, transcript.NewRecorder(stores.Executions, models.ConversationOwner{ExecutionID: source.ID}).Save(ctx, header, prefix))
				source.ReplayMessages = []models.ExecutionReplayMessage{{TranscriptJSON: `[{"role":"user","content":"EARLIER_CURRENT_WORK"}]`}}
				// In-memory status is not sufficient evidence: query persisted provenance.
				source.Status = models.ExecCompleted
				switch sourceKind {
				case "synthetic":
					source.ID = current.ID + "-steering-context-1"
				case "current":
					source.ID = current.ID
				case "cross-task":
					other := &models.Task{ProjectID: "default", Title: "other", Category: models.CategoryCompleted, Status: models.StatusCompleted}
					require.NoError(t, stores.Tasks.Create(ctx, other))
					current = models.Execution{TaskID: other.ID, Status: models.ExecRunning}
					require.NoError(t, stores.Executions.Create(ctx, &current))
				}
				recorder := transcript.NewExchangeRecorder(stores.Executions, models.ConversationOwner{ExecutionID: current.ID}, []models.Execution{source})
				items := []any{map[string]any{"role": "user", "content": "LATE_STEER"}}
				for turn := 0; turn < 2; turn++ {
					if turn == 1 {
						items = append(items, map[string]any{"role": "assistant", "content": "TOOL_FINISHED"})
					}
					recovery, err := recorder.SaveUpdate(ctx, header, replacement, transcript.ExchangeUpdate{Prefix: prefix, Items: items, FirstChanged: turn})
					require.NoError(t, err)
					require.Len(t, recovery, len(prefix)+len(items))
					// Inspect durable state immediately, before handler finalization or any
					// in-memory recovery callback could mask a crash-time loss.
					saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
					require.NoError(t, err)
					require.Len(t, saved[current.ID], 1)
					raw := saved[current.ID][0].FullTranscriptJSON()
					require.Contains(t, raw, "LATE_STEER")
					if sourceKind == "completed" {
						require.NotContains(t, raw, "EARLIER_CURRENT_WORK")
					} else {
						require.Contains(t, raw, "EARLIER_CURRENT_WORK")
					}
				}
				summary := []any{map[string]any{"role": "user", "content": "REPLACEMENT_SUMMARY"}}
				_, err := recorder.SaveUpdate(ctx, header, replacement, transcript.ExchangeUpdate{Prefix: prefix, Items: summary, ReplacesHistory: true})
				require.NoError(t, err)
				saved, err := stores.Executions.ReplayMessagesByExecutionIDs(ctx, []string{current.ID})
				require.NoError(t, err)
				require.Contains(t, saved[current.ID][0].FullTranscriptJSON(), "REPLACEMENT_SUMMARY")
				require.NotContains(t, saved[current.ID][0].FullTranscriptJSON(), "LATE_STEER")
			})
		}
	}
}
