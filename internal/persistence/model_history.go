package persistence

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

type modelHistoryKey struct{}

// WithModelHistory marks a model-only history read. UI reads must not use this
// context: completed native replay can replace their display output projection.
func WithModelHistory(ctx context.Context, provider models.LLMProvider) context.Context {
	return context.WithValue(ctx, modelHistoryKey{}, provider)
}

func ModelHistoryFormat(ctx context.Context) (format, itemsKey string, ok bool) {
	provider, _ := ctx.Value(modelHistoryKey{}).(models.LLMProvider)
	switch provider {
	case models.ProviderOpenAI:
		return "envelope", "responses_input", true
	case models.ProviderAnthropic:
		return "envelope", "messages", true
	case models.ProviderOpenAICompatible, models.ProviderOllama:
		return "array", "", true
	default:
		return "", "", false
	}
}

type executionMetadataKey struct{}

// WithExecutionMetadata requests only identity, ownership, status and ordering
// fields. Callers must not use it to render output or construct model history.
func WithExecutionMetadata(ctx context.Context) context.Context {
	return WithExecutionPayload(ctx, 0)
}
func ExecutionMetadataOnly(ctx context.Context) bool {
	_, ok := ctx.Value(executionMetadataKey{}).(ExecutionPayload)
	return ok
}

// ExecutionPayload selects the large fields needed in addition to metadata.
// Output is never included; UI and full-history callers must use their normal read.
type ExecutionPayload uint8

const (
	ExecutionPrompt ExecutionPayload = 1 << iota
	ExecutionReasoning
	ExecutionError
	ExecutionSession
)

func WithExecutionPayload(ctx context.Context, fields ExecutionPayload) context.Context {
	return context.WithValue(ctx, executionMetadataKey{}, fields)
}
func ExecutionPayloadFromContext(ctx context.Context) ExecutionPayload {
	fields, _ := ctx.Value(executionMetadataKey{}).(ExecutionPayload)
	return fields
}

type checkpointRowsKey struct{}

// WithCheckpointRows requests immutable native replay rows at internal model
// boundaries. The default repository API retains its serialized compatibility view.
func WithCheckpointRows(ctx context.Context) context.Context {
	return context.WithValue(ctx, checkpointRowsKey{}, true)
}
func CheckpointRows(ctx context.Context) bool {
	value, _ := ctx.Value(checkpointRowsKey{}).(bool)
	return value
}
