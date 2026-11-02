package sqlite

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestReplayContinuationSelectsSnapshotAndSuffix(t *testing.T) {
	_, repo, owner := conversationTestRepo(t)
	ctx := context.Background()
	first, err := repo.GetByID(ctx, owner.ExecutionID)
	require.NoError(t, err)
	ids := []string{first.ID}
	for range 3 {
		e := models.Execution{TaskID: first.TaskID, Status: models.ExecCompleted}
		require.NoError(t, repo.Create(ctx, &e))
		ids = append(ids, e.ID)
	}
	delta := models.ExecutionReplayMessage{TranscriptJSON: `[{"role":"user","content":"delta"}]`}
	snapshot := models.ExecutionReplayMessage{TranscriptJSON: `{"responses_input":[{"role":"user","content":"complete"}]}`}
	for _, id := range ids {
		require.NoError(t, repo.ReplaceReasoningReplay(ctx, id, "", []models.ExecutionReplayMessage{delta}))
	}
	// Repeated snapshots within an execution also supersede earlier frames.
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, ids[2], "", []models.ExecutionReplayMessage{snapshot, delta, snapshot, delta}))
	got, err := repo.ReplayContinuationRowsByExecutionIDs(ctx, ids)
	require.NoError(t, err)
	require.Empty(t, got[ids[0]])
	require.Empty(t, got[ids[1]])
	require.Len(t, got[ids[2]], 2)
	require.True(t, got[ids[2]][0].HasResponsesInput())
	require.Len(t, got[ids[3]], 1)
	require.NotNil(t, got[ids[3]][0].Rows)
	// Legacy/full reads still retain all frames for explicit recovery callers.
	all, err := repo.ReplayRowsByExecutionIDs(ctx, ids)
	require.NoError(t, err)
	require.Len(t, all[ids[2]], 4)
	require.Len(t, all[ids[0]], 1)
	// An empty snapshot cannot replace previous exchanges.
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, ids[2], "", []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[]}`}}))
	got, err = repo.ReplayContinuationRowsByExecutionIDs(ctx, ids)
	require.NoError(t, err)
	require.Len(t, got[ids[0]], 1)
	require.Len(t, got[ids[1]], 1)
	// Caller order, not execution IDs or query order, defines continuation order.
	require.NoError(t, repo.ReplaceReasoningReplay(ctx, ids[0], "", []models.ExecutionReplayMessage{snapshot}))
	got, err = repo.ReplayContinuationRowsByExecutionIDs(ctx, []string{ids[3], ids[2], ids[1], ids[0]})
	require.NoError(t, err)
	require.Empty(t, got[ids[3]])
	require.Empty(t, got[ids[1]])
	require.Len(t, got[ids[0]], 1)
	require.JSONEq(t, `{"role":"user","content":"complete"}`, string(got[ids[0]][0].Rows.Items[0]))
}
