package stream

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestStreamingWriterReportsActivity(t *testing.T) {
	activity := 0
	ctx := llmcontracts.WithActivityCallback(context.Background(), func() { activity++ })
	sw := newWriterWithOutputRepo("", "", nil, ctx, time.Hour, nil)
	defer sw.Stop()

	if _, err := sw.Write([]byte("progress")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if activity != 1 {
		t.Fatalf("activity callback calls = %d, want 1", activity)
	}
	if _, err := sw.Write(nil); err != nil {
		t.Fatalf("empty write failed: %v", err)
	}
	if activity != 1 {
		t.Fatalf("empty write reported activity; calls = %d, want 1", activity)
	}
}

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

type fakeExecutionOutputRepo struct {
	mu      sync.Mutex
	output  string
	writes  []string
	updated chan string
}

func (r *fakeExecutionOutputRepo) GetByID(ctx context.Context, id string) (*models.Execution, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &models.Execution{ID: id, Output: r.output}, nil
}

func (r *fakeExecutionOutputRepo) UpdateOutput(ctx context.Context, id string, output string) error {
	r.mu.Lock()
	r.output = output
	r.writes = append(r.writes, output)
	r.mu.Unlock()
	if r.updated != nil {
		r.updated <- output
	}
	return nil
}

func (r *fakeExecutionOutputRepo) snapshot() (string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	writes := append([]string(nil), r.writes...)
	return r.output, writes
}

func TestStreamingWriter_PeriodicCannotOverwriteNewerFinalFlush(t *testing.T) {
	repo := &fakeExecutionOutputRepo{updated: make(chan string, 2)}
	sw := newWriterWithOutputRepo("exec-1", "task-1", repo, context.Background(), time.Hour, nil)
	defer sw.Stop()

	periodicSnapshot := make(chan string, 1)
	releasePeriodicWrite := make(chan struct{})
	sw.afterPeriodicSnapshot = func(output string) {
		periodicSnapshot <- output
		<-releasePeriodicWrite
	}

	if _, err := sw.Write([]byte("hello")); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	periodicDone := make(chan struct{})
	go func() {
		sw.flushPeriodicOnce()
		close(periodicDone)
	}()

	select {
	case got := <-periodicSnapshot:
		if got != "hello" {
			t.Fatalf("expected periodic to snapshot %q before final output, got %q", "hello", got)
		}
	case <-time.After(time.Second):
		t.Fatal("periodic flush did not take initial snapshot")
	}

	if _, err := sw.Write([]byte(" world")); err != nil {
		t.Fatalf("second write failed: %v", err)
	}

	finalDone := make(chan struct{})
	go func() {
		sw.Flush()
		close(finalDone)
	}()

	close(releasePeriodicWrite)

	select {
	case <-periodicDone:
	case <-time.After(time.Second):
		t.Fatal("periodic flush did not complete")
	}
	select {
	case <-finalDone:
	case <-time.After(time.Second):
		t.Fatal("final flush did not complete")
	}

	output, writes := repo.snapshot()
	if output != "hello world" {
		t.Fatalf("expected persisted output to remain %q, got %q (writes=%q)", "hello world", output, writes)
	}
	if len(writes) != 2 || writes[0] != "hello" || writes[1] != "hello world" {
		t.Fatalf("expected stale periodic write to be followed by final full write, got writes=%q", writes)
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

type recordingExecutionStreamPublisher struct {
	events []events.ExecutionStreamEvent
}

func (p *recordingExecutionStreamPublisher) Publish(event events.ExecutionStreamEvent) {
	p.events = append(p.events, event)
}

func TestStreamingWriter_PublishesDeltaImmediatelyWithOffset(t *testing.T) {
	publisher := &recordingExecutionStreamPublisher{}
	sw := NewWriterWithPublisher("exec-1", "task-1", nil, context.Background(), time.Hour, publisher)
	defer sw.Stop()

	if _, err := sw.Write([]byte("hi ")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if _, err := sw.Write([]byte("世界")); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	if len(publisher.events) != 2 {
		t.Fatalf("expected 2 published events, got %d", len(publisher.events))
	}
	if got := publisher.events[0]; got.ExecID != "exec-1" || got.Type != events.ExecutionStreamDelta || got.Delta != "hi " || got.Offset != len("hi ") {
		t.Fatalf("unexpected first event: %+v", got)
	}
	if got := publisher.events[1]; got.Delta != "世界" || got.Offset != len("hi 世界") {
		t.Fatalf("unexpected second event: %+v", got)
	}
}

func TestStreamingWriter_WriteDoesNotFlushSynchronouslyWhenIntervalElapsed(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "No Eager Flush", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "test"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	publisher := &recordingExecutionStreamPublisher{}
	sw := NewWriterWithPublisher(exec.ID, task.ID, execRepo, ctx, time.Hour, publisher)
	defer sw.Stop()
	sw.lastFlush = time.Now().Add(-time.Hour)

	if _, err := sw.Write([]byte("instant")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Delta != "instant" {
		t.Fatalf("expected immediate publish without waiting for persistence, got %+v", publisher.events)
	}

	updatedExec, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if updatedExec.Output != "" {
		t.Fatalf("Write should not synchronously flush to DB, got output %q", updatedExec.Output)
	}
}

func TestStreamingWriter_DoesNotPublishSeedOrWriteText(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Seed Publish", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatalf("failed to get default agent: %v", err)
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "test"}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}
	if err := execRepo.UpdateOutput(ctx, exec.ID, "seed"); err != nil {
		t.Fatalf("failed to seed execution: %v", err)
	}

	publisher := &recordingExecutionStreamPublisher{}
	sw := NewWriterWithPublisher(exec.ID, task.ID, execRepo, ctx, time.Hour, publisher)
	defer sw.Stop()
	if len(publisher.events) != 0 {
		t.Fatalf("seeded content should not publish, got %+v", publisher.events)
	}
	sw.WriteText([]byte("text only"))
	if len(publisher.events) != 0 {
		t.Fatalf("WriteText should not publish, got %+v", publisher.events)
	}
	if _, err := sw.Write([]byte(" plus")); err != nil {
		t.Fatalf("write failed: %v", err)
	}
	if len(publisher.events) != 1 || publisher.events[0].Offset != len("seed plus") {
		t.Fatalf("expected one appended event with cumulative offset, got %+v", publisher.events)
	}
}
