package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func TestCardSearch_PersonalityPage(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/personality?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "personality", "Search personalities...")
}

func TestCardSearch_PersonalityPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/personality?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "personality", "Search personalities...")
	// Partial should not contain full layout
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_ChannelsPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "channels", "Search channels...")
}

func TestCardSearch_ChannelsPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "channels", "Search channels...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_ChannelsMutationsRefreshContainerWithoutFullPageReload(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	_ = h

	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="channels-container"`,
		`data-card-search="channels"`,
		`hx-get="/channels?project_id=default"`,
		`hx-trigger="channels-refresh from:body"`,
		`hx-target="this"`,
		`hx-swap="outerHTML"`,
		`window.refreshCardSearches = initAllCardSearches`,
		`var webhookAvailableAgents = [];`,
		`var selectedWebhookAgentIDs = [];`,
		`var activeWebhookSection = 'config';`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Channels searchable refresh contract to contain %q", want)
		}
	}
	for _, forbidden := range []string{
		`let webhookAvailableAgents = [];`,
		`let selectedWebhookAgentIDs = [];`,
		`let activeWebhookSection = 'config';`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("expected Channels refreshed script to avoid re-execution-unsafe top-level declaration %q", forbidden)
		}
	}
}

func TestCardSearch_AgentsPage(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	// Create an agent so there's a card to search
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		Name:        "TestSearchAgent",
		Description: "A test agent for search",
		Model:       "inherit",
	}
	err := agentRepo.Create(context.Background(), agent)
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "agents", "Search agents...")
	// Verify search text attribute includes the agent name
	if !strings.Contains(body, "data-search-text") {
		t.Error("expected data-search-text attribute on agent cards")
	}
	if !strings.Contains(body, "TestSearchAgent") {
		t.Error("expected agent name in page body")
	}
}

func TestCardSearch_AgentsPartial(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "agents", "Search agents...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_ModelsPage(t *testing.T) {
	h, e, llmRepo := setupTestHandler(t)
	_ = h
	createAgent(t, llmRepo, func(c *models.LLMConfig) {
		c.Name = "TestModelSearch"
		c.Model = "claude-sonnet-4-5"
	})

	req := httptest.NewRequest(http.MethodGet, "/models?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "models", "Search models...")
	if !strings.Contains(body, "data-search-card") {
		t.Error("expected data-search-card attribute on model cards")
	}
}

func TestCardSearch_ModelsPartial(t *testing.T) {
	h, e, llmRepo := setupTestHandler(t)
	_ = h
	createAgent(t, llmRepo, func(c *models.LLMConfig) {
		c.Name = "TestModelSearch"
		c.Model = "claude-sonnet-4-5"
	})

	req := httptest.NewRequest(http.MethodGet, "/models?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "models", "Search models...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_SkillsPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "skills", "Search skills...")
	if !strings.Contains(body, `data-nav-base="/skills"`) {
		t.Error("expected Skills sidebar nav item")
	}
}

func TestCardSearch_SkillsPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "skills", "Search skills...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

func TestCardSearch_SkillsManualRefreshReappliesActiveSearch(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetAgentSkillRoot(t.TempDir())

	req := httptest.NewRequest(http.MethodGet, "/skills?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`window.refreshCardSearches = initAllCardSearches`,
		`window.replaceSearchableCardContainer = replaceSearchableCardContainer`,
		`data-skill-scroll-anchor`,
		`function captureSkillsViewportState(root, activeHandle)`,
		`function restoreSkillsViewportState(root, saved)`,
		`function replaceSkillsContainer(html, options)`,
		`nextContainer = window.replaceSearchableCardContainer('#skills-container', html);`,
		`state.preparedSwap = captureSkillsViewportState(document.getElementById('skills-container'), deleteSkillHandle);`,
		`swap: 'outerHTML show:none'`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Skills page search refresh contract to contain %q", want)
		}
	}
	if got := strings.Count(body, `replaceSkillsContainer(html);`); got != 4 {
		t.Fatalf("expected all four manual Skills container refresh paths to use shared search-aware replacement, got %d", got)
	}
}

func TestCardSearch_AgentsManualDeleteRefreshReappliesActiveSearch(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))
	project := createProject(t, h, "Test Project")

	req := httptest.NewRequest(http.MethodGet, "/agents?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`data-card-search="agents"`,
		`const refreshedContainer = document.getElementById('agents-container');`,
		`htmx.process(refreshedContainer);`,
		`if (window.refreshCardSearches) window.refreshCardSearches(refreshedContainer);`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Agents delete search refresh contract to contain %q", want)
		}
	}
}

func TestCardSearch_AlertsPage(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	createAlert(t, h, project.ID, "SearchableAlert")

	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "alerts", "Search alerts...")
	if !strings.Contains(body, "SearchableAlert") {
		t.Error("expected alert title in page body")
	}
}

func TestCardSearch_AlertsPartial(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Test Project")

	createAlert(t, h, project.ID, "SearchableAlert")

	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertSearch(t, body, "alerts", "Search alerts...")
	if strings.Contains(body, "<!DOCTYPE") {
		t.Error("HTMX partial should not contain full HTML layout")
	}
}

// assertSearch checks that the response body contains the search input with
// the expected page key and placeholder, plus the no-results element.
func assertSearch(t *testing.T, body, pageKey, placeholder string) {
	t.Helper()
	if !strings.Contains(body, `data-card-search="`+pageKey+`"`) {
		t.Errorf("expected data-card-search=%q attribute in body", pageKey)
	}
	if !strings.Contains(body, `placeholder="`+placeholder+`"`) {
		t.Errorf("expected placeholder=%q in body", placeholder)
	}
	if !strings.Contains(body, `data-search-container`) {
		t.Errorf("expected data-search-container attribute in body")
	}
	if !strings.Contains(body, `data-search-no-results`) {
		t.Errorf("expected data-search-no-results element in body")
	}
}
