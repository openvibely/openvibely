package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

// Count credential reads to verify rejection occurs before provider setup.
type connectionIdentityStore struct {
	persistence.SettingsStore
	reads int
}

func (s *connectionIdentityStore) Get(ctx context.Context, key string) (string, error) {
	s.reads++
	return s.SettingsStore.Get(ctx, key)
}
func TestChannelConnectionIdentityBeforeCredentialUse(t *testing.T) {
	env := teststores.New(t)
	ctx := t.Context()
	worker := &WorkerService{}
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, false)
	settings := &connectionIdentityStore{SettingsStore: env.Settings}
	cancelled := false
	discord := &DiscordService{settingsRepo: settings, workerSvc: worker, running: true, cancel: func() { cancelled = true }}
	slack := &SlackService{settingsRepo: settings, workerSvc: worker}
	email := &EmailService{settingsRepo: settings, workerSvc: worker}
	require.NoError(t, env.Settings.Set(ctx, DiscordSettingBotToken, "preserved"))
	org := NewOrganizationService(env.Organizations, "secret")
	invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
	require.NoError(t, err)
	actor, err := org.Accept(ctx, invite, "connector", "long-password-value", "")
	require.NoError(t, err)
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
	for _, caller := range []context.Context{ctx, models.WithPrincipal(ctx, models.Principal{}), models.WithPrincipal(ctx, actor.Principal)} {
		require.ErrorIs(t, discord.Disconnect(caller), authz.ErrDenied)
		require.ErrorIs(t, discord.TestConnection(caller), authz.ErrDenied)
		require.ErrorIs(t, slack.TestConnection(caller), authz.ErrDenied)
		require.ErrorIs(t, email.TestConnection(caller), authz.ErrDenied)
		require.Zero(t, settings.reads)
		require.False(t, cancelled)
		token, err := env.Settings.Get(ctx, DiscordSettingBotToken)
		require.NoError(t, err)
		require.Equal(t, "preserved", token)
	}
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "admin", false))
	require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "admin", true))
	removed := models.WithPrincipal(ctx, actor.Principal)
	require.ErrorIs(t, email.TestConnection(removed), authz.ErrDenied)
	require.ErrorIs(t, slack.TestConnection(removed), authz.ErrDenied)
	require.ErrorIs(t, discord.TestConnection(removed), authz.ErrDenied)
	require.Zero(t, settings.reads)
	worker.SetRunAuthorization(env.RunIdentities, env.Organizations, env.ProjectAccess, true)
	require.NoError(t, discord.Disconnect(ctx))
	require.True(t, cancelled)
	// With no token configured, auth-off reaches normal validation, not a denial.
	require.ErrorContains(t, discord.TestConnection(ctx), "not configured")
	require.Positive(t, settings.reads)
	require.ErrorContains(t, email.TestConnection(ctx), "not fully configured")
}
