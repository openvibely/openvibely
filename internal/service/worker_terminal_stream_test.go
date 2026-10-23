package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

type panicTerminalStore struct {
	persistence.ExecutionStore
	execution   models.Execution
	completeErr error
}

func (s *panicTerminalStore) ListByTask(context.Context, string) ([]models.Execution, error) {
	return []models.Execution{s.execution}, nil
}
func (s *panicTerminalStore) Complete(_ context.Context, _ string, status models.ExecutionStatus, _ string, _ string, _ int, _ int64) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	s.execution.Status = status
	return nil
}
func TestPanicCleanupNotifiesExecutionSubscriber(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		name := "saved"
		if failWrite {
			name = "write failure"
		}
		t.Run(name, func(t *testing.T) {
			store := &panicTerminalStore{execution: models.Execution{ID: "exec", Status: models.ExecRunning, StartedAt: time.Now()}}
			if failWrite {
				store.completeErr = errors.New("write failed")
			}
			hub := events.NewExecutionStreamHub()
			sub, unsubscribe, err := hub.Subscribe("exec")
			require.NoError(t, err)
			defer unsubscribe()
			w := &WorkerService{execRepo: store, llmSvc: &LLMService{executionStreamHub: hub}}
			w.failRunningExecutionsAfterPanic(context.Background(), "task", errors.New("provider panic"))
			if failWrite {
				require.Equal(t, models.ExecRunning, store.execution.Status)
				select {
				case ev := <-sub:
					t.Fatalf("unexpected terminal event on failed write: %+v", ev)
				default:
				}
				return
			}
			require.Equal(t, models.ExecFailed, store.execution.Status)
			select {
			case ev, ok := <-sub:
				require.True(t, ok)
				require.Equal(t, events.ExecutionStreamError, ev.Type)
				require.Equal(t, "provider panic", ev.Error)
			default:
				t.Fatal("persisted failure did not notify connected execution subscriber")
			}
			select {
			case _, ok := <-sub:
				require.False(t, ok, "terminal subscription must close")
			default:
				t.Fatal("terminal subscription remained open")
			}
		})
	}
}
