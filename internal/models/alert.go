package models

import "time"

type AlertType string

const (
	AlertTaskFailed        AlertType = "task_failed"
	AlertTaskNeedsFollowup AlertType = "task_needs_followup"
	AlertCustom            AlertType = "custom"
)

type AlertScope string

const AlertScopeProject AlertScope = "project"

// AlertAutomationProvenanceMetadataKey identifies server-owned Automation routing metadata.
// Runtime creation overwrites any model-supplied value at this key.
const AlertAutomationProvenanceMetadataKey = "openvibely_automation_provenance"

type AlertDecisionState string

const (
	AlertDecisionNotRequired AlertDecisionState = "not_required"
	AlertDecisionPending     AlertDecisionState = "pending"
	AlertDecisionApproved    AlertDecisionState = "approved"
	AlertDecisionRejected    AlertDecisionState = "rejected"
	AlertDecisionDismissed   AlertDecisionState = "dismissed"
)

type AlertProcessingState string

const (
	AlertProcessingNotApplicable            AlertProcessingState = "not_applicable"
	AlertProcessingUnclaimed                AlertProcessingState = "unclaimed"
	AlertProcessingClaimed                  AlertProcessingState = "claimed"
	AlertProcessingImplementationTaskLinked AlertProcessingState = "implementation_task_linked"
	AlertProcessingCompleted                AlertProcessingState = "completed"
	AlertProcessingFailed                   AlertProcessingState = "failed"
)

type AlertSeverity string

const (
	SeverityInfo    AlertSeverity = "info"
	SeverityWarning AlertSeverity = "warning"
	SeverityError   AlertSeverity = "error"
)

type Alert struct {
	ID                   string               `json:"id"`
	ProjectID            string               `json:"project_id"`
	Scope                AlertScope           `json:"scope"`
	TaskID               *string              `json:"task_id,omitempty"`
	ExecutionID          *string              `json:"execution_id,omitempty"`
	SourceTaskID         *string              `json:"source_task_id,omitempty"`
	Type                 AlertType            `json:"type"`
	Severity             AlertSeverity        `json:"severity"`
	Title                string               `json:"title"`
	Message              string               `json:"message"`
	Body                 string               `json:"body"`
	Source               string               `json:"source"`
	Metadata             map[string]any       `json:"metadata"`
	IdempotencyKey       string               `json:"idempotency_key,omitempty"`
	DecisionState        AlertDecisionState   `json:"decision_state"`
	DecidedAt            *time.Time           `json:"decided_at,omitempty"`
	ProcessingState      AlertProcessingState `json:"processing_state"`
	Claimant             string               `json:"claimant,omitempty"`
	ClaimedAt            *time.Time           `json:"claimed_at,omitempty"`
	ClaimExpiresAt       *time.Time           `json:"claim_expires_at,omitempty"`
	ImplementationTaskID *string              `json:"implementation_task_id,omitempty"`
	ProcessingError      string               `json:"processing_error,omitempty"`
	IsRead               bool                 `json:"is_read"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
	AutomationContext    *AutomationContext   `json:"-"`
}

// AlertSummary is the bounded runtime/list projection for notification triage.
// Full body text and structured metadata are intentionally available only from
// Alert detail lookups.
type AlertSummary struct {
	ID                   string               `json:"id"`
	ProjectID            string               `json:"project_id"`
	Scope                AlertScope           `json:"scope"`
	TaskID               *string              `json:"task_id,omitempty"`
	ExecutionID          *string              `json:"execution_id,omitempty"`
	SourceTaskID         *string              `json:"source_task_id,omitempty"`
	Type                 AlertType            `json:"type"`
	Severity             AlertSeverity        `json:"severity"`
	Title                string               `json:"title"`
	Message              string               `json:"message"`
	Source               string               `json:"source"`
	IdempotencyKey       string               `json:"idempotency_key,omitempty"`
	DecisionState        AlertDecisionState   `json:"decision_state"`
	DecidedAt            *time.Time           `json:"decided_at,omitempty"`
	ProcessingState      AlertProcessingState `json:"processing_state"`
	Claimant             string               `json:"claimant,omitempty"`
	ClaimedAt            *time.Time           `json:"claimed_at,omitempty"`
	ClaimExpiresAt       *time.Time           `json:"claim_expires_at,omitempty"`
	ImplementationTaskID *string              `json:"implementation_task_id,omitempty"`
	ProcessingError      string               `json:"processing_error,omitempty"`
	IsRead               bool                 `json:"is_read"`
	CreatedAt            time.Time            `json:"created_at"`
	UpdatedAt            time.Time            `json:"updated_at"`
}

type AlertListFilter struct {
	DecisionState            AlertDecisionState
	ProcessingState          AlertProcessingState
	Type                     AlertType
	Severity                 AlertSeverity
	Source                   string
	Read                     *bool
	Sort                     string
	ImplementationTaskLinked *bool
	AutomationInboxBindings  []AutomationBinding
	Search                   string
	Limit                    int
	Offset                   int
}

type AlertImplementationTaskInput struct {
	Title    string  `json:"title"`
	Prompt   string  `json:"prompt"`
	Goal     string  `json:"goal"`
	Priority int     `json:"priority"`
	Tag      TaskTag `json:"tag"`
	AgentID  string  `json:"agent_id,omitempty"`
}
