package handler

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

type replayCountingStore struct {
	persistence.ExecutionStore
	reads  int
	output string
	status models.ExecutionStatus
}

func (s *replayCountingStore) ReadOutputAfter(_ context.Context, _ string, offset int) (*persistence.ExecutionReplay, error) {
	s.reads++
	start := min(offset, len(s.output))
	return &persistence.ExecutionReplay{Output: s.output[start:], Start: start, Total: len(s.output), Status: s.status}, nil
}
func TestExecutionStreamNoPollingAndPersistedRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &replayCountingStore{status: models.ExecRunning}
		hub := events.NewExecutionStreamHub()
		h := &Handler{execRepo: store, executionStreamHub: hub}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		frames := make(chan liveFrame, 20)
		go h.streamExecution(ctx, liveCommand{ExecID: "exec"}, func(f liveFrame) bool { frames <- f; return true })
		synctest.Wait()
		require.Equal(t, 1, store.reads)
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, 1, store.reads, "idle stream must not poll")
		hub.Publish(events.ExecutionStreamEvent{ExecID: "exec", Type: events.ExecutionStreamDelta, Delta: "hello", Offset: 5})
		synctest.Wait()
		require.Equal(t, "hello", (<-frames).Data)
		hub.Publish(events.ExecutionStreamEvent{ExecID: "exec", Type: events.ExecutionStreamPersisted, Offset: 5})
		synctest.Wait()
		require.Equal(t, 1, store.reads, "caught-up persistence signals must not read")
		// Persistence can catch up after reconnect, with no subsequent token.
		store.output = "hello missed"
		hub.Publish(events.ExecutionStreamEvent{ExecID: "exec", Type: events.ExecutionStreamPersisted, Offset: 12})
		synctest.Wait()
		require.Equal(t, " missed", (<-frames).Data)
		require.Equal(t, 2, store.reads)
		store.status = models.ExecCompleted
		hub.CloseTerminal("exec", models.ExecCompleted, "")
		synctest.Wait()
		require.Equal(t, "done", (<-frames).Event)
		require.Equal(t, 3, store.reads)
	})
}
