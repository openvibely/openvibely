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
