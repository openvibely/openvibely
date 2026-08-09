package models

import "time"

// LifecycleWhen names the points where the task runner can invoke a hook.
// The runner enters each `when` value; child hook executions never start new
// `when` values themselves.
type LifecycleWhen string

const (
	LifecycleRouteTask     LifecycleWhen = "route_task"
	LifecycleBeforeRun     LifecycleWhen = "before_run"
	LifecycleTaskMode      LifecycleWhen = "task_mode"
	LifecycleAfterComplete LifecycleWhen = "after_complete"
	LifecycleScheduled     LifecycleWhen = "scheduled"
)

// LifecycleOutputContract names the structured output shape a hook returns.
// The lifecycle runner validates the hook output against the named contract
// before applying it to the task run.
type LifecycleOutputContract string

const (
	OutputContractSelectedMode         LifecycleOutputContract = "selected_mode" // legacy; do not use for Skill Curator route_task.
	OutputContractSelectedSkills       LifecycleOutputContract = "selected_skills"
	OutputContractSelectedMemories     LifecycleOutputContract = "selected_memories"
	OutputContractContextBlock         LifecycleOutputContract = "context_block"
	OutputContractActivitySummary      LifecycleOutputContract = "activity_summary"
	OutputContractLearningSummary      LifecycleOutputContract = "learning_summary"
	OutputContractLibraryUpdateSummary LifecycleOutputContract = "library_update_summary"
)

// AgentLifecycleHook binds one agent skill to a lifecycle slot. The hook
// configuration is durable; each invocation produces an execution row.
type AgentLifecycleHook struct {
	ID              string                  `json:"id"`
	AgentID         string                  `json:"agent_id"`
	When            LifecycleWhen           `json:"when"`
	SkillKey        string                  `json:"skill_key"`
	PromptOverride  string                  `json:"prompt_override,omitempty"`
	OutputContract  LifecycleOutputContract `json:"output_contract"`
	Blocking        bool                    `json:"blocking"`
	Enabled         bool                    `json:"enabled"`
	PermissionsJSON string                  `json:"permissions_json,omitempty"`
	RunPolicyJSON   string                  `json:"run_policy_json,omitempty"`
	ScheduleJSON    string                  `json:"schedule_json,omitempty"`
	// PayloadJSON declares which lifecycle context blocks this hook's skill
	// reads, as {"blocks":["conversation_transcript",...]}. Empty or absent
	// means the hook receives every block the slot produced.
	PayloadJSON string `json:"payload_json,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

// LifecycleExecutionStatus reports the result of a hook execution. These
// values intentionally mirror the existing executions table so UI code can
// reuse status badges.
type LifecycleExecutionStatus string

const (
	LifecycleExecPending   LifecycleExecutionStatus = "pending"
	LifecycleExecRunning   LifecycleExecutionStatus = "running"
	LifecycleExecCompleted LifecycleExecutionStatus = "completed"
	LifecycleExecFailed    LifecycleExecutionStatus = "failed"
	LifecycleExecSkipped   LifecycleExecutionStatus = "skipped"
)

// MutationTargetType names the kind of artifact a skill maintenance proposal
// touched. The audit table stores every model-driven proposal, including ones
// the backend blocked, so debugging can answer why a skill or related support
// artifact did not change.
type MutationTargetType string

const (
	MutationTargetAgent       MutationTargetType = "agent"
	MutationTargetSkill       MutationTargetType = "skill"
	MutationTargetRouting     MutationTargetType = "routing"
	MutationTargetHook        MutationTargetType = "hook"
	MutationTargetSupportFile MutationTargetType = "support_file"
)

// MutationValidationStatus reports whether a proposal was applied, blocked by
// validation, or had no effect.
type MutationValidationStatus string

const (
	MutationStatusApplied MutationValidationStatus = "applied"
	MutationStatusBlocked MutationValidationStatus = "blocked"
	MutationStatusNoOp    MutationValidationStatus = "no_op"
)

// AgentConfigMutation is one durable audit row produced every time the model
// proposes a standalone skill change through skill_manage. Blocked rows preserve
// validation errors so future debugging can explain why a change was refused.
// The table name is historical; agents are now user-managed through the UI.
type AgentConfigMutation struct {
	ID                   string                   `json:"id"`
	LifecycleExecutionID string                   `json:"lifecycle_execution_id,omitempty"`
	TaskID               string                   `json:"task_id,omitempty"`
	TaskRunID            string                   `json:"task_run_id,omitempty"`
	ProjectID            string                   `json:"project_id,omitempty"`
	ActorAgentID         string                   `json:"actor_agent_id,omitempty"`
	TargetType           MutationTargetType       `json:"target_type"`
	TargetKey            string                   `json:"target_key,omitempty"`
	Action               string                   `json:"action"`
	ProposedPayloadJSON  string                   `json:"proposed_payload_json,omitempty"`
	ValidationStatus     MutationValidationStatus `json:"validation_status"`
	ValidationErrorsJSON string                   `json:"validation_errors_json,omitempty"`
	ChangedPathsJSON     string                   `json:"changed_paths_json,omitempty"`
	ImportedChangesJSON  string                   `json:"imported_config_changes_json,omitempty"`
	EvidenceRefsJSON     string                   `json:"evidence_refs_json,omitempty"`
	IdempotencyKey       string                   `json:"idempotency_key,omitempty"`
	CreatedAt            time.Time                `json:"created_at"`
}

// LifecycleExecution records one invocation of an agent skill by the
// lifecycle runner. Each execution always records the task run it belongs to
// and the lifecycle hook (if any) that created it.
type LifecycleExecution struct {
	ID              string                   `json:"id"`
	TaskID          string                   `json:"task_id"`
	TaskRunID       string                   `json:"task_run_id,omitempty"`
	AgentID         string                   `json:"agent_id"`
	When            LifecycleWhen            `json:"when"`
	LifecycleHookID *string                  `json:"lifecycle_hook_id,omitempty"`
	ParentExecID    *string                  `json:"parent_execution_id,omitempty"`
	SkillKey        string                   `json:"skill_key"`
	OutputContract  LifecycleOutputContract  `json:"output_contract"`
	Status          LifecycleExecutionStatus `json:"status"`
	InputJSON       string                   `json:"input_json,omitempty"`
	OutputJSON      string                   `json:"output_json,omitempty"`
	Error           string                   `json:"error,omitempty"`
	AttemptCount    int                      `json:"attempt_count"`
	Priority        int                      `json:"priority"`
	NextRetryAt     *time.Time               `json:"next_retry_at,omitempty"`
	IdempotencyKey  string                   `json:"idempotency_key,omitempty"`
	StartedAt       time.Time                `json:"started_at"`
	CompletedAt     *time.Time               `json:"completed_at,omitempty"`
}

// LifecycleExecutionEvent records one prompt-safe trace event emitted while a
// lifecycle execution ran. PayloadJSON is intentionally generic so provider/tool
// traces can evolve without schema churn.
type LifecycleExecutionEvent struct {
	ID                   string    `json:"id"`
	LifecycleExecutionID string    `json:"lifecycle_execution_id"`
	Seq                  int       `json:"seq"`
	EventType            string    `json:"event_type"`
	PayloadJSON          string    `json:"payload_json"`
	CreatedAt            time.Time `json:"created_at"`
}
