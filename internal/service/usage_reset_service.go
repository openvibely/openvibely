package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// UsageResetCredits reads provider-owned credits; these are not local token totals.
func (s *UsageAnalyticsService) UsageResetCredits(ctx context.Context, accountKey string) (map[string]any, error) {
	return s.usageResetRequest(ctx, accountKey, "", "")
}

func (s *UsageAnalyticsService) ConsumeUsageReset(ctx context.Context, accountKey, creditID, idempotencyKey string) (map[string]any, error) {
	if strings.TrimSpace(creditID) == "" || strings.TrimSpace(idempotencyKey) == "" || len(creditID) > 512 || len(idempotencyKey) > 128 {
		return nil, fmt.Errorf("credit_id and idempotency_key are required and must be bounded")
	}
	return s.usageResetRequest(ctx, accountKey, creditID, idempotencyKey)
}

func (s *UsageAnalyticsService) usageResetRequest(ctx context.Context, accountKey, creditID, idempotencyKey string) (map[string]any, error) {
	if s.llmConfigRepo == nil || accountKey == "" {
		return nil, fmt.Errorf("connected OpenAI account is required")
	}
	configs, err := s.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	var selected []models.LLMConfig
	for _, group := range accountUsageGroups(configs, "openai") {
		if usageAccountGroupKey(group) == accountKey {
			selected = group
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("OpenAI account configuration changed; reload accounts")
	}
	endpoint, err := openAIUsageEndpoint()
	if err != nil {
		return nil, err
	}
	endpoint = strings.TrimSuffix(endpoint, "/usage") + "/rate-limit-reset-credits"
	if creditID != "" {
		endpoint += "/consume"
	}
	build := func(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error) {
		req, err := buildOpenAIAccountUsageRequest(ctx, endpoint, cfg)
		if err != nil {
			return nil, err
		}
		if creditID != "" {
			body, _ := json.Marshal(map[string]string{"credit_id": creditID, "redeem_request_id": idempotencyKey})
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
	credentials := newAccountUsageCredentials(selected, "openai")
	var lastErr error
	for {
		candidate, ok := credentials.next()
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cfg, err := s.ensureFreshAccountUsageOAuth(ctx, candidate, s.openAIAccountUsageRefreshFunc())
		if err != nil {
			credentials.failed(candidate)
			lastErr = err
			continue
		}
		result, err := s.doAccountUsageRequestWithOAuthRecovery(ctx, cfg, endpoint, build, s.openAIAccountUsageRefreshFunc())
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
		lastErr = fmt.Errorf("no usable OpenAI credentials for this account")
	}
	return nil, lastErr
}
