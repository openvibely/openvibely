package jsonview

import (
	"bytes"
	"encoding/json"
)

// ProjectObject decodes metadata but borrows selected immutable values. A caller
// must explicitly select only fields whose consumers accept raw JSON.
func ProjectObject(raw json.RawMessage, keep func(map[string]json.RawMessage, string) bool) (map[string]any, error) {
	fields, err := Fields(raw)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if keep(fields, k) {
			view, err := Borrow(v)
			if err != nil {
				return nil, err
			}
			// Preserve the decoded-text budgeting convention for noncanonical
			// escapes supplied by legacy/foreign producers.
			if trimmed := bytes.TrimSpace(v); len(trimmed) > 0 && trimmed[0] == '"' {
				canonical, _ := view.StringSize()
				encoded, _ := view.Size()
				if canonical != encoded {
					text, err := view.Text()
					if err != nil {
						return nil, err
					}
					out[k] = text
					continue
				}
			}
			out[k] = view
			continue
		}
		var value any
		if err := json.Unmarshal(v, &value); err != nil {
			return nil, err
		}
		out[k] = value
	}
	return out, nil
}

// ToolOutput keeps only textual Responses tool results raw. Message, image,
// reasoning and tool-call structure remains decoded for provider rules.
func ToolOutput(raw json.RawMessage) (any, error) {
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '{' {
		var v any
		err := json.Unmarshal(raw, &v)
		return v, err
	}
	return ProjectObject(raw, func(fields map[string]json.RawMessage, key string) bool {
		if key != "output" {
			return false
		}
		var typ string
		_ = json.Unmarshal(fields["type"], &typ)
		v := bytes.TrimSpace(fields[key])
		return typ == "function_call_output" && len(v) > 0 && v[0] == '"'
	})
}

// ToolOutputs prepares immutable Responses rows while preserving tool text.
func ToolOutputs(rows []json.RawMessage) ([]any, error) {
	var items []any
	for _, raw := range rows {
		item, err := ToolOutput(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}
