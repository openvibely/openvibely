package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCollectionSelectionBrowserContractIsShared(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	for _, want := range []string{
		`checkbox.addEventListener('click'`, `event.stopPropagation()`, `state.last`, `event.key !== 'Escape'`,
		`data-card-select-mode`, `data-card-mobile-actions`, `data-card-bulk-confirm`, `state.ids[card.getAttribute('data-card-select-id')] = true`,
		`_openVibelyInstallSelectionCards`, `if (!existing[id]) delete state.ids[id]`, `focus({preventScroll: true})`,
	} {
		require.Contains(t, body, want)
	}
}

func TestCollectionCardToolbars(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		pageKey  string
		wantSort bool
	}{
		{name: "alerts", path: "/alerts?project_id=default", pageKey: "alerts", wantSort: true},
		{name: "automations", path: "/automations?project_id=default", pageKey: "automations", wantSort: true},
		{name: "agents", path: "/agents?project_id=default", pageKey: "agents", wantSort: true},
		{name: "skills", path: "/skills?project_id=default", pageKey: "skills", wantSort: true},
		{name: "models", path: "/models?project_id=default", pageKey: "models", wantSort: true},
		{name: "channels", path: "/channels?project_id=default", pageKey: "channels", wantSort: false},
		{name: "personality", path: "/personality?project_id=default", pageKey: "personality", wantSort: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			h.SetAgentRepo(repository.NewAgentRepo(db))
			if tt.name == "automations" {
				h.SetAutomationServices(service.NewAutomationGraphService(repository.NewAutomationRepo(db)), nil)
			}
			h.SetAgentSkillRoot(t.TempDir())
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, want := range []string{
				`data-card-list-toolbar="` + tt.pageKey + `"`,
				`data-card-select-loaded`,
				`data-card-filters-button`,
				`data-card-selection-actions`,
			} {
				if !strings.Contains(body, want) {
					t.Errorf("expected %s toolbar to contain %q", tt.name, want)
				}
			}
			if got := strings.Contains(body, `id="`+tt.pageKey+`-card-sort"`); got != tt.wantSort {
				t.Errorf("sort presence = %v, want %v", got, tt.wantSort)
			}
		})
	}
}

func TestAlertsFiltersDecisionStateOnlyInsidePopover(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/alerts?project_id=default&decision_state=pending", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `aria-label="Filter by decision state"`) {
		t.Fatal("standalone decision-state selector must not be rendered")
	}
	if !strings.Contains(body, `data-card-filter-group="decision_state"`) {
		t.Fatal("decision state must be rendered as a Filters popover group")
	}
	if !strings.Contains(body, `data-card-filter-chip="decision_state"`) {
		t.Fatal("active decision state must be rendered as a removable chip")
	}
}

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
		`window.cardPaginationRefreshURL = cardPaginationRefreshURL`,
		`window.destroyCardPagination = destroyCardPagination`,
		`destroyCardPagination(container, true);`,
		`document.body.addEventListener('htmx:configRequest'`,
		`detail.path = cardPaginationRefreshURL(root, detail.path)`,
		`rememberReplacementSnapshot(state);`,
		`nextPage: Math.max(1, Math.ceil(initialCardCount / pageSize))`,
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
		`window.replaceSearchableCardContainer('#agents-container', html)`,
		`cardPaginationRefreshURL`,
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
