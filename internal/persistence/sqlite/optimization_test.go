package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestResponsesHistoryKeepsFallbackOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, format, key string
		count             int
		status            models.ExecutionStatus
		omitted           bool
	}{
		{"native", "envelope", "responses_input", 1, models.ExecCompleted, true},
		{"failed", "envelope", "responses_input", 1, models.ExecFailed, false},
		{"empty", "envelope", "responses_input", 0, models.ExecCompleted, false},
		{"foreign", "envelope", "messages", 1, models.ExecCompleted, false},
		{"legacy", "array", "", 0, models.ExecCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			items := []any{}
			if tc.count > 0 {
				items = append(items, map[string]any{"role": "assistant", "content": "answer"})
			}
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: tc.format, ItemsKey: tc.key}, items))
			require.NoError(t, repo.Complete(ctx, ex.ID, tc.status, "display answer", "", 0, 0))
			rows, err := repo.ListTaskResponsesHistory(ctx, original.TaskID, 100)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.ID == ex.ID {
					found = true
					if tc.omitted {
						require.Empty(t, row.Output)
					} else {
						require.Equal(t, "display answer", row.Output)
					}
				}
			}
			require.True(t, found)
			full, err := repo.GetByID(ctx, ex.ID)
			require.NoError(t, err)
			require.Equal(t, "display answer", full.Output)
		})
	}
}

func TestAnthropicHistoryKeepsFallbackOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, format, key string
		count             int
		status            models.ExecutionStatus
		omitted           bool
	}{
		{"native", "envelope", "messages", 1, models.ExecCompleted, true},
		{"failed", "envelope", "messages", 1, models.ExecFailed, false},
		{"empty", "envelope", "messages", 0, models.ExecCompleted, false},
		{"foreign", "envelope", "responses_input", 1, models.ExecCompleted, false},
		{"legacy", "array", "", 0, models.ExecCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			items := []any{}
			if tc.count > 0 {
				items = append(items, map[string]any{"role": "assistant", "content": "answer"})
			}
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: tc.format, ItemsKey: tc.key}, items))
			require.NoError(t, repo.Complete(ctx, ex.ID, tc.status, "display answer", "", 0, 0))
			rows, err := repo.ListTaskAnthropicHistory(ctx, original.TaskID, 100)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.ID == ex.ID {
					found = true
					if tc.omitted {
						require.Empty(t, row.Output)
					} else {
						require.Equal(t, "display answer", row.Output)
					}
				}
			}
			require.True(t, found)
			full, err := repo.GetByID(ctx, ex.ID)
			require.NoError(t, err)
			require.Equal(t, "display answer", full.Output)
		})
	}
}

func TestCompletionsHistoryKeepsFallbackOutput(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	original, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, format, key string
		count             int
		status            models.ExecutionStatus
		omitted           bool
	}{
		{"native", "array", "", 1, models.ExecCompleted, true},
		{"failed", "array", "", 1, models.ExecFailed, false},
		{"cancelled", "array", "", 1, models.ExecCancelled, false},
		{"empty", "array", "", 0, models.ExecCompleted, false},
		{"anthropic", "envelope", "messages", 1, models.ExecCompleted, false},
		{"responses", "envelope", "responses_input", 1, models.ExecCompleted, false},
		{"legacy", "none", "", 0, models.ExecCompleted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ex := &models.Execution{TaskID: original.TaskID, Status: models.ExecRunning}
			require.NoError(t, repo.Create(ctx, ex))
			items := []any{}
			if tc.count > 0 {
				items = append(items, map[string]any{"role": "assistant", "content": "answer"})
			}
			require.NoError(t, transcript.NewRecorder(repo, models.ConversationOwner{ExecutionID: ex.ID}).Save(ctx, transcript.ReplayHeader{Format: tc.format, ItemsKey: tc.key}, items))
			require.NoError(t, repo.Complete(ctx, ex.ID, tc.status, "display answer", "", 0, 0))
			rows, err := repo.ListTaskCompletionsHistory(ctx, original.TaskID, 100)
			require.NoError(t, err)
			found := false
			for _, row := range rows {
				if row.ID == ex.ID {
					found = true
					if tc.omitted {
						require.Empty(t, row.Output)
					} else {
						require.Equal(t, "display answer", row.Output)
					}
				}
			}
			require.True(t, found)
			full, err := repo.GetByID(ctx, ex.ID)
			require.NoError(t, err)
			require.Equal(t, "display answer", full.Output)
		})
	}
}

func TestStreamOffsetLookupUsesCoveringIndex(t *testing.T) {
	db, _, _ := conversationTestRepo(t)
	rows, err := db.Query(`EXPLAIN QUERY PLAN SELECT end_offset FROM execution_stream_text WHERE execution_id=? ORDER BY end_offset DESC LIMIT 1`, "test")
	require.NoError(t, err)
	defer rows.Close()
	found := false
	for rows.Next() {
		var a, b, c int
		var detail string
		require.NoError(t, rows.Scan(&a, &b, &c, &detail))
		found = found || strings.Contains(detail, "COVERING INDEX execution_stream_offsets")
	}
	require.NoError(t, rows.Err())
	require.True(t, found)
}
