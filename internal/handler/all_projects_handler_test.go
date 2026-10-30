package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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
	require.Contains(t, sidebar, `&amp;project_id=`+models.AllProjectsID)
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

	for _, endpoint := range []string{"dashboard", "skills", "success-failure-rates", "avg-execution-time-by-task", "avg-execution-time-by-agent", "execution-trends-by-hour", "task-run-activity", "agent-usage-by-project", "most-frequent-tasks", "failed-task-patterns"} {
		response := get("/api/analytics/" + endpoint + "?project_id=" + models.AllProjectsID)
		require.Contains(t, response.Header().Get("Content-Type"), "application/json", endpoint)
	}
	analytics := get("/analytics?project_id=" + models.AllProjectsID).Body.String()
	require.Contains(t, analytics, `id="analytics-data" data-project-id="__all_projects__"`)
	require.Contains(t, get("/api/analytics/usage?project_id="+models.AllProjectsID+"&projection=local").Body.String(), `"total_tokens":200`)
	require.Contains(t, get("/analytics?project_id="+models.AllProjectsID+"&filter_project="+first.ID).Body.String(), `data-project-id="`+first.ID+`" data-workspace-project-id="__all_projects__"`)
	require.Contains(t, get("/api/analytics/usage?project_id="+first.ID+"&projection=local").Body.String(), `"total_tokens":100`)
	firstPage := get("/automations?project_id=" + models.AllProjectsID + "&card_page=1&page_size=1").Body.String()
	require.Contains(t, firstPage, "First workspace automation")
	require.NotContains(t, firstPage, "Second workspace automation")
	secondPage := get("/automations?project_id=" + models.AllProjectsID + "&card_page=1&page_size=1&offset=1").Body.String()
	require.Contains(t, secondPage, "Second workspace automation")
	require.NotContains(t, secondPage, "First workspace automation")
	chooser := get("/tasks/new?project_id=" + models.AllProjectsID).Body.String()
	require.Contains(t, chooser, `destination_project=`+first.ID)
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
	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/tasks?project_id="+models.AllProjectsID, rec.Header().Get("Location"))
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
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), `id="all-projects-chat"`)
	require.NotContains(t, rec.Body.String(), `id="all-projects-choose"`)
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

func TestAllProjectsStaleFilterRefreshesWholeView(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	for _, page := range []string{"tasks", "automations", "analytics"} {
		for _, target := range []string{"main-content", "all-projects-results"} {
			t.Run(page+"/"+target, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, "/"+page+"?project_id="+models.AllProjectsID+"&filter_project=deleted&search=retained&range=7d&offset=20&card_page=1", nil)
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", target)
				rec := httptest.NewRecorder()
				app.ServeHTTP(rec, req)
				require.Equal(t, 200, rec.Code)
				if target == "all-projects-results" {
					require.Equal(t, "/"+page+"?project_id="+models.AllProjectsID+"&range=7d&search=retained", rec.Header().Get("X-OpenVibely-All-Projects-Location"))
					require.Empty(t, rec.Header().Get("HX-Retarget"))
					require.Empty(t, rec.Body.String())
					return
				}
				require.Equal(t, "#main-content", rec.Header().Get("HX-Retarget"))
				require.Equal(t, "innerHTML", rec.Header().Get("HX-Reswap"))
				require.Equal(t, "/"+page+"?project_id="+models.AllProjectsID+"&range=7d&search=retained", rec.Header().Get("HX-Replace-Url"))
				require.Contains(t, rec.Body.String(), `data-all-projects-view="`+page+`"`)
				require.Contains(t, rec.Body.String(), `name="filter_project"`)
				require.NotContains(t, rec.Body.String(), "filter_project=deleted")
			})
		}
	}
}

func TestAllProjectsTaskWorkspaceKeepsOwnerForEdits(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	project := createProject(t, h, "Task owner")
	task := models.Task{ProjectID: project.ID, Title: "Original", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(t.Context(), &task))
	path := "/tasks/" + task.ID + "?project_id=" + models.AllProjectsID
	full := httptest.NewRecorder()
	app.ServeHTTP(full, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, full.Code)
	require.Contains(t, full.Body.String(), `value="__all_projects__" selected`)
	require.Contains(t, full.Body.String(), `data-project-id="`+project.ID+`"`)

	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader("title=Updated&prompt=Keep+owner&priority=2&category=backlog"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	saved, err := stores.Tasks.GetByID(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "Updated", saved.Title)
	require.Equal(t, project.ID, saved.ProjectID)
	require.Contains(t, rec.Body.String(), "/tasks?project_id="+models.AllProjectsID)
	req = httptest.NewRequest(http.MethodDelete, path+"&redirect=list", nil)
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "/tasks?project_id="+models.AllProjectsID, rec.Header().Get("HX-Redirect"))
}

func TestAllProjectsResourceNavigationAndCreation(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	first := createProject(t, h, "Navigation first")
	second := createProject(t, h, "Navigation second")
	var tasks []models.Task
	var automationID string
	for _, project := range []*models.Project{first, second} {
		task := models.Task{ProjectID: project.ID, Title: project.Name + " task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, stores.Tasks.Create(t.Context(), &task))
		tasks = append(tasks, task)
		schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC(), RepeatType: models.RepeatOnce, RepeatInterval: 1}
		require.NoError(t, stores.Schedules.Create(t.Context(), &schedule))
		definition, _, err := stores.Automations.PublishRegistered(t.Context(), models.AutomationRegisteredPublication{ProjectID: project.ID, StableKey: "navigation", Name: project.Name + " automation", AutomationType: "scheduled", AdapterKey: "custom", CreatedVia: "test", Resources: []models.AutomationResourceBinding{{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"}}, Nodes: []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"}}})
		require.NoError(t, err)
		automationID = definition.Automation.ID
	}
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	for _, resource := range []string{"tasks", "automations"} {
		body := get("/breadcrumb-selectors/" + resource + "?project_id=" + models.AllProjectsID)
		require.Contains(t, body, "Navigation first")
		require.Contains(t, body, "Navigation second")
		require.Contains(t, body, "project_id="+models.AllProjectsID)
		scoped := get("/breadcrumb-selectors/" + resource + "?project_id=" + first.ID)
		require.NotContains(t, scoped, "Navigation second")
	}
	searched := get("/breadcrumb-selectors/tasks?project_id=" + models.AllProjectsID + "&search=second&current_id=" + tasks[1].ID)
	require.Contains(t, searched, tasks[1].Title)
	require.NotContains(t, searched, tasks[0].Title)
	live := get("/automations/" + automationID + "?project_id=" + models.AllProjectsID)
	require.Contains(t, live, `value="__all_projects__" selected`)
	require.Contains(t, live, `data-project-id="`+second.ID+`"`)
	require.Contains(t, live, `data-refresh-url="/automations/`+automationID+`?project_id=__all_projects__"`)
	form := get("/tasks/new?project_id=" + models.AllProjectsID + "&destination_project=" + second.ID)
	require.Contains(t, form, `value="__all_projects__" selected`)
	require.Contains(t, form, "destination_project="+second.ID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+models.AllProjectsID+"&destination_project="+second.ID+"&from=new&thread=1", strings.NewReader("title=Created+in+All&message=New+task&category=backlog&priority=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	app.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := rec.Header().Get("X-Created-Task-ID")
	require.NotEmpty(t, id)
	require.Equal(t, "/tasks/"+id+"?project_id="+models.AllProjectsID, rec.Header().Get("HX-Push-Url"))
	task, err := stores.Tasks.GetByID(t.Context(), id)
	require.NoError(t, err)
	require.Equal(t, second.ID, task.ProjectID)
}

func TestAllProjectsNewAndDuplicateAutomationKeepWorkspace(t *testing.T) {
	tc := NewStoreTestContext(t)
	project := tc.CreateProject().WithName("Automation destination").Build()
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(tc.stores.Automations, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(tc.stores.Automations, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(tc.stores.Automations), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(tc.stores.Automations, tc.scheduleRepo))
	seed, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
	require.NoError(t, err)
	seed.Name = "Source automation"
	seed.Nodes = []models.AutomationDraftNode{{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review changes", "category": "backlog", "priority": 2}}}
	saved, err := compiler.Save(t.Context(), service.AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: seed})
	require.NoError(t, err)
	chooser := tc.HTTP().Get("/automations/new?project_id=" + models.AllProjectsID).Execute()
	require.Equal(t, http.StatusOK, chooser.Code)
	require.Contains(t, chooser.Body.String(), "/automations/new?project_id="+models.AllProjectsID+"&amp;destination_project="+project.ID)
	for _, path := range []string{
		"/automations/new?project_id=" + models.AllProjectsID + "&destination_project=" + project.ID,
		"/automations/" + saved.Definition.Automation.ID + "/duplicate?project_id=" + models.AllProjectsID,
	} {
		for _, fragment := range []bool{false, true} {
			client := tc.HTTP()
			if fragment {
				client = tc.HTMX()
			}
			response := client.Get(path).Execute()
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			body := response.Body.String()
			require.Contains(t, body, `id="automation-builder"`)
			require.Contains(t, body, `href="/automations?project_id=__all_projects__"`)
			require.Contains(t, body, `/automations/builder?project_id=__all_projects__&amp;destination_project=`+project.ID)
			require.Contains(t, body, `name="project_id" value="`+project.ID+`"`)
			if !fragment {
				require.Contains(t, body, `value="__all_projects__" selected`)
			}
		}
	}
	definitions, err := tc.stores.Automations.ListPortfolioCards(t.Context(), project.ID)
	require.NoError(t, err)
	require.Len(t, definitions, 1, "opening drafts must not save them")
	seed.Name = "Source A survivor"
	_, err = compiler.Save(t.Context(), service.AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: seed})
	require.NoError(t, err)
	returnList := "/automations?project_id=__all_projects__&search=Source&card_page=1&page_size=1&offset=1&filter_project=" + project.ID
	deleted := tc.HTMX().Post("/automations/" + saved.Definition.Automation.ID + "/delete?project_id=__all_projects__&return_list=" + url.QueryEscape(returnList)).Execute()
	require.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())
	require.Equal(t, returnList, deleted.Header().Get("HX-Redirect"))
	for _, target := range []string{"", "main-content", "all-projects-results"} {
		request := httptest.NewRequest(http.MethodGet, returnList, nil)
		if target != "" {
			request.Header.Set("HX-Request", "true")
			request.Header.Set("HX-Target", target)
		}
		response := httptest.NewRecorder()
		tc.echo.ServeHTTP(response, request)
		header := "Location"
		if target == "" {
			require.Equal(t, http.StatusSeeOther, response.Code)
		} else {
			require.Equal(t, http.StatusOK, response.Code)
			header = "HX-Replace-Url"
			if target == "all-projects-results" {
				header = "X-OpenVibely-All-Projects-Location"
			} else {
				require.Contains(t, response.Body.String(), "Source A survivor")
			}
		}
		corrected, err := url.Parse(response.Header().Get(header))
		require.NoError(t, err)
		require.Equal(t, "0", corrected.Query().Get("offset"))
		require.Equal(t, "1", corrected.Query().Get("page_size"))
		require.Equal(t, "Source", corrected.Query().Get("search"))
		require.Equal(t, project.ID, corrected.Query().Get("filter_project"))
		final := tc.HTTP().Get(corrected.String()).Execute()
		require.Equal(t, http.StatusOK, final.Code)
		require.Contains(t, final.Body.String(), "Source A survivor")
	}

}

func TestAllProjectsDeleteTaskReturnList(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	project := createProject(t, h, "Delete destination")
	for _, returnList := range []string{"/tasks?project_id=__all_projects__&search=Review&filter_project=" + project.ID, "https://example.com/tasks?project_id=__all_projects__", "//example.com/tasks?project_id=__all_projects__", "/automations?project_id=__all_projects__", "/tasks?project_id=default"} {
		task := models.Task{ProjectID: project.ID, Title: "Review", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, stores.Tasks.Create(t.Context(), &task))
		req := httptest.NewRequest(http.MethodDelete, "/tasks/"+task.ID+"?project_id=__all_projects__&redirect=list&return_list="+url.QueryEscape(returnList), nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		expected := "/tasks?project_id=__all_projects__"
		if strings.HasPrefix(returnList, expected+"&") {
			expected = returnList
		}
		require.Equal(t, expected, rec.Header().Get("HX-Redirect"))
	}
}

func TestAllProjectsDefaultDestinationIgnoresSavedSelection(t *testing.T) {
	tc := NewStoreTestContext(t)
	other := tc.CreateProject().WithName("Saved other project").Build()
	require.NoError(t, tc.handler.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, other.ID))
	form := tc.HTMX().Get("/tasks/new?project_id=__all_projects__&destination_project=default").Execute()
	require.Equal(t, http.StatusOK, form.Code)
	require.Contains(t, form.Body.String(), "destination_project=default")
	require.NotContains(t, form.Body.String(), "destination_project="+other.ID)
	created := tc.HTMX().Post("/tasks?project_id=__all_projects__&destination_project=default&from=new&thread=1").WithForm(url.Values{"title": {"Default destination task"}, "message": {"Save in Default"}, "category": {"backlog"}, "priority": {"2"}}).Execute()
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	task, err := tc.stores.Tasks.GetByID(t.Context(), created.Header().Get("X-Created-Task-ID"))
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, "default", task.ProjectID)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(tc.stores.Automations, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(tc.stores.Automations, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(tc.stores.Automations), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, nil)
	draft := tc.HTMX().Get("/automations/new?project_id=__all_projects__&destination_project=default").Execute()
	require.Equal(t, http.StatusOK, draft.Code, draft.Body.String())
	require.Contains(t, draft.Body.String(), "destination_project=default")
	require.Contains(t, draft.Body.String(), `name="project_id" value="default"`)
	seed, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
	require.NoError(t, err)
	seed.Name = "Default automation"
	seed.Nodes = []models.AutomationDraftNode{{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review changes", "category": "backlog", "priority": 2}}}
	saved, err := compiler.Save(t.Context(), service.AutomationSaveRequest{ProjectID: "default", Source: "manual", CreatedVia: "web", Candidate: seed})
	require.NoError(t, err)
	live := tc.HTMX().Get("/automations/" + saved.Definition.Automation.ID + "?project_id=__all_projects__").Execute()
	require.Equal(t, http.StatusOK, live.Code, live.Body.String())
	require.Contains(t, live.Body.String(), `data-project-id="default"`)
	selected, err := tc.handler.settingsRepo.Get(t.Context(), uiPreferenceSelectedProjectIDKey)
	require.NoError(t, err)
	require.Equal(t, other.ID, selected)
}

func TestAllProjectsChatDoesNotRequireProjectChooser(t *testing.T) {
	_, app, _ := setupTestHandler(t)
	for _, fragment := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/chat?project_id="+models.AllProjectsID, nil)
		if fragment {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), `id="all-projects-chat"`)
		require.NotContains(t, rec.Body.String(), `id="all-projects-choose"`)
		require.Empty(t, rec.Header().Get("Location"))
	}
}

func TestAllProjectsDragMovePreservesOwnerAndValidatesSchedule(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	project := createProject(t, h, "Move owner")
	for _, owner := range []string{"default", project.ID} {
		task := models.Task{ProjectID: owner, Title: "Move task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
		require.NoError(t, stores.Tasks.Create(t.Context(), &task))
		move := func(category string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodPatch, "/tasks/"+task.ID+"/category?project_id="+models.AllProjectsID, strings.NewReader("category="+category))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			return rec
		}
		require.Equal(t, http.StatusBadRequest, move("scheduled").Code)
		unchanged, err := h.taskSvc.GetByID(t.Context(), task.ID)
		require.NoError(t, err)
		require.Equal(t, models.CategoryBacklog, unchanged.Category)
		for _, category := range []models.TaskCategory{models.CategoryCompleted, models.CategoryBacklog} {
			rec := move(string(category))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			updated, err := h.taskSvc.GetByID(t.Context(), task.ID)
			require.NoError(t, err)
			require.Equal(t, owner, updated.ProjectID)
			require.Equal(t, category, updated.Category)
		}
	}
}
