package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/service/authz"
)

// Reads may take long enough for authorization to change. Recheck immediately
// before each mutation, including mutations reached through multi-call helpers.
// This cannot revoke a request already dispatched to GitHub.
type authorizedGitHubRuntimeProvider struct {
	GitHubIssueRuntimeProvider
	options githubIssueRuntimeOptions
}

func (p *authorizedGitHubRuntimeProvider) PublishBranch(ctx context.Context, repo *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return nil, err
	}
	return p.GitHubIssueRuntimeProvider.PublishBranch(ctx, repo, req)
}
func (p *authorizedGitHubRuntimeProvider) CreatePullRequest(ctx context.Context, repo *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return nil, err
	}
	return p.GitHubIssueRuntimeProvider.CreatePullRequest(ctx, repo, req)
}
func (p *authorizedGitHubRuntimeProvider) EnsureIssueLabels(ctx context.Context, repo *GitHubRepoRef, labels []string) error {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return err
	}
	return p.GitHubIssueRuntimeProvider.EnsureIssueLabels(ctx, repo, labels)
}
func (p *authorizedGitHubRuntimeProvider) CreateIssue(ctx context.Context, repo *GitHubRepoRef, req GitHubCreateIssueRequest) (*GitHubIssue, error) {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return nil, err
	}
	return p.GitHubIssueRuntimeProvider.CreateIssue(ctx, repo, req)
}
func (p *authorizedGitHubRuntimeProvider) CommentOnIssue(ctx context.Context, repo *GitHubRepoRef, number int, body string) error {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return err
	}
	return p.GitHubIssueRuntimeProvider.CommentOnIssue(ctx, repo, number, body)
}
func (p *authorizedGitHubRuntimeProvider) AddLabelsToIssue(ctx context.Context, repo *GitHubRepoRef, number int, labels []string) error {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return err
	}
	return p.GitHubIssueRuntimeProvider.AddLabelsToIssue(ctx, repo, number, labels)
}
func (p *authorizedGitHubRuntimeProvider) CloseIssue(ctx context.Context, repo *GitHubRepoRef, number int) error {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return err
	}
	return p.GitHubIssueRuntimeProvider.CloseIssue(ctx, repo, number)
}

// Preserve optional provider capabilities; wrapping must neither hide supported
// operations nor advertise capabilities an embedded provider does not have.
func guardGitHubRuntimeProvider(opts githubIssueRuntimeOptions) GitHubIssueRuntimeProvider {
	base := &authorizedGitHubRuntimeProvider{GitHubIssueRuntimeProvider: opts.GitHub, options: opts}
	body, hasBody := opts.GitHub.(taskPullRequestBodyUpdater)
	branch, hasBranch := opts.GitHub.(taskPullRequestBranchReplacer)
	if hasBody && hasBranch {
		return struct {
			GitHubIssueRuntimeProvider
			taskPullRequestBodyUpdater
			githubRuntimeBranchReplacement
		}{base, authorizedGitHubBodyUpdater{body, opts}, authorizedGitHubBranchReplacer{branch, opts}}
	}
	if hasBody {
		return struct {
			GitHubIssueRuntimeProvider
			taskPullRequestBodyUpdater
		}{base, authorizedGitHubBodyUpdater{body, opts}}
	}
	if hasBranch {
		return struct {
			GitHubIssueRuntimeProvider
			githubRuntimeBranchReplacement
		}{base, authorizedGitHubBranchReplacer{branch, opts}}
	}
	return base
}

type githubRuntimeBranchReplacement interface {
	ReplaceBranchHead(context.Context, *GitHubRepoRef, GitHubReplaceBranchHeadRequest) (string, error)
}
type authorizedGitHubBodyUpdater struct {
	provider taskPullRequestBodyUpdater
	options  githubIssueRuntimeOptions
}

func (p authorizedGitHubBodyUpdater) UpdatePullRequestBody(ctx context.Context, repo *GitHubRepoRef, number int, body string) error {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return err
	}
	return p.provider.UpdatePullRequestBody(ctx, repo, number, body)
}

type authorizedGitHubBranchReplacer struct {
	provider taskPullRequestBranchReplacer
	options  githubIssueRuntimeOptions
}

func (p authorizedGitHubBranchReplacer) ReplaceBranchHead(ctx context.Context, repo *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) (string, error) {
	ctx, err := authorizeGitHubRuntime(ctx, p.options, authz.Manage)
	if err != nil {
		return "", err
	}
	return p.provider.ReplaceBranchHead(ctx, repo, req)
}
