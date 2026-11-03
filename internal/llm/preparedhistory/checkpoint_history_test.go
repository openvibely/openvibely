package preparedhistory

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"sync"
	"sync/atomic"
	"testing"
)

func TestCombinedCheckpointReuse(t *testing.T) {
	history := []models.Execution{{ID: "a", PromptSent: "first"}, {ID: "b", PromptSent: "second"}}
	scope := NewReusableState()
	scope.BindHistory(history)
	calls := 0
	build := func(h []models.Execution) string { calls++; return h[0].PromptSent + h[1].PromptSent }
	for range 3 {
		got, _, n := ReadCheckpointHistory(scope.Context(context.Background()), append(append([]models.Execution(nil), history...), models.Execution{PromptSent: "new"}), build)
		require.Equal(t, "firstsecond", got)
		require.Equal(t, 2, n)
	}
	require.Equal(t, 1, calls)
	wrapped := append([]models.Execution{{PromptSent: "fresh environment"}}, history...)
	_, start, count := ReadCheckpointHistory(scope.Context(context.Background()), wrapped, build)
	require.Equal(t, 1, start)
	require.Equal(t, 2, count)
	require.Equal(t, 1, calls)
	relabeled := append([]models.Execution(nil), history...)
	for i := range relabeled {
		relabeled[i].ID = ""
		relabeled[i].ContextKind = "user"
	}
	_, _, count = ReadCheckpointHistory(scope.Context(context.Background()), relabeled, build)
	require.Equal(t, 2, count)
	require.Equal(t, 1, calls)
	changed := append([]models.Execution(nil), history...)
	changed[0].PromptSent = "edited"
	for _, h := range [][]models.Execution{changed, history[1:], nil} {
		_, _, n := ReadCheckpointHistory(scope.Context(context.Background()), h, build)
		require.Zero(t, n)
	}
	// A new checkpoint owner cannot reuse an earlier revision's decoded value.
	next := NewReusableState()
	next.BindHistory(changed)
	got, _, n := ReadCheckpointHistory(next.Context(context.Background()), changed, build)
	require.Equal(t, "editedsecond", got)
	require.Equal(t, 2, n)
	require.Equal(t, 2, calls)
}

func TestCombinedCheckpointConcurrentReaders(t *testing.T) {
	history := []models.Execution{{PromptSent: "saved"}}
	scope := NewReusableState()
	scope.BindHistory(history)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			got, _, n := ReadCheckpointHistory(scope.Context(context.Background()), history, func(h []models.Execution) string { calls.Add(1); return h[0].PromptSent })
			if got != "saved" || n != 1 {
				t.Errorf("value=%q count=%d", got, n)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int32(1), calls.Load())
}
