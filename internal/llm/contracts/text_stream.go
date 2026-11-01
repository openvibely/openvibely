package contracts

import "context"

type textStreamKey struct{}

// WithTextStream attaches a transient response-text sink, without recording an execution.
func WithTextStream(ctx context.Context, sink func(string)) context.Context {
	return context.WithValue(ctx, textStreamKey{}, sink)
}
func TextStreamFromContext(ctx context.Context) func(string) {
	sink, _ := ctx.Value(textStreamKey{}).(func(string))
	return sink
}
