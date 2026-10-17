package service

import (
	"context"
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
	return nil, persistence.ErrNotFound
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

func TestLLMHookInvokerMemoryRecallRouteSuppressesDirectAgentScopedFileTools(t *testing.T) {
	storeEnv := teststores.New(t)
	llmRepo := storeEnv.Stores.LLMConfigs
	execRepo := storeEnv.Stores.Executions
	taskRepo := storeEnv.Stores.Tasks
	projectRepo := storeEnv.Stores.Projects
	scheduleRepo := storeEnv.Stores.Schedules
	attachmentRepo := storeEnv.Stores.Attachments
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
	storeEnv := teststores.New(t)
	llmRepo := storeEnv.Stores.LLMConfigs
	execRepo := storeEnv.Stores.Executions
	taskRepo := storeEnv.Stores.Tasks
	projectRepo := storeEnv.Stores.Projects
	scheduleRepo := storeEnv.Stores.Schedules
	attachmentRepo := storeEnv.Stores.Attachments
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
