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
