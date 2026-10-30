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
	var selected *models.LLMConfig
	for _, group := range accountUsageGroups(configs, "openai") {
		if usageAccountGroupKey(group) == accountKey {
			cfg := group[0]
			selected = &cfg
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("OpenAI account configuration changed; reload accounts")
	}
	cfg, err := s.ensureFreshAccountUsageOAuth(ctx, *selected, s.openAIAccountUsageRefreshFunc())
	if err != nil {
		return nil, err
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
	return s.doAccountUsageRequestWithOAuthRecovery(ctx, cfg, endpoint, build, s.openAIAccountUsageRefreshFunc())
}
