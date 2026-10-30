package handler

import (
	"context"
	"encoding/json"
	"golang.org/x/net/html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestConversationModelSettingsSharedPersistence(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, repo, func(a *models.LLMConfig) { a.Provider = models.ProviderOpenAI; a.Model = "gpt-6.1-sol" })
	project := createProject(t, h, "Conversation settings")
	other := createProject(t, h, "Other selected project")
	task := createTask(t, h, project.ID, "Thread", func(tk *models.Task) { tk.AgentID = &agent.ID })
	require.NoError(t, h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, other.ID))
	endpoint := "/conversation/model-settings?project_id=" + project.ID
	request := func(method, url, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	payload := `{"agent_id":"` + agent.ID + `","reasoning_effort":"high","service_tier":"priority"}`
	for _, suffix := range []string{"", "&task_id=" + task.ID} {
		rec := request(http.MethodPost, endpoint+suffix, payload)
		require.Equal(t, 204, rec.Code, rec.Body.String())
		rec = request(http.MethodGet, endpoint+suffix, "")
		require.Equal(t, 200, rec.Code, rec.Body.String())
		var data map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &data))
		require.Equal(t, agent.ID, data["agent_id"])
		require.Equal(t, "high", data["reasoning_effort"])
		require.Equal(t, "priority", data["service_tier"])
	}
	rec := request(http.MethodGet, "/conversation/model-settings?project_id="+other.ID+"&task_id="+task.ID, "")
	require.Equal(t, 404, rec.Code)
	rec = request(http.MethodGet, "/conversation/model-settings", "")
	require.Equal(t, 400, rec.Code)
	// Browser import must not replace a choice already persisted by another browser.
	rec = request(http.MethodPost, endpoint, `{"agent_id":"auto","import_only":true}`)
	require.Equal(t, 204, rec.Code)
	rec = request(http.MethodGet, endpoint, "")
	require.Contains(t, rec.Body.String(), agent.ID)
	// Explicit defaults clear both overrides, independently of another conversation.
	rec = request(http.MethodPost, endpoint, `{"agent_id":"`+agent.ID+`"}`)
	require.Equal(t, 204, rec.Code)
	rec = request(http.MethodGet, endpoint, "")
	require.Contains(t, rec.Body.String(), `"reasoning_effort":""`)
	rec = request(http.MethodGet, endpoint+"&task_id="+task.ID, "")
	require.Contains(t, rec.Body.String(), `"reasoning_effort":"high"`)
	// First import preserves every model-specific legacy preference.
	importBody := `{"agent_id":"` + agent.ID + `","import_only":true,"overrides":{"` + agent.ID + `:` + agent.Model + `":{"reasoning_effort":"medium","service_tier":"default"}}}`
	otherEndpoint := "/conversation/model-settings?project_id=" + other.ID
	rec = request(http.MethodPost, otherEndpoint, importBody)
	require.Equal(t, 204, rec.Code, rec.Body.String())
	rec = request(http.MethodGet, otherEndpoint, "")
	require.Contains(t, rec.Body.String(), `"reasoning_effort":"medium"`)
	require.Contains(t, rec.Body.String(), `"service_tier":"default"`)
	// Validate before writing.
	rec = request(http.MethodPost, otherEndpoint, `{"agent_id":"`+agent.ID+`","service_tier":"bogus"}`)
	require.Equal(t, 400, rec.Code)
	rec = request(http.MethodGet, otherEndpoint, "")
	require.Contains(t, rec.Body.String(), `"service_tier":"default"`)
}

func TestConversationModelSettingsPreservesResolvedTaskDefault(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	global := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-6.1-sol"
		a.IsDefault = true
	})
	projectAgent := createAgent(t, repo, func(a *models.LLMConfig) {
		a.Name = "Project default"
		a.Provider = models.ProviderOpenAI
		a.Model = "gpt-6.1-terra"
		a.IsDefault = false
	})
	project := createProject(t, h, "Default resolution")
	project.DefaultAgentConfigID = &projectAgent.ID
	require.NoError(t, h.projectRepo.Update(ctx, project))
	task := createTask(t, h, project.ID, "Unassigned", func(tk *models.Task) { tk.AgentID = nil; tk.Category = models.CategoryBacklog })
	require.NoError(t, h.taskRepo.SetModelOverrides(ctx, task.ID, *projectAgent, stringPtrForModelTest("high"), stringPtrForModelTest("priority")))
	endpoint := "/conversation/model-settings?project_id=" + project.ID + "&task_id=" + task.ID
	check := func(expected, effort, tier string) {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var data map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &data))
		require.Equal(t, expected, data["agent_id"])
		require.Equal(t, effort, data["reasoning_effort"])
		require.Equal(t, tier, data["service_tier"])
	}
	check(projectAgent.ID, "high", "priority")
	project.DefaultAgentConfigID = nil
	require.NoError(t, h.projectRepo.Update(ctx, project))
	check(global.ID, "", "")
	require.NoError(t, h.taskRepo.UpdateAgentID(ctx, task.ID, projectAgent.ID))
	check(projectAgent.ID, "high", "priority")
	// Explicit Auto is still a supported one-off selection.
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, endpoint+"&agent_id=auto", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"agent_id":"auto"`)
}

func stringPtrForModelTest(value string) *string { return &value }

func TestTaskComposerSendAndSteerCarryOwningProject(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	agent := createAgent(t, repo, func(a *models.LLMConfig) { a.Provider = models.ProviderOpenAI; a.Model = "gpt-6.1-sol" })
	project := createProject(t, h, "Thread owner")
	other := createProject(t, h, "Selected project")
	task := createTask(t, h, project.ID, "Thread", func(tk *models.Task) { tk.AgentID = &agent.ID; tk.Category = models.CategoryBacklog })
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferenceSelectedProjectIDKey, other.ID))
	rec := htmxGet(e, "/tasks/"+task.ID+"/thread?project_id="+project.ID)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	doc, err := html.Parse(strings.NewReader(rec.Body.String()))
	require.NoError(t, err)
	var attrs map[string]string
	var visit func(*html.Node)
	visit = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "form" {
			values := map[string]string{}
			for _, attr := range node.Attr {
				values[attr.Key] = attr.Val
			}
			if values["id"] == "task-thread-form" {
				attrs = values
				return
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	require.NotNil(t, attrs)
	for _, attribute := range []string{"hx-post", "data-steer-endpoint"} {
		endpoint, err := url.Parse(attrs[attribute])
		require.NoError(t, err)
		require.Equal(t, project.ID, endpoint.Query().Get("project_id"))
		// Reaching empty-message validation proves the other project's preference
		// cannot reject the request. No model execution is started by this test.
		response := httptest.NewRecorder()
		e.ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint.String(), nil))
		require.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "message is required")
		query := endpoint.Query()
		query.Set("project_id", other.ID)
		endpoint.RawQuery = query.Encode()
		response = httptest.NewRecorder()
		e.ServeHTTP(response, httptest.NewRequest(http.MethodPost, endpoint.String(), nil))
		require.Equal(t, http.StatusNotFound, response.Code, "mismatched project must still be rejected")
	}
}
