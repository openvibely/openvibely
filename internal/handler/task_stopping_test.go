package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestTaskRestartWhileStopping(t *testing.T) {
	for _, stop := range []string{"stop", "move to backlog"} {
		for _, action := range []string{"run", "category", "active lane", "batch active lane", "status"} {
			t.Run(stop+"/"+action, func(t *testing.T) {
				h, e, configs, stores := setupTestHandlerWithStores(t)
				goals := service.NewTaskGoalService(stores.TaskGoals, stores.Tasks, nil)
				h.SetTaskGoalService(goals)
				h.taskSvc.SetTaskGoalService(goals)
				ctx := context.Background()
				agent := createAgent(t, configs)
				task := createTask(t, h, "default", "Stopping follow-up", func(task *models.Task) {
					task.Category = models.CategoryActive
					task.Status = models.StatusRunning
					task.AgentID = &agent.ID
					task.Prompt = "original prompt"
				})
				execution := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
					ex.Status = models.ExecRunning
					ex.IsFollowup = true
					ex.PromptSent = "latest follow-up"
				})
				_, err := h.taskGoalSvc.SetGoal(ctx, task.ID, "Finish the latest follow-up", service.GoalOptions{})
				require.NoError(t, err)
				runningCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				registration := h.workerSvc.RegisterCancelOwned(task.ID, cancel)
				defer h.workerSvc.DeregisterCancelOwned(task.ID, registration)
				if stop == "stop" {
					stopResponse := httptest.NewRecorder()
					e.ServeHTTP(stopResponse, httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/cancel?project_id=default", nil))
					require.Less(t, stopResponse.Code, 400, stopResponse.Body.String())
				} else {
					require.NoError(t, h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryBacklog))
				}
				require.ErrorIs(t, runningCtx.Err(), context.Canceled)
				require.True(t, h.workerSvc.IsTaskStopping(task.ID))

				request := func() *httptest.ResponseRecorder {
					method, path := http.MethodPost, fmt.Sprintf("/tasks/%s/run", task.ID)
					form := url.Values{"project_id": {"default"}}
					if action != "run" {
						method, path = http.MethodPatch, fmt.Sprintf("/tasks/%s/category", task.ID)
						form.Set("category", "active")
						if action != "category" {
							form.Set("target_status", "running")
						}
						if action == "batch active lane" {
							path = "/tasks/batch-category"
							form.Set("task_ids", task.ID)
						}
					}
					if action == "status" {
						path = fmt.Sprintf("/tasks/%s/status", task.ID)
						form.Set("status", "running")
					}
					req := httptest.NewRequest(method, path+"?project_id=default", strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.Header.Set("HX-Request", "true")
					rec := httptest.NewRecorder()
					e.ServeHTTP(rec, req)
					return rec
				}
				for i := 0; i < 2; i++ {
					rec := request()
					require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
					var triggers map[string]struct {
						Message string `json:"message"`
						Status  string `json:"status"`
					}
					require.NoError(t, json.Unmarshal([]byte(rec.Header().Get("HX-Trigger")), &triggers))
					require.Equal(t, service.ErrTaskStillStopping.Error(), triggers["openvibelyToast"].Message)
					require.Equal(t, "failed", triggers["openvibelyToast"].Status)
					require.Equal(t, "none", rec.Header().Get("HX-Reswap"))
					require.True(t, h.workerSvc.IsTaskStopping(task.ID))
					goal, err := h.taskGoalSvc.GetGoal(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, models.TaskGoalStatusPaused, goal.Status)
					require.Equal(t, service.TaskGoalStoppedByUserReason, goal.Reason)
					stored, err := h.taskRepo.GetByID(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, models.CategoryBacklog, stored.Category)
					require.Equal(t, models.StatusCancelled, stored.Status)
					execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
					require.NoError(t, err)
					require.Len(t, execs, 1)
				}

				// Finish the delayed execution cleanup, then repeat the same user action.
				h.completeWithCancellation(execution.ID, task.ID, "partial progress", 0, 1, 0)
				h.workerSvc.DeregisterCancelOwned(task.ID, registration)
				require.False(t, h.workerSvc.IsTaskStopping(task.ID))
				mock := testutil.NewMockLLMCaller()
				mock.Response, mock.TextOnly = "finished", "finished"
				h.llmSvc.SetLLMCaller(mock)
				rec := request()
				require.NotContains(t, rec.Body.String(), service.ErrTaskStillStopping.Error())
				require.Eventually(t, func() bool {
					execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
					return err == nil && len(execs) == 2 && execs[1].Status == models.ExecCompleted
				}, 3*time.Second, 10*time.Millisecond)
				require.Equal(t, 1, mock.CallCount())
				require.Equal(t, "latest follow-up", mock.LastCall().Prompt)
				goal, err := h.taskGoalSvc.GetGoal(ctx, task.ID)
				require.NoError(t, err)
				require.Equal(t, models.TaskGoalStatusActive, goal.Status)
			})
		}
	}
}

func TestTaskActivationDoesNotClearNewerStop(t *testing.T) {
	for _, action := range []string{"run", "category"} {
		for _, hookName := range []string{"queued", "retry"} {
			for _, cleanupFinished := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/cleanup=%t", action, hookName, cleanupFinished), func(t *testing.T) {
					h, _, configs, stores := setupTestHandlerWithStores(t)
					goals := service.NewTaskGoalService(stores.TaskGoals, stores.Tasks, nil)
					h.taskSvc.SetTaskGoalService(goals)
					ctx := context.Background()
					agent := createAgent(t, configs)
					task := createTask(t, h, "default", "Stop after follow-up check", func(task *models.Task) {
						task.Category = models.CategoryBacklog
						if action == "run" {
							task.Category = models.CategoryActive
						}
						task.Status = models.StatusRunning
						task.AgentID = &agent.ID
					})
					createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
						exec.Status = models.ExecRunning
						exec.IsFollowup = true
						exec.PromptSent = "latest follow-up"
					})
					_, err := goals.SetGoal(ctx, task.ID, "Finish the follow-up", service.GoalOptions{})
					require.NoError(t, err)
					runningCtx, cancel := context.WithCancel(ctx)
					defer cancel()
					registration := h.workerSvc.RegisterCancelOwned(task.ID, cancel)
					defer h.workerSvc.DeregisterCancelOwned(task.ID, registration)
					entered, release := make(chan struct{}), make(chan struct{})
					hook := h.StartPendingTaskThreadFollowup
					if hookName == "retry" {
						hook = h.RetryLatestFailedTaskThreadFollowup
					}
					pausedHook := func(ctx context.Context, id string) (bool, error) {
						handled, err := hook(ctx, id)
						close(entered)
						<-release
						return handled, err
					}
					if hookName == "queued" {
						h.taskSvc.SetQueuedTaskThreadFollowupHook(pausedHook)
					} else {
						h.taskSvc.SetQueuedTaskThreadFollowupHook(nil)
						h.taskSvc.SetFailedTaskThreadFollowupRetryHook(pausedHook)
					}
					done := make(chan error, 1)
					go func() {
						if action == "run" {
							done <- h.taskSvc.RunTask(ctx, task.ID)
						} else {
							done <- h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryActive)
						}
					}()
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("activation did not finish its follow-up check")
					}
					require.NoError(t, h.taskSvc.CancelTask(ctx, task.ID))
					require.ErrorIs(t, runningCtx.Err(), context.Canceled)
					require.True(t, h.workerSvc.IsTaskStopping(task.ID))
					if cleanupFinished {
						h.workerSvc.DeregisterCancelOwned(task.ID, registration)
					}
					close(release)
					require.NoError(t, <-done)
					require.True(t, h.workerSvc.IsCancellationRequested(task.ID), "an older activation must not clear a newer Stop")
					goal, err := goals.GetGoal(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, models.TaskGoalStatusPaused, goal.Status, "an older activation must not undo a newer Stop's goal pause")
					if !cleanupFinished {
						_, err = h.StartPendingTaskThreadFollowup(ctx, task.ID)
						require.ErrorIs(t, err, service.ErrTaskStillStopping)
					}
				})
			}
		}
	}
}
