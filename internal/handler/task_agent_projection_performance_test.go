package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/database"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const taskUIAgentPerformanceSamples = 5

type taskUIAgentRenderFixture struct {
	connections *database.Connections
	h           *Handler
	e           http.Handler
	agentRepo   *repository.AgentRepo
	projectID   string
	detailTask  string
}

type taskUIAgentRenderCase struct {
	name    string
	request func(*taskUIAgentRenderFixture) *http.Request
}

type taskUIAgentRenderMetrics struct {
	latency            time.Duration
	allocatedBytes     uint64
	allocations        uint64
	responseHTMLBytes  int
	concurrentReadWait time.Duration
	concurrentWaits    int64
}

func TestHandlerTaskUIAgentProjectionProductionRenderPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping production-topology Task UI Agent render measurements in short mode")
	}

	fixture := newTaskUIAgentRenderFixture(t)
	for _, tc := range taskUIAgentRenderCases() {
		t.Run(tc.name, func(t *testing.T) {
			baseline := fixture.measure(t, fixture.fullAgentLoader, tc.request)
			compact := fixture.measure(t, fixture.compactAgentLoader, tc.request)

			if compact.latency*5 > baseline.latency {
				t.Fatalf("compact %s latency = %s, want at least 80%% lower than full hydration %s", tc.name, compact.latency, baseline.latency)
			}
			if compact.allocatedBytes*10 > baseline.allocatedBytes {
				t.Fatalf("compact %s allocated bytes = %d, want at least 90%% lower than full hydration %d", tc.name, compact.allocatedBytes, baseline.allocatedBytes)
			}
			if compact.allocations*10 > baseline.allocations {
				t.Fatalf("compact %s allocations = %d, want at least 90%% lower than full hydration %d", tc.name, compact.allocations, baseline.allocations)
			}
			if compact.responseHTMLBytes > baseline.responseHTMLBytes {
				t.Fatalf("compact %s response HTML bytes = %d, want <= full hydration %d", tc.name, compact.responseHTMLBytes, baseline.responseHTMLBytes)
			}
			if compact.concurrentReadWait > baseline.concurrentReadWait {
				t.Fatalf("compact %s concurrent production-reader wait = %s, want <= full hydration %s", tc.name, compact.concurrentReadWait, baseline.concurrentReadWait)
			}
			if baseline.concurrentWaits == 0 {
				t.Fatalf("full hydration %s did not produce a concurrent production-reader pool wait", tc.name)
			}

			t.Logf("%s median: full=%s/%d B/%d allocs/%d HTML bytes/%s reader wait; compact=%s/%d B/%d allocs/%d HTML bytes/%s reader wait",
				tc.name,
				baseline.latency, baseline.allocatedBytes, baseline.allocations, baseline.responseHTMLBytes, baseline.concurrentReadWait,
				compact.latency, compact.allocatedBytes, compact.allocations, compact.responseHTMLBytes, compact.concurrentReadWait,
			)
		})
	}
}

func BenchmarkHandlerTaskUIAgentProjection(b *testing.B) {
	fixture := newTaskUIAgentRenderFixture(b)
	for _, tc := range taskUIAgentRenderCases() {
		for _, loader := range []struct {
			name   string
			loader func(context.Context) ([]repository.AgentTaskUIOption, error)
		}{
			{name: "full Agent hydration", loader: fixture.fullAgentLoader},
			{name: "compact Task UI projection", loader: fixture.compactAgentLoader},
		} {
			b.Run(tc.name+"/"+loader.name, func(b *testing.B) {
				fixture.h.taskUIAgentOptionsLoader = loader.loader
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					rec := httptest.NewRecorder()
					fixture.e.ServeHTTP(rec, tc.request(fixture))
					if rec.Code != http.StatusOK {
						b.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
					}
					if i == 0 {
						b.ReportMetric(float64(rec.Body.Len()), "response_html_bytes")
					}
				}
			})
		}
	}
}

func newTaskUIAgentRenderFixture(tb testing.TB) *taskUIAgentRenderFixture {
	tb.Helper()
	connections, err := database.NewReadWrite(filepath.Join(tb.TempDir(), "task-ui-agent-performance.db"))
	if err != nil {
		tb.Fatalf("open production-topology database: %v", err)
	}
	tb.Cleanup(func() {
		if err := connections.Close(); err != nil {
			tb.Errorf("close production-topology database: %v", err)
		}
	})
	if connections.Reader == connections.Writer || connections.Reader.Stats().MaxOpenConnections != 1 || connections.Writer.Stats().MaxOpenConnections != 1 {
		tb.Fatalf("Task UI performance fixture must use production 1W + 1R topology: reader=%p writer=%p reader_max=%d writer_max=%d",
			connections.Reader, connections.Writer, connections.Reader.Stats().MaxOpenConnections, connections.Writer.Stats().MaxOpenConnections)
	}
	unregister := repository.RegisterDedicatedWriter(connections.Reader, connections.Writer)
	tb.Cleanup(unregister)

	ctx := context.Background()
	writerAgentRepo := repository.NewAgentRepo(connections.Writer)
	var assignedID, fallbackID string
	for i := 0; i < 1000; i++ {
		agent := newProductionTaskUIAgent(fmt.Sprintf("Task UI Performance Agent %04d", i))
		if i == 1 {
			agent.Enabled = false
			agent.SelectableAsPrimary = false
		}
		if err := writerAgentRepo.Create(ctx, agent); err != nil {
			tb.Fatalf("create rich Agent %d: %v", i, err)
		}
		switch i {
		case 0:
			assignedID = agent.ID
		case 1:
			fallbackID = agent.ID
		}
	}

	writerTaskRepo := repository.NewTaskRepo(connections.Writer, nil)
	boardTask := &models.Task{
		ProjectID:         "default",
		Title:             "Task UI Performance Board Badge",
		Category:          models.CategoryBacklog,
		Priority:          2,
		Status:            models.StatusPending,
		Prompt:            "render compact Agent badge",
		AgentDefinitionID: &assignedID,
	}
	if err := writerTaskRepo.Create(ctx, boardTask); err != nil {
		tb.Fatalf("create Task UI board task: %v", err)
	}
	detailTask := &models.Task{
		ProjectID:         "default",
		Title:             "Task UI Performance Detail Fallback",
		Category:          models.CategoryBacklog,
		Priority:          2,
		Status:            models.StatusPending,
		Prompt:            "render compact Agent selector",
		AgentDefinitionID: &fallbackID,
	}
	if err := writerTaskRepo.Create(ctx, detailTask); err != nil {
		tb.Fatalf("create Task UI detail task: %v", err)
	}

	h, e, _ := setupTestHandlerForDB(tb, connections.Reader)
	h.SetAgentRepo(repository.NewAgentRepo(connections.Reader))
	return &taskUIAgentRenderFixture{
		connections: connections,
		h:           h,
		e:           e,
		agentRepo:   repository.NewAgentRepo(connections.Reader),
		projectID:   "default",
		detailTask:  detailTask.ID,
	}
}

func newProductionTaskUIAgent(name string) *models.Agent {
	agent := &models.Agent{
		Name:         name,
		Description:  "production-shaped Task UI Agent performance fixture",
		SystemPrompt: fmt.Sprintf("%s %s", name, strings.Repeat("rich private Task UI Agent configuration ", 384)),
		Model:        "inherit",
		Tools:        []string{"Read", "Write", "Edit", "Bash", models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{
			Directory:   "src",
			Permissions: []string{"read", "write"},
		}}},
		Plugins:             []string{"github@marketplace", "playwright@claude-plugins-official"},
		PermissionDefaults:  models.AgentPermissionDefaults{ReadAgents: true, ReadSkills: true, ReadRepositoryFiles: true, UseShellOrTools: true},
		ModelDefaults:       models.AgentModelDefaults{Model: "gpt-5", Temperature: 0.3, MaxTokens: 8192},
		SourceRefs:          []string{"agents/task-ui/SKILLS.md", strings.Repeat("source-reference ", 128)},
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	for i := 0; i < 12; i++ {
		suffix := fmt.Sprintf("%02d", i)
		agent.Tools = append(agent.Tools, "TaskUI"+suffix)
		agent.ToolConfig.ScopedFiles = append(agent.ToolConfig.ScopedFiles, models.ScopedFilesConfig{
			Directory:   "project/" + suffix,
			Permissions: []string{"read", "write", "execute"},
		})
		agent.Plugins = append(agent.Plugins, "task-ui-plugin-"+suffix)
		agent.MCPServers = append(agent.MCPServers, models.MCPServerConfig{
			Name:    "task-ui-mcp-" + suffix,
			Command: []string{"task-ui-mcp", "--profile", suffix},
			Env: map[string]string{
				"TOKEN":  strings.Repeat("x", 256),
				"CONFIG": strings.Repeat("configuration-", 32),
			},
		})
		agent.Skills = append(agent.Skills, models.SkillConfig{
			Name:        "task-ui-skill-" + suffix,
			Description: "production-shaped Task UI projection skill",
			Tools:       "Read, Grep, Edit, Bash",
			Content:     strings.Repeat("production-shaped skill body ", 128),
		})
		agent.SourceRefs = append(agent.SourceRefs, "agents/task-ui/"+suffix+"/SKILLS.md")
	}
	return agent
}

func taskUIAgentRenderCases() []taskUIAgentRenderCase {
	return []taskUIAgentRenderCase{
		{
			name: "full Task board",
			request: func(fixture *taskUIAgentRenderFixture) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/tasks?project_id="+fixture.projectID, nil)
			},
		},
		{
			name: "main-content Task board",
			request: func(fixture *taskUIAgentRenderFixture) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+fixture.projectID, nil)
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", "main-content")
				return req
			},
		},
		{
			name: "board-only Task refresh",
			request: func(fixture *taskUIAgentRenderFixture) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+fixture.projectID, nil)
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", "kanban-board")
				return req
			},
		},
		{
			name: "initial Task Detail",
			request: func(fixture *taskUIAgentRenderFixture) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/tasks/"+fixture.detailTask, nil)
			},
		},
	}
}

func (fixture *taskUIAgentRenderFixture) compactAgentLoader(ctx context.Context) ([]repository.AgentTaskUIOption, error) {
	return fixture.agentRepo.ListTaskUIOptions(ctx)
}

func (fixture *taskUIAgentRenderFixture) fullAgentLoader(ctx context.Context) ([]repository.AgentTaskUIOption, error) {
	agents, err := fixture.agentRepo.List(ctx)
	if err != nil {
		return nil, err
	}
	options := make([]repository.AgentTaskUIOption, 0, len(agents))
	for _, agent := range agents {
		options = append(options, repository.AgentTaskUIOption{
			ID:                  agent.ID,
			Name:                agent.Name,
			Model:               agent.Model,
			Scope:               agent.Scope,
			ProjectID:           agent.ProjectID,
			SelectableAsPrimary: agent.SelectableAsPrimary,
			Enabled:             agent.Enabled,
			GeneratedStatus:     agent.GeneratedStatus,
			ArchivedAt:          agent.ArchivedAt,
		})
	}
	return options, nil
}

func (fixture *taskUIAgentRenderFixture) measure(t *testing.T, loader func(context.Context) ([]repository.AgentTaskUIOption, error), request func(*taskUIAgentRenderFixture) *http.Request) taskUIAgentRenderMetrics {
	t.Helper()
	fixture.h.taskUIAgentOptionsLoader = loader
	for range 2 {
		fixture.render(t, request)
	}

	latencies := make([]time.Duration, 0, taskUIAgentPerformanceSamples)
	allocatedBytes := make([]uint64, 0, taskUIAgentPerformanceSamples)
	allocations := make([]uint64, 0, taskUIAgentPerformanceSamples)
	responseBytes := make([]int, 0, taskUIAgentPerformanceSamples)
	concurrentWaits := make([]time.Duration, 0, taskUIAgentPerformanceSamples)
	concurrentWaitCounts := make([]int64, 0, taskUIAgentPerformanceSamples)
	for range taskUIAgentPerformanceSamples {
		latency, bytes, allocs, response := fixture.measureRenderSample(t, request)
		latencies = append(latencies, latency)
		allocatedBytes = append(allocatedBytes, bytes)
		allocations = append(allocations, allocs)
		responseBytes = append(responseBytes, response)
		wait, waitCount := fixture.measureConcurrentRead(t, loader, request)
		concurrentWaits = append(concurrentWaits, wait)
		concurrentWaitCounts = append(concurrentWaitCounts, waitCount)
	}
	slices.Sort(latencies)
	slices.Sort(allocatedBytes)
	slices.Sort(allocations)
	slices.Sort(responseBytes)
	slices.Sort(concurrentWaits)
	slices.Sort(concurrentWaitCounts)
	middle := taskUIAgentPerformanceSamples / 2
	return taskUIAgentRenderMetrics{
		latency:            latencies[middle],
		allocatedBytes:     allocatedBytes[middle],
		allocations:        allocations[middle],
		responseHTMLBytes:  responseBytes[middle],
		concurrentReadWait: concurrentWaits[middle],
		concurrentWaits:    concurrentWaitCounts[middle],
	}
}

func (fixture *taskUIAgentRenderFixture) measureRenderSample(t *testing.T, request func(*taskUIAgentRenderFixture) *http.Request) (time.Duration, uint64, uint64, int) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	startedAt := time.Now()
	responseBytes := fixture.render(t, request)
	elapsed := time.Since(startedAt)
	runtime.ReadMemStats(&after)
	return elapsed, after.TotalAlloc - before.TotalAlloc, after.Mallocs - before.Mallocs, responseBytes
}

func (fixture *taskUIAgentRenderFixture) measureConcurrentRead(t *testing.T, loader func(context.Context) ([]repository.AgentTaskUIOption, error), request func(*taskUIAgentRenderFixture) *http.Request) (time.Duration, int64) {
	t.Helper()
	catalogEntered := make(chan struct{})
	var catalogOnce sync.Once
	fixture.h.taskUIAgentOptionsLoader = func(ctx context.Context) ([]repository.AgentTaskUIOption, error) {
		catalogOnce.Do(func() { close(catalogEntered) })
		return loader(ctx)
	}

	type renderResult struct {
		code int
		body string
	}
	rendered := make(chan renderResult, 1)
	go func() {
		rec := httptest.NewRecorder()
		fixture.e.ServeHTTP(rec, request(fixture))
		rendered <- renderResult{code: rec.Code, body: rec.Body.String()}
	}()
	select {
	case <-catalogEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("Task UI Agent catalog did not start")
	}

	const concurrentReaders = 8
	ready := make(chan struct{}, concurrentReaders)
	start := make(chan struct{})
	readErrors := make(chan error, concurrentReaders)
	for range concurrentReaders {
		go func() {
			ready <- struct{}{}
			<-start
			var projectID string
			readErrors <- fixture.connections.Reader.QueryRowContext(context.Background(), `SELECT id FROM projects ORDER BY id LIMIT 1`).Scan(&projectID)
		}()
	}
	for range concurrentReaders {
		<-ready
	}
	before := fixture.connections.Reader.Stats()
	close(start)
	for range concurrentReaders {
		if err := <-readErrors; err != nil {
			t.Fatalf("concurrent lightweight project read: %v", err)
		}
	}
	result := <-rendered
	if result.code != http.StatusOK {
		t.Fatalf("render with concurrent reads = status %d body %s", result.code, result.body)
	}
	after := fixture.connections.Reader.Stats()
	return after.WaitDuration - before.WaitDuration, after.WaitCount - before.WaitCount
}

func (fixture *taskUIAgentRenderFixture) render(t testing.TB, request func(*taskUIAgentRenderFixture) *http.Request) int {
	t.Helper()
	rec := httptest.NewRecorder()
	fixture.e.ServeHTTP(rec, request(fixture))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	return rec.Body.Len()
}
