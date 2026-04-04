package contracts

import (
	"context"
	"encoding/json"
	"strings"
)

// RuntimeToolDefinition is a provider-agnostic tool definition injected at request time.
type RuntimeToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
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
