package handler

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProjectSharingControlsAndOwnership(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	project := createProject(t, h, "Sharing")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "sharing-user", "long-password-value", "")
	require.NoError(t, err)
	e := echo.New()
	e.GET("/projects/:id/sharing", h.ProjectSharing)
	e.POST("/projects/:id/sharing", h.UpdateProjectSharing)
	path := "/projects/" + project.ID + "/sharing"
	owner := models.Principal{UserID: models.BootstrapUserID}
	request := func(method string, p models.Principal, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(values.Encode())).WithContext(models.WithPrincipal(ctx, p))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 404, request("GET", user.Principal, nil).Code)
	rec := request("GET", owner, nil)
	require.Equal(t, 200, rec.Code)
	require.Contains(t, rec.Body.String(), "Transfer ownership")
	require.Contains(t, rec.Body.String(), "sandboxing")
	require.NotContains(t, rec.Body.String(), `value="private"`)
	rec = request("POST", owner, url.Values{"action": {"policy"}, "org_role": {"viewer"}, "visibility": {"private"}})
	require.Equal(t, 200, rec.Code)
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "org", policy.Visibility)
	require.Equal(t, "viewer", policy.OrgRole)
	rec = request("POST", owner, url.Values{"action": {"member"}, "user_id": {user.UserID}, "role": {"maintainer"}})
	require.Equal(t, 200, rec.Code)
	rec = request("GET", user.Principal, nil)
	require.Equal(t, 200, rec.Code)
	require.NotContains(t, rec.Body.String(), "Transfer ownership")
	rec = request("POST", user.Principal, url.Values{"action": {"transfer"}, "user_id": {user.UserID}})
	require.Contains(t, rec.Body.String(), "This change is not allowed")
	policy, err = tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, policy.OwnerUserID)
	rec = request("POST", owner, url.Values{"action": {"transfer"}, "user_id": {user.UserID}})
	require.Equal(t, 200, rec.Code)
	policy, err = tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, user.UserID, policy.OwnerUserID)
	events, err := tc.stores.ProjectAccess.Audit(ctx, project.ID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 3)
	// Origin checks happen before any mutation.
	req := httptest.NewRequest("POST", path, strings.NewReader("action=policy&org_role=editor")).WithContext(models.WithPrincipal(ctx, owner))
	req.Header.Set("Origin", "https://foreign.example")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	denied := httptest.NewRecorder()
	e.ServeHTTP(denied, req)
	require.Equal(t, 403, denied.Code)
	policy, err = tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "viewer", policy.OrgRole)
}

func TestProjectSharingRoleMatrixRemovalAndAudit(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	project := createProject(t, h, "Sharing matrix")
	newUser := func(name string) models.Principal {
		token, err := org.Invite(ctx, models.BootstrapUserID, "member")
		require.NoError(t, err)
		user, err := org.Accept(ctx, token, name, "long-password-value", "")
		require.NoError(t, err)
		return user.Principal
	}
	actor, target := newUser("matrix-actor"), newUser("matrix-target")
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	e.GET("/projects/:id/sharing", h.ProjectSharing)
	e.POST("/projects/:id/sharing", h.UpdateProjectSharing)
	path := "/projects/" + project.ID + "/sharing"
	request := func(method string, p models.Principal, values url.Values) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(values.Encode())).WithContext(models.WithPrincipal(ctx, p))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	grant := url.Values{"action": {"member"}, "user_id": {target.UserID}, "role": {"editor"}}
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, actor.UserID, role))
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, target.UserID, ""))
		page := request("GET", actor, nil)
		rec := request("POST", actor, grant)
		targetRole, err := tc.stores.ProjectAccess.Role(ctx, target.UserID, project.ID)
		require.NoError(t, err)
		if role == "maintainer" {
			require.Equal(t, 200, page.Code)
			require.Contains(t, page.Body.String(), "project.member", "maintainers see the audit trail")
			require.NotContains(t, page.Body.String(), "Transfer ownership")
			require.Equal(t, 200, rec.Code)
			require.Equal(t, "editor", targetRole)
		} else {
			require.Equal(t, 404, page.Code, role)
			require.Equal(t, 404, rec.Code, role)
			// Org-wide editor access still applies; no individual grant was saved.
			members, err := tc.stores.ProjectAccess.Members(ctx, project.ID)
			require.NoError(t, err)
			for _, member := range members {
				require.NotEqual(t, target.UserID, member.UserID, role)
			}
		}
	}
	// Removing a grant drops back to organization access, never below it.
	rec := request("POST", actor, url.Values{"action": {"member"}, "user_id": {target.UserID}, "role": {""}})
	require.Equal(t, 200, rec.Code)
	targetRole, err := tc.stores.ProjectAccess.Role(ctx, target.UserID, project.ID)
	require.NoError(t, err)
	require.Equal(t, "editor", targetRole)
	// A stale form submitted after the actor's own downgrade is refused.
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, actor.UserID, ""))
	rec = request("POST", actor, url.Values{"action": {"member"}, "user_id": {target.UserID}, "role": {"maintainer"}})
	require.Equal(t, 404, rec.Code)
	targetRole, err = tc.stores.ProjectAccess.Role(ctx, target.UserID, project.ID)
	require.NoError(t, err)
	require.Equal(t, "editor", targetRole)
}
