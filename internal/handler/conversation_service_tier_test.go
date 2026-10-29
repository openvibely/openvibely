package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestConversationServiceTier(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-6.1-sol"
		a.ServiceTier = "priority"
	})
	project := createProject(t, h, "Fast")
	task := createTask(t, h, project.ID, "Fast task", func(tk *models.Task) { tk.AgentID = &agent.ID })
	endpoint := "/tasks/" + task.ID + "/thread/model"
	for _, tier := range []string{"default", "priority", ""} {
		res := postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "service_tier": {tier}})
		require.Equal(t, http.StatusNoContent, res.Code, res.Body.String())
		got, _, err := h.resolveTaskThreadExecutionAgent(ctx, task)
		require.NoError(t, err)
		want := tier
		if want == "" {
			want = "priority"
		}
		require.Equal(t, want, got.ServiceTier)
		req := httptest.NewRequest(http.MethodGet, endpoint+"?agent_id="+agent.ID, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `"service_tier":"`+tier+`"`)
	}
	require.Equal(t, http.StatusBadRequest, postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "service_tier": {"bogus"}}).Code)
	require.Equal(t, http.StatusBadRequest, postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "service_tier": {"default"}, "reasoning_effort": {"bogus"}}).Code)
	got, _, err := h.resolveTaskThreadExecutionAgent(ctx, task)
	require.NoError(t, err)
	require.Equal(t, "priority", got.ServiceTier)
	saved, err := repo.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "priority", saved.ServiceTier)
	agent.Model = "claude-opus-5-5"
	agent.Provider = models.ProviderAnthropic
	agent.ServiceTier = ""
	require.NoError(t, repo.Update(ctx, agent))
	require.Equal(t, http.StatusBadRequest, postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "service_tier": {"priority"}}).Code)
}

func TestChatServiceTierQueuesSnapshot(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-6.1-sol"
		a.ServiceTier = "priority"
	})
	project := createProject(t, h, "Fast queued")
	task := createTask(t, h, project.ID, "Active", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning; ex.PromptSent = "active" })
	response := htmxPost(e, "/chat/send?project_id="+project.ID, url.Values{"message": {"next"}, "agent_id": {agent.ID}, "service_tier": {"default"}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	inputs, err := h.threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "default", inputs[0].ServiceTier)
	selected, _, err := h.resolveQueuedInputAgent(ctx, inputs[0])
	require.NoError(t, err)
	require.Equal(t, "default", selected.ServiceTier)
}
