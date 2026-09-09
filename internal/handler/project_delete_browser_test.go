package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestProjectDeletionConfirmationAndSuccessfulFlowInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	_, app, _, db := setupTestHandlerWithDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := models.Project{Name: "Disposable Browser Delete Project"}
	require.NoError(t, projectRepo.Create(t.Context(), &project))
	require.NoError(t, func() error {
		_, err := db.ExecContext(t.Context(), `
			CREATE TABLE memory_consolidation_runs (id TEXT PRIMARY KEY);
			CREATE TABLE memory_consolidation_schedules (
				project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
				last_run_id TEXT REFERENCES memory_consolidation_runs(id) ON DELETE SET NULL
			);
			INSERT INTO memory_consolidation_schedules(project_id) VALUES (?)`, project.ID)
		return err
	}())
	_, err := db.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `DROP TABLE memory_consolidation_runs`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `PRAGMA foreign_keys=ON`)
	require.NoError(t, err)

	result := make(chan string, 1)
	var deleteRequests atomic.Int32
	runner := `<script>
	(async function() {
		if (sessionStorage.getItem('project-delete-complete') === 'true') {
			await fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':'pass'}, body:'confirmation and successful deletion completed', keepalive:true});
			return;
		}
		function fail(message) { throw new Error(message); }
		function waitFor(selector) {
			return new Promise(function(resolve, reject) {
				var deadline = Date.now() + 10000;
				(function poll() {
					var node = document.querySelector(selector);
					if (node) return resolve(node);
					if (Date.now() > deadline) return reject(new Error('timed out waiting for ' + selector));
					setTimeout(poll, 25);
				})();
			});
		}
		try {
			document.getElementById('project-settings-btn').click();
			var deleteButton = await waitFor('#edit_project_modal .btn-error');
			deleteButton.click();
			var confirm = await waitFor('#delete_project_confirm_modal');
			if (!confirm.open || !confirm.textContent.includes('Disposable Browser Delete Project')) fail('confirmation did not open with project name');
			confirm.querySelector('.modal-action .btn:not(.btn-error)').click();
			if (confirm.open) fail('Cancel did not close confirmation');
			deleteButton.click();
			if (!confirm.open) fail('confirmation did not reopen');
			confirm.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true, cancelable:true}));
			if (confirm.open) fail('Escape did not close confirmation');
			deleteButton.click();
			var confirmButton = confirm.querySelector('.modal-action .btn-error');
			confirmButton.click();
			confirmButton.click();
			if (!confirmButton.disabled || confirm.dataset.deletePending !== 'true') fail('confirmed deletion was not guarded while pending');
			var failureDeadline = Date.now() + 10000;
			while (confirmButton.disabled && Date.now() < failureDeadline) await new Promise(function(resolve) { setTimeout(resolve, 25); });
			var errorMessage = document.getElementById('delete_project_error');
			if (confirmButton.disabled || !confirm.open || !errorMessage || errorMessage.classList.contains('hidden') || !errorMessage.textContent.includes('data was kept')) fail('failed deletion did not remain retryable with clear feedback');
			sessionStorage.setItem('project-delete-complete', 'true');
			confirmButton.click();
			confirmButton.click();
			if (!confirmButton.disabled || confirm.dataset.deletePending !== 'true') fail('retried deletion was not guarded while pending');
		} catch (error) {
			await fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':'fail'}, body:String(error && error.stack || error), keepalive:true});
		}
	})();
	</script>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			select {
			case result <- r.Header.Get("X-Browser-Status") + ":" + string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		firstDelete := false
		if r.Method == http.MethodDelete && r.URL.Path == "/projects/"+project.ID {
			firstDelete = deleteRequests.Add(1) == 1
		}
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, r)
		if firstDelete {
			if _, dropErr := db.ExecContext(r.Context(), `DROP TABLE memory_consolidation_schedules`); dropErr != nil {
				select {
				case result <- "fail:removing disposable legacy constraint: " + dropErr.Error():
				default:
				}
			}
		}
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		body := strings.Replace(recorder.Body.String(), "</body>", runner+"</body>", 1)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "project-delete-profile"),
		"--window-size=1024,768", server.URL+"/tasks?project_id="+project.ID,
	)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(cmd)
		}
	}()
	select {
	case outcome := <-result:
		stopHandlerBrowserProcess(cmd)
		stopped = true
		require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
	case <-time.After(45 * time.Second):
		t.Fatal("project deletion browser regression timed out")
	}
	require.EqualValues(t, 2, deleteRequests.Load(), "each confirmation attempt must submit exactly one DELETE")
	deleted, err := projectRepo.GetByID(t.Context(), project.ID)
	require.NoError(t, err)
	require.Nil(t, deleted)
}
