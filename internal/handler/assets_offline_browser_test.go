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

	"github.com/openvibely/openvibely/internal/auth"
)

// With every non-local host unresolvable, the app shell and login page must still load
// their framework styles and scripts, and must not request anything off the server.
func TestAppAndLoginPagesLoadWithoutExternalNetworkInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Offline Assets").Build()

	probe := `<script>window.addEventListener('load', function() { setTimeout(function() {
	  var problems = [];
	  var isLogin = location.pathname.indexOf('/probe/login') === 0;
	  if (!isLogin) {
	    if (!window.htmx) problems.push('htmx missing');
	    if (!window.marked) problems.push('marked missing');
	    if (!window.hljs) problems.push('highlight.js missing');
	  }
	  var button = document.createElement('button');
	  button.className = 'btn btn-primary';
	  document.body.appendChild(button);
	  var buttonStyle = getComputedStyle(button);
	  if (buttonStyle.backgroundColor === 'rgba(0, 0, 0, 0)' || parseFloat(buttonStyle.minHeight) === 0) problems.push('DaisyUI button styles missing');
	  if (getComputedStyle(document.body).backgroundColor === 'rgba(0, 0, 0, 0)') problems.push('Tailwind utility bg-base-200 missing on body');
	  performance.getEntriesByType('resource').forEach(function(entry) {
	    if (entry.name.indexOf(location.origin + '/') !== 0 && entry.name.indexOf('wails:') !== 0) problems.push('external request ' + entry.name);
	  });
	  fetch('/offline-result?page=' + encodeURIComponent(location.pathname) + '&status=' + (problems.length ? 'fail' : 'pass') + '&detail=' + encodeURIComponent(problems.join('; ')));
	}, 300); });</script>`

	pagesUnderTest := map[string]string{
		"/probe/tasks": "/tasks?project_id=" + project.ID,
		"/probe/login": "/login",
	}
	results := make(chan string, len(pagesUnderTest))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/offline-result" {
			q := r.URL.Query()
			results <- q.Get("page") + " " + q.Get("status") + " " + q.Get("detail")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if target, ok := pagesUnderTest[r.URL.Path]; ok {
			rec := httptest.NewRecorder()
			if target == "/login" {
				// The login page only renders when local auth is on; static assets stay public.
				tc.handler.SetAuthMode(auth.AuthModeLocal)
				tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
				tc.handler.SetAuthMode(auth.AuthModeDisabled)
			} else {
				tc.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", probe+"</body>", 1))
			return
		}
		// The live-update stream never completes, which would hold the page's network open.
		if strings.HasPrefix(r.URL.Path, "/events") {
			http.NotFound(w, r)
			return
		}
		tc.echo.ServeHTTP(w, r)
	}))
	defer server.Close()

	for probePath := range pagesUnderTest {
		cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
			"--host-resolver-rules=MAP * ~NOTFOUND , EXCLUDE localhost , EXCLUDE 127.0.0.1",
			"--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+probePath)
		if err := startHandlerBrowserProcess(cmd); err != nil {
			t.Fatalf("start Chrome: %v", err)
		}
		select {
		case got := <-results:
			if !strings.HasPrefix(got, probePath+" pass") {
				t.Errorf("offline load failed: %s", got)
			}
		case <-time.After(30 * time.Second):
			t.Errorf("%s did not finish loading without external network", probePath)
		}
		stopHandlerBrowserProcess(cmd)
	}
}
