package jsonview

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"
)

// ProjectObject decodes metadata but borrows selected immutable values. A caller
// must explicitly select only fields whose consumers accept raw JSON.
func ProjectObject(raw json.RawMessage, keep func(map[string]json.RawMessage, string) bool) (map[string]any, error) {
	fields, err := Fields(raw)
	if err != nil {
		return nil, err
	}
	return projectFields(fields, keep)
}

func projectFields(fields map[string]json.RawMessage, keep func(map[string]json.RawMessage, string) bool) (map[string]any, error) {
	out := make(map[string]any, len(fields))
	for k, v := range fields {
		if keep(fields, k) {
			view, err := borrowProjectedValue(v)
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

// ToolOutput borrows tool results, message text, and image URLs. Message
// structure, reasoning and tool calls remain decoded for provider rules. Scalar
// message content stays decoded for application-context matching.
func ToolOutput(raw json.RawMessage) (any, error) {
	if raw = bytes.TrimSpace(raw); len(raw) == 0 || raw[0] != '{' {
		var v any
		err := json.Unmarshal(raw, &v)
		return v, err
	}
	fields, err := Fields(raw)
	if err != nil {
		return nil, err
	}
	var typ string
	_ = json.Unmarshal(fields["type"], &typ)
	content := bytes.TrimSpace(fields["content"])
	if (typ == "message" || typ == "") && len(content) > 0 && content[0] == '[' {
		return projectMessage(fields, content)
	}
	return projectFields(fields, func(fields map[string]json.RawMessage, key string) bool {
		if key != "output" {
			return false
		}
		var typ string
		_ = json.Unmarshal(fields["type"], &typ)
		v := bytes.TrimSpace(fields[key])
		return typ == "function_call_output" && len(v) > 0 && v[0] == '"'
	})
}

// ToolOutputs prepares immutable Responses rows while borrowing large content.
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

// projectMessage decodes the envelope and block metadata, borrowing only text
// strings and image URLs. Unknown block types retain ordinary decoding behavior.
func projectMessage(fields map[string]json.RawMessage, content json.RawMessage) (map[string]any, error) {
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		if key == "content" {
			continue
		}
		var decoded any
		if err := json.Unmarshal(value, &decoded); err != nil {
			return nil, err
		}
		out[key] = decoded
	}
	rows, err := Array(content)
	if err != nil {
		return nil, err
	}
	blocks := make([]any, 0, len(rows))
	for _, row := range rows {
		row = bytes.TrimSpace(row)
		var block any
		if len(row) > 0 && row[0] == '{' {
			block, err = ProjectObject(row, func(fields map[string]json.RawMessage, key string) bool {
				if key != "text" && key != "image_url" && key != "url" {
					return false
				}
				var typ string
				_ = json.Unmarshal(fields["type"], &typ)
				value := bytes.TrimSpace(fields[key])
				return (((typ == "input_text" || typ == "output_text") && key == "text") || (typ == "input_image" && (key == "image_url" || key == "url"))) && len(value) > 0 && value[0] == '"'
			})
		} else {
			err = json.Unmarshal(row, &block)
		}
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	out["content"] = blocks
	return out, nil
}

// Text materializes borrowed text only when a consumer actually needs it.
func Text(value any) string {
	if view, ok := value.(Value); ok {
		text, _ := view.Text()
		return text
	}
	text, _ := value.(string)
	return text
}

// Fields has already validated these JSON values. Unescaped strings need no
// unquoting or size walk; this is especially useful for base64 image data.
func borrowProjectedValue(raw json.RawMessage) (Value, error) {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' && bytes.IndexAny(raw, "\\<>&") < 0 && bytes.IndexByte(raw, 0xe2) < 0 && utf8.Valid(raw) {
		return Value{raw: raw, encodedSize: len(raw), stringSize: len(raw)}, nil
	}
	return Borrow(raw)
}
