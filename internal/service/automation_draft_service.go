package service

import (
	"bytes"
	"context"
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

type AutomationDraftService struct {
	repo         *repository.AutomationRepo
	registry     *AutomationAdapterRegistry
	capabilities *AutomationCapabilitySnapshotBuilder
}

type AutomationDraftCreateRequest struct {
	ProjectID  string
	Source     string
	CreatedVia string
	StableKey  string
	Candidate  models.AutomationDraftCandidate
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
		return models.AutomationDraftCandidate{}, errors.New("automation draft candidate must be between 1 byte and 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var candidate models.AutomationDraftCandidate
	if err := decoder.Decode(&candidate); err != nil {
		return candidate, fmt.Errorf("invalid automation draft candidate: %w", err)
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
		return fmt.Errorf("invalid automation draft candidate: %w", err)
	}
	return errors.New("automation draft candidate contains trailing JSON")
}

func (s *AutomationDraftService) TemplateCandidate(adapterKey string) (models.AutomationDraftCandidate, error) {
	adapter, ok := s.registry.Get(strings.TrimSpace(adapterKey))
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
	for _, node := range adapter.Nodes {
		config := map[string]any{}
		if node.AllowedResources["task"] {
			config["prompt"] = defaultAutomationNodePrompt(adapter.Key, node.Role)
			config["category"] = string(models.CategoryBacklog)
			config["priority"] = 2
		}
		if node.AllowedResources["schedule"] {
			config["target_node_key"] = adapterScheduleTarget(adapter, node.Key)
			config["run_at"] = "09:00"
			config["repeat_type"] = string(models.RepeatDaily)
			config["repeat_interval"] = 1
			config["enabled"] = true
			if strings.Contains(node.Key, "inbox") {
				config["repeat_type"] = string(models.RepeatHours)
				config["repeat_interval"] = 1
			}
		}
		candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{
			Key: node.Key, Name: node.Name, Type: models.AutomationNodeType(node.Type), Role: node.Role,
			Config: config, Position: &models.AutomationDraftPoint{X: node.X, Y: node.Y},
		})
	}
	for i := range candidate.Nodes {
		if !adapterNodeAccepts(adapter, candidate.Nodes[i].Key, "schedule") {
			continue
		}
		target := adapterScheduleTarget(adapter, candidate.Nodes[i].Key)
		for j := range candidate.Nodes {
			if candidate.Nodes[j].Key == target {
				candidate.Nodes[j].Config["category"] = string(models.CategoryScheduled)
			}
		}
	}
	for _, edge := range adapter.Edges {
		condition := map[string]any{}
		if strings.TrimSpace(edge.Condition) != "" {
			_ = json.Unmarshal([]byte(edge.Condition), &condition)
		}
		candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{Key: edge.Key, From: edge.From, To: edge.To, Label: edge.Label, Condition: condition})
	}
	return candidate, nil
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
	candidate.AutomationType = adapter.AutomationType
	candidate.AdapterKey = adapter.Key
	adapterNodes := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		adapterNodes[node.Key] = node
	}
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
		for _, field := range []string{"skills", "source_files"} {
			if value, exists := node.Config[field]; exists {
				if values, valid := draftStringSlice(value); valid {
					node.Config[field] = normalizeDraftReferences(values)
				}
			}
		}
		if canonical, exists := adapterNodes[node.Key]; exists {
			if node.Name == "" {
				node.Name = canonical.Name
			}
			if node.Position == nil {
				node.Position = &models.AutomationDraftPoint{X: canonical.X, Y: canonical.Y}
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
	if adapter.DynamicTopology {
		nodeTypes := make(map[string]models.AutomationNodeType, len(candidate.Nodes))
		for _, node := range candidate.Nodes {
			nodeTypes[node.Key] = node.Type
		}
		for i := range candidate.Nodes {
			if candidate.Nodes[i].Type != models.AutomationNodeTrigger {
				continue
			}
			delete(candidate.Nodes[i].Config, "target_node_key")
			for _, edge := range candidate.Edges {
				if edge.From == candidate.Nodes[i].Key && nodeTypes[edge.To] == models.AutomationNodeAgentTask {
					candidate.Nodes[i].Config["target_node_key"] = edge.To
					break
				}
			}
		}
	}
	candidate.Assumptions = normalizeDraftMessages(candidate.Assumptions)
	candidate.Warnings = normalizeDraftMessages(candidate.Warnings)
	return candidate, nil
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
		issues = append(issues, models.AutomationValidationIssue{Code: "schema_version", Message: "Unsupported automation draft schema version."})
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

	canonicalNodes := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		canonicalNodes[node.Key] = node
	}
	seenNodes := map[string]bool{}
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
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_topology", Message: "Custom graph nodes can be saved, but publication requires a registered runtime adapter."})
			issues = append(issues, validateCustomAutomationNodeConfig(node)...)
			continue
		}
		if node.Type != models.AutomationNodeType(canonical.Type) || node.Role != canonical.Role {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_topology", Message: "Node type and role are fixed by the registered adapter."})
		}
		issues = append(issues, validateAutomationNodeConfig(adapter, canonical, node)...)
	}
	for key := range canonicalNodes {
		if !seenNodes[key] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: key, Code: "missing_node", Message: "Add this required node before publication."})
		}
	}

	canonicalEdges := make(map[string]AutomationAdapterEdge, len(adapter.Edges))
	for _, edge := range adapter.Edges {
		canonicalEdges[edge.Key] = edge
	}
	seenEdgeKeys := map[string]bool{}
	seenCanonicalEdges := map[string]bool{}
	for _, edge := range candidate.Edges {
		if edge.Key == "" || seenEdgeKeys[edge.Key] {
			issues = append(issues, models.AutomationValidationIssue{Code: "invalid_edge", Message: "Every graph connection requires a unique key."})
			continue
		}
		seenEdgeKeys[edge.Key] = true
		if !validAutomationDraftPort(edge.FromPort) || !validAutomationDraftPort(edge.ToPort) {
			issues = append(issues, models.AutomationValidationIssue{Code: "invalid_edge", Message: "Graph connection ports must be left or right."})
			continue
		}
		if !seenNodes[edge.From] || !seenNodes[edge.To] || edge.From == edge.To {
			issues = append(issues, models.AutomationValidationIssue{Code: "invalid_edge", Message: "Graph edge references an invalid node."})
			continue
		}
		if adapter.DynamicTopology {
			if len(edge.Condition) != 0 {
				issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_condition", Message: "Custom connection conditions are not supported yet."})
			}
			continue
		}
		canonical, isCanonical := canonicalEdges[edge.Key]
		if !isCanonical || edge.From != canonical.From || edge.To != canonical.To {
			issues = append(issues, models.AutomationValidationIssue{Code: "unsupported_topology", Message: "Custom graph connections can be saved, but publication requires a registered runtime adapter."})
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
	for key := range canonicalEdges {
		if !seenCanonicalEdges[key] {
			issues = append(issues, models.AutomationValidationIssue{Code: "missing_edge", Message: "Add every required transition before publication."})
		}
	}
	if adapter.DynamicTopology {
		issues = append(issues, validateCustomAutomationTopology(candidate)...)
	}
	sortAutomationValidationIssues(issues)
	return issues
}

func validateCustomAutomationTopology(candidate models.AutomationDraftCandidate) []models.AutomationValidationIssue {
	if len(candidate.Nodes) == 0 {
		return []models.AutomationValidationIssue{{Code: "empty_graph", Message: "Add a Schedule and an Agent task to make this custom automation runnable."}}
	}
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	incoming := make(map[string][]models.AutomationDraftEdge, len(candidate.Nodes))
	outgoing := make(map[string][]models.AutomationDraftEdge, len(candidate.Nodes))
	triggerCount, taskCount := 0, 0
	var issues []models.AutomationValidationIssue
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
		switch node.Type {
		case models.AutomationNodeTrigger:
			triggerCount++
		case models.AutomationNodeAgentTask:
			taskCount++
		case models.AutomationNodeOutcome:
		default:
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "unsupported_capability", Message: "This capability is not executable in custom automations yet."})
		}
	}
	for _, edge := range candidate.Edges {
		from, fromOK := nodes[edge.From]
		to, toOK := nodes[edge.To]
		if !fromOK || !toOK || edge.From == edge.To {
			continue
		}
		outgoing[edge.From] = append(outgoing[edge.From], edge)
		incoming[edge.To] = append(incoming[edge.To], edge)
		valid := from.Type == models.AutomationNodeTrigger && to.Type == models.AutomationNodeAgentTask ||
			from.Type == models.AutomationNodeAgentTask && to.Type == models.AutomationNodeOutcome
		if !valid {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: from.Key, Code: "unsupported_handoff", Message: "Custom automations currently support Schedule → Agent task → Outcome handoffs."})
		}
	}
	if triggerCount == 0 {
		issues = append(issues, models.AutomationValidationIssue{Code: "missing_trigger", Message: "Add at least one Schedule to start this custom automation."})
	}
	if taskCount == 0 {
		issues = append(issues, models.AutomationValidationIssue{Code: "missing_task", Message: "Add at least one Agent task for this custom automation to run."})
	}
	for _, node := range candidate.Nodes {
		switch node.Type {
		case models.AutomationNodeTrigger:
			if len(outgoing[node.Key]) != 1 || nodes[outgoing[node.Key][0].To].Type != models.AutomationNodeAgentTask {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "schedule_target", Message: "Connect this Schedule to exactly one Agent task."})
			}
		case models.AutomationNodeAgentTask:
			hasTrigger := false
			for _, edge := range incoming[node.Key] {
				if nodes[edge.From].Type == models.AutomationNodeTrigger {
					hasTrigger = true
					break
				}
			}
			if !hasTrigger {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "task_trigger", Message: "Connect a Schedule to this Agent task so it can run."})
			}
			if len(outgoing[node.Key]) > 1 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "task_outcome", Message: "An Agent task can connect to at most one Outcome."})
			}
		case models.AutomationNodeOutcome:
			if len(outgoing[node.Key]) != 0 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "outcome_terminal", Message: "An Outcome must be the end of a path."})
			}
		}
	}
	return issues
}

func validAutomationDraftPort(port string) bool {
	return port == "" || port == "left" || port == "right"
}

func validateCustomAutomationNodeConfig(node models.AutomationDraftNode) []models.AutomationValidationIssue {
	allowed := map[string]bool{}
	switch node.Type {
	case models.AutomationNodeAgentTask:
		allowed = map[string]bool{"prompt": true, "category": true, "priority": true, "agent_ref": true, "skills": true, "source_files": true}
	case models.AutomationNodeTrigger:
		allowed = map[string]bool{"target_node_key": true, "run_at": true, "repeat_type": true, "repeat_interval": true, "enabled": true}
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
	if node.Type == models.AutomationNodeAgentTask {
		prompt, promptOK := node.Config["prompt"].(string)
		if !promptOK || strings.TrimSpace(prompt) == "" {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "missing_prompt", Message: "Task nodes require a prompt before publication."})
		}
		category, categoryOK := node.Config["category"].(string)
		if !categoryOK || (category != string(models.CategoryBacklog) && category != string(models.CategoryScheduled)) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "Automation task category must be backlog or scheduled."})
		}
		priority, priorityOK := draftInt(node.Config["priority"])
		if !priorityOK || priority < 1 || priority > 4 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "priority", Message: "Task priority must be between 1 and 4."})
		}
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
	}
	if node.Type == models.AutomationNodeTrigger {
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
		if !intervalOK || interval < 1 || interval > 365 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "repeat_interval", Message: "Schedule interval must be between 1 and 365."})
		}
		if _, enabledOK := node.Config["enabled"].(bool); !enabledOK {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "enabled", Message: "Trigger enabled state must be true or false."})
		}
	}
	return issues
}

func validateAutomationNodeConfig(adapter AutomationAdapter, canonical AutomationAdapterNode, node models.AutomationDraftNode) []models.AutomationValidationIssue {
	allowed := map[string]bool{}
	if canonical.AllowedResources["task"] {
		allowed = map[string]bool{"prompt": true, "category": true, "priority": true, "agent_ref": true, "skills": true, "source_files": true}
	}
	if canonical.AllowedResources["schedule"] {
		allowed = map[string]bool{"target_node_key": true, "run_at": true, "repeat_type": true, "repeat_interval": true, "enabled": true}
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
	if canonical.AllowedResources["task"] {
		prompt, promptOK := node.Config["prompt"].(string)
		if !promptOK || strings.TrimSpace(prompt) == "" {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "missing_prompt", Message: "Task nodes require a prompt before publication."})
		}
		category, categoryOK := node.Config["category"].(string)
		if !categoryOK || (category != string(models.CategoryBacklog) && category != string(models.CategoryScheduled)) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "category", Message: "Automation task category must be backlog or scheduled."})
		}
		priority, priorityOK := draftInt(node.Config["priority"])
		if !priorityOK || priority < 1 || priority > 4 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "priority", Message: "Task priority must be between 1 and 4."})
		}
		issues = append(issues, validateAutomationTaskReferenceShape(node)...)
	}
	if canonical.AllowedResources["schedule"] {
		target, targetOK := node.Config["target_node_key"].(string)
		if !targetOK || target == "" || target != adapterScheduleTarget(adapter, node.Key) {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "schedule_target", Message: "Trigger target is fixed by the registered adapter."})
		}
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
		interval, ok := draftInt(node.Config["repeat_interval"])
		if !ok || interval < 1 || interval > 365 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "repeat_interval", Message: "Schedule interval must be between 1 and 365."})
		}
		if _, ok := node.Config["enabled"].(bool); !ok {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "enabled", Message: "Trigger enabled state must be true or false."})
		}
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
	for _, field := range []struct {
		key  string
		code string
		name string
	}{{"skills", "skill_ref", "Skill"}, {"source_files", "source_file", "Source file"}} {
		value, exists := node.Config[field.key]
		if !exists {
			continue
		}
		values, ok := draftStringSlice(value)
		if !ok || len(values) > 20 {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: field.code, Message: field.name + " selection must be a bounded list of supported references."})
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" || len(value) > 240 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: field.code, Message: field.name + " selection contains an unsupported reference."})
				break
			}
		}
	}
	return issues
}

func (s *AutomationDraftService) ValidateCandidateWithCapabilities(candidate models.AutomationDraftCandidate, snapshot models.AutomationCapabilitySnapshot) []models.AutomationValidationIssue {
	issues := s.ValidateCandidate(candidate)
	agents := make(map[string]bool, len(snapshot.Agents))
	for _, agent := range snapshot.Agents {
		agents[agent.ID] = true
	}
	skills := make(map[string]bool, len(snapshot.Skills))
	for _, skill := range snapshot.Skills {
		skills[skill.ID] = true
	}
	sourceFiles := make(map[string]bool, len(snapshot.SourceFiles))
	for _, sourceFile := range snapshot.SourceFiles {
		sourceFiles[sourceFile] = true
	}
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask {
			continue
		}
		agentRef, _ := node.Config["agent_ref"].(string)
		agentRef = strings.TrimSpace(agentRef)
		if agentRef != "" && !agents[agentRef] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection is unavailable in this project."})
		}
		if selected, ok := draftStringSlice(node.Config["skills"]); ok {
			for _, skill := range normalizeDraftReferences(selected) {
				if !skills[skill] || agentRef == "" || !strings.HasPrefix(skill, agentRef+":") {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "skill_ref", Message: "Skill selection is unavailable for the selected Agent in this project."})
				}
			}
		}
		if selected, ok := draftStringSlice(node.Config["source_files"]); ok {
			for _, sourceFile := range normalizeDraftReferences(selected) {
				if !sourceFiles[sourceFile] {
					issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "source_file", Message: "Source file selection is unavailable in this project."})
				}
			}
		}
	}
	sortAutomationValidationIssues(issues)
	return issues
}

func (s *AutomationDraftService) validateCandidateForProject(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]models.AutomationValidationIssue, error) {
	if s.capabilities == nil {
		issues := s.ValidateCandidate(candidate)
		for _, node := range candidate.Nodes {
			if node.Type != models.AutomationNodeAgentTask {
				continue
			}
			if ref, _ := node.Config["agent_ref"].(string); strings.TrimSpace(ref) != "" {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection cannot be resolved because project capabilities are unavailable."})
			}
			if values, ok := draftStringSlice(node.Config["skills"]); ok && len(normalizeDraftReferences(values)) > 0 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "skill_ref", Message: "Skill selection cannot be resolved because project capabilities are unavailable."})
			}
			if values, ok := draftStringSlice(node.Config["source_files"]); ok && len(normalizeDraftReferences(values)) > 0 {
				issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "source_file", Message: "Source file selection cannot be resolved because project capabilities are unavailable."})
			}
		}
		sortAutomationValidationIssues(issues)
		return issues, nil
	}
	snapshot, err := s.capabilities.Build(ctx, projectID)
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

func defaultAutomationNodePrompt(adapterKey, role string) string {
	return fmt.Sprintf("Run the %s role for this %s automation using the existing project-scoped tools and human review boundaries.", strings.ReplaceAll(role, "_", " "), strings.ReplaceAll(adapterKey, "_", " "))
}

func (s *AutomationDraftService) CreateDraft(ctx context.Context, request AutomationDraftCreateRequest) (*models.AutomationDraftResult, error) {
	if s.repo == nil {
		return nil, errors.New("automation repository is unavailable")
	}
	if strings.TrimSpace(request.ProjectID) == "" {
		return nil, errors.New("project is required")
	}
	candidate, err := s.NormalizeCandidate(request.Candidate)
	if err != nil {
		return nil, err
	}
	issues, err := s.validateCandidateForProject(ctx, request.ProjectID, candidate)
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if automationDraftIssuePreventsPersistence(issue.Code) {
			return nil, fmt.Errorf("automation draft validation failed: %s", issue.Message)
		}
	}
	definition, err := s.repo.CreateAutomationDraft(ctx, repository.AutomationDraftWrite{
		ProjectID: request.ProjectID, StableKey: request.StableKey, Source: request.Source,
		CreatedVia: request.CreatedVia, Candidate: candidate, ValidationErrors: issues,
	})
	if err != nil {
		return nil, err
	}
	automationobs.Event("automation.draft.created",
		automationobs.String("project_id", request.ProjectID),
		automationobs.String("automation_id", definition.Automation.ID),
		automationobs.String("version_id", definition.Version.ID),
		automationobs.String("adapter_key", candidate.AdapterKey))
	return &models.AutomationDraftResult{
		Definition: definition, Candidate: candidate, Assumptions: candidate.Assumptions,
		Warnings: candidate.Warnings, ValidationErrors: issues,
		Summary: automationDraftSummary(candidate),
		URL:     fmt.Sprintf("/automations/%s?project_id=%s&view=definition&version=%s", definition.Automation.ID, request.ProjectID, definition.Version.ID),
	}, nil
}

func (s *AutomationDraftService) GetCurrentDraft(ctx context.Context, projectID, automationID string) (*models.AutomationDraftResult, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("automation repository is unavailable")
	}
	metadata, err := s.repo.GetLatestAutomationDraftMetadata(ctx, projectID, automationID)
	if err != nil || metadata == nil {
		return nil, err
	}
	return s.GetDraft(ctx, projectID, automationID, metadata.VersionID)
}

func (s *AutomationDraftService) GetDraft(ctx context.Context, projectID, automationID, versionID string) (*models.AutomationDraftResult, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("automation repository is unavailable")
	}
	definition, err := s.repo.GetDefinitionVersion(ctx, projectID, automationID, versionID)
	if err != nil || definition == nil {
		return nil, err
	}
	metadata, err := s.repo.GetAutomationDraftMetadata(ctx, projectID, automationID, versionID)
	if err != nil || metadata == nil {
		return nil, err
	}
	candidate, err := metadata.Candidate()
	if err != nil {
		return nil, err
	}
	candidate, err = s.NormalizeCandidate(candidate)
	if err != nil {
		return nil, err
	}
	currentIssues, err := s.validateCandidateForProject(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	for _, issue := range currentIssues {
		if automationDraftIssuePreventsPersistence(issue.Code) {
			return nil, fmt.Errorf("automation draft validation failed: %s", issue.Message)
		}
	}
	result := draftPreviewResult(candidate, definition)
	result.Assumptions = metadata.Assumptions
	result.Warnings = metadata.Warnings
	result.ValidationErrors = currentIssues
	result.URL = fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, projectID)
	return result, nil
}

func (s *AutomationDraftService) UpdateDraft(ctx context.Context, automationID, versionID, projectID string, candidate models.AutomationDraftCandidate) (*models.AutomationDraftResult, error) {
	if s.repo == nil {
		return nil, errors.New("automation repository is unavailable")
	}
	candidate, err := s.NormalizeCandidate(candidate)
	if err != nil {
		return nil, err
	}
	issues, err := s.validateCandidateForProject(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if automationDraftIssuePreventsPersistence(issue.Code) {
			return nil, fmt.Errorf("automation draft validation failed: %s", issue.Message)
		}
	}
	definition, err := s.repo.ReplaceAutomationDraft(ctx, repository.AutomationDraftWrite{
		ProjectID: projectID, AutomationID: automationID, VersionID: versionID,
		Candidate: candidate, ValidationErrors: issues,
	})
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, errors.New("automation draft not found")
	}
	automationobs.Event("automation.draft.updated",
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", versionID), automationobs.String("adapter_key", candidate.AdapterKey))
	result := draftPreviewResult(candidate, definition)
	result.ValidationErrors = issues
	result.URL = fmt.Sprintf("/automations/%s?project_id=%s&view=definition&version=%s", automationID, projectID, versionID)
	return result, nil
}

func automationDraftIssuePreventsPersistence(code string) bool {
	switch code {
	case "unsupported_adapter", "schema_version", "graph_size", "invalid_json", "invalid_node", "invalid_edge", "unsupported_condition", "unknown_config", "unsafe_config":
		return true
	default:
		return false
	}
}

func (s *AutomationDraftService) ClonePublishedVersion(ctx context.Context, projectID, automationID string) (*models.AutomationDraftResult, error) {
	if s == nil || s.repo == nil {
		return nil, errors.New("automation repository is unavailable")
	}
	existing, err := s.repo.GetLatestAutomationDraftMetadata(ctx, projectID, automationID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return s.GetDraft(ctx, projectID, automationID, existing.VersionID)
	}
	published, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil {
		return nil, err
	}
	if published == nil || published.Version.State != models.AutomationVersionPublished {
		return nil, errors.New("published automation not found")
	}
	var candidate models.AutomationDraftCandidate
	publishedMetadata, err := s.repo.GetAutomationDraftMetadata(ctx, projectID, automationID, published.Version.ID)
	if err != nil {
		return nil, err
	}
	if publishedMetadata != nil {
		candidate, err = publishedMetadata.Candidate()
		if err != nil {
			return nil, err
		}
	} else {
		candidate = models.AutomationDraftCandidate{SchemaVersion: automationDraftSchemaVersion,
			Name: published.Automation.Name, Description: published.Automation.Description,
			AutomationType: published.Automation.AutomationType, AdapterKey: published.Version.AdapterKey}
		nodeKeys := make(map[string]string, len(published.Nodes))
		for _, node := range published.Nodes {
			var config map[string]any
			if err := json.Unmarshal([]byte(node.ConfigJSON), &config); err != nil {
				return nil, err
			}
			if config == nil {
				config = map[string]any{}
			}
			nodeKeys[node.ID] = node.NodeKey
			candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: node.NodeKey, Name: node.Name,
				Type: node.NodeType, Role: node.Role, Config: config,
				Position: &models.AutomationDraftPoint{X: node.PositionX, Y: node.PositionY}})
		}
		for _, edge := range published.Edges {
			var condition map[string]any
			if err := json.Unmarshal([]byte(edge.ConditionJSON), &condition); err != nil {
				return nil, err
			}
			candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{Key: edge.EdgeKey,
				From: nodeKeys[edge.SourceNodeID], To: nodeKeys[edge.TargetNodeID], Label: edge.Label, Condition: condition})
		}
	}
	candidate, err = s.NormalizeCandidate(candidate)
	if err != nil {
		return nil, err
	}
	issues, err := s.validateCandidateForProject(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		if automationDraftIssuePreventsPersistence(issue.Code) {
			return nil, fmt.Errorf("automation draft validation failed: %s", issue.Message)
		}
	}
	definition, err := s.repo.CreateAutomationDraftVersion(ctx, repository.AutomationDraftWrite{ProjectID: projectID,
		AutomationID: automationID, Source: "manual", Candidate: candidate, ValidationErrors: issues})
	if err != nil {
		return nil, err
	}
	result := draftPreviewResult(candidate, definition)
	result.ValidationErrors = issues
	result.URL = fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, definition.Version.ID, projectID)
	return result, nil
}

func automationDraftSummary(candidate models.AutomationDraftCandidate) string {
	names := make([]string, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		names = append(names, node.Name)
	}
	return strings.Join(names, " -> ")
}
