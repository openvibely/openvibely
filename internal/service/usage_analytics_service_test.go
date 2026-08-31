package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoauth "github.com/openvibely/openvibely/internal/llm/oauth"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func TestNormalizeUsageFilter_RangeSemantics(t *testing.T) {
	oldLocal := time.Local
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatalf("load local timezone: %v", err)
	}
	t.Cleanup(func() { time.Local = oldLocal })
	time.Local = loc

	now := time.Date(2024, 11, 3, 12, 0, 0, 0, loc)
	tests := []struct {
		name       string
		rangeValue string
		canonical  string
		days       int
		month      bool
		all        bool
	}{
		{name: "missing", canonical: "30d", days: 30},
		{name: "seven days", rangeValue: "7d", canonical: "7d", days: 7},
		{name: "thirty days", rangeValue: "30d", canonical: "30d", days: 30},
		{name: "ninety days", rangeValue: "90d", canonical: "90d", days: 90},
		{name: "year", rangeValue: "365d", canonical: "365d", days: 365},
		{name: "trimmed range", rangeValue: " 7d ", canonical: "7d", days: 7},
		{name: "month", rangeValue: "month", canonical: "month", month: true},
		{name: "all", rangeValue: "all", canonical: "all", all: true},
		{name: "unknown", rangeValue: "quarter", canonical: "30d", days: 30},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			filter, canonical := normalizeUsageFilterAt(UsageFilterInput{
				ProjectID: " project-id ",
				Provider:  " openai ",
				Range:     tt.rangeValue,
				Refresh:   true,
			}, now)
			if canonical != tt.canonical {
				t.Fatalf("canonical range = %q, want %q", canonical, tt.canonical)
			}
			if filter.ProjectID != "project-id" || filter.Provider != "openai" {
				t.Fatalf("filter scope = project=%q provider=%q", filter.ProjectID, filter.Provider)
			}
			if filter.GroupBy != "day" {
				t.Fatalf("GroupBy = %q, want day", filter.GroupBy)
			}
			if !filter.Refresh {
				t.Fatal("Refresh = false, want true")
			}
			if tt.all {
				if !filter.DateFrom.IsZero() || !filter.DateTo.IsZero() {
					t.Fatalf("all range has date bounds: from=%v to=%v", filter.DateFrom, filter.DateTo)
				}
				return
			}
			if filter.DateTo.IsZero() || !filter.DateTo.Equal(now) {
				t.Fatalf("DateTo = %v, want %v", filter.DateTo, now)
			}
			var wantFrom time.Time
			switch {
			case tt.month:
				wantFrom = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
			default:
				wantFrom = now.AddDate(0, 0, -tt.days)
			}
			if !filter.DateFrom.Equal(wantFrom) {
				t.Fatalf("DateFrom = %v, want %v", filter.DateFrom, wantFrom)
			}
		})
	}
}

func TestNormalizeUsageFilter_ExplicitBounds(t *testing.T) {
	filter, canonical := normalizeUsageFilterAt(UsageFilterInput{
		ProjectID: " project-id ",
		Provider:  " anthropic ",
		GroupBy:   " hour ",
		Range:     " 90d ",
		DateFrom:  " 2024-03-10T01:30:00-08:00 ",
		DateTo:    " 2024-03-11 ",
		Refresh:   true,
	}, time.Date(2024, 11, 3, 12, 0, 0, 0, time.UTC))

	if canonical != "90d" {
		t.Fatalf("canonical range = %q, want 90d", canonical)
	}
	if filter.ProjectID != "project-id" || filter.Provider != "anthropic" || filter.GroupBy != "hour" || !filter.Refresh {
		t.Fatalf("normalized filter = %+v", filter)
	}
	wantFrom := time.Date(2024, 3, 10, 9, 30, 0, 0, time.UTC)
	wantTo := time.Date(2024, 3, 11, 0, 0, 0, 0, time.UTC)
	if !filter.DateFrom.Equal(wantFrom) || !filter.DateTo.Equal(wantTo) {
		t.Fatalf("explicit bounds = %v to %v, want %v to %v", filter.DateFrom, filter.DateTo, wantFrom, wantTo)
	}
}

func TestUsageAnalyticsActionFilterFromInput_UsesNormalizedSummary(t *testing.T) {
	filter, summary := usageAnalyticsActionFilterFromInput(" project-id ", usageAnalyticsActionInput{
		Range:    " quarter ",
		Provider: " openai ",
		GroupBy:  " ",
	})

	if filter.ProjectID != "project-id" || filter.Provider != "openai" || filter.GroupBy != "day" {
		t.Fatalf("normalized action filter = %+v", filter)
	}
	if filter.Refresh {
		t.Fatal("Chat usage filter unexpectedly enables provider refresh")
	}
	if summary.Range != "30d" || summary.Provider != "openai" || summary.GroupBy != "day" {
		t.Fatalf("action filter summary = %+v", summary)
	}
	if summary.DateFrom == "" || summary.DateTo == "" {
		t.Fatalf("action filter summary omitted fallback bounds: %+v", summary)
	}
}
func TestRecordUsageFromResult_PersistsOpenAICompatibleUsageAndAggregates(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewUsageRepo(db)
	ctx := context.Background()

	configRepo := repository.NewLLMConfigRepo(db)
	compatible := models.LLMConfig{Name: "OpenRouter", Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, Model: "deepseek/deepseek-chat-v3.1:free", APIKey: "key"}
	if err := configRepo.Create(ctx, &compatible); err != nil {
		t.Fatalf("create compatible config: %v", err)
	}

	RecordUsageFromResult(ctx, repo, UsageCapture{Operation: "chat", Status: "completed", LatencyMs: 150, OccurredAt: time.Now().UTC()}, compatible, llmcontracts.AgentResult{Usage: llmusage.FromOpenAI(120, 45, 20, 8)})

	var provider, model string
	var inputTokens, outputTokens, cachedTokens, reasoningTokens, totalTokens int
	if err := db.QueryRowContext(ctx, `SELECT provider, model, input_tokens, output_tokens, cached_input_tokens, reasoning_output_tokens, total_tokens FROM llm_usage_events`).Scan(&provider, &model, &inputTokens, &outputTokens, &cachedTokens, &reasoningTokens, &totalTokens); err != nil {
		t.Fatalf("scan compatible usage event: %v", err)
	}
	if provider != string(models.ProviderOpenAICompatible) || model != "deepseek/deepseek-chat-v3.1:free" {
		t.Fatalf("expected exact compatible provider/model, got provider=%q model=%q", provider, model)
	}
	if inputTokens != 120 || outputTokens != 45 || cachedTokens != 20 || reasoningTokens != 8 || totalTokens != 165 {
		t.Fatalf("unexpected compatible usage tokens input=%d output=%d cached=%d reasoning=%d total=%d", inputTokens, outputTokens, cachedTokens, reasoningTokens, totalTokens)
	}

	svc := NewUsageAnalyticsService(repo, configRepo)
	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: string(models.ProviderOpenAICompatible)})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if view.Totals.TotalTokens != 165 || view.Totals.CachedInputTokens != 20 || view.Totals.ReasoningOutputTokens != 8 || view.Totals.CallCount != 1 {
		t.Fatalf("unexpected compatible usage totals: %+v", view.Totals)
	}
	if len(view.ModelBreakdown) != 1 {
		t.Fatalf("expected one compatible model breakdown row, got %+v", view.ModelBreakdown)
	}
	breakdown := view.ModelBreakdown[0]
	if breakdown.Provider != string(models.ProviderOpenAICompatible) || breakdown.Model != "deepseek/deepseek-chat-v3.1:free" {
		t.Fatalf("expected exact compatible breakdown provider/model, got %+v", breakdown)
	}
	if breakdown.TotalTokens != 165 || breakdown.InputTokens != 120 || breakdown.OutputTokens != 45 || breakdown.CacheTokens != 20 || breakdown.ReasoningOutputTokens != 8 || breakdown.CallCount != 1 {
		t.Fatalf("unexpected compatible model breakdown tokens: %+v", breakdown)
	}
}

func TestRecordUsageFromResult_PersistsAnthropicAndOpenAIDetails(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewUsageRepo(db)
	ctx := context.Background()

	configRepo := repository.NewLLMConfigRepo(db)
	anthropic := models.LLMConfig{Name: "Anthropic", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, Model: "claude-sonnet-4-20250514", OAuthAccessToken: "token", OAuthAccountID: "anthropic-account"}
	if err := configRepo.Create(ctx, &anthropic); err != nil {
		t.Fatalf("create anthropic config: %v", err)
	}
	openai := models.LLMConfig{Name: "OpenAI", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5.3-codex", APIKey: "key"}
	if err := configRepo.Create(ctx, &openai); err != nil {
		t.Fatalf("create openai config: %v", err)
	}

	RecordUsageFromResult(ctx, repo, UsageCapture{Operation: "task", Status: "completed", LatencyMs: 100, OccurredAt: time.Now().UTC()}, anthropic, llmcontracts.AgentResult{Usage: llmusage.FromAnthropic(50, 25, 11, 7)})
	RecordUsageFromResult(ctx, repo, UsageCapture{Operation: "streaming", Status: "completed", LatencyMs: 200, OccurredAt: time.Now().UTC()}, openai, llmcontracts.AgentResult{Usage: llmusage.FromOpenAI(80, 20, 30, 4)})
	RecordUsageFromResult(ctx, repo, UsageCapture{ExecutionID: "exec-cli", Operation: "task", Status: "completed", LatencyMs: 1, OccurredAt: time.Now().UTC()}, models.LLMConfig{Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude"}, llmcontracts.AgentResult{Usage: llmusage.FromTotal(999)})

	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_usage_events`).Scan(&count); err != nil {
		t.Fatalf("count usage events: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two persisted API/OAuth events, got %d", count)
	}

	var cacheCreate, cacheRead int
	if err := db.QueryRowContext(ctx, `SELECT cache_creation_input_tokens, cache_read_input_tokens FROM llm_usage_events WHERE provider = 'anthropic'`).Scan(&cacheCreate, &cacheRead); err != nil {
		t.Fatalf("scan anthropic cache usage: %v", err)
	}
	if cacheCreate != 11 || cacheRead != 7 {
		t.Fatalf("unexpected anthropic cache usage create=%d read=%d", cacheCreate, cacheRead)
	}

	var cached, reasoning int
	if err := db.QueryRowContext(ctx, `SELECT cached_input_tokens, reasoning_output_tokens FROM llm_usage_events WHERE provider = 'openai'`).Scan(&cached, &reasoning); err != nil {
		t.Fatalf("scan openai usage: %v", err)
	}
	if cached != 30 || reasoning != 4 {
		t.Fatalf("unexpected openai usage cached=%d reasoning=%d", cached, reasoning)
	}
}

func TestLLMService_CallAgentDirectRecordsAPIKeyUsageWithoutExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	configRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	usageRepo := repository.NewUsageRepo(db)
	svc := NewLLMService(configRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetUsageRepo(usageRepo)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAI: staticProviderAdapter{result: llmcontracts.AgentResult{Output: "ok", Usage: llmusage.FromOpenAI(12, 8, 4, 2)}},
	}
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, APIKey: "key", Model: "gpt-test"}
	if _, _, err := svc.CallAgentDirect(ctx, "hello", nil, agent, ""); err != nil {
		t.Fatalf("CallAgentDirect: %v", err)
	}
	totals, err := usageRepo.GetUsageTotals(ctx, repository.UsageFilter{Provider: "openai"})
	if err != nil {
		t.Fatalf("GetUsageTotals: %v", err)
	}
	if totals.TotalTokens != 20 || totals.CachedInputTokens != 4 || totals.ReasoningOutputTokens != 2 {
		t.Fatalf("unexpected direct-call totals: %+v", totals)
	}
}

func TestLLMService_CallAgentDirectRecordsOpenAICompatibleUsage(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	configRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	usageRepo := repository.NewUsageRepo(db)
	svc := NewLLMService(configRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetUsageRepo(usageRepo)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAICompatible: staticProviderAdapter{result: llmcontracts.AgentResult{Output: "ok", Usage: llmusage.FromOpenAI(30, 12, 6, 3)}},
	}
	agent := models.LLMConfig{Provider: models.ProviderOpenAICompatible, AuthMethod: models.AuthMethodAPIKey, APIKey: "key", Model: "provider/model:exact"}
	if _, _, err := svc.CallAgentDirect(ctx, "hello", nil, agent, ""); err != nil {
		t.Fatalf("CallAgentDirect: %v", err)
	}

	view, err := NewUsageAnalyticsService(usageRepo, configRepo).BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: string(models.ProviderOpenAICompatible)})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if view.Totals.TotalTokens != 42 || view.Totals.CachedInputTokens != 6 || view.Totals.ReasoningOutputTokens != 3 || view.Totals.CallCount != 1 {
		t.Fatalf("unexpected compatible direct-call totals: %+v", view.Totals)
	}
	if len(view.ModelBreakdown) != 1 || view.ModelBreakdown[0].Provider != string(models.ProviderOpenAICompatible) || view.ModelBreakdown[0].Model != "provider/model:exact" {
		t.Fatalf("expected compatible direct-call model breakdown with exact model id, got %+v", view.ModelBreakdown)
	}
}

func TestLLMService_CallAgentDirectUsageInfersProjectFromWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	configRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	usageRepo := repository.NewUsageRepo(db)
	svc := NewLLMService(configRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetUsageRepo(usageRepo)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAI: staticProviderAdapter{result: llmcontracts.AgentResult{Output: "ok", Usage: llmusage.FromOpenAI(12, 8, 4, 2)}},
	}

	project := &models.Project{Name: "Usage Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, APIKey: "key", Model: "gpt-test"}
	if _, _, err := svc.CallAgentDirect(ctx, "hello", nil, agent, project.RepoPath); err != nil {
		t.Fatalf("CallAgentDirect: %v", err)
	}

	totals, err := usageRepo.GetUsageTotals(ctx, repository.UsageFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("GetUsageTotals: %v", err)
	}
	if totals.TotalTokens != 20 {
		t.Fatalf("expected project-scoped direct usage totals, got %+v", totals)
	}
}

type staticProviderAdapter struct {
	result llmcontracts.AgentResult
	err    error
}

func (a staticProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	return a.result, a.err
}

func TestUsageAnalyticsService_BuildAnalyticsUsageIncludesSnapshotsAndLocalUsage(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	config := &models.LLMConfig{Name: "OpenAI OAuth", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token", OAuthAccountID: "acct-openai"}
	if err := configRepo.Create(ctx, config); err != nil {
		t.Fatalf("create oauth config: %v", err)
	}
	pct := 42.0
	window := 300
	reset := "2026-06-04T05:59:00Z"
	if err := usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{Provider: "openai", AccountID: "acct-openai", AgentConfigID: config.ID, PlanType: "pro", PrimaryLabel: "5-hour session", PrimaryUsedPercent: &pct, PrimaryWindowMinutes: &window, PrimaryResetsAt: &reset}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if err := usageRepo.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "openai", Model: "gpt-5.3-codex", Operation: "task", InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatalf("usage event: %v", err)
	}

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if view.Totals.TotalTokens != 15 || len(view.ModelBreakdown) != 1 {
		t.Fatalf("unexpected local usage view: %+v", view)
	}
	if len(view.DailyUsage) != 1 || len(view.DailyUsageByModel) != 1 || len(view.UsageRate) != 1 || len(view.UsageRateByModel) != 1 {
		t.Fatalf("expected combined and per-model usage series, got daily=%+v dailyByModel=%+v rate=%+v rateByModel=%+v", view.DailyUsage, view.DailyUsageByModel, view.UsageRate, view.UsageRateByModel)
	}
	if len(view.AccountLimits) != 1 || len(view.AccountLimits[0].Limits) != 1 || view.AccountLimits[0].Limits[0].UsedPercent == nil || *view.AccountLimits[0].Limits[0].UsedPercent != pct {
		t.Fatalf("unexpected account limits: %+v", view.AccountLimits)
	}
}

func TestUsageAnalyticsService_APIKeyUsageDoesNotCreateAccountLimitCards(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	apiKeyConfig := &models.LLMConfig{Name: "OpenAI API", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodAPIKey, APIKey: "sk-test"}
	if err := configRepo.Create(ctx, apiKeyConfig); err != nil {
		t.Fatalf("create api key config: %v", err)
	}
	if err := usageRepo.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "openai", AgentConfigID: apiKeyConfig.ID, Model: "gpt-5.3-codex", Operation: "task", InputTokens: 100, OutputTokens: 25, TotalTokens: 125}); err != nil {
		t.Fatalf("usage event: %v", err)
	}
	pct := 75.0
	if err := usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{Provider: "openai", AgentConfigID: apiKeyConfig.ID, PlanType: "ChatGPT Pro", PrimaryLabel: "5-hour session", PrimaryUsedPercent: &pct}); err != nil {
		t.Fatalf("stray api-key snapshot: %v", err)
	}

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if view.Totals.TotalTokens != 125 || len(view.ModelBreakdown) != 1 {
		t.Fatalf("expected API-key local usage to remain visible, got %+v", view)
	}
	if len(view.AccountLimits) != 0 {
		t.Fatalf("expected no fake account-limit cards for API-key configs, got %+v", view.AccountLimits)
	}
}

func TestUsageAnalyticsService_RefreshFetchesAndStoresOAuthAccountUsage(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	openai := &models.LLMConfig{Name: "OpenAI OAuth", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token", OAuthAccountID: "acct-openai"}
	if err := configRepo.Create(ctx, openai); err != nil {
		t.Fatalf("create openai oauth config: %v", err)
	}
	anthropic := &models.LLMConfig{Name: "Anthropic OAuth", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token", OAuthAccountID: "acct-anthropic"}
	if err := configRepo.Create(ctx, anthropic); err != nil {
		t.Fatalf("create anthropic oauth config: %v", err)
	}
	apiKey := &models.LLMConfig{Name: "OpenAI API", Provider: models.ProviderOpenAI, Model: "gpt-test", AuthMethod: models.AuthMethodAPIKey, APIKey: "key"}
	if err := configRepo.Create(ctx, apiKey); err != nil {
		t.Fatalf("create api key config: %v", err)
	}

	calls := 0
	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		calls++
		pct := 42.0
		window := 300
		reset := "2026-06-04T05:59:00Z"
		return &models.AccountUsageSnapshot{
			Provider:             string(cfg.Provider),
			AccountID:            cfg.OAuthAccountID,
			AgentConfigID:        cfg.ID,
			PlanType:             "pro",
			PrimaryLabel:         "5-hour session",
			PrimaryUsedPercent:   &pct,
			PrimaryWindowMinutes: &window,
			PrimaryResetsAt:      &reset,
			RawJSON:              `{"ok":true}`,
		}, nil
	})

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected two OAuth fetches, got %d", calls)
	}
	if len(view.AccountLimits) != 2 {
		t.Fatalf("expected two account limit views, got %+v", view.AccountLimits)
	}
	for _, account := range view.AccountLimits {
		if len(account.Limits) != 1 || account.Limits[0].UsedPercent == nil || *account.Limits[0].UsedPercent != 42 {
			t.Fatalf("unexpected refreshed account limit: %+v", account)
		}
	}
	var snapshotCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	if snapshotCount != 2 {
		t.Fatalf("expected two persisted snapshots, got %d", snapshotCount)
	}
}

func TestUsageAnalyticsService_DedupesAccountLimitsByOAuthAccount(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	sonnet := &models.LLMConfig{Name: "Anthropic Sonnet", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "same-access", OAuthRefreshToken: "same-refresh", OAuthAccountID: "organization:org-same", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, sonnet); err != nil {
		t.Fatalf("create sonnet config: %v", err)
	}
	opus := &models.LLMConfig{Name: "Anthropic Opus", Provider: models.ProviderAnthropic, Model: "claude-opus", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "same-access", OAuthRefreshToken: "same-refresh", OAuthAccountID: "organization:org-same", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, opus); err != nil {
		t.Fatalf("create opus config: %v", err)
	}

	calls := 0
	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		calls++
		pct := 1.0
		return &models.AccountUsageSnapshot{
			Provider:      string(cfg.Provider),
			AccountID:     cfg.OAuthAccountID,
			AgentConfigID: cfg.ID, SecondaryLabel: "weekly limit",
			SecondaryUsedPercent: &pct,
			RawJSON:              `{"seven_day":{"utilization":1}}`,
		}, nil
	})

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one provider fetch for duplicate Anthropic configs, got %d", calls)
	}
	if len(view.AccountLimits) != 1 {
		t.Fatalf("expected one account-limit card for duplicate Anthropic configs, got %+v", view.AccountLimits)
	}
	if view.AccountLimits[0].AccountID != "organization:org-same" {
		t.Fatalf("expected real Anthropic account id to remain visible: %+v", view.AccountLimits[0])
	}
	if len(view.AccountLimits[0].Limits) != 1 || view.AccountLimits[0].Limits[0].UsedPercent == nil || *view.AccountLimits[0].Limits[0].UsedPercent != 1 {
		t.Fatalf("unexpected deduped account limits: %+v", view.AccountLimits)
	}

	var snapshotCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_snapshots`).Scan(&snapshotCount); err != nil {
		t.Fatalf("count account snapshots: %v", err)
	}
	if snapshotCount != 1 {
		t.Fatalf("expected one persisted snapshot for duplicate Anthropic configs, got %d", snapshotCount)
	}
}

func TestUsageAnalyticsService_DedupesAnthropicAccountLimitsByOAuthProfile(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	sonnet := &models.LLMConfig{Name: "Anthropic Sonnet", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-one", OAuthRefreshToken: "refresh-one", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, sonnet); err != nil {
		t.Fatalf("create sonnet config: %v", err)
	}
	opus := &models.LLMConfig{Name: "Anthropic Opus", Provider: models.ProviderAnthropic, Model: "claude-opus", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-two", OAuthRefreshToken: "refresh-two", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, opus); err != nil {
		t.Fatalf("create opus config: %v", err)
	}

	oldHost := anthropicclient.AnthropicAPIHost
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" || r.Header.Get("anthropic-beta") != anthropicclient.OAuthBetaHeader || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("missing profile/usage OAuth headers: %v", r.Header)
		}
		switch r.URL.Path {
		case "/api/oauth/profile":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"account":{"uuid":"acct-user","display_name":"James","email":"james@example.com","has_claude_max":true,"has_claude_pro":false},"organization":{"uuid":"org-same","organization_type":"claude_max","billing_type":"stripe_subscription","rate_limit_tier":"default_claude_max_20x","has_extra_usage_enabled":true,"subscription_status":"active"},"application":{"slug":"claude-code"}}`))
		case "/api/oauth/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"seven_day":{"utilization":1,"resets_at":"2026-06-04T09:00:00Z"},"extra_usage":{"is_enabled":true,"monthly_limit":20000,"used_credits":0,"currency":"USD"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = oldHost }()

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: "anthropic", Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if len(view.AccountLimits) != 1 {
		t.Fatalf("expected one Anthropic account-limit card for shared profile identity, got %+v", view.AccountLimits)
	}
	if view.AccountLimits[0].AccountID != "organization:org-same" {
		t.Fatalf("expected profile-derived account id, got %+v", view.AccountLimits[0])
	}
	if view.AccountLimits[0].PlanType != "Claude Max (20x)" || view.AccountLimits[0].ExtraUsageLabel != "Usage credits enabled" {
		t.Fatalf("unexpected Anthropic profile metadata: %+v", view.AccountLimits[0])
	}
	if view.AccountLimits[0].StatusLabel != "" || view.AccountLimits[0].BillingLabel != "" || view.AccountLimits[0].AccountDetail != "" {
		t.Fatalf("Anthropic public card should not expose status, billing, or email/detail: %+v", view.AccountLimits[0])
	}
	if !usageFloatPtrClose(view.AccountLimits[0].ExtraUsageMonthlyUSD, 200) || !usageFloatPtrClose(view.AccountLimits[0].ExtraUsageUsedUSD, 0) {
		t.Fatalf("unexpected extra usage dollars: %+v", view.AccountLimits[0])
	}
	if len(view.AccountLimits[0].Limits) != 1 || !usageFloatPtrClose(view.AccountLimits[0].Limits[0].UsedPercent, 1) {
		t.Fatalf("unexpected account limits: %+v", view.AccountLimits)
	}
	for _, id := range []string{sonnet.ID, opus.ID} {
		cfg, err := configRepo.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("load config %s: %v", id, err)
		}
		if cfg.OAuthAccountID != "organization:org-same" {
			t.Fatalf("expected persisted Anthropic profile account id for %s, got %q", id, cfg.OAuthAccountID)
		}
	}
}

func TestUsageAnalyticsService_KeepsAnthropicConfigsSeparateWhenProfileFails(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	first := &models.LLMConfig{Name: "Anthropic One", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-one", OAuthRefreshToken: "refresh-one", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, first); err != nil {
		t.Fatalf("create first config: %v", err)
	}
	second := &models.LLMConfig{Name: "Anthropic Two", Provider: models.ProviderAnthropic, Model: "claude-opus", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-two", OAuthRefreshToken: "refresh-two", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, second); err != nil {
		t.Fatalf("create second config: %v", err)
	}

	oldHost := anthropicclient.AnthropicAPIHost
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/profile":
			http.Error(w, "unavailable", http.StatusNotFound)
		case "/api/oauth/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"seven_day":{"utilization":1,"resets_at":"2026-06-04T09:00:00Z"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = oldHost }()

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: "anthropic", Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if len(view.AccountLimits) != 2 {
		t.Fatalf("expected per-config Anthropic cards when profile identity is unavailable, got %+v", view.AccountLimits)
	}
	for _, account := range view.AccountLimits {
		if account.AccountID != "" {
			t.Fatalf("profile failure should not invent Anthropic account id: %+v", account)
		}
	}
}

func TestUsageAnalyticsService_DedupesStaleAnthropicSnapshotsByConfigOAuthAccountID(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	sonnet := &models.LLMConfig{Name: "Anthropic Sonnet", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token-one", OAuthAccountID: "organization:org-same"}
	if err := configRepo.Create(ctx, sonnet); err != nil {
		t.Fatalf("create sonnet config: %v", err)
	}
	opus := &models.LLMConfig{Name: "Anthropic Opus", Provider: models.ProviderAnthropic, Model: "claude-opus", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token-two", OAuthAccountID: "organization:org-same"}
	if err := configRepo.Create(ctx, opus); err != nil {
		t.Fatalf("create opus config: %v", err)
	}

	oldPct := 100.0
	if err := usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{
		Provider:             "anthropic",
		AccountID:            sonnet.ID,
		AgentConfigID:        sonnet.ID,
		SecondaryLabel:       "weekly limit",
		SecondaryUsedPercent: &oldPct,
		FetchedAt:            time.Now().Add(-2 * time.Minute),
	}); err != nil {
		t.Fatalf("create stale sonnet snapshot: %v", err)
	}
	otherOldPct := 2.0
	if err := usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{
		Provider:             "anthropic",
		AccountID:            opus.ID,
		AgentConfigID:        opus.ID,
		SecondaryLabel:       "weekly limit",
		SecondaryUsedPercent: &otherOldPct,
		FetchedAt:            time.Now().Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("create stale opus snapshot: %v", err)
	}
	currentPct := 1.0
	if err := usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{
		Provider:             "anthropic",
		AccountID:            "organization:org-same",
		AgentConfigID:        sonnet.ID,
		PlanType:             "Claude Max (20x)",
		BillingLabel:         "Subscription billing",
		SubscriptionStatus:   "Active",
		ExtraUsageLabel:      "Usage credits enabled",
		SecondaryLabel:       "weekly limit",
		SecondaryUsedPercent: &currentPct,
		FetchedAt:            time.Now(),
	}); err != nil {
		t.Fatalf("create current profile snapshot: %v", err)
	}

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: "anthropic"})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if len(view.AccountLimits) != 1 {
		t.Fatalf("expected stale per-config snapshots to collapse under config OAuth account id, got %+v", view.AccountLimits)
	}
	account := view.AccountLimits[0]
	if account.PlanType != "Claude Max (20x)" || account.ExtraUsageLabel != "Usage credits enabled" {
		t.Fatalf("expected public Anthropic profile metadata to survive stale snapshot merge, got %+v", account)
	}
	if account.StatusLabel != "" || account.BillingLabel != "" || account.AccountDetail != "" {
		t.Fatalf("Anthropic public card should not expose status, billing, or email/detail from stale rows: %+v", account)
	}
	if account.SecondaryLimit == nil || account.SecondaryLimit.UsedPercent == nil || *account.SecondaryLimit.UsedPercent != currentPct {
		t.Fatalf("expected current weekly limit to win, got %+v", account)
	}
}

func TestUsageAnalyticsService_SyncsRefreshedOAuthTokensAcrossDuplicateAccountConfigs(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	sonnet := &models.LLMConfig{Name: "Anthropic Sonnet", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "same-access", OAuthRefreshToken: "same-refresh"}
	if err := configRepo.Create(ctx, sonnet); err != nil {
		t.Fatalf("create sonnet config: %v", err)
	}
	opus := &models.LLMConfig{Name: "Anthropic Opus", Provider: models.ProviderAnthropic, Model: "claude-opus", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "same-access", OAuthRefreshToken: "same-refresh"}
	if err := configRepo.Create(ctx, opus); err != nil {
		t.Fatalf("create opus config: %v", err)
	}

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetOAuthRefreshers(func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		return llmoauth.TokenSet{AccessToken: "fresh-access", RefreshToken: "fresh-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
	}, nil)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		fresh, err := svc.ensureFreshAccountUsageOAuth(ctx, cfg, svc.anthropicAccountUsageRefreshFunc())
		if err != nil {
			return nil, err
		}
		if fresh.OAuthAccessToken != "fresh-access" {
			t.Fatalf("fetch used stale access token %q", fresh.OAuthAccessToken)
		}
		pct := 1.0
		return &models.AccountUsageSnapshot{Provider: string(fresh.Provider), AgentConfigID: fresh.ID, SecondaryLabel: "weekly limit", SecondaryUsedPercent: &pct, RawJSON: `{"seven_day":{"utilization":1}}`}, nil
	})

	if _, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true}); err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	loaded, err := configRepo.GetByID(ctx, opus.ID)
	if err != nil {
		t.Fatalf("load duplicate config: %v", err)
	}
	if loaded.OAuthAccessToken != "fresh-access" || loaded.OAuthRefreshToken != "fresh-refresh" {
		t.Fatalf("duplicate config tokens were not synced: access=%q refresh=%q", loaded.OAuthAccessToken, loaded.OAuthRefreshToken)
	}
}

func TestUsageAnalyticsService_DedupesAccountLimitsByOpenAIAccountID(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	codex := &models.LLMConfig{Name: "Codex", Provider: models.ProviderOpenAI, Model: "gpt-5.5-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token-one", OAuthAccountID: "acct-same"}
	if err := configRepo.Create(ctx, codex); err != nil {
		t.Fatalf("create codex config: %v", err)
	}
	chat := &models.LLMConfig{Name: "OpenAI Chat", Provider: models.ProviderOpenAI, Model: "gpt-5.5", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token-two", OAuthAccountID: "acct-same"}
	if err := configRepo.Create(ctx, chat); err != nil {
		t.Fatalf("create chat config: %v", err)
	}

	calls := 0
	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		calls++
		pct := 3.0
		return &models.AccountUsageSnapshot{
			Provider:           string(cfg.Provider),
			AccountID:          cfg.OAuthAccountID,
			AgentConfigID:      cfg.ID,
			PrimaryLabel:       "5-hour session",
			PrimaryUsedPercent: &pct,
			RawJSON:            `{"rate_limit":{"primary_window":{"used_percent":3}}}`,
		}, nil
	})

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one provider fetch for duplicate OpenAI account configs, got %d", calls)
	}
	if len(view.AccountLimits) != 1 {
		t.Fatalf("expected one account-limit card for duplicate OpenAI configs, got %+v", view.AccountLimits)
	}
	if view.AccountLimits[0].AccountID != "acct-same" {
		t.Fatalf("expected real OpenAI account id to remain visible, got %+v", view.AccountLimits[0])
	}
}

func TestUsageAnalyticsService_KeepsDistinctOAuthAccountsSeparate(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	first := &models.LLMConfig{Name: "Anthropic One", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-one", OAuthRefreshToken: "refresh-one", OAuthAccountID: "organization:org-one"}
	if err := configRepo.Create(ctx, first); err != nil {
		t.Fatalf("create first config: %v", err)
	}
	second := &models.LLMConfig{Name: "Anthropic Two", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "access-two", OAuthRefreshToken: "refresh-two", OAuthAccountID: "organization:org-two"}
	if err := configRepo.Create(ctx, second); err != nil {
		t.Fatalf("create second config: %v", err)
	}

	calls := 0
	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		calls++
		raw := map[string]any{"seven_day": map[string]any{"utilization": float64(calls), "resets_at": fmt.Sprintf("2026-06-0%dT09:00:00Z", calls+4)}}
		return normalizeAnthropicAccountUsage(cfg, raw)
	})

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected two provider fetches for distinct Anthropic accounts, got %d", calls)
	}
	if len(view.AccountLimits) != 2 {
		t.Fatalf("expected two account-limit cards for distinct Anthropic accounts, got %+v", view.AccountLimits)
	}
}

func TestUsageAnalyticsService_RefreshFailureKeepsLocalUsageAndBacksOff(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	config := &models.LLMConfig{Name: "OpenAI OAuth", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token", OAuthAccountID: "acct-openai"}
	if err := configRepo.Create(ctx, config); err != nil {
		t.Fatalf("create oauth config: %v", err)
	}
	if err := usageRepo.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "openai", Model: "gpt-5.3-codex", Operation: "task", InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatalf("usage event: %v", err)
	}
	pct := 24.0
	weeklyPct := 41.0
	modelPct := 7.5
	monthlyLimit := 200.0
	usedCredits := 12.5
	if err := usageRepo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{
		Provider:             "openai",
		AccountID:            "acct-openai",
		AgentConfigID:        config.ID,
		PlanType:             "Codex Pro",
		BillingLabel:         "Subscription billing",
		SubscriptionStatus:   "Active",
		ExtraUsageLabel:      "Usage credits available",
		ExtraUsageMonthlyUSD: &monthlyLimit,
		ExtraUsageUsedUSD:    &usedCredits,
		PrimaryLabel:         "5-hour session",
		PrimaryUsedPercent:   &pct,
		ExtraLimits: []models.AccountUsageExtraLimit{
			{Provider: "openai", AccountID: "acct-openai", AgentConfigID: config.ID, LimitKey: "weekly", Label: "weekly limit", UsedPercent: &weeklyPct},
			{Provider: "openai", AccountID: "acct-openai", AgentConfigID: config.ID, LimitKey: "gpt-codex", Label: "GPT Codex limit", UsedPercent: &modelPct},
		},
	}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	calls := 0
	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetAccountUsageFetcher(func(ctx context.Context, cfg models.LLMConfig) (*models.AccountUsageSnapshot, error) {
		calls++
		return nil, accountUsageHTTPError{Method: "GET", URL: "https://chatgpt.com/api/codex/usage", StatusCode: 403, ContentType: "text/html"}
	})

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage should not fail on account refresh error: %v", err)
	}
	if view.Totals.TotalTokens != 15 {
		t.Fatalf("expected local usage to render, got %+v", view.Totals)
	}
	if len(view.AccountLimits) != 1 || view.AccountLimits[0].Error == "" || len(view.AccountLimits[0].Limits) != 3 || view.AccountLimits[0].Limits[0].UsedPercent == nil || *view.AccountLimits[0].Limits[0].UsedPercent != pct {
		t.Fatalf("expected old snapshot with refresh error and all limits, got %+v", view.AccountLimits)
	}
	if view.AccountLimits[0].PlanType != "Codex Pro" || view.AccountLimits[0].BillingLabel != "Subscription billing" || view.AccountLimits[0].StatusLabel != "Active" || view.AccountLimits[0].ExtraUsageLabel != "Usage credits available" {
		t.Fatalf("expected profile/credit metadata to survive refresh failure, got %+v", view.AccountLimits[0])
	}
	if calls != 1 {
		t.Fatalf("expected first refresh to call provider once, got %d", calls)
	}

	view, err = svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage after failed refresh: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected failed refresh cooldown to suppress automatic retry, got %d calls", calls)
	}
	if len(view.AccountLimits) != 1 || view.AccountLimits[0].Error != accountRefreshFailureMessage("refresh_failed_forbidden") {
		t.Fatalf("expected persisted sanitized refresh error, got %+v", view.AccountLimits)
	}

	_, err = svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Refresh: true})
	if err != nil {
		t.Fatalf("manual refresh after failed snapshot should not fail: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected manual refresh to bypass failed-snapshot cooldown, got %d calls", calls)
	}

	snapshots, err := usageRepo.GetLatestAccountUsageSnapshots(ctx, "openai")
	if err != nil {
		t.Fatalf("read latest failure snapshot: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected one latest snapshot, got %+v", snapshots)
	}
	if snapshots[0].RateLimitReachedType != "refresh_failed_forbidden" {
		t.Fatalf("failure reason = %q, want refresh_failed_forbidden", snapshots[0].RateLimitReachedType)
	}
	if snapshots[0].PlanType != "Codex Pro" || len(snapshots[0].ExtraLimits) != 2 {
		t.Fatalf("failure snapshot did not preserve prior metadata/extra limits: %+v", snapshots[0])
	}
	if strings.Contains(snapshots[0].RawJSON, "<html>") || strings.Contains(snapshots[0].RawJSON, "chatgpt.com") {
		t.Fatalf("failure snapshot stored raw provider details: %s", snapshots[0].RawJSON)
	}
}

func TestAccountUsageHTTPErrorDoesNotExposeResponseBody(t *testing.T) {
	err := accountUsageHTTPError{Method: "GET", URL: "https://chatgpt.com/api/codex/usage", StatusCode: 403, ContentType: "text/html"}
	message := err.Error()
	if strings.Contains(message, "<html>") || strings.Contains(message, "Invalid authentication credentials") {
		t.Fatalf("sanitized account usage error exposed provider body: %s", message)
	}
	if !strings.Contains(message, "HTTP 403") {
		t.Fatalf("sanitized account usage error should include status code, got %s", message)
	}
}

func TestUsageAnalyticsService_AccountUsageRefreshesExpiredOpenAIOAuthBeforeRequest(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	jwtWithAccountID := "header.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiY2hhdGdwdF9hY2NvdW50X2lkIjoiYWNjdC1mcm9tLWp3dCJ9fQ.sig"
	config := &models.LLMConfig{Name: "OpenAI OAuth", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: jwtWithAccountID, OAuthRefreshToken: "refresh-token", OAuthExpiresAt: time.Now().Add(-time.Hour).UnixMilli()}
	if err := configRepo.Create(ctx, config); err != nil {
		t.Fatalf("create oauth config: %v", err)
	}

	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		if r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("account usage path = %q, want /backend-api/wham/usage", r.URL.Path)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Fatalf("account usage Accept = %q, want application/json", r.Header.Get("Accept"))
		}
		if r.Header.Get("ChatGPT-Account-ID") != "acct-from-jwt" {
			t.Fatalf("missing decoded account id header: %q", r.Header.Get("ChatGPT-Account-ID"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"plan_type": "pro",
			"primary":   map[string]any{"usedPercent": 12.0, "windowDurationMins": 300},
		})
	}))
	defer server.Close()
	oldBase := openaiclient.OpenAIChatGPTAPIBaseURL
	openaiclient.OpenAIChatGPTAPIBaseURL = server.URL + "/backend-api/codex/"
	defer func() { openaiclient.OpenAIChatGPTAPIBaseURL = oldBase }()

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetOAuthRefreshers(nil, func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		if cfg.OAuthAccessToken != jwtWithAccountID {
			t.Fatalf("refresh saw access token %q, want original JWT", cfg.OAuthAccessToken)
		}
		if cfg.OAuthAccountID != "acct-from-jwt" {
			t.Fatalf("refresh config account id = %q, want decoded JWT account id", cfg.OAuthAccountID)
		}
		return llmoauth.TokenSet{AccessToken: "fresh-token", RefreshToken: "fresh-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(), AccountID: cfg.OAuthAccountID}, nil
	})

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: "openai", Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if authHeader != "Bearer fresh-token" {
		t.Fatalf("account usage request Authorization = %q, want fresh token", authHeader)
	}
	loaded, err := configRepo.GetByID(ctx, config.ID)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.OAuthAccessToken != "fresh-token" || loaded.OAuthRefreshToken != "fresh-refresh" || loaded.OAuthAccountID != "acct-from-jwt" {
		t.Fatalf("refreshed tokens/account id were not persisted: access=%q refresh=%q account=%q", loaded.OAuthAccessToken, loaded.OAuthRefreshToken, loaded.OAuthAccountID)
	}
	if len(view.AccountLimits) != 1 || len(view.AccountLimits[0].Limits) != 1 || view.AccountLimits[0].Error != "" {
		t.Fatalf("expected refreshed account limits without error, got %+v", view.AccountLimits)
	}
}

func TestUsageAnalyticsService_AccountUsageRecoversUnauthorizedAnthropicWithFreshToken(t *testing.T) {
	db := testutil.NewTestDB(t)
	usageRepo := repository.NewUsageRepo(db)
	configRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	config := &models.LLMConfig{Name: "Anthropic OAuth", Provider: models.ProviderAnthropic, Model: "claude-sonnet", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "stale-token", OAuthRefreshToken: "refresh-token", OAuthExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli(), OAuthAccountID: "acct-anthropic"}
	if err := configRepo.Create(ctx, config); err != nil {
		t.Fatalf("create oauth config: %v", err)
	}

	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta := r.Header.Get("anthropic-beta")
		if beta != anthropicclient.OAuthBetaHeader || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("missing anthropic account usage headers: beta=%q content-type=%q", beta, r.Header.Get("Content-Type"))
		}
		if r.Header.Get("anthropic-version") != "" || r.Header.Get("x-app") != "" || strings.Contains(beta, "claude-code-20250219") {
			t.Fatalf("account usage GET included inference-only headers: version=%q x-app=%q beta=%q", r.Header.Get("anthropic-version"), r.Header.Get("x-app"), beta)
		}
		switch r.URL.Path {
		case "/api/oauth/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{"organization": map[string]any{"uuid": "org-anthropic"}})
			return
		case "/api/oauth/usage":
			authHeaders = append(authHeaders, r.Header.Get("Authorization"))
			if len(authHeaders) == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"expired"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"fiveHour": map[string]any{"utilization": 0.25, "resetsAt": "2026-06-04T05:59:00Z"},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	oldHost := anthropicclient.AnthropicAPIHost
	anthropicclient.AnthropicAPIHost = server.URL
	defer func() { anthropicclient.AnthropicAPIHost = oldHost }()

	svc := NewUsageAnalyticsService(usageRepo, configRepo)
	svc.SetOAuthRefreshers(func(ctx context.Context, cfg models.LLMConfig) (llmoauth.TokenSet, error) {
		if cfg.OAuthAccessToken != "stale-token" {
			t.Fatalf("refresh saw access token %q, want stale-token", cfg.OAuthAccessToken)
		}
		return llmoauth.TokenSet{AccessToken: "fresh-token", RefreshToken: "fresh-refresh", ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}, nil
	}, nil)

	view, err := svc.BuildAnalyticsUsage(ctx, repository.UsageFilter{Provider: "anthropic", Refresh: true})
	if err != nil {
		t.Fatalf("BuildAnalyticsUsage: %v", err)
	}
	if len(authHeaders) != 2 {
		t.Fatalf("expected one unauthorized request and one recovery retry, got %d", len(authHeaders))
	}
	if authHeaders[0] != "Bearer stale-token" || authHeaders[1] != "Bearer fresh-token" {
		t.Fatalf("unexpected auth headers: %+v", authHeaders)
	}
	loaded, err := configRepo.GetByID(ctx, config.ID)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.OAuthAccessToken != "fresh-token" || loaded.OAuthRefreshToken != "fresh-refresh" {
		t.Fatalf("recovered tokens were not persisted: access=%q refresh=%q", loaded.OAuthAccessToken, loaded.OAuthRefreshToken)
	}
	if len(view.AccountLimits) != 1 || len(view.AccountLimits[0].Limits) != 1 || view.AccountLimits[0].Error != "" {
		t.Fatalf("expected recovered account limits without error, got %+v", view.AccountLimits)
	}
}

func usageFloatPtrClose(value *float64, want float64) bool {
	return value != nil && math.Abs(*value-want) < 0.000001
}

func TestNormalizeAnthropicOAuthProfileSubscriptionMetadata(t *testing.T) {
	profile := normalizeAnthropicOAuthProfile(map[string]any{
		"account": map[string]any{
			"uuid":           "acct-1",
			"display_name":   "Claude User",
			"email":          "user@example.com",
			"has_claude_max": false,
			"has_claude_pro": true,
		},
		"organization": map[string]any{
			"uuid":                    "org-1",
			"organization_type":       "claude_team",
			"billing_type":            "stripe_subscription_contracted",
			"rate_limit_tier":         "default_claude_max_5x",
			"has_extra_usage_enabled": true,
			"subscription_status":     "pending",
		},
	})
	if profile.AccountID != "organization:org-1" {
		t.Fatalf("account id = %q", profile.AccountID)
	}
	if profile.PlanLabel != "Claude Max (5x)" || profile.BillingLabel != "Contract subscription" || profile.StatusLabel != "Pending" || profile.ExtraUsageLabel != "Usage credits enabled" {
		t.Fatalf("unexpected metadata: %+v", profile)
	}
	if profile.Detail != "user@example.com" {
		t.Fatalf("detail = %q", profile.Detail)
	}

	fallback := normalizeAnthropicOAuthProfile(map[string]any{
		"account":      map[string]any{"uuid": "acct-2", "has_claude_pro": true},
		"organization": map[string]any{"organization_type": "enterprise_usage_based", "billing_type": "team", "subscription_status": "disabled"},
	})
	if fallback.AccountID != "account:acct-2" || fallback.PlanLabel != "Claude Pro" || fallback.BillingLabel != "Team billing" || fallback.StatusLabel != "Disabled" {
		t.Fatalf("unexpected fallback metadata: %+v", fallback)
	}
}

func TestNormalizeAnthropicAccountUsageAcceptsSnakeCaseOAuthResponse(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-anthropic", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-anthropic"}
	raw := map[string]any{
		"five_hour":        map[string]any{"utilization": 1.0, "resets_at": "2026-06-04T05:00:00Z"},
		"seven_day":        map[string]any{"utilization": 1.0, "resets_at": "2026-06-08T05:00:00Z"},
		"seven_day_opus":   map[string]any{"utilization": 2.0, "resets_at": "2026-06-08T06:00:00Z"},
		"seven_day_sonnet": map[string]any{"utilization": 1.0, "resets_at": "2026-06-08T05:00:00Z"},
		"seven_day_haiku":  map[string]any{"utilization": 4.0, "resets_at": "2026-06-08T07:00:00Z"},
		"extra_usage":      map[string]any{"tokens": 123},
	}

	snapshot, err := normalizeAnthropicAccountUsage(cfg, raw)
	if err != nil {
		t.Fatalf("normalizeAnthropicAccountUsage: %v", err)
	}
	if !usageFloatPtrClose(snapshot.PrimaryUsedPercent, 1) {
		t.Fatalf("primary used percent = %v, want 1", snapshot.PrimaryUsedPercent)
	}
	if !usageFloatPtrClose(snapshot.SecondaryUsedPercent, 1) {
		t.Fatalf("secondary used percent = %v, want 1", snapshot.SecondaryUsedPercent)
	}
	if len(snapshot.ExtraLimits) != 3 {
		t.Fatalf("extra limits count = %d, want 3: %+v", len(snapshot.ExtraLimits), snapshot.ExtraLimits)
	}
	if snapshot.ExtraLimits[0].LimitKey != "seven_day_opus" || snapshot.ExtraLimits[0].Label != "Opus weekly limit" || !usageFloatPtrClose(snapshot.ExtraLimits[0].UsedPercent, 2) {
		t.Fatalf("opus extra limit = %+v, want seven_day_opus/Opus weekly limit/2", snapshot.ExtraLimits[0])
	}
	if snapshot.ExtraLimits[1].LimitKey != "seven_day_sonnet" || snapshot.ExtraLimits[1].Label != "Sonnet weekly limit" || !usageFloatPtrClose(snapshot.ExtraLimits[1].UsedPercent, 1) {
		t.Fatalf("sonnet extra limit = %+v, want seven_day_sonnet/Sonnet weekly limit/1", snapshot.ExtraLimits[1])
	}
	if snapshot.ExtraLimits[2].LimitKey != "seven_day_haiku" || snapshot.ExtraLimits[2].Label != "seven day haiku limit" || !usageFloatPtrClose(snapshot.ExtraLimits[2].UsedPercent, 4) {
		t.Fatalf("haiku extra limit = %+v, want seven_day_haiku/seven day haiku limit/4", snapshot.ExtraLimits[2])
	}
	if snapshot.PrimaryResetsAt == nil || *snapshot.PrimaryResetsAt != "2026-06-04T05:00:00Z" {
		t.Fatalf("primary reset = %v, want snake_case resets_at value", snapshot.PrimaryResetsAt)
	}
	if !strings.Contains(snapshot.RawJSON, "seven_day_sonnet") || !strings.Contains(snapshot.RawJSON, "extra_usage") {
		t.Fatalf("raw JSON did not preserve provider snake_case payload: %s", snapshot.RawJSON)
	}
}

func TestAccountViewFromSnapshotRepairsStoredAnthropicSnakeCaseRawJSON(t *testing.T) {
	wrongHundred := 100.0
	snapshot := models.AccountUsageSnapshot{
		Provider:             string(models.ProviderAnthropic),
		AccountID:            "acct-anthropic",
		AgentConfigID:        "cfg-anthropic",
		PrimaryUsedPercent:   &wrongHundred,
		SecondaryUsedPercent: &wrongHundred,
		RawJSON:              `{"five_hour":{"utilization":1,"resets_at":"2026-06-04T05:00:00Z"},"seven_day":{"utilization":1,"resets_at":"2026-06-08T05:00:00Z"},"seven_day_opus":{"utilization":3,"resets_at":"2026-06-08T06:00:00Z"},"seven_day_sonnet":{"utilization":2,"resets_at":"2026-06-08T05:00:00Z"}}`,
		FetchedAt:            time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
	}

	view := accountViewFromSnapshot(snapshot)
	if view.Error != "" {
		t.Fatalf("unexpected error on repaired snapshot: %q", view.Error)
	}
	if len(view.Limits) != 4 || len(view.ExtraLimits) != 2 {
		t.Fatalf("limits count = %d extra=%d, want 4/2: %+v", len(view.Limits), len(view.ExtraLimits), view.Limits)
	}
	if view.Limits[0].Label != "5-hour session" || !usageFloatPtrClose(view.Limits[0].UsedPercent, 1) {
		t.Fatalf("primary limit = %+v, want 5-hour session at 1%%", view.Limits[0])
	}
	if view.Limits[1].Label != "weekly limit" || !usageFloatPtrClose(view.Limits[1].UsedPercent, 1) {
		t.Fatalf("weekly limit = %+v, want weekly limit at 1%%", view.Limits[1])
	}
	if view.Limits[2].Label != "Opus weekly limit" || !usageFloatPtrClose(view.Limits[2].UsedPercent, 3) {
		t.Fatalf("opus limit = %+v, want Opus weekly limit at 3%%", view.Limits[2])
	}
	if view.Limits[3].Label != "Sonnet weekly limit" || !usageFloatPtrClose(view.Limits[3].UsedPercent, 2) {
		t.Fatalf("sonnet limit = %+v, want Sonnet weekly limit at 2%%", view.Limits[3])
	}
}

func TestOpenAIWindowClassificationAndDurationAliases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		minutes int
		kind    string
		label   string
	}{
		{name: "five hour", minutes: 300, kind: openAIWindowFiveHour, label: "5-hour session"},
		{name: "daily", minutes: 1440, kind: openAIWindowDaily, label: "daily limit"},
		{name: "weekly", minutes: 10080, kind: openAIWindowWeekly, label: "weekly limit"},
		{name: "monthly", minutes: 43200, kind: openAIWindowMonthly, label: "monthly limit"},
		{name: "annual", minutes: 525600, kind: openAIWindowAnnual, label: "annual limit"},
		{name: "five percent tolerance", minutes: 285, kind: openAIWindowFiveHour, label: "5-hour session"},
		{name: "unknown", minutes: 600, kind: openAIWindowUnknown, label: "usage limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kind := classifyOpenAIWindow(tc.minutes)
			if kind != tc.kind {
				t.Fatalf("classifyOpenAIWindow(%d) = %q, want %q", tc.minutes, kind, tc.kind)
			}
			if label := openAIWindowDisplayLabel(kind); label != tc.label {
				t.Fatalf("label = %q, want %q", label, tc.label)
			}
		})
	}

	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	snapshot, err := normalizeOpenAIAccountUsage(cfg, map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 2.0, "window_duration_mins": 300.0},
			"secondary_window": map[string]any{"used_percent": 3.0, "limitWindowSeconds": 604801.0},
		},
	})
	if err != nil {
		t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
	}
	if snapshot.PrimaryWindowMinutes == nil || *snapshot.PrimaryWindowMinutes != 300 {
		t.Fatalf("window_duration_mins was not parsed: %+v", snapshot.PrimaryWindowMinutes)
	}
	if snapshot.SecondaryWindowMinutes == nil || *snapshot.SecondaryWindowMinutes != 10081 {
		t.Fatalf("partial seconds were not rounded upward: %+v", snapshot.SecondaryWindowMinutes)
	}
}

func TestNormalizeOpenAIAccountUsageSwappedWindowsHaveStableIdentityAndOrder(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	snapshot, err := normalizeOpenAIAccountUsage(cfg, map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 8.0, "window_minutes": 10080.0},
			"secondary_window": map[string]any{"used_percent": 4.0, "windowDurationMins": 300.0},
		},
	})
	if err != nil {
		t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
	}
	view := accountViewFromSnapshot(*snapshot)
	if len(view.Limits) != 2 {
		t.Fatalf("limits = %+v, want two", view.Limits)
	}
	if view.Limits[0].LimitKey != openAIWindowFiveHour || view.Limits[0].Label != "5-hour session" || !usageFloatPtrClose(view.Limits[0].UsedPercent, 4) {
		t.Fatalf("first limit = %+v, want stable five-hour identity", view.Limits[0])
	}
	if view.Limits[1].LimitKey != openAIWindowWeekly || view.Limits[1].Label != "weekly limit" || !usageFloatPtrClose(view.Limits[1].UsedPercent, 8) {
		t.Fatalf("second limit = %+v, want stable weekly identity", view.Limits[1])
	}

	unknown, err := normalizeOpenAIAccountUsage(cfg, map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 1.0, "window_minutes": 600.0},
			"secondary_window": map[string]any{"used_percent": 2.0, "window_minutes": 700.0},
		},
	})
	if err != nil {
		t.Fatalf("normalize unknown windows: %v", err)
	}
	unknownView := accountViewFromSnapshot(*unknown)
	if len(unknownView.Limits) != 2 || unknownView.Limits[0].LimitKey != "primary" || unknownView.Limits[1].LimitKey != "secondary" {
		t.Fatalf("unknown windows did not preserve stable provider order: %+v", unknownView.Limits)
	}
	if unknownView.Limits[0].Label != "usage limit" || unknownView.Limits[1].Label != "usage limit" {
		t.Fatalf("unknown windows should use neutral labels: %+v", unknownView.Limits)
	}
}

func TestNormalizeOpenAIAccountUsageDuplicateSemanticWindowsRenderOnce(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	snapshot, err := normalizeOpenAIAccountUsage(cfg, map[string]any{
		"rate_limit": map[string]any{
			"primary_window":   map[string]any{"used_percent": 4.0, "window_minutes": 10080.0},
			"secondary_window": map[string]any{"used_percent": 5.0, "limit_window_seconds": 604800.0},
		},
	})
	if err != nil {
		t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
	}
	view := accountViewFromSnapshot(*snapshot)
	if len(view.Limits) != 1 || view.Limits[0].LimitKey != openAIWindowWeekly || !usageFloatPtrClose(view.Limits[0].UsedPercent, 4) {
		t.Fatalf("duplicate semantic windows were not deterministically deduplicated in provider order: %+v", view.Limits)
	}
}

func TestNormalizeOpenAIAdditionalPrimaryWindowPersistsCurrentLimit(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	configRepo := repository.NewLLMConfigRepo(db)
	cfg := models.LLMConfig{Name: "OpenAI OAuth", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "test-token", OAuthAccountID: "acct-openai"}
	if err := configRepo.Create(ctx, &cfg); err != nil {
		t.Fatalf("create config: %v", err)
	}
	snapshot, err := normalizeOpenAIAccountUsage(cfg, map[string]any{
		"additional_rate_limits": []any{
			map[string]any{
				"metered_feature": "codex_bengalfox",
				"limit_name":      "GPT-5.3-Codex-Spark",
				"account_id":      "raw-provider-account-must-be-redacted",
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"used_percent":         0.0,
						"limit_window_seconds": 604800.0,
						"reset_at":             1784692110.0,
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
	}
	if len(snapshot.ExtraLimits) != 1 {
		t.Fatalf("extra limits = %+v, want one", snapshot.ExtraLimits)
	}
	limit := snapshot.ExtraLimits[0]
	if limit.LimitKey != "codex_bengalfox" || limit.Label != "GPT-5.3-Codex-Spark" || !usageFloatPtrClose(limit.UsedPercent, 0) {
		t.Fatalf("unexpected normalized extra limit: %+v", limit)
	}
	if limit.WindowMinutes == nil || *limit.WindowMinutes != 10080 || limit.ResetAt == nil || *limit.ResetAt != "2026-07-22T03:48:30Z" {
		t.Fatalf("unexpected normalized window/reset: %+v", limit)
	}
	if strings.Contains(limit.RawJSON, "raw-provider-account-must-be-redacted") {
		t.Fatalf("extra-limit raw JSON exposed provider identity: %s", limit.RawJSON)
	}

	repo := repository.NewUsageRepo(db)
	if err := repo.CreateAccountUsageSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("persist snapshot: %v", err)
	}
	stored, err := repo.GetLatestAccountUsageSnapshots(ctx, "openai")
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(stored) != 1 || len(stored[0].ExtraLimits) != 1 {
		t.Fatalf("persisted snapshots = %+v", stored)
	}
	persisted := stored[0].ExtraLimits[0]
	if persisted.LimitKey != limit.LimitKey || !usageFloatPtrClose(persisted.UsedPercent, 0) || persisted.ResetAt == nil || *persisted.ResetAt != *limit.ResetAt {
		t.Fatalf("persisted extra limit changed identity/current values: %+v", persisted)
	}
}

func TestNormalizeOpenAIAccountUsageWeeklyPrimaryWindowIsNotFiveHour(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	snapshot, err := normalizeOpenAIAccountUsage(cfg, map[string]any{
		"rate_limit": map[string]any{
			"primary_window": map[string]any{
				"used_percent":         6.0,
				"limit_window_seconds": 604800.0,
				"reset_at":             1784667607.0,
			},
		},
	})
	if err != nil {
		t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
	}
	view := accountViewFromSnapshot(*snapshot)
	if len(view.Limits) != 1 {
		t.Fatalf("limits = %+v, want one weekly-only limit", view.Limits)
	}
	if view.Limits[0].Label != "weekly limit" {
		t.Fatalf("label = %q, want weekly limit", view.Limits[0].Label)
	}
	if view.Limits[0].WindowMinutes == nil || *view.Limits[0].WindowMinutes != 10080 {
		t.Fatalf("window = %v, want 10080 minutes", view.Limits[0].WindowMinutes)
	}
	if view.Limits[0].ResetsAt == nil || *view.Limits[0].ResetsAt != "2026-07-21T21:00:07Z" {
		t.Fatalf("reset = %v, want absolute RFC3339 timestamp", view.Limits[0].ResetsAt)
	}
}

func TestPreferAccountUsageViewDoesNotMergeOlderDynamicLimits(t *testing.T) {
	currentPercent := 6.0
	stalePercent := 14.0
	currentReset := "2026-07-21T21:00:07Z"
	staleReset := "2025-01-01T00:00:00Z"

	for _, tc := range []struct {
		name     string
		provider string
		newer    models.AccountUsageView
		older    models.AccountUsageView
	}{
		{
			name:     "openai",
			provider: "openai",
			newer: models.AccountUsageView{
				Provider:     "openai",
				PlanType:     "",
				UpdatedAt:    time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
				PrimaryLimit: &models.AccountLimitView{LimitKey: "weekly", Label: "weekly limit", UsedPercent: &currentPercent, ResetsAt: &currentReset},
				Limits:       []models.AccountLimitView{{LimitKey: "weekly", Label: "weekly limit", UsedPercent: &currentPercent, ResetsAt: &currentReset}},
			},
			older: models.AccountUsageView{
				Provider:       "openai",
				PlanType:       "ChatGPT Pro",
				UpdatedAt:      time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
				SecondaryLimit: &models.AccountLimitView{LimitKey: "weekly", Label: "weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
				ExtraLimits:    []models.AccountLimitView{{LimitKey: "codex_bengalfox", Label: "GPT-5.3-Codex-Spark", UsedPercent: &stalePercent, ResetsAt: &staleReset}},
				Limits: []models.AccountLimitView{
					{LimitKey: "weekly", Label: "weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
					{LimitKey: "codex_bengalfox", Label: "GPT-5.3-Codex-Spark", UsedPercent: &stalePercent, ResetsAt: &staleReset},
				},
			},
		},
		{
			name:     "anthropic",
			provider: "anthropic",
			newer: models.AccountUsageView{
				Provider:     "anthropic",
				PlanType:     "",
				UpdatedAt:    time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC),
				PrimaryLimit: &models.AccountLimitView{LimitKey: "five_hour", Label: "5-hour session", UsedPercent: &currentPercent, ResetsAt: &currentReset},
				Limits:       []models.AccountLimitView{{LimitKey: "five_hour", Label: "5-hour session", UsedPercent: &currentPercent, ResetsAt: &currentReset}},
			},
			older: models.AccountUsageView{
				Provider:       "anthropic",
				PlanType:       "Claude Max (20x)",
				UpdatedAt:      time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
				SecondaryLimit: &models.AccountLimitView{LimitKey: "seven_day", Label: "weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
				ExtraLimits: []models.AccountLimitView{
					{LimitKey: "seven_day_opus", Label: "Opus weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
					{LimitKey: "seven_day_sonnet", Label: "Sonnet weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
				},
				Limits: []models.AccountLimitView{
					{LimitKey: "seven_day", Label: "weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
					{LimitKey: "seven_day_opus", Label: "Opus weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
					{LimitKey: "seven_day_sonnet", Label: "Sonnet weekly limit", UsedPercent: &stalePercent, ResetsAt: &staleReset},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			merged := preferAccountUsageView(tc.newer, tc.older)
			if merged.PlanType != tc.older.PlanType {
				t.Fatalf("stable plan metadata was not backfilled: %+v", merged)
			}
			if len(merged.Limits) != 1 || merged.Limits[0].Label != tc.newer.Limits[0].Label || merged.Limits[0].UsedPercent == nil || *merged.Limits[0].UsedPercent != currentPercent {
				t.Fatalf("newest dynamic limit set was not authoritative: %+v", merged.Limits)
			}
			if merged.SecondaryLimit != nil || len(merged.ExtraLimits) != 0 {
				t.Fatalf("older %s dynamic limits were resurrected: secondary=%+v extras=%+v", tc.provider, merged.SecondaryLimit, merged.ExtraLimits)
			}
		})
	}
}

func TestPreferAccountUsageViewDoesNotMergeOlderUsageCreditState(t *testing.T) {
	currentPercent := 6.0
	currentReset := "2026-07-21T21:00:07Z"
	newerUpdatedAt := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	olderUpdatedAt := newerUpdatedAt.Add(-24 * time.Hour)
	staleMonthlyUSD := 100.0
	staleUsedUSD := 42.0

	for _, tc := range []struct {
		provider string
		planType string
	}{
		{provider: "openai", planType: "ChatGPT Pro"},
		{provider: "anthropic", planType: "Claude Max (20x)"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			accountID := "acct-" + tc.provider
			newer := models.AccountUsageView{
				Provider:  tc.provider,
				AccountID: accountID,
				UpdatedAt: newerUpdatedAt,
				Limits: []models.AccountLimitView{{
					LimitKey:    "current",
					Label:       "current limit",
					UsedPercent: &currentPercent,
					ResetsAt:    &currentReset,
				}},
				Error: "current refresh error",
			}
			older := models.AccountUsageView{
				Provider:             tc.provider,
				AccountID:            accountID,
				PlanType:             tc.planType,
				ExtraUsageLabel:      "stale usage credits",
				ExtraUsageMonthlyUSD: &staleMonthlyUSD,
				ExtraUsageUsedUSD:    &staleUsedUSD,
				UpdatedAt:            olderUpdatedAt,
				Error:                "stale refresh error",
			}

			accounts := dedupeAccountUsageViews([]models.AccountUsageView{older, newer}, nil)
			if len(accounts) != 1 {
				t.Fatalf("deduped account count = %d, want 1: %+v", len(accounts), accounts)
			}
			merged := accounts[0]
			if merged.PlanType != tc.planType {
				t.Fatalf("safe plan metadata was not backfilled: %+v", merged)
			}
			if merged.ExtraUsageLabel != "" || merged.ExtraUsageMonthlyUSD != nil || merged.ExtraUsageUsedUSD != nil {
				t.Fatalf("older usage-credit state was resurrected: label=%q monthly=%v used=%v", merged.ExtraUsageLabel, merged.ExtraUsageMonthlyUSD, merged.ExtraUsageUsedUSD)
			}
			if !merged.UpdatedAt.Equal(newerUpdatedAt) || merged.Error != newer.Error {
				t.Fatalf("older dynamic timestamp/error replaced winner state: updated_at=%s error=%q", merged.UpdatedAt, merged.Error)
			}
			if len(merged.Limits) != 1 || merged.Limits[0].LimitKey != "current" {
				t.Fatalf("winner limit state changed: %+v", merged.Limits)
			}
		})
	}
}

func TestNormalizeOpenAIAccountUsagePlanLabels(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{name: "free", raw: map[string]any{"plan_type": "free"}, want: "ChatGPT Free"},
		{name: "plus", raw: map[string]any{"plan_type": "plus"}, want: "ChatGPT Plus"},
		{name: "pro", raw: map[string]any{"plan_type": "pro"}, want: "ChatGPT Pro"},
		{name: "team", raw: map[string]any{"plan_type": "team"}, want: "ChatGPT Team"},
		{name: "enterprise", raw: map[string]any{"plan_type": "enterprise"}, want: "ChatGPT Enterprise"},
		{name: "edu", raw: map[string]any{"plan_type": "edu"}, want: "ChatGPT Edu"},
		{name: "education", raw: map[string]any{"plan_type": "education"}, want: "ChatGPT Edu"},
		{name: "unknown", raw: map[string]any{"plan_type": "research_preview"}, want: "Research Preview"},
		{name: "empty", raw: map[string]any{}, want: "OpenAI subscription"},
		{name: "nested", raw: map[string]any{"rate_limits": map[string]any{"plan_type": "plus"}}, want: "ChatGPT Plus"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := normalizeOpenAIAccountUsage(cfg, tc.raw)
			if err != nil {
				t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
			}
			if snapshot.PlanType != tc.want {
				t.Fatalf("plan type = %q, want %q", snapshot.PlanType, tc.want)
			}
		})
	}
}

func TestNormalizeOpenAIAccountUsageAcceptsCodexRateLimitsShape(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	raw := map[string]any{
		"type":       "token_count",
		"email":      "person@example.com",
		"user_id":    "user-secret",
		"account_id": "acct-secret-from-provider",
		"info": map[string]any{
			"total_token_usage": map[string]any{"input_tokens": 12052, "cached_input_tokens": 9600, "output_tokens": 312, "reasoning_output_tokens": 0, "total_tokens": 12364},
			"last_token_usage":  map[string]any{"input_tokens": 12052, "cached_input_tokens": 9600, "output_tokens": 312, "reasoning_output_tokens": 0, "total_tokens": 12364, "email": "nested@example.com"},
		},
		"rate_limit": map[string]any{
			"primary_window":          map[string]any{"used_percent": 1.5, "limit_window_seconds": 18000, "reset_at": float64(1780030350)},
			"secondary_window":        map[string]any{"used_percent": 24.0, "limit_window_seconds": 604800, "reset_at": float64(1780173306)},
			"rate_limit_reached_type": "primary_window",
		},
		"credits":                  map[string]any{"has_credits": true, "balance": "123.5"},
		"plan_type":                "pro",
		"spend_control":            map[string]any{"enabled": true, "reached": false},
		"rate_limit_reset_credits": map[string]any{"available_count": 10},
		"additional_rate_limits": []any{
			map[string]any{"metered_feature": "gpt-5.3-codex-spark", "limit_name": "GPT-5.3-Codex-Spark limit", "rate_limit": map[string]any{"secondary_window": map[string]any{"used_percent": 12.5, "limit_window_seconds": 604800, "reset_at": float64(1780173306)}}},
			map[string]any{"metered_feature": "gpt-5.3-codex-pro", "limit_name": "GPT-5.3-Codex-Pro limit", "rate_limit": map[string]any{"secondary_window": map[string]any{"used_percent": 44.0, "limit_window_seconds": 604800, "reset_at": float64(1780173307)}}},
		},
	}

	snapshot, err := normalizeOpenAIAccountUsage(cfg, raw)
	if err != nil {
		t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
	}
	if snapshot.PlanType != "ChatGPT Pro" {
		t.Fatalf("plan type = %q, want ChatGPT Pro", snapshot.PlanType)
	}
	if !usageFloatPtrClose(snapshot.PrimaryUsedPercent, 1.5) {
		t.Fatalf("primary used percent = %v, want 1.5", snapshot.PrimaryUsedPercent)
	}
	if snapshot.PrimaryWindowMinutes == nil || *snapshot.PrimaryWindowMinutes != 300 {
		t.Fatalf("primary window minutes = %v, want 300", snapshot.PrimaryWindowMinutes)
	}
	if snapshot.SecondaryWindowMinutes == nil || *snapshot.SecondaryWindowMinutes != 10080 {
		t.Fatalf("secondary window minutes = %v, want 10080", snapshot.SecondaryWindowMinutes)
	}
	if !usageFloatPtrClose(snapshot.SecondaryUsedPercent, 24) {
		t.Fatalf("secondary used percent = %v, want 24", snapshot.SecondaryUsedPercent)
	}
	if !usageFloatPtrClose(snapshot.CreditsRemaining, 123.5) {
		t.Fatalf("credits remaining = %v, want 123.5", snapshot.CreditsRemaining)
	}
	if snapshot.ExtraUsageLabel != "Usage credits available" {
		t.Fatalf("extra usage label = %q, want Usage credits available", snapshot.ExtraUsageLabel)
	}
	if len(snapshot.ExtraLimits) != 2 {
		t.Fatalf("extra limits count = %d, want 2: %+v", len(snapshot.ExtraLimits), snapshot.ExtraLimits)
	}
	if snapshot.ExtraLimits[0].LimitKey != "gpt-5.3-codex-spark" || snapshot.ExtraLimits[0].Label != "GPT-5.3-Codex-Spark limit" || !usageFloatPtrClose(snapshot.ExtraLimits[0].UsedPercent, 12.5) {
		t.Fatalf("first extra limit = %+v", snapshot.ExtraLimits[0])
	}
	if snapshot.ExtraLimits[1].LimitKey != "gpt-5.3-codex-pro" || snapshot.ExtraLimits[1].Label != "GPT-5.3-Codex-Pro limit" || !usageFloatPtrClose(snapshot.ExtraLimits[1].UsedPercent, 44) {
		t.Fatalf("second extra limit = %+v", snapshot.ExtraLimits[1])
	}
	view := accountViewFromSnapshot(*snapshot)
	if len(view.ExtraLimits) != 2 || len(view.Limits) != 4 {
		t.Fatalf("view limits = %+v extra=%+v, want primary+secondary+2 extra", view.Limits, view.ExtraLimits)
	}
	if snapshot.RateLimitReachedType != "primary_window" {
		t.Fatalf("rate limit reached type = %q, want primary_window", snapshot.RateLimitReachedType)
	}
	if !strings.Contains(snapshot.RawJSON, "last_token_usage") || !strings.Contains(snapshot.RawJSON, "total_token_usage") || !strings.Contains(snapshot.RawJSON, "spend_control") || !strings.Contains(snapshot.RawJSON, "rate_limit_reset_credits") {
		t.Fatalf("raw JSON did not preserve Codex non-identity payload: %s", snapshot.RawJSON)
	}
	if strings.Contains(snapshot.RawJSON, "person@example.com") || strings.Contains(snapshot.RawJSON, "user-secret") || strings.Contains(snapshot.RawJSON, "acct-secret-from-provider") || strings.Contains(snapshot.RawJSON, "nested@example.com") {
		t.Fatalf("raw JSON exposed OpenAI identity fields: %s", snapshot.RawJSON)
	}
}

func TestNormalizeOpenAIAccountUsageCreditBadges(t *testing.T) {
	cfg := models.LLMConfig{ID: "cfg-openai", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth, OAuthAccountID: "acct-openai"}
	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			name: "current codex account with no credits shows no badge",
			raw: map[string]any{
				"credits":                  map[string]any{"has_credits": false, "unlimited": false, "overage_limit_reached": false, "balance": "0", "approx_local_messages": []any{0, 0}, "approx_cloud_messages": []any{0, 0}},
				"spend_control":            map[string]any{"reached": false, "individual_limit": nil},
				"rate_limit_reset_credits": map[string]any{"available_count": 0},
				"rate_limit_reached_type":  "",
				"additional_rate_limits":   []any{},
			},
			want: "",
		},
		{
			name: "has credits",
			raw:  map[string]any{"credits": map[string]any{"has_credits": true, "balance": "12"}},
			want: "Usage credits available",
		},
		{
			name: "unlimited credits",
			raw:  map[string]any{"credits": map[string]any{"has_credits": true, "unlimited": true}},
			want: "Unlimited credits",
		},
		{
			name: "credit limit reached",
			raw:  map[string]any{"credits": map[string]any{"has_credits": true, "overage_limit_reached": true}},
			want: "Usage credit limit reached",
		},
		{
			name: "spend limit reached",
			raw:  map[string]any{"credits": map[string]any{"has_credits": true}, "spend_control": map[string]any{"reached": true}},
			want: "Spend limit reached",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := normalizeOpenAIAccountUsage(cfg, tc.raw)
			if err != nil {
				t.Fatalf("normalizeOpenAIAccountUsage: %v", err)
			}
			if snapshot.ExtraUsageLabel != tc.want {
				t.Fatalf("extra usage label = %q, want %q", snapshot.ExtraUsageLabel, tc.want)
			}
		})
	}
}

func TestOpenAIUsageEndpointMatchesRunbookPathStyles(t *testing.T) {
	oldBase := openaiclient.OpenAIChatGPTAPIBaseURL
	defer func() { openaiclient.OpenAIChatGPTAPIBaseURL = oldBase }()

	cases := []struct {
		base string
		want string
	}{
		{base: "https://chatgpt.com/backend-api/codex/", want: "https://chatgpt.com/backend-api/wham/usage"},
		{base: "https://chatgpt.com/backend-api/anything", want: "https://chatgpt.com/backend-api/wham/usage"},
		{base: "https://chatgpt.com/", want: "https://chatgpt.com/backend-api/wham/usage"},
		{base: "https://chatgpt.com/backend-api", want: "https://chatgpt.com/backend-api/wham/usage"},
	}
	for _, tc := range cases {
		openaiclient.OpenAIChatGPTAPIBaseURL = tc.base
		got, err := openAIUsageEndpoint()
		if err != nil {
			t.Fatalf("openAIUsageEndpoint(%q): %v", tc.base, err)
		}
		if got != tc.want {
			t.Fatalf("openAIUsageEndpoint(%q) = %q, want %q", tc.base, got, tc.want)
		}
	}
}

func BenchmarkUsageAnalyticsServiceBuildAnalyticsUsage50K(b *testing.B) {
	db := testutil.NewTestDB(b)
	ctx := context.Background()
	seedUsageAnalyticsBenchmarkEvents(b, db, ctx, 50000)
	usageRepo := repository.NewUsageRepo(db)
	svc := NewUsageAnalyticsService(usageRepo, nil)
	filter := repository.UsageFilter{
		ProjectID: "bench-project-0",
		DateFrom:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DateTo:    time.Date(2026, 4, 1, 23, 59, 59, 0, time.UTC),
		GroupBy:   "day",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := svc.BuildAnalyticsUsage(ctx, filter); err != nil {
			b.Fatalf("BuildAnalyticsUsage: %v", err)
		}
	}
}

func seedUsageAnalyticsBenchmarkEvents(b *testing.B, db *sql.DB, ctx context.Context, count int) {
	b.Helper()
	for i := 0; i < 3; i++ {
		if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name) VALUES (?, ?)`, fmt.Sprintf("bench-project-%d", i), fmt.Sprintf("Bench Project %d", i)); err != nil {
			b.Fatalf("insert project: %v", err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatalf("begin seed tx: %v", err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO llm_usage_events (
			id, provider, account_id, project_id, model, operation, status,
			input_tokens, output_tokens, cached_input_tokens, reasoning_output_tokens, total_tokens, cost_usd, raw_usage_json, occurred_at
		) VALUES (?, ?, ?, ?, ?, 'task', 'completed', ?, ?, ?, ?, ?, ?, '{}', ?)`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatalf("prepare seed usage: %v", err)
	}
	defer stmt.Close()
	providers := []string{"openai", "anthropic"}
	models := []string{"gpt-5", "gpt-5-mini", "claude-sonnet", "claude-opus"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < count; i++ {
		input := 100 + i%37
		output := 40 + i%23
		cached := i % 17
		reasoning := i % 11
		total := input + output
		cost := float64(total) / 1000000
		occurred := base.Add(time.Duration(i%2160) * time.Hour)
		if _, err := stmt.ExecContext(ctx,
			fmt.Sprintf("bench-usage-%05d", i),
			providers[i%len(providers)],
			fmt.Sprintf("acct-%d", i%5),
			fmt.Sprintf("bench-project-%d", i%3),
			models[i%len(models)],
			input,
			output,
			cached,
			reasoning,
			total,
			cost,
			occurred.Format("2006-01-02 15:04:05"),
		); err != nil {
			_ = tx.Rollback()
			b.Fatalf("insert usage event %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit seed tx: %v", err)
	}
}
