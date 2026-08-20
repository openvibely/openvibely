package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
)

func TestWorktreeHandler_UpdateSettingsUsesCanonicalToastTrigger(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := worktreeFormRequest(http.MethodPost, "/settings/worktree", url.Values{
		"worktree_auto_merge": {"true"},
	})
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	trigger := rec.Header().Get("HX-Trigger")
	var data map[string]any
	if err := json.Unmarshal([]byte(trigger), &data); err != nil {
		t.Fatalf("HX-Trigger should be valid JSON, got %q: %v", trigger, err)
	}
	if _, ok := data["showToast"]; ok {
		t.Fatalf("worktree settings must not emit legacy showToast trigger: %s", trigger)
	}
	toast, ok := data["openvibelyToast"].(map[string]any)
	if !ok {
		t.Fatalf("expected canonical openvibelyToast trigger, got %s", trigger)
	}
	if toast["message"] != "Worktree settings saved" {
		t.Fatalf("unexpected toast message: %#v", toast["message"])
	}
	if toast["status"] != "completed" {
		t.Fatalf("expected completed status for saved settings toast, got %#v", toast["status"])
	}
}
