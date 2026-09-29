package contracts

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

type steeringCallbackKey struct{}
type steeringRetryResetCallbackKey struct{}
type midTurnSteeringWakeupKey struct{}
type localSteeringCallbackKey struct{}

// SteeringCallback returns raw steering text to inject before the next provider/tool-loop model request.
type SteeringCallback func(context.Context) (string, error)

// LocalSteeringMessage preserves one queued Codex-style user input and its
// attachment boundary.
type LocalSteeringMessage struct {
	Text        string
	Attachments []models.Attachment
}

// LocalSteeringInput mirrors Codex's ordered pending-input drain. Messages is
// preferred; Text and Attachments retain compatibility with single-input callers.
type LocalSteeringInput struct {
	Text        string
	Attachments []models.Attachment
	Messages    []LocalSteeringMessage
}

type LocalSteeringCallback func(context.Context) (LocalSteeringInput, error)

// SteeringRetryResetCallback resets steering claimed by a failed provider attempt before retrying.
type SteeringRetryResetCallback func(context.Context) error

func WithSteeringCallback(ctx context.Context, callback SteeringCallback) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, steeringCallbackKey{}, callback)
}

func SteeringCallbackFromContext(ctx context.Context) SteeringCallback {
	if ctx == nil {
		return nil
	}
	callback, _ := ctx.Value(steeringCallbackKey{}).(SteeringCallback)
	return callback
}

func WithLocalSteeringCallback(ctx context.Context, callback LocalSteeringCallback) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, localSteeringCallbackKey{}, callback)
}

func LocalSteeringCallbackFromContext(ctx context.Context) LocalSteeringCallback {
	if ctx == nil {
		return nil
	}
	callback, _ := ctx.Value(localSteeringCallbackKey{}).(LocalSteeringCallback)
	return callback
}

func WithSteeringRetryResetCallback(ctx context.Context, callback SteeringRetryResetCallback) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, steeringRetryResetCallbackKey{}, callback)
}

func SteeringRetryResetCallbackFromContext(ctx context.Context) SteeringRetryResetCallback {
	if ctx == nil {
		return nil
	}
	callback, _ := ctx.Value(steeringRetryResetCallbackKey{}).(SteeringRetryResetCallback)
	return callback
}

// WithMidTurnSteeringWakeup attaches an event source that is signalled when
// steering becomes available for the active execution.
func WithMidTurnSteeringWakeup(ctx context.Context, wakeup <-chan struct{}) context.Context {
	if wakeup == nil {
		return ctx
	}
	return context.WithValue(ctx, midTurnSteeringWakeupKey{}, wakeup)
}

func MidTurnSteeringWakeupFromContext(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	wakeup, _ := ctx.Value(midTurnSteeringWakeupKey{}).(<-chan struct{})
	return wakeup
}
