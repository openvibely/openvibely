package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
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

type hostedStartResult struct {
	recorder *httptest.ResponseRecorder
	location *url.URL
	state    string
	binding  *http.Cookie
}

func performHostedStart(t *testing.T, e *echo.Echo, binding *http.Cookie, destination string) hostedStartResult {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, auth.HostedSSOStartURL(destination), nil)
	if binding != nil {
		req.AddCookie(binding)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("start status=%d body=%s", rec.Code, rec.Body.String())
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var refreshed *http.Cookie
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == "ov_sso_browser" {
			refreshed = cookie
			break
		}
	}
	if refreshed == nil {
		t.Fatal("start response omitted browser binding")
	}
	return hostedStartResult{
		recorder: rec,
		location: location,
		state:    location.Query().Get("state"),
		binding:  refreshed,
	}
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
	start := performHostedStart(t, e, nil, destination)
	state := start.state
	if start.location.Path != "/sso/authorize" || !strings.HasPrefix(start.location.Query().Get("redirect_uri"), "https://alice.openvibely.ai/") || len(state) != 43 {
		t.Fatalf("unexpected authorize URL %q", start.location.String())
	}
	binding := start.binding
	if binding.Path != "/auth/sso" || !binding.HttpOnly || !binding.Secure || binding.SameSite != http.SameSiteLaxMode || binding.MaxAge != 600 {
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
	legacy := auth.Config{Enabled: true, Username: "user@example.com", SessionSecret: string(hostedHandlerKey), SessionTTL: time.Hour}
	if _, err := legacy.VerifyToken(session.Value, time.Now()); err == nil {
		t.Fatal("legacy local-auth decoder accepted hosted session")
	}
	if bindingDeletion == nil || bindingDeletion.Value != "" || bindingDeletion.Domain != "" || bindingDeletion.MaxAge != -1 ||
		bindingDeletion.Path != "/auth/sso" || !bindingDeletion.HttpOnly || !bindingDeletion.Secure ||
		bindingDeletion.SameSite != http.SameSiteLaxMode || !bindingDeletion.Expires.Equal(time.Unix(0, 0).UTC()) {
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

func TestSessionRecognition_HostedCookieStates(t *testing.T) {
	h, _, _ := hostedAuthTestHandler(t, nil)
	now := time.Now()
	base := auth.SessionClaims{
		Version: 1, Subject: "subject", Email: "user@example.com", Display: "user@example.com",
		InstanceID: "instance-1", AuthSource: auth.HostedAuthSource,
		IssuedAt: now.Unix(), ExpiresAt: now.Unix() + int64(time.Hour/time.Second),
	}
	sign := func(claims auth.SessionClaims) string {
		token, err := auth.SignHostedSession(claims, hostedHandlerKey)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	valid := sign(base)
	tampered := valid[:len(valid)-1] + "A"
	if tampered == valid {
		tampered = valid[:len(valid)-1] + "B"
	}
	expired := base
	expired.IssuedAt = now.Add(-2 * time.Hour).Unix()
	expired.ExpiresAt = expired.IssuedAt + int64(time.Hour/time.Second)
	wrongInstance := base
	wrongInstance.InstanceID = "instance-2"

	for _, tt := range []struct {
		name       string
		cookie     string
		wantState  hostedCookieState
		wantClaims bool
	}{
		{name: "missing", wantState: hostedCookieMissing},
		{name: "valid", cookie: valid, wantState: hostedCookieValid, wantClaims: true},
		{name: "expired", cookie: sign(expired), wantState: hostedCookieInvalid},
		{name: "malformed", cookie: "malformed", wantState: hostedCookieInvalid},
		{name: "tampered", cookie: tampered, wantState: hostedCookieInvalid},
		{name: "wrong instance", cookie: sign(wrongInstance), wantState: hostedCookieInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: tt.cookie})
			}
			session := h.recognizeSession(req, now)
			if session.hostedCookieState != tt.wantState {
				t.Fatalf("state=%d want=%d", session.hostedCookieState, tt.wantState)
			}
			if (session.hostedClaims != nil) != tt.wantClaims {
				t.Fatalf("claims=%#v want claims=%v", session.hostedClaims, tt.wantClaims)
			}
			if tt.wantClaims && (session.hostedClaims.Subject != base.Subject || session.hostedClaims.InstanceID != base.InstanceID) {
				t.Fatalf("claims=%#v", session.hostedClaims)
			}
		})
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
	firstStart := performHostedStart(t, e, nil, "/first")
	secondStart := performHostedStart(t, e, firstStart.binding, "/second")
	binding := secondStart.binding
	firstState := firstStart.state
	secondState := secondStart.state
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

func TestHostedCallbackEncodedErrorBoundaryPrecedesStateLookup(t *testing.T) {
	_, e, store := hostedAuthTestHandler(t, nil)
	exactStart := performHostedStart(t, e, nil, "/exact")
	overStart := performHostedStart(t, e, exactStart.binding, "/over")
	binding := overStart.binding
	exactState := exactStart.state
	overState := overStart.state

	exactEncodedError := strings.Repeat("%61", 64)
	exactReq := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?error="+exactEncodedError+"&state="+exactState, nil)
	exactReq.AddCookie(binding)
	exactRec := httptest.NewRecorder()
	e.ServeHTTP(exactRec, exactReq)
	if exactRec.Code != http.StatusBadRequest || store.Count() != 1 {
		t.Fatalf("exact boundary status=%d pending=%d", exactRec.Code, store.Count())
	}

	overReq := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?error="+exactEncodedError+"a&state="+overState, nil)
	overReq.AddCookie(binding)
	overRec := httptest.NewRecorder()
	e.ServeHTTP(overRec, overReq)
	if overRec.Code != http.StatusBadRequest || store.Count() != 1 {
		t.Fatalf("over-limit error reached state lookup: status=%d pending=%d", overRec.Code, store.Count())
	}

	validReq := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?error=access_denied&state="+overState, nil)
	validReq.AddCookie(binding)
	validRec := httptest.NewRecorder()
	e.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusBadRequest || store.Count() != 0 {
		t.Fatalf("over-limit callback consumed matching state: status=%d pending=%d", validRec.Code, store.Count())
	}
}

func TestHostedBrowserBindingRefreshPreservesNewestTransactionLifetime(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := auth.NewPendingStore(ctx, func() time.Time { return now })
	defer store.Close()

	var providerCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sub":"subject","email":"user@example.com","email_verified":true,"instance_id":"instance-1","instance_slug":"alice","instance_host":"alice.openvibely.ai"}`)
	}))
	defer provider.Close()

	h, _, _ := setupTestHandler(t)
	client := auth.NewHostedSSOClient(provider.URL, "instance-1", "https://alice.openvibely.ai/auth/sso/callback")
	h.SetHostedSSO(client, store, hostedHandlerKey, "instance-1", "https://alice.openvibely.ai")
	e := echo.New()
	e.Use(h.AuthMiddleware())
	h.RegisterRoutes(e)

	nonce := canonicalHostedValue("b")
	bindingValue, err := auth.SignBrowserBinding(nonce, hostedHandlerKey)
	if err != nil {
		t.Fatal(err)
	}
	priorExpiry := time.Now().Add(time.Second)
	start := performHostedStart(t, e, &http.Cookie{Name: "ov_sso_browser", Value: bindingValue, Expires: priorExpiry, MaxAge: 1}, "/newest")
	refreshed := start.binding
	if refreshed.Value != bindingValue || refreshed.MaxAge != 600 || !refreshed.Expires.After(priorExpiry.Add(9*time.Minute)) {
		t.Fatalf("browser binding was not refreshed for ten minutes: %#v priorExpiry=%s", refreshed, priorExpiry)
	}

	now = now.Add(10*time.Minute - time.Second)
	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?code="+canonicalHostedValue("c")+"&state="+start.state, nil)
	callbackReq.AddCookie(refreshed)
	callbackRec := httptest.NewRecorder()
	e.ServeHTTP(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusFound || callbackRec.Header().Get("Location") != "/newest" || providerCalls != 1 || store.Count() != 0 {
		t.Fatalf("callback status=%d location=%q providerCalls=%d pending=%d body=%s", callbackRec.Code, callbackRec.Header().Get("Location"), providerCalls, store.Count(), callbackRec.Body.String())
	}
	foundSession := false
	for _, cookie := range callbackRec.Result().Cookies() {
		if cookie.Name == auth.DefaultCookieName && cookie.Value != "" {
			foundSession = true
		}
	}
	if !foundSession {
		t.Fatal("newest transaction did not create a hosted session near its full lifetime")
	}
}

func TestHostedAuthTransitionCacheHeaders(t *testing.T) {
	_, e, _ := hostedAuthTestHandler(t, nil)
	assertHeaders := func(method, target string, configure func(*http.Request), wantStatus int, callback bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		if configure != nil {
			configure(req)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != wantStatus || rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s %s status=%d cache=%q body=%q", method, target, rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
		}
		if callback && rec.Header().Get("Pragma") != "no-cache" {
			t.Fatalf("%s %s pragma=%q", method, target, rec.Header().Get("Pragma"))
		}
		return rec
	}

	assertHeaders(http.MethodGet, "/login", nil, http.StatusFound, false)
	assertHeaders(http.MethodPost, "/login", nil, http.StatusMethodNotAllowed, false)
	assertHeaders(http.MethodGet, auth.HostedSSOStartURL("/start"), nil, http.StatusFound, false)
	assertHeaders(http.MethodPost, "/logout", nil, http.StatusForbidden, false)
	assertHeaders(http.MethodPost, "/logout", func(req *http.Request) {
		req.Header.Set("Origin", "https://alice.openvibely.ai")
	}, http.StatusFound, false)
	assertHeaders(http.MethodGet, "/auth/me", nil, http.StatusUnauthorized, false)
	assertHeaders(http.MethodGet, "/logged-out", nil, http.StatusOK, false)
	assertHeaders(http.MethodGet, "/auth/sso/callback?bad=input", nil, http.StatusBadRequest, true)

	errorStart := performHostedStart(t, e, nil, "/error")
	if errorStart.recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SSO start cache=%q", errorStart.recorder.Header().Get("Cache-Control"))
	}
	assertHeaders(http.MethodGet, "/auth/sso/callback?error=access_denied&state="+errorStart.state, func(req *http.Request) {
		req.AddCookie(errorStart.binding)
	}, http.StatusBadRequest, true)
	successStart := performHostedStart(t, e, nil, "/success")
	if successStart.recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("SSO start cache=%q", successStart.recorder.Header().Get("Cache-Control"))
	}
	assertHeaders(http.MethodGet, "/auth/sso/callback?code="+canonicalHostedValue("c")+"&state="+successStart.state, func(req *http.Request) {
		req.AddCookie(successStart.binding)
	}, http.StatusFound, true)
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

func TestHostedHTTPLoopbackCookieLifecycle(t *testing.T) {
	const appBaseURL = "http://127.0.0.1:3001"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sso/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"sub":"subject","email":"user@example.com","email_verified":true,"instance_id":"instance-1","instance_slug":"alice","instance_host":"127.0.0.1"}`)
	}))
	defer provider.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := auth.NewPendingStore(ctx, time.Now)
	defer store.Close()
	h, _, _ := setupTestHandler(t)
	h.SetHostedSSO(
		auth.NewHostedSSOClient(provider.URL, "instance-1", appBaseURL+"/auth/sso/callback"),
		store,
		hostedHandlerKey,
		"instance-1",
		appBaseURL,
	)
	e := echo.New()
	e.Use(h.AuthMiddleware())
	h.RegisterRoutes(e)
	workspace := httptest.NewServer(e)
	defer workspace.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	startResponse, err := client.Get(workspace.URL + auth.HostedSSOStartURL("/projects"))
	if err != nil {
		t.Fatal(err)
	}
	startResponse.Body.Close()
	if startResponse.StatusCode != http.StatusFound {
		t.Fatalf("start status=%d", startResponse.StatusCode)
	}
	authorizeURL, err := url.Parse(startResponse.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if authorizeURL.Query().Get("redirect_uri") != appBaseURL+"/auth/sso/callback" {
		t.Fatalf("redirect_uri=%q", authorizeURL.Query().Get("redirect_uri"))
	}
	var binding *http.Cookie
	for _, cookie := range startResponse.Cookies() {
		if cookie.Name == "ov_sso_browser" {
			binding = cookie
		}
	}
	if binding == nil || binding.Value == "" || binding.Domain != "" || binding.Path != "/auth/sso" ||
		!binding.HttpOnly || binding.Secure || binding.SameSite != http.SameSiteLaxMode || binding.MaxAge != 600 || !binding.Expires.After(time.Now()) {
		t.Fatalf("unexpected HTTP browser binding %#v", binding)
	}

	callbackResponse, err := client.Get(workspace.URL + "/auth/sso/callback?code=" + canonicalHostedValue("c") + "&state=" + authorizeURL.Query().Get("state"))
	if err != nil {
		t.Fatal(err)
	}
	callbackResponse.Body.Close()
	if callbackResponse.StatusCode != http.StatusFound || callbackResponse.Header.Get("Location") != "/projects" {
		t.Fatalf("callback status=%d location=%q", callbackResponse.StatusCode, callbackResponse.Header.Get("Location"))
	}
	var session, bindingDeletion *http.Cookie
	for _, cookie := range callbackResponse.Cookies() {
		switch cookie.Name {
		case auth.DefaultCookieName:
			session = cookie
		case "ov_sso_browser":
			bindingDeletion = cookie
		}
	}
	if session == nil || session.Value == "" || session.Domain != "" || session.Path != "/" ||
		!session.HttpOnly || session.Secure || session.SameSite != http.SameSiteLaxMode || session.MaxAge != 3600 || !session.Expires.After(time.Now()) {
		t.Fatalf("unexpected HTTP hosted session %#v", session)
	}
	if bindingDeletion == nil || bindingDeletion.Value != "" || bindingDeletion.Domain != binding.Domain || bindingDeletion.Path != binding.Path ||
		bindingDeletion.HttpOnly != binding.HttpOnly || bindingDeletion.Secure != binding.Secure || bindingDeletion.SameSite != binding.SameSite ||
		bindingDeletion.MaxAge != -1 || !bindingDeletion.Expires.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("HTTP binding deletion did not replace issuance: issued=%#v deleted=%#v", binding, bindingDeletion)
	}

	meResponse, err := client.Get(workspace.URL + "/auth/me")
	if err != nil {
		t.Fatal(err)
	}
	meResponse.Body.Close()
	if meResponse.StatusCode != http.StatusOK {
		t.Fatalf("HTTP hosted session did not survive callback: status=%d", meResponse.StatusCode)
	}
	logoutRequest, err := http.NewRequest(http.MethodPost, workspace.URL+"/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	logoutRequest.Header.Set("Origin", appBaseURL)
	logoutResponse, err := client.Do(logoutRequest)
	if err != nil {
		t.Fatal(err)
	}
	logoutResponse.Body.Close()
	if logoutResponse.StatusCode != http.StatusFound || logoutResponse.Header.Get("Location") != "/logged-out" {
		t.Fatalf("logout status=%d location=%q", logoutResponse.StatusCode, logoutResponse.Header.Get("Location"))
	}
	logoutCookies := logoutResponse.Cookies()
	if len(logoutCookies) != 1 {
		t.Fatalf("logout cookies=%#v", logoutCookies)
	}
	sessionDeletion := logoutCookies[0]
	if sessionDeletion.Name != session.Name || sessionDeletion.Value != "" || sessionDeletion.Domain != session.Domain || sessionDeletion.Path != session.Path ||
		sessionDeletion.HttpOnly != session.HttpOnly || sessionDeletion.Secure != session.Secure || sessionDeletion.SameSite != session.SameSite ||
		sessionDeletion.MaxAge != -1 || !sessionDeletion.Expires.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("HTTP session deletion did not replace issuance: issued=%#v deleted=%#v", session, sessionDeletion)
	}
	workspaceURL, err := url.Parse(workspace.URL + "/auth/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range jar.Cookies(workspaceURL) {
		if cookie.Name == auth.DefaultCookieName || cookie.Name == "ov_sso_browser" {
			t.Fatalf("deleted auth cookie remains in HTTP jar: %#v", cookie)
		}
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
		"/favicon.png", "/favicon.ico",
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

	invalidOrigins := [][]string{
		nil,
		{"null"},
		{"::::"},
		{"https://bob.openvibely.ai"},
		{"https://alice.openvibely.ai/"},
		{"https://alice.openvibely.ai", "https://alice.openvibely.ai"},
	}
	for _, origins := range invalidOrigins {
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		for _, origin := range origins {
			req.Header.Add("Origin", origin)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Body.Len() != 0 || len(rec.Result().Cookies()) != 0 || rec.Header().Get("Location") != "" {
			t.Fatalf("origins=%q status=%d body=%q cookies=%#v location=%q", origins, rec.Code, rec.Body.String(), rec.Result().Cookies(), rec.Header().Get("Location"))
		}
	}
	logoutReq := httptest.NewRequest(http.MethodPost, "/logout", nil)
	logoutReq.Header.Set("Origin", "https://alice.openvibely.ai")
	logoutRec := httptest.NewRecorder()
	e.ServeHTTP(logoutRec, logoutReq)
	logoutCookies := logoutRec.Result().Cookies()
	if logoutRec.Code != http.StatusFound || logoutRec.Header().Get("Location") != "/logged-out" || len(logoutCookies) != 1 {
		t.Fatalf("valid logout status=%d location=%q cookies=%#v", logoutRec.Code, logoutRec.Header().Get("Location"), logoutCookies)
	}
	logoutCookie := logoutCookies[0]
	if logoutCookie.Name != auth.DefaultCookieName || logoutCookie.Value != "" || logoutCookie.Domain != "" || logoutCookie.Path != "/" ||
		!logoutCookie.HttpOnly || !logoutCookie.Secure || logoutCookie.SameSite != http.SameSiteLaxMode || logoutCookie.MaxAge != -1 ||
		!logoutCookie.Expires.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("unexpected logout deletion cookie %#v", logoutCookie)
	}

	_ = h
	loggedOutReq := httptest.NewRequest(http.MethodGet, "/logged-out", nil)
	loggedOutRec := httptest.NewRecorder()
	e.ServeHTTP(loggedOutRec, loggedOutReq)
	if loggedOutRec.Code != http.StatusOK || !strings.Contains(loggedOutRec.Body.String(), "Sign in again") || loggedOutRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logged out status=%d body=%s", loggedOutRec.Code, loggedOutRec.Body.String())
	}
}

func TestHostedInvalidGrantIsTerminalManualRetryWithoutProviderText(t *testing.T) {
	var providerCalls int
	provider := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"invalid_grant","error_description":"provider-secret-description"}`)
	})
	_, e, store := hostedAuthTestHandler(t, provider)
	start := performHostedStart(t, e, nil, "/safe?tab=one")
	callbackReq := httptest.NewRequest(http.MethodGet, "/auth/sso/callback?code="+canonicalHostedValue("c")+"&state="+start.state, nil)
	callbackReq.AddCookie(start.binding)
	callbackRec := httptest.NewRecorder()
	e.ServeHTTP(callbackRec, callbackReq)
	if callbackRec.Code != http.StatusBadGateway || callbackRec.Header().Get("Location") != "" || providerCalls != 1 || store.Count() != 0 {
		t.Fatalf("status=%d location=%q providerCalls=%d count=%d", callbackRec.Code, callbackRec.Header().Get("Location"), providerCalls, store.Count())
	}
	if strings.Contains(callbackRec.Body.String(), "provider-secret-description") || strings.Contains(callbackRec.Body.String(), "invalid_grant") || !strings.Contains(callbackRec.Body.String(), "Try again") {
		t.Fatalf("unsafe or missing retry page: %s", callbackRec.Body.String())
	}
	for _, cookie := range callbackRec.Result().Cookies() {
		if cookie.Name == auth.DefaultCookieName && cookie.Value != "" {
			t.Fatal("invalid grant created hosted session")
		}
	}
}

func TestHostedCallbackRejectsMissingAndTamperedBrowserBindingsWithoutConsumingState(t *testing.T) {
	_, e, store := hostedAuthTestHandler(t, nil)
	start := performHostedStart(t, e, nil, "/destination")
	state := start.state
	binding := start.binding
	if store.Count() != 1 {
		t.Fatalf("pending count=%d", store.Count())
	}
	callbackPath := "/auth/sso/callback?error=access_denied&state=" + state
	tamperedBinding := binding.Value[:len(binding.Value)-1] + "A"
	if tamperedBinding == binding.Value {
		tamperedBinding = binding.Value[:len(binding.Value)-1] + "B"
	}
	for _, cookieValue := range []string{"", tamperedBinding} {
		req := httptest.NewRequest(http.MethodGet, callbackPath, nil)
		if cookieValue != "" {
			req.AddCookie(&http.Cookie{Name: "ov_sso_browser", Value: cookieValue})
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || store.Count() != 1 || strings.Contains(rec.Body.String(), "access_denied") {
			t.Fatalf("cookie=%q status=%d count=%d body=%q", cookieValue, rec.Code, store.Count(), rec.Body.String())
		}
	}
	validReq := httptest.NewRequest(http.MethodGet, callbackPath, nil)
	validReq.AddCookie(binding)
	validRec := httptest.NewRecorder()
	e.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusBadRequest || store.Count() != 0 {
		t.Fatalf("valid terminal callback status=%d count=%d", validRec.Code, store.Count())
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

func TestHostedAuthMeRejectedCookieMatrix(t *testing.T) {
	h, e, _ := hostedAuthTestHandler(t, nil)
	now := time.Now()
	base := auth.SessionClaims{
		Version: 1, Subject: "subject", Email: "user@example.com", Display: "user@example.com",
		InstanceID: "instance-1", AuthSource: auth.HostedAuthSource,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	sign := func(claims auth.SessionClaims) string {
		token, err := auth.SignHostedSession(claims, hostedHandlerKey)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	expired := base
	expired.IssuedAt = now.Add(-2 * time.Hour).Unix()
	expired.ExpiresAt = expired.IssuedAt + int64(time.Hour/time.Second)
	wrongInstance := base
	wrongInstance.InstanceID = "instance-2"
	localCfg := auth.Config{Enabled: true, Username: "local-user", SessionSecret: string(hostedHandlerKey), SessionTTL: time.Hour}
	localToken, err := localCfg.SignToken(now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		cookie     string
		wantDelete bool
	}{
		{name: "missing"},
		{name: "invalid", cookie: "invalid", wantDelete: true},
		{name: "expired", cookie: sign(expired), wantDelete: true},
		{name: "wrong instance", cookie: sign(wrongInstance), wantDelete: true},
		{name: "wrong source local token", cookie: localToken, wantDelete: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: tt.cookie})
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || rec.Header().Get("Location") != "" || strings.TrimSpace(rec.Body.String()) != `{"authenticated":false}` {
				t.Fatalf("status=%d location=%q body=%q", rec.Code, rec.Header().Get("Location"), rec.Body.String())
			}
			cookies := rec.Result().Cookies()
			if !tt.wantDelete {
				if len(cookies) != 0 {
					t.Fatalf("missing cookie emitted deletion: %#v", cookies)
				}
				return
			}
			if len(cookies) != 1 {
				t.Fatalf("deletion cookies=%#v", cookies)
			}
			cookie := cookies[0]
			if cookie.Name != auth.DefaultCookieName || cookie.Value != "" || cookie.Domain != "" || cookie.Path != "/" ||
				!cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != -1 ||
				!cookie.Expires.Equal(time.Unix(0, 0).UTC()) {
				t.Fatalf("unexpected deletion cookie %#v", cookie)
			}
		})
	}
	_ = h
}

func TestAuthPublicPathsReachHandlersInEveryRuntimeMode(t *testing.T) {
	paths := []string{
		"/login", "/logout", "/auth/me", "/logged-out", "/swagger/doc.json",
		"/favicon.png", "/favicon.ico",
		"/webhooks/inbound", "/webhooks/inbound/token", "/callback", "/auth/callback",
		"/models/oauth/callback", "/channels/github/callback", "/channels/slack/callback",
	}
	modes := []struct {
		name    string
		mode    auth.AuthMode
		desktop bool
	}{
		{name: "hosted", mode: auth.AuthModeHostedSSO},
		{name: "local", mode: auth.AuthModeLocal},
		{name: "disabled", mode: auth.AuthModeDisabled},
		{name: "desktop", mode: auth.AuthModeDisabled, desktop: true},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			h := &Handler{authMode: mode.mode, desktopMode: mode.desktop}
			if mode.mode == auth.AuthModeLocal {
				cfg := auth.Config{Enabled: true, Username: "local", Password: "password", SessionSecret: "secret"}
				h.authCfg = &cfg
			}
			for _, path := range paths {
				req := httptest.NewRequest(http.MethodGet, path+"?provider_input=secret", nil)
				rec := httptest.NewRecorder()
				ctx := echo.New().NewContext(req, rec)
				called := false
				err := h.AuthMiddleware()(func(c echo.Context) error {
					called = true
					return c.NoContent(http.StatusNoContent)
				})(ctx)
				if err != nil {
					t.Fatalf("path=%s err=%v", path, err)
				}
				if !called || rec.Code != http.StatusNoContent || rec.Header().Get("Location") != "" || rec.Header().Get("HX-Redirect") != "" {
					t.Fatalf("path=%s called=%v status=%d location=%q hx=%q", path, called, rec.Code, rec.Header().Get("Location"), rec.Header().Get("HX-Redirect"))
				}
			}
		})
	}
}

func TestHostedPayloadShapedIdentityRemainsEscapedJSON(t *testing.T) {
	_, e, _ := hostedAuthTestHandler(t, nil)
	payload := `</script><img src=x onerror=alert(1)>{{constructor.constructor('alert(1)')()}}`
	now := time.Now()
	claims := auth.SessionClaims{
		Version: 1, Subject: payload, Email: payload, Display: payload,
		InstanceID: "instance-1", AuthSource: auth.HostedAuthSource,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Hour).Unix(),
	}
	token, err := auth.SignHostedSession(claims, hostedHandlerKey)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "</script>") || strings.Contains(rec.Body.String(), "<img") {
		t.Fatalf("identity was emitted as literal HTML: %s", rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"subject", "email", "username", "display"} {
		if response[key] != payload {
			t.Fatalf("%s=%q", key, response[key])
		}
	}
}

func TestSSOProtocolRoutesAreGuardedOutsideHostedMode(t *testing.T) {
	modes := []struct {
		name    string
		mode    auth.AuthMode
		desktop bool
	}{
		{name: "local", mode: auth.AuthModeLocal},
		{name: "disabled", mode: auth.AuthModeDisabled},
		{name: "desktop", mode: auth.AuthModeDisabled, desktop: true},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			h, _, _ := setupTestHandler(t)
			h.SetAuthMode(mode.mode)
			h.SetDesktopMode(mode.desktop)
			if mode.mode == auth.AuthModeLocal {
				h.SetAuthConfig(auth.Config{Enabled: true, Username: "admin", Password: "secret", SessionSecret: "local-secret"})
			}
			e := echo.New()
			e.Use(h.AuthMiddleware())
			h.RegisterRoutes(e)
			for _, path := range []string{"/auth/sso/start?next=secret", "/auth/sso/callback?code=secret&state=secret"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" || rec.Header().Get("Cache-Control") != "no-store" || rec.Body.Len() != 0 {
					t.Fatalf("path=%q status=%d location=%q cache=%q body=%q", path, rec.Code, rec.Header().Get("Location"), rec.Header().Get("Cache-Control"), rec.Body.String())
				}
				if h.hostedSSOClient != nil || h.hostedPendingStore != nil {
					t.Fatal("non-hosted route constructed or dereferenced hosted SSO dependencies")
				}
			}
		})
	}
}
