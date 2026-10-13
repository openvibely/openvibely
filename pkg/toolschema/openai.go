package toolschema

import "encoding/json"

// MarshalOpenAI is shared by OpenAI and compatible endpoints that opt into strict
// tools. Non-strict endpoints receive the ordinary schema with optional fields.
func MarshalOpenAI(s *Schema, raw json.RawMessage, strict bool) (json.RawMessage, error) {
	if !strict {
		return JSON(s, raw), nil
	}
	out, _, err := marshalStrict(s, raw, serializer{property: func(child map[string]any, required bool, cost *Cost) bool {
		if !required && child["type"] != "null" {
			child["type"] = []any{child["type"], "null"}
			if values, ok := child["enum"].([]any); ok {
				child["enum"] = append(values, nil)
			}
			cost.Unions++
		}
		return true
	}})
	return out, err
}
