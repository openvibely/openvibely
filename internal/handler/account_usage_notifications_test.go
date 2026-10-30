package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAccountUsageNotificationSettings(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	for _, tc := range []struct {
		method, body, want string
		code               int
	}{
		{"GET", "", `"enabled":true`, 200}, {"POST", `{"enabled":false}`, `"enabled":false`, 200}, {"GET", "", `"enabled":false`, 200}, {"POST", `{}`, "enabled is required", 400}, {"POST", `{"enabled":true}`, `"enabled":true`, 200},
	} {
		req := httptest.NewRequest(tc.method, "/api/analytics/usage/notifications", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s: %d %s", tc.method, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/usage/notifications?project_id=missing", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
}
