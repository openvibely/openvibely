package preparedhistory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestReplayProjectionCachesOnlyBoundHistory(t *testing.T) {
	replay := models.ReplayFromRows("", nil)
	replay.TranscriptJSON = `["saved"]`
	scope := NewReusableState()
	scope.BindHistory([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{replay}}})
	calls := 0
	decode := func(r models.ExecutionReplayMessage) (string, error) { calls++; return r.FullTranscriptJSON(), nil }
	for range 3 {
		_, err := ReadReplayProjection(scope.Context(context.Background()), replay, decode)
		require.NoError(t, err)
	}
	require.Equal(t, 1, calls)
	changed := replay
	changed.TranscriptJSON = `["replacement"]`
	for range 2 {
		_, err := ReadReplayProjection(scope.Context(context.Background()), changed, decode)
		require.NoError(t, err)
	}
	require.Equal(t, 3, calls)
	// Errors are retried; a different projection type has an independent key.
	for range 2 {
		_, err := ReadReplayProjection(scope.Context(context.Background()), replay, func(models.ExecutionReplayMessage) ([]string, error) { calls++; return nil, errors.New("invalid") })
		require.Error(t, err)
	}
	require.Equal(t, 5, calls)
}

func TestReplayProjectionConcurrentReaders(t *testing.T) {
	replay := models.ExecutionReplayMessage{TranscriptJSON: `["saved"]`}
	scope := NewReusableState()
	scope.BindHistory([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{replay}}})
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			got, err := ReadReplayProjection(scope.Context(context.Background()), replay, func(models.ExecutionReplayMessage) (string, error) { calls.Add(1); return "saved", nil })
			if err != nil || got != "saved" {
				t.Errorf("value=%q error=%v", got, err)
			}
		})
	}
	wg.Wait()
	require.Equal(t, int32(1), calls.Load())
}
