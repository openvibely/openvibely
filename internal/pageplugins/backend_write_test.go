package pageplugins

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func unreadBackend(t *testing.T) *Process {
	t.Helper()
	p := NewProcess(helperSpec(t))
	t.Cleanup(p.Abort)
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Call(t.Context(), "operation/invoke", map[string]any{"operation": "stop_reading"}); err != nil {
		t.Fatal(err)
	}
	return p
}

func largeBackendRequest() map[string]any {
	return map[string]any{"operation": "echo", "input": strings.Repeat("x", 512<<10)}
}

func waitBackendCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !condition() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("backend condition not reached")
		}
	}
}

func awaitBackendResult(t *testing.T, result <-chan error, timeout time.Duration) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(timeout):
		t.Fatal("backend call exceeded its cancellation/shutdown bound")
		return nil
	}
}

func TestBackendBlockedWriteHonorsDeadlineAndCancellation(t *testing.T) {
	for _, manual := range []bool{false, true} {
		name := "deadline"
		if manual {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			p := unreadBackend(t)
			var ctx context.Context
			var cancel context.CancelFunc
			if manual {
				ctx, cancel = context.WithCancel(t.Context())
			} else {
				ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { _, err := p.Call(ctx, "operation/invoke", largeBackendRequest()); result <- err }()
			waitBackendCondition(t, func() bool { return len(p.writeGate) == 1 })
			if manual {
				cancel()
			}
			err := awaitBackendResult(t, result, time.Second)
			want := context.DeadlineExceeded
			if manual {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			waitBackendCondition(t, func() bool { return !p.Running() })
			if len(p.writeGate) != 0 {
				t.Fatal("blocked writer was not reaped")
			}
			p.mu.Lock()
			pending := len(p.pending)
			p.mu.Unlock()
			if pending != 0 {
				t.Fatal("cancelled request left pending state")
			}
		})
	}
}

func TestBackendQueuedWriteAndCancellationNotificationAreBounded(t *testing.T) {
	p := unreadBackend(t)
	written := make(chan struct{}, 2)
	p.mu.Lock()
	p.stdin = observedBackendWriter{WriteCloser: p.stdin, written: written}
	p.mu.Unlock()
	// This small request fits in the pipe but never receives a response.
	smallCtx, smallCancel := context.WithCancel(t.Context())
	defer smallCancel()
	small := make(chan error, 1)
	go func() {
		_, err := p.Call(smallCtx, "operation/invoke", map[string]any{"operation": "echo"})
		small <- err
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("small request was not written")
	}

	bigCtx, bigCancel := context.WithCancel(t.Context())
	defer bigCancel()
	big := make(chan error, 1)
	go func() { _, err := p.Call(bigCtx, "operation/invoke", largeBackendRequest()); big <- err }()
	waitBackendCondition(t, func() bool { return len(p.writeGate) == 1 })
	queuedCtx, queuedCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer queuedCancel()
	queued := make(chan error, 1)
	go func() { _, err := p.Call(queuedCtx, "operation/invoke", largeBackendRequest()); queued <- err }()
	if err := awaitBackendResult(t, queued, time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued writer: %v", err)
	}
	// Its cancellation notification now has to contend with the blocked writer.
	smallCancel()
	if err := awaitBackendResult(t, small, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel notification: %v", err)
	}
	bigCancel()
	if err := awaitBackendResult(t, big, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked writer: %v", err)
	}
}

func TestBackendShutdownKillsDespiteBlockedWriter(t *testing.T) {
	p := unreadBackend(t)
	result := make(chan error, 1)
	go func() { _, err := p.Call(t.Context(), "operation/invoke", largeBackendRequest()); result <- err }()
	waitBackendCondition(t, func() bool { return len(p.writeGate) == 1 })
	stopped := make(chan error, 1)
	go func() { p.Stop(); stopped <- nil }()
	_ = awaitBackendResult(t, stopped, ShutdownGrace+1500*time.Millisecond)
	if err := awaitBackendResult(t, result, time.Second); err == nil {
		t.Fatal("blocked operation survived shutdown")
	}
	if p.Running() || p.Status() != "stopped" || len(p.writeGate) != 0 {
		t.Fatal("shutdown left a process or writer running")
	}
	p.Stop()
}

// Signal only after an actual pipe write finishes so the notification test
// cannot accidentally test cancellation before the first request was sent.
type observedBackendWriter struct {
	io.WriteCloser
	written chan struct{}
}

func (w observedBackendWriter) Write(data []byte) (int, error) {
	n, err := w.WriteCloser.Write(data)
	if err == nil {
		select {
		case w.written <- struct{}{}:
		default:
		}
	}
	return n, err
}
