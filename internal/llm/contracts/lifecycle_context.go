package contracts

import (
	"context"
	"strings"
)

type lifecycleCompletionUserMessageKey struct{}
type directUsageProjectKey struct{}

// WithDirectUsageProject attaches an authoritative project to a direct model
// call so post-call usage recording does not need repository-root discovery.
func WithDirectUsageProject(ctx context.Context, projectID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, directUsageProjectKey{}, strings.TrimSpace(projectID))
}

// DirectUsageProjectFromContext returns the authoritative project attached to a
// direct model call, or an empty string when fallback attribution is required.
func DirectUsageProjectFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(directUsageProjectKey{}).(string)
	return strings.TrimSpace(value)
}

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
