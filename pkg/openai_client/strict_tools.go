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
		t := &out[i]
		if t.Type == "namespace" {
			t.Tools = prepareStrictTools(t.Tools)
			continue
		}
		if t.Type != "function" || (t.Strict != nil && !*t.Strict) {
			continue
		}
		schema, _, err := toolschema.Strict(t.Parameters, false)
		strict := err == nil
		t.Strict = &strict
		if strict {
			t.Parameters = schema
		}
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
		if def.Name == name && len(def.Parameters) > 0 {
			return toolschema.NormalizeValidate(def.Parameters, input)
		}
	}
	// Provider-native tools and custom executors without advertised definitions
	// retain their own validation contracts.
	return input, nil
}
