package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", rec.Code)
	}
}

func TestCreateTaskPullRequest_CreatesAndPersistsPR(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", CloneURL: "https://github.com/openvibely/openvibely.git", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
		pushBranchFn: func(_ context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error {
			return nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			return nil, nil
		},
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			return &service.GitHubPullRequest{Number: 77, URL: "https://github.com/openvibely/openvibely/pull/77", State: "open"}, nil
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

	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/pull-request", strings.NewReader(url.Values{}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	trigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(trigger, "Pull request created (#77)") {
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

func TestCreateTaskPullRequest_ReusesExistingTaskPR(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	prRepo := repository.NewTaskPullRequestRepo(db)
	h.SetTaskPullRequestRepo(prRepo)

	createCalls := 0
	h.SetGitHubService(&fakeGitHubService{
		createPRFn: func(_ context.Context, repo *service.GitHubRepoRef, createReq service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			createCalls++
			return &service.GitHubPullRequest{Number: 1, URL: "https://github.com/x/y/pull/1", State: "open"}, nil
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
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if createCalls != 0 {
		t.Fatalf("expected create PR not to run, got %d calls", createCalls)
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "PR already exists (#22)") {
		t.Fatalf("expected existing PR toast trigger, got %s", rec.Header().Get("HX-Trigger"))
	}
}

func TestHandler_GetTaskChanges_ShowsMergeOptionsWhenFlagEnabled(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetTaskChangesMergeOptionsEnabled(true)
	ctx := context.Background()

	repoPath := t.TempDir()
	worktreePath := t.TempDir()
	project := &models.Project{Name: "Flag Enabled Project", RepoPath: repoPath, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Flag Enabled Task",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreePath:      worktreePath,
		WorktreeBranch:    "task/flag-enabled",
		MergeTargetBranch: "main",
		MergeStatus:       models.MergeStatusPending,
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
	if !strings.Contains(body, "Merge to") {
		t.Fatalf("expected merge options to be rendered when flag enabled, body=%s", body)
	}
	if !strings.Contains(body, "merge_source") {
		t.Fatalf("expected changes-tab merge actions to include merge_source marker, body=%s", body)
	}
}
