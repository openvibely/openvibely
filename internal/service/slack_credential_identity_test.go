package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestSlackCredentialsRequireCurrentIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	cancelled := false
	svc := &SlackService{settingsRepo: env.Settings, workerSvc: worker, running: true, cancel: func() { cancelled = true }}
	require.NoError(t, env.Settings.SetMany(ctx, map[string]string{SlackSettingClientID: "client", SlackSettingClientSecret: "secret", SlackSettingBotToken: "preserved", SlackSettingOAuthState: "state"}))
	checkDenied := func(c context.Context) {
		t.Helper()
		_, err := svc.ConnectURL(c, "https://example.test/callback")
		require.ErrorIs(t, err, authz.ErrDenied)
		require.ErrorIs(t, svc.HandleOAuthCallback(c, "code", "state", "https://example.test/callback"), authz.ErrDenied)
		require.ErrorIs(t, svc.Disconnect(c), authz.ErrDenied)
		token, err := env.Settings.Get(ctx, SlackSettingBotToken)
		require.NoError(t, err)
		require.Equal(t, "preserved", token)
		state, err := env.Settings.Get(ctx, SlackSettingOAuthState)
		require.NoError(t, err)
		require.Equal(t, "state", state)
		require.False(t, cancelled)
	}
	checkDenied(ctx)
	checkDenied(models.WithPrincipal(ctx, models.Principal{}))
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	actor, err := org.Accept(ctx, invite, "connector", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
	checkDenied(models.WithPrincipal(ctx, actor.Principal))
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	// Auth-off resolves the bootstrap owner; it does not elevate an explicit member.
	checkDenied(models.WithPrincipal(ctx, actor.Principal))
	require.NoError(t, svc.Disconnect(ctx))
	require.True(t, cancelled)
	token, err := env.Settings.Get(ctx, SlackSettingBotToken)
	require.NoError(t, err)
	require.Empty(t, token)
}
