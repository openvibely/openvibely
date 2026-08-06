package handler

import (
	"context"
	"fmt"

	"github.com/openvibely/openvibely/internal/service"
)

type fakeGitHubService struct {
	statusFn               func(ctx context.Context) (service.GitHubConnectionStatus, error)
	connectURLFn           func(ctx context.Context) (string, error)
	callbackFn             func(ctx context.Context, installationID string) error
	disconnectFn           func(ctx context.Context) error
	cloneFn                func(ctx context.Context, projectID, repoURL string) (string, string, error)
	recloneFn              func(ctx context.Context, projectID, currentRepoPath, repoURL string) (string, string, error)
	resolveRepoFn          func(ctx context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error)
	defaultBranchFn        func(ctx context.Context, repo *service.GitHubRepoRef) (string, error)
	publishBranchFn        func(ctx context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) error
	replaceBranchHeadFn    func(ctx context.Context, repo *service.GitHubRepoRef, req service.GitHubReplaceBranchHeadRequest) error
	getPullRequestFn       func(ctx context.Context, repo *service.GitHubRepoRef, number int) (*service.GitHubPullRequest, error)
	findPRFn               func(ctx context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error)
	createPRFn             func(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error)
	createIssueFn          func(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreateIssueRequest) (*service.GitHubIssue, error)
	getIssueFn             func(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) (*service.GitHubIssue, error)
	getAuthenticatedUserFn func(ctx context.Context) (*service.GitHubAuthenticatedUser, error)
	listMyAssignedIssuesFn func(ctx context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, []service.GitHubIssue, error)
	listAssignedIssuesFn   func(ctx context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssue, error)
	listAssignedIssuesPRFn func(ctx context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssueWithPullRequest, error)
	findIssuePRFn          func(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) (*service.GitHubPullRequest, error)
	listPRFeedbackFn       func(ctx context.Context, repo *service.GitHubRepoRef, prNumber int) ([]service.GitHubPullRequestFeedback, error)
	commentOnIssueFn       func(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int, bodyText string) error
	addLabelsToIssueFn     func(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int, labels []string) error
	globalAPIEndpoint      string
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

func (f *fakeGitHubService) DefaultBranch(ctx context.Context, repo *service.GitHubRepoRef) (string, error) {
	if f != nil && f.defaultBranchFn != nil {
		return f.defaultBranchFn(ctx, repo)
	}
	return "main", nil
}

func (f *fakeGitHubService) PublishBranch(ctx context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) error {
	if f != nil && f.publishBranchFn != nil {
		return f.publishBranchFn(ctx, repo, publishReq)
	}
	return nil
}

func (f *fakeGitHubService) ReplaceBranchHead(ctx context.Context, repo *service.GitHubRepoRef, req service.GitHubReplaceBranchHeadRequest) error {
	if f != nil && f.replaceBranchHeadFn != nil {
		return f.replaceBranchHeadFn(ctx, repo, req)
	}
	return nil
}

func (f *fakeGitHubService) GetPullRequest(ctx context.Context, repo *service.GitHubRepoRef, number int) (*service.GitHubPullRequest, error) {
	if f != nil && f.getPullRequestFn != nil {
		return f.getPullRequestFn(ctx, repo, number)
	}
	return nil, fmt.Errorf("get PR not configured")
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

func (f *fakeGitHubService) EnsureIssueLabels(context.Context, *service.GitHubRepoRef, []string) error {
	return nil
}

func (f *fakeGitHubService) CreateIssue(ctx context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
	if f != nil && f.createIssueFn != nil {
		return f.createIssueFn(ctx, repo, createReq)
	}
	return nil, fmt.Errorf("create issue not configured")
}

func (f *fakeGitHubService) GetIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) (*service.GitHubIssue, error) {
	if f != nil && f.getIssueFn != nil {
		return f.getIssueFn(ctx, repo, issueNumber)
	}
	return nil, fmt.Errorf("get issue not configured")
}

func (f *fakeGitHubService) GetAuthenticatedUser(ctx context.Context) (*service.GitHubAuthenticatedUser, error) {
	if f != nil && f.getAuthenticatedUserFn != nil {
		return f.getAuthenticatedUserFn(ctx)
	}
	return &service.GitHubAuthenticatedUser{Login: "openvibely", Source: service.GitHubAuthModePAT}, nil
}

func (f *fakeGitHubService) GetAuthenticatedUserForRepo(ctx context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, error) {
	return f.GetAuthenticatedUser(ctx)
}

func (f *fakeGitHubService) ListAuthenticatedAssignedIssues(ctx context.Context, repo *service.GitHubRepoRef) (*service.GitHubAuthenticatedUser, []service.GitHubIssue, error) {
	if f != nil && f.listMyAssignedIssuesFn != nil {
		return f.listMyAssignedIssuesFn(ctx, repo)
	}
	return nil, nil, fmt.Errorf("list my assigned issues not configured")
}

func (f *fakeGitHubService) ListAssignedIssues(ctx context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssue, error) {
	if f != nil && f.listAssignedIssuesFn != nil {
		return f.listAssignedIssuesFn(ctx, repo, assignee)
	}
	return nil, fmt.Errorf("list assigned issues not configured")
}

func (f *fakeGitHubService) ListAssignedIssuesWithPullRequests(ctx context.Context, repo *service.GitHubRepoRef, assignee string) ([]service.GitHubIssueWithPullRequest, error) {
	if f != nil && f.listAssignedIssuesPRFn != nil {
		return f.listAssignedIssuesPRFn(ctx, repo, assignee)
	}
	return nil, fmt.Errorf("list assigned issues with PRs not configured")
}

func (f *fakeGitHubService) FindPullRequestForIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int) (*service.GitHubPullRequest, error) {
	if f != nil && f.findIssuePRFn != nil {
		return f.findIssuePRFn(ctx, repo, issueNumber)
	}
	return nil, nil
}

func (f *fakeGitHubService) ListPullRequestFeedback(ctx context.Context, repo *service.GitHubRepoRef, prNumber int) ([]service.GitHubPullRequestFeedback, error) {
	if f != nil && f.listPRFeedbackFn != nil {
		return f.listPRFeedbackFn(ctx, repo, prNumber)
	}
	return nil, fmt.Errorf("list pull request feedback not configured")
}

func (f *fakeGitHubService) CommentOnIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int, bodyText string) error {
	if f != nil && f.commentOnIssueFn != nil {
		return f.commentOnIssueFn(ctx, repo, issueNumber, bodyText)
	}
	return fmt.Errorf("comment on issue not configured")
}

func (f *fakeGitHubService) AddLabelsToIssue(ctx context.Context, repo *service.GitHubRepoRef, issueNumber int, labels []string) error {
	if f != nil && f.addLabelsToIssueFn != nil {
		return f.addLabelsToIssueFn(ctx, repo, issueNumber, labels)
	}
	return fmt.Errorf("add labels to issue not configured")
}

func (f *fakeGitHubService) GlobalAPIEndpoint(_ context.Context) string {
	if f != nil {
		return f.globalAPIEndpoint
	}
	return ""
}
