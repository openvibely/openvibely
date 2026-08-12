package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

var newTelegramService = func(
	token string,
	taskSvc *service.TaskService,
	projectRepo *repository.ProjectRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	taskRepo *repository.TaskRepo,
	execRepo *repository.ExecutionRepo,
	scheduleRepo *repository.ScheduleRepo,
	chatAttachmentRepo *repository.ChatAttachmentRepo,
	llmSvc *service.LLMService,
	workerSvc *service.WorkerService,
) (*service.TelegramService, error) {
	return service.NewTelegramService(token, taskSvc, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, chatAttachmentRepo, llmSvc, workerSvc)
}

var updateTelegramServiceToken = func(svc *service.TelegramService, token string) error {
	return svc.UpdateToken(token)
}

var isTelegramServiceRunning = func(svc *service.TelegramService) bool {
	return svc != nil && svc.IsRunning()
}

const channelsRefreshTrigger = "channels-refresh"

type channelSettingReset struct {
	key   string
	value string
}

func applyChannelSettingResets(ctx context.Context, settingsRepo *repository.SettingsRepo, resets []channelSettingReset) error {
	for _, reset := range resets {
		if err := settingsRepo.Set(ctx, reset.key, reset.value); err != nil {
			return fmt.Errorf("reset channel setting %q: %w", reset.key, err)
		}
	}
	return nil
}

func triggerChannelsRefresh(c echo.Context) error {
	c.Response().Header().Set("HX-Trigger", channelsRefreshTrigger)
	return c.NoContent(http.StatusOK)
}

func returnToChannels(c echo.Context) error {
	if isHTMX(c) {
		return triggerChannelsRefresh(c)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

// handleChannels renders the channels (integrations) page
func (h *Handler) handleChannels(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	// Get projects for sidebar
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load projects")
	}

	resolvedProjectID := projectID
	if id, err := h.getCurrentProjectID(c); err == nil && id != "" {
		resolvedProjectID = id
	} else if resolvedProjectID == "" && len(projects) > 0 {
		resolvedProjectID = projects[0].ID
	}

	// Get current Telegram bot token and status
	var token string
	if h.settingsRepo != nil {
		token, _ = h.settingsRepo.Get(c.Request().Context(), service.TelegramSettingBotToken)
	}
	isBotRunning := h.telegramService != nil && h.telegramService.IsRunning()
	hasTelegramChannel := strings.TrimSpace(token) != "" || isBotRunning

	// Load system-level inbound channel authorization allowlists.
	var authorizedUsers []models.TelegramAuthorizedUser
	if h.telegramAuthRepo != nil {
		authorizedUsers, _ = h.telegramAuthRepo.ListByProject(c.Request().Context(), resolvedProjectID)
	}

	var slackAuthorizedUsers []models.SlackAuthorizedUser
	if h.slackAuthRepo != nil {
		slackAuthorizedUsers, _ = h.slackAuthRepo.ListByProject(c.Request().Context(), resolvedProjectID)
	}

	var emailAuthorizedSenders []models.EmailAuthorizedSender
	if h.emailAuthRepo != nil {
		emailAuthorizedSenders, _ = h.emailAuthRepo.ListByProject(c.Request().Context(), resolvedProjectID)
	}

	var discordAuthorizedUsers []models.DiscordAuthorizedUser
	if h.discordAuthRepo != nil {
		discordAuthorizedUsers, _ = h.discordAuthRepo.ListByProject(c.Request().Context(), resolvedProjectID)
	}

	// Load Telegram settings (default: enabled)
	sendResponses := true
	richMessagesV2 := true
	if h.settingsRepo != nil {
		val, _ := h.settingsRepo.Get(c.Request().Context(), service.TelegramSettingSendResponses)
		if strings.EqualFold(strings.TrimSpace(val), "false") {
			sendResponses = false
		}
		val, _ = h.settingsRepo.Get(c.Request().Context(), service.TelegramSettingRichMessagesV2)
		if strings.EqualFold(strings.TrimSpace(val), "false") {
			richMessagesV2 = false
		}
	}

	var githubStatus service.GitHubConnectionStatus
	githubAuthMode := service.GitHubAuthModePAT
	githubAppID := ""
	githubAppSlug := ""
	githubPrivateKeyValue := ""
	githubPATValue := ""
	githubAPIEndpoint := ""
	githubHasPrivateKey := false
	githubHasPAT := false
	githubModeSetting := ""
	slackStatus := service.SlackConnectionStatus{}
	slackClientID := ""
	slackClientSecret := ""
	slackAppToken := ""
	slackBotToken := ""
	slackBotTokenMode := service.SlackBotTokenSourceOAuth
	slackHasClientID := false
	slackHasClientSecret := false
	slackHasAppToken := false
	slackHasBotToken := false
	slackHasOAuthBotToken := false
	slackSendResponses := true
	discordStatus := service.DiscordConnectionStatus{SendResponses: true}
	discordBotToken := ""
	discordSendResponses := true
	emailStatus := service.EmailConnectionStatus{Provider: service.EmailProviderCustom, IMAPPort: 993, SMTPPort: 587}
	emailPasswordValue := ""
	emailHasPassword := false
	emailSendResponses := true
	emailSkipAttachments := false
	emailMarkExistingSeenOnStart := true
	emailPollIntervalSeconds := "15"
	if h.githubSvc != nil {
		githubStatus, _ = h.githubSvc.GetConnectionStatus(c.Request().Context())
		if githubStatus.AuthMode != "" {
			githubAuthMode = service.NormalizeGitHubAuthMode(githubStatus.AuthMode)
		}
		githubHasPAT = githubStatus.HasPAT
	}
	if h.settingsRepo != nil {
		githubModeSetting, _ = h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingAuthMode)
		githubAppID, _ = h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingAppID)
		githubAppSlug, _ = h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingAppSlug)
		githubPrivateKeyValue, _ = h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingAppPrivateKey)
		githubHasPrivateKey = strings.TrimSpace(githubPrivateKeyValue) != ""
		githubPATValue, _ = h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingPAT)
		if strings.TrimSpace(githubPATValue) != "" {
			githubHasPAT = true
		}
		githubAPIEndpoint, _ = h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingAPIEndpoint)
		if strings.TrimSpace(githubModeSetting) != "" {
			githubAuthMode = service.NormalizeGitHubAuthMode(githubModeSetting)
		}
	}
	if h.slackSvc != nil {
		slackStatus, _ = h.slackSvc.GetConnectionStatus(c.Request().Context())
	}
	if h.discordSvc != nil {
		discordStatus, _ = h.discordSvc.GetConnectionStatus(c.Request().Context())
	}
	if h.settingsRepo != nil {
		slackClientID, _ = h.settingsRepo.Get(c.Request().Context(), service.SlackSettingClientID)
		slackClientSecret, _ = h.settingsRepo.Get(c.Request().Context(), service.SlackSettingClientSecret)
		slackAppToken, _ = h.settingsRepo.Get(c.Request().Context(), service.SlackSettingAppToken)
		slackBotToken, _ = h.settingsRepo.Get(c.Request().Context(), service.SlackSettingBotTokenOverride)
		slackBotTokenMode, _ = h.settingsRepo.Get(c.Request().Context(), service.SlackSettingBotTokenSource)
		slackBotTokenMode = strings.TrimSpace(strings.ToLower(slackBotTokenMode))
		if slackBotTokenMode != service.SlackBotTokenSourceManual {
			slackBotTokenMode = service.SlackBotTokenSourceOAuth
		}
		oauthBotToken, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingBotToken)
		slackHasOAuthBotToken = strings.TrimSpace(oauthBotToken) != ""
		if val, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingSendResponses); strings.TrimSpace(strings.ToLower(val)) == "false" {
			slackSendResponses = false
		}
		slackHasClientID = strings.TrimSpace(slackClientID) != ""
		slackHasClientSecret = strings.TrimSpace(slackClientSecret) != ""
		slackHasAppToken = strings.TrimSpace(slackAppToken) != ""
		slackHasBotToken = strings.TrimSpace(slackBotToken) != ""

		discordBotToken, _ = h.settingsRepo.Get(c.Request().Context(), service.DiscordSettingBotToken)
		if val, _ := h.settingsRepo.Get(c.Request().Context(), service.DiscordSettingSendResponses); strings.TrimSpace(strings.ToLower(val)) == "false" {
			discordSendResponses = false
		}

		emailProvider, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingProvider)
		emailAddress, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingAddress)
		emailPasswordValue, _ = h.settingsRepo.Get(c.Request().Context(), service.EmailSettingPassword)
		emailHasPassword = strings.TrimSpace(emailPasswordValue) != ""
		emailIMAPHost, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingIMAPHost)
		emailIMAPPort, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingIMAPPort)
		emailSMTPHost, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingSMTPHost)
		emailSMTPPort, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingSMTPPort)
		emailPollIntervalSeconds, _ = h.settingsRepo.Get(c.Request().Context(), service.EmailSettingPollIntervalSeconds)
		if strings.TrimSpace(emailPollIntervalSeconds) == "" {
			emailPollIntervalSeconds = "15"
		}
		if val, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingSendResponses); strings.TrimSpace(strings.ToLower(val)) == "false" {
			emailSendResponses = false
		}
		if val, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingSkipAttachments); strings.TrimSpace(strings.ToLower(val)) == "true" {
			emailSkipAttachments = true
		}
		if val, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingMarkExistingSeenOnStart); strings.TrimSpace(strings.ToLower(val)) == "false" {
			emailMarkExistingSeenOnStart = false
		}
		emailStatus.Provider = service.NormalizeEmailProvider(emailProvider)
		emailStatus.Address = strings.TrimSpace(emailAddress)
		emailStatus.IMAPHost = strings.TrimSpace(emailIMAPHost)
		emailStatus.SMTPHost = strings.TrimSpace(emailSMTPHost)
		if port, err := strconv.Atoi(strings.TrimSpace(emailIMAPPort)); err == nil && port > 0 {
			emailStatus.IMAPPort = port
		}
		if port, err := strconv.Atoi(strings.TrimSpace(emailSMTPPort)); err == nil && port > 0 {
			emailStatus.SMTPPort = port
		}
	}
	if h.emailService != nil {
		runtimeStatus := h.emailService.GetConnectionStatus(c.Request().Context())
		if runtimeStatus.Address != "" || runtimeStatus.Configured || runtimeStatus.Running {
			emailStatus = runtimeStatus
		}
	}
	hasGitHubChannel := githubStatus.Configured || githubStatus.Connected ||
		strings.TrimSpace(githubModeSetting) != "" ||
		githubHasPAT ||
		strings.TrimSpace(githubAppID) != "" ||
		strings.TrimSpace(githubAppSlug) != "" ||
		githubHasPrivateKey
	hasSlackChannel := slackStatus.Configured || slackStatus.Connected ||
		slackHasClientID || slackHasClientSecret || slackHasAppToken || slackHasBotToken || slackHasOAuthBotToken
	hasEmailChannel := emailStatus.Configured || emailStatus.Running || strings.TrimSpace(emailStatus.Address) != "" || strings.TrimSpace(emailStatus.IMAPHost) != "" || strings.TrimSpace(emailStatus.SMTPHost) != "" || emailHasPassword
	hasDiscordChannel := discordStatus.Configured || discordStatus.Connected || strings.TrimSpace(discordBotToken) != ""

	var channelTargets []models.ChannelTarget
	sendMessageExplicitTargets := false
	if resolvedProjectID != "" && h.channelTargetRepo != nil {
		channelTargets, _ = h.channelTargetRepo.ListByProject(c.Request().Context(), resolvedProjectID)
	}
	if resolvedProjectID != "" && h.settingsRepo != nil {
		if val, _ := h.settingsRepo.Get(c.Request().Context(), service.SendMessageAllowExplicitTargetsSetting+":"+resolvedProjectID); strings.TrimSpace(val) != "" {
			sendMessageExplicitTargets = strings.EqualFold(strings.TrimSpace(val), "true")
		}
	}

	// Load webhooks for current project
	var webhooks []models.WebhookEndpoint
	if resolvedProjectID != "" && h.webhookRepo != nil {
		webhooks, _ = h.webhookRepo.ListCardsByProject(c.Request().Context(), resolvedProjectID)
	}

	// Load agents for webhook agent selection
	var agents []models.Agent
	if h.agentRepo != nil {
		agents, _ = h.agentRepo.List(c.Request().Context())
	}

	webhookAgents := map[string][]models.WebhookEndpointAgent{}

	channelView := pages.ChannelsSettingsView{
		TelegramToken:                token,
		IsBotRunning:                 isBotRunning,
		AuthorizedUsers:              authorizedUsers,
		SlackAuthorizedUsers:         slackAuthorizedUsers,
		DiscordAuthorizedUsers:       discordAuthorizedUsers,
		CurrentProjectID:             resolvedProjectID,
		SendResponses:                sendResponses,
		RichMessagesV2:               richMessagesV2,
		GitHubStatus:                 githubStatus,
		GitHubAuthMode:               githubAuthMode,
		GitHubAppID:                  githubAppID,
		GitHubAppSlug:                githubAppSlug,
		GitHubPrivateKeyValue:        githubPrivateKeyValue,
		GitHubPATValue:               githubPATValue,
		GitHubAPIEndpoint:            githubAPIEndpoint,
		GitHubHasPrivateKey:          githubHasPrivateKey,
		GitHubHasPAT:                 githubHasPAT,
		SlackStatus:                  slackStatus,
		SlackClientID:                slackClientID,
		SlackClientSecret:            slackClientSecret,
		SlackAppToken:                slackAppToken,
		SlackBotToken:                slackBotToken,
		SlackBotTokenMode:            slackBotTokenMode,
		SlackHasClientID:             slackHasClientID,
		SlackHasClientSecret:         slackHasClientSecret,
		SlackHasAppToken:             slackHasAppToken,
		SlackHasBotToken:             slackHasBotToken,
		SlackSendResponses:           slackSendResponses,
		DiscordStatus:                discordStatus,
		DiscordBotToken:              discordBotToken,
		DiscordSendResponses:         discordSendResponses,
		EmailStatus:                  emailStatus,
		EmailAuthorizedSenders:       emailAuthorizedSenders,
		EmailPasswordValue:           emailPasswordValue,
		EmailSendResponses:           emailSendResponses,
		EmailSkipAttachments:         emailSkipAttachments,
		EmailMarkExistingSeenOnStart: emailMarkExistingSeenOnStart,
		EmailPollIntervalSeconds:     emailPollIntervalSeconds,
		HasTelegramChannel:           hasTelegramChannel,
		HasGitHubChannel:             hasGitHubChannel,
		HasSlackChannel:              hasSlackChannel,
		HasDiscordChannel:            hasDiscordChannel,
		HasEmailChannel:              hasEmailChannel,
		Webhooks:                     webhooks,
		Agents:                       agents,
		WebhookAgents:                webhookAgents,
		ChannelTargets:               channelTargets,
		SendMessageExplicitTargets:   sendMessageExplicitTargets,
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.SettingsContent(channelView))
	}
	return render(c, http.StatusOK, pages.SettingsPage(projects, channelView))
}

// handleAppSettings renders the application settings page (personality, etc.)
func (h *Handler) handleAppSettings(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	// Get projects for sidebar
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load projects")
	}

	// Load global personality setting
	var personality string
	if h.settingsRepo != nil {
		personality, _ = h.settingsRepo.Get(c.Request().Context(), "personality")
	}

	// Load custom personalities
	var customPersonalities []models.CustomPersonality
	if h.customPersonalityRepo != nil {
		customPersonalities, _ = h.customPersonalityRepo.List(c.Request().Context())
	}

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.AppSettingsContent(personality, projectID, customPersonalities))
	}
	return render(c, http.StatusOK, pages.AppSettingsPage(personality, projects, projectID, customPersonalities))
}

// handleTelegramSave saves the Telegram bot token and starts the bot
func (h *Handler) handleTelegramSave(c echo.Context) error {
	token := c.FormValue("token")

	// Save token to database
	if h.settingsRepo != nil {
		if err := h.settingsRepo.Set(c.Request().Context(), service.TelegramSettingBotToken, token); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save token")
		}
		richValue := "false"
		if c.FormValue("telegram_rich_messages_v2") == "true" {
			richValue = "true"
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.TelegramSettingRichMessagesV2, richValue); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save rich message setting")
		}
	}

	// Create or update telegram service
	if h.telegramService != nil {
		if err := updateTelegramServiceToken(h.telegramService, token); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to start Telegram bot: "+err.Error())
		}
	} else {
		// Create a new TelegramService on the fly
		svc, err := newTelegramService(token, h.taskSvc, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.chatAttachmentRepo, h.llmSvc, h.workerSvc)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to start Telegram bot: "+err.Error())
		}
		svc.SetTelegramAuthRepo(h.telegramAuthRepo)
		if h.agentRepo != nil {
			svc.SetAgentRepo(h.agentRepo)
		}
		if h.settingsRepo != nil {
			svc.SetSettingsRepo(h.settingsRepo)
		}
		if h.customPersonalityRepo != nil {
			svc.SetCustomPersonalityRepo(h.customPersonalityRepo)
		}
		if h.chatBroadcaster != nil {
			svc.SetChatBroadcaster(h.chatBroadcaster)
		}
		if h.threadInputRepo != nil {
			svc.SetThreadInputRepo(h.threadInputRepo)
		}
		svc.SetQueuedTurnPromoter(h.PromoteQueuedChatInput)
		svc.SetQueuedTaskThreadPromoter(h.PromoteQueuedTaskThreadInput)
		svc.SetChannelChatRunner(h.StartChannelChatRun)
		svc.SetChannelTaskRunner(h.StartChannelTaskRun)
		if h.channelMessageRouter != nil {
			svc.SetChannelMessageRouter(h.channelMessageRouter)
			h.channelMessageRouter.SetTelegramService(svc)
		}
		svc.Start()
		h.telegramService = svc
	}

	return returnToChannels(c)
}

// handleTelegramTest tests the Telegram bot connection
func (h *Handler) handleTelegramTest(c echo.Context) error {
	options := channelConnectionTestFeedbackOptions{
		ElementID:              "telegram-test-feedback",
		MissingServiceMessage:  "Connection failed: Bot is not running",
		AutoDismissOnSuccess:   true,
		AutoDismissElementID:   "telegram-test-feedback",
		AutoDismissDelayMillis: 3000,
	}
	if !isTelegramServiceRunning(h.telegramService) {
		return renderStandardChannelConnectionTestFeedbackWithOptions(c, "Telegram", false, nil, options)
	}
	return renderStandardChannelConnectionTestFeedbackWithOptions(c, "Telegram", true, nil, options)
}

// handleTelegramSendResponses toggles the "send task responses to Telegram" setting
func (h *Handler) handleTelegramSendResponses(c echo.Context) error {
	// Checkbox sends "true" when checked, nothing when unchecked
	enabled := c.FormValue("enabled")
	value := "false"
	if enabled == "true" {
		value = "true"
	}

	if h.settingsRepo != nil {
		if err := h.settingsRepo.Set(c.Request().Context(), service.TelegramSettingSendResponses, value); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save setting")
		}
	}

	return c.String(http.StatusOK, "Setting saved")
}

func (h *Handler) handleTelegramRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	if h.telegramService != nil && h.telegramService.IsRunning() {
		h.telegramService.Stop()
	}
	if err := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: service.TelegramSettingBotToken, value: ""},
		{key: service.TelegramSettingSendResponses, value: ""},
		{key: service.TelegramSettingRichMessagesV2, value: ""},
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(err)
	}

	return returnToChannels(c)
}

func (h *Handler) handleGitHubConnect(c echo.Context) error {
	if h.githubSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "GitHub integration is not configured")
	}
	connectURL, err := h.githubSvc.ConnectURL(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.Redirect(http.StatusTemporaryRedirect, connectURL)
}

func (h *Handler) handleGitHubConfigure(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}

	authMode := service.NormalizeGitHubAuthMode(c.FormValue("github_auth_mode"))
	appID := strings.TrimSpace(c.FormValue("github_app_id"))
	appSlug := strings.TrimSpace(c.FormValue("github_app_slug"))
	privateKey := strings.TrimSpace(c.FormValue("github_app_private_key"))
	pat := strings.TrimSpace(c.FormValue("github_pat"))
	apiEndpoint := strings.TrimSpace(c.FormValue("github_api_endpoint"))

	if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAPIEndpoint, apiEndpoint); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub API endpoint")
	}

	if strings.TrimSpace(c.FormValue("github_auth_mode")) == "" && (appID != "" || appSlug != "" || privateKey != "") {
		authMode = service.GitHubAuthModeApp
	}

	if authMode == service.GitHubAuthModePAT {
		if pat == "" {
			existingPAT, _ := h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingPAT)
			if strings.TrimSpace(existingPAT) == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "GitHub personal access token is required")
			}
			pat = strings.TrimSpace(existingPAT)
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAuthMode, service.GitHubAuthModePAT); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub auth mode")
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingPAT, pat); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub token")
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingPATUserLogin, ""); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to update GitHub token metadata")
		}
	} else {
		if appID == "" || appSlug == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "GitHub App ID and slug are required")
		}
		if privateKey == "" {
			existingPrivateKey, _ := h.settingsRepo.Get(c.Request().Context(), service.GitHubSettingAppPrivateKey)
			if strings.TrimSpace(existingPrivateKey) == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "GitHub App private key is required")
			}
			privateKey = existingPrivateKey
		}

		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAppID, appID); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub App ID")
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAppSlug, appSlug); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub App slug")
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAppPrivateKey, privateKey); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub App private key")
		}
		if err := h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAuthMode, service.GitHubAuthModeApp); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save GitHub auth mode")
		}
		if h.githubSvc != nil {
			_ = h.githubSvc.Disconnect(c.Request().Context())
		}
	}

	return returnToChannels(c)
}

func (h *Handler) handleGitHubCallback(c echo.Context) error {
	if h.githubSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "GitHub integration is not configured")
	}
	installationID := c.QueryParam("installation_id")
	if err := h.githubSvc.HandleInstallCallback(c.Request().Context(), installationID); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

func (h *Handler) handleGitHubDisconnect(c echo.Context) error {
	if h.githubSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "GitHub integration is not configured")
	}
	if err := h.githubSvc.Disconnect(c.Request().Context()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to disconnect GitHub")
	}
	return returnToChannels(c)
}

func (h *Handler) handleGitHubRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	if h.githubSvc != nil {
		_ = h.githubSvc.Disconnect(c.Request().Context())
	}
	if err := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: service.GitHubSettingAppID, value: ""},
		{key: service.GitHubSettingAppSlug, value: ""},
		{key: service.GitHubSettingAppPrivateKey, value: ""},
		{key: service.GitHubSettingPAT, value: ""},
		{key: service.GitHubSettingPATUserLogin, value: ""},
		{key: service.GitHubSettingAuthMode, value: ""},
		{key: service.GitHubSettingAPIEndpoint, value: ""},
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(err)
	}

	return returnToChannels(c)
}

func (h *Handler) handleSlackConfigure(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}

	clientID := strings.TrimSpace(c.FormValue("slack_client_id"))
	clientSecret := strings.TrimSpace(c.FormValue("slack_client_secret"))
	appToken := strings.TrimSpace(c.FormValue("slack_app_token"))
	botToken := strings.TrimSpace(c.FormValue("slack_bot_token"))
	botTokenMode := strings.TrimSpace(strings.ToLower(c.FormValue("slack_bot_token_mode")))
	sendResponses := strings.TrimSpace(strings.ToLower(c.FormValue("slack_send_responses")))

	if clientID == "" {
		existing, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingClientID)
		clientID = strings.TrimSpace(existing)
	}
	if clientSecret == "" {
		existing, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingClientSecret)
		clientSecret = strings.TrimSpace(existing)
	}
	if appToken == "" {
		existing, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingAppToken)
		appToken = strings.TrimSpace(existing)
	}
	if botTokenMode != service.SlackBotTokenSourceManual {
		botTokenMode = service.SlackBotTokenSourceOAuth
	}

	existingOverrideToken, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingBotTokenOverride)
	existingOverrideToken = strings.TrimSpace(existingOverrideToken)
	if botTokenMode == service.SlackBotTokenSourceManual && botToken == "" {
		botToken = existingOverrideToken
	}

	if clientID == "" || clientSecret == "" || appToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack client ID, client secret, and app token are required")
	}
	if botTokenMode == service.SlackBotTokenSourceManual && botToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack bot token is required when manual override mode is selected")
	}

	if err := h.settingsRepo.Set(c.Request().Context(), service.SlackSettingClientID, clientID); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Slack client ID")
	}
	if err := h.settingsRepo.Set(c.Request().Context(), service.SlackSettingClientSecret, clientSecret); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Slack client secret")
	}
	if err := h.settingsRepo.Set(c.Request().Context(), service.SlackSettingAppToken, appToken); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Slack app token")
	}
	if err := h.settingsRepo.Set(c.Request().Context(), service.SlackSettingBotTokenSource, botTokenMode); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Slack bot token mode")
	}
	if botTokenMode == service.SlackBotTokenSourceManual {
		if err := h.settingsRepo.Set(c.Request().Context(), service.SlackSettingBotTokenOverride, botToken); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Slack bot token override")
		}
	}

	if sendResponses == "true" || sendResponses == "false" {
		_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingSendResponses, sendResponses)
	} else if strings.TrimSpace(sendResponses) == "" {
		current, _ := h.settingsRepo.Get(c.Request().Context(), service.SlackSettingSendResponses)
		if strings.TrimSpace(current) == "" {
			_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingSendResponses, "true")
		}
	}

	if h.slackSvc != nil {
		_ = h.slackSvc.ReloadFromSettings(c.Request().Context())
	}

	return returnToChannels(c)
}

func (h *Handler) handleSlackConnect(c echo.Context) error {
	if h.slackSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack integration is not configured")
	}
	redirectURI := h.buildAbsoluteURL(c, "/channels/slack/callback")
	connectURL, err := h.slackSvc.ConnectURL(c.Request().Context(), redirectURI)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.Redirect(http.StatusTemporaryRedirect, connectURL)
}

func (h *Handler) handleSlackCallback(c echo.Context) error {
	if h.slackSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack integration is not configured")
	}
	redirectURI := h.buildAbsoluteURL(c, "/channels/slack/callback")
	code := c.QueryParam("code")
	state := c.QueryParam("state")
	if err := h.slackSvc.HandleOAuthCallback(c.Request().Context(), code, state, redirectURI); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

func (h *Handler) handleSlackDisconnect(c echo.Context) error {
	if h.slackSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack integration is not configured")
	}
	if err := h.slackSvc.Disconnect(c.Request().Context()); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to disconnect Slack")
	}
	return returnToChannels(c)
}

func (h *Handler) handleSlackRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	if h.slackSvc != nil {
		_ = h.slackSvc.Disconnect(c.Request().Context())
	}
	if err := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: service.SlackSettingClientID, value: ""},
		{key: service.SlackSettingClientSecret, value: ""},
		{key: service.SlackSettingAppToken, value: ""},
		{key: service.SlackSettingBotToken, value: ""},
		{key: service.SlackSettingBotTokenOverride, value: ""},
		{key: service.SlackSettingBotTokenSource, value: service.SlackBotTokenSourceOAuth},
		{key: service.SlackSettingBotUserID, value: ""},
		{key: service.SlackSettingTeamID, value: ""},
		{key: service.SlackSettingTeamName, value: ""},
		{key: service.SlackSettingConnectedAt, value: ""},
		{key: service.SlackSettingOAuthState, value: ""},
		{key: service.SlackSettingSendResponses, value: ""},
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(err)
	}

	return returnToChannels(c)
}

func (h *Handler) handleSlackTest(c echo.Context) error {
	if h.slackSvc == nil {
		return renderStandardChannelConnectionTestFeedback(c, "Slack", false, nil)
	}
	return renderStandardChannelConnectionTestFeedback(c, "Slack", true, h.slackSvc.TestConnection(c.Request().Context()))
}

func (h *Handler) handleDiscordConfigure(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	botToken := strings.TrimSpace(c.FormValue("discord_bot_token"))
	sendResponses := strings.TrimSpace(strings.ToLower(c.FormValue("discord_send_responses")))
	if botToken == "" {
		existing, _ := h.settingsRepo.Get(c.Request().Context(), service.DiscordSettingBotToken)
		botToken = strings.TrimSpace(existing)
	}
	if botToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Discord bot token is required")
	}
	if err := h.settingsRepo.Set(c.Request().Context(), service.DiscordSettingBotToken, botToken); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Discord bot token")
	}
	if sendResponses == "true" || sendResponses == "false" {
		_ = h.settingsRepo.Set(c.Request().Context(), service.DiscordSettingSendResponses, sendResponses)
	} else {
		current, _ := h.settingsRepo.Get(c.Request().Context(), service.DiscordSettingSendResponses)
		if strings.TrimSpace(current) == "" {
			_ = h.settingsRepo.Set(c.Request().Context(), service.DiscordSettingSendResponses, "true")
		}
	}
	if h.discordSvc != nil {
		if err := h.discordSvc.ReloadFromSettings(c.Request().Context()); err != nil {
			applog.Infof("warning: failed to reload discord gateway after settings save: %v", err)
		}
	}
	return returnToChannels(c)
}

func (h *Handler) handleDiscordRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	projectID, _ := h.getCurrentProjectID(c)
	if h.discordSvc != nil {
		_ = h.discordSvc.Disconnect(c.Request().Context())
	}
	resetErr := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: service.DiscordSettingBotToken, value: ""},
		{key: service.DiscordSettingBotUserID, value: ""},
		{key: service.DiscordSettingSendResponses, value: ""},
	})
	if h.discordAuthRepo != nil && projectID != "" {
		_ = h.discordAuthRepo.DeleteByProject(c.Request().Context(), projectID)
	}
	if resetErr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(resetErr)
	}
	return returnToChannels(c)
}

func (h *Handler) handleDiscordTest(c echo.Context) error {
	if h.discordSvc == nil {
		return renderStandardChannelConnectionTestFeedback(c, "Discord", false, nil)
	}
	return renderStandardChannelConnectionTestFeedback(c, "Discord", true, h.discordSvc.TestConnection(c.Request().Context()))
}

func (h *Handler) handleEmailConfigure(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	provider, imapHost, imapPort, smtpHost, smtpPort, err := service.ResolveEmailProviderSettings(
		c.FormValue("email_provider"),
		c.FormValue("email_imap_host"),
		c.FormValue("email_imap_port"),
		c.FormValue("email_smtp_host"),
		c.FormValue("email_smtp_port"),
	)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	emailAddress := repository.NormalizeEmailAddress(c.FormValue("email_address"))
	password := service.NormalizeEmailPasswordForProvider(provider, c.FormValue("email_password"))
	if password == "" {
		existing, _ := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingPassword)
		password = strings.TrimSpace(existing)
	}
	if emailAddress == "" || password == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Email address and app password are required")
	}
	settings := map[string]string{
		service.EmailSettingProvider:                provider,
		service.EmailSettingAddress:                 emailAddress,
		service.EmailSettingPassword:                password,
		service.EmailSettingIMAPHost:                imapHost,
		service.EmailSettingIMAPPort:                strconv.Itoa(imapPort),
		service.EmailSettingSMTPHost:                smtpHost,
		service.EmailSettingSMTPPort:                strconv.Itoa(smtpPort),
		service.EmailSettingPollIntervalSeconds:     defaultIfBlank(c.FormValue("email_poll_interval_seconds"), "15"),
		service.EmailSettingSendResponses:           boolFormValue(c.FormValue("email_send_responses"), true),
		service.EmailSettingSkipAttachments:         boolFormValue(c.FormValue("email_skip_attachments"), false),
		service.EmailSettingMarkExistingSeenOnStart: boolFormValue(c.FormValue("email_mark_existing_seen_on_start"), true),
	}
	for key, value := range settings {
		if err := h.settingsRepo.Set(c.Request().Context(), key, value); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save email settings")
		}
	}
	if h.emailService != nil {
		_ = h.emailService.ReloadFromSettings(c.Request().Context())
	}
	return returnToChannels(c)
}

func (h *Handler) handleEmailRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	if h.emailService != nil {
		h.emailService.Stop()
	}
	if err := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: service.EmailSettingProvider, value: ""},
		{key: service.EmailSettingAddress, value: ""},
		{key: service.EmailSettingPassword, value: ""},
		{key: service.EmailSettingIMAPHost, value: ""},
		{key: service.EmailSettingIMAPPort, value: ""},
		{key: service.EmailSettingSMTPHost, value: ""},
		{key: service.EmailSettingSMTPPort, value: ""},
		{key: service.EmailSettingPollIntervalSeconds, value: ""},
		{key: service.EmailSettingSendResponses, value: ""},
		{key: service.EmailSettingSkipAttachments, value: ""},
		{key: service.EmailSettingMarkExistingSeenOnStart, value: ""},
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(err)
	}
	return returnToChannels(c)
}

func (h *Handler) handleEmailTest(c echo.Context) error {
	if h.emailService == nil {
		return renderStandardChannelConnectionTestFeedback(c, "Email", false, nil)
	}
	return renderStandardChannelConnectionTestFeedback(c, "Email", true, h.emailService.TestConnection(c.Request().Context()))
}

func defaultIfBlank(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func boolFormValue(value string, fallback bool) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "true" || value == "on" || value == "1" {
		return "true"
	}
	if value == "false" || value == "0" {
		return "false"
	}
	if fallback {
		return "true"
	}
	return "false"
}

type channelConnectionTestFeedbackOptions struct {
	ElementID              string
	MissingServiceMessage  string
	AutoDismissOnSuccess   bool
	AutoDismissElementID   string
	AutoDismissDelayMillis int
}

func renderStandardChannelConnectionTestFeedback(c echo.Context, channelName string, serviceConfigured bool, testErr error) error {
	return renderStandardChannelConnectionTestFeedbackWithOptions(c, channelName, serviceConfigured, testErr, channelConnectionTestFeedbackOptions{})
}

func renderStandardChannelConnectionTestFeedbackWithOptions(c echo.Context, channelName string, serviceConfigured bool, testErr error, options channelConnectionTestFeedbackOptions) error {
	if !serviceConfigured {
		message := channelName + ` service not configured`
		if options.MissingServiceMessage != "" {
			message = options.MissingServiceMessage
		}
		return c.HTML(http.StatusOK, channelConnectionTestFeedbackHTML("text-error", message, options.ElementID)+channelConnectionTestAutoDismissHTML(false, options))
	}
	if testErr != nil {
		return c.HTML(http.StatusOK, channelConnectionTestFeedbackHTML("text-error", `Connection failed: `+testErr.Error(), options.ElementID)+channelConnectionTestAutoDismissHTML(false, options))
	}
	return c.HTML(http.StatusOK, channelConnectionTestFeedbackHTML("text-success", "Connection successful!", options.ElementID)+channelConnectionTestAutoDismissHTML(true, options))
}

func channelConnectionTestFeedbackHTML(textClass, message, elementID string) string {
	idAttr := ""
	if elementID != "" {
		idAttr = ` id="` + templateEscape(elementID) + `"`
	}
	return `<div class="flex items-center gap-2 ` + textClass + `"` + idAttr + `><span>` + templateEscape(message) + `</span></div>`
}

func channelConnectionTestAutoDismissHTML(success bool, options channelConnectionTestFeedbackOptions) string {
	if !success || !options.AutoDismissOnSuccess {
		return ""
	}
	elementID := options.AutoDismissElementID
	if elementID == "" {
		elementID = options.ElementID
	}
	if elementID == "" {
		return ""
	}
	delay := options.AutoDismissDelayMillis
	if delay <= 0 {
		delay = 3000
	}
	return `<script>setTimeout(function(){var el=document.getElementById(` + strconv.Quote(elementID) + `);if(el){el.style.transition='opacity 0.5s';el.style.opacity='0';setTimeout(function(){el.remove();},500);}},` + strconv.Itoa(delay) + `);</script>`
}

func (h *Handler) buildAbsoluteURL(c echo.Context, path string) string {
	if base := h.configuredAppBaseURL(); base != "" {
		return strings.TrimRight(base, "/") + path
	}

	req := c.Request()
	scheme := req.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if req.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := req.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = req.Host
	}
	base := (&url.URL{Scheme: scheme, Host: host, Path: path}).String()
	return base
}

func (h *Handler) configuredAppBaseURL() string {
	if h.appBaseURL != "" {
		return h.appBaseURL
	}
	if h.authMode == auth.AuthModeHostedSSO {
		return ""
	}
	return config.ResolveAppBaseURL(os.Getenv("APP_BASE_URL"))
}

func templateEscape(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(s)
}

// handlePersonalitySave saves the global chat personality setting
func (h *Handler) handlePersonalitySave(c echo.Context) error {
	// Accept personality from form value OR query param (for kebab menu hx-post with query string)
	personality := c.FormValue("personality")
	if personality == "" {
		personality = c.QueryParam("personality")
	}

	if h.settingsRepo != nil {
		if err := h.settingsRepo.Set(c.Request().Context(), "personality", personality); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save personality")
		}
	}

	// Return re-rendered personality section for HTMX requests
	if isHTMX(c) {
		return h.renderPersonalitySection(c)
	}

	return c.String(http.StatusOK, "Personality saved successfully")
}
