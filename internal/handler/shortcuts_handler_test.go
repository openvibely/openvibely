package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShortcutsResponses(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	for _, fragment := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/shortcuts?project_id=test-project", nil)
		if fragment {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("fragment=%v status=%d", fragment, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{`id="shortcuts-container"`, `data-shortcut-search`, `data-shortcut-category`, `Previous task`, `data-openvibely-page-title="Shortcuts - OpenVibely"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("fragment=%v missing %s", fragment, want)
			}
		}
		if strings.Contains(body, `id="sidebar"`) == fragment {
			t.Fatalf("incorrect shell for fragment=%v", fragment)
		}
	}
}
