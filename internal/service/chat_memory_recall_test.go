package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

type chatRecallHookStore struct {
	hooks []models.AgentLifecycleHook
	seen  []models.LifecycleWhen
}

func createChatMemoryProject(t *testing.T, ctx context.Context, projectRepo persistence.ProjectStore) *models.Project {
	t.Helper()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- chat_memory.md: chat should use repo-local project memory.\n"), 0o644); err != nil {
		t.Fatalf("write memory index: %v", err)
	}
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return project
}

func (s *chatRecallHookStore) HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error) {
	s.seen = append(s.seen, when)
	var out []models.AgentLifecycleHook
	for _, h := range s.hooks {
		if h.When == when && h.Enabled {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *chatRecallHookStore) CreateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	if e.ID == "" {
		e.ID = "exec-" + string(e.When)
	}
	return nil
}

func (s *chatRecallHookStore) UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	return nil
}

func (s *chatRecallHookStore) FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error) {
	return nil, sql.ErrNoRows
}

type chatRecallHookInvoker struct {
	seen []string
}

type memoryReadingCaptureAdapter struct {
	lastReq llmcontracts.AgentRequest
}

func (c *memoryReadingCaptureAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	c.lastReq = req
	rt := llmcontracts.RuntimeToolsFromContext(req.Ctx)
	if rt == nil || !rt.HasDefinition("read_file") || !rt.HasDefinition("list_files") {
		return llmcontracts.AgentResult{}, fmt.Errorf("missing scoped memory read tools")
	}
	out, handled, isErr, err := rt.Executor(req.Ctx, "read_file", json.RawMessage(`{"file_path":"chat_memory_probe.md"}`))
	if !handled || isErr || err != nil {
		return llmcontracts.AgentResult{}, fmt.Errorf("read memory file handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	return llmcontracts.AgentResult{Output: strings.TrimSpace(out), TextOnlyOutput: strings.TrimSpace(out), Usage: llmcontracts.Usage{TotalTokens: 1}}, nil
}

type fakeServiceAgentLookup struct {
	byID map[string]*models.Agent
}

func (f *fakeServiceAgentLookup) GetByID(_ context.Context, id string) (*models.Agent, error) {
	return f.byID[id], nil
}

type fakeServiceLLMConfig struct {
	def *models.LLMConfig
}

func (f *fakeServiceLLMConfig) GetDefault(_ context.Context) (*models.LLMConfig, error) {
	return f.def, nil
}

type memoryToolIsolationCaptureAdapter struct {
	lastReq llmcontracts.AgentRequest
}

func (c *memoryToolIsolationCaptureAdapter) Call(req llmcontracts.AgentRequest) (llmcontracts.AgentResult, error) {
	c.lastReq = req
	if !req.DisableTools {
		return llmcontracts.AgentResult{}, fmt.Errorf("expected memory recall route direct path to set DisableTools")
	}
	rt := llmcontracts.RuntimeToolsFromContext(req.Ctx)
	if rt != nil && (rt.HasDefinition("read_file") || rt.HasDefinition("list_files") || rt.HasDefinition("grep_search")) {
		return llmcontracts.AgentResult{}, fmt.Errorf("memory recall route direct path exposed file tools: %#v", rt.Definitions)
	}
	if rt != nil && rt.Executor != nil {
		if _, handled, _, err := rt.Executor(req.Ctx, "read_file", json.RawMessage(`{"file_path":"chat_memory_probe.md"}`)); handled || err != nil {
			return llmcontracts.AgentResult{}, fmt.Errorf("memory recall route direct path executed read_file handled=%v err=%v", handled, err)
		}
	}
	if req.AgentDefinition == nil {
		return llmcontracts.AgentResult{}, fmt.Errorf("missing agent definition")
	}
	if len(req.AgentDefinition.Tools) != 0 || len(req.AgentDefinition.ToolConfig.ScopedFiles) != 0 || len(req.AgentDefinition.Plugins) != 0 || len(req.AgentDefinition.MCPServers) != 0 {
		return llmcontracts.AgentResult{}, fmt.Errorf("unsanitized agent definition reached provider: tools=%v config=%#v plugins=%v mcp=%v", req.AgentDefinition.Tools, req.AgentDefinition.ToolConfig, req.AgentDefinition.Plugins, req.AgentDefinition.MCPServers)
	}
	if !req.AgentDefinition.ToolConfig.SkipDefaultTools {
		return llmcontracts.AgentResult{}, fmt.Errorf("expected sanitized memory recall agent definition to skip default tools")
	}
	return llmcontracts.AgentResult{Output: `{"memories":[],"content":"","confidence":0,"reason":"none","needs_clarification":false}`, TextOnlyOutput: `{"memories":[],"content":"","confidence":0,"reason":"none","needs_clarification":false}`, Usage: llmcontracts.Usage{TotalTokens: 1}}, nil
}

func (i *chatRecallHookInvoker) Invoke(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
	i.seen = append(i.seen, string(hook.When)+"/"+hook.SkillKey)
	if hook.When == models.LifecycleRouteTask {
		if _, ok := in.Extras["available_memories"]; !ok {
			return nil, fmt.Errorf("missing available_memories recall index")
		}
		if _, ok := in.Extras["available_skills"]; ok {
			return nil, fmt.Errorf("memory recall route hook received available_skills: %#v", in.Extras)
		}
		return json.Marshal(lifecycle.SelectedMemories{
			Memories: []lifecycle.SelectedMemory{
				{File: "chat_memory.md", Summary: "chat should use repo-local project memory."},
				{File: "unindexed_chat.md", Summary: "this safe-looking file is not indexed."},
			},
			Content:    "",
			Confidence: 0.9,
			Reason:     "test",
		})
	}
	if hook.When == models.LifecycleBeforeRun {
		return json.Marshal(lifecycle.ContextBlock{Content: "Remember: chat should use repo-local project memory.", Sources: []string{"MEMORIES.md"}, Confidence: 0.9})
	}
	return json.Marshal(lifecycle.ActivitySummary{Summary: "updated memory", ChangedPaths: []string{"chat.md"}})
}

func TestPrepareRecallOnlyLifecycleTurnInjectsChatMemoryWithoutExtraction(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- chat_memory.md: chat should use repo-local project memory.\n"), 0o644); err != nil {
		t.Fatalf("write memory index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "chat_memory.md"), []byte("# Chat Memory\n\nRecall-only selected memory body."), 0o644); err != nil {
		t.Fatalf("write selected memory: %v", err)
	}
	db := teststores.NewDB(t)
	projectRepo := teststores.Of(db).Projects
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	store := &chatRecallHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "other-before", When: models.LifecycleBeforeRun, SkillKey: "load_context", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true},
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
		{ID: "update", When: models.LifecycleAfterComplete, SkillKey: "update_memory", OutputContract: models.OutputContractActivitySummary, Blocking: false, Enabled: true},
	}}
	invoker := &chatRecallHookInvoker{}
	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))

	turn := worker.PrepareRecallOnlyLifecycleTurn(ctx, models.Task{ID: "chat-task", ProjectID: project.ID, Category: models.CategoryChat, Title: "Chat", Prompt: "Use remembered project context"})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "## Selected Memories For This Task") || !strings.Contains(instructions, "memory_view(\"<memory>\")") || !strings.Contains(instructions, "`chat_memory.md`") || strings.Contains(instructions, "unindexed_chat.md") {
		t.Fatalf("expected index-filtered selected memory handle in chat lifecycle context, got:\n%s", instructions)
	}
	for _, unwanted := range []string{"chat should use repo-local project memory.", "this safe-looking file is not indexed."} {
		if strings.Contains(instructions, unwanted) {
			t.Fatalf("chat lifecycle context leaked route/index memory text %q:\n%s", unwanted, instructions)
		}
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected recall-only chat context to expose selected-memory runtime tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"chat_memory.md"}`))
	if err != nil || !handled || isErr || !strings.Contains(out, "Recall-only selected memory body.") {
		t.Fatalf("selected chat memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, input := range []string{`{"handle":".openvibely/memories/chat_memory.md"}`, `{"handle":"unindexed_chat.md"}`} {
		out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(input))
		if err != nil || !handled || !isErr {
			t.Fatalf("unauthorized chat memory_view %s should be rejected handled=%v isErr=%v err=%v out=%q", input, handled, isErr, err, out)
		}
	}
	if len(invoker.seen) != 1 || invoker.seen[0] != "route_task/recall_memory" {
		t.Fatalf("expected only route_task recall hook, got %#v", invoker.seen)
	}
	turn.AfterComplete(nil, llmcontracts.ChatContext{})
	for _, when := range store.seen {
		if when == models.LifecycleAfterComplete {
			t.Fatalf("recall-only chat lifecycle must not run after_complete, saw %#v", store.seen)
		}
	}
}

func TestPrepareRecallOnlyLifecycleTurnAuthorizesMarkdownLinkedSemanticMemory(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Memory Index\n\n- [Realtime and Frontend Patterns](realtime_and_frontend_patterns.md) - SSE/diff streaming and frontend UI patterns.\n- [Unrelated](unrelated.md) - Unrelated body should stay unselected.\n"), 0o644); err != nil {
		t.Fatalf("write memory index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "realtime_and_frontend_patterns.md"), []byte("# Realtime and Frontend Patterns\n\nChat should recall realtime memory body."), 0o644); err != nil {
		t.Fatalf("write realtime memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "unrelated.md"), []byte("unrelated body"), 0o644); err != nil {
		t.Fatalf("write unrelated memory: %v", err)
	}
	db := teststores.NewDB(t)
	projectRepo := teststores.Of(db).Projects
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	store := &chatRecallHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		available, _ := in.Extras["available_memories"].(string)
		if !strings.Contains(available, "[Realtime and Frontend Patterns](realtime_and_frontend_patterns.md)") || strings.Contains(available, "Chat should recall realtime memory body") {
			return nil, fmt.Errorf("missing compact Markdown-linked memory index: %#v", in.Extras["available_memories"])
		}
		return json.Marshal(lifecycle.SelectedMemories{Memories: []lifecycle.SelectedMemory{{File: "realtime_and_frontend_patterns.md", Topic: "Realtime/frontend UI patterns"}}, Confidence: 0.92, Reason: "semantic realtime frontend match"})
	}), nil)
	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(runner)

	turn := worker.PrepareRecallOnlyLifecycleTurn(ctx, models.Task{ID: "chat-task", ProjectID: project.ID, Category: models.CategoryChat, Title: "Chat", Prompt: "Tell me about realtime front end patterns for this app"})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "## Selected Memories For This Task") || !strings.Contains(instructions, "`realtime_and_frontend_patterns.md`") || strings.Contains(instructions, "Chat should recall realtime memory body") || strings.Contains(instructions, "`unrelated.md`") {
		t.Fatalf("expected selected Markdown-linked memory handle only in recall chat context, got:\n%s", instructions)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected selected-memory runtime tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"realtime_and_frontend_patterns.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "Chat should recall realtime memory body") {
		t.Fatalf("selected Markdown-linked memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, input := range []string{`{"handle":".openvibely/memories/realtime_and_frontend_patterns.md"}`, `{"handle":"unrelated.md"}`, `{"handle":"MEMORIES.md"}`} {
		out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(input))
		if err != nil || !handled || !isErr {
			t.Fatalf("unauthorized recall memory_view %s should be rejected handled=%v isErr=%v err=%v out=%q", input, handled, isErr, err, out)
		}
	}
}

func TestPrepareRecallOnlyLifecycleTurnHonorsExplicitIndexedMemoryViewRequest(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- chat_memory.md: chat should use repo-local project memory.\n"), 0o644); err != nil {
		t.Fatalf("write memory index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "chat_memory.md"), []byte("# Chat Memory\n\nExplicit requested selected memory body."), 0o644); err != nil {
		t.Fatalf("write selected memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "unindexed_chat.md"), []byte("Unindexed body must not be authorized."), 0o644); err != nil {
		t.Fatalf("write unindexed memory: %v", err)
	}
	db := teststores.NewDB(t)
	projectRepo := teststores.Of(db).Projects
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	store := &chatRecallHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
	}}
	capturedPrompt := ""
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		capturedPrompt = in.TaskPrompt
		available, _ := in.Extras["available_memories"].(string)
		if !strings.Contains(available, "chat_memory.md: chat should use repo-local project memory") {
			return nil, fmt.Errorf("missing compact MEMORIES.md index: %#v", in.Extras["available_memories"])
		}
		return json.Marshal(lifecycle.SelectedMemories{Memories: nil, Content: "", Confidence: 0, Reason: "curator did not select explicit handle"})
	}), nil)
	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(runner)

	turn := worker.PrepareRecallOnlyLifecycleTurn(ctx, models.Task{ID: "chat-task", ProjectID: project.ID, Category: models.CategoryChat, Title: "Chat", Prompt: `call memory_view("chat_memory.md") but reject memory_view(".openvibely/memories/chat_memory.md") and memory_view("unindexed_chat.md")`})
	if !strings.Contains(capturedPrompt, "memory_view") || !strings.Contains(capturedPrompt, "chat_memory.md") {
		t.Fatalf("expected Memory Curator route hook to receive explicit prompt, got %q", capturedPrompt)
	}
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "`chat_memory.md`") || strings.Contains(instructions, ".openvibely/memories/chat_memory.md") || strings.Contains(instructions, "unindexed_chat.md") || strings.Contains(instructions, "chat should use repo-local project memory") {
		t.Fatalf("expected only explicit indexed selected memory handle, got:\n%s", instructions)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected selected-memory runtime tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"chat_memory.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "Explicit requested selected memory body.") {
		t.Fatalf("explicit indexed memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, input := range []string{`{"handle":".openvibely/memories/chat_memory.md"}`, `{"handle":"unindexed_chat.md"}`, `{"handle":"MEMORIES.md"}`} {
		out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(input))
		if err != nil || !handled || !isErr {
			t.Fatalf("unauthorized memory_view %s should be rejected handled=%v isErr=%v err=%v out=%q", input, handled, isErr, err, out)
		}
	}
}

func TestPrepareRecallOnlyLifecycleTurnUsesOnlyMemoryCuratorOwner(t *testing.T) {
	ctx := context.Background()
	db := teststores.NewDB(t)
	projectRepo := teststores.Of(db).Projects
	project := createChatMemoryProject(t, ctx, projectRepo)
	agentRepo := teststores.Of(db).Agents

	memoryAgent := &models.Agent{Key: models.AgentSystemKindMemoryCurator, Name: "System: Memory Curator", SystemKind: models.AgentSystemKindMemoryCurator, Model: "inherit", Enabled: true, GeneratedStatus: models.AgentStatusProtected}
	if err := agentRepo.Create(ctx, memoryAgent); err != nil {
		t.Fatalf("create memory agent: %v", err)
	}
	otherAgent := &models.Agent{Key: "custom_recaller", Name: "Custom Recaller", Model: "inherit", Enabled: true}
	if err := agentRepo.Create(ctx, otherAgent); err != nil {
		t.Fatalf("create other agent: %v", err)
	}

	store := &chatRecallHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "wrong-owner", AgentID: otherAgent.ID, When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
		{ID: "memory-owner", AgentID: memoryAgent.ID, When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
		{ID: "update", AgentID: memoryAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "update_memory", OutputContract: models.OutputContractActivitySummary, Enabled: true},
	}}
	invoked := []string{}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		invoked = append(invoked, hook.ID)
		return json.Marshal(lifecycle.SelectedMemories{
			Memories:   []lifecycle.SelectedMemory{{File: "chat_memory.md", Summary: "Memory Curator only."}},
			Content:    "",
			Confidence: 0.95,
			Reason:     "test",
		})
	}), nil)
	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRunner(runner)

	turn := worker.PrepareRecallOnlyLifecycleTurn(ctx, models.Task{ID: "chat-task", ProjectID: project.ID, Category: models.CategoryChat, Title: "Chat", Prompt: "What do you remember about chat memory?"})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "`chat_memory.md`") || strings.Contains(instructions, "Memory Curator only.") || strings.Contains(instructions, "Remembered from Memory Curator only.") {
		t.Fatalf("expected Memory Curator selected handle without route summary in chat context, got:\n%s", instructions)
	}
	if len(invoked) != 1 || invoked[0] != "memory-owner" {
		t.Fatalf("expected only Memory Curator recall hook, got %#v", invoked)
	}
	turn.AfterComplete(nil, llmcontracts.ChatContext{})
	for _, when := range store.seen {
		if when == models.LifecycleAfterComplete {
			t.Fatalf("chat recall must not run memory extraction, saw slots %#v", store.seen)
		}
	}
}

func TestLLMHookInvokerMemoryRecallRouteSuppressesDirectAgentScopedFileTools(t *testing.T) {
	db := teststores.NewDB(t)
	llmRepo := teststores.Of(db).LLMConfigs
	execRepo := teststores.Of(db).Executions
	taskRepo := teststores.Of(db).Tasks
	projectRepo := teststores.Of(db).Projects
	scheduleRepo := teststores.Of(db).Schedules
	attachmentRepo := teststores.Of(db).Attachments
	svc := NewLLMService(llmRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	capture := &memoryToolIsolationCaptureAdapter{}
	svc.providerAdapters[models.ProviderTest] = capture

	memoryAgent := &models.Agent{
		ID:         "memory-curator",
		Key:        models.AgentSystemKindMemoryCurator,
		Name:       "System: Memory Curator",
		SystemKind: models.AgentSystemKindMemoryCurator,
		Model:      "inherit",
		Enabled:    true,
		Tools:      []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely/memories", Permissions: []string{"read", "write", "delete"}}}, SkipDefaultTools: true, DisableRuntimeWorktree: true},
		Plugins:    []string{"memory-plugin"},
		MCPServers: []models.MCPServerConfig{{Name: "memory-mcp", Command: []string{"memory-mcp"}}},
	}
	agents := &fakeServiceAgentLookup{byID: map[string]*models.Agent{memoryAgent.ID: memoryAgent}}
	inv := lifecycle.NewLLMHookInvoker(svc, agents, &fakeServiceLLMConfig{def: &models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}})
	repoPath := t.TempDir()
	raw, err := inv.Invoke(context.Background(), models.AgentLifecycleHook{AgentID: memoryAgent.ID, When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Enabled: true}, lifecycle.HookInput{WorkDir: repoPath, Extras: map[string]any{"available_memories": "# Memories\n"}})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if err := lifecycle.ValidateOutput(models.OutputContractSelectedMemories, raw); err != nil {
		t.Fatalf("expected selected_memories output validation to pass: %v", err)
	}
	if capture.lastReq.AgentDefinition == nil || capture.lastReq.AgentDefinition.SystemKind != models.AgentSystemKindMemoryCurator {
		t.Fatalf("expected sanitized Memory Curator definition in production request, got %#v", capture.lastReq.AgentDefinition)
	}
}

func TestCallAgentDirectWithDefinitionMemoryCuratorCanReadRealMemoryFile(t *testing.T) {
	db := teststores.NewDB(t)
	llmRepo := teststores.Of(db).LLMConfigs
	execRepo := teststores.Of(db).Executions
	taskRepo := teststores.Of(db).Tasks
	projectRepo := teststores.Of(db).Projects
	scheduleRepo := teststores.Of(db).Schedules
	attachmentRepo := teststores.Of(db).Attachments
	svc := NewLLMService(llmRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	capture := &memoryReadingCaptureAdapter{}
	svc.providerAdapters[models.ProviderTest] = capture

	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Memories\n\n- chat_memory_probe.md: Chat should recall the user's preferred test keyword.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "chat_memory_probe.md"), []byte("The user prefers answers to mention kiwi-signal when discussing chat memory recall."), 0o644); err != nil {
		t.Fatal(err)
	}
	memoryAgent := &models.Agent{
		Key:        models.AgentSystemKindMemoryCurator,
		Name:       "System: Memory Curator",
		SystemKind: models.AgentSystemKindMemoryCurator,
		Model:      "inherit",
		Enabled:    true,
		Tools:      []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{Directory: ".openvibely/memories", Permissions: []string{"read", "write", "delete"}}}, SkipDefaultTools: true, DisableRuntimeWorktree: true},
	}
	out, _, err := svc.CallAgentDirectWithDefinition(context.Background(), "read relevant memory", nil, models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}, repoPath, memoryAgent)
	if err != nil {
		t.Fatalf("CallAgentDirectWithDefinition: %v", err)
	}
	if !strings.Contains(out, "kiwi-signal") {
		t.Fatalf("expected real memory file content through scoped runtime, got %q", out)
	}
	if capture.lastReq.WorkDir != memoryDir {
		t.Fatalf("expected Memory Curator scoped workdir %q, got %q", memoryDir, capture.lastReq.WorkDir)
	}
}

func TestCallAgentDirectStreamingDetailedIncludesRecallOnlyMemoryInChatContext(t *testing.T) {
	db := teststores.NewDB(t)
	llmRepo := teststores.Of(db).LLMConfigs
	execRepo := teststores.Of(db).Executions
	taskRepo := teststores.Of(db).Tasks
	projectRepo := teststores.Of(db).Projects
	scheduleRepo := teststores.Of(db).Schedules
	attachmentRepo := teststores.Of(db).Attachments
	svc := NewLLMService(llmRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	capture := &captureProviderAdapter{}
	svc.providerAdapters[models.ProviderTest] = capture

	ctx := context.Background()
	project := createChatMemoryProject(t, ctx, projectRepo)
	store := &chatRecallHookStore{hooks: []models.AgentLifecycleHook{{ID: "recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true}}}
	invoker := &chatRecallHookInvoker{}
	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(lifecycle.NewRunner(store, invoker, nil))
	turn := worker.PrepareRecallOnlyLifecycleTurn(ctx, models.Task{ID: "chat-task", ProjectID: project.ID, Category: models.CategoryChat, Title: "Chat", Prompt: "Use remembered project context"})

	_, err := svc.CallAgentDirectStreamingDetailed(turn.Ctx, "What project memory matters?", nil, models.LLMConfig{Provider: models.ProviderTest, Model: "test-model"}, "exec-1", []models.Execution{}, "base chat context", "", nil, false)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed: %v", err)
	}
	if !strings.Contains(capture.lastReq.ChatSystemContext, "base chat context") {
		t.Fatalf("expected original chat context, got %q", capture.lastReq.ChatSystemContext)
	}
	if !strings.Contains(capture.lastReq.ChatSystemContext, "## Selected Memories For This Task") || !strings.Contains(capture.lastReq.ChatSystemContext, "`chat_memory.md`") || strings.Contains(capture.lastReq.ChatSystemContext, "unindexed_chat.md") || strings.Contains(capture.lastReq.ChatSystemContext, "Remember: chat should use repo-local project memory.") {
		t.Fatalf("expected selected memory handle in model-facing chat context, got:\n%s", capture.lastReq.ChatSystemContext)
	}
	for _, unwanted := range []string{"chat should use repo-local project memory.", "this safe-looking file is not indexed."} {
		if strings.Contains(capture.lastReq.ChatSystemContext, unwanted) {
			t.Fatalf("model-facing chat context leaked route/index memory text %q:\n%s", unwanted, capture.lastReq.ChatSystemContext)
		}
	}
}
