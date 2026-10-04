package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

var newTaskSelectionBenchmarkSink *models.LLMConfig

func TestHandler_NewTaskFirstMessageReusesPreflightSelection(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		agentMode string
	}{
		{name: "auto_text", agentMode: "auto"},
		{name: "explicit", agentMode: "explicit"},
		{name: "default", agentMode: "default"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			env := newTestHandlerEnv(t, db)
			project := createProjectTB(t, env.Handler, "New task selection "+scenario.name)
			clearModelConfigs(t, db)
			catalog := seedNewTaskSelectionModels(t, env.LLMConfigRepo, 3, false)
			selectionAgentID := scenario.agentMode
			if scenario.agentMode == "explicit" {
				selectionAgentID = catalog[1].ID
			}
			message := "build endpoint handler service database integration test"
			selectedBefore, err := env.Handler.selectTaskAgent(context.Background(), project.ID, selectionAgentID, message, false)
			require.NoError(t, err)
			require.NotNil(t, selectedBefore)
			form := url.Values{"message": {message}, "agent_id": {selectionAgentID}}
			req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			env.Echo.ServeHTTP(rec, req)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			taskID := rec.Header().Get("X-Created-Task-ID")
			require.NotEmpty(t, taskID)
			require.Contains(t, rec.Body.String(), `data-loaded="true"`)
			require.Contains(t, rec.Body.String(), message)
			require.NotContains(t, rec.Body.String(), "Thread is loading...")
			task, err := env.TaskRepo.GetByID(context.Background(), taskID)
			require.NoError(t, err)
			require.NotNil(t, task)
			executions, err := env.ExecRepo.ListByTask(context.Background(), taskID)
			require.NoError(t, err)
			require.Len(t, executions, 1)
			require.Equal(t, selectedBefore.ID, executions[0].AgentConfigID)
			require.Equal(t, message, executions[0].PromptSent)
		})
	}
}

func TestHandler_NewTaskSelectionHandoffDoesNotRepeatModelLookups(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		agentID     string
		hasImages   bool
		vision      bool
		expectedSQL int
	}{
		{name: "auto_text", agentID: "auto", expectedSQL: 2},
		{name: "auto_image", agentID: "auto", hasImages: true, vision: true, expectedSQL: 2},
		{name: "explicit", expectedSQL: 1},
		{name: "default", agentID: "default", expectedSQL: 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, counter := testutil.NewStatementCountingTestDB(t)
			env := newTestHandlerEnv(t, db)
			project := createProjectTB(t, env.Handler, "Selection handoff "+scenario.name)
			clearModelConfigs(t, db)
			catalog := seedNewTaskSelectionModels(t, env.LLMConfigRepo, 3, scenario.vision)
			if scenario.name == "explicit" {
				scenario.agentID = catalog[1].ID
			}
			message := "build endpoint handler service database integration test"
			req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", nil)
			c := env.Echo.NewContext(req, httptest.NewRecorder())
			c.Set("newTaskThread", true)

			counter.Reset()
			counter.SetEnabled(true)
			preflight, err := env.Handler.selectTaskAgent(context.Background(), project.ID, scenario.agentID, message, scenario.hasImages)
			require.NoError(t, err)
			require.NotNil(t, preflight)
			if scenario.vision {
				require.Equal(t, catalog[2].ID, preflight.ID, "Auto image routing must select the vision-capable model")
			}
			c.Set(newTaskThreadSelectedAgentContextKey, preflight)
			handoff, err := env.Handler.selectTaskThreadAgent(c, project.ID, scenario.agentID, message, scenario.hasImages)
			counter.SetEnabled(false)
			require.NoError(t, err)
			require.Equal(t, preflight.ID, handoff.ID)
			require.Equal(t, scenario.expectedSQL, countNewTaskSelectionQueries(counter.Statements()), "selection SQL statements: %q", counter.Statements())
		})
	}
}

func TestHandler_NewTaskModelSelectionFailuresAreRejectedBeforePersistence(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		seedModels bool
		agentID    string
	}{
		{name: "no_available_models", agentID: "auto"},
		{name: "invalid_explicit_model", seedModels: true, agentID: "missing-model"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, _ := testutil.NewStatementCountingTestDB(t)
			env := newTestHandlerEnv(t, db)
			project := createProjectTB(t, env.Handler, "Rejected new task model")
			clearModelConfigs(t, db)
			if scenario.seedModels {
				seedNewTaskSelectionModels(t, env.LLMConfigRepo, 1, false)
			}

			form := url.Values{"message": {"Do the work"}, "agent_id": {scenario.agentID}}
			req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			env.Echo.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code)
			tasks, err := env.TaskRepo.ListByProject(context.Background(), project.ID, "")
			require.NoError(t, err)
			require.Empty(t, tasks)
		})
	}
}

func TestHandler_DirectTaskThreadStillSelectsFromRequest(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	env := newTestHandlerEnv(t, db)
	project := createProjectTB(t, env.Handler, "Direct thread selection")
	clearModelConfigs(t, db)
	model := seedNewTaskSelectionModels(t, env.LLMConfigRepo, 1, false)[0]
	task := &models.Task{ProjectID: project.ID, Title: "Direct follow-up", Prompt: "initial", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	require.NoError(t, env.TaskRepo.Create(context.Background(), task))
	form := url.Values{"message": {"Follow up"}, "agent_id": {model.ID}}
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/thread?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	counter.Reset()
	counter.SetEnabled(true)
	env.Echo.ServeHTTP(rec, req)
	counter.SetEnabled(false)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, countNewTaskSelectionQueries(counter.Statements()), "direct follow-up must make its own explicit model lookup")
	executions, err := env.ExecRepo.ListByTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	require.Equal(t, model.ID, executions[0].AgentConfigID)
}

func BenchmarkNewTaskModelSelectionHandoff(b *testing.B) {
	previousLogOutput := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogOutput)

	for _, catalogSize := range []int{1, 50, 200} {
		b.Run(fmt.Sprintf("models_%d", catalogSize), func(b *testing.B) {
			db, counter := testutil.NewStatementCountingTestDB(b)
			h, _, repo := setupTestHandlerForDB(b, db)
			project := createProjectTB(b, h, "New task model selection benchmark")
			clearModelConfigs(b, db)
			catalog := seedNewTaskSelectionModels(b, repo, catalogSize, true)
			for _, selectionMode := range []struct {
				name      string
				agentID   string
				hasImages bool
			}{
				{name: "auto_text", agentID: "auto"},
				{name: "auto_image", agentID: "auto", hasImages: true},
				{name: "explicit", agentID: catalog[catalogSize/2].ID},
				{name: "default", agentID: "default"},
			} {
				b.Run(selectionMode.name, func(b *testing.B) {
					for _, variant := range []struct {
						name  string
						reuse bool
					}{{name: "before", reuse: false}, {name: "after", reuse: true}} {
						b.Run(variant.name, func(b *testing.B) {
							ctx := newTaskSelectionBenchmarkContext(project.ID)
							selectOnce := func() *models.LLMConfig {
								selected, err := runNewTaskSelectionHandoff(h, ctx, project.ID, selectionMode.agentID, "build endpoint handler service database integration test", selectionMode.hasImages, variant.reuse)
								if err != nil {
									b.Fatal(err)
								}
								return selected
							}

							counter.Reset()
							counter.SetEnabled(true)
							selected := selectOnce()
							counter.SetEnabled(false)
							if selected == nil {
								b.Fatal("selection returned nil")
							}
							selectionSQL := countNewTaskSelectionQueries(counter.Statements())
							counter.Reset()
							selectOnce() // warm the same fixture before timing
							b.ReportAllocs()
							b.ResetTimer()
							for i := 0; i < b.N; i++ {
								newTaskSelectionBenchmarkSink = selectOnce()
							}
							b.StopTimer()
							b.ReportMetric(float64(selectionSQL), "selection-sql/op")
							b.ReportMetric(float64(medianNewTaskSelectionLatency(b, selectOnce)), "median-ns/op")
						})
					}
				})
			}
		})
	}
}

func newTaskSelectionBenchmarkContext(projectID string) echo.Context {
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+projectID+"&from=new&thread=1", nil)
	ctx := echo.New().NewContext(req, httptest.NewRecorder())
	ctx.Set("newTaskThread", true)
	ctx.SetParamNames("taskId")
	ctx.SetParamValues("benchmark-task")
	return ctx
}

func runNewTaskSelectionHandoff(h *Handler, c echo.Context, projectID, agentID, message string, hasImages, reuse bool) (*models.LLMConfig, error) {
	selected, err := h.selectTaskAgent(c.Request().Context(), projectID, agentID, message, hasImages)
	if err != nil {
		return nil, err
	}
	if !reuse {
		return h.selectTaskAgent(c.Request().Context(), projectID, agentID, message, hasImages)
	}
	c.Set(newTaskThreadSelectedAgentContextKey, selected)
	handoff, handoffErr := h.selectTaskThreadAgent(c, projectID, agentID, message, hasImages)
	c.Set(newTaskThreadSelectedAgentContextKey, nil)
	return handoff, handoffErr
}

func medianNewTaskSelectionLatency(b *testing.B, run func() *models.LLMConfig) int64 {
	b.Helper()
	const samples = 9
	const batch = 32
	latencies := make([]int64, 0, samples)
	for sample := 0; sample < samples; sample++ {
		start := time.Now()
		for i := 0; i < batch; i++ {
			if run() == nil {
				b.Fatal("selection returned nil")
			}
		}
		latencies = append(latencies, time.Since(start).Nanoseconds()/batch)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	return latencies[len(latencies)/2]
}

func seedNewTaskSelectionModels(tb testing.TB, repo *repository.LLMConfigRepo, count int, includeVisionModel bool) []*models.LLMConfig {
	tb.Helper()
	catalog := make([]*models.LLMConfig, 0, count)
	for i := 0; i < count; i++ {
		config := &models.LLMConfig{
			Name:      fmt.Sprintf("Selection model %03d", i),
			Provider:  models.ProviderTest,
			Model:     "test-model",
			MaxTokens: 4096,
			IsDefault: i == 0,
		}
		if includeVisionModel && i == count-1 {
			config.Name = "Vision selection model"
			config.Provider = models.ProviderAnthropic
			config.Model = "claude-sonnet-4-5-20250929"
			config.APIKey = "test-api-key"
			config.AuthMethod = models.AuthMethodAPIKey
		}
		if err := repo.Create(context.Background(), config); err != nil {
			tb.Fatalf("create selection model %d: %v", i, err)
		}
		catalog = append(catalog, config)
	}
	return catalog
}

func countNewTaskSelectionQueries(statements []string) int {
	count := 0
	for _, statement := range statements {
		normalized := strings.ToLower(strings.Join(strings.Fields(statement), " "))
		if strings.HasPrefix(normalized, "select id, name, provider, model, is_default, auth_method from agent_configs order by is_default desc, name asc") ||
			strings.HasPrefix(normalized, "select id, name, provider, model, auth_method, is_default, case when coalesce(api_key, '')") ||
			strings.HasPrefix(normalized, "select a.id, a.name, a.provider, a.model, a.reasoning_effort,") ||
			strings.HasPrefix(normalized, "select id, name, description, repo_path, repo_url, is_default, default_agent_config_id, max_workers") {
			count++
		}
	}
	return count
}
