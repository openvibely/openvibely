package handler

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

type channelStatusToolResponse struct {
	OK                     bool                         `json:"ok"`
	ProjectID              string                       `json:"project_id,omitempty"`
	ConfiguredChannelCount int                          `json:"configured_channel_count"`
	ConfiguredChannels     []string                     `json:"configured_channels"`
	NoneConfigured         bool                         `json:"none_configured"`
	GitHub                 githubChannelStatusSummary   `json:"github"`
	Slack                  slackChannelStatusSummary    `json:"slack"`
	Telegram               telegramChannelStatusSummary `json:"telegram"`
	Discord                discordChannelStatusSummary  `json:"discord"`
	Email                  emailChannelStatusSummary    `json:"email"`
	Webhooks               webhookStatusSummary         `json:"webhooks"`
	OutboundTargets        outboundTargetsStatusSummary `json:"outbound_message_targets"`
}

type githubChannelStatusSummary struct {
	Configured           bool   `json:"configured"`
	Connected            bool   `json:"connected"`
	Status               string `json:"status"`
	AuthMode             string `json:"auth_mode,omitempty"`
	AccountLogin         string `json:"account_login,omitempty"`
	AccountType          string `json:"account_type,omitempty"`
	AppConfigured        bool   `json:"app_configured"`
	PATConfigured        bool   `json:"pat_configured"`
	AuthorizedActorCount int    `json:"authorized_actor_count"`
}

type slackChannelStatusSummary struct {
	Configured          bool   `json:"configured"`
	Connected           bool   `json:"connected"`
	Running             bool   `json:"running"`
	Status              string `json:"status"`
	TeamName            string `json:"team_name,omitempty"`
	TeamID              string `json:"team_id,omitempty"`
	BotUserID           string `json:"bot_user_id,omitempty"`
	BotTokenSource      string `json:"bot_token_source,omitempty"`
	SendResponses       bool   `json:"send_responses"`
	AuthorizedUserCount int    `json:"authorized_user_count"`
}

type telegramChannelStatusSummary struct {
	Configured          bool   `json:"configured"`
	Running             bool   `json:"running"`
	Status              string `json:"status"`
	SendResponses       bool   `json:"send_responses"`
	RichMessagesV2      bool   `json:"rich_messages_v2"`
	AuthorizedUserCount int    `json:"authorized_user_count"`
}

type discordChannelStatusSummary struct {
	Configured          bool   `json:"configured"`
	Connected           bool   `json:"connected"`
	Running             bool   `json:"running"`
	Status              string `json:"status"`
	BotUserID           string `json:"bot_user_id,omitempty"`
	SendResponses       bool   `json:"send_responses"`
	LastError           string `json:"last_error,omitempty"`
	AuthorizedUserCount int    `json:"authorized_user_count"`
}

type emailChannelStatusSummary struct {
	Configured            bool   `json:"configured"`
	Running               bool   `json:"running"`
	Status                string `json:"status"`
	Provider              string `json:"provider,omitempty"`
	Address               string `json:"address,omitempty"`
	IMAPHost              string `json:"imap_host,omitempty"`
	IMAPPort              int    `json:"imap_port,omitempty"`
	SMTPHost              string `json:"smtp_host,omitempty"`
	SMTPPort              int    `json:"smtp_port,omitempty"`
	SendResponses         bool   `json:"send_responses"`
	SkipAttachments       bool   `json:"skip_attachments"`
	AuthorizedSenderCount int    `json:"authorized_sender_count"`
}

type webhookStatusSummary struct {
	Total       int                    `json:"total"`
	Active      int                    `json:"active"`
	Disabled    int                    `json:"disabled"`
	Configured  bool                   `json:"configured"`
	EndpointIDs []webhookEndpointLabel `json:"endpoints,omitempty"`
}

type webhookEndpointLabel struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type outboundTargetsStatusSummary struct {
	service.OutboundTargetStatusSummary
	ExplicitUnsavedTargetsAllowed bool `json:"explicit_unsaved_targets_allowed"`
	MessagingAvailable            bool `json:"messaging_available"`
}

type outboundTargetPlatformSummary = service.OutboundTargetPlatformSummary

func (h *Handler) executeListChannels(ctx context.Context, projectID string) string {
	resp := h.buildChannelStatusSummary(ctx, strings.TrimSpace(projectID))
	b, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return `{"ok":false,"error":"failed to render channel status"}`
	}
	return string(b)
}

func (h *Handler) buildChannelStatusSummary(ctx context.Context, projectID string) channelStatusToolResponse {
	settings := h.channelStatusSettings(ctx, projectID)
	get := func(key string) string { return strings.TrimSpace(settings[key]) }
	isFalse := func(key string) bool { return strings.EqualFold(get(key), "false") }
	isTrue := func(key string) bool { return strings.EqualFold(get(key), "true") }

	resp := channelStatusToolResponse{
		OK:                 projectID != "",
		ProjectID:          projectID,
		ConfiguredChannels: []string{},
		OutboundTargets: outboundTargetsStatusSummary{
			OutboundTargetStatusSummary: service.OutboundTargetStatusSummary{
				ByPlatform: map[string]outboundTargetPlatformSummary{},
			},
		},
	}
	if projectID == "" {
		resp.NoneConfigured = true
		return resp
	}

	github := service.GitHubConnectionStatus{}
	if h.githubSvc != nil {
		if status, err := h.githubSvc.GetConnectionStatus(ctx); err == nil {
			github = status
		}
	}
	if github.AuthMode == "" {
		github.AuthMode = service.NormalizeGitHubAuthMode(get(service.GitHubSettingAuthMode))
	}
	patConfigured := github.HasPAT || get(service.GitHubSettingPAT) != ""
	github.AppConfigured = github.AppConfigured || get(service.GitHubSettingAppID) != "" || get(service.GitHubSettingAppSlug) != "" || get(service.GitHubSettingAppPrivateKey) != ""
	github.Configured = github.Configured || patConfigured || github.AppConfigured || get(service.GitHubSettingAuthMode) != ""
	resp.GitHub = githubChannelStatusSummary{
		Configured:    github.Configured,
		Connected:     github.Connected,
		Status:        connectedStatus(github.Configured, github.Connected),
		AuthMode:      github.AuthMode,
		AccountLogin:  strings.TrimSpace(github.AccountLogin),
		AccountType:   strings.TrimSpace(github.AccountType),
		AppConfigured: github.AppConfigured,
		PATConfigured: patConfigured,
	}
	if h.githubAuthRepo != nil {
		if actors, err := h.githubAuthRepo.ListAuthorizedActors(ctx); err == nil {
			resp.GitHub.AuthorizedActorCount = len(actors)
		}
	}
	if resp.GitHub.Configured {
		resp.ConfiguredChannels = append(resp.ConfiguredChannels, "github")
	}

	slack := service.SlackConnectionStatus{}
	if h.slackSvc != nil {
		if status, err := h.slackSvc.GetConnectionStatus(ctx); err == nil {
			slack = status
		}
	}
	slack.Configured = slack.Configured || slack.HasClientID || slack.HasClientSecret || slack.HasAppToken || slack.HasBotToken || slack.HasBotTokenOverride || get(service.SlackSettingClientID) != "" || get(service.SlackSettingClientSecret) != "" || get(service.SlackSettingAppToken) != "" || get(service.SlackSettingBotToken) != "" || get(service.SlackSettingBotTokenOverride) != ""
	if slack.BotTokenSource == "" {
		slack.BotTokenSource = strings.ToLower(get(service.SlackSettingBotTokenSource))
	}
	resp.Slack = slackChannelStatusSummary{
		Configured:     slack.Configured,
		Connected:      slack.Connected,
		Running:        slack.Running,
		Status:         connectedStatus(slack.Configured, slack.Connected),
		TeamName:       strings.TrimSpace(slack.TeamName),
		TeamID:         strings.TrimSpace(slack.TeamID),
		BotUserID:      strings.TrimSpace(slack.BotUserID),
		BotTokenSource: strings.TrimSpace(slack.BotTokenSource),
		SendResponses:  !isFalse(service.SlackSettingSendResponses),
	}
	if h.slackAuthRepo != nil {
		if count, err := h.slackAuthRepo.CountByProject(ctx, projectID); err == nil {
			resp.Slack.AuthorizedUserCount = count
		}
	}
	if resp.Slack.Configured {
		resp.ConfiguredChannels = append(resp.ConfiguredChannels, "slack")
	}

	telegramConfigured := get(service.TelegramSettingBotToken) != ""
	telegramRunning := h.telegramService != nil && h.telegramService.IsRunning()
	resp.Telegram = telegramChannelStatusSummary{
		Configured:     telegramConfigured || telegramRunning,
		Running:        telegramRunning,
		Status:         runningStatus(telegramConfigured || telegramRunning, telegramRunning),
		SendResponses:  !isFalse(service.TelegramSettingSendResponses),
		RichMessagesV2: !isFalse(service.TelegramSettingRichMessagesV2),
	}
	if h.telegramAuthRepo != nil {
		if count, err := h.telegramAuthRepo.CountByProject(ctx, projectID); err == nil {
			resp.Telegram.AuthorizedUserCount = count
		}
	}
	if resp.Telegram.Configured {
		resp.ConfiguredChannels = append(resp.ConfiguredChannels, "telegram")
	}

	discord := service.DiscordConnectionStatus{SendResponses: true}
	if h.discordSvc != nil {
		if status, err := h.discordSvc.GetConnectionStatus(ctx); err == nil {
			discord = status
		}
	}
	discord.Configured = discord.Configured || discord.HasBotToken || get(service.DiscordSettingBotToken) != ""
	if !discord.SendResponses && get(service.DiscordSettingSendResponses) == "" {
		discord.SendResponses = true
	}
	if get(service.DiscordSettingSendResponses) != "" {
		discord.SendResponses = !isFalse(service.DiscordSettingSendResponses)
	}
	resp.Discord = discordChannelStatusSummary{
		Configured:    discord.Configured,
		Connected:     discord.Connected,
		Running:       discord.Running,
		Status:        discordStatus(discord),
		BotUserID:     strings.TrimSpace(discord.BotUserID),
		SendResponses: discord.SendResponses,
		LastError:     safeSingleLine(discord.LastError),
	}
	if h.discordAuthRepo != nil {
		if count, err := h.discordAuthRepo.CountByProject(ctx, projectID); err == nil {
			resp.Discord.AuthorizedUserCount = count
		}
	}
	if resp.Discord.Configured {
		resp.ConfiguredChannels = append(resp.ConfiguredChannels, "discord")
	}

	email := service.EmailConnectionStatus{Provider: service.EmailProviderCustom, IMAPPort: 993, SMTPPort: 587}
	if h.emailService != nil {
		email = h.emailService.GetConnectionStatus(ctx)
	}
	if email.Provider == "" {
		email.Provider = service.NormalizeEmailProvider(get(service.EmailSettingProvider))
		if email.Provider == "" {
			email.Provider = service.EmailProviderCustom
		}
	}
	if email.Address == "" {
		email.Address = get(service.EmailSettingAddress)
	}
	if email.IMAPHost == "" {
		email.IMAPHost = get(service.EmailSettingIMAPHost)
	}
	if email.SMTPHost == "" {
		email.SMTPHost = get(service.EmailSettingSMTPHost)
	}
	email.Configured = email.Configured || email.Running || email.Address != "" || email.IMAPHost != "" || email.SMTPHost != "" || get(service.EmailSettingPassword) != ""
	resp.Email = emailChannelStatusSummary{
		Configured:      email.Configured,
		Running:         email.Running,
		Status:          runningStatus(email.Configured, email.Running),
		Provider:        email.Provider,
		Address:         email.Address,
		IMAPHost:        email.IMAPHost,
		IMAPPort:        email.IMAPPort,
		SMTPHost:        email.SMTPHost,
		SMTPPort:        email.SMTPPort,
		SendResponses:   !isFalse(service.EmailSettingSendResponses),
		SkipAttachments: isTrue(service.EmailSettingSkipAttachments),
	}
	if h.emailAuthRepo != nil {
		if count, err := h.emailAuthRepo.CountByProject(ctx, projectID); err == nil {
			resp.Email.AuthorizedSenderCount = count
		}
	}
	if resp.Email.Configured {
		resp.ConfiguredChannels = append(resp.ConfiguredChannels, "email")
	}

	if h.webhookRepo != nil {
		if webhooks, err := h.webhookRepo.ListCardsByProject(ctx, projectID); err == nil {
			resp.Webhooks = summarizeWebhooks(webhooks)
		}
	}
	if resp.Webhooks.Configured {
		resp.ConfiguredChannels = append(resp.ConfiguredChannels, "webhooks")
	}

	if h.channelTargetRepo != nil {
		if summary, err := h.channelTargetRepo.SummarizeByProject(ctx, projectID); err == nil {
			resp.OutboundTargets = outboundTargetsStatusFromRepoSummary(summary)
		}
	}
	resp.OutboundTargets.ExplicitUnsavedTargetsAllowed = isTrue(service.SendMessageAllowExplicitTargetsSetting + ":" + projectID)
	resp.OutboundTargets.MessagingAvailable = resp.OutboundTargets.Configured || resp.OutboundTargets.ExplicitUnsavedTargetsAllowed

	resp.ConfiguredChannelCount = len(resp.ConfiguredChannels)
	resp.NoneConfigured = resp.ConfiguredChannelCount == 0
	return resp
}

func (h *Handler) channelStatusSettings(ctx context.Context, projectID string) map[string]string {
	if h.settingsRepo == nil {
		return map[string]string{}
	}
	keys := []string{
		service.GitHubSettingAuthMode,
		service.GitHubSettingAppID,
		service.GitHubSettingAppSlug,
		service.GitHubSettingAppPrivateKey,
		service.GitHubSettingPAT,
		service.SlackSettingClientID,
		service.SlackSettingClientSecret,
		service.SlackSettingAppToken,
		service.SlackSettingBotToken,
		service.SlackSettingBotTokenOverride,
		service.SlackSettingBotTokenSource,
		service.SlackSettingSendResponses,
		service.TelegramSettingBotToken,
		service.TelegramSettingSendResponses,
		service.TelegramSettingRichMessagesV2,
		service.DiscordSettingBotToken,
		service.DiscordSettingSendResponses,
		service.EmailSettingProvider,
		service.EmailSettingAddress,
		service.EmailSettingPassword,
		service.EmailSettingIMAPHost,
		service.EmailSettingSMTPHost,
		service.EmailSettingSendResponses,
		service.EmailSettingSkipAttachments,
	}
	if projectID != "" {
		keys = append(keys, service.SendMessageAllowExplicitTargetsSetting+":"+projectID)
	}
	values, err := h.settingsRepo.GetMany(ctx, keys)
	if err != nil {
		return map[string]string{}
	}
	return values
}

func summarizeWebhooks(webhooks []models.WebhookEndpoint) webhookStatusSummary {
	out := webhookStatusSummary{
		Total:       len(webhooks),
		Configured:  len(webhooks) > 0,
		EndpointIDs: make([]webhookEndpointLabel, 0, len(webhooks)),
	}
	for _, webhook := range webhooks {
		if webhook.Enabled {
			out.Active++
		} else {
			out.Disabled++
		}
		out.EndpointIDs = append(out.EndpointIDs, webhookEndpointLabel{ID: strings.TrimSpace(webhook.ID), Name: strings.TrimSpace(webhook.Name), Enabled: webhook.Enabled})
	}
	return out
}

func outboundTargetsStatusFromRepoSummary(summary repository.ChannelTargetProjectSummary) outboundTargetsStatusSummary {
	return outboundTargetsStatusSummary{
		OutboundTargetStatusSummary: service.OutboundTargetStatusSummaryFromRepoSummary(summary),
	}
}

func summarizeOutboundTargets(targets []models.ChannelTarget) outboundTargetsStatusSummary {
	out := outboundTargetsStatusSummary{
		OutboundTargetStatusSummary: service.OutboundTargetStatusSummary{
			Total:      len(targets),
			Configured: len(targets) > 0,
			ByPlatform: map[string]outboundTargetPlatformSummary{},
		},
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

func connectedStatus(configured, connected bool) string {
	switch {
	case connected:
		return "connected"
	case configured:
		return "configured_not_connected"
	default:
		return "not_configured"
	}
}

func runningStatus(configured, running bool) string {
	switch {
	case running:
		return "running"
	case configured:
		return "configured_not_running"
	default:
		return "not_configured"
	}
}

func discordStatus(status service.DiscordConnectionStatus) string {
	if status.Connected {
		return "connected"
	}
	if status.Configured && !status.Running {
		return "gateway_offline"
	}
	if status.Configured {
		return "configured_not_connected"
	}
	return "not_configured"
}

func safeSingleLine(value string) string {
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
