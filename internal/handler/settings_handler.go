package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
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

// handleChannels renders the channels (integrations) page
func (h *Handler) handleChannels(c echo.Context) error {
	projectID := c.QueryParam("project_id")

	// Get projects for sidebar
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load projects")
	}

	// Get current Telegram bot token and status
	var token string
	if h.settingsRepo != nil {
		token, _ = h.settingsRepo.Get(c.Request().Context(), "telegram_bot_token")
	}
	isBotRunning := h.telegramService != nil && h.telegramService.IsRunning()
	hasTelegramChannel := strings.TrimSpace(token) != "" || isBotRunning

	// Load authorized Telegram users for the current project
	var authorizedUsers []models.TelegramAuthorizedUser
	if projectID != "" && h.telegramAuthRepo != nil {
		authorizedUsers, _ = h.telegramAuthRepo.ListByProject(c.Request().Context(), projectID)
	}

	// Load authorized Slack users for the current project
	var slackAuthorizedUsers []models.SlackAuthorizedUser
	if projectID != "" && h.slackAuthRepo != nil {
		slackAuthorizedUsers, _ = h.slackAuthRepo.ListByProject(c.Request().Context(), projectID)
	}

	// Load send-responses setting (default: enabled)
	sendResponses := true
	if h.settingsRepo != nil {
		val, _ := h.settingsRepo.Get(c.Request().Context(), "telegram_send_responses")
		if val == "false" {
			sendResponses = false
		}
	}

	var githubStatus service.GitHubConnectionStatus
	githubAuthMode := service.GitHubAuthModePAT
	githubAppID := ""
	githubAppSlug := ""
	githubPrivateKeyValue := ""
	githubPATValue := ""
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
		if strings.TrimSpace(githubModeSetting) != "" {
			githubAuthMode = service.NormalizeGitHubAuthMode(githubModeSetting)
		}
	}
	if h.slackSvc != nil {
		slackStatus, _ = h.slackSvc.GetConnectionStatus(c.Request().Context())
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
	}
	hasGitHubChannel := githubStatus.Configured || githubStatus.Connected ||
		strings.TrimSpace(githubModeSetting) != "" ||
		githubHasPAT ||
		strings.TrimSpace(githubAppID) != "" ||
		strings.TrimSpace(githubAppSlug) != "" ||
		githubHasPrivateKey
	hasSlackChannel := slackStatus.Configured || slackStatus.Connected ||
		slackHasClientID || slackHasClientSecret || slackHasAppToken || slackHasBotToken || slackHasOAuthBotToken

	if isHTMX(c) {
		return render(c, http.StatusOK, pages.SettingsContent(token, isBotRunning, authorizedUsers, slackAuthorizedUsers, projectID, sendResponses, githubStatus, githubAuthMode, githubAppID, githubAppSlug, githubPrivateKeyValue, githubPATValue, githubHasPrivateKey, githubHasPAT, slackStatus, slackClientID, slackClientSecret, slackAppToken, slackBotToken, slackBotTokenMode, slackHasClientID, slackHasClientSecret, slackHasAppToken, slackHasBotToken, slackSendResponses, hasTelegramChannel, hasGitHubChannel, hasSlackChannel))
	}
	return render(c, http.StatusOK, pages.SettingsPage(token, isBotRunning, projects, projectID, authorizedUsers, slackAuthorizedUsers, sendResponses, githubStatus, githubAuthMode, githubAppID, githubAppSlug, githubPrivateKeyValue, githubPATValue, githubHasPrivateKey, githubHasPAT, slackStatus, slackClientID, slackClientSecret, slackAppToken, slackBotToken, slackBotTokenMode, slackHasClientID, slackHasClientSecret, slackHasAppToken, slackHasBotToken, slackSendResponses, hasTelegramChannel, hasGitHubChannel, hasSlackChannel))
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
		if err := h.settingsRepo.Set(c.Request().Context(), "telegram_bot_token", token); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save token")
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
		if h.settingsRepo != nil {
			svc.SetSettingsRepo(h.settingsRepo)
		}
		if h.customPersonalityRepo != nil {
			svc.SetCustomPersonalityRepo(h.customPersonalityRepo)
		}
		if h.chatBroadcaster != nil {
			svc.SetChatBroadcaster(h.chatBroadcaster)
		}
		svc.Start()
		h.telegramService = svc
	}

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

// handleTelegramTest tests the Telegram bot connection
func (h *Handler) handleTelegramTest(c echo.Context) error {
	if h.telegramService == nil || !h.telegramService.IsRunning() {
		// Return error HTML with red X icon
		errorHTML := `
			<div class="flex items-center gap-2 text-error" id="telegram-test-feedback">
				<svg xmlns="http://www.w3.org/2000/svg" class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
					<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />
				</svg>
				<span>Connection failed: Bot is not running</span>
			</div>
		`
		return c.HTML(http.StatusOK, errorHTML)
	}

	// Return success HTML with green checkmark and auto-dismiss
	successHTML := `
		<div class="flex items-center gap-2 text-success" id="telegram-test-feedback">
			<svg xmlns="http://www.w3.org/2000/svg" class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor">
				<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
			</svg>
			<span>Connection successful!</span>
		</div>
		<script>
			setTimeout(function() {
				var el = document.getElementById('telegram-test-feedback');
				if (el) {
					el.style.transition = 'opacity 0.5s';
					el.style.opacity = '0';
					setTimeout(function() { el.remove(); }, 500);
				}
			}, 3000);
		</script>
	`
	return c.HTML(http.StatusOK, successHTML)
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
		if err := h.settingsRepo.Set(c.Request().Context(), "telegram_send_responses", value); err != nil {
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
	_ = h.settingsRepo.Set(c.Request().Context(), "telegram_bot_token", "")
	_ = h.settingsRepo.Set(c.Request().Context(), "telegram_send_responses", "")

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
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

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
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
	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

func (h *Handler) handleGitHubRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	if h.githubSvc != nil {
		_ = h.githubSvc.Disconnect(c.Request().Context())
	}
	_ = h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAppID, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAppSlug, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAppPrivateKey, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingPAT, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingPATUserLogin, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.GitHubSettingAuthMode, "")

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
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

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

func (h *Handler) handleSlackConnect(c echo.Context) error {
	if h.slackSvc == nil {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack integration is not configured")
	}
	redirectURI := buildAbsoluteURL(c, "/channels/slack/callback")
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
	redirectURI := buildAbsoluteURL(c, "/channels/slack/callback")
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
	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

func (h *Handler) handleSlackRemove(c echo.Context) error {
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	if h.slackSvc != nil {
		_ = h.slackSvc.Disconnect(c.Request().Context())
	}
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingClientID, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingClientSecret, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingAppToken, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingBotToken, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingBotTokenOverride, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingBotTokenSource, service.SlackBotTokenSourceOAuth)
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingBotUserID, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingTeamID, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingTeamName, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingConnectedAt, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingOAuthState, "")
	_ = h.settingsRepo.Set(c.Request().Context(), service.SlackSettingSendResponses, "")

	if isHTMX(c) {
		c.Response().Header().Set("HX-Refresh", "true")
		return c.NoContent(http.StatusOK)
	}
	return c.Redirect(http.StatusSeeOther, "/channels")
}

func (h *Handler) handleSlackTest(c echo.Context) error {
	if h.slackSvc == nil {
		return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-error"><span>Slack service not configured</span></div>`)
	}
	if err := h.slackSvc.TestConnection(c.Request().Context()); err != nil {
		return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-error"><span>Connection failed: `+templateEscape(err.Error())+`</span></div>`)
	}
	return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-success"><span>Connection successful!</span></div>`)
}

func buildAbsoluteURL(c echo.Context, path string) string {
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
