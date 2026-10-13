package toolschema

import "encoding/json"

// MarshalOpenAI builds the strict form used to check whether external schemas
// support optional-null normalization. Provider requests use the original JSON.
func MarshalOpenAI(s *Schema, raw json.RawMessage) (json.RawMessage, error) {
	return marshalStrict(s, raw, serializer{property: func(child map[string]any, required bool) bool {
		if !required && child["type"] != "null" {
			child["type"] = []any{child["type"], "null"}
			if values, ok := child["enum"].([]any); ok {
				child["enum"] = append(values, nil)
			}
		}
		return true
	}})
}
