package models

import (
	"encoding/json"
	"time"
)

type AutomationDraftCandidate struct {
	SchemaVersion  int                   `json:"schema_version"`
	Name           string                `json:"name"`
	Description    string                `json:"description"`
	AutomationType string                `json:"automation_type"`
	AdapterKey     string                `json:"adapter_key"`
	Nodes          []AutomationDraftNode `json:"nodes"`
	Edges          []AutomationDraftEdge `json:"edges"`
	Assumptions    []string              `json:"assumptions,omitempty"`
	Warnings       []string              `json:"warnings,omitempty"`
}

type AutomationDraftNode struct {
	Key      string                `json:"key"`
	Name     string                `json:"name"`
	Type     AutomationNodeType    `json:"type"`
	Role     string                `json:"role"`
	Config   map[string]any        `json:"config"`
	Position *AutomationDraftPoint `json:"position,omitempty"`
}

type AutomationDraftPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type AutomationDraftEdge struct {
	Key       string         `json:"key"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Label     string         `json:"label,omitempty"`
	Condition map[string]any `json:"condition,omitempty"`
}

type AutomationValidationIssue struct {
	NodeKey string `json:"node_key,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AutomationDraftMetadata struct {
	ProjectID        string                      `json:"project_id"`
	AutomationID     string                      `json:"automation_id"`
	VersionID        string                      `json:"version_id"`
	CandidateJSON    string                      `json:"candidate_json"`
	Assumptions      []string                    `json:"assumptions"`
	Warnings         []string                    `json:"warnings"`
	ValidationErrors []AutomationValidationIssue `json:"validation_errors"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}

type AutomationDraftResult struct {
	Definition       *AutomationDefinition       `json:"definition,omitempty"`
	Candidate        AutomationDraftCandidate    `json:"candidate"`
	Assumptions      []string                    `json:"assumptions"`
	Warnings         []string                    `json:"warnings"`
	ValidationErrors []AutomationValidationIssue `json:"validation_errors"`
	Summary          string                      `json:"summary"`
	URL              string                      `json:"url,omitempty"`
}

type AutomationCapabilityRef struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities,omitempty"`
}

type AutomationIntegrationCapability struct {
	Configured    bool     `json:"configured"`
	ApprovalModes []string `json:"approval_modes,omitempty"`
}

type AutomationCapabilitySnapshot struct {
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	SupportedNodeTypes []AutomationNodeType                       `json:"supported_node_types"`
	SupportedRoles     []string                                   `json:"supported_roles"`
	Agents             []AutomationCapabilityRef                  `json:"agents"`
	Skills             []AutomationCapabilityRef                  `json:"skills"`
	Integrations       map[string]AutomationIntegrationCapability `json:"integrations"`
	SourceFiles        []string                                   `json:"source_files"`
	ReusableResources  []AutomationCapabilityRef                  `json:"reusable_resources"`
	SafetyBoundaries   map[string]bool                            `json:"safety_boundaries"`
}

type AutomationPublicationEffect struct {
	StepKey      string `json:"step_key"`
	Operation    string `json:"operation"`
	TargetKey    string `json:"target_key"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
	ResourceID   string `json:"resource_id,omitempty"`
}

type AutomationPublicationPlan struct {
	ProjectID    string                        `json:"project_id"`
	AutomationID string                        `json:"automation_id"`
	VersionID    string                        `json:"version_id"`
	PlanRevision string                        `json:"plan_revision"`
	Effects      []AutomationPublicationEffect `json:"effects"`
	Validation   []AutomationValidationIssue   `json:"validation_errors"`
	WillNot      []string                      `json:"will_not"`
}

type AutomationPublicationAttempt struct {
	ID             string     `json:"id"`
	ProjectID      string     `json:"project_id"`
	AutomationID   string     `json:"automation_id"`
	VersionID      string     `json:"version_id"`
	PlanRevision   string     `json:"plan_revision"`
	Status         string     `json:"status"`
	ErrorMessage   string     `json:"error_message"`
	ClaimOwner     string     `json:"-"`
	ClaimExpiresAt *time.Time `json:"-"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
}

type AutomationPublicationStep struct {
	ID           string    `json:"id"`
	AttemptID    string    `json:"attempt_id"`
	StepKey      string    `json:"step_key"`
	Operation    string    `json:"operation"`
	TargetKey    string    `json:"target_key"`
	DisplayOrder int       `json:"display_order"`
	Status       string    `json:"status"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	ErrorMessage string    `json:"error_message"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AutomationChatConfirmationReceipt struct {
	TokenID               string     `json:"token_id"`
	ProjectID             string     `json:"project_id"`
	AutomationID          string     `json:"automation_id"`
	VersionID             string     `json:"version_id"`
	PlanRevision          string     `json:"plan_revision"`
	PrincipalID           string     `json:"principal_id"`
	ThreadID              string     `json:"thread_id"`
	PlanMessageID         string     `json:"plan_message_id"`
	ExpiresAt             time.Time  `json:"expires_at"`
	ConsumedAttemptID     string     `json:"consumed_attempt_id,omitempty"`
	ConfirmingUserInputID string     `json:"confirming_user_input_id,omitempty"`
	ConfirmationMethod    string     `json:"confirmation_method,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	ConsumedAt            *time.Time `json:"consumed_at,omitempty"`
}

type AutomationBuilderPage struct {
	Result            AutomationDraftResult       `json:"result"`
	Plan              *AutomationPublicationPlan  `json:"plan,omitempty"`
	PublicationSteps  []AutomationPublicationStep `json:"publication_steps,omitempty"`
	NodePalette       []AutomationDraftNode       `json:"node_palette,omitempty"`
	EdgePalette       []AutomationDraftEdge       `json:"edge_palette,omitempty"`
	ConfirmationToken string                      `json:"-"`
	Error             string                      `json:"error,omitempty"`
}

func (m AutomationDraftMetadata) Candidate() (AutomationDraftCandidate, error) {
	var candidate AutomationDraftCandidate
	err := json.Unmarshal([]byte(m.CandidateJSON), &candidate)
	return candidate, err
}
