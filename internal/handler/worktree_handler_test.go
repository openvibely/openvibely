package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

func worktreeFormRequest(method, path string, form url.Values) *http.Request {
	var body string
	if form != nil {
		body = form.Encode()
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return req
}

func taskCardActionDisabled(body, mergeType string) bool {
	marker := `data-merge-type="` + mergeType + `"`
	markerIndex := strings.Index(body, marker)
	if markerIndex < 0 {
		return false
	}
	buttonIndex := strings.LastIndex(body[:markerIndex], "<button")
	return buttonIndex >= 0 && strings.Contains(body[buttonIndex:markerIndex], " disabled")
}

func worktreeExecute(e *echo.Echo, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestHandler_TaskCardMergeOptionsUseAuthoritativeStateAndProjectOwnership(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.taskPullRequestRepo = repository.NewTaskPullRequestRepo(db)
	h.SetGitHubService(&fakeGitHubService{})
	h.worktreeSvc = service.NewWorktreeService(h.taskRepo, h.projectRepo, nil)
	ctx := context.Background()
	repoDir := t.TempDir()
	runIn := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run := func(args ...string) string {
		return runIn(repoDir, args...)
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "base")
	run("checkout", "-b", "task/card-merge")
	if err := os.WriteFile(filepath.Join(repoDir, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "feature.txt")
	run("commit", "-m", "feature")
	run("checkout", "main")
	if err := os.WriteFile(filepath.Join(repoDir, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "target.txt")
	run("commit", "-m", "advance target")
	worktreePath := filepath.Join(t.TempDir(), "task-worktree")
	run("worktree", "add", worktreePath, "task/card-merge")

	project := &models.Project{Name: "Merge Cards", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "Card merge", Prompt: "merge", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, WorktreePath: worktreePath, WorktreeBranch: "task/card-merge", MergeTargetBranch: "main", MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if h.taskPullRequestRepo != nil {
		if err := h.taskPullRequestRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "open"}); err != nil {
			t.Fatal(err)
		}
	}
	menuState := h.taskCardMergeMenuStates(ctx, []models.Task{*task}, project.ID)[task.ID]
	if !menuState.LocalEligible || !menuState.RebaseEligible || !menuState.PullEligible || menuState.PullRequest == nil || menuState.PullRequest.PRNumber != 42 {
		t.Fatalf("board render did not precompute authoritative card menu state: %#v", menuState)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil)
	rec := worktreeExecute(e, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("eligible options status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"Merge commit", "Fast-forward only", "Rebase onto main", "Squash merge", "View PR #42", `data-task-card-local-submenu`, `data-task-card-github-submenu`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("eligible options missing %q: %s", want, rec.Body.String())
		}
	}
	if err := h.taskPullRequestRepo.Upsert(ctx, &models.TaskPullRequest{TaskID: task.ID, PRNumber: 42, PRURL: "https://github.com/example/repo/pull/42", PRState: "closed"}); err != nil {
		t.Fatal(err)
	}
	rec = worktreeExecute(e, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil))
	if strings.Contains(rec.Body.String(), "View PR #42") || !strings.Contains(rec.Body.String(), "data-task-card-pr-action") || !strings.Contains(rec.Body.String(), "Create PR") {
		t.Fatalf("closed historical PR should expose current Create PR action, body=%s", rec.Body.String())
	}

	foreign := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id=foreign", nil)
	if got := worktreeExecute(e, foreign); got.Code != http.StatusNotFound {
		t.Fatalf("foreign project should receive 404, got %d: %s", got.Code, got.Body.String())
	}
	foreignPost := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", url.Values{
		"merge_type": {"ff"}, "merge_source": {"task_card"}, "project_id": {"foreign"},
	})
	if got := worktreeExecute(e, foreignPost); got.Code != http.StatusNotFound {
		t.Fatalf("foreign card merge should receive 404, got %d: %s", got.Code, got.Body.String())
	}
	if got := run("rev-parse", "main"); got == run("rev-parse", "task/card-merge") {
		t.Fatal("foreign card request mutated the target branch")
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec = worktreeExecute(e, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil))
	if !taskCardActionDisabled(rec.Body.String(), "rebase") || taskCardActionDisabled(rec.Body.String(), "merge") {
		t.Fatalf("dirty worktree should disable rebase but retain supported commit merge, body=%s", rec.Body.String())
	}
	dirtyRebase := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/rebase", url.Values{
		"merge_source": {"task_card"}, "project_id": {project.ID},
	})
	if got := worktreeExecute(e, dirtyRebase); got.Code != http.StatusConflict {
		t.Fatalf("dirty stale card rebase should receive 409, got %d: %s", got.Code, got.Body.String())
	}
	if err := os.Remove(filepath.Join(worktreePath, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	run("worktree", "lock", worktreePath)
	rec = worktreeExecute(e, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil))
	if strings.Contains(rec.Body.String(), "Merge unavailable") || strings.Contains(rec.Body.String(), "Create PR unavailable") || !taskCardActionDisabled(rec.Body.String(), "merge") || !taskCardActionDisabled(rec.Body.String(), "pr") {
		t.Fatalf("locked worktree should retain stable disabled local and GitHub actions, body=%s", rec.Body.String())
	}
	run("worktree", "unlock", worktreePath)

	if err := os.WriteFile(filepath.Join(worktreePath, "README.md"), []byte("task conflict\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(worktreePath, "add", "README.md")
	runIn(worktreePath, "commit", "-m", "add task conflict")
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("main conflict\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "add main conflict")
	mergeCmd := exec.Command("git", "merge", "task/card-merge")
	mergeCmd.Dir = repoDir
	if out, err := mergeCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected task merge conflict, output=%s", out)
	}
	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
		t.Fatal(err)
	}
	rec = worktreeExecute(e, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "Merge unavailable") || !taskCardActionDisabled(rec.Body.String(), "merge") {
		t.Fatalf("conflict state should retain stable disabled local actions, status=%d body=%s", rec.Code, rec.Body.String())
	}
	conflictPost := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", url.Values{
		"merge_type": {"merge"}, "merge_source": {"task_card"}, "project_id": {project.ID},
	})
	conflictPost.Header.Set("HX-Request", "true")
	if got := worktreeExecute(e, conflictPost); got.Code != http.StatusConflict {
		t.Fatalf("stale conflict card merge should receive 409, got %d: %s", got.Code, got.Body.String())
	}
	if got := run("rev-parse", "main"); got == run("rev-parse", "task/card-merge") {
		t.Fatal("stale conflict card request mutated the target branch")
	}
	run("merge", "--abort")

	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
		t.Fatal(err)
	}
	leaseEntered := make(chan struct{})
	releaseLease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- h.worktreeSvc.WithRepositoryMutation(repoDir, func() error {
			close(leaseEntered)
			<-releaseLease
			return nil
		})
	}()
	<-leaseEntered
	concurrentResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", url.Values{
			"merge_type": {"merge"}, "merge_source": {"task_card"}, "project_id": {project.ID},
		})
		concurrentResult <- worktreeExecute(e, req)
	}()
	select {
	case got := <-concurrentResult:
		close(releaseLease)
		t.Fatalf("concurrent card mutation bypassed repository lock: %d %s", got.Code, got.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
		close(releaseLease)
		t.Fatal(err)
	}
	close(releaseLease)
	if err := <-leaseDone; err != nil {
		t.Fatalf("release repository mutation lease: %v", err)
	}
	select {
	case got := <-concurrentResult:
		if got.Code != http.StatusConflict {
			t.Fatalf("serialized card mutation did not revalidate stale state: %d %s", got.Code, got.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serialized card mutation did not resume after repository lock release")
	}
}

func TestHandler_TaskCardMergeOptionsUseCanonicalMergeEligibility(t *testing.T) {
	tests := []struct {
		name       string
		status     models.TaskStatus
		branch     string
		target     string
		wantReason string
		wantPR     bool
	}{
		{name: "pending task", status: models.StatusPending, branch: "task/existing", target: "main", wantReason: "pending", wantPR: true},
		{name: "blocked task", status: models.StatusBlocked, branch: "task/existing", target: "main", wantReason: "blocked", wantPR: true},
		{name: "missing task branch", status: models.StatusCompleted, branch: "task/missing", target: "main", wantReason: "task branch does not exist", wantPR: true},
		{name: "missing target branch", status: models.StatusCompleted, branch: "task/existing", target: "missing-target", wantReason: "target branch does not exist", wantPR: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _, db := setupTestHandlerWithDB(t)
			h.taskPullRequestRepo = repository.NewTaskPullRequestRepo(db)
			h.SetGitHubService(&fakeGitHubService{})
			ctx := context.Background()
			repoDir := createHandlerTestGitRepo(t)
			target := service.GetCurrentBranch(repoDir)
			runGit(t, repoDir, "checkout", "-b", "task/existing")
			if err := os.WriteFile(filepath.Join(repoDir, "card-eligibility.txt"), []byte("task branch\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runGit(t, repoDir, "add", "card-eligibility.txt")
			runGit(t, repoDir, "commit", "-m", "add task branch change")
			runGit(t, repoDir, "checkout", target)
			project := &models.Project{Name: tt.name, RepoPath: repoDir, IsDefault: true}
			if err := h.projectSvc.Create(ctx, project); err != nil {
				t.Fatal(err)
			}
			mergeTarget := tt.target
			if mergeTarget == "main" {
				mergeTarget = target
			}
			task := &models.Task{
				ProjectID: project.ID, Title: "Card eligibility", Prompt: "test", Category: models.CategoryCompleted,
				Status: tt.status, WorktreeBranch: tt.branch, MergeTargetBranch: mergeTarget, MergeStatus: models.MergeStatusPending,
			}
			if err := h.taskRepo.Create(ctx, task); err != nil {
				t.Fatal(err)
			}

			rec := worktreeExecute(e, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil))
			body := rec.Body.String()
			if rec.Code != http.StatusOK {
				t.Fatalf("options status=%d body=%s", rec.Code, body)
			}
			if !taskCardActionDisabled(body, "merge") || strings.Contains(body, "Merge unavailable") {
				t.Fatalf("ineligible card state did not retain a disabled local action without unavailable copy: %s", body)
			}
			if got := !taskCardActionDisabled(body, "pr"); got != tt.wantPR {
				t.Fatalf("PR eligibility changed: got enabled=%v want=%v body=%s", got, tt.wantPR, body)
			}
		})
	}
}

func TestHandler_TaskCardCreatePROptionUsesAuthoritativeEligibility(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	h.taskPullRequestRepo = repository.NewTaskPullRequestRepo(db)
	ctx := context.Background()
	repoDir := t.TempDir()
	cmd := exec.Command("git", "init", "-b", "main")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	project := &models.Project{Name: "PR eligibility", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "No branch", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	requestOptions := func() string {
		t.Helper()
		rec := worktreeExecute(e, httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/card/merge-options?project_id="+project.ID, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("options status=%d body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	if body := requestOptions(); !taskCardActionDisabled(body, "pr") || strings.Contains(body, "Create PR unavailable") {
		t.Fatalf("task without branch should pre-render a disabled Create PR option: %s", body)
	}

	task.WorktreeBranch = "task/pr-eligibility"
	task.Status = models.StatusRunning
	if err := h.taskRepo.Update(ctx, task); err != nil {
		t.Fatal(err)
	}
	if body := requestOptions(); !taskCardActionDisabled(body, "pr") || strings.Contains(body, "Create PR unavailable") {
		t.Fatalf("running task should pre-render a disabled Create PR option: %s", body)
	}
}

func TestHandler_UpdateTask_UnchecksAutoMerge(t *testing.T) {
	// Bug: unchecking auto_merge in the edit form didn't update the task
	// because unchecked checkboxes send no value and the handler skipped the update.
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	// Create task with auto_merge enabled
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Auto Merge Test",
		Prompt:            "do something",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		AutoMerge:         true,
		MergeTargetBranch: "main",
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Verify auto_merge is true
	got, _ := h.taskSvc.GetByID(ctx, task.ID)
	if !got.AutoMerge {
		t.Fatal("expected auto_merge=true after create")
	}

	// Submit edit form WITHOUT auto_merge checked (simulates unchecking)
	// The hidden sentinel field auto_merge_present=1 tells the handler
	// that the edit form was submitted, so it should set auto_merge=false.
	form := url.Values{
		"title":              {"Auto Merge Test"},
		"prompt":             {"do something"},
		"category":           {"active"},
		"priority":           {"2"},
		"auto_merge_present": {"1"},
		// Note: no "auto_merge" key — this is what happens when checkbox is unchecked
	}

	req := worktreeFormRequest(http.MethodPut, "/tasks/"+task.ID, form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify auto_merge is now false
	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if updated.AutoMerge {
		t.Error("expected auto_merge=false after unchecking, but got true")
	}
}

func TestHandler_UpdateTaskAutoMerge_Toggle(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Worktree Auto-merge Toggle",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		AutoMerge:         false,
		MergeTargetBranch: "",
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Enable auto-merge via the worktree panel endpoint
	form := url.Values{
		"auto_merge":          {"on"},
		"merge_target_branch": {"develop"},
	}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/auto-merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	updated, _ := h.taskSvc.GetByID(ctx, task.ID)
	if !updated.AutoMerge {
		t.Error("expected auto_merge=true after toggle on")
	}
	if updated.MergeTargetBranch != "develop" {
		t.Errorf("expected merge_target_branch=develop, got %q", updated.MergeTargetBranch)
	}
}

func TestHandler_GetTaskWorktreeInfo(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Worktree Info Task",
		Prompt:    "test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Task without worktree - should show "no worktree" message
	req := worktreeFormRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree", nil)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No worktree created") {
		t.Error("expected 'No worktree created' message for task without worktree")
	}

	// Set worktree info manually
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, "/tmp/wt", "task/abc-test"); err != nil {
		t.Fatal(err)
	}
	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusPending); err != nil {
		t.Fatal(err)
	}

	// Task with worktree - should show branch info
	req = worktreeFormRequest(http.MethodGet, "/tasks/"+task.ID+"/worktree", nil)
	req.Header.Set("HX-Request", "true")
	rec = worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "task/abc-test") {
		t.Error("expected branch name in response")
	}
	if !strings.Contains(body, "Pending Merge") {
		t.Error("expected merge status badge")
	}
}

func TestHandler_CreateTask_WithAutoMerge(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: "/tmp/test", IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"title":      {"Task with Auto Merge"},
		"prompt":     {"do work"},
		"category":   {"active"},
		"priority":   {"1"},
		"auto_merge": {"on"},
	}

	req := worktreeFormRequest(http.MethodPost, "/tasks?project_id="+project.ID, form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the created task has auto_merge enabled
	tasks, err := h.taskSvc.ListByProject(ctx, project.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	var found *models.Task
	for i := range tasks {
		if tasks[i].Title == "Task with Auto Merge" {
			found = &tasks[i]
			break
		}
	}
	if found == nil {
		t.Fatal("created task not found")
	}
	if !found.AutoMerge {
		t.Error("expected auto_merge=true on created task")
	}
}

func TestHandler_ChangesMergeEligibilityBlocksUnsafeAndMissingStates(t *testing.T) {
	tests := []struct {
		name         string
		status       models.TaskStatus
		branch       string
		target       string
		wantPostCode int
		wantReason   string
	}{
		{name: "blocked task", status: models.StatusBlocked, branch: "task/blocked", target: "main", wantPostCode: http.StatusConflict, wantReason: "blocked"},
		{name: "missing task branch", status: models.StatusCompleted, branch: "task/missing", target: "main", wantPostCode: http.StatusConflict, wantReason: "does not exist"},
		{name: "missing target branch", status: models.StatusCompleted, branch: "task/existing", target: "missing-target", wantPostCode: http.StatusConflict, wantReason: "target branch"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
			ctx := context.Background()
			repoDir := createHandlerTestGitRepo(t)
			target := service.GetCurrentBranch(repoDir)
			project := &models.Project{Name: tt.name, RepoPath: repoDir, IsDefault: true}
			if err := h.projectSvc.Create(ctx, project); err != nil {
				t.Fatal(err)
			}
			branch := tt.branch
			if branch == "task/existing" {
				runGit(t, repoDir, "branch", branch, target)
			}
			mergeTarget := tt.target
			if mergeTarget == "main" {
				mergeTarget = target
			}
			task := &models.Task{
				ProjectID: project.ID, Title: tt.name, Prompt: "test", Category: models.CategoryCompleted,
				Status: tt.status, WorktreeBranch: branch, MergeTargetBranch: mergeTarget, MergeStatus: models.MergeStatusPending,
			}
			if err := h.taskRepo.Create(ctx, task); err != nil {
				t.Fatal(err)
			}

			getReq := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
			getRec := worktreeExecute(e, getReq)
			if getRec.Code != http.StatusOK {
				t.Fatalf("GET Changes returned %d: %s", getRec.Code, getRec.Body.String())
			}
			if strings.Contains(getRec.Body.String(), "/tasks/"+task.ID+"/worktree/merge") {
				t.Fatalf("ineligible state exposed merge action: %s", getRec.Body.String())
			}

			form := url.Values{"merge_type": {"merge"}, "merge_source": {"changes_tab"}}
			postReq := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
			postReq.Header.Set("HX-Request", "true")
			postRec := worktreeExecute(e, postReq)
			if postRec.Code != tt.wantPostCode || !strings.Contains(strings.ToLower(postRec.Body.String()), tt.wantReason) {
				t.Fatalf("POST merge got %d %q, want %d containing %q", postRec.Code, postRec.Body.String(), tt.wantPostCode, tt.wantReason)
			}
		})
	}
}

func TestHandler_TaskCardRebaseRevalidatesEligibilityAfterRepositoryLease(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	target := service.GetCurrentBranch(repoDir)
	project := &models.Project{Name: "Card rebase lease", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "Card rebase lease", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, MergeTargetBranch: target, MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branchName); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "task-change.txt"), []byte("task change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "add task change"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "target-change.txt"), []byte("target change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "target-change.txt")
	runGit(t, repoDir, "commit", "-m", "advance target")

	leaseEntered := make(chan struct{})
	releaseLease := make(chan struct{})
	leaseDone := make(chan error, 1)
	go func() {
		leaseDone <- h.worktreeSvc.WithRepositoryMutation(repoDir, func() error {
			close(leaseEntered)
			<-releaseLease
			return nil
		})
	}()
	<-leaseEntered

	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/rebase", url.Values{
			"merge_source": {"task_card"}, "project_id": {project.ID},
		})
		req.Header.Set("HX-Request", "true")
		result <- worktreeExecute(e, req)
	}()
	select {
	case rec := <-result:
		close(releaseLease)
		t.Fatalf("card rebase bypassed repository lease: %d %s", rec.Code, rec.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusBlocked); err != nil {
		close(releaseLease)
		t.Fatal(err)
	}
	close(releaseLease)
	if err := <-leaseDone; err != nil {
		t.Fatalf("release repository mutation lease: %v", err)
	}

	select {
	case rec := <-result:
		if rec.Code != http.StatusConflict {
			t.Fatalf("stale card rebase returned %d, want 409: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(strings.ToLower(rec.Body.String()), "blocked") {
			t.Fatalf("stale card rebase response missing authoritative reason: %s", rec.Body.String())
		}
		if trigger := rec.Header().Get("HX-Trigger"); !strings.Contains(trigger, "openvibelyToast") || !strings.Contains(strings.ToLower(trigger), "blocked") {
			t.Fatalf("stale card rebase response missing failure toast: %q", trigger)
		}
		if strings.Contains(rec.Body.String(), "changes-actions-dropdown") {
			t.Fatalf("stale card rebase returned Changes markup to the Kanban target: %s", rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("card rebase did not resume after repository lease release")
	}
}

func TestHandler_RebaseTaskBranchRejectsForgedIneligibleRequests(t *testing.T) {
	tests := []struct {
		name       string
		status     models.TaskStatus
		target     string
		wantReason string
	}{
		{name: "blocked task", status: models.StatusBlocked, wantReason: "blocked"},
		{name: "missing target", status: models.StatusCompleted, target: "missing-target", wantReason: "target branch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
			ctx := context.Background()
			repoDir := createHandlerTestGitRepo(t)
			target := service.GetCurrentBranch(repoDir)
			project := &models.Project{Name: "Forged rebase " + tt.name, RepoPath: repoDir, IsDefault: true}
			if err := h.projectSvc.Create(ctx, project); err != nil {
				t.Fatal(err)
			}
			mergeTarget := target
			task := &models.Task{
				ProjectID: project.ID, Title: "Forged rebase " + tt.name, Prompt: "test", Category: models.CategoryCompleted,
				Status: tt.status, MergeTargetBranch: mergeTarget, MergeStatus: models.MergeStatusPending,
			}
			if err := h.taskRepo.Create(ctx, task); err != nil {
				t.Fatal(err)
			}
			wtPath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branchName); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(wtPath, "rebase-ahead.txt"), []byte("task change\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := service.CommitWorktreeChanges(wtPath, "rebase gate fixture"); err != nil {
				t.Fatal(err)
			}
			if tt.target != "" {
				if err := h.taskRepo.UpdateAutoMerge(ctx, task.ID, false, tt.target); err != nil {
					t.Fatal(err)
				}
			}

			req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/rebase", nil)
			req.Header.Set("HX-Request", "true")
			rec := worktreeExecute(e, req)
			if rec.Code != http.StatusConflict || !strings.Contains(strings.ToLower(rec.Body.String()), tt.wantReason) {
				t.Fatalf("forged rebase got %d %q, want 409 containing %q", rec.Code, rec.Body.String(), tt.wantReason)
			}
		})
	}
}

func TestHandler_StaleTerminalConflictRecoversChangesAndRecoveryPosts(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	target := service.GetCurrentBranch(repoDir)
	project := &models.Project{Name: "Stale conflict recovery", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "Stale conflict", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, MergeTargetBranch: target, MergeStatus: models.MergeStatusConflict,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath, task.WorktreeBranch = wtPath, branchName
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branchName); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "stale-conflict.txt"), []byte("recover\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "stale conflict recovery"); err != nil {
		t.Fatal(err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	getRec := worktreeExecute(e, getReq)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), "/tasks/"+task.ID+"/worktree/merge") {
		t.Fatalf("stale conflict did not recover merge actions: %d %s", getRec.Code, getRec.Body.String())
	}
	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusPending {
		t.Fatalf("stale conflict status = %q, want pending", updated.MergeStatus)
	}

	if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
		t.Fatal(err)
	}
	form := url.Values{"merge_source": {"changes_tab"}}
	for _, path := range []string{"resolve", "abort"} {
		req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/"+path, form)
		req.Header.Set("HX-Request", "true")
		rec := worktreeExecute(e, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/tasks/"+task.ID+"/worktree/merge") {
			t.Fatalf("stale conflict %s did not refresh recoverable Changes: %d %s", path, rec.Code, rec.Body.String())
		}
		if err := h.taskRepo.UpdateMergeStatus(ctx, task.ID, models.MergeStatusConflict); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHandler_MergeTaskBranch_CommitFailure_ReturnsHTMXErrorWithToast(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	targetBranch := service.GetCurrentBranch(repoDir)
	project := &models.Project{Name: "Test Project", Description: "Test", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	task := &models.Task{
		ProjectID: project.ID, Title: "Merge Failure", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, MergeTargetBranch: targetBranch, MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	worktreePath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath, task.WorktreeBranch = worktreePath, branchName
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, worktreePath, branchName); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "dirty.txt"), []byte("dirty\n"), 0644); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(repoDir, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"merge_type": {"merge"}}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Local merge failed") {
		t.Fatalf("expected merge failure body, got %s", rec.Body.String())
	}
	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected HX-Trigger toast, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, "Local merge failed") {
		t.Fatalf("expected toast message to include merge failure, got %q", hxTrigger)
	}
}

func TestHandler_MergeTaskBranch_ChangesTabRecoverableFailureRendersRetryAndSucceeds(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	targetBranch := service.GetCurrentBranch(repoDir)
	project := &models.Project{Name: "Retry Merge Project", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Retry failed merge",
		Prompt:            "test",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branchName); err != nil {
		t.Fatal(err)
	}

	blockingName := "retry-after-refusal.txt"
	if err := os.WriteFile(filepath.Join(wtPath, blockingName), []byte("task branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "add retry fixture"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, blockingName), []byte("user checkout work\n"), 0644); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"merge_type": {"merge"}, "merge_source": {"changes_tab"}}
	firstReq := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	firstReq.Header.Set("HX-Request", "true")
	first := worktreeExecute(e, firstReq)
	if first.Code != http.StatusOK {
		t.Fatalf("expected recoverable failure to refresh Changes with 200, got %d: %s", first.Code, first.Body.String())
	}
	if body := first.Body.String(); !strings.Contains(body, "Worktree Changes") || !strings.Contains(body, "Merge commit") || !strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected failed merge response to retain retry actions, got %s", body)
	}
	failedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.MergeStatus != models.MergeStatusFailed {
		t.Fatalf("expected failed merge status, got %s", failedTask.MergeStatus)
	}
	if got, err := os.ReadFile(filepath.Join(repoDir, blockingName)); err != nil || string(got) != "user checkout work\n" {
		t.Fatalf("target checkout work was not preserved: %q, %v", got, err)
	}

	if err := os.Remove(filepath.Join(repoDir, blockingName)); err != nil {
		t.Fatal(err)
	}
	secondReq := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	secondReq.Header.Set("HX-Request", "true")
	second := worktreeExecute(e, secondReq)
	if second.Code != http.StatusOK {
		t.Fatalf("expected retry to succeed, got %d: %s", second.Code, second.Body.String())
	}
	if body := second.Body.String(); strings.Contains(body, "/worktree/merge") || strings.Contains(body, "Merge commit") {
		t.Fatalf("expected terminal merged response to hide retry actions, got %s", body)
	}
	mergedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if mergedTask.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merged status after retry, got %s", mergedTask.MergeStatus)
	}
}

func TestHandler_MergeTaskBranch_ChangesTabSquashConflictRefreshesRetryWithoutRecoveryActions(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	target := service.GetCurrentBranch(repoDir)
	conflictPath := filepath.Join(repoDir, "changes-squash-conflict.txt")
	if err := os.WriteFile(conflictPath, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "changes-squash-conflict.txt")
	runGit(t, repoDir, "commit", "-m", "squash conflict base")
	project := &models.Project{Name: "Changes squash conflict", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "Changes squash conflict", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, MergeTargetBranch: target, MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	wtPath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath, task.WorktreeBranch = wtPath, branchName
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branchName); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, "changes-squash-conflict.txt"), []byte("task\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "task squash conflict"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflictPath, []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "changes-squash-conflict.txt")
	runGit(t, repoDir, "commit", "-m", "target squash conflict")

	form := url.Values{"merge_type": {"squash"}, "merge_source": {"changes_tab"}}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected authoritative retry fragment, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Merge commit") || !strings.Contains(body, "Squash merge") {
		t.Fatalf("cleaned squash conflict did not restore retry actions: %s", body)
	}
	if strings.Contains(body, "/worktree/resolve") || strings.Contains(body, "/worktree/abort") {
		t.Fatalf("cleaned squash conflict exposed unusable recovery actions: %s", body)
	}
	if service.HasActiveMerge(repoDir) || len(service.ActiveConflictFiles(repoDir)) != 0 {
		t.Fatalf("squash conflict remained active after response: merge=%v conflicts=%v", service.HasActiveMerge(repoDir), service.ActiveConflictFiles(repoDir))
	}
	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusFailed {
		t.Fatalf("merge status=%q, want failed", updated.MergeStatus)
	}
}

func TestHandler_GetTaskChanges_LiveConflictRecoverySurvivesStatusPersistenceFailure(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()
	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := service.GetCurrentBranch(repoDir)

	conflictFile := filepath.Join(repoDir, "persistence-conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "persistence-conflict.txt")
	runGit(t, repoDir, "commit", "-m", "conflict base")
	branchName := "task/live-conflict-persistence-failure"
	runGit(t, repoDir, "checkout", "-b", branchName)
	if err := os.WriteFile(conflictFile, []byte("task branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "persistence-conflict.txt")
	runGit(t, repoDir, "commit", "-m", "task conflict")
	runGit(t, repoDir, "checkout", targetBranch)
	if err := os.WriteFile(conflictFile, []byte("target branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoDir, "add", "persistence-conflict.txt")
	runGit(t, repoDir, "commit", "-m", "target conflict")
	mergeCmd := exec.Command("git", "merge", "--no-ff", branchName)
	mergeCmd.Dir = repoDir
	if out, err := mergeCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected fixture merge conflict, got success: %s", out)
	}

	project := &models.Project{Name: "Conflict persistence failure", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID: project.ID, Title: "Live conflict persistence failure", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, WorktreeBranch: branchName, MergeTargetBranch: targetBranch, MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_conflict_status BEFORE UPDATE OF merge_status ON tasks WHEN NEW.merge_status = 'conflict' BEGIN SELECT RAISE(ABORT, 'injected conflict status failure'); END`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
	rec := worktreeExecute(e, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET Changes returned %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"AI Resolve Conflicts", "Abort Merge", "merge conflict is active"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected live conflict recovery content %q despite persistence failure, got %s", want, body)
		}
	}
	if strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") {
		t.Fatalf("live conflict exposed ordinary merge actions after persistence failure: %s", body)
	}
	stored, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.MergeStatus != models.MergeStatusPending {
		t.Fatalf("fixture did not preserve stale pending status, got %q", stored.MergeStatus)
	}
}

func TestHandler_MergeTaskBranch_Conflict_ReturnsHTMXToast(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}
	mustRunOutput := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	mustRun("git", "init")
	mustRun("git", "config", "user.email", "test@example.com")
	mustRun("git", "config", "user.name", "Test User")
	defaultBranch := mustRunOutput("git", "branch", "--show-current")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	conflictFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("shared line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "base")

	worktreeBranch := "task/conflict-merge"
	mustRun("git", "checkout", "-b", worktreeBranch)
	if err := os.WriteFile(conflictFile, []byte("branch change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "branch change")

	mustRun("git", "checkout", defaultBranch)
	if err := os.WriteFile(conflictFile, []byte("target change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "target change")

	project := &models.Project{
		Name: "Test Project", Description: "Test", RepoPath: repoDir, IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Merge Conflict",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreeBranch:    worktreeBranch,
		MergeTargetBranch: defaultBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"merge_type": {"merge"}, "merge_source": {"changes_tab"}}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	hxTrigger := rec.Header().Get("HX-Trigger")
	if !strings.Contains(hxTrigger, "openvibelyToast") {
		t.Fatalf("expected conflict toast trigger, got %q", hxTrigger)
	}
	if !strings.Contains(hxTrigger, "Local merge has conflicts") {
		t.Fatalf("expected conflict toast message, got %q", hxTrigger)
	}
	body := rec.Body.String()
	for _, want := range []string{"Worktree Changes", "AI Resolve Conflicts", "Abort Merge", "merge conflict is active"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected Changes conflict recovery content %q, got %s", want, body)
		}
	}
	if strings.Contains(body, "/worktree/merge") || strings.Contains(body, "Fast-forward only") {
		t.Fatalf("conflicted Changes response must hide ordinary merge actions, got %s", body)
	}

	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusConflict {
		t.Fatalf("expected merge_status=conflict, got %s", updated.MergeStatus)
	}

	leaseStarted := make(chan struct{})
	leaseRelease := make(chan struct{})
	leaseDone := make(chan error, 1)
	stopLease := errors.New("stop handler recovery lease")
	go func() {
		_, leaseErr := h.worktreeSvc.MergeBranchValidated(ctx, &models.Task{}, repoDir, "merge", func() error {
			close(leaseStarted)
			<-leaseRelease
			return stopLease
		})
		leaseDone <- leaseErr
	}()
	<-leaseStarted
	abortForm := url.Values{"merge_source": {"changes_tab"}}
	abortReq := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/abort", abortForm)
	abortReq.Header.Set("HX-Request", "true")
	abortRec := worktreeExecute(e, abortReq)
	if abortRec.Code != http.StatusOK {
		close(leaseRelease)
		<-leaseDone
		t.Fatalf("expected busy Abort to refresh Changes, got %d: %s", abortRec.Code, abortRec.Body.String())
	}
	if !strings.Contains(abortRec.Body.String(), "/tasks/"+task.ID+"/worktree/abort") || strings.Contains(abortRec.Body.String(), "/tasks/"+task.ID+"/worktree/merge") {
		close(leaseRelease)
		<-leaseDone
		t.Fatalf("busy Abort did not preserve conflict recovery controls: %s", abortRec.Body.String())
	}
	if trigger := abortRec.Header().Get("HX-Trigger"); !strings.Contains(trigger, "already in progress") {
		close(leaseRelease)
		<-leaseDone
		t.Fatalf("busy Abort did not emit repository mutation feedback: %q", trigger)
	}
	if !service.HasActiveMerge(repoDir) || len(service.ActiveConflictFiles(repoDir)) == 0 {
		close(leaseRelease)
		<-leaseDone
		t.Fatal("busy Abort mutated the active conflict")
	}
	resolveReq := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/resolve", abortForm)
	resolveReq.Header.Set("HX-Request", "true")
	resolveRec := worktreeExecute(e, resolveReq)
	if resolveRec.Code != http.StatusOK || !strings.Contains(resolveRec.Body.String(), "/tasks/"+task.ID+"/worktree/resolve") {
		close(leaseRelease)
		<-leaseDone
		t.Fatalf("expected busy Resolve to preserve conflict Changes, got %d: %s", resolveRec.Code, resolveRec.Body.String())
	}
	if trigger := resolveRec.Header().Get("HX-Trigger"); !strings.Contains(trigger, "already in progress") {
		close(leaseRelease)
		<-leaseDone
		t.Fatalf("busy Resolve did not emit repository mutation feedback: %q", trigger)
	}
	if !service.HasActiveMerge(repoDir) || len(service.ActiveConflictFiles(repoDir)) == 0 {
		close(leaseRelease)
		<-leaseDone
		t.Fatal("busy Resolve mutated the active conflict")
	}
	close(leaseRelease)
	if leaseErr := <-leaseDone; !errors.Is(leaseErr, stopLease) {
		t.Fatalf("lease holder returned %v, want validator stop", leaseErr)
	}

	unrelatedBranch := "task/unrelated-during-conflict"
	mustRun("git", "branch", unrelatedBranch, defaultBranch)
	unrelated := &models.Task{
		ProjectID: project.ID, Title: "Unrelated conflict task", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, WorktreeBranch: unrelatedBranch, MergeTargetBranch: defaultBranch, MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, unrelated); err != nil {
		t.Fatal(err)
	}
	unrelatedReq := httptest.NewRequest(http.MethodGet, "/tasks/"+unrelated.ID+"/changes", nil)
	unrelatedRec := worktreeExecute(e, unrelatedReq)
	if unrelatedRec.Code != http.StatusOK {
		t.Fatalf("unrelated Changes returned %d: %s", unrelatedRec.Code, unrelatedRec.Body.String())
	}
	if strings.Contains(unrelatedRec.Body.String(), "/tasks/"+unrelated.ID+"/worktree/resolve") || strings.Contains(unrelatedRec.Body.String(), "/tasks/"+unrelated.ID+"/worktree/abort") {
		t.Fatalf("unrelated task exposed another task's conflict recovery controls: %s", unrelatedRec.Body.String())
	}
	unrelatedUpdated, err := h.taskRepo.GetByID(ctx, unrelated.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unrelatedUpdated.MergeStatus == models.MergeStatusConflict {
		t.Fatal("unrelated task inherited active repository conflict status")
	}

	finalAbortReq := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/abort", abortForm)
	finalAbortReq.Header.Set("HX-Request", "true")
	finalAbortRec := worktreeExecute(e, finalAbortReq)
	if finalAbortRec.Code != http.StatusOK {
		t.Fatalf("successful Abort returned %d: %s", finalAbortRec.Code, finalAbortRec.Body.String())
	}
	if service.HasActiveMerge(repoDir) || len(service.ActiveConflictFiles(repoDir)) != 0 {
		t.Fatal("successful Abort left active Git conflict state")
	}
	abortedTask, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if abortedTask.MergeStatus != models.MergeStatusPending {
		t.Fatalf("successful Abort persisted status = %q, want pending", abortedTask.MergeStatus)
	}
	if body := finalAbortRec.Body.String(); !strings.Contains(body, "/tasks/"+task.ID+"/worktree/merge") || strings.Contains(body, "/tasks/"+task.ID+"/worktree/abort") {
		t.Fatalf("successful Abort did not refresh retryable Changes actions: %s", body)
	}
}

func TestHandler_MergeTaskBranch_ActiveConflictBlocksDuplicateMerge(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	repoDir := t.TempDir()
	mustRun := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
	}
	mustRunOutput := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v failed: %v\n%s", name, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	mustRun("git", "init")
	mustRun("git", "config", "user.email", "test@example.com")
	mustRun("git", "config", "user.name", "Test User")
	defaultBranch := mustRunOutput("git", "branch", "--show-current")
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	conflictFile := filepath.Join(repoDir, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "base")

	worktreeBranch := "task/active-conflict"
	mustRun("git", "checkout", "-b", worktreeBranch)
	if err := os.WriteFile(conflictFile, []byte("branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "branch")

	mustRun("git", "checkout", defaultBranch)
	if err := os.WriteFile(conflictFile, []byte("target\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mustRun("git", "add", "conflict.txt")
	mustRun("git", "commit", "-m", "target")
	mergeCmd := exec.Command("git", "merge", "--no-ff", worktreeBranch)
	mergeCmd.Dir = repoDir
	if out, err := mergeCmd.CombinedOutput(); err == nil {
		t.Fatalf("expected manual setup merge to conflict, got success: %s", out)
	}

	project := &models.Project{Name: "Test Project", Description: "Test", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Active Conflict",
		Prompt:            "test",
		Category:          models.CategoryActive,
		Status:            models.StatusCompleted,
		WorktreeBranch:    worktreeBranch,
		MergeTargetBranch: defaultBranch,
		MergeStatus:       models.MergeStatusConflict,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	form := url.Values{"merge_type": {"merge"}}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 while active conflict exists, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "merge conflict is already active") {
		t.Fatalf("expected active conflict message, got %s", rec.Body.String())
	}
}

func TestHandler_MergeTaskBranch_ChangesTabFastForwardAdvancesTargetAndRefreshesChanges(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()

	repoDir := createHandlerTestGitRepo(t)
	targetBranch := service.GetCurrentBranch(repoDir)

	project := &models.Project{
		Name: "Fast Forward Project", RepoPath: repoDir, IsDefault: true,
	}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}

	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Changes tab ff",
		Prompt:            "test",
		Category:          models.CategoryCompleted,
		Status:            models.StatusCompleted,
		MergeTargetBranch: targetBranch,
		MergeStatus:       models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}

	wtPath, branchName, err := h.worktreeSvc.SetupWorktree(ctx, task, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = wtPath
	task.WorktreeBranch = branchName
	if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, wtPath, branchName); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(wtPath, "ff_from_changes.txt"), []byte("merged by changes tab ff\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "ff change"); err != nil {
		t.Fatal(err)
	}
	branchTip := runGit(t, repoDir, "rev-parse", branchName)

	form := url.Values{
		"merge_type":   {"ff"},
		"merge_source": {"changes_tab"},
	}
	req := worktreeFormRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", form)
	req.Header.Set("HX-Request", "true")
	rec := worktreeExecute(e, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := runGit(t, repoDir, "rev-parse", targetBranch); got != branchTip {
		t.Fatalf("expected %s to fast-forward to task branch tip %s, got %s", targetBranch, branchTip, got)
	}
	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MergeStatus != models.MergeStatusMerged {
		t.Fatalf("expected merge_status merged, got %s", updated.MergeStatus)
	}
	trigger := rec.Header().Get("HX-Trigger")
	var triggerData map[string]any
	if err := json.Unmarshal([]byte(trigger), &triggerData); err != nil {
		t.Fatalf("expected valid HX-Trigger JSON, got %q: %v", trigger, err)
	}
	if triggerData["refreshChanges"] != true {
		t.Fatalf("expected refreshChanges trigger, got %q", trigger)
	}
	if _, ok := triggerData["showToast"]; ok {
		t.Fatalf("merge success must not emit legacy showToast trigger, got %q", trigger)
	}
	toast, ok := triggerData["openvibelyToast"].(map[string]any)
	if !ok {
		t.Fatalf("expected canonical success toast trigger, got %q", trigger)
	}
	if toast["message"] != "Merged locally into "+targetBranch || toast["status"] != "completed" || toast["task_id"] != task.ID {
		t.Fatalf("unexpected merge success toast payload: %#v", toast)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Worktree Changes") {
		t.Fatalf("expected changes-tab partial response, got %s", body)
	}
	if strings.Contains(body, "/worktree/merge") || strings.Contains(body, "Fast-forward only") {
		t.Fatalf("expected local merge actions hidden after successful ff merge, got %s", body)
	}
}
