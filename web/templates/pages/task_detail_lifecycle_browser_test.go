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
	"reflect"
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
	row := func(id string, sequence int) map[string]any {
		return map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(sequence) * time.Minute).Format(time.RFC3339),
		}
	}
	rows := func(first, last int, descending bool) []map[string]any {
		items := make([]map[string]any, 0, last-first+1)
		if descending {
			for sequence := last; sequence >= first; sequence-- {
				items = append(items, row("event-"+strconv.Itoa(sequence), sequence))
			}
			return items
		}
		for sequence := first; sequence <= last; sequence++ {
			items = append(items, row("event-"+strconv.Itoa(sequence), sequence))
		}
		return items
	}
	writePage := func(w http.ResponseWriter, items []map[string]any, hasMore bool, cursor string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": hasMore, "next_cursor": cursor})
	}

	var mu sync.Mutex
	initialCalls := 0
	newerCalls := 0
	newerCursors := make([]string, 0, 3)
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
		case "/browser-stats":
			mu.Lock()
			stats := map[string]any{"newer_calls": newerCalls, "newer_cursors": append([]string(nil), newerCursors...)}
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(stats)
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
			newerCursors = append(newerCursors, after)
		}
		if query.Get("before") == "" && after == "" {
			initialCalls++
		}
		currentInitialCall := initialCalls
		mu.Unlock()

		if query.Get("before") != "" {
			http.Error(w, "unexpected older lifecycle cursor", http.StatusBadRequest)
			return
		}
		switch after {
		case "event-0":
			writePage(w, rows(1, 20, false), true, "gap-cursor-20")
			return
		case "gap-cursor-20":
			writePage(w, rows(21, 40, false), true, "gap-cursor-40")
			return
		case "gap-cursor-40":
			writePage(w, rows(41, 45, false), false, "")
			return
		case "":
		default:
			http.Error(w, "unexpected newer lifecycle cursor", http.StatusBadRequest)
			return
		}
		if currentInitialCall == 1 {
			writePage(w, rows(0, 0, true), false, "")
			return
		}
		writePage(w, rows(26, 45, true), true, "latest-before-cursor")
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
	  function gap() { return document.querySelector('[data-lifecycle-gap]'); }
	  function ids() { return Array.prototype.map.call(document.querySelectorAll('[data-lifecycle-execution-id]'), function(item) { return item.getAttribute('data-lifecycle-execution-id'); }); }
	  function stats() { return fetch('/browser-stats').then(function(response) { return response.json(); }); }
	  function scrollGapIntoView() {
	    var marker = gap();
	    var port = document.getElementById('lifecycle-activity-scroll');
	    if (!marker || !port) throw new Error('missing lifecycle gap marker or scrollport');
	    port.dispatchEvent(new WheelEvent('wheel', {bubbles:true, deltaY:120}));
	    port.scrollTop += marker.getBoundingClientRect().top - port.getBoundingClientRect().bottom + 40;
	    port.dispatchEvent(new Event('scroll', {bubbles:true}));
	  }
	  async function run() {
	    await waitFor(function() { return !!row('event-0'); }, 'initial lifecycle row');
	    window.dispatchEvent(new CustomEvent('sse-live-connected', {detail:{reconnected:true}}));
	    await waitFor(function() { return !!row('event-45') && !!gap(); }, 'newest lifecycle reconnect page and gap marker', 3000);
	    await wait(250);
	    var initialStats = await stats();
	    if (initialStats.newer_calls !== 0) throw new Error('reconnect eagerly requested gap pages before scrolling: ' + initialStats.newer_calls);
	    if (row('event-1')) throw new Error('reconnect eagerly rendered missed lifecycle rows before scrolling');

	    scrollGapIntoView();
	    await waitFor(function() { return !!row('event-1') && !!row('event-20') && !!gap(); }, 'first bounded missed lifecycle page', 3000);
	    var firstStats = await stats();
	    if (firstStats.newer_calls !== 1) throw new Error('first gap scroll requested ' + firstStats.newer_calls + ' pages, want exactly one');
	    if (row('event-21')) throw new Error('first gap scroll rendered more than one missed page');

	    scrollGapIntoView();
	    await waitFor(function() { return !!row('event-25') && !gap(); }, 'second bounded missed lifecycle page', 3000);
	    var rendered = ids();
	    var unique = Object.create(null);
	    rendered.forEach(function(id) { unique[id] = true; });
	    if (rendered.length !== 46 || Object.keys(unique).length !== 46) throw new Error('reconnect did not render 46 unique lifecycle rows: rendered=' + rendered.length + ' unique=' + Object.keys(unique).length);
	    for (var sequence = 0; sequence <= 45; sequence++) {
	      if (!unique['event-' + sequence]) throw new Error('reconnect skipped lifecycle event-' + sequence);
	    }
	    var finalStats = await stats();
	    if (finalStats.newer_calls !== 2) throw new Error('two gap scrolls requested ' + finalStats.newer_calls + ' pages, want exactly two');
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
	gotInitialCalls, gotNewerCalls := initialCalls, newerCalls
	gotNewerCursors := append([]string(nil), newerCursors...)
	gotProject, gotLimit := sawProject, sawLimit
	mu.Unlock()
	if gotInitialCalls != 2 || gotNewerCalls != 2 {
		t.Fatalf("reconnect lifecycle fixture received initial=%d newer=%d requests, want two latest pages and two scroll-driven bounded gap pages", gotInitialCalls, gotNewerCalls)
	}
	wantNewerCursors := []string{"event-0", "gap-cursor-20"}
	if !reflect.DeepEqual(gotNewerCursors, wantNewerCursors) {
		t.Fatalf("reconnect lifecycle gap cursors = %v, want %v", gotNewerCursors, wantNewerCursors)
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

func TestTaskDetailLifecycleRetainedRowFinalizationRehydratesInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-retained-finalization-browser",
		ProjectID: "project-lifecycle-retained-finalization-browser",
		Title:     "Lifecycle retained finalization browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}

	row := func(id, status string, hour int, completedAt string) map[string]any {
		result := map[string]any{
			"id":              id,
			"when":            "after_complete",
			"skill_key":       "hook-" + id,
			"status":          status,
			"output_contract": "activity_summary",
			"summary":         "summary for " + id,
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		}
		if completedAt != "" {
			result["completed_at"] = completedAt
		}
		return result
	}
	initialItems := make([]map[string]any, 0, 20)
	for hour := 19; hour >= 2; hour-- {
		initialItems = append(initialItems, row("event-"+strconv.Itoa(hour), "completed", hour, ""))
	}
	initialItems = append(initialItems, row("event-reconnect", "running", 1, ""))
	initialItems = append(initialItems, row("event-target", "running", 0, ""))
	latestItems := make([]map[string]any, 0, 20)
	for hour := 39; hour >= 20; hour-- {
		latestItems = append(latestItems, row("event-new-"+strconv.Itoa(hour), "completed", hour, ""))
	}
	finalizedTarget := row("event-target", "completed", 0, "2026-01-01T00:05:00Z")
	finalizedTarget["error"] = "terminal target error"
	runningReconnectTarget := row("event-reconnect", "running", 1, "")
	finalizedReconnectTarget := row("event-reconnect", "failed", 1, "2026-01-01T01:05:00Z")
	finalizedReconnectTarget["error"] = "reconnected target error"
	writeJSON := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	writePage := func(w http.ResponseWriter, items []map[string]any) {
		writeJSON(w, map[string]any{"items": items, "has_more": false, "next_cursor": ""})
	}

	var mu sync.Mutex
	initialCalls := 0
	targetCalls := 0
	reconnectTargetCalls := 0
	sawProject := false
	sawLimit := false
	sawTargetProject := false
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
		case "/tasks/task-lifecycle-retained-finalization-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
			return
		case "/api/tasks/task-lifecycle-retained-finalization-browser/lifecycle-executions":
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
			} else {
				writePage(w, latestItems)
			}
			return
		case "/api/tasks/task-lifecycle-retained-finalization-browser/lifecycle-executions/event-target":
			if r.URL.Query().Get("project_id") == task.ProjectID {
				mu.Lock()
				sawTargetProject = true
				mu.Unlock()
			}
			mu.Lock()
			targetCalls++
			mu.Unlock()
			writeJSON(w, finalizedTarget)
			return
		case "/api/tasks/task-lifecycle-retained-finalization-browser/lifecycle-executions/event-reconnect":
			if r.URL.Query().Get("project_id") == task.ProjectID {
				mu.Lock()
				sawTargetProject = true
				mu.Unlock()
			}
			mu.Lock()
			reconnectTargetCalls++
			call := reconnectTargetCalls
			mu.Unlock()
			if call == 1 {
				writeJSON(w, runningReconnectTarget)
			} else {
				writeJSON(w, finalizedReconnectTarget)
			}
			return
		default:
			http.NotFound(w, r)
			return
		}
	})
	server := httptest.NewServer(fixtureHandler)
	defer server.Close()

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
  function lifecycleRow(id) { return document.querySelector('[data-lifecycle-execution-id="' + id + '"]'); }
  function lifecycleStatus(id) {
    var target = lifecycleRow(id);
    var badges = target && target.querySelectorAll('.badge');
    return badges && badges.length ? badges[badges.length - 1].textContent.trim() : '';
  }
  async function run() {
    await waitFor(function() { return !!lifecycleRow('event-target') && lifecycleStatus('event-target') === 'running' && lifecycleStatus('event-reconnect') === 'running'; }, 'initial retained running rows');
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{
      type:'task_lifecycle_execution_changed',
      task_id:'task-lifecycle-retained-finalization-browser',
      project_id:'project-lifecycle-retained-finalization-browser',
      exec_id:'event-target',
      status:'completed'
    }}));
    await waitFor(function() { return lifecycleStatus('event-target') === 'completed'; }, 'retained row terminal rehydration', 4000);
    var completed = lifecycleRow('event-target').querySelector('.text-xs.opacity-60');
    var error = lifecycleRow('event-target').querySelector('.text-error');
    if (!completed || completed.textContent.indexOf(':05:') < 0) throw new Error('retained row did not render refreshed completion time: ' + (completed && completed.textContent || '<missing>'));
    if (!error || error.textContent.indexOf('terminal target error') < 0) throw new Error('retained row did not render refreshed terminal error');
    if (lifecycleStatus('event-reconnect') !== 'running') throw new Error('first refresh unexpectedly finalized reconnect target');
    window.dispatchEvent(new CustomEvent('sse-live-connected', {detail:{reconnected:true}}));
    await waitFor(function() { return lifecycleStatus('event-reconnect') === 'failed'; }, 'retained row reconnect rehydration', 4000);
    var reconnectError = lifecycleRow('event-reconnect').querySelector('.text-error');
    if (!reconnectError || reconnectError.textContent.indexOf('reconnected target error') < 0) throw new Error('reconnect did not render retained terminal error');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;}#lifecycle-activity-scroll{height:240px!important;max-height:240px!important;overflow-y:scroll!important;}#lifecycle-activity-list [data-lifecycle-execution-id]{min-height:80px;box-sizing:border-box;}.hidden{display:none!important;}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-retained-finalization.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	profileDir := filepath.Join(t.TempDir(), "task-detail-lifecycle-retained-finalization-profile")
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+profileDir,
		server.URL+"/tasks/task-lifecycle-retained-finalization-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle retained finalization fixture: %v", err)
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
		t.Fatalf("Task Detail Lifecycle retained finalization browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}

	mu.Lock()
	gotInitialCalls, gotTargetCalls, gotReconnectTargetCalls := initialCalls, targetCalls, reconnectTargetCalls
	gotProject, gotLimit, gotTargetProject := sawProject, sawLimit, sawTargetProject
	mu.Unlock()
	if gotInitialCalls != 3 || gotTargetCalls != 1 || gotReconnectTargetCalls != 2 {
		t.Fatalf("retained finalization fixture received page=%d target=%d reconnect_target=%d requests, want initial/event/reconnect pages and bounded targeted refreshes", gotInitialCalls, gotTargetCalls, gotReconnectTargetCalls)
	}
	if !gotProject || !gotLimit || !gotTargetProject {
		t.Fatalf("retained finalization requests did not preserve project scope and bounded page size: project=%v limit=%v target_project=%v", gotProject, gotLimit, gotTargetProject)
	}
}

func TestTaskDetailLifecycleFillsRemainingHeightInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{
		ID:        "task-lifecycle-fill-browser",
		ProjectID: "project-lifecycle-fill-browser",
		Title:     "Lifecycle fill browser fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	var fragment bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "lifecycle", nil).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render TaskDetailContent: %v", err)
	}

	items := make([]map[string]any, 0, 20)
	for hour := 19; hour >= 0; hour-- {
		items = append(items, map[string]any{
			"id":              "event-" + strconv.Itoa(hour),
			"when":            "after_complete",
			"skill_key":       "hook-" + strconv.Itoa(hour),
			"status":          "completed",
			"output_contract": "activity_summary",
			"summary":         "summary for event " + strconv.Itoa(hour),
			"started_at":      time.Date(2026, time.January, 1, hour, 0, 0, 0, time.UTC).Format(time.RFC3339),
		})
	}

	page := ""
	browserResult := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/browser-result":
			select {
			case browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/task-lifecycle-fill-browser":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		case "/api/tasks/task-lifecycle-fill-browser/lifecycle-executions":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "has_more": false, "next_cursor": ""})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > 6000) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function report(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}).catch(function() {});
  }
  async function run() {
    await waitFor(function() { return document.querySelectorAll('#lifecycle-activity-list [data-lifecycle-execution-id]').length === 20; }, 'lifecycle rows');
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var root = document.getElementById('task-detail-content');
    var panel = document.getElementById('tab-lifecycle');
    var card = panel.querySelector('.card');
    var port = document.getElementById('lifecycle-activity-scroll');
    var rootRect = root.getBoundingClientRect();
    var cardRect = card.getBoundingClientRect();
    if (Math.abs(cardRect.bottom - rootRect.bottom) > 2) throw new Error('lifecycle card does not fill remaining height: card=' + cardRect.bottom + ' root=' + rootRect.bottom);
    if (port.clientHeight < 500) throw new Error('lifecycle scrollport is unexpectedly short: ' + port.clientHeight);
    if (port.scrollHeight <= port.clientHeight) throw new Error('lifecycle rows do not overflow their internal scrollport');
    if (root.scrollHeight > root.clientHeight + 2) throw new Error('lifecycle rows escaped into page-level overflow');
    if (getComputedStyle(port).overflowY !== 'auto') throw new Error('lifecycle activity is not the vertical scroll owner');
    await report('pass', '');
  }
  run().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	page = "<!doctype html><html><head><meta charset=\"utf-8\"><style>html,body{margin:0;padding:0;}#task-detail-content{height:900px;display:flex;flex-direction:column;box-sizing:border-box;}.flex{display:flex}.flex-col{flex-direction:column}.flex-1{flex:1 1 0%}.flex-shrink-0{flex-shrink:0}.min-h-0{min-height:0}.hidden{display:none!important}.card{display:flex;flex-direction:column}.card-body{display:flex;flex:1 1 auto;flex-direction:column;min-height:0;padding:32px;box-sizing:border-box}.mb-6{margin-bottom:24px}.mb-3{margin-bottom:12px}#lifecycle-activity-scroll{overflow-y:auto}.space-y-2>[data-lifecycle-execution-id]{min-height:80px;margin-bottom:8px;box-sizing:border-box}</style></head><body>" + fragment.String() + runner + "</body></html>"

	stderrPath := filepath.Join(t.TempDir(), "task-detail-lifecycle-fill.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--user-data-dir="+filepath.Join(t.TempDir(), "task-detail-lifecycle-fill-profile"),
		server.URL+"/tasks/task-lifecycle-fill-browser",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome lifecycle fill fixture: %v", err)
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
		t.Fatalf("Task Detail Lifecycle fill browser regression failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}
}
