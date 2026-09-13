package models

// MetricDefinition documents one dashboard calculation and its denominator.
type MetricDefinition struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Definition  string `json:"definition"`
	Denominator string `json:"denominator"`
}

// AnalyticsMetric is a ratio with the evidence population disclosed explicitly.
type AnalyticsMetric struct {
	Numerator   int     `json:"numerator"`
	Denominator int     `json:"denominator"`
	SampleSize  int     `json:"sample_size"`
	Percent     float64 `json:"percent"`
}

// CostCoverage reports an average only across eligible tasks with recorded cost.
type CostCoverage struct {
	Value    float64 `json:"value"`
	Covered  int     `json:"covered"`
	Eligible int     `json:"eligible"`
}

// OutcomeMetrics keeps technical execution state separate from task-goal state.
type OutcomeMetrics struct {
	TechnicalCompletion       AnalyticsMetric `json:"technical_completion"`
	GoalAchievement           AnalyticsMetric `json:"goal_achievement"`
	FirstPass                 AnalyticsMetric `json:"first_pass"`
	FollowUp                  AnalyticsMetric `json:"follow_up"`
	MedianCycleTimeMs         int64           `json:"median_cycle_time_ms"`
	P90CycleTimeMs            int64           `json:"p90_cycle_time_ms"`
	TasksEvaluated            int             `json:"tasks_evaluated"`
	KnownCostPerAchievedGoal  *CostCoverage   `json:"known_cost_per_achieved_goal,omitempty"`
	KnownCostPerCompletedTask *CostCoverage   `json:"known_cost_per_completed_task,omitempty"`
	KnownFailedExecutionCost  float64         `json:"known_failed_execution_cost"`
	TokensPerAchievedGoal     *CostCoverage   `json:"tokens_per_achieved_goal,omitempty"`
}

type OutcomeFunnelStage struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Count       int    `json:"count"`
	Denominator int    `json:"denominator"`
}

type AnalyticsDistributionPoint struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type AgentPerformance struct {
	AgentID                  string          `json:"agent_id,omitempty"`
	AgentName                string          `json:"agent_name"`
	TasksEvaluated           int             `json:"tasks_evaluated"`
	TechnicalCompletion      AnalyticsMetric `json:"technical_completion"`
	GoalAchievement          AnalyticsMetric `json:"goal_achievement"`
	FirstPass                AnalyticsMetric `json:"first_pass"`
	FollowUp                 AnalyticsMetric `json:"follow_up"`
	MedianDurationMs         int64           `json:"median_duration_ms"`
	KnownCostPerAchievedGoal *CostCoverage   `json:"known_cost_per_achieved_goal,omitempty"`
	MostUsedModel            string          `json:"most_used_model,omitempty"`
}

type SkillOutcomePerformance struct {
	SkillHandle         string          `json:"skill_handle"`
	TasksEvaluated      int             `json:"tasks_evaluated"`
	TechnicalCompletion AnalyticsMetric `json:"technical_completion"`
	GoalAchievement     AnalyticsMetric `json:"goal_achievement"`
	FollowUp            AnalyticsMetric `json:"follow_up"`
}

type WorkflowPerformance struct {
	WorkflowID        string  `json:"workflow_id"`
	WorkflowName      string  `json:"workflow_name"`
	InvocationCount   int     `json:"invocation_count"`
	CompletedCount    int     `json:"completed_count"`
	FailedCount       int     `json:"failed_count"`
	CompletionRate    float64 `json:"completion_rate"`
	AverageDurationMs int64   `json:"average_duration_ms"`
	WaitingCount      int     `json:"waiting_count"`
	BlockedCount      int     `json:"blocked_count"`
	Health            string  `json:"health"`
}

type EvidenceTaskRow struct {
	TaskID          string   `json:"task_id"`
	TaskTitle       string   `json:"task_title"`
	TechnicalResult string   `json:"technical_result"`
	GoalResult      string   `json:"goal_result,omitempty"`
	MergeState      string   `json:"merge_state,omitempty"`
	AgentID         string   `json:"agent_id,omitempty"`
	AgentName       string   `json:"agent_name"`
	Model           string   `json:"model"`
	ExecutionCount  int      `json:"execution_count"`
	FollowUpCount   int      `json:"follow_up_count"`
	CycleTimeMs     int64    `json:"cycle_time_ms"`
	KnownCostUSD    *float64 `json:"known_cost_usd,omitempty"`
}

type AnalyticsInsight struct {
	Kind             string  `json:"kind"`
	Title            string  `json:"title"`
	Detail           string  `json:"detail"`
	MetricKey        string  `json:"metric_key"`
	CurrentPercent   float64 `json:"current_percent"`
	PreviousPercent  float64 `json:"previous_percent"`
	SampleSize       int     `json:"sample_size"`
	ComparisonWindow string  `json:"comparison_window"`
	EvidenceView     string  `json:"evidence_view"`
}

type AnalyticsDashboard struct {
	Definitions          []MetricDefinition           `json:"definitions"`
	Current              OutcomeMetrics               `json:"current"`
	Previous             *OutcomeMetrics              `json:"previous,omitempty"`
	Funnel               []OutcomeFunnelStage         `json:"funnel"`
	CycleDistribution    []AnalyticsDistributionPoint `json:"cycle_distribution"`
	FollowUpDistribution []AnalyticsDistributionPoint `json:"follow_up_distribution"`
	Agents               []AgentPerformance           `json:"agents"`
	SkillOutcomes        []SkillOutcomePerformance    `json:"skill_outcomes"`
	Workflows            []WorkflowPerformance        `json:"workflows"`
	RecentOutcomes       []EvidenceTaskRow            `json:"recent_outcomes"`
	Insights             []AnalyticsInsight           `json:"insights"`
}
