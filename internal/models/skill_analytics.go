package models

import "time"

const (
	SkillScopeProject    = "project"
	SkillScopeGlobal     = "global"
	SkillScopeAgentOwned = "agent_owned"

	SkillEventSelected = "selected"
	SkillEventLoaded   = "loaded"
	SkillEventViewed   = "viewed"
	SkillEventCreated  = "created"
	SkillEventEdited   = "edited"

	SkillEventSourceSkillCurator  = "skill_curator"
	SkillEventSourceAlwaysUse     = "always_use"
	SkillEventSourceManual        = "manual"
	SkillEventSourceAssignedAgent = "assigned_agent"
	SkillEventSourceLifecycleHook = "lifecycle_hook"
	SkillEventSourceSystem        = "system"

	SkillSurfaceChat             = "chat"
	SkillSurfaceTaskThread       = "task_thread"
	SkillSurfaceScheduledTask    = "scheduled_task"
	SkillSurfaceLifecycleHook    = "lifecycle_hook"
	SkillSurfaceChannel          = "channel"
	SkillSurfaceGoalContinuation = "goal_continuation"
)

type SkillAnalyticsEvent struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	ProjectID   string    `json:"project_id,omitempty"`
	TaskID      string    `json:"task_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	ThreadID    string    `json:"thread_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	SkillScope  string    `json:"skill_scope"`
	SkillHandle string    `json:"skill_handle"`
	EventType   string    `json:"event_type"`
	Source      string    `json:"source"`
	Surface     string    `json:"surface"`
}

type SkillAnalyticsDashboard struct {
	UsageOverTime []SkillUsagePeriodMetric    `json:"usage_over_time"`
	TopSkills     []SkillAnalyticsSkillMetric `json:"top_skills"`
	FollowThrough []SkillFollowThroughMetric  `json:"follow_through"`
	AgentUsage    SkillAgentUsageHeatmap      `json:"agent_usage"`
	Underused     []UnderusedSkillMetric      `json:"underused"`
	Evidence      []SkillAnalyticsEvidenceRow `json:"evidence"`
	EvidenceTotal int                         `json:"evidence_total"`
}

type SkillAnalyticsEvidenceRow struct {
	ID          string    `json:"id"`
	CreatedAt   time.Time `json:"created_at"`
	ProjectID   string    `json:"project_id"`
	TaskID      string    `json:"task_id,omitempty"`
	ExecutionID string    `json:"execution_id,omitempty"`
	AgentID     string    `json:"agent_id,omitempty"`
	AgentName   string    `json:"agent_name"`
	SkillHandle string    `json:"skill_handle"`
	SkillScope  string    `json:"skill_scope"`
	EventType   string    `json:"event_type"`
	Source      string    `json:"source"`
	Surface     string    `json:"surface"`
}

type SkillUsagePeriodMetric struct {
	Period        string `json:"period"`
	SelectedCount int    `json:"selected_count"`
	LoadedCount   int    `json:"loaded_count"`
	ViewedCount   int    `json:"viewed_count"`
	CreatedCount  int    `json:"created_count"`
	EditedCount   int    `json:"edited_count"`
	ActivityCount int    `json:"activity_count"`
}

type SkillAnalyticsSkillMetric struct {
	SkillHandle       string     `json:"skill_handle"`
	SkillScope        string     `json:"skill_scope"`
	SelectedCount     int        `json:"selected_count"`
	LoadedCount       int        `json:"loaded_count"`
	ViewedCount       int        `json:"viewed_count"`
	CreatedCount      int        `json:"created_count"`
	EditedCount       int        `json:"edited_count"`
	ActivityCount     int        `json:"activity_count"`
	FollowThroughRate *float64   `json:"follow_through_rate,omitempty"`
	LastActivity      *time.Time `json:"last_activity,omitempty"`
}

type SkillFollowThroughMetric struct {
	SkillHandle    string `json:"skill_handle"`
	SkillScope     string `json:"skill_scope"`
	SelectedCount  int    `json:"selected_count"`
	LoadedOrViewed int    `json:"loaded_or_viewed_count"`
	IgnoredCount   int    `json:"ignored_count"`
}

type SkillAgentUsageHeatmap struct {
	Agents     []SkillAgentUsageAgent `json:"agents"`
	Skills     []string               `json:"skills"`
	SkillPairs []SkillAgentUsageSkill `json:"skill_pairs"`
	Cells      []SkillAgentUsageCell  `json:"cells"`
}

type SkillAgentUsageSkill struct {
	SkillHandle string `json:"skill_handle"`
	SkillScope  string `json:"skill_scope"`
}

type SkillAgentUsageAgent struct {
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
}

type SkillAgentUsageCell struct {
	AgentID       string `json:"agent_id"`
	AgentName     string `json:"agent_name"`
	SkillHandle   string `json:"skill_handle"`
	SkillScope    string `json:"skill_scope"`
	SelectedCount int    `json:"selected_count"`
	LoadedCount   int    `json:"loaded_count"`
	ViewedCount   int    `json:"viewed_count"`
	CreatedCount  int    `json:"created_count"`
	EditedCount   int    `json:"edited_count"`
	ActivityCount int    `json:"activity_count"`
}

type UnderusedSkillMetric struct {
	SkillHandle   string     `json:"skill_handle"`
	SkillScope    string     `json:"skill_scope"`
	Enabled       bool       `json:"enabled"`
	AlwaysUse     bool       `json:"always_use"`
	SelectedCount int        `json:"selected_count"`
	LoadedCount   int        `json:"loaded_count"`
	ViewedCount   int        `json:"viewed_count"`
	CreatedCount  int        `json:"created_count"`
	EditedCount   int        `json:"edited_count"`
	ActivityCount int        `json:"activity_count"`
	LastActivity  *time.Time `json:"last_activity,omitempty"`
}
