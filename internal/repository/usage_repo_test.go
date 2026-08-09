package repository

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestUsageRepo_RecordUsageEventAndAggregate(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	projectID := "proj-usage"
	taskID := "task-usage"
	execID := "exec-usage"
	agentID := "agent-usage"
	_, err := db.ExecContext(ctx, `INSERT INTO projects (id, name) VALUES (?, ?)`, projectID, "Usage Project")
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO agent_configs (id, name, provider, model, auth_method) VALUES (?, ?, ?, ?, ?)`, agentID, "OpenAI", "openai", "gpt-5.3-codex", "oauth")
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, title, category, status) VALUES (?, ?, ?, ?, ?)`, taskID, projectID, "Usage Task", "active", "running")
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO executions (id, task_id, agent_config_id, status, started_at) VALUES (?, ?, ?, ?, datetime('now'))`, execID, taskID, agentID, "completed")
	if err != nil {
		t.Fatalf("insert execution: %v", err)
	}

	cost := 1.25
	latency := int64(321)
	event := &models.LLMUsageEvent{
		Provider:                 "openai",
		AccountID:                "acct-1",
		ProjectID:                projectID,
		TaskID:                   taskID,
		ExecutionID:              execID,
		TurnID:                   execID,
		AgentConfigID:            agentID,
		Model:                    "gpt-5.3-codex",
		Operation:                "streaming",
		Status:                   "completed",
		InputTokens:              100,
		OutputTokens:             40,
		CachedInputTokens:        20,
		CacheCreationInputTokens: 5,
		CacheReadInputTokens:     15,
		ReasoningOutputTokens:    7,
		CostUSD:                  &cost,
		LatencyMs:                &latency,
		RawUsageJSON:             `{"input_tokens":100}`,
		OccurredAt:               time.Now().UTC(),
	}
	if err := repo.RecordUsageEvent(ctx, event); err != nil {
		t.Fatalf("RecordUsageEvent: %v", err)
	}
	if err := repo.RecordUsageEvent(ctx, event); err != nil {
		t.Fatalf("RecordUsageEvent duplicate: %v", err)
	}

	totals, err := repo.GetUsageTotals(ctx, UsageFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("GetUsageTotals: %v", err)
	}
	if totals.CallCount != 1 || totals.InputTokens != 100 || totals.OutputTokens != 40 || totals.TotalTokens != 140 {
		t.Fatalf("unexpected totals: %+v", totals)
	}
	if totals.CachedInputTokens != 20 || totals.CacheCreationInputTokens != 5 || totals.CacheReadInputTokens != 15 || totals.ReasoningOutputTokens != 7 {
		t.Fatalf("unexpected detailed totals: %+v", totals)
	}
	if !totals.CostAvailable || totals.CostUSD == nil || *totals.CostUSD != cost {
		t.Fatalf("expected provider cost %v, got %+v", cost, totals)
	}

	breakdown, err := repo.GetModelUsageBreakdown(ctx, UsageFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("GetModelUsageBreakdown: %v", err)
	}
	if len(breakdown) != 1 || breakdown[0].Provider != "openai" || breakdown[0].Model != "gpt-5.3-codex" || breakdown[0].Percent != 100 {
		t.Fatalf("unexpected breakdown: %+v", breakdown)
	}

	anthropicEvent := &models.LLMUsageEvent{
		Provider:     "anthropic",
		ProjectID:    projectID,
		Model:        "claude-sonnet",
		Operation:    "task",
		Status:       "completed",
		InputTokens:  25,
		OutputTokens: 15,
		OccurredAt:   time.Now().UTC().Add(2 * time.Hour),
	}
	if err := repo.RecordUsageEvent(ctx, anthropicEvent); err != nil {
		t.Fatalf("RecordUsageEvent anthropic: %v", err)
	}
	dailyByModel, err := repo.GetDailyUsageByModel(ctx, UsageFilter{ProjectID: projectID})
	if err != nil {
		t.Fatalf("GetDailyUsageByModel: %v", err)
	}
	if len(dailyByModel) != 2 {
		t.Fatalf("expected two daily model points, got %+v", dailyByModel)
	}
	rateByModel, err := repo.GetUsageRateBucketsByModel(ctx, UsageFilter{ProjectID: projectID, GroupBy: "day"})
	if err != nil {
		t.Fatalf("GetUsageRateBucketsByModel: %v", err)
	}
	if len(rateByModel) != 2 {
		t.Fatalf("expected two rate model points, got %+v", rateByModel)
	}
}

func TestUsageRepo_CostUnavailableWhenProviderCostMissing(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	if err := repo.RecordUsageEvent(ctx, &models.LLMUsageEvent{Provider: "anthropic", Model: "claude-sonnet", Operation: "task", InputTokens: 10, OutputTokens: 5}); err != nil {
		t.Fatalf("RecordUsageEvent: %v", err)
	}
	totals, err := repo.GetUsageTotals(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("GetUsageTotals: %v", err)
	}
	if totals.CostAvailable || totals.CostUSD != nil {
		t.Fatalf("expected cost unavailable, got %+v", totals)
	}
}

func TestUsageRepo_CreateAccountUsageSnapshotPersistsExtraLimits(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO agent_configs (id, name, provider, model, auth_method) VALUES (?, ?, ?, ?, ?)`, "agent-extra", "OpenAI", "openai", "gpt-test", "oauth"); err != nil {
		t.Fatalf("insert agent config: %v", err)
	}
	first := 12.5
	second := 44.0
	reset := "2026-06-08T05:00:00Z"
	snapshot := &models.AccountUsageSnapshot{
		Provider:      "openai",
		AccountID:     "acct-extra",
		AgentConfigID: "agent-extra",
		PrimaryLabel:  "5-hour session",
		ExtraLimits: []models.AccountUsageExtraLimit{
			{LimitKey: "gpt-5.3-codex-spark", Label: "GPT-5.3-Codex-Spark limit", UsedPercent: &first, ResetAt: &reset, RawJSON: `{"metered_feature":"gpt-5.3-codex-spark"}`},
			{LimitKey: "gpt-5.3-codex-pro", Label: "GPT-5.3-Codex-Pro limit", UsedPercent: &second, RawJSON: `{"metered_feature":"gpt-5.3-codex-pro"}`},
		},
	}
	if err := repo.CreateAccountUsageSnapshot(ctx, snapshot); err != nil {
		t.Fatalf("CreateAccountUsageSnapshot: %v", err)
	}

	snapshots, err := repo.GetLatestAccountUsageSnapshots(ctx, "openai")
	if err != nil {
		t.Fatalf("GetLatestAccountUsageSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected one snapshot, got %+v", snapshots)
	}
	if len(snapshots[0].ExtraLimits) != 2 {
		t.Fatalf("expected two extra limits, got %+v", snapshots[0].ExtraLimits)
	}
	if snapshots[0].ExtraLimits[0].LimitKey != "gpt-5.3-codex-spark" || snapshots[0].ExtraLimits[1].LimitKey != "gpt-5.3-codex-pro" {
		t.Fatalf("unexpected extra limits: %+v", snapshots[0].ExtraLimits)
	}
}

func TestUsageRepo_GetLatestAccountUsageSnapshotsBreaksTimestampTiesByInsertOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `INSERT INTO agent_configs (id, name, provider, model, auth_method) VALUES (?, ?, ?, ?, ?)`, "agent", "OpenAI", "openai", "gpt-test", "oauth"); err != nil {
		t.Fatalf("insert agent config: %v", err)
	}
	fetchedAt := time.Date(2026, 6, 3, 17, 0, 0, 0, time.UTC)
	if err := repo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{Provider: "openai", AccountID: "acct", AgentConfigID: "agent", PrimaryLabel: "5-hour session", FetchedAt: fetchedAt}); err != nil {
		t.Fatalf("create initial snapshot: %v", err)
	}
	if err := repo.CreateAccountUsageSnapshot(ctx, &models.AccountUsageSnapshot{Provider: "openai", AccountID: "acct", AgentConfigID: "agent", RateLimitReachedType: "refresh_failed_forbidden", RawJSON: `{"refresh_error":"refresh_failed_forbidden"}`, FetchedAt: fetchedAt}); err != nil {
		t.Fatalf("create failure snapshot: %v", err)
	}

	snapshots, err := repo.GetLatestAccountUsageSnapshots(ctx, "openai")
	if err != nil {
		t.Fatalf("GetLatestAccountUsageSnapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected one latest snapshot, got %+v", snapshots)
	}
	if snapshots[0].RateLimitReachedType != "refresh_failed_forbidden" {
		t.Fatalf("expected latest inserted snapshot to win timestamp tie, got %+v", snapshots[0])
	}
}

// TestUsageRepo_LocaltimeDayBucketing verifies that GetDailyUsage, GetDailyUsageByModel,
// and GetUsageRateBuckets group events by the server's local calendar day rather than
// the UTC calendar day. This is the Analytics-page equivalent of the Schedules page using
// time.Local / time.Now() for all calendar display, so the chart X-axis shows local dates.
func TestUsageRepo_LocaltimeDayBucketing(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	// Use a specific UTC time; the expected period label must match the local date.
	// Both SQLite's 'localtime' modifier and Go's time.Local use the OS timezone,
	// so they agree on what "today" is regardless of the UTC offset.
	eventTime := time.Now().UTC()
	expectedPeriod := eventTime.In(time.Local).Format("2006-01-02")

	if err := repo.RecordUsageEvent(ctx, &models.LLMUsageEvent{
		Provider:     "anthropic",
		Model:        "claude-sonnet",
		Operation:    "task",
		Status:       "completed",
		InputTokens:  50,
		OutputTokens: 25,
		TotalTokens:  75,
		OccurredAt:   eventTime,
	}); err != nil {
		t.Fatalf("RecordUsageEvent: %v", err)
	}

	// GetDailyUsage should label the period with the local date.
	daily, err := repo.GetDailyUsage(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("GetDailyUsage: %v", err)
	}
	if len(daily) != 1 {
		t.Fatalf("expected one daily point, got %d: %+v", len(daily), daily)
	}
	if daily[0].Period != expectedPeriod {
		t.Errorf("GetDailyUsage period: got %q, want %q (local date for UTC time %v)",
			daily[0].Period, expectedPeriod, eventTime)
	}

	// GetDailyUsageByModel should label the period with the local date.
	byModel, err := repo.GetDailyUsageByModel(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("GetDailyUsageByModel: %v", err)
	}
	if len(byModel) != 1 {
		t.Fatalf("expected one daily-by-model point, got %d: %+v", len(byModel), byModel)
	}
	if byModel[0].Period != expectedPeriod {
		t.Errorf("GetDailyUsageByModel period: got %q, want %q (local date for UTC time %v)",
			byModel[0].Period, expectedPeriod, eventTime)
	}

	// GetUsageRateBuckets with group_by=day should label the period with the local date.
	rate, err := repo.GetUsageRateBuckets(ctx, UsageFilter{GroupBy: "day"})
	if err != nil {
		t.Fatalf("GetUsageRateBuckets: %v", err)
	}
	if len(rate) != 1 {
		t.Fatalf("expected one rate bucket, got %d: %+v", len(rate), rate)
	}
	if rate[0].Period != expectedPeriod {
		t.Errorf("GetUsageRateBuckets period: got %q, want %q (local date for UTC time %v)",
			rate[0].Period, expectedPeriod, eventTime)
	}

	// GetUsageRateBucketsByModel with group_by=day should also label by local date.
	rateByModel, err := repo.GetUsageRateBucketsByModel(ctx, UsageFilter{GroupBy: "day"})
	if err != nil {
		t.Fatalf("GetUsageRateBucketsByModel: %v", err)
	}
	if len(rateByModel) != 1 {
		t.Fatalf("expected one rate-by-model bucket, got %d: %+v", len(rateByModel), rateByModel)
	}
	if rateByModel[0].Period != expectedPeriod {
		t.Errorf("GetUsageRateBucketsByModel period: got %q, want %q (local date for UTC time %v)",
			rateByModel[0].Period, expectedPeriod, eventTime)
	}
}

// TestUsageRepo_LocaltimeBucketingCrossesMidnight verifies that an event stored at a UTC
// time that falls on a different local calendar day than its UTC date is bucketed by the
// correct LOCAL day. In timezones with nonzero offsets, UTC midnight may correspond to
// yesterday or tomorrow in local time; the Analytics charts must reflect local dates.
func TestUsageRepo_LocaltimeBucketingCrossesMidnight(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	// Build two event times: one on local day N, one on local day N+1.
	// We derive both using time.Local so the test is timezone-agnostic.
	localRef := time.Now().In(time.Local).Truncate(24 * time.Hour) // midnight of today, local
	dayN := localRef.Add(6 * time.Hour)                            // 06:00 local today → UTC varies by TZ
	dayN1 := localRef.Add(30 * time.Hour)                          // 06:00 local tomorrow

	for i, eventTime := range []time.Time{dayN.UTC(), dayN1.UTC()} {
		if err := repo.RecordUsageEvent(ctx, &models.LLMUsageEvent{
			Provider:     "openai",
			Model:        "gpt-5",
			Operation:    "task",
			Status:       "completed",
			InputTokens:  10,
			OutputTokens: 5,
			TotalTokens:  15,
			OccurredAt:   eventTime,
		}); err != nil {
			t.Fatalf("RecordUsageEvent[%d]: %v", i, err)
		}
	}

	daily, err := repo.GetDailyUsage(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("GetDailyUsage: %v", err)
	}
	if len(daily) != 2 {
		t.Fatalf("expected two daily points (one per local day), got %d: %+v", len(daily), daily)
	}
	expectedDayN := dayN.In(time.Local).Format("2006-01-02")
	expectedDayN1 := dayN1.In(time.Local).Format("2006-01-02")
	if daily[0].Period != expectedDayN || daily[1].Period != expectedDayN1 {
		t.Errorf("expected periods [%q, %q], got [%q, %q]",
			expectedDayN, expectedDayN1, daily[0].Period, daily[1].Period)
	}
}

// TestUsageRepo_ModelUsageGlobalVisibility verifies that model usage events recorded with
// an empty project_id appear in GetModelUsageBreakdown and GetUsageTotals when no project
// filter is applied. This tests the global model-usage visibility path for the Analytics page
// which does not send a project_id to /api/analytics/usage.
func TestUsageRepo_ModelUsageGlobalVisibility(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()

	// Record Anthropic and OpenAI events without a project_id (simulating direct/lifecycle calls).
	if err := repo.RecordUsageEvent(ctx, &models.LLMUsageEvent{
		Provider:     "anthropic",
		Model:        "claude-sonnet-4",
		Operation:    "task",
		Status:       "completed",
		InputTokens:  80,
		OutputTokens: 40,
		TotalTokens:  120,
		OccurredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordUsageEvent anthropic: %v", err)
	}
	if err := repo.RecordUsageEvent(ctx, &models.LLMUsageEvent{
		Provider:     "openai",
		Model:        "gpt-5.3-codex",
		Operation:    "streaming",
		Status:       "completed",
		InputTokens:  60,
		OutputTokens: 30,
		TotalTokens:  90,
		OccurredAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("RecordUsageEvent openai: %v", err)
	}

	// Global query (no project filter) must return both providers.
	breakdown, err := repo.GetModelUsageBreakdown(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("GetModelUsageBreakdown: %v", err)
	}
	if len(breakdown) != 2 {
		t.Fatalf("expected two breakdown rows (one per provider), got %d: %+v", len(breakdown), breakdown)
	}
	totalTokens := 0
	for _, b := range breakdown {
		totalTokens += b.TotalTokens
	}
	if totalTokens != 210 {
		t.Errorf("expected 210 total tokens across providers, got %d", totalTokens)
	}

	totals, err := repo.GetUsageTotals(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("GetUsageTotals: %v", err)
	}
	if totals.CallCount != 2 {
		t.Errorf("expected call_count=2 (global, no project filter), got %d", totals.CallCount)
	}
	if totals.TotalTokens != 210 {
		t.Errorf("expected total_tokens=210, got %d", totals.TotalTokens)
	}
}

func TestUsageRepo_ProjectDateBoundedAggregatePlansAvoidTempGroupBy(t *testing.T) {
	db := testutil.NewTestDB(t)
	filter := UsageFilter{
		ProjectID: "project-plan",
		DateFrom:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DateTo:    time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		GroupBy:   "day",
	}

	periodExpr, source, where, args := usageEventSourceWithPeriod(filter, "day")
	assertNoTempGroupBy(t, db, "GetDailyUsage", `
		SELECT `+periodExpr+` AS period,
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(total_tokens), 0), SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY period
		ORDER BY period ASC`, args...)
	assertNoTempGroupBy(t, db, "GetDailyUsageByModel", `
		SELECT `+periodExpr+` AS period, provider, model,
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(total_tokens), 0), SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY period, provider, model
		ORDER BY period ASC, provider ASC, model ASC`, args...)

	for _, groupBy := range []string{"hour", "day", "week", "month"} {
		filter.GroupBy = groupBy
		periodExpr, source, where, args = usageEventSourceWithPeriod(filter, groupBy)
		assertNoTempGroupBy(t, db, "GetUsageRateBuckets/"+groupBy, `
			SELECT `+periodExpr+` AS period, COALESCE(SUM(total_tokens), 0), COUNT(*)
			FROM `+source+` `+where+`
			GROUP BY period
			ORDER BY period ASC`, args...)
		assertNoTempGroupBy(t, db, "GetUsageRateBucketsByModel/"+groupBy, `
			SELECT `+periodExpr+` AS period, provider, model, COALESCE(SUM(total_tokens), 0), COUNT(*)
			FROM `+source+` `+where+`
			GROUP BY period, provider, model
			ORDER BY period ASC, provider ASC, model ASC`, args...)
	}

	source, where, args = usageEventSourceForModelBreakdown(filter)
	assertNoTempGroupBy(t, db, "GetModelUsageBreakdown", `
		SELECT provider, model, COALESCE(SUM(total_tokens), 0), COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0), COALESCE(SUM(reasoning_output_tokens), 0),
		       SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY provider, model
		ORDER BY COALESCE(SUM(total_tokens), 0) DESC, provider ASC, model ASC`, args...)
}

func TestUsageRepo_LocalBucketIndexesPreserveDSTSensitiveLocaltimeSemantics(t *testing.T) {
	oldTZ, hadTZ := os.LookupEnv("TZ")
	oldLocal := time.Local
	t.Cleanup(func() {
		if hadTZ {
			_ = os.Setenv("TZ", oldTZ)
		} else {
			_ = os.Unsetenv("TZ")
		}
		time.Local = oldLocal
	})
	if err := os.Setenv("TZ", "America/New_York"); err != nil {
		t.Fatalf("set TZ: %v", err)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	time.Local = loc

	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()
	projectID := "project-dst"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name) VALUES (?, ?)`, projectID, "DST Usage Project"); err != nil {
		t.Fatalf("insert project: %v", err)
	}

	events := []time.Time{
		time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC),
		time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC),
		time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC),
	}
	for i, eventTime := range events {
		if err := repo.RecordUsageEvent(ctx, &models.LLMUsageEvent{
			Provider:    "openai",
			ProjectID:   projectID,
			Model:       "gpt-dst",
			Operation:   "task",
			Status:      "completed",
			InputTokens: 10 + i,
			TotalTokens: 10 + i,
			OccurredAt:  eventTime,
		}); err != nil {
			t.Fatalf("RecordUsageEvent[%d]: %v", i, err)
		}
	}

	filter := UsageFilter{
		ProjectID: projectID,
		DateFrom:  time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC),
		DateTo:    time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	}
	for _, groupBy := range []string{"hour", "day", "week", "month"} {
		filter.GroupBy = groupBy
		legacy := legacyUsageRateBuckets(t, db, filter)
		optimized, err := repo.GetUsageRateBuckets(ctx, filter)
		if err != nil {
			t.Fatalf("GetUsageRateBuckets(%s): %v", groupBy, err)
		}
		if !reflect.DeepEqual(optimized, legacy) {
			t.Fatalf("%s localtime bucket mismatch\noptimized=%+v\nlegacy=%+v", groupBy, optimized, legacy)
		}
	}
}

func legacyUsageRateBuckets(t *testing.T, db *sql.DB, filter UsageFilter) []models.UsageRatePoint {
	t.Helper()
	where, args := usageWhere(filter)
	periodExpr := usagePeriodExpression(filter.GroupBy)
	rows, err := db.QueryContext(context.Background(), `
		SELECT `+periodExpr+` AS period, COALESCE(SUM(total_tokens), 0), COUNT(*)
		FROM llm_usage_events `+where+`
		GROUP BY period
		ORDER BY period ASC`, args...)
	if err != nil {
		t.Fatalf("legacy usage rate buckets: %v", err)
	}
	defer rows.Close()
	var points []models.UsageRatePoint
	for rows.Next() {
		var point models.UsageRatePoint
		if err := rows.Scan(&point.Period, &point.TotalTokens, &point.CallCount); err != nil {
			t.Fatalf("scan legacy usage rate: %v", err)
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate legacy usage rate: %v", err)
	}
	return points
}

func assertNoTempGroupBy(t *testing.T, db *sql.DB, name, query string, args ...any) {
	t.Helper()
	plan := explainUsageQueryPlan(t, db, query, args...)
	if strings.Contains(plan, "USE TEMP B-TREE FOR GROUP BY") {
		t.Fatalf("%s plan still uses temp GROUP BY B-tree:\n%s", name, plan)
	}
}

func explainUsageQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()
	var parts []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate query plan: %v", err)
	}
	return strings.Join(parts, "\n")
}
