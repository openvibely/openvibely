package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

type agentRefreshBenchmarkContextKey struct{}

func BenchmarkListAgentsHTMXWarm100(b *testing.B) {
	db, statementCounter := testutil.NewStatementCountingTestDB(b)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	projectSvc := service.NewProjectService(projectRepo)
	globalRoot := b.TempDir()
	projectCheckout := b.TempDir()
	projectRoot := filepath.Join(projectCheckout, ".openvibely")
	project := &models.Project{Name: "agents-performance", RepoPath: projectCheckout}
	if err := projectRepo.Create(context.Background(), project); err != nil {
		b.Fatal(err)
	}

	prompt := strings.Repeat("Production-shaped agent instructions with constraints and examples. ", 128)
	writeAgentBenchmarkDeclarations(b, globalRoot, "global", "", 0, 50, prompt)
	writeAgentBenchmarkDeclarations(b, projectRoot, "project", project.ID, 50, 100, prompt)

	maintenance := service.NewAgentLibraryMaintenanceService(nil, nil, agentRepo)
	maintenance.SetLifecycleRepo(lifecycleRepo)
	maintenance.SetAgentsRootPath(globalRoot)
	if err := maintenance.SyncRootDeclarations(context.Background(), projectRoot); err != nil {
		b.Fatal(err)
	}
	h := &Handler{
		projectSvc:                 projectSvc,
		agentRepo:                  agentRepo,
		lifecycleRepo:              lifecycleRepo,
		llmConfigRepo:              llmConfigRepo,
		agentSkillRoot:             globalRoot,
		agentLibraryMaintenanceSvc: maintenance,
	}
	e := echo.New()

	request := func() (echo.Context, *httptest.ResponseRecorder) {
		req := httptest.NewRequest(http.MethodGet, "/agents?project_id="+project.ID, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		return e.NewContext(req, rec), rec
	}
	warmContext, _ := request()
	if err := h.ListAgents(warmContext); err != nil {
		b.Fatal(err)
	}
	convergenceContext, _ := request()
	if err := h.ListAgents(convergenceContext); err != nil {
		b.Fatal(err)
	}

	statementCounter.Reset()
	statementCounter.SetEnabled(true)
	warmDeclarationMetricsBefore := maintenance.DeclarationSyncMetrics()
	instrumentedContext, _ := request()
	if err := h.ListAgents(instrumentedContext); err != nil {
		b.Fatal(err)
	}
	warmDeclarationMetricsAfter := maintenance.DeclarationSyncMetrics()
	statementCounter.SetEnabled(false)
	warmStatements := statementCounter.Statements()
	warmAgentLists := countSQLStatements(warmStatements, "SELECT ", " FROM agents WHERE COALESCE(generated_status, 'user_edited') <> 'archived' ORDER BY name ASC, id ASC LIMIT ? OFFSET ?")
	warmWrites := countSQLStatements(warmStatements, "INSERT ", "") + countSQLStatements(warmStatements, "UPDATE ", "") + countSQLStatements(warmStatements, "DELETE ", "")
	warmAgentHookWrites := countSQLStatements(warmStatements, "INSERT ", "agents") + countSQLStatements(warmStatements, "UPDATE ", "agents") + countSQLStatements(warmStatements, "DELETE ", "agents") +
		countSQLStatements(warmStatements, "INSERT ", "agent_lifecycle_hooks") + countSQLStatements(warmStatements, "UPDATE ", "agent_lifecycle_hooks") + countSQLStatements(warmStatements, "DELETE ", "agent_lifecycle_hooks")
	if warmDeclarationMetricsAfter != warmDeclarationMetricsBefore {
		b.Fatalf("warm ListAgents read or parsed unchanged declarations: before=%#v after=%#v", warmDeclarationMetricsBefore, warmDeclarationMetricsAfter)
	}
	if warmAgentLists == 0 {
		b.Fatalf("warm ListAgents did not execute a bounded agent-page query; statements: %q", warmStatements)
	}
	for _, statement := range warmStatements {
		normalized := strings.ToUpper(strings.TrimSpace(statement))
		if strings.Contains(normalized, " FROM AGENTS WHERE COALESCE(GENERATED_STATUS, 'USER_EDITED') <> 'ARCHIVED' ORDER BY NAME ASC") &&
			!strings.Contains(normalized, " LIMIT ? OFFSET ?") {
			b.Fatalf("warm ListAgents executed an unbounded agent-list query: %q", statement)
		}
	}
	if warmAgentHookWrites != 0 {
		b.Fatalf("warm ListAgents executed %d agent/hook writes, want 0; statements: %q", warmAgentHookWrites, warmStatements)
	}
	if warmWrites != 0 {
		b.Fatalf("warm ListAgents executed %d SQLite writes, want 0; statements: %q", warmWrites, warmStatements)
	}

	b.ReportAllocs()
	b.ReportMetric(float64(len(warmStatements)), "sqlite-statements/op")
	b.ReportMetric(float64(warmAgentLists), "agent-page-statements/op")
	b.ReportMetric(float64(warmWrites), "sqlite-writes/op")
	b.ReportMetric(float64(warmDeclarationMetricsAfter.ContentReads-warmDeclarationMetricsBefore.ContentReads), "declaration-reads/op")
	b.ReportMetric(float64(warmDeclarationMetricsAfter.Parses-warmDeclarationMetricsBefore.Parses), "declaration-parses/op")
	for i := 0; i < b.N; i++ {
		c, _ := request()
		if err := h.ListAgents(c); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("sqlite_query_wait", func(b *testing.B) {
		refresh := func() error {
			c, _ := request()
			refreshContext := context.WithValue(c.Request().Context(), agentRefreshBenchmarkContextKey{}, true)
			c.SetRequest(c.Request().WithContext(refreshContext))
			return h.ListAgents(c)
		}

		const refreshWorkers = 10
		const unrelatedQueriesPerBurst = 25
		waits := make([]time.Duration, 0, b.N*unrelatedQueriesPerBurst)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			firstRefreshStatement := make(chan struct{}, 1)
			statementCounter.SetObserver(func(ctx context.Context, _ string) {
				if marked, _ := ctx.Value(agentRefreshBenchmarkContextKey{}).(bool); marked {
					select {
					case firstRefreshStatement <- struct{}{}:
					default:
					}
				}
			})
			errs := make(chan error, refreshWorkers)
			var wg sync.WaitGroup
			for worker := 0; worker < refreshWorkers; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs <- refresh()
				}()
			}
			select {
			case <-firstRefreshStatement:
			case <-time.After(10 * time.Second):
				b.Fatal("refresh burst did not reach SQLite")
			}
			for query := 0; query < unrelatedQueriesPerBurst; query++ {
				startedAt := time.Now()
				var count int
				err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM projects`).Scan(&count)
				waits = append(waits, time.Since(startedAt))
				if err != nil {
					b.Fatal(err)
				}
			}
			wg.Wait()
			statementCounter.SetObserver(nil)
			close(errs)
			for refreshErr := range errs {
				if refreshErr != nil {
					b.Fatal(refreshErr)
				}
			}
		}
		b.StopTimer()
		sort.Slice(waits, func(i, j int) bool { return waits[i] < waits[j] })
		if len(waits) > 0 {
			median := waits[len(waits)/2]
			p95Index := (len(waits)*95+99)/100 - 1
			p95 := waits[p95Index]
			b.ReportMetric(float64(median.Nanoseconds()), "query-wait-median-ns")
			b.ReportMetric(float64(p95.Nanoseconds()), "query-wait-p95-ns")
		}
	})
}
func countSQLStatements(statements []string, prefix, contains string) int {
	count := 0
	for _, statement := range statements {
		normalized := strings.ToUpper(strings.TrimSpace(statement))
		if prefix != "" && !strings.HasPrefix(normalized, strings.ToUpper(prefix)) {
			continue
		}
		if contains != "" && !strings.Contains(normalized, strings.ToUpper(contains)) {
			continue
		}
		count++
	}
	return count
}

func writeAgentBenchmarkDeclarations(b *testing.B, root, scope, projectID string, start, end int, prompt string) {
	b.Helper()
	for i := start; i < end; i++ {
		key := fmt.Sprintf("perf_agent_%03d", i)
		dir := filepath.Join(root, "agents", key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		indentedPrompt := strings.ReplaceAll(prompt, "\n", "\n    ")
		declaration := fmt.Sprintf(`---
kind: openvibely.agent_skill
version: 1
agent:
  key: %s
  name: Performance Agent %03d
  description: Representative declaration used by the Agents HTMX benchmark.
  scope: %s
  project_id: %s
  selectable_as_primary: true
  enabled: true
  system_prompt: |
    %s
tools:
  - read_file
  - edit_file
  - bash
plugins:
  - github@official
mcp_servers:
  - github
permissions:
  read_task_prompt: true
  read_task_execution: true
  read_repository_files: true
  write_repository_files: true
model_defaults:
  model: inherit
  temperature: 0.2
  max_tokens: 4096
evidence_refs:
  - task_fixture
  - issue_173
lifecycle_hooks:
  after_complete:
    skill: validate_change
    output_contract: activity_summary
    blocking: false
    permissions:
      read_task_execution: true
---
# Performance Agent %03d
`, key, i, scope, projectID, indentedPrompt, i)
		if err := os.WriteFile(filepath.Join(dir, "SKILLS.md"), []byte(declaration), 0o644); err != nil {
			b.Fatal(err)
		}
	}
}
