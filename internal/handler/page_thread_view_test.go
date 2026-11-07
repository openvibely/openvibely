package handler

import (
	"net/http"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"

	"github.com/stretchr/testify/require"
)

func TestPageUsesTaskThreadViewAndAuthorizedReaders(t *testing.T) {
	env := setupPluginPages(t)
	// Empty pages need an identity before mounting the existing thread runtime.
	env.openPage(t, env.inA)
	base := pluginAPI(env.inA.ID, env.projA.ID, "/chat/thread")
	first := env.do(t, http.MethodGet, base, "", nil)
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	require.Contains(t, first.Body.String(), `id="task-thread-view"`)
	require.Contains(t, first.Body.String(), `id="task-thread-runtime-`)
	require.Contains(t, first.Body.String(), `data-thread-api-base="/api/plugin-pages/`+env.inA.ID+`/chat/thread"`)
	require.Contains(t, first.Body.String(), `data-thread-project-id="`+env.projA.ID+`"`)
	require.Contains(t, first.Body.String(), `window.reconcileTaskThreadState`)
	require.NotContains(t, first.Body.String(), `controller.mount`)
	sendPageTest(t, env, env.inA, "reader owner")
	exec := env.starts[0]
	for _, suffix := range []string{"/chat/thread", "/chat/thread/pending-inputs", "/chat/thread/composer-action", "/chat/thread/executions/" + exec.ExecID + "/fragment"} {
		rec := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projA.ID, suffix), "", nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		wrong := env.do(t, http.MethodGet, pluginAPI(env.inA.ID, env.projB.ID, suffix), "", nil)
		require.Equal(t, http.StatusNotFound, wrong.Code)
	}
	other := env.do(t, http.MethodGet, pluginAPI(env.inB.ID, env.projB.ID, "/chat/thread/executions/"+exec.ExecID+"/fragment"), "", nil)
	require.Equal(t, http.StatusNotFound, other.Code)
	poll := env.do(t, http.MethodGet, base+"&poll=1&preserved_exec_ids="+exec.ExecID, "", nil)
	require.Equal(t, http.StatusOK, poll.Code)
	require.Contains(t, poll.Body.String(), `hx-preserve="true"`)
	require.NotContains(t, poll.Body.String(), `window.reconcileTaskThreadState =`)
	require.NoError(t, env.h.pagePlugins.Disable(t.Context(), env.inA.ID))
	denied := env.do(t, http.MethodGet, base, "", nil)
	require.Equal(t, http.StatusNotFound, denied.Code)
}

func TestTaskThreadSendRejectsPageConversations(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		name := "enabled"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			env := setupPluginPages(t)
			env.openPage(t, env.inA)
			taskID, err := env.mgr.Store().GetConversationTask(t.Context(), env.inA.ID)
			require.NoError(t, err)
			require.NotEmpty(t, taskID)
			if disabled {
				require.NoError(t, env.mgr.Disable(t.Context(), env.inA.ID))
			}
			before, err := env.h.taskRepo.GetByID(t.Context(), taskID)
			require.NoError(t, err)
			rec := env.do(t, http.MethodPost, "/tasks/"+taskID+"/thread?project_id="+env.projA.ID, "message=bypass&agent_id="+env.agent.ID, map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
			require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
			require.Empty(t, env.starts, "must not launch a coding response")
			after, err := env.h.taskRepo.GetByID(t.Context(), taskID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryPageChat, after.Category)
			require.Equal(t, before.Status, after.Status)
			require.Equal(t, before.AgentID, after.AgentID)
			executions, err := env.h.execRepo.ListByTaskChronological(t.Context(), taskID)
			require.NoError(t, err)
			require.Empty(t, executions)
			pending, err := env.h.threadInputRepo.ListPendingForTask(t.Context(), taskID)
			require.NoError(t, err)
			require.Empty(t, pending)
			if !disabled {
				sendPageTest(t, env, env.inA, "authorized page message")
				require.Len(t, env.starts, 1)
				require.NotNil(t, env.starts[0].PageConversation)
			}
		})
	}
}

// The durable binding must protect even conversations reclassified by old code.
func TestPageBindingRejectsAllRawTaskRoutes(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryPageChat, models.CategoryBacklog} {
		t.Run(string(category), func(t *testing.T) {
			env := setupPluginPages(t)
			env.openPage(t, env.inA)
			id, err := env.mgr.Store().GetConversationTask(t.Context(), env.inA.ID)
			require.NoError(t, err)
			require.NoError(t, env.mgr.Disable(t.Context(), env.inA.ID))
			require.NoError(t, env.h.taskRepo.UpdateCategory(t.Context(), id, category))
			count := 0
			for _, route := range env.e.Routes() {
				if route.Method == "echo_route_not_found" || (!strings.HasPrefix(route.Path, "/tasks/:taskId") && !strings.HasPrefix(route.Path, "/api/tasks/:id/")) {
					continue
				}
				count++
				path := strings.NewReplacer(":taskId", id, ":id", id, ":execId", "exec", ":executionID", "exec", ":inputId", "input").Replace(route.Path)
				for _, query := range []string{"", "?project_id=" + env.projA.ID} {
					rec := env.do(t, route.Method, path+query, "category=backlog&message=bypass", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
					require.Equal(t, http.StatusNotFound, rec.Code, "%s %s: %s", route.Method, path+query, rec.Body.String())
				}
			}
			require.Greater(t, count, 40)
			ordinary := &models.Task{ProjectID: env.projA.ID, Title: "ordinary task", Prompt: "ordinary task", Category: models.CategoryBacklog, Status: models.StatusPending}
			require.NoError(t, env.h.taskRepo.Create(t.Context(), ordinary))
			batch := env.do(t, http.MethodPatch, "/tasks/batch-category", "project_id="+env.projA.ID+"&task_ids="+ordinary.ID+","+id+"&category=completed", map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
			require.Equal(t, http.StatusBadRequest, batch.Code, batch.Body.String())
			unchanged, err := env.h.taskRepo.GetByID(t.Context(), ordinary.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, unchanged.Category, "reject the entire batch before changing ordinary tasks")
			// Service callers and bulk preflight must use the same durable ownership.
			require.Error(t, env.h.taskSvc.UpdateCategory(t.Context(), id, models.CategoryBacklog))
			require.Error(t, env.h.taskSvc.RunTask(t.Context(), id))
			require.Error(t, env.h.taskSvc.ValidateTaskIDsForProject(t.Context(), env.projA.ID, []string{id}))
			task, err := env.h.taskRepo.GetByID(t.Context(), id)
			require.NoError(t, err)
			require.Error(t, env.h.taskSvc.Update(t.Context(), task))
			require.Error(t, env.h.taskSvc.UpdatePreservingTitle(t.Context(), task))
			require.Equal(t, category, task.Category)
			require.Empty(t, env.starts)
		})
	}
}
