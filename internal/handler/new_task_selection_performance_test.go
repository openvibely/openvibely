package handler

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

func TestHandler_NewTaskFirstMessageReusesPreflightSelection(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		agentMode string
		images    bool
	}{
		{name: "auto_text", agentMode: "auto"},
		{name: "auto_image", agentMode: "auto", images: true},
		{name: "explicit", agentMode: "explicit"},
		{name: "default", agentMode: "default"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			env := newTestHandlerEnv(t, db)
			env.Handler.startStreamingResponseOverride = func(streamingResponseParams) error { return nil }
			project := createProjectTB(t, env.Handler, "New task selection "+scenario.name)
			clearModelConfigs(t, db)
			catalog := seedNewTaskSelectionModels(t, env.LLMConfigRepo, 3, scenario.images)
			selectionAgentID := scenario.agentMode
			if scenario.agentMode == "explicit" {
				selectionAgentID = catalog[1].ID
			}
			message := "build endpoint handler service database integration test"
			selectedBefore, err := env.Handler.selectTaskAgent(context.Background(), project.ID, selectionAgentID, message, scenario.images)
			require.NoError(t, err)
			require.NotNil(t, selectedBefore)
			require.Equal(t, scenario.images, selectedBefore.Provider == models.ProviderAnthropic, "fixture should exercise image-aware model selection")

			form := url.Values{"message": {message}, "agent_id": {selectionAgentID}}
			if scenario.images {
				sessionID := repository.NewID()
				form.Set("attachment_session_id", sessionID)
				seedNewTaskSelectionImage(t, sessionID)
			}
			rec, err := runNewTaskCreateHandler(env.Handler, env.Echo, project.ID, form, false)
			require.NoError(t, err)
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
			inputs, err := env.Handler.threadInputRepo.ListPendingForTask(context.Background(), taskID)
			require.NoError(t, err)
			require.Empty(t, inputs, "a direct first-turn admission must not also create a queued input")
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
		{name: "default", agentID: "default", expectedSQL: 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			db, counter := testutil.NewStatementCountingTestDB(t)
			env := newTestHandlerEnv(t, db)
			env.Handler.startStreamingResponseOverride = func(streamingResponseParams) error { return nil }
			project := createProjectTB(t, env.Handler, "Selection handoff "+scenario.name)
			clearModelConfigs(t, db)
			catalog := seedNewTaskSelectionModels(t, env.LLMConfigRepo, 3, scenario.vision)
			if scenario.name == "explicit" {
				scenario.agentID = catalog[1].ID
			}
			form := url.Values{
				"message":  {"build endpoint handler service database integration test"},
				"agent_id": {scenario.agentID},
			}
			if scenario.hasImages {
				sessionID := repository.NewID()
				form.Set("attachment_session_id", sessionID)
				seedNewTaskSelectionImage(t, sessionID)
			}

			counter.Reset()
			counter.SetEnabled(true)
			rec, err := runNewTaskCreateHandler(env.Handler, env.Echo, project.ID, form, false)
			counter.SetEnabled(false)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, scenario.expectedSQL, countNewTaskSelectionQueries(counter.Statements()), "selection SQL statements: %q", counter.Statements())
			executions, err := env.ExecRepo.ListByTask(context.Background(), rec.Header().Get("X-Created-Task-ID"))
			require.NoError(t, err)
			require.Len(t, executions, 1)
			if scenario.vision {
				require.Equal(t, catalog[2].ID, executions[0].AgentConfigID, "first send must keep image-aware routing")
			}
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
	env.Handler.startStreamingResponseOverride = func(streamingResponseParams) error { return nil }
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

func BenchmarkNewTaskFirstSubmitHandlerPath(b *testing.B) {
	previousLogOutput := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogOutput)

	for _, catalogSize := range []int{1, 50, 200} {
		b.Run(fmt.Sprintf("models_%d", catalogSize), func(b *testing.B) {
			db, counter := testutil.NewStatementCountingTestDB(b)
			env := newTestHandlerEnv(b, db)
			h := env.Handler
			project := createProjectTB(b, h, "New task first-submit benchmark")
			clearModelConfigs(b, db)
			catalog := seedNewTaskSelectionModels(b, env.LLMConfigRepo, catalogSize, true)
			h.startStreamingResponseOverride = func(streamingResponseParams) error { return nil }
			h.processTaskThreadAttachmentsOverride = func(context.Context, string, string) (string, []models.Attachment, []models.ChatAttachment, error) {
				return "", nil, nil, nil
			}

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
					beforeLatencies := make([]int64, 0, b.N)
					afterLatencies := make([]int64, 0, b.N)
					pairedLatencyDeltas := make([]int64, 0, b.N)
					var beforeBytes, afterBytes uint64
					var beforeAllocs, afterAllocs uint64
					var beforeSQL, afterSQL int64
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						order := [2]bool{true, false}
						if i%2 == 1 {
							order = [2]bool{false, true}
						}
						var pairBefore, pairAfter int64
						for _, forceReselection := range order {
							b.StopTimer()
							form := url.Values{
								"message":  {"build endpoint handler service database integration test"},
								"agent_id": {selectionMode.agentID},
							}
							pendingSessionID := ""
							if selectionMode.hasImages {
								pendingSessionID = repository.NewID()
								form.Set("attachment_session_id", pendingSessionID)
								seedNewTaskSelectionImage(b, pendingSessionID)
							}
							ctx, rec := newTaskCreateEchoContext(env.Echo, project.ID, form, forceReselection)
							counter.Reset()
							counter.SetEnabled(true)
							var memBefore, memAfter runtime.MemStats
							runtime.ReadMemStats(&memBefore)
							b.StartTimer()
							start := time.Now()
							err := h.CreateTask(ctx)
							latency := time.Since(start).Nanoseconds()
							b.StopTimer()
							runtime.ReadMemStats(&memAfter)
							counter.SetEnabled(false)
							if err != nil {
								b.Fatalf("CreateTask: %v", err)
							}
							if rec.Code != http.StatusOK {
								b.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
							}
							if !strings.Contains(rec.Body.String(), `data-loaded="true"`) || strings.Contains(rec.Body.String(), "Thread is loading...") {
								b.Fatal("first-submit response did not contain the preloaded task thread")
							}
							taskID := rec.Header().Get("X-Created-Task-ID")
							if taskID == "" {
								b.Fatal("first-submit response omitted X-Created-Task-ID")
							}
							executions, err := env.ExecRepo.ListByTask(context.Background(), taskID)
							if err != nil || len(executions) != 1 {
								b.Fatalf("first-submit executions = %d, err=%v; want one", len(executions), err)
							}
							statements := counter.Statements()
							if forceReselection {
								pairBefore = latency
								beforeBytes += memAfter.TotalAlloc - memBefore.TotalAlloc
								beforeAllocs += memAfter.Mallocs - memBefore.Mallocs
								beforeSQL += int64(countNewTaskSelectionQueries(statements))
							} else {
								pairAfter = latency
								afterBytes += memAfter.TotalAlloc - memBefore.TotalAlloc
								afterAllocs += memAfter.Mallocs - memBefore.Mallocs
								afterSQL += int64(countNewTaskSelectionQueries(statements))
							}
							if err := env.TaskRepo.Delete(context.Background(), taskID); err != nil {
								b.Fatalf("delete benchmark task: %v", err)
							}
							for _, execution := range executions {
								_ = os.RemoveAll(filepath.Join(uploadsDir, "chat", execution.ID))
							}
							if pendingSessionID != "" {
								_ = os.RemoveAll(filepath.Join(uploadsDir, "chat", "pending", pendingSessionID))
							}
						}
						beforeLatencies = append(beforeLatencies, pairBefore)
						afterLatencies = append(afterLatencies, pairAfter)
						pairedLatencyDeltas = append(pairedLatencyDeltas, pairAfter-pairBefore)
					}
					b.StopTimer()
					b.ReportMetric(float64(medianInt64(beforeLatencies)), "median-before-ns/op")
					b.ReportMetric(float64(medianInt64(afterLatencies)), "median-after-ns/op")
					b.ReportMetric(float64(medianInt64(pairedLatencyDeltas)), "median-paired-delta-ns/op")
					b.ReportMetric(float64(beforeBytes)/float64(b.N), "before-B/op")
					b.ReportMetric(float64(afterBytes)/float64(b.N), "after-B/op")
					b.ReportMetric(float64(beforeAllocs)/float64(b.N), "before-allocs/op")
					b.ReportMetric(float64(afterAllocs)/float64(b.N), "after-allocs/op")
					b.ReportMetric(float64(beforeSQL)/float64(b.N), "before-selection-sql/op")
					b.ReportMetric(float64(afterSQL)/float64(b.N), "after-selection-sql/op")
				})
			}
		})
	}
}

func medianInt64(values []int64) int64 {
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values[len(values)/2]
}

func newTaskCreateEchoContext(e *echo.Echo, projectID string, form url.Values, forceReselection bool) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+projectID+"&from=new&thread=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)
	if forceReselection {
		ctx.Set(newTaskThreadForceReselectionContextKey, true)
	}
	return ctx, rec
}

func runNewTaskCreateHandler(h *Handler, e *echo.Echo, projectID string, form url.Values, forceReselection bool) (*httptest.ResponseRecorder, error) {
	ctx, rec := newTaskCreateEchoContext(e, projectID, form, forceReselection)
	return rec, h.CreateTask(ctx)
}

func seedNewTaskSelectionImage(tb testing.TB, sessionID string) {
	tb.Helper()
	pendingDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	if err := os.MkdirAll(pendingDir, 0o700); err != nil {
		tb.Fatalf("create benchmark pending image directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pendingDir, "draft.png"), []byte("image"), 0o600); err != nil {
		tb.Fatalf("write benchmark pending image: %v", err)
	}
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
			strings.HasPrefix(normalized, "select a.id, a.name, a.provider, a.model, a.reasoning_effort,") {
			count++
		}
	}
	return count
}
