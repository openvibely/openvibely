//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testDesktopManager(start func(string) (*desktopProcess, error)) *desktopManager {
	return &desktopManager{mu: &sync.Mutex{}, sessions: map[string]*desktopProcess{}, starting: map[string]*desktopStart{}, deleting: map[string]bool{}, start: start}
}
func TestDesktopStartupDoesNotBlockOtherDesktops(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m := testDesktopManager(func(string) (*desktopProcess, error) {
		calls.Add(1)
		close(started)
		<-finish
		return &desktopProcess{done: make(chan struct{})}, nil
	})
	existing := &desktopProcess{done: make(chan struct{}), uid: desktopUID("task-a")}
	m.sessions["task-a"] = existing
	results := make(chan error, 2)
	go func() { _, err := m.ensure(t.Context(), "task-b", true); results <- err }()
	<-started
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	active := make(chan error, 1)
	go func() {
		p, err := m.ensure(t.Context(), "task-a", true)
		if err == nil && p != existing {
			err = errors.New("wrong desktop")
		}
		active <- err
	}()
	select {
	case err := <-active:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("existing desktop blocked behind startup")
	}
	// Same-desktop waiters can cancel without cancelling the shared startup.
	ctx, cancel := context.WithCancel(t.Context())
	waiter := make(chan error, 1)
	go func() { _, err := m.ensure(ctx, "task-b", true); waiter <- err }()
	cancel()
	select {
	case err := <-waiter:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter ignored cancellation")
	}
	go func() { _, err := m.ensure(t.Context(), "task-b", true); results <- err }()
	close(finish)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate starts: %d", calls.Load())
	}
}

func TestDesktopStartupReservesCapacityAndReleasesFailedSlot(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	failure := errors.New("startup failed")
	m := testDesktopManager(func(string) (*desktopProcess, error) { close(started); <-finish; return nil, failure })
	for i := 0; i < 31; i++ {
		name := fmt.Sprintf("task-existing-%d", i)
		m.sessions[name] = &desktopProcess{uid: desktopUID(name), done: make(chan struct{})}
	}
	result := make(chan error, 1)
	go func() { _, err := m.ensure(t.Context(), "task-new", true); result <- err }()
	<-started
	defer func() {
		select {
		case <-finish:
		default:
			close(finish)
		}
	}()
	if _, err := m.ensure(t.Context(), "task-overflow", true); err == nil {
		t.Fatal("pending startup did not reserve a slot")
	}
	close(finish)
	if err := <-result; !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if len(m.starting) != 0 {
		t.Fatal("failed startup leaked a slot")
	}
	m.start = func(string) (*desktopProcess, error) { return &desktopProcess{done: make(chan struct{})}, nil }
	if _, err := m.ensure(t.Context(), "task-retry", true); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopShutdownWaitsForStartupWithoutHoldingLock(t *testing.T) {
	started, finish := make(chan struct{}), make(chan struct{})
	m := testDesktopManager(func(string) (*desktopProcess, error) {
		close(started)
		<-finish
		return &desktopProcess{done: make(chan struct{})}, nil
	})
	result := make(chan error, 1)
	go func() { _, err := m.ensure(t.Context(), "task-new", true); result <- err }()
	<-started
	m.mu.Lock()
	m.stopping = true
	m.mu.Unlock()
	drained := make(chan error, 1)
	go func() { drained <- m.waitStarts(t.Context(), "") }()
	close(finish)
	if err := <-result; err == nil {
		t.Fatal("request admitted during shutdown")
	}
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown deadlock")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions["task-new"] == nil {
		t.Fatal("shutdown cannot find the newly started process")
	}
}

func TestDesktopHealthProbeDoesNotStartDesktop(t *testing.T) {
	mux := http.NewServeMux()
	desktopRoutes(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/desktops/task-probe-regression/health?probe=1", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing desktop probe: %d %s", recorder.Code, recorder.Body.String())
	}
}
