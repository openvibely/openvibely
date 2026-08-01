package components

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/templates/layout"
)

//go:embed testdata/htmx-2.0.4.min.js
var htmx204 []byte

const htmx204SHA256 = "e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447"

func TestHTMXHistoryNavigationAndTitlesInChrome(t *testing.T) {
	chrome := testChromePath(t)

	actualHash := fmt.Sprintf("%x", sha256.Sum256(htmx204))
	if actualHash != htmx204SHA256 {
		t.Fatalf("pinned HTMX 2.0.4 fixture hash = %s, want %s", actualHash, htmx204SHA256)
	}

	var renderedBase bytes.Buffer
	if err := layout.Base("History fixture", nil, "").Render(context.Background(), &renderedBase); err != nil {
		t.Fatalf("render base layout: %v", err)
	}
	baseHTML := renderedBase.String()
	scriptStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	if scriptStart < 0 {
		t.Fatal("rendered base layout is missing openVibelyNavigate")
	}
	scriptEnd := strings.Index(baseHTML[scriptStart:], "// Scroll position restoration for drop zones")
	if scriptEnd < 0 {
		t.Fatal("could not isolate the production HTMX navigation and title script")
	}
	productionScript := baseHTML[scriptStart : scriptStart+scriptEnd]

	var historyMissRequests atomic.Int32
	var ordinaryBetaRequests atomic.Int32
	var fixtureServer *httptest.Server
	fixtureServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmx204)
		case "/alpha":
			isRestore := r.Header.Get("HX-History-Restore-Request") == "true"
			if isRestore {
				historyMissRequests.Add(1)
				if r.Header.Get("HX-Request") != "true" {
					http.Error(w, "history restore omitted HX-Request", http.StatusBadRequest)
					return
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(historyFixtureDocument(productionScript, "Alpha", "alpha", isRestore, !isRestore)))
		case "/beta":
			if r.Header.Get("HX-History-Restore-Request") == "true" {
				historyMissRequests.Add(1)
				if r.Header.Get("HX-Request") != "true" {
					http.Error(w, "history restore omitted HX-Request", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(historyFixtureDocument(productionScript, "Beta", "beta", true, false)))
				return
			}
			ordinaryBetaRequests.Add(1)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(historyFixtureFragment("Beta", "beta")))
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixtureServer.Close()

	runHeadlessChromeFixture(t, chrome, fixtureServer.URL+"/alpha", "history", 10000, 25*time.Second)
	if got := ordinaryBetaRequests.Load(); got != 1 {
		t.Fatalf("ordinary programmatic navigation requests to beta = %d, want 1; cache-hit Back/Forward unexpectedly used the server", got)
	}
	if got := historyMissRequests.Load(); got != 1 {
		t.Fatalf("HTMX history cache-miss requests = %d, want 1", got)
	}
}

func TestHTMXHistoryOptOutPreventsSecretSnapshotInChrome(t *testing.T) {
	chrome := testChromePath(t)

	var renderedBase bytes.Buffer
	if err := layout.Base("Models history fixture", nil, "").Render(context.Background(), &renderedBase); err != nil {
		t.Fatalf("render base layout: %v", err)
	}
	baseHTML := renderedBase.String()
	scriptStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	if scriptStart < 0 {
		t.Fatal("rendered base layout is missing openVibelyNavigate")
	}
	scriptEnd := strings.Index(baseHTML[scriptStart:], "// Scroll position restoration for drop zones")
	if scriptEnd < 0 {
		t.Fatal("could not isolate the production HTMX navigation script")
	}
	productionScript := baseHTML[scriptStart : scriptStart+scriptEnd]

	const secret = "custom-oauth-secret-must-not-enter-history"
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) {
    document.body.setAttribute('data-test-result', 'fail');
    document.body.setAttribute('data-test-error', message);
  }
  (async function() {
    localStorage.removeItem('htmx-history-cache');
    await window.openVibelyNavigate('/other');
    var cache = localStorage.getItem('htmx-history-cache') || '';
    if (cache.indexOf('` + secret + `') !== -1) throw new Error('secret entered HTMX history cache');
    var entries = JSON.parse(cache || '[]');
    if (entries.some(function(entry) { return entry.url === '/models'; })) {
      throw new Error('Models page entered HTMX history cache');
    }
    document.body.setAttribute('data-test-result', 'pass');
  })().catch(function(error) { fail(String(error && error.stack || error)); });
});
</script>`

	fixtureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/models":
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><script src="/htmx-2.0.4.min.js"></script><script>%s</script>%s</head><body data-test-result="pending"><main id="main-content"><div id="models-container" hx-history="false" data-secret="%s">Models</div></main></body></html>`, productionScript, runner, secret)
		case "/other":
			_, _ = w.Write([]byte(`<div id="other-page">Other</div>`))
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmx204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixtureServer.Close()

	runHeadlessChromeFixture(t, chrome, fixtureServer.URL+"/models", "Models HTMX history opt-out", 5000, 20*time.Second)
}

func TestSidebarHostedIdentityPayloadIsInertInChrome(t *testing.T) {
	chrome := testChromePath(t)

	var renderedSidebar bytes.Buffer
	if err := layout.Sidebar(nil, "").Render(context.Background(), &renderedSidebar); err != nil {
		t.Fatalf("render sidebar: %v", err)
	}
	rendered := renderedSidebar.String()
	marker := strings.Index(rendered, "fetch('/auth/me'")
	if marker < 0 {
		t.Fatal("rendered sidebar is missing hosted identity update script")
	}
	scriptStart := strings.LastIndex(rendered[:marker], "<script>")
	scriptEndOffset := strings.Index(rendered[marker:], "</script>")
	if scriptStart < 0 || scriptEndOffset < 0 {
		t.Fatal("could not isolate hosted identity update script")
	}
	productionScript := rendered[scriptStart+len("<script>") : marker+scriptEndOffset]

	payload := `</script><img src=x onerror="window.__identityPayloadExecuted=true"><script>window.__identityPayloadExecuted=true</script>javascript:alert(1);background:url(https://attacker.example/x)`
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	identityBody, err := json.Marshal(map[string]any{
		"authenticated": true,
		"auth_source":   "hosted_sso",
		"subject":       payload,
		"email":         payload,
		"username":      payload,
		"display":       payload,
	})
	if err != nil {
		t.Fatal(err)
	}

	fixture := `<!doctype html><html><body data-test-result="pending">
<div id="sidebar-auth-user" class="hidden"><span id="sidebar-auth-name">User</span><span id="sidebar-auth-avatar">U</span><button id="sidebar-logout-label">Logout</button></div>
<div id="sidebar-auth-user-collapsed" class="hidden"><span id="sidebar-auth-avatar-collapsed">U</span></div>
<script>window.__identityPayloadExecuted = false;</script>
<script>` + productionScript + `</script>
<script>
(function pollForIdentity() {
  var started = performance.now();
  function fail(message) {
    document.body.setAttribute('data-test-result', 'fail');
    document.body.setAttribute('data-test-error', message);
  }
  function poll() {
    var expected = ` + string(payloadJSON) + `;
    var name = document.getElementById('sidebar-auth-name');
    var avatar = document.getElementById('sidebar-auth-avatar');
    var collapsed = document.getElementById('sidebar-auth-avatar-collapsed');
    var logout = document.getElementById('sidebar-logout-label');
    if (name && name.textContent === expected && logout && logout.textContent === 'Log out of this workspace') {
      if (window.__identityPayloadExecuted) return fail('payload executed');
      if (name.children.length !== 0 || document.querySelector('img') || document.querySelector('script[src]')) return fail('payload created active DOM');
      for (var element of [name, avatar, collapsed, logout]) {
        for (var attribute of ['href', 'src', 'style', 'onclick', 'onerror']) {
          if (element.hasAttribute(attribute)) return fail('identity reached ' + attribute + ' sink');
        }
      }
      if (avatar.textContent !== '<' || collapsed.textContent !== '<') return fail('avatar text was not derived inertly');
      document.body.setAttribute('data-test-result', 'pass');
      return;
    }
    if (performance.now() - started > 2500) return fail('timed out waiting for identity update');
    setTimeout(poll, 10);
  }
  poll();
})();
</script></body></html>`

	fixtureServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(fixture))
		case "/auth/me":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = w.Write(identityBody)
		default:
			http.NotFound(w, r)
		}
	}))
	defer fixtureServer.Close()

	runHeadlessChromeFixture(t, chrome, fixtureServer.URL+"/", "hosted identity", 5000, 20*time.Second)
}

func runHeadlessChromeFixture(t *testing.T, chrome, targetURL, name string, virtualTimeBudget int, timeout time.Duration) {
	t.Helper()
	tempDir := t.TempDir()
	stdoutPath := filepath.Join(tempDir, "chrome-stdout.html")
	stderrPath := filepath.Join(tempDir, "chrome-stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout file: %v", err)
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		_ = stdoutFile.Close()
		t.Fatalf("create Chrome stderr file: %v", err)
	}

	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(tempDir, "chrome-profile"),
		fmt.Sprintf("--virtual-time-budget=%d", virtualTimeBudget),
		"--dump-dom",
		targetURL,
	)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	configureTestBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		t.Fatalf("start Chrome %s fixture: %v", name, err)
	}

	deadline := time.Now().Add(timeout)
	var result string
	for time.Now().Before(deadline) {
		if output, readErr := os.ReadFile(stdoutPath); readErr == nil {
			result = string(output)
			if strings.Contains(result, `data-test-result="pass"`) || strings.Contains(result, `data-test-result="fail"`) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	stopTestBrowserProcess(cmd)
	_ = stdoutFile.Close()
	_ = stderrFile.Close()

	if strings.Contains(result, `data-test-result="pass"`) {
		return
	}
	stderr, _ := os.ReadFile(stderrPath)
	if len(result) > 5000 {
		result = result[len(result)-5000:]
	}
	if len(stderr) > 5000 {
		stderr = stderr[len(stderr)-5000:]
	}
	t.Fatalf("real %s fixture failed:\nDOM tail:\n%s\nChrome stderr tail:\n%s", name, result, stderr)
}

func testChromePath(t *testing.T) string {
	t.Helper()
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chrome); err == nil {
		return chrome
	}
	for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	t.Skip("Chrome or Chromium is required for real HTMX history integration coverage")
	return ""
}

func historyFixtureDocument(productionScript, title, page string, cacheMiss, runTest bool) string {
	testScript := ""
	if runTest {
		testScript = `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) { throw new Error(message); }
  function currentPage() { return document.getElementById('history-page'); }
  function waitForPage(page, title, cacheMiss) {
    return new Promise(function(resolve, reject) {
      var started = performance.now();
      function poll() {
        var content = currentPage();
        var shell = document.getElementById('history-shell');
        if (window.location.pathname === '/' + page && content && content.getAttribute('data-page') === page &&
            document.title === title + ' - OpenVibely' && shell &&
            shell.getAttribute('data-history-cache-miss') === String(cacheMiss)) {
          resolve();
          return;
        }
        if (performance.now() - started > 2500) {
          reject(new Error('timed out waiting for ' + page + '; path=' + window.location.pathname +
            '; title=' + document.title + '; content=' + (content && content.getAttribute('data-page')) +
            '; cacheMiss=' + (shell && shell.getAttribute('data-history-cache-miss'))));
          return;
        }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function markFailure(error) {
    var result = currentPage() || document.body.appendChild(document.createElement('div'));
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }

  (async function() {
    if (!window.htmx || htmx.version !== '2.0.4') fail('expected real HTMX 2.0.4, got ' + (window.htmx && htmx.version));

    await window.openVibelyNavigate('/beta');
    await waitForPage('beta', 'Beta', false);
    if (!history.state || history.state.htmx !== true) fail('programmatic navigation did not create HTMX history state');
    var cache = JSON.parse(localStorage.getItem('htmx-history-cache') || '[]');
    if (!cache.some(function(item) { return item.url === '/alpha'; })) fail('HTMX did not cache the outgoing alpha page');

    history.back();
    await waitForPage('alpha', 'Alpha', false);
    history.forward();
    await waitForPage('beta', 'Beta', false);

    localStorage.removeItem('htmx-history-cache');
    history.back();
    await waitForPage('alpha', 'Alpha', true);

    history.forward();
    await waitForPage('beta', 'Beta', false);

    var result = currentPage();
    result.setAttribute('data-test-result', 'pass');
    result.setAttribute('data-htmx-version', htmx.version);
    result.setAttribute('data-final-title', document.title);
  })().catch(markFailure);
});
</script>`
	}

	return `<!doctype html><html lang="en"><head><meta charset="utf-8"><title>` + html.EscapeString(title) + ` - OpenVibely</title>` +
		`<script src="/htmx-2.0.4.min.js"></script><script>` + productionScript + `</script>` + testScript + `</head><body>` +
		`<div id="history-shell" data-history-cache-miss="` + fmt.Sprintf("%t", cacheMiss) + `"><main id="main-content">` +
		historyFixtureFragment(title, page) + `</main></div></body></html>`
}

func historyFixtureFragment(title, page string) string {
	documentTitle := title + " - OpenVibely"
	return `<div id="history-page" data-page="` + html.EscapeString(page) + `">` +
		`<span hidden aria-hidden="true" data-openvibely-page-title="` + html.EscapeString(documentTitle) + `"></span>` +
		`<h1>` + html.EscapeString(title) + `</h1></div>`
}
