package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestPinnedProjectPreferences(t *testing.T) {
	h, svc := setupProjectTestHandler(t)
	projects, err := svc.List(context.Background())
	if err != nil || len(projects) == 0 {
		t.Fatal("missing fixture projects", err)
	}
	id := projects[0].ID
	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{"pinned_project_ids":["` + id + `"]}`, 204, `["` + id + `"]`},
		{`{"pinned_project_ids":["missing"]}`, 400, `["` + id + `"]`},
		{`{"pinned_project_ids":["` + id + `","` + id + `"]}`, 400, `["` + id + `"]`},
		{`{"pinned_project_ids":[]}`, 204, `[]`},
	} {
		req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		e := echo.New()
		c := e.NewContext(req, rec)
		if err := h.SaveUIPreferences(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
		if rec.Code != tc.status {
			t.Fatalf("%s: status %d: %s", tc.body, rec.Code, rec.Body.String())
		}
		got, err := h.settingsRepo.Get(context.Background(), "ui.pinned_project_ids")
		if err != nil || got != tc.want {
			t.Fatalf("pins = %q, want %q: %v", got, tc.want, err)
		}
		// The full-document bootstrap reads the durable value, not WebView storage.
		prefs, _ := json.Marshal(h.uiPreferences(context.Background()))
		if !strings.Contains(string(prefs), `"PinnedProjectIDs":`+tc.want) {
			t.Fatalf("bootstrap missing pins: %s", prefs)
		}
	}
}

func TestProjectLocationPreferences(t *testing.T) {
	h, svc := setupProjectTestHandler(t)
	projects, _ := svc.List(context.Background())
	id := projects[0].ID
	path := "/tasks?project_id=" + id + "&view=list#recent"
	for _, value := range []string{path, "https://example.com/tasks?project_id=" + id, "/tasks?project_id=wrong"} {
		body, _ := json.Marshal(map[string]any{"project_locations": map[string]string{id: value}})
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest("POST", "/ui/preferences", strings.NewReader(string(body))), rec)
		if err := h.SaveUIPreferences(c); err != nil {
			e.HTTPErrorHandler(err, c)
		}
		want := 400
		if value == path {
			want = 204
		}
		if rec.Code != want {
			t.Fatalf("%s: got %d want %d", value, rec.Code, want)
		}
	}
	got, _ := h.settingsRepo.Get(context.Background(), "ui.project_locations")
	if !strings.Contains(got, "recent") {
		t.Fatalf("location not persisted: %s", got)
	}
}
