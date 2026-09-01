package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
)

const lifecycleHookExecutionTimeout = 10 * time.Minute

// LifecycleTurn is the result of preparing a model turn for lifecycle hooks.
// It carries the prepared context (with runtime tools and prompt context
// attached) and an AfterComplete closure the caller must invoke once the
// underlying LLM call returns.
//
// The same struct is used by initial task runs (WorkerService.executeTask)
// and by task-thread followups (handler.processStreamingResponse). The
// runbook (§Lifecycle Slots line 296 + §Task-Thread Followup lines 94-99)
// requires lifecycle hooks to run on every model turn, including followups.
type LifecycleTurn struct {
	// Ctx is the prepared context. Caller passes it to LLMService.
	Ctx context.Context

	// Task is the task that will be executed. Lifecycle routing no longer
	// changes AgentDefinitionID; agents are selected manually/defaulted, while
	// route_task only selects relevant skills for this turn.
	Task models.Task

	// AfterComplete must be invoked once the LLM call returns so the
	// after_complete hook slot fires. Safe to call with err == nil. The
	// runbook (§Execution And Queueing line 1806) says after_complete runs
	// asynchronously; this closure handles that for you.
	AfterComplete func(err error, chatContext llmcontracts.ChatContext)
}

// PrepareRecallOnlyLifecycleTurn runs only Memory Curator recall hooks for an
// interactive chat turn. It deliberately skips skill selection, task runtime
// tools, and after_complete so Chat can recall managed memory without letting
// Chat prompts or mode-control text update memory.
func (w *WorkerService) PrepareRecallOnlyLifecycleTurn(ctx context.Context, task models.Task) LifecycleTurn {
	if w == nil {
		return LifecycleTurn{Ctx: ctx, Task: task, AfterComplete: func(error, llmcontracts.ChatContext) {}}
	}

	incomingTurn := lifecycleTurnFromContext(ctx)
	effectiveTask := task
	if incomingTurn.TurnPrompt != "" {
		effectiveTask.Prompt = incomingTurn.TurnPrompt
	}
	explicitMemoryEntries := w.explicitIndexedMemoryEntries(ctx, effectiveTask)
	runID := newLifecycleTaskRunID(task.ID)
	if projectRoot := projectSkillRoot(ctx, w.projectRepo, task.ProjectID); projectRoot != "" && w.agentRootSyncService != nil {
		if err := w.agentRootSyncService.SyncRootDeclarationsForProject(ctx, projectRoot, task.ProjectID); err != nil {
			applog.Infof("[lifecycle-turn] sync agent root declarations failed task=%s: %v", task.ID, err)
		}
	}
	catalog := w.buildSkillCatalog(ctx, task)
	w.currentCatalog.Store(catalog)
	hookCtx := ctx
	if hookReadTools := w.buildLifecycleReadRuntimeTools(task, catalog); hookReadTools != nil {
		hookCtx = llmcontracts.WithRuntimeTools(hookCtx, hookReadTools)
	}
	preparedContext := ""
	selectedMemoryEntries := []memory.SelectedMemory{}
	if w.lifecycleRunner != nil {
		if route := w.runLifecycleSlotFiltered(hookCtx, models.LifecycleRouteTask, task, runID, nil, llmcontracts.ChatContext{}, w.isChatMemoryRecallHook(ctx)); len(route.Outputs) > 0 {
			if selected, ok := bestSelectedMemories(route.Outputs); ok {
				selectedMemoryEntries = w.mergeSelectedMemoryEntries(explicitMemoryEntries, w.filterSelectedMemoryEntries(ctx, task, memoryEntriesFromLifecycle(selected)))
			}
		}
		preparedContext = memory.RenderSelectedMemoriesMarkdown(selectedMemoryEntries)
		before := w.runLifecycleSlotFiltered(hookCtx, models.LifecycleBeforeRun, task, runID, nil, llmcontracts.ChatContext{}, w.isChatMemoryRecallHook(ctx))
		legacyContext := lifecycle.MergeContextBlocks(before.Outputs)
		preparedContext = joinLifecyclePromptBlocks(preparedContext, legacyContext)
		if preparedContext != "" {
			applog.Infof("[lifecycle-turn] chat memory prepared_context task=%s bytes=%d", task.ID, len(preparedContext))
		}
	}
	promptContext := buildLifecyclePromptContext("", preparedContext)
	if promptContext != "" {
		ctx = withAdditionalProjectInstructions(ctx, promptContext)
	}
	if len(selectedMemoryEntries) > 0 {
		ctx = WithSelectedMemoryHandles(ctx, selectedMemoryHandles(selectedMemoryEntries))
	}
	if memoryTools := w.buildTaskMemoryRuntimeTools(ctx, task, selectedMemoryEntries); memoryTools != nil {
		ctx = llmcontracts.WithRuntimeTools(ctx, llmcontracts.CompositeRuntimeTools(memoryTools, llmcontracts.RuntimeToolsFromContext(ctx)))
	}
	return LifecycleTurn{Ctx: ctx, Task: task, AfterComplete: func(error, llmcontracts.ChatContext) {}}
}

// PrepareLifecycleTurn runs the route_task + before_run lifecycle slots for
// a model turn and returns the prepared context. The caller passes the
// resulting Ctx to the LLM and then invokes AfterComplete(err, chatContext)
// with the model-facing chat context from that LLM turn once the LLM returns.
//
// This method is the single integration point for lifecycle behavior. Both
// initial task runs and task-thread followups must call it so selected skills,
// skill runtime tools, and any recall outputs are consistently delivered to the
// model per runbook §Lifecycle Slots line 296.
func (w *WorkerService) PrepareLifecycleTurn(ctx context.Context, task models.Task) LifecycleTurn {
	if w == nil {
		return LifecycleTurn{Ctx: ctx, Task: task, AfterComplete: func(error, llmcontracts.ChatContext) {}}
	}

	runID := newLifecycleTaskRunID(task.ID)
	incomingTurn := lifecycleTurnFromContext(ctx)
	projectRoot := projectSkillRoot(ctx, w.projectRepo, task.ProjectID)
	assignedAgent := w.taskAgentDefinition(ctx, task)
	if w.agentRootSyncService != nil {
		if err := w.agentRootSyncService.SyncRootDeclarationsForProject(ctx, projectRoot, task.ProjectID); err != nil {
			applog.Infof("[lifecycle-turn] sync agent root declarations failed task=%s: %v", task.ID, err)
		}
	}
	catalog := w.buildSkillCatalog(ctx, task)
	w.currentCatalog.Store(catalog)
	fullSkillIndex := w.renderAvailableSkillsForTask(ctx, task, projectRoot)
	taskTurnRuntimeTools := llmcontracts.RuntimeToolsFromContext(ctx)
	afterCompleteRuntimeTools := incomingTurn.AfterCompleteRuntimeTools
	if w.afterCompleteRuntimeToolProvider != nil {
		afterCompleteRuntimeTools = llmcontracts.CompositeRuntimeTools(afterCompleteRuntimeTools, w.afterCompleteRuntimeToolProvider(ctx, task))
	}
	ctx = withLifecycleTurnContext(ctx, lifecycleTurnContext{Catalog: catalog, SkillIndex: fullSkillIndex, AssignedAgent: assignedAgent, AfterCompleteRuntimeTools: afterCompleteRuntimeTools, TaskThreadTurn: incomingTurn.TaskThreadTurn, TurnPrompt: incomingTurn.TurnPrompt, TaskRunID: runID})
	hookReadTools := w.buildLifecycleReadRuntimeTools(task, catalog)
	if hookReadTools != nil {
		ctx = llmcontracts.WithRuntimeTools(ctx, hookReadTools)
	}
	hookMutationTools := w.buildLifecycleRuntimeTools(task, catalog)
	applog.Infof("[lifecycle-turn] prepared task=%s catalog_skills=%d runtime_tools=%t", task.ID, len(catalog.Entries()), hookReadTools != nil)
	effectiveTask := task
	if incomingTurn.TaskThreadTurn && incomingTurn.TurnPrompt != "" {
		effectiveTask.Prompt = incomingTurn.TurnPrompt
	}
	explicitMemoryEntries := w.explicitIndexedMemoryEntries(ctx, effectiveTask)

	// route_task selects relevant skill handles for this turn. It never changes
	// Task.AgentDefinitionID. No-agent tasks route standalone skills; assigned-agent
	// tasks route skills owned by the assigned agent.
	selectedSkillHandles := []string{}
	var selectedMemories lifecycle.SelectedMemories
	haveSelectedMemories := false
	// routeTaskExecID holds the lifecycle_executions row ID for the winning
	// selected_skills output so we can patch it after the always-use merge.
	routeTaskExecID := ""
	if w.lifecycleRunner != nil {
		if route := w.runLifecycleSlot(ctx, models.LifecycleRouteTask, task, runID, nil, llmcontracts.ChatContext{}); len(route.Outputs) > 0 {
			var best lifecycle.SelectedSkills
			var bestExecID string
			haveBest := false
			for _, out := range route.Outputs {
				if len(out.Payload) == 0 || out.Error != "" {
					continue
				}
				switch out.OutputContract {
				case models.OutputContractSelectedSkills:
					selected, err := lifecycle.ValidateSelectedSkills(out.Payload)
					if err != nil {
						applog.Infof("[lifecycle-turn] route_task invalid selected_skills task=%s: %v", task.ID, err)
						continue
					}
					if selected.NeedsClarification {
						applog.Infof("[lifecycle-turn] route_task clarification requested task=%s question=%q", task.ID, selected.ClarifyingQuestion)
						continue
					}
					if !haveBest || selected.Confidence > best.Confidence {
						best = selected
						bestExecID = out.ExecutionID
						haveBest = true
					}
				case models.OutputContractSelectedMemories:
					selected, err := lifecycle.ValidateSelectedMemories(out.Payload)
					if err != nil {
						applog.Infof("[lifecycle-turn] route_task invalid selected_memories task=%s: %v", task.ID, err)
						continue
					}
					if selected.NeedsClarification {
						applog.Infof("[lifecycle-turn] route_task memory clarification requested task=%s question=%q", task.ID, selected.ClarifyingQuestion)
						continue
					}
					if !haveSelectedMemories || selected.Confidence > selectedMemories.Confidence {
						selectedMemories = selected
						haveSelectedMemories = true
					}
				}
			}
			if haveBest {
				selectedSkillHandles = filterCatalogHandles(catalog, best.Skills)
				routeTaskExecID = bestExecID
				applog.Infof("[lifecycle-turn] route_task selected_skills task=%s handles=%d confidence=%.2f", task.ID, len(selectedSkillHandles), best.Confidence)
			}
			if haveSelectedMemories {
				applog.Infof("[lifecycle-turn] route_task selected_memories task=%s memories=%d confidence=%.2f", task.ID, len(selectedMemories.Memories), selectedMemories.Confidence)
			}
		}
	}

	// Merge always-use handles from SKILLS.md frontmatter into selectedSkillHandles.
	// This is only applied for standalone (non-agent-owned) tasks; assigned-agent
	// tasks use their agent's private skill catalog and must not be polluted by the
	// top-level standalone always_use settings.
	var selectedSkillsProvenance agentskills.SkillSelectionProvenance
	if !catalog.IsAgentOwned() {
		selectedSkillHandles, selectedSkillsProvenance = agentskills.MergeAlwaysUseIntoSelected(catalog, w.globalSkillRoot, projectRoot, selectedSkillHandles)
		logAlwaysUseProvenance(task.ID, selectedSkillsProvenance)
		// Patch the stored route_task output_json so the Lifecycle tab reflects
		// the full merged skill list (always-use + skill_curator) rather than
		// only the LLM-selected subset.
		if routeTaskExecID != "" && w.lifecycleRepo != nil {
			if err := w.lifecycleRepo.PatchExecutionOutputSkills(ctx, routeTaskExecID, selectedSkillHandles); err != nil {
				applog.Infof("[lifecycle-turn] patch route_task output_json task=%s exec=%s: %v", task.ID, routeTaskExecID, err)
			}
		}
	} else {
		selectedSkillsProvenance = make(agentskills.SkillSelectionProvenance, len(selectedSkillHandles))
		for _, h := range selectedSkillHandles {
			selectedSkillsProvenance[h] = agentskills.ProvenanceSkillCurator
		}
	}

	w.recordSelectedSkillEvents(ctx, task, catalog, selectedSkillHandles, selectedSkillsProvenance, lifecycleTurnContext{AssignedAgent: assignedAgent, TaskThreadTurn: incomingTurn.TaskThreadTurn, TurnPrompt: incomingTurn.TurnPrompt, TaskRunID: runID})
	taskCatalog := catalog.Filter(runID+":selected", selectedSkillHandles)
	ctx = withLifecycleTurnContext(ctx, lifecycleTurnContext{Catalog: catalog, SelectedSkillHandles: selectedSkillHandles, SelectedSkillsProvenance: selectedSkillsProvenance, AssignedAgent: assignedAgent, AfterCompleteRuntimeTools: afterCompleteRuntimeTools, TaskThreadTurn: incomingTurn.TaskThreadTurn, TurnPrompt: incomingTurn.TurnPrompt, TaskRunID: runID})

	// before_run: produce context_blocks the model should see. The runbook
	// (§Auto-Routing line 130) says these blocks are merged into the system
	// prompt before the active agent runs.
	preparedContext := ""
	if w.lifecycleRunner != nil {
		before := w.runLifecycleSlot(ctx, models.LifecycleBeforeRun, task, runID, nil, llmcontracts.ChatContext{})
		preparedContext = lifecycle.MergeContextBlocks(before.Outputs)
		if preparedContext != "" {
			applog.Infof("[lifecycle-turn] before_run prepared_context task=%s bytes=%d outputs=%d", task.ID, len(preparedContext), len(before.Outputs))
		}
	}

	skillIndex := agentskills.RenderSelectedSkillsMarkdown(taskCatalog, selectedSkillHandles)
	taskRuntimeTools := llmcontracts.CompositeRuntimeTools(taskTurnRuntimeTools, w.buildTaskSkillRuntimeTools(ctx, task, taskCatalog))
	if taskRuntimeTools != nil {
		ctx = llmcontracts.WithRuntimeTools(ctx, taskRuntimeTools)
	}
	ctx = withLifecycleTurnContext(ctx, lifecycleTurnContext{
		Catalog:                  taskCatalog,
		SkillIndex:               skillIndex,
		PreparedBlocks:           preparedContext,
		SelectedSkillHandles:     selectedSkillHandles,
		SelectedSkillsProvenance: selectedSkillsProvenance,
		AssignedAgent:            assignedAgent,
		TaskThreadTurn:           incomingTurn.TaskThreadTurn,
		TurnPrompt:               incomingTurn.TurnPrompt,
		TaskRunID:                runID,
	})
	selectedMemoryEntries := explicitMemoryEntries
	if haveSelectedMemories {
		selectedMemoryEntries = w.mergeSelectedMemoryEntries(selectedMemoryEntries, w.filterSelectedMemoryEntries(ctx, task, memoryEntriesFromLifecycle(selectedMemories)))
	}
	memoryIndex := memory.RenderSelectedMemoriesMarkdown(selectedMemoryEntries)
	if len(selectedMemoryEntries) > 0 {
		ctx = WithSelectedMemoryHandles(ctx, selectedMemoryHandles(selectedMemoryEntries))
	}
	if memoryTools := w.buildTaskMemoryRuntimeTools(ctx, task, selectedMemoryEntries); memoryTools != nil {
		taskRuntimeTools = llmcontracts.CompositeRuntimeTools(memoryTools, taskRuntimeTools)
		ctx = llmcontracts.WithRuntimeTools(ctx, taskRuntimeTools)
	}
	promptContext := buildLifecyclePromptContext(skillIndex, joinLifecyclePromptBlocks(memoryIndex, preparedContext))
	if promptContext != "" {
		ctx = withAdditionalProjectInstructions(ctx, promptContext)
	}

	after := func(err error, chatContext llmcontracts.ChatContext) {
		if w.lifecycleRunner == nil {
			return
		}
		if isLifecycleCancellation(err) {
			applog.Infof("[lifecycle-turn] skipping after_complete for cancelled task=%s run=%s: %v", task.ID, runID, err)
			return
		}
		// Run after_complete in a detached goroutine so it never blocks
		// caller dispatch. Runbook §Execution And Queueing line 1806.
		go func(t models.Task, taskRunID string, runErr error, taskChatContext llmcontracts.ChatContext, rt *llmcontracts.RuntimeTools, turn lifecycleTurnContext) {
			defer func() {
				if rec := recover(); rec != nil {
					applog.Infof("[lifecycle-turn] after_complete panic for task=%s: %v", t.ID, rec)
				}
			}()
			bgCtx, cancel := context.WithTimeout(context.Background(), lifecycleHookExecutionTimeout)
			defer cancel()
			bgCtx = withLifecycleTurnContext(bgCtx, turn)
			if rt != nil {
				bgCtx = llmcontracts.WithRuntimeTools(bgCtx, rt)
			}
			if w.shouldSkipAfterCompleteForCancellation(bgCtx, t.ID, runErr) {
				applog.Infof("[lifecycle-turn] skipping after_complete for task=%s run=%s because task cancellation was requested", t.ID, taskRunID)
				return
			}
			result := w.runLifecycleSlotFiltered(bgCtx, models.LifecycleAfterComplete, t, taskRunID, runErr, taskChatContext, w.afterCompleteHookEligible(bgCtx, t))
			w.publishGoalEvaluationAfterComplete(bgCtx, t, result)
		}(task, runID, err, chatContext, llmcontracts.CompositeRuntimeTools(hookMutationTools, afterCompleteRuntimeTools), lifecycleTurnContext{Catalog: taskCatalog, SelectedSkillHandles: selectedSkillHandles, SelectedSkillsProvenance: selectedSkillsProvenance, AssignedAgent: assignedAgent, AfterCompleteRuntimeTools: afterCompleteRuntimeTools, TaskThreadTurn: incomingTurn.TaskThreadTurn, TurnPrompt: incomingTurn.TurnPrompt, TaskRunID: runID})
	}
	return LifecycleTurn{Ctx: ctx, Task: task, AfterComplete: after}
}

func isLifecycleCancellation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch msg {
	case "task cancelled", "task canceled", "task cancelled by user", "task canceled by user", "context canceled", "context cancelled":
		return true
	default:
		return false
	}
}

func (w *WorkerService) shouldSkipAfterCompleteForCancellation(ctx context.Context, taskID string, runErr error) bool {
	if isLifecycleCancellation(runErr) || w.IsCancellationRequested(taskID) {
		return true
	}
	// A successful model return can race with a user stop that persisted cancelled
	// status before the detached hook starts. For ordinary failures, including
	// timeouts/deadlines, after_complete must still run with execution_error even
	// if later terminal bookkeeping stores the execution/task as cancelled.
	return runErr == nil && w.taskIsCancelled(ctx, taskID)
}

func (w *WorkerService) taskIsCancelled(ctx context.Context, taskID string) bool {
	if w == nil || w.taskRepo == nil || taskID == "" {
		return false
	}
	task, err := w.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		applog.Infof("[lifecycle-turn] cancelled-state check failed task=%s: %v", taskID, err)
		return false
	}
	return task != nil && task.Status == models.StatusCancelled
}

func (w *WorkerService) publishGoalEvaluationAfterComplete(ctx context.Context, task models.Task, result lifecycle.SlotResult) {
	if w == nil || w.taskGoalSvc == nil || !slotResultContainsGoalAgent(ctx, w, result) {
		return
	}
	if _, err := w.taskGoalSvc.PublishEvaluatedGoal(ctx, task.ID); err != nil {
		applog.Infof("[lifecycle-turn] reload evaluated task goal failed task=%s: %v", task.ID, err)
	}
}

func slotResultContainsGoalAgent(ctx context.Context, w *WorkerService, result lifecycle.SlotResult) bool {
	goalAgentID := w.goalAgentID(ctx)
	if goalAgentID == "" {
		return false
	}
	for _, out := range result.Outputs {
		if out.AgentID == goalAgentID && out.SkillKey == "evaluate_task_goal" {
			return true
		}
	}
	return false
}

func (w *WorkerService) afterCompleteHookEligible(ctx context.Context, task models.Task) func(models.AgentLifecycleHook) bool {
	goalAgentID := w.goalAgentID(ctx)
	return func(hook models.AgentLifecycleHook) bool {
		if hook.When != models.LifecycleAfterComplete || !hook.Enabled {
			return false
		}
		if goalAgentID == "" || hook.AgentID != goalAgentID {
			return true
		}
		return w.taskHasEvaluableGoal(ctx, task.ID)
	}
}

func (w *WorkerService) taskHasEvaluableGoal(ctx context.Context, taskID string) bool {
	return w.evaluableTaskGoal(ctx, taskID) != nil
}

func (w *WorkerService) evaluableTaskGoal(ctx context.Context, taskID string) *models.TaskGoal {
	if w == nil || w.taskGoalSvc == nil || taskID == "" {
		return nil
	}
	goal, err := w.taskGoalSvc.GetEvaluableGoal(ctx, taskID)
	if err != nil {
		applog.Infof("[lifecycle-turn] load evaluable task goal failed task=%s: %v", taskID, err)
		return nil
	}
	return goal
}

func (w *WorkerService) goalAgentID(ctx context.Context) string {
	if w == nil || w.agentRepo == nil {
		return ""
	}
	agent, err := w.agentRepo.GetBySystemKind(ctx, models.AgentSystemKindGoal)
	if err != nil || agent == nil {
		return ""
	}
	return agent.ID
}

func (w *WorkerService) isChatMemoryRecallHook(ctx context.Context) func(models.AgentLifecycleHook) bool {
	memoryAgentID := ""
	if w != nil && w.agentRepo != nil {
		if agent, err := w.agentRepo.GetByKey(ctx, models.AgentSystemKindMemoryCurator); err == nil && agent != nil {
			memoryAgentID = agent.ID
		}
	}
	return func(hook models.AgentLifecycleHook) bool {
		if hook.SkillKey != "recall_memory" {
			return false
		}
		if !(hook.When == models.LifecycleRouteTask && hook.OutputContract == models.OutputContractSelectedMemories) && !(hook.When == models.LifecycleBeforeRun && hook.OutputContract == models.OutputContractContextBlock) {
			return false
		}
		if memoryAgentID == "" {
			return w == nil || w.agentRepo == nil
		}
		return hook.AgentID == memoryAgentID
	}
}

func bestSelectedMemories(outputs []lifecycle.HookOutput) (lifecycle.SelectedMemories, bool) {
	var best lifecycle.SelectedMemories
	haveBest := false
	for _, out := range outputs {
		if out.OutputContract != models.OutputContractSelectedMemories || len(out.Payload) == 0 || out.Error != "" {
			continue
		}
		selected, err := lifecycle.ValidateSelectedMemories(out.Payload)
		if err != nil || selected.NeedsClarification {
			continue
		}
		if !haveBest || selected.Confidence > best.Confidence {
			best = selected
			haveBest = true
		}
	}
	return best, haveBest
}

func memoryEntriesFromLifecycle(selected lifecycle.SelectedMemories) []memory.SelectedMemory {
	if len(selected.Memories) == 0 {
		return nil
	}
	out := make([]memory.SelectedMemory, 0, len(selected.Memories))
	for _, entry := range selected.Memories {
		out = append(out, memory.SelectedMemory{
			File:    entry.File,
			Topic:   entry.Topic,
			Summary: entry.Summary,
			Snippet: entry.Snippet,
		})
	}
	return out
}

func selectedMemoryHandles(entries []memory.SelectedMemory) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	seen := map[string]struct{}{}
	for _, entry := range entries {
		handle := memory.NormalizeMemoryHandle(entry.File)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, handle)
	}
	return out
}

func (w *WorkerService) filterSelectedMemoryEntries(ctx context.Context, task models.Task, entries []memory.SelectedMemory) []memory.SelectedMemory {
	if len(entries) == 0 {
		return nil
	}
	allowed := memory.IndexedMemoryHandles(w.availableMemoryIndex(ctx, task))
	if len(allowed) == 0 {
		for _, entry := range entries {
			if entry.File != "" {
				applog.Infof("[lifecycle-turn] route_task selected memory without MEMORIES.md index task=%s handle=%s", task.ID, entry.File)
			}
		}
		return nil
	}
	out := make([]memory.SelectedMemory, 0, len(entries))
	for _, entry := range entries {
		handle := memory.NormalizeMemoryHandle(entry.File)
		if _, ok := allowed[handle]; !ok {
			applog.Infof("[lifecycle-turn] route_task selected memory not in MEMORIES.md task=%s handle=%s", task.ID, strings.TrimSpace(entry.File))
			continue
		}
		out = append(out, memory.SelectedMemory{File: handle})
	}
	return out
}

func (w *WorkerService) explicitIndexedMemoryEntries(ctx context.Context, task models.Task) []memory.SelectedMemory {
	index := w.availableMemoryIndex(ctx, task)
	allowed := memory.IndexedMemoryHandles(index)
	if len(allowed) == 0 {
		return nil
	}
	requested := explicitMemoryViewHandles(task.Prompt)
	if len(requested) == 0 {
		return nil
	}
	out := make([]memory.SelectedMemory, 0, len(requested))
	seen := map[string]struct{}{}
	for _, handle := range requested {
		if _, ok := allowed[handle]; !ok {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, memory.SelectedMemory{File: handle})
	}
	return out
}

func explicitMemoryViewHandles(prompt string) []string {
	if !strings.Contains(strings.ToLower(prompt), "memory_view") {
		return nil
	}
	out := []string{}
	seen := map[string]struct{}{}
	for _, raw := range strings.FieldsFunc(prompt, func(ch rune) bool {
		return !(ch == '_' || ch == '-' || ch == '.' || ch == '/' || ch == '\\' || (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9'))
	}) {
		handle := memory.NormalizeMemoryHandle(raw)
		if handle == "" || !strings.HasSuffix(strings.ToLower(handle), ".md") {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, handle)
	}
	return out
}

func (w *WorkerService) mergeSelectedMemoryEntries(first, second []memory.SelectedMemory) []memory.SelectedMemory {
	if len(first) == 0 {
		return second
	}
	if len(second) == 0 {
		return first
	}
	out := make([]memory.SelectedMemory, 0, len(first)+len(second))
	seen := map[string]struct{}{}
	for _, entries := range [][]memory.SelectedMemory{first, second} {
		for _, entry := range entries {
			handle := memory.NormalizeMemoryHandle(entry.File)
			if handle == "" {
				continue
			}
			if _, ok := seen[handle]; ok {
				continue
			}
			seen[handle] = struct{}{}
			out = append(out, memory.SelectedMemory{File: handle})
		}
	}
	return out
}

func joinLifecyclePromptBlocks(parts ...string) string {
	out := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += part
	}
	return out
}

func filterCatalogHandles(catalog *agentskills.Catalog, handles []string) []string {
	out := make([]string, 0, len(handles))
	seen := map[string]struct{}{}
	for _, handle := range handles {
		if _, ok := seen[handle]; ok {
			continue
		}
		if _, ok := catalog.Lookup(handle); !ok {
			applog.Infof("[lifecycle-turn] route_task selected unknown skill handle=%s", handle)
			continue
		}
		seen[handle] = struct{}{}
		out = append(out, handle)
	}
	return out
}

func newLifecycleTaskRunID(taskID string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return taskID + ":" + hex.EncodeToString(b[:])
	}
	return taskID + ":" + time.Now().UTC().Format("20060102T150405.000000000")
}

// logAlwaysUseProvenance logs always-use injected handles so operators can see
// which skills were forced-included deterministically from catalog metadata vs
// selected by the Skill Curator LLM route hook.
func logAlwaysUseProvenance(taskID string, prov agentskills.SkillSelectionProvenance) {
	var alwaysUse, both []string
	for handle, source := range prov {
		switch source {
		case agentskills.ProvenanceAlwaysUse:
			alwaysUse = append(alwaysUse, handle)
		case agentskills.ProvenanceBoth:
			both = append(both, handle)
		}
	}
	if len(alwaysUse) > 0 {
		applog.Infof("[lifecycle-turn] always_use injected task=%s handles=%v", taskID, alwaysUse)
	}
	if len(both) > 0 {
		applog.Infof("[lifecycle-turn] always_use+skill_curator overlap task=%s handles=%v", taskID, both)
	}
}
