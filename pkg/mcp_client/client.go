package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	anthropicclient "github.com/openvibely/openvibely/pkg/anthropic_client"
)

// MCPServer represents a running MCP server subprocess.
type MCPServer struct {
	name       string
	kind       string // stdio or http/sse/ws (request/response over HTTP POST)
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     *bufio.Reader
	url        string
	headers    map[string]string
	httpClient *http.Client
	mu         sync.Mutex
	nextID     atomic.Int64
	tools      []MCPTool
}

// MCPTool is a tool exposed by an MCP server.
type MCPTool struct {
	ServerName string
	Name       string          // original name from the server
	PrefixName string          // server__name for disambiguation
	Schema     json.RawMessage // JSON Schema input_schema
	Desc       string
}

// jsonRPCRequest is a JSON-RPC 2.0 request.
type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolsListResult is the result from tools/list.
type toolsListResult struct {
	Tools []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	} `json:"tools"`
}

// toolsCallResult is the result from tools/call.
type toolsCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// MCPManager manages multiple MCP server connections.
type MCPManager struct {
	servers []managedServerRef
}

var startServerFn = startServer

type managedServerRef struct {
	key string
	cfg models.MCPServerConfig
	srv *MCPServer
}

type sharedServerEntry struct {
	cfg        models.MCPServerConfig
	srv        *MCPServer
	refs       int
	persistent bool
	lastErr    string
	updatedAt  time.Time
}

var (
	sharedMu      sync.Mutex
	sharedServers = map[string]*sharedServerEntry{}
)

// NewMCPManager creates a manager and starts all configured MCP servers.
func NewMCPManager(ctx context.Context, configs []models.MCPServerConfig, workDir string) (*MCPManager, error) {
	m := &MCPManager{}
	var errs []string
	for _, cfg := range configs {
		key := serverConfigKey(cfg)
		srv, err := acquireServer(ctx, cfg, workDir, false)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", cfg.Name, err))
			log.Printf("[mcp] skipping server %q: %v", cfg.Name, err)
			continue
		}
		m.servers = append(m.servers, managedServerRef{
			key: key,
			cfg: cfg,
			srv: srv,
		})
	}

	if len(m.servers) == 0 && len(errs) > 0 {
		return nil, fmt.Errorf("starting MCP servers failed: %s", strings.Join(errs, " | "))
	}
	if len(errs) > 0 {
		log.Printf("[mcp] started %d/%d MCP servers (%d skipped)", len(m.servers), len(configs), len(errs))
	}
	return m, nil
}

// EnsurePersistentServers starts MCP servers in persistent mode so sessions can reuse them.
func EnsurePersistentServers(ctx context.Context, configs []models.MCPServerConfig, workDir string) error {
	var errs []string
	started := 0
	for _, cfg := range configs {
		_, err := acquireServer(ctx, cfg, workDir, true)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", cfg.Name, err))
			log.Printf("[mcp] persistent server %q failed: %v", cfg.Name, err)
			continue
		}
		started++
		releaseServer(cfg)
	}
	if started == 0 && len(errs) > 0 {
		return fmt.Errorf("starting persistent MCP servers failed: %s", strings.Join(errs, " | "))
	}
	if len(errs) > 0 {
		return fmt.Errorf("partial persistent MCP startup: %s", strings.Join(errs, " | "))
	}
	return nil
}

// ReconcilePersistentServers ensures exactly the provided persistent MCP servers are active.
// Servers not present in configs are removed from persistent mode and stopped when unreferenced.
func ReconcilePersistentServers(ctx context.Context, configs []models.MCPServerConfig, workDir string) error {
	desired := make(map[string]models.MCPServerConfig, len(configs))
	ordered := make([]models.MCPServerConfig, 0, len(configs))
	for _, cfg := range configs {
		key := serverConfigKey(cfg)
		if _, exists := desired[key]; exists {
			continue
		}
		desired[key] = cfg
		ordered = append(ordered, cfg)
	}

	var errs []string
	started := 0
	for _, cfg := range ordered {
		_, err := acquireServer(ctx, cfg, workDir, true)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", cfg.Name, err))
			log.Printf("[mcp] reconcile persistent server %q failed: %v", cfg.Name, err)
			continue
		}
		started++
		releaseServer(cfg)
	}

	var toClose []*MCPServer
	now := time.Now().UTC()
	sharedMu.Lock()
	for key, entry := range sharedServers {
		if !entry.persistent {
			continue
		}
		if _, keep := desired[key]; keep {
			continue
		}
		entry.persistent = false
		entry.updatedAt = now
		if entry.refs == 0 && entry.srv != nil {
			toClose = append(toClose, entry.srv)
			entry.srv = nil
		}
		if entry.refs == 0 && entry.srv == nil {
			delete(sharedServers, key)
		}
	}
	sharedMu.Unlock()

	for _, srv := range toClose {
		srv.Close()
	}

	if len(ordered) > 0 && started == 0 && len(errs) > 0 {
		return fmt.Errorf("reconciling persistent MCP servers failed: %s", strings.Join(errs, " | "))
	}
	if len(errs) > 0 {
		return fmt.Errorf("partial persistent MCP reconcile: %s", strings.Join(errs, " | "))
	}
	return nil
}

// PersistentRuntimeState returns MCP server runtime status snapshots for UI display.
func PersistentRuntimeState() []models.PluginRuntimeMCP {
	sharedMu.Lock()
	defer sharedMu.Unlock()

	out := make([]models.PluginRuntimeMCP, 0, len(sharedServers))
	for _, entry := range sharedServers {
		if !entry.persistent {
			continue
		}
		status := "stopped"
		toolCount := 0
		if entry.srv != nil {
			status = "running"
			toolCount = len(entry.srv.tools)
		} else if strings.TrimSpace(entry.lastErr) != "" {
			status = "failed"
		}
		out = append(out, models.PluginRuntimeMCP{
			Name:      strings.TrimSpace(entry.cfg.Name),
			Status:    status,
			Error:     strings.TrimSpace(entry.lastErr),
			ToolCount: toolCount,
			UpdatedAt: entry.updatedAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

func acquireServer(ctx context.Context, cfg models.MCPServerConfig, workDir string, persistent bool) (*MCPServer, error) {
	key := serverConfigKey(cfg)

	sharedMu.Lock()
	existing := sharedServers[key]
	if existing != nil && existing.srv != nil {
		existing.refs++
		if persistent {
			existing.persistent = true
		}
		existing.updatedAt = time.Now().UTC()
		srv := existing.srv
		sharedMu.Unlock()
		return srv, nil
	}
	sharedMu.Unlock()

	srv, startErr := startServerFn(ctx, cfg, workDir)
	now := time.Now().UTC()

	sharedMu.Lock()
	defer sharedMu.Unlock()

	entry := sharedServers[key]
	if entry == nil {
		entry = &sharedServerEntry{cfg: cfg}
		sharedServers[key] = entry
	}
	if persistent {
		entry.persistent = true
	}
	entry.updatedAt = now

	// Another goroutine may have started it while we were outside the lock.
	if entry.srv != nil {
		entry.refs++
		entry.lastErr = ""
		if startErr == nil && srv != nil && srv != entry.srv {
			go srv.Close()
		}
		return entry.srv, nil
	}

	if startErr != nil {
		entry.lastErr = startErr.Error()
		return nil, startErr
	}

	entry.srv = srv
	entry.lastErr = ""
	entry.refs++
	return srv, nil
}

func releaseServer(cfg models.MCPServerConfig) {
	key := serverConfigKey(cfg)
	var toClose *MCPServer

	sharedMu.Lock()
	entry := sharedServers[key]
	if entry == nil {
		sharedMu.Unlock()
		return
	}
	if entry.refs > 0 {
		entry.refs--
	}
	entry.updatedAt = time.Now().UTC()
	if entry.refs == 0 && !entry.persistent && entry.srv != nil {
		toClose = entry.srv
		entry.srv = nil
	}
	sharedMu.Unlock()

	if toClose != nil {
		toClose.Close()
	}
}

func serverConfigKey(cfg models.MCPServerConfig) string {
	name := strings.TrimSpace(strings.ToLower(cfg.Name))
	kind := strings.TrimSpace(strings.ToLower(cfg.Type))
	url := strings.TrimSpace(strings.ToLower(cfg.URL))
	cmd := strings.Join(trimmedSlice(cfg.Command), "\x1f")
	env := canonicalMap(cfg.Env)
	headers := canonicalMap(cfg.Headers)
	return strings.Join([]string{name, kind, url, cmd, env, headers}, "\x1e")
}

func trimmedSlice(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func canonicalMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	pairs := make([]string, 0, len(m))
	for k, v := range m {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		pairs = append(pairs, key+"="+strings.TrimSpace(v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "\x1f")
}

func startServer(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
	srv := &MCPServer{
		name: cfg.Name,
		kind: strings.ToLower(strings.TrimSpace(cfg.Type)),
	}
	if srv.kind == "" {
		if cfg.URL != "" {
			srv.kind = "http"
		} else {
			srv.kind = "stdio"
		}
	}

	switch srv.kind {
	case "stdio":
		if len(cfg.Command) == 0 {
			return nil, fmt.Errorf("MCP server %q has no command", cfg.Name)
		}
		cmd := exec.CommandContext(ctx, cfg.Command[0], cfg.Command[1:]...)
		cmd.Dir = workDir
		cmd.Stderr = os.Stderr
		cmd.Env = os.Environ()
		for k, v := range cfg.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, err
		}
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("starting process: %w", err)
		}
		srv.cmd = cmd
		srv.stdin = stdin
		srv.stdout = bufio.NewReader(stdout)

	case "http", "sse", "ws":
		if strings.TrimSpace(cfg.URL) == "" {
			return nil, fmt.Errorf("MCP server %q requires URL for type %q", cfg.Name, srv.kind)
		}
		srv.url = strings.TrimSpace(cfg.URL)
		srv.headers = map[string]string{}
		for k, v := range cfg.Headers {
			srv.headers[k] = v
		}
		srv.httpClient = &http.Client{Timeout: 60 * time.Second}

	default:
		return nil, fmt.Errorf("unsupported MCP type %q for server %q", cfg.Type, cfg.Name)
	}

	// Initialize with JSON-RPC initialize handshake
	if err := srv.initialize(); err != nil {
		srv.Close()
		return nil, fmt.Errorf("MCP initialize: %w", err)
	}

	// Fetch tool list
	if err := srv.fetchTools(); err != nil {
		srv.Close()
		return nil, fmt.Errorf("fetching tools: %w", err)
	}

	return srv, nil
}

func (s *MCPServer) call(method string, params interface{}) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := s.nextID.Add(1)
	req := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if s.kind == "stdio" {
		data = append(data, '\n')
		if _, err := s.stdin.Write(data); err != nil {
			return nil, fmt.Errorf("writing to MCP server: %w", err)
		}

		// Read response lines until we find our ID
		for {
			line, err := s.stdout.ReadBytes('\n')
			if err != nil {
				return nil, fmt.Errorf("reading from MCP server: %w", err)
			}
			line = []byte(strings.TrimSpace(string(line)))
			if len(line) == 0 {
				continue
			}

			var resp jsonRPCResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				// Could be a notification, skip
				continue
			}
			if resp.ID == id {
				if resp.Error != nil {
					return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
				}
				return resp.Result, nil
			}
		}
	}

	httpReq, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for k, v := range s.headers {
		httpReq.Header.Set(k, v)
	}
	httpResp, err := s.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("MCP HTTP request failed: %w", err)
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading MCP HTTP response: %w", err)
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP HTTP %d: %s", httpResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("parsing MCP HTTP response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("MCP error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

func (s *MCPServer) initialize() error {
	params := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]interface{}{},
		"clientInfo": map[string]string{
			"name":    "openvibely",
			"version": "1.0.0",
		},
	}
	_, err := s.call("initialize", params)
	if err != nil {
		return err
	}
	if s.kind != "stdio" {
		return nil
	}
	// Send initialized notification (no response expected)
	s.mu.Lock()
	defer s.mu.Unlock()
	notif := map[string]interface{}{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	data, _ := json.Marshal(notif)
	data = append(data, '\n')
	_, err = s.stdin.Write(data)
	return err
}

func (s *MCPServer) fetchTools() error {
	result, err := s.call("tools/list", map[string]interface{}{})
	if err != nil {
		return err
	}
	var listing toolsListResult
	if err := json.Unmarshal(result, &listing); err != nil {
		return fmt.Errorf("parsing tools list: %w", err)
	}

	s.tools = make([]MCPTool, 0, len(listing.Tools))
	for _, t := range listing.Tools {
		s.tools = append(s.tools, MCPTool{
			ServerName: s.name,
			Name:       t.Name,
			PrefixName: s.name + "__" + t.Name,
			Schema:     t.InputSchema,
			Desc:       t.Description,
		})
	}
	log.Printf("[mcp] server %q: %d tools available", s.name, len(s.tools))
	return nil
}

// Execute calls a tool on this server and returns the result text.
func (s *MCPServer) Execute(toolName string, args map[string]interface{}) (string, bool, error) {
	params := map[string]interface{}{
		"name":      toolName,
		"arguments": args,
	}
	result, err := s.call("tools/call", params)
	if err != nil {
		return "", true, err
	}
	var callResult toolsCallResult
	if err := json.Unmarshal(result, &callResult); err != nil {
		return "", true, fmt.Errorf("parsing tool result: %w", err)
	}
	var texts []string
	for _, c := range callResult.Content {
		if c.Text != "" {
			texts = append(texts, c.Text)
		}
	}
	return strings.Join(texts, "\n"), callResult.IsError, nil
}

// Close kills the server subprocess.
func (s *MCPServer) Close() {
	if s.kind == "stdio" {
		if s.stdin != nil {
			s.stdin.Close()
		}
		if s.cmd != nil && s.cmd.Process != nil {
			s.cmd.Process.Kill()
			s.cmd.Wait()
		}
	}
}

// ToolDefinitions returns all MCP tools as Anthropic API ToolDefinitions.
func (m *MCPManager) ToolDefinitions() []anthropicclient.ToolDefinition {
	var defs []anthropicclient.ToolDefinition
	for _, ref := range m.servers {
		for _, t := range ref.srv.tools {
			defs = append(defs, anthropicclient.ToolDefinition{
				Name:        t.PrefixName,
				Description: fmt.Sprintf("[MCP:%s] %s", t.ServerName, t.Desc),
				InputSchema: t.Schema,
			})
		}
	}
	return defs
}

// ExecuteTool routes a tool call to the correct MCP server.
// Returns output text, isError, and any error.
func (m *MCPManager) ExecuteTool(prefixedName string, args map[string]interface{}) (string, bool, error) {
	for _, ref := range m.servers {
		for _, t := range ref.srv.tools {
			if t.PrefixName == prefixedName {
				return ref.srv.Execute(t.Name, args)
			}
		}
	}
	return "", true, fmt.Errorf("unknown MCP tool: %s", prefixedName)
}

// IsMCPTool checks if a tool name belongs to an MCP server.
func (m *MCPManager) IsMCPTool(name string) bool {
	for _, ref := range m.servers {
		for _, t := range ref.srv.tools {
			if t.PrefixName == name {
				return true
			}
		}
	}
	return false
}

// Close shuts down all MCP servers.
func (m *MCPManager) Close() {
	for _, ref := range m.servers {
		releaseServer(ref.cfg)
	}
}
