package anthropicclient

import (
	"encoding/json"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

// Disable strict without rewriting the original input schemas.
func prepareStrictTools(tools []ToolDefinition) []ToolDefinition {
	out := append([]ToolDefinition(nil), tools...)
	for i := range out {
		strict := false
		out[i].Strict = &strict
	}
	return out
}

func normalizeToolArguments(opts *AgenticOptions, name string, input json.RawMessage) (json.RawMessage, error) {
	defs := opts.ExtraTools
	if !opts.SkipDefaultTools {
		defs = append(DefaultTools(), defs...)
	}
	for _, def := range defs {
		if (def.Name == name || def.Name == "shell" && (name == "bash" || name == "Bash")) && len(def.ParameterSchema()) > 0 {
			return toolschema.ValidateArguments(def.Schema, def.InputSchema, input)
		}
	}
	return input, nil
}
