package anthropicclient

import (
	"encoding/json"

	"github.com/openvibely/openvibely/pkg/toolschema"
)

// All converted fields are required, so only tool and union budgets apply.
// https://platform.claude.com/docs/en/build-with-claude/structured-outputs
const maxStrictTools = 20
const maxStrictUnions = 16

func prepareStrictTools(tools []ToolDefinition) []ToolDefinition {
	out := append([]ToolDefinition(nil), tools...)
	order := make([]int, 0, len(out))
	// Reserve capacity for task lookup before the larger orchestration tools.
	for i := range out {
		if out[i].Name == "list_tasks" {
			order = append(order, i)
		}
	}
	for i := range out {
		if out[i].Name != "list_tasks" {
			order = append(order, i)
		}
	}
	count, unions := 0, 0
	for _, i := range order {
		t := &out[i]
		disabled := t.Strict != nil && !*t.Strict
		strict := false
		t.Strict = &strict
		if disabled || count >= maxStrictTools {
			continue
		}
		schema, cost, err := toolschema.Strict(t.InputSchema, true)
		if err != nil || unions+cost > maxStrictUnions {
			continue
		}
		strict = true
		t.InputSchema = schema
		count++
		unions += cost
	}
	return out
}

func normalizeToolArguments(opts *AgenticOptions, name string, input json.RawMessage) (json.RawMessage, error) {
	defs := opts.ExtraTools
	if !opts.SkipDefaultTools {
		defs = append(DefaultTools(), defs...)
	}
	for _, def := range defs {
		if def.Name == name && len(def.InputSchema) > 0 {
			return toolschema.NormalizeValidate(def.InputSchema, input)
		}
	}
	return input, nil
}
