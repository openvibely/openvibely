package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

type fakeTaskPullRequestGitHubProvider struct {
	globalAPIEndpoint   string
	resolveRepoFn       func(context.Context, string, string) (*GitHubRepoRef, error)
	defaultBranchFn     func(context.Context, *GitHubRepoRef) (string, error)
	publishBranchFn     func(context.Context, *GitHubRepoRef, GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error)
	replaceBranchHeadFn func(context.Context, *GitHubRepoRef, GitHubReplaceBranchHeadRequest) error
	getPullRequestFn    func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error)
	updatePRBodyFn      func(context.Context, *GitHubRepoRef, int, string) error
	findPRFn            func(context.Context, *GitHubRepoRef, string) (*GitHubPullRequest, error)
	createPRFn          func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error)
}

func (f *fakeTaskPullRequestGitHubProvider) ResolveRepo(ctx context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
	if f.resolveRepoFn != nil {
		return f.resolveRepoFn(ctx, repoURL, repoPath)
	}
	return &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
}

func (f *fakeTaskPullRequestGitHubProvider) DefaultBranch(ctx context.Context, repo *GitHubRepoRef) (string, error) {
	if f.defaultBranchFn != nil {
		return f.defaultBranchFn(ctx, repo)
	}
	return "main", nil
}

func (f *fakeTaskPullRequestGitHubProvider) PublishBranch(ctx context.Context, repo *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
	if f.publishBranchFn != nil {
		return f.publishBranchFn(ctx, repo, req)
	}
	return &GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (f *fakeTaskPullRequestGitHubProvider) ReplaceBranchHead(ctx context.Context, repo *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) error {
	if f.replaceBranchHeadFn != nil {
		return f.replaceBranchHeadFn(ctx, repo, req)
	}
	return nil
}

func (f *fakeTaskPullRequestGitHubProvider) GetPullRequest(ctx context.Context, repo *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
	if f.getPullRequestFn != nil {
		return f.getPullRequestFn(ctx, repo, number)
	}
	return &GitHubPullRequest{Number: number, URL: fmt.Sprintf("https://github.com/openvibely/openvibely/pull/%d", number), State: "open", HeadRef: "task/clean-history", HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (f *fakeTaskPullRequestGitHubProvider) UpdatePullRequestBody(ctx context.Context, repo *GitHubRepoRef, number int, body string) error {
	if f.updatePRBodyFn != nil {
		return f.updatePRBodyFn(ctx, repo, number, body)
	}
	return nil
}

func (f *fakeTaskPullRequestGitHubProvider) FindPullRequestByBranch(ctx context.Context, repo *GitHubRepoRef, branch string) (*GitHubPullRequest, error) {
	if f.findPRFn != nil {
		return f.findPRFn(ctx, repo, branch)
	}
	return nil, nil
}

func (f *fakeTaskPullRequestGitHubProvider) CreatePullRequest(ctx context.Context, repo *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
	if f.createPRFn != nil {
		return f.createPRFn(ctx, repo, req)
	}
	return &GitHubPullRequest{Number: 42, URL: "https://github.com/openvibely/openvibely/pull/42", State: "open", HeadRef: req.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
}

func (f *fakeTaskPullRequestGitHubProvider) GlobalAPIEndpoint(_ context.Context) string {
	return f.globalAPIEndpoint
}

func TestTaskPullRequestServiceOpenForTaskUsesCanonicalRepositoryMutationBoundary(t *testing.T) {
	ctx := context.Background()
	repoDir := t.TempDir()
	aliasRoot := t.TempDir()
	alias := filepath.Join(aliasRoot, "repo-alias")
	if err := os.Symlink(repoDir, alias); err != nil {
		t.Skipf("symlink repository alias: %v", err)
	}
	db := testutil.NewTestDB(t)
	prRepo := repository.NewTaskPullRequestRepo(db)
	publishEntered := make(chan struct{}, 1)
	github := &fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(context.Context, *GitHubRepoRef, GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			publishEntered <- struct{}{}
			return nil, errors.New("stop after publication entry")
		},
	}
	service := NewTaskPullRequestService(github, prRepo)
	project := &models.Project{ID: "project", RepoPath: alias, RepoURL: "https://github.com/openvibely/openvibely"}
	task := &models.Task{ID: "task", ProjectID: project.ID, Title: "Publish", WorktreeBranch: "task/publish", MergeTargetBranch: "main"}

	leaseEntered := make(chan struct{})
	releaseLease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- WithRepositoryMutation(repoDir, func() error {
			close(leaseEntered)
			<-releaseLease
			return nil
		})
	}()
	select {
	case <-leaseEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("repository mutation lease was not acquired")
	}

	openDone := make(chan error, 1)
	go func() {
		_, err := service.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{})
		openDone <- err
	}()
	select {
	case <-publishEntered:
		t.Fatal("direct pull-request publication bypassed canonical repository mutation boundary")
	case err := <-openDone:
		t.Fatalf("direct pull-request publication returned before lease release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseLease)
	select {
	case err := <-leaseDone:
		if err != nil {
			t.Fatalf("repository mutation lease: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("repository mutation lease did not finish")
	}
	select {
	case <-publishEntered:
	case err := <-openDone:
		t.Fatalf("publication did not enter after lease release: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("publication did not resume after lease release")
	}
	select {
	case err := <-openDone:
		if err == nil || !strings.Contains(err.Error(), "stop after publication entry") {
			t.Fatalf("unexpected publication result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("publication did not return")
	}
}

func TestTaskPullRequestServiceReplaceBranchHeadForTaskUsesLinkedPRAndTaskBranch(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Replacement Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Clean PR history", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/clean-history"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	stored := &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}
	if err := prRepo.Upsert(ctx, stored); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}

	var gotReq GitHubReplaceBranchHeadRequest
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		replaceBranchHeadFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubReplaceBranchHeadRequest) error {
			gotReq = req
			return nil
		},
	}, prRepo)
	expected := strings.Repeat("a", 40)
	got, err := svc.ReplaceBranchHeadForTask(ctx, project, task, expected)
	if err != nil {
		t.Fatalf("ReplaceBranchHeadForTask: %v", err)
	}
	if got.PRNumber != 4 || gotReq.WorktreePath != task.WorktreePath || gotReq.Branch != task.WorktreeBranch || gotReq.ExpectedHead != expected {
		t.Fatalf("unexpected record/request: record=%#v request=%#v", got, gotReq)
	}
}

func TestTaskPullRequestServiceReplaceBranchHeadForTaskRejectsLinkedPRHeadMismatch(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Replacement Guard Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Replacement guard", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/clean-history"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	replaceCalled := false
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(_ context.Context, _ *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: number, HeadRef: "task/different-branch", HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		replaceBranchHeadFn: func(_ context.Context, _ *GitHubRepoRef, _ GitHubReplaceBranchHeadRequest) error {
			replaceCalled = true
			return nil
		},
	}, prRepo)

	_, err := svc.ReplaceBranchHeadForTask(ctx, project, task, strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "head branch") {
		t.Fatalf("expected linked PR head mismatch error, got %v", err)
	}
	if replaceCalled {
		t.Fatal("branch replacement must not run when linked PR head differs")
	}
}

func TestTaskPullRequestServiceReplaceBranchHeadForTaskRejectsLinkedPRHeadRepositoryMismatch(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Fork Guard Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Fork guard", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/clean-history"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	replaceCalled := false
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(_ context.Context, _ *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: number, HeadRef: task.WorktreeBranch, HeadRepoFullName: "contributor/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		replaceBranchHeadFn: func(_ context.Context, _ *GitHubRepoRef, _ GitHubReplaceBranchHeadRequest) error {
			replaceCalled = true
			return nil
		},
	}, prRepo)

	_, err := svc.ReplaceBranchHeadForTask(ctx, project, task, strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "head repository") {
		t.Fatalf("expected linked PR head repository mismatch error, got %v", err)
	}
	if replaceCalled {
		t.Fatal("branch replacement must not run when linked PR head repository differs")
	}
}

func TestTaskPullRequestServiceReplaceBranchHeadForTaskFailsClosedWhenLinkedPRCannotBeFetched(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Replacement Guard Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Replacement guard", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/clean-history"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 4, PRURL: "https://github.com/openvibely/openvibely/pull/4", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			return nil, fmt.Errorf("github unavailable")
		},
		replaceBranchHeadFn: func(context.Context, *GitHubRepoRef, GitHubReplaceBranchHeadRequest) error {
			t.Fatal("branch replacement must not run when linked PR cannot be fetched")
			return nil
		},
	}, prRepo)

	_, err := svc.ReplaceBranchHeadForTask(ctx, project, task, strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "fetching linked pull request") {
		t.Fatalf("expected linked PR fetch error, got %v", err)
	}
}

func TestTaskPullRequestServiceReplaceBranchHeadForTaskRequiresLinkedPR(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	task := &models.Task{ID: "task-id", WorktreePath: t.TempDir(), WorktreeBranch: "task/clean-history"}
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{}, prRepo)

	_, err := svc.ReplaceBranchHeadForTask(ctx, project, task, strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "no linked pull request") {
		t.Fatalf("expected linked PR error, got %v", err)
	}
}

func TestTaskPullRequestServiceBuildCreatePullRequestRequestUsesGenericFallbackWithoutInternalMetadata(t *testing.T) {
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{}, nil)
	issueNumber := 262

	request := svc.buildCreatePullRequestRequest(context.Background(), &models.Project{RepoPath: t.TempDir()}, &models.Task{
		ID:                "4465e3079fae6b5c4a3ce4ba81d623ee",
		Title:             "Narrow template dashboard/list SQL projection (#262)",
		WorktreeBranch:    "task/narrow-template-sql",
		MergeTargetBranch: "main",
	}, OpenTaskPullRequestOptions{IssueNumber: &issueNumber}, &GitHubRepoRef{})

	want := "## Summary\n- Narrow template dashboard/list SQL projection\n\nCloses #262"
	if request.Body != want {
		t.Fatalf("default PR body = %q, want %q", request.Body, want)
	}
	for _, forbidden := range []string{"Task ID:", "Task title:", "OpenVibely", "Automated pull request"} {
		if strings.Contains(request.Body, forbidden) {
			t.Fatalf("default PR body contains %q: %q", forbidden, request.Body)
		}
	}
}

func TestTaskPullRequestServiceOpenForTaskCreatesAndPersistsPR(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	var publishedReq GitHubPublishBranchRequest
	var createdReq GitHubCreatePullRequestRequest
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			publishedReq = req
			return &GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createdReq = req
			return &GitHubPullRequest{Number: 77, URL: "https://github.com/openvibely/openvibely/pull/77", State: "open", HeadRef: req.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	}, prRepo)
	project := &models.Project{Name: "PR Service Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Implement runtime PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/runtime-pr", MergeTargetBranch: "develop"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	issueNumber := 99

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{
		Title:       "Custom PR",
		Body:        "Custom body",
		Draft:       true,
		IssueNumber: &issueNumber,
		IssueURL:    "https://github.com/openvibely/openvibely/issues/99",
	})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if !result.Created || result.PullRequest.Number != 77 || publishedReq.Branch != task.WorktreeBranch || publishedReq.BaseBranch != "develop" {
		t.Fatalf("unexpected result=%#v publishedReq=%#v", result, publishedReq)
	}
	if createdReq.Title != "Custom PR" || createdReq.Body != "Custom body" || createdReq.Head != task.WorktreeBranch || createdReq.Base != "develop" || !createdReq.Draft {
		t.Fatalf("unexpected create request: %#v", createdReq)
	}
	record, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if record == nil || record.PRNumber != 77 || record.PublishedHeadSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || record.IssueNumber == nil || *record.IssueNumber != 99 || record.IssueURL == "" {
		t.Fatalf("unexpected persisted PR record: %#v", record)
	}
}

func TestTaskPullRequestServiceOpenForTaskRecordsPublishedCommitStat(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	statRepo := repository.NewTaskCommitStatRepo(db)

	repoDir := createTestGitRepo(t)
	if out, err := gitOutput(repoDir, "checkout", "-b", "task/publish-stat"); err != nil {
		t.Fatalf("checkout task branch: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n\nPublished update\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "published.go"), []byte("package published\n\nfunc Added() {}\n"), 0644); err != nil {
		t.Fatalf("write published.go: %v", err)
	}
	project := &models.Project{Name: "Published Commit Stats", RepoPath: repoDir, RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Publish stat task", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: repoDir, WorktreeBranch: "task/publish-stat", MergeTargetBranch: "main"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	publishedSHA := strings.Repeat("c", 40)
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			if req.BaseBranch != "main" || req.Branch != task.WorktreeBranch || req.WorktreePath != repoDir {
				t.Fatalf("unexpected publish request: %#v", req)
			}
			return &GitHubPublishBranchResult{HeadSHA: publishedSHA, CreatedCommit: true}, nil
		},
		createPRFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 87, URL: "https://github.com/openvibely/openvibely/pull/87", State: "open", HeadRef: req.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: publishedSHA}, nil
		},
	}, prRepo).SetTaskCommitStatRepo(statRepo)

	_, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{CommitMessage: "Publish task stats"})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	stats, err := statRepo.ListProducedCommitStats(ctx, project.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("list stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	stat := stats[0]
	if stat.CommitSHA != publishedSHA || stat.ShortSHA != publishedSHA[:7] || stat.Subject != "Publish task stats" {
		t.Fatalf("unexpected published stat metadata: %#v", stat)
	}
	if stat.Insertions != 5 || stat.FilesChanged != 2 || stat.ChangedFilesJSON != `["README.md","published.go"]` {
		t.Fatalf("unexpected published stat totals: insertions=%d files=%d changed=%s", stat.Insertions, stat.FilesChanged, stat.ChangedFilesJSON)
	}
}

func TestTaskPullRequestServiceOpenForTaskRecordsPublishedUpdateCommitStat(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	statRepo := repository.NewTaskCommitStatRepo(db)

	repoDir := createTestGitRepo(t)
	if out, err := gitOutput(repoDir, "checkout", "-b", "task/publish-update-stat"); err != nil {
		t.Fatalf("checkout task branch: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("# Test\n\nPreviously published branch change\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "update.go"), []byte("package update\n\nfunc Later() {}\n"), 0644); err != nil {
		t.Fatalf("write update.go: %v", err)
	}
	project := &models.Project{Name: "Published Update Stats", RepoPath: repoDir, RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Publish update stat task", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: repoDir, WorktreeBranch: "task/publish-update-stat", MergeTargetBranch: "main"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	previousSHA := strings.Repeat("b", 40)
	publishedSHA := strings.Repeat("d", 40)
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 88, PRURL: "https://github.com/openvibely/openvibely/pull/88", PRState: "open", PublishedHeadSHA: previousSHA}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			if req.BaseBranch != "main" || req.Branch != task.WorktreeBranch || req.WorktreePath != repoDir {
				t.Fatalf("unexpected publish request: %#v", req)
			}
			return &GitHubPublishBranchResult{
				HeadSHA:       publishedSHA,
				CreatedCommit: true,
				ParentSHA:     previousSHA,
				CommitStats:   &GitHubPublishedCommitStats{Insertions: 3, FilesChanged: 1, ChangedFiles: []string{"update.go"}},
			}, nil
		},
		getPullRequestFn: func(_ context.Context, _ *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: number, URL: "https://github.com/openvibely/openvibely/pull/88", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: publishedSHA}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			t.Fatal("existing open PR should be reused")
			return nil, nil
		},
	}, prRepo).SetTaskCommitStatRepo(statRepo)

	_, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{CommitMessage: "Publish update stats"})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	stats, err := statRepo.ListProducedCommitStats(ctx, project.ID, time.Now().UTC().Add(-time.Minute))
	if err != nil {
		t.Fatalf("list stats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("stats count = %d, want 1", len(stats))
	}
	stat := stats[0]
	if stat.CommitSHA != publishedSHA || stat.ShortSHA != publishedSHA[:7] || stat.Subject != "Publish update stats" {
		t.Fatalf("unexpected published update stat metadata: %#v", stat)
	}
	if stat.Insertions != 3 || stat.Deletions != 0 || stat.FilesChanged != 1 || stat.ChangedFilesJSON != `["update.go"]` {
		t.Fatalf("unexpected published update stat totals: insertions=%d deletions=%d files=%d changed=%s", stat.Insertions, stat.Deletions, stat.FilesChanged, stat.ChangedFilesJSON)
	}
}

func TestTaskPullRequestServiceOpenForTaskReusesExistingRecord(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Existing PR Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Existing PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/existing"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 22, PRURL: "https://github.com/openvibely/openvibely/pull/22", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	createCalls := 0
	getCalls := 0
	publishCalls := 0
	updateBodyCalls := 0
	updatedBody := ""
	var publishedReq GitHubPublishBranchRequest
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			publishCalls++
			publishedReq = req
			return &GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			getCalls++
			return &GitHubPullRequest{Number: 22, URL: "https://github.com/openvibely/openvibely/pull/22", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		updatePRBodyFn: func(_ context.Context, _ *GitHubRepoRef, number int, body string) error {
			if number != 22 {
				t.Fatalf("unexpected pull request number for body update: %d", number)
			}
			updateBodyCalls++
			updatedBody = body
			return nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createCalls++
			return nil, fmt.Errorf("should not create")
		},
	}, prRepo)

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{Body: "Refreshed body"})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if !result.ReusedExistingRecord || result.PullRequest.Number != 22 || createCalls != 0 || getCalls != 1 || publishCalls != 1 || updateBodyCalls != 1 || updatedBody != "Refreshed body" || publishedReq.Branch != task.WorktreeBranch {
		t.Fatalf("expected existing PR reuse with branch publish, body refresh, and live verification, result=%#v createCalls=%d getCalls=%d publishCalls=%d updateBodyCalls=%d updatedBody=%q publishedReq=%#v", result, createCalls, getCalls, publishCalls, updateBodyCalls, updatedBody, publishedReq)
	}
	record, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if record == nil || record.PublishedHeadSHA != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("expected existing PR record to store current publication head, got %#v", record)
	}
}

func TestTaskPullRequestServiceOpenForTaskRejectsOldOpenPullRequestHead(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Old Head PR Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Old Head PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/old-head"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 22, PRURL: "https://github.com/openvibely/openvibely/pull/22", PRState: "open", PublishedHeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	createCalls := 0
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			if req.Branch != task.WorktreeBranch {
				t.Fatalf("unexpected publish request: %#v", req)
			}
			return &GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 22, URL: "https://github.com/openvibely/openvibely/pull/22", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, nil
		},
		findPRFn: func(_ context.Context, _ *GitHubRepoRef, branch string) (*GitHubPullRequest, error) {
			if branch != task.WorktreeBranch {
				t.Fatalf("unexpected branch lookup: %s", branch)
			}
			return &GitHubPullRequest{Number: 22, URL: "https://github.com/openvibely/openvibely/pull/22", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createCalls++
			return nil, fmt.Errorf("should not create while GitHub reports an open branch PR")
		},
	}, prRepo)

	_, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not match published branch head") {
		t.Fatalf("expected stale head publication error, got %v", err)
	}
	if createCalls != 0 {
		t.Fatalf("did not expect create call, got %d", createCalls)
	}
	record, getErr := prRepo.GetByTaskID(ctx, task.ID)
	if getErr != nil {
		t.Fatalf("GetByTaskID: %v", getErr)
	}
	if record == nil || record.PublishedHeadSHA != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("stale publication row should not be updated on failed verification: %#v", record)
	}
}

func TestTaskPullRequestServiceOpenForTaskReplacesStaleOpenRecordWithOpenPR(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Stale Open PR Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Stale Open PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/stale-open"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 22, PRURL: "https://github.com/openvibely/openvibely/pull/22", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	getCalls := 0
	findCalls := 0
	createCalls := 0
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			getCalls++
			return &GitHubPullRequest{Number: 22, URL: "https://github.com/openvibely/openvibely/pull/22", State: "closed", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, _ *GitHubRepoRef, branch string) (*GitHubPullRequest, error) {
			findCalls++
			if branch != task.WorktreeBranch {
				t.Fatalf("unexpected branch lookup: %s", branch)
			}
			return nil, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createCalls++
			return &GitHubPullRequest{Number: 33, URL: "https://github.com/openvibely/openvibely/pull/33", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	}, prRepo)

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if result.ReusedExistingRecord || !result.Created || result.PullRequest.Number != 33 || getCalls != 1 || findCalls != 1 || createCalls != 1 {
		t.Fatalf("expected stale open record replacement, result=%#v getCalls=%d findCalls=%d createCalls=%d", result, getCalls, findCalls, createCalls)
	}
	record, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if record == nil || record.PRNumber != 33 || record.PRState != "open" {
		t.Fatalf("expected open replacement PR record, got %#v", record)
	}
}

func TestTaskPullRequestServiceOpenForTaskReplacesClosedRecordWithOpenPR(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Closed PR Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Closed PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/closed"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 22, PRURL: "https://github.com/openvibely/openvibely/pull/22", PRState: "closed"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	publishCalls := 0
	findCalls := 0
	createCalls := 0
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		publishBranchFn: func(_ context.Context, _ *GitHubRepoRef, req GitHubPublishBranchRequest) (*GitHubPublishBranchResult, error) {
			publishCalls++
			if req.Branch != task.WorktreeBranch {
				t.Fatalf("unexpected publish request: %#v", req)
			}
			return &GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, _ *GitHubRepoRef, branch string) (*GitHubPullRequest, error) {
			findCalls++
			if branch != task.WorktreeBranch {
				t.Fatalf("unexpected branch lookup: %s", branch)
			}
			return &GitHubPullRequest{Number: 22, URL: "https://github.com/openvibely/openvibely/pull/22", State: "closed"}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createCalls++
			return &GitHubPullRequest{Number: 33, URL: "https://github.com/openvibely/openvibely/pull/33", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	}, prRepo)

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if result.ReusedExistingRecord || !result.Created || result.PullRequest.Number != 33 || publishCalls != 1 || findCalls != 1 || createCalls != 1 {
		t.Fatalf("expected closed record replacement, result=%#v publishCalls=%d findCalls=%d createCalls=%d", result, publishCalls, findCalls, createCalls)
	}
	record, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if record == nil || record.PRNumber != 33 || record.PRState != "open" {
		t.Fatalf("expected open replacement PR record, got %#v", record)
	}
}

func TestTaskPullRequestServiceOpenForTaskReusesExistingRecordAndPersistsIssueMetadata(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Existing PR Metadata Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Existing PR Metadata", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/existing-metadata"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 23, PRURL: "https://github.com/openvibely/openvibely/pull/23", PRState: "open"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	createCalls := 0
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 23, URL: "https://github.com/openvibely/openvibely/pull/23", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			createCalls++
			return nil, fmt.Errorf("should not create")
		},
	}, prRepo)
	issueNumber := 123

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{
		IssueNumber: &issueNumber,
		IssueURL:    "https://github.com/openvibely/openvibely/issues/123",
	})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if !result.ReusedExistingRecord || result.PullRequest.Number != 23 || createCalls != 0 {
		t.Fatalf("expected existing PR reuse, result=%#v createCalls=%d", result, createCalls)
	}
	if result.Record.IssueNumber == nil || *result.Record.IssueNumber != 123 || result.Record.IssueURL == "" {
		t.Fatalf("expected result record issue metadata, got %#v", result.Record)
	}
	persisted, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if persisted == nil || persisted.IssueNumber == nil || *persisted.IssueNumber != 123 || persisted.IssueURL != "https://github.com/openvibely/openvibely/issues/123" {
		t.Fatalf("expected persisted issue metadata, got %#v", persisted)
	}
}

func TestTaskPullRequestServiceOpenForTaskClearsStaleIssueURLWhenIssueNumberChanges(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Existing PR Stale Issue URL Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Existing PR Stale Issue URL", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/existing-stale-issue-url"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	oldIssueNumber := 123
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 24, PRURL: "https://github.com/openvibely/openvibely/pull/24", PRState: "open", IssueNumber: &oldIssueNumber, IssueURL: "https://github.com/openvibely/openvibely/issues/123"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 24, URL: "https://github.com/openvibely/openvibely/pull/24", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return nil, fmt.Errorf("should not create")
		},
	}, prRepo)
	newIssueNumber := 456

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{IssueNumber: &newIssueNumber})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if !result.ReusedExistingRecord || result.PullRequest.Number != 24 {
		t.Fatalf("expected existing PR reuse, got %#v", result)
	}
	if result.Record.IssueNumber == nil || *result.Record.IssueNumber != 456 || result.Record.IssueURL != "" {
		t.Fatalf("expected result record with new issue number and cleared URL, got %#v", result.Record)
	}
	persisted, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if persisted == nil || persisted.IssueNumber == nil || *persisted.IssueNumber != 456 || persisted.IssueURL != "" {
		t.Fatalf("expected persisted issue number with cleared stale URL, got %#v", persisted)
	}
}

func TestTaskPullRequestServiceOpenForTaskClearsStaleIssueNumberWhenIssueURLChanges(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Existing PR Stale Issue Number Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Existing PR Stale Issue Number", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/existing-stale-issue-number"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	oldIssueNumber := 123
	if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 25, PRURL: "https://github.com/openvibely/openvibely/pull/25", PRState: "open", IssueNumber: &oldIssueNumber, IssueURL: "https://github.com/openvibely/openvibely/issues/123"}); err != nil {
		t.Fatalf("seed PR record: %v", err)
	}
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		getPullRequestFn: func(context.Context, *GitHubRepoRef, int) (*GitHubPullRequest, error) {
			return &GitHubPullRequest{Number: 25, URL: "https://github.com/openvibely/openvibely/pull/25", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return nil, fmt.Errorf("should not create")
		},
	}, prRepo)

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{IssueURL: "https://github.com/openvibely/openvibely/issues/456"})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if !result.ReusedExistingRecord || result.PullRequest.Number != 25 {
		t.Fatalf("expected existing PR reuse, got %#v", result)
	}
	if result.Record.IssueNumber != nil || result.Record.IssueURL != "https://github.com/openvibely/openvibely/issues/456" {
		t.Fatalf("expected result record with new issue URL and cleared number, got %#v", result.Record)
	}
	persisted, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if persisted == nil || persisted.IssueNumber != nil || persisted.IssueURL != "https://github.com/openvibely/openvibely/issues/456" {
		t.Fatalf("expected persisted issue URL with cleared stale number, got %#v", persisted)
	}
}

func TestTaskPullRequestServiceOpenForTaskRecoversAlreadyExistsByFindingPR(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	prRepo := repository.NewTaskPullRequestRepo(db)
	project := &models.Project{Name: "Recovered PR Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Recovered PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/reused"}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	findCalls := 0
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
		findPRFn: func(_ context.Context, _ *GitHubRepoRef, _ string) (*GitHubPullRequest, error) {
			findCalls++
			if findCalls == 1 {
				return nil, nil
			}
			return &GitHubPullRequest{Number: 88, URL: "https://github.com/openvibely/openvibely/pull/88", State: "open", HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(context.Context, *GitHubRepoRef, GitHubCreatePullRequestRequest) (*GitHubPullRequest, error) {
			return nil, fmt.Errorf("Validation Failed: pull request already exists for openvibely:task/reused")
		},
	}, prRepo)

	result, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{})
	if err != nil {
		t.Fatalf("OpenForTask: %v", err)
	}
	if result.Created || !result.ReusedRemote || result.PullRequest.Number != 88 || findCalls != 2 {
		t.Fatalf("expected remote PR recovery, result=%#v findCalls=%d", result, findCalls)
	}
	record, err := prRepo.GetByTaskID(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetByTaskID: %v", err)
	}
	if record == nil || record.PRNumber != 88 {
		t.Fatalf("expected recovered PR record, got %#v", record)
	}
}

func TestTaskPullRequestServiceAutomationOperationsUseProjectURLOrLocalGitRemote(t *testing.T) {
	ctx := context.Background()
	t.Run("open", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		projectRepo := repository.NewProjectRepo(db)
		taskRepo := repository.NewTaskRepo(db, nil)
		prRepo := repository.NewTaskPullRequestRepo(db)
		project := &models.Project{Name: "Automation PR Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
		if err := projectRepo.Create(ctx, project); err != nil {
			t.Fatalf("create project: %v", err)
		}
		task := &models.Task{ProjectID: project.ID, Title: "Automation PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/automation-pr"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create Automation task: %v", err)
		}
		var resolvedURL, resolvedPath string
		resolveCalls := 0
		svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
			resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
				resolveCalls++
				resolvedURL, resolvedPath = repoURL, repoPath
				return &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
			},
		}, prRepo)

		if _, err := svc.OpenForAutomationTask(ctx, project, task, OpenTaskPullRequestOptions{}); err != nil {
			t.Fatalf("OpenForAutomationTask: %v", err)
		}
		if resolvedURL != project.RepoURL || resolvedPath != "" {
			t.Fatalf("Automation repository resolution must use only the explicit URL, got url=%q path=%q", resolvedURL, resolvedPath)
		}

		project.RepoURL = ""
		localRemoteTask := &models.Task{ProjectID: project.ID, Title: "Local remote", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/local-remote"}
		if err := taskRepo.Create(ctx, localRemoteTask); err != nil {
			t.Fatalf("create local-remote Automation task: %v", err)
		}
		if _, err := svc.OpenForAutomationTask(ctx, project, localRemoteTask, OpenTaskPullRequestOptions{}); err != nil {
			t.Fatalf("Automation PR open must use the project local Git remote when repo_url is absent: %v", err)
		}
		if resolvedURL != "" || resolvedPath != project.RepoPath {
			t.Fatalf("Automation repository resolution must fall back to repo_path, got url=%q path=%q", resolvedURL, resolvedPath)
		}

		ordinaryTask := &models.Task{ProjectID: project.ID, Title: "Ordinary PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/ordinary-pr"}
		if err := taskRepo.Create(ctx, ordinaryTask); err != nil {
			t.Fatalf("create ordinary task: %v", err)
		}
		if _, err := svc.OpenForTask(ctx, project, ordinaryTask, OpenTaskPullRequestOptions{}); err != nil {
			t.Fatalf("ordinary OpenForTask must retain repository-path fallback: %v", err)
		}
		if resolvedURL != "" || resolvedPath != project.RepoPath {
			t.Fatalf("ordinary repository resolution must retain repo_path fallback, got url=%q path=%q", resolvedURL, resolvedPath)
		}
	})

	t.Run("replace branch", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		projectRepo := repository.NewProjectRepo(db)
		taskRepo := repository.NewTaskRepo(db, nil)
		prRepo := repository.NewTaskPullRequestRepo(db)
		project := &models.Project{Name: "Automation Replace Project", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
		if err := projectRepo.Create(ctx, project); err != nil {
			t.Fatalf("create project: %v", err)
		}
		task := &models.Task{ProjectID: project.ID, Title: "Replace Automation PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: t.TempDir(), WorktreeBranch: "task/replace"}
		if err := taskRepo.Create(ctx, task); err != nil {
			t.Fatalf("create task: %v", err)
		}
		if err := prRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/openvibely/openvibely/pull/42", PRState: "open"}); err != nil {
			t.Fatalf("seed PR record: %v", err)
		}
		var resolvedURL, resolvedPath string
		resolveCalls := 0
		svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{
			resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*GitHubRepoRef, error) {
				resolveCalls++
				resolvedURL, resolvedPath = repoURL, repoPath
				return &GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
			},
			getPullRequestFn: func(_ context.Context, _ *GitHubRepoRef, number int) (*GitHubPullRequest, error) {
				return &GitHubPullRequest{Number: number, HeadRef: task.WorktreeBranch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
			},
		}, prRepo)

		if _, err := svc.ReplaceBranchHeadForAutomationTask(ctx, project, task, strings.Repeat("a", 40)); err != nil {
			t.Fatalf("ReplaceBranchHeadForAutomationTask: %v", err)
		}
		if resolvedURL != project.RepoURL || resolvedPath != "" {
			t.Fatalf("Automation branch replacement must resolve only the explicit URL, got url=%q path=%q", resolvedURL, resolvedPath)
		}

		project.RepoURL = ""
		if _, err := svc.ReplaceBranchHeadForAutomationTask(ctx, project, task, strings.Repeat("a", 40)); err != nil {
			t.Fatalf("Automation branch replacement must use the project local Git remote when repo_url is absent: %v", err)
		}
		if resolvedURL != "" || resolvedPath != project.RepoPath {
			t.Fatalf("Automation branch replacement must fall back to repo_path, got url=%q path=%q", resolvedURL, resolvedPath)
		}

		if _, err := svc.ReplaceBranchHeadForTask(ctx, project, task, strings.Repeat("a", 40)); err != nil {
			t.Fatalf("ordinary ReplaceBranchHeadForTask must retain repository-path fallback: %v", err)
		}
		if resolvedURL != "" || resolvedPath != project.RepoPath {
			t.Fatalf("ordinary replacement resolution must retain repo_path fallback, got url=%q path=%q", resolvedURL, resolvedPath)
		}
	})
}

func TestTaskPullRequestServiceOpenForTaskRequiresWorktreeBranch(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	svc := NewTaskPullRequestService(&fakeTaskPullRequestGitHubProvider{}, repository.NewTaskPullRequestRepo(db))
	project := &models.Project{ID: "project-1", RepoPath: t.TempDir(), RepoURL: "https://github.com/openvibely/openvibely"}
	task := &models.Task{ID: "task-1", ProjectID: project.ID, Title: "No Branch"}

	_, err := svc.OpenForTask(ctx, project, task, OpenTaskPullRequestOptions{})
	if err == nil || !strings.Contains(err.Error(), "no worktree branch") {
		t.Fatalf("expected missing branch error, got %v", err)
	}
}
