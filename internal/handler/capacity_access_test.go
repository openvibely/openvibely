package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestCapacityHTTPAccessAndRevocation(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "capacity-user", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Capacity")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	calls := 0
	next := func(c echo.Context) error { calls++; return c.NoContent(204) }
	e.POST("/workers/projects/:projectId/limit", next)
	e.GET("/api/capacity/projects/:projectId", next)
	e.POST("/workers", next)
	e.POST("/settings/worktree", next)
	e.GET("/api/capacity/global", next)
	e.GET("/api/capacity/models/:modelId", next)
	request := func(method, path string, p models.Principal) int {
		req := httptest.NewRequest(method, path, nil).WithContext(models.WithPrincipal(ctx, p))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	principal := user.Principal
	principal.Role = "owner"
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, role))
		require.Equal(t, 204, request("GET", "/api/capacity/projects/"+project.ID, principal))
		want := 403
		if role == "maintainer" {
			want = 204
		}
		require.Equal(t, want, request("POST", "/workers/projects/"+project.ID+"/limit", principal))
		require.Equal(t, 404, request("POST", "/workers", principal))
		require.Equal(t, 404, request("POST", "/settings/worktree", principal))
		require.Equal(t, 404, request("GET", "/api/capacity/global", principal))
		require.Equal(t, 404, request("GET", "/api/capacity/models/model", principal))
	}
	before := calls
	require.Equal(t, 404, request("POST", "/workers/projects/foreign/limit", principal))
	require.Equal(t, before, calls)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	require.Equal(t, 404, request("GET", "/api/capacity/projects/"+project.ID, principal))
	e.POST("/workers/projects/:projectId/limit", h.UpdateProjectWorkerLimit)
	require.Equal(t, 404, request("POST", "/workers/projects/"+project.ID+"/limit", principal), "denied before form parsing or mutation")
	h.authMode = auth.AuthModeDisabled
	require.Equal(t, 204, request("GET", "/api/capacity/global", models.Principal{UserID: models.BootstrapUserID}))
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	require.Equal(t, 204, request("GET", "/api/capacity/global", principal))
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	require.Equal(t, 404, request("GET", "/api/capacity/global", principal))
}
