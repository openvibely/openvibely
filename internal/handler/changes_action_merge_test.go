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

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestChangesActionMergeUsesSelectedLocalBranch(t *testing.T) {
	h, e, _, _ := setupTestHandlerWithStores(t)
	ctx := context.Background()
	repo := createHandlerTestGitRepo(t)
	main := service.GetCurrentBranch(repo)
	runGit(t, repo, "branch", "alternate")
	project := &models.Project{Name: "Local destinations", RepoPath: repo}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	task := &models.Task{ProjectID: project.ID, Title: "Selected destination", Category: models.CategoryCompleted, Status: models.StatusCompleted, MergeTargetBranch: main, AutoMerge: true, AutoMergeOnGoalAchieved: true}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	dir, branch, err := h.worktreeSvc.SetupWorktree(ctx, task, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, dir, branch); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "selection.txt"), []byte("chosen target\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = service.CommitWorktreeChanges(dir, "selected change"); err != nil {
		t.Fatal(err)
	}
	sha := func(branch string) string {
		t.Helper()
		out, err := exec.Command("git", "-C", repo, "rev-parse", branch).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	before := sha(main)
	form := url.Values{"changes_target": {"alternate"}, "merge_type": {"squash"}, "merge_source": {"changes_tab"}}
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/worktree/merge", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge failed: %d %s", rec.Code, rec.Body.String())
	}
	if sha(main) != before || sha("alternate") == before {
		t.Fatal("merge did not exclusively update selected branch")
	}
	current, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.MergeTargetBranch != "alternate" || !current.AutoMerge || !current.AutoMergeOnGoalAchieved {
		t.Fatalf("target/automatic settings changed incorrectly: %+v", current)
	}
}
