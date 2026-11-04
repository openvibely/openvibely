package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestGitHubCredentialsRequireCurrentIdentity(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	settings := &connectionIdentityStore{SettingsStore: env.Settings}
	svc := NewGitHubService(settings, "", "", "", "")
	svc.SetCredentialAuthorization(env.Organizations, false)
	require.NoError(t, env.Settings.SetMany(ctx, map[string]string{GitHubSettingAuthMode: GitHubAuthModePAT, GitHubSettingPAT: "preserved"}))
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	actor, err := org.Accept(ctx, invite, "connector", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
	for _, caller := range []context.Context{ctx, models.WithPrincipal(ctx, models.Principal{}), models.WithPrincipal(ctx, actor.Principal)} {
		_, err := svc.ConnectURL(caller)
		require.ErrorIs(t, err, authz.ErrDenied)
		require.ErrorIs(t, svc.HandleInstallCallback(caller, "123"), authz.ErrDenied)
		require.ErrorIs(t, svc.Disconnect(caller), authz.ErrDenied)
		require.Zero(t, settings.reads)
		token, err := env.Settings.Get(ctx, GitHubSettingPAT)
		require.NoError(t, err)
		require.Equal(t, "preserved", token)
	}
	svc.SetCredentialAuthorization(env.Organizations, true)
	require.ErrorIs(t, svc.Disconnect(models.WithPrincipal(ctx, actor.Principal)), authz.ErrDenied)
	require.NoError(t, svc.Disconnect(ctx))
	token, err := env.Settings.Get(ctx, GitHubSettingPAT)
	require.NoError(t, err)
	require.Empty(t, token)
}
