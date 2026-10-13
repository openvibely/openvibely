package toolschema

import (
	"encoding/json"
	"fmt"
)

// MarshalAnthropic preserves optional fields. Unsupported numeric/length
// constraints remain in descriptions and are enforced against the source schema
// before execution. Cost is used for Anthropic's aggregate request limits.
func MarshalAnthropic(s *Schema, raw json.RawMessage) (json.RawMessage, Cost, error) {
	return marshalStrict(s, raw, serializer{
		property: func(child map[string]any, required bool, cost *Cost) bool {
			if !required {
				cost.Optional++
			}
			return required
		},
		constraints: func(out map[string]any) {
			for _, key := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "pattern"} {
				if value, ok := out[key]; ok {
					desc, _ := out["description"].(string)
					out["description"] = fmt.Sprintf("%s Constraint: %s=%v.", desc, key, value)
					delete(out, key)
				}
			}
		},
	})
}
