package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

var newXAPIClientForSettings = func(credentials service.XCredentials) service.XAPI {
	return service.NewXAPIClient(credentials)
}

func (h *Handler) xCredentials(ctx context.Context, form echo.Context) (service.XCredentials, error) {
	if h.settingsRepo == nil {
		return service.XCredentials{}, fmt.Errorf("settings repository not configured")
	}
	keys := []string{service.XSettingConsumerKey, service.XSettingConsumerSecret, service.XSettingAccessToken, service.XSettingAccessTokenSecret}
	existing, err := h.settingsRepo.GetMany(ctx, keys)
	if err != nil {
		return service.XCredentials{}, err
	}
	value := func(formKey, settingKey string) string {
		v := strings.TrimSpace(form.FormValue(formKey))
		if v == "" {
			v = strings.TrimSpace(existing[settingKey])
		}
		return v
	}
	return service.XCredentials{ConsumerKey: value("x_consumer_key", service.XSettingConsumerKey), ConsumerSecret: value("x_consumer_secret", service.XSettingConsumerSecret), AccessToken: value("x_access_token", service.XSettingAccessToken), AccessTokenSecret: value("x_access_token_secret", service.XSettingAccessTokenSecret)}, nil
}

func (h *Handler) handleXConfigure(c echo.Context) error {
	h.xConfigMu.Lock()
	defer h.xConfigMu.Unlock()
	ctx := c.Request().Context()
	creds, err := h.xCredentials(ctx, c)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to load X settings")
	}
	if !creds.Ready() {
		return echo.NewHTTPError(http.StatusBadRequest, "All four X OAuth 1.0a credentials are required")
	}
	pollSeconds, err := strconv.Atoi(strings.TrimSpace(c.FormValue("x_poll_interval_seconds")))
	if err != nil || pollSeconds < 15 || pollSeconds > 300 {
		return echo.NewHTTPError(http.StatusBadRequest, "X poll interval must be between 15 and 300 seconds")
	}
	api := newXAPIClientForSettings(creds)
	svc := service.NewXService(creds, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	svc.SetAPI(api)
	svc.SetRepositories(h.xAuthRepo, h.xUserProjectRepo, h.xTaskContextRepo, h.xInboundReceiptRepo, h.threadInputRepo)
	svc.SetRuntime(h.agentRepo, h.customPersonalityRepo, h.chatBroadcaster, h.executionStreamHub, h.StartChannelChatRun, h.StartChannelTaskRun, h.PromoteQueuedChatInput, h.PromoteQueuedTaskThreadInput, h.channelMessageRouter)
	svc.SetPollInterval(time.Duration(pollSeconds) * time.Second)
	me, baselineCursor, err := svc.PrepareConnection(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "X connection failed: "+err.Error())
	}
	values := map[string]string{
		service.XSettingConsumerKey: creds.ConsumerKey, service.XSettingConsumerSecret: creds.ConsumerSecret,
		service.XSettingAccessToken: creds.AccessToken, service.XSettingAccessTokenSecret: creds.AccessTokenSecret,
		service.XSettingPollIntervalSeconds: strconv.Itoa(pollSeconds),
		service.XSettingSendResponses:       strconv.FormatBool(c.FormValue("x_send_responses") == "true"),
		service.XSettingSinceID:             baselineCursor,
	}
	if err := h.settingsRepo.SetMany(ctx, values); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to save X settings")
	}
	// PrepareConnection proved the only StartVerified preconditions before the
	// atomic settings commit, so activation performs no fallible provider call.
	if err := svc.StartVerified(me); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "X failed to activate: "+err.Error())
	}
	old := h.swapXService(svc)
	if h.channelMessageRouter != nil {
		h.channelMessageRouter.SetXService(svc)
	}
	if old != nil {
		old.Stop()
	}
	return returnToChannels(c)
}
func (h *Handler) handleXTest(c echo.Context) error {
	svc := h.getXService()
	if svc == nil {
		return renderStandardChannelConnectionTestFeedback(c, "X", false, fmt.Errorf("channel is not running"), channelConnectionTestFeedbackOptions{})
	}
	_, err := svc.TestConnection(c.Request().Context())
	return renderStandardChannelConnectionTestFeedback(c, "X", err == nil, err, channelConnectionTestFeedbackOptions{})
}
func (h *Handler) handleXRemove(c echo.Context) error {
	h.xConfigMu.Lock()
	defer h.xConfigMu.Unlock()
	if h.settingsRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "settings repository not configured")
	}
	values := map[string]string{
		service.XSettingConsumerKey: "", service.XSettingConsumerSecret: "", service.XSettingAccessToken: "",
		service.XSettingAccessTokenSecret: "", service.XSettingPollIntervalSeconds: "", service.XSettingSendResponses: "", service.XSettingSinceID: "",
	}
	if err := h.settingsRepo.SetMany(c.Request().Context(), values); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to remove X channel settings")
	}
	old := h.swapXService(nil)
	if h.channelMessageRouter != nil {
		h.channelMessageRouter.SetXService(nil)
	}
	if old != nil {
		old.Stop()
	}
	return returnToChannels(c)
}
func (h *Handler) AddXAuthorizedUser(c echo.Context) error {
	if h.xAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "X authorization repository not configured")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project is required")
	}
	userID := strings.TrimSpace(c.FormValue("x_user_id"))
	if userID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "X user ID is required")
	}
	if _, err := strconv.ParseUint(userID, 10, 64); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "X user ID must be numeric")
	}
	u := &models.XAuthorizedUser{ProjectID: projectID, XUserID: userID, Username: strings.TrimPrefix(strings.TrimSpace(c.FormValue("x_username")), "@")}
	if err := h.xAuthRepo.Create(c.Request().Context(), u); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return returnToChannels(c)
}
func (h *Handler) RemoveXAuthorizedUser(c echo.Context) error {
	if h.xAuthRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "X authorization repository not configured")
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project is required")
	}
	if err := h.xAuthRepo.Delete(c.Request().Context(), projectID, c.Param("id")); err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "X authorized user not found")
	}
	return returnToChannels(c)
}
