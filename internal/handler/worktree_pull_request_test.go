package handler

import (
	"context"
	"fmt"
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

func TestCreateTaskPullRequest_PushBranchFailureShowsToast(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, nil
		},
		pushBranchFn: func(_ context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error {
			return fmt.Errorf("authentication failed: bad credentials")
		},
	})

	project := &models.Project{Name: "Push Fail Project", RepoPath: "/tmp/repo", RepoURL: "https://github.com/openvibely/openvibely"}
	if err := h.projectSvc.Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Push Fail", Category: models.CategoryActive, Status: models.StatusCompleted, WorktreeBranch: "task/push-fail"}
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
	if !strings.Contains(trigger, "push branch") {
		t.Fatalf("expected toast about push failure, got %s", trigger)
	}
}

func TestCreateTaskPullRequest_CreatePRAlreadyExistsRecoversByFindingPR(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.SetTaskPullRequestRepo(repository.NewTaskPullRequestRepo(db))

	findCalls := 0
	h.SetGitHubService(&fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, nil
		},
		pushBranchFn: func(_ context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error {
			return nil
		},
		findPRFn: func(_ context.Context, repo *service.GitHubRepoRef, branch string) (*service.GitHubPullRequest, error) {
			findCalls++
			if findCalls == 1 {
				return nil, nil
			}
			return &service.GitHubPullRequest{Number: 88, URL: "https://github.com/openvibely/openvibely/pull/88", State: "open"}, nil
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
	if !strings.Contains(trigger, "GitHub PR created (#88)") {
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
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely"}, nil
		},
		pushBranchFn: func(_ context.Context, repoPath, worktreePath, branch string, repo *service.GitHubRepoRef) error {
			return nil
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
	if !strings.Contains(body, "Merge commit") {
		t.Fatalf("expected merge options to be rendered when flag enabled, body=%s", body)
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

func TestHandler_GetTaskChanges_ShowsMergeOptionsForFailedMergedTask(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetTaskChangesMergeOptionsEnabled(true)
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
	if !strings.Contains(body, "Merge commit") {
		t.Fatalf("expected merge options to be rendered for failed merged task, body=%s", body)
	}
	if !strings.Contains(body, "Local") {
		t.Fatalf("expected Local section header for failed merged task, body=%s", body)
	}
}

