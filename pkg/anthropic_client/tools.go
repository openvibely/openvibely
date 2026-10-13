package anthropicclient

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
	Strict         *bool              `json:"strict,omitempty"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	InputSchema    json.RawMessage    `json:"input_schema"`
}

// ToolCall records a single tool invocation and its result.
type ToolCall struct {
	Name   string                 `json:"name"`
	Input  map[string]interface{} `json:"input"`
	Output string                 `json:"output"`
	Error  bool                   `json:"error"`
}

// DefaultTools returns the standard set of tool definitions for agentic use.
func DefaultTools() []ToolDefinition {
	var out []ToolDefinition
	for _, d := range agenttools.Definitions(agenttools.BashPolicy{DefaultTimeoutSeconds: defaultExecBashTimeoutSeconds}) {
		out = append(out, ToolDefinition{Name: d.Name, Description: d.Description, Schema: d.Schema})
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

const defaultExecBashTimeoutSeconds = 600

// ExecuteTool runs a tool locally and returns the output string.
// It resolves relative paths against workDir.
func ExecuteTool(ctx context.Context, workDir, name string, input json.RawMessage) (string, error) {
	return agenttools.Execute(ctx, workDir, name, input, agenttools.BashPolicy{
		DefaultTimeoutSeconds: defaultExecBashTimeoutSeconds,
	})
}

func (t ToolDefinition) ParameterSchema() json.RawMessage {
	return toolschema.JSON(t.Schema, t.InputSchema)
}

func (t ToolDefinition) MarshalJSON() ([]byte, error) {
	type wire ToolDefinition
	v := wire(t)
	v.InputSchema = t.ParameterSchema()
	return json.Marshal(v)
}
