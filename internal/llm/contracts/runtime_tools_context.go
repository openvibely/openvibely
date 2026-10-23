package contracts

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/pkg/toolschema"
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
	// Parameters holds externally supplied JSON; built-ins use Schema.
	Parameters json.RawMessage
	Schema     *toolschema.Schema
	Access     RuntimeToolAccess
}

// RuntimeToolExecutor runs request-scoped tools.
// If handled is false, provider adapters should fall back to their default executors.
type RuntimeToolExecutor func(ctx context.Context, name string, input json.RawMessage) (output string, handled bool, isError bool, err error)

// RuntimeToolFilter can allow/deny request-scoped tools.
// If handled is false, adapters should apply their default filtering behavior.
type RuntimeToolFilter func(name string) (allow bool, handled bool)

// RuntimeTools carries request-scoped tool definitions and execution hooks.
type RuntimeTools struct {
	// Definitions are candidates; providers must apply Filter before every request.
	// Keeping denied candidates permits grants to change during a tool loop.
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
			if rt.Filter != nil {
				if allow, handled := rt.Filter(def.Name); handled {
					return allow
				}
			}
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
		if name := strings.TrimSpace(def.Name); name != "" && rt.HasDefinition(name) {
			names = append(names, name)
		}
	}
	return names
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

func (d RuntimeToolDefinition) ParameterSchema() json.RawMessage {
	return toolschema.JSON(d.Schema, d.Parameters)
}

// ToolCallID is supplied by the provider loop, never by tool arguments.
type toolCallIDKey struct{}

func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}
func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
