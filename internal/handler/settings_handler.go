package handler

import (
	"context"
	"errors"
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
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/pages"
)

var newTelegramService = func(
	token string,
	taskSvc *service.TaskService,
	projectRepo persistence.ProjectStore,
	llmConfigRepo persistence.LLMConfigStore,
	taskRepo persistence.TaskStore,
	execRepo persistence.ExecutionStore,
	scheduleRepo persistence.ScheduleStore,
	chatAttachmentRepo persistence.ChatAttachmentStore,
	llmSvc *service.LLMService,
	workerSvc *service.WorkerService,
) (*service.TelegramService, error) {
	return service.NewTelegramService(token, taskSvc, projectRepo, llmConfigRepo, taskRepo, execRepo, scheduleRepo, chatAttachmentRepo, llmSvc, workerSvc)
}

var updateTelegramServiceToken = func(svc *service.TelegramService, token string) error {
	return svc.UpdateToken(token)
}

const channelsRefreshTrigger = "channels-refresh"

type channelSettingReset struct {
	key   string
	value string
}

func applyChannelSettingResets(ctx context.Context, settingsRepo persistence.SettingsStore, resets []channelSettingReset) error {
	values := make(map[string]string, len(resets))
	for _, reset := range resets {
		values[reset.key] = reset.value
	}
	return settingsRepo.SetMany(ctx, values)
}

func channelBulkProviderResets(provider string) (map[string]string, bool) {
	resets := map[string]string{}
	add := func(key, value string) { resets[key] = value }
	switch provider {
	case "github":
		for _, key := range []string{
			service.GitHubSettingAppID, service.GitHubSettingAppSlug, service.GitHubSettingAppPrivateKey,
			service.GitHubSettingPAT, service.GitHubSettingPATUserLogin, service.GitHubSettingAuthMode,
			service.GitHubSettingAPIEndpoint, "github_app_installation_id", "github_app_account_login",
			"github_app_account_type", "github_app_connected_at",
		} {
			add(key, "")
		}
	case "slack":
		for _, key := range []string{
			service.SlackSettingClientID, service.SlackSettingClientSecret, service.SlackSettingAppToken,
			service.SlackSettingBotToken, service.SlackSettingBotTokenOverride, service.SlackSettingBotUserID,
			service.SlackSettingTeamID, service.SlackSettingTeamName, service.SlackSettingConnectedAt,
			service.SlackSettingOAuthState, service.SlackSettingSendResponses,
		} {
			add(key, "")
		}
		add(service.SlackSettingBotTokenSource, service.SlackBotTokenSourceOAuth)
	case "telegram":
		add(service.TelegramSettingBotToken, "")
		add(service.TelegramSettingSendResponses, "")
		add(service.TelegramSettingRichMessagesV2, "")
	case "discord":
		add(service.DiscordSettingBotToken, "")
		add(service.DiscordSettingBotUserID, "")
		add(service.DiscordSettingSendResponses, "")
	case "email":
		for _, key := range []string{
			service.EmailSettingProvider, service.EmailSettingAddress, service.EmailSettingPassword,
			service.EmailSettingIMAPHost, service.EmailSettingIMAPPort, service.EmailSettingSMTPHost,
			service.EmailSettingSMTPPort, service.EmailSettingPollIntervalSeconds, service.EmailSettingSendResponses,
			service.EmailSettingSkipAttachments, service.EmailSettingMarkExistingSeenOnStart,
		} {
			add(key, "")
		}
	case "x":
		for _, key := range []string{
			service.XSettingConsumerKey, service.XSettingConsumerSecret, service.XSettingAccessToken,
			service.XSettingAccessTokenSecret, service.XSettingPollIntervalSeconds, service.XSettingSendResponses,
			service.XSettingSinceID, service.XSettingAccountID, service.XSettingConfigurationID, service.XSettingRunAsUserID,
		} {
			add(key, "")
		}
	default:
		return nil, false
	}
	return resets, true
}

func mergeChannelSettingResets(target, source map[string]string) {
	for key, value := range source {
		target[key] = value
	}
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

func (h *Handler) handleChannelsBulkDelete(c echo.Context) error {
	ids, err := decodeBulkIDs(c.Request().Body)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}

	providers := make(map[string]bool)
	settingResets := make(map[string]string)
	webhookIDs := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.HasPrefix(id, "channel:") {
			provider := strings.TrimPrefix(id, "channel:")
			resets, ok := channelBulkProviderResets(provider)
			if !ok {
				return echo.NewHTTPError(http.StatusBadRequest, "unsupported channel selection")
			}
			providers[provider] = true
			mergeChannelSettingResets(settingResets, resets)
			continue
		}
		webhookIDs = append(webhookIDs, id)
	}

	if providers["x"] {
		h.xConfigMu.Lock()
		defer h.xConfigMu.Unlock()
	}
	if err := h.settingsRepo.RemoveChannelsBulk(c.Request().Context(), projectID, settingResets, webhookIDs, providers["discord"]); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "all selected channels must be removable from the current project").SetInternal(err)
	}

	if providers["github"] && h.githubSvc != nil {
		_ = h.githubSvc.Disconnect(c.Request().Context())
	}
	if providers["slack"] && h.slackSvc != nil {
		_ = h.slackSvc.Disconnect(c.Request().Context())
	}
	if providers["telegram"] && h.telegramService != nil && h.telegramService.IsRunning() {
		h.telegramService.Stop()
	}
	if providers["discord"] && h.discordSvc != nil {
		_ = h.discordSvc.Disconnect(c.Request().Context())
	}
	if providers["email"] && h.emailService != nil {
		h.emailService.Stop()
	}
	if providers["x"] {
		old := h.swapXService(nil)
		if h.channelMessageRouter != nil {
			h.channelMessageRouter.SetXService(nil)
		}
		if old != nil {
			old.Stop()
		}
	}

	return c.JSON(http.StatusOK, map[string]int{"deleted": len(ids)})
}

// handleChannels renders the channels (integrations) page
func (h *Handler) handleChannels(c echo.Context) error {
	if err := h.prepareChannelSenderOptions(c, "", false); err != nil {
		return projectAccessHTTPError(err)
	}
	projectID := c.QueryParam("project_id")

	// Get projects for sidebar
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load projects")
	}

	ctx := c.Request().Context()
	page := parseCardPageRequest(c)
	channelTypeFilter := allowlistedQuery(c, "type", "", "github", "slack", "telegram", "discord", "x", "email", "webhook", "outbound_targets")
	connectionStateFilter := allowlistedQuery(c, "connection_state", "", "connected", "configured", "disconnected")
	webhookEnabledFilter := allowlistedQuery(c, "webhook_enabled", "", "true", "false")
	channelSort := allowlistedQuery(c, "sort", "name_asc", "name_asc", "name_desc")
	resolvedProjectID := projectID
	if id, err := h.getCurrentProjectID(c); err == nil && id != "" {
		resolvedProjectID = id
	} else if resolvedProjectID == "" && len(projects) > 0 {
		resolvedProjectID = projects[0].ID
	}

	settingsValues := map[string]string{}
	if h.settingsRepo != nil {
		keys := []string{
			service.TelegramSettingBotToken,
			service.TelegramSettingSendResponses,
			service.TelegramSettingRichMessagesV2,
			service.GitHubSettingAuthMode,
			service.GitHubSettingAppID,
			service.GitHubSettingAppSlug,
			service.GitHubSettingAppPrivateKey,
			service.GitHubSettingPAT,
			service.GitHubSettingAPIEndpoint,
			service.SlackSettingClientID,
			service.SlackSettingClientSecret,
			service.SlackSettingAppToken,
			service.SlackSettingBotTokenOverride,
			service.SlackSettingBotTokenSource,
			service.SlackSettingBotToken,
			service.SlackSettingSendResponses,
			service.DiscordSettingBotToken,
			service.DiscordSettingSendResponses,
			service.XSettingConsumerKey,
			service.XSettingConsumerSecret,
			service.XSettingAccessToken,
			service.XSettingAccessTokenSecret,
			service.XSettingPollIntervalSeconds,
			service.XSettingSendResponses,
			service.EmailSettingProvider, service.EmailSettingAddress,
			service.EmailSettingPassword,
			service.EmailSettingIMAPHost,
			service.EmailSettingIMAPPort,
			service.EmailSettingSMTPHost,
			service.EmailSettingSMTPPort,
			service.EmailSettingPollIntervalSeconds,
			service.EmailSettingSendResponses,
			service.EmailSettingSkipAttachments,
			service.EmailSettingMarkExistingSeenOnStart,
		}
		if resolvedProjectID != "" {
			keys = append(keys, service.SendMessageAllowExplicitTargetsSetting+":"+resolvedProjectID)
		}
		settingsValues, _ = h.settingsRepo.GetMany(ctx, keys)
	}
	setting := func(key string) string {
		return settingsValues[key]
	}

	// Get current Telegram bot token and status
	token := setting(service.TelegramSettingBotToken)
	isBotRunning := h.telegramService != nil && h.telegramService.IsRunning()
	hasTelegramChannel := strings.TrimSpace(token) != "" || isBotRunning

	// Load system-level inbound channel authorization allowlists.
	var authorizedUsers []models.TelegramAuthorizedUser
	if h.telegramAuthRepo != nil {
		authorizedUsers, _ = h.telegramAuthRepo.ListByProject(ctx, resolvedProjectID)
	}

	var slackAuthorizedUsers []models.SlackAuthorizedUser
	if h.slackAuthRepo != nil {
		slackAuthorizedUsers, _ = h.slackAuthRepo.ListByProject(ctx, resolvedProjectID)
	}

	var emailAuthorizedSenders []models.EmailAuthorizedSender
	if h.emailAuthRepo != nil {
		emailAuthorizedSenders, _ = h.emailAuthRepo.ListByProject(ctx, resolvedProjectID)
	}

	var discordAuthorizedUsers []models.DiscordAuthorizedUser
	if h.discordAuthRepo != nil {
		discordAuthorizedUsers, _ = h.discordAuthRepo.ListByProject(ctx, resolvedProjectID)
	}

	var xAuthorizedUsers []models.XAuthorizedUser
	if h.xAuthRepo != nil && resolvedProjectID != "" {
		xAuthorizedUsers, _ = h.xAuthRepo.ListByProject(ctx, resolvedProjectID)
	}

	// Load Telegram settings (default: enabled)
	sendResponses := true
	richMessagesV2 := true
	if strings.EqualFold(strings.TrimSpace(setting(service.TelegramSettingSendResponses)), "false") {
		sendResponses = false
	}
	if strings.EqualFold(strings.TrimSpace(setting(service.TelegramSettingRichMessagesV2)), "false") {
		richMessagesV2 = false
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
	xStatus := service.XConnectionStatus{}
	xPollIntervalSeconds := strings.TrimSpace(setting(service.XSettingPollIntervalSeconds))
	if xPollIntervalSeconds == "" {
		xPollIntervalSeconds = "30"
	}
	xSendResponses := !strings.EqualFold(strings.TrimSpace(setting(service.XSettingSendResponses)), "false")
	xHasConsumerKey := strings.TrimSpace(setting(service.XSettingConsumerKey)) != ""
	xHasConsumerSecret := strings.TrimSpace(setting(service.XSettingConsumerSecret)) != ""
	xHasAccessToken := strings.TrimSpace(setting(service.XSettingAccessToken)) != ""
	xHasAccessTokenSecret := strings.TrimSpace(setting(service.XSettingAccessTokenSecret)) != ""
	if xService := h.getXService(); xService != nil {
		xStatus = xService.Status()
	} else {
		xStatus.Configured = xHasConsumerKey && xHasConsumerSecret && xHasAccessToken && xHasAccessTokenSecret
	}
	emailStatus := service.EmailConnectionStatus{Provider: service.EmailProviderCustom, IMAPPort: 993, SMTPPort: 587}
	emailPasswordValue := ""
	emailHasPassword := false
	emailSendResponses := true
	emailSkipAttachments := false
	emailMarkExistingSeenOnStart := true
	emailPollIntervalSeconds := "15"
	if h.githubSvc != nil {
		githubStatus, _ = h.githubSvc.GetConnectionStatus(ctx)
		if githubStatus.AuthMode != "" {
			githubAuthMode = service.NormalizeGitHubAuthMode(githubStatus.AuthMode)
		}
		githubHasPAT = githubStatus.HasPAT
	}
	githubModeSetting = setting(service.GitHubSettingAuthMode)
	githubAppID = setting(service.GitHubSettingAppID)
	githubAppSlug = setting(service.GitHubSettingAppSlug)
	githubPrivateKeyValue = setting(service.GitHubSettingAppPrivateKey)
	githubHasPrivateKey = strings.TrimSpace(githubPrivateKeyValue) != ""
	githubPATValue = setting(service.GitHubSettingPAT)
	if strings.TrimSpace(githubPATValue) != "" {
		githubHasPAT = true
	}
	githubAPIEndpoint = setting(service.GitHubSettingAPIEndpoint)
	if strings.TrimSpace(githubModeSetting) != "" {
		githubAuthMode = service.NormalizeGitHubAuthMode(githubModeSetting)
	}
	if h.slackSvc != nil {
		slackStatus, _ = h.slackSvc.GetConnectionStatus(ctx)
	}
	if h.discordSvc != nil {
		discordStatus, _ = h.discordSvc.GetConnectionStatus(ctx)
	}
	slackClientID = setting(service.SlackSettingClientID)
	slackClientSecret = setting(service.SlackSettingClientSecret)
	slackAppToken = setting(service.SlackSettingAppToken)
	slackBotToken = setting(service.SlackSettingBotTokenOverride)
	slackBotTokenMode = setting(service.SlackSettingBotTokenSource)
	slackBotTokenMode = strings.TrimSpace(strings.ToLower(slackBotTokenMode))
	if slackBotTokenMode != service.SlackBotTokenSourceManual {
		slackBotTokenMode = service.SlackBotTokenSourceOAuth
	}
	oauthBotToken := setting(service.SlackSettingBotToken)
	slackHasOAuthBotToken = strings.TrimSpace(oauthBotToken) != ""
	if strings.TrimSpace(strings.ToLower(setting(service.SlackSettingSendResponses))) == "false" {
		slackSendResponses = false
	}
	slackHasClientID = strings.TrimSpace(slackClientID) != ""
	slackHasClientSecret = strings.TrimSpace(slackClientSecret) != ""
	slackHasAppToken = strings.TrimSpace(slackAppToken) != ""
	slackHasBotToken = strings.TrimSpace(slackBotToken) != ""

	discordBotToken = setting(service.DiscordSettingBotToken)
	if strings.TrimSpace(strings.ToLower(setting(service.DiscordSettingSendResponses))) == "false" {
		discordSendResponses = false
	}

	emailProvider := setting(service.EmailSettingProvider)
	emailAddress := setting(service.EmailSettingAddress)
	emailPasswordValue = setting(service.EmailSettingPassword)
	emailHasPassword = strings.TrimSpace(emailPasswordValue) != ""
	emailIMAPHost := setting(service.EmailSettingIMAPHost)
	emailIMAPPort := setting(service.EmailSettingIMAPPort)
	emailSMTPHost := setting(service.EmailSettingSMTPHost)
	emailSMTPPort := setting(service.EmailSettingSMTPPort)
	emailPollIntervalSeconds = setting(service.EmailSettingPollIntervalSeconds)
	if strings.TrimSpace(emailPollIntervalSeconds) == "" {
		emailPollIntervalSeconds = "15"
	}
	if strings.TrimSpace(strings.ToLower(setting(service.EmailSettingSendResponses))) == "false" {
		emailSendResponses = false
	}
	if strings.TrimSpace(strings.ToLower(setting(service.EmailSettingSkipAttachments))) == "true" {
		emailSkipAttachments = true
	}
	if strings.TrimSpace(strings.ToLower(setting(service.EmailSettingMarkExistingSeenOnStart))) == "false" {
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
	if h.emailService != nil {
		runtimeStatus := h.emailService.GetConnectionStatus(ctx)
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
	hasXChannel := xStatus.Configured || xStatus.Connected || xHasConsumerKey || xHasConsumerSecret || xHasAccessToken || xHasAccessTokenSecret

	var channelTargets []models.ChannelTarget
	sendMessageExplicitTargets := false
	if resolvedProjectID != "" && h.channelTargetRepo != nil {
		channelTargets, _ = h.channelTargetRepo.ListByProject(ctx, resolvedProjectID)
	}
	if resolvedProjectID != "" {
		if val := setting(service.SendMessageAllowExplicitTargetsSetting + ":" + resolvedProjectID); strings.TrimSpace(val) != "" {
			sendMessageExplicitTargets = strings.EqualFold(strings.TrimSpace(val), "true")
		}
	}

	// Load webhooks for current project using the bounded card projection.
	var webhooks []models.WebhookEndpoint
	var webhooksHasMore bool
	if resolvedProjectID != "" && h.webhookRepo != nil {
		channelType := channelTypeFilter
		connectionState := connectionStateFilter
		enabled := optionalBoolQuery(c, "webhook_enabled")
		var connectionEnabled *bool
		switch connectionState {
		case "connected":
			value := true
			connectionEnabled = &value
		case "disconnected":
			value := false
			connectionEnabled = &value
		}
		conflictingEnabledFilters := enabled != nil && connectionEnabled != nil && *enabled != *connectionEnabled
		if enabled == nil {
			enabled = connectionEnabled
		}
		if !conflictingEnabledFilters && (channelType == "" || channelType == "webhook") {
			pageItems, err := h.webhookRepo.ListCardsByProjectPageFiltered(ctx, resolvedProjectID, page.PageSize+1, page.Offset, persistence.WebhookCardFilter{Search: page.Search, Enabled: enabled, Sort: channelSort})
			if err != nil {
				return err
			}
			webhooks, webhooksHasMore = cardPageItems(pageItems, page.PageSize)
		}
	}
	setCardPageResponse(c, webhooksHasMore)

	// Load compact agents for webhook agent selection in the current project.
	var agentPickerOptions []persistence.AgentPickerOption
	if h.agentRepo != nil && resolvedProjectID != "" {
		agentPickerOptions, _ = h.agentRepo.ListPickerOptionsForProject(ctx, resolvedProjectID)
	}

	webhookAgents := map[string][]models.WebhookEndpointAgent{}

	channelView := pages.ChannelsSettingsView{
		TelegramToken:                token,
		IsBotRunning:                 isBotRunning,
		AuthorizedUsers:              authorizedUsers,
		SlackAuthorizedUsers:         slackAuthorizedUsers,
		DiscordAuthorizedUsers:       discordAuthorizedUsers,
		XAuthorizedUsers:             xAuthorizedUsers,
		XStatus:                      xStatus,
		XHasConsumerKey:              xHasConsumerKey,
		XHasConsumerSecret:           xHasConsumerSecret,
		XHasAccessToken:              xHasAccessToken,
		XHasAccessTokenSecret:        xHasAccessTokenSecret,
		XPollIntervalSeconds:         xPollIntervalSeconds,
		XSendResponses:               xSendResponses,
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
		HasXChannel:                  hasXChannel,
		HasEmailChannel:              hasEmailChannel,
		Webhooks:                     webhooks,
		WebhooksPageOffset:           page.Offset,
		WebhooksSearch:               page.Search,
		ChannelTypeFilter:            channelTypeFilter,
		ConnectionStateFilter:        connectionStateFilter,
		WebhookEnabledFilter:         webhookEnabledFilter,
		Sort:                         channelSort,
		WebhooksHasMore:              webhooksHasMore,
		AgentPickerOptions:           agentPickerOptions,
		WebhookAgents:                webhookAgents,
		ChannelTargets:               channelTargets,
		SendMessageExplicitTargets:   sendMessageExplicitTargets,
	}
	if isHTMX(c) || page.IsFragment {
		return render(c, http.StatusOK, pages.SettingsContent(channelView))
	}
	return render(c, http.StatusOK, pages.SettingsPage(projects, channelView))
}

// handleAppSettings renders the application settings page (personality, etc.)
func (h *Handler) handleAppSettings(c echo.Context) error {
	projectID := c.QueryParam("project_id")
	page := parseCardPageRequest(c)

	// Get projects for sidebar
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load projects")
	}

	// Load global personality setting
	var personality string
	if h.settingsRepo != nil {
		personality, _ = h.settingsRepo.Get(c.Request().Context(), "personality")
	}

	// Load custom personalities using the bounded card projection.
	customPersonalities, customPersonalitiesHasMore, err := h.listPersonalityCardPage(c.Request().Context(), page, personality)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load custom personalities")
	}
	setCardPageResponse(c, customPersonalitiesHasMore)

	state := personalityListState(page)
	state.ProjectID = projectID
	if isHTMX(c) || page.IsFragment {
		return render(c, http.StatusOK, pages.AppSettingsContentWithPaginationState(personality, projectID, customPersonalities, customPersonalitiesHasMore, state))
	}
	return render(c, http.StatusOK, pages.AppSettingsPageWithPaginationState(personality, projects, projectID, customPersonalities, customPersonalitiesHasMore, state))
}

// handleTelegramSave saves the Telegram bot token and starts the bot
func (h *Handler) handleTelegramSave(c echo.Context) error {
	token := c.FormValue("token")

	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	richValue := "false"
	if c.FormValue("telegram_rich_messages_v2") == "true" {
		richValue = "true"
	}
	err := h.settingsRepo.SetMany(c.Request().Context(), map[string]string{
		service.TelegramSettingBotToken:       token,
		service.TelegramSettingRichMessagesV2: richValue,
	})
	if errors.Is(err, persistence.ErrOrgDenied) {
		return echo.NewHTTPError(http.StatusForbidden, "organization access required")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save Telegram configuration").SetInternal(err)
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
		svc.SetTelegramUserProjectRepo(h.telegramUserProjectRepo)
		if h.usageAnalyticsSvc != nil {
			svc.SetUsageAnalyticsService(h.usageAnalyticsSvc)
		}
		if h.agentRepo != nil {
			svc.SetAgentRepo(h.agentRepo)
		}
		if h.settingsRepo != nil {
			svc.SetSettingsRepo(h.settingsRepo)
		}
		if h.customPersonalityRepo != nil {
			svc.SetCustomPersonalityRepo(h.customPersonalityRepo)
		}
		if h.automationGraphSvc != nil {
			svc.SetAutomationGraphService(h.automationGraphSvc)
		}
		svc.SetProjectCreationServices(h.projectSvc, h.githubSvc, h.memorySvc, h.agentLibraryMaintenanceSvc)
		if h.chatBroadcaster != nil {
			svc.SetChatBroadcaster(h.chatBroadcaster)
		}
		if h.threadInputRepo != nil {
			svc.SetThreadInputRepo(h.threadInputRepo)
		}
		h.wireChannelIntegrationSummaryDeps(svc)
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
		ElementID:          "telegram-test-feedback",
		MissingServiceText: "Connection failed: Bot is not running",
		AutoDismissSuccess: true,
	}
	if h.telegramService == nil || !h.telegramService.IsRunning() {
		return renderStandardChannelConnectionTestFeedback(c, "Telegram", false, nil, options)
	}
	return renderStandardChannelConnectionTestFeedback(c, "Telegram", true, nil, options)
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
	if err := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: service.TelegramSettingBotToken, value: ""},
		{key: service.TelegramSettingSendResponses, value: ""},
		{key: service.TelegramSettingRichMessagesV2, value: ""},
	}); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(err)
	}

	if h.telegramService != nil && h.telegramService.IsRunning() {
		h.telegramService.Stop()
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
	connectURL, err = h.bindChannelOAuth(c, "github", connectURL)
	if err != nil {
		return projectAccessHTTPError(err)
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
	ctx := c.Request().Context()
	keys := []string{service.GitHubSettingAuthMode, service.GitHubSettingPAT, service.GitHubSettingPATUserLogin, service.GitHubSettingAppID, service.GitHubSettingAppSlug, service.GitHubSettingAppPrivateKey, service.GitHubSettingAPIEndpoint, "github_app_installation_id", "github_app_account_login", "github_app_account_type", "github_app_connected_at"}
	snapshot, err := h.settingsRepo.GetMany(ctx, keys)
	if err != nil {
		return echo.NewHTTPError(500, "failed to read GitHub configuration").SetInternal(err)
	}
	expected := make(map[string]string, len(keys))
	for _, key := range keys {
		expected[key] = snapshot[key]
	}

	if strings.TrimSpace(c.FormValue("github_auth_mode")) == "" && (appID != "" || appSlug != "" || privateKey != "") {
		authMode = service.GitHubAuthModeApp
	}

	if authMode == service.GitHubAuthModePAT {
		if pat == "" {
			existingPAT := snapshot[service.GitHubSettingPAT]
			if strings.TrimSpace(existingPAT) == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "GitHub personal access token is required")
			}
			pat = strings.TrimSpace(existingPAT)
		}
	} else {
		if appID == "" || appSlug == "" {
			return echo.NewHTTPError(http.StatusBadRequest, "GitHub App ID and slug are required")
		}
		if privateKey == "" {
			existingPrivateKey := snapshot[service.GitHubSettingAppPrivateKey]
			if strings.TrimSpace(existingPrivateKey) == "" {
				return echo.NewHTTPError(http.StatusBadRequest, "GitHub App private key is required")
			}
			privateKey = existingPrivateKey
		}
	}

	values := map[string]string{service.GitHubSettingAPIEndpoint: apiEndpoint, service.GitHubSettingAuthMode: authMode}
	if authMode == service.GitHubAuthModePAT {
		values[service.GitHubSettingPAT] = pat
		values[service.GitHubSettingPATUserLogin] = ""
	} else {
		values[service.GitHubSettingAppID] = appID
		values[service.GitHubSettingAppSlug] = appSlug
		values[service.GitHubSettingAppPrivateKey] = privateKey
		// Clear the previous App connection in the same commit as its replacement credentials.
		for _, key := range []string{"github_app_installation_id", "github_app_account_login", "github_app_account_type", "github_app_connected_at"} {
			values[key] = ""
		}
	}
	updated, err := h.settingsRepo.CompareAndSetMany(ctx, expected, values, nil)
	if errors.Is(err, persistence.ErrOrgDenied) {
		return echo.NewHTTPError(403, "organization access required")
	}
	if err != nil {
		return echo.NewHTTPError(500, "failed to save GitHub configuration").SetInternal(err)
	}
	if !updated {
		return echo.NewHTTPError(409, "GitHub configuration changed; refresh before saving")
	}

	return returnToChannels(c)
}

func (h *Handler) handleGitHubCallback(c echo.Context) error {
	if err := h.consumeChannelOAuth(c, "github"); err != nil {
		return projectAccessHTTPError(err)
	}
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
	if err := applyChannelSettingResets(c.Request().Context(), h.settingsRepo, []channelSettingReset{
		{key: "github_app_installation_id", value: ""},
		{key: "github_app_account_login", value: ""},
		{key: "github_app_account_type", value: ""},
		{key: "github_app_connected_at", value: ""},
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
	ctx := c.Request().Context()
	keys := []string{service.SlackSettingClientID, service.SlackSettingClientSecret, service.SlackSettingAppToken, service.SlackSettingBotTokenOverride, service.SlackSettingBotTokenSource, service.SlackSettingSendResponses}
	snapshot, err := h.settingsRepo.GetMany(ctx, keys)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read Slack configuration").SetInternal(err)
	}
	expected := make(map[string]string, len(keys))
	for _, key := range keys {
		expected[key] = snapshot[key]
	}
	value := func(key string) string {
		v := strings.TrimSpace(c.FormValue(key))
		if v == "" {
			v = strings.TrimSpace(snapshot[key])
		}
		return v
	}
	clientID := value(service.SlackSettingClientID)
	clientSecret := value(service.SlackSettingClientSecret)
	appToken := value(service.SlackSettingAppToken)
	mode := strings.ToLower(strings.TrimSpace(c.FormValue("slack_bot_token_mode")))
	if mode != service.SlackBotTokenSourceManual {
		mode = service.SlackBotTokenSourceOAuth
	}
	token := strings.TrimSpace(c.FormValue("slack_bot_token"))
	if token == "" {
		token = strings.TrimSpace(snapshot[service.SlackSettingBotTokenOverride])
	}
	if clientID == "" || clientSecret == "" || appToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack client ID, client secret, and app token are required")
	}
	if mode == service.SlackBotTokenSourceManual && token == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Slack bot token is required when manual override mode is selected")
	}
	values := map[string]string{service.SlackSettingClientID: clientID, service.SlackSettingClientSecret: clientSecret, service.SlackSettingAppToken: appToken, service.SlackSettingBotTokenSource: mode}
	if mode == service.SlackBotTokenSourceManual {
		values[service.SlackSettingBotTokenOverride] = token
	}
	send := strings.ToLower(strings.TrimSpace(c.FormValue("slack_send_responses")))
	if send == "true" || send == "false" {
		values[service.SlackSettingSendResponses] = send
	} else if send == "" && strings.TrimSpace(snapshot[service.SlackSettingSendResponses]) == "" {
		values[service.SlackSettingSendResponses] = "true"
	}
	updated, err := h.settingsRepo.CompareAndSetMany(ctx, expected, values, nil)
	if errors.Is(err, persistence.ErrOrgDenied) {
		return echo.NewHTTPError(http.StatusForbidden, "organization access required")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to save Slack configuration").SetInternal(err)
	}
	if !updated {
		return echo.NewHTTPError(http.StatusConflict, "Slack configuration changed; refresh before saving")
	}
	if h.slackSvc != nil {
		_ = h.slackSvc.ReloadFromSettings(ctx)
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
	connectURL, err = h.bindChannelOAuth(c, "slack", connectURL)
	if err != nil {
		return projectAccessHTTPError(err)
	}
	return c.Redirect(http.StatusTemporaryRedirect, connectURL)
}

func (h *Handler) handleSlackCallback(c echo.Context) error {
	if err := h.consumeChannelOAuth(c, "slack"); err != nil {
		return projectAccessHTTPError(err)
	}
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

	if h.slackSvc != nil {
		_ = h.slackSvc.Disconnect(c.Request().Context())
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
	snapshot, err := h.settingsRepo.GetMany(c.Request().Context(), []string{service.DiscordSettingBotToken, service.DiscordSettingSendResponses})
	if err != nil {
		return echo.NewHTTPError(500, "failed to read Discord settings").SetInternal(err)
	}
	botToken := strings.TrimSpace(c.FormValue("discord_bot_token"))
	sendResponses := strings.TrimSpace(strings.ToLower(c.FormValue("discord_send_responses")))
	if botToken == "" {
		botToken = strings.TrimSpace(snapshot[service.DiscordSettingBotToken])
	}
	if botToken == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "Discord bot token is required")
	}
	values := map[string]string{service.DiscordSettingBotToken: botToken}
	if sendResponses == "true" || sendResponses == "false" {
		values[service.DiscordSettingSendResponses] = sendResponses
	} else if strings.TrimSpace(snapshot[service.DiscordSettingSendResponses]) == "" {
		values[service.DiscordSettingSendResponses] = "true"
	}
	expected := map[string]string{service.DiscordSettingBotToken: snapshot[service.DiscordSettingBotToken], service.DiscordSettingSendResponses: snapshot[service.DiscordSettingSendResponses]}
	updated, err := h.settingsRepo.CompareAndSetMany(c.Request().Context(), expected, values, nil)
	if errors.Is(err, persistence.ErrOrgDenied) {
		return echo.NewHTTPError(403, "organization access required")
	}
	if err != nil {
		return echo.NewHTTPError(500, "failed to save Discord settings").SetInternal(err)
	}
	if !updated {
		return echo.NewHTTPError(409, "Discord configuration changed; refresh before saving")
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
	resets, _ := channelBulkProviderResets("discord")
	if err := h.settingsRepo.RemoveDiscordChannel(c.Request().Context(), projectID, resets); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove channel settings").SetInternal(err)
	}
	if h.discordSvc != nil {
		if err := h.discordSvc.Disconnect(c.Request().Context()); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "settings removed but Discord disconnect failed").SetInternal(err)
		}
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
	emailAddress := persistence.NormalizeEmailAddress(c.FormValue("email_address"))
	password := service.NormalizeEmailPasswordForProvider(provider, c.FormValue("email_password"))
	existingPassword, err := h.settingsRepo.Get(c.Request().Context(), service.EmailSettingPassword)
	if err != nil {
		return echo.NewHTTPError(500, "failed to read email settings").SetInternal(err)
	}
	if password == "" {
		password = strings.TrimSpace(existingPassword)
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
	updated, err := h.settingsRepo.CompareAndSetMany(c.Request().Context(), map[string]string{service.EmailSettingPassword: existingPassword}, settings, nil)
	if errors.Is(err, persistence.ErrOrgDenied) {
		return echo.NewHTTPError(403, "organization access required")
	}
	if err != nil {
		return echo.NewHTTPError(500, "failed to save email settings").SetInternal(err)
	}
	if !updated {
		return echo.NewHTTPError(409, "Email credentials changed; refresh before saving")
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
	if h.emailService != nil {
		h.emailService.Stop()
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

// saveOptionalBoolSetting preserves a stored preference when the form omits it.
func saveOptionalBoolSetting(ctx context.Context, settingsRepo persistence.SettingsStore, key, value string, fallback bool) error {
	if value == "true" || value == "false" {
		return settingsRepo.Set(ctx, key, value)
	}
	if value != "" {
		return nil
	}
	current, err := settingsRepo.Get(ctx, key)
	if err != nil {
		return err
	}
	if strings.TrimSpace(current) == "" {
		return settingsRepo.Set(ctx, key, strconv.FormatBool(fallback))
	}
	return nil
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
	ElementID          string
	MissingServiceText string
	AutoDismissSuccess bool
}

func renderStandardChannelConnectionTestFeedback(c echo.Context, channelName string, serviceConfigured bool, testErr error, opts ...channelConnectionTestFeedbackOptions) error {
	options := channelConnectionTestFeedbackOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	elementID := ""
	if options.ElementID != "" {
		elementID = ` id="` + templateEscape(options.ElementID) + `"`
	}
	if !serviceConfigured {
		message := channelName + " service not configured"
		if options.MissingServiceText != "" {
			message = options.MissingServiceText
		}
		return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-error"`+elementID+`><span>`+templateEscape(message)+`</span></div>`)
	}
	if testErr != nil {
		return c.HTML(http.StatusOK, `<div class="flex items-center gap-2 text-error"`+elementID+`><span>Connection failed: `+templateEscape(testErr.Error())+`</span></div>`)
	}
	feedbackHTML := `<div class="flex items-center gap-2 text-success"` + elementID + `><span>Connection successful!</span></div>`
	if options.AutoDismissSuccess && options.ElementID != "" {
		feedbackHTML += `<script>
			setTimeout(function() {
				var el = document.getElementById(` + strconv.Quote(options.ElementID) + `);
				if (el) {
					el.style.transition = 'opacity 0.5s';
					el.style.opacity = '0';
					setTimeout(function() { el.remove(); }, 500);
				}
			}, 3000);
		</script>`
	}
	return c.HTML(http.StatusOK, feedbackHTML)
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
	ctx := c.Request().Context()
	// Accept personality from form value OR query param (for kebab menu hx-post with query string).
	// Read PostForm directly so an explicit empty form value still clears to the base/default personality.
	personality := c.QueryParam("personality")
	if err := c.Request().ParseForm(); err == nil {
		if values, ok := c.Request().PostForm["personality"]; ok {
			personality = ""
			if len(values) > 0 {
				personality = values[0]
			}
		}
	}

	if !service.IsAvailablePersonalityKey(ctx, personality, h.customPersonalityRepo) {
		return c.String(http.StatusBadRequest, fmt.Sprintf("Unknown personality %q. Refresh Personality settings and choose an available option.", personality))
	}

	if h.settingsRepo != nil {
		if err := h.settingsRepo.Set(ctx, "personality", personality); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save personality")
		}
	}

	// Return re-rendered personality section for HTMX requests
	if isHTMX(c) {
		return h.renderPersonalitySection(c)
	}

	return c.String(http.StatusOK, "Personality saved successfully")
}
