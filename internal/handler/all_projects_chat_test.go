package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsChatHistoryAndDestinations(t *testing.T) {
	h, app, configs := setupTestHandler(t)
	ctx := t.Context()
	agent := &models.LLMConfig{Name: "Chat test", Provider: models.ProviderTest, Model: "test", APIKey: "test", MaxTokens: 1024, IsDefault: true}
	require.NoError(t, configs.Create(ctx, agent))
	form := url.Values{"message": {"Audit our dependencies"}, "agent_id": {agent.ID}}
	req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+models.AllProjectsID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	history, err := h.execRepo.ListChatHistory(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "Audit our dependencies", history[0].PromptSent)
	require.True(t, h.liveSubscriptionAllowed(ctx, liveCommand{Op: "execution", ProjectID: models.AllProjectsID, ExecID: history[0].ID}))
	require.False(t, h.liveSubscriptionAllowed(ctx, liveCommand{Op: "execution", ProjectID: "default", ExecID: history[0].ID}))
	require.Eventually(t, func() bool {
		active, e := h.execRepo.FindLatestActiveChatExecution(ctx, models.AllProjectsID)
		return e == nil && active == nil
	}, 5*time.Second, 20*time.Millisecond)
	ordinary, err := h.execRepo.ListChatHistory(ctx, "default", 10)
	require.NoError(t, err)
	require.Empty(t, ordinary)
	reload := httptest.NewRecorder()
	app.ServeHTTP(reload, httptest.NewRequest(http.MethodGet, "/chat?project_id="+models.AllProjectsID, nil))
	require.Contains(t, reload.Body.String(), "Audit our dependencies")
	require.Contains(t, reload.Body.String(), `id="chat-form"`)
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	require.NoError(t, err)
	for _, p := range projects {
		require.NotEqual(t, models.AllProjectsID, p.ID)
	}
	require.Error(t, h.projectSvc.Delete(ctx, models.AllProjectsID))
	require.Error(t, h.taskRepo.Create(ctx, &models.Task{ProjectID: models.AllProjectsID, Title: "Invalid", Category: models.CategoryBacklog, Status: models.StatusPending}))

	executor := h.chatActionExecutor(streamingResponseParams{ProjectID: models.AllProjectsID}, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	for _, pid := range []string{"", models.AllProjectsID} {
		payload, _ := json.Marshal(map[string]string{"title": "No destination", "project_id": pid, "prompt": "Audit", "category": "backlog"})
		_, _, failed, err := executor(ctx, "create_task", payload)
		require.ErrorContains(t, err, "real project_id")
		require.True(t, failed)
	}
	for _, project := range []*models.Project{createProject(t, h, "API"), createProject(t, h, "Web")} {
		payload, _ := json.Marshal(map[string]string{"title": "Dependency audit", "project_id": project.ID, "prompt": "Audit dependencies", "category": "backlog"})
		out, _, failed, err := executor(ctx, "create_task", payload)
		require.NoError(t, err, out)
		require.False(t, failed, out)
		tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "backlog")
		require.NoError(t, err)
		require.Len(t, tasks, 1)
	}
	ordinaryTask := &models.Task{ProjectID: "default", Title: "Project chat", Category: models.CategoryChat, Status: models.StatusCompleted, AgentID: &agent.ID}
	require.NoError(t, h.taskRepo.Create(ctx, ordinaryTask))
	require.NoError(t, h.execRepo.Create(ctx, &models.Execution{TaskID: ordinaryTask.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "Keep project history"}))
	clear := httptest.NewRecorder()
	app.ServeHTTP(clear, httptest.NewRequest(http.MethodDelete, "/chat/history?project_id="+models.AllProjectsID, nil))
	require.Equal(t, 200, clear.Code, clear.Body.String())
	history, err = h.execRepo.ListChatHistory(ctx, models.AllProjectsID, 10)
	require.NoError(t, err)
	require.Empty(t, history)
	ordinary, err = h.execRepo.ListChatHistory(ctx, "default", 10)
	require.NoError(t, err)
	require.Len(t, ordinary, 1)
	require.Equal(t, "Keep project history", ordinary[0].PromptSent)
}
