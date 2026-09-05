package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
)

func authTestHandler(t *testing.T) (*Handler, *echo.Echo) {
	t.Helper()
	h, e, _ := setupTestHandler(t)
	h.SetAuthConfig(auth.Config{
		Enabled:       true,
		Username:      "admin",
		Password:      "secret",
		SessionSecret: "test-signing-secret",
		SessionTTL:    time.Hour,
	})
	e = echo.New()
	e.Use(h.AuthMiddleware())
	h.RegisterRoutes(e)
	return h, e
}

func TestAuthLoginPage_Render(t *testing.T) {
	_, e := authTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Fatalf("expected login page content, got %s", rec.Body.String())
	}
}

func TestAuthLogin_SuccessSetsCookieAndRedirects(t *testing.T) {
	_, e := authTestHandler(t)
	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "secret")
	form.Set("next", "")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected redirect '/', got %q", loc)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie to be set")
	}
	var found bool
	for _, c := range cookies {
		if c.Name == auth.DefaultCookieName {
			found = true
			if !c.HttpOnly {
				t.Fatal("expected HttpOnly cookie")
			}
			if c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("expected SameSite=Lax, got %v", c.SameSite)
			}
			if c.Path != "/" {
				t.Fatalf("expected path '/', got %q", c.Path)
			}
		}
	}
	if !found {
		t.Fatalf("expected %s cookie", auth.DefaultCookieName)
	}
}

func TestAuthLogin_FailureRedirectsBackToLogin(t *testing.T) {
	_, e := authTestHandler(t)
	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "wrong")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("expected redirect to login with next, got %q", rec.Header().Get("Location"))
	}
}

func TestAuthLogout_ClearsCookieAndRedirects(t *testing.T) {
	_, e := authTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/logout", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if rec.Header().Get("Location") != "/login" {
		t.Fatalf("expected redirect to /login, got %q", rec.Header().Get("Location"))
	}

	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected clearing cookie")
	}
	if cookies[0].MaxAge != -1 {
		t.Fatalf("expected MaxAge=-1 clear cookie, got %d", cookies[0].MaxAge)
	}
}

func TestAuthMe_UnauthenticatedReturns401(t *testing.T) {
	_, e := authTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestAuthMe_LocalSessionMatrix(t *testing.T) {
	h, e := authTestHandler(t)
	cfg := *h.authCfg
	cfg.CookieName = "custom_session"
	h.SetAuthConfig(cfg)
	now := time.Now()
	validToken, err := h.authCfg.SignToken(now)
	if err != nil {
		t.Fatal(err)
	}
	expiredToken, err := h.authCfg.SignToken(now.Add(-2 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	tamperedSignature, err := base64.RawURLEncoding.DecodeString(strings.Split(validToken, ".")[1])
	if err != nil {
		t.Fatal(err)
	}
	tamperedSignature[0] ^= 1
	tamperedToken := strings.Split(validToken, ".")[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedSignature)

	for _, tt := range []struct {
		name              string
		cookie            string
		wantAuthenticated bool
	}{
		{name: "missing"},
		{name: "expired", cookie: expiredToken},
		{name: "tampered", cookie: tamperedToken},
		{name: "valid custom cookie", cookie: validToken, wantAuthenticated: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: cfg.CookieName, Value: tt.cookie})
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if tt.wantAuthenticated {
				if rec.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["authenticated"] != true || body["username"] != cfg.Username || body["display"] != cfg.Username {
					t.Fatalf("body=%#v", body)
				}
				return
			}
			if rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != `{"authenticated":false}` {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if len(rec.Result().Cookies()) != 0 {
				t.Fatalf("local auth emitted cookies for rejected session: %#v", rec.Result().Cookies())
			}
		})
	}
}

func TestAuthMiddleware_FullPageRedirectWhenUnauthenticated(t *testing.T) {
	_, e := authTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=p1", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("expected login redirect, got %q", rec.Header().Get("Location"))
	}
}

func TestAuthMiddleware_HTMXGets401WithHXRedirect(t *testing.T) {
	_, e := authTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=p1", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("HX-Redirect"), "/login?next=") {
		t.Fatalf("expected HX-Redirect to login, got %q", rec.Header().Get("HX-Redirect"))
	}
}

func TestAuthMiddleware_HTMXRejectsUnauthenticatedMergeMutation(t *testing.T) {
	_, e := authTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/tasks/unauthorized/worktree/merge", strings.NewReader("merge_type=merge"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthenticated merge mutation to return 401, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("HX-Redirect"), "/login?next=") {
		t.Fatalf("expected merge mutation to redirect authentication through HTMX, got %q", rec.Header().Get("HX-Redirect"))
	}
}

func TestAuthMiddleware_RejectsLocalSessionAtExpirationBoundary(t *testing.T) {
	h, e := authTestHandler(t)
	cfg := *h.authCfg
	cfg.SessionTTL = time.Second
	h.SetAuthConfig(cfg)

	for attempt := 0; attempt < 20; attempt++ {
		expiresAt := time.Now().Unix()
		token, err := h.authCfg.SignToken(time.Unix(expiresAt-1, 0))
		if err != nil {
			t.Fatalf("SignToken error: %v", err)
		}
		if time.Now().Unix() != expiresAt {
			continue
		}

		req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=p1", nil)
		req.AddCookie(&http.Cookie{Name: h.authCfg.CookieName, Value: token})
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusFound && strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
			return
		}
		if time.Now().Unix() == expiresAt {
			t.Fatalf("local session was accepted at its expiration boundary: status=%d location=%q", rec.Code, rec.Header().Get("Location"))
		}
	}

	t.Fatal("could not observe a stable exact expiration boundary")
}

func TestAuthMiddleware_AllowsAuthenticatedPassThrough(t *testing.T) {
	h, e := authTestHandler(t)
	token, err := h.authCfg.SignToken(time.Now())
	if err != nil {
		t.Fatalf("SignToken error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks", nil)
	req.AddCookie(&http.Cookie{Name: auth.DefaultCookieName, Value: token, Path: "/"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code == http.StatusFound || rec.Code == http.StatusUnauthorized {
		t.Fatalf("expected authenticated request to pass through, got %d", rec.Code)
	}
}

func TestAuthMiddleware_PublicRouteExceptions(t *testing.T) {
	_, e := authTestHandler(t)
	allowed := []string{
		"/login",
		"/favicon.png",
		"/favicon.ico",
		"/webhooks/inbound/token123",
		"/callback",
		"/auth/callback",
		"/models/oauth/callback",
		"/channels/github/callback",
		"/channels/slack/callback",
	}
	for _, p := range allowed {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code == http.StatusFound && strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
			t.Fatalf("expected public path %s not to be auth-redirected", p)
		}
	}
}

func TestAuthLoginFlowSanitizesRedirectDestinations(t *testing.T) {
	_, e := authTestHandler(t)
	tests := []struct {
		name        string
		destination string
		want        string
	}{
		{name: "safe internal path", destination: "/chat?project_id=p1", want: "/chat?project_id=p1"},
		{name: "backslash authority form", destination: "/\\attacker.example", want: "/"},
		{name: "double backslash authority form", destination: "/\\\\attacker.example", want: "/"},
		{name: "mixed slash authority form", destination: "/\\/attacker.example", want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tt.destination, nil)
			loginResponse := httptest.NewRecorder()
			e.ServeHTTP(loginResponse, request)
			if loginResponse.Code != http.StatusFound {
				t.Fatalf("protected request: expected 302, got %d", loginResponse.Code)
			}

			loginLocation := loginResponse.Header().Get("Location")
			if !strings.HasPrefix(loginLocation, "/login?next=") {
				t.Fatalf("protected request: expected login redirect, got %q", loginLocation)
			}

			loginPageRequest := httptest.NewRequest(http.MethodGet, loginLocation, nil)
			loginPageResponse := httptest.NewRecorder()
			e.ServeHTTP(loginPageResponse, loginPageRequest)
			if loginPageResponse.Code != http.StatusOK {
				t.Fatalf("login page: expected 200, got %d", loginPageResponse.Code)
			}

			const hiddenNextPrefix = `<input type="hidden" name="next" value="`
			body := loginPageResponse.Body.String()
			nextStart := strings.Index(body, hiddenNextPrefix)
			if nextStart < 0 {
				t.Fatalf("login page did not contain hidden next value")
			}
			nextStart += len(hiddenNextPrefix)
			nextEnd := strings.IndexByte(body[nextStart:], '"')
			if nextEnd < 0 {
				t.Fatalf("login page hidden next value was not terminated")
			}
			nextEncoded := body[nextStart : nextStart+nextEnd]

			form := url.Values{}
			form.Set("username", "admin")
			form.Set("password", "secret")
			form.Set("next", nextEncoded)
			loginRequest := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			loginRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			finalResponse := httptest.NewRecorder()
			e.ServeHTTP(finalResponse, loginRequest)

			if finalResponse.Code != http.StatusFound {
				t.Fatalf("login submission: expected 302, got %d", finalResponse.Code)
			}
			if got := finalResponse.Header().Get("Location"); got != tt.want {
				t.Fatalf("login submission: expected redirect %q, got %q", tt.want, got)
			}
		})
	}
}

func TestAuthLogin_RejectsTamperedBackslashNext(t *testing.T) {
	_, e := authTestHandler(t)
	for _, destination := range []string{
		"/\\attacker.example",
		"/\\\\attacker.example",
		"/\\/attacker.example",
		"/%5Cattacker.example",
		"/safe\\path",
	} {
		t.Run(destination, func(t *testing.T) {
			encodedNext := base64.RawURLEncoding.EncodeToString([]byte(destination))
			form := url.Values{}
			form.Set("username", "admin")
			form.Set("password", "secret")
			form.Set("next", encodedNext)
			request := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			e.ServeHTTP(response, request)

			if response.Code != http.StatusFound {
				t.Fatalf("expected 302, got %d", response.Code)
			}
			if got := response.Header().Get("Location"); got != "/" {
				t.Fatalf("expected safe local redirect '/', got %q", got)
			}
		})
	}
}

func TestAuthLogin_LocksAfterThreeFailedAttemptsForSixHours(t *testing.T) {
	_, e := authTestHandler(t)

	for i := 0; i < 3; i++ {
		form := url.Values{}
		form.Set("username", "admin")
		form.Set("password", "wrong")
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("attempt %d: expected 302, got %d", i+1, rec.Code)
		}
	}

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "secret")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302 while locked, got %d", rec.Code)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), "/login?next=") {
		t.Fatalf("expected redirect to login while locked, got %q", rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.DefaultCookieName && c.Value != "" {
			t.Fatalf("expected no session cookie while locked, got %q", c.Value)
		}
	}
}

func TestAuthLogin_WrongUsernameDoesNotTriggerConfiguredUserLockout(t *testing.T) {
	_, e := authTestHandler(t)

	for i := 0; i < 5; i++ {
		form := url.Values{}
		form.Set("username", "someone-else")
		form.Set("password", "wrong")
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("attempt %d: expected 302, got %d", i+1, rec.Code)
		}
	}

	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "secret")
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("expected successful redirect '/', got %q", loc)
	}
}

func TestAuthLoginLockout_SlidingWindowAndDuration(t *testing.T) {
	h, _ := authTestHandler(t)
	base := time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)

	h.recordFailedLoginAttempt(base)
	h.recordFailedLoginAttempt(base.Add(30 * time.Minute))
	h.recordFailedLoginAttempt(base.Add(61 * time.Minute))
	if h.isLoginLocked(base.Add(61 * time.Minute)) {
		t.Fatal("did not expect lock after only two failures in the last hour")
	}

	thirdInWindow := base.Add(70 * time.Minute)
	h.recordFailedLoginAttempt(thirdInWindow)
	if !h.isLoginLocked(thirdInWindow) {
		t.Fatal("expected lock after third failure within one-hour sliding window")
	}
	if !h.isLoginLocked(thirdInWindow.Add(6*time.Hour - time.Second)) {
		t.Fatal("expected lock to remain active until full six-hour duration elapsed")
	}
	if h.isLoginLocked(thirdInWindow.Add(6*time.Hour + time.Second)) {
		t.Fatal("expected lock to expire after six hours")
	}
}

func TestAuthLoginLockout_SuccessClearsFailureState(t *testing.T) {
	h, _ := authTestHandler(t)
	base := time.Date(2026, 2, 2, 12, 0, 0, 0, time.UTC)

	h.recordFailedLoginAttempt(base)
	h.recordFailedLoginAttempt(base.Add(10 * time.Minute))
	h.clearFailedLoginAttempts()
	h.recordFailedLoginAttempt(base.Add(20 * time.Minute))

	if h.isLoginLocked(base.Add(20 * time.Minute)) {
		t.Fatal("did not expect lock after successful login cleared prior failures")
	}
}
