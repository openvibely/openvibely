package handler

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/util"
)

const (
	chatProcessingTimeout = 30 * time.Minute // Timeout for LLM processing in background goroutines
	chatHistoryLimit      = 50               // Number of recent chat messages to load for conversation context
	maxFileSize           = 10 << 20         // 10 MB per file
	maxFilesPerReq        = 10               // Max 10 files per request
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
//   - ProcessMarkers: true = process [CREATE_TASK]/[EDIT_TASK]/[EXECUTE_TASKS] markers in response
//   - ChatMode: orchestration mode for interactive chat (orchestrate/plan)
type streamingResponseParams struct {
	ExecID           string
	TaskID           string
	Message          string
	Agent            models.LLMConfig
	AgentDefinition  *models.Agent
	ChatHistory      []models.Execution
	ProjectID        string
	SystemContext    string
	WorkDir          string
	ImageAttachments []models.Attachment
	IsTaskFollowup   bool // true = coding agent prompt; false = orchestration prompt
	ProcessMarkers   bool // true = process [CREATE_TASK]/[EDIT_TASK]/[EXECUTE_TASKS] markers
	ChatMode         models.ChatMode
	Surface          chatcontrol.Surface // chat entry point (web/api/telegram/slack)
}

// processStreamingResponse is the shared goroutine that handles LLM streaming for
// both chat and task follow-up messages. This function runs asynchronously in a
// background goroutine, allowing the HTTP handler to return immediately.
//
// Process flow:
// 1. Creates a timeout context and registers cancellation with worker service
// 2. Calls the LLM service for streaming output (writes to DB in real-time)
// 3. Optionally processes response markers (task creation/edit/execution)
// 4. Completes the execution and updates task status
//
// Uses context.Background() for the base context since this goroutine should
// complete independently of the HTTP request (which may be canceled when the
// client disconnects). The timeout ensures we don't run forever.
//
// Error handling: All errors in the completion path are logged but don't fail the
// function since we're in a background goroutine. Failed completions leave tasks
// stuck in "running" status, which is why error logging is critical.
func (h *Handler) processStreamingResponse(params streamingResponseParams) {
	timeout := chatProcessingTimeout

	// Enforce per-project and per-model worker constraints for task follow-ups only.
	// Interactive chat (IsTaskFollowup=false) bypasses worker limits so the chat
	// orchestrator stays responsive even when all task workers are busy.
	// Task follow-ups (IsTaskFollowup=true) respect worker limits because they
	// execute code against active tasks and share resources with task workers.
	if params.IsTaskFollowup && h.workerSvc != nil {
		agentConfigID := params.Agent.ID

		// Apply model-specific worker_timeout if configured
		if modelTimeout := h.workerSvc.GetModelWorkerTimeout(agentConfigID); modelTimeout > 0 {
			timeout = modelTimeout
		}

		// Register DispatchNext FIRST so it runs LAST (Go defers are LIFO).
		// After this thread follow-up releases project+model slots, the worker
		// pool needs to check if any queued tasks can now be dispatched.
		defer h.workerSvc.DispatchNext()

		// Block until a per-project slot is available (respects project max_workers limit).
		// This queues the thread follow-up instead of rejecting it when workers are at capacity.
		waitCtx, waitCancel := context.WithTimeout(context.Background(), timeout)
		if err := h.workerSvc.AcquireProjectSlot(waitCtx, params.ProjectID); err != nil {
			waitCancel()
			log.Printf("[handler] processStreamingResponse exec=%s task=%s timed out waiting for project slot %s: %v",
				params.ExecID, params.TaskID, params.ProjectID, err)
			h.completeWithFailure(context.Background(), params.ExecID, params.TaskID,
				"project worker limit reached — timed out waiting for slot", 0)
			return
		}
		waitCancel()
		defer h.workerSvc.ReleaseProjectSlot(params.ProjectID)

		// Block until a model slot is available (respects max_workers)
		waitCtx2, waitCancel2 := context.WithTimeout(context.Background(), timeout)
		if err := h.workerSvc.AcquireModelSlot(waitCtx2, agentConfigID); err != nil {
			waitCancel2()
			log.Printf("[handler] processStreamingResponse exec=%s task=%s failed to acquire model slot for %s: %v",
				params.ExecID, params.TaskID, agentConfigID, err)
			h.completeWithFailure(context.Background(), params.ExecID, params.TaskID,
				fmt.Sprintf("model %s at capacity, timed out waiting for slot", params.Agent.Name), 0)
			return
		}
		waitCancel2()
		defer h.workerSvc.ReleaseModelSlot(agentConfigID)

		log.Printf("[handler] processStreamingResponse exec=%s acquired project + model slots for %s", params.ExecID, agentConfigID)

		// Transition task from "queued" to "running" now that worker slots are acquired
		if task, err := h.taskRepo.GetByID(context.Background(), params.TaskID); err == nil && task != nil && task.Status == models.StatusQueued {
			log.Printf("[handler] processStreamingResponse exec=%s task=%s transitioning from queued to running", params.ExecID, params.TaskID)
			if err := h.taskRepo.UpdateStatus(context.Background(), params.TaskID, models.StatusRunning); err != nil {
				log.Printf("[handler] processStreamingResponse exec=%s task=%s failed to update status to running: %v", params.ExecID, params.TaskID, err)
			}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	chatMode := params.ChatMode
	if chatMode == "" {
		chatMode = models.ChatModeOrchestrate
	}
	ctx = llmcontracts.WithChatMode(ctx, chatMode)
	// Inject request-scoped action tools when the provider supports runtime tool
	// calling. Tool definitions are derived from the canonical chatcontrol registry
	// filtered by mode and surface. In plan mode only read actions are available.
	// Runtime-tools and marker post-processing are mutually exclusive per request.
	if !params.IsTaskFollowup && supportsChatActionTools(params.Agent) {
		surface := params.Surface
		if surface == "" {
			surface = chatcontrol.SurfaceWeb
		}
		defs := chatcontrol.ToolDefsForContext(chatMode, surface, chatMode == models.ChatModeOrchestrate)
		if len(defs) > 0 {
			rt := h.buildChatActionToolRuntimeFromDefs(params, nil, defs, chatMode, surface)
			ctx = llmcontracts.WithRuntimeTools(ctx, rt)
			params.ProcessMarkers = false
			log.Printf("[handler] processStreamingResponse exec=%s injected %d runtime action tools mode=%s surface=%s",
				params.ExecID, len(defs), chatMode, surface)
		}
	}

	h.registerTaskCancellation(params.TaskID, cancel)
	defer h.deregisterTaskCancellation(params.TaskID)

	log.Printf("[handler] processStreamingResponse exec=%s task=%s agent=%s model=%s followup=%v markers=%v history=%d",
		params.ExecID, params.TaskID, params.Agent.Name, params.Agent.Model, params.IsTaskFollowup, params.ProcessMarkers, len(params.ChatHistory))

	initialDiffOutput := h.captureTaskDiffBaseline(ctx, params.TaskID, params.WorkDir, params.IsTaskFollowup)
	stopDiffBroadcast, diffBroadcastDone := h.startFollowupDiffSnapshotBroadcast(ctx, params.TaskID, params.ExecID, params.WorkDir, params.IsTaskFollowup)

	agentDef := h.resolveTaskAgentDefinitionForTask(ctx, params.TaskID, params.AgentDefinition)

	start := time.Now()
	result, err := h.llmSvc.CallAgentDirectStreamingDetailed(
		ctx, params.Message, params.ImageAttachments, params.Agent,
		params.ExecID, params.ChatHistory, params.SystemContext,
		params.WorkDir, agentDef, params.IsTaskFollowup,
	)
	if stopDiffBroadcast != nil {
		close(stopDiffBroadcast)
		if diffBroadcastDone != nil {
			<-diffBroadcastDone
		}
	}
	durationMs := time.Since(start).Milliseconds()
	output := result.Output
	textOnlyOutput := result.TextOnlyOutput
	tokensUsed := result.Usage.TotalTokens

	if err != nil {
		log.Printf("[handler] processStreamingResponse exec=%s task=%s LLM call failed after %dms: %v", params.ExecID, params.TaskID, durationMs, err)
		// When max_tokens is hit, partial output is returned. Preserve it in the
		// execution so the user can see what work was done before the limit.
		if output != "" {
			log.Printf("[handler] processStreamingResponse exec=%s max_tokens failure, preserving partial output (%d bytes)", params.ExecID, len(output))
			h.completeWithFailureAndOutput(ctx, params.ExecID, params.TaskID, err.Error(), output, tokensUsed, durationMs)
		} else {
			h.completeWithFailure(ctx, params.ExecID, params.TaskID, err.Error(), durationMs)
		}
		return
	}

	// Check context before expensive marker processing
	if ctx.Err() != nil {
		log.Printf("[handler] processStreamingResponse exec=%s task=%s context cancelled, skipping marker processing", params.ExecID, params.TaskID)
		h.completeWithFailure(ctx, params.ExecID, params.TaskID, "processing cancelled", durationMs)
		return
	}

	// Marker fallback path for non-tool providers/transports.
	if params.ProcessMarkers {
		agents, err := h.llmConfigRepo.List(ctx)
		if err != nil {
			log.Printf("[handler] processStreamingResponse error listing agents: %v", err)
			// Continue without marker processing rather than failing the entire response
			agents = []models.LLMConfig{}
		}
		output = h.processChatResponse(ctx, params.ExecID, params.ProjectID, output, agents)
	}

	if params.IsTaskFollowup {
		statusCheckOutput := textOnlyOutput
		if statusCheckOutput == "" {
			statusCheckOutput = output
		}
		if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: FAILED |"); found {
			log.Printf("[handler] processStreamingResponse exec=%s task=%s agent reported STATUS FAILED reason=%q", params.ExecID, params.TaskID, reason)
			h.completeWithFailureAndOutput(ctx, params.ExecID, params.TaskID, reason, output, tokensUsed, durationMs)
			return
		}
	}

	log.Printf("[handler] processStreamingResponse exec=%s task=%s success tokens=%d duration=%dms output_len=%d",
		params.ExecID, params.TaskID, tokensUsed, durationMs, len(output))

	h.completeWithSuccess(ctx, params.ExecID, params.TaskID, output, params.WorkDir, initialDiffOutput, tokensUsed, durationMs)

	if params.IsTaskFollowup {
		statusCheckOutput := textOnlyOutput
		if statusCheckOutput == "" {
			statusCheckOutput = output
		}
		if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: NEEDS_FOLLOWUP |"); found {
			task, taskErr := h.taskRepo.GetByID(ctx, params.TaskID)
			if taskErr != nil {
				log.Printf("[handler] processStreamingResponse exec=%s task=%s error loading task for followup alert: %v", params.ExecID, params.TaskID, taskErr)
			} else if task != nil && h.alertSvc != nil {
				if alertErr := h.alertSvc.CreateTaskNeedsFollowupAlert(ctx, task.ProjectID, task.ID, params.ExecID, task.Title, reason); alertErr != nil {
					log.Printf("[handler] processStreamingResponse exec=%s task=%s error creating followup alert: %v", params.ExecID, params.TaskID, alertErr)
				}
			}
		}
	}

	// Broadcast response done for chat messages (not task followups).
	// Include completed output so chat_response_done SSE fallback can evaluate
	// plan-completion prompt visibility without a DOM scan.
	if !params.IsTaskFollowup && h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:            events.ChatResponseDone,
			ProjectID:       params.ProjectID,
			ExecID:          params.ExecID,
			TaskID:          params.TaskID,
			CompletedOutput: output,
		})
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
func (h *Handler) completeWithSuccess(ctx context.Context, execID, taskID, output, workDir, initialDiffOutput string, tokensUsed int, durationMs int64) {
	if err := h.execRepo.Complete(ctx, execID, models.ExecCompleted, output, "", tokensUsed, durationMs); err != nil {
		log.Printf("[handler] completeWithSuccess exec=%s error completing execution: %v", execID, err)
	}

	// Update task status BEFORE git diff capture. The SSE handler detects
	// ExecCompleted and sends a 'done' event, triggering a client-side page
	// refresh. If the task status update happens after git diff capture
	// (which can be slow), the refreshed page may still show 'running' status.
	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusCompleted); err != nil {
		log.Printf("[handler] completeWithSuccess task=%s error updating status: %v", taskID, err)
	}

	// Move active tasks to the completed category so they appear in the right column
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err == nil && task != nil && task.Category == models.CategoryActive {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryCompleted); err != nil {
			log.Printf("[handler] completeWithSuccess task=%s error moving to completed category: %v", taskID, err)
		} else {
			log.Printf("[handler] completeWithSuccess task=%s moved to completed category", taskID)
		}
	}

	// Capture git diff after status update so it doesn't delay the UI refresh
	if workDir != "" {
		commitMessage := ""
		if task != nil {
			commitMessage = fmt.Sprintf("Followup: %s", task.Title)
		}
		diffOutput := h.captureTaskDiffOutput(ctx, task, workDir, commitMessage)

		if diffOutput != "" {
			if err := h.execRepo.UpdateDiffOutput(ctx, execID, diffOutput); err != nil {
				log.Printf("[handler] completeWithSuccess exec=%s error updating diff: %v", execID, err)
			}

			// Reset merge status when follow-up creates new changes
			// If task was previously merged and now has new changes, set status to pending
			// so the merge button re-appears
			if task != nil && task.WorktreePath != "" && task.MergeStatus == models.MergeStatusMerged {
				log.Printf("[handler] completeWithSuccess task=%s resetting merge_status from merged to pending (new changes detected)", taskID)
				if err := h.taskRepo.UpdateMergeStatus(ctx, taskID, models.MergeStatusPending); err != nil {
					log.Printf("[handler] completeWithSuccess task=%s error resetting merge status: %v", taskID, err)
				}
			}
		}

	}
}

func normalizeDiffSnapshot(diff string) string {
	return strings.TrimSpace(diff)
}

func (h *Handler) startFollowupDiffSnapshotBroadcast(ctx context.Context, taskID, execID, workDir string, isTaskFollowup bool) (chan struct{}, chan struct{}) {
	if !isTaskFollowup || workDir == "" || h.fileChangeBroadcaster == nil || h.execRepo == nil {
		return nil, nil
	}

	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return nil, nil
	}

	var repoDir, worktreeBranch, mergeTargetBranch string
	if task.WorktreePath != "" && task.WorktreeBranch != "" && task.WorktreePath == workDir {
		project, projErr := h.projectRepo.GetByID(ctx, task.ProjectID)
		if projErr != nil || project == nil || project.RepoPath == "" {
			return nil, nil
		}
		repoDir = project.RepoPath
		worktreeBranch = task.WorktreeBranch
		mergeTargetBranch = task.MergeTargetBranch
	}

	captureDiff := func() string {
		if repoDir != "" && worktreeBranch != "" {
			targetBranch := mergeTargetBranch
			if targetBranch == "" {
				targetBranch = service.GetDefaultBranch(repoDir)
			}
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

		publish := func() {
			diffOutput := captureDiff()
			if diffOutput == "" {
				return
			}
			if err := h.execRepo.UpdateDiffOutput(ctx, execID, diffOutput); err != nil {
				log.Printf("[handler] followup diff broadcast exec=%s error updating diff output: %v", execID, err)
			} else {
				log.Printf("[handler] followup diff broadcast exec=%s updated diff output (%d bytes)", execID, len(diffOutput))
			}
			h.fileChangeBroadcaster.Publish(events.FileChangeEvent{
				Type:       events.DiffSnapshot,
				TaskID:     taskID,
				ExecID:     execID,
				DiffOutput: diffOutput,
				Timestamp:  time.Now().UnixMilli(),
			})
		}

		for {
			select {
			case <-stop:
				publish()
				return
			default:
			}

			select {
			case <-stop:
				publish()
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				publish()
			}
		}
	}()

	return stop, done
}

func (h *Handler) captureTaskDiffBaseline(ctx context.Context, taskID, workDir string, isTaskFollowup bool) string {
	if !isTaskFollowup || workDir == "" {
		return ""
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil {
		return ""
	}
	return h.captureTaskDiffOutput(ctx, task, workDir, "")
}

func (h *Handler) captureTaskDiffOutput(ctx context.Context, task *models.Task, workDir, commitMessage string) string {
	if workDir == "" {
		return ""
	}

	if task != nil && task.WorktreePath != "" && task.WorktreeBranch != "" && task.WorktreePath == workDir {
		project, _ := h.projectRepo.GetByID(ctx, task.ProjectID)
		if project != nil && project.RepoPath != "" {
			if commitMessage != "" {
				service.CommitWorktreeChanges(task.WorktreePath, commitMessage)
			}
			targetBranch := task.MergeTargetBranch
			if targetBranch == "" {
				targetBranch = service.GetDefaultBranch(project.RepoPath)
			}
			return service.GetWorktreeDiff(project.RepoPath, task.WorktreeBranch, targetBranch)
		}
	}

	return h.llmSvc.CaptureGitDiff(workDir)
}

// completeWithFailure marks an execution and its task as failed, moves it to backlog,
// and creates a failure alert. Uses a fresh background context with a 30-second timeout
// to ensure DB updates succeed even when the original context has expired (e.g., after
// the 5-minute LLM processing timeout).
func (h *Handler) completeWithFailure(_ context.Context, execID, taskID, errorMessage string, durationMs int64) {
	// Use a fresh context — the caller's context may already be expired (e.g., after
	// chatProcessingTimeout). DB updates must still succeed.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.execRepo.Complete(ctx, execID, models.ExecFailed, "", errorMessage, 0, durationMs); err != nil {
		log.Printf("[handler] completeWithFailure exec=%s error completing execution: %v", execID, err)
	}

	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
		log.Printf("[handler] completeWithFailure task=%s error updating status: %v", taskID, err)
	}

	// Move task to backlog so it can be re-executed
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		log.Printf("[handler] completeWithFailure task=%s error getting task: %v", taskID, err)
		return
	}
	if task != nil && (task.Category == models.CategoryActive || task.Category == models.CategoryCompleted) {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryBacklog); err != nil {
			log.Printf("[handler] completeWithFailure task=%s error moving to backlog: %v", taskID, err)
		} else {
			log.Printf("[handler] completeWithFailure task=%s moved to backlog", taskID)
		}
	}

	// Create failure alert
	if task != nil && h.alertSvc != nil {
		if err := h.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, taskID, execID, task.Title, errorMessage); err != nil {
			log.Printf("[handler] completeWithFailure task=%s error creating alert: %v", taskID, err)
		}
	}
}

// completeWithFailureAndOutput is like completeWithFailure but preserves partial output
// (e.g., when max_tokens is hit and the LLM produced output before the limit).
func (h *Handler) completeWithFailureAndOutput(_ context.Context, execID, taskID, errorMessage, output string, tokensUsed int, durationMs int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.execRepo.Complete(ctx, execID, models.ExecFailed, output, errorMessage, tokensUsed, durationMs); err != nil {
		log.Printf("[handler] completeWithFailureAndOutput exec=%s error completing execution: %v", execID, err)
	}

	if err := h.taskRepo.UpdateStatus(ctx, taskID, models.StatusFailed); err != nil {
		log.Printf("[handler] completeWithFailureAndOutput task=%s error updating status: %v", taskID, err)
	}

	// Move task to backlog so it can be re-executed
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		log.Printf("[handler] completeWithFailureAndOutput task=%s error getting task: %v", taskID, err)
		return
	}
	if task != nil && (task.Category == models.CategoryActive || task.Category == models.CategoryCompleted) {
		if err := h.taskRepo.UpdateCategory(ctx, taskID, models.CategoryBacklog); err != nil {
			log.Printf("[handler] completeWithFailureAndOutput task=%s error moving to backlog: %v", taskID, err)
		} else {
			log.Printf("[handler] completeWithFailureAndOutput task=%s moved to backlog", taskID)
		}
	}

	// Create failure alert
	if task != nil && h.alertSvc != nil {
		if err := h.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, taskID, execID, task.Title, errorMessage); err != nil {
			log.Printf("[handler] completeWithFailureAndOutput task=%s error creating alert: %v", taskID, err)
		}
	}
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

	result := make([]models.Execution, 0, len(executions))
	for i := range executions {
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
		log.Printf("[handler] selectDefaultAgent no default configured, falling back to first agent: %s", agents[0].Name)
		return &agents[0], nil
	}
	if hasImages && !agent.IsAnthropicAPIKey() && !agent.IsOAuth() {
		log.Printf("[handler] selectDefaultAgent warning: agent %s may not support vision with image attachments", agent.Name)
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
		log.Printf("[handler] selectExplicitAgent warning: agent %s may not support vision with image attachments", agent.Name)
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
		log.Printf("[handler] autoSelectAgent has images but no vision-capable agents, falling back to first available")
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
func (h *Handler) resolveWorktreeWorkDir(ctx context.Context, task *models.Task) string {
	project, err := h.projectSvc.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return ""
	}
	repoDir := project.RepoPath

	if task.Category == models.CategoryChat || !service.IsGitRepo(repoDir) {
		return repoDir
	}

	if h.worktreeSvc == nil {
		return repoDir
	}

	wtPath, _, wtErr := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if wtErr != nil {
		log.Printf("[handler] resolveWorktreeWorkDir worktree setup failed for task %s, using main repo: %v", task.ID, wtErr)
		return repoDir
	}

	log.Printf("[handler] resolveWorktreeWorkDir task=%s using worktree path=%s", task.ID, wtPath)
	return wtPath
}

// processChatTaskCreations handles task creation markers from AI responses.
// It parses [CREATE_TASK] markers, creates tasks, copies attachments, and handles
// deferred activation (when attachments need to be copied before auto-submission).
// Returns the updated output with creation summaries and the count of attachments copied.
func (h *Handler) processChatTaskCreations(ctx context.Context, execID, projectID, output string, agents []models.LLMConfig) (string, int) {
	taskRequests := parseChatTaskCreations(output)
	if len(taskRequests) == 0 {
		return output, 0
	}

	log.Printf("[handler] processChatTaskCreations exec=%s found %d task creation requests", execID, len(taskRequests))

	// Resolve agent names to agent definition IDs
	if h.agentRepo != nil {
		for i := range taskRequests {
			agentName := taskRequests[i].Agent
			if agentName == "" {
				agentName = taskRequests[i].AgentDefinitionID
			}
			if agentName != "" && h.agentRepo != nil {
				if ad, err := h.agentRepo.GetByName(ctx, agentName); err == nil && ad != nil {
					taskRequests[i].AgentDefinitionID = ad.ID
					log.Printf("[handler] resolved agent %q → %s for task %q", agentName, ad.ID, taskRequests[i].Title)
				}
			}
		}
	}

	chatAtts, _ := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	deferredActiveTitles := h.deferActiveTasksWithAttachments(taskRequests, chatAtts)

	createdTasks, summary := h.executeChatTaskCreationsWithAttachments(ctx, taskRequests, projectID, execID, agents)

	totalAttachmentsCopied := h.copyAttachmentsToTasks(ctx, execID, createdTasks, chatAtts)
	h.activateDeferredTasks(ctx, createdTasks, deferredActiveTitles)

	return h.appendCreationSummary(output, summary, totalAttachmentsCopied, chatAtts), totalAttachmentsCopied
}

// deferActiveTasksWithAttachments defers activation of "active" tasks when attachments exist.
// Returns a map of task titles that should be activated after attachment copying.
func (h *Handler) deferActiveTasksWithAttachments(taskRequests []service.TaskCreationRequest, chatAtts []models.ChatAttachment) map[string]bool {
	if len(chatAtts) == 0 {
		return nil
	}

	deferredActiveTitles := make(map[string]bool)
	for i := range taskRequests {
		if taskRequests[i].Category == "active" {
			deferredActiveTitles[taskRequests[i].Title] = true
			taskRequests[i].Category = "backlog"
			log.Printf("[handler] deferActiveTasksWithAttachments deferred auto-submit for task %q (has attachments)", taskRequests[i].Title)
		}
	}
	return deferredActiveTitles
}

// copyAttachmentsToTasks copies chat attachments to all created tasks.
// Returns the total count of attachments successfully copied.
func (h *Handler) copyAttachmentsToTasks(ctx context.Context, execID string, createdTasks []models.Task, chatAtts []models.ChatAttachment) int {
	if len(createdTasks) == 0 || len(chatAtts) == 0 {
		return 0
	}

	totalCopied := 0
	for _, task := range createdTasks {
		copiedCount, err := h.copyChatAttachmentsToTask(ctx, execID, task.ID)
		if err != nil {
			log.Printf("[handler] copyAttachmentsToTasks error copying to task %s: %v", task.ID, err)
		} else if copiedCount > 0 {
			totalCopied += copiedCount
			log.Printf("[handler] copyAttachmentsToTasks copied %d attachments to task %s", copiedCount, task.ID)
		}
	}
	return totalCopied
}

// activateDeferredTasks activates tasks that were deferred due to attachment copying.
func (h *Handler) activateDeferredTasks(ctx context.Context, createdTasks []models.Task, deferredActiveTitles map[string]bool) {
	if len(deferredActiveTitles) == 0 {
		return
	}

	for _, task := range createdTasks {
		if deferredActiveTitles[task.Title] {
			log.Printf("[handler] activateDeferredTasks activating task %s %q", task.ID, task.Title)
			if err := h.taskSvc.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				log.Printf("[handler] activateDeferredTasks error activating task %s: %v", task.ID, err)
			}
		}
	}
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

// processChatTaskEdits handles task edit markers from AI responses.
// It parses [EDIT_TASK] markers, applies the edits (including file attachments), and appends summaries to the output.
// When an edit request includes "chat" in its attachments list, the current chat execution's
// file attachments are copied to the target task (appearing in its Attachments tab).
// Returns the updated output with edit summaries appended.
func (h *Handler) processChatTaskEdits(ctx context.Context, execID, projectID, output string) string {
	editRequests := parseChatTaskEdits(output)
	if len(editRequests) == 0 {
		return output
	}

	log.Printf("[handler] processChatTaskEdits exec=%s found %d task edit requests", execID, len(editRequests))

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

// processChatAttachmentsForEdits processes the special "chat" keyword in edit request attachments.
// When an [EDIT_TASK] includes "chat" in its attachments list, this copies the current chat
// execution's file attachments to the target task's attachment tab. The "chat" keyword is
// removed from the request so it doesn't get processed as a file path by copyAttachmentFiles.
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
			log.Printf("[handler] processChatAttachmentsForEdits error copying chat attachments to task %s: %v", taskID, err)
		} else if copiedCount > 0 {
			totalCopied += copiedCount
			log.Printf("[handler] processChatAttachmentsForEdits copied %d chat attachments to task %s", copiedCount, taskID)
		} else {
			log.Printf("[handler] processChatAttachmentsForEdits no chat attachments to copy for exec=%s", execID)
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

// processChatTaskExecutions handles task execution markers from AI responses.
// It parses [EXECUTE_TASKS] markers, triggers task execution, and appends summaries to the output.
// Returns the updated output with execution summaries appended.
func (h *Handler) processChatTaskExecutions(ctx context.Context, execID, projectID, output string) string {
	execRequests := parseChatTaskExecutions(output)
	if len(execRequests) == 0 {
		return output
	}

	log.Printf("[handler] processChatTaskExecutions exec=%s found %d task execution requests", execID, len(execRequests))
	execSummary := executeChatTaskExecutions(ctx, execRequests, projectID, h.taskSvc)
	if execSummary != "" {
		output += execSummary
	}
	return output
}

// processViewThread handles [VIEW_TASK_CHAT] markers from AI responses.
// It resolves the target task (by ID or title search), fetches the execution history,
// formats it as a readable thread transcript, and replaces the marker with the transcript.
func (h *Handler) processViewThread(ctx context.Context, execID, projectID, output string) string {
	viewRequests := parseViewThread(output)
	if len(viewRequests) == 0 {
		return output
	}

	log.Printf("[handler] processViewThread exec=%s found %d view requests", execID, len(viewRequests))

	for _, req := range viewRequests {
		task, err := h.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[handler] processViewThread error resolving task: %v", err)
			output += fmt.Sprintf("\n\n---\nCould not find task: %v", err)
			continue
		}

		executions, err := h.execRepo.ListByTaskChronological(ctx, task.ID)
		if err != nil {
			log.Printf("[handler] processViewThread error listing executions for task %s: %v", task.ID, err)
			output += fmt.Sprintf("\n\n---\nError retrieving thread for task \"%s\": %v", task.Title, err)
			continue
		}

		transcript := h.formatThreadTranscript(task, executions, req.Offset, req.Limit)
		output += transcript
	}

	return output
}

// processChatSendToTask handles [SEND_TO_TASK] markers from AI responses.
// It resolves the target task, validates its state, creates a follow-up execution,
// and spawns a background goroutine to process the message with the task's AI agent.
func (h *Handler) processChatSendToTask(ctx context.Context, execID, projectID, output string) string {
	sendRequests := parseChatSendToTask(output)
	if len(sendRequests) == 0 {
		return output
	}

	log.Printf("[handler] processChatSendToTask exec=%s found %d send requests", execID, len(sendRequests))

	var results []string
	for _, req := range sendRequests {
		task, err := h.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[handler] processChatSendToTask error resolving task: %v", err)
			results = append(results, fmt.Sprintf("- Could not find task: %v", err))
			continue
		}

		// Don't send to running tasks — wait for them to finish
		if task.Status == models.StatusRunning {
			log.Printf("[handler] processChatSendToTask task %s is running, cannot send", task.ID)
			results = append(results, fmt.Sprintf("- Task \"%s\" is currently running. Wait for it to finish before sending a message.", task.Title))
			continue
		}

		// Always set status to running since we're about to execute
		if task.Status != models.StatusRunning {
			log.Printf("[handler] processChatSendToTask setting task=%s status=running (was %s)", task.ID, task.Status)
			if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning); err != nil {
				log.Printf("[handler] processChatSendToTask error setting status: %v", err)
				results = append(results, fmt.Sprintf("- Error updating task \"%s\": %v", task.Title, err))
				continue
			}
		}
		// Always move to active category so the task appears in the Active column
		if task.Category != models.CategoryActive {
			log.Printf("[handler] processChatSendToTask moving task=%s to active (was %s)", task.ID, task.Category)
			if err := h.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
				log.Printf("[handler] processChatSendToTask error updating category: %v", err)
			}
		}

		// Select agent: use task's assigned agent, or fall back to default
		var agent *models.LLMConfig
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(ctx, *task.AgentID)
		}
		if agent == nil {
			agent, err = h.selectDefaultAgent(ctx, false)
			if err != nil {
				log.Printf("[handler] processChatSendToTask error selecting agent: %v", err)
				results = append(results, fmt.Sprintf("- Error selecting agent for task \"%s\": %v", task.Title, err))
				continue
			}
		}

		// Check per-project and per-model worker capacity before starting execution
		if h.workerSvc != nil {
			if !h.workerSvc.HasProjectCapacity(task.ProjectID) {
				log.Printf("[handler] processChatSendToTask rejected: project %s at worker capacity", task.ProjectID)
				results = append(results, fmt.Sprintf("- Project worker limit reached for task \"%s\" — please wait for a running task to complete", task.Title))
				continue
			}
			if !h.workerSvc.HasModelCapacity(agent.ID) {
				log.Printf("[handler] processChatSendToTask rejected: model %s is at capacity", agent.ID)
				results = append(results, fmt.Sprintf("- Model %s is at capacity for task \"%s\" — please wait for a running task to complete", agent.Name, task.Title))
				continue
			}
		}

		// Create follow-up execution
		exec := &models.Execution{
			TaskID:        task.ID,
			AgentConfigID: agent.ID,
			Status:        models.ExecRunning,
			PromptSent:    req.Message,
			IsFollowup:    true,
		}
		if err := h.execRepo.Create(ctx, exec); err != nil {
			log.Printf("[handler] processChatSendToTask error creating execution: %v", err)
			results = append(results, fmt.Sprintf("- Error creating execution for task \"%s\": %v", task.Title, err))
			continue
		}

		// Load conversation history and build context
		priorExecs, _ := h.execRepo.ListByTaskChronological(ctx, task.ID)
		priorHistory := filterChatHistory(priorExecs, exec.ID)
		systemContext := buildThreadSystemContext(task.Title, len(priorHistory) > 0, "")
		pCtx := h.getPersonalityContext(ctx, task.ProjectID)
		workDir := h.resolveWorktreeWorkDir(ctx, task)
		var agentDef *models.Agent
		if task.AgentDefinitionID != nil && h.agentRepo != nil {
			if ad, adErr := h.agentRepo.GetByID(ctx, *task.AgentDefinitionID); adErr == nil && ad != nil {
				agentDef = ad
			}
		}

		log.Printf("[handler] processChatSendToTask sending message to task=%s exec=%s agent=%s", task.ID, exec.ID, agent.Name)

		// Spawn background goroutine (same as TaskThreadSend)
		go h.processStreamingResponse(streamingResponseParams{
			ExecID:          exec.ID,
			TaskID:          task.ID,
			Message:         req.Message,
			Agent:           *agent,
			AgentDefinition: agentDef,
			ChatHistory:     priorHistory,
			ProjectID:       task.ProjectID,
			SystemContext:   combineContexts(systemContext, pCtx),
			WorkDir:         workDir,
			IsTaskFollowup:  true,
			ProcessMarkers:  false,
		})

		results = append(results, fmt.Sprintf("- Sent message to task \"%s\" [TASK_ID:%s] — the agent is now processing your message. View the response on the task's thread page.", task.Title, task.ID))
	}

	if len(results) > 0 {
		output += "\n\n---\nThread Messages:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatScheduleTask handles [SCHEDULE_TASK] markers from AI responses.
// It resolves the target task (by ID or title search), creates a schedule entry,
// moves the task to 'scheduled' category, and returns the updated output.
func (h *Handler) processChatScheduleTask(ctx context.Context, execID, projectID, output string) string {
	scheduleRequests := parseChatScheduleTask(output)
	if len(scheduleRequests) == 0 {
		return output
	}

	log.Printf("[handler] processChatScheduleTask exec=%s found %d schedule requests", execID, len(scheduleRequests))

	var results []string
	for _, req := range scheduleRequests {
		task, err := h.resolveTaskReference(ctx, projectID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[handler] processChatScheduleTask error resolving task: %v", err)
			results = append(results, fmt.Sprintf("- Could not find task: %v", err))
			continue
		}

		// Parse time (HH:MM format)
		var hourVal, minuteVal int
		if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
			log.Printf("[handler] processChatScheduleTask invalid time: %s", req.Time)
			results = append(results, fmt.Sprintf("- Invalid time %q for task \"%s\" (expected HH:MM, 00:00-23:59)", req.Time, task.Title))
			continue
		}

		// Determine repeat type
		repeatType := models.RepeatDaily // default
		switch strings.ToLower(req.Repeat) {
		case "once":
			repeatType = models.RepeatOnce
		case "daily", "":
			repeatType = models.RepeatDaily
		case "weekly":
			repeatType = models.RepeatWeekly
		case "monthly":
			repeatType = models.RepeatMonthly
		case "hours", "hourly":
			repeatType = models.RepeatHours
		case "minutes":
			repeatType = models.RepeatMinutes
		case "seconds":
			repeatType = models.RepeatSeconds
		default:
			log.Printf("[handler] processChatScheduleTask unknown repeat type %q, defaulting to daily", req.Repeat)
		}

		// Determine repeat interval (default 1)
		repeatInterval := 1
		if req.Interval > 0 {
			repeatInterval = req.Interval
		}

		// Build RunAt: today at the specified time in local timezone
		now := time.Now().Local()
		runAt := time.Date(now.Year(), now.Month(), now.Day(), hourVal, minuteVal, 0, 0, time.Local)

		// For weekly schedules with specific days, adjust RunAt to the next matching day
		if repeatType == models.RepeatWeekly && len(req.Days) > 0 {
			dayMap := map[string]time.Weekday{
				"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
				"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
				"sat": time.Saturday,
			}
			// Find the nearest future day from the requested days
			bestOffset := 8 // more than 7
			for _, d := range req.Days {
				target, ok := dayMap[strings.ToLower(d)]
				if !ok {
					continue
				}
				offset := int(target - runAt.Weekday())
				if offset < 0 {
					offset += 7
				}
				if offset == 0 && runAt.Before(now) {
					offset = 7
				}
				if offset < bestOffset {
					bestOffset = offset
				}
			}
			if bestOffset < 8 {
				runAt = runAt.AddDate(0, 0, bestOffset)
			}
		}

		// Convert to UTC for storage
		runAtUTC := runAt.UTC()

		schedule := &models.Schedule{
			TaskID:         task.ID,
			RunAt:          runAtUTC,
			RepeatType:     repeatType,
			RepeatInterval: repeatInterval,
			Enabled:        true,
		}

		if err := h.scheduleRepo.Create(ctx, schedule); err != nil {
			log.Printf("[handler] processChatScheduleTask error creating schedule: %v", err)
			results = append(results, fmt.Sprintf("- Error scheduling task \"%s\": %v", task.Title, err))
			continue
		}

		// Move task to scheduled category if not already
		if task.Category != models.CategoryScheduled {
			if err := h.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryScheduled); err != nil {
				log.Printf("[handler] processChatScheduleTask error updating category: %v", err)
			}
			if task.Status != models.StatusPending {
				if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
					log.Printf("[handler] processChatScheduleTask error updating status: %v", err)
				}
			}
		}

		repeatDesc := service.FormatRepeatPattern(repeatType, repeatInterval)
		if repeatType == models.RepeatWeekly && len(req.Days) > 0 {
			repeatDesc = fmt.Sprintf("weekly on %s", strings.Join(req.Days, ", "))
			if repeatInterval > 1 {
				repeatDesc = fmt.Sprintf("every %d weeks on %s", repeatInterval, strings.Join(req.Days, ", "))
			}
		}
		results = append(results, fmt.Sprintf("- Scheduled task \"%s\" [TASK_ID:%s] at %s (%s)", task.Title, task.ID, req.Time, repeatDesc))
		log.Printf("[handler] processChatScheduleTask scheduled task=%s schedule=%s at %s repeat=%s", task.ID, schedule.ID, req.Time, repeatType)
	}

	if len(results) > 0 {
		output += "\n\n---\nSchedule Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatDeleteSchedule handles [DELETE_SCHEDULE] markers from AI responses.
// It resolves the target schedule (by schedule_id, task_id, or title), deletes it,
// and returns the updated output.
func (h *Handler) processChatDeleteSchedule(ctx context.Context, execID, projectID, output string) string {
	deleteRequests := parseChatDeleteSchedule(output)
	if len(deleteRequests) == 0 {
		return output
	}

	log.Printf("[handler] processChatDeleteSchedule exec=%s found %d delete requests", execID, len(deleteRequests))

	var results []string
	for _, req := range deleteRequests {
		// Resolve the schedule to delete
		schedule, task, err := h.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[handler] processChatDeleteSchedule error resolving schedule: %v", err)
			results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			continue
		}

		if err := h.scheduleRepo.Delete(ctx, schedule.ID); err != nil {
			log.Printf("[handler] processChatDeleteSchedule error deleting schedule: %v", err)
			results = append(results, fmt.Sprintf("- Error deleting schedule for task \"%s\": %v", task.Title, err))
			continue
		}

		// Check if the task has any remaining schedules
		remaining, err := h.scheduleRepo.ListByTask(ctx, task.ID)
		if err != nil {
			log.Printf("[handler] processChatDeleteSchedule error checking remaining schedules: %v", err)
		}
		if len(remaining) == 0 && task.Category == models.CategoryScheduled {
			// No more schedules — move task back to backlog
			if err := h.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryBacklog); err != nil {
				log.Printf("[handler] processChatDeleteSchedule error updating category: %v", err)
			}
		}

		results = append(results, fmt.Sprintf("- Deleted schedule for task \"%s\" [TASK_ID:%s]", task.Title, task.ID))
		log.Printf("[handler] processChatDeleteSchedule deleted schedule=%s task=%s", schedule.ID, task.ID)
	}

	if len(results) > 0 {
		output += "\n\n---\nSchedule Delete Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatModifySchedule handles [MODIFY_SCHEDULE] markers from AI responses.
// It resolves the target schedule, applies the requested modifications, and returns the updated output.
func (h *Handler) processChatModifySchedule(ctx context.Context, execID, projectID, output string) string {
	modifyRequests := parseChatModifySchedule(output)
	if len(modifyRequests) == 0 {
		return output
	}

	log.Printf("[handler] processChatModifySchedule exec=%s found %d modify requests", execID, len(modifyRequests))

	var results []string
	for _, req := range modifyRequests {
		schedule, task, err := h.resolveScheduleReference(ctx, projectID, req.ScheduleID, req.TaskID, req.Title)
		if err != nil {
			log.Printf("[handler] processChatModifySchedule error resolving schedule: %v", err)
			results = append(results, fmt.Sprintf("- Could not find schedule: %v", err))
			continue
		}

		var changes []string

		// Update time if provided
		if req.Time != "" {
			var hourVal, minuteVal int
			if _, err := fmt.Sscanf(req.Time, "%d:%d", &hourVal, &minuteVal); err != nil || hourVal < 0 || hourVal > 23 || minuteVal < 0 || minuteVal > 59 {
				log.Printf("[handler] processChatModifySchedule invalid time: %s", req.Time)
				results = append(results, fmt.Sprintf("- Invalid time %q for schedule on task \"%s\" (expected HH:MM, 00:00-23:59)", req.Time, task.Title))
				continue
			}
			// Rebuild RunAt with new time, preserving the date
			oldLocal := schedule.RunAt.Local()
			newRunAt := time.Date(oldLocal.Year(), oldLocal.Month(), oldLocal.Day(), hourVal, minuteVal, 0, 0, time.Local).UTC()
			schedule.RunAt = newRunAt
			changes = append(changes, fmt.Sprintf("time→%s", req.Time))
		}

		// Update repeat type if provided
		if req.Repeat != "" {
			switch strings.ToLower(req.Repeat) {
			case "once":
				schedule.RepeatType = models.RepeatOnce
			case "daily":
				schedule.RepeatType = models.RepeatDaily
			case "weekly":
				schedule.RepeatType = models.RepeatWeekly
			case "monthly":
				schedule.RepeatType = models.RepeatMonthly
			case "hours", "hourly":
				schedule.RepeatType = models.RepeatHours
			case "minutes":
				schedule.RepeatType = models.RepeatMinutes
			case "seconds":
				schedule.RepeatType = models.RepeatSeconds
			default:
				log.Printf("[handler] processChatModifySchedule unknown repeat type %q", req.Repeat)
				results = append(results, fmt.Sprintf("- Unknown repeat type %q for schedule on task \"%s\"", req.Repeat, task.Title))
				continue
			}
			changes = append(changes, fmt.Sprintf("repeat→%s", req.Repeat))
		}

		// Update interval if provided
		if req.Interval != nil {
			if *req.Interval < 1 {
				results = append(results, fmt.Sprintf("- Invalid interval %d for schedule on task \"%s\" (must be >= 1)", *req.Interval, task.Title))
				continue
			}
			schedule.RepeatInterval = *req.Interval
			changes = append(changes, fmt.Sprintf("interval→%d", *req.Interval))
		}

		// Update days (for weekly schedules)
		if len(req.Days) > 0 && schedule.RepeatType == models.RepeatWeekly {
			dayMap := map[string]time.Weekday{
				"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
				"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
				"sat": time.Saturday,
			}
			now := time.Now().Local()
			runAtLocal := schedule.RunAt.Local()
			bestOffset := 8
			for _, d := range req.Days {
				target, ok := dayMap[strings.ToLower(d)]
				if !ok {
					continue
				}
				offset := int(target - runAtLocal.Weekday())
				if offset < 0 {
					offset += 7
				}
				if offset == 0 && runAtLocal.Before(now) {
					offset = 7
				}
				if offset < bestOffset {
					bestOffset = offset
				}
			}
			if bestOffset < 8 {
				newRunAt := time.Date(now.Year(), now.Month(), now.Day(), runAtLocal.Hour(), runAtLocal.Minute(), 0, 0, time.Local)
				newRunAt = newRunAt.AddDate(0, 0, bestOffset)
				schedule.RunAt = newRunAt.UTC()
			}
			changes = append(changes, fmt.Sprintf("days→%s", strings.Join(req.Days, ",")))
		}

		// Update enabled status if provided
		if req.Enabled != nil {
			schedule.Enabled = *req.Enabled
			if *req.Enabled {
				changes = append(changes, "enabled→true")
			} else {
				changes = append(changes, "enabled→false")
			}
		}

		if len(changes) == 0 {
			results = append(results, fmt.Sprintf("- No changes specified for schedule on task \"%s\"", task.Title))
			continue
		}

		// Recompute next_run
		nextRun := schedule.ComputeNextRun(time.Now())
		schedule.NextRun = nextRun

		if err := h.scheduleRepo.Update(ctx, schedule); err != nil {
			log.Printf("[handler] processChatModifySchedule error updating schedule: %v", err)
			results = append(results, fmt.Sprintf("- Error updating schedule for task \"%s\": %v", task.Title, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Updated schedule for task \"%s\" [TASK_ID:%s]: %s", task.Title, task.ID, strings.Join(changes, ", ")))
		log.Printf("[handler] processChatModifySchedule updated schedule=%s task=%s changes=%s", schedule.ID, task.ID, strings.Join(changes, ", "))
	}

	if len(results) > 0 {
		output += "\n\n---\nSchedule Modify Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// resolveScheduleReference finds a schedule by schedule_id, or by task_id/title (returning the first schedule).
// Returns both the schedule and the associated task.
func (h *Handler) resolveScheduleReference(ctx context.Context, projectID, scheduleID, taskID, title string) (*models.Schedule, *models.Task, error) {
	// Direct schedule ID lookup
	if scheduleID != "" {
		schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
		if err != nil {
			return nil, nil, fmt.Errorf("error looking up schedule %s: %w", scheduleID, err)
		}
		if schedule == nil {
			return nil, nil, fmt.Errorf("schedule %s not found", scheduleID)
		}
		// Verify the schedule belongs to the project
		task, err := h.taskRepo.GetByID(ctx, schedule.TaskID)
		if err != nil || task == nil {
			return nil, nil, fmt.Errorf("task for schedule %s not found", scheduleID)
		}
		if task.ProjectID != projectID {
			return nil, nil, fmt.Errorf("schedule %s belongs to a different project", scheduleID)
		}
		return schedule, task, nil
	}

	// Resolve via task
	task, err := h.resolveTaskReference(ctx, projectID, taskID, title)
	if err != nil {
		return nil, nil, err
	}

	// Get schedules for the task
	schedules, err := h.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("error listing schedules for task %s: %w", task.ID, err)
	}
	if len(schedules) == 0 {
		return nil, nil, fmt.Errorf("no schedules found for task \"%s\"", task.Title)
	}

	// Return the first (most recent) schedule
	return &schedules[0], task, nil
}

// resolveTaskReference finds a task by ID or by title search within a project.
// Prefers ID lookup when available; falls back to title search.
func (h *Handler) resolveTaskReference(ctx context.Context, projectID, taskID, title string) (*models.Task, error) {
	if taskID != "" {
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

	if title != "" {
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

// processChatListPersonalities handles [LIST_PERSONALITIES] markers by returning all available personality presets.
func (h *Handler) processChatListPersonalities(ctx context.Context, execID, projectID, output string) string {
	if !service.HasListPersonalities(output) {
		return output
	}

	log.Printf("[handler] processChatListPersonalities exec=%s", execID)

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
		log.Printf("[handler] processChatListPersonalities error reading current personality: %v", err)
	}
	if current == "" {
		current = "default"
	}
	sb.WriteString(fmt.Sprintf("\nCurrent personality: **%s**\n", current))

	output += sb.String()
	return output
}

// processChatSetPersonality handles [SET_PERSONALITY] markers by changing the global personality setting.
func (h *Handler) processChatSetPersonality(ctx context.Context, execID, projectID, output string) string {
	requests := service.ParseSetPersonality(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[handler] processChatSetPersonality exec=%s found %d requests", execID, len(requests))

	var results []string
	for _, req := range requests {
		// Validate personality key against presets + custom
		valid := false
		var matchedName string
		for _, p := range service.AllPersonalitiesWithCustom(ctx, h.customPersonalityRepo) {
			if p.Key == req.Personality {
				valid = true
				matchedName = p.Name
				break
			}
		}
		if !valid {
			results = append(results, fmt.Sprintf("- Unknown personality %q. Use [LIST_PERSONALITIES] to see available options.", req.Personality))
			continue
		}

		if err := h.settingsRepo.Set(ctx, "personality", req.Personality); err != nil {
			log.Printf("[handler] processChatSetPersonality error: %v", err)
			results = append(results, fmt.Sprintf("- Error setting personality to %q: %v", req.Personality, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Personality changed to **%s** (`%s`)", matchedName, req.Personality))
		log.Printf("[handler] processChatSetPersonality set personality to %q", req.Personality)
	}

	if len(results) > 0 {
		output += "\n\n---\nPersonality Settings:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatListModels handles [LIST_MODELS] markers by returning available AI model configurations.
func (h *Handler) processChatListModels(ctx context.Context, execID, projectID, output string) string {
	if !service.HasListModels(output) {
		return output
	}

	log.Printf("[handler] processChatListModels exec=%s", execID)

	configs, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] processChatListModels error listing models: %v", err)
		output += "\n\n---\nModel Settings:\n- Error retrieving model configurations: " + err.Error()
		return output
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

	output += sb.String()
	return output
}

// processChatListAgents handles [LIST_AGENTS] markers by returning available agent definitions.
func (h *Handler) processChatListAgents(ctx context.Context, execID, projectID, output string) string {
	if !service.HasListAgents(output) {
		return output
	}
	log.Printf("[handler] processChatListAgents exec=%s", execID)

	if h.agentRepo == nil {
		output += "\n\n---\nConfigured Agents:\nAgent definitions not available.\n"
		return output
	}

	agents, err := h.agentRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] processChatListAgents error: %v", err)
		output += "\n\n---\nConfigured Agents:\n- Error: " + err.Error()
		return output
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
				a.Name, a.Description, modelStr, len(a.Skills), len(a.MCPServers)))
		}
	}
	output += sb.String()
	return output
}

// processChatViewSettings handles [VIEW_SETTINGS] markers by returning current app settings.
func (h *Handler) processChatViewSettings(ctx context.Context, execID, projectID, output string) string {
	if !service.HasViewSettings(output) {
		return output
	}

	log.Printf("[handler] processChatViewSettings exec=%s", execID)

	var sb strings.Builder
	sb.WriteString("\n\n---\nApp Settings:\n")

	// Personality
	personality, err := h.settingsRepo.Get(ctx, "personality")
	if err != nil {
		log.Printf("[handler] processChatViewSettings error reading personality: %v", err)
	}
	if personality == "" {
		personality = "default (no personality)"
	}
	sb.WriteString(fmt.Sprintf("- **Personality:** %s\n", personality))

	// Model count
	configs, err := h.llmConfigRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] processChatViewSettings error listing models: %v", err)
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
			log.Printf("[handler] processChatViewSettings error reading global workers: %v", err)
			sb.WriteString("- **Global max workers:** error reading\n")
		} else {
			sb.WriteString(fmt.Sprintf("- **Global max workers:** %d\n", globalMax))
		}
	} else {
		sb.WriteString("- **Global max workers:** not configured\n")
	}

	// Per-project worker limits
	projects, err := h.projectRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] processChatViewSettings error listing projects: %v", err)
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

	output += sb.String()
	return output
}

// processChatProjectInfo handles [PROJECT_INFO] markers by returning current project details and task counts.
func (h *Handler) processChatProjectInfo(ctx context.Context, execID, projectID, output string) string {
	if !service.HasProjectInfo(output) {
		return output
	}

	log.Printf("[handler] processChatProjectInfo exec=%s project=%s", execID, projectID)

	var sb strings.Builder
	sb.WriteString("\n\n---\nProject Info:\n")

	// Get project details
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		log.Printf("[handler] processChatProjectInfo error getting project: %v", err)
		sb.WriteString("- Error retrieving project details\n")
		output += sb.String()
		return output
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
		log.Printf("[handler] processChatProjectInfo error counting tasks: %v", err)
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

	output += sb.String()
	return output
}

// processChatListProjects handles [LIST_PROJECTS] markers by returning all available projects.
func (h *Handler) processChatListProjects(ctx context.Context, execID, projectID, output string) string {
	if !service.HasListProjects(output) {
		return output
	}

	log.Printf("[handler] processChatListProjects exec=%s project=%s", execID, projectID)

	projects, err := h.projectRepo.List(ctx)
	if err != nil {
		log.Printf("[handler] processChatListProjects error listing projects: %v", err)
		output += "\n\n---\nAvailable Projects:\n- Error retrieving projects: " + err.Error()
		return output
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

	output += sb.String()
	return output
}

// processChatListAlerts handles [LIST_ALERTS] markers by returning alerts for the current project.
func (h *Handler) processChatListAlerts(ctx context.Context, execID, projectID, output string) string {
	if !service.HasListAlerts(output) {
		return output
	}

	log.Printf("[handler] processChatListAlerts exec=%s project=%s", execID, projectID)

	if h.alertSvc == nil {
		output += "\n\n---\nAlert Results:\n- Alert service not available"
		return output
	}

	alerts, err := h.alertSvc.ListByProject(ctx, projectID, 50)
	if err != nil {
		log.Printf("[handler] processChatListAlerts error listing alerts: %v", err)
		output += "\n\n---\nAlert Results:\n- Error retrieving alerts: " + err.Error()
		return output
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

	output += sb.String()
	return output
}

// processChatCreateAlert handles [CREATE_ALERT] markers by creating new alerts.
func (h *Handler) processChatCreateAlert(ctx context.Context, execID, projectID, output string) string {
	requests := parseChatCreateAlert(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[handler] processChatCreateAlert exec=%s found %d requests", execID, len(requests))

	if h.alertSvc == nil {
		output += "\n\n---\nAlert Create Results:\n- Alert service not available"
		return output
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
			log.Printf("[handler] processChatCreateAlert error: %v", err)
			results = append(results, fmt.Sprintf("- Error creating alert %q: %v", req.Title, err))
			continue
		}

		results = append(results, fmt.Sprintf("- Created alert %q (id: `%s`, severity: %s)", req.Title, a.ID, severity))
	}

	if len(results) > 0 {
		output += "\n\n---\nAlert Create Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatDeleteAlert handles [DELETE_ALERT] markers by deleting alerts by ID.
func (h *Handler) processChatDeleteAlert(ctx context.Context, execID, projectID, output string) string {
	requests := parseChatDeleteAlert(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[handler] processChatDeleteAlert exec=%s found %d requests", execID, len(requests))

	if h.alertSvc == nil {
		output += "\n\n---\nAlert Delete Results:\n- Alert service not available"
		return output
	}

	var results []string
	for _, req := range requests {
		if err := h.alertSvc.Delete(ctx, req.AlertID); err != nil {
			log.Printf("[handler] processChatDeleteAlert error: %v", err)
			results = append(results, fmt.Sprintf("- Error deleting alert %q: %v", req.AlertID, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Deleted alert `%s`", req.AlertID))
	}

	if len(results) > 0 {
		output += "\n\n---\nAlert Delete Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatToggleAlert handles [TOGGLE_ALERT] markers by marking alerts as read.
func (h *Handler) processChatToggleAlert(ctx context.Context, execID, projectID, output string) string {
	requests := parseChatToggleAlert(output)
	if len(requests) == 0 {
		return output
	}

	log.Printf("[handler] processChatToggleAlert exec=%s found %d requests", execID, len(requests))

	if h.alertSvc == nil {
		output += "\n\n---\nAlert Toggle Results:\n- Alert service not available"
		return output
	}

	var results []string
	for _, req := range requests {
		if err := h.alertSvc.MarkRead(ctx, req.AlertID); err != nil {
			log.Printf("[handler] processChatToggleAlert error: %v", err)
			results = append(results, fmt.Sprintf("- Error marking alert %q as read: %v", req.AlertID, err))
			continue
		}
		results = append(results, fmt.Sprintf("- Marked alert `%s` as read", req.AlertID))
	}

	if len(results) > 0 {
		output += "\n\n---\nAlert Toggle Results:\n" + strings.Join(results, "\n")
	}

	return output
}

// processChatResponse handles all post-LLM processing for chat responses.
// This includes task creation, editing, execution, chat viewing, and message sending.
// It processes all marker types in sequence and updates the execution output in the database.
// Returns the final output with any marker processing results appended.
//
// The agents parameter may be empty if agent listing failed; in that case task creation
// will still proceed but without auto-assignment of agents.
//
// Early return: If output is empty, returns immediately without processing (no markers to parse).
func (h *Handler) processChatResponse(ctx context.Context, execID, projectID, output string, agents []models.LLMConfig) string {
	if output == "" {
		return output // No content to process
	}

	originalOutput := output

	// Process task creation markers
	if newOutput, attachmentCount := h.processChatTaskCreations(ctx, execID, projectID, output, agents); newOutput != output {
		output = newOutput
		if attachmentCount > 0 {
			log.Printf("[handler] processChatResponse exec=%s copied %d attachments to created tasks", execID, attachmentCount)
		}
	}

	// Process task edit markers
	if newOutput := h.processChatTaskEdits(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process task execution markers
	if newOutput := h.processChatTaskExecutions(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process thread view markers
	if newOutput := h.processViewThread(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process send-to-task markers
	if newOutput := h.processChatSendToTask(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process schedule task markers
	if newOutput := h.processChatScheduleTask(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process delete schedule markers
	if newOutput := h.processChatDeleteSchedule(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process modify schedule markers
	if newOutput := h.processChatModifySchedule(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list personalities markers
	if newOutput := h.processChatListPersonalities(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process set personality markers
	if newOutput := h.processChatSetPersonality(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list models markers
	if newOutput := h.processChatListModels(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list agents markers
	if newOutput := h.processChatListAgents(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process view settings markers
	if newOutput := h.processChatViewSettings(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process project info markers
	if newOutput := h.processChatProjectInfo(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process list projects markers
	if newOutput := h.processChatListProjects(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Process alert markers
	if newOutput := h.processChatListAlerts(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	if newOutput := h.processChatCreateAlert(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	if newOutput := h.processChatDeleteAlert(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	if newOutput := h.processChatToggleAlert(ctx, execID, projectID, output); newOutput != output {
		output = newOutput
	}

	// Detect missing markers: warn when the LLM appears to promise an action
	// but didn't actually output the corresponding marker block
	if warnings := service.DetectMissingMarkers(originalOutput); len(warnings) > 0 {
		for _, w := range warnings {
			log.Printf("[handler] processChatResponse exec=%s WARNING: LLM promised %s action (matched %q) but no %s marker found in output",
				execID, w.Action, w.MatchedHint, w.MarkerName)
		}
		// Append a user-visible warning so the user knows the action didn't execute
		output += "\n\n---\n**Warning:** The assistant appeared to promise an action but did not include the required marker. The following actions were NOT performed:\n"
		for _, w := range warnings {
			output += fmt.Sprintf("- %s (no %s marker found)\n", w.Action, w.MarkerName)
		}
		output += "\nPlease retry your request."
	}

	// Update execution output if modified
	if output != originalOutput {
		if updateErr := h.execRepo.UpdateOutput(ctx, execID, output); updateErr != nil {
			log.Printf("[handler] processChatResponse error updating output: %v", updateErr)
		}
	}

	return output
}

// buildChatContext builds the context string for chat prompts, including task, model, and schedule information.
// Returns a formatted string with current tasks (excluding chat tasks), available models, and schedule details.
// Delegates to the shared service.BuildChatContext so /chat and Telegram produce identical context.
func (h *Handler) buildChatContext(ctx context.Context, projectID string, availableModels []models.LLMConfig) string {
	existingTasks, err := h.taskSvc.ListByProject(ctx, projectID, "")
	if err != nil {
		log.Printf("[handler] buildChatContext error listing tasks for project %s: %v", projectID, err)
		existingTasks = []models.Task{}
	}

	schedules, err := h.scheduleRepo.ListByProject(ctx, projectID)
	if err != nil {
		log.Printf("[handler] buildChatContext error listing schedules for project %s: %v", projectID, err)
		schedules = []models.Schedule{}
	}

	return service.BuildChatContext(existingTasks, availableModels, schedules, time.Now())
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

// extractTaskIDsFromOutput parses [TASK_ID:xxx] markers from AI output and returns
// a slice of task IDs. This is used to identify which tasks were created by a chat
// execution without querying the database.
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
