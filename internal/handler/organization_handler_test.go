package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func organizationTestApp(t *testing.T, enabled bool) (*TestContext, *echo.Echo) {
	tc := NewStoreTestContext(t)
	cfg := auth.Config{Enabled: enabled, Username: "alice", Password: "alice-password-long", SessionSecret: "org-test-secret"}.Normalized()
	tc.handler.SetAuthConfig(cfg)
	s := service.NewOrganizationService(tc.stores.Organizations, cfg.SessionSecret)
	require.NoError(t, s.Bootstrap(t.Context(), cfg.Username, cfg.Password, enabled))
	tc.handler.SetOrganizationService(s)
	// Match production ordering: authenticate before route ownership checks.
	e := echo.New()
	e.Use(tc.handler.AuthMiddleware())
	tc.handler.RegisterRoutes(e)
	return tc, e
}
func orgRequest(e *echo.Echo, method, path string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://example.com"+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "http://example.com")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}
func orgCookie(t *testing.T, r *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range r.Result().Cookies() {
		if c.Name == auth.DefaultCookieName {
			return c
		}
	}
	t.Fatal("session cookie missing")
	return nil
}

func TestOrganizationHTTPJoinRolesPreferencesAndRemoval(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	login := orgRequest(e, http.MethodPost, "/login", url.Values{"username": {"alice"}, "password": {"alice-password-long"}}, nil)
	require.Equal(t, http.StatusFound, login.Code, login.Body.String())
	ownerCookie := orgCookie(t, login)
	require.True(t, ownerCookie.HttpOnly)
	invite := orgRequest(e, http.MethodPost, "/organization/invitations", url.Values{"role": {"member"}}, ownerCookie)
	require.Equal(t, http.StatusOK, invite.Code, invite.Body.String())
	require.Equal(t, "no-store", invite.Header().Get("Cache-Control"))
	match := regexp.MustCompile(`/invite\?token=([A-Za-z0-9_-]+)`).FindStringSubmatch(invite.Body.String())
	require.Len(t, match, 2)
	join := orgRequest(e, http.MethodPost, "/invite", url.Values{"token": {match[1]}, "username": {"bob"}, "password": {"bob-password-long"}}, nil)
	require.Equal(t, http.StatusSeeOther, join.Code, join.Body.String())
	bobCookie := orgCookie(t, join)
	again := orgRequest(e, http.MethodPost, "/invite", url.Values{"token": {match[1]}, "username": {"mallory"}, "password": {"mallory-password-long"}}, nil)
	require.Equal(t, http.StatusBadRequest, again.Code)
	me := orgRequest(e, http.MethodGet, "/auth/me", nil, bobCookie)
	require.Equal(t, http.StatusOK, me.Code)
	var identity struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	require.NoError(t, json.Unmarshal(me.Body.Bytes(), &identity))
	require.Equal(t, "member", identity.Role)
	denied := orgRequest(e, http.MethodPost, "/organization/invitations", url.Values{"role": {"admin"}}, bobCookie)
	require.Equal(t, http.StatusForbidden, denied.Code)
	denied = orgRequest(e, http.MethodPost, "/organization", url.Values{"name": {"hijacked"}}, bobCookie)
	require.Equal(t, http.StatusForbidden, denied.Code)
	for _, item := range []struct {
		cookie *http.Cookie
		theme  string
	}{{ownerCookie, "dark"}, {bobCookie, "light"}} {
		req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"theme":"`+item.theme+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(item.cookie)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	}
	bob, err := tc.stores.Organizations.UserByID(t.Context(), identity.UserID)
	require.NoError(t, err)
	ownPrefs, err := tc.stores.Organizations.Preferences(t.Context(), models.BootstrapUserID, []string{"ui.theme"})
	require.NoError(t, err)
	require.Equal(t, "dark", ownPrefs["ui.theme"])
	bobPrefs, err := tc.stores.Organizations.Preferences(t.Context(), bob.UserID, []string{"ui.theme"})
	require.NoError(t, err)
	require.Equal(t, "light", bobPrefs["ui.theme"])
	lastOwner := orgRequest(e, http.MethodPost, "/organization/members/"+models.BootstrapUserID, url.Values{"action": {"remove"}}, ownerCookie)
	require.Equal(t, http.StatusConflict, lastOwner.Code)
	remove := orgRequest(e, http.MethodPost, "/organization/members/"+bob.UserID, url.Values{"action": {"remove"}}, ownerCookie)
	require.Equal(t, http.StatusSeeOther, remove.Code, remove.Body.String())
	me = orgRequest(e, http.MethodGet, "/auth/me", nil, bobCookie)
	require.Equal(t, http.StatusUnauthorized, me.Code)
	logout := orgRequest(e, http.MethodPost, "/logout", nil, ownerCookie)
	require.Equal(t, http.StatusFound, logout.Code)
	me = orgRequest(e, http.MethodGet, "/auth/me", nil, ownerCookie)
	require.Equal(t, http.StatusUnauthorized, me.Code)
}

func TestOrganizationAuthOffAndCrossOrigin(t *testing.T) {
	_, e := organizationTestApp(t, false)
	page := orgRequest(e, http.MethodGet, "/organization", nil, nil)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "Authentication is off")
	invite := orgRequest(e, http.MethodPost, "/organization/invitations", url.Values{"role": {"member"}}, nil)
	require.Equal(t, http.StatusForbidden, invite.Code)
	_, e = organizationTestApp(t, true)
	req := httptest.NewRequest(http.MethodPost, "http://example.com/login", strings.NewReader("username=alice&password=alice-password-long"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestOrganizationMemberRemovalClosesWebSocket(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	s := tc.handler.organizationSvc
	invite, err := s.Invite(t.Context(), models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := s.Accept(t.Context(), invite, "socket-user", "socket-password-long", "")
	require.NoError(t, err)
	token, err := s.NewSession(t.Context(), member.UserID, time.Hour)
	require.NoError(t, err)
	server := httptest.NewServer(e)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var frame liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &frame))
	require.Equal(t, "ready", frame.Event)
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "live", ID: "project", ProjectID: "default"}))
	require.NoError(t, wsjson.Read(ctx, conn, &frame))
	require.Equal(t, "subscribed", frame.Event)
	ownerToken, err := s.NewSession(t.Context(), models.BootstrapUserID, time.Hour)
	require.NoError(t, err)
	rec := orgRequest(e, http.MethodPost, "/organization/members/"+member.UserID, url.Values{"action": {"remove"}}, &http.Cookie{Name: auth.DefaultCookieName, Value: ownerToken})
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Error(t, wsjson.Read(ctx, conn, &frame))
	require.NoError(t, ctx.Err(), "membership removal must close the socket before the test deadline")
}

func TestOrganizationHostedLogoutRevokesSignedCookie(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	h := tc.handler
	h.authMode = auth.AuthModeHostedSSO
	h.hostedSSOKey = []byte("0123456789abcdef0123456789abcdef")
	h.hostedSSOInstanceID = "instance-1"
	h.appBaseURL = "http://example.com"
	now := time.Now()
	claims := auth.SessionClaims{Version: 1, Subject: "owner-subject", Email: "owner@example.com", Display: "owner@example.com", InstanceID: "instance-1", AuthSource: auth.HostedAuthSource, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	token, err := auth.SignHostedSession(claims, h.hostedSSOKey)
	require.NoError(t, err)
	cookie := &http.Cookie{Name: auth.DefaultCookieName, Value: token}
	// A signature alone does not restore an old or revoked session.
	require.Equal(t, http.StatusUnauthorized, orgRequest(e, http.MethodGet, "/auth/me", nil, cookie).Code)
	require.NoError(t, h.organizationSvc.RegisterHostedSession(t.Context(), token, models.BootstrapUserID, claims.ExpiresAt))
	require.Equal(t, http.StatusOK, orgRequest(e, http.MethodGet, "/auth/me", nil, cookie).Code)
	require.Equal(t, http.StatusFound, orgRequest(e, http.MethodPost, "/logout", nil, cookie).Code)
	require.Equal(t, http.StatusUnauthorized, orgRequest(e, http.MethodGet, "/auth/me", nil, cookie).Code)
}

func TestOrganizationUsesAppShellAndHTMXFragments(t *testing.T) {
	_, e := organizationTestApp(t, false)
	full := orgRequest(e, http.MethodGet, "/organization", nil, nil)
	require.Equal(t, http.StatusOK, full.Code)
	require.Contains(t, full.Body.String(), `id="main-content"`)
	require.Contains(t, full.Body.String(), `data-nav-base="/organization"`)
	require.Contains(t, full.Body.String(), `hx-get="/organization?project_id=`)
	require.Contains(t, full.Body.String(), `id="organization-container"`)
	require.NotContains(t, full.Body.String(), "Back to OpenVibely")
	req := httptest.NewRequest(http.MethodGet, "http://example.com/organization", nil)
	req.Header.Set("HX-Request", "true")
	fragment := httptest.NewRecorder()
	e.ServeHTTP(fragment, req)
	require.Equal(t, http.StatusOK, fragment.Code)
	require.Contains(t, fragment.Body.String(), `id="organization-container"`)
	require.NotContains(t, fragment.Body.String(), "<html")
	require.NotContains(t, fragment.Body.String(), `id="main-content"`)
	require.Contains(t, fragment.Body.String(), `hx-post="/organization" hx-target="#main-content"`)
}
