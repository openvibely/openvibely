package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type TaskPullRequestGitHubProvider interface {
	ResolveRepo(ctx context.Context, repoURL, repoPath string) (*GitHubRepoRef, error)
	DefaultBranch(ctx context.Context, repo *GitHubRepoRef) (string, error)
	PublishBranch(ctx context.Context, repo *GitHubRepoRef, publishReq GitHubPublishBranchRequest) error
	FindPullRequestByBranch(ctx context.Context, repo *GitHubRepoRef, branch string) (*GitHubPullRequest, error)
	CreatePullRequest(ctx context.Context, repo *GitHubRepoRef, createReq GitHubCreatePullRequestRequest) (*GitHubPullRequest, error)
	GlobalAPIEndpoint(ctx context.Context) string
}

type taskPullRequestBranchReplacer interface {
	GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error)
	ReplaceBranchHead(ctx context.Context, repo *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) error
}

type TaskPullRequestService struct {
	github TaskPullRequestGitHubProvider
	repo   *repository.TaskPullRequestRepo
}

type OpenTaskPullRequestOptions struct {
	Title         string
	Body          string
	Base          string
	Draft         bool
	CommitMessage string
	IssueNumber   *int
	IssueURL      string
}

type OpenTaskPullRequestResult struct {
	PullRequest          *GitHubPullRequest
	Record               *models.TaskPullRequest
	ReusedExistingRecord bool
	ReusedRemote         bool
	Created              bool
}

func NewTaskPullRequestService(github TaskPullRequestGitHubProvider, repo *repository.TaskPullRequestRepo) *TaskPullRequestService {
	return &TaskPullRequestService{github: github, repo: repo}
}

func (s *TaskPullRequestService) ReplaceBranchHeadForTask(ctx context.Context, project *models.Project, task *models.Task, expectedHead string) (*models.TaskPullRequest, error) {
	return s.replaceBranchHeadForTask(ctx, project, task, expectedHead)
}

func (s *TaskPullRequestService) ReplaceBranchHeadForAutomationTask(ctx context.Context, project *models.Project, task *models.Task, expectedHead string) (*models.TaskPullRequest, error) {
	return s.replaceBranchHeadForTask(ctx, project, task, expectedHead)
}

func (s *TaskPullRequestService) replaceBranchHeadForTask(ctx context.Context, project *models.Project, task *models.Task, expectedHead string) (*models.TaskPullRequest, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	if project == nil {
		return nil, fmt.Errorf("project is required")
	}
	if task.ProjectID != "" && project.ID != "" && task.ProjectID != project.ID {
		return nil, fmt.Errorf("task does not belong to project")
	}
	if strings.TrimSpace(task.WorktreePath) == "" {
		return nil, fmt.Errorf("task has no worktree path")
	}
	if strings.TrimSpace(task.WorktreeBranch) == "" {
		return nil, fmt.Errorf("task has no worktree branch")
	}
	if s == nil || s.github == nil {
		return nil, fmt.Errorf("github integration is not configured")
	}
	if s.repo == nil {
		return nil, fmt.Errorf("task pull request repository not available")
	}
	if strings.TrimSpace(project.RepoPath) == "" {
		return nil, fmt.Errorf("project has no repository path configured")
	}
	existingPR, err := s.repo.GetByTaskID(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("checking existing pull request: %w", err)
	}
	if existingPR == nil {
		return nil, fmt.Errorf("task has no linked pull request to replace")
	}
	replacer, ok := s.github.(taskPullRequestBranchReplacer)
	if !ok {
		return nil, fmt.Errorf("github integration does not support branch replacement")
	}
	repoPathForResolution := ""
	if strings.TrimSpace(project.RepoURL) == "" {
		repoPathForResolution = project.RepoPath
	}
	repoRef, err := s.github.ResolveRepo(ctx, project.RepoURL, repoPathForResolution)
	if err != nil {
		return nil, fmt.Errorf("resolving repository: %w", err)
	}
	if err := ConfigureGitHubRepoEndpoint(repoRef, s.github.GlobalAPIEndpoint(ctx)); err != nil {
		return nil, fmt.Errorf("configuring GitHub API endpoint: %w", err)
	}
	linkedPR, err := replacer.GetPullRequest(ctx, repoRef, existingPR.PRNumber)
	if err != nil {
		return nil, fmt.Errorf("fetching linked pull request: %w", err)
	}
	if linkedPR == nil {
		return nil, fmt.Errorf("linked pull request #%d was not found", existingPR.PRNumber)
	}
	expectedRepo := strings.TrimSpace(repoRef.FullName)
	if expectedRepo == "" {
		expectedRepo = strings.Trim(strings.TrimSpace(repoRef.Owner)+"/"+strings.TrimSpace(repoRef.Name), "/")
	}
	if expectedRepo == "" || !strings.EqualFold(strings.TrimSpace(linkedPR.HeadRepoFullName), expectedRepo) {
		return nil, fmt.Errorf("linked pull request #%d head repository %q does not match project repository %q", existingPR.PRNumber, linkedPR.HeadRepoFullName, expectedRepo)
	}
	if strings.TrimSpace(linkedPR.HeadRef) != strings.TrimSpace(task.WorktreeBranch) {
		return nil, fmt.Errorf("linked pull request #%d head branch %q does not match task worktree branch %q", existingPR.PRNumber, linkedPR.HeadRef, task.WorktreeBranch)
	}
	if err := replacer.ReplaceBranchHead(ctx, repoRef, GitHubReplaceBranchHeadRequest{
		WorktreePath: task.WorktreePath,
		Branch:       task.WorktreeBranch,
		ExpectedHead: expectedHead,
	}); err != nil {
		return nil, fmt.Errorf("replacing pull request branch head: %w", err)
	}
	return existingPR, nil
}

func (s *TaskPullRequestService) OpenForTask(ctx context.Context, project *models.Project, task *models.Task, opts OpenTaskPullRequestOptions) (*OpenTaskPullRequestResult, error) {
	return s.openForTask(ctx, project, task, opts)
}

func (s *TaskPullRequestService) OpenForAutomationTask(ctx context.Context, project *models.Project, task *models.Task, opts OpenTaskPullRequestOptions) (*OpenTaskPullRequestResult, error) {
	return s.openForTask(ctx, project, task, opts)
}

func (s *TaskPullRequestService) openForTask(ctx context.Context, project *models.Project, task *models.Task, opts OpenTaskPullRequestOptions) (*OpenTaskPullRequestResult, error) {
	if task == nil {
		return nil, fmt.Errorf("task is required")
	}
	if project == nil {
		return nil, fmt.Errorf("project is required")
	}
	if strings.TrimSpace(task.WorktreeBranch) == "" {
		return nil, fmt.Errorf("task has no worktree branch")
	}
	if s == nil || s.github == nil {
		return nil, fmt.Errorf("github integration is not configured")
	}
	if s.repo == nil {
		return nil, fmt.Errorf("task pull request repository not available")
	}
	if strings.TrimSpace(project.RepoPath) == "" {
		return nil, fmt.Errorf("project has no repository path configured")
	}

	existingPR, err := s.repo.GetByTaskID(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("checking existing pull request: %w", err)
	}

	repoPathForResolution := ""
	if strings.TrimSpace(project.RepoURL) == "" {
		repoPathForResolution = project.RepoPath
	}
	repoRef, err := s.github.ResolveRepo(ctx, project.RepoURL, repoPathForResolution)
	if err != nil {
		return nil, fmt.Errorf("resolving repository: %w", err)
	}
	if err := ConfigureGitHubRepoEndpoint(repoRef, s.github.GlobalAPIEndpoint(ctx)); err != nil {
		return nil, fmt.Errorf("configuring GitHub API endpoint: %w", err)
	}

	createReq := s.buildCreatePullRequestRequest(ctx, project, task, opts, repoRef)
	commitMessage := strings.TrimSpace(opts.CommitMessage)
	if commitMessage == "" && strings.TrimSpace(task.WorktreePath) != "" {
		commitMessage = BuildWorktreeCommitMessage(task.WorktreePath, WorktreeCommitMessageContext{Phase: WorktreeCommitPhaseMerge, TaskTitle: task.Title})
	}
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("Prepare task %s", task.ID)
	}
	if err := s.github.PublishBranch(ctx, repoRef, GitHubPublishBranchRequest{
		RepoPath:       project.RepoPath,
		WorktreePath:   task.WorktreePath,
		Branch:         task.WorktreeBranch,
		BaseBranch:     createReq.Base,
		CommitMessage:  commitMessage,
		CommitterName:  "OpenVibely Bot",
		CommitterEmail: "bot@openvibely.ai",
	}); err != nil {
		return nil, fmt.Errorf("publishing branch: %w", err)
	}
	if existingPR != nil {
		if mergeTaskPullRequestIssueMetadata(existingPR, opts) {
			if err := s.repo.Upsert(ctx, existingPR); err != nil {
				return nil, fmt.Errorf("saving pull request issue metadata: %w", err)
			}
		}
		return &OpenTaskPullRequestResult{
			PullRequest:          taskPullRequestRecordToGitHubPR(existingPR),
			Record:               existingPR,
			ReusedExistingRecord: true,
		}, nil
	}

	foundPR, err := s.github.FindPullRequestByBranch(ctx, repoRef, task.WorktreeBranch)
	if err != nil {
		return nil, fmt.Errorf("finding pull request: %w", err)
	}

	pr := foundPR
	created := false
	if pr == nil {
		pr, err = s.github.CreatePullRequest(ctx, repoRef, createReq)
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "pull request already exists") {
				retryPR, findErr := s.github.FindPullRequestByBranch(ctx, repoRef, task.WorktreeBranch)
				if findErr == nil && retryPR != nil {
					pr = retryPR
				} else {
					return nil, fmt.Errorf("creating pull request: %w", err)
				}
			} else {
				return nil, fmt.Errorf("creating pull request: %w", err)
			}
		} else {
			created = true
		}
	}
	if pr == nil {
		return nil, fmt.Errorf("pull request was not created or found")
	}

	record := &models.TaskPullRequest{
		TaskID:      task.ID,
		PRNumber:    pr.Number,
		PRURL:       pr.URL,
		PRState:     pr.State,
		IssueNumber: opts.IssueNumber,
		IssueURL:    strings.TrimSpace(opts.IssueURL),
	}
	if err := s.repo.Upsert(ctx, record); err != nil {
		return nil, fmt.Errorf("saving pull request record: %w", err)
	}

	return &OpenTaskPullRequestResult{
		PullRequest:  pr,
		Record:       record,
		ReusedRemote: foundPR != nil || !created,
		Created:      created,
	}, nil
}

func (s *TaskPullRequestService) buildCreatePullRequestRequest(ctx context.Context, project *models.Project, task *models.Task, opts OpenTaskPullRequestOptions, repoRef *GitHubRepoRef) GitHubCreatePullRequestRequest {
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = task.Title
	}
	body := strings.TrimSpace(opts.Body)
	if body == "" {
		summary := strings.TrimSpace(task.Title)
		if opts.IssueNumber != nil {
			summary = strings.TrimSpace(strings.TrimSuffix(summary, fmt.Sprintf("(#%d)", *opts.IssueNumber)))
		}
		body = "## Summary\n- " + summary
		if opts.IssueNumber != nil {
			body += fmt.Sprintf("\n\nCloses #%d", *opts.IssueNumber)
		}
	}
	base := strings.TrimSpace(opts.Base)
	if base == "" {
		base = strings.TrimSpace(task.MergeTargetBranch)
	}
	if base == "" && s.github != nil && repoRef != nil {
		if defaultBranch, err := s.github.DefaultBranch(ctx, repoRef); err == nil {
			base = strings.TrimSpace(defaultBranch)
		}
	}
	if base == "" {
		base = GetDefaultBranch(project.RepoPath)
	}
	if base == "" {
		base = "main"
	}
	return GitHubCreatePullRequestRequest{
		Title: title,
		Body:  body,
		Head:  task.WorktreeBranch,
		Base:  base,
		Draft: opts.Draft,
	}
}

func mergeTaskPullRequestIssueMetadata(record *models.TaskPullRequest, opts OpenTaskPullRequestOptions) bool {
	if record == nil {
		return false
	}
	changed := false
	issueURL := strings.TrimSpace(opts.IssueURL)
	if opts.IssueNumber != nil && (record.IssueNumber == nil || *record.IssueNumber != *opts.IssueNumber) {
		issueNumber := *opts.IssueNumber
		record.IssueNumber = &issueNumber
		if issueURL == "" && record.IssueURL != "" {
			record.IssueURL = ""
		}
		changed = true
	}
	if issueURL != "" && record.IssueURL != issueURL {
		record.IssueURL = issueURL
		if opts.IssueNumber == nil && record.IssueNumber != nil {
			record.IssueNumber = nil
		}
		changed = true
	}
	return changed
}

func taskPullRequestRecordToGitHubPR(record *models.TaskPullRequest) *GitHubPullRequest {
	if record == nil {
		return nil
	}
	return &GitHubPullRequest{Number: record.PRNumber, URL: record.PRURL, State: record.PRState}
}
