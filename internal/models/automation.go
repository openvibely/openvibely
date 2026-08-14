package models

import "time"

type AutomationLifecycleState string
type AutomationHealthState string
type AutomationVersionState string

type AutomationNodeType string

const (
	AutomationDraft    AutomationLifecycleState = "draft"
	AutomationActive   AutomationLifecycleState = "active"
	AutomationPaused   AutomationLifecycleState = "paused"
	AutomationArchived AutomationLifecycleState = "archived"

	AutomationHealthUnknown   AutomationHealthState = "unknown"
	AutomationHealthHealthy   AutomationHealthState = "healthy"
	AutomationHealthDegraded  AutomationHealthState = "degraded"
	AutomationHealthUnhealthy AutomationHealthState = "unhealthy"

	AutomationVersionDraft     AutomationVersionState = "draft"
	AutomationVersionPublished AutomationVersionState = "published"

	AutomationNodeTrigger   AutomationNodeType = "trigger"
	AutomationNodeAgentTask AutomationNodeType = "agent_task"
	AutomationNodeHumanGate AutomationNodeType = "human_gate"
	AutomationNodeAction    AutomationNodeType = "action"
	AutomationNodeCondition AutomationNodeType = "condition"
	AutomationNodeOutcome   AutomationNodeType = "outcome"
)

type Automation struct {
	ID                 string                   `json:"id"`
	ProjectID          string                   `json:"project_id"`
	StableKey          string                   `json:"stable_key"`
	Name               string                   `json:"name"`
	Description        string                   `json:"description"`
	AutomationType     string                   `json:"automation_type"`
	LifecycleState     AutomationLifecycleState `json:"lifecycle_state"`
	HealthState        AutomationHealthState    `json:"health_state"`
	HealthReason       string                   `json:"health_reason"`
	HealthEvaluatedAt  *time.Time               `json:"health_evaluated_at,omitempty"`
	PublishedVersionID *string                  `json:"published_version_id,omitempty"`
	TemplateRevision   *int                     `json:"template_revision,omitempty"`
	CreatedVia         string                   `json:"created_via"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	ArchivedAt         *time.Time               `json:"archived_at,omitempty"`
}

type AutomationVersion struct {
	ID            string                 `json:"id"`
	ProjectID     string                 `json:"project_id"`
	AutomationID  string                 `json:"automation_id"`
	Version       int                    `json:"version"`
	State         AutomationVersionState `json:"state"`
	Source        string                 `json:"source"`
	AdapterKey    string                 `json:"adapter_key"`
	SchemaVersion int                    `json:"schema_version"`
	CreatedAt     time.Time              `json:"created_at"`
	PublishedAt   *time.Time             `json:"published_at,omitempty"`
}

type AutomationNode struct {
	ID           string             `json:"id"`
	ProjectID    string             `json:"project_id"`
	AutomationID string             `json:"automation_id"`
	VersionID    string             `json:"version_id"`
	NodeKey      string             `json:"node_key"`
	Name         string             `json:"name"`
	NodeType     AutomationNodeType `json:"node_type"`
	Role         string             `json:"role"`
	ConfigJSON   string             `json:"config_json"`
	PositionX    float64            `json:"position_x"`
	PositionY    float64            `json:"position_y"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

type AutomationEdge struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	AutomationID  string    `json:"automation_id"`
	VersionID     string    `json:"version_id"`
	SourceNodeID  string    `json:"source_node_id"`
	TargetNodeID  string    `json:"target_node_id"`
	EdgeKey       string    `json:"edge_key"`
	Label         string    `json:"label"`
	ConditionJSON string    `json:"condition_json"`
	DisplayOrder  int       `json:"display_order"`
	CreatedAt     time.Time `json:"created_at"`
}

type AutomationDefinitionResource struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	AutomationID string    `json:"automation_id"`
	VersionID    string    `json:"version_id"`
	NodeID       string    `json:"node_id"`
	NodeKey      string    `json:"node_key,omitempty"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Relation     string    `json:"relation"`
	CreatedAt    time.Time `json:"created_at"`
}

type AutomationTriggerOwner struct {
	ScheduleID     string    `json:"schedule_id"`
	ProjectID      string    `json:"project_id"`
	AutomationID   string    `json:"automation_id"`
	VersionID      string    `json:"version_id"`
	NodeID         string    `json:"node_id"`
	OwnershipState string    `json:"ownership_state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AutomationDefinition struct {
	Automation Automation                     `json:"automation"`
	Version    AutomationVersion              `json:"version"`
	Nodes      []AutomationNode               `json:"nodes"`
	Edges      []AutomationEdge               `json:"edges"`
	Resources  []AutomationDefinitionResource `json:"resources"`
}

type AutomationNodeSpec struct {
	Key        string
	Name       string
	Type       AutomationNodeType
	Role       string
	ConfigJSON string
	PositionX  float64
	PositionY  float64
}

type AutomationEdgeSpec struct {
	Key           string
	SourceNodeKey string
	TargetNodeKey string
	Label         string
	ConditionJSON string
	DisplayOrder  int
}

type AutomationResourceBinding struct {
	NodeKey      string `json:"node_key"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Relation     string `json:"relation"`
}

type AutomationRegisteredPublication struct {
	ProjectID      string
	StableKey      string
	Name           string
	Description    string
	AutomationType string
	AdapterKey     string
	CreatedVia     string
	Nodes          []AutomationNodeSpec
	Edges          []AutomationEdgeSpec
	Resources      []AutomationResourceBinding
}

type AutomationResourceSummary struct {
	NodeID       string `json:"node_id"`
	NodeKey      string `json:"node_key"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Relation     string `json:"relation"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	URL          string `json:"url"`
}

type AutomationCard struct {
	Automation              Automation                  `json:"automation"`
	Version                 AutomationVersion           `json:"version"`
	Resources               []AutomationResourceSummary `json:"resources"`
	Counts                  AutomationNodeCounts        `json:"counts"`
	NextRun                 *time.Time                  `json:"next_run,omitempty"`
	LastRun                 *time.Time                  `json:"last_run,omitempty"`
	TemplateUpdateAvailable bool                        `json:"template_update_available,omitempty"`
}

type AutomationInvocationStatus string
type AutomationWorkItemStatus string
type AutomationPositionState string
type AutomationActivityStatus string
type AutomationTransitionState string

const (
	AutomationInvocationClaimed    AutomationInvocationStatus = "claimed"
	AutomationInvocationDispatched AutomationInvocationStatus = "dispatched"
	AutomationInvocationRunning    AutomationInvocationStatus = "running"
	AutomationInvocationCompleted  AutomationInvocationStatus = "completed"
	AutomationInvocationFailed     AutomationInvocationStatus = "failed"
	AutomationInvocationCancelled  AutomationInvocationStatus = "cancelled"
	AutomationInvocationSkipped    AutomationInvocationStatus = "skipped"

	AutomationWorkItemActive    AutomationWorkItemStatus = "active"
	AutomationWorkItemWaiting   AutomationWorkItemStatus = "waiting"
	AutomationWorkItemBlocked   AutomationWorkItemStatus = "blocked"
	AutomationWorkItemFailed    AutomationWorkItemStatus = "failed"
	AutomationWorkItemCompleted AutomationWorkItemStatus = "completed"
	AutomationWorkItemCancelled AutomationWorkItemStatus = "cancelled"

	AutomationPositionActive  AutomationPositionState = "active"
	AutomationPositionWaiting AutomationPositionState = "waiting"
	AutomationPositionBlocked AutomationPositionState = "blocked"
	AutomationPositionFailed  AutomationPositionState = "failed"

	AutomationActivityPending   AutomationActivityStatus = "pending"
	AutomationActivityRunning   AutomationActivityStatus = "running"
	AutomationActivityWaiting   AutomationActivityStatus = "waiting"
	AutomationActivityCompleted AutomationActivityStatus = "completed"
	AutomationActivityFailed    AutomationActivityStatus = "failed"
	AutomationActivityCancelled AutomationActivityStatus = "cancelled"

	AutomationTransitionEntered   AutomationTransitionState = "entered"
	AutomationTransitionWaiting   AutomationTransitionState = "waiting"
	AutomationTransitionCompleted AutomationTransitionState = "completed"
	AutomationTransitionFailed    AutomationTransitionState = "failed"
	AutomationTransitionBlocked   AutomationTransitionState = "blocked"
	AutomationTransitionCancelled AutomationTransitionState = "cancelled"
)

type AutomationBinding struct {
	AutomationID   string `json:"automation_id"`
	AutomationName string `json:"automation_name,omitempty"`
	VersionID      string `json:"version_id"`
	InvocationID   string `json:"invocation_id,omitempty"`
	NodeID         string `json:"node_id"`
	WorkItemID     string `json:"work_item_id,omitempty"`
}

type AutomationContext struct {
	ProjectID  string              `json:"project_id"`
	Bindings   []AutomationBinding `json:"bindings"`
	OriginTask bool                `json:"origin_task,omitempty"`
}

type AutomationDispatchEnvelope struct {
	DispatchID string            `json:"dispatch_id"`
	Task       Task              `json:"task"`
	Context    AutomationContext `json:"context"`
}

type AutomationInvocation struct {
	ID                  string                     `json:"id"`
	ProjectID           string                     `json:"project_id"`
	AutomationID        string                     `json:"automation_id"`
	VersionID           string                     `json:"version_id"`
	TriggerNodeID       string                     `json:"trigger_node_id"`
	TriggerResourceType string                     `json:"trigger_resource_type"`
	TriggerResourceID   string                     `json:"trigger_resource_id"`
	OccurrenceKey       string                     `json:"occurrence_key"`
	ScheduledFor        *time.Time                 `json:"scheduled_for,omitempty"`
	Status              AutomationInvocationStatus `json:"status"`
	SkippedReason       string                     `json:"skipped_reason,omitempty"`
	StartedAt           *time.Time                 `json:"started_at,omitempty"`
	CompletedAt         *time.Time                 `json:"completed_at,omitempty"`
	CreatedAt           time.Time                  `json:"created_at"`
	UpdatedAt           time.Time                  `json:"updated_at"`
	ErrorMessage        string                     `json:"error_message,omitempty"`
}

type AutomationDispatch struct {
	ID             string     `json:"id"`
	InvocationID   string     `json:"invocation_id"`
	TaskID         string     `json:"task_id"`
	ExecutionID    string     `json:"execution_id,omitempty"`
	Status         string     `json:"status"`
	Attempts       int        `json:"attempts"`
	ClaimedBy      string     `json:"claimed_by,omitempty"`
	ClaimExpiresAt *time.Time `json:"claim_expires_at,omitempty"`
	NextAttemptAt  time.Time  `json:"next_attempt_at"`
	LastError      string     `json:"last_error,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type AutomationWorkItem struct {
	ID                 string                   `json:"id"`
	ProjectID          string                   `json:"project_id"`
	AutomationID       string                   `json:"automation_id"`
	OriginVersionID    string                   `json:"origin_version_id"`
	OriginInvocationID string                   `json:"origin_invocation_id,omitempty"`
	ParentWorkItemID   string                   `json:"parent_work_item_id,omitempty"`
	WorkItemKey        string                   `json:"work_item_key"`
	Kind               string                   `json:"kind"`
	Title              string                   `json:"title"`
	Status             AutomationWorkItemStatus `json:"status"`
	CreatedAt          time.Time                `json:"created_at"`
	UpdatedAt          time.Time                `json:"updated_at"`
	CompletedAt        *time.Time               `json:"completed_at,omitempty"`
}

type AutomationWorkItemPosition struct {
	WorkItemID   string                  `json:"work_item_id"`
	ProjectID    string                  `json:"project_id"`
	AutomationID string                  `json:"automation_id"`
	VersionID    string                  `json:"version_id"`
	NodeID       string                  `json:"node_id"`
	State        AutomationPositionState `json:"state"`
	EnteredAt    time.Time               `json:"entered_at"`
	UpdatedAt    time.Time               `json:"updated_at"`
}

type AutomationActivity struct {
	ID           string                   `json:"id"`
	ProjectID    string                   `json:"project_id"`
	AutomationID string                   `json:"automation_id"`
	VersionID    string                   `json:"version_id"`
	NodeID       string                   `json:"node_id"`
	InvocationID string                   `json:"invocation_id,omitempty"`
	WorkItemID   string                   `json:"work_item_id,omitempty"`
	ActivityKey  string                   `json:"activity_key"`
	ActivityType string                   `json:"activity_type"`
	Status       AutomationActivityStatus `json:"status"`
	StartedAt    time.Time                `json:"started_at"`
	CompletedAt  *time.Time               `json:"completed_at,omitempty"`
	ErrorMessage string                   `json:"error_message,omitempty"`
}

type AutomationActivityResource struct {
	ID           string    `json:"id"`
	ActivityID   string    `json:"activity_id"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Relation     string    `json:"relation"`
	CreatedAt    time.Time `json:"created_at"`
}

type AutomationTransition struct {
	ID           string                    `json:"id"`
	ProjectID    string                    `json:"project_id"`
	AutomationID string                    `json:"automation_id"`
	VersionID    string                    `json:"version_id"`
	WorkItemID   string                    `json:"work_item_id"`
	InvocationID string                    `json:"invocation_id,omitempty"`
	ActivityID   string                    `json:"activity_id,omitempty"`
	FromNodeID   string                    `json:"from_node_id,omitempty"`
	ToNodeID     string                    `json:"to_node_id"`
	EdgeID       string                    `json:"edge_id,omitempty"`
	EventKey     string                    `json:"event_key"`
	State        AutomationTransitionState `json:"state"`
	MetadataJSON string                    `json:"metadata_json"`
	OccurredAt   time.Time                 `json:"occurred_at"`
}

type AutomationNodeResource struct {
	NodeID       string    `json:"node_id"`
	ActivityID   string    `json:"activity_id"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Relation     string    `json:"relation"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	URL          string    `json:"url"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AutomationNodeResourcePage struct {
	Items      []AutomationNodeResource `json:"items"`
	NextCursor string                   `json:"next_cursor,omitempty"`
}

type AutomationNodeCounts struct {
	Running           int `json:"running"`
	Waiting           int `json:"waiting"`
	Blocked           int `json:"blocked"`
	Failed            int `json:"failed"`
	CompletedRecently int `json:"completed_recently"`
}

type AutomationLiveNode struct {
	AutomationNode
	Counts       AutomationNodeCounts `json:"counts"`
	DisplayState string               `json:"display_state"`
}

type AutomationLiveEdge struct {
	AutomationEdge
	TransitionCount       int  `json:"transition_count"`
	RecentTransitionCount int  `json:"recent_transition_count"`
	Highlighted           bool `json:"highlighted"`
}

type AutomationExternalState struct {
	TrackedResources int        `json:"tracked_resources"`
	LastUpdatedAt    *time.Time `json:"last_updated_at,omitempty"`
	Stale            bool       `json:"stale"`
}

type AutomationLiveGraph struct {
	Automation              Automation                  `json:"automation"`
	Version                 AutomationVersion           `json:"version"`
	Nodes                   []AutomationLiveNode        `json:"nodes"`
	Edges                   []AutomationLiveEdge        `json:"edges"`
	Resources               []AutomationResourceSummary `json:"resources"`
	ActiveInvocations       int                         `json:"active_invocations"`
	ActiveWorkItems         int                         `json:"active_work_items"`
	RecentCutoff            time.Time                   `json:"recent_cutoff"`
	ExternalState           AutomationExternalState     `json:"external_state"`
	TemplateUpdateAvailable bool                        `json:"template_update_available,omitempty"`
	YAML                    string                      `json:"-"`
}

type AutomationInvocationPage struct {
	Items      []AutomationInvocation `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type AutomationWorkItemPage struct {
	Items      []AutomationWorkItem `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type AutomationTransitionPage struct {
	Items      []AutomationTransition `json:"items"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type AutomationActivityPage struct {
	Items      []AutomationActivity `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type AutomationReplayPosition struct {
	NodeID string                  `json:"node_id"`
	State  AutomationPositionState `json:"state"`
}

type AutomationReplayFrame struct {
	State      AutomationTransitionState  `json:"state"`
	OccurredAt time.Time                  `json:"occurred_at"`
	Positions  []AutomationReplayPosition `json:"positions"`
}

type AutomationInvocationHistory struct {
	Invocation     AutomationInvocation     `json:"invocation"`
	Definition     AutomationDefinition     `json:"definition"`
	Activities     AutomationActivityPage   `json:"activities"`
	Transitions    AutomationTransitionPage `json:"transitions"`
	TouchedNodeIDs []string                 `json:"touched_node_ids"`
}

type AutomationWorkItemHistory struct {
	WorkItem    AutomationWorkItem       `json:"work_item"`
	Definition  AutomationDefinition     `json:"definition"`
	Activities  AutomationActivityPage   `json:"activities"`
	Transitions AutomationTransitionPage `json:"transitions"`
	Replay      []AutomationReplayFrame  `json:"replay"`
}

type AutomationFunnelPoint struct {
	NodeID            string  `json:"node_id"`
	NodeName          string  `json:"node_name"`
	EnteredCount      int     `json:"entered_count"`
	ConversionPercent float64 `json:"conversion_percent"`
}

type AutomationDurationPoint struct {
	NodeID         string  `json:"node_id"`
	NodeName       string  `json:"node_name"`
	SampleCount    int     `json:"sample_count"`
	AverageSeconds float64 `json:"average_seconds"`
}

type AutomationFailureSummary struct {
	NodeID      string    `json:"node_id"`
	NodeName    string    `json:"node_name"`
	Count       int       `json:"count"`
	LastFailure time.Time `json:"last_failure"`
}

type AutomationBottleneckSummary struct {
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Waiting  int    `json:"waiting"`
	Blocked  int    `json:"blocked"`
}

type AutomationMetrics struct {
	Funnel      []AutomationFunnelPoint       `json:"funnel"`
	Durations   []AutomationDurationPoint     `json:"durations"`
	Failures    []AutomationFailureSummary    `json:"failures"`
	Bottlenecks []AutomationBottleneckSummary `json:"bottlenecks"`
}

type AutomationHealth struct {
	State       AutomationHealthState `json:"state"`
	Reason      string                `json:"reason"`
	EvaluatedAt time.Time             `json:"evaluated_at"`
}

type AutomationHistoryDashboard struct {
	Automation  Automation               `json:"automation"`
	Invocations AutomationInvocationPage `json:"invocations"`
	WorkItems   AutomationWorkItemPage   `json:"work_items"`
	Metrics     AutomationMetrics        `json:"metrics"`
	Health      AutomationHealth         `json:"health"`
}
