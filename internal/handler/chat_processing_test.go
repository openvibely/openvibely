package handler

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createHandlerTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %v\n%s", args, err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "initial commit")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("initial commit failed: %v\n%s", err, out)
	}

	return dir
}

func TestBuildThreadSystemContext_WithHistory_DoesNotIncludeTaskPrompt(t *testing.T) {
	// When there is prior conversation history, the system context should NOT
	// include the original task prompt because it's already in the conversation
	// history as the first user message. Re-injecting it causes the model to
	// restart work from scratch.
	result := buildThreadSystemContext("Fix login bug", true, "")

	if strings.Contains(result, "Original prompt") {
		t.Error("system context with history should NOT contain 'Original prompt' — it causes model to restart work")
	}
	if strings.Contains(result, "task prompt was") {
		t.Error("system context with history should NOT re-inject the task prompt")
	}
	if !strings.Contains(result, "continue from where you left off") && !strings.Contains(result, "Continue from where you left off") {
		t.Error("system context with history should instruct model to continue, not restart")
	}
	if !strings.Contains(result, "do NOT restart") && !strings.Contains(result, "do not restart") {
		t.Error("system context with history should explicitly say not to restart")
	}
	if !strings.Contains(result, "Fix login bug") {
		t.Error("system context should include the task title for reference")
	}
}

func TestBuildThreadSystemContext_WithoutHistory_NoTaskPrompt(t *testing.T) {
	// When there is no history (first follow-up), the system context should
	// indicate the task prompt follows as the user message.
	result := buildThreadSystemContext("Fix login bug", false, "")

	if strings.Contains(result, "Fix login bug") {
		t.Error("system context without history should not include title (task prompt is the user message)")
	}
	if !strings.Contains(result, "user's message below") {
		t.Error("system context without history should reference the user's message")
	}
}

func TestBuildThreadSystemContext_WithAttachments(t *testing.T) {
	result := buildThreadSystemContext("Fix login bug", true, "Attached file: screenshot.png")

	if !strings.Contains(result, "screenshot.png") {
		t.Error("system context should include attachment context when provided")
	}
}

func TestBuildThreadSystemContext_NoAttachments(t *testing.T) {
	result := buildThreadSystemContext("Fix login bug", true, "")

	// Should not have double newlines from empty attachment context
	if strings.Contains(result, "\n\n\n") {
		t.Error("system context should not have triple newlines when no attachment context")
	}
}

func TestFilterChatHistory_ExcludesRunningAndCurrentExec(t *testing.T) {
	// filterChatHistory should exclude the current execution and running ones,
	// preserving only completed/failed executions for conversation context.
	executions := []models.Execution{
		{ID: "exec1", Status: models.ExecCompleted, PromptSent: "original prompt", Output: "response 1"},
		{ID: "exec2", Status: models.ExecFailed, PromptSent: "follow-up 1", Output: "error msg"},
		{ID: "exec3", Status: models.ExecRunning, PromptSent: "running exec"},
		{ID: "exec4", Status: models.ExecCompleted, PromptSent: "current exec"},
	}

	result := filterChatHistory(executions, "exec4")

	if len(result) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(result))
	}
	if result[0].ID != "exec1" {
		t.Errorf("expected first entry to be exec1, got %s", result[0].ID)
	}
	if result[1].ID != "exec2" {
		t.Errorf("expected second entry to be exec2, got %s", result[1].ID)
	}
}

func TestFilterChatHistory_ReturnsNonNilForEmpty(t *testing.T) {
	// filterChatHistory must return a non-nil slice even when empty,
	// so CallAgentDirectStreaming routes to the chat path.
	result := filterChatHistory([]models.Execution{}, "any-id")

	if result == nil {
		t.Error("filterChatHistory should return non-nil empty slice, not nil")
	}
}

func TestCombineContexts_BothPresent(t *testing.T) {
	result := combineContexts("task context here", "attachment context here")
	if result != "task context here\nattachment context here" {
		t.Errorf("expected combined contexts joined with newline, got %q", result)
	}
}

func TestCombineContexts_OnlyTaskContext(t *testing.T) {
	result := combineContexts("task context only", "")
	if result != "task context only" {
		t.Errorf("expected just task context, got %q", result)
	}
}

func TestCombineContexts_OnlyAttachmentContext(t *testing.T) {
	result := combineContexts("", "attachment context only")
	if result != "attachment context only" {
		t.Errorf("expected just attachment context, got %q", result)
	}
}

func TestCombineContexts_BothEmpty(t *testing.T) {
	result := combineContexts("", "")
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestResolveTaskAgentDefinitionForTask_LoadsAssignedDefinition(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	project := createProject(t, h, "agent-def-project")
	agentDef := &models.Agent{
		Name:         "ui-reviewer",
		Description:  "review ui with playwright",
		SystemPrompt: "Use MCP tools.",
		Model:        "inherit",
		Tools:        []string{"Read", "Bash"},
		Plugins:      []string{"playwright@claude-plugins-official"},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("create agent definition: %v", err)
	}
	task := createTask(t, h, project.ID, "thread task", func(tk *models.Task) {
		tk.AgentDefinitionID = &agentDef.ID
	})

	resolved := h.resolveTaskAgentDefinitionForTask(ctx, task.ID, nil)
	if resolved == nil {
		t.Fatalf("expected resolved agent definition")
	}
	if resolved.ID != agentDef.ID {
		t.Fatalf("expected agent definition id %q, got %q", agentDef.ID, resolved.ID)
	}
}

func TestBuildThreadSystemContext_AttachmentIntegration(t *testing.T) {
	// When attachments are provided, the system context should include them
	// and they should be passed to the LLM as part of the system prompt.
	result := buildThreadSystemContext("Fix CSS bug", true, "\n\n--- Attached Files ---\nFile: screenshot.png")

	if !strings.Contains(result, "screenshot.png") {
		t.Error("system context should include attachment file reference")
	}
	if !strings.Contains(result, "Attached Files") {
		t.Error("system context should include attachment section header")
	}
	if !strings.Contains(result, "continue from where you left off") {
		t.Error("system context with history should still instruct continuation")
	}
}

func TestBuildThreadSystemContext_FollowupDoesNotReInjectTaskPrompt(t *testing.T) {
	// The system context must NOT contain the actual task prompt text when
	// there is history. The task prompt is already in history as the first
	// user message. Re-injecting it causes the model to see it twice and
	// restart from scratch.
	taskTitle := "Implement user authentication"
	result := buildThreadSystemContext(taskTitle, true, "")

	// Should mention the task title for reference
	if !strings.Contains(result, taskTitle) {
		t.Error("system context should include task title for reference")
	}

	// Should explicitly say to continue, not restart
	if !strings.Contains(result, "continue from where you left off") {
		t.Error("system context should instruct to continue from where left off")
	}

	// Should NOT contain any phrase suggesting the prompt is being provided anew
	if strings.Contains(result, "task prompt is provided") {
		t.Error("system context with history should NOT say 'task prompt is provided' — that's for the no-history case")
	}
}

// TestFilterChatHistory_MultiTurnPreservesOrder verifies that filterChatHistory
// maintains chronological order for multi-turn conversations and excludes the
// current and running executions.
func TestFilterChatHistory_MultiTurnPreservesOrder(t *testing.T) {
	executions := []models.Execution{
		{ID: "exec1", Status: models.ExecCompleted, PromptSent: "original prompt", IsFollowup: false},
		{ID: "exec2", Status: models.ExecCompleted, PromptSent: "first followup", IsFollowup: true},
		{ID: "exec3", Status: models.ExecCompleted, PromptSent: "second followup", IsFollowup: true},
		{ID: "exec4", Status: models.ExecRunning, PromptSent: "current followup", IsFollowup: true},
	}

	// Current exec is exec4
	result := filterChatHistory(executions, "exec4")

	if len(result) != 3 {
		t.Fatalf("expected 3 history entries (excluding current running), got %d", len(result))
	}

	// Verify chronological order is preserved
	expectedPrompts := []string{"original prompt", "first followup", "second followup"}
	for i, expected := range expectedPrompts {
		if result[i].PromptSent != expected {
			t.Errorf("entry %d: expected %q, got %q", i, expected, result[i].PromptSent)
		}
	}

	// Verify follow-up flags are preserved
	if result[0].IsFollowup {
		t.Error("first entry should be non-followup (original)")
	}
	if !result[1].IsFollowup {
		t.Error("second entry should be followup")
	}
}

// TestFilterChatHistory_ExcludesMultipleRunning verifies that all running
// executions are filtered out, not just the current one.
func TestFilterChatHistory_ExcludesMultipleRunning(t *testing.T) {
	executions := []models.Execution{
		{ID: "exec1", Status: models.ExecCompleted, PromptSent: "completed1"},
		{ID: "exec2", Status: models.ExecRunning, PromptSent: "orphaned running"},
		{ID: "exec3", Status: models.ExecCompleted, PromptSent: "completed2"},
		{ID: "exec4", Status: models.ExecRunning, PromptSent: "current"},
	}

	result := filterChatHistory(executions, "exec4")

	if len(result) != 2 {
		t.Fatalf("expected 2 entries (only completed), got %d", len(result))
	}
	if result[0].ID != "exec1" || result[1].ID != "exec3" {
		t.Errorf("expected exec1 and exec3, got %s and %s", result[0].ID, result[1].ID)
	}
}

func TestProcessStreamingResponse_TaskFollowupFailedMarkerMarksTaskFailed(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "I couldn't finish this.\n[STATUS: FAILED | tests failed]"
	mock.TextOnly = mock.Response
	mock.Tokens = 42
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Followup Failure Project")
	task := createTask(t, h, project.ID, "Followup Failure Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Please continue"
		ex.IsFollowup = true
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "Please continue",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "continue from where you left off",
		WorkDir:        "",
		IsTaskFollowup: true,
	})

	updatedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecFailed {
		t.Fatalf("expected execution failed, got %s", updatedExec.Status)
	}
	if updatedExec.ErrorMessage != "tests failed" {
		t.Fatalf("expected error message %q, got %q", "tests failed", updatedExec.ErrorMessage)
	}
	if !strings.Contains(updatedExec.Output, "[STATUS: FAILED | tests failed]") {
		t.Fatalf("expected preserved failed output, got %q", updatedExec.Output)
	}

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusFailed {
		t.Fatalf("expected task failed, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Fatalf("expected task moved to backlog, got %s", updatedTask.Category)
	}
}

func TestProcessStreamingResponse_TaskFollowupWithOnlyPreexistingDiffCompletes(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "I inspected the codebase and I'm ready for the next step."
	mock.TextOnly = mock.Response
	mock.Tokens = 17
	h.llmSvc.SetLLMCaller(mock)

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Worktree Followup Project", RepoPath: repoDir}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := createAgent(t, llmConfigRepo)
	task := createTask(t, h, project.ID, "Worktree Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}

	if err := os.WriteFile(filepath.Join(wtPath, "followup.txt"), []byte("existing change\n"), 0644); err != nil {
		t.Fatalf("write preexisting change: %v", err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "existing change"); err != nil {
		t.Fatalf("commit preexisting change: %v", err)
	}
	if diff := service.GetWorktreeDiff(repoDir, wtBranch, service.GetDefaultBranch(repoDir)); strings.TrimSpace(diff) == "" {
		t.Fatal("expected preexisting worktree diff before followup")
	}

	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Keep going"
		ex.IsFollowup = true
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "Keep going",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "continue from where you left off",
		WorkDir:        wtPath,
		IsTaskFollowup: true,
	})

	updatedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecCompleted {
		t.Fatalf("expected execution completed, got %s", updatedExec.Status)
	}
	if updatedExec.ErrorMessage != "" {
		t.Fatalf("expected empty error message, got %q", updatedExec.ErrorMessage)
	}

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryCompleted {
		t.Fatalf("expected task moved to completed, got %s", updatedTask.Category)
	}
}

func TestCompleteWithSuccess_UpdatesTaskStatusBeforeDiffCapture(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID, Title: "Test Task",
		Category: models.CategoryActive, Priority: 2, Prompt: "Test",
		Status: models.StatusRunning,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "Test",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Call completeWithSuccess (no workDir so no git diff capture)
	h.completeWithSuccess(ctx, exec.ID, task.ID, "output text", "", "", 100, 5000)

	// Verify execution is completed
	completedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if completedExec.Status != models.ExecCompleted {
		t.Errorf("expected execution status %q, got %q", models.ExecCompleted, completedExec.Status)
	}

	// Verify task status is completed
	completedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if completedTask.Status != models.StatusCompleted {
		t.Errorf("expected task status %q, got %q", models.StatusCompleted, completedTask.Status)
	}

	// Verify category moved to completed
	if completedTask.Category != models.CategoryCompleted {
		t.Errorf("expected category %q, got %q", models.CategoryCompleted, completedTask.Category)
	}
}

func TestCompleteWithFailure_UpdatesTaskStatus(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID, Title: "Test Task",
		Category: models.CategoryActive, Priority: 2, Prompt: "Test",
		Status: models.StatusRunning,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "Test",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	h.completeWithFailure(ctx, exec.ID, task.ID, "something failed", 3000)

	// Verify execution is failed
	failedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if failedExec.Status != models.ExecFailed {
		t.Errorf("expected execution status %q, got %q", models.ExecFailed, failedExec.Status)
	}

	// Verify task status is failed
	failedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if failedTask.Status != models.StatusFailed {
		t.Errorf("expected task status %q, got %q", models.StatusFailed, failedTask.Status)
	}

	// Verify task moved to backlog (not stuck in active)
	if failedTask.Category != models.CategoryBacklog {
		t.Errorf("expected category %q, got %q", models.CategoryBacklog, failedTask.Category)
	}

	// Verify failure alert was created
	alerts, err := h.alertSvc.ListByProject(ctx, project.ID, 100)
	if err != nil {
		t.Fatalf("failed to list alerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != models.AlertTaskFailed {
		t.Errorf("expected alert type %q, got %q", models.AlertTaskFailed, alerts[0].Type)
	}
	if alerts[0].Severity != models.SeverityError {
		t.Errorf("expected alert severity %q, got %q", models.SeverityError, alerts[0].Severity)
	}
	if !strings.Contains(alerts[0].Message, "something failed") {
		t.Errorf("expected alert message to contain error, got %q", alerts[0].Message)
	}
}

func TestCompleteWithFailure_MovesCompletedTaskToBacklog(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Reproduces follow-up failure on a task already in the completed column.
	task := &models.Task{
		ProjectID: project.ID, Title: "Previously completed task",
		Category: models.CategoryCompleted, Priority: 2, Prompt: "Test",
		Status: models.StatusRunning,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "Test",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	h.completeWithFailure(ctx, exec.ID, task.ID, "follow-up failed", 1200)

	failedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if failedTask.Status != models.StatusFailed {
		t.Errorf("expected task status %q, got %q", models.StatusFailed, failedTask.Status)
	}
	if failedTask.Category != models.CategoryBacklog {
		t.Errorf("expected category %q, got %q", models.CategoryBacklog, failedTask.Category)
	}
}

func TestCompleteWithFailure_WorksWithExpiredContext(t *testing.T) {
	// This is the exact bug scenario: the 5-minute timeout fires, killing the
	// LLM call. The caller's context is expired, but completeWithFailure must
	// still update the DB using its own fresh context.
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	project := &models.Project{Name: "Test Project"}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID, Title: "Timeout Task",
		Category: models.CategoryActive, Priority: 2, Prompt: "Test",
		Status: models.StatusRunning,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	exec := &models.Execution{
		TaskID: task.ID, AgentConfigID: agent.ID,
		Status: models.ExecRunning, PromptSent: "Test",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("failed to create execution: %v", err)
	}

	// Simulate the bug: call completeWithFailure with an already-cancelled context
	expiredCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — this is what happens when the 5-min timeout fires

	h.completeWithFailure(expiredCtx, exec.ID, task.ID, "claude CLI error: signal: killed", 300000)

	// Verify everything still updated despite the expired context
	failedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("failed to get execution: %v", err)
	}
	if failedExec.Status != models.ExecFailed {
		t.Errorf("expected execution status %q, got %q — DB update failed with expired context", models.ExecFailed, failedExec.Status)
	}

	failedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to get task: %v", err)
	}
	if failedTask.Status != models.StatusFailed {
		t.Errorf("expected task status %q, got %q — task stuck in running", models.StatusFailed, failedTask.Status)
	}
	if failedTask.Category != models.CategoryBacklog {
		t.Errorf("expected category %q, got %q — task not moved to backlog", models.CategoryBacklog, failedTask.Category)
	}

	// Verify alert was created even with expired caller context
	alerts, err := h.alertSvc.ListByProject(ctx, project.ID, 100)
	if err != nil {
		t.Fatalf("failed to list alerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d — alert not created with expired context", len(alerts))
	}
	if !strings.Contains(alerts[0].Title, "Timeout Task") {
		t.Errorf("expected alert title to contain task name, got %q", alerts[0].Title)
	}
}

func TestSelectAgent_DefaultReturnsDefaultModel(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create two agents, mark the second as default
	agent1 := &models.LLMConfig{
		Name: "Haiku", Provider: models.ProviderTest, Model: "claude-3-haiku",
		APIKey: "key1", MaxTokens: 4096, Temperature: 1.0, IsDefault: false,
	}
	agent2 := &models.LLMConfig{
		Name: "Sonnet", Provider: models.ProviderTest, Model: "claude-3-5-sonnet",
		APIKey: "key2", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent1); err != nil {
		t.Fatalf("failed to create agent1: %v", err)
	}
	if err := llmConfigRepo.Create(ctx, agent2); err != nil {
		t.Fatalf("failed to create agent2: %v", err)
	}

	// selectAgent with "default" should return the default agent (agent2)
	selected, err := h.selectAgent(ctx, "default", "hello", false)
	if err != nil {
		t.Fatalf("selectAgent default failed: %v", err)
	}
	if selected.ID != agent2.ID {
		t.Errorf("expected default agent %s, got %s", agent2.ID, selected.ID)
	}
	if selected.Name != "Sonnet" {
		t.Errorf("expected agent name 'Sonnet', got %q", selected.Name)
	}
}

func TestSelectAgent_DefaultFallsBackWhenNoDefaultConfigured(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Delete any seeded default agents (migration 003 seeds "Claude Max" with is_default=1)
	seeded, _ := llmConfigRepo.GetDefault(ctx)
	if seeded != nil {
		_ = llmConfigRepo.Delete(ctx, seeded.ID)
	}

	// Create agent without IsDefault set
	agent := &models.LLMConfig{
		Name: "Haiku", Provider: models.ProviderTest, Model: "claude-3-haiku",
		APIKey: "key1", MaxTokens: 4096, Temperature: 1.0, IsDefault: false,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// selectAgent with "default" should fall back to first available
	selected, err := h.selectAgent(ctx, "default", "hello", false)
	if err != nil {
		t.Fatalf("selectAgent default fallback failed: %v", err)
	}
	if selected.ID != agent.ID {
		t.Errorf("expected fallback to first agent %s, got %s", agent.ID, selected.ID)
	}
}

func TestSelectAgent_AutoStillWorks(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Sonnet", Provider: models.ProviderTest, Model: "claude-3-5-sonnet",
		APIKey: "key1", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// "auto" should still use auto-selection (not default)
	selected, err := h.selectAgent(ctx, "auto", "hello", false)
	if err != nil {
		t.Fatalf("selectAgent auto failed: %v", err)
	}
	if selected == nil {
		t.Fatal("selectAgent auto returned nil")
	}
}

func TestSelectAgent_ExplicitIDStillWorks(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent1 := &models.LLMConfig{
		Name: "Haiku", Provider: models.ProviderTest, Model: "claude-3-haiku",
		APIKey: "key1", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	agent2 := &models.LLMConfig{
		Name: "Sonnet", Provider: models.ProviderTest, Model: "claude-3-5-sonnet",
		APIKey: "key2", MaxTokens: 4096, Temperature: 1.0, IsDefault: false,
	}
	if err := llmConfigRepo.Create(ctx, agent1); err != nil {
		t.Fatalf("failed to create agent1: %v", err)
	}
	if err := llmConfigRepo.Create(ctx, agent2); err != nil {
		t.Fatalf("failed to create agent2: %v", err)
	}

	// Explicit agent ID should bypass both auto and default
	selected, err := h.selectAgent(ctx, agent2.ID, "hello", false)
	if err != nil {
		t.Fatalf("selectAgent explicit failed: %v", err)
	}
	if selected.ID != agent2.ID {
		t.Errorf("expected agent2 %s, got %s", agent2.ID, selected.ID)
	}
}

// TestFollowupResetsMergeStatus verifies that when a follow-up creates new changes
// after a task has been merged, the merge_status is reset from "merged" to "pending"
// so the merge button re-appears in the UI.
func TestFollowupResetsMergeStatus(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a test git repository
	repoDir := createHandlerTestGitRepo(t)

	// Create project with repo
	project := &models.Project{Name: "Test Project", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create task with worktree
	task := createTask(t, h, project.ID, "Test Task with Worktree", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.AutoMerge = false
	})

	// Set up worktree service and create worktree
	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, _, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}

	// Create initial changes and commit them
	if err := os.WriteFile(filepath.Join(wtPath, "test.txt"), []byte("initial change\n"), 0644); err != nil {
		t.Fatalf("write initial change: %v", err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "initial change"); err != nil {
		t.Fatalf("commit initial change: %v", err)
	}

	// Simulate the task being merged
	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged); err != nil {
		t.Fatalf("update merge status to merged: %v", err)
	}

	// Verify merge status is "merged"
	task, err = h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status=merged before followup, got %s", task.MergeStatus)
	}

	// Create follow-up execution that will make new changes
	followupExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Make more changes"
		ex.IsFollowup = true
	})

	// Create new change in worktree before processing (simulating LLM making changes)
	if err := os.WriteFile(filepath.Join(wtPath, "followup.txt"), []byte("followup change\n"), 0644); err != nil {
		t.Fatalf("write followup change: %v", err)
	}

	// Mock LLM caller
	h.llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())

	// Process the follow-up
	h.processStreamingResponse(streamingResponseParams{
		ExecID:         followupExec.ID,
		TaskID:         task.ID,
		Message:        "Make more changes",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "continue work",
		WorkDir:        wtPath,
		IsTaskFollowup: true,
	})

	// Verify execution completed
	updatedExec, err := h.execRepo.GetByID(ctx, followupExec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecCompleted {
		t.Fatalf("expected execution completed, got %s (error: %s)", updatedExec.Status, updatedExec.ErrorMessage)
	}

	// Verify merge status was reset to "pending"
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.MergeStatus != models.MergeStatusPending {
		t.Errorf("expected merge_status=pending after followup with new changes, got %s", updatedTask.MergeStatus)
	}

	// Verify the diff was captured
	if updatedExec.DiffOutput == "" {
		t.Error("expected diff output to be captured for followup")
	}

	// Verify the changes are committed in the worktree
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = wtPath
	out, _ := statusCmd.Output()
	if len(strings.TrimSpace(string(out))) > 0 {
		t.Errorf("expected worktree to have no uncommitted changes after followup, got: %s", string(out))
	}
}

// TestFollowupNoChangesDoesNotResetMergeStatus verifies that when a follow-up
// does NOT create new changes, the merge_status stays as "merged".
func TestFollowupNoChangesDoesNotResetMergeStatus(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	// Create a test git repository
	repoDir := createHandlerTestGitRepo(t)

	// Create project with repo
	project := &models.Project{Name: "Test Project", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Create task with worktree
	task := createTask(t, h, project.ID, "Read-only Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.AutoMerge = false
	})

	// Set up worktree service and create worktree
	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, _, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}

	// Simulate the task being merged
	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusMerged); err != nil {
		t.Fatalf("update merge status to merged: %v", err)
	}

	// Verify merge status is "merged"
	task, err = h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status=merged before followup, got %s", task.MergeStatus)
	}

	// Create follow-up execution that will NOT make changes (read-only)
	followupExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "What's in this repository?"
		ex.IsFollowup = true
	})

	// Mock LLM caller (doesn't create any files)
	h.llmSvc.SetLLMCaller(testutil.NewMockLLMCaller())

	// Process the follow-up
	h.processStreamingResponse(streamingResponseParams{
		ExecID:         followupExec.ID,
		TaskID:         task.ID,
		Message:        "What's in this repository?",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "continue work",
		WorkDir:        wtPath,
		IsTaskFollowup: true,
	})

	// Verify execution completed
	updatedExec, err := h.execRepo.GetByID(ctx, followupExec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecCompleted {
		t.Fatalf("expected execution completed, got %s (error: %s)", updatedExec.Status, updatedExec.ErrorMessage)
	}

	// Verify merge status stayed as "merged" (not reset)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.MergeStatus != models.MergeStatusMerged {
		t.Errorf("expected merge_status=merged after followup without changes, got %s", updatedTask.MergeStatus)
	}

	// Verify no diff was captured (no changes)
	if updatedExec.DiffOutput != "" {
		t.Errorf("expected no diff output for read-only followup, got %d bytes", len(updatedExec.DiffOutput))
	}
}

func TestProcessStreamingResponse_TaskFollowupRateLimitFailurePreservesHistory(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Rate Limit Followup Project")
	task := createTask(t, h, project.ID, "Rate Limit Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
		tk.Prompt = "Original implementation prompt"
	})

	initial := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Original implementation prompt"
		ex.IsFollowup = false
	})
	if err := h.execRepo.Complete(ctx, initial.ID, models.ExecCompleted, "initial success output", "", 33, 120); err != nil {
		t.Fatalf("complete initial execution: %v", err)
	}

	failedFollowup := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Continue with follow-up"
		ex.IsFollowup = true
	})
	streamedPrefix := "Investigating prior changes..."
	if err := h.execRepo.UpdateOutput(ctx, failedFollowup.ID, streamedPrefix); err != nil {
		t.Fatalf("seed streamed output: %v", err)
	}

	mock := testutil.NewMockLLMCaller()
	mock.Err = fmt.Errorf("API error 429: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"This request would exceed your account's rate limit. Please try again later.\"}}")
	h.llmSvc.SetLLMCaller(mock)

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         failedFollowup.ID,
		TaskID:         task.ID,
		Message:        "Continue with follow-up",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "continue work",
		WorkDir:        "",
		IsTaskFollowup: true,
	})

	failedExec, err := h.execRepo.GetByID(ctx, failedFollowup.ID)
	if err != nil {
		t.Fatalf("get failed execution: %v", err)
	}
	if failedExec.Status != models.ExecFailed {
		t.Fatalf("expected failed execution status, got %s", failedExec.Status)
	}
	if !strings.Contains(failedExec.ErrorMessage, "429") || !strings.Contains(strings.ToLower(failedExec.ErrorMessage), "rate_limit_error") {
		t.Fatalf("expected rate-limit error message, got %q", failedExec.ErrorMessage)
	}
	if failedExec.Output != streamedPrefix {
		t.Fatalf("expected failed execution to preserve streamed output, got %q", failedExec.Output)
	}

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get updated task: %v", err)
	}
	if updatedTask.Status != models.StatusFailed {
		t.Fatalf("expected task status failed after 429, got %s", updatedTask.Status)
	}
	if updatedTask.Category != models.CategoryBacklog {
		t.Fatalf("expected task moved to backlog after failure, got %s", updatedTask.Category)
	}

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		t.Fatalf("list task executions: %v", err)
	}
	if len(execs) != 2 {
		t.Fatalf("expected 2 executions preserved after 429, got %d", len(execs))
	}
	if execs[0].Output != "initial success output" {
		t.Fatalf("expected initial execution output preserved, got %q", execs[0].Output)
	}
	if execs[1].Status != models.ExecFailed {
		t.Fatalf("expected second execution failed, got %s", execs[1].Status)
	}
}

func TestProcessStreamingResponse_TaskFollowupBroadcastsRealtimeDiffSnapshots(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	fileChangeBroadcaster := events.NewFileChangeBroadcaster()
	h.SetFileChangeBroadcaster(fileChangeBroadcaster)

	agent := createAgent(t, llmConfigRepo)
	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Followup Diff Project", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := createTask(t, h, project.ID, "Completed task followup", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusQueued
		tk.AgentID = &agent.ID
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, _, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}

	followupExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Apply follow-up"
		ex.IsFollowup = true
	})

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	h.llmSvc.SetLLMCaller(mock)

	sub, err := fileChangeBroadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe filechange broadcaster: %v", err)
	}
	defer fileChangeBroadcaster.Unsubscribe(sub)

	if err := os.WriteFile(filepath.Join(wtPath, "followup.txt"), []byte("followup change\n"), 0644); err != nil {
		t.Fatalf("write followup change: %v", err)
	}

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         followupExec.ID,
		TaskID:         task.ID,
		Message:        "Apply follow-up",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "continue work",
		WorkDir:        wtPath,
		IsTaskFollowup: true,
	})

	timeout := time.After(2 * time.Second)
	receivedDiffSnapshot := false
	for !receivedDiffSnapshot {
		select {
		case evt := <-sub:
			if evt.Type == events.DiffSnapshot && evt.TaskID == task.ID && evt.ExecID == followupExec.ID && strings.TrimSpace(evt.DiffOutput) != "" {
				receivedDiffSnapshot = true
			}
		case <-timeout:
			t.Fatal("expected diff_snapshot event for follow-up execution")
		}
	}

	updatedExec, err := h.execRepo.GetByID(ctx, followupExec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if strings.TrimSpace(updatedExec.DiffOutput) == "" {
		t.Fatal("expected follow-up execution diff_output to be persisted during realtime snapshot broadcast")
	}
}

func TestFormatThreadTranscript_FullContent(t *testing.T) {
	tc := NewTestContext(t)
	h := tc.handler

	task := &models.Task{
		ID:       "task-full",
		Title:    "Full Content Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Prompt:   "Build the API",
		Priority: 2,
	}

	executions := []models.Execution{
		{
			ID:         "exec1",
			PromptSent: "Build the API",
			Output:     "Created 3 endpoints for users, posts, and comments with full CRUD operations.",
			Status:     models.ExecCompleted,
			StartedAt:  time.Now().Add(-2 * time.Hour),
		},
		{
			ID:         "exec2",
			PromptSent: "Add authentication middleware",
			Output:     "Added JWT auth middleware with token validation and refresh logic.",
			Status:     models.ExecCompleted,
			IsFollowup: true,
			StartedAt:  time.Now().Add(-1 * time.Hour),
		},
	}

	transcript := h.formatThreadTranscript(task, executions, 0, 0)

	// All content should be present without truncation
	if !strings.Contains(transcript, "Created 3 endpoints for users, posts, and comments with full CRUD operations.") {
		t.Error("expected full first execution output, got truncated")
	}
	if !strings.Contains(transcript, "Added JWT auth middleware with token validation and refresh logic.") {
		t.Error("expected full second execution output, got truncated")
	}
	if !strings.Contains(transcript, "Total executions: 2") {
		t.Error("expected total executions count")
	}
	if strings.Contains(transcript, "truncated") {
		t.Error("short content should not be truncated")
	}
	if strings.Contains(transcript, "offset") {
		t.Error("short content should not show pagination")
	}
}

func TestFormatThreadTranscript_Pagination(t *testing.T) {
	tc := NewTestContext(t)
	h := tc.handler

	task := &models.Task{
		ID:       "task-page",
		Title:    "Paginated Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Prompt:   "Do work",
		Priority: 1,
	}

	// Create enough executions with large output to exceed budget
	var executions []models.Execution
	largeOutput := strings.Repeat("A", 20*1024) // 20KB each
	for i := 0; i < 10; i++ {
		executions = append(executions, models.Execution{
			ID:         "exec-" + strings.Repeat("x", i+1),
			PromptSent: "step " + strings.Repeat("x", i+1),
			Output:     largeOutput,
			Status:     models.ExecCompleted,
			IsFollowup: i > 0,
			StartedAt:  time.Now().Add(-time.Duration(10-i) * time.Hour),
		})
	}

	// First page (offset=0, no limit)
	page1 := h.formatThreadTranscript(task, executions, 0, 0)
	if !strings.Contains(page1, "Total executions: 10") {
		t.Error("expected total execution count of 10")
	}
	// Should hit budget and show pagination hint
	if !strings.Contains(page1, "Transcript size limit reached") {
		t.Error("expected size limit pagination hint for large thread")
	}
	if !strings.Contains(page1, "offset") {
		t.Error("expected offset hint in pagination message")
	}
}

func TestFormatThreadTranscript_OffsetAndLimit(t *testing.T) {
	tc := NewTestContext(t)
	h := tc.handler

	task := &models.Task{
		ID:       "task-ol",
		Title:    "Offset Limit Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Prompt:   "original prompt",
		Priority: 1,
	}

	executions := []models.Execution{
		{ID: "e0", PromptSent: "msg0", Output: "out0", Status: models.ExecCompleted, StartedAt: time.Now().Add(-3 * time.Hour)},
		{ID: "e1", PromptSent: "msg1", Output: "out1", Status: models.ExecCompleted, IsFollowup: true, StartedAt: time.Now().Add(-2 * time.Hour)},
		{ID: "e2", PromptSent: "msg2", Output: "out2", Status: models.ExecCompleted, IsFollowup: true, StartedAt: time.Now().Add(-1 * time.Hour)},
	}

	// With offset=1, limit=1: should show only exec1
	transcript := h.formatThreadTranscript(task, executions, 1, 1)
	if !strings.Contains(transcript, "msg1") {
		t.Error("expected exec at offset 1")
	}
	if !strings.Contains(transcript, "out1") {
		t.Error("expected output at offset 1")
	}
	if strings.Contains(transcript, "msg0") {
		t.Error("should not include exec before offset")
	}
	if strings.Contains(transcript, "msg2") {
		t.Error("should not include exec beyond limit")
	}
	if !strings.Contains(transcript, "Showing executions 2–2 of 3") {
		t.Error("expected pagination summary showing position")
	}
}

func TestFormatThreadTranscript_OffsetBeyondTotal(t *testing.T) {
	tc := NewTestContext(t)
	h := tc.handler

	task := &models.Task{
		ID:       "task-oob",
		Title:    "OOB Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Priority: 1,
	}

	executions := []models.Execution{
		{ID: "e0", Output: "out0", Status: models.ExecCompleted, StartedAt: time.Now()},
	}

	transcript := h.formatThreadTranscript(task, executions, 5, 0)
	if !strings.Contains(transcript, "Offset 5 exceeds total executions (1)") {
		t.Error("expected offset out-of-bounds message")
	}
}

func TestFormatThreadTranscript_Empty(t *testing.T) {
	tc := NewTestContext(t)
	h := tc.handler

	task := &models.Task{
		ID:       "task-empty",
		Title:    "Empty Task",
		Status:   models.StatusPending,
		Category: models.CategoryBacklog,
		Priority: 1,
	}

	transcript := h.formatThreadTranscript(task, []models.Execution{}, 0, 0)
	if !strings.Contains(transcript, "No execution history found") {
		t.Error("expected empty history message")
	}
}

func TestFormatThreadTranscript_LargeMessageTruncation(t *testing.T) {
	tc := NewTestContext(t)
	h := tc.handler

	task := &models.Task{
		ID:       "task-large",
		Title:    "Large Msg Task",
		Status:   models.StatusCompleted,
		Category: models.CategoryCompleted,
		Prompt:   "Do it",
		Priority: 1,
	}

	// Output larger than maxPerMessageBytes (50KB)
	hugeOutput := strings.Repeat("B", 60*1024)
	executions := []models.Execution{
		{ID: "e0", PromptSent: "go", Output: hugeOutput, Status: models.ExecCompleted, StartedAt: time.Now()},
	}

	transcript := h.formatThreadTranscript(task, executions, 0, 0)
	if !strings.Contains(transcript, "message truncated at 50KB") {
		t.Error("expected per-message truncation suffix for oversized output")
	}
	// The transcript itself should be well under 100KB even with truncation
	if len(transcript) > 100*1024 {
		t.Errorf("transcript too large: %d bytes", len(transcript))
	}
}

func TestViewThreadRequest_OffsetLimit(t *testing.T) {
	// Verify ViewThreadRequest parses offset/limit from JSON
	output := `[VIEW_TASK_CHAT]
{"task_id": "abc123", "offset": 5, "limit": 3}
[/VIEW_TASK_CHAT]`

	requests := service.ParseViewThread(output)
	if len(requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(requests))
	}
	if requests[0].TaskID != "abc123" {
		t.Errorf("expected task_id abc123, got %q", requests[0].TaskID)
	}
	if requests[0].Offset != 5 {
		t.Errorf("expected offset 5, got %d", requests[0].Offset)
	}
	if requests[0].Limit != 3 {
		t.Errorf("expected limit 3, got %d", requests[0].Limit)
	}
}
