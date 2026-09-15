package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/stretchr/testify/require"
)

func TestProjectRelatedModalClosePositionStaysStationaryInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	_, app, _, _ := setupTestHandlerWithDB(t)
	result := make(chan string, 2)
	runner := `<script>
	(async function() {
		function report(status, message) {
			return fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message || status, keepalive:true});
		}
		function fail(message) { throw new Error(message); }
		function nextFrame() {
			return new Promise(function(resolve) { requestAnimationFrame(function() { resolve(); }); });
		}
		function waitFor(selector, predicate) {
			return new Promise(function(resolve, reject) {
				var deadline = Date.now() + 10000;
				(function poll() {
					var node = document.querySelector(selector);
					if (node && (!predicate || predicate(node))) return resolve(node);
					if (Date.now() > deadline) return reject(new Error('timed out waiting for ' + selector));
					setTimeout(poll, 25);
				})();
			});
		}
		async function assertStationaryClose(name, openFn, dialogSelector) {
			await openFn();
			var dialog = await waitFor(dialogSelector, function(node) { return node.open; });
			var box = dialog.querySelector('.modal-box');
			if (!box) fail(name + ' modal missing .modal-box');
			await nextFrame();
			await nextFrame();
			var before = box.getBoundingClientRect();
			if (!before.width || !before.height) fail(name + ' modal box did not render before close');
			var beforeCenter = before.left + (before.width / 2);
			var close = dialog.querySelector('.ov-modal-close') || dialog.querySelector('.modal-action .btn[type="button"]');
			if (!close) fail(name + ' modal missing close control');
			close.click();
			var sampled = [];
			for (var i = 0; i < 12; i++) {
				await nextFrame();
				var rects = box.getClientRects();
				if (rects.length > 0) {
					var rect = box.getBoundingClientRect();
					if (rect.width > 0 && rect.height > 0) sampled.push({center: rect.left + (rect.width / 2), left: rect.left, top: rect.top});
				}
			}
			for (var j = 0; j < sampled.length; j++) {
				var shift = Math.abs(sampled[j].center - beforeCenter);
				if (shift > 2) fail(name + ' modal center shifted horizontally by ' + shift.toFixed(2) + 'px during close; before=' + beforeCenter.toFixed(2) + ' after=' + sampled[j].center.toFixed(2));
			}
			return name + ': sampled ' + sampled.length + ' painted close frames';
		}
		try {
			var path = window.location.pathname;
			var checks = [];
			checks.push(await assertStationaryClose('project create', function() {
				document.getElementById('new-project-btn').click();
				return waitFor('#new_project_modal', function(node) { return node.open; });
			}, '#new_project_modal'));
			checks.push(await assertStationaryClose('project settings', function() {
				document.getElementById('project-settings-btn').click();
				return waitFor('#edit_project_modal', function(node) { return node.open; });
			}, '#edit_project_modal'));
			if (path === '/tasks') {
				checks.push(await assertStationaryClose('create task', function() {
					if (typeof openNewTaskModal !== 'function') fail('openNewTaskModal is unavailable');
					openNewTaskModal();
					return waitFor('#new_task_modal', function(node) { return node.open; });
				}, '#new_task_modal'));
			} else if (path === '/schedule') {
				checks.push(await assertStationaryClose('schedule create task', function() {
					if (typeof openNewScheduledTaskModal !== 'function') fail('openNewScheduledTaskModal is unavailable');
					openNewScheduledTaskModal();
					return waitFor('#new_scheduled_task_modal', function(node) { return node.open; });
				}, '#new_scheduled_task_modal'));
			} else {
				fail('unexpected modal regression path ' + path);
			}
			await report('pass', checks.join('; '));
		} catch (error) {
			await report('fail', String(error && error.stack || error));
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
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, r)
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

	for _, page := range []string{"/tasks?project_id=default", "/schedule?project_id=default"} {
		cmd := exec.Command(chrome,
			"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
			"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
			"--user-data-dir="+filepath.Join(t.TempDir(), strings.TrimPrefix(strings.Split(page, "?")[0], "/")+"-project-modal-close-profile"),
			"--window-size=1024,768", server.URL+page,
		)
		require.NoError(t, startHandlerBrowserProcess(cmd))
		stopped := false
		func() {
			defer func() {
				if !stopped {
					stopHandlerBrowserProcess(cmd)
				}
			}()
			select {
			case outcome := <-result:
				stopHandlerBrowserProcess(cmd)
				stopped = true
				t.Logf("%s: %s", page, outcome)
				require.True(t, strings.HasPrefix(outcome, "pass:"), page+": "+outcome)
			case <-time.After(45 * time.Second):
				t.Fatalf("modal close position browser regression timed out on %s", page)
			}
		}()
	}
}

func TestProjectSelectorSearchesOnProductionRenderedPageInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	_, app, _, db := setupTestHandlerWithDB(t)
	projectRepo := repository.NewProjectRepo(db)
	swarmProject := models.Project{Name: "Swarm Workspace"}
	unrelatedProject := models.Project{Name: "Unrelated Workspace"}
	require.NoError(t, projectRepo.Create(t.Context(), &swarmProject))
	require.NoError(t, projectRepo.Create(t.Context(), &unrelatedProject))

	result := make(chan string, 1)
	runner := `<script>
	(async function() {
		function report(status, message) {
			return fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message || status, keepalive:true});
		}
		function fail(message) { throw new Error(message); }
		function visibleOptions() {
			return Array.prototype.slice.call(document.querySelectorAll('[data-project-selector-option]')).filter(function(option) {
				return !option.hidden && !option.classList.contains('hidden') && option.getClientRects().length > 0;
			});
		}
		try {
			var trigger = document.querySelector('[data-project-selector] [data-searchable-selector-trigger]');
			var dialog = document.querySelector('[data-project-selector-dialog]');
			var search = document.querySelector('[data-project-selector-search]');
			if (!trigger || !dialog || !search) fail('production project selector markup is incomplete');
			trigger.click();
			if (!dialog.open || document.activeElement !== search) fail('production project selector did not open and focus search');
			if (!document.execCommand('insertText', false, 'default')) fail('browser text insertion was not supported');
			await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
			var visible = visibleOptions();
			if (visible.length !== 1 || visible[0].dataset.projectId !== 'default') fail('production project search did not paint only the matching current project');
			if (visible[0].getAttribute('aria-selected') !== 'true' || visible[0].querySelector('[data-project-selector-current]').textContent.trim() !== '✓') fail('production matching current project was not shown as selected');
			search.value = '';
			search.dispatchEvent(new Event('input', {bubbles:true}));
			if (!document.execCommand('insertText', false, 'swarm')) fail('browser text insertion was not supported');
			await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
			visible = visibleOptions();
			if (search.value !== 'swarm') fail('production project search did not receive typed text');
			if (visible.length !== 1) fail('production project search painted ' + visible.length + ' rows instead of the sole match');
			if (visible[0].dataset.projectName !== 'Swarm Workspace') fail('production project search did not paint only Swarm Workspace: ' + visible.map(function(option) { return option.dataset.projectName; }).join(','));
			if (visible.some(function(option) { return option.dataset.projectName === 'Unrelated Workspace'; })) fail('production project search retained an unrelated row');
			search.value = '';
			search.dispatchEvent(new Event('input', {bubbles:true}));
			await new Promise(function(resolve) { requestAnimationFrame(resolve); });
			if (visibleOptions().length !== document.querySelectorAll('[data-project-selector-option]').length) fail('production manual search clearing did not restore every project');
			await report('pass', 'production project search filtered painted rows');
		} catch (error) {
			await report('fail', String(error && error.stack || error));
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
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, r)
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
		"--user-data-dir="+filepath.Join(t.TempDir(), "project-selector-production-page-profile"),
		"--window-size=1024,768", server.URL+"/tasks?project_id=default",
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
		t.Fatal("production project search browser regression timed out")
	}
}
