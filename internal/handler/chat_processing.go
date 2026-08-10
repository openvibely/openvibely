package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/update"
	"github.com/openvibely/openvibely/internal/util"
)

const (
	chatProcessingTimeout     = 30 * time.Minute // Inactivity timeout for LLM processing in background goroutines
	chatHistoryLimit          = 50               // Number of recent chat messages to load for conversation context
	taskThreadHistoryLimit    = 21               // Load at most 20 prior turns plus the current execution for filtering
	maxFileSize               = 10 << 20         // 10 MB per file
	maxFilesPerReq            = 10               // Max 10 files per request
	finalSteeringGracePeriod  = 150 * time.Millisecond
	finalSteeringPollInterval = 25 * time.Millisecond
)

var (
	// Compiled regex patterns for performance (compile once, reuse many times)
	taskIDRegex = regexp.MustCompile(`\[TASK_ID:([^\]]+)\]`)
)

// streamingResponseParams holds all parameters needed for the shared streaming
// response processor. Used by ChatSend, TaskThreadSend, and APIChatMessage to
// consolidate duplicate chat processing logic.
//
// Fields:
//   - ExecID: Execution record ID for tracking the LLM call
//   - TaskID: Task record ID (required for cancellation and status updates)
//   - Message: User's message/prompt to send to the LLM
//   - Agent: LLM configuration (model, provider, API key, etc.)
//   - ChatHistory: Prior conversation turns for context (may be empty for first message)
//   - ProjectID: Project ID for task creation/lookup
//   - SystemContext: Additional system prompt context (task list, file contents, etc.)
//   - WorkDir: Working directory for CLI agents (project repo path)
//   - ImageAttachments: Image files for vision-capable models
//   - IsTaskFollowup: true = coding agent prompt (executes code); false = orchestration prompt (creates tasks)
//   - ChatMode: orchestration mode for interactive chat (orchestrate/plan)
type streamingResponseParams struct {
	ExecID                      string
	TaskID                      string
	Message                     string
	Agent                       models.LLMConfig
	AgentDefinition             *models.Agent
	ChatHistory                 []models.Execution
	ProjectID                   string
	PrincipalID                 string
	SystemContext               string
	WorkDir                     string
	ImageAttachments            []models.Attachment
	IsTaskFollowup              bool // true = coding agent prompt; false = orchestration prompt
	ChatMode                    models.ChatMode
	Surface                     chatcontrol.Surface // chat entry point (web/api/telegram/slack)
	TelegramInitialAckMessageID int
	ChannelReply                service.ChannelReplyContext

	RuntimeOrigin      string
	RuntimeOriginAgent string
	InputOrigin        string
	InputOriginAgent   string

	// RuntimeTools holds channel-specific runtime tools pre-built by a channel
	// service (Discord, Slack, Telegram, Email). When non-nil, processStreamingResponse
	// uses these tools for this turn instead of rebuilding the generic handler
	// runtime, so switch_project and other channel-sensitive tools execute through
	// the channel service handler rather than the informational web/API path.
	RuntimeTools      *llmcontracts.RuntimeTools
	AutomationContext *models.AutomationContext

	// DeferHistoryLoad signals processStreamingResponse to load ChatHistory,
	// SystemContext, and WorkDir lazily after acquiring worker slots. Set by
	// TaskThreadSend so the HTTP handler can return immediately without blocking
	// on a full execution-history scan when a task has many prior executions.
	DeferHistoryLoad bool
	// AttachmentContext is the pre-computed attachment description text to inject
	// into the system context when DeferHistoryLoad is true.
	AttachmentContext string
	// Task is the task record supplied for deferred context loading when
	// DeferHistoryLoad is true.
	Task *models.Task

	steeringHistoryStarted bool
	steeringOutputCursor   string
	lifecycleUserMessage   string

	// Tests that inspect steering recovery before a later queued turn starts
	// suppress the asynchronous promotion launched during finalization.
	suppressQueuedTurnPromotion bool
	updateWorkDone              func()
}

func streamingTransportScope(params streamingResponseParams) string {
	if params.IsTaskFollowup {
		if strings.TrimSpace(params.TaskID) != "" {
			return "task:" + strings.TrimSpace(params.TaskID)
		}
		return ""
	}
	if strings.TrimSpace(params.ProjectID) != "" {
		return "chat:project:" + strings.TrimSpace(params.ProjectID)
	}
	return ""
}

// processStreamingResponse is the shared goroutine that handles LLM streaming for
// both chat and task follow-up messages. This function runs asynchronously in a
// background goroutine, allowing the HTTP handler to return immediately.
//
// Process flow:
//  1. Creates a timeout context and registers cancellation with worker service
//  2. Attaches authorized runtime tools and calls the LLM service for streaming output
//     (writes to DB in real-time)
//  3. Evaluates final standalone task status markers
//  4. Completes the execution and updates task status
//
// Uses context.Background() for the base context since this goroutine should
// complete independently of the HTTP request (which may be canceled when the
// client disconnects). The inactivity timeout is reset by streamed model text,
// thinking, tool starts, and tool results so active work can continue.
//
// Error handling: All errors in the completion path are logged but don't fail the
// function since we're in a background goroutine. Failed completions leave tasks
// stuck in "running" status, which is why error logging is critical.
func (h *Handler) prepareChatMemoryRecall(ctx context.Context, task models.Task) context.Context {
	if h.workerSvc == nil {
		return ctx
	}
	return h.workerSvc.PrepareRecallOnlyLifecycleTurn(ctx, task).Ctx
}

func (h *Handler) prepareAutomationTaskFollowup(ctx context.Context, params *streamingResponseParams) error {
	if params == nil || !params.IsTaskFollowup || strings.TrimSpace(params.TaskID) == "" {
		return nil
	}
	if params.Task == nil {
		task, err := h.taskRepo.GetByID(ctx, params.TaskID)
		if err != nil {
			return fmt.Errorf("loading task-thread follow-up task: %w", err)
		}
		if task == nil {
			return fmt.Errorf("task-thread follow-up task not found: %s", params.TaskID)
		}
		params.Task = task
	}
	if params.AutomationContext != nil {
		if repository.IsAutomationTaskCreatedVia(params.Task.CreatedVia) {
			params.AutomationContext.OriginTask = true
		}
		return nil
	}
	var automationContext models.AutomationContext
	var err error
	if h.automationGraphSvc != nil && strings.TrimSpace(params.ExecID) != "" {
		automationContext, err = h.automationGraphSvc.ContextForExecution(ctx, params.ProjectID, params.ExecID)
		if err != nil {
			return fmt.Errorf("loading task-thread Automation execution context: %w", err)
		}
	}
	if h.automationGraphSvc != nil && len(automationContext.Bindings) == 0 {
		automationContext, err = h.automationGraphSvc.ContextForTask(ctx, params.ProjectID, params.TaskID)
		if err != nil {
			return fmt.Errorf("loading task-thread Automation task context: %w", err)
		}
	}
	if repository.IsAutomationTaskCreatedVia(params.Task.CreatedVia) {
		automationContext.ProjectID = params.Task.ProjectID
		automationContext.OriginTask = true
	}
	if len(automationContext.Bindings) > 0 || automationContext.OriginTask {
		params.AutomationContext = &automationContext
	}
	return nil
}

func (h *Handler) automationGitHubRuntimeTools(ctx context.Context, task models.Task, defs []llmcontracts.RuntimeToolDefinition) *llmcontracts.RuntimeTools {
	if h.githubRuntimeHook != nil {
		h.githubRuntimeHook()
	}
	return h.llmSvc.AutomationGitHubRuntimeTools(ctx, task, defs)
}

// buildStreamingResponseActionRuntime assembles the request-scoped runtime used
// by processStreamingResponse. The hardened Automation GitHub runtime is built
// once here and remains first in the dispatch chain for the whole model turn.
func (h *Handler) buildStreamingResponseActionRuntime(ctx context.Context, params streamingResponseParams, collector *chatActionSummaryCollector, defs []llmcontracts.RuntimeToolDefinition, mode models.ChatMode, surface chatcontrol.Surface) *llmcontracts.RuntimeTools {
	_, automationBound := service.AutomationContextFromContext(ctx)
	automationBound = automationBound || params.AutomationContext != nil
	if automationBound {
		filtered := make([]llmcontracts.RuntimeToolDefinition, 0, len(defs))
		for _, def := range defs {
			if def.Name != "github_comment_on_issue" {
				filtered = append(filtered, def)
			}
		}
		defs = filtered
	}
	var hardenedAutomationGitHubRT *llmcontracts.RuntimeTools
	if params.IsTaskFollowup && params.Task != nil && h.llmSvc != nil {
		hardenedAutomationGitHubRT = h.automationGitHubRuntimeTools(ctx, *params.Task, defs)
	}
	genericRT := h.buildChatActionToolRuntimeFromDefs(params, collector, defs, mode, surface)
	runtime := llmcontracts.CompositeRuntimeTools(hardenedAutomationGitHubRT, llmcontracts.RuntimeToolsFromContext(ctx), params.RuntimeTools, genericRT)
	if automationBound && runtime != nil {
		baseExecutor := runtime.Executor
		runtime.Executor = func(toolCtx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			if strings.EqualFold(strings.TrimSpace(name), "github_comment_on_issue") {
				return "", true, true, errors.New("GitHub issue status comments are disabled for Automation tasks")
			}
			return baseExecutor(toolCtx, name, input)
		}
	}
	return runtime
}

func (h *Handler) startStreamingResponse(params streamingResponseParams) error {
	class := update.WorkChat
	if params.IsTaskFollowup {
		class = update.WorkTask
	}
	if params.updateWorkDone == nil && h.updateWorkTracker != nil {
		done, err := h.updateWorkTracker.Start(class)
		if err != nil {
			h.completeWithFailure(context.Background(), params.ExecID, params.TaskID, update.ErrDraining.Error(), 0, params.ChannelReply)
			return update.ErrDraining
		}
		params.updateWorkDone = done
	}
	go h.processStreamingResponse(params)
	return nil
}

func (h *Handler) processStreamingResponse(params streamingResponseParams) {
	if params.updateWorkDone != nil {
		defer params.updateWorkDone()
	}
	// Memory recall is injected for task-thread followups through the full task
	// lifecycle path below. Interactive chat uses a recall-only lifecycle path so
	// relevant project memory reaches Chat without triggering after_complete memory
	// extraction for Chat prompts/mode-control text.
	timeout := chatProcessingTimeout
	if params.IsTaskFollowup && h.workerSvc != nil {
		if modelTimeout := h.workerSvc.GetModelWorkerTimeout(params.Agent.ID); modelTimeout > 0 {
			timeout = modelTimeout
		}
	}

	waitCtx := context.Background()
	var waitCancel context.CancelFunc
	if params.IsTaskFollowup && h.workerSvc != nil {
		waitCtx, waitCancel = context.WithCancel(context.Background())
		h.registerTaskCancellation(params.TaskID, waitCancel)
	}
	cleanupWaitCancellation := func() {
		if waitCancel != nil {
			h.deregisterTaskCancellation(params.TaskID)
			waitCancel()
			waitCancel = nil
		}
	}
	cancelWaitOnly := func() {
		if waitCancel != nil {
			waitCancel()
			waitCancel = nil
		}
	}
	var ctx context.Context
	var cancel context.CancelFunc
	runtimeCancelRegistered := false
	startRuntimeCancellation := func() {
		if ctx != nil {
			return
		}
		var resetInactivity func()
		ctx, cancel, resetInactivity = withInactivityTimeout(context.Background(), timeout)
		ctx = llmcontracts.WithActivityCallback(ctx, resetInactivity)
		h.registerTaskCancellation(params.TaskID, cancel)
		runtimeCancelRegistered = true
		cancelWaitOnly()
	}
	cleanupRuntimeCancellation := func() {
		if runtimeCancelRegistered {
			h.deregisterTaskCancellation(params.TaskID)
			runtimeCancelRegistered = false
		}
		if cancel != nil {
			cancel()
			cancel = nil
		}
	}
	completeCancelledBeforeModel := func() {
		cleanupRuntimeCancellation()
		h.completeWithCancellation(params.ExecID, params.TaskID, "", 0, 0, 0, params.ChannelReply)
		h.finalizeStreamingTurn(params, "")
	}
	alreadyCancelledBeforeModel := func() bool {
		if params.TaskID != "" {
			if task, err := h.taskRepo.GetByID(ctx, params.TaskID); err == nil && task != nil && task.Status == models.StatusCancelled {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s observed cancelled task before model preparation", params.ExecID, params.TaskID)
				return true
			} else if err != nil {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s error checking task cancellation before model preparation: %v", params.ExecID, params.TaskID, err)
			}
		}
		if params.ExecID != "" {
			if exec, err := h.execRepo.GetByID(ctx, params.ExecID); err == nil && exec != nil && exec.Status == models.ExecCancelled {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s observed cancelled execution before model preparation", params.ExecID, params.TaskID)
				return true
			} else if err != nil {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s error checking execution cancellation before model preparation: %v", params.ExecID, params.TaskID, err)
			}
		}
		return false
	}

	// Enforce global, per-project, and per-model worker constraints for task follow-ups only.
	// Interactive chat (IsTaskFollowup=false) bypasses worker limits so the chat
	// orchestrator stays responsive even when all task workers are busy.
	// Task follow-ups (IsTaskFollowup=true) respect worker limits because they
	// execute code against active tasks and share resources with task workers.
	if params.IsTaskFollowup && h.workerSvc != nil {
		agentConfigID := params.Agent.ID

		// Register DispatchNext FIRST so it runs LAST (Go defers are LIFO).
		// After this thread follow-up releases project+model slots, the worker
		// pool needs to check if any queued tasks can now be dispatched.
		defer h.workerSvc.DispatchNext()

		// Block until global and per-project slots are available. This queues the
		// thread follow-up instead of rejecting it when either limit is at capacity.
		if err := h.workerSvc.AcquireProjectSlot(waitCtx, params.ProjectID); err != nil {
			applog.Infof("[handler] processStreamingResponse exec=%s task=%s cancelled waiting for project slot %s: %v",
				params.ExecID, params.TaskID, params.ProjectID, err)
			cleanupWaitCancellation()
			h.completeWithCancellation(params.ExecID, params.TaskID, "", 0, 0, 0, params.ChannelReply)
			h.finalizeStreamingTurn(params, "")
			return
		}
		defer h.workerSvc.ReleaseProjectSlot(params.ProjectID)

		// Block until a model slot is available (respects max_workers).
		if err := h.workerSvc.AcquireModelSlot(waitCtx, agentConfigID); err != nil {
			applog.Infof("[handler] processStreamingResponse exec=%s task=%s cancelled waiting for model slot for %s: %v",
				params.ExecID, params.TaskID, agentConfigID, err)
			cleanupWaitCancellation()
			h.completeWithCancellation(params.ExecID, params.TaskID, "", 0, 0, 0, params.ChannelReply)
			h.finalizeStreamingTurn(params, "")
			return
		}
		defer h.workerSvc.ReleaseModelSlot(agentConfigID)
		applog.Infof("[handler] processStreamingResponse exec=%s acquired project + model slots for %s", params.ExecID, agentConfigID)
		startRuntimeCancellation()

		// Transition task from "queued" to "running" now that worker slots are acquired
		if task, err := h.taskRepo.GetByID(ctx, params.TaskID); err == nil && task != nil {
			if task.Status == models.StatusCancelled {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s cancelled while waiting for worker slots", params.ExecID, params.TaskID)
				completeCancelledBeforeModel()
				return
			}
			if task.Status == models.StatusQueued {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s transitioning from queued to running", params.ExecID, params.TaskID)
				if err := h.taskRepo.UpdateStatus(ctx, params.TaskID, models.StatusRunning); err != nil {
					applog.Infof("[handler] processStreamingResponse exec=%s task=%s failed to update status to running: %v", params.ExecID, params.TaskID, err)
				}
			}
		}
		if ctx.Err() != nil {
			applog.Infof("[handler] processStreamingResponse exec=%s task=%s cancelled before model preparation: %v", params.ExecID, params.TaskID, ctx.Err())
			completeCancelledBeforeModel()
			return
		}
	}

	startRuntimeCancellation()
	if err := h.prepareAutomationTaskFollowup(ctx, &params); err != nil {
		applog.Infof("[handler] processStreamingResponse exec=%s task=%s Automation follow-up context error: %v", params.ExecID, params.TaskID, err)
		h.completeWithFailure(ctx, params.ExecID, params.TaskID, err.Error(), 0, params.ChannelReply)
		h.finalizeStreamingTurn(params, "")
		return
	}
	if params.AutomationContext != nil {
		ctx = service.WithAutomationContext(ctx, *params.AutomationContext)
		if params.TaskID != "" && params.ExecID != "" {
			ctx = service.WithAutomationExecution(ctx, params.TaskID, params.ExecID)
		}
	}
	defer cleanupRuntimeCancellation()
	if ctx.Err() != nil {
		applog.Infof("[handler] processStreamingResponse exec=%s task=%s cancelled before model preparation: %v", params.ExecID, params.TaskID, ctx.Err())
		completeCancelledBeforeModel()
		return
	}
	if alreadyCancelledBeforeModel() {
		completeCancelledBeforeModel()
		return
	}
	if params.IsTaskFollowup {
		h.resumeUserStoppedGoalForManualStart(ctx, params.TaskID, params.InputOrigin, params.InputOriginAgent)
		h.reactivateAchievedGoalForManualFollowup(ctx, params.TaskID, params.InputOrigin, params.InputOriginAgent)
	}

	chatMode := params.ChatMode
	if chatMode == "" {
		chatMode = models.ChatModeOrchestrate
	}
	agentDef := h.resolveTaskAgentDefinitionForTask(ctx, params.TaskID, params.AgentDefinition)
	params.AgentDefinition = agentDef
	ctx = llmcontracts.WithChatMode(ctx, chatMode)
	// Lifecycle integration for task-thread followups: lifecycle hooks must run on
	// every model turn including followups. Without this block, followups would
	// never see selected skills, skill runtime tools, or the available-skills
	// catalog. Interactive chat only runs memory recall before the model turn;
	// it intentionally skips after_complete extraction so Chat prompts and mode
	// control text are not written back to managed memory.
	var lifecycleAfter func(err error, chatContext llmcontracts.ChatContext)
	if params.TaskID != "" {
		if task, terr := h.taskRepo.GetByID(ctx, params.TaskID); terr == nil && task != nil {
			if params.IsTaskFollowup && h.workerSvc != nil {
				ctx = service.WithTaskThreadLifecycleTurnPrompt(ctx, params.Message)
				turn := h.workerSvc.PrepareLifecycleTurn(ctx, *task)
				ctx = turn.Ctx
				lifecycleAfter = turn.AfterComplete
			} else if !params.IsTaskFollowup {
				ctx = h.prepareChatMemoryRecall(ctx, *task)
			}
		}
	}

	actionCollector := newChatActionSummaryCollector()
	// Inject request-scoped action tools when the provider supports runtime tool
	// calling. Tool definitions are derived from the canonical chatcontrol registry
	// filtered by mode and surface. In plan mode only read actions are available.
	// Runtime action definitions are attached only when the concrete provider can
	// invoke them. Model text is never parsed as an action fallback.
	// Build these after lifecycle preparation so task follow-up capabilities see
	// selected memory handles/tools from the router.
	//
	// For channel surfaces (Discord, Slack, Telegram, Email), params.RuntimeTools
	// carries a complete channel-specific runtime pre-built by the channel service.
	// Using it directly ensures switch_project and other channel-sensitive tools
	// execute the channel service handler (with persistence) rather than the
	// informational web/API handler.
	if h.supportsChatActionTools(ctx, params.Agent) {
		surface := params.Surface
		if surface == "" {
			surface = chatcontrol.SurfaceWeb
		}
		var defs []llmcontracts.RuntimeToolDefinition
		if params.IsTaskFollowup {
			includeMemoryView := len(service.SelectedMemoryHandlesFromContext(ctx)) > 0 && !runtimeToolDefinitionsInclude(llmcontracts.RuntimeToolsFromContext(ctx), "memory_view")
			defs = filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, surface, true), agentDef, includeMemoryView)
		} else {
			defs = chatcontrol.ToolDefsForContext(chatMode, surface, chatMode == models.ChatModeOrchestrate)
			if agentDef != nil {
				defs = filterAssignedAgentRuntimeToolDefs(defs, agentDef)
			}
		}
		if params.RuntimeTools != nil && len(params.RuntimeTools.Definitions) > 0 {
			// Automation-specific GitHub handlers take priority over channel and
			// generic handlers. Channel-specific handlers retain priority for all
			// other tools and generic handlers remain the final fallback.
			merged := h.buildStreamingResponseActionRuntime(ctx, params, actionCollector, defs, chatMode, surface)
			ctx = llmcontracts.WithRuntimeTools(ctx, merged)
			applog.Infof("[handler] processStreamingResponse exec=%s injected channel+generic runtime action tools surface=%s followup=%v channel_defs=%d generic_defs=%d",
				params.ExecID, surface, params.IsTaskFollowup, len(params.RuntimeTools.Definitions), len(defs))
		} else if len(defs) > 0 {
			rt := h.buildStreamingResponseActionRuntime(ctx, params, actionCollector, defs, chatMode, surface)
			ctx = llmcontracts.WithRuntimeTools(ctx, rt)
			applog.Infof("[handler] processStreamingResponse exec=%s injected %d runtime action tools mode=%s surface=%s followup=%v",
				params.ExecID, len(defs), chatMode, surface, params.IsTaskFollowup)
		}
		if params.IsTaskFollowup && params.Task != nil && h.llmSvc != nil {
			bootstrapRT := h.llmSvc.AutomationBootstrapRuntimeTools(ctx, *params.Task)
			if bootstrapRT != nil {
				ctx = llmcontracts.WithRuntimeTools(ctx, llmcontracts.CompositeRuntimeTools(llmcontracts.RuntimeToolsFromContext(ctx), bootstrapRT))
			}
		}
	}
	// Lazy-load chat history, system context, and work dir for task-thread
	// follow-ups that set DeferHistoryLoad. TaskThreadSend sets this flag so
	// the HTTP handler can return immediately without blocking on a full
	// execution-history scan; the expensive DB/FS work runs here, after worker
	// slots are acquired and lifecycle hooks have run.
	if params.DeferHistoryLoad && params.IsTaskFollowup && params.Task != nil {
		priorExecs, _ := h.execRepo.ListByTaskChronologicalLimit(ctx, params.TaskID, taskThreadHistoryLimit)
		params.ChatHistory = filterChatHistory(priorExecs, params.ExecID)
		agentDefForSys := params.AgentDefinition // already resolved above
		sysCtx := combineContexts(
			buildThreadSystemContext(params.Task.Title, len(params.ChatHistory) > 0, params.AttachmentContext),
			h.taskGoalContext(ctx, params.Task.ID, agentDefForSys),
		)
		personalityCtx := h.getPersonalityContext(ctx, params.ProjectID)
		workDir, worktreeCtx, workDirErr := h.resolveWorktreeWorkDir(ctx, params.Task)
		if workDirErr != nil {
			applog.Infof("[handler] processStreamingResponse exec=%s deferred worktree error: %v", params.ExecID, workDirErr)
			h.completeWithFailure(ctx, params.ExecID, params.TaskID, workDirErr.Error(), 0)
			if lifecycleAfter != nil {
				lifecycleAfter(workDirErr, llmcontracts.ChatContext{})
			}
			h.finalizeStreamingTurn(params, "")
			return
		}
		params.SystemContext = combineContexts(combineContexts(sysCtx, worktreeCtx), personalityCtx)
		params.WorkDir = workDir
	}

	applog.Infof("[handler] processStreamingResponse exec=%s task=%s agent=%s model=%s followup=%v history=%d",
		params.ExecID, params.TaskID, params.Agent.Name, params.Agent.Model, params.IsTaskFollowup, len(params.ChatHistory))

	stopDiffBroadcast, diffBroadcastDone := h.startFollowupDiffSnapshotBroadcast(ctx, params.TaskID, params.ExecID, params.WorkDir, params.IsTaskFollowup)

	var result llmcontracts.AgentResult
	var err error
	var pendingSteering preparedSteeringBatch
	var attemptSteering preparedSteeringBatch
	var steeringCallbackParams *streamingResponseParams
	steeringCallback := func(callbackCtx context.Context) (string, error) {
		if steeringCallbackParams == nil {
			return "", nil
		}
		batch, steeringErr := h.claimPendingTextSteeringInputs(callbackCtx, steeringCallbackParams)
		if steeringErr != nil || batch.count() == 0 {
			return "", steeringErr
		}
		pendingSteering.inputs = append(pendingSteering.inputs, batch.inputs...)
		attemptSteering.inputs = append(attemptSteering.inputs, batch.inputs...)
		return formatSteeringInstruction(combinedSteeringContent(batch.inputs)), nil
	}
	start := time.Now()
	finalizeLifecycle := func(runErr error, chatContext llmcontracts.ChatContext) {
		if lifecycleAfter != nil {
			lifecycleAfter(runErr, chatContext)
		}
		if stopDiffBroadcast != nil {
			close(stopDiffBroadcast)
			stopDiffBroadcast = nil
			if diffBroadcastDone != nil {
				<-diffBroadcastDone
				diffBroadcastDone = nil
			}
		}
	}
modelLoop:
	for {
		if pendingSteering.count() == 0 {
			preparedBefore, steeringErr := h.preparePendingSteeringInputs(ctx, &params, "")
			if steeringErr != nil {
				applog.Infof("[handler] processStreamingResponse exec=%s error preparing steering before model call: %v", params.ExecID, steeringErr)
			}
			if preparedBefore.count() > 0 {
				applog.Infof("[handler] processStreamingResponse exec=%s prepared %d steering inputs before model call", params.ExecID, preparedBefore.count())
				pendingSteering = preparedBefore
			}
		}
		requestImageAttachments := params.ImageAttachments
		params.ImageAttachments = nil
		steeringCallbackParams = &params
		attemptSteering = preparedSteeringBatch{}
		ctx = llmcontracts.WithSteeringCallback(ctx, steeringCallback)
		ctx = llmcontracts.WithSteeringRetryResetCallback(ctx, func(callbackCtx context.Context) error {
			if attemptSteering.count() == 0 || h.threadInputRepo == nil {
				return nil
			}
			if err := h.threadInputRepo.RestorePreparedSteering(callbackCtx, preparedSteeringInputIDs(attemptSteering), params.ExecID, params.ExecID); err != nil {
				return err
			}
			pendingSteering = removePreparedSteeringInputs(pendingSteering, attemptSteering)
			attemptSteering = preparedSteeringBatch{}
			return nil
		})
		if ctx.Err() != nil {
			h.requeuePendingSteeringForExecution(ctx, params.ExecID)
			pendingSteering = preparedSteeringBatch{}
			steeringCallbackParams = nil
			attemptSteering = preparedSteeringBatch{}
			err = ctx.Err()
			break
		}
		requestCtx := llmcontracts.WithTransportScope(ctx, streamingTransportScope(params))
		if params.lifecycleUserMessage != "" {
			requestCtx = llmcontracts.WithLifecycleCompletionUserMessage(requestCtx, params.lifecycleUserMessage)
		}
		result, err = h.llmSvc.CallAgentDirectStreamingDetailed(
			requestCtx, params.Message, requestImageAttachments, params.Agent,
			params.ExecID, params.ChatHistory, params.SystemContext,
			params.WorkDir, agentDef, params.IsTaskFollowup,
		)
		steeringCallbackParams = nil
		attemptSteering = preparedSteeringBatch{}
		if err != nil || ctx.Err() != nil {
			h.requeuePendingSteeringForExecution(ctx, params.ExecID)
			pendingSteering = preparedSteeringBatch{}
			break
		}
		if err := h.commitPreparedSteeringInputs(ctx, params, pendingSteering); err != nil {
			h.requeueUncommittedSteering(ctx, params.ExecID, pendingSteering)
			pendingSteering = preparedSteeringBatch{}
			finalizeLifecycle(err, result.ChatContext)
			applog.Infof("[handler] processStreamingResponse exec=%s error committing steering after successful model call: %v", params.ExecID, err)
			h.completeWithFailure(ctx, params.ExecID, params.TaskID, err.Error(), time.Since(start).Milliseconds(), params.TelegramInitialAckMessageID, params.ChannelReply)
			h.finalizeStreamingTurn(params, result.Output)
			return
		}
		pendingSteering = preparedSteeringBatch{}
		preparedAfter, steeringErr := h.preparePendingTextSteeringInputs(ctx, &params, result.Output)
		if steeringErr != nil {
			applog.Infof("[handler] processStreamingResponse exec=%s error preparing steering after model call: %v", params.ExecID, steeringErr)
		}
		if preparedAfter.count() == 0 {
			latePrepared, lateErr := h.waitForFinalSteeringInputs(ctx, &params, result.Output)
			if lateErr != nil {
				applog.Infof("[handler] processStreamingResponse exec=%s error preparing final steering before completion: %v", params.ExecID, lateErr)
			}
			if latePrepared.count() == 0 {
				break modelLoop
			}
			preparedAfter = latePrepared
		}
		pendingSteering = preparedAfter
		applog.Infof("[handler] processStreamingResponse exec=%s prepared %d steering inputs for continuation", params.ExecID, pendingSteering.count())
	}
	durationMs := time.Since(start).Milliseconds()
	output := result.Output
	textOnlyOutput := result.TextOnlyOutput
	tokensUsed := result.Usage.TotalTokens

	if err != nil {
		status := string(models.ExecFailed)
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = string(models.ExecCancelled)
		}
		h.recordStreamingUsage(ctx, params, result, status, err.Error(), durationMs)
		finalizeLifecycle(err, result.ChatContext)
		applog.Infof("[handler] processStreamingResponse exec=%s task=%s LLM call failed after %dms: %v", params.ExecID, params.TaskID, durationMs, err)
		// When max_tokens is hit, partial output is returned. Preserve it in the
		// execution so the user can see what work was done before the limit.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			h.completeWithCancellation(params.ExecID, params.TaskID, output, tokensUsed, durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
		} else if output != "" {
			applog.Infof("[handler] processStreamingResponse exec=%s max_tokens failure, preserving partial output (%d bytes)", params.ExecID, len(output))
			h.completeWithFailureAndOutput(ctx, params.ExecID, params.TaskID, err.Error(), output, tokensUsed, durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
		} else {
			h.completeWithFailure(ctx, params.ExecID, params.TaskID, err.Error(), durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
		}
		h.finalizeStreamingTurn(params, output)
		return
	}
	// Check context before final status processing.
	if ctx.Err() != nil {
		finalizeLifecycle(ctx.Err(), result.ChatContext)
		applog.Infof("[handler] processStreamingResponse exec=%s task=%s context cancelled, skipping final status processing", params.ExecID, params.TaskID)
		h.completeWithCancellation(params.ExecID, params.TaskID, output, tokensUsed, durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
		h.finalizeStreamingTurn(params, output)
		return
	}

	if params.IsTaskFollowup {
		statusCheckOutput := textOnlyOutput
		if statusCheckOutput == "" {
			statusCheckOutput = output
		}
		if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: FAILED |"); found {
			finalizeLifecycle(errors.New(reason), result.ChatContext)
			applog.Infof("[handler] processStreamingResponse exec=%s task=%s agent reported STATUS FAILED reason=%q", params.ExecID, params.TaskID, reason)
			h.recordStreamingUsage(ctx, params, result, string(models.ExecFailed), reason, durationMs)
			h.completeWithFailureAndOutput(ctx, params.ExecID, params.TaskID, reason, output, tokensUsed, durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
			h.finalizeStreamingTurn(params, output)
			return
		}
	}

	applog.Infof("[handler] processStreamingResponse exec=%s task=%s success tokens=%d duration=%dms output_len=%d",
		params.ExecID, params.TaskID, tokensUsed, durationMs, len(output))

	if replayErr := h.persistSteeringReplayHistory(ctx, params, result.Output); replayErr != nil {
		finalizeLifecycle(replayErr, result.ChatContext)
		applog.Infof("[handler] processStreamingResponse exec=%s error preserving steering replay history: %v", params.ExecID, replayErr)
		h.completeWithFailure(ctx, params.ExecID, params.TaskID, replayErr.Error(), durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
		h.finalizeStreamingTurn(params, output)
		return
	}
	completionOutcome := h.completeWithSuccess(ctx, params.ExecID, params.TaskID, output, params.WorkDir, tokensUsed, durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
	if completionOutcome == repository.CompleteSuccessCompleted {
		h.recordStreamingUsage(ctx, params, result, string(models.ExecCompleted), "", durationMs)
	}
	if completionOutcome == repository.CompleteSuccessAlreadyTerminal {
		finalizeLifecycle(nil, result.ChatContext)
		h.finalizeStreamingTurn(params, output)
		return
	}
	if completionOutcome == repository.CompleteSuccessPendingSteering {
		prepared, steeringErr := h.preparePendingTextSteeringInputsFromPersistedReplay(ctx, &params, output)
		if steeringErr != nil {
			finalizeLifecycle(steeringErr, result.ChatContext)
			applog.Infof("[handler] processStreamingResponse exec=%s error preparing steering after deferred completion: %v", params.ExecID, steeringErr)
			h.completeWithFailure(ctx, params.ExecID, params.TaskID, steeringErr.Error(), durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
			h.finalizeStreamingTurn(params, output)
			return
		}
		if prepared.count() > 0 {
			applog.Infof("[handler] processStreamingResponse exec=%s prepared %d text steering inputs after deferred completion; continuing active turn", params.ExecID, prepared.count())
			pendingSteering = prepared
			goto modelLoop
		}
		applog.Infof("[handler] processStreamingResponse exec=%s completion deferred with no text steering; requeueing remaining steering inputs", params.ExecID)
		h.requeuePendingSteeringForExecution(ctx, params.ExecID)
		completionOutcome = h.completeWithSuccess(ctx, params.ExecID, params.TaskID, output, params.WorkDir, tokensUsed, durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
		if completionOutcome == repository.CompleteSuccessAlreadyTerminal {
			finalizeLifecycle(nil, result.ChatContext)
			h.finalizeStreamingTurn(params, output)
			return
		}
		if completionOutcome != repository.CompleteSuccessCompleted {
			completionErr := errors.New("failed to complete response after steering check")
			finalizeLifecycle(completionErr, result.ChatContext)
			h.completeWithFailure(ctx, params.ExecID, params.TaskID, completionErr.Error(), durationMs, params.TelegramInitialAckMessageID, params.ChannelReply)
			h.finalizeStreamingTurn(params, output)
			return
		}
		h.recordStreamingUsage(ctx, params, result, string(models.ExecCompleted), "", durationMs)
	}
	finalizeLifecycle(nil, result.ChatContext)
	if params.IsTaskFollowup {
		statusCheckOutput := textOnlyOutput
		if statusCheckOutput == "" {
			statusCheckOutput = output
		}
		if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: NEEDS_FOLLOWUP |"); found {
			task, taskErr := h.taskRepo.GetByID(ctx, params.TaskID)
			if taskErr != nil {
				applog.Infof("[handler] processStreamingResponse exec=%s task=%s error loading task for followup alert: %v", params.ExecID, params.TaskID, taskErr)
			} else if task != nil && h.alertSvc != nil {
				if alertErr := h.alertSvc.CreateTaskNeedsFollowupAlert(ctx, task.ProjectID, task.ID, params.ExecID, task.Title, reason); alertErr != nil {
					applog.Infof("[handler] processStreamingResponse exec=%s task=%s error creating followup alert: %v", params.ExecID, params.TaskID, alertErr)
				}
			}
		}
	}

	h.finalizeStreamingTurn(params, output)
}

type preparedSteeringBatch struct {
	inputs []models.ThreadInput
}

func (b preparedSteeringBatch) count() int {
	return len(b.inputs)
}

func preparedSteeringInputIDs(batch preparedSteeringBatch) []string {
	ids := make([]string, 0, batch.count())
	for _, input := range batch.inputs {
		ids = append(ids, input.ID)
	}
	return ids
}

func removePreparedSteeringInputs(batch, remove preparedSteeringBatch) preparedSteeringBatch {
	if batch.count() == 0 || remove.count() == 0 {
		return batch
	}
	removeIDs := make(map[string]struct{}, len(remove.inputs))
	for _, input := range remove.inputs {
		removeIDs[input.ID] = struct{}{}
	}
	kept := batch.inputs[:0]
	for _, input := range batch.inputs {
		if _, ok := removeIDs[input.ID]; !ok {
			kept = append(kept, input)
		}
	}
	return preparedSteeringBatch{inputs: kept}
}

func (h *Handler) waitForFinalSteeringInputs(ctx context.Context, params *streamingResponseParams, previousAssistantOutput string) (preparedSteeringBatch, error) {
	if h.threadInputRepo == nil || params == nil || params.ExecID == "" {
		return preparedSteeringBatch{}, nil
	}
	deadline := time.NewTimer(finalSteeringGracePeriod)
	defer deadline.Stop()
	poll := time.NewTicker(finalSteeringPollInterval)
	defer poll.Stop()
	for {
		// Once an outer provider call has returned, attachment-bearing steering can no
		// longer be represented as a separate user attachment on this execution. Only
		// text steering may continue the current turn; attachment steering is requeued
		// by the deferred-completion path and processed as the next normal message.
		prepared, err := h.preparePendingTextSteeringInputs(ctx, params, previousAssistantOutput)
		if prepared.count() > 0 || err != nil {
			return prepared, err
		}
		select {
		case <-ctx.Done():
			return preparedSteeringBatch{}, ctx.Err()
		case <-deadline.C:
			return preparedSteeringBatch{}, nil
		case <-poll.C:
		}
	}
}

func (h *Handler) claimPendingSteeringInputs(ctx context.Context, params *streamingResponseParams) (preparedSteeringBatch, error) {
	return h.claimPendingSteeringInputsWithOptions(ctx, params, false)
}

func (h *Handler) claimPendingTextSteeringInputs(ctx context.Context, params *streamingResponseParams) (preparedSteeringBatch, error) {
	return h.claimPendingSteeringInputsWithOptions(ctx, params, true)
}

func (h *Handler) claimPendingSteeringInputsWithOptions(ctx context.Context, params *streamingResponseParams, textOnly bool) (preparedSteeringBatch, error) {
	if h.threadInputRepo == nil || params == nil || params.ExecID == "" {
		return preparedSteeringBatch{}, nil
	}
	var inputs []models.ThreadInput
	var err error
	if textOnly {
		inputs, err = h.threadInputRepo.PreparePendingTextSteering(ctx, params.ExecID, params.ExecID)
	} else {
		inputs, err = h.threadInputRepo.PreparePendingSteering(ctx, params.ExecID, params.ExecID)
	}
	if err != nil {
		return preparedSteeringBatch{}, err
	}
	if len(inputs) == 0 {
		return preparedSteeringBatch{}, nil
	}
	prepared := make([]models.ThreadInput, 0, len(inputs))
	for _, input := range inputs {
		if textOnly && input.AttachmentSessionID != "" {
			continue
		}
		if input.AttachmentSessionID != "" {
			attachmentContext, imageAttachments, attErr := h.previewPendingAttachments(input.AttachmentSessionID)
			if attErr != nil {
				applog.Infof("[handler] claimPendingSteeringInputs exec=%s input=%s attachment preview error: %v", params.ExecID, input.ID, attErr)
				attachmentContext = fmt.Sprintf("⚠️ Attachment processing error: %v", attErr)
			}
			if attachmentContext != "" {
				input.Content = combineContexts(input.Content, attachmentContext)
			}
			if len(imageAttachments) > 0 {
				params.ImageAttachments = append(params.ImageAttachments, imageAttachments...)
			}
		}
		prepared = append(prepared, input)
	}
	if len(prepared) == 0 {
		return preparedSteeringBatch{}, nil
	}
	h.publishThreadInputAppliedEvents(*params, prepared)
	return preparedSteeringBatch{inputs: prepared}, nil
}

func (h *Handler) preparePendingSteeringInputs(ctx context.Context, params *streamingResponseParams, previousAssistantOutput string) (preparedSteeringBatch, error) {
	batch, err := h.claimPendingSteeringInputs(ctx, params)
	return h.prepareClaimedSteeringInputs(ctx, params, previousAssistantOutput, batch, err)
}

func (h *Handler) preparePendingTextSteeringInputs(ctx context.Context, params *streamingResponseParams, previousAssistantOutput string) (preparedSteeringBatch, error) {
	batch, err := h.claimPendingTextSteeringInputs(ctx, params)
	return h.prepareClaimedSteeringInputs(ctx, params, previousAssistantOutput, batch, err)
}

func (h *Handler) prepareClaimedSteeringInputs(ctx context.Context, params *streamingResponseParams, previousAssistantOutput string, batch preparedSteeringBatch, err error) (preparedSteeringBatch, error) {
	if err != nil || batch.count() == 0 {
		return batch, err
	}
	steeringMessage := combinedSteeringContent(batch.inputs)
	steeringInstruction := formatSteeringInstruction(steeringMessage)
	params.lifecycleUserMessage = steeringInstruction
	if previousAssistantOutput != "" {
		assistantDelta := previousAssistantOutput
		if strings.HasPrefix(previousAssistantOutput, params.steeringOutputCursor) {
			assistantDelta = previousAssistantOutput[len(params.steeringOutputCursor):]
		}
		if strings.TrimSpace(params.Message) != "" || strings.TrimSpace(assistantDelta) != "" {
			reasoningContent := ""
			var replayMessages []models.ExecutionReplayMessage
			if h.execRepo != nil && strings.TrimSpace(params.ExecID) != "" {
				if exec, execErr := h.execRepo.GetByID(ctx, params.ExecID); execErr == nil && exec != nil {
					reasoningContent = exec.ReasoningContent
					replayByID, replayErr := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{params.ExecID})
					if replayErr != nil {
						applog.Infof("[handler] preparePendingSteeringInputs exec=%s error loading replay context: %v", params.ExecID, replayErr)
					} else {
						replayMessages = replayByID[params.ExecID]
					}
				} else if execErr != nil {
					applog.Infof("[handler] preparePendingSteeringInputs exec=%s error loading reasoning context: %v", params.ExecID, execErr)
				}
			}
			params.ChatHistory = append(params.ChatHistory, models.Execution{
				ID:               nextSteeringContextID(*params),
				TaskID:           params.TaskID,
				Status:           models.ExecCompleted,
				PromptSent:       params.Message,
				Output:           assistantDelta,
				IsFollowup:       params.IsTaskFollowup,
				ReasoningContent: reasoningContent,
				ReplayMessages:   replayMessages,
			})
		}
		params.Message = steeringInstruction
	} else if !params.steeringHistoryStarted {
		params.Message = combineActivePromptWithSteering(params.Message, steeringInstruction)
	} else {
		params.Message = steeringInstruction
	}
	params.steeringHistoryStarted = true
	params.steeringOutputCursor = previousAssistantOutput
	return batch, nil
}

func (h *Handler) preparePendingSteeringInputsFromPersistedReplay(ctx context.Context, params *streamingResponseParams, previousAssistantOutput string) (preparedSteeringBatch, error) {
	batch, err := h.preparePendingSteeringInputs(ctx, params, previousAssistantOutput)
	return h.collapsePreparedSteeringReplay(params, batch, err)
}

func (h *Handler) preparePendingTextSteeringInputsFromPersistedReplay(ctx context.Context, params *streamingResponseParams, previousAssistantOutput string) (preparedSteeringBatch, error) {
	batch, err := h.preparePendingTextSteeringInputs(ctx, params, previousAssistantOutput)
	return h.collapsePreparedSteeringReplay(params, batch, err)
}

func (h *Handler) collapsePreparedSteeringReplay(params *streamingResponseParams, batch preparedSteeringBatch, err error) (preparedSteeringBatch, error) {
	if err != nil || batch.count() == 0 {
		return batch, err
	}
	// Completion persists one replay containing every synthetic steering turn.
	// Keep only the newly appended context that owns that replay, or the prior
	// turns would be sent both individually and through the combined replay.
	collapseSteeringContextsCoveredByLatestReplay(params)
	return batch, nil
}

func nextSteeringContextID(params streamingResponseParams) string {
	prefix := strings.TrimSpace(params.ExecID) + "-steering-context-"
	used := make(map[string]struct{}, len(params.ChatHistory))
	for _, exec := range params.ChatHistory {
		used[exec.ID] = struct{}{}
	}
	for suffix := len(params.ChatHistory) + 1; ; suffix++ {
		id := fmt.Sprintf("%s%d", prefix, suffix)
		if _, exists := used[id]; !exists {
			return id
		}
	}
}

func collapseSteeringContextsCoveredByLatestReplay(params *streamingResponseParams) {
	prefix := strings.TrimSpace(params.ExecID) + "-steering-context-"
	latestIndex := -1
	for i := range params.ChatHistory {
		if strings.HasPrefix(params.ChatHistory[i].ID, prefix) {
			latestIndex = i
		}
	}
	if latestIndex < 0 || len(params.ChatHistory[latestIndex].ReplayMessages) == 0 {
		return
	}

	history := params.ChatHistory[:0]
	for i, exec := range params.ChatHistory {
		if strings.HasPrefix(exec.ID, prefix) && i != latestIndex {
			continue
		}
		history = append(history, exec)
	}
	params.ChatHistory = history
}

func steeringContextHistory(params streamingResponseParams) []models.Execution {
	prefix := strings.TrimSpace(params.ExecID) + "-steering-context-"
	if prefix == "-steering-context-" {
		return nil
	}
	history := make([]models.Execution, 0)
	for _, exec := range params.ChatHistory {
		if strings.HasPrefix(exec.ID, prefix) {
			history = append(history, exec)
		}
	}
	return history
}

func (h *Handler) persistSteeringReplayHistory(ctx context.Context, params streamingResponseParams, finalOutput string) error {
	steeringHistory := steeringContextHistory(params)
	if len(steeringHistory) == 0 || h.execRepo == nil || strings.TrimSpace(params.ExecID) == "" {
		return nil
	}
	exec, err := h.execRepo.GetByID(ctx, params.ExecID)
	if err != nil {
		return fmt.Errorf("load current reasoning content: %w", err)
	}
	if exec == nil {
		return fmt.Errorf("load current reasoning content: execution %s not found", params.ExecID)
	}

	replay := make([]models.ExecutionReplayMessage, 0, len(steeringHistory)+1)
	var reasoning strings.Builder
	for _, turn := range steeringHistory {
		if len(turn.ReplayMessages) > 0 {
			replay = append(replay, turn.ReplayMessages...)
			for _, message := range turn.ReplayMessages {
				reasoning.WriteString(message.ReasoningContent)
			}
			continue
		}
		replay = append(replay, models.ExecutionReplayMessage{
			UserContent:      turn.PromptSent,
			AssistantContent: turn.Output,
			ReasoningContent: turn.ReasoningContent,
		})
		reasoning.WriteString(turn.ReasoningContent)
	}
	currentReplayByID, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{params.ExecID})
	if err != nil {
		return fmt.Errorf("load current replay history: %w", err)
	}
	if currentReplay := currentReplayByID[params.ExecID]; len(currentReplay) > 0 {
		replay = append(replay, currentReplay...)
		for _, message := range currentReplay {
			reasoning.WriteString(message.ReasoningContent)
		}
	} else {
		finalAssistantOutput := finalOutput
		if strings.HasPrefix(finalOutput, params.steeringOutputCursor) {
			finalAssistantOutput = finalOutput[len(params.steeringOutputCursor):]
		}
		replay = append(replay, models.ExecutionReplayMessage{
			UserContent:      params.Message,
			AssistantContent: finalAssistantOutput,
			ReasoningContent: exec.ReasoningContent,
		})
		reasoning.WriteString(exec.ReasoningContent)
	}

	if err := h.execRepo.ReplaceReasoningReplay(context.WithoutCancel(ctx), params.ExecID, reasoning.String(), replay); err != nil {
		return fmt.Errorf("persist steering replay history: %w", err)
	}
	return nil
}

func (h *Handler) publishThreadInputAppliedEvents(params streamingResponseParams, inputs []models.ThreadInput) {
	for _, input := range inputs {
		if h.broadcaster != nil && input.TaskID != "" {
			h.broadcaster.Publish(events.TaskEvent{
				Type:           events.TaskThreadInputApplied,
				ProjectID:      input.ProjectID,
				TaskID:         input.TaskID,
				ExecID:         params.ExecID,
				Message:        input.Content,
				PendingInputID: input.ID,
				HasAttachments: input.AttachmentSessionID != "",
			})
		}
		if h.chatBroadcaster != nil && input.Scope == models.ThreadInputScopeChat {
			h.chatBroadcaster.Publish(events.ChatEvent{
				Type:           events.ChatThreadInputApplied,
				ProjectID:      input.ProjectID,
				ExecID:         params.ExecID,
				TaskID:         params.TaskID,
				Message:        input.Content,
				Source:         string(input.Source),
				Steering:       true,
				PendingInputID: input.ID,
				HasAttachments: input.AttachmentSessionID != "",
			})
		}
	}
}

func (h *Handler) requeueUncommittedSteering(ctx context.Context, execID string, batch preparedSteeringBatch) {
	if h.threadInputRepo == nil || batch.count() == 0 {
		return
	}
	ids := make([]string, 0, len(batch.inputs))
	for _, input := range batch.inputs {
		ids = append(ids, input.ID)
	}
	requeued, err := h.threadInputRepo.RequeuePendingSteering(steeringCleanupContext(ctx), ids, execID)
	if err != nil {
		applog.Infof("[handler] processStreamingResponse exec=%s error requeueing uncommitted steering after failed model call: %v", execID, err)
		return
	}
	h.publishThreadInputQueuedEvents(requeued)
}

func (h *Handler) requeuePendingSteeringForExecution(ctx context.Context, execID string) {
	if h.threadInputRepo == nil || execID == "" {
		return
	}
	requeued, err := h.threadInputRepo.RequeuePendingSteeringForExecution(steeringCleanupContext(ctx), execID)
	if err != nil {
		applog.Infof("[handler] processStreamingResponse exec=%s error requeueing pending steering after failed model call: %v", execID, err)
		return
	}
	h.publishThreadInputQueuedEvents(requeued)
}

func steeringCleanupContext(ctx context.Context) context.Context {
	if ctx == nil || ctx.Err() != nil {
		return context.Background()
	}
	return ctx
}

func (h *Handler) publishThreadInputQueuedEvents(inputs []models.ThreadInput) {
	for _, input := range inputs {
		if h.broadcaster != nil && input.TaskID != "" {
			h.broadcaster.Publish(events.TaskEvent{
				Type:           events.TaskThreadInputQueued,
				ProjectID:      input.ProjectID,
				TaskID:         input.TaskID,
				ExecID:         input.RunExecutionID,
				Message:        input.Content,
				PendingInputID: input.ID,
				HasAttachments: input.AttachmentSessionID != "",
			})
		}
		if h.chatBroadcaster != nil && input.Scope == models.ThreadInputScopeChat {
			h.chatBroadcaster.Publish(events.ChatEvent{
				Type:           events.ChatNewMessage,
				ProjectID:      input.ProjectID,
				ExecID:         input.ID,
				Message:        input.Content,
				Source:         string(input.Source),
				Queued:         true,
				PendingInputID: input.ID,
				HasAttachments: input.AttachmentSessionID != "",
			})
		}
	}
}

func (h *Handler) commitPreparedSteeringInputs(ctx context.Context, params streamingResponseParams, batch preparedSteeringBatch) error {
	if h.threadInputRepo == nil || batch.count() == 0 {
		return nil
	}
	for _, input := range batch.inputs {
		if input.AttachmentSessionID != "" {
			if _, _, _, attErr := h.processAttachmentsWithReturn(ctx, input.AttachmentSessionID, params.ExecID); attErr != nil {
				return fmt.Errorf("committing steering attachments for input %s: %w", input.ID, attErr)
			}
		}
		if err := h.threadInputRepo.MarkApplied(ctx, input.ID, params.ExecID, params.ExecID); err != nil {
			return err
		}
	}
	return nil
}

func combinedSteeringContent(inputs []models.ThreadInput) string {
	parts := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if content := strings.TrimSpace(input.Content); content != "" {
			parts = append(parts, content)
		}
	}
	return strings.Join(parts, "\n\n")
}

func formatSteeringInstruction(steeringMessage string) string {
	return strings.TrimSpace(steeringMessage)
}

func combineActivePromptWithSteering(activePrompt, steeringInstruction string) string {
	activePrompt = strings.TrimSpace(activePrompt)
	steeringInstruction = strings.TrimSpace(steeringInstruction)
	if activePrompt == "" {
		return steeringInstruction
	}
	if steeringInstruction == "" {
		return activePrompt
	}
	return activePrompt + "\n\n" + steeringInstruction
}

func (h *Handler) finalizeStreamingTurn(params streamingResponseParams, output string) {
	// Broadcast response done for chat messages and task-thread follow-ups.
	// Include completed output and the persisted terminal status so live UI
	// fallbacks converge even when the per-execution terminal event races or is missed.
	terminalStatus := ""
	if h.execRepo != nil && params.ExecID != "" {
		if exec, err := h.execRepo.GetByID(context.Background(), params.ExecID); err == nil && exec != nil {
			terminalStatus = string(exec.Status)
		} else if err != nil {
			applog.Infof("[handler] finalizeStreamingTurn exec=%s error loading terminal status: %v", params.ExecID, err)
		}
	}
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:            events.ChatResponseDone,
			ProjectID:       params.ProjectID,
			ExecID:          params.ExecID,
			TaskID:          params.TaskID,
			CompletedOutput: output,
			Status:          terminalStatus,
			IsTaskFollowup:  params.IsTaskFollowup,
		})
	}
	if !params.suppressQueuedTurnPromotion {
		go h.startNextQueuedTurnAfter(context.Background(), params, "")
	}
}

func (h *Handler) resolveTaskAgentDefinitionForTask(ctx context.Context, taskID string, current *models.Agent) *models.Agent {
	if current != nil {
		return current
	}
	if h.agentRepo == nil || h.taskRepo == nil || taskID == "" {
		return nil
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil || task.AgentDefinitionID == nil || *task.AgentDefinitionID == "" {
		return nil
	}
	ad, err := h.agentRepo.GetByID(ctx, *task.AgentDefinitionID)
	if err != nil || ad == nil {
		return nil
	}
	return ad
}

// registerTaskCancellation registers a cancel function for a task with the worker service.
// No-op if worker service is unavailable.
func (h *Handler) startNextQueuedTurnAfter(ctx context.Context, completed streamingResponseParams, excludeExecID string) {
	if h.execRepo == nil || h.threadInputRepo == nil {
		return
	}
	if completed.IsTaskFollowup {
		if completed.TaskID == "" {
			return
		}
		active, activeErr := h.execRepo.HasActiveTaskExecution(ctx, completed.TaskID, excludeExecID)
		if activeErr != nil {
			applog.Infof("[handler] startNextQueuedTurn error checking active task turn task=%s: %v", completed.TaskID, activeErr)
			return
		}
		if active {
			return
		}
		queued, err := h.threadInputRepo.FindOldestQueuedForTask(ctx, completed.TaskID)
		if err != nil {
			applog.Infof("[handler] startNextQueuedTurn error finding queued task input task=%s: %v", completed.TaskID, err)
			return
		}
		if queued == nil {
			return
		}
		if startErr := h.startQueuedTaskThreadInput(ctx, *queued); startErr != nil {
			applog.Infof("[handler] startNextQueuedTurn task=%s input=%s start error: %v", completed.TaskID, queued.ID, startErr)
		}
		return
	}
	if completed.ProjectID == "" {
		return
	}
	activeChat, activeErr := h.execRepo.FindLatestActiveChatExecution(ctx, completed.ProjectID)
	if activeErr != nil {
		applog.Infof("[handler] startNextQueuedTurn error checking active chat turn project=%s: %v", completed.ProjectID, activeErr)
		return
	}
	if activeChat != nil && activeChat.ID != excludeExecID {
		return
	}
	queued, err := h.threadInputRepo.FindOldestQueuedForChat(ctx, completed.ProjectID)
	if err != nil {
		applog.Infof("[handler] startNextQueuedTurn error finding queued chat input project=%s: %v", completed.ProjectID, err)
		return
	}
	if queued == nil {
		return
	}
	h.startQueuedChatInput(ctx, *queued)
}

func (h *Handler) startQueuedChatInput(ctx context.Context, input models.ThreadInput) {
	var updateWorkDone func()
	if h.updateWorkTracker != nil {
		var err error
		updateWorkDone, err = h.updateWorkTracker.Start(update.WorkChat)
		if err != nil {
			return
		}
		defer func() {
			if updateWorkDone != nil {
				updateWorkDone()
			}
		}()
	}
	agent, unstartable, err := h.resolveQueuedInputAgent(ctx, input)
	if err != nil {
		applog.Infof("[handler] startQueuedChatInput input=%s agent=%s load error: %v", input.ID, input.AgentConfigID, err)
		return
	}
	if agent == nil {
		applog.Infof("[handler] startQueuedChatInput input=%s agent=%s no usable model", input.ID, input.AgentConfigID)
		if unstartable {
			h.cancelUnstartableQueuedInput(ctx, input)
			h.startNextQueuedTurnAfter(ctx, streamingResponseParams{ProjectID: input.ProjectID}, "")
		}
		return
	}
	selectedAgentID := agent.ID
	task := &models.Task{
		ProjectID: input.ProjectID,
		Title:     fmt.Sprintf("Chat %s: %s", time.Now().Format("15:04:05.000"), input.Content[:min(50, len(input.Content))]),
		Prompt:    input.Content,
		Status:    models.StatusRunning,
		Category:  models.CategoryChat,
		AgentID:   &selectedAgentID,
	}
	if input.Source == models.TaskOriginTelegram {
		task.CreatedVia = models.TaskOriginTelegram
		task.TelegramChatID = input.TelegramChatID
	} else if input.Source == models.TaskOriginSlack {
		task.CreatedVia = models.TaskOriginSlack
	} else if input.Source == models.TaskOriginEmail {
		task.CreatedVia = models.TaskOriginEmail
	} else if input.Source == models.TaskOriginDiscord {
		task.CreatedVia = models.TaskOriginDiscord
	}
	exec := &models.Execution{
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    input.Content,
	}
	var slackContext *models.SlackTaskContext
	if input.Source == models.TaskOriginSlack {
		slackContext = &models.SlackTaskContext{
			SlackTeamID:    input.SlackTeamID,
			SlackChannelID: input.SlackChannelID,
			SlackThreadTS:  input.SlackThreadTS,
			SlackUserID:    input.SlackUserID,
		}
	}
	var emailContext *models.EmailTaskContext
	if input.Source == models.TaskOriginEmail {
		emailContext = &models.EmailTaskContext{
			EmailFrom:       input.EmailFrom,
			EmailMessageID:  input.EmailMessageID,
			EmailReferences: input.EmailReferences,
			EmailSubject:    input.EmailSubject,
			EmailSessionKey: input.EmailSessionKey,
		}
	}
	var discordContext *models.DiscordTaskContext
	if input.Source == models.TaskOriginDiscord {
		discordContext = &models.DiscordTaskContext{
			DiscordChannelID: input.DiscordChannelID,
			DiscordThreadID:  input.DiscordThreadID,
			DiscordMessageID: input.DiscordMessageID,
			DiscordUserID:    input.DiscordUserID,
		}
	}
	if err := h.threadInputRepo.ClaimQueuedForChatExecution(ctx, input.ID, task, exec, slackContext, emailContext, discordContext); err != nil {
		if err != repository.ErrInputNotPending {
			applog.Infof("[handler] startQueuedChatInput input=%s claim error: %v", input.ID, err)
		}
		return
	}

	var attachmentContext string
	var imageAttachments []models.Attachment
	if input.AttachmentSessionID != "" {
		var attErr error
		attachmentContext, imageAttachments, _, attErr = h.processAttachmentsWithReturn(ctx, input.AttachmentSessionID, exec.ID)
		if attErr != nil {
			applog.Infof("[handler] startQueuedChatInput exec=%s attachment processing error: %v", exec.ID, attErr)
			attachmentContext = fmt.Sprintf("⚠️ Attachment processing error: %v", attErr)
		}
	}
	history, err := h.queuedChatHistory(ctx, input, exec.ID)
	if err != nil {
		applog.Infof("[handler] startQueuedChatInput exec=%s history error: %v", exec.ID, err)
		history = []models.Execution{}
	}
	availableModels, _ := h.llmConfigRepo.List(ctx)
	taskContext := h.buildChatContext(ctx, input.ProjectID, availableModels)
	personalityContext := h.getPersonalityContext(ctx, input.ProjectID)
	workDir := h.resolveWorkDir(ctx, input.ProjectID)
	chatMode := models.NormalizeChatMode(string(input.ChatMode))
	if chatMode == "" {
		chatMode = models.ChatModeOrchestrate
	}
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:           events.ChatNewMessage,
			ProjectID:      input.ProjectID,
			ExecID:         exec.ID,
			TaskID:         task.ID,
			Message:        input.Content,
			Source:         string(input.Source),
			AgentName:      agent.Name,
			PendingInputID: input.ID,
		})
	}

	h.startStreamingResponse(streamingResponseParams{
		ExecID:      exec.ID,
		TaskID:      task.ID,
		Message:     input.Content,
		Agent:       *agent,
		ChatHistory: history, ProjectID: input.ProjectID,
		SystemContext:    combineContexts(combineContexts(taskContext, attachmentContext), personalityContext),
		WorkDir:          workDir,
		ImageAttachments: imageAttachments,
		IsTaskFollowup:   false,
		ChatMode:         chatMode,
		Surface:          surfaceForThreadInput(input),
		ChannelReply:     channelReplyFromThreadInput(input),
		updateWorkDone:   updateWorkDone,
	})
	updateWorkDone = nil
}

func (h *Handler) queuedChatHistory(ctx context.Context, input models.ThreadInput, currentExecID string) ([]models.Execution, error) {
	if input.Source == models.TaskOriginEmail && strings.TrimSpace(input.EmailSessionKey) != "" {
		history, err := h.execRepo.ListEmailChatHistory(ctx, input.ProjectID, input.EmailSessionKey, chatHistoryLimit)
		return filterChatHistory(history, currentExecID), err
	}
	history, err := h.execRepo.ListChatHistory(ctx, input.ProjectID, chatHistoryLimit)
	return filterChatHistory(history, currentExecID), err
}

func surfaceForThreadInput(input models.ThreadInput) chatcontrol.Surface {
	switch input.Source {
	case models.TaskOriginTelegram:
		return chatcontrol.SurfaceTelegram
	case models.TaskOriginSlack:
		return chatcontrol.SurfaceSlack
	case models.TaskOriginEmail:
		return chatcontrol.SurfaceEmail
	case models.TaskOriginDiscord:
		return chatcontrol.SurfaceDiscord
	default:
		return chatcontrol.SurfaceWeb
	}
}

func (h *Handler) resolveQueuedInputAgent(ctx context.Context, input models.ThreadInput) (*models.LLMConfig, bool, error) {
	if strings.TrimSpace(input.AgentConfigID) != "" {
		agent, err := h.llmConfigRepo.GetByID(ctx, input.AgentConfigID)
		if err != nil || agent != nil {
			return agent, false, err
		}
	}
	agent, err := h.selectDefaultAgent(ctx, false)
	if err != nil {
		if strings.Contains(err.Error(), "no agents configured") {
			return nil, true, nil
		}
		return nil, false, err
	}
	return agent, agent == nil, nil
}

func (h *Handler) resolveTaskThreadExecutionAgent(ctx context.Context, task *models.Task) (*models.LLMConfig, bool, error) {
	if task == nil || h.llmConfigRepo == nil {
		return nil, true, nil
	}
	if task.AgentID != nil && strings.TrimSpace(*task.AgentID) != "" {
		agent, err := h.llmConfigRepo.GetByID(ctx, strings.TrimSpace(*task.AgentID))
		if err != nil || agent != nil {
			return agent, false, err
		}
	}
	if h.projectRepo != nil && strings.TrimSpace(task.ProjectID) != "" {
		project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
		if err != nil {
			return nil, false, err
		}
		if project != nil && project.DefaultAgentConfigID != nil && strings.TrimSpace(*project.DefaultAgentConfigID) != "" {
			agent, err := h.llmConfigRepo.GetByID(ctx, strings.TrimSpace(*project.DefaultAgentConfigID))
			if err != nil || agent != nil {
				return agent, false, err
			}
		}
	}
	agent, err := h.selectDefaultAgent(ctx, false)
	if err != nil {
		if strings.Contains(err.Error(), "no agents configured") {
			return nil, true, nil
		}
		return nil, false, err
	}
	return agent, agent == nil, nil
}

func (h *Handler) cancelUnstartableQueuedInput(ctx context.Context, input models.ThreadInput) {
	if h.threadInputRepo == nil || input.ID == "" {
		return
	}
	if _, err := h.threadInputRepo.CancelPending(ctx, input.ID); err != nil && !errors.Is(err, repository.ErrInputNotPending) {
		applog.Infof("[handler] cancelUnstartableQueuedInput input=%s error: %v", input.ID, err)
	}
}

func channelReplyFromThreadInput(input models.ThreadInput) service.ChannelReplyContext {
	return service.ChannelReplyContext{
		Source:           input.Source,
		TelegramChatID:   input.TelegramChatID,
		SlackTeamID:      input.SlackTeamID,
		SlackChannelID:   input.SlackChannelID,
		SlackThreadTS:    input.SlackThreadTS,
		SlackUserID:      input.SlackUserID,
		EmailFrom:        input.EmailFrom,
		EmailMessageID:   input.EmailMessageID,
		EmailReferences:  input.EmailReferences,
		EmailSubject:     input.EmailSubject,
		EmailSessionKey:  input.EmailSessionKey,
		DiscordChannelID: input.DiscordChannelID,
		DiscordThreadID:  input.DiscordThreadID,
		DiscordMessageID: input.DiscordMessageID,
		DiscordUserID:    input.DiscordUserID,
	}
}

func (h *Handler) StartPendingTaskThreadFollowup(ctx context.Context, taskID string) (bool, error) {
	if h.threadInputRepo == nil || h.execRepo == nil || taskID == "" {
		return false, nil
	}
	active, err := h.execRepo.HasActiveTaskExecution(ctx, taskID, "")
	if err != nil {
		applog.Infof("[handler] startPendingTaskThreadFollowup task=%s active check error: %v", taskID, err)
		return false, err
	}
	if active {
		if h.workerSvc != nil {
			h.workerSvc.ClearCancellationRequested(taskID)
		}
		return true, nil
	}
	queued, err := h.threadInputRepo.FindOldestQueuedForTask(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] startPendingTaskThreadFollowup task=%s queued lookup error: %v", taskID, err)
		return false, err
	}
	if queued == nil {
		return false, nil
	}
	if err := h.startQueuedTaskThreadInput(ctx, *queued); err != nil {
		return true, err
	}
	return true, nil
}

func (h *Handler) RetryLatestFailedTaskThreadFollowup(ctx context.Context, taskID string) (bool, error) {
	if h.execRepo == nil || h.taskRepo == nil || taskID == "" {
		return false, nil
	}
	active, err := h.execRepo.HasActiveTaskExecution(ctx, taskID, "")
	if err != nil {
		return false, err
	}
	if active {
		if h.workerSvc != nil {
			h.workerSvc.ClearCancellationRequested(taskID)
		}
		return true, nil
	}
	failed, err := h.execRepo.GetLatestFailedFollowupByTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	if failed == nil || strings.TrimSpace(failed.PromptSent) == "" {
		return false, nil
	}
	if err := h.retryFailedTaskThreadExecution(ctx, taskID, *failed); err != nil {
		return true, err
	}
	return true, nil
}

func (h *Handler) retryFailedTaskThreadExecution(ctx context.Context, taskID string, failed models.Execution) error {
	var updateWorkDone func()
	if h.updateWorkTracker != nil {
		var err error
		updateWorkDone, err = h.updateWorkTracker.Start(update.WorkTask)
		if err != nil {
			return err
		}
		defer func() {
			if updateWorkDone != nil {
				updateWorkDone()
			}
		}()
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		if err == nil {
			err = fmt.Errorf("task not found: %s", taskID)
		}
		return err
	}
	agent, _, err := h.resolveTaskThreadExecutionAgent(ctx, task)
	if err != nil {
		return err
	}
	if agent == nil {
		return fmt.Errorf("no model configured for failed task-thread retry: %s", taskID)
	}
	exec := &models.Execution{
		TaskID:        taskID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    failed.PromptSent,
		IsFollowup:    true,
	}
	queued := &models.ThreadInput{AgentConfigID: agent.ID, Content: failed.PromptSent, Source: models.TaskOriginWeb}
	started, err := h.execRepo.CreateDirectTaskFollowupOrQueue(ctx, exec, queued)
	if err != nil {
		return err
	}
	if !started {
		go h.PromoteQueuedTaskThreadInput(taskID)
		return nil
	}
	if h.workerSvc != nil {
		h.workerSvc.ClearCancellationRequested(taskID)
	}
	if err := h.applySwarmChildFollowupRetryStart(ctx, task, failed.PromptSent); err != nil {
		h.completeWithFailure(ctx, exec.ID, taskID, err.Error(), 0)
		return err
	}
	if updatedTask, getErr := h.taskRepo.GetByID(ctx, taskID); getErr == nil && updatedTask != nil {
		task = updatedTask
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{
			Type:      events.TaskThreadExecutionStarted,
			ProjectID: task.ProjectID,
			TaskID:    taskID,
			TaskName:  task.Title,
			ExecID:    exec.ID,
			Message:   failed.PromptSent,
		})
	}
	h.resumeUserStoppedGoalForManualStart(ctx, taskID, models.TaskOriginWeb, "")
	h.reactivateAchievedGoalForManualFollowup(ctx, taskID, models.TaskOriginWeb, "")
	priorExecs, _ := h.execRepo.ListByTaskChronologicalLimit(ctx, taskID, taskThreadHistoryLimit)
	priorHistory := filterChatHistory(priorExecs, exec.ID)
	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && h.agentRepo != nil {
		if ad, adErr := h.agentRepo.GetByID(ctx, *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
		}
	}
	systemContext := combineContexts(buildThreadSystemContext(task.Title, len(priorHistory) > 0, ""), h.taskGoalContext(ctx, task.ID, agentDef))
	personalityContext := h.getPersonalityContext(ctx, task.ProjectID)
	workDir, worktreeContext, workDirErr := h.resolveWorktreeWorkDir(ctx, task)
	if workDirErr != nil {
		h.completeWithFailure(ctx, exec.ID, taskID, workDirErr.Error(), 0)
		go h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{ProjectID: task.ProjectID, TaskID: task.ID, IsTaskFollowup: true}, exec.ID)
		return nil
	}
	var automationContext *models.AutomationContext
	if h.automationGraphSvc != nil {
		if value, contextErr := h.automationGraphSvc.ContextForExecution(ctx, task.ProjectID, failed.ID); contextErr != nil {
			return contextErr
		} else if len(value.Bindings) > 0 {
			automationContext = &value
		}
	}
	h.startStreamingResponse(streamingResponseParams{
		ExecID:            exec.ID,
		TaskID:            taskID,
		Message:           failed.PromptSent,
		Agent:             *agent,
		AgentDefinition:   agentDef,
		ChatHistory:       priorHistory,
		ProjectID:         task.ProjectID,
		SystemContext:     combineContexts(combineContexts(systemContext, worktreeContext), personalityContext),
		WorkDir:           workDir,
		IsTaskFollowup:    true,
		InputOrigin:       models.TaskOriginWeb,
		Task:              task,
		AutomationContext: automationContext,
		updateWorkDone:    updateWorkDone,
	})
	updateWorkDone = nil
	return nil
}

func (h *Handler) applySwarmChildFollowupStart(ctx context.Context, task *models.Task, message string) error {
	if h.swarmSvc == nil || task == nil || !models.IsSwarmChildRole(task.SwarmRole) {
		return nil
	}
	return h.swarmSvc.HandleChildFollowup(ctx, task.ID, message)
}

func (h *Handler) applySwarmChildFollowupRetryStart(ctx context.Context, task *models.Task, message string) error {
	if h.swarmSvc == nil || task == nil || !models.IsSwarmChildRole(task.SwarmRole) {
		return nil
	}
	if task.SwarmStatus == "followup_pending" {
		return h.swarmSvc.ReactivateParentForChildFollowupRetry(ctx, task.ID)
	}
	return h.swarmSvc.HandleChildFollowup(ctx, task.ID, message)
}

func (h *Handler) startQueuedTaskThreadInput(ctx context.Context, input models.ThreadInput) error {
	var updateWorkDone func()
	if h.updateWorkTracker != nil {
		var err error
		updateWorkDone, err = h.updateWorkTracker.Start(update.WorkTask)
		if err != nil {
			return err
		}
		defer func() {
			if updateWorkDone != nil {
				updateWorkDone()
			}
		}()
	}
	task, err := h.taskRepo.GetByID(ctx, input.TaskID)
	if err != nil || task == nil {
		if err == nil {
			err = fmt.Errorf("task not found: %s", input.TaskID)
		}
		applog.Infof("[handler] startQueuedTaskThreadInput input=%s task=%s load error: %v", input.ID, input.TaskID, err)
		return err
	}
	agent, unstartable, err := h.resolveTaskThreadExecutionAgent(ctx, task)
	if err != nil {
		applog.Infof("[handler] startQueuedTaskThreadInput input=%s task=%s model load error: %v", input.ID, task.ID, err)
		return err
	}
	if agent == nil {
		applog.Infof("[handler] startQueuedTaskThreadInput input=%s task=%s no usable current model", input.ID, task.ID)
		if unstartable {
			h.cancelUnstartableQueuedInput(ctx, input)
			h.startNextQueuedTurnAfter(ctx, streamingResponseParams{ProjectID: task.ProjectID, TaskID: task.ID, IsTaskFollowup: true}, "")
			return nil
		}
		return fmt.Errorf("model not found for queued task-thread input: %s", input.AgentConfigID)
	}
	exec := &models.Execution{
		TaskID:        input.TaskID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    input.Content,
		IsFollowup:    true,
	}
	if err := h.threadInputRepo.ClaimQueuedForTaskExecution(ctx, input.ID, exec); err != nil {
		if err != repository.ErrInputNotPending {
			applog.Infof("[handler] startQueuedTaskThreadInput input=%s claim error: %v", input.ID, err)
		}
		return err
	}
	if h.workerSvc != nil {
		h.workerSvc.ClearCancellationRequested(input.TaskID)
	}
	if err := h.applySwarmChildFollowupStart(ctx, task, input.Content); err != nil {
		applog.Infof("[handler] startQueuedTaskThreadInput input=%s swarm child follow-up routing failed: %v", input.ID, err)
		h.completeWithFailure(ctx, exec.ID, exec.TaskID, err.Error(), 0, channelReplyFromThreadInput(input))
		go h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{ProjectID: task.ProjectID, TaskID: task.ID, IsTaskFollowup: true}, exec.ID)
		return nil
	}
	task.Status = models.StatusQueued
	task.Category = models.CategoryActive
	var attachmentContext string
	var imageAttachments []models.Attachment
	if input.AttachmentSessionID != "" {
		var attErr error
		attachmentContext, imageAttachments, _, attErr = h.processAttachmentsWithReturn(ctx, input.AttachmentSessionID, exec.ID)
		if attErr != nil {
			applog.Infof("[handler] startQueuedTaskThreadInput exec=%s attachment processing error: %v", exec.ID, attErr)
			attachmentContext = fmt.Sprintf("⚠️ Attachment processing error: %v", attErr)
		}
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadExecutionStarted,
			ProjectID:      task.ProjectID,
			TaskID:         input.TaskID,
			TaskName:       task.Title,
			ExecID:         exec.ID,
			Message:        input.Content,
			PendingInputID: input.ID,
		})
		h.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputApplied,
			ProjectID:      task.ProjectID,
			TaskID:         input.TaskID,
			TaskName:       task.Title,
			ExecID:         exec.ID,
			Message:        input.Content,
			PendingInputID: input.ID,
		})
	}
	h.resumeUserStoppedGoalForManualStart(ctx, input.TaskID, string(input.Source), input.OriginAgent)
	h.reactivateAchievedGoalForManualFollowup(ctx, input.TaskID, string(input.Source), input.OriginAgent)
	priorExecs, _ := h.execRepo.ListByTaskChronologicalLimit(ctx, exec.TaskID, taskThreadHistoryLimit)
	priorHistory := filterChatHistory(priorExecs, exec.ID)
	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && h.agentRepo != nil {
		if ad, adErr := h.agentRepo.GetByID(ctx, *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
		}
	}
	systemContext := combineContexts(buildThreadSystemContext(task.Title, len(priorHistory) > 0, attachmentContext), h.taskGoalContext(ctx, task.ID, agentDef))
	personalityContext := h.getPersonalityContext(ctx, task.ProjectID)
	workDir, worktreeContext, workDirErr := h.resolveWorktreeWorkDir(ctx, task)
	if workDirErr != nil {
		h.completeWithFailure(ctx, exec.ID, exec.TaskID, workDirErr.Error(), 0, channelReplyFromThreadInput(input))
		go h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{ProjectID: task.ProjectID, TaskID: task.ID, IsTaskFollowup: true}, exec.ID)
		return nil
	}
	var automationContext *models.AutomationContext
	if h.automationGraphSvc != nil {
		if value, contextErr := h.automationGraphSvc.ContextForThreadInput(ctx, task.ProjectID, input.ID); contextErr != nil {
			applog.Infof("[handler] startQueuedTaskThreadInput input=%s automation context load failed: %v", input.ID, contextErr)
		} else if len(value.Bindings) > 0 {
			automationContext = &value
		}
	}
	h.startStreamingResponse(streamingResponseParams{
		ExecID:            exec.ID,
		TaskID:            exec.TaskID,
		Message:           input.Content,
		Agent:             *agent,
		AgentDefinition:   agentDef,
		ChatHistory:       priorHistory,
		ProjectID:         task.ProjectID,
		SystemContext:     combineContexts(combineContexts(systemContext, worktreeContext), personalityContext),
		WorkDir:           workDir,
		ImageAttachments:  imageAttachments,
		IsTaskFollowup:    true,
		ChannelReply:      channelReplyFromThreadInput(input),
		InputOrigin:       string(input.Source),
		InputOriginAgent:  input.OriginAgent,
		Task:              task,
		AutomationContext: automationContext,
		updateWorkDone:    updateWorkDone,
	})
	updateWorkDone = nil
	return nil
}

func (h *Handler) registerTaskCancellation(taskID string, cancel context.CancelFunc) {
	if h.workerSvc != nil {
		h.workerSvc.RegisterCancel(taskID, cancel)
	}
}

// deregisterTaskCancellation removes a task's cancel function from the worker service.
// No-op if worker service is unavailable.
func (h *Handler) deregisterTaskCancellation(taskID string) {
	if h.workerSvc != nil {
		h.workerSvc.DeregisterCancel(taskID)
	}
}

// completeWithSuccess marks an execution and its task as completed.
// Also moves Active tasks to the Completed category so they appear in the right column.
// Logs errors but does not fail since this runs in a background goroutine.
// Captures git diff if workDir is provided.
func (h *Handler) completeWithSuccess(ctx context.Context, execID, taskID, output, workDir string, tokensUsed int, durationMs int64, completionOptions ...interface{}) repository.CompleteSuccessOutcome {
	telegramMessageID, channelReply := parseCompletionOptions(completionOptions...)
	outcome, err := h.execRepo.CompleteSuccessIfNoPendingSteering(ctx, execID, output, tokensUsed, durationMs)
	if err != nil {
		applog.Infof("[handler] completeWithSuccess exec=%s error completing execution: %v", execID, err)
		return repository.CompleteSuccessAlreadyTerminal
	}
	if outcome == repository.CompleteSuccessPendingSteering {
		applog.Infof("[handler] completeWithSuccess exec=%s deferred completion because pending steering exists", execID)
		return outcome
	}
	if outcome == repository.CompleteSuccessAlreadyTerminal {
		applog.Infof("[handler] completeWithSuccess exec=%s skipped because execution is already terminal", execID)
		return outcome
	}

	// Load task state before publishing the terminal event so any final status
	// transition is visible when the client refreshes in response to ExecCompleted.
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] completeWithSuccess task=%s error getting task: %v", taskID, err)
	}
	if blocked, reason := h.blockGitHubSDLCSuccessWithoutPullRequest(ctx, task); blocked {
		if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
			applog.Infof("[handler] completeWithSuccess task=%s error marking missing GitHub SDLC PR failure: %v", taskID, err)
		}
		if task != nil && (task.Category == models.CategoryActive || task.Category == models.CategoryCompleted) {
			if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryBacklog); err != nil {
				applog.Infof("[handler] completeWithSuccess task=%s error moving missing GitHub SDLC PR failure to backlog: %v", taskID, err)
			}
		}
		h.sendChannelResponse(ctx, task, channelReply, output, reason, telegramMessageID)
		if task != nil && h.alertSvc != nil {
			if err := h.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, taskID, execID, task.Title, reason); err != nil {
				applog.Infof("[handler] completeWithSuccess task=%s error creating missing GitHub SDLC PR failure alert: %v", taskID, err)
			}
		}
		h.publishExecutionTerminal(execID, models.ExecCompleted, "")
		h.notifySwarmChildTerminal(ctx, taskID)
		return repository.CompleteSuccessCompleted
	}

	// Update task status BEFORE git diff capture. The SSE handler detects
	// ExecCompleted and sends a 'done' event, triggering a client-side page
	// refresh. If the task status update happens after git diff capture
	// (which can be slow), the refreshed page may still show 'running' status.
	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusCompleted); err != nil {
		applog.Infof("[handler] completeWithSuccess task=%s error updating status: %v", taskID, err)
	}
	h.publishExecutionTerminal(execID, models.ExecCompleted, "")

	// Move active tasks to the completed category so they appear in the right column
	h.sendChannelResponse(ctx, task, channelReply, output, "", telegramMessageID)
	if task != nil && task.Category == models.CategoryActive {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryCompleted); err != nil {
			applog.Infof("[handler] completeWithSuccess task=%s error moving to completed category: %v", taskID, err)
		} else {
			applog.Infof("[handler] completeWithSuccess task=%s moved to completed category", taskID)
		}
	}

	// Capture git diff after status update so it doesn't delay the UI refresh
	if workDir != "" {
		execForCommit, _ := h.execRepo.GetByID(ctx, execID)
		diffOutput := h.captureTaskDiffOutput(ctx, task, execForCommit, workDir, output)

		if diffOutput != "" {
			if err := h.execRepo.UpdateDiffOutput(ctx, execID, diffOutput); err != nil {
				applog.Infof("[handler] completeWithSuccess exec=%s error updating diff: %v", execID, err)
			}

			// Reset stale terminal merge states when follow-up creates new changes.
			if task != nil && task.WorktreePath != "" &&
				(task.MergeStatus == models.MergeStatusMerged || task.MergeStatus == models.MergeStatusConflict) {
				applog.Infof("[handler] completeWithSuccess task=%s resetting merge_status from %s to pending (new changes detected)", taskID, task.MergeStatus)
				if err := h.taskRepo.UpdateMergeStatus(ctx, taskID, models.MergeStatusPending); err != nil {
					applog.Infof("[handler] completeWithSuccess task=%s error resetting merge status: %v", taskID, err)
				}
			}
		}
	}
	h.notifySwarmChildTerminal(ctx, taskID)
	return repository.CompleteSuccessCompleted
}

func (h *Handler) blockGitHubSDLCSuccessWithoutPullRequest(ctx context.Context, task *models.Task) (bool, string) {
	if h == nil || task == nil || h.automationGraphSvc == nil {
		return false, ""
	}
	provenance, err := h.automationGraphSvc.GitHubIssueTaskProvenance(ctx, task.ProjectID, task.ID)
	if err != nil {
		return true, fmt.Sprintf("GitHub SDLC pull request publication could not be verified: %v", err)
	}
	if provenance == nil {
		return false, ""
	}
	if h.taskPullRequestRepo == nil {
		return true, "GitHub SDLC pull request publication could not be verified because pull request records are unavailable"
	}
	pullRequest, err := h.taskPullRequestRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		return true, fmt.Sprintf("GitHub SDLC pull request publication could not be verified: %v", err)
	}
	if pullRequest == nil {
		return true, "GitHub SDLC implementation completed without publishing a pull request; rerun after resolving PR publication"
	}
	if !service.IsOpenPullRequestState(pullRequest.PRState) {
		state := strings.TrimSpace(pullRequest.PRState)
		if state == "" {
			state = "not open"
		}
		return true, fmt.Sprintf("GitHub SDLC implementation linked pull request #%d is %s; rerun after resolving PR publication", pullRequest.PRNumber, state)
	}
	if h.githubSvc == nil || h.projectRepo == nil {
		return true, "GitHub SDLC pull request publication could not be verified because GitHub live-state verification is unavailable"
	}
	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil {
		return true, fmt.Sprintf("GitHub SDLC pull request publication could not be verified: %v", err)
	}
	if project == nil {
		return true, "GitHub SDLC pull request publication could not be verified because the project was not found"
	}
	repoPathForResolution := ""
	if strings.TrimSpace(project.RepoURL) == "" {
		repoPathForResolution = project.RepoPath
	}
	repoRef, err := h.githubSvc.ResolveRepo(ctx, project.RepoURL, repoPathForResolution)
	if err != nil {
		return true, fmt.Sprintf("GitHub SDLC pull request publication could not be verified: %v", err)
	}
	if err := service.ConfigureGitHubRepoEndpoint(repoRef, h.githubSvc.GlobalAPIEndpoint(ctx)); err != nil {
		return true, fmt.Sprintf("GitHub SDLC pull request publication could not be verified: %v", err)
	}
	livePR, err := h.githubSvc.GetPullRequest(ctx, repoRef, pullRequest.PRNumber)
	if err != nil {
		return true, fmt.Sprintf("GitHub SDLC pull request publication could not be verified: %v", err)
	}
	if err := service.ValidateTaskPullRequestCurrentPublication(project, task, repoRef, livePR, pullRequest.PublishedHeadSHA); err != nil {
		return true, fmt.Sprintf("GitHub SDLC implementation linked pull request #%d is not reviewable with the current published task work: %v; rerun after resolving PR publication", pullRequest.PRNumber, err)
	}
	return false, ""
}

func (h *Handler) notifySwarmChildTerminal(ctx context.Context, taskID string) {
	if h.swarmSvc == nil || taskID == "" {
		return
	}
	if err := h.swarmSvc.OnChildCompleted(ctx, taskID); err != nil {
		applog.Infof("[handler] swarm child completion hook task=%s error: %v", taskID, err)
	}
}

func (h *Handler) recordStreamingUsage(ctx context.Context, params streamingResponseParams, result llmcontracts.AgentResult, status, errMsg string, durationMs int64) {
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
	}
	operation := string(llmcontracts.OperationStreaming)
	if params.IsTaskFollowup {
		operation = "task_followup"
	}
	service.RecordUsageFromResult(ctx, h.usageRepo, service.UsageCapture{
		ProjectID:    params.ProjectID,
		TaskID:       params.TaskID,
		ExecutionID:  params.ExecID,
		ChatThreadID: params.TaskID,
		TurnID:       params.ExecID,
		Operation:    operation,
		Status:       status,
		ErrorMessage: errMsg,
		LatencyMs:    durationMs,
		OccurredAt:   time.Now().UTC(),
	}, params.Agent, result)
}

func firstInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	return values[0]
}

func parseCompletionOptions(options ...interface{}) (int, service.ChannelReplyContext) {
	var telegramMessageID int
	var channelReply service.ChannelReplyContext
	for _, option := range options {
		switch v := option.(type) {
		case int:
			telegramMessageID = v
		case service.ChannelReplyContext:
			channelReply = v
		}
	}
	return telegramMessageID, channelReply
}

func (h *Handler) completeWithCancellation(execID, taskID, output string, tokensUsed int, durationMs int64, telegramMessageID int, channelReply ...service.ChannelReplyContext) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.execRepo.Complete(ctx, execID, models.ExecCancelled, output, "cancelled", tokensUsed, durationMs); err != nil {
		applog.Infof("[handler] completeWithCancellation exec=%s error completing cancelled execution: %v", execID, err)
	} else {
		h.publishExecutionTerminal(execID, models.ExecCancelled, "cancelled")
	}
	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusCancelled); err != nil {
		applog.Infof("[handler] completeWithCancellation task=%s error updating status: %v", taskID, err)
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] completeWithCancellation task=%s error getting task: %v", taskID, err)
		return
	}
	if task != nil && task.Category == models.CategoryActive {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryBacklog); err != nil {
			applog.Infof("[handler] completeWithCancellation task=%s error moving to backlog: %v", taskID, err)
		} else {
			applog.Infof("[handler] completeWithCancellation task=%s moved to backlog", taskID)
			task.Category = models.CategoryBacklog
		}
	}
	reply := service.ChannelReplyContext{}
	if len(channelReply) > 0 {
		reply = channelReply[0]
	}
	h.notifySwarmChildTerminal(ctx, taskID)
	h.sendChannelResponse(ctx, task, reply, output, "cancelled", telegramMessageID)
}

func (h *Handler) sendChannelResponse(ctx context.Context, task *models.Task, reply service.ChannelReplyContext, output, errMsg string, telegramMessageID int) {
	if task == nil {
		return
	}
	if reply.Source == models.TaskOriginTelegram && task.Category != models.CategoryChat && reply.TelegramChatID != 0 {
		if h.telegramService != nil {
			h.telegramService.SendTaskCompletionToChat(ctx, reply.TelegramChatID, task.Title, output, errMsg)
		}
		return
	}
	if reply.Source == models.TaskOriginSlack && task.Category != models.CategoryChat && reply.SlackChannelID != "" {
		if svc, ok := h.slackSvc.(interface {
			SendTaskCompletionToThread(context.Context, string, string, string, string, string, string)
		}); ok {
			svc.SendTaskCompletionToThread(ctx, reply.SlackChannelID, reply.SlackThreadTS, task.Title, output, errMsg, reply.SlackUserID)
		}
		return
	}
	if reply.Source == models.TaskOriginEmail && task.Category != models.CategoryChat && reply.EmailFrom != "" {
		if h.emailService != nil {
			h.emailService.SendTaskCompletionToThread(ctx, reply.EmailFrom, reply.EmailMessageID, reply.EmailReferences, reply.EmailSubject, task.Title, output, errMsg)
		}
		return
	}
	if reply.Source == models.TaskOriginDiscord && task.Category != models.CategoryChat && reply.DiscordChannelID != "" {
		if svc, ok := h.discordSvc.(interface {
			SendTaskCompletionToThread(context.Context, string, string, string, string, string, string, string)
		}); ok {
			svc.SendTaskCompletionToThread(ctx, reply.DiscordChannelID, reply.DiscordThreadID, reply.DiscordMessageID, task.Title, output, errMsg, reply.DiscordUserID)
		}
		return
	}
	switch task.CreatedVia {
	case models.TaskOriginTelegram:
		if h.telegramService == nil {
			return
		}
		if task.Category == models.CategoryChat {
			h.telegramService.SendChatResponse(ctx, *task, output, errMsg, telegramMessageID)
		} else {
			h.telegramService.SendTaskCompletionNotification(ctx, *task, output, errMsg)
		}
	case models.TaskOriginSlack:
		if task.Category == models.CategoryChat {
			if svc, ok := h.slackSvc.(interface {
				SendChatResponse(context.Context, models.Task, string, string)
			}); ok {
				svc.SendChatResponse(ctx, *task, output, errMsg)
			}
		} else if svc, ok := h.slackSvc.(interface {
			SendTaskCompletionNotification(context.Context, models.Task, string, string)
		}); ok {
			svc.SendTaskCompletionNotification(ctx, *task, output, errMsg)
		}
	case models.TaskOriginEmail:
		if h.emailService == nil {
			return
		}
		if task.Category == models.CategoryChat {
			h.emailService.SendChatResponse(ctx, *task, output, errMsg)
		} else {
			h.emailService.SendTaskCompletionNotification(ctx, *task, output, errMsg)
		}
	case models.TaskOriginDiscord:
		if task.Category == models.CategoryChat {
			if svc, ok := h.discordSvc.(interface {
				SendChatResponse(context.Context, models.Task, string, string)
			}); ok {
				svc.SendChatResponse(ctx, *task, output, errMsg)
			}
		} else if svc, ok := h.discordSvc.(interface {
			SendTaskCompletionNotification(context.Context, models.Task, string, string)
		}); ok {
			svc.SendTaskCompletionNotification(ctx, *task, output, errMsg)
		}
	}
}

func normalizeDiffSnapshot(diff string) string {
	return strings.TrimSpace(diff)
}

// resolveTaskChangesWorktreeRefs identifies the managed worktree and explicit
// comparison target used by Task Changes. Worktree tasks are review views: the
// base is the task's merge target (or resolved repository default), and the tip
// is the current worktree state. git diff HEAD is reserved for non-worktree
// executions that only expose pending changes.
func resolveTaskChangesWorktreeRefs(task *models.Task, project *models.Project, workDir string) (repoDir, branch, target string, ok bool) {
	if task == nil || project == nil || project.RepoPath == "" || workDir == "" || !service.IsGitRepo(workDir) {
		return "", "", "", false
	}
	managedPath := task.WorktreePath == workDir
	if !managedPath {
		rel, err := filepath.Rel(project.RepoPath, workDir)
		base := filepath.Base(workDir)
		expectedBase := "task_" + task.ID
		managedPath = err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) &&
			filepath.Dir(rel) == ".worktrees" && (base == expectedBase || strings.HasPrefix(base, expectedBase+"_followup_"))
	}
	if !managedPath {
		return "", "", "", false
	}

	target = task.MergeTargetBranch
	if target == "" {
		target = service.GetDefaultBranch(project.RepoPath)
	}
	if target == "" {
		return "", "", "", false
	}
	branch = service.GetCurrentBranch(workDir)
	if branch == "" {
		branch = task.WorktreeBranch
	}
	return project.RepoPath, branch, target, true
}

func (h *Handler) startFollowupDiffSnapshotBroadcast(ctx context.Context, taskID, execID, workDir string, isTaskFollowup bool) (chan struct{}, chan struct{}) {
	if !isTaskFollowup || workDir == "" || h.fileChangeBroadcaster == nil || h.execRepo == nil {
		return nil, nil
	}

	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, nil
	}

	project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
	repoDir, worktreeBranch, targetBranch, worktreeDiff := resolveTaskChangesWorktreeRefs(task, project, workDir)
	if worktreeDiff {
		applog.Debugf("[task-changes] follow-up diff base task=%s target=%s branch=%s worktree=%s", task.ID, targetBranch, worktreeBranch, workDir)
	}

	captureDiff := func() string {
		if worktreeDiff {
			return service.GetWorktreeDiffWithUncommitted(repoDir, worktreeBranch, targetBranch, workDir)
		}
		return h.llmSvc.CaptureGitDiff(workDir)
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		state := &service.DiffSnapshotState{}
		publish := func(final bool) {
			diffOutput := captureDiff()
			if diffOutput == "" {
				return
			}
			service.PublishDiffSnapshotIfChanged(ctx, h.execRepo, h.fileChangeBroadcaster, state, taskID, execID, diffOutput, final)
		}

		for {
			select {
			case <-stop:
				publish(true)
				return
			default:
			}

			select {
			case <-stop:
				publish(true)
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				publish(false)
			}
		}
	}()

	return stop, done
}

func (h *Handler) captureTaskDiffOutput(ctx context.Context, task *models.Task, exec *models.Execution, workDir string, outputSummary string) string {
	if workDir == "" {
		return ""
	}

	if task != nil {
		project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
		repoDir, worktreeBranch, targetBranch, worktreeDiff := resolveTaskChangesWorktreeRefs(task, project, workDir)
		if worktreeDiff {
			commitCtx := service.WorktreeCommitMessageContext{
				Phase:     service.WorktreeCommitPhaseFollowup,
				TaskTitle: task.Title,
				Summary:   outputSummary,
			}
			if exec != nil {
				commitCtx.TurnIntent = exec.PromptSent
				if h.llmSvc != nil {
					commitCtx.DiffSummary = h.llmSvc.SummarizeWorktreeCommitDiffForAgentID(ctx, workDir, exec.AgentConfigID, commitCtx)
				}
			}
			service.CommitWorktreeChanges(workDir, service.BuildWorktreeCommitMessage(workDir, commitCtx))
			return service.GetWorktreeDiffWithUncommitted(repoDir, worktreeBranch, targetBranch, workDir)
		}
	}

	return h.llmSvc.CaptureGitDiff(workDir)
}

// completeWithFailure marks an execution and its task as failed, moves it to backlog,
// and creates a failure alert. Uses a fresh background context with a 30-second timeout
// to ensure DB updates succeed even when the original context has expired (e.g., after
// the 5-minute LLM processing timeout).
func (h *Handler) completeWithFailure(_ context.Context, execID, taskID, errorMessage string, durationMs int64, completionOptions ...interface{}) {
	telegramMessageID, channelReply := parseCompletionOptions(completionOptions...)
	// Use a fresh context — the caller's context may already be expired (e.g., after
	// chatProcessingTimeout). DB updates must still succeed.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.execRepo.Complete(ctx, execID, models.ExecFailed, "", errorMessage, 0, durationMs); err != nil {
		applog.Infof("[handler] completeWithFailure exec=%s error completing execution: %v", execID, err)
	} else {
		h.publishExecutionTerminal(execID, models.ExecFailed, errorMessage)
	}

	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
		applog.Infof("[handler] completeWithFailure task=%s error updating status: %v", taskID, err)
	}

	// Move task to backlog so it can be re-executed
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] completeWithFailure task=%s error getting task: %v", taskID, err)
		return
	}
	h.sendChannelResponse(ctx, task, channelReply, "", errorMessage, telegramMessageID)
	if task != nil && (task.Category == models.CategoryActive || task.Category == models.CategoryCompleted) {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryBacklog); err != nil {
			applog.Infof("[handler] completeWithFailure task=%s error moving to backlog: %v", taskID, err)
		} else {
			applog.Infof("[handler] completeWithFailure task=%s moved to backlog", taskID)
		}
	}

	// Create failure alert
	if task != nil && h.alertSvc != nil {
		if err := h.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, taskID, execID, task.Title, errorMessage); err != nil {
			applog.Infof("[handler] completeWithFailure task=%s error creating alert: %v", taskID, err)
		}
	}
	h.notifySwarmChildTerminal(ctx, taskID)
}

// completeWithFailureAndOutput is like completeWithFailure but preserves partial output
// (e.g., when max_tokens is hit and the LLM produced output before the limit).
func (h *Handler) completeWithFailureAndOutput(_ context.Context, execID, taskID, errorMessage, output string, tokensUsed int, durationMs int64, completionOptions ...interface{}) {
	telegramMessageID, channelReply := parseCompletionOptions(completionOptions...)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.execRepo.Complete(ctx, execID, models.ExecFailed, output, errorMessage, tokensUsed, durationMs); err != nil {
		applog.Infof("[handler] completeWithFailureAndOutput exec=%s error completing execution: %v", execID, err)
	} else {
		h.publishExecutionTerminal(execID, models.ExecFailed, errorMessage)
	}

	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
		applog.Infof("[handler] completeWithFailureAndOutput task=%s error updating status: %v", taskID, err)
	}

	// Move task to backlog so it can be re-executed
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		applog.Infof("[handler] completeWithFailureAndOutput task=%s error getting task: %v", taskID, err)
		return
	}
	h.sendChannelResponse(ctx, task, channelReply, output, errorMessage, telegramMessageID)
	if task != nil && (task.Category == models.CategoryActive || task.Category == models.CategoryCompleted) {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryBacklog); err != nil {
			applog.Infof("[handler] completeWithFailureAndOutput task=%s error moving to backlog: %v", taskID, err)
		} else {
			applog.Infof("[handler] completeWithFailureAndOutput task=%s moved to backlog", taskID)
		}
	}

	// Create failure alert
	if task != nil && h.alertSvc != nil {
		if err := h.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, taskID, execID, task.Title, errorMessage); err != nil {
			applog.Infof("[handler] completeWithFailureAndOutput task=%s error creating alert: %v", taskID, err)
		}
	}
	h.notifySwarmChildTerminal(ctx, taskID)
}

// filterChatHistory filters a list of executions to exclude the current execution
// and any running executions, returning only completed/failed executions for
// conversation context.
//
// The function ensures the returned slice is non-nil (even when empty) so that
// CallAgentDirectStreaming correctly routes to the chat path instead of treating
// it as a single-turn execution.
//
// Parameters:
//   - executions: all executions to filter (typically from a task or chat history)
//   - currentExecID: the execution ID currently being processed (will be excluded)
//
// Returns a slice of executions suitable for conversation history, preserving order.
func filterChatHistory(executions []models.Execution, currentExecID string) []models.Execution {
	if len(executions) == 0 {
		return []models.Execution{}
	}

	start := 0
	for i := len(executions) - 1; i >= 0; i-- {
		if executions[i].StartsNewContext && executions[i].ID != currentExecID {
			start = i
			break
		}
	}
	result := make([]models.Execution, 0, len(executions)-start)
	for i := start; i < len(executions); i++ {
		if executions[i].ID == currentExecID || executions[i].Status == models.ExecRunning {
			continue
		}
		result = append(result, executions[i])
	}
	return result
}

// selectAgent handles agent selection with vision-awareness for both chat and task thread.
//
// Selection logic:
//   - If agentID is "default", uses the project's default model (marked IsDefault in agent_configs)
//   - If agentID is specified (not "auto", "default", or empty), validates and returns that agent
//   - If agentID is "auto" or empty, automatically selects based on:
//   - Message complexity (using service.AnalyzeComplexity)
//   - Vision requirements (hasImages flag)
//   - Available vision-capable models (Anthropic provider required for images)
//
// Parameters:
//   - ctx: request context
//   - agentID: specific agent ID, "auto", "default", or empty for auto-selection
//   - message: user's message text (used for complexity analysis)
//   - hasImages: whether the request includes image attachments
//
// Returns the selected LLM configuration or an error if no suitable agent is found.
// Logs a warning if a non-Anthropic agent is explicitly selected with image attachments.
func (h *Handler) selectAgent(ctx context.Context, agentID, message string, hasImages bool) (*models.LLMConfig, error) {
	// Default model selection
	if agentID == "default" {
		return h.selectDefaultAgent(ctx, hasImages)
	}

	// Explicit agent selection
	if agentID != "" && agentID != "auto" {
		return h.selectExplicitAgent(ctx, agentID, hasImages)
	}

	// Auto-select
	return h.autoSelectAgent(ctx, message, hasImages)
}

// selectDefaultAgent retrieves the project's default model (the one marked IsDefault in agent_configs).
// Falls back to the first available agent if no default is configured.
func (h *Handler) selectDefaultAgent(ctx context.Context, hasImages bool) (*models.LLMConfig, error) {
	agent, err := h.llmConfigRepo.GetDefault(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get default agent: %w", err)
	}
	if agent == nil {
		// No default configured — fall back to first available
		agents, err := h.llmConfigRepo.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list agents: %w", err)
		}
		if len(agents) == 0 {
			return nil, fmt.Errorf("no agents configured - please add at least one agent/model in settings")
		}
		applog.Infof("[handler] selectDefaultAgent no default configured, falling back to first agent: %s", agents[0].Name)
		return &agents[0], nil
	}
	if hasImages && !agent.IsAnthropicAPIKey() && !agent.IsOAuth() {
		applog.Infof("[handler] selectDefaultAgent warning: agent %s may not support vision with image attachments", agent.Name)
	}
	return agent, nil
}

// selectExplicitAgent retrieves and validates an explicitly specified agent.
func (h *Handler) selectExplicitAgent(ctx context.Context, agentID string, hasImages bool) (*models.LLMConfig, error) {
	agent, err := h.llmConfigRepo.GetByID(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent %s: %w", agentID, err)
	}
	if agent == nil {
		return nil, fmt.Errorf("agent %s not found", agentID)
	}
	if hasImages && !agent.IsAnthropicAPIKey() && !agent.IsOAuth() {
		applog.Infof("[handler] selectExplicitAgent warning: agent %s may not support vision with image attachments", agent.Name)
	}
	return agent, nil
}

// autoSelectAgent automatically selects an agent based on message complexity and vision requirements.
func (h *Handler) autoSelectAgent(ctx context.Context, message string, hasImages bool) (*models.LLMConfig, error) {
	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list agents for auto-selection: %w", err)
	}
	if len(agents) == 0 {
		return nil, fmt.Errorf("no agents configured - please add at least one agent/model in settings")
	}

	complexity := service.AnalyzeComplexity(message)
	if result := service.SelectLLMWithVision(complexity, agents, hasImages); result != nil {
		return result.LLMConfig, nil
	}

	// Fallback to first agent
	if hasImages {
		applog.Infof("[handler] autoSelectAgent has images but no vision-capable agents, falling back to first available")
	}
	return &agents[0], nil
}

// resolveWorkDir retrieves the repository path for a project to use as the working
// directory for CLI-based agents (e.g., Claude CLI subprocess execution).
//
// Returns an empty string if the project is not found or has no configured repo path.
// This graceful degradation allows the LLM service to handle missing work directories
// appropriately based on the agent type.
func (h *Handler) resolveWorkDir(ctx context.Context, projectID string) string {
	if project, err := h.projectSvc.GetByID(ctx, projectID); err == nil && project != nil {
		return project.RepoPath
	}
	return ""
}

// resolveWorktreeWorkDir resolves the working directory for a task followup,
// preferring the task's git worktree. Falls back to project repo path for
// non-git projects, chat tasks, or when worktree service is unavailable.
func (h *Handler) resolveWorktreeWorkDir(ctx context.Context, task *models.Task) (string, string, error) {
	project, err := h.projectSvc.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return "", "", nil
	}
	repoDir := project.RepoPath

	if task.Category == models.CategoryChat || !service.IsGitRepo(repoDir) {
		return repoDir, "", nil
	}

	if h.worktreeSvc == nil {
		return repoDir, "", nil
	}

	wtPath, wtBranch, skipStartupSync, wtErr := h.worktreeSvc.SetupFollowupWorktree(ctx, task, repoDir)
	if wtErr != nil {
		applog.Infof("[handler] resolveWorktreeWorkDir worktree setup failed for task %s; refusing to use main repo: %v", task.ID, wtErr)
		return "", "", fmt.Errorf("setting up isolated task worktree: %w", wtErr)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = wtBranch

	if skipStartupSync {
		applog.Infof("[handler] resolveWorktreeWorkDir task=%s using fresh current-target follow-up worktree path=%s", task.ID, wtPath)
		return wtPath, "", nil
	}

	if syncErr := h.worktreeSvc.SyncWorktreeFromMainAtStart(ctx, task, repoDir); syncErr != nil {
		var conflictErr *service.StartupSyncConflictError
		if errors.As(syncErr, &conflictErr) {
			applog.Infof("[handler] resolveWorktreeWorkDir startup worktree sync conflict for task follow-up %s, continuing in preserved worktree: %v", task.ID, syncErr)
			return wtPath, buildStartupSyncConflictContext(conflictErr), nil
		}
		applog.Infof("[handler] resolveWorktreeWorkDir startup worktree sync failed for task %s: %v", task.ID, syncErr)
		return "", "", syncErr
	}

	applog.Infof("[handler] resolveWorktreeWorkDir task=%s using worktree path=%s", task.ID, wtPath)
	return wtPath, "", nil
}

// executeChatTaskCreationRequests creates tasks from typed runtime-tool requests,
// copies Chat attachments, and defers activation until attachment publication succeeds.
func (h *Handler) executeChatTaskCreationRequests(ctx context.Context, execID, projectID string, taskRequests []service.TaskCreationRequest, agents []models.LLMConfig, channelReply ...service.ChannelReplyContext) (string, int) {
	if len(taskRequests) == 0 {
		return "", 0
	}

	applog.Infof("[handler] executeChatTaskCreationRequests exec=%s found %d task creation requests", execID, len(taskRequests))

	chatAtts, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	if err != nil {
		applog.Infof("[handler] attachment lifecycle stage=metadata-load execution=%s destination_task=uncreated error=%v", execID, err)
		return fmt.Sprintf("Failed to create %d task(s): Chat attachments could not be loaded; no tasks were created.", len(taskRequests)), 0
	}
	deferredRequestIndexes := h.deferActiveTasksWithAttachments(taskRequests, chatAtts, agents)

	createdResults, summary := h.executeChatTaskCreationsWithAttachments(ctx, taskRequests, projectID, execID, agents)
	createdTasks := tasksFromCreationResults(createdResults)
	h.applyChannelOriginToCreatedTasks(ctx, createdTasks, firstChannelReply(channelReply))

	totalAttachmentsCopied, attachmentReadyByTask := h.copyAttachmentsToTasks(ctx, execID, createdTasks, chatAtts)
	activatedTaskIDs := h.activateDeferredTasks(ctx, createdResults, deferredRequestIndexes, attachmentReadyByTask)
	summary = summarizeActivatedTasks(summary, createdResults, activatedTaskIDs)
	if len(attachmentReadyByTask) != len(createdTasks) {
		applog.Infof("[handler] attachment lifecycle stage=activation-partial execution=%s ready=%d tasks=%d", execID, len(attachmentReadyByTask), len(createdTasks))
		summary += "\nAttachment conversion failed; affected tasks were left in Backlog without broken attachment records."
	}

	return strings.TrimSpace(h.appendCreationSummary("", summary, totalAttachmentsCopied, chatAtts)), totalAttachmentsCopied
}

// deferActiveTasksWithAttachments resolves each request's effective category and
// defers anything that would activate until attachment conversion succeeds.
// Returns the original request indexes that should be activated after copying.
func firstChannelReply(replies []service.ChannelReplyContext) service.ChannelReplyContext {
	if len(replies) == 0 {
		return service.ChannelReplyContext{}
	}
	return replies[0]
}

func (h *Handler) applyChannelOriginToCreatedTasks(ctx context.Context, tasks []models.Task, reply service.ChannelReplyContext) {
	if len(tasks) == 0 || reply.Source != models.TaskOriginEmail || strings.TrimSpace(reply.EmailFrom) == "" {
		return
	}
	for _, task := range tasks {
		if h.taskRepo != nil {
			if err := h.taskRepo.UpdateEmailOrigin(ctx, task.ID); err != nil {
				applog.Infof("[handler] applyChannelOriginToCreatedTasks task=%s email origin update failed: %v", task.ID, err)
			}
		}
		if h.emailTaskContextRepo != nil {
			if err := h.emailTaskContextRepo.Upsert(ctx, &models.EmailTaskContext{
				TaskID:          task.ID,
				EmailFrom:       reply.EmailFrom,
				EmailMessageID:  reply.EmailMessageID,
				EmailReferences: reply.EmailReferences,
				EmailSubject:    reply.EmailSubject,
				EmailSessionKey: reply.EmailSessionKey,
			}); err != nil {
				applog.Infof("[handler] applyChannelOriginToCreatedTasks task=%s email context update failed: %v", task.ID, err)
			}
		}
	}
}

func (h *Handler) deferActiveTasksWithAttachments(taskRequests []service.TaskCreationRequest, chatAtts []models.ChatAttachment, agents []models.LLMConfig) map[int]bool {
	if len(chatAtts) == 0 {
		return nil
	}

	deferredRequestIndexes := make(map[int]bool)
	for i := range taskRequests {
		if service.EffectiveTaskCreationCategory(taskRequests[i], agents) == models.CategoryActive {
			deferredRequestIndexes[i] = true
			taskRequests[i].Category = string(models.CategoryBacklog)
			applog.Infof("[handler] deferActiveTasksWithAttachments deferred auto-submit for request=%d task=%q (has attachments)", i, taskRequests[i].Title)
		}
	}
	return deferredRequestIndexes
}

// copyAttachmentsToTasks copies chat attachments to all created tasks.
// Returns the total count of attachments successfully copied and the IDs of tasks
// whose complete attachment batch is ready.
func (h *Handler) copyAttachmentsToTasks(ctx context.Context, execID string, createdTasks []models.Task, chatAtts []models.ChatAttachment) (int, map[string]bool) {
	readyByTask := make(map[string]bool, len(createdTasks))
	if len(chatAtts) == 0 {
		for _, task := range createdTasks {
			readyByTask[task.ID] = true
		}
		return 0, readyByTask
	}

	totalCopied := 0
	for _, task := range createdTasks {
		copiedCount, err := h.copyChatAttachmentsToTask(ctx, execID, task.ID)
		if err != nil {
			applog.Infof("[handler] attachment lifecycle stage=convert execution=%s task=%s error=%v", execID, task.ID, err)
		} else if copiedCount != len(chatAtts) {
			applog.Infof("[handler] attachment lifecycle stage=verify-count execution=%s task=%s expected=%d copied=%d", execID, task.ID, len(chatAtts), copiedCount)
		} else {
			totalCopied += copiedCount
			readyByTask[task.ID] = true
		}
	}
	return totalCopied, readyByTask
}

func tasksFromCreationResults(results []service.TaskCreationResult) []models.Task {
	tasks := make([]models.Task, 0, len(results))
	for _, result := range results {
		tasks = append(tasks, result.Task)
	}
	return tasks
}

// activateDeferredTasks activates only tasks whose exact originating request was deferred
// and whose complete attachment batch is ready. It returns the IDs of tasks whose
// activation succeeded.
func (h *Handler) activateDeferredTasks(ctx context.Context, createdResults []service.TaskCreationResult, deferredRequestIndexes map[int]bool, attachmentReadyByTask map[string]bool) map[string]bool {
	activatedTaskIDs := make(map[string]bool, len(deferredRequestIndexes))
	for _, result := range createdResults {
		if deferredRequestIndexes[result.RequestIndex] && attachmentReadyByTask[result.Task.ID] {
			task := result.Task
			applog.Infof("[handler] activateDeferredTasks activating request=%d task=%s %q", result.RequestIndex, task.ID, task.Title)
			if err := h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				applog.Infof("[handler] activateDeferredTasks error activating request=%d task=%s: %v", result.RequestIndex, task.ID, err)
				continue
			}
			activatedTaskIDs[task.ID] = true
		}
	}
	return activatedTaskIDs
}

func summarizeActivatedTasks(summary string, createdResults []service.TaskCreationResult, activatedTaskIDs map[string]bool) string {
	for _, result := range createdResults {
		if !activatedTaskIDs[result.Task.ID] {
			continue
		}
		backlogLine := fmt.Sprintf("- \"%s\" (%s) [TASK_ID:%s]", result.Task.Title, models.CategoryBacklog, result.Task.ID)
		activeLine := fmt.Sprintf("- \"%s\" (%s) [TASK_ID:%s]", result.Task.Title, models.CategoryActive, result.Task.ID)
		summary = strings.Replace(summary, backlogLine, activeLine, 1)
	}
	return summary
}

// appendCreationSummary appends task creation summary and attachment info to output.
func (h *Handler) appendCreationSummary(output, summary string, totalAttachmentsCopied int, chatAtts []models.ChatAttachment) string {
	if summary == "" {
		return output
	}

	output += summary
	if totalAttachmentsCopied > 0 {
		attachmentFileNames := make([]string, 0, len(chatAtts))
		for _, att := range chatAtts {
			attachmentFileNames = append(attachmentFileNames, att.FileName)
		}

		if len(attachmentFileNames) > 0 {
			output += fmt.Sprintf("\n\nAttachments copied to tasks: %s", strings.Join(attachmentFileNames, ", "))
		} else {
			output += fmt.Sprintf("\n(%d attachment(s) copied to created tasks)", totalAttachmentsCopied)
		}
	}
	return output
}

// executeChatTaskEditRequests applies typed runtime-tool task edits, including
// copying current Chat attachments when requested.
func (h *Handler) executeChatTaskEditRequests(ctx context.Context, execID, projectID string, editRequests []service.TaskEditRequest) string {
	if len(editRequests) == 0 {
		return ""
	}

	applog.Infof("[handler] executeChatTaskEditRequests exec=%s found %d task edit requests", execID, len(editRequests))
	output := ""

	// Handle "chat" attachment keyword: copy chat attachments to target tasks
	chatAttCopied, chatOnlyTaskIDs := h.processChatAttachmentsForEdits(ctx, execID, editRequests)

	// Filter out edit requests that only had "chat" attachments and no other changes
	var remainingEdits []service.TaskEditRequest
	for _, req := range editRequests {
		if chatOnlyTaskIDs[req.ID] && !hasOtherEditFields(req) {
			continue // Skip: only change was chat attachment copy, already handled
		}
		remainingEdits = append(remainingEdits, req)
	}

	editSummary := executeChatTaskEdits(ctx, remainingEdits, projectID, h.taskSvc, h.attachmentRepo, uploadsDir)
	if editSummary != "" {
		output += editSummary
	}

	// Append chat attachment copy info if any were copied
	if chatAttCopied > 0 {
		output += fmt.Sprintf("\n(%d chat attachment(s) copied to task)", chatAttCopied)
	}

	return output
}

// processChatAttachmentsForEdits handles the special "chat" attachment value in
// typed edit_task requests by copying the current execution's attachments.
// Returns the total number of chat attachments copied and a set of task IDs that had the "chat" keyword.
func (h *Handler) processChatAttachmentsForEdits(ctx context.Context, execID string, editRequests []service.TaskEditRequest) (int, map[string]bool) {
	totalCopied := 0
	chatOnlyTaskIDs := make(map[string]bool)

	for i := range editRequests {
		hasChatKeyword := false
		var filteredAttachments []string
		for _, att := range editRequests[i].Attachments {
			if att == "chat" {
				hasChatKeyword = true
			} else {
				filteredAttachments = append(filteredAttachments, att)
			}
		}

		if !hasChatKeyword {
			continue
		}

		// Replace attachments list with only non-"chat" entries
		editRequests[i].Attachments = filteredAttachments
		chatOnlyTaskIDs[editRequests[i].ID] = true

		taskID := editRequests[i].ID
		if taskID == "" {
			continue
		}

		copiedCount, err := h.copyChatAttachmentsToTask(ctx, execID, taskID)
		if err != nil {
			applog.Infof("[handler] processChatAttachmentsForEdits error copying chat attachments to task %s: %v", taskID, err)
		} else if copiedCount > 0 {
			totalCopied += copiedCount
			applog.Infof("[handler] processChatAttachmentsForEdits copied %d chat attachments to task %s", copiedCount, taskID)
		} else {
			applog.Infof("[handler] processChatAttachmentsForEdits no chat attachments to copy for exec=%s", execID)
		}
	}
	return totalCopied, chatOnlyTaskIDs
}

// hasOtherEditFields returns true if a TaskEditRequest has fields beyond just attachments.
func hasOtherEditFields(req service.TaskEditRequest) bool {
	return req.Title != "" || req.Prompt != "" || req.Category != "" ||
		req.Priority > 0 || req.Tag != "" || req.AgentID != "" ||
		req.AgentConfigID != "" || req.Chain != nil || len(req.Attachments) > 0
}

// executeChatTaskExecutionRequests executes tasks from typed runtime-tool requests.
func (h *Handler) executeChatTaskExecutionRequests(ctx context.Context, execID, projectID string, execRequests []service.TaskExecutionRequest) string {
	if len(execRequests) == 0 {
		return ""
	}

	applog.Infof("[handler] executeChatTaskExecutionRequests exec=%s found %d task execution requests", execID, len(execRequests))
	return executeChatTaskExecutions(ctx, execRequests, projectID, h.taskSvc)
}

// executeViewTaskThreadRequest resolves a typed runtime-tool task reference and
// returns its execution history. A task_id of "current" resolves to the
// persisted task backing this task-thread follow-up, matching the resolution
// used by the goal and send_to_task runtime tools.
func (h *Handler) executeViewTaskThreadRequest(ctx context.Context, params streamingResponseParams, req service.ViewThreadRequest) (string, error) {
	if strings.TrimSpace(req.TaskID) == "" && strings.TrimSpace(req.Title) == "" {
		return "", fmt.Errorf("view_task_thread requires task_id or title")
	}
	taskID, err := h.resolveTaskIDForTool(ctx, params, req.TaskID, req.Title)
	if err != nil {
		return "", err
	}
	task, err := h.resolveTaskReference(ctx, params.ProjectID, taskID, "")
	if err != nil {
		return "", err
	}
	executions, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		return "", fmt.Errorf("retrieving thread for task %q: %w", task.Title, err)
	}
	return strings.TrimSpace(h.formatThreadTranscript(task, executions, req.Offset, req.Limit)), nil
}

// executeChatScheduleRequests schedules tasks from typed runtime-tool requests.
func (h *Handler) executeChatScheduleRequests(ctx context.Context, projectID string, requests []service.ScheduleTaskRequest) string {
	if len(requests) == 0 {
		return ""
	}
	actions := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc)
	var results []string
	for _, req := range requests {
		result, err := actions.Create(ctx, projectID, req)
		if err != nil {
			var actionErr *service.ScheduleActionError
			errors.As(err, &actionErr)
			switch {
			case actionErr != nil && actionErr.Kind == service.ScheduleActionReferenceError:
				applog.Infof("[handler] executeChatScheduleRequests error resolving task: %v", err)
				results = append(results, fmt.Sprintf("- Could not find task: %v", err))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionTimeError:
				applog.Infof("[handler] executeChatScheduleRequests invalid time: %s", req.Time)
				results = append(results, fmt.Sprintf("- Invalid time %q for task \"%s\" (expected HH:MM, 00:00-23:59)", req.Time, result.Task.Title))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionRepeatError:
				results = append(results, fmt.Sprintf("- Unknown repeat type %q for task \"%s\"", req.Repeat, result.Task.Title))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionDaysError:
				results = append(results, fmt.Sprintf("- Invalid weekly days for task \"%s\": %v", result.Task.Title, err))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionIntervalError:
				results = append(results, fmt.Sprintf("- Invalid interval %d for task \"%s\" (%v)", req.Interval, result.Task.Title, err))
			default:
				title := req.Title
				if result != nil && result.Task != nil {
					title = result.Task.Title
				}
				applog.Infof("[handler] executeChatScheduleRequests error creating schedule: %v", err)
				results = append(results, fmt.Sprintf("- Error scheduling task \"%s\": %v", title, err))
			}
			continue
		}
		for _, warning := range result.Warnings {
			applog.Infof("[handler] executeChatScheduleRequests task transition warning: %v", warning)
		}
		repeatDesc := service.FormatRepeatPattern(result.Schedule.RepeatType, result.Schedule.RepeatInterval)
		if result.Schedule.RepeatType == models.RepeatWeekly && len(req.Days) > 0 {
			repeatDesc = fmt.Sprintf("weekly on %s", strings.Join(req.Days, ", "))
			if result.Schedule.RepeatInterval > 1 {
				repeatDesc = fmt.Sprintf("every %d weeks on %s", result.Schedule.RepeatInterval, strings.Join(req.Days, ", "))
			}
		}
		results = append(results, fmt.Sprintf("- Scheduled task \"%s\" [TASK_ID:%s] at %s (%s)", result.Task.Title, result.Task.ID, req.Time, repeatDesc))
		applog.Infof("[handler] executeChatScheduleRequests scheduled task=%s schedule=%s at %s repeat=%s", result.Task.ID, result.Schedule.ID, req.Time, result.Schedule.RepeatType)
	}
	return "Schedule Results:\n" + strings.Join(results, "\n")
}

// executeChatDeleteScheduleRequests deletes schedules from typed runtime-tool requests.
func (h *Handler) executeChatDeleteScheduleRequests(ctx context.Context, projectID string, requests []service.DeleteScheduleRequest) string {
	if len(requests) == 0 {
		return ""
	}
	actions := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc)
	var results []string
	for _, req := range requests {
		result, err := actions.Delete(ctx, projectID, req)
		if err != nil {
			var actionErr *service.ScheduleActionError
			errors.As(err, &actionErr)
			if actionErr != nil && actionErr.Kind == service.ScheduleActionReferenceError {
				applog.Infof("[handler] executeChatDeleteScheduleRequests error resolving schedule: %v", err)
				results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			} else {
				title := req.Title
				if result != nil && result.Task != nil {
					title = result.Task.Title
				}
				applog.Infof("[handler] executeChatDeleteScheduleRequests error deleting schedule: %v", err)
				results = append(results, fmt.Sprintf("- Error deleting schedule for task \"%s\": %v", title, err))
			}
			continue
		}
		for _, warning := range result.Warnings {
			applog.Infof("[handler] executeChatDeleteScheduleRequests transition warning: %v", warning)
		}
		results = append(results, fmt.Sprintf("- Deleted schedule for task \"%s\" [TASK_ID:%s]", result.Task.Title, result.Task.ID))
		applog.Infof("[handler] executeChatDeleteScheduleRequests deleted schedule=%s task=%s", result.Schedule.ID, result.Task.ID)
	}
	return "Schedule Delete Results:\n" + strings.Join(results, "\n")
}

// executeChatModifyScheduleRequests modifies schedules from typed runtime-tool requests.
func (h *Handler) executeChatModifyScheduleRequests(ctx context.Context, projectID string, requests []service.ModifyScheduleRequest) string {
	if len(requests) == 0 {
		return ""
	}
	actions := service.NewScheduleActionService(h.taskRepo, h.scheduleRepo, h.workerSvc)
	var results []string
	for _, req := range requests {
		result, err := actions.Modify(ctx, projectID, req)
		if err != nil {
			var actionErr *service.ScheduleActionError
			errors.As(err, &actionErr)
			title := req.Title
			if result != nil && result.Task != nil {
				title = result.Task.Title
			}
			switch {
			case actionErr != nil && actionErr.Kind == service.ScheduleActionReferenceError:
				applog.Infof("[handler] executeChatModifyScheduleRequests error resolving schedule: %v", err)
				results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionTimeError:
				applog.Infof("[handler] executeChatModifyScheduleRequests invalid time: %s", req.Time)
				results = append(results, fmt.Sprintf("- Invalid time %q for schedule on task \"%s\" (expected HH:MM, 00:00-23:59)", req.Time, title))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionRepeatError:
				applog.Infof("[handler] executeChatModifyScheduleRequests unknown repeat type %q", req.Repeat)
				results = append(results, fmt.Sprintf("- Unknown repeat type %q for schedule on task \"%s\"", req.Repeat, title))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionDaysError:
				results = append(results, fmt.Sprintf("- Invalid weekly days for schedule on task \"%s\": %v", title, err))
			case actionErr != nil && actionErr.Kind == service.ScheduleActionIntervalError:
				results = append(results, fmt.Sprintf("- Invalid interval %d for schedule on task \"%s\" (%v)", *req.Interval, title, err))
			default:
				applog.Infof("[handler] executeChatModifyScheduleRequests error updating schedule: %v", err)
				results = append(results, fmt.Sprintf("- Error updating schedule for task \"%s\": %v", title, err))
			}
			continue
		}
		if len(result.Changes) == 0 {
			results = append(results, fmt.Sprintf("- No changes specified for schedule on task \"%s\"", result.Task.Title))
			continue
		}
		results = append(results, fmt.Sprintf("- Updated schedule for task \"%s\" [TASK_ID:%s]: %s", result.Task.Title, result.Task.ID, strings.Join(result.Changes, ", ")))
		applog.Infof("[handler] executeChatModifyScheduleRequests updated schedule=%s task=%s changes=%s", result.Schedule.ID, result.Task.ID, strings.Join(result.Changes, ", "))
	}
	return "Schedule Modify Results:\n" + strings.Join(results, "\n")
}

// resolveTaskReference finds a task by ID or title within the current project.
func (h *Handler) resolveTaskReference(ctx context.Context, projectID, taskID, title string) (*models.Task, error) {
	if taskID = strings.TrimSpace(taskID); taskID != "" {
		task, err := h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("error looking up task %s: %w", taskID, err)
		}
		if task == nil {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		if task.ProjectID != projectID {
			return nil, fmt.Errorf("task %s belongs to a different project", taskID)
		}
		return task, nil
	}
	if title = strings.TrimSpace(title); title != "" {
		tasks, err := h.taskRepo.SearchByTitle(ctx, projectID, title)
		if err != nil {
			return nil, fmt.Errorf("error searching for task %q: %w", title, err)
		}
		if len(tasks) == 0 {
			return nil, fmt.Errorf("no task found matching %q", title)
		}
		return &tasks[0], nil
	}
	return nil, fmt.Errorf("no task_id or title provided")
}

// maxThreadTranscriptBytes is the total size budget for a thread transcript (80KB).
// If the full transcript exceeds this, only the most recent executions that fit are included.
const maxThreadTranscriptBytes = 80 * 1024

// maxPerMessageBytes is a safety limit for a single message within the transcript (50KB).
const maxPerMessageBytes = 50 * 1024

// formatThreadTranscript formats a task's execution history as a readable thread transcript.
// offset/limit control pagination: offset is the execution index to start from (0-based),
// limit is the max number of executions to include (0 = all).
func (h *Handler) formatThreadTranscript(task *models.Task, executions []models.Execution, offset, limit int) string {
	total := len(executions)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n\n---\n**Thread history for task: \"%s\"** [TASK_ID:%s]\n", task.Title, task.ID))
	sb.WriteString(fmt.Sprintf("Status: %s | Category: %s | Priority: %d\n", task.Status, task.Category, task.Priority))
	sb.WriteString(fmt.Sprintf("Total executions: %d\n\n", total))

	if total == 0 {
		sb.WriteString("No execution history found for this task.\n")
		return sb.String()
	}

	// Apply offset
	if offset > 0 {
		if offset >= total {
			sb.WriteString(fmt.Sprintf("Offset %d exceeds total executions (%d). Use a lower offset.\n", offset, total))
			return sb.String()
		}
		executions = executions[offset:]
	}

	// Apply limit
	if limit > 0 && limit < len(executions) {
		executions = executions[:limit]
	}

	// Format each execution, tracking total size
	budgetExceeded := false
	included := 0
	for i, exec := range executions {
		execIdx := offset + i
		timestamp := exec.StartedAt.Local().Format("2006-01-02 15:04:05")

		var entry strings.Builder

		// User message
		prompt := exec.PromptSent
		if !exec.IsFollowup && execIdx == 0 {
			prompt = task.Prompt
		}
		if prompt != "" {
			prompt = util.TruncateWithSuffix(prompt, maxPerMessageBytes, "\n... (message truncated at 50KB)")
			entry.WriteString(fmt.Sprintf("**[%s] User:**\n%s\n\n", timestamp, prompt))
		}

		// Assistant response (strip thinking blocks so thread context stays clean)
		if exec.Output != "" {
			cleaned := llmoutput.CleanChatOutput(exec.Output)
			cleaned = util.TruncateWithSuffix(cleaned, maxPerMessageBytes, "\n... (message truncated at 50KB)")
			entry.WriteString(fmt.Sprintf("**[%s] Assistant** (status: %s):\n%s\n\n", timestamp, exec.Status, cleaned))
		}

		// Error message
		if exec.ErrorMessage != "" {
			entry.WriteString(fmt.Sprintf("**Error:** %s\n\n", exec.ErrorMessage))
		}

		// Check total budget before appending
		if sb.Len()+entry.Len() > maxThreadTranscriptBytes {
			budgetExceeded = true
			break
		}

		sb.WriteString(entry.String())
		included++
	}

	remaining := total - offset - included
	if budgetExceeded && remaining > 0 {
		sb.WriteString(fmt.Sprintf("\n---\n⚠️ Transcript size limit reached. Showing executions %d–%d of %d. Use `offset: %d` to fetch the next page.\n",
			offset+1, offset+included, total, offset+included))
	} else if offset > 0 {
		sb.WriteString(fmt.Sprintf("\n---\nShowing executions %d–%d of %d.\n", offset+1, offset+included, total))
	}

	return sb.String()
}

// executeListPersonalities returns all available personality presets.
func (h *Handler) executeListPersonalities(ctx context.Context) string {
	personalities := service.AllPersonalitiesWithCustom(ctx, h.customPersonalityRepo)
	var sb strings.Builder
	sb.WriteString("\n\n---\nAvailable Personalities:\n")
	for _, p := range personalities {
		if p.Key == "" {
			sb.WriteString(fmt.Sprintf("- **%s** (default) — %s\n", p.Name, p.Description))
		} else if p.IsCustom {
			sb.WriteString(fmt.Sprintf("- **%s** (key: `%s`, custom) — %s\n", p.Name, p.Key, p.Description))
		} else {
			sb.WriteString(fmt.Sprintf("- **%s** (key: `%s`) — %s\n", p.Name, p.Key, p.Description))
		}
	}

	// Also show current personality
	current, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		applog.Infof("[handler] executeListPersonalities error reading current personality: %v", err)
	}
	if current == "" {
		current = "default"
	}
	sb.WriteString(fmt.Sprintf("\nCurrent personality: **%s**\n", current))
	return sb.String()
}

// executeSetPersonality applies a typed set_personality runtime action.
func (h *Handler) executeSetPersonality(ctx context.Context, req service.SetPersonalityRequest) string {
	// Validate personality key against presets + custom
	valid := false
	var matchedName string
	for _, personality := range service.AllPersonalitiesWithCustom(ctx, h.customPersonalityRepo) {
		if personality.Key == req.Personality {
			valid = true
			matchedName = personality.Name
			break
		}
	}
	if !valid {
		return fmt.Sprintf("Unknown personality %q. Use list_personalities to see available options.", req.Personality)
	}
	if err := h.settingsRepo.Set(ctx, "personality", req.Personality); err != nil {
		return fmt.Sprintf("Error setting personality to %q: %v", req.Personality, err)
	}
	return fmt.Sprintf("Personality changed to **%s** (`%s`)", matchedName, req.Personality)
}

// executeListModels returns available model configurations.
func (h *Handler) executeListModels(ctx context.Context) string {
	configs, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		return "Model Settings:\n- Error retrieving model configurations: " + err.Error()
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nConfigured Models:\n")
	if len(configs) == 0 {
		sb.WriteString("No models configured.\n")
	} else {
		for _, c := range configs {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			authStr := string(c.AuthMethod)
			if authStr == "" {
				authStr = "cli"
			}
			workerInfo := ""
			if c.MaxWorkers > 0 {
				workerInfo = fmt.Sprintf(" | max_workers: %d", c.MaxWorkers)
			}
			sb.WriteString(fmt.Sprintf("- **%s**%s — provider: %s, model: %s, auth: %s%s\n",
				c.Name, defaultStr, c.Provider, c.Model, authStr, workerInfo))
		}
	}

	return sb.String()
}

// executeListAgents returns available agent definitions.
func (h *Handler) executeListAgents(ctx context.Context) string {
	if h.agentRepo == nil {
		return "Configured Agents:\nAgent definitions not available.\n"
	}

	agents, err := h.agentRepo.ListRuntimeSummaries(ctx)
	if err != nil {
		return "Configured Agents:\n- Error: " + err.Error()
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nConfigured Agents:\n")
	if len(agents) == 0 {
		sb.WriteString("No agents configured.\n")
	} else {
		for _, a := range agents {
			modelStr := ""
			if a.Model != "inherit" {
				modelStr = fmt.Sprintf(", model: %s", a.Model)
			}
			sb.WriteString(fmt.Sprintf("- **%s** — %s%s, %d skills, %d MCP servers\n",
				a.Name, a.Description, modelStr, a.SkillCount, a.MCPServerCount))
		}
	}
	return sb.String()
}

// executeViewSettings returns current application settings.
func (h *Handler) executeViewSettings(ctx context.Context) string {
	var sb strings.Builder
	sb.WriteString("\n\n---\nApp Settings:\n")

	// Personality
	personality, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		applog.Infof("[handler] executeViewSettings error reading personality: %v", err)
	}
	if personality == "" {
		personality = "default (no personality)"
	}
	sb.WriteString(fmt.Sprintf("- **Personality:** %s\n", personality))

	// Model count
	configs, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		applog.Infof("[handler] executeViewSettings error listing models: %v", err)
	} else {
		sb.WriteString(fmt.Sprintf("- **Configured models:** %d\n", len(configs)))
		for _, c := range configs {
			defaultStr := ""
			if c.IsDefault {
				defaultStr = " (default)"
			}
			sb.WriteString(fmt.Sprintf("  - %s%s — %s/%s\n", c.Name, defaultStr, c.Provider, c.Model))
		}
	}

	// Global worker settings (from worker_settings table)
	if h.workerRepo != nil {
		globalMax, err := h.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			applog.Infof("[handler] executeViewSettings error reading global workers: %v", err)
			sb.WriteString("- **Global max workers:** error reading\n")
		} else if globalMax == 0 {
			sb.WriteString("- **Global max workers:** unlimited\n")
		} else {
			sb.WriteString(fmt.Sprintf("- **Global max workers:** %d\n", globalMax))
		}
	} else {
		sb.WriteString("- **Global max workers:** not configured\n")
	}

	// Per-project worker limits
	projects, err := h.projectRepo.List(ctx)
	if err != nil {
		applog.Infof("[handler] executeViewSettings error listing projects: %v", err)
	} else {
		hasProjectLimits := false
		for _, p := range projects {
			if p.MaxWorkers != nil {
				if !hasProjectLimits {
					sb.WriteString("- **Per-project worker limits:**\n")
					hasProjectLimits = true
				}
				sb.WriteString(fmt.Sprintf("  - %s: %d\n", p.Name, *p.MaxWorkers))
			}
		}
		if !hasProjectLimits {
			sb.WriteString("- **Per-project worker limits:** none configured\n")
		}
	}

	// Per-model worker pools
	if configs != nil {
		hasModelPools := false
		for _, c := range configs {
			if c.MaxWorkers > 0 {
				if !hasModelPools {
					sb.WriteString("- **Per-model worker pools:**\n")
					hasModelPools = true
				}
				if c.WorkerTimeout > 0 {
					sb.WriteString(fmt.Sprintf("  - %s: max_workers=%d, timeout=%ds\n", c.Name, c.MaxWorkers, c.WorkerTimeout))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s: max_workers=%d\n", c.Name, c.MaxWorkers))
				}
			}
		}
		if !hasModelPools {
			sb.WriteString("- **Per-model worker pools:** none configured\n")
		}
	}

	return sb.String()
}

// executeProjectInfo returns current project details and task counts.
func (h *Handler) executeProjectInfo(ctx context.Context, projectID string) string {
	var sb strings.Builder
	sb.WriteString("\n\n---\nProject Info:\n")

	// Get project details
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		applog.Infof("[handler] executeProjectInfo error getting project: %v", err)
		sb.WriteString("- Error retrieving project details\n")
		return sb.String()
	}

	sb.WriteString(fmt.Sprintf("- **Name:** %s\n", project.Name))
	if project.Description != "" {
		sb.WriteString(fmt.Sprintf("- **Description:** %s\n", project.Description))
	}
	if project.RepoPath != "" {
		sb.WriteString(fmt.Sprintf("- **Repository:** %s\n", project.RepoPath))
	}

	// Task counts by category
	categoryCounts, err := h.taskRepo.CountByProjectAndCategory(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] executeProjectInfo error counting tasks: %v", err)
	} else {
		total := 0
		for _, count := range categoryCounts {
			total += count
		}
		sb.WriteString(fmt.Sprintf("- **Total tasks:** %d\n", total))
		sb.WriteString("- **Tasks by category:**\n")
		for category, count := range categoryCounts {
			sb.WriteString(fmt.Sprintf("  - %s: %d\n", category, count))
		}
	}

	return sb.String()
}

// executeListProjects returns all available projects.
func (h *Handler) executeListProjects(ctx context.Context, projectID string) string {
	projects, err := h.projectRepo.List(ctx)
	if err != nil {
		return "Available Projects:\n- Error retrieving projects: " + err.Error()
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nAvailable Projects:\n")
	if len(projects) == 0 {
		sb.WriteString("No projects found.\n")
	} else {
		for _, p := range projects {
			marker := ""
			if p.ID == projectID {
				marker = " ← _current_"
			}
			desc := ""
			if p.Description != "" {
				desc = fmt.Sprintf(" — %s", p.Description)
			}
			sb.WriteString(fmt.Sprintf("- **%s**%s%s\n", p.Name, desc, marker))
		}
	}

	return sb.String()
}

// executeListAlerts returns alerts for the current project.
func (h *Handler) executeListAlerts(ctx context.Context, projectID string) string {
	if h.alertSvc == nil {
		return "Alert Results:\n- Alert service not available"
	}

	alerts, err := h.alertSvc.ListSummariesByProject(ctx, projectID, 50)
	if err != nil {
		return "Alert Results:\n- Error retrieving alerts: " + err.Error()
	}

	var sb strings.Builder
	sb.WriteString("\n\n---\nAlert Results:\n")
	if len(alerts) == 0 {
		sb.WriteString("No alerts found. You're all clear!\n")
	} else {
		unreadCount, _ := h.alertSvc.CountUnread(ctx, projectID)
		sb.WriteString(fmt.Sprintf("Found %d alerts (%d unread):\n", len(alerts), unreadCount))
		for _, a := range alerts {
			readStr := "unread"
			if a.IsRead {
				readStr = "read"
			}
			taskStr := ""
			if a.TaskID != nil {
				taskStr = fmt.Sprintf(" | task: %s", *a.TaskID)
			}
			sb.WriteString(fmt.Sprintf("- **%s** (id: `%s`, type: %s, severity: %s, %s%s) — %s\n",
				a.Title, a.ID, a.Type, a.Severity, readStr, taskStr, a.CreatedAt.Format("Jan 2, 2006 3:04 PM")))
			if a.Message != "" {
				sb.WriteString(fmt.Sprintf("  Message: %s\n", a.Message))
			}
		}
	}

	return sb.String()
}

// executeCreateAlertRequests creates alerts from typed runtime-tool requests.
func (h *Handler) executeCreateAlertRequests(ctx context.Context, projectID string, requests []service.CreateAlertRequest) string {
	if len(requests) == 0 {
		return ""
	}
	if h.alertSvc == nil {
		return "Alert Create Results:\n- Alert service not available"
	}

	var results []string
	for _, req := range requests {
		// Default severity to info
		severity := models.SeverityInfo
		switch req.Severity {
		case "warning":
			severity = models.SeverityWarning
		case "error":
			severity = models.SeverityError
		case "info", "":
			severity = models.SeverityInfo
		default:
			results = append(results, fmt.Sprintf("- Invalid severity %q (use info, warning, or error)", req.Severity))
			continue
		}

		// Default type to custom
		alertType := models.AlertCustom
		switch req.Type {
		case "task_failed":
			alertType = models.AlertTaskFailed
		case "task_needs_followup":
			alertType = models.AlertTaskNeedsFollowup
		case "custom", "":
			alertType = models.AlertCustom
		default:
			results = append(results, fmt.Sprintf("- Invalid alert type %q (use custom, task_failed, or task_needs_followup)", req.Type))
			continue
		}

		a := &models.Alert{
			ProjectID: projectID,
			Type:      alertType,
			Severity:  severity,
			Title:     req.Title,
			Message:   req.Message,
		}
		if req.TaskID != "" {
			a.TaskID = &req.TaskID
		}

		if err := h.alertSvc.Create(ctx, a); err != nil {
			applog.Infof("[handler] executeCreateAlertRequests error: %v", err)
			results = append(results, fmt.Sprintf("- Error creating alert %q: %v", req.Title, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Created alert %q (id: `%s`, severity: %s)", req.Title, a.ID, severity))
	}

	if len(results) == 0 {
		return ""
	}
	return "Alert Create Results:\n" + strings.Join(results, "\n")
}

// executeDeleteAlertRequests deletes alerts from typed runtime-tool requests.
func (h *Handler) executeDeleteAlertRequests(ctx context.Context, projectID string, requests []service.DeleteAlertRequest) string {
	if len(requests) == 0 {
		return ""
	}
	if h.alertSvc == nil {
		return "Alert Delete Results:\n- Alert service not available"
	}

	var results []string
	for _, req := range requests {
		if err := h.alertSvc.Delete(ctx, projectID, req.AlertID); err != nil {
			applog.Infof("[handler] executeDeleteAlertRequests error: %v", err)
			results = append(results, fmt.Sprintf("- Error deleting alert %q: %v", req.AlertID, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Deleted alert `%s`", req.AlertID))
	}

	if len(results) == 0 {
		return ""
	}
	return "Alert Delete Results:\n" + strings.Join(results, "\n")
}

// executeToggleAlertRequests marks alerts read from typed runtime-tool requests.
func (h *Handler) executeToggleAlertRequests(ctx context.Context, projectID string, requests []service.ToggleAlertRequest) string {
	if len(requests) == 0 {
		return ""
	}
	if h.alertSvc == nil {
		return "Alert Toggle Results:\n- Alert service not available"
	}

	var results []string
	for _, req := range requests {
		if err := h.alertSvc.MarkRead(ctx, projectID, req.AlertID); err != nil {
			applog.Infof("[handler] executeToggleAlertRequests error: %v", err)
			results = append(results, fmt.Sprintf("- Error marking alert %q as read: %v", req.AlertID, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Marked alert `%s` as read", req.AlertID))
	}

	if len(results) == 0 {
		return ""
	}
	return "Alert Toggle Results:\n" + strings.Join(results, "\n")
}

// buildChatContext builds the context string for chat prompts, including task, model, and schedule information.
// Returns a formatted string with current tasks (excluding chat tasks), available models, and schedule details.
// Delegates to the shared service.BuildChatContext so /chat and Telegram produce identical context.
func (h *Handler) buildChatContext(ctx context.Context, projectID string, availableModels []models.LLMConfig) string {
	existingTasks, err := h.taskSvc.ListByProject(ctx, projectID, "")
	if err != nil {
		applog.Infof("[handler] buildChatContext error listing tasks for project %s: %v", projectID, err)
		existingTasks = []models.Task{}
	}

	schedules, err := h.scheduleRepo.ListByProject(ctx, projectID)
	if err != nil {
		applog.Infof("[handler] buildChatContext error listing schedules for project %s: %v", projectID, err)
		schedules = []models.Schedule{}
	}

	agentDefinitions := h.listChatAssignableAgentDefinitions(ctx)
	return service.BuildChatContextWithAgentDefinitions(existingTasks, availableModels, agentDefinitions, schedules, time.Now())
}

func (h *Handler) listChatAssignableAgentDefinitions(ctx context.Context) []models.Agent {
	if h.agentRepo == nil {
		return nil
	}
	agents, err := h.agentRepo.List(ctx)
	if err != nil {
		applog.Infof("[handler] buildChatContext error listing agent definitions: %v", err)
		return nil
	}
	return service.UniqueChatAssignableAgentDefinitions(agents)
}

// buildThreadSystemContext builds the system context string for task thread follow-ups.
// When there is prior conversation history (hasHistory=true), the original task prompt
// is already present as the first user message in the history, so we do NOT re-inject it.
// Re-injecting it causes the model to restart work from scratch instead of continuing.
func buildThreadSystemContext(taskTitle string, hasHistory bool, attachmentContext string) string {
	var systemContext string
	if hasHistory {
		systemContext = fmt.Sprintf("You are continuing work on a task titled %q. The conversation history shows the original task prompt and all prior work done on this task. The user's new message is a follow-up instruction — continue from where you left off, do NOT restart the original task from scratch.", taskTitle)
	} else {
		systemContext = "You are starting work on a task. The task prompt is provided as the user's message below."
	}
	if attachmentContext != "" {
		systemContext += "\n\n" + attachmentContext
	}
	return systemContext
}

// combineContexts merges task context and attachment context into a single context
// string for LLM prompts.
//
// The function handles empty inputs gracefully:
//   - If both are empty, returns empty string
//   - If only one is present, returns that one
//   - If both are present, joins them with a single newline
//
// This standardized context combining ensures consistent formatting across chat
// and task follow-up scenarios.
func buildStartupSyncConflictContext(conflict *service.StartupSyncConflictError) string {
	if conflict == nil {
		return ""
	}
	return fmt.Sprintf("# Worktree Sync Warning\n\nStartup sync could not merge %s into %s because Git reported conflicts in: %s. The merge was aborted before this turn started, so the preserved worktree is clean but may be behind or diverged from %s. Before handling the follow-up, run the merge in %s, resolve the conflicts while preserving both the task changes and current target changes, then build, test, and commit the resolution. Sync error: %v", conflict.TargetBranch, conflict.TaskBranch, strings.Join(conflict.ConflictFiles, ", "), conflict.TargetBranch, conflict.WorktreePath, conflict)
}

func combineContexts(taskContext, attachmentContext string) string {
	fullContext := taskContext
	if attachmentContext != "" {
		if fullContext != "" {
			fullContext += "\n"
		}
		fullContext += attachmentContext
	}
	return fullContext
}

// getPersonalityContext loads the global personality setting and returns the
// corresponding system prompt modifier. Returns empty string if no personality is set.
func (h *Handler) getPersonalityContext(ctx context.Context, projectID string) string {
	if h.settingsRepo == nil {
		return ""
	}
	personality, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil || personality == "" {
		return ""
	}
	prompt := service.GetPersonalityPromptWithCustom(ctx, personality, h.customPersonalityRepo)
	if prompt == "" {
		return ""
	}
	return "\n# Communication Style\n\n" + prompt
}

// extractTaskIDsFromOutput reads [TASK_ID:xxx] result metadata emitted by
// successful runtime task-creation actions. It identifies which tasks were created
// during a chat execution without querying the database.
//
// The format is: [TASK_ID:abc123]
// Returns task IDs in the order they appear in the output.
// Uses a pre-compiled regex pattern for performance.
func extractTaskIDsFromOutput(output string) []string {
	matches := taskIDRegex.FindAllStringSubmatch(output, -1)

	var taskIDs []string
	for _, match := range matches {
		if len(match) > 1 {
			taskIDs = append(taskIDs, match[1])
		}
	}
	return taskIDs
}

// hasPendingImages checks if there are any image files in the pending uploads directory
// for a given attachment session ID. Returns true if at least one image file exists.
func hasPendingImages(sessionID string) bool {
	if sessionID == "" {
		return false
	}

	pendingDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	entries, err := os.ReadDir(pendingDir)
	if err != nil {
		return false // Directory doesn't exist or can't be read
	}

	for _, entry := range entries {
		if !entry.IsDir() && isImageFile(entry.Name()) {
			return true
		}
	}
	return false
}

func (h *Handler) taskHasStartingFirstTurn(ctx context.Context, task *models.Task) (bool, error) {
	if task == nil || task.ID == "" || h.execRepo == nil {
		return false, nil
	}
	if task.Category != models.CategoryActive || (task.Status != models.StatusPending && task.Status != models.StatusQueued && task.Status != models.StatusRunning) {
		return false, nil
	}
	execs, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
	if err != nil {
		return false, err
	}
	return len(execs) == 0, nil
}

func (h *Handler) bindQueuedTaskInputToActiveExecutionIfAvailable(ctx context.Context, input *models.ThreadInput) error {
	if input == nil || input.RunExecutionID != "" || h.execRepo == nil || h.threadInputRepo == nil {
		return nil
	}
	active, err := h.execRepo.FindActiveTaskExecution(ctx, input.TaskID, "")
	if err != nil || active == nil {
		return err
	}
	if err := h.threadInputRepo.BindPreExecutionQueuedTaskInputs(ctx, input.TaskID, active.ID); err != nil {
		return err
	}
	input.RunExecutionID = active.ID
	return nil
}

func (h *Handler) shouldPromotePreExecutionQueuedInput(ctx context.Context, task *models.Task, input *models.ThreadInput) (bool, error) {
	if task == nil || input == nil || input.RunExecutionID != "" {
		return false, nil
	}
	starting, err := h.taskHasStartingFirstTurn(ctx, task)
	if err != nil || starting {
		return false, err
	}
	return true, nil
}

func (h *Handler) enqueueTaskThreadInput(ctx context.Context, taskID, message, origin, originAgent string, channelReply ...service.ChannelReplyContext) (*models.ThreadInput, error) {
	if h.threadInputRepo == nil {
		return nil, fmt.Errorf("thread input queue is unavailable")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	agentID := ""
	if task.AgentID != nil {
		agentID = *task.AgentID
	} else if h.llmConfigRepo != nil {
		if agent, err := h.selectDefaultAgent(ctx, false); err == nil && agent != nil {
			agentID = agent.ID
		}
	}
	if strings.TrimSpace(origin) == "" {
		origin = models.TaskOriginWeb
	}
	activeExecutionID := ""
	if h.execRepo != nil {
		if active, activeErr := h.execRepo.FindActiveTaskExecution(ctx, task.ID, ""); activeErr != nil {
			return nil, activeErr
		} else if active != nil {
			activeExecutionID = active.ID
		}
	}
	queueBehindFirstTurn, err := h.taskHasStartingFirstTurn(ctx, task)
	if err != nil {
		return nil, err
	}
	reply := firstChannelReply(channelReply)
	queued := &models.ThreadInput{
		Scope:          models.ThreadInputScopeTask,
		ProjectID:      task.ProjectID,
		TaskID:         task.ID,
		RunExecutionID: activeExecutionID,
		AgentConfigID:  agentID,
		InputMode:      models.ThreadInputModeQueued,
		InputStatus:    models.ThreadInputPending,
		Content:        message,
		Source:         origin,
		OriginAgent:    originAgent,
	}
	if origin == models.TaskOriginEmail && strings.TrimSpace(reply.EmailFrom) != "" {
		queued.EmailFrom = reply.EmailFrom
		queued.EmailMessageID = reply.EmailMessageID
		queued.EmailReferences = reply.EmailReferences
		queued.EmailSubject = reply.EmailSubject
		queued.EmailSessionKey = reply.EmailSessionKey
	}
	automationContext, hasAutomationContext := service.AutomationContextFromContext(ctx)
	if !hasAutomationContext && origin == models.TaskOriginSystemAgent && originAgent == models.AgentSystemKindGoal && h.automationGraphSvc != nil {
		derivedContext, contextErr := h.automationGraphSvc.ContextForTask(ctx, task.ProjectID, task.ID)
		if contextErr != nil {
			return nil, fmt.Errorf("loading Automation context for goal continuation: %w", contextErr)
		}
		if len(derivedContext.Bindings) > 0 {
			automationContext = derivedContext
			hasAutomationContext = true
		}
	}
	if hasAutomationContext && automationContext.ProjectID == task.ProjectID && len(automationContext.Bindings) > 0 {
		if err := h.threadInputRepo.CreateQueuedWithAutomationContext(ctx, queued, automationContext, "causal"); err != nil {
			return nil, err
		}
	} else if err := h.threadInputRepo.CreateQueued(ctx, queued); err != nil {
		return nil, err
	}
	if err := h.bindQueuedTaskInputToActiveExecutionIfAvailable(ctx, queued); err != nil {
		applog.Infof("[handler] enqueueTaskThreadInput task=%s input=%s active execution bind skipped: %v", task.ID, queued.ID, err)
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{
			Type:           events.TaskThreadInputQueued,
			ProjectID:      task.ProjectID,
			TaskID:         task.ID,
			TaskName:       task.Title,
			PendingInputID: queued.ID,
			Message:        message,
		})
		if originAgent == "goal" {
			h.broadcaster.Publish(events.TaskEvent{
				Type:           events.TaskGoalFollowupQueued,
				ProjectID:      task.ProjectID,
				TaskID:         task.ID,
				TaskName:       task.Title,
				PendingInputID: queued.ID,
				Message:        message,
			})
		}
	}
	if shouldPromote, promoteErr := h.shouldPromotePreExecutionQueuedInput(ctx, task, queued); promoteErr != nil {
		applog.Infof("[handler] enqueueTaskThreadInput task=%s input=%s promotion recheck skipped: %v", task.ID, queued.ID, promoteErr)
	} else if shouldPromote || !queueBehindFirstTurn || queued.RunExecutionID != "" {
		go h.PromoteQueuedTaskThreadInput(task.ID)
	}
	return queued, nil
}

func (h *Handler) reactivateAchievedGoalForManualFollowup(ctx context.Context, taskID, origin, originAgent string) {
	origin, ok := normalizeManualGoalFollowupOrigin(origin, originAgent)
	if !ok || h.taskGoalSvc == nil || taskID == "" {
		return
	}
	goal, err := h.taskGoalSvc.ReactivateAchievedGoal(ctx, taskID, origin)
	if err != nil {
		applog.Infof("[handler] task=%s error reactivating achieved goal for follow-up: %v", taskID, err)
		return
	}
	if goal != nil {
		applog.Infof("[handler] task=%s goal=%s reactivated achieved goal for %s follow-up", taskID, goal.GoalID, origin)
	}
}

func (h *Handler) resumeUserStoppedGoalForManualStart(ctx context.Context, taskID, origin, originAgent string) {
	origin, ok := normalizeManualGoalFollowupOrigin(origin, originAgent)
	if !ok || h.taskGoalSvc == nil || taskID == "" {
		return
	}
	goal, err := h.taskGoalSvc.ResumeGoalStoppedByUser(ctx, taskID, origin)
	if err != nil {
		applog.Infof("[handler] task=%s error resuming user-stopped goal for follow-up: %v", taskID, err)
		return
	}
	if goal != nil {
		applog.Infof("[handler] task=%s goal=%s resumed user-stopped goal for %s follow-up", taskID, goal.GoalID, origin)
	}
}

func normalizeManualGoalFollowupOrigin(origin, originAgent string) (string, bool) {
	if strings.TrimSpace(originAgent) != "" {
		return "", false
	}
	origin = strings.TrimSpace(origin)
	if origin == "" {
		origin = models.TaskOriginWeb
	}
	if origin == models.TaskOriginSystemAgent {
		return "", false
	}
	return origin, true
}

func (h *Handler) GoalAgentAfterCompleteRuntimeTools(ctx context.Context, task models.Task) *llmcontracts.RuntimeTools {
	if h == nil || h.taskGoalSvc == nil || task.ID == "" {
		return nil
	}
	params := streamingResponseParams{
		ProjectID:       task.ProjectID,
		TaskID:          task.ID,
		IsTaskFollowup:  true,
		Surface:         chatcontrol.SurfaceWeb,
		AgentDefinition: h.resolveTaskAgentDefinitionForTask(ctx, task.ID, nil),
	}
	defs := filterGoalAgentRuntimeToolDefs(chatcontrol.LifecycleToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true))
	if len(defs) == 0 {
		return nil
	}
	return h.buildLifecycleChatActionToolRuntimeFromDefs(params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
}

func (h *Handler) taskGoalContext(ctx context.Context, taskID string, agentDef *models.Agent) string {
	if h.taskGoalSvc == nil || taskID == "" {
		return ""
	}
	goal, err := h.taskGoalSvc.GetGoal(ctx, taskID)
	if err != nil || goal == nil || goal.Status == models.TaskGoalStatusCleared {
		return ""
	}
	guidance := `Goal tools are available for this task thread. Use task_id="current" to read, update, pause, resume, or clear this goal.`
	grantedStatusTools := explicitlyGrantedGoalStatusTools(agentDef)
	if len(grantedStatusTools) > 0 {
		guidance += fmt.Sprintf(` This assigned agent is explicitly granted these goal status tools: %s. Use only the granted status tools when appropriate. Status writes are accepted only for this same goal_id while the goal is still active, so reload the goal first if state may have changed.`, strings.Join(grantedStatusTools, ", "))
	} else {
		guidance += ` Goal completion and blocker evaluation are handled by the protected Goal Agent unless this assigned agent is explicitly granted mark_task_goal_achieved or report_task_goal_blocked.`
	}
	return fmt.Sprintf("Task goal (%s):\n%s\n\n%s", goal.Status, goal.Objective, guidance)
}
