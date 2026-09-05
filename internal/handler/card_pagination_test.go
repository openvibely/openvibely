package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

func serveCardPageRequest(t *testing.T, e *echo.Echo, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}
func TestEveryCollectionBulkRouteRejectsMalformedPayload(t *testing.T) {
	tc := NewTestContext(t)
	tc.handler.SetAgentRepo(repository.NewAgentRepo(tc.db))
	tc.handler.SetWebhookRepo(repository.NewWebhookRepo(tc.db))
	tc.handler.SetCustomPersonalityRepo(repository.NewCustomPersonalityRepo(tc.db))
	automationRepo := repository.NewAutomationRepo(tc.db)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
	project := tc.CreateProject().WithName("Bulk validation").Build()

	malformedBodies := []string{`{}`, `{`, `{"ids":["a"],"extra":true}`, `{"ids":["a"]} {}`, `{"ids":[]}`, `{"skills":[]}`}
	for _, path := range []string{
		"/alerts/bulk", "/automations/bulk", "/agents/bulk", "/skills/bulk", "/models/bulk", "/channels/webhooks/bulk", "/personality/custom/bulk",
	} {
		t.Run(path, func(t *testing.T) {
			for _, body := range malformedBodies {
				req := httptest.NewRequest(http.MethodDelete, path+"?project_id="+project.ID, strings.NewReader(body))
				req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
				rec := httptest.NewRecorder()
				tc.echo.ServeHTTP(rec, req)
				require.Equal(t, http.StatusBadRequest, rec.Code, body+": "+rec.Body.String())
			}
		})
	}
}

func TestBulkHandlersPreflightWithoutPartialDeletion(t *testing.T) {
	t.Run("models", func(t *testing.T) {
		tc := NewTestContext(t)
		defaultModel := &models.LLMConfig{Name: "Default protected", Provider: models.ProviderTest, Model: "default-protected", IsDefault: true}
		ordinaryModel := &models.LLMConfig{Name: "Ordinary model", Provider: models.ProviderTest, Model: "ordinary-model"}
		require.NoError(t, tc.llmConfigRepo.Create(t.Context(), defaultModel))
		require.NoError(t, tc.llmConfigRepo.Create(t.Context(), ordinaryModel))

		rec := serveBulkDeleteRequest(t, tc.echo, "/models/bulk", bulkIDsRequest{IDs: []string{ordinaryModel.ID, defaultModel.ID}})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		remaining, err := tc.llmConfigRepo.GetByIDs(t.Context(), []string{ordinaryModel.ID, defaultModel.ID})
		require.NoError(t, err)
		require.Len(t, remaining, 2)
	})

	t.Run("personalities", func(t *testing.T) {
		tc := NewTestContext(t)
		repo := repository.NewCustomPersonalityRepo(tc.db)
		tc.handler.SetCustomPersonalityRepo(repo)
		active := &models.CustomPersonality{Name: "Active custom", Key: "active_custom", SystemPrompt: "A sufficiently long active prompt."}
		inactive := &models.CustomPersonality{Name: "Inactive custom", Key: "inactive_custom", SystemPrompt: "A sufficiently long inactive prompt."}
		require.NoError(t, repo.Create(t.Context(), active))
		require.NoError(t, repo.Create(t.Context(), inactive))
		require.NoError(t, tc.settingsRepo.Set(t.Context(), "personality", active.Key))

		rec := serveBulkDeleteRequest(t, tc.echo, "/personality/custom/bulk", bulkIDsRequest{IDs: []string{inactive.Key, active.Key}})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		for _, key := range []string{inactive.Key, active.Key} {
			personality, err := repo.GetByKey(t.Context(), key)
			require.NoError(t, err)
			require.NotNil(t, personality)
		}
	})

	t.Run("webhooks", func(t *testing.T) {
		tc := NewTestContext(t)
		repo := repository.NewWebhookRepo(tc.db)
		tc.handler.SetWebhookRepo(repo)
		firstProject := tc.CreateProject().WithName("Bulk webhook first").Build()
		secondProject := tc.CreateProject().WithName("Bulk webhook second").Build()
		own := &models.WebhookEndpoint{ProjectID: firstProject.ID, Name: "Own webhook", Enabled: true}
		foreign := &models.WebhookEndpoint{ProjectID: secondProject.ID, Name: "Foreign webhook", Enabled: true}
		require.NoError(t, repo.Create(t.Context(), own))
		require.NoError(t, repo.Create(t.Context(), foreign))

		rec := serveBulkDeleteRequest(t, tc.echo, "/channels/webhooks/bulk?project_id="+firstProject.ID, bulkIDsRequest{IDs: []string{own.ID, foreign.ID}})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		for _, id := range []string{own.ID, foreign.ID} {
			endpoint, err := repo.GetByID(t.Context(), id)
			require.NoError(t, err)
			require.NotNil(t, endpoint)
		}
	})
	t.Run("agents", func(t *testing.T) {
		tc := NewTestContext(t)
		repo := repository.NewAgentRepo(tc.db)
		tc.handler.SetAgentRepo(repo)
		ordinary := &models.Agent{Name: "Ordinary bulk agent", Key: "ordinary_bulk_agent", Scope: models.AgentScopeGlobal, Enabled: true, GeneratedStatus: models.AgentStatusUserEdited}
		protected := &models.Agent{Name: "Protected bulk agent", Key: "protected_bulk_agent", Scope: models.AgentScopeGlobal, Enabled: true, GeneratedStatus: models.AgentStatusProtected}
		require.NoError(t, repo.Create(t.Context(), ordinary))
		require.NoError(t, repo.Create(t.Context(), protected))
		rec := serveBulkDeleteRequest(t, tc.echo, "/agents/bulk", bulkIDsRequest{IDs: []string{ordinary.ID, protected.ID}})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		for _, id := range []string{ordinary.ID, protected.ID} {
			agent, err := repo.GetByID(t.Context(), id)
			require.NoError(t, err)
			require.NotNil(t, agent)
		}
	})

	t.Run("automations", func(t *testing.T) {
		tc := NewTestContext(t)
		repo := repository.NewAutomationRepo(tc.db)
		tc.handler.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(repo, tc.scheduleRepo))
		firstProject := tc.CreateProject().WithName("Bulk automation first").Build()
		secondProject := tc.CreateProject().WithName("Bulk automation second").Build()
		var ownID, foreignID string
		require.NoError(t, tc.db.QueryRow(`INSERT INTO automations (project_id, stable_key, name) VALUES (?, 'own', 'Own') RETURNING id`, firstProject.ID).Scan(&ownID))
		require.NoError(t, tc.db.QueryRow(`INSERT INTO automations (project_id, stable_key, name) VALUES (?, 'foreign', 'Foreign') RETURNING id`, secondProject.ID).Scan(&foreignID))
		rec := serveBulkDeleteRequest(t, tc.echo, "/automations/bulk?project_id="+firstProject.ID, bulkIDsRequest{IDs: []string{ownID, foreignID}})
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		for _, id := range []string{ownID, foreignID} {
			var count int
			require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automations WHERE id = ?`, id).Scan(&count))
			require.Equal(t, 1, count)
		}
	})

	t.Run("skills", func(t *testing.T) {
		tc := NewTestContext(t)
		root := t.TempDir()
		tc.handler.SetAgentSkillRoot(root)
		existing := filepath.Join(root, "skills", "existing-skill")
		require.NoError(t, os.MkdirAll(existing, 0o755))
		payload := bulkSkillsRequest{Skills: []bulkSkillRef{{Handle: "existing-skill", Scope: "global"}, {Handle: "missing-skill", Scope: "global"}}}
		rec := serveBulkDeleteRequest(t, tc.echo, "/skills/bulk", payload)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		stat, err := os.Stat(existing)
		require.NoError(t, err)
		require.True(t, stat.IsDir())
	})
}

func serveBulkDeleteRequest(t *testing.T, e *echo.Echo, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodDelete, path, bytes.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestCollectionHandlersNormalizeRejectedToolbarState(t *testing.T) {
	tc := NewTestContext(t)
	tc.handler.SetAgentRepo(repository.NewAgentRepo(tc.db))
	tc.handler.SetWebhookRepo(repository.NewWebhookRepo(tc.db))
	tc.handler.SetCustomPersonalityRepo(repository.NewCustomPersonalityRepo(tc.db))
	automationRepo := repository.NewAutomationRepo(tc.db)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	project := tc.CreateProject().WithName("Rejected toolbar state").Build()
	for _, path := range []string{
		"/alerts?project_id=" + project.ID + "&read=bad&severity=fatal&decision_state=queued&type=other&sort=title",
		"/automations?project_id=" + project.ID + "&lifecycle_state=bad&health_state=bad&automation_type=bad&adapter=bad&sort=bad",
		"/agents?project_id=" + project.ID + "&enabled=bad&scope=bad&origin=bad&sort=bad",
		"/skills?project_id=" + project.ID + "&enabled=bad&scope=bad&always_use=bad&archived=bad&source=bad&sort=bad",
		"/models?project_id=" + project.ID + "&provider=bad&default=bad&auth_status=bad&kind=bad&sort=bad",
		"/channels?project_id=" + project.ID + "&type=bad&connection_state=bad&webhook_enabled=bad",
		"/personality?project_id=" + project.ID + "&kind=bad&active=bad&sort=bad",
	} {
		rec := serveCardPageRequest(t, tc.echo, path)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotContains(t, rec.Body.String(), `data-card-filter-chip=`)
	}
}

func TestCollectionMutationMarkupPreservesValidatedListState(t *testing.T) {
	state := pages.CardListState{
		ProjectID: "project-state",
		Search:    "needle",
		Sort:      "name_desc",
		Filters: map[string]string{
			"lifecycle_state": "active",
			"health_state":    "degraded",
		},
	}
	var automations bytes.Buffer
	automation := models.AutomationCard{Automation: models.Automation{ID: "automation-state", Name: "Stateful", LifecycleState: models.AutomationActive}}
	require.NoError(t, pages.AutomationsContentPageWithState([]models.AutomationCard{automation}, state.ProjectID, false, state).Render(t.Context(), &automations))
	for _, endpoint := range []string{"run-now", "pause"} {
		require.Contains(t, automations.String(), "/automations/automation-state/"+endpoint+"?health_state=degraded&amp;lifecycle_state=active&amp;project_id=project-state&amp;search=needle&amp;sort=name_desc")
	}

	personalityState := pages.PersonalityListState{ProjectID: "project-state", Search: "needle", Kind: "custom", Active: "false", Sort: "name_desc"}
	cards := []pages.PersonalityListCard{{Key: "custom-state", Name: "Custom state", Kind: "custom", Custom: &models.CustomPersonality{Key: "custom-state", Name: "Custom state"}}}
	var personality bytes.Buffer
	require.NoError(t, pages.PersonalitySectionPageWithPaginationState("", cards, false, personalityState).Render(t.Context(), &personality))
	require.Contains(t, personality.String(), "/personality/custom/custom-state?active=false&amp;kind=custom&amp;project_id=project-state&amp;search=needle&amp;sort=name_desc")
	require.Contains(t, personality.String(), "/personality/save?active=false&amp;kind=custom&amp;personality=custom-state&amp;project_id=project-state&amp;search=needle&amp;sort=name_desc")

	for _, marker := range []string{
		"cardCollectionActionURL(document.getElementById('agents-container')",
		"cardCollectionActionURL(document.getElementById('models-container')",
		"cardCollectionActionURL(root, path)",
	} {
		require.Contains(t, automations.String()+personality.String()+renderCollectionPageScripts(t), marker)
	}
}

func renderCollectionPageScripts(t *testing.T) string {
	t.Helper()
	var agents, modelsPage bytes.Buffer
	require.NoError(t, pages.AgentsContentPageWithState(nil, nil, false, pages.CardListState{}).Render(t.Context(), &agents))
	require.NoError(t, pages.ModelsContentPageWithPaginationAndState(nil, nil, nil, false, false, pages.CardListState{}).Render(t.Context(), &modelsPage))
	return agents.String() + modelsPage.String()
}

func TestAlertsEmptyStateRecognizesEveryActiveFilter(t *testing.T) {
	for _, filter := range []struct{ key, value string }{{"read", "unread"}, {"severity", "error"}, {"decision_state", "pending"}, {"type", "custom"}, {"source", "native"}} {
		t.Run(filter.key, func(t *testing.T) {
			state := pages.CardListState{Filters: map[string]string{filter.key: filter.value}}
			var body bytes.Buffer
			require.NoError(t, pages.AlertsContentPageWithState(nil, "project-alerts", 0, false, "", "", state).Render(t.Context(), &body))
			require.Contains(t, body.String(), "No alerts match the selected filters.")
			require.NotContains(t, body.String(), "No alerts. You&#39;re all clear!")
		})
	}
}

func TestIndividualMutationRefreshesPreserveValidatedListState(t *testing.T) {
	t.Run("agent create", func(t *testing.T) {
		tc := NewTestContext(t)
		tc.handler.SetAgentRepo(repository.NewAgentRepo(tc.db))
		tc.handler.SetAgentSkillRoot(t.TempDir())
		form := url.Values{
			"name": {"mutation-agent"}, "description": {"mutation"}, "system_prompt": {"do work"},
			"model": {"inherit"}, "scope": {"global"}, "tools_json": {`[]`}, "plugins_json": {`[]`}, "skills_json": {`[]`}, "mcp_servers_json": {`[]`},
		}
		req := httptest.NewRequest(http.MethodPost, "/agents?scope=project&sort=name_desc&search=mutation", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		tc.echo.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `data-card-pagination-url="/agents?project_id=default&amp;scope=project&amp;search=mutation&amp;sort=name_desc"`)
		require.NotContains(t, rec.Body.String(), `data-agent-name="mutation-agent"`)
	})

	t.Run("model set default", func(t *testing.T) {
		tc := NewTestContext(t)
		first := &models.LLMConfig{Name: "First model", Provider: models.ProviderTest, Model: "first", IsDefault: true}
		second := &models.LLMConfig{Name: "Second model", Provider: models.ProviderTest, Model: "second"}
		require.NoError(t, tc.llmConfigRepo.Create(t.Context(), first))
		require.NoError(t, tc.llmConfigRepo.Create(t.Context(), second))
		req := httptest.NewRequest(http.MethodPost, "/models/"+second.ID+"/set-default?default=false&sort=name_desc&search=Second", nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		tc.echo.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `data-card-pagination-url="/models?default=false&amp;project_id=default&amp;search=Second&amp;sort=name_desc"`)
		require.NotContains(t, rec.Body.String(), `data-model-id="`+second.ID+`"`)
	})

	t.Run("personality create", func(t *testing.T) {
		tc := NewTestContext(t)
		tc.handler.SetCustomPersonalityRepo(repository.NewCustomPersonalityRepo(tc.db))
		project := tc.CreateProject().WithName("Personality mutation state").Build()
		form := url.Values{"name": {"Mutation personality"}, "description": {"mutation"}, "system_prompt": {"A sufficiently long personality prompt."}}
		req := httptest.NewRequest(http.MethodPost, "/personality/custom?project_id="+project.ID+"&kind=base&active=false&sort=name_desc&search=Base", strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		tc.echo.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), `data-card-pagination-url="/personality?active=false&amp;kind=base&amp;project_id=`+project.ID+`&amp;search=Base&amp;sort=name_desc"`)
		require.NotContains(t, rec.Body.String(), `data-personality-name="Mutation personality"`)
	})
}

func TestCollectionHandlersRenderValidatedToolbarState(t *testing.T) {
	tc := NewTestContext(t)
	tc.handler.SetAgentRepo(repository.NewAgentRepo(tc.db))
	tc.handler.SetWebhookRepo(repository.NewWebhookRepo(tc.db))
	tc.handler.SetCustomPersonalityRepo(repository.NewCustomPersonalityRepo(tc.db))
	automationRepo := repository.NewAutomationRepo(tc.db)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	project := tc.CreateProject().WithName("Toolbar state").Build()
	tests := []struct {
		name     string
		path     string
		contains []string
	}{
		{name: "automations", path: "/automations?project_id=" + project.ID + "&lifecycle_state=paused&health_state=degraded&automation_type=custom&adapter=custom&sort=name_desc", contains: []string{`data-card-filter-chip="lifecycle_state"`, `data-card-filter-chip="health_state"`, `data-card-filter-chip="automation_type"`, `data-card-filter-chip="adapter"`, `<option value="name_desc" selected`}},
		{name: "models", path: "/models?provider=openai&default=false&auth_status=not_connected&kind=direct&sort=name_desc", contains: []string{`data-card-filter-chip="provider"`, `data-card-filter-chip="default"`, `data-card-filter-chip="auth_status"`, `data-card-filter-chip="kind"`, `<option value="name_desc" selected`}},
		{name: "agents", path: "/agents?enabled=true&scope=global&origin=custom&sort=updated_desc", contains: []string{`data-card-filter-chip="enabled"`, `data-card-filter-chip="scope"`, `data-card-filter-chip="origin"`, `<option value="updated_desc" selected`}},
		{name: "skills", path: "/skills?project_id=" + project.ID + "&enabled=true&scope=global&always_use=false&archived=false&source=global&sort=scope", contains: []string{`data-card-filter-chip="enabled"`, `data-card-filter-chip="scope"`, `data-card-filter-chip="always_use"`, `data-card-filter-chip="archived"`, `data-card-filter-chip="source"`, `<option value="scope" selected`}},
		{name: "channels", path: "/channels?project_id=" + project.ID + "&type=webhook&connection_state=configured&webhook_enabled=true", contains: []string{`data-card-filter-chip="type"`, `data-card-filter-chip="connection_state"`, `data-card-filter-chip="webhook_enabled"`}},
		{name: "personality", path: "/personality?project_id=" + project.ID + "&kind=custom&active=false&sort=name_desc", contains: []string{`data-card-filter-chip="kind"`, `data-card-filter-chip="active"`, `project_id=` + project.ID, `<option value="name_desc" selected`}},
		{name: "alerts", path: "/alerts?project_id=" + project.ID + "&read=unread&severity=error&decision_state=pending&type=custom&source=native&processing_state=failed&sort=severity", contains: []string{`data-card-filter-chip="read"`, `data-card-filter-chip="severity"`, `data-card-filter-chip="decision_state"`, `data-card-filter-chip="type"`, `data-card-filter-chip="source"`, `<option value="severity" selected`, `processing_state=failed`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveCardPageRequest(t, tc.echo, tt.path)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			for _, expected := range tt.contains {
				require.Contains(t, rec.Body.String(), expected)
			}
		})
	}
}

func TestPersonalityFilteringAndSortingPrecedePagination(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	h.SetCustomPersonalityRepo(repo)
	for _, personality := range []*models.CustomPersonality{
		{Name: "Aardvark custom", Key: "aardvark_custom", Description: "first by name", SystemPrompt: "A sufficiently long custom personality prompt."},
		{Name: "Zulu custom", Key: "zulu_custom", Description: "last by name", SystemPrompt: "Another sufficiently long custom personality prompt."},
	} {
		require.NoError(t, repo.Create(t.Context(), personality))
	}

	baseOnly := serveCardPageRequest(t, e, "/personality?kind=base&page_size=2&card_page=1")
	require.Equal(t, http.StatusOK, baseOnly.Code)
	require.Equal(t, "false", baseOnly.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 1, strings.Count(baseOnly.Body.String(), `data-personality-key=`))
	require.Contains(t, baseOnly.Body.String(), `data-personality-name="Base"`)

	ascending := serveCardPageRequest(t, e, "/personality?sort=name_asc&page_size=2&card_page=1")
	require.Equal(t, http.StatusOK, ascending.Code)
	require.Equal(t, 2, strings.Count(ascending.Body.String(), `data-personality-key=`))
	matches := regexp.MustCompile(`data-personality-name="([^"]+)"`).FindAllStringSubmatch(ascending.Body.String(), -1)
	require.Len(t, matches, 2)
	require.LessOrEqual(t, strings.ToLower(matches[0][1]), strings.ToLower(matches[1][1]))
}

func TestChannelsSearchFiltersFixedCardsOnServer(t *testing.T) {
	var body bytes.Buffer
	view := pages.ChannelsSettingsView{
		CurrentProjectID: "project-channels-search",
		HasGitHubChannel: true,
		HasSlackChannel:  true,
		WebhooksSearch:   "github",
		GitHubStatus:     service.GitHubConnectionStatus{Configured: true, Connected: true},
		SlackStatus:      service.SlackConnectionStatus{Configured: true, Connected: true},
	}
	require.NoError(t, pages.SettingsContent(view).Render(t.Context(), &body))
	require.Contains(t, body.String(), `data-channel-type="github"`)
	require.Regexp(t, `data-channel-type="slack"[^>]* hidden`, body.String())
}

func TestUnmanagedSkillsRenderNoSelectionUIOrMetadata(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	h.SetAgentSkillRoot("")
	rec := serveCardPageRequest(t, e, "/skills?project_id=default")
	require.Equal(t, http.StatusOK, rec.Code)
	for _, forbidden := range []string{
		`data-card-select-loaded`, `data-card-select-mode`, `data-card-selection-actions`,
		`data-card-bulk-confirm`, `data-card-mobile-actions`, `data-card-select-id`,
	} {
		require.NotContains(t, rec.Body.String(), forbidden)
	}
}

func TestEveryCollectionPageAcceptsAndRejectsItsFilterAndSortValues(t *testing.T) {
	tc := NewTestContext(t)
	tc.handler.SetAgentRepo(repository.NewAgentRepo(tc.db))
	tc.handler.SetWebhookRepo(repository.NewWebhookRepo(tc.db))
	tc.handler.SetCustomPersonalityRepo(repository.NewCustomPersonalityRepo(tc.db))
	tc.handler.SetAgentSkillRoot(t.TempDir())
	automationRepo := repository.NewAutomationRepo(tc.db)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	project := tc.CreateProject().WithName("Collection parser matrix").Build()

	type pageSpec struct {
		path     string
		filters  map[string][]string
		sorts    []string
		fallback string
	}
	specs := map[string]pageSpec{
		"alerts": {path: "/alerts?project_id=" + project.ID, filters: map[string][]string{
			"read": {"read", "unread"}, "severity": {"info", "warning", "error"}, "decision_state": {"not_required", "pending", "approved", "rejected", "dismissed"},
			"type": {"task_failed", "task_needs_followup", "custom"}, "source": {"native"},
		}, sorts: []string{"newest", "oldest", "severity", "unread_first"}, fallback: "newest"},
		"automations": {path: "/automations?project_id=" + project.ID, filters: map[string][]string{
			"lifecycle_state": {"active", "paused", "draft", "archived"}, "health_state": {"unknown", "healthy", "degraded", "unhealthy"},
			"automation_type": {"custom", "native_sdlc", "github_sdlc", "vision_driver", "scheduled"}, "adapter": {"custom", "native_sdlc", "github_sdlc", "vision_driver"},
		}, sorts: []string{"updated_desc", "updated_asc", "name_asc", "name_desc"}, fallback: "updated_desc"},
		"agents": {path: "/agents?project_id=" + project.ID, filters: map[string][]string{
			"enabled": {"true", "false"}, "scope": {"global", "project"}, "origin": {"custom", "generated", "protected"},
		}, sorts: []string{"name_asc", "name_desc", "updated_desc", "created_desc"}, fallback: "name_asc"},
		"skills": {path: "/skills?project_id=" + project.ID, filters: map[string][]string{
			"enabled": {"true", "false"}, "scope": {"global", "project"}, "always_use": {"true", "false"}, "archived": {"true", "false"}, "source": {"global", "project"},
		}, sorts: []string{"name_asc", "name_desc", "scope", "source"}, fallback: "name_asc"},
		"models": {path: "/models?project_id=" + project.ID, filters: map[string][]string{
			"provider": {"openai", "anthropic", "ollama", "openai_compatible", "mixture"}, "default": {"true", "false"}, "auth_status": {"connected", "not_connected", "not_required"}, "kind": {"direct", "mixture"},
		}, sorts: []string{"default_name", "name_asc", "name_desc", "provider"}, fallback: "default_name"},
		"channels": {path: "/channels?project_id=" + project.ID, filters: map[string][]string{
			"type": {"github", "slack", "telegram", "discord", "x", "email", "webhook", "outbound_targets"}, "connection_state": {"connected", "configured", "disconnected"}, "webhook_enabled": {"true", "false"},
		}},
		"personality": {path: "/personality?project_id=" + project.ID, filters: map[string][]string{
			"kind": {"base", "built_in", "custom", "override"}, "active": {"true", "false"},
		}, sorts: []string{"curated", "name_asc", "name_desc"}, fallback: "curated"},
	}

	for pageName, spec := range specs {
		t.Run(pageName, func(t *testing.T) {
			separator := "&"
			for key, values := range spec.filters {
				for _, value := range values {
					rec := serveCardPageRequest(t, tc.echo, spec.path+separator+url.QueryEscape(key)+"="+url.QueryEscape(value))
					require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
					require.Contains(t, rec.Body.String(), `data-card-filter-chip="`+key+`"`, key+"="+value)
				}
				if pageName == "alerts" && key == "source" {
					continue
				}
				rec := serveCardPageRequest(t, tc.echo, spec.path+separator+url.QueryEscape(key)+"=invalid")
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.NotContains(t, rec.Body.String(), `data-card-filter-chip="`+key+`"`, key)
			}
			for _, sortValue := range spec.sorts {
				rec := serveCardPageRequest(t, tc.echo, spec.path+separator+"sort="+url.QueryEscape(sortValue))
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), `<option value="`+sortValue+`" selected`, sortValue)
			}
			if spec.fallback != "" {
				rec := serveCardPageRequest(t, tc.echo, spec.path+separator+"sort=invalid")
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Contains(t, rec.Body.String(), `<option value="`+spec.fallback+`" selected`)
				require.NotContains(t, rec.Body.String(), `<option value="invalid" selected`)
			}
		})
	}

	overlongSource := strings.Repeat("s", 101)
	rec := serveCardPageRequest(t, tc.echo, "/alerts?project_id="+project.ID+"&source="+overlongSource)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), `data-card-filter-chip="source"`)
	require.NotContains(t, rec.Body.String(), "source="+overlongSource)
}

func TestCollectionFilterAndSortAllowlists(t *testing.T) {
	tests := []struct {
		key, fallback string
		values        []string
	}{
		{"lifecycle_state", "", []string{"active", "paused", "draft", "archived"}},
		{"health_state", "", []string{"unknown", "healthy", "degraded", "unhealthy"}},
		{"automation_type", "", []string{"custom", "native_sdlc", "github_sdlc", "vision_driver", "scheduled"}},
		{"adapter", "", []string{"custom", "native_sdlc", "github_sdlc", "vision_driver"}},
		{"enabled", "", []string{"true", "false"}}, {"scope", "", []string{"global", "project"}},
		{"origin", "", []string{"custom", "generated", "protected"}}, {"always_use", "", []string{"true", "false"}},
		{"archived", "", []string{"true", "false"}}, {"source", "", []string{"global", "project"}},
		{"provider", "", []string{"openai", "anthropic", "ollama", "openai_compatible", "mixture"}},
		{"auth_status", "", []string{"connected", "not_connected", "not_required"}}, {"kind", "", []string{"direct", "mixture"}},
		{"type", "", []string{"github", "slack", "telegram", "discord", "x", "email", "webhook", "outbound_targets"}},
		{"connection_state", "", []string{"connected", "configured", "disconnected"}}, {"webhook_enabled", "", []string{"true", "false"}},
		{"sort", "name_asc", []string{"name_asc", "name_desc", "updated_desc", "created_desc"}},
	}
	e := echo.New()
	for _, tt := range tests {
		for _, value := range tt.values {
			ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/?"+tt.key+"="+value, nil), httptest.NewRecorder())
			require.Equal(t, value, allowlistedQuery(ctx, tt.key, tt.fallback, tt.values...), tt.key+"="+value)
		}
		ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/?"+tt.key+"=invalid", nil), httptest.NewRecorder())
		require.Equal(t, tt.fallback, allowlistedQuery(ctx, tt.key, tt.fallback, tt.values...), tt.key)
	}
}

func TestBulkIDsRequestValidation(t *testing.T) {
	ids, err := decodeBulkIDs(strings.NewReader(`{"ids":[" a ","a","b"]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b"}, ids)
	for _, body := range []string{`{}`, `{"ids":[]}`, `{"ids":[""]}`, `{"ids":["a"],"extra":true}`, `{`, `{"ids":["a"]} {}`} {
		_, err := decodeBulkIDs(strings.NewReader(body))
		require.Error(t, err, body)
	}
	tooMany := make([]string, bulkDeleteMaxItems+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("id-%d", i)
	}
	payload, err := json.Marshal(bulkIDsRequest{IDs: tooMany})
	require.NoError(t, err)
	_, err = decodeBulkIDs(bytes.NewReader(payload))
	require.ErrorContains(t, err, "at most")
}

func TestAlertListFilterAllowlistedValues(t *testing.T) {
	e := echo.New()
	accepted := "/alerts?read=unread&severity=error&decision_state=approved&type=custom&source=native&sort=severity"
	ctx := e.NewContext(httptest.NewRequest(http.MethodGet, accepted, nil), httptest.NewRecorder())
	filter := alertListFilter(ctx, parseCardPageRequest(ctx))
	require.NotNil(t, filter.Read)
	require.False(t, *filter.Read)
	require.Equal(t, models.SeverityError, filter.Severity)
	require.Equal(t, models.AlertDecisionApproved, filter.DecisionState)
	require.Equal(t, models.AlertCustom, filter.Type)
	require.Equal(t, "native", filter.Source)
	require.Equal(t, "severity", filter.Sort)

	rejected := e.NewContext(httptest.NewRequest(http.MethodGet, "/alerts?read=maybe&severity=fatal&decision_state=queued&type=other&sort=title", nil), httptest.NewRecorder())
	filter = alertListFilter(rejected, parseCardPageRequest(rejected))
	require.Nil(t, filter.Read)
	require.Empty(t, filter.Severity)
	require.Empty(t, filter.DecisionState)
	require.Empty(t, filter.Type)
	require.Equal(t, "newest", filter.Sort)
}

func TestParseCardPageRequestKeepsFullDocumentsAlignedWithLoaderSize(t *testing.T) {
	e := echo.New()
	full := e.NewContext(httptest.NewRequest(http.MethodGet, "/models?page=0&page_size=2", nil), httptest.NewRecorder())
	parsed := parseCardPageRequest(full)
	require.Equal(t, 0, parsed.Page)
	require.Equal(t, cardPageDefaultSize, parsed.PageSize)
	require.Equal(t, 0, parsed.Offset)
	require.False(t, parsed.IsFragment)

	fragment := e.NewContext(httptest.NewRequest(http.MethodGet, "/models?page=0&page_size=2&card_page=1", nil), httptest.NewRecorder())
	parsed = parseCardPageRequest(fragment)
	require.Equal(t, 2, parsed.PageSize)
	require.True(t, parsed.IsFragment)

	later := e.NewContext(httptest.NewRequest(http.MethodGet, "/models?page=1&page_size=2", nil), httptest.NewRecorder())
	parsed = parseCardPageRequest(later)
	require.Equal(t, 1, parsed.Page)
	require.Equal(t, 2, parsed.Offset)
	require.True(t, parsed.IsFragment)

	explicitOffset := e.NewContext(httptest.NewRequest(http.MethodGet, "/models?page=2&page_size=20&offset=35&card_page=1", nil), httptest.NewRecorder())
	parsed = parseCardPageRequest(explicitOffset)
	require.Equal(t, 2, parsed.Page)
	require.Equal(t, 35, parsed.Offset)
	require.True(t, parsed.IsFragment)

	refresh := e.NewContext(httptest.NewRequest(http.MethodGet, "/models?page=7&page_size=40&card_window=1&search=active", nil), httptest.NewRecorder())
	parsed = parseCardPageRequest(refresh)
	require.Equal(t, 0, parsed.Page)
	require.Equal(t, 40, parsed.PageSize)
	require.Equal(t, 0, parsed.Offset)
	require.Equal(t, "active", parsed.Search)
	require.False(t, parsed.IsFragment)

	boundedRefresh := e.NewContext(httptest.NewRequest(http.MethodGet, "/models?page_size=999999&card_window=1", nil), httptest.NewRecorder())
	parsed = parseCardPageRequest(boundedRefresh)
	require.Equal(t, cardPageRefreshMaxSize, parsed.PageSize)
	require.False(t, parsed.IsFragment)
}

func TestCardPaginationRefreshWindowPreservesLoadedCardsAndContinuation(t *testing.T) {
	_, e, repo, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	for i := 0; i < 41; i++ {
		require.NoError(t, repo.Create(ctx, &models.LLMConfig{
			Name:     fmt.Sprintf("Refresh Window Model %02d", i),
			Provider: models.ProviderTest,
			Model:    fmt.Sprintf("refresh-window-%02d", i),
		}))
	}

	refresh := serveCardPageRequest(t, e, "/models?card_window=1&page=0&page_size=35&search=refresh+window")
	require.Equal(t, http.StatusOK, refresh.Code)
	require.Equal(t, 35, strings.Count(refresh.Body.String(), "data-model-provider="))
	require.Contains(t, refresh.Body.String(), `data-card-pagination-has-more="true"`)
	require.Contains(t, refresh.Body.String(), "Refresh Window Model 34")
	require.NotContains(t, refresh.Body.String(), `data-model-name="Refresh Window Model 35"`)

	continuation := serveCardPageRequest(t, e, "/models?card_page=1&page=2&page_size=20&offset=35&search=refresh+window")
	require.Equal(t, http.StatusOK, continuation.Code)
	require.Equal(t, 6, strings.Count(continuation.Body.String(), "data-model-provider="))
	require.Contains(t, continuation.Body.String(), "Refresh Window Model 35")
	require.Equal(t, "false", continuation.Header().Get(cardPageHasMoreHeader))
}

func TestCardPaginationModelHandlerReturnsPagesAndEndState(t *testing.T) {
	_, e, repo, _ := setupTestHandlerWithDB(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		config := &models.LLMConfig{
			Name:     fmt.Sprintf("Scroll Model %02d", i),
			Provider: models.ProviderTest,
			Model:    fmt.Sprintf("scroll-model-%02d", i),
		}
		require.NoError(t, repo.Create(ctx, config))
	}

	first := serveCardPageRequest(t, e, "/models?page=0&page_size=2&search=scroll+model&card_page=1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "true", first.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, first.Body.String(), `data-card-pagination-has-more="true"`)
	require.Contains(t, first.Body.String(), "Scroll Model 00")
	require.Contains(t, first.Body.String(), "Scroll Model 01")
	require.NotContains(t, first.Body.String(), `data-model-name="Scroll Model 02"`)
	require.Equal(t, 2, strings.Count(first.Body.String(), "data-model-provider="))

	second := serveCardPageRequest(t, e, "/models?page=1&page_size=2&search=scroll+model&card_page=1")
	require.Equal(t, http.StatusOK, second.Code)
	require.Equal(t, "true", second.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, second.Body.String(), "Scroll Model 02")
	require.Contains(t, second.Body.String(), "Scroll Model 03")
	require.NotContains(t, second.Body.String(), `data-model-name="Scroll Model 00"`)

	end := serveCardPageRequest(t, e, "/models?page=2&page_size=2&search=scroll+model&card_page=1")
	require.Equal(t, http.StatusOK, end.Code)
	require.Equal(t, "false", end.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, end.Body.String(), `data-model-name="Scroll Model 04"`)
	require.Equal(t, 1, strings.Count(end.Body.String(), "data-model-provider="))

	empty := serveCardPageRequest(t, e, "/models?page=3&page_size=2&search=scroll+model&card_page=1")
	require.Equal(t, http.StatusOK, empty.Code)
	require.Equal(t, "false", empty.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, empty.Body.String(), "No models configured yet")
}

func TestCardPaginationAgentHandlerBoundsSearchResults(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		agent := &models.Agent{Name: fmt.Sprintf("Paged Handler Agent %02d", i), Description: "searchable", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
		require.NoError(t, agentRepo.Create(ctx, agent))
	}

	first := serveCardPageRequest(t, e, "/agents?page=0&page_size=2&search=paged+handler&card_page=1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "true", first.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 2, strings.Count(first.Body.String(), "data-agent-description="))
	require.Contains(t, first.Body.String(), "Paged Handler Agent 00")
	require.NotContains(t, first.Body.String(), "Paged Handler Agent 02")

	last := serveCardPageRequest(t, e, "/agents?page=1&page_size=2&search=paged+handler&card_page=1")
	require.Equal(t, http.StatusOK, last.Code)
	require.Equal(t, "false", last.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 1, strings.Count(last.Body.String(), "data-agent-description="))
	require.Contains(t, last.Body.String(), "Paged Handler Agent 02")
}

func TestCardPaginationAutomationHandlerReturnsProjectScopedPages(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Paged automation project"}
	other := &models.Project{Name: "Other automation project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, other))

	automationRepo := repository.NewAutomationRepo(db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	h.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)
	for i := 0; i < 3; i++ {
		task := &models.Task{
			ProjectID: project.ID, Title: fmt.Sprintf("Paged automation task %02d", i),
			Prompt: "run paged automation", Category: models.CategoryScheduled,
			Status: models.StatusPending, Priority: 1,
		}
		require.NoError(t, h.taskRepo.Create(ctx, task))
		runAt := time.Now().UTC().Add(time.Hour)
		schedule := &models.Schedule{
			TaskID: task.ID, RunAt: runAt, NextRun: &runAt,
			RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true,
		}
		require.NoError(t, h.scheduleRepo.Create(ctx, schedule))
		_, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{
			ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC,
			StableKey: fmt.Sprintf("native-sdlc/paged-%02d", i),
			Name:      fmt.Sprintf("Paged automation %02d", i),
			Resources: []models.AutomationResourceBinding{
				{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
				{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
			},
		})
		require.NoError(t, err)
	}

	first := serveCardPageRequest(t, e, "/automations?project_id="+project.ID+"&page=0&page_size=2&search=paged+automation&card_page=1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "true", first.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 2, strings.Count(first.Body.String(), "data-automation-url="))
	require.Contains(t, first.Body.String(), "Paged automation")

	last := serveCardPageRequest(t, e, "/automations?project_id="+project.ID+"&page=1&page_size=2&search=paged+automation&card_page=1")
	require.Equal(t, http.StatusOK, last.Code)
	require.Equal(t, "false", last.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 1, strings.Count(last.Body.String(), "data-automation-url="))
	require.Contains(t, last.Body.String(), "Paged automation")
	later, err := automationRepo.ListPortfolioCardsPage(ctx, project.ID, 2, 2, "paged automation")
	require.NoError(t, err)
	require.Len(t, later, 1)
	require.Contains(t, last.Body.String(), later[0].Automation.Name)
	require.NotContains(t, first.Body.String(), `data-automation-url="/automations/`+later[0].Automation.ID)

	foreignTask := &models.Task{
		ProjectID: other.ID, Title: "Foreign automation task", Prompt: "run",
		Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 1,
	}
	require.NoError(t, h.taskRepo.Create(ctx, foreignTask))
	foreignRunAt := time.Now().UTC().Add(time.Hour)
	foreignSchedule := &models.Schedule{TaskID: foreignTask.ID, RunAt: foreignRunAt, NextRun: &foreignRunAt, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: true}
	require.NoError(t, h.scheduleRepo.Create(ctx, foreignSchedule))
	_, _, err = registration.Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: other.ID, AdapterKey: service.AutomationAdapterNativeSDLC,
		StableKey: "native-sdlc/foreign-paged", Name: "Foreign paged automation",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: foreignTask.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: foreignSchedule.ID},
		},
	})
	require.NoError(t, err)
	isolated := serveCardPageRequest(t, e, "/automations?project_id="+project.ID+"&page=0&page_size=20&search=foreign&card_page=1")
	require.Equal(t, "false", isolated.Header().Get(cardPageHasMoreHeader))
	require.NotContains(t, isolated.Body.String(), "Foreign paged automation")
}

func TestCardPaginationSkillsHandlerBoundsFilesystemCards(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithDB(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	for i := 0; i < 5; i++ {
		writeStandaloneSkill(t, root, fmt.Sprintf("paged_skill_%02d", i), fmt.Sprintf("Paged skill %02d", i), "paged skill description", "global")
	}

	first := serveCardPageRequest(t, e, "/skills?page=0&page_size=2&search=paged+skill&card_page=1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "true", first.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 2, strings.Count(first.Body.String(), `data-skill-scroll-anchor="paged_skill`))
	require.Contains(t, first.Body.String(), "Paged skill 00")
	require.NotContains(t, first.Body.String(), "Paged skill 02")

	last := serveCardPageRequest(t, e, "/skills?page=2&page_size=2&search=paged+skill&card_page=1")
	require.Equal(t, http.StatusOK, last.Code)
	require.Equal(t, "false", last.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, last.Body.String(), "Paged skill 04")
}

func TestCardPaginationAlertsAndWebhooksStayProjectScoped(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Paged handler project"}
	other := &models.Project{Name: "Other paged handler project"}
	require.NoError(t, projectRepo.Create(ctx, project))
	require.NoError(t, projectRepo.Create(ctx, other))

	alertRepo := repository.NewAlertRepo(db)
	for i := 0; i < 3; i++ {
		alert := &models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: fmt.Sprintf("Paged alert %02d", i), Message: "needle summary", Source: "handler-page"}
		require.NoError(t, alertRepo.Create(ctx, alert))
	}
	foreignAlert := &models.Alert{ProjectID: other.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "Foreign alert", Message: "needle summary", Source: "handler-page"}
	require.NoError(t, alertRepo.Create(ctx, foreignAlert))

	alerts := serveCardPageRequest(t, e, "/alerts?project_id="+project.ID+"&page=0&page_size=2&search=needle&card_page=1")
	require.Equal(t, http.StatusOK, alerts.Code)
	require.Equal(t, "true", alerts.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 2, strings.Count(alerts.Body.String(), "data-alert-id="))
	require.NotContains(t, alerts.Body.String(), "Foreign alert")

	webhookRepo := repository.NewWebhookRepo(db)
	h.SetWebhookRepo(webhookRepo)
	for i := 0; i < 3; i++ {
		endpoint := &models.WebhookEndpoint{ProjectID: project.ID, Name: fmt.Sprintf("Paged webhook %02d", i), Enabled: true}
		require.NoError(t, webhookRepo.Create(ctx, endpoint))
	}
	foreignWebhook := &models.WebhookEndpoint{ProjectID: other.ID, Name: "Foreign webhook", Enabled: true}
	require.NoError(t, webhookRepo.Create(ctx, foreignWebhook))

	webhooks := serveCardPageRequest(t, e, "/channels?project_id="+project.ID+"&page=0&page_size=2&search=paged+webhook&card_page=1")
	require.Equal(t, http.StatusOK, webhooks.Code)
	require.Equal(t, "true", webhooks.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 2, strings.Count(webhooks.Body.String(), "data-webhook-name="))
	require.NotContains(t, webhooks.Body.String(), "Foreign webhook")
}

func TestCardPaginationPersonalityHandlerFiltersFixedCardsAndPagesCustomCards(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	h.SetCustomPersonalityRepo(repo)
	ctx := context.Background()
	presets := service.AllPersonalities()
	require.NotEmpty(t, presets)
	require.NoError(t, repo.Create(ctx, &models.CustomPersonality{
		Name:         "Paged custom preset override",
		Key:          presets[0].Key,
		Description:  "preset override",
		SystemPrompt: "A custom prompt long enough for a preset override card.",
	}))
	for i := 0; i < 3; i++ {
		personality := &models.CustomPersonality{
			Name:         fmt.Sprintf("Paged custom %02d", i),
			Key:          fmt.Sprintf("paged_custom_%02d", i),
			Description:  "paged personality",
			SystemPrompt: "A custom prompt long enough for a card.",
		}
		require.NoError(t, repo.Create(ctx, personality))
	}

	first := serveCardPageRequest(t, e, "/personality?page=0&page_size=2&search=paged+custom&card_page=1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "true", first.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, first.Body.String(), `data-card-pagination-has-more="true"`)
	require.Equal(t, 2, strings.Count(first.Body.String(), `data-personality-is-preset="false"`))
	require.Contains(t, first.Body.String(), "Paged custom 00")
	require.NotContains(t, first.Body.String(), "data-personality-is-preset=\"true\"")

	last := serveCardPageRequest(t, e, "/personality?page=1&page_size=2&search=paged+custom&card_page=1")
	require.Equal(t, http.StatusOK, last.Code)
	require.Equal(t, "false", last.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 1, strings.Count(last.Body.String(), `data-personality-is-preset="false"`))
	require.Contains(t, last.Body.String(), "Paged custom 02")
}

func TestCardPaginationPersonalityHandlerKeepsMatchingActiveCardOutsidePageWindow(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	repo := repository.NewCustomPersonalityRepo(db)
	h.SetCustomPersonalityRepo(repo)
	ctx := context.Background()
	presets := service.AllPersonalities()
	require.Greater(t, len(presets), 1)
	activeKey := "paged_custom_21"
	require.NoError(t, h.settingsRepo.Set(ctx, "personality", activeKey))
	require.NoError(t, repo.Create(ctx, &models.CustomPersonality{
		Name:         "Overridden preset",
		Key:          presets[1].Key,
		Description:  "fixed override",
		SystemPrompt: "A compact preset override prompt for cards.",
	}))
	for i := 0; i < 22; i++ {
		require.NoError(t, repo.Create(ctx, &models.CustomPersonality{
			Name:         fmt.Sprintf("Paged custom %02d", i),
			Key:          fmt.Sprintf("paged_custom_%02d", i),
			Description:  "paged personality",
			SystemPrompt: "A custom prompt long enough for a card.",
		}))
	}

	first := serveCardPageRequest(t, e, "/personality?page=0&page_size=20&search=paged+custom&card_page=1")
	require.Equal(t, http.StatusOK, first.Code)
	require.Equal(t, "true", first.Header().Get(cardPageHasMoreHeader))
	require.Contains(t, first.Body.String(), `data-personality-key="paged_custom_21"`)
	require.NotContains(t, first.Body.String(), `data-personality-key="`+presets[1].Key+`"`)
	require.NotContains(t, first.Body.String(), `data-personality-name="Overridden preset"`)
	require.Equal(t, 20, strings.Count(first.Body.String(), `data-personality-pagination-card="true"`))
	require.NotContains(t, first.Body.String(), `data-personality-pagination-card="false"`)
	for i := 0; i < 19; i++ {
		require.Contains(t, first.Body.String(), fmt.Sprintf(`data-personality-key="paged_custom_%02d"`, i))
	}
	require.NotContains(t, first.Body.String(), `data-personality-key="paged_custom_19"`)
	require.NotContains(t, first.Body.String(), `data-personality-key="paged_custom_20"`)

	continuationOffset := strings.Count(first.Body.String(), `data-personality-pagination-card="true"`)
	continuation := serveCardPageRequest(t, e, fmt.Sprintf("/personality?page=1&page_size=20&offset=%d&search=paged+custom&card_page=1", continuationOffset))
	require.Equal(t, http.StatusOK, continuation.Code)
	require.Equal(t, "false", continuation.Header().Get(cardPageHasMoreHeader))
	require.Equal(t, 2, strings.Count(continuation.Body.String(), `data-personality-pagination-card="true"`))
	require.Contains(t, continuation.Body.String(), `data-personality-key="paged_custom_19"`)
	require.Contains(t, continuation.Body.String(), `data-personality-key="paged_custom_20"`)
	require.NotContains(t, continuation.Body.String(), `data-personality-key="paged_custom_21"`)
}

func TestCardPaginationProductionBrowserLoadsSequentialPagesAndResetsSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	initialModels := make([]models.LLMConfig, 20)
	for i := range initialModels {
		initialModels[i] = models.LLMConfig{Name: fmt.Sprintf("Browser page %02d", i), Provider: models.ProviderTest, Model: fmt.Sprintf("browser-page-%02d", i), ID: fmt.Sprintf("browser-%02d", i)}
	}
	pageOne := []models.LLMConfig{
		{ID: "browser-20", Name: "Browser page 20", Provider: models.ProviderTest, Model: "browser-page-20"},
		{ID: "browser-21", Name: "Browser page 21", Provider: models.ProviderTest, Model: "browser-page-21"},
	}
	pageTwo := []models.LLMConfig{{ID: "browser-22", Name: "Browser page 22", Provider: models.ProviderTest, Model: "browser-page-22"}}
	late := []models.LLMConfig{{ID: "browser-late", Name: "Late search result", Provider: models.ProviderTest, Model: "late-model"}}

	render := func(cards []models.LLMConfig) string {
		var buf bytes.Buffer
		if err := pages.ModelsContentPageWithPagination(cards, cards, map[string]int{}, false, true).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render models fragment: %v", err)
		}
		return buf.String()
	}
	initialHTML := render(initialModels)
	pageOneHTML := render(pageOne)
	pageTwoHTML := render(pageTwo)
	lateHTML := render(late)
	var pageOneRequests atomic.Int32
	var pageTwoRequests atomic.Int32
	var searchRequests atomic.Int32
	var base bytes.Buffer
	if err := layout.Base("Models browser pagination", nil, "").Render(context.Background(), &base); err != nil {
		t.Fatalf("render base layout: %v", err)
	}
	baseHTML := base.String()
	var localBaseLines []string
	for _, line := range strings.Split(baseHTML, "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		localBaseLines = append(localBaseLines, line)
	}
	page := strings.Replace(strings.Join(localBaseLines, "\n"), "</main>", initialHTML+"</main>", 1)
	page = strings.Replace(page, "</body>", `<script>
(function() {
  function result(status, message) {
    var node = document.createElement('div');
    node.id = 'browser-result';
    node.setAttribute('data-status', status);
    node.textContent = message || status;
    document.body.appendChild(node);
  }
  function fail(error) { result('fail', String(error && error.stack || error)); }
  function cards() { return document.querySelectorAll('#models-card-list [data-model-provider]'); }
  function scrollToSentinel() {
    var sentinel = document.querySelector('[data-card-pagination-sentinel]');
    if (!sentinel) return;
    if (sentinel.scrollIntoView) sentinel.scrollIntoView({block: 'center'});
    var rect = sentinel.getBoundingClientRect();
    window.scrollTo(0, Math.max(0, window.scrollY + rect.top - window.innerHeight + 120));
    var scroll = document.getElementById('main-content');
    if (scroll) { scroll.scrollTop = scroll.scrollHeight; scroll.dispatchEvent(new Event('scroll')); }
    window.dispatchEvent(new Event('scroll'));
  }
  function waitForInitial() {
    var list = cards();
    if (list.length < 23) {
      scrollToSentinel();
      setTimeout(waitForInitial, 50);
      return;
    }
    if (list.length === 23) {
      setTimeout(function() {
        try {
          var input = document.querySelector('input[data-card-search="models"]');
          input.value = 'late';
          input.dispatchEvent(new Event('input', {bubbles: true}));
          waitForSearch();
        } catch (error) { fail(error); }
      }, 250);
      return;
    }
    setTimeout(waitForInitial, 50);
  }
  function waitForSearch() {
    var list = cards();
    var late = document.querySelector('[data-model-id="browser-late"]');
    if (list.length === 1 && late && !document.querySelector('[data-model-id="browser-22"]')) {
      var root = document.getElementById('models-container');
      var state = root && root._openVibelyCardPaginationState;
      if (state && state.hasMore === false) return result('pass', 'sequential pages and search reset applied');
    }
    setTimeout(waitForSearch, 50);
  }
  window.addEventListener('load', function() { setTimeout(waitForInitial, 100); });
  setTimeout(function() {
    if (!document.getElementById('browser-result')) fail('pagination fixture timed out waiting for cards');
  }, 7000);
})();
</script></body>`, 1)

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			switch r.URL.Query().Get("page") {
			case "1":
				pageOneRequests.Add(1)
				w.Header().Set(cardPageHasMoreHeader, "true")
				_, _ = w.Write([]byte(pageOneHTML))
			case "2":
				pageTwoRequests.Add(1)
				w.Header().Set(cardPageHasMoreHeader, "false")
				_, _ = w.Write([]byte(pageTwoHTML))
			default:
				if r.URL.Query().Get("search") == "late" {
					searchRequests.Add(1)
					w.Header().Set(cardPageHasMoreHeader, "false")
					_, _ = w.Write([]byte(lateHTML))
					return
				}
				w.Header().Set(cardPageHasMoreHeader, "false")
				_, _ = w.Write([]byte(initialHTML))
			}
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check",
		"--no-first-run", "--window-size=1280,900", "--virtual-time-budget=8000", "--dump-dom", fixture.URL,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chrome timed out: %v\n%s", ctx.Err(), out)
	}
	require.NoError(t, err, "Chrome output: %s", out)
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 500
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser pagination regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser pagination regression did not report a result; DOM length=%d", len(dom))
	}
	require.Equal(t, int32(1), pageOneRequests.Load())
	require.Equal(t, int32(1), pageTwoRequests.Load())
	require.Equal(t, int32(1), searchRequests.Load())
}

func TestCardPaginationProductionBrowserDoesNotRestoreClearedURLSearchAfterRefresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	modelsForPage := []models.LLMConfig{
		{ID: "url-search-foo", Name: "Foo model", Provider: models.ProviderTest, Model: "foo-model"},
		{ID: "url-search-other", Name: "Other model", Provider: models.ProviderTest, Model: "other-model"},
	}
	render := func(cards []models.LLMConfig) string {
		var buf bytes.Buffer
		if err := pages.ModelsContentPageWithPagination(cards, cards, map[string]int{}, false, true).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render models fragment: %v", err)
		}
		return buf.String()
	}
	initialHTML := render(modelsForPage)
	clearHTML := render(modelsForPage)
	refreshHTML := render(modelsForPage)
	refreshHTMLJSON, err := json.Marshal(refreshHTML)
	require.NoError(t, err)

	var base bytes.Buffer
	require.NoError(t, layout.Base("Models URL search pagination", nil, "").Render(context.Background(), &base))
	var localBaseLines []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		localBaseLines = append(localBaseLines, line)
	}
	page := strings.Replace(strings.Join(localBaseLines, "\n"), "</main>", initialHTML+"</main>", 1)
	page = strings.Replace(page, "</body>", `<script>
(function() {
  function result(status, message) {
    var node = document.createElement('div');
    node.id = 'browser-result';
    node.setAttribute('data-status', status);
    node.textContent = message || status;
    document.body.appendChild(node);
  }
  function fail(error) { result('fail', String(error && error.stack || error)); }
  function input() { return document.querySelector('input[data-card-search="models"]'); }
  function otherCard() { return document.querySelector('[data-model-id="url-search-other"]'); }
  function waitForInitial() {
    var field = input(), other = otherCard();
    if (!field || !other || field.value !== 'foo') {
      setTimeout(waitForInitial, 30);
      return;
    }
    if (getComputedStyle(other).display !== 'none') {
      fail('URL search was not applied on initial load');
      return;
    }
    field.value = '';
    field.dispatchEvent(new Event('input', {bubbles: true}));
    waitForClearedSearchRequest();
  }
  function waitForClearedSearchRequest() {
    fetch('/card-search-state').then(function(response) { return response.text(); }).then(function(value) {
      var field = input(), other = otherCard();
      if (value === 'ready' && field && field.value === '' && other && getComputedStyle(other).display !== 'none') {
        var oldRoot = document.getElementById('models-container');
        document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap', {detail: {target: oldRoot}}));
        oldRoot.outerHTML = `+string(refreshHTMLJSON)+`;
        var nextRoot = document.getElementById('models-container');
        document.body.dispatchEvent(new CustomEvent('htmx:afterSettle', {detail: {elt: nextRoot}}));
        waitForRefreshedRoot();
        return;
      }
      setTimeout(waitForClearedSearchRequest, 30);
    }).catch(fail);
  }
  function waitForRefreshedRoot() {
    var field = input(), other = otherCard();
    if (field && field.value === 'foo') {
      fail('cleared URL search was restored after root refresh');
      return;
    }
    if (field && field.value === '' && other && getComputedStyle(other).display !== 'none') {
      result('pass', 'cleared URL search survived root refresh');
      return;
    }
    setTimeout(waitForRefreshedRoot, 30);
  }
  window.addEventListener('load', function() { setTimeout(waitForInitial, 100); });
  setTimeout(function() {
    if (!document.getElementById('browser-result')) fail('URL search pagination fixture timed out');
  }, 8000);
})();
</script></body>`, 1)

	var clearedSearchRequests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/card-search-state" {
			if clearedSearchRequests.Load() > 0 {
				_, _ = w.Write([]byte("ready"))
			} else {
				_, _ = w.Write([]byte("waiting"))
			}
			return
		}
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.URL.Query().Get("page") == "0" && r.URL.Query().Get("search") == "" {
				clearedSearchRequests.Add(1)
				w.Header().Set(cardPageHasMoreHeader, "false")
				_, _ = w.Write([]byte(clearHTML))
				return
			}
			w.Header().Set(cardPageHasMoreHeader, "false")
			_, _ = w.Write([]byte(initialHTML))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check",
		"--no-first-run", "--window-size=1280,900", "--virtual-time-budget=9000", "--dump-dom", fixture.URL+"?search=foo",
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chrome timed out: %v (clearedSearchRequests=%d)\n%s", ctx.Err(), clearedSearchRequests.Load(), out)
	}
	require.NoError(t, err, "Chrome output: %s", out)
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 700
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser URL search pagination regression failed (clearedSearchRequests=%d): %s", clearedSearchRequests.Load(), html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser URL search pagination regression did not report a result; DOM length=%d", len(dom))
	}
	require.Equal(t, int32(1), clearedSearchRequests.Load())
}

func TestCardPaginationProductionBrowserRestoresFocusFromFixedPersonalityCard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	selected := models.CustomPersonality{
		Key:                 "fixed-selected",
		Name:                "Fixed selected",
		Description:         "selected fixed card",
		SystemPromptPreview: "A selected custom personality preview.",
	}
	pageable := models.CustomPersonality{
		Key:                 "page-owned",
		Name:                "Page owned",
		Description:         "page-owned card",
		SystemPromptPreview: "A pageable custom personality preview.",
	}
	render := func(personality string, cards []models.CustomPersonality) string {
		var buf bytes.Buffer
		require.NoError(t, pages.PersonalitySectionPageWithPagination(personality, cards, false).Render(context.Background(), &buf))
		return buf.String()
	}
	initialHTML := render(selected.Key, []models.CustomPersonality{selected, pageable})
	replacementHTMLJSON, err := json.Marshal(render("", []models.CustomPersonality{pageable}))
	require.NoError(t, err)

	var base bytes.Buffer
	require.NoError(t, layout.Base("Fixed Personality focus fixture", nil, "").Render(context.Background(), &base))
	var localBaseLines []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		localBaseLines = append(localBaseLines, line)
	}
	page := strings.Replace(strings.Join(localBaseLines, "\n"), "</main>", initialHTML+"</main>", 1)
	page = strings.Replace(page, "</head>", `<style>
	.dropdown-content { visibility: hidden; opacity: 0; }
	.dropdown:focus-within > .dropdown-content { visibility: visible; opacity: 1; }
	</style></head>`, 1)
	page = strings.Replace(page, "</body>", `<script>
(function() {
  function result(status, message) {
    var node = document.createElement('div');
    node.id = 'browser-result';
    node.setAttribute('data-status', status);
    node.textContent = message || status;
    document.body.appendChild(node);
  }
  function fail(message) { result('fail', String(message && message.stack || message)); }
  function visibleEnabled(node) {
    if (!node || node.disabled || node.getAttribute('aria-disabled') === 'true') return false;
    var style = window.getComputedStyle(node);
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0;
  }
  function menuVisible(card) {
    var menu = card && card.querySelector('.dropdown-content');
    return !!(menu && window.getComputedStyle(menu).visibility !== 'hidden');
  }
  function waitForInitial() {
    var root = document.getElementById('personality-section');
    var card = document.querySelector('[data-personality-key="fixed-selected"]');
    if (!root || !root._openVibelyCardPaginationState || !card) return setTimeout(waitForInitial, 30);
    var toggle = card.querySelector('.dropdown > [tabindex="0"]');
    var actions = card.querySelectorAll('.dropdown-content button');
    var deletion = null;
    for (var i = 0; i < actions.length; i++) if (actions[i].textContent.trim() === 'Delete') deletion = actions[i];
    if (!toggle || !deletion) return fail('fixed selected card actions were not rendered');
    toggle.focus();
    deletion.focus();
    if (document.activeElement !== deletion || !visibleEnabled(deletion)) return fail('fixed selected delete action could not be focused while open');
    window.replaceSearchableCardContainer(root, `+string(replacementHTMLJSON)+`);
    var focused = document.activeElement;
    if (!focused || !focused.matches('[data-personality-key]') || !visibleEnabled(focused)) return fail('fixed selected deletion did not focus a visible surviving Personality card');
    if (focused.getAttribute('data-personality-key') === 'fixed-selected') return fail('focus remained on the deleted fixed selected card');
    if (menuVisible(focused)) return fail('fixed selected deletion reopened a surviving dropdown');
    result('pass', 'fixed selected Personality focus restored');
  }
  window.addEventListener('load', function() { setTimeout(waitForInitial, 50); });
  setTimeout(function() {
    if (!document.getElementById('browser-result')) fail('fixed Personality focus fixture timed out');
  }, 8000);
})();
</script></body>`, 1)

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check",
		"--no-first-run", "--window-size=1280,900", "--virtual-time-budget=9000", "--dump-dom", fixture.URL,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chrome timed out: %v\n%s", ctx.Err(), out)
	}
	require.NoError(t, err, "Chrome output: %s", out)
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 700
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("fixed Personality browser focus regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("fixed Personality browser focus regression did not report a result; DOM length=%d", len(dom))
	}
}

func TestCardPaginationProductionBrowserRestoresFocusFromFixedChannelCard(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	render := func(connected bool) string {
		var buf bytes.Buffer
		require.NoError(t, pages.SettingsContent(pages.ChannelsSettingsView{
			CurrentProjectID: "channels-focus-project",
			HasGitHubChannel: true,
			GitHubStatus: service.GitHubConnectionStatus{
				Configured:     true,
				Connected:      connected,
				AuthMode:       service.GitHubAuthModeApp,
				InstallationID: "12345",
			},
		}).Render(context.Background(), &buf))
		return buf.String()
	}
	initialHTML := render(true)
	replacementHTMLJSON, err := json.Marshal(render(false))
	require.NoError(t, err)

	var base bytes.Buffer
	require.NoError(t, layout.Base("Fixed Channel focus fixture", nil, "").Render(context.Background(), &base))
	var localBaseLines []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		localBaseLines = append(localBaseLines, line)
	}
	page := strings.Replace(strings.Join(localBaseLines, "\n"), "</main>", initialHTML+"</main>", 1)
	page = strings.Replace(page, "</head>", `<style>
	.dropdown-content { visibility: hidden; opacity: 0; }
	.dropdown:focus-within > .dropdown-content { visibility: visible; opacity: 1; }
	</style></head>`, 1)
	page = strings.Replace(page, "</body>", `<script>
(function() {
  function result(status, message) {
    var node = document.createElement('div');
    node.id = 'browser-result';
    node.setAttribute('data-status', status);
    node.textContent = message || status;
    document.body.appendChild(node);
  }
  function fail(message) { result('fail', String(message && message.stack || message)); }
  function visibleEnabled(node) {
    if (!node || node.disabled || node.getAttribute('aria-disabled') === 'true') return false;
    var style = window.getComputedStyle(node);
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0;
  }
  function menuVisible(card) {
    var menu = card && card.querySelector('.dropdown-content');
    return !!(menu && window.getComputedStyle(menu).visibility !== 'hidden');
  }
  function waitForInitial() {
    var root = document.getElementById('channels-container');
    var card = root && root.querySelector('[data-channel-type="github"]');
    if (!root || !root._openVibelyCardPaginationState || !card) return setTimeout(waitForInitial, 30);
    var disconnect = null;
    var buttons = card.querySelectorAll('button');
    for (var i = 0; i < buttons.length; i++) if (buttons[i].textContent.trim() === 'Disconnect') disconnect = buttons[i];
    if (!disconnect) return fail('fixed GitHub Disconnect action was not rendered');
    disconnect.focus();
    if (document.activeElement !== disconnect || !visibleEnabled(disconnect)) return fail('fixed GitHub Disconnect action could not be focused');
    document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap', {detail: {target: root}}));
    root.outerHTML = `+string(replacementHTMLJSON)+`;
    var replacement = document.getElementById('channels-container');
    document.body.dispatchEvent(new CustomEvent('htmx:afterSettle', {detail: {elt: replacement}}));
    setTimeout(checkRestoredFocus, 30);
  }
  function checkRestoredFocus() {
    var root = document.getElementById('channels-container');
    var focused = document.activeElement;
    var card = focused && focused.closest ? focused.closest('[data-channel-type]') : null;
    if (!root || !card || !root.contains(card) || !visibleEnabled(focused)) return fail('fixed Channel refresh did not focus a visible surviving card');
    if (menuVisible(card)) return fail('fixed Channel refresh reopened a surviving dropdown');
    var state = root._openVibelyCardPaginationState;
    if (!state || state.nextOffset !== 0) return fail('fixed Channel cards changed the empty webhook offset');
    result('pass', 'fixed Channel focus restored without changing webhook offset');
  }
  window.addEventListener('load', function() { setTimeout(waitForInitial, 50); });
  setTimeout(function() {
    if (!document.getElementById('browser-result')) fail('fixed Channel focus fixture timed out');
  }, 8000);
})();
</script></body>`, 1)

	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check",
		"--no-first-run", "--window-size=1280,900", "--virtual-time-budget=9000", "--dump-dom", fixture.URL,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chrome timed out: %v\n%s", ctx.Err(), out)
	}
	require.NoError(t, err, "Chrome output: %s", out)
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 700
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("fixed Channel browser focus regression failed: %s", html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("fixed Channel browser focus regression did not report a result; DOM length=%d", len(dom))
	}
}

func TestCardPaginationProductionBrowserPreservesGenericFocusAndPartialWindowOffset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	makeCards := func(start, end int) []models.LLMConfig {
		cards := make([]models.LLMConfig, 0, end-start)
		for i := start; i < end; i++ {
			cards = append(cards, models.LLMConfig{ID: fmt.Sprintf("focus-%02d", i), Name: fmt.Sprintf("Focus model %02d", i), Provider: models.ProviderTest, Model: fmt.Sprintf("focus-%02d", i)})
		}
		return cards
	}
	render := func(cards []models.LLMConfig, hasMore bool) string {
		var buf bytes.Buffer
		state := pages.CardListState{Preserved: []pages.CardListQueryValue{{Key: "partial", Value: "1"}}}
		require.NoError(t, pages.ModelsContentPageWithPaginationAndState(cards, nil, map[string]int{}, false, hasMore, state).Render(context.Background(), &buf))
		return buf.String()
	}
	initialHTML := render(makeCards(0, 35), false)
	sameHTMLJSON, err := json.Marshal(render(makeCards(0, 35), false))
	require.NoError(t, err)
	withoutFocused := append(makeCards(0, 10), makeCards(11, 35)...)
	deletedHTMLJSON, err := json.Marshal(render(withoutFocused, false))
	require.NoError(t, err)
	withoutLastHTMLJSON, err := json.Marshal(render(makeCards(0, 34), false))
	require.NoError(t, err)
	emptyHTMLJSON, err := json.Marshal(render(nil, false))
	require.NoError(t, err)
	partialHTMLJSON, err := json.Marshal(render(makeCards(0, 35), true))
	require.NoError(t, err)
	continuationHTML := render(makeCards(35, 41), false)

	var base bytes.Buffer
	require.NoError(t, layout.Base("Card pagination focus fixture", nil, "").Render(context.Background(), &base))
	var localBaseLines []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		localBaseLines = append(localBaseLines, line)
	}
	page := strings.Replace(strings.Join(localBaseLines, "\n"), "</main>", initialHTML+"</main>", 1)
	page = strings.Replace(page, "</head>", `<style>
.dropdown-content { visibility: hidden; opacity: 0; }
.dropdown:focus-within > .dropdown-content { visibility: visible; opacity: 1; }
</style></head>`, 1)
	page = strings.Replace(page, "</body>", `<script>
(function() {
  function result(status, message) {
    var node = document.createElement('div');
    node.id = 'browser-result';
    node.setAttribute('data-status', status);
    node.textContent = message || status;
    document.body.appendChild(node);
  }
  function fail(message) { result('fail', String(message && message.stack || message)); }
  window.addEventListener('error', function(event) { fail(event.error || event.message); });
  function modelCard(id) {
    return document.querySelector('[data-model-id="' + id + '"][data-model-provider]');
  }
  function cardButton(id, label) {
    var card = modelCard(id);
    if (!card) return null;
    var buttons = card.querySelectorAll('button');
    for (var i = 0; i < buttons.length; i++) if (buttons[i].textContent.trim() === label) return buttons[i];
    return null;
  }
  function decorateVisibleActions(scope) {
    var cards = scope.querySelectorAll('[data-model-id][data-model-provider]');
    for (var i = 0; i < cards.length; i++) {
      if (cards[i].querySelector('[data-fixture-visible-action]')) continue;
      var button = document.createElement('button');
      button.type = 'button';
      button.setAttribute('data-fixture-visible-action', '');
      button.textContent = 'Visible action';
      cards[i].appendChild(button);
    }
  }
  function visibleAction(id) {
    var card = modelCard(id);
    return card && card.querySelector('[data-fixture-visible-action]');
  }
  function isVisibleEnabled(node) {
    if (!node || node.disabled || node.getAttribute('aria-disabled') === 'true') return false;
    var style = window.getComputedStyle(node);
    return style.display !== 'none' && style.visibility !== 'hidden' && node.getClientRects().length > 0;
  }
  function focusHiddenAction(id, label) {
    var card = modelCard(id);
    var toggle = card && card.querySelector('.dropdown > [tabindex="0"]');
    var action = cardButton(id, label);
    if (!toggle || !action) return null;
    toggle.focus();
    action.focus();
    return document.activeElement === action && isVisibleEnabled(action) ? action : null;
  }
  function cardHasVisibleMenu(card) {
    var menu = card && card.querySelector('.dropdown-content');
    return !!(menu && window.getComputedStyle(menu).visibility !== 'hidden');
  }
  function replace(html, disabledVisibleActionID) {
    var shell = document.createElement('template');
    shell.innerHTML = html;
    decorateVisibleActions(shell.content);
    if (disabledVisibleActionID) {
      var disabledCard = shell.content.querySelector('[data-model-id="' + disabledVisibleActionID + '"][data-model-provider]');
      var disabledAction = disabledCard && disabledCard.querySelector('[data-fixture-visible-action]');
      if (disabledAction) disabledAction.disabled = true;
    }
    window.replaceSearchableCardContainer(document.getElementById('models-container'), shell.innerHTML);
  }
  function waitForInitial() {
    document.body.setAttribute('data-fixture-stage', 'waiting-initial');
    decorateVisibleActions(document);
    var button = visibleAction('focus-10');
    var root = document.getElementById('models-container');
    if (!button || !root || !root._openVibelyCardPaginationState) return setTimeout(waitForInitial, 30);
    document.body.setAttribute('data-fixture-stage', 'replace-hidden-survivor');
    var hiddenControl = focusHiddenAction('focus-09', 'Delete');
    if (!hiddenControl) return fail('hidden dropdown delete control could not be focused while its menu was open');
    replace(`+string(sameHTMLJSON)+`);
    var hiddenSurvivor = modelCard('focus-09');
    if (!hiddenSurvivor || document.activeElement !== hiddenSurvivor || !isVisibleEnabled(hiddenSurvivor)) return fail('hidden surviving action did not fall back to its visible card');
    if (cardHasVisibleMenu(hiddenSurvivor)) return fail('hidden surviving action fallback reopened its dropdown');
    document.body.setAttribute('data-fixture-stage', 'replace-visible-survivor');
    button = visibleAction('focus-10');
    button.focus();
    replace(`+string(sameHTMLJSON)+`);
    var surviving = visibleAction('focus-10');
    if (!surviving || document.activeElement !== surviving || !isVisibleEnabled(surviving)) return fail('visible enabled card control focus was not restored');
    document.body.setAttribute('data-fixture-stage', 'replace-disabled-survivor');
    surviving.focus();
    replace(`+string(sameHTMLJSON)+`, 'focus-10');
    var disabledSurvivor = modelCard('focus-10');
    var disabledAction = visibleAction('focus-10');
    if (!disabledSurvivor || !disabledAction || !disabledAction.disabled) return fail('disabled replacement control fixture was not rendered');
    if (document.activeElement !== disabledSurvivor || !isVisibleEnabled(disabledSurvivor)) return fail('disabled surviving action did not fall back to its visible card');
    document.body.setAttribute('data-fixture-stage', 'replace-deleted-next');
    if (!focusHiddenAction('focus-10', 'Delete')) return fail('next-fallback delete control could not be focused');
    replace(`+string(deletedHTMLJSON)+`);
    var fallback = modelCard('focus-11');
    if (!fallback || document.activeElement !== fallback || !isVisibleEnabled(fallback)) return fail('deleted card did not focus the visible next surviving card');
    if (cardHasVisibleMenu(fallback)) return fail('next-card fallback opened its dropdown');
    document.body.setAttribute('data-fixture-stage', 'replace-deleted-previous');
    if (!focusHiddenAction('focus-34', 'Delete')) return fail('previous-fallback delete control could not be focused');
    replace(`+string(withoutLastHTMLJSON)+`);
    var previous = modelCard('focus-33');
    if (!previous || document.activeElement !== previous || !isVisibleEnabled(previous)) return fail('deleted last card did not focus the visible previous surviving card');
    if (cardHasVisibleMenu(previous)) return fail('previous-card fallback opened its dropdown');
    previous.focus();
    replace(`+string(emptyHTMLJSON)+`);
    var search = document.querySelector('input[data-card-search="models"]');
    if (!search || document.activeElement !== search || !isVisibleEnabled(search)) return fail('empty replacement did not focus the visible search fallback');
    document.body.setAttribute('data-fixture-stage', 'replace-partial');
    replace(`+string(partialHTMLJSON)+`);
    var sentinel = document.querySelector('[data-card-pagination-sentinel]');
    if (sentinel && sentinel.scrollIntoView) sentinel.scrollIntoView({block: 'center'});
    var scroll = document.getElementById('main-content');
    if (scroll) { scroll.scrollTop = scroll.scrollHeight; scroll.dispatchEvent(new Event('scroll')); }
    window.dispatchEvent(new Event('scroll'));
    document.body.setAttribute('data-fixture-stage', 'wait-continuation');
    waitForContinuation();
  }
  function waitForContinuation() {
    var cards = document.querySelectorAll('#models-card-list [data-model-id][data-model-provider]');
    if (cards.length === 41) {
      var seen = Object.create(null);
      for (var i = 0; i < cards.length; i++) seen[cards[i].getAttribute('data-model-id')] = (seen[cards[i].getAttribute('data-model-id')] || 0) + 1;
      for (var n = 0; n < 41; n++) {
        var id = 'focus-' + String(n).padStart(2, '0');
        if (seen[id] !== 1) return fail('partial-window continuation lost or duplicated ' + id);
      }
      return result('pass', 'generic focus and partial-window continuation preserved');
    }
    setTimeout(waitForContinuation, 30);
  }
  window.addEventListener('load', function() { setTimeout(waitForInitial, 50); });
  setTimeout(function() {
    if (!document.getElementById('browser-result')) {
      var root = document.getElementById('models-container');
      fail('fixture timed out: stage=' + document.body.getAttribute('data-fixture-stage') + ' cards=' + document.querySelectorAll('#models-card-list [data-model-id][data-model-provider]').length + ' button=' + !!visibleAction('focus-10') + ' root=' + !!root + ' state=' + !!(root && root._openVibelyCardPaginationState) + ' loading=' + !!(root && root._openVibelyCardPaginationState && root._openVibelyCardPaginationState.loading));
    }
  }, 8000);
})();
</script></body>`, 1)

	var continuationRequests atomic.Int32
	var wrongOffsets atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/models" && r.URL.Query().Get("partial") == "1" {
			continuationRequests.Add(1)
			if r.URL.Query().Get("offset") != "35" {
				wrongOffsets.Add(1)
			}
			w.Header().Set(cardPageHasMoreHeader, "false")
			_, _ = w.Write([]byte(continuationHTML))
			return
		}
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check",
		"--no-first-run", "--window-size=1280,900", "--virtual-time-budget=10000", "--dump-dom", fixture.URL,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chrome timed out: %v (requests=%d wrongOffsets=%d)\n%s", ctx.Err(), continuationRequests.Load(), wrongOffsets.Load(), out)
	}
	require.NoError(t, err, "Chrome output: %s", out)
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 700
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser focus/partial-window regression failed (requests=%d wrongOffsets=%d): %s", continuationRequests.Load(), wrongOffsets.Load(), html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser focus/partial-window regression did not report a result; DOM length=%d", len(dom))
	}
	require.Equal(t, int32(1), continuationRequests.Load())
	require.Zero(t, wrongOffsets.Load())
}

func TestCardPaginationProductionBrowserRejectsStalePagesAndRecoversLiveRefresh(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	makeModel := func(id, name string) models.LLMConfig {
		return models.LLMConfig{ID: id, Name: name, Provider: models.ProviderTest, Model: id}
	}
	initial := make([]models.LLMConfig, 20)
	for i := range initial {
		initial[i] = makeModel(fmt.Sprintf("race-%02d", i), fmt.Sprintf("Race model %02d", i))
	}
	stale := []models.LLMConfig{makeModel("stale-page", "Stale page result")}
	fresh := []models.LLMConfig{makeModel("fresh-result", "Fresh search result")}
	retry := []models.LLMConfig{makeModel("retry-result", "Retry result")}
	liveInitial := make([]models.LLMConfig, 20)
	for i := range liveInitial {
		liveInitial[i] = makeModel(fmt.Sprintf("live-%02d", i), fmt.Sprintf("Live model %02d", i))
	}
	liveLater := []models.LLMConfig{makeModel("live-later", "Live later result")}

	renderWithState := func(cards []models.LLMConfig, state pages.CardListState) string {
		var buf bytes.Buffer
		if err := pages.ModelsContentPageWithPaginationAndState(cards, cards, map[string]int{}, false, true, state).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render models fragment: %v", err)
		}
		return buf.String()
	}
	render := func(cards []models.LLMConfig) string {
		return renderWithState(cards, pages.CardListState{})
	}
	initialHTML := render(initial)
	staleHTML := render(stale)
	freshHTML := render(fresh)
	retryHTML := render(retry)
	liveHTML := renderWithState(liveInitial, pages.CardListState{Preserved: []pages.CardListQueryValue{{Key: "live", Value: "1"}}})
	liveLaterHTML := render(liveLater)
	liveHTMLJSON, err := json.Marshal(liveHTML)
	require.NoError(t, err)

	var base bytes.Buffer
	require.NoError(t, layout.Base("Models browser race", nil, "").Render(context.Background(), &base))
	var localBaseLines []string
	for _, line := range strings.Split(base.String(), "\n") {
		if strings.Contains(line, "<script src=") || strings.Contains(line, "<link href=") || strings.Contains(line, `<link rel="stylesheet" href=`) {
			continue
		}
		localBaseLines = append(localBaseLines, line)
	}
	page := strings.Replace(strings.Join(localBaseLines, "\n"), "</main>", initialHTML+"</main>", 1)
	page = strings.Replace(page, "</body>", `<script>
(function() {
  function result(status, message) {
    var node = document.createElement('div');
    node.id = 'browser-result';
    node.setAttribute('data-status', status);
    node.textContent = message || status;
    document.body.appendChild(node);
  }
  function fail(error) { result('fail', String(error && error.stack || error)); }
  function cards() { return document.querySelectorAll('#models-card-list [data-model-provider]'); }
  function scrollToSentinel() {
    var sentinel = document.querySelector('[data-card-pagination-sentinel]');
    if (!sentinel) return;
    if (sentinel.scrollIntoView) sentinel.scrollIntoView({block: 'center'});
    var rect = sentinel.getBoundingClientRect();
    window.scrollTo(0, Math.max(0, window.scrollY + rect.top - window.innerHeight + 120));
    var scroll = document.getElementById('main-content');
    if (scroll) { scroll.scrollTop = scroll.scrollHeight; scroll.dispatchEvent(new Event('scroll')); }
    window.dispatchEvent(new Event('scroll'));
  }
  function setSearch(value) {
    var input = document.querySelector('input[data-card-search="models"]');
    input.value = value;
    input.dispatchEvent(new Event('input', {bubbles: true}));
  }
  function waitForPageOneStart() {
    fetch('/models-state').then(function(response) { return response.text(); }).then(function(value) {
      if (value === 'started') {
        setSearch('fresh');
        waitForFresh();
      } else {
        scrollToSentinel();
        setTimeout(waitForPageOneStart, 30);
      }
    }).catch(fail);
  }
  function waitForFresh() {
    if (document.querySelector('[data-model-id="fresh-result"]') && cards().length === 1) {
      fetch('/release-stale').then(function() { setSearch('retry'); waitForRetryError(); }).catch(fail);
      return;
    }
    setTimeout(waitForFresh, 30);
  }
  function waitForRetryError() {
    var error = document.querySelector('[data-card-pagination-error]');
    if (error && !error.classList.contains('hidden')) {
      var retryButton = document.querySelector('[data-card-pagination-retry]');
      retryButton.click();
      waitForRetryResult();
      return;
    }
    setTimeout(waitForRetryError, 30);
  }
  function waitForRetryResult() {
    var card = document.querySelector('[data-model-id="retry-result"]');
    var error = document.querySelector('[data-card-pagination-error]');
    if (card && cards().length === 1 && error && error.classList.contains('hidden')) {
      var oldRoot = document.getElementById('models-container');
      var oldState = oldRoot._openVibelyCardPaginationState;
      var teardown = {aborted: false, disconnected: false, staleScrolls: 0};
      if (oldState.observer) oldState.observer.disconnect();
      if (oldState.scrollTarget && oldState.scrollHandler) oldState.scrollTarget.removeEventListener('scroll', oldState.scrollHandler);
      oldState.controller = {abort: function() { teardown.aborted = true; }};
      oldState.observer = {disconnect: function() { teardown.disconnected = true; }};
      oldState.scrollTarget = window;
      oldState.scrollHandler = function() { teardown.staleScrolls++; };
      window.addEventListener('scroll', oldState.scrollHandler);
      window.replaceSearchableCardContainer(oldRoot, `+string(liveHTMLJSON)+`);
      window.dispatchEvent(new Event('scroll'));
      if (!teardown.aborted || !teardown.disconnected || teardown.staleScrolls !== 0) {
        return fail('direct replacement did not tear down the detached loader');
      }
      var nextRoot = document.getElementById('models-container');
      waitForLiveResult();
      return;
    }
    setTimeout(waitForRetryResult, 30);
  }
  function waitForLiveResult() {
    var card = document.querySelector('[data-model-id="live-later"]');
    var old = document.querySelector('[data-model-id="retry-result"]');
    if (card && cards().length === 21 && !old) {
      var root = document.getElementById('models-container');
      var state = root && root._openVibelyCardPaginationState;
	      var searchStateInURL = new URL(window.location.href).searchParams.get('search') === 'retry';
	      if (state && state.hasMore === false && searchStateInURL) {
	        return result('pass', 'stale page ignored, retry recovered, live refresh reset pagination, and search state remained in the URL');      }
    }
    scrollToSentinel();
    setTimeout(waitForLiveResult, 30);
  }
  window.addEventListener('load', function() {
    window._paginationInitialLocation = window.location.href;
    setTimeout(waitForPageOneStart, 50);
  });
  setTimeout(function() {
    if (!document.getElementById('browser-result')) {
      var root = document.getElementById('models-container');
      var state = root && root._openVibelyCardPaginationState;
      fail('pagination race fixture timed out; cards=' + Array.prototype.map.call(cards(), function(card) { return card.getAttribute('data-model-id'); }).join(',') + '; hasMore=' + (state && state.hasMore) + '; page=' + (state && state.page) + '; location=' + window.location.href + '; initial=' + window._paginationInitialLocation + '; url=' + (root && root.getAttribute('data-card-pagination-url')) + '; search=' + (document.querySelector('input[data-card-search="models"]') || {}).value);
    }
  }, 10000);
})();
</script></body>`, 1)

	var staleStarted atomic.Bool
	var staleReleased atomic.Bool
	var staleRelease = make(chan struct{})
	var staleRequests atomic.Int32
	var retryRequests atomic.Int32
	var livePageRequests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models-state" {
			if staleStarted.Load() {
				_, _ = w.Write([]byte("started"))
			} else {
				_, _ = w.Write([]byte("waiting"))
			}
			return
		}
		if r.URL.Path == "/release-stale" {
			if staleReleased.CompareAndSwap(false, true) {
				close(staleRelease)
			}
			return
		}
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.URL.Query().Get("live") == "1" {
				if r.URL.Query().Get("page") == "1" {
					livePageRequests.Add(1)
					w.Header().Set(cardPageHasMoreHeader, "false")
					_, _ = w.Write([]byte(liveLaterHTML))
				} else {
					w.Header().Set(cardPageHasMoreHeader, "true")
					_, _ = w.Write([]byte(liveHTML))
				}
				return
			}
			if r.URL.Query().Get("search") == "fresh" {
				w.Header().Set(cardPageHasMoreHeader, "false")
				_, _ = w.Write([]byte(freshHTML))
				return
			}
			if r.URL.Query().Get("search") == "retry" {
				if retryRequests.Add(1) == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set(cardPageHasMoreHeader, "false")
				_, _ = w.Write([]byte(retryHTML))
				return
			}
			if r.URL.Query().Get("page") == "1" {
				staleRequests.Add(1)
				staleStarted.Store(true)
				select {
				case <-staleRelease:
				case <-r.Context().Done():
					return
				case <-time.After(2 * time.Second):
				}
				w.Header().Set(cardPageHasMoreHeader, "true")
				_, _ = w.Write([]byte(staleHTML))
				return
			}
			w.Header().Set(cardPageHasMoreHeader, "false")
			_, _ = w.Write([]byte(initialHTML))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer fixture.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-extensions", "--no-default-browser-check",
		"--no-first-run", "--window-size=1280,900", "--virtual-time-budget=12000", "--dump-dom", fixture.URL,
	)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("Chrome timed out: %v (staleStarted=%v staleRequests=%d retryRequests=%d livePageRequests=%d)\n%s", ctx.Err(), staleStarted.Load(), staleRequests.Load(), retryRequests.Load(), livePageRequests.Load(), out)
	}
	require.NoError(t, err, "Chrome output: %s", out)
	dom := string(out)
	if !strings.Contains(dom, `id="browser-result" data-status="pass"`) {
		idx := strings.Index(dom, `id="browser-result"`)
		if idx >= 0 {
			end := idx + 700
			if end > len(dom) {
				end = len(dom)
			}
			t.Fatalf("browser pagination race failed (staleStarted=%v staleRequests=%d retryRequests=%d livePageRequests=%d): %s", staleStarted.Load(), staleRequests.Load(), retryRequests.Load(), livePageRequests.Load(), html.UnescapeString(dom[idx:end]))
		}
		t.Fatalf("browser pagination race did not report a result; DOM length=%d", len(dom))
	}
	require.Equal(t, int32(1), staleRequests.Load())
	require.Equal(t, int32(2), retryRequests.Load())
	require.Equal(t, int32(1), livePageRequests.Load())
}
