package handler

import (
	"context"
	"errors"
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

// Simulate another request changing the saved target after initial validation.
// The locked callback must use this request's destination for both operations.
func TestChangesActionTargetSurvivesConcurrentPreferenceChange(t *testing.T) {
	for _, operation := range []string{"merge", "rebase"} {
		t.Run(operation, func(t *testing.T) {
			h, _, _, _ := setupTestHandlerWithStores(t)
			ctx := context.Background()
			repo := createHandlerTestGitRepo(t)
			main := service.GetCurrentBranch(repo)
			runGit(t, repo, "checkout", "-b", "alternate")
			if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("alternate base\n"), 0600); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "base.txt")
			runGit(t, repo, "commit", "-m", "alternate base")
			runGit(t, repo, "checkout", main)
			runGit(t, repo, "branch", "other")
			project := &models.Project{Name: "Concurrent destination", RepoPath: repo}
			if err := h.projectSvc.Create(ctx, project); err != nil {
				t.Fatal(err)
			}
			h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
			task := &models.Task{ProjectID: project.ID, Title: "Fixed destination", Category: models.CategoryCompleted, Status: models.StatusCompleted, MergeTargetBranch: main}
			if err := h.taskRepo.Create(ctx, task); err != nil {
				t.Fatal(err)
			}
			dir, branch, err := h.worktreeSvc.SetupWorktree(ctx, task, repo)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.taskRepo.UpdateWorktreeInfo(ctx, task.ID, dir, branch); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "task.txt"), []byte("task change\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := service.CommitWorktreeChanges(dir, "task change"); err != nil {
				t.Fatal(err)
			}
			preflight, err := h.loadTaskBranchMutation(ctx, task.ID, "", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.selectChangesLocalTarget(ctx, preflight.task, project, "alternate"); err != nil {
				t.Fatal(err)
			}
			stored, err := h.taskSvc.GetByID(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.MergeTargetBranch != main {
				t.Fatal("initial validation persisted target before acquiring mutation lease")
			}
			options := h.mergeTaskBranchOptions(task.ID, "", false, "merge")
			if operation == "rebase" {
				options = h.rebaseTaskBranchOptions(task.ID, "", false)
			}
			options.selectedTarget = "alternate"
			h.refreshTaskBranchMutation(ctx, preflight, options)
			if err := h.taskRepo.UpdateMergeTargetBranch(ctx, task.ID, "other"); err != nil {
				t.Fatal(err)
			}
			toMutate := preflight.task
			validated := false
			validate := func() error {
				if err := h.revalidateTaskBranchMutation(ctx, preflight, options); err != nil {
					return err
				}
				validated = true
				if preflight.task.MergeTargetBranch != "alternate" {
					t.Fatal("concurrent saved target redirected operation")
				}
				if _, err := h.worktreeSvc.MergeBranchValidated(ctx, toMutate, repo, "merge", func() error {
					t.Error("competing merge entered the mutation callback")
					return nil
				}); !errors.Is(err, service.ErrMergeInProgress) {
					t.Fatalf("expected repository lease to remain held, got %v", err)
				}
				*toMutate = *preflight.task
				return nil
			}
			if operation == "merge" {
				_, err = h.worktreeSvc.MergeBranchValidated(ctx, toMutate, repo, "merge", validate)
			} else {
				_, err = h.worktreeSvc.RebaseBranchValidated(ctx, toMutate, repo, validate)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !validated {
				t.Fatal("mutation skipped validation")
			}
			stored, err = h.taskSvc.GetByID(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.MergeTargetBranch != "alternate" {
				t.Fatalf("saved destination = %q", stored.MergeTargetBranch)
			}
			if operation == "merge" {
				if _, err := exec.Command("git", "-C", repo, "cat-file", "-e", "alternate:task.txt").Output(); err != nil {
					t.Fatal("selected branch did not receive task change")
				}
			} else {
				if err := exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", "alternate", branch).Run(); err != nil {
					t.Fatal("task was not rebased onto selected branch")
				}
			}
			if _, err := exec.Command("git", "-C", repo, "cat-file", "-e", "other:task.txt").Output(); err == nil {
				t.Fatal("concurrent target received task change")
			}
		})
	}
}
