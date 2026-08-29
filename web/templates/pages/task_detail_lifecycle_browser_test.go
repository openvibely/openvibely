package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskDetailLifecyclePaginationInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)

	task := &models.Task{
		ID:        "task-lifecycle-browser",
		ProjectID: "project-lifecycle-browser",
		Title:     "Lifecycle pagination browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}

	rowAt := func(id string, startedAt time.Time, skill string) map[string]any {
		return map[string]any{
			"id":                id,
			"when":              "after_complete",
			"skill_key":         skill,
			"status":            "completed",
			"output_contract":   "activity_summary",
			"summary":           "summary for " + id,
			"started_at":        startedAt.Format(time.RFC3339),
			"selected_skills":   []string{"debug_go_tests", "review_changes"},
			"selected_memories": []map[string]string{{"file": "testing_coverage_and_performance.md"}},
		}
	}
	row := func(id string, hour int, skill string) map[string]any {
		return rowAt(id, time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC), skill)
	}
	writePage := func(w http.ResponseWriter, items []map[string]any, hasMore bool, cursor string) {
		if items == nil {
			items = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"items":       items,
			"has_more":    hasMore,
			"next_cursor": cursor,
		})
	}

	var mu sync.Mutex
	initialCalls := 0
	olderCalls := 0
	newerCalls := 0
	newerAfterCalls := []string{}
	sawProject := false
	sawBoundedLimit := false

	page := ""
	browserResult := make(chan string, 8)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-browser/lifecycle-executions":
			// Continue below with the deterministic pagination fixture.
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawBoundedLimit = true
		}
		before := query.Get("before")
		after := query.Get("after")
		if before != "" {
			olderCalls++
		}
		if after != "" {
			newerCalls++
			newerAfterCalls = append(newerAfterCalls, after)
		}
		if before == "" && after == "" {
			initialCalls++
		}
		currentInitialCall := initialCalls
		currentNewerCall := newerCalls
		mu.Unlock()
		if before != "" {
			// Delaying the page makes two rapid loader clicks exercise the
			// browser's single-flight guard instead of racing a fast response.
			time.Sleep(100 * time.Millisecond)
			switch before {
			case "cursor-initial", "cursor-fresh", "cursor-empty":
				writePage(w, []map[string]any{row("event-0", 0, "hook-0"), row("event-minus-1", -1, "hook-minus-1")}, true, "cursor-final")
			case "cursor-final":
				writePage(w, []map[string]any{row("event-minus-2", -2, "hook-minus-2")}, false, "")
			default:
				http.Error(w, "unknown older cursor", http.StatusBadRequest)
			}
			return
		}
		if after != "" {
			if after == "event-4" {
				if currentNewerCall == 1 {
					// The first newer request intentionally misses a row that is
					// announced while the request is still in flight.
					time.Sleep(300 * time.Millisecond)
					writePage(w, []map[string]any{}, false, "")
				} else {
					writePage(w, []map[string]any{row("event-5", 5, "live-hook")}, false, "")
				}
			} else {
				writePage(w, nil, false, "")
			}
			return
		}
		switch currentInitialCall {
		case 1:
			http.Error(w, "fixture initial lifecycle failure", http.StatusServiceUnavailable)
		case 2:
			writePage(w, []map[string]any{
				row("event-4", 4, "hook-4"), row("event-3", 3, "hook-3"), row("event-2", 2, "hook-2"),
				row("event-1", 1, "hook-1"), row("event-0", 0, "hook-0"),
			}, true, "cursor-initial")
		case 3:
			writePage(w, []map[string]any{
				row("event-5", 5, "live-hook"), row("event-4", 4, "hook-4"), row("event-3", 3, "hook-3"),
				row("event-2", 2, "hook-2"), row("event-1", 1, "hook-1"),
			}, true, "cursor-fresh")
		case 4:
			// This response must arrive after the fifth refresh response and
			// must never insert event-stale into the DOM.
			time.Sleep(250 * time.Millisecond)
			writePage(w, []map[string]any{row("event-stale", 6, "stale-hook"), row("event-5", 5, "live-hook")}, true, "cursor-fresh")
		case 5:
			writePage(w, []map[string]any{
				row("event-fresh", 7, "fresh-hook"), row("event-5", 5, "live-hook"),
				rowAt("event-4", time.Date(2026, time.January, 1, 5, 0, 0, 0, time.UTC), "hook-4"),
			}, true, "cursor-fresh")
		case 6:
			writePage(w, nil, false, "")
		default:
			writePage(w, []map[string]any{row("event-empty-live", 8, "empty-live-hook")}, true, "cursor-empty")
		}
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function list() { return document.getElementById('lifecycle-activity-list'); }
  function port() { return document.getElementById('lifecycle-activity-scroll'); }
  function ids() {
    return Array.prototype.map.call(list().querySelectorAll('[data-lifecycle-execution-id]'), function(row) { return row.getAttribute('data-lifecycle-execution-id'); });
  }
  function assertUnique(values, label) {
    var seen = Object.create(null);
    values.forEach(function(value) {
      if (seen[value]) fail(label + ' duplicated ' + value + ': ' + values.join(','));
      seen[value] = true;
    });
  }
  async function run() {
    await waitFor(function() { return list() && list().querySelector('[data-lifecycle-error]'); }, 'initial error state');
    var retry = list().querySelector('[data-lifecycle-retry="refresh"]');
    if (!retry) fail('initial lifecycle error did not expose retry');
    retry.click();
    await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-4"]'); }, 'initial lifecycle page');
    if (!list().querySelector('[data-lifecycle-execution-id="event-3"]')) fail('initial lifecycle rows missing');
    if (list().textContent.indexOf('Selected skills') < 0 || list().textContent.indexOf('testing_coverage_and_performance.md') < 0) fail('selected lifecycle evidence was not rendered');

    var lifecyclePort = port();
    lifecyclePort.scrollTop = 50;
    lifecyclePort.dispatchEvent(new Event('scroll', {bubbles:true}));
    await wait(20);
    var anchor = list().querySelector('[data-lifecycle-execution-id="event-4"]');
    var anchorBeforeLive = anchor.getBoundingClientRect().top;
			window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-browser', project_id:'project-lifecycle-browser'}}));
			await wait(240);
			window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-browser', project_id:'project-lifecycle-browser'}}));
			await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-5"]'); }, 'newer live insert after pending retry');    var anchorAfterLive = list().querySelector('[data-lifecycle-execution-id="event-4"]').getBoundingClientRect().top;
    if (Math.abs(anchorAfterLive - anchorBeforeLive) > 2) fail('newer live insert moved the reading anchor: before=' + anchorBeforeLive + ' after=' + anchorAfterLive);

    var anchorBeforeRefresh = list().querySelector('[data-lifecycle-execution-id="event-4"]').getBoundingClientRect().top;
    await window.refreshLifecycleActivity('task-lifecycle-browser', 'project-lifecycle-browser');
    var anchorAfterRefresh = list().querySelector('[data-lifecycle-execution-id="event-4"]').getBoundingClientRect().top;
    if (Math.abs(anchorAfterRefresh - anchorBeforeRefresh) > 2) fail('refresh moved the reading anchor: before=' + anchorBeforeRefresh + ' after=' + anchorAfterRefresh);

    var staleRefresh = window.refreshLifecycleActivity('task-lifecycle-browser', 'project-lifecycle-browser');
    var freshRefresh = window.refreshLifecycleActivity('task-lifecycle-browser', 'project-lifecycle-browser');
    await Promise.all([staleRefresh, freshRefresh]);
    await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-fresh"]'); }, 'latest refresh response');
    if (list().querySelector('[data-lifecycle-execution-id="event-stale"]')) fail('stale refresh response was applied');
    var renderedIDs = ids();
    assertUnique(renderedIDs, 'lifecycle rows');
			if (renderedIDs[0] !== 'event-fresh') fail('lifecycle rows lost newest-first order: ' + renderedIDs.join(','));
			if (renderedIDs.indexOf('event-5') > renderedIDs.indexOf('event-4')) fail('equal-timestamp lifecycle rows lost id-desc order: ' + renderedIDs.join(','));

			await window.refreshLifecycleActivity('task-lifecycle-browser', 'project-lifecycle-browser');
			await waitFor(function() { return list().querySelector('[data-lifecycle-empty]'); }, 'empty lifecycle state');
			window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-browser', project_id:'project-lifecycle-browser'}}));
			await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-empty-live"]'); }, 'live insert after empty lifecycle state');

			var older = list().querySelector('[data-lifecycle-load-older]');
			if (!older) fail('initial bounded lifecycle page did not expose older loader');
			older.click();
			lifecyclePort.dispatchEvent(new Event('scroll', {bubbles:true}));
			lifecyclePort.dispatchEvent(new Event('scroll', {bubbles:true}));
			await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-minus-1"]'); }, 'first older page');    var olderAgain = list().querySelector('[data-lifecycle-load-older]');
    if (!olderAgain) fail('older continuation disappeared after first page');
    olderAgain.click();
    await waitFor(function() { return list().querySelector('[data-lifecycle-no-more]'); }, 'no-more lifecycle state');
    assertUnique(ids(), 'lifecycle rows after older pages');
    if (!list().querySelector('[data-lifecycle-execution-id="event-minus-2"]')) fail('final older lifecycle page was not rendered');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	// The page string is populated after the handler closure is created; requests
	// cannot arrive before the browser is started below.
	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-browser-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotOlderCalls, gotNewerCalls := initialCalls, olderCalls, newerCalls
	gotProject, gotLimit := sawProject, sawBoundedLimit
	gotNewerAfterCalls := append([]string(nil), newerAfterCalls...)
	mu.Unlock()
	if gotInitialCalls < 7 {
		t.Fatalf("browser refresh fixture received %d initial-page requests, want retry, refresh, stale/latest race, and empty/live recovery", gotInitialCalls)
	}
	if gotOlderCalls != 2 {
		t.Fatalf("browser rapid-scroll fixture received %d older-page requests, want exactly 2", gotOlderCalls)
	}
	if gotNewerCalls != 3 || len(gotNewerAfterCalls) != 3 || gotNewerAfterCalls[0] != "event-4" || gotNewerAfterCalls[1] != "event-4" || gotNewerAfterCalls[2] != "event-empty-live" {
		t.Fatalf("browser live fixture received newer-page requests %v, want two coalesced event-4 requests plus the empty-state follow-up", gotNewerAfterCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("browser lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecyclePendingRefreshFailureInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-refresh-failure-browser",
		ProjectID: "project-lifecycle-refresh-failure-browser",
		Title:     "Lifecycle refresh failure browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any, hasMore bool, cursor string) {
		if items == nil {
			items = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": hasMore, "next_cursor": cursor})
	}

	var mu sync.Mutex
	initialCalls := 0
	newerCalls := 0
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-refresh-failure-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-refresh-failure-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		after := query.Get("after")
		if after != "" {
			newerCalls++
		}
		if query.Get("before") == "" && after == "" {
			initialCalls++
		}
		currentInitialCall := initialCalls
		mu.Unlock()

		if after != "" {
			if after != "event-0" {
				http.Error(w, "unexpected newer cursor", http.StatusBadRequest)
				return
			}
			writePage(w, []map[string]any{row("event-1", 1)}, false, "")
			return
		}
		if currentInitialCall == 1 {
			writePage(w, []map[string]any{row("event-0", 0)}, false, "")
			return
		}
		if currentInitialCall == 2 {
			time.Sleep(300 * time.Millisecond)
			http.Error(w, "fixture refresh failure", http.StatusServiceUnavailable)
			return
		}
		writePage(w, []map[string]any{row("event-1", 1), row("event-0", 0)}, false, "")
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function list() { return document.getElementById('lifecycle-activity-list'); }
  async function run() {
    await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-0"]'); }, 'initial lifecycle row');
    var refresh = window.refreshLifecycleActivity('task-lifecycle-refresh-failure-browser', 'project-lifecycle-refresh-failure-browser');
    await wait(50);
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-refresh-failure-browser', project_id:'project-lifecycle-refresh-failure-browser'}}));
    await refresh;
		await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-1"]'); }, 'pending newer retry after refresh failure');
		if (list().querySelector('[data-lifecycle-error]')) fail('refresh failure replaced the preserved lifecycle rows');
		if (list().querySelector('[data-lifecycle-refresh-error]')) fail('successful pending newer retry left a stale refresh error');    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-refresh-failure.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-refresh-failure-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-refresh-failure-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle refresh failure fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle refresh failure browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotNewerCalls := initialCalls, newerCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 || gotNewerCalls != 1 {
		t.Fatalf("refresh failure fixture received initial=%d newer=%d requests, want 2 initial and 1 pending retry", gotInitialCalls, gotNewerCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("refresh failure lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecyclePendingFullRefreshFailureInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-full-refresh-failure-browser",
		ProjectID: "project-lifecycle-full-refresh-failure-browser",
		Title:     "Lifecycle full refresh failure browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any, hasMore bool, cursor string) {
		if items == nil {
			items = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": hasMore, "next_cursor": cursor})
	}

	var mu sync.Mutex
	initialCalls := 0
	refreshInFlight := false
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-full-refresh-failure-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-full-refresh-failure-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("before") != "" || query.Get("after") != "" {
			http.Error(w, "unexpected lifecycle cursor", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		initialCalls++
		currentInitialCall := initialCalls
		if currentInitialCall == 2 {
			refreshInFlight = true
		}
		replacementSawInFlight := refreshInFlight
		mu.Unlock()

		switch currentInitialCall {
		case 1:
			writePage(w, []map[string]any{row("event-0", 0)}, false, "")
		case 2:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte("{"))
			mu.Lock()
			refreshInFlight = false
			mu.Unlock()
		case 3:
			if replacementSawInFlight {
				http.Error(w, "replacement refresh raced original", http.StatusServiceUnavailable)
				return
			}
			writePage(w, []map[string]any{row("event-1", 1), row("event-0", 0)}, false, "")
		default:
			writePage(w, []map[string]any{row("event-1", 1), row("event-0", 0)}, false, "")
		}
	})
	server := httptest.NewServer(fixtureHandler)
	server.Config.SetKeepAlivesEnabled(false)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function list() { return document.getElementById('lifecycle-activity-list'); }
  async function run() {
    await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-0"]'); }, 'initial lifecycle row');
    // Collapse the production debounce only in this fixture so the in-flight refresh race is deterministic.
    var nativeSetTimeout = window.setTimeout;
    window.setTimeout = function(callback, delay) { return nativeSetTimeout(callback, delay === 150 ? 0 : delay); };
    var refresh = window.refreshLifecycleActivity('task-lifecycle-full-refresh-failure-browser', 'project-lifecycle-full-refresh-failure-browser');
    await wait(50);
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_lifecycle_execution_changed', task_id:'task-lifecycle-full-refresh-failure-browser', project_id:'project-lifecycle-full-refresh-failure-browser'}}));
    await refresh;
    await waitFor(function() { return list().querySelector('[data-lifecycle-execution-id="event-1"]'); }, 'pending full-refresh retry', 2000);
    if (list().querySelector('[data-lifecycle-refresh-error]')) fail('successful pending full refresh left a stale refresh error');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-full-refresh-failure.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-full-refresh-failure-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-full-refresh-failure-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle full refresh failure fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle full refresh failure browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotProject, gotLimit := initialCalls, sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 3 {
		t.Fatalf("full refresh failure fixture received %d initial-page requests, want initial, failed refresh, and pending retry", gotInitialCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("full refresh failure lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleRefreshPreservesAnchorBeyondLatestWindowInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-anchor-browser",
		ProjectID: "project-lifecycle-anchor-browser",
		Title:     "Lifecycle anchor browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	initialItems := make([]map[string]any, 0, 20)
	for hour := 19; hour >= 0; hour-- {
		initialItems = append(initialItems, row("event-"+strconv.Itoa(hour), hour))
	}
	newerItems := make([]map[string]any, 0, 5)
	for hour := 20; hour <= 24; hour++ {
		newerItems = append(newerItems, row("event-"+strconv.Itoa(hour), hour))
	}
	refreshedItems := make([]map[string]any, 0, 20)
	for hour := 24; hour >= 5; hour-- {
		refreshedItems = append(refreshedItems, row("event-"+strconv.Itoa(hour), hour))
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	newerCalls := 0
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-anchor-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-anchor-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		if query.Get("after") != "" {
			newerCalls++
		}
		if query.Get("before") == "" && query.Get("after") == "" {
			initialCalls++
		}
		currentInitialCall := initialCalls
		mu.Unlock()

		if query.Get("after") != "" {
			if query.Get("after") != "event-19" {
				http.Error(w, "unexpected newer cursor", http.StatusBadRequest)
				return
			}
			writePage(w, newerItems)
			return
		}
		if currentInitialCall == 1 {
			writePage(w, initialItems)
			return
		}
		writePage(w, refreshedItems)
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function list() { return document.getElementById('lifecycle-activity-list'); }
  function port() { return document.getElementById('lifecycle-activity-scroll'); }
  function row(id) { return list().querySelector('[data-lifecycle-execution-id="' + id + '"]'); }
  function ids() { return Array.prototype.map.call(list().querySelectorAll('[data-lifecycle-execution-id]'), function(item) { return item.getAttribute('data-lifecycle-execution-id'); }); }
  async function run() {
    await waitFor(function() { return row('event-0'); }, 'initial lifecycle rows');
    var lifecyclePort = port();
    lifecyclePort.scrollTop = row('event-0').offsetTop - 40;
    lifecyclePort.dispatchEvent(new Event('scroll', {bubbles:true}));
    await wait(20);
    var anchorBeforeLive = row('event-0').getBoundingClientRect().top;
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-anchor-browser', project_id:'project-lifecycle-anchor-browser'}}));
    await waitFor(function() { return row('event-24'); }, 'newer lifecycle rows');
    var anchorBeforeRefresh = row('event-0').getBoundingClientRect().top;
    if (Math.abs(anchorBeforeRefresh - anchorBeforeLive) > 2) fail('live insert moved the reading anchor: before=' + anchorBeforeLive + ' after=' + anchorBeforeRefresh);
    await window.refreshLifecycleActivity('task-lifecycle-anchor-browser', 'project-lifecycle-anchor-browser');
    await waitFor(function() { return row('event-0'); }, 'preserved lifecycle anchor after refresh');
    var anchorAfterRefresh = row('event-0').getBoundingClientRect().top;
    if (Math.abs(anchorAfterRefresh - anchorBeforeRefresh) > 2) fail('refresh moved the reading anchor beyond the latest window: before=' + anchorBeforeRefresh + ' after=' + anchorAfterRefresh);
    var rendered = ids();
    if (rendered.indexOf('event-24') < 0 || rendered.indexOf('event-0') < 0) fail('refresh dropped loaded lifecycle rows: ' + rendered.join(','));
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-anchor.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-anchor-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-anchor-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle anchor fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle anchor browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotNewerCalls := initialCalls, newerCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 || gotNewerCalls != 1 {
		t.Fatalf("anchor fixture received initial=%d newer=%d requests, want one initial, one refresh, and one newer page", gotInitialCalls, gotNewerCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("anchor lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleMixedLiveRefreshInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-mixed-browser",
		ProjectID: "project-lifecycle-mixed-browser",
		Title:     "Lifecycle mixed refresh browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id, status string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          status,
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		if items == nil {
			items = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	newerCalls := 0
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-mixed-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-mixed-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("before") != "" {
			http.Error(w, "unexpected older cursor", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		if after := query.Get("after"); after != "" {
			newerCalls++
			mu.Unlock()
			if after != "event-0" {
				http.Error(w, "unexpected newer cursor", http.StatusBadRequest)
				return
			}
			writePage(w, nil)
			return
		}
		initialCalls++
		currentInitialCall := initialCalls
		mu.Unlock()

		if currentInitialCall == 1 {
			writePage(w, []map[string]any{row("event-0", "completed", 0)})
			return
		}
		writePage(w, []map[string]any{row("event-0", "failed", 0)})
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function row(id) { return document.querySelector('[data-lifecycle-execution-id="' + id + '"]'); }
  function hasStatus(id, status) { var item = row(id); return !!item && item.textContent.indexOf(status) >= 0; }
  async function run() {
    await waitFor(function() { return hasStatus('event-0', 'completed'); }, 'initial lifecycle row');
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_lifecycle_execution_changed', task_id:'task-lifecycle-mixed-browser', project_id:'project-lifecycle-mixed-browser'}}));
    await wait(40);
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-mixed-browser', project_id:'project-lifecycle-mixed-browser'}}));
    await waitFor(function() { return hasStatus('event-0', 'failed'); }, 'full refresh after mixed live invalidation', 3000);
    if (document.querySelector('[data-lifecycle-refresh-error]')) fail('mixed live invalidation left a refresh error');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-mixed.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-mixed-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-mixed-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle mixed fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle mixed browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotNewerCalls := initialCalls, newerCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 {
		t.Fatalf("mixed lifecycle fixture received %d initial-page requests, want initial page and one full refresh", gotInitialCalls)
	}
	if gotNewerCalls > 1 {
		t.Fatalf("mixed lifecycle fixture received %d newer-page requests, want at most one coalesced request", gotNewerCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("mixed lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleFullInvalidationDuringNewerFailureInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-mixed-failure-browser",
		ProjectID: "project-lifecycle-mixed-failure-browser",
		Title:     "Lifecycle mixed failure browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id, status string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          status,
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		if items == nil {
			items = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	newerCalls := 0
	newerInFlight := false
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/lifecycle-state":
			mu.Lock()
			inFlight := newerInFlight
			mu.Unlock()
			if inFlight {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("1"))
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		case "/tasks/task-lifecycle-mixed-failure-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-mixed-failure-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("before") != "" {
			http.Error(w, "unexpected older cursor", http.StatusBadRequest)
			return
		}
		if after := query.Get("after"); after != "" {
			if after != "event-0" {
				http.Error(w, "unexpected newer cursor", http.StatusBadRequest)
				return
			}
			mu.Lock()
			newerCalls++
			newerInFlight = true
			mu.Unlock()
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			newerInFlight = false
			mu.Unlock()
			http.Error(w, "fixture newer failure", http.StatusServiceUnavailable)
			return
		}

		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		initialCalls++
		currentInitialCall := initialCalls
		replacementSawNewer := newerInFlight
		mu.Unlock()
		if currentInitialCall == 1 {
			writePage(w, []map[string]any{row("event-0", "completed", 0)})
			return
		}
		if replacementSawNewer {
			http.Error(w, "full refresh raced newer request", http.StatusServiceUnavailable)
			return
		}
		writePage(w, []map[string]any{row("event-1", "completed", 1), row("event-0", "completed", 0)})
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        Promise.resolve().then(check).then(function(ok) {
          if (ok) { resolve(); return; }
          if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
          setTimeout(poll, 10);
        }).catch(reject);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function row(id) { return document.querySelector('[data-lifecycle-execution-id="' + id + '"]'); }
  async function newerRequestIsInFlight() {
    await waitFor(function() { return fetch('/lifecycle-state').then(function(response) { return response.status === 200; }); }, 'newer request in flight', 2000);
  }
  async function run() {
    await waitFor(function() { return !!row('event-0'); }, 'initial lifecycle row');
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_thread_execution_started', task_id:'task-lifecycle-mixed-failure-browser', project_id:'project-lifecycle-mixed-failure-browser'}}));
    await newerRequestIsInFlight();
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_lifecycle_execution_changed', task_id:'task-lifecycle-mixed-failure-browser', project_id:'project-lifecycle-mixed-failure-browser'}}));
    await waitFor(function() { return !!row('event-1'); }, 'full refresh retry after newer failure', 4000);
    if (document.querySelector('[data-lifecycle-refresh-error]')) fail('full refresh retry left a refresh error');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-mixed-failure.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-mixed-failure-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-mixed-failure-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle mixed failure fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle mixed failure browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotNewerCalls := initialCalls, newerCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 || gotNewerCalls != 1 {
		t.Fatalf("mixed failure fixture received initial=%d newer=%d requests, want initial, one failing newer, and one retried full refresh", gotInitialCalls, gotNewerCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("mixed failure lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleReconnectReconcilesInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-reconnect-browser",
		ProjectID: "project-lifecycle-reconnect-browser",
		Title:     "Lifecycle reconnect browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-reconnect-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-reconnect-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("before") != "" || query.Get("after") != "" {
			http.Error(w, "unexpected lifecycle cursor", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		initialCalls++
		currentInitialCall := initialCalls
		mu.Unlock()
		if currentInitialCall == 1 {
			writePage(w, []map[string]any{row("event-0", 0)})
			return
		}
		writePage(w, []map[string]any{row("event-1", 1), row("event-0", 0)})
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function row(id) { return document.querySelector('[data-lifecycle-execution-id="' + id + '"]'); }
  async function run() {
    await waitFor(function() { return !!row('event-0'); }, 'initial lifecycle row');
    window.dispatchEvent(new CustomEvent('sse-live-connected', {detail:{reconnected:true}}));
    await waitFor(function() { return !!row('event-1'); }, 'lifecycle reconnect reconciliation', 3000);
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-reconnect.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-reconnect-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-reconnect-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle reconnect fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle reconnect browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls := initialCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 {
		t.Fatalf("reconnect lifecycle fixture received %d initial-page requests, want initial page and reconnect refresh", gotInitialCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("reconnect lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleFullInvalidationDuringOlderFailureInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-older-failure-browser",
		ProjectID: "project-lifecycle-older-failure-browser",
		Title:     "Lifecycle older failure browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}
	row := func(id string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any, hasMore bool, cursor string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": hasMore, "next_cursor": cursor})
	}

	var mu sync.Mutex
	initialCalls := 0
	olderInFlight := false
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/lifecycle-state":
			mu.Lock()
			inFlight := olderInFlight
			mu.Unlock()
			if inFlight {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("1"))
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
			return
		case "/tasks/task-lifecycle-older-failure-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-older-failure-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("after") != "" {
			http.Error(w, "unexpected newer cursor", http.StatusBadRequest)
			return
		}
		if before := query.Get("before"); before != "" {
			if before != "cursor-0" {
				http.Error(w, "unexpected older cursor", http.StatusBadRequest)
				return
			}
			mu.Lock()
			olderInFlight = true
			mu.Unlock()
			time.Sleep(300 * time.Millisecond)
			mu.Lock()
			olderInFlight = false
			mu.Unlock()
			http.Error(w, "fixture older failure", http.StatusServiceUnavailable)
			return
		}

		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		initialCalls++
		currentInitialCall := initialCalls
		replacementSawOlder := olderInFlight
		mu.Unlock()
		if currentInitialCall == 1 {
			writePage(w, []map[string]any{row("event-0", 0)}, true, "cursor-0")
			return
		}
		if replacementSawOlder {
			http.Error(w, "full refresh raced older request", http.StatusServiceUnavailable)
			return
		}
		writePage(w, []map[string]any{row("event-1", 1), row("event-0", 0)}, false, "")
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        Promise.resolve().then(check).then(function(ok) {
          if (ok) { resolve(); return; }
          if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
          setTimeout(poll, 10);
        }).catch(reject);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  async function olderRequestIsInFlight() {
    await waitFor(function() { return fetch('/lifecycle-state').then(function(response) { return response.status === 200; }); }, 'older request in flight', 2000);
  }
  async function run() {
    await waitFor(function() { return !!document.querySelector('[data-lifecycle-execution-id="event-0"]'); }, 'initial lifecycle row');
    var older = document.querySelector('[data-lifecycle-load-older]');
    if (!older) throw new Error('initial lifecycle page did not expose older loader');
    older.click();
    await olderRequestIsInFlight();
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_lifecycle_execution_changed', task_id:'task-lifecycle-older-failure-browser', project_id:'project-lifecycle-older-failure-browser'}}));
    await waitFor(function() { return !!document.querySelector('[data-lifecycle-execution-id="event-1"]'); }, 'full refresh retry after older failure', 4000);
    if (document.querySelector('[data-lifecycle-refresh-error]')) throw new Error('full refresh retry left a refresh error');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-older-failure.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-older-failure-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-older-failure-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle older failure fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle older failure browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls := initialCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 {
		t.Fatalf("older failure fixture received %d initial-page requests, want initial and retried full refresh", gotInitialCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("older failure lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleHTMXSwapDuringRequestReconcilesOnReconnectInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-htmx-swap-browser",
		ProjectID: "project-lifecycle-htmx-swap-browser",
		Title:     "Lifecycle HTMX swap browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}

	row := func(id string, hour int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	sawProject := false
	sawLimit := false
	initialStarted := make(chan struct{})
	releaseInitial := make(chan struct{})
	var initialStartedOnce sync.Once
	var releaseInitialOnce sync.Once
	releaseInitialRequest := func() {
		releaseInitialOnce.Do(func() { close(releaseInitial) })
	}
	browserResult := make(chan string, 1)
	page := ""
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/lifecycle-state":
			select {
			case <-initialStarted:
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("1"))
			default:
				w.WriteHeader(http.StatusNoContent)
			}
			return
		case "/tasks/task-lifecycle-htmx-swap-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-htmx-swap-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("before") != "" || query.Get("after") != "" {
			http.Error(w, "unexpected lifecycle cursor", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		initialCalls++
		currentInitialCall := initialCalls
		mu.Unlock()
		if currentInitialCall == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			initialStartedOnce.Do(func() { close(initialStarted) })
			<-releaseInitial
			writePage(w, []map[string]any{row("event-0", 0)})
			return
		}
		writePage(w, []map[string]any{row("event-1", 1)})
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()
	defer releaseInitialRequest()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        Promise.resolve().then(check).then(function(ok) {
          if (ok) { resolve(); return; }
          if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
          setTimeout(poll, 10);
        }).catch(reject);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  async function run() {
    await waitFor(function() {
      return fetch('/lifecycle-state').then(function(response) { return response.status === 200; });
    }, 'lifecycle request in flight', 3000);

    var oldRoot = document.getElementById('task-detail-content');
    if (!oldRoot) fail('missing original task detail root');
    var lifecycleScriptSource = '';
    Array.prototype.some.call(document.getElementsByTagName('script'), function(script) {
      if (script.textContent.indexOf('window._taskLifecycleActivityHandlers') < 0) return false;
      lifecycleScriptSource = script.textContent;
      return true;
    });
    if (!lifecycleScriptSource) fail('missing lifecycle script source');

    oldRoot.dispatchEvent(new CustomEvent('htmx:beforeSwap', {bubbles:true, detail:{target:oldRoot}}));
    oldRoot.outerHTML = '<div id="task-detail-content" class="h-full flex flex-col">' +
      '<div><a data-tab="details" class="tab-active">Details</a><a data-tab="lifecycle">Lifecycle</a></div>' +
      '<div id="tab-lifecycle" class="task-tab-panel hidden">' +
      '<div id="lifecycle-activity-scroll" data-lifecycle-scrollport="true" style="height:240px;overflow-y:auto;">' +
      '<div id="lifecycle-activity-list" data-task-id="task-lifecycle-htmx-swap-browser" data-project-id="project-lifecycle-htmx-swap-browser"></div>' +
      '</div></div></div>';
    var reboundScript = document.createElement('script');
    reboundScript.textContent = lifecycleScriptSource;
    document.body.appendChild(reboundScript);

    window.dispatchEvent(new CustomEvent('sse-live-connected', {detail:{reconnected:true}}));
    await waitFor(function() {
      return !!document.querySelector('[data-lifecycle-execution-id="event-1"]');
    }, 'lifecycle reconnect recovery after HTMX swap', 4000);
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:140px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-htmx-swap.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-htmx-swap-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-htmx-swap-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle HTMX swap fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	releaseInitialRequest()
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle HTMX swap browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls := initialCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 {
		t.Fatalf("HTMX swap lifecycle fixture received %d initial-page requests, want the detached request and reconnect refresh", gotInitialCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("HTMX swap lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}

func TestTaskDetailLifecycleHTMXSwapPreservesAnchorInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-htmx-anchor-browser",
		ProjectID: "project-lifecycle-htmx-anchor-browser",
		Title:     "Lifecycle HTMX anchor browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}

	row := func(id string, hour int, summary string) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         summary,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
	}
	initialItems := []map[string]any{
		row("event-4", 4, "initial row 4"),
		row("event-3", 3, "initial row 3"),
		row("event-2", 2, "initial row 2"),
		row("event-1", 1, "initial row 1"),
		row("event-0", 0, "initial row 0"),
	}
	refreshedItems := []map[string]any{
		row("event-new", 5, "new row inserted above the anchor with a deliberately long summary that changes its height after the HTMX replacement"),
		row("event-4", 4, "initial row 4"),
		row("event-3", 3, "initial row 3"),
		row("event-2", 2, "initial row 2"),
		row("event-1", 1, "initial row 1"),
		row("event-0", 0, "initial row 0"),
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	sawProject := false
	sawLimit := false
	page := ""
	browserResult := make(chan string, 1)
	fixtureHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case "/tasks/task-lifecycle-htmx-anchor-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-htmx-anchor-browser/lifecycle-executions":
		default:
			http.NotFound(w, r)
			return
		}

		query := r.URL.Query()
		if query.Get("before") != "" || query.Get("after") != "" {
			http.Error(w, "unexpected lifecycle cursor", http.StatusBadRequest)
			return
		}
		mu.Lock()
		if query.Get("project_id") == task.ProjectID {
			sawProject = true
		}
		if query.Get("limit") == "20" {
			sawLimit = true
		}
		initialCalls++
		call := initialCalls
		mu.Unlock()
		if call == 1 {
			writePage(w, initialItems)
			return
		}
		writePage(w, refreshedItems)
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function wait(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 6000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  function fail(message) { throw new Error(message); }
  function list() { return document.getElementById('lifecycle-activity-list'); }
  function port() { return document.getElementById('lifecycle-activity-scroll'); }
  function row(id) { return list().querySelector('[data-lifecycle-execution-id="' + id + '"]'); }
  async function run() {
    await waitFor(function() { return !!row('event-2'); }, 'initial lifecycle rows');
    await wait(50);
    var lifecyclePort = port();
    var initialAnchor = row('event-2');
    var portRect = lifecyclePort.getBoundingClientRect();
    lifecyclePort.scrollTop += initialAnchor.getBoundingClientRect().top - portRect.top - 40;
    lifecyclePort.dispatchEvent(new Event('scroll', {bubbles:true}));
    await wait(20);
    var anchorBeforeSwap = row('event-2').getBoundingClientRect().top - portRect.top;
    if (Math.abs(anchorBeforeSwap - 40) > 2) fail('failed to position lifecycle anchor before swap');

    var oldRoot = document.getElementById('task-detail-content');
    if (!oldRoot) fail('missing original task detail root');
    var lifecycleScriptSource = '';
    Array.prototype.some.call(document.getElementsByTagName('script'), function(script) {
      if (script.textContent.indexOf('window._taskLifecycleActivityHandlers') < 0) return false;
      lifecycleScriptSource = script.textContent;
      return true;
    });
    if (!lifecycleScriptSource) fail('missing lifecycle script source');

    oldRoot.dispatchEvent(new CustomEvent('htmx:beforeSwap', {bubbles:true, detail:{target:oldRoot}}));
    oldRoot.outerHTML = '<div id="task-detail-content" class="replacement h-full flex flex-col">' +
      '<div><a data-tab="details">Details</a><a data-tab="lifecycle" class="tab-active">Lifecycle</a></div>' +
      '<div id="tab-lifecycle" class="task-tab-panel">' +
      '<div id="lifecycle-activity-scroll" data-lifecycle-scrollport="true" style="height:240px;overflow-y:auto;">' +
      '<div id="lifecycle-activity-list" data-task-id="task-lifecycle-htmx-anchor-browser" data-project-id="project-lifecycle-htmx-anchor-browser"></div>' +
      '</div></div></div>';
    var reboundScript = document.createElement('script');
    reboundScript.textContent = lifecycleScriptSource;
    document.body.appendChild(reboundScript);

    await waitFor(function() { return !!row('event-new'); }, 'lifecycle rebound anchor refresh', 4000);
    var replacementPortRect = port().getBoundingClientRect();
    var anchorAfterSwap = row('event-2').getBoundingClientRect().top - replacementPortRect.top;
    if (Math.abs(anchorAfterSwap - anchorBeforeSwap) > 2) fail('HTMX swap moved the lifecycle reading anchor: before=' + anchorBeforeSwap + ' after=' + anchorAfterSwap);
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{height:80px!important;min-height:80px!important;box-sizing:border-box;}#lifecycle-activity-list [data-lifecycle-execution-id=\"event-new\"]{height:240px!important;}#task-detail-content.replacement #lifecycle-activity-list [data-lifecycle-execution-id=\"event-4\"]{height:200px!important;} .hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-htmx-anchor.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-htmx-anchor-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-htmx-anchor-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle HTMX anchor fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for lifecycle browser result"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("Task Detail Lifecycle HTMX swap anchor browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls := initialCalls
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 {
		t.Fatalf("HTMX swap anchor lifecycle fixture received %d initial-page requests, want initial and rebound refresh", gotInitialCalls)
	}
	if !gotProject || !gotLimit {
		t.Fatalf("HTMX swap anchor lifecycle requests did not preserve project scope and bounded limit: project=%v limit=%v", gotProject, gotLimit)
	}
}
