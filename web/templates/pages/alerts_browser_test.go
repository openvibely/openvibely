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

	"github.com/openvibely/openvibely/internal/models"
)

func TestAlertsSingleDeletePreservesViewportInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var mu sync.Mutex
	alerts := make([]models.Alert, 30)
	for i := range alerts {
		message := strings.Repeat("Long alert content ", 5)
		if i == 2 || i == 5 || i == 8 || i == 11 || i == 14 || i == 17 || i == 20 || i == 24 || i == 27 {
			message += " filtered-focus"
		}
		alerts[i] = models.Alert{
			ID:        fmt.Sprintf("item-%02d", i),
			ProjectID: "project-alerts-browser",
			Title:     fmt.Sprintf("Notification %02d", i),
			Message:   message,
			IsRead:    i%3 == 0,
		}
		if i%2 == 1 {
			alerts[i].DecisionState = models.AlertDecisionPending
		} else {
			alerts[i].DecisionState = models.AlertDecisionNotRequired
		}
	}
	renderAlerts := func() string {
		mu.Lock()
		defer mu.Unlock()
		unread := 0
		for _, alert := range alerts {
			if !alert.IsRead {
				unread++
			}
		}
		var out bytes.Buffer
		if err := AlertsContent(append([]models.Alert(nil), alerts...), "project-alerts-browser", unread).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Alerts content: %v", err)
		}
		return out.String()
	}
	renderAlertsPage := func() string {
		mu.Lock()
		defer mu.Unlock()
		unread := 0
		for _, alert := range alerts {
			if !alert.IsRead {
				unread++
			}
		}
		var out bytes.Buffer
		if err := Alerts(nil, "project-alerts-browser", append([]models.Alert(nil), alerts...), unread).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Alerts page: %v", err)
		}
		return out.String()
	}
	deleteAlert := func(id string) {
		mu.Lock()
		defer mu.Unlock()
		for i := range alerts {
			if alerts[i].ID == id {
				alerts = append(alerts[:i], alerts[i+1:]...)
				return
			}
		}
	}

	runner := `<script>
	window.handleDropdownToggle = function(event) { event.stopPropagation(); };
	window.addEventListener('DOMContentLoaded', function() {
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
	  function fail(message) { throw new Error(message); }
	  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
	  function waitFor(check, label) {
	    var started = performance.now();
	    return new Promise(function(resolve, reject) {
	      function poll() {
	        try { if (check()) return resolve(); } catch (error) { return reject(error); }
	        if (performance.now() - started > 5000) return reject(new Error('timed out waiting for ' + label));
	        setTimeout(poll, 10);
	      }
	      poll();
	    });
	  }
	  function row(id) { return document.querySelector('[data-alert-scroll-anchor="' + id + '"]'); }
	  function remove(id) {
	    var button = row(id) && row(id).querySelector('[data-alert-delete]');
	    if (!button) fail('missing delete control for ' + id);
	    button.focus({preventScroll:true});
	    button.click();
	    return waitFor(function() { return !row(id); }, id + ' deletion');
	  }
	  function assertNear(actual, expected, label) {
	    if (Math.abs(actual - expected) > 3) fail(label + ' moved by ' + (actual - expected) + 'px');
	  }
	  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
	  (async function() {
	    await waitFor(function() { return window.htmx && row('item-15'); }, 'Alerts hydration');
	    htmx.process(document.body);
	    var root = document.getElementById('alerts-container');
	    root.scrollTop = row('item-15').offsetTop - root.offsetTop - 70;
	    await wait(50);
	    var stableTop = row('item-14').getBoundingClientRect().top;
	    await remove('item-15');
	    await wait(250);
	    root = document.getElementById('alerts-container');
	    assertNear(row('item-14').getBoundingClientRect().top, stableTop, 'actionable notification nearest surviving anchor');
	    if (document.activeElement !== row('item-16').querySelector('[data-alert-delete]')) fail('focus did not move to the next delete control');
	    if (root.scrollTop < 100) fail('single delete reset the Alerts scrollport to the top');
	    if (!document.getElementById('alert-badge').textContent.trim()) fail('unread badge was not authoritatively refreshed');

	    var repeatedTop = row('item-14').getBoundingClientRect().top;
	    await remove('item-16');
	    await wait(250);
	    assertNear(row('item-14').getBoundingClientRect().top, repeatedTop, 'repeated delete nearest surviving anchor');
	    if (document.activeElement !== row('item-17').querySelector('[data-alert-delete]')) fail('repeated delete focus was not predictable');

	    var visibleAnchor = row('item-17').getBoundingClientRect().top;
	    await remove('item-01');
	    await wait(250);
	    assertNear(row('item-17').getBoundingClientRect().top, visibleAnchor, 'deletion above viewport');
	    var beforeBelow = row('item-17').getBoundingClientRect().top;
	    await remove('item-29');
	    await wait(250);
	    assertNear(row('item-17').getBoundingClientRect().top, beforeBelow, 'deletion below viewport');
	    if (document.getElementById('alerts-container').scrollTop < 100) fail('off-viewport deletion reset the scrollport');

	    root = document.getElementById('alerts-container');
	    root.scrollTop = root.scrollHeight;
	    await wait(50);
	    await remove('item-28');
	    await wait(250);
	    root = document.getElementById('alerts-container');
	    if (Math.abs((root.scrollHeight - root.clientHeight) - root.scrollTop) > 3) fail('deleting the last visible item did not preserve the end-of-list anchor');
	    if (document.activeElement !== row('item-27').querySelector('[data-alert-delete]')) fail('deleting the last visible item did not focus the previous delete control');
	    if (document.getElementById('alerts-container').scrollTop < 100) fail('deleting the last visible item reset the scrollport');

	    var search = document.querySelector('input[data-card-search="alerts"]');
	    search.value = 'filtered-focus';
	    search.dispatchEvent(new Event('input', {bubbles:true}));
	    await wait(50);
	    root = document.getElementById('alerts-container');
	    root.scrollTop = row('item-11').offsetTop - root.offsetTop - 70;
	    await wait(50);
	    if (getComputedStyle(row('item-12')).display !== 'none') fail('card search did not hide the adjacent non-matching row');
	    if (getComputedStyle(row('item-14')).display === 'none') fail('card search hid the expected focus fallback row');
	    var filteredTop = row('item-08').getBoundingClientRect().top;
	    await remove('item-11');
	    await wait(250);
	    assertNear(row('item-08').getBoundingClientRect().top, filteredTop, 'filtered delete nearest surviving visible anchor');
	    if (document.activeElement !== row('item-14').querySelector('[data-alert-delete]')) fail('filtered delete did not focus the next visible delete control');
	    if (getComputedStyle(row('item-12')).display !== 'none') fail('persisted card search was not reapplied after deletion');
	    await report('pass', '');
	  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
	});
	</script>`
	style := `<style>
	body { margin: 0; }
	#alerts-container { box-sizing: border-box; height: 420px !important; overflow-y: auto !important; padding: 12px; }
	[data-alert-scroll-anchor] { box-sizing: border-box; min-height: 132px; margin-bottom: 16px; }
	</style>`

	browserResult := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/alerts" && r.Method == http.MethodGet:
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(renderAlerts()))
				return
			}
			page := renderAlertsPage()
			page = strings.Replace(page, "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", style+runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case strings.HasPrefix(r.URL.Path, "/alerts/item-") && r.Method == http.MethodDelete:
			deleteAlert(strings.TrimPrefix(r.URL.Path, "/alerts/"))
			w.Header().Set("HX-Trigger", "alertUpdate")
			_, _ = w.Write([]byte(renderAlerts()))
		case r.URL.Path == "/alerts/unread-count":
			mu.Lock()
			unread := 0
			for _, alert := range alerts {
				if !alert.IsRead {
					unread++
				}
			}
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `<span>%d</span>`, unread)
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "alerts-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "alerts-browser-profile"),
		server.URL+"/alerts?project_id=project-alerts-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(15 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Alerts browser regression failed: %s\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
