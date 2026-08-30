package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestChangesMergeFailureRetryAndIneligibleStatesInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	htmxJS, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	h, e, _ := setupTestHandler(t)
	h.SetWorktreeService(service.NewWorktreeService(h.taskRepo, h.projectRepo, h.settingsRepo))
	ctx := context.Background()
	repoDir := createHandlerTestGitRepo(t)
	targetBranch := service.GetCurrentBranch(repoDir)
	project := &models.Project{Name: "Browser merge retry", RepoPath: repoDir, IsDefault: true}
	if err := h.projectSvc.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{
		ProjectID:         project.ID,
		Title:             "Browser merge retry",
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
	blockingName := "browser-merge-retry.txt"
	if err := os.WriteFile(filepath.Join(wtPath, blockingName), []byte("task branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := service.CommitWorktreeChanges(wtPath, "add browser retry fixture"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, blockingName), []byte("user checkout work\n"), 0644); err != nil {
		t.Fatal(err)
	}

	conflictRepo := createHandlerTestGitRepo(t)
	conflictTarget := service.GetCurrentBranch(conflictRepo)
	conflictFile := filepath.Join(conflictRepo, "browser-conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, conflictRepo, "add", "browser-conflict.txt")
	runGit(t, conflictRepo, "commit", "-m", "conflict base")
	conflictBranch := "task/browser-conflict-recovery"
	runGit(t, conflictRepo, "checkout", "-b", conflictBranch)
	if err := os.WriteFile(conflictFile, []byte("task branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, conflictRepo, "add", "browser-conflict.txt")
	runGit(t, conflictRepo, "commit", "-m", "task conflict")
	runGit(t, conflictRepo, "checkout", conflictTarget)
	if err := os.WriteFile(conflictFile, []byte("target branch\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, conflictRepo, "add", "browser-conflict.txt")
	runGit(t, conflictRepo, "commit", "-m", "target conflict")
	conflictProject := &models.Project{Name: "Browser conflict recovery", RepoPath: conflictRepo}
	if err := h.projectSvc.Create(ctx, conflictProject); err != nil {
		t.Fatal(err)
	}
	conflictTask := &models.Task{
		ProjectID: conflictProject.ID, Title: "Browser conflict recovery", Prompt: "test", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, WorktreeBranch: conflictBranch, MergeTargetBranch: conflictTarget, MergeStatus: models.MergeStatusPending,
	}
	if err := h.taskRepo.Create(ctx, conflictTask); err != nil {
		t.Fatal(err)
	}
	conflictResult, conflictErr := h.worktreeSvc.MergeBranch(ctx, conflictTask, conflictRepo, "merge")
	if conflictErr != nil || conflictResult == nil || len(conflictResult.ConflictFiles) == 0 {
		t.Fatalf("create active browser conflict: result=%#v err=%v", conflictResult, conflictErr)
	}

	var mergeRequests atomic.Int32
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/tasks/"+task.ID+"/worktree/merge" {
			mergeRequests.Add(1)
		}
		e.ServeHTTP(w, r)
	})
	result := make(chan string, 1)
	mux := http.NewServeMux()
	mux.Handle("/tasks/", app)
	mux.HandleFunc("/htmx-2.0.4.min.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(htmxJS)
	})
	mux.HandleFunc("/unblock", func(w http.ResponseWriter, _ *http.Request) {
		if err := os.Remove(filepath.Join(repoDir, blockingName)); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove blocking target file: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/browser-result", func(w http.ResponseWriter, r *http.Request) {
		result <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		initialReq := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes", nil)
		initialReq.Header.Set("HX-Request", "true")
		initialRec := httptest.NewRecorder()
		e.ServeHTTP(initialRec, initialReq)
		if initialRec.Code != http.StatusOK {
			http.Error(w, initialRec.Body.String(), initialRec.Code)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head><script src="/htmx-2.0.4.min.js"></script></head><body>
<div id="changes-content">%s</div>
<script>
(function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > 8000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 10);
      }
      poll();
    });
  }
	  function mergeButton() { return document.querySelector('#changes-content button[hx-post$="/worktree/merge"][hx-vals*="merge_type"]'); }
	  function mergeButtonFor(type) { return Array.from(document.querySelectorAll('#changes-content button[hx-post$="/worktree/merge"]')).find(function(button) { return (button.getAttribute('hx-vals') || '').includes('"merge_type": "' + type + '"'); }); }
	  window.addEventListener('DOMContentLoaded', function() {
	    (async function() {
	      await waitFor(function() { return !!mergeButton(); }, 'initial merge action');
	      var first = mergeButtonFor('merge') || mergeButton();
	      if (first.getAttribute('hx-disabled-elt') !== '#changes-actions-dropdown button') throw new Error('merge action lacks menu-wide duplicate-submit guard');
	      var alternate = mergeButtonFor('squash');
	      first.click();
	      await waitFor(function() { return first.disabled && alternate && alternate.disabled; }, 'all merge options disabled');
	      alternate.click();
	      await waitFor(function() { var current = mergeButton(); return current && current !== first; }, 'failed-state authoritative retry menu');
	      await new Promise(function(resolve) { setTimeout(resolve, 50); });
	      if (!document.body.textContent.includes('Fast-forward only')) throw new Error('failed state lost retry actions');
	      await fetch('/unblock', {method:'POST'});
	      mergeButton().click();
	      await waitFor(function() { return !mergeButton(); }, 'terminal merged state');

	      var conflictHTML = await (await fetch('/tasks/%s/changes', {headers: {'HX-Request': 'true'}})).text();
	      document.getElementById('changes-content').innerHTML = conflictHTML;
	      htmx.process(document.getElementById('changes-content'));
	      if (mergeButton()) throw new Error('conflict state exposed ordinary merge action');
	      if (!document.querySelector('[data-merge-conflict-guidance]')) throw new Error('conflict recovery guidance missing');
	      var resolve = document.querySelector('#changes-content button[hx-post$="/worktree/resolve"]');
	      var abort = document.querySelector('#changes-content button[hx-post$="/worktree/abort"]');
	      if (!resolve || !abort) throw new Error('conflict recovery actions missing');
	      resolve.click();
	      await waitFor(function() { var current = document.querySelector('#changes-content button[hx-post$="/worktree/resolve"]'); return current && current !== resolve; }, 'failed recovery authoritative refresh');
	      if (mergeButton()) throw new Error('failed conflict recovery exposed ordinary merge action');
	      await new Promise(function(resolve) { setTimeout(resolve, 50); });
	      abort = document.querySelector('#changes-content button[hx-post$="/worktree/abort"]');
	      abort.click();
	      await waitFor(function() { return !!mergeButton(); }, 'aborted conflict retry actions');
	      await report('pass', 'ok');
	    })().catch(function(error) { report('fail', error && error.message ? error.message : String(error)); });
	  });})();
	</script></body></html>`, initialRec.Body.String(), conflictTask.ID)
	})

	server := httptest.NewServer(mux)
	defer server.Close()
	stderrPath := filepath.Join(t.TempDir(), "changes-merge-retry.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/",
	)
	cmd.Stderr = stderrFile
	if err := startHandlerBrowserProcess(cmd); err != nil {
		_ = stderrFile.Close()
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-result:
	case <-time.After(20 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopHandlerBrowserProcess(cmd)
	_ = stderrFile.Close()
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Changes merge retry browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
	if got := mergeRequests.Load(); got != 2 {
		t.Fatalf("merge requests = %d, want one failed attempt plus one retry", got)
	}
}
