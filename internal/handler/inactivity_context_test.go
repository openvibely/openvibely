package handler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
)

type fakeInactivityClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeInactivityClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeInactivityClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type fakeInactivityTimer struct {
	ch     chan time.Time
	resets chan time.Duration
}

func newFakeInactivityTimer() *fakeInactivityTimer {
	return &fakeInactivityTimer{
		ch:     make(chan time.Time, 1),
		resets: make(chan time.Duration, 4),
	}
}

func (t *fakeInactivityTimer) Chan() <-chan time.Time { return t.ch }
func (t *fakeInactivityTimer) Stop() bool             { return true }
func (t *fakeInactivityTimer) Reset(duration time.Duration) bool {
	t.resets <- duration
	return true
}

func (t *fakeInactivityTimer) Fire(at time.Time) {
	t.ch <- at
}

func TestInactivityContextInitialTimerUsesRemainingWindow(t *testing.T) {
	const (
		timeout = 30 * time.Minute
		delay   = 5 * time.Minute
	)
	start := time.Unix(1_000, 0)
	var nowCalls atomic.Int32
	now := func() time.Time {
		if nowCalls.Add(1) == 1 {
			return start
		}
		return start.Add(delay)
	}
	initialDuration := make(chan time.Duration, 1)
	timer := newFakeInactivityTimer()
	ctx, cancel, _ := withInactivityTimeoutComponents(
		context.Background(),
		timeout,
		now,
		func(duration time.Duration) inactivityTimer {
			initialDuration <- duration
			return timer
		},
	)
	defer cancel()

	select {
	case duration := <-initialDuration:
		if want := timeout - delay; duration != want {
			t.Fatalf("initial timer duration = %v, want remaining window %v", duration, want)
		}
	case <-ctx.Done():
		t.Fatalf("context ended before timer creation: %v", ctx.Err())
	case <-time.After(time.Second):
		t.Fatal("inactivity timer was not created")
	}
}

func TestStreamingActivityResetsInactivityContext(t *testing.T) {
	const timeout = 30 * time.Minute
	clock := &fakeInactivityClock{now: time.Unix(1_000, 0)}
	timer := newFakeInactivityTimer()
	initialDuration := make(chan time.Duration, 1)
	ctx, cancel, reset := withInactivityTimeoutComponents(
		context.Background(),
		timeout,
		clock.Now,
		func(duration time.Duration) inactivityTimer {
			initialDuration <- duration
			return timer
		},
	)
	defer cancel()
	ctx = llmcontracts.WithActivityCallback(ctx, reset)
	writer := llmstream.NewWriter("", "", nil, ctx, time.Hour)
	defer writer.Stop()
	select {
	case duration := <-initialDuration:
		if duration != timeout {
			t.Fatalf("initial timer duration = %v, want %v", duration, timeout)
		}
	case <-time.After(time.Second):
		t.Fatal("inactivity timer was not created")
	}

	clock.Advance(timeout / 2)
	if _, err := writer.Write([]byte("still working")); err != nil {
		t.Fatalf("stream write failed: %v", err)
	}

	// Wait until the timer-owning goroutine has applied the stream activity.
	select {
	case duration := <-timer.resets:
		if duration != timeout {
			t.Fatalf("reset duration = %v, want %v", duration, timeout)
		}
	case <-time.After(time.Second):
		t.Fatal("stream activity did not reset the inactivity timer")
	}

	// Advance directly to the new inactivity boundary and fire the fake timer.
	clock.Advance(timeout)
	timer.Fire(clock.Now())
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("context error = %v, want deadline exceeded", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("context did not expire after inactivity")
	}
}

func TestInactivityContextManualCancel(t *testing.T) {
	ctx, cancel, _ := withInactivityTimeout(context.Background(), time.Hour)
	cancel()

	<-ctx.Done()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want canceled", ctx.Err())
	}
}

func TestInactivityContextParentCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel, _ := withInactivityTimeout(parent, time.Hour)
	defer cancel()
	cancelParent()

	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error = %v, want canceled", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation was not propagated")
	}
}
