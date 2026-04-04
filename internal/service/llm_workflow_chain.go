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

// triggerTaskChain checks if a task has chaining configured and creates a child task
// with the parent's output as the prompt (for plan-to-implementation chains).
func (s *LLMService) triggerTaskChain(ctx context.Context, parentTask models.Task, parentOutput string) error {
	return s.workflowChainService().TriggerTaskChain(ctx, parentTask, parentOutput)
}
