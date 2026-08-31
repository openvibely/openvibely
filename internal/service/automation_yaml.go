package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/openvibely/openvibely/internal/models"
	"gopkg.in/yaml.v3"
)

// DecodeAutomationDraftYAML parses the canonical Automation YAML document into
// the existing graph candidate. The candidate remains the only compiler input.
func DecodeAutomationDraftYAML(raw []byte) (models.AutomationDraftCandidate, error) {
	if len(raw) == 0 || len(raw) > maxAutomationDraftBytes {
		return models.AutomationDraftCandidate{}, errors.New("automation YAML must be between 1 byte and 64 KiB")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return models.AutomationDraftCandidate{}, fmt.Errorf("invalid automation YAML: %w", err)
	}
	if err := validateAutomationYAMLNode(&document); err != nil {
		return models.AutomationDraftCandidate{}, err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return models.AutomationDraftCandidate{}, errors.New("automation YAML contains multiple documents")
		}
		return models.AutomationDraftCandidate{}, fmt.Errorf("invalid automation YAML: %w", err)
	}

	var decoded automationYAMLCandidate
	strict := yaml.NewDecoder(bytes.NewReader(raw))
	strict.KnownFields(true)
	if err := strict.Decode(&decoded); err != nil {
		return models.AutomationDraftCandidate{}, fmt.Errorf("invalid automation YAML: %w", err)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return models.AutomationDraftCandidate{}, fmt.Errorf("invalid automation YAML: %w", err)
	}
	candidate, err := DecodeAutomationDraftCandidate(encoded)
	if err != nil {
		return models.AutomationDraftCandidate{}, err
	}
	return normalizeAutomationDraftYAMLNumbers(candidate), nil
}

// EncodeAutomationDraftYAML produces the stable, editable YAML representation
// of a graph candidate. It deliberately does not persist or mutate the graph.
func EncodeAutomationDraftYAML(candidate models.AutomationDraftCandidate) (string, error) {
	encoded, err := yaml.Marshal(automationYAMLCandidateFromCandidate(candidate))
	if err != nil {
		return "", fmt.Errorf("encode automation YAML: %w", err)
	}
	return string(encoded), nil
}

type automationYAMLCandidate struct {
	SchemaVersion  int                  `yaml:"schema_version" json:"schema_version"`
	Name           string               `yaml:"name" json:"name"`
	Description    string               `yaml:"description" json:"description"`
	AutomationType string               `yaml:"automation_type" json:"automation_type"`
	AdapterKey     string               `yaml:"adapter_key" json:"adapter_key"`
	Nodes          []automationYAMLNode `yaml:"nodes" json:"nodes"`
	Edges          []automationYAMLEdge `yaml:"edges" json:"edges"`
	Assumptions    []string             `yaml:"assumptions,omitempty" json:"assumptions,omitempty"`
	Warnings       []string             `yaml:"warnings,omitempty" json:"warnings,omitempty"`
}

type automationYAMLNode struct {
	Key      string                       `yaml:"key" json:"key"`
	Name     string                       `yaml:"name" json:"name"`
	Type     models.AutomationNodeType    `yaml:"type" json:"type"`
	Role     string                       `yaml:"role" json:"role"`
	Config   map[string]any               `yaml:"config" json:"config"`
	Position *models.AutomationDraftPoint `yaml:"position,omitempty" json:"position,omitempty"`
}

type automationYAMLEdge struct {
	Key       string         `yaml:"key" json:"key"`
	From      string         `yaml:"from" json:"from"`
	To        string         `yaml:"to" json:"to"`
	FromPort  string         `yaml:"from_port,omitempty" json:"from_port,omitempty"`
	ToPort    string         `yaml:"to_port,omitempty" json:"to_port,omitempty"`
	Label     string         `yaml:"label,omitempty" json:"label,omitempty"`
	Condition map[string]any `yaml:"condition,omitempty" json:"condition,omitempty"`
}

func automationYAMLCandidateFromCandidate(candidate models.AutomationDraftCandidate) automationYAMLCandidate {
	document := automationYAMLCandidate{
		SchemaVersion: candidate.SchemaVersion, Name: candidate.Name, Description: candidate.Description,
		AutomationType: candidate.AutomationType, AdapterKey: candidate.AdapterKey,
		Assumptions: candidate.Assumptions, Warnings: candidate.Warnings,
	}
	for _, node := range candidate.Nodes {
		config, _ := normalizeAutomationYAMLValue(node.Config).(map[string]any)
		if config == nil {
			config = map[string]any{}
		}
		document.Nodes = append(document.Nodes, automationYAMLNode{Key: node.Key, Name: node.Name, Type: node.Type, Role: node.Role, Config: config, Position: node.Position})
	}
	for _, edge := range candidate.Edges {
		condition, _ := normalizeAutomationYAMLValue(edge.Condition).(map[string]any)
		if condition == nil {
			condition = map[string]any{}
		}
		document.Edges = append(document.Edges, automationYAMLEdge{Key: edge.Key, From: edge.From, To: edge.To, FromPort: edge.FromPort, ToPort: edge.ToPort, Label: edge.Label, Condition: condition})
	}
	return document
}

func normalizeAutomationDraftYAMLNumbers(candidate models.AutomationDraftCandidate) models.AutomationDraftCandidate {
	for i := range candidate.Nodes {
		candidate.Nodes[i].Config, _ = normalizeAutomationYAMLValue(candidate.Nodes[i].Config).(map[string]any)
		if candidate.Nodes[i].Config == nil {
			candidate.Nodes[i].Config = map[string]any{}
		}
	}
	for i := range candidate.Edges {
		candidate.Edges[i].Condition, _ = normalizeAutomationYAMLValue(candidate.Edges[i].Condition).(map[string]any)
		if candidate.Edges[i].Condition == nil {
			candidate.Edges[i].Condition = map[string]any{}
		}
	}
	return candidate
}

func normalizeAutomationYAMLValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := strconv.Atoi(string(typed)); err == nil {
			return integer
		}
		if integer, err := typed.Int64(); err == nil {
			return integer
		}
		if decimal, err := strconv.ParseFloat(string(typed), 64); err == nil {
			return decimal
		}
		return string(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = normalizeAutomationYAMLValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, item := range typed {
			out[index] = normalizeAutomationYAMLValue(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	default:
		return value
	}
}

func validateAutomationYAMLNode(node *yaml.Node) error {
	if node == nil {
		return errors.New("invalid automation YAML")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("automation YAML aliases and anchors are not supported")
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) != 1 {
			return errors.New("automation YAML must contain one document")
		}
		return validateAutomationYAMLNode(node.Content[0])
	case yaml.MappingNode:
		seen := make(map[string]bool, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("automation YAML mapping keys must be strings")
			}
			if seen[key.Value] {
				return fmt.Errorf("automation YAML contains duplicate key %q", key.Value)
			}
			seen[key.Value] = true
			if err := validateAutomationYAMLNode(node.Content[i+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateAutomationYAMLNode(child); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!int", "!!float", "!!bool", "!!null":
		default:
			return fmt.Errorf("automation YAML uses unsupported value type %q", node.Tag)
		}
	default:
		return errors.New("automation YAML contains an unsupported structure")
	}
	return nil
}
