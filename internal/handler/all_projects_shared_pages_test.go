package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsSharedSidebarPages(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.insightsSvc = service.NewInsightsService(stores.Insights, stores.Tasks, stores.Projects, stores.LLMConfigs, stores.Executions)
	second := createProject(t, h, "Second native workspace")
	for _, project := range []string{"default", second.ID} {
		task := &models.Task{ProjectID: project, Title: project + " shared pending", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
		require.NoError(t, stores.Tasks.Create(t.Context(), task))
		next := time.Now().UTC().Add(time.Hour)
		schedule := &models.Schedule{TaskID: task.ID, RunAt: next, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, NextRun: &next}
		require.NoError(t, stores.Schedules.Create(t.Context(), schedule))
		require.NoError(t, stores.Alerts.Create(t.Context(), &models.Alert{ProjectID: project, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: project + " shared alert", Scope: models.AlertScopeProject}))
	}
	for _, tc := range []struct{ path, marker, content string }{
		{"/upcoming", `id="upcoming-container"`, "shared pending"},
		{"/history", `id="history-container"`, ""},
		{"/schedule", `id="schedule-content"`, "shared pending"},
		{"/alerts", `id="alerts-container"`, "shared alert"},
		{"/insights", `id="health-check-section"`, ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path+"?project_id="+models.AllProjectsID, nil))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			body := rec.Body.String()
			require.Contains(t, body, tc.marker)
			require.NotContains(t, body, `id="all-projects-choose"`)
			if tc.content != "" {
				require.Contains(t, body, "default "+tc.content)
				require.Contains(t, body, second.ID+" "+tc.content)
			}
		})
	}
	all, err := stores.Upcoming.GetTaskSummary(t.Context(), models.AllProjectsID, time.Now())
	require.NoError(t, err)
	scoped, err := stores.Upcoming.GetTaskSummary(t.Context(), second.ID, time.Now())
	require.NoError(t, err)
	require.NotEqual(t, all, scoped)
	count, err := stores.Alerts.CountUnread(t.Context(), models.AllProjectsID)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, stores.Alerts.MarkAllRead(t.Context(), models.AllProjectsID))
	count, err = stores.Alerts.CountUnread(t.Context(), second.ID)
	require.NoError(t, err)
	require.Zero(t, count)
}
