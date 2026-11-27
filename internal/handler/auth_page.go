package handler

import (
	"bytes"
	"runtime"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func (h *Handler) nativeAuthWindow(c echo.Context) bool {
	return h.desktopMode && c.Request().Header.Get("X-OpenVibely-Native-Window") == "1"
}

// Standalone auth documents bypass Base, but still need native window chrome.
func (h *Handler) renderAuthDocument(c echo.Context, status int, body string) error {
	if h.nativeAuthWindow(c) {
		var navigation bytes.Buffer
		if err := layout.DesktopNavigation(runtime.GOOS).Render(c.Request().Context(), &navigation); err != nil {
			return err
		}
		body = strings.Replace(body, "<head>", "<head>"+navigation.String(), 1)
		var bar bytes.Buffer
		if err := layout.AuthTitlebar(runtime.GOOS).Render(c.Request().Context(), &bar); err != nil {
			return err
		}
		if start := strings.Index(body, "<body"); start >= 0 {
			if end := strings.IndexByte(body[start:], '>'); end >= 0 {
				position := start + end + 1
				body = body[:position] + bar.String() + body[position:]
			}
		}
	}
	return c.HTML(status, body)
}
