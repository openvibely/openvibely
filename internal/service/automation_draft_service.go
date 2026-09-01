package service

import (
	"bytes"
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const (
	automationDraftSchemaVersion = 1
	maxAutomationDraftBytes      = 64 * 1024
	maxAutomationDraftNodes      = 50
	maxAutomationDraftEdges      = 100
)

var (
	//go:embed automation_templates/native_sdlc.yaml
	nativeSDLCTemplateYAML string
	//go:embed automation_templates/github_sdlc.yaml
	githubSDLCTemplateYAML string
)

func maintainedAutomationTemplateYAML(adapterKey string) (string, bool) {
	switch strings.TrimSpace(adapterKey) {
	case AutomationAdapterNativeSDLC:
		return nativeSDLCTemplateYAML, true
	case AutomationAdapterGitHubSDLC:
		return githubSDLCTemplateYAML, true
	default:
		return "", false
	}
}

type AutomationDraftService struct {
	repo         *repository.AutomationRepo
	registry     *AutomationAdapterRegistry
	capabilities *AutomationCapabilitySnapshotBuilder
}

type AutomationDraftCreateRequest struct {
	ProjectID    string
	AutomationID string
	VersionID    string
	Source       string
	CreatedVia   string
	StableKey    string
	Candidate    models.AutomationDraftCandidate
}

func NewAutomationDraftService(repo *repository.AutomationRepo, registry *AutomationAdapterRegistry) *AutomationDraftService {
	if registry == nil {
		registry = NewAutomationAdapterRegistry()
	}
	return &AutomationDraftService{repo: repo, registry: registry}
}

func (s *AutomationDraftService) SetCapabilitySnapshotBuilder(capabilities *AutomationCapabilitySnapshotBuilder) {
	s.capabilities = capabilities
}

func DecodeAutomationDraftCandidate(raw []byte) (models.AutomationDraftCandidate, error) {
	if len(raw) == 0 || len(raw) > maxAutomationDraftBytes {
		return models.AutomationDraftCandidate{}, errors.New("automation graph candidate must be between 1 byte and 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var candidate models.AutomationDraftCandidate
	if err := decoder.Decode(&candidate); err != nil {
		return candidate, fmt.Errorf("invalid automation graph candidate: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return candidate, err
	}
	return candidate, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == io.EOF {
		return nil
	} else if err != nil {
		return fmt.Errorf("invalid automation graph candidate: %w", err)
	}
	return errors.New("automation graph candidate contains trailing JSON")
}

func (s *AutomationDraftService) CreationTemplateCandidate(adapterKey string) (models.AutomationDraftCandidate, error) {
	adapterKey = strings.TrimSpace(adapterKey)
	if adapterKey != AutomationAdapterNativeSDLC && adapterKey != AutomationAdapterGitHubSDLC {
		return models.AutomationDraftCandidate{}, fmt.Errorf("unsupported automation template %q", adapterKey)
	}
	return s.TemplateCandidate(adapterKey)
}

func ApplyAutomationTemplateDefaultModel(candidate *models.AutomationDraftCandidate) {
	if candidate == nil {
		return
	}
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		if node.Type != models.AutomationNodeTrigger && node.Type != models.AutomationNodeAgentTask {
			continue
		}
		if node.Config == nil {
			node.Config = map[string]any{}
		}
		if existing, ok := node.Config["model_config_id"].(string); ok && strings.TrimSpace(existing) != "" {
			continue
		}
		if _, hasPrompt := node.Config["prompt"]; hasPrompt || node.Role == "implementation" {
			node.Config["model_config_id"] = automationDefaultModelConfigID
		}
	}
}

func (s *AutomationDraftService) TemplateCandidate(adapterKey string) (models.AutomationDraftCandidate, error) {
	adapterKey = strings.TrimSpace(adapterKey)
	if document, maintained := maintainedAutomationTemplateYAML(adapterKey); maintained {
		candidate, err := DecodeAutomationDraftYAML([]byte(document))
		if err != nil {
			return models.AutomationDraftCandidate{}, fmt.Errorf("decode maintained automation template %q: %w", adapterKey, err)
		}
		if candidate.AdapterKey != adapterKey {
			return models.AutomationDraftCandidate{}, fmt.Errorf("maintained automation template %q has adapter %q", adapterKey, candidate.AdapterKey)
		}
		return candidate, nil
	}
	adapter, ok := s.registry.Get(adapterKey)
	if !ok {
		return models.AutomationDraftCandidate{}, fmt.Errorf("unsupported automation template %q", adapterKey)
	}
	candidate := models.AutomationDraftCandidate{
		SchemaVersion:  automationDraftSchemaVersion,
		Name:           adapter.DefaultName,
		Description:    adapter.Description,
		AutomationType: adapter.AutomationType,
		AdapterKey:     adapter.Key,
	}
	defaultConfigs, err := defaultAutomationNodeConfigs(adapter)
	if err != nil {
		return models.AutomationDraftCandidate{}, err
	}
	for _, node := range adapter.Nodes {
		config := defaultConfigs[node.Key]
		candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: node.Key, Name: node.Name, Type: models.AutomationNodeType(node.Type), Role: node.Role,
			Config: config, Position: &models.AutomationDraftPoint{X: node.X, Y: node.Y},
		})
	}
	for _, edge := range adapter.Edges {
		condition := map[string]any{}
		if strings.TrimSpace(edge.Condition) != "" {
			_ = json.Unmarshal([]byte(edge.Condition), &condition)
		}
		candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{
			Key: edge.Key, From: edge.From, To: edge.To, FromPort: "right", ToPort: "left",
			Label: edge.Label, Condition: condition,
		})
	}
	return candidate, nil
}

const automationDefaultModelConfigID = "default"

func automationExplicitModelConfigID(value any) string {
	modelConfigID, _ := value.(string)
	modelConfigID = strings.TrimSpace(modelConfigID)
	if strings.EqualFold(modelConfigID, automationDefaultModelConfigID) {
		return ""
	}
	return modelConfigID
}

// DefaultAutomationDraftNodeConfig returns the canonical starting Config for a draft node.
func DefaultAutomationDraftNodeConfig(adapterKey, nodeKey string, nodeType models.AutomationNodeType, role string, allowedResources map[string]bool) (map[string]any, error) {
	adapterKey = strings.TrimSpace(adapterKey)
	nodeKey = strings.TrimSpace(nodeKey)
	role = strings.TrimSpace(role)
	resources := make(map[string]bool, len(allowedResources))
	for key, allowed := range allowedResources {
		resources[key] = allowed
	}
	config := map[string]any{}
	usesTaskConfiguration := resources["task"] || adapterKey == AutomationAdapterGitHubSDLC && role == "implementation"
	usesModelConfiguration := usesTaskConfiguration || role == "implementation" && (adapterKey == AutomationAdapterNativeSDLC || adapterKey == AutomationAdapterCustom && nodeType == models.AutomationNodeAgentTask)
	if usesTaskConfiguration {
		prompt, err := defaultAutomationNodePrompt(adapterKey, role)
		if err != nil {
			return nil, err
		}
		config["prompt"] = prompt
		config["goal"] = ""
		config["category"] = string(models.CategoryBacklog)
		config["priority"] = 2
		config["model_config_id"] = automationDefaultModelConfigID
		if resources["schedule"] {
			config["category"] = string(models.CategoryScheduled)
		}
		if adapterKey == AutomationAdapterGitHubSDLC && role == "implementation" {
			config["category"] = string(models.CategoryActive)
		}
	}
	if resources["schedule"] {
		config["run_at"] = "09:00"
		config["repeat_type"] = string(models.RepeatDaily)
		config["repeat_interval"] = 1
		config["enabled"] = true
		config["clear_context_on_start"] = true
		if role == "loop_auditor" {
			config["repeat_type"] = string(models.RepeatWeekly)
		} else if role == "native_inbox" || role == "github_inbox" || strings.Contains(nodeKey, "inbox") {
			config["run_at"] = "10:00"
		}
	}
	if usesModelConfiguration && !usesTaskConfiguration {
		config["goal"] = ""
		config["model_config_id"] = automationDefaultModelConfigID
	}
	switch role {
	case "create_notification":
		config["notification_type"] = "approval_request"
		config["instructions"] = "Summarize the proposal that needs a human decision."
	case "create_github_issue":
		config["instructions"] = "Open one focused, reviewable GitHub issue."
		config["labels"] = []string{}
	case "open_pull_request":
		config["instructions"] = "Open a reviewable pull request linked to the source issue."
		config["base"] = ""
		config["draft"] = false
	case "native_approval":
		config["approval_method"] = "native_alert"
	case "github_assignment":
		config["approval_method"] = "github_assignment"
	case "pull_request_review":
		config["approval_method"] = "pull_request_review"
	}
	return config, nil
}

func defaultAutomationNodeConfigs(adapter AutomationAdapter) (map[string]map[string]any, error) {
	configs := make(map[string]map[string]any, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		config, err := DefaultAutomationDraftNodeConfig(adapter.Key, node.Key, models.AutomationNodeType(node.Type), node.Role, node.AllowedResources)
		if err != nil {
			return nil, err
		}
		if node.AllowedResources["schedule"] {
			config["target_node_key"] = adapterScheduleTarget(adapter, node.Key)
		}
		configs[node.Key] = config
	}
	for _, node := range adapter.Nodes {
		if !node.AllowedResources["schedule"] {
			continue
		}
		if target := configs[adapterScheduleTarget(adapter, node.Key)]; target != nil {
			target["category"] = string(models.CategoryScheduled)
		}
	}
	return configs, nil
}

func automationMaintainedNodeUsesTaskConfiguration(adapter AutomationAdapter, node AutomationAdapterNode) bool {
	return node.AllowedResources["task"] || adapter.Key == AutomationAdapterGitHubSDLC && node.Role == "implementation"
}

func (s *AutomationDraftService) BlankCandidate(adapterKey string) (models.AutomationDraftCandidate, error) {
	if strings.TrimSpace(adapterKey) == "" {
		adapterKey = AutomationAdapterCustom
	}
	adapter, ok := s.registry.Get(strings.TrimSpace(adapterKey))
	if !ok {
		return models.AutomationDraftCandidate{}, fmt.Errorf("unsupported automation template %q", adapterKey)
	}
	return models.AutomationDraftCandidate{
		SchemaVersion:  automationDraftSchemaVersion,
		Name:           "Untitled Automation",
		Description:    "",
		AutomationType: adapter.AutomationType,
		AdapterKey:     adapter.Key,
		Nodes:          []models.AutomationDraftNode{},
		Edges:          []models.AutomationDraftEdge{},
	}, nil
}

func (s *AutomationDraftService) NormalizeCandidate(candidate models.AutomationDraftCandidate) (models.AutomationDraftCandidate, error) {
	adapter, ok := s.registry.Get(strings.TrimSpace(candidate.AdapterKey))
	if !ok {
		return candidate, fmt.Errorf("unsupported automation adapter %q", candidate.AdapterKey)
	}
	candidate.Name = strings.TrimSpace(candidate.Name)
	candidate.Description = strings.TrimSpace(candidate.Description)
	candidate.AdapterKey = adapter.Key
	adapterNodes := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		adapterNodes[node.Key] = node
	}
	missingPositions := make([]int, 0, len(candidate.Nodes))
	hasPosition := false
	var minY, maxX float64
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		node.Key = strings.TrimSpace(node.Key)
		node.Name = strings.TrimSpace(node.Name)
		node.Role = strings.TrimSpace(node.Role)
		if node.Config == nil {
			node.Config = map[string]any{}
		}
		if agentRef, exists := node.Config["agent_ref"]; exists {
			if text, valid := agentRef.(string); valid {
				node.Config["agent_ref"] = strings.TrimSpace(text)
			}
		}
		delete(node.Config, "skills")
		delete(node.Config, "source_files")
		if canonical, exists := adapterNodes[node.Key]; exists && node.Position == nil {
			node.Position = &models.AutomationDraftPoint{X: canonical.X, Y: canonical.Y}
		}
		if node.Position == nil {
			missingPositions = append(missingPositions, i)
			continue
		}
		if !hasPosition || node.Position.X > maxX {
			maxX = node.Position.X
		}
		if !hasPosition || node.Position.Y < minY {
			minY = node.Position.Y
		}
		hasPosition = true
	}
	if len(missingPositions) > 0 {
		sort.SliceStable(missingPositions, func(i, j int) bool {
			return candidate.Nodes[missingPositions[i]].Key < candidate.Nodes[missingPositions[j]].Key
		})
		baseX, baseY := 0.0, 0.0
		if hasPosition {
			baseX, baseY = maxX+220, minY
		}
		for order, index := range missingPositions {
			candidate.Nodes[index].Position = &models.AutomationDraftPoint{
				X: baseX + float64(order%3)*220,
				Y: baseY + float64(order/3)*140,
			}
		}
	}
	for i := range candidate.Edges {
		candidate.Edges[i].Key = strings.TrimSpace(candidate.Edges[i].Key)
		candidate.Edges[i].From = strings.TrimSpace(candidate.Edges[i].From)
		candidate.Edges[i].To = strings.TrimSpace(candidate.Edges[i].To)
		candidate.Edges[i].Label = strings.TrimSpace(candidate.Edges[i].Label)
		if candidate.Edges[i].Condition == nil {
			candidate.Edges[i].Condition = map[string]any{}
		}
	}
	candidate.Assumptions = normalizeDraftMessages(candidate.Assumptions)
	candidate.Warnings = normalizeDraftMessages(candidate.Warnings)
	return candidate, nil
}

func (s *AutomationDraftService) normalizeReopenedCandidate(candidate models.AutomationDraftCandidate) (models.AutomationDraftCandidate, error) {
	adapter, ok := s.registry.Get(strings.TrimSpace(candidate.AdapterKey))
	if !ok {
		return candidate, fmt.Errorf("unsupported automation adapter %q", candidate.AdapterKey)
	}
	candidate.AutomationType = adapter.AutomationType
	adapterNodes := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		adapterNodes[node.Key] = node
	}
	for i := range candidate.Nodes {
		if canonical, exists := adapterNodes[candidate.Nodes[i].Key]; exists && strings.TrimSpace(candidate.Nodes[i].Name) == "" {
			candidate.Nodes[i].Name = canonical.Name
		}
	}
	for i := range candidate.Edges {
		candidate.Edges[i].FromPort = "right"
		candidate.Edges[i].ToPort = "left"
	}
	if !adapter.DynamicTopology {
		defaults, err := defaultAutomationNodeConfigs(adapter)
		if err != nil {
			return candidate, err
		}
		for i := range candidate.Nodes {
			node := &candidate.Nodes[i]
			if node.Config == nil {
				node.Config = map[string]any{}
			}
			for key, value := range defaults[node.Key] {
				if _, exists := node.Config[key]; !exists {
					node.Config[key] = value
				}
			}
		}
	}
	if adapter.DynamicTopology {
		for i := range candidate.Nodes {
			node := &candidate.Nodes[i]
			// Older saved custom graphs exposed GitHub issue work as a separate
			// implementation role. Tasks are generic now; the surrounding inbox and
			// pull-request connections determine issue-linked materialization.
			if node.Type == models.AutomationNodeAgentTask && node.Role == "implementation" && !customAutomationNativeImplementation(candidate, node.Key) {
				node.Role = "task"
			}
			// A custom Schedule is its own task. Its downstream handoff is represented
			// only by graph edges, so remove topology-derived metadata from older saves.
			if node.Type == models.AutomationNodeTrigger {
				delete(node.Config, "target_node_key")
			}
		}
	}
	return s.NormalizeCandidate(candidate)
}

func normalizeDraftMessages(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] || len(out) >= 20 {
			continue
		}
		if len(value) > 500 {
			value = value[:500]
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func normalizeDraftReferences(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func draftStringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}

func (s *AutomationDraftService) ValidateCandidate(candidate models.AutomationDraftCandidate) []models.AutomationValidationIssue {
	var issues []models.AutomationValidationIssue
	encoded, encodeErr := json.Marshal(candidate)
	if encodeErr != nil {
		issues = append(issues, models.AutomationValidationIssue{Code: "invalid_json", Message: "Automation configuration contains a non-finite or unsupported JSON value."})
	} else if len(encoded) > maxAutomationDraftBytes {
		issues = append(issues, models.AutomationValidationIssue{Code: "graph_size", Message: "Automation graph exceeds the 64 KiB supported payload size."})
		automationobs.Event("automation.graph.limit_reached", automationobs.String("adapter_key", candidate.AdapterKey), automationobs.String("limit", "payload_bytes"))
	}
	adapter, ok := s.registry.Get(candidate.AdapterKey)
	if !ok {
		return []models.AutomationValidationIssue{{Code: "unsupported_adapter", Message: "The selected topology is not supported by a registered adapter."}}
	}
	if candidate.SchemaVersion != automationDraftSchemaVersion {
		issues = append(issues, models.AutomationValidationIssue{Code: "schema_version", Message: "Unsupported automation graph schema version."})
	}
	if candidate.AutomationType != adapter.AutomationType {
		issues = append(issues, models.AutomationValidationIssue{Code: "automation_type", Message: "Automation type must match the selected adapter."})
	}
	if candidate.Name == "" || len(candidate.Name) > 200 {
		issues = append(issues, models.AutomationValidationIssue{Code: "name", Message: "Automation name must be between 1 and 200 characters."})
	}
	if len(candidate.Description) > 2000 {
		issues = append(issues, models.AutomationValidationIssue{Code: "description", Message: "Automation description exceeds 2000 characters."})
	}
	if len(candidate.Nodes) > maxAutomationDraftNodes || len(candidate.Edges) > maxAutomationDraftEdges {
		issues = append(issues, models.AutomationValidationIssue{Code: "graph_size", Message: "Automation graph exceeds the supported size."})
		automationobs.Event("automation.graph.limit_reached", automationobs.String("adapter_key", candidate.AdapterKey), automationobs.String("limit", "nodes_or_edges"))
	}

	flexibleTemplate := adapter.Key == AutomationAdapterNativeSDLC || adapter.Key == AutomationAdapterGitHubSDLC
	canonicalNodes := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		canonicalNodes[node.Key] = node
	}
	seenNodes := map[string]bool{}
	draftNodes := map[string]models.AutomationDraftNode{}
	validNodeTypes := map[models.AutomationNodeType]bool{
		models.AutomationNodeTrigger: true, models.AutomationNodeAgentTask: true,
		models.AutomationNodeHumanGate: true, models.AutomationNodeAction: true,
		models.AutomationNodeCondition: true, models.AutomationNodeOutcome: true,
	}
	for _, node := range candidate.Nodes {
		if node.Key == "" || seenNodes[node.Key] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "invalid_node", Message: "Every graph node requires a unique key."})
			continue
		}
		seenNodes[node.Key] = true
		draftNodes[node.Key] = node
		if node.Name == "" || len(node.Name) > 200 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "node_name", Message: "Node name must be between 1 and 200 characters."})
		}
		if adapter.DynamicTopology {
			if !validNodeTypes[node.Type] {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "invalid_node", Message: "Node type is not supported by the graph editor."})
				continue
			}
			issues = append(issues, validateCustomAutomationNodeConfig(node)...)
			continue
		}
		canonical, exists := canonicalNodes[node.Key]
		if !exists {
			if !validNodeTypes[node.Type] {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "invalid_node", Message: "Node type is not supported by the graph editor."})
				continue
			}
			if !flexibleTemplate {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_topology", Message: "Custom graph nodes can be saved only when they use supported runtime capabilities."})
			}
			issues = append(issues, validateCustomAutomationNodeConfig(node)...)
			continue
		}
		if node.Type != models.AutomationNodeType(canonical.Type) || node.Role != canonical.Role {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_topology", Message: "Node type and role are fixed by the registered adapter."})
		}
		issues = append(issues, validateAutomationNodeConfig(adapter, canonical, node)...)
	}
	if !flexibleTemplate {
		for key := range canonicalNodes {
			if !seenNodes[key] {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: key, Code: "missing_node", Message: fmt.Sprintf("Required node %q is missing. Restore it before saving.", key)})
			}
		}
	}

	canonicalEdges := make(map[string]AutomationAdapterEdge, len(adapter.Edges))
	for _, edge := range adapter.Edges {
		canonicalEdges[edge.Key] = edge
	}
	seenEdgeKeys := map[string]bool{}
	seenEndpointPairs := map[string]bool{}
	seenCanonicalEdges := map[string]bool{}
	for _, edge := range candidate.Edges {
		if edge.Key == "" || seenEdgeKeys[edge.Key] {
			issues = append(issues, models.AutomationValidationIssue{Code: "invalid_edge", Message: "Every graph connection requires a unique key."})
			continue
		}
		seenEdgeKeys[edge.Key] = true
		if edge.FromPort != "right" || edge.ToPort != "left" {
			issues = append(issues, models.AutomationValidationIssue{Code: "invalid_edge", Message: "Graph connections must run from a source OUT port to a target IN port."})
			continue
		}
		if !seenNodes[edge.From] || !seenNodes[edge.To] || edge.From == edge.To {
			issues = append(issues, models.AutomationValidationIssue{Code: "invalid_edge", Message: "Graph edge references an invalid node."})
			continue
		}
		endpointPair := edge.From + "\x00" + edge.To
		if seenEndpointPairs[endpointPair] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: edge.From, Code: "ambiguous_handoff", Message: fmt.Sprintf("Nodes %q and %q have more than one connection. Keep exactly one connection between the same source and target.", edge.From, edge.To)})
		}
		seenEndpointPairs[endpointPair] = true
		if adapter.DynamicTopology || flexibleTemplate {
			conditionState, hasCondition := customAutomationEdgeConditionState(edge.Condition)
			fromNode := draftNodes[edge.From]
			switch fromNode.Role {
			case "native_approval":
				if len(edge.Condition) == 0 {
					issues = append(issues, models.AutomationValidationIssue{Code: "missing_condition", Message: "Choose whether this Human approval connection is the approved or rejected result."})
				} else if !hasCondition || (conditionState != "approved" && conditionState != "rejected") {
					issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_condition", Message: "Human approval connections must select the approved or rejected result."})
				}
			case "github_assignment":
				if len(edge.Condition) == 0 {
					issues = append(issues, models.AutomationValidationIssue{Code: "missing_condition", Message: "Choose the assigned result for this Human assignment connection."})
				} else if !hasCondition || conditionState != "assigned" {
					issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_condition", Message: "Human assignment connections must use the assigned result."})
				}
			default:
				if len(edge.Condition) != 0 {
					issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_condition", Message: "Only supported human gate connections may have a result condition."})
				}
			}
			continue
		}
		canonical, isCanonical := canonicalEdges[edge.Key]
		if !isCanonical || edge.From != canonical.From || edge.To != canonical.To {
			issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_topology", Message: "Custom graph connections can be saved only when they use supported runtime handoffs."})
			if len(edge.Condition) != 0 {
				issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_condition", Message: "Custom edge conditions are not executable by the registered adapter."})
			}
			continue
		}
		seenCanonicalEdges[edge.Key] = true
		expectedCondition := map[string]any{}
		if strings.TrimSpace(canonical.Condition) != "" {
			_ = json.Unmarshal([]byte(canonical.Condition), &expectedCondition)
		}
		actualCondition := edge.Condition
		if actualCondition == nil {
			actualCondition = map[string]any{}
		}
		actualJSON, actualErr := json.Marshal(actualCondition)
		expectedJSON, _ := json.Marshal(expectedCondition)
		if actualErr != nil || !bytes.Equal(actualJSON, expectedJSON) {
			issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_condition", Message: "Edge conditions are fixed by the registered adapter."})
		}
	}
	if !flexibleTemplate {
		for key, canonical := range canonicalEdges {
			if seenCanonicalEdges[key] || !seenNodes[canonical.From] || !seenNodes[canonical.To] {
				continue
			}
			issues = append(issues, models.AutomationValidationIssue{NodeKey: canonical.From, Code: "missing_edge", Message: fmt.Sprintf("Required connection %q from node %q to node %q is missing. Restore that connection before saving.", key, canonical.From, canonical.To)})
		}
	}
	if adapter.DynamicTopology {
		issues = append(issues, validateCustomAutomationTopology(candidate)...)
	} else if flexibleTemplate {
		issues = append(issues, validateMaintainedSDLCTopology(candidate, canonicalNodes)...)
	}
	sortAutomationValidationIssues(issues)
	return issues
}

func validateMaintainedSDLCTopology(candidate models.AutomationDraftCandidate, canonicalNodes map[string]AutomationAdapterNode) []models.AutomationValidationIssue {
	if len(candidate.Nodes) == 0 {
		return []models.AutomationValidationIssue{{Code: "empty_graph", Message: "Keep at least one runnable node before saving."}}
	}
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	incoming := make(map[string][]models.AutomationDraftEdge, len(candidate.Nodes))
	outgoing := make(map[string][]models.AutomationDraftEdge, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	for _, edge := range candidate.Edges {
		if _, ok := nodes[edge.From]; !ok {
			continue
		}
		if _, ok := nodes[edge.To]; !ok || edge.From == edge.To {
			continue
		}
		outgoing[edge.From] = append(outgoing[edge.From], edge)
		incoming[edge.To] = append(incoming[edge.To], edge)
	}

	isProducer := func(role string) bool {
		return role == "offering_manager" || role == "bug_finder" || role == "optimization_finder" || role == "redundancy_finder"
	}
	var issues []models.AutomationValidationIssue
	for _, edge := range candidate.Edges {
		from, fromOK := nodes[edge.From]
		to, toOK := nodes[edge.To]
		if !fromOK || !toOK || edge.From == edge.To {
			continue
		}
		_, fromCanonical := canonicalNodes[from.Key]
		_, toCanonical := canonicalNodes[to.Key]
		if !fromCanonical && !toCanonical {
			continue
		}
		supported := false
		if candidate.AdapterKey == AutomationAdapterNativeSDLC {
			supported = isProducer(from.Role) && to.Role == "create_notification" ||
				from.Role == "create_notification" && to.Role == "native_approval" ||
				from.Role == "native_approval" && (to.Role == "rejected" || to.Role == "native_inbox") ||
				from.Role == "native_inbox" && to.Role == "implementation" ||
				from.Role == "implementation" && to.Role == "completed"
		} else {
			supported = isProducer(from.Role) && to.Role == "create_github_issue" ||
				from.Role == "create_github_issue" && to.Role == "github_assignment" ||
				from.Role == "github_assignment" && to.Role == "github_inbox" ||
				from.Role == "github_inbox" && to.Role == "implementation" ||
				from.Role == "implementation" && to.Role == "open_pull_request" ||
				from.Role == "open_pull_request" && to.Role == "pull_request_review" ||
				from.Role == "pull_request_review" && to.Role == "completed"
		}
		if !supported {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: from.Key, Code: "unsupported_handoff", Message: fmt.Sprintf("Connection %q from node %q to node %q is not a supported runtime handoff.", edge.Key, from.Key, to.Key)})
			continue
		}
		if from.Role == "native_approval" {
			state, _ := customAutomationEdgeConditionState(edge.Condition)
			expected := "approved"
			if to.Role == "rejected" {
				expected = "rejected"
			}
			if state != expected {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: from.Key, Code: "unsupported_condition", Message: fmt.Sprintf("Connection %q to node %q must use the %q result.", edge.Key, to.Key, expected)})
			}
		}
	}

	customNodes := make([]models.AutomationDraftNode, 0)
	for _, node := range candidate.Nodes {
		if _, canonical := canonicalNodes[node.Key]; !canonical {
			customNodes = append(customNodes, node)
		}
	}
	if len(customNodes) > 0 {
		customEdges := make([]models.AutomationDraftEdge, 0)
		for _, edge := range candidate.Edges {
			_, fromCanonical := canonicalNodes[edge.From]
			_, toCanonical := canonicalNodes[edge.To]
			if !fromCanonical && !toCanonical {
				customEdges = append(customEdges, edge)
			}
		}
		customCandidate := candidate
		customCandidate.AdapterKey = AutomationAdapterCustom
		customCandidate.AutomationType = "custom"
		customCandidate.Nodes = customNodes
		customCandidate.Edges = customEdges
		issues = append(issues, validateCustomAutomationTopology(customCandidate)...)
	}

	for _, node := range candidate.Nodes {
		if _, canonical := canonicalNodes[node.Key]; !canonical {
			continue
		}
		in, out := incoming[node.Key], outgoing[node.Key]
		add := func(code, message string) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: code, Message: message})
		}
		switch node.Role {
		case "offering_manager", "bug_finder", "optimization_finder", "redundancy_finder":
			if len(in) != 0 {
				add("schedule_parents", fmt.Sprintf("Schedule node %q cannot have an incoming connection.", node.Key))
			}
		case "loop_auditor":
			if len(in) != 0 {
				add("schedule_parents", fmt.Sprintf("Schedule node %q cannot have an incoming connection.", node.Key))
			}
		case "create_notification":
			if len(in) == 0 || len(out) != 1 || nodes[out[0].To].Role != "native_approval" {
				add("notification_connections", fmt.Sprintf("Node %q needs at least one producer source and exactly one Human approval target.", node.Key))
			}
		case "native_approval":
			if len(in) == 0 {
				add("approval_source", fmt.Sprintf("Human approval node %q needs a Create notification source.", node.Key))
			}
			states := map[string]int{}
			for _, edge := range out {
				state, _ := customAutomationEdgeConditionState(edge.Condition)
				states[state]++
			}
			if states["approved"] > 1 || states["rejected"] > 1 {
				add("approval_branches", fmt.Sprintf("Human approval node %q may have at most one target for each result.", node.Key))
			}
		case "native_inbox":
			if len(in) != 1 || nodes[in[0].From].Role != "native_approval" {
				add("native_inbox_source", fmt.Sprintf("Approved inbox node %q needs exactly one approved Human approval source.", node.Key))
			}
			if len(out) != 1 || nodes[out[0].To].Role != "implementation" {
				add("native_inbox_target", fmt.Sprintf("Approved inbox node %q needs exactly one implementation target.", node.Key))
			}
		case "create_github_issue":
			if len(in) == 0 || len(out) != 1 || nodes[out[0].To].Role != "github_assignment" {
				add("github_issue_connections", fmt.Sprintf("Node %q needs at least one producer source and exactly one Human assignment target.", node.Key))
			}
		case "github_assignment":
			if len(in) == 0 {
				add("github_assignment_source", fmt.Sprintf("Human assignment node %q needs a Create GitHub issue source.", node.Key))
			}
			if len(out) != 1 || nodes[out[0].To].Role != "github_inbox" {
				add("github_assignment_target", fmt.Sprintf("Human assignment node %q needs exactly one assigned GitHub inbox target.", node.Key))
			}
		case "github_inbox":
			if len(in) != 1 || nodes[in[0].From].Role != "github_assignment" {
				add("github_inbox_source", fmt.Sprintf("GitHub inbox node %q needs exactly one Human assignment source.", node.Key))
			}
			if len(out) != 1 || nodes[out[0].To].Role != "implementation" {
				add("github_inbox_target", fmt.Sprintf("GitHub inbox node %q needs exactly one implementation target.", node.Key))
			}
		case "implementation":
			if candidate.AdapterKey == AutomationAdapterNativeSDLC {
				if len(in) != 1 || nodes[in[0].From].Role != "native_inbox" {
					add("native_implementation_source", fmt.Sprintf("Implementation node %q needs exactly one Approved inbox source.", node.Key))
				}
				if len(out) != 1 || nodes[out[0].To].Type != models.AutomationNodeOutcome {
					add("native_implementation_target", fmt.Sprintf("Implementation node %q needs exactly one terminal Outcome.", node.Key))
				}
			} else {
				if len(in) != 1 || nodes[in[0].From].Role != "github_inbox" {
					add("github_implementation_source", fmt.Sprintf("Implementation node %q needs exactly one GitHub inbox source.", node.Key))
				}
				if len(out) != 1 || nodes[out[0].To].Role != "open_pull_request" {
					add("github_implementation_target", fmt.Sprintf("Implementation node %q needs exactly one Open pull request target.", node.Key))
				}
			}
		case "open_pull_request":
			if len(in) != 1 || nodes[in[0].From].Role != "implementation" {
				add("pull_request_source", fmt.Sprintf("Open pull request node %q needs exactly one implementation source.", node.Key))
			}
			if len(out) != 1 || nodes[out[0].To].Role != "pull_request_review" {
				add("pull_request_target", fmt.Sprintf("Open pull request node %q needs exactly one Human review target.", node.Key))
			}
		case "pull_request_review":
			if len(in) != 1 || nodes[in[0].From].Role != "open_pull_request" {
				add("pull_request_review_source", fmt.Sprintf("Human review node %q needs exactly one Open pull request source.", node.Key))
			}
			if len(out) != 1 || nodes[out[0].To].Type != models.AutomationNodeOutcome {
				add("pull_request_review_target", fmt.Sprintf("Human review node %q needs exactly one terminal Outcome.", node.Key))
			}
		case "completed", "rejected":
			if len(out) != 0 {
				add("outcome_terminal", fmt.Sprintf("Outcome node %q must be the end of a path.", node.Key))
			}
		}
	}

	indegree := make(map[string]int, len(nodes))
	queue := make([]string, 0, len(nodes))
	for key := range nodes {
		indegree[key] = len(incoming[key])
		if indegree[key] == 0 {
			queue = append(queue, key)
		}
	}
	visited := 0
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		visited++
		for _, edge := range outgoing[key] {
			indegree[edge.To]--
			if indegree[edge.To] == 0 {
				queue = append(queue, edge.To)
			}
		}
	}
	if visited != len(nodes) {
		issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_cycle", Message: "Executable Automation handoffs must not contain a cycle."})
	}
	return issues
}

func customAutomationEdgeConditionState(condition map[string]any) (string, bool) {
	if len(condition) != 1 {
		return "", false
	}
	state, ok := condition["state"].(string)
	return state, ok
}

func customAutomationTaskSource(node models.AutomationDraftNode) bool {
	return node.Type == models.AutomationNodeTrigger && node.Role == "fixed_schedule" || node.Type == models.AutomationNodeAgentTask && node.Role == "task"
}

func customAutomationNativeImplementation(candidate models.AutomationDraftCandidate, nodeKey string) bool {
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	node, ok := nodes[nodeKey]
	if !ok || node.Type != models.AutomationNodeAgentTask || node.Role != "implementation" {
		return false
	}
	for key := range node.Config {
		if key != "goal" && key != "model_config_id" {
			return false
		}
	}
	incoming, outgoing := 0, 0
	for _, edge := range candidate.Edges {
		if edge.To == nodeKey && nodes[edge.From].Type == models.AutomationNodeTrigger && nodes[edge.From].Role == "native_inbox" {
			incoming++
		}
		if edge.From == nodeKey && nodes[edge.To].Type == models.AutomationNodeOutcome {
			outgoing++
		}
	}
	return incoming == 1 && outgoing == 1
}

func customAutomationGitHubIssueTask(candidate models.AutomationDraftCandidate, nodeKey string) bool {
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	node, ok := nodes[nodeKey]
	if !ok || node.Type != models.AutomationNodeAgentTask || node.Role != "task" {
		return false
	}
	inboxSources := 0
	pullRequestTargets := 0
	incoming := 0
	outgoing := 0
	for _, edge := range candidate.Edges {
		if edge.To == nodeKey {
			incoming++
			source := nodes[edge.From]
			if source.Role == "github_inbox" && (source.Type == models.AutomationNodeAgentTask || source.Type == models.AutomationNodeTrigger) {
				inboxSources++
			}
		}
		if edge.From == nodeKey {
			outgoing++
			target := nodes[edge.To]
			if target.Type == models.AutomationNodeAction && target.Role == "open_pull_request" {
				pullRequestTargets++
			}
		}
	}
	return incoming == 1 && inboxSources == 1 && outgoing == 1 && pullRequestTargets == 1
}

func customAutomationGitHubIssueTaskTarget(candidate models.AutomationDraftCandidate, inboxKey string) *models.AutomationDraftNode {
	for _, edge := range candidate.Edges {
		if edge.From != inboxKey || !customAutomationGitHubIssueTask(candidate, edge.To) {
			continue
		}
		for _, node := range candidate.Nodes {
			if node.Key == edge.To {
				value := node
				return &value
			}
		}
	}
	return nil
}

func customAutomationHandoffSupported(from, to models.AutomationDraftNode) bool {
	if customAutomationTaskSource(from) {
		return to.Type == models.AutomationNodeAgentTask && to.Role == "task" ||
			from.Type == models.AutomationNodeTrigger && to.Type == models.AutomationNodeAgentTask && to.Role == "github_inbox" ||
			to.Type == models.AutomationNodeOutcome ||
			to.Type == models.AutomationNodeAction && (to.Role == "create_notification" || to.Role == "create_github_issue" || to.Role == "open_pull_request")
	}
	return from.Type == models.AutomationNodeAction && from.Role == "create_notification" && to.Type == models.AutomationNodeHumanGate && to.Role == "native_approval" ||
		from.Type == models.AutomationNodeHumanGate && from.Role == "native_approval" && (to.Type == models.AutomationNodeOutcome || to.Type == models.AutomationNodeTrigger && to.Role == "native_inbox") ||
		from.Type == models.AutomationNodeTrigger && from.Role == "native_inbox" && to.Type == models.AutomationNodeAgentTask && to.Role == "implementation" ||
		from.Type == models.AutomationNodeAgentTask && from.Role == "implementation" && to.Type == models.AutomationNodeOutcome ||
		from.Type == models.AutomationNodeAction && from.Role == "create_github_issue" && to.Type == models.AutomationNodeHumanGate && to.Role == "github_assignment" ||
		from.Type == models.AutomationNodeHumanGate && from.Role == "github_assignment" && (to.Type == models.AutomationNodeAgentTask || to.Type == models.AutomationNodeTrigger) && to.Role == "github_inbox" ||
		(from.Type == models.AutomationNodeAgentTask || from.Type == models.AutomationNodeTrigger) && from.Role == "github_inbox" && to.Type == models.AutomationNodeAgentTask && to.Role == "task" ||
		from.Type == models.AutomationNodeAction && from.Role == "open_pull_request" && to.Type == models.AutomationNodeHumanGate && to.Role == "pull_request_review" ||
		from.Type == models.AutomationNodeHumanGate && from.Role == "pull_request_review" && to.Type == models.AutomationNodeOutcome
}

func validateCustomAutomationTopology(candidate models.AutomationDraftCandidate) []models.AutomationValidationIssue {
	if len(candidate.Nodes) == 0 {
		return []models.AutomationValidationIssue{{Code: "empty_graph", Message: "Add a capability node before saving."}}
	}
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	incoming := make(map[string][]models.AutomationDraftEdge, len(candidate.Nodes))
	outgoing := make(map[string][]models.AutomationDraftEdge, len(candidate.Nodes))
	var issues []models.AutomationValidationIssue
	nativeMailbox, githubMailbox := false, false
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
		switch node.Role {
		case "native_approval", "native_inbox", "implementation":
			nativeMailbox = true
		case "github_assignment", "github_inbox", "open_pull_request", "pull_request_review":
			githubMailbox = true
		}
		switch node.Type {
		case models.AutomationNodeTrigger:
			if node.Role != "fixed_schedule" && node.Role != "native_inbox" && node.Role != "github_inbox" {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This Schedule role is not executable in custom automations yet."})
			}
		case models.AutomationNodeAgentTask:
			if node.Role != "task" && node.Role != "github_inbox" && node.Role != "implementation" {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This Task role is not executable in custom automations yet."})
			}
		case models.AutomationNodeAction:
			if node.Role != "create_notification" && node.Role != "create_github_issue" && node.Role != "open_pull_request" {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This action is not executable in custom automations yet."})
			}
		case models.AutomationNodeHumanGate:
			if node.Role != "native_approval" && node.Role != "github_assignment" && node.Role != "pull_request_review" {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This human gate is not executable in custom automations yet."})
			}
		case models.AutomationNodeOutcome:
		default:
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This capability is not executable in custom automations yet."})
		}
	}
	if nativeMailbox && githubMailbox {
		issues = append(issues, models.AutomationValidationIssue{Code: "mixed_mailbox_families", Message: "Choose either the Native approval/inbox flow or the GitHub assignment/inbox/review flow; do not combine mailbox families."})
	}
	for _, edge := range candidate.Edges {
		from, fromOK := nodes[edge.From]
		to, toOK := nodes[edge.To]
		if !fromOK || !toOK || edge.From == edge.To {
			continue
		}
		outgoing[edge.From] = append(outgoing[edge.From], edge)
		incoming[edge.To] = append(incoming[edge.To], edge)
		if !customAutomationHandoffSupported(from, to) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: from.Key, Code: "unsupported_handoff", Message: "This connection does not map to a supported OpenVibely capability handoff."})
		}
	}
	for _, node := range candidate.Nodes {
		if customAutomationTaskSource(node) {
			targetKinds := map[string]int{}
			for _, edge := range outgoing[node.Key] {
				target := nodes[edge.To]
				switch {
				case target.Type == models.AutomationNodeOutcome:
					targetKinds["Outcome"]++
				case target.Type == models.AutomationNodeAction && target.Role == "create_notification":
					targetKinds["Create notification"]++
				case target.Type == models.AutomationNodeAction && target.Role == "create_github_issue":
					targetKinds["Create GitHub issue"]++
				}
			}
			for kind, count := range targetKinds {
				if count > 1 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "ambiguous_handoff", Message: fmt.Sprintf("Connect this task to at most one %s target; the existing runtime cannot distinguish duplicate targets of the same kind.", kind)})
				}
			}
		}
		switch node.Type {
		case models.AutomationNodeTrigger:
			if node.Role == "native_inbox" {
				approvals := 0
				for _, edge := range incoming[node.Key] {
					state, ok := customAutomationEdgeConditionState(edge.Condition)
					if nodes[edge.From].Role == "native_approval" && ok && state == "approved" {
						approvals++
					}
				}
				if approvals != 1 || len(incoming[node.Key]) != 1 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "native_inbox_source", Message: "An Approved inbox needs exactly one approved connection from Human approval."})
				}
				if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Role != "implementation" {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "native_inbox_target", Message: "An Approved inbox needs exactly one Native implementation target."})
				}
			} else if node.Role == "github_inbox" {
				assignments := 0
				for _, edge := range incoming[node.Key] {
					state, ok := customAutomationEdgeConditionState(edge.Condition)
					if nodes[edge.From].Role == "github_assignment" && ok && state == "assigned" {
						assignments++
					}
				}
				if assignments != 1 || len(incoming[node.Key]) != 1 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_inbox_sources", Message: "A scheduled GitHub inbox needs exactly one assigned connection from Human assignment."})
				}
				if customAutomationGitHubIssueTaskTarget(candidate, node.Key) == nil {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_inbox_target", Message: "A GitHub inbox needs one Task projection connected to an Open pull request action."})
				}
			} else if len(incoming[node.Key]) != 0 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "schedule_parents", Message: "A Schedule starts its own task and cannot have an incoming connection."})
			}
		case models.AutomationNodeAgentTask:
			switch node.Role {
			case "task":
				if customAutomationGitHubIssueTask(candidate, node.Key) {
					category, categoryOK := node.Config["category"].(string)
					if !categoryOK || category != string(models.CategoryActive) {
						issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "A Task between a GitHub inbox and Open pull request must use the active category so assignment starts implementation immediately."})
					}
					break
				}
				parents := 0
				githubConnections := 0
				for _, edge := range incoming[node.Key] {
					source := nodes[edge.From]
					if customAutomationTaskSource(source) {
						parents++
					}
					if source.Role == "github_inbox" {
						githubConnections++
					}
				}
				for _, edge := range outgoing[node.Key] {
					if nodes[edge.To].Role == "open_pull_request" {
						githubConnections++
					}
				}
				if parents > 1 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "task_parents", Message: "A task can have at most one task or Schedule parent because OpenVibely tasks store one parent."})
				}
				if githubConnections > 0 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_task_connections", Message: "A Task between a GitHub inbox and Open pull request must have exactly those two connections."})
				}
			case "implementation":
				if len(incoming[node.Key]) != 1 || nodes[incoming[node.Key][0].From].Role != "native_inbox" {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "native_implementation_source", Message: "Native implementation needs exactly one Approved inbox source."})
				}
				if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Type != models.AutomationNodeOutcome {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "native_implementation_target", Message: "Native implementation needs exactly one terminal Outcome."})
				}
			case "github_inbox":
				schedules, assignments := 0, 0
				for _, edge := range incoming[node.Key] {
					source := nodes[edge.From]
					if source.Type == models.AutomationNodeTrigger {
						schedules++
					}
					if source.Role == "github_assignment" {
						assignments++
					}
				}
				if schedules != 1 || assignments != 1 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_inbox_sources", Message: "A GitHub inbox needs one Schedule and one Human assignment source."})
				}
				if customAutomationGitHubIssueTaskTarget(candidate, node.Key) == nil {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_inbox_target", Message: "A GitHub inbox needs one Task connected to an Open pull request action."})
				}
			}
		case models.AutomationNodeAction:
			switch node.Role {
			case "create_notification":
				sources := 0
				for _, edge := range incoming[node.Key] {
					if customAutomationTaskSource(nodes[edge.From]) {
						sources++
					}
				}
				if sources == 0 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "notification_source", Message: "Connect a Schedule or task to this Create notification action."})
				}
				if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Role != "native_approval" {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "notification_target", Message: "A Create notification action needs one Human approval node."})
				}
			case "create_github_issue":
				sources := 0
				for _, edge := range incoming[node.Key] {
					if customAutomationTaskSource(nodes[edge.From]) {
						sources++
					}
				}
				if sources == 0 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_issue_source", Message: "Connect a Schedule or task to this Create GitHub issue action."})
				}
				if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Role != "github_assignment" {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_issue_target", Message: "A Create GitHub issue action needs one Human assignment gate."})
				}
			case "open_pull_request":
				validTaskSource := false
				if len(incoming[node.Key]) == 1 {
					validTaskSource = customAutomationGitHubIssueTask(candidate, incoming[node.Key][0].From)
				}
				if !validTaskSource {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "pull_request_source", Message: "Connect a GitHub inbox through one Task to this Open pull request action."})
				}
				if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Role != "pull_request_review" {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "pull_request_target", Message: "An Open pull request action needs one Human review gate."})
				}
			}
		case models.AutomationNodeHumanGate:
			switch node.Role {
			case "native_approval":
				if len(incoming[node.Key]) == 0 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "approval_source", Message: "Connect a Create notification action to this Human approval node."})
				}
				states := map[string]int{}
				for _, edge := range outgoing[node.Key] {
					state, ok := customAutomationEdgeConditionState(edge.Condition)
					if ok && (nodes[edge.To].Type == models.AutomationNodeOutcome || nodes[edge.To].Role == "native_inbox") {
						states[state]++
					}
				}
				if states["approved"] > 1 || states["rejected"] > 1 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "approval_branches", Message: "Human approval may expose at most one target for each result."})
				}
			case "github_assignment":
				if len(incoming[node.Key]) == 0 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_assignment_source", Message: "Connect a Create GitHub issue action to this Human assignment gate."})
				}
				state, validState := "", false
				if len(outgoing[node.Key]) == 1 {
					state, validState = customAutomationEdgeConditionState(outgoing[node.Key][0].Condition)
				}
				if len(outgoing[node.Key]) != 1 || !validState || state != "assigned" || nodes[outgoing[node.Key][0].To].Role != "github_inbox" {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_assignment_target", Message: "Human assignment needs one assigned connection to a GitHub inbox."})
				}
			case "pull_request_review":
				if len(incoming[node.Key]) == 0 {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "pull_request_review_source", Message: "Connect an Open pull request action to this Human review gate."})
				}
				if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Type != models.AutomationNodeOutcome {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "pull_request_review_target", Message: "A Human review gate needs one terminal Outcome for an observed merge."})
				}
			}
		case models.AutomationNodeOutcome:
			if len(outgoing[node.Key]) != 0 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "outcome_terminal", Message: "An Outcome must be the end of a path."})
			}
		}
	}

	indegree := make(map[string]int, len(nodes))
	for key := range nodes {
		indegree[key] = len(incoming[key])
	}
	queue := make([]string, 0, len(nodes))
	for key, degree := range indegree {
		if degree == 0 {
			queue = append(queue, key)
		}
	}
	visited := 0
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		visited++
		for _, edge := range outgoing[key] {
			indegree[edge.To]--
			if indegree[edge.To] == 0 {
				queue = append(queue, edge.To)
			}
		}
	}
	if visited != len(nodes) {
		issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_cycle", Message: "Executable Automation handoffs must not contain a cycle."})
	}
	return issues
}

func validateCustomAutomationNodeConfig(node models.AutomationDraftNode) []models.AutomationValidationIssue {
	allowed := map[string]bool{}
	switch node.Type {
	case models.AutomationNodeAgentTask:
		if node.Role == "implementation" {
			allowed = map[string]bool{"goal": true, "model_config_id": true}
		} else {
			allowed = map[string]bool{"prompt": true, "goal": true, "category": true, "priority": true, "agent_ref": true, "model_config_id": true}
		}
	case models.AutomationNodeTrigger:
		allowed = map[string]bool{"prompt": true, "goal": true, "category": true, "priority": true, "agent_ref": true, "model_config_id": true, "run_at": true, "repeat_type": true, "repeat_interval": true, "enabled": true, "clear_context_on_start": true}
	case models.AutomationNodeAction:
		switch node.Role {
		case "create_notification":
			allowed = map[string]bool{"notification_type": true, "instructions": true}
		case "create_github_issue":
			allowed = map[string]bool{"instructions": true, "labels": true}
		case "open_pull_request":
			allowed = map[string]bool{"instructions": true, "base": true, "draft": true}
		}
	case models.AutomationNodeHumanGate:
		allowed = map[string]bool{"approval_method": true}
	case models.AutomationNodeOutcome:
		allowed = map[string]bool{}
	}
	var issues []models.AutomationValidationIssue
	validRole := false
	switch node.Type {
	case models.AutomationNodeTrigger:
		validRole = node.Role == "fixed_schedule" || node.Role == "native_inbox" || node.Role == "github_inbox"
	case models.AutomationNodeAgentTask:
		validRole = node.Role == "task" || node.Role == "github_inbox" || node.Role == "implementation"
	case models.AutomationNodeAction:
		validRole = node.Role == "create_notification" || node.Role == "create_github_issue" || node.Role == "open_pull_request"
	case models.AutomationNodeHumanGate:
		validRole = node.Role == "native_approval" || node.Role == "github_assignment" || node.Role == "pull_request_review"
	case models.AutomationNodeOutcome:
		validRole = node.Role == "completed"
	}
	if !validRole {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This node purpose is not an allowlisted custom Automation capability."})
	}
	for key, value := range node.Config {
		if !allowed[key] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unknown_config", Message: fmt.Sprintf("Configuration field %q is not supported for this node.", key)})
			continue
		}
		if unsafeAutomationConfigValue(key, value) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsafe_config", Message: fmt.Sprintf("Configuration field %q contains an unsupported value.", key)})
		}
	}
	if node.Type == models.AutomationNodeAgentTask && node.Role != "implementation" {
		prompt, promptOK := node.Config["prompt"].(string)
		if !promptOK || strings.TrimSpace(prompt) == "" {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "missing_prompt", Message: "Task nodes require a prompt before saving."})
		}
		category, categoryOK := node.Config["category"].(string)
		if !categoryOK || (category != string(models.CategoryBacklog) && category != string(models.CategoryActive)) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "Custom Automation Task category must be active or backlog."})
		}
		priority, priorityOK := draftInt(node.Config["priority"])
		if !priorityOK || priority < 1 || priority > 4 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "priority", Message: "Task priority must be between 1 and 4."})
		}
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
	}
	if node.Type == models.AutomationNodeAgentTask && node.Role == "implementation" {
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
	}
	if node.Type == models.AutomationNodeTrigger {
		prompt, promptOK := node.Config["prompt"].(string)
		if !promptOK || strings.TrimSpace(prompt) == "" {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "missing_prompt", Message: "Schedule nodes require a task prompt before saving."})
		}
		category, categoryOK := node.Config["category"].(string)
		if !categoryOK || category != string(models.CategoryScheduled) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "A Schedule node must use the scheduled task category."})
		}
		priority, priorityOK := draftInt(node.Config["priority"])
		if !priorityOK || priority < 1 || priority > 4 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "priority", Message: "Schedule task priority must be between 1 and 4."})
		}
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
		issues = append(issues, validateAutomationScheduleConfig(node)...)
	}
	if goal, present := node.Config["goal"]; present {
		if text, ok := goal.(string); !ok || len(strings.TrimSpace(text)) > MaxTaskGoalLength {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "goal", Message: "Task goal must be text of at most 2000 characters."})
		}
	}
	if node.Type == models.AutomationNodeAction {
		issues = append(issues, validateAutomationActionConfig(node, node.Role)...)
	}
	if node.Type == models.AutomationNodeHumanGate {
		issues = append(issues, validateAutomationHumanGateConfig(node, node.Role)...)
	}
	return issues
}

func validateAutomationScheduleConfig(node models.AutomationDraftNode) []models.AutomationValidationIssue {
	var issues []models.AutomationValidationIssue
	runAt, runAtOK := node.Config["run_at"].(string)
	if !runAtOK {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "run_at", Message: "Trigger time must use HH:MM local time."})
	} else if _, err := time.Parse("15:04", runAt); err != nil {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "run_at", Message: "Trigger time must use HH:MM local time."})
	}
	repeat, repeatOK := node.Config["repeat_type"].(string)
	if !repeatOK || !map[string]bool{"once": true, "minutes": true, "hours": true, "daily": true, "weekly": true, "monthly": true}[repeat] {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "repeat_type", Message: "Unsupported schedule repeat type."})
	}
	interval, intervalOK := draftInt(node.Config["repeat_interval"])
	if !intervalOK || models.ValidateScheduleRepeatInterval(interval) != nil {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "repeat_interval", Message: "Schedule interval must be between 1 and 365."})
	}
	if enabled, enabledOK := node.Config["enabled"].(bool); !enabledOK || !enabled {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "enabled", Message: "Schedule execution is controlled by the Automation lifecycle and must be enabled."})
	}
	if clearContextOnStart, present := node.Config["clear_context_on_start"]; present {
		if _, valid := clearContextOnStart.(bool); !valid {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "clear_context_on_start", Message: "Clear context on start must be true or false."})
		}
	}
	return issues
}

func validateAutomationActionConfig(node models.AutomationDraftNode, role string) []models.AutomationValidationIssue {
	var issues []models.AutomationValidationIssue
	instructions, instructionsOK := node.Config["instructions"].(string)
	if !instructionsOK || strings.TrimSpace(instructions) == "" || len(instructions) > 2000 {
		issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "action_instructions", Message: "This action requires instructions of at most 2000 characters."})
	}
	switch role {
	case "create_notification":
		notificationType, typeOK := node.Config["notification_type"].(string)
		if !typeOK || strings.TrimSpace(notificationType) == "" || len(notificationType) > 100 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "notification_type", Message: "Create notification requires a notification type of at most 100 characters."})
		}
	case "create_github_issue":
		labels, labelsOK := draftStringSlice(node.Config["labels"])
		if !labelsOK || len(labels) > 10 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_issue_labels", Message: "GitHub issue labels must be a list of at most 10 labels."})
		} else {
			for _, label := range labels {
				label = strings.TrimSpace(label)
				if label == "" || len(label) > 100 || strings.HasPrefix(strings.ToLower(label), "openvibely:") {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "github_issue_labels", Message: "GitHub issue labels must be non-empty plain labels of at most 100 characters and must not use the openvibely: prefix."})
					break
				}
			}
		}
	case "open_pull_request":
		base, baseOK := node.Config["base"].(string)
		if !baseOK || len(strings.TrimSpace(base)) > 200 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "pull_request_base", Message: "Pull request base must be blank or at most 200 characters."})
		}
		if _, draftOK := node.Config["draft"].(bool); !draftOK {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "pull_request_draft", Message: "Pull request draft state must be true or false."})
		}
	}
	return issues
}

func validateAutomationHumanGateConfig(node models.AutomationDraftNode, role string) []models.AutomationValidationIssue {
	method, methodOK := node.Config["approval_method"].(string)
	expectedMethod := map[string]string{"native_approval": "native_alert", "github_assignment": "github_assignment", "pull_request_review": "pull_request_review"}[role]
	if !methodOK || expectedMethod == "" || method != expectedMethod {
		return []models.AutomationValidationIssue{{NodeKey: node.Key, Code: "approval_method", Message: "This human gate must use its matching human-controlled approval method."}}
	}
	return nil
}

func validateAutomationNodeConfig(adapter AutomationAdapter, canonical AutomationAdapterNode, node models.AutomationDraftNode) []models.AutomationValidationIssue {
	allowed := map[string]bool{}
	usesTaskConfiguration := automationMaintainedNodeUsesTaskConfiguration(adapter, canonical)
	usesModelConfiguration := usesTaskConfiguration || adapter.Key == AutomationAdapterNativeSDLC && canonical.Role == "implementation"
	usesGoalConfiguration := usesModelConfiguration
	if usesTaskConfiguration {
		for _, key := range []string{"prompt", "category", "priority", "agent_ref"} {
			allowed[key] = true
		}
	}
	if usesGoalConfiguration {
		allowed["goal"] = true
	}
	if usesModelConfiguration {
		allowed["model_config_id"] = true
	}
	if canonical.AllowedResources["schedule"] {
		for _, key := range []string{"target_node_key", "run_at", "repeat_type", "repeat_interval", "enabled", "clear_context_on_start"} {
			allowed[key] = true
		}
	}
	switch canonical.Role {
	case "create_notification":
		allowed["notification_type"] = true
		allowed["instructions"] = true
	case "create_github_issue":
		allowed["instructions"] = true
		allowed["labels"] = true
	case "open_pull_request":
		allowed["instructions"] = true
		allowed["base"] = true
		allowed["draft"] = true
	case "native_approval", "github_assignment", "pull_request_review":
		allowed["approval_method"] = true
	}
	var issues []models.AutomationValidationIssue
	for key, value := range node.Config {
		if !allowed[key] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unknown_config", Message: fmt.Sprintf("Configuration field %q is not supported for this node.", key)})
			continue
		}
		if unsafeAutomationConfigValue(key, value) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsafe_config", Message: fmt.Sprintf("Configuration field %q contains an unsupported value.", key)})
		}
	}
	if usesTaskConfiguration {
		prompt, promptOK := node.Config["prompt"].(string)
		if !promptOK || strings.TrimSpace(prompt) == "" {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "missing_prompt", Message: "Task nodes require a prompt before saving."})
		}
		category, categoryOK := node.Config["category"].(string)
		if node.Role == "implementation" && adapter.Key == AutomationAdapterGitHubSDLC {
			if !categoryOK || category != string(models.CategoryActive) {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "GitHub implementation task category must be active."})
			}
		} else if !categoryOK || (category != string(models.CategoryBacklog) && category != string(models.CategoryScheduled)) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "Automation task category must be backlog or scheduled."})
		}
		priority, priorityOK := draftInt(node.Config["priority"])
		if !priorityOK || priority < 1 || priority > 4 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "priority", Message: "Task priority must be between 1 and 4."})
		}
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
	}
	if usesModelConfiguration && !usesTaskConfiguration {
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
	}
	if usesGoalConfiguration {
		if goal, present := node.Config["goal"]; present {
			if text, ok := goal.(string); !ok || len(strings.TrimSpace(text)) > MaxTaskGoalLength {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "goal", Message: "Task goal must be text of at most 2000 characters."})
			}
		}
	}
	if canonical.AllowedResources["schedule"] {
		target, targetOK := node.Config["target_node_key"].(string)
		if !targetOK || target == "" || target != adapterScheduleTarget(adapter, node.Key) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "schedule_target", Message: "Trigger target is fixed by the registered adapter."})
		}
		issues = append(issues, validateAutomationScheduleConfig(node)...)
	}
	switch canonical.Role {
	case "create_notification", "create_github_issue", "open_pull_request":
		issues = append(issues, validateAutomationActionConfig(node, canonical.Role)...)
	case "native_approval", "github_assignment", "pull_request_review":
		issues = append(issues, validateAutomationHumanGateConfig(node, canonical.Role)...)
	}
	return issues
}

func validateAutomationTaskReferenceShape(node models.AutomationDraftNode) []models.AutomationValidationIssue {
	var issues []models.AutomationValidationIssue
	if value, exists := node.Config["agent_ref"]; exists {
		ref, ok := value.(string)
		if !ok || len(strings.TrimSpace(ref)) > 200 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection must use a supported project Agent reference."})
		}
	}
	if value, exists := node.Config["model_config_id"]; exists {
		ref, ok := value.(string)
		if !ok || len(strings.TrimSpace(ref)) > 200 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "model_config_id", Message: "Model selection must use a supported configured model."})
		}
	}
	return issues
}

func (s *AutomationDraftService) ValidateCandidateWithCapabilities(candidate models.AutomationDraftCandidate, snapshot models.AutomationCapabilitySnapshot) []models.AutomationValidationIssue {
	issues := s.ValidateCandidate(candidate)
	if automationUsesGitHub(candidate) {
		github, configured := snapshot.Integrations["github"]
		if !configured || !github.Configured {
			issues = append(issues, models.AutomationValidationIssue{Code: "github_unavailable", Message: "GitHub is not ready for this project. Configure connected GitHub authentication, at least one GitHub Authorized User, and either a project GitHub repository URL or a GitHub remote in the project's local checkout before saving this Automation."})
		}
	}
	agents := make(map[string]bool, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		agents[agent.ID] = true
	}
	modelsByID := make(map[string]bool, len(snapshot.Models))
	for _, model := range snapshot.Models {
		modelsByID[model.ID] = true
	}
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
			continue
		}
		agentRef, _ := node.Config["agent_ref"].(string)
		agentRef = strings.TrimSpace(agentRef)
		modelConfigID := automationExplicitModelConfigID(node.Config["model_config_id"])
		if agentRef != "" && !agents[agentRef] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection is unavailable in this project."})
		}
		if modelConfigID != "" && !modelsByID[modelConfigID] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "model_config_id", Message: "Model selection is unavailable in this project."})
		}
	}
	sortAutomationValidationIssues(issues)
	return issues
}

func automationCandidateHasAgentReferences(candidate models.AutomationDraftCandidate) bool {
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
			continue
		}
		ref, _ := node.Config["agent_ref"].(string)
		if strings.TrimSpace(ref) != "" {
			return true
		}
	}
	return false
}

func (s *AutomationDraftService) validateCandidateForProject(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]models.AutomationValidationIssue, error) {
	if s.capabilities == nil {
		issues := s.ValidateCandidate(candidate)
		for _, node := range candidate.Nodes {
			if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
				continue
			}
			if ref, _ := node.Config["agent_ref"].(string); strings.TrimSpace(ref) != "" {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection cannot be resolved because project capabilities are unavailable."})
			}
		}
		sortAutomationValidationIssues(issues)
		return issues, nil
	}
	snapshot, err := s.capabilities.BuildForValidation(ctx, projectID, automationCandidateHasAgentReferences(candidate))
	if err != nil {
		return nil, err
	}
	return s.ValidateCandidateWithCapabilities(candidate, snapshot), nil
}

func sortAutomationValidationIssues(issues []models.AutomationValidationIssue) {
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].NodeKey != issues[j].NodeKey {
			return issues[i].NodeKey < issues[j].NodeKey
		}
		if issues[i].Code != issues[j].Code {
			return issues[i].Code < issues[j].Code
		}
		return issues[i].Message < issues[j].Message
	})
}

func unsafeAutomationConfigValue(key string, value any) bool {
	if key == "model_config_id" {
		text, ok := value.(string)
		return !ok || len(strings.TrimSpace(text)) > 200
	}
	if strings.Contains(strings.ToLower(key), "url") || strings.Contains(strings.ToLower(key), "sql") || strings.Contains(strings.ToLower(key), "code") || strings.Contains(strings.ToLower(key), "tool") || strings.HasSuffix(strings.ToLower(key), "_id") {
		return true
	}
	text, ok := value.(string)
	if !ok {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	if len(text) > 20000 || strings.Contains(lower, "http://") || strings.Contains(lower, "https://") || strings.Contains(lower, "```") || strings.Contains(lower, "<script") {
		return true
	}
	for _, executable := range []string{"#!/bin/", "#!/usr/bin/", "rm -rf ", "drop table ", "delete from ", "insert into ", "alter table ", "truncate table "} {
		if strings.Contains(lower, executable) {
			return true
		}
	}
	return false
}

func draftInt(value any) (int, bool) {
	switch number := value.(type) {
	case int:
		return number, true
	case float64:
		if number != float64(int(number)) {
			return 0, false
		}
		return int(number), true
	case json.Number:
		parsed, err := number.Int64()
		return int(parsed), err == nil
	default:
		return 0, false
	}
}

func adapterNodeAccepts(adapter AutomationAdapter, nodeKey, resourceType string) bool {
	for _, node := range adapter.Nodes {
		if node.Key == nodeKey {
			return node.AllowedResources[resourceType]
		}
	}
	return false
}

func adapterScheduleTarget(adapter AutomationAdapter, triggerKey string) string {
	for _, node := range adapter.Nodes {
		if node.Key == triggerKey && node.AllowedResources["schedule"] && node.AllowedResources["task"] {
			return node.Key
		}
	}
	for _, edge := range adapter.Edges {
		if edge.From != triggerKey {
			continue
		}
		for _, node := range adapter.Nodes {
			if node.Key == edge.To && node.AllowedResources["task"] {
				return node.Key
			}
		}
	}
	return ""
}

func defaultAutomationNodePrompt(adapterKey, role string) (string, error) {
	switch adapterKey {
	case AutomationAdapterNativeSDLC:
		return nativeSDLCRolePrompt(role)
	case AutomationAdapterGitHubSDLC:
		return githubSDLCRolePrompt(role)
	default:
		switch strings.TrimSpace(role) {
		case "fixed_schedule":
			return "Describe the scheduled work this node should perform.", nil
		case "task":
			return "Describe the work this node should perform.", nil
		default:
			return fmt.Sprintf("Run the %s role for this %s automation using the existing project-scoped tools and human review boundaries.", strings.ReplaceAll(role, "_", " "), strings.ReplaceAll(adapterKey, "_", " ")), nil
		}
	}
}

// PreviewValidatedCandidate builds the display result for a candidate that has
// already been normalized and validated by AutomationCompiler.
func (s *AutomationDraftService) PreviewValidatedCandidate(candidate models.AutomationDraftCandidate, definition *models.AutomationDefinition) *models.AutomationDraftResult {
	return draftPreviewResult(candidate, definition)
}

func (s *AutomationDraftService) PreviewCandidate(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate, definition *models.AutomationDefinition) (*models.AutomationDraftResult, error) {
	candidate, err := s.NormalizeCandidate(candidate)
	if err != nil {
		return nil, err
	}
	issues, err := s.validateCandidateForProject(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	result := draftPreviewResult(candidate, definition)
	result.ValidationErrors = issues
	return result, nil
}

// LoadCurrentCandidate loads and normalizes the persisted candidate without
// validating it against the current project capabilities. Edit callers can use
// this when they will validate the candidate that is actually being submitted.
func (s *AutomationDraftService) LoadCurrentCandidate(ctx context.Context, projectID, automationID string) (*models.AutomationDraftResult, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("automation repository is unavailable")
	}
	current, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil {
		return nil, err
	}
	if current == nil || current.Version.State != models.AutomationVersionPublished {
		return nil, errors.New("saved automation not found")
	}
	candidate, err := s.candidateFromDefinition(ctx, projectID, automationID, current)
	if err != nil {
		return nil, err
	}
	candidate, err = s.hydratePersistedScheduleContext(ctx, projectID, candidate, current)
	if err != nil {
		return nil, err
	}
	candidate, err = s.normalizeReopenedCandidate(candidate)
	if err != nil {
		return nil, err
	}
	return draftPreviewResult(candidate, current), nil
}

func (s *AutomationDraftService) CurrentCandidate(ctx context.Context, projectID, automationID string) (*models.AutomationDraftResult, error) {
	loaded, err := s.LoadCurrentCandidate(ctx, projectID, automationID)
	if err != nil {
		return nil, err
	}
	return s.PreviewCandidate(ctx, projectID, loaded.Candidate, loaded.Definition)
}

func (s *AutomationDraftService) hydratePersistedScheduleContext(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate, current *models.AutomationDefinition) (models.AutomationDraftCandidate, error) {
	scheduleByNode := make(map[string]string)
	for _, resource := range current.Resources {
		if resource.ResourceType == "schedule" {
			scheduleByNode[resource.NodeKey] = resource.ResourceID
		}
	}
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		if node.Config == nil {
			node.Config = map[string]any{}
		}
		if _, present := node.Config["clear_context_on_start"]; present {
			continue
		}
		scheduleID := scheduleByNode[node.Key]
		if scheduleID == "" {
			continue
		}
		var clearContextOnStart bool
		if err := s.repo.DB().QueryRowContext(ctx, `SELECT s.clear_context_on_start
			FROM schedules s JOIN tasks t ON t.id = s.task_id
			WHERE s.id = ? AND t.project_id = ?`, scheduleID, projectID).Scan(&clearContextOnStart); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return candidate, fmt.Errorf("load saved schedule context for node %q: %w", node.Key, err)
		}
		node.Config["clear_context_on_start"] = clearContextOnStart
	}
	return candidate, nil
}

func (s *AutomationDraftService) candidateFromDefinition(ctx context.Context, projectID, automationID string, current *models.AutomationDefinition) (models.AutomationDraftCandidate, error) {
	var candidate models.AutomationDraftCandidate
	currentMetadata, err := s.repo.GetAutomationGraphMetadata(ctx, projectID, automationID, current.Version.ID)
	if err != nil {
		return candidate, err
	}
	if currentMetadata != nil {
		return currentMetadata.Candidate()
	}
	candidate = models.AutomationDraftCandidate{SchemaVersion: automationDraftSchemaVersion,
		Name: current.Automation.Name, Description: current.Automation.Description,
		AutomationType: current.Automation.AutomationType, AdapterKey: current.Version.AdapterKey}
	nodeKeys := make(map[string]string, len(current.Nodes))
	for _, node := range current.Nodes {
		var config map[string]any
		if err := json.Unmarshal([]byte(node.ConfigJSON), &config); err != nil {
			return candidate, err
		}
		if config == nil {
			config = map[string]any{}
		}
		nodeKeys[node.ID] = node.NodeKey
		candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: node.NodeKey, Name: node.Name,
			Type: node.NodeType, Role: node.Role, Config: config,
			Position: &models.AutomationDraftPoint{X: node.PositionX, Y: node.PositionY}})
	}
	for _, edge := range current.Edges {
		var condition map[string]any
		if err := json.Unmarshal([]byte(edge.ConditionJSON), &condition); err != nil {
			return candidate, err
		}
		candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{Key: edge.EdgeKey,
			From: nodeKeys[edge.SourceNodeID], To: nodeKeys[edge.TargetNodeID], Label: edge.Label, Condition: condition})
	}
	return candidate, nil
}

func automationDraftSummary(candidate models.AutomationDraftCandidate) string {
	names := make([]string, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		names = append(names, node.Name)
	}
	return strings.Join(names, " -> ")
}
