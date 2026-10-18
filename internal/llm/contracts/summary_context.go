package contracts

import "context"

type summarySystemPromptKey struct{}

// WithSummarySystemPrompt preserves the originating request's rendered system
// instructions while the summary uses a direct call with a raw user prompt.
func WithSummarySystemPrompt(ctx context.Context, prompt string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, summarySystemPromptKey{}, prompt)
}

func SummarySystemPromptFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	prompt, ok := ctx.Value(summarySystemPromptKey{}).(string)
	return prompt, ok
}
