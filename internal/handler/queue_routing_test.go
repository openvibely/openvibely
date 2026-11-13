package handler

import (
	"net/http"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

// Every entry point must select the same runner, including callers that only
// have a durable task ID and cannot supply an in-memory page binding.
func TestQueueRoutingAcrossConversations(t *testing.T) {
	for _, kind := range []string{"chat", "task", "page"} {
		for _, action := range []string{"save", "close", "expire", "complete", "recover"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				env := setupPluginPages(t)
				ctx := t.Context()
				started := make(chan streamingResponseParams, 4)
				env.h.startStreamingResponseOverride = func(p streamingResponseParams) error { started <- p; return nil }
				var first streamingResponseParams
				var input *models.ThreadInput
				if kind == "page" {
					handle := env.openPage(t, env.inA)
					response, _ := env.bridge(t, env.inA, env.projA.ID, handle, "context.set", `{"summary":"saved page context"}`)
					require.Equal(t, http.StatusOK, response.Code)
					sendPageTest(t, env, env.inA, "first")
					first = <-started
					sendPageTest(t, env, env.inA, "original")
					pending, err := env.h.threadInputRepo.ListPendingForTask(ctx, first.TaskID)
					require.NoError(t, err)
					require.Len(t, pending, 1)
					input = &pending[0]
				} else {
					category := models.CategoryActive
					scope := models.ThreadInputScopeTask
					if kind == "chat" {
						category = models.CategoryChat
						scope = models.ThreadInputScopeChat
					}
					task := &models.Task{ProjectID: env.projA.ID, Title: "routing", Prompt: "first", Category: category, Status: models.StatusRunning, AgentID: &env.agent.ID}
					require.NoError(t, env.h.taskRepo.Create(ctx, task))
					execution := &models.Execution{TaskID: task.ID, AgentConfigID: env.agent.ID, Status: models.ExecRunning, PromptSent: "first", IsFollowup: kind == "task"}
					require.NoError(t, env.h.execRepo.Create(ctx, execution))
					first = streamingResponseParams{ProjectID: task.ProjectID, TaskID: task.ID, ExecID: execution.ID, IsTaskFollowup: kind == "task"}
					input = &models.ThreadInput{Scope: scope, ProjectID: task.ProjectID, TaskID: task.ID, AgentConfigID: env.agent.ID, Content: "original"}
					require.NoError(t, env.h.threadInputRepo.CreateQueued(ctx, input))
				}
				endpoint := "/thread-inputs/" + input.ID
				headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
				editing := action == "save" || action == "close" || action == "expire"
				if editing {
					response := env.do(t, http.MethodPost, endpoint+"/edit-hold", "", headers)
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				}
				require.NoError(t, env.h.execRepo.Complete(ctx, first.ExecID, models.ExecCompleted, "done", "", 0, 0))
				require.NoError(t, env.h.taskRepo.UpdateStatus(ctx, first.TaskID, models.StatusCompleted))
				if editing {
					env.h.startNextQueuedTurnAfter(ctx, first, "")
					stored, err := env.h.threadInputRepo.GetByID(ctx, input.ID)
					require.NoError(t, err)
					require.Equal(t, models.ThreadInputPending, stored.InputStatus, "editing must hold the head of the queue")
				}
				want := "original"
				switch action {
				case "save":
					want = "edited"
					response := env.do(t, http.MethodPost, endpoint+"/edit", "content=edited&resume=true", headers)
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				case "close":
					response := env.do(t, http.MethodPost, endpoint+"/edit-hold", "hold=false", headers)
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
				case "expire":
					require.NoError(t, env.stores.Fixtures.SetInputEditHoldUntil(ctx, input.ID, time.Now().UTC().Add(-time.Second)))
					env.h.RecoverExpiredInputEdits(ctx)
				case "complete":
					env.h.startNextQueuedTurnAfter(ctx, first, "")
				case "recover":
					env.h.RecoverQueuedInputs(ctx)
				}
				require.Eventually(t, func() bool {
					stored, err := env.h.threadInputRepo.GetByID(ctx, input.ID)
					return err == nil && stored.InputStatus == models.ThreadInputApplied
				}, 5*time.Second, 10*time.Millisecond)
				stored, err := env.h.threadInputRepo.GetByID(ctx, input.ID)
				require.NoError(t, err)
				execution, err := env.h.execRepo.GetByID(ctx, stored.RunExecutionID)
				require.NoError(t, err)
				require.Equal(t, want, execution.PromptSent)
				select {
				case next := <-started:
					require.Equal(t, want, next.Message)
					require.Equal(t, kind == "page", next.PageConversation != nil)
					require.Equal(t, kind == "task", next.IsTaskFollowup)
					if kind == "page" {
						require.Equal(t, first.TaskID, next.TaskID)
						require.Contains(t, next.SystemContext, "saved page context")
						require.NotNil(t, next.RuntimeTools)
						task, err := env.h.taskRepo.GetByID(ctx, next.TaskID)
						require.NoError(t, err)
						require.Equal(t, models.CategoryPageChat, task.Category)
					}
					if next.updateWorkDone != nil {
						next.updateWorkDone()
					}
				case <-time.After(5 * time.Second):
					t.Fatal("queue was claimed without starting the conversation runner")
				}
				if kind == "task" {
					require.Equal(t, models.ExecQueued, execution.Status, "task queue must still use worker admission")
				}
			})
		}
	}
}
