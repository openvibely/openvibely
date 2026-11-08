package handler

import (
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestChannelOAuthSavedActorAndSingleUse(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.authMode = auth.AuthModeLocal
	token, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "channel-admin", "long-password-value", "")
	require.NoError(t, err)
	e := echo.New()
	contextFor := func(path string, p models.Principal) echo.Context {
		req := httptest.NewRequest("GET", path, nil).WithContext(ctx)
		if p.UserID != "" {
			req = req.WithContext(models.WithPrincipal(ctx, p))
		}
		return e.NewContext(req, httptest.NewRecorder())
	}
	for _, provider := range []string{"github", "slack"} {
		t.Run(provider, func(t *testing.T) {
			require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
			begin := func() string {
				destination := "https://provider.example/authorize"
				if provider == "slack" {
					destination += "?state=service-generated-state"
				}
				result, err := h.bindChannelOAuth(contextFor("/connect", user.Principal), provider, destination)
				require.NoError(t, err)
				u, err := url.Parse(result)
				require.NoError(t, err)
				state := u.Query().Get("state")
				require.NotEmpty(t, state)
				if provider == "slack" {
					require.Equal(t, "service-generated-state", state)
				}
				return state
			}
			state := begin()
			callback := contextFor("/callback?state="+url.QueryEscape(state), models.Principal{UserID: models.BootstrapUserID})
			require.NoError(t, h.consumeChannelOAuth(callback, provider))
			saved, _ := models.PrincipalFromContext(callback.Request().Context())
			require.Equal(t, user.UserID, saved.UserID)
			require.ErrorIs(t, h.consumeChannelOAuth(callback, provider), authz.ErrDenied)
			state = begin()
			require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
			callback = contextFor("/callback?state="+url.QueryEscape(state), models.Principal{UserID: models.BootstrapUserID})
			// Exercise actual callback: denial precedes provider/service access, even for another owner browser.
			if provider == "github" {
				err = h.handleGitHubCallback(callback)
			} else {
				err = h.handleSlackCallback(callback)
			}
			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, 404, httpErr.Code)
			require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", false))
			require.ErrorIs(t, h.consumeChannelOAuth(callback, provider), authz.ErrDenied, "revoked attempt consumed state")
			state = begin()
			h.channelOAuthMu.Lock()
			flow := h.channelOAuthFlows[state]
			flow.expires = time.Now().Add(-time.Second)
			h.channelOAuthFlows[state] = flow
			h.channelOAuthMu.Unlock()
			require.ErrorIs(t, h.consumeChannelOAuth(contextFor("/callback?state="+url.QueryEscape(state), user.Principal), provider), authz.ErrDenied)
			require.ErrorIs(t, h.consumeChannelOAuth(contextFor("/callback", user.Principal), provider), authz.ErrDenied)
			state = begin()
			require.ErrorIs(t, h.consumeChannelOAuth(contextFor("/callback?state="+url.QueryEscape(state), user.Principal), "wrong-provider"), authz.ErrDenied)
		})
	}
	removedURL, err := h.bindChannelOAuth(contextFor("/connect", user.Principal), "github", "https://provider.example/install")
	require.NoError(t, err)
	removed, err := url.Parse(removedURL)
	require.NoError(t, err)
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "admin", true))
	require.ErrorIs(t, h.consumeChannelOAuth(contextFor("/callback?state="+removed.Query().Get("state"), user.Principal), "github"), authz.ErrDenied)
	h.authMode = auth.AuthModeDisabled
	destination, err := h.bindChannelOAuth(contextFor("/connect", models.Principal{}), "github", "https://provider.example/install")
	require.NoError(t, err)
	u, err := url.Parse(destination)
	require.NoError(t, err)
	callback := contextFor("/callback?state="+u.Query().Get("state"), models.Principal{})
	require.NoError(t, h.consumeChannelOAuth(callback, "github"))
	actor, _ := models.PrincipalFromContext(callback.Request().Context())
	require.Equal(t, models.BootstrapUserID, actor.UserID)
}
