package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
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

func TestBuiltInModelOAuthConnectBeforeSave(t *testing.T) {
	for _, providerName := range []string{"anthropic", "openai"} {
		t.Run(providerName, func(t *testing.T) {
			t.Setenv("OAUTH_REDIRECT_MODE", "hosted")
			t.Setenv("APP_BASE_URL", "https://app.example.com")
			token := "draft-access"
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"access_token": token, "refresh_token": "draft-refresh", "expires_in": 3600})
			}))
			defer provider.Close()
			t.Setenv(strings.ToUpper(providerName)+"_OAUTH_TOKEN_URL", provider.URL)
			h, e, repo := setupTestHandler(t)
			h.openAIIdentityResolver = func(context.Context, string) (service.OpenAIOAuthIdentity, error) {
				return service.OpenAIOAuthIdentity{AccountID: "account", DisplayName: "Verified user", PrincipalHash: "principal"}, nil
			}
			h.oauthIdentityResolver = func(context.Context, string) (service.AnthropicOAuthIdentity, error) {
				return service.AnthropicOAuthIdentity{AccountID: "account", DisplayName: "Verified user", PrincipalHash: "principal"}, nil
			}
			form := url.Values{"provider": {providerName}, providerName + "_auth_type": {"oauth"}, "auth_method": {"oauth"}, "oauth_connection_id": {"new"}}
			send := func(path string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("Accept", "application/json")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				return rec
			}
			before, _ := repo.List(context.Background())
			var siblingID, connectionID string
			for attempt := 0; attempt < 8; attempt++ {
				if attempt == 2 || (attempt >= 4 && attempt != 7) {
					form.Set("oauth_connection_id", "new")
				}
				if attempt == 7 {
					current, _ := repo.GetByID(context.Background(), form.Get("model_config_id"))
					form.Set("oauth_connection_id", current.OAuthConnectionID)
				}
				rec := send("/models/oauth/setup")
				if rec.Code != 200 {
					t.Fatalf("begin: %d %s", rec.Code, rec.Body.String())
				}
				var flow struct {
					Session string `json:"session_id"`
					URL     string `json:"authorization_url"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &flow); err != nil {
					t.Fatal(err)
				}
				defer removeModelOAuthSetup(flow.Session)
				authURL, _ := url.Parse(flow.URL)
				result := h.completeOAuthFlow(authURL.Query().Get("state"), "code")
				if result.Outcome != oauthCompletionSucceeded {
					t.Fatal(result.Err)
				}
				after, _ := repo.List(context.Background())
				expectedModels := len(before)
				if attempt > 0 {
					expectedModels += 2
				}
				if attempt > 5 {
					expectedModels++
				}
				if len(after) != expectedModels {
					t.Fatal("sign-in persisted a model")
				}
				if attempt > 0 {
					old, _ := repo.GetByID(context.Background(), form.Get("model_config_id"))
					if old.Name != "Connected model" {
						t.Fatal("sign-in saved draft edits")
					}
				}
				form.Set("oauth_setup_session", flow.Session)
				form.Set("name", "Connected model")
				model := "claude-sonnet-4-5-20250929"
				if providerName == "openai" {
					model = "gpt-6.1-sol"
				}
				form.Set("model", model)
				if attempt >= 3 {
					current, _ := repo.GetByID(context.Background(), form.Get("model_config_id"))
					if attempt == 5 {
						// Another modal creates a matching account after this draft
						// completed, so its ID was absent from the snapshot.
						newer := &models.LLMConfig{Name: "Newer account", Provider: current.Provider, Model: current.Model, AuthMethod: models.AuthMethodOAuth}
						if err := repo.Create(context.Background(), newer); err != nil {
							t.Fatal(err)
						}
						current = newer
					}
					if attempt >= 6 {
						// Refresh writes deliberately retain the authorization revision.
						ok, err := repo.UpdateLinkedOAuthConnectionTokensIfRevision(context.Background(), current.ID, current.OAuthConnectionID, current.OAuthConfigRevision, current.Provider, fmt.Sprintf("refreshed-access-%d", attempt), "refreshed-refresh", time.Now().Add(2*time.Hour).UnixMilli())
						if err != nil || !ok {
							t.Fatal("refresh failed", err)
						}
						refreshed, err := repo.GetByID(context.Background(), current.ID)
						if err != nil || refreshed.OAuthConfigRevision != current.OAuthConfigRevision {
							t.Fatal("refresh must retain the authorization revision", err)
						}
					} else {
						if ok, err := repo.ReplaceLinkedOAuthConnectionWithProfileIfRevision(context.Background(), current.ID, current.OAuthConnectionID, current.OAuthConfigRevision, current.Provider, "concurrent-access", "concurrent-refresh", time.Now().Add(time.Hour).UnixMilli(), "account", "Verified user", "principal"); err != nil || !ok {
							t.Fatal(err)
						}
					}
					form.Set("name", "Must roll back")
					rec = send("/models")
					if rec.Code != http.StatusConflict {
						t.Fatalf("stale sign-in status: %d", rec.Code)
					}
					expectedAccess := "concurrent-access"
					if attempt >= 6 {
						expectedAccess = fmt.Sprintf("refreshed-access-%d", attempt)
					}
					unchanged, _ := repo.GetByID(context.Background(), form.Get("model_config_id"))
					if unchanged.Name != "Connected model" || unchanged.OAuthAccessToken != expectedAccess {
						t.Fatal("stale save was not atomic")
					}
					sibling, err := repo.GetByID(context.Background(), siblingID)
					if err != nil || sibling.OAuthConnectionID != unchanged.OAuthConnectionID || sibling.OAuthAccessToken != expectedAccess {
						t.Fatal("stale draft replaced shared credentials", err)
					}
					connections, err := repo.ListOAuthConnections(context.Background(), current.Provider)
					if err != nil || len(connections) != 1 {
						t.Fatal("stale save left an account behind", err)
					}
					form.Set("name", "Unsaved edit")
					form.Del("oauth_setup_session")
					continue
				}
				rec = send("/models")
				if rec.Code != 200 {
					t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
				}
				var saved map[string]string
				json.Unmarshal(rec.Body.Bytes(), &saved)
				cfg, err := repo.GetByID(context.Background(), saved["id"])
				if err != nil || cfg.OAuthAccessToken != token {
					t.Fatal("save lost authorization", err)
				}
				connection, err := repo.GetOAuthConnectionByID(context.Background(), cfg.OAuthConnectionID)
				if err != nil || connection.ProviderDisplayName != "Verified user" || connection.PrincipalHash != "principal" || !connection.PrincipalVerified {
					t.Fatal("verified identity lost", err)
				}
				if attempt == 0 {
					connectionID = cfg.OAuthConnectionID
					sibling := &models.LLMConfig{Name: "Shared sibling", Provider: cfg.Provider, Model: cfg.Model, AuthMethod: models.AuthMethodOAuth, OAuthConnectionID: connectionID}
					if err := repo.Create(context.Background(), sibling); err != nil {
						t.Fatal(err)
					}
					siblingID = sibling.ID
					token = "reconnected-access"
				} else {
					if attempt == 2 {
						old, err := repo.GetOAuthConnectionByID(context.Background(), connectionID)
						if err != nil || old != nil {
							t.Fatal("verified duplicate was not consolidated", err)
						}
						connectionID = cfg.OAuthConnectionID
					}
					sibling, _ := repo.GetByID(context.Background(), siblingID)
					if cfg.OAuthConnectionID != connectionID || sibling.OAuthConnectionID != connectionID || sibling.OAuthAccessToken != token {
						t.Fatal("reconnect detached shared account")
					}
				}
				form.Set("model_config_id", cfg.ID)
				form.Set("oauth_connection_id", cfg.OAuthConnectionID)
				form.Set("name", "Unsaved edit")
				form.Del("oauth_setup_session")
			}
		})
	}
}

func TestOAuthModelLifecycleTriggersSharedAccountUsageSync(t *testing.T) {
	t.Setenv("OAUTH_REDIRECT_MODE", "hosted")
	t.Setenv("APP_BASE_URL", "https://app.example.com")

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/backend-api/codex/") {
			_, _ = w.Write([]byte(`{"available_count":0,"credits":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "live-oauth-access",
			"refresh_token": "live-oauth-refresh",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	oldCodexBase := openaiclient.OpenAIChatGPTAPIBaseURL
	openaiclient.OpenAIChatGPTAPIBaseURL = tokenServer.URL + "/backend-api/codex/"
	t.Cleanup(func() { openaiclient.OpenAIChatGPTAPIBaseURL = oldCodexBase })
	t.Setenv("OPENAI_OAUTH_TOKEN_URL", tokenServer.URL+"/oauth/token")

	h, e, repo, env := setupTestHandlerWithStores(t)
	h.broadcaster = events.NewBroadcaster()
	h.openAIIdentityResolver = func(context.Context, string) (service.OpenAIOAuthIdentity, error) {
		return service.OpenAIOAuthIdentity{
			AccountID:     "acct-usage-sync",
			DisplayName:   "Usage Sync User",
			PrincipalHash: "principal-usage-sync",
		}, nil
	}

	usageSvc := service.NewUsageAnalyticsService(env.Usage, repo)
	var fetchCalls atomic.Int32
	usageSvc.SetAccountUsageFetcher(func(_ context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
		fetchCalls.Add(1)
		pct := 42.0
		return &models.AccountUsageResult{
			PrimaryLabel:       "5-hour session",
			PrimaryUsedPercent: &pct,
		}, nil
	})
	shared := usageSvc.EnableSharedAccountUsage(context.Background(), func() {
		h.broadcaster.Publish(events.TaskEvent{Type: events.AccountUsageUpdated})
	}, nil)
	defer shared.Stop()
	h.SetUsageAnalyticsService(usageSvc)

	sub, _ := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)

	drainUsageUpdated := func() {
		for {
			select {
			case <-sub:
			default:
				return
			}
		}
	}

	waitUsageUpdated := func(step string) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			select {
			case ev := <-sub:
				if ev.Type == events.AccountUsageUpdated {
					return
				}
			case <-deadline:
				t.Fatalf("timed out waiting for AccountUsageUpdated after %s", step)
			}
		}
	}

	waitFetchCount := func(step string, want int32) {
		t.Helper()
		deadline := time.After(3 * time.Second)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if fetchCalls.Load() >= want {
				return
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for %d usage fetches after %s (got %d)", want, step, fetchCalls.Load())
			case <-ticker.C:
			}
		}
	}

	// 1. Complete OAuth setup in modal, then create the OAuth model via POST /models.
	setupForm := url.Values{
		"provider":            {"openai"},
		"openai_auth_type":    {"oauth"},
		"auth_method":         {"oauth"},
		"oauth_connection_id": {"new"},
	}
	setupReq := httptest.NewRequest(http.MethodPost, "/models/oauth/setup", strings.NewReader(setupForm.Encode()))
	setupReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setupRec := httptest.NewRecorder()
	e.ServeHTTP(setupRec, setupReq)
	if setupRec.Code != http.StatusOK {
		t.Fatalf("begin oauth setup: %d %s", setupRec.Code, setupRec.Body.String())
	}
	var flow struct {
		Session string `json:"session_id"`
		URL     string `json:"authorization_url"`
	}
	if err := json.Unmarshal(setupRec.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	defer removeModelOAuthSetup(flow.Session)
	authURL, err := url.Parse(flow.URL)
	if err != nil {
		t.Fatal(err)
	}
	if result := h.completeOAuthFlow(authURL.Query().Get("state"), "code"); result.Outcome != oauthCompletionSucceeded {
		t.Fatalf("complete setup oauth: %v", result.Err)
	}

	createForm := url.Values{
		"name":                {"Synced OAuth Model"},
		"provider":            {"openai"},
		"model":               {"gpt-6.1-sol"},
		"openai_auth_type":    {"oauth"},
		"auth_method":         {"oauth"},
		"oauth_connection_id": {"new"},
		"oauth_setup_session": {flow.Session},
	}
	createReq := httptest.NewRequest(http.MethodPost, "/models", strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.Header.Set("HX-Request", "true")
	createRec := httptest.NewRecorder()
	e.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create oauth model: %d %s", createRec.Code, createRec.Body.String())
	}

	waitUsageUpdated("CreateModel")
	if fetchCalls.Load() != 1 {
		t.Fatalf("expected 1 usage fetch after CreateModel, got %d", fetchCalls.Load())
	}

	accounts, err := usageSvc.ListUsageAccounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("expected 1 usage account, got %+v err=%v", accounts, err)
	}
	snapshots, err := usageSvc.SharedAccountUsage(context.Background(), "", false)
	if err != nil || len(snapshots[accounts[0].Key].AccountLimits) != 1 {
		t.Fatalf("expected cached usage snapshot after CreateModel, got %+v err=%v", snapshots, err)
	}

	configs, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var created *models.LLMConfig
	for i := range configs {
		if configs[i].Name == "Synced OAuth Model" {
			created = &configs[i]
			break
		}
	}
	if created == nil {
		t.Fatal("created model not found")
	}

	// 2. Disconnect OAuth evicts snapshot and broadcasts immediately.
	disconnectReq := httptest.NewRequest(http.MethodPost, "/models/"+created.ID+"/oauth/disconnect", nil)
	disconnectRec := httptest.NewRecorder()
	e.ServeHTTP(disconnectRec, disconnectReq)
	if disconnectRec.Code != http.StatusSeeOther {
		t.Fatalf("disconnect oauth: %d %s", disconnectRec.Code, disconnectRec.Body.String())
	}
	waitUsageUpdated("DisconnectModelOAuthConnection")
	snapshots, err = usageSvc.SharedAccountUsage(context.Background(), "", false)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("expected empty snapshots after disconnect, got %+v err=%v", snapshots, err)
	}

	// 3. Reconnect via existing-model OAuth flow fetches usage and broadcasts immediately.
	initReq := httptest.NewRequest(http.MethodGet, "/models/"+created.ID+"/oauth/initiate", nil)
	initRec := httptest.NewRecorder()
	e.ServeHTTP(initRec, initReq)
	if initRec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("initiate oauth: %d %s", initRec.Code, initRec.Body.String())
	}
	reauthURL, err := url.Parse(initRec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if result := h.completeOAuthFlow(reauthURL.Query().Get("state"), "code-2"); result.Outcome != oauthCompletionSucceeded {
		t.Fatalf("complete reconnect oauth: %v", result.Err)
	}
	waitUsageUpdated("completeOAuthFlow")
	if fetchCalls.Load() != 2 {
		t.Fatalf("expected 2 usage fetches after reconnect, got %d", fetchCalls.Load())
	}

	// 4. MoveModelsToOAuthConnection syncs and broadcasts immediately.
	second := &models.LLMConfig{
		Name:       "Second OAuth Model",
		Provider:   models.ProviderOpenAI,
		Model:      "gpt-6.1-sol",
		AuthMethod: models.AuthMethodOAuth,
	}
	if err := repo.Create(context.Background(), second); err != nil {
		t.Fatalf("create second model: %v", err)
	}
	drainUsageUpdated()
	moveForm := url.Values{"model_ids": {second.ID}}
	moveReq := httptest.NewRequest(http.MethodPost, "/models/oauth-connections/"+created.OAuthConnectionID+"/models", strings.NewReader(moveForm.Encode()))
	moveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	moveRec := httptest.NewRecorder()
	e.ServeHTTP(moveRec, moveReq)
	if moveRec.Code != http.StatusSeeOther {
		t.Fatalf("move models to oauth connection: %d %s", moveRec.Code, moveRec.Body.String())
	}
	waitUsageUpdated("MoveModelsToOAuthConnection")
	waitFetchCount("MoveModelsToOAuthConnection", 3)
	waitUsageUpdated("MoveModelsToOAuthConnection fetch")

	// 5. UpdateModel switching an OAuth model to API key syncs and broadcasts immediately.
	drainUsageUpdated()
	updateForm := url.Values{
		"name":             {created.Name},
		"provider":         {"openai"},
		"model":            {"gpt-6.1-sol"},
		"openai_auth_type": {"api_key"},
		"auth_method":      {"api_key"},
		"api_key":          {"sk-switched"},
	}
	updateReq := httptest.NewRequest(http.MethodPut, "/models/"+created.ID, strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRec := httptest.NewRecorder()
	e.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusSeeOther {
		t.Fatalf("update model: %d %s", updateRec.Code, updateRec.Body.String())
	}
	waitUsageUpdated("UpdateModel")
	waitFetchCount("UpdateModel", 4)
	waitUsageUpdated("UpdateModel fetch")

	// 6. DeleteModelsBulk evicts the removed OAuth account and broadcasts immediately.
	drainUsageUpdated()
	bulkBody, err := json.Marshal(map[string][]string{"ids": {second.ID}})
	if err != nil {
		t.Fatal(err)
	}
	bulkReq := httptest.NewRequest(http.MethodDelete, "/models/bulk", strings.NewReader(string(bulkBody)))
	bulkReq.Header.Set("Content-Type", "application/json")
	bulkRec := httptest.NewRecorder()
	e.ServeHTTP(bulkRec, bulkReq)
	if bulkRec.Code != http.StatusOK {
		t.Fatalf("bulk delete models: %d %s", bulkRec.Code, bulkRec.Body.String())
	}
	waitUsageUpdated("DeleteModelsBulk")
	snapshots, err = usageSvc.SharedAccountUsage(context.Background(), "", false)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("expected empty snapshots after bulk delete, got %+v err=%v", snapshots, err)
	}

	// 7. finishGeminiOAuth syncs usage and broadcasts immediately.
	fakeGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"currentTier":             map[string]string{"id": "standard-tier"},
			"cloudaicompanionProject": "proj-sync-1",
		})
	})
	geminiModel := &models.LLMConfig{
		Name:       "Synced Gemini OAuth",
		Provider:   models.ProviderGemini,
		Model:      "gemini-2.5-pro",
		AuthMethod: models.AuthMethodOAuth,
	}
	if err := repo.Create(context.Background(), geminiModel); err != nil {
		t.Fatalf("create gemini model: %v", err)
	}
	drainUsageUpdated()
	if _, err := h.exchangeGeminiOAuthCodeAndSaveTokens(context.Background(), &oauthPendingFlow{
		ConfigID:       geminiModel.ID,
		ConnectionID:   geminiModel.OAuthConnectionID,
		ConfigRevision: geminiModel.OAuthConfigRevision,
		Provider:       models.ProviderGemini,
	}, "gemini-code"); err != nil {
		t.Fatalf("finish gemini oauth: %v", err)
	}
	waitUsageUpdated("finishGeminiOAuth")
	waitFetchCount("finishGeminiOAuth", 5)
	geminiAccounts, err := usageSvc.ListUsageAccounts(context.Background())
	if err != nil || len(geminiAccounts) != 1 || geminiAccounts[0].Provider != "gemini" {
		t.Fatalf("expected 1 gemini usage account, got %+v err=%v", geminiAccounts, err)
	}

	// 8. DeleteModel evicts snapshot and broadcasts immediately.
	drainUsageUpdated()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/models/"+geminiModel.ID, nil)
	deleteRec := httptest.NewRecorder()
	e.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusSeeOther {
		t.Fatalf("delete model: %d %s", deleteRec.Code, deleteRec.Body.String())
	}
	waitUsageUpdated("DeleteModel")
	snapshots, err = usageSvc.SharedAccountUsage(context.Background(), "", false)
	if err != nil || len(snapshots) != 0 {
		t.Fatalf("expected empty snapshots after delete, got %+v err=%v", snapshots, err)
	}
}
