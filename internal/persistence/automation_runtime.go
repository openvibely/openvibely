package persistence

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"time"
)

var (
	ErrAutomationScheduleChanged        = errors.New("automation schedule occurrence changed")
	ErrAutomationNotActive              = errors.New("automation must be active to run now")
	ErrAutomationNoScheduleEntries      = errors.New("automation has no runnable schedule entries")
	ErrAutomationDispatchLease          = errors.New("automation dispatch lease is not owned")
	ErrAutomationTaskBusy               = errors.New("automation task is not available")
	ErrAutomationExternalReconciliation = errors.New("automation external mutation requires reconciliation")
)

const AutomationExternalStaleAfter = 15 * time.Minute

type AutomationGitHubIssueDedupSource struct {
	Context        models.AutomationContext               `json:"context"`
	TaskID         string                                 `json:"task_id"`
	ExecutionID    string                                 `json:"execution_id"`
	StableBindings []AutomationGitHubIssueDedupNodeSource `json:"stable_bindings,omitempty"`
}

type AutomationGitHubIssueDedupNodeSource struct {
	AutomationID string `json:"automation_id"`
	NodeKey      string `json:"node_key"`
	Role         string `json:"role"`
}

type AutomationGitHubIssueDedupClaim struct {
	IssueNumber int
	OwnerToken  string
	Source      AutomationGitHubIssueDedupSource
}

type AutomationProjectionEvent struct {
	Context        models.AutomationContext
	Binding        models.AutomationBinding
	WorkItemKey    string
	WorkItemKind   string
	WorkItemTitle  string
	WorkItemStatus models.AutomationWorkItemStatus
	ActivityKey    string
	ActivityType   string
	ActivityStatus models.AutomationActivityStatus
	Resources      []models.AutomationActivityResource
	EventKey       string
	FromNodeID     string
	ToNodeID       string
	EdgeID         string
	Transition     models.AutomationTransitionState
	MetadataJSON   string
}

type AutomationGitHubIssueTaskCreation struct {
	ProjectID       string
	ExecutionID     string
	IssueResourceID string
	SourceBinding   models.AutomationBinding
	TargetNodeID    string
	Task            *models.Task
	Goal            *models.TaskGoal
}

type AutomationGitHubIssueTaskProvenance struct {
	ProjectID             string
	AutomationID          string
	TaskID                string
	IssueResourceID       string
	ImplementationNodeKey string
}

type CustomAutomationTaskHandoff struct {
	Node   models.AutomationNode
	TaskID string
}

type AutomationExecutionProjectionRepair struct {
	ProjectID   string
	ExecutionID string
	Status      models.ExecutionStatus
	Error       string
}
