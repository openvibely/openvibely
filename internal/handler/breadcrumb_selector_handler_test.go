package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestBreadcrumbSelectorTaskResultsAreProjectScopedAndPreserveAllowlistedTab(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	owning := createProject(t, h, "Selector owning")
	foreign := createProject(t, h, "Selector foreign")
	current := createTask(t, h, owning.ID, "Current selector task")
	other := createTask(t, h, owning.ID, "Other selector task")
	secret := createTask(t, h, foreign.ID, "Foreign selector secret")

	req := httptest.NewRequest(http.MethodGet, "/breadcrumb-selectors/tasks?project_id="+owning.ID+"&current_id="+current.ID+"&search=selector&tab=changes", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, current.Title)
	require.Contains(t, body, other.Title)
	require.Contains(t, body, "/tasks/"+other.ID+"?project_id="+owning.ID+"&amp;tab=changes")
	require.NotContains(t, body, secret.Title)
	require.NotContains(t, body, secret.ID)

	req = httptest.NewRequest(http.MethodGet, "/breadcrumb-selectors/tasks?project_id=missing&current_id="+secret.ID+"&search=selector", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), secret.Title)
}

func TestBreadcrumbSelectorScheduleOriginShowsOnlyScheduleCalendarTasks(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Schedule selector project")
	scheduledCategory := createTask(t, h, project.ID, "Scheduled category selector task", func(task *models.Task) {
		task.Category = models.CategoryScheduled
	})
	activeWithSchedule := createTask(t, h, project.ID, "Active scheduled selector task")
	createSchedule(t, h, activeWithSchedule.ID, time.Now().UTC().Add(time.Hour))
	ordinary := createTask(t, h, project.ID, "Ordinary selector task")
	foreignProject := createProject(t, h, "Foreign schedule selector project")
	foreignScheduled := createTask(t, h, foreignProject.ID, "Foreign scheduled selector secret", func(task *models.Task) {
		task.Category = models.CategoryScheduled
	})

	req := httptest.NewRequest(http.MethodGet, "/breadcrumb-selectors/tasks?project_id="+project.ID+"&current_id="+scheduledCategory.ID+"&search=selector&tab=schedules&from=schedule", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	require.Contains(t, body, scheduledCategory.Title)
	require.Contains(t, body, activeWithSchedule.Title)
	require.NotContains(t, body, ordinary.Title)
	require.NotContains(t, body, foreignScheduled.Title)
	require.Contains(t, body, "/tasks/"+activeWithSchedule.ID+"?from=schedule&amp;project_id="+project.ID+"&amp;tab=schedules")
}

func TestBreadcrumbSelectorAutomationResultsAreProjectScopedBoundedAndPreserveViews(t *testing.T) {
	tc := NewTestContext(t)
	owning := tc.CreateProject().WithName("Automation selector owning").Build()
	foreign := tc.CreateProject().WithName("Automation selector foreign").Build()
	repo := repository.NewAutomationRepo(tc.db)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(repo), nil)

	insertPublished := func(projectID, id, name string) {
		t.Helper()
		versionID := id + "-version"
		_, err := tc.db.Exec(`INSERT INTO automations (id, project_id, stable_key, name, lifecycle_state)
			VALUES (?, ?, ?, ?, 'active')`, id, projectID, id, name)
		require.NoError(t, err)
		_, err = tc.db.Exec(`INSERT INTO automation_versions (id, project_id, automation_id, version, state, source, adapter_key)
			VALUES (?, ?, ?, 1, 'published', 'manual', 'custom')`, versionID, projectID, id)
		require.NoError(t, err)
		_, err = tc.db.Exec(`UPDATE automations SET published_version_id = ? WHERE id = ?`, versionID, id)
		require.NoError(t, err)
	}

	for i := 0; i < 24; i++ {
		insertPublished(owning.ID, fmt.Sprintf("automation-%02d", i), fmt.Sprintf("Selector Automation %02d", i))
	}
	insertPublished(foreign.ID, "foreign-automation", "Selector Automation foreign secret")

	for _, test := range []struct {
		name         string
		view         string
		wantURL      string
		forbiddenURL string
	}{
		{name: "live", view: "live", wantURL: "/automations/automation-01?project_id=" + owning.ID, forbiddenURL: "/builder"},
		{name: "edit", view: "edit", wantURL: "/automations/automation-01/builder?project_id=" + owning.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/breadcrumb-selectors/automations?project_id="+owning.ID+"&current_id=automation-23&search=selector&view="+test.view, nil)
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			body := rec.Body.String()
			require.Equal(t, 20, strings.Count(body, `data-breadcrumb-selector-option`), "Automation results must stay bounded")
			require.Contains(t, body, `aria-selected="true"`)
			require.Contains(t, body, test.wantURL)
			if test.forbiddenURL != "" {
				require.NotContains(t, body, test.forbiddenURL)
			}
			require.NotContains(t, body, "Selector Automation foreign secret")
			require.NotContains(t, body, "foreign-automation")
			require.Contains(t, body, "More matches are available")
		})
	}
}

func TestBreadcrumbSelectorItemURLPreservesOnlySupportedContext(t *testing.T) {
	taskURL := breadcrumbSelectorItemURL("tasks", "task/id", "project id", "changes", "", "")
	require.Equal(t, "/tasks/task%2Fid?project_id=project+id&tab=changes", taskURL)
	require.NotContains(t, breadcrumbSelectorItemURL("tasks", "task", "project", "../../admin", "", ""), "tab=")
	require.Equal(t, "/tasks/task?from=schedule&project_id=project&tab=schedules", breadcrumbSelectorItemURL("tasks", "task", "project", "schedules", "", "schedule"))
	require.NotContains(t, breadcrumbSelectorItemURL("tasks", "task", "project", "", "", "other"), "from=")
	require.Equal(t, "/automations/automation/builder?project_id=project", breadcrumbSelectorItemURL("automations", "automation", "project", "", "edit", ""))
	require.Equal(t, "/automations/automation?project_id=project", breadcrumbSelectorItemURL("automations", "automation", "project", "", strings.Repeat("x", 20), ""))
}
