package handler

import (
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

func TestAuthLoginRedirectsToNext(t *testing.T) {
	_, e := authTestHandler(t)
	nextEncoded := strings.TrimPrefix(auth.RedirectURL("/chat?project_id=p1"), "/login?next=")
	form := url.Values{}
	form.Set("username", "admin")
	form.Set("password", "secret")
	form.Set("next", nextEncoded)

	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/chat?project_id=p1" {
		t.Fatalf("expected redirect to next URL, got %q", got)
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
	if !h.isLoginLocked(thirdInWindow.Add(6*time.Hour-time.Second)) {
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
