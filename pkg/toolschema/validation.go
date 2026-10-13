package toolschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func requiredFields(s map[string]any) map[string]bool {
	result := map[string]bool{}
	if names, ok := s["required"].([]any); ok {
		for _, name := range names {
			if n, ok := name.(string); ok {
				result[n] = true
			}
		}
	}
	return result
}

// ValidateArguments leaves unsupported external schemas to their tool server.
// Built-in schema errors and validation failures always reject the call.
func ValidateArguments(schema *Schema, raw, input json.RawMessage) (json.RawMessage, error) {
	return normalizeValidate(JSON(schema, raw), input, schema == nil)
}

func normalizeValidate(schema, input json.RawMessage, external bool) (json.RawMessage, error) {
	var s map[string]any
	if err := json.Unmarshal(schema, &s); err != nil {
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing tool arguments")
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, fmt.Errorf("tool arguments must be an object")
	}
	compiled, err := cachedValidator(schema)
	if err != nil {
		if external {
			return input, nil
		}
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
	if external {
		// Valid external arguments belong to the tool server. In particular,
		// a draft-07 $ref can override a sibling type and allow explicit null.
		if validationErr := compiled.Validate(value); validationErr == nil {
			return input, nil
		} else if _, conversionErr := MarshalOpenAI(nil, schema); conversionErr != nil {
			// Only schemas supported by strict conversion can contain our
			// nullable placeholders. Do not rewrite other schema dialects.
			return nil, fmt.Errorf("invalid tool arguments: %w", validationErr)
		}
	}
	normalize(s, value)
	if err = compiled.Validate(value); err != nil {
		return nil, fmt.Errorf("invalid tool arguments: %w", err)
	}
	return json.Marshal(value)
}

func normalize(s map[string]any, value any) {
	switch v := value.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		required := requiredFields(s)
		for name, raw := range props {
			child, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			val, present := v[name]
			if !present {
				continue
			}
			// Only simple non-nullable types are converted; union/ref semantics remain intact.
			kind, _ := child["type"].(string)
			if val == nil && !required[name] && kind != "" && kind != "null" {
				delete(v, name)
				continue
			}
			normalize(child, val)
		}
	case []any:
		if child, ok := s["items"].(map[string]any); ok {
			for _, item := range v {
				normalize(child, item)
			}
		}
	}
}
