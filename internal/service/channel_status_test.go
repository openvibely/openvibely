package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestPrepareChannelStatusKeepsDirectAndChannelCommonFieldsInParity(t *testing.T) {
	settings := map[string]string{
		GitHubSettingAuthMode:         GitHubAuthModePAT,
		GitHubSettingPAT:              "github-pat-secret",
		githubSettingAccountLogin:     "ov-user",
		githubSettingAccountType:      "User",
		SlackSettingBotToken:          "slack-bot-secret",
		SlackSettingSendResponses:     "true",
		TelegramSettingBotToken:       "telegram-bot-secret",
		TelegramSettingRichMessagesV2: "true",
		DiscordSettingBotToken:        "discord-bot-secret",
		XSettingConsumerKey:           "x-consumer-key-secret",
		XSettingConsumerSecret:        "x-consumer-secret",
		XSettingAccessToken:           "x-access-token-secret",
		XSettingAccessTokenSecret:     "x-access-token-secret-2",
		EmailSettingAddress:           "alerts@example.com",
		EmailSettingPassword:          "email-password-secret",
		EmailSettingIMAPHost:          "imap.example.com",
		EmailSettingSMTPHost:          "smtp.example.com",
		SendMessageAllowExplicitTargetsSetting + ":parity-project": "true",
	}
	webhooks := []models.WebhookEndpoint{{Enabled: true}, {Enabled: false}, {Enabled: true}}
	targets := []models.ChannelTarget{
		{Platform: " Slack ", TargetKind: "channel", Name: "ops", Home: true},
		{Platform: "slack", TargetKind: "user"},
		{Platform: "email", TargetKind: "email", Name: "team"},
	}
	outbound := OutboundTargetStatusSummaryFromTargets(targets)
	counts := ChannelStatusAuthorizationCounts{
		GitHubActors:  1,
		SlackUsers:    2,
		TelegramUsers: 3,
		DiscordUsers:  4,
		XUsers:        5,
		EmailSenders:  6,
	}
	discordStatus := DiscordConnectionStatus{
		Configured:    true,
		Connected:     false,
		Running:       false,
		HasBotToken:   true,
		SendResponses: true,
		LastError:     " gateway unavailable\n\twith a second line ",
	}
	xStatus := XConnectionStatus{
		Configured: true,
		Connected:  true,
		Running:    true,
		Username:   "x-user",
		LastError:  strings.Repeat("long diagnostic ", 30),
	}
	base := ChannelStatusPreparationInput{
		ProjectID: projectIDForStatusParity,
		Settings:  settings,
		Slack: SlackConnectionStatus{
			Configured:     true,
			Connected:      true,
			Running:        true,
			TeamName:       "OpenVibely",
			TeamID:         "T1",
			BotUserID:      "B1",
			BotTokenSource: SlackBotTokenSourceOAuth,
		},
		TelegramRunning: true,
		Discord:         discordStatus,
		X:               xStatus,
		Email: EmailConnectionStatus{
			Configured: true,
			Running:    true,
			Provider:   EmailProviderCustom,
			Address:    "alerts@example.com",
			IMAPHost:   "imap.example.com",
			IMAPPort:   993,
			SMTPHost:   "smtp.example.com",
			SMTPPort:   587,
		},
		EmailStatusProvided: true,
		AuthorizationCounts: counts,
		Webhooks:            webhooks,
		OutboundTargets:     outbound,
	}
	base.GitHub = GitHubConnectionStatus{
		Configured:   true,
		Connected:    true,
		AuthMode:     GitHubAuthModePAT,
		AccountLogin: "ov-user",
		AccountType:  "User",
		HasPAT:       true,
	}

	direct := PrepareChannelStatus(base)
	channelInput := base
	channelInput.GitHub = GitHubConnectionStatus{}
	channelInput.GitHubUsePersistedSettings = true
	channel := PrepareChannelStatus(channelInput)

	require.Equal(t, direct, channel)
	require.Equal(t, []string{"github", "slack", "telegram", "discord", "x", "email", "webhooks"}, direct.ConfiguredChannels)
	require.Equal(t, 7, direct.ConfiguredChannelCount)
	require.Equal(t, "gateway_offline", direct.Discord.Status)
	require.Equal(t, "gateway unavailable with a second line", direct.Discord.LastError)
	require.NotContains(t, direct.X.LastError, "\n")
	require.LessOrEqual(t, len(direct.X.LastError), 240)
	require.Equal(t, counts.EmailSenders, direct.Email.AuthorizedSenderCount)
	require.Equal(t, 3, direct.Webhooks.Total)
	require.Equal(t, 2, direct.Webhooks.Active)
	require.Equal(t, 1, direct.Webhooks.Disabled)
	require.True(t, direct.OutboundTargets.ExplicitUnsavedTargetsAllowed)
	require.True(t, direct.OutboundTargets.MessagingAvailable)
	require.NotNil(t, direct.OutboundTargets.ByPlatform)
	require.Equal(t, 2, direct.OutboundTargets.ByPlatform["slack"].Total)
	require.Equal(t, map[string]int{"channel": 1, "user": 1}, direct.OutboundTargets.ByPlatform["slack"].ByKind)

	encoded, err := json.Marshal(direct)
	require.NoError(t, err)
	for _, secret := range []string{"github-pat-secret", "slack-bot-secret", "telegram-bot-secret", "discord-bot-secret", "email-password-secret", "x-access-token-secret"} {
		require.NotContains(t, string(encoded), secret)
	}
}

func TestChannelListChannelsWithoutProjectReturnsEmptySafeSummary(t *testing.T) {
	handlers := buildChannelUtilityActionHandlers(channelUtilityActionHandlerOptions{})
	out, err := handlers["list_channels"](context.Background(), json.RawMessage(`{}`))
	require.NoError(t, err)

	var got channelStatusActionResponse
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.False(t, got.OK)
	require.Empty(t, got.ProjectID)
	require.Empty(t, got.ConfiguredChannels)
	require.Zero(t, got.ConfiguredChannelCount)
	require.True(t, got.NoneConfigured)
	require.NotNil(t, got.OutboundTargets.ByPlatform)
	require.Empty(t, got.OutboundTargets.ByPlatform)
}

const projectIDForStatusParity = "parity-project"
