package contracts

import "context"

type steeringCallbackKey struct{}
type steeringRetryResetCallbackKey struct{}
type midTurnSteeringCallbackKey struct{}

// SteeringCallback returns raw steering text to inject before the next provider/tool-loop model request.
type SteeringCallback func(context.Context) (string, error)

// SteeringRetryResetCallback resets steering claimed by a failed provider attempt before retrying.
type SteeringRetryResetCallback func(context.Context) error

// SteeringDeliveryStatus describes provider-side mid-turn steering delivery state.
type SteeringDeliveryStatus string

const (
	SteeringDeliveryUnavailable SteeringDeliveryStatus = "unavailable"
	SteeringDeliveryDelivered   SteeringDeliveryStatus = "delivered"
	SteeringDeliveryPending     SteeringDeliveryStatus = "pending"
	SteeringDeliveryAmbiguous   SteeringDeliveryStatus = "ambiguous"
	SteeringDeliveryAccepted    SteeringDeliveryStatus = "accepted"
	SteeringDeliveryFailed      SteeringDeliveryStatus = "failed"
)

// SteeringDeliveryState records whether a provider accepted or rejected a mid-turn steering event.
type SteeringDeliveryState struct {
	Status             SteeringDeliveryStatus
	SteeringID         string
	ResponseID         string
	PreviousResponseID string
	Error              string
}

// SteeringDeliverer sends raw steering text to an active provider response.
type SteeringDeliverer func(context.Context, string) (SteeringDeliveryState, error)

// MidTurnSteeringCallback claims and delivers steering while a provider stream is active.
type MidTurnSteeringCallback func(context.Context, SteeringDeliverer) error

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

func WithMidTurnSteeringCallback(ctx context.Context, callback MidTurnSteeringCallback) context.Context {
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, midTurnSteeringCallbackKey{}, callback)
}

func MidTurnSteeringCallbackFromContext(ctx context.Context) MidTurnSteeringCallback {
	if ctx == nil {
		return nil
	}
	callback, _ := ctx.Value(midTurnSteeringCallbackKey{}).(MidTurnSteeringCallback)
	return callback
}
