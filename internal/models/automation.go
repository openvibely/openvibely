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

	AutomationVersionDraft      AutomationVersionState = "draft"
	AutomationVersionPublished  AutomationVersionState = "published"
	AutomationVersionSuperseded AutomationVersionState = "superseded"

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
	Automation Automation                  `json:"automation"`
	Version    AutomationVersion           `json:"version"`
	Resources  []AutomationResourceSummary `json:"resources"`
	NextRun    *time.Time                  `json:"next_run,omitempty"`
	LastRun    *time.Time                  `json:"last_run,omitempty"`
}
