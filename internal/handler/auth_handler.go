package handler

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/auth"
)

const (
	loginFailureWindow    = time.Hour
	loginFailureThreshold = 3
	loginLockDuration     = 6 * time.Hour
)

func (h *Handler) SetAuthConfig(cfg auth.Config) {
	norm := cfg.Normalized()
	h.authCfg = &norm
	if h.authMode != auth.AuthModeHostedSSO {
		if norm.Enabled {
			h.authMode = auth.AuthModeLocal
		} else {
			h.authMode = auth.AuthModeDisabled
		}
	}
}

func (h *Handler) SetAuthMode(mode auth.AuthMode) {
	h.authMode = mode
}

func (h *Handler) SetAppBaseURL(appBaseURL string) {
	h.appBaseURL = appBaseURL
}

func (h *Handler) SetHostedSSO(client *auth.HostedSSOClient, store *auth.PendingStore, key []byte, instanceID, appBaseURL string) {
	h.authMode = auth.AuthModeHostedSSO
	h.hostedSSOClient = client
	h.hostedPendingStore = store
	h.hostedSSOKey = append([]byte(nil), key...)
	h.hostedSSOInstanceID = instanceID
	h.appBaseURL = appBaseURL
}

func (h *Handler) authEnabled() bool {
	return h.authMode == auth.AuthModeLocal || h.authMode == auth.AuthModeHostedSSO
}

func (h *Handler) isAuthPublicPath(path string) bool {
	if path == "/login" || path == "/logout" || path == "/auth/me" || path == "/auth/sso/start" || path == "/auth/sso/callback" || path == "/logged-out" || path == "/api/system/health" {
		return true
	}
	if strings.HasPrefix(path, "/swagger/") {
		return true
	}
	if path == "/webhooks/inbound" || strings.HasPrefix(path, "/webhooks/inbound/") {
		return true
	}
	return path == "/callback" ||
		path == "/auth/callback" ||
		path == "/models/oauth/callback" ||
		path == "/channels/github/callback" ||
		path == "/channels/slack/callback"
}

type hostedCookieState uint8

const (
	hostedCookieMissing hostedCookieState = iota
	hostedCookieInvalid
	hostedCookieValid
)

type sessionRecognition struct {
	localUser         *auth.User
	hostedClaims      *auth.SessionClaims
	hostedCookieState hostedCookieState
	err               error
}

func (h *Handler) recognizeSession(request *http.Request, now time.Time) sessionRecognition {
	if h.authMode == auth.AuthModeHostedSSO {
		cookie, err := request.Cookie(auth.DefaultCookieName)
		if err != nil {
			if errors.Is(err, http.ErrNoCookie) {
				return sessionRecognition{hostedCookieState: hostedCookieMissing}
			}
			return sessionRecognition{hostedCookieState: hostedCookieInvalid}
		}
		claims, err := auth.VerifyHostedSession(cookie.Value, h.hostedSSOKey, h.hostedSSOInstanceID, now)
		if err != nil {
			return sessionRecognition{hostedCookieState: hostedCookieInvalid}
		}
		return sessionRecognition{hostedClaims: claims, hostedCookieState: hostedCookieValid}
	}
	if h.authMode == auth.AuthModeLocal && h.authCfg != nil {
		user, err := auth.UserFromRequest(request, *h.authCfg, now)
		return sessionRecognition{localUser: user, err: err}
	}
	return sessionRecognition{}
}

func (h *Handler) AuthMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if !h.authEnabled() {
				return next(c)
			}

			path := c.Request().URL.Path
			if h.isAuthPublicPath(path) {
				return next(c)
			}

			var authenticated any
			session := h.recognizeSession(c.Request(), time.Now())
			if session.localUser != nil {
				authenticated = session.localUser
			} else if session.hostedClaims != nil {
				authenticated = session.hostedClaims
			}
			if authenticated != nil {
				c.Set("auth_user", authenticated)
				return next(c)
			}

			nextURL := c.Request().URL.RequestURI()
			loginURL := auth.RedirectURL(nextURL)
			if h.authMode == auth.AuthModeHostedSSO {
				loginURL = auth.HostedSSOStartURL(nextURL)
			}
			if c.Request().Header.Get("HX-Request") == "true" {
				c.Response().Header().Set("HX-Redirect", loginURL)
				return c.NoContent(http.StatusUnauthorized)
			}
			return c.Redirect(http.StatusFound, loginURL)
		}
	}
}

func (h *Handler) AuthLoginPage(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.authMode == auth.AuthModeHostedSSO {
		destination, err := auth.DecodeHostedNext(c.Request().URL.RawQuery, len(c.Request().URL.RequestURI()))
		if err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "bad request")
		}
		if h.recognizeSession(c.Request(), time.Now()).hostedClaims != nil {
			return c.Redirect(http.StatusFound, destination)
		}
		return c.Redirect(http.StatusFound, auth.HostedSSOStartURL(destination))
	}
	if !h.authEnabled() {
		return c.Redirect(http.StatusFound, "/")
	}
	if h.recognizeSession(c.Request(), time.Now()).localUser != nil {
		return c.Redirect(http.StatusFound, "/")
	}

	next := template.HTMLEscapeString(c.QueryParam("next"))
	body := `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Login - OpenVibely</title>
  <link href="https://cdn.jsdelivr.net/npm/daisyui@4.12.14/dist/full.min.css" rel="stylesheet" type="text/css" />
  <script src="https://cdn.tailwindcss.com"></script>
</head>
<body class="min-h-screen bg-base-200 flex items-center justify-center p-6">
  <div class="card w-full max-w-md bg-base-100 shadow-xl border border-base-300">
    <div class="card-body">
      <h1 class="card-title text-2xl">Sign in</h1>
      <p class="text-sm opacity-70 mb-2">Enter your credentials to continue.</p>
      <form method="POST" action="/login" class="space-y-4">
        <input type="hidden" name="next" value="` + next + `" />
        <label class="form-control w-full">
          <span class="label-text mb-1">Username</span>
          <input name="username" type="text" class="input input-bordered w-full" required autofocus />
        </label>
        <label class="form-control w-full">
          <span class="label-text mb-1">Password</span>
          <input name="password" type="password" class="input input-bordered w-full" required />
        </label>
        <button type="submit" class="btn btn-primary w-full">Login</button>
      </form>
    </div>
  </div>
</body>
</html>`
	return c.HTML(http.StatusOK, body)
}

func (h *Handler) AuthLogin(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.authMode == auth.AuthModeHostedSSO {
		c.Response().Header().Set("Allow", http.MethodGet)
		return c.NoContent(http.StatusMethodNotAllowed)
	}
	if !h.authEnabled() {
		return c.Redirect(http.StatusFound, "/")
	}

	now := time.Now()
	username := strings.TrimSpace(c.FormValue("username"))
	if h.isConfiguredLoginUser(username) && h.isLoginLocked(now) {
		encodedNext := c.FormValue("next")
		nextPath, _ := auth.DecodeNext(encodedNext)
		loginURL := auth.RedirectURL(nextPath)
		return c.Redirect(http.StatusFound, loginURL)
	}

	password := c.FormValue("password")
	if !h.authCfg.ValidateCredentials(username, password) {
		if h.isConfiguredLoginUser(username) {
			h.recordFailedLoginAttempt(now)
		}
		encodedNext := c.FormValue("next")
		nextPath, _ := auth.DecodeNext(encodedNext)
		loginURL := auth.RedirectURL(nextPath)
		return c.Redirect(http.StatusFound, loginURL)
	}

	h.clearFailedLoginAttempts()
	token, err := h.authCfg.SignToken(now)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create session")
	}
	secure := c.Scheme() == "https"
	c.SetCookie(h.authCfg.SessionCookie(token, secure))

	nextPath, err := auth.DecodeNext(c.FormValue("next"))
	if err != nil {
		nextPath = "/"
	}
	return c.Redirect(http.StatusFound, nextPath)
}

func (h *Handler) HostedSSOStart(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.authMode != auth.AuthModeHostedSSO {
		return c.NoContent(http.StatusNotFound)
	}
	if h.hostedSSOClient == nil || h.hostedPendingStore == nil || len(h.hostedSSOKey) != 32 {
		return echo.NewHTTPError(http.StatusInternalServerError, "hosted SSO unavailable")
	}
	destination, err := auth.DecodeHostedNext(c.Request().URL.RawQuery, len(c.Request().URL.RequestURI()))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "bad request")
	}
	if h.recognizeSession(c.Request(), time.Now()).hostedClaims != nil {
		return c.Redirect(http.StatusFound, destination)
	}

	browserNonce := ""
	if cookie, cookieErr := c.Request().Cookie("ov_sso_browser"); cookieErr == nil {
		browserNonce, _ = auth.VerifyBrowserBinding(cookie.Value, h.hostedSSOKey)
	}
	if browserNonce == "" {
		nonceBytes := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to start hosted SSO")
		}
		browserNonce = base64.RawURLEncoding.EncodeToString(nonceBytes)
	}

	challenge := ""
	tx, err := h.hostedPendingStore.Admit(browserNonce, destination, func() (string, string, error) {
		state, verifier, generatedChallenge, generateErr := auth.GenerateStateAndPKCE(rand.Reader)
		challenge = generatedChallenge
		return state, verifier, generateErr
	})
	if err != nil {
		if errors.Is(err, auth.ErrPendingFull) || errors.Is(err, auth.ErrStartRateLimited) {
			c.Response().Header().Set("Retry-After", "1")
			return c.NoContent(http.StatusTooManyRequests)
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to start hosted SSO")
	}
	authorizeURL, err := h.hostedSSOClient.AuthorizationURL(tx.State, challenge)
	if err != nil {
		h.hostedPendingStore.Discard(tx.State)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to start hosted SSO")
	}
	binding, err := auth.SignBrowserBinding(browserNonce, h.hostedSSOKey)
	if err != nil {
		h.hostedPendingStore.Discard(tx.State)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to start hosted SSO")
	}
	c.SetCookie(h.hostedBrowserCookie(binding))
	return c.Redirect(http.StatusFound, authorizeURL)
}

func (h *Handler) HostedSSOCallback(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	c.Response().Header().Set("Pragma", "no-cache")
	if h.authMode != auth.AuthModeHostedSSO {
		return c.NoContent(http.StatusNotFound)
	}
	if h.hostedSSOClient == nil || h.hostedPendingStore == nil || len(h.hostedSSOKey) != 32 {
		return h.hostedRetryPage(c, http.StatusBadRequest, "/")
	}
	callback, err := auth.ParseHostedCallback(c.Request().URL.RawQuery, len(c.Request().URL.RequestURI()))
	if err != nil {
		return h.hostedRetryPage(c, http.StatusBadRequest, "/")
	}
	browserCookie, err := c.Request().Cookie("ov_sso_browser")
	if err != nil {
		return h.hostedRetryPage(c, http.StatusBadRequest, "/")
	}
	browserNonce, err := auth.VerifyBrowserBinding(browserCookie.Value, h.hostedSSOKey)
	if err != nil {
		return h.hostedRetryPage(c, http.StatusBadRequest, "/")
	}
	tx, ok, remaining := h.hostedPendingStore.Consume(callback.State, browserNonce)
	if !ok {
		if !h.hostedPendingStore.HasBrowser(browserNonce) {
			c.SetCookie(h.clearHostedBrowserCookie())
		}
		return h.hostedRetryPage(c, http.StatusBadRequest, "/")
	}
	if !remaining {
		c.SetCookie(h.clearHostedBrowserCookie())
	}
	if callback.IsError {
		return h.hostedRetryPage(c, http.StatusBadRequest, tx.Destination)
	}
	identity, err := h.hostedSSOClient.Exchange(c.Request().Context(), callback.Code, tx.Verifier)
	if err != nil {
		var exchangeErr *auth.ExchangeError
		if errors.As(err, &exchangeErr) {
			applog.Infof("hosted SSO token exchange failed status=%d category=%s", exchangeErr.Status, exchangeErr.Category)
		} else {
			applog.Infof("hosted SSO token exchange failed status=0 category=exchange_failed")
		}
		return h.hostedRetryPage(c, http.StatusBadGateway, tx.Destination)
	}
	now := time.Now()
	claims := auth.SessionClaims{
		Version: 1, Subject: identity.Subject, Email: identity.Email, Display: identity.Email,
		InstanceID: identity.InstanceID, AuthSource: auth.HostedAuthSource,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(auth.HostedSessionLifetime).Unix(),
	}
	token, err := auth.SignHostedSession(claims, h.hostedSSOKey)
	if err != nil {
		return h.hostedRetryPage(c, http.StatusBadGateway, tx.Destination)
	}
	c.SetCookie(h.hostedSessionCookie(token, now))
	return c.Redirect(http.StatusFound, tx.Destination)
}

func (h *Handler) LoggedOut(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.authMode != auth.AuthModeHostedSSO {
		return c.Redirect(http.StatusFound, "/login")
	}
	body := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Signed out - OpenVibely</title></head><body><main><h1>Workspace session ended</h1><p>You have logged out of this workspace. Your hosted account session may still be active.</p><a href="/auth/sso/start">Sign in again</a></main></body></html>`
	return c.HTML(http.StatusOK, body)
}

func (h *Handler) hostedSecureCookies() bool {
	return strings.HasPrefix(h.appBaseURL, "https://")
}

func (h *Handler) hostedCookie(name, value, path string, maxAge int, expires time.Time) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: path, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: h.hostedSecureCookies(), MaxAge: maxAge,
		Expires: expires,
	}
}

func (h *Handler) hostedSessionCookie(token string, now time.Time) *http.Cookie {
	return h.hostedCookie(
		auth.DefaultCookieName,
		token,
		"/",
		int(auth.HostedSessionLifetime.Seconds()),
		now.Add(auth.HostedSessionLifetime),
	)
}

func (h *Handler) hostedSessionDeletionCookie() *http.Cookie {
	return h.hostedCookie(auth.DefaultCookieName, "", "/", -1, time.Unix(0, 0).UTC())
}

func (h *Handler) hostedBrowserCookie(value string) *http.Cookie {
	return h.hostedCookie("ov_sso_browser", value, "/auth/sso", 600, time.Now().Add(10*time.Minute))
}

func (h *Handler) clearHostedBrowserCookie() *http.Cookie {
	return h.hostedCookie("ov_sso_browser", "", "/auth/sso", -1, time.Unix(0, 0).UTC())
}

func (h *Handler) hostedRetryPage(c echo.Context, status int, destination string) error {
	restart := template.HTMLEscapeString(auth.HostedSSOStartURL(destination))
	body := `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Sign-in failed - OpenVibely</title></head><body><main><h1>Sign-in could not be completed</h1><p>Please restart workspace sign-in.</p><a href="` + restart + `">Try again</a></main></body></html>`
	return c.HTML(status, body)
}

func (h *Handler) isConfiguredLoginUser(username string) bool {
	if h.authCfg == nil {
		return false
	}
	cfgUser := strings.TrimSpace(h.authCfg.Username)
	inputUser := strings.TrimSpace(username)
	if cfgUser == "" || inputUser == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(inputUser), []byte(cfgUser)) == 1
}

func (h *Handler) isLoginLocked(now time.Time) bool {
	h.loginFailuresMu.Lock()
	defer h.loginFailuresMu.Unlock()

	h.pruneLoginFailuresLocked(now)
	if h.loginLockedUntil.After(now) {
		return true
	}
	if !h.loginLockedUntil.IsZero() {
		h.loginLockedUntil = time.Time{}
	}
	return false
}

func (h *Handler) recordFailedLoginAttempt(now time.Time) {
	h.loginFailuresMu.Lock()
	defer h.loginFailuresMu.Unlock()

	h.pruneLoginFailuresLocked(now)
	h.loginFailureTimes = append(h.loginFailureTimes, now)
	if len(h.loginFailureTimes) >= loginFailureThreshold {
		h.loginLockedUntil = now.Add(loginLockDuration)
	}
}

func (h *Handler) clearFailedLoginAttempts() {
	h.loginFailuresMu.Lock()
	defer h.loginFailuresMu.Unlock()
	h.loginFailureTimes = nil
	h.loginLockedUntil = time.Time{}
}

func (h *Handler) pruneLoginFailuresLocked(now time.Time) {
	if len(h.loginFailureTimes) == 0 {
		return
	}
	cutoff := now.Add(-loginFailureWindow)
	idx := 0
	for idx < len(h.loginFailureTimes) && h.loginFailureTimes[idx].Before(cutoff) {
		idx++
	}
	if idx > 0 {
		remaining := len(h.loginFailureTimes) - idx
		if remaining <= 0 {
			h.loginFailureTimes = nil
			return
		}
		trimmed := make([]time.Time, remaining)
		copy(trimmed, h.loginFailureTimes[idx:])
		h.loginFailureTimes = trimmed
	}
}

func (h *Handler) authPrincipalID(c echo.Context) string {
	if c != nil {
		if value := c.Get("auth_user"); value != nil {
			if user, ok := value.(*auth.User); ok && strings.TrimSpace(user.Username) != "" {
				return "web:" + strings.TrimSpace(user.Username)
			}
		}
	}
	return "local"
}

func (h *Handler) AuthLogout(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.authMode == auth.AuthModeHostedSSO {
		origins := c.Request().Header.Values("Origin")
		if len(origins) != 1 || origins[0] != h.appBaseURL {
			return c.NoContent(http.StatusForbidden)
		}
		c.SetCookie(h.hostedSessionDeletionCookie())
		return c.Redirect(http.StatusFound, "/logged-out")
	}
	if h.authCfg != nil {
		secure := c.Scheme() == "https"
		c.SetCookie(h.authCfg.ClearSessionCookie(secure))
	}
	return c.Redirect(http.StatusFound, "/login")
}

func (h *Handler) AuthMe(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if h.authMode == auth.AuthModeHostedSSO {
		session := h.recognizeSession(c.Request(), time.Now())
		if session.hostedClaims == nil {
			if session.hostedCookieState == hostedCookieInvalid {
				c.SetCookie(h.hostedSessionDeletionCookie())
			}
			return c.JSON(http.StatusUnauthorized, map[string]any{"authenticated": false})
		}
		claims := session.hostedClaims
		return c.JSON(http.StatusOK, map[string]any{
			"authenticated": true,
			"auth_source":   auth.HostedAuthSource,
			"subject":       claims.Subject,
			"email":         claims.Email,
			"username":      claims.Email,
			"display":       claims.Display,
		})
	}
	if !h.authEnabled() {
		return c.JSON(http.StatusOK, map[string]any{
			"authenticated": false,
		})
	}

	if v := c.Get("auth_user"); v != nil {
		if u, ok := v.(*auth.User); ok {
			return c.JSON(http.StatusOK, map[string]any{
				"authenticated": true,
				"username":      u.Username,
				"display":       u.Display,
			})
		}
	}

	session := h.recognizeSession(c.Request(), time.Now())
	if session.err != nil {
		if errors.Is(session.err, http.ErrNoCookie) || errors.Is(session.err, auth.ErrExpiredToken) || errors.Is(session.err, auth.ErrInvalidToken) {
			return c.JSON(http.StatusUnauthorized, map[string]any{"authenticated": false})
		}
		return echo.NewHTTPError(http.StatusUnauthorized, fmt.Sprintf("unauthorized: %v", session.err))
	}
	user := session.localUser

	return c.JSON(http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      user.Username,
		"display":       user.Display,
	})
}
