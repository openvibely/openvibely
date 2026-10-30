package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
