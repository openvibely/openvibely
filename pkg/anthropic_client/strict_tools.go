package anthropicclient

import (
	"encoding/json"
	"sort"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

// Request-wide limits, including optional fields retained by this serializer.
// https://platform.claude.com/docs/en/build-with-claude/structured-outputs
const maxStrictTools = 20
const maxStrictOptional = 24

func prepareStrictTools(tools []ToolDefinition) []ToolDefinition {
	out := append([]ToolDefinition(nil), tools...)
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := out[order[i]], out[order[j]]
		if pa, pb := strictToolPriority(a), strictToolPriority(b); pa != pb {
			return pa > pb
		}
		return a.Name < b.Name
	})
	count, optional := 0, 0
	for _, i := range order {
		t := &out[i]
		disabled := t.Strict != nil && !*t.Strict
		strict := false
		t.Strict = &strict
		if disabled || count >= maxStrictTools {
			continue
		}
		schema, cost, err := toolschema.MarshalAnthropic(t.Schema, t.InputSchema)
		if err != nil || optional+cost.Optional > maxStrictOptional {
			continue
		}
		strict = true
		t.InputSchema = schema
		t.Schema = nil
		count++
		optional += cost.Optional
	}
	return out
}

func normalizeToolArguments(opts *AgenticOptions, name string, input json.RawMessage) (json.RawMessage, error) {
	defs := opts.ExtraTools
	if !opts.SkipDefaultTools {
		defs = append(DefaultTools(), defs...)
	}
	for _, def := range defs {
		if def.Name == name && len(def.ParameterSchema()) > 0 {
			return toolschema.ValidateArguments(def.Schema, def.InputSchema, input)
		}
	}
	return input, nil
}
