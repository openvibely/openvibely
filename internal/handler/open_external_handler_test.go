package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestOpenExternal_ServerMode_Returns404(t *testing.T) {
	h := &Handler{desktopMode: false}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external?url=https://github.com/org/repo/pull/1", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.OpenExternal(c)
	if err == nil {
		t.Fatal("expected an error (echo.ErrNotFound) in server mode, got nil")
	}
	he, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
	}
	if he.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", he.Code)
	}
}

func TestOpenExternal_DesktopMode_RequiresPostAndDesktopHeader(t *testing.T) {
	tests := []struct {
		name   string
		method string
		header string
	}{
		{name: "get", method: http.MethodGet, header: "1"},
		{name: "missing_header", method: http.MethodPost, header: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{desktopMode: true}
			e := echo.New()
			req := httptest.NewRequest(tt.method, "/open-external?url=https://github.com/org/repo/pull/7", nil)
			if tt.header != "" {
				req.Header.Set("X-OpenVibely-Desktop", tt.header)
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			err := h.OpenExternal(c)
			if err == nil {
				t.Fatal("expected forbidden error")
			}
			he, ok := err.(*echo.HTTPError)
			if !ok {
				t.Fatalf("expected *echo.HTTPError, got %T: %v", err, err)
			}
			if he.Code != http.StatusForbidden {
				t.Errorf("expected 403, got %d", he.Code)
			}
		})
	}
}

func TestOpenExternal_DesktopMode_MissingURL_Returns400(t *testing.T) {
	h := &Handler{desktopMode: true}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.OpenExternal(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestOpenExternal_DesktopMode_InvalidScheme_Returns400(t *testing.T) {
	h := &Handler{desktopMode: true}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external?url=javascript:alert(1)", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	err := h.OpenExternal(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for non-http(s)/mailto scheme, got %d", rec.Code)
	}
}

func TestOpenExternal_DesktopMode_HostlessWebURL_Returns400(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "https_opaque", url: "https:github.com/openvibely/openvibely/pull/1"},
		{name: "http_opaque", url: "http:example.com/docs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			orig := openExternalURL
			defer func() { openExternalURL = orig }()
			openExternalURL = func(url string) error {
				called = true
				return nil
			}

			h := &Handler{desktopMode: true}
			e := echo.New()
			req := httptest.NewRequest(http.MethodPost, "/open-external?url="+url.QueryEscape(tt.url), nil)
			req.Header.Set("X-OpenVibely-Desktop", "1")
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			if err := h.OpenExternal(c); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if rec.Code != http.StatusBadRequest {
				t.Errorf("expected 400 for hostless web URL %q, got %d", tt.url, rec.Code)
			}
			if called {
				t.Error("openExternalURL must not be called for hostless web URLs")
			}
		})
	}
}

func TestOpenExternal_DesktopMode_ValidURL_Opens(t *testing.T) {
	var openedURL string
	orig := openExternalURL
	defer func() { openExternalURL = orig }()
	openExternalURL = func(url string) error {
		openedURL = url
		return nil
	}

	h := &Handler{desktopMode: true}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external?url=https://github.com/org/repo/pull/7", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.OpenExternal(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	if openedURL != "https://github.com/org/repo/pull/7" {
		t.Errorf("expected opened URL to be the PR URL, got %q", openedURL)
	}
}

func TestOpenExternal_DesktopMode_ValidHTTPURL_Opens(t *testing.T) {
	var openedURL string
	orig := openExternalURL
	defer func() { openExternalURL = orig }()
	openExternalURL = func(url string) error {
		openedURL = url
		return nil
	}

	h := &Handler{desktopMode: true}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external?url=http://example.com/docs", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.OpenExternal(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	if openedURL != "http://example.com/docs" {
		t.Errorf("expected opened URL to be http docs URL, got %q", openedURL)
	}
}

func TestOpenExternal_DesktopMode_MailtoURL_Opens(t *testing.T) {
	var openedURL string
	orig := openExternalURL
	defer func() { openExternalURL = orig }()
	openExternalURL = func(url string) error {
		openedURL = url
		return nil
	}

	h := &Handler{desktopMode: true}
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external?url=mailto%3Ahello%40example.com", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.OpenExternal(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}
	if openedURL != "mailto:hello@example.com" {
		t.Errorf("expected opened URL to be mailto URL, got %q", openedURL)
	}
}

func TestOpenExternal_DesktopMode_OpenError_Returns500(t *testing.T) {
	orig := openExternalURL
	defer func() { openExternalURL = orig }()
	openExternalURL = func(url string) error {
		return &testOpenError{"no browser available"}
	}

	h := &Handler{desktopMode: true}

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/open-external?url=https://github.com/org/repo/pull/7", nil)
	req.Header.Set("X-OpenVibely-Desktop", "1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	if err := h.OpenExternal(c); err != nil {
		t.Fatalf("unexpected echo error: %v", err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

type testOpenError struct{ msg string }

func (e *testOpenError) Error() string { return e.msg }
