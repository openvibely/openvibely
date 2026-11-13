package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/update"
)

func TestServerInvalidInputsFatalBeforeHelper(t *testing.T) {
	tests := []struct {
		name         string
		inputFailure string
		envName      string
		value        string
		want         string
	}{
		{name: "arguments", inputFailure: "arguments", want: "invalid executable-update-helper arguments"},
		{name: "metadata", inputFailure: "metadata", want: "relaunch metadata"},
		{name: "wait timeout", envName: "OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS", value: "not-a-duration", want: "parse update integration wait timeout"},
		{name: "validation timeout", envName: "OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS", value: "not-a-duration", want: "parse update integration validation timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			metadata, err := json.Marshal(map[string]any{
				"arguments":         []string{"fixture"},
				"working_directory": root,
			})
			if err != nil {
				t.Fatal(err)
			}
			helperArgs := []string{
				update.ExecutableUpdateHelperCommand,
				"--parent-pid", "99999999",
				"--current", filepath.Join(root, "current"),
				"--staged", filepath.Join(root, "current.openvibely-new"),
				"--backup", filepath.Join(root, "current.openvibely-backup"),
				"--health-url", "http://127.0.0.1:1/health",
				"--expected-version", "0.6.0",
				"--previous-version", "0.5.0",
				"--outcome-id", "server-invalid-timeout",
			}
			if test.inputFailure == "arguments" {
				helperArgs = append(helperArgs, "--invalid")
			}
			if test.inputFailure == "metadata" {
				metadata = []byte(`{}`)
			}
			args, err := json.Marshal(helperArgs)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=TestServerInvalidTimeoutHelperProcess")
			cmd.Env = append(os.Environ(),
				"OPENVIBELY_SERVER_INVALID_TIMEOUT_HELPER=1",
				"OPENVIBELY_SERVER_HELPER_ARGS="+string(args),
				"OPENVIBELY_UPDATE_INTEGRATION_WAIT_TIMEOUT_MS=",
				"OPENVIBELY_UPDATE_INTEGRATION_VALIDATION_TIMEOUT_MS=",
				"OPENVIBELY_UPDATE_INTEGRATION_HELPER_LOG=1",
			)
			if test.envName != "" {
				cmd.Env = append(cmd.Env, test.envName+"="+test.value)
			}
			cmd.Stdin = strings.NewReader(string(metadata))
			output, runErr := cmd.CombinedOutput()
			if runErr == nil {
				t.Fatal("server helper unexpectedly exited successfully")
			}
			if !strings.Contains(string(output), test.want) {
				t.Fatalf("server fatal output = %q, want %q", output, test.want)
			}
			if strings.Contains(string(output), "[update-helper] started") {
				t.Fatalf("server helper started after invalid timeout: %q", output)
			}
			if strings.Contains(string(output), "timed out waiting for parent process to exit") {
				t.Fatalf("server helper ran after invalid timeout: %q", output)
			}
		})
	}
}

func TestServerInvalidTimeoutHelperProcess(t *testing.T) {
	if os.Getenv("OPENVIBELY_SERVER_INVALID_TIMEOUT_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("OPENVIBELY_SERVER_HELPER_ARGS")), &args); err != nil {
		t.Fatal(err)
	}
	os.Args = append([]string{"openvibely-server-test"}, args...)
	main()
}

func TestWaitForShutdownAcceptsBinaryUpdateHandoff(t *testing.T) {
	requested := make(chan struct{})
	close(requested)
	done := make(chan struct{})
	go func() {
		waitForShutdown(make(chan os.Signal), requested)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("binary update shutdown handoff did not stop command wait")
	}
}

func TestRunHealthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system/health" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if err := runHealthcheck(srv.URL+"/api/system/health", srv.Client()); err != nil {
		t.Fatal(err)
	}
}

func TestServerPackagedHelperCommandRouting(t *testing.T) {
	for _, args := range [][]string{
		{"server"}, {"server", "serve"}, {"server", "healthcheck"},
		{"server", update.AppBundleUpdateHelperCommand, "--invalid"},
	} {
		handled, err := runPackagedUpdateHelperCommand(context.Background(), args, nil)
		if handled || err != nil {
			t.Fatalf("args %v: handled=%t err=%v", args, handled, err)
		}
	}
	handled, err := runPackagedUpdateHelperCommand(context.Background(), []string{"server", update.ExecutableUpdateHelperCommand, "--invalid"}, nil)
	if !handled || err == nil {
		t.Fatalf("executable helper: handled=%t err=%v", handled, err)
	}
}
