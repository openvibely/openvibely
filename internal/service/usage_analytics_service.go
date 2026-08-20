package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

type UsageAnalyticsService struct {
	usageRepo               *repository.UsageRepo
	llmConfigRepo           *repository.LLMConfigRepo
	oauthRecovery           *llmoauth.Manager
	accountFetcher          accountUsageFetcher
	anthropicOAuthRefresher oauthRefreshFunc
	openAIOAuthRefresher    oauthRefreshFunc
	httpClient              *http.Client
}

type accountUsageFetcher func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error)
type oauthRefreshFunc func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error)

const (
	accountRefreshSuccessTTL       = time.Hour
	accountRefreshFailureTTL       = 6 * time.Hour
	accountRefreshRateLimitTTL     = 30 * time.Minute
	accountRefreshUnavailableTTL   = 24 * time.Hour
	accountRefreshFailureRawPrefix = `{"refresh_error":`
)

func NewUsageAnalyticsService(usageRepo *repository.UsageRepo, llmConfigRepo *repository.LLMConfigRepo) *UsageAnalyticsService {
	s := &UsageAnalyticsService{
		usageRepo:     usageRepo,
		llmConfigRepo: llmConfigRepo,
		oauthRecovery: llmoauth.NewManager(llmConfigRepo),
		httpClient:    &http.Client{Timeout: 5 * time.Second},
	}
	s.anthropicOAuthRefresher = s.defaultAnthropicAccountUsageRefreshFunc()
	s.openAIOAuthRefresher = s.defaultOpenAIAccountUsageRefreshFunc()
	s.accountFetcher = s.fetchOAuthAccountUsage
	return s
}

func (s *UsageAnalyticsService) SetAccountUsageFetcher(fetcher accountUsageFetcher) {
	if fetcher == nil {
		s.accountFetcher = s.fetchOAuthAccountUsage
		return
	}
	s.accountFetcher = fetcher
}

func (s *UsageAnalyticsService) SetOAuthRecoveryManager(manager *llmoauth.Manager) {
	s.oauthRecovery = manager
}

func (s *UsageAnalyticsService) SetOAuthRefreshers(anthropicRefresh, openAIRefresh oauthRefreshFunc) {
	if anthropicRefresh != nil {
		s.anthropicOAuthRefresher = anthropicRefresh
	}
	if openAIRefresh != nil {
		s.openAIOAuthRefresher = openAIRefresh
	}
}

func (s *UsageAnalyticsService) BuildAnalyticsUsage(ctx context.Context, filter repository.UsageFilter) (*models.AnalyticsUsageViewModel, error) {
	if s == nil || s.usageRepo == nil {
		return nil, fmt.Errorf("usage analytics service is not configured")
	}

	view := &models.AnalyticsUsageViewModel{}
	var oauthAccounts []models.AccountUsageView
	configsByID := map[string]models.LLMConfig{}
	refreshErrors := map[string]string{}
	if s.llmConfigRepo != nil {
		configs, err := s.llmConfigRepo.List(ctx)
		if err != nil {
			view.Errors = append(view.Errors, fmt.Sprintf("listing OAuth accounts: %v", err))
		} else {
			for i := range configs {
				configs[i] = s.resolveAccountUsageOAuthAccountID(ctx, configs[i])
				configsByID[configs[i].ID] = configs[i]
			}
			oauthAccounts = s.oauthAccountPlaceholders(configs, filter.Provider)
			view.AccountLimits = append(view.AccountLimits, oauthAccounts...)
			if fetched, errs := s.refreshAccountSnapshots(ctx, configs, filter.Provider, filter.Refresh); len(fetched) > 0 || len(errs) > 0 {
				view.AccountLimits = mergeAccountSnapshots(view.AccountLimits, fetched, configsByID)
				for key, value := range errs {
					refreshErrors[key] = value
				}
			}
		}
	}

	if err := s.populateAnalyticsUsageView(ctx, filter, view, configsByID, refreshErrors); err != nil {
		return nil, err
	}
	return view, nil
}

func (s *UsageAnalyticsService) BuildLocalAnalyticsUsage(ctx context.Context, filter repository.UsageFilter) (*models.AnalyticsUsageViewModel, error) {
	if s == nil || s.usageRepo == nil {
		return nil, fmt.Errorf("usage analytics service is not configured")
	}

	view := &models.AnalyticsUsageViewModel{}
	configsByID := map[string]models.LLMConfig{}
	if s.llmConfigRepo != nil {
		configs, err := s.llmConfigRepo.List(ctx)
		if err != nil {
			view.Errors = append(view.Errors, fmt.Sprintf("listing OAuth accounts: %v", err))
		} else {
			for i := range configs {
				configsByID[configs[i].ID] = configs[i]
			}
			view.AccountLimits = append(view.AccountLimits, s.oauthAccountPlaceholders(configs, filter.Provider)...)
		}
	}

	if err := s.populateAnalyticsUsageView(ctx, filter, view, configsByID, nil); err != nil {
		return nil, err
	}
	if latestUsageAt, err := s.usageRepo.GetLatestUsageEventTime(ctx, filter); err != nil {
		return nil, err
	} else if latestUsageAt != nil && (view.LastUpdatedAt == nil || latestUsageAt.After(*view.LastUpdatedAt)) {
		view.LastUpdatedAt = latestUsageAt
	}
	return view, nil
}

type usageAnalyticsActionInput struct {
	Range             string `json:"range"`
	Provider          string `json:"provider"`
	GroupBy           string `json:"group_by"`
	DateFrom          string `json:"date_from"`
	DateTo            string `json:"date_to"`
	TopLimit          int    `json:"top_limit"`
	RecentBucketLimit *int   `json:"recent_bucket_limit"`
}

type usageAnalyticsActionResponse struct {
	OK                 bool                            `json:"ok"`
	ProjectID          string                          `json:"project_id"`
	Filter             usageAnalyticsActionFilter      `json:"filter"`
	Totals             models.UsageTotals              `json:"totals"`
	Cost               usageAnalyticsActionCost        `json:"cost"`
	TopModels          []models.ModelUsagePoint        `json:"top_models"`
	TopProviders       []usageAnalyticsProviderSummary `json:"top_providers"`
	RecentBuckets      []models.UsageRatePoint         `json:"recent_buckets,omitempty"`
	AccountLimits      []usageAnalyticsAccountSummary  `json:"account_limits"`
	LastUpdatedAt      *string                         `json:"last_updated_at,omitempty"`
	LocalOnly          bool                            `json:"local_only"`
	ProviderRefreshed  bool                            `json:"provider_refreshed"`
	CostAvailability   string                          `json:"cost_availability"`
	AccountLimitSource string                          `json:"account_limit_source"`
	Errors             []string                        `json:"errors,omitempty"`
}

type usageAnalyticsActionFilter struct {
	Range    string `json:"range"`
	Provider string `json:"provider,omitempty"`
	GroupBy  string `json:"group_by"`
	DateFrom string `json:"date_from,omitempty"`
	DateTo   string `json:"date_to,omitempty"`
}

type usageAnalyticsActionCost struct {
	Available bool     `json:"available"`
	Status    string   `json:"status"`
	TotalUSD  *float64 `json:"total_usd,omitempty"`
}

type usageAnalyticsProviderSummary struct {
	Provider      string   `json:"provider"`
	TotalTokens   int      `json:"total_tokens"`
	InputTokens   int      `json:"input_tokens"`
	OutputTokens  int      `json:"output_tokens"`
	CacheTokens   int      `json:"cached_input_tokens"`
	CostUSD       *float64 `json:"cost_usd,omitempty"`
	CostAvailable bool     `json:"cost_available"`
	CallCount     int      `json:"call_count"`
	Percent       float64  `json:"percent"`
}

type usageAnalyticsAccountSummary struct {
	Provider             string                    `json:"provider"`
	PlanType             string                    `json:"plan_type,omitempty"`
	StatusLabel          string                    `json:"status_label,omitempty"`
	ExtraUsageLabel      string                    `json:"extra_usage_label,omitempty"`
	ExtraUsageMonthlyUSD *float64                  `json:"extra_usage_monthly_usd,omitempty"`
	ExtraUsageUsedUSD    *float64                  `json:"extra_usage_used_usd,omitempty"`
	UpdatedAt            string                    `json:"updated_at,omitempty"`
	Limits               []models.AccountLimitView `json:"limits"`
	Error                string                    `json:"error,omitempty"`
}

func ExecuteViewUsageAnalyticsTool(ctx context.Context, usageAnalyticsSvc *UsageAnalyticsService, projectID string, input json.RawMessage) (string, error) {
	if usageAnalyticsSvc == nil {
		return "", fmt.Errorf("usage analytics service is not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", fmt.Errorf("view_usage_analytics requires a current project")
	}
	var req usageAnalyticsActionInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	filter, filterSummary := usageAnalyticsActionFilterFromInput(projectID, req)
	view, err := usageAnalyticsSvc.BuildLocalAnalyticsUsage(ctx, filter)
	if err != nil {
		return "", err
	}
	response := compactUsageAnalyticsActionResponse(projectID, filterSummary, view, req.TopLimit, req.RecentBucketLimit)
	b, err := json.Marshal(response)
	return string(b), err
}

func usageAnalyticsActionFilterFromInput(projectID string, req usageAnalyticsActionInput) (repository.UsageFilter, usageAnalyticsActionFilter) {
	rangeValue := strings.TrimSpace(req.Range)
	if rangeValue == "" {
		rangeValue = "30d"
	}
	groupBy := strings.TrimSpace(req.GroupBy)
	if groupBy == "" {
		groupBy = "day"
	}
	filter := repository.UsageFilter{ProjectID: projectID, Provider: strings.TrimSpace(req.Provider), GroupBy: groupBy, Refresh: false}
	if from := parseUsageAnalyticsActionTime(strings.TrimSpace(req.DateFrom)); !from.IsZero() {
		filter.DateFrom = from
	}
	if to := parseUsageAnalyticsActionTime(strings.TrimSpace(req.DateTo)); !to.IsZero() {
		filter.DateTo = to
	}
	if filter.DateFrom.IsZero() && filter.DateTo.IsZero() {
		now := time.Now()
		if days, ok := usageAnalyticsActionRangeDays(rangeValue); ok {
			filter.DateFrom = now.AddDate(0, 0, -days)
			filter.DateTo = now
		} else {
			switch rangeValue {
			case "month":
				filter.DateFrom = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
				filter.DateTo = now
			case "all":
			default:
				rangeValue = "30d"
				filter.DateFrom = now.AddDate(0, 0, -30)
				filter.DateTo = now
			}
		}
	}
	summary := usageAnalyticsActionFilter{Range: rangeValue, Provider: filter.Provider, GroupBy: groupBy}
	if !filter.DateFrom.IsZero() {
		summary.DateFrom = filter.DateFrom.UTC().Format(time.RFC3339)
	}
	if !filter.DateTo.IsZero() {
		summary.DateTo = filter.DateTo.UTC().Format(time.RFC3339)
	}
	return filter, summary
}

func parseUsageAnalyticsActionTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func usageAnalyticsActionRangeDays(value string) (int, bool) {
	if !strings.HasSuffix(value, "d") {
		return 0, false
	}
	days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
	return days, err == nil && days > 0
}

func compactUsageAnalyticsActionResponse(projectID string, filter usageAnalyticsActionFilter, view *models.AnalyticsUsageViewModel, topLimit int, recentLimit *int) usageAnalyticsActionResponse {
	if topLimit <= 0 {
		topLimit = 5
	}
	if topLimit > 10 {
		topLimit = 10
	}
	bucketLimit := 8
	if recentLimit != nil {
		bucketLimit = *recentLimit
	}
	if bucketLimit < 0 {
		bucketLimit = 0
	}
	if bucketLimit > 24 {
		bucketLimit = 24
	}
	costStatus := usageAnalyticsActionCostStatus(view.Totals, view.ModelBreakdown)
	response := usageAnalyticsActionResponse{
		OK:                 true,
		ProjectID:          projectID,
		Filter:             filter,
		Totals:             view.Totals,
		Cost:               usageAnalyticsActionCost{Available: view.Totals.CostAvailable, Status: costStatus, TotalUSD: view.Totals.CostUSD},
		TopModels:          firstUsageAnalyticsModelRows(view.ModelBreakdown, topLimit),
		TopProviders:       topUsageAnalyticsProviderRows(view.ModelBreakdown, topLimit),
		RecentBuckets:      lastUsageAnalyticsRateRows(view.UsageRate, bucketLimit),
		AccountLimits:      compactUsageAnalyticsAccountRows(view.AccountLimits),
		LocalOnly:          true,
		ProviderRefreshed:  false,
		CostAvailability:   costStatus,
		AccountLimitSource: "stored_snapshots_only",
		Errors:             view.Errors,
	}
	if view.LastUpdatedAt != nil {
		updated := view.LastUpdatedAt.UTC().Format(time.RFC3339)
		response.LastUpdatedAt = &updated
	}
	return response
}

func usageAnalyticsActionCostStatus(totals models.UsageTotals, breakdown []models.ModelUsagePoint) string {
	if totals.CallCount == 0 {
		return "no_usage"
	}
	if !totals.CostAvailable {
		return "unavailable"
	}
	for _, row := range breakdown {
		if row.CallCount > 0 && row.CostUSD == nil {
			return "partial"
		}
	}
	return "available"
}

func firstUsageAnalyticsModelRows(rows []models.ModelUsagePoint, limit int) []models.ModelUsagePoint {
	if limit > len(rows) {
		limit = len(rows)
	}
	out := make([]models.ModelUsagePoint, limit)
	copy(out, rows[:limit])
	return out
}

func lastUsageAnalyticsRateRows(rows []models.UsageRatePoint, limit int) []models.UsageRatePoint {
	if limit <= 0 || len(rows) == 0 {
		return nil
	}
	if limit > len(rows) {
		limit = len(rows)
	}
	out := make([]models.UsageRatePoint, limit)
	copy(out, rows[len(rows)-limit:])
	return out
}

func topUsageAnalyticsProviderRows(rows []models.ModelUsagePoint, limit int) []usageAnalyticsProviderSummary {
	index := map[string]int{}
	providers := []usageAnalyticsProviderSummary{}
	costSums := map[string]float64{}
	costAvailable := map[string]bool{}
	for _, row := range rows {
		i, ok := index[row.Provider]
		if !ok {
			index[row.Provider] = len(providers)
			providers = append(providers, usageAnalyticsProviderSummary{Provider: row.Provider})
			i = len(providers) - 1
		}
		providers[i].TotalTokens += row.TotalTokens
		providers[i].InputTokens += row.InputTokens
		providers[i].OutputTokens += row.OutputTokens
		providers[i].CacheTokens += row.CacheTokens
		providers[i].CallCount += row.CallCount
		if row.CostUSD != nil {
			costSums[row.Provider] += *row.CostUSD
			costAvailable[row.Provider] = true
		}
	}
	totalTokens := 0
	for _, row := range providers {
		totalTokens += row.TotalTokens
	}
	for i := range providers {
		if costAvailable[providers[i].Provider] {
			cost := costSums[providers[i].Provider]
			providers[i].CostUSD = &cost
			providers[i].CostAvailable = true
		}
		if totalTokens > 0 {
			providers[i].Percent = float64(providers[i].TotalTokens) * 100 / float64(totalTokens)
		}
	}
	sort.Slice(providers, func(i, j int) bool {
		if providers[i].TotalTokens == providers[j].TotalTokens {
			return providers[i].Provider < providers[j].Provider
		}
		return providers[i].TotalTokens > providers[j].TotalTokens
	})
	if limit > len(providers) {
		limit = len(providers)
	}
	out := make([]usageAnalyticsProviderSummary, limit)
	copy(out, providers[:limit])
	return out
}

func compactUsageAnalyticsAccountRows(accounts []models.AccountUsageView) []usageAnalyticsAccountSummary {
	out := make([]usageAnalyticsAccountSummary, 0, len(accounts))
	for _, account := range accounts {
		updated := ""
		if !account.UpdatedAt.IsZero() {
			updated = account.UpdatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, usageAnalyticsAccountSummary{
			Provider:             account.Provider,
			PlanType:             account.PlanType,
			StatusLabel:          account.StatusLabel,
			ExtraUsageLabel:      account.ExtraUsageLabel,
			ExtraUsageMonthlyUSD: account.ExtraUsageMonthlyUSD,
			ExtraUsageUsedUSD:    account.ExtraUsageUsedUSD,
			UpdatedAt:            updated,
			Limits:               account.Limits,
			Error:                account.Error,
		})
	}
	return out
}

func (s *UsageAnalyticsService) populateAnalyticsUsageView(ctx context.Context, filter repository.UsageFilter, view *models.AnalyticsUsageViewModel, configsByID map[string]models.LLMConfig, refreshErrors map[string]string) error {
	snapshots, err := s.usageRepo.GetLatestAccountUsageSnapshots(ctx, filter.Provider)
	if err != nil {
		view.Errors = append(view.Errors, fmt.Sprintf("loading account snapshots: %v", err))
	} else {
		view.AccountLimits = mergeAccountSnapshots(view.AccountLimits, snapshots, configsByID)
		view.AccountLimits = dedupeAccountUsageViews(view.AccountLimits, configsByID)
		view.AccountLimits = applyAccountErrors(view.AccountLimits, refreshErrors, configsByID)
		for _, snapshot := range snapshots {
			if view.LastUpdatedAt == nil || snapshot.FetchedAt.After(*view.LastUpdatedAt) {
				updated := snapshot.FetchedAt
				view.LastUpdatedAt = &updated
			}
		}
	}

	totals, err := s.usageRepo.GetUsageTotals(ctx, filter)
	if err != nil {
		return err
	}
	view.Totals = *totals

	daily, err := s.usageRepo.GetDailyUsage(ctx, filter)
	if err != nil {
		return err
	}
	view.DailyUsage = daily
	dailyByModel, err := s.usageRepo.GetDailyUsageByModel(ctx, filter)
	if err != nil {
		return err
	}
	view.DailyUsageByModel = dailyByModel

	rateFilter := filter
	if rateFilter.GroupBy == "" {
		rateFilter.GroupBy = "day"
	}
	rate, err := s.usageRepo.GetUsageRateBuckets(ctx, rateFilter)
	if err != nil {
		return err
	}
	view.UsageRate = rate
	rateByModel, err := s.usageRepo.GetUsageRateBucketsByModel(ctx, rateFilter)
	if err != nil {
		return err
	}
	view.UsageRateByModel = rateByModel

	breakdown, err := s.usageRepo.GetModelUsageBreakdown(ctx, filter)
	if err != nil {
		return err
	}
	view.ModelBreakdown = breakdown
	return nil
}

func (s *UsageAnalyticsService) refreshAccountSnapshots(ctx context.Context, configs []models.LLMConfig, provider string, force bool) ([]models.AccountUsageSnapshot, map[string]string) {
	if s.accountFetcher == nil {
		return nil, nil
	}
	var snapshots []models.AccountUsageSnapshot
	errorsByKey := map[string]string{}
	seenAccounts := map[string]bool{}
	for _, cfg := range configs {
		if cfg.AuthMethod != models.AuthMethodOAuth || strings.TrimSpace(cfg.OAuthAccessToken) == "" {
			continue
		}
		if cfg.Provider != models.ProviderAnthropic && cfg.Provider != models.ProviderOpenAI {
			continue
		}
		if provider != "" && string(cfg.Provider) != provider {
			continue
		}
		key := accountUsageKeyForConfig(cfg)
		if seenAccounts[key] {
			continue
		}
		seenAccounts[key] = true
		latest, shouldRefresh := latestAccountSnapshotForConfig(ctx, s.usageRepo, cfg, force)
		if !shouldRefresh {
			continue
		}
		snapshot, err := s.accountFetcher(ctx, cfg)
		if err != nil {
			reason := accountRefreshFailureReason(err)
			message := accountRefreshFailureMessage(reason)
			applog.Infof("[usage] account usage refresh failed provider=%s reason=%s: %v", cfg.Provider, reason, sanitizeAccountUsageError(err))
			errorsByKey[accountUsageKeyForConfig(cfg)] = message
			failure := accountRefreshFailureSnapshot(cfg, latest, reason)
			if storeErr := s.usageRepo.CreateAccountUsageSnapshot(ctx, &failure); storeErr != nil {
				applog.Infof("[usage] storing account usage refresh failure failed provider=%s: %v", cfg.Provider, storeErr)
				continue
			}
			snapshots = append(snapshots, failure)
			continue
		}
		if snapshot == nil {
			continue
		}
		if snapshot.Provider == "" {
			snapshot.Provider = string(cfg.Provider)
		}
		if snapshot.AgentConfigID == "" {
			snapshot.AgentConfigID = cfg.ID
		}
		if snapshot.AccountID == "" {
			snapshot.AccountID = accountIDForConfig(cfg)
		}
		if err := s.usageRepo.CreateAccountUsageSnapshot(ctx, snapshot); err != nil {
			applog.Infof("[usage] storing account usage snapshot failed provider=%s: %v", cfg.Provider, err)
			continue
		}
		s.syncOAuthAccountGroup(ctx, cfg, configs, key)
		snapshots = append(snapshots, *snapshot)
	}
	return snapshots, errorsByKey
}

func (s *UsageAnalyticsService) syncOAuthAccountGroup(ctx context.Context, refreshed models.LLMConfig, configs []models.LLMConfig, groupKey string) {
	if s == nil || s.llmConfigRepo == nil || strings.TrimSpace(refreshed.ID) == "" {
		return
	}
	latest, err := s.llmConfigRepo.GetByID(ctx, refreshed.ID)
	if err != nil || latest == nil || strings.TrimSpace(latest.OAuthAccessToken) == "" {
		return
	}
	for _, cfg := range configs {
		if cfg.ID == refreshed.ID || accountUsageKeyForConfig(cfg) != groupKey {
			continue
		}
		if err := s.llmConfigRepo.UpdateOAuthTokens(ctx, cfg.ID, latest.OAuthAccessToken, latest.OAuthRefreshToken, latest.OAuthExpiresAt, latest.OAuthAccountID); err != nil {
			applog.Infof("[usage] syncing OAuth account tokens failed provider=%s: %v", cfg.Provider, err)
		}
	}
}

func latestAccountSnapshotForConfig(ctx context.Context, usageRepo *repository.UsageRepo, cfg models.LLMConfig, force bool) (*models.AccountUsageSnapshot, bool) {
	if usageRepo == nil {
		return nil, false
	}
	snapshots, err := usageRepo.GetLatestAccountUsageSnapshots(ctx, string(cfg.Provider))
	if err != nil {
		return nil, true
	}
	for i := range snapshots {
		snapshot := &snapshots[i]
		if !snapshotMatchesConfigAccount(*snapshot, cfg) {
			continue
		}
		age := time.Since(snapshot.FetchedAt)
		if force {
			return snapshot, true
		}
		if isAccountRefreshFailure(snapshot.RateLimitReachedType) {
			return snapshot, age > accountRefreshFailureCooldown(snapshot.RateLimitReachedType)
		}
		return snapshot, age > accountRefreshSuccessTTL
	}
	return nil, true
}

func (s *UsageAnalyticsService) oauthAccountPlaceholders(configs []models.LLMConfig, provider string) []models.AccountUsageView {
	var accounts []models.AccountUsageView
	seenAccounts := map[string]bool{}
	for _, cfg := range configs {
		if cfg.AuthMethod != models.AuthMethodOAuth || cfg.OAuthAccessToken == "" {
			continue
		}
		if cfg.Provider != models.ProviderAnthropic && cfg.Provider != models.ProviderOpenAI {
			continue
		}
		if provider != "" && string(cfg.Provider) != provider {
			continue
		}
		key := accountUsageKeyForConfig(cfg)
		if seenAccounts[key] {
			continue
		}
		seenAccounts[key] = true
		accounts = append(accounts, models.AccountUsageView{
			Provider:      string(cfg.Provider),
			AccountID:     accountIDForConfig(cfg),
			AgentConfigID: cfg.ID,
			Limits:        []models.AccountLimitView{},
			Error:         "live account usage unavailable; reconnect OAuth or retry refresh",
		})
	}
	return accounts
}

func accountIDForConfig(cfg models.LLMConfig) string {
	return strings.TrimSpace(cfg.OAuthAccountID)
}

func accountRefreshFailureSnapshot(cfg models.LLMConfig, previous *models.AccountUsageSnapshot, reason string) models.AccountUsageSnapshot {
	snapshot := models.AccountUsageSnapshot{
		Provider:             string(cfg.Provider),
		AccountID:            accountIDForConfig(cfg),
		AgentConfigID:        cfg.ID,
		RateLimitReachedType: reason,
		RawJSON:              accountRefreshFailureRawPrefix + strconv.Quote(reason) + "}",
	}
	if previous == nil {
		return snapshot
	}
	snapshot.PlanType = previous.PlanType
	snapshot.AccountDisplayName = previous.AccountDisplayName
	snapshot.AccountDetail = previous.AccountDetail
	snapshot.BillingLabel = previous.BillingLabel
	snapshot.SubscriptionStatus = previous.SubscriptionStatus
	snapshot.ExtraUsageLabel = previous.ExtraUsageLabel
	snapshot.ExtraUsageMonthlyUSD = previous.ExtraUsageMonthlyUSD
	snapshot.ExtraUsageUsedUSD = previous.ExtraUsageUsedUSD
	snapshot.CreditsRemaining = previous.CreditsRemaining
	snapshot.PrimaryLabel = previous.PrimaryLabel
	snapshot.PrimaryUsedPercent = previous.PrimaryUsedPercent
	snapshot.PrimaryWindowMinutes = previous.PrimaryWindowMinutes
	snapshot.PrimaryResetsAt = previous.PrimaryResetsAt
	snapshot.SecondaryLabel = previous.SecondaryLabel
	snapshot.SecondaryUsedPercent = previous.SecondaryUsedPercent
	snapshot.SecondaryWindowMinutes = previous.SecondaryWindowMinutes
	snapshot.SecondaryResetsAt = previous.SecondaryResetsAt
	snapshot.ModelLimitLabel = previous.ModelLimitLabel
	snapshot.ModelLimitUsedPercent = previous.ModelLimitUsedPercent
	snapshot.ModelLimitWindowMinutes = previous.ModelLimitWindowMinutes
	snapshot.ModelLimitResetsAt = previous.ModelLimitResetsAt
	snapshot.ExtraLimits = append([]models.AccountUsageExtraLimit(nil), previous.ExtraLimits...)
	return snapshot
}

func isAccountRefreshFailure(reason string) bool {
	return strings.HasPrefix(reason, "refresh_failed")
}

func accountRefreshFailureCooldown(reason string) time.Duration {
	switch reason {
	case "refresh_failed_rate_limited":
		return accountRefreshRateLimitTTL
	case "refresh_failed_unauthorized", "refresh_failed_forbidden":
		return accountRefreshUnavailableTTL
	default:
		return accountRefreshFailureTTL
	}
}

func accountRefreshFailureReason(err error) string {
	var httpErr accountUsageHTTPError
	if ok := errors.As(err, &httpErr); ok {
		switch httpErr.StatusCode {
		case http.StatusUnauthorized:
			return "refresh_failed_unauthorized"
		case http.StatusForbidden:
			return "refresh_failed_forbidden"
		case http.StatusTooManyRequests:
			return "refresh_failed_rate_limited"
		case http.StatusNotFound:
			return "refresh_failed_unavailable"
		}
	}
	return "refresh_failed"
}

func accountRefreshFailureMessage(reason string) string {
	switch reason {
	case "refresh_failed_unauthorized":
		return "live account limits unavailable: OAuth credentials are invalid; reconnect this account"
	case "refresh_failed_forbidden":
		return "live account limits unavailable: provider denied the account usage endpoint"
	case "refresh_failed_rate_limited":
		return "live account limits temporarily unavailable: provider rate limited the usage endpoint"
	case "refresh_failed_unavailable":
		return "live account limits unavailable: provider usage endpoint is not available"
	default:
		return "live account limits unavailable; showing local usage history and last successful snapshot if available"
	}
}

func sanitizeAccountUsageError(err error) error {
	var httpErr accountUsageHTTPError
	if ok := errors.As(err, &httpErr); ok {
		return httpErr
	}
	return err
}

func mergeAccountSnapshots(existing []models.AccountUsageView, snapshots []models.AccountUsageSnapshot, configsByID map[string]models.LLMConfig) []models.AccountUsageView {
	index := make(map[string]int, len(existing))
	for i, account := range existing {
		index[accountUsageKeyForViewWithConfigs(account, configsByID)] = i
	}
	for _, snapshot := range snapshots {
		if !accountUsageSnapshotShouldRender(snapshot, configsByID) {
			continue
		}
		view := accountViewFromSnapshot(sanitizeSnapshotAccountDisplay(snapshot, configsByID))
		key := accountUsageKeyForSnapshotWithConfigs(snapshot, configsByID)
		if i, ok := index[key]; ok {
			existing[i] = preferAccountUsageView(existing[i], view)
			continue
		}
		index[key] = len(existing)
		existing = append(existing, view)
	}
	return existing
}

func accountUsageSnapshotShouldRender(snapshot models.AccountUsageSnapshot, configsByID map[string]models.LLMConfig) bool {
	if cfg, ok := configsByID[snapshot.AgentConfigID]; ok {
		return cfg.AuthMethod == models.AuthMethodOAuth && strings.TrimSpace(cfg.OAuthAccessToken) != ""
	}
	return true
}

func applyAccountErrors(accounts []models.AccountUsageView, errorsByKey map[string]string, configsByID map[string]models.LLMConfig) []models.AccountUsageView {
	if len(errorsByKey) == 0 {
		return accounts
	}
	for i := range accounts {
		key := accountUsageKeyForViewWithConfigs(accounts[i], configsByID)
		if errMsg, ok := errorsByKey[key]; ok {
			accounts[i].Error = errMsg
		}
	}
	return accounts
}

func dedupeAccountUsageViews(accounts []models.AccountUsageView, configsByID map[string]models.LLMConfig) []models.AccountUsageView {
	if len(accounts) < 2 {
		return accounts
	}
	deduped := make([]models.AccountUsageView, 0, len(accounts))
	index := map[string]int{}
	for _, account := range accounts {
		key := accountUsageDisplayKey(account, configsByID)
		if i, ok := index[key]; ok {
			deduped[i] = preferAccountUsageView(deduped[i], account)
			continue
		}
		index[key] = len(deduped)
		deduped = append(deduped, account)
	}
	return deduped
}

func preferAccountUsageView(existing, next models.AccountUsageView) models.AccountUsageView {
	winner := existing
	other := next
	if next.UpdatedAt.After(existing.UpdatedAt) {
		winner = next
		other = existing
	}
	return mergeAccountUsageViewMetadata(winner, other)
}

func mergeAccountUsageViewMetadata(existing, next models.AccountUsageView) models.AccountUsageView {
	if existing.AccountID == "" && next.AccountID != "" {
		existing.AccountID = next.AccountID
	}
	if existing.PlanType == "" && next.PlanType != "" {
		existing.PlanType = next.PlanType
	}
	if existing.AccountDetail == "" && next.AccountDetail != "" {
		existing.AccountDetail = next.AccountDetail
	}
	if existing.BillingLabel == "" && next.BillingLabel != "" {
		existing.BillingLabel = next.BillingLabel
	}
	if existing.StatusLabel == "" && next.StatusLabel != "" {
		existing.StatusLabel = next.StatusLabel
	}
	return existing
}

func composeAccountLimits(primary, secondary *models.AccountLimitView, extra []models.AccountLimitView) []models.AccountLimitView {
	sourceLimits := make([]models.AccountLimitView, 0, 2)
	if primary != nil {
		sourceLimits = append(sourceLimits, *primary)
	}
	if secondary != nil {
		sourceLimits = append(sourceLimits, *secondary)
	}
	sort.SliceStable(sourceLimits, func(i, j int) bool {
		return accountLimitWindowOrder(sourceLimits[i]) < accountLimitWindowOrder(sourceLimits[j])
	})
	limits := make([]models.AccountLimitView, 0, len(sourceLimits)+len(extra))
	seen := make(map[string]bool, len(sourceLimits))
	for _, limit := range sourceLimits {
		if key := strings.TrimSpace(limit.LimitKey); key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		limits = append(limits, limit)
	}
	limits = append(limits, extra...)
	return limits
}

func accountLimitWindowOrder(limit models.AccountLimitView) int {
	if limit.WindowMinutes == nil {
		return 5
	}
	switch classifyOpenAIWindow(*limit.WindowMinutes) {
	case openAIWindowFiveHour:
		return 0
	case openAIWindowDaily:
		return 1
	case openAIWindowWeekly:
		return 2
	case openAIWindowMonthly:
		return 3
	case openAIWindowAnnual:
		return 4
	default:
		return 5
	}
}

func accountUsageDisplayKey(account models.AccountUsageView, configsByID map[string]models.LLMConfig) string {
	provider := strings.TrimSpace(account.Provider)
	if cfg, ok := configsByID[account.AgentConfigID]; ok && strings.TrimSpace(cfg.OAuthAccountID) != "" {
		return provider + "\x00account\x00" + strings.TrimSpace(cfg.OAuthAccountID)
	}
	accountID := strings.TrimSpace(account.AccountID)
	if accountID != "" {
		return provider + "\x00account\x00" + accountID
	}
	return accountUsageKeyForViewWithConfigs(account, configsByID)
}

func repairAccountSnapshotFromRawJSON(snapshot models.AccountUsageSnapshot) models.AccountUsageSnapshot {
	if isAccountRefreshFailure(snapshot.RateLimitReachedType) || strings.TrimSpace(snapshot.RawJSON) == "" {
		return snapshot
	}
	hasParsedLimits := accountSnapshotHasParsedLimits(snapshot)
	if hasParsedLimits && models.LLMProvider(snapshot.Provider) != models.ProviderAnthropic {
		return snapshot
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(snapshot.RawJSON), &raw); err != nil || len(raw) == 0 {
		return snapshot
	}
	cfg := models.LLMConfig{ID: snapshot.AgentConfigID, OAuthAccountID: snapshot.AccountID}
	var repaired *models.AccountUsageSnapshot
	var err error
	switch models.LLMProvider(snapshot.Provider) {
	case models.ProviderAnthropic:
		cfg.Provider = models.ProviderAnthropic
		repaired, err = normalizeAnthropicAccountUsage(cfg, raw)
	case models.ProviderOpenAI:
		cfg.Provider = models.ProviderOpenAI
		repaired, err = normalizeOpenAIAccountUsage(cfg, raw)
	}
	if err != nil || repaired == nil || !accountSnapshotHasParsedLimits(*repaired) {
		return snapshot
	}
	snapshot.PlanType = firstNonEmptyUsageString(snapshot.PlanType, repaired.PlanType)
	if snapshot.CreditsRemaining == nil {
		snapshot.CreditsRemaining = repaired.CreditsRemaining
	}
	snapshot.PrimaryLabel = repaired.PrimaryLabel
	snapshot.PrimaryUsedPercent = repaired.PrimaryUsedPercent
	snapshot.PrimaryWindowMinutes = repaired.PrimaryWindowMinutes
	snapshot.PrimaryResetsAt = repaired.PrimaryResetsAt
	snapshot.SecondaryLabel = repaired.SecondaryLabel
	snapshot.SecondaryUsedPercent = repaired.SecondaryUsedPercent
	snapshot.SecondaryWindowMinutes = repaired.SecondaryWindowMinutes
	snapshot.SecondaryResetsAt = repaired.SecondaryResetsAt
	snapshot.ModelLimitLabel = repaired.ModelLimitLabel
	snapshot.ModelLimitUsedPercent = repaired.ModelLimitUsedPercent
	snapshot.ModelLimitWindowMinutes = repaired.ModelLimitWindowMinutes
	snapshot.ModelLimitResetsAt = repaired.ModelLimitResetsAt
	if len(repaired.ExtraLimits) > 0 {
		snapshot.ExtraLimits = repaired.ExtraLimits
	}
	return snapshot
}

func accountSnapshotHasParsedLimits(snapshot models.AccountUsageSnapshot) bool {
	return snapshot.PrimaryLabel != "" || snapshot.PrimaryUsedPercent != nil || snapshot.SecondaryLabel != "" || snapshot.SecondaryUsedPercent != nil || snapshot.ModelLimitLabel != "" || snapshot.ModelLimitUsedPercent != nil || len(snapshot.ExtraLimits) > 0
}

func sanitizeSnapshotAccountDisplay(snapshot models.AccountUsageSnapshot, configsByID map[string]models.LLMConfig) models.AccountUsageSnapshot {
	cfg, ok := configsByID[snapshot.AgentConfigID]
	if !ok {
		return snapshot
	}
	if strings.TrimSpace(cfg.OAuthAccountID) == "" && strings.TrimSpace(snapshot.AccountID) == strings.TrimSpace(snapshot.AgentConfigID) {
		snapshot.AccountID = ""
	}
	return snapshot
}

func accountViewFromSnapshot(snapshot models.AccountUsageSnapshot) models.AccountUsageView {
	snapshot = repairAccountSnapshotFromRawJSON(snapshot)
	errorMessage := ""
	if isAccountRefreshFailure(snapshot.RateLimitReachedType) {
		errorMessage = accountRefreshFailureMessage(snapshot.RateLimitReachedType)
	}

	var primaryLimit *models.AccountLimitView
	if snapshot.PrimaryLabel != "" || snapshot.PrimaryUsedPercent != nil {
		primaryLimit = accountLimitViewFromSnapshotWindow(
			snapshot.Provider,
			"primary",
			snapshot.PrimaryLabel,
			"5-hour session",
			snapshot.PrimaryUsedPercent,
			snapshot.PrimaryWindowMinutes,
			snapshot.PrimaryResetsAt,
		)
	}
	var secondaryLimit *models.AccountLimitView
	if snapshot.SecondaryLabel != "" || snapshot.SecondaryUsedPercent != nil {
		secondaryLimit = accountLimitViewFromSnapshotWindow(
			snapshot.Provider,
			"secondary",
			snapshot.SecondaryLabel,
			"weekly limit",
			snapshot.SecondaryUsedPercent,
			snapshot.SecondaryWindowMinutes,
			snapshot.SecondaryResetsAt,
		)
	}
	extraLimits := accountExtraLimitViews(snapshot)
	limits := composeAccountLimits(primaryLimit, secondaryLimit, extraLimits)
	view := models.AccountUsageView{
		Provider:             snapshot.Provider,
		AccountID:            snapshot.AccountID,
		AgentConfigID:        snapshot.AgentConfigID,
		PlanType:             snapshot.PlanType,
		AccountDetail:        snapshot.AccountDetail,
		BillingLabel:         snapshot.BillingLabel,
		StatusLabel:          snapshot.SubscriptionStatus,
		ExtraUsageLabel:      snapshot.ExtraUsageLabel,
		ExtraUsageMonthlyUSD: snapshot.ExtraUsageMonthlyUSD,
		ExtraUsageUsedUSD:    snapshot.ExtraUsageUsedUSD,
		UpdatedAt:            snapshot.FetchedAt,
		PrimaryLimit:         primaryLimit,
		SecondaryLimit:       secondaryLimit,
		ExtraLimits:          extraLimits,
		Limits:               limits,
		Error:                errorMessage,
	}
	return applyAccountUsageDisplayPolicy(view)
}

func accountLimitViewFromSnapshotWindow(provider, sourceKey, label, fallback string, used *float64, windowMinutes *int, resetsAt *string) *models.AccountLimitView {
	limitKey := sourceKey
	if models.LLMProvider(strings.ToLower(strings.TrimSpace(provider))) == models.ProviderOpenAI {
		kind := openAIWindowUnknown
		if windowMinutes != nil {
			kind = classifyOpenAIWindow(*windowMinutes)
		}
		label = openAIWindowDisplayLabel(kind)
		if kind != openAIWindowUnknown {
			limitKey = kind
		}
	} else {
		label = defaultLimitLabel(label, fallback)
		if sourceKey == "primary" {
			limitKey = "five_hour"
		} else if sourceKey == "secondary" {
			limitKey = "seven_day"
		}
	}
	return &models.AccountLimitView{
		LimitKey:      limitKey,
		Label:         label,
		UsedPercent:   used,
		WindowMinutes: windowMinutes,
		ResetsAt:      resetsAt,
		Status:        limitStatus(used),
	}
}

func applyAccountUsageDisplayPolicy(view models.AccountUsageView) models.AccountUsageView {
	if models.LLMProvider(strings.ToLower(strings.TrimSpace(view.Provider))) == models.ProviderAnthropic {
		view.AccountDetail = ""
		view.BillingLabel = ""
		view.StatusLabel = ""
	}
	return view
}

func accountExtraLimitViews(snapshot models.AccountUsageSnapshot) []models.AccountLimitView {
	seen := map[string]int{}
	views := make([]models.AccountLimitView, 0, len(snapshot.ExtraLimits)+1)
	for _, limit := range snapshot.ExtraLimits {
		key := strings.TrimSpace(limit.LimitKey)
		if key == "" {
			key = strings.TrimSpace(limit.Label)
		}
		if key == "" {
			continue
		}
		view := models.AccountLimitView{
			LimitKey:      key,
			Label:         defaultLimitLabel(limit.Label, key),
			UsedPercent:   limit.UsedPercent,
			WindowMinutes: limit.WindowMinutes,
			ResetsAt:      limit.ResetAt,
			Status:        limitStatus(limit.UsedPercent),
		}
		if i, ok := seen[key]; ok {
			views[i] = preferAccountLimitView(views[i], view)
			continue
		}
		seen[key] = len(views)
		views = append(views, view)
	}
	if len(views) == 0 && (snapshot.ModelLimitLabel != "" || snapshot.ModelLimitUsedPercent != nil) {
		views = append(views, models.AccountLimitView{
			LimitKey:      "legacy_model_limit",
			Label:         defaultLimitLabel(snapshot.ModelLimitLabel, "model limit"),
			UsedPercent:   snapshot.ModelLimitUsedPercent,
			WindowMinutes: snapshot.ModelLimitWindowMinutes,
			ResetsAt:      snapshot.ModelLimitResetsAt,
			Status:        limitStatus(snapshot.ModelLimitUsedPercent),
		})
	}
	return views
}

func preferAccountLimitView(existing, next models.AccountLimitView) models.AccountLimitView {
	if next.UsedPercent != nil || existing.UsedPercent == nil {
		return next
	}
	return existing
}

func accountUsageKey(provider, accountID, agentConfigID string) string {
	if strings.TrimSpace(accountID) != "" {
		return provider + "\x00account\x00" + strings.TrimSpace(accountID)
	}
	return provider + "\x00config\x00" + strings.TrimSpace(agentConfigID)
}

func accountUsageKeyForConfig(cfg models.LLMConfig) string {
	provider := string(cfg.Provider)
	if strings.TrimSpace(cfg.OAuthAccountID) != "" {
		return accountUsageKey(provider, cfg.OAuthAccountID, cfg.ID)
	}
	return accountUsageKey(provider, "", cfg.ID)
}

func accountUsageKeyForView(view models.AccountUsageView) string {
	return accountUsageKey(view.Provider, view.AccountID, view.AgentConfigID)
}

func accountUsageKeyForViewWithConfigs(view models.AccountUsageView, configsByID map[string]models.LLMConfig) string {
	if cfg, ok := configsByID[view.AgentConfigID]; ok {
		return accountUsageKeyForConfig(cfg)
	}
	return accountUsageKeyForView(view)
}

func accountUsageKeyForSnapshot(snapshot models.AccountUsageSnapshot) string {
	return accountUsageKey(snapshot.Provider, snapshot.AccountID, snapshot.AgentConfigID)
}

func accountUsageKeyForSnapshotWithConfigs(snapshot models.AccountUsageSnapshot, configsByID map[string]models.LLMConfig) string {
	if cfg, ok := configsByID[snapshot.AgentConfigID]; ok {
		return accountUsageKeyForConfig(cfg)
	}
	return accountUsageKeyForSnapshot(snapshot)
}

func snapshotMatchesConfigAccount(snapshot models.AccountUsageSnapshot, cfg models.LLMConfig) bool {
	if strings.TrimSpace(snapshot.Provider) != string(cfg.Provider) {
		return false
	}
	if strings.TrimSpace(snapshot.AccountID) != "" && strings.TrimSpace(cfg.OAuthAccountID) != "" {
		return strings.TrimSpace(snapshot.AccountID) == strings.TrimSpace(cfg.OAuthAccountID)
	}
	if strings.TrimSpace(snapshot.AgentConfigID) != "" && strings.TrimSpace(snapshot.AgentConfigID) == strings.TrimSpace(cfg.ID) {
		return true
	}
	return false
}

type anthropicOAuthProfile struct {
	AccountID       string
	DisplayName     string
	Detail          string
	PlanLabel       string
	BillingLabel    string
	StatusLabel     string
	ExtraUsageLabel string
}

func normalizeAnthropicOAuthProfile(raw map[string]any) anthropicOAuthProfile {
	if raw == nil {
		return anthropicOAuthProfile{}
	}
	org, _ := rawMap(firstPresent(raw, "organization", "org"))
	account, _ := rawMap(firstPresent(raw, "account", "user"))
	profile := anthropicOAuthProfile{
		DisplayName: strings.TrimSpace(firstNonEmptyUsageString(stringValue(account["display_name"]), stringValue(account["name"]))),
		Detail:      strings.TrimSpace(firstNonEmptyUsageString(stringValue(account["email"]), stringValue(account["display_name"]), stringValue(account["name"]))),
	}
	if id := firstNonEmptyUsageString(stringValue(firstPresent(org, "uuid", "id"))); id != "" {
		profile.AccountID = "organization:" + id
	} else if id := firstNonEmptyUsageString(stringValue(firstPresent(account, "uuid", "id"))); id != "" {
		profile.AccountID = "account:" + id
	} else if id := firstNonEmptyUsageString(stringValue(firstPresent(raw, "organization_uuid", "organization_id", "account_uuid", "account_id", "uuid", "id"))); id != "" {
		profile.AccountID = "account:" + id
	}
	profile.PlanLabel = anthropicSubscriptionLabel(org, account)
	profile.BillingLabel = anthropicBillingLabel(stringValue(org["billing_type"]))
	profile.StatusLabel = anthropicSubscriptionStatusLabel(stringValue(org["subscription_status"]))
	if b, ok := boolValue(org["has_extra_usage_enabled"]); ok && b {
		profile.ExtraUsageLabel = "Usage credits enabled"
	}
	return profile
}

func applyAnthropicProfileToSnapshot(snapshot *models.AccountUsageSnapshot, profile anthropicOAuthProfile) {
	if snapshot == nil {
		return
	}
	if profile.AccountID != "" {
		snapshot.AccountID = profile.AccountID
	}
	if profile.PlanLabel != "" {
		snapshot.PlanType = profile.PlanLabel
	}
	if profile.DisplayName != "" {
		snapshot.AccountDisplayName = profile.DisplayName
	}
	if profile.Detail != "" {
		snapshot.AccountDetail = profile.Detail
	}
	if profile.BillingLabel != "" {
		snapshot.BillingLabel = profile.BillingLabel
	}
	if profile.StatusLabel != "" {
		snapshot.SubscriptionStatus = profile.StatusLabel
	}
	if profile.ExtraUsageLabel != "" {
		snapshot.ExtraUsageLabel = profile.ExtraUsageLabel
	}
}

func applyAnthropicExtraUsageToSnapshot(snapshot *models.AccountUsageSnapshot, raw map[string]any) {
	if snapshot == nil || raw == nil {
		return
	}
	extra, ok := rawMap(raw["extra_usage"])
	if !ok {
		return
	}
	if enabled, ok := boolValue(extra["is_enabled"]); ok {
		if enabled {
			snapshot.ExtraUsageLabel = "Usage credits enabled"
		} else if snapshot.ExtraUsageLabel == "" {
			snapshot.ExtraUsageLabel = "Usage credits disabled"
		}
	}
	if cents, ok := floatValue(extra["monthly_limit"]); ok {
		v := cents / 100
		snapshot.ExtraUsageMonthlyUSD = &v
	}
	if cents, ok := floatValue(extra["used_credits"]); ok {
		v := cents / 100
		snapshot.ExtraUsageUsedUSD = &v
	}
}

func anthropicSubscriptionLabel(org, account map[string]any) string {
	switch strings.TrimSpace(stringValue(org["rate_limit_tier"])) {
	case "default_claude_max_20x":
		return "Claude Max (20x)"
	case "default_claude_max_5x":
		return "Claude Max (5x)"
	}
	if b, ok := boolValue(account["has_claude_max"]); ok && b {
		return "Claude Max"
	}
	if b, ok := boolValue(account["has_claude_pro"]); ok && b {
		return "Claude Pro"
	}
	switch strings.TrimSpace(stringValue(org["organization_type"])) {
	case "claude_max":
		return "Claude Max"
	case "claude_pro":
		return "Claude Pro"
	case "claude_team", "team":
		return "Claude Team"
	case "claude_enterprise", "enterprise", "enterprise_usage_based":
		return "Claude Enterprise"
	}
	return "Anthropic subscription"
}

func anthropicBillingLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "stripe_subscription":
		return "Subscription billing"
	case "stripe_subscription_contracted":
		return "Contract subscription"
	case "apple_subscription":
		return "Apple subscription"
	case "google_play_subscription":
		return "Google Play subscription"
	case "enterprise":
		return "Enterprise billing"
	case "team":
		return "Team billing"
	default:
		return humanizeUsageLabel(value)
	}
}

func anthropicSubscriptionStatusLabel(value string) string {
	switch strings.TrimSpace(value) {
	case "active":
		return "Active"
	case "pending":
		return "Pending"
	case "disabled":
		return "Disabled"
	default:
		return humanizeUsageLabel(value)
	}
}

func openAIPlanLabel(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "free":
		return "ChatGPT Free"
	case "plus":
		return "ChatGPT Plus"
	case "pro":
		return "ChatGPT Pro"
	case "team":
		return "ChatGPT Team"
	case "enterprise":
		return "ChatGPT Enterprise"
	case "edu", "education":
		return "ChatGPT Edu"
	case "":
		return "OpenAI subscription"
	default:
		return humanizeUsageLabel(value)
	}
}

func humanizeUsageLabel(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parts := strings.Fields(strings.ReplaceAll(value, "_", " "))
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}

const (
	openAIWindowFiveHour = "five_hour"
	openAIWindowDaily    = "daily"
	openAIWindowWeekly   = "weekly"
	openAIWindowMonthly  = "monthly"
	openAIWindowAnnual   = "annual"
	openAIWindowUnknown  = "unknown"
)

var openAIWindowDurations = []struct {
	kind    string
	minutes int
}{
	{kind: openAIWindowFiveHour, minutes: 300},
	{kind: openAIWindowDaily, minutes: 1440},
	{kind: openAIWindowWeekly, minutes: 10080},
	{kind: openAIWindowMonthly, minutes: 43200},
	{kind: openAIWindowAnnual, minutes: 525600},
}

func classifyOpenAIWindow(minutes int) string {
	if minutes <= 0 {
		return openAIWindowUnknown
	}
	for _, candidate := range openAIWindowDurations {
		tolerance := float64(candidate.minutes) * 0.05
		if math.Abs(float64(minutes-candidate.minutes)) <= tolerance {
			return candidate.kind
		}
	}
	return openAIWindowUnknown
}

func openAIWindowDisplayLabel(kind string) string {
	switch kind {
	case openAIWindowFiveHour:
		return "5-hour session"
	case openAIWindowDaily:
		return "daily limit"
	case openAIWindowWeekly:
		return "weekly limit"
	case openAIWindowMonthly:
		return "monthly limit"
	case openAIWindowAnnual:
		return "annual limit"
	default:
		return "usage limit"
	}
}

func defaultLimitLabel(label, fallback string) string {
	if strings.TrimSpace(label) != "" {
		return label
	}
	return fallback
}

func limitStatus(used *float64) string {
	if used == nil {
		return "unknown"
	}
	if *used >= 90 {
		return "warning"
	}
	if *used >= 70 {
		return "watch"
	}
	return "safe"
}

func (s *UsageAnalyticsService) resolveAccountUsageOAuthAccountID(ctx context.Context, cfg models.LLMConfig) models.LLMConfig {
	if strings.TrimSpace(cfg.OAuthAccountID) != "" || strings.TrimSpace(cfg.OAuthAccessToken) == "" {
		return cfg
	}
	var accountID string
	switch cfg.Provider {
	case models.ProviderOpenAI:
		accountID = openaiclient.ExtractChatGPTAccountID(cfg.OAuthAccessToken)
	case models.ProviderAnthropic:
		fresh, profile, err := s.resolveAnthropicOAuthProfile(ctx, cfg)
		if err != nil {
			applog.Infof("[usage] resolving Anthropic OAuth account profile failed provider=%s: %v", cfg.Provider, sanitizeAccountUsageError(err))
			return cfg
		}
		cfg = fresh
		accountID = profile.AccountID
	default:
		return cfg
	}
	if accountID == "" {
		return cfg
	}
	cfg.OAuthAccountID = accountID
	if s.llmConfigRepo != nil && strings.TrimSpace(cfg.ID) != "" {
		if err := s.llmConfigRepo.UpdateOAuthTokens(ctx, cfg.ID, cfg.OAuthAccessToken, cfg.OAuthRefreshToken, cfg.OAuthExpiresAt, accountID); err != nil {
			applog.Infof("[usage] persisting OAuth account id failed provider=%s: %v", cfg.Provider, err)
		}
	}
	return cfg
}

func (s *UsageAnalyticsService) fetchOAuthAccountUsage(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
	switch cfg.Provider {
	case models.ProviderAnthropic:
		fresh, err := s.ensureFreshAccountUsageOAuth(ctx, cfg, s.anthropicAccountUsageRefreshFunc())
		if err != nil {
			return nil, err
		}
		return s.fetchAnthropicOAuthUsage(ctx, fresh)
	case models.ProviderOpenAI:
		fresh, err := s.ensureFreshAccountUsageOAuth(ctx, cfg, s.openAIAccountUsageRefreshFunc())
		if err != nil {
			return nil, err
		}
		return s.fetchOpenAIOAuthUsage(ctx, fresh)
	default:
		return nil, fmt.Errorf("provider %s does not expose OAuth account usage", cfg.Provider)
	}
}

func (s *UsageAnalyticsService) ensureFreshAccountUsageOAuth(ctx context.Context, cfg models.LLMConfig, refresh oauthRefreshFunc) (models.LLMConfig, error) {
	if s.oauthRecovery == nil {
		return cfg, nil
	}
	return s.oauthRecovery.EnsureFresh(ctx, cfg, time.Hour, accountUsageOAuthRefreshAdapter(refresh))
}

func (s *UsageAnalyticsService) recoverAccountUsageUnauthorized(ctx context.Context, cfg models.LLMConfig, tokenUsed string, refresh oauthRefreshFunc) (models.LLMConfig, bool, error) {
	if s.oauthRecovery == nil {
		return cfg, false, nil
	}
	return s.oauthRecovery.RecoverUnauthorized(ctx, cfg, tokenUsed, accountUsageOAuthRefreshAdapter(refresh))
}

func accountUsageOAuthRefreshAdapter(refresh oauthRefreshFunc) llmoauth.RefreshFunc {
	if refresh == nil {
		return nil
	}
	return func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		return refresh(ctx, cfg)
	}
}

func (s *UsageAnalyticsService) anthropicAccountUsageRefreshFunc() oauthRefreshFunc {
	if s.anthropicOAuthRefresher != nil {
		return s.anthropicOAuthRefresher
	}
	return s.defaultAnthropicAccountUsageRefreshFunc()
}

func (s *UsageAnalyticsService) defaultAnthropicAccountUsageRefreshFunc() oauthRefreshFunc {
	return func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		auth, err := anthropicclient.RefreshToken(cfg.OAuthRefreshToken)
		if err != nil {
			return llmoauth.TokenSet{}, err
		}
		return llmoauth.TokenSet{AccessToken: auth.Token, RefreshToken: auth.RefreshToken, ExpiresAt: auth.ExpiresAt}, nil
	}
}

func (s *UsageAnalyticsService) openAIAccountUsageRefreshFunc() oauthRefreshFunc {
	if s.openAIOAuthRefresher != nil {
		return s.openAIOAuthRefresher
	}
	return s.defaultOpenAIAccountUsageRefreshFunc()
}

func (s *UsageAnalyticsService) defaultOpenAIAccountUsageRefreshFunc() oauthRefreshFunc {
	return func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		auth, err := openaiclient.RefreshToken(cfg.OAuthRefreshToken)
		if err != nil {
			return llmoauth.TokenSet{}, err
		}
		accountID := strings.TrimSpace(cfg.OAuthAccountID)
		if accountID == "" {
			accountID = openaiclient.ExtractChatGPTAccountID(auth.Token)
		}
		return llmoauth.TokenSet{AccessToken: auth.Token, RefreshToken: auth.RefreshToken, ExpiresAt: auth.ExpiresAt, AccountID: accountID}, nil
	}
}

func (s *UsageAnalyticsService) fetchAnthropicOAuthUsage(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
	profileCfg, profile, profileErr := s.resolveAnthropicOAuthProfile(ctx, cfg)
	if profileErr == nil {
		cfg = profileCfg
		if profile.AccountID != "" && cfg.OAuthAccountID != profile.AccountID {
			cfg.OAuthAccountID = profile.AccountID
			if s.llmConfigRepo != nil && strings.TrimSpace(cfg.ID) != "" {
				if err := s.llmConfigRepo.UpdateOAuthTokens(ctx, cfg.ID, cfg.OAuthAccessToken, cfg.OAuthRefreshToken, cfg.OAuthExpiresAt, cfg.OAuthAccountID); err != nil {
					applog.Infof("[usage] persisting Anthropic OAuth profile account id failed provider=%s: %v", cfg.Provider, err)
				}
			}
		}
	} else {
		applog.Infof("[usage] resolving Anthropic OAuth account profile failed provider=%s: %v", cfg.Provider, sanitizeAccountUsageError(profileErr))
	}
	endpoint := strings.TrimRight(anthropicclient.AnthropicAPIHost, "/") + "/api/oauth/usage"
	raw, err := s.doAccountUsageRequestWithOAuthRecovery(ctx, cfg, endpoint, buildAnthropicAccountUsageRequest, s.anthropicAccountUsageRefreshFunc())
	if err != nil {
		return nil, err
	}
	snapshot, err := normalizeAnthropicAccountUsage(cfg, raw)
	if err != nil {
		return nil, err
	}
	applyAnthropicProfileToSnapshot(snapshot, profile)
	applyAnthropicExtraUsageToSnapshot(snapshot, raw)
	return snapshot, nil
}

func (s *UsageAnalyticsService) resolveAnthropicOAuthProfile(ctx context.Context, cfg models.LLMConfig) (models.LLMConfig, anthropicOAuthProfile, error) {
	fresh, err := s.ensureFreshAccountUsageOAuth(ctx, cfg, s.anthropicAccountUsageRefreshFunc())
	if err != nil {
		return cfg, anthropicOAuthProfile{}, err
	}
	endpoint := strings.TrimRight(anthropicclient.AnthropicAPIHost, "/") + "/api/oauth/profile"
	raw, err := s.doAccountUsageRequestWithOAuthRecovery(ctx, fresh, endpoint, buildAnthropicAccountProfileRequest, s.anthropicAccountUsageRefreshFunc())
	if err != nil {
		return fresh, anthropicOAuthProfile{}, err
	}
	return fresh, normalizeAnthropicOAuthProfile(raw), nil
}

func (s *UsageAnalyticsService) fetchOpenAIOAuthUsage(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
	endpoint, err := openAIUsageEndpoint()
	if err != nil {
		return nil, err
	}
	raw, err := s.doAccountUsageRequestWithOAuthRecovery(ctx, cfg, endpoint, buildOpenAIAccountUsageRequest, s.openAIAccountUsageRefreshFunc())
	if err != nil {
		return nil, err
	}
	return normalizeOpenAIAccountUsage(cfg, raw)
}

type accountUsageRequestBuilder func(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error)

func buildAnthropicAccountUsageRequest(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	applyAnthropicOAuthAccountHeaders(req, cfg)
	return req, nil
}

func buildAnthropicAccountProfileRequest(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	applyAnthropicOAuthAccountHeaders(req, cfg)
	return req, nil
}

func applyAnthropicOAuthAccountHeaders(req *http.Request, cfg models.LLMConfig) {
	req.Header.Set("Authorization", "Bearer "+cfg.OAuthAccessToken)
	req.Header.Set("anthropic-beta", anthropicclient.OAuthBetaHeader)
	req.Header.Set("Content-Type", "application/json")
}

func buildOpenAIAccountUsageRequest(ctx context.Context, endpoint string, cfg models.LLMConfig) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.OAuthAccessToken)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Accept", "application/json")
	accountID := strings.TrimSpace(cfg.OAuthAccountID)
	if accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", accountID)
	}
	return req, nil
}

func (s *UsageAnalyticsService) doAccountUsageRequestWithOAuthRecovery(ctx context.Context, cfg models.LLMConfig, endpoint string, build accountUsageRequestBuilder, refresh oauthRefreshFunc) (map[string]any, error) {
	tokenUsed := cfg.OAuthAccessToken
	req, err := build(ctx, endpoint, cfg)
	if err != nil {
		return nil, err
	}
	raw, err := s.doAccountUsageRequest(req)
	if err == nil {
		return raw, nil
	}
	if !isAccountUsageHTTPStatus(err, http.StatusUnauthorized) || tokenUsed == "" {
		return nil, err
	}
	fresh, recovered, recoverErr := s.recoverAccountUsageUnauthorized(ctx, cfg, tokenUsed, refresh)
	if recoverErr != nil {
		return nil, recoverErr
	}
	if !recovered || strings.TrimSpace(fresh.OAuthAccessToken) == "" || fresh.OAuthAccessToken == tokenUsed {
		return nil, err
	}
	req, buildErr := build(ctx, endpoint, fresh)
	if buildErr != nil {
		return nil, buildErr
	}
	return s.doAccountUsageRequest(req)
}

func isAccountUsageHTTPStatus(err error, status int) bool {
	var httpErr accountUsageHTTPError
	return errors.As(err, &httpErr) && httpErr.StatusCode == status
}

func (s *UsageAnalyticsService) doAccountUsageRequest(req *http.Request) (map[string]any, error) {
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, accountUsageHTTPError{Method: req.Method, URL: req.URL.String(), StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type")}
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode account usage: %w", err)
	}
	return raw, nil
}

type accountUsageHTTPError struct {
	Method      string
	URL         string
	StatusCode  int
	ContentType string
}

func (e accountUsageHTTPError) Error() string {
	return fmt.Sprintf("%s %q failed with HTTP %d", e.Method, e.URL, e.StatusCode)
}

func openAIUsageEndpoint() (string, error) {
	base := strings.TrimSpace(openaiclient.OpenAIChatGPTAPIBaseURL)
	if base == "" {
		return "", fmt.Errorf("missing OpenAI OAuth base URL")
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" || path == "/" || strings.HasPrefix(path, "/backend-api") || strings.Contains(path, "/codex") {
		u.Path = "/backend-api/wham/usage"
	} else {
		u.Path = strings.TrimRight(path, "/") + "/wham/usage"
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func normalizeAnthropicAccountUsage(cfg models.LLMConfig, raw map[string]any) (*models.AccountUsageSnapshot, error) {
	rawJSON, _ := json.Marshal(sanitizeAccountUsageRawJSON(raw, string(models.ProviderAnthropic)))
	snapshot := &models.AccountUsageSnapshot{
		Provider:      string(cfg.Provider),
		AccountID:     accountIDForConfig(cfg),
		AgentConfigID: cfg.ID,
		RawJSON:       string(rawJSON),
	}
	if window, ok := rawMap(firstPresent(raw, "fiveHour", "five_hour")); ok {
		applyAnthropicWindow(snapshot, window, "primary", "5-hour session", 300)
	}
	if window, ok := rawMap(firstPresent(raw, "sevenDay", "seven_day")); ok {
		applyAnthropicWindow(snapshot, window, "secondary", "weekly limit", 10080)
	}
	applyAnthropicExtraWindows(snapshot, raw)
	return snapshot, nil
}

func applyAnthropicExtraWindows(snapshot *models.AccountUsageSnapshot, raw map[string]any) {
	if snapshot == nil || raw == nil {
		return
	}
	emitted := map[string]bool{}
	emit := func(key, label string, value any) {
		canonical := canonicalAnthropicLimitKey(key)
		if canonical == "" || emitted[canonical] || canonical == "five_hour" || canonical == "seven_day" {
			return
		}
		window, ok := rawMap(value)
		if !ok {
			return
		}
		limit := accountExtraLimitFromWindow(*snapshot, canonical, label, window, 10080, false)
		if limit.UsedPercent == nil && limit.ResetAt == nil {
			return
		}
		emitted[canonical] = true
		snapshot.ExtraLimits = append(snapshot.ExtraLimits, limit)
	}
	emit("seven_day_opus", "Opus weekly limit", firstPresent(raw, "sevenDayOpus", "seven_day_opus"))
	emit("seven_day_sonnet", "Sonnet weekly limit", firstPresent(raw, "sevenDaySonnet", "seven_day_sonnet"))

	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		canonical := canonicalAnthropicLimitKey(key)
		if canonical == "" || emitted[canonical] || canonical == "five_hour" || canonical == "seven_day" {
			continue
		}
		emit(canonical, anthropicLimitLabel(canonical), raw[key])
	}
}

func canonicalAnthropicLimitKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	var b strings.Builder
	for i, r := range key {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		if r == '-' || r == ' ' {
			b.WriteByte('_')
			continue
		}
		b.WriteRune(r)
	}
	canonical := strings.ToLower(strings.Trim(b.String(), "_"))
	if !strings.Contains(canonical, "day") && !strings.Contains(canonical, "hour") && !strings.Contains(canonical, "limit") {
		return ""
	}
	return canonical
}

func anthropicLimitLabel(key string) string {
	switch key {
	case "seven_day_opus":
		return "Opus weekly limit"
	case "seven_day_sonnet":
		return "Sonnet weekly limit"
	}
	label := strings.ReplaceAll(key, "_", " ")
	label = strings.TrimSpace(label)
	if label == "" {
		return key
	}
	return label + " limit"
}

func applyAnthropicWindow(snapshot *models.AccountUsageSnapshot, window map[string]any, target, label string, windowMinutes int) {
	if snapshot == nil {
		return
	}
	var used *float64
	if v, ok := floatValue(firstPresent(window, "utilization", "used_percent", "usedPercent")); ok {
		used = &v
	}
	minutes := windowMinutes
	reset := normalizeResetTime(firstPresent(window, "resetsAt", "resets_at", "reset_at"))
	switch target {
	case "primary":
		snapshot.PrimaryLabel = label
		snapshot.PrimaryUsedPercent = used
		snapshot.PrimaryWindowMinutes = &minutes
		if reset != "" {
			snapshot.PrimaryResetsAt = &reset
		}
	case "secondary":
		snapshot.SecondaryLabel = label
		snapshot.SecondaryUsedPercent = used
		snapshot.SecondaryWindowMinutes = &minutes
		if reset != "" {
			snapshot.SecondaryResetsAt = &reset
		}
	}
}

func normalizeOpenAIAccountUsage(cfg models.LLMConfig, raw map[string]any) (*models.AccountUsageSnapshot, error) {
	rawJSON, _ := json.Marshal(sanitizeAccountUsageRawJSON(raw, string(models.ProviderOpenAI)))
	planType := openAIPlanLabel(firstNonEmptyUsageString(stringValue(raw["plan_type"]), stringValue(raw["planType"])))
	snapshot := &models.AccountUsageSnapshot{
		Provider:      string(cfg.Provider),
		AccountID:     accountIDForConfig(cfg),
		AgentConfigID: cfg.ID,
		PlanType:      planType,
		RawJSON:       string(rawJSON),
	}
	if rateLimit, ok := rawMap(raw["rate_limit"]); ok {
		if primary, ok := rawMap(rateLimit["primary_window"]); ok {
			applyOpenAIWindow(snapshot, primary, true)
		}
		if secondary, ok := rawMap(rateLimit["secondary_window"]); ok {
			applyOpenAIWindow(snapshot, secondary, false)
		}
		if snapshot.RateLimitReachedType == "" {
			snapshot.RateLimitReachedType = firstNonEmptyUsageString(stringValue(rateLimit["rate_limit_reached_type"]), stringValue(raw["rate_limit_reached_type"]))
		}
	}
	if rateLimits, ok := rawMap(raw["rate_limits"]); ok {
		if snapshot.PlanType == "" || snapshot.PlanType == "OpenAI subscription" {
			snapshot.PlanType = openAIPlanLabel(firstNonEmptyUsageString(stringValue(rateLimits["plan_type"]), stringValue(rateLimits["planType"])))
		}
		if primary, ok := rawMap(rateLimits["primary"]); ok {
			applyOpenAIWindow(snapshot, primary, true)
		}
		if secondary, ok := rawMap(rateLimits["secondary"]); ok {
			applyOpenAIWindow(snapshot, secondary, false)
		}
		if credits, ok := rawMap(rateLimits["credits"]); ok {
			applyOpenAICredits(snapshot, credits)
		}
		if spendControl, ok := rawMap(rateLimits["spend_control"]); ok {
			applyOpenAISpendControl(snapshot, spendControl)
		}
	}
	if primary, ok := rawMap(raw["primary"]); ok {
		applyOpenAIWindow(snapshot, primary, true)
	}
	if secondary, ok := rawMap(raw["secondary"]); ok {
		applyOpenAIWindow(snapshot, secondary, false)
	}
	if credits, ok := rawMap(raw["credits"]); ok {
		applyOpenAICredits(snapshot, credits)
	}
	if spendControl, ok := rawMap(raw["spend_control"]); ok {
		applyOpenAISpendControl(snapshot, spendControl)
	}
	if snapshot.RateLimitReachedType == "" {
		snapshot.RateLimitReachedType = stringValue(raw["rate_limit_reached_type"])
	}
	applyOpenAIAdditionalRateLimits(snapshot, raw["additional_rate_limits"])
	return snapshot, nil
}

func applyOpenAICredits(snapshot *models.AccountUsageSnapshot, credits map[string]any) {
	if snapshot == nil || credits == nil {
		return
	}
	if balance, ok := floatValue(firstPresent(credits, "balance", "credits_remaining", "creditsRemaining")); ok {
		snapshot.CreditsRemaining = &balance
	}
	if overageReached, ok := boolValue(credits["overage_limit_reached"]); ok && overageReached {
		snapshot.ExtraUsageLabel = "Usage credit limit reached"
		return
	}
	if unlimited, ok := boolValue(credits["unlimited"]); ok && unlimited {
		snapshot.ExtraUsageLabel = "Unlimited credits"
		return
	}
	if hasCredits, ok := boolValue(credits["has_credits"]); ok && hasCredits {
		snapshot.ExtraUsageLabel = "Usage credits available"
	}
}

func applyOpenAISpendControl(snapshot *models.AccountUsageSnapshot, spendControl map[string]any) {
	if snapshot == nil || spendControl == nil {
		return
	}
	if reached, ok := boolValue(spendControl["reached"]); ok && reached {
		snapshot.ExtraUsageLabel = "Spend limit reached"
	}
}

func applyOpenAIAdditionalRateLimits(snapshot *models.AccountUsageSnapshot, value any) {
	if snapshot == nil {
		return
	}
	seen := map[string]int{}
	for _, limit := range rateLimitWindows(value) {
		window := limit
		if nested, ok := rawMap(firstPresent(limit, "window", "limit", "primary", "secondary")); ok {
			window = nested
		}
		if rateLimit, ok := rawMap(limit["rate_limit"]); ok {
			if nested, ok := rawMap(firstPresent(rateLimit, "secondary_window", "secondary", "primary_window", "primary")); ok {
				window = nested
			}
		}
		key := firstNonEmptyUsageString(stringValue(limit["metered_feature"]), stringValue(limit["limit_key"]), stringValue(limit["key"]), stringValue(limit["model"]), stringValue(limit["model_name"]))
		if key == "" {
			key = fmt.Sprintf("additional_rate_limit_%d", len(snapshot.ExtraLimits)+1)
		}
		label := firstNonEmptyUsageString(stringValue(limit["limit_name"]), stringValue(limit["label"]), stringValue(limit["name"]), stringValue(limit["model"]), stringValue(limit["model_name"]), key)
		extra := accountExtraLimitFromWindow(*snapshot, key, label, window, 0, false)
		if extra.UsedPercent == nil && extra.WindowMinutes == nil && extra.ResetAt == nil {
			continue
		}
		extra.RawJSON = marshalAccountUsageLimitRaw(limit, string(models.ProviderOpenAI))
		if index, ok := seen[key]; ok {
			snapshot.ExtraLimits[index] = extra
			continue
		}
		seen[key] = len(snapshot.ExtraLimits)
		snapshot.ExtraLimits = append(snapshot.ExtraLimits, extra)
	}
}

func accountExtraLimitFromWindow(snapshot models.AccountUsageSnapshot, key, label string, window map[string]any, defaultWindowMinutes int, normalizeUtilization bool) models.AccountUsageExtraLimit {
	limit := models.AccountUsageExtraLimit{
		Provider:      snapshot.Provider,
		AccountID:     snapshot.AccountID,
		AgentConfigID: snapshot.AgentConfigID,
		LimitKey:      key,
		Label:         label,
		RawJSON:       marshalAccountUsageLimitRaw(window, snapshot.Provider),
	}
	if v, ok := floatValue(firstPresent(window, "used_percent", "usedPercent")); ok {
		limit.UsedPercent = &v
	} else if v, ok := floatValue(window["utilization"]); ok {
		if normalizeUtilization {
			v = normalizePercentValue(v)
		}
		limit.UsedPercent = &v
	}
	if minutes := openAIWindowMinutes(window); minutes != nil {
		limit.WindowMinutes = minutes
	} else if defaultWindowMinutes > 0 {
		v := defaultWindowMinutes
		limit.WindowMinutes = &v
	}
	if reset := normalizeResetTime(firstPresent(window, "reset_at", "resetsAt", "resets_at")); reset != "" {
		limit.ResetAt = &reset
	}
	return limit
}

func marshalAccountUsageLimitRaw(raw map[string]any, provider string) string {
	if raw == nil {
		return "{}"
	}
	payload, err := json.Marshal(sanitizeAccountUsageRawJSON(raw, provider))
	if err != nil || len(payload) == 0 {
		return "{}"
	}
	return string(payload)
}

func rateLimitWindows(value any) []map[string]any {
	switch v := value.(type) {
	case []any:
		windows := make([]map[string]any, 0, len(v))
		for _, item := range v {
			if m, ok := rawMap(item); ok {
				windows = append(windows, m)
			}
		}
		return windows
	case []map[string]any:
		return v
	case map[string]any:
		if firstNonEmptyUsageString(stringValue(v["metered_feature"]), stringValue(v["limit_key"]), stringValue(v["key"]), stringValue(v["model"]), stringValue(v["model_name"])) != "" {
			return []map[string]any{v}
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		windows := make([]map[string]any, 0, len(v))
		for _, key := range keys {
			if m, ok := rawMap(v[key]); ok {
				windows = append(windows, m)
			}
		}
		if len(windows) == 0 {
			windows = append(windows, v)
		}
		return windows
	default:
		return nil
	}
}

func openAIWindowMinutes(window map[string]any) *int {
	if window == nil {
		return nil
	}
	if value, ok := floatValue(firstPresent(window, "window_minutes", "windowDurationMins", "window_duration_mins")); ok && value > 0 {
		minutes := int(math.Ceil(value))
		return &minutes
	}
	if seconds, ok := floatValue(firstPresent(window, "limit_window_seconds", "limitWindowSeconds")); ok && seconds > 0 {
		minutes := int(math.Ceil(seconds / 60))
		return &minutes
	}
	return nil
}

func applyOpenAIWindow(snapshot *models.AccountUsageSnapshot, window map[string]any, primary bool) {
	if snapshot == nil {
		return
	}
	var used *float64
	if v, ok := floatValue(firstPresent(window, "used_percent", "usedPercent")); ok {
		used = &v
	}
	minutes := openAIWindowMinutes(window)
	kind := openAIWindowUnknown
	if minutes != nil {
		kind = classifyOpenAIWindow(*minutes)
	}
	label := openAIWindowDisplayLabel(kind)
	reset := normalizeResetTime(firstPresent(window, "reset_at", "resetsAt", "resets_at"))
	if primary {
		snapshot.PrimaryLabel = label
		snapshot.PrimaryUsedPercent = used
		snapshot.PrimaryWindowMinutes = minutes
		if reset != "" {
			snapshot.PrimaryResetsAt = &reset
		}
		return
	}
	snapshot.SecondaryLabel = label
	snapshot.SecondaryUsedPercent = used
	snapshot.SecondaryWindowMinutes = minutes
	if reset != "" {
		snapshot.SecondaryResetsAt = &reset
	}
}

func normalizePercentValue(value float64) float64 {
	if value > 0 && value <= 1 {
		return value * 100
	}
	return value
}

func sanitizeAccountUsageRawJSON(raw map[string]any, provider string) map[string]any {
	if raw == nil {
		return nil
	}
	clone := cloneAccountUsageMap(raw)
	if provider == string(models.ProviderOpenAI) {
		redactAccountUsageIdentityFields(clone)
	}
	return clone
}

func cloneAccountUsageMap(raw map[string]any) map[string]any {
	clone := make(map[string]any, len(raw))
	for key, value := range raw {
		clone[key] = cloneAccountUsageValue(value)
	}
	return clone
}

func cloneAccountUsageValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneAccountUsageMap(v)
	case []any:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = cloneAccountUsageValue(item)
		}
		return items
	default:
		return value
	}
}

func redactAccountUsageIdentityFields(raw map[string]any) {
	for key, value := range raw {
		if isAccountUsageIdentityKey(key) {
			delete(raw, key)
			continue
		}
		switch v := value.(type) {
		case map[string]any:
			redactAccountUsageIdentityFields(v)
		case []any:
			for _, item := range v {
				if m, ok := item.(map[string]any); ok {
					redactAccountUsageIdentityFields(m)
				}
			}
		}
	}
}

func isAccountUsageIdentityKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "email", "user_id", "userid", "account_id", "accountid":
		return true
	default:
		return false
	}
}

func rawMap(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func firstPresent(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func firstNonEmptyUsageString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringValue(value any) string {
	s, _ := value.(string)
	return strings.TrimSpace(s)
}

func boolValue(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes":
			return true, true
		case "false", "0", "no":
			return false, true
		}
	}
	return false, false
}

func floatValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

func intValue(value any) (int, bool) {
	if f, ok := floatValue(value); ok {
		return int(f), true
	}
	return 0, false
}

func normalizeResetTime(value any) string {
	if s := stringValue(value); s != "" {
		return s
	}
	if seconds, ok := intValue(value); ok && seconds > 0 {
		return time.Unix(int64(seconds), 0).UTC().Format(time.RFC3339)
	}
	return ""
}

type UsageCapture struct {
	ProjectID    string
	TaskID       string
	ExecutionID  string
	ChatThreadID string
	TurnID       string
	Operation    string
	Status       string
	ErrorMessage string
	LatencyMs    int64
	OccurredAt   time.Time
}

func RecordUsageFromResult(ctx context.Context, usageRepo *repository.UsageRepo, capture UsageCapture, agent models.LLMConfig, result llmcontracts.AgentResult) {
	if usageRepo == nil {
		return
	}
	if !shouldPersistUsageForProvider(agent) {
		return
	}
	usage := result.Usage
	if usage.TotalTokens == 0 && usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CachedInputTokens == 0 && usage.ReasoningTokens == 0 && len(usage.ProviderRaw) == 0 {
		return
	}
	raw, err := json.Marshal(usageRawJSON(usage))
	if err != nil {
		raw = []byte("{}")
	}
	latency := capture.LatencyMs
	event := &models.LLMUsageEvent{
		Provider:                 string(agent.Provider),
		AccountID:                agent.OAuthAccountID,
		ProjectID:                capture.ProjectID,
		TaskID:                   capture.TaskID,
		ExecutionID:              capture.ExecutionID,
		ChatThreadID:             capture.ChatThreadID,
		TurnID:                   capture.TurnID,
		AgentConfigID:            agent.ID,
		Model:                    agent.Model,
		Operation:                capture.Operation,
		Status:                   capture.Status,
		ErrorMessage:             capture.ErrorMessage,
		InputTokens:              usage.InputTokens,
		OutputTokens:             usage.OutputTokens,
		CachedInputTokens:        usage.CachedInputTokens,
		CacheCreationInputTokens: usage.ProviderRaw["cache_creation_input_tokens"],
		CacheReadInputTokens:     usage.ProviderRaw["cache_read_input_tokens"],
		ReasoningOutputTokens:    usage.ReasoningTokens,
		TotalTokens:              usage.TotalTokens,
		LatencyMs:                &latency,
		ContextWindow:            usageOptionalInt(usage.ProviderRaw, "context_window"),
		MaxOutputTokens:          usageOptionalInt(usage.ProviderRaw, "max_output_tokens"),
		ProviderResponseID:       usage.ProviderIDs["response_id"],
		RawUsageJSON:             string(raw),
		OccurredAt:               capture.OccurredAt,
	}
	if event.Operation == "" {
		event.Operation = "unknown"
	}
	if event.Status == "" {
		event.Status = "completed"
	}
	if err := usageRepo.RecordUsageEvent(ctx, event); err != nil {
		applog.Infof("[usage] error recording usage event provider=%s model=%s exec=%s: %v", agent.Provider, agent.Model, capture.ExecutionID, err)
	}
}

func shouldPersistUsageForProvider(agent models.LLMConfig) bool {
	switch agent.Provider {
	case models.ProviderAnthropic, models.ProviderOpenAI:
		return agent.AuthMethod == models.AuthMethodOAuth || agent.AuthMethod == models.AuthMethodAPIKey || agent.APIKey != ""
	case models.ProviderOpenAICompatible:
		return agent.AuthMethod == models.AuthMethodAPIKey || agent.APIKey != ""
	default:
		return false
	}
}

func usageRawJSON(usage llmcontracts.Usage) map[string]any {
	raw := map[string]any{
		"input_tokens":            usage.InputTokens,
		"output_tokens":           usage.OutputTokens,
		"total_tokens":            usage.TotalTokens,
		"cached_input_tokens":     usage.CachedInputTokens,
		"reasoning_output_tokens": usage.ReasoningTokens,
	}
	for k, v := range usage.ProviderRaw {
		raw[k] = v
	}
	for k, v := range usage.ProviderIDs {
		raw[k] = v
	}
	return raw
}

func usageOptionalInt(raw map[string]int, key string) *int {
	if raw == nil {
		return nil
	}
	v, ok := raw[key]
	if !ok || v == 0 {
		return nil
	}
	return &v
}
