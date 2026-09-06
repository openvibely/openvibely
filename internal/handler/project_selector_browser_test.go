package handler

import (
	"context"
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
			search.dispatchEvent(new Event('search', {bubbles:true}));
			await new Promise(function(resolve) { requestAnimationFrame(resolve); });
			if (visibleOptions().length !== document.querySelectorAll('[data-project-selector-option]').length) fail('production native search clear did not restore every project');
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

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "project-selector-production-page-profile"),
		"--window-size=1024,768", server.URL+"/tasks?project_id=default",
	)
	require.NoError(t, cmd.Start())
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case outcome := <-result:
		cancel()
		<-done
		require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
	case err := <-done:
		require.NoError(t, err)
		t.Fatal("Chrome exited before reporting production project search result")
	case <-ctx.Done():
		t.Fatal("production project search browser regression timed out")
	}
}
