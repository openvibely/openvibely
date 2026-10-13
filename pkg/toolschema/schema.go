// Package toolschema adapts tool schemas without changing the handler contract.
package toolschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

// Strict returns a closed, required-but-nullable schema. Unsupported schemas
// remain non-strict rather than silently losing arbitrary object fields.
// unions is the number of union fields charged to Anthropic's request budget.
func Strict(raw json.RawMessage, anthropic bool) (converted json.RawMessage, unions int, err error) {
	if len(raw) > 120000 {
		return nil, 0, fmt.Errorf("strict schema size limit exceeded")
	}
	var schema map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&schema); err != nil {
		return
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, 0, fmt.Errorf("invalid trailing tool schema")
	}
	properties, enums := schemaSize(schema)
	if properties > 5000 || enums > 1000 {
		return nil, 0, fmt.Errorf("strict schema property or enum limit exceeded")
	}
	if schema["type"] != "object" {
		return nil, 0, fmt.Errorf("tool root must be an object")
	}
	err = convert(schema, anthropic, &unions, 0)
	if err != nil {
		return nil, 0, err
	}
	_, enums = schemaSize(schema)
	if enums > 1000 {
		return nil, 0, fmt.Errorf("strict schema enum limit exceeded")
	}
	converted, err = json.Marshal(schema)
	return
}

func convert(s map[string]any, anthropic bool, unions *int, depth int) error {
	if depth > 9 {
		return fmt.Errorf("strict schema nesting limit exceeded")
	}
	for k := range s {
		switch k {
		case "type", "properties", "required", "additionalProperties", "items", "enum", "const", "description", "title", "default", "minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "pattern":
		default:
			return fmt.Errorf("unsupported strict keyword %s", k)
		}
	}
	delete(s, "default")
	if value, ok := s["const"]; ok {
		if _, hasEnum := s["enum"]; hasEnum {
			return fmt.Errorf("combined const and enum")
		}
		s["enum"] = []any{value}
		delete(s, "const")
	}
	if anthropic {
		for _, k := range []string{"minimum", "maximum", "minLength", "maxLength", "minItems", "maxItems", "pattern"} {
			if v, ok := s[k]; ok {
				desc, _ := s["description"].(string)
				s["description"] = fmt.Sprintf("%s Constraint: %s=%v.", desc, k, v)
				delete(s, k)
			}
		}
	}
	kind, ok := s["type"].(string)
	if !ok {
		return fmt.Errorf("unsupported strict type")
	}
	switch kind {
	case "object":
		props, _ := s["properties"].(map[string]any)
		// Properties describe known fields; only an explicit false closes an object.
		if s["additionalProperties"] != false {
			return fmt.Errorf("open object")
		}
		if props == nil {
			props = map[string]any{}
		}
		required := requiredFields(s)
		names := make([]string, 0, len(props))
		for name, value := range props {
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid property")
			}
			if err := convert(child, anthropic, unions, depth+1); err != nil {
				return err
			}
			if !required[name] && child["type"] != "null" {
				child["type"] = []any{child["type"], "null"}
				if values, ok := child["enum"].([]any); ok {
					child["enum"] = append(values, nil)
				}
				*unions++
			}
			names = append(names, name)
		}
		sort.Strings(names)
		s["properties"], s["required"], s["additionalProperties"] = props, names, false
	case "array":
		child, ok := s["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("untyped array")
		}
		return convert(child, anthropic, unions, depth+1)
	case "string", "integer", "number", "boolean", "null":
	default:
		return fmt.Errorf("unsupported type %s", kind)
	}
	return nil
}

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

// Count across the whole schema, not independently for each nested object.
func schemaSize(s map[string]any) (properties, enums int) {
	if values, ok := s["enum"].([]any); ok {
		enums += len(values)
	}
	if _, ok := s["const"]; ok {
		enums++
	}
	if props, ok := s["properties"].(map[string]any); ok {
		properties += len(props)
		for _, raw := range props {
			if child, ok := raw.(map[string]any); ok {
				p, e := schemaSize(child)
				properties += p
				enums += e
			}
		}
	}
	if child, ok := s["items"].(map[string]any); ok {
		p, e := schemaSize(child)
		properties += p
		enums += e
	}
	return
}
