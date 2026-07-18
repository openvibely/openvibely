package handler

import (
	"context"
	"sync"
	"time"
)

// inactivityContext is cancelled when no activity is reported for timeout.
// It deliberately has no fixed Deadline because each activity notification
// moves the expiry time forward.
type inactivityContext struct {
	parent   context.Context
	timeout  time.Duration
	done     chan struct{}
	activity chan struct{}
	now      func() time.Time
	newTimer func(time.Duration) inactivityTimer

	mu           sync.Mutex
	err          error
	lastActivity time.Time
}

type inactivityTimer interface {
	Chan() <-chan time.Time
	Stop() bool
	Reset(time.Duration) bool
}

type realInactivityTimer struct {
	*time.Timer
}

func (t realInactivityTimer) Chan() <-chan time.Time { return t.C }

func withInactivityTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, func()) {
	return withInactivityTimeoutComponents(parent, timeout, time.Now, func(duration time.Duration) inactivityTimer {
		return realInactivityTimer{Timer: time.NewTimer(duration)}
	})
}

func withInactivityTimeoutComponents(
	parent context.Context,
	timeout time.Duration,
	now func() time.Time,
	newTimer func(time.Duration) inactivityTimer,
) (context.Context, context.CancelFunc, func()) {
	if parent == nil {
		panic("cannot create context from nil parent")
	}
	if now == nil || newTimer == nil {
		panic("inactivity context requires a clock and timer factory")
	}

	ctx := &inactivityContext{
		parent:       parent,
		timeout:      timeout,
		done:         make(chan struct{}),
		activity:     make(chan struct{}, 1),
		now:          now,
		newTimer:     newTimer,
		lastActivity: now(),
	}
	go ctx.run()

	cancel := func() { ctx.finish(context.Canceled) }
	return ctx, cancel, ctx.reset
}

func (c *inactivityContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *inactivityContext) Done() <-chan struct{}       { return c.done }
func (c *inactivityContext) Value(key any) any           { return c.parent.Value(key) }

func (c *inactivityContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *inactivityContext) reset() {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return
	}
	c.lastActivity = c.now()
	c.mu.Unlock()

	// Coalesce bursts of streaming chunks. The timer-owning goroutine only
	// needs one wake-up because it reads the latest activity timestamp.
	select {
	case c.activity <- struct{}{}:
	default:
	}
}

func (c *inactivityContext) run() {
	if err := c.parent.Err(); err != nil {
		c.finish(err)
		return
	}
	if c.timeout <= 0 {
		c.finish(context.DeadlineExceeded)
		return
	}

	remaining := c.remaining()
	if remaining <= 0 {
		c.finish(context.DeadlineExceeded)
		return
	}
	timer := c.newTimer(remaining)
	defer timer.Stop()
	for {
		select {
		case <-c.parent.Done():
			c.finish(c.parent.Err())
			return
		case <-c.done:
			return
		case <-c.activity:
			resetTimer(timer, c.remaining())
		case <-timer.Chan():
			remaining := c.remaining()
			if remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			c.finish(context.DeadlineExceeded)
			return
		}
	}
}

func (c *inactivityContext) remaining() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return 0
	}
	return c.timeout - c.now().Sub(c.lastActivity)
}

func resetTimer(timer inactivityTimer, duration time.Duration) {
	if duration <= 0 {
		duration = time.Nanosecond
	}
	if !timer.Stop() {
		select {
		case <-timer.Chan():
		default:
		}
	}
	timer.Reset(duration)
}

func (c *inactivityContext) finish(err error) {
	if err == nil {
		err = context.Canceled
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return
	}
	c.err = err
	close(c.done)
}
