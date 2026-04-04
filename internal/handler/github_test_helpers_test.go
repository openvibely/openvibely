package handler

import (
	"context"
	"fmt"

	"github.com/openvibely/openvibely/internal/service"
)

type fakeGitHubService struct {
	statusFn      func(ctx context.Context) (service.GitHubConnectionStatus, error)
	connectURLFn  func(ctx context.Context) (string, error)
	callbackFn    func(ctx context.Context, installationID string) error
	disconnectFn  func(ctx context.Context) error
	cloneFn       func(ctx context.Context, projectID, repoURL string) (string, string, error)
	recloneFn     func(ctx context.Context, projectID, currentRepoPath, repoURL string) (string, string, error)
	resolveRepoFn func(ctx context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error)
	pushBranchFn  func(ctx context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error
	findPRFn      func(ctx context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error)
	createPRFn    func(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error)
}

func (f *fakeGitHubService) GetConnectionStatus(ctx context.Context) (service.GitHubConnectionStatus, error) {
	if f != nil && f.statusFn != nil {
		return f.statusFn(ctx)
	}
	return service.GitHubConnectionStatus{}, nil
}

func (f *fakeGitHubService) ConnectURL(ctx context.Context) (string, error) {
	if f != nil && f.connectURLFn != nil {
		return f.connectURLFn(ctx)
	}
	return "", fmt.Errorf("connect URL not configured")
}

func (f *fakeGitHubService) HandleInstallCallback(ctx context.Context, installationID string) error {
	if f != nil && f.callbackFn != nil {
		return f.callbackFn(ctx, installationID)
	}
	return nil
}

func (f *fakeGitHubService) Disconnect(ctx context.Context) error {
	if f != nil && f.disconnectFn != nil {
		return f.disconnectFn(ctx)
	}
	return nil
}

func (f *fakeGitHubService) CloneProjectRepo(ctx context.Context, projectID, repoURL string) (string, string, error) {
	if f != nil && f.cloneFn != nil {
		return f.cloneFn(ctx, projectID, repoURL)
	}
	return "", "", fmt.Errorf("clone not configured")
}

func (f *fakeGitHubService) RecloneProjectRepo(ctx context.Context, projectID, currentRepoPath, repoURL string) (string, string, error) {
	if f != nil && f.recloneFn != nil {
		return f.recloneFn(ctx, projectID, currentRepoPath, repoURL)
	}
	return "", "", fmt.Errorf("reclone not configured")
}

func (f *fakeGitHubService) ResolveRepo(ctx context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
	if f != nil && f.resolveRepoFn != nil {
		return f.resolveRepoFn(ctx, repoURL, repoPath)
	}
	return nil, fmt.Errorf("resolve repo not configured")
}

func (f *fakeGitHubService) PushBranch(ctx context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error {
	if f != nil && f.pushBranchFn != nil {
		return f.pushBranchFn(ctx, repoPath, worktreePath, branch, repo)
	}
	return nil
}

func (f *fakeGitHubService) FindPullRequestByBranch(ctx context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
	if f != nil && f.findPRFn != nil {
		return f.findPRFn(ctx, repo, branch)
	}
	return nil, nil
}

func (f *fakeGitHubService) CreatePullRequest(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
	if f != nil && f.createPRFn != nil {
		return f.createPRFn(ctx, repo, createReq)
	}
	return nil, fmt.Errorf("create PR not configured")
}
