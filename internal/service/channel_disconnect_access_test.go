package service

import (
	"context"
	"errors"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type disconnectFailStore struct {
	persistence.SettingsStore
	fail bool
}

func (s *disconnectFailStore) SetMany(ctx context.Context, values map[string]string) error {
	if s.fail {
		return errors.New("save failed")
	}
	return s.SettingsStore.SetMany(ctx, values)
}

func TestChannelDisconnectPreservesConfigurationAndRuntimeOnDenial(t *testing.T) {
	for _, provider := range []string{"github-pat", "github-app", "slack", "discord"} {
		t.Run(provider, func(t *testing.T) {
			env := teststores.New(t)
			ctx := t.Context()
			org := NewOrganizationService(env.Organizations, "secret")
			invite, err := org.Invite(ctx, models.BootstrapUserID, "admin")
			require.NoError(t, err)
			actor, err := org.Accept(ctx, invite, "connector", "long-password-value", "")
			require.NoError(t, err)
			settings := &disconnectFailStore{SettingsStore: env.Settings}
			values := map[string]string{}
			cancelled := false
			var disconnect func(context.Context) error
			switch provider {
			case "github-pat":
				values = map[string]string{GitHubSettingAuthMode: GitHubAuthModePAT, GitHubSettingPAT: "token", GitHubSettingPATUserLogin: "user"}
				disconnect = NewGitHubService(settings, "", "", "", "").Disconnect
			case "github-app":
				values = map[string]string{GitHubSettingAuthMode: GitHubAuthModeApp, GitHubSettingAppID: "123", GitHubSettingAppSlug: "slug", GitHubSettingAppPrivateKey: "key", githubSettingInstallationID: "42", githubSettingAccountLogin: "user"}
				disconnect = NewGitHubService(settings, "", "", "", "").Disconnect
			case "slack":
				values = map[string]string{SlackSettingBotToken: "token", SlackSettingTeamID: "team", SlackSettingBotTokenOverride: "override"}
				svc := &SlackService{settingsRepo: settings, running: true, cancel: func() { cancelled = true }}
				disconnect = svc.Disconnect
			case "discord":
				values = map[string]string{DiscordSettingBotToken: "token", DiscordSettingBotUserID: "user"}
				svc := &DiscordService{settingsRepo: settings, running: true, cancel: func() { cancelled = true }}
				disconnect = svc.Disconnect
			}
			require.NoError(t, env.Settings.SetMany(ctx, values))
			principal := models.WithPrincipal(ctx, actor.Principal)
			unchanged := func() {
				for key, want := range values {
					got, err := env.Settings.Get(ctx, key)
					require.NoError(t, err)
					require.Equal(t, want, got, key)
				}
				require.False(t, cancelled)
			}
			settings.fail = true
			require.ErrorContains(t, disconnect(principal), "save failed")
			unchanged()
			settings.fail = false
			require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "member", false))
			require.ErrorIs(t, disconnect(principal), persistence.ErrOrgDenied)
			unchanged()
			require.NoError(t, env.Organizations.ChangeMember(ctx, models.BootstrapUserID, actor.UserID, "admin", false))
			require.NoError(t, disconnect(principal))
			key := GitHubSettingPAT
			switch provider {
			case "github-app":
				key = githubSettingInstallationID
			case "slack":
				key = SlackSettingBotToken
				require.True(t, cancelled)
			case "discord":
				key = DiscordSettingBotToken
				require.True(t, cancelled)
			}
			got, err := env.Settings.Get(ctx, key)
			require.NoError(t, err)
			require.Empty(t, got)
		})
	}
}
