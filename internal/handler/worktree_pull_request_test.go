package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

func TestCreateTaskPullRequest_RequiresWorktreeBranch(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	project := &models.Project{Name: "PR Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "No Branch", Category: models.CategoryActive, Status: models.StatusPending}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", rec.Code)
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger on error, got %s", trigger)
	}
	if !strings.Contains(trigger, "worktree branch") {
		t.Fatalf("expected toast message about worktree branch, got %s", trigger)
	}
}

func TestCreateTaskPullRequest_TaskCardOwnershipFailuresAreIndistinguishable(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	project := &models.Project{Name: "PR Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Scoped PR", Category: models.CategoryCompleted, Status: models.StatusCompleted, WorktreeBranch: "task/scoped-pr"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	request := func(taskID string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"merge_source": {"task_card"}, "project_id": {"foreign"}}
		req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID+"/worktree/pull-request", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}

	missing := request("missing-task")
	foreign := request(task.ID)
	for name, rec := range map[string]*httptest.ResponseRecorder{"missing": missing, "foreign": foreign} {
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s task-card PR should return 404, got %d: %s", name, rec.Code, rec.Body.String())
		}
		trigger := rec.Header().Get("HX-Trigger")
		if !strings.Contains(trigger, "openvibelyToast") || !strings.Contains(strings.ToLower(trigger), "task not found") {
			t.Fatalf("%s task-card PR should emit the same failure toast, got %s", name, trigger)
		}
		if strings.Contains(rec.Body.String(), "kanban-board") || strings.Contains(rec.Body.String(), "changes-actions-dropdown") {
			t.Fatalf("%s task-card PR should not return a replacement fragment: %s", name, rec.Body.String())
		}
	}
	if missing.Body.String() != foreign.Body.String() || missing.Header().Get("HX-Trigger") != foreign.Header().Get("HX-Trigger") {
		t.Fatalf("missing and foreign task-card ownership responses must be indistinguishable: missing=%d %q %q foreign=%d %q %q",
			missing.Code, missing.Body.String(), missing.Header().Get("HX-Trigger"), foreign.Code, foreign.Body.String(), foreign.Header().Get("HX-Trigger"))
	}
}

func TestCreateTaskPullRequest_TaskCardRevalidatesProjectContextInsideRepositoryLease(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(context.Context, *testing.T, *models.Project, *models.Project, *models.Task, *sql.DB)
	}{
		{
			name: "task moved to another project",
			mutate: func(ctx context.Context, t *testing.T, _ *models.Project, foreign *models.Project, task *models.Task, db *sql.DB) {
				if _, err := db.ExecContext(ctx, "UPDATE tasks SET project_id = ? WHERE id = ?", foreign.ID, task.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "project repository changed",
			mutate: func(ctx context.Context, t *testing.T, project, _ *models.Project, _ *models.Task, db *sql.DB) {
				if _, err := db.ExecContext(ctx, "UPDATE projects SET repo_path = ? WHERE id = ?", t.TempDir(), project.ID); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			h, e, _, db := setupTestHandlerWithDB(t)
			h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
			repositoryCalls := 0
			h.SetGitHubService(&fakeGitHubService{
				resolveRepoFn: func(context.Context, string, string) (*service.GitHubRepoRef, error) {
					repositoryCalls++
					return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely"}, nil
				},
			})

			repoDir := t.TempDir()
			project := &models.Project{Name: "PR lease project", RepoPath: repoDir, RepoURL: "https://github.com/openvibely/openvibely"}
			if err := h.projectSvc.Create(ctx, project); err != nil {
				t.Fatal(err)
			}
			foreign := &models.Project{Name: "Foreign PR lease project", RepoPath: t.TempDir(), RepoURL: "https://github.com/foreign/repository"}
			if err := h.projectSvc.Create(ctx, foreign); err != nil {
				t.Fatal(err)
			}
			task := &models.Task{ProjectID: project.ID, Title: "Lease PR", Category: models.CategoryCompleted, Status: models.StatusCompleted, WorktreeBranch: "task/lease-pr", MergeTargetBranch: "main"}
			if err := h.taskRepo.Create(ctx, task); err != nil {
				t.Fatal(err)
			}

			leaseEntered := make(chan struct{})
			releaseLease := make(chan struct{})
			leaseDone := make(chan error, 1)
			go func() {
				leaseDone <- service.WithRepositoryMutation(repoDir, func() error {
					close(leaseEntered)
					<-releaseLease
					return nil
				})
			}()
			<-leaseEntered

			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				form := url.Values{"merge_source": {"task_card"}, "project_id": {project.ID}}
				req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", strings.NewReader(form.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				result <- rec
			}()
			select {
			case rec := <-result:
				close(releaseLease)
				t.Fatalf("card Create PR bypassed repository lease: %d %s", rec.Code, rec.Body.String())
			case <-time.After(100 * time.Millisecond):
			}

			tt.mutate(ctx, t, project, foreign, task, db)
			close(releaseLease)
			if err := <-leaseDone; err != nil {
				t.Fatal(err)
			}
			select {
			case rec := <-result:
				if rec.Code != http.StatusConflict {
					t.Fatalf("stale card Create PR returned %d, want 409: %s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(strings.ToLower(rec.Body.String()), "project") && !strings.Contains(strings.ToLower(rec.Body.String()), "eligibility") {
					t.Fatalf("stale card Create PR did not report changed project context: %s", rec.Body.String())
				}
			case <-time.After(2 * time.Second):
				t.Fatal("card Create PR did not resume after repository lease release")
			}
			if repositoryCalls != 0 {
				t.Fatalf("stale card Create PR reached GitHub repository resolution %d times", repositoryCalls)
			}
		})
	}
}

func TestCreateTaskPullRequest_UsesGlobalEnterpriseEndpointAndIgnoresRequestOverride(t *testing.T) {
	const enterpriseEndpoint = "https://github.example.com/api/v3"
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	// Seed the global GitHub API endpoint setting.
	settingsRepo := repository.NewSettingsRepo(db)
	if err := settingsRepo.Set(context.Background(), service.GitHubSettingAPIEndpoint, enterpriseEndpoint); err != nil {
		t.Fatal(err)
	}

	var publishedEndpoint string
	h.SetGitHubService(&fakeGitHubService{
		globalAPIEndpoint: enterpriseEndpoint,
		resolveRepoFn: func(_ context.Context, _, _ string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "acme", Name: "widgets", FullName: "acme/widgets", CloneURL: "https://github.example.com/acme/widgets.git", HTMLURL: "https://github.example.com/acme/widgets"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, _ service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			publishedEndpoint = repo.APIBaseURL
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(context.Context, *service.GitHubRepoRef, string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		createPRFn: func(_ context.Context, _ *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: 81, URL: "https://github.example.com/acme/widgets/pull/81", State: "open", HeadRef: createReq.Head, HeadRepoFullName: "acme/widgets", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		}})
	project := &models.Project{Name: "Enterprise PR", RepoPath: "/tmp/repo"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Enterprise PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/enterprise", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatal(err)
	}

	// A request-time override must be ignored; the global setting must win.
	form := url.Values{"github_api_endpoint": {"https://evil.example/api/v3"}}
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s trigger=%s", rec.Code, rec.Body.String(), rec.Header().Get("HX-Trigger"))
	}
	if publishedEndpoint != enterpriseEndpoint {
		t.Fatalf("published endpoint = %q, want global endpoint %q", publishedEndpoint, enterpriseEndpoint)
	}
	// Global setting must remain unchanged after PR creation.
	storedEndpoint, err := settingsRepo.Get(context.Background(), service.GitHubSettingAPIEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if storedEndpoint != enterpriseEndpoint {
		t.Fatalf("global endpoint after PR creation = %q, want %q", storedEndpoint, enterpriseEndpoint)
	}
}

func TestCreateTaskPullRequest_CreatesAndPersistsPR(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", CloneURL: "https://github.com/openvibely/openvibely.git", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: 77, URL: "https://github.com/openvibely/openvibely/pull/77", State: "open", HeadRef: createReq.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	})

	project := &models.Project{Name: "PR Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Create PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/abc-create-pr", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	form := url.Values{"merge_source": {"task_card"}, "project_id": {project.ID}}
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="kanban-board"`) {
		t.Fatalf("expected authoritative board status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger, got %s", trigger)
	}
	if !strings.Contains(trigger, "GitHub PR created (#77)") {
		t.Fatalf("expected success toast trigger, got %s", trigger)
	}

	record, err := h.taskPullRequestRepo.GetByTaskID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("load task pull request: %v", err)
	}
	if record == nil {
		t.Fatal("expected task pull request record")
	}
	if record.PRNumber != 77 {
		t.Fatalf("expected PR number 77, got %d", record.PRNumber)
	}
}

func TestCreateTaskPullRequest_PublishesDirtyWorktreeWithDiffSummaryMessage(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
	var publishedReq service.GitHubPublishBranchRequest
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", CloneURL: "https://github.com/openvibely/openvibely.git", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			publishedReq = publishReq
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: 78, URL: "https://github.com/openvibely/openvibely/pull/78", State: "open", HeadRef: createReq.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	})

	repoDir := t.TempDir()
	runGit(t, repoDir, "init", "-b", "main")
	runGit(t, repoDir, "config", "user.email", "test@example.com")
	runGit(t, repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, repoDir, "add", "README.md")
	runGit(t, repoDir, "commit", "-m", "base")
	runGit(t, repoDir, "checkout", "-b", "task/pr-prep")
	if err := os.WriteFile(filepath.Join(repoDir, "analytics.templ"), []byte("model usage breakdown\n"), 0644); err != nil {
		t.Fatalf("write analytics template: %v", err)
	}

	project := &models.Project{Name: "PR Project", RepoPath: repoDir, RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Create PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreePath: repoDir, WorktreeBranch: "task/pr-prep", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	if status := runGit(t, repoDir, "status", "--porcelain"); !strings.Contains(status, "?? analytics.templ") {
		t.Fatalf("expected local worktree to remain dirty after API branch publish, got %q", status)
	}
	if publishedReq.Branch != "task/pr-prep" || publishedReq.BaseBranch != "main" {
		t.Fatalf("unexpected publish request: %#v", publishedReq)
	}
	if publishedReq.CommitMessage != "Add analytics template" {
		t.Fatalf("expected diff-derived API commit subject, got %q", publishedReq.CommitMessage)
	}
	if strings.Contains(publishedReq.CommitMessage, "Task updates") || strings.Contains(publishedReq.CommitMessage, "Create PR") {
		t.Fatalf("expected no static task PR-prep wording, got %q", publishedReq.CommitMessage)
	}
}

func TestCreateTaskPullRequest_ReusesExistingTaskPR(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	prRepo := repository.NewTaskPullRequestRepo(db)
	h.SetTaskPullRequestRepo(prRepo)

	createCalls := 0
	publishCalls := 0
	var publishedReq service.GitHubPublishBranchRequest
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, _ *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			publishCalls++
			publishedReq = publishReq
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		getPullRequestFn: func(context.Context, *service.GitHubRepoRef, int) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: 22, URL: "https://github.com/openvibely/openvibely/pull/22", State: "open", HeadRef: "task/existing", HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			createCalls++
			return &service.GitHubPullRequest{Number: 1, URL: "https://github.com/x/y/pull/1", State: "open", HeadRef: createReq.Head, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
	})

	project := &models.Project{Name: "Existing PR Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Existing PR", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/existing"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := prRepo.Upsert(context.Background(), &models.TaskPullRequest{TaskID: task.ID, PRNumber: 22, PRURL: "https://github.com/openvibely/openvibely/pull/22", PRState: "open"}); err != nil {
		t.Fatalf("insert existing task PR: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%q trigger=%q", rec.Code, rec.Body.String(), rec.Header().Get("HX-Trigger"))
	}
	if createCalls != 0 {
		t.Fatalf("expected create PR not to run, got %d calls", createCalls)
	}
	if publishCalls != 1 || publishedReq.Branch != task.WorktreeBranch {
		t.Fatalf("expected existing PR branch to be published once, publishCalls=%d publishedReq=%#v", publishCalls, publishedReq)
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger, got %s", trigger)
	}
	if !strings.Contains(trigger, "GitHub PR already exists (#22)") {
		t.Fatalf("expected existing PR toast trigger, got %s", trigger)
	}
}

func TestCreateTaskPullRequest_NoGitHubServiceShowsToast(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))
	// Do NOT set GitHub service — simulates missing GitHub integration

	project := &models.Project{Name: "No GH Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "No GH", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/no-gh"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", rec.Code)
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger, got %s", trigger)
	}
	if !strings.Contains(trigger, "not configured") {
		t.Fatalf("expected toast about GitHub not configured, got %s", trigger)
	}
}

func TestCreateTaskPullRequest_PublishBranchFailureShowsToast(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			return nil, fmt.Errorf("authentication failed: bad credentials")
		},
	})

	project := &models.Project{Name: "Publish Fail Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Publish Fail", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/publish-fail"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", rec.Code)
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger, got %s", trigger)
	}
	if !strings.Contains(trigger, "publish branch") {
		t.Fatalf("expected toast about publish failure, got %s", trigger)
	}
}

func TestCreateTaskPullRequest_CreatePRAlreadyExistsRecoversByFindingPR(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	findCalls := 0
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			findCalls++
			if findCalls == 1 {
				return nil, nil
			}
			return &service.GitHubPullRequest{Number: 88, URL: "https://github.com/openvibely/openvibely/pull/88", State: "open", HeadRef: branch, HeadRepoFullName: "openvibely/openvibely", HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			return nil, fmt.Errorf("github API request failed (422): Validation Failed; A pull request already exists for openvibely:task/create-fail")
		},
	})

	project := &models.Project{Name: "Create Exists Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Create Exists", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/create-fail", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger, got %s", trigger)
	}
	if !strings.Contains(trigger, "GitHub PR reused (#88)") {
		t.Fatalf("expected recovered success toast, got %s", trigger)
	}

	record, err := h.taskPullRequestRepo.GetByTaskID(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("load task pull request: %v", err)
	}
	if record == nil || record.PRNumber != 88 {
		t.Fatalf("expected persisted PR #88, got %#v", record)
	}
}

func TestCreateTaskPullRequest_CreatePRFailureShowsToast(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		publishBranchFn: func(_ context.Context, repo *service.GitHubRepoRef, publishReq service.GitHubPublishBranchRequest) (*service.GitHubPublishBranchResult, error) {
			return &service.GitHubPublishBranchResult{HeadSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			return nil, fmt.Errorf("github API request failed (422): Validation Failed; No commits between main and task/create-fail")
		},
	})

	project := &models.Project{Name: "Create Fail Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Create Fail", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/create-fail", MergeTargetBranch: "main"}
	if err := h.taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status 204, got %d", rec.Code)
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "openvibelyToast") {
		t.Fatalf("expected openvibelyToast trigger, got %s", trigger)
	}
	if !strings.Contains(trigger, "No commits between") {
		t.Fatalf("expected actionable error detail in toast, got %s", trigger)
	}
}

func TestHandler_GetTaskChanges_ShowsMergeOptions(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoPath := createHandlerTestGitRepo(t)
	targetBranch := service.GetCurrentBranch(repoPath)
	project := &models.Project{Name: "Merge Options Project", RepoPath: repoPath, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Merge Options Task",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	worktreePath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoPath)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, branchName); err != nil {
		t.Fatalf("update worktree info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "merge-options.txt"), []byte("merge options\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(worktreePath, "merge options"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Merge commit") {
		t.Fatalf("expected merge options to be rendered, body=%s", body)
	}
	if !strings.Contains(body, "merge_source") {
		t.Fatalf("expected changes-tab merge actions to include merge_source marker, body=%s", body)
	}
	if !strings.Contains(body, "Local") {
		t.Fatalf("expected Local section header in actions dropdown, body=%s", body)
	}
	if !strings.Contains(body, "GitHub") {
		t.Fatalf("expected GitHub section header in actions dropdown, body=%s", body)
	}
}

func TestHandler_GetTaskChanges_HidesMergeOptionsForFailedMergedTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoPath := t.TempDir()
	project := &models.Project{Name: "Failed Merged Project", RepoPath: repoPath, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Failed merged task",
		Category:          models.CategoryCompleted,
		Status:            models.StatusFailed,
		WorktreePath:      "",
		WorktreeBranch:    "task/failed-merged",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusMerged,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChanges(c); err != nil {
		t.Fatalf("GetTaskChanges failed: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/worktree/merge") || strings.Contains(body, "Merge commit") {
		t.Fatalf("did not expect local merge options for already-merged failed task, body=%s", body)
	}
	if strings.Contains(body, "Local") {
		t.Fatalf("did not expect Local section header for already-merged failed task, body=%s", body)
	}
	if !strings.Contains(body, "GitHub") {
		t.Fatalf("expected GitHub section to remain available for already-merged failed task, body=%s", body)
	}
}
