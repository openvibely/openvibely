package handler

import (
	"context"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComputerTakeoverInterruptsAndQueuesOnce(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	p := createProject(t, h, "Takeover")
	task := createTask(t, h, p.ID, "Interrupted", func(t *models.Task) {
		t.Status = models.StatusRunning
		t.Category = models.CategoryActive
		t.AgentID = &agent.ID
	})
	execution := createExec(t, h, task.ID, agent.ID, func(e *models.Execution) { e.Status = models.ExecRunning })
	running, cancel := context.WithCancel(t.Context())
	defer cancel()
	registration := h.workerSvc.RegisterCancelOwned(task.ID, cancel)
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer bridge.Close()
	h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL}})
	scopedComputer, scopeErr := h.computerSvc.ForTask(t.Context(), p.ID, task.ID)
	require.NoError(t, scopeErr)
	if err := scopedComputer.SetEnabled(t.Context(), p.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, scopedComputer.SetTaskEnabled(t.Context(), p.ID, true))
	e := echo.New()
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	e.GET("/projects/:id/computer/status", h.ProjectComputerStatus)
	e.GET("/projects/:id/computer/stream/:mode", h.ProjectComputerStream)
	send := func(action string) {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest("POST", "/projects/"+p.ID+"/computer/"+action+"?task_id="+task.ID, nil))
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	finished := make(chan struct{})
	go func() { send("takeover"); close(finished) }()
	<-running.Done()
	select {
	case <-finished:
		t.Fatal("takeover returned before run cleanup")
	case <-time.After(25 * time.Millisecond):
	}
	statusResponse := httptest.NewRecorder()
	e.ServeHTTP(statusResponse, httptest.NewRequest("GET", "/projects/"+p.ID+"/computer/status?task_id="+task.ID, nil))
	require.Contains(t, statusResponse.Body.String(), `"takeover_pending":true`)
	require.Contains(t, statusResponse.Body.String(), `"held":false`)
	inputResponse := httptest.NewRecorder()
	e.ServeHTTP(inputResponse, httptest.NewRequest("GET", "/projects/"+p.ID+"/computer/stream/control?task_id="+task.ID, nil))
	require.Equal(t, http.StatusConflict, inputResponse.Code)
	h.workerSvc.DeregisterCancelOwned(task.ID, registration)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("takeover did not finish after cleanup")
	}
	require.ErrorIs(t, running.Err(), context.Canceled)
	send("takeover")
	inputs, err := h.threadInputRepo.ListPendingForTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Empty(t, inputs)
	send("release")
	send("release")
	inputs, err = h.threadInputRepo.ListPendingForTask(t.Context(), task.ID)
	require.NoError(t, err)
	require.Len(t, inputs, 1)
	require.Contains(t, inputs[0].Content, "Continue the original task")
	require.NoError(t, h.execRepo.Complete(t.Context(), execution.ID, models.ExecCancelled, "", "Paused for takeover", 0, 0))
	require.NoError(t, h.taskRepo.UpdateStatus(t.Context(), task.ID, models.StatusCancelled))
	h.workerSvc = nil
	mock := testutil.NewMockLLMCaller()
	mock.Response = "Continued"
	mock.TextOnly = mock.Response
	started := make(chan testutil.MockLLMCall, 1)
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) { started <- call }
	h.llmSvc.SetLLMCaller(mock)
	h.PromoteQueuedTaskThreadInput(task.ID)
	select {
	case call := <-started:
		require.Contains(t, call.Prompt, "Continue the original task")
	case <-time.After(5 * time.Second):
		t.Fatal("handback continuation never reached model")
	}
	require.Eventually(t, func() bool {
		rows, err := h.execRepo.ListByTaskChronologicalLimit(t.Context(), task.ID, 10)
		if err != nil {
			return false
		}
		for _, row := range rows {
			if row.ID != execution.ID && row.Status == models.ExecCompleted {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

}

func TestComputerTakeoverReleasesQueuedTaskAdmission(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	p := createProject(t, h, "Queued takeover")
	task := createTask(t, h, p.ID, "Queued assignment", func(task *models.Task) {
		task.Status = models.StatusQueued
		task.Category = models.CategoryActive
		task.AgentID = &agent.ID
	})
	require.NoError(t, h.taskSvc.InterruptForComputer(t.Context(), task.ID))
	saved, err := h.taskRepo.GetByID(t.Context(), task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, saved.Status)
}

func TestComputerTakeoverWaitsForAlreadyStoppedTask(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	project := createProject(t, h, "Stopping run")
	task := createTask(t, h, project.ID, "Stopping", func(task *models.Task) { task.Status = models.StatusRunning })
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	registration := h.workerSvc.RegisterCancelOwned(task.ID, cancel)
	require.True(t, h.workerSvc.CancelRunningTask(task.ID))
	require.ErrorIs(t, run.Err(), context.Canceled)
	require.NoError(t, h.taskRepo.UpdateStatus(t.Context(), task.ID, models.StatusCancelled))
	wait, cancelWait := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelWait()
	require.ErrorIs(t, h.taskSvc.InterruptForComputer(wait, task.ID), context.DeadlineExceeded)
	h.workerSvc.DeregisterCancelOwned(task.ID, registration)
	require.NoError(t, h.taskSvc.InterruptForComputer(t.Context(), task.ID))
}

func TestComputerCancellationPropagatesAcrossRuntimeRegistration(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	wait, cancelWait := context.WithCancel(t.Context())
	defer cancelWait()
	original := h.registerTaskCancellation("transition", cancelWait)
	runtime, cancelRuntime, _ := withInactivityTimeout(wait, time.Minute)
	defer cancelRuntime()
	// Takeover captured the wait registration just before the runtime replaces it.
	done := h.workerSvc.CancelForComputer("transition")
	replacement := h.registerTaskCancellation("transition", cancelRuntime)
	select {
	case <-runtime.Done():
	case <-time.After(time.Second):
		t.Fatal("runtime did not inherit cancellation")
	}
	require.ErrorIs(t, runtime.Err(), context.Canceled)
	select {
	case <-done:
		t.Fatal("run has not cleaned up")
	default:
	}
	h.deregisterTaskCancellation("transition", replacement)
	h.deregisterTaskCancellation("transition", original)
	select {
	case <-done:
	default:
		t.Fatal("completion acknowledgement missing")
	}
}

// Hold terminal persistence open so completion acknowledgement ordering is observable.
type computerBlockingCompletionStore struct {
	persistence.ExecutionStore
	entered chan struct{}
	release chan struct{}
}

func (s *computerBlockingCompletionStore) Complete(ctx context.Context, id string, status models.ExecutionStatus, output, errText string, tokens int, duration int64) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.ExecutionStore.Complete(ctx, id, status, output, errText, tokens, duration)
}
func TestComputerCapacityWaitAcknowledgesAfterFinalization(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Capacity cancellation cleanup")
	task := createTask(t, h, project.ID, "Waiting task", func(task *models.Task) {
		task.Category = models.CategoryActive
		task.Status = models.StatusQueued
		task.AgentID = &agent.ID
	})
	execution := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning; ex.IsFollowup = true })
	h.workerSvc.Resize(1)
	require.True(t, h.workerSvc.TryAcquireProjectSlot(project.ID))
	defer h.workerSvc.ReleaseProjectSlot(project.ID)
	block := &computerBlockingCompletionStore{ExecutionStore: h.execRepo, entered: make(chan struct{}), release: make(chan struct{})}
	h.execRepo = block
	released := false
	defer func() {
		if !released {
			close(block.release)
		}
	}()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		h.processStreamingResponse(streamingResponseParams{ExecID: execution.ID, TaskID: task.ID, ProjectID: project.ID, Agent: *agent, Message: "waiting", IsTaskFollowup: true})
	}()
	var acknowledged <-chan struct{}
	require.Eventually(t, func() bool { acknowledged = h.workerSvc.CancelForComputer(task.ID); return acknowledged != nil }, time.Second, time.Millisecond)
	select {
	case <-block.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach finalization")
	}
	select {
	case <-acknowledged:
		t.Fatal("takeover acknowledged before finalization")
	default:
	}
	close(block.release)
	released = true
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("follow-up failed to finish")
	}
	select {
	case <-acknowledged:
	default:
		t.Fatal("completion was not acknowledged")
	}
	saved, err := h.execRepo.GetByID(t.Context(), execution.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecCancelled, saved.Status)
}
