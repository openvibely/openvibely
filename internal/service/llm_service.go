package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmnormalize "github.com/openvibely/openvibely/internal/llm/normalize"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/pkg/anthropic_client"
)

// buildAttachmentInstructionsForCLI is a helper that builds CLI-specific attachment
// instructions, separating text files (which can be read) from image files (which cannot).
// Exposed for testing.
func buildAttachmentInstructionsForCLI(attachments []models.Attachment) string {
	return llmprompt.BuildAttachmentInstructions(attachments)
}

// LLMCaller abstracts model provider calls so tests can inject a mock
// instead of hitting real APIs or spawning CLI subprocesses.
type LLMCaller interface {
	CallModel(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (output, textOnly string, tokens int, err error)
}

type LLMService struct {
	llmConfigRepo         *repository.LLMConfigRepo
	execRepo              *repository.ExecutionRepo
	taskRepo              *repository.TaskRepo
	projectRepo           *repository.ProjectRepo
	scheduleRepo          *repository.ScheduleRepo
	attachmentRepo        *repository.AttachmentRepo
	agentRepo             *repository.AgentRepo
	alertSvc              *AlertService
	taskSvc               *TaskService
	worktreeSvc           *WorktreeService
	telegramSvc           *TelegramService
	slackSvc              *SlackService
	llmCaller             LLMCaller
	providerAdapters      map[models.LLMProvider]ProviderAdapter
	routing               *agentRoutingStrategy
	fileChangeBroadcaster *events.FileChangeBroadcaster
}

func NewLLMService(llmConfigRepo *repository.LLMConfigRepo, execRepo *repository.ExecutionRepo, taskRepo *repository.TaskRepo, projectRepo *repository.ProjectRepo, scheduleRepo *repository.ScheduleRepo, attachmentRepo *repository.AttachmentRepo) *LLMService {
	s := &LLMService{
		llmConfigRepo:  llmConfigRepo,
		execRepo:       execRepo,
		taskRepo:       taskRepo,
		projectRepo:    projectRepo,
		scheduleRepo:   scheduleRepo,
		attachmentRepo: attachmentRepo,
	}
	s.initProviderAdapters()
	s.routing = newAgentRoutingStrategy(s)
	return s
}

// isTestMode detects whether the code is running inside a Go test binary.
// Checks both GO_TESTING env var (set by testutil.init()) and the binary name
// suffix (.test), which Go always uses for compiled test binaries.
func isTestMode() bool {
	if os.Getenv("GO_TESTING") != "" {
		return true
	}
	return strings.HasSuffix(os.Args[0], ".test") ||
		strings.Contains(os.Args[0], "/_test/")
}

// SetAlertService sets the alert service for creating alerts on task failures.
// Called after construction to avoid circular dependencies.
func (s *LLMService) SetAlertService(alertSvc *AlertService) {
	s.alertSvc = alertSvc
}

// SetTaskService sets the task service for creating tasks from agent output.
// Called after construction to avoid circular dependencies
// (LLMService -> TaskService -> WorkerService -> LLMService).
func (s *LLMService) SetTaskService(taskSvc *TaskService) {
	s.taskSvc = taskSvc
}

// SetWorktreeService sets the worktree service for task isolation.
func (s *LLMService) SetWorktreeService(wts *WorktreeService) {
	s.worktreeSvc = wts
}

// SetTelegramService sets the Telegram service for sending task completion notifications.
func (s *LLMService) SetTelegramService(ts *TelegramService) {
	s.telegramSvc = ts
}

// SetSlackService sets the Slack service for sending task completion notifications.
func (s *LLMService) SetSlackService(ss *SlackService) {
	s.slackSvc = ss
}

// SetFileChangeBroadcaster sets the file change broadcaster for real-time file change updates.
func (s *LLMService) SetFileChangeBroadcaster(fcb *events.FileChangeBroadcaster) {
	s.fileChangeBroadcaster = fcb
}

// SetAgentRepo sets the agent repository for resolving agent definitions on tasks.
func (s *LLMService) SetAgentRepo(repo *repository.AgentRepo) {
	s.agentRepo = repo
}

// SetLLMCaller overrides the default model calling behavior.
// In tests, pass a mock to prevent real API/CLI calls.
func (s *LLMService) SetLLMCaller(c LLMCaller) {
	s.llmCaller = c
}

func (s *LLMService) ensureRoutingStrategy() *agentRoutingStrategy {
	if s.routing == nil {
		if s.providerAdapters == nil {
			s.initProviderAdapters()
		}
		s.routing = newAgentRoutingStrategy(s)
	}
	return s.routing
}

func (s *LLMService) ExecuteTask(ctx context.Context, task models.Task) (*models.Execution, error) {
	log.Printf("[agent-svc] ExecuteTask task=%s title=%q agent_id=%v", task.ID, task.Title, task.AgentID)

	var agent *models.LLMConfig
	var err error

	// If task has a specific agent assigned, use it; otherwise use default
	if task.AgentID != nil && *task.AgentID != "" {
		agent, err = s.llmConfigRepo.GetByID(ctx, *task.AgentID)
		if err != nil {
			log.Printf("[agent-svc] ExecuteTask error getting agent %s: %v", *task.AgentID, err)
			return nil, fmt.Errorf("getting agent: %w", err)
		}
		if agent == nil {
			log.Printf("[agent-svc] ExecuteTask agent %s not found, falling back to default", *task.AgentID)
			// Fall back to project default, then global default
			agent, err = s.getDefaultAgentForTask(ctx, task.ProjectID)
			if err != nil {
				log.Printf("[agent-svc] ExecuteTask error getting default agent: %v", err)
				return nil, fmt.Errorf("getting default agent: %w", err)
			}
		} else {
			log.Printf("[agent-svc] ExecuteTask using assigned agent=%s provider=%s model=%s", agent.Name, agent.Provider, agent.Model)
		}
	} else {
		// Try project-level default agent first, then fall back to global default
		agent, err = s.getDefaultAgentForTask(ctx, task.ProjectID)
		if err != nil {
			log.Printf("[agent-svc] ExecuteTask error getting default agent: %v", err)
			return nil, fmt.Errorf("getting default agent: %w", err)
		}
		if agent != nil {
			log.Printf("[agent-svc] ExecuteTask using default agent=%s provider=%s model=%s", agent.Name, agent.Provider, agent.Model)
		}
	}

	if agent == nil {
		log.Printf("[agent-svc] ExecuteTask no agent available")
		return nil, fmt.Errorf("no agent configured")
	}

	return s.ExecuteTaskWithAgent(ctx, task, *agent)
}

// getDefaultAgentForTask returns the appropriate default agent for a task.
// It checks the project's default agent first, then falls back to the global default.
func (s *LLMService) getDefaultAgentForTask(ctx context.Context, projectID string) (*models.LLMConfig, error) {
	// Try project-level default agent
	if projectID != "" && s.projectRepo != nil {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err != nil {
			log.Printf("[agent-svc] getDefaultAgentForTask error getting project %s: %v", projectID, err)
		} else if project != nil && project.DefaultAgentConfigID != nil && *project.DefaultAgentConfigID != "" {
			agent, err := s.llmConfigRepo.GetByID(ctx, *project.DefaultAgentConfigID)
			if err != nil {
				log.Printf("[agent-svc] getDefaultAgentForTask error getting project default agent %s: %v", *project.DefaultAgentConfigID, err)
			} else if agent != nil {
				log.Printf("[agent-svc] getDefaultAgentForTask using project default agent=%s for project=%s", agent.Name, projectID)
				return agent, nil
			}
		}
	}

	// Fall back to global default
	return s.llmConfigRepo.GetDefault(ctx)
}

func (s *LLMService) ExecuteTaskWithAgent(ctx context.Context, task models.Task, agent models.LLMConfig) (*models.Execution, error) {
	log.Printf("[agent-svc] ExecuteTaskWithAgent task=%s agent=%s model=%s", task.ID, agent.Name, agent.Model)

	// Atomically claim the task (only succeeds if status is pending)
	claimed, err := s.taskRepo.ClaimTask(ctx, task.ID)
	if err != nil {
		log.Printf("[agent-svc] ExecuteTaskWithAgent error claiming task: %v", err)
		return nil, fmt.Errorf("claiming task: %w", err)
	}
	if !claimed {
		log.Printf("[agent-svc] ExecuteTaskWithAgent task=%s not pending (already running/completed), skipping", task.ID)
		return nil, nil
	}
	log.Printf("[agent-svc] ExecuteTaskWithAgent task=%s status -> running", task.ID)

	// Create execution record
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    task.Prompt,
	}
	if err := s.execRepo.Create(ctx, exec); err != nil {
		log.Printf("[agent-svc] ExecuteTaskWithAgent error creating execution: %v", err)
		return nil, fmt.Errorf("creating execution: %w", err)
	}
	log.Printf("[agent-svc] ExecuteTaskWithAgent execution=%s created, calling LLM...", exec.ID)

	// Load attachments for the task
	attachments, err := s.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		log.Printf("[agent-svc] ExecuteTaskWithAgent error loading attachments: %v", err)
		return nil, fmt.Errorf("loading attachments: %w", err)
	}
	log.Printf("[agent-svc] ExecuteTaskWithAgent loaded %d attachments for task=%s", len(attachments), task.ID)

	// Vision-aware agent override: if the task has image attachments and the
	// current agent doesn't support vision (e.g., Anthropic CLI which can't
	// send images as multimodal content), try to find a vision-capable agent.
	// API key and OAuth agents support vision natively via multimodal content blocks.
	visionDecision := s.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, task.Prompt, attachments, agent, "ExecuteTaskWithAgent", task.ID)
	agent = visionDecision.Agent
	log.Printf("[agent-svc] ExecuteTaskWithAgent vision routing changed=%v reason=%s detail=%q selected_agent=%s selected_provider=%s",
		visionDecision.Changed, visionDecision.Reason, visionDecision.Detail, agent.Name, agent.Provider)

	// Look up the project's repo path to use as the working directory
	// for the CLI subprocess. Without this, the agent runs in the OpenVibely
	// server directory instead of the project's configured directory.
	workDir := ""
	repoDir := "" // original repo dir (for worktree setup and post-execution)
	if task.ProjectID != "" && s.projectRepo != nil {
		project, projErr := s.projectRepo.GetByID(ctx, task.ProjectID)
		if projErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error getting project for workDir: %v", projErr)
		} else if project != nil && project.RepoPath != "" {
			if _, statErr := os.Stat(project.RepoPath); os.IsNotExist(statErr) {
				errMsg := fmt.Sprintf("project repo path %q does not exist on disk", project.RepoPath)
				if project.RepoURL != "" {
					errMsg += fmt.Sprintf(" (cloned from %s). This typically happens after a container restart when PROJECT_REPO_ROOT is not on a persistent volume. Re-clone the project or fix your volume mounts.", project.RepoURL)
				} else {
					errMsg += ". Ensure the local repo path is mounted into the container."
				}
				log.Printf("[agent-svc] ExecuteTaskWithAgent ERROR: %s", errMsg)
				if completeErr := s.execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", errMsg, 0, 0); completeErr != nil {
					log.Printf("[agent-svc] ExecuteTaskWithAgent error completing execution after missing repo: %v", completeErr)
				}
				if statusErr := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed); statusErr != nil {
					log.Printf("[agent-svc] ExecuteTaskWithAgent error updating task status after missing repo: %v", statusErr)
				}
				return exec, fmt.Errorf("repo path missing: %s", errMsg)
			}
			repoDir = project.RepoPath
			workDir = project.RepoPath
			log.Printf("[agent-svc] ExecuteTaskWithAgent using project workDir=%s", workDir)
		}
	}

	// Set up git worktree for task isolation (if repo supports it and it's not a chat task)
	if s.worktreeSvc != nil && repoDir != "" && task.Category != models.CategoryChat && IsGitRepo(repoDir) {
		wtPath, wtBranch, wtErr := s.worktreeSvc.SetupWorktree(ctx, &task, repoDir)
		if wtErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent worktree setup failed (using main repo): %v", wtErr)
		} else if wtPath != "" {
			workDir = wtPath
			task.WorktreePath = wtPath
			task.WorktreeBranch = wtBranch
			log.Printf("[agent-svc] ExecuteTaskWithAgent using worktree workDir=%s branch=%s", workDir, wtBranch)

			if syncErr := s.worktreeSvc.SyncWorktreeFromMainAtStart(ctx, &task, repoDir); syncErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent startup worktree auto-merge failed task=%s: %v", task.ID, syncErr)
				if completeErr := s.execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", syncErr.Error(), 0, 0); completeErr != nil {
					log.Printf("[agent-svc] ExecuteTaskWithAgent error completing execution after startup auto-merge failure: %v", completeErr)
				}
				if statusErr := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed); statusErr != nil {
					log.Printf("[agent-svc] ExecuteTaskWithAgent error updating task status after startup auto-merge failure: %v", statusErr)
				}
				if task.Category == models.CategoryActive {
					if categoryErr := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); categoryErr != nil {
						log.Printf("[agent-svc] ExecuteTaskWithAgent error moving startup-auto-merge-failed task to completed category: %v", categoryErr)
					} else {
						log.Printf("[agent-svc] ExecuteTaskWithAgent moved startup-auto-merge-failed task=%s to completed category", task.ID)
					}
				}
				if s.alertSvc != nil {
					if alertErr := s.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, task.ID, exec.ID, task.Title, syncErr.Error()); alertErr != nil {
						log.Printf("[agent-svc] ExecuteTaskWithAgent error creating startup auto-merge failure alert: %v", alertErr)
					}
				}
				exec.Status = models.ExecFailed
				exec.ErrorMessage = syncErr.Error()
				if s.telegramSvc != nil {
					s.telegramSvc.SendTaskCompletionNotification(ctx, task, "", syncErr.Error())
				}
				if s.slackSvc != nil {
					s.slackSvc.SendTaskCompletionNotification(ctx, task, "", syncErr.Error())
				}
				return exec, fmt.Errorf("startup worktree auto-merge failed: %w", syncErr)
			}
		}
	}

	// Load project instructions (AGENTS.md) from the working directory
	projectInstructions := loadProjectInstructions(workDir)
	if projectInstructions != "" {
		log.Printf("[agent-svc] ExecuteTaskWithAgent loaded AGENTS.md (%d bytes) from %s", len(projectInstructions), workDir)
	}

	// Start background diff snapshot broadcaster (if file change broadcaster is configured)
	var stopDiffBroadcast chan struct{}
	if s.fileChangeBroadcaster != nil && workDir != "" {
		stopDiffBroadcast = make(chan struct{})
		go s.broadcastDiffSnapshots(ctx, task.ID, exec.ID, workDir, repoDir, task.WorktreeBranch, task.MergeTargetBranch, stopDiffBroadcast)
	}

	// Resolve agent definition if set
	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && s.agentRepo != nil {
		if ad, adErr := s.agentRepo.GetByID(ctx, *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
			log.Printf("[agent-svc] ExecuteTaskWithAgent using agent definition=%s (%s)", ad.Name, ad.ID)
		}
	}

	// Call the LLM
	start := time.Now()
	output, textOnlyOutput, tokensUsed, err := s.callLLM(ctx, task.Prompt, attachments, agent, exec.ID, workDir, projectInstructions, agentDef)
	durationMs := time.Since(start).Milliseconds()

	// Stop diff snapshot broadcaster
	if stopDiffBroadcast != nil {
		close(stopDiffBroadcast)
	}

	if err != nil {
		// Distinguish between user cancellation and actual failures.
		// When a task is cancelled, the context is cancelled which kills the CLI process.
		// Use background context for DB updates since the task context may be cancelled.
		bgCtx := context.Background()
		if ctx.Err() == context.Canceled {
			log.Printf("[agent-svc] ExecuteTaskWithAgent CANCELLED task=%s duration=%dms",
				task.ID, durationMs)
			// Pass output (may contain partial streamed content) so Complete preserves it
			if completeErr := s.execRepo.Complete(bgCtx, exec.ID, models.ExecCancelled, output, "task cancelled by user", 0, durationMs); completeErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error completing cancelled execution: %v", completeErr)
			}
			// Task status is already set to cancelled by CancelTask, but set it again
			// in case the cancellation came from a different path (e.g., server shutdown).
			if statusErr := s.taskRepo.UpdateStatus(bgCtx, task.ID, models.StatusCancelled); statusErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error updating task status to cancelled: %v", statusErr)
			}
			exec.Status = models.ExecCancelled
			exec.ErrorMessage = "task cancelled by user"
			return exec, fmt.Errorf("task cancelled")
		}

		log.Printf("[agent-svc] ExecuteTaskWithAgent LLM call FAILED task=%s duration=%dms error=%v",
			task.ID, durationMs, err)
		// For max_tokens failures, preserve the partial output so the user can see
		// what work was done before the token limit was hit. For other failures,
		// clear the output — the task detail should only show the prompt and error.
		failedOutput := ""
		if output != "" {
			failedOutput = output
			log.Printf("[agent-svc] ExecuteTaskWithAgent max_tokens failure, preserving partial output (%d bytes) task=%s", len(output), task.ID)
		}
		if completeErr := s.execRepo.Complete(bgCtx, exec.ID, models.ExecFailed, failedOutput, err.Error(), tokensUsed, durationMs); completeErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
		}
		if statusErr := s.taskRepo.UpdateStatus(bgCtx, task.ID, models.StatusFailed); statusErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error updating task status to failed: %v", statusErr)
		}
		// Move failed tasks to completed category (same as successful tasks)
		if task.Category == models.CategoryActive {
			if categoryErr := s.taskRepo.UpdateCategory(bgCtx, task.ID, models.CategoryCompleted); categoryErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error moving failed task to completed category: %v", categoryErr)
			} else {
				log.Printf("[agent-svc] ExecuteTaskWithAgent moved failed task=%s to completed category", task.ID)
			}
		}
		// Create an alert for the failed task
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskFailedAlert(bgCtx, task.ProjectID, task.ID, exec.ID, task.Title, err.Error()); alertErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error creating alert: %v", alertErr)
			}
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = err.Error()
		// Send Telegram notification for tasks created via Telegram
		if s.telegramSvc != nil {
			s.telegramSvc.SendTaskCompletionNotification(bgCtx, task, "", err.Error())
		}
		if s.slackSvc != nil {
			s.slackSvc.SendTaskCompletionNotification(bgCtx, task, "", err.Error())
		}
		return exec, fmt.Errorf("calling LLM: %w", err)
	}

	log.Printf("[agent-svc] ExecuteTaskWithAgent LLM call SUCCESS task=%s tokens=%d duration=%dms output_len=%d",
		task.ID, tokensUsed, durationMs, len(output))

	// Check for agent-reported failure/followup markers in the output.
	// The agent is instructed to end its response with [STATUS: FAILED | reason]
	// or [STATUS: NEEDS_FOLLOWUP | reason] when the task fails or needs attention.
	// Use textOnlyOutput (model text only, no tool results/thinking) to avoid
	// false positives from source code containing STATUS markers in tool results.
	statusCheckOutput := textOnlyOutput
	if statusCheckOutput == "" {
		statusCheckOutput = output
	}
	if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: FAILED |"); found {
		log.Printf("[agent-svc] ExecuteTaskWithAgent agent reported STATUS FAILED task=%s reason=%q", task.ID, reason)
		// Clear the execution output on failure — only keep the prompt and error message.
		if completeErr := s.execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", reason, tokensUsed, durationMs); completeErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
		}
		if statusErr := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed); statusErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error updating task status to failed: %v", statusErr)
		}
		// Move failed tasks to completed category (same as successful tasks)
		if task.Category == models.CategoryActive {
			if categoryErr := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); categoryErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error moving failed task to completed category: %v", categoryErr)
			} else {
				log.Printf("[agent-svc] ExecuteTaskWithAgent moved failed task=%s to completed category", task.ID)
			}
		}
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, task.ID, exec.ID, task.Title, reason); alertErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error creating alert: %v", alertErr)
			}
		}
		exec.Status = models.ExecFailed
		exec.ErrorMessage = reason
		// Send Telegram notification for tasks created via Telegram
		if s.telegramSvc != nil {
			s.telegramSvc.SendTaskCompletionNotification(ctx, task, "", reason)
		}
		if s.slackSvc != nil {
			s.slackSvc.SendTaskCompletionNotification(ctx, task, "", reason)
		}
		return exec, nil
	}

	// NOTE: detectToolFailures was previously used here to scan for non-zero exit
	// codes and fail the task. This was removed because both the CLI path (Claude Code)
	// and OAuth agentic path handle tool errors internally — the model sees the error,
	// can retry or fix the issue, and continues working. Intermediate command failures
	// should not kill the task. The model uses [STATUS: FAILED | reason] to explicitly
	// report task failure when it determines the task cannot be completed.

	// Process task creation markers from the agent's output.
	// Webhook-created tasks must remain one-task-per-webhook-call, so do not
	// allow marker-driven fan-out during their execution.
	if s.taskSvc != nil {
		if task.CreatedVia == models.TaskOriginWebhook {
			log.Printf("[agent-svc] ExecuteTaskWithAgent skipping marker task creation for webhook-origin task=%s", task.ID)
		} else {
			taskRequests := ParseTaskCreations(output)
			if len(taskRequests) > 0 {
				log.Printf("[agent-svc] ExecuteTaskWithAgent task=%s found %d task creation requests", task.ID, len(taskRequests))
				summary := ExecuteTaskCreations(ctx, taskRequests, task.ProjectID, s.taskSvc)
				if summary != "" {
					output += summary
				}
			}
		}
	}

	// Record success
	if completeErr := s.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, output, "", tokensUsed, durationMs); completeErr != nil {
		log.Printf("[agent-svc] ExecuteTaskWithAgent error completing execution: %v", completeErr)
	}
	if statusErr := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusCompleted); statusErr != nil {
		log.Printf("[agent-svc] ExecuteTaskWithAgent error updating task status to completed: %v", statusErr)
	}

	// Capture git diff of changes made during execution
	if workDir != "" {
		// For worktree tasks, capture the diff between the task branch and target
		if task.WorktreePath != "" && task.WorktreeBranch != "" {
			targetBranch := task.MergeTargetBranch
			if targetBranch == "" {
				targetBranch = GetDefaultBranch(repoDir)
			}
			// First commit changes in the worktree
			CommitWorktreeChanges(task.WorktreePath, fmt.Sprintf("Task completed: %s", task.Title))
			diffOutput := GetWorktreeDiff(repoDir, task.WorktreeBranch, targetBranch)
			if diffOutput != "" {
				if diffErr := s.execRepo.UpdateDiffOutput(ctx, exec.ID, diffOutput); diffErr != nil {
					log.Printf("[agent-svc] ExecuteTaskWithAgent error saving worktree diff output: %v", diffErr)
				} else {
					exec.DiffOutput = diffOutput
					log.Printf("[agent-svc] ExecuteTaskWithAgent captured worktree diff output for exec=%s (%d bytes)", exec.ID, len(diffOutput))
				}
			}
		} else if diffOutput := s.CaptureGitDiff(workDir); diffOutput != "" {
			if diffErr := s.execRepo.UpdateDiffOutput(ctx, exec.ID, diffOutput); diffErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error saving diff output: %v", diffErr)
			} else {
				exec.DiffOutput = diffOutput
				log.Printf("[agent-svc] ExecuteTaskWithAgent captured diff output for exec=%s (%d bytes)", exec.ID, len(diffOutput))
			}
		}
	}

	// Handle worktree post-execution (auto-merge, status updates)
	if s.worktreeSvc != nil && task.WorktreePath != "" && repoDir != "" {
		s.worktreeSvc.HandlePostExecution(ctx, &task, repoDir)
	}

	// Check for follow-up marker (task still completed, but alert created)
	if reason, found := llmoutput.ExtractMarker(statusCheckOutput, "[STATUS: NEEDS_FOLLOWUP |"); found {
		log.Printf("[agent-svc] ExecuteTaskWithAgent agent reported STATUS NEEDS_FOLLOWUP task=%s reason=%q", task.ID, reason)
		if s.alertSvc != nil {
			if alertErr := s.alertSvc.CreateTaskNeedsFollowupAlert(ctx, task.ProjectID, task.ID, exec.ID, task.Title, reason); alertErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error creating followup alert: %v", alertErr)
			}
		}
	}

	// Automatically move completed tasks from active category to completed category
	if task.Category == models.CategoryActive {
		if categoryErr := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); categoryErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error moving task to completed category: %v", categoryErr)
		} else {
			log.Printf("[agent-svc] ExecuteTaskWithAgent moved task=%s to completed category", task.ID)
		}
	}
	// Automatically move completed scheduled tasks with RepeatOnce to completed category
	if task.Category == models.CategoryScheduled {
		schedules, err := s.scheduleRepo.ListByTask(ctx, task.ID)
		if err != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error getting schedules for task %s: %v", task.ID, err)
		} else if len(schedules) > 0 && schedules[0].RepeatType == models.RepeatOnce {
			if categoryErr := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryCompleted); categoryErr != nil {
				log.Printf("[agent-svc] ExecuteTaskWithAgent error moving RepeatOnce task to completed category: %v", categoryErr)
			} else {
				log.Printf("[agent-svc] ExecuteTaskWithAgent moved RepeatOnce task=%s to completed category", task.ID)
			}
		}
	}
	exec.Status = models.ExecCompleted
	exec.Output = output
	exec.TokensUsed = tokensUsed
	exec.DurationMs = durationMs

	// Trigger task chaining if configured
	if s.taskSvc != nil {
		if chainErr := s.triggerTaskChain(ctx, task, textOnlyOutput); chainErr != nil {
			log.Printf("[agent-svc] ExecuteTaskWithAgent error triggering task chain: %v", chainErr)
		}
	}

	// Send Telegram notification for tasks created via Telegram
	if s.telegramSvc != nil {
		s.telegramSvc.SendTaskCompletionNotification(ctx, task, output, "")
	}
	if s.slackSvc != nil {
		s.slackSvc.SendTaskCompletionNotification(ctx, task, output, "")
	}

	return exec, nil
}

// broadcastDiffSnapshots periodically captures and broadcasts git diff snapshots
// while a task is executing, allowing real-time file change monitoring.
func (s *LLMService) broadcastDiffSnapshots(ctx context.Context, taskID, execID, workDir, repoDir, worktreeBranch, mergeTargetBranch string, stop <-chan struct{}) {
	captureDiff := func() string {
		if repoDir != "" && worktreeBranch != "" {
			targetBranch := mergeTargetBranch
			if targetBranch == "" {
				targetBranch = GetDefaultBranch(repoDir)
			}
			// Capture committed branch diff + uncommitted working directory changes
			// without auto-committing (avoids polluting history with periodic commits).
			return GetWorktreeDiffWithUncommitted(repoDir, worktreeBranch, targetBranch, workDir)
		}
		return s.CaptureGitDiff(workDir)
	}

	ticker := time.NewTicker(2 * time.Second) // Capture diff every 2 seconds
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			// Final snapshot on completion
			if diffOutput := captureDiff(); diffOutput != "" {
				s.fileChangeBroadcaster.Publish(events.FileChangeEvent{
					Type:       events.DiffSnapshot,
					TaskID:     taskID,
					ExecID:     execID,
					DiffOutput: diffOutput,
					Timestamp:  time.Now().UnixMilli(),
				})
			}
			return

		case <-ctx.Done():
			return

		case <-ticker.C:
			diffOutput := captureDiff()
			if diffOutput != "" {
				// Update execution's diff output in database for realtime UI refresh.
				// This allows the Changes tab to show in-progress diffs when it polls
				// via GET /tasks/:taskId/changes, not just completed execution diffs.
				if err := s.execRepo.UpdateDiffOutput(ctx, execID, diffOutput); err != nil {
					log.Printf("[diff-broadcast] error updating execution diff output: %v", err)
				} else {
					log.Printf("[diff-broadcast] updated execution diff for realtime UI (task=%s exec=%s, %d bytes)", taskID, execID, len(diffOutput))
				}
				// Broadcast via SSE to connected clients
				s.fileChangeBroadcaster.Publish(events.FileChangeEvent{
					Type:       events.DiffSnapshot,
					TaskID:     taskID,
					ExecID:     execID,
					DiffOutput: diffOutput,
					Timestamp:  time.Now().UnixMilli(),
				})
			}
		}
	}
}

// CallAgentDirect calls the agent directly with a message, without task execution overhead
func (s *LLMService) CallAgentDirect(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	return s.callAgentDirect(ctx, message, attachments, agent, workDir, false)
}

// CallAgentDirectNoTools calls the agent directly and explicitly suppresses
// tool/plugin execution. Use this for strict JSON-generation helpers.
func (s *LLMService) CallAgentDirectNoTools(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string) (string, int, error) {
	return s.callAgentDirect(ctx, message, attachments, agent, workDir, true)
}

func (s *LLMService) callAgentDirect(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	log.Printf("[agent-svc] CallAgentDirect agent=%s model=%s message_len=%d workDir=%s disable_tools=%v", agent.Name, agent.Model, len(message), workDir, disableTools)

	adapter, err := s.ensureRoutingStrategy().resolveAdapter(agent.Provider)
	if err != nil {
		return "", 0, err
	}
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:          ctx,
		Operation:    llmcontracts.OperationDirect,
		Message:      message,
		Attachments:  attachments,
		Agent:        agent,
		WorkDir:      workDir,
		DisableTools: disableTools,
	})
	if err != nil {
		return "", 0, err
	}
	res, err := adapter.Call(req)
	if err != nil {
		if res.StopReason == "max_tokens" {
			return res.Output, res.Usage.TotalTokens, err
		}
		return "", 0, err
	}
	return res.Output, res.Usage.TotalTokens, nil
}

// CallAgentDirectStreaming calls the agent with streaming support, writing output to DB in real-time.
// chatHistory provides prior conversation turns for context (nil for non-chat calls).
// chatSystemContext is optional additional context appended to the chat system prompt (e.g., task list).
// workDir is the project's repo path used as the working directory for CLI subprocesses.
// isTaskFollowup when true uses the coding agent system prompt instead of task management prompt.
func (s *LLMService) CallAgentDirectStreamingDetailed(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, agentDef *models.Agent, isTaskFollowup ...bool) (llmcontracts.AgentResult, error) {
	followup := len(isTaskFollowup) > 0 && isTaskFollowup[0]
	log.Printf("[agent-svc] CallAgentDirectStreaming agent=%s model=%s message_len=%d exec=%s history=%d workDir=%s isTaskFollowup=%v", agent.Name, agent.Model, len(message), execID, len(chatHistory), workDir, followup)
	chatMode := llmcontracts.ChatModeFromContext(ctx)
	if followup {
		chatMode = models.ChatModeOrchestrate
	}

	visionDecision := s.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, message, attachments, agent, "CallAgentDirectStreaming", "")
	agent = visionDecision.Agent
	log.Printf("[agent-svc] CallAgentDirectStreaming vision routing changed=%v reason=%s detail=%q selected_agent=%s selected_provider=%s",
		visionDecision.Changed, visionDecision.Reason, visionDecision.Detail, agent.Name, agent.Provider)
	adapter, err := s.ensureRoutingStrategy().resolveAdapter(agent.Provider)
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:               ctx,
		Operation:         llmcontracts.OperationStreaming,
		Message:           message,
		Attachments:       attachments,
		Agent:             agent,
		ExecID:            execID,
		ChatHistory:       chatHistory,
		ChatMode:          chatMode,
		ChatSystemContext: chatSystemContext,
		WorkDir:           workDir,
		AgentDefinition:   agentDef,
		Followup:          followup,
	})
	if err != nil {
		return llmcontracts.AgentResult{}, err
	}
	res, err := adapter.Call(req)
	if err != nil {
		if res.StopReason == "max_tokens" {
			return res, err
		}
		return llmcontracts.AgentResult{}, err
	}
	return res, nil
}

func (s *LLMService) CallAgentDirectStreaming(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, isTaskFollowup ...bool) (string, int, error) {
	res, err := s.CallAgentDirectStreamingDetailed(ctx, message, attachments, agent, execID, chatHistory, chatSystemContext, workDir, nil, isTaskFollowup...)
	return res.Output, res.Usage.TotalTokens, err
}

// loadProjectInstructions reads AGENTS.md from the given directory.
// Returns the file content or empty string if the file doesn't exist or can't be read.
func loadProjectInstructions(workDir string) string {
	if workDir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(workDir, "AGENTS.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *LLMService) callLLM(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, projectInstructions string, agentDef ...*models.Agent) (string, string, int, error) {
	log.Printf("[agent-svc] callLLM provider=%s model=%s prompt_len=%d attachments=%d workDir=%s projectInstructions=%d", agent.Provider, agent.Model, len(prompt), len(attachments), workDir, len(projectInstructions))
	adapter, err := s.ensureRoutingStrategy().resolveAdapter(agent.Provider)
	if err != nil {
		return "", "", 0, err
	}
	var ad *models.Agent
	if len(agentDef) > 0 {
		ad = agentDef[0]
	}
	req, err := llmnormalize.NormalizeRequest(llmcontracts.AgentRequest{
		Ctx:                 ctx,
		Operation:           llmcontracts.OperationTask,
		Message:             prompt,
		Attachments:         attachments,
		Agent:               agent,
		ExecID:              execID,
		WorkDir:             workDir,
		ProjectInstructions: projectInstructions,
		AgentDefinition:     ad,
	})
	if err != nil {
		return "", "", 0, err
	}
	res, err := adapter.Call(req)
	if err != nil {
		// On max_tokens, return the partial output so callers can preserve it.
		// The error still propagates so the task is marked as failed.
		if res.StopReason == "max_tokens" {
			return res.Output, res.TextOnlyOutput, res.Usage.TotalTokens, err
		}
		return "", "", 0, err
	}
	return res.Output, res.TextOnlyOutput, res.Usage.TotalTokens, nil
}

func (s *LLMService) callAnthropic(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig) (string, int, error) {
	log.Printf("[agent-svc] callAnthropic model=%s max_tokens=%d temp=%.1f attachments=%d",
		agent.Model, agent.MaxTokens, agent.Temperature, len(attachments))

	client := anthropicclient.NewWithAPIKey(agent.APIKey)

	// Convert attachments to anthropicclient format
	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	// If there are non-image attachments, mention them in the prompt
	fullPrompt := prompt
	if len(attachments) > 0 {
		attachmentInfo := "\n\nYou have been provided with the following attached files:\n"
		for _, att := range attachments {
			attachmentInfo += fmt.Sprintf("- %s\n", att.FileName)
		}
		attachmentInfo += "\nPlease examine these files as part of your task.\n"
		fullPrompt += attachmentInfo
	}

	maxTokens := agent.MaxTokens
	if maxTokens == 0 {
		maxTokens = 4096
	}

	resp, err := client.Send(ctx, fullPrompt, &anthropicclient.SendOptions{
		Model:       agent.Model,
		MaxTokens:   maxTokens,
		Attachments: mcAttachments,
	})
	if err != nil {
		log.Printf("[agent-svc] callAnthropic API error: %v", err)
		return "", 0, fmt.Errorf("anthropic API call: %w", err)
	}

	tokensUsed := resp.InputTokens + resp.OutputTokens
	log.Printf("[agent-svc] callAnthropic success input_tokens=%d output_tokens=%d stop_reason=%s",
		resp.InputTokens, resp.OutputTokens, resp.StopReason)

	return resp.Text, tokensUsed, nil
}

// callAnthropicChat is the chat-specific variant of callAnthropic.
// It includes a system prompt with task context and conversation history.
// Image attachments are sent as proper multimodal content blocks instead of text.
// Uses anthropicclient for retries, connection pooling, and streaming.
func (s *LLMService) callAnthropicChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode) (string, int, error) {
	log.Printf("[agent-svc] callAnthropicChat model=%s history=%d message_len=%d context_len=%d attachments=%d exec=%s isTaskFollowup=%v chat_mode=%s", agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), execID, isTaskFollowup, chatMode)

	client := anthropicclient.NewWithAPIKey(agent.APIKey)

	// Build the system prompt based on whether this is a task followup or orchestration chat
	// Anthropic API agents don't need tool restrictions (restrictTools=false)
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	client.History = append(client.History, buildAnthropicClientHistory(chatHistory)...)

	// Convert attachments to anthropicclient format
	mcAttachments, err := convertAttachments(attachments)
	if err != nil {
		return "", 0, fmt.Errorf("convert attachments: %w", err)
	}

	sw := llmstream.NewWriter(execID, "", s.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	chatInThinking := false
	disableTools := !isTaskFollowup && chatMode != models.ChatModePlan
	opts := &anthropicclient.AgenticOptions{
		Model:          agent.Model,
		MaxTokens:      agenticMaxTokens(agent.MaxTokens),
		EnableThinking: true,
		DisableTools:   disableTools,
		System:         systemPromptStr,
		Attachments:    mcAttachments,
		AutoCompaction: true,
		OnThinking: func(text string) {
			if !chatInThinking {
				chatInThinking = true
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingOpen}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingText, Text: text}, false)
		},
		OnText: func(text string) {
			if chatInThinking {
				chatInThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: text}, false)
		},
		OnToolUse: func(name string, input json.RawMessage) {
			if chatInThinking {
				chatInThinking = false
				llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingEnd}, false)
			}
			secondary := toolSecondaryInfo(name, input)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name, Secondary: secondary}, false)
		},
		OnToolResult: func(name string, output string, isError bool) {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isError}, false)
		},
		OnCompaction: func(summary string) {
			log.Printf("[agent-svc] callAnthropicChat context compacted, summary_len=%d", len(summary))
		},
	}

	resp, err := client.SendAgentic(ctx, message, opts)
	if err != nil {
		sw.Flush()
		log.Printf("[agent-svc] callAnthropicChat error: %v", err)
		return "", 0, fmt.Errorf("anthropic API streaming call: %w", err)
	}

	sw.Flush()

	output := sw.String()
	tokensUsed := resp.InputTokens + resp.OutputTokens
	log.Printf("[agent-svc] callAnthropicChat success input_tokens=%d output_tokens=%d output_len=%d tools=%d stop=%s", resp.InputTokens, resp.OutputTokens, len(output), len(resp.ToolCalls), resp.StopReason)
	if resp.StopReason == "max_tokens" {
		return output, tokensUsed, errMaxTokens
	}
	return output, tokensUsed, nil
}

func (s *LLMService) callClaudeCLI(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string, pluginDirs []string, agentDef ...*models.Agent) (string, string, int, error) {
	log.Printf("[agent-svc] callClaudeCLI model=%s max_tokens=%d attachments=%d workDir=%s", agent.Model, agent.MaxTokens, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", "", 0, fmt.Errorf("callClaudeCLI blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	// Find the claude binary
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		log.Printf("[agent-svc] callClaudeCLI 'claude' not found in PATH: %v", err)
		return "", "", 0, fmt.Errorf("claude CLI not found in PATH - install it from https://docs.anthropic.com/en/docs/claude-code")
	}
	log.Printf("[agent-svc] callClaudeCLI using binary: %s", claudePath)

	// Claude CLI reads CLAUDE.md natively (which points to AGENTS.md),
	// so no need to inject project instructions here.
	var fullPrompt strings.Builder
	fullPrompt.WriteString(llmprompt.BuildTaskPromptHeader())
	if worktreeContext := llmprompt.BuildWorktreeContextSentence(workDir); worktreeContext != "" {
		fullPrompt.WriteString(worktreeContext)
		fullPrompt.WriteString("\n\n")
	}
	fullPrompt.WriteString(llmprompt.BuildAttachmentInstructions(attachments))
	fullPrompt.WriteString(prompt)

	// Add task creation instructions so the agent can create sub-tasks
	fullPrompt.WriteString("\n\n")
	fullPrompt.WriteString(llmprompt.TaskCreationInstructions)

	// Append status reporting instructions AFTER the task prompt so the agent
	// sees them last and is more likely to follow them.
	fullPrompt.WriteString("\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response.")

	// Build command: -p reads prompt from stdin, stream-json gives us JSON events,
	// --include-partial-messages gives us token-level streaming for real-time output
	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	for _, dir := range pluginDirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		args = append(args, "--plugin-dir", dir)
	}

	log.Printf("[agent-svc] callClaudeCLI executing: claude %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, claudePath, args...)

	// Set working directory to the project's repo path so the agent
	// operates in the correct project directory (not the OpenVibely server dir).
	if workDir != "" {
		cmd.Dir = workDir
		log.Printf("[agent-svc] callClaudeCLI using workDir=%s", workDir)
	}

	// Write agent definition files (agent.md, skills, .mcp.json) if present
	var ad *models.Agent
	if len(agentDef) > 0 {
		ad = agentDef[0]
	}
	if ad != nil && workDir != "" {
		cleanup, writeErr := WriteAgentFiles(workDir, ad)
		if writeErr != nil {
			log.Printf("[agent-svc] callClaudeCLI error writing agent files: %v", writeErr)
		} else {
			defer cleanup()
			log.Printf("[agent-svc] callClaudeCLI wrote agent definition files for %q", ad.Name)
		}
	}

	cmd.Env = llmprompt.FilteredEnvWithoutClaudeCode()

	// Pass prompt via stdin for streaming output
	cmd.Stdin = strings.NewReader(fullPrompt.String())

	// Get stdout pipe for reading JSON stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[agent-svc] callClaudeCLI error creating stdout pipe: %v", err)
		return "", "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Use streaming writer for real-time DB updates.
	// The background periodic flush ensures output is visible even during
	// long pauses (e.g., while a tool is running).
	sw := llmstream.NewWriter(execID, "", s.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Start the command
	if err := cmd.Start(); err != nil {
		log.Printf("[agent-svc] callClaudeCLI error starting command: %v", err)
		return "", "", 0, fmt.Errorf("starting claude CLI: %w", err)
	}

	// Parse JSON stream in a goroutine and extract text content
	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseJSONStream(stdoutPipe, sw, false)
	}()

	// Wait for command to finish
	err = cmd.Wait()

	// Wait for parsing to complete
	if pErr := <-parseErr; pErr != nil {
		log.Printf("[agent-svc] callClaudeCLI JSON parsing error: %v", pErr)
	}

	// Flush any remaining output to the DB
	sw.Flush()

	if err != nil {
		errOutput := stderr.String()
		log.Printf("[agent-svc] callClaudeCLI error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", "", 0, fmt.Errorf("claude CLI error: %s", errOutput)
		}
		return "", "", 0, fmt.Errorf("claude CLI error: %w", err)
	}

	// Check if the CLI result event reported an error (e.g., max turns exceeded).
	// The CLI may exit 0 even when it reports is_error=true in the result event.
	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		log.Printf("[agent-svc] callClaudeCLI result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, "", 0, fmt.Errorf("claude CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()
	textOnly := sw.TextString()
	log.Printf("[agent-svc] callClaudeCLI success output_len=%d text_only_len=%d", len(output), len(textOnly))

	// CLI doesn't report token counts, so we return 0
	return output, textOnly, 0, nil
}

// callClaudeCLIChat is the chat-specific variant of callClaudeCLI.
// It builds a lightweight prompt with conversation history and no task-execution
// directives (no AGENTS.md, no STATUS markers).
func (s *LLMService) callClaudeCLIChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, isTaskFollowup bool, chatMode models.ChatMode, pluginDirs []string) (string, int, error) {
	log.Printf("[agent-svc] callClaudeCLIChat model=%s history=%d message_len=%d context_len=%d attachments=%d workDir=%s isTaskFollowup=%v chat_mode=%s", agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), workDir, isTaskFollowup, chatMode)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callClaudeCLIChat blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		log.Printf("[agent-svc] callClaudeCLIChat 'claude' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("claude CLI not found in PATH - install it from https://docs.anthropic.com/en/docs/claude-code")
	}

	// Check if we have a CLI session ID from a prior chat turn to resume
	var lastSessionID string
	for i := len(chatHistory) - 1; i >= 0; i-- {
		if chatHistory[i].CliSessionID != "" {
			lastSessionID = chatHistory[i].CliSessionID
			break
		}
	}

	// Build prompt — just the system prompt + current message (no manual history).
	// If resuming a session, the CLI manages its own conversation state.
	var fullPrompt strings.Builder
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, true)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	fullPrompt.WriteString(systemPromptStr)
	fullPrompt.WriteString("\n")

	if lastSessionID == "" {
		// First message — no session to resume, include history text as context
		fullPrompt.WriteString(llmprompt.BuildChatHistoryText(chatHistory))
	}

	fullPrompt.WriteString(message)

	// Pass attachments - separate handling for text vs images
	if len(attachments) > 0 {
		var textFiles []models.Attachment
		var imageFiles []models.Attachment

		for _, att := range attachments {
			if strings.HasPrefix(strings.ToLower(att.MediaType), "image/") {
				imageFiles = append(imageFiles, att)
			} else {
				textFiles = append(textFiles, att)
			}
		}

		// Text files can be read normally
		if len(textFiles) > 0 {
			fullPrompt.WriteString("\n\n[The user attached the following files:\n")
			for _, att := range textFiles {
				absPath := llmprompt.AttachmentAbsPath(att)
				fullPrompt.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
			}
			fullPrompt.WriteString("You can read these files using your Read tool.]")
		}

		// Image files cannot be viewed in CLI mode
		if len(imageFiles) > 0 {
			fullPrompt.WriteString("\n\n[NOTE: The user attached the following image files, but you cannot view them because you are running in CLI mode without vision support:\n")
			for _, att := range imageFiles {
				absPath := llmprompt.AttachmentAbsPath(att)
				fullPrompt.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
			}
			fullPrompt.WriteString("\nPlease inform the user that you cannot analyze images in CLI mode and suggest they reconfigure to use a vision-capable model (Anthropic API or OpenAI API with an API key or OAuth).]")
		}
	}

	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if !isTaskFollowup && chatMode == models.ChatModePlan {
		args = append(args, "--permission-mode", "plan")
	} else {
		args = append(args, "--dangerously-skip-permissions")
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}
	if !(chatMode == models.ChatModePlan && !isTaskFollowup) {
		for _, dir := range pluginDirs {
			dir = strings.TrimSpace(dir)
			if dir == "" {
				continue
			}
			args = append(args, "--plugin-dir", dir)
		}
	}
	// Resume the CLI session if we have one from a prior chat turn
	if lastSessionID != "" {
		args = append(args, "--resume", lastSessionID)
		log.Printf("[agent-svc] callClaudeCLIChat resuming session=%s", lastSessionID)
	}

	log.Printf("[agent-svc] callClaudeCLIChat executing: claude %s (prompt via stdin, len=%d)", strings.Join(args, " "), fullPrompt.Len())

	cmd := exec.CommandContext(ctx, claudePath, args...)

	// Set working directory to the project's repo path so the agent
	// operates in the correct project directory (not the OpenVibely server dir).
	if workDir != "" {
		cmd.Dir = workDir
		log.Printf("[agent-svc] callClaudeCLIChat using workDir=%s", workDir)
	}

	cmd.Env = llmprompt.FilteredEnvWithoutClaudeCode()

	cmd.Stdin = strings.NewReader(fullPrompt.String())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[agent-svc] callClaudeCLIChat error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriter(execID, "", s.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[agent-svc] callClaudeCLIChat error starting command: %v", err)
		return "", 0, fmt.Errorf("starting claude CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseJSONStream(stdoutPipe, sw, true)
	}()

	err = cmd.Wait()

	if pErr := <-parseErr; pErr != nil {
		log.Printf("[agent-svc] callClaudeCLIChat JSON parsing error: %v", pErr)
	}

	sw.Flush()

	if err != nil {
		errOutput := stderr.String()
		log.Printf("[agent-svc] callClaudeCLIChat error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("claude CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("claude CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		log.Printf("[agent-svc] callClaudeCLIChat result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, 0, fmt.Errorf("claude CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()

	// Persist the CLI session ID so subsequent chat calls can --resume
	sid := sw.SessionID()
	if sid != "" && s.execRepo != nil {
		if err := s.execRepo.UpdateCliSessionID(ctx, execID, sid); err != nil {
			log.Printf("[agent-svc] callClaudeCLIChat error persisting session_id: %v", err)
		} else {
			log.Printf("[agent-svc] callClaudeCLIChat persisted session_id=%s for exec=%s", sid, execID)
		}
	}

	log.Printf("[agent-svc] callClaudeCLIChat success output_len=%d session_id=%s", len(output), sid)
	return output, 0, nil
}

func prependDirectNoToolsInstruction(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	prefix := "IMPORTANT: Do not execute any tools, plugins, MCP actions, or shell commands for this request. Reply directly with plain text only."
	if prompt == "" {
		return prefix
	}
	return prefix + "\n\n" + prompt
}

func (s *LLMService) callClaudeCLISimple(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	log.Printf("[agent-svc] callClaudeCLISimple model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callClaudeCLISimple blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	// Find the claude binary
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		log.Printf("[agent-svc] callClaudeCLISimple 'claude' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("claude CLI not found in PATH - install it from https://docs.anthropic.com/en/docs/claude-code")
	}
	log.Printf("[agent-svc] callClaudeCLISimple using binary: %s", claudePath)

	// Build command with streaming JSON output
	args := []string{
		"-p",
		"--output-format=stream-json",
		"--verbose",
		"--include-partial-messages",
		"--dangerously-skip-permissions",
	}
	if agent.Model != "" {
		args = append(args, "--model", agent.Model)
	}

	log.Printf("[agent-svc] callClaudeCLISimple executing: claude %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, claudePath, args...)

	// Set working directory to the project's repo path so the agent
	// operates in the correct project directory (not the OpenVibely server dir).
	if workDir != "" {
		cmd.Dir = workDir
		log.Printf("[agent-svc] callClaudeCLISimple using workDir=%s", workDir)
	}

	cmd.Env = llmprompt.FilteredEnvWithoutClaudeCode()

	fullPrompt := prompt
	if disableTools {
		fullPrompt = prependDirectNoToolsInstruction(prompt)
	}

	// Pass prompt via stdin
	cmd.Stdin = strings.NewReader(fullPrompt)

	// Get stdout pipe for reading JSON stream
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[agent-svc] callClaudeCLISimple error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	// Use a simple buffer writer for collecting output
	var outputBuf bytes.Buffer

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	// Start the command
	if err := cmd.Start(); err != nil {
		log.Printf("[agent-svc] callClaudeCLISimple error starting command: %v", err)
		return "", 0, fmt.Errorf("starting claude CLI: %w", err)
	}

	// Parse JSON stream and collect text
	scanner := bufio.NewScanner(stdoutPipe)
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		eventType, hasType := event["type"].(string)
		if hasType {
			switch eventType {
			case "content_block_delta":
				if delta, ok := event["delta"].(map[string]interface{}); ok {
					if dt, _ := delta["type"].(string); dt == "text_delta" {
						if text, ok := delta["text"].(string); ok && text != "" {
							outputBuf.WriteString(text)
						}
					}
				}
			case "content_block_start":
				if cb, ok := event["content_block"].(map[string]interface{}); ok {
					if bt, _ := cb["type"].(string); bt == "tool_use" {
						if name, ok := cb["name"].(string); ok && name != "" {
							outputBuf.WriteString(fmt.Sprintf("\n[Using tool: %s]\n", name))
						}
					}
				}
			case "result":
				if result, ok := event["result"].(string); ok && result != "" {
					if outputBuf.Len() == 0 {
						outputBuf.WriteString(result)
					}
				}
			}
		}
	}

	// Wait for command to finish
	err = cmd.Wait()

	if err != nil {
		errOutput := stderr.String()
		log.Printf("[agent-svc] callClaudeCLISimple error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("claude CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("claude CLI error: %w", err)
	}

	output := outputBuf.String()
	log.Printf("[agent-svc] callClaudeCLISimple success output_len=%d", len(output))

	return output, 0, nil
}

func (s *LLMService) callCodexCLI(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, workDir string) (string, string, int, error) {
	log.Printf("[agent-svc] callCodexCLI model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", "", 0, fmt.Errorf("callCodexCLI blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		log.Printf("[agent-svc] callCodexCLI 'codex' not found in PATH: %v", err)
		return "", "", 0, fmt.Errorf("codex CLI not found in PATH - install it from https://github.com/openai/codex")
	}
	log.Printf("[agent-svc] callCodexCLI using binary: %s", codexPath)

	var fullPrompt strings.Builder
	fullPrompt.WriteString(llmprompt.BuildTaskPromptHeader())
	if worktreeContext := llmprompt.BuildWorktreeContextSentence(workDir); worktreeContext != "" {
		fullPrompt.WriteString(worktreeContext)
		fullPrompt.WriteString("\n\n")
	}
	fullPrompt.WriteString(llmprompt.BuildAttachmentInstructions(attachments))
	fullPrompt.WriteString(prompt)

	imagePaths := make([]string, 0, len(attachments))
	for _, att := range attachments {
		if llmoutput.IsImageMediaType(att.MediaType) {
			imagePaths = append(imagePaths, llmprompt.AttachmentAbsPath(att))
		}
	}
	fullPrompt.WriteString("\n\n")
	fullPrompt.WriteString(llmprompt.TaskCreationInstructions)
	fullPrompt.WriteString("\n\n---\nRESPONSE FORMAT REQUIREMENT: You MUST end your final response with exactly one of these status lines:\n" +
		"- If the task completed successfully: [STATUS: SUCCESS]\n" +
		"- If a command failed, a script returned non-zero, or the task could not be completed: [STATUS: FAILED | <describe what went wrong>]\n" +
		"- If the task completed but something needs human attention: [STATUS: NEEDS_FOLLOWUP | <describe what needs attention>]\n" +
		"Example: [STATUS: FAILED | fail.sh returned exit code 1]\n" +
		"Example: [STATUS: NEEDS_FOLLOWUP | tests pass but 3 warnings need review]\n" +
		"Replace <describe what went wrong> or <describe what needs attention> with your actual description.\n" +
		"This status line is MANDATORY. Always include it as the very last line of your response.")

	args := llmprompt.CodexExecArgs(agent.Model, agent.ReasoningEffort, imagePaths)
	log.Printf("[agent-svc] callCodexCLI executing: codex %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, codexPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
		log.Printf("[agent-svc] callCodexCLI using workDir=%s", workDir)
	}
	cmd.Stdin = strings.NewReader(fullPrompt.String())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[agent-svc] callCodexCLI error creating stdout pipe: %v", err)
		return "", "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriter(execID, "", s.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[agent-svc] callCodexCLI error starting command: %v", err)
		return "", "", 0, fmt.Errorf("starting codex CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseCodexJSONStream(stdoutPipe, sw, false)
	}()

	err = cmd.Wait()
	if pErr := <-parseErr; pErr != nil {
		log.Printf("[agent-svc] callCodexCLI JSON parsing error: %v", pErr)
	}

	sw.Flush()

	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		log.Printf("[agent-svc] callCodexCLI error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", "", 0, fmt.Errorf("codex CLI error: %s", errOutput)
		}
		return "", "", 0, fmt.Errorf("codex CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		log.Printf("[agent-svc] callCodexCLI result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, "", 0, fmt.Errorf("codex CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()
	textOnly := sw.TextString()
	log.Printf("[agent-svc] callCodexCLI success output_len=%d text_only_len=%d", len(output), len(textOnly))
	return output, textOnly, 0, nil
}

func (s *LLMService) callCodexCLIChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, workDir string, isTaskFollowup bool, chatMode models.ChatMode) (string, int, error) {
	log.Printf("[agent-svc] callCodexCLIChat model=%s history=%d message_len=%d context_len=%d attachments=%d workDir=%s isTaskFollowup=%v chat_mode=%s", agent.Model, len(chatHistory), len(message), len(chatSystemContext), len(attachments), workDir, isTaskFollowup, chatMode)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callCodexCLIChat blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		log.Printf("[agent-svc] callCodexCLIChat 'codex' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("codex CLI not found in PATH - install it from https://github.com/openai/codex")
	}

	// Check for a prior Codex thread ID to resume
	var lastThreadID string
	for i := len(chatHistory) - 1; i >= 0; i-- {
		if chatHistory[i].CliSessionID != "" {
			lastThreadID = chatHistory[i].CliSessionID
			break
		}
	}

	var fullPrompt strings.Builder
	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, true)
	systemPromptStr = llmprompt.AppendWorktreeContextPrompt(systemPromptStr, workDir)
	fullPrompt.WriteString(systemPromptStr)
	fullPrompt.WriteString("\n")

	if lastThreadID == "" {
		// First message — no thread to resume, include history text as context
		fullPrompt.WriteString(llmprompt.BuildChatHistoryText(chatHistory))
	}

	fullPrompt.WriteString(message)

	imagePaths := make([]string, 0, len(attachments))
	if len(attachments) > 0 {
		fullPrompt.WriteString("\n\n[The user attached files. Use the absolute paths below when needed:\n")
		for _, att := range attachments {
			absPath := llmprompt.AttachmentAbsPath(att)
			fullPrompt.WriteString(fmt.Sprintf("- %s (path: %s)\n", att.FileName, absPath))
			if llmoutput.IsImageMediaType(att.MediaType) {
				imagePaths = append(imagePaths, absPath)
			}
		}
		fullPrompt.WriteString("]")
	}

	var args []string
	if lastThreadID != "" {
		// Resume existing thread — codex manages its own history
		args = llmprompt.CodexResumeArgs(agent.Model, agent.ReasoningEffort, lastThreadID, imagePaths, chatMode)
		log.Printf("[agent-svc] callCodexCLIChat resuming thread=%s", lastThreadID)
	} else {
		args = llmprompt.CodexChatArgs(agent.Model, agent.ReasoningEffort, imagePaths, chatMode)
	}
	log.Printf("[agent-svc] callCodexCLIChat executing: codex %s (prompt via stdin, len=%d)", strings.Join(args, " "), fullPrompt.Len())

	cmd := exec.CommandContext(ctx, codexPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
		log.Printf("[agent-svc] callCodexCLIChat using workDir=%s", workDir)
	}
	cmd.Stdin = strings.NewReader(fullPrompt.String())

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[agent-svc] callCodexCLIChat error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriter(execID, "", s.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[agent-svc] callCodexCLIChat error starting command: %v", err)
		return "", 0, fmt.Errorf("starting codex CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseCodexJSONStream(stdoutPipe, sw, true)
	}()

	err = cmd.Wait()
	if pErr := <-parseErr; pErr != nil {
		log.Printf("[agent-svc] callCodexCLIChat JSON parsing error: %v", pErr)
	}

	sw.Flush()

	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		log.Printf("[agent-svc] callCodexCLIChat error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("codex CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("codex CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		log.Printf("[agent-svc] callCodexCLIChat result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, 0, fmt.Errorf("codex CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()

	// Persist the Codex thread ID so subsequent chat calls can resume
	tid := sw.SessionID()
	if tid != "" && s.execRepo != nil {
		if err := s.execRepo.UpdateCliSessionID(ctx, execID, tid); err != nil {
			log.Printf("[agent-svc] callCodexCLIChat error persisting thread_id: %v", err)
		} else {
			log.Printf("[agent-svc] callCodexCLIChat persisted thread_id=%s for exec=%s", tid, execID)
		}
	}

	log.Printf("[agent-svc] callCodexCLIChat success output_len=%d thread_id=%s", len(output), tid)
	return output, 0, nil
}

func (s *LLMService) callCodexCLISimple(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, workDir string, disableTools bool) (string, int, error) {
	log.Printf("[agent-svc] callCodexCLISimple model=%s attachments=%d workDir=%s", agent.Model, len(attachments), workDir)

	// SAFETY: Prevent accidental real CLI calls during tests
	if isTestMode() {
		return "", 0, fmt.Errorf("callCodexCLISimple blocked in test mode - use ProviderTest with SetLLMCaller() instead")
	}

	codexPath, err := exec.LookPath("codex")
	if err != nil {
		log.Printf("[agent-svc] callCodexCLISimple 'codex' not found in PATH: %v", err)
		return "", 0, fmt.Errorf("codex CLI not found in PATH - install it from https://github.com/openai/codex")
	}

	fullPrompt := strings.TrimSpace(prompt)
	if disableTools {
		fullPrompt = prependDirectNoToolsInstruction(fullPrompt)
	}
	imagePaths := make([]string, 0, len(attachments))
	if len(attachments) > 0 {
		fullPrompt += "\n\nAttached files:\n"
		for _, att := range attachments {
			absPath := llmprompt.AttachmentAbsPath(att)
			fullPrompt += fmt.Sprintf("- %s (absolute path: %s)\n", att.FileName, absPath)
			if llmoutput.IsImageMediaType(att.MediaType) {
				imagePaths = append(imagePaths, absPath)
			}
		}
	}

	args := llmprompt.CodexExecArgs(agent.Model, agent.ReasoningEffort, imagePaths)
	log.Printf("[agent-svc] callCodexCLISimple executing: codex %s (prompt via stdin)", strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, codexPath, args...)
	if workDir != "" {
		cmd.Dir = workDir
		log.Printf("[agent-svc] callCodexCLISimple using workDir=%s", workDir)
	}
	cmd.Stdin = strings.NewReader(fullPrompt)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[agent-svc] callCodexCLISimple error creating stdout pipe: %v", err)
		return "", 0, fmt.Errorf("creating stdout pipe: %w", err)
	}

	sw := llmstream.NewWriter("", "", nil, ctx, time.Hour)
	defer sw.Stop()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		log.Printf("[agent-svc] callCodexCLISimple error starting command: %v", err)
		return "", 0, fmt.Errorf("starting codex CLI: %w", err)
	}

	parseErr := make(chan error, 1)
	go func() {
		parseErr <- llmstream.ParseCodexJSONStream(stdoutPipe, sw, true)
	}()

	err = cmd.Wait()
	if pErr := <-parseErr; pErr != nil {
		log.Printf("[agent-svc] callCodexCLISimple JSON parsing error: %v", pErr)
	}

	if err != nil {
		errOutput := strings.TrimSpace(stderr.String())
		log.Printf("[agent-svc] callCodexCLISimple error: %v stderr: %s", err, errOutput)
		if errOutput != "" {
			return "", 0, fmt.Errorf("codex CLI error: %s", errOutput)
		}
		return "", 0, fmt.Errorf("codex CLI error: %w", err)
	}

	if sw.IsError() {
		output := sw.String()
		subtype := sw.ResultSubtype()
		log.Printf("[agent-svc] callCodexCLISimple result is_error=true subtype=%s output_len=%d", subtype, len(output))
		return output, 0, fmt.Errorf("codex CLI reported error (subtype=%s)", subtype)
	}

	output := sw.String()
	log.Printf("[agent-svc] callCodexCLISimple success output_len=%d", len(output))
	return output, 0, nil
}
