package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkerSettingsContentUsesNonShrinkingScrollableTableLayout(t *testing.T) {
	projectStats := makeWorkerStats(24)
	modelStats := []ModelWorkerStats{{ID: "model-1", Name: "Reference Model", Model: "gpt-example", Running: 1, MaxWorkers: 3}}

	var buf bytes.Buffer
	if err := WorkerSettingsContent(0, 0, 0, 0, projectStats, modelStats).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render worker settings content: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`id="worker-settings-content" class="w-full min-w-0 max-w-full flex-none pb-6"`,
		`class="card bg-base-100 shadow-sm border border-base-300 mb-6 w-full min-w-0 max-w-full flex-none h-auto"`,
		`class="card bg-base-100 shadow-sm border border-base-300 w-full min-w-0 max-w-full flex-none h-auto"`,
		`class="worker-table-scroll w-full min-w-0 max-w-full overflow-x-auto"`,
		`class="table table-sm worker-stats-table w-full min-w-max h-auto"`,
		`#worker-settings-content .worker-table-scroll`,
		`max-height: none;`,
		`#worker-settings-content .worker-stats-table :where(th, td)`,
		`padding-top: 0.625rem;`,
		`white-space: nowrap;`,
		`min-height: 2.75rem;`,
		`class="flex flex-nowrap items-center gap-2 worker-limit-form"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("worker table layout contract missing %q", required)
		}
	}

	for _, forbidden := range []string{
		`class="overflow-x-auto"`,
		`class="table table-sm">`,
		`outline: 2px solid hsl(var(--p));`,
	} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("worker table layout retained shrinking-prone markup %q", forbidden)
		}
	}

	if got := strings.Count(html, `id="project-row-project-`); got != len(projectStats) {
		t.Fatalf("rendered %d project rows, want %d", got, len(projectStats))
	}
}

func TestWorkerSettingsContentSupportsHighLimitsAndReportsStaleProjectCaps(t *testing.T) {
	overGlobal := 50
	equalGlobal := 25
	projectStats := []ProjectWorkerStats{
		{ID: "project-over", Name: "Project Over Global", MaxWorkers: &overGlobal},
		{ID: "project-equal", Name: "Project At Global", MaxWorkers: &equalGlobal},
		{ID: "project-inherited", Name: "Project Inherited", MaxWorkers: nil},
	}

	var buf bytes.Buffer
	if err := WorkerSettingsContent(25, 2, 2, 0, projectStats, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render worker settings content: %v", err)
	}
	html := buf.String()

	for _, required := range []string{
		`id="limit-input-global"`,
		`value="25"`,
		`id="limit-input-project-over"`,
		`value="50"`,
		`max="25"`,
		"Exceeds global limit; lower this cap",
		"Exceeds global",
		"positive values must not exceed the global worker limit",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("high-limit UI contract missing %q", required)
		}
	}
	if strings.Contains(html, `max="10"`) {
		t.Fatal("worker settings UI retained the removed hard maximum of 10")
	}
}

func TestWorkerSettingsManyRowsScrollInMainContentAfterHTMXRefreshInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var mu sync.Mutex
	rowCount := 28
	renderContent := func() string {
		mu.Lock()
		stats := makeWorkerStats(rowCount)
		mu.Unlock()
		var buf bytes.Buffer
		if err := WorkerSettingsContent(25, 0, 0, 0, stats, nil).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render worker settings content: %v", err)
		}
		return buf.String()
	}
	renderTBody := func() string {
		mu.Lock()
		stats := makeWorkerStats(rowCount)
		mu.Unlock()
		var buf bytes.Buffer
		if err := ProjectStatsTableBody(25, 0, 0, 0, stats).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render worker stats table body: %v", err)
		}
		return buf.String()
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > 6000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 20);
      }
      poll();
    });
  }
  function assertWorkerTableLayout(label, previousRowHeight) {
    var main = document.getElementById('main-content');
    var root = document.getElementById('worker-settings-content');
    var wrapper = document.querySelector('.worker-table-scroll');
    var table = document.querySelector('.worker-stats-table');
    var row = document.querySelector('#project-stats-tbody tr[id^="project-row-"]');
    if (!main || !root || !wrapper || !table || !row) fail(label + ': missing worker layout elements');
    var rowHeight = row.getBoundingClientRect().height;
    if (rowHeight < 40) fail(label + ': project row compressed to ' + rowHeight + 'px');
    if (previousRowHeight && rowHeight < previousRowHeight - 1) fail(label + ': row height shrank from ' + previousRowHeight + 'px to ' + rowHeight + 'px');
    if (main.scrollHeight <= main.clientHeight + 80) fail(label + ': main content did not become vertically scrollable');
    if (wrapper.scrollHeight > wrapper.clientHeight + 2) fail(label + ': table wrapper became a vertical scroll region instead of growing page content');
    if (main.scrollWidth > main.clientWidth + 2) fail(label + ': page-level horizontal overflow escaped main content');
    if (getComputedStyle(root).flexShrink !== '0') fail(label + ': worker root is allowed to flex-shrink');
    if (getComputedStyle(table).minWidth === '0px') fail(label + ': table can collapse instead of using inner horizontal scrolling');
    return rowHeight;
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    await waitFor(function() { return window.htmx && document.getElementById('project-stats-tbody'); }, 'Workers hydration');
    htmx.process(document.body);
    var globalInput = document.getElementById('limit-input-global');
    var firstProjectInput = document.getElementById('limit-input-project-00');
    if (!globalInput || globalInput.value !== '25' || globalInput.getAttribute('max') !== null) fail('global high worker limit input contract is incorrect');
    if (!firstProjectInput || firstProjectInput.value !== '25' || firstProjectInput.getAttribute('max') !== '25') fail('project worker limit input did not inherit the finite global ceiling');
    var initialHeight = assertWorkerTableLayout('initial many-row render', 0);
    var beforeScrollHeight = document.getElementById('main-content').scrollHeight;
    await fetch('/browser-expand', {method:'POST'});
    await htmx.ajax('GET', '/workers/stats/projects', {target:'#project-stats-tbody', swap:'outerHTML'});
    await waitFor(function() { return document.querySelectorAll('#project-stats-tbody tr[id^="project-row-"]').length >= 56; }, 'expanded worker rows');
    var refreshedHeight = assertWorkerTableLayout('after HTMX row expansion', initialHeight);
    var afterScrollHeight = document.getElementById('main-content').scrollHeight;
    if (afterScrollHeight <= beforeScrollHeight + refreshedHeight * 20) fail('main scroll height did not grow with added rows');
    document.getElementById('main-content').scrollTop = afterScrollHeight;
    await new Promise(function(resolve) { requestAnimationFrame(resolve); });
    if (document.getElementById('main-content').scrollTop <= 0) fail('main content cannot scroll to later worker rows');
    await report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	page := `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1.0"><style>` + workerSettingsBrowserCSS() + `</style><script src="/htmx-2.0.4.min.js"></script>` + runner + `</head><body class="h-screen bg-base-200 overflow-hidden"><div class="drawer lg:drawer-open h-full"><div class="drawer-content flex flex-col h-full overflow-hidden"><main id="main-content" class="p-6 flex-1 overflow-y-auto overflow-x-hidden">` + renderContent() + `</main></div></div></body></html>`

	browserResult := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/workers/stats/projects":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(renderTBody()))
		case r.URL.Path == "/browser-expand" && r.Method == http.MethodPost:
			mu.Lock()
			rowCount = 56
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "workers-layout-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=390,520",
		"--user-data-dir="+filepath.Join(t.TempDir(), "workers-layout-browser-profile"),
		server.URL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(20 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Workers layout browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}

func makeWorkerStats(count int) []ProjectWorkerStats {
	stats := make([]ProjectWorkerStats, count)
	for i := range stats {
		limit := (i % 5) + 1
		if i == 0 {
			limit = 25
		}
		stats[i] = ProjectWorkerStats{
			ID:         fmt.Sprintf("project-%02d", i),
			Name:       fmt.Sprintf("Project %02d With A Descriptive Name", i),
			Running:    i % 3,
			QueueSize:  i % 7,
			MaxWorkers: &limit,
		}
	}
	return stats
}

func workerSettingsBrowserCSS() string {
	return `
html, body { margin: 0; height: 100vh; width: 100vw; overflow: hidden; font-family: system-ui, sans-serif; }
*, ::before, ::after { box-sizing: border-box; }
.h-screen { height: 100vh; }
.h-full { height: 100%; }
.h-auto { height: auto; }
.w-full { width: 100%; }
.w-20 { width: 5rem; }
.min-w-0 { min-width: 0; }
.min-w-max { min-width: max-content; }
.max-w-full { max-width: 100%; }
.flex { display: flex; }
.flex-col { flex-direction: column; }
.flex-none { flex: none; }
.flex-1 { flex: 1 1 0%; }
.flex-nowrap { flex-wrap: nowrap; }
.items-center { align-items: center; }
.gap-2 { gap: 0.5rem; }
.space-y-6 > :not([hidden]) ~ :not([hidden]) { margin-top: 1.5rem; }
.pb-6 { padding-bottom: 1.5rem; }
.p-6 { padding: 1.5rem; }
.mb-4 { margin-bottom: 1rem; }
.mt-1 { margin-top: 0.25rem; }
.block { display: block; }
.hidden { display: none; }
.overflow-hidden { overflow: hidden; }
.overflow-x-hidden { overflow-x: hidden; }
.overflow-x-auto { overflow-x: auto; }
.overflow-y-auto { overflow-y: auto; }
.drawer, .drawer-content { height: 100%; }
.card { border: 1px solid #d0d0d0; border-radius: 0.75rem; background: white; }
.card-body { display: flex; flex-direction: column; padding: 1rem; }
.card-title { margin: 0; }
.text-2xl { font-size: 1.5rem; line-height: 2rem; }
.text-lg { font-size: 1.125rem; line-height: 1.75rem; }
.text-sm { font-size: 0.875rem; line-height: 1.25rem; }
.text-xs { font-size: 0.75rem; line-height: 1rem; }
.font-bold { font-weight: 700; }
.font-medium { font-weight: 500; }
.opacity-60 { opacity: 0.6; }
.table { border-collapse: collapse; width: 100%; }
.table.table-sm :where(th, td) { padding: 0.25rem 0.5rem; }
.table :where(th, td) { border-bottom: 1px solid #dedede; text-align: left; }
.badge { display: inline-flex; align-items: center; border: 1px solid #aaa; border-radius: 9999px; padding: 0.125rem 0.5rem; line-height: 1.25; white-space: nowrap; }
.input { border: 1px solid #aaa; border-radius: 0.375rem; padding: 0 0.5rem; }
.input-xs { min-height: 1.5rem; height: 1.5rem; }
.btn { border: 1px solid #aaa; border-radius: 0.375rem; padding: 0 0.5rem; background: #eee; }
.btn-xs { min-height: 1.5rem; height: 1.5rem; }
`
}
