package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleTelegramTest_NotRunning(t *testing.T) {
	e := echo.New()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleTelegramTest(c)
	require.NoError(t, err)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Equal(t, `<div class="flex items-center gap-2 text-error" id="telegram-test-feedback"><span>Connection failed: Bot is not running</span></div>`, body)
	assert.NotContains(t, body, "setTimeout")
}

func TestHandleTelegramTest_Success(t *testing.T) {
	e := echo.New()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	h.telegramService = &service.TelegramService{}

	origIsTelegramServiceRunning := isTelegramServiceRunning
	t.Cleanup(func() { isTelegramServiceRunning = origIsTelegramServiceRunning })
	isTelegramServiceRunning = func(svc *service.TelegramService) bool {
		return svc == h.telegramService
	}

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/test", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleTelegramTest(c)
	require.NoError(t, err)

	body := rec.Body.String()
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, body, `<div class="flex items-center gap-2 text-success" id="telegram-test-feedback"><span>Connection successful!</span></div>`)
	assert.Contains(t, body, "setTimeout")
	assert.Contains(t, body, "document.getElementById('telegram-test-feedback')")
}

func TestRenderStandardChannelConnectionTestFeedbackPreservesDefaultFragmentsAndEscaping(t *testing.T) {
	tests := []struct {
		name              string
		channelName       string
		serviceConfigured bool
		testErr           error
		want              string
	}{
		{
			name:              "slack missing service",
			channelName:       "Slack",
			serviceConfigured: false,
			want:              `<div class="flex items-center gap-2 text-error"><span>Slack service not configured</span></div>`,
		},
		{
			name:              "discord failure escapes service error",
			channelName:       "Discord",
			serviceConfigured: true,
			testErr:           errors.New(`bad <token> & "quoted"`),
			want:              `<div class="flex items-center gap-2 text-error"><span>Connection failed: bad &lt;token&gt; &amp; &quot;quoted&quot;</span></div>`,
		},
		{
			name:              "email success",
			channelName:       "Email",
			serviceConfigured: true,
			want:              `<div class="flex items-center gap-2 text-success"><span>Connection successful!</span></div>`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/channels/test", nil)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := renderStandardChannelConnectionTestFeedback(c, tc.channelName, tc.serviceConfigured, tc.testErr)
			require.NoError(t, err)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tc.want, rec.Body.String())
		})
	}
}

func TestRenderStandardChannelConnectionTestFeedbackSupportsTelegramOptions(t *testing.T) {
	options := channelConnectionTestFeedbackOptions{
		ElementID:              "telegram-test-feedback",
		MissingServiceMessage:  "Connection failed: Bot is not running",
		AutoDismissOnSuccess:   true,
		AutoDismissElementID:   "telegram-test-feedback",
		AutoDismissDelayMillis: 3000,
	}

	t.Run("missing service keeps Telegram copy and id", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/channels/telegram/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := renderStandardChannelConnectionTestFeedbackWithOptions(c, "Telegram", false, nil, options)
		require.NoError(t, err)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, `<div class="flex items-center gap-2 text-error" id="telegram-test-feedback"><span>Connection failed: Bot is not running</span></div>`, rec.Body.String())
	})

	t.Run("success keeps Telegram id and auto dismiss", func(t *testing.T) {
		e := echo.New()
		req := httptest.NewRequest(http.MethodPost, "/channels/telegram/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)

		err := renderStandardChannelConnectionTestFeedbackWithOptions(c, "Telegram", true, nil, options)
		require.NoError(t, err)

		body := rec.Body.String()
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, body, `<div class="flex items-center gap-2 text-success" id="telegram-test-feedback"><span>Connection successful!</span></div>`)
		assert.Contains(t, body, "setTimeout")
		assert.Contains(t, body, "document.getElementById('telegram-test-feedback')")
	})
}

func assertChannelsRefreshTrigger(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, channelsRefreshTrigger, rec.Header().Get("HX-Trigger"))
	assert.Empty(t, rec.Header().Get("HX-Refresh"))
	assert.Empty(t, rec.Header().Get("Location"))
}

func TestReturnToChannelsHTMXTriggersChannelsRefresh(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/channels/github/configure", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, returnToChannels(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assertChannelsRefreshTrigger(t, rec)
}

func TestReturnToChannelsNonHTMXRedirectsToChannels(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/channels/github/configure", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, returnToChannels(c))
	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/channels", rec.Header().Get("Location"))
	assert.Empty(t, rec.Header().Get("HX-Trigger"))
	assert.Empty(t, rec.Header().Get("HX-Refresh"))
}

func TestHandleTelegramSaveHTMXTriggersChannelsRefresh(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.telegramService = &service.TelegramService{}
	origUpdateTelegramServiceToken := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = origUpdateTelegramServiceToken })
	updateTelegramServiceToken = func(svc *service.TelegramService, token string) error {
		return nil
	}

	form := url.Values{}
	form.Set("token", "test-token")
	form.Set("telegram_rich_messages_v2", "true")

	rec := htmxPost(e, "/channels/telegram", form)
	assert.Equal(t, http.StatusOK, rec.Code)
	assertChannelsRefreshTrigger(t, rec)

	token, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingBotToken)
	require.NoError(t, err)
	assert.Equal(t, "test-token", token)
	richMessages, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingRichMessagesV2)
	require.NoError(t, err)
	assert.Equal(t, "true", richMessages)
}

func TestChannelCoreHTMXMutationsTriggerInPlaceChannelsRefresh(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.telegramService = &service.TelegramService{}
	origUpdateTelegramServiceToken := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = origUpdateTelegramServiceToken })
	updateTelegramServiceToken = func(svc *service.TelegramService, token string) error { return nil }

	base := func() url.Values {
		return url.Values{}
	}
	tests := []struct {
		name string
		path string
		form func() url.Values
	}{
		{
			name: "telegram save",
			path: "/channels/telegram",
			form: func() url.Values {
				form := base()
				form.Set("token", "test-token")
				form.Set("telegram_rich_messages_v2", "true")
				return form
			},
		},
		{
			name: "telegram remove",
			path: "/channels/telegram/remove",
			form: base,
		},
		{
			name: "github configure",
			path: "/channels/github/configure",
			form: func() url.Values {
				form := base()
				form.Set("github_auth_mode", service.GitHubAuthModePAT)
				form.Set("github_pat", "ghp_test_token")
				return form
			},
		},
		{
			name: "github remove",
			path: "/channels/github/remove",
			form: base,
		},
		{
			name: "slack configure",
			path: "/channels/slack/configure",
			form: func() url.Values {
				form := base()
				form.Set("slack_client_id", "cid")
				form.Set("slack_client_secret", "secret")
				form.Set("slack_app_token", "xapp-token")
				form.Set("slack_bot_token_mode", service.SlackBotTokenSourceOAuth)
				return form
			},
		},
		{
			name: "slack remove",
			path: "/channels/slack/remove",
			form: base,
		},
		{
			name: "discord configure",
			path: "/channels/discord/configure",
			form: func() url.Values {
				form := base()
				form.Set("discord_bot_token", "discord-token")
				return form
			},
		},
		{
			name: "discord remove",
			path: "/channels/discord/remove",
			form: base,
		},
		{
			name: "email configure",
			path: "/channels/email/configure",
			form: func() url.Values {
				form := base()
				form.Set("email_provider", service.EmailProviderCustom)
				form.Set("email_address", "bot@example.com")
				form.Set("email_password", "app-password")
				form.Set("email_imap_host", "imap.example.com")
				form.Set("email_imap_port", "993")
				form.Set("email_smtp_host", "smtp.example.com")
				form.Set("email_smtp_port", "587")
				return form
			},
		},
		{
			name: "email remove",
			path: "/channels/email/remove",
			form: base,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := htmxPost(e, tc.path, tc.form())
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assertChannelsRefreshTrigger(t, rec)
		})
	}
}

func TestHandleTelegramSaveStoresRichMessagesFalseWhenUnchecked(t *testing.T) {
	h, e, _ := setupTestHandler(t)

	h.telegramService = &service.TelegramService{}
	origUpdateTelegramServiceToken := updateTelegramServiceToken
	t.Cleanup(func() { updateTelegramServiceToken = origUpdateTelegramServiceToken })
	updateTelegramServiceToken = func(svc *service.TelegramService, token string) error { return nil }

	form := url.Values{}
	form.Set("token", "test-token")

	rec := htmxPost(e, "/channels/telegram", form)
	assert.Equal(t, http.StatusOK, rec.Code)
	richMessages, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingRichMessagesV2)
	require.NoError(t, err)
	assert.Equal(t, "false", richMessages)
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

	token, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingBotToken)
	require.NoError(t, err)
	assert.Equal(t, "test-token", token)
}

func TestHandleTelegramSaveNewServiceWiresSharedRunner(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetAgentRepo(repository.NewAgentRepo(db))

	createdSvc := &service.TelegramService{}
	origNewTelegramService := newTelegramService
	t.Cleanup(func() { newTelegramService = origNewTelegramService })
	newTelegramService = func(
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
		return createdSvc, nil
	}

	form := url.Values{}
	form.Set("token", "test-token")

	rec := htmxPost(e, "/channels/telegram", form)
	assert.Equal(t, http.StatusOK, rec.Code)
	require.Same(t, createdSvc, h.telegramService)
	assert.True(t, createdSvc.HasChannelChatRunner(), "settings-created Telegram service must use shared steering-aware runner")
	assert.True(t, createdSvc.HasAgentRepo(), "settings-created Telegram service must expose agent definitions in chat context")
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

	token, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingBotToken)
	require.NoError(t, err)
	assert.Equal(t, "", token)
}

func TestHandleTelegramRemoveHTMXTriggersChannelsRefreshAndClearsSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	require.NoError(t, h.settingsRepo.Set(context.Background(), service.TelegramSettingBotToken, "test-token"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), service.TelegramSettingSendResponses, "true"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), service.TelegramSettingRichMessagesV2, "false"))

	h.telegramService = &service.TelegramService{}

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/remove", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assertChannelsRefreshTrigger(t, rec)

	token, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingBotToken)
	require.NoError(t, err)
	assert.Equal(t, "", token)
	sendResponses, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingSendResponses)
	require.NoError(t, err)
	assert.Equal(t, "", sendResponses)
	richMessages, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingRichMessagesV2)
	require.NoError(t, err)
	assert.Equal(t, "", richMessages)
}

func TestHandleTelegramRemoveNonHTMXRedirectsToChannelsAndClearsSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	require.NoError(t, h.settingsRepo.Set(context.Background(), service.TelegramSettingBotToken, "test-token"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), service.TelegramSettingSendResponses, "true"))
	require.NoError(t, h.settingsRepo.Set(context.Background(), service.TelegramSettingRichMessagesV2, "false"))

	h.telegramService = &service.TelegramService{}

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/remove", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusSeeOther, rec.Code)
	assert.Equal(t, "/channels", rec.Header().Get("Location"))

	token, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingBotToken)
	require.NoError(t, err)
	assert.Equal(t, "", token)
	sendResponses, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingSendResponses)
	require.NoError(t, err)
	assert.Equal(t, "", sendResponses)
	richMessages, err := h.settingsRepo.Get(context.Background(), service.TelegramSettingRichMessagesV2)
	require.NoError(t, err)
	assert.Equal(t, "", richMessages)
}

func TestHandleTelegramRemoveMissingSettingsRepoReturnsError(t *testing.T) {
	e := echo.New()
	h := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/remove", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.handleTelegramRemove(c)
	require.Error(t, err)

	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusInternalServerError, httpErr.Code)
}

func TestChannelRemovalResetsAllSettings(t *testing.T) {
	tests := []struct {
		name             string
		path             string
		resets           map[string]string
		configureHandler func(*Handler)
	}{
		{
			name: "telegram",
			path: "/channels/telegram/remove",
			resets: map[string]string{
				service.TelegramSettingBotToken:       "",
				service.TelegramSettingSendResponses:  "",
				service.TelegramSettingRichMessagesV2: "",
			},
		},
		{
			name: "github",
			path: "/channels/github/remove",
			resets: map[string]string{
				service.GitHubSettingAppID:         "",
				service.GitHubSettingAppSlug:       "",
				service.GitHubSettingAppPrivateKey: "",
				service.GitHubSettingPAT:           "",
				service.GitHubSettingPATUserLogin:  "",
				service.GitHubSettingAuthMode:      "",
			},
		},
		{
			name: "slack",
			path: "/channels/slack/remove",
			resets: map[string]string{
				service.SlackSettingClientID:         "",
				service.SlackSettingClientSecret:     "",
				service.SlackSettingAppToken:         "",
				service.SlackSettingBotToken:         "",
				service.SlackSettingBotTokenOverride: "",
				service.SlackSettingBotTokenSource:   service.SlackBotTokenSourceOAuth,
				service.SlackSettingBotUserID:        "",
				service.SlackSettingTeamID:           "",
				service.SlackSettingTeamName:         "",
				service.SlackSettingConnectedAt:      "",
				service.SlackSettingOAuthState:       "",
				service.SlackSettingSendResponses:    "",
			},
		},
		{
			name: "discord",
			path: "/channels/discord/remove",
			resets: map[string]string{
				service.DiscordSettingBotToken:      "",
				service.DiscordSettingBotUserID:     "",
				service.DiscordSettingSendResponses: "",
			},
			configureHandler: func(h *Handler) {
				h.SetDiscordService(&fakeDiscordService{disconnectFn: func(context.Context) error { return nil }})
			},
		},
		{
			name: "discord service unavailable fallback",
			path: "/channels/discord/remove",
			resets: map[string]string{
				service.DiscordSettingBotToken:      "",
				service.DiscordSettingBotUserID:     "",
				service.DiscordSettingSendResponses: "",
			},
		},
		{
			name: "email",
			path: "/channels/email/remove",
			resets: map[string]string{
				service.EmailSettingProvider:                "",
				service.EmailSettingAddress:                 "",
				service.EmailSettingPassword:                "",
				service.EmailSettingIMAPHost:                "",
				service.EmailSettingIMAPPort:                "",
				service.EmailSettingSMTPHost:                "",
				service.EmailSettingSMTPPort:                "",
				service.EmailSettingPollIntervalSeconds:     "",
				service.EmailSettingSendResponses:           "",
				service.EmailSettingSkipAttachments:         "",
				service.EmailSettingMarkExistingSeenOnStart: "",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			if tc.configureHandler != nil {
				tc.configureHandler(h)
			}
			for key := range tc.resets {
				require.NoError(t, h.settingsRepo.Set(context.Background(), key, "configured"))
			}

			rec := htmxPost(e, tc.path, url.Values{})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assertChannelsRefreshTrigger(t, rec)
			for key, expected := range tc.resets {
				actual, err := h.settingsRepo.Get(context.Background(), key)
				require.NoError(t, err)
				assert.Equal(t, expected, actual, key)
			}
		})
	}
}

func TestChannelRemovalPreservesServiceLifecycleBehavior(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		setup func(h *Handler) func() bool
	}{
		{
			name: "github disconnect",
			path: "/channels/github/remove",
			setup: func(h *Handler) func() bool {
				called := false
				h.SetGitHubService(&fakeGitHubService{disconnectFn: func(context.Context) error {
					called = true
					return nil
				}})
				return func() bool { return called }
			},
		},
		{
			name: "slack disconnect",
			path: "/channels/slack/remove",
			setup: func(h *Handler) func() bool {
				called := false
				h.SetSlackService(&fakeSlackService{disconnectFn: func(context.Context) error {
					called = true
					return nil
				}})
				return func() bool { return called }
			},
		},
		{
			name: "discord disconnect",
			path: "/channels/discord/remove",
			setup: func(h *Handler) func() bool {
				called := false
				h.SetDiscordService(&fakeDiscordService{disconnectFn: func(context.Context) error {
					called = true
					return nil
				}})
				return func() bool { return called }
			},
		},
		{
			name: "email stop",
			path: "/channels/email/remove",
			setup: func(h *Handler) func() bool {
				svc := &removalEmailService{}
				h.SetEmailService(svc)
				return func() bool { return svc.stopped }
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			wasCalled := tc.setup(h)
			rec := htmxPost(e, tc.path, url.Values{})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.True(t, wasCalled())
		})
	}
}

type removalEmailService struct {
	EmailServiceProvider
	stopped bool
}

func (s *removalEmailService) Stop() {
	s.stopped = true
}

func TestChannelRemovalSettingFailureDoesNotReportSuccess(t *testing.T) {
	tests := []struct {
		name             string
		path             string
		firstKey         string
		failKey          string
		configureHandler func(*Handler)
	}{
		{name: "telegram", path: "/channels/telegram/remove", firstKey: service.TelegramSettingBotToken, failKey: service.TelegramSettingSendResponses},
		{name: "github", path: "/channels/github/remove", firstKey: service.GitHubSettingAppID, failKey: service.GitHubSettingAppSlug},
		{name: "slack", path: "/channels/slack/remove", firstKey: service.SlackSettingClientID, failKey: service.SlackSettingClientSecret},
		{
			name:     "discord",
			path:     "/channels/discord/remove",
			firstKey: service.DiscordSettingBotToken,
			failKey:  service.DiscordSettingBotUserID,
			configureHandler: func(h *Handler) {
				h.SetDiscordService(&fakeDiscordService{disconnectFn: func(context.Context) error { return nil }})
			},
		},
		{name: "discord fallback", path: "/channels/discord/remove", firstKey: service.DiscordSettingBotToken, failKey: service.DiscordSettingBotUserID},
		{name: "email", path: "/channels/email/remove", firstKey: service.EmailSettingProvider, failKey: service.EmailSettingAddress},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			if tc.configureHandler != nil {
				tc.configureHandler(h)
			}
			require.NoError(t, h.settingsRepo.Set(context.Background(), tc.firstKey, "configured"))
			require.NoError(t, h.settingsRepo.Set(context.Background(), tc.failKey, "configured"))
			trigger := fmt.Sprintf(`CREATE TRIGGER fail_channel_setting_reset
				BEFORE UPDATE ON app_settings
				WHEN NEW.key = '%s' AND NEW.value = ''
				BEGIN SELECT RAISE(FAIL, 'injected settings write failure'); END`, tc.failKey)
			require.NoError(t, func() error { _, err := db.Exec(trigger); return err }())

			for _, htmx := range []bool{true, false} {
				mode := "redirect"
				if htmx {
					mode = "htmx"
				}
				t.Run(mode, func(t *testing.T) {
					require.NoError(t, h.settingsRepo.Set(context.Background(), tc.firstKey, "configured"))
					require.NoError(t, h.settingsRepo.Set(context.Background(), tc.failKey, "configured"))

					req := httptest.NewRequest(http.MethodPost, tc.path, nil)
					if htmx {
						req.Header.Set("HX-Request", "true")
					}
					rec := httptest.NewRecorder()
					e.ServeHTTP(rec, req)

					assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
					assert.Empty(t, rec.Header().Get("HX-Trigger"))
					assert.Empty(t, rec.Header().Get("Location"))
					firstValue, err := h.settingsRepo.Get(context.Background(), tc.firstKey)
					require.NoError(t, err)
					assert.Empty(t, firstValue, "the injected failure should happen after a partial cleanup")
					failedValue, err := h.settingsRepo.Get(context.Background(), tc.failKey)
					require.NoError(t, err)
					assert.Equal(t, "configured", failedValue)
				})
			}
		})
	}
}
