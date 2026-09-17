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
	CycleSampleSize           int             `json:"cycle_sample_size"`
	CancelledExecutionCount   int             `json:"cancelled_execution_count"`
	TasksEvaluated            int             `json:"tasks_evaluated"`
	KnownCostPerAchievedGoal  *CostCoverage   `json:"known_cost_per_achieved_goal,omitempty"`
	KnownCostPerCompletedTask *CostCoverage   `json:"known_cost_per_completed_task,omitempty"`
	KnownFailedExecutionCost  *CostCoverage   `json:"known_failed_execution_cost,omitempty"`
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
	DurationSampleSize       int             `json:"duration_sample_size"`
	KnownCostPerAchievedGoal *CostCoverage   `json:"known_cost_per_achieved_goal,omitempty"`
	MostUsedModel            string          `json:"most_used_model,omitempty"`
}

type SkillOutcomePerformance struct {
	SkillHandle         string          `json:"skill_handle"`
	SkillScope          string          `json:"skill_scope"`
	TasksEvaluated      int             `json:"tasks_evaluated"`
	TechnicalCompletion AnalyticsMetric `json:"technical_completion"`
	GoalAchievement     AnalyticsMetric `json:"goal_achievement"`
	FollowUp            AnalyticsMetric `json:"follow_up"`
}

type AgentSkillOutcomePerformance struct {
	AgentID             string          `json:"agent_id,omitempty"`
	AgentName           string          `json:"agent_name"`
	SkillHandle         string          `json:"skill_handle"`
	SkillScope          string          `json:"skill_scope"`
	TasksEvaluated      int             `json:"tasks_evaluated"`
	TechnicalCompletion AnalyticsMetric `json:"technical_completion"`
	GoalAchievement     AnalyticsMetric `json:"goal_achievement"`
	FollowUp            AnalyticsMetric `json:"follow_up"`
}

type OutcomeTrendPoint struct {
	Period              string          `json:"period"`
	TechnicalCompletion AnalyticsMetric `json:"technical_completion"`
	GoalAchievement     AnalyticsMetric `json:"goal_achievement"`
	FirstPass           AnalyticsMetric `json:"first_pass"`
	FollowUp            AnalyticsMetric `json:"follow_up"`
}

type AnalyticsTrendPoint struct {
	Period     string `json:"period"`
	Completed  int    `json:"completed"`
	Failed     int    `json:"failed"`
	Cancelled  int    `json:"cancelled"`
	SampleSize int    `json:"sample_size"`
}

type AnalyticsCategoryPerformance struct {
	Category            string          `json:"category"`
	TasksEvaluated      int             `json:"tasks_evaluated"`
	TechnicalCompletion AnalyticsMetric `json:"technical_completion"`
	GoalAchievement     AnalyticsMetric `json:"goal_achievement"`
	FollowUp            AnalyticsMetric `json:"follow_up"`
}

type AnalyticsModelMix struct {
	ModelConfigID  string `json:"model_config_id,omitempty"`
	Model          string `json:"model"`
	ExecutionCount int    `json:"execution_count"`
}

type AnalyticsFailurePattern struct {
	TaskID       string `json:"task_id"`
	TaskTitle    string `json:"task_title"`
	FailureCount int    `json:"failure_count"`
	LastError    string `json:"last_error"`
}

type AgentAnalyticsDetail struct {
	AgentID      string                         `json:"agent_id"`
	OutcomeTrend []AnalyticsTrendPoint          `json:"outcome_trend"`
	Categories   []AnalyticsCategoryPerformance `json:"categories"`
	ModelMix     []AnalyticsModelMix            `json:"model_mix"`
	Failures     []AnalyticsFailurePattern      `json:"failures"`
	Skills       []SkillOutcomePerformance      `json:"skills"`
	RecentTasks  []EvidenceTaskRow              `json:"recent_tasks"`
}

type ModelCategoryPerformance struct {
	ModelConfigID       string          `json:"model_config_id,omitempty"`
	Model               string          `json:"model"`
	Category            string          `json:"category"`
	TasksEvaluated      int             `json:"tasks_evaluated"`
	TechnicalCompletion AnalyticsMetric `json:"technical_completion"`
}

type WorkflowAnalyticsDetail struct {
	WorkflowID  string                        `json:"workflow_id"`
	Funnel      []AutomationFunnelPoint       `json:"funnel"`
	Durations   []AutomationDurationPoint     `json:"durations"`
	Failures    []AutomationFailureSummary    `json:"failures"`
	Bottlenecks []AutomationBottleneckSummary `json:"bottlenecks"`
}

type WorkflowPerformance struct {
	WorkflowID         string  `json:"workflow_id"`
	WorkflowName       string  `json:"workflow_name"`
	InvocationCount    int     `json:"invocation_count"`
	CompletedCount     int     `json:"completed_count"`
	FailedCount        int     `json:"failed_count"`
	CancelledCount     int     `json:"cancelled_count"`
	SkippedCount       int     `json:"skipped_count"`
	OpenCount          int     `json:"open_count"`
	CompletionRate     float64 `json:"completion_rate"`
	AverageDurationMs  int64   `json:"average_duration_ms"`
	DurationSampleSize int     `json:"duration_sample_size"`
	WaitingCount       int     `json:"waiting_count"`
	BlockedCount       int     `json:"blocked_count"`
	Health             string  `json:"health"`
}

type EvidenceTaskRow struct {
	TaskID                   string   `json:"task_id"`
	TaskTitle                string   `json:"task_title"`
	TechnicalResult          string   `json:"technical_result"`
	GoalResult               string   `json:"goal_result,omitempty"`
	MergeState               string   `json:"merge_state,omitempty"`
	AgentID                  string   `json:"agent_id,omitempty"`
	AgentName                string   `json:"agent_name"`
	Model                    string   `json:"model"`
	Category                 string   `json:"category"`
	ModelConfigIDs           []string `json:"model_config_ids"`
	ExecutionHours           []int    `json:"execution_hours"`
	TerminalPeriodStatuses   []string `json:"terminal_period_statuses"`
	FirstPassCompleted       bool     `json:"first_pass_completed"`
	FirstPassEligible        bool     `json:"first_pass_eligible"`
	GoalAchievementEligible  bool     `json:"goal_achievement_eligible"`
	GoalAchievedInPeriod     bool     `json:"goal_achieved_in_period"`
	CreatedInPeriod          bool     `json:"created_in_period"`
	StartedInPeriod          bool     `json:"started_in_period"`
	FunnelTechnicalCompleted bool     `json:"funnel_technical_completed"`
	FunnelGoalEligible       bool     `json:"funnel_goal_eligible"`
	FunnelGoalAchieved       bool     `json:"funnel_goal_achieved"`
	FunnelMergeEligible      bool     `json:"funnel_merge_eligible"`
	FunnelMerged             bool     `json:"funnel_merged"`
	CycleEligible            bool     `json:"cycle_eligible"`
	KnownCostEligible        bool     `json:"known_cost_eligible"`
	LatestStartedAt          string   `json:"latest_started_at,omitempty"`
	EvidencePeriod           string   `json:"evidence_period,omitempty"`
	ExecutionCount           int      `json:"execution_count"`
	PeriodCompletedCount     int      `json:"period_completed_count"`
	PeriodFailedCount        int      `json:"period_failed_count"`
	PeriodCancelledCount     int      `json:"period_cancelled_count"`
	FollowUpCount            int      `json:"follow_up_count"`
	CycleTimeMs              int64    `json:"cycle_time_ms"`
	KnownCostUSD             *float64 `json:"known_cost_usd,omitempty"`
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
	EvidenceID       string  `json:"evidence_id,omitempty"`
}

type AnalyticsDashboard struct {
	Definitions          []MetricDefinition             `json:"definitions"`
	Current              OutcomeMetrics                 `json:"current"`
	Previous             *OutcomeMetrics                `json:"previous,omitempty"`
	Funnel               []OutcomeFunnelStage           `json:"funnel"`
	OutcomeTrend         []OutcomeTrendPoint            `json:"outcome_trend"`
	CycleDistribution    []AnalyticsDistributionPoint   `json:"cycle_distribution"`
	FollowUpDistribution []AnalyticsDistributionPoint   `json:"follow_up_distribution"`
	Agents               []AgentPerformance             `json:"agents"`
	AgentDetail          *AgentAnalyticsDetail          `json:"agent_detail,omitempty"`
	SkillOutcomes        []SkillOutcomePerformance      `json:"skill_outcomes"`
	AgentSkillOutcomes   []AgentSkillOutcomePerformance `json:"agent_skill_outcomes"`
	ModelCategories      []ModelCategoryPerformance     `json:"model_categories"`
	Workflows            []WorkflowPerformance          `json:"workflows"`
	WorkflowDetail       *WorkflowAnalyticsDetail       `json:"workflow_detail,omitempty"`
	RecentOutcomes       []EvidenceTaskRow              `json:"recent_outcomes"`
	EvidenceTotal        int                            `json:"evidence_total"`
	EvidenceLimit        int                            `json:"evidence_limit"`
	EvidenceOffset       int                            `json:"evidence_offset"`
	Insights             []AnalyticsInsight             `json:"insights"`
}
