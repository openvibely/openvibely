//go:build linux

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDesktopIdleProtectsRequestsAndNever(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name         string
		age, timeOut time.Duration
		requests     int
		want         bool
	}{
		{"expired", 31 * time.Minute, 30 * time.Minute, 0, true},
		{"recent", 29 * time.Minute, 30 * time.Minute, 0, false},
		{"viewer", time.Hour, 30 * time.Minute, 1, false},
		{"tool", time.Hour, 30 * time.Minute, 2, false},
		{"never", time.Hour, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &desktopProcess{lastActivity: now.Add(-tc.age), requests: tc.requests}
			if got := desktopIdleEligible(p, now, tc.timeOut); got != tc.want {
				t.Fatalf("eligible=%v want %v", got, tc.want)
			}
		})
	}
}

func TestReapingOneDesktopDoesNotHoldTheSharedLock(t *testing.T) {
	var mu sync.Mutex
	old := time.Now().Add(-time.Hour)
	a := &desktopProcess{done: make(chan struct{}), lastActivity: old}
	b := &desktopProcess{done: make(chan struct{}), lastActivity: old}
	sessions := map[string]*desktopProcess{"task-a": a, "task-b": b}
	probing, finish := make(chan struct{}), make(chan bool)
	desktopJobsIdleFn = func(string) (bool, time.Time) {
		close(probing)
		return <-finish, time.Time{}
	}
	t.Cleanup(func() { desktopJobsIdleFn = desktopJobsIdle })

	result := make(chan bool)
	go func() { result <- reapDesktop(&mu, sessions, "task-a", time.Minute) }()
	<-probing
	locked := make(chan struct{})
	go func() { mu.Lock(); b.requests++; mu.Unlock(); close(locked) }()
	select {
	case <-locked:
	case <-time.After(time.Second):
		t.Fatal("a request for another desktop waited on cleanup")
	}
	mu.Lock()
	claimed := a.reaping != nil
	mu.Unlock()
	if !claimed {
		t.Fatal("the desktop being checked is not claimed")
	}

	close(a.done)
	finish <- true
	if !<-result {
		t.Fatal("idle desktop was not stopped")
	}
	if sessions["task-a"] != nil || a.reaping != nil {
		t.Fatal("stopped desktop was not removed and released")
	}
	if sessions["task-b"] != b {
		t.Fatal("other desktop was touched")
	}
}

func TestReapReleasesDesktopThatIsStillBusy(t *testing.T) {
	var mu sync.Mutex
	p := &desktopProcess{done: make(chan struct{}), lastActivity: time.Now().Add(-time.Hour)}
	sessions := map[string]*desktopProcess{"task-a": p}
	desktopJobsIdleFn = func(string) (bool, time.Time) { return false, time.Time{} }
	t.Cleanup(func() { desktopJobsIdleFn = desktopJobsIdle })
	if reapDesktop(&mu, sessions, "task-a", time.Minute) {
		t.Fatal("busy desktop was stopped")
	}
	if sessions["task-a"] != p || p.reaping != nil {
		t.Fatal("busy desktop was not kept and released")
	}
}

func TestJobsPollingDoesNotStartDesktop(t *testing.T) {
	mux := http.NewServeMux()
	desktopRoutes(mux)
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/desktops/task-unused/jobs", nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"jobs":[]`) {
			t.Fatalf("poll attempted desktop startup: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/desktop-sessions", nil))
	if strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatal(rec.Body.String())
	}
}

func TestTargetedReapPreservesOtherIdleClocks(t *testing.T) {
	var mu sync.Mutex
	old := time.Now().Add(-time.Hour)
	a := &desktopProcess{done: make(chan struct{}), lastActivity: old, requests: 1}
	b := &desktopProcess{done: make(chan struct{}), lastActivity: old, requests: 1}
	protected := &desktopProcess{done: make(chan struct{}), lastActivity: old}
	sessions := map[string]*desktopProcess{"task-a": a, "task-b": b, "task-protected": protected}
	mux := http.NewServeMux()
	registerDesktopReaper(mux, &mu, sessions)
	for _, path := range []string{"/desktop-idle-protection", "/desktop-reap-one"} {
		candidates := `["task-a","task-b"]`
		if path == "/desktop-reap-one" {
			candidates = `["task-a"]`
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(`{"idle_seconds":60,"candidates":`+candidates+`}`)))
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
		if !b.lastActivity.Equal(old) {
			t.Fatal("unrelated candidate's idle clock was reset")
		}
	}
	if !protected.lastActivity.After(old) {
		t.Fatal("protected desktop's idle clock was not refreshed")
	}
}
