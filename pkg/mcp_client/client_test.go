package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

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

func TestHTTPMCPManagerExecutesToolAndPropagatesServerErrors(t *testing.T) {
	resetSharedStateForTests()
	defer resetSharedStateForTests()

	var mu sync.Mutex
	seenMethods := []string{}
	seenAuth := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s, want POST", r.Method)
		}
		if got := r.Header.Get("X-MCP-Token"); got != "secret-token" {
			t.Fatalf("missing MCP header, got %q", got)
		}
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode JSON-RPC request: %v", err)
		}
		mu.Lock()
		seenMethods = append(seenMethods, req.Method)
		seenAuth = append(seenAuth, r.Header.Get("X-MCP-Token"))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"tools":[{"name":"echo","description":"echo text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}`)})
		case "tools/call":
			params, ok := req.Params.(map[string]interface{})
			if !ok || params["name"] != "echo" {
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32602, Message: "unknown tool"}})
				return
			}
			args, _ := params["arguments"].(map[string]interface{})
			if args["fail"] == true {
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32000, Message: "tool failed"}})
				return
			}
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"first line"},{"type":"text","text":"second line"}],"isError":false}`)})
		default:
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonRPCError{Code: -32601, Message: "missing method"}})
		}
	}))
	defer srv.Close()

	manager, err := NewMCPManager(context.Background(), []models.MCPServerConfig{{
		Name:    "tools",
		Type:    "http",
		URL:     srv.URL,
		Headers: map[string]string{"X-MCP-Token": "secret-token"},
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer manager.Close()

	defs := manager.ToolDefinitions()
	if len(defs) != 1 || defs[0].Name != "tools__echo" || defs[0].Description != "[MCP:tools] echo text" {
		t.Fatalf("unexpected tool definitions: %#v", defs)
	}
	if !manager.IsMCPTool("tools__echo") || manager.IsMCPTool("tools__missing") {
		t.Fatalf("MCP tool detection mismatch")
	}
	out, isErr, err := manager.ExecuteTool(context.Background(), "tools__echo", map[string]interface{}{"text": "hello"})
	if err != nil || isErr || out != "first line\nsecond line" {
		t.Fatalf("ExecuteTool output=%q isErr=%v err=%v", out, isErr, err)
	}
	_, isErr, err = manager.ExecuteTool(context.Background(), "tools__echo", map[string]interface{}{"fail": true})
	if err == nil || !isErr || err.Error() != "MCP error -32000: tool failed" {
		t.Fatalf("expected JSON-RPC error, isErr=%v err=%v", isErr, err)
	}
	_, isErr, err = manager.ExecuteTool(context.Background(), "tools__missing", nil)
	if err == nil || !isErr || err.Error() != "unknown MCP tool: tools__missing" {
		t.Fatalf("expected unknown tool error, isErr=%v err=%v", isErr, err)
	}

	mu.Lock()
	methods := append([]string(nil), seenMethods...)
	authHeaders := append([]string(nil), seenAuth...)
	mu.Unlock()
	if len(methods) < 4 || methods[0] != "initialize" || methods[1] != "tools/list" {
		t.Fatalf("unexpected method sequence: %#v", methods)
	}
	for _, auth := range authHeaders {
		if auth != "secret-token" {
			t.Fatalf("auth header was not propagated on every call: %#v", authHeaders)
		}
	}
}

func TestHTTPMCPToolCallStopsWhenCallerContextIsCanceled(t *testing.T) {
	resetSharedStateForTests()
	defer resetSharedStateForTests()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode JSON-RPC request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"tools":[{"name":"slow","description":"blocks","inputSchema":{"type":"object"}}]}`)})
		case "tools/call":
			// Simulate a server that keeps working until the client gives up.
			<-r.Context().Done()
		}
	}))
	defer srv.Close()

	manager, err := NewMCPManager(context.Background(), []models.MCPServerConfig{{
		Name: "tools",
		Type: "http",
		URL:  srv.URL,
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		_, _, err := manager.ExecuteTool(ctx, "tools__slow", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled from ExecuteTool, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ExecuteTool did not return after the caller context was canceled")
	}
}

func TestHTTPMCPToolCallIsNotSentWhenCallerContextAlreadyCanceled(t *testing.T) {
	resetSharedStateForTests()
	defer resetSharedStateForTests()

	var mu sync.Mutex
	toolCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode JSON-RPC request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"tools":[{"name":"echo","description":"echo","inputSchema":{"type":"object"}}]}`)})
		case "tools/call":
			mu.Lock()
			toolCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)})
		}
	}))
	defer srv.Close()

	manager, err := NewMCPManager(context.Background(), []models.MCPServerConfig{{
		Name: "tools",
		Type: "http",
		URL:  srv.URL,
	}}, t.TempDir())
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer manager.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, isErr, err := manager.ExecuteTool(ctx, "tools__echo", nil)
	if !errors.Is(err, context.Canceled) || !isErr {
		t.Fatalf("expected context.Canceled with isErr, got isErr=%v err=%v", isErr, err)
	}

	mu.Lock()
	defer mu.Unlock()
	if toolCalls != 0 {
		t.Fatalf("tools/call reached the server %d times after cancellation", toolCalls)
	}
}

func TestHTTPMCPConcurrentToolCalls(t *testing.T) {
	for _, cancelSecond := range []bool{false, true} {
		name := "second_completes_while_first_blocked"
		if cancelSecond {
			name = "cancel_second_without_canceling_first"
		}
		t.Run(name, func(t *testing.T) {
			firstStarted := make(chan int64, 1)
			secondStarted := make(chan int64, 1)
			releaseFirst := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req jsonRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				params, _ := req.Params.(map[string]interface{})
				if params["name"] == "first" {
					firstStarted <- req.ID
					select {
					case <-releaseFirst:
					case <-r.Context().Done():
						return
					}
				} else {
					secondStarted <- req.ID
					if cancelSecond {
						<-r.Context().Done()
						return
					}
				}
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[{"type":"text","text":"ok"}]}`)})
			}))
			defer srv.Close()
			firstCtx, cancelFirst := context.WithCancel(context.Background())
			defer cancelFirst()
			secondCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := &MCPServer{kind: "http", url: srv.URL, httpClient: &http.Client{Timeout: 60 * time.Second}}
			execute := func(ctx context.Context, tool string) <-chan error {
				done := make(chan error, 1)
				go func() {
					out, isErr, err := server.Execute(ctx, tool, nil)
					if err == nil && (out != "ok" || isErr) {
						t.Errorf("%s: output=%q isErr=%v", tool, out, isErr)
					}
					done <- err
				}()
				return done
			}
			awaitStart := func(started <-chan int64) int64 {
				t.Helper()
				select {
				case id := <-started:
					return id
				case <-time.After(5 * time.Second):
					t.Fatal("call did not reach server while first call was blocked")
					return 0
				}
			}
			awaitResult := func(done <-chan error, want error) {
				t.Helper()
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("call error=%v, want %v", err, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("call did not return promptly")
				}
			}
			firstDone := execute(firstCtx, "first")
			firstID := awaitStart(firstStarted)
			secondDone := execute(secondCtx, "second")
			secondID := awaitStart(secondStarted)
			if firstID == secondID || firstID == 0 || secondID == 0 {
				t.Fatalf("invalid request IDs: %d, %d", firstID, secondID)
			}
			var want error
			if cancelSecond {
				cancel()
				want = context.Canceled
			}
			awaitResult(secondDone, want)
			select {
			case err := <-firstDone:
				t.Fatalf("first call returned before release: %v", err)
			default:
			}
			close(releaseFirst)
			awaitResult(firstDone, nil)
		})
	}
}

func TestHTTPMCPRetriesSameRPCRequest(t *testing.T) {
	calls := 0
	var original string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls++
		if calls == 1 {
			original = string(body)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if string(body) != original || r.Header.Get("X-MCP-Token") != "fixture" {
			t.Error("retry changed RPC identity/body/auth")
		}
		var request jsonRPCRequest
		_ = json.Unmarshal(body, &request)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{"ok":true}`)})
	}))
	defer srv.Close()
	server := &MCPServer{kind: "http", url: srv.URL, httpClient: srv.Client(), headers: map[string]string{"X-MCP-Token": "fixture"}}
	_, err := server.call(context.Background(), "tools/list", nil)
	if err != nil || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
