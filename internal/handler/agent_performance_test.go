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
	"github.com/openvibely/openvibely/web/templates/pages"
)

func BenchmarkListAgentsHTMXWarm100(b *testing.B) {
	db := testutil.NewTestDB(b)
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

	b.Run("baseline", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			c, _ := request()
			uncached := service.NewAgentLibraryMaintenanceService(nil, nil, agentRepo)
			uncached.SetLifecycleRepo(lifecycleRepo)
			uncached.SetAgentsRootPath(globalRoot)
			if err := uncached.SyncRootDeclarations(c.Request().Context(), projectRoot); err != nil {
				b.Fatal(err)
			}
			agents, err := agentRepo.List(c.Request().Context())
			if err != nil {
				b.Fatal(err)
			}
			for j := range agents {
				if agents[j].GeneratedStatus != models.AgentStatusProtected {
					if err := agentRepo.Update(c.Request().Context(), &agents[j]); err != nil {
						b.Fatal(err)
					}
					hooks, err := lifecycleRepo.HooksByAgent(c.Request().Context(), agents[j].ID)
					if err != nil {
						b.Fatal(err)
					}
					for k := range hooks {
						if err := lifecycleRepo.UpdateHook(c.Request().Context(), &hooks[k]); err != nil {
							b.Fatal(err)
						}
					}
				}
			}
			if _, err := h.materializeDBAgentsToDisk(c, agents); err != nil {
				b.Fatal(err)
			}
			agents, err = agentRepo.List(c.Request().Context())
			if err != nil {
				b.Fatal(err)
			}
			configs, err := llmConfigRepo.List(c.Request().Context())
			if err != nil {
				b.Fatal(err)
			}
			if err := render(c, http.StatusOK, pages.AgentsContent(agents, buildAgentModelOptions(configs))); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("candidate", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			c, _ := request()
			if err := h.ListAgents(c); err != nil {
				b.Fatal(err)
			}
		}
	})

	for _, kind := range []string{"baseline", "candidate"} {
		b.Run("sqlite_query_wait/"+kind, func(b *testing.B) {
			refresh := func() error {
				c, _ := request()
				if kind == "candidate" {
					return h.ListAgents(c)
				}
				uncached := service.NewAgentLibraryMaintenanceService(nil, nil, agentRepo)
				uncached.SetLifecycleRepo(lifecycleRepo)
				uncached.SetAgentsRootPath(globalRoot)
				if err := uncached.SyncRootDeclarations(c.Request().Context(), projectRoot); err != nil {
					return err
				}
				agents, err := agentRepo.List(c.Request().Context())
				if err != nil {
					return err
				}
				for j := range agents {
					if agents[j].GeneratedStatus != models.AgentStatusProtected {
						if err := agentRepo.Update(c.Request().Context(), &agents[j]); err != nil {
							return err
						}
						hooks, err := lifecycleRepo.HooksByAgent(c.Request().Context(), agents[j].ID)
						if err != nil {
							return err
						}
						for k := range hooks {
							if err := lifecycleRepo.UpdateHook(c.Request().Context(), &hooks[k]); err != nil {
								return err
							}
						}
					}
				}
				if _, err := h.materializeDBAgentsToDisk(c, agents); err != nil {
					return err
				}
				agents, err = agentRepo.List(c.Request().Context())
				if err != nil {
					return err
				}
				configs, err := llmConfigRepo.List(c.Request().Context())
				if err != nil {
					return err
				}
				return render(c, http.StatusOK, pages.AgentsContent(agents, buildAgentModelOptions(configs)))
			}

			waits := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				started := make(chan struct{}, 10)
				errs := make(chan error, 10)
				var wg sync.WaitGroup
				for worker := 0; worker < 10; worker++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						started <- struct{}{}
						errs <- refresh()
					}()
				}
				for worker := 0; worker < 10; worker++ {
					<-started
				}
				time.Sleep(5 * time.Millisecond)
				startedAt := time.Now()
				var count int
				err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM projects`).Scan(&count)
				waits = append(waits, time.Since(startedAt))
				if err != nil {
					b.Fatal(err)
				}
				wg.Wait()
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
