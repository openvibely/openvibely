package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

func TestGeminiLiveAccountQuota(t *testing.T) {
	env := teststores.New(t)
	ctx := context.Background()
	cfg := models.LLMConfig{Name: "Google", Provider: models.ProviderGemini, Model: "gemini-4-neon", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "fixture", OAuthAccountID: "project", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("User-Agent") != geminiclient.OAuthUserAgent(cfg.OAuthRefreshToken) {
			t.Error("quota request missing client metadata")
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["project"] != "project" || r.Method != "POST" || r.URL.Path != "/v1internal:retrieveUserQuotaSummary" || r.Header.Get("Authorization") != "Bearer fixture" {
			t.Errorf("invalid request: %s %s %+v", r.Method, r.URL, body)
		}
		if calls == 1 {
			http.Error(w, "temporary", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"groups":[{"displayName":"Advanced models","buckets":[{"bucketId":"quota","displayName":"Allowance","window":"ROLLING","remainingFraction":0.25,"resetTime":"2027-01-01T12:00:00Z"}]}]}`))
	}))
	defer server.Close()
	old := geminiclient.CodeAssistBaseURL
	geminiclient.CodeAssistBaseURL = server.URL + "/v1internal"
	defer func() { geminiclient.CodeAssistBaseURL = old }()
	svc := NewUsageAnalyticsService(liveUsageWithoutStorage{}, env.LLMConfigs)
	accounts, err := svc.ListUsageAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%+v err=%v", accounts, err)
	}
	view, err := svc.BuildLiveAnalyticsAccountLimits(ctx, persistence.UsageFilter{AccountGroupKey: accounts[0].Key})
	if err != nil || len(view.AccountLimits) != 1 {
		t.Fatalf("view=%+v err=%v", view, err)
	}
	card := view.AccountLimits[0]
	if card.Error != "" || len(card.Limits) != 1 {
		t.Fatalf("card=%+v", card)
	}
	limit := card.Limits[0]
	if limit.UsedPercent == nil || *limit.UsedPercent != 75 || limit.ResetsAt == nil || *limit.ResetsAt != "2027-01-01T12:00:00Z" || limit.WindowMinutes != nil {
		t.Fatalf("limit=%+v", limit)
	}
	if calls != 2 {
		t.Fatalf("calls=%d", calls)
	}
	resets, err := svc.UsageResetCredits(ctx, accounts[0].Key)
	if err != nil || resets["available_count"] != 0 {
		t.Fatalf("resets=%+v err=%v", resets, err)
	}
}

func TestGeminiQuotaUnknownPercentAndDisabled(t *testing.T) {
	var raw map[string]any
	json.Unmarshal([]byte(`{"buckets":[{"bucketId":"disabled","disabled":true,"remainingFraction":1},{"bucketId":"amount","remainingAmount":"42","resetTime":"invalid"},{"bucketId":"zero","remainingFraction":0},{"bucketId":"bad","remainingFraction":2}]}`), &raw)
	result, err := parseGeminiAccountUsage(raw, models.LLMConfig{})
	if err != nil || len(result.ExtraLimits) != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.ExtraLimits[0].UsedPercent != nil || result.ExtraLimits[0].ResetAt != nil || *result.ExtraLimits[1].UsedPercent != 100 || result.ExtraLimits[2].UsedPercent != nil {
		t.Fatalf("limits=%+v", result.ExtraLimits)
	}
	if _, err := parseGeminiAccountUsage(map[string]any{}, models.LLMConfig{}); err == nil {
		t.Fatal("missing quota must be unavailable")
	}
}

func TestGeminiQuotaOAuthRecovery(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			env := teststores.New(t)
			ctx := context.Background()
			expiry := time.Now().Add(3 * time.Hour)
			if expired {
				expiry = time.Now().Add(-time.Hour)
			}
			cfg := models.LLMConfig{Name: "Google", Provider: models.ProviderGemini, Model: "gemini-4-neon", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "old", OAuthRefreshToken: "refresh", OAuthAccountID: "project", OAuthExpiresAt: expiry.UnixMilli()}
			if err := env.LLMConfigs.Create(ctx, &cfg); err != nil {
				t.Fatal(err)
			}
			refreshes, queries := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					refreshes++
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"access_token":"fresh","expires_in":7200,"token_type":"Bearer"}`))
					return
				}
				queries++
				if r.Header.Get("Authorization") != "Bearer fresh" {
					http.Error(w, "expired", 401)
					return
				}
				w.Write([]byte(`{"buckets":[{"bucketId":"quota","remainingFraction":1}]}`))
			}))
			defer server.Close()
			oldBase, oldToken := geminiclient.CodeAssistBaseURL, geminiclient.OAuthTokenURL
			geminiclient.CodeAssistBaseURL, geminiclient.OAuthTokenURL = server.URL+"/v1internal", server.URL+"/token"
			defer func() { geminiclient.CodeAssistBaseURL, geminiclient.OAuthTokenURL = oldBase, oldToken }()
			svc := NewUsageAnalyticsService(liveUsageWithoutStorage{}, env.LLMConfigs)
			result, err := svc.fetchGeminiOAuthUsage(ctx, cfg)
			wantQueries := 2
			if expired {
				wantQueries = 1
			}
			if err != nil || result == nil || refreshes != 1 || queries != wantQueries {
				t.Fatalf("result=%+v err=%v refreshes=%d queries=%d", result, err, refreshes, queries)
			}
		})
	}
}

func TestGeminiQuotaLabels(t *testing.T) {
	cases := []struct{ group, name, window, want string }{
		{"Gemini Models", "Weekly Limit Remaining", "weekly", "Gemini Models · weekly limit"},
		{"Gemini Models", "Five Hour Limit Remaining", "5h", "Gemini Models · 5-hour session"},
		{"Claude and GPT models", "Weekly Limit Remaining", "weekly", "Claude and GPT models · weekly limit"},
		{"Claude and GPT models", "Five Hour Limit Remaining", "5h", "Claude and GPT models · 5-hour session"},
		{"Gemini 4 Argon", "Gemini 4 Argon Weekly Limit", "weekly", "Gemini 4 Argon · weekly limit"},
		{"Advanced models", "Allowance", "ROLLING", "Advanced models · Allowance (ROLLING)"},
		{"", "", "", "quota"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			raw := map[string]any{"groups": []any{map[string]any{"displayName": tc.group, "buckets": []any{map[string]any{"bucketId": "quota", "displayName": tc.name, "window": tc.window, "remainingFraction": 0.97, "resetTime": "2027-01-01T12:00:00Z"}}}}}
			result, err := parseGeminiAccountUsage(raw, models.LLMConfig{})
			if err != nil {
				t.Fatal(err)
			}
			limit := result.ExtraLimits[0]
			if limit.Label != tc.want {
				t.Fatalf("got %q, want %q", limit.Label, tc.want)
			}
			if limit.UsedPercent == nil || *limit.UsedPercent < 2.99 || *limit.UsedPercent > 3.01 || limit.ResetAt == nil {
				t.Fatalf("usage changed: %+v", limit)
			}
		})
	}
}
