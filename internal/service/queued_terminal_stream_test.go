package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestQueuedCancellationNotifiesOnlyChangedExecutions(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "explicit"
		if stale {
			name = "stale queue"
		}
		t.Run(name, func(t *testing.T) {
			env := teststores.New(t)
			ctx := context.Background()
			task := &models.Task{ProjectID: "default", Title: "queued cancellation", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.NoError(t, env.Tasks.Create(ctx, task))
			hub := events.NewExecutionStreamHub()
			var queuedIDs []string
			var checks []func()
			for _, status := range []models.ExecutionStatus{models.ExecQueued, models.ExecQueued, models.ExecRunning, models.ExecCompleted} {
				ex := &models.Execution{TaskID: task.ID, Status: status}
				require.NoError(t, env.Executions.Create(ctx, ex))
				sub, closeSub, err := hub.Subscribe(ex.ID)
				require.NoError(t, err)
				defer closeSub()
				if status == models.ExecQueued {
					queuedIDs = append(queuedIDs, ex.ID)
				}
				checks = append(checks, func() {
					select {
					case ev, ok := <-sub:
						require.Equal(t, models.ExecQueued, status, "unchanged execution must not be notified")
						require.True(t, ok)
						require.Equal(t, events.ExecutionStreamDone, ev.Type)
						require.Equal(t, string(models.ExecCancelled), ev.Status)
					default:
						require.NotEqual(t, models.ExecQueued, status, "cancelled execution subscriber is still waiting")
					}
				})
			}
			w := &WorkerService{execRepo: env.Executions, taskRepo: env.Tasks, llmSvc: &LLMService{executionStreamHub: hub}, queue: []models.Task{*task}, pending: map[string]bool{task.ID: true}, reserved: map[string]string{task.ID: queuedIDs[0]}}
			if stale {
				w.PruneQueuedWork()
			} else {
				require.NoError(t, w.CancelQueuedTask(ctx, task.ID, "cancelled"))
			}
			for _, id := range queuedIDs {
				ex, err := env.Executions.GetByID(ctx, id)
				require.NoError(t, err)
				require.Equal(t, models.ExecCancelled, ex.Status)
			}
			require.Empty(t, w.queue)
			for _, check := range checks {
				check()
			}
		})
	}
}

type queuedAutomationCancellationStore struct {
	persistence.AutomationStore
	ids []string
	err error
}

func (s *queuedAutomationCancellationStore) CancelDispatchesForTaskReturningIDs(context.Context, string, string) ([]string, error) {
	return s.ids, s.err
}

func TestQueuedAutomationCancellationPublishesOnlySuccessfulWrites(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			hub := events.NewExecutionStreamHub()
			sub, closeSub, err := hub.Subscribe("prepared")
			require.NoError(t, err)
			defer closeSub()
			store := &queuedAutomationCancellationStore{ids: []string{"prepared"}}
			if fail {
				store.err = errors.New("database unavailable")
			}
			w := &WorkerService{automationRepo: store, llmSvc: &LLMService{executionStreamHub: hub}}
			err = w.CancelQueuedTask(context.Background(), "task", "cancelled")
			if fail {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			select {
			case ev, ok := <-sub:
				require.False(t, fail)
				require.True(t, ok)
				require.Equal(t, events.ExecutionStreamDone, ev.Type)
				require.Equal(t, string(models.ExecCancelled), ev.Status)
			default:
				require.True(t, fail, "prepared execution subscriber is still waiting")
			}
		})
	}
}
