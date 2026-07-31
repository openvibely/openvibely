package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type prFeedbackCall struct {
	repoFullName string
	prNumber     int
}

type fakePRFeedbackProvider struct {
	authenticatedUser *GitHubAuthenticatedUser
	items             []GitHubPullRequestFeedback
	itemsByCall       map[prFeedbackCall][]GitHubPullRequestFeedback
	calls             []prFeedbackCall
}

func (f *fakePRFeedbackProvider) GetAuthenticatedUser(ctx context.Context) (*GitHubAuthenticatedUser, error) {
	if f.authenticatedUser != nil {
		return f.authenticatedUser, nil
	}
	return &GitHubAuthenticatedUser{Login: "openvibely", Source: GitHubAuthModePAT}, nil
}

func (f *fakePRFeedbackProvider) ListPullRequestFeedback(ctx context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	call := prFeedbackCall{prNumber: prNumber}
	if repo != nil {
		call.repoFullName = repo.FullName
	}
	f.calls = append(f.calls, call)
	if f.itemsByCall != nil {
		return f.itemsByCall[call], nil
	}
	return f.items, nil
}

func TestGitHubPRFeedbackForwarderQueuesAuthorizedFeedbackOnce(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	authRepo := repository.NewGitHubAuthRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{ID: "proj-pr-feedback", Name: "PR Feedback", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ID: "task-pr-feedback", ProjectID: project.ID, Title: "Implement issue", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	for _, login := range []string{"Alice", "openvibely", "ci-bot"} {
		if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: login, Permission: "triage", AddedBy: "test"}); err != nil {
			t.Fatalf("authorize actor %s: %v", login, err)
		}
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	provider := &fakePRFeedbackProvider{items: []GitHubPullRequestFeedback{
		{Kind: "issue_comment", ID: "100", AuthorLogin: "alice", AuthorType: "User", Body: "Please add tests.", URL: "https://github.com/openvibely/openvibely/pull/42#issuecomment-100", CreatedAt: time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)},
		{Kind: "review_comment", ID: "101", AuthorLogin: "mallory", AuthorType: "User", Body: "Unauthorized steer", URL: "https://github.com/openvibely/openvibely/pull/42#discussion_r101", CreatedAt: time.Date(2026, 7, 9, 11, 0, 0, 0, time.UTC)},
		{Kind: "issue_comment", ID: "102", AuthorLogin: "openvibely", AuthorType: "User", Body: "Self-authored bot steer", URL: "https://github.com/openvibely/openvibely/pull/42#issuecomment-102", CreatedAt: time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)},
		{Kind: "review", ID: "103", AuthorLogin: "ci-bot", AuthorType: "Bot", Body: "Automated review", State: "commented", URL: "https://github.com/openvibely/openvibely/pull/42#pullrequestreview-103", CreatedAt: time.Date(2026, 7, 9, 13, 0, 0, 0, time.UTC)},
	}}

	forwarder := NewGitHubPRFeedbackForwarder(provider, prRepo, feedbackRepo, authRepo, threadInputRepo)
	result, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely"})
	if err != nil {
		t.Fatalf("forward feedback: %v", err)
	}
	if len(result.Forwarded) != 1 || result.SkippedUnauthorized != 1 || result.SkippedSelfOrBot != 2 || result.SkippedDuplicate != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	pending, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected one queued task message, got %d", len(pending))
	}
	if !strings.Contains(pending[0].Content, "Please add tests.") || !strings.Contains(pending[0].Content, "@alice") {
		t.Fatalf("queued feedback missing content/author: %q", pending[0].Content)
	}

	second, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely"})
	if err != nil {
		t.Fatalf("forward feedback second time: %v", err)
	}
	if len(second.Forwarded) != 0 || second.SkippedDuplicate != 1 {
		t.Fatalf("expected duplicate skip on second run, got %#v", second)
	}
	pending, err = threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending second: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected no duplicate queued messages, got %d", len(pending))
	}
}

func TestGitHubPRFeedbackForwarderDeduplicatesPreexistingMixedCaseRepository(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	authRepo := repository.NewGitHubAuthRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{Name: "Mixed Case Feedback", RepoPath: t.TempDir(), RepoURL: "https://github.com/Owner/Repo"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Mixed case PR", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "alice", Permission: "triage", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize actor: %v", err)
	}
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/Owner/Repo/pull/42", PRState: "open"}
	if err := prRepo.Upsert(ctx, pr); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO github_pr_feedback_forwarded (
		task_pull_request_id, task_id, repo_full_name, pr_number, feedback_kind, github_id,
		author_login, body, created_at
	) VALUES (?, ?, 'Owner/Repo', 42, 'issue_comment', 'mixed-case-existing', 'alice', 'Already forwarded.', '2026-07-09T10:00:00Z')`, pr.ID, task.ID); err != nil {
		t.Fatalf("seed mixed-case feedback: %v", err)
	}

	provider := &fakePRFeedbackProvider{items: []GitHubPullRequestFeedback{{
		Kind: "issue_comment", ID: "mixed-case-existing", AuthorLogin: "alice", AuthorType: "User", Body: "Already forwarded.",
	}}}
	forwarder := NewGitHubPRFeedbackForwarder(provider, prRepo, feedbackRepo, authRepo, threadInputRepo)
	result, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{Owner: "owner", Name: "repo", FullName: "owner/repo"})
	if err != nil {
		t.Fatalf("forward feedback: %v", err)
	}
	if len(result.Forwarded) != 0 || result.SkippedDuplicate != 1 {
		t.Fatalf("result = %#v, want one mixed-case duplicate skip", result)
	}
	pending, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("mixed-case duplicate queued feedback: %#v", pending)
	}
}

func TestGitHubPRFeedbackRepoAtomicallyDeduplicatesMixedCaseRepository(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{Name: "Atomic Mixed Case Feedback", RepoPath: t.TempDir()}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Atomic mixed case PR", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	pr := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/Owner/Repo/pull/42", PRState: "open"}
	if err := prRepo.Upsert(ctx, pr); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO github_pr_feedback_forwarded (
		task_pull_request_id, task_id, repo_full_name, pr_number, feedback_kind, github_id,
		author_login, body, created_at
	) VALUES (?, ?, 'Owner/Repo', 42, 'issue_comment', 'mixed-case-atomic', 'alice', 'Already recorded.', '2026-07-09T10:00:00Z')`, pr.ID, task.ID); err != nil {
		t.Fatalf("seed mixed-case feedback: %v", err)
	}

	feedback := &models.GitHubPRFeedbackForwarded{
		TaskPullRequestID: pr.ID,
		TaskID:            task.ID,
		RepoFullName:      "owner/repo",
		PRNumber:          42,
		FeedbackKind:      "issue_comment",
		GitHubID:          "mixed-case-atomic",
		AuthorLogin:       "alice",
		Body:              "Already recorded.",
		CreatedAt:         time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC),
	}
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, Content: "duplicate"}
	recorded, err := feedbackRepo.RecordForwardedAndQueue(ctx, threadInputRepo, feedback, input)
	if err != nil {
		t.Fatalf("record duplicate feedback: %v", err)
	}
	if recorded {
		t.Fatal("mixed-case duplicate was recorded")
	}
	pending, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("mixed-case duplicate queued feedback: %#v", pending)
	}
}

func TestGitHubPRFeedbackForwarderDoesNotFetchPersistedPRFromSelectedRepository(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	authRepo := repository.NewGitHubAuthRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{ID: "proj-pr-repo-mismatch", Name: "PR Repo Mismatch", RepoPath: t.TempDir(), RepoURL: "https://github.com/example/repo-b"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ID: "task-repo-a-pr", ProjectID: project.ID, Title: "Repo A PR", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "alice", Permission: "triage", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize actor: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo-a/pull/42", PRState: "open"}); err != nil {
		t.Fatalf("upsert pr: %v", err)
	}

	provider := &fakePRFeedbackProvider{itemsByCall: map[prFeedbackCall][]GitHubPullRequestFeedback{
		{repoFullName: "example/repo-b", prNumber: 42}: {{Kind: "issue_comment", ID: "repo-b-feedback", AuthorLogin: "alice", AuthorType: "User", Body: "Feedback from the wrong repository."}},
	}}
	forwarder := NewGitHubPRFeedbackForwarder(provider, prRepo, feedbackRepo, authRepo, threadInputRepo)
	result, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{Owner: "example", Name: "repo-b", FullName: "example/repo-b"})
	if err != nil {
		t.Fatalf("forward feedback: %v", err)
	}
	if len(result.Forwarded) != 0 {
		t.Fatalf("wrong-repository feedback was forwarded: %#v", result.Forwarded)
	}
	wantCalls := []prFeedbackCall{{repoFullName: "example/repo-a", prNumber: 42}}
	if len(provider.calls) != len(wantCalls) || provider.calls[0] != wantCalls[0] {
		t.Fatalf("provider calls = %#v, want %#v", provider.calls, wantCalls)
	}
	pending, err := threadInputRepo.ListPendingForTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no queued feedback, got %#v", pending)
	}
}

func TestGitHubPRFeedbackForwarderUsesPersistedRepositoryForEachPR(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	authRepo := repository.NewGitHubAuthRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{ID: "proj-multi-pr-repos", Name: "Multiple PR Repos", RepoPath: t.TempDir(), RepoURL: "https://github.com/example/current"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "alice", Permission: "triage", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize actor: %v", err)
	}
	prs := []struct {
		name string
		url  string
	}{
		{name: "task-repo-a", url: "https://github.com/example/repo-a/pull/42"},
		{name: "task-repo-b", url: "https://github.com/example/repo-b/pull/42"},
		{name: "task-malformed-pr", url: "https://github.com/example/repo-c/issues/42"},
		{name: "task-unsupported-pr", url: "https://gitlab.com/example/repo-d/pull/42"},
		{name: "task-number-mismatch", url: "https://github.com/example/repo-e/pull/41"},
	}
	taskIDs := make(map[string]string, len(prs))
	for _, stored := range prs {
		task := &models.Task{ProjectID: project.ID, Title: stored.name, Category: models.CategoryActive, Status: models.StatusPending}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", stored.name, err)
		}
		taskIDs[stored.name] = task.ID
		if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: stored.url, PRState: "open"}); err != nil {
			t.Fatalf("upsert pr %s: %v", stored.name, err)
		}
	}

	provider := &fakePRFeedbackProvider{itemsByCall: map[prFeedbackCall][]GitHubPullRequestFeedback{
		{repoFullName: "example/repo-a", prNumber: 42}: {{Kind: "issue_comment", ID: "shared-feedback-id", AuthorLogin: "alice", AuthorType: "User", Body: "Repo A feedback."}},
		{repoFullName: "example/repo-b", prNumber: 42}: {{Kind: "issue_comment", ID: "shared-feedback-id", AuthorLogin: "alice", AuthorType: "User", Body: "Repo B feedback."}},
	}}
	forwarder := NewGitHubPRFeedbackForwarder(provider, prRepo, feedbackRepo, authRepo, threadInputRepo)
	result, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{Owner: "example", Name: "current", FullName: "example/current"})
	if err != nil {
		t.Fatalf("forward feedback: %v", err)
	}
	if len(result.Forwarded) != 2 {
		t.Fatalf("forwarded = %#v, want two repository-scoped items", result.Forwarded)
	}
	callCounts := make(map[prFeedbackCall]int)
	for _, call := range provider.calls {
		callCounts[call]++
	}
	for _, want := range []prFeedbackCall{{repoFullName: "example/repo-a", prNumber: 42}, {repoFullName: "example/repo-b", prNumber: 42}} {
		if callCounts[want] != 1 {
			t.Fatalf("provider calls = %#v, want one call for %#v", provider.calls, want)
		}
	}
	if len(provider.calls) != 2 {
		t.Fatalf("provider calls = %#v, malformed PR URL should be skipped", provider.calls)
	}
	second, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{Owner: "example", Name: "current", FullName: "example/current"})
	if err != nil {
		t.Fatalf("forward feedback second time: %v", err)
	}
	if len(second.Forwarded) != 0 || second.SkippedDuplicate != 2 {
		t.Fatalf("second result = %#v, want two repository-scoped duplicate skips", second)
	}
	secondCallCounts := make(map[prFeedbackCall]int)
	for _, call := range provider.calls {
		secondCallCounts[call]++
	}
	for _, want := range []prFeedbackCall{{repoFullName: "example/repo-a", prNumber: 42}, {repoFullName: "example/repo-b", prNumber: 42}} {
		if secondCallCounts[want] != 2 {
			t.Fatalf("provider calls after second run = %#v, want two calls for %#v", provider.calls, want)
		}
	}
	if len(provider.calls) != 4 {
		t.Fatalf("provider calls after second run = %#v, invalid PR URLs should remain skipped", provider.calls)
	}
	for taskName, wantBody := range map[string]string{"task-repo-a": "Repo A feedback.", "task-repo-b": "Repo B feedback."} {
		pending, err := threadInputRepo.ListPendingForTask(ctx, taskIDs[taskName])
		if err != nil {
			t.Fatalf("list pending for %s: %v", taskName, err)
		}
		if len(pending) != 1 || !strings.Contains(pending[0].Content, wantBody) {
			t.Fatalf("pending for %s = %#v, want body %q", taskName, pending, wantBody)
		}
	}
	for _, taskName := range []string{"task-malformed-pr", "task-unsupported-pr", "task-number-mismatch"} {
		pending, err := threadInputRepo.ListPendingForTask(ctx, taskIDs[taskName])
		if err != nil {
			t.Fatalf("list invalid pending for %s: %v", taskName, err)
		}
		if len(pending) != 0 {
			t.Fatalf("invalid PR URL for %s queued feedback: %#v", taskName, pending)
		}
	}
}
