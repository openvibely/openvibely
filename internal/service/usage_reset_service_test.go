package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func TestUsageResetCreditsAndSelectedRedemption(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	cfg := models.LLMConfig{Name: "reset fixture", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "account-one", OAuthExpiresAt: time.Now().Add(4 * time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	postCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("ChatGPT-Account-ID") != "account-one" {
			t.Error("wrong account headers")
		}
		switch r.URL.Path {
		case "/backend-api/wham/rate-limit-reset-credits":
			if r.Method != "GET" {
				t.Error("expected GET")
			}
			w.Write([]byte(`{"available_count":2,"credits":[{"id":"first","status":"available","expires_at":"2027-01-01T00:00:00Z"},{"id":"chosen","status":"available","expires_at":null}]}`))
		case "/backend-api/wham/rate-limit-reset-credits/consume":
			postCalls++
			if r.Method != "POST" {
				t.Error("expected POST")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["credit_id"] != "chosen" || body["redeem_request_id"] != "stable-attempt" || len(body) != 2 {
				t.Errorf("wrong redemption payload: %v", body)
			}
			if postCalls == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(503)
				return
			}
			w.Write([]byte(`{"status":"reset","windows_reset":2}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	old := openaiclient.OpenAIChatGPTAPIBaseURL
	openaiclient.OpenAIChatGPTAPIBaseURL = server.URL + "/backend-api/codex/"
	defer func() { openaiclient.OpenAIChatGPTAPIBaseURL = old }()
	svc := NewUsageAnalyticsService(env.Usage, env.LLMConfigs)
	accounts, err := svc.ListUsageAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts %v: %v", accounts, err)
	}
	credits, err := svc.UsageResetCredits(ctx, accounts[0].Key)
	if err != nil || credits["available_count"] != float64(2) {
		t.Fatalf("credits %v: %v", credits, err)
	}
	if _, err := svc.ConsumeUsageReset(ctx, accounts[0].Key, "chosen", "stable-attempt"); err == nil || postCalls != 1 {
		t.Fatalf("failed redemption must surface without automatic POST retries: calls=%d err=%v", postCalls, err)
	}
	result, err := svc.ConsumeUsageReset(ctx, accounts[0].Key, "chosen", "stable-attempt")
	if err != nil || result["status"] != "reset" || postCalls != 2 {
		t.Fatalf("result %v, calls %d: %v", result, postCalls, err)
	}
	before := calls
	if _, err := svc.ConsumeUsageReset(ctx, accounts[0].Key, "", "attempt"); err == nil {
		t.Error("missing credit accepted")
	}
	if _, err := svc.UsageResetCredits(ctx, "other-account"); err == nil {
		t.Error("unknown account accepted")
	}
	if calls != before {
		t.Error("invalid request reached provider")
	}
}

func TestUsageResetAccountCredentialFallback(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	first := models.LLMConfig{Name: "A failed", Provider: models.ProviderOpenAI, Model: "one", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "bad", OAuthAccountID: "same-account", OAuthExpiresAt: time.Now().Add(4 * time.Hour).UnixMilli()}
	second := models.LLMConfig{Name: "B alias", Provider: models.ProviderOpenAI, Model: "two", AuthMethod: models.AuthMethodOAuth}
	healthy := first
	healthy.Name = "C healthy"
	healthy.Model = "three"
	healthy.OAuthAccessToken = "good"
	other := first
	other.Name = "D different account"
	other.OAuthAccountID = "other-account"
	other.OAuthAccessToken = "other"
	for _, cfg := range []*models.LLMConfig{&first, &second, &healthy, &other} {
		if err := env.LLMConfigs.Create(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	}
	if err := env.Fixtures.LinkModelOAuthConnection(ctx, second.ID, first.OAuthConnectionID); err != nil {
		t.Fatal(err)
	}
	badStatus := http.StatusForbidden
	var tokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		tokens = append(tokens, token)
		if r.Header.Get("ChatGPT-Account-ID") != "same-account" {
			t.Error("crossed account boundary")
		}
		if r.Method == http.MethodPost {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["credit_id"] != "selected" || body["redeem_request_id"] != "same-attempt" {
				t.Errorf("changed redemption: %v", body)
			}
		}
		if token == "Bearer bad" {
			w.WriteHeader(badStatus)
			return
		}
		if token != "Bearer good" {
			t.Errorf("unexpected credential %s", token)
		}
		w.Write([]byte(`{"available_count":1,"status":"reset"}`))
	}))
	defer server.Close()
	old := openaiclient.OpenAIChatGPTAPIBaseURL
	openaiclient.OpenAIChatGPTAPIBaseURL = server.URL + "/backend-api/codex/"
	defer func() { openaiclient.OpenAIChatGPTAPIBaseURL = old }()
	svc := NewUsageAnalyticsService(env.Usage, env.LLMConfigs)
	configs, err := env.LLMConfigs.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var key string
	for _, group := range accountUsageGroups(configs, "openai") {
		if group[0].OAuthAccountID == "same-account" {
			key = usageAccountGroupKey(group)
		}
	}
	if key == "" {
		t.Fatal("missing account group")
	}
	if _, err := svc.UsageResetCredits(ctx, key); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0] != "Bearer bad" || tokens[1] != "Bearer good" {
		t.Fatalf("read credential sequence: %v", tokens)
	}
	tokens = nil
	if _, err := svc.ConsumeUsageReset(ctx, key, "selected", "same-attempt"); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[0] != "Bearer bad" || tokens[1] != "Bearer good" {
		t.Fatalf("redemption credential sequence: %v", tokens)
	}
	tokens = nil
	badStatus = http.StatusUnauthorized
	if _, err := svc.ConsumeUsageReset(ctx, key, "selected", "same-attempt"); err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 2 || tokens[1] != "Bearer good" {
		t.Fatalf("failed OAuth recovery prevented credential fallback: %v", tokens)
	}
	tokens = nil
	badStatus = http.StatusServiceUnavailable
	if _, err := svc.ConsumeUsageReset(ctx, key, "selected", "same-attempt"); err == nil {
		t.Fatal("expected provider failure")
	}
	if len(tokens) != 1 {
		t.Fatalf("ambiguous POST was repeated using other credentials: %v", tokens)
	}
}

func TestAnthropicUsageResetRequests(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	cfg := models.LLMConfig{Name: "Claude resets", Provider: models.ProviderAnthropic, Model: "claude-opus-5", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "organization:org-one", OAuthExpiresAt: time.Now().Add(4 * time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	claims := 0
	wrongOrg := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture" || r.Header.Get("anthropic-beta") == "" {
			t.Error("missing OAuth headers")
		}
		switch r.URL.Path {
		case "/api/oauth/usage":
			if r.Method != "GET" || r.URL.RawQuery != "cedar_ember=1&skip_spend=1" {
				t.Errorf("wrong status request: %s %s", r.Method, r.URL)
			}
			w.Write([]byte(`{"cedar_ember":{"eligible":true,"grants":[{"id":"weekly","label":"Weekly reset","resets_left":2,"usable_now":true,"clears":["seven_day"],"ends_at":"2035-01-01T00:00:00Z"},{"id":"paused","resets_left":1,"usable_now":false,"paused":true,"ends_at":null}]}}`))
		case "/api/oauth/profile":
			org := "org-one"
			if wrongOrg {
				org = "different-org"
			}
			json.NewEncoder(w).Encode(map[string]any{"organization": map[string]string{"uuid": org}})
		case "/api/organizations/org-one/reset_rate_limits":
			claims++
			if r.Method != "POST" {
				t.Error("claim not POST")
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 3 || body["program"] != "cedar_ember" || body["grant_id"] != "weekly" || body["request_id"] != "attempt-1" {
				t.Errorf("wrong claim body %v", body)
			}
			w.Write([]byte(`{"result":"reset","resets_left":1,"cleared":["seven_day"]}`))
		default:
			t.Errorf("unexpected route %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	old := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = old }()
	svc := NewUsageAnalyticsService(env.Usage, env.LLMConfigs)
	accounts, err := svc.ListUsageAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatal(err)
	}
	data, err := svc.UsageResetCredits(ctx, accounts[0].Key)
	if err != nil {
		t.Fatal(err)
	}
	credits := data["credits"].([]any)
	if data["available_count"] != 3 || len(credits) != 2 || credits[0].(map[string]any)["usable"] != true || credits[1].(map[string]any)["usable"] != false {
		t.Fatalf("wrong grants: %v", data)
	}
	result, err := svc.ConsumeUsageReset(ctx, accounts[0].Key, "weekly", "attempt-1")
	if err != nil || result["result"] != "reset" || claims != 1 {
		t.Fatalf("claim %v err=%v", result, err)
	}
	wrongOrg = true
	if _, err := svc.ConsumeUsageReset(ctx, accounts[0].Key, "weekly", "attempt-2"); err == nil {
		t.Fatal("changed organization accepted")
	}
	if claims != 1 {
		t.Fatal("claimed against changed organization")
	}
	if _, err := svc.ConsumeUsageReset(ctx, accounts[0].Key, "../bad", "attempt-3"); err == nil {
		t.Fatal("invalid grant accepted")
	}
}

func TestAnthropicResetGrantAvailability(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name     string
		eligible bool
		fields   map[string]any
		usable   bool
	}{
		{"usable", true, map[string]any{}, true},
		{"ineligible", false, map[string]any{}, false},
		{"paused", true, map[string]any{"paused": true}, false},
		{"expired", true, map[string]any{"ends_at": "2029-01-01T00:00:00Z"}, false},
		{"future", true, map[string]any{"starts_at": "2031-01-01T00:00:00Z"}, false},
		{"blocked", true, map[string]any{"usable_now": false, "unavailable_reason": "needs_limit"}, false},
		{"malformed date", true, map[string]any{"ends_at": "bad"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant := map[string]any{"id": "grant", "resets_left": float64(1), "usable_now": true}
			for k, v := range tc.fields {
				grant[k] = v
			}
			got := normalizeAnthropicResetGrants(map[string]any{"cedar_ember": map[string]any{"eligible": tc.eligible, "grants": []any{grant}}}, now)
			credit := got["credits"].([]any)[0].(map[string]any)
			if credit["usable"] != tc.usable {
				t.Fatalf("grant=%v", credit)
			}
		})
	}
}
