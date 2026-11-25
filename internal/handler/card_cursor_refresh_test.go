package handler

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/cardcursor"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/stretchr/testify/require"
)

func TestCardCursorActionScopeMatchesDisplayedList(t *testing.T) {
	for _, tc := range []struct{ name, action, list, workspace string }{
		{"automation pause", "/automations/id/pause?project_id=project1", "/automations?project_id=" + models.AllProjectsID, models.AllProjectsID},
		{"automation resume", "/automations/id/resume?project_id=project1", "/automations?project_id=" + models.AllProjectsID, models.AllProjectsID},
		{"automation run", "/automations/id/run-now?project_id=project1", "/automations?project_id=" + models.AllProjectsID, models.AllProjectsID},
		{"skills enabled", "/skills/backup/enabled?skill_project_id=project1", "/skills", ""},
		{"skills always use", "/skills/backup/always_use?skill_project_id=project1", "/skills", ""},
		{"skills edit", "/skills/backup?skill_project_id=project1", "/skills", ""},
		{"skills delete", "/skills/backup?target_scope=global", "/skills", ""},
		{"personality select", "/personality/save?personality=analyst", "/personality", ""},
		{"model default delete", "/models/id?new_default_id=other", "/models", ""},
		{"agents", "/agents/id", "/agents", ""},
		{"alerts", "/alerts/id/read", "/alerts", ""},
		{"channels", "/channels", "/channels", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			action := e.NewContext(httptest.NewRequest(http.MethodPost, tc.action, nil), httptest.NewRecorder())
			if tc.workspace != "" {
				action.SetRequest(action.Request().WithContext(layout.WithWorkspaceProject(action.Request().Context(), tc.workspace)))
			}
			initCardCursor(action)
			token := cardcursor.Encoded(action.Request().Context())
			require.NotEmpty(t, token)
			u, err := url.Parse(tc.list)
			require.NoError(t, err)
			q := u.Query()
			q.Set("cursor", token)
			u.RawQuery = q.Encode()
			next := e.NewContext(httptest.NewRequest(http.MethodGet, u.String(), nil), httptest.NewRecorder())
			initCardCursor(next)
			require.NoError(t, cardcursor.From(next.Request().Context()).Err)
		})
	}
}

func cursorFromRefresh(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	match := regexp.MustCompile(`data-card-pagination-cursor="([^"]+)"`).FindStringSubmatch(rec.Body.String())
	require.Len(t, match, 2)
	return html.UnescapeString(match[1])
}

// Compare every returned card, not just the status: the refreshed first batch
// and its continuations must equal a fresh complete traversal of the same list.
func assertRefreshContinuation(t *testing.T, e *echo.Echo, rec *httptest.ResponseRecorder, path, attribute string) {
	t.Helper()
	pattern := regexp.MustCompile(`<div\b[^>]*\b` + attribute + `="([^"]+)"`)
	collect := func(start *httptest.ResponseRecorder) []string {
		var got []string
		for i := 0; i < 10; i++ {
			for _, match := range pattern.FindAllStringSubmatch(start.Body.String(), -1) {
				got = append(got, html.UnescapeString(match[1]))
			}
			token := cursorFromRefresh(t, start)
			u, err := url.Parse(path)
			require.NoError(t, err)
			q := u.Query()
			q.Set("cursor", token)
			q.Set("card_page", "1")
			u.RawQuery = q.Encode()
			next := serveCardPageRequest(t, e, u.String())
			require.Equal(t, http.StatusOK, next.Code, next.Body.String())
			if len(pattern.FindAllStringSubmatch(next.Body.String(), -1)) == 0 {
				return got
			}
			start = next
		}
		t.Fatal("pagination did not terminate")
		return nil
	}
	got := collect(rec)
	want := collect(serveCardPageRequest(t, e, path))
	require.Greater(t, len(want), 20)
	require.Equal(t, want, got)
	seen := map[string]bool{}
	for _, id := range got {
		require.False(t, seen[id], "duplicate %s", id)
		seen[id] = true
	}
}

func TestCardCursorAfterModelDefaultDeletion(t *testing.T) {
	_, e, repo, _ := setupTestHandlerWithStores(t)
	var ids []string
	for i := 0; i < 45; i++ {
		m := &models.LLMConfig{Name: fmt.Sprintf("Cursor model %03d", i), Provider: models.ProviderTest, Model: "test", IsDefault: i == 0}
		require.NoError(t, repo.Create(t.Context(), m))
		ids = append(ids, m.ID)
	}
	req := httptest.NewRequest(http.MethodDelete, "/models/"+ids[0]+"?new_default_id="+ids[1]+"&search=Cursor+model", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertRefreshContinuation(t, e, rec, "/models?search=Cursor+model", "data-model-name")
}

func TestCardCursorAfterPersonalitySelection(t *testing.T) {
	h, e, _, stores := setupTestHandlerWithStores(t)
	h.SetCustomPersonalityRepo(stores.CustomPersonalities)
	for i := 0; i < 45; i++ {
		require.NoError(t, stores.CustomPersonalities.Create(t.Context(), &models.CustomPersonality{Name: fmt.Sprintf("Cursor personality %03d", i), Key: fmt.Sprintf("cursor_%03d", i), SystemPrompt: "A sufficiently long personality prompt for testing."}))
	}
	req := httptest.NewRequest(http.MethodPost, "/personality/save?personality=cursor_000&search=Cursor+personality", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertRefreshContinuation(t, e, rec, "/personality?search=Cursor+personality", "data-personality-key")
}

func TestCardCursorAfterAllProjectsAutomationActions(t *testing.T) {
	tc := NewStoreTestContext(t)
	p := tc.CreateProject().WithName("Project 1").Build()
	repo := tc.stores.Automations
	registration := service.NewAutomationRegistrationService(repo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(repo), registration)
	tc.handler.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(repo, tc.scheduleRepo))
	var target string
	for i := 0; i < 25; i++ {
		task := &models.Task{ProjectID: p.ID, Title: fmt.Sprintf("Task %d", i), Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "test"}
		require.NoError(t, tc.taskRepo.Create(t.Context(), task))
		next := time.Now().UTC().Add(time.Hour)
		schedule := &models.Schedule{TaskID: task.ID, RunAt: next, NextRun: &next, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
		require.NoError(t, tc.scheduleRepo.Create(t.Context(), schedule))
		def, _, err := registration.Register(t.Context(), service.AutomationRegistrationRequest{ProjectID: p.ID, Name: fmt.Sprintf("Cursor automation %03d", i), StableKey: fmt.Sprint(i), AdapterKey: service.AutomationAdapterNativeSDLC, Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID}, {NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		}})
		require.NoError(t, err)
		target = def.Automation.ID
	}
	for _, action := range []string{"pause", "resume", "run-now"} {
		t.Run(action, func(t *testing.T) {
			path := "/automations?project_id=" + models.AllProjectsID + "&search=Cursor+automation"
			rec := tc.HTMX().Post("/automations/" + target + "/" + action + "?project_id=" + models.AllProjectsID + "&search=Cursor+automation").WithForm(url.Values{"return_to": {"portfolio"}}).Execute()
			assertRefreshContinuation(t, tc.echo, rec, path, "data-automation-url")
		})
	}
}

func TestCardCursorAfterSkillActions(t *testing.T) {
	h, e, _, stores := setupTestHandlerWithStores(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	p := &models.Project{Name: "Project 1", RepoPath: t.TempDir()}
	require.NoError(t, stores.Projects.Create(t.Context(), p))
	for i := 0; i < 45; i++ {
		writeStandaloneSkill(t, root, fmt.Sprintf("cursor_skill_%03d", i), fmt.Sprintf("Cursor skill %03d", i), "description", "global")
	}
	for _, tc := range []struct{ method, suffix, body string }{
		{"POST", "/enabled", `{"scope":"global","enabled":false}`},
		{"POST", "/enabled", `{"scope":"global","enabled":true}`},
		{"POST", "/always_use", `{"scope":"global","always_use":true}`},
		{"PUT", "", `{"scope":"global","handle":"cursor_skill_000","name":"Cursor skill 000 edited","description":"description","body":"Instructions for this skill","enabled":true}`},
		{"DELETE", "", ``},
	} {
		t.Run(tc.method+tc.suffix+tc.body, func(t *testing.T) {
			query := url.Values{"project_id": {p.ID}, "skill_project_id": {p.ID}, "search": {"Cursor skill"}, "target_scope": {"global"}}
			req := httptest.NewRequest(tc.method, "/skills/cursor_skill_000"+tc.suffix+"?"+query.Encode(), strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assertRefreshContinuation(t, e, rec, "/skills?project_id="+p.ID+"&search=Cursor+skill", "data-skill-scroll-anchor")
		})
	}
}

func TestCardCursorListFiltersRemainBoundToCursor(t *testing.T) {
	for _, tc := range []struct{ path, filters string }{
		{"/automations", "lifecycle_state=active&health_state=healthy&automation_type=custom&adapter=custom&sort=updated_desc"},
		{"/alerts", "read=unread&severity=error&decision_state=pending&processing_state=unclaimed&implementation_task_linked=false&type=custom&source=test&sort=oldest"},
		{"/models", "provider=anthropic&default=false&auth_status=connected&kind=direct&sort=provider"},
		{"/agents", "enabled=true&scope=project&origin=custom&sort=created_desc"},
		{"/skills", "enabled=true&scope=project&always_use=true&archived=false&source=project&sort=source"},
		{"/personality", "kind=custom&active=true&sort=name_desc"},
		{"/channels", "type=webhook&connection_state=connected&webhook_enabled=true&sort=name_desc"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			e := echo.New()
			query, err := url.ParseQuery(tc.filters)
			require.NoError(t, err)
			query.Set("project_id", "project1")
			query.Set("search", "Backup")
			initial := e.NewContext(httptest.NewRequest("GET", tc.path+"?"+query.Encode(), nil), httptest.NewRecorder())
			initCardCursor(initial)
			token := cardcursor.Encoded(initial.Request().Context())
			for key := range query {
				t.Run(key, func(t *testing.T) {
					changed := url.Values{}
					for k, v := range query {
						changed[k] = v
					}
					changed.Del(key)
					changed.Set("cursor", token)
					next := e.NewContext(httptest.NewRequest("GET", tc.path+"?"+changed.Encode(), nil), httptest.NewRecorder())
					initCardCursor(next)
					require.ErrorIs(t, cardcursor.From(next.Request().Context()).Err, cardcursor.ErrInvalid)
				})
			}
		})
	}
}

func TestCardCursorEquivalentListInputs(t *testing.T) {
	e := echo.New()
	first := e.NewContext(httptest.NewRequest("GET", "/skills?search=%20Backup%20&sort=invalid&enabled=invalid", nil), httptest.NewRecorder())
	initCardCursor(first)
	token := cardcursor.Encoded(first.Request().Context())
	next := e.NewContext(httptest.NewRequest("GET", "/skills?search=Backup&sort=name_asc&cursor="+url.QueryEscape(token), nil), httptest.NewRecorder())
	initCardCursor(next)
	require.NoError(t, cardcursor.From(next.Request().Context()).Err)
}

// Names in action URLs must not change the kind of the displayed card list.
func TestCardCursorAfterNamedSkillActions(t *testing.T) {
	for _, handle := range []string{"models", "agents", "automations", "alerts", "webhook_backup", "channels_helper"} {
		t.Run(handle, func(t *testing.T) {
			h, e, _, _ := setupTestHandlerWithStores(t)
			root := t.TempDir()
			h.SetAgentSkillRoot(root)
			for i := 0; i < 45; i++ {
				writeStandaloneSkill(t, root, fmt.Sprintf("review_%03d", i), fmt.Sprintf("Review skill %03d", i), "description", "global")
			}
			writeStandaloneSkill(t, root, handle, "Review target", "description", "global")
			for _, action := range []struct{ method, suffix, body string }{
				{"POST", "/enabled", `{"scope":"global","enabled":false}`},
				{"POST", "/always_use", `{"scope":"global","always_use":true}`},
				{"DELETE", "", ``},
			} {
				req := httptest.NewRequest(action.method, "/skills/"+handle+action.suffix+"?search=Review&target_scope=global", strings.NewReader(action.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				assertRefreshContinuation(t, e, rec, "/skills?search=Review", "data-skill-scroll-anchor")
			}
		})
	}
}

func TestCardCursorAfterNamedPersonalityActions(t *testing.T) {
	for _, key := range []string{"models", "agents", "automations", "alerts", "skills", "webhook_backup", "channels_helper"} {
		t.Run(key, func(t *testing.T) {
			h, e, _, stores := setupTestHandlerWithStores(t)
			h.SetCustomPersonalityRepo(stores.CustomPersonalities)
			for i := 0; i < 45; i++ {
				require.NoError(t, stores.CustomPersonalities.Create(t.Context(), &models.CustomPersonality{Name: fmt.Sprintf("Review personality %03d", i), Key: fmt.Sprintf("review_%03d", i), SystemPrompt: "A sufficiently long personality prompt for testing."}))
			}
			require.NoError(t, stores.CustomPersonalities.Create(t.Context(), &models.CustomPersonality{Name: "Review target", Key: key, SystemPrompt: "A sufficiently long personality prompt for testing."}))
			for _, method := range []string{http.MethodPut, http.MethodDelete} {
				req := serversRequest(method, "/personality/custom/"+key+"?search=Review", url.Values{"name": {"Review target edited"}, "system_prompt": {"A sufficiently long personality prompt for testing."}})
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				assertRefreshContinuation(t, e, rec, "/personality?search=Review", "data-personality-key")
			}
		})
	}
}
