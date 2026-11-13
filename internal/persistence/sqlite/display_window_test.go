package sqlite

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

func TestDisplayExecutionPreservesCompleteResponse(t *testing.T) {
	for _, status := range []models.ExecutionStatus{models.ExecRunning, models.ExecCompleted, models.ExecFailed, models.ExecCancelled} {
		t.Run(string(status), func(t *testing.T) {
			_, repo, owner := conversationTestRepo(t)
			ctx := t.Context()
			var changes []models.ConversationChange
			var want strings.Builder
			for i := 0; i < 45; i++ {
				text := fmt.Sprintf("message-%02d\n", i)
				want.WriteString(text)
				raw, err := json.Marshal(map[string]any{"type": "text", "text": text})
				require.NoError(t, err)
				changes = append(changes, models.ConversationChange{ItemID: fmt.Sprintf("display:%02d", i), Position: int64(i / 2), Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: raw, MutationID: fmt.Sprintf("msg-%d", i)})
			}
			changes = append(changes, models.ConversationChange{ItemID: "provider-secret", Lane: models.ConversationProvider, Operation: models.ConversationPut, Payload: json.RawMessage(`{"secret":"PRIVATE"}`), MutationID: "secret"})
			_, err := repo.AppendConversationChanges(ctx, owner, changes)
			require.NoError(t, err)
			require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, "pending tail"))
			want.WriteString("pending tail")
			if status != models.ExecRunning {
				require.NoError(t, repo.Complete(ctx, owner.ExecutionID, status, "", "terminal error", 0, 0))
			}
			got, err := repo.GetDisplayExecution(ctx, owner.ExecutionID)
			require.NoError(t, err)
			require.Equal(t, want.String(), got.Output)
			require.Equal(t, status, got.Status)
			require.NotContains(t, got.Output, "PRIVATE")
		})
	}
}

func TestDisplayExecutionKeepsEarlierPlanContent(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	output := "<proposed_plan>Earlier plan</proposed_plan>\n" + strings.Repeat("text\n[Using tool: shell | echo hi]\n[Tool shell done]\n"+strings.Repeat("result\n", 10000)+"[/Tool]\n", 20)
	require.NoError(t, repo.Complete(t.Context(), owner.ExecutionID, models.ExecCompleted, output, "", 0, 0))
	got, err := repo.GetDisplayExecution(t.Context(), owner.ExecutionID)
	require.NoError(t, err)
	require.Equal(t, output, got.Output)
	for _, legacy := range []bool{false, true} {
		if legacy {
			_, err := db.ExecContext(t.Context(), `UPDATE executions SET display_has_plan=NULL WHERE id=?`, owner.ExecutionID)
			require.NoError(t, err)
		}
		tail, err := repo.GetDisplayExecutionTail(t.Context(), owner.ExecutionID)
		require.NoError(t, err)
		require.Less(t, len(tail.Output), len(output))
		require.Equal(t, "<proposed_plan>Earlier plan</proposed_plan>\n", tail.DisplayPlan)
		require.NotNil(t, tail.DisplayTail)
		// Older pages still contain the original plan exactly once.
		joined := tail.Output
		cursor := tail.DisplayTail.Before
		for cursor != 0 {
			page, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, cursor, 1024*1024)
			require.NoError(t, err)
			joined = page.Text + joined
			cursor = page.Before
		}
		require.Equal(t, output, joined)
	}
}

func TestDisplayByteWindowsRoundTripAndIsolation(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	var want strings.Builder
	var changes []models.ConversationChange
	for i := 0; i < 600; i++ {
		text := fmt.Sprintf("block-%03d café\n```go\nfmt.Println(1)\n```\n", i)
		want.WriteString(text)
		raw, err := json.Marshal(map[string]any{"type": "text", "text": text})
		require.NoError(t, err)
		changes = append(changes, models.ConversationChange{ItemID: fmt.Sprintf("display:%04d", i), Position: int64(i / 2), Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: raw, MutationID: fmt.Sprintf("window-%d", i)})
	}
	changes = append(changes, models.ConversationChange{ItemID: "secret", Lane: models.ConversationProvider, Operation: models.ConversationPut, Payload: json.RawMessage(`{"text":"PRIVATE"}`), MutationID: "secret"})
	_, err := repo.AppendConversationChanges(ctx, owner, changes)
	require.NoError(t, err)
	require.NoError(t, repo.Complete(ctx, owner.ExecutionID, models.ExecCompleted, "", "", 0, 0))
	unauthorized, err := repo.ReadProjectDisplayTail(ctx, "other-project", owner.ExecutionID, 0, 1000)
	require.ErrorIs(t, err, persistence.ErrNotFound)
	require.Empty(t, unauthorized.Text)
	var cursor int64
	var joined string
	pages := 0
	for {
		page, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, cursor, 1000)
		require.NoError(t, err)
		require.LessOrEqual(t, len(page.Text), 1000)
		require.NotContains(t, page.Text, "PRIVATE")
		require.Equal(t, 0, strings.Count(page.Text, "```")%2, "never split fenced Markdown")
		joined = page.Text + joined
		pages++
		if page.Before == 0 {
			break
		}
		require.NotEqual(t, cursor, page.Before)
		cursor = page.Before
		require.Less(t, pages, 100)
	}
	require.Equal(t, want.String(), joined)
	require.Greater(t, pages, 1)
	full, err := repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 0, len(want.String()))
	require.NoError(t, err)
	require.Zero(t, full.Before, "SQL batching must not impose an item-count window")
	require.Equal(t, want.String(), full.Text)
	_, err = repo.ReadProjectDisplayTail(ctx, "default", owner.ExecutionID, 999999, 1000)
	require.Error(t, err)
}

func TestDisplayByteWindowOversizedBlockAndStreaming(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	output := "```text\n" + strings.Repeat("oversized ", 300000) + "\n```"
	require.NoError(t, repo.AppendOutput(t.Context(), owner.ExecutionID, output))
	running, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, 0, 1024)
	require.NoError(t, err)
	require.Equal(t, output, running.Text)
	require.Zero(t, running.Before)
	require.NoError(t, repo.Complete(t.Context(), owner.ExecutionID, models.ExecCompleted, "", "", 0, 0))
	done, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, 0, 1024)
	require.NoError(t, err)
	require.Equal(t, output, done.Text)
	require.Zero(t, done.Before)
}

func TestDisplayByteWindowPreservesCancelledRawStream(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := t.Context()
	exec, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	output := strings.Repeat("unfinished tool output\n", 60000)
	require.NoError(t, repo.AppendOutput(ctx, owner.ExecutionID, output))
	ids, err := repo.CancelActiveByTaskThroughHistoryOrderReturningIDs(ctx, exec.TaskID, exec.HistoryOrder)
	require.NoError(t, err)
	require.Contains(t, ids, exec.ID)
	got, err := repo.GetDisplayExecutionTail(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecCancelled, got.Status)
	require.Equal(t, output, got.Output, "cancellation does not finalize pending chunks")
	require.Nil(t, got.DisplayTail)
}

func TestDisplayWindowCompletionSnapshot(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	db.SetMaxOpenConns(1)
	ctx := t.Context()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	output := strings.Repeat("before\n[Using tool: shell | echo]\n[Tool shell done]\n"+strings.Repeat("result\n", 10000)+"[/Tool]\n", 20)
	type readResult struct {
		execution *models.Execution
		err       error
	}
	for round := 0; round < 8; round++ {
		exec := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
		require.NoError(t, repo.Create(ctx, exec))
		require.NoError(t, repo.AppendOutput(ctx, exec.ID, output))
		// Queue both readers and completion behind the same occupied connection.
		// Either may win; status and output must belong to that same snapshot.
		conn, err := db.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close()
		waits := db.Stats().WaitCount
		read := make(chan readResult, 1)
		write := make(chan error, 1)
		go func(list bool) {
			if list {
				rows, _, err := repo.ListDisplayExecutions(ctx, "", exec.TaskID, "", 1, 1024*1024)
				if err != nil || len(rows) == 0 {
					read <- readResult{err: err}
					return
				}
				read <- readResult{execution: &rows[0]}
				return
			}
			got, err := repo.GetDisplayExecutionTail(ctx, exec.ID)
			read <- readResult{got, err}
		}(round%2 == 0)
		require.Eventually(t, func() bool { return db.Stats().WaitCount > waits }, 5*time.Second, time.Millisecond)
		go func() { write <- repo.Complete(ctx, exec.ID, models.ExecCompleted, output, "", 0, 0) }()
		require.Eventually(t, func() bool { return db.Stats().WaitCount > waits+1 }, 5*time.Second, time.Millisecond)
		require.NoError(t, conn.Close())
		got := <-read
		require.NoError(t, got.err)
		require.NotNil(t, got.execution)
		require.Equal(t, exec.ID, got.execution.ID)
		if got.execution.Status == models.ExecRunning {
			require.NotNil(t, got.execution.DisplayTail)
			require.Equal(t, int64(len(output)), got.execution.DisplayTail.EndOffset, "live resume uses the full durable offset, not the visible suffix length")
			require.True(t, strings.HasSuffix(output, got.execution.Output))
			require.True(t, strings.HasSuffix(output, got.execution.Output))
		} else {
			require.Equal(t, models.ExecCompleted, got.execution.Status)
			require.NotNil(t, got.execution.DisplayTail)
			require.True(t, strings.HasSuffix(output, got.execution.Output))
		}
		require.NoError(t, <-write)
	}
}

func TestDisplayByteWindowKeepsParallelToolGroups(t *testing.T) {
	for _, secondResult := range []bool{false, true} {
		t.Run(fmt.Sprint(secondResult), func(t *testing.T) {
			_, repo, owner := conversationTestRepo(t)
			prefix := "Earlier prose\n"
			group := "[Using tool: shell | first]\n[Using tool: shell | second]\n[Tool shell done]\nFIRST_RESULT\n[/Tool]\n"
			if secondResult {
				group += "[Tool shell done]\nSECOND_RESULT\n[/Tool]\n"
			}
			output := prefix + group
			require.NoError(t, repo.Complete(t.Context(), owner.ExecutionID, models.ExecCompleted, output, "", 0, 0))
			// Even a budget smaller than a result must preserve both FIFO calls.
			tail, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, 0, 10)
			require.NoError(t, err)
			require.Equal(t, group, tail.Text)
			require.NotZero(t, tail.Before)
			earlier, err := repo.ReadProjectDisplayTail(t.Context(), "default", owner.ExecutionID, tail.Before, 10)
			require.NoError(t, err)
			require.Equal(t, prefix, earlier.Text)
			require.Zero(t, earlier.Before)
		})
	}
}
