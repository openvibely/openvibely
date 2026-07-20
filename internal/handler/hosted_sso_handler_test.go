package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
)

var hostedHandlerKey = []byte("0123456789abcdef0123456789abcdef")

func canonicalHostedValue(fill string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat(fill, 32)))
}

func hostedAuthTestHandler(t *testing.T, provider http.Handler) (*Handler, *echo.Echo, *auth.PendingStore) {
	t.Helper()
	if provider == nil {
		provider = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"sub":"subject","email":"user@example.com","email_verified":true,"instance_id":"instance-1","instance_slug":"alice","instance_host":"alice.openvibely.ai"}`)
		})
	}
	providerServer := httptest.NewServer(provider)
	t.Cleanup(providerServer.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	store := auth.NewPendingStore(ctx, time.Now)
	t.Cleanup(store.Close)
	h, _, _ := setupTestHandler(t)
	client := auth.NewHostedSSOClient(providerServer.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	h.SetHostedSSO(client, store, hostedHandlerKey, "instance-1", "https://alice.openvibely.ai")
	e := echo.New()
	e.Use(h.AuthMiddleware())
	h.RegisterRoutes(e)
	return h, e, store
}

func TestHostedSSOStartAndCallbackCreateWorkspaceSession(t *testing.T) {
	var tokenForm url.Values
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sso/token" {
			http.NotFound(w, r)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		tokenForm = r.Form
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sub":"subject","email":"user@example.com","email_verified":true,"instance_id":"instance-1","instance_slug":"alice","instance_host":"alice.openvibely.ai"}`)
	})
	_, e, _ := hostedAuthTestHandler(t, provider)
	destination := "/projects?tab=active"
	startReq := httptest.NewRequest(http.MethodGet, auth.HostedSSOStartURL(destination), nil)
	startRec := httptest.NewRecorder()
	e.ServeHTTP(startRec, startReq)
	if startRec.Code != http.StatusFound {
		t.Fatalf("start status=%d body=%s", startRec.Code, startRec.Body.String())
	}
	location, err := url.Parse(startRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	state := location.Query().Get("state")
	if location.Path != "/sso/authorize" || !strings.HasPrefix(location.Query().Get("redirect_uri"), "https://alice.openvibely.ai/") || len(state) != 43 {
		t.Fatalf("unexpected authorize URL %q", location.String())
	}
	var binding *http.Cookie
	for _, cookie := range startRec.Result().Cookies() {
		if cookie.Name == "ov_sso_browser" {
			binding = cookie
		}
	}
	if binding == nil || binding.Path != "/auth/sso" || !binding.HttpOnly || !binding.Secure || binding.SameSite != http.SameSiteLaxMode || binding.MaxAge != 600 {
		t.Fatalf("unexpected browser cookie %#v", binding)
	}

	code := canonicalHostedValue("c")
	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?code="+code+"&state="+state, nil)
	callbackReq.AddCookie(binding)
	callbackRec := httptest.NewRecorder()
	e.ServeHTTP(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusFound || callbackRec.Header().Get("Location") != destination {
		t.Fatalf("callback status=%d location=%q body=%s", callbackRec.Code, callbackRec.Header().Get("Location"), callbackRec.Body.String())
	}
	if tokenForm.Get("code") != code || tokenForm.Get("client_id") != "instance-1" || tokenForm.Get("redirect_uri") != "https://alice.openvibely.ai/auth/sso/callback" || len(tokenForm.Get("code_verifier")) != 43 {
		t.Fatalf("unexpected token form %#v", tokenForm)
	}
	var session *http.Cookie
	var bindingDeletion *http.Cookie
	for _, cookie := range callbackRec.Result().Cookies() {
		switch cookie.Name {
		case auth.DefaultCookieName:
			session = cookie
		case "ov_sso_browser":
			bindingDeletion = cookie
		}
	}
	if session == nil || !session.Secure || session.Domain != "" || session.Path != "/" || session.MaxAge != 3600 {
		t.Fatalf("unexpected hosted session cookie %#v", session)
	}
	claims, err := auth.VerifyHostedSession(session.Value, hostedHandlerKey, "instance-1", time.Now())
	if err != nil || claims.Email != "user@example.com" || claims.Display != claims.Email {
		t.Fatalf("claims=%#v err=%v", claims, err)
	}
	if bindingDeletion == nil || bindingDeletion.MaxAge != -1 || bindingDeletion.Path != "/auth/sso" || !bindingDeletion.Secure {
		t.Fatalf("unexpected binding deletion %#v", bindingDeletion)
	}
}

func TestHostedValidSessionBypassesNewTransactions(t *testing.T) {
	_, e, store := hostedAuthTestHandler(t, nil)
	now := time.Now()
	claims := auth.SessionClaims{Version: 1, Subject: "subject", Email: "user@example.com", Display: "user@example.com", InstanceID: "instance-1", AuthSource: auth.HostedAuthSource, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	token, err := auth.SignHostedSession(claims, hostedHandlerKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{auth.HostedSSOStartURL("/projects?tab=active"), "/login?next=" + base64.RawURLEncoding.EncodeToString([]byte("/projects?tab=active"))} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token})
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/projects?tab=active" || store.Count() != 0 {
			t.Fatalf("target=%q status=%d location=%q count=%d", target, rec.Code, rec.Header().Get("Location"), store.Count())
		}
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == "ov_sso_browser" {
				t.Fatal("valid session refreshed browser binding")
			}
		}
	}
}

func TestHostedErrorCallbackConsumesOnlyMatchingTransaction(t *testing.T) {
	var providerCalls int
	provider := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":"server_error"}`)
	})
	_, e, store := hostedAuthTestHandler(t, provider)
	start := func(binding *http.Cookie, destination string) (*http.Cookie, string) {
		req := httptest.NewRequest(http.MethodGet, auth.HostedSSOStartURL(destination), nil)
		if binding != nil {
			req.AddCookie(binding)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("start status=%d", rec.Code)
		}
		location, _ := url.Parse(rec.Header().Get("Location"))
		var refreshed *http.Cookie
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Name == "ov_sso_browser" {
				refreshed = cookie
			}
		}
		return refreshed, location.Query().Get("state")
	}
	binding, firstState := start(nil, "/first")
	binding, secondState := start(binding, "/second")
	if store.Count() != 2 {
		t.Fatalf("pending count=%d", store.Count())
	}
	callback := func(state string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?error=access_denied&state="+state+"&error_description=not+displayed", nil)
		req.AddCookie(binding)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	firstRec := callback(firstState)
	if firstRec.Code != http.StatusBadRequest || strings.Contains(firstRec.Body.String(), "not displayed") || store.Count() != 1 {
		t.Fatalf("first callback status=%d body=%s count=%d", firstRec.Code, firstRec.Body.String(), store.Count())
	}
	for _, cookie := range firstRec.Result().Cookies() {
		if cookie.Name == "ov_sso_browser" && cookie.MaxAge < 0 {
			t.Fatal("first callback prematurely deleted shared binding")
		}
	}
	secondRec := callback(secondState)
	if secondRec.Code != http.StatusBadRequest || store.Count() != 0 || providerCalls != 0 {
		t.Fatalf("second callback status=%d count=%d providerCalls=%d", secondRec.Code, store.Count(), providerCalls)
	}
	foundDeletion := false
	for _, cookie := range secondRec.Result().Cookies() {
		if cookie.Name == "ov_sso_browser" && cookie.MaxAge == -1 {
			foundDeletion = true
		}
	}
	if !foundDeletion {
		t.Fatal("final callback did not delete browser binding")
	}
}

func TestHostedCookiesUseConfiguredExternalScheme(t *testing.T) {
	h, _, _ := hostedAuthTestHandler(t, nil)
	h.SetAppBaseURL("http://127.0.0.1:3001")
	if h.hostedBrowserCookie("value").Secure || h.hostedSessionCookie("value", time.Now()).Secure || h.hostedSessionDeletionCookie().Secure || h.clearHostedBrowserCookie().Secure {
		t.Fatal("permitted HTTP loopback cookies were marked Secure")
	}
	h.SetAppBaseURL("https://alice.openvibely.ai")
	if !h.hostedBrowserCookie("value").Secure || !h.hostedSessionCookie("value", time.Now()).Secure || !h.hostedSessionDeletionCookie().Secure || !h.clearHostedBrowserCookie().Secure {
		t.Fatal("HTTPS external-origin cookies were not marked Secure")
	}
}

func TestHostedAbsoluteURLsUseInjectedCanonicalOrigin(t *testing.T) {
	t.Setenv("APP_BASE_URL", "https://attacker.example")
	h := &Handler{authMode: auth.AuthModeHostedSSO, appBaseURL: "https://alice.openvibely.ai"}
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "http://backend.internal/path", nil)
	req.Host = "backend.internal"
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("X-Forwarded-Host", "spoofed.example")
	ctx := e.NewContext(req, httptest.NewRecorder())
	if got := h.buildAbsoluteURL(ctx, "/channels/slack/callback"); got != "https://alice.openvibely.ai/channels/slack/callback" {
		t.Fatalf("absolute URL=%q", got)
	}
	if got := h.configuredAppBaseURL(); got != "https://alice.openvibely.ai" {
		t.Fatalf("configured origin=%q", got)
	}
}

func TestAuthPublicPathAllowlistPreserved(t *testing.T) {
	paths := []string{
		"/login", "/logout", "/auth/me", "/auth/sso/start", "/auth/sso/callback", "/logged-out",
		"/swagger/doc.json", "/webhooks/inbound", "/webhooks/inbound/token", "/callback", "/auth/callback",
		"/models/oauth/callback", "/channels/github/callback", "/channels/slack/callback",
	}
	for _, mode := range []auth.AuthMode{auth.AuthModeHostedSSO, auth.AuthModeLocal, auth.AuthModeDisabled} {
		h := &Handler{authMode: mode}
		for _, path := range paths {
			if !h.isAuthPublicPath(path) {
				t.Fatalf("mode=%s path=%s is not public", mode, path)
			}
		}
	}
}

func TestHostedMiddlewareAndLoginContracts(t *testing.T) {
	_, e, store := hostedAuthTestHandler(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=p1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/sso/start?next=") || store.Count() != 0 {
		t.Fatalf("status=%d location=%q count=%d", rec.Code, rec.Header().Get("Location"), store.Count())
	}

	htmxReq := httptest.NewRequest(http.MethodGet, "/api/tasks?x=1", nil)
	htmxReq.Header.Set("HX-Request", "true")
	htmxRec := httptest.NewRecorder()
	e.ServeHTTP(htmxRec, htmxReq)
	if htmxRec.Code != http.StatusUnauthorized || !strings.HasPrefix(htmxRec.Header().Get("HX-Redirect"), "/auth/sso/start?next=") {
		t.Fatalf("HTMX status=%d redirect=%q", htmxRec.Code, htmxRec.Header().Get("HX-Redirect"))
	}

	loginReq := httptest.NewRequest(http.MethodGet, "/login?next=***", nil)
	loginRec := httptest.NewRecorder()
	e.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusBadRequest || store.Count() != 0 {
		t.Fatalf("invalid login status=%d count=%d", loginRec.Code, store.Count())
	}

	body := "username=legacy&password=do-not-parse"
	postReq := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	postReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	postRec := httptest.NewRecorder()
	e.ServeHTTP(postRec, postReq)
	if postRec.Code != http.StatusMethodNotAllowed || postRec.Header().Get("Allow") != http.MethodGet || postRec.Header().Get("Location") != "" {
		t.Fatalf("POST login status=%d allow=%q location=%q", postRec.Code, postRec.Header().Get("Allow"), postRec.Header().Get("Location"))
	}
}

func TestHostedAuthMeAndLogoutContracts(t *testing.T) {
	h, e, _ := hostedAuthTestHandler(t, nil)
	now := time.Now()
	claims := auth.SessionClaims{Version: 1, Subject: "subject", Email: "<user+tag@example.com>", Display: "<user+tag@example.com>", InstanceID: "instance-1", AuthSource: auth.HostedAuthSource, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix()}
	token, err := auth.SignHostedSession(claims, hostedHandlerKey)
	if err != nil {
		t.Fatal(err)
	}
	meReq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	meReq.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token})
	meRec := httptest.NewRecorder()
	e.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK || meRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("me status=%d cache=%q", meRec.Code, meRec.Header().Get("Cache-Control"))
	}
	var got map[string]any
	if err := json.Unmarshal(meRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || got["auth_source"] != auth.HostedAuthSource || got["username"] != claims.Email || got["display"] != claims.Display {
		t.Fatalf("unexpected identity response %#v", got)
	}

	invalidReq := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	invalidReq.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: "invalid"})
	invalidRec := httptest.NewRecorder()
	e.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusUnauthorized || len(invalidRec.Result().Cookies()) != 1 || invalidRec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("invalid status=%d cookies=%#v", invalidRec.Code, invalidRec.Result().Cookies())
	}

	for _, origin := range []string{"", "null", "https://bob.openvibely.ai", "https://alice.openvibely.ai/"} {
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || len(rec.Result().Cookies()) != 0 || rec.Header().Get("Location") != "" {
			t.Fatalf("origin=%q status=%d cookies=%#v location=%q", origin, rec.Code, rec.Result().Cookies(), rec.Header().Get("Location"))
		}
	}
	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.Header.Set("Origin", "https://alice.openvibely.ai")
	logoutRec := httptest.NewRecorder()
	e.ServeHTTP(logoutRec, logoutReq)
	if logoutRec.Code != http.StatusFound || logoutRec.Header().Get("Location") != "/logged-out" || len(logoutRec.Result().Cookies()) != 1 || logoutRec.Result().Cookies()[0].MaxAge != -1 {
		t.Fatalf("valid logout status=%d location=%q cookies=%#v", logoutRec.Code, logoutRec.Header().Get("Location"), logoutRec.Result().Cookies())
	}

	_ = h
	loggedOutReq := httptest.NewRequest(http.MethodGet, "/logged-out", nil)
	loggedOutRec := httptest.NewRecorder()
	e.ServeHTTP(loggedOutRec, loggedOutReq)
	if loggedOutRec.Code != http.StatusOK || !strings.Contains(loggedOutRec.Body.String(), "Sign in again") || loggedOutRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logged out status=%d body=%s", loggedOutRec.Code, loggedOutRec.Body.String())
	}
}

func TestHostedRestartLostStateClearsOrphanedBrowserBinding(t *testing.T) {
	_, e, _ := hostedAuthTestHandler(t, nil)
	nonce := canonicalHostedValue("b")
	binding, err := auth.SignBrowserBinding(nonce, hostedHandlerKey)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?code="+canonicalHostedValue("c")+"&state="+canonicalHostedValue("s"), nil)
	req.AddCookie(&http.Cookie{Name: "ov_sso_browser", Value: binding})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Try again") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	foundDeletion := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "ov_sso_browser" && cookie.MaxAge == -1 && cookie.Path == "/auth/sso" {
			foundDeletion = true
		}
	}
	if !foundDeletion {
		t.Fatal("orphaned browser binding was not deleted")
	}
}

func TestSSOProtocolRoutesAreGuardedOutsideHostedMode(t *testing.T) {
	_, e := authTestHandler(t)
	for _, path := range []string{"/auth/sso/start?next=secret", "/auth/sso/callback?code=secret&state=secret"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("path=%q status=%d location=%q cache=%q", path, rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"))
		}
	}
}
