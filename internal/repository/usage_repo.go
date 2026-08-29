package repository

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type UsageRepo struct {
	db *sql.DB
}

func NewUsageRepo(db *sql.DB) *UsageRepo {
	return &UsageRepo{db: db}
}

type UsageFilter struct {
	ProjectID string
	Provider  string
	AccountID string
	DateFrom  time.Time
	DateTo    time.Time
	GroupBy   string
	Refresh   bool
}

func (r *UsageRepo) RecordUsageEvent(ctx context.Context, event *models.LLMUsageEvent) error {
	if event == nil {
		return nil
	}
	if event.RawUsageJSON == "" {
		event.RawUsageJSON = "{}"
	}
	if event.TotalTokens == 0 {
		event.TotalTokens = event.InputTokens + event.OutputTokens
	}
	if event.CachedInputTokens == 0 && (event.CacheCreationInputTokens > 0 || event.CacheReadInputTokens > 0) {
		event.CachedInputTokens = event.CacheCreationInputTokens + event.CacheReadInputTokens
	}
	occurredAt := event.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	var occurredRaw, createdRaw string
	err := queryRowBoundSQLite(ctx, r.db, `
		INSERT OR IGNORE INTO llm_usage_events (
			id, provider, account_id, project_id, task_id, execution_id, chat_thread_id, turn_id,
			agent_config_id, model, operation, status, error_message,
			input_tokens, output_tokens, cached_input_tokens, cache_creation_input_tokens,
			cache_read_input_tokens, reasoning_output_tokens, total_tokens, cost_usd, latency_ms,
			context_window, max_output_tokens, provider_response_id, raw_usage_json, occurred_at
		) VALUES (
			lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		)
		RETURNING id, occurred_at, created_at`,
		event.Provider, nullStringArg(event.AccountID), nullStringArg(event.ProjectID), nullStringArg(event.TaskID), nullStringArg(event.ExecutionID), nullStringArg(event.ChatThreadID), nullStringArg(event.TurnID),
		nullStringArg(event.AgentConfigID), event.Model, event.Operation, event.Status, event.ErrorMessage,
		event.InputTokens, event.OutputTokens, event.CachedInputTokens, event.CacheCreationInputTokens,
		event.CacheReadInputTokens, event.ReasoningOutputTokens, event.TotalTokens, event.CostUSD, event.LatencyMs,
		event.ContextWindow, event.MaxOutputTokens, nullStringArg(event.ProviderResponseID), event.RawUsageJSON, formatSQLiteTime(occurredAt)).
		Scan(&event.ID, &occurredRaw, &createdRaw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recording llm usage event: %w", err)
	}
	event.OccurredAt = parseSQLiteTime(occurredRaw)
	event.CreatedAt = parseSQLiteTime(createdRaw)
	return nil
}

func (r *UsageRepo) CreateAccountUsageSnapshot(ctx context.Context, snapshot *models.AccountUsageSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.RawJSON == "" {
		snapshot.RawJSON = "{}"
	}
	fetchedAt := snapshot.FetchedAt
	if fetchedAt.IsZero() {
		fetchedAt = time.Now().UTC()
	}

	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("starting account usage snapshot transaction: %w", err)
	}
	defer cleanup()

	var fetchedRaw, createdRaw string
	err = tx.QueryRowContext(ctx, `
				INSERT INTO account_usage_snapshots (
					id, provider, account_id, agent_config_id, plan_type, account_display_name, account_detail,
					billing_label, subscription_status, extra_usage_label, extra_usage_monthly_limit_usd, extra_usage_used_usd, credits_remaining,
					primary_label, primary_used_percent, primary_window_minutes, primary_resets_at,				secondary_label, secondary_used_percent, secondary_window_minutes, secondary_resets_at,
				model_limit_label, model_limit_used_percent, model_limit_window_minutes, model_limit_resets_at,
				rate_limit_reached_type, raw_json, fetched_at
				) VALUES (
					lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
				)
				RETURNING id, fetched_at, created_at`,
		snapshot.Provider, nullStringArg(snapshot.AccountID), nullStringArg(snapshot.AgentConfigID), snapshot.PlanType, snapshot.AccountDisplayName, snapshot.AccountDetail,
		snapshot.BillingLabel, snapshot.SubscriptionStatus, snapshot.ExtraUsageLabel, snapshot.ExtraUsageMonthlyUSD, snapshot.ExtraUsageUsedUSD, snapshot.CreditsRemaining,
		snapshot.PrimaryLabel, snapshot.PrimaryUsedPercent, snapshot.PrimaryWindowMinutes, snapshot.PrimaryResetsAt, snapshot.SecondaryLabel, snapshot.SecondaryUsedPercent, snapshot.SecondaryWindowMinutes, snapshot.SecondaryResetsAt,
		snapshot.ModelLimitLabel, snapshot.ModelLimitUsedPercent, snapshot.ModelLimitWindowMinutes, snapshot.ModelLimitResetsAt,
		snapshot.RateLimitReachedType, snapshot.RawJSON, formatSQLiteTime(fetchedAt)).
		Scan(&snapshot.ID, &fetchedRaw, &createdRaw)
	if err != nil {
		return fmt.Errorf("creating account usage snapshot: %w", err)
	}
	if err := insertAccountUsageExtraLimits(ctx, tx, snapshot); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing account usage snapshot: %w", err)
	}
	snapshot.FetchedAt = parseSQLiteTime(fetchedRaw)
	snapshot.CreatedAt = parseSQLiteTime(createdRaw)
	return nil
}

func (r *UsageRepo) GetLatestAccountUsageSnapshots(ctx context.Context, provider string) ([]models.AccountUsageSnapshot, error) {
	where := "WHERE 1=1"
	args := []any{}
	if provider != "" {
		where += " AND s.provider = ?"
		args = append(args, provider)
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.provider, COALESCE(s.account_id, ''), COALESCE(s.agent_config_id, ''), s.plan_type,
		       COALESCE(s.account_display_name, ''), COALESCE(s.account_detail, ''), COALESCE(s.billing_label, ''),
		       COALESCE(s.subscription_status, ''), COALESCE(s.extra_usage_label, ''), s.extra_usage_monthly_limit_usd, s.extra_usage_used_usd,
		       s.credits_remaining,
		       s.primary_label, s.primary_used_percent, s.primary_window_minutes, s.primary_resets_at,
		       s.secondary_label, s.secondary_used_percent, s.secondary_window_minutes, s.secondary_resets_at,
		       s.model_limit_label, s.model_limit_used_percent, s.model_limit_window_minutes, s.model_limit_resets_at,
		       s.rate_limit_reached_type, s.raw_json, s.fetched_at, s.created_at
		FROM account_usage_snapshots s
		`+where+`
			  AND NOT EXISTS (
			    SELECT 1 FROM account_usage_snapshots newer
			    WHERE newer.provider = s.provider
			      AND COALESCE(newer.account_id, '') = COALESCE(s.account_id, '')
			      AND COALESCE(newer.agent_config_id, '') = COALESCE(s.agent_config_id, '')
			      AND (
			        newer.fetched_at > s.fetched_at
			        OR (newer.fetched_at = s.fetched_at AND newer.created_at > s.created_at)
			        OR (newer.fetched_at = s.fetched_at AND newer.created_at = s.created_at AND newer.rowid > s.rowid)
			      )
			  )
			ORDER BY s.provider ASC, s.fetched_at DESC, s.created_at DESC, s.rowid DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("listing latest account usage snapshots: %w", err)
	}
	defer rows.Close()

	var snapshots []models.AccountUsageSnapshot
	for rows.Next() {
		snapshot, scanErr := scanAccountUsageSnapshot(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := r.attachAccountUsageExtraLimits(ctx, snapshots); err != nil {
		return nil, err
	}
	return snapshots, nil
}

func (r *UsageRepo) GetUsageTotals(ctx context.Context, filter UsageFilter) (*models.UsageTotals, error) {
	where, args := usageWhere(filter)
	var totals models.UsageTotals
	var cost sql.NullFloat64
	var costCount int
	err := r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		       COALESCE(SUM(reasoning_output_tokens), 0), COALESCE(SUM(total_tokens), 0),
		       SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM llm_usage_events `+where, args...).Scan(
		&totals.InputTokens, &totals.OutputTokens, &totals.CachedInputTokens,
		&totals.CacheCreationInputTokens, &totals.CacheReadInputTokens,
		&totals.ReasoningOutputTokens, &totals.TotalTokens,
		&cost, &costCount, &totals.CallCount)
	if err != nil {
		return nil, fmt.Errorf("getting usage totals: %w", err)
	}
	if costCount > 0 && cost.Valid {
		totals.CostAvailable = true
		totals.CostUSD = &cost.Float64
	}
	return &totals, nil
}

func (r *UsageRepo) GetDailyUsage(ctx context.Context, filter UsageFilter) ([]models.DailyUsagePoint, error) {
	if shouldUseProjectDateBoundedAggregate(filter) {
		return r.getDailyUsageFromScan(ctx, filter, false)
	}
	where, args := usageWhere(filter)
	periodExpr := usagePeriodExpression("day")
	source := "llm_usage_events"
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+periodExpr+` AS period,
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(total_tokens), 0), SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY period
		ORDER BY period ASC`, args...)

	if err != nil {
		return nil, fmt.Errorf("getting daily usage: %w", err)
	}
	defer rows.Close()

	var points []models.DailyUsagePoint
	for rows.Next() {
		var point models.DailyUsagePoint
		var cost sql.NullFloat64
		var costCount int
		if err := rows.Scan(&point.Period, &point.InputTokens, &point.OutputTokens, &point.CacheTokens, &point.TotalTokens, &cost, &costCount, &point.CallCount); err != nil {
			return nil, fmt.Errorf("scanning daily usage: %w", err)
		}
		if costCount > 0 && cost.Valid {
			point.CostUSD = &cost.Float64
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (r *UsageRepo) GetDailyUsageByModel(ctx context.Context, filter UsageFilter) ([]models.DailyUsagePoint, error) {
	if shouldUseProjectDateBoundedAggregate(filter) {
		return r.getDailyUsageFromScan(ctx, filter, true)
	}
	where, args := usageWhere(filter)
	periodExpr := usagePeriodExpression("day")
	source := "llm_usage_events"
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+periodExpr+` AS period, provider, model,
		       COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0),
		       COALESCE(SUM(total_tokens), 0), SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY period, provider, model
		ORDER BY period ASC, provider ASC, model ASC`, args...)

	if err != nil {
		return nil, fmt.Errorf("getting daily usage by model: %w", err)
	}
	defer rows.Close()

	var points []models.DailyUsagePoint
	for rows.Next() {
		var point models.DailyUsagePoint
		var cost sql.NullFloat64
		var costCount int
		if err := rows.Scan(&point.Period, &point.Provider, &point.Model, &point.InputTokens, &point.OutputTokens, &point.CacheTokens, &point.TotalTokens, &cost, &costCount, &point.CallCount); err != nil {
			return nil, fmt.Errorf("scanning daily usage by model: %w", err)
		}
		if costCount > 0 && cost.Valid {
			point.CostUSD = &cost.Float64
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (r *UsageRepo) GetUsageRateBuckets(ctx context.Context, filter UsageFilter) ([]models.UsageRatePoint, error) {
	if shouldUseProjectDateBoundedAggregate(filter) {
		return r.getUsageRateBucketsFromScan(ctx, filter, false)
	}
	where, args := usageWhere(filter)
	periodExpr := usagePeriodExpression(filter.GroupBy)
	source := "llm_usage_events"
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+periodExpr+` AS period, COALESCE(SUM(total_tokens), 0), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY period
		ORDER BY period ASC`, args...)

	if err != nil {
		return nil, fmt.Errorf("getting usage rate buckets: %w", err)
	}
	defer rows.Close()

	var points []models.UsageRatePoint
	for rows.Next() {
		var point models.UsageRatePoint
		if err := rows.Scan(&point.Period, &point.TotalTokens, &point.CallCount); err != nil {
			return nil, fmt.Errorf("scanning usage rate: %w", err)
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (r *UsageRepo) GetUsageRateBucketsByModel(ctx context.Context, filter UsageFilter) ([]models.UsageRatePoint, error) {
	if shouldUseProjectDateBoundedAggregate(filter) {
		return r.getUsageRateBucketsFromScan(ctx, filter, true)
	}
	where, args := usageWhere(filter)
	periodExpr := usagePeriodExpression(filter.GroupBy)
	source := "llm_usage_events"
	rows, err := r.db.QueryContext(ctx, `
		SELECT `+periodExpr+` AS period, provider, model, COALESCE(SUM(total_tokens), 0), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY period, provider, model
		ORDER BY period ASC, provider ASC, model ASC`, args...)

	if err != nil {
		return nil, fmt.Errorf("getting usage rate buckets by model: %w", err)
	}
	defer rows.Close()

	var points []models.UsageRatePoint
	for rows.Next() {
		var point models.UsageRatePoint
		if err := rows.Scan(&point.Period, &point.Provider, &point.Model, &point.TotalTokens, &point.CallCount); err != nil {
			return nil, fmt.Errorf("scanning usage rate by model: %w", err)
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func (r *UsageRepo) GetLatestUsageEventTime(ctx context.Context, filter UsageFilter) (*time.Time, error) {
	where, args := usageWhere(filter)
	var occurredRaw string
	err := r.db.QueryRowContext(ctx, `
		SELECT occurred_at
		FROM llm_usage_events `+where+`
		ORDER BY occurred_at DESC, rowid DESC
		LIMIT 1`, args...).Scan(&occurredRaw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest usage event time: %w", err)
	}
	occurredAt := parseSQLiteTime(occurredRaw)
	return &occurredAt, nil
}

func (r *UsageRepo) GetModelUsageBreakdown(ctx context.Context, filter UsageFilter) ([]models.ModelUsagePoint, error) {
	if shouldUseProjectDateBoundedAggregate(filter) {
		return r.getModelUsageBreakdownFromScan(ctx, filter)
	}
	where, args := usageWhere(filter)
	source := "llm_usage_events"
	rows, err := r.db.QueryContext(ctx, `
		SELECT provider, model, COALESCE(SUM(total_tokens), 0), COALESCE(SUM(input_tokens), 0),
		       COALESCE(SUM(output_tokens), 0), COALESCE(SUM(cached_input_tokens), 0), COALESCE(SUM(reasoning_output_tokens), 0),
		       SUM(cost_usd), COUNT(cost_usd), COUNT(*)
		FROM `+source+` `+where+`
		GROUP BY provider, model
		ORDER BY COALESCE(SUM(total_tokens), 0) DESC, provider ASC, model ASC`, args...)

	if err != nil {
		return nil, fmt.Errorf("getting model usage breakdown: %w", err)
	}
	defer rows.Close()

	var points []models.ModelUsagePoint
	var total int
	for rows.Next() {
		var point models.ModelUsagePoint
		var cost sql.NullFloat64
		var costCount int
		if err := rows.Scan(&point.Provider, &point.Model, &point.TotalTokens, &point.InputTokens, &point.OutputTokens, &point.CacheTokens, &point.ReasoningOutputTokens, &cost, &costCount, &point.CallCount); err != nil {
			return nil, fmt.Errorf("scanning model usage breakdown: %w", err)
		}
		if costCount > 0 && cost.Valid {
			point.CostUSD = &cost.Float64
		}
		total += point.TotalTokens
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if total > 0 {
		for i := range points {
			points[i].Percent = float64(points[i].TotalTokens) * 100 / float64(total)
		}
	}
	return points, nil
}

type usageAggregateEvent struct {
	Provider              string
	Model                 string
	InputTokens           int
	OutputTokens          int
	CacheTokens           int
	ReasoningOutputTokens int
	TotalTokens           int
	CostUSD               float64
	CostValid             bool
	OccurredAt            time.Time
}

type usageModelKey struct {
	Period   string
	Provider string
	Model    string
}

type usageDailyAggregate struct {
	point     models.DailyUsagePoint
	costSum   float64
	costCount int
}

type usageRateAggregate struct {
	point models.UsageRatePoint
}

type usageModelAggregate struct {
	point     models.ModelUsagePoint
	costSum   float64
	costCount int
}

func (r *UsageRepo) getDailyUsageFromScan(ctx context.Context, filter UsageFilter, byModel bool) ([]models.DailyUsagePoint, error) {
	aggregates := make(map[usageModelKey]*usageDailyAggregate)
	if err := r.forEachUsageAggregateEvent(ctx, filter, func(event usageAggregateEvent) {
		key := usageModelKey{Period: usageLocalPeriod(event.OccurredAt, "day")}
		if byModel {
			key.Provider = event.Provider
			key.Model = event.Model
		}
		agg := aggregates[key]
		if agg == nil {
			agg = &usageDailyAggregate{point: models.DailyUsagePoint{Period: key.Period, Provider: key.Provider, Model: key.Model}}
			aggregates[key] = agg
		}
		agg.point.InputTokens += event.InputTokens
		agg.point.OutputTokens += event.OutputTokens
		agg.point.CacheTokens += event.CacheTokens
		agg.point.TotalTokens += event.TotalTokens
		agg.point.CallCount++
		if event.CostValid {
			agg.costSum += event.CostUSD
			agg.costCount++
		}
	}); err != nil {
		return nil, err
	}
	keys := sortedUsageModelKeys(aggregates)
	points := make([]models.DailyUsagePoint, 0, len(keys))
	for _, key := range keys {
		agg := aggregates[key]
		point := agg.point
		if agg.costCount > 0 {
			cost := agg.costSum
			point.CostUSD = &cost
		}
		points = append(points, point)
	}
	return points, nil
}

func (r *UsageRepo) getUsageRateBucketsFromScan(ctx context.Context, filter UsageFilter, byModel bool) ([]models.UsageRatePoint, error) {
	groupBy := normalizedUsageGroupBy(filter.GroupBy)
	aggregates := make(map[usageModelKey]*usageRateAggregate)
	if err := r.forEachUsageAggregateEvent(ctx, filter, func(event usageAggregateEvent) {
		key := usageModelKey{Period: usageLocalPeriod(event.OccurredAt, groupBy)}
		if byModel {
			key.Provider = event.Provider
			key.Model = event.Model
		}
		agg := aggregates[key]
		if agg == nil {
			agg = &usageRateAggregate{point: models.UsageRatePoint{Period: key.Period, Provider: key.Provider, Model: key.Model}}
			aggregates[key] = agg
		}
		agg.point.TotalTokens += event.TotalTokens
		agg.point.CallCount++
	}); err != nil {
		return nil, err
	}
	keys := sortedUsageModelKeys(aggregates)
	points := make([]models.UsageRatePoint, 0, len(keys))
	for _, key := range keys {
		points = append(points, aggregates[key].point)
	}
	return points, nil
}

func (r *UsageRepo) getModelUsageBreakdownFromScan(ctx context.Context, filter UsageFilter) ([]models.ModelUsagePoint, error) {
	aggregates := make(map[usageModelKey]*usageModelAggregate)
	var total int
	if err := r.forEachUsageAggregateEvent(ctx, filter, func(event usageAggregateEvent) {
		key := usageModelKey{Provider: event.Provider, Model: event.Model}
		agg := aggregates[key]
		if agg == nil {
			agg = &usageModelAggregate{point: models.ModelUsagePoint{Provider: key.Provider, Model: key.Model}}
			aggregates[key] = agg
		}
		agg.point.TotalTokens += event.TotalTokens
		agg.point.InputTokens += event.InputTokens
		agg.point.OutputTokens += event.OutputTokens
		agg.point.CacheTokens += event.CacheTokens
		agg.point.ReasoningOutputTokens += event.ReasoningOutputTokens
		agg.point.CallCount++
		if event.CostValid {
			agg.costSum += event.CostUSD
			agg.costCount++
		}
		total += event.TotalTokens
	}); err != nil {
		return nil, err
	}
	points := make([]models.ModelUsagePoint, 0, len(aggregates))
	for _, agg := range aggregates {
		point := agg.point
		if agg.costCount > 0 {
			cost := agg.costSum
			point.CostUSD = &cost
		}
		if total > 0 {
			point.Percent = float64(point.TotalTokens) * 100 / float64(total)
		}
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].TotalTokens != points[j].TotalTokens {
			return points[i].TotalTokens > points[j].TotalTokens
		}
		if points[i].Provider != points[j].Provider {
			return points[i].Provider < points[j].Provider
		}
		return points[i].Model < points[j].Model
	})
	return points, nil
}

func (r *UsageRepo) forEachUsageAggregateEvent(ctx context.Context, filter UsageFilter, handle func(usageAggregateEvent)) error {
	source, where, args := usageAggregateScanSource(filter)
	rows, err := r.db.QueryContext(ctx, `
		SELECT provider, model, input_tokens, output_tokens, cached_input_tokens, reasoning_output_tokens,
		       total_tokens, cost_usd, occurred_at
		FROM `+source+` `+where+`
		ORDER BY occurred_at ASC`, args...)
	if err != nil {
		return fmt.Errorf("scanning usage aggregate events: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var event usageAggregateEvent
		var cost sql.NullFloat64
		var occurredRaw string
		if err := rows.Scan(&event.Provider, &event.Model, &event.InputTokens, &event.OutputTokens, &event.CacheTokens, &event.ReasoningOutputTokens, &event.TotalTokens, &cost, &occurredRaw); err != nil {
			return fmt.Errorf("scanning usage aggregate event: %w", err)
		}
		if cost.Valid {
			event.CostUSD = cost.Float64
			event.CostValid = true
		}
		event.OccurredAt = parseSQLiteTime(occurredRaw)
		handle(event)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return nil
}

func usageAggregateScanSource(filter UsageFilter) (string, string, []any) {
	where, args := usageWhere(filter)
	return "llm_usage_events", where, args
}

func shouldUseProjectDateBoundedAggregate(filter UsageFilter) bool {
	return filter.ProjectID != "" && !filter.DateFrom.IsZero() && !filter.DateTo.IsZero()
}

func sortedUsageModelKeys[T any](aggregates map[usageModelKey]T) []usageModelKey {
	keys := make([]usageModelKey, 0, len(aggregates))
	for key := range aggregates {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Period != keys[j].Period {
			return keys[i].Period < keys[j].Period
		}
		if keys[i].Provider != keys[j].Provider {
			return keys[i].Provider < keys[j].Provider
		}
		return keys[i].Model < keys[j].Model
	})
	return keys
}

func normalizedUsageGroupBy(groupBy string) string {
	switch groupBy {
	case "hour", "week", "month":
		return groupBy
	default:
		return "day"
	}
}

func usageLocalPeriod(occurredAt time.Time, groupBy string) string {
	local := occurredAt.In(time.Local)
	switch normalizedUsageGroupBy(groupBy) {
	case "hour":
		return local.Format("2006-01-02 15:00:00")
	case "week":
		return fmt.Sprintf("%04d-W%02d", local.Year(), sqliteWeekMonday(local))
	case "month":
		return local.Format("2006-01")
	default:
		return local.Format("2006-01-02")
	}
}

func sqliteWeekMonday(t time.Time) int {
	jan1 := time.Date(t.Year(), time.January, 1, 0, 0, 0, 0, t.Location())
	firstMonday := (int(time.Monday) - int(jan1.Weekday()) + 7) % 7
	yday := t.YearDay() - 1
	if yday < firstMonday {
		return 0
	}
	return (yday-firstMonday)/7 + 1
}

func usageWhere(filter UsageFilter) (string, []any) {
	clauses := []string{"1=1"}
	args := []any{}
	if filter.ProjectID != "" {
		clauses = append(clauses, "project_id = ?")
		args = append(args, filter.ProjectID)
	}
	if filter.Provider != "" {
		clauses = append(clauses, "provider = ?")
		args = append(args, filter.Provider)
	}
	if filter.AccountID != "" {
		clauses = append(clauses, "account_id = ?")
		args = append(args, filter.AccountID)
	}
	if !filter.DateFrom.IsZero() {
		clauses = append(clauses, "occurred_at >= ?")
		args = append(args, formatSQLiteTime(filter.DateFrom))
	}
	if !filter.DateTo.IsZero() {
		clauses = append(clauses, "occurred_at <= ?")
		args = append(args, formatSQLiteTime(filter.DateTo))
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// usagePeriodExpression returns a SQLite expression that groups occurred_at into
// the requested bucket using the server's local timezone ('localtime' modifier).
// This matches the Schedules page pattern of using time.Local / time.Now() so
// that chart X-axis labels show local calendar days/weeks rather than UTC days.
func usagePeriodExpression(groupBy string) string {
	switch groupBy {
	case "hour":
		return "strftime('%Y-%m-%d %H:00:00', occurred_at, 'localtime')"
	case "week":
		return "strftime('%Y-W%W', occurred_at, 'localtime')"
	case "month":
		return "strftime('%Y-%m', occurred_at, 'localtime')"
	default:
		return "date(occurred_at, 'localtime')"
	}
}

func insertAccountUsageExtraLimits(ctx context.Context, tx SQLExecutor, snapshot *models.AccountUsageSnapshot) error {
	if snapshot == nil || len(snapshot.ExtraLimits) == 0 {
		return nil
	}
	for i := range snapshot.ExtraLimits {
		limit := &snapshot.ExtraLimits[i]
		if strings.TrimSpace(limit.LimitKey) == "" {
			continue
		}
		if limit.RawJSON == "" {
			limit.RawJSON = "{}"
		}
		limit.SnapshotID = snapshot.ID
		if limit.Provider == "" {
			limit.Provider = snapshot.Provider
		}
		if limit.AccountID == "" {
			limit.AccountID = snapshot.AccountID
		}
		if limit.AgentConfigID == "" {
			limit.AgentConfigID = snapshot.AgentConfigID
		}
		if limit.Label == "" {
			limit.Label = limit.LimitKey
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT OR REPLACE INTO account_usage_extra_limits (
				id, snapshot_id, provider, account_id, agent_config_id, limit_key, label,
				used_percent, window_minutes, reset_at, raw_json
			) VALUES (
				lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
			)
			RETURNING id`,
			limit.SnapshotID, limit.Provider, nullStringArg(limit.AccountID), nullStringArg(limit.AgentConfigID), limit.LimitKey, limit.Label,
			limit.UsedPercent, limit.WindowMinutes, limit.ResetAt, limit.RawJSON).Scan(&limit.ID); err != nil {
			return fmt.Errorf("creating account usage extra limit: %w", err)
		}
	}
	return nil
}

func (r *UsageRepo) attachAccountUsageExtraLimits(ctx context.Context, snapshots []models.AccountUsageSnapshot) error {
	if len(snapshots) == 0 {
		return nil
	}
	byID := make(map[string]*models.AccountUsageSnapshot, len(snapshots))
	ids := make([]string, 0, len(snapshots))
	for i := range snapshots {
		if snapshots[i].ID == "" {
			continue
		}
		byID[snapshots[i].ID] = &snapshots[i]
		ids = append(ids, snapshots[i].ID)
	}
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, snapshot_id, provider, COALESCE(account_id, ''), COALESCE(agent_config_id, ''),
		       limit_key, label, used_percent, window_minutes, reset_at, raw_json
		FROM account_usage_extra_limits
		WHERE snapshot_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY snapshot_id, rowid`, args...)
	if err != nil {
		return fmt.Errorf("listing account usage extra limits: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		limit, err := scanAccountUsageExtraLimit(rows)
		if err != nil {
			return err
		}
		if snapshot := byID[limit.SnapshotID]; snapshot != nil {
			snapshot.ExtraLimits = append(snapshot.ExtraLimits, limit)
		}
	}
	return rows.Err()
}

func scanAccountUsageExtraLimit(scanner interface{ Scan(dest ...any) error }) (models.AccountUsageExtraLimit, error) {
	var limit models.AccountUsageExtraLimit
	var used sql.NullFloat64
	var window sql.NullInt64
	var reset sql.NullString
	if err := scanner.Scan(&limit.ID, &limit.SnapshotID, &limit.Provider, &limit.AccountID, &limit.AgentConfigID, &limit.LimitKey, &limit.Label, &used, &window, &reset, &limit.RawJSON); err != nil {
		return limit, fmt.Errorf("scanning account usage extra limit: %w", err)
	}
	if used.Valid {
		limit.UsedPercent = &used.Float64
	}
	if window.Valid {
		v := int(window.Int64)
		limit.WindowMinutes = &v
	}
	if reset.Valid {
		limit.ResetAt = &reset.String
	}
	return limit, nil
}

func scanAccountUsageSnapshot(scanner interface{ Scan(dest ...any) error }) (models.AccountUsageSnapshot, error) {
	var snapshot models.AccountUsageSnapshot
	var credits, extraMonthly, extraUsed sql.NullFloat64
	var primaryUsed, secondaryUsed, modelUsed sql.NullFloat64
	var primaryWindow, secondaryWindow, modelWindow sql.NullInt64
	var primaryReset, secondaryReset, modelReset sql.NullString
	var fetchedRaw, createdRaw string
	if err := scanner.Scan(
		&snapshot.ID, &snapshot.Provider, &snapshot.AccountID, &snapshot.AgentConfigID, &snapshot.PlanType,
		&snapshot.AccountDisplayName, &snapshot.AccountDetail, &snapshot.BillingLabel, &snapshot.SubscriptionStatus, &snapshot.ExtraUsageLabel, &extraMonthly, &extraUsed,
		&credits,
		&snapshot.PrimaryLabel, &primaryUsed, &primaryWindow, &primaryReset, &snapshot.SecondaryLabel, &secondaryUsed, &secondaryWindow, &secondaryReset,
		&snapshot.ModelLimitLabel, &modelUsed, &modelWindow, &modelReset,
		&snapshot.RateLimitReachedType, &snapshot.RawJSON, &fetchedRaw, &createdRaw,
	); err != nil {
		return snapshot, fmt.Errorf("scanning account usage snapshot: %w", err)
	}
	snapshot.FetchedAt = parseSQLiteTime(fetchedRaw)
	snapshot.CreatedAt = parseSQLiteTime(createdRaw)
	if credits.Valid {
		snapshot.CreditsRemaining = &credits.Float64
	}
	if extraMonthly.Valid {
		snapshot.ExtraUsageMonthlyUSD = &extraMonthly.Float64
	}
	if extraUsed.Valid {
		snapshot.ExtraUsageUsedUSD = &extraUsed.Float64
	}
	if primaryUsed.Valid {
		snapshot.PrimaryUsedPercent = &primaryUsed.Float64
	}
	if secondaryUsed.Valid {
		snapshot.SecondaryUsedPercent = &secondaryUsed.Float64
	}
	if modelUsed.Valid {
		snapshot.ModelLimitUsedPercent = &modelUsed.Float64
	}
	if primaryWindow.Valid {
		v := int(primaryWindow.Int64)
		snapshot.PrimaryWindowMinutes = &v
	}
	if secondaryWindow.Valid {
		v := int(secondaryWindow.Int64)
		snapshot.SecondaryWindowMinutes = &v
	}
	if modelWindow.Valid {
		v := int(modelWindow.Int64)
		snapshot.ModelLimitWindowMinutes = &v
	}
	if primaryReset.Valid {
		snapshot.PrimaryResetsAt = &primaryReset.String
	}
	if secondaryReset.Valid {
		snapshot.SecondaryResetsAt = &secondaryReset.String
	}
	if modelReset.Valid {
		snapshot.ModelLimitResetsAt = &modelReset.String
	}
	return snapshot, nil
}

func nullStringArg(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func formatSQLiteTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05")
}

func parseSQLiteTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339} {
		if t, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}
