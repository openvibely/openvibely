package contracts

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
)

// RuntimeToolDefinition is a provider-agnostic tool definition injected at request time.
type RuntimeToolAccess string

const (
	RuntimeToolAccessWrite RuntimeToolAccess = "write"
	RuntimeToolAccessRead  RuntimeToolAccess = "read"
)

type RuntimeToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Access      RuntimeToolAccess
}

// RuntimeToolExecutor runs request-scoped tools.
// If handled is false, provider adapters should fall back to their default executors.
type RuntimeToolExecutor func(ctx context.Context, name string, input json.RawMessage) (output string, handled bool, isError bool, err error)

// RuntimeToolFilter can allow/deny request-scoped tools.
// If handled is false, adapters should apply their default filtering behavior.
type RuntimeToolFilter func(name string) (allow bool, handled bool)

// RuntimeTools carries request-scoped tool definitions and execution hooks.
type RuntimeTools struct {
	Definitions []RuntimeToolDefinition
	Executor    RuntimeToolExecutor
	Filter      RuntimeToolFilter
	Metadata    any

	// SkipDefaultTools requests that provider-native/default tools be hidden for
	// this request. Use this for tightly scoped tool sessions, such as memory
	// maintenance, where the model should only see the supplied runtime tools.
	SkipDefaultTools bool
}

func (rt *RuntimeTools) HasDefinition(name string) bool {
	if rt == nil {
		return false
	}
	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return false
	}
	for _, def := range rt.Definitions {
		if strings.EqualFold(strings.TrimSpace(def.Name), needle) {
			return true
		}
	}
	return false
}

// DefinitionNames returns the display names for request-scoped runtime tool
// definitions, preserving definition order while trimming whitespace and
// omitting blank names.
func (rt *RuntimeTools) DefinitionNames() []string {
	if rt == nil {
		return nil
	}
	var names []string
	for _, def := range rt.Definitions {
		if name := strings.TrimSpace(def.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func (rt *RuntimeTools) ProviderParameters(name string) json.RawMessage {
	if rt == nil {
		return nil
	}
	for _, definition := range rt.Definitions {
		if !strings.EqualFold(strings.TrimSpace(definition.Name), strings.TrimSpace(name)) {
			continue
		}
		var schema map[string]any
		if json.Unmarshal(definition.Parameters, &schema) != nil {
			return definition.Parameters
		}
		properties, _ := schema["properties"].(map[string]any)
		changed := false
		for _, rawProperty := range properties {
			property, _ := rawProperty.(map[string]any)
			if _, ok := property["x-openvibely-omit-value"]; ok {
				delete(property, "x-openvibely-omit-value")
				changed = true
			}
		}
		if !changed {
			return definition.Parameters
		}
		encoded, err := json.Marshal(schema)
		if err != nil {
			return definition.Parameters
		}
		return encoded
	}
	return nil
}

// NormalizeToolInput preserves optional-argument presence at provider boundaries.
// It removes optional nulls and schema-declared non-semantic omission sentinels
// before dispatch. Every other value remains explicit caller input.
func (rt *RuntimeTools) NormalizeToolInput(name string, input json.RawMessage) json.RawMessage {
	if rt == nil || len(input) == 0 {
		return input
	}
	var definition *RuntimeToolDefinition
	for i := range rt.Definitions {
		if strings.EqualFold(strings.TrimSpace(rt.Definitions[i].Name), strings.TrimSpace(name)) {
			definition = &rt.Definitions[i]
			break
		}
	}
	if definition == nil {
		return input
	}
	var schema struct {
		Properties map[string]struct {
			OmitValue json.RawMessage `json:"x-openvibely-omit-value"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(definition.Parameters, &schema) != nil || json.Unmarshal(input, &object) != nil {
		return input
	}
	required := make(map[string]bool, len(schema.Required))
	for _, field := range schema.Required {
		required[strings.ToLower(field)] = true
	}
	changed := false
	for inputKey, value := range object {
		for property, propertySchema := range schema.Properties {
			if !strings.EqualFold(property, inputKey) {
				continue
			}
			optionalNull := string(value) == "null" && !required[strings.ToLower(inputKey)]
			omitSentinel := len(propertySchema.OmitValue) > 0 && jsonEqual(value, propertySchema.OmitValue)
			if optionalNull || omitSentinel {
				delete(object, inputKey)
				changed = true
			}
			break
		}
	}
	if !changed {
		return input
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return input
	}
	return normalized
}

func jsonEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

type runtimeToolsContextKey struct{}

// WithRuntimeTools annotates context with request-scoped tool definitions/executor.
func WithRuntimeTools(ctx context.Context, tools *RuntimeTools) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if tools == nil {
		return ctx
	}
	return context.WithValue(ctx, runtimeToolsContextKey{}, tools)
}

// WithoutRuntimeTools returns a child context that masks any inherited runtime tools
// while preserving cancellation, deadlines, and other context values.
func WithoutRuntimeTools(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, runtimeToolsContextKey{}, (*RuntimeTools)(nil))
}

// RuntimeToolsFromContext returns request-scoped runtime tools, if present.
func RuntimeToolsFromContext(ctx context.Context) *RuntimeTools {
	if ctx == nil {
		return nil
	}
	if tools, ok := ctx.Value(runtimeToolsContextKey{}).(*RuntimeTools); ok {
		return tools
	}
	return nil
}
