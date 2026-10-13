// Package toolschema defines tool inputs independently of provider wire formats.
package toolschema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// Schema is the shared definition for built-in tools. A property is optional
// unless its name appears in Required. Nil AdditionalProperties means open;
// serializers must never silently close arbitrary user data.
// External schemas stay raw when they use features this representation cannot express.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Description          string             `json:"description,omitempty"`
	Title                string             `json:"title,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Enum                 []any              `json:"enum,omitempty"`
	Const                json.RawMessage    `json:"const,omitempty"`
	Default              any                `json:"default,omitempty"`
	Minimum              *json.Number       `json:"minimum,omitempty"`
	Maximum              *json.Number       `json:"maximum,omitempty"`
	MinLength            *int               `json:"minLength,omitempty"`
	MaxLength            *int               `json:"maxLength,omitempty"`
	MinItems             *int               `json:"minItems,omitempty"`
	MaxItems             *int               `json:"maxItems,omitempty"`
	Pattern              string             `json:"pattern,omitempty"`
}

func Pointer[T any](v T) *T { return &v }

// Literal preserves JSON constants, including false, zero and explicit null.
func Literal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func Number(v string) *json.Number { n := json.Number(v); return &n }

// JSON returns the unmodified handler contract. Raw schemas belong to external
// tools; typed built-in definitions take precedence. Invalid built-in constants
// are programming errors, like an invalid regexp passed to regexp.MustCompile.
func JSON(s *Schema, raw json.RawMessage) json.RawMessage {
	if s == nil {
		return raw
	}
	b, err := json.Marshal(s)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in tool schema: %v", err))
	}
	return b
}

// WithoutProperties returns a copy suitable for request-scoped restrictions.
func (s *Schema) WithoutProperties(names ...string) *Schema {
	copy := *s
	copy.Properties = make(map[string]*Schema, len(s.Properties))
	removed := map[string]bool{}
	for _, name := range names {
		removed[name] = true
	}
	for name, child := range s.Properties {
		if !removed[name] {
			copy.Properties[name] = child
		}
	}
	copy.Required = nil
	for _, name := range s.Required {
		if !removed[name] {
			copy.Required = append(copy.Required, name)
		}
	}
	return &copy
}

// ParseExternal is deliberately conservative: unsupported keywords/types cause
// strict mode to be disabled, retaining the original JSON without data loss.
func ParseExternal(raw json.RawMessage) (*Schema, error) {
	if len(raw) > 120000 {
		return nil, fmt.Errorf("strict schema size limit exceeded")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	var s *Schema
	if err := dec.Decode(&s); err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing tool schema")
	}
	return s, nil
}

type Cost struct {
	Optional int
	Unions   int
}
type serializer struct {
	property    func(map[string]any, bool, *Cost) bool
	constraints func(map[string]any)
}

func marshalStrict(s *Schema, raw json.RawMessage, rules serializer) (json.RawMessage, Cost, error) {
	var cost Cost
	if s == nil {
		var err error
		s, err = ParseExternal(raw)
		if err != nil {
			return nil, cost, err
		}
	}
	if s == nil || s.Type != "object" {
		return nil, cost, fmt.Errorf("tool root must be an object")
	}
	properties, enums := 0, 0
	out, err := build(s, rules, &cost, 0, &properties, &enums)
	if err != nil {
		return nil, cost, err
	}
	if properties > 5000 || enums > 1000 {
		return nil, cost, fmt.Errorf("strict schema property or enum limit exceeded")
	}
	data, err := json.Marshal(out)
	if len(data) > 120000 {
		return nil, cost, fmt.Errorf("strict schema size limit exceeded")
	}
	return data, cost, err
}

func build(s *Schema, rules serializer, cost *Cost, depth int, properties, enums *int) (map[string]any, error) {
	if s == nil || depth > 9 {
		return nil, fmt.Errorf("invalid or deeply nested strict schema")
	}
	out := map[string]any{"type": s.Type}
	if s.Description != "" {
		out["description"] = s.Description
	}
	if s.Title != "" {
		out["title"] = s.Title
	}
	if s.Enum != nil {
		out["enum"] = append([]any{}, s.Enum...)
	}
	if s.Const != nil {
		if s.Enum != nil {
			return nil, fmt.Errorf("combined const and enum")
		}
		out["enum"] = []any{s.Const}
	}
	if s.Minimum != nil {
		out["minimum"] = *s.Minimum
	}
	if s.Maximum != nil {
		out["maximum"] = *s.Maximum
	}
	if s.MinLength != nil {
		out["minLength"] = *s.MinLength
	}
	if s.MaxLength != nil {
		out["maxLength"] = *s.MaxLength
	}
	if s.MinItems != nil {
		out["minItems"] = *s.MinItems
	}
	if s.MaxItems != nil {
		out["maxItems"] = *s.MaxItems
	}
	if s.Pattern != "" {
		out["pattern"] = s.Pattern
	}
	if rules.constraints != nil {
		rules.constraints(out)
	}
	switch s.Type {
	case "object":
		if s.AdditionalProperties == nil || *s.AdditionalProperties {
			return nil, fmt.Errorf("open object")
		}
		required := map[string]bool{}
		for _, name := range s.Required {
			if _, ok := s.Properties[name]; !ok {
				return nil, fmt.Errorf("unknown required property %s", name)
			}
			required[name] = true
		}
		names := make([]string, 0, len(s.Properties))
		for name := range s.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		props := map[string]any{}
		requiredNames := []string{}
		*properties += len(names)
		for _, name := range names {
			child, err := build(s.Properties[name], rules, cost, depth+1, properties, enums)
			if err != nil {
				return nil, err
			}
			previousEnum, _ := child["enum"].([]any)
			if rules.property(child, required[name], cost) {
				requiredNames = append(requiredNames, name)
			}
			if values, ok := child["enum"].([]any); ok {
				*enums += len(values) - len(previousEnum)
			}
			props[name] = child
		}
		out["properties"] = props
		out["required"] = requiredNames
		out["additionalProperties"] = false
	case "array":
		child, err := build(s.Items, rules, cost, depth+1, properties, enums)
		if err != nil {
			return nil, err
		}
		out["items"] = child
	case "string", "integer", "number", "boolean", "null":
	default:
		return nil, fmt.Errorf("unsupported strict type %s", s.Type)
	}
	if values, ok := out["enum"].([]any); ok {
		*enums += len(values)
	}
	return out, nil
}
