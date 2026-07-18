package components

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
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

	stdoutPath := filepath.Join(t.TempDir(), "chrome-stdout.html")
	stderrPath := filepath.Join(t.TempDir(), "chrome-stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout file: %v", err)
	}
	defer stdoutFile.Close()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr file: %v", err)
	}
	defer stderrFile.Close()

	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chrome-profile"),
		"--virtual-time-budget=10000",
		"--dump-dom",
		fixtureServer.URL+"/alpha",
	)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	configureTestBrowserProcess(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome history fixture: %v", err)
	}

	deadline := time.Now().Add(25 * time.Second)
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

	if !strings.Contains(result, `data-test-result="pass"`) {
		stderr, _ := os.ReadFile(stderrPath)
		if len(result) > 5000 {
			result = result[len(result)-5000:]
		}
		if len(stderr) > 5000 {
			stderr = stderr[len(stderr)-5000:]
		}
		t.Fatalf("real HTMX history fixture failed:\nDOM tail:\n%s\nChrome stderr tail:\n%s", result, stderr)
	}
	if got := ordinaryBetaRequests.Load(); got != 1 {
		t.Fatalf("ordinary programmatic navigation requests to beta = %d, want 1; cache-hit Back/Forward unexpectedly used the server", got)
	}
	if got := historyMissRequests.Load(); got != 1 {
		t.Fatalf("HTMX history cache-miss requests = %d, want 1", got)
	}
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
