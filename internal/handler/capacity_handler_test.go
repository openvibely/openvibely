package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_GetGlobalCapacity(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Set global worker count
	h.workerSvc.Resize(5)

	// Create a project to use for acquiring slots
	project := &models.Project{Name: "Test Project"}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)
	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Simulate 2 running workers by acquiring project slots
	ok := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, ok)
	ok = h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, ok)
	defer func() {
		h.workerSvc.ReleaseProjectSlot(project.ID)
		h.workerSvc.ReleaseProjectSlot(project.ID)
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/global", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp GlobalCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, 5, resp.MaxWorkers)
	assert.Equal(t, 2, resp.TotalRunning)
	assert.True(t, resp.HasCapacity)
	assert.Equal(t, 3, resp.AvailableSlots)
}

func TestHandler_GetGlobalCapacity_Unlimited(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.workerSvc.Resize(0)

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/global", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp GlobalCapacityResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, 0, resp.MaxWorkers)
	assert.True(t, resp.HasCapacity)
	assert.Equal(t, 0, resp.AvailableSlots)
}

func TestHandler_GetGlobalCapacity_AtCapacity(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Set global worker count to 2
	h.workerSvc.Resize(2)

	// Create a project to use for acquiring slots
	project := &models.Project{Name: "Test Project"}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)
	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Acquire all slots
	ok := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, ok)
	ok = h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, ok)
	defer func() {
		h.workerSvc.ReleaseProjectSlot(project.ID)
		h.workerSvc.ReleaseProjectSlot(project.ID)
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/global", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp GlobalCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, 2, resp.MaxWorkers)
	assert.Equal(t, 2, resp.TotalRunning)
	assert.False(t, resp.HasCapacity)
	assert.Equal(t, 0, resp.AvailableSlots)
}

func TestHandler_GetProjectCapacities(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)

	// Create projects with different worker limits
	maxWorkers1 := 3
	project1 := &models.Project{Name: "Project 1", MaxWorkers: &maxWorkers1}
	err := h.projectSvc.Create(ctx, project1)
	require.NoError(t, err)

	maxWorkers2 := 2
	project2 := &models.Project{Name: "Project 2", MaxWorkers: &maxWorkers2}
	err = h.projectSvc.Create(ctx, project2)
	require.NoError(t, err)

	// Wire up project repo to worker service
	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Create some tasks for project 1
	createTask(t, h, project1.ID, "Task 1", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
	})
	createTask(t, h, project1.ID, "Task 2", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
	})

	// Acquire a worker slot for project 1
	ok := h.workerSvc.TryAcquireProjectSlot(project1.ID)
	require.True(t, ok)
	defer h.workerSvc.ReleaseProjectSlot(project1.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/projects", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []ProjectCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	// 3 projects: "default" from migrations + the 2 we created
	assert.Len(t, resp, 3)

	// Find project 1 in response
	var p1Resp *ProjectCapacityResponse
	for i := range resp {
		if resp[i].ID == project1.ID {
			p1Resp = &resp[i]
			break
		}
	}
	require.NotNil(t, p1Resp)

	assert.Equal(t, "Project 1", p1Resp.Name)
	assert.Equal(t, 1, p1Resp.Running)
	assert.Equal(t, 2, p1Resp.QueueSize)
	assert.NotNil(t, p1Resp.MaxWorkers)
	assert.Equal(t, 3, *p1Resp.MaxWorkers)
	assert.True(t, p1Resp.HasCapacity)
	assert.NotNil(t, p1Resp.AvailableSlots)
	assert.Equal(t, 2, *p1Resp.AvailableSlots)

	// Find project 2 in response
	var p2Resp *ProjectCapacityResponse
	for i := range resp {
		if resp[i].ID == project2.ID {
			p2Resp = &resp[i]
			break
		}
	}
	require.NotNil(t, p2Resp)

	assert.Equal(t, "Project 2", p2Resp.Name)
	assert.Equal(t, 0, p2Resp.Running)
	assert.Equal(t, 0, p2Resp.QueueSize)
	assert.NotNil(t, p2Resp.MaxWorkers)
	assert.Equal(t, 2, *p2Resp.MaxWorkers)
	assert.True(t, p2Resp.HasCapacity)
	assert.NotNil(t, p2Resp.AvailableSlots)
	assert.Equal(t, 2, *p2Resp.AvailableSlots)
}

func TestHandler_GetProjectCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)

	maxWorkers := 5
	project := &models.Project{Name: "Test Project", MaxWorkers: &maxWorkers}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Create pending tasks
	createTask(t, h, project.ID, "Pending 1", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
	})
	createTask(t, h, project.ID, "Pending 2", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
	})

	// Acquire 2 worker slots
	ok := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, ok)
	ok = h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, ok)
	defer func() {
		h.workerSvc.ReleaseProjectSlot(project.ID)
		h.workerSvc.ReleaseProjectSlot(project.ID)
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/projects/"+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ProjectCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, project.ID, resp.ID)
	assert.Equal(t, "Test Project", resp.Name)
	assert.Equal(t, 2, resp.Running)
	assert.Equal(t, 2, resp.QueueSize)
	assert.NotNil(t, resp.MaxWorkers)
	assert.Equal(t, 5, *resp.MaxWorkers)
	assert.True(t, resp.HasCapacity)
	assert.NotNil(t, resp.AvailableSlots)
	assert.Equal(t, 3, *resp.AvailableSlots)
}

func TestHandler_GetProjectCapacity_NotFound(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/projects/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_GetProjectCapacity_NoLimit(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)

	// Project with no worker limit
	project := &models.Project{Name: "Unlimited Project", MaxWorkers: nil}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Create pending task
	createTask(t, h, project.ID, "Task", func(tk *models.Task) {
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
	})

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/projects/"+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ProjectCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, project.ID, resp.ID)
	assert.Nil(t, resp.MaxWorkers)
	assert.True(t, resp.HasCapacity) // No limit = always has capacity
	assert.Nil(t, resp.AvailableSlots)
}

func TestHandler_GetModelCapacities(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create agents with different worker limits
	agent1 := &models.LLMConfig{
		Name:       "GPT-4",
		Model:      "gpt-4",
		Provider:   "openai",
		MaxWorkers: 3,
	}
	err := llmConfigRepo.Create(ctx, agent1)
	require.NoError(t, err)

	agent2 := &models.LLMConfig{
		Name:       "Claude",
		Model:      "claude-3-opus",
		Provider:   "anthropic",
		MaxWorkers: 2,
	}
	err = llmConfigRepo.Create(ctx, agent2)
	require.NoError(t, err)

	// Agent with no worker limit (should not appear in response)
	agent3 := &models.LLMConfig{
		Name:       "Unlimited",
		Model:      "test-model",
		Provider:   "anthropic",
		MaxWorkers: 0,
	}
	err = llmConfigRepo.Create(ctx, agent3)
	require.NoError(t, err)

	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)

	// Acquire worker slot for agent1
	ok := h.workerSvc.TryAcquireModelSlot(agent1.ID)
	require.True(t, ok)
	defer h.workerSvc.ReleaseModelSlot(agent1.ID)

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/models", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp []ModelCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	// Should only include agents with MaxWorkers > 0
	assert.Len(t, resp, 2)

	// Find agent1 in response
	var a1Resp *ModelCapacityResponse
	for i := range resp {
		if resp[i].ID == agent1.ID {
			a1Resp = &resp[i]
			break
		}
	}
	require.NotNil(t, a1Resp)

	assert.Equal(t, "GPT-4", a1Resp.Name)
	assert.Equal(t, "gpt-4", a1Resp.Model)
	assert.Equal(t, 1, a1Resp.Running)
	assert.Equal(t, 3, a1Resp.MaxWorkers)
	assert.True(t, a1Resp.HasCapacity)
	assert.Equal(t, 2, a1Resp.AvailableSlots)

	// Find agent2 in response
	var a2Resp *ModelCapacityResponse
	for i := range resp {
		if resp[i].ID == agent2.ID {
			a2Resp = &resp[i]
			break
		}
	}
	require.NotNil(t, a2Resp)

	assert.Equal(t, "Claude", a2Resp.Name)
	assert.Equal(t, "claude-3-opus", a2Resp.Model)
	assert.Equal(t, 0, a2Resp.Running)
	assert.Equal(t, 2, a2Resp.MaxWorkers)
	assert.True(t, a2Resp.HasCapacity)
	assert.Equal(t, 2, a2Resp.AvailableSlots)
}

func TestHandler_GetModelCapacitiesUsesCompactWorkerProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	alpha := &models.LLMConfig{
		Name: "Worker Alpha", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodOAuth,
		Model: "worker-alpha-model", MaxWorkers: 1, APIKey: "secret-key", OAuthAccessToken: "secret-token",
		OAuthRefreshToken: "secret-refresh", OAuthClientSecret: "secret-client", BaseURL: "https://example.com/v1/",
		ModelsURL: "https://example.com/v1/models", ExtraHeadersJSON: `{"secret":"header"}`,
		ExtraBodyJSON: strings.Repeat("x", 64*1024), CustomAuthConfigJSON: `{"signing_secret":"secret"}`,
		CustomAuthStateJSON: `{"token":"secret"}`, MixtureConfigJSON: `{"large":true}`,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, alpha))
	unlimited := &models.LLMConfig{
		Name: "Worker Unlimited", Provider: models.ProviderAnthropic, Model: "worker-unlimited-model", MaxWorkers: 0,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, unlimited))
	zuluDefault := &models.LLMConfig{
		Name: "Worker Zulu Default", Provider: models.ProviderOpenAI, Model: "worker-zulu-model", IsDefault: true, MaxWorkers: 2,
		APIKey: "secret-key", ExtraBodyJSON: strings.Repeat("y", 64*1024),
	}
	require.NoError(t, llmConfigRepo.Create(ctx, zuluDefault))

	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)
	require.True(t, h.workerSvc.TryAcquireModelSlot(zuluDefault.ID))
	defer h.workerSvc.ReleaseModelSlot(zuluDefault.ID)

	counter.Reset()
	counter.SetEnabled(true)
	req := httptest.NewRequest(http.MethodGet, "/api/capacity/models", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	counter.SetEnabled(false)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp []ModelCapacityResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp, 2)
	assert.Equal(t, zuluDefault.ID, resp[0].ID)
	assert.Equal(t, "Worker Zulu Default", resp[0].Name)
	assert.Equal(t, "worker-zulu-model", resp[0].Model)
	assert.Equal(t, 1, resp[0].Running)
	assert.Equal(t, 2, resp[0].MaxWorkers)
	assert.True(t, resp[0].HasCapacity)
	assert.Equal(t, 1, resp[0].AvailableSlots)
	assert.Equal(t, alpha.ID, resp[1].ID)
	assert.Equal(t, "Worker Alpha", resp[1].Name)
	assert.Equal(t, "worker-alpha-model", resp[1].Model)
	assert.Equal(t, 0, resp[1].Running)
	assert.Equal(t, 1, resp[1].MaxWorkers)
	assert.True(t, resp[1].HasCapacity)
	assert.Equal(t, 1, resp[1].AvailableSlots)

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one compact worker capacity query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	if projection != "select id, name, model, max_workers" {
		t.Fatalf("model capacity projection = %q, want compact worker fields in %s", projection, statements[0])
	}
	if !strings.Contains(stmt, "where max_workers > 0") || !strings.Contains(stmt, "order by is_default desc, name asc") {
		t.Fatalf("model capacity query must filter and preserve ordering: %s", statements[0])
	}
	for _, forbidden := range []string{
		"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "oauth_authorize_url",
		"oauth_token_url", "ollama_base_url", "base_url", "models_url", "extra_headers_json", "extra_body_json",
		"custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "worker_timeout",
	} {
		if strings.Contains(projection, forbidden) {
			t.Fatalf("model capacity query selected forbidden column %q: %s", forbidden, statements[0])
		}
	}
}

func TestHandler_GetModelCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Test Model",
		Model:      "test-model",
		Provider:   "anthropic",
		MaxWorkers: 4,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)

	// Acquire 2 worker slots
	ok := h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, ok)
	ok = h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, ok)
	defer func() {
		h.workerSvc.ReleaseModelSlot(agent.ID)
		h.workerSvc.ReleaseModelSlot(agent.ID)
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/models/"+agent.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ModelCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, agent.ID, resp.ID)
	assert.Equal(t, "Test Model", resp.Name)
	assert.Equal(t, "test-model", resp.Model)
	assert.Equal(t, 2, resp.Running)
	assert.Equal(t, 4, resp.MaxWorkers)
	assert.True(t, resp.HasCapacity)
	assert.Equal(t, 2, resp.AvailableSlots)
}

func TestHandler_GetModelCapacity_NotFound(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/models/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandler_GetModelCapacity_AtCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Test Model",
		Model:      "test-model",
		Provider:   "anthropic",
		MaxWorkers: 2,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)

	// Acquire all worker slots
	ok := h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, ok)
	ok = h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, ok)
	defer func() {
		h.workerSvc.ReleaseModelSlot(agent.ID)
		h.workerSvc.ReleaseModelSlot(agent.ID)
	}()

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/models/"+agent.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ModelCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, 2, resp.Running)
	assert.Equal(t, 2, resp.MaxWorkers)
	assert.False(t, resp.HasCapacity)
	assert.Equal(t, 0, resp.AvailableSlots)
}

func TestHandler_GetModelCapacity_NoLimit(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:       "Unlimited Model",
		Model:      "test-model",
		Provider:   "anthropic",
		MaxWorkers: 0,
	}
	err := llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	h.workerSvc.SetLLMConfigRepo(llmConfigRepo)

	req := httptest.NewRequest(http.MethodGet, "/api/capacity/models/"+agent.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ModelCapacityResponse
	err = json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err)

	assert.Equal(t, 0, resp.MaxWorkers)
	assert.True(t, resp.HasCapacity) // No limit = always has capacity
	assert.Equal(t, 0, resp.AvailableSlots)
}
