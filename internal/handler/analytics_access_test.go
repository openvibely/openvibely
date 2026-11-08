package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAnalyticsHTTPProjectAndAccountBoundaries(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	project := createProject(t, h, "Analytics scope")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "analytics-user", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "viewer"))
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	calls := 0
	paths := []string{"/analytics", "/api/analytics/usage", "/api/analytics/skills", "/api/analytics/dashboard", "/api/analytics/success-failure-rates", "/api/analytics/avg-execution-time-by-task", "/api/analytics/avg-execution-time-by-agent", "/api/analytics/execution-trends-by-hour", "/api/analytics/task-run-activity", "/api/analytics/agent-usage-by-project", "/api/analytics/most-frequent-tasks", "/api/analytics/failed-task-patterns"}
	for _, path := range paths {
		e.GET(path, func(c echo.Context) error {
			calls++
			require.NotEmpty(t, c.QueryParam("project_id"), "never permit an unscoped aggregate query")
			return c.NoContent(204)
		})
	}
	request := func(method, path string, p models.Principal) int {
		req := httptest.NewRequest(method, path, nil).WithContext(models.WithPrincipal(ctx, p))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, path := range paths {
		require.Equal(t, 204, request("GET", path+"?projection=local&project_id="+project.ID, user.Principal), path)
		before := calls
		require.Equal(t, 404, request("GET", path+"?projection=local&project_id=missing", user.Principal), path)
		require.Equal(t, before, calls)
	}
	owner := models.Principal{UserID: models.BootstrapUserID}
	require.Equal(t, 204, request("GET", "/api/analytics/dashboard", owner), "auth-off owner selects a project")
	// Actual provider handlers must not be reached, even with no provider configured.
	e.GET("/api/analytics/usage/resets", h.GetUsageResetCredits)
	e.POST("/api/analytics/usage/resets/consume", h.ConsumeUsageReset)
	for _, path := range []string{"/api/analytics/usage?projection=accounts", "/api/analytics/usage?projection=account_limits", "/api/analytics/usage?projection=unknown", "/api/analytics/usage?"} {
		require.Equal(t, 404, request("GET", path+"&project_id="+project.ID, user.Principal))
	}
	require.Equal(t, 404, request("GET", "/api/analytics/usage/resets?account_key=other", user.Principal))
	require.Equal(t, 404, request("POST", "/api/analytics/usage/resets/consume", user.Principal))
	// Test the allow path without consuming a real provider credit.
	e.POST("/api/analytics/usage/resets/consume", func(c echo.Context) error { return c.NoContent(204) })
	require.Equal(t, 204, request("POST", "/api/analytics/usage/resets/consume", owner))
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	stale := user.Principal
	stale.Role = "admin"
	for _, projection := range []string{"accounts", "account_limits", ""} {
		require.Equal(t, 204, request("GET", "/api/analytics/usage?projection="+projection+"&project_id="+project.ID, stale))
	}
	require.Equal(t, 204, request("POST", "/api/analytics/usage/resets/consume", stale))
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	require.Equal(t, 404, request("POST", "/api/analytics/usage/resets/consume", stale), "stale role cannot consume credits")
	e.GET("/analytics", h.Analytics)
	for _, example := range []struct {
		principal models.Principal
		allowed   string
	}{{stale, "false"}, {owner, "true"}} {
		req := httptest.NewRequest("GET", "/analytics?project_id="+project.ID, nil).WithContext(models.WithPrincipal(ctx, example.principal))
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code)
		require.Contains(t, rec.Body.String(), `data-account-access="`+example.allowed+`"`)
	}
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	for _, path := range paths {
		require.Equal(t, 404, request("GET", path+"?projection=local&project_id="+project.ID, user.Principal))
	}
	require.Equal(t, 404, request("POST", "/api/analytics/usage/resets/consume", models.Principal{}))
}
