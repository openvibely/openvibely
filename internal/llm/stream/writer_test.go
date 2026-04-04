package stream

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestStreamingWriter_PeriodicFlush(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Periodic Flush",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
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
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 50*time.Millisecond)
	defer sw.Stop()

	sw.Write([]byte("hello world"))

	time.Sleep(200 * time.Millisecond)

	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get updated execution: %v", err)
	}
	if updatedExec.Output != "hello world" {
		t.Errorf("expected DB output %q after periodic flush, got %q", "hello world", updatedExec.Output)
	}
}

func TestStreamingWriter_StopPreventsLeak(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Stop Cleanup",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
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
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 10*time.Millisecond)

	sw.Write([]byte("test"))
	sw.Stop()

	time.Sleep(50 * time.Millisecond)
}

func TestStreamingWriter_FlushSucceedsAfterContextCancel(t *testing.T) {

	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)

	bgCtx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Canceled Context Flush",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(bgCtx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent, err := llmConfigRepo.GetDefault(bgCtx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	if err := execRepo.Create(bgCtx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	ctx, cancel := context.WithCancel(bgCtx)
	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	sw.Write([]byte("important output"))

	cancel()

	sw.Flush()

	updatedExec, err := execRepo.GetByID(bgCtx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get updated execution: %v", err)
	}
	if updatedExec.Output != "important output" {
		t.Errorf("expected DB output %q after flush with canceled context, got %q", "important output", updatedExec.Output)
	}
}

func TestStreamingWriter_NewWriterSeedsExistingOutputOnRetry(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Retry Seed",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
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
		PromptSent:    "retry prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	if err := execRepo.UpdateOutput(ctx, exec.ID, "existing streamed output\n"); err != nil {
		t.Fatalf("failed to seed existing output: %v", err)
	}

	sw := NewWriter(exec.ID, task.ID, execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	if got := sw.String(); got != "existing streamed output\n" {
		t.Fatalf("expected writer to seed existing output, got %q", got)
	}

	sw.Write([]byte("retry appended output"))
	sw.Flush()

	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get updated execution: %v", err)
	}
	want := "existing streamed output\nretry appended output"
	if updatedExec.Output != want {
		t.Fatalf("expected merged output %q, got %q", want, updatedExec.Output)
	}
}

func TestStreamingWriter_EmptyRetryFlushDoesNotOverwriteExistingOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Retry Flush",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
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
		PromptSent:    "retry prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	if err := execRepo.UpdateOutput(ctx, exec.ID, "tool output before 429"); err != nil {
		t.Fatalf("failed to seed execution output: %v", err)
	}

	// Simulate retry attempt creating a new writer for the same exec ID but
	// failing before any chunks arrive (empty in-memory buffer). Flush must not
	// wipe persisted output from the prior attempt.
	retry := NewWriter(exec.ID, task.ID, execRepo, ctx, 500*time.Millisecond)
	retry.buf.Reset()
	retry.Flush()
	retry.Stop()

	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get updated execution: %v", err)
	}
	if updatedExec.Output != "tool output before 429" {
		t.Fatalf("expected prior streamed history to survive empty retry flush, got %q", updatedExec.Output)
	}
}
