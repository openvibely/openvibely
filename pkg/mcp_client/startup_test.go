package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

const helperEnv = "OPENVIBELY_MCP_HELPER_PROCESS"

// TestMCPHelperProcess is a fake stdio MCP server run as a subprocess of the test binary.
func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	delay, _ := time.ParseDuration(os.Getenv("MCP_HELPER_DELAY"))
	tool := os.Getenv("MCP_HELPER_TOOL")
	in := bufio.NewScanner(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	for in.Scan() {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var result string
		switch req.Method {
		case "initialize":
			time.Sleep(delay)
			result = `{"capabilities":{}}`
		case "tools/list":
			time.Sleep(delay)
			result = fmt.Sprintf(`{"tools":[{"name":%q,"description":"desc %s","inputSchema":{"type":"object"}}]}`, tool, tool)
		default:
			result = `{"content":[{"type":"text","text":"ok"}]}`
		}
		fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":%s}`+"\n", *req.ID, result)
		out.Flush()
	}
	os.Exit(0)
}

func helperConfig(name string, delay time.Duration) models.MCPServerConfig {
	return models.MCPServerConfig{
		Name:    name,
		Type:    "stdio",
		Command: []string{os.Args[0], "-test.run=^TestMCPHelperProcess$"},
		Env: map[string]string{
			helperEnv:          "1",
			"MCP_HELPER_DELAY": delay.String(),
			"MCP_HELPER_TOOL":  "tool_" + name,
		},
	}
}

// sequentialManager preserves the original constructor for before/after measurements.
func sequentialManager(ctx context.Context, configs []models.MCPServerConfig, workDir string) (*MCPManager, error) {
	m := &MCPManager{}
	for _, cfg := range configs {
		key := serverConfigKey(cfg)
		srv, err := acquireServer(ctx, cfg, workDir, false)
		if err != nil {
			m.Close()
			return nil, err
		}
		m.servers = append(m.servers, managedServerRef{key: key, cfg: cfg, srv: srv})
	}
	return m, nil
}

type managerConstructor func(context.Context, []models.MCPServerConfig, string) (*MCPManager, error)

func withStartServerFn(t testing.TB, fn func(context.Context, models.MCPServerConfig, string) (*MCPServer, error)) {
	t.Helper()
	orig := startServerFn
	startServerFn = fn
	t.Cleanup(func() { startServerFn = orig })
}

func sharedEntry(cfg models.MCPServerConfig) (refs int, running bool, ok bool) {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	e := sharedServers[serverConfigKey(cfg)]
	if e == nil {
		return 0, false, false
	}
	return e.refs, e.srv != nil, true
}

func timeColdSetup(t *testing.T, configs []models.MCPServerConfig, workDir string, constructor managerConstructor) (time.Duration, []byte) {
	t.Helper()
	resetSharedStateForTests()
	start := time.Now()
	m, err := constructor(context.Background(), configs, workDir)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defs, _ := json.Marshal(m.ToolDefinitions())
	m.Close()
	return elapsed, defs
}

func percentile(ds []time.Duration, p float64) time.Duration {
	s := slices.Clone(ds)
	slices.Sort(s)
	idx := int(float64(len(s)-1)*p + 0.5)
	return s[idx]
}

func TestNewMCPManager_ParallelColdStartupPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	t.Cleanup(resetSharedStateForTests)
	workDir := t.TempDir()
	const runs = 10
	delay := 100 * time.Millisecond

	measure := func(configs []models.MCPServerConfig) (seq, par []time.Duration) {
		var seqDefs, parDefs []byte
		for range runs {
			d, defs := timeColdSetup(t, configs, workDir, sequentialManager)
			seq = append(seq, d)
			seqDefs = defs
			d, defs = timeColdSetup(t, configs, workDir, NewMCPManager)
			par = append(par, d)
			parDefs = defs
		}
		if string(seqDefs) != string(parDefs) {
			t.Fatalf("tool definitions differ:\nsequential=%s\nparallel=%s", seqDefs, parDefs)
		}
		return seq, par
	}

	four := []models.MCPServerConfig{
		helperConfig("alpha", delay), helperConfig("bravo", delay),
		helperConfig("charlie", delay), helperConfig("delta", delay),
	}
	seq, par := measure(four)
	seqMed, parMed := percentile(seq, 0.5), percentile(par, 0.5)
	t.Logf("four cold servers: sequential median=%v p95=%v; parallel(4) median=%v p95=%v",
		seqMed, percentile(seq, 0.95), parMed, percentile(par, 0.95))
	if reduction := 1 - float64(parMed)/float64(seqMed); reduction < 0.60 {
		t.Fatalf("median reduction %.0f%% < 60%%", reduction*100)
	}

	seq, par = measure([]models.MCPServerConfig{helperConfig("solo", delay)})
	seqMed, parMed = percentile(seq, 0.5), percentile(par, 0.5)
	t.Logf("single cold server: sequential median=%v p95=%v; parallel(4) median=%v p95=%v",
		seqMed, percentile(seq, 0.95), parMed, percentile(par, 0.95))
	if float64(parMed) > float64(seqMed)*1.10 {
		t.Fatalf("single cold server regressed: %v vs %v", parMed, seqMed)
	}

	_, defs := timeColdSetup(t, four, workDir, NewMCPManager)
	var got []struct{ Name string }
	_ = json.Unmarshal(defs, &got)
	want := []string{"alpha__tool_alpha", "bravo__tool_bravo", "charlie__tool_charlie", "delta__tool_delta"}
	if len(got) != len(want) {
		t.Fatalf("tool defs=%s", defs)
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("tool %d = %q, want %q", i, got[i].Name, want[i])
		}
	}
}

func TestNewMCPManager_ConcurrencyLimitAndConfigOrder(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)

	var active, maxActive atomic.Int32
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, _ string) (*MCPServer, error) {
		n := active.Add(1)
		for {
			m := maxActive.Load()
			if n <= m || maxActive.CompareAndSwap(m, n) {
				break
			}
		}
		// Reverse finish order relative to config order.
		idx, _ := strconv.Atoi(cfg.Name[1:])
		time.Sleep(time.Duration(10-idx) * 10 * time.Millisecond)
		active.Add(-1)
		return &MCPServer{name: cfg.Name, tools: []MCPTool{{ServerName: cfg.Name, Name: "t", PrefixName: cfg.Name + "__t"}}}, nil
	})

	var configs []models.MCPServerConfig
	for i := range 9 {
		configs = append(configs, models.MCPServerConfig{Name: fmt.Sprintf("s%d", i), URL: fmt.Sprintf("https://example.invalid/%d", i)})
	}
	m, err := NewMCPManager(context.Background(), configs, ".")
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	defer m.Close()

	if got := maxActive.Load(); got != 4 {
		t.Fatalf("max simultaneous starts = %d, want 4", got)
	}
	defs := m.ToolDefinitions()
	if len(defs) != len(configs) {
		t.Fatalf("got %d defs", len(defs))
	}
	for i, d := range defs {
		if want := fmt.Sprintf("s%d__t", i); d.Name != want {
			t.Fatalf("def %d = %q, want %q", i, d.Name, want)
		}
	}
}

func TestNewMCPManager_ParallelPartialAndTotalFailure(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)
	failAll := false
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, _ string) (*MCPServer, error) {
		if failAll || cfg.Name == "bad" {
			return nil, errors.New("boom")
		}
		return &MCPServer{name: cfg.Name, tools: []MCPTool{{ServerName: cfg.Name, Name: "t", PrefixName: cfg.Name + "__t"}}}, nil
	})
	configs := []models.MCPServerConfig{
		{Name: "a", URL: "https://example.invalid/a"},
		{Name: "bad", URL: "https://example.invalid/bad"},
		{Name: "c", URL: "https://example.invalid/c"},
	}
	m, err := NewMCPManager(context.Background(), configs, ".")
	if err != nil {
		t.Fatalf("partial failure returned error: %v", err)
	}
	defs := m.ToolDefinitions()
	if len(defs) != 2 || defs[0].Name != "a__t" || defs[1].Name != "c__t" {
		t.Fatalf("unexpected defs: %#v", defs)
	}
	m.Close()

	resetSharedStateForTests()
	failAll = true
	_, err = NewMCPManager(context.Background(), configs, ".")
	if err == nil || err.Error() != "starting MCP servers failed: a: boom | bad: boom | c: boom" {
		t.Fatalf("unexpected all-failure error: %v", err)
	}
}

func TestNewMCPManager_DuplicateConfigsStartOnce(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)
	var mu sync.Mutex
	starts := map[string]int{}
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, _ string) (*MCPServer, error) {
		mu.Lock()
		starts[cfg.Name]++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		return &MCPServer{name: cfg.Name, tools: []MCPTool{{ServerName: cfg.Name, Name: "t", PrefixName: cfg.Name + "__t"}}}, nil
	})
	a := models.MCPServerConfig{Name: "a", URL: "https://example.invalid/a"}
	b := models.MCPServerConfig{Name: "b", URL: "https://example.invalid/b"}
	m, err := NewMCPManager(context.Background(), []models.MCPServerConfig{a, b, a, a}, ".")
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	if starts["a"] != 1 || starts["b"] != 1 {
		t.Fatalf("starts = %v, want one per distinct config", starts)
	}
	if refs, running, _ := sharedEntry(a); refs != 3 || !running {
		t.Fatalf("a refs=%d running=%v, want 3 running", refs, running)
	}
	m.Close()
	if refs, running, _ := sharedEntry(a); refs != 0 || running {
		t.Fatalf("after Close a refs=%d running=%v", refs, running)
	}
	if refs, running, _ := sharedEntry(b); refs != 0 || running {
		t.Fatalf("after Close b refs=%d running=%v", refs, running)
	}
}

func TestNewMCPManager_ReusesWarmPersistentServerWithoutRestart(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)
	var starts atomic.Int32
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, _ string) (*MCPServer, error) {
		starts.Add(1)
		return &MCPServer{name: cfg.Name, tools: []MCPTool{{ServerName: cfg.Name, Name: "t", PrefixName: cfg.Name + "__t"}}}, nil
	})
	warm := models.MCPServerConfig{Name: "warm", URL: "https://example.invalid/warm"}
	cold := models.MCPServerConfig{Name: "cold", URL: "https://example.invalid/cold"}
	if err := EnsurePersistentServers(context.Background(), []models.MCPServerConfig{warm}, "."); err != nil {
		t.Fatalf("EnsurePersistentServers: %v", err)
	}
	m, err := NewMCPManager(context.Background(), []models.MCPServerConfig{warm, cold}, ".")
	if err != nil {
		t.Fatalf("NewMCPManager: %v", err)
	}
	if got := starts.Load(); got != 2 {
		t.Fatalf("starts = %d, want 2 (persistent reused)", got)
	}
	defs := m.ToolDefinitions()
	if len(defs) != 2 || defs[0].Name != "warm__t" || defs[1].Name != "cold__t" {
		t.Fatalf("unexpected defs: %#v", defs)
	}
	m.Close()
	if refs, running, _ := sharedEntry(warm); refs != 0 || !running {
		t.Fatalf("persistent server refs=%d running=%v, want 0 running", refs, running)
	}
}

func TestNewMCPManager_CancellationReleasesAndStopsProcesses(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)

	var mu sync.Mutex
	var started []*MCPServer
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		srv, err := startServer(ctx, cfg, workDir)
		if srv != nil {
			mu.Lock()
			started = append(started, srv)
			mu.Unlock()
		}
		return srv, err
	})

	fast1, fast2 := helperConfig("fast1", 0), helperConfig("fast2", 0)
	slow1, slow2 := helperConfig("slow1", 30*time.Second), helperConfig("slow2", 30*time.Second)
	configs := []models.MCPServerConfig{fast1, slow1, fast2, slow2, fast1}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(started)
			mu.Unlock()
			if n == 2 {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()

	begin := time.Now()
	m, err := NewMCPManager(ctx, configs, t.TempDir())
	if err == nil {
		m.Close()
		t.Fatal("expected error after cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if time.Since(begin) > 10*time.Second {
		t.Fatal("cancellation did not stop startup promptly")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(started) != 2 {
		t.Fatalf("expected the two fast servers to start, got %d", len(started))
	}
	for _, srv := range started {
		if srv.cmd.ProcessState == nil {
			t.Fatalf("process for %q still running after cancellation", srv.name)
		}
	}
	for _, cfg := range configs {
		if refs, running, _ := sharedEntry(cfg); refs != 0 || running {
			t.Fatalf("%s refs=%d running=%v after cancellation", cfg.Name, refs, running)
		}
	}
}

func benchmarkColdSetup(b *testing.B, configs []models.MCPServerConfig, constructor managerConstructor) {
	workDir := b.TempDir()
	b.Cleanup(resetSharedStateForTests)
	for b.Loop() {
		resetSharedStateForTests()
		m, err := constructor(context.Background(), configs, workDir)
		if err != nil {
			b.Fatal(err)
		}
		m.Close()
	}
}

func BenchmarkNewMCPManagerColdFour(b *testing.B) {
	d := 100 * time.Millisecond
	configs := []models.MCPServerConfig{helperConfig("a", d), helperConfig("b", d), helperConfig("c", d), helperConfig("d", d)}
	b.Run("sequential", func(b *testing.B) { benchmarkColdSetup(b, configs, sequentialManager) })
	b.Run("parallel4", func(b *testing.B) { benchmarkColdSetup(b, configs, NewMCPManager) })
}

func BenchmarkNewMCPManagerColdSingle(b *testing.B) {
	configs := []models.MCPServerConfig{helperConfig("solo", 100*time.Millisecond)}
	b.Run("sequential", func(b *testing.B) { benchmarkColdSetup(b, configs, sequentialManager) })
	b.Run("parallel4", func(b *testing.B) { benchmarkColdSetup(b, configs, NewMCPManager) })
}

func BenchmarkNewMCPManagerWarmPersistent(b *testing.B) {
	configs := []models.MCPServerConfig{helperConfig("warm", 0)}
	run := func(b *testing.B, constructor managerConstructor) {
		resetSharedStateForTests()
		b.Cleanup(resetSharedStateForTests)
		if err := EnsurePersistentServers(context.Background(), configs, b.TempDir()); err != nil {
			b.Fatal(err)
		}
		for b.Loop() {
			m, err := constructor(context.Background(), configs, ".")
			if err != nil {
				b.Fatal(err)
			}
			m.Close()
		}
	}
	b.Run("sequential", func(b *testing.B) { run(b, sequentialManager) })
	b.Run("parallel4", func(b *testing.B) { run(b, NewMCPManager) })
}

// Timing comparisons belong in BenchmarkNewMCPManagerWarmPersistent: shared CI
// runners cannot reliably enforce a percentage threshold on microsecond calls.
func TestNewMCPManager_WarmPersistentRepeatedAcquisition(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)
	var starts atomic.Int32
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		starts.Add(1)
		return startServer(ctx, cfg, workDir)
	})
	cfg := helperConfig("warm", 0)
	configs := []models.MCPServerConfig{cfg}
	if err := EnsurePersistentServers(context.Background(), configs, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	var warmServer *MCPServer
	for range 20 {
		m, err := NewMCPManager(context.Background(), configs, ".")
		if err != nil {
			t.Fatal(err)
		}
		if len(m.servers) != 1 {
			m.Close()
			t.Fatal("expected one warm server")
		}
		if warmServer == nil {
			warmServer = m.servers[0].srv
		}
		if m.servers[0].srv != warmServer {
			m.Close()
			t.Fatal("warm acquisition replaced the persistent server")
		}
		if refs, running, _ := sharedEntry(cfg); refs != 1 || !running {
			m.Close()
			t.Fatalf("acquired server refs=%d running=%v, want 1 running", refs, running)
		}
		defs := m.ToolDefinitions()
		m.Close()
		if len(defs) != 1 || defs[0].Name != "warm__tool_warm" {
			t.Fatalf("unexpected tool definitions: %#v", defs)
		}
		if refs, running, _ := sharedEntry(cfg); refs != 0 || !running {
			t.Fatalf("released server refs=%d running=%v, want 0 running", refs, running)
		}
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("server starts = %d, want 1", got)
	}
}

func TestNewMCPManager_CancelsHTTPStartup(t *testing.T) {
	for _, method := range []string{"initialize", "tools/list"} {
		t.Run(method, func(t *testing.T) {
			resetSharedStateForTests()
			t.Cleanup(resetSharedStateForTests)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req jsonRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				if req.Method == method {
					cancel()
					<-r.Context().Done()
					return
				}
				_ = json.NewEncoder(w).Encode(jsonRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"capabilities":{}}`)})
			}))
			defer server.Close()
			cfg := models.MCPServerConfig{Name: "http", Type: "http", URL: server.URL}
			m, err := NewMCPManager(ctx, []models.MCPServerConfig{cfg}, ".")
			if m != nil {
				m.Close()
				t.Fatal("returned manager after cancellation")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if refs, running, _ := sharedEntry(cfg); refs != 0 || running {
				t.Fatalf("refs=%d running=%v after cancellation", refs, running)
			}
		})
	}
}
