package service

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// ChannelStatusAuthorizationCounts contains only aggregate counts used by the
// prompt-safe list_channels summaries. The repositories that supply each count
// remain owned by the calling surface.
type ChannelStatusAuthorizationCounts struct {
	GitHubActors  int
	SlackUsers    int
	TelegramUsers int
	DiscordUsers  int
	XUsers        int
	EmailSenders  int
}

// ChannelStatusPreparationInput contains normalized provider status callbacks
// and aggregate data for the common list_channels preparation step.
type ChannelStatusPreparationInput struct {
	ProjectID string
	Settings  map[string]string

	GitHub                     GitHubConnectionStatus
	GitHubUsePersistedSettings bool
	Slack                      SlackConnectionStatus
	TelegramRunning            bool
	Discord                    DiscordConnectionStatus
	X                          XConnectionStatus
	Email                      EmailConnectionStatus
	EmailStatusProvided        bool

	AuthorizationCounts ChannelStatusAuthorizationCounts
	Webhooks            []models.WebhookEndpoint
	OutboundTargets     OutboundTargetStatusSummary
}

// ChannelStatusSnapshot is the shared, prompt-safe status projection consumed
// by the direct Chat and channel Chat response adapters.
type ChannelStatusSnapshot struct {
	OK                     bool
	ProjectID              string
	ConfiguredChannelCount int
	ConfiguredChannels     []string
	NoneConfigured         bool
	GitHub                 ChannelStatusGitHub
	Slack                  ChannelStatusSlack
	Telegram               ChannelStatusTelegram
	Discord                ChannelStatusDiscord
	X                      ChannelStatusX
	Email                  ChannelStatusEmail
	Webhooks               ChannelStatusWebhooks
	OutboundTargets        ChannelStatusOutboundTargets
}

type ChannelStatusGitHub struct {
	Configured           bool
	Connected            bool
	Status               string
	AuthMode             string
	AccountLogin         string
	AccountType          string
	AppConfigured        bool
	PATConfigured        bool
	AuthorizedActorCount int
}

type ChannelStatusSlack struct {
	Configured          bool
	Connected           bool
	Running             bool
	Status              string
	TeamName            string
	TeamID              string
	BotUserID           string
	BotTokenSource      string
	SendResponses       bool
	AuthorizedUserCount int
}

type ChannelStatusTelegram struct {
	Configured          bool
	Running             bool
	Status              string
	SendResponses       bool
	RichMessagesV2      bool
	AuthorizedUserCount int
}

type ChannelStatusDiscord struct {
	Configured          bool
	Connected           bool
	Running             bool
	Status              string
	BotUserID           string
	SendResponses       bool
	LastError           string
	AuthorizedUserCount int
}

type ChannelStatusX struct {
	Configured          bool
	Connected           bool
	Running             bool
	Status              string
	Username            string
	SendResponses       bool
	AuthorizedUserCount int
	LastError           string
}

type ChannelStatusEmail struct {
	Configured            bool
	Running               bool
	Status                string
	Provider              string
	Address               string
	IMAPHost              string
	IMAPPort              int
	SMTPHost              string
	SMTPPort              int
	SendResponses         bool
	SkipAttachments       bool
	AuthorizedSenderCount int
}

type ChannelStatusWebhooks struct {
	Total      int
	Active     int
	Disabled   int
	Configured bool
}

type ChannelStatusOutboundTargets struct {
	OutboundTargetStatusSummary
	ExplicitUnsavedTargetsAllowed bool
	MessagingAvailable            bool
}

// LoadChannelStatusSettings loads the canonical settings inventory used by all
// list_channels surfaces. Secrets are retained only in the in-process settings
// map so configuration can be classified; they are never part of the snapshot.
func LoadChannelStatusSettings(ctx context.Context, settingsRepo *repository.SettingsRepo, projectID string) map[string]string {
	if settingsRepo == nil {
		return map[string]string{}
	}
	keys := []string{
		GitHubSettingAuthMode,
		GitHubSettingAppID,
		GitHubSettingAppSlug,
		GitHubSettingAppPrivateKey,
		GitHubSettingPAT,
		githubSettingInstallationID,
		githubSettingAccountLogin,
		githubSettingAccountType,
		SlackSettingClientID,
		SlackSettingClientSecret,
		SlackSettingAppToken,
		SlackSettingBotToken,
		SlackSettingBotTokenOverride,
		SlackSettingBotTokenSource,
		SlackSettingSendResponses,
		TelegramSettingBotToken,
		TelegramSettingSendResponses,
		TelegramSettingRichMessagesV2,
		DiscordSettingBotToken,
		DiscordSettingSendResponses,
		XSettingConsumerKey,
		XSettingConsumerSecret,
		XSettingAccessToken,
		XSettingAccessTokenSecret,
		XSettingSendResponses,
		EmailSettingProvider,
		EmailSettingAddress,
		EmailSettingPassword,
		EmailSettingIMAPHost,
		EmailSettingSMTPHost,
		EmailSettingSendResponses,
		EmailSettingSkipAttachments,
	}
	if strings.TrimSpace(projectID) != "" {
		keys = append(keys, SendMessageAllowExplicitTargetsSetting+":"+strings.TrimSpace(projectID))
	}
	values, err := settingsRepo.GetMany(ctx, keys)
	if err != nil {
		return map[string]string{}
	}
	return values
}

// PrepareChannelStatus applies the common provider classification, safe
// normalization, aggregate counts, configured-channel list, and messaging
// policy used by every list_channels response surface.
func PrepareChannelStatus(input ChannelStatusPreparationInput) ChannelStatusSnapshot {
	projectID := strings.TrimSpace(input.ProjectID)
	out := ChannelStatusSnapshot{
		OK:                 projectID != "",
		ProjectID:          projectID,
		ConfiguredChannels: []string{},
		OutboundTargets: ChannelStatusOutboundTargets{
			OutboundTargetStatusSummary: OutboundTargetStatusSummary{ByPlatform: map[string]OutboundTargetPlatformSummary{}},
		},
	}
	if projectID == "" {
		out.NoneConfigured = true
		return out
	}

	settings := input.Settings
	get := func(key string) string { return strings.TrimSpace(settings[key]) }
	isFalse := func(key string) bool { return strings.EqualFold(get(key), "false") }
	isTrue := func(key string) bool { return strings.EqualFold(get(key), "true") }

	out.GitHub = prepareChannelStatusGitHub(input.GitHub, settings, input.GitHubUsePersistedSettings)
	out.GitHub.AuthorizedActorCount = input.AuthorizationCounts.GitHubActors
	if out.GitHub.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "github")
	}

	slack := input.Slack
	slack.Configured = slack.Configured || slack.HasClientID || slack.HasClientSecret || slack.HasAppToken || slack.HasBotToken || slack.HasBotTokenOverride || get(SlackSettingClientID) != "" || get(SlackSettingClientSecret) != "" || get(SlackSettingAppToken) != "" || get(SlackSettingBotToken) != "" || get(SlackSettingBotTokenOverride) != ""
	if slack.BotTokenSource == "" {
		slack.BotTokenSource = strings.ToLower(get(SlackSettingBotTokenSource))
	}
	out.Slack = ChannelStatusSlack{
		Configured:          slack.Configured,
		Connected:           slack.Connected,
		Running:             slack.Running,
		Status:              channelStatusConnected(slack.Configured, slack.Connected),
		TeamName:            strings.TrimSpace(slack.TeamName),
		TeamID:              strings.TrimSpace(slack.TeamID),
		BotUserID:           strings.TrimSpace(slack.BotUserID),
		BotTokenSource:      strings.TrimSpace(slack.BotTokenSource),
		SendResponses:       !isFalse(SlackSettingSendResponses),
		AuthorizedUserCount: input.AuthorizationCounts.SlackUsers,
	}
	if out.Slack.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "slack")
	}

	telegramConfigured := get(TelegramSettingBotToken) != ""
	out.Telegram = ChannelStatusTelegram{
		Configured:          telegramConfigured || input.TelegramRunning,
		Running:             input.TelegramRunning,
		Status:              channelStatusRunning(telegramConfigured || input.TelegramRunning, input.TelegramRunning),
		SendResponses:       !isFalse(TelegramSettingSendResponses),
		RichMessagesV2:      !isFalse(TelegramSettingRichMessagesV2),
		AuthorizedUserCount: input.AuthorizationCounts.TelegramUsers,
	}
	if out.Telegram.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "telegram")
	}

	discord := input.Discord
	if !discord.SendResponses && get(DiscordSettingSendResponses) == "" {
		discord.SendResponses = true
	}
	if get(DiscordSettingSendResponses) != "" {
		discord.SendResponses = !isFalse(DiscordSettingSendResponses)
	}
	out.Discord = ChannelStatusDiscord{
		Configured:          discord.Configured || discord.HasBotToken || get(DiscordSettingBotToken) != "",
		Connected:           discord.Connected,
		Running:             discord.Running,
		BotUserID:           strings.TrimSpace(discord.BotUserID),
		SendResponses:       discord.SendResponses,
		LastError:           channelStatusSafeSingleLine(discord.LastError),
		AuthorizedUserCount: input.AuthorizationCounts.DiscordUsers,
	}
	out.Discord.Status = channelStatusDiscord(out.Discord.Configured, out.Discord.Connected, out.Discord.Running)
	if out.Discord.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "discord")
	}

	xConfigured := get(XSettingConsumerKey) != "" && get(XSettingConsumerSecret) != "" && get(XSettingAccessToken) != "" && get(XSettingAccessTokenSecret) != ""
	xStatus := input.X
	xStatus.Configured = xStatus.Configured || xConfigured
	out.X = ChannelStatusX{
		Configured:          xStatus.Configured,
		Connected:           xStatus.Connected,
		Running:             xStatus.Running,
		Status:              channelStatusConnected(xStatus.Configured, xStatus.Connected),
		Username:            strings.TrimSpace(xStatus.Username),
		SendResponses:       !isFalse(XSettingSendResponses),
		AuthorizedUserCount: input.AuthorizationCounts.XUsers,
		LastError:           channelStatusSafeSingleLine(xStatus.LastError),
	}
	if out.X.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "x")
	}

	email := EmailConnectionStatus{Provider: EmailProviderCustom, IMAPPort: 993, SMTPPort: 587}
	if input.EmailStatusProvided {
		email = input.Email
	}
	if email.Provider == "" {
		email.Provider = NormalizeEmailProvider(get(EmailSettingProvider))
		if email.Provider == "" {
			email.Provider = EmailProviderCustom
		}
	}
	if email.Address == "" {
		email.Address = get(EmailSettingAddress)
	}
	if email.IMAPHost == "" {
		email.IMAPHost = get(EmailSettingIMAPHost)
	}
	if email.SMTPHost == "" {
		email.SMTPHost = get(EmailSettingSMTPHost)
	}
	email.Configured = email.Configured || email.Running || email.Address != "" || email.IMAPHost != "" || email.SMTPHost != "" || get(EmailSettingPassword) != ""
	out.Email = ChannelStatusEmail{
		Configured:            email.Configured,
		Running:               email.Running,
		Status:                channelStatusRunning(email.Configured, email.Running),
		Provider:              email.Provider,
		Address:               email.Address,
		IMAPHost:              email.IMAPHost,
		IMAPPort:              email.IMAPPort,
		SMTPHost:              email.SMTPHost,
		SMTPPort:              email.SMTPPort,
		SendResponses:         !isFalse(EmailSettingSendResponses),
		SkipAttachments:       isTrue(EmailSettingSkipAttachments),
		AuthorizedSenderCount: input.AuthorizationCounts.EmailSenders,
	}
	if out.Email.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "email")
	}

	out.Webhooks = summarizeChannelStatusWebhooks(input.Webhooks)
	if out.Webhooks.Configured {
		out.ConfiguredChannels = append(out.ConfiguredChannels, "webhooks")
	}

	out.OutboundTargets.OutboundTargetStatusSummary = input.OutboundTargets
	if out.OutboundTargets.ByPlatform == nil {
		out.OutboundTargets.ByPlatform = map[string]OutboundTargetPlatformSummary{}
	}
	out.OutboundTargets.ExplicitUnsavedTargetsAllowed = isTrue(SendMessageAllowExplicitTargetsSetting + ":" + projectID)
	out.OutboundTargets.MessagingAvailable = out.OutboundTargets.Configured || out.OutboundTargets.ExplicitUnsavedTargetsAllowed

	out.ConfiguredChannelCount = len(out.ConfiguredChannels)
	out.NoneConfigured = out.ConfiguredChannelCount == 0
	return out
}

func prepareChannelStatusGitHub(status GitHubConnectionStatus, settings map[string]string, usePersistedSettings bool) ChannelStatusGitHub {
	get := func(key string) string { return strings.TrimSpace(settings[key]) }
	patConfigured := status.HasPAT || get(GitHubSettingPAT) != ""
	appConfigured := status.AppConfigured || get(GitHubSettingAppID) != "" || get(GitHubSettingAppSlug) != "" || get(GitHubSettingAppPrivateKey) != ""

	if usePersistedSettings {
		storedMode := strings.ToLower(get(GitHubSettingAuthMode))
		authMode := GitHubAuthModePAT
		if storedMode == GitHubAuthModeApp || storedMode == GitHubAuthModePAT {
			authMode = storedMode
		} else if patConfigured {
			authMode = GitHubAuthModePAT
		} else if appConfigured {
			authMode = GitHubAuthModeApp
		}
		configured := patConfigured
		connected := patConfigured
		if authMode == GitHubAuthModeApp {
			configured = appConfigured
			connected = get(githubSettingInstallationID) != ""
		}
		return ChannelStatusGitHub{
			Configured:    configured,
			Connected:     connected,
			Status:        channelStatusConnected(configured, connected),
			AuthMode:      authMode,
			AccountLogin:  get(githubSettingAccountLogin),
			AccountType:   get(githubSettingAccountType),
			AppConfigured: appConfigured,
			PATConfigured: patConfigured,
		}
	}

	authMode := strings.TrimSpace(status.AuthMode)
	if authMode == "" {
		authMode = NormalizeGitHubAuthMode(get(GitHubSettingAuthMode))
	}
	configured := status.Configured || patConfigured || appConfigured || get(GitHubSettingAuthMode) != ""
	return ChannelStatusGitHub{
		Configured:    configured,
		Connected:     status.Connected,
		Status:        channelStatusConnected(configured, status.Connected),
		AuthMode:      authMode,
		AccountLogin:  strings.TrimSpace(status.AccountLogin),
		AccountType:   strings.TrimSpace(status.AccountType),
		AppConfigured: appConfigured,
		PATConfigured: patConfigured,
	}
}

func summarizeChannelStatusWebhooks(webhooks []models.WebhookEndpoint) ChannelStatusWebhooks {
	out := ChannelStatusWebhooks{Total: len(webhooks), Configured: len(webhooks) > 0}
	for _, webhook := range webhooks {
		if webhook.Enabled {
			out.Active++
		} else {
			out.Disabled++
		}
	}
	return out
}

func channelStatusConnected(configured, connected bool) string {
	switch {
	case connected:
		return "connected"
	case configured:
		return "configured_not_connected"
	default:
		return "not_configured"
	}
}

func channelStatusRunning(configured, running bool) string {
	switch {
	case running:
		return "running"
	case configured:
		return "configured_not_running"
	default:
		return "not_configured"
	}
}

func channelStatusDiscord(configured, connected, running bool) string {
	if connected {
		return "connected"
	}
	if configured && !running {
		return "gateway_offline"
	}
	if configured {
		return "configured_not_connected"
	}
	return "not_configured"
}

func channelStatusSafeSingleLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 240 {
		return value[:240]
	}
	return value
}

// OutboundTargetStatusSummaryFromTargets creates the same aggregate projection
// used by the repository summary and intentionally omits target identifiers.
func OutboundTargetStatusSummaryFromTargets(targets []models.ChannelTarget) OutboundTargetStatusSummary {
	out := OutboundTargetStatusSummary{
		Total:      len(targets),
		Configured: len(targets) > 0,
		ByPlatform: map[string]OutboundTargetPlatformSummary{},
	}
	for _, target := range targets {
		platform := strings.ToLower(strings.TrimSpace(target.Platform))
		if platform == "" {
			platform = "unknown"
		}
		kind := strings.ToLower(strings.TrimSpace(target.TargetKind))
		if kind == "" {
			kind = models.DefaultChannelTargetKind(platform)
		}
		platformSummary := out.ByPlatform[platform]
		platformSummary.Total++
		if target.Home {
			platformSummary.Home++
		}
		if strings.TrimSpace(target.Name) != "" {
			platformSummary.Named++
		}
		if platformSummary.ByKind == nil {
			platformSummary.ByKind = map[string]int{}
		}
		platformSummary.ByKind[kind]++
		out.ByPlatform[platform] = platformSummary
	}
	return out
}
