package service

import (
	"context"
	"encoding/json"
	"fmt"
	netmail "net/mail"
	"regexp"
	"strconv"
	"strings"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const SendMessageAllowExplicitTargetsSetting = "send_message_allow_explicit_targets"

type channelTargetStore interface {
	ListByProject(ctx context.Context, projectID string) ([]models.ChannelTarget, error)
	FindHome(ctx context.Context, projectID, platform string) (*models.ChannelTarget, error)
	FindByName(ctx context.Context, projectID, platform, name string) (*models.ChannelTarget, error)
	FindByTarget(ctx context.Context, projectID, platform, targetID, threadID string) (*models.ChannelTarget, error)
	FindByTargetAndKind(ctx context.Context, projectID, platform, targetID, threadID, targetKind string) (*models.ChannelTarget, error)
	RecordSend(ctx context.Context, send models.ChannelMessageSend) error
}

type outboundSlackSender interface {
	SendOutboundMessage(ctx context.Context, channelID, threadTS, text string) SendMessageResult
}

type outboundSlackDMSender interface {
	SendOutboundDirectMessage(ctx context.Context, userID, text string) SendMessageResult
}

type outboundTelegramSender interface {
	SendOutboundMessage(ctx context.Context, chatID int64, threadID int, text string) SendMessageResult
}

type outboundEmailSender interface {
	SendOutboundMessage(ctx context.Context, to, subject, body string) SendMessageResult
}

type outboundDiscordSender interface {
	SendOutboundMessage(ctx context.Context, channelID, threadID, text string) SendMessageResult
}

type outboundDiscordDMSender interface {
	SendOutboundDirectMessage(ctx context.Context, userID, text string) SendMessageResult
}

type outboundAuthorizedUserStore interface {
	IsAuthorized(ctx context.Context, projectID, userID string) (bool, error)
}

type outboundAuthorizedUserAnywhereStore interface {
	IsAuthorizedAnywhere(ctx context.Context, userID string) (bool, error)
}

type outboundTelegramAuthorizedUserStore interface {
	IsAuthorized(ctx context.Context, projectID string, telegramUserID int64, username string) (bool, error)
}

type ChannelMessageRouter struct {
	slack        outboundSlackSender
	telegram     outboundTelegramSender
	email        outboundEmailSender
	discord      outboundDiscordSender
	slackAuth    outboundAuthorizedUserStore
	telegramAuth outboundTelegramAuthorizedUserStore
	emailAuth    outboundAuthorizedUserStore
	discordAuth  outboundAuthorizedUserStore
	targets      channelTargetStore
	settings     *repository.SettingsRepo
	newID        func() string
	auditSurface string
	auditUser    string
}

// ChannelTarget is the send_message tool view of a saved outbound target.
type ChannelTarget struct {
	ProjectID      string `json:"project_id"`
	Platform       string `json:"platform"`
	TargetKind     string `json:"target_kind,omitempty"`
	Name           string `json:"name,omitempty"`
	TargetID       string `json:"target_id"`
	ThreadID       string `json:"thread_id,omitempty"`
	Home           bool   `json:"home"`
	DefaultSubject string `json:"default_subject,omitempty"`
}

type SendMessageRequest struct {
	Action  string `json:"action"`
	Target  string `json:"target"`
	Message string `json:"message"`
	Subject string `json:"subject,omitempty"`
}

type SendMessageResult struct {
	OK        bool   `json:"ok"`
	Platform  string `json:"platform,omitempty"`
	Target    string `json:"target,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

func NewChannelMessageRouter(targets channelTargetStore, settings *repository.SettingsRepo) *ChannelMessageRouter {
	return &ChannelMessageRouter{targets: targets, settings: settings, newID: repository.NewID}
}

func (r *ChannelMessageRouter) SetSlackService(svc outboundSlackSender)       { r.slack = svc }
func (r *ChannelMessageRouter) SetTelegramService(svc outboundTelegramSender) { r.telegram = svc }
func (r *ChannelMessageRouter) SetEmailService(svc outboundEmailSender)       { r.email = svc }
func (r *ChannelMessageRouter) SetDiscordService(svc outboundDiscordSender)   { r.discord = svc }
func (r *ChannelMessageRouter) SetSlackAuthStore(store outboundAuthorizedUserStore) {
	r.slackAuth = store
}
func (r *ChannelMessageRouter) SetTelegramAuthStore(store outboundTelegramAuthorizedUserStore) {
	r.telegramAuth = store
}
func (r *ChannelMessageRouter) SetEmailAuthStore(store outboundAuthorizedUserStore) {
	r.emailAuth = store
}
func (r *ChannelMessageRouter) SetDiscordAuthStore(store outboundAuthorizedUserStore) {
	r.discordAuth = store
}
func (r *ChannelMessageRouter) WithAuditContext(surface, user string) *ChannelMessageRouter {
	if r == nil {
		return nil
	}
	copy := *r
	copy.auditSurface = strings.TrimSpace(surface)
	copy.auditUser = strings.TrimSpace(user)
	return &copy
}

func (r *ChannelMessageRouter) ListTargets(ctx context.Context, projectID string) ([]ChannelTarget, error) {
	if r == nil || r.targets == nil {
		return nil, fmt.Errorf("channel message router is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	stored, err := r.targets.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelTarget, 0, len(stored))
	for _, t := range stored {
		out = append(out, ChannelTarget{ProjectID: t.ProjectID, Platform: t.Platform, TargetKind: t.TargetKind, Name: t.Name, TargetID: t.TargetID, ThreadID: t.ThreadID, Home: t.Home, DefaultSubject: t.DefaultSubject})
	}
	return out, nil
}

func (r *ChannelMessageRouter) Send(ctx context.Context, projectID string, req SendMessageRequest) SendMessageResult {
	if r == nil {
		return sendMessageError("channel message router is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return sendMessageError("project id is required")
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "send"
	}
	if action == "list" {
		targets, err := r.ListTargets(ctx, projectID)
		if err != nil {
			return sendMessageError(err.Error())
		}
		b, _ := json.Marshal(map[string]interface{}{"ok": true, "targets": targets})
		return SendMessageResult{OK: true, MessageID: string(b)}
	}
	if action != "send" {
		return r.auditAndReturn(ctx, projectID, "", "", "", "", req.Message, sendMessageError("unsupported send_message action"))
	}
	if strings.TrimSpace(req.Target) == "" {
		return r.auditAndReturn(ctx, projectID, "", "", "", "", req.Message, sendMessageError("send_message requires target; call send_message with action=list to see configured targets"))
	}
	if strings.TrimSpace(req.Message) == "" {
		return r.auditAndReturn(ctx, projectID, "", "", "", "", req.Message, sendMessageError("send_message requires message"))
	}
	resolved, err := r.resolveTarget(ctx, projectID, req.Target)
	if err != nil {
		return r.auditAndReturn(ctx, projectID, "", "", "", "", req.Message, sendMessageError(err.Error()))
	}
	result := r.dispatch(ctx, req, resolved)
	return r.auditAndReturn(ctx, projectID, resolved.Platform, resolved.TargetKind, resolved.TargetID, resolved.ThreadID, req.Message, result)
}

func (r *ChannelMessageRouter) SendDirectTarget(ctx context.Context, projectID string, target ChannelTarget, req SendMessageRequest) SendMessageResult {
	if r == nil {
		return sendMessageError("channel message router is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return sendMessageError("project id is required")
	}
	if strings.TrimSpace(req.Message) == "" {
		return r.auditAndReturn(ctx, projectID, "", "", "", "", req.Message, sendMessageError("send_message requires message"))
	}
	normalized, err := NormalizeOutboundChannelTarget(models.ChannelTarget{
		ProjectID:      target.ProjectID,
		Platform:       target.Platform,
		TargetKind:     target.TargetKind,
		Name:           target.Name,
		TargetID:       target.TargetID,
		ThreadID:       target.ThreadID,
		Home:           target.Home,
		DefaultSubject: target.DefaultSubject,
	})
	if err != nil {
		platform := strings.ToLower(strings.TrimSpace(target.Platform))
		targetKind := strings.ToLower(strings.TrimSpace(target.TargetKind))
		return r.auditAndReturn(ctx, projectID, platform, targetKind, strings.TrimSpace(target.TargetID), strings.TrimSpace(target.ThreadID), req.Message, sendMessageError(err.Error()))
	}
	resolved := fromStoredTarget(normalized)
	result := r.dispatch(ctx, req, resolved)
	return r.auditAndReturn(ctx, projectID, resolved.Platform, resolved.TargetKind, resolved.TargetID, resolved.ThreadID, req.Message, result)
}

func ExecuteSendMessageTool(ctx context.Context, router *ChannelMessageRouter, projectID string, input json.RawMessage) (string, error) {
	var req SendMessageRequest
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	result := router.Send(ctx, projectID, req)
	if reqAction := strings.ToLower(strings.TrimSpace(req.Action)); reqAction == "list" && result.OK && result.MessageID != "" {
		return result.MessageID, nil
	}
	b, _ := json.Marshal(result)
	return string(b), nil
}

type resolvedMessageTarget struct {
	Platform       string
	TargetKind     string
	TargetID       string
	ThreadID       string
	DefaultSubject string
	DirectUser     bool
}

func (r *ChannelMessageRouter) resolveTarget(ctx context.Context, projectID, raw string) (resolvedMessageTarget, error) {
	platform, ref, threadID, err := parseSendMessageTarget(raw)
	if err != nil {
		return resolvedMessageTarget{}, err
	}
	if r.targets == nil {
		return resolvedMessageTarget{}, fmt.Errorf("no outbound channel targets are configured")
	}
	if ref == "" {
		target, err := r.targets.FindHome(ctx, projectID, platform)
		if err != nil {
			return resolvedMessageTarget{}, err
		}
		if target == nil {
			return resolvedMessageTarget{}, fmt.Errorf("No home target configured for %s; call send_message with action=list", platform)
		}
		return fromStoredTarget(*target), nil
	}
	if strings.HasPrefix(ref, "#") {
		name := strings.TrimPrefix(ref, "#")
		target, err := r.targets.FindByName(ctx, projectID, platform, name)
		if err != nil {
			return resolvedMessageTarget{}, err
		}
		if target == nil {
			return resolvedMessageTarget{}, fmt.Errorf("No saved %s target named #%s; call send_message with action=list", platform, name)
		}
		return fromStoredTarget(*target), nil
	}

	// Handle explicit user DM syntax: slack:user:<user_id> and discord:user:<user_id>.
	// Saved user-kind targets are preferred, then authorized users are allowed
	// as direct recipients, then explicit DM policy applies.
	if (platform == "slack" || platform == "discord") && ref == "user" {
		userID := strings.TrimSpace(threadID)
		if userID == "" {
			return resolvedMessageTarget{}, fmt.Errorf("%s user DM target requires %s:user:<user_id>", platform, platform)
		}
		if err := validateOutboundUserDMTarget(platform, userID); err != nil {
			return resolvedMessageTarget{}, err
		}
		// Prefer a saved user-kind target, then allow system-authorized direct users
		// before falling through to the explicit-target policy.
		if saved, err := r.targets.FindByTargetAndKind(ctx, projectID, platform, userID, "", "user"); err != nil {
			return resolvedMessageTarget{}, err
		} else if saved != nil {
			return fromStoredTarget(*saved), nil
		}
		if dmTarget, dmErr := r.resolveAuthorizedDirectUserTarget(ctx, projectID, platform, userID); dmErr == nil {
			return dmTarget, nil
		}
		if !r.allowExplicitTargets(ctx, projectID) {
			return resolvedMessageTarget{}, fmt.Errorf("No saved or authorized %s user DM target for %s; add it in Outbound Message Targets or call send_message with action=list", platform, userID)
		}
		return resolvedMessageTarget{Platform: platform, TargetKind: "user", TargetID: userID, DirectUser: true}, nil
	}

	if platform == "email" {
		normalized, err := NormalizeOutboundEmailForTarget(ref)
		if err != nil {
			return resolvedMessageTarget{}, err
		}
		ref = normalized
	}
	if !isNativeTarget(platform, ref) {
		if isOutboundDirectUserTarget(platform, ref) {
			// Check saved targets first before falling back to the auth store.
			// A saved user-kind target routes as a DM; a saved channel-kind target routes as a channel.
			if saved, err := r.targets.FindByTarget(ctx, projectID, platform, ref, ""); err != nil {
				return resolvedMessageTarget{}, err
			} else if saved != nil {
				return fromStoredTarget(*saved), nil
			}
			dmTarget, dmErr := r.resolveAuthorizedDirectUserTarget(ctx, projectID, platform, ref)
			if dmErr == nil {
				return dmTarget, nil
			}
			if r.isAuthorizedDirectUserTargetAnywhere(ctx, platform, ref) {
				return resolvedMessageTarget{}, dmErr
			}
		}
		return resolvedMessageTarget{}, fmt.Errorf("Invalid %s target %q; call send_message with action=list", platform, ref)
	}
	if err := validateOutboundTelegramThreadID(platform, threadID); err != nil {
		return resolvedMessageTarget{}, err
	}
	if platform == "discord" && ref == "channel" {
		channelID := strings.TrimSpace(threadID)
		if channelID == "" {
			return resolvedMessageTarget{}, fmt.Errorf("discord channel target requires discord:channel:<channel_id>")
		}
		channelThreadID := ""
		if idx := strings.Index(channelID, ":"); idx >= 0 {
			channelThreadID = strings.TrimSpace(channelID[idx+1:])
			channelID = strings.TrimSpace(channelID[:idx])
		}
		if saved, err := r.targets.FindByTarget(ctx, projectID, platform, channelID, channelThreadID); err != nil {
			return resolvedMessageTarget{}, err
		} else if saved != nil {
			return fromStoredTarget(*saved), nil
		}
		if !r.allowExplicitTargets(ctx, projectID) {
			return resolvedMessageTarget{}, fmt.Errorf("Explicit discord channel target is not saved for this project; call send_message with action=list")
		}
		return resolvedMessageTarget{Platform: platform, TargetKind: "channel", TargetID: channelID, ThreadID: channelThreadID}, nil
	}
	if saved, err := r.targets.FindByTarget(ctx, projectID, platform, ref, threadID); err != nil {
		return resolvedMessageTarget{}, err
	} else if saved != nil {
		return fromStoredTarget(*saved), nil
	}
	if isOutboundDirectUserTarget(platform, ref) {
		if threadID == "" {
			dmTarget, dmErr := r.resolveAuthorizedDirectUserTarget(ctx, projectID, platform, ref)
			if dmErr == nil {
				return dmTarget, nil
			}
			if platform == "discord" || r.isAuthorizedDirectUserTargetAnywhere(ctx, platform, ref) {
				return resolvedMessageTarget{}, dmErr
			}
		} else if platform == "discord" {
			return resolvedMessageTarget{}, fmt.Errorf("Bare Discord snowflake targets are ambiguous; save the channel target or use discord:channel:<channel_id>:<thread_id>")
		}
	}
	if platform == "email" && threadID == "" {
		emailTarget, emailErr := r.resolveAuthorizedEmailTarget(ctx, projectID, ref)
		if emailErr == nil {
			return emailTarget, nil
		}
	}
	if platform == "telegram" && threadID == "" {
		telegramTarget, telegramErr := r.resolveAuthorizedTelegramTarget(ctx, projectID, ref)
		if telegramErr == nil {
			return telegramTarget, nil
		}
	}
	if !r.allowExplicitTargets(ctx, projectID) {
		return resolvedMessageTarget{}, fmt.Errorf("Explicit %s target is not saved for this project; call send_message with action=list", platform)
	}
	return resolvedMessageTarget{Platform: platform, TargetKind: models.DefaultChannelTargetKind(platform), TargetID: ref, ThreadID: threadID}, nil
}
func (r *ChannelMessageRouter) dispatch(ctx context.Context, req SendMessageRequest, target resolvedMessageTarget) SendMessageResult {
	switch target.Platform {
	case "slack":
		if r.slack == nil {
			return sendMessageError("slack channel is not configured")
		}
		if target.DirectUser {
			dmSender, ok := r.slack.(outboundSlackDMSender)
			if !ok {
				return sendMessageError("slack direct messages are not supported by this channel service")
			}
			return dmSender.SendOutboundDirectMessage(ctx, target.TargetID, req.Message)
		}
		return r.slack.SendOutboundMessage(ctx, target.TargetID, target.ThreadID, req.Message)
	case "telegram":
		if r.telegram == nil {
			return sendMessageError("telegram channel is not configured")
		}
		chatID, err := strconv.ParseInt(target.TargetID, 10, 64)
		if err != nil {
			return sendMessageError("telegram chat id must be an integer")
		}
		threadID := 0
		if target.ThreadID != "" {
			threadID, err = strconv.Atoi(target.ThreadID)
			if err != nil {
				return sendMessageError("telegram thread id must be an integer")
			}
		}
		return r.telegram.SendOutboundMessage(ctx, chatID, threadID, req.Message)
	case "email":
		if r.email == nil {
			return sendMessageError("email channel is not configured")
		}
		subject := strings.TrimSpace(req.Subject)
		if subject == "" {
			subject = strings.TrimSpace(target.DefaultSubject)
		}
		return r.email.SendOutboundMessage(ctx, target.TargetID, subject, req.Message)
	case "discord":
		if r.discord == nil {
			return sendMessageError("discord channel is not configured")
		}
		if target.DirectUser {
			dmSender, ok := r.discord.(outboundDiscordDMSender)
			if !ok {
				return sendMessageError("discord direct messages are not supported by this channel service")
			}
			return dmSender.SendOutboundDirectMessage(ctx, target.TargetID, req.Message)
		}
		return r.discord.SendOutboundMessage(ctx, target.TargetID, target.ThreadID, req.Message)
	default:
		return sendMessageError("unknown platform")
	}
}

func (r *ChannelMessageRouter) auditAndReturn(ctx context.Context, projectID, platform, targetKind, targetID, threadID, message string, result SendMessageResult) SendMessageResult {
	if result.Platform == "" {
		result.Platform = platform
	}
	if result.Target == "" && platform != "" && targetID != "" {
		result.Target = formatResolvedMessageTarget(platform, targetID, threadID)
	}
	if r.targets == nil {
		return result
	}
	id := repository.NewID()
	if r.newID != nil {
		id = r.newID()
	}
	_ = r.targets.RecordSend(ctx, models.ChannelMessageSend{
		ID:                 id,
		ProjectID:          projectID,
		Platform:           firstNonEmptyMessageString(platform, result.Platform),
		TargetKind:         targetKind,
		TargetID:           targetID,
		ThreadID:           threadID,
		RequestedBySurface: r.auditSurface,
		RequestedByUser:    r.auditUser,
		MessagePreview:     truncateSendMessagePreview(message, 500),
		Success:            result.OK,
		Error:              result.Error,
	})
	return result
}

func parseSendMessageTarget(raw string) (platform, ref, threadID string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", "", fmt.Errorf("send_message target is required")
	}
	parts := strings.Split(raw, ":")
	platform = strings.ToLower(strings.TrimSpace(parts[0]))
	switch platform {
	case "slack", "telegram", "email", "discord":
	default:
		return "", "", "", fmt.Errorf("Unknown send_message platform %q", platform)
	}
	if len(parts) == 1 {
		return platform, "", "", nil
	}
	if platform == "email" {
		return platform, strings.TrimSpace(strings.Join(parts[1:], ":")), "", nil
	}
	ref = strings.TrimSpace(parts[1])
	if len(parts) > 2 {
		threadID = strings.TrimSpace(strings.Join(parts[2:], ":"))
	}
	return platform, ref, threadID, nil
}

func fromStoredTarget(target models.ChannelTarget) resolvedMessageTarget {
	kind := strings.ToLower(strings.TrimSpace(target.TargetKind))
	directUser := kind == "user"
	return resolvedMessageTarget{
		Platform:       strings.ToLower(strings.TrimSpace(target.Platform)),
		TargetKind:     kind,
		TargetID:       strings.TrimSpace(target.TargetID),
		ThreadID:       strings.TrimSpace(target.ThreadID),
		DefaultSubject: strings.TrimSpace(target.DefaultSubject),
		DirectUser:     directUser,
	}
}

var slackNativeTargetPattern = regexp.MustCompile(`^[CGD][A-Z0-9]+$`)
var slackUserTargetPattern = regexp.MustCompile(`^U[A-Z0-9]+$`)
var discordUserTargetPattern = regexp.MustCompile(`^[0-9]+$`)

func isNativeTarget(platform, ref string) bool {
	ref = strings.TrimSpace(ref)
	switch platform {
	case "slack":
		return slackNativeTargetPattern.MatchString(ref)
	case "telegram":
		_, err := strconv.ParseInt(ref, 10, 64)
		return err == nil
	case "email":
		return ref != "" && strings.Contains(ref, "@")
	case "discord":
		return ref != ""
	default:
		return false
	}
}

func isOutboundDirectUserTarget(platform, ref string) bool {
	ref = strings.TrimSpace(ref)
	switch platform {
	case "slack":
		return slackUserTargetPattern.MatchString(ref)
	case "discord":
		return discordUserTargetPattern.MatchString(ref)
	default:
		return false
	}
}

// NormalizeOutboundChannelTarget returns the canonical saved/draft outbound target representation.
func NormalizeOutboundChannelTarget(target models.ChannelTarget) (models.ChannelTarget, error) {
	target.ID = strings.TrimSpace(target.ID)
	target.ProjectID = strings.TrimSpace(target.ProjectID)
	target.Platform = strings.ToLower(strings.TrimSpace(target.Platform))
	target.TargetKind = strings.ToLower(strings.TrimSpace(target.TargetKind))
	target.Name = strings.Trim(strings.ToLower(strings.TrimSpace(target.Name)), "#")
	target.TargetID = strings.TrimSpace(target.TargetID)
	target.ThreadID = strings.TrimSpace(target.ThreadID)
	target.DefaultSubject = strings.TrimSpace(target.DefaultSubject)

	if target.Platform != "slack" && target.Platform != "telegram" && target.Platform != "email" && target.Platform != "discord" {
		return target, fmt.Errorf("Unsupported platform")
	}
	if target.TargetKind == "" {
		target.TargetKind = models.DefaultChannelTargetKind(target.Platform)
	}
	switch target.Platform {
	case "slack", "discord":
		if target.TargetKind != "channel" && target.TargetKind != "user" {
			return target, fmt.Errorf("Invalid target type for %s; expected channel or user", target.Platform)
		}
	case "telegram":
		target.TargetKind = "chat"
	case "email":
		target.TargetKind = "email"
	}
	if target.TargetID == "" {
		return target, fmt.Errorf("Target ID is required")
	}
	if target.TargetKind == "user" {
		if err := validateOutboundUserDMTarget(target.Platform, target.TargetID); err != nil {
			return target, err
		}
		return target, nil
	}
	if target.Platform == "email" {
		normalized, err := NormalizeOutboundEmailForTarget(target.TargetID)
		if err != nil {
			return target, err
		}
		target.TargetID = normalized
	}
	if !isNativeTarget(target.Platform, target.TargetID) {
		return target, fmt.Errorf("Invalid %s target %q", target.Platform, target.TargetID)
	}
	if err := validateOutboundTelegramThreadID(target.Platform, target.ThreadID); err != nil {
		return target, err
	}
	return target, nil
}

func validateOutboundUserDMTarget(platform, targetID string) error {
	switch platform {
	case "slack":
		if !slackUserTargetPattern.MatchString(strings.TrimSpace(targetID)) {
			return fmt.Errorf("Invalid Slack user ID %q; expected format U...", strings.TrimSpace(targetID))
		}
	case "discord":
		if !discordUserTargetPattern.MatchString(strings.TrimSpace(targetID)) {
			return fmt.Errorf("Invalid Discord user ID %q; expected a numeric snowflake", strings.TrimSpace(targetID))
		}
	default:
		return fmt.Errorf("Invalid target type for %s; expected channel or user", platform)
	}
	return nil
}

func validateOutboundTelegramThreadID(platform, threadID string) error {
	if platform != "telegram" || strings.TrimSpace(threadID) == "" {
		return nil
	}
	if _, err := strconv.Atoi(strings.TrimSpace(threadID)); err != nil {
		return fmt.Errorf("telegram thread id must be an integer")
	}
	return nil
}

func (r *ChannelMessageRouter) resolveAuthorizedDirectUserTarget(ctx context.Context, projectID, platform, userID string) (resolvedMessageTarget, error) {
	userID = strings.TrimSpace(userID)
	store := r.authorizedUserStore(platform)
	if store == nil {
		return resolvedMessageTarget{}, fmt.Errorf("%s authorized-user store is not configured", platform)
	}
	allowed, err := store.IsAuthorized(ctx, projectID, userID)
	if err != nil {
		return resolvedMessageTarget{}, err
	}
	if !allowed {
		return resolvedMessageTarget{}, fmt.Errorf("%s user is not authorized for outbound direct messages", platform)
	}
	return resolvedMessageTarget{Platform: platform, TargetKind: "user", TargetID: userID, DirectUser: true}, nil
}

func (r *ChannelMessageRouter) resolveAuthorizedEmailTarget(ctx context.Context, projectID, emailAddress string) (resolvedMessageTarget, error) {
	store := r.authorizedUserStore("email")
	if store == nil {
		return resolvedMessageTarget{}, fmt.Errorf("email authorized-sender store is not configured")
	}
	normalized, err := NormalizeOutboundEmailForTarget(emailAddress)
	if err != nil {
		return resolvedMessageTarget{}, err
	}
	allowed, err := store.IsAuthorized(ctx, projectID, normalized)
	if err != nil {
		return resolvedMessageTarget{}, err
	}
	if !allowed {
		return resolvedMessageTarget{}, fmt.Errorf("email recipient is not an authorized sender")
	}
	return resolvedMessageTarget{Platform: "email", TargetKind: "email", TargetID: normalized}, nil
}

func (r *ChannelMessageRouter) resolveAuthorizedTelegramTarget(ctx context.Context, projectID, ref string) (resolvedMessageTarget, error) {
	if r.telegramAuth == nil {
		return resolvedMessageTarget{}, fmt.Errorf("telegram authorized-user store is not configured")
	}
	userID, err := strconv.ParseInt(strings.TrimSpace(ref), 10, 64)
	if err != nil || userID <= 0 {
		return resolvedMessageTarget{}, fmt.Errorf("telegram authorized direct recipients require a positive numeric user id")
	}
	allowed, err := r.telegramAuth.IsAuthorized(ctx, projectID, userID, "")
	if err != nil {
		return resolvedMessageTarget{}, err
	}
	if !allowed {
		return resolvedMessageTarget{}, fmt.Errorf("telegram user is not authorized for outbound direct messages")
	}
	return resolvedMessageTarget{Platform: "telegram", TargetKind: "chat", TargetID: strconv.FormatInt(userID, 10)}, nil
}

func (r *ChannelMessageRouter) authorizedUserStore(platform string) outboundAuthorizedUserStore {
	switch platform {
	case "slack":
		return r.slackAuth
	case "email":
		return r.emailAuth
	case "discord":
		return r.discordAuth
	default:
		return nil
	}
}

func (r *ChannelMessageRouter) isAuthorizedDirectUserTargetAnywhere(ctx context.Context, platform, userID string) bool {
	store, ok := r.authorizedUserStore(platform).(outboundAuthorizedUserAnywhereStore)
	if !ok || store == nil {
		return false
	}
	allowed, err := store.IsAuthorizedAnywhere(ctx, strings.TrimSpace(userID))
	return err == nil && allowed
}

func NormalizeOutboundEmailForTarget(email string) (string, error) {
	addr, err := netmail.ParseAddress(strings.TrimSpace(email))
	if err != nil || addr == nil || strings.TrimSpace(addr.Address) == "" {
		return "", fmt.Errorf("invalid email recipient")
	}
	return repository.NormalizeEmailAddress(addr.Address), nil
}

func (r *ChannelMessageRouter) allowExplicitTargets(ctx context.Context, projectID string) bool {
	if r.settings == nil {
		return false
	}
	if strings.TrimSpace(projectID) != "" {
		val, _ := r.settings.Get(ctx, SendMessageAllowExplicitTargetsSetting+":"+strings.TrimSpace(projectID))
		if strings.TrimSpace(val) != "" {
			return strings.EqualFold(strings.TrimSpace(val), "true")
		}
	}
	val, _ := r.settings.Get(ctx, SendMessageAllowExplicitTargetsSetting)
	return strings.EqualFold(strings.TrimSpace(val), "true")
}

func formatResolvedMessageTarget(platform, targetID, threadID string) string {
	if threadID != "" {
		return platform + ":" + targetID + ":" + threadID
	}
	return platform + ":" + targetID
}

func sendMessageError(msg string) SendMessageResult {
	return SendMessageResult{OK: false, Error: strings.TrimSpace(msg)}
}

func truncateSendMessagePreview(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}

func firstNonEmptyMessageString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
