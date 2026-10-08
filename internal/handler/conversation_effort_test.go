package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestConversationEffort_TaskOverrideAndDefault(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5.5"
		a.ReasoningEffort = "medium"
	})
	project := createProject(t, h, "Effort")
	task := createTask(t, h, project.ID, "Task effort", func(task *models.Task) { task.AgentID = &agent.ID })
	endpoint := "/tasks/" + task.ID + "/thread/model"
	require.Equal(t, http.StatusNoContent, postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "reasoning_effort": {"high"}}).Code)
	got, _, err := h.resolveTaskThreadExecutionAgent(ctx, task)
	require.NoError(t, err)
	require.Equal(t, "high", got.ReasoningEffort)
	saved, err := repo.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	require.Equal(t, "medium", saved.ReasoningEffort)
	req := httptest.NewRequest(http.MethodGet, endpoint+"?agent_id="+agent.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"reasoning_effort":"high"`)
	require.Equal(t, http.StatusBadRequest, postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "reasoning_effort": {"invalid"}}).Code)
	got, _, err = h.resolveTaskThreadExecutionAgent(ctx, task)
	require.NoError(t, err)
	require.Equal(t, "high", got.ReasoningEffort)
	require.Equal(t, http.StatusNoContent, postForm(e, endpoint, url.Values{"agent_id": {agent.ID}, "reasoning_effort": {""}}).Code)
	got, _, err = h.resolveTaskThreadExecutionAgent(ctx, task)
	require.NoError(t, err)
	require.Equal(t, "medium", got.ReasoningEffort)
}

func TestConversationEffort_ChatAndQueue(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	agent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5.5"
		a.ReasoningEffort = "medium"
	})
	request := httptest.NewRequest(http.MethodPost, "/chat/send", strings.NewReader(url.Values{"reasoning_effort": {"high"}}.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, h.applyConversationEffort(e.NewContext(request, httptest.NewRecorder()), "", agent))
	require.Equal(t, "high", agent.ReasoningEffort)
	got, _, err := h.resolveQueuedInputAgent(context.Background(), models.ThreadInput{AgentConfigID: agent.ID, ReasoningEffort: "high"})
	require.NoError(t, err)
	require.Equal(t, "high", got.ReasoningEffort)
	saved, err := repo.GetByID(context.Background(), agent.ID)
	require.NoError(t, err)
	require.Equal(t, "medium", saved.ReasoningEffort)
}

func TestConversationEffort_ChatSendQueuesSnapshot(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5.5"
		a.ReasoningEffort = "medium"
	})
	project := createProject(t, h, "Queued effort")
	task := createTask(t, h, project.ID, "Active chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning; ex.PromptSent = "active" })
	response := htmxPost(e, "/chat/send?project_id="+project.ID, url.Values{"message": {"next"}, "agent_id": {agent.ID}, "reasoning_effort": {"high"}})
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	inputs, err := h.threadInputRepo.ListPendingForChat(ctx, project.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, "high", inputs[0].ReasoningEffort)
	agent.ReasoningEffort = "low"
	require.NoError(t, repo.Update(ctx, agent))
	got, _, err := h.resolveQueuedInputAgent(ctx, inputs[0])
	require.NoError(t, err)
	require.Equal(t, "high", got.ReasoningEffort)
}

func TestConversationPicker_RealPagesIncludeProviderAndEffortMetadata(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	agent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5.5"
		a.ReasoningEffort = "high"
	})
	createAgent(t, repo, func(a *models.LLMConfig) {
		a.Name = "Local Qwen"
		a.Provider = models.ProviderOpenAICompatible
		a.PresetSlug = "vllm"
		a.Model = "qwen"
	})
	project := createProject(t, h, "Picker pages")
	task := createTask(t, h, project.ID, "Picker task", func(tk *models.Task) { tk.AgentID = &agent.ID })
	for _, path := range []string{"/chat?project_id=" + project.ID, "/tasks/" + task.ID + "/thread", "/tasks/new?project_id=" + project.ID} {
		t.Run(path, func(t *testing.T) {
			response := htmxGet(e, path)
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), `data-picker-provider="OpenAI"`)
			require.Contains(t, response.Body.String(), `data-picker-provider="Local vLLM"`)
			require.Contains(t, response.Body.String(), `data-picker-default="high"`)
			require.Contains(t, response.Body.String(), `data-picker-efforts="low,medium,high,xhigh"`)
		})
	}
	data, err := h.loadTaskDetailContentData(context.Background(), task.ID)
	require.NoError(t, err)
	for _, a := range data.agents {
		if a.ID == agent.ID {
			require.Equal(t, models.ProviderOpenAI, a.Provider)
			require.Equal(t, "high", a.ReasoningEffort)
			return
		}
	}
	t.Fatal("selected model missing from task detail data")
}

func TestConversationPicker_DefaultShowsResolvedModelAndAcceptsTaskEffort(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	global := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Name = "Global model"
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5.5"
		a.ReasoningEffort = "medium"
	})
	projectModel := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Name = "Project model"
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-5.5"
		a.ReasoningEffort = "medium"
		a.IsDefault = false
	})
	project := createProject(t, h, "Picker default")
	project.DefaultAgentConfigID = &projectModel.ID
	require.NoError(t, h.projectRepo.Update(context.Background(), project))
	task := createTask(t, h, project.ID, "Picker task", func(tk *models.Task) { tk.AgentID = &global.ID })
	for _, tc := range []struct{ path, name, id string }{
		{"/chat?project_id=" + project.ID, "Project model", projectModel.ID},
		{"/tasks/new?project_id=" + project.ID, "Project model", projectModel.ID},
		{"/tasks/" + task.ID + "/thread", "Project model", projectModel.ID},
	} {
		t.Run(tc.path, func(t *testing.T) {
			response := htmxGet(e, tc.path)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), `>Default — `+tc.name+`</li>`)
			require.Contains(t, response.Body.String(), `data-picker-effective-id="`+tc.id+`"`)
		})
	}
	fallbackProject := createProject(t, h, "Picker global fallback")
	fallbackPage := htmxGet(e, "/chat?project_id="+fallbackProject.ID)
	require.Equal(t, http.StatusOK, fallbackPage.Code, fallbackPage.Body.String())
	require.Contains(t, fallbackPage.Body.String(), `>Default — Global model</li>`)
	require.Contains(t, fallbackPage.Body.String(), `data-picker-effective-id="`+global.ID+`"`)
	for _, tc := range []struct {
		projectID, modelID string
	}{
		{project.ID, projectModel.ID},
		{fallbackProject.ID, global.ID},
	} {
		activeTask := createTask(t, h, tc.projectID, "Active chat", func(tk *models.Task) {
			tk.Category = models.CategoryChat
			tk.Status = models.StatusRunning
			tk.AgentID = &global.ID
		})
		createExec(t, h, activeTask.ID, global.ID, func(ex *models.Execution) {
			ex.Status = models.ExecRunning
		})
		response := htmxPost(e, "/chat/send?project_id="+tc.projectID, url.Values{
			"message":          {"queued with default"},
			"agent_id":         {"default"},
			"reasoning_effort": {"high"},
		})
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		queued, err := h.threadInputRepo.ListPendingForChat(context.Background(), tc.projectID)
		require.NoError(t, err)
		require.Len(t, queued, 1)
		require.Equal(t, tc.modelID, queued[0].AgentConfigID)
		require.Equal(t, "high", queued[0].ReasoningEffort)
	}
	endpoint := "/tasks/" + task.ID + "/thread/model"
	require.Equal(t, http.StatusNoContent, postForm(e, endpoint, url.Values{"agent_id": {"default"}, "reasoning_effort": {"high"}}).Code)
	response := htmxGet(e, endpoint+"?agent_id=default")
	require.Equal(t, http.StatusOK, response.Code)
	require.Contains(t, response.Body.String(), `"reasoning_effort":"high"`)
	saved, err := repo.GetByID(context.Background(), projectModel.ID)
	require.NoError(t, err)
	require.Equal(t, "medium", saved.ReasoningEffort)
}

func TestConversationEffort_NewDeferredTaskPreservesSelection(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		name := "backlog"
		if scheduled {
			name = "scheduled"
		}
		t.Run(name, func(t *testing.T) {
			h, e, repo := setupTestHandler(t)
			agent := createAgent(t, repo, func(a *models.LLMConfig) {
				a.Provider = models.ProviderOpenAI
				a.Model = "gpt-5.5"
				a.ReasoningEffort = "medium"
			})
			project := createProject(t, h, "Deferred effort")
			form := url.Values{"message": {"Future work"}, "category": {"backlog"}, "agent_id": {agent.ID}, "reasoning_effort": {"high"}}
			if scheduled {
				form.Set("add_schedule", "on")
				form.Set("run_at", "2035-01-02T09:30")
				form.Set("repeat_type", "once")
			}
			response := htmxPost(e, "/tasks?project_id="+project.ID+"&from=new&thread=1", form)
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			id := response.Header().Get("X-Created-Task-ID")
			require.NotEmpty(t, id)
			effort, err := h.taskRepo.ModelEffort(context.Background(), id, *agent)
			require.NoError(t, err)
			require.Equal(t, "high", effort)
			task, err := h.taskRepo.GetByID(context.Background(), id)
			require.NoError(t, err)
			resolved, _, err := h.resolveTaskThreadExecutionAgent(context.Background(), task)
			require.NoError(t, err)
			require.Equal(t, "high", resolved.ReasoningEffort)
			saved, err := repo.GetByID(context.Background(), agent.ID)
			require.NoError(t, err)
			require.Equal(t, "medium", saved.ReasoningEffort)
			form.Set("reasoning_effort", "invalid")
			response = htmxPost(e, "/tasks?project_id="+project.ID+"&from=new&thread=1", form)
			require.Equal(t, http.StatusBadRequest, response.Code)
		})
	}
}
