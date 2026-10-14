package openaiclient

import (
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

// prepareStrictTools copies definitions so request adaptation never changes the
// shared schemas used by handlers, other providers, or a later steering refresh.
func prepareStrictTools(tools []ToolDefinition) []ToolDefinition {
	out := append([]ToolDefinition(nil), tools...)
	for i := range out {
		if out[i].Type == "namespace" {
			out[i].Tools = prepareStrictTools(out[i].Tools)
			continue
		}
		if out[i].Type != "function" {
			continue
		}
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
	return normalizeDefinedArguments(defs, name, input)
}

func normalizeDefinedArguments(defs []ToolDefinition, name string, input json.RawMessage) (json.RawMessage, error) {
	for _, def := range defs {
		if def.Type == "namespace" {
			if childName, ok := strings.CutPrefix(name, def.Name+"."); ok {
				return normalizeDefinedArguments(def.Tools, childName, input)
			}
			continue
		}
		if (def.Name == name || def.Name == "shell" && (name == "bash" || name == "Bash")) && len(def.ParameterSchema()) > 0 {
			return toolschema.ValidateArguments(def.Schema, def.Parameters, input)
		}
	}
	// Provider-native tools and custom executors without advertised definitions
	// retain their own validation contracts.
	return input, nil
}
