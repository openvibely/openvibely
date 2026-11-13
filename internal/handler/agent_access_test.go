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

func TestSharedDefinitionHTTPRequiresCurrentOrgAdmin(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "agent-manager", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Agent scope")
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, "maintainer"))
	routes := []struct{ method, path string }{
		{"DELETE", "/channels/bulk"},
		{"POST", "/channels/x/authorized-users"}, {"DELETE", "/channels/x/authorized-users/:id"},
		{"POST", "/channels/telegram"}, {"POST", "/channels/telegram/test"}, {"POST", "/channels/telegram/remove"}, {"POST", "/channels/telegram/send-responses"},
		{"POST", "/channels/github/configure"}, {"GET", "/channels/github/connect"}, {"POST", "/channels/github/disconnect"}, {"POST", "/channels/github/remove"},
		{"POST", "/channels/slack/configure"}, {"GET", "/channels/slack/connect"}, {"POST", "/channels/slack/disconnect"}, {"POST", "/channels/slack/remove"}, {"POST", "/channels/slack/test"},
		{"POST", "/channels/discord/configure"}, {"POST", "/channels/discord/remove"}, {"POST", "/channels/discord/test"},
		{"POST", "/channels/x/configure"}, {"POST", "/channels/x/remove"}, {"POST", "/channels/x/test"},
		{"POST", "/channels/email/configure"}, {"POST", "/channels/email/remove"}, {"POST", "/channels/email/test"},
		{"GET", "/agents"}, {"POST", "/agents"}, {"POST", "/agents/generate"},
		{"GET", "/agents/id/json"}, {"PUT", "/agents/id"}, {"DELETE", "/agents/id"},
		{"DELETE", "/agents/bulk"}, {"PUT", "/agents/id/lifecycle-hooks"},
		{"POST", "/agents/id/skills"}, {"POST", "/agents/plugins/install"}, {"POST", "/agents/plugins/uninstall"},
		{"GET", "/models"}, {"POST", "/models"}, {"PUT", "/models/id"}, {"POST", "/models/id"},
		{"GET", "/models/id/edit-details"}, {"POST", "/models/id/set-default"}, {"DELETE", "/models/bulk"}, {"DELETE", "/models/id"},
		{"POST", "/models/oauth-connections/id/rename"}, {"POST", "/models/oauth-connections/id/models"}, {"POST", "/models/oauth-connections/id/delete"},
		{"POST", "/models/oauth/setup"}, {"DELETE", "/models/oauth/setup"}, {"GET", "/models/oauth/setup/status"},
		{"GET", "/models/id/oauth/initiate"}, {"POST", "/models/id/oauth/disconnect"}, {"POST", "/models/oauth/manual-complete"},
		{"GET", "/models/id/oauth/status"}, {"GET", "/models/openai-compatible/available"}, {"GET", "/models/ollama/available"}, {"GET", "/models/ollama/context"},
		{"GET", "/skills"}, {"POST", "/skills"}, {"POST", "/skills/import"},
		{"GET", "/skills/item/details"}, {"PUT", "/skills/item"},
		{"POST", "/skills/item/enabled"}, {"POST", "/skills/item/always_use"},
		{"DELETE", "/skills/bulk"}, {"DELETE", "/skills/item"}, {"POST", "/skills/dependency-preview"},
		{"GET", "/personality"}, {"POST", "/personality/save"}, {"POST", "/personality/custom"},
		{"GET", "/personality/custom/key"}, {"PUT", "/personality/custom/key"},
		{"DELETE", "/personality/custom/bulk"}, {"DELETE", "/personality/custom/key"},
	}
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	calls := 0
	for _, route := range routes {
		e.Add(route.method, route.path, func(c echo.Context) error { calls++; return c.NoContent(204) })
	}
	request := func(method, path string, p models.Principal) int {
		req := httptest.NewRequest(method, path, nil).WithContext(ctx)
		if p.UserID != "" {
			req = req.WithContext(models.WithPrincipal(ctx, p))
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	principal := user.Principal
	principal.Role = "owner"
	for _, route := range routes {
		want := 404
		if route.method == "GET" && (route.path == "/models" || route.path == "/agents" || route.path == "/skills" || route.path == "/personality") {
			want = 403
		}
		require.Equal(t, want, request(route.method, route.path, principal), route.path)
	}
	require.Zero(t, calls, "project maintainer cannot read or change shared definitions")
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	for _, route := range routes {
		require.Equal(t, 204, request(route.method, route.path, principal))
	}
	require.Equal(t, len(routes), calls)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	for _, route := range routes {
		want := 404
		if route.method == "GET" && (route.path == "/models" || route.path == "/agents" || route.path == "/skills" || route.path == "/personality") {
			want = 403
		}
		require.Equal(t, want, request(route.method, route.path, principal), route.path)
	}
	require.Equal(t, len(routes), calls)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", true))
	for _, route := range routes {
		require.Equal(t, 404, request(route.method, route.path, principal), "removed admin: %s", route.path)
	}
	require.Equal(t, len(routes), calls)
	require.Equal(t, 404, request("POST", "/agents", models.Principal{}))
	h.authMode = auth.AuthModeDisabled
	for _, route := range routes {
		require.Equal(t, 204, request(route.method, route.path, models.Principal{}))
	}
	// A real mutation handler must be unreachable after demotion, even when its
	// dependencies are missing. This includes plugin filesystem operations.
	e.POST("/agents/plugins/install", h.InstallPlugin)
	require.Equal(t, 404, request("POST", "/agents/plugins/install", principal))
}

func TestSharedDefinitionHTTPBlocksRealMutations(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "skill-reader", "long-password-value", "")
	require.NoError(t, err)
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	e.POST("/models", h.CreateModel)
	e.POST("/models/oauth/setup", h.BeginModelOAuthSetup)
	e.POST("/models/oauth/manual-complete", h.OAuthManualComplete)
	e.POST("/channels/telegram", h.handleTelegramSave)
	e.POST("/channels/github/configure", h.handleGitHubConfigure)
	e.POST("/channels/email/configure", h.handleEmailConfigure)
	e.POST("/skills", h.CreateSkill)
	e.POST("/skills/import", h.ImportSkillPackage)
	e.POST("/skills/dependency-preview", h.PreviewSkillDependencies)
	e.POST("/personality/custom", h.CreateCustomPersonality)
	e.POST("/personality/save", h.handlePersonalitySave)
	for _, path := range []string{"/channels/telegram", "/channels/github/configure", "/channels/email/configure", "/models", "/models/oauth/setup", "/models/oauth/manual-complete", "/skills", "/skills/import", "/skills/dependency-preview", "/personality/custom", "/personality/save"} {
		req := httptest.NewRequest("POST", path, nil).WithContext(models.WithPrincipal(ctx, user.Principal))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 404, rec.Code, path)
	}
}
