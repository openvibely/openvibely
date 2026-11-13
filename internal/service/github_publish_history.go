package service

import (
	"context"
	"fmt"
	"github.com/openvibely/openvibely/pkg/gitidentity"
	"strings"
)

// publishTaskHistory publishes the task's commits to its source repository.
// Neither the task's local base nor the PR's target branch needs to exist there.
// The caller holds the repository mutation lock throughout preparation and push.
func (s *GitHubService) publishTaskHistory(ctx context.Context, repo *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository reference is required")
	}
	dir, branch := strings.TrimSpace(req.WorktreePath), strings.TrimSpace(req.Branch)
	if dir == "" || branch == "" {
		return nil, fmt.Errorf("task worktree path and branch are required")
	}
	if _, err := s.runGit(ctx, dir, nil, "check-ref-format", "refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("invalid task branch: %w", err)
	}
	current, err := s.runGit(ctx, dir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(string(current)) != branch {
		return nil, fmt.Errorf("worktree must be checked out on task branch %q", branch)
	}
	// Use the canonical source URL, not a locally configured push remote.
	remote := strings.TrimRight(repo.HTMLURL, "/") + ".git"
	if strings.TrimSpace(repo.HTMLURL) == "" {
		return nil, fmt.Errorf("source repository URL is required")
	}
	token, err := s.createOperationAccessToken(ctx, githubAPIBaseURLForRepo(repo, s.apiBaseURL))
	if err != nil {
		return nil, err
	}
	previous, err := s.runGit(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolving task HEAD: %w", err)
	}
	status, err := s.runGit(ctx, dir, nil, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("checking task changes: %w", err)
	}
	created := strings.TrimSpace(string(status)) != ""
	if created {
		if strings.TrimSpace(req.CommitMessage) == "" {
			return nil, fmt.Errorf("commit message is required")
		}
		if _, err := s.runGit(ctx, dir, nil, "add", "-A"); err != nil {
			return nil, fmt.Errorf("staging task changes: %w", err)
		}
		name, email := strings.TrimSpace(req.CommitterName), strings.TrimSpace(req.CommitterEmail)
		gitCtx := s.GitContext(ctx)
		if gitidentity.Configured(gitCtx) {
			if err := gitidentity.RequireCommit(gitCtx); err != nil {
				return nil, err
			}
			identity, err := gitidentity.Resolve(gitCtx)
			if err != nil {
				return nil, err
			}
			if identity.Name != "" {
				name = identity.Name
			}
			if identity.Email != "" {
				email = identity.Email
			}
		}
		if name == "" {
			name = "OpenVibely Bot"
		}
		if email == "" {
			email = "bot@openvibely.ai"
		}
		if _, err := s.runGit(ctx, dir, nil, "-c", "user.name="+name, "-c", "user.email="+email, "commit", "-m", req.CommitMessage); err != nil {
			return nil, fmt.Errorf("committing task changes: %w", err)
		}
	}
	head, err := s.runGit(ctx, dir, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("resolving publication HEAD: %w", err)
	}
	sha := strings.TrimSpace(string(head))
	if !isGitHubCommitSHA(sha) {
		return nil, fmt.Errorf("task HEAD is not a GitHub commit SHA")
	}
	// An explicit SHA pins this request's publication. A normal push rejects
	// divergent remote history; publication never silently force-pushes.
	if _, err := s.runGit(ctx, dir, gitHubTokenEnvForURL(token, remote), "push", "--no-follow-tags", remote, sha+":refs/heads/"+branch); err != nil {
		return nil, fmt.Errorf("publishing task history failed (remote history must fast-forward): %w", err)
	}
	result := &GitHubPublishBranchResult{HeadSHA: sha, CreatedCommit: created}
	if created {
		result.ParentSHA = strings.TrimSpace(string(previous))
	}
	return result, nil
}
