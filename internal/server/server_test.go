package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/config"
)

func TestStart_BootstrapAndShutdown(t *testing.T) {
	// Smoke-test: start the full server with an in-memory-style temp DB and
	// an ephemeral port, hit a core route, then shut down gracefully.
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Mode:            config.ModeDesktop,
		Port:            "0", // ephemeral port
		DatabasePath:    tmpDir + "/test.db",
		ProjectRepoRoot: tmpDir + "/repos",
		Environment:     "test",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer inst.Shutdown()

	if inst.BoundAddr == "" {
		t.Fatal("expected non-empty BoundAddr")
	}
	if inst.BaseURL == "" {
		t.Fatal("expected non-empty BaseURL")
	}

	// Hit a core route to verify the server is serving.
	client := &http.Client{Timeout: 5 * time.Second}

	// The root should redirect to /chat
	resp, err := client.Get(inst.BaseURL + "/")
	if err != nil {
		t.Fatalf("GET / failed: %v", err)
	}
	resp.Body.Close()
	// Accept redirect (302) or the final page (200).
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
		t.Fatalf("GET / returned %d, expected 200 or 302", resp.StatusCode)
	}

	// Swagger spec should be reachable.
	resp, err = client.Get(inst.BaseURL + "/swagger/doc.json")
	if err != nil {
		t.Fatalf("GET /swagger/doc.json failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /swagger/doc.json returned %d, expected 200", resp.StatusCode)
	}

	// Graceful shutdown.
	inst.Shutdown()
}

func TestStart_ServerModeDefaults(t *testing.T) {
	// Verify existing server mode still works with explicit port.
	tmpDir := t.TempDir()
	cfg := &config.Config{
		Mode:            config.ModeServer,
		Port:            "0",
		DatabasePath:    tmpDir + "/test.db",
		ProjectRepoRoot: tmpDir + "/repos",
		Environment:     "test",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	inst, err := Start(ctx, cfg)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	defer inst.Shutdown()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(inst.BaseURL + "/models")
	if err != nil {
		t.Fatalf("GET /models failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /models returned %d, expected 200", resp.StatusCode)
	}
}
