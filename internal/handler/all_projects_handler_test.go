package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsViewsAndOwnership(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	first := createProject(t, h, "First workspace")
	second := createProject(t, h, "Second workspace")
	for _, p := range []*models.Project{first, second} {
		task := models.Task{ProjectID: p.ID, Title: p.Name + " task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, stores.Tasks.Create(t.Context(), &task))
		schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC(), RepeatType: models.RepeatOnce, RepeatInterval: 1}
		require.NoError(t, stores.Schedules.Create(t.Context(), &schedule))
		_, _, err := stores.Automations.PublishRegistered(t.Context(), models.AutomationRegisteredPublication{
			ProjectID: p.ID, StableKey: "all-projects-test", Name: p.Name + " automation", AutomationType: "scheduled", AdapterKey: "custom", CreatedVia: "test",
			Resources: []models.AutomationResourceBinding{{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"}},
			Nodes:     []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"}},
		})
		require.NoError(t, err)
		require.NoError(t, stores.Usage.RecordUsageEvent(t.Context(), &models.LLMUsageEvent{ProjectID: p.ID, Provider: "test", Model: "test", Operation: "task", InputTokens: 100, TotalTokens: 100, OccurredAt: time.Now().UTC().Add(-time.Minute)}))
	}
	require.NoError(t, stores.Tasks.Create(t.Context(), &models.Task{ProjectID: first.ID, Title: "Private chat title", Category: models.CategoryChat, Status: models.StatusPending, Priority: 2}))
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		return rec
	}
	sidebar := get("/tasks/sidebar?project_id=" + models.AllProjectsID).Body.String()
	require.Contains(t, sidebar, "First workspace task")
	require.Contains(t, sidebar, "Second workspace task")
	require.Contains(t, sidebar, `data-task-list-project="`+first.ID+`"`)
	require.Contains(t, sidebar, `&amp;project_id=`+second.ID)
	require.NotContains(t, sidebar, "Private chat title")
	require.NotContains(t, sidebar, "all-projects-choose")
	scopedSidebar := get("/tasks/sidebar?project_id=" + first.ID).Body.String()
	require.NotContains(t, scopedSidebar, "Second workspace task")
	require.NotContains(t, scopedSidebar, "data-task-list-project")
	body := get("/tasks?project_id=" + models.AllProjectsID).Body.String()
	require.Contains(t, body, `data-project-tab="`+models.AllProjectsID+`"`)
	require.Contains(t, body, "First workspace task")
	require.Contains(t, body, "Second workspace task")
	require.NotContains(t, body, "Private chat title")
	require.Contains(t, body, `data-owner-project="`+second.ID+`"`)
	filtered := get("/tasks?project_id=" + models.AllProjectsID + "&filter_project=" + first.ID).Body.String()
	require.Contains(t, filtered, "First workspace task")
	require.NotContains(t, filtered, "Second workspace task")
	scoped := get("/tasks?project_id=" + first.ID).Body.String()
	require.NotContains(t, scoped, "Second workspace task")
	for _, page := range []string{"analytics", "automations"} {
		require.Contains(t, get("/"+page+"?project_id="+models.AllProjectsID).Body.String(), `data-all-projects-view="`+page+`"`)
	}

	analytics := get("/analytics?project_id=" + models.AllProjectsID).Body.String()
	require.Contains(t, analytics, `data-all-total-tokens>200</strong>`)
	require.Contains(t, get("/analytics?project_id="+models.AllProjectsID+"&filter_project="+first.ID).Body.String(), `data-all-total-tokens>100</strong>`)
	firstPage := get("/automations?project_id=" + models.AllProjectsID + "&card_page=1&page_size=1").Body.String()
	require.Contains(t, firstPage, "First workspace automation")
	require.NotContains(t, firstPage, "Second workspace automation")
	secondPage := get("/automations?project_id=" + models.AllProjectsID + "&card_page=1&page_size=1&offset=1").Body.String()
	require.Contains(t, secondPage, "Second workspace automation")
	require.NotContains(t, secondPage, "First workspace automation")
	chooser := get("/tasks/new?project_id=" + models.AllProjectsID).Body.String()
	require.Contains(t, chooser, `/tasks/new?project_id=`+first.ID)
	require.Contains(t, chooser, `id="all-projects-choose"`)
	require.Contains(t, get("/projects/new?project_id="+models.AllProjectsID).Body.String(), `id="new_project_modal"`)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+models.AllProjectsID, nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "all-projects-results")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "Second workspace task")
	require.NotContains(t, rec.Body.String(), "desktop-project-titlebar")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tasks?project_id="+models.AllProjectsID, strings.NewReader("title=wrong")))
	require.Equal(t, 400, rec.Code)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks?project_id="+models.AllProjectsID+"&filter_project=missing", nil))
	require.Equal(t, 400, rec.Code)
	project, err := h.projectSvc.GetByID(t.Context(), models.AllProjectsID)
	require.NoError(t, err)
	require.Nil(t, project)
}

func TestAllProjectsPreferenceSurvivesLaunch(t *testing.T) {
	h, app, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"project_id":"__all_projects__","pinned_project_ids":["__all_projects__"]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 204, rec.Code, rec.Body.String())
	selected, err := h.settingsRepo.Get(t.Context(), uiPreferenceSelectedProjectIDKey)
	require.NoError(t, err)
	require.Equal(t, models.AllProjectsID, selected)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/chat", nil))
	require.Equal(t, 303, rec.Code)
	require.Equal(t, "/tasks?project_id="+models.AllProjectsID, rec.Header().Get("Location"))
}

func TestAllProjectsSavedSelectionOnWorkspaceEntry(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	project := createProject(t, h, "Explicit project")
	require.NoError(t, h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, models.AllProjectsID))
	for _, page := range []string{"tasks", "analytics", "automations"} {
		t.Run(page, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/"+page+"?search=needle&range=7d", nil)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			require.Equal(t, 200, rec.Code)
			require.Contains(t, rec.Body.String(), `data-all-projects-view="`+page+`"`)
			require.Equal(t, "needle", req.URL.Query().Get("search"))
			require.Equal(t, "7d", req.URL.Query().Get("range"))
		})
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks/new", nil))
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `id="all-projects-choose"`)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tasks?project_id="+project.ID, nil))
	require.Equal(t, 200, rec.Code)
	require.NotContains(t, rec.Body.String(), `data-all-projects-view="tasks"`)
}

func TestAllProjectsDesktopLaunchKeepsRestorationBootstrap(t *testing.T) {
	h, app, _ := setupTestHandler(t)
	h.SetDesktopMode(true)
	require.NoError(t, h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, models.AllProjectsID))
	require.NoError(t, h.settingsRepo.Set(t.Context(), "ui.project_locations", `{"__all_projects__":"/analytics?project_id=__all_projects__&range=7d#usage"}`))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/chat", nil))
	require.Equal(t, 200, rec.Code)
	require.Empty(t, rec.Header().Get("Location"), "the original launch URL must reach the shell's saved-location/outbox restoration")
	require.Contains(t, rec.Body.String(), `data-openvibely-runtime="desktop"`)
	require.Contains(t, rec.Body.String(), `/analytics?project_id=__all_projects__&amp;range=7d#usage`)
	require.Contains(t, rec.Body.String(), `data-all-projects-view="tasks"`)
}
