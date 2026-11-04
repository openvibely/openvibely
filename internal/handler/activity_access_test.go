package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestActivityHTTPPermissionsAndRevocation(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := createProject(t, h, "Activity permissions")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "activity-user", "long-password-value", "")
	require.NoError(t, err)
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	routes := []struct {
		method, path string
		minimum      int
	}{
		{"GET", "/upcoming", 1}, {"GET", "/history", 1}, {"POST", "/upcoming/summary", 2}, {"POST", "/history/summary", 2},
		{"GET", "/insights", 1}, {"GET", "/insights/by-type", 1}, {"GET", "/insights/reports", 1},
		{"POST", "/insights/analyze", 2}, {"POST", "/insights/extract-knowledge", 2}, {"POST", "/insights/health-check", 2}, {"POST", "/history/grade-ideas", 2},
		{"PATCH", "/insights/:id/status", 2}, {"DELETE", "/insights/:id", 2}, {"DELETE", "/insights/knowledge/:id", 2},
	}
	calls := 0
	for _, route := range routes {
		e.Add(route.method, route.path, func(c echo.Context) error {
			calls++
			require.Equal(t, project.ID, c.QueryParam("project_id"))
			return c.NoContent(204)
		})
	}
	request := func(method, path string, p models.Principal) int {
		req := httptest.NewRequest(method, path, nil).WithContext(models.WithPrincipal(ctx, p))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, role := range []string{"", "viewer", "editor", "maintainer", "owner"} {
		principal := user.Principal
		if role == "owner" {
			principal = models.Principal{UserID: models.BootstrapUserID}
		} else {
			require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, role))
		}
		for _, route := range routes {
			before := calls
			want := 404
			if authz.Rank(role) >= route.minimum {
				want = 204
			}
			require.Equal(t, want, request(route.method, route.path+"?project_id="+project.ID, principal), role+" "+route.path)
			if want == 404 {
				require.Equal(t, before, calls, "denial must happen before model work")
			}
		}
	}
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	require.Equal(t, 404, request("GET", "/history?project_id="+project.ID, user.Principal))
	// Real summary handlers must be unreachable after revocation, even with no service configured.
	h.upcomingSvc = nil
	e.POST("/upcoming/summary", h.GeneratePulseSummary)
	e.POST("/history/summary", h.GenerateReflectionSummary)
	for _, path := range []string{"/upcoming/summary", "/history/summary"} {
		require.Equal(t, 404, request("POST", path+"?project_id="+project.ID, user.Principal))
		require.Equal(t, 400, request("POST", path, models.Principal{UserID: models.BootstrapUserID}))
	}
	// Auth-off middleware supplies the default owner identity.
	require.Equal(t, 204, request("GET", "/upcoming?project_id="+project.ID, models.Principal{UserID: models.BootstrapUserID}))
}
