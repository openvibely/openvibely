package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
)

func TestExecutionReplaySuffix(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "replay", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	ex := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, ex))
	require.NoError(t, repo.FinalizeOutput(ctx, ex.ID, "hé"))
	for _, chunk := range []string{"llo", "世界", "!"} {
		require.NoError(t, repo.AppendOutput(ctx, ex.ID, chunk))
	}
	full := "héllo世界!"
	for offset := 0; offset <= len(full); offset++ {
		got, err := repo.ReadOutputAfter(ctx, ex.ID, offset)
		require.NoError(t, err)
		require.Equal(t, len(full), got.Total)
		require.Equal(t, full[got.Start:], got.Output)
		expectedStart := offset
		for expectedStart > 0 && expectedStart < len(full) && !utf8.RuneStart(full[expectedStart]) {
			expectedStart--
		}
		require.Equal(t, expectedStart, got.Start)
	}
	got, err := repo.ReadOutputAfter(ctx, ex.ID, len(full)+20)
	require.NoError(t, err)
	require.Empty(t, got.Output)
	require.NoError(t, repo.Complete(ctx, ex.ID, models.ExecCompleted, "short", "", 0, 0))
	got, err = repo.ReadOutputAfter(ctx, ex.ID, len(full))
	require.NoError(t, err)
	require.Equal(t, 0, got.Start)
	require.Equal(t, "short", got.Output)
	require.Equal(t, 5, got.Total)
	got, err = repo.ReadOutputAfter(ctx, "missing", 0)
	require.NoError(t, err)
	require.Nil(t, got)
}

func BenchmarkExecutionReplayTail(b *testing.B) {
	db := testutil.NewTestDB(b)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "replay benchmark", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(b, NewTaskRepo(db, nil).Create(ctx, task))
	ex := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(b, repo.Create(ctx, ex))
	chunk := strings.Repeat("x", 8192)
	for i := 0; i < 128; i++ {
		require.NoError(b, repo.AppendOutput(ctx, ex.ID, chunk))
	}
	require.NoError(b, repo.AppendOutput(ctx, ex.ID, strings.Repeat("y", 100)))
	b.Run("full_read", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_, err := repo.GetByID(ctx, ex.ID)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("missing_100_bytes", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			got, err := repo.ReadOutputAfter(ctx, ex.ID, 1024*1024)
			if err != nil {
				b.Fatal(err)
			}
			if len(got.Output) != 100 {
				b.Fatal(len(got.Output))
			}
		}
	})
}

func TestExecutionReplayTailMatchesProjection(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "replay projection", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	ex := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, ex))
	owner := models.ConversationOwner{ExecutionID: ex.ID}
	check := func() {
		t.Helper()
		got, err := repo.GetByID(ctx, ex.ID)
		require.NoError(t, err)
		full := got.Output
		for offset := 0; offset <= len(full); offset++ {
			tail, err := repo.ReadOutputAfter(ctx, ex.ID, offset)
			require.NoError(t, err)
			require.Equal(t, len(full), tail.Total)
			require.Equal(t, full[tail.Start:], tail.Output, "offset %d", offset)
			require.LessOrEqual(t, tail.Start, offset)
		}
	}
	require.NoError(t, repo.FinalizeOutput(ctx, ex.ID, "intro é\n[Thinking]\nponder\n[/Thinking]\n[Tool bash done]\na &#91;b]\n[/Tool]\n"))
	check()
	// A raw tool result is escaped on projection and its appends are escaped too.
	raw := `{"type":"tool_result","name":"bash","status":"done","text":"x[&","complete":false,"prefix":"[Tool bash done]\n"}`
	saved, err := repo.AppendConversationChanges(ctx, models.ConversationOwner{ExecutionID: ex.ID}, []models.ConversationChange{{ItemID: "display:000000000100", Position: 100, Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: json.RawMessage(raw), MutationID: "tool"}})
	require.NoError(t, err)
	_, err = repo.AppendConversationChanges(ctx, models.ConversationOwner{ExecutionID: ex.ID}, []models.ConversationChange{{ItemID: "display:000000000100", Position: 100, Lane: models.ConversationDisplay, Operation: models.ConversationAppend, Payload: json.RawMessage(`{"text":"[y&世"}`), PreviousRevision: saved[0].Sequence, MutationID: "tool-delta"}})
	require.NoError(t, err)
	check()
	for _, chunk := range []string{"stream ", "世界", "", "!"} {
		require.NoError(t, repo.AppendOutput(ctx, ex.ID, chunk))
	}
	check()
	page, err := repo.ReadConversationPage(ctx, owner, models.ConversationDisplay, 0, -1, "", 100)
	require.NoError(t, err)
	require.Contains(t, string(page.Items[len(page.Items)-1].Payload), `"stream 世界!"`)
	require.NoError(t, repo.Complete(ctx, ex.ID, models.ExecCompleted, "done [Thinking]\nnot a marker", "", 0, 0))
	check()
}

// Superseded revisions are deleted when a put/remove replaces an item; only the
// current snapshot chain remains, so repeated rewrites do not grow storage.
func TestConversationJournalRetentionIsBounded(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{ProjectID: "default", Title: "retention", Category: models.CategoryBacklog, Status: models.StatusPending}
	require.NoError(t, NewTaskRepo(db, nil).Create(ctx, task))
	ex := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, repo.Create(ctx, ex))
	count := func(where string, args ...any) int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM conversation_items WHERE `+where, args...).Scan(&n))
		return n
	}
	var output strings.Builder
	for round := 0; round < 20; round++ {
		for i := 0; i < 10; i++ {
			chunk := fmt.Sprintf("chunk %d.%d ", round, i)
			output.WriteString(chunk)
			require.NoError(t, repo.AppendOutput(ctx, ex.ID, chunk))
		}
		output.WriteString(fmt.Sprintf("\n[Using tool: bash | step %d]\n", round))
		require.NoError(t, repo.FinalizeOutput(ctx, ex.ID, output.String()))
	}
	require.NoError(t, repo.FinalizeOutput(ctx, ex.ID, output.String()))
	heads := count(`execution_id=? AND lane='display' AND is_current=1 AND operation!='remove'`, ex.ID)
	require.Equal(t, heads, count(`execution_id=? AND lane='display'`, ex.ID), "display journal retains superseded rows")
	got, err := repo.GetByID(ctx, ex.ID)
	require.NoError(t, err)
	require.Equal(t, output.String(), got.Output)

	var history []any
	for i := 0; i < 30; i++ {
		history = append(history, map[string]any{"role": "user", "content": fmt.Sprintf("message %d", i)})
		if i%3 == 0 && i > 0 {
			history[i-1] = map[string]any{"role": "assistant", "content": fmt.Sprintf("edited %d", i)}
		}
		err := transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: "envelope", ItemsKey: "responses_input"}, history)
		require.NoError(t, err)
	}
	replayHeads := count(`execution_id=? AND lane!='display' AND is_current=1`, ex.ID)
	require.Equal(t, replayHeads, count(`execution_id=? AND lane!='display'`, ex.ID), "replay journal retains superseded rows")

	scope := models.ChatCompactionCheckpoint{ScopeType: "task_thread", ScopeID: task.ID}
	for i := 0; i < 10; i++ {
		scope.Summary = fmt.Sprintf("summary %d", i)
		scope.History = []models.Execution{{ID: ex.ID, Output: fmt.Sprintf("output %d", i)}}
		require.NoError(t, repo.UpsertChatCompactionCheckpoint(ctx, scope))
	}
	checkpointHeads := count(`checkpoint_scope_id=? AND is_current=1`, task.ID)
	require.Equal(t, checkpointHeads, count(`checkpoint_scope_id=?`, task.ID), "checkpoint journal retains superseded rows")
	restored, err := repo.GetChatCompactionCheckpoint(ctx, scope.ScopeType, scope.ScopeID)
	require.NoError(t, err)
	require.Equal(t, "output 9", restored.History[0].Output)
}
