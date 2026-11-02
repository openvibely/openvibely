package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestResponsesDeltaRestoresBeyondHistoryWindowAndPinsBase(t *testing.T) {
	db, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	_, err0 := db.ExecContext(ctx, `UPDATE executions SET status='completed' WHERE id=?`, owner.ExecutionID)
	require.NoError(t, err0)
	first, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	put := func(id, raw string) {
		t.Helper()
		require.NoError(t, repo.ReplaceReasoningReplay(ctx, id, "", []models.ExecutionReplayMessage{{TranscriptJSON: raw}}))
	}
	put(first.ID, `{"responses_input":[{"type":"message","role":"user","content":"original"}]}`)
	latest := first.ID
	var ids []string
	for i := 0; i < 25; i++ {
		base, err := repo.ReplayRowsByExecutionIDs(ctx, []string{latest})
		require.NoError(t, err)
		e := models.Execution{TaskID: first.TaskID, Status: models.ExecCompleted}
		require.NoError(t, repo.Create(ctx, &e))
		raw, err := json.Marshal(map[string]any{"responses_base_execution_ids": []string{latest}, "responses_base_revision": base[latest][0].Rows.Revision, "responses_delta": []any{map[string]any{"type": "message", "role": "user", "content": fmt.Sprintf("turn-%d", i)}}})
		require.NoError(t, err)
		put(e.ID, string(raw))
		latest = e.ID
		ids = append(ids, latest)
	}
	// A later summary/approval may replace an older execution's current heads.
	// Descendants must still restore the revision they actually used.
	put(first.ID, `{"responses_input":[{"type":"message","role":"user","content":"changed"}]}`)
	got, err := repo.ReplayContinuationRowsByExecutionIDs(ctx, ids[len(ids)-20:])
	require.NoError(t, err)
	for _, id := range ids[len(ids)-20 : len(ids)-1] {
		require.Empty(t, got[id])
	}
	require.Len(t, got[latest], 1)
	items := got[latest][0].Rows.Items
	require.Len(t, items, 26)
	require.Contains(t, string(items[0]), "original")
	require.Contains(t, string(items[25]), "turn-24")
	exported, err := repo.ReplayMessagesByExecutionIDs(ctx, []string{latest})
	require.NoError(t, err)
	require.Contains(t, exported[latest][0].TranscriptJSON, "original")
	require.NotContains(t, exported[latest][0].TranscriptJSON, "responses_delta")
	// New history after compaction is a new root, without an old dependency.
	put(latest, `{"responses_input":[{"type":"compaction","encrypted_content":"summary"}]}`)
	got, err = repo.ReplayRowsByExecutionIDs(ctx, []string{latest})
	require.NoError(t, err)
	require.Len(t, got[latest][0].Rows.Items, 1)
	// Deleting a task deletes its entire chain; no cross-task dependencies exist.
	_, err = db.ExecContext(ctx, `DELETE FROM tasks WHERE id=?`, first.TaskID)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_items WHERE execution_id=?`, latest).Scan(&count))
	require.Zero(t, count)
}

func TestResponsesDeltaRejectsMissingBase(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	require.NoError(t, repo.ReplaceReasoningReplay(context.Background(), owner.ExecutionID, "", []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_base_execution_ids":["missing"],"responses_base_revision":1,"responses_delta":[{"role":"user","content":"new"}]}`}}))
	_, err := repo.ReplayRowsByExecutionIDs(context.Background(), []string{owner.ExecutionID})
	require.ErrorContains(t, err, "missing or cross-task")
}

func TestResponsesDeltaBaseMayBeLastLegacyFrame(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	e, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, e.ID, "", []models.ExecutionReplayMessage{
		{TranscriptJSON: `{"responses_input":[{"role":"user","content":"obsolete"}]}`},
		{TranscriptJSON: `{"responses_input":[{"role":"user","content":"retained"}]}`},
	}))
	base, err := repo.ReplayContinuationRowsByExecutionIDs(ctx, []string{e.ID})
	require.NoError(t, err)
	require.Len(t, base[e.ID], 1)
	next := models.Execution{TaskID: e.TaskID, Status: models.ExecCompleted}
	require.NoError(t, repo.Create(ctx, &next))
	raw, err := json.Marshal(map[string]any{"responses_base_execution_ids": []string{e.ID}, "responses_base_revision": base[e.ID][0].Rows.Revision, "responses_delta": []any{map[string]any{"role": "user", "content": "next"}}})
	require.NoError(t, err)
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, next.ID, "", []models.ExecutionReplayMessage{{TranscriptJSON: string(raw)}}))
	restored, err := repo.ReplayRowsByExecutionIDs(ctx, []string{next.ID})
	require.NoError(t, err)
	require.Len(t, restored[next.ID][0].Rows.Items, 2)
	require.Contains(t, string(restored[next.ID][0].Rows.Items[0]), "retained")
}
