package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func resetSharedStateForTests() {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	for key, entry := range sharedServers {
		if entry.srv != nil {
			entry.srv.Close()
		}
		delete(sharedServers, key)
	}
}

func TestNewMCPManager_PartialServerFailureStillReturnsWorkingTools(t *testing.T) {
	resetSharedStateForTests()
	orig := startServerFn
	defer func() { startServerFn = orig }()

	startServerFn = func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		if cfg.Name == "github" {
			return nil, errors.New("401 missing auth")
		}
		return &MCPServer{
			name: cfg.Name,
			tools: []MCPTool{
				{
					ServerName: cfg.Name,
					Name:       "browser_navigate",
					PrefixName: cfg.Name + "__browser_navigate",
					Schema:     json.RawMessage(`{"type":"object"}`),
					Desc:       "navigate browser",
				},
			},
		}, nil
	}

	manager, err := NewMCPManager(context.Background(), []models.MCPServerConfig{
		{Name: "github", URL: "https://example.invalid/mcp"},
		{Name: "playwright", Command: []string{"npx", "-y", "@playwright/mcp@latest"}},
	}, ".")
	if err != nil {
		t.Fatalf("expected partial success manager, got error: %v", err)
	}
	defer manager.Close()

	defs := manager.ToolDefinitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 MCP tool definition, got %d", len(defs))
	}
	if defs[0].Name != "playwright__browser_navigate" {
		t.Fatalf("unexpected tool definition name: %s", defs[0].Name)
	}
}

func TestNewMCPManager_AllServerFailuresReturnsError(t *testing.T) {
	resetSharedStateForTests()
	orig := startServerFn
	defer func() { startServerFn = orig }()

	startServerFn = func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		return nil, errors.New("startup failed")
	}

	_, err := NewMCPManager(context.Background(), []models.MCPServerConfig{
		{Name: "github", URL: "https://example.invalid/mcp"},
		{Name: "playwright", Command: []string{"npx", "-y", "@playwright/mcp@latest"}},
	}, ".")
	if err == nil {
		t.Fatal("expected error when all MCP servers fail")
	}
}

func TestEnsurePersistentServers_TracksRuntimeFailures(t *testing.T) {
	resetSharedStateForTests()
	orig := startServerFn
	defer func() { startServerFn = orig }()

	startServerFn = func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		if cfg.Name == "playwright" {
			return nil, errors.New("exec: npx not found")
		}
		return &MCPServer{name: cfg.Name}, nil
	}

	err := EnsurePersistentServers(context.Background(), []models.MCPServerConfig{
		{Name: "playwright", Command: []string{"npx", "@playwright/mcp"}},
		{Name: "github", URL: "https://example.invalid/mcp"},
	}, ".")
	if err == nil {
		t.Fatal("expected partial startup error")
	}

	runtime := PersistentRuntimeState()
	if len(runtime) != 2 {
		t.Fatalf("expected 2 runtime entries, got %d", len(runtime))
	}

	foundFailed := false
	foundRunning := false
	for _, r := range runtime {
		if r.Name == "playwright" && r.Status == "failed" && r.Error != "" {
			foundFailed = true
		}
		if r.Name == "github" && r.Status == "running" {
			foundRunning = true
		}
	}
	if !foundFailed {
		t.Fatalf("expected failed runtime entry for playwright, got %#v", runtime)
	}
	if !foundRunning {
		t.Fatalf("expected running runtime entry for github, got %#v", runtime)
	}
}

func TestReconcilePersistentServers_RemovesDisabledServer(t *testing.T) {
	resetSharedStateForTests()
	orig := startServerFn
	defer func() { startServerFn = orig }()

	startServerFn = func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		return &MCPServer{name: cfg.Name}, nil
	}

	if err := EnsurePersistentServers(context.Background(), []models.MCPServerConfig{
		{Name: "playwright", Command: []string{"npx", "@playwright/mcp"}},
		{Name: "github", URL: "https://example.invalid/mcp"},
	}, "."); err != nil {
		t.Fatalf("seed persistent servers: %v", err)
	}

	if err := ReconcilePersistentServers(context.Background(), []models.MCPServerConfig{
		{Name: "github", URL: "https://example.invalid/mcp"},
	}, "."); err != nil {
		t.Fatalf("reconcile persistent servers: %v", err)
	}

	runtime := PersistentRuntimeState()
	if len(runtime) != 1 {
		t.Fatalf("expected 1 runtime entry after reconcile, got %d (%#v)", len(runtime), runtime)
	}
	if runtime[0].Name != "github" {
		t.Fatalf("expected github runtime entry to remain, got %#v", runtime[0])
	}
}
