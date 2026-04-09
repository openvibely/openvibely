package service

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"

	llmworkflow "github.com/openvibely/openvibely/internal/llm/workflow"
	"github.com/openvibely/openvibely/internal/models"
)

var reThinkingBlock = regexp.MustCompile(`(?s)\[Thinking\]\n.*?\[/Thinking\]\n?`)

type workflowProjectResolver struct {
	s *LLMService
}

func (r workflowProjectResolver) ResolveWorkDir(ctx context.Context, projectID string) string {
	if projectID == "" || r.s.projectRepo == nil {
		return ""
	}
	project, err := r.s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return ""
	}
	return project.RepoPath
}

type workflowAgentCaller struct {
	s *LLMService
}

func (c workflowAgentCaller) CallAgentDirect(ctx context.Context, message string, agent models.LLMConfig, workDir string) (string, error) {
	out, _, err := c.s.CallAgentDirect(ctx, message, nil, agent, workDir)
	return out, err
}

type workflowTaskCreator struct {
	s *LLMService
}

func (c workflowTaskCreator) Create(ctx context.Context, task *models.Task) error {
	return c.s.taskSvc.Create(ctx, task)
}

type workflowLineageResolver struct {
	s *LLMService
}

// ResolveParentLineage resolves the Git branch + commit SHA for a parent task.
// Preferred: parent worktree branch + HEAD commit SHA.
// Fallback: merge target / default branch HEAD SHA.
func (r workflowLineageResolver) ResolveParentLineage(ctx context.Context, parentTask models.Task) (string, string, error) {
	// If parent has a worktree, resolve from it
	if parentTask.WorktreePath != "" && parentTask.WorktreeBranch != "" {
		sha, err := resolveGitHEAD(parentTask.WorktreePath)
		if err != nil {
			log.Printf("[lineage] failed to resolve HEAD in worktree %s: %v", parentTask.WorktreePath, err)
		} else {
			return parentTask.WorktreeBranch, sha, nil
		}
	}

	// Fallback: resolve from the project repo using merge target or default branch
	repoDir := ""
	if parentTask.ProjectID != "" && r.s.projectRepo != nil {
		project, err := r.s.projectRepo.GetByID(ctx, parentTask.ProjectID)
		if err == nil && project != nil {
			repoDir = project.RepoPath
		}
	}
	if repoDir == "" || !IsGitRepo(repoDir) {
		return "", "", fmt.Errorf("no git repo available for lineage resolution (project=%s)", parentTask.ProjectID)
	}

	targetBranch := parentTask.MergeTargetBranch
	if targetBranch == "" {
		targetBranch = GetDefaultBranch(repoDir)
	}

	sha, err := resolveGitRef(repoDir, targetBranch)
	if err != nil {
		return "", "", fmt.Errorf("resolving ref %s in %s: %w", targetBranch, repoDir, err)
	}
	return targetBranch, sha, nil
}

// resolveGitHEAD returns the HEAD commit SHA in the given directory.
func resolveGitHEAD(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveGitRef returns the commit SHA for a named ref in the given repo.
func resolveGitRef(repoDir, ref string) (string, error) {
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *LLMService) workflowChainService() *llmworkflow.Service {
	svc := llmworkflow.NewService(
		workflowProjectResolver{s: s},
		workflowTaskCreator{s: s},
		workflowAgentCaller{s: s},
	)
	svc.SetLineageResolver(workflowLineageResolver{s: s})
	return svc
}

// CallAgentForWorkflow calls an LLM agent for workflow step execution.
// It resolves the project working directory and delegates to CallAgentDirect.
func (s *LLMService) CallAgentForWorkflow(ctx context.Context, prompt string, agent *models.LLMConfig, projectID string) (string, error) {
	return s.workflowChainService().CallAgentForWorkflow(ctx, prompt, agent, projectID)
}

func cleanOutputForChain(output string) string {
	return llmworkflow.CleanOutputForChain(output)
}

func defaultRunnableChildCategory(parentCategory models.TaskCategory) models.TaskCategory {
	switch parentCategory {
	case models.CategoryActive, models.CategoryBacklog:
		return parentCategory
	default:
		// Parent can be moved to completed before chain activation runs.
		// Defaulting to active preserves sequential execution behavior.
		return models.CategoryActive
	}
}

// triggerTaskChain checks if a task has chaining configured and activates the child task.
// If a blocked child was pre-created for visibility, it is activated in place.
// Otherwise, a new child task is created (fallback for chains without pre-created children).
func (s *LLMService) triggerTaskChain(ctx context.Context, parentTask models.Task, parentOutput string) error {
	// Reload latest parent from DB so chain edits made while the task was running
	// (e.g., via chat EDIT_TASK) are respected at completion time.
	if s.taskRepo != nil {
		if latest, getErr := s.taskRepo.GetByID(ctx, parentTask.ID); getErr != nil {
			log.Printf("[agent-svc] triggerTaskChain error loading latest parent task=%s: %v", parentTask.ID, getErr)
		} else if latest != nil {
			parentTask = *latest
		}
	}

	config, err := parentTask.ParseChainConfig()
	if err != nil || !config.Enabled {
		return s.workflowChainService().TriggerTaskChain(ctx, parentTask, parentOutput)
	}

	// Look for an existing blocked child to activate
	if s.taskRepo != nil {
		blockedChild, findErr := s.taskRepo.FindBlockedChildByParent(ctx, parentTask.ID)
		if findErr != nil {
			log.Printf("[agent-svc] triggerTaskChain error finding blocked child for parent=%s: %v", parentTask.ID, findErr)
		}
		if blockedChild != nil {
			log.Printf("[agent-svc] triggerTaskChain activating blocked child id=%s parent=%s", blockedChild.ID, parentTask.ID)

			// Build the real prompt from parent output
			childPrompt := llmworkflow.CleanOutputForChain(parentOutput)
			if config.ChildPromptPrefix != "" {
				childPrompt = config.ChildPromptPrefix + "\n\n" + childPrompt
			}
			blockedChild.Prompt = childPrompt
			blockedChild.Status = models.StatusPending

			// Resolve lineage now that parent is complete
			svc := s.workflowChainService()
			if svc != nil {
				branch, sha, lineageErr := workflowLineageResolver{s: s}.ResolveParentLineage(ctx, parentTask)
				if lineageErr != nil {
					log.Printf("[agent-svc] triggerTaskChain lineage resolution failed parent=%s: %v", parentTask.ID, lineageErr)
				} else {
					blockedChild.BaseBranch = branch
					blockedChild.BaseCommitSHA = sha
					log.Printf("[agent-svc] triggerTaskChain resolved lineage for child=%s branch=%s sha=%s", blockedChild.ID, branch, sha)
				}
			}

			// Resolve category: inherit only runnable categories by default.
			if config.ChildCategory != "" {
				blockedChild.Category = models.TaskCategory(config.ChildCategory)
			} else {
				blockedChild.Category = defaultRunnableChildCategory(parentTask.Category)
			}

			if err := s.taskRepo.Update(ctx, blockedChild); err != nil {
				log.Printf("[agent-svc] triggerTaskChain error updating blocked child id=%s: %v", blockedChild.ID, err)
				return fmt.Errorf("activating blocked child: %w", err)
			}

			// Submit to worker pool
			if s.taskSvc != nil && blockedChild.Category == models.CategoryActive {
				log.Printf("[agent-svc] triggerTaskChain submitting activated child id=%s to worker pool", blockedChild.ID)
				s.taskSvc.workerSvc.Submit(*blockedChild)
			}
			return nil
		}
	}

	// Fallback: no blocked child found, create a new one via the workflow service
	log.Printf("[agent-svc] triggerTaskChain no blocked child found for parent=%s, creating new child", parentTask.ID)
	return s.workflowChainService().TriggerTaskChain(ctx, parentTask, parentOutput)
}
