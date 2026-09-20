package handler

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/pkg/browser"
)

// openExternalURL is the function used to open a URL in the system browser.
// Swapped out in tests.
var openExternalURL = browser.OpenURL

// OpenExternal opens a validated URL in the system browser.
// It is only active in desktop mode; in server mode it returns 404.
//
// POST /open-external?url=<encoded-url>
func (h *Handler) OpenExternal(c echo.Context) error {
	if !h.desktopMode {
		return echo.ErrNotFound
	}
	if c.Request().Method != http.MethodPost || c.Request().Header.Get("X-OpenVibely-Desktop") != "1" {
		return echo.ErrForbidden
	}

	rawURL := strings.TrimSpace(c.QueryParam("url"))
	if rawURL == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "url parameter is required"})
	}

	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http" && parsed.Scheme != "mailto") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "url must be a valid http/https/mailto URL"})
	}
	// url.Parse accepts scheme-only values like "https:github.com/org/repo"
	// (empty Host, opaque path). Those are not usable web URLs.
	if (parsed.Scheme == "http" || parsed.Scheme == "https") && (parsed.Host == "" || parsed.Opaque != "") {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "url must be a valid http/https/mailto URL"})
	}

	if err := openExternalURL(rawURL); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to open URL: " + err.Error()})
	}

	return c.NoContent(http.StatusNoContent)
}
