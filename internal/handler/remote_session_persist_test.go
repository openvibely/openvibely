package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

func TestOrganizationSessionRenewsOnlyAfterHalfItsLife(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	login := orgRequest(e, http.MethodPost, "/login", url.Values{"username": {"alice"}, "password": {"alice-password-long"}}, nil)
	require.Equal(t, http.StatusFound, login.Code)
	cookie := orgCookie(t, login)
	org := tc.handler.organizationSvc
	user, err := org.Session(t.Context(), cookie.Value)
	require.NoError(t, err)
	issued := user.SessionExpiresAt

	fresh := orgRequest(e, http.MethodGet, "/auth/me", nil, cookie)
	require.Equal(t, http.StatusOK, fresh.Code)
	require.Empty(t, fresh.Result().Cookies(), "a fresh session is not re-sent")

	ttl := tc.handler.authCfg.Normalized().SessionTTL
	near := time.Now().Add(ttl/2 - time.Minute)
	short, err := org.NewSession(t.Context(), user.UserID, time.Until(near))
	require.NoError(t, err)
	cookie = &http.Cookie{Name: auth.DefaultCookieName, Value: short}
	user, err = org.Session(t.Context(), short)
	require.NoError(t, err)
	issued = user.SessionExpiresAt
	renewed := orgRequest(e, http.MethodGet, "/projects", nil, cookie)
	require.Less(t, renewed.Code, 500)
	again := orgCookie(t, renewed)
	require.Equal(t, cookie.Value, again.Value)
	require.Equal(t, int(ttl.Seconds()), again.MaxAge)
	user, err = org.Session(t.Context(), cookie.Value)
	require.NoError(t, err)
	require.Greater(t, user.SessionExpiresAt, issued)

	require.NoError(t, org.Logout(t.Context(), cookie.Value))
	revoked := orgRequest(e, http.MethodGet, "/auth/me", nil, cookie)
	require.Equal(t, http.StatusUnauthorized, revoked.Code)
}

// restartedHandler is a second handler on the same database, as after a restart.
func restartedHandler(t *testing.T, stores persistence.Stores) *Handler {
	t.Helper()
	hub := &remote.Hub{}
	env := wireTestHandler(t, remote.Wrap(stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	env.Handler.SetRemote(hub, nil)
	t.Cleanup(hub.Shutdown)
	return env.Handler
}

func connectSavedServer(t *testing.T, e *echo.Echo, h *Handler, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {target}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return savedServerID(t, h)
}

// setupPersistentHubHandler is setupHubHandler with stores a restart can reuse.
func setupPersistentHubHandler(t *testing.T) (*Handler, *echo.Echo, persistence.Stores) {
	t.Helper()
	stores := teststores.New(t).Stores
	hub := &remote.Hub{}
	env := wireTestHandler(t, remote.Wrap(stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	env.Handler.SetRemote(hub, nil)
	t.Cleanup(hub.Shutdown)
	return env.Handler, env.Echo, stores
}

func TestRestartRestoresSavedRemoteSession(t *testing.T) {
	h, e, stores := setupPersistentHubHandler(t)
	srv, revoked, _ := fakeRemoteOV(t, "secret")
	id := connectSavedServer(t, e, h, srv.URL)

	raw, err := h.settingsRepo.Get(context.Background(), remoteSessionsSettingKey)
	require.NoError(t, err)
	require.Contains(t, raw, `"value":"good"`)
	require.NotContains(t, raw, "secret")

	restarted := restartedHandler(t, stores)
	restarted.RestoreRemoteSessions(context.Background())
	require.Eventually(t, func() bool { return restarted.remoteSignedIn(context.Background(), id) }, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, "Alice", restarted.remoteSessions.get(models.BootstrapUserID, id).user)

	revoked.Store(true)
	again := restartedHandler(t, stores)
	again.RestoreRemoteSessions(context.Background())
	require.Nil(t, again.remoteSessions.get(models.BootstrapUserID, id))
	raw, err = h.settingsRepo.Get(context.Background(), remoteSessionsSettingKey)
	require.NoError(t, err)
	require.NotContains(t, raw, id, "a rejected session is forgotten")
}

func TestDisconnectAndServerChangesForgetSavedSession(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "secret")
	id := connectSavedServer(t, e, h, srv.URL)
	stored := func() string {
		raw, err := h.settingsRepo.Get(context.Background(), remoteSessionsSettingKey)
		require.NoError(t, err)
		return raw
	}
	require.Contains(t, stored(), id)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id+"/disconnect", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, stored(), id)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id+"/connect", url.Values{"password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, stored(), id)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/servers/"+id, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotContains(t, stored(), id)
}

func TestRestoreSkipsSessionForChangedServer(t *testing.T) {
	h, e, stores := setupPersistentHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "secret")
	id := connectSavedServer(t, e, h, srv.URL)
	servers, err := h.loadSavedServers(context.Background())
	require.NoError(t, err)
	servers[0].Username = "mallory"
	require.NoError(t, h.storeSavedServers(context.Background(), servers))

	restarted := restartedHandler(t, stores)
	restarted.RestoreRemoteSessions(context.Background())
	require.Nil(t, restarted.remoteSessions.get(models.BootstrapUserID, id))
}

func TestRemoteRenewalCookieIsSaved(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "secret")
	id := connectSavedServer(t, e, h, srv.URL)
	s := h.remoteSessions.get(models.BootstrapUserID, id)
	s.jar.SetCookies(s.base, []*http.Cookie{{Name: auth.DefaultCookieName, Value: "renewed", Path: "/", MaxAge: 3600}})
	raw, err := h.settingsRepo.Get(context.Background(), remoteSessionsSettingKey)
	require.NoError(t, err)
	require.Contains(t, raw, `"value":"renewed"`)
	require.Equal(t, int64(time.Hour), s.jar.lifetime.Load())
}

func TestSavedRemoteCookiesKeepTheirAttributes(t *testing.T) {
	base, _ := url.Parse("https://box.example")
	jar := newRemoteJar()
	expires := time.Now().Add(time.Hour).Truncate(time.Second)
	jar.SetCookies(base, []*http.Cookie{
		{Name: "session", Value: "good", Path: "/", Secure: true, HttpOnly: true, MaxAge: 3600},
		{Name: "flow", Value: "x", Path: "/auth/sso", Expires: expires},
		{Name: "gone", Value: "old", Path: "/", Expires: time.Now().Add(-time.Minute)},
	})
	stored := map[string]storedRemoteCookie{}
	for _, c := range jar.storedCookies() {
		stored[c.Name] = c
	}
	require.Len(t, stored, 2)
	session := stored["session"]
	require.True(t, session.Secure && session.HTTPOnly)
	require.Equal(t, "/", session.Path)
	require.WithinDuration(t, time.Now().Add(time.Hour), session.Expires, 5*time.Second)
	require.Equal(t, "/auth/sso", stored["flow"].Path)
	require.True(t, expires.Equal(stored["flow"].Expires))

	record := storedRemoteSession{Cookies: append(jar.storedCookies(), storedRemoteCookie{Name: "stale", Value: "v", Path: "/", Expires: time.Now().Add(-time.Second)})}
	restored := restoredRemoteSession(pages.SavedServer{URL: base.String()}, record)
	names := func(u string) []string {
		parsed, _ := url.Parse(u)
		var out []string
		for _, c := range restored.jar.Cookies(parsed) {
			out = append(out, c.Name)
		}
		return out
	}
	require.ElementsMatch(t, []string{"session"}, names("https://box.example/tasks"))
	require.ElementsMatch(t, []string{"session", "flow"}, names("https://box.example/auth/sso/start"))
	require.Empty(t, names("http://box.example/tasks"), "secure cookies stay off plain HTTP")
	require.Len(t, restored.jar.storedCookies(), 2, "expired cookies are not restored")

	restored.jar.SetCookies(base, []*http.Cookie{{Name: "session", Path: "/", MaxAge: -1}})
	require.Len(t, restored.jar.storedCookies(), 1, "a cleared cookie is forgotten")
}
