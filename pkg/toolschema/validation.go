package toolschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/santhosh-tekuri/jsonschema/v5"
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

// NormalizeValidate removes null placeholders only from optional, non-nullable
// declared properties. Explicit nulls in arbitrary data are left untouched.
// Validation uses the original schema, including constraints omitted on the wire.
func NormalizeValidate(schema, input json.RawMessage) (json.RawMessage, error) {
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
	normalize(s, value)
	c := jsonschema.NewCompiler()
	// Tool schemas must be self-contained; never fetch a model/tool supplied URL.
	c.LoadURL = func(string) (io.ReadCloser, error) { return nil, fmt.Errorf("external schema references are disabled") }
	if err := c.AddResource("https://tools.invalid/schema.json", bytes.NewReader(schema)); err != nil {
		return nil, err
	}
	compiled, err := c.Compile("https://tools.invalid/schema.json")
	if err != nil {
		return nil, fmt.Errorf("invalid tool schema: %w", err)
	}
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
