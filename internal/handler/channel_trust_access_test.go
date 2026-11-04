package handler

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestChannelTrustRolesAndRevocation(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetTelegramAuthRepo(tc.stores.TelegramAuth)
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.authMode = auth.AuthModeLocal
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "trust-manager", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Trust scope")
	e := echo.New()
	e.Use(h.projectAccessBoundary)
	calls := 0
	for _, base := range []string{"/channels/github/authorized-actors", "/channels/telegram/authorized-users", "/channels/slack/authorized-users", "/channels/discord/authorized-users", "/channels/email/authorized-senders"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			path := base
			if method == "DELETE" {
				path += "/:id"
			}
			e.Add(method, path, func(c echo.Context) error { calls++; return c.NoContent(204) })
		}
	}
	request := func(method, base, projectID string, principal models.Principal) int {
		path := base
		if method == "DELETE" {
			path += "/entry"
		}
		form := url.Values{"project_id": {projectID}}
		req := httptest.NewRequest(method, path+"?"+form.Encode(), strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if principal.UserID != "" {
			req = req.WithContext(models.WithPrincipal(ctx, principal))
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, base := range []string{"/channels/github/authorized-actors", "/channels/telegram/authorized-users", "/channels/slack/authorized-users", "/channels/discord/authorized-users", "/channels/email/authorized-senders"} {
		for _, role := range []string{"viewer", "editor", "maintainer"} {
			require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, role))
			require.Equal(t, 204, request("GET", base, project.ID, user.Principal))
			for _, method := range []string{"POST", "DELETE"} {
				want := 404

				require.Equal(t, want, request(method, base, project.ID, user.Principal))
				require.Equal(t, 404, request(method, base, "missing-project", user.Principal))
			}
		}
	}
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
	require.Equal(t, 204, request("POST", "/channels/slack/authorized-users", project.ID, user.Principal))
	require.Positive(t, calls)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", true))
	before := calls
	require.Equal(t, 404, request("GET", "/channels/slack/authorized-users", project.ID, user.Principal))
	require.Equal(t, 404, request("POST", "/channels/slack/authorized-users", project.ID, user.Principal))
	require.Equal(t, before, calls)
	require.Equal(t, 404, request("POST", "/channels/slack/authorized-users", project.ID, models.Principal{}))
	h.authMode = auth.AuthModeDisabled
	require.Equal(t, 204, request("POST", "/channels/slack/authorized-users", project.ID, models.Principal{}))
}

// Exercise the actual handlers without middleware: a missing principal must not
// reach the repositories' legacy anonymous-write compatibility path.
func TestChannelTrustDirectHandlersRequireIdentity(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetTelegramAuthRepo(tc.stores.TelegramAuth)
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.authMode = auth.AuthModeLocal
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "direct-trust-admin", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Direct trust")
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", true))
	cases := []struct {
		name, field, value   string
		list, create, remove echo.HandlerFunc
	}{
		{"github", "github_login", "reviewer", h.GitHubRuntimeSettingsFragment, h.AddGitHubAuthorizedActor, h.RemoveGitHubAuthorizedActor},
		{"telegram", "user_id_or_username", "12345", h.ListTelegramAuthorizedUsers, h.AddTelegramAuthorizedUser, h.RemoveTelegramAuthorizedUser},
		{"slack", "slack_user_id", "U12345", h.ListSlackAuthorizedUsers, h.AddSlackAuthorizedUser, h.RemoveSlackAuthorizedUser},
		{"discord", "discord_user_id", "12345", h.ListDiscordAuthorizedUsers, h.AddDiscordAuthorizedUser, h.RemoveDiscordAuthorizedUser},
		{"email", "authorized_email_address", "sender@example.com", h.ListEmailAuthorizedSenders, h.AddEmailAuthorizedSender, h.RemoveEmailAuthorizedSender},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h.authMode = auth.AuthModeLocal
			call := func(method string, fn echo.HandlerFunc, principal *models.Principal) error {
				form := url.Values{"project_id": {project.ID}, tt.field: {tt.value}}
				req := httptest.NewRequest(method, "/?"+form.Encode(), strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if principal != nil {
					req = req.WithContext(models.WithPrincipal(ctx, *principal))
				}
				c := echo.New().NewContext(req, httptest.NewRecorder())
				c.SetParamNames("id")
				c.SetParamValues("missing")
				return fn(c)
			}
			for _, principal := range []*models.Principal{nil, {}, &user.Principal} {
				require.ErrorIs(t, call("GET", tt.list, principal), authz.ErrDenied)
				require.ErrorIs(t, call("POST", tt.create, principal), authz.ErrDenied)
				require.ErrorIs(t, call("DELETE", tt.remove, principal), authz.ErrDenied)
			}
			h.authMode = auth.AuthModeDisabled
			require.NoError(t, call("POST", tt.create, nil))
			require.NoError(t, call("GET", tt.list, nil))
			// Explicit revoked identities must not inherit the auth-off owner fallback.
			require.ErrorIs(t, call("POST", tt.create, &user.Principal), authz.ErrDenied)
			h.authMode = auth.AuthModeLocal
		})
	}
}

func TestSlackIdentityBindingHandler(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	h.SetOrganizationService(service.NewOrganizationService(tc.stores.Organizations, "secret"))
	h.authMode = auth.AuthModeLocal
	project := createProject(t, h, "Slack identity")
	owner := models.Principal{UserID: models.BootstrapUserID}
	entry := &models.SlackAuthorizedUser{ProjectID: project.ID, SlackUserID: "U123"}
	require.NoError(t, tc.stores.SlackAuth.Create(models.WithPrincipal(ctx, owner), entry))
	call := func(projectID string, principal *models.Principal) error {
		values := url.Values{"project_id": {projectID}, "team_id": {"T123"}, "application_user_id": {models.BootstrapUserID}}
		req := httptest.NewRequest("POST", "/", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if principal != nil {
			req = req.WithContext(models.WithPrincipal(ctx, *principal))
		}
		c := echo.New().NewContext(req, httptest.NewRecorder())
		c.SetParamNames("id")
		c.SetParamValues(entry.ID)
		return h.BindSlackApplicationUser(c)
	}
	require.Error(t, call(project.ID, nil))
	require.Error(t, call("foreign", &owner))
	_, err := tc.stores.SlackAuth.ResolveApplicationUser(ctx, project.ID, "T123", "U123")
	require.Error(t, err)
	require.NoError(t, call(project.ID, &owner))
	mapped, err := tc.stores.SlackAuth.ResolveApplicationUser(ctx, project.ID, "T123", "U123")
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, mapped)
}

func TestGitHubReviewerIdentityBindingHandler(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	project := createProject(t, h, "GitHub reviewer binding")
	owner := models.Principal{UserID: models.BootstrapUserID}
	actor := &models.GitHubAuthorizedActor{GitHubLogin: "reviewer"}
	require.NoError(t, tc.stores.GitHubAuth.UpsertAuthorizedActor(models.WithPrincipal(ctx, owner), actor))
	call := func(principal *models.Principal, attested, projectID string) (string, error) {
		values := url.Values{"project_id": {projectID}, "host": {"github.com"}, "external_user_id": {"123"}, "application_user_id": {models.BootstrapUserID}, "attested": {attested}}
		req := httptest.NewRequest("POST", "/", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if principal != nil {
			req = req.WithContext(models.WithPrincipal(ctx, *principal))
		}
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(actor.ID)
		err := h.BindGitHubReviewerIdentity(c)
		return rec.Body.String(), err
	}
	_, err := call(nil, "true", project.ID)
	require.Error(t, err)
	_, err = call(&owner, "false", project.ID)
	require.Error(t, err)
	_, err = call(&owner, "true", "foreign")
	require.Error(t, err)
	body, err := call(&owner, "true", project.ID)
	require.NoError(t, err)
	require.Contains(t, body, "Attested: github.com user 123")
	mapped, err := tc.stores.GitHubAuth.ResolveReviewerIdentity(ctx, project.ID, "github.com", 123, "reviewer")
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, mapped)
	token, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	admin, err := org.Accept(ctx, token, "revoked-reviewer-admin", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, admin.UserID, "member", true))
	_, err = call(&admin.Principal, "true", project.ID)
	require.Error(t, err)
	h.authMode = auth.AuthModeDisabled
	_, err = call(nil, "true", project.ID)
	require.NoError(t, err, "auth-off resolves owner explicitly")
}

func TestGitHubProjectInboxDirectAuthorization(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "inbox-maintainer", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Inbox authorization")
	foreign := createProject(t, h, "Other inbox")
	call := func(projectID, login string, principal *models.Principal) error {
		form := url.Values{"project_id": {projectID}, "github_login": {login}, "enabled": {"true"}}
		req := httptest.NewRequest("POST", "/channels/github/project-inbox", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if principal != nil {
			req = req.WithContext(models.WithPrincipal(ctx, *principal))
		}
		return h.SaveGitHubProjectInbox(echo.New().NewContext(req, httptest.NewRecorder()))
	}
	require.ErrorIs(t, call(project.ID, "reviewer", nil), authz.ErrDenied)
	for _, role := range []string{"viewer", "editor", "maintainer"} {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, user.UserID, role))
		err := call(project.ID, "reviewer", &user.Principal)
		if role == "maintainer" {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, authz.ErrDenied)
		}
	}
	require.ErrorIs(t, call(foreign.ID, "reviewer", &user.Principal), authz.ErrDenied)
	require.NoError(t, call(project.ID, "", &user.Principal))
	saved, err := tc.stores.GitHubAuth.GetProjectInbox(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, saved.Enabled)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", true))
	require.ErrorIs(t, call(project.ID, "reviewer", &user.Principal), authz.ErrDenied)
	h.authMode = auth.AuthModeDisabled
	require.NoError(t, call(project.ID, "reviewer", nil))
	require.ErrorIs(t, call(project.ID, "", &user.Principal), authz.ErrDenied)
}

func TestDiscordIdentityBindingHandler(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	project := createProject(t, h, "Discord binding")
	owner := models.Principal{UserID: models.BootstrapUserID}
	entry := &models.DiscordAuthorizedUser{ProjectID: project.ID, DiscordUserID: "123456789012345678"}
	require.NoError(t, tc.stores.DiscordAuth.Create(models.WithPrincipal(ctx, owner), entry))
	call := func(principal *models.Principal, attested, projectID string) (string, error) {
		values := url.Values{"project_id": {projectID}, "application_user_id": {models.BootstrapUserID}, "attested": {attested}}
		req := httptest.NewRequest("POST", "/channels/discord/authorized-users/"+entry.ID+"/identity", strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if principal != nil {
			req = req.WithContext(models.WithPrincipal(ctx, *principal))
		}
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetPath("/channels/discord/authorized-users/:id/identity")
		c.SetParamNames("id")
		c.SetParamValues(entry.ID)
		if err := h.authorizeChannelTrustResource(c); err != nil {
			return "", err
		}
		err := h.BindDiscordApplicationUser(c)
		return rec.Body.String(), err
	}
	_, err := call(nil, "true", project.ID)
	require.Error(t, err)
	_, err = call(&owner, "false", project.ID)
	require.Error(t, err)
	_, err = call(&owner, "true", "foreign")
	require.Error(t, err)
	_, err = tc.stores.DiscordAuth.ResolveApplicationUser(ctx, project.ID, entry.DiscordUserID)
	require.Error(t, err)
	body, err := call(&owner, "true", project.ID)
	require.NoError(t, err)
	require.Contains(t, body, "Application user: "+models.BootstrapUserID)
	require.Contains(t, body, `name="attested"`)
	mapped, err := tc.stores.DiscordAuth.ResolveApplicationUser(ctx, project.ID, entry.DiscordUserID)
	require.NoError(t, err)
	require.Equal(t, models.BootstrapUserID, mapped)
	token, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	admin, err := org.Accept(ctx, token, "discord-binding-admin", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, admin.UserID, "member", false))
	_, err = call(&admin.Principal, "true", project.ID)
	require.Error(t, err)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, admin.UserID, "member", true))
	_, err = call(&admin.Principal, "true", project.ID)
	require.Error(t, err)
	h.authMode = auth.AuthModeDisabled
	_, err = call(nil, "true", project.ID)
	require.NoError(t, err)
}
