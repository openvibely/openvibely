package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type prFeedbackCall struct {
	repoFullName string
	apiBaseURL   string
	prNumber     int
}

type fakePRFeedbackProvider struct {
	authenticatedUser *GitHubAuthenticatedUser
	items             []GitHubPullRequestFeedback
	itemsFn           func() []GitHubPullRequestFeedback
	itemsByCall       map[prFeedbackCall][]GitHubPullRequestFeedback
	calls             []prFeedbackCall
}

func (f *fakePRFeedbackProvider) GetAuthenticatedUserForRepo(ctx context.Context, repo *GitHubRepoRef) (*GitHubAuthenticatedUser, error) {
	if f.authenticatedUser != nil {
		return f.authenticatedUser, nil
	}
	return &GitHubAuthenticatedUser{Login: "openvibely", Source: GitHubAuthModePAT}, nil
}

func (f *fakePRFeedbackProvider) ListPullRequestFeedback(ctx context.Context, repo *GitHubRepoRef, prNumber int) ([]GitHubPullRequestFeedback, error) {
	call := prFeedbackCall{prNumber: prNumber}
	if repo != nil {
		call.repoFullName = repo.FullName
		call.apiBaseURL = repo.APIBaseURL
	}
	f.calls = append(f.calls, call)
	if f.itemsByCall != nil {
		return f.itemsByCall[call], nil
	}
	if f.itemsFn != nil {
		return f.itemsFn(), nil
	}
	return f.items, nil
}

type synchronizedPRFeedbackProvider struct {
	entered chan<- struct{}
	release <-chan struct{}
	items   []GitHubPullRequestFeedback
}

func (p *synchronizedPRFeedbackProvider) GetAuthenticatedUserForRepo(context.Context, *GitHubRepoRef) (*GitHubAuthenticatedUser, error) {
	return &GitHubAuthenticatedUser{Login: "openvibely", Source: GitHubAuthModePAT}, nil
}

func (p *synchronizedPRFeedbackProvider) ListPullRequestFeedback(ctx context.Context, _ *GitHubRepoRef, _ int) ([]GitHubPullRequestFeedback, error) {
	select {
	case p.entered <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return append([]GitHubPullRequestFeedback(nil), p.items...), nil
}

func TestGitHubPRFeedbackForwarderResolvesPATIdentityThroughEnterpriseEndpoint(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	settingsRepo := repository.NewSettingsRepo(db)
	if err := settingsRepo.Set(ctx, GitHubSettingPAT, "enterprise-token"); err != nil {
		t.Fatalf("set PAT: %v", err)
	}
	if err := settingsRepo.Set(ctx, GitHubSettingPATUserLogin, "cached-public-user"); err != nil {
		t.Fatalf("set cached public PAT user: %v", err)
	}

	var publicRequests atomic.Int32
	publicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"login":"wrong-public-user"}`))
	}))
	defer publicServer.Close()

	var enterpriseUserRequests atomic.Int32
	enterpriseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/user" {
			enterpriseUserRequests.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer enterprise-token" {
				t.Errorf("enterprise /user authorization = %q", got)
			}
			_, _ = w.Write([]byte(`{"login":"enterprise-user"}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}))
	defer enterpriseServer.Close()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Enterprise Identity", RepoPath: t.TempDir(), RepoURL: "https://github.example.com/acme/widgets"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Enterprise PR", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.example.com/acme/widgets/pull/42", PRState: "open"}); err != nil {
		t.Fatalf("upsert PR: %v", err)
	}

	github := NewGitHubService(settingsRepo, "", "", "", "")
	github.apiBaseURL = publicServer.URL
	forwarder := NewGitHubPRFeedbackForwarder(
		github,
		prRepo,
		repository.NewGitHubPRFeedbackRepo(db),
		repository.NewGitHubAuthRepo(db),
		repository.NewThreadInputRepo(db),
	)
	_, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, &GitHubRepoRef{
		Owner:      "acme",
		Name:       "widgets",
		FullName:   "acme/widgets",
		HTMLURL:    "https://github.example.com/acme/widgets",
		APIBaseURL: enterpriseServer.URL,
	})
	if err != nil {
		t.Fatalf("forward feedback: %v", err)
	}
	if got := publicRequests.Load(); got != 0 {
		t.Fatalf("public/default GitHub endpoint received %d request(s)", got)
	}
	if got := enterpriseUserRequests.Load(); got != 1 {
		t.Fatalf("enterprise /user requests = %d, want 1", got)
	}
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

func TestParsePersistedGitHubPullRequestURLMatchesExplicitDefaultHTTPSPort(t *testing.T) {
	selectedRepo, err := ParseGitHubRepoURL("https://github.example.com:443/acme/widgets")
	if err != nil {
		t.Fatalf("parse selected repository: %v", err)
	}
	if err := ConfigureGitHubRepoEndpoint(&selectedRepo, "https://github.example.com/api/v3"); err != nil {
		t.Fatalf("configure selected repository endpoint: %v", err)
	}

	parsed, err := parsePersistedGitHubPullRequestURL("https://github.example.com/acme/widgets/pull/42", 42, &selectedRepo)
	if err != nil {
		t.Fatalf("parse persisted PR URL: %v", err)
	}
	if parsed.FullName != "acme/widgets" || parsed.APIBaseURL != "https://github.example.com/api/v3" {
		t.Fatalf("parsed repository = %#v", parsed)
	}
}

func TestGitHubPRFeedbackForwarderUsesEnterpriseHostAndAPIEndpoint(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	authRepo := repository.NewGitHubAuthRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)

	project := &models.Project{ID: "proj-enterprise-pr-feedback", Name: "Enterprise PR Feedback", RepoPath: t.TempDir(), RepoURL: "https://github.example.com:8443/acme/widgets"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "alice", Permission: "triage", AddedBy: "test"}); err != nil {
		t.Fatalf("authorize actor: %v", err)
	}
	var enterpriseTaskID string
	for _, stored := range []struct {
		id  string
		url string
	}{
		{id: "task-enterprise-pr", url: "https://github.example.com:8443/acme/widgets/pull/42"},
		{id: "task-foreign-pr", url: "https://attacker.example.com/acme/widgets/pull/43"},
	} {
		task := &models.Task{ID: stored.id, ProjectID: project.ID, Title: stored.id, Category: models.CategoryActive, Status: models.StatusPending}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task %s: %v", stored.id, err)
		}
		if stored.id == "task-enterprise-pr" {
			enterpriseTaskID = task.ID
		}
		prNumber := 42
		if stored.id == "task-foreign-pr" {
			prNumber = 43
		}
		if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: prNumber, PRURL: stored.url, PRState: "open"}); err != nil {
			t.Fatalf("upsert PR %s: %v", stored.id, err)
		}
	}

	provider := &fakePRFeedbackProvider{items: []GitHubPullRequestFeedback{{
		Kind: "issue_comment", ID: "enterprise-feedback", AuthorLogin: "alice", AuthorType: "User", Body: "Enterprise feedback.",
	}}}
	parsedRepo, err := ParseGitHubRepoURL(project.RepoURL)
	if err != nil {
		t.Fatalf("parse selected repository: %v", err)
	}
	if err := ConfigureGitHubRepoEndpoint(&parsedRepo, "https://github.example.com/api/v3"); err != nil {
		t.Fatalf("configure selected repository endpoint: %v", err)
	}
	selectedRepo := &parsedRepo
	forwarder := NewGitHubPRFeedbackForwarder(provider, prRepo, feedbackRepo, authRepo, threadInputRepo)
	result, err := forwarder.ForwardAuthorizedFeedback(ctx, project.ID, selectedRepo)
	if err != nil {
		t.Fatalf("forward feedback: %v", err)
	}
	if len(result.Forwarded) != 1 || result.Forwarded[0].TaskID != enterpriseTaskID {
		t.Fatalf("forwarded = %#v, want only Enterprise PR feedback", result.Forwarded)
	}
	wantCalls := []prFeedbackCall{{repoFullName: "acme/widgets", apiBaseURL: "https://github.example.com/api/v3", prNumber: 42}}
	if len(provider.calls) != len(wantCalls) || provider.calls[0] != wantCalls[0] {
		t.Fatalf("provider calls = %#v, want %#v", provider.calls, wantCalls)
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

type githubPRFeedbackForwarderFixture struct {
	db              *sql.DB
	project         *models.Project
	task            *models.Task
	forwarder       *GitHubPRFeedbackForwarder
	provider        *fakePRFeedbackProvider
	counter         *testutil.SQLStatementCounter
	threadInputRepo *repository.ThreadInputRepo
}

func (f githubPRFeedbackForwarderFixture) resetForwardingState(tb testing.TB) {
	tb.Helper()
	if _, err := f.db.ExecContext(context.Background(), `DELETE FROM github_pr_feedback_forwarded WHERE task_id = ?`, f.task.ID); err != nil {
		tb.Fatalf("reset forwarded feedback: %v", err)
	}
	if _, err := f.db.ExecContext(context.Background(), `DELETE FROM thread_inputs WHERE task_id = ?`, f.task.ID); err != nil {
		tb.Fatalf("reset queued feedback: %v", err)
	}
}

func (f githubPRFeedbackForwarderFixture) forward(ctx context.Context, cached bool) (*GitHubPRFeedbackForwardResult, error) {
	if cached {
		return f.forwarder.ForwardAuthorizedFeedback(ctx, f.project.ID, githubPRFeedbackTestRepo())
	}
	return f.forwarder.forwardAuthorizedFeedback(ctx, f.project.ID, githubPRFeedbackTestRepo(), nil)
}

func TestGitHubPRFeedbackForwarderCachesAuthorizationDecisionsPerPass(t *testing.T) {
	cases := []struct {
		name              string
		itemCount         int
		authors           []string
		authorizedLogins  []string
		wantAuthorization int
		wantForwarded     int
		wantSkippedUnauth int
		verifySecondPass  bool
	}{
		{name: "one authorized feedback item", itemCount: 1, authors: []string{"Alice"}, authorizedLogins: []string{"alice"}, wantAuthorization: 1, wantForwarded: 1},
		{name: "ten mixed case authorized feedback items", itemCount: 10, authors: []string{"Alice", "alice"}, authorizedLogins: []string{"alice"}, wantAuthorization: 1, wantForwarded: 10, verifySecondPass: true},
		{name: "one hundred mixed case authorized feedback items", itemCount: 100, authors: []string{"Alice", "alice"}, authorizedLogins: []string{"alice"}, wantAuthorization: 1, wantForwarded: 100},
		{name: "one hundred feedback items from five authorized reviewers", itemCount: 100, authors: []string{"Alice", "BOB", "carol", "DAVE", "eve"}, authorizedLogins: []string{"alice", "bob", "carol", "dave", "eve"}, wantAuthorization: 5, wantForwarded: 100},
		{name: "one hundred unauthorized feedback items", itemCount: 100, authors: []string{"Mallory", "mallory"}, wantAuthorization: 1, wantSkippedUnauth: 100},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newGitHubPRFeedbackForwarderFixture(t, tc.authorizedLogins, realisticGitHubPRFeedbackItems(tc.itemCount, tc.authors, "feedback"))

			fixture.counter.Reset()
			fixture.counter.SetEnabled(true)
			result, err := fixture.forwarder.ForwardAuthorizedFeedback(context.Background(), fixture.project.ID, githubPRFeedbackTestRepo())
			fixture.counter.SetEnabled(false)
			if err != nil {
				t.Fatalf("forward feedback: %v", err)
			}
			statements := fixture.counter.Statements()
			if got := countGitHubAuthorizationLookups(statements); got != tc.wantAuthorization {
				t.Fatalf("authorization lookups = %d, want %d; statements: %q", got, tc.wantAuthorization, statements)
			}
			if len(statements) < tc.wantAuthorization {
				t.Fatalf("total SQL statements = %d, want at least %d", len(statements), tc.wantAuthorization)
			}
			if got := len(result.Forwarded); got != tc.wantForwarded || result.SkippedUnauthorized != tc.wantSkippedUnauth {
				t.Fatalf("forward result = %#v, want forwarded=%d skipped_unauthorized=%d", result, tc.wantForwarded, tc.wantSkippedUnauth)
			}
			pending, err := fixture.threadInputRepo.ListPendingForTask(context.Background(), fixture.task.ID)
			if err != nil {
				t.Fatalf("list pending feedback: %v", err)
			}
			if got := len(pending); got != tc.wantForwarded {
				t.Fatalf("queued feedback = %d, want %d", got, tc.wantForwarded)
			}

			if !tc.verifySecondPass {
				return
			}
			fixture.counter.Reset()
			fixture.counter.SetEnabled(true)
			second, err := fixture.forwarder.ForwardAuthorizedFeedback(context.Background(), fixture.project.ID, githubPRFeedbackTestRepo())
			fixture.counter.SetEnabled(false)
			if err != nil {
				t.Fatalf("forward feedback second pass: %v", err)
			}
			if got := countGitHubAuthorizationLookups(fixture.counter.Statements()); got != tc.wantAuthorization {
				t.Fatalf("second-pass authorization lookups = %d, want %d", got, tc.wantAuthorization)
			}
			if len(second.Forwarded) != 0 || second.SkippedDuplicate != tc.itemCount {
				t.Fatalf("second forward result = %#v, want %d duplicate skips", second, tc.itemCount)
			}
		})
	}
}

func TestGitHubPRFeedbackForwarderKeepsAuthorizationFilteringAndErrorsFailClosed(t *testing.T) {
	t.Run("self and bot feedback skip authorization", func(t *testing.T) {
		fixture := newGitHubPRFeedbackForwarderFixture(t, nil, []GitHubPullRequestFeedback{
			{Kind: "issue_comment", ID: "self", AuthorLogin: "openvibely", AuthorType: "User", Body: "Self-authored feedback."},
			{Kind: "review_comment", ID: "bot", AuthorLogin: "ci-bot", AuthorType: "Bot", Body: "Bot-authored feedback."},
		})

		fixture.counter.Reset()
		fixture.counter.SetEnabled(true)
		result, err := fixture.forwarder.ForwardAuthorizedFeedback(context.Background(), fixture.project.ID, githubPRFeedbackTestRepo())
		fixture.counter.SetEnabled(false)
		if err != nil {
			t.Fatalf("forward feedback: %v", err)
		}
		if result.SkippedSelfOrBot != 2 || len(result.Forwarded) != 0 {
			t.Fatalf("forward result = %#v", result)
		}
		if got := countGitHubAuthorizationLookups(fixture.counter.Statements()); got != 0 {
			t.Fatalf("self/bot feedback authorization lookups = %d, want 0", got)
		}
	})

	t.Run("authorization lookup error aborts forwarding", func(t *testing.T) {
		fixture := newGitHubPRFeedbackForwarderFixture(t, []string{"alice"}, realisticGitHubPRFeedbackItems(1, []string{"alice"}, "auth-error"))
		authDB := testutil.NewTestDB(t)
		fixture.forwarder.authRepo = repository.NewGitHubAuthRepo(authDB)
		if err := authDB.Close(); err != nil {
			t.Fatalf("close authorization database: %v", err)
		}

		result, err := fixture.forwarder.ForwardAuthorizedFeedback(context.Background(), fixture.project.ID, githubPRFeedbackTestRepo())
		if err == nil || result != nil {
			t.Fatalf("forward result = %#v, error = %v; want authorization failure", result, err)
		}
		pending, listErr := fixture.threadInputRepo.ListPendingForTask(context.Background(), fixture.task.ID)
		if listErr != nil {
			t.Fatalf("list pending feedback: %v", listErr)
		}
		if len(pending) != 0 {
			t.Fatalf("authorization failure queued feedback: %#v", pending)
		}
	})
}

func TestGitHubPRFeedbackForwarderConcurrentPassesKeepCachesIndependentAndQueueOnce(t *testing.T) {
	fixture := newGitHubPRFeedbackForwarderFixture(t, []string{"alice"}, realisticGitHubPRFeedbackItems(1, []string{"Alice"}, "concurrent"))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	fixture.forwarder.github = &synchronizedPRFeedbackProvider{
		entered: entered,
		release: release,
		items:   realisticGitHubPRFeedbackItems(1, []string{"Alice"}, "concurrent"),
	}

	type outcome struct {
		result *GitHubPRFeedbackForwardResult
		err    error
	}
	outcomes := make(chan outcome, 2)
	var wait sync.WaitGroup
	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := fixture.forwarder.ForwardAuthorizedFeedback(context.Background(), fixture.project.ID, githubPRFeedbackTestRepo())
			outcomes <- outcome{result: result, err: err}
		}()
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			close(release)
			wait.Wait()
			t.Fatal("concurrent forwarding passes did not both reach the feedback barrier")
		}
	}
	close(release)
	wait.Wait()
	fixture.counter.SetEnabled(false)
	close(outcomes)

	var forwarded, duplicates int
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatalf("concurrent forward feedback: %v", outcome.err)
		}
		forwarded += len(outcome.result.Forwarded)
		duplicates += outcome.result.SkippedDuplicate
	}
	if got := countGitHubAuthorizationLookups(fixture.counter.Statements()); got != 2 {
		t.Fatalf("concurrent authorization lookups = %d, want one per forwarding pass", got)
	}
	if forwarded != 1 || duplicates != 1 {
		t.Fatalf("concurrent results forwarded=%d duplicates=%d, want one each", forwarded, duplicates)
	}
	pending, err := fixture.threadInputRepo.ListPendingForTask(context.Background(), fixture.task.ID)
	if err != nil {
		t.Fatalf("list pending feedback: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("concurrent forwarding queued %d messages, want exactly one", len(pending))
	}
}

type githubPRFeedbackForwarderAuthorizationMeasurement struct {
	medianWall           time.Duration
	bytesPerOperation    float64
	allocsPerOperation   float64
	totalSQLStatements   int
	authorizationSelects int
}

func TestGitHubPRFeedbackForwarderAuthorizationCachePerformance(t *testing.T) {
	for _, itemCount := range []int{1, 10, 100} {
		t.Run(fmt.Sprintf("feedback_items=%d", itemCount), func(t *testing.T) {
			fixture := newGitHubPRFeedbackForwarderFixture(t, []string{"alice"}, nil)
			var batch int
			fixture.provider.itemsFn = func() []GitHubPullRequestFeedback {
				batch++
				return realisticGitHubPRFeedbackItems(itemCount, []string{"Alice", "alice"}, fmt.Sprintf("measurement-%d", batch))
			}

			baseline := measureGitHubPRFeedbackForwarderAuthorizationCache(t, fixture, false)
			// Keep the warmed database and repositories, but return the durable
			// forwarding state to the same empty starting point for the candidate.
			fixture.resetForwardingState(t)
			candidate := measureGitHubPRFeedbackForwarderAuthorizationCache(t, fixture, true)
			t.Logf("baseline: wall=%s bytes=%.0f B/op allocs=%.0f sql_statements=%d authorization_selects=%d; candidate: wall=%s bytes=%.0f B/op allocs=%.0f sql_statements=%d authorization_selects=%d", baseline.medianWall, baseline.bytesPerOperation, baseline.allocsPerOperation, baseline.totalSQLStatements, baseline.authorizationSelects, candidate.medianWall, candidate.bytesPerOperation, candidate.allocsPerOperation, candidate.totalSQLStatements, candidate.authorizationSelects)

			if baseline.authorizationSelects != itemCount {
				t.Fatalf("baseline authorization selects = %d, want %d", baseline.authorizationSelects, itemCount)
			}
			if candidate.authorizationSelects != 1 {
				t.Fatalf("candidate authorization selects = %d, want 1", candidate.authorizationSelects)
			}
			if candidate.totalSQLStatements != baseline.totalSQLStatements-(itemCount-1) {
				t.Fatalf("candidate SQL statements = %d, want baseline %d minus %d authorization reads", candidate.totalSQLStatements, baseline.totalSQLStatements, itemCount-1)
			}
		})
	}
}

func measureGitHubPRFeedbackForwarderAuthorizationCache(tb testing.TB, fixture githubPRFeedbackForwarderFixture, cached bool) githubPRFeedbackForwarderAuthorizationMeasurement {
	tb.Helper()
	ctx := context.Background()
	forward := func() {
		result, err := fixture.forward(ctx, cached)
		if err != nil {
			tb.Fatalf("forward feedback: %v", err)
		}
		if len(result.Forwarded) == 0 {
			tb.Fatalf("forward feedback did not queue the unique measurement batch: %#v", result)
		}
	}

	// Both modes use this same real, already initialized SQLite fixture. The
	// warm pass is excluded from timing and allocation samples.
	forward()
	const (
		medianSamples = 5
		timingRuns    = 3
	)
	wallSamples := make([]time.Duration, medianSamples)
	byteSamples := make([]float64, medianSamples)
	allocationSamples := make([]float64, medianSamples)
	for sample := range wallSamples {
		var elapsed time.Duration
		for range timingRuns {
			started := time.Now()
			forward()
			elapsed += time.Since(started)
		}
		wallSamples[sample] = elapsed / timingRuns
		byteSamples[sample] = githubPRFeedbackForwarderAllocatedBytes(tb, timingRuns, forward)
		allocationSamples[sample] = testing.AllocsPerRun(timingRuns, forward)
	}

	fixture.counter.Reset()
	fixture.counter.SetEnabled(true)
	forward()
	fixture.counter.SetEnabled(false)
	statements := fixture.counter.Statements()
	sort.Slice(wallSamples, func(i, j int) bool { return wallSamples[i] < wallSamples[j] })
	sort.Float64s(byteSamples)
	sort.Float64s(allocationSamples)
	return githubPRFeedbackForwarderAuthorizationMeasurement{
		medianWall:           wallSamples[len(wallSamples)/2],
		bytesPerOperation:    byteSamples[len(byteSamples)/2],
		allocsPerOperation:   allocationSamples[len(allocationSamples)/2],
		totalSQLStatements:   len(statements),
		authorizationSelects: countGitHubAuthorizationLookups(statements),
	}
}

func githubPRFeedbackForwarderAllocatedBytes(tb testing.TB, runs int, forward func()) float64 {
	tb.Helper()
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		forward()
	}
	runtime.ReadMemStats(&after)
	return float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

func BenchmarkGitHubPRFeedbackForwarderAuthorizationCache(b *testing.B) {
	for _, itemCount := range []int{1, 10, 100} {
		for _, mode := range []struct {
			name   string
			cached bool
		}{
			{name: "baseline", cached: false},
			{name: "candidate", cached: true},
		} {
			mode := mode
			b.Run(fmt.Sprintf("feedback_items=%d/%s", itemCount, mode.name), func(b *testing.B) {
				var batch int
				fixture := newGitHubPRFeedbackForwarderFixture(b, []string{"alice"}, nil)
				fixture.provider.itemsFn = func() []GitHubPullRequestFeedback {
					batch++
					return realisticGitHubPRFeedbackItems(itemCount, []string{"Alice", "alice"}, fmt.Sprintf("benchmark-%d", batch))
				}
				forward := func() {
					result, err := fixture.forward(context.Background(), mode.cached)
					if err != nil {
						b.Fatal(err)
					}
					if len(result.Forwarded) != itemCount {
						b.Fatalf("forwarded = %d, want %d", len(result.Forwarded), itemCount)
					}
				}

				fixture.counter.Reset()
				fixture.counter.SetEnabled(true)
				forward()
				fixture.counter.SetEnabled(false)
				statements := fixture.counter.Statements()
				authorizationSelects := countGitHubAuthorizationLookups(statements)
				wantAuthorizationSelects := itemCount
				if mode.cached {
					wantAuthorizationSelects = 1
				}
				if authorizationSelects != wantAuthorizationSelects {
					b.Fatalf("warm authorization selects = %d, want %d; statements: %q", authorizationSelects, wantAuthorizationSelects, statements)
				}

				durations := make([]time.Duration, 0, b.N)
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					started := time.Now()
					forward()
					durations = append(durations, time.Since(started))
				}
				b.StopTimer()
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				b.ReportMetric(float64(durations[len(durations)/2].Nanoseconds()), "median_wall_ns/op")
				b.ReportMetric(float64(len(statements)), "sqlite_statements/op")
				b.ReportMetric(float64(authorizationSelects), "authorization_selects/op")
			})
		}
	}
}

func newGitHubPRFeedbackForwarderFixture(tb testing.TB, authorizedLogins []string, items []GitHubPullRequestFeedback) githubPRFeedbackForwarderFixture {
	tb.Helper()
	ctx := context.Background()
	db, counter := testutil.NewStatementCountingTestDB(tb)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	feedbackRepo := repository.NewGitHubPRFeedbackRepo(db)
	authRepo := repository.NewGitHubAuthRepo(db)
	threadInputRepo := repository.NewThreadInputRepo(db)
	project := &models.Project{Name: "PR feedback authorization cache", RepoPath: tb.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		tb.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Implement feedback", Category: models.CategoryActive, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		tb.Fatalf("create task: %v", err)
	}
	for _, login := range authorizedLogins {
		if err := authRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: login, Permission: "triage", AddedBy: "test"}); err != nil {
			tb.Fatalf("authorize actor %q: %v", login, err)
		}
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}); err != nil {
		tb.Fatalf("upsert pull request: %v", err)
	}
	provider := &fakePRFeedbackProvider{items: items}
	return githubPRFeedbackForwarderFixture{
		db:              db,
		project:         project,
		task:            task,
		forwarder:       NewGitHubPRFeedbackForwarder(provider, prRepo, feedbackRepo, authRepo, threadInputRepo),
		provider:        provider,
		counter:         counter,
		threadInputRepo: threadInputRepo,
	}
}

func realisticGitHubPRFeedbackItems(count int, authors []string, idPrefix string) []GitHubPullRequestFeedback {
	items := make([]GitHubPullRequestFeedback, count)
	for i := range items {
		items[i] = GitHubPullRequestFeedback{
			Kind:        "review_comment",
			ID:          fmt.Sprintf("%s-%03d", idPrefix, i),
			AuthorLogin: authors[i%len(authors)],
			AuthorType:  "User",
			Body:        fmt.Sprintf("Please make the authorization lookup efficient for feedback item %d.", i+1),
			Path:        "internal/service/github_pr_feedback_forwarder.go",
			Line:        i + 1,
			URL:         fmt.Sprintf("https://github.com/openvibely/openvibely/pull/42#discussion_r%d", i+1),
			CreatedAt:   time.Date(2026, 9, 12, 12, 0, i%60, 0, time.UTC),
		}
	}
	return items
}

func githubPRFeedbackTestRepo() *GitHubRepoRef {
	return &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely"}
}

func countGitHubAuthorizationLookups(statements []string) int {
	const authorizationQuery = "SELECT COUNT(*) FROM GITHUB_AUTHORIZED_ACTORS WHERE LOWER(GITHUB_LOGIN) = ?"
	count := 0
	for _, statement := range statements {
		normalized := strings.Join(strings.Fields(strings.ToUpper(statement)), " ")
		if normalized == authorizationQuery {
			count++
		}
	}
	return count
}
