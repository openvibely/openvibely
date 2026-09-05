package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestBreadcrumbSelectorItemURLPreservesOnlySupportedContext(t *testing.T) {
	taskURL := breadcrumbSelectorItemURL("tasks", "task/id", "project id", "changes", "")
	require.Equal(t, "/tasks/task%2Fid?project_id=project+id&tab=changes", taskURL)
	require.NotContains(t, breadcrumbSelectorItemURL("tasks", "task", "project", "../../admin", ""), "tab=")
	require.Equal(t, "/automations/automation/builder?project_id=project", breadcrumbSelectorItemURL("automations", "automation", "project", "", "edit"))
	require.Equal(t, "/automations/automation?project_id=project", breadcrumbSelectorItemURL("automations", "automation", "project", "", strings.Repeat("x", 20)))
}
