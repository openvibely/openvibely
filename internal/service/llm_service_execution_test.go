package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type captureProviderAdapter struct {
	mu       sync.Mutex
	lastReq  llmcontracts.AgentRequest
	requests []llmcontracts.AgentRequest
}

type taskSteeringProviderAdapter struct {
	steering string
}

type providerAdapterFunc func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error)

type retryingProviderAdapterFunc func(llmcontracts.AgentRequest) (llmcontracts.AgentResult, error)

func (f providerAdapterFunc) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	return f(req)
}

func (f retryingProviderAdapterFunc) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	return callProviderOnce(func() (llmcontracts.AgentResult, error) {
		return f(req)
	})
}

type runtimeToolWritingLLMCaller struct {
	workDir string
}

type fileWritingLLMCaller struct {
	fileName string
	content  string
	workDir  string
}

type completionPathWritingLLMCaller struct {
	fileName string
	content  string
	workDir  string
}

func (c *completionPathWritingLLMCaller) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	if strings.HasPrefix(prompt, "Write one concise git commit subject") {
		return `{"subject":"Update stale fixture"}`, `{"subject":"Update stale fixture"}`, 1, nil
	}
	c.workDir = workDir
	if err := os.WriteFile(filepath.Join(workDir, c.fileName), []byte(c.content), 0644); err != nil {
		return "", "", 0, err
	}
	return "changed files\n[STATUS: SUCCESS]", "changed files\n[STATUS: SUCCESS]", 10, nil
}

func (c *fileWritingLLMCaller) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	c.workDir = workDir
	if err := os.WriteFile(filepath.Join(workDir, c.fileName), []byte(c.content), 0644); err != nil {
		return "", "", 0, err
	}
	return "changed files\n[STATUS: SUCCESS]", "changed files\n[STATUS: SUCCESS]", 10, nil
}

func (c *runtimeToolWritingLLMCaller) CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	c.workDir = workDir
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	if rt == nil || rt.Executor == nil {
		return "", "", 0, fmt.Errorf("missing runtime tools")
	}
	payload, _ := json.Marshal(map[string]string{"file_path": "scoped.txt", "content": "from scoped runtime"})
	_, _, _, err := rt.Executor(ctx, "write_file", payload)
	if err != nil {
		return "", "", 0, err
	}
	return "scoped task response", "scoped task response", 10, nil
}

func (c *captureProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	c.mu.Lock()
	c.lastReq = req
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	return llmcontracts.AgentResult{
		Output:         "ok",
		TextOnlyOutput: "ok",
		Usage:          llmcontracts.Usage{TotalTokens: 1},
	}, nil
}

func (c *captureProviderAdapter) Requests() []llmcontracts.AgentRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]llmcontracts.AgentRequest, len(c.requests))
	copy(out, c.requests)
	return out
}

func (a *taskSteeringProviderAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	if callback := llmcontracts.SteeringCallbackFromContext(req.Ctx); callback != nil {
		steering, err := callback(req.Ctx)
		if err != nil {
			return llmcontracts.AgentResult{}, err
		}
		a.steering = steering
	}
	return llmcontracts.AgentResult{
		Output:         "task output",
		TextOnlyOutput: "task output",
		Usage:          llmcontracts.Usage{TotalTokens: 1},
	}, nil
}

func TestLLMService_ExecuteTaskWithAgent_PublishesInitialThreadExecutionStarted(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	broadcaster := events.NewBroadcaster()
	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Initial Thread Event Project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Initial thread event", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "initial prompt", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetBroadcaster(broadcaster)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	svc.SetLLMCaller(mock)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.ID == "" {
		t.Fatalf("expected execution with id, got %#v", exec)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-sub:
			if ev.Type != events.TaskThreadExecutionStarted {
				continue
			}
			if ev.TaskID != task.ID || ev.ProjectID != project.ID || ev.ExecID != exec.ID || ev.Message != task.Prompt {
				t.Fatalf("unexpected start event: %#v", ev)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for initial task thread execution started event")
		}
	}
}

func TestLLMService_ExecuteTaskWithAgent_PublishesCompletedTerminalEvent(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Completed terminal stream project", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Completed terminal stream", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "initial prompt", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	hub := events.NewExecutionStreamHub()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetExecutionStreamHub(hub)
	called := make(chan string, 1)
	release := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "worker complete"
	mock.TextOnly = "worker complete"
	mock.OnCall = func(ctx context.Context, call testutil.MockLLMCall) {
		called <- call.ExecID
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	svc.SetLLMCaller(mock)
	type executeResult struct {
		exec *models.Execution
		err  error
	}
	resultCh := make(chan executeResult, 1)
	go func() {
		exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
		resultCh <- executeResult{exec: exec, err: err}
	}()

	var execID string
	select {
	case execID = <-called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for mock LLM call")
	}
	sub, _, err := hub.Subscribe(execID)
	if err != nil {
		t.Fatalf("subscribe execution stream: %v", err)
	}
	close(release)

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("ExecuteTaskWithAgent: %v", result.err)
		}
		if result.exec == nil || result.exec.ID != execID || result.exec.Status != models.ExecCompleted {
			t.Fatalf("unexpected execution result: %+v", result.exec)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for ExecuteTaskWithAgent")
	}
	select {
	case event, ok := <-sub:
		if !ok || event.ExecID != execID || event.Type != events.ExecutionStreamDone || event.Status != string(models.ExecCompleted) {
			t.Fatalf("terminal event = %+v, open=%v", event, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for completed terminal event")
	}
	if _, ok := <-sub; ok {
		t.Fatal("subscriber remained open after completed terminal event")
	}
}

func TestLLMService_ExecuteTaskWithAgent_PromotesQueuedTaskThreadInputAfterCompletion(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Worker Queue Promotion Service", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Worker queue promotion", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "initial prompt", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued follow-up"}
	if err := threadInputRepo.CreateQueued(ctx, queued); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}
	promoted := make(chan string, 1)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "worker complete"
	mock.TextOnly = "worker complete"
	svc.SetLLMCaller(mock)
	svc.SetQueuedTaskThreadPromoter(func(taskID string) { promoted <- taskID })

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected completed worker execution, got %#v", exec)
	}
	select {
	case got := <-promoted:
		if got != task.ID {
			t.Fatalf("expected promoted task %s, got %s", task.ID, got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued task-thread promoter")
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted || updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("promoter should run after worker terminal state is committed, got status=%s category=%s", updatedTask.Status, updatedTask.Category)
	}
	stored, err := threadInputRepo.GetByID(ctx, queued.ID)
	if err != nil {
		t.Fatalf("GetByID queued: %v", err)
	}
	if stored.InputStatus != models.ThreadInputPending {
		t.Fatalf("service callback wiring should not itself claim queued input, got %s", stored.InputStatus)
	}
}

func TestLLMService_ExecuteTaskWithAgent_PreservesFailedTranscriptForTaskThreadFollowup(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Failed Thread Context", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Preserve failure", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original task context", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "I inspected the repository and could not finish.\n[STATUS: FAILED | tests failed]"
	mock.TextOnly = mock.Response
	mock.Tokens = 12
	svc.SetLLMCaller(mock)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecFailed {
		t.Fatalf("expected failed execution, got %#v", exec)
	}
	stored, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID execution: %v", err)
	}
	if stored.PromptSent != "original task context" {
		t.Fatalf("failed execution prompt was not preserved: %q", stored.PromptSent)
	}
	if stored.ErrorMessage != "tests failed" {
		t.Fatalf("failed execution metadata was not preserved: %q", stored.ErrorMessage)
	}
	if !strings.Contains(stored.Output, "I inspected the repository and could not finish.") {
		t.Fatalf("failed execution assistant transcript was not preserved: %q", stored.Output)
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID task: %v", err)
	}
	if updatedTask.Status != models.StatusFailed || updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected terminal failed task in completed category, got status=%s category=%s", updatedTask.Status, updatedTask.Category)
	}

	history, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderTest: capture}
	res, err := svc.CallAgentDirectStreamingDetailed(ctx, "follow up with the prior failure context", nil, *agent, "followup-exec", history, "thread context", project.RepoPath, nil, true)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed: %v", err)
	}
	got := res.ChatContext.Messages
	want := []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "follow up with the prior failure context"},
		{Role: "assistant", Content: "ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected current-turn lifecycle context\n got: %#v\nwant: %#v", got, want)
	}
	if len(capture.lastReq.ChatHistory) != 1 || capture.lastReq.ChatHistory[0].Status != models.ExecFailed {
		t.Fatalf("provider request should include failed terminal execution history, got %#v", capture.lastReq.ChatHistory)
	}
}

func TestLLMService_ExecuteTaskWithAgent_FailsEmptySuccessfulResponse(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Empty Provider Response", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Arithmetic", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "4+2?", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected empty response error")
	}
	if exec == nil || exec.Status != models.ExecFailed {
		t.Fatalf("expected failed execution, got %#v", exec)
	}
	if exec.ErrorMessage != "model returned empty response" {
		t.Fatalf("unexpected error message: %q", exec.ErrorMessage)
	}
	stored, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID execution: %v", err)
	}
	if stored.Status != models.ExecFailed || stored.Output != "" || stored.ErrorMessage != "model returned empty response" {
		t.Fatalf("unexpected stored execution: status=%s output=%q error=%q", stored.Status, stored.Output, stored.ErrorMessage)
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID task: %v", err)
	}
	if updatedTask.Status != models.StatusFailed || updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected failed task in completed category, got status=%s category=%s", updatedTask.Status, updatedTask.Category)
	}
}

func TestLLMService_ExecuteTaskWithAgent_IgnoresStatusMarkerMentionInProse(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Codex Marker Prose", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Explain marker syntax", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "explain why a task failed", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "The previous execution mentioned the literal marker `[STATUS: FAILED | ...]` in prose.\n\nValidation passed."
	mock.TextOnly = mock.Response
	svc.SetLLMCaller(mock)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected marker mention in prose to complete, got %#v", exec)
	}
	stored, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID execution: %v", err)
	}
	if stored.Status != models.ExecCompleted || stored.ErrorMessage != "" {
		t.Fatalf("expected completed execution without failure metadata, got status=%s error=%q", stored.Status, stored.ErrorMessage)
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted || updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected completed task, got status=%s category=%s", updatedTask.Status, updatedTask.Category)
	}
}

func TestLLMService_ExecuteTaskWithAgent_IgnoresIncompleteStatusControl(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Incomplete Status Control", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Ignore incomplete status", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "complete the work", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "Work is complete.\n[STATUS: FAILED | incomplete control"
	mock.TextOnly = mock.Response
	svc.SetLLMCaller(mock)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted || exec.ErrorMessage != "" {
		t.Fatalf("expected incomplete control to remain inert, got %#v", exec)
	}
	stored, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID execution: %v", err)
	}
	if stored.Status != models.ExecCompleted || stored.ErrorMessage != "" || !strings.Contains(stored.Output, "[STATUS: FAILED | incomplete control") {
		t.Fatalf("expected completed execution preserving incomplete control, got status=%s error=%q output=%q", stored.Status, stored.ErrorMessage, stored.Output)
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByID task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted || updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected completed task, got status=%s category=%s", updatedTask.Status, updatedTask.Category)
	}
}

func TestLLMService_ExecuteTaskWithAgent_IgnoresExtraStatusReasonDelimiters(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
	}{
		{name: "failed", output: "Work is complete.\n[STATUS: FAILED | reason | extra]"},
		{name: "followup", output: "Work is complete.\n[STATUS: NEEDS_FOLLOWUP | reason | extra]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			llmConfigRepo := repository.NewLLMConfigRepo(db)
			execRepo := repository.NewExecutionRepo(db)
			taskRepo := repository.NewTaskRepo(db, nil)
			projectRepo := repository.NewProjectRepo(db)
			scheduleRepo := repository.NewScheduleRepo(db)
			attachmentRepo := repository.NewAttachmentRepo(db)
			agent := ensureDefaultAgent(t, llmConfigRepo)
			project := &models.Project{Name: "Extra Status Delimiter " + tc.name, RepoPath: t.TempDir()}
			if err := projectRepo.Create(ctx, project); err != nil {
				t.Fatalf("create project: %v", err)
			}
			task := &models.Task{ProjectID: project.ID, Title: "Ignore extra status delimiter", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "complete the work", AgentID: &agent.ID}
			if err := taskRepo.Create(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
			mock := testutil.NewMockLLMCaller()
			mock.Response = tc.output
			mock.TextOnly = tc.output
			svc.SetLLMCaller(mock)

			exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
			if err != nil {
				t.Fatalf("ExecuteTaskWithAgent: %v", err)
			}
			if exec == nil || exec.Status != models.ExecCompleted || exec.ErrorMessage != "" {
				t.Fatalf("expected malformed control to remain inert, got %#v", exec)
			}
			stored, err := execRepo.GetByID(ctx, exec.ID)
			if err != nil {
				t.Fatalf("GetByID execution: %v", err)
			}
			if stored.Status != models.ExecCompleted || stored.ErrorMessage != "" || !strings.Contains(stored.Output, tc.output) {
				t.Fatalf("expected completed execution preserving malformed control, got status=%s error=%q output=%q", stored.Status, stored.ErrorMessage, stored.Output)
			}
		})
	}
}

func TestLLMService_ExecuteTaskWithAgent_PromotesQueuedTaskThreadInputAfterCompletionWithMultipleQueued(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	agent := ensureDefaultAgent(t, llmConfigRepo)
	project := &models.Project{Name: "Worker Queue Promotion Service Multi", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Worker queue promotion multi", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "initial prompt", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	first := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first queued follow-up"}
	second := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "second queued follow-up"}
	if err := threadInputRepo.CreateQueued(ctx, first); err != nil {
		t.Fatalf("CreateQueued first: %v", err)
	}
	if err := threadInputRepo.CreateQueued(ctx, second); err != nil {
		t.Fatalf("CreateQueued second: %v", err)
	}
	promoted := make(chan string, 1)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "worker complete"
	mock.TextOnly = "worker complete"
	svc.SetLLMCaller(mock)
	svc.SetQueuedTaskThreadPromoter(func(taskID string) { promoted <- taskID })

	if _, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent); err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	select {
	case <-promoted:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued task-thread promoter")
	}
	pending, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListPendingForTask: %v", err)
	}
	if len(pending) != 2 || pending[0].ID != first.ID || pending[1].ID != second.ID {
		t.Fatalf("expected both queued inputs to remain FIFO pending for shared promoter, got %#v", pending)
	}
}

func TestLLMService_ExecuteTask_NoDefaultAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	agents, _ := llmConfigRepo.List(ctx)
	for _, a := range agents {
		llmConfigRepo.Delete(ctx, a.ID)
	}

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	task := models.Task{
		ID:        "test-task-id",
		ProjectID: "default",
		Title:     "Test",
		Prompt:    "hello",
		Status:    models.StatusPending,
	}

	_, err := svc.ExecuteTask(ctx, task)
	if err == nil {
		t.Fatal("expected error when no agent configured")
	}
	if !strings.Contains(err.Error(), "no agent configured") {
		t.Errorf("expected 'no agent configured' error, got: %v", err)
	}
}

func TestLLMService_CallLLM_UnsupportedProvider(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	agent := models.LLMConfig{
		Provider: "unsupported_provider",
		Model:    "test-model",
	}

	_, _, _, err := svc.callLLM(context.Background(), "test", nil, agent, "test-exec-id", "", "")
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error, got: %v", err)
	}
}

func TestLLMService_CallAgentDirect_TestProviderUsesMockCaller(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "mock-output"
	mock.Tokens = 17
	svc.SetLLMCaller(mock)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	output, tokens, err := svc.CallAgentDirect(context.Background(), "hello", nil, agent, "/tmp/workdir")
	if err != nil {
		t.Fatalf("CallAgentDirect error: %v", err)
	}
	if output != "mock-output" {
		t.Fatalf("expected mock output, got %q", output)
	}
	if tokens != 17 {
		t.Fatalf("expected tokens=17, got %d", tokens)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected CallModel called once, got %d", mock.CallCount())
	}
	last := mock.LastCall()
	if last.ExecID != "" {
		t.Fatalf("expected empty execID for direct calls, got %q", last.ExecID)
	}
	if last.WorkDir != "/tmp/workdir" {
		t.Fatalf("expected workdir propagated, got %q", last.WorkDir)
	}
}

func TestLLMService_CallAgentDirect_RecordsUsageForProjectWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	usageRepo := repository.NewUsageRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	ctx := context.Background()

	repoPath := t.TempDir()
	project := &models.Project{Name: "Usage Worktree Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}
	worktreePath := filepath.Join(repoPath, ".worktrees", "task_usage")
	if err := os.MkdirAll(worktreePath, 0o755); err != nil {
		t.Fatalf("MkdirAll worktree: %v", err)
	}

	svc := NewLLMService(nil, nil, nil, projectRepo, nil, nil)
	svc.SetUsageRepo(usageRepo)
	svc.providerAdapters[models.ProviderAnthropic] = providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		return llmcontracts.AgentResult{
			Output:         "summarize committed diff",
			TextOnlyOutput: "summarize committed diff",
			Usage:          llmcontracts.Usage{InputTokens: 10, OutputTokens: 13, TotalTokens: 23},
		}, nil
	})

	agent := models.LLMConfig{Name: "Usage Agent", Provider: models.ProviderAnthropic, Model: "claude-test", APIKey: "sk-test"}
	if err := llmConfigRepo.Create(ctx, &agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}
	if _, _, err := svc.CallAgentDirect(ctx, "summarize diff", nil, agent, worktreePath); err != nil {
		t.Fatalf("CallAgentDirect: %v", err)
	}
	totals, err := usageRepo.GetUsageTotals(ctx, repository.UsageFilter{ProjectID: project.ID})
	if err != nil {
		t.Fatalf("GetUsageTotals: %v", err)
	}
	if totals.CallCount != 1 || totals.TotalTokens != 23 {
		t.Fatalf("expected project usage call_count=1 total_tokens=23, got call_count=%d total_tokens=%d", totals.CallCount, totals.TotalTokens)
	}
}

func TestLLMService_CallAgentDirectStreaming_TestProviderUsesMockCaller(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "stream-output"
	mock.Tokens = 29
	svc.SetLLMCaller(mock)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	output, tokens, err := svc.CallAgentDirectStreaming(context.Background(), "hello", nil, agent, "exec-123", nil, "ctx", "/tmp/workdir")
	if err != nil {
		t.Fatalf("CallAgentDirectStreaming error: %v", err)
	}
	if output != "stream-output" {
		t.Fatalf("expected stream output, got %q", output)
	}
	if tokens != 29 {
		t.Fatalf("expected tokens=29, got %d", tokens)
	}
	if mock.CallCount() != 1 {
		t.Fatalf("expected CallModel called once, got %d", mock.CallCount())
	}
	last := mock.LastCall()
	if last.ExecID != "exec-123" {
		t.Fatalf("expected execID propagated, got %q", last.ExecID)
	}
	if last.WorkDir != "/tmp/workdir" {
		t.Fatalf("expected workdir propagated, got %q", last.WorkDir)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_PreservesFailedHistoryWithoutOutput(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	history := []models.Execution{{
		PromptSent:    "original task prompt",
		Status:        models.ExecFailed,
		ErrorMessage:  "provider timeout",
		AgentConfigID: "agent-1",
	}}
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	res, err := svc.CallAgentDirectStreamingDetailed(context.Background(), "follow up after failure", nil, agent, "exec-123", history, "ctx", "/tmp/workdir", nil)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	got := res.ChatContext.Messages
	want := []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "follow up after failure"},
		{Role: "assistant", Content: "ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected chat context messages\n got: %#v\nwant: %#v", got, want)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_PropagatesTransportScope(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	ctx := llmcontracts.WithTransportScope(context.Background(), "chat:project:project-1")
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	if _, err := svc.CallAgentDirectStreamingDetailed(ctx, "hello", nil, agent, "exec-123", nil, "ctx", "/tmp/workdir", nil); err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}

	requests := capture.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	if got := requests[0].TransportScope; got != "chat:project:project-1" {
		t.Fatalf("provider transport scope = %q", got)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_LifecycleContextContainsOnlyCurrentTurn(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	history := make([]models.Execution, 22)
	for i := range history {
		history[i] = models.Execution{PromptSent: fmt.Sprintf("old prompt %02d", i), Output: fmt.Sprintf("old output %02d", i), Status: models.ExecCompleted}
	}
	history[20].Status = models.ExecRunning
	history[20].Output = "running output should be skipped"
	history[21].Output = "visible answer\n[CREATE_TASK]\n{\"title\":\"internal\"}\n[/CREATE_TASK]"

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	res, err := svc.CallAgentDirectStreamingDetailed(context.Background(), "  current prompt  ", nil, agent, "exec-123", history, "ctx", "/tmp/workdir", nil)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	got := res.ChatContext.Messages
	want := []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "current prompt"},
		{Role: "assistant", Content: "ok"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected current-turn lifecycle context\n got: %#v\nwant: %#v", got, want)
	}
	if len(capture.lastReq.ChatHistory) != 20 || capture.lastReq.ChatHistory[0].PromptSent != "old prompt 02" {
		t.Fatalf("provider request should retain normalized history, got %#v", capture.lastReq.ChatHistory)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_LifecycleContextUsesToolBoundarySteering(t *testing.T) {
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	adapter := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		callback := llmcontracts.SteeringCallbackFromContext(req.Ctx)
		if callback == nil {
			return llmcontracts.AgentResult{}, fmt.Errorf("missing steering callback")
		}
		for i := 0; i < 2; i++ {
			if _, err := callback(req.Ctx); err != nil {
				return llmcontracts.AgentResult{}, err
			}
		}
		return llmcontracts.AgentResult{Output: "task output", TextOnlyOutput: "task output"}, nil
	})
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: adapter}
	steering := []string{"first steering", "latest steering"}
	steeringIndex := 0
	ctx := llmcontracts.WithSteeringCallback(context.Background(), func(context.Context) (string, error) {
		message := steering[steeringIndex]
		steeringIndex++
		return message, nil
	})

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	res, err := svc.CallAgentDirectStreamingDetailed(ctx, "original prompt", nil, agent, "exec-123", nil, "ctx", "/tmp/workdir", nil)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	want := []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "latest steering"},
		{Role: "assistant", Content: "task output"},
	}
	if !reflect.DeepEqual(res.ChatContext.Messages, want) {
		t.Fatalf("unexpected lifecycle context after streaming steering\n got: %#v\nwant: %#v", res.ChatContext.Messages, want)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_TestProviderPreservesTextOnly(t *testing.T) {
	svc := &LLMService{}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "stream-output"
	mock.TextOnly = "text-only"
	mock.Tokens = 29
	svc.SetLLMCaller(mock)

	agent := models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}
	res, err := svc.CallAgentDirectStreamingDetailed(context.Background(), "hello", nil, agent, "exec-123", nil, "ctx", "/tmp/workdir", nil)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	if res.Output != "stream-output" {
		t.Fatalf("expected stream output, got %q", res.Output)
	}
	if res.TextOnlyOutput != "text-only" {
		t.Fatalf("expected text-only output, got %q", res.TextOnlyOutput)
	}
	if res.Usage.TotalTokens != 29 {
		t.Fatalf("expected tokens=29, got %d", res.Usage.TotalTokens)
	}
	if got := res.ChatContext.Messages; len(got) != 2 || got[0].Role != "user" || got[0].Content != "hello" || got[1].Role != "assistant" || got[1].Content != "text-only" {
		t.Fatalf("expected normalized request chat context plus assistant text, got %#v", got)
	}
}

func TestLLMService_CallAgentDirectWithDefinition_PropagatesAgentDefinitionAndScopedTools(t *testing.T) {
	repo := t.TempDir()
	globalRoot := t.TempDir()
	capture := &captureProviderAdapter{}
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}
	svc.SetGlobalSkillRoot(globalRoot)

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	agentDef := &models.Agent{
		ID:         "skill-curator-1",
		Name:       "System: Skill Curator",
		SystemKind: models.AgentSystemKindSkillCurator,
		Tools:      []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: ".openvibely/agents", Permissions: []string{"read", "write", "delete"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
	}

	_, _, err := svc.CallAgentDirectWithDefinition(context.Background(), "review", nil, agent, repo, agentDef)
	if err != nil {
		t.Fatalf("CallAgentDirectWithDefinition error: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil || capture.lastReq.AgentDefinition.ID != agentDef.ID {
		t.Fatalf("expected agent definition propagated, got %#v", capture.lastReq.AgentDefinition)
	}
	rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx)
	if rt == nil || !rt.HasDefinition("write_file") {
		t.Fatalf("expected scoped file runtime tools, got %#v", rt)
	}
	if capture.lastReq.WorkDir == repo {
		t.Fatalf("expected workdir switched to scoped root when SkipDefaultTools=true")
	}
	payload, _ := json.Marshal(map[string]string{"file_path": "AGENTS.md", "content": "# Agents\n"})
	if _, handled, isErr, err := rt.Executor(capture.lastReq.Ctx, "write_file", payload); !handled || isErr || err != nil {
		t.Fatalf("expected configured scoped file write to succeed handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	globalPayload, _ := json.Marshal(map[string]string{"file_path": "global_agents/AGENTS.md", "content": "# Agents\n"})
	if _, handled, isErr, err := rt.Executor(capture.lastReq.Ctx, "write_file", globalPayload); !handled || isErr || err != nil {
		t.Fatalf("global_agents is only a normal relative path without an injected global scope, handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "agents", "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("global agents index must not be writable through scoped files, stat err=%v", err)
	}
}

func TestLLMService_CallAgentDirectWithDefinition_GenericHookAgentGetsScopedTools(t *testing.T) {
	repo := t.TempDir()
	customMemoryDir := filepath.Join(repo, ".openvibely", "custom-memory")
	if err := os.MkdirAll(customMemoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	capture := &captureProviderAdapter{}
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	agentDef := &models.Agent{
		ID:      "custom-after-complete-agent",
		Key:     "custom_memory_hook",
		Name:    "Custom Memory Hook",
		Enabled: true,
		Tools:   []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: ".openvibely/custom-memory", Permissions: []string{"read", "write"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
	}

	_, _, err := svc.CallAgentDirectWithDefinition(context.Background(), "update custom memory", nil, agent, repo, agentDef)
	if err != nil {
		t.Fatalf("CallAgentDirectWithDefinition error: %v", err)
	}
	rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx)
	if rt == nil || !rt.HasDefinition("read_file") || !rt.HasDefinition("write_file") {
		t.Fatalf("expected generic hook agent scoped file tools, got %#v", rt)
	}
	if capture.lastReq.WorkDir != customMemoryDir {
		t.Fatalf("expected scoped workdir %q, got %q", customMemoryDir, capture.lastReq.WorkDir)
	}
	payload, _ := json.Marshal(map[string]string{"file_path": "after_complete_probe.md", "content": "durable hook memory"})
	if _, handled, isErr, err := rt.Executor(capture.lastReq.Ctx, "write_file", payload); !handled || isErr || err != nil {
		t.Fatalf("expected generic hook scoped write to succeed handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if _, err := os.Stat(filepath.Join(customMemoryDir, "after_complete_probe.md")); err != nil {
		t.Fatalf("expected generic hook scoped file write: %v", err)
	}
}

func TestLLMService_LifecycleAfterCompleteGenericAgentGetsScopedTools(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	llmRepo := repository.NewLLMConfigRepo(db)
	if err := llmRepo.Create(ctx, &models.LLMConfig{Name: "test", Provider: models.ProviderOpenAI, Model: "gpt-test", IsDefault: true}); err != nil {
		t.Fatalf("create default model: %v", err)
	}
	agentRepo := repository.NewAgentRepo(db)
	hookAgent := &models.Agent{
		Key:     "custom_memory_hook",
		Name:    "Custom Memory Hook",
		Enabled: true,
		Tools:   []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: ".openvibely/custom-memory", Permissions: []string{"read", "write"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
	}
	if err := agentRepo.Create(ctx, hookAgent); err != nil {
		t.Fatalf("create hook agent: %v", err)
	}
	lifecycleRepo := repository.NewLifecycleRepo(db)
	if err := lifecycleRepo.CreateHook(ctx, &models.AgentLifecycleHook{AgentID: hookAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "update_custom_memory", OutputContract: models.OutputContractActivitySummary, Enabled: true}); err != nil {
		t.Fatalf("create lifecycle hook: %v", err)
	}
	repoPath := t.TempDir()
	projectRepo := repository.NewProjectRepo(db)
	project, err := projectRepo.GetByID(ctx, "default")
	if err != nil {
		t.Fatalf("get default project: %v", err)
	}
	project.RepoPath = repoPath
	if err := projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("update project: %v", err)
	}

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmRepo, nil, nil, nil, nil, nil)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}
	invoker := lifecycle.NewLLMHookInvoker(svc, agentRepo, llmRepo)
	runner := lifecycle.NewRunner(lifecycleRepo, invoker, nil)

	_, err = runner.RunSlot(ctx, models.LifecycleAfterComplete, lifecycle.HookInput{TaskID: "task-generic-hook", TaskRunID: "run-generic-hook", ProjectID: "default", WorkDir: repoPath})
	if err != nil {
		t.Fatalf("RunSlot: %v", err)
	}
	var scopedReq llmcontracts.AgentRequest
	var rt *llmcontracts.RuntimeTools
	for _, req := range capture.Requests() {
		candidate := llmcontracts.RuntimeToolsFromContext(req.Ctx)
		if candidate != nil && candidate.HasDefinition("write_file") {
			scopedReq = req
			rt = candidate
			break
		}
	}
	if rt == nil || !rt.HasDefinition("write_file") {
		t.Fatalf("expected after_complete hook agent scoped file tools, got %#v", rt)
	}
	payload, _ := json.Marshal(map[string]string{"file_path": "after_complete_probe.md", "content": "durable hook memory"})
	if _, handled, isErr, err := rt.Executor(scopedReq.Ctx, "write_file", payload); !handled || isErr || err != nil {
		t.Fatalf("expected lifecycle hook scoped write to succeed handled=%v isErr=%v err=%v", handled, isErr, err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".openvibely", "custom-memory", "after_complete_probe.md")); err != nil {
		t.Fatalf("expected after_complete hook scoped file write: %v", err)
	}
}

func TestLLMService_CallAgentDirectWithDefinition_RequiresScopedFilesToolGrant(t *testing.T) {
	repo := t.TempDir()
	capture := &captureProviderAdapter{}
	svc := NewLLMService(nil, nil, nil, nil, nil, nil)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	agentDef := &models.Agent{
		ID:    "agent-no-files",
		Name:  "Agent Without File Grant",
		Tools: []string{"skill_view"},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: ".openvibely/agents", Permissions: []string{"read", "write"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
	}

	_, _, err := svc.CallAgentDirectWithDefinition(context.Background(), "review", nil, agent, repo, agentDef)
	if err != nil {
		t.Fatalf("CallAgentDirectWithDefinition error: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil || capture.lastReq.AgentDefinition.ID != agentDef.ID {
		t.Fatalf("expected agent definition propagated, got %#v", capture.lastReq.AgentDefinition)
	}
	if rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx); rt != nil && rt.HasDefinition("write_file") {
		t.Fatalf("did not expect scoped file tools without ScopedFiles grant, got %#v", rt.Definitions)
	}
	if capture.lastReq.WorkDir != repo {
		t.Fatalf("expected workdir to stay unchanged without ScopedFiles grant, got %q", capture.lastReq.WorkDir)
	}
}

func TestLLMService_CallAgentDirectStreamingDetailed_PropagatesAgentDefinition(t *testing.T) {
	svc := &LLMService{}
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAI: capture,
	}

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}
	agentDef := &models.Agent{
		ID:           "agent-def-1",
		Name:         "playwright-reviewer",
		SystemPrompt: "Use Playwright MCP tools for screenshots.",
	}
	ctx := llmcontracts.WithChatMode(context.Background(), models.ChatModePlan)
	_, err := svc.CallAgentDirectStreamingDetailed(
		ctx,
		"check ui",
		nil,
		agent,
		"exec-123",
		nil,
		"ctx",
		"/tmp/workdir",
		agentDef,
		false,
	)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed error: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil {
		t.Fatalf("expected agent definition to be propagated")
	}
	if capture.lastReq.AgentDefinition.ID != agentDef.ID {
		t.Fatalf("expected agent definition id %q, got %q", agentDef.ID, capture.lastReq.AgentDefinition.ID)
	}
	if capture.lastReq.ChatMode != models.ChatModePlan {
		t.Fatalf("expected chat mode %q, got %q", models.ChatModePlan, capture.lastReq.ChatMode)
	}
}

func TestLLMService_CallClaudeCLI_EnvFiltering(t *testing.T) {

	os.Setenv("CLAUDECODE", "test-value")
	defer os.Unsetenv("CLAUDECODE")

	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "CLAUDECODE=") {
			filtered = append(filtered, e)
		}
	}

	for _, e := range filtered {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			t.Error("CLAUDECODE should be filtered from env")
		}
	}

	found := false
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDECODE=") {
			found = true
			break
		}
	}
	if !found {
		t.Error("CLAUDECODE should be in original env")
	}
}

func TestLLMService_ExecuteTaskWithAgent_SkipsNonPendingTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	agent := ensureDefaultAgent(t, llmConfigRepo)

	task := &models.Task{ProjectID: "default", Title: "Already Done", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "test"}
	taskRepo.Create(ctx, task)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("expected no error for skipped task, got: %v", err)
	}
	if exec != nil {
		t.Error("expected nil execution for skipped non-pending task")
	}

	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Status != models.StatusCompleted {
		t.Errorf("expected status to remain completed, got %q", updated.Status)
	}
}

func TestLLMService_ExecuteTaskWithAgent_SkipsRunningTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	task := &models.Task{ProjectID: "default", Title: "Already Running", Category: models.CategoryActive, Status: models.StatusRunning, Prompt: "test"}
	taskRepo.Create(ctx, task)

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("expected no error for skipped task, got: %v", err)
	}
	if exec != nil {
		t.Error("expected nil execution for skipped running task")
	}

	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Status != models.StatusRunning {
		t.Errorf("expected status to remain running, got %q", updated.Status)
	}
}

func TestLLMService_ExecuteTaskWithAgent_ClaimsToolBoundarySteering(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	threadInputRepo := repository.NewThreadInputRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetThreadInputRepo(threadInputRepo)
	adapter := &taskSteeringProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.LLMProvider("task-steer-test"): adapter}

	task := &models.Task{ProjectID: "default", Title: "Steer Main Run", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = models.LLMProvider("task-steer-test")

	input := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           task.ProjectID,
		TaskID:              task.ID,
		InputMode:           models.ThreadInputModeQueued,
		InputStatus:         models.ThreadInputPending,
		Content:             "review only",
		RunExecutionID:      "",
		ExpectedTurnID:      "",
		AttachmentSessionID: "",
	}

	// Create the execution first by starting the task in the fake adapter path, then
	// inject the pending steer during the provider callback by targeting that active exec.
	var createdExecID string
	adapterWithInsert := providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		createdExecID = req.ExecID
		input.RunExecutionID = createdExecID
		input.TurnID = createdExecID
		input.ExpectedTurnID = createdExecID
		if err := threadInputRepo.CreateQueued(ctx, input); err != nil {
			return llmcontracts.AgentResult{}, err
		}
		converted, err := threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, createdExecID, createdExecID)
		if err != nil {
			return llmcontracts.AgentResult{}, err
		}
		input.ID = converted.ID
		return adapter.Call(req)
	})
	svc.providerAdapters[models.LLMProvider("task-steer-test")] = adapterWithInsert
	svc.routing = nil

	exec, chatContext, err := svc.executeTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || createdExecID == "" {
		t.Fatal("expected execution")
	}
	if adapter.steering != "review only" {
		t.Fatalf("expected steering injected, got %q", adapter.steering)
	}
	wantContext := []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "review only"},
		{Role: "assistant", Content: "task output"},
	}
	if !reflect.DeepEqual(chatContext.Messages, wantContext) {
		t.Fatalf("unexpected lifecycle context after task steering\n got: %#v\nwant: %#v", chatContext.Messages, wantContext)
	}
	updated, err := threadInputRepo.GetByID(ctx, input.ID)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	if updated == nil || updated.InputStatus != models.ThreadInputApplied {
		t.Fatalf("expected steering applied, got %#v", updated)
	}
}

func TestLLMService_ExecuteTaskWithAgent_DoesNotReplayProviderCall(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	threadInputRepo := repository.NewThreadInputRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetThreadInputRepo(threadInputRepo)
	testProvider := models.LLMProvider("task-steer-retry-test")
	task := &models.Task{ProjectID: "default", Title: "Steer Main Run Retry", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	var inputID string
	var attempts int
	var steers []string
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		testProvider: retryingProviderAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			attempts++
			if attempts == 1 {
				input := &models.ThreadInput{
					Scope:          models.ThreadInputScopeTask,
					ProjectID:      task.ProjectID,
					TaskID:         task.ID,
					InputMode:      models.ThreadInputModeQueued,
					InputStatus:    models.ThreadInputPending,
					Content:        "retry steer",
					RunExecutionID: req.ExecID,
				}
				if err := threadInputRepo.CreateQueued(ctx, input); err != nil {
					return llmcontracts.AgentResult{}, err
				}
				converted, err := threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, req.ExecID, req.ExecID)
				if err != nil {
					return llmcontracts.AgentResult{}, err
				}
				inputID = converted.ID
			}
			callback := llmcontracts.SteeringCallbackFromContext(req.Ctx)
			if callback == nil {
				return llmcontracts.AgentResult{}, fmt.Errorf("missing steering callback")
			}
			steering, err := callback(req.Ctx)
			if err != nil {
				return llmcontracts.AgentResult{}, err
			}
			steers = append(steers, steering)
			if attempts == 1 {
				return llmcontracts.AgentResult{}, fmt.Errorf("503 temporarily unavailable")
			}
			return llmcontracts.AgentResult{Output: "ok", TextOnlyOutput: "ok", Usage: llmcontracts.Usage{TotalTokens: 1}}, nil
		}),
	}
	svc.routing = nil

	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = testProvider

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected provider error")
	}
	if attempts != 1 {
		t.Fatalf("expected provider call not to be replayed, attempts=%d", attempts)
	}
	if len(steers) != 1 || steers[0] != "retry steer" {
		t.Fatalf("expected steering injected into the single attempt, got %#v", steers)
	}
	updated, err := threadInputRepo.GetByID(ctx, inputID)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	if updated == nil || updated.InputMode != models.ThreadInputModeQueued || updated.InputStatus != models.ThreadInputPending {
		t.Fatalf("expected steering requeued after failed call, got %#v", updated)
	}
}

func TestLLMService_ExecuteTaskWithAgent_RequeuesToolBoundarySteeringOnFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	threadInputRepo := repository.NewThreadInputRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetThreadInputRepo(threadInputRepo)
	testProvider := models.LLMProvider("task-steer-fail-test")
	var inputID string
	task := &models.Task{ProjectID: "default", Title: "Steer Main Run Failure", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		testProvider: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			input := &models.ThreadInput{
				Scope:          models.ThreadInputScopeTask,
				ProjectID:      task.ProjectID,
				TaskID:         task.ID,
				InputMode:      models.ThreadInputModeQueued,
				InputStatus:    models.ThreadInputPending,
				Content:        "stop editing",
				RunExecutionID: req.ExecID,
			}
			if err := threadInputRepo.CreateQueued(ctx, input); err != nil {
				return llmcontracts.AgentResult{}, err
			}
			converted, err := threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, req.ExecID, req.ExecID)
			if err != nil {
				return llmcontracts.AgentResult{}, err
			}
			inputID = converted.ID
			callback := llmcontracts.SteeringCallbackFromContext(req.Ctx)
			if callback == nil {
				return llmcontracts.AgentResult{}, fmt.Errorf("missing steering callback")
			}
			if steering, err := callback(req.Ctx); err != nil {
				return llmcontracts.AgentResult{}, err
			} else if steering != "stop editing" {
				return llmcontracts.AgentResult{}, fmt.Errorf("unexpected steering %q", steering)
			}
			return llmcontracts.AgentResult{Output: "partial", TextOnlyOutput: "partial", Usage: llmcontracts.Usage{TotalTokens: 1}}, fmt.Errorf("provider failed")
		}),
	}
	svc.routing = nil

	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = testProvider

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected provider failure")
	}
	updated, err := threadInputRepo.GetByID(ctx, inputID)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	if updated == nil || updated.InputMode != models.ThreadInputModeQueued || updated.InputStatus != models.ThreadInputPending {
		t.Fatalf("expected steering requeued pending, got %#v", updated)
	}
}

func TestLLMService_ExecuteTaskWithAgent_CommitFailureWithCancelledContextMarksExecutionFailed(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	threadInputRepo := repository.NewThreadInputRepo(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetThreadInputRepo(threadInputRepo)
	testProvider := models.LLMProvider("task-steer-commit-cancel-test")
	var inputID string
	task := &models.Task{ProjectID: "default", Title: "Steer Commit Cancel", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		testProvider: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			input := &models.ThreadInput{
				Scope:          models.ThreadInputScopeTask,
				ProjectID:      task.ProjectID,
				TaskID:         task.ID,
				InputMode:      models.ThreadInputModeQueued,
				InputStatus:    models.ThreadInputPending,
				Content:        "commit then cancel",
				RunExecutionID: req.ExecID,
			}
			if err := threadInputRepo.CreateQueued(ctx, input); err != nil {
				return llmcontracts.AgentResult{}, err
			}
			converted, err := threadInputRepo.ConvertQueuedToSteering(ctx, input.ID, req.ExecID, req.ExecID)
			if err != nil {
				return llmcontracts.AgentResult{}, err
			}
			inputID = converted.ID
			callback := llmcontracts.SteeringCallbackFromContext(req.Ctx)
			if callback == nil {
				return llmcontracts.AgentResult{}, fmt.Errorf("missing steering callback")
			}
			if steering, err := callback(req.Ctx); err != nil {
				return llmcontracts.AgentResult{}, err
			} else if steering != "commit then cancel" {
				return llmcontracts.AgentResult{}, fmt.Errorf("unexpected steering %q", steering)
			}
			cancel()
			return llmcontracts.AgentResult{Output: "provider succeeded", TextOnlyOutput: "provider succeeded", Usage: llmcontracts.Usage{TotalTokens: 1}}, nil
		}),
	}
	svc.routing = nil

	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = testProvider

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected steering commit failure")
	}
	if exec == nil || exec.Status != models.ExecFailed {
		t.Fatalf("expected returned execution failed, got %#v", exec)
	}
	storedExec, err := execRepo.GetByID(context.Background(), exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if storedExec.Status != models.ExecFailed || storedExec.CompletedAt == nil {
		t.Fatalf("expected stored execution failed and completed, got %#v", storedExec)
	}
	updatedTask, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusFailed {
		t.Fatalf("expected task failed, got %#v", updatedTask)
	}
	updatedInput, err := threadInputRepo.GetByID(context.Background(), inputID)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	if updatedInput == nil || updatedInput.InputMode != models.ThreadInputModeQueued || updatedInput.InputStatus != models.ThreadInputPending {
		t.Fatalf("expected steering requeued after commit failure, got %#v", updatedInput)
	}
}

func TestLLMService_ExecuteTaskWithAgent_RequeuesRestoredSteeringOnCancellation(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	threadInputRepo := repository.NewThreadInputRepo(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetThreadInputRepo(threadInputRepo)
	testProvider := models.LLMProvider("task-steer-cancel-after-reset-test")
	var inputID string
	task := &models.Task{ProjectID: "default", Title: "Steer Main Run Cancel After Reset", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		testProvider: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			input := &models.ThreadInput{
				Scope:          models.ThreadInputScopeTask,
				ProjectID:      task.ProjectID,
				TaskID:         task.ID,
				InputMode:      models.ThreadInputModeQueued,
				InputStatus:    models.ThreadInputPending,
				Content:        "retry then cancel",
				RunExecutionID: req.ExecID,
			}
			if err := threadInputRepo.CreateQueued(context.Background(), input); err != nil {
				return llmcontracts.AgentResult{}, err
			}
			converted, err := threadInputRepo.ConvertQueuedToSteering(context.Background(), input.ID, req.ExecID, req.ExecID)
			if err != nil {
				return llmcontracts.AgentResult{}, err
			}
			inputID = converted.ID
			callback := llmcontracts.SteeringCallbackFromContext(req.Ctx)
			if callback == nil {
				return llmcontracts.AgentResult{}, fmt.Errorf("missing steering callback")
			}
			if steering, err := callback(req.Ctx); err != nil {
				return llmcontracts.AgentResult{}, err
			} else if steering != "retry then cancel" {
				return llmcontracts.AgentResult{}, fmt.Errorf("unexpected steering %q", steering)
			}
			reset := llmcontracts.SteeringRetryResetCallbackFromContext(req.Ctx)
			if reset == nil {
				return llmcontracts.AgentResult{}, fmt.Errorf("missing steering retry reset callback")
			}
			if err := reset(context.Background()); err != nil {
				return llmcontracts.AgentResult{}, err
			}
			cancel()
			return llmcontracts.AgentResult{}, context.Canceled
		}),
	}
	svc.routing = nil

	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = testProvider

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected cancellation")
	}
	updated, err := threadInputRepo.GetByID(context.Background(), inputID)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	if updated == nil || updated.InputMode != models.ThreadInputModeQueued || updated.InputStatus != models.ThreadInputPending || updated.ExpectedTurnID != "" {
		t.Fatalf("expected restored steering requeued after cancellation, got %#v", updated)
	}
}

func TestLLMService_ExecuteTaskWithAgent_RecordsExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	mock := &testutil.MockLLMCaller{Err: fmt.Errorf("mock error: simulated failure")}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	task := &models.Task{ProjectID: "default", Title: "Record Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected error from mock")
	}

	updated, _ := taskRepo.GetByID(ctx, task.ID)
	if updated.Status != models.StatusFailed {
		t.Errorf("expected task status=failed, got %q", updated.Status)
	}

	if exec == nil {
		t.Fatal("expected execution record even on failure")
	}
	if exec.Status != models.ExecFailed {
		t.Errorf("expected exec status=failed, got %q", exec.Status)
	}
}

func TestLLMService_ExecuteTaskWithAgent_FinalizesAfterProviderSuccessWhenContextCancelled(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	testProvider := models.LLMProvider("task-finalize-cancel-after-success-test")
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		testProvider: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			cancel()
			return llmcontracts.AgentResult{Output: "provider succeeded", TextOnlyOutput: "provider succeeded", Usage: llmcontracts.Usage{TotalTokens: 1}}, nil
		}),
	}
	svc.routing = nil

	task := &models.Task{ProjectID: "default", Title: "Finalize Cancel", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = testProvider

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected returned execution completed, got %#v", exec)
	}
	storedExec, err := execRepo.GetByID(context.Background(), exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if storedExec.Status != models.ExecCompleted || storedExec.CompletedAt == nil {
		t.Fatalf("expected stored execution completed despite cancelled request context, got %#v", storedExec)
	}
	updatedTask, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed despite cancelled request context, got %#v", updatedTask)
	}
}

func TestLLMService_ExecuteTaskWithAgent_FinalizesReportedFailureWhenContextCancelled(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	testProvider := models.LLMProvider("task-finalize-cancel-after-status-failed-test")
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		testProvider: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
			cancel()
			return llmcontracts.AgentResult{Output: "could not finish\n[STATUS: FAILED | blocked]", TextOnlyOutput: "could not finish\n[STATUS: FAILED | blocked]", Usage: llmcontracts.Usage{TotalTokens: 1}}, nil
		}),
	}
	svc.routing = nil

	task := &models.Task{ProjectID: "default", Title: "Finalize Reported Failure", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = testProvider

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("execute task: %v", err)
	}
	if exec == nil || exec.Status != models.ExecFailed {
		t.Fatalf("expected returned execution failed, got %#v", exec)
	}
	storedExec, err := execRepo.GetByID(context.Background(), exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if storedExec.Status != models.ExecFailed || storedExec.CompletedAt == nil || storedExec.ErrorMessage != "blocked" {
		t.Fatalf("expected stored execution failed despite cancelled request context, got %#v", storedExec)
	}
	updatedTask, err := taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusFailed {
		t.Fatalf("expected task failed despite cancelled request context, got %#v", updatedTask)
	}
}

func TestLLMService_ExecuteTaskWithAgent_AllowsExplicitNonSelectableAgentForNormalTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetAgentRepo(agentRepo)
	mock := &testutil.MockLLMCaller{Response: "skill curator ran", TextOnly: "skill curator ran", Tokens: 1}
	svc.SetLLMCaller(mock)

	agentDef, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil {
		t.Fatalf("get skill curator definition: %v", err)
	}
	if agentDef == nil {
		t.Fatal("expected seeded skill curator definition")
	}
	agent := ensureDefaultAgent(t, llmConfigRepo)
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Normal user task",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "run skill curator explicitly",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("explicit non-selectable agent should run: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution")
	}
	if mock.CallCount() == 0 {
		t.Fatal("expected model to be called")
	}
}

func TestLLMService_ExecuteTaskWithAgent_IncludesSendMessageRuntimeTool(t *testing.T) {
	cases := []struct {
		name     string
		provider models.LLMProvider
		adapter  models.LLMProvider
	}{
		{name: "openai", provider: models.ProviderOpenAI, adapter: models.ProviderOpenAI},
		{name: "openai-compatible", provider: models.ProviderOpenAICompatible, adapter: models.ProviderOpenAICompatible},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			llmConfigRepo := repository.NewLLMConfigRepo(db)
			execRepo := repository.NewExecutionRepo(db)
			taskRepo := repository.NewTaskRepo(db, nil)
			projectRepo := repository.NewProjectRepo(db)
			ctx := context.Background()

			svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
			svc.SetChannelMessageRouter(NewChannelMessageRouter(repository.NewChannelTargetRepo(db), repository.NewSettingsRepo(db)))
			capture := &captureProviderAdapter{}
			svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{tc.adapter: capture}

			agent := &models.LLMConfig{Name: "Runtime Provider", Provider: tc.provider, AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key", BaseURL: "https://example.invalid/v1", Model: "gpt-test", IsDefault: true}
			if err := llmConfigRepo.Create(ctx, agent); err != nil {
				t.Fatalf("create model agent: %v", err)
			}
			task := &models.Task{ProjectID: "default", Title: "Write and email a story", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "Write a story then email me when done"}
			if err := taskRepo.Create(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
			if err != nil {
				t.Fatalf("ExecuteTaskWithAgent: %v", err)
			}
			if exec == nil {
				t.Fatal("expected execution")
			}
			rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx)
			if rt == nil || !rt.HasDefinition("send_message") {
				t.Fatalf("initial task provider request missing send_message runtime tool: %#v", rt)
			}
			out, handled, isErr, err := rt.Executor(context.Background(), "send_message", json.RawMessage(`{"action":"list"}`))
			if !handled || err != nil || isErr || !strings.Contains(out, `"targets"`) {
				t.Fatalf("send_message list should execute through task runtime handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
			}
		})
	}
}

func TestLLMService_ExecuteTaskWithAgent_MixtureSupportedAggregatorReceivesGrantedAndDefaultTools(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.SetTaskService(NewTaskService(taskRepo, attachmentRepo, nil))
	svc.SetChannelMessageRouter(NewChannelMessageRouter(repository.NewChannelTargetRepo(db), repository.NewSettingsRepo(db)))
	recorder := &recordingMixtureAdapter{responses: map[string]llmcontracts.AgentResult{}, errors: map[string]error{}}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderMixture: &mixtureProviderAdapter{svc: svc},
		models.ProviderTest:    recorder,
		models.ProviderOpenAI:  recorder,
	}

	ref := createMixtureTestConfig(t, llmConfigRepo, "Initial Task Reference", models.ProviderTest, "ref")
	aggregator := createMixtureTestConfig(t, llmConfigRepo, "Initial Task Aggregator", models.ProviderOpenAI, "gpt-test")
	recorder.responses[ref.ID] = llmcontracts.AgentResult{Output: "private advice", TextOnlyOutput: "private advice"}
	aggregatorOutput := "completed\n[CREATE_TASK]{\"title\":\"must not be created\",\"prompt\":\"runtime tools disable markers\"}[/CREATE_TASK]"
	recorder.responses[aggregator.ID] = llmcontracts.AgentResult{Output: aggregatorOutput, TextOnlyOutput: aggregatorOutput}
	mixture := &models.LLMConfig{
		Name: "Initial Task Mixture", Provider: models.ProviderMixture, Model: "mixture",
		MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"` + ref.ID + `"}],"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
	}
	if err := llmConfigRepo.Create(ctx, mixture); err != nil {
		t.Fatalf("create mixture: %v", err)
	}
	assignedAgent := &models.Agent{ID: "mixture-tool-agent", Key: "mixture_tool_agent", Name: "Mixture Tool Agent", Enabled: true, Tools: []string{"agent_allowed"}}
	if err := agentRepo.Create(ctx, assignedAgent); err != nil {
		t.Fatalf("create assigned agent: %v", err)
	}
	task := &models.Task{ProjectID: "default", Title: "Initial mixture task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "Create a follow-up task", AgentDefinitionID: &assignedAgent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	grantedTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "agent_allowed", Access: llmcontracts.RuntimeToolAccessRead}}}
	recorder.onCall = func(req llmcontracts.AgentRequest) {
		if req.Agent.ID != aggregator.ID {
			return
		}
		rt := llmcontracts.RuntimeToolsFromContext(req.Ctx)
		if rt == nil || rt.Executor == nil {
			t.Fatalf("aggregator runtime is not executable: %#v", rt)
		}
		_, handled, isErr, execErr := rt.Executor(req.Ctx, "create_task", json.RawMessage(`{"title":"Initial mixture runtime child","prompt":"Created through the aggregator runtime.","category":"backlog"}`))
		if execErr != nil || !handled || isErr {
			t.Fatalf("create_task execution failed handled=%v isErr=%v err=%v", handled, isErr, execErr)
		}
	}
	callCtx := llmcontracts.WithRuntimeTools(ctx, grantedTools)

	exec, err := svc.ExecuteTaskWithAgent(callCtx, *task, *mixture)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected completed execution, got %#v", exec)
	}

	requests := recorder.Requests()
	if len(requests) != 2 {
		t.Fatalf("expected one reference and one aggregator request, got %d", len(requests))
	}
	for _, req := range requests {
		rt := llmcontracts.RuntimeToolsFromContext(req.Ctx)
		if req.Agent.ID == ref.ID {
			if rt != nil {
				t.Fatalf("reference inherited runtime tools: %#v", rt)
			}
			continue
		}
		if req.Agent.ID != aggregator.ID {
			t.Fatalf("unexpected mixture request agent %q", req.Agent.ID)
		}
		if req.AgentDefinition == nil || req.AgentDefinition.ID != assignedAgent.ID {
			t.Fatalf("aggregator did not preserve assigned agent: %#v", req.AgentDefinition)
		}
		if rt == nil || !rt.HasDefinition("agent_allowed") || !rt.HasDefinition("create_task") || !rt.HasDefinition("send_message") {
			t.Fatalf("aggregator missing granted/default runtime tools: %#v", rt)
		}
		if rt.HasDefinition("agent_disallowed") {
			t.Fatalf("aggregator received an ungranted agent tool: %#v", rt)
		}
	}
	tasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected original task plus runtime-created child, got %d tasks", len(tasks))
	}
	foundRuntimeChild := false
	for _, created := range tasks {
		if created.Title == "must not be created" {
			t.Fatalf("runtime-enabled mixture processed marker output: %#v", tasks)
		}
		if created.Title == "Initial mixture runtime child" {
			foundRuntimeChild = true
		}
	}
	if !foundRuntimeChild {
		t.Fatalf("aggregator runtime did not persist created task: %#v", tasks)
	}
}

func TestLLMService_ExecuteTaskWithAgent_MixtureOllamaAggregatorMasksToolsAndLeavesMarkersInert(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	providerRequests := make(chan map[string]any, 1)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		providerRequests <- body
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":"[CREATE_TASK]\n{\"title\":\"Initial Ollama mixture child\",\"prompt\":\"This marker-looking text must remain inert.\"}\n[/CREATE_TASK]"},"done":false}`)
		_, _ = fmt.Fprintln(w, `{"model":"test-model","message":{"role":"assistant","content":""},"done":true,"eval_count":12}`)
	}))
	defer providerServer.Close()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	svc.SetTaskService(NewTaskService(taskRepo, attachmentRepo, nil))
	recorder := &recordingMixtureAdapter{responses: map[string]llmcontracts.AgentResult{}, errors: map[string]error{}}
	ollamaAdapter := svc.providerAdapters[models.ProviderOllama]
	svc.providerAdapters[models.ProviderTest] = recorder
	svc.providerAdapters[models.ProviderOllama] = providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		if rt := llmcontracts.RuntimeToolsFromContext(req.Ctx); rt != nil {
			t.Fatalf("runtime-tool-incapable aggregator inherited tools: %#v", rt)
		}
		return ollamaAdapter.Call(req)
	})

	ref := createMixtureTestConfig(t, llmConfigRepo, "Initial Ollama Reference", models.ProviderTest, "ref")
	recorder.responses[ref.ID] = llmcontracts.AgentResult{Output: "private advice", TextOnlyOutput: "private advice"}
	aggregator := &models.LLMConfig{Name: "Initial Ollama Aggregator", Provider: models.ProviderOllama, Model: "test-model", OllamaBaseURL: providerServer.URL}
	if err := llmConfigRepo.Create(ctx, aggregator); err != nil {
		t.Fatalf("create Ollama aggregator: %v", err)
	}
	mixture := &models.LLMConfig{
		Name: "Initial Ollama Mixture", Provider: models.ProviderMixture, Model: "mixture",
		MixtureConfigJSON: `{"enabled":true,"reference_models":[{"agent_config_id":"` + ref.ID + `"}],"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
	}
	if err := llmConfigRepo.Create(ctx, mixture); err != nil {
		t.Fatalf("create mixture: %v", err)
	}
	task := &models.Task{ProjectID: "default", Title: "Initial Ollama mixture task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "Create a child task"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	grantedTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "agent_allowed", Access: llmcontracts.RuntimeToolAccessRead}}}

	exec, err := svc.ExecuteTaskWithAgent(llmcontracts.WithRuntimeTools(ctx, grantedTools), *task, *mixture)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected completed execution, got %#v", exec)
	}
	requests := recorder.Requests()
	if len(requests) != 1 || requests[0].Agent.ID != ref.ID {
		t.Fatalf("expected one reference request, got %#v", requests)
	}
	if rt := llmcontracts.RuntimeToolsFromContext(requests[0].Ctx); rt != nil {
		t.Fatalf("reference inherited runtime tools: %#v", rt)
	}
	select {
	case providerRequest := <-providerRequests:
		if _, exists := providerRequest["tools"]; exists {
			t.Fatalf("Ollama payload included runtime tools: %#v", providerRequest["tools"])
		}
		messages, _ := providerRequest["messages"].([]any)
		foundLimitation := false
		for _, raw := range messages {
			message, _ := raw.(map[string]any)
			content, _ := message["content"].(string)
			if strings.Contains(content, llmprompt.ChatActionUnavailableInstructions) {
				foundLimitation = true
			}
			if strings.Contains(content, "[CREATE_TASK]") {
				t.Fatalf("Ollama initial-task prompt advertised a legacy marker: %q", content)
			}
		}
		if !foundLimitation {
			t.Fatalf("Ollama initial-task prompt omitted runtime-action limitation: %#v", messages)
		}
	default:
		t.Fatal("expected concrete Ollama aggregator request")
	}
	tasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("marker-looking Ollama output created a child task: %#v", tasks)
	}
	if strings.Contains(tasks[0].Title, "Initial Ollama mixture child") {
		t.Fatalf("unexpected child task persisted from marker-looking output: %#v", tasks)
	}
}

func TestLLMService_ExecuteTaskWithAgent_RuntimeToolsSkipTaskCreationMarkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	svc.SetTaskService(NewTaskService(taskRepo, attachmentRepo, nil))
	svc.SetChannelMessageRouter(NewChannelMessageRouter(repository.NewChannelTargetRepo(db), repository.NewSettingsRepo(db)))
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: providerAdapterFunc(func(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
		if rt := llmcontracts.RuntimeToolsFromContext(req.Ctx); rt == nil || !rt.HasDefinition("send_message") {
			t.Fatalf("expected provider request to include send_message runtime tools, got %#v", rt)
		}
		out := "Story complete.\n[CREATE_TASK]{\"title\":\"Unexpected child\",\"prompt\":\"should not be created\"}[/CREATE_TASK]"
		return llmcontracts.AgentResult{Output: out, TextOnlyOutput: out, Usage: llmcontracts.Usage{TotalTokens: 12}}, nil
	})}

	agent := &models.LLMConfig{Name: "OpenAI", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key", Model: "gpt-test", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create model agent: %v", err)
	}
	task := &models.Task{ProjectID: "default", Title: "Write and email a story", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "Write a story then email me when done"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected completed execution, got %#v", exec)
	}
	tasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected runtime-tool task output not to create marker child tasks, got %d tasks: %#v", len(tasks), tasks)
	}
}

func TestLLMService_ExecuteTaskWithAgent_RuntimeIncapableProviderLeavesTaskCreationMarkersInert(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), attachmentRepo)
	svc.SetTaskService(NewTaskService(taskRepo, attachmentRepo, nil))
	svc.SetChannelMessageRouter(NewChannelMessageRouter(repository.NewChannelTargetRepo(db), repository.NewSettingsRepo(db)))

	agent := ensureDefaultAgent(t, llmConfigRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "Story complete.\n[CREATE_TASK]{\"title\":\"Expected child\",\"prompt\":\"should be created\"}[/CREATE_TASK]"
	mock.TextOnly = mock.Response
	mock.Tokens = 12
	svc.SetLLMCaller(mock)

	task := &models.Task{ProjectID: "default", Title: "Marker fallback task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "Create a child task"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil || exec.Status != models.ExecCompleted {
		t.Fatalf("expected completed execution, got %#v", exec)
	}
	tasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected runtime-incapable provider marker text to remain inert, got %d tasks: %#v", len(tasks), tasks)
	}
}

func TestLLMService_ExecuteTaskWithAgent_CustomAgentSkillLibraryToolsUseRoutedSelection(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	root := t.TempDir()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetAgentRepo(agentRepo)
	svc.SetGlobalSkillRoot(root)
	svc.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	agentDef := &models.Agent{ID: "custom-skill-librarian-id", Key: "custom_librarian", Name: "Custom Librarian", Enabled: true, Tools: []string{"skill_view", "skills_list", "agent_list", "agent_view", "skill_manage"}}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create custom agent definition: %v", err)
	}
	agent := &models.LLMConfig{Name: "OpenAI", Provider: models.ProviderOpenAI, Model: "gpt-test", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create model agent: %v", err)
	}
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Custom library maintenance",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "curate skills",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	writeLifecycleTestSkill(t, root, "custom_librarian", "curate", "custom selected skill body")
	writeLifecycleStandaloneSkill(t, root, "standalone_skill", "standalone body")
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		available, _ := in.Extras["available_skills"].(string)
		if !strings.Contains(available, "custom_librarian/curate") || strings.Contains(available, "standalone_skill") {
			return nil, fmt.Errorf("assigned-agent router saw wrong skill index: %s", available)
		}
		return routePayload([]string{"curate"}, 0.9), nil
	}), nil)
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	turn := worker.PrepareLifecycleTurn(ctx, *task)
	exec, err := svc.ExecuteTaskWithAgent(turn.Ctx, turn.Task, *agent)
	if err != nil {
		t.Fatalf("custom skill-library agent task should run: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution")
	}
	rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx)
	if rt == nil || !rt.HasDefinition("skill_manage") || !rt.HasDefinition("skills_list") || !rt.HasDefinition("agent_list") || !rt.HasDefinition("agent_view") || rt.HasDefinition("agent_manage") {
		t.Fatalf("custom agent should get explicitly declared scoped skill tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "skills_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "## standalone_skill") || strings.Contains(out, "custom_librarian/curate") {
		t.Fatalf("skills_list should expose standalone skills only handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"standalone_skill"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "standalone body") {
		t.Fatalf("final runtime should view listed standalone skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"curate"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "custom selected skill body") {
		t.Fatalf("final runtime should view router-selected agent-owned skill handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "agent_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "custom_librarian") || strings.Contains(out, "skill_curator") || strings.Contains(out, "memory_curator") {
		t.Fatalf("agent_list should expose user-managed agents only handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_manage", json.RawMessage(`{"action":"write_file","handle":"custom_librarian/curate","scope":"global","support":{"kind":"references","path":"forbidden.md","content":"blocked"}}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "invalid standalone skill handle") {
		t.Fatalf("skill_manage must not mutate agent-owned custom skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestLLMService_ExecuteTaskWithAgent_AllowsScheduledSkillCuratorTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	ctx := context.Background()

	root := t.TempDir()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, repository.NewScheduleRepo(db), repository.NewAttachmentRepo(db))
	svc.SetAgentRepo(agentRepo)
	svc.SetGlobalSkillRoot(root)
	svc.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}

	agentDef, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil {
		t.Fatalf("get skill curator definition: %v", err)
	}
	if agentDef == nil {
		t.Fatal("expected seeded skill curator definition")
	}
	agent := &models.LLMConfig{Name: "OpenAI", Provider: models.ProviderOpenAI, Model: "gpt-test", IsDefault: true}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create model agent: %v", err)
	}
	task := &models.Task{
		ProjectID:         "default",
		Title:             agentLibraryMaintenanceTaskTitle,
		Category:          models.CategoryScheduled,
		Status:            models.StatusPending,
		Prompt:            agentLibraryMaintenanceTaskPrompt,
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	writeLifecycleTestSkill(t, root, "skill_curator", "maintain_skill_library", "maintenance skill body")
	writeLifecycleStandaloneSkill(t, root, "other_skill", "other skill body")
	writeLifecycleStandaloneSkill(t, root, "maintain_skill_library", "standalone maintenance body")
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		return routePayload([]string{"maintain_skill_library"}, 0.9), nil
	}), nil)
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	turn := worker.PrepareLifecycleTurn(ctx, *task)
	exec, err := svc.ExecuteTaskWithAgent(turn.Ctx, turn.Task, *agent)
	if err != nil {
		t.Fatalf("scheduled skill curator task should run: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution")
	}
	rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx)
	if rt == nil || !rt.HasDefinition("skill_manage") || !rt.HasDefinition("agent_skill_manage") || !rt.HasDefinition("skills_list") || !rt.HasDefinition("agent_list") || !rt.HasDefinition("agent_view") || rt.HasDefinition("agent_manage") {
		t.Fatalf("scheduled Skill Curator task should get tools from assigned agent declaration, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"other_skill"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "other skill body") {
		t.Fatalf("final runtime should view listed standalone skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skills_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "standalone:maintain_skill_library") {
		t.Fatalf("final runtime skills_list should return qualified view handles handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"maintain_skill_library"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "ambiguous") {
		t.Fatalf("final runtime should reject colliding bare maintainer skill handle handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"standalone:maintain_skill_library"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "standalone maintenance body") {
		t.Fatalf("final runtime should view qualified standalone maintainer skill handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"agent:skill_curator/maintain_skill_library"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "maintenance skill body") {
		t.Fatalf("final runtime should view qualified selected maintainer skill handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	reviewerAgent := &models.Agent{ID: "reviewer-agent-id", Key: "reviewer", Name: "Reviewer", Description: "Reviews code", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true, GeneratedStatus: models.AgentStatusGenerated}
	if err := agentRepo.Create(ctx, reviewerAgent); err != nil {
		t.Fatalf("create reviewer agent: %v", err)
	}
	disabledAgent := &models.Agent{ID: "disabled-agent-id", Key: "disabled_agent", Name: "Disabled", Enabled: false, GeneratedStatus: models.AgentStatusGenerated}
	if err := agentRepo.Create(ctx, disabledAgent); err != nil {
		t.Fatalf("create disabled agent: %v", err)
	}
	archivedAgent := &models.Agent{ID: "archived-agent-id", Key: "archived_agent", Name: "Archived", Enabled: true, GeneratedStatus: models.AgentStatusArchived}
	if err := agentRepo.Create(ctx, archivedAgent); err != nil {
		t.Fatalf("create archived agent: %v", err)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "agent_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "reviewer") || strings.Contains(out, "skill_curator") || strings.Contains(out, "memory_curator") || strings.Contains(out, "disabled_agent") || strings.Contains(out, "archived_agent") {
		t.Fatalf("final runtime agent_list should expose active non-system agents only handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_manage", json.RawMessage(`{"action":"write_file","handle":"skill_curator/maintain_skill_library","scope":"global","support":{"kind":"references","path":"forbidden.md","content":"blocked"}}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "invalid standalone skill handle") {
		t.Fatalf("final runtime skill_manage must not mutate system agent skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "agent_skill_manage", json.RawMessage(`{"action":"create","agent":"reviewer","scope":"global","declaration":"---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: review_migrations\n---\n# Review migrations\n"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("final runtime agent_skill_manage should mutate non-system agent skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "reviewer", "skills", "review_migrations", "SKILL.md")); err != nil {
		t.Fatalf("agent_skill_manage did not write non-system agent skill: %v", err)
	}
	for _, tc := range []struct {
		agent string
		skill string
		want  string
	}{
		{agent: "skill_curator", skill: "maintain_skill_library", want: "protected"},
		{agent: "disabled_agent", skill: "disabled_skill", want: "disabled"},
		{agent: "archived_agent", skill: "archived_skill", want: "archived"},
	} {
		params := fmt.Sprintf(`{"action":"write_file","agent":%q,"scope":"global","handle":%q,"support":{"kind":"references","path":"forbidden.md","content":"blocked"}}`, tc.agent, tc.skill)
		out, handled, isErr, err = rt.Executor(context.Background(), "agent_skill_manage", json.RawMessage(params))
		if !handled || err != nil || !isErr || !strings.Contains(out, tc.want) {
			t.Fatalf("final runtime agent_skill_manage must block %s agent skills handled=%v isErr=%v err=%v out=%q", tc.want, handled, isErr, err, out)
		}
	}
}

func TestLLMService_ExecuteTask_UsesAssignedAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	customAgent := &models.LLMConfig{
		Name:        "Custom Agent",
		Provider:    models.ProviderAnthropic,
		Model:       "custom-model",
		MaxTokens:   1000,
		Temperature: 0.5,
		IsDefault:   false,
	}
	if err := llmConfigRepo.Create(ctx, customAgent); err != nil {
		t.Fatalf("failed to create custom agent: %v", err)
	}

	task := &models.Task{
		ProjectID: "default",
		Title:     "Custom Agent Task",
		Category:  models.CategoryActive,
		Status:    models.StatusCompleted,
		Prompt:    "test",
		AgentID:   &customAgent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec, err := svc.ExecuteTask(ctx, *task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec != nil {
		t.Errorf("expected nil execution for non-pending task, got %+v", exec)
	}

	task2 := &models.Task{
		ProjectID: "default",
		Title:     "Custom Agent Task 2",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
		AgentID:   &customAgent.ID,
	}
	if err := taskRepo.Create(ctx, task2); err != nil {
		t.Fatalf("failed to create task2: %v", err)
	}

	mock := &testutil.MockLLMCaller{Response: "ok", TextOnly: "ok", Tokens: 10}
	svc.SetLLMCaller(mock)

	fetchedAgent, _ := llmConfigRepo.GetByID(ctx, customAgent.ID)

	fetchedAgent.Provider = models.ProviderTest

	exec2, err := svc.ExecuteTaskWithAgent(ctx, *task2, *fetchedAgent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec2 == nil {
		t.Fatal("expected execution record")
	}
	if exec2.AgentConfigID != customAgent.ID {
		t.Errorf("expected execution to use custom agent %s, got %s", customAgent.ID, exec2.AgentConfigID)
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if mock.LastCall().Agent.ID != customAgent.ID {
		t.Errorf("expected callLLM to receive custom agent %s, got %s", customAgent.ID, mock.LastCall().Agent.ID)
	}
}

func TestLLMService_ExecuteTask_MemoryConsolidationUsesNormalExecutionPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	agentRepo := repository.NewAgentRepo(db)
	svc.SetAgentRepo(agentRepo)
	mock := &testutil.MockLLMCaller{Response: "memory task response", TextOnly: "memory task response", Tokens: 10}
	svc.SetLLMCaller(mock)

	repoPath := t.TempDir()
	agentDef := &models.Agent{
		Name:         "System: Memory Curator",
		SystemKind:   models.AgentSystemKindMemoryCurator,
		SystemPrompt: "Consolidate memory",
		Model:        "inherit",
		Tools:        []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: ".openvibely/memories", Permissions: []string{"read", "write", "delete"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	agent, _ := llmConfigRepo.GetDefault(ctx)
	agent.Provider = models.ProviderTest
	projectRepo := repository.NewProjectRepo(db)
	project, err := projectRepo.GetByID(ctx, "default")
	if err != nil {
		t.Fatalf("get default project: %v", err)
	}
	if project == nil {
		project = &models.Project{ID: "default", Name: "Default"}
	}
	project.RepoPath = repoPath
	if err := projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("update project repo path: %v", err)
	}
	task := &models.Task{
		ProjectID:         "default",
		Title:             "System: Memory Consolidation",
		Category:          models.CategoryScheduled,
		Status:            models.StatusPending,
		Prompt:            "Run scheduled memory consolidation for this project.",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution")
	}
	last := mock.LastCall()
	if last.Prompt != task.Prompt {
		t.Fatalf("expected scheduled task prompt, got %q", last.Prompt)
	}
	wantWorkDir := filepath.Join(repoPath, ".openvibely", "memories")
	if last.WorkDir != wantWorkDir {
		t.Fatalf("expected scoped memory workdir %q, got %q", wantWorkDir, last.WorkDir)
	}
	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("expected one execution, got %d", len(execs))
	}
	if execs[0].PromptSent != task.Prompt {
		t.Fatalf("expected execution prompt to be scheduled task prompt, got %q", execs[0].PromptSent)
	}
	if execs[0].Output != "memory task response" {
		t.Fatalf("expected execution output to be model response, got %q", execs[0].Output)
	}
}

func TestLLMService_ExecuteTask_GitWorktreeIsolation(t *testing.T) {
	tests := []struct {
		name           string
		prepareRepo    func(t *testing.T) string
		wantErr        string
		wantModelCalls int
	}{
		{
			name: "committed local repository without remote uses worktree",
			prepareRepo: func(t *testing.T) string {
				return createTestGitRepo(t)
			},
			wantModelCalls: 1,
		},
		{
			name: "unborn repository fails closed instead of using main checkout",
			prepareRepo: func(t *testing.T) string {
				repoDir := t.TempDir()
				cmd := exec.Command("git", "init", "-b", "main")
				cmd.Dir = repoDir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git init: %v\n%s", err, out)
				}
				return repoDir
			},
			wantErr: "repository has no commit for worktree base",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			llmConfigRepo := repository.NewLLMConfigRepo(db)
			execRepo := repository.NewExecutionRepo(db)
			taskRepo := repository.NewTaskRepo(db, nil)
			projectRepo := repository.NewProjectRepo(db)
			settingsRepo := repository.NewSettingsRepo(db)
			scheduleRepo := repository.NewScheduleRepo(db)
			attachmentRepo := repository.NewAttachmentRepo(db)

			repoDir := tt.prepareRepo(t)
			project := &models.Project{Name: "Worktree Isolation", RepoPath: repoDir}
			if err := projectRepo.Create(ctx, project); err != nil {
				t.Fatalf("create project: %v", err)
			}
			agent := ensureDefaultAgent(t, llmConfigRepo)
			task := &models.Task{ProjectID: project.ID, Title: "Isolated coding task", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "write a file", AgentID: &agent.ID}
			if err := taskRepo.Create(ctx, task); err != nil {
				t.Fatalf("create task: %v", err)
			}

			svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
			svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))
			mock := &testutil.MockLLMCaller{Response: "done", TextOnly: "done"}
			svc.SetLLMCaller(mock)

			_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
				}
			} else if err != nil {
				t.Fatalf("ExecuteTaskWithAgent: %v", err)
			}
			if got := mock.CallCount(); got != tt.wantModelCalls {
				t.Fatalf("expected %d model calls, got %d", tt.wantModelCalls, got)
			}

			updated, getErr := taskRepo.GetByID(ctx, task.ID)
			if getErr != nil {
				t.Fatalf("get task: %v", getErr)
			}
			if tt.wantErr != "" {
				if updated.Status != models.StatusFailed || updated.WorktreePath != "" {
					t.Fatalf("expected failed task without worktree metadata, got status=%s path=%q", updated.Status, updated.WorktreePath)
				}
				return
			}
			if updated.WorktreePath == "" || updated.WorktreePath == repoDir {
				t.Fatalf("expected isolated worktree path, got %q", updated.WorktreePath)
			}
			if got := mock.LastCall().WorkDir; got != updated.WorktreePath {
				t.Fatalf("expected model workdir %q, got %q", updated.WorktreePath, got)
			}
			if remotes, remoteErr := exec.Command("git", "-C", repoDir, "remote").Output(); remoteErr != nil || strings.TrimSpace(string(remotes)) != "" {
				t.Fatalf("test repository should have no remote, output=%q err=%v", remotes, remoteErr)
			}
		})
	}
}

func TestLLMService_ExecuteTask_DirectCheckoutCompletionIgnoresStaleWorktreeMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	repoPath := createTestGitRepo(t)
	staleWorktreePath := filepath.Join(t.TempDir(), "stale-worktree")
	runGitTest(t, repoPath, "worktree", "add", "-b", "task/stale-completion", staleWorktreePath, "main")
	if err := os.WriteFile(filepath.Join(staleWorktreePath, "stale.txt"), []byte("stale worktree change\n"), 0644); err != nil {
		t.Fatalf("write stale worktree change: %v", err)
	}
	staleHead := runGitTest(t, staleWorktreePath, "rev-parse", "HEAD")

	project := &models.Project{Name: "Direct Checkout Repo", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewAgentRepo(db)
	agentDef := &models.Agent{
		Name:         "Direct Checkout Agent",
		SystemPrompt: "Edit the project checkout directly",
		Model:        "inherit",
		ToolConfig: models.AgentToolConfig{
			DisableRuntimeWorktree: true,
		},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = models.ProviderTest
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Direct checkout completion",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "Update README in the project checkout.",
		AgentDefinitionID: &agentDef.ID,
		WorktreePath:      staleWorktreePath,
		WorktreeBranch:    "task/stale-completion",
		MergeTargetBranch: "main",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	beforeTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task before execution: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))
	caller := &completionPathWritingLLMCaller{fileName: "README.md", content: "# Direct checkout completion\n"}
	svc.SetLLMCaller(caller)

	execution, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if caller.workDir != repoPath {
		t.Fatalf("expected direct project checkout workdir %q, got %q", repoPath, caller.workDir)
	}
	if !strings.Contains(execution.DiffOutput, "+# Direct checkout completion") {
		t.Fatalf("expected completed execution to persist the direct-checkout HEAD diff, got:\n%s", execution.DiffOutput)
	}
	if strings.Contains(execution.DiffOutput, "stale.txt") || strings.Contains(execution.DiffOutput, "stale worktree change") {
		t.Fatalf("completed execution must not persist stale worktree changes, got:\n%s", execution.DiffOutput)
	}
	if got := runGitTest(t, staleWorktreePath, "rev-parse", "HEAD"); got != staleHead {
		t.Fatalf("completion committed stale worktree: HEAD changed from %s to %s", staleHead, got)
	}
	if got := runGitTest(t, staleWorktreePath, "status", "--porcelain"); got != "?? stale.txt" {
		t.Fatalf("expected stale worktree to remain untouched and dirty, got status %q", got)
	}
	afterTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task after execution: %v", err)
	}
	if afterTask.MergeStatus != beforeTask.MergeStatus {
		t.Fatalf("direct-checkout completion changed stale worktree merge status from %q to %q", beforeTask.MergeStatus, afterTask.MergeStatus)
	}
}

func TestLLMService_ExecuteTask_MemoryConsolidationSkipsRuntimeWorktree(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	repoPath := createTestGitRepo(t)
	project := &models.Project{Name: "Memory Repo", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agentRepo := repository.NewAgentRepo(db)
	agentDef := &models.Agent{
		Name:         "System: Memory Curator",
		SystemKind:   models.AgentSystemKindMemoryCurator,
		SystemPrompt: "Consolidate memory",
		Model:        "inherit",
		Tools:        []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{
			ScopedFiles:            []models.ScopedFilesConfig{{Directory: ".openvibely/memories", Permissions: []string{"read", "write", "delete"}}},
			SkipDefaultTools:       true,
			DisableRuntimeWorktree: true,
		},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))
	mock := &testutil.MockLLMCaller{Response: "memory task response", TextOnly: "memory task response", Tokens: 10}
	svc.SetLLMCaller(mock)

	agent, _ := llmConfigRepo.GetDefault(ctx)
	agent.Provider = models.ProviderTest
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "System: Memory Consolidation",
		Category:          models.CategoryScheduled,
		Status:            models.StatusPending,
		Prompt:            "Run scheduled memory consolidation for this project.",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution")
	}
	wantWorkDir := filepath.Join(repoPath, ".openvibely", "memories")
	if got := mock.LastCall().WorkDir; got != wantWorkDir {
		t.Fatalf("expected Memory Curator to write directly to repo memory dir %q, got %q", wantWorkDir, got)
	}
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.WorktreePath != "" || updated.WorktreeBranch != "" {
		t.Fatalf("Memory Curator should not create runtime worktree, got path=%q branch=%q", updated.WorktreePath, updated.WorktreeBranch)
	}
}

func TestLLMService_ExecuteTask_ScopedFilesAgentUsesRuntimeWorktreeByDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	repoPath := createTestGitRepo(t)
	project := &models.Project{Name: "Scoped Repo", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agentRepo := repository.NewAgentRepo(db)
	agentDef := &models.Agent{
		Name:                "Scoped Docs Agent",
		SystemPrompt:        "Edit scoped docs",
		Model:               "inherit",
		Tools:               []string{models.AgentToolScopedFiles},
		SelectableAsPrimary: true,
		ToolConfig: models.AgentToolConfig{
			ScopedFiles: []models.ScopedFilesConfig{{Directory: "docs", Permissions: []string{"read", "write"}}},
		},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))
	caller := &runtimeToolWritingLLMCaller{}
	svc.SetLLMCaller(caller)

	agent, _ := llmConfigRepo.GetDefault(ctx)
	agent.Provider = models.ProviderTest
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Scoped Docs Task",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "Update docs.",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution")
	}
	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.WorktreePath == "" || updated.WorktreeBranch == "" {
		t.Fatalf("expected generic scoped-files agent to use runtime worktree by default")
	}
	if got := caller.workDir; got != updated.WorktreePath {
		t.Fatalf("expected agent process workdir to stay on runtime worktree root %q when default tools are enabled, got %q", updated.WorktreePath, got)
	}
	if _, err := os.Stat(filepath.Join(updated.WorktreePath, "docs", "scoped.txt")); err != nil {
		t.Fatalf("expected scoped file write to land inside runtime worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "docs", "scoped.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected project repo docs to remain untouched, stat err=%v", err)
	}
}

func TestLLMService_ExecuteTask_UsesDefaultWhenNoAgentAssigned(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	defaultAgent, _ := llmConfigRepo.GetDefault(ctx)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Default Agent Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
		AgentID:   nil,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	mock := &testutil.MockLLMCaller{Response: "ok", TextOnly: "ok", Tokens: 10}
	svc.SetLLMCaller(mock)

	defaultAgent.Provider = models.ProviderTest

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *defaultAgent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec == nil {
		t.Fatal("expected execution record")
	}
	if exec.AgentConfigID != defaultAgent.ID {
		t.Errorf("expected execution to use default agent %s, got %s", defaultAgent.ID, exec.AgentConfigID)
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if mock.LastCall().Agent.ID != defaultAgent.ID {
		t.Errorf("expected callLLM to receive default agent %s, got %s", defaultAgent.ID, mock.LastCall().Agent.ID)
	}
}

func TestLLMService_ExecuteTaskWithAgent_LoadsAttachments(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	task := &models.Task{
		ProjectID: "default",
		Title:     "Task with Attachment",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "What do you see in the image?",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	attachmentPath := filepath.Join(t.TempDir(), "test.png")
	if err := os.WriteFile(attachmentPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("failed to create attachment file: %v", err)
	}
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "test.png",
		FilePath:  attachmentPath,
		MediaType: "image/png",
		FileSize:  3,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("failed to create attachment: %v", err)
	}

	mock := &testutil.MockLLMCaller{Response: "ok", TextOnly: "ok", Tokens: 10}
	svc.SetLLMCaller(mock)

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if exec == nil {
		t.Fatal("expected execution record")
	}

	if exec.PromptSent != task.Prompt {
		t.Errorf("expected PromptSent to match task prompt")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	lastCall := mock.LastCall()
	if len(lastCall.Attachments) != 1 {
		t.Errorf("expected 1 attachment passed to callLLM, got %d", len(lastCall.Attachments))
	} else if lastCall.Attachments[0].FileName != "test.png" {
		t.Errorf("expected attachment filename 'test.png', got %q", lastCall.Attachments[0].FileName)
	}
}

func TestLLMService_ExecuteTaskWithAgent_VisionAwareAgentOverride(t *testing.T) {

	anthropicAgent := models.LLMConfig{
		Name:       "Anthropic Sonnet",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		Model:      "claude-sonnet-4-20250514",
	}
	cliAgent := models.LLMConfig{
		Name:       "Claude Max",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodCLI,
		Model:      "claude-sonnet-4-5",
	}

	complexity := AnalyzeComplexity("What do you see?")
	result := SelectLLMWithVision(complexity, []models.LLMConfig{cliAgent, anthropicAgent}, true)
	if result == nil {
		t.Fatal("expected vision-capable agent to be selected")
	}
	if result.LLMConfig.Provider != models.ProviderAnthropic {
		t.Errorf("expected anthropic provider, got %s", result.LLMConfig.Provider)
	}
	if result.LLMConfig.Name != "Anthropic Sonnet" {
		t.Errorf("expected 'Anthropic Sonnet', got %q", result.LLMConfig.Name)
	}
}

func TestLLMService_ExecuteTaskWithAgent_NoOverrideForTextAttachments(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	task := &models.Task{
		ProjectID: "default",
		Title:     "Process Text File",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Read this file",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "data.json",
		FilePath:  "/tmp/data.json",
		MediaType: "application/json",
		FileSize:  512,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("failed to create attachment: %v", err)
	}

	defaultAgent, _ := llmConfigRepo.GetDefault(ctx)
	if defaultAgent == nil {
		t.Fatal("no default agent found")
	}

	agent := *defaultAgent
	agent.Provider = "unsupported"

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, agent)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}

	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error for text-only attachments, got: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}
}

func TestLLMService_CallAgentDirectStreaming_VisionAwareOverride(t *testing.T) {

	cliOnly := []models.LLMConfig{
		{Name: "Claude Max", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5"},
	}
	complexity := AnalyzeComplexity("What do you see?")
	result := SelectLLMWithVision(complexity, cliOnly, true)
	if result != nil {
		t.Errorf("expected nil when no vision-capable agent available, got %+v", result.LLMConfig)
	}

	withAnthropic := append(cliOnly, models.LLMConfig{
		Name: "Anthropic", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-sonnet-4-20250514",
	})
	result = SelectLLMWithVision(complexity, withAnthropic, true)
	if result == nil {
		t.Fatal("expected vision-capable agent to be selected")
	}
	if result.LLMConfig.Provider != models.ProviderAnthropic {
		t.Errorf("expected anthropic, got %s", result.LLMConfig.Provider)
	}
}

func TestLLMService_CallAgentDirectStreaming_NoOverrideWithoutImages(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	defaultAgent, _ := llmConfigRepo.GetDefault(ctx)
	if defaultAgent == nil {
		t.Fatal("no default agent found")
	}

	task := &models.Task{
		ProjectID: "default",
		Title:     "Chat No Images",
		Category:  models.CategoryChat,
		Status:    models.StatusPending,
		Prompt:    "Hello, how are you?",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: defaultAgent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	textAttachments := []models.Attachment{
		{
			FileName:  "data.json",
			FilePath:  "/tmp/data.json",
			MediaType: "application/json",
			FileSize:  512,
		},
	}

	agent := *defaultAgent
	agent.Provider = "unsupported"

	chatHistory := make([]models.Execution, 0)
	_, _, err := svc.CallAgentDirectStreaming(ctx, task.Prompt, textAttachments, agent, exec.ID, chatHistory, "", "", false)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}

	if !strings.Contains(err.Error(), "unsupported provider") {
		t.Errorf("expected 'unsupported provider' error for text-only attachments, got: %v", err)
	}
}

func TestLLMService_CallAgentDirectStreaming_VisionEnvVarFallback(t *testing.T) {

	cliOnly := []models.LLMConfig{
		{Name: "Claude Max", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet-4-5"},
		{Name: "Ollama Local", Provider: models.ProviderOllama, Model: "llama3"},
	}
	complexity := AnalyzeComplexity("What do you see?")
	result := SelectLLMWithVision(complexity, cliOnly, true)
	if result != nil {
		t.Errorf("expected nil when no vision-capable agents, got %+v", result.LLMConfig)
	}

}

func TestLLMService_ExecuteTaskWithAgent_MovesRepeatOnceToCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "RepeatOnce Scheduled Task",
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	schedule := &models.Schedule{
		TaskID:         task.ID,
		RunAt:          time.Now(),
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
	}
	if err := scheduleRepo.Create(ctx, schedule); err != nil {
		t.Fatalf("failed to create schedule: %v", err)
	}

	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}

	schedules, err := scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get schedules: %v", err)
	}
	if len(schedules) > 0 && schedules[0].RepeatType == models.RepeatOnce {
		if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); err != nil {
			t.Fatalf("failed to update category: %v", err)
		}
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected task to be moved to completed category, got %q", updated.Category)
	}
}

func TestLLMService_ExecuteTaskWithAgent_CommitsWorktreeEditsAndPersistsDiff(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	repoDir := createTestGitRepo(t)
	project := &models.Project{Name: "Provider Edit Project", RepoPath: repoDir}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	caller := &fileWritingLLMCaller{fileName: "anthropic-style.txt", content: "provider left this edit\n"}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(caller)
	svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))

	agent := ensureDefaultAgent(t, llmConfigRepo)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Capture Anthropic edits",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Create a file.",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	execRec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if execRec == nil {
		t.Fatal("expected execution")
	}
	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if caller.workDir != updatedTask.WorktreePath {
		t.Fatalf("expected provider to run in worktree %q, got %q", updatedTask.WorktreePath, caller.workDir)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "anthropic-style.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected main checkout to remain untouched, stat err=%v", err)
	}

	targetBranch := updatedTask.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}
	committedDiff := GetWorktreeDiff(repoDir, updatedTask.WorktreeBranch, targetBranch)
	if !strings.Contains(committedDiff, "provider left this edit") {
		t.Fatalf("expected task branch diff to contain provider edit, got:\n%s", committedDiff)
	}
	stored, err := execRepo.GetByID(ctx, execRec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if stored == nil || !strings.Contains(stored.DiffOutput, "provider left this edit") {
		t.Fatalf("expected persisted diff_output to contain provider edit, got %#v", stored)
	}
}

func TestLLMService_ExecuteTaskWithAgent_AllowsCompletionWithoutCodeChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "The screenshot shows the OpenVibely chat page in an idle state."
	mock.TextOnly = mock.Response
	mock.Tokens = 21

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)
	svc.SetWorktreeService(NewWorktreeService(taskRepo, projectRepo, settingsRepo))

	repoDir := createTestGitRepo(t)
	project := &models.Project{
		Name:     "Read Only Task Project",
		RepoPath: repoDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Describe screenshot contents",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Describe the attached screenshot and summarize the visible UI state.",
		AgentID:   &agent.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	execRec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if execRec == nil {
		t.Fatal("expected execution record")
	}
	if execRec.Status != models.ExecCompleted {
		t.Fatalf("expected completed execution, got %s", execRec.Status)
	}
	if execRec.ErrorMessage != "" {
		t.Fatalf("expected empty error message, got %q", execRec.ErrorMessage)
	}

	updatedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected task moved to completed category, got %s", updatedTask.Category)
	}
}

func TestLLMService_ExecuteTaskWithAgent_WebhookOriginSkipsTaskCreationMarkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)

	agent := ensureDefaultAgent(t, llmConfigRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = `[CREATE_TASK]{"title":"Unexpected child","prompt":"should not be created"}[/CREATE_TASK]`
	mock.TextOnly = mock.Response
	mock.Tokens = 9
	svc.SetLLMCaller(mock)

	parent := &models.Task{
		ProjectID:  "default",
		Title:      "Webhook Parent",
		Category:   models.CategoryActive,
		Status:     models.StatusPending,
		CreatedVia: models.TaskOriginWebhook,
		Prompt:     "Handle webhook payload",
		AgentID:    &agent.ID,
	}
	if err := taskRepo.Create(ctx, parent); err != nil {
		t.Fatalf("failed to create parent task: %v", err)
	}

	if _, err := svc.ExecuteTaskWithAgent(ctx, *parent, *agent); err != nil {
		t.Fatalf("ExecuteTaskWithAgent returned error: %v", err)
	}

	tasks, err := taskRepo.ListByProject(ctx, "default", "")
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected exactly 1 task (no fan-out), got %d", len(tasks))
	}
}

func TestLLMService_FailedTaskMovedToCompletedCategory(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Failed Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	output := "I tried to complete the task but encountered an error.\n[STATUS: FAILED | test error]"
	reason := "test error"

	if err := execRepo.Complete(ctx, exec.ID, models.ExecFailed, output, reason, 0, 100); err != nil {
		t.Fatalf("failed to complete execution: %v", err)
	}

	if err := taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed); err != nil {
		t.Fatalf("failed to update task status: %v", err)
	}

	if err := taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); err != nil {
		t.Fatalf("failed to move task to completed category: %v", err)
	}

	updated, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated task: %v", err)
	}
	if updated.Category != models.CategoryCompleted {
		t.Errorf("expected failed task to be moved to completed category, got %q", updated.Category)
	}
	if updated.Status != models.StatusFailed {
		t.Errorf("expected task status to be failed, got %q", updated.Status)
	}
}

// TestLLMService_ExecuteTaskWithAgent_IncludesManagedAndOptionalRootInstructions verifies
// managed lifecycle guidance is preserved while optional user-provided root
// AGENTS.md/CLAUDE.md files are also included when present.
func TestLLMService_ExecuteTaskWithAgent_IncludesManagedAndOptionalRootInstructions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	repoPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoPath, "AGENTS.md"), []byte("user AGENTS guidance"), 0o644); err != nil {
		t.Fatalf("write root AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "CLAUDE.md"), []byte("user CLAUDE guidance"), 0o644); err != nil {
		t.Fatalf("write root CLAUDE.md: %v", err)
	}
	project := &models.Project{Name: "Managed Instructions Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.providerAdapters[models.ProviderTest] = capture
	agent := ensureDefaultAgent(t, llmConfigRepo)
	task := &models.Task{ProjectID: project.ID, Title: "Managed instructions", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "do work", AgentID: &agent.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	managedCtx := withAdditionalProjectInstructions(ctx, "managed skill and memory instructions")
	if _, err := svc.ExecuteTaskWithAgent(managedCtx, *task, *agent); err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	if !strings.Contains(capture.lastReq.ProjectInstructions, "managed skill and memory instructions") {
		t.Fatalf("expected managed instructions in provider request, got %q", capture.lastReq.ProjectInstructions)
	}
	if !strings.Contains(capture.lastReq.ProjectInstructions, "# AGENTS.md") || !strings.Contains(capture.lastReq.ProjectInstructions, "user AGENTS guidance") {
		t.Fatalf("expected optional AGENTS.md instructions in provider request, got %q", capture.lastReq.ProjectInstructions)
	}
	if !strings.Contains(capture.lastReq.ProjectInstructions, "# CLAUDE.md") || !strings.Contains(capture.lastReq.ProjectInstructions, "user CLAUDE guidance") {
		t.Fatalf("expected optional CLAUDE.md instructions in provider request, got %q", capture.lastReq.ProjectInstructions)
	}
}

func TestLoadRootProjectInstructions_MissingFilesIsEmpty(t *testing.T) {
	if got := loadRootProjectInstructions(t.TempDir()); got != "" {
		t.Fatalf("expected missing root instruction files to produce empty instructions, got %q", got)
	}
}

func TestLLMService_ExecuteTaskWithAgent_PluginScopingWithAgentDef(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Use a capture adapter to inspect the request that reaches the provider
	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	// Override the test provider adapter with our capture
	svc.providerAdapters[models.ProviderTest] = capture

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Create an agent definition with plugins
	agentDef := &models.Agent{
		Name:                "test-plugin-agent",
		SystemPrompt:        "Use plugin tools for testing",
		Plugins:             []string{"test-plugin@test-market"},
		SelectableAsPrimary: true,
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Task with agent def",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "run plugin tools",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the agent definition was propagated to the adapter
	if capture.lastReq.AgentDefinition == nil {
		t.Fatal("expected agent definition to be propagated to adapter")
	}
	if capture.lastReq.AgentDefinition.ID != agentDef.ID {
		t.Fatalf("expected agent definition ID %q, got %q", agentDef.ID, capture.lastReq.AgentDefinition.ID)
	}
	if capture.lastReq.AgentDefinition.SystemPrompt != "Use plugin tools for testing" {
		t.Fatalf("expected agent system prompt propagated, got %q", capture.lastReq.AgentDefinition.SystemPrompt)
	}
}

// TestLLMService_ExecuteTaskWithAgent_NoAgentDef_NilPluginContext verifies that
// when a task has no AgentDefinitionID, the adapter receives nil AgentDefinition
// (zero plugin context).
func TestLLMService_ExecuteTaskWithAgent_NoAgentDef_NilPluginContext(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.providerAdapters[models.ProviderTest] = capture

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Create a task WITHOUT AgentDefinitionID
	task := &models.Task{
		ProjectID: "default",
		Title:     "Task without agent def",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "do something without plugins",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no agent definition was propagated (nil = zero plugin context)
	if capture.lastReq.AgentDefinition != nil {
		t.Fatalf("expected nil AgentDefinition for task without agent def, got %+v", capture.lastReq.AgentDefinition)
	}
	if len(capture.lastReq.PluginDirs) != 0 {
		t.Fatalf("expected zero PluginDirs for task without agent def, got %v", capture.lastReq.PluginDirs)
	}
}

// TestLLMService_ExecuteTaskWithAgent_WrongAgentDefNotUsed verifies that
// if a task references a non-existent agent definition, no plugin context
// leaks from other agent definitions.
func TestLLMService_ExecuteTaskWithAgent_WrongAgentDefNotUsed(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.providerAdapters[models.ProviderTest] = capture

	agent := ensureDefaultAgent(t, llmConfigRepo)

	// Create an agent definition that should NOT be used
	otherAgentDef := &models.Agent{
		Name:         "other-agent",
		SystemPrompt: "I am a different agent",
		Plugins:      []string{"other-plugin@other-market"},
	}
	if err := agentRepo.Create(ctx, otherAgentDef); err != nil {
		t.Fatalf("create other agent definition: %v", err)
	}

	// Create the task without AgentDefinitionID (to satisfy FK constraints),
	// then set it in memory to a non-existent ID before passing to ExecuteTaskWithAgent.
	task := &models.Task{
		ProjectID: "default",
		Title:     "Task with bad agent def ref",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "this should have no plugins",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Simulate a stale/invalid reference in the in-memory task object
	nonExistentID := "non-existent-agent-def-id"
	task.AgentDefinitionID = &nonExistentID

	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The non-existent agent def lookup should fail gracefully,
	// resulting in nil AgentDefinition (no plugin context)
	if capture.lastReq.AgentDefinition != nil {
		t.Fatalf("expected nil AgentDefinition for non-existent ref, got %+v", capture.lastReq.AgentDefinition)
	}
}

// TestLLMService_CallAgentDirectStreamingDetailed_PluginScopingByAgentDef
// verifies that CallAgentDirectStreamingDetailed (used by thread follow-ups)
// propagates agent definition correctly and that nil agent def means zero plugins.
func TestLLMService_CallAgentDirectStreamingDetailed_PluginScopingByAgentDef(t *testing.T) {
	capture := &captureProviderAdapter{}
	svc := &LLMService{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{
		models.ProviderOpenAI: capture,
	}

	agent := models.LLMConfig{Provider: models.ProviderOpenAI, Model: "gpt-test"}

	// Case 1: With agent definition
	agentDef := &models.Agent{
		ID:           "follow-up-agent",
		Name:         "followup-agent",
		SystemPrompt: "I handle follow-ups",
		Plugins:      []string{"followup-plugin@market"},
	}
	_, err := svc.CallAgentDirectStreamingDetailed(
		context.Background(), "follow up message", nil,
		agent, "exec-1", nil, "sys ctx", "/work", agentDef, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil {
		t.Fatal("expected agent definition for follow-up with agent def")
	}
	if capture.lastReq.AgentDefinition.ID != "follow-up-agent" {
		t.Fatalf("expected agent def ID follow-up-agent, got %q", capture.lastReq.AgentDefinition.ID)
	}

	// Case 2: Without agent definition (task has no agent assigned)
	_, err = svc.CallAgentDirectStreamingDetailed(
		context.Background(), "follow up without agent", nil,
		agent, "exec-2", nil, "sys ctx", "/work", nil, true,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capture.lastReq.AgentDefinition != nil {
		t.Fatalf("expected nil AgentDefinition for follow-up without agent def, got %+v", capture.lastReq.AgentDefinition)
	}
}

func TestLLMService_ExecuteTask_ScopedFilesPrepFailureCompletesExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetAgentRepo(agentRepo)
	svc.SetLLMCaller(&testutil.MockLLMCaller{Response: "should not run", TextOnly: "should not run", Tokens: 1})

	project, err := projectRepo.GetByID(ctx, "default")
	if err != nil {
		t.Fatalf("get default project: %v", err)
	}
	if project == nil {
		project = &models.Project{ID: "default", Name: "Default"}
	}
	project.RepoPath = t.TempDir()
	if err := projectRepo.Update(ctx, project); err != nil {
		t.Fatalf("update project repo path: %v", err)
	}

	agentDef := &models.Agent{
		Name:                "Bad scoped agent",
		SystemPrompt:        "Use scoped files",
		Model:               "inherit",
		Tools:               []string{models.AgentToolScopedFiles},
		SelectableAsPrimary: true,
		ToolConfig: models.AgentToolConfig{
			ScopedFiles: []models.ScopedFilesConfig{{Directory: "../escape", Permissions: []string{"read"}}},
		},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}

	agent, _ := llmConfigRepo.GetDefault(ctx)
	agent.Provider = models.ProviderTest
	task := &models.Task{
		ProjectID:         "default",
		Title:             "Bad scoped files",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "Try scoped files",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err == nil {
		t.Fatal("expected scoped files prep error")
	}
	if exec == nil {
		t.Fatal("expected failed execution to be returned")
	}
	if exec.Status != models.ExecFailed {
		t.Fatalf("expected returned execution failed, got %s", exec.Status)
	}

	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list executions: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("expected one execution, got %d", len(execs))
	}
	if execs[0].Status != models.ExecFailed {
		t.Fatalf("expected persisted execution failed, got %s", execs[0].Status)
	}
	if !strings.Contains(execs[0].ErrorMessage, "preparing scoped file tools") {
		t.Fatalf("expected scoped files error message, got %q", execs[0].ErrorMessage)
	}
}

type fakeGitHubIssueRuntimeProvider struct {
	issueMu              sync.Mutex
	issues               map[int]GitHubIssue
	globalAPIEndpoint    string
	resolveRepoFn        func(context.Context, string, string) (*GitHubRepoRef, error)
	createIssueFn        func(context.Context, *GitHubRepoRef, GitHubCreateIssueRequest) (*GitHubIssue, error)
	ensureIssueLabelsFn  func(context.Context, *GitHubRepoRef, []string) error
	getIssueFn           func(context.Context, *GitHubRepoRef, int) (*GitHubIssue, error)
	findPRFn             func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error)
	addLabelsFn          func(context.Context, *GitHubRepoRef, int, []string) error
	listMyIssuesFn       func(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error)
	listAssignedIssuesFn func(context.Context, *GitHubRepoRef, string) ([]GitHubIssue, error)
	listIssuesPRFn       func(context.Context, *GitHubRepoRef, string) ([]GitHubIssueWithPullRequest, error)
	listPRFeedbackFn     func(context.Context, *GitHubRepoRef, int) ([]GitHubPullRequestFeedback, error)
	commentIssueFn       func(context.Context, *GitHubRepoRef, int, string) error
	publishBranchFn      func(context.Context, *GitHubRepoRef, GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error)
	replaceBranchHeadFn  func(context.Context, *GitHubRepoRef, GitHubReplaceBranchHeadRequest) error
	getPullRequestFn     func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error)
	findBranchPRFn       func(context.Context, *GitHubRepoRef, string) (*GitHubPullRequest, error)
	createPRFn           func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error)
}

func (f *fakeGitHubIssueRuntimeProvider) ResolveRepo(ctx context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
	if f.resolveRepoFn != nil {
		return f.resolveRepoFn(ctx, repoURL, repoPath)
	}
	return &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
}

func (f *fakeGitHubIssueRuntimeProvider) DefaultBranch(ctx context.Context, repo *GitHubRepoRef) (string, error) {
	return "main", nil
}

func (f *fakeGitHubIssueRuntimeProvider) PublishBranch(ctx context.Context, repo *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
	if f.publishBranchFn != nil {
		return f.publishBranchFn(ctx, repo, req)
	}
	return &GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (f *fakeGitHubIssueRuntimeProvider) ReplaceBranchHead(ctx context.Context, repo *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) error {
	if f.replaceBranchHeadFn != nil {
		return f.replaceBranchHeadFn(ctx, repo, req)
	}
	return nil
}

func (f *fakeGitHubIssueRuntimeProvider) GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
	if f.getPullRequestFn != nil {
		return f.getPullRequestFn(ctx, repo, number)
	}
	return nil, fmt.Errorf("get PR not configured")
}

func (f *fakeGitHubIssueRuntimeProvider) FindPullRequestByBranch(ctx context.Context, repo *GitHubRepoRef, branch string) (*GitHubPullRequest, error) {
	if f.findBranchPRFn != nil {
		return f.findBranchPRFn(ctx, repo, branch)
	}
	return nil, nil
}

func (f *fakeGitHubIssueRuntimeProvider) CreatePullRequest(ctx context.Context, repo *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
	if f.createPRFn != nil {
		return f.createPRFn(ctx, repo, req)
	}
	return &GitHubPullRequest{Number: 101, URL: "https://github.com/openvibely/openvibely/pull/101", State: "open", HeadRef: req.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (f *fakeGitHubIssueRuntimeProvider) EnsureIssueLabels(ctx context.Context, repo *GitHubRepoRef, labels []string) error {
	if f.ensureIssueLabelsFn != nil {
		return f.ensureIssueLabelsFn(ctx, repo, labels)
	}
	return nil
}

func (f *fakeGitHubIssueRuntimeProvider) CreateIssue(ctx context.Context, repo *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
	var issue *GitHubIssue
	var err error
	if f.createIssueFn != nil {
		issue, err = f.createIssueFn(ctx, repo, req)
		if issue != nil && issue.Labels == nil {
			issue.Labels = append([]string(nil), req.Labels...)
		}
	} else {
		issue = &GitHubIssue{Number: 1, URL: "https://github.com/openvibely/openvibely/issues/1", Title: req.Title, Labels: req.Labels}
	}
	if issue != nil && err == nil {
		f.issueMu.Lock()
		if f.issues == nil {
			f.issues = make(map[int]GitHubIssue)
		}
		f.issues[issue.Number] = *issue
		f.issueMu.Unlock()
	}
	return issue, err
}

func (f *fakeGitHubIssueRuntimeProvider) GetIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubIssue, error) {
	if f.getIssueFn != nil {
		return f.getIssueFn(ctx, repo, issueNumber)
	}
	f.issueMu.Lock()
	issue, ok := f.issues[issueNumber]
	if !ok {
		baseURL := ""
		if repo != nil {
			baseURL = strings.TrimRight(strings.TrimSpace(repo.HTMLURL), "/")
			if baseURL == "" && strings.TrimSpace(repo.FullName) != "" {
				baseURL = "https://github.com/" + strings.Trim(strings.TrimSpace(repo.FullName), "/")
			}
		}
		if baseURL == "" {
			baseURL = "https://github.com/openvibely/openvibely"
		}
		issue = GitHubIssue{Number: issueNumber, URL: fmt.Sprintf("%s/issues/%d", baseURL, issueNumber), Title: "Issue", State: "open"}
		if f.issues == nil {
			f.issues = make(map[int]GitHubIssue)
		}
		f.issues[issueNumber] = issue
	}
	f.issueMu.Unlock()
	return &issue, nil
}

func (f *fakeGitHubIssueRuntimeProvider) GetAuthenticatedUser(ctx context.Context) (*GitHubAuthenticatedUser, error) {
	return &GitHubAuthenticatedUser{Login: "channel-user", Source: GitHubAuthModePAT}, nil
}

func (f *fakeGitHubIssueRuntimeProvider) GetAuthenticatedUserForRepo(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, error) {
	return f.GetAuthenticatedUser(ctx)
}

func (f *fakeGitHubIssueRuntimeProvider) ListAuthenticatedAssignedIssues(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
	if f.listMyIssuesFn != nil {
		return f.listMyIssuesFn(ctx, repo)
	}
	return &GitHubAuthenticatedUser{Login: "channel-user", Source: GitHubAuthModePAT}, []GitHubIssue{{Number: 5, URL: "https://github.com/openvibely/openvibely/issues/5", Title: "Testing", State: "open", Assignees: []string{"channel-user"}}}, nil
}

func (f *fakeGitHubIssueRuntimeProvider) ListAssignedIssues(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssue, error) {
	if f.listAssignedIssuesFn != nil {
		return f.listAssignedIssuesFn(ctx, repo, assignee)
	}
	return []GitHubIssue{{Number: 6, URL: "https://github.com/openvibely/openvibely/issues/6", Title: "Override", State: "open", Assignees: []string{assignee}}}, nil
}

func (f *fakeGitHubIssueRuntimeProvider) ListAssignedIssuesWithPullRequests(ctx context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssueWithPullRequest, error) {
	if f.listIssuesPRFn != nil {
		return f.listIssuesPRFn(ctx, repo, assignee)
	}
	return nil, nil
}

func (f *fakeGitHubIssueRuntimeProvider) FindPullRequestForIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int) (*GitHubPullRequest, error) {
	if f.findPRFn != nil {
		return f.findPRFn(ctx, repo, issueNumber)
	}
	return nil, nil
}

func (f *fakeGitHubIssueRuntimeProvider) ListPullRequestFeedback(ctx context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	if f.listPRFeedbackFn != nil {
		return f.listPRFeedbackFn(ctx, repo, prNumber)
	}
	return nil, nil
}

func (f *fakeGitHubIssueRuntimeProvider) CommentOnIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, bodyText string) error {
	if f.commentIssueFn != nil {
		return f.commentIssueFn(ctx, repo, issueNumber, bodyText)
	}
	return nil
}

func (f *fakeGitHubIssueRuntimeProvider) AddLabelsToIssue(ctx context.Context, repo *GitHubRepoRef, issueNumber int, labels []string) error {
	if f.addLabelsFn != nil {
		return f.addLabelsFn(ctx, repo, issueNumber, labels)
	}
	for _, label := range labels {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), "openvibely:") {
			return fmt.Errorf("github issue labels must not use openvibely: prefix: %s", strings.TrimSpace(label))
		}
	}
	f.issueMu.Lock()
	issue, ok := f.issues[issueNumber]
	if ok {
		issue.Labels = normalizeDraftReferences(append(issue.Labels, labels...))
		f.issues[issueNumber] = issue
	}
	f.issueMu.Unlock()
	return nil
}

func (f *fakeGitHubIssueRuntimeProvider) GlobalAPIEndpoint(_ context.Context) string {
	return f.globalAPIEndpoint
}

func TestGitHubRuntimeCurrentEnterpriseRepositoryRequiresAPIEndpoint(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{
		Name:    "Enterprise runtime repository without endpoint",
		RepoURL: "https://github.example.com/acme/widgets.git",
	}
	require.NoError(t, projectRepo.Create(ctx, project))
	provider := &fakeGitHubIssueRuntimeProvider{resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
		require.Equal(t, project.RepoURL, repoURL)
		require.Empty(t, repoPath)
		parsed, err := ParseGitHubRepoURL(repoURL)
		require.NoError(t, err)
		return &parsed, nil
	}}
	opts := githubIssueRuntimeOptions{ProjectID: project.ID, ProjectRepo: projectRepo, GitHub: provider}

	repo, err := resolveGitHubRepoForRuntimeTool(ctx, opts)
	require.Nil(t, repo)
	require.ErrorContains(t, err, "custom repository host requires a configured GitHub API endpoint")
}

func TestGitHubRuntimeExplicitRepositoryUsesProjectEndpointByParsedHost(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	const enterpriseEndpoint = "https://github.example.com/api/v3"
	project := &models.Project{
		Name:    "Enterprise runtime repository",
		RepoURL: "https://github.example.com/acme/widgets.git",
	}
	require.NoError(t, projectRepo.Create(ctx, project))
	provider := &fakeGitHubIssueRuntimeProvider{
		globalAPIEndpoint: enterpriseEndpoint,
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
			require.Empty(t, repoPath)
			parsed, err := ParseGitHubRepoURL(repoURL)
			require.NoError(t, err)
			return &parsed, nil
		},
	}
	opts := githubIssueRuntimeOptions{ProjectID: project.ID, ProjectRepo: projectRepo, GitHub: provider}

	for _, repoURL := range []string{
		"git@github.example.com:acme/widgets.git",
		"https://github.example.com/acme/sibling.git",
	} {
		repo, err := resolveGitHubRepoForRuntimeToolURL(ctx, opts, repoURL)
		require.NoError(t, err)
		require.Equal(t, enterpriseEndpoint, repo.APIBaseURL)
	}

	repo, err := resolveGitHubRepoForRuntimeToolURL(ctx, opts, "https://github.com/acme/widgets.git")
	require.Nil(t, repo)
	require.ErrorContains(t, err, "repository host")
}

func TestAutomationGitHubRuntimeToolsAlwaysResolveCurrentProjectRepository(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Automation repository boundary", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/project.git"}
	require.NoError(t, projectRepo.Create(ctx, project))
	var resolvedURL, resolvedPath string
	provider := &fakeGitHubIssueRuntimeProvider{resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
		resolvedURL, resolvedPath = repoURL, repoPath
		return &GitHubRepoRef{Owner: "openvibely", Name: "project", FullName: "openvibely/project", HTMLURL: "https://github.com/openvibely/project"}, nil
	}}
	opts := githubIssueRuntimeOptions{ProjectID: project.ID, ProjectRepo: projectRepo, GitHub: provider}
	automationCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID,
		Bindings: []models.AutomationBinding{{AutomationID: "automation", VersionID: "graph", NodeID: "node"}}})

	_, err := resolveGitHubRepoForRuntimeToolURL(automationCtx, opts, "https://github.com/attacker/other")
	require.NoError(t, err)
	require.Equal(t, project.RepoURL, resolvedURL)
	require.Empty(t, resolvedPath, "Automation issue tools must never pass the local checkout as a repository inference fallback")

	project.RepoURL = ""
	require.NoError(t, projectRepo.Update(ctx, project))
	provider.resolveRepoFn = func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
		resolvedURL, resolvedPath = repoURL, repoPath
		return &GitHubRepoRef{Owner: "openvibely", Name: "project", FullName: "openvibely/project", HTMLURL: "https://github.com/openvibely/project"}, nil
	}
	_, err = resolveGitHubRepoForRuntimeToolURL(automationCtx, opts, "https://github.com/attacker/other")
	require.NoError(t, err)
	require.Empty(t, resolvedURL, "Automation runtime must ignore the model repository override")
	require.Equal(t, project.RepoPath, resolvedPath, "Automation runtime must resolve the project's local Git remote when repo_url is absent")

	project.RepoURL = "https://github.com/openvibely/project.git"
	require.NoError(t, projectRepo.Update(ctx, project))
	provider.resolveRepoFn = func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
		resolvedURL, resolvedPath = repoURL, repoPath
		return &GitHubRepoRef{Owner: "example", Name: "other", FullName: "example/other", HTMLURL: "https://github.com/example/other"}, nil
	}
	_, err = resolveGitHubRepoForRuntimeToolURL(ctx, opts, "https://github.com/example/other")
	require.NoError(t, err)
	require.Equal(t, "https://github.com/example/other", resolvedURL, "ordinary Chat must retain explicit repository selection")
	require.Empty(t, resolvedPath)
}

func TestAutomationGitHubRuntimeToolsDoNotExposeStatusCommenting(t *testing.T) {
	defs := gitHubIssueRuntimeToolDefs(true)
	for _, def := range defs {
		require.NotEqual(t, "github_comment_on_issue", def.Name)
	}
	require.Contains(t, runtimeToolDefinitionSet(defs), "github_open_pull_request")
}

func TestGitHubIssueRuntimeToolsExposeDefaultTaskToolsAndPreserveSafetyRules(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "GitHub Runtime Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely.git"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Linked Task", Category: models.CategoryBacklog, Status: models.StatusPending, Prompt: "work"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	var createdRepo, commentedRepo, labeledRepo string
	var replacementReq GitHubReplaceBranchHeadRequest
	provider := &fakeGitHubIssueRuntimeProvider{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
			switch repoURL {
			case project.RepoURL:
				return &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
			case "https://github.com/example/other":
				if strings.TrimSpace(repoPath) != "" {
					t.Fatalf("expected explicit repo_url lookup to avoid local repo path, got %q", repoPath)
				}
				return &GitHubRepoRef{Owner: "example", Name: "other", FullName: "example/other", HTMLURL: "https://github.com/example/other"}, nil
			default:
				t.Fatalf("unexpected repo URL %q", repoURL)
				return nil, nil
			}
		},
		listMyIssuesFn: func(_ context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, []GitHubIssue, error) {
			if repo.Owner == "example" && repo.Name == "other" {
				return &GitHubAuthenticatedUser{Login: "channel-user", Source: GitHubAuthModePAT}, []GitHubIssue{{Number: 7, URL: "https://github.com/example/other/issues/7", Title: "Explicit URL", State: "open", Assignees: []string{"channel-user"}}}, nil
			}
			return &GitHubAuthenticatedUser{Login: "channel-user", Source: GitHubAuthModePAT}, []GitHubIssue{{Number: 5, URL: "https://github.com/openvibely/openvibely/issues/5", Title: "Testing", State: "open", Assignees: []string{"channel-user"}}}, nil
		},
		listAssignedIssuesFn: func(_ context.Context, repo *GitHubRepoRef, assignee string) ([]GitHubIssue, error) {
			if repo.Owner == "example" && repo.Name == "other" {
				return []GitHubIssue{{Number: 8, URL: "https://github.com/example/other/issues/8", Title: "Explicit assignee URL", State: "open", Assignees: []string{assignee}}}, nil
			}
			return []GitHubIssue{{Number: 6, URL: "https://github.com/openvibely/openvibely/issues/6", Title: "Override", State: "open", Assignees: []string{assignee}}}, nil
		},
		createIssueFn: func(_ context.Context, repo *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
			createdRepo = repo.FullName
			return &GitHubIssue{Number: 9, URL: "https://github.com/example/other/issues/9", Title: req.Title, State: "open", Labels: req.Labels, Assignees: req.Assignees}, nil
		},
		commentIssueFn: func(_ context.Context, repo *GitHubRepoRef, issueNumber int, body string) error {
			commentedRepo = repo.FullName
			if issueNumber != 9 || body != "Looks good" {
				t.Fatalf("unexpected runtime comment input issue=%d body=%q", issueNumber, body)
			}
			return nil
		},
		addLabelsFn: func(_ context.Context, repo *GitHubRepoRef, issueNumber int, labels []string) error {
			for _, label := range labels {
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(label)), "openvibely:") {
					return fmt.Errorf("github issue labels must not use openvibely: prefix: %s", strings.TrimSpace(label))
				}
			}
			labeledRepo = repo.FullName
			if issueNumber != 9 || len(labels) != 1 || labels[0] != "approved" {
				t.Fatalf("unexpected runtime labels input issue=%d labels=%v", issueNumber, labels)
			}
			return nil
		},
		getPullRequestFn: func(_ context.Context, _ *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
			switch number {
			case 202:
				return &GitHubPullRequest{Number: number, URL: "https://github.com/openvibely/openvibely/pull/202", State: "open", HeadRef: "task/existing-runtime-pr", HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
			case 203:
				return &GitHubPullRequest{Number: number, URL: "https://github.com/openvibely/openvibely/pull/203", State: "open", HeadRef: "task/existing-runtime-pr-url", HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
			default:
				return nil, fmt.Errorf("unexpected pull request #%d", number)
			}
		},
		replaceBranchHeadFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) error {
			replacementReq = req
			return nil
		},
	}
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "Dev-Bot"}))
	rt := buildGitHubIssueRuntimeTools(githubIssueRuntimeOptions{
		ProjectID:            project.ID,
		ProjectRepo:          projectRepo,
		TaskRepo:             taskRepo,
		TaskPullRequestRepo:  prRepo,
		GitHubPRFeedbackRepo: feedbackRepo,
		GitHubAuthRepo:       githubAuthRepo,
		ThreadInputRepo:      threadInputRepo,
		GitHub:               provider,
	})
	for _, name := range []string{"github_create_issue", "github_get_issue", "github_get_project_inbox", "github_is_actor_authorized", "github_list_my_assigned_issues", "github_list_assigned_issues", "github_add_issue_labels", "github_open_pull_request", "github_replace_pull_request_branch", "github_forward_pr_feedback_to_tasks"} {
		if rt == nil || !rt.HasDefinition(name) {
			t.Fatalf("expected %s in default GitHub runtime definitions for task runs, got %#v", name, rt)
		}
	}

	out, handled, isErr, err := rt.Executor(ctx, "github_list_my_assigned_issues", []byte(`{}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected my assigned issues success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"login":"channel-user"`) || !strings.Contains(out, `"Number":5`) {
		t.Fatalf("expected authenticated assigned issues output, got %s", out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_list_assigned_issues", []byte(`{"assignee":"Dev-Bot"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected explicit assigned issues success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"assignee":"dev-bot"`) || !strings.Contains(out, `"Number":6`) {
		t.Fatalf("expected explicit assigned issues output, got %s", out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_list_my_assigned_issues", []byte(`{"repo_url":"https://github.com/example/other"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected explicit repo_url my assigned issues success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"Number":7`) || !strings.Contains(out, `"https://github.com/example/other/issues/7"`) {
		t.Fatalf("expected explicit repo_url my assigned issues output, got %s", out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_list_assigned_issues", []byte(`{"assignee":"Dev-Bot","repo_url":"https://github.com/example/other"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected explicit repo_url assigned issues success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"Number":8`) || !strings.Contains(out, `"https://github.com/example/other/issues/8"`) {
		t.Fatalf("expected explicit repo_url assigned issues output, got %s", out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_create_issue", []byte(`{"title":"URL issue","body":"Created by URL","labels":["bug"],"assignees":["dev-bot"],"repo_url":"https://github.com/example/other"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected explicit repo_url create issue success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if createdRepo != "example/other" || !strings.Contains(out, `"Number":9`) || !strings.Contains(out, `"https://github.com/example/other/issues/9"`) {
		t.Fatalf("expected explicit repo_url create output repo=%q out=%s", createdRepo, out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_comment_on_issue", []byte(`{"issue_number":9,"body":"Looks good","repo_url":"https://github.com/example/other"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected explicit repo_url comment success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if commentedRepo != "example/other" || !strings.Contains(out, `"issue_number":9`) {
		t.Fatalf("expected explicit repo_url comment output repo=%q out=%s", commentedRepo, out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_add_issue_labels", []byte(`{"issue_number":9,"labels":["approved"],"repo_url":"https://github.com/example/other"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected explicit repo_url label success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if labeledRepo != "example/other" || !strings.Contains(out, `"labels":["approved"]`) {
		t.Fatalf("expected explicit repo_url label output repo=%q out=%s", labeledRepo, out)
	}

	_, handled, isErr, err = rt.Executor(ctx, "github_add_issue_labels", []byte(`{"issue_number":77,"labels":["openvibely:bug"]}`))
	if !handled || !isErr || err == nil || !strings.Contains(err.Error(), "openvibely:") {
		t.Fatalf("expected prefixed label rejection handled=%v isErr=%v err=%v", handled, isErr, err)
	}

	task.WorktreeBranch = "task/runtime-pr"
	if err := taskRepo.UpdateWorktreeInfo(ctx, task.ID, "", task.WorktreeBranch); err != nil {
		t.Fatalf("update task worktree branch: %v", err)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_open_pull_request", []byte(fmt.Sprintf(`{"task_id":"%s","pr_title":"Runtime PR","pr_body":"Opened from runtime","base":"main","issue_number":99,"issue_url":"https://github.com/openvibely/openvibely/issues/99"}`, task.ID)))
	if !handled || isErr || err != nil {
		t.Fatalf("expected open PR success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"created":true`) || !strings.Contains(out, `"pull_request"`) {
		t.Fatalf("expected open PR output, got %s", out)
	}
	record, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("lookup opened task PR: %v", err)
	}
	if record == nil || record.PRNumber != 101 || record.IssueNumber == nil || *record.IssueNumber != 99 {
		t.Fatalf("unexpected opened task PR record: %#v", record)
	}

	existingPRTask := &models.Task{ProjectID: project.ID, Title: "Existing Runtime PR", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "work"}
	if err := taskRepo.Create(ctx, existingPRTask); err != nil {
		t.Fatalf("create existing PR task: %v", err)
	}
	existingPRWorktree := t.TempDir()
	if err := taskRepo.UpdateWorktreeInfo(ctx, existingPRTask.ID, existingPRWorktree, "task/existing-runtime-pr"); err != nil {
		t.Fatalf("update existing PR task worktree branch: %v", err)
	}
	oldIssueNumber := 99
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: existingPRTask.ID, PRNumber: 202, PRURL: "https://github.com/openvibely/openvibely/pull/202", PRState: "open", IssueNumber: &oldIssueNumber, IssueURL: "https://github.com/openvibely/openvibely/issues/99"}); err != nil {
		t.Fatalf("seed existing runtime PR: %v", err)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_open_pull_request", []byte(fmt.Sprintf(`{"task_id":"%s","issue_number":100}`, existingPRTask.ID)))
	if !handled || isErr || err != nil {
		t.Fatalf("expected existing PR reuse success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"reused_existing_record":true`) {
		t.Fatalf("expected existing PR reuse output, got %s", out)
	}
	record, err = prRepo.GetByTaskID(ctx, existingPRTask.ID)
	if err != nil {
		t.Fatalf("lookup existing runtime task PR: %v", err)
	}
	if record == nil || record.PRNumber != 202 || record.IssueNumber == nil || *record.IssueNumber != 100 || record.IssueURL != "" {
		t.Fatalf("expected existing runtime PR issue number with stale URL cleared, got %#v", record)
	}

	expectedHead := strings.Repeat("a", 40)
	out, handled, isErr, err = rt.Executor(ctx, "github_replace_pull_request_branch", []byte(fmt.Sprintf(`{"task_id":"%s","expected_head_sha":"%s","confirm_history_rewrite":true}`, existingPRTask.ID, expectedHead)))
	if !handled || isErr || err != nil {
		t.Fatalf("expected PR branch replacement success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if replacementReq.WorktreePath != existingPRWorktree || replacementReq.Branch != "task/existing-runtime-pr" || replacementReq.ExpectedHead != expectedHead {
		t.Fatalf("unexpected runtime replacement request: %#v", replacementReq)
	}
	if !strings.Contains(out, `"replaced_branch":"task/existing-runtime-pr"`) {
		t.Fatalf("expected replacement output, got %s", out)
	}

	existingURLTask := &models.Task{ProjectID: project.ID, Title: "Existing Runtime PR URL", Category: models.CategoryActive, Status: models.StatusCompleted, Prompt: "work"}
	if err := taskRepo.Create(ctx, existingURLTask); err != nil {
		t.Fatalf("create existing URL PR task: %v", err)
	}
	if err := taskRepo.UpdateWorktreeInfo(ctx, existingURLTask.ID, "", "task/existing-runtime-pr-url"); err != nil {
		t.Fatalf("update existing URL PR task worktree branch: %v", err)
	}
	oldURLIssueNumber := 123
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: existingURLTask.ID, PRNumber: 203, PRURL: "https://github.com/openvibely/openvibely/pull/203", PRState: "open", IssueNumber: &oldURLIssueNumber, IssueURL: "https://github.com/openvibely/openvibely/issues/123"}); err != nil {
		t.Fatalf("seed existing URL runtime PR: %v", err)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_open_pull_request", []byte(fmt.Sprintf(`{"task_id":"%s","issue_url":"https://github.com/openvibely/openvibely/issues/456"}`, existingURLTask.ID)))
	if !handled || isErr || err != nil {
		t.Fatalf("expected existing PR URL reuse success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	record, err = prRepo.GetByTaskID(ctx, existingURLTask.ID)
	if err != nil {
		t.Fatalf("lookup existing URL runtime task PR: %v", err)
	}
	if record == nil || record.PRNumber != 203 || record.IssueNumber != nil || record.IssueURL != "https://github.com/openvibely/openvibely/issues/456" {
		t.Fatalf("expected existing runtime PR issue URL with stale number cleared, got %#v", record)
	}

	out, handled, isErr, err = rt.Executor(ctx, "github_is_actor_authorized", []byte(`{"github_login":"alice"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected authorization check success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"authorized":false`) {
		t.Fatalf("expected deny-by-default authorization output, got %s", out)
	}
	if err := githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "Dev-Bot", Permission: "triage"}); err != nil {
		t.Fatalf("configure github authorized user: %v", err)
	}
	if err := githubAuthRepo.UpsertProjectInbox(ctx, &models.GitHubProjectInbox{ProjectID: project.ID, GitHubLogin: "Legacy-Bot", Enabled: true}); err != nil {
		t.Fatalf("configure legacy github project inbox: %v", err)
	}
	out, handled, isErr, err = rt.Executor(ctx, "github_get_project_inbox", []byte(`{}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected project inbox tool success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"configured":true`) || !strings.Contains(out, `"assignees":["dev-bot"]`) || !strings.Contains(out, `"legacy_inbox"`) || !strings.Contains(out, `"github_login":"legacy-bot"`) {
		t.Fatalf("expected authorized-user assignee output with legacy inbox metadata, got %s", out)
	}
}

func TestGitHubIssueRuntimeForwardPRFeedbackPromotesLinkedTasks(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	githubAuthRepo := repository.NewGitHubAuthRepo(db)

	project := &models.Project{Name: "GitHub Feedback Runtime", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely.git"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Completed PR Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Prompt: "done"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "dubee", Permission: "triage", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize actor: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}); err != nil {
		t.Fatalf("seed pr: %v", err)
	}

	provider := &fakeGitHubIssueRuntimeProvider{
		listPRFeedbackFn: func(_ context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
			if repo.FullName != "openvibely/openvibely" || prNumber != 4 {
				t.Fatalf("unexpected feedback lookup repo=%#v pr=%d", repo, prNumber)
			}
			return []GitHubPullRequestFeedback{{Kind: "issue_comment", ID: "4921937310", AuthorLogin: "dubee", AuthorType: "User", Body: "This branch has conflicts", URL: "https://github.com/openvibely/openvibely/pull/4#issuecomment-4921937310", CreatedAt: time.Date(2026, 7, 9, 5, 44, 10, 0, time.UTC)}}, nil
		},
	}
	var promoted []string
	rt := buildGitHubIssueRuntimeTools(githubIssueRuntimeOptions{
		ProjectID:                project.ID,
		ProjectRepo:              projectRepo,
		TaskRepo:                 taskRepo,
		TaskPullRequestRepo:      prRepo,
		GitHubPRFeedbackRepo:     feedbackRepo,
		GitHubAuthRepo:           githubAuthRepo,
		ThreadInputRepo:          threadInputRepo,
		GitHub:                   provider,
		AfterPRFeedbackForwarded: func(taskID string) { promoted = append(promoted, taskID) },
	})
	out, handled, isErr, err := rt.Executor(ctx, "github_forward_pr_feedback_to_tasks", []byte(`{}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected feedback forward success handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if len(promoted) != 1 || promoted[0] != task.ID {
		t.Fatalf("expected linked task promoted once, got %#v", promoted)
	}
	pending, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || !strings.Contains(pending[0].Content, "This branch has conflicts") {
		t.Fatalf("expected queued conflict feedback, got %#v", pending)
	}
}

func TestLLMServiceExecuteTaskWithAgentExposesBootstrapToolsToInitialRuns(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	goalRepo := repository.NewTaskGoalRepo(db)
	goalSvc := NewTaskGoalService(goalRepo, taskRepo, nil)

	agent := ensureDefaultAgent(t, llmConfigRepo)
	agent.Provider = models.ProviderOpenAI
	agent.AuthMethod = models.AuthMethodAPIKey
	agent.APIKey = "test-key"
	project := &models.Project{Name: "Initial GitHub Runtime Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely.git"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentDef := &models.Agent{Key: "github_dev_poller", Name: "GitHub Dev Poller", Enabled: true, Tools: []string{"github_get_issue"}}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Scheduled GitHub poller", Category: models.CategoryScheduled, Status: models.StatusPending, Prompt: "poll assigned GitHub issues", AgentDefinitionID: &agentDef.ID}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	capture := &captureProviderAdapter{}
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderOpenAI: capture}
	svc.SetAgentRepo(agentRepo)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, nil)
	taskSvc.SetTaskGoalService(goalSvc)
	svc.SetTaskService(taskSvc)
	svc.SetTaskGoalService(goalSvc)
	svc.SetGitHubIssueRuntimeProvider(&fakeGitHubIssueRuntimeProvider{})
	svc.SetGitHubAuthRepo(githubAuthRepo)
	svc.SetTaskPullRequestRepo(prRepo)
	svc.SetGitHubPRFeedbackRepo(feedbackRepo)
	svc.SetThreadInputRepo(threadInputRepo)

	if _, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent); err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	rt := llmcontracts.RuntimeToolsFromContext(capture.lastReq.Ctx)
	if rt == nil {
		t.Fatal("expected runtime tools on provider request")
	}
	for _, name := range []string{"list_tasks", "create_task", "set_task_goal", "get_task_goal", "schedule_task", "modify_schedule", "github_create_issue", "github_get_issue", "github_get_project_inbox", "github_is_actor_authorized", "github_list_my_assigned_issues", "github_list_assigned_issues", "github_list_assigned_issues_with_prs", "github_comment_on_issue", "github_add_issue_labels", "github_open_pull_request", "github_replace_pull_request_branch", "github_forward_pr_feedback_to_tasks"} {
		if !rt.HasDefinition(name) {
			t.Fatalf("expected %s on initial task run, got %#v", name, rt.Definitions)
		}
	}
	if out, handled, isErr, err := rt.Executor(ctx, "list_tasks", []byte(`{"query":"scheduled github poller"}`)); !handled || isErr || err != nil {
		t.Fatalf("expected list_tasks handler on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	} else if !strings.Contains(out, `"ok":true`) || !strings.Contains(out, task.ID) {
		t.Fatalf("expected list_tasks to discover the scheduled task, got %s", out)
	}
	out, handled, isErr, err := rt.Executor(ctx, "github_get_project_inbox", []byte(`{}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected github_get_project_inbox handler on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, `"ok":true`) {
		t.Fatalf("expected project inbox success output, got %s", out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "list_capabilities", []byte(`{}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected list_capabilities handler on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, "schedule_task") || !strings.Contains(out, "github_get_project_inbox") {
		t.Fatalf("expected capabilities to include bootstrap tools, got %s", out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "create_task", []byte(`{"title":"Implement GitHub issue #42","prompt":"Implement assigned GitHub issue #42 and open a PR."}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected create_task handler for implementation task on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	implementationTask, err := taskRepo.GetByProjectAndTitle(ctx, project.ID, "Implement GitHub issue #42")
	if err != nil || implementationTask == nil {
		t.Fatalf("expected created implementation task, task=%#v err=%v out=%s", implementationTask, err, out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "set_task_goal", []byte(`{"title":"Implement GitHub issue #42","goal":"Implement assigned GitHub issue #42 and open a GitHub PR."}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected set_task_goal handler for implementation task on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	goal, err := goalSvc.GetGoal(ctx, implementationTask.ID)
	if err != nil || goal == nil || goal.Objective != "Implement assigned GitHub issue #42 and open a GitHub PR." {
		t.Fatalf("expected persisted goal for implementation task, goal=%#v err=%v out=%s", goal, err, out)
	}
	// Dev Inbox reconciliation: a subsequent run can discover the existing
	// implementation task by GitHub issue number before creating a duplicate.
	if out, handled, isErr, err := rt.Executor(ctx, "list_tasks", []byte(`{"query":"issue #42"}`)); !handled || isErr || err != nil {
		t.Fatalf("expected list_tasks reconciliation handler handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	} else if !strings.Contains(out, implementationTask.ID) {
		t.Fatalf("expected list_tasks to reconcile existing implementation task, got %s", out)
	}

	out, handled, isErr, err = rt.Executor(ctx, "create_task", []byte(`{"title":"GitHub Dev Inbox","prompt":"Poll assigned GitHub issues and create implementation tasks."}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected create_task handler for scheduled loop task on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	loopTask, err := taskRepo.GetByProjectAndTitle(ctx, project.ID, "GitHub Dev Inbox")
	if err != nil || loopTask == nil {
		t.Fatalf("expected created loop task, task=%#v err=%v out=%s", loopTask, err, out)
	}
	out, handled, isErr, err = rt.Executor(ctx, "schedule_task", []byte(`{"title":"GitHub Dev Inbox","time":"09:30","repeat":"daily"}`))
	if !handled || isErr || err != nil {
		t.Fatalf("expected schedule_task handler on initial task run handled=%v isErr=%v err=%v out=%s", handled, isErr, err, out)
	}
	if !strings.Contains(out, "Scheduled task") {
		t.Fatalf("expected schedule output, got %s", out)
	}
	schedules, err := scheduleRepo.ListByTask(ctx, loopTask.ID)
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected one schedule, got %d", len(schedules))
	}
	loopGoal, err := goalSvc.GetGoal(ctx, loopTask.ID)
	if err != nil {
		t.Fatalf("get loop task goal: %v", err)
	}
	if loopGoal != nil {
		t.Fatalf("expected scheduled loop task to have no persisted goal, got %#v", loopGoal)
	}
}
