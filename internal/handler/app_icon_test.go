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
	e.GET("/favicon.png", h.AppIconPNG)
	e.GET("/favicon.ico", h.AppIconICO)

	tests := []struct {
		path        string
		contentType string
		signature   []byte
	}{
		{path: "/favicon.png", contentType: "image/png", signature: []byte("\x89PNG\r\n\x1a\n")},
		{path: "/favicon.ico", contentType: "image/x-icon", signature: []byte("\x00\x00\x01\x00")},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, test.path, nil)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if got := rec.Header().Get("Content-Type"); got != test.contentType {
				t.Fatalf("Content-Type = %q, want %q", got, test.contentType)
			}
			if !bytes.HasPrefix(rec.Body.Bytes(), test.signature) {
				t.Fatalf("response for %s has the wrong file signature", test.path)
			}
			if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
				t.Fatalf("Cache-Control = %q", got)
			}
		})
	}
}
