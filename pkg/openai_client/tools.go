package openaiclient

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

type ToolDefinition struct {
	Schema         *toolschema.Schema `json:"-"`
	StrictPriority int                `json:"-"`
	Type           string             `json:"type"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Parameters     json.RawMessage    `json:"parameters,omitempty"`
	Tools          []ToolDefinition   `json:"tools,omitempty"`
	Async          bool               `json:"async,omitempty"`
	Strict         *bool              `json:"strict,omitempty"`
}

// ToolCall records a single tool invocation and its result.
type ToolCall struct {
	Name   string                 `json:"name"`
	Input  map[string]interface{} `json:"input"`
	Output string                 `json:"output"`
	Error  bool                   `json:"error"`
}

// DefaultTools returns the standard set of tool definitions for agentic use
// in OpenAI's function calling format.
func DefaultTools() []ToolDefinition {
	var out []ToolDefinition
	for _, d := range agenttools.Definitions(agenttools.BashPolicy{DefaultTimeoutSeconds: 120, MaxTimeoutSeconds: 600}) {
		out = append(out, ToolDefinition{
			Type: "function", Name: d.Name, Description: d.Description, Schema: d.Schema})
	}
	return out
}

func filterToolDefinitions(tools []ToolDefinition, filter func(string) bool) []ToolDefinition {
	if filter == nil || len(tools) == 0 {
		return tools
	}
	out := tools[:0]
	for _, tool := range tools {
		if filter(strings.TrimSpace(tool.Name)) {
			out = append(out, tool)
		}
	}
	return out
}

// ExecuteTool runs a tool locally and returns the output string.
// It resolves relative paths against workDir.
func ExecuteTool(ctx context.Context, workDir, name string, input json.RawMessage) (string, error) {
	return agenttools.Execute(ctx, workDir, name, input, agenttools.BashPolicy{
		DefaultTimeoutSeconds: 120,
		MaxTimeoutSeconds:     600,
	})
}

func (t ToolDefinition) ParameterSchema() json.RawMessage {
	return toolschema.JSON(t.Schema, t.Parameters)
}

func (t ToolDefinition) MarshalJSON() ([]byte, error) {
	type wire ToolDefinition
	v := wire(t)
	v.Parameters = t.ParameterSchema()
	return json.Marshal(v)
}
