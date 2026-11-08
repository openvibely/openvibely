package handler

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestOAuthSetupIsBoundToInitiatingAdmin(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	token, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "setup-admin", "long-password-value", "")
	require.NoError(t, err)
	key := "actor-owned-setup"
	modelOAuthSetups.Lock()
	modelOAuthSetups.sessions[key] = &modelOAuthSetup{owner: h, actorUserID: user.UserID, expires: time.Now().Add(time.Minute), config: models.LLMConfig{OAuthAccessToken: "private-token"}}
	modelOAuthSetups.Unlock()
	t.Cleanup(func() { removeModelOAuthSetup(key) })
	own := models.WithPrincipal(ctx, user.Principal)
	other := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, err = h.modelOAuthSetupConfig(key, true, own)
	require.NoError(t, err)
	_, err = h.modelOAuthSetupConfig(key, true, other)
	require.ErrorIs(t, err, authz.ErrDenied)
	_, err = h.modelOAuthSetupConfig(key, true)
	require.ErrorIs(t, err, authz.ErrDenied)
	e := echo.New()
	for _, handler := range []echo.HandlerFunc{h.ModelOAuthSetupStatus, h.CancelModelOAuthSetup} {
		req := httptest.NewRequest("GET", "/models/oauth/setup", nil).WithContext(other)
		req.Header.Set(modelOAuthSetupHeader, key)
		require.ErrorIs(t, handler(e.NewContext(req, httptest.NewRecorder())), authz.ErrDenied)
	}
	_, err = h.modelOAuthSetupConfig(key, true, own)
	require.NoError(t, err, "foreign cancel must preserve setup")
	require.NoError(t, tc.stores.Organizations.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", false))
	_, err = h.modelOAuthSetupConfig(key, true, own)
	require.ErrorIs(t, err, authz.ErrDenied)
	require.ErrorIs(t, h.finishModelOAuthSetup(key, "replacement", "", 0, ""), authz.ErrDenied)
	modelOAuthSetups.Lock()
	saved := modelOAuthSetups.sessions[key].config.OAuthAccessToken
	modelOAuthSetups.Unlock()
	require.Equal(t, "private-token", saved, "revocation must not replace pending credentials")
}
