package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

func (s *UsageAnalyticsService) fetchGeminiOAuthUsage(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
	refresh := oauthRefreshFunc(llmoauth.GeminiRefreshFunc())
	fresh, err := s.ensureFreshAccountUsageOAuth(ctx, cfg, refresh)
	if err != nil {
		return nil, err
	}
	raw, err := s.doAccountUsageRequestWithOAuthRecovery(ctx, fresh, geminiclient.CodeAssistBaseURL+":retrieveUserQuotaSummary", func(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error) {
		body, err := json.Marshal(map[string]string{"project": cfg.OAuthAccountID})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.OAuthAccessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		return req, nil
	}, refresh)
	if err != nil {
		return nil, err
	}
	return parseGeminiAccountUsage(raw, fresh)
}

func parseGeminiAccountUsage(raw map[string]any, cfg models.LLMConfig) (*models.AccountUsageResult, error) {
	type bucket struct {
		ID        string   `json:"bucketId"`
		Name      string   `json:"displayName"`
		Window    string   `json:"window"`
		Remaining *float64 `json:"remainingFraction"`
		Reset     string   `json:"resetTime"`
		Disabled  bool     `json:"disabled"`
	}
	var data struct {
		Buckets []bucket `json:"buckets"`
		Groups  []struct {
			Name    string   `json:"displayName"`
			Buckets []bucket `json:"buckets"`
		} `json:"groups"`
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(encoded, &data); err != nil {
		return nil, err
	}
	result := &models.AccountUsageResult{Provider: string(models.ProviderGemini), AccountID: cfg.OAuthAccountID, AgentConfigID: cfg.ID, FetchedAt: time.Now().UTC(), RawJSON: "{}"}
	appendBucket := func(group string, b bucket, index int) {
		if b.Disabled {
			return
		}
		key := b.ID
		if key == "" {
			key = fmt.Sprintf("bucket_%d", index)
		}
		label := b.Name
		if label == "" {
			label = key
		}
		if group != "" {
			label = group + " · " + label
		}
		if b.Window != "" {
			label += " (" + b.Window + ")"
		}
		limit := models.AccountUsageExtraLimit{Provider: result.Provider, AccountID: result.AccountID, AgentConfigID: cfg.ID, LimitKey: fmt.Sprintf("google_%s_%s", group, key), Label: label, RawJSON: "{}"}
		if b.Remaining != nil && *b.Remaining >= 0 && *b.Remaining <= 1 {
			used := (1 - *b.Remaining) * 100
			limit.UsedPercent = &used
		}
		if reset, err := time.Parse(time.RFC3339Nano, b.Reset); err == nil {
			value := reset.UTC().Format(time.RFC3339Nano)
			limit.ResetAt = &value
		}
		result.ExtraLimits = append(result.ExtraLimits, limit)
	}
	if len(data.Groups) > 0 {
		index := 0
		for _, group := range data.Groups {
			for _, b := range group.Buckets {
				appendBucket(strings.TrimSpace(group.Name), b, index)
				index++
			}
		}
	} else {
		for i, b := range data.Buckets {
			appendBucket("", b, i)
		}
	}
	if len(result.ExtraLimits) == 0 {
		return nil, fmt.Errorf("Google returned no available quota buckets")
	}
	return result, nil
}
