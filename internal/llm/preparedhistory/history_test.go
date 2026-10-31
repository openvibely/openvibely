package preparedhistory

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestHistoryOwnershipAndInvalidation(t *testing.T) {
	ctx := WithScope(context.Background())
	history := []models.Execution{{Output: "one", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: "old"}}}}
	builds := 0
	build := func(h []models.Execution) []string {
		builds++
		return []string{h[0].Output, h[0].ReplayMessages[0].TranscriptJSON}
	}
	require.Equal(t, []string{"one", "old"}, Read(ctx, history, build))
	require.Equal(t, []string{"one", "old"}, Read(ctx, history, build))
	require.Equal(t, 1, builds)
	history[0].ReplayMessages[0].TranscriptJSON = "new"
	require.Equal(t, []string{"one", "new"}, Read(ctx, history, build))
	require.Equal(t, 2, builds)
	taken := Take(ctx, history, build)
	require.Equal(t, 2, builds)
	taken[0] = "sender mutation"
	require.Equal(t, []string{"one", "new"}, Read(ctx, history, build))
	require.Equal(t, 3, builds)
	history[0].Output = "compacted"
	require.Equal(t, "compacted", Take(ctx, history, build)[0])
	require.Equal(t, 4, builds)
	Read(WithScope(ctx), history, build)
	require.Equal(t, 5, builds)
}

func TestEmptyBudgetDoesNotEvictHistory(t *testing.T) {
	ctx := WithScope(context.Background())
	calls := 0
	build := func(h []models.Execution) []string {
		if len(h) == 0 {
			return nil
		}
		calls++
		return []string{h[0].Output}
	}
	history := []models.Execution{{Output: "history"}}
	Read(ctx, history, build)
	Read(ctx, nil, build)
	Take(ctx, history, build)
	require.Equal(t, 1, calls)
}

func TestEmptySendReleasesPreviousHistory(t *testing.T) {
	ctx := WithScope(context.Background())
	history := []models.Execution{{Output: "before compaction"}}
	calls := 0
	build := func(h []models.Execution) []string {
		if len(h) == 0 {
			return nil
		}
		calls++
		return []string{h[0].Output}
	}
	Read(ctx, history, build)
	Take(ctx, nil, build)
	Read(ctx, history, build)
	require.Equal(t, 2, calls, "an empty send must release cached pre-compaction history")
}
