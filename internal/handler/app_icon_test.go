package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestAppIconRoutes(t *testing.T) {
	e := echo.New()
	h := &Handler{}
	e.GET("/favicon.png", h.AppIcon)
	e.GET("/favicon.ico", h.AppIcon)

	for _, path := range []string{"/favicon.png", "/favicon.ico"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != "image/png" {
				t.Fatalf("Content-Type = %q, want image/png", got)
			}
			if !bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
				t.Fatal("response is not a PNG")
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}
