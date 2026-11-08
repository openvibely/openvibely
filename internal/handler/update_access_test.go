package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestSystemUpdateMutationsRequireCurrentOrgAdmin(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "update-user", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Update scope")
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "maintainer"))
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	calls := 0
	for _, path := range []string{"/api/system/update/apply", "/api/system/update/cancel"} {
		e.POST(path, func(c echo.Context) error { calls++; return c.NoContent(http.StatusAccepted) })
	}
	e.GET("/api/system/update", h.SystemUpdate)
	request := func(method, path string, p models.Principal) int {
		req := httptest.NewRequest(method, path, nil)
		if p.UserID != "" {
			req = req.WithContext(models.WithPrincipal(ctx, p))
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	header := func(p models.Principal) string {
		req := httptest.NewRequest(http.MethodGet, "/api/system/update", nil).WithContext(models.WithPrincipal(ctx, p))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		return rec.Header().Get("X-OpenVibely-Can-Manage-Updates")
	}
	principal := user.Principal
	principal.Role = "owner" // Untrusted/stale claims must not grant instance control.
	check := func(p models.Principal, want int) {
		t.Helper()
		for _, path := range []string{"/api/system/update/apply", "/api/system/update/cancel"} {
			require.Equal(t, want, request(http.MethodPost, path, p))
		}
	}
	check(principal, http.StatusNotFound)
	require.Equal(t, "false", header(principal))
	check(models.Principal{}, http.StatusNotFound)
	require.Zero(t, calls)
	require.Equal(t, http.StatusNoContent, request(http.MethodGet, "/api/system/update", principal))
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	check(principal, http.StatusAccepted)
	require.Equal(t, "true", header(principal))
	require.Equal(t, 2, calls)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	check(principal, http.StatusNotFound)
	require.Equal(t, "false", header(principal))
	require.Equal(t, 2, calls, "demotion must stop calls before reaching the coordinator")
	check(models.Principal{UserID: models.BootstrapUserID}, http.StatusAccepted)
	h.authMode = auth.AuthModeDisabled
	check(models.Principal{}, http.StatusAccepted)
}
