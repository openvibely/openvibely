package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestModelOAuthSetupSignsInAndDiscoversBeforeCreate(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	t.Setenv("OAUTH_REDIRECT_MODE", "hosted")
	t.Setenv("APP_BASE_URL", "https://app.example.com")
	requests := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			w.Write([]byte(`{"access_token":"setup-access","refresh_token":"setup-refresh","expires_in":3600}`))
		case "/models":
			requests++
			if r.Header.Get("Authorization") != "Bearer setup-access" {
				t.Error("discovery missing OAuth token")
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"data":[{"id":"qwen","max_model_len":262144}]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	h, e, repo := setupTestHandler(t)
	before, _ := repo.List(context.Background())
	form := url.Values{"name": {"Custom OAuth model"}, "provider": {"openai_compatible_custom"}, "preset_slug": {"custom"}, "custom_auth_method": {"oauth"}, "base_url": {provider.URL}, "models_url": {provider.URL + "/models"}, "oauth_authorize_url": {provider.URL + "/authorize"}, "oauth_token_url": {provider.URL + "/token"}, "oauth_client_id": {"test-client"}, "custom_allow_private_endpoints": {"on"}, "custom_standard_token_fields": {"on"}, "custom_oauth_pkce": {"on"}}
	send := func(method, path string, values url.Values, session string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if session != "" {
			req.Header.Set(modelOAuthSetupHeader, session)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	rec := send(http.MethodPost, "/models/oauth/setup", form, "")
	if rec.Code != 200 {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	var setup struct {
		SessionID string `json:"session_id"`
		URL       string `json:"authorization_url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeModelOAuthSetup(setup.SessionID) })
	after, _ := repo.List(context.Background())
	if len(after) != len(before) {
		t.Fatal("sign-in created a model")
	}
	authURL, err := url.Parse(setup.URL)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(setup.URL, setup.SessionID) {
		t.Fatal("setup capability exposed to OAuth provider")
	}
	result := h.completeOAuthFlow(authURL.Query().Get("state"), "test-code")
	if result.Outcome != oauthCompletionSucceeded {
		t.Fatalf("callback: %v", result.Err)
	}
	after, _ = repo.List(context.Background())
	if len(after) != len(before) {
		t.Fatal("callback created a model")
	}
	rec = send(http.MethodGet, "/models/oauth/setup/status", nil, setup.SessionID)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"connected":true`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "setup-access") {
		t.Fatal("status exposed credentials")
	}
	query := url.Values{"base_url": {provider.URL}, "models_url": {provider.URL + "/models"}}
	rec = send(http.MethodGet, "/models/openai-compatible/available?"+query.Encode(), nil, setup.SessionID)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "qwen") {
		t.Fatalf("discovery: %d %s", rec.Code, rec.Body.String())
	}
	query.Set("base_url", provider.URL+"/changed")
	rec = send(http.MethodGet, "/models/openai-compatible/available?"+query.Encode(), nil, setup.SessionID)
	if rec.Code != http.StatusConflict || requests != 1 {
		t.Fatal("discovery allowed changing authenticated endpoint")
	}
	form.Set("oauth_setup_session", setup.SessionID)
	// A model is still required when creating; signing in never bypasses validation.
	rec = send(http.MethodPost, "/models", form, "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create without model: %d", rec.Code)
	}
	form.Set("model", "qwen")
	form.Set("base_url", provider.URL+"/changed")
	rec = send(http.MethodPost, "/models", form, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("changed connection accepted: %d %s", rec.Code, rec.Body.String())
	}
	form.Set("base_url", provider.URL)
	rec = send(http.MethodPost, "/models", form, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	after, _ = repo.List(context.Background())
	if len(after) != len(before)+1 {
		t.Fatal("expected exactly one completed model")
	}
	var saved *models.LLMConfig
	for i := range after {
		if after[i].Name == "Custom OAuth model" {
			saved = &after[i]
		}
	}
	if saved == nil || saved.Model != "qwen" || saved.OAuthAccessToken != "setup-access" || saved.OAuthRefreshToken != "setup-refresh" {
		t.Fatal("selected model and OAuth credentials not persisted together")
	}
	if _, err := h.modelOAuthSetupConfig(setup.SessionID, true); err == nil {
		t.Fatal("consumed setup session still usable")
	}
	// Editing signs in against the new endpoint without changing the saved model.
	saved.OAuthAccessToken = "old-access"
	saved.OAuthClientSecret = "saved-secret"
	if err := repo.Update(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	form.Del("oauth_setup_session")
	form.Set("model_config_id", saved.ID)
	form.Set("base_url", provider.URL+"/new")
	form.Set("model", "")
	rec = send(http.MethodPost, "/models/oauth/setup", form, "")
	if rec.Code != 200 {
		t.Fatalf("edit setup: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &setup); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeModelOAuthSetup(setup.SessionID) })
	snapshot, err := h.modelOAuthSetupConfig(setup.SessionID, false)
	if err != nil || snapshot.OAuthClientSecret != "saved-secret" || snapshot.ID != "" || snapshot.OAuthAccessToken != "" {
		t.Fatalf("edit snapshot: %#v %v", snapshot, err)
	}
	authURL, _ = url.Parse(setup.URL)
	result = h.completeOAuthFlow(authURL.Query().Get("state"), "edit-code")
	if result.Outcome != oauthCompletionSucceeded {
		t.Fatalf("edit callback: %v", result.Err)
	}
	unchanged, _ := repo.GetByID(context.Background(), saved.ID)
	if unchanged.BaseURL != provider.URL || unchanged.OAuthAccessToken != "old-access" {
		t.Fatal("sign-in changed saved configuration before Save")
	}
	query.Set("base_url", provider.URL+"/new")
	rec = send(http.MethodGet, "/models/openai-compatible/available?"+query.Encode(), nil, setup.SessionID)
	if rec.Code != 200 {
		t.Fatalf("edit discovery: %d %s", rec.Code, rec.Body.String())
	}
	form.Set("oauth_setup_session", setup.SessionID)
	rec = send(http.MethodPut, "/models/"+saved.ID, form, "")
	if rec.Code != 400 {
		t.Fatalf("edit without selection: %d", rec.Code)
	}
	form.Set("model", "qwen")
	form.Set("base_url", provider.URL+"/tampered")
	rec = send(http.MethodPut, "/models/"+saved.ID, form, "")
	if rec.Code != 409 {
		t.Fatalf("edit with changed authenticated settings: %d", rec.Code)
	}
	form.Set("base_url", provider.URL+"/new")
	rec = send(http.MethodPut, "/models/"+saved.ID, form, "")
	if rec.Code != 303 {
		t.Fatalf("save edit: %d %s", rec.Code, rec.Body.String())
	}
	updated, _ := repo.GetByID(context.Background(), saved.ID)
	if updated.BaseURL != provider.URL+"/new" || updated.OAuthAccessToken != "setup-access" || updated.OAuthClientSecret != "saved-secret" {
		t.Fatal("edited settings and new credentials not saved together")
	}
	if _, err := h.modelOAuthSetupConfig(setup.SessionID, true); err == nil {
		t.Fatal("edit session not consumed")
	}
	after, _ = repo.List(context.Background())
	if len(after) != len(before)+1 {
		t.Fatal("editing created another model")
	}

}

func TestModelOAuthSetupCancellationAndExpiry(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	before, _ := repo.List(context.Background())
	for _, scenario := range []string{"cancel", "expire", "denied"} {
		t.Run(scenario, func(t *testing.T) {
			key := "test-setup-" + scenario
			modelOAuthSetups.Lock()
			session := &modelOAuthSetup{owner: h, expires: time.Now().Add(time.Minute)}
			if scenario == "expire" {
				session.expires = time.Now().Add(-time.Minute)
			}
			modelOAuthSetups.sessions[key] = session
			modelOAuthSetups.Unlock()
			defer removeModelOAuthSetup(key)
			if scenario == "denied" {
				failModelOAuthSetup(key)
			}
			method, path := http.MethodGet, "/models/oauth/setup/status"
			if scenario == "cancel" {
				method, path = http.MethodDelete, "/models/oauth/setup"
			}
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set(modelOAuthSetupHeader, key)
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			want := http.StatusBadRequest
			if scenario == "cancel" {
				want = http.StatusNoContent
			}
			if rec.Code != want {
				t.Fatalf("%s: %d %s", scenario, rec.Code, rec.Body.String())
			}
			if _, err := h.modelOAuthSetupConfig(key, true); err == nil {
				t.Fatal("cancelled, expired or failed sign-in remained usable")
			}
		})
	}
	after, _ := repo.List(context.Background())
	if len(after) != len(before) {
		t.Fatal("setup cleanup changed model records")
	}
}

// The built-in modal saves without an HTML swap, then authorizes that exact
// persisted account and observes a new credential generation before finishing.
func TestBuiltInModelOAuthModalSaveAndConnect(t *testing.T) {
	for _, providerName := range []string{"anthropic", "openai"} {
		t.Run(providerName, func(t *testing.T) {
			t.Setenv("OAUTH_REDIRECT_MODE", "hosted")
			t.Setenv("APP_BASE_URL", "https://app.example.com")
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"access_token":"modal-access","refresh_token":"modal-refresh","expires_in":3600}`))
			}))
			defer provider.Close()
			t.Setenv(strings.ToUpper(providerName)+"_OAUTH_TOKEN_URL", provider.URL)
			h, e, repo := setupTestHandler(t)
			h.openAIIdentityResolver = nil
			h.oauthIdentityResolver = nil
			originalOpen := openOAuthURL
			opened := 0
			openOAuthURL = func(string) error { opened++; return nil }
			t.Cleanup(func() { openOAuthURL = originalOpen })
			send := func(method, path string, form url.Values) map[string]string {
				t.Helper()
				req := httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Accept", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
				}
				var data map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
					t.Fatal(err)
				}
				return data
			}
			model := "claude-sonnet-4-5-20250929"
			if providerName == "openai" {
				model = "gpt-5.5"
			}
			form := url.Values{"name": {"Modal OAuth"}, "provider": {providerName}, "model": {model}, providerName + "_auth_type": {"oauth"}, "auth_method": {"oauth"}, "oauth_connection_id": {"new"}}
			saved := send(http.MethodPost, "/models", form)
			if saved["id"] == "" || saved["oauth_connection_id"] == "" {
				t.Fatal("save omitted model/account identity")
			}
			form.Set("model_config_id", saved["id"])
			form.Set("oauth_connection_id", saved["oauth_connection_id"])
			for attempt := 0; attempt < 2; attempt++ {
				// The second save and sign-in exercises reconnect without duplicating the model.
				if attempt > 0 {
					h.desktopMode = true
					send(http.MethodPost, "/models", form)
				}
				req := httptest.NewRequest(http.MethodGet, "/models/"+saved["id"]+"/oauth/initiate", nil)
				req.Header.Set("Accept", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				var flow struct {
					URL          string `json:"authorization_url"`
					Revision     string `json:"revision"`
					ConnectionID string `json:"connection_id"`
					Opened       bool   `json:"opened"`
				}
				if rec.Code != http.StatusOK {
					t.Fatalf("initiate: %d", rec.Code)
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &flow); err != nil {
					t.Fatal(err)
				}
				if flow.Opened != h.desktopMode || opened != attempt {
					t.Fatal("modal did not use the correct browser launch mode")
				}
				pending := send(http.MethodGet, "/models/"+saved["id"]+"/oauth/status", nil)
				if pending["revision"] != flow.Revision || pending["connection_id"] != flow.ConnectionID {
					t.Fatal("initiation does not match persisted connection")
				}
				authorizationURL, err := url.Parse(flow.URL)
				if err != nil {
					t.Fatal(err)
				}
				callback := url.Values{"code": {"modal-code"}, "state": {authorizationURL.Query().Get("state")}}
				manual := `{"callback_url":"http://localhost:1455/auth/callback?` + callback.Encode() + `"}`
				req = httptest.NewRequest(http.MethodPost, "/models/oauth/manual-complete", strings.NewReader(manual))
				req.Header.Set("Content-Type", "application/json")
				rec = httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("complete: %d %s", rec.Code, rec.Body.String())
				}
				state := send(http.MethodGet, "/models/"+saved["id"]+"/oauth/status", nil)
				if state["status"] != "connected" || state["revision"] == flow.Revision {
					t.Fatal("new sign-in not observable in modal")
				}
			}
			all, err := repo.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, cfg := range all {
				if cfg.Name == "Modal OAuth" {
					count++
					if cfg.OAuthAccessToken != "modal-access" {
						t.Fatal("credentials not persisted")
					}
				}
			}
			if count != 1 {
				t.Fatalf("saved %d models", count)
			}
		})
	}
}
