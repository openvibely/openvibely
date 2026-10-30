package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

// UsageResetCredits reads provider-owned credits; these are not local token totals.
func (s *UsageAnalyticsService) UsageResetCredits(ctx context.Context, accountKey string) (map[string]any, error) {
	value, err := s.sharedAccountRead(ctx, "resets:"+accountKey, func(work context.Context) (any, error) { return s.usageResetRequest(work, accountKey, "", "") })
	if err != nil {
		return nil, err
	}
	return value.(map[string]any), nil
}

func (s *UsageAnalyticsService) ConsumeUsageReset(ctx context.Context, accountKey, creditID, idempotencyKey string) (map[string]any, error) {
	if strings.TrimSpace(creditID) == "" || strings.TrimSpace(idempotencyKey) == "" || len(creditID) > 512 || len(idempotencyKey) > 128 {
		return nil, fmt.Errorf("credit_id and idempotency_key are required and must be bounded")
	}
	defer s.accountReads.Forget("resets:" + accountKey)
	defer s.accountReads.Forget("limits:" + accountKey)
	return s.usageResetRequest(ctx, accountKey, creditID, idempotencyKey)
}

func (s *UsageAnalyticsService) usageResetRequest(ctx context.Context, accountKey, creditID, idempotencyKey string) (map[string]any, error) {
	if s.llmConfigRepo == nil || accountKey == "" {
		return nil, fmt.Errorf("connected OAuth account is required")
	}
	configs, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	var selected []models.LLMConfig
	var provider string
	for _, name := range []string{"openai", "anthropic"} {
		for _, group := range accountUsageGroups(configs, name) {
			if usageAccountGroupKey(group) == accountKey {
				selected = group
				provider = name
				break
			}
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("account configuration changed; reload accounts")
	}
	if provider == "anthropic" && creditID != "" && (!anthropicResetGrantID.MatchString(creditID) || !anthropicResetRequestID.MatchString(idempotencyKey)) {
		return nil, fmt.Errorf("invalid Anthropic grant or request ID")
	}
	refresh := s.openAIAccountUsageRefreshFunc()
	if provider == "anthropic" {
		refresh = s.anthropicAccountUsageRefreshFunc()
	}
	credentials := newAccountUsageCredentials(selected, provider)
	var lastErr error
	for {
		candidate, ok := credentials.next()
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cfg, err := s.ensureFreshAccountUsageOAuth(ctx, candidate, refresh)
		if err != nil {
			credentials.failed(candidate)
			lastErr = err
			continue
		}
		result, err := s.requestUsageResetForCredential(ctx, cfg, creditID, idempotencyKey, refresh)
		if err == nil {
			credentials.succeeded(candidate)
			return result, nil
		}
		// A failed read can use another credential. A redemption may already
		// have happened: only retry it with another credential after an explicit
		// authentication rejection, preserving the credit and redemption IDs.
		if creditID != "" && !isAccountUsageHTTPStatus(err, http.StatusUnauthorized) && !isAccountUsageHTTPStatus(err, http.StatusForbidden) {
			return nil, err
		}
		credentials.failed(candidate)
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable credentials for this account")
	}
	return nil, lastErr
}

var anthropicResetGrantID = regexp.MustCompile(`^[a-z0-9_-]{1,40}$`)
var anthropicResetRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (s *UsageAnalyticsService) requestUsageResetForCredential(ctx context.Context, cfg models.LLMConfig, creditID, requestID string, refresh oauthRefreshFunc) (map[string]any, error) {
	var endpoint string
	var err error
	builder := buildOpenAIAccountUsageRequest
	payload := map[string]string{"credit_id": creditID, "redeem_request_id": requestID}
	if cfg.Provider == models.ProviderAnthropic {
		builder = buildAnthropicAccountUsageRequest
		base := strings.TrimRight(anthropicclient.AnthropicAPIHost, "/")
		endpoint = base + "/api/oauth/usage?cedar_ember=1&skip_spend=1"
		if creditID != "" {
			fresh, profile, profileErr := s.resolveAnthropicOAuthProfile(ctx, cfg)
			if profileErr != nil {
				return nil, profileErr
			}
			if !strings.HasPrefix(profile.AccountID, "organization:") {
				return nil, fmt.Errorf("Anthropic organization unavailable")
			}
			if cfg.OAuthAccountID != "" && cfg.OAuthAccountID != profile.AccountID {
				return nil, fmt.Errorf("Anthropic account changed; reload accounts")
			}
			cfg = fresh
			org := strings.TrimPrefix(profile.AccountID, "organization:")
			if org == "" {
				return nil, fmt.Errorf("Anthropic organization unavailable")
			}
			endpoint = base + "/api/organizations/" + url.PathEscape(org) + "/reset_rate_limits"
			payload = map[string]string{"program": "cedar_ember", "grant_id": creditID, "request_id": requestID}
		}
	} else {
		endpoint, err = openAIUsageEndpoint()
		if err != nil {
			return nil, err
		}
		endpoint = strings.TrimSuffix(endpoint, "/usage") + "/rate-limit-reset-credits"
		if creditID != "" {
			endpoint += "/consume"
		}
	}
	build := func(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error) {
		req, err := builder(ctx, endpoint, cfg)
		if err != nil {
			return nil, err
		}
		if creditID != "" {
			body, _ := json.Marshal(payload)
			post, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			post.Header = req.Header
			post.Header.Set("Content-Type", "application/json")
			return post, nil
		}
		return req, nil
	}
	raw, err := s.doAccountUsageRequestWithOAuthRecovery(ctx, cfg, endpoint, build, refresh)
	if err != nil {
		return nil, err
	}
	if cfg.Provider == models.ProviderAnthropic && creditID == "" {
		return normalizeAnthropicResetGrants(raw, time.Now()), nil
	}
	return raw, nil
}

func normalizeAnthropicResetGrants(raw map[string]any, now time.Time) map[string]any {
	block, ok := raw["cedar_ember"].(map[string]any)
	if !ok {
		return map[string]any{"available_count": 0, "credits": []any{}, "unavailable_reason": "Usage resets are not offered for this account."}
	}
	eligible, _ := block["eligible"].(bool)
	grants, _ := block["grants"].([]any)
	credits := []any{}
	total := 0
	for _, value := range grants {
		grant, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, _ := grant["id"].(string)
		left, _ := grant["resets_left"].(float64)
		if !anthropicResetGrantID.MatchString(id) || left <= 0 {
			continue
		}
		usable, _ := grant["usable_now"].(bool)
		paused, _ := grant["paused"].(bool)
		reason, _ := grant["unavailable_reason"].(string)
		if !eligible {
			reason = stringValue(block["ineligible_reason"])
			if reason == "" {
				reason = "unavailable"
			}
		}
		if paused {
			reason = "paused"
		}
		expired := false
		for _, field := range []string{"starts_at", "ends_at", "cooldown_until"} {
			v := grant[field]
			if field == "cooldown_until" {
				v = block[field]
			}
			if v == nil {
				continue
			}
			text, _ := v.(string)
			stamp, err := time.Parse(time.RFC3339, text)
			if err != nil {
				reason = "invalid availability date"
				usable = false
				continue
			}
			if field == "ends_at" && !now.Before(stamp) {
				reason = "expired"
				expired = true
				usable = false
			}
			if field != "ends_at" && now.Before(stamp) {
				reason = field
				usable = false
			}
		}
		if !usable && reason == "" {
			reason = "Currently unavailable"
		}
		if !expired {
			total += int(left)
		}
		credits = append(credits, map[string]any{"id": id, "title": grant["label"], "status": "available", "expires_at": grant["ends_at"], "remaining_uses": int(left), "clears": grant["clears"], "gives_back_weekly": grant["gives_back_weekly"], "usable": eligible && usable && !paused && !expired && reason == "", "unavailable_reason": reason})
	}
	return map[string]any{"available_count": total, "credits": credits, "unavailable_reason": block["ineligible_reason"]}
}

// Concurrent views and monitoring share reads only while in flight, never a cached
// result. A departing browser must not cancel a background monitor's shared read.
func (s *UsageAnalyticsService) sharedAccountRead(ctx context.Context, key string, read func(context.Context) (any, error)) (any, error) {
	result := s.accountReads.DoChan(key, func() (any, error) {
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		return read(work)
	})
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-result:
		return r.Val, r.Err
	}
}
