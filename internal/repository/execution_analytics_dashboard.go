package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// AnalyticsDashboardFilter applies one authoritative project and a half-open
// [DateFrom, DateTo) window to every period-sensitive dashboard query.
type AnalyticsDashboardFilter struct {
	ProjectID            string
	DateFrom             time.Time
	DateTo               time.Time
	Compare              bool
	Limit                int
	EvidenceOffset       int
	GroupBy              string
	View                 string
	AgentID              string
	WorkflowID           string
	WorkType             string
	EvidenceSkillHandle  string
	EvidenceSkillScope   string
	EvidenceSkillAgentID string
}

var analyticsMetricDefinitions = []models.MetricDefinition{
	{Key: "technical_completion", Label: "Run success rate", Definition: "Successful runs divided by all finished runs; cancelled runs remain visible in the denominator.", Denominator: "Completed, failed, and cancelled runs in the selected period."},
	{Key: "goal_achievement", Label: "Goal achievement rate", Definition: "Tasks with an achieved persisted goal divided by goal-bearing tasks that reached an evaluable task or goal state.", Denominator: "Non-cleared goal-bearing tasks whose task is terminal or whose goal is achieved or failed."},
	{Key: "first_pass", Label: "First-run success rate", Definition: "Tasks whose first finished run succeeded divided by tasks with at least one finished run.", Denominator: "Tasks with a completed, failed, or cancelled run in the selected period."},
	{Key: "follow_up", Label: "Follow-up run rate", Definition: "Tasks with at least one follow-up run divided by tasks with at least one run; the distribution uses the same task cohort.", Denominator: "Tasks with a run in the selected period."},
	{Key: "median_cycle_time", Label: "Median task duration", Definition: "Median elapsed time from a task's first run start through its latest finished run in the selected period, including retries and follow-up runs.", Denominator: "Non-scheduled task instances with a finished run in the selected period and a persisted historical first run start; repeating task templates are excluded."},
	{Key: "known_cost_per_achieved_goal", Label: "Known cost per achieved goal", Definition: "Selected-period recorded cost associated with tasks whose goal was achieved in the period, divided only by achieved-goal tasks represented by that cost.", Denominator: "Tasks with a selected-period run, an achieved goal event in the period, and at least one selected-period usage event containing recorded cost; coverage is disclosed separately."},
	{Key: "known_cost_per_completed_task", Label: "Known cost per successful task", Definition: "Recorded task cost divided by successfully completed tasks represented by that cost.", Denominator: "Successfully completed tasks with recorded cost; cost coverage is disclosed."},
	{Key: "known_failed_execution_cost", Label: "Known failed-run cost", Definition: "Sum of recorded cost attached to failed runs.", Denominator: "Failed runs with recorded cost out of all failed runs; unavailable when none have recorded cost."},
	{Key: "cancelled_executions", Label: "Cancelled runs", Definition: "Runs explicitly cancelled in the selected period.", Denominator: "All finished runs in the selected period."},
	{Key: "cycle_time_p90", Label: "P90 task duration", Definition: "90th percentile elapsed time from a task's first run start through its latest finished run in the selected period, including retries and follow-up runs.", Denominator: "Non-scheduled task instances with a finished run in the selected period and a persisted historical first run start; repeating task templates are excluded."},
	{Key: "tokens_per_achieved_goal", Label: "Tokens per achieved goal", Definition: "Recorded tokens associated with achieved-goal tasks divided by represented achieved goals.", Denominator: "Achieved-goal tasks with usage records; coverage is disclosed."},
	{Key: "agent_performance", Label: "Agent performance", Definition: "Current task outcomes attributed through tasks.agent_definition_id. Run time sums finished runs started in the selected period, excluding idle gaps; scheduled tasks are included and chats excluded.", Denominator: "Tasks with runs or task usage in the selected period, assigned to each reusable Agent definition. Goal rates use non-cleared goals; merge rates use merge-eligible tasks. Each metric discloses its sample."},
	{Key: "workflow_performance", Label: "Workflow performance", Definition: "Invocation and current work-item state for project-owned automations; selected-period invocation status counts are displayed as completed, failed, cancelled, skipped, or open, and terminal-duration sample size is disclosed.", Denominator: "All selected-period workflow invocations for completion rate; waiting and blocked values are explicitly current state."},
	{Key: "agent_skill_outcomes", Label: "Observed Agent and skill outcomes", Definition: "Task outcomes grouped by assigned reusable Agent definition and selected or loaded skill.", Denominator: "Selected-period tasks with run evidence and a selected or loaded skill event; association is observational, not causal."},
	{Key: "skill_outcomes", Label: "Observed skill outcomes", Definition: "Observed task outcomes where a skill was selected or loaded; this is association, not causation.", Denominator: "Selected-period tasks with a selected or loaded skill event and run evidence."},
	{Key: "model_category", Label: "Model performance by task category", Definition: "Run success grouped by configured model and task category.", Denominator: "Completed, failed, and cancelled runs in each model/category group during the selected period."},
	{Key: "model_performance", Label: "Model task outcomes", Definition: "Tasks whose latest run finished in the selected period, including failed and cancelled tasks, with their full recorded history. Mixed configurations are grouped separately. Goal achievement and merging are separate outcomes; follow-ups measure effort, not failure.", Denominator: "Goal rates include non-cleared goals; merge rates include tasks with worktrees or merge status. Duration is median elapsed first-start to latest-finish time, excluding recurring tasks. Usage averages include tasks with recorded tokens or cost; records may be incomplete."},
	{Key: "token_usage", Label: "Token usage", Definition: "Locally recorded provider input, output, cache, reasoning, and total token counts.", Denominator: "Usage events in the selected project and period with the applicable task dimensions."},
	{Key: "cache_utilization", Label: "Cache utilization", Definition: "Cached input tokens divided by recorded input tokens.", Denominator: "Recorded input tokens in the selected project and period."},
	{Key: "execution_hour", Label: "Runs by hour", Definition: "Run starts grouped by local hour of day.", Denominator: "Runs in the selected project, period, Agent, and workflow scope."},
	{Key: "duration_by_task", Label: "Run duration by task", Definition: "Average recorded duration of completed runs grouped by task.", Denominator: "Completed runs with positive duration in each task group; samples are shown in tooltips."},
	{Key: "duration_by_model", Label: "Run duration by model", Definition: "Average recorded duration of completed runs grouped by configured model.", Denominator: "Completed runs with positive duration in each model group; samples are shown in tooltips."},
	{Key: "model_execution_share", Label: "Model run breakdown", Definition: "Run count grouped by model configuration.", Denominator: "Runs in the selected project, period, Agent, and workflow scope."},
	{Key: "frequent_tasks", Label: "Most frequently run tasks", Definition: "Tasks ordered by run count.", Denominator: "Runs in the selected project, period, Agent, and workflow scope."},
	{Key: "failed_patterns", Label: "Failed task patterns", Definition: "Failed runs grouped by task with the latest stored error.", Denominator: "Failed runs in the selected project, period, Agent, and workflow scope."},
	{Key: "skill_activity", Label: "Skill activity", Definition: "Locally recorded selected, loaded, viewed, created, and edited skill events.", Denominator: "Skill events in the selected project, period, Agent, and workflow scope."},
	{Key: "skill_follow_through", Label: "Skill follow-through", Definition: "Selected skill events observed with or without a later loaded or viewed event.", Denominator: "Selected skill events in the selected filter scope; this does not measure causality."},
	{Key: "workflow_nodes", Label: "Workflow node metrics", Definition: "Node entries, elapsed transition samples, failed activities, and current waiting or blocked positions.", Denominator: "Selected-period transitions and activities for funnel, duration, and failures; bottlenecks are current state."},
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

func analyticsGoalOutcomeWindowClause(alias string, filter AnalyticsDashboardFilter) (string, []any) {
	clause := ""
	args := []any{}
	expression := "COALESCE(" + alias + ".achieved_at," + alias + ".updated_at)"
	if !filter.DateFrom.IsZero() {
		clause += " AND " + expression + ">=?"
		args = append(args, filter.DateFrom.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	if !filter.DateTo.IsZero() {
		clause += " AND " + expression + "<?"
		args = append(args, filter.DateTo.UTC().Format("2006-01-02 15:04:05.999999999"))
	}
	return clause, args
}

func analyticsTaskDimensionClause(alias string, filter AnalyticsDashboardFilter) (string, []any) {
	clause := ""
	args := []any{}
	if filter.View == "agents" {
		clause += " AND COALESCE(" + alias + ".category,'')<>'chat'"
		filter.WorkType = ""
	}
	if filter.AgentID == "__unassigned__" {
		clause += " AND " + alias + ".agent_definition_id IS NULL"
	} else if filter.AgentID != "" {
		clause += " AND " + alias + ".agent_definition_id=?"
		args = append(args, filter.AgentID)
	}
	if filter.WorkflowID != "" {
		clause += ` AND EXISTS (SELECT 1 FROM automation_dispatch_outbox ado
			JOIN automation_invocations ai ON ai.id=ado.invocation_id
			WHERE ado.task_id=` + alias + `.id AND ai.project_id=` + alias + `.project_id AND ai.automation_id=?)`
		args = append(args, filter.WorkflowID)
	}
	switch filter.WorkType {
	case "interactive":
		clause += " AND COALESCE(" + alias + ".category,'')<>'scheduled' AND NOT EXISTS (SELECT 1 FROM schedules analytics_schedule WHERE analytics_schedule.task_id=" + alias + ".id AND analytics_schedule.repeat_type<>'once')"
	case "recurring":
		clause += " AND (" + alias + ".category='scheduled' OR EXISTS (SELECT 1 FROM schedules analytics_schedule WHERE analytics_schedule.task_id=" + alias + ".id AND analytics_schedule.repeat_type<>'once'))"
	}
	return clause, args
}

func analyticsSkillOutcomeEvidenceClause(taskAlias string, filter AnalyticsDashboardFilter) (string, []any, bool) {
	handle := strings.TrimSpace(filter.EvidenceSkillHandle)
	scope := strings.TrimSpace(filter.EvidenceSkillScope)
	agentID := strings.TrimSpace(filter.EvidenceSkillAgentID)
	if handle == "" && scope == "" && agentID == "" {
		return "", nil, false
	}

	clause := ""
	args := []any{}
	if agentID == "__unassigned__" {
		clause += " AND " + taskAlias + ".agent_definition_id IS NULL"
	} else if agentID != "" {
		clause += " AND " + taskAlias + ".agent_definition_id=?"
		args = append(args, agentID)
	}
	eventWindow, eventArgs := analyticsEventWindowClause("skill_evidence", "created_at", filter)
	clause += " AND EXISTS (SELECT 1 FROM skill_analytics_events skill_evidence WHERE skill_evidence.task_id=" + taskAlias + ".id AND skill_evidence.project_id=" + taskAlias + ".project_id AND skill_evidence.event_type IN ('selected','loaded')" + eventWindow
	args = append(args, eventArgs...)
	if handle != "" {
		clause += " AND skill_evidence.skill_handle=?"
		args = append(args, handle)
	}
	if scope != "" {
		clause += " AND skill_evidence.skill_scope=?"
		args = append(args, scope)
	}
	clause += ")"
	return clause, args, true
}

func analyticsPeriodExpression(groupBy, column string) string {
	switch groupBy {
	case "week":
		return "strftime('%Y-W%W'," + column + ",'localtime')"
	case "month":
		return "strftime('%Y-%m'," + column + ",'localtime')"
	default:
		return "strftime('%Y-%m-%d'," + column + ",'localtime')"
	}
}

func metric(numerator, denominator int) models.AnalyticsMetric {
	m := models.AnalyticsMetric{Numerator: numerator, Denominator: denominator, SampleSize: denominator}
	if denominator > 0 {
		m.Percent = float64(numerator) * 100 / float64(denominator)
	}
	return m
}

type analyticsDashboardSections struct {
	outcomeMetrics       bool
	outcomeTrend         bool
	followUpDistribution bool
	comparison           bool
	funnel               bool
	agents               bool
	skills               bool
	agentSkills          bool
	modelCategories      bool
	models               bool
	workflows            bool
	evidenceRows         bool
	evidenceTotal        bool
	agentDetail          bool
	workflowDetail       bool
	insights             bool
}

func analyticsDashboardSectionsForView(view string) analyticsDashboardSections {
	switch strings.ToLower(strings.TrimSpace(view)) {
	case "overview":
		return analyticsDashboardSections{outcomeMetrics: true, outcomeTrend: true, comparison: true, funnel: true, workflows: true, evidenceRows: true, insights: true}
	case "outcomes":
		return analyticsDashboardSections{outcomeMetrics: true, outcomeTrend: true, followUpDistribution: true, comparison: true, funnel: true, evidenceRows: true, evidenceTotal: true}
	case "agents":
		return analyticsDashboardSections{agents: true, skills: true, evidenceRows: true, agentDetail: true}
	case "models":
		return analyticsDashboardSections{models: true}
	case "learning":
		return analyticsDashboardSections{skills: true, agentSkills: true}
	case "usage":
		return analyticsDashboardSections{}
	case "automations":
		return analyticsDashboardSections{workflows: true, workflowDetail: true}
	default:
		return analyticsDashboardSections{
			outcomeMetrics: true, outcomeTrend: true, followUpDistribution: true, comparison: true, funnel: true, agents: true, skills: true,
			agentSkills: true, modelCategories: true, models: true, workflows: true, evidenceRows: true, evidenceTotal: true,
			agentDetail: true, workflowDetail: true, insights: true,
		}
	}
}

func (r *ExecutionRepo) GetAnalyticsDashboard(ctx context.Context, filter AnalyticsDashboardFilter) (models.AnalyticsDashboard, error) {
	if strings.TrimSpace(filter.ProjectID) == "" {
		return models.AnalyticsDashboard{}, fmt.Errorf("analytics project_id is required")
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 20
	}
	if filter.EvidenceOffset < 0 {
		filter.EvidenceOffset = 0
	}
	dashboard := models.AnalyticsDashboard{
		Definitions:          append([]models.MetricDefinition(nil), analyticsMetricDefinitions...),
		Funnel:               []models.OutcomeFunnelStage{},
		OutcomeTrend:         []models.OutcomeTrendPoint{},
		CycleDistribution:    []models.AnalyticsDistributionPoint{},
		FollowUpDistribution: []models.AnalyticsDistributionPoint{},
		Agents:               []models.AgentPerformance{},
		SkillOutcomes:        []models.SkillOutcomePerformance{},
		AgentSkillOutcomes:   []models.AgentSkillOutcomePerformance{},
		ModelCategories:      []models.ModelCategoryPerformance{},
		Models:               []models.ModelPerformance{},
		Workflows:            []models.WorkflowPerformance{},
		RecentOutcomes:       []models.EvidenceTaskRow{},
		Insights:             []models.AnalyticsInsight{},
	}
	sections := analyticsDashboardSectionsForView(filter.View)
	var err error
	if sections.outcomeMetrics {
		current, cycles, followups, queryErr := r.queryOutcomeMetrics(ctx, filter, sections.followUpDistribution)
		if queryErr != nil {
			return dashboard, queryErr
		}
		dashboard.Current = current
		dashboard.CycleDistribution = cycleDistribution(cycles)
		if sections.followUpDistribution {
			dashboard.FollowUpDistribution = followUpDistribution(followups)
		}
	}
	if sections.outcomeTrend {
		if dashboard.OutcomeTrend, err = r.queryOutcomeTrend(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.comparison && filter.Compare && !filter.DateFrom.IsZero() && !filter.DateTo.IsZero() && filter.DateTo.After(filter.DateFrom) {
		duration := filter.DateTo.Sub(filter.DateFrom)
		previousFilter := filter
		previousFilter.DateTo = filter.DateFrom
		previousFilter.DateFrom = filter.DateFrom.Add(-duration)
		previousFilter.Compare = false
		previous, _, _, queryErr := r.queryOutcomeMetrics(ctx, previousFilter, false)
		if queryErr != nil {
			return dashboard, queryErr
		}
		dashboard.Previous = &previous
	}
	if sections.funnel {
		if dashboard.Funnel, err = r.queryOutcomeFunnel(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.agents {
		if filter.View == "agents" {
			dashboard.TaskSummary, err = r.queryAnalyticsTaskSummary(ctx, filter)
			if err != nil {
				return dashboard, err
			}
		}
		if dashboard.Agents, err = r.queryAgentPerformance(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.skills {
		if dashboard.SkillSummary, err = r.querySkillOutcomeSummary(ctx, filter); err != nil {
			return dashboard, err
		}
		if dashboard.SkillOutcomes, err = r.querySkillOutcomePerformance(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.agentSkills {
		if dashboard.AgentSkillOutcomes, err = r.queryAgentSkillOutcomePerformance(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.modelCategories {
		if dashboard.ModelCategories, err = r.queryModelCategoryPerformance(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.models {
		if dashboard.Models, err = r.queryModelPerformance(ctx, filter); err != nil {
			return dashboard, err
		}
		summary := &models.AnalyticsTaskSummary{}
		var goals, goalN, merged, mergeN int
		for _, row := range dashboard.Models {
			summary.TasksWorkedOn += row.TasksUsed
			goals += row.GoalAchievement.Numerator
			goalN += row.GoalAchievement.Denominator
			merged += row.MergeCompletion.Numerator
			mergeN += row.MergeCompletion.Denominator
		}
		summary.GoalAchievement = metric(goals, goalN)
		summary.MergeCompletion = metric(merged, mergeN)
		dashboard.TaskSummary = summary
	}
	if sections.workflows {
		if dashboard.Workflows, err = r.queryWorkflowPerformance(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	dashboard.EvidenceLimit = filter.Limit
	dashboard.EvidenceOffset = filter.EvidenceOffset
	if sections.evidenceTotal {
		if dashboard.EvidenceTotal, err = r.queryEvidenceTotal(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.evidenceRows {
		if dashboard.RecentOutcomes, err = r.queryRecentOutcomes(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.agentDetail && filter.AgentID != "" {
		if dashboard.AgentDetail, err = r.queryAgentAnalyticsDetail(ctx, filter, dashboard.SkillOutcomes, dashboard.RecentOutcomes); err != nil {
			return dashboard, err
		}
	}
	if sections.workflowDetail && filter.WorkflowID != "" {
		if dashboard.WorkflowDetail, err = r.queryWorkflowAnalyticsDetail(ctx, filter); err != nil {
			return dashboard, err
		}
	}
	if sections.insights {
		dashboard.Insights = buildAnalyticsInsights(dashboard.Current, dashboard.Previous, dashboard.Workflows, filter)
	}
	return dashboard, nil
}

func (r *ExecutionRepo) queryOutcomeTrend(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.OutcomeTrendPoint, error) {
	execWindow, execArgs := analyticsWindowClause("e", filter)
	goalWindow, goalArgs := analyticsGoalOutcomeWindowClause("g", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	execPeriod := analyticsPeriodExpression(filter.GroupBy, "e.started_at")
	goalPeriod := analyticsPeriodExpression(filter.GroupBy, "COALESCE(g.achieved_at,g.updated_at)")
	query := `WITH scoped_tasks AS (
		SELECT t.id FROM tasks t WHERE t.project_id=?` + dimension + `
	), period_exec AS (
		SELECT ` + execPeriod + ` period,e.task_id,e.status,e.is_followup
		FROM scoped_tasks t CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=t.id
		WHERE 1=1` + execWindow + `
	), task_period AS (
		SELECT period,task_id,MAX(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followed,
			MAX(CASE WHEN status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal
		FROM period_exec GROUP BY period,task_id
	), historical_first AS (
		SELECT p.period,p.task_id,(
			SELECT e.status FROM executions e INDEXED BY idx_executions_task_analytics
			WHERE e.task_id=p.task_id AND e.status IN ('completed','failed','cancelled')
			ORDER BY e.started_at,e.history_order,e.id LIMIT 1
		) status FROM task_period p WHERE p.terminal=1
	), technical AS (
		SELECT period,SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END) completed,COUNT(*) terminal
		FROM period_exec WHERE status IN ('completed','failed','cancelled') GROUP BY period
	), task_rates AS (
		SELECT p.period,SUM(p.followed) followed,COUNT(*) tasks,
			SUM(CASE WHEN f.status='completed' THEN 1 ELSE 0 END) first_completed,
			SUM(CASE WHEN f.status IS NOT NULL THEN 1 ELSE 0 END) first_terminal
		FROM task_period p LEFT JOIN historical_first f ON f.period=p.period AND f.task_id=p.task_id GROUP BY p.period
	), goal_outcomes AS (
		SELECT ` + goalPeriod + ` period,g.task_id,g.status FROM task_goals g JOIN scoped_tasks t ON t.id=g.task_id
		WHERE g.status IN ('achieved','failed')` + goalWindow + `
	), active_goal_outcomes AS (
		SELECT p.period,g.task_id,g.status FROM task_period p JOIN task_goals g ON g.task_id=p.task_id
		WHERE p.terminal=1 AND g.status IN ('active','paused','blocked')
	), evaluable_goals AS (
		SELECT period,task_id,status FROM goal_outcomes
		UNION SELECT period,task_id,status FROM active_goal_outcomes
	), goals AS (
		SELECT period,SUM(CASE WHEN status='achieved' THEN 1 ELSE 0 END) achieved,COUNT(*) evaluable
		FROM evaluable_goals GROUP BY period
	), periods AS (
		SELECT period FROM task_period UNION SELECT period FROM goal_outcomes
	)
	SELECT p.period,COALESCE(t.completed,0),COALESCE(t.terminal,0),
		COALESCE(g.achieved,0),COALESCE(g.evaluable,0),
		COALESCE(r.first_completed,0),COALESCE(r.first_terminal,0),
		COALESCE(r.followed,0),COALESCE(r.tasks,0)
	FROM periods p LEFT JOIN technical t ON t.period=p.period
	LEFT JOIN task_rates r ON r.period=p.period LEFT JOIN goals g ON g.period=p.period
	ORDER BY p.period`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, execArgs...)
	args = append(args, goalArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting analytics outcome trend: %w", err)
	}
	defer rows.Close()
	result := []models.OutcomeTrendPoint{}
	for rows.Next() {
		var point models.OutcomeTrendPoint
		var technicalCompleted, technicalTerminal, achieved, evaluable, firstCompleted, firstTerminal, followed, tasks int
		if err := rows.Scan(&point.Period, &technicalCompleted, &technicalTerminal, &achieved, &evaluable, &firstCompleted, &firstTerminal, &followed, &tasks); err != nil {
			return nil, fmt.Errorf("scanning analytics outcome trend: %w", err)
		}
		point.TechnicalCompletion = metric(technicalCompleted, technicalTerminal)
		point.GoalAchievement = metric(achieved, evaluable)
		point.FirstPass = metric(firstCompleted, firstTerminal)
		point.FollowUp = metric(followed, tasks)
		result = append(result, point)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryOutcomeMetrics(ctx context.Context, filter AnalyticsDashboardFilter, includeFollowUpDistribution bool) (models.OutcomeMetrics, []int64, []int, error) {
	out := models.OutcomeMetrics{}
	window, windowArgs := analyticsWindowClause("e", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	goalWindow, goalWindowArgs := analyticsGoalOutcomeWindowClause("g", filter)
	query := `WITH scoped_tasks AS (
			SELECT t.id,t.status FROM tasks t WHERE t.project_id=?` + dimension + `
		), period_exec AS (
			SELECT e.task_id,e.status,e.is_followup FROM scoped_tasks t
			CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=t.id WHERE 1=1` + window + `
		), period_terminal AS (
			SELECT * FROM period_exec WHERE status IN ('completed','failed','cancelled')
		), period_terminal_tasks AS (
			SELECT DISTINCT task_id FROM period_terminal
		), task_stats AS (
			SELECT task_id,COUNT(*) execution_count,SUM(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followups FROM period_exec GROUP BY task_id
		), first_terminal AS (
			SELECT p.task_id,(
				SELECT e.status FROM executions e INDEXED BY idx_executions_task_analytics
				WHERE e.task_id=p.task_id AND e.status IN ('completed','failed','cancelled')
				ORDER BY e.started_at,e.history_order,e.id LIMIT 1
			) status
			FROM period_terminal_tasks p
		), period_goal_outcomes AS (
			SELECT g.task_id,g.status FROM task_goals g JOIN scoped_tasks t ON t.id=g.task_id
			WHERE g.status IN ('achieved','failed')` + goalWindow + `
		), evaluable_goals AS (
			SELECT task_id,status FROM period_goal_outcomes
			UNION SELECT g.task_id,g.status FROM task_goals g JOIN period_terminal_tasks p ON p.task_id=g.task_id
			WHERE g.status IN ('active','paused','blocked')
		)
		SELECT
			(SELECT COUNT(*) FROM period_terminal WHERE status='completed'),
			(SELECT COUNT(*) FROM period_terminal),
			(SELECT COUNT(*) FROM period_terminal WHERE status='cancelled'),
			(SELECT COUNT(*) FROM first_terminal WHERE status='completed'),
			(SELECT COUNT(*) FROM first_terminal),
			(SELECT COUNT(*) FROM task_stats WHERE followups>0),
			(SELECT COUNT(*) FROM task_stats),
			(SELECT COUNT(*) FROM evaluable_goals WHERE status='achieved'),
			(SELECT COUNT(*) FROM evaluable_goals)`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	args = append(args, goalWindowArgs...)
	var technicalCompleted, terminal, cancelled, firstCompleted, firstTerminal, followed, tasks, achieved, evaluable int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&technicalCompleted, &terminal, &cancelled, &firstCompleted, &firstTerminal, &followed, &tasks, &achieved, &evaluable); err != nil {
		return out, nil, nil, fmt.Errorf("getting analytics outcome metrics: %w", err)
	}
	out.TechnicalCompletion = metric(technicalCompleted, terminal)
	out.CancelledExecutionCount = cancelled
	out.FirstPass = metric(firstCompleted, firstTerminal)
	out.FollowUp = metric(followed, tasks)
	out.GoalAchievement = metric(achieved, evaluable)
	out.TasksEvaluated = tasks

	terminalWindow, terminalWindowArgs := analyticsWindowClause("e2", filter)
	cycleQuery := `WITH scoped_tasks AS (
			SELECT t.id,t.category FROM tasks t WHERE t.project_id=?` + dimension + `
	), terminal_tasks AS (
		SELECT t.id task_id FROM scoped_tasks t
		WHERE t.category<>'scheduled'
		AND NOT EXISTS (SELECT 1 FROM schedules s WHERE s.task_id=t.id AND s.repeat_type<>'once')
		AND EXISTS (
			SELECT 1 FROM executions e
			WHERE e.task_id=t.id AND e.status IN ('completed','failed','cancelled')` + window + `
			LIMIT 1
		)
	)
	SELECT COALESCE(CAST(MAX(0,(julianday(terminal_at)-julianday(first_started_at))*86400000) AS INTEGER),0)
	FROM (
		SELECT p.task_id,
			(SELECT COALESCE(e2.completed_at,e2.started_at) FROM executions e2 WHERE e2.task_id=p.task_id AND e2.status IN ('completed','failed','cancelled')` + terminalWindow + ` ORDER BY COALESCE(e2.completed_at,e2.started_at) DESC LIMIT 1) terminal_at,
			(SELECT e3.started_at FROM executions e3 WHERE e3.task_id=p.task_id ORDER BY e3.started_at ASC LIMIT 1) first_started_at
		FROM terminal_tasks p
	) task_cycles
	WHERE terminal_at IS NOT NULL AND first_started_at IS NOT NULL`
	cycleArgs := append([]any{filter.ProjectID}, dimensionArgs...)
	cycleArgs = append(cycleArgs, windowArgs...)
	cycleArgs = append(cycleArgs, terminalWindowArgs...)
	rows, err := r.db.QueryContext(ctx, cycleQuery, cycleArgs...)
	if err != nil {
		return out, nil, nil, fmt.Errorf("getting analytics task distributions: %w", err)
	}
	cycles := []int64{}
	for rows.Next() {
		var cycle int64
		if err := rows.Scan(&cycle); err != nil {
			rows.Close()
			return out, nil, nil, fmt.Errorf("scanning analytics task distributions: %w", err)
		}
		cycles = append(cycles, cycle)
	}
	if err := rows.Close(); err != nil {
		return out, nil, nil, err
	}
	followups := []int{}
	if includeFollowUpDistribution {
		followupQuery := `SELECT COALESCE(SUM(CASE WHEN e.is_followup=1 THEN 1 ELSE 0 END),0)
			FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + dimension + window + ` GROUP BY e.task_id`
		followupArgs := append([]any{filter.ProjectID}, dimensionArgs...)
		followupArgs = append(followupArgs, windowArgs...)
		followupRows, err := r.db.QueryContext(ctx, followupQuery, followupArgs...)
		if err != nil {
			return out, nil, nil, fmt.Errorf("getting analytics follow-up distribution: %w", err)
		}
		for followupRows.Next() {
			var count int
			if err := followupRows.Scan(&count); err != nil {
				followupRows.Close()
				return out, nil, nil, fmt.Errorf("scanning analytics follow-up distribution: %w", err)
			}
			followups = append(followups, count)
		}
		if err := followupRows.Close(); err != nil {
			return out, nil, nil, err
		}
	}
	sort.Slice(cycles, func(i, j int) bool { return cycles[i] < cycles[j] })
	out.MedianCycleTimeMs = percentileInt64(cycles, 0.5)
	out.P90CycleTimeMs = percentileInt64(cycles, 0.9)
	out.CycleSampleSize = len(cycles)
	if err := r.queryOutcomeCosts(ctx, filter, &out); err != nil {
		return out, nil, nil, err
	}
	return out, cycles, followups, nil
}

func (r *ExecutionRepo) queryOutcomeCosts(ctx context.Context, filter AnalyticsDashboardFilter, out *models.OutcomeMetrics) error {
	window, windowArgs := analyticsWindowClause("e", filter)
	usageWindow, usageWindowArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	goalWindow, goalWindowArgs := analyticsGoalOutcomeWindowClause("g", filter)
	query := `WITH period_tasks AS (
		SELECT e.task_id,MAX(CASE WHEN e.status='completed' THEN 1 ELSE 0 END) technical_completed
		FROM tasks t CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=t.id
		WHERE t.project_id=?` + dimension + window + ` GROUP BY e.task_id
	), achieved_tasks AS (
		SELECT g.task_id FROM task_goals g JOIN period_tasks p ON p.task_id=g.task_id WHERE g.status='achieved'` + goalWindow + `
	), task_usage AS (
		SELECT p.task_id,SUM(u.cost_usd) known_cost,SUM(u.total_tokens) tokens,
			MAX(CASE WHEN u.cost_usd IS NOT NULL THEN 1 ELSE 0 END) has_cost,COUNT(u.task_id) has_usage
		FROM period_tasks p CROSS JOIN llm_usage_events u INDEXED BY idx_llm_usage_events_task_project_time_cost
		ON u.task_id=p.task_id AND u.project_id=?` + usageWindow + ` GROUP BY p.task_id
	)
	SELECT
		COALESCE(SUM(CASE WHEN a.task_id IS NOT NULL AND u.has_cost=1 THEN u.known_cost ELSE 0 END),0),
		COUNT(DISTINCT CASE WHEN a.task_id IS NOT NULL AND u.has_cost=1 THEN p.task_id END),
		COUNT(DISTINCT a.task_id),
		COALESCE(SUM(CASE WHEN a.task_id IS NOT NULL AND u.has_usage>0 THEN u.tokens ELSE 0 END),0),
		COUNT(DISTINCT CASE WHEN a.task_id IS NOT NULL AND u.has_usage>0 THEN p.task_id END),
		COALESCE(SUM(CASE WHEN p.technical_completed=1 AND u.has_cost=1 THEN u.known_cost ELSE 0 END),0),
		COUNT(DISTINCT CASE WHEN p.technical_completed=1 AND u.has_cost=1 THEN p.task_id END),
		COUNT(DISTINCT CASE WHEN p.technical_completed=1 THEN p.task_id END)
	FROM period_tasks p LEFT JOIN achieved_tasks a ON a.task_id=p.task_id LEFT JOIN task_usage u ON u.task_id=p.task_id`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	args = append(args, goalWindowArgs...)
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
	failedQuery := `SELECT SUM(u.cost_usd),COUNT(DISTINCT CASE WHEN u.cost_usd IS NOT NULL THEN e.id END),COUNT(DISTINCT e.id)
		FROM tasks t CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=t.id
		LEFT JOIN llm_usage_events u INDEXED BY idx_llm_usage_events_execution_project_time_cost
		ON u.execution_id=e.id AND u.project_id=t.project_id` + usageWindow + `
		WHERE t.project_id=? AND e.status='failed'` + dimension + window
	failedArgs := append([]any{}, usageWindowArgs...)
	failedArgs = append(failedArgs, filter.ProjectID)
	failedArgs = append(failedArgs, dimensionArgs...)
	failedArgs = append(failedArgs, windowArgs...)
	var failedCost sql.NullFloat64
	var failedCovered, failedEligible int
	if err := r.db.QueryRowContext(ctx, failedQuery, failedArgs...).Scan(&failedCost, &failedCovered, &failedEligible); err != nil {
		return fmt.Errorf("getting known failed run cost: %w", err)
	}
	if failedCovered > 0 && failedCost.Valid {
		out.KnownFailedExecutionCost = &models.CostCoverage{Value: failedCost.Float64, Covered: failedCovered, Eligible: failedEligible}
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
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	goalWindow, goalArgs := analyticsGoalOutcomeWindowClause("g", filter)
	query := `WITH created_tasks AS (
		SELECT t.id,t.worktree_path,t.merge_status FROM tasks t WHERE t.project_id=?` + dimension + taskWindow + `
		), period_exec AS (
			SELECT e.task_id,e.status FROM executions e JOIN created_tasks t ON t.id=e.task_id WHERE 1=1` + execWindow + `
	), goal_eligible AS (
		SELECT g.task_id,g.status,CASE WHEN g.status='achieved'` + goalWindow + ` THEN 1 ELSE 0 END achieved_in_period
		FROM task_goals g JOIN created_tasks t ON t.id=g.task_id WHERE g.status<>'cleared'
	)
	SELECT
		(SELECT COUNT(*) FROM created_tasks),
		(SELECT COUNT(DISTINCT task_id) FROM period_exec),
		(SELECT COUNT(DISTINCT task_id) FROM period_exec WHERE status='completed'),
		(SELECT COALESCE(SUM(achieved_in_period),0) FROM goal_eligible),
		(SELECT COUNT(*) FROM goal_eligible),
		(SELECT COUNT(*) FROM created_tasks WHERE worktree_path<>'' OR merge_status<>''),
		(SELECT COUNT(*) FROM created_tasks WHERE merge_status='merged')`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, taskArgs...)
	args = append(args, execArgs...)
	args = append(args, goalArgs...)
	var created, started, completed, achieved, goalEligible, mergeEligible, merged int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&created, &started, &completed, &achieved, &goalEligible, &mergeEligible, &merged); err != nil {
		return nil, fmt.Errorf("getting analytics outcome funnel: %w", err)
	}
	return []models.OutcomeFunnelStage{
		{Key: "created", Label: "Tasks created", Count: created, Denominator: created},
		{Key: "started", Label: "Created tasks started", Count: started, Denominator: created},
		{Key: "technical_completed", Label: "Created tasks completed successfully", Count: completed, Denominator: started},
		{Key: "goal_achieved", Label: "Goal achieved", Count: achieved, Denominator: goalEligible},
		{Key: "merged", Label: "Merge-tracked tasks merged", Count: merged, Denominator: mergeEligible},
	}, nil
}

func (r *ExecutionRepo) queryAnalyticsTaskSummary(ctx context.Context, filter AnalyticsDashboardFilter) (*models.AnalyticsTaskSummary, error) {
	if filter.View == "agents" {
		filter.WorkType = ""
	}
	window, args := analyticsWindowClause("e", filter)
	usageWindow, usageArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	query := `WITH active_tasks AS (
		SELECT e.task_id FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + window + `
		UNION SELECT u.task_id FROM llm_usage_events u WHERE u.project_id=? AND (u.operation IN ('task','task_followup') OR (u.operation='' AND EXISTS (SELECT 1 FROM executions e WHERE e.id=u.execution_id AND e.task_id=u.task_id)))` + usageWindow + `
	) SELECT COUNT(*),COALESCE(SUM(g.status='achieved'),0),COALESCE(SUM(g.status IN ('achieved','failed','active','paused','blocked')),0),
	COALESCE(SUM(t.merge_status='merged'),0),COALESCE(SUM(t.worktree_path<>'' OR t.merge_status<>''),0)
	FROM active_tasks a JOIN tasks t ON t.id=a.task_id LEFT JOIN task_goals g ON g.task_id=t.id
	WHERE t.project_id=? AND COALESCE(t.category,'')<>'chat'` + dimension
	params := append([]any{filter.ProjectID}, args...)
	params = append(params, filter.ProjectID)
	params = append(params, usageArgs...)
	params = append(params, filter.ProjectID)
	params = append(params, dimensionArgs...)
	summary := &models.AnalyticsTaskSummary{}
	var goals, goalN, merged, mergeN int
	err := r.db.QueryRowContext(ctx, query, params...).Scan(&summary.TasksWorkedOn, &goals, &goalN, &merged, &mergeN)
	summary.GoalAchievement = metric(goals, goalN)
	summary.MergeCompletion = metric(merged, mergeN)
	return summary, err
}

func (r *ExecutionRepo) queryAgentPerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.AgentPerformance, error) {
	filter.WorkType = ""
	window, windowArgs := analyticsWindowClause("e", filter)
	usageWindow, usageWindowArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	comparisonFilter := filter
	comparisonFilter.AgentID = ""
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", comparisonFilter)
	query := `WITH scoped_tasks AS (
			SELECT t.id,t.agent_definition_id,t.merge_status,t.worktree_path FROM tasks t WHERE t.project_id=? AND COALESCE(t.category,'')<>'chat'` + dimension + `
	), period_exec AS (
			SELECT t.id task_id,e.agent_config_id,e.status,e.started_at,e.completed_at,e.is_followup,t.agent_definition_id
			FROM scoped_tasks t CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=t.id WHERE 1=1` + window + `
	), period_tasks AS (
		SELECT task_id FROM period_exec
		UNION SELECT u.task_id FROM scoped_tasks t JOIN llm_usage_events u ON u.task_id=t.id
		WHERE (u.operation IN ('task','task_followup') OR (u.operation='' AND EXISTS (SELECT 1 FROM executions e WHERE e.id=u.execution_id AND e.task_id=t.id)))` + usageWindow + `
	), period_terminal_task_ids AS (
		SELECT DISTINCT task_id FROM period_exec WHERE status IN ('completed','failed','cancelled')
	), historical_terminal AS (
		SELECT p.task_id,(
			SELECT e.status FROM executions e INDEXED BY idx_executions_task_analytics
			WHERE e.task_id=p.task_id AND e.status IN ('completed','failed','cancelled')
			ORDER BY e.started_at,e.history_order,e.id LIMIT 1
		) status FROM period_terminal_task_ids p
		), task_stats AS (
			SELECT pt.task_id,t.agent_definition_id,COUNT(p.task_id) executions,SUM(CASE WHEN p.is_followup=1 THEN 1 ELSE 0 END) followups,
			SUM(CASE WHEN p.status='completed' THEN 1 ELSE 0 END) completed_execs,
			SUM(CASE WHEN p.status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal_execs,
			MAX(CASE WHEN f.status='completed' THEN 1 ELSE 0 END) first_completed,
			MAX(CASE WHEN f.status IS NOT NULL THEN 1 ELSE 0 END) has_first,
			CAST(SUM(CASE WHEN p.completed_at IS NOT NULL THEN MAX(0,(julianday(p.completed_at)-julianday(p.started_at))*86400000) END) AS INTEGER) duration_ms
			FROM period_tasks pt JOIN scoped_tasks t ON t.id=pt.task_id LEFT JOIN period_exec p ON p.task_id=pt.task_id
			LEFT JOIN historical_terminal f ON f.task_id=pt.task_id
			GROUP BY pt.task_id,t.agent_definition_id
		), period_goals AS (
			SELECT g.task_id,g.status FROM task_goals g WHERE g.status IN ('achieved','failed','active','paused','blocked')
		), evaluable_goals AS (
			SELECT task_id,status FROM period_goals
		), agent_rollup AS (		SELECT s.agent_definition_id,COUNT(*) tasks_evaluated,SUM(completed_execs) completed_execs,SUM(terminal_execs) terminal_execs,
		SUM(first_completed) first_completed,SUM(has_first) first_denominator,SUM(CASE WHEN followups>0 THEN 1 ELSE 0 END) followed,
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END) achieved,COUNT(g.task_id) goal_denominator,
		SUM(s.followups) total_followups,SUM(CASE WHEN t.merge_status='merged' THEN 1 ELSE 0 END) merged,
		SUM(CASE WHEN t.worktree_path<>'' OR t.merge_status<>'' THEN 1 ELSE 0 END) merge_n
		FROM task_stats s JOIN scoped_tasks t ON t.id=s.task_id LEFT JOIN evaluable_goals g ON g.task_id=s.task_id GROUP BY s.agent_definition_id
	), ranked_durations AS (
		SELECT agent_definition_id,duration_ms,ROW_NUMBER() OVER(PARTITION BY agent_definition_id ORDER BY duration_ms) rn,
		COUNT(*) OVER(PARTITION BY agent_definition_id) duration_count FROM task_stats WHERE duration_ms IS NOT NULL
	), medians AS (
		SELECT agent_definition_id,CAST(AVG(duration_ms) AS INTEGER) median_duration_ms,MAX(duration_count) duration_count FROM ranked_durations
		WHERE rn IN ((duration_count+1)/2,(duration_count+2)/2) GROUP BY agent_definition_id
	), model_counts AS (
		SELECT agent_definition_id,agent_config_id,COUNT(*) use_count FROM period_exec GROUP BY agent_definition_id,agent_config_id
	), ranked_models AS (
		SELECT agent_definition_id,agent_config_id,ROW_NUMBER() OVER(PARTITION BY agent_definition_id ORDER BY use_count DESC,agent_config_id) rn FROM model_counts
	), achieved_tasks AS (
		SELECT s.task_id,s.agent_definition_id FROM task_stats s
		JOIN period_goals g ON g.task_id=s.task_id AND g.status='achieved'
	), usage AS (
		SELECT p.task_id,SUM(u.cost_usd) known_cost,MAX(CASE WHEN u.cost_usd IS NOT NULL THEN 1 ELSE 0 END) has_cost
		FROM achieved_tasks p CROSS JOIN llm_usage_events u INDEXED BY idx_llm_usage_events_task_project_time_cost
		ON u.task_id=p.task_id WHERE u.project_id=? AND (u.operation IN ('task','task_followup') OR (u.operation='' AND EXISTS (SELECT 1 FROM executions e WHERE e.id=u.execution_id AND e.task_id=p.task_id)))` + usageWindow + ` GROUP BY p.task_id
	), costs AS (
		SELECT s.agent_definition_id,COALESCE(SUM(CASE WHEN u.has_cost=1 THEN u.known_cost ELSE 0 END),0) known_cost,
		COUNT(DISTINCT CASE WHEN u.has_cost=1 THEN s.task_id END) covered,COUNT(DISTINCT s.task_id) eligible
		FROM achieved_tasks s LEFT JOIN usage u ON u.task_id=s.task_id GROUP BY s.agent_definition_id
	)
	SELECT COALESCE(a.id,''),COALESCE(a.name,'Unassigned / Auto-routed'),r.tasks_evaluated,
		r.completed_execs,r.terminal_execs,r.first_completed,r.first_denominator,r.followed,r.achieved,r.goal_denominator,
		COALESCE(m.median_duration_ms,0),COALESCE(m.duration_count,0),COALESCE(ac.name || ' (' || ac.model || ')',rm.agent_config_id,'Unknown'),
		COALESCE(c.known_cost,0),COALESCE(c.covered,0),COALESCE(c.eligible,0),r.total_followups,r.merged,r.merge_n
	FROM agent_rollup r LEFT JOIN agents a ON a.id=r.agent_definition_id
	LEFT JOIN medians m ON m.agent_definition_id IS r.agent_definition_id
	LEFT JOIN ranked_models rm ON rm.agent_definition_id IS r.agent_definition_id AND rm.rn=1
	LEFT JOIN agent_configs ac ON ac.id=rm.agent_config_id LEFT JOIN costs c ON c.agent_definition_id IS r.agent_definition_id
	ORDER BY CASE WHEN a.id IS NULL THEN 1 ELSE 0 END,r.tasks_evaluated DESC,a.name`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	args = append(args, usageWindowArgs...)
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
		var completed, terminal, firstCompleted, firstDenom, followed, achieved, goalDenom, followups, merged, mergeN int
		var knownCost float64
		var costCovered, costEligible int
		if err := rows.Scan(&row.AgentID, &row.AgentName, &row.TasksEvaluated, &completed, &terminal, &firstCompleted, &firstDenom, &followed, &achieved, &goalDenom, &row.MedianDurationMs, &row.DurationSampleSize, &row.MostUsedModel, &knownCost, &costCovered, &costEligible, &followups, &merged, &mergeN); err != nil {
			return nil, fmt.Errorf("scanning agent performance: %w", err)
		}
		row.TechnicalCompletion = metric(completed, terminal)
		row.FirstPass = metric(firstCompleted, firstDenom)
		row.FollowUp = metric(followed, row.TasksEvaluated)
		row.GoalAchievement = metric(achieved, goalDenom)
		row.MergeCompletion = metric(merged, mergeN)
		if row.TasksEvaluated > 0 {
			row.AverageFollowUps = float64(followups) / float64(row.TasksEvaluated)
		}
		if costCovered > 0 {
			row.KnownCostPerAchievedGoal = &models.CostCoverage{Value: knownCost / float64(costCovered), Covered: costCovered, Eligible: costEligible}
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func skillOutcomePopulation(filter AnalyticsDashboardFilter) (string, []any) {
	eventWindow, eventArgs := analyticsEventWindowClause("s", "created_at", filter)
	execWindow, execArgs := analyticsWindowClause("e", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	goalWindow, goalArgs := analyticsGoalOutcomeWindowClause("g", filter)
	query := `WITH skill_tasks AS (
		SELECT DISTINCT s.skill_handle,s.skill_scope,s.task_id FROM skill_analytics_events s JOIN tasks t ON t.id=s.task_id
		WHERE t.project_id=? AND s.project_id=t.project_id` + dimension + ` AND s.event_type IN ('selected','loaded') AND s.task_id IS NOT NULL AND s.task_id<>''` + eventWindow + `
		), period_exec AS (
			SELECT st.skill_handle,st.skill_scope,e.task_id,e.status,e.is_followup FROM skill_tasks st
			CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=st.task_id WHERE 1=1` + execWindow + `
	), task_stats AS (
		SELECT skill_handle,skill_scope,task_id,MAX(CASE WHEN status='completed' THEN 1 ELSE 0 END) completed,
		MAX(CASE WHEN status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal,
		MAX(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followed FROM period_exec GROUP BY skill_handle,skill_scope,task_id
		), period_terminal_tasks AS (
			SELECT DISTINCT task_id FROM period_exec WHERE status IN ('completed','failed','cancelled')
		), period_goals AS (
			SELECT g.task_id,g.status FROM task_goals g WHERE g.status IN ('achieved','failed')` + goalWindow + `
		), evaluable_goals AS (
			SELECT task_id,status FROM period_goals
			UNION SELECT g.task_id,g.status FROM task_goals g JOIN period_terminal_tasks p ON p.task_id=g.task_id WHERE g.status IN ('active','paused','blocked')
		) `
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, eventArgs...)
	args = append(args, execArgs...)
	args = append(args, goalArgs...)
	return query, args
}

func (r *ExecutionRepo) querySkillOutcomeSummary(ctx context.Context, filter AnalyticsDashboardFilter) (models.SkillOutcomeSummary, error) {
	query, args := skillOutcomePopulation(filter)
	var summary models.SkillOutcomeSummary
	err := r.db.QueryRowContext(ctx, query+`SELECT
		(SELECT COUNT(*) FROM (SELECT DISTINCT skill_handle,skill_scope FROM task_stats)),
		COUNT(DISTINCT s.task_id),COUNT(DISTINCT g.task_id)
		FROM task_stats s LEFT JOIN evaluable_goals g ON g.task_id=s.task_id`, args...).Scan(&summary.SkillsUsed, &summary.TasksUsingSkills, &summary.TasksWithGoalEvidence)
	return summary, err
}

func (r *ExecutionRepo) querySkillOutcomePerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.SkillOutcomePerformance, error) {
	query, args := skillOutcomePopulation(filter)
	query += `SELECT s.skill_handle,s.skill_scope,COUNT(*),SUM(s.completed),SUM(s.terminal),SUM(s.followed),
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END),COUNT(g.task_id)
		FROM task_stats s LEFT JOIN evaluable_goals g ON g.task_id=s.task_id GROUP BY s.skill_handle,s.skill_scope ORDER BY COUNT(*) DESC,s.skill_handle,s.skill_scope LIMIT 20`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting skill outcome performance: %w", err)
	}
	defer rows.Close()
	result := []models.SkillOutcomePerformance{}
	for rows.Next() {
		var row models.SkillOutcomePerformance
		var completed, terminal, followed, achieved, goalDenominator int
		if err := rows.Scan(&row.SkillHandle, &row.SkillScope, &row.TasksEvaluated, &completed, &terminal, &followed, &achieved, &goalDenominator); err != nil {
			return nil, fmt.Errorf("scanning skill outcome performance: %w", err)
		}
		row.TechnicalCompletion = metric(completed, terminal)
		row.GoalAchievement = metric(achieved, goalDenominator)
		row.FollowUp = metric(followed, row.TasksEvaluated)
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryAgentSkillOutcomePerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.AgentSkillOutcomePerformance, error) {
	eventWindow, eventArgs := analyticsEventWindowClause("s", "created_at", filter)
	execWindow, execArgs := analyticsWindowClause("e", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	goalWindow, goalArgs := analyticsGoalOutcomeWindowClause("g", filter)
	query := `WITH skill_tasks AS (
		SELECT DISTINCT s.skill_handle,s.skill_scope,s.task_id,t.agent_definition_id FROM skill_analytics_events s JOIN tasks t ON t.id=s.task_id
		WHERE t.project_id=? AND s.project_id=t.project_id` + dimension + ` AND s.event_type IN ('selected','loaded') AND s.task_id IS NOT NULL AND s.task_id<>''` + eventWindow + `
		), period_exec AS (
			SELECT st.skill_handle,st.skill_scope,st.agent_definition_id,e.task_id,e.status,e.is_followup FROM skill_tasks st
			CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=st.task_id WHERE 1=1` + execWindow + `
	), task_stats AS (
		SELECT skill_handle,skill_scope,agent_definition_id,task_id,MAX(CASE WHEN status='completed' THEN 1 ELSE 0 END) completed,
		MAX(CASE WHEN status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal,
		MAX(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followed FROM period_exec GROUP BY skill_handle,skill_scope,agent_definition_id,task_id
	), period_terminal_tasks AS (
		SELECT DISTINCT task_id FROM period_exec WHERE status IN ('completed','failed','cancelled')
	), period_goals AS (
		SELECT g.task_id,g.status FROM task_goals g WHERE g.status IN ('achieved','failed')` + goalWindow + `
	), evaluable_goals AS (
		SELECT task_id,status FROM period_goals
		UNION SELECT g.task_id,g.status FROM task_goals g JOIN period_terminal_tasks p ON p.task_id=g.task_id WHERE g.status IN ('active','paused','blocked')
	)
	SELECT COALESCE(a.id,''),COALESCE(a.name,'Unassigned / Auto-routed'),s.skill_handle,s.skill_scope,COUNT(*),SUM(s.completed),SUM(s.terminal),SUM(s.followed),
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END),COUNT(g.task_id)
	FROM task_stats s LEFT JOIN evaluable_goals g ON g.task_id=s.task_id LEFT JOIN agents a ON a.id=s.agent_definition_id
	GROUP BY s.agent_definition_id,s.skill_handle,s.skill_scope ORDER BY COUNT(*) DESC,a.name,s.skill_handle,s.skill_scope LIMIT 50`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, eventArgs...)
	args = append(args, execArgs...)
	args = append(args, goalArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting Agent skill outcome performance: %w", err)
	}
	defer rows.Close()
	result := []models.AgentSkillOutcomePerformance{}
	for rows.Next() {
		var row models.AgentSkillOutcomePerformance
		var completed, terminal, followed, achieved, goalDenominator int
		if err := rows.Scan(&row.AgentID, &row.AgentName, &row.SkillHandle, &row.SkillScope, &row.TasksEvaluated, &completed, &terminal, &followed, &achieved, &goalDenominator); err != nil {
			return nil, fmt.Errorf("scanning Agent skill outcome performance: %w", err)
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
	query := `SELECT a.id,a.name,
		COUNT(DISTINCT i.id),
		COUNT(DISTINCT CASE WHEN i.status='completed' THEN i.id END),
		COUNT(DISTINCT CASE WHEN i.status='failed' THEN i.id END),
		COUNT(DISTINCT CASE WHEN i.status='cancelled' THEN i.id END),
		COUNT(DISTINCT CASE WHEN i.status='skipped' THEN i.id END),
		COUNT(DISTINCT CASE WHEN i.status IN ('claimed','dispatched','running') THEN i.id END),
		COALESCE(CAST(AVG(CASE WHEN i.status IN ('completed','failed','cancelled','skipped') AND i.completed_at IS NOT NULL AND i.started_at IS NOT NULL THEN (julianday(i.completed_at)-julianday(i.started_at))*86400000 END) AS INTEGER),0),
		COUNT(DISTINCT CASE WHEN i.status IN ('completed','failed','cancelled','skipped') AND i.completed_at IS NOT NULL AND i.started_at IS NOT NULL THEN i.id END),
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
		if err := rows.Scan(&row.WorkflowID, &row.WorkflowName, &row.InvocationCount, &row.CompletedCount, &row.FailedCount, &row.CancelledCount, &row.SkippedCount, &row.OpenCount, &row.AverageDurationMs, &row.DurationSampleSize, &row.WaitingCount, &row.BlockedCount, &row.Health); err != nil {
			return nil, err
		}
		if row.InvocationCount > 0 {
			row.CompletionRate = float64(row.CompletedCount) * 100 / float64(row.InvocationCount)
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryEvidenceTotal(ctx context.Context, filter AnalyticsDashboardFilter) (int, error) {
	window, windowArgs := analyticsWindowClause("e", filter)
	taskWindow, taskWindowArgs := analyticsTaskWindowClause("t", filter)
	goalWindow, goalWindowArgs := analyticsGoalOutcomeWindowClause("g", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	skillEvidence, skillEvidenceArgs, skillEvidenceActive := analyticsSkillOutcomeEvidenceClause("t", filter)
	evidenceTaskIDs := "SELECT task_id FROM period_task_ids UNION SELECT task_id FROM created_task_ids UNION SELECT task_id FROM evaluable_goals"
	if skillEvidenceActive {
		evidenceTaskIDs = "SELECT task_id FROM period_task_ids"
	}
	query := `WITH scoped_tasks AS (
			SELECT t.id,t.created_at FROM tasks t WHERE t.project_id=?` + dimension + skillEvidence + `
		), period_exec AS (
			SELECT e.task_id,e.status FROM executions e JOIN scoped_tasks t ON t.id=e.task_id WHERE 1=1` + window + `
	), period_task_ids AS (SELECT DISTINCT task_id FROM period_exec),
	created_task_ids AS (SELECT t.id task_id FROM scoped_tasks t WHERE 1=1` + taskWindow + `),
	period_terminal_task_ids AS (SELECT DISTINCT task_id FROM period_exec WHERE status IN ('completed','failed','cancelled')),
	evaluable_goals AS (
		SELECT g.task_id,g.status FROM task_goals g JOIN scoped_tasks t ON t.id=g.task_id WHERE g.status IN ('achieved','failed')` + goalWindow + `
		UNION SELECT g.task_id,g.status FROM task_goals g JOIN period_terminal_task_ids p ON p.task_id=g.task_id WHERE g.status IN ('active','paused','blocked')
	), evidence_task_ids AS (
		` + evidenceTaskIDs + `
	) SELECT COUNT(*) FROM evidence_task_ids`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, skillEvidenceArgs...)
	args = append(args, windowArgs...)
	args = append(args, taskWindowArgs...)
	args = append(args, goalWindowArgs...)
	var total int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("counting analytics evidence: %w", err)
	}
	return total, nil
}

func (r *ExecutionRepo) queryRecentOutcomes(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.EvidenceTaskRow, error) {
	window, windowArgs := analyticsWindowClause("e", filter)
	taskWindow, taskWindowArgs := analyticsTaskWindowClause("t", filter)
	goalWindow, goalWindowArgs := analyticsGoalOutcomeWindowClause("g", filter)
	usageWindow, usageWindowArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	skillEvidence, skillEvidenceArgs, skillEvidenceActive := analyticsSkillOutcomeEvidenceClause("t", filter)
	evidenceTaskIDs := "SELECT task_id FROM period_task_ids UNION SELECT task_id FROM created_task_ids UNION SELECT task_id FROM evaluable_goals"
	if skillEvidenceActive {
		evidenceTaskIDs = "SELECT task_id FROM period_task_ids"
	}
	periodExpr := analyticsPeriodExpression(filter.GroupBy, "MAX(p.started_at)")
	terminalPeriodExpr := analyticsPeriodExpression(filter.GroupBy, "started_at")
	query := `WITH scoped_tasks AS (
			SELECT t.id,t.title,t.status,t.merge_status,t.agent_definition_id,t.category,t.created_at,t.worktree_path FROM tasks t WHERE t.project_id=?` + dimension + skillEvidence + `
		), period_exec AS (
			SELECT e.id,e.task_id,e.status,e.started_at,e.history_order,e.agent_config_id,e.completed_at,e.is_followup
			FROM scoped_tasks t CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=t.id WHERE 1=1` + window + `
		), period_task_ids AS (SELECT DISTINCT task_id FROM period_exec),
		created_task_ids AS (SELECT t.id task_id FROM scoped_tasks t WHERE 1=1` + taskWindow + `),
		period_terminal_task_ids AS (SELECT DISTINCT task_id FROM period_exec WHERE status IN ('completed','failed','cancelled')),
		evaluable_goals AS (
			SELECT g.task_id,g.status FROM task_goals g WHERE g.status IN ('achieved','failed')` + goalWindow + `
			UNION SELECT g.task_id,g.status FROM task_goals g JOIN period_terminal_task_ids p ON p.task_id=g.task_id WHERE g.status IN ('active','paused','blocked')
		), evidence_task_ids AS (
			` + evidenceTaskIDs + `
		), selected_evidence_tasks AS (
			SELECT t.id task_id,COALESCE(MAX(p.started_at),t.created_at) sort_at
			FROM scoped_tasks t JOIN evidence_task_ids eti ON eti.task_id=t.id
			LEFT JOIN period_exec p ON p.task_id=t.id
			GROUP BY t.id ORDER BY sort_at DESC,t.id DESC LIMIT ? OFFSET ?
		), selected_period_exec AS (
			SELECT p.* FROM period_exec p JOIN selected_evidence_tasks s ON s.task_id=p.task_id
		), selected_terminal_task_ids AS (
			SELECT DISTINCT task_id FROM selected_period_exec WHERE status IN ('completed','failed','cancelled')
		), historical_terminal AS (
			SELECT e.task_id,e.status,ROW_NUMBER() OVER(PARTITION BY e.task_id ORDER BY e.started_at,e.history_order,e.id) rn
			FROM selected_terminal_task_ids p CROSS JOIN executions e INDEXED BY idx_executions_task_analytics
			ON e.task_id=p.task_id WHERE e.status IN ('completed','failed','cancelled')
		), historical_start AS (
			SELECT e.task_id,MIN(e.started_at) first_started_at FROM selected_terminal_task_ids p
			CROSS JOIN executions e INDEXED BY idx_executions_task_analytics ON e.task_id=p.task_id GROUP BY e.task_id
		), recurring_task_ids AS (
			SELECT t.id task_id FROM scoped_tasks t JOIN selected_terminal_task_ids p ON p.task_id=t.id WHERE t.category='scheduled'
			UNION SELECT s.task_id FROM schedules s JOIN selected_terminal_task_ids p ON p.task_id=s.task_id WHERE s.repeat_type<>'once'
		), usage AS (
			SELECT u.task_id,SUM(u.cost_usd) cost,MAX(CASE WHEN u.cost_usd IS NOT NULL THEN 1 ELSE 0 END) known
			FROM selected_evidence_tasks s CROSS JOIN llm_usage_events u INDEXED BY idx_llm_usage_events_task_project_time_cost
			ON u.task_id=s.task_id
			WHERE u.project_id=?` + usageWindow + ` GROUP BY u.task_id
		), model_ids AS (
			SELECT task_id,GROUP_CONCAT(DISTINCT agent_config_id) ids FROM selected_period_exec GROUP BY task_id
		), terminal_evidence AS (
			SELECT task_id,GROUP_CONCAT(DISTINCT (` + terminalPeriodExpr + ` || '|' || status)) statuses
			FROM selected_period_exec WHERE status IN ('completed','failed','cancelled') GROUP BY task_id
		)
	SELECT t.id,t.title,
		COALESCE((SELECT pe.status FROM period_exec pe WHERE pe.task_id=t.id ORDER BY pe.started_at DESC,pe.history_order DESC,pe.id DESC LIMIT 1),t.status),
		COALESCE(g.status,''),t.merge_status,COALESCE(a.id,''),COALESCE(a.name,'Unassigned / Auto-routed'),
		COALESCE((SELECT ac.name || ' (' || ac.model || ')' FROM period_exec pe LEFT JOIN agent_configs ac ON ac.id=pe.agent_config_id WHERE pe.task_id=t.id ORDER BY pe.started_at DESC,pe.history_order DESC LIMIT 1),'Unknown'),
		t.category,COALESCE((SELECT ht.status FROM historical_terminal ht WHERE ht.task_id=t.id AND ht.rn=1),''),t.created_at,
		COALESCE(MAX(p.started_at),''),COALESCE(` + periodExpr + `,''),COUNT(p.id),
		COALESCE(SUM(CASE WHEN p.status='completed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN p.is_followup=1 THEN 1 ELSE 0 END),0),
		CASE WHEN rt.task_id IS NULL THEN COALESCE(CAST(MAX(0,(julianday(MAX(CASE WHEN p.status IN ('completed','failed','cancelled') THEN COALESCE(p.completed_at,p.started_at) END))-julianday(hs.first_started_at))*86400000) AS INTEGER),0) ELSE 0 END,
		CASE WHEN pt.task_id IS NOT NULL THEN 1 ELSE 0 END,
		CASE WHEN eg.task_id IS NOT NULL THEN 1 ELSE 0 END,
		CASE WHEN eg.status='achieved' THEN 1 ELSE 0 END,
		CASE WHEN COUNT(p.id)>0 THEN 1 ELSE 0 END,
		CASE WHEN ct.task_id IS NOT NULL AND SUM(CASE WHEN p.status='completed' THEN 1 ELSE 0 END)>0 THEN 1 ELSE 0 END,
		CASE WHEN ct.task_id IS NOT NULL AND g.task_id IS NOT NULL AND g.status<>'cleared' THEN 1 ELSE 0 END,
		CASE WHEN ct.task_id IS NOT NULL AND eg.status='achieved' THEN 1 ELSE 0 END,
		CASE WHEN ct.task_id IS NOT NULL AND (t.worktree_path<>'' OR t.merge_status<>'') THEN 1 ELSE 0 END,
		CASE WHEN ct.task_id IS NOT NULL AND t.merge_status='merged' THEN 1 ELSE 0 END,
		CASE WHEN pt.task_id IS NOT NULL AND hs.first_started_at IS NOT NULL AND rt.task_id IS NULL THEN 1 ELSE 0 END,
		u.cost,u.known,COALESCE(mi.ids,''),COALESCE(te.statuses,''),
		COALESCE(GROUP_CONCAT(DISTINCT CAST(strftime('%H',p.started_at,'localtime') AS INTEGER)),'')
	FROM selected_evidence_tasks selected JOIN scoped_tasks t ON t.id=selected.task_id
	LEFT JOIN selected_period_exec p ON p.task_id=t.id
	LEFT JOIN period_terminal_task_ids pt ON pt.task_id=t.id
	LEFT JOIN created_task_ids ct ON ct.task_id=t.id
	LEFT JOIN historical_start hs ON hs.task_id=t.id
	LEFT JOIN recurring_task_ids rt ON rt.task_id=t.id
	LEFT JOIN task_goals g ON g.task_id=t.id
	LEFT JOIN evaluable_goals eg ON eg.task_id=t.id
	LEFT JOIN agents a ON a.id=t.agent_definition_id
	LEFT JOIN usage u ON u.task_id=t.id
	LEFT JOIN model_ids mi ON mi.task_id=t.id
	LEFT JOIN terminal_evidence te ON te.task_id=t.id
	GROUP BY t.id ORDER BY selected.sort_at DESC,t.id DESC`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, skillEvidenceArgs...)
	args = append(args, windowArgs...)
	args = append(args, taskWindowArgs...)
	args = append(args, goalWindowArgs...)
	args = append(args, filter.Limit, filter.EvidenceOffset)
	args = append(args, filter.ProjectID)
	args = append(args, usageWindowArgs...)
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
		var firstTerminalStatus, createdAt, modelIDs, terminalStatuses, executionHours string
		if err := rows.Scan(&row.TaskID, &row.TaskTitle, &row.TechnicalResult, &row.GoalResult, &row.MergeState, &row.AgentID, &row.AgentName, &row.Model, &row.Category, &firstTerminalStatus, &createdAt, &row.LatestStartedAt, &row.EvidencePeriod, &row.ExecutionCount, &row.PeriodCompletedCount, &row.PeriodFailedCount, &row.PeriodCancelledCount, &row.FollowUpCount, &row.CycleTimeMs, &row.FirstPassEligible, &row.GoalAchievementEligible, &row.GoalAchievedInPeriod, &row.StartedInPeriod, &row.FunnelTechnicalCompleted, &row.FunnelGoalEligible, &row.FunnelGoalAchieved, &row.FunnelMergeEligible, &row.FunnelMerged, &row.CycleEligible, &cost, &known, &modelIDs, &terminalStatuses, &executionHours); err != nil {
			return nil, err
		}
		row.FirstPassCompleted = row.FirstPassEligible && firstTerminalStatus == "completed"
		created := parseSQLiteTime(createdAt)
		row.CreatedInPeriod = (filter.DateFrom.IsZero() || !created.Before(filter.DateFrom)) && (filter.DateTo.IsZero() || created.Before(filter.DateTo))
		if modelIDs != "" {
			row.ModelConfigIDs = strings.Split(modelIDs, ",")
		} else {
			row.ModelConfigIDs = []string{}
		}
		if terminalStatuses != "" {
			row.TerminalPeriodStatuses = strings.Split(terminalStatuses, ",")
		} else {
			row.TerminalPeriodStatuses = []string{}
		}
		row.ExecutionHours = []int{}
		for _, value := range strings.Split(executionHours, ",") {
			hour, parseErr := strconv.Atoi(value)
			if parseErr == nil {
				row.ExecutionHours = append(row.ExecutionHours, hour)
			}
		}
		if known.Valid && known.Int64 > 0 && cost.Valid {
			value := cost.Float64
			row.KnownCostUSD = &value
		}
		row.KnownCostEligible = row.StartedInPeriod && row.GoalAchievedInPeriod && row.KnownCostUSD != nil
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryModelCategoryPerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.ModelCategoryPerformance, error) {
	window, windowArgs := analyticsWindowClause("e", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	query := `SELECT COALESCE(e.agent_config_id,''),COALESCE(ac.name || ' (' || ac.model || ')',e.agent_config_id,'Unknown'),COALESCE(t.category,''),
		COUNT(DISTINCT e.task_id),SUM(CASE WHEN e.status='completed' THEN 1 ELSE 0 END),
		SUM(CASE WHEN e.status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END)
		FROM executions e JOIN tasks t ON t.id=e.task_id LEFT JOIN agent_configs ac ON ac.id=e.agent_config_id
		WHERE t.project_id=?` + dimension + window + ` GROUP BY e.agent_config_id,2,3 HAVING COUNT(*)>0 ORDER BY COUNT(*) DESC,2,3 LIMIT 50`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting model category performance: %w", err)
	}
	defer rows.Close()
	result := []models.ModelCategoryPerformance{}
	for rows.Next() {
		var row models.ModelCategoryPerformance
		var completed, terminal int
		if err := rows.Scan(&row.ModelConfigID, &row.Model, &row.Category, &row.TasksEvaluated, &completed, &terminal); err != nil {
			return nil, err
		}
		if row.Category == "" {
			row.Category = "Uncategorized"
		}
		row.TechnicalCompletion = metric(completed, terminal)
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryModelPerformance(ctx context.Context, filter AnalyticsDashboardFilter) ([]models.ModelPerformance, error) {
	// Model evaluation includes scheduled and interactive tasks together, even
	// when an older saved URL or API client still sends a work-type filter.
	filter.WorkType = ""
	window, windowArgs := analyticsWindowClause("e", filter)
	usageWindow, usageArgs := analyticsEventWindowClause("u", "occurred_at", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	// Like the other tabs, count activity in the selected period, not lifetime
	// effort for a cohort of tasks finishing in that period.
	query := `WITH scoped_tasks AS (
		SELECT t.id,t.status,t.category,t.worktree_path,t.merge_status FROM tasks t WHERE t.project_id=? AND COALESCE(t.category,'')<>'chat'` + dimension + `
	), period_runs AS MATERIALIZED (
		SELECT e.task_id,e.agent_config_id,e.status,e.started_at,e.completed_at,e.is_followup
		FROM scoped_tasks t JOIN executions e ON e.task_id=t.id WHERE 1=1` + window + `
	), task_usage_events AS MATERIALIZED (
		SELECT u.task_id,u.agent_config_id,u.total_tokens,u.cost_usd,u.occurred_at
		FROM scoped_tasks t JOIN llm_usage_events u ON u.task_id=t.id
		WHERE (u.operation IN ('task','task_followup') OR (u.operation='' AND EXISTS (
			SELECT 1 FROM executions e WHERE e.id=u.execution_id AND e.task_id=t.id
		)))` + usageWindow + `
	), selected_tasks AS MATERIALIZED (
		SELECT t.*,a.activity_at completed_at FROM scoped_tasks t JOIN (
			SELECT task_id,MAX(activity_at) activity_at FROM (
				SELECT task_id,started_at activity_at FROM period_runs
				UNION ALL SELECT task_id,occurred_at FROM task_usage_events
			) GROUP BY task_id
		) a ON a.task_id=t.id
	), identities AS (
		SELECT e.task_id,COALESCE(e.agent_config_id,'') config_id
		FROM selected_tasks t JOIN executions e ON e.task_id=t.id
		UNION SELECT u.task_id,u.agent_config_id FROM task_usage_events u
		WHERE u.agent_config_id IS NOT NULL AND u.agent_config_id<>''
	), attribution AS (
		SELECT task_id,CASE WHEN COUNT(DISTINCT config_id)>1 THEN '__mixed__' ELSE MIN(config_id) END model_config_id
		FROM identities GROUP BY task_id
	), task_runs AS MATERIALIZED (
		SELECT t.id,a.model_config_id,COUNT(e.task_id) runs,
		SUM(CASE WHEN e.status='completed' THEN 1 ELSE 0 END) completed_runs,
		SUM(CASE WHEN e.status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal_runs,
		SUM(CASE WHEN e.is_followup=1 THEN 1 ELSE 0 END) followups,
		CAST(SUM(CASE WHEN e.completed_at IS NOT NULL THEN MAX(0,(julianday(e.completed_at)-julianday(e.started_at))*86400000) END) AS INTEGER) duration_ms
		FROM selected_tasks t JOIN attribution a ON a.task_id=t.id LEFT JOIN period_runs e ON e.task_id=t.id
		GROUP BY t.id
	), task_usage AS (
		SELECT u.task_id,SUM(u.total_tokens) tokens,SUM(u.cost_usd) cost
		FROM task_usage_events u GROUP BY u.task_id
	), ranked_durations AS (
		SELECT model_config_id,duration_ms,ROW_NUMBER() OVER(PARTITION BY model_config_id ORDER BY duration_ms) rn,
		COUNT(*) OVER(PARTITION BY model_config_id) n FROM task_runs WHERE duration_ms IS NOT NULL
	), durations AS (
		SELECT model_config_id,CAST(AVG(CASE WHEN rn IN ((n+1)/2,(n+2)/2) THEN duration_ms END) AS INTEGER) median_ms,
		MAX(CASE WHEN rn=((n*9+9)/10) THEN duration_ms END) p90_ms,MAX(n) n
		FROM ranked_durations GROUP BY model_config_id
	), trend_periods AS (
		SELECT tr.model_config_id,` + analyticsPeriodExpression(filter.GroupBy, "t.completed_at") + ` period,
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END) achieved,
		SUM(CASE WHEN g.status IN ('achieved','failed','active','paused','blocked') THEN 1 ELSE 0 END) goal_n,
		SUM(CASE WHEN t.merge_status='merged' THEN 1 ELSE 0 END) merged,
		SUM(CASE WHEN t.worktree_path<>'' OR t.merge_status<>'' THEN 1 ELSE 0 END) merge_n
		FROM task_runs tr JOIN selected_tasks t ON t.id=tr.id LEFT JOIN task_goals g ON g.task_id=t.id
		GROUP BY tr.model_config_id,period
	), trends AS (
		SELECT model_config_id,json_group_array(json_object('period',period,
		'goal_achievement',json_object('numerator',achieved,'denominator',goal_n),
		'merge_completion',json_object('numerator',merged,'denominator',merge_n))) points
		FROM (SELECT * FROM trend_periods ORDER BY model_config_id,period) GROUP BY model_config_id
	)
	SELECT tr.model_config_id,COALESCE(ac.name,'Unknown configuration'),COALESCE(ac.provider,''),COALESCE(ac.model,'Unknown'),COALESCE(ac.reasoning_effort,''),
		COUNT(*),SUM(tr.runs),SUM(tr.completed_runs),SUM(tr.terminal_runs),
		SUM(CASE WHEN tr.followups>0 THEN 1 ELSE 0 END),SUM(tr.followups),
		SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END),
		SUM(CASE WHEN g.status IN ('achieved','failed','active','paused','blocked') THEN 1 ELSE 0 END),
		SUM(CASE WHEN t.merge_status='merged' THEN 1 ELSE 0 END),
		SUM(CASE WHEN t.worktree_path<>'' OR t.merge_status<>'' THEN 1 ELSE 0 END),
		COALESCE(d.median_ms,0),COALESCE(d.p90_ms,0),COALESCE(d.n,0),
		COALESCE(SUM(u.tokens),0),COUNT(u.tokens),SUM(u.cost),COUNT(u.cost),COALESCE(trends.points,'[]'),
		json_group_array(json_object('period',` + analyticsPeriodExpression(filter.GroupBy, "t.completed_at") + `,'duration',tr.duration_ms,'tokens',u.tokens,'followups',tr.followups))
	FROM task_runs tr JOIN selected_tasks t ON t.id=tr.id
	LEFT JOIN task_goals g ON g.task_id=t.id LEFT JOIN task_usage u ON u.task_id=t.id
	LEFT JOIN durations d ON d.model_config_id=tr.model_config_id
	LEFT JOIN trends ON trends.model_config_id=tr.model_config_id
	LEFT JOIN agent_configs ac ON ac.id=tr.model_config_id
	GROUP BY tr.model_config_id ORDER BY COUNT(*) DESC,ac.name,tr.model_config_id`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	args = append(args, usageArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting model performance: %w", err)
	}
	defer rows.Close()
	result := []models.ModelPerformance{}
	for rows.Next() {
		var row models.ModelPerformance
		var completed, terminal, followed, followups, achieved, goalDenom, merged, mergeDenom int
		var knownCost sql.NullFloat64
		var trendJSON, effortJSON string
		if err := rows.Scan(&row.ModelConfigID, &row.ConfigName, &row.Provider, &row.Model, &row.ReasoningEffort,
			&row.TasksUsed, &row.RunCount, &completed, &terminal, &followed, &followups,
			&achieved, &goalDenom, &merged, &mergeDenom, &row.MedianDurationMs, &row.P90DurationMs, &row.DurationSampleSize,
			&row.TotalTokens, &row.TokenCoveredTasks, &knownCost, &row.CostCoveredTasks, &trendJSON, &effortJSON); err != nil {
			return nil, fmt.Errorf("scanning model performance: %w", err)
		}
		row.MixedModels = row.ModelConfigID == "__mixed__"
		row.EffortTrend, err = aggregateModelEffortTrend(effortJSON)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(trendJSON), &row.OutcomeTrend); err != nil {
			return nil, fmt.Errorf("decoding model outcome trend: %w", err)
		}
		for i := range row.OutcomeTrend {
			p := &row.OutcomeTrend[i]
			p.GoalAchievement = metric(p.GoalAchievement.Numerator, p.GoalAchievement.Denominator)
			p.MergeCompletion = metric(p.MergeCompletion.Numerator, p.MergeCompletion.Denominator)
		}
		if row.MixedModels {
			row.ConfigName, row.Model = "Mixed models", ""
		}
		row.TechnicalCompletion = metric(completed, terminal)
		row.GoalAchievement = metric(achieved, goalDenom)
		row.MergeCompletion = metric(merged, mergeDenom)
		row.FollowUp = metric(followed, row.TasksUsed)
		if row.TasksUsed > 0 {
			row.AverageRuns = float64(row.RunCount) / float64(row.TasksUsed)
			row.AverageFollowUps = float64(followups) / float64(row.TasksUsed)
		}
		if knownCost.Valid {
			value := knownCost.Float64
			row.KnownCostUSD = &value
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) queryAgentAnalyticsDetail(ctx context.Context, filter AnalyticsDashboardFilter, skills []models.SkillOutcomePerformance, recent []models.EvidenceTaskRow) (*models.AgentAnalyticsDetail, error) {
	filter.WorkType = ""
	detail := &models.AgentAnalyticsDetail{AgentID: filter.AgentID, OutcomeTrend: []models.AnalyticsTrendPoint{}, Categories: []models.AnalyticsCategoryPerformance{}, ModelMix: []models.AnalyticsModelMix{}, Failures: []models.AnalyticsFailurePattern{}, Skills: skills, RecentTasks: recent}
	window, windowArgs := analyticsWindowClause("e", filter)
	dimension, dimensionArgs := analyticsTaskDimensionClause("t", filter)
	dimension += " AND COALESCE(t.category,'')<>'chat'"
	periodExpr := analyticsPeriodExpression(filter.GroupBy, "e.started_at")
	query := `SELECT ` + periodExpr + `,SUM(CASE WHEN e.status='completed' THEN 1 ELSE 0 END),SUM(CASE WHEN e.status='failed' THEN 1 ELSE 0 END),SUM(CASE WHEN e.status='cancelled' THEN 1 ELSE 0 END),COUNT(*)
		FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + dimension + window + ` GROUP BY 1 ORDER BY 1`
	args := append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting Agent outcome trend: %w", err)
	}
	for rows.Next() {
		var row models.AnalyticsTrendPoint
		if err := rows.Scan(&row.Period, &row.Completed, &row.Failed, &row.Cancelled, &row.SampleSize); err != nil {
			rows.Close()
			return nil, err
		}
		detail.OutcomeTrend = append(detail.OutcomeTrend, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	goalWindow, goalArgs := analyticsGoalOutcomeWindowClause("g", filter)
	query = `WITH period_exec AS (
			SELECT e.task_id,e.status,e.is_followup,t.category FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + dimension + window + `
	), task_stats AS (
		SELECT task_id,COALESCE(category,'') category,SUM(CASE WHEN status='completed' THEN 1 ELSE 0 END) completed,
		SUM(CASE WHEN status IN ('completed','failed','cancelled') THEN 1 ELSE 0 END) terminal,MAX(CASE WHEN is_followup=1 THEN 1 ELSE 0 END) followed
		FROM period_exec GROUP BY task_id,category
	), goals AS (SELECT g.task_id,g.status FROM task_goals g WHERE g.status IN ('achieved','failed')` + goalWindow + `), evaluable_goals AS (
		SELECT task_id,status FROM goals UNION SELECT g.task_id,g.status FROM task_goals g JOIN task_stats s ON s.task_id=g.task_id AND s.terminal=1 WHERE g.status IN ('active','paused','blocked')
	)
	SELECT s.category,COUNT(*),SUM(s.completed),SUM(s.terminal),SUM(s.followed),SUM(CASE WHEN g.status='achieved' THEN 1 ELSE 0 END),COUNT(g.task_id)
	FROM task_stats s LEFT JOIN evaluable_goals g ON g.task_id=s.task_id GROUP BY s.category ORDER BY COUNT(*) DESC,s.category`
	args = append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	args = append(args, goalArgs...)
	rows, err = r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting Agent category performance: %w", err)
	}
	for rows.Next() {
		var row models.AnalyticsCategoryPerformance
		var completed, terminal, followed, achieved, goalDenom int
		if err := rows.Scan(&row.Category, &row.TasksEvaluated, &completed, &terminal, &followed, &achieved, &goalDenom); err != nil {
			rows.Close()
			return nil, err
		}
		if row.Category == "" {
			row.Category = "Uncategorized"
		}
		row.TechnicalCompletion = metric(completed, terminal)
		row.GoalAchievement = metric(achieved, goalDenom)
		row.FollowUp = metric(followed, row.TasksEvaluated)
		detail.Categories = append(detail.Categories, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	query = `SELECT COALESCE(e.agent_config_id,''),COALESCE(ac.name || ' (' || ac.model || ')',e.agent_config_id,'Unknown'),COUNT(*) FROM executions e JOIN tasks t ON t.id=e.task_id LEFT JOIN agent_configs ac ON ac.id=e.agent_config_id WHERE t.project_id=?` + dimension + window + ` GROUP BY e.agent_config_id,2 ORDER BY COUNT(*) DESC,2 LIMIT 20`
	args = append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	rows, err = r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting Agent model mix: %w", err)
	}
	for rows.Next() {
		var row models.AnalyticsModelMix
		if err := rows.Scan(&row.ModelConfigID, &row.Model, &row.ExecutionCount); err != nil {
			rows.Close()
			return nil, err
		}
		detail.ModelMix = append(detail.ModelMix, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	query = `SELECT t.id,t.title,COUNT(*),COALESCE(MAX(e.error_message),'') FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?` + dimension + window + ` AND e.status='failed' GROUP BY t.id,t.title ORDER BY COUNT(*) DESC,MAX(e.started_at) DESC LIMIT 10`
	args = append([]any{filter.ProjectID}, dimensionArgs...)
	args = append(args, windowArgs...)
	rows, err = r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting Agent failure patterns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row models.AnalyticsFailurePattern
		if err := rows.Scan(&row.TaskID, &row.TaskTitle, &row.FailureCount, &row.LastError); err != nil {
			return nil, err
		}
		detail.Failures = append(detail.Failures, row)
	}
	return detail, rows.Err()
}

func (r *ExecutionRepo) queryWorkflowAnalyticsDetail(ctx context.Context, filter AnalyticsDashboardFilter) (*models.WorkflowAnalyticsDetail, error) {
	detail := &models.WorkflowAnalyticsDetail{WorkflowID: filter.WorkflowID, Funnel: []models.AutomationFunnelPoint{}, Durations: []models.AutomationDurationPoint{}, Failures: []models.AutomationFailureSummary{}, Bottlenecks: []models.AutomationBottleneckSummary{}}
	var versionID string
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(published_version_id,'') FROM automations WHERE project_id=? AND id=? AND lifecycle_state<>'archived'`, filter.ProjectID, filter.WorkflowID).Scan(&versionID); err != nil {
		if err == sql.ErrNoRows {
			return detail, nil
		}
		return nil, err
	}
	if versionID == "" {
		return detail, nil
	}
	transitionWindow, transitionArgs := analyticsEventWindowClause("tr", "occurred_at", filter)
	query := `SELECT n.id,n.name,COUNT(DISTINCT CASE WHEN tr.state='entered' THEN tr.work_item_id END) FROM automation_nodes n
		LEFT JOIN automation_transitions tr ON tr.project_id=n.project_id AND tr.automation_id=n.automation_id AND tr.version_id=n.version_id AND tr.to_node_id=n.id` + transitionWindow + `
		WHERE n.project_id=? AND n.automation_id=? AND n.version_id=? GROUP BY n.id,n.name,n.position_x,n.position_y,n.node_key ORDER BY n.position_x,n.position_y,n.node_key`
	args := append([]any{}, transitionArgs...)
	args = append(args, filter.ProjectID, filter.WorkflowID, versionID)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var point models.AutomationFunnelPoint
		if err := rows.Scan(&point.NodeID, &point.NodeName, &point.EnteredCount); err != nil {
			rows.Close()
			return nil, err
		}
		detail.Funnel = append(detail.Funnel, point)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	base := 0
	for _, point := range detail.Funnel {
		if point.EnteredCount > 0 {
			base = point.EnteredCount
			break
		}
	}
	for i := range detail.Funnel {
		if base > 0 {
			detail.Funnel[i].ConversionPercent = float64(detail.Funnel[i].EnteredCount) * 100 / float64(base)
		}
	}

	query = `WITH ordered AS (SELECT work_item_id,to_node_id,occurred_at,id,LEAD(occurred_at) OVER(PARTITION BY work_item_id ORDER BY datetime(occurred_at),id) next_at FROM automation_transitions tr WHERE project_id=? AND automation_id=? AND version_id=?` + transitionWindow + `), entries AS (SELECT *,ROW_NUMBER() OVER(PARTITION BY work_item_id,to_node_id ORDER BY datetime(occurred_at),id) entry_rank FROM ordered)
		SELECT n.id,n.name,COUNT(*),AVG((julianday(e.next_at)-julianday(e.occurred_at))*86400.0) FROM entries e JOIN automation_nodes n ON n.id=e.to_node_id AND n.version_id=? WHERE e.entry_rank=1 AND e.next_at IS NOT NULL GROUP BY n.id,n.name ORDER BY n.position_x,n.position_y,n.node_key`
	args = []any{filter.ProjectID, filter.WorkflowID, versionID}
	args = append(args, transitionArgs...)
	args = append(args, versionID)
	rows, err = r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var point models.AutomationDurationPoint
		if err := rows.Scan(&point.NodeID, &point.NodeName, &point.SampleCount, &point.AverageSeconds); err != nil {
			rows.Close()
			return nil, err
		}
		detail.Durations = append(detail.Durations, point)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	activityWindow, activityArgs := analyticsEventWindowClause("aa", "completed_at", filter)
	query = `SELECT n.id,n.name,COUNT(*),MAX(aa.completed_at) FROM automation_activities aa JOIN automation_nodes n ON n.id=aa.node_id AND n.version_id=aa.version_id WHERE aa.project_id=? AND aa.automation_id=? AND aa.version_id=? AND aa.status='failed'` + activityWindow + ` GROUP BY n.id,n.name ORDER BY COUNT(*) DESC,MAX(aa.completed_at) DESC LIMIT 10`
	args = []any{filter.ProjectID, filter.WorkflowID, versionID}
	args = append(args, activityArgs...)
	rows, err = r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var row models.AutomationFailureSummary
		var last string
		if err := rows.Scan(&row.NodeID, &row.NodeName, &row.Count, &last); err != nil {
			rows.Close()
			return nil, err
		}
		row.LastFailure = parseSQLiteTime(last)
		detail.Failures = append(detail.Failures, row)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = r.db.QueryContext(ctx, `SELECT n.id,n.name,SUM(CASE WHEN p.state='waiting' THEN 1 ELSE 0 END),SUM(CASE WHEN p.state='blocked' THEN 1 ELSE 0 END) FROM automation_work_item_positions p JOIN automation_nodes n ON n.id=p.node_id AND n.version_id=p.version_id WHERE p.project_id=? AND p.automation_id=? GROUP BY n.id,n.name HAVING COUNT(*)>0 ORDER BY 3 DESC,4 DESC,n.name LIMIT 10`, filter.ProjectID, filter.WorkflowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row models.AutomationBottleneckSummary
		if err := rows.Scan(&row.NodeID, &row.NodeName, &row.Waiting, &row.Blocked); err != nil {
			return nil, err
		}
		detail.Bottlenecks = append(detail.Bottlenecks, row)
	}
	return detail, rows.Err()
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
			{"first_pass", "First-run success rate", "agents", current.FirstPass, previous.FirstPass, false},
			{"follow_up", "Follow-up run rate", "outcomes", current.FollowUp, previous.FollowUp, true},
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
			insights = append(insights, models.AnalyticsInsight{Kind: "attention", Title: workflow.WorkflowName + " has blocked work", Detail: fmt.Sprintf("%d blocked work items in current workflow state.", workflow.BlockedCount), MetricKey: "workflow_blocked", SampleSize: workflow.BlockedCount, ComparisonWindow: "current state", EvidenceView: "automations", EvidenceID: workflow.WorkflowID})
		}
	}
	return insights
}
