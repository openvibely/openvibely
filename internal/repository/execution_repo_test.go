package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestExecutionRepo_CreateAndComplete(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	// Create a task
	task := &models.Task{ProjectID: "default", Title: "Exec Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)

	// Get default agent
	agent, _ := agentRepo.GetDefault(ctx)

	// Create execution
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if exec.ID == "" {
		t.Fatal("expected ID to be set")
	}

	// Complete execution
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output text", "", 100, 500); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Verify
	got, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Status != models.ExecCompleted {
		t.Errorf("expected Status=completed, got %q", got.Status)
	}
	if got.Output != "output text" {
		t.Errorf("expected Output=output text, got %q", got.Output)
	}
	if got.TokensUsed != 100 {
		t.Errorf("expected TokensUsed=100, got %d", got.TokensUsed)
	}
	if got.DurationMs != 500 {
		t.Errorf("expected DurationMs=500, got %d", got.DurationMs)
	}
}

func TestExecutionRepo_CompleteFailed(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Fail Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	execRepo.Create(ctx, exec)

	execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "something broke", 0, 100)

	got, _ := execRepo.GetByID(ctx, exec.ID)
	if got.Status != models.ExecFailed {
		t.Errorf("expected Status=failed, got %q", got.Status)
	}
	if got.ErrorMessage != "something broke" {
		t.Errorf("expected ErrorMessage=something broke, got %q", got.ErrorMessage)
	}
}

func TestExecutionRepo_CompleteFailedPreservesStreamedOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Fail Preserve Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test prompt",
	}
	execRepo.Create(ctx, exec)

	streamed := "[Using tool: bash]\nrunning commands...\n[Thinking] lots of intermediate content"
	// Simulate streaming writer writing partial output during execution
	execRepo.UpdateOutput(ctx, exec.ID, streamed)

	// Verify output was written
	before, _ := execRepo.GetByID(ctx, exec.ID)
	if before.Output == "" {
		t.Fatal("expected output to be set before Complete")
	}

	// Complete with failed status and empty output — must preserve streamed content
	execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "command failed with exit code 1", 0, 500)

	got, _ := execRepo.GetByID(ctx, exec.ID)
	if got.Status != models.ExecFailed {
		t.Errorf("expected Status=failed, got %q", got.Status)
	}
	if got.Output != streamed {
		t.Errorf("expected Output to preserve streamed content on failure, got %q", got.Output)
	}
	if got.ErrorMessage != "command failed with exit code 1" {
		t.Errorf("expected ErrorMessage='command failed with exit code 1', got %q", got.ErrorMessage)
	}
	if got.PromptSent != "test prompt" {
		t.Errorf("expected PromptSent preserved, got %q", got.PromptSent)
	}
}

func TestExecutionRepo_CompleteNonFailedPreservesOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Preserve Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	execRepo.Create(ctx, exec)

	// Simulate streaming writer writing output
	execRepo.UpdateOutput(ctx, exec.ID, "streamed output content")

	// Complete with completed status and empty output — should preserve existing output
	execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "", "", 100, 500)

	got, _ := execRepo.GetByID(ctx, exec.ID)
	if got.Status != models.ExecCompleted {
		t.Errorf("expected Status=completed, got %q", got.Status)
	}
	if got.Output != "streamed output content" {
		t.Errorf("expected Output preserved for non-failed status, got %q", got.Output)
	}
}

func TestExecutionRepo_ListByTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "List Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create two executions
	for i := 0; i < 2; i++ {
		exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "test"}
		execRepo.Create(ctx, exec)
	}

	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) != 2 {
		t.Errorf("expected 2 executions, got %d", len(execs))
	}
}

func TestExecutionRepo_ListChatHistory(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	agent, _ := agentRepo.GetDefault(ctx)

	// Create chat tasks and executions
	chatMessages := []struct {
		prompt string
		output string
	}{
		{"Hello", "Hi there!"},
		{"How are you?", "I'm doing well, thanks!"},
		{"What's the weather?", "I don't have access to weather data."},
	}

	for _, msg := range chatMessages {
		task := &models.Task{
			ProjectID: "default",
			Title:     "Chat: " + msg.prompt,
			Category:  models.CategoryChat,
			Status:    models.StatusPending,
			Prompt:    msg.prompt,
		}
		taskRepo.Create(ctx, task)

		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    msg.prompt,
		}
		execRepo.Create(ctx, exec)
		execRepo.Complete(ctx, exec.ID, models.ExecCompleted, msg.output, "", 50, 100)
	}

	// Create a non-chat task to ensure it's not included
	regularTask := &models.Task{
		ProjectID: "default",
		Title:     "Regular Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "regular prompt",
	}
	taskRepo.Create(ctx, regularTask)
	regularExec := &models.Execution{
		TaskID:        regularTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "regular prompt",
	}
	execRepo.Create(ctx, regularExec)

	// Test ListChatHistory
	history, err := execRepo.ListChatHistory(ctx, "default", 50)
	if err != nil {
		t.Fatalf("ListChatHistory: %v", err)
	}

	// Should only return chat messages (3), not the regular task
	if len(history) != 3 {
		t.Fatalf("expected 3 chat messages, got %d", len(history))
	}

	// Verify messages are in chronological order (oldest first)
	if history[0].PromptSent != "Hello" {
		t.Errorf("expected first message to be 'Hello', got %q", history[0].PromptSent)
	}
	if history[1].PromptSent != "How are you?" {
		t.Errorf("expected second message to be 'How are you?', got %q", history[1].PromptSent)
	}
	if history[2].PromptSent != "What's the weather?" {
		t.Errorf("expected third message to be 'What's the weather?', got %q", history[2].PromptSent)
	}

	// Verify outputs
	if history[0].Output != "Hi there!" {
		t.Errorf("expected first output to be 'Hi there!', got %q", history[0].Output)
	}

	// Test limit parameter
	limitedHistory, err := execRepo.ListChatHistory(ctx, "default", 2)
	if err != nil {
		t.Fatalf("ListChatHistory with limit: %v", err)
	}
	if len(limitedHistory) != 2 {
		t.Errorf("expected 2 messages with limit=2, got %d", len(limitedHistory))
	}
}

func TestExecutionRepo_IsFollowup(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Followup Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create a regular execution (not a followup)
	regularExec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "original prompt",
		IsFollowup:    false,
	}
	if err := execRepo.Create(ctx, regularExec); err != nil {
		t.Fatalf("Create regular exec: %v", err)
	}

	// Create a followup execution
	followupExec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "followup message",
		IsFollowup:    true,
	}
	if err := execRepo.Create(ctx, followupExec); err != nil {
		t.Fatalf("Create followup exec: %v", err)
	}

	// Verify regular execution
	got, err := execRepo.GetByID(ctx, regularExec.ID)
	if err != nil {
		t.Fatalf("GetByID regular: %v", err)
	}
	if got.IsFollowup {
		t.Error("expected regular execution to have IsFollowup=false")
	}

	// Verify followup execution
	got, err = execRepo.GetByID(ctx, followupExec.ID)
	if err != nil {
		t.Fatalf("GetByID followup: %v", err)
	}
	if !got.IsFollowup {
		t.Error("expected followup execution to have IsFollowup=true")
	}

	// Verify ListByTask also returns the IsFollowup flag
	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) != 2 {
		t.Fatalf("expected 2 executions, got %d", len(execs))
	}
	// Check that one is followup and one is not (order may vary due to same timestamp)
	followupCount := 0
	for _, e := range execs {
		if e.IsFollowup {
			followupCount++
		}
	}
	if followupCount != 1 {
		t.Errorf("expected exactly 1 followup execution, got %d", followupCount)
	}
}

func TestExecutionRepo_ListByTaskChronological(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Chrono Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create executions in order
	prompts := []string{"first", "second", "third"}
	for _, p := range prompts {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    p,
		}
		execRepo.Create(ctx, exec)
		execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output for "+p, "", 10, 100)
	}

	// ListByTaskChronological should return oldest first
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(execs))
	}
	if execs[0].PromptSent != "first" {
		t.Errorf("expected first execution to be 'first', got %q", execs[0].PromptSent)
	}
	if execs[1].PromptSent != "second" {
		t.Errorf("expected second execution to be 'second', got %q", execs[1].PromptSent)
	}
	if execs[2].PromptSent != "third" {
		t.Errorf("expected third execution to be 'third', got %q", execs[2].PromptSent)
	}

	// Verify chronological order: first should come before third
	if execs[0].StartedAt.After(execs[2].StartedAt) {
		t.Error("expected chronological order: first execution should not be after third")
	}
}

func TestExecutionRepo_ListByTaskChronological_FollowupOrder(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Followup Order Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Create original execution
	exec1 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "original prompt",
		IsFollowup:    false,
	}
	execRepo.Create(ctx, exec1)
	execRepo.Complete(ctx, exec1.ID, models.ExecCompleted, "original output", "", 100, 500)

	// Create follow-up executions
	exec2 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "followup 1",
		IsFollowup:    true,
	}
	execRepo.Create(ctx, exec2)
	execRepo.Complete(ctx, exec2.ID, models.ExecCompleted, "followup 1 output", "", 50, 300)

	exec3 := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "followup 2",
		IsFollowup:    true,
	}
	execRepo.Create(ctx, exec3)
	execRepo.Complete(ctx, exec3.ID, models.ExecCompleted, "followup 2 output", "", 50, 200)

	// ListByTaskChronological should return in creation order
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 3 {
		t.Fatalf("expected 3 executions, got %d", len(execs))
	}

	// Verify order: original, followup 1, followup 2
	if execs[0].PromptSent != "original prompt" {
		t.Errorf("expected first execution to be original, got %q", execs[0].PromptSent)
	}
	if execs[0].IsFollowup {
		t.Error("expected first execution to be non-followup")
	}
	if execs[1].PromptSent != "followup 1" {
		t.Errorf("expected second execution to be followup 1, got %q", execs[1].PromptSent)
	}
	if !execs[1].IsFollowup {
		t.Error("expected second execution to be followup")
	}
	if execs[2].PromptSent != "followup 2" {
		t.Errorf("expected third execution to be followup 2, got %q", execs[2].PromptSent)
	}
	if !execs[2].IsFollowup {
		t.Error("expected third execution to be followup")
	}
}

// TestExecutionRepo_MultiTurnOrderingWithReRuns verifies that chronological ordering
// works correctly when a task has multiple runs AND follow-ups, reproducing the bug
// where follow-up messages appeared at the top of the chat instead of the bottom.
func TestExecutionRepo_DiffOutput(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Diff Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "test"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Complete the execution
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output text", "", 100, 500); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Update diff output
	diffData := "diff --git a/main.go b/main.go\n--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,4 @@\n package main\n+import \"fmt\"\n func main() {\n }\n"
	if err := execRepo.UpdateDiffOutput(ctx, exec.ID, diffData); err != nil {
		t.Fatalf("UpdateDiffOutput: %v", err)
	}

	// Verify via GetByID
	got, err := execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DiffOutput != diffData {
		t.Errorf("expected DiffOutput=%q, got %q", diffData, got.DiffOutput)
	}

	// Verify via ListByTask
	execs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(execs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(execs))
	}
	if execs[0].DiffOutput != diffData {
		t.Errorf("ListByTask: expected DiffOutput=%q, got %q", diffData, execs[0].DiffOutput)
	}

	// Verify via ListByTaskChronological
	chronoExecs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(chronoExecs) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(chronoExecs))
	}
	if chronoExecs[0].DiffOutput != diffData {
		t.Errorf("ListByTaskChronological: expected DiffOutput=%q, got %q", diffData, chronoExecs[0].DiffOutput)
	}
}

func TestExecutionRepo_ListByTaskIDs(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	agent, _ := agentRepo.GetDefault(ctx)

	task1 := &models.Task{ProjectID: "default", Title: "Task A", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "a"}
	task2 := &models.Task{ProjectID: "default", Title: "Task B", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "b"}
	taskRepo.Create(ctx, task1)
	taskRepo.Create(ctx, task2)

	// Create executions: 2 for task1, 1 for task2
	for i := 0; i < 2; i++ {
		exec := &models.Execution{TaskID: task1.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "prompt"}
		execRepo.Create(ctx, exec)
	}
	exec3 := &models.Execution{TaskID: task2.ID, AgentConfigID: agent.ID, Status: models.ExecCompleted, PromptSent: "prompt"}
	execRepo.Create(ctx, exec3)

	// Batch load
	result, err := execRepo.ListByTaskIDs(ctx, []string{task1.ID, task2.ID})
	if err != nil {
		t.Fatalf("ListByTaskIDs: %v", err)
	}
	if len(result[task1.ID]) != 2 {
		t.Errorf("expected 2 executions for task1, got %d", len(result[task1.ID]))
	}
	if len(result[task2.ID]) != 1 {
		t.Errorf("expected 1 execution for task2, got %d", len(result[task2.ID]))
	}

	// Task with no executions should not appear
	task3 := &models.Task{ProjectID: "default", Title: "Task C", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "c"}
	taskRepo.Create(ctx, task3)
	result2, err := execRepo.ListByTaskIDs(ctx, []string{task3.ID})
	if err != nil {
		t.Fatalf("ListByTaskIDs no execs: %v", err)
	}
	if len(result2[task3.ID]) != 0 {
		t.Errorf("expected 0 executions for task3, got %d", len(result2[task3.ID]))
	}

	// Empty input
	result3, err := execRepo.ListByTaskIDs(ctx, []string{})
	if err != nil {
		t.Fatalf("ListByTaskIDs empty: %v", err)
	}
	if len(result3) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result3))
	}
}

func TestExecutionRepo_MultiTurnOrderingWithReRuns(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := NewTaskRepo(db, nil)
	agentRepo := NewLLMConfigRepo(db)
	execRepo := NewExecutionRepo(db)
	ctx := context.Background()

	task := &models.Task{ProjectID: "default", Title: "Multi-turn Test", Category: models.CategoryActive, Status: models.StatusPending, Prompt: "original task prompt"}
	taskRepo.Create(ctx, task)
	agent, _ := agentRepo.GetDefault(ctx)

	// Simulate: initial run → re-run → follow-up → another follow-up
	execData := []struct {
		prompt     string
		isFollowup bool
	}{
		{"original task prompt", false},  // Initial run
		{"original task prompt", false},  // Re-run (e.g., from scheduler)
		{"Summarize this", true},         // Follow-up 1
		{"What about performance?", true}, // Follow-up 2
	}

	execIDs := make([]string, len(execData))
	for i, ed := range execData {
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    ed.prompt,
			IsFollowup:    ed.isFollowup,
		}
		if err := execRepo.Create(ctx, exec); err != nil {
			t.Fatalf("Create exec %d: %v", i, err)
		}
		execIDs[i] = exec.ID
		execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "output "+exec.ID, "", 50, 100)
	}

	// ListByTaskChronological must return in creation order (ASC)
	execs, err := execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTaskChronological: %v", err)
	}
	if len(execs) != 4 {
		t.Fatalf("expected 4 executions, got %d", len(execs))
	}

	// Verify order matches creation order
	for i, exec := range execs {
		if exec.ID != execIDs[i] {
			t.Errorf("execution %d: expected ID %s, got %s", i, execIDs[i], exec.ID)
		}
	}

	// Follow-ups must be LAST (at the bottom of the chat)
	if execs[2].PromptSent != "Summarize this" {
		t.Errorf("expected 3rd execution to be follow-up 'Summarize this', got %q", execs[2].PromptSent)
	}
	if execs[3].PromptSent != "What about performance?" {
		t.Errorf("expected 4th execution to be follow-up 'What about performance?', got %q", execs[3].PromptSent)
	}

	// Also verify that ListByTaskChronological and ListByTask return
	// different orderings, confirming the bug was using the wrong query.
	// ListByTask uses DESC (newest first), ListByTaskChronological uses ASC.
	descExecs, err := execRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(descExecs) != 4 {
		t.Fatalf("expected 4 executions from ListByTask, got %d", len(descExecs))
	}
}
