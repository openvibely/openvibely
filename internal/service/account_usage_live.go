package service

import (
	"context"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// BuildLiveAnalyticsAccountLimits renders provider responses without reading or
// writing snapshot history. Cards, reporting, and monitoring share this live path.
func (s *UsageAnalyticsService) BuildLiveAnalyticsAccountLimits(ctx context.Context, filter persistence.UsageFilter) (*models.AnalyticsUsageAccountLimitsViewModel, error) {
	// Keep provider work attached to the browser request so navigation cancels it.
	view, configs, failures, err := s.buildAnalyticsAccountLimitBase(ctx, filter)
	if err != nil {
		return nil, err
	}
	view.AccountLimits = dedupeAccountUsageViews(view.AccountLimits, configs)
	view.AccountLimits = applyAccountErrors(view.AccountLimits, failures, configs)
	for _, account := range view.AccountLimits {
		if account.Error == "" && !account.UpdatedAt.IsZero() && (view.LastUpdatedAt == nil || account.UpdatedAt.After(*view.LastUpdatedAt)) {
			updated := account.UpdatedAt
			view.LastUpdatedAt = &updated
		}
	}
	return &models.AnalyticsUsageAccountLimitsViewModel{AccountLimits: view.AccountLimits, LastUpdatedAt: view.LastUpdatedAt, Errors: view.Errors}, nil
}

func (s *UsageAnalyticsService) fetchLiveAccountUsage(ctx context.Context, configs []models.LLMConfig, provider string) ([]models.AccountUsageResult, map[string]string) {
	type result struct {
		snapshots []models.AccountUsageResult
		errors    map[string]string
	}
	var pending []chan result
	for _, name := range []string{"openai", "anthropic"} {
		if provider != "" && provider != name {
			continue
		}
		for _, group := range accountUsageGroups(configs, name) {
			ch := make(chan result, 1)
			pending = append(pending, ch)
			go func(group []models.LLMConfig, name string) {
				snapshots, failures := s.fetchLiveAccountGroup(ctx, group, name)
				ch <- result{snapshots, failures}
			}(group, name)
		}
	}
	var snapshots []models.AccountUsageResult
	failures := map[string]string{}
	for _, ch := range pending {
		result := <-ch
		snapshots = append(snapshots, result.snapshots...)
		for key, message := range result.errors {
			failures[key] = message
		}
	}
	return snapshots, failures
}

func (s *UsageAnalyticsService) fetchLiveAccountGroup(ctx context.Context, configs []models.LLMConfig, provider string) ([]models.AccountUsageResult, map[string]string) {
	credentials := newAccountUsageCredentials(configs, provider)
	failures := map[string]string{}
	var snapshots []models.AccountUsageResult
	for {
		cfg, ok := credentials.next()
		if !ok {
			break
		}
		key := accountUsageKeyForConfig(cfg)
		failures[key] = "Usage unavailable"
		if ctx.Err() != nil || s.accountFetcher == nil {
			break
		}
		snapshot, err := s.accountFetcher(ctx, cfg)
		if err != nil || snapshot == nil {
			credentials.failed(cfg)
			continue
		}
		// A reconnect or account change while the provider is responding must not
		// attribute the old response to the new credentials.
		current, err := s.llmConfigRepo.GetByID(ctx, cfg.ID)
		if err != nil || current == nil || current.Provider != cfg.Provider || current.AuthMethod != models.AuthMethodOAuth || current.OAuthConnectionID != cfg.OAuthConnectionID || current.OAuthConfigRevision != cfg.OAuthConfigRevision {
			credentials.failed(cfg)
			continue
		}
		snapshot.Provider = string(cfg.Provider)
		snapshot.AgentConfigID = cfg.ID
		snapshot.OAuthConnectionID = cfg.OAuthConnectionID
		snapshot.OAuthConfigRevision = cfg.OAuthConfigRevision
		if snapshot.AccountID == "" {
			snapshot.AccountID = accountIDForConfig(cfg)
		}
		snapshot.FetchedAt = time.Now()
		credentials.succeeded(cfg)
		snapshots = append(snapshots, *snapshot)
	}
	for key := range failures {
		if credentials.servedAccounts[key] {
			delete(failures, key)
		}
	}
	return snapshots, failures
}

// BuildLiveAnalyticsUsage combines recorded application usage with current
// provider limits. No saved account-limit responses are used as a fallback.
func (s *UsageAnalyticsService) BuildLiveAnalyticsUsage(ctx context.Context, filter persistence.UsageFilter) (*models.AnalyticsUsageViewModel, error) {
	if s == nil || s.usageRepo == nil {
		return nil, fmt.Errorf("usage analytics service is not configured")
	}
	view := &models.AnalyticsUsageViewModel{}
	if err := s.populateLocalUsageMetrics(ctx, filter, view); err != nil {
		return nil, err
	}
	latest, err := s.usageRepo.GetLatestUsageEventTime(ctx, filter)
	if err != nil {
		return nil, err
	}
	view.LastUpdatedAt = latest
	accounts, err := s.BuildLiveAnalyticsAccountLimits(ctx, filter)
	if err != nil {
		return nil, err
	}
	view.AccountLimits = accounts.AccountLimits
	view.Errors = accounts.Errors
	if accounts.LastUpdatedAt != nil && (view.LastUpdatedAt == nil || accounts.LastUpdatedAt.After(*view.LastUpdatedAt)) {
		view.LastUpdatedAt = accounts.LastUpdatedAt
	}
	return view, nil
}
