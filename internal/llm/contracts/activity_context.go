package contracts

import "context"

type activityCallbackKey struct{}

// ActivityCallback records meaningful progress during a streaming model run.
// Implementations must be safe for concurrent use and should return quickly.
type ActivityCallback func()

// WithActivityCallback attaches a progress callback to a model request context.
func WithActivityCallback(ctx context.Context, callback ActivityCallback) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, activityCallbackKey{}, callback)
}

// NotifyActivity invokes the progress callback attached to ctx, if any.
func NotifyActivity(ctx context.Context) {
	if ctx == nil {
		return
	}
	callback, _ := ctx.Value(activityCallbackKey{}).(ActivityCallback)
	if callback != nil {
		callback()
	}
}
