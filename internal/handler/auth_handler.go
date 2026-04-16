package handler

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
)

const (
	loginFailureWindow      = time.Hour
	loginFailureThreshold   = 3
	loginLockDuration       = 6 * time.Hour
)

func (h *Handler) SetAuthConfig(cfg auth.Config) {
	norm := cfg.Normalized()
	h.authCfg = &norm
}

func (h *Handler) authEnabled() bool {
	return h.authCfg != nil && h.authCfg.Enabled
}

func (h *Handler) isAuthPublicPath(path string) bool {
	if path == "/login" || path == "/logout" || path == "/auth/me" {
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

			user, err := auth.UserFromRequest(c.Request(), *h.authCfg, time.Now())
			if err == nil {
				c.Set("auth_user", user)
				return next(c)
			}

			nextURL := c.Request().URL.RequestURI()
			loginURL := auth.RedirectURL(nextURL)
			if c.Request().Header.Get("HX-Request") == "true" {
				c.Response().Header().Set("HX-Redirect", loginURL)
				return c.NoContent(http.StatusUnauthorized)
			}
			return c.Redirect(http.StatusFound, loginURL)
		}
	}
}

func (h *Handler) AuthLoginPage(c echo.Context) error {
	if !h.authEnabled() {
		return c.Redirect(http.StatusFound, "/")
	}
	if _, err := auth.UserFromRequest(c.Request(), *h.authCfg, time.Now()); err == nil {
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

func (h *Handler) AuthLogout(c echo.Context) error {
	if h.authCfg != nil {
		secure := c.Scheme() == "https"
		c.SetCookie(h.authCfg.ClearSessionCookie(secure))
	}
	return c.Redirect(http.StatusFound, "/login")
}

func (h *Handler) AuthMe(c echo.Context) error {
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

	user, err := auth.UserFromRequest(c.Request(), *h.authCfg, time.Now())
	if err != nil {
		if errors.Is(err, http.ErrNoCookie) || errors.Is(err, auth.ErrExpiredToken) || errors.Is(err, auth.ErrInvalidToken) {
			return c.JSON(http.StatusUnauthorized, map[string]any{"authenticated": false})
		}
		return echo.NewHTTPError(http.StatusUnauthorized, fmt.Sprintf("unauthorized: %v", err))
	}

	return c.JSON(http.StatusOK, map[string]any{
		"authenticated": true,
		"username":      user.Username,
		"display":       user.Display,
	})
}
