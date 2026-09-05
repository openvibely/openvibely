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
	FromPort  string         `json:"from_port,omitempty"`
	ToPort    string         `json:"to_port,omitempty"`
	Label     string         `json:"label,omitempty"`
	Condition map[string]any `json:"condition,omitempty"`
}

type AutomationValidationIssue struct {
	NodeKey string `json:"node_key,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type AutomationGraphMetadata struct {
	ProjectID        string                      `json:"project_id"`
	AutomationID     string                      `json:"automation_id"`
	GraphID          string                      `json:"graph_id"`
	CandidateJSON    string                      `json:"candidate_json"`
	AssumptionsJSON  string                      `json:"-"`
	WarningsJSON     string                      `json:"-"`
	ValidationJSON   string                      `json:"-"`
	Assumptions      []string                    `json:"assumptions"`
	Warnings         []string                    `json:"warnings"`
	ValidationErrors []AutomationValidationIssue `json:"validation_errors"`
	UpdatedAt        time.Time                   `json:"updated_at"`
}

type AutomationDraftResult struct {
	Definition                 *AutomationDefinition       `json:"definition,omitempty"`
	Candidate                  AutomationDraftCandidate    `json:"candidate"`
	Assumptions                []string                    `json:"assumptions"`
	Warnings                   []string                    `json:"warnings"`
	ValidationErrors           []AutomationValidationIssue `json:"validation_errors"`
	Summary                    string                      `json:"summary"`
	ResolvedAgentDefinitionIDs map[string]string           `json:"-"`
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
	Models             []AutomationCapabilityRef                  `json:"models"`
	Integrations       map[string]AutomationIntegrationCapability `json:"integrations"`
	ReusableResources  []AutomationCapabilityRef                  `json:"reusable_resources"`
	SafetyBoundaries   map[string]bool                            `json:"safety_boundaries"`
	AgentDefinitionIDs map[string]string                          `json:"-"`
}

type AutomationSaveEffect struct {
	Operation    string `json:"operation"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
}

type AutomationSavePlan struct {
	Effects            []AutomationSaveEffect      `json:"effects"`
	Validation         []AutomationValidationIssue `json:"validation_errors"`
	WillNot            []string                    `json:"will_not"`
	AgentDefinitionIDs map[string]string           `json:"-"`
}

type AutomationChatConfirmationReceipt struct {
	TokenID               string     `json:"token_id"`
	ProjectID             string     `json:"project_id"`
	PrincipalID           string     `json:"principal_id"`
	ThreadID              string     `json:"thread_id"`
	PlanMessageID         string     `json:"plan_message_id"`
	AutomationName        string     `json:"automation_name"`
	Source                string     `json:"source"`
	CandidateJSON         string     `json:"candidate_json"`
	ExpiresAt             time.Time  `json:"expires_at"`
	ConfirmingUserInputID string     `json:"confirming_user_input_id,omitempty"`
	ConfirmationMethod    string     `json:"confirmation_method,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	ConsumedAt            *time.Time `json:"consumed_at,omitempty"`
}

type AutomationBuilderPage struct {
	Result                  AutomationDraftResult        `json:"result"`
	AutomationID            string                       `json:"automation_id,omitempty"`
	Source                  string                       `json:"source,omitempty"`
	NodePalette             []AutomationDraftNode        `json:"node_palette,omitempty"`
	EdgePalette             []AutomationDraftEdge        `json:"edge_palette,omitempty"`
	Capabilities            AutomationCapabilitySnapshot `json:"capabilities"`
	TemplateUpdateAvailable bool                         `json:"template_update_available,omitempty"`
	LifecycleState          AutomationLifecycleState     `json:"lifecycle_state,omitempty"`
	YAML                    string                       `json:"-"`
	YAMLProvided            bool                         `json:"-"`
	InitialView             string                       `json:"-"`
	Error                   string                       `json:"error,omitempty"`
}

func (m AutomationGraphMetadata) Candidate() (AutomationDraftCandidate, error) {
	var candidate AutomationDraftCandidate
	err := json.Unmarshal([]byte(m.CandidateJSON), &candidate)
	return candidate, err
}
