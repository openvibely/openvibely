package handler

import (
	"context"
	"io"
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
)

// DesktopFileSaver prompts for a destination and saves an authorized original.
// The destination is chosen exclusively by the native dialog, never by HTTP.
type DesktopFileSaver func(context.Context, string, io.Reader) (canceled bool, err error)

const desktopSaveKey = "desktop-file-saver"

func (h *Handler) SetDesktopFileSaver(save DesktopFileSaver) { h.desktopFileSaver = save }

func (h *Handler) saveDesktopFile(authorize echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if h.desktopFileSaver == nil {
			return echo.NewHTTPError(http.StatusNotImplemented, "Desktop saving is unavailable")
		}
		origin, err := url.Parse(c.Request().Header.Get("Origin"))
		sameOrigin := err == nil && origin.Host == c.Request().Host && (origin.Scheme == "http" || origin.Scheme == "https")
		// WKWebView/WebKitGTK use a custom wails://localhost origin, which can
		// serialize as null. Only the native asset proxy supplies this marker.
		nativeOrigin := c.Request().Header.Get("X-OpenVibely-Native-Window") == "1" && c.Request().Host == "localhost" && (c.Request().Header.Get("Origin") == "null" || c.Request().Header.Get("Origin") == "" || c.Request().Header.Get("Origin") == "wails://localhost")
		if (!sameOrigin && !nativeOrigin) || c.Request().Header.Get("X-OpenVibely-Save") != "1" {
			return echo.NewHTTPError(http.StatusForbidden, "Invalid save request")
		}
		// Force the original-file path, not metadata or a rendered preview.
		query := c.Request().URL.Query()
		for _, key := range []string{"metadata", "view", "raw", "thumbnail"} {
			query.Del(key)
		}
		query.Set("download", "1")
		c.Request().URL.RawQuery = query.Encode()
		c.Set(desktopSaveKey, h.desktopFileSaver)
		return authorize(c)
	}
}
