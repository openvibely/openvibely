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
	for _, delay := range []bool{false, true} {
		t.Run(fmt.Sprintf("delayed-output=%t", delay), func(t *testing.T) {
			dir := t.TempDir()
			m := agenttools.NewProcessManager(func(ctx context.Context, command, directory string) (*exec.Cmd, error) {
				// Exercise a child that produces no output during the initial 250ms yield.
				if delay {
					command = "sleep 0.4; " + command
				}
				return shellCommand(ctx, command, directory)
			})
			defer m.Close()
			tooltest.Processes(t, func(ctx context.Context, name string, input json.RawMessage) (string, error) {
				return m.Execute(ctx, dir, name, input)
			})
		})
	}
}

func TestComputerProcessHTTPUsesSharedManager(t *testing.T) {
	t.Setenv("OPENVIBELY_DESKTOP_ID", "task-process-contract")
	temporaryComputerHome(t)
	m := agenttools.NewProcessManager(func(ctx context.Context, command, dir string) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/bash", "-c", command), nil
	})
	defer m.Close()
	mux := http.NewServeMux()
	processRoutes(mux, m)
	execute := func(ctx context.Context, name string, input json.RawMessage) (string, error) {
		body, _ := json.Marshal(map[string]any{"name": name, "input": input, "execution_id": "contract"})
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

	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("command ran outside isolation")
	}
}
