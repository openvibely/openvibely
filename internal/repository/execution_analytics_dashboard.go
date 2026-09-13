package repository

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// AnalyticsDashboardFilter applies one authoritative project and a half-open
// [DateFrom, DateTo) window to every period-sensitive dashboard query.
type AnalyticsDashboardFilter struct {
	ProjectID string
	DateFrom  time.Time
	DateTo    time.Time
	Compare   bool
	Limit     int
}

var analyticsMetricDefinitions = []models.MetricDefinition{
	{Key: "technical_completion", Label: "Technical completion rate", Definition: "Completed terminal executions divided by all terminal executions; cancelled executions remain visible in the denominator.", Denominator: "Completed, failed, and cancelled executions in the selected period."},
	{Key: "goal_achievement", Label: "Goal achievement rate", Definition: "Tasks with an achieved persisted goal divided by goal-bearing tasks that reached an evaluable task or goal state.", Denominator: "Non-cleared goal-bearing tasks whose task is terminal or whose goal is achieved or failed."},
	{Key: "first_pass", Label: "Technical first-pass rate", Definition: "Tasks whose first terminal execution completed divided by tasks with at least one terminal execution.", Denominator: "Tasks with a completed, failed, or cancelled execution in the selected period."},
	{Key: "follow_up", Label: "Follow-up rate", Definition: "Tasks with at least one follow-up execution divided by tasks with at least one execution.", Denominator: "Tasks with an execution in the selected period."},
	{Key: "median_cycle_time", Label: "Median task cycle time", Definition: "Median elapsed time from a task's first execution start to its latest terminal execution in the selected period.", Denominator: "Tasks with both a first start and a terminal execution in the selected period."},
	{Key: "known_cost_per_achieved_goal", Label: "Known cost per achieved goal", Definition: "Recorded cost associated with achieved-goal tasks divided only by achieved-goal tasks represented by that cost.", Denominator: "Achieved-goal tasks with at least one usage event containing recorded cost; coverage is disclosed separately."},
}

func analyticsWindowClause(alias string, filter AnalyticsDashboardFilter) (string, []any) {
	clause := ""
	args := []any{}
	if !filter.DateFrom.IsZero() {
		clause += " AND " + alias + ".started_at >= ?"
		args = append(args, filter.DateFrom.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	if !filter.DateTo.IsZero() {
		clause += " AND " + alias + ".started_at < ?"
		args = append(args, filter.DateTo.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	return clause, args
}

func analyticsEventWindowClause(alias, column string, filter AnalyticsDashboardFilter) (string, []any) {
	clause := ""
	args := []any{}
	if !filter.DateFrom.IsZero() {
		clause += " AND " + alias + "." + column + " >= ?"
		args = append(args, filter.DateFrom.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	if !filter.DateTo.IsZero() {
		clause += " AND " + alias + "." + column + " < ?"
		args = append(args, filter.DateTo.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	return clause, args
}

func analyticsTaskWindowClause(alias string, filter AnalyticsDashboardFilter) (string, []any) {
	clause := ""
	args := []any{}
	if !filter.DateFrom.IsZero() {
		clause += " AND " + alias + ".created_at >= ?"
		args = append(args, filter.DateFrom.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	if !filter.DateTo.IsZero() {
		clause += " AND " + alias + ".created_at < ?"
		args = append(args, filter.DateTo.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	return clause, args
}

func metric(numerator, denominator int) models.AnalyticsMetric {
	m := models.AnalyticsMetric{Numerator: numerator, Denominator: denominator, SampleSize: denominator}
	if denominator > 0 {
		m.Percent = float64(numerator) * 100 / float64(denominator)
	}
	return m
}

func (r *ExecutionRepo) GetAnalyticsDashboard(ctx context.Context, filter AnalyticsDashboardFilter) (models.AnalyticsDashboard, error) {
	if strings.TrimSpace(filter.ProjectID) == "" {
		return models.AnalyticsDashboard{}, fmt.Errorf("analytics project_id is required")
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	dashboard := models.AnalyticsDashboard{
		Definitions:          append([]models.MetricDefinition(nil), analyticsMetricDefinitions...),
		Funnel:               []models.OutcomeFunnelStage{},
		CycleDistribution:    []models.AnalyticsDistributionPoint{},
		FollowUpDistribution: []models.AnalyticsDistributionPoint{},
		Agents:               []models.AgentPerformance{},
		SkillOutcomes:        []models.SkillOutcomePerformance{},
		Workflows:            []models.WorkflowPerformance{},
		RecentOutcomes:       []models.EvidenceTaskRow{},
		Insights:             []models.AnalyticsInsight{},
	}
	current, cycles, followups, err := r.queryOutcomeMetrics(ctx, filter)
	if err != nil {
		return dashboard, err
	}
	dashboard.Current = current
	dashboard.CycleDistribution = cycleDistribution(cycles)
	dashboard.FollowUpDistribution = followUpDistribution(followups)
	if filter.Compare && !filter.DateFrom.IsZero() && !filter.DateTo.IsZero() && filter.DateTo.After(filter.DateFrom) {
		duration := filter.DateTo.Sub(filter.DateFrom)
		previousFilter := filter
		previousFilter.DateTo = filter.DateFrom
		previousFilter.DateFrom = filter.DateFrom.Add(-duration)
		previousFilter.Compare = false
		previous, _, _, queryErr := r.queryOutcomeMetrics(ctx, previousFilter)
		if queryErr != nil {
			return dashboard, queryErr
		}
		dashboard.Previous = &previous
	}
	if dashboard.Funnel, err = r.queryOutcomeFunnel(ctx, filter); err != nil {
		return dashboard, err
	}
	if dashboard.Agents, err = r.queryAgentPerformance(ctx, filter); err != nil {
		return dashboard, err
	}
	if dashboard.SkillOutcomes, err = r.querySkillOutcomePerformance(ctx, filter); err != nil {
		return dashboard, err
	}
	if dashboard.Workflows, err = r.queryWorkflowPerformance(ctx, filter); err != nil {
		return dashboard, err
	}
	if dashboard.RecentOutcomes, err = r.queryRecentOutcomes(ctx, filter); err != nil {
		return dashboard, err
	}
	dashboard.Insights = buildAnalyticsInsights(dashboard.Current, dashboard.Previous, dashboard.Workflows, filter)
	return dashboard, nil
}

func (r *ExecutionRepo) queryOutcomeMetrics(ctx context.Context, filter AnalyticsDashboardFilter) (models.OutcomeMetrics, []int64, []int, error) {
	out := models.OutcomeMetrics{}
	window, windowArgs := analyticsWindowClause("e", filter)
	query := `WITH period_exec AS (
			SELECT e.* FROM executions e JOIN tasks t ON t.id=e.task_id
			WHERE t.project_id=?` + window + `
		), period_terminal AS (
			SELECT * FROM period_exec WHERE status IN ('completed','failed','cancelled')
		), task_stats AS (
			SELECT task_id, COUNT(*) execution_count, SUM(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followups,
				MIN(started_at) first_started,
				MAX(CASE WHEN status IN ('completed','failed','cancelled') THEN COALESCE(completed_at,started_at) END) terminal_at
			FROM period_exec GROUP BY task_id
		), first_terminal AS (
			SELECT e.task_id,e.status,ROW_NUMBER() OVER(PARTITION BY e.task_id ORDER BY e.started_at,e.history_order,e.id) rn
			FROM executions e JOIN task_stats s ON s.task_id=e.task_id WHERE e.status IN ('completed','failed','cancelled')
		)
		SELECT
			(SELECT COUNT(*) FROM period_terminal WHERE status='completed'),
			(SELECT COUNT(*) FROM period_terminal),
			(SELECT COUNT(*) FROM first_terminal WHERE rn=1 AND status='completed'),
			(SELECT COUNT(*) FROM first_terminal WHERE rn=1),
		(SELECT COUNT(*) FROM task_stats WHERE followups>0),
		(SELECT COUNT(*) FROM task_stats),
		(SELECT COUNT(*) FROM task_goals g JOIN tasks t ON t.id=g.task_id JOIN task_stats s ON s.task_id=t.id WHERE g.status='achieved'),
		(SELECT COUNT(*) FROM task_goals g JOIN tasks t ON t.id=g.task_id JOIN task_stats s ON s.task_id=t.id WHERE g.status<>'cleared' AND (g.status IN ('achieved','failed') OR t.status IN ('completed','failed','cancelled')))`
	args := append([]any{filter.ProjectID}, windowArgs...)
	var technicalCompleted, terminal, firstCompleted, firstTerminal, followed, tasks, achieved, evaluable int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&technicalCompleted, &terminal, &firstCompleted, &firstTerminal, &followed, &tasks, &achieved, &evaluable); err != nil {
		return out, nil, nil, fmt.Errorf("getting analytics outcome metrics: %w", err)
	}
	out.TechnicalCompletion = metric(technicalCompleted, terminal)
	out.FirstPass = metric(firstCompleted, firstTerminal)
	out.FollowUp = metric(followed, tasks)
	out.GoalAchievement = metric(achieved, evaluable)
	out.TasksEvaluated = tasks

	cycleQuery := `SELECT CAST(MAX(0, (julianday(MAX(CASE WHEN e.status IN ('completed','failed','cancelled') THEN COALESCE(e.completed_at,e.started_at) END))-julianday(MIN(e.started_at)))*86400000) AS INTEGER),
		SUM(CASE WHEN e.is_followup=1 THEN 1 ELSE 0 END)
		FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + window + ` GROUP BY e.task_id
		HAVING MAX(CASE WHEN e.status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END)=1`
	rows, err := r.db.QueryContext(ctx, cycleQuery, args...)
	if err != nil {
		return out, nil, nil, fmt.Errorf("getting analytics task distributions: %w", err)
	}
	cycles := []int64{}
	followups := []int{}
	for rows.Next() {
		var cycle int64
		var count int
		if err := rows.Scan(&cycle, &count); err != nil {
			rows.Close()
			return out, nil, nil, fmt.Errorf("scanning analytics task distributions: %w", err)
		}
		cycles = append(cycles, cycle)
		followups = append(followups, count)
	}
	if err := rows.Close(); err != nil {
		return out, nil, nil, err
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i] < cycles[j] })
	out.MedianCycleTimeMs = percentileInt64(cycles, 0.5)
	out.P90CycleTimeMs = percentileInt64(cycles, 0.9)
	if err := r.queryOutcomeCosts(ctx, filter, &out); err != nil {
		return out, nil, nil, err
	}
	return out, cycles, followups, nil
}

func (r *ExecutionRepo) queryOutcomeCosts(ctx context.Context, filter AnalyticsDashboardFilter, out *models.OutcomeMetrics) error {
	window, windowArgs := analyticsWindowClause("e", filter)
	usageWindow, usageWindowArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	query := `WITH period_tasks AS (
		SELECT e.task_id,MAX(CASE WHEN e.status='completed' THEN 1 ELSE 0 END) technical_completed
		FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + window + ` GROUP BY e.task_id
	), task_usage AS (
		SELECT u.task_id, SUM(u.cost_usd) known_cost, SUM(u.total_tokens) tokens,
			MAX(CASE WHEN u.cost_usd IS NOT NULL THEN 1 ELSE 0 END) has_cost, COUNT(*) has_usage
			FROM llm_usage_events u JOIN period_tasks p ON p.task_id=u.task_id
			WHERE u.project_id=?` + usageWindow + ` GROUP BY u.task_id
	)
	SELECT
		COALESCE(SUM(CASE WHEN g.status='achieved' AND u.has_cost=1 THEN u.known_cost ELSE 0 END),0),
		COUNT(DISTINCT CASE WHEN g.status='achieved' AND u.has_cost=1 THEN p.task_id END),
		COUNT(DISTINCT CASE WHEN g.status='achieved' THEN p.task_id END),
		COALESCE(SUM(CASE WHEN g.status='achieved' AND u.has_usage>0 THEN u.tokens ELSE 0 END),0),
		COUNT(DISTINCT CASE WHEN g.status='achieved' AND u.has_usage>0 THEN p.task_id END),
		COALESCE(SUM(CASE WHEN p.technical_completed=1 AND u.has_cost=1 THEN u.known_cost ELSE 0 END),0),
		COUNT(DISTINCT CASE WHEN p.technical_completed=1 AND u.has_cost=1 THEN p.task_id END),
		COUNT(DISTINCT CASE WHEN p.technical_completed=1 THEN p.task_id END)
	FROM period_tasks p LEFT JOIN task_goals g ON g.task_id=p.task_id LEFT JOIN task_usage u ON u.task_id=p.task_id`
	args := append([]any{filter.ProjectID}, windowArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, usageWindowArgs...)
	var achievedCost, achievedTokens, completedCost float64
	var achievedCovered, achievedEligible, tokenCovered, completedCovered, completedEligible int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&achievedCost, &achievedCovered, &achievedEligible, &achievedTokens, &tokenCovered, &completedCost, &completedCovered, &completedEligible); err != nil {
		return fmt.Errorf("getting analytics cost coverage: %w", err)
	}
	if achievedCovered > 0 {
		out.KnownCostPerAchievedGoal = &models.CostCoverage{Value: achievedCost / float64(achievedCovered), Covered: achievedCovered, Eligible: achievedEligible}
	}
	if tokenCovered > 0 {
		out.TokensPerAchievedGoal = &models.CostCoverage{Value: achievedTokens / float64(tokenCovered), Covered: tokenCovered, Eligible: achievedEligible}
	}
	if completedCovered > 0 {
		out.KnownCostPerCompletedTask = &models.CostCoverage{Value: completedCost / float64(completedCovered), Covered: completedCovered, Eligible: completedEligible}
	}
	failedQuery := `SELECT COALESCE(SUM(u.cost_usd),0) FROM llm_usage_events u JOIN executions e ON e.id=u.execution_id JOIN tasks t ON t.id=e.task_id WHERE t.project_id=? AND e.status='failed'` + window + usageWindow
	failedArgs := append([]any{filter.ProjectID}, windowArgs...)
	failedArgs = append(failedArgs, usageWindowArgs...)
	if err := r.db.QueryRowContext(ctx, failedQuery, failedArgs...).Scan(&out.KnownFailedExecutionCost); err != nil {
		return fmt.Errorf("getting known failed execution cost: %w", err)
	}
	return nil
}

func percentileInt64(sortedValues []int64, percentile float64) int64 {
	if len(sortedValues) == 0 {
		return 0
	}
	index := int(math.Ceil(float64(len(sortedValues))*percentile)) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sortedValues) {
		index = len(sortedValues) - 1
	}
	return sortedValues[index]
}

func cycleDistribution(cycles []int64) []models.AnalyticsDistributionPoint {
	if len(cycles) == 0 {
		return []models.AnalyticsDistributionPoint{}
	}
	points := []models.AnalyticsDistributionPoint{{Label: "< 1m"}, {Label: "1-10m"}, {Label: "10-60m"}, {Label: "1-24h"}, {Label: ">= 24h"}}
	for _, value := range cycles {
		switch {
		case value < 60000:
			points[0].Count++
		case value < 600000:
			points[1].Count++
		case value < 3600000:
			points[2].Count++
		case value < 86400000:
			points[3].Count++
		default:
			points[4].Count++
		}
	}
	return points
}

func followUpDistribution(values []int) []models.AnalyticsDistributionPoint {
	if len(values) == 0 {
		return []models.AnalyticsDistributionPoint{}
	}
	points := []models.AnalyticsDistributionPoint{{Label: "0"}, {Label: "1"}, {Label: "2"}, {Label: "3+"}}
	for _, value := range values {
		if value >= 3 {
			points[3].Count++
		} else {
			points[value].Count++
		}
	}
	return points
}

func (r *ExecutionRepo) queryOutcomeFunnel(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.OutcomeFunnelStage, error) {
	taskWindow, taskArgs := analyticsTaskWindowClause("t", filter)
	execWindow, execArgs := analyticsWindowClause("e", filter)
	query := `SELECT
		(SELECT COUNT(*) FROM tasks t WHERE t.project_id=?` + taskWindow + `),
		(SELECT COUNT(DISTINCT e.task_id) FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + execWindow + `),
		(SELECT COUNT(DISTINCT e.task_id) FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=? AND e.status='completed'` + execWindow + `),
		(SELECT COUNT(DISTINCT g.task_id) FROM task_goals g JOIN tasks t ON t.id=g.task_id WHERE t.project_id=? AND g.status='achieved' AND EXISTS (SELECT 1 FROM executions e WHERE e.task_id=t.id` + execWindow + `)),
		(SELECT COUNT(DISTINCT g.task_id) FROM task_goals g JOIN tasks t ON t.id=g.task_id WHERE t.project_id=? AND g.status<>'cleared' AND (g.status IN ('achieved','failed') OR t.status IN ('completed','failed','cancelled')) AND EXISTS (SELECT 1 FROM executions e WHERE e.task_id=t.id` + execWindow + `)),
		(SELECT COUNT(*) FROM tasks t WHERE t.project_id=? AND t.worktree_path<>''` + taskWindow + `),
		(SELECT COUNT(*) FROM tasks t WHERE t.project_id=? AND t.worktree_path<>'' AND t.merge_status='merged'` + taskWindow + `)`
	args := []any{filter.ProjectID}
	args = append(args, taskArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, execArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, execArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, execArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, execArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, taskArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, taskArgs...)
	var created, started, completed, achieved, goalEligible, mergeEligible, merged int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&created, &started, &completed, &achieved, &goalEligible, &mergeEligible, &merged); err != nil {
		return nil, fmt.Errorf("getting analytics outcome funnel: %w", err)
	}
	return []models.OutcomeFunnelStage{
		{Key: "created", Label: "Tasks created", Count: created, Denominator: created},
		{Key: "started", Label: "Tasks started", Count: started, Denominator: created},
		{Key: "technical_completed", Label: "Technical execution completed", Count: completed, Denominator: started},
		{Key: "goal_achieved", Label: "Goal achieved", Count: achieved, Denominator: goalEligible},
		{Key: "merged", Label: "Merged eligible worktree tasks", Count: merged, Denominator: mergeEligible},
	}, nil
}

func (r *ExecutionRepo) queryAgentPerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.AgentPerformance, error) {
	window, windowArgs := analyticsWindowClause("e", filter)
	usageWindow, usageWindowArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	query := `WITH period_exec AS (
		SELECT e.*, t.agent_definition_id FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + window + `
	), period_task_ids AS (
		SELECT DISTINCT task_id FROM period_exec
	), historical_terminal AS (
		SELECT e.task_id,e.status,ROW_NUMBER() OVER(PARTITION BY e.task_id ORDER BY e.started_at,e.history_order,e.id) rn
		FROM executions e JOIN period_task_ids p ON p.task_id=e.task_id WHERE e.status IN ('completed','failed','cancelled')
	), task_stats AS (
		SELECT p.task_id,p.agent_definition_id,COUNT(*) executions,SUM(CASE WHEN p.is_followup=1 THEN 1 ELSE 0 END) followups,
		SUM(CASE WHEN p.status='completed' THEN 1 ELSE 0 END) completed_execs,
		SUM(CASE WHEN p.status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal_execs,
		MAX(CASE WHEN f.rn=1 AND f.status='completed' THEN 1 ELSE 0 END) first_completed,
		MAX(CASE WHEN f.rn=1 THEN 1 ELSE 0 END) has_first,
		CAST(MAX(0,(julianday(MAX(CASE WHEN p.status IN ('completed','failed','cancelled') THEN COALESCE(p.completed_at,p.started_at) END))-julianday(MIN(p.started_at)))*86400000) AS INTEGER) duration_ms
		FROM period_exec p LEFT JOIN historical_terminal f ON f.task_id=p.task_id AND f.rn=1 GROUP BY p.task_id,p.agent_definition_id
	), agent_rollup AS (
		SELECT s.agent_definition_id,COUNT(*) tasks_evaluated,SUM(completed_execs) completed_execs,SUM(terminal_execs) terminal_execs,
		SUM(first_completed) first_completed,SUM(has_first) first_denominator,SUM(CASE WHEN followups>0 THEN 1 ELSE 0 END) followed,
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END) achieved,
		SUM(CASE WHEN g.status<>'cleared' AND (g.status IN ('achieved','failed') OR t.status IN ('completed','failed','cancelled')) THEN 1 ELSE 0 END) goal_denominator
		FROM task_stats s JOIN tasks t ON t.id=s.task_id LEFT JOIN task_goals g ON g.task_id=s.task_id GROUP BY s.agent_definition_id
	), ranked_durations AS (
		SELECT agent_definition_id,duration_ms,ROW_NUMBER() OVER(PARTITION BY agent_definition_id ORDER BY duration_ms) rn,
		COUNT(*) OVER(PARTITION BY agent_definition_id) duration_count FROM task_stats WHERE duration_ms IS NOT NULL
	), medians AS (
		SELECT agent_definition_id,CAST(AVG(duration_ms) AS INTEGER) median_duration_ms FROM ranked_durations
		WHERE rn IN ((duration_count+1)/2,(duration_count+2)/2) GROUP BY agent_definition_id
	), model_counts AS (
		SELECT agent_definition_id,agent_config_id,COUNT(*) use_count FROM period_exec GROUP BY agent_definition_id,agent_config_id
	), ranked_models AS (
		SELECT agent_definition_id,agent_config_id,ROW_NUMBER() OVER(PARTITION BY agent_definition_id ORDER BY use_count DESC,agent_config_id) rn FROM model_counts
	), usage AS (
		SELECT u.task_id,SUM(u.cost_usd) known_cost,MAX(CASE WHEN u.cost_usd IS NOT NULL THEN 1 ELSE 0 END) has_cost
		FROM llm_usage_events u JOIN period_task_ids p ON p.task_id=u.task_id WHERE u.project_id=?` + usageWindow + ` GROUP BY u.task_id
	), costs AS (
		SELECT s.agent_definition_id,COALESCE(SUM(CASE WHEN u.has_cost=1 THEN u.known_cost ELSE 0 END),0) known_cost,
		COUNT(DISTINCT CASE WHEN u.has_cost=1 THEN s.task_id END) covered,COUNT(DISTINCT s.task_id) eligible
		FROM task_stats s JOIN task_goals g ON g.task_id=s.task_id AND g.status='achieved' LEFT JOIN usage u ON u.task_id=s.task_id GROUP BY s.agent_definition_id
	)
	SELECT COALESCE(a.id,''),COALESCE(a.name,'Unassigned / Auto-routed'),r.tasks_evaluated,
		r.completed_execs,r.terminal_execs,r.first_completed,r.first_denominator,r.followed,r.achieved,r.goal_denominator,
		COALESCE(m.median_duration_ms,0),COALESCE(ac.name || ' (' || ac.model || ')',rm.agent_config_id,'Unknown'),
		COALESCE(c.known_cost,0),COALESCE(c.covered,0),COALESCE(c.eligible,0)
	FROM agent_rollup r LEFT JOIN agents a ON a.id=r.agent_definition_id
	LEFT JOIN medians m ON m.agent_definition_id IS r.agent_definition_id
	LEFT JOIN ranked_models rm ON rm.agent_definition_id IS r.agent_definition_id AND rm.rn=1
	LEFT JOIN agent_configs ac ON ac.id=rm.agent_config_id LEFT JOIN costs c ON c.agent_definition_id IS r.agent_definition_id
	ORDER BY CASE WHEN a.id IS NULL THEN 1 ELSE 0 END,r.tasks_evaluated DESC,a.name`
	args := append([]any{filter.ProjectID}, windowArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, usageWindowArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting agent performance: %w", err)
	}
	defer rows.Close()
	result := []models.AgentPerformance{}
	for rows.Next() {
		var row models.AgentPerformance
		var completed, terminal, firstCompleted, firstDenom, followed, achieved, goalDenom int
		var knownCost float64
		var costCovered, costEligible int
		if err := rows.Scan(&row.AgentID, &row.AgentName, &row.TasksEvaluated, &completed, &terminal, &firstCompleted, &firstDenom, &followed, &achieved, &goalDenom, &row.MedianDurationMs, &row.MostUsedModel, &knownCost, &costCovered, &costEligible); err != nil {
			return nil, fmt.Errorf("scanning agent performance: %w", err)
		}
		row.TechnicalCompletion = metric(completed, terminal)
		row.FirstPass = metric(firstCompleted, firstDenom)
		row.FollowUp = metric(followed, row.TasksEvaluated)
		row.GoalAchievement = metric(achieved, goalDenom)
		if costCovered > 0 {
			row.KnownCostPerAchievedGoal = &models.CostCoverage{Value: knownCost / float64(costCovered), Covered: costCovered, Eligible: costEligible}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) querySkillOutcomePerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.SkillOutcomePerformance, error) {
	eventWindow, eventArgs := analyticsEventWindowClause("s", "created_at", filter)
	execWindow, execArgs := analyticsWindowClause("e", filter)
	query := `WITH skill_tasks AS (
		SELECT DISTINCT s.skill_handle,s.task_id FROM skill_analytics_events s JOIN tasks t ON t.id=s.task_id
		WHERE t.project_id=? AND s.event_type IN ('selected','loaded') AND s.task_id IS NOT NULL AND s.task_id<>''` + eventWindow + `
	), period_exec AS (
		SELECT st.skill_handle,e.* FROM skill_tasks st JOIN executions e ON e.task_id=st.task_id WHERE 1=1` + execWindow + `
	), task_stats AS (
		SELECT skill_handle,task_id,MAX(CASE WHEN status='completed' THEN 1 ELSE 0 END) completed,
		MAX(CASE WHEN status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal,
		MAX(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followed FROM period_exec GROUP BY skill_handle,task_id
	)
	SELECT s.skill_handle,COUNT(*),SUM(s.completed),SUM(s.terminal),SUM(s.followed),
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END),
		SUM(CASE WHEN g.status<>'cleared' AND (g.status IN ('achieved','failed') OR t.status IN ('completed','failed','cancelled')) THEN 1 ELSE 0 END)
	FROM task_stats s JOIN tasks t ON t.id=s.task_id LEFT JOIN task_goals g ON g.task_id=s.task_id
	GROUP BY s.skill_handle ORDER BY COUNT(*) DESC,s.skill_handle LIMIT 20`
	args := append([]any{filter.ProjectID}, eventArgs...)
	args = append(args, execArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting skill outcome performance: %w", err)
	}
	defer rows.Close()
	result := []models.SkillOutcomePerformance{}
	for rows.Next() {
		var row models.SkillOutcomePerformance
		var completed, terminal, followed, achieved, goalDenominator int
		if err := rows.Scan(&row.SkillHandle, &row.TasksEvaluated, &completed, &terminal, &followed, &achieved, &goalDenominator); err != nil {
			return nil, fmt.Errorf("scanning skill outcome performance: %w", err)
		}
		row.TechnicalCompletion = metric(completed, terminal)
		row.GoalAchievement = metric(achieved, goalDenominator)
		row.FollowUp = metric(followed, row.TasksEvaluated)
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryWorkflowPerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.WorkflowPerformance, error) {
	invocationWindow := ""
	args := []any{filter.ProjectID}
	if !filter.DateFrom.IsZero() {
		invocationWindow += " AND i.created_at>=?"
		args = append(args, filter.DateFrom.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	if !filter.DateTo.IsZero() {
		invocationWindow += " AND i.created_at<?"
		args = append(args, filter.DateTo.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	query := `SELECT a.id,a.name,COUNT(DISTINCT i.id),COUNT(DISTINCT CASE WHEN i.status='completed' THEN i.id END),COUNT(DISTINCT CASE WHEN i.status='failed' THEN i.id END),
		COALESCE(CAST(AVG(CASE WHEN i.completed_at IS NOT NULL AND i.started_at IS NOT NULL THEN (julianday(i.completed_at)-julianday(i.started_at))*86400000 END) AS INTEGER),0),
		(SELECT COUNT(*) FROM automation_work_items w WHERE w.project_id=a.project_id AND w.automation_id=a.id AND w.status='waiting'),
		(SELECT COUNT(*) FROM automation_work_items w WHERE w.project_id=a.project_id AND w.automation_id=a.id AND w.status='blocked'),
		a.health_state
	FROM automations a LEFT JOIN automation_invocations i ON i.project_id=a.project_id AND i.automation_id=a.id` + invocationWindow + `
	WHERE a.project_id=? AND a.lifecycle_state<>'archived' GROUP BY a.id,a.name,a.health_state ORDER BY COUNT(DISTINCT i.id) DESC,a.name`
	// JOIN arguments occur before the WHERE project argument.
	args = append(args[1:], filter.ProjectID)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting workflow performance: %w", err)
	}
	defer rows.Close()
	result := []models.WorkflowPerformance{}
	for rows.Next() {
		var row models.WorkflowPerformance
		if err := rows.Scan(&row.WorkflowID, &row.WorkflowName, &row.InvocationCount, &row.CompletedCount, &row.FailedCount, &row.AverageDurationMs, &row.WaitingCount, &row.BlockedCount, &row.Health); err != nil {
			return nil, err
		}
		terminal := row.CompletedCount + row.FailedCount
		if terminal > 0 {
			row.CompletionRate = float64(row.CompletedCount) * 100 / float64(terminal)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryRecentOutcomes(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.EvidenceTaskRow, error) {
	window, windowArgs := analyticsWindowClause("e", filter)
	usageWindow, usageWindowArgs := analyticsEventWindowClause("llm_usage_events", "occurred_at", filter)
	query := `WITH period_exec AS (SELECT e.* FROM executions e JOIN tasks x ON x.id=e.task_id WHERE x.project_id=?` + window + `),
	usage AS (SELECT task_id,SUM(cost_usd) cost,MAX(CASE WHEN cost_usd IS NOT NULL THEN 1 ELSE 0 END) known FROM llm_usage_events WHERE project_id=?` + usageWindow + ` GROUP BY task_id)
	SELECT t.id,t.title,COALESCE((SELECT pe.status FROM period_exec pe WHERE pe.task_id=t.id ORDER BY pe.started_at DESC,pe.history_order DESC,pe.id DESC LIMIT 1),t.status),COALESCE(g.status,''),t.merge_status,COALESCE(a.id,''),COALESCE(a.name,'Unassigned / Auto-routed'),
		COALESCE((SELECT ac.name || ' (' || ac.model || ')' FROM period_exec pe LEFT JOIN agent_configs ac ON ac.id=pe.agent_config_id WHERE pe.task_id=t.id ORDER BY pe.started_at DESC,pe.history_order DESC LIMIT 1),'Unknown'),
		COUNT(p.id),SUM(CASE WHEN p.is_followup=1 THEN 1 ELSE 0 END),
		CAST(MAX(0,(julianday(MAX(CASE WHEN p.status IN ('completed','failed','cancelled') THEN COALESCE(p.completed_at,p.started_at) END))-julianday(MIN(p.started_at)))*86400000) AS INTEGER),
		u.cost,u.known
	FROM tasks t JOIN period_exec p ON p.task_id=t.id LEFT JOIN task_goals g ON g.task_id=t.id LEFT JOIN agents a ON a.id=t.agent_definition_id LEFT JOIN usage u ON u.task_id=t.id
	GROUP BY t.id ORDER BY MAX(p.started_at) DESC,t.id DESC LIMIT ?`
	args := append([]any{filter.ProjectID}, windowArgs...)
	args = append(args, filter.ProjectID)
	args = append(args, usageWindowArgs...)
	args = append(args, filter.Limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting recent analytics outcomes: %w", err)
	}
	defer rows.Close()
	result := []models.EvidenceTaskRow{}
	for rows.Next() {
		var row models.EvidenceTaskRow
		var cost sql.NullFloat64
		var known sql.NullInt64
		if err := rows.Scan(&row.TaskID, &row.TaskTitle, &row.TechnicalResult, &row.GoalResult, &row.MergeState, &row.AgentID, &row.AgentName, &row.Model, &row.ExecutionCount, &row.FollowUpCount, &row.CycleTimeMs, &cost, &known); err != nil {
			return nil, err
		}
		if known.Valid && known.Int64 > 0 && cost.Valid {
			value := cost.Float64
			row.KnownCostUSD = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func buildAnalyticsInsights(current models.OutcomeMetrics, previous *models.OutcomeMetrics, workflows []models.WorkflowPerformance, filter AnalyticsDashboardFilter) []models.AnalyticsInsight {
	insights := []models.AnalyticsInsight{}
	if previous != nil {
		window := filter.DateFrom.Format("2006-01-02") + " to " + filter.DateTo.Format("2006-01-02")
		comparisons := []struct {
			key, label, view  string
			current, previous models.AnalyticsMetric
			lowerBetter       bool
		}{
			{"goal_achievement", "Goal achievement", "outcomes", current.GoalAchievement, previous.GoalAchievement, false},
			{"first_pass", "Technical first-pass rate", "agents", current.FirstPass, previous.FirstPass, false},
			{"follow_up", "Follow-up rate", "outcomes", current.FollowUp, previous.FollowUp, true},
		}
		for _, item := range comparisons {
			if item.current.Denominator < 5 || item.previous.Denominator < 5 {
				continue
			}
			delta := item.current.Percent - item.previous.Percent
			if math.Abs(delta) < 5 {
				continue
			}
			improved := delta > 0
			if item.lowerBetter {
				improved = delta < 0
			}
			kind := "attention"
			word := "changed"
			if improved {
				kind = "improvement"
				word = "improved"
			} else {
				word = "declined"
			}
			insights = append(insights, models.AnalyticsInsight{Kind: kind, Title: item.label + " " + word, Detail: fmt.Sprintf("%+.1f percentage points versus the immediately preceding equivalent period.", delta), MetricKey: item.key, CurrentPercent: item.current.Percent, PreviousPercent: item.previous.Percent, SampleSize: item.current.Denominator, ComparisonWindow: window, EvidenceView: item.view})
		}
	}
	for _, workflow := range workflows {
		if workflow.BlockedCount > 0 {
			insights = append(insights, models.AnalyticsInsight{Kind: "attention", Title: workflow.WorkflowName + " has blocked work", Detail: fmt.Sprintf("%d blocked work items in current workflow state.", workflow.BlockedCount), MetricKey: "workflow_blocked", SampleSize: workflow.InvocationCount, ComparisonWindow: "current state", EvidenceView: "workflows"})
		}
	}
	return insights
}
