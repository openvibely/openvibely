package contracts

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestLifecycleHookAndSteeringContextHelpers(t *testing.T) {
	if LifecycleHookCallFromContext(nil) {
		t.Fatal("nil context should not be a lifecycle hook call")
	}
	if !LifecycleHookCallFromContext(WithLifecycleHookCall(nil)) {
		t.Fatal("expected lifecycle hook context flag")
	}
	if WithSteeringCallback(context.Background(), nil) == nil || SteeringCallbackFromContext(nil) != nil {
		t.Fatal("steering callback helpers should be nil-safe")
	}
	ctx := WithSteeringCallback(context.Background(), func(context.Context) (string, error) { return "steer", nil })
	if got, err := SteeringCallbackFromContext(ctx)(context.Background()); got != "steer" || err != nil {
		t.Fatalf("steering callback = %q, %v", got, err)
	}
	wantErr := errors.New("reset")
	ctx = WithSteeringRetryResetCallback(context.Background(), func(context.Context) error { return wantErr })
	if err := SteeringRetryResetCallbackFromContext(ctx)(context.Background()); !errors.Is(err, wantErr) {
		t.Fatalf("steering reset callback error = %v", err)
	}
	if WithSteeringRetryResetCallback(context.Background(), nil) == nil || SteeringRetryResetCallbackFromContext(nil) != nil {
		t.Fatal("steering reset helpers should be nil-safe")
	}
	if WithMidTurnSteeringCallback(context.Background(), nil) == nil || MidTurnSteeringCallbackFromContext(nil) != nil {
		t.Fatal("mid-turn steering helpers should be nil-safe")
	}
	ctx = WithMidTurnSteeringCallback(context.Background(), func(ctx context.Context, deliver SteeringDeliverer) error {
		state, err := deliver(ctx, "steer")
		if err != nil || state.Status != SteeringDeliveryAccepted {
			t.Fatalf("delivery state = %#v, err=%v", state, err)
		}
		return nil
	})
	if err := MidTurnSteeringCallbackFromContext(ctx)(context.Background(), func(context.Context, string) (SteeringDeliveryState, error) {
		return SteeringDeliveryState{Status: SteeringDeliveryAccepted}, nil
	}); err != nil {
		t.Fatalf("mid-turn steering callback error = %v", err)
	}
	wakeup := make(chan struct{}, 1)
	ctx = WithMidTurnSteeringWakeup(context.Background(), wakeup)
	if MidTurnSteeringWakeupFromContext(ctx) != wakeup || MidTurnSteeringWakeupFromContext(nil) != nil {
		t.Fatal("mid-turn steering wakeup helpers should preserve the channel and be nil-safe")
	}
}

type fakeRuntimeToolTraceRecorder struct {
	events []string
}

func (f *fakeRuntimeToolTraceRecorder) RecordRuntimeToolEvent(_ context.Context, eventType string, _ any) {
	f.events = append(f.events, eventType)
}

func TestRuntimeToolTraceRecorderContextAndWrapper(t *testing.T) {
	if RuntimeToolTraceRecorderFromContext(nil) != nil {
		t.Fatal("nil context should have no trace recorder")
	}
	recorder := &fakeRuntimeToolTraceRecorder{}
	if RuntimeToolTraceRecorderFromContext(WithRuntimeToolTraceRecorder(nil, nil)) != nil {
		t.Fatal("nil recorder should not be attached")
	}
	ctx := WithRuntimeToolTraceRecorder(nil, recorder)
	if RuntimeToolTraceRecorderFromContext(ctx) != recorder {
		t.Fatal("expected recorder from context")
	}

	rt := &RuntimeTools{Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
		return "out", true, false, errors.New("wrapped")
	}}
	wrapped := TraceRuntimeTools(rt, recorder)
	out, handled, isErr, err := wrapped.Executor(context.Background(), "tool", json.RawMessage(`{"x":1}`))
	if out != "out" || !handled || isErr || err == nil {
		t.Fatalf("wrapped executor = out=%q handled=%v isErr=%v err=%v", out, handled, isErr, err)
	}
	if len(recorder.events) != 2 || recorder.events[0] != "tool_call" || recorder.events[1] != "tool_result" {
		t.Fatalf("trace events = %#v", recorder.events)
	}
	if TraceRuntimeTools(nil, recorder) != nil || TraceRuntimeTools(&RuntimeTools{}, recorder) == nil || TraceRuntimeTools(rt, nil) != rt {
		t.Fatal("TraceRuntimeTools should return unchanged values when wrapping is unavailable")
	}
}
