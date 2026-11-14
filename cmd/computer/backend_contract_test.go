//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"github.com/openvibely/openvibely/pkg/agenttools/tooltest"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputerFileContract(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	tooltest.Files(t, func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		var p nativeFileRequest
		p.Operation = name
		if err := json.Unmarshal(input, &p.Input); err != nil {
			return "", err
		}
		return nativeFileOperation(ctx, root, p)
	})
}
func TestComputerProcessContract(t *testing.T) {
	dir := t.TempDir()
	m := agenttools.NewProcessManager(shellCommand)
	defer m.Close()
	tooltest.Processes(t, func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		return m.Execute(ctx, dir, name, input)
	})
}

func TestComputerProcessHTTPUsesSharedManager(t *testing.T) {
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-process-contract")
	home := computerHome()
	if err := os.MkdirAll(filepath.Join(home, "Files"), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	m := agenttools.NewProcessManager(func(ctx context.Context, command, dir string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/bash", "-c", command), nil
	})
	defer m.Close()
	mux := http.NewServeMux()
	processRoutes(mux, m)
	execute := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		body, _ := json.Marshal(map[string]any{"name": name, "input": input})
		r := httptest.NewRequest("POST", "/process-tools", bytes.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			return "", fmt.Errorf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var result struct{ Output, Error string }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			return "", err
		}
		if result.Error != "" {
			return result.Output, errors.New(result.Error)
		}
		return result.Output, nil
	}
	tooltest.Processes(t, execute)
	// A job started through the explicit entry point can be controlled by process.
	job, err := m.StartJob(context.Background(), filepath.Join(home, "Files"), "read -r x; printf 'explicit:%s' \"$x\"", 10)
	if err != nil {
		t.Fatal(err)
	}
	id := job.Snapshot()["job_id"].(string)
	data, _ := json.Marshal(map[string]any{"session_id": id, "input": "input\n", "close_stdin": true})
	out, err := execute(t.Context(), "process", data)
	if err != nil || !strings.Contains(out, "explicit:input") {
		t.Fatalf("%s: %v", out, err)
	}
}

func TestSharedSessionsCannotBypassComputerIsolation(t *testing.T) {
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-closed")
	t.Setenv("OPENVIBELY_ISOLATED", "")
	manager := agenttools.NewProcessManager(shellCommand)
	defer manager.Close()
	marker := filepath.Join(t.TempDir(), "must-not-exist")
	input, _ := json.Marshal(map[string]any{"command": "touch " + marker})
	if _, err := manager.Execute(t.Context(), t.TempDir(), "shell", input); err == nil {
		t.Fatal("shell bypassed isolation")
	}
	if _, err := manager.StartJob(t.Context(), t.TempDir(), "touch "+marker, 10); err == nil {
		t.Fatal("explicit job bypassed isolation")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command ran outside isolation")
	}
}
