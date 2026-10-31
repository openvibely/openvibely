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
	return context.WithValue(ctx, executionMetadataKey{}, true)
}
func ExecutionMetadataOnly(ctx context.Context) bool {
	value, _ := ctx.Value(executionMetadataKey{}).(bool)
	return value
}
