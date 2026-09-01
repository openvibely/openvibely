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
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestScheduleContentTimelineFillsAvailableHeightInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)

	var content bytes.Buffer
	if err := ScheduleContent(&models.Project{ID: "project-1", Name: "Project 1"}, nil, 0, nil, nil).Render(context.Background(), &content); err != nil {
		t.Fatalf("render schedule content: %v", err)
	}
	renderedContent := content.String()
	for {
		scriptStart := strings.Index(renderedContent, "<script")
		if scriptStart < 0 {
			break
		}
		scriptEnd := strings.Index(renderedContent[scriptStart:], "</script>")
		if scriptEnd < 0 {
			t.Fatal("rendered schedule content has an unterminated script element")
		}
		scriptEnd += scriptStart + len("</script>")
		renderedContent = renderedContent[:scriptStart] + renderedContent[scriptEnd:]
	}

	page := `<!doctype html>
<html><head><meta name="viewport" content="width=device-width, initial-scale=1.0"><style>` + scheduleLayoutBrowserCSS() + `</style></head>
<body class="h-screen bg-base-200 overflow-hidden"><div class="drawer lg:drawer-open h-full"><div class="drawer-content flex flex-col h-full overflow-hidden"><main id="main-content" class="p-6 flex-1 overflow-y-auto overflow-x-hidden">` + renderedContent + `</main></div></div>
<script>
(function() {
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST', keepalive: true});
  }
  function fail(message) { throw new Error(message); }
  function assertDesktopLayout() {
    var main = document.getElementById('main-content');
    var timeline = document.getElementById('schedule-timeline-container');
    var wrapper = timeline && timeline.querySelector(':scope > div');
    var gridBody = wrapper && wrapper.querySelector(':scope > .relative');
    var rows = gridBody ? Array.from(gridBody.querySelectorAll(':scope > .schedule-grid-row')) : [];
    if (!main || !timeline || !wrapper || !gridBody || rows.length !== 24) fail('desktop: missing schedule height-chain elements');
    var timelineRect = timeline.getBoundingClientRect();
    var wrapperRect = wrapper.getBoundingClientRect();
    var bodyRect = gridBody.getBoundingClientRect();
    if (timeline.clientHeight < 500) fail('desktop: timeline is unexpectedly short at ' + timeline.clientHeight + 'px');
    if (main.scrollHeight > main.clientHeight + 2) fail('desktop: page-level vertical overflow escaped main content');
    if (main.scrollWidth > main.clientWidth + 2) fail('desktop: page-level horizontal overflow escaped main content');
    if (timeline.scrollHeight > timeline.clientHeight + 2) fail('desktop: timeline gained avoidable vertical overflow (scroll=' + timeline.scrollHeight + ', client=' + timeline.clientHeight + ', wrapper=' + wrapperRect.height + ', body=' + bodyRect.height + ', timelineRect=' + timelineRect.height + ')');
    if (Math.abs((timelineRect.bottom - 1) - bodyRect.bottom) > 2) fail('desktop: grid body leaves a bottom gap of ' + ((timelineRect.bottom - 1) - bodyRect.bottom) + 'px');
    if (Math.abs(wrapperRect.bottom - bodyRect.bottom) > 1) fail('desktop: grid body does not fill its width wrapper');
    if (Math.abs(timelineRect.left - 24) > 1) fail('desktop: main-content padding was not preserved');
    var heights = rows.map(function(row) { return row.getBoundingClientRect().height; });
    if (heights[0] <= 32) fail('desktop: hour rows did not grow beyond their 32px minimum');
    if (heights.some(function(height) { return Math.abs(height - heights[0]) > 1; })) fail('desktop: hour rows have inconsistent heights: ' + heights.map(function(height) { return height.toFixed(2); }).join(','));
  }
  function assertMobileLayout() {
    var main = document.getElementById('main-content');
    var timeline = document.getElementById('schedule-timeline-container');
    if (!main || !timeline) fail('mobile: missing shell elements');
    if (main.scrollHeight > main.clientHeight + 2) fail('mobile: page-level vertical overflow escaped main content');
    if (main.scrollWidth > main.clientWidth + 2) fail('mobile: page-level horizontal overflow escaped main content');
    if (timeline.scrollHeight <= timeline.clientHeight + 2) fail('mobile: vertical scrolling moved out of the timeline');
    if (timeline.scrollWidth < 800) fail('mobile: schedule width wrapper no longer preserves horizontal scrolling');
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  window.addEventListener('load', function() {
    requestAnimationFrame(function() {
      try {
        if (window.innerWidth <= 600) assertMobileLayout();
        else assertDesktopLayout();
        report('pass', '');
      } catch (error) {
        report('fail', String(error && error.stack || error));
      }
    });
  });
})();
</script></body></html>`

	cases := []struct {
		name   string
		width  int
		height int
	}{
		{name: "desktop", width: 1440, height: 1400},
		{name: "mobile", width: 390, height: 520},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			browserResult := make(chan string, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/browser-result" {
					browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(page))
			}))
			defer server.Close()

			stderrPath := filepath.Join(t.TempDir(), "schedule-layout-browser.stderr")
			stderrFile, err := os.Create(stderrPath)
			if err != nil {
				t.Fatal(err)
			}
			defer stderrFile.Close()

			cmd := exec.Command(chrome,
				"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
				"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
				"--no-first-run", "--no-default-browser-check", fmt.Sprintf("--window-size=%d,%d", tc.width, tc.height),
				"--user-data-dir="+filepath.Join(t.TempDir(), "schedule-layout-browser-profile"),
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
				outcome = "fail: timed out waiting for browser result"
			}
			stopBrowserProcess(cmd)
			if !strings.HasPrefix(outcome, "pass:") {
				stderr, _ := os.ReadFile(stderrPath)
				t.Fatalf("schedule %s layout browser regression failed: %s\nChrome:\n%s", tc.name, outcome, strings.TrimSpace(string(stderr)))
			}
		})
	}
}

func scheduleLayoutBrowserCSS() string {
	return `
html, body { margin: 0; height: 100%; width: 100%; overflow: hidden; font-family: system-ui, sans-serif; }
*, ::before, ::after { box-sizing: border-box; }
.h-screen { height: 100vh; }
.h-full { height: 100%; }
.w-full { width: 100%; }
.min-h-0 { min-height: 0; }
.min-w-0 { min-width: 0; }
[class~="min-w-[800px]"] { min-width: 800px; }
.flex { display: flex; }
.flex-col { flex-direction: column; }
.flex-1 { flex: 1 1 0%; }
.flex-shrink-0 { flex-shrink: 0; }
.items-center { align-items: center; }
.justify-between { justify-content: space-between; }
.gap-2 { gap: 0.5rem; }
.gap-3 { gap: 0.75rem; }
.p-6 { padding: 1.5rem; }
.p-2 { padding: 0.5rem; }
.p-0 { padding: 0; }
.p-0\.5 { padding: 0.125rem; }
.px-1 { padding-left: 0.25rem; padding-right: 0.25rem; }
.px-2 { padding-left: 0.5rem; padding-right: 0.5rem; }
.px-1\.5 { padding-left: 0.375rem; padding-right: 0.375rem; }
.py-1 { padding-top: 0.25rem; padding-bottom: 0.25rem; }
.pb-2 { padding-bottom: 0.5rem; }
.mb-3 { margin-bottom: 0.75rem; }
.mt-2 { margin-top: 0.5rem; }
.flex-shrink-0 { flex-shrink: 0; }
.grid { display: grid; }
.relative { position: relative; }
.absolute { position: absolute; }
.hidden { display: none; }
.overflow-hidden { overflow: hidden; }
.overflow-x-hidden { overflow-x: hidden; }
.overflow-x-auto { overflow-x: auto; }
.overflow-y-auto { overflow-y: auto; }
[class~="min-h-[32px]"] { min-height: 32px; }
[class~="text-[10px]"] { font-size: 0.625rem; line-height: 1rem; }
.text-right { text-align: right; }
.opacity-0 { opacity: 0; }
.rounded-lg { border-radius: 0.5rem; }
.bg-base-100 { background: #fff; }
.bg-base-200 { background: #f3f3f3; }
.btn { min-height: 2rem; }
.btn-xs { min-height: 1.5rem; }
.text-xs { font-size: 0.75rem; line-height: 1rem; }
.text-sm { font-size: 0.875rem; line-height: 1.25rem; }
.text-2xl { font-size: 1.5rem; line-height: 2rem; }
.font-bold { font-weight: 700; }
.font-semibold { font-weight: 600; }
`
}
