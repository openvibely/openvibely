package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

func TestHandler_GetTaskChangesFile_LoadsRequestedInlineFile(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projects, err := projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("expected default project, err=%v", err)
	}
	project := projects[0]

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Per-file load test",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatalf("list agents: %v", err)
	}
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agents[0].ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	diff := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-old
+new
diff --git a/b.txt b/b.txt
--- a/b.txt
+++ b/b.txt
@@ -1 +1 @@
-foo
+bar
`
	if err := h.execRepo.UpdateDiffOutput(ctx, exec.ID, diff); err != nil {
		t.Fatalf("update diff output: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/file?file_index=1&view=inline&review=true", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChangesFile(c); err != nil {
		t.Fatalf("GetTaskChangesFile failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="diff-file-1"`) {
		t.Fatalf("expected inline file card for file index 1, got: %s", body)
	}
	if !strings.Contains(body, "b.txt") {
		t.Fatalf("expected loaded card to include requested file name, got: %s", body)
	}
}

func TestHandler_GetTaskChangesFile_RecoversLiveWorktreeLineage(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runGit(repoDir, "init", "-b", "main")
	runGit(repoDir, "config", "user.email", "test@example.com")
	runGit(repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "base.txt")
	runGit(repoDir, "commit", "-m", "initial")

	project := &models.Project{Name: "lazy diff recovery", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := createTask(t, h, project.ID, "Recover lazy diff lineage", func(task *models.Task) {
		task.Category = models.CategoryActive
		task.Status = models.StatusRunning
		task.MergeTargetBranch = "main"
	})

	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	worktreeBranch := "task/" + task.ID[:8] + "-current-lineage"
	runGit(repoDir, "worktree", "add", "-b", worktreeBranch, worktreePath, "main")
	if err := os.WriteFile(filepath.Join(worktreePath, "live.txt"), []byte("live lineage\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(worktreePath, "add live lineage"); err != nil {
		t.Fatalf("commit live change: %v", err)
	}

	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatalf("list agents: %v", err)
	}
	execution := &models.Execution{TaskID: task.ID, AgentConfigID: agents[0].ID, Status: models.ExecRunning, PromptSent: task.Prompt}
	if err := h.execRepo.Create(ctx, execution); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	staleDiff := "diff --git a/stale.txt b/stale.txt\nnew file mode 100644\n--- /dev/null\n+++ b/stale.txt\n@@ -0,0 +1 @@\n+stale execution\n"
	if err := h.execRepo.UpdateDiffOutput(ctx, execution.ID, staleDiff); err != nil {
		t.Fatalf("store stale execution diff: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/file?file_index=0&view=inline", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)
	if err := h.GetTaskChangesFile(c); err != nil {
		t.Fatalf("GetTaskChangesFile: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "live.txt") || !strings.Contains(body, "live lineage") {
		t.Fatalf("expected lazy file diff from recovered live lineage, got:\n%s", body)
	}
	if strings.Contains(body, "stale.txt") || strings.Contains(body, "stale execution") {
		t.Fatalf("expected stored execution fallback to be ignored when live worktree exists, got:\n%s", body)
	}
	updated, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if updated.WorktreePath != worktreePath || updated.WorktreeBranch != worktreeBranch {
		t.Fatalf("expected recovered current lineage path=%q branch=%q, got path=%q branch=%q", worktreePath, worktreeBranch, updated.WorktreePath, updated.WorktreeBranch)
	}

	liveReq := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/changes/live?diff_output="+url.QueryEscape(staleDiff), nil)
	liveRec := httptest.NewRecorder()
	liveContext := e.NewContext(liveReq, liveRec)
	liveContext.SetParamNames("taskId")
	liveContext.SetParamValues(task.ID)
	if err := h.GetTaskChangesLive(liveContext); err != nil {
		t.Fatalf("GetTaskChangesLive: %v", err)
	}
	liveBody := liveRec.Body.String()
	if !strings.Contains(liveBody, "live.txt") || strings.Contains(liveBody, "stale.txt") {
		t.Fatalf("expected live fragment to resolve the same recovered worktree diff, got:\n%s", liveBody)
	}
}

func TestHandler_GetTaskChangesFile_LoadsTargetedWorktreeUntrackedFile(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runGit(repoDir, "init", "-b", "main")
	runGit(repoDir, "config", "user.email", "test@example.com")
	runGit(repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "tracked.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "tracked.txt")
	runGit(repoDir, "commit", "-m", "initial")

	project := &models.Project{Name: "targeted lazy worktree", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := createTask(t, h, project.ID, "Target lazy untracked", func(task *models.Task) {
		task.Category = models.CategoryActive
		task.Status = models.StatusRunning
		task.MergeTargetBranch = "main"
	})
	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	worktreeBranch := "task/" + task.ID[:8] + "-targeted-lazy"
	runGit(repoDir, "worktree", "add", "-b", worktreeBranch, worktreePath, "main")
	if err := os.WriteFile(filepath.Join(worktreePath, "tracked.txt"), []byte("base\ntracked change\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "untracked.txt"), []byte("untracked lazy body\n"), 0644); err != nil {
		t.Fatal(err)
	}
	task.WorktreePath = worktreePath
	task.WorktreeBranch = worktreeBranch
	if err := h.taskRepo.Update(ctx, task); err != nil {
		t.Fatalf("update task worktree metadata: %v", err)
	}

	// Tracked git diff files are ordered before synthetic untracked files, so the
	// second lazy card should resolve only the untracked file and not the tracked
	// file's body.
	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/file?file_index=1&view=split", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)
	if err := h.GetTaskChangesFile(c); err != nil {
		t.Fatalf("GetTaskChangesFile: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="diff-file-split-1"`) || !strings.Contains(body, "untracked.txt") || !strings.Contains(body, "untracked lazy body") {
		t.Fatalf("expected split lazy card for untracked file, got:\n%s", body)
	}
	if strings.Contains(body, "tracked change") {
		t.Fatalf("expected targeted lazy response to omit other file hunks, got:\n%s", body)
	}
}

func TestCaptureTaskDiffOutput_UsesCurrentWorktreeLineageWhenMetadataIsStale(t *testing.T) {
	h, _, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	repoDir := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runGit(repoDir, "init", "-b", "main")
	runGit(repoDir, "config", "user.email", "test@example.com")
	runGit(repoDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoDir, "base.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(repoDir, "add", "base.txt")
	runGit(repoDir, "commit", "-m", "initial")

	project := &models.Project{Name: "follow-up final diff", RepoPath: repoDir}
	if err := h.projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := createTask(t, h, project.ID, "Finalize current lineage", func(task *models.Task) {
		task.Category = models.CategoryActive
		task.Status = models.StatusRunning
		task.MergeTargetBranch = "main"
	})
	worktreePath := filepath.Join(repoDir, ".worktrees", "task_"+task.ID)
	worktreeBranch := "task/" + task.ID[:8] + "-followup-current"
	runGit(repoDir, "worktree", "add", "-b", worktreeBranch, worktreePath, "main")
	if err := os.WriteFile(filepath.Join(worktreePath, "committed.txt"), []byte("earlier task commit\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(worktreePath, "earlier task commit"); err != nil {
		t.Fatalf("commit earlier task output: %v", err)
	}
	if err := os.WriteFile(filepath.Join(worktreePath, "pending.txt"), []byte("later follow-up edit\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Simulate stale DB metadata after a follow-up worktree was created. Final
	// capture must still use the current managed worktree and target, not HEAD.
	task.WorktreePath = ""
	task.WorktreeBranch = ""
	diff := h.captureTaskDiffOutput(ctx, task, nil, worktreePath, "follow-up complete")
	if !strings.Contains(diff, "committed.txt") || !strings.Contains(diff, "earlier task commit") {
		t.Fatalf("expected committed task output in target-based final diff:\n%s", diff)
	}
	if !strings.Contains(diff, "pending.txt") || !strings.Contains(diff, "later follow-up edit") {
		t.Fatalf("expected pending follow-up output in target-based final diff:\n%s", diff)
	}
}

func TestHandler_GetTaskChangesFile_InvalidIndexReturnsBadRequest(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projects, err := projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("expected default project, err=%v", err)
	}
	project := projects[0]
	task := createTask(t, h, project.ID, "Invalid index task", func(task *models.Task) {
		task.Category = models.CategoryCompleted
		task.Status = models.StatusCompleted
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/file?file_index=oops&view=inline", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	err = h.GetTaskChangesFile(c)
	if err == nil {
		t.Fatal("expected bad request error")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected echo.HTTPError, got %T", err)
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", httpErr.Code)
	}
}
