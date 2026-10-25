package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThemesFullPageResponse(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/themes?project_id=test-project", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, fragment := range []string{
		`<title>Appearance - OpenVibely</title>`,
		`id="themes-container"`,
		`data-nav-base="/themes"`,
		`href="/themes?project_id=`,
		`data-color-theme=`,
		`id="ov-theme-catalog"`,
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("full Themes page missing %q", fragment)
		}
	}
}

func TestThemesHTMXFragmentResponse(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := htmxGet(e, "/themes?project_id=test-project")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="themes-container"`) || !strings.Contains(body, `data-openvibely-page-title="Appearance - OpenVibely"`) {
		t.Fatalf("HTMX Themes fragment missing container/title marker: %s", body)
	}
	if strings.Contains(body, "<!DOCTYPE html>") || strings.Contains(body, `<aside id="sidebar"`) {
		t.Fatalf("HTMX Themes response should be a fragment, not a full shell")
	}
}
