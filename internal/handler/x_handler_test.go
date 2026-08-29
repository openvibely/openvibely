package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

func xFormContext(e *echo.Echo, method, path string, values url.Values) echo.Context {
	req := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	return e.NewContext(req, httptest.NewRecorder())
}

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

func TestXConfigureProviderFailureDoesNotOverwriteSettings(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	require.NoError(t, h.settingsRepo.Set(ctx, service.XSettingConsumerKey, "old-key"))
	originalFactory := newXAPIClientForSettings
	newXAPIClientForSettings = func(service.XCredentials) interface {
		Me(context.Context) (service.XUser, error)
	} {
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
