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
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestAutomationGraphThemeAndHistoryNavigationInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}
	var base bytes.Buffer
	if err := layout.Base("Automation navigation fixture", nil, "project-browser").Render(context.Background(), &base); err != nil {
		t.Fatalf("render production navigation lifecycle: %v", err)
	}
	baseHTML := base.String()
	navStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	navEnd := strings.Index(baseHTML[navStart:], "// Scroll position restoration for drop zones")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("could not isolate production HTMX navigation lifecycle")
	}
	navigationScript := baseHTML[navStart : navStart+navEnd]

	projectID := "project-browser"
	cards := []models.AutomationCard{
		{Automation: models.Automation{ID: "automation-a", Name: "Automation A", Description: "First", LifecycleState: models.AutomationActive}, Version: models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"}},
		{Automation: models.Automation{ID: "automation-b", Name: "Automation B", Description: "Second", LifecycleState: models.AutomationActive}, Version: models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"}},
	}
	renderPortfolio := func() string {
		var out bytes.Buffer
		if err := AutomationsContent(cards, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation portfolio: %v", err)
		}
		return out.String()
	}
	renderLive := func(id, name string) string {
		node := models.AutomationNode{ID: id + "-node", Name: "Readable node", NodeType: models.AutomationNodeAgentTask, PositionX: 20, PositionY: 20}
		graph := models.AutomationLiveGraph{
			Automation:   models.Automation{ID: id, Name: name, Description: "Theme and navigation fixture", LifecycleState: models.AutomationActive},
			Version:      models.AutomationVersion{Version: 1, AdapterKey: "native_sdlc"},
			Nodes:        []models.AutomationLiveNode{{AutomationNode: node, DisplayState: "idle"}},
			RecentCutoff: time.Unix(1, 0),
		}
		var out bytes.Buffer
		if err := AutomationLiveContent(graph, projectID).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Automation live graph: %v", err)
		}
		return out.String()
	}

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > 5000) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function click(selector, label) {
    var element = document.querySelector(selector);
    if (!element) fail('missing ' + label);
    element.click();
  }
  function liveID() { var root = document.getElementById('automation-live'); return root && root.dataset.automationId; }
  function portfolioReady() { return !!document.getElementById('automations-container'); }
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}); }
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message || 'window error')); });
  (async function() {
    await report('progress', 'runner-started');
    await waitFor(function() { return window.htmx && document.body; }, 'HTMX initialization');
    htmx.process(document.body);
    await wait(50);
    await window.openVibelyNavigate('/automations/automation-a?project_id=project-browser');
    await report('progress', 'automation-a-clicked');
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Automation A');
    await report('progress', 'automation-a-loaded');
    var node = document.querySelector('.automation-graph-node');
    var label = document.querySelector('.automation-graph-label--primary');
    var nodeFill = getComputedStyle(node).fill;
    var labelFill = getComputedStyle(label).fill;
    if (nodeFill === 'rgb(0, 0, 0)' || nodeFill === 'rgba(0, 0, 0, 0)') fail('node fill fell back to black: ' + nodeFill);
    if (labelFill === 'rgb(0, 0, 0)') fail('label fill fell back to black: ' + labelFill);

    history.back();
    await waitFor(portfolioReady, 'portfolio after browser Back');
    await report('progress', 'browser-back-restored');
    click('a[href^="/automations/automation-b?"]', 'Automation B card after browser Back');
    await waitFor(function() { return liveID() === 'automation-b'; }, 'Automation B after browser Back');
    await report('progress', 'automation-b-clicked');

    click('#automation-live > a[href^="/automations?"]', 'in-page Automations back link');
    await report('progress', 'in-page-back-clicked');
    await waitFor(portfolioReady, 'portfolio after in-page back link');
    await report('progress', 'in-page-back-restored');
    await wait(100);
    click('a[href^="/automations/automation-a?"]', 'Automation A card after in-page back');
    await report('progress', 'automation-a-reclicked');
    await waitFor(function() { return liveID() === 'automation-a'; }, 'Automation A after in-page back');
    await report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	style := `<style>
:root { --bc: 220 14% 90%; --b2: 220 16% 16%; --p: 235 85% 68%; --er: 0 75% 60%; --wa: 40 90% 60%; --in: 200 90% 60%; --su: 145 65% 48%; }
.hidden { display: none !important; }
</style>`

	browserResult := make(chan string, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Header.Get("HX-Request") == "true" {
			select {
			case browserResult <- "progress:server-" + r.URL.Path:
			default:
			}
		}
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/automations":
			fragment := renderPortfolio()
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(fragment))
				return
			}
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><script src="/htmx-2.0.4.min.js"></script><script>%s</script>%s%s</head><body><main id="main-content" hx-history-elt>%s</main></body></html>`, navigationScript, style, runner, fragment)
		case "/automations/automation-a":
			_, _ = w.Write([]byte(renderLive("automation-a", "Automation A")))
		case "/automations/automation-b":
			_, _ = w.Write([]byte(renderLive("automation-b", "Automation B")))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "automation-navigation.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "automation-navigation-profile"),
		server.URL+"/automations?project_id="+projectID,
	)
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}

	var outcome string
	lastProgress := "none"
	deadline := time.After(20 * time.Second)
	for outcome == "" {
		select {
		case result := <-browserResult:
			if strings.HasPrefix(result, "progress:") {
				lastProgress = strings.TrimPrefix(result, "progress:")
				continue
			}
			outcome = result
		case <-deadline:
			outcome = "fail:timed out waiting for browser result; last progress=" + lastProgress
		}
	}
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait()
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 4000 {
			stderr = stderr[len(stderr)-4000:]
		}
		t.Fatalf("Automation browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
