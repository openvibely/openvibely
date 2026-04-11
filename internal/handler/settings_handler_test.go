package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleTelegramTest_NotRunning tests the error feedback HTML
func TestHandleTelegramTest_NotRunning(t *testing.T) {
	e := echo.New()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	// telegramService is nil by default (not running)

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleTelegramTest(c)
	require.NoError(t, err)

	// Verify handler returns 200 OK with error HTML
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Verify error feedback HTML contains:
	// - Error styling (text-error class)
	// - Error icon (X SVG path)
	// - Error message
	// - No auto-dismiss script (errors should persist)
	assert.Contains(t, body, "text-error")
	assert.Contains(t, body, "Connection failed")
	assert.Contains(t, body, "Bot is not running")
	assert.Contains(t, body, "M6 18L18 6M6 6l12 12") // X SVG path
	assert.NotContains(t, body, "setTimeout")        // should NOT auto-dismiss
}

func TestHandleTelegramSaveHTMXRefreshesChannels(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.telegramService = &service.TelegramService{}
	origUpdateTelegramServiceToken := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = origUpdateTelegramServiceToken })
	updateTelegramServiceToken = func(svc *service.TelegramService, token string) error {
		return nil
	}

	form := url.Values{}
	form.Set("token", "test-token")

	rec := htmxPost(e, "/channels/telegram", form)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))
	assert.Empty(t, rec.Header().Get("Location"))

	token, err := h.settingsRepo.Get(context.Background(), "telegram_bot_token")
	require.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestHandleTelegramSaveErrorDoesNotRefreshOrRedirect(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.telegramService = &service.TelegramService{}
	origUpdateTelegramServiceToken := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = origUpdateTelegramServiceToken })
	updateTelegramServiceToken = func(svc *service.TelegramService, token string) error {
		return assert.AnError
	}

	form := url.Values{}
	form.Set("token", "test-token")

	rec := htmxPost(e, "/channels/telegram", form)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Get("HX-Refresh"))
	assert.Empty(t, rec.Header().Get("Location"))

	token, err := h.settingsRepo.Get(context.Background(), "telegram_bot_token")
	require.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestHandleTelegramSaveNonHTMXRedirectsToChannels(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.telegramService = &service.TelegramService{}
	origUpdateTelegramServiceToken := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = origUpdateTelegramServiceToken })
	updateTelegramServiceToken = func(svc *service.TelegramService, token string) error {
		return nil
	}

	form := url.Values{}
	form.Set("token", "")

	rec := postForm(e, "/channels/telegram", form)
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/channels", rec.Header().Get("Location"))

	token, err := h.settingsRepo.Get(context.Background(), "telegram_bot_token")
	require.NoError(t, err)
	assert.Equal(t, "", token)
}

func TestHandleTelegramRemoveHTMXRefreshesChannelsAndClearsSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	require.NoError(t, h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), "telegram_send_responses", "true"))

	h.telegramService = &service.TelegramService{}

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/remove", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "true", rec.Header().Get("HX-Refresh"))
	assert.Empty(t, rec.Header().Get("Location"))

	token, err := h.settingsRepo.Get(context.Background(), "telegram_bot_token")
	require.NoError(t, err)
	assert.Equal(t, "", token)
	sendResponses, err := h.settingsRepo.Get(context.Background(), "telegram_send_responses")
	require.NoError(t, err)
	assert.Equal(t, "", sendResponses)
}

func TestHandleTelegramRemoveNonHTMXRedirectsToChannelsAndClearsSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	require.NoError(t, h.settingsRepo.Set(context.Background(), "telegram_bot_token", "test-token"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), "telegram_send_responses", "true"))

	h.telegramService = &service.TelegramService{}

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/remove", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/channels", rec.Header().Get("Location"))

	token, err := h.settingsRepo.Get(context.Background(), "telegram_bot_token")
	require.NoError(t, err)
	assert.Equal(t, "", token)
	sendResponses, err := h.settingsRepo.Get(context.Background(), "telegram_send_responses")
	require.NoError(t, err)
	assert.Equal(t, "", sendResponses)
}

func TestHandleTelegramRemoveMissingSettingsRepoReturnsError(t *testing.T) {
	e := echo.New()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/remove", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleTelegramRemove(c)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusInternalServerError, httpErr.Code)
}
