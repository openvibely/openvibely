package contracts

import "context"

type lifecycleCompletionUserMessageKey struct{}

// WithLifecycleCompletionUserMessage records the latest user-authored input
// that after_complete hooks should receive for the current model call.
func WithLifecycleCompletionUserMessage(ctx context.Context, message string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, lifecycleCompletionUserMessageKey{}, message)
}

// LifecycleCompletionUserMessageFromContext returns an explicit current-turn
// user message when upstream request assembly differs from the provider prompt.
func LifecycleCompletionUserMessageFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	message, ok := ctx.Value(lifecycleCompletionUserMessageKey{}).(string)
	return message, ok
}
