//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

func TestRemoteExecutionCancellation(t *testing.T) {
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-execution-test")
	if err := os.MkdirAll(filepath.Join(computerHome(), "Files"), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(computerHome())
	manager := agenttools.NewProcessManager(func(ctx context.Context, command, dir string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", command), nil
	})
	defer manager.Close()
	mux := http.NewServeMux()
	processRoutes(mux, manager)
	post := func(path string, p any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(p)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, bytes.NewReader(data)))
		return w
	}
	start := func(id string, yield int) *httptest.ResponseRecorder {
		return post("/process-tools", map[string]any{"execution_id": id, "name": "shell", "input": map[string]any{"command": "sleep 30", "yield_time_ms": yield}})
	}
	cancel := func(id string) {
		t.Helper()
		w := post("/executions/cancel", map[string]string{"execution_id": id})
		if w.Code != 200 {
			t.Fatalf("cancel: %d %s", w.Code, w.Body.String())
		}
	}
	// Cancellation can beat the first request to the server. Never revive that ID.
	cancel("late")
	if w := start("late", 250); w.Code != 409 {
		t.Fatalf("late request accepted: %d", w.Code)
	}
	for _, id := range []string{"a", "b"} {
		if w := start(id, 250); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if jobs := manager.Jobs(""); len(jobs) != 2 {
		t.Fatal("expected two jobs")
	}
	cancel("a")
	running := 0
	for _, job := range manager.Jobs("") {
		if job.Snapshot()["running"] == true {
			running++
		}
	}
	if running != 1 {
		t.Fatalf("cancel affected another execution: %d running", running)
	}
	cancel("a") // idempotent
	cancel("b")
	// Cancel while the HTTP shell call still waits, before it returns a session ID.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- start("waiting", 10000) }()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		active := false
		for _, job := range manager.Jobs("") {
			if job.Snapshot()["running"] == true {
				active = true
			}
		}
		if active {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("no waiting job")
		}
	}
	cancel("waiting")
	select {
	case w := <-done:
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shell request did not finish")
	}
	for _, job := range manager.Jobs("") {
		if job.Snapshot()["running"] == true {
			t.Fatal("process survived acknowledged cancellation")
		}
	}
	// An individual request ending must not cancel the execution-owned process.
	requestCtx, endRequest := context.WithCancel(context.Background())
	defer endRequest()
	data := []byte(`{"execution_id":"request-only","name":"shell","input":{"command":"sleep 30","yield_time_ms":250}}`)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("POST", "/process-tools", bytes.NewReader(data)).WithContext(requestCtx))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	endRequest()
	active := false
	for _, job := range manager.Jobs("") {
		if job.Snapshot()["running"] == true {
			select {
			case <-job.Done():
				t.Fatal("request cancellation killed session")
			case <-time.After(50 * time.Millisecond):
				active = true
			}
		}
	}
	if !active {
		t.Fatal("request cancellation killed session")
	}
	cancel("request-only")
}
