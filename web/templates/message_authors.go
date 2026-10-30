package templates

import "context"

type messageAuthorsKey struct{}

// WithMessageAuthors controls presentation only; stored attribution is unchanged.
func WithMessageAuthors(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, messageAuthorsKey{}, enabled)
}

func MessageAuthorsEnabled(ctx context.Context) bool {
	enabled, configured := ctx.Value(messageAuthorsKey{}).(bool)
	return !configured || enabled
}
