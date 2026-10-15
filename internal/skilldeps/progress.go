package skilldeps

import "context"

type progressKey struct{}

func WithProgress(ctx context.Context, report func(Progress)) context.Context {
	return context.WithValue(ctx, progressKey{}, report)
}
func ProgressFromContext(ctx context.Context) func(Progress) {
	report, _ := ctx.Value(progressKey{}).(func(Progress))
	return report
}
