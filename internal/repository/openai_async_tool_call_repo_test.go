package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createAsyncToolCallFixture(t *testing.T, repo *OpenAIAsyncToolCallRepo, projectID, taskID, execID, callID string, deadline time.Time) *models.OpenAIAsyncToolCall {
	t.Helper()
	ctx := context.Background()
	call, err := repo.CreatePending(ctx, &models.OpenAIAsyncToolCall{
		ProjectID:     projectID,
		TaskID:        taskID,
		ExecutionID:   execID,
		ResponseID:    "resp_1",
		CallID:        callID,
		ToolName:      "memory_view",
		ArgumentsJSON: `{"handle":"provider_architecture.md"}`,
		DeadlineAt:    deadline,
	})
	if err != nil {
		t.Fatalf("CreatePending: %v", err)
	}
	return call
}

func TestOpenAIAsyncToolCallRepoLifecycleTransitions(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "async-project"
	taskID := "async-task"
	execID := "async-exec"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id, name, repo_path) VALUES (?, 'Async Project', '')`, projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id, project_id, title, status, category, prompt) VALUES (?, ?, 'Async Task', 'running', 'active', 'prompt')`, taskID, projectID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO executions(id, task_id, status, prompt_sent) VALUES (?, ?, 'running', 'prompt')`, execID, taskID); err != nil {
		t.Fatalf("insert execution: %v", err)
	}

	repo := NewOpenAIAsyncToolCallRepo(db)
	now := time.Now().UTC()
	call := createAsyncToolCallFixture(t, repo, projectID, taskID, execID, "call_1", now.Add(time.Hour))
	if call.ProjectID != projectID || call.TaskID != taskID || call.Status != models.OpenAIAsyncToolCallPending {
		t.Fatalf("unexpected pending call: %+v", call)
	}
	duplicate := createAsyncToolCallFixture(t, repo, projectID, taskID, execID, "call_1", now.Add(time.Hour))
	if duplicate.ID != call.ID {
		t.Fatalf("duplicate create returned id %s, want %s", duplicate.ID, call.ID)
	}

	claimed, err := repo.ClaimForRun(ctx, call.ID, now)
	if err != nil || !claimed {
		t.Fatalf("ClaimForRun claimed=%v err=%v", claimed, err)
	}
	claimed, err = repo.ClaimForRun(ctx, call.ID, now)
	if err != nil || claimed {
		t.Fatalf("duplicate ClaimForRun claimed=%v err=%v", claimed, err)
	}
	if err := repo.MarkCompleted(ctx, call.ID, "tool result", false, now); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
	recovered, err := repo.ListRecoverable(ctx, now, 10)
	if err != nil {
		t.Fatalf("ListRecoverable: %v", err)
	}
	if len(recovered) != 1 || recovered[0].Status != models.OpenAIAsyncToolCallCompleted || recovered[0].Result != "tool result" {
		t.Fatalf("recoverable = %+v", recovered)
	}
	delivered, err := repo.MarkDelivered(ctx, call.ID, now)
	if err != nil || !delivered {
		t.Fatalf("MarkDelivered delivered=%v err=%v", delivered, err)
	}
	delivered, err = repo.MarkDelivered(ctx, call.ID, now)
	if err != nil || delivered {
		t.Fatalf("duplicate MarkDelivered delivered=%v err=%v", delivered, err)
	}
	persisted, err := repo.GetByExecutionCallID(ctx, execID, "call_1")
	if err != nil {
		t.Fatalf("GetByExecutionCallID: %v", err)
	}
	if persisted.Status != models.OpenAIAsyncToolCallDelivered || persisted.DeliverAttempts != 1 || persisted.DeliveredAt == nil {
		t.Fatalf("unexpected delivered state: %+v", persisted)
	}
}

func TestOpenAIAsyncToolCallRepoFailureExpiryCancellationAndStale(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "async-project-2"
	taskID := "async-task-2"
	execID := "async-exec-2"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects(id, name, repo_path) VALUES (?, 'Async Project 2', '')`, projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks(id, project_id, title, status, category, prompt) VALUES (?, ?, 'Async Task 2', 'running', 'active', 'prompt')`, taskID, projectID); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO executions(id, task_id, status, prompt_sent) VALUES (?, ?, 'running', 'prompt')`, execID, taskID); err != nil {
		t.Fatalf("insert execution: %v", err)
	}

	repo := NewOpenAIAsyncToolCallRepo(db)
	now := time.Now().UTC()
	rejected := createAsyncToolCallFixture(t, repo, projectID, taskID, execID, "call_rejected", now.Add(time.Hour))
	if ok, err := repo.ClaimForRun(ctx, rejected.ID, now); err != nil || !ok {
		t.Fatalf("claim rejected fixture: %v %v", ok, err)
	}
	if err := repo.MarkCompleted(ctx, rejected.ID, "result", false, now); err != nil {
		t.Fatalf("complete rejected fixture: %v", err)
	}
	if err := repo.MarkProviderRejected(ctx, rejected.ID, "bad call_id", now); err != nil {
		t.Fatalf("MarkProviderRejected: %v", err)
	}
	got, _ := repo.GetByExecutionCallID(ctx, execID, "call_rejected")
	if got.Status != models.OpenAIAsyncToolCallProviderRejected || got.DeliverAttempts != 1 {
		t.Fatalf("provider rejected state = %+v", got)
	}

	stale := createAsyncToolCallFixture(t, repo, projectID, taskID, execID, "call_stale", now.Add(time.Hour))
	if err := repo.MarkStale(ctx, stale.ID, "response moved on", now); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	got, _ = repo.GetByExecutionCallID(ctx, execID, "call_stale")
	if got.Status != models.OpenAIAsyncToolCallStale {
		t.Fatalf("stale state = %+v", got)
	}

	expired := createAsyncToolCallFixture(t, repo, projectID, taskID, execID, "call_expired", now.Add(-time.Minute))
	_ = expired
	n, err := repo.ExpireDue(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("ExpireDue n=%d err=%v", n, err)
	}
	got, _ = repo.GetByExecutionCallID(ctx, execID, "call_expired")
	if got.Status != models.OpenAIAsyncToolCallExpired {
		t.Fatalf("expired state = %+v", got)
	}

	cancelled := createAsyncToolCallFixture(t, repo, projectID, taskID, execID, "call_cancelled", now.Add(time.Hour))
	_ = cancelled
	n, err = repo.CancelByExecution(ctx, execID, "execution cancelled", now)
	if err != nil || n != 1 {
		t.Fatalf("CancelByExecution n=%d err=%v", n, err)
	}
	got, _ = repo.GetByExecutionCallID(ctx, execID, "call_cancelled")
	if got.Status != models.OpenAIAsyncToolCallCancelled {
		t.Fatalf("cancelled state = %+v", got)
	}
}
