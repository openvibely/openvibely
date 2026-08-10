package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProcessStreamingResponse_TaskFollowupRespectsGlobalWorkerLimit(t *testing.T) {
	setup := func(t *testing.T, maxWorkers int) (*Handler, *models.Project, *models.Task, *models.Execution, *models.LLMConfig, <-chan struct{}) {
		t.Helper()
		h, _, llmConfigRepo := setupTestHandler(t)
		h.workerSvc.Resize(maxWorkers)

		providerCalled := make(chan struct{}, 1)
		mock := testutil.NewMockLLMCaller()
		mock.Response = "follow-up complete"
		mock.TextOnly = "follow-up complete"
		mock.OnCall = func(context.Context, testutil.MockLLMCall) {
			providerCalled <- struct{}{}
		}
		h.llmSvc.SetLLMCaller(mock)

		agent := createAgent(t, llmConfigRepo)
		project := createProject(t, h, "Global Follow-up Capacity Project")
		task := createTask(t, h, project.ID, "Global Follow-up Capacity Task", func(tk *models.Task) {
			tk.Category = models.CategoryActive
			tk.Status = models.StatusQueued
			tk.AgentID = &agent.ID
		})
		exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecRunning
			ex.PromptSent = "follow-up prompt"
			ex.IsFollowup = true
		})
		return h, project, task, exec, agent, providerCalled
	}

	runFollowup := func(h *Handler, project *models.Project, task *models.Task, exec *models.Execution, agent *models.LLMConfig) <-chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			h.processStreamingResponse(streamingResponseParams{
				ExecID:         exec.ID,
				TaskID:         task.ID,
				Message:        "follow-up prompt",
				Agent:          *agent,
				ProjectID:      project.ID,
				IsTaskFollowup: true,
			})
		}()
		return done
	}

	t.Run("finite limit blocks provider until a global slot is released", func(t *testing.T) {
		h, project, task, exec, agent, providerCalled := setup(t, 1)
		require.True(t, h.workerSvc.TryAcquireProjectSlot(project.ID))

		done := runFollowup(h, project, task, exec, agent)
		startedEarly := false
		select {
		case <-providerCalled:
			startedEarly = true
		case <-time.After(150 * time.Millisecond):
		}

		h.workerSvc.ReleaseProjectSlot(project.ID)
		if !startedEarly {
			select {
			case <-providerCalled:
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for task follow-up after releasing global capacity")
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for task follow-up completion")
		}

		if startedEarly {
			t.Fatal("task follow-up reached provider while finite global worker limit was full")
		}
		require.Equal(t, 0, h.workerSvc.TotalRunning())
		require.Equal(t, 0, h.workerSvc.ProjectRunning(project.ID))
	})

	t.Run("unlimited permits provider while another worker is active", func(t *testing.T) {
		h, project, task, exec, agent, providerCalled := setup(t, 0)
		require.True(t, h.workerSvc.TryAcquireProjectSlot(project.ID))
		existingSlotHeld := true
		defer func() {
			if existingSlotHeld {
				h.workerSvc.ReleaseProjectSlot(project.ID)
			}
		}()

		done := runFollowup(h, project, task, exec, agent)
		select {
		case <-providerCalled:
		case <-time.After(3 * time.Second):
			t.Fatal("unlimited global worker limit blocked task follow-up provider call")
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for unlimited task follow-up completion")
		}

		require.Equal(t, 1, h.workerSvc.TotalRunning())
		require.Equal(t, 1, h.workerSvc.ProjectRunning(project.ID))
		h.workerSvc.ReleaseProjectSlot(project.ID)
		existingSlotHeld = false
		require.Equal(t, 0, h.workerSvc.TotalRunning())
		require.Equal(t, 0, h.workerSvc.ProjectRunning(project.ID))
	})
}

func TestStreamingTransportScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params streamingResponseParams
		want   string
	}{
		{name: "project chat", params: streamingResponseParams{ProjectID: "project-1", TaskID: "hidden-chat-task"}, want: "chat:project:project-1"},
		{name: "task followup", params: streamingResponseParams{ProjectID: "project-1", TaskID: "task-1", IsTaskFollowup: true}, want: "task:task-1"},
		{name: "task followup missing task", params: streamingResponseParams{ProjectID: "project-1", IsTaskFollowup: true}, want: ""},
		{name: "missing identity", params: streamingResponseParams{}, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := streamingTransportScope(tc.params); got != tc.want {
				t.Fatalf("streamingTransportScope() = %q, want %q", got, tc.want)
			}
		})
	}
}

type chatMemoryHookStore struct {
	hooks []models.AgentLifecycleHook
	seen  []models.LifecycleWhen
}

func (s *chatMemoryHookStore) HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error) {
	s.seen = append(s.seen, when)
	var out []models.AgentLifecycleHook
	for _, h := range s.hooks {
		if h.When == when && h.Enabled {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *chatMemoryHookStore) CreateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	if e.ID == "" {
		e.ID = "chat-memory-" + string(e.When)
	}
	return nil
}

func (s *chatMemoryHookStore) UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	return nil
}

func (s *chatMemoryHookStore) FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error) {
	return nil, os.ErrNotExist
}

type chatMemoryHookInvoker struct {
	mu          sync.Mutex
	seen        []string
	onInvoke    func(context.Context, models.AgentLifecycleHook, lifecycle.HookInput) error
	routeOutput *lifecycle.SelectedMemories
}

func (i *chatMemoryHookInvoker) Invoke(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
	i.mu.Lock()
	i.seen = append(i.seen, string(hook.When)+"/"+hook.SkillKey)
	i.mu.Unlock()
	if i.onInvoke != nil {
		if err := i.onInvoke(ctx, hook, in); err != nil {
			return nil, err
		}
	}
	if hook.When == models.LifecycleRouteTask {
		if i.routeOutput != nil {
			return json.Marshal(*i.routeOutput)
		}
		memories := []lifecycle.SelectedMemory{{File: "chat_memory.md", Summary: "prefer repo-local managed memory for this project."}}
		if strings.Contains(strings.ToLower(in.TaskPrompt), "usage analytics") {
			memories = []lifecycle.SelectedMemory{{File: "usage_analytics.md", Summary: "usage analytics for this app."}}
		}
		return json.Marshal(lifecycle.SelectedMemories{
			Memories:   memories,
			Content:    "",
			Confidence: 0.9,
			Reason:     "test",
		})
	}
	if hook.When == models.LifecycleBeforeRun {
		return json.Marshal(lifecycle.ContextBlock{Content: "Remember: prefer repo-local managed memory for this project.", Sources: []string{"MEMORIES.md"}, Confidence: 0.9})
	}
	return json.Marshal(lifecycle.ActivitySummary{Summary: "updated chat memory", ChangedPaths: []string{"chat.md"}})
}

func (i *chatMemoryHookInvoker) Seen() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.seen...)
}

func assertTaskFollowupSelectedMemoryContext(t *testing.T, chatContext string) {
	t.Helper()
	for _, want := range []string{"## Selected Memories For This Task", "memory_view(\"<memory>\")", "`chat_memory.md`"} {
		if !strings.Contains(chatContext, want) {
			t.Fatalf("expected task follow-up model context to include selected memory marker %q, got:\n%s", want, chatContext)
		}
	}
	for _, unwanted := range []string{"Selected chat memory body", "prefer repo-local managed memory for this project.", ".openvibely/memories/chat_memory.md"} {
		if strings.Contains(chatContext, unwanted) {
			t.Fatalf("task follow-up model context should include selected memory handles only, found %q in:\n%s", unwanted, chatContext)
		}
	}
}

func assertChatMemoryViewToolAvailable(t *testing.T, ctx context.Context) {
	t.Helper()
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected chat provider request to expose selected memory_view runtime tool, got %#v", rt)
	}
	for _, toolName := range []string{"memory_view"} {
		out, handled, isErr, err := rt.Executor(context.Background(), toolName, json.RawMessage(`{"handle":"chat_memory.md"}`))
		if err != nil || !handled || isErr || !strings.Contains(out, "Selected chat memory body.") {
			t.Fatalf("selected %s failed handled=%v isErr=%v err=%v out=%q", toolName, handled, isErr, err, out)
		}
		for _, input := range []string{`{"handle":".openvibely/memories/chat_memory.md"}`, `{"handle":"unselected.md"}`} {
			out, handled, isErr, err = rt.Executor(context.Background(), toolName, json.RawMessage(input))
			if err != nil || !handled || !isErr {
				t.Fatalf("unauthorized chat %s %s should be rejected handled=%v isErr=%v err=%v out=%q", toolName, input, handled, isErr, err, out)
			}
		}
	}
}

func seedHandlerTestMemoryIndex(t *testing.T, h *Handler, project *models.Project) {
	t.Helper()
	repoPath := t.TempDir()
	project.RepoPath = repoPath
	if err := h.projectRepo.Update(context.Background(), project); err != nil {
		t.Fatalf("update project repo path: %v", err)
	}
	h.workerSvc.SetProjectRepo(h.projectRepo)
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- chat_memory.md: prefer repo-local managed memory for this project.\n- usage_analytics.md: usage analytics for this app.\n"), 0o644); err != nil {
		t.Fatalf("write memory index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "chat_memory.md"), []byte("# Chat Memory\n\nSelected chat memory body."), 0o644); err != nil {
		t.Fatalf("write selected memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "usage_analytics.md"), []byte("# Usage Analytics\n\nSelected usage analytics memory body."), 0o644); err != nil {
		t.Fatalf("write usage analytics memory: %v", err)
	}
}

func createHandlerTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", "-b", "main"},
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

func TestFinalizeStreamingTurn_BroadcastsTaskFollowupResponseDone(t *testing.T) {
	chatBroadcaster := events.NewChatBroadcaster()
	h := &Handler{}
	h.SetChatBroadcaster(chatBroadcaster)
	sub, err := chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer chatBroadcaster.Unsubscribe(sub)

	h.finalizeStreamingTurn(streamingResponseParams{
		ProjectID:      "proj-1",
		TaskID:         "task-1",
		ExecID:         "exec-1",
		IsTaskFollowup: true,
	}, "final task-thread answer")

	select {
	case evt := <-sub:
		require.Equal(t, events.ChatResponseDone, evt.Type)
		require.Equal(t, "proj-1", evt.ProjectID)
		require.Equal(t, "task-1", evt.TaskID)
		require.Equal(t, "exec-1", evt.ExecID)
		require.Equal(t, "final task-thread answer", evt.CompletedOutput)
		require.True(t, evt.IsTaskFollowup)
	case <-time.After(time.Second):
		t.Fatal("expected task follow-up completion to broadcast chat_response_done with completed output")
	}
}

func TestFinalizeStreamingTurn_BroadcastsPersistedTerminalStatus(t *testing.T) {
	for _, status := range []models.ExecutionStatus{models.ExecFailed, models.ExecCancelled} {
		t.Run(string(status), func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().Build()
			task := tc.CreateTask(project.ID).WithCategory(models.CategoryBacklog).Build()
			exec := &models.Execution{TaskID: task.ID, Status: status, PromptSent: "terminal status"}
			require.NoError(t, tc.execRepo.Create(context.Background(), exec))

			chatBroadcaster := events.NewChatBroadcaster()
			tc.handler.SetChatBroadcaster(chatBroadcaster)
			sub, err := chatBroadcaster.Subscribe()
			require.NoError(t, err)
			defer chatBroadcaster.Unsubscribe(sub)

			tc.handler.finalizeStreamingTurn(streamingResponseParams{
				ProjectID:      project.ID,
				TaskID:         task.ID,
				ExecID:         exec.ID,
				IsTaskFollowup: true,
			}, "partial output")

			select {
			case evt := <-sub:
				require.Equal(t, string(status), evt.Status)
				require.Equal(t, "partial output", evt.CompletedOutput)
			case <-time.After(time.Second):
				t.Fatal("expected terminal chat_response_done event")
			}
		})
	}
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

func TestFilterChatHistory_StopsAtLatestNewContextBoundary(t *testing.T) {
	executions := []models.Execution{
		{ID: "old", Status: models.ExecCompleted, PromptSent: "old prompt"},
		{ID: "boundary", Status: models.ExecCompleted, PromptSent: "scheduled run", StartsNewContext: true},
		{ID: "new", Status: models.ExecCompleted, PromptSent: "follow-up"},
		{ID: "current", Status: models.ExecRunning},
	}

	result := filterChatHistory(executions, "current")
	require.Len(t, result, 2)
	assert.Equal(t, "boundary", result[0].ID)
	assert.Equal(t, "new", result[1].ID)
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

func TestProcessStreamingResponse_InteractiveChatAlreadyCancelledBeforeCallbackDoesNotCallModel(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "should not be called"
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Chat Early Stop Project")
	chatTask := createTask(t, h, project.ID, "Stopped Chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusCancelled
		tk.AgentID = &agent.ID
		tk.Prompt = "Stop before callback registration reaches the runner"
	})
	exec := createExec(t, h, chatTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = chatTask.Prompt
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         chatTask.ID,
		Message:        chatTask.Prompt,
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "task list context",
		IsTaskFollowup: false,
		ChatMode:       models.ChatModeOrchestrate,
	})

	if mock.CallCount() != 0 {
		t.Fatalf("expected early-cancelled chat to skip model call, got %d calls", mock.CallCount())
	}
	updatedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedExec)
	require.Equal(t, models.ExecCancelled, updatedExec.Status)
	updatedTask, err := h.taskRepo.GetByID(ctx, chatTask.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedTask)
	require.Equal(t, models.StatusCancelled, updatedTask.Status)
	require.Equal(t, models.CategoryChat, updatedTask.Category)
}

func TestProcessStreamingResponse_InteractiveChatRunsMemoryRecallOnly(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "chat response"
	mock.TextOnly = "chat response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Chat Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	chatTask := createTask(t, h, project.ID, "Chat host", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
		tk.Prompt = "What should I remember about managed memory?"
	})
	exec := createExec(t, h, chatTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = chatTask.Prompt
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "other-before", When: models.LifecycleBeforeRun, SkillKey: "load_context", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true},
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
		{ID: "update", When: models.LifecycleAfterComplete, SkillKey: "update_memory", OutputContract: models.OutputContractActivitySummary, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         chatTask.ID,
		Message:        chatTask.Prompt,
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "task list context",
		IsTaskFollowup: false,
		ChatMode:       models.ChatModeOrchestrate,
	})

	if mock.CallCount() != 1 {
		t.Fatalf("expected one chat model call, got %d", mock.CallCount())
	}
	seen := invoker.Seen()
	if len(seen) != 1 || seen[0] != "route_task/recall_memory" {
		t.Fatalf("expected only route_task recall hook for chat, got %#v", seen)
	}
	request := mock.LastAgentRequest()
	if chatContext := request.ChatSystemContext; !strings.Contains(chatContext, "## Selected Memories For This Task") || !strings.Contains(chatContext, "`chat_memory.md`") || strings.Contains(chatContext, "Remember: prefer repo-local managed memory for this project.") || strings.Contains(chatContext, "prefer repo-local managed memory for this project.") {
		t.Fatalf("expected selected memory handle without route/index summary in model-facing chat context, got:\n%s", chatContext)
	}
	assertChatMemoryViewToolAvailable(t, request.Ctx)
	for _, when := range store.seen {
		if when == models.LifecycleAfterComplete {
			t.Fatalf("interactive chat must not run after_complete memory extraction, saw slots %#v", store.seen)
		}
	}
	updatedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecCompleted {
		t.Fatalf("expected chat execution completed, got %s", updatedExec.Status)
	}
}

func TestProcessStreamingResponse_InteractiveChatPlanModeRunsMemoryRecallOnly(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	mock := testutil.NewMockLLMCaller()
	mock.Response = "<proposed_plan>Plan with memory.</proposed_plan>"
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Plan Chat Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	chatTask := createTask(t, h, project.ID, "Plan chat host", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
		tk.Prompt = "Plan how to update memory-safe chat."
	})
	exec := createExec(t, h, chatTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = chatTask.Prompt
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "other-before", When: models.LifecycleBeforeRun, SkillKey: "load_context", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true},
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
		{ID: "update", When: models.LifecycleAfterComplete, SkillKey: "update_memory", OutputContract: models.OutputContractActivitySummary, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         chatTask.ID,
		Message:        chatTask.Prompt,
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "task list context",
		IsTaskFollowup: false,
		ChatMode:       models.ChatModePlan,
	})

	if mock.CallCount() != 1 {
		t.Fatalf("expected one plan chat model call, got %d", mock.CallCount())
	}
	seen := invoker.Seen()
	if len(seen) != 1 || seen[0] != "route_task/recall_memory" {
		t.Fatalf("expected only route_task recall hook for plan chat, got %#v", seen)
	}
	request := mock.LastAgentRequest()
	if chatContext := request.ChatSystemContext; !strings.Contains(chatContext, "## Selected Memories For This Task") || !strings.Contains(chatContext, "`chat_memory.md`") || strings.Contains(chatContext, "Remember: prefer repo-local managed memory for this project.") || strings.Contains(chatContext, "prefer repo-local managed memory for this project.") {
		t.Fatalf("expected selected memory handle without route/index summary in model-facing plan chat context, got:\n%s", chatContext)
	}
	assertChatMemoryViewToolAvailable(t, request.Ctx)
	for _, when := range store.seen {
		if when == models.LifecycleAfterComplete {
			t.Fatalf("plan chat must not run after_complete memory extraction, saw slots %#v", store.seen)
		}
	}
	updatedExec, err := h.execRepo.GetByID(context.Background(), exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecCompleted {
		t.Fatalf("expected plan chat execution completed, got %s", updatedExec.Status)
	}
}

func TestProcessStreamingResponse_InteractiveChatExplicitMemoryViewRequestAuthorizesIndexedHandle(t *testing.T) {
	for _, mode := range []models.ChatMode{models.ChatModeOrchestrate, models.ChatModePlan} {
		t.Run(string(mode), func(t *testing.T) {
			h, _, llmConfigRepo := setupTestHandler(t)
			mock := testutil.NewMockLLMCaller()
			mock.Response = "chat response"
			mock.TextOnly = "chat response"
			h.llmSvc.SetLLMCaller(mock)

			agent := createAgent(t, llmConfigRepo)
			project := createProject(t, h, "Explicit Chat Memory Project "+string(mode))
			seedHandlerTestMemoryIndex(t, h, project)
			chatTask := createTask(t, h, project.ID, "Explicit chat host "+string(mode), func(tk *models.Task) {
				tk.Category = models.CategoryChat
				tk.Status = models.StatusPending
				tk.AgentID = &agent.ID
				tk.Prompt = `call memory_view on chat_memory.md and not .openvibely/memories/chat_memory.md or unindexed_chat.md`
			})
			exec := createExec(t, h, chatTask.ID, agent.ID, func(ex *models.Execution) {
				ex.Status = models.ExecRunning
				ex.PromptSent = chatTask.Prompt
			})

			store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
				{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
			}}
			invoker := &chatMemoryHookInvoker{
				routeOutput: &lifecycle.SelectedMemories{Memories: nil, Content: "", Confidence: 0, Reason: "curator missed explicit handle"},
				onInvoke: func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) error {
					if hook.When != models.LifecycleRouteTask {
						return fmt.Errorf("unexpected hook when %s", hook.When)
					}
					if !strings.Contains(in.TaskPrompt, "memory_view") || !strings.Contains(in.TaskPrompt, "chat_memory.md") {
						return fmt.Errorf("Memory Curator route hook did not receive explicit user prompt: %q", in.TaskPrompt)
					}
					available, _ := in.Extras["available_memories"].(string)
					if !strings.Contains(available, "chat_memory.md") || strings.Contains(available, "Selected chat memory body") {
						return fmt.Errorf("Memory Curator route hook missing compact MEMORIES.md index or received bodies: %#v", in.Extras["available_memories"])
					}
					return nil
				},
			}
			h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

			h.processStreamingResponse(streamingResponseParams{
				ExecID:         exec.ID,
				TaskID:         chatTask.ID,
				Message:        chatTask.Prompt,
				Agent:          *agent,
				ProjectID:      project.ID,
				SystemContext:  "task list context",
				IsTaskFollowup: false,
				ChatMode:       mode,
			})

			if mock.CallCount() != 1 {
				t.Fatalf("expected one chat model call, got %d", mock.CallCount())
			}
			seen := invoker.Seen()
			if len(seen) != 1 || seen[0] != "route_task/recall_memory" {
				t.Fatalf("expected route_task recall hook, got %#v", seen)
			}
			request := mock.LastAgentRequest()
			if chatContext := request.ChatSystemContext; !strings.Contains(chatContext, "## Selected Memories For This Task") || !strings.Contains(chatContext, "`chat_memory.md`") || strings.Contains(chatContext, ".openvibely/memories/chat_memory.md") || strings.Contains(chatContext, "unindexed_chat.md") || strings.Contains(chatContext, "Selected chat memory body") {
				t.Fatalf("expected explicit indexed memory handle only in provider request, got:\n%s", chatContext)
			}
			assertChatMemoryViewToolAvailable(t, request.Ctx)
		})
	}
}

func TestProcessStreamingResponse_TaskFollowupPreservesFailedExecutionHistory(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	mock := testutil.NewMockLLMCaller()
	mock.Response = "followup response"
	mock.TextOnly = "followup response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Failed Task Followup History Project")
	task := createTask(t, h, project.ID, "Failed task followup history", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "original failed task prompt"
	})
	ctx := context.Background()
	failedExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "original failed task prompt"
	})
	require.NoError(t, h.execRepo.Complete(ctx, failedExec.ID, models.ExecFailed, "", "provider timeout", 0, 1))
	failedExec, err := h.execRepo.GetByID(ctx, failedExec.ID)
	require.NoError(t, err)
	require.NotNil(t, failedExec)
	currentExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "what happened before?"
		ex.IsFollowup = true
	})
	priorExecs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         currentExec.ID,
		TaskID:         task.ID,
		Message:        "what happened before?",
		Agent:          *agent,
		ChatHistory:    filterChatHistory(priorExecs, currentExec.ID),
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	require.Equal(t, 1, mock.CallCount())
	request := mock.LastAgentRequest()
	require.Len(t, request.ChatHistory, 1)
	assert.Equal(t, failedExec.ID, request.ChatHistory[0].ID)
	assert.Equal(t, models.ExecFailed, request.ChatHistory[0].Status)
	assert.Equal(t, "original failed task prompt", request.ChatHistory[0].PromptSent)
	assert.Equal(t, "provider timeout", request.ChatHistory[0].ErrorMessage)
	assert.Equal(t, "what happened before?", request.Message)
	updatedCurrent, err := h.execRepo.GetByID(context.Background(), currentExec.ID)
	require.NoError(t, err)
	require.NotNil(t, updatedCurrent)
	assert.Equal(t, models.ExecCompleted, updatedCurrent.Status)
	assert.Equal(t, "followup response", updatedCurrent.Output)
}

func TestProcessStreamingResponse_TaskFollowupRoutesMemoryFromCurrentMessage(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	mock := testutil.NewMockLLMCaller()
	mock.Response = "task followup response"
	mock.TextOnly = "task followup response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Task Followup Current Message Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Realtime task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Tell me about realtime front end patterns for this app."
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "tell me about usage analytics"
		ex.IsFollowup = true
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{
		onInvoke: func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) error {
			if hook.When != models.LifecycleRouteTask {
				return nil
			}
			if !strings.Contains(in.TaskPrompt, "usage analytics") {
				return fmt.Errorf("expected route hook to receive current follow-up prompt, got %q", in.TaskPrompt)
			}
			if strings.Contains(in.TaskPrompt, "realtime front end") {
				return fmt.Errorf("route hook received stale original task prompt: %q", in.TaskPrompt)
			}
			if got, _ := in.Extras["original_task_prompt"].(string); !strings.Contains(got, "realtime front end") {
				return fmt.Errorf("expected original task prompt in extras, got %#v", in.Extras["original_task_prompt"])
			}
			return nil
		},
	}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "tell me about usage analytics",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	if mock.CallCount() != 1 {
		t.Fatalf("expected one task followup model call, got %d", mock.CallCount())
	}
	seen := invoker.Seen()
	if len(seen) != 1 || seen[0] != "route_task/recall_memory" {
		t.Fatalf("expected route_task memory recall hook for task followup, got %#v", seen)
	}
	request := mock.LastAgentRequest()
	if chatContext := request.ChatSystemContext; !strings.Contains(chatContext, "`usage_analytics.md`") || !strings.Contains(chatContext, "MUST call `memory_view(\"<memory>\")`") || !strings.Contains(chatContext, "tell them about a selected memory/topic") || strings.Contains(chatContext, "`chat_memory.md`") {
		t.Fatalf("expected usage analytics selected memory and mandatory memory_view instruction in task followup provider request, got:\n%s", chatContext)
	}
}

func TestProcessStreamingResponse_TaskFollowupExposesSelectedMemoryViewTool(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	mock := testutil.NewMockLLMCaller()
	mock.Response = "task followup response"
	mock.TextOnly = "task followup response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Task Followup Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Task memory followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Use managed memory if needed."
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "follow up with memory"
		ex.IsFollowup = true
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "follow up with memory",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	if mock.CallCount() != 1 {
		t.Fatalf("expected one task followup model call, got %d", mock.CallCount())
	}
	seen := invoker.Seen()
	if len(seen) != 1 || seen[0] != "route_task/recall_memory" {
		t.Fatalf("expected route_task memory recall hook for task followup, got %#v", seen)
	}
	request := mock.LastAgentRequest()
	if request.Operation != llmcontracts.OperationStreaming || !request.Followup {
		t.Fatalf("expected streaming task followup provider request, got operation=%s followup=%v", request.Operation, request.Followup)
	}
	assertTaskFollowupSelectedMemoryContext(t, request.ChatSystemContext)
	assertChatMemoryViewToolAvailable(t, request.Ctx)
}

func TestProcessStreamingResponse_TaskFollowupExplicitMemoryViewRequestAuthorizesIndexedHandleWhenRouterMisses(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	mock := testutil.NewMockLLMCaller()
	mock.Response = "task followup response"
	mock.TextOnly = "task followup response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Task Followup Explicit Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Task explicit memory followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Use managed memory if needed."
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "call memory_view on chat_memory.md"
		ex.IsFollowup = true
	})

	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(&chatMemoryHookStore{}, &chatMemoryHookInvoker{}, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "call memory_view on chat_memory.md",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	if mock.CallCount() != 1 {
		t.Fatalf("expected one task followup model call, got %d", mock.CallCount())
	}
	request := mock.LastAgentRequest()
	if chatContext := request.ChatSystemContext; !strings.Contains(chatContext, "## Selected Memories For This Task") || !strings.Contains(chatContext, "`chat_memory.md`") {
		t.Fatalf("expected explicit selected memory in task followup provider request when router misses, got:\n%s", chatContext)
	}
	assertChatMemoryViewToolAvailable(t, request.Ctx)
}

func TestProcessStreamingResponse_ListCapabilitiesShowsSelectedMemoryHandles(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	mock := testutil.NewMockLLMCaller()
	mock.Response = "task followup response"
	mock.TextOnly = "task followup response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Task Followup Capability Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Task memory capability followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Use managed memory if needed."
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "what memories did the router provide?"
		ex.IsFollowup = true
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "what memories did the router provide?",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	request := mock.LastAgentRequest()
	if handles := service.SelectedMemoryHandlesFromContext(request.Ctx); len(handles) != 1 || handles[0] != "chat_memory.md" {
		t.Fatalf("expected selected memory handles in provider request context, got %#v", handles)
	}
}

func TestProcessStreamingResponse_TaskFollowupListCapabilitiesIncludesSelectedMemoryHandles(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Task Followup Capability Tool Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Task memory capability tool followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Use managed memory if needed."
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	ctx := context.Background()
	ctx = service.WithTaskThreadLifecycleTurnPrompt(ctx, "what memories did the router provide?")
	turn := h.workerSvc.PrepareLifecycleTurn(ctx, *task)
	ctx = turn.Ctx

	params := streamingResponseParams{
		TaskID:         task.ID,
		Message:        "what memories did the router provide?",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	}
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, true)
	rt := h.buildChatActionToolRuntimeFromDefs(params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	ctx = llmcontracts.WithRuntimeTools(ctx, llmcontracts.CompositeRuntimeTools(llmcontracts.RuntimeToolsFromContext(ctx), rt))

	composed := llmcontracts.RuntimeToolsFromContext(ctx)
	if composed == nil || !composed.HasDefinition("list_capabilities") || !composed.HasDefinition("memory_view") {
		t.Fatalf("expected list_capabilities and memory_view runtime tools, got %#v", composed)
	}
	memoryViewDefs := 0
	for _, def := range composed.Definitions {
		if strings.EqualFold(strings.TrimSpace(def.Name), "memory_view") {
			memoryViewDefs++
		}
	}
	if memoryViewDefs != 1 {
		t.Fatalf("expected exactly one memory_view runtime definition, got %d in %#v", memoryViewDefs, composed.Definitions)
	}
	out, handled, isErr, err := composed.Executor(ctx, "list_capabilities", nil)
	if err != nil || !handled || isErr {
		t.Fatalf("execute list_capabilities handled=%v isErr=%v err=%v output=%q", handled, isErr, err, out)
	}
	if !strings.Contains(out, "Selected memories for this turn") || !strings.Contains(out, "chat_memory.md") || !strings.Contains(out, "memory_view") {
		t.Fatalf("expected list_capabilities to include selected memory handle and memory_view, got:\n%s", out)
	}
}

func TestProcessStreamingResponse_ManualFollowupReactivatesAchievedGoalForCheckpoint(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	goalRepo := repository.NewTaskGoalRepo(db)
	goalSvc := service.NewTaskGoalService(goalRepo, h.taskRepo, nil)
	h.SetTaskGoalService(goalSvc)
	h.taskSvc.SetTaskGoalService(goalSvc)
	h.workerSvc.SetTaskGoalService(goalSvc)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	h.workerSvc.SetLifecycleAgentRepo(agentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "manual followup complete"
	mock.TextOnly = "manual followup complete"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Achieved Goal Manual Followup Project")
	task := createTask(t, h, project.ID, "Achieved Goal Manual Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Continue task"
	})
	goal, err := h.taskGoalSvc.SetGoal(ctx, task.ID, "Keep the invariant satisfied", service.GoalOptions{})
	require.NoError(t, err)
	_, err = h.taskGoalSvc.MarkAchieved(ctx, task.ID, goal.GoalID, "initially satisfied")
	require.NoError(t, err)
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "manual followup input"
	})
	goalAgent := &models.Agent{Key: models.AgentSystemKindGoal, Name: "System: Goal Agent", Model: "inherit", Tools: []string{"get_task_goal", "send_to_task", "mark_task_goal_achieved", "report_task_goal_blocked"}, SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, CreatedBy: models.AgentCreatedBySystem, Enabled: true}
	require.NoError(t, agentRepo.Create(ctx, goalAgent))
	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{{ID: "goal-hook-reactivate", AgentID: goalAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "manual followup input",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
		InputOrigin:    models.TaskOriginWeb,
	})

	require.Eventually(t, func() bool {
		for _, seen := range invoker.Seen() {
			if seen == "after_complete/evaluate_task_goal" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "expected Goal Agent after_complete hook after manual achieved-goal follow-up, seen=%#v", invoker.Seen())

	latest, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, models.TaskGoalStatusActive, latest.Status)
	require.Nil(t, latest.AchievedAt)
}

func TestProcessStreamingResponse_GoalAgentQueuedFollowupDoesNotReactivateAchievedGoal(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	goalRepo := repository.NewTaskGoalRepo(db)
	goalSvc := service.NewTaskGoalService(goalRepo, h.taskRepo, nil)
	h.SetTaskGoalService(goalSvc)
	h.taskSvc.SetTaskGoalService(goalSvc)
	h.workerSvc.SetTaskGoalService(goalSvc)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "goal followup complete"
	mock.TextOnly = "goal followup complete"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Goal Agent Followup No Reactivate Project")
	task := createTask(t, h, project.ID, "Goal Agent Followup No Reactivate Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Continue task"
	})
	goal, err := h.taskGoalSvc.SetGoal(ctx, task.ID, "Already satisfied", service.GoalOptions{})
	require.NoError(t, err)
	achieved, err := h.taskGoalSvc.MarkAchieved(ctx, task.ID, goal.GoalID, "done")
	require.NoError(t, err)
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "system continuation"
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:           exec.ID,
		TaskID:           task.ID,
		Message:          "system continuation",
		Agent:            *agent,
		ProjectID:        project.ID,
		IsTaskFollowup:   true,
		ChatMode:         models.ChatModeOrchestrate,
		InputOrigin:      models.TaskOriginSystemAgent,
		InputOriginAgent: models.AgentSystemKindGoal,
	})

	latest, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, models.TaskGoalStatusAchieved, latest.Status)
	require.Equal(t, achieved.GoalID, latest.GoalID)
	require.NotNil(t, latest.AchievedAt)
}

func TestProcessStreamingResponse_GenericAfterCompleteRunsGoalAgentWithoutAutoEnqueue(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	goalRepo := repository.NewTaskGoalRepo(db)
	goalSvc := service.NewTaskGoalService(goalRepo, h.taskRepo, nil)
	h.SetTaskGoalService(goalSvc)
	h.taskSvc.SetTaskGoalService(goalSvc)
	h.workerSvc.SetTaskGoalService(goalSvc)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	h.workerSvc.SetLifecycleAgentRepo(agentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "followup complete"
	mock.TextOnly = "followup complete"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Goal Checkpoint Project")
	task := createTask(t, h, project.ID, "Goal Checkpoint Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Continue task"
	})
	goal, err := h.taskGoalSvc.SetGoal(ctx, task.ID, "Ship the persisted goal", service.GoalOptions{})
	require.NoError(t, err)
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "followup input"
	})
	goalAgent := &models.Agent{Key: models.AgentSystemKindGoal, Name: "System: Goal Agent", Model: "inherit", Tools: []string{"get_task_goal", "send_to_task", "mark_task_goal_achieved", "report_task_goal_blocked"}, SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, CreatedBy: models.AgentCreatedBySystem, Enabled: true}
	require.NoError(t, agentRepo.Create(ctx, goalAgent))
	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{{ID: "goal-hook", AgentID: goalAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}}}
	invoker := &chatMemoryHookInvoker{onInvoke: func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) error {
		if hook.SkillKey == "evaluate_task_goal" && in.Extras["task_goal"] == nil {
			return fmt.Errorf("missing task_goal extras in generic after_complete hook input")
		}
		return nil
	}}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "followup input",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	require.Eventually(t, func() bool {
		n := 0
		for _, seen := range invoker.Seen() {
			if seen == "after_complete/evaluate_task_goal" {
				n++
			}
		}
		return n == 1
	}, 2*time.Second, 10*time.Millisecond, "expected Goal Agent lifecycle hook once, got seen=%#v", invoker.Seen())
	pending, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	if len(pending) != 0 {
		t.Fatalf("generic Goal Agent after_complete must not enqueue without send_to_task tool call, got %#v for goal %#v", pending, goal)
	}
}

func TestProcessStreamingResponse_GenericAfterCompletePublishesReloadedGoalStatus(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	broadcaster := events.NewBroadcaster()
	h.broadcaster = broadcaster
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)
	goalRepo := repository.NewTaskGoalRepo(db)
	goalSvc := service.NewTaskGoalService(goalRepo, h.taskRepo, broadcaster)
	h.SetTaskGoalService(goalSvc)
	h.taskSvc.SetTaskGoalService(goalSvc)
	h.workerSvc.SetTaskGoalService(goalSvc)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	h.workerSvc.SetLifecycleAgentRepo(agentRepo)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "followup complete"
	mock.TextOnly = "followup complete"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Goal Checkpoint Reload Project")
	task := createTask(t, h, project.ID, "Goal Checkpoint Reload Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.Prompt = "Continue task"
	})
	goal, err := h.taskGoalSvc.SetGoal(ctx, task.ID, "Ship the persisted goal", service.GoalOptions{})
	require.NoError(t, err)
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "followup input"
	})
	goalAgent := &models.Agent{Key: models.AgentSystemKindGoal, Name: "System: Goal Agent", Model: "inherit", Tools: []string{"get_task_goal", "send_to_task", "mark_task_goal_achieved", "report_task_goal_blocked"}, SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, CreatedBy: models.AgentCreatedBySystem, Enabled: true}
	require.NoError(t, agentRepo.Create(ctx, goalAgent))
	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{{ID: "goal-hook-reload", AgentID: goalAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}}}
	invoker := &chatMemoryHookInvoker{onInvoke: func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) error {
		if hook.SkillKey == "evaluate_task_goal" {
			_, err := goalSvc.MarkAchieved(ctx, task.ID, goal.GoalID, "goal satisfied")
			return err
		}
		return nil
	}}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "followup input",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatMode:       models.ChatModeOrchestrate,
	})

	deadline := time.Now().Add(2 * time.Second)
	var goalEvents []events.TaskEvent
	for time.Now().Before(deadline) {
		select {
		case ev := <-sub:
			if ev.Type == events.TaskGoalEvaluated && ev.TaskID == task.ID {
				goalEvents = append(goalEvents, ev)
			}
		default:
			if len(invoker.Seen()) > 0 && len(goalEvents) >= 2 {
				goto done
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
done:
	if len(goalEvents) == 0 {
		t.Fatalf("expected goal evaluation events after generic after_complete")
	}
	for _, ev := range goalEvents {
		if ev.GoalStatus == string(models.TaskGoalStatusActive) {
			t.Fatalf("generic after_complete published stale active goal event after hook mutation: %#v", goalEvents)
		}
	}
	latest, err := goalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, latest)
	require.Equal(t, models.TaskGoalStatusAchieved, latest.Status)
}

func waitForExecutionTerminal(t *testing.T, h *Handler, execID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		exec, err := h.execRepo.GetByID(context.Background(), execID)
		if err != nil || exec == nil {
			return false
		}
		return exec.Status != models.ExecRunning
	}, 2*time.Second, 25*time.Millisecond)
}

func TestHandler_InitialTaskTurnQueuesRuntimeFollowupBeforeExecutionExistsAndPromotes(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Pre Execution Queued Followup Project")
	task := createTask(t, h, project.ID, "Pre Execution Queued Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "tell me a story about a duck"
		tk.AgentID = &agent.ID
	})

	queued, err := h.enqueueTaskThreadInput(ctx, task.ID, "1+1=?", models.TaskOriginWeb, "")
	require.NoError(t, err)
	require.Empty(t, queued.RunExecutionID)
	require.Equal(t, models.ThreadInputPending, queued.InputStatus)

	dispatchClaim, claimed, err := h.taskRepo.ClaimTaskForDispatch(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, claimed, "a generic follow-up queued before the first execution must not block the original worker claim")
	require.NotNil(t, dispatchClaim)
	require.Equal(t, models.StatusRunning, dispatchClaim.Task.Status)
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending), "restore fixture for direct LLM execution")

	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, execs, "pre-execution queueing must not create a follow-up execution")

	started := make(chan testutil.MockLLMCall, 2)
	releaseInitial := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(ctx context.Context, call testutil.MockLLMCall) {
		started <- call
		if call.Prompt == "tell me a story about a duck" {
			select {
			case <-releaseInitial:
			case <-ctx.Done():
			}
		}
	}
	h.llmSvc.SetLLMCaller(mock)
	h.llmSvc.SetThreadInputRepo(h.threadInputRepo)

	done := make(chan error, 1)
	go func() {
		_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
		done <- err
	}()

	var initial testutil.MockLLMCall
	select {
	case initial = <-started:
		require.Equal(t, "tell me a story about a duck", initial.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial task turn")
	}
	bound, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, initial.ExecID, bound.RunExecutionID)
	require.Equal(t, models.ThreadInputPending, bound.InputStatus)

	select {
	case promoted := <-started:
		t.Fatalf("queued follow-up promoted before initial turn completed: %#v", promoted)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseInitial)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial task turn to finish")
	}

	var promoted testutil.MockLLMCall
	select {
	case promoted = <-started:
		require.Equal(t, "1+1=?", promoted.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up to promote")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	require.NotEqual(t, initial.ExecID, stored.RunExecutionID)
	waitForExecutionTerminal(t, h, promoted.ExecID)
}

func TestHandler_InitialTaskTurnFailurePromotesRuntimeFollowupQueuedBeforeExecutionExists(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Pre Execution Failure Queued Followup Project")
	task := createTask(t, h, project.ID, "Pre Execution Failure Queued Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "initial task prompt"
		tk.AgentID = &agent.ID
	})

	queued, err := h.enqueueTaskThreadInput(ctx, task.ID, "follow-up after pre-execution failure", models.TaskOriginWeb, "")
	require.NoError(t, err)
	require.Empty(t, queued.RunExecutionID)

	started := make(chan testutil.MockLLMCall, 2)
	releaseInitial := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(callCtx context.Context, call testutil.MockLLMCall) {
		started <- call
		if call.Prompt == "initial task prompt" {
			select {
			case <-releaseInitial:
			case <-callCtx.Done():
			}
			mock.Response = "[STATUS: FAILED | initial provider failed]"
			mock.TextOnly = "[STATUS: FAILED | initial provider failed]"
			return
		}
		mock.Response = "done"
		mock.TextOnly = "done"
	}
	h.llmSvc.SetLLMCaller(mock)

	done := make(chan error, 1)
	go func() {
		_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
		done <- err
	}()

	var initial testutil.MockLLMCall
	select {
	case initial = <-started:
		require.Equal(t, "initial task prompt", initial.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed initial task turn")
	}
	bound, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, initial.ExecID, bound.RunExecutionID)
	close(releaseInitial)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed initial task turn to finish")
	}
	var promoted testutil.MockLLMCall
	select {
	case promoted = <-started:
		require.Equal(t, "follow-up after pre-execution failure", promoted.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up to promote after pre-execution failure")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	waitForExecutionTerminal(t, h, promoted.ExecID)
}

func TestHandler_InitialTaskTurnCancellationPromotesRuntimeFollowupQueuedBeforeExecutionExists(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx, cancel := context.WithCancel(context.Background())
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Pre Execution Cancellation Queued Followup Project")
	task := createTask(t, h, project.ID, "Pre Execution Cancellation Queued Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "initial task prompt"
		tk.AgentID = &agent.ID
	})

	queued, err := h.enqueueTaskThreadInput(context.Background(), task.ID, "follow-up after pre-execution cancellation", models.TaskOriginWeb, "")
	require.NoError(t, err)
	require.Empty(t, queued.RunExecutionID)

	started := make(chan testutil.MockLLMCall, 2)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(callCtx context.Context, call testutil.MockLLMCall) {
		started <- call
		if call.Prompt == "initial task prompt" {
			<-callCtx.Done()
			mock.Err = context.Canceled
			return
		}
		mock.Err = nil
	}
	h.llmSvc.SetLLMCaller(mock)

	done := make(chan error, 1)
	go func() {
		_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
		done <- err
	}()

	var initial testutil.MockLLMCall
	select {
	case initial = <-started:
		require.Equal(t, "initial task prompt", initial.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellable initial task turn")
	}
	bound, err := h.threadInputRepo.GetByID(context.Background(), queued.ID)
	require.NoError(t, err)
	require.Equal(t, initial.ExecID, bound.RunExecutionID)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "task cancelled")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled initial task turn to finish")
	}
	var promoted testutil.MockLLMCall
	select {
	case promoted = <-started:
		require.Equal(t, "follow-up after pre-execution cancellation", promoted.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up to promote after pre-execution cancellation")
	}
	stored, err := h.threadInputRepo.GetByID(context.Background(), queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	waitForExecutionTerminal(t, h, promoted.ExecID)
}

func TestHandler_InitialTaskTurnQueuesFollowupBeforeFirstOutputAndPromotes(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Initial Turn Queued Followup Project")
	task := createTask(t, h, project.ID, "Initial Turn Queued Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "initial task prompt"
		tk.AgentID = &agent.ID
	})

	started := make(chan testutil.MockLLMCall, 2)
	releaseInitial := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(ctx context.Context, call testutil.MockLLMCall) {
		started <- call
		if call.Prompt == "initial task prompt" {
			select {
			case <-releaseInitial:
			case <-ctx.Done():
			}
		}
	}
	h.llmSvc.SetLLMCaller(mock)

	done := make(chan error, 1)
	go func() {
		_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
		done <- err
	}()

	var initial testutil.MockLLMCall
	select {
	case initial = <-started:
		require.Equal(t, "initial task prompt", initial.Prompt)
		require.NotEmpty(t, initial.ExecID)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial task turn to start")
	}

	queued, err := h.enqueueTaskThreadInput(ctx, task.ID, "follow-up before first output", models.TaskOriginWeb, "")
	require.NoError(t, err)
	require.Equal(t, initial.ExecID, queued.RunExecutionID)
	require.Equal(t, models.ThreadInputPending, queued.InputStatus)

	pending, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, queued.ID, pending[0].ID)
	require.Equal(t, initial.ExecID, pending[0].RunExecutionID)

	select {
	case promoted := <-started:
		t.Fatalf("queued follow-up promoted before initial turn completed: %#v", promoted)
	case <-time.After(100 * time.Millisecond):
	}

	close(releaseInitial)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial task turn to finish")
	}

	var promoted testutil.MockLLMCall
	select {
	case promoted = <-started:
		require.Equal(t, "follow-up before first output", promoted.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up to promote")
	}
	promotedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryActive, promotedTask.Category)
	require.Equal(t, models.StatusQueued, promotedTask.Status)

	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	require.NotEmpty(t, stored.RunExecutionID)
	require.NotEqual(t, initial.ExecID, stored.RunExecutionID)
	waitForExecutionTerminal(t, h, promoted.ExecID)
}

func TestHandler_InitialTaskTurnFailurePromotesQueuedFollowup(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Initial Failure Queued Followup Project")
	task := createTask(t, h, project.ID, "Initial Failure Queued Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "initial task prompt"
		tk.AgentID = &agent.ID
	})

	started := make(chan testutil.MockLLMCall, 2)
	releaseInitial := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(callCtx context.Context, call testutil.MockLLMCall) {
		started <- call
		if call.Prompt == "initial task prompt" {
			select {
			case <-releaseInitial:
			case <-callCtx.Done():
			}
			mock.Response = "[STATUS: FAILED | initial provider failed]"
			mock.TextOnly = "[STATUS: FAILED | initial provider failed]"
			return
		}
		mock.Response = "done"
		mock.TextOnly = "done"
	}
	h.llmSvc.SetLLMCaller(mock)

	done := make(chan error, 1)
	go func() {
		_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
		done <- err
	}()

	var initial testutil.MockLLMCall
	select {
	case initial = <-started:
		require.Equal(t, "initial task prompt", initial.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed initial task turn")
	}
	queued, err := h.enqueueTaskThreadInput(ctx, task.ID, "follow-up after failed initial turn", models.TaskOriginWeb, "")
	require.NoError(t, err)
	require.Equal(t, initial.ExecID, queued.RunExecutionID)
	close(releaseInitial)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for failed initial task turn to finish")
	}
	var promoted testutil.MockLLMCall
	select {
	case promoted = <-started:
		require.Equal(t, "follow-up after failed initial turn", promoted.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up to promote after failure")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	waitForExecutionTerminal(t, h, promoted.ExecID)
}

func TestHandler_InitialTaskTurnCancellationPromotesQueuedFollowup(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx, cancel := context.WithCancel(context.Background())
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Initial Cancellation Queued Followup Project")
	task := createTask(t, h, project.ID, "Initial Cancellation Queued Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "initial task prompt"
		tk.AgentID = &agent.ID
	})

	started := make(chan testutil.MockLLMCall, 2)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(callCtx context.Context, call testutil.MockLLMCall) {
		started <- call
		if call.Prompt == "initial task prompt" {
			<-callCtx.Done()
			mock.Err = context.Canceled
			return
		}
		mock.Err = nil
	}
	h.llmSvc.SetLLMCaller(mock)

	done := make(chan error, 1)
	go func() {
		_, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent)
		done <- err
	}()

	var initial testutil.MockLLMCall
	select {
	case initial = <-started:
		require.Equal(t, "initial task prompt", initial.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellable initial task turn")
	}
	queued, err := h.enqueueTaskThreadInput(context.Background(), task.ID, "follow-up after cancelled initial turn", models.TaskOriginWeb, "")
	require.NoError(t, err)
	require.Equal(t, initial.ExecID, queued.RunExecutionID)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		require.Contains(t, err.Error(), "task cancelled")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancelled initial task turn to finish")
	}
	var promoted testutil.MockLLMCall
	select {
	case promoted = <-started:
		require.Equal(t, "follow-up after cancelled initial turn", promoted.Prompt)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued follow-up to promote after cancellation")
	}
	stored, err := h.threadInputRepo.GetByID(context.Background(), queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	waitForExecutionTerminal(t, h, promoted.ExecID)
}

func TestHandler_WorkerCompletionPromotesQueuedTaskThreadInput(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Worker Completion Wired Promotion Project")
	task := createTask(t, h, project.ID, "Worker Completion Wired Promotion Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "Run initial worker task"
		tk.AgentID = &agent.ID
	})

	started := make(chan string, 2)
	queuedCreated := make(chan struct{}, 1)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		started <- call.Prompt
		if call.Prompt == "Run initial worker task" {
			queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: call.ExecID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued while worker runs"}
			require.NoError(t, h.threadInputRepo.CreateQueued(context.Background(), queued))
			queuedCreated <- struct{}{}
		}
	}
	h.llmSvc.SetLLMCaller(mock)

	if _, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent); err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	select {
	case <-queuedCreated:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued input fixture")
	}
	select {
	case got := <-started:
		if got != "Run initial worker task" {
			t.Fatalf("expected original worker prompt first, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for original worker prompt")
	}
	select {
	case got := <-started:
		if got != "queued while worker runs" {
			t.Fatalf("expected queued worker follow-up to start, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued worker follow-up to start")
	}
	inputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	if len(inputs) != 0 {
		t.Fatalf("expected no stranded pending queued inputs, got %#v", inputs)
	}
}

func TestHandler_RecoverQueuedInputsPromotesChatAfterDrain(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Recover Chat Queue After Drain")
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeChat, ProjectID: project.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued chat after drain", ChatMode: models.ChatModeOrchestrate}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))
	started := make(chan string, 1)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "recovered"
	mock.TextOnly = "recovered"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) { started <- call.Prompt }
	h.llmSvc.SetLLMCaller(mock)

	h.RecoverQueuedInputs(ctx)
	select {
	case got := <-started:
		require.Equal(t, queued.Content, got)
	case <-time.After(2 * time.Second):
		t.Fatal("queued Chat input was not resumed after drain")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
}

func TestHandler_RecoverQueuedTaskThreadInputsDrainsMoreThanOneBatch(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Recover Batched Task Thread Queue Project")
	const taskCount = 101
	tasks := make([]*models.Task, 0, taskCount)
	inputs := make([]*models.ThreadInput, 0, taskCount)
	for i := 0; i < taskCount; i++ {
		task := createTask(t, h, project.ID, fmt.Sprintf("Recover Batched Task %03d", i), func(tk *models.Task) {
			tk.Category = models.CategoryCompleted
			tk.Status = models.StatusCompleted
			tk.AgentID = &agent.ID
		})
		input := &models.ThreadInput{
			Scope:         models.ThreadInputScopeTask,
			ProjectID:     project.ID,
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			InputMode:     models.ThreadInputModeQueued,
			Content:       fmt.Sprintf("recover follow-up %03d", i),
		}
		require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))
		tasks = append(tasks, task)
		inputs = append(inputs, input)
	}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "recovered"
	mock.TextOnly = "recovered"
	h.llmSvc.SetLLMCaller(mock)

	h.RecoverQueuedTaskThreadInputs(ctx)

	for i, input := range inputs {
		stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
		require.NoError(t, err)
		require.Equalf(t, models.ThreadInputApplied, stored.InputStatus, "input %d was stranded beyond the recovery batch", i)
		execs, err := h.execRepo.ListByTaskChronological(ctx, tasks[i].ID)
		require.NoError(t, err)
		require.Lenf(t, execs, 1, "task %d should have exactly one promoted execution", i)
		require.Equal(t, input.Content, execs[0].PromptSent)
	}
}

func TestHandler_RecoverQueuedTaskThreadInputsPromotesUnboundInputAfterClaimCrash(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Recover Unbound Task Thread Queue Project")
	task := createTask(t, h, project.ID, "Recover Unbound Task Thread Queue Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.Prompt = "stored original prompt"
		tk.AgentID = &agent.ID
	})
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "follow-up queued before execution insertion"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending), "startup resets the abandoned ordinary claim")

	started := make(chan string, 1)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "recovered"
	mock.TextOnly = "recovered"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) { started <- call.Prompt }
	h.llmSvc.SetLLMCaller(mock)

	h.RecoverQueuedTaskThreadInputs(ctx)
	select {
	case got := <-started:
		if got != queued.Content {
			t.Fatalf("expected unbound FIFO follow-up, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for unbound queued follow-up recovery")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	require.Equal(t, queued.Content, execs[0].PromptSent, "startup must not rerun the stored original prompt")
}

func TestHandler_RecoverQueuedTaskThreadInputsPromotesStrandedInput(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Recover Stranded Task Thread Queue Project")
	task := createTask(t, h, project.ID, "Recover Stranded Task Thread Queue Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.Prompt = "original worker prompt"
		tk.AgentID = &agent.ID
	})
	original := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: task.Prompt}
	require.NoError(t, h.execRepo.Create(ctx, original))
	require.NoError(t, h.execRepo.Complete(ctx, original.ID, models.ExecCompleted, "done", "", 0, 1))
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: original.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "what's 1+1=?"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))

	started := make(chan string, 1)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "2"
	mock.TextOnly = "2"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		started <- call.Prompt
	}
	h.llmSvc.SetLLMCaller(mock)

	h.RecoverQueuedTaskThreadInputs(ctx)
	select {
	case got := <-started:
		if got != "what's 1+1=?" {
			t.Fatalf("expected stranded queued follow-up to start, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for stranded queued follow-up to start")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	if stored.InputStatus != models.ThreadInputApplied {
		t.Fatalf("expected stranded queued input applied, got %#v", stored)
	}
}

func TestHandler_WorkerCompletionPromotesFirstQueuedTaskThreadInputAndRetargetsRest(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Worker Completion Wired Multi Promotion Project")
	task := createTask(t, h, project.ID, "Worker Completion Wired Multi Promotion Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.Prompt = "Run initial worker task"
		tk.AgentID = &agent.ID
	})

	started := make(chan string, 2)
	queuedIDs := make(chan [2]string, 1)
	releasePromoted := make(chan struct{})
	var blockPromotedOnce sync.Once
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		started <- call.Prompt
		if call.Prompt == "Run initial worker task" {
			first := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: call.ExecID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first queued while worker runs"}
			second := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: call.ExecID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "second queued while worker runs"}
			require.NoError(t, h.threadInputRepo.CreateQueued(context.Background(), first))
			require.NoError(t, h.threadInputRepo.CreateQueued(context.Background(), second))
			queuedIDs <- [2]string{first.ID, second.ID}
			return
		}
		if call.Prompt == "first queued while worker runs" {
			blockPromotedOnce.Do(func() { <-releasePromoted })
		}
	}
	h.llmSvc.SetLLMCaller(mock)

	if _, err := h.llmSvc.ExecuteTaskWithAgent(ctx, *task, *agent); err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}
	ids := [2]string{}
	select {
	case ids = <-queuedIDs:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued input fixtures")
	}
	select {
	case got := <-started:
		if got != "Run initial worker task" {
			t.Fatalf("expected original worker prompt first, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for original worker prompt")
	}
	select {
	case got := <-started:
		if got != "first queued while worker runs" {
			t.Fatalf("expected first queued worker follow-up to start, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first queued worker follow-up to start")
	}
	first, err := h.threadInputRepo.GetByID(ctx, ids[0])
	require.NoError(t, err)
	second, err := h.threadInputRepo.GetByID(ctx, ids[1])
	require.NoError(t, err)
	if first.InputStatus != models.ThreadInputApplied || second.InputStatus != models.ThreadInputPending {
		t.Fatalf("expected first applied and second pending, got first=%s second=%s", first.InputStatus, second.InputStatus)
	}
	if second.RunExecutionID == "" || second.RunExecutionID != first.RunExecutionID {
		t.Fatalf("expected remaining queued input guard retargeted to promoted execution, first=%#v second=%#v", first, second)
	}
	close(releasePromoted)
	select {
	case got := <-started:
		if got != "second queued while worker runs" {
			t.Fatalf("expected second queued worker follow-up to start, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second queued worker follow-up to start")
	}
}

func TestHandler_PromoteQueuedTaskThreadInput_PromotesAfterWorkerCompletion(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Worker Completion Promotion Project")
	task := createTask(t, h, project.ID, "Worker Completion Promotion Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})
	completed := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "initial worker run"
	})
	queued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: completed.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "queued after worker"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))

	started := make(chan string, 1)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		started <- call.Prompt
	}
	h.llmSvc.SetLLMCaller(mock)

	h.PromoteQueuedTaskThreadInput(task.ID)

	select {
	case got := <-started:
		if got != "queued after worker" {
			t.Fatalf("expected queued worker follow-up to start, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued worker follow-up to start")
	}
	stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
	require.NoError(t, err)
	if stored.InputStatus != models.ThreadInputApplied {
		t.Fatalf("expected queued worker follow-up applied, got %s", stored.InputStatus)
	}
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	if updatedTask.Status != models.StatusQueued || updatedTask.Category != models.CategoryActive {
		t.Fatalf("expected promoted task active/queued, got status=%s category=%s", updatedTask.Status, updatedTask.Category)
	}
}

func TestProcessStreamingResponse_PromotesQueuedTaskThreadInputsFIFO(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Promotion Project")
	task := createTask(t, h, project.ID, "Queued Promotion Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	active := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active"
		ex.IsFollowup = true
	})
	firstQueued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first queued"}
	secondQueued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "second queued"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, firstQueued))
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, secondQueued))

	started := make(chan string, 2)
	release := make(chan struct{})
	var once sync.Once
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		if call.Prompt == "active" {
			return
		}
		started <- call.Prompt
		once.Do(func() { <-release })
	}
	h.llmSvc.SetLLMCaller(mock)

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         active.ID,
		TaskID:         task.ID,
		Message:        "active",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})

	select {
	case got := <-started:
		if got != "first queued" {
			t.Fatalf("expected first queued input to start, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first queued turn to start")
	}
	first, _ := h.threadInputRepo.GetByID(ctx, firstQueued.ID)
	second, _ := h.threadInputRepo.GetByID(ctx, secondQueued.ID)
	if first.InputStatus != models.ThreadInputApplied || second.InputStatus != models.ThreadInputPending {
		t.Fatalf("expected first applied and second pending, got first=%s second=%s", first.InputStatus, second.InputStatus)
	}
	close(release)
	select {
	case got := <-started:
		if got != "second queued" {
			t.Fatalf("expected second queued input to start after first, got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second queued turn to start")
	}
	for i := 0; i < 20; i++ {
		latestSecond, _ := h.threadInputRepo.GetByID(ctx, secondQueued.ID)
		if latestSecond.InputStatus == models.ThreadInputApplied {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	latestSecond, _ := h.threadInputRepo.GetByID(ctx, secondQueued.ID)
	if latestSecond.InputStatus != models.ThreadInputApplied {
		t.Fatalf("expected second queued input to apply, got %s", latestSecond.InputStatus)
	}
}

func TestProcessStreamingResponse_SteeredQueuedInputRunsBeforeRemainingQueuedTurns(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Steered Queue Priority Project")
	task := createTask(t, h, project.ID, "Steered Queue Priority Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	active := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active"
		ex.IsFollowup = true
	})
	firstQueued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "first queued"}
	steeredQueued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "steered queued"}
	thirdQueued := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: active.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, Content: "third queued"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, firstQueued))
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, steeredQueued))
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, thirdQueued))

	calls := make(chan string, 4)
	releasePromoted := make(chan struct{})
	var convertOnce sync.Once
	var blockPromotedOnce sync.Once
	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		calls <- call.Prompt
		if call.Prompt == "active" {
			convertOnce.Do(func() {
				converted, err := h.threadInputRepo.ConvertQueuedToSteering(ctx, steeredQueued.ID, active.ID, active.ID)
				require.NoError(t, err)
				require.NotNil(t, converted)
			})
			return
		}
		if strings.Contains(call.Prompt, "steered queued") {
			return
		}
		blockPromotedOnce.Do(func() { <-releasePromoted })
	}
	h.llmSvc.SetLLMCaller(mock)

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         active.ID,
		TaskID:         task.ID,
		Message:        "active",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})

	var seen []string
	deadline := time.After(2 * time.Second)
	for len(seen) < 3 {
		select {
		case prompt := <-calls:
			seen = append(seen, prompt)
		case <-deadline:
			t.Fatalf("timed out waiting for active steering and first promoted call, saw %#v", seen)
		}
	}
	if seen[0] != "active" || !strings.Contains(seen[1], "steered queued") || seen[2] != "first queued" {
		t.Fatalf("expected steered queued input to run before FIFO promotion, got %#v", seen)
	}
	if strings.Contains(seen[1], "latest user instruction") || strings.Contains(seen[1], "Start the next visible assistant text") {
		t.Fatalf("expected steered queued input without wrapper text, got %q", seen[1])
	}

	steered, err := h.threadInputRepo.GetByID(ctx, steeredQueued.ID)
	require.NoError(t, err)
	first, err := h.threadInputRepo.GetByID(ctx, firstQueued.ID)
	require.NoError(t, err)
	third, err := h.threadInputRepo.GetByID(ctx, thirdQueued.ID)
	require.NoError(t, err)
	if steered.InputStatus != models.ThreadInputApplied || steered.InputMode != models.ThreadInputModeSteering || steered.RunExecutionID != active.ID {
		t.Fatalf("expected steered row applied to active execution, got %#v", steered)
	}
	if first.InputStatus != models.ThreadInputApplied {
		t.Fatalf("expected first queued row to promote after steered turn completed, got %#v", first)
	}
	if third.InputStatus != models.ThreadInputPending || third.InputMode != models.ThreadInputModeQueued {
		t.Fatalf("expected remaining queued row to stay queued until promoted turn completes, got %#v", third)
	}
	close(releasePromoted)
}

func TestStartQueuedChatInputProcessesSavedAttachmentSession(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Chat Attachment Project")
	activeTask := createTask(t, h, project.ID, "Active Chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, activeTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active chat"
	})

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "queued-chat-attachments"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte("queued text attachment"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "diagram.png"), []byte("fake-png"), 0644))

	input := &models.ThreadInput{
		Scope:               models.ThreadInputScopeChat,
		ProjectID:           project.ID,
		RunExecutionID:      activeExec.ID,
		AgentConfigID:       agent.ID,
		InputMode:           models.ThreadInputModeQueued,
		InputStatus:         models.ThreadInputPending,
		Content:             "review queued attachments",
		AttachmentSessionID: sessionID,
		ChatMode:            models.ChatModeOrchestrate,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	h.llmSvc.SetLLMCaller(mock)
	cb := events.NewChatBroadcaster()
	h.SetChatBroadcaster(cb)
	sub, err := cb.Subscribe()
	require.NoError(t, err)
	defer cb.Unsubscribe(sub)

	h.startQueuedChatInput(ctx, *input)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)

	var newMessage events.ChatEvent
	select {
	case newMessage = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for promoted chat new-message event")
	}
	require.Equal(t, events.ChatNewMessage, newMessage.Type)
	require.Equal(t, input.ProjectID, newMessage.ProjectID)
	require.NotEqual(t, input.ID, newMessage.ExecID)
	require.Equal(t, input.ID, newMessage.PendingInputID)
	require.False(t, newMessage.Queued)

	request := mock.LastAgentRequest()
	require.Len(t, request.Attachments, 1)
	require.Equal(t, "diagram.png", request.Attachments[0].FileName)
	require.Contains(t, request.ChatSystemContext, "queued text attachment")
	attachments, err := h.chatAttachmentRepo.ListByExecutionIDs(ctx, []string{request.ExecID})
	require.NoError(t, err)
	require.Len(t, attachments[request.ExecID], 2)
}

func TestStartQueuedEmailChatInputLeavesActionMarkerTextInert(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Email Chat Reply Context Project")
	activeTask := createTask(t, h, project.ID, "Active Email Chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.CreatedVia = models.TaskOriginEmail
	})
	activeExec := createExec(t, h, activeTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active email chat"
	})
	input := &models.ThreadInput{
		Scope:           models.ThreadInputScopeChat,
		ProjectID:       project.ID,
		RunExecutionID:  activeExec.ID,
		AgentConfigID:   agent.ID,
		InputMode:       models.ThreadInputModeQueued,
		InputStatus:     models.ThreadInputPending,
		Content:         "queued email follow-up",
		ChatMode:        models.ChatModeOrchestrate,
		Source:          models.TaskOriginEmail,
		EmailFrom:       "alice@example.com",
		EmailMessageID:  "<msg-queued@example.com>",
		EmailReferences: "<root@example.com>",
		EmailSubject:    "Queued email",
		EmailSessionKey: "email:alice@example.com:<root@example.com>",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	mock := testutil.NewMockLLMCaller()
	mock.Response = `[CREATE_TASK]
{"title":"Task From Queued Email","prompt":"Handle queued email task","category":"backlog"}
[/CREATE_TASK]`
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)
	require.NoError(t, h.execRepo.Complete(ctx, activeExec.ID, models.ExecCompleted, "active done", "", 0, 0))

	h.startQueuedChatInput(ctx, *input)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)

	promotedExecID := mock.LastAgentRequest().ExecID
	require.Eventually(t, func() bool {
		promotedExec, err := h.execRepo.GetByID(ctx, promotedExecID)
		return err == nil && promotedExec != nil && strings.Contains(promotedExec.Output, "[CREATE_TASK]")
	}, 2*time.Second, 25*time.Millisecond)
	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(tasks, func(task models.Task) bool {
		return task.Title == "Task From Queued Email"
	}), "queued Email marker-looking prose must not create a task")
}

func TestStartQueuedEmailChatInputUsesSessionScopedHistory(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Email Chat History Project")

	otherTask := createTask(t, h, project.ID, "Other Email Chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
		tk.CreatedVia = models.TaskOriginEmail
	})
	otherExec := createExec(t, h, otherTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "other thread prompt"
		ex.Output = "other thread output"
	})
	require.NotEmpty(t, otherExec.ID)
	require.NoError(t, h.emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{
		TaskID:          otherTask.ID,
		EmailFrom:       "alice@example.com",
		EmailMessageID:  "<other@example.com>",
		EmailSubject:    "Other thread",
		EmailSessionKey: "email:alice@example.com:<other@example.com>",
	}))

	activeTask := createTask(t, h, project.ID, "Active Email Chat History", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.CreatedVia = models.TaskOriginEmail
	})
	activeExec := createExec(t, h, activeTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "same thread prompt"
		ex.Output = "same thread output"
	})
	require.NoError(t, h.emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{
		TaskID:          activeTask.ID,
		EmailFrom:       "alice@example.com",
		EmailMessageID:  "<root@example.com>",
		EmailSubject:    "Same thread",
		EmailSessionKey: "email:alice@example.com:<root@example.com>",
	}))
	input := &models.ThreadInput{
		Scope:           models.ThreadInputScopeChat,
		ProjectID:       project.ID,
		RunExecutionID:  activeExec.ID,
		AgentConfigID:   agent.ID,
		InputMode:       models.ThreadInputModeQueued,
		InputStatus:     models.ThreadInputPending,
		Content:         "queued same thread",
		ChatMode:        models.ChatModeOrchestrate,
		Source:          models.TaskOriginEmail,
		EmailFrom:       "alice@example.com",
		EmailMessageID:  "<reply@example.com>",
		EmailReferences: "<root@example.com>",
		EmailSubject:    "Same thread",
		EmailSessionKey: "email:alice@example.com:<root@example.com>",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	h.llmSvc.SetLLMCaller(mock)

	h.startQueuedChatInput(ctx, *input)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	history := mock.LastAgentRequest().ChatHistory
	require.Len(t, history, 1)
	require.Equal(t, "same thread prompt", history[0].PromptSent)
	require.NotContains(t, history[0].PromptSent, "other thread")
}

func TestStartQueuedChatInputFallsBackWhenQueuedAgentDeleted(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	queuedAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Queued Deleted Agent"
		a.IsDefault = false
	})
	fallbackAgent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Fallback Agent"
		a.IsDefault = true
	})
	project := createProject(t, h, "Queued Deleted Agent Project")
	activeTask := createTask(t, h, project.ID, "Active Chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &fallbackAgent.ID
	})
	activeExec := createExec(t, h, activeTask.ID, fallbackAgent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active chat"
	})
	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeChat,
		ProjectID:      project.ID,
		RunExecutionID: activeExec.ID,
		AgentConfigID:  queuedAgent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "queued after model deletion",
		ChatMode:       models.ChatModeOrchestrate,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))
	require.NoError(t, llmConfigRepo.Delete(ctx, queuedAgent.ID))
	input.AgentConfigID = ""

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	h.llmSvc.SetLLMCaller(mock)

	h.startQueuedChatInput(ctx, *input)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	request := mock.LastAgentRequest()
	require.Equal(t, fallbackAgent.ID, request.Agent.ID)
	updated, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, updated.InputStatus)
}

func TestQueuedTaskFollowupRoutesMemoryFromFollowupMessageAfterInitialMemoryTask(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "memory task response"
	mock.TextOnly = "memory task response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Followup Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Created Memory Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
		tk.Prompt = "Tell me about repo chat memory for this app."
	})

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	h.workerSvc.Start(ctx)
	t.Cleanup(h.workerSvc.Stop)
	h.workerSvc.Resize(1)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	initialRequest := mock.LastAgentRequest()
	if instructions := initialRequest.ProjectInstructions; !strings.Contains(instructions, "`chat_memory.md`") || strings.Contains(instructions, "`usage_analytics.md`") {
		t.Fatalf("expected initial task turn to select chat_memory.md only, got:\n%s", instructions)
	}
	initialExecs, err := h.execRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotEmpty(t, initialExecs)

	followup := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: initialExecs[0].ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "Now answer using the usage analytics memory file.",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, followup))
	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *followup))
	require.Eventually(t, func() bool { return mock.CallCount() == 2 }, 2*time.Second, 25*time.Millisecond)

	seen := invoker.Seen()
	if len(seen) != 2 || seen[0] != "route_task/recall_memory" || seen[1] != "route_task/recall_memory" {
		t.Fatalf("expected memory recall hook for initial task and queued followup, got %#v", seen)
	}
	followupRequest := mock.LastAgentRequest()
	if chatContext := followupRequest.ChatSystemContext; !strings.Contains(chatContext, "## Selected Memories For This Task") || !strings.Contains(chatContext, "`usage_analytics.md`") || strings.Contains(chatContext, "`chat_memory.md`") {
		t.Fatalf("expected queued followup to route memory from the followup message, got:\n%s", chatContext)
	}
	rt := llmcontracts.RuntimeToolsFromContext(followupRequest.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected queued followup to expose selected memory_view runtime tool, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"usage_analytics.md"}`))
	if err != nil || !handled || isErr || !strings.Contains(out, "Selected usage analytics memory body.") {
		t.Fatalf("expected queued followup memory_view to load usage analytics memory, handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"chat_memory.md"}`))
	if err != nil || !handled || !isErr {
		t.Fatalf("expected previous-turn memory handle to be unauthorized for followup, handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestStartQueuedTaskThreadInputPreparesSelectedMemoryForQueuedFollowup(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "queued followup response"
	mock.TextOnly = "queued followup response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Task Followup Memory Project")
	seedHandlerTestMemoryIndex(t, h, project)
	task := createTask(t, h, project.ID, "Queued Memory Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
		tk.Prompt = "Tell me about realtime front end patterns for this app."
	})
	priorExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = task.Prompt
		ex.Output = "initial answer"
	})
	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: priorExec.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "Tell me about realtime front end patterns for this app",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	store := &chatMemoryHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: false, Enabled: true},
	}}
	invoker := &chatMemoryHookInvoker{}
	h.workerSvc.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)

	seen := invoker.Seen()
	if len(seen) != 1 || seen[0] != "route_task/recall_memory" {
		t.Fatalf("expected route_task memory recall hook for queued task followup, got %#v", seen)
	}
	request := mock.LastAgentRequest()
	if chatContext := request.ChatSystemContext; !strings.Contains(chatContext, "## Selected Memories For This Task") || !strings.Contains(chatContext, "`chat_memory.md`") {
		t.Fatalf("expected selected memory handle in queued task followup provider request, got:\n%s", chatContext)
	}
	assertChatMemoryViewToolAvailable(t, request.Ctx)
	rt := llmcontracts.RuntimeToolsFromContext(request.Ctx)
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"chat_memory.md"}`))
	if err != nil || !handled || isErr || !strings.Contains(out, "Selected chat memory body.") || strings.Contains(out, "available only after") {
		t.Fatalf("expected final queued followup memory_view to load selected memory, handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestResolveTaskThreadExecutionAgentUsesOpenAICodexTaskModelAfterStaleAnthropicRun(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	staleOpus := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Claude Opus 4.8"
		a.Provider = models.ProviderAnthropic
		a.AuthMethod = models.AuthMethodOAuth
		a.Model = "claude-opus-4-8"
		a.IsDefault = false
	})
	codex := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Codex 5.5"
		a.Provider = models.ProviderOpenAI
		a.AuthMethod = models.AuthMethodOAuth
		a.Model = "gpt-5.5"
		a.IsDefault = true
	})
	project := createProject(t, h, "OpenAI Codex Resolver Project")
	task := createTask(t, h, project.ID, "OpenAI Codex Resolver Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusFailed
		tk.AgentID = &codex.ID
	})
	createExec(t, h, task.ID, staleOpus.ID, func(ex *models.Execution) {
		ex.Status = models.ExecFailed
		ex.PromptSent = "failed with expired Anthropic OAuth"
		ex.ErrorMessage = `OAuth token refresh failed for model config "Claude Opus 4.8" (provider=anthropic model=claude-opus-4-8): refresh failed with HTTP 400`
	})

	agent, unstartable, err := h.resolveTaskThreadExecutionAgent(ctx, task)
	require.NoError(t, err)
	require.False(t, unstartable)
	require.NotNil(t, agent)
	require.Equal(t, codex.ID, agent.ID)
	require.Equal(t, models.ProviderOpenAI, agent.Provider)
	require.Equal(t, models.AuthMethodOAuth, agent.AuthMethod)
	require.Equal(t, "gpt-5.5", agent.Model)
	require.NotEqual(t, staleOpus.ID, agent.ID)
}

func TestRetryLatestFailedTaskThreadFollowupUsesCurrentTaskModel(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	staleOpus := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Claude Opus 4.8"
		a.Provider = models.ProviderAnthropic
		a.AuthMethod = models.AuthMethodOAuth
		a.Model = "claude-opus-4-8"
		a.IsDefault = false
	})
	codex := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Codex 5.5"
		a.Provider = models.ProviderTest
		a.Model = "gpt-5.5"
		a.IsDefault = true
	})
	project := createProject(t, h, "Failed Followup Model Switch Project")
	task := createTask(t, h, project.ID, "Failed Followup Model Switch Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusFailed
		tk.AgentID = &codex.ID
	})
	createExec(t, h, task.ID, staleOpus.ID, func(ex *models.Execution) {
		ex.Status = models.ExecFailed
		ex.PromptSent = "retry the failed follow-up"
		ex.ErrorMessage = `OAuth token refresh failed for model config "Claude Opus 4.8" (provider=anthropic model=claude-opus-4-8): refresh failed with HTTP 400`
		ex.IsFollowup = true
	})

	mock := testutil.NewMockLLMCaller()
	mock.Response = "retried with codex"
	mock.TextOnly = "retried with codex"
	h.llmSvc.SetLLMCaller(mock)

	started, err := h.RetryLatestFailedTaskThreadFollowup(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, started)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	require.Equal(t, codex.ID, mock.LastAgentRequest().Agent.ID)
	require.NotEqual(t, staleOpus.ID, mock.LastAgentRequest().Agent.ID)

	execs, err := h.execRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	foundRetry := false
	for _, exec := range execs {
		if exec.PromptSent == "retry the failed follow-up" && exec.ID != "" && exec.Status != models.ExecFailed {
			foundRetry = true
			require.Equal(t, codex.ID, exec.AgentConfigID)
		}
	}
	require.True(t, foundRetry, "expected rerun execution to be recorded with current task model")
}

func TestStartQueuedTaskThreadInputUsesCurrentTaskModelAfterModelChange(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	staleOpus := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Claude Opus 4.8"
		a.Provider = models.ProviderAnthropic
		a.AuthMethod = models.AuthMethodOAuth
		a.Model = "claude-opus-4-8"
		a.IsDefault = false
	})
	codex := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Codex 5.5"
		a.Provider = models.ProviderTest
		a.Model = "gpt-5.5"
		a.IsDefault = true
	})
	project := createProject(t, h, "Queued Followup Model Switch Project")
	task := createTask(t, h, project.ID, "Queued Followup Model Switch Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusFailed
		tk.AgentID = &codex.ID
	})
	failed := createExec(t, h, task.ID, staleOpus.ID, func(ex *models.Execution) {
		ex.Status = models.ExecFailed
		ex.PromptSent = "initial failed run"
		ex.ErrorMessage = `OAuth token refresh failed for model config "Claude Opus 4.8" (provider=anthropic model=claude-opus-4-8): refresh failed with HTTP 400`
	})
	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: failed.ID,
		AgentConfigID:  staleOpus.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "continue after switching to Codex 5.5",
		Source:         models.TaskOriginWeb,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "continued with codex"
	mock.TextOnly = "continued with codex"
	h.llmSvc.SetLLMCaller(mock)

	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	require.Equal(t, codex.ID, mock.LastAgentRequest().Agent.ID)
	require.NotEqual(t, staleOpus.ID, mock.LastAgentRequest().Agent.ID)

	execs, err := h.execRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	foundPromoted := false
	for _, exec := range execs {
		if exec.PromptSent == input.Content {
			foundPromoted = true
			require.Equal(t, codex.ID, exec.AgentConfigID)
		}
	}
	require.True(t, foundPromoted, "expected promoted execution to be recorded with current task model")
}

func TestStartQueuedTaskThreadInputCancelsQueuedInputWhenNoModelAvailable(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Missing Agent Project")
	task := createTask(t, h, project.ID, "Queued Missing Agent Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active task"
		ex.IsFollowup = true
	})
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, RunExecutionID: activeExec.ID, AgentConfigID: agent.ID, InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending, Content: "queued with deleted model"}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))
	agents, err := llmConfigRepo.List(ctx)
	require.NoError(t, err)
	for _, cfg := range agents {
		require.NoError(t, llmConfigRepo.Delete(ctx, cfg.ID))
	}
	input.AgentConfigID = ""

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	h.llmSvc.SetLLMCaller(mock)

	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))
	updated, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputCancelled, updated.InputStatus)
	require.Equal(t, 0, mock.CallCount())
}

func TestStartQueuedTaskThreadInputUsesQueuedChannelReplyContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Task Channel Reply Project")
	task := createTask(t, h, project.ID, "Queued Channel Reply Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active task"
		ex.IsFollowup = true
	})
	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: activeExec.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "queued from slack",
		Source:         models.TaskOriginSlack,
		SlackChannelID: "C1",
		SlackThreadTS:  "1710000000.100000",
		SlackUserID:    "U1",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	var sentChannel, sentThread, sentTitle, sentOutput, sentErr, sentUser string
	h.SetSlackService(&fakeSlackService{taskCompletionFn: func(_ context.Context, channelID, threadTS, taskTitle, output, errMsg, userID string) {
		sentChannel = channelID
		sentThread = threadTS
		sentTitle = taskTitle
		sentOutput = output
		sentErr = errMsg
		sentUser = userID
	}})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "queued task done"
	mock.TextOnly = "queued task done"
	h.llmSvc.SetLLMCaller(mock)

	require.NoError(t, h.execRepo.Complete(ctx, activeExec.ID, models.ExecCompleted, "active done", "", 0, 0))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted))
	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	require.Eventually(t, func() bool { return sentChannel == "C1" }, 2*time.Second, 25*time.Millisecond)
	require.Equal(t, "1710000000.100000", sentThread)
	require.Equal(t, "Queued Channel Reply Task", sentTitle)
	require.Equal(t, "queued task done", sentOutput)
	require.Empty(t, sentErr)
	require.Equal(t, "U1", sentUser)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.NotEqual(t, models.TaskOriginSlack, updatedTask.CreatedVia)
}

func TestEmailChannelSendToTaskQueuesReplyContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Email Send To Task Project")
	task := createTask(t, h, project.ID, "Existing Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	params := streamingResponseParams{
		ProjectID: project.ID,
		TaskID:    task.ID,
		Surface:   chatcontrol.SurfaceEmail,
		ChannelReply: service.ChannelReplyContext{
			Source:          models.TaskOriginEmail,
			EmailFrom:       "alice@example.com",
			EmailMessageID:  "<msg-2@example.com>",
			EmailReferences: "<root@example.com>",
			EmailSubject:    "Follow-up",
			EmailSessionKey: "email:alice@example.com:<root@example.com>",
		},
	}

	out, err := h.executeSendToTaskTool(ctx, params, []byte(`{"task_id":"`+task.ID+`","message":"Continue from email","origin":"email"}`))
	require.NoError(t, err, out)
	pending, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, models.TaskOriginEmail, pending[0].Source)
	require.Equal(t, "alice@example.com", pending[0].EmailFrom)
	require.Equal(t, "<msg-2@example.com>", pending[0].EmailMessageID)
	require.Equal(t, "<root@example.com>", pending[0].EmailReferences)
	require.Equal(t, "Follow-up", pending[0].EmailSubject)
	require.Equal(t, "email:alice@example.com:<root@example.com>", pending[0].EmailSessionKey)
}

func TestEmailChannelSendToTaskDefaultsToReplyContextOrigin(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Email Send To Task Default Origin Project")
	task := createTask(t, h, project.ID, "Existing Default Origin Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	params := streamingResponseParams{
		ProjectID: project.ID,
		TaskID:    task.ID,
		Surface:   chatcontrol.SurfaceEmail,
		ChannelReply: service.ChannelReplyContext{
			Source:          models.TaskOriginEmail,
			EmailFrom:       "alice@example.com",
			EmailMessageID:  "<msg-default@example.com>",
			EmailReferences: "<root@example.com>",
			EmailSubject:    "Default origin",
			EmailSessionKey: "email:alice@example.com:<root@example.com>",
		},
	}

	out, err := h.executeSendToTaskTool(ctx, params, []byte(`{"task_id":"`+task.ID+`","message":"Continue from implicit email origin"}`))
	require.NoError(t, err, out)
	pending, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, models.TaskOriginEmail, pending[0].Source)
	require.Equal(t, "alice@example.com", pending[0].EmailFrom)
	require.Equal(t, "<msg-default@example.com>", pending[0].EmailMessageID)
	require.Equal(t, "<root@example.com>", pending[0].EmailReferences)
	require.Equal(t, "Default origin", pending[0].EmailSubject)
	require.Equal(t, "email:alice@example.com:<root@example.com>", pending[0].EmailSessionKey)
}

func TestStartQueuedTaskThreadInputMovesCompletedTaskBackToActive(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Task Reactivation Project")
	task := createTask(t, h, project.ID, "Queued Reactivated Task", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "previous run"
		ex.IsFollowup = true
	})
	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: activeExec.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "queued follow-up after completion",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	started := make(chan struct{})
	release := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "reactivated task done"
	mock.TextOnly = "reactivated task done"
	mock.OnCall = func(ctx context.Context, _ testutil.MockLLMCall) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	h.llmSvc.SetLLMCaller(mock)
	broadcaster := events.NewBroadcaster()
	h.broadcaster = broadcaster
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)

	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))
	var startEvent events.TaskEvent
	select {
	case startEvent = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued task-thread start event")
	}
	require.Equal(t, events.TaskThreadExecutionStarted, startEvent.Type)
	require.Equal(t, task.ID, startEvent.TaskID)
	require.Equal(t, input.ID, startEvent.PendingInputID)
	require.NotEmpty(t, startEvent.ExecID)
	require.Equal(t, input.Content, startEvent.Message)

	var appliedEvent events.TaskEvent
	select {
	case appliedEvent = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for queued task-thread applied event")
	}
	require.Equal(t, events.TaskThreadInputApplied, appliedEvent.Type)
	require.Equal(t, task.ID, appliedEvent.TaskID)
	require.Equal(t, input.ID, appliedEvent.PendingInputID)
	require.Equal(t, startEvent.ExecID, appliedEvent.ExecID)
	require.Equal(t, input.Content, appliedEvent.Message)

	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, 2*time.Second, 25*time.Millisecond)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryActive, updatedTask.Category)
	close(release)
}

func TestStartQueuedTaskThreadInputFailureUsesQueuedChannelReplyContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Queued Task Failure Reply Project", RepoPath: repoDir}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	task := createTask(t, h, project.ID, "Queued Failure Reply Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	require.NoError(t, err)
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, ".git"), []byte("not a gitdir"), 0644))
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "previous run"
		ex.IsFollowup = true
	})
	input := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: activeExec.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "queued from slack",
		Source:         models.TaskOriginSlack,
		SlackChannelID: "Cfail",
		SlackThreadTS:  "1710000000.200000",
		SlackUserID:    "Ufail",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	var sentChannel, sentThread, sentErr, sentUser string
	h.SetSlackService(&fakeSlackService{taskCompletionFn: func(_ context.Context, channelID, threadTS, taskTitle, output, errMsg, userID string) {
		sentChannel = channelID
		sentThread = threadTS
		sentErr = errMsg
		sentUser = userID
	}})
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted))

	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))
	require.Eventually(t, func() bool { return sentChannel == "Cfail" }, 2*time.Second, 25*time.Millisecond)
	require.Equal(t, "1710000000.200000", sentThread)
	require.Contains(t, sentErr, "could not check worktree status")
	require.Equal(t, "Ufail", sentUser)
}

func TestStartChannelTaskRun_AppliesSwarmChildFollowupRouting(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Channel Swarm Child Direct Followup Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "Channel worker", Prompt: "Update from channel", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	initialParentGeneration := parentCfg.Generation

	exec := createExec(t, h, worker.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "channel worker follow-up"
		ex.IsFollowup = true
	})

	h.StartChannelTaskRun(ctx, service.ChannelTaskRunRequest{
		ExecID:    exec.ID,
		TaskID:    worker.ID,
		ProjectID: project.ID,
		Message:   "channel worker follow-up",
		Agent:     *agent,
		Surface:   "slack",
		ReplyContext: service.ChannelReplyContext{
			Source:         models.TaskOriginSlack,
			SlackChannelID: "Cswarm",
			SlackThreadTS:  "1710000000.900000",
			SlackUserID:    "Uswarm",
		},
	})

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialParentGeneration+1, updatedParentCfg.Generation)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)

	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, updatedParentCfg.Generation, updatedWorkerCfg.RerunGeneration)
	assert.Equal(t, "followup_pending", updatedWorker.SwarmStatus)
}

func TestStartChannelTaskRun_RoutesSwarmChildBeforeSetupFailure(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Channel Swarm Setup Failure Project", RepoPath: repoDir}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "Channel worker", Prompt: "Update from channel", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	initialParentGeneration := parentCfg.Generation

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, worker, repoDir)
	require.NoError(t, err)
	worker.WorktreePath = wtPath
	worker.WorktreeBranch = wtBranch
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, ".git"), []byte("not a gitdir"), 0644))
	require.NoError(t, h.taskRepo.UpdateWorktreeInfo(ctx, worker.ID, wtPath, wtBranch))
	exec := createExec(t, h, worker.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "channel worker follow-up"
		ex.IsFollowup = true
	})

	h.StartChannelTaskRun(ctx, service.ChannelTaskRunRequest{
		ExecID:    exec.ID,
		TaskID:    worker.ID,
		ProjectID: project.ID,
		Message:   "channel worker follow-up",
		Agent:     *agent,
		Surface:   "slack",
		ReplyContext: service.ChannelReplyContext{
			Source:         models.TaskOriginSlack,
			SlackChannelID: "Cswarmfail",
			SlackThreadTS:  "1710000000.910000",
			SlackUserID:    "Uswarmfail",
		},
	})

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialParentGeneration+1, updatedParentCfg.Generation)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)
	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, updatedParentCfg.Generation, updatedWorkerCfg.RerunGeneration)
	assert.Equal(t, "followup_failed", updatedWorker.SwarmStatus)
	require.Eventually(t, func() bool {
		failedExec, err := h.execRepo.GetByID(ctx, exec.ID)
		return err == nil && failedExec != nil && failedExec.Status == models.ExecFailed
	}, 2*time.Second, 25*time.Millisecond)
}

func TestStartChannelTaskRunIncludesTaskGoalContext(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	agent := createAgent(t, tc.llmConfigRepo)
	project := createProject(t, tc.handler, "Channel Goal Context Project")
	task := createTask(t, tc.handler, project.ID, "Channel Goal Context Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})
	goalObjective := "Channel follow-ups must see this persisted goal"
	goal, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, goalObjective, service.GoalOptions{})
	require.NoError(t, err)
	require.NoError(t, tc.handler.taskGoalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID))
	exec := createExec(t, tc.handler, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "channel follow-up"
		ex.IsFollowup = true
	})
	agentDef := &models.Agent{Tools: []string{"mark_task_goal_achieved"}}
	mock := testutil.NewMockLLMCaller()
	mock.Response = "channel done"
	mock.TextOnly = "channel done"
	tc.handler.llmSvc.SetLLMCaller(mock)

	tc.handler.StartChannelTaskRun(ctx, service.ChannelTaskRunRequest{
		ExecID:          exec.ID,
		TaskID:          task.ID,
		ProjectID:       project.ID,
		Message:         "channel follow-up",
		Agent:           *agent,
		AgentDefinition: agentDef,
		SystemContext:   "Channel task context.",
		Surface:         "slack",
		ReplyContext: service.ChannelReplyContext{
			Source:         models.TaskOriginSlack,
			SlackChannelID: "Cgoal",
			SlackThreadTS:  "1710000000.400000",
			SlackUserID:    "Ugoal",
		},
	})
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	chatCtx := mock.LastAgentRequest().ChatSystemContext
	require.Contains(t, chatCtx, "Channel task context.")
	resumed, err := tc.handler.taskGoalSvc.GetGoal(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, resumed)
	require.Equal(t, goal.GoalID, resumed.GoalID)
	require.Equal(t, models.TaskGoalStatusActive, resumed.Status)
	require.Equal(t, "resumed by slack", resumed.Reason)
	require.Contains(t, chatCtx, "Task goal (active):")
	require.Contains(t, chatCtx, goalObjective)
	require.Contains(t, chatCtx, "This assigned agent is explicitly granted these goal status tools: mark_task_goal_achieved")
	require.NotContains(t, chatCtx, "report_task_goal_blocked. Use only the granted")
}

func TestStartChannelTaskRunSetupFailureUsesReplyContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Immediate Channel Failure Reply Project", RepoPath: repoDir}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	task := createTask(t, h, project.ID, "Immediate Failure Reply Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	require.NoError(t, err)
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch
	require.NoError(t, os.WriteFile(filepath.Join(wtPath, ".git"), []byte("not a gitdir"), 0644))
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "channel follow-up"
		ex.IsFollowup = true
	})
	queued := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		Content:        "queued after channel setup failure",
		Source:         models.TaskOriginSlack,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, queued))

	var sentChannel, sentThread, sentErr, sentUser string
	h.SetSlackService(&fakeSlackService{taskCompletionFn: func(_ context.Context, channelID, threadTS, taskTitle, output, errMsg, userID string) {
		sentChannel = channelID
		sentThread = threadTS
		sentErr = errMsg
		sentUser = userID
	}})

	h.StartChannelTaskRun(ctx, service.ChannelTaskRunRequest{
		ExecID:    exec.ID,
		TaskID:    task.ID,
		ProjectID: project.ID,
		Message:   "channel follow-up",
		Agent:     *agent,
		Surface:   "slack",
		ReplyContext: service.ChannelReplyContext{
			Source:         models.TaskOriginSlack,
			SlackChannelID: "Cimmediate",
			SlackThreadTS:  "1710000000.300000",
			SlackUserID:    "Uimmediate",
		},
	})
	require.Eventually(t, func() bool { return sentChannel == "Cimmediate" }, 2*time.Second, 25*time.Millisecond)
	require.Equal(t, "1710000000.300000", sentThread)
	require.Contains(t, sentErr, "could not check worktree status")
	require.Equal(t, "Uimmediate", sentUser)
	require.Eventually(t, func() bool {
		stored, err := h.threadInputRepo.GetByID(ctx, queued.ID)
		return err == nil && stored != nil && stored.InputStatus == models.ThreadInputApplied
	}, 2*time.Second, 25*time.Millisecond, "queued channel follow-up should be promoted after setup failure")
}

func TestStartQueuedTaskThreadInputProcessesSavedAttachmentSession(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Task Attachment Project")
	task := createTask(t, h, project.ID, "Queued Attachment Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active task"
		ex.IsFollowup = true
	})

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	sessionID := "queued-task-attachments"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "instructions.txt"), []byte("queued task attachment"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "screen.png"), []byte("fake-png"), 0644))

	input := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           project.ID,
		TaskID:              task.ID,
		RunExecutionID:      activeExec.ID,
		AgentConfigID:       agent.ID,
		InputMode:           models.ThreadInputModeQueued,
		InputStatus:         models.ThreadInputPending,
		Content:             "review queued task attachments",
		AttachmentSessionID: sessionID,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "done"
	mock.TextOnly = "done"
	h.llmSvc.SetLLMCaller(mock)
	broadcaster := events.NewBroadcaster()
	h.broadcaster = broadcaster
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)

	require.NoError(t, h.execRepo.Complete(ctx, activeExec.ID, models.ExecCompleted, "active done", "", 0, 0))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted))
	require.NoError(t, h.startQueuedTaskThreadInput(ctx, *input))

	var started events.TaskEvent
	select {
	case started = <-sub:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for promoted task-thread event")
	}
	require.Equal(t, events.TaskThreadExecutionStarted, started.Type)
	require.Equal(t, input.ID, started.PendingInputID)
	require.NotEmpty(t, started.ExecID)

	fragment := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/thread/executions/"+started.ExecID+"/fragment", nil)
	c := echo.New().NewContext(req, fragment)
	c.SetParamNames("taskId", "execId")
	c.SetParamValues(task.ID, started.ExecID)
	require.NoError(t, h.GetTaskThreadExecutionFragment(c))
	assert.Equal(t, http.StatusOK, fragment.Code)
	assert.Contains(t, fragment.Body.String(), "review queued task attachments")
	assert.Contains(t, fragment.Body.String(), "screen.png")
	assert.Contains(t, fragment.Body.String(), "/chat/attachments/")

	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)
	request := mock.LastAgentRequest()
	require.Len(t, request.Attachments, 1)
	require.Equal(t, "screen.png", request.Attachments[0].FileName)
	require.Contains(t, request.ChatSystemContext, "queued task attachment")
	attachments, err := h.chatAttachmentRepo.ListByExecutionIDs(ctx, []string{request.ExecID})
	require.NoError(t, err)
	require.Len(t, attachments[request.ExecID], 2)
}

func TestProcessStreamingResponse_AppliesPendingSteeringBeforeModelCall(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "handled steering"
	mock.TextOnly = "handled steering"
	var exec *models.Execution
	var steering *models.ThreadInput
	broadcaster := events.NewBroadcaster()
	h.broadcaster = broadcaster
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)
	mock.OnCall = func(_ context.Context, _ testutil.MockLLMCall) {
		select {
		case event := <-sub:
			require.Equal(t, events.TaskThreadInputApplied, event.Type)
			require.Equal(t, exec.ID, event.ExecID)
			require.Equal(t, steering.ID, event.PendingInputID)
		default:
			t.Fatal("expected pending steering removal event before provider call")
		}
		stored, err := h.threadInputRepo.GetByID(ctx, steering.ID)
		require.NoError(t, err)
		require.Equal(t, models.ThreadInputPending, stored.InputStatus)
	}
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Steering Project")
	task := createTask(t, h, project.ID, "Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec = createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering = &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "do not change the public API",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "active prompt",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})

	if mock.CallCount() != 1 {
		t.Fatalf("expected one model call, got %d", mock.CallCount())
	}
	request := mock.LastAgentRequest()
	if request.Message == "" {
		t.Fatalf("expected steering request to keep a non-empty current user turn")
	}
	if !strings.Contains(request.Message, "active prompt") || !strings.Contains(request.Message, "do not change the public API") {
		t.Fatalf("expected current request message to combine active prompt and steering, got %q", request.Message)
	}
	if strings.Contains(request.Message, "latest user instruction") || strings.Contains(request.Message, "Start the next visible assistant text") {
		t.Fatalf("expected steering without wrapper text, got %q", request.Message)
	}
	if got, ok := llmcontracts.LifecycleCompletionUserMessageFromContext(request.Ctx); !ok || got != "do not change the public API" {
		t.Fatalf("lifecycle completion user message = %q, %v; want latest steering", got, ok)
	}
	for _, turn := range request.ChatHistory {
		if turn.PromptSent == "do not change the public API" {
			t.Fatalf("steering should be delivered as the current provider message, not trailing user-only history: %#v", request.ChatHistory)
		}
	}
	applied, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	if applied.InputStatus != models.ThreadInputApplied {
		t.Fatalf("expected steering input applied, got %s", applied.InputStatus)
	}
}

func TestPreparePendingSteeringInputsPreservesCurrentReasoningContent(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Reasoning Steering Project")
	task := createTask(t, h, project.ID, "Reasoning Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	require.NoError(t, h.execRepo.UpdateReasoningContent(ctx, exec.ID, "first private reasoning plus detail"))

	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "continue with this constraint",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))

	params := streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "active prompt",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
		ChatHistory: []models.Execution{{
			ID:               exec.ID + "-steering-context-1",
			PromptSent:       "first prompt",
			Output:           "first answer",
			ReasoningContent: "first private reasoning",
		}},
	}
	batch, err := h.preparePendingSteeringInputs(ctx, &params, "assistant answer")
	require.NoError(t, err)
	require.Equal(t, 1, batch.count())
	require.Len(t, params.ChatHistory, 2)
	require.Equal(t, "assistant answer", params.ChatHistory[1].Output)
	require.Equal(t, "first private reasoning plus detail", params.ChatHistory[1].ReasoningContent)

	require.NoError(t, h.execRepo.UpdateReasoningContent(ctx, exec.ID, "final private reasoning"))
	require.NoError(t, h.persistSteeringReplayHistory(ctx, params, "assistant answerfinal answer"))
	stored, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, "first private reasoningfirst private reasoning plus detailfinal private reasoning", stored.ReasoningContent)

	replay, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Equal(t, []models.ExecutionReplayMessage{
		{
			UserContent:      "first prompt",
			AssistantContent: "first answer",
			ReasoningContent: "first private reasoning",
		},
		{
			UserContent:      "active prompt",
			AssistantContent: "assistant answer",
			ReasoningContent: "first private reasoning plus detail",
		},
		{
			UserContent:      formatSteeringInstruction("continue with this constraint"),
			AssistantContent: "final answer",
			ReasoningContent: "final private reasoning",
		},
	}, replay[exec.ID])

	lateSteering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "late completion constraint",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, lateSteering, exec.ID))

	batch, err = h.preparePendingSteeringInputsFromPersistedReplay(ctx, &params, "assistant answerfinal answer")
	require.NoError(t, err)
	require.Equal(t, 1, batch.count())
	require.Len(t, params.ChatHistory, 1)
	require.Equal(t, replay[exec.ID], params.ChatHistory[0].ReplayMessages)
	require.Equal(t, formatSteeringInstruction("late completion constraint"), params.Message)
}

func TestClaimPendingTextSteeringInputsSkipsAttachmentSteering(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Text Boundary Steering Attachment Project")
	task := createTask(t, h, project.ID, "Text Boundary Steering Attachment Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})

	textSteer := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "3+2=?",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, textSteer, exec.ID))
	attachmentSteer := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           project.ID,
		TaskID:              task.ID,
		RunExecutionID:      exec.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              exec.ID,
		ExpectedTurnID:      exec.ID,
		Content:             "also inspect this screenshot",
		AttachmentSessionID: "attach-session-1",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, attachmentSteer, exec.ID))

	params := streamingResponseParams{ExecID: exec.ID, TaskID: task.ID, ProjectID: project.ID, Agent: *agent, IsTaskFollowup: true}
	batch, err := h.claimPendingTextSteeringInputs(ctx, &params)
	require.NoError(t, err)
	if batch.count() != 1 || batch.inputs[0].ID != textSteer.ID {
		t.Fatalf("prepared text-only batch = %#v, want only text steer %s", batch.inputs, textSteer.ID)
	}

	textStored, err := h.threadInputRepo.GetByID(ctx, textSteer.ID)
	require.NoError(t, err)
	if textStored.ExpectedTurnID != "" {
		t.Fatalf("text steer expected_turn_id = %q, want prepared empty", textStored.ExpectedTurnID)
	}
	attachmentStored, err := h.threadInputRepo.GetByID(ctx, attachmentSteer.ID)
	require.NoError(t, err)
	if attachmentStored.ExpectedTurnID != exec.ID || attachmentStored.InputStatus != models.ThreadInputPending {
		t.Fatalf("attachment steer state = status %s expected_turn_id %q, want pending/unprepared", attachmentStored.InputStatus, attachmentStored.ExpectedTurnID)
	}
}

func TestProcessStreamingResponse_CancelledContextUpdatesTaskStatus(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "partial before cancel"
	mock.TextOnly = "partial before cancel"
	mock.Err = context.Canceled
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Cancelled Shared Runner Project")
	task := createTask(t, h, project.ID, "Cancelled Shared Runner Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "active prompt",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})

	updatedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecCancelled, updatedExec.Status)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCancelled, updatedTask.Status)
	require.Equal(t, models.CategoryBacklog, updatedTask.Category)
}

func TestProcessStreamingResponse_DoesNotApplyPreparedSteeringWhenProviderCallFails(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "partial output"
	mock.TextOnly = "partial output"
	mock.Err = errors.New("provider failed")
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Prepared Failed Steering Project")
	task := createTask(t, h, project.ID, "Prepared Failed Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "retry this steering",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, mock.CallCount())
	require.Contains(t, mock.LastCall().Prompt, "retry this steering")
	require.NotContains(t, mock.LastCall().Prompt, "latest user instruction")
	require.NotContains(t, mock.LastCall().Prompt, "Start the next visible assistant text")
	stillPending, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, stillPending)
	require.Equal(t, models.ThreadInputPending, stillPending.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, stillPending.InputMode)
}

func TestProcessStreamingResponse_RequeuesPendingSteeringWithCancelledContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Cancelled Cleanup Steering Project")
	task := createTask(t, h, project.ID, "Cancelled Cleanup Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "recover despite cancelled request context",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
	prepared, err := h.threadInputRepo.PreparePendingTextSteering(ctx, exec.ID, exec.ID)
	require.NoError(t, err)
	require.Len(t, prepared, 1)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	h.requeuePendingSteeringForExecution(cancelledCtx, exec.ID)

	requeued, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, requeued)
	require.Equal(t, models.ThreadInputPending, requeued.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, requeued.InputMode)
	require.Empty(t, requeued.TurnID)
	require.Empty(t, requeued.ExpectedTurnID)
}

func TestProcessStreamingResponse_RequeuesUncommittedSteeringWithCancelledContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Cancelled Uncommitted Steering Project")
	task := createTask(t, h, project.ID, "Cancelled Uncommitted Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "recover uncommitted despite cancelled request context",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
	prepared, err := h.threadInputRepo.PreparePendingTextSteering(ctx, exec.ID, exec.ID)
	require.NoError(t, err)
	require.Len(t, prepared, 1)

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	h.requeueUncommittedSteering(cancelledCtx, exec.ID, preparedSteeringBatch{inputs: prepared})

	requeued, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, requeued)
	require.Equal(t, models.ThreadInputPending, requeued.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, requeued.InputMode)
	require.Empty(t, requeued.TurnID)
	require.Empty(t, requeued.ExpectedTurnID)
}

func TestProcessStreamingResponse_AppliesPreparedSteeringAfterSuccessfulProviderCall(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "steered output"
	mock.TextOnly = "steered output"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Prepared Successful Steering Project")
	task := createTask(t, h, project.ID, "Prepared Successful Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "apply this steering",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "active prompt",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})

	require.Equal(t, 1, mock.CallCount())
	require.Contains(t, mock.LastCall().Prompt, "apply this steering")
	require.NotContains(t, mock.LastCall().Prompt, "latest user instruction")
	require.NotContains(t, mock.LastCall().Prompt, "Start the next visible assistant text")
	applied, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, applied)
	require.Equal(t, models.ThreadInputApplied, applied.InputStatus)
}

func TestProcessStreamingResponse_RequeuesLateAttachmentSteeringInsteadOfCommittingToCompletedChatTurn(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.workerSvc = nil
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalUploadsDir := uploadsDir
	uploadsDir = tmpDir
	t.Cleanup(func() { uploadsDir = originalUploadsDir })
	project := createProject(t, h, "Late Chat Steering Attachment Project")
	sessionID := "late-chat-steering-session"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	var exec *models.Execution
	var steeringID string
	var createLateSteering sync.Once
	var providerCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		createLateSteering.Do(func() {
			require.NotNil(t, exec)
			require.NoError(t, os.MkdirAll(pendingDir, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "screen.png"), []byte("fake-png"), 0644))
			steering := &models.ThreadInput{
				Scope:               models.ThreadInputScopeChat,
				ProjectID:           project.ID,
				RunExecutionID:      exec.ID,
				InputMode:           models.ThreadInputModeSteering,
				InputStatus:         models.ThreadInputPending,
				TurnID:              exec.ID,
				ExpectedTurnID:      exec.ID,
				Content:             "what is in the image?",
				AttachmentSessionID: sessionID,
			}
			require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
			steeringID = steering.ID
		})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"cow story complete\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":20,\"completion_tokens\":17,\"total_tokens\":37}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer server.Close()
	agent := createAgent(t, llmConfigRepo, func(a *models.LLMConfig) {
		a.Provider = models.ProviderOpenAICompatible
		a.AuthMethod = models.AuthMethodAPIKey
		a.APIKey = "test-key"
		a.BaseURL = server.URL + "/v1/"
		a.Transport = "chat_completions"
		a.PresetSlug = "vllm"
	})
	activeTask := createTask(t, h, project.ID, "Late Chat Steering Attachment Task", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec = createExec(t, h, activeTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "tell me a story about a cow"
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      activeTask.ID,
		Message:                     "tell me a story about a cow",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, providerCalls, "late attachment steering must not trigger a continuation on the completed turn")
	chatAttachments, err := h.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	require.NoError(t, err)
	require.Empty(t, chatAttachments, "late steering attachments must not be published on the original user turn")
	requeued, err := h.threadInputRepo.GetByID(ctx, steeringID)
	require.NoError(t, err)
	require.NotNil(t, requeued)
	require.Equal(t, models.ThreadInputPending, requeued.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, requeued.InputMode)
	require.Empty(t, requeued.TurnID)
	require.Empty(t, requeued.ExpectedTurnID)
	require.Equal(t, sessionID, requeued.AttachmentSessionID)
	require.FileExists(t, filepath.Join(pendingDir, "screen.png"))

	var usageCount, totalTokens int
	var usageStatus, operation string
	err = db.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(MAX(total_tokens), 0), COALESCE(MAX(status), ''), COALESCE(MAX(operation), '')
		FROM llm_usage_events
		WHERE execution_id = ?`, exec.ID).Scan(&usageCount, &totalTokens, &usageStatus, &operation)
	require.NoError(t, err)
	require.Equal(t, 1, usageCount, "late attachment-steering completion must still record provider usage")
	require.Equal(t, 37, totalTokens)
	require.Equal(t, string(models.ExecCompleted), usageStatus)
	require.Equal(t, string(llmcontracts.OperationStreaming), operation)
}

func TestProcessStreamingResponse_SendsAndCommitsPreparedSteeringAttachmentsAfterSuccessfulProviderCall(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalUploadsDir := uploadsDir
	uploadsDir = tmpDir
	t.Cleanup(func() { uploadsDir = originalUploadsDir })
	mock := testutil.NewMockLLMCaller()
	mock.Response = "model used steering attachments"
	mock.TextOnly = "model used steering attachments"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Prepared Successful Steering Attachments Project")
	task := createTask(t, h, project.ID, "Prepared Successful Steering Attachments Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	sessionID := "steering-success-session"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte("steering notes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "screen.png"), []byte("fake-png"), 0644))
	largeContent := strings.Repeat("x", maxTextAttachmentSize+1)
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "large.txt"), []byte(largeContent), 0644))
	steering := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           project.ID,
		TaskID:              task.ID,
		RunExecutionID:      exec.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              exec.ID,
		ExpectedTurnID:      exec.ID,
		Content:             "use attached steering files",
		AttachmentSessionID: sessionID,
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, mock.CallCount())
	lastCall := mock.LastCall()
	require.Contains(t, lastCall.Prompt, "use attached steering files")
	require.Contains(t, lastCall.Prompt, "steering notes")
	require.Contains(t, lastCall.Prompt, "large.txt (attached")
	require.NotContains(t, lastCall.Prompt, largeContent[:50])
	require.Len(t, lastCall.Attachments, 1)
	require.Equal(t, filepath.Join(pendingDir, "screen.png"), lastCall.Attachments[0].FilePath)
	applied, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, applied)
	require.Equal(t, models.ThreadInputApplied, applied.InputStatus)
	chatAttachments, err := h.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	require.NoError(t, err)
	require.Len(t, chatAttachments, 3)
	seen := map[string]string{}
	for _, att := range chatAttachments {
		seen[att.FileName] = att.FilePath
		require.FileExists(t, att.FilePath)
	}
	require.Contains(t, seen, "notes.txt")
	require.Contains(t, seen, "screen.png")
	require.Contains(t, seen, "large.txt")
	require.NoDirExists(t, pendingDir)
}

func TestProcessStreamingResponse_DoesNotMovePreparedSteeringAttachmentsWhenProviderCallFails(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalUploadsDir := uploadsDir
	uploadsDir = tmpDir
	t.Cleanup(func() { uploadsDir = originalUploadsDir })
	mock := testutil.NewMockLLMCaller()
	mock.Response = "partial output"
	mock.TextOnly = "partial output"
	mock.Err = errors.New("provider failed")
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Prepared Failed Steering Attachments Project")
	task := createTask(t, h, project.ID, "Prepared Failed Steering Attachments Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	sessionID := "steering-session"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte("steering notes"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "screen.png"), []byte("fake-png"), 0644))
	steering := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           project.ID,
		TaskID:              task.ID,
		RunExecutionID:      exec.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              exec.ID,
		ExpectedTurnID:      exec.ID,
		Content:             "use attached steering files",
		AttachmentSessionID: sessionID,
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, mock.CallCount())
	require.Contains(t, mock.LastCall().Prompt, "steering notes")
	require.Len(t, mock.LastCall().Attachments, 1)
	require.Equal(t, filepath.Join(pendingDir, "screen.png"), mock.LastCall().Attachments[0].FilePath)
	stillPending, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, stillPending)
	require.Equal(t, models.ThreadInputPending, stillPending.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, stillPending.InputMode)
	require.FileExists(t, filepath.Join(pendingDir, "notes.txt"))
	require.FileExists(t, filepath.Join(pendingDir, "screen.png"))
}

func TestProcessStreamingResponse_RequeuesPreparedSteeringWhenCommitFailsAfterProviderSuccess(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalUploadsDir := uploadsDir
	uploadsDir = tmpDir
	t.Cleanup(func() { uploadsDir = originalUploadsDir })
	mock := testutil.NewMockLLMCaller()
	mock.Response = "model used steering"
	mock.TextOnly = "model used steering"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Prepared Commit Failed Steering Project")
	task := createTask(t, h, project.ID, "Prepared Commit Failed Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	sessionID := "steering-commit-fails"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte("steering notes"), 0644))
	h.chatAttachmentRepo = nil
	steering := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           project.ID,
		TaskID:              task.ID,
		RunExecutionID:      exec.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              exec.ID,
		ExpectedTurnID:      exec.ID,
		Content:             "commit should not strand me",
		AttachmentSessionID: sessionID,
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, mock.CallCount())
	require.Contains(t, mock.LastCall().Prompt, "commit should not strand me")
	requeued, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, requeued)
	require.Equal(t, models.ThreadInputPending, requeued.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, requeued.InputMode)
	require.Empty(t, requeued.TurnID)
}

func TestProcessStreamingResponse_RequeuesOnlyUncommittedSteeringWhenLaterCommitFails(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	tmpDir := t.TempDir()
	originalUploadsDir := uploadsDir
	uploadsDir = tmpDir
	t.Cleanup(func() { uploadsDir = originalUploadsDir })
	mock := testutil.NewMockLLMCaller()
	mock.Response = "model used steering"
	mock.TextOnly = "model used steering"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Mixed Commit Failed Steering Project")
	task := createTask(t, h, project.ID, "Mixed Commit Failed Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	first := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "first steering commits",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, first, exec.ID))
	sessionID := "mixed-steering-commit-fails"
	pendingDir := filepath.Join(tmpDir, "chat", "pending", sessionID)
	require.NoError(t, os.MkdirAll(pendingDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pendingDir, "notes.txt"), []byte("second notes"), 0644))
	second := &models.ThreadInput{
		Scope:               models.ThreadInputScopeTask,
		ProjectID:           project.ID,
		TaskID:              task.ID,
		RunExecutionID:      exec.ID,
		InputMode:           models.ThreadInputModeSteering,
		InputStatus:         models.ThreadInputPending,
		TurnID:              exec.ID,
		ExpectedTurnID:      exec.ID,
		Content:             "second steering recovers",
		AttachmentSessionID: sessionID,
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, second, exec.ID))
	h.chatAttachmentRepo = nil

	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, mock.CallCount())
	storedFirst, err := h.threadInputRepo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	require.NotNil(t, storedFirst)
	require.Equal(t, models.ThreadInputApplied, storedFirst.InputStatus)
	require.Equal(t, models.ThreadInputModeSteering, storedFirst.InputMode)
	storedSecond, err := h.threadInputRepo.GetByID(ctx, second.ID)
	require.NoError(t, err)
	require.NotNil(t, storedSecond)
	require.Equal(t, models.ThreadInputPending, storedSecond.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, storedSecond.InputMode)
	require.Empty(t, storedSecond.TurnID)
}

func TestProcessStreamingResponse_DoesNotApplySteeringCreatedDuringFailedModelCall(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "partial output"
	mock.TextOnly = "partial output"
	mock.Err = errors.New("provider failed")
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Failed Steering Project")
	task := createTask(t, h, project.ID, "Failed Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	var steeringID string
	mock.OnCall = func(context.Context, testutil.MockLLMCall) {
		steering := &models.ThreadInput{
			Scope:          models.ThreadInputScopeTask,
			ProjectID:      project.ID,
			TaskID:         task.ID,
			RunExecutionID: exec.ID,
			InputMode:      models.ThreadInputModeSteering,
			InputStatus:    models.ThreadInputPending,
			TurnID:         exec.ID,
			ExpectedTurnID: exec.ID,
			Content:        "steering during failed call",
		}
		require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
		steeringID = steering.ID
	}

	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	require.Equal(t, 1, mock.CallCount())
	require.NotEmpty(t, steeringID)
	steering, err := h.threadInputRepo.GetByID(ctx, steeringID)
	require.NoError(t, err)
	require.NotNil(t, steering)
	require.Equal(t, models.ThreadInputPending, steering.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, steering.InputMode)
	require.Empty(t, steering.TurnID)
}

func TestProcessStreamingResponse_AppliesSteeringCreatedDuringFinalGraceBeforeCompletion(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "initial final response"
	mock.TextOnly = "initial final response"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Late Steering Project")
	task := createTask(t, h, project.ID, "Late Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})

	created := make(chan struct{})
	var once sync.Once
	mock.OnCall = func(context.Context, testutil.MockLLMCall) {
		once.Do(func() {
			go func() {
				time.Sleep(finalSteeringPollInterval)
				steering := &models.ThreadInput{
					Scope:          models.ThreadInputScopeTask,
					ProjectID:      project.ID,
					TaskID:         task.ID,
					RunExecutionID: exec.ID,
					InputMode:      models.ThreadInputModeSteering,
					InputStatus:    models.ThreadInputPending,
					TurnID:         exec.ID,
					ExpectedTurnID: exec.ID,
					Content:        "late steering before completion",
				}
				require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))
				close(created)
			}()
		})
	}

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "active prompt",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})
	<-created

	if mock.CallCount() != 2 {
		t.Fatalf("expected late steering to extend the active turn with a second model call, got %d", mock.CallCount())
	}
	finalRequest := mock.LastAgentRequest()
	if !strings.Contains(finalRequest.Message, "late steering before completion") {
		t.Fatalf("expected late steering in provider request, got %q", finalRequest.Message)
	}
	if strings.Contains(finalRequest.Message, "latest user instruction") || strings.Contains(finalRequest.Message, "Start the next visible assistant text") {
		t.Fatalf("expected late steering without wrapper text, got %q", finalRequest.Message)
	}
}

func TestProcessStreamingResponse_MultipleSteeringBatchesDoNotDuplicateActiveContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "assistant step one"
	mock.TextOnly = "assistant step one"
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Repeated Steering Project")
	task := createTask(t, h, project.ID, "Repeated Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	first := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "first steering",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, first, exec.ID))

	secondCreated := make(chan struct{})
	var once sync.Once
	mock.OnCall = func(context.Context, testutil.MockLLMCall) {
		once.Do(func() {
			second := &models.ThreadInput{
				Scope:          models.ThreadInputScopeTask,
				ProjectID:      project.ID,
				TaskID:         task.ID,
				RunExecutionID: exec.ID,
				InputMode:      models.ThreadInputModeSteering,
				InputStatus:    models.ThreadInputPending,
				TurnID:         exec.ID,
				ExpectedTurnID: exec.ID,
				Content:        "second steering",
			}
			require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, second, exec.ID))
			close(secondCreated)
		})
	}

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "active prompt",
		Agent:          *agent,
		ProjectID:      project.ID,
		IsTaskFollowup: true,
	})
	<-secondCreated

	if mock.CallCount() != 2 {
		t.Fatalf("expected two model calls after second steering, got %d", mock.CallCount())
	}
	requests := mock.AgentRequests
	if len(requests) != 2 {
		t.Fatalf("expected two recorded provider requests, got %d", len(requests))
	}
	firstRequest := requests[0]
	if !strings.Contains(firstRequest.Message, "active prompt") || !strings.Contains(firstRequest.Message, "first steering") {
		t.Fatalf("expected first request to combine active prompt and first steering, got %q", firstRequest.Message)
	}
	finalRequest := requests[1]
	finalHistory := finalRequest.ChatHistory
	var firstTurnOutput string
	var duplicateSecondSteering bool
	for _, turn := range finalHistory {
		if strings.Contains(turn.PromptSent, "first steering") {
			firstTurnOutput = turn.Output
		}
		if turn.PromptSent == "second steering" {
			duplicateSecondSteering = true
		}
	}
	if firstTurnOutput != "assistant step one" {
		t.Fatalf("expected first request output attached before second steering, got %q in %#v", firstTurnOutput, finalHistory)
	}
	if duplicateSecondSteering {
		t.Fatalf("second steering should be delivered as current provider message, not trailing user-only history: %#v", finalHistory)
	}
	if !strings.Contains(finalRequest.Message, "second steering") {
		t.Fatalf("expected second steering as current provider message, got %q", finalRequest.Message)
	}
	if strings.Contains(finalRequest.Message, "latest user instruction") || strings.Contains(finalRequest.Message, "Start the next visible assistant text") {
		t.Fatalf("expected second steering without wrapper text, got %q", finalRequest.Message)
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

func TestProcessStreamingResponse_TaskFollowupIgnoresStatusMarkerMentionInProse(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "The previous output mentioned the literal marker `[STATUS: FAILED | ...]` while explaining the parser.\n\nFollow-up completed."
	mock.TextOnly = mock.Response
	mock.Tokens = 24
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Followup Marker Prose Project")
	task := createTask(t, h, project.ID, "Followup Marker Prose Task", func(tk *models.Task) {
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
	if updatedExec.Status != models.ExecCompleted || updatedExec.ErrorMessage != "" {
		t.Fatalf("expected completed execution without failure metadata, got status=%s error=%q", updatedExec.Status, updatedExec.ErrorMessage)
	}
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed, got %s", updatedTask.Status)
	}
}

func TestProcessStreamingResponse_TaskFollowupIgnoresIncompleteStatusControl(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "Follow-up completed.\n[STATUS: NEEDS_FOLLOWUP | incomplete control"
	mock.TextOnly = mock.Response
	mock.Tokens = 24
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Incomplete Followup Status Project")
	task := createTask(t, h, project.ID, "Incomplete Followup Status Task", func(tk *models.Task) {
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
	if updatedExec.Status != models.ExecCompleted || updatedExec.ErrorMessage != "" || !strings.Contains(updatedExec.Output, "[STATUS: NEEDS_FOLLOWUP | incomplete control") {
		t.Fatalf("expected completed execution preserving incomplete control, got status=%s error=%q output=%q", updatedExec.Status, updatedExec.ErrorMessage, updatedExec.Output)
	}
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed, got %s", updatedTask.Status)
	}
}

func TestProcessStreamingResponse_TaskFollowupIgnoresExtraStatusReasonDelimiter(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "Follow-up completed.\n[STATUS: NEEDS_FOLLOWUP | reason | extra]"
	mock.TextOnly = mock.Response
	mock.Tokens = 24
	h.llmSvc.SetLLMCaller(mock)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Extra Delimiter Followup Status Project")
	task := createTask(t, h, project.ID, "Extra Delimiter Followup Status Task", func(tk *models.Task) {
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
	if updatedExec.Status != models.ExecCompleted || updatedExec.ErrorMessage != "" || !strings.Contains(updatedExec.Output, mock.Response) {
		t.Fatalf("expected completed execution preserving malformed control, got status=%s error=%q output=%q", updatedExec.Status, updatedExec.ErrorMessage, updatedExec.Output)
	}
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.Status != models.StatusCompleted {
		t.Fatalf("expected task completed, got %s", updatedTask.Status)
	}
}

func TestResolveWorktreeWorkDir_ReactivatedConflictContinuesInPreservedWorktree(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Terminal Conflict Followup Project", RepoPath: repoDir}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := createTask(t, h, project.ID, "Terminal unmerged conflict followup", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusRunning
		tk.MergeStatus = models.MergeStatusPending
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	conflictFile := filepath.Join(wtPath, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("task version\n"), 0644); err != nil {
		t.Fatalf("write task conflict file: %v", err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "task conflict version"); err != nil {
		t.Fatalf("commit task conflict version: %v", err)
	}

	mainConflictFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(mainConflictFile, []byte("main version\n"), 0644); err != nil {
		t.Fatalf("write main conflict file: %v", err)
	}
	cmd := exec.Command("git", "add", "conflict.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add main conflict file: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "main conflict version")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit main conflict version: %v\n%s", err, out)
	}

	workDir, worktreeContext, err := h.resolveWorktreeWorkDir(ctx, task)
	if err != nil {
		t.Fatalf("terminal followup should continue in preserved worktree after startup conflict: %v", err)
	}
	if workDir != wtPath {
		t.Fatalf("expected preserved worktree %q, got %q", wtPath, workDir)
	}
	if !strings.Contains(worktreeContext, "Startup sync could not merge") || !strings.Contains(worktreeContext, "merge was aborted") {
		t.Fatalf("expected model-visible startup sync warning, got %q", worktreeContext)
	}
	content, err := os.ReadFile(conflictFile)
	if err != nil {
		t.Fatalf("read conflict file: %v", err)
	}
	if string(content) != "task version\n" {
		t.Fatalf("expected task worktree version preserved, got %q", content)
	}
	statusCmd := exec.Command("git", "status", "--porcelain")
	statusCmd.Dir = wtPath
	out, err := statusCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status after aborted startup conflict: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected startup conflict to be aborted cleanly, status:\n%s", out)
	}
	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updated.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("expected merge_status conflict, got %q", updated.MergeStatus)
	}
}

func TestProcessStreamingResponse_ReactivatedSyncConflictIncludesWorktreeWarningContext(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	mock.Response = "I will inspect the divergence."
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Terminal Conflict Prompt Context Project", RepoPath: repoDir}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	agent := createAgent(t, llmConfigRepo)
	task := createTask(t, h, project.ID, "Terminal conflict prompt context", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusRunning
		tk.MergeStatus = models.MergeStatusPending
		tk.AgentID = &agent.ID
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	conflictFile := filepath.Join(wtPath, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("task version\n"), 0644); err != nil {
		t.Fatalf("write task conflict file: %v", err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "task conflict version"); err != nil {
		t.Fatalf("commit task conflict version: %v", err)
	}

	mainConflictFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(mainConflictFile, []byte("main version\n"), 0644); err != nil {
		t.Fatalf("write main conflict file: %v", err)
	}
	cmd := exec.Command("git", "add", "conflict.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add main conflict file: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "main conflict version")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit main conflict version: %v\n%s", err, out)
	}

	workDir, worktreeContext, err := h.resolveWorktreeWorkDir(ctx, task)
	if err != nil {
		t.Fatalf("terminal followup should continue in preserved worktree after startup conflict: %v", err)
	}
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Please reconcile main"
		ex.IsFollowup = true
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        "Please reconcile main",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  combineContexts(buildThreadSystemContext(task.Title, true, ""), worktreeContext),
		WorkDir:        workDir,
		IsTaskFollowup: true,
	})

	request := mock.LastAgentRequest()
	if !strings.Contains(request.ChatSystemContext, "Startup sync could not merge") {
		t.Fatalf("expected provider system context to include startup sync warning, got %q", request.ChatSystemContext)
	}
	if !strings.Contains(request.ChatSystemContext, "merge was aborted") || !strings.Contains(request.ChatSystemContext, "run the merge") || !strings.Contains(request.ChatSystemContext, "build, test, and commit") {
		t.Fatalf("expected provider system context to tell agent how to recover, got %q", request.ChatSystemContext)
	}
}

func TestResolveWorktreeWorkDir_UnbornRepoFailsClosed(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()
	repoDir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	project := &models.Project{Name: "Unborn Followup Project", RepoPath: repoDir}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := createTask(t, h, project.ID, "Unborn followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
	})
	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)

	workDir, _, err := h.resolveWorktreeWorkDir(ctx, task)
	if err == nil || !strings.Contains(err.Error(), "repository has no commit for worktree base") {
		t.Fatalf("expected missing base commit error, got workDir=%q err=%v", workDir, err)
	}
	if workDir != "" {
		t.Fatalf("expected no fallback to main checkout, got %q", workDir)
	}
}

func TestResolveWorktreeWorkDir_SyncsExistingWorktreeFromTargetBeforeFollowup(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Stale Followup Project", RepoPath: repoDir}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := createTask(t, h, project.ID, "Stale worktree followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, wtBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	mainOnlyPath := filepath.Join(repoDir, "main-only.txt")
	if err := os.WriteFile(mainOnlyPath, []byte("new main change\n"), 0644); err != nil {
		t.Fatalf("write main-only file: %v", err)
	}
	cmd := exec.Command("git", "add", "main-only.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add main-only: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "main advanced")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit main-only: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(wtPath, "main-only.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected stale worktree to not have main-only.txt before followup sync, stat err=%v", err)
	}

	workDir, worktreeContext, err := h.resolveWorktreeWorkDir(ctx, task)
	if err != nil {
		t.Fatalf("resolve worktree workdir: %v", err)
	}
	if workDir != wtPath {
		t.Fatalf("expected workDir %q, got %q", wtPath, workDir)
	}
	if worktreeContext != "" {
		t.Fatalf("expected no worktree warning context, got %q", worktreeContext)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "main-only.txt")); err != nil {
		t.Fatalf("expected followup worktree sync to include main-only.txt: %v", err)
	}

	defaultBranch := service.GetDefaultBranch(repoDir)
	cmd = exec.Command("git", "merge-base", "--is-ancestor", defaultBranch, "HEAD")
	cmd.Dir = wtPath
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("expected %s to be ancestor of synced worktree HEAD: %v\n%s", defaultBranch, err, out)
	}
}

func TestResolveWorktreeWorkDir_MergedStaleFollowupStartsFromCurrentTarget(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Merged Stale Followup Project", RepoPath: repoDir}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	task := createTask(t, h, project.ID, "Merged stale followup", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.MergeStatus = models.MergeStatusMerged
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	oldPath, oldBranch, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup original worktree: %v", err)
	}
	task.WorktreePath = oldPath
	task.WorktreeBranch = oldBranch

	if err := os.WriteFile(filepath.Join(oldPath, "registry.go"), []byte("package registry\n\nconst value = \"stale task edit\"\n"), 0644); err != nil {
		t.Fatalf("write stale task edit: %v", err)
	}
	if err := service.CommitWorktreeChanges(oldPath, "stale task edit"); err != nil {
		t.Fatalf("commit stale task edit: %v", err)
	}

	mainFile := filepath.Join(repoDir, "registry.go")
	if err := os.WriteFile(mainFile, []byte("package registry\n\nconst value = \"accepted main edit\"\n"), 0644); err != nil {
		t.Fatalf("write accepted main edit: %v", err)
	}
	cmd := exec.Command("git", "add", "registry.go")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add accepted main edit: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "accepted main edit")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit accepted main edit: %v\n%s", err, out)
	}

	workDir, worktreeContext, err := h.resolveWorktreeWorkDir(ctx, task)
	if err != nil {
		t.Fatalf("resolve worktree workdir should skip stale startup merge conflict: %v", err)
	}
	if workDir == oldPath {
		t.Fatalf("expected fresh current-target follow-up worktree, got original stale path %q", workDir)
	}
	if worktreeContext != "" {
		t.Fatalf("expected no worktree warning context for fresh current-target worktree, got %q", worktreeContext)
	}
	if !strings.Contains(task.WorktreeBranch, "-followup-") {
		t.Fatalf("expected follow-up branch, got %q", task.WorktreeBranch)
	}
	if task.MergeTargetBranch != service.GetDefaultBranch(repoDir) {
		t.Fatalf("expected merge target %q, got %q", service.GetDefaultBranch(repoDir), task.MergeTargetBranch)
	}
	content, err := os.ReadFile(filepath.Join(workDir, "registry.go"))
	if err != nil {
		t.Fatalf("read fresh followup file: %v", err)
	}
	if !strings.Contains(string(content), "accepted main edit") || strings.Contains(string(content), "stale task edit") {
		t.Fatalf("expected fresh worktree from current target, got content:\n%s", content)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("expected original stale worktree to remain preserved: %v", err)
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
	h.completeWithSuccess(ctx, exec.ID, task.ID, "output text", "", 100, 5000)

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

func TestCompleteWithSuccess_GitHubSDLCImplementationWithoutPullRequestFailsTask(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	prRepo := repository.NewTaskPullRequestRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	h.SetTaskPullRequestRepo(prRepo)
	h.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)

	agent := &models.LLMConfig{Name: "Test Agent", Provider: models.ProviderTest, Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	project := &models.Project{Name: "GitHub SDLC PR required"}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	_, err := db.ExecContext(ctx, `INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state, created_via)
		VALUES ('github-sdlc-completion-automation', ?, 'github-sdlc-completion', 'GitHub SDLC', 'custom', 'active', 'test')`, project.ID)
	require.NoError(t, err)

	task := &models.Task{ProjectID: project.ID, Title: "Implement GitHub issue #42", Category: models.CategoryActive, Priority: 2, Prompt: "Test", Status: models.StatusRunning, CreatedVia: "automation:github-sdlc-completion-automation:implementation"}
	require.NoError(t, h.taskSvc.Create(ctx, task))
	_, err = db.ExecContext(ctx, `INSERT INTO automation_github_issue_task_provenance
		(project_id, automation_id, task_id, issue_resource_id, implementation_node_key)
		VALUES (?, 'github-sdlc-completion-automation', ?, 'github_issue:openvibely/openvibely:42', 'implementation')`, project.ID, task.ID)
	require.NoError(t, err)

	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "Test"}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	h.completeWithSuccess(ctx, exec.ID, task.ID, "implementation complete", "", 100, 5000)

	completedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecCompleted, completedExec.Status)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusFailed, updatedTask.Status)
	require.Equal(t, models.CategoryBacklog, updatedTask.Category)
}

func TestCompleteWithSuccess_GitHubSDLCImplementationWithPullRequestCompletesTask(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	prRepo := repository.NewTaskPullRequestRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	h.SetTaskPullRequestRepo(prRepo)
	h.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)

	agent := &models.LLMConfig{Name: "Test Agent", Provider: models.ProviderTest, Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	project := &models.Project{Name: "GitHub SDLC PR published"}
	require.NoError(t, h.projectSvc.Create(ctx, project))
	_, err := db.ExecContext(ctx, `INSERT INTO automations (id, project_id, stable_key, name, automation_type, lifecycle_state, created_via)
		VALUES ('github-sdlc-completion-automation', ?, 'github-sdlc-completion', 'GitHub SDLC', 'custom', 'active', 'test')`, project.ID)
	require.NoError(t, err)

	task := &models.Task{ProjectID: project.ID, Title: "Implement GitHub issue #42", Category: models.CategoryActive, Priority: 2, Prompt: "Test", Status: models.StatusRunning, CreatedVia: "automation:github-sdlc-completion-automation:implementation"}
	require.NoError(t, h.taskSvc.Create(ctx, task))
	_, err = db.ExecContext(ctx, `INSERT INTO automation_github_issue_task_provenance
		(project_id, automation_id, task_id, issue_resource_id, implementation_node_key)
		VALUES (?, 'github-sdlc-completion-automation', ?, 'github_issue:openvibely/openvibely:42', 'implementation')`, project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 123, PRURL: "https://github.com/openvibely/openvibely/pull/123", PRState: "open"}))

	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "Test"}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	h.completeWithSuccess(ctx, exec.ID, task.ID, "implementation complete", "", 100, 5000)

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusCompleted, updatedTask.Status)
	require.Equal(t, models.CategoryCompleted, updatedTask.Category)
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

func TestFollowupResetsConflictMergeStatus(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name: "Test Agent", Provider: models.ProviderTest,
		Model: "claude-sonnet-4-5", MaxTokens: 4096, Temperature: 1.0, IsDefault: true,
	}
	if err := llmConfigRepo.Create(ctx, agent); err != nil {
		t.Fatalf("failed to create agent: %v", err)
	}

	repoDir := createHandlerTestGitRepo(t)
	project := &models.Project{Name: "Test Project", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := createTask(t, h, project.ID, "Conflicted Task with Followup", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
		tk.AutoMerge = false
	})

	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo)
	wtPath, _, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}

	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
		t.Fatalf("update merge status to conflict: %v", err)
	}

	followupExec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Resolve and continue"
		ex.IsFollowup = true
	})

	if err := os.WriteFile(filepath.Join(wtPath, "resolved-followup.txt"), []byte("new followup work\n"), 0644); err != nil {
		t.Fatalf("write followup change: %v", err)
	}

	h.completeWithSuccess(ctx, followupExec.ID, task.ID, "done", wtPath, 0, 1)

	updatedExec, err := h.execRepo.GetByID(ctx, followupExec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if updatedExec.Status != models.ExecCompleted {
		t.Fatalf("expected execution completed, got %s (error: %s)", updatedExec.Status, updatedExec.ErrorMessage)
	}
	if updatedExec.DiffOutput == "" {
		t.Fatal("expected diff output to be captured for followup")
	}

	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if updatedTask.MergeStatus != models.MergeStatusPending {
		t.Errorf("expected merge_status=pending after conflict followup with new changes, got %s", updatedTask.MergeStatus)
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

func TestStartPendingTaskThreadFollowup_AlreadyActiveIsHandled(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Already Active Queued Followup Project")
	task := createTask(t, h, project.ID, "Already Active Queued Followup Task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusPending
		task.AgentID = &agent.ID
		task.Prompt = "original prompt"
	})
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued))
	active := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = "queued follow-up"
		exec.IsFollowup = true
	})

	handled, err := h.StartPendingTaskThreadFollowup(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryActive))

	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusQueued, updated.Status)
	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 1)
	assert.Equal(t, active.ID, execs[0].ID)
	select {
	case submitted := <-h.workerSvc.Submitted():
		t.Fatalf("original task was submitted while queued follow-up execution was active: %s", submitted.ID)
	default:
	}
}

func TestRetryLatestFailedTaskThreadFollowup_AlreadyActiveIsHandled(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Already Active Failed Followup Project")
	task := createTask(t, h, project.ID, "Already Active Failed Followup Task", func(task *models.Task) {
		task.Category = models.CategoryBacklog
		task.Status = models.StatusFailed
		task.AgentID = &agent.ID
		task.Prompt = "original prompt"
	})
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusQueued))
	failed := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecFailed
		exec.PromptSent = "failed follow-up"
		exec.IsFollowup = true
	})
	active := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) {
		exec.Status = models.ExecRunning
		exec.PromptSent = failed.PromptSent
		exec.IsFollowup = true
	})

	handled, err := h.RetryLatestFailedTaskThreadFollowup(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, handled)
	require.NoError(t, h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryActive))

	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusQueued, updated.Status)
	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 2)
	assert.Equal(t, active.ID, execs[1].ID)
	select {
	case submitted := <-h.workerSvc.Submitted():
		t.Fatalf("original task was submitted while failed follow-up retry execution was active: %s", submitted.ID)
	default:
	}
}

func TestRetryLatestFailedTaskThreadFollowup_RoutesUnroutedSwarmChildRetry(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Retry Unrouted Swarm Followup Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "Retry worker", Prompt: "Update retry path", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)
	parentCfg, err := models.ParseSwarmConfig(parent.SwarmConfig)
	require.NoError(t, err)
	initialParentGeneration := parentCfg.Generation
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusFailed))
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, worker.ID, models.CategoryBacklog))
	failedFollowup := createExec(t, h, worker.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "retry the unrouted swarm worker follow-up"
		ex.IsFollowup = true
	})
	require.NoError(t, h.execRepo.Complete(ctx, failedFollowup.ID, models.ExecFailed, "setup failed before routing", "worktree setup failed", 0, 20))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "retry complete"
	mock.TextOnly = "retry complete"
	h.llmSvc.SetLLMCaller(mock)

	handled, err := h.RetryLatestFailedTaskThreadFollowup(ctx, worker.ID)
	require.NoError(t, err)
	require.True(t, handled)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, time.Second, 10*time.Millisecond)

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, initialParentGeneration+1, updatedParentCfg.Generation)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)
	updatedWorker, err := h.taskRepo.GetByID(ctx, worker.ID)
	require.NoError(t, err)
	updatedWorkerCfg, err := models.ParseSwarmConfig(updatedWorker.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, updatedParentCfg.Generation, updatedWorkerCfg.RerunGeneration)
}

func TestRetryLatestFailedTaskThreadFollowup_ReactivatesParentForAlreadyRoutedSwarmChild(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Retry Routed Swarm Followup Project")
	parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{
		ProjectID:       project.ID,
		Title:           "Swarm parent",
		Prompt:          "Build the swarm result",
		Category:        models.CategoryActive,
		Priority:        2,
		AgentID:         &agent.ID,
		MaxWorkers:      1,
		WorkerIsolation: "worktree",
		ReviewerEnabled: true,
		MergerEnabled:   true,
	})
	require.NoError(t, err)
	planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	require.NoError(t, err)
	require.NotNil(t, planner)
	require.NoError(t, h.swarmSvc.ApplyPlannerOutput(ctx, planner.ID, service.PlannerOutput{
		Workers:        []service.PlannerWorker{{Title: "Retry worker", Prompt: "Update retry path", WorkerKind: "backend", Ownership: []string{"internal/handler"}, Isolation: "worktree", WriteScope: []string{"internal/handler"}, Required: true}},
		ReviewerPrompt: "Review the worker",
		MergerPrompt:   "Integrate the worker",
	}))
	worker, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRoleWorker)
	require.NoError(t, err)
	require.NotNil(t, worker)

	require.NoError(t, h.swarmSvc.HandleChildFollowup(ctx, worker.ID, "retry the already routed swarm worker follow-up"))
	routedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	routedParentCfg, err := models.ParseSwarmConfig(routedParent.SwarmConfig)
	require.NoError(t, err)
	routedGeneration := routedParentCfg.Generation

	require.NoError(t, h.taskRepo.UpdateStatus(ctx, worker.ID, models.StatusFailed))
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, worker.ID, models.CategoryBacklog))
	require.NoError(t, h.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusFailed))
	require.NoError(t, h.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryBacklog))
	failedFollowup := createExec(t, h, worker.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "retry the already routed swarm worker follow-up"
		ex.IsFollowup = true
	})
	require.NoError(t, h.execRepo.Complete(ctx, failedFollowup.ID, models.ExecFailed, "provider failed", "provider failed", 0, 20))

	started := make(chan struct{})
	release := make(chan struct{})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "retry complete"
	mock.TextOnly = "retry complete"
	mock.OnCall = func(ctx context.Context, _ testutil.MockLLMCall) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	h.llmSvc.SetLLMCaller(mock)

	handled, err := h.RetryLatestFailedTaskThreadFollowup(ctx, worker.ID)
	require.NoError(t, err)
	require.True(t, handled)
	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)

	updatedParent, err := h.taskRepo.GetByID(ctx, parent.ID)
	require.NoError(t, err)
	updatedParentCfg, err := models.ParseSwarmConfig(updatedParent.SwarmConfig)
	require.NoError(t, err)
	assert.Equal(t, routedGeneration, updatedParentCfg.Generation)
	assert.Equal(t, models.StatusRunning, updatedParent.Status)
	assert.Equal(t, models.CategoryActive, updatedParent.Category)
	assert.Equal(t, "needs_review", updatedParent.SwarmStatus)

	close(release)
	require.Eventually(t, func() bool {
		execs, err := h.execRepo.ListByTask(ctx, worker.ID)
		if err != nil {
			return false
		}
		return len(execs) >= 2 && execs[0].Status == models.ExecCompleted
	}, time.Second, 10*time.Millisecond)
}

func TestRetryLatestFailedTaskThreadFollowup_ReplaysFailedFollowupPromptFromActiveDrop(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Retry Failed Followup Project")
	task := createTask(t, h, project.ID, "Retry Failed Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusFailed
		tk.AgentID = &agent.ID
		tk.Prompt = "original task prompt"
	})
	initial := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "original task prompt"
		ex.IsFollowup = false
	})
	require.NoError(t, h.execRepo.Complete(ctx, initial.ID, models.ExecCompleted, "initial task output", "", 3, 10))
	failedFollowup := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "fix the failed follow-up"
		ex.IsFollowup = true
	})
	require.NoError(t, h.execRepo.Complete(ctx, failedFollowup.ID, models.ExecFailed, "failed follow-up output", "provider failed", 0, 20))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "retry complete"
	mock.TextOnly = "retry complete"
	h.llmSvc.SetLLMCaller(mock)

	handled, err := h.RetryLatestFailedTaskThreadFollowup(ctx, task.ID)
	require.NoError(t, err)
	require.True(t, handled)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, time.Second, 10*time.Millisecond)

	call := mock.LastCall()
	assert.Equal(t, "fix the failed follow-up", call.Prompt)
	assert.NotEqual(t, "original task prompt", call.Prompt)
	req := mock.LastAgentRequest()
	require.Len(t, req.ChatHistory, 2)
	assert.Equal(t, "original task prompt", req.ChatHistory[0].PromptSent)
	assert.Contains(t, req.ChatHistory[0].Output, "initial task output")
	assert.Equal(t, "fix the failed follow-up", req.ChatHistory[1].PromptSent)
	assert.Equal(t, models.ExecFailed, req.ChatHistory[1].Status)
	assert.Contains(t, req.ChatHistory[1].Output, "failed follow-up output")
	require.Eventually(t, func() bool {
		execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
		if err != nil || len(execs) != 3 || execs[2].Status != models.ExecCompleted {
			return false
		}
		updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
		return err == nil && updatedTask != nil && updatedTask.Status == models.StatusCompleted && updatedTask.Category == models.CategoryCompleted
	}, time.Second, 10*time.Millisecond)
	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	require.NoError(t, err)
	require.Len(t, execs, 3)
	assert.True(t, execs[2].IsFollowup)
	assert.Equal(t, "fix the failed follow-up", execs[2].PromptSent)
	assert.Equal(t, models.ExecCompleted, execs[2].Status)
	updatedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	assert.Equal(t, models.StatusCompleted, updatedTask.Status)
	assert.Equal(t, models.CategoryCompleted, updatedTask.Category)
}

func TestRetryLatestFailedTaskThreadFollowup_IgnoresOlderFailedFollowup(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Older Failed Followup Project")
	task := createTask(t, h, project.ID, "Older Failed Followup Task", func(tk *models.Task) {
		tk.Category = models.CategoryBacklog
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
		tk.Prompt = "original task prompt"
	})
	failedFollowup := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "old failed follow-up"
		ex.IsFollowup = true
	})
	require.NoError(t, h.execRepo.Complete(ctx, failedFollowup.ID, models.ExecFailed, "old failure", "failed", 0, 10))
	laterSuccess := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "later successful follow-up"
		ex.IsFollowup = true
	})
	require.NoError(t, h.execRepo.Complete(ctx, laterSuccess.ID, models.ExecCompleted, "later success", "", 1, 20))

	handled, err := h.RetryLatestFailedTaskThreadFollowup(ctx, task.ID)
	require.NoError(t, err)
	assert.False(t, handled)
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
			if evt.Type == events.DiffSnapshot && evt.TaskID == task.ID && evt.ExecID == followupExec.ID {
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

func TestProcessStreamingResponse_MixtureSupportedAggregatorInjectsCreateTask(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	project := createProject(t, h, "Tool-capable Mixture Project")
	providerCalls := 0
	var firstTools []any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if providerCalls == 1 {
			firstTools, _ = body["tools"].([]any)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if providerCalls == 1 {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_create\",\"type\":\"function\",\"function\":{\"name\":\"create_task\",\"arguments\":\"{\\\"title\\\":\\\"Mixture runtime investigation\\\",\\\"prompt\\\":\\\"Investigate the mixture runtime tool path.\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"))
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Task created.\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	aggregator := &models.LLMConfig{Name: "Compatible Aggregator", Provider: models.ProviderOpenAICompatible, Model: "provider/model", AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key", BaseURL: server.URL + "/v1/", PresetSlug: "vllm", Transport: "chat_completions"}
	require.NoError(t, llmConfigRepo.Create(ctx, aggregator))
	mixture := &models.LLMConfig{
		Name:              "Tool-capable Mixture",
		Provider:          models.ProviderMixture,
		Model:             "mixture",
		MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, mixture))
	chatHostTask := createTask(t, h, project.ID, "Mixture chat host", func(task *models.Task) {
		task.Category = models.CategoryChat
		task.Status = models.StatusPending
		task.AgentID = &mixture.ID
	})
	exec := createExec(t, h, chatHostTask.ID, mixture.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Create an investigation task"
	})

	h.processStreamingResponse(streamingResponseParams{
		ExecID: exec.ID, TaskID: chatHostTask.ID, Message: exec.PromptSent, Agent: *mixture,
		ProjectID: project.ID,
	})

	require.Equal(t, 2, providerCalls)
	require.True(t, slices.ContainsFunc(firstTools, func(raw any) bool {
		tool, _ := raw.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		return function["name"] == "create_task"
	}))

	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(tasks, func(task models.Task) bool { return task.Title == "Mixture runtime investigation" }))
}

func TestProcessStreamingResponse_MixtureUnsupportedAggregatorLeavesMarkerTextInert(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	project := createProject(t, h, "Marker Mixture Project")
	aggregator := &models.LLMConfig{Name: "Runtime-incapable Aggregator", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, llmConfigRepo.Create(ctx, aggregator))
	mixture := &models.LLMConfig{
		Name:              "Marker Mixture",
		Provider:          models.ProviderMixture,
		Model:             "mixture",
		MixtureConfigJSON: `{"enabled":true,"aggregator":{"agent_config_id":"` + aggregator.ID + `"}}`,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, mixture))
	chatHostTask := createTask(t, h, project.ID, "Marker mixture chat host", func(task *models.Task) {
		task.Category = models.CategoryChat
		task.Status = models.StatusPending
		task.AgentID = &mixture.ID
	})
	exec := createExec(t, h, chatHostTask.ID, mixture.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Create an investigation task"
	})
	mock := testutil.NewMockLLMCaller()
	mock.Response = "[CREATE_TASK]\n" + `{"title":"Mixture marker investigation","prompt":"Verify marker-looking text remains inert."}` + "\n[/CREATE_TASK]"
	mock.TextOnly = mock.Response
	h.llmSvc.SetLLMCaller(mock)

	h.processStreamingResponse(streamingResponseParams{
		ExecID: exec.ID, TaskID: chatHostTask.ID, Message: exec.PromptSent, Agent: *mixture,
		ProjectID: project.ID,
	})

	require.Nil(t, llmcontracts.RuntimeToolsFromContext(mock.LastAgentRequest().Ctx))
	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
	require.NoError(t, err)
	require.False(t, slices.ContainsFunc(tasks, func(task models.Task) bool { return task.Title == "Mixture marker investigation" }))
}

// TestProcessStreamingResponse_ActionMarkerTextIsInert verifies that a provider
// without runtime action tools cannot mutate state by emitting marker-looking prose.
func TestProcessStreamingResponse_ActionMarkerTextIsInert(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()

	// ProviderTest deliberately returns false from supportsChatActionTools,
	// mirroring the behavior of Claude CLI / Codex CLI / Ollama in production.
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Phantom Task Project")

	// Mock a normal chat turn that emits an inert [CREATE_TASK] block, as a
	// CLI-backed model might.
	mock := testutil.NewMockLLMCaller()
	mock.Response = "I'll create that task for you.\n\n[CREATE_TASK]\n" +
		`{"title": "Fix overlapping thinking and non-thinking content in task thread view", "prompt": "Investigate and fix the overlapping rendering."}` +
		"\n[/CREATE_TASK]"
	mock.TextOnly = mock.Response
	mock.Tokens = 25
	h.llmSvc.SetLLMCaller(mock)

	// ChatSend creates a CategoryChat host task that owns the chat execution
	// record. Mirror that so the FK on executions(task_id) is satisfied.
	chatHostTask := createTask(t, h, project.ID, "Chat host", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusPending
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, chatHostTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "Create a task to fix overlapping thinking content"
	})

	// Simulate the call pattern used by ChatSend / APIChatMessage: a chat turn
	h.processStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         chatHostTask.ID,
		Message:        "Create a task to fix overlapping thinking content",
		Agent:          *agent,
		ProjectID:      project.ID,
		SystemContext:  "",
		WorkDir:        "",
		IsTaskFollowup: false,
	})

	tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	for _, task := range tasks {
		if strings.Contains(task.Title, "overlapping thinking") {
			t.Fatalf("marker-looking model prose created a task: %+v", tasks)
		}
	}

	updatedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	if err != nil {
		t.Fatalf("get execution: %v", err)
	}
	if !strings.Contains(updatedExec.Output, "[CREATE_TASK]") || strings.Contains(updatedExec.Output, "[TASK_ID:") {
		t.Errorf("marker-looking prose should be preserved as inert output without a persistence summary, got: %s", updatedExec.Output)
	}
}

func TestStartQueuedChatInputPreservesChannelOriginMetadata(t *testing.T) {
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.workerSvc = nil
	h.SetSlackTaskContextRepo(repository.NewSlackTaskContextRepo(db))
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Queued Channel Metadata Project")
	activeTask := createTask(t, h, project.ID, "Active Channel Chat", func(tk *models.Task) {
		tk.Category = models.CategoryChat
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	activeExec := createExec(t, h, activeTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active chat"
	})

	telegramInput := &models.ThreadInput{
		Scope:          models.ThreadInputScopeChat,
		ProjectID:      project.ID,
		RunExecutionID: activeExec.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "telegram follow-up",
		ChatMode:       models.ChatModeOrchestrate,
		Source:         models.TaskOriginTelegram,
		TelegramChatID: 12345,
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, telegramInput))

	mock := testutil.NewMockLLMCaller()
	mock.Response = "telegram done"
	mock.TextOnly = "telegram done"
	h.llmSvc.SetLLMCaller(mock)
	h.startQueuedChatInput(ctx, *telegramInput)
	require.Eventually(t, func() bool { return mock.CallCount() == 1 }, 2*time.Second, 25*time.Millisecond)

	telegramReq := mock.LastAgentRequest()
	telegramExec, err := h.execRepo.GetByID(ctx, telegramReq.ExecID)
	require.NoError(t, err)
	require.NotNil(t, telegramExec)
	telegramTask, err := h.taskRepo.GetByID(ctx, telegramExec.TaskID)
	require.NoError(t, err)
	require.NotNil(t, telegramTask)
	require.Equal(t, models.TaskOriginTelegram, telegramTask.CreatedVia)
	require.Equal(t, int64(12345), telegramTask.TelegramChatID)

	activeExec2 := createExec(t, h, activeTask.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "second active chat"
	})
	slackInput := &models.ThreadInput{
		Scope:          models.ThreadInputScopeChat,
		ProjectID:      project.ID,
		RunExecutionID: activeExec2.ID,
		AgentConfigID:  agent.ID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        "slack follow-up",
		ChatMode:       models.ChatModeOrchestrate,
		Source:         models.TaskOriginSlack,
		SlackTeamID:    "T1",
		SlackChannelID: "C1",
		SlackThreadTS:  "1710000000.100000",
		SlackUserID:    "U1",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, slackInput))

	h.startQueuedChatInput(ctx, *slackInput)
	require.Eventually(t, func() bool { return mock.CallCount() == 2 }, 2*time.Second, 25*time.Millisecond)

	slackReq := mock.LastAgentRequest()
	slackExec, err := h.execRepo.GetByID(ctx, slackReq.ExecID)
	require.NoError(t, err)
	require.NotNil(t, slackExec)
	slackTask, err := h.taskRepo.GetByID(ctx, slackExec.TaskID)
	require.NoError(t, err)
	require.NotNil(t, slackTask)
	require.Equal(t, models.TaskOriginSlack, slackTask.CreatedVia)
	stc, err := h.slackTaskContextRepo.GetByTaskID(ctx, slackTask.ID)
	require.NoError(t, err)
	require.NotNil(t, stc)
	require.Equal(t, "C1", stc.SlackChannelID)
	require.Equal(t, "1710000000.100000", stc.SlackThreadTS)
}

func TestProcessStreamingResponse_ReaddsRecoveredPreparedSteeringToRealtimeUI(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	h.workerSvc = nil
	ctx := context.Background()
	mock := testutil.NewMockLLMCaller()
	mock.Response = "partial output"
	mock.TextOnly = "partial output"
	mock.Err = errors.New("provider failed")
	h.llmSvc.SetLLMCaller(mock)
	broadcaster := events.NewBroadcaster()
	h.broadcaster = broadcaster
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)

	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Recovered Steering Event Project")
	task := createTask(t, h, project.ID, "Recovered Steering Event Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "recovered steer",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))

	h.processStreamingResponse(streamingResponseParams{
		ExecID:                      exec.ID,
		TaskID:                      task.ID,
		Message:                     "active prompt",
		Agent:                       *agent,
		ProjectID:                   project.ID,
		IsTaskFollowup:              true,
		suppressQueuedTurnPromotion: true,
	})

	var sawRemoval bool
	var sawReadd bool
	deadline := time.After(2 * time.Second)
	for !sawRemoval || !sawReadd {
		select {
		case event := <-sub:
			if event.PendingInputID != steering.ID {
				continue
			}
			if event.Type == events.TaskThreadInputApplied {
				sawRemoval = true
			}
			if event.Type == events.TaskThreadInputQueued {
				sawReadd = true
				require.Equal(t, "recovered steer", event.Message)
				require.Equal(t, exec.ID, event.ExecID)
			}
		case <-deadline:
			t.Fatalf("timed out waiting for recovery events; removal=%v readd=%v", sawRemoval, sawReadd)
		}
	}

	stored, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, models.ThreadInputPending, stored.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, stored.InputMode)
}

func TestCancelThreadInputAllowsPendingSteeringBeforePreparation(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	broadcaster := events.NewBroadcaster()
	h.broadcaster = broadcaster
	sub, err := broadcaster.Subscribe()
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)
	ctx := context.Background()
	agent := createAgent(t, llmConfigRepo)
	project := createProject(t, h, "Cancel Pending Steering Project")
	task := createTask(t, h, project.ID, "Cancel Pending Steering Task", func(tk *models.Task) {
		tk.Category = models.CategoryActive
		tk.Status = models.StatusRunning
		tk.AgentID = &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecRunning
		ex.PromptSent = "active prompt"
		ex.IsFollowup = true
	})
	steering := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      project.ID,
		TaskID:         task.ID,
		RunExecutionID: exec.ID,
		InputMode:      models.ThreadInputModeSteering,
		InputStatus:    models.ThreadInputPending,
		TurnID:         exec.ID,
		ExpectedTurnID: exec.ID,
		Content:        "cancel me before processing",
	}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, steering, exec.ID))

	req := httptest.NewRequest(http.MethodPost, "/thread-inputs/"+steering.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `id="thread-input-`+steering.ID+`"`)

	select {
	case event := <-sub:
		require.Equal(t, events.TaskThreadInputCancelled, event.Type)
		require.Equal(t, task.ID, event.TaskID)
		require.Equal(t, project.ID, event.ProjectID)
		require.Equal(t, steering.ID, event.PendingInputID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for task thread input cancellation event")
	}

	stored, err := h.threadInputRepo.GetByID(ctx, steering.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, models.ThreadInputCancelled, stored.InputStatus)
}

func TestCancelThreadInputBroadcastsChatCancellation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()
	project := createProject(t, h, "Cancel Chat Pending Input Project")
	input := &models.ThreadInput{
		Scope:       models.ThreadInputScopeChat,
		ProjectID:   project.ID,
		InputMode:   models.ThreadInputModeQueued,
		InputStatus: models.ThreadInputPending,
		Content:     "cancel queued chat",
		Source:      "web",
	}
	require.NoError(t, h.threadInputRepo.CreateQueued(ctx, input))
	chatBroadcaster := events.NewChatBroadcaster()
	h.SetChatBroadcaster(chatBroadcaster)
	sub, err := chatBroadcaster.Subscribe()
	require.NoError(t, err)
	defer chatBroadcaster.Unsubscribe(sub)

	req := httptest.NewRequest(http.MethodPost, "/thread-inputs/"+input.ID+"/cancel", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	select {
	case event := <-sub:
		require.Equal(t, events.ChatThreadInputCancelled, event.Type)
		require.Equal(t, project.ID, event.ProjectID)
		require.Equal(t, input.ID, event.PendingInputID)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for chat thread input cancellation event")
	}
}

func TestProcessStreamingResponseConstructsRealHardenedGitHubRuntimeOnceFor50ToolCalls(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	h, _, llmConfigRepo, db := setupTestHandlerWithDB(t)
	h.workerSvc = nil
	ctx := context.Background()
	project := createProject(t, h, "Real hardened runtime construction project")

	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls++
		w.Header().Set("Content-Type", "text/event-stream")
		if providerCalls == 1 {
			toolCalls := make([]map[string]any, 50)
			for i := range toolCalls {
				toolCalls[i] = map[string]any{
					"index": i,
					"id":    fmt.Sprintf("call_%d", i),
					"type":  "function",
					"function": map[string]any{
						"name":      "list_capabilities",
						"arguments": `{}`,
					},
				}
			}
			payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"delta": map[string]any{"tool_calls": toolCalls}, "finish_reason": "tool_calls",
			}}})
			require.NoError(t, err)
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"complete\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	agent := models.LLMConfig{Name: "Runtime fixture model", Provider: models.ProviderOpenAICompatible, Model: "fixture/model", AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key", BaseURL: server.URL + "/v1/", PresetSlug: "vllm", Transport: "chat_completions"}
	require.NoError(t, llmConfigRepo.Create(ctx, &agent))
	task := createTask(t, h, project.ID, "Runtime fixture task", func(task *models.Task) {
		task.Category = models.CategoryActive
		task.Status = models.StatusRunning
		task.AgentID = &agent.ID
	})
	execution := createExec(t, h, task.ID, agent.ID, func(execution *models.Execution) {
		execution.Status = models.ExecRunning
		execution.PromptSent = "Run fifty tools"
		execution.IsFollowup = true
	})

	automationRepo := repository.NewAutomationRepo(db)
	h.llmSvc.SetAutomationRepo(automationRepo)
	h.llmSvc.SetGitHubIssueRuntimeProvider(&fakeGitHubService{})
	constructions := 0
	h.githubRuntimeHook = func() { constructions++ }
	automationContext := models.AutomationContext{ProjectID: project.ID, OriginTask: true}

	h.processStreamingResponse(streamingResponseParams{
		ExecID: execution.ID, TaskID: task.ID, Message: execution.PromptSent, Agent: agent,
		ProjectID: project.ID, IsTaskFollowup: true, Task: task, AutomationContext: &automationContext,
	})

	require.Equal(t, 2, providerCalls, "the provider should execute one 50-tool round and one final response round")
	require.Equal(t, 1, constructions, "processStreamingResponse must construct the real hardened runtime once for all 50 tool calls")
}

func newProductionHardenedRuntimeDispatchFixture() (*Handler, context.Context, streamingResponseParams, []llmcontracts.RuntimeToolDefinition) {
	llmSvc := service.NewLLMService(nil, nil, nil, repository.NewProjectRepo(nil), nil, nil)
	llmSvc.SetAutomationRepo(repository.NewAutomationRepo(nil))
	llmSvc.SetGitHubIssueRuntimeProvider(&fakeGitHubService{})
	h := &Handler{llmSvc: llmSvc}
	task := models.Task{ID: "runtime-fixture-task", ProjectID: "runtime-fixture-project", Category: models.CategoryScheduled}
	ctx := service.WithAutomationContext(context.Background(), models.AutomationContext{ProjectID: task.ProjectID, OriginTask: true})
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, true)
	params := streamingResponseParams{
		ProjectID:      task.ProjectID,
		TaskID:         task.ID,
		ExecID:         "runtime-fixture-execution",
		IsTaskFollowup: true,
		Task:           &task,
	}
	return h, ctx, params, defs
}

func TestTaskFollowupRuntimeReusesRealHardenedGitHubRuntimeFor50Dispatches(t *testing.T) {
	h, ctx, params, defs := newProductionHardenedRuntimeDispatchFixture()
	constructions := 0
	h.githubRuntimeHook = func() { constructions++ }
	channelCalls := 0
	params.RuntimeTools = &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "channel_tool"}},
		Executor: func(_ context.Context, name string, _ json.RawMessage) (string, bool, bool, error) {
			if name != "channel_tool" {
				return "", false, false, nil
			}
			channelCalls++
			return "channel", true, false, nil
		},
	}
	hardened := h.llmSvc.AutomationGitHubRuntimeTools(ctx, *params.Task, defs)
	require.NotNil(t, hardened, "fixture must construct the real hardened Automation GitHub runtime")
	runtime := h.buildStreamingResponseActionRuntime(ctx, params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	for i := 0; i < 50; i++ {
		output, handled, isError, err := runtime.Executor(ctx, "memory_view", nil)
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, isError)
		require.Contains(t, output, "memory_view")
	}
	require.Equal(t, 1, constructions, "processStreamingResponse runtime assembly must construct the real hardened runtime once, not once per dispatch")

	_, handled, isError, err := runtime.Executor(ctx, "github_create_issue", json.RawMessage(`{}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "not authorized")
	require.Equal(t, 1, constructions, "GitHub dispatch must reuse the request-scoped hardened runtime")

	output, handled, isError, err := runtime.Executor(ctx, "channel_tool", nil)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError)
	require.Equal(t, "channel", output)
	require.Equal(t, 1, channelCalls, "channel runtime must retain priority for its non-GitHub tool")

	output, handled, isError, err = runtime.Executor(ctx, "unknown_tool", nil)
	require.NoError(t, err)
	require.False(t, handled)
	require.False(t, isError)
	require.Empty(t, output)
	require.Equal(t, 1, constructions)
}

var taskFollowupDispatchBenchmarkOutput string

func BenchmarkTaskFollowupHardenedGitHubRuntime50Dispatches(b *testing.B) {
	h, ctx, params, defs := newProductionHardenedRuntimeDispatchFixture()
	contextRT := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "context_tool"}},
		Executor: func(_ context.Context, _ string, _ json.RawMessage) (string, bool, bool, error) {
			return "", false, false, nil
		},
	}
	ctx = llmcontracts.WithRuntimeTools(ctx, contextRT)
	params.RuntimeTools = &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "channel_tool"}},
		Executor: func(_ context.Context, _ string, _ json.RawMessage) (string, bool, bool, error) {
			return "", false, false, nil
		},
	}
	if hardened := h.llmSvc.AutomationGitHubRuntimeTools(ctx, *params.Task, defs); hardened == nil {
		b.Fatal("fixture must construct the real hardened Automation GitHub runtime")
	}

	benchmarkDispatches := func(b *testing.B, buildRuntime func() *llmcontracts.RuntimeTools) {
		b.Helper()
		for i := 0; i < b.N; i++ {
			runtime := buildRuntime()
			for call := 0; call < 50; call++ {
				output, handled, isError, err := runtime.Executor(ctx, "memory_view", nil)
				if err != nil || !handled || isError || !strings.Contains(output, "memory_view") {
					b.Fatalf("real generic dispatch failed: output=%q handled=%v isError=%v err=%v", output, handled, isError, err)
				}
				taskFollowupDispatchBenchmarkOutput = output
			}
		}
	}

	b.Run("legacy_reconstruct", func(b *testing.B) {
		benchmarkDispatches(b, func() *llmcontracts.RuntimeTools {
			initial := h.llmSvc.AutomationGitHubRuntimeTools(ctx, *params.Task, defs)
			genericExecutor := h.chatActionExecutor(params, nil, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
			legacyGeneric := &llmcontracts.RuntimeTools{
				Definitions: defs,
				Executor: func(dispatchCtx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
					hardened := h.llmSvc.AutomationGitHubRuntimeTools(dispatchCtx, *params.Task, defs)
					if hardened != nil {
						if output, handled, isError, err := hardened.Executor(dispatchCtx, name, input); handled {
							return output, true, isError, err
						}
					}
					return genericExecutor(dispatchCtx, name, input)
				},
			}
			return llmcontracts.CompositeRuntimeTools(initial, llmcontracts.RuntimeToolsFromContext(ctx), params.RuntimeTools, legacyGeneric)
		})
	})
	b.Run("reused", func(b *testing.B) {
		benchmarkDispatches(b, func() *llmcontracts.RuntimeTools {
			return h.buildStreamingResponseActionRuntime(ctx, params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		})
	})
}
