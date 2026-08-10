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
	"time"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type routeHookStore struct {
	hooks     []models.AgentLifecycleHook
	created   []models.LifecycleExecution
	completed []models.LifecycleExecution
}

func (s *routeHookStore) HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error) {
	var out []models.AgentLifecycleHook
	for _, h := range s.hooks {
		if h.When == when && h.Enabled {
			out = append(out, h)
		}
	}
	return out, nil
}

func (s *routeHookStore) CreateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	if e.ID == "" {
		e.ID = "exec"
		if e.LifecycleHookID != nil {
			e.ID += "-" + *e.LifecycleHookID
		}
	}
	s.created = append(s.created, *e)
	return nil
}

func (s *routeHookStore) UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error {
	s.completed = append(s.completed, *e)
	return nil
}

func (s *routeHookStore) FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error) {
	return nil, sql.ErrNoRows
}

type routeHookInvoker struct {
	outputs map[string]json.RawMessage
}

func (i *routeHookInvoker) Invoke(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
	if out, ok := i.outputs[hook.ID]; ok {
		return out, nil
	}
	return nil, fmt.Errorf("missing output for hook %s", hook.ID)
}

type routeHookInvokerFunc func(context.Context, models.AgentLifecycleHook, lifecycle.HookInput) (json.RawMessage, error)

func (f routeHookInvokerFunc) Invoke(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
	return f(ctx, hook, in)
}

func routePayload(skills []string, confidence float64) json.RawMessage {
	b, _ := json.Marshal(lifecycle.SelectedSkills{Skills: skills, Confidence: confidence, Reason: "test"})
	return b
}

func routeClarificationPayload(question string) json.RawMessage {
	b, _ := json.Marshal(lifecycle.SelectedSkills{NeedsClarification: true, ClarifyingQuestion: question})
	return b
}

func contextBlockPayload(title string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"title":%q,"content":"test context"}`, title))
}

func memoryContextBlockPayload(content string, sources ...string) json.RawMessage {
	b, _ := json.Marshal(lifecycle.ContextBlock{Content: content, Sources: sources, Confidence: 0.9})
	return b
}

func routeTestRunner(outputs map[string]json.RawMessage) *lifecycle.Runner {
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "a-low", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
		{ID: "b-high", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
	}}
	return lifecycle.NewRunner(store, &routeHookInvoker{outputs: outputs}, nil)
}

func routeTestRunnerWithStore(outputs map[string]json.RawMessage) (*lifecycle.Runner, *routeHookStore) {
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "a-low", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
		{ID: "b-high", When: models.LifecycleBeforeRun, SkillKey: "before_run", OutputContract: models.OutputContractContextBlock, Blocking: true, Enabled: true},
	}}
	return lifecycle.NewRunner(store, &routeHookInvoker{outputs: outputs}, nil), store
}

func TestPrepareLifecycleTurn_RouteTaskSelectsSkillsWithoutSwitchingAgent(t *testing.T) {
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routePayload([]string{"missing/low"}, 0.2),
		"b-high": routePayload([]string{"missing_high"}, 0.9),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-1"})
	if turn.Task.AgentDefinitionID != nil {
		t.Fatalf("route_task must not auto-assign agents, got %v", *turn.Task.AgentDefinitionID)
	}
}

func TestPrepareLifecycleTurn_RouteTaskIgnoresInvalidSelectedSkills(t *testing.T) {
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routeClarificationPayload("which skill area?"),
		"b-high": json.RawMessage(`{"skills":["bad"],"confidence":0.9}`),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-1"})
	if turn.Task.AgentDefinitionID != nil {
		t.Fatalf("invalid skill routing must not auto-assign agents, got %v", *turn.Task.AgentDefinitionID)
	}
}

func TestPrepareLifecycleTurn_RouteTaskInputIncludesUserTaskText(t *testing.T) {
	var captured lifecycle.HookInput
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{
		ID:             "route",
		When:           models.LifecycleRouteTask,
		SkillKey:       "route_task",
		OutputContract: models.OutputContractSelectedSkills,
		Blocking:       true,
		Enabled:        true,
	}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, _ models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		captured = in
		return routePayload(nil, 0.8), nil
	}), nil)
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)

	worker.PrepareLifecycleTurn(context.Background(), models.Task{
		ID:        "task-art",
		ProjectID: "project-1",
		Title:     "Generate algorithmic dizzy city skyline artwork",
		Prompt:    "Create an algorithmic art implementation with a dizzy city skyline.",
	})

	if captured.TaskTitle != "Generate algorithmic dizzy city skyline artwork" {
		t.Fatalf("route_task input missing task title, got %q", captured.TaskTitle)
	}
	if !strings.Contains(captured.TaskPrompt, "dizzy city skyline") {
		t.Fatalf("route_task input missing task prompt, got %q", captured.TaskPrompt)
	}
	if _, ok := captured.Extras["available_skills"]; !ok {
		t.Fatalf("route_task input missing available_skills: %#v", captured.Extras)
	}
}

func TestPrepareLifecycleTurn_UsesDistinctTaskRunIDPerRun(t *testing.T) {
	runner, store := routeTestRunnerWithStore(map[string]json.RawMessage{
		"a-low":  routePayload([]string{"skill"}, 0.8),
		"b-high": contextBlockPayload("prepared"),
	})
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)

	worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-1"})
	worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-1"})

	seen := map[string]bool{}
	for _, exec := range store.created {
		if exec.TaskID != "task-1" {
			t.Fatalf("unexpected task id %q", exec.TaskID)
		}
		if exec.TaskRunID == "" || exec.TaskRunID == "task-1" {
			t.Fatalf("expected generated task run id, got %q", exec.TaskRunID)
		}
		seen[exec.TaskRunID] = true
	}
	if len(store.created) < 4 {
		t.Fatalf("expected lifecycle rows from both runs, got %d", len(store.created))
	}
	if len(seen) != 2 {
		t.Fatalf("expected exactly two task run ids, got %v", seen)
	}
}

func TestPrepareLifecycleTurn_AfterCompleteUsesProvidedTaskChatContext(t *testing.T) {
	ctx := context.Background()
	var captured lifecycle.HookInput
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{
		ID:             "learn",
		When:           models.LifecycleAfterComplete,
		SkillKey:       "observe_task_for_learning",
		OutputContract: models.OutputContractLearningSummary,
		Blocking:       false,
		Enabled:        true,
	}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, _ models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		captured = in
		return json.RawMessage(`{"summary":"ok","nothing_to_save":true}`), nil
	}), nil)
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)

	worker.runLifecycleSlot(ctx, models.LifecycleAfterComplete, models.Task{ID: "task-transcript", ProjectID: "default", Title: "Transcript task", Prompt: "initial user request", Status: models.StatusCompleted}, "run-1", nil, llmcontracts.ChatContext{Messages: []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "compacted user context"},
		{Role: "assistant", Content: "compacted assistant context"},
	}})

	raw, ok := captured.Extras[lifecycle.ConversationTranscriptKey]
	if !ok {
		t.Fatalf("expected conversation transcript in extras, got %#v", captured.Extras)
	}
	transcript, ok := raw.(llmcontracts.ChatContext)
	if !ok {
		t.Fatalf("expected task chat context, got %T", raw)
	}
	if len(transcript.Messages) != 2 {
		t.Fatalf("expected provided chat messages, got %#v", transcript.Messages)
	}
	if transcript.Messages[0].Content != "compacted user context" || transcript.Messages[1].Content != "compacted assistant context" {
		t.Fatalf("expected provided chat context, got %#v", transcript.Messages)
	}
}

func TestPrepareLifecycleTurn_AfterCompleteDoesNotRebuildFromExecutions(t *testing.T) {
	ctx := context.Background()
	var captured lifecycle.HookInput
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{
		ID:             "learn",
		When:           models.LifecycleAfterComplete,
		SkillKey:       "observe_task_for_learning",
		OutputContract: models.OutputContractLearningSummary,
		Enabled:        true,
	}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, _ models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		captured = in
		return json.RawMessage(`{"summary":"ok","nothing_to_save":true}`), nil
	}), nil)
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)

	worker.runLifecycleSlot(ctx, models.LifecycleAfterComplete, models.Task{ID: "task-chat-context", ProjectID: "default", Title: "Chat context task", Prompt: "initial user request", Status: models.StatusCompleted}, "run-1", nil, llmcontracts.ChatContext{Messages: []llmcontracts.ChatContextMessage{
		{Role: "user", Content: "current compacted request"},
	}})
	transcript := captured.Extras[lifecycle.ConversationTranscriptKey].(llmcontracts.ChatContext)
	encoded, err := json.Marshal(transcript)
	if err != nil {
		t.Fatalf("marshal transcript: %v", err)
	}
	if string(encoded) != `{"messages":[{"role":"user","content":"current compacted request"}]}` {
		t.Fatalf("expected only provided chat context, got %s", encoded)
	}
	for _, forbidden := range []string{"diff_output", "error_message", "status", "execution_id", "task_id", "truncated", "initial user request"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("chat context should not include %q: %s", forbidden, encoded)
		}
	}
}

func TestPrepareLifecycleTurn_AfterCompletePreservesRuntimeTools(t *testing.T) {
	ctx := context.Background()
	done := make(chan error, 1)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{
		ID:             "learn",
		When:           models.LifecycleAfterComplete,
		SkillKey:       "observe_task_for_learning",
		OutputContract: models.OutputContractLearningSummary,
		Enabled:        true,
	}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, _ models.AgentLifecycleHook, _ lifecycle.HookInput) (json.RawMessage, error) {
		rt := llmcontracts.RuntimeToolsFromContext(ctx)
		if rt == nil || !rt.HasDefinition("skill_manage") || !rt.HasDefinition("skills_list") || !rt.HasDefinition("agent_list") {
			done <- fmt.Errorf("expected lifecycle runtime tools in after_complete context, got %#v", rt)
			return json.RawMessage(`{"summary":"missing tools","nothing_to_save":true}`), nil
		}
		done <- nil
		return json.RawMessage(`{"summary":"ok","nothing_to_save":true}`), nil
	}), nil)
	db := testutil.NewTestDB(t)
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(t.TempDir())
	worker.SetLifecycleAgentRepo(repository.NewAgentRepo(db))
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))

	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-tools"})
	turn.AfterComplete(nil, llmcontracts.ChatContext{})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for after_complete hook")
	}
}

func TestPrepareLifecycleTurn_RouteTaskNoValidSkillsKeepsAgentUnchanged(t *testing.T) {
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routeClarificationPayload("which skill area?"),
		"b-high": json.RawMessage(`{"skills":[],"confidence":0.8,"reason":"none"}`),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-1"})
	if turn.Task.AgentDefinitionID != nil {
		t.Fatalf("route_task must not auto-assign agents, got %v", turn.Task.AgentDefinitionID)
	}
}

func TestPrepareLifecycleTurn_AssignedAgentRoutesAgentOwnedSkills(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeLifecycleTestSkill(t, root, "task_agent", "task_skill", "assigned agent skill body")
	writeLifecycleStandaloneSkill(t, root, "standalone_skill", "standalone body")

	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{ID: "task-agent-id", Key: "task_agent", Name: "Task Agent", Enabled: true, Tools: []string{"skill_view"}}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create task agent: %v", err)
	}

	var routeCalled bool
	var available any
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		routeCalled = true
		available = in.Extras["available_skills"]
		return routePayload([]string{"task_skill", "standalone_skill"}, 0.9), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-1", AgentDefinitionID: &agent.ID})
	if !routeCalled {
		t.Fatal("assigned agent tasks should run route_task against the assigned agent skill index")
	}
	if turn.Task.AgentDefinitionID == nil || *turn.Task.AgentDefinitionID != agent.ID {
		t.Fatalf("expected explicit assigned agent kept, got %v", turn.Task.AgentDefinitionID)
	}
	availableText, _ := available.(string)
	if !strings.Contains(availableText, "Available Assigned-Agent Skills") || !strings.Contains(availableText, "task_agent/task_skill") || strings.Contains(availableText, "standalone_skill") {
		t.Fatalf("route_task should receive only assigned-agent skills, got:\n%s", availableText)
	}
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "task_skill") || strings.Contains(instructions, "standalone_skill") {
		t.Fatalf("task prompt should include only selected assigned-agent skill, got:\n%s", instructions)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"task_skill"}`))
	if !handled || isErr || err != nil || !strings.Contains(out, "assigned agent skill body") {
		t.Fatalf("selected assigned-agent skill_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"standalone_skill"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "not in this turn's authorized index") {
		t.Fatalf("standalone skill must not load for assigned-agent turn handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestPrepareLifecycleTurn_SelectedTaskSkillsDoNotHideHookSkills(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "task_skill", "task skill body")
	writeLifecycleTestSkill(t, root, "skill_curator", "observe_task_for_learning", "observer skill body")

	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent, err := agentRepo.GetByKey(ctx, "skill_curator")
	if err != nil {
		t.Fatalf("load skill curator: %v", err)
	}
	if agent == nil {
		t.Fatal("expected seeded skill_curator")
	}

	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "route", AgentID: agent.ID, When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
		{ID: "learn", AgentID: agent.ID, When: models.LifecycleAfterComplete, SkillKey: "observe_task_for_learning", OutputContract: models.OutputContractLearningSummary, Enabled: true},
	}}
	afterSkillBody := make(chan string, 1)
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		switch hook.ID {
		case "route":
			return routePayload([]string{"task_skill"}, 0.9), nil
		case "learn":
			afterSkillBody <- in.SkillBody
			return json.RawMessage(`{"summary":"ok","nothing_to_save":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected hook %s", hook.ID)
		}
	}), NewCatalogSkillResolver(agentRepo, func() *agentskills.Catalog {
		return nil
	}, root, nil))

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))

	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-selected-skill"})
	turn.AfterComplete(nil, llmcontracts.ChatContext{})

	var resolvedSkillBody string
	select {
	case resolvedSkillBody = <-afterSkillBody:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for after_complete skill resolution")
	}
	if !strings.Contains(resolvedSkillBody, "observer skill body") {
		t.Fatalf("expected after_complete hook skill from full catalog, got %q", resolvedSkillBody)
	}
}

func TestPrepareLifecycleTurn_SeparatesTaskTurnAndAfterCompleteRuntimeTools(t *testing.T) {
	ctx := context.Background()
	mainTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "main_task_tool"}}}
	afterTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "after_complete_tool"}}}
	providerTools := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "provider_after_complete_tool"}}}
	ctx = llmcontracts.WithRuntimeTools(ctx, mainTools)
	ctx = WithAfterCompleteRuntimeTools(ctx, afterTools)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetAfterCompleteRuntimeToolProvider(func(context.Context, models.Task) *llmcontracts.RuntimeTools {
		return providerTools
	})
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-runtime-separation"})
	mainRT := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if mainRT == nil || !mainRT.HasDefinition("main_task_tool") {
		t.Fatalf("main task turn lost incoming runtime tools: %#v", mainRT)
	}
	if mainRT.HasDefinition("after_complete_tool") || mainRT.HasDefinition("provider_after_complete_tool") {
		t.Fatalf("after-complete-only tools leaked into main task turn: %#v", mainRT.Definitions)
	}

	seen := make(chan *llmcontracts.RuntimeTools, 1)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "after-hook", AgentID: "agent", When: models.LifecycleAfterComplete, SkillKey: "observe", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		seen <- llmcontracts.RuntimeToolsFromContext(ctx)
		return json.RawMessage(`{"summary":"ok","changed_paths":[]}`), nil
	}), nil)
	worker.SetLifecycleRunner(runner)
	turn.AfterComplete(nil, llmcontracts.ChatContext{})
	select {
	case rt := <-seen:
		if rt == nil || !rt.HasDefinition("after_complete_tool") || !rt.HasDefinition("provider_after_complete_tool") {
			t.Fatalf("after_complete hook did not receive after-complete runtime tools: %#v", rt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for after_complete hook")
	}
}

func TestPrepareLifecycleTurn_OrdinaryTaskWithActiveGoalRunsGoalAgentAfterComplete(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)

	project := &models.Project{Name: "Ordinary Goal Project"}
	if err := repository.NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := models.Task{ID: "ordinary-goal-task", ProjectID: project.ID, Title: "Ordinary goal task", Prompt: "work", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := taskRepo.Create(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := goalSvc.SetGoal(ctx, task.ID, "audit after completion", GoalOptions{}); err != nil {
		t.Fatalf("set goal: %v", err)
	}
	goalAgent := &models.Agent{Key: models.AgentSystemKindGoal, Name: "System: Goal Agent", Model: "inherit", SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, CreatedBy: models.AgentCreatedBySystem, Enabled: true}
	if err := agentRepo.Create(ctx, goalAgent); err != nil {
		t.Fatalf("create goal agent: %v", err)
	}

	done := make(chan error, 1)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "goal-after", AgentID: goalAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", OutputContract: models.OutputContractActivitySummary, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		if hook.AgentID != goalAgent.ID {
			done <- fmt.Errorf("unexpected hook agent %s", hook.AgentID)
			return json.RawMessage(`{"summary":"bad"}`), nil
		}
		if _, ok := in.Extras["task_goal"].(*models.TaskGoal); !ok {
			done <- fmt.Errorf("Goal Agent hook missing task_goal extras: %#v", in.Extras["task_goal"])
			return json.RawMessage(`{"summary":"missing goal"}`), nil
		}
		rt := llmcontracts.RuntimeToolsFromContext(ctx)
		if rt == nil || !rt.HasDefinition("get_task_goal") {
			done <- fmt.Errorf("Goal Agent hook missing provider-supplied goal runtime tools: %#v", rt)
			return json.RawMessage(`{"summary":"missing tools"}`), nil
		}
		done <- nil
		return json.RawMessage(`{"summary":"checked goal"}`), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetTaskGoalService(goalSvc)
	worker.SetAfterCompleteRuntimeToolProvider(func(context.Context, models.Task) *llmcontracts.RuntimeTools {
		return &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "get_task_goal"}}}
	})

	turn := worker.PrepareLifecycleTurn(ctx, task)
	turn.AfterComplete(nil, llmcontracts.ChatContext{})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Goal Agent after_complete hook")
	}
}

func TestPrepareLifecycleTurn_RecordsLifecycleHookSkillAnalyticsForRouteHooks(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	skillRepo := repository.NewSkillAnalyticsRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	skillCuratorAgent := &models.Agent{Name: "Skill Curator Test", Key: "skill_curator_test", Model: "inherit", Enabled: true}
	if err := agentRepo.Create(ctx, skillCuratorAgent); err != nil {
		t.Fatalf("create skill curator agent: %v", err)
	}
	memoryCuratorAgent := &models.Agent{Name: "Memory Curator Test", Key: "memory_curator_test", Model: "inherit", Enabled: true}
	if err := agentRepo.Create(ctx, memoryCuratorAgent); err != nil {
		t.Fatalf("create memory curator agent: %v", err)
	}
	projectA := &models.Project{Name: "Lifecycle Analytics A"}
	if err := projectRepo.Create(ctx, projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projectB := &models.Project{Name: "Lifecycle Analytics B"}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	taskA := models.Task{ProjectID: projectA.ID, Title: "Route analytics A", Prompt: "work", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, &taskA); err != nil {
		t.Fatalf("create task A: %v", err)
	}
	taskB := models.Task{ProjectID: projectB.ID, Title: "Route analytics B", Prompt: "work", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, &taskB); err != nil {
		t.Fatalf("create task B: %v", err)
	}

	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "route", AgentID: skillCuratorAgent.ID, When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
		{ID: "recall", AgentID: memoryCuratorAgent.ID, When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, _ lifecycle.HookInput) (json.RawMessage, error) {
		switch hook.SkillKey {
		case "route_task":
			return json.RawMessage(`{"skills":[],"confidence":0.1,"reason":"test"}`), nil
		case "recall_memory":
			return json.RawMessage(`{"memories":[],"confidence":0.1,"reason":"test"}`), nil
		default:
			return nil, fmt.Errorf("unexpected hook %s", hook.SkillKey)
		}
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetSkillAnalyticsRepo(skillRepo)
	worker.SetLifecycleRunner(runner)
	worker.PrepareLifecycleTurn(ctx, taskA)
	worker.PrepareLifecycleTurn(ctx, taskB)

	var routeCount, recallCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM skill_analytics_events WHERE project_id = ? AND task_id = ? AND skill_handle = 'route_task' AND event_type = 'selected' AND source = 'lifecycle_hook' AND surface = 'lifecycle_hook' AND skill_scope = 'agent_owned' AND agent_id = ?`, projectA.ID, taskA.ID, skillCuratorAgent.ID).Scan(&routeCount); err != nil {
		t.Fatalf("count route analytics: %v", err)
	}
	if routeCount != 1 {
		t.Fatalf("project A route_task selected events = %d, want 1", routeCount)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM skill_analytics_events WHERE project_id = ? AND task_id = ? AND skill_handle = 'recall_memory' AND event_type = 'selected' AND source = 'lifecycle_hook' AND surface = 'lifecycle_hook' AND skill_scope = 'agent_owned' AND agent_id = ?`, projectA.ID, taskA.ID, memoryCuratorAgent.ID).Scan(&recallCount); err != nil {
		t.Fatalf("count recall analytics: %v", err)
	}
	if recallCount != 1 {
		t.Fatalf("project A recall_memory selected events = %d, want 1", recallCount)
	}

	underusedA, err := skillRepo.GetUnderusedSkills(ctx, repository.SkillAnalyticsFilter{ProjectID: projectA.ID}, []repository.EnabledSkillInfo{{Handle: "route_task", Scope: models.SkillScopeAgentOwned, Enabled: true}, {Handle: "recall_memory", Scope: models.SkillScopeAgentOwned, Enabled: true}})
	if err != nil {
		t.Fatalf("GetUnderusedSkills project A: %v", err)
	}
	for _, handle := range []string{"route_task", "recall_memory"} {
		metric := findLifecycleTurnUnderusedSkill(underusedA, handle)
		if metric == nil || metric.ActivityCount != 1 || metric.LastActivity == nil {
			t.Fatalf("project A %s underused metric = %+v, want activity 1 with last activity", handle, metric)
		}
	}

	underusedB, err := skillRepo.GetUnderusedSkills(ctx, repository.SkillAnalyticsFilter{ProjectID: projectB.ID}, []repository.EnabledSkillInfo{{Handle: "route_task", Scope: models.SkillScopeAgentOwned, Enabled: true}})
	if err != nil {
		t.Fatalf("GetUnderusedSkills project B: %v", err)
	}
	metricB := findLifecycleTurnUnderusedSkill(underusedB, "route_task")
	if metricB == nil || metricB.ActivityCount != 1 || metricB.LastActivity == nil {
		t.Fatalf("project B route_task metric = %+v, want only project B activity", metricB)
	}
}

func TestPrepareLifecycleTurn_RecordsLifecycleHookSkillAnalyticsForAfterCompleteHooks(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	skillRepo := repository.NewSkillAnalyticsRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	skillCuratorAgent := &models.Agent{Name: "Skill Curator After Test", Key: "skill_curator_after_test", Model: "inherit", Enabled: true}
	if err := agentRepo.Create(ctx, skillCuratorAgent); err != nil {
		t.Fatalf("create skill curator agent: %v", err)
	}
	project := &models.Project{Name: "Lifecycle After Analytics"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := models.Task{ProjectID: project.ID, Title: "After analytics", Prompt: "work", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	done := make(chan struct{}, 1)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "observe", AgentID: skillCuratorAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "observe_task_for_learning", OutputContract: models.OutputContractLearningSummary, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, _ lifecycle.HookInput) (json.RawMessage, error) {
		done <- struct{}{}
		return json.RawMessage(`{"summary":"No durable learning to save.","nothing_to_save":true}`), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetSkillAnalyticsRepo(skillRepo)
	worker.SetLifecycleRunner(runner)
	turn := worker.PrepareLifecycleTurn(ctx, task)
	turn.AfterComplete(nil, llmcontracts.ChatContext{})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for after_complete hook")
	}

	underused, err := skillRepo.GetUnderusedSkills(ctx, repository.SkillAnalyticsFilter{ProjectID: project.ID}, []repository.EnabledSkillInfo{{Handle: "observe_task_for_learning", Scope: models.SkillScopeAgentOwned, Enabled: true}})
	if err != nil {
		t.Fatalf("GetUnderusedSkills: %v", err)
	}
	metric := findLifecycleTurnUnderusedSkill(underused, "observe_task_for_learning")
	if metric == nil || metric.ActivityCount != 1 || metric.SelectedCount != 1 || metric.LastActivity == nil {
		t.Fatalf("observe_task_for_learning metric = %+v, want selected activity", metric)
	}
	var threadID string
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(thread_id, '') FROM skill_analytics_events WHERE project_id = ? AND task_id = ? AND skill_handle = 'observe_task_for_learning'`, project.ID, task.ID).Scan(&threadID); err != nil {
		t.Fatalf("load analytics thread id: %v", err)
	}
	if threadID == "" || !strings.HasPrefix(threadID, "exec-observe") {
		t.Fatalf("thread_id = %q, want lifecycle execution id", threadID)
	}
}

func TestPrepareLifecycleTurn_TaskRuntimeToolsExposeOnlySelectedSkillView(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "task_skill", "task skill body")

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-runtime-tools"})
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("skill_view") {
		t.Fatalf("expected task skill_view runtime tools, got %#v", rt)
	}
	assertTaskRuntimeIsSelectedSkillOnly(t, rt)
	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"task_skill"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "not in this turn's authorized index") {
		t.Fatalf("unselected skill must not load, handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestPrepareLifecycleTurn_NormalTaskAssignedToSkillMaintainerDoesNotGetMutationTools(t *testing.T) {
	root := t.TempDir()
	writeLifecycleTestSkill(t, root, "skill_curator", "maintain_skill_library", "maintenance skill body")
	writeLifecycleStandaloneSkill(t, root, "other_skill", "other skill body")
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil || agent == nil {
		t.Fatalf("load system agent: agent=%#v err=%v", agent, err)
	}

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "normal-task", Title: "User task", Category: models.CategoryActive, AgentDefinitionID: &agent.ID})
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	assertTaskRuntimeIsSelectedSkillOnly(t, rt)
	if cat := lifecycleTurnFromContext(turn.Ctx).Catalog; cat == nil {
		t.Fatal("expected selected-task catalog in lifecycle context")
	} else if _, ok := cat.Lookup("other_skill"); ok {
		t.Fatal("assigned-agent task context must not retain full skill catalog when router is skipped")
	}
}

func TestPrepareLifecycleTurn_ScheduledSkillMaintenanceTaskUsesScopedSkillLibraryTools(t *testing.T) {
	root := t.TempDir()
	writeLifecycleTestSkill(t, root, "skill_curator", "maintain_skill_library", "maintenance skill body")
	writeLifecycleTestSkill(t, root, "skill_curator", "observe_task_for_learning", "observe body")
	writeLifecycleStandaloneSkill(t, root, "other_skill", "other skill body")
	writeLifecycleStandaloneSkill(t, root, "maintain_skill_library", "standalone maintenance body")
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil || agent == nil {
		t.Fatalf("load system agent: agent=%#v err=%v", agent, err)
	}

	var routeCalled bool
	var available any
	var routeTitle string
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		routeCalled = true
		available = in.Extras["available_skills"]
		routeTitle = in.TaskTitle
		return routePayload([]string{"maintain_skill_library"}, 0.9), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	task := models.Task{ID: "maintenance-task", Title: agentLibraryMaintenanceTaskTitle, Category: models.CategoryScheduled, AgentDefinitionID: &agent.ID}
	turn := worker.PrepareLifecycleTurn(ctx, task)
	if !routeCalled {
		t.Fatal("system skill maintenance task should run route_task against Skill Curator's owned skills")
	}
	if routeTitle != "Skill Library Maintenance" || strings.Contains(routeTitle, "System:") {
		t.Fatalf("scheduled maintenance hook title should be prompt-safe, got %q", routeTitle)
	}
	availableText, _ := available.(string)
	if !strings.Contains(availableText, "skill_curator/maintain_skill_library") || strings.Contains(availableText, "other_skill") {
		t.Fatalf("route_task should receive Skill Curator skill index only, got:\n%s", availableText)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	assertTaskRuntimeIsSelectedSkillOnly(t, rt)
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "maintain_skill_library") || strings.Contains(instructions, "observe_task_for_learning") || strings.Contains(instructions, "other_skill") {
		t.Fatalf("maintenance prompt should include only router-selected maintainer skill, got:\n%s", instructions)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"maintain_skill_library"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "maintenance skill body") {
		t.Fatalf("selected maintenance skill_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"skill_curator/maintain_skill_library"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "not a valid") {
		t.Fatalf("agent-prefixed handle must be rejected handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}

	llmSvc := NewLLMService(nil, nil, nil, nil, nil, nil)
	llmSvc.SetGlobalSkillRoot(root)
	llmSvc.SetAgentRepo(agentRepo)
	llmSvc.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	maintenanceTools := llmSvc.agentDeclaredSkillRuntimeTools(turn.Ctx, task, agent, "")
	mergedTools := llmcontracts.CompositeRuntimeTools(maintenanceTools, rt)
	for _, want := range []string{"skill_view", "skills_list", "agent_list", "agent_view", "skill_manage", "skill_import", "agent_skill_manage"} {
		if !mergedTools.HasDefinition(want) {
			t.Fatalf("scheduled maintenance runtime missing %s: %#v", want, mergedTools.Definitions)
		}
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skills_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "## other_skill") || !strings.Contains(out, "standalone:other_skill") || !strings.Contains(out, "standalone:maintain_skill_library") || strings.Contains(out, "skill_curator/maintain_skill_library") {
		t.Fatalf("maintenance skills_list should expose standalone skills only with qualified view handles handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"other_skill"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "other skill body") {
		t.Fatalf("maintenance skill_view must load unambiguous listed standalone skill handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"maintain_skill_library"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "ambiguous") {
		t.Fatalf("maintenance skill_view must reject colliding bare handles handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"standalone:maintain_skill_library"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "standalone maintenance body") {
		t.Fatalf("maintenance skill_view must load qualified standalone skill handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"agent:skill_curator/maintain_skill_library"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "maintenance skill body") {
		t.Fatalf("maintenance skill_view must load qualified selected agent skill read-only handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	reviewerAgent := &models.Agent{ID: "reviewer-agent-id", Key: "reviewer", Name: "Reviewer", Description: "Reviews code", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true, GeneratedStatus: models.AgentStatusGenerated, Skills: []models.SkillConfig{{Name: "review_migrations", Description: "Review migrations"}}}
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

	out, handled, isErr, err = mergedTools.Executor(context.Background(), "agent_list", json.RawMessage(`{}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "reviewer") || strings.Contains(out, "skill_curator") || strings.Contains(out, "memory_curator") || strings.Contains(out, "disabled_agent") || strings.Contains(out, "archived_agent") {
		t.Fatalf("agent_list should expose active user-managed agents only handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skill_manage", json.RawMessage(`{"action":"write_file","handle":"other_skill","scope":"global","support":{"kind":"references","path":"maintenance-note.md","content":"kept"}}`))
	if !handled || err != nil || isErr {
		t.Fatalf("maintenance skill_manage write_file failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "skills", "other_skill", "references", "maintenance-note.md")); err != nil {
		t.Fatalf("expected skill_manage to write standalone support file: %v", err)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "skill_manage", json.RawMessage(`{"action":"write_file","handle":"skill_curator/maintain_skill_library","scope":"global","support":{"kind":"references","path":"forbidden.md","content":"blocked"}}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "invalid standalone skill handle") {
		t.Fatalf("skill_manage must not mutate agent-owned system skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = mergedTools.Executor(context.Background(), "agent_skill_manage", json.RawMessage(`{"action":"create","agent":"reviewer","scope":"global","declaration":"---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: review_migrations\n---\n# Review migrations\n"}`))
	if !handled || err != nil || isErr {
		t.Fatalf("agent_skill_manage should mutate user-managed agent skills handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "agents", "reviewer", "skills", "review_migrations", "SKILL.md")); err != nil {
		t.Fatalf("agent_skill_manage did not write user-managed agent skill: %v", err)
	}
	for _, tc := range []struct {
		agent string
		skill string
		want  string
	}{
		{agent: "skill_curator", skill: "maintain_skill_library", want: "protected"},
		{agent: "memory_curator", skill: "consolidate_memory", want: "protected"},
		{agent: "disabled_agent", skill: "disabled_skill", want: "disabled"},
		{agent: "archived_agent", skill: "archived_skill", want: "archived"},
	} {
		params := fmt.Sprintf(`{"action":"write_file","agent":%q,"scope":"global","handle":%q,"support":{"kind":"references","path":"forbidden.md","content":"blocked"}}`, tc.agent, tc.skill)
		out, handled, isErr, err = mergedTools.Executor(context.Background(), "agent_skill_manage", json.RawMessage(params))
		if !handled || err != nil || !isErr || !strings.Contains(out, tc.want) {
			t.Fatalf("agent_skill_manage must block %s agent skills handled=%v isErr=%v err=%v out=%q", tc.want, handled, isErr, err, out)
		}
		if _, err := os.Stat(filepath.Join(root, "agents", tc.agent, "skills", tc.skill, "references", "forbidden.md")); !os.IsNotExist(err) {
			t.Fatalf("agent_skill_manage wrote into protected system agent skill path err=%v", err)
		}
	}
}

func TestPrepareLifecycleTurn_TaskFollowupUsesTurnPromptForSelectedMemoryView(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- usage_analytics.md: Usage analytics provider and operation tracking.\n- original_prompt.md: Original task-only memory.\n"), 0o644); err != nil {
		t.Fatalf("write MEMORIES.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "usage_analytics.md"), []byte("# Usage Analytics\n\nFollowup-selected memory body."), 0o644); err != nil {
		t.Fatalf("write usage memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "original_prompt.md"), []byte("Original prompt memory body must not be authorized."), 0o644); err != nil {
		t.Fatalf("write original memory: %v", err)
	}

	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "memory-recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		if hook.When != models.LifecycleRouteTask || hook.OutputContract != models.OutputContractSelectedMemories {
			return nil, fmt.Errorf("unexpected hook input hook=%#v input=%#v", hook, in)
		}
		if !strings.Contains(in.TaskPrompt, "usage_analytics.md") {
			return nil, fmt.Errorf("route hook did not receive current followup prompt: %q", in.TaskPrompt)
		}
		if strings.Contains(in.TaskPrompt, "original_prompt.md") {
			return nil, fmt.Errorf("route hook received stale original task prompt: %q", in.TaskPrompt)
		}
		available, _ := in.Extras["available_memories"].(string)
		if !strings.Contains(available, "usage_analytics.md") || strings.Contains(available, "Followup-selected memory body") {
			return nil, fmt.Errorf("route hook missing compact memory index or received bodies: %#v", in.Extras["available_memories"])
		}
		return json.Marshal(lifecycle.SelectedMemories{Memories: []lifecycle.SelectedMemory{{File: "usage_analytics.md", Topic: "Usage analytics"}}, Confidence: 0.95, Reason: "followup mentions usage analytics"})
	}), nil)

	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(runner)
	turn := worker.PrepareLifecycleTurn(WithTaskThreadLifecycleTurnPrompt(ctx, `view the memory for usage_analytics.md`), models.Task{ID: "task-memory-followup", ProjectID: project.ID, Title: "Original task", Prompt: `original task referenced original_prompt.md`})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "## Selected Memories For This Task") || !strings.Contains(instructions, "`usage_analytics.md`") {
		t.Fatalf("expected selected followup memory handle in task followup prompt, got:\n%s", instructions)
	}
	if strings.Contains(instructions, "`original_prompt.md`") || strings.Contains(instructions, "Followup-selected memory body") {
		t.Fatalf("task followup prompt leaked stale or full memory body, got:\n%s", instructions)
	}
	handles := SelectedMemoryHandlesFromContext(turn.Ctx)
	if len(handles) != 1 || handles[0] != "usage_analytics.md" {
		t.Fatalf("expected selected memory handle in context, got %#v", handles)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected task followup selected-memory runtime tool, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"usage_analytics.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "Followup-selected memory body") {
		t.Fatalf("task followup memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"original_prompt.md"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "not in this turn's authorized memory index") {
		t.Fatalf("unselected original memory must be rejected handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestPrepareLifecycleTurn_MemoryCuratorRouteTaskSelectsRelevantMemory(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- managed_memory.md: Preserve repo-local managed memory.\n"), 0o644); err != nil {
		t.Fatalf("write MEMORIES.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "managed_memory.md"), []byte("RAW TOPIC BODY SHOULD NOT BE ROUTED INTO RECALL INPUT"), 0o644); err != nil {
		t.Fatalf("write topic memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "hallucinated.md"), []byte("UNINDEXED MEMORY BODY MUST NOT BE AUTHORIZED"), 0o644); err != nil {
		t.Fatalf("write unindexed memory: %v", err)
	}

	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		ID:                  "memory-agent",
		Key:                 "memory_curator",
		Name:                "System: Memory Curator",
		SystemKind:          models.AgentSystemKindMemoryCurator,
		SelectableAsPrimary: false,
		Tools:               []string{models.AgentToolScopedFiles},
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create memory curator: %v", err)
	}

	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "skill-route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
		{ID: "memory-recall", AgentID: agent.ID, When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		if hook.When != models.LifecycleRouteTask || in.ProjectID != project.ID {
			t.Fatalf("unexpected route hook input hook=%#v input=%#v", hook, in)
		}
		switch hook.OutputContract {
		case models.OutputContractSelectedSkills:
			if _, ok := in.Extras["available_skills"]; !ok {
				t.Fatalf("selected_skills hook missing available_skills: %#v", in.Extras)
			}
			if _, ok := in.Extras["available_memories"]; ok {
				t.Fatalf("selected_skills hook must not receive available_memories: %#v", in.Extras)
			}
			return routePayload(nil, 0.1), nil
		case models.OutputContractSelectedMemories:
			if _, ok := in.Extras["available_skills"]; ok {
				t.Fatalf("selected_memories hook must not receive available_skills: %#v", in.Extras)
			}
			available, _ := in.Extras["available_memories"].(string)
			if !strings.Contains(available, "managed_memory.md: Preserve repo-local managed memory") {
				t.Fatalf("expected recall route hook to receive MEMORIES.md index, got %#v", in.Extras["available_memories"])
			}
			if strings.Contains(available, "RAW TOPIC BODY") {
				t.Fatalf("available_memories must not include topic file bodies, got %q", available)
			}
			b, _ := json.Marshal(lifecycle.SelectedMemories{
				Memories: []lifecycle.SelectedMemory{
					{File: "managed_memory.md", Topic: "Managed memory", Summary: "ROUTE SUMMARY MUST NOT RENDER", Snippet: "ROUTE SNIPPET MUST NOT RENDER"},
					{File: "hallucinated.md", Topic: "Hallucinated memory", Summary: "This safe-looking file is not in MEMORIES.md."},
				},
				Content:    "",
				Confidence: 0.9,
				Reason:     "test",
			})
			return b, nil
		default:
			t.Fatalf("unexpected route output contract %q", hook.OutputContract)
			return nil, nil
		}
	}), nil)

	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleAgentRepo(agentRepo)
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-memory", ProjectID: project.ID, Title: "Need memory", Prompt: "Use relevant context"})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "## Selected Memories For This Task") || !strings.Contains(instructions, "memory_view(\"<memory>\")") || !strings.Contains(instructions, "`managed_memory.md`") {
		t.Fatalf("expected Memory Curator route-selected memory handle in task prompt, got:\n%s", instructions)
	}
	for _, unwanted := range []string{"RAW TOPIC BODY", "hallucinated.md", "[recall_memory]", "Remembered context:", "Managed memory", "ROUTE SUMMARY MUST NOT RENDER", "ROUTE SNIPPET MUST NOT RENDER", "Preserve repo-local managed memory."} {
		if strings.Contains(instructions, unwanted) {
			t.Fatalf("route-selected memory prompt leaked %q or legacy before_run content, got:\n%s", unwanted, instructions)
		}
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected selected-memory runtime tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"managed_memory.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "RAW TOPIC BODY SHOULD NOT BE ROUTED INTO RECALL INPUT") {
		t.Fatalf("selected memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"MEMORIES.md"}`))
	if !handled || err != nil || !isErr || !strings.Contains(out, "not in this turn's authorized memory index") {
		t.Fatalf("unselected memory_view must be rejected handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestPrepareLifecycleTurn_HonorsExplicitIndexedMemoryViewRequest(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "managed_memory.md"), []byte("Explicit managed memory body."), 0o644); err != nil {
		t.Fatalf("write managed memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "unindexed.md"), []byte("Unindexed body must not be authorized."), 0o644); err != nil {
		t.Fatalf("write unindexed memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Project Memory\n\n- managed_memory.md: Preserve repo-local managed memory.\n"), 0o644); err != nil {
		t.Fatalf("write MEMORIES.md: %v", err)
	}

	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "memory-recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		if !strings.Contains(in.TaskPrompt, "memory_view") || !strings.Contains(in.TaskPrompt, "managed_memory.md") {
			return nil, fmt.Errorf("route hook missing explicit prompt: %q", in.TaskPrompt)
		}
		available, _ := in.Extras["available_memories"].(string)
		if !strings.Contains(available, "managed_memory.md: Preserve repo-local managed memory") {
			return nil, fmt.Errorf("route hook missing MEMORIES.md index: %#v", in.Extras["available_memories"])
		}
		return json.Marshal(lifecycle.SelectedMemories{Memories: nil, Content: "", Confidence: 0, Reason: "curator missed explicit handle"})
	}), nil)

	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(runner)
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-memory-explicit", ProjectID: project.ID, Title: "Need memory", Prompt: `call memory_view("managed_memory.md") but do not authorize memory_view(".openvibely/memories/managed_memory.md") or memory_view("unindexed.md")`})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "## Selected Memories For This Task") || !strings.Contains(instructions, "`managed_memory.md`") || strings.Contains(instructions, ".openvibely/memories/managed_memory.md") || strings.Contains(instructions, "unindexed.md") || strings.Contains(instructions, "Preserve repo-local managed memory") {
		t.Fatalf("expected only explicit indexed memory handle in task prompt, got:\n%s", instructions)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("expected selected-memory runtime tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"managed_memory.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "Explicit managed memory body.") {
		t.Fatalf("explicit task memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, input := range []string{`{"handle":".openvibely/memories/managed_memory.md"}`, `{"handle":"unindexed.md"}`, `{"handle":"MEMORIES.md"}`} {
		out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(input))
		if err != nil || !handled || !isErr {
			t.Fatalf("unauthorized memory_view %s should be rejected handled=%v isErr=%v err=%v out=%q", input, handled, isErr, err, out)
		}
	}
}

func TestPrepareLifecycleTurn_SemanticRealtimePromptKeepsMemoryRecallBeforeSelectedSkill(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "MEMORIES.md"), []byte("# Memory Index\n\n- [Realtime and Frontend Patterns](realtime_and_frontend_patterns.md) - SSE/diff streaming, lazy tab rendering, markdown/link safety, scroll behavior, page UI patterns, shared tokens, and pending-message UI.\n- [Unrelated](unrelated.md) - Background notes that should not be selected.\n"), 0o644); err != nil {
		t.Fatalf("write memory index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "realtime_and_frontend_patterns.md"), []byte("# Realtime and Frontend Patterns\n\nUse memory-first SSE guidance."), 0o644); err != nil {
		t.Fatalf("write realtime memory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "unrelated.md"), []byte("unrelated body"), 0o644); err != nil {
		t.Fatalf("write unrelated memory: %v", err)
	}

	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "openvibely_htmx_templ_ui_workflow", "HTMX skill body")
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Realtime Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "memory-recall", When: models.LifecycleRouteTask, SkillKey: "recall_memory", OutputContract: models.OutputContractSelectedMemories, Blocking: true, Enabled: true},
		{ID: "skill-route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(_ context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		switch hook.OutputContract {
		case models.OutputContractSelectedMemories:
			available, _ := in.Extras["available_memories"].(string)
			if !strings.Contains(available, "realtime_and_frontend_patterns.md") || strings.Contains(available, "Use memory-first SSE guidance") {
				return nil, fmt.Errorf("memory route hook missing compact index or received bodies: %#v", in.Extras["available_memories"])
			}
			return json.Marshal(lifecycle.SelectedMemories{Memories: []lifecycle.SelectedMemory{{File: "realtime_and_frontend_patterns.md", Topic: "Realtime/frontend UI patterns"}}, Confidence: 0.92, Reason: "semantic realtime frontend match"})
		case models.OutputContractSelectedSkills:
			return routePayload([]string{"openvibely_htmx_templ_ui_workflow"}, 0.86), nil
		default:
			return nil, fmt.Errorf("unexpected output contract %q", hook.OutputContract)
		}
	}), nil)

	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-realtime", ProjectID: project.ID, Title: "Realtime frontend", Prompt: "Tell me about realtime front end patterns for this app"})
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	memoryPos := strings.Index(instructions, "## Selected Memories For This Task")
	skillPos := strings.Index(instructions, "## Selected Skills For This Task")
	if memoryPos < 0 || skillPos < 0 || memoryPos > skillPos {
		t.Fatalf("expected selected memory block before selected skill block, got:\n%s", instructions)
	}
	for _, want := range []string{"MUST call `memory_view(\"<memory>\")`", "before relying on selected skills", "`realtime_and_frontend_patterns.md`", "`openvibely_htmx_templ_ui_workflow`"} {
		if !strings.Contains(instructions, want) {
			t.Fatalf("selected memory/skill context missing %q, got:\n%s", want, instructions)
		}
	}
	for _, unwanted := range []string{"Use memory-first SSE guidance", ".openvibely/memories/realtime_and_frontend_patterns.md", "`unrelated.md`"} {
		if strings.Contains(instructions, unwanted) {
			t.Fatalf("selected memory prompt leaked %q, got:\n%s", unwanted, instructions)
		}
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	if rt == nil || !rt.HasDefinition("memory_view") || !rt.HasDefinition("skill_view") {
		t.Fatalf("expected selected memory_view and skill_view runtime tools, got %#v", rt)
	}
	out, handled, isErr, err := rt.Executor(context.Background(), "memory_view", json.RawMessage(`{"handle":"realtime_and_frontend_patterns.md"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "Use memory-first SSE guidance") {
		t.Fatalf("selected semantic memory_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
	for _, input := range []string{`{"handle":".openvibely/memories/realtime_and_frontend_patterns.md"}`, `{"handle":"unrelated.md"}`, `{"handle":"MEMORIES.md"}`} {
		out, handled, isErr, err = rt.Executor(context.Background(), "memory_view", json.RawMessage(input))
		if err != nil || !handled || !isErr {
			t.Fatalf("unauthorized semantic memory_view %s should be rejected handled=%v isErr=%v err=%v out=%q", input, handled, isErr, err, out)
		}
	}
	out, handled, isErr, err = rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"openvibely_htmx_templ_ui_workflow"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "HTMX skill body") {
		t.Fatalf("selected semantic skill_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestBuildTaskMemoryRuntimeToolsRequiresAssignedAgentMemoryViewGrant(t *testing.T) {
	ctx := context.Background()
	repoPath := t.TempDir()
	memoryDir := filepath.Join(repoPath, ".openvibely", "memories")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("create memory dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "selected.md"), []byte("selected body"), 0o644); err != nil {
		t.Fatalf("write selected memory: %v", err)
	}

	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	agentRepo := repository.NewAgentRepo(db)
	withoutMemory := &models.Agent{ID: "agent-no-memory", Name: "No Memory", Tools: []string{"Read"}}
	withMemory := &models.Agent{ID: "agent-with-memory", Name: "With Memory", Tools: []string{"memory_view"}}
	if err := agentRepo.Create(ctx, withoutMemory); err != nil {
		t.Fatalf("create no-memory agent: %v", err)
	}
	if err := agentRepo.Create(ctx, withMemory); err != nil {
		t.Fatalf("create memory agent: %v", err)
	}

	worker := NewWorkerService(nil, 0, projectRepo)
	worker.SetLifecycleAgentRepo(agentRepo)
	memories := []memory.SelectedMemory{{File: "selected.md"}}
	noMemoryTask := models.Task{ID: "task-no-memory", ProjectID: project.ID, AgentDefinitionID: &withoutMemory.ID}
	if rt := worker.buildTaskMemoryRuntimeTools(ctx, noMemoryTask, memories); rt != nil {
		t.Fatalf("agent without memory_view grant received selected-memory tools: %#v", rt)
	}
	memoryTask := models.Task{ID: "task-with-memory", ProjectID: project.ID, AgentDefinitionID: &withMemory.ID}
	rt := worker.buildTaskMemoryRuntimeTools(ctx, memoryTask, memories)
	if rt == nil || !rt.HasDefinition("memory_view") {
		t.Fatalf("agent with memory_view grant did not receive selected-memory tools: %#v", rt)
	}
}

func TestPrepareLifecycleTurn_MemoryConsolidationTaskRoutesConsolidateMemorySkill(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeLifecycleTestSkill(t, root, "memory_curator", "recall_memory", "recall body")
	writeLifecycleTestSkill(t, root, "memory_curator", "update_memory", "update body")
	writeLifecycleTestSkill(t, root, "memory_curator", "consolidate_memory", "consolidate body")

	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		ID:                  "memory-agent",
		Key:                 "memory_curator",
		Name:                "System: Memory Curator",
		SystemKind:          models.AgentSystemKindMemoryCurator,
		SelectableAsPrimary: false,
		Tools:               []string{models.AgentToolScopedFiles},
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create memory curator: %v", err)
	}

	var routeCalled bool
	var available any
	var routeTitle string
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		routeCalled = true
		available = in.Extras["available_skills"]
		routeTitle = in.TaskTitle
		return routePayload([]string{"consolidate_memory"}, 0.95), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "memory-task", Title: "System: Memory Consolidation", Category: models.CategoryScheduled, AgentDefinitionID: &agent.ID})
	if !routeCalled {
		t.Fatal("assigned system tasks should run skill routing")
	}
	if routeTitle != "Memory Consolidation" || strings.Contains(routeTitle, "System:") {
		t.Fatalf("scheduled memory hook title should be prompt-safe, got %q", routeTitle)
	}
	if availableText, _ := available.(string); !strings.Contains(availableText, "memory_curator/consolidate_memory") || strings.Contains(availableText, "other_skill") {
		t.Fatalf("route_task should receive Memory Curator skill index, got:\n%s", availableText)
	}
	instructions := additionalProjectInstructionsFromContext(turn.Ctx)
	if !strings.Contains(instructions, "consolidate_memory") || strings.Contains(instructions, "recall_memory") || strings.Contains(instructions, "update_memory") {
		t.Fatalf("scheduled Memory Curator task should include only router-selected consolidate_memory skill, got:\n%s", instructions)
	}
	rt := llmcontracts.RuntimeToolsFromContext(turn.Ctx)
	out, handled, isErr, err := rt.Executor(context.Background(), "skill_view", json.RawMessage(`{"handle":"consolidate_memory"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "consolidate body") {
		t.Fatalf("selected consolidate_memory skill_view failed handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func TestPrepareLifecycleTurn_AssignedAgentWithNoSkillsRoutesEmptyIndex(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		ID:                  "memory-agent",
		Key:                 "memory_curator",
		Name:                "System: Memory Curator",
		SystemKind:          models.AgentSystemKindMemoryCurator,
		SelectableAsPrimary: false,
		Tools:               []string{models.AgentToolScopedFiles},
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create memory curator: %v", err)
	}

	var routeCalled bool
	var available any
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		routeCalled = true
		available = in.Extras["available_skills"]
		return routePayload([]string{"maintain_skill_library"}, 0.9), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleAgentRepo(agentRepo)
	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "memory-task", Title: "System: Memory Consolidation", Category: models.CategoryScheduled, AgentDefinitionID: &agent.ID})
	if !routeCalled {
		t.Fatal("assigned system tasks should still run skill routing")
	}
	if availableText, _ := available.(string); !strings.Contains(availableText, "No skills indexed for assigned agent") {
		t.Fatalf("expected empty assigned-agent skill index, got:\n%s", availableText)
	}
	if got := additionalProjectInstructionsFromContext(turn.Ctx); got != "" {
		t.Fatalf("no selected skills should produce no skill prompt, got:\n%s", got)
	}
}

func TestPromptSafeTaskTitleSanitizesScheduledMaintenanceTasks(t *testing.T) {
	cases := []struct {
		name string
		task models.Task
		want string
	}{
		{name: "skill maintenance", task: models.Task{Title: agentLibraryMaintenanceTaskTitle, Category: models.CategoryScheduled}, want: "Skill Library Maintenance"},
		{name: "memory consolidation", task: models.Task{Title: memoryConsolidationTaskTitle, Category: models.CategoryScheduled}, want: "Memory Consolidation"},
		{name: "user title", task: models.Task{Title: "System: User asked for this literal title", Category: models.CategoryScheduled}, want: "System: User asked for this literal title"},
		{name: "active task", task: models.Task{Title: agentLibraryMaintenanceTaskTitle, Category: models.CategoryActive}, want: agentLibraryMaintenanceTaskTitle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := promptSafeTaskTitle(tc.task); got != tc.want {
				t.Fatalf("promptSafeTaskTitle() = %q, want %q", got, tc.want)
			}
		})
	}
}

func assertTaskRuntimeIsSelectedSkillOnly(t *testing.T, rt *llmcontracts.RuntimeTools) {
	t.Helper()
	if rt == nil || !rt.HasDefinition("skill_view") {
		t.Fatalf("expected task skill_view runtime tools, got %#v", rt)
	}
	for _, denied := range []string{"skill_manage", "agent_manage", "skills_list", "agent_list", "agent_view"} {
		if rt.HasDefinition(denied) {
			t.Fatalf("task runtime tools must not expose %s, got %#v", denied, rt.Definitions)
		}
		if allow, handled := rt.Filter(denied); allow || handled {
			t.Fatalf("task runtime filter must not own %s", denied)
		}
	}
}

func TestPrepareLifecycleTurn_RouteHookDoesNotExposeMutationTools(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "task_skill", "task skill body")

	var routeTools *llmcontracts.RuntimeTools
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		routeTools = llmcontracts.RuntimeToolsFromContext(ctx)
		return routePayload([]string{"task_skill"}, 0.9), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-route-tools"})
	if routeTools == nil || !routeTools.HasDefinition("skill_view") || !routeTools.HasDefinition("skills_list") {
		t.Fatalf("expected standalone route hook read tools, got %#v", routeTools)
	}
	if routeTools.HasDefinition("skill_manage") || routeTools.HasDefinition("agent_manage") {
		t.Fatalf("route hook must not expose mutation tools, got %#v", routeTools.Definitions)
	}
}

func TestPrepareLifecycleTurn_AssignedAgentRouteHookDoesNotExposeStandaloneSkillsList(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeLifecycleTestSkill(t, root, "task_agent", "task_skill", "task skill body")
	writeLifecycleStandaloneSkill(t, root, "standalone_skill", "standalone body")

	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{ID: "task-agent-id", Key: "task_agent", Name: "Task Agent", Enabled: true, Tools: []string{"skill_view", "skills_list"}}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create task agent: %v", err)
	}

	var routeTools *llmcontracts.RuntimeTools
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		routeTools = llmcontracts.RuntimeToolsFromContext(ctx)
		return routePayload([]string{"task_skill"}, 0.9), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-route-agent-tools", AgentDefinitionID: &agent.ID})
	if routeTools == nil || !routeTools.HasDefinition("skill_view") {
		t.Fatalf("expected assigned-agent route hook skill_view, got %#v", routeTools)
	}
	if routeTools.HasDefinition("skills_list") || routeTools.HasDefinition("agent_list") || routeTools.HasDefinition("agent_view") || routeTools.HasDefinition("skill_manage") {
		t.Fatalf("assigned-agent route hook must expose only scoped skill_view, got %#v", routeTools.Definitions)
	}
	out, handled, isErr, err := routeTools.Executor(ctx, "skill_view", json.RawMessage(`{"handle":"task_skill"}`))
	if !handled || err != nil || isErr || !strings.Contains(out, "task skill body") {
		t.Fatalf("expected assigned-agent skill_view to load task skill handled=%v isErr=%v err=%v out=%q", handled, isErr, err, out)
	}
}

func writeLifecycleTestSkill(t *testing.T, root, agent, skill, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "agents", agent, "skills", skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", agent, "skills", skill, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	appendLifecycleTestHeader(t, filepath.Join(root, "agents", "AGENTS.md"), agent)
	appendLifecycleTestHeader(t, filepath.Join(root, "agents", agent, "SKILLS.md"), agent+"/"+skill)
}

func writeLifecycleStandaloneSkill(t *testing.T, root, skill, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "skills", skill), 0o755); err != nil {
		t.Fatal(err)
	}
	content := body
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		content = "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: " + skill + "\n  scope: global\n---\n" + body
	}
	if err := os.WriteFile(filepath.Join(root, "skills", skill, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	appendLifecycleTestHeader(t, filepath.Join(root, "skills", "SKILLS.md"), skill)
}

func appendLifecycleTestHeader(t *testing.T, path, header string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("## " + header + "\n\n"); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareLifecycleTurn_AfterCompleteDoesNotPreExposeScopedFileTools(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	hookAgent := &models.Agent{
		Key:     "custom_memory_hook",
		Name:    "Custom Memory Hook",
		Enabled: true,
		Tools:   []string{models.AgentToolScopedFiles},
		ToolConfig: models.AgentToolConfig{ScopedFiles: []models.ScopedFilesConfig{{
			Directory:   ".openvibely/custom-memory",
			Permissions: []string{"read", "write"},
		}}},
	}
	if err := agentRepo.Create(ctx, hookAgent); err != nil {
		t.Fatalf("create hook agent: %v", err)
	}

	done := make(chan error, 1)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{{
		ID:             "custom-memory-update",
		AgentID:        hookAgent.ID,
		When:           models.LifecycleAfterComplete,
		SkillKey:       "update_custom_memory",
		OutputContract: models.OutputContractActivitySummary,
		Enabled:        true,
	}}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		rt := llmcontracts.RuntimeToolsFromContext(ctx)
		if rt != nil && rt.HasDefinition("write_file") {
			done <- fmt.Errorf("after_complete worker context must not pre-expose scoped file tools before per-agent direct-call setup: %#v", rt.Definitions)
			return json.RawMessage(`{"summary":"bad","skipped":true,"skip_reason":"pre-exposed tools"}`), nil
		}
		done <- nil
		return json.RawMessage(`{"summary":"ok","changed_paths":[]}`), nil
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))

	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-hook-scoped-files", ProjectID: "default"})
	turn.AfterComplete(nil, llmcontracts.ChatContext{})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for after_complete hook")
	}
}

func TestPrepareLifecycleTurn_AfterCompleteIncludesAssignedAgentLearningContextAndTool(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeLifecycleTestSkill(t, root, "reviewer", "review_migrations", "migration review body")

	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{ID: "reviewer-id", Key: "reviewer", Name: "Reviewer", Description: "Reviews code changes", Enabled: true, Scope: models.AgentScopeProject, Tools: []string{"skill_view"}}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	done := make(chan error, 1)
	store := &routeHookStore{hooks: []models.AgentLifecycleHook{
		{ID: "route", When: models.LifecycleRouteTask, SkillKey: "route_task", OutputContract: models.OutputContractSelectedSkills, Blocking: true, Enabled: true},
		{ID: "learn", When: models.LifecycleAfterComplete, SkillKey: "observe_task_for_learning", OutputContract: models.OutputContractLearningSummary, Enabled: true},
	}}
	runner := lifecycle.NewRunner(store, routeHookInvokerFunc(func(ctx context.Context, hook models.AgentLifecycleHook, in lifecycle.HookInput) (json.RawMessage, error) {
		switch hook.ID {
		case "route":
			return routePayload([]string{"review_migrations"}, 0.9), nil
		case "learn":
			rt := llmcontracts.RuntimeToolsFromContext(ctx)
			if rt == nil || !rt.HasDefinition("agent_skill_manage") || !rt.HasDefinition("skill_manage") {
				done <- fmt.Errorf("expected learning mutation tools, got %#v", rt)
				return json.RawMessage(`{"summary":"bad","nothing_to_save":true}`), nil
			}
			raw, ok := in.Extras[lifecycle.LearningSnapshotKey].(lifecycle.LearningInputSnapshot)
			if !ok {
				done <- fmt.Errorf("missing learning snapshot: %#v", in.Extras[lifecycle.LearningSnapshotKey])
				return json.RawMessage(`{"summary":"bad","nothing_to_save":true}`), nil
			}
			if raw.AssignedAgent == nil || raw.AssignedAgent.Key != "reviewer" || raw.AssignedAgent.Description != "Reviews code changes" {
				done <- fmt.Errorf("bad assigned agent context: %+v", raw.AssignedAgent)
				return json.RawMessage(`{"summary":"bad","nothing_to_save":true}`), nil
			}
			if len(raw.SelectedAgentSkills) != 1 || raw.SelectedAgentSkills[0].Handle != "review_migrations" || raw.SelectedAgentSkills[0].Owner != "assigned_agent" {
				done <- fmt.Errorf("bad selected agent skills: %+v", raw.SelectedAgentSkills)
				return json.RawMessage(`{"summary":"bad","nothing_to_save":true}`), nil
			}
			if len(raw.SelectedStandaloneSkills) != 0 || !stringSliceContains(raw.SkillWritePolicy, "Use agent_skill_manage only for changes specific to the assigned agent's role, workflow, or selected agent-owned skills.") {
				done <- fmt.Errorf("bad write policy/scope: standalone=%+v policy=%+v", raw.SelectedStandaloneSkills, raw.SkillWritePolicy)
				return json.RawMessage(`{"summary":"bad","nothing_to_save":true}`), nil
			}
			done <- nil
			return json.RawMessage(`{"summary":"ok","nothing_to_save":true}`), nil
		default:
			return nil, fmt.Errorf("unexpected hook %s", hook.ID)
		}
	}), nil)

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleRunner(runner)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))

	turn := worker.PrepareLifecycleTurn(ctx, models.Task{ID: "task-learning", ProjectID: "default", AgentDefinitionID: &agent.ID})
	turn.AfterComplete(nil, llmcontracts.ChatContext{})

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for after_complete hook")
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestAfterCompleteEligibilityRunsProtectedGoalAgentOnlyForActiveGoal(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	goalSvc := NewTaskGoalService(repository.NewTaskGoalRepo(db), taskRepo, nil)
	w := &WorkerService{agentRepo: agentRepo, taskGoalSvc: goalSvc}

	project := &models.Project{Name: "Goal Eligibility Project"}
	if err := repository.NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := models.Task{ID: "task-goal-eligibility", ProjectID: project.ID, Title: "Goal eligibility", Prompt: "work", Category: models.CategoryActive, Status: models.StatusRunning}
	if err := taskRepo.Create(ctx, &task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	otherHook := models.AgentLifecycleHook{ID: "other-hook", AgentID: "other-agent", When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", Enabled: true}
	if !w.afterCompleteHookEligible(ctx, task)(otherHook) {
		t.Fatal("generic after_complete must continue to run non-goal hooks sharing the evaluate_task_goal skill key")
	}

	goalAgent := &models.Agent{Key: models.AgentSystemKindGoal, Name: "System: Goal Agent", Model: "inherit", SystemKind: models.AgentSystemKindGoal, GeneratedStatus: models.AgentStatusProtected, CreatedBy: models.AgentCreatedBySystem, Enabled: true}
	if err := agentRepo.Create(ctx, goalAgent); err != nil {
		t.Fatalf("create goal agent: %v", err)
	}
	goalHook := models.AgentLifecycleHook{ID: "goal-hook", AgentID: goalAgent.ID, When: models.LifecycleAfterComplete, SkillKey: "evaluate_task_goal", Enabled: true}
	if w.afterCompleteHookEligible(ctx, task)(goalHook) {
		t.Fatal("protected Goal Agent hook must not run when the task has no active goal")
	}
	goal, err := goalSvc.SetGoal(ctx, task.ID, "Keep the goal active", GoalOptions{})
	if err != nil {
		t.Fatalf("set goal: %v", err)
	}
	if !w.afterCompleteHookEligible(ctx, task)(goalHook) {
		t.Fatal("protected Goal Agent hook must run through generic after_complete for ordinary task turns with an active goal")
	}
	taskThreadCtx := WithTaskThreadLifecycleTurn(ctx)
	if !w.afterCompleteHookEligible(taskThreadCtx, task)(goalHook) {
		t.Fatal("protected Goal Agent hook must run through generic after_complete for task-thread turns with an active goal")
	}
	if err := goalSvc.PauseActiveGoalStoppedByUser(ctx, task.ID); err != nil {
		t.Fatalf("pause after user stop: %v", err)
	}
	if w.afterCompleteHookEligible(taskThreadCtx, task)(goalHook) {
		t.Fatal("protected Goal Agent hook must not run after the user stopped and paused the goal")
	}
	if err := goalSvc.ResumeGoal(ctx, task.ID, "user"); err != nil {
		t.Fatalf("resume user-stopped goal: %v", err)
	}
	if !w.afterCompleteHookEligible(taskThreadCtx, task)(goalHook) {
		t.Fatal("protected Goal Agent hook must run again after the user resumes the goal")
	}
	_, err = goalSvc.MarkAchieved(ctx, task.ID, goal.GoalID, "done")
	if err != nil {
		t.Fatalf("mark achieved: %v", err)
	}
	if w.afterCompleteHookEligible(taskThreadCtx, task)(goalHook) {
		t.Fatal("protected Goal Agent hook must not run when the goal is no longer active")
	}
}

// ---------------------------------------------------------------------------
// Always-use routing merge tests
// ---------------------------------------------------------------------------

func TestPrepareLifecycleTurn_AlwaysUseSkillsMergedAfterRouteTask(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "curator_skill", "body of curator")
	writeLifecycleStandaloneSkill(t, root, "always_skill", "body of always")

	// Mark always_skill as always_use in the SKILLS.md frontmatter.
	skillsIndex := filepath.Join(root, "skills", "SKILLS.md")
	if err := setAlwaysUseInIndex(skillsIndex, "always_skill"); err != nil {
		t.Fatalf("set always use: %v", err)
	}

	// route_task selects only curator_skill; always_skill must be injected.
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routePayload([]string{"curator_skill"}, 0.5),
		"b-high": routePayload([]string{"curator_skill"}, 0.9),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-always-use-merge"})
	ltc := lifecycleTurnFromContext(turn.Ctx)
	handles := ltc.SelectedSkillHandles

	if !containsHandle(handles, "curator_skill") {
		t.Errorf("curator_skill (skill_curator selected) must appear in merged handles; got %v", handles)
	}
	if !containsHandle(handles, "always_skill") {
		t.Fatalf("always_skill must be injected via always_use; got %v", handles)
	}
	if len(handles) != 2 {
		t.Fatalf("expected exactly 2 handles (no duplicates), got %v", handles)
	}
}

func TestPrepareLifecycleTurn_AlwaysUseProvenanceTracked(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "curator_skill", "body")
	writeLifecycleStandaloneSkill(t, root, "always_skill", "body")
	writeLifecycleStandaloneSkill(t, root, "shared_skill", "body") // both always_use AND selected by curator

	skillsIndex := filepath.Join(root, "skills", "SKILLS.md")
	if err := setAlwaysUseInIndex(skillsIndex, "always_skill", "shared_skill"); err != nil {
		t.Fatalf("set always use: %v", err)
	}

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routePayload([]string{"curator_skill", "shared_skill"}, 0.5),
		"b-high": routePayload([]string{"curator_skill", "shared_skill"}, 0.9),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-provenance"})
	ltc := lifecycleTurnFromContext(turn.Ctx)
	prov := ltc.SelectedSkillsProvenance

	if prov["curator_skill"] != agentskills.ProvenanceSkillCurator {
		t.Errorf("curator_skill: expected %q, got %q", agentskills.ProvenanceSkillCurator, prov["curator_skill"])
	}
	if prov["always_skill"] != agentskills.ProvenanceAlwaysUse {
		t.Errorf("always_skill: expected %q, got %q", agentskills.ProvenanceAlwaysUse, prov["always_skill"])
	}
	if prov["shared_skill"] != agentskills.ProvenanceBoth {
		t.Errorf("shared_skill: expected %q (both), got %q", agentskills.ProvenanceBoth, prov["shared_skill"])
	}
}

func TestPrepareLifecycleTurn_DisabledAlwaysUseSkillExcluded(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "active_skill", "body")
	// Write a disabled standalone skill.
	writeLifecycleDisabledStandaloneSkill(t, root, "disabled_always", "body")

	skillsIndex := filepath.Join(root, "skills", "SKILLS.md")
	if err := setAlwaysUseInIndex(skillsIndex, "active_skill", "disabled_always"); err != nil {
		t.Fatalf("set always use: %v", err)
	}

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routePayload(nil, 0.1),
		"b-high": routePayload(nil, 0.1),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-disabled-always"})
	ltc := lifecycleTurnFromContext(turn.Ctx)
	handles := ltc.SelectedSkillHandles

	for _, h := range handles {
		if h == "disabled_always" {
			t.Fatalf("disabled always_use skill must be excluded; got handles %v", handles)
		}
	}
	if !containsHandle(handles, "active_skill") {
		t.Fatalf("enabled always_use skill must be included; got handles %v", handles)
	}
}

func TestPrepareLifecycleTurn_AlwaysUseNotAppliedToAssignedAgentTasks(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "standalone_always", "body")
	writeLifecycleTestSkill(t, root, "skill_curator", "maintain_skill_library", "body")

	skillsIndex := filepath.Join(root, "skills", "SKILLS.md")
	if err := setAlwaysUseInIndex(skillsIndex, "standalone_always"); err != nil {
		t.Fatalf("set always use: %v", err)
	}

	ctx := context.Background()
	db := testutil.NewTestDB(t)
	agentRepo := repository.NewAgentRepo(db)
	agent, err := agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil || agent == nil {
		t.Fatalf("load system agent: %v / %v", err, agent)
	}

	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleAgentRepo(agentRepo)
	worker.SetLifecycleRepo(repository.NewLifecycleRepo(db))

	turn := worker.PrepareLifecycleTurn(ctx, models.Task{
		ID: "task-agent-no-always", Category: models.CategoryActive,
		AgentDefinitionID: &agent.ID,
	})
	ltc := lifecycleTurnFromContext(turn.Ctx)
	handles := ltc.SelectedSkillHandles

	for _, h := range handles {
		if h == "standalone_always" {
			t.Fatalf("standalone always_use must not pollute assigned-agent task; got handles %v", handles)
		}
	}
}

func TestPrepareLifecycleTurn_AlwaysUseDeduplicatesExistingHandles(t *testing.T) {
	root := t.TempDir()
	writeLifecycleStandaloneSkill(t, root, "skill_a", "body")

	skillsIndex := filepath.Join(root, "skills", "SKILLS.md")
	if err := setAlwaysUseInIndex(skillsIndex, "skill_a"); err != nil {
		t.Fatalf("set always use: %v", err)
	}

	// route_task also selects skill_a
	worker := NewWorkerService(nil, 0, nil)
	worker.SetLifecycleSkillRoot(root)
	worker.SetLifecycleRunner(routeTestRunner(map[string]json.RawMessage{
		"a-low":  routePayload([]string{"skill_a"}, 0.5),
		"b-high": routePayload([]string{"skill_a"}, 0.9),
	}))

	turn := worker.PrepareLifecycleTurn(context.Background(), models.Task{ID: "task-dedup"})
	ltc := lifecycleTurnFromContext(turn.Ctx)
	handles := ltc.SelectedSkillHandles

	count := 0
	for _, h := range handles {
		if h == "skill_a" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("skill_a must appear exactly once after dedupliation; got %d times in %v", count, handles)
	}
}

// setAlwaysUseInIndex is a test helper that writes always_use handles into
// the SKILLS.md frontmatter at the given index path using the production mutation.
func setAlwaysUseInIndex(indexPath string, handles ...string) error {
	for _, h := range handles {
		if err := agentlibrary.SetSkillAlwaysUse(indexPath, h, true); err != nil {
			return err
		}
	}
	return nil
}

// writeLifecycleDisabledStandaloneSkill writes a disabled standalone skill to root
// and appends its header to SKILLS.md.
func writeLifecycleDisabledStandaloneSkill(t *testing.T, root, skill, body string) {
	t.Helper()
	content := "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n  key: " + skill + "\n  scope: global\n  enabled: false\n---\n" + body
	if err := os.MkdirAll(filepath.Join(root, "skills", skill), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "skills", skill, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// Append to SKILLS.md even though it's disabled (it should appear in the index
	// but be excluded from BuildCatalog which filters disabled skills).
	appendLifecycleTestHeader(t, filepath.Join(root, "skills", "SKILLS.md"), skill)
}

func containsHandle(handles []string, target string) bool {
	for _, h := range handles {
		if h == target {
			return true
		}
	}
	return false
}

func findLifecycleTurnUnderusedSkill(rows []models.UnderusedSkillMetric, handle string) *models.UnderusedSkillMetric {
	for i := range rows {
		if rows[i].SkillHandle == handle {
			return &rows[i]
		}
	}
	return nil
}
