package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

type failingXSettingsAPI struct{ err error }

func (f failingXSettingsAPI) Me(context.Context) (service.XUser, error) {
	return service.XUser{}, f.err
}
func (f failingXSettingsAPI) Mentions(context.Context, string, string, string) (service.XMentionsResponse, error) {
	return service.XMentionsResponse{}, f.err
}
func (f failingXSettingsAPI) Post(context.Context, string, string) (string, error) {
	return "", f.err
}

type readyXSettingsAPI struct {
	accountID string
	newest    string
}

func (f *readyXSettingsAPI) Me(context.Context) (service.XUser, error) {
	accountID := f.accountID
	if accountID == "" {
		accountID = "bot"
	}
	return service.XUser{ID: accountID, Username: "openvibely"}, nil
}
func (f *readyXSettingsAPI) Mentions(context.Context, string, string, string) (service.XMentionsResponse, error) {
	var out service.XMentionsResponse
	out.Meta.NewestID = f.newest
	return out, nil
}
func (f *readyXSettingsAPI) Post(context.Context, string, string) (string, error) {
	return "tweet", nil
}

type cancelAwareXAPI struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (f *cancelAwareXAPI) Me(context.Context) (service.XUser, error) {
	return service.XUser{ID: "old", Username: "old"}, nil
}
func (f *cancelAwareXAPI) Mentions(ctx context.Context, _, _, _ string) (service.XMentionsResponse, error) {
	select {
	case <-f.started:
	default:
		close(f.started)
	}
	<-ctx.Done()
	close(f.cancelled)
	return service.XMentionsResponse{}, ctx.Err()
}
func (f *cancelAwareXAPI) Post(context.Context, string, string) (string, error) { return "", nil }

type blockingXSettingsAPI struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingXSettingsAPI) Me(context.Context) (service.XUser, error) {
	return service.XUser{ID: "old-account", Username: "old"}, nil
}
func (f *blockingXSettingsAPI) Mentions(context.Context, string, string, string) (service.XMentionsResponse, error) {
	f.once.Do(func() { close(f.started) })
	<-f.release
	var out service.XMentionsResponse
	out.Meta.NewestID = "90"
	return out, nil
}
func (f *blockingXSettingsAPI) Post(context.Context, string, string) (string, error) { return "", nil }

func xFormContext(e *echo.Echo, method, path string, values url.Values) echo.Context {
	req := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	return e.NewContext(req, httptest.NewRecorder())
}

func TestXConnectionTestReportsMentionReadFailureForConfiguredService(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	svc := service.NewXService(service.XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	svc.SetAPI(xMentionFailureAPI{})
	h.SetXService(svc)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(httptest.NewRequest(http.MethodPost, "/channels/x/test", nil), recorder)

	require.NoError(t, h.handleXTest(ctx))
	require.Contains(t, recorder.Body.String(), "Connection failed: verify X mention access")
	require.NotContains(t, recorder.Body.String(), "service not configured")
}

type xMentionFailureAPI struct{}

func (xMentionFailureAPI) Me(context.Context) (service.XUser, error) {
	return service.XUser{ID: "bot", Username: "openvibely"}, nil
}
func (xMentionFailureAPI) Mentions(context.Context, string, string, string) (service.XMentionsResponse, error) {
	return service.XMentionsResponse{}, errors.New("mention access revoked")
}
func (xMentionFailureAPI) Post(context.Context, string, string) (string, error) { return "", nil }

func TestXCredentialsBlankFieldsPreserveSavedSecrets(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	saved := map[string]string{
		service.XSettingConsumerKey:       "saved-consumer-key",
		service.XSettingConsumerSecret:    "saved-consumer-secret",
		service.XSettingAccessToken:       "saved-access-token",
		service.XSettingAccessTokenSecret: "saved-access-secret",
	}
	for key, value := range saved {
		require.NoError(t, h.settingsRepo.Set(ctx, key, value))
	}
	c := xFormContext(e, http.MethodPost, "/channels/x/configure", url.Values{
		"x_consumer_key":        {"new-consumer-key"},
		"x_consumer_secret":     {""},
		"x_access_token":        {"  "},
		"x_access_token_secret": {""},
	})
	credentials, err := h.xCredentials(ctx, c)
	require.NoError(t, err)
	require.Equal(t, "new-consumer-key", credentials.ConsumerKey)
	require.Equal(t, saved[service.XSettingConsumerSecret], credentials.ConsumerSecret)
	require.Equal(t, saved[service.XSettingAccessToken], credentials.AccessToken)
	require.Equal(t, saved[service.XSettingAccessTokenSecret], credentials.AccessTokenSecret)
}

func TestXConfigureProviderFailureDoesNotOverwriteSettingsOrStopExistingService(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	contexts := repository.NewXTaskContextRepo(db)
	receipts := repository.NewXInboundReceiptRepo(db)
	h.SetXRepositories(auth, selections, contexts, receipts)
	old := service.NewXService(service.XCredentials{ConsumerKey: "old-key", ConsumerSecret: "old-secret", AccessToken: "old-token", AccessTokenSecret: "old-token-secret"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	old.SetAPI(&readyXSettingsAPI{})
	old.SetRepositories(auth, selections, contexts, receipts, h.threadInputRepo)
	require.NoError(t, old.StartVerified(service.XUser{ID: "old", Username: "old"}))
	h.SetXService(old)
	t.Cleanup(old.Stop)
	require.NoError(t, h.settingsRepo.Set(ctx, service.XSettingConsumerKey, "old-key"))
	originalFactory := newXAPIClientForSettings
	newXAPIClientForSettings = func(service.XCredentials) service.XAPI {
		return failingXSettingsAPI{err: errors.New("access tier unavailable")}
	}
	t.Cleanup(func() { newXAPIClientForSettings = originalFactory })
	c := xFormContext(e, http.MethodPost, "/channels/x/configure", url.Values{
		"x_consumer_key":          {"new-key"},
		"x_consumer_secret":       {"new-secret"},
		"x_access_token":          {"new-token"},
		"x_access_token_secret":   {"new-token-secret"},
		"x_poll_interval_seconds": {"30"},
	})

	err := h.handleXConfigure(c)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
	stored, getErr := h.settingsRepo.Get(ctx, service.XSettingConsumerKey)
	require.NoError(t, getErr)
	require.Equal(t, "old-key", stored)
	stored, getErr = h.settingsRepo.Get(ctx, service.XSettingConsumerSecret)
	require.NoError(t, getErr)
	require.Empty(t, stored)
	require.Same(t, old, h.xService)
	require.True(t, old.Status().Running)
}

func TestXConfigureInitializesCursorAndCancelsReplacedService(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	authRepo := repository.NewXAuthRepo(db)
	selectionRepo := repository.NewXUserProjectRepo(db)
	contextRepo := repository.NewXTaskContextRepo(db)
	receiptRepo := repository.NewXInboundReceiptRepo(db)
	h.SetXRepositories(authRepo, selectionRepo, contextRepo, receiptRepo)
	oldAPI := &cancelAwareXAPI{started: make(chan struct{}), cancelled: make(chan struct{})}
	old := service.NewXService(service.XCredentials{ConsumerKey: "old-key", ConsumerSecret: "old-secret", AccessToken: "old-token", AccessTokenSecret: "old-token-secret"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	old.SetAPI(oldAPI)
	old.SetRepositories(authRepo, selectionRepo, contextRepo, receiptRepo, h.threadInputRepo)
	require.NoError(t, old.StartVerified(service.XUser{ID: "old", Username: "old"}))
	<-oldAPI.started
	h.SetXService(old)

	originalFactory := newXAPIClientForSettings
	newXAPIClientForSettings = func(service.XCredentials) service.XAPI { return &readyXSettingsAPI{newest: "99"} }
	t.Cleanup(func() {
		newXAPIClientForSettings = originalFactory
		if h.xService != nil {
			h.xService.Stop()
		}
	})
	c := xFormContext(e, http.MethodPost, "/channels/x/configure", url.Values{
		"x_consumer_key": {"new-key"}, "x_consumer_secret": {"new-secret"},
		"x_access_token": {"new-token"}, "x_access_token_secret": {"new-token-secret"},
		"x_poll_interval_seconds": {"30"}, "x_send_responses": {"true"},
	})
	require.NoError(t, h.handleXConfigure(c))
	select {
	case <-oldAPI.cancelled:
	case <-time.After(time.Second):
		t.Fatal("replaced X poller was not cancelled")
	}
	cursor, err := h.settingsRepo.Get(context.Background(), service.XSettingSinceID)
	require.NoError(t, err)
	require.Equal(t, "99", cursor)
	require.True(t, h.xService.Status().Running)
}

func TestXConfigurePreservesCursorForSameAccountAndBaselinesAccountChange(t *testing.T) {
	credentials := service.XCredentials{ConsumerKey: "key", ConsumerSecret: "secret", AccessToken: "token", AccessTokenSecret: "token-secret"}
	existing := map[string]string{
		service.XSettingConsumerKey: credentials.ConsumerKey, service.XSettingConsumerSecret: credentials.ConsumerSecret,
		service.XSettingAccessToken: credentials.AccessToken, service.XSettingAccessTokenSecret: credentials.AccessTokenSecret,
		service.XSettingAccountID: "bot", service.XSettingSinceID: "42",
	}
	require.Equal(t, "42", xCursorForConfiguration(existing, credentials, "bot", "99"))
	existing[service.XSettingSinceID] = ""
	require.Empty(t, xCursorForConfiguration(existing, credentials, "bot", "110"), "same-account edits must preserve an intentionally empty cursor")
	require.Equal(t, "120", xCursorForConfiguration(existing, credentials, "different-account", "120"), "account changes must establish a safe current baseline")
	delete(existing, service.XSettingAccountID)
	require.Empty(t, xCursorForConfiguration(existing, credentials, "bot", "130"), "legacy unchanged credentials must preserve an empty cursor")
}

func TestXReconfigurationFencesOldInFlightCursorWrite(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	contexts := repository.NewXTaskContextRepo(db)
	receipts := repository.NewXInboundReceiptRepo(db)
	h.SetXRepositories(auth, selections, contexts, receipts)
	ctx := context.Background()
	require.NoError(t, h.settingsRepo.SetMany(ctx, map[string]string{
		service.XSettingConsumerKey: "old-key", service.XSettingConsumerSecret: "old-secret",
		service.XSettingAccessToken: "old-token", service.XSettingAccessTokenSecret: "old-token-secret",
		service.XSettingAccountID: "old-account", service.XSettingSinceID: "10",
	}))
	oldAPI := &blockingXSettingsAPI{started: make(chan struct{}), release: make(chan struct{})}
	old := service.NewXService(service.XCredentials{ConsumerKey: "old-key", ConsumerSecret: "old-secret", AccessToken: "old-token", AccessTokenSecret: "old-token-secret"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	old.SetAPI(oldAPI)
	old.SetRepositories(auth, selections, contexts, receipts, h.threadInputRepo)
	require.NoError(t, old.StartVerified(service.XUser{ID: "old-account", Username: "old"}))
	<-oldAPI.started
	h.SetXService(old)

	originalFactory := newXAPIClientForSettings
	newXAPIClientForSettings = func(service.XCredentials) service.XAPI {
		return &readyXSettingsAPI{accountID: "new-account", newest: "200"}
	}
	t.Cleanup(func() {
		newXAPIClientForSettings = originalFactory
		h.StopXService()
	})
	form := url.Values{
		"x_consumer_key": {"new-key"}, "x_consumer_secret": {"new-secret"},
		"x_access_token": {"new-token"}, "x_access_token_secret": {"new-token-secret"},
		"x_poll_interval_seconds": {"30"},
	}
	done := make(chan error, 1)
	go func() { done <- h.handleXConfigure(xFormContext(e, http.MethodPost, "/channels/x/configure", form)) }()
	require.Eventually(t, func() bool {
		accountID, _ := h.settingsRepo.Get(ctx, service.XSettingAccountID)
		return accountID == "new-account"
	}, time.Second, 10*time.Millisecond)
	close(oldAPI.release)
	require.NoError(t, <-done)
	cursor, err := h.settingsRepo.Get(ctx, service.XSettingSinceID)
	require.NoError(t, err)
	require.Equal(t, "200", cursor)
}

func TestXStopServiceStopsDynamicallyInstalledPoller(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	api := &cancelAwareXAPI{started: make(chan struct{}), cancelled: make(chan struct{})}
	svc := service.NewXService(service.XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	svc.SetAPI(api)
	svc.SetRepositories(repository.NewXAuthRepo(db), repository.NewXUserProjectRepo(db), repository.NewXTaskContextRepo(db), repository.NewXInboundReceiptRepo(db), h.threadInputRepo)
	require.NoError(t, svc.StartVerified(service.XUser{ID: "dynamic", Username: "dynamic"}))
	<-api.started
	h.SetXService(svc)
	h.StopXService()
	select {
	case <-api.cancelled:
	case <-time.After(time.Second):
		t.Fatal("dynamic X service was not stopped")
	}
	require.Nil(t, h.getXService())
}

func TestXQueuedInputRuntimePreservesAuthorizedProjectSwitchPersistence(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	p1 := createProject(t, h, "Queued X One")
	p2 := createProject(t, h, "Queued X Two")
	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	contexts := repository.NewXTaskContextRepo(db)
	receipts := repository.NewXInboundReceiptRepo(db)
	require.NoError(t, auth.Create(context.Background(), &models.XAuthorizedUser{ProjectID: p1.ID, XUserID: "123"}))
	require.NoError(t, auth.Create(context.Background(), &models.XAuthorizedUser{ProjectID: p2.ID, XUserID: "123"}))
	h.SetXRepositories(auth, selections, contexts, receipts)
	input := models.ThreadInput{Source: models.TaskOriginX, ProjectID: p1.ID, XUserID: "123", XUsername: "alice", XConversationID: "conversation", XReplyToTweetID: "tweet"}

	runtime := h.xRuntimeToolsForThreadInput("promoted-task", input)
	require.NotNil(t, runtime)
	output, handled, isError, err := runtime.Executor(context.Background(), "switch_project", []byte(`{"project":"Queued X Two"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, "Queued X Two")
	selected, err := selections.GetUserProject(context.Background(), "123")
	require.NoError(t, err)
	require.Equal(t, p2.ID, selected)
}

func TestGenericChannelStatusReportsRunningButUnhealthyXAsNotConnected(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	project := createProject(t, h, "X Degraded Status")
	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	contexts := repository.NewXTaskContextRepo(db)
	receipts := repository.NewXInboundReceiptRepo(db)
	h.SetXRepositories(auth, selections, contexts, receipts)
	svc := service.NewXService(service.XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	svc.SetAPI(failingXSettingsAPI{err: errors.New("mention access revoked")})
	svc.SetRepositories(auth, selections, contexts, receipts, h.threadInputRepo)
	require.NoError(t, svc.StartVerified(service.XUser{ID: "bot", Username: "openvibely"}))
	t.Cleanup(svc.Stop)
	h.SetXService(svc)
	require.Eventually(t, func() bool { return !svc.Status().Connected && svc.Status().Running }, time.Second, 10*time.Millisecond)

	summary := h.buildChannelStatusSummary(context.Background(), project.ID)
	require.False(t, summary.X.Connected)
	require.True(t, summary.X.Running)
	require.Equal(t, "configured_not_connected", summary.X.Status)
	require.Contains(t, summary.X.LastError, "revoked")
}

func TestGenericChannelStatusRetainsXReadinessAndAuthorization(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	project := createProject(t, h, "X Status")
	auth := repository.NewXAuthRepo(db)
	selections := repository.NewXUserProjectRepo(db)
	contexts := repository.NewXTaskContextRepo(db)
	receipts := repository.NewXInboundReceiptRepo(db)
	h.SetXRepositories(auth, selections, contexts, receipts)
	require.NoError(t, auth.Create(context.Background(), &models.XAuthorizedUser{ProjectID: project.ID, XUserID: "123"}))
	svc := service.NewXService(service.XCredentials{ConsumerKey: "a", ConsumerSecret: "b", AccessToken: "c", AccessTokenSecret: "d"}, h.settingsRepo, h.projectRepo, h.llmConfigRepo, h.taskRepo, h.execRepo, h.scheduleRepo, h.taskSvc)
	svc.SetAPI(&readyXSettingsAPI{})
	svc.SetRepositories(auth, selections, contexts, receipts, h.threadInputRepo)
	require.NoError(t, svc.StartVerified(service.XUser{ID: "bot", Username: "openvibely"}))
	t.Cleanup(svc.Stop)
	h.SetXService(svc)

	summary := h.buildChannelStatusSummary(context.Background(), project.ID)
	require.True(t, summary.X.Configured)
	require.True(t, summary.X.Connected)
	require.True(t, summary.X.Running)
	require.Equal(t, "openvibely", summary.X.Username)
	require.Equal(t, 1, summary.X.AuthorizedUserCount)
}

func TestXAuthorizationCreateUsesExplicitFormProject(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	p1 := createProject(t, h, "X Explicit One")
	p2 := createProject(t, h, "X Explicit Two")
	auth := repository.NewXAuthRepo(db)
	h.SetXRepositories(auth, repository.NewXUserProjectRepo(db), repository.NewXTaskContextRepo(db), repository.NewXInboundReceiptRepo(db))
	ctx := xFormContext(e, http.MethodPost, "/channels/x/authorized-users", url.Values{
		"project_id": {p2.ID}, "x_user_id": {"123"}, "x_username": {"alice"},
	})

	require.NoError(t, h.AddXAuthorizedUser(ctx))
	authorizedOne, err := auth.IsAuthorized(context.Background(), p1.ID, "123")
	require.NoError(t, err)
	require.False(t, authorizedOne)
	authorizedTwo, err := auth.IsAuthorized(context.Background(), p2.ID, "123")
	require.NoError(t, err)
	require.True(t, authorizedTwo)
}

func TestXAuthorizationDeleteIsProjectScoped(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	p1 := createProject(t, h, "X One")
	p2 := createProject(t, h, "X Two")
	auth := repository.NewXAuthRepo(db)
	h.SetXRepositories(auth, repository.NewXUserProjectRepo(db), repository.NewXTaskContextRepo(db), repository.NewXInboundReceiptRepo(db))
	entry := &models.XAuthorizedUser{ProjectID: p1.ID, XUserID: "123", Username: "alice"}
	require.NoError(t, auth.Create(context.Background(), entry))

	c := xFormContext(e, http.MethodDelete, "/channels/x/authorized-users/"+entry.ID, url.Values{})
	c.SetPath("/channels/x/authorized-users/:id")
	c.SetParamNames("id")
	c.SetParamValues(entry.ID)
	c.QueryParams().Set("project_id", p2.ID)
	err := h.RemoveXAuthorizedUser(c)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusNotFound, httpErr.Code)
	authorized, checkErr := auth.IsAuthorized(context.Background(), p1.ID, "123")
	require.NoError(t, checkErr)
	require.True(t, authorized)
}
