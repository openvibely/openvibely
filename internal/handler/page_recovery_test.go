package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func restartPageTest(t *testing.T, env *pluginTestEnv) {
	t.Helper()
	env.mgr.StopAll()
	env.mgr = pageplugins.NewManager(env.mgr.Store(), env.mgr.Root())
	t.Cleanup(env.mgr.StopAll)
	env.h.SetPagePlugins(env.mgr)
	env.h.chatInputRequests = newChatInputRequestBroker()
	_, err := env.h.taskRepo.ResetOrphanedRunning(t.Context())
	require.NoError(t, err)
	_, err = env.h.execRepo.RecoverPreRestartRunningTaskExecutions(t.Context())
	require.NoError(t, err)
	_, err = env.h.execRepo.RecoverStaleRunningTaskExecutions(t.Context())
	require.NoError(t, err)
}
func sendPageTest(t *testing.T, env *pluginTestEnv, in *models.PagePluginInstance, message string) {
	t.Helper()
	response := env.do(t, http.MethodPost, "/api/plugin-pages/"+in.ID+"/chat?project_id="+in.ProjectID, "message="+message, map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
}
func TestPageQueueRestartRecoversFIFOWithSavedContext(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	response, _ := env.bridge(t, env.inA, env.projA.ID, handle, "context.set", `{"summary":"North at submission"}`)
	require.Equal(t, 200, response.Code)
	sendPageTest(t, env, env.inA, "first")
	first := env.starts[0]
	sendPageTest(t, env, env.inA, "second")
	sendPageTest(t, env, env.inA, "third")
	sendPageTest(t, env, env.inB, "other")
	restartPageTest(t, env)
	env.h.RecoverQueuedInputs(t.Context())
	require.Len(t, env.starts, 3)
	next := env.starts[2]
	require.Equal(t, "second", next.Message)
	require.Equal(t, first.TaskID, next.TaskID)
	require.Equal(t, first.PrincipalID, next.PrincipalID)
	require.Equal(t, first.Agent.ID, next.Agent.ID)
	require.Contains(t, next.SystemContext, "North at submission")
	require.NotNil(t, next.RuntimeTools)
	require.False(t, next.IsTaskFollowup)
	require.True(t, next.TaskScopedChat)
	task, err := env.h.taskRepo.GetByID(t.Context(), first.TaskID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryPageChat, task.Category)
	require.Equal(t, models.StatusRunning, task.Status)
	env.h.RecoverQueuedInputs(t.Context())
	require.Len(t, env.starts, 3, "recovery duplicated active turn")
	require.NoError(t, env.h.execRepo.Complete(t.Context(), next.ExecID, models.ExecCompleted, "done", "", 0, 0))
	require.NoError(t, env.h.taskRepo.UpdateStatus(t.Context(), next.TaskID, models.StatusCompleted))
	env.h.startNextQueuedTurnAfter(t.Context(), next, "")
	require.Len(t, env.starts, 4)
	require.Equal(t, "third", env.starts[3].Message)
}
func TestPageQueueRestartDoesNotRunDisabledPage(t *testing.T) {
	env := setupPluginPages(t)
	sendPageTest(t, env, env.inA, "first")
	sendPageTest(t, env, env.inA, "second")
	require.NoError(t, env.mgr.Store().SetInstanceEnabled(t.Context(), env.inA.ID, false))
	restartPageTest(t, env)
	env.h.RecoverQueuedInputs(t.Context())
	require.Len(t, env.starts, 1)
}

func TestPageInstallationContinuationRetainsPageAcrossRestart(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	response, _ := env.bridge(t, env.inA, env.projA.ID, handle, "context.set", `{"summary":"North installation context"}`)
	require.Equal(t, 200, response.Code)
	sendPageTest(t, env, env.inA, "analyze")
	original := env.starts[0]
	question, err := env.h.chatInputRequests.create(env.projA.ID, original.ExecID, []chatInputRequestQuestion{{ID: "install", Question: "Install dependencies?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{}`), ChatMode: string(models.ChatModeOrchestrate)})
	require.NoError(t, err)
	require.NoError(t, env.h.persistChatInputRequest(t.Context(), question))
	record, err := env.h.chatInputRequestRepo.Resolve(t.Context(), question.ID, env.projA.ID, `[{"question_id":"install","label":"Not now"}]`)
	require.NoError(t, err)
	restartPageTest(t, env)
	require.NoError(t, env.h.startInstallationContinuation(t.Context(), *record))
	require.Len(t, env.starts, 2)
	next := env.starts[1]
	require.Equal(t, original.TaskID, next.TaskID)
	require.Equal(t, original.PrincipalID, next.PrincipalID)
	require.Contains(t, next.SystemContext, "North installation context")
	require.False(t, next.IsTaskFollowup)
	require.False(t, next.DeferHistoryLoad)
	require.True(t, next.TaskScopedChat)
	require.NotNil(t, next.PageConversation)
	require.NotNil(t, next.RuntimeTools)
	_, handled, isError, err := next.RuntimeTools.Executor(context.Background(), pageintegration.ToolName("get_report"), json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError)
	task, err := env.h.taskRepo.GetByID(t.Context(), next.TaskID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryPageChat, task.Category)
	require.Equal(t, models.StatusRunning, task.Status)
	if next.updateWorkDone != nil {
		next.updateWorkDone()
	}
	// A second restart interrupts the claimed installation continuation. Its
	// hidden conversation must never become an ordinary Backlog task.
	restartPageTest(t, env)
	task, err = env.h.taskRepo.GetByID(t.Context(), next.TaskID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryPageChat, task.Category)
	execution, err := env.h.execRepo.GetByID(t.Context(), next.ExecID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, execution.Status)
}

func TestPageQueueRecoveryRejectsRemovedSubmittingUser(t *testing.T) {
	env := setupPluginPages(t)
	resolved, err := env.mgr.Resolve(t.Context(), env.inA.ID, env.projA.ID)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/page/chat", nil)
	req = req.WithContext(models.WithPrincipal(req.Context(), models.Principal{UserID: "removed-user", Role: "member"}))
	c := env.e.NewContext(req, httptest.NewRecorder())
	_, _, err = env.h.startPluginChatTurn(c, resolved, "first", pageChatInput{})
	require.NoError(t, err)
	_, _, err = env.h.startPluginChatTurn(c, resolved, "queued", pageChatInput{AllowQueue: true})
	var queued *pageChatQueued
	require.ErrorAs(t, err, &queued)
	org := teststores.New(t)
	env.h.SetOrganizationService(service.NewOrganizationService(org.Organizations, "test-secret"))
	restartPageTest(t, env)
	env.h.RecoverQueuedInputs(t.Context())
	require.Len(t, env.starts, 1, "removed user started a model turn")
	pending, err := env.h.threadInputRepo.ListPendingForTask(t.Context(), env.starts[0].TaskID)
	require.NoError(t, err)
	require.Empty(t, pending)
}
