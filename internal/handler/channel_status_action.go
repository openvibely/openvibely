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
	X                      xChannelStatusSummary        `json:"x"`
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

type xChannelStatusSummary struct {
	Configured          bool   `json:"configured"`
	Connected           bool   `json:"connected"`
	Running             bool   `json:"running"`
	Status              string `json:"status"`
	Username            string `json:"username,omitempty"`
	SendResponses       bool   `json:"send_responses"`
	AuthorizedUserCount int    `json:"authorized_user_count"`
	LastError           string `json:"last_error,omitempty"`
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

func (h *Handler) executeListChannels(ctx context.Context, projectID string) string {
	resp := h.buildChannelStatusSummary(ctx, strings.TrimSpace(projectID))
	b, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return `{"ok":false,"error":"failed to render channel status"}`
	}
	return string(b)
}

func (h *Handler) buildChannelStatusSummary(ctx context.Context, projectID string) channelStatusToolResponse {
	projectID = strings.TrimSpace(projectID)
	settings := service.LoadChannelStatusSettings(ctx, h.settingsRepo, projectID)
	input := service.ChannelStatusPreparationInput{
		ProjectID:                  projectID,
		Settings:                   settings,
		GitHubUsePersistedSettings: false,
		Discord:                    service.DiscordConnectionStatus{SendResponses: true},
	}
	var webhooks []models.WebhookEndpoint

	if projectID != "" {
		if h.githubSvc != nil {
			if status, err := h.githubSvc.GetConnectionStatus(ctx); err == nil {
				input.GitHub = status
			}
		}
		if h.slackSvc != nil {
			if status, err := h.slackSvc.GetConnectionStatus(ctx); err == nil {
				input.Slack = status
			}
		}
		input.TelegramRunning = h.telegramService != nil && h.telegramService.IsRunning()
		if h.discordSvc != nil {
			if status, err := h.discordSvc.GetConnectionStatus(ctx); err == nil {
				input.Discord = status
			}
		}
		if xService := h.getXService(); xService != nil {
			input.X = xService.Status()
		}
		if h.emailService != nil {
			input.Email = h.emailService.GetConnectionStatus(ctx)
			input.EmailStatusProvided = true
		}

		if h.githubAuthRepo != nil {
			if actors, err := h.githubAuthRepo.ListAuthorizedActors(ctx); err == nil {
				input.AuthorizationCounts.GitHubActors = len(actors)
			}
		}
		if h.slackAuthRepo != nil {
			if count, err := h.slackAuthRepo.CountByProject(ctx, projectID); err == nil {
				input.AuthorizationCounts.SlackUsers = count
			}
		}
		if h.telegramAuthRepo != nil {
			if count, err := h.telegramAuthRepo.CountByProject(ctx, projectID); err == nil {
				input.AuthorizationCounts.TelegramUsers = count
			}
		}
		if h.discordAuthRepo != nil {
			if count, err := h.discordAuthRepo.CountByProject(ctx, projectID); err == nil {
				input.AuthorizationCounts.DiscordUsers = count
			}
		}
		if h.xAuthRepo != nil {
			if count, err := h.xAuthRepo.CountByProject(ctx, projectID); err == nil {
				input.AuthorizationCounts.XUsers = count
			}
		}
		if h.emailAuthRepo != nil {
			if count, err := h.emailAuthRepo.CountByProject(ctx, projectID); err == nil {
				input.AuthorizationCounts.EmailSenders = count
			}
		}
		if h.webhookRepo != nil {
			if listed, err := h.webhookRepo.ListCardsByProject(ctx, projectID); err == nil {
				webhooks = listed
			}
		}
		if h.channelTargetRepo != nil {
			if summary, err := h.channelTargetRepo.SummarizeByProject(ctx, projectID); err == nil {
				input.OutboundTargets = service.OutboundTargetStatusSummaryFromRepoSummary(summary)
			}
		}
	}

	input.Webhooks = webhooks
	snapshot := service.PrepareChannelStatus(input)
	resp := channelStatusToolResponse{
		OK:                     snapshot.OK,
		ProjectID:              snapshot.ProjectID,
		ConfiguredChannelCount: snapshot.ConfiguredChannelCount,
		ConfiguredChannels:     snapshot.ConfiguredChannels,
		NoneConfigured:         snapshot.NoneConfigured,
		GitHub: githubChannelStatusSummary{
			Configured:           snapshot.GitHub.Configured,
			Connected:            snapshot.GitHub.Connected,
			Status:               snapshot.GitHub.Status,
			AuthMode:             snapshot.GitHub.AuthMode,
			AccountLogin:         snapshot.GitHub.AccountLogin,
			AccountType:          snapshot.GitHub.AccountType,
			AppConfigured:        snapshot.GitHub.AppConfigured,
			PATConfigured:        snapshot.GitHub.PATConfigured,
			AuthorizedActorCount: snapshot.GitHub.AuthorizedActorCount,
		},
		Slack: slackChannelStatusSummary{
			Configured:          snapshot.Slack.Configured,
			Connected:           snapshot.Slack.Connected,
			Running:             snapshot.Slack.Running,
			Status:              snapshot.Slack.Status,
			TeamName:            snapshot.Slack.TeamName,
			TeamID:              snapshot.Slack.TeamID,
			BotUserID:           snapshot.Slack.BotUserID,
			BotTokenSource:      snapshot.Slack.BotTokenSource,
			SendResponses:       snapshot.Slack.SendResponses,
			AuthorizedUserCount: snapshot.Slack.AuthorizedUserCount,
		},
		Telegram: telegramChannelStatusSummary{
			Configured:          snapshot.Telegram.Configured,
			Running:             snapshot.Telegram.Running,
			Status:              snapshot.Telegram.Status,
			SendResponses:       snapshot.Telegram.SendResponses,
			RichMessagesV2:      snapshot.Telegram.RichMessagesV2,
			AuthorizedUserCount: snapshot.Telegram.AuthorizedUserCount,
		},
		Discord: discordChannelStatusSummary{
			Configured:          snapshot.Discord.Configured,
			Connected:           snapshot.Discord.Connected,
			Running:             snapshot.Discord.Running,
			Status:              snapshot.Discord.Status,
			BotUserID:           snapshot.Discord.BotUserID,
			SendResponses:       snapshot.Discord.SendResponses,
			LastError:           snapshot.Discord.LastError,
			AuthorizedUserCount: snapshot.Discord.AuthorizedUserCount,
		},
		X: xChannelStatusSummary{
			Configured:          snapshot.X.Configured,
			Connected:           snapshot.X.Connected,
			Running:             snapshot.X.Running,
			Status:              snapshot.X.Status,
			Username:            snapshot.X.Username,
			SendResponses:       snapshot.X.SendResponses,
			AuthorizedUserCount: snapshot.X.AuthorizedUserCount,
			LastError:           snapshot.X.LastError,
		},
		Email: emailChannelStatusSummary{
			Configured:            snapshot.Email.Configured,
			Running:               snapshot.Email.Running,
			Status:                snapshot.Email.Status,
			Provider:              snapshot.Email.Provider,
			Address:               snapshot.Email.Address,
			IMAPHost:              snapshot.Email.IMAPHost,
			IMAPPort:              snapshot.Email.IMAPPort,
			SMTPHost:              snapshot.Email.SMTPHost,
			SMTPPort:              snapshot.Email.SMTPPort,
			SendResponses:         snapshot.Email.SendResponses,
			SkipAttachments:       snapshot.Email.SkipAttachments,
			AuthorizedSenderCount: snapshot.Email.AuthorizedSenderCount,
		},
		Webhooks: webhookStatusSummary{
			Total:       snapshot.Webhooks.Total,
			Active:      snapshot.Webhooks.Active,
			Disabled:    snapshot.Webhooks.Disabled,
			Configured:  snapshot.Webhooks.Configured,
			EndpointIDs: make([]webhookEndpointLabel, 0, len(webhooks)),
		},
		OutboundTargets: outboundTargetsStatusSummary{
			OutboundTargetStatusSummary:   snapshot.OutboundTargets.OutboundTargetStatusSummary,
			ExplicitUnsavedTargetsAllowed: snapshot.OutboundTargets.ExplicitUnsavedTargetsAllowed,
			MessagingAvailable:            snapshot.OutboundTargets.MessagingAvailable,
		},
	}
	for _, webhook := range webhooks {
		resp.Webhooks.EndpointIDs = append(resp.Webhooks.EndpointIDs, webhookEndpointLabel{
			ID: strings.TrimSpace(webhook.ID), Name: strings.TrimSpace(webhook.Name), Enabled: webhook.Enabled,
		})
	}
	return resp
}

func outboundTargetsStatusFromRepoSummary(summary repository.ChannelTargetProjectSummary) outboundTargetsStatusSummary {
	return outboundTargetsStatusSummary{
		OutboundTargetStatusSummary: service.OutboundTargetStatusSummaryFromRepoSummary(summary),
	}
}

func summarizeOutboundTargets(targets []models.ChannelTarget) outboundTargetsStatusSummary {
	return outboundTargetsStatusSummary{
		OutboundTargetStatusSummary: service.OutboundTargetStatusSummaryFromTargets(targets),
	}
}
