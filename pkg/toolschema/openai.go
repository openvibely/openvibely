package toolschema

import "encoding/json"

// MarshalOpenAI is shared by OpenAI and compatible endpoints that opt into strict
// tools. Non-strict endpoints use JSON to retain the original contract.
func MarshalOpenAI(s *Schema, raw json.RawMessage) (json.RawMessage, error) {
	out, _, err := marshalStrict(s, raw, serializer{property: func(child map[string]any, required bool, cost *Cost) bool {
		if !required && child["type"] != "null" {
			child["type"] = []any{child["type"], "null"}
			if values, ok := child["enum"].([]any); ok {
				child["enum"] = append(values, nil)
			}
		}
		return true
	}})
	return out, err
}
