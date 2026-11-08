package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestAlertHTTPPermissionsAndRevocation(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := createProject(t, h, "Alert permissions")
	foreign := createProject(t, h, "Foreign alerts")
	alert := models.Alert{ProjectID: project.ID, Title: "Keep", Type: "custom", Severity: "info"}
	other := models.Alert{ProjectID: foreign.ID, Title: "Other", Type: "custom", Severity: "info"}
	require.NoError(t, h.alertSvc.Create(ctx, &alert))
	require.NoError(t, h.alertSvc.Create(ctx, &other))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "alert-user", "long-password-value", "")
	require.NoError(t, err)
	routes := []struct{ method, path string }{
		{"GET", "/alerts"}, {"GET", "/alerts/:id/details"}, {"GET", "/alerts/unread-count"}, {"GET", "/api/alerts/pending-count"},
		{"POST", "/alerts/:id/read"}, {"POST", "/alerts/:id/approve"}, {"POST", "/alerts/:id/reject"}, {"POST", "/alerts/:id/dismiss"},
		{"POST", "/alerts/read-all"}, {"POST", "/alerts/read-bulk"}, {"DELETE", "/alerts/bulk"}, {"DELETE", "/alerts/:id"}, {"DELETE", "/alerts"},
	}
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	for _, route := range routes {
		e.Add(route.method, route.path, func(c echo.Context) error { return c.NoContent(204) })
	}
	request := func(method, path string, p models.Principal, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(models.WithPrincipal(ctx, p))
		req.Header.Set("Content-Type", "application/json")
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
			minimum := 2
			if route.method == "GET" {
				minimum = 1
			}
			want := 404
			if role != "" {
				want = 403
			}
			if authz.Rank(role) >= minimum {
				want = 204
			}
			path := strings.ReplaceAll(route.path, ":id", alert.ID) + "?project_id=" + project.ID
			require.Equal(t, want, request(route.method, path, principal, ""), role+" "+route.path)
		}
	}
	owner := models.Principal{UserID: models.BootstrapUserID}
	require.Equal(t, 404, request("GET", "/alerts/"+other.ID+"/details?project_id="+project.ID, owner, ""))
	require.Equal(t, 400, request("GET", "/api/alerts/pending-count", owner, ""))
	// Exercise real deletion handlers and check that denied mutations leave both rows.
	e.DELETE("/alerts/:id", h.DeleteAlert)
	e.DELETE("/alerts/bulk", h.DeleteAlertsBulk)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "viewer"))
	require.Equal(t, 403, request("DELETE", "/alerts/"+alert.ID+"?project_id="+project.ID, user.Principal, ""))
	require.Equal(t, 403, request("DELETE", "/alerts/bulk?project_id="+project.ID, user.Principal, `{"ids":["`+alert.ID+`"]}`))
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "editor"))
	require.Equal(t, http.StatusBadRequest, request("DELETE", "/alerts/bulk?project_id="+project.ID, user.Principal, `{"ids":["`+alert.ID+`","`+other.ID+`"]}`))
	retained, err := h.alertSvc.GetByID(ctx, project.ID, alert.ID)
	require.NoError(t, err)
	require.NotNil(t, retained)
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, ""))
	require.Equal(t, 404, request("GET", "/alerts?project_id="+project.ID, user.Principal, ""))
	// Auth-off middleware supplies this same default owner principal.
	require.Equal(t, 204, request("GET", "/alerts?project_id="+project.ID, owner, ""))
}
