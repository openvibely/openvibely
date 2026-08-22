package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerAgentInstallerCancelValidateRollbackAndCapabilities(t *testing.T) {
	ctx := context.Background()
	var cancelPath, cancelKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/capabilities":
			_ = json.NewEncoder(w).Encode(DockerCapabilities{SchemaVersion: 1, ManagedUpdates: true})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/update-requests/request-1/cancel":
			cancelPath = r.URL.Path
			cancelKey = r.Header.Get("Idempotency-Key")
			if cancelKey == "" {
				t.Fatalf("cancel request missing idempotency key")
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Docker agent request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	api, err := NewAgentHTTPClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatalf("NewAgentHTTPClient: %v", err)
	}

	installer := &DockerAgentInstaller{API: api, StatePath: filepath.Join(t.TempDir(), "docker-state.json")}
	if caps, err := installer.Capabilities(ctx); err != nil || !caps.ManagedUpdates {
		t.Fatalf("Capabilities = %#v, %v", caps, err)
	}
	if err := installer.Cancel(ctx); err != nil {
		t.Fatalf("empty Cancel should be no-op: %v", err)
	}
	installer.state = dockerPersistentState{RequestID: "request-1", Status: "accepted"}
	if err := installer.Cancel(ctx); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelPath != "/v1/update-requests/request-1/cancel" {
		t.Fatalf("unexpected cancel path %q", cancelPath)
	}
	if installer.state.Status != "cancelled" || installer.state.CancelIdempotencyKey != cancelKey {
		t.Fatalf("cancel did not persist state/key: %#v key=%q", installer.state, cancelKey)
	}
	data, err := os.ReadFile(installer.StatePath)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !strings.Contains(string(data), `"status":"cancelled"`) || !strings.Contains(string(data), cancelKey) {
		t.Fatalf("cancel state not written: %s", data)
	}
	installer.state = dockerPersistentState{Status: "succeeded", TargetVersion: "2.0.0", ReportedTargetVersion: "2.0.0", ReportedCurrentVersion: "2.0.0"}
	if err := installer.Validate(ctx, ReleaseMetadata{Version: "2.0.0"}); err != nil {
		t.Fatalf("Validate succeeded state: %v", err)
	}
	if err := installer.Validate(ctx, ReleaseMetadata{Version: "2.1.0"}); err == nil || !strings.Contains(err.Error(), "different version") {
		t.Fatalf("expected version mismatch validation error, got %v", err)
	}
	if err := installer.Rollback(ctx, nil); err == nil || !strings.Contains(err.Error(), "owns rollback") {
		t.Fatalf("expected rollback ownership error, got %v", err)
	}
	installer.state.Status = "rolled_back"
	if err := installer.Rollback(ctx, nil); err != nil {
		t.Fatalf("rolled back Rollback should be no-op: %v", err)
	}
}

func TestDockerAgentCapabilitiesRejectUnsupportedSchema(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(DockerCapabilities{SchemaVersion: 2, ManagedUpdates: true})
	}))
	defer server.Close()
	api, err := NewAgentHTTPClient(server.URL, "token", server.Client())
	if err != nil {
		t.Fatalf("NewAgentHTTPClient: %v", err)
	}
	installer := &DockerAgentInstaller{API: api}
	caps, err := installer.Capabilities(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported Docker agent capabilities schema") {
		t.Fatalf("expected unsupported schema error, got caps=%#v err=%v", caps, err)
	}
}
