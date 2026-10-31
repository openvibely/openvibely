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
			value, err := view.CanonicalTextValue()
			if err != nil {
				return nil, err
			}
			out[k] = value
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

// CanonicalTextValue borrows ordinary text but decodes unusual escape spellings
// when retaining them would change the budgeting convention of decoded history.
func (v Value) CanonicalTextValue() (any, error) {
	if trimmed := bytes.TrimSpace(v.Raw()); len(trimmed) > 0 && trimmed[0] == '"' {
		canonical, err := v.StringSize()
		if err != nil {
			return nil, err
		}
		encoded, err := v.Size()
		if err != nil {
			return nil, err
		}
		if canonical != encoded {
			return v.Text()
		}
	}
	return v, nil
}
