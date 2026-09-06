package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/stretchr/testify/require"
)

func TestHandler_TaskThreadSteerRejectsTerminalTaskStates(t *testing.T) {
	cases := []struct {
		name     string
		status   models.TaskStatus
		category models.TaskCategory
	}{
		{name: "completed", status: models.StatusCompleted, category: models.CategoryCompleted},
		{name: "failed", status: models.StatusFailed, category: models.CategoryBacklog},
		{name: "cancelled", status: models.StatusCancelled, category: models.CategoryBacklog},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, e, llmConfigRepo := setupTestHandler(t)
			ctx := context.Background()
			agent := createAgent(t, llmConfigRepo)
			project := createProject(t, h, "Terminal Task Steering "+tc.name)
			task := createTask(t, h, project.ID, "Terminal Steering Task "+tc.name, func(task *models.Task) {
				task.Status = tc.status
				task.Category = tc.category
				task.AgentID = &agent.ID
			})

			form := url.Values{}
			form.Set("message", "steer a terminal task")
			form.Set("expected_turn_id", "stale-turn")
			rec := htmxPost(e, "/tasks/"+task.ID+"/thread/steer", form)

			assertCode(t, rec, http.StatusConflict)
			assertContains(t, rec, "no active response to steer")
			inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, inputs, "terminal task steering must not create a pending input")
		})
	}
}

func TestHandler_TaskThreadSteerRejectsMismatchedExpectedTurn(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Mismatched Scheduled Steering Project")
	task := createTask(t, h, project.ID, "Mismatched Scheduled Steering Task", func(task *models.Task) {
		task.Status = models.StatusRunning
		task.Category = models.CategoryScheduled
		task.AgentID = &agent.ID
	})
	active := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = "current scheduled turn"
	})

	form := url.Values{}
	form.Set("message", "use the current scheduled turn")
	form.Set("expected_turn_id", "different-turn")
	rec := htmxPost(e, "/tasks/"+task.ID+"/thread/steer", form)

	assertCode(t, rec, http.StatusConflict)
	assertContains(t, rec, "active turn changed; queue the message instead")
	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, inputs, "a stale expected-turn guard must not create steering input")
	stored, err := h.execRepo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecRunning, stored.Status)
}

func TestProcessStreamingResponse_ScheduledTaskConsumesPendingSteering(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Scheduled Steering Consumption Project")
	task := createTask(t, h, project.ID, "Scheduled Steering Consumption Task", func(task *models.Task) {
		task.Status = models.StatusRunning
		task.Category = models.CategoryScheduled
		task.AgentID = &agent.ID
	})
	active := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = "scheduled provider turn"
		exec.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: active.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         active.ID,
		ExpectedTurnID: active.ID,
		Content:        "keep the scheduled output compatible",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, active.ID))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "scheduled response with steering"
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         active.ID,
		TaskID:         task.ID,
		Message:        active.PromptSent,
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})

	require.Equal(t, 1, mock.CallCount())
	request := mock.LastAgentRequest()
	require.Contains(t, request.Message, active.PromptSent)
	require.Contains(t, request.Message, steering.Content)
	storedSteering, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, storedSteering.InputStatus)
	require.Equal(t, models.ThreadInputModeSteering, storedSteering.InputMode)
	storedExec, err := h.execRepo.GetByID(ctx, active.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecCompleted, storedExec.Status)
}

func TestHandler_PromoteQueuedTaskThreadInputAfterScheduledCompletion(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Scheduled Completion Promotion Project")
	task := createTask(t, h, project.ID, "Scheduled Completion Promotion Task", func(task *models.Task) {
		task.Status = models.StatusRunning
		task.Category = models.CategoryScheduled
		task.AgentID = &agent.ID
	})
	active := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = "scheduled execution"
	})
	queued := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: active.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "follow up after the scheduled run",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))

	require.Equal(t, repository.CompleteSuccessCompleted, h.completeWithSuccess(ctx, active.ID, task.ID, "scheduled output", "", 0, 1))
	storedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCompleted, storedTask.Status)
	require.Equal(t, models.CategoryScheduled, storedTask.Category, "scheduled task category must remain schedule-owned before promotion")

	started := make(chan string, 1)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "follow-up complete"
	mock.TextOnly = mock.Response
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		started <- call.Prompt
	}
	h.llmSvc.SetLLMCaller(mock)

	h.PromoteQueuedTaskThreadInput(task.ID)
	select {
	case got := <-started:
		require.Equal(t, queued.Content, got)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up after scheduled completion")
	}

	storedInput, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, storedInput.InputStatus)
	require.NotEqual(t, active.ID, storedInput.RunExecutionID)
	require.Eventually(t, func() bool {
		promotedExec, getErr := h.execRepo.GetByID(ctx, storedInput.RunExecutionID)
		return getErr == nil && promotedExec != nil && promotedExec.Status == models.ExecCompleted
	}, 2*time.Second, 10*time.Millisecond, "promoted scheduled follow-up did not reach a terminal success")
}

func TestTaskThreadScheduledSteeringFallsBackToNormalFollowupInChrome(t *testing.T) {
	chrome := handlerTestChromePath(t)
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Scheduled Browser Steering Project")
	task := createTask(t, h, project.ID, "Scheduled Browser Steering Task", func(task *models.Task) {
		task.Status = models.StatusRunning
		task.Category = models.CategoryScheduled
		task.AgentID = &agent.ID
	})
	stale := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = "stale scheduled turn"
	})

	var form bytes.Buffer
	require.NoError(t, components.ChatInputForm(components.ChatInputFormConfig{
		FormID:        "task-thread-form",
		InputID:       "task-message-input",
		PostEndpoint:  "/tasks/" + task.ID + "/thread",
		SteerEndpoint: "/tasks/" + task.ID + "/thread/steer",
		StopEndpoint:  "/tasks/" + task.ID + "/cancel?composer_stop=1",
		TargetID:      "task-thread-messages",
		TaskID:        task.ID,
		IsRunning:     true,
		ActiveTurnID:  stale.ID,
	}).Render(ctx, &form))

	current := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = "current scheduled turn"
	})

	var mu sync.Mutex
	type browserRequest struct {
		path   string
		form   url.Values
		status int
	}
	var requests []browserRequest
	htmxBytes := handlerPinnedHTMX(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/htmx.js" {
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmxBytes)
			return
		}
		requestIndex := -1
		if r.Method == http.MethodPost {
			_ = r.ParseForm()
			mu.Lock()
			requests = append(requests, browserRequest{path: r.URL.Path, form: r.PostForm.Clone()})
			requestIndex = len(requests) - 1
			mu.Unlock()
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprintf(w, `<!doctype html><html><body><script src="/htmx.js"></script><div id="task-thread-messages"><div data-execution-pair="true" data-exec-status="running" data-exec-id="%s"></div></div>%s<div id="browser-result" data-test-result="pending"></div><script>
(async function() {
  function fail(message) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', message); throw new Error(message); }
  function key(input, options) { input.dispatchEvent(new KeyboardEvent('keydown', Object.assign({key:'Enter', bubbles:true, cancelable:true}, options || {}))); }
  function waitForQueuedRow() {
    return new Promise(function(resolve, reject) {
      var started = Date.now();
      function check() {
        var row = document.querySelector('#task-thread-form [data-input-mode="queued"]');
        if (row) { resolve(row); return; }
        if (Date.now() - started > 5000) { reject(new Error('normal follow-up replay did not render a queued row')); return; }
        setTimeout(check, 25);
      }
      check();
    });
  }
  if (document.readyState === 'loading') {
    await new Promise(function(resolve) { document.addEventListener('DOMContentLoaded', resolve, {once: true}); });
  }
  var input = document.getElementById('task-message-input');
  var apple = input.placeholder.indexOf('⌘+⏎ steers') !== -1;
  input.value = 'queue after stale scheduled steer';
  key(input, apple ? {metaKey:true} : {ctrlKey:true});
  var row = await waitForQueuedRow();
  if (row.textContent.indexOf('queue after stale scheduled steer') === -1) fail('queued row contains the wrong follow-up');
  document.body.setAttribute('data-test-result', 'pass');
})().catch(function(error) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', String(error && error.stack || error)); });
</script></body></html>`, stale.ID, form.String())
			return
		}
		response := httptest.NewRecorder()
		e.ServeHTTP(response, r)
		if requestIndex >= 0 {
			mu.Lock()
			requests[requestIndex].status = response.Code
			mu.Unlock()
		}
		for key, values := range response.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()

	runHandlerChromeFixture(t, chrome, server.URL+"/", "scheduled task-thread steer fallback", 10000, 25*time.Second)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requests, 2, "the browser must first steer, then replay exactly one normal follow-up")
	require.Equal(t, "/tasks/"+task.ID+"/thread/steer", requests[0].path)
	require.Equal(t, http.StatusConflict, requests[0].status)
	require.Equal(t, "/tasks/"+task.ID+"/thread", requests[1].path)
	require.Equal(t, http.StatusOK, requests[1].status)
	require.Equal(t, "queue after stale scheduled steer", requests[0].form.Get("message"))
	require.Equal(t, stale.ID, requests[0].form.Get("expected_turn_id"))
	require.Equal(t, "queue after stale scheduled steer", requests[1].form.Get("message"))

	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Equal(t, models.ThreadInputModeQueued, inputs[0].InputMode)
	require.Equal(t, models.ThreadInputPending, inputs[0].InputStatus)
	require.Equal(t, current.ID, inputs[0].RunExecutionID)
	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2, "normal fallback must queue behind the scheduled execution without creating a concurrent execution")
}

func handlerPinnedHTMX(t *testing.T) []byte {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve handler browser test source path")
	}
	path := filepath.Join(filepath.Dir(sourceFile), "..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}
	return contents
}

func handlerTestChromePath(t *testing.T) string {
	t.Helper()
	const macChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(macChrome); err == nil {
		return macChrome
	}
	for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	t.Skip("Chrome or Chromium is required for scheduled task-thread browser integration coverage")
	return ""
}

func runHandlerChromeFixture(t *testing.T, chrome, targetURL, name string, virtualTimeBudget int, timeout time.Duration) {
	t.Helper()
	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "chrome-stdout.html")
	stderrPath := filepath.Join(tempDir, "chrome-stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout file: %v", err)
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		_ = stdoutFile.Close()
		t.Fatalf("create Chrome stderr file: %v", err)
	}

	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(tempDir, "chrome-profile"),
		fmt.Sprintf("--virtual-time-budget=%d", virtualTimeBudget),
		"--dump-dom",
		targetURL,
	)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		t.Fatalf("start Chrome %s fixture: %v", name, err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	deadline := time.Now().Add(timeout)
	var result string
	for time.Now().Before(deadline) {
		if output, readErr := os.ReadFile(stdoutPath); readErr == nil {
			result = string(output)
			if strings.Contains(result, `data-test-result="pass"`) || strings.Contains(result, `data-test-result="fail"`) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	// --dump-dom writes its result immediately before Chrome exits. Give the
	// browser a chance to shut down its profile-writing children naturally so
	// TempDir cleanup cannot race a late Default-directory write.
	select {
	case <-waitDone:
	case <-time.After(2 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waitDone
	}
	_ = stdoutFile.Close()
	_ = stderrFile.Close()

	if strings.Contains(result, `data-test-result="pass"`) {
		return
	}
	stderr, _ := os.ReadFile(stderrPath)
	if len(result) > 5000 {
		result = result[len(result)-5000:]
	}
	if len(stderr) > 5000 {
		stderr = stderr[len(stderr)-5000:]
	}
	t.Fatalf("real %s fixture failed:\nDOM tail:\n%s\nChrome stderr tail:\n%s", name, result, stderr)
}
