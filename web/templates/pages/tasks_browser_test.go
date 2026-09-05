package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestTasksDefaultAndPersistedSortsAcrossLiveRefreshAndDragInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-tasks-browser", Name: "Tasks Browser"}
	base := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	completedOld := base.Add(10 * time.Minute)
	completedNew := base.Add(30 * time.Minute)
	var mu sync.Mutex
	var requestMu sync.Mutex
	var requestLog []string
	tasks := []models.Task{
		{ID: "backlog-old", ProjectID: project.ID, Title: "Alpha Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, CreatedAt: base, UpdatedAt: base, DisplayOrder: 0},
		{ID: "backlog-new", ProjectID: project.ID, Title: "Zulu Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, CreatedAt: base.Add(20 * time.Minute), UpdatedAt: base.Add(20 * time.Minute), DisplayOrder: 1},
		{ID: "completed-old", ProjectID: project.ID, Title: "Alpha Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base.Add(40 * time.Minute), UpdatedAt: base.Add(40 * time.Minute), CompletedAt: &completedOld, DisplayOrder: 0},
		{ID: "completed-legacy", ProjectID: project.ID, Title: "Mike Legacy Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base.Add(50 * time.Minute), UpdatedAt: base.Add(20 * time.Minute), CompletedAt: nil, DisplayOrder: 1},
		{ID: "completed-new", ProjectID: project.ID, Title: "Zulu Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base, UpdatedAt: base, CompletedAt: &completedNew, DisplayOrder: 2},
		{ID: "active-move", ProjectID: project.ID, Title: "Delta Moved", Category: models.CategoryActive, Status: models.StatusPending, CreatedAt: base.Add(5 * time.Minute), UpdatedAt: base.Add(5 * time.Minute), DisplayOrder: 0},
	}
	clock := base.Add(40 * time.Minute)

	sortPreference := func(r *http.Request, name, fallback string) string {
		cookie, err := r.Cookie(name)
		if err != nil || cookie.Value == "" {
			return fallback
		}
		return cookie.Value
	}
	sortedTasks := func(backlogSort, completedSort string) []models.Task {
		mu.Lock()
		defer mu.Unlock()
		out := append([]models.Task(nil), tasks...)
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Category != out[j].Category {
				return out[i].Category < out[j].Category
			}
			sortBy := ""
			switch out[i].Category {
			case models.CategoryBacklog:
				sortBy = backlogSort
			case models.CategoryCompleted:
				sortBy = completedSort
			}
			switch sortBy {
			case "title_asc":
				return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
			case "created_desc":
				return out[i].CreatedAt.After(out[j].CreatedAt)
			case "completed_desc":
				iTime, jTime := out[i].UpdatedAt, out[j].UpdatedAt
				if out[i].CompletedAt != nil {
					iTime = *out[i].CompletedAt
				}
				if out[j].CompletedAt != nil {
					jTime = *out[j].CompletedAt
				}
				return iTime.After(jTime)
			default:
				return out[i].DisplayOrder < out[j].DisplayOrder
			}
		})
		return out
	}
	renderBoardWithSorts := func(backlogSort, completedSort string) string {
		var out bytes.Buffer
		if err := components.KanbanBoard(sortedTasks(backlogSort, completedSort), project.ID, backlogSort, completedSort, nil, nil).Render(context.Background(), &out); err != nil {
			t.Fatalf("render task board: %v", err)
		}
		return out.String()
	}
	renderBoard := func(r *http.Request) string {
		backlogSort := sortPreference(r, "backlog_sort", "created_desc")
		completedSort := sortPreference(r, "completed_sort", "completed_desc")
		return renderBoardWithSorts(backlogSort, completedSort)
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
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function zone(category) { return document.querySelector('.category-drop-zone[data-category="' + category + '"]:not([data-drop-type="status"])'); }
  function ids(category) { return Array.from(zone(category).querySelectorAll(':scope > [data-task-id]')).map(function(card) { return card.dataset.taskId; }); }
  function taskState(id) {
    var card = document.getElementById('task-' + id);
    var icon = card && card.querySelector('[data-task-state-icon]');
    return icon && icon.dataset.taskState;
  }
  function assertStateIconBeforeTitle(id, expected) {
    var card = document.getElementById('task-' + id);
    var icon = card && card.querySelector('[data-task-state-icon]');
    var title = card && card.querySelector('[data-task-title]');
    if (!icon || !title || icon.nextElementSibling !== title) fail(id + ' state icon is not immediately before title');
    if (icon.dataset.taskState !== expected) fail(id + ': expected state ' + expected + ', got ' + icon.dataset.taskState);
    if (!icon.getAttribute('aria-label') || icon.getAttribute('role') !== 'img') fail(id + ' state icon is not accessible');
  }
  function assertOrder(category, expected, label) {
    var actual = ids(category);
    if (actual.join(',') !== expected.join(',')) fail(label + ': expected ' + expected.join(',') + ', got ' + actual.join(','));
  }
  function activeSort(category, key) {
    var link = document.querySelector('button[hx-post*="/tasks/' + category + '/sort"][hx-post*="sort=' + key + '"]');
    return link && link.classList.contains('active');
  }
  function clickSort(category, key) {
    htmx.process(document.getElementById('kanban-board'));
    var link = document.querySelector('button[hx-post*="/tasks/' + category + '/sort"][hx-post*="sort=' + key + '"]');
    if (!link) fail('missing ' + category + ' ' + key + ' sort control');
    link.click();
    return waitFor(function() { return activeSort(category, key); }, category + ' ' + key + ' sort');
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    await waitFor(function() { return window.htmx && document.getElementById('task-active-move'); }, 'Tasks hydration');
    htmx.process(document.body);
    assertOrder('backlog', ['backlog-new', 'backlog-old'], 'default Backlog creation order');
    assertOrder('completed', ['completed-new', 'completed-legacy', 'completed-old'], 'default Completed completion order');
    assertStateIconBeforeTitle('backlog-old', 'pending');
    assertStateIconBeforeTitle('active-move', 'queued');
    assertStateIconBeforeTitle('completed-old', 'completed');
    assertStateIconBeforeTitle('completed-new', 'completed');
    if (!activeSort('backlog', 'created_desc')) fail('Backlog default sort control is not active');
    if (!activeSort('completed', 'completed_desc')) fail('Completed default sort control is not active');

    await fetch('/browser-add?phase=default', {method:'POST'});
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_updated', project_id:'project-tasks-browser'}}));
    await waitFor(function() { return document.getElementById('task-backlog-live') && ids('backlog')[0] === 'backlog-live' && taskState('backlog-old') === 'failed' && taskState('completed-old') === 'merged' && taskState('completed-new') === 'goal-met'; }, 'default live refresh and state icon morph');
    assertOrder('backlog', ['backlog-live', 'backlog-new', 'backlog-old'], 'live Backlog creation order');
    assertOrder('completed', ['completed-live', 'completed-new', 'completed-legacy', 'completed-old'], 'live Completed completion order');
    assertStateIconBeforeTitle('backlog-old', 'failed');
    assertStateIconBeforeTitle('completed-old', 'merged');
    assertStateIconBeforeTitle('completed-new', 'goal-met');

    var card = document.getElementById('task-active-move');
    var completedZone = zone('completed');
    var cardRect = card.getBoundingClientRect();
    var zoneRect = completedZone.getBoundingClientRect();
    var startX = cardRect.left + 10, startY = cardRect.top + 10;
    var dropX = zoneRect.left + zoneRect.width / 2, dropY = zoneRect.top + 10;
    card.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, pointerId:9, pointerType:'mouse', button:0, buttons:1, clientX:startX, clientY:startY}));
    window.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, pointerId:9, pointerType:'mouse', buttons:1, clientX:dropX, clientY:dropY}));
    window.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, pointerId:9, pointerType:'mouse', button:0, clientX:dropX, clientY:dropY}));
    await waitFor(function() { return ids('completed')[0] === 'active-move'; }, 'drag into Completed');
    assertOrder('completed', ['active-move', 'completed-live', 'completed-new', 'completed-legacy', 'completed-old'], 'dragged task completion order');

    await clickSort('backlog', 'title_asc');
    await clickSort('completed', 'title_asc');
    assertOrder('backlog', ['backlog-old', 'backlog-live', 'backlog-new'], 'explicit Backlog title order');
    assertOrder('completed', ['completed-old', 'active-move', 'completed-legacy', 'completed-live', 'completed-new'], 'explicit Completed title order');

    await fetch('/browser-add?phase=persisted', {method:'POST'});
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_updated', project_id:'project-tasks-browser'}}));
    await waitFor(function() { return document.getElementById('task-backlog-persisted'); }, 'persisted live refresh');
    assertOrder('backlog', ['backlog-old', 'backlog-live', 'backlog-persisted', 'backlog-new'], 'persisted Backlog title order');
    assertOrder('completed', ['completed-old', 'active-move', 'completed-legacy', 'completed-live', 'completed-persisted', 'completed-new'], 'persisted Completed title order');
    if (!activeSort('backlog', 'title_asc') || !activeSort('completed', 'title_asc')) fail('explicit sorts were not preserved after live refresh');
    await report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requestLog = append(requestLog, r.Method+" "+r.URL.RequestURI()+" HX="+r.Header.Get("HX-Request"))
		requestMu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/tasks" && r.Method == http.MethodGet:
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(renderBoard(r)))
				return
			}
			backlogSort := sortPreference(r, "backlog_sort", "created_desc")
			completedSort := sortPreference(r, "completed_sort", "completed_desc")
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, sortedTasks(backlogSort, completedSort), nil, nil, backlogSort, completedSort).Render(context.Background(), &out); err != nil {
				t.Fatalf("render Tasks page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case strings.HasPrefix(r.URL.Path, "/tasks/") && strings.HasSuffix(r.URL.Path, "/sort") && r.Method == http.MethodPost:
			category := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/tasks/"), "/sort")
			sortBy := r.URL.Query().Get("sort")
			http.SetCookie(w, &http.Cookie{Name: category + "_sort", Value: sortBy, Path: "/"})
			backlogSort := sortPreference(r, "backlog_sort", "created_desc")
			completedSort := sortPreference(r, "completed_sort", "completed_desc")
			if category == "backlog" {
				backlogSort = sortBy
			} else {
				completedSort = sortBy
			}
			_, _ = w.Write([]byte(renderBoardWithSorts(backlogSort, completedSort)))
		case r.URL.Path == "/tasks/active-move/category" && r.Method == http.MethodPatch:
			mu.Lock()
			clock = clock.Add(time.Minute)
			for i := range tasks {
				if tasks[i].ID == "active-move" {
					tasks[i].Category = models.CategoryCompleted
					tasks[i].CompletedAt = new(time.Time)
					*tasks[i].CompletedAt = clock
					tasks[i].UpdatedAt = clock
				}
			}
			mu.Unlock()
			_, _ = w.Write([]byte(renderBoard(r)))
		case r.URL.Path == "/browser-add" && r.Method == http.MethodPost:
			mu.Lock()
			clock = clock.Add(time.Minute)
			created := clock
			if r.URL.Query().Get("phase") == "default" {
				for i := range tasks {
					switch tasks[i].ID {
					case "backlog-old":
						tasks[i].Status = models.StatusFailed
					case "completed-old":
						tasks[i].MergeStatus = models.MergeStatusMerged
						tasks[i].GoalMet = true
					case "completed-new":
						tasks[i].GoalMet = true
					}
				}
				tasks = append(tasks,
					models.Task{ID: "backlog-live", ProjectID: project.ID, Title: "Omega Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, CreatedAt: created, UpdatedAt: created, DisplayOrder: 2},
					models.Task{ID: "completed-live", ProjectID: project.ID, Title: "Omega Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base, UpdatedAt: created, CompletedAt: &created, DisplayOrder: 3},
				)
			} else {
				tasks = append(tasks,
					models.Task{ID: "backlog-persisted", ProjectID: project.ID, Title: "Yankee Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, CreatedAt: created, UpdatedAt: created, DisplayOrder: 3},
					models.Task{ID: "completed-persisted", ProjectID: project.ID, Title: "Yankee Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base, UpdatedAt: created, CompletedAt: &created, DisplayOrder: 5},
				)
			}
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "tasks-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "tasks-browser-profile"),
		server.URL+"/tasks?project_id="+project.ID,
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
		requestMu.Lock()
		requests := strings.Join(requestLog, "\n")
		requestMu.Unlock()
		t.Fatalf("Tasks browser regression failed: %s\nRequests:\n%s\nChrome:\n%s", outcome, requests, strings.TrimSpace(string(stderr)))
	}
}

func TestCapacityQueuedAutomationAndTerminalTasksAreVisibleInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-automation-capacity-browser", Name: "Automation Capacity Browser"}
	completedAt := time.Now().UTC()
	var stateMu sync.Mutex
	stage := 0
	liveEvents := make(chan string, 8)
	boardTasks := func() []models.Task {
		stateMu.Lock()
		defer stateMu.Unlock()
		tasks := []models.Task{
			{ID: "automation-future", ProjectID: project.ID, Title: "Future Automation", Category: models.CategoryScheduled, Status: models.StatusPending, CreatedVia: "automation:auto:future"},
			{ID: "ordinary-scheduled", ProjectID: project.ID, Title: "Ordinary Scheduled", Category: models.CategoryScheduled, Status: models.StatusPending},
			{ID: "terminal-cancelled", ProjectID: project.ID, Title: "Cancelled Automation", Category: models.CategoryBacklog, Status: models.StatusCancelled, CreatedVia: "automation:auto:worker"},
		}
		if stage == 1 {
			tasks = append(tasks, models.Task{ID: "automation-capacity", ProjectID: project.ID, Title: "Queued Automation", Category: models.CategoryScheduled, Status: models.StatusPending, AutomationCapacityQueued: true})
		} else if stage == 2 {
			tasks = append(tasks, models.Task{ID: "terminal-failed", ProjectID: project.ID, Title: "Failed Automation", Category: models.CategoryCompleted, Status: models.StatusFailed, CreatedVia: "automation:auto:worker", CompletedAt: &completedAt})
		}
		return tasks
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
        if (performance.now() - started > 8000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    await waitFor(function() { return document.getElementById('kanban-board'); }, 'initial Tasks board');
    if (document.getElementById('task-automation-capacity')) fail('capacity task was visible before its occurrence was queued');
    if (document.getElementById('task-automation-future')) fail('future Automation schedule was incorrectly projected as queued');
    if (document.getElementById('task-ordinary-scheduled')) fail('ordinary scheduled task was incorrectly projected onto the board');

    await fetch('/claim', {method:'POST'});
    await new Promise(function(resolve) { setTimeout(resolve, 700); });
    if (document.getElementById('task-automation-capacity')) fail('foreign-project board event refreshed the selected project');
    await fetch('/publish-claim', {method:'POST'});
    await waitFor(function() { return document.getElementById('task-automation-capacity'); }, 'capacity-queued live projection');
    var pending = document.querySelector('.task-drop-zone[data-status="pending"][data-category="active"]');
    var queued = document.getElementById('task-automation-capacity');
    if (!pending || !pending.contains(queued)) fail('capacity-queued Automation is not in Active pending dropzone');
    if (queued.dataset.taskCategory !== 'scheduled' || queued.dataset.taskStatus !== 'pending') fail('queued Automation card lost its persisted category/status');

    await fetch('/fail', {method:'POST'});
    await waitFor(function() {
      return !document.getElementById('task-automation-capacity') && document.getElementById('task-terminal-failed');
    }, 'terminal failed live projection');
    var completed = document.querySelector('.category-drop-zone[data-category="completed"]');
    var backlog = document.querySelector('.category-drop-zone[data-category="backlog"]');
    if (!completed || !completed.contains(document.getElementById('task-terminal-failed'))) fail('terminal failed Automation is not visible in Completed');
    if (!backlog || !backlog.contains(document.getElementById('task-terminal-cancelled'))) fail('terminal cancelled Automation is not visible in Backlog');
    await report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/claim":
			stateMu.Lock()
			stage = 1
			stateMu.Unlock()
			liveEvents <- `event: task_board_updated
data: {"type":"task_board_updated","project_id":"foreign-project","task_id":"automation-capacity"}

`
			w.WriteHeader(http.StatusNoContent)
		case "/publish-claim":
			liveEvents <- `event: task_board_updated
data: {"type":"task_board_updated","project_id":"` + project.ID + `","task_id":"automation-capacity"}

`
			w.WriteHeader(http.StatusNoContent)
		case "/fail":
			stateMu.Lock()
			stage = 2
			stateMu.Unlock()
			liveEvents <- `event: task_board_updated
data: {"type":"task_board_updated","project_id":"` + project.ID + `","task_id":"automation-capacity"}

`
			w.WriteHeader(http.StatusNoContent)
		case "/events/live":
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("test response writer does not support SSE flushing")
			}
			w.WriteHeader(http.StatusOK)
			flusher.Flush()
			for {
				select {
				case event := <-liveEvents:
					_, _ = w.Write([]byte(event))
					flusher.Flush()
				case <-r.Context().Done():
					return
				}
			}
		case "/tasks":
			var out bytes.Buffer
			if r.Header.Get("HX-Request") != "" {
				if err := components.KanbanBoard(boardTasks(), project.ID, "created_desc", "completed_desc", nil, nil).Render(context.Background(), &out); err != nil {
					t.Fatalf("render Tasks board: %v", err)
				}
				_, _ = w.Write(out.Bytes())
				return
			}
			if err := Tasks([]models.Project{project}, &project, boardTasks(), nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out); err != nil {
				t.Fatalf("render Tasks page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "automation-capacity-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900",
		"--user-data-dir="+filepath.Join(t.TempDir(), "automation-capacity-browser-profile"), server.URL+"/tasks?project_id="+project.ID,
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
		t.Fatalf("Automation capacity browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}

func TestTaskRunningIconSharesThemeAwareSendColorWithoutSuppressingHoverInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var page bytes.Buffer
	if err := layout.Base("Primary action color", nil, "").Render(context.Background(), &page); err != nil {
		t.Fatalf("render base layout: %v", err)
	}

	renderedLayout := page.String()
	headEnd := strings.Index(renderedLayout, "</head>")
	if headEnd < 0 {
		t.Fatal("rendered base layout is missing its head")
	}
	var inlineStyles strings.Builder
	remainingHead := renderedLayout[:headEnd]
	for {
		styleStart := strings.Index(remainingHead, "<style")
		if styleStart < 0 {
			break
		}
		styleEnd := strings.Index(remainingHead[styleStart:], "</style>")
		if styleEnd < 0 {
			t.Fatal("rendered base layout has an unterminated style element")
		}
		styleEnd += styleStart + len("</style>")
		inlineStyles.WriteString(remainingHead[styleStart:styleEnd])
		remainingHead = remainingHead[styleEnd:]
	}
	if inlineStyles.Len() == 0 {
		t.Fatal("rendered base layout is missing inline theme styles")
	}
	importedCSS := `<style>
	[data-color-theme="vscode-test"][data-theme="dark"] { --p: 0.7 0.12 190; }
	[data-color-theme="vscode-test"][data-theme="dark"] .btn-primary:hover { background-color: rgb(4, 5, 6); border-color: rgb(4, 5, 6); }
	</style>`
	fixture := `<button class="btn btn-primary chat-send-button" style="position:fixed;left:20px;top:20px;width:100px;transform:none;transition:none;z-index:2147483647" data-test-send>Send</button><span class="task-state-running" style="position:fixed;left:20px;top:80px;z-index:2147483647" data-test-running>Running</span>`
	html := `<!doctype html><html data-theme="dark" data-color-theme="openvibely-dark"><head><meta charset="utf-8">` + inlineStyles.String() + importedCSS + `</head><body>` + fixture + `</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	defer server.Close()

	debugListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Chrome debugging port: %v", err)
	}
	debugPort := debugListener.Addr().(*net.TCPAddr).Port
	_ = debugListener.Close()

	stderrPath := filepath.Join(t.TempDir(), "primary-action-color-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--no-first-run", "--no-default-browser-check",
		fmt.Sprintf("--remote-debugging-port=%d", debugPort),
		"--user-data-dir="+filepath.Join(t.TempDir(), "primary-action-color-browser-profile"), server.URL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	defer stopBrowserProcess(cmd)

	type debugTarget struct {
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	var target debugTarget
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, requestErr := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", debugPort))
		if requestErr == nil {
			var targets []debugTarget
			decodeErr := json.NewDecoder(resp.Body).Decode(&targets)
			_ = resp.Body.Close()
			if decodeErr == nil {
				for _, candidate := range targets {
					if strings.TrimRight(candidate.URL, "/") == strings.TrimRight(server.URL, "/") && candidate.WebSocketDebuggerURL != "" {
						target = candidate
						break
					}
				}
			}
		}
		if target.WebSocketDebuggerURL != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if target.WebSocketDebuggerURL == "" {
		t.Fatalf("find Chrome debugging target for %s", server.URL)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, target.WebSocketDebuggerURL, nil)
	if err != nil {
		t.Fatalf("connect to Chrome debugging target: %v", err)
	}
	defer conn.CloseNow()

	nextID := 0
	call := func(tb testing.TB, method string, params any, result any) {
		tb.Helper()
		nextID++
		request, marshalErr := json.Marshal(map[string]any{"id": nextID, "method": method, "params": params})
		if marshalErr != nil {
			tb.Fatalf("marshal CDP %s request: %v", method, marshalErr)
		}
		if writeErr := conn.Write(ctx, websocket.MessageText, request); writeErr != nil {
			tb.Fatalf("write CDP %s request: %v", method, writeErr)
		}
		for {
			_, message, readErr := conn.Read(ctx)
			if readErr != nil {
				tb.Fatalf("read CDP %s response: %v", method, readErr)
			}
			var response struct {
				ID     int             `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if unmarshalErr := json.Unmarshal(message, &response); unmarshalErr != nil || response.ID != nextID {
				continue
			}
			if len(response.Error) > 0 {
				tb.Fatalf("CDP %s error: %s", method, response.Error)
			}
			if result != nil && len(response.Result) > 0 {
				if unmarshalErr := json.Unmarshal(response.Result, result); unmarshalErr != nil {
					tb.Fatalf("decode CDP %s result: %v", method, unmarshalErr)
				}
			}
			return
		}
	}

	evaluate := func(tb testing.TB, expression string) string {
		tb.Helper()
		var response struct {
			Result struct {
				Type        string          `json:"type"`
				Value       json.RawMessage `json:"value"`
				Description string          `json:"description"`
			} `json:"result"`
			ExceptionDetails json.RawMessage `json:"exceptionDetails"`
		}
		call(tb, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &response)
		if len(response.ExceptionDetails) > 0 {
			tb.Fatalf("evaluate JavaScript %q: %s", expression, response.ExceptionDetails)
		}
		if response.Result.Type != "string" || len(response.Result.Value) == 0 {
			tb.Fatalf("evaluate JavaScript %q returned type %q without a string value: %s", expression, response.Result.Type, response.Result.Description)
		}
		var value string
		if err := json.Unmarshal(response.Result.Value, &value); err != nil {
			tb.Fatalf("decode JavaScript result for %q: %v", expression, err)
		}
		return value
	}
	movePointer := func(tb testing.TB, x, y float64) {
		tb.Helper()
		call(tb, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y}, nil)
	}

	pageReadyDeadline := time.Now().Add(10 * time.Second)
	pageState := ""
	for time.Now().Before(pageReadyDeadline) {
		pageState = evaluate(t, `document.readyState + ':' + Boolean(document.querySelector('[data-test-send]')) + ':' + Boolean(document.querySelector('[data-test-running]'))`)
		if pageState == "complete:true:true" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if pageState != "complete:true:true" {
		t.Fatalf("Chrome page did not finish loading the primary-action fixture; final state %q", pageState)
	}
	type themeCase struct {
		name      string
		mode      string
		id        string
		wantHover string
	}
	for _, tc := range []themeCase{
		{name: "native dark", mode: "dark", id: "openvibely-dark", wantHover: "rgb(100, 111, 228)"},
		{name: "native light", mode: "light", id: "openvibely-light", wantHover: "rgb(79, 60, 184)"},
		{name: "imported", mode: "dark", id: "vscode-test", wantHover: "rgb(4, 5, 6)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			movePointer(t, 1000, 1000)
			evaluate(t, fmt.Sprintf(`document.documentElement.setAttribute('data-theme', %q); document.documentElement.setAttribute('data-color-theme', %q); ''`, tc.mode, tc.id))
			time.Sleep(400 * time.Millisecond)
			var normal struct {
				Send    string     `json:"send"`
				Running string     `json:"running"`
				Center  [2]float64 `json:"center"`
			}
			if err := json.Unmarshal([]byte(evaluate(t, `JSON.stringify((function(){var b=document.querySelector('[data-test-send]'),r=b.getBoundingClientRect();return {send:getComputedStyle(b).backgroundColor,running:getComputedStyle(document.querySelector('[data-test-running]')).color,center:[r.left+r.width/2,r.top+r.height/2]};})())`)), &normal); err != nil {
				t.Fatalf("decode normal colors: %v", err)
			}
			if normal.Send != normal.Running {
				t.Fatalf("normal Send/running colors differ: %s vs %s", normal.Send, normal.Running)
			}

			movePointer(t, normal.Center[0], normal.Center[1])
			time.Sleep(400 * time.Millisecond)
			var hovered struct {
				Send    string `json:"send"`
				Running string `json:"running"`
				Hovered bool   `json:"hovered"`
				Hit     string `json:"hit"`
			}
			if err := json.Unmarshal([]byte(evaluate(t, fmt.Sprintf(`JSON.stringify({send:getComputedStyle(document.querySelector('[data-test-send]')).backgroundColor,running:getComputedStyle(document.querySelector('[data-test-running]')).color,hovered:document.querySelector('[data-test-send]').matches(':hover'),hit:(document.elementFromPoint(%f,%f)||{}).outerHTML||''})`, normal.Center[0], normal.Center[1]))), &hovered); err != nil {
				t.Fatalf("decode hovered colors: %v", err)
			}
			if !hovered.Hovered {
				t.Fatalf("Chrome pointer did not activate the Send button :hover state at %.1f,%.1f; hit %s", normal.Center[0], normal.Center[1], hovered.Hit)
			}
			if hovered.Send != tc.wantHover {
				t.Fatalf("hover Send color = %s, want %s", hovered.Send, tc.wantHover)
			}
			if hovered.Send == normal.Send {
				t.Fatalf("hover did not change Send color from %s", normal.Send)
			}
			if hovered.Running != normal.Running {
				t.Fatalf("hover changed running icon color from %s to %s", normal.Running, hovered.Running)
			}
		})
	}
}

func TestTaskCardStateIconStaysVisibleWithLongTitleAtMobileWidthInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := models.Task{
		ID:          "mobile-merged",
		ProjectID:   "default",
		Title:       strings.Repeat("LongUnbrokenTaskTitle", 12),
		Category:    models.CategoryCompleted,
		Status:      models.StatusCompleted,
		MergeStatus: models.MergeStatusMerged,
	}
	var card bytes.Buffer
	if err := components.TaskCard(task, "default", "completed", nil, nil).Render(context.Background(), &card); err != nil {
		t.Fatalf("render task card: %v", err)
	}

	fixture := `<!doctype html><html><head><meta name="viewport" content="width=device-width,initial-scale=1"><style>
	* { box-sizing: border-box; }
	body { margin: 0; padding: 8px; width: 100%; overflow-x: hidden; font: 16px/1.375 sans-serif; }
	.card { position: relative; width: 100%; max-width: 100%; border: 1px solid #bbb; }
	.card-body { padding: 16px; padding-top: 56px; }
	.flex { display: flex; }
	.inline-flex { display: inline-flex; }
	.items-start { align-items: flex-start; }
	.items-center { align-items: center; }
	.justify-center { justify-content: center; }
	.flex-1 { flex: 1 1 0%; }
	.min-w-0 { min-width: 0; }
	.max-w-full { max-width: 100%; }
	.shrink-0 { flex-shrink: 0; }
	.gap-2 { gap: 8px; }
	.h-5 { height: 20px; }
	.w-5 { width: 20px; }
	.h-4 { height: 16px; }
	.w-4 { width: 16px; }
	.break-words { overflow-wrap: anywhere; word-break: break-word; }
	.absolute { position: absolute; }
	.dropdown, button { display: none; }
	</style></head><body>` + card.String() + `<script>
	(function() {
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
	  try {
	    var card = document.getElementById('task-mobile-merged');
	    var link = card && card.querySelector('a[title]');
	    var icon = card && card.querySelector('[data-task-state-icon]');
	    var title = card && card.querySelector('[data-task-title]');
	    if (!card || !link || !icon || !title) throw new Error('missing mobile task title state markup');
	    var cardRect = card.getBoundingClientRect(), iconRect = icon.getBoundingClientRect(), titleRect = title.getBoundingClientRect();
	    if (icon.dataset.taskState !== 'merged') throw new Error('merged state icon was not preserved');
	    if (icon.nextElementSibling !== title) throw new Error('state icon is not immediately before mobile title');
	    if (Math.abs(iconRect.width - 20) > 0.5 || Math.abs(iconRect.height - 20) > 0.5) throw new Error('state icon shrank at mobile width: ' + iconRect.width + 'x' + iconRect.height);
	    if (iconRect.left >= titleRect.left) throw new Error('state icon is not positioned before title');
	    if (titleRect.height <= 30) throw new Error('long mobile title did not wrap');
	    if (card.scrollWidth > card.clientWidth + 1 || titleRect.right > cardRect.right + 1) throw new Error('long mobile title overflowed task card');
	    report('pass', '');
	  } catch (error) { report('fail', String(error && error.stack || error)); }
	})();
	</script></body></html>`

	browserResult := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(fixture))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "task-state-mobile-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=390,700", "--user-data-dir="+filepath.Join(t.TempDir(), "task-state-mobile-browser-profile"),
		server.URL,
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
		t.Fatalf("Task state mobile browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}

func TestTasksRendersDirectTaskCardMergeActionsWithoutConfirmation(t *testing.T) {
	project := models.Project{ID: "project-merge", Name: "Merge Project"}
	var out bytes.Buffer
	if err := Tasks([]models.Project{project}, &project, nil, nil, nil, "", "").Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, forbidden := range []string{
		`id="task_card_merge_confirm_modal"`,
		`id="task_card_merge_confirm_button"`,
		`id="task_card_merge_error"`,
		`openTaskCardMergeConfirm`,
		`confirmTaskCardMerge`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("direct task-card actions must omit confirmation contract %q", forbidden)
		}
	}
	for _, want := range []string{
		`data-new-task-form`,
		`closest('form[data-new-task-form]')`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected scoped new-task error contract %q", want)
		}
	}
}

func TestTaskCardActionsOwnDirectRequestMetadata(t *testing.T) {
	task := models.Task{ID: "task-direct", ProjectID: "project-merge", Title: "Direct", WorktreeBranch: "task/direct", MergeTargetBranch: "main"}
	var out bytes.Buffer
	if err := components.TaskCardMergeOptions(&task, task.ProjectID, true, true, nil, true).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, forbidden := range []string{"openTaskCardMergeConfirm", "confirmTaskCardMerge"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("direct card action must omit confirmation hook %q", forbidden)
		}
	}
	for _, required := range []string{
		`onclick="runTaskCardAction(this)"`,
		`data-merge-endpoint="merge"`,
		`data-merge-endpoint="rebase"`,
		`data-merge-endpoint="pull-request"`,
		`data-merge-type="merge"`,
		`data-merge-type="pr"`,
		`data-project-id="project-merge"`,
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("direct task-card action contract missing %q: %s", required, body)
		}
	}
}

func TestTaskCardMergeMenuDirectActionConflictRetryAndBoardRefreshInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	project := models.Project{ID: "project-card-merge-browser", Name: "Card Merge Browser"}
	task := models.Task{ID: "merge-browser-task", ProjectID: project.ID, Title: "Merge Browser Task", Category: models.CategoryCompleted, Status: models.StatusCompleted, WorktreeBranch: "task/merge-browser", MergeTargetBranch: "main", MergeStatus: models.MergeStatusPending}
	var posts int
	var optionGets int
	var dirtyWorktree bool
	var lockedWorktree bool
	var delayNextBoardRefresh bool
	var boardRefreshStarted bool
	var releaseBoardRefresh = make(chan struct{}, 1)
	var mu sync.Mutex
	result := make(chan string, 2)
	fixtureCSS := `<style>
	#kanban-board{display:grid;grid-template-columns:1fr;height:420px;overflow-y:auto}.kanban-column{min-width:0}.card{position:relative;min-height:120px}.dropdown{position:relative}.card>.dropdown{position:absolute}.dropdown-content{display:none;position:absolute;right:0;top:100%;width:210px;background:white;border:1px solid #333;z-index:100}.dropdown:focus-within>.dropdown-content{display:block}.modal{display:none}.modal[open]{display:grid;position:fixed;inset:0;z-index:999;background:rgba(0,0,0,.2)}.modal-box{margin:auto;background:white;padding:16px;max-width:340px}.hidden{display:none!important}.task-selected{outline:3px solid blue}.btn{min-height:32px}</style>`
	runner := `<script>window.addEventListener('DOMContentLoaded',function(){
	function report(s,m){return fetch('/browser-result?status='+encodeURIComponent(s)+'&message='+encodeURIComponent(m||''),{method:'POST'})}function fail(m){throw new Error(m)}function waitFor(fn,label){return new Promise(function(resolve,reject){var end=Date.now()+4000;(function poll(){if(fn())return resolve();if(Date.now()>end)return reject(new Error('timeout '+label));setTimeout(poll,20)})()})}function waitForText(path,value,label){return new Promise(function(resolve,reject){var end=Date.now()+4000;(function poll(){fetch(path).then(function(r){return r.text()}).then(function(text){if(text.trim()===value)return resolve();if(Date.now()>end)return reject(new Error('timeout '+label));setTimeout(poll,20)}).catch(reject)})()})}function frame(){return new Promise(function(r){requestAnimationFrame(function(){requestAnimationFrame(r)})})}function clickTrigger(el){el.dispatchEvent(new MouseEvent('mousedown',{bubbles:true,cancelable:true,detail:1}));el.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true,detail:1}))}
		(async function(){await frame();var card=document.getElementById('task-merge-browser-task');handleTaskSelect({currentTarget:card,target:card,metaKey:true,ctrlKey:false,preventDefault:function(){},stopPropagation:function(){}});if(!card.classList.contains('task-selected'))fail('card selection was not established');var trigger=card.querySelector('[data-task-card-menu-trigger]');clickTrigger(trigger);await waitFor(function(){return card.querySelector('[data-task-card-merge-action]')},'preloaded merge options');clickTrigger(trigger);await fetch('/arm-delayed-board-refresh',{method:'POST'});var crossedRefreshFinished=false;function handleCrossedRefresh(event){var config=event.detail&&event.detail.requestConfig;if(config&&config.path.indexOf('/tasks?project_id=project-card-merge-browser')===0)crossedRefreshFinished=true}document.body.addEventListener('htmx:afterRequest',handleCrossedRefresh);window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_updated',project_id:'project-card-merge-browser'}}));await waitForText('/board-refresh-started','true','background board refresh start');var optionsBeforeCrossedMenuOpen=card.querySelector('[data-task-card-merge-options]');clickTrigger(trigger);await waitFor(function(){return card.querySelector('[data-task-card-merge-options]')!==optionsBeforeCrossedMenuOpen},'merge options after aborting crossed board refresh');var boardBeforeCrossedRefresh=document.getElementById('kanban-board'),optionsBeforeCrossedRefresh=card.querySelector('[data-task-card-merge-options]');if(!optionsBeforeCrossedRefresh.querySelector('[data-task-card-merge-action]'))fail('refreshed merge options missing during in-flight board refresh');await fetch('/release-board-refresh',{method:'POST'});await waitFor(function(){return crossedRefreshFinished},'crossed background board refresh completion');if(document.getElementById('kanban-board')!==boardBeforeCrossedRefresh)fail('in-flight SSE refresh replaced board after task menu opened');if(card.querySelector('[data-task-card-merge-options]')!==optionsBeforeCrossedRefresh)fail('in-flight SSE refresh replaced options after task menu opened');document.body.removeEventListener('htmx:afterRequest',handleCrossedRefresh);clickTrigger(trigger);await waitFor(function(){return document.getElementById('kanban-board')!==boardBeforeCrossedRefresh},'crossed deferred board refresh after menu close');card=document.getElementById('task-merge-browser-task');trigger=card.querySelector('[data-task-card-menu-trigger]');clickTrigger(trigger);await waitFor(function(){return card.querySelector('[data-task-card-merge-action]')},'merge options');var options=card.querySelector('[data-task-card-merge-options]');if(options.querySelector('details')||options.querySelector('summary'))fail('merge options rendered an expand/collapse control');var mergeRows=Array.from(options.querySelectorAll('[data-task-card-merge-action]'));if(mergeRows.length<3||mergeRows.some(function(row){return row.parentElement!==options}))fail('merge actions were not rendered as flat menu rows');var pr=card.querySelector('[data-task-card-pr-action]');if(!pr)fail('create PR action was not rendered');if(document.getElementById('task_card_merge_confirm_modal'))fail('task-card confirmation modal is still rendered');if(pr.getAttribute('onclick')!=='runTaskCardAction(this)')fail('create PR is not a direct action');var boardBeforeDeferredRefresh=document.getElementById('kanban-board'),optionsBeforeDeferredRefresh=card.querySelector('[data-task-card-merge-options]');window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_updated',project_id:'project-card-merge-browser'}}));await new Promise(function(resolve){setTimeout(resolve,700)});if(document.getElementById('kanban-board')!==boardBeforeDeferredRefresh)fail('SSE replaced board while task menu was open');if(card.querySelector('[data-task-card-merge-options]')!==optionsBeforeDeferredRefresh)fail('SSE refreshed merge options while task menu was open');clickTrigger(trigger);await waitFor(function(){return document.getElementById('kanban-board')!==boardBeforeDeferredRefresh},'deferred board refresh after menu close');card=document.getElementById('task-merge-browser-task');trigger=card.querySelector('[data-task-card-menu-trigger]');clickTrigger(trigger);await waitFor(function(){return card.querySelector('[data-task-card-merge-action]')},'merge options after deferred board refresh');var menu=card.querySelector('.dropdown-content'),rect=menu.getBoundingClientRect();if(rect.left<0||rect.right>window.innerWidth+1)fail('responsive menu escaped viewport '+JSON.stringify({left:rect.left,right:rect.right,width:window.innerWidth}));var firstOptions=card.querySelector('[data-task-card-merge-options]');clickTrigger(trigger);if(trigger.getAttribute('aria-expanded')!=='false')fail('trigger did not dismiss menu');clickTrigger(trigger);await waitFor(function(){return card.querySelector('[data-task-card-merge-options]')!==firstOptions},'fresh options on reopen');var optionCount=parseInt(await fetch('/option-count').then(function(r){return r.text()}),10);if(optionCount<2)fail('menu reopen did not refresh options, gets='+optionCount);var rebase=card.querySelector('[data-merge-type="rebase"]');if(!rebase)fail('rebase option missing before fallback test');rebase.focus();if(trigger.getAttribute('aria-expanded')!=='true'||!trigger.closest('[data-kanban-menu-key]').hasAttribute('data-kanban-menu-open'))fail('focused menu option was not captured');var refresher=document.createElement('button');refresher.setAttribute('hx-get','/board-refresh');refresher.setAttribute('hx-trigger','refresh');refresher.setAttribute('hx-target','#kanban-board');refresher.setAttribute('hx-swap','outerHTML');document.body.appendChild(refresher);htmx.process(refresher);var oldBoard=document.getElementById('kanban-board');htmx.trigger(refresher,'refresh');await waitFor(function(){return document.getElementById('kanban-board')!==oldBoard},'board replacement retaining focused option');await waitFor(function(){var live=document.getElementById('task-merge-browser-task'),liveRebase=live&&live.querySelector('[data-merge-type="rebase"]');return liveRebase&&document.activeElement===liveRebase},'focused option restoration');await fetch('/dirty-worktree',{method:'POST'});oldBoard=document.getElementById('kanban-board');htmx.trigger(refresher,'refresh');await waitFor(function(){return document.getElementById('kanban-board')!==oldBoard},'board replacement for focused option');await frame();var restoredCard=document.getElementById('task-merge-browser-task'),restoredTrigger=restoredCard&&restoredCard.querySelector('[data-task-card-menu-trigger]');if(!restoredTrigger||restoredTrigger.getAttribute('aria-expanded')!=='true')fail('open menu restore state');await waitFor(function(){var live=document.getElementById('task-merge-browser-task');return live&&live.querySelector('[data-task-card-merge-action]')&&!live.querySelector('[data-merge-type="rebase"]')},'refreshed options without disappeared action');var fallbackCard=document.getElementById('task-merge-browser-task'),fallbackTrigger=fallbackCard.querySelector('[data-task-card-menu-trigger]');if(document.activeElement!==fallbackTrigger)fail('disappeared option did not fall back to trigger, active='+document.activeElement.outerHTML);await fetch('/lock-worktree',{method:'POST'});var preLockOptions=fallbackCard.querySelector('[data-task-card-merge-options]');clickTrigger(fallbackTrigger);clickTrigger(fallbackTrigger);await waitFor(function(){var live=fallbackCard.querySelector('[data-task-card-merge-options]');return live!==preLockOptions&&!live.textContent.includes('unavailable')&&!live.querySelector('[data-task-card-merge-action]')&&!live.querySelector('[data-task-card-pr-action]')},'locked ineligible refresh');await fetch('/clean-worktree',{method:'POST'});var lockedOptions=fallbackCard.querySelector('[data-task-card-merge-options]');clickTrigger(fallbackTrigger);clickTrigger(fallbackTrigger);await waitFor(function(){var live=fallbackCard.querySelector('[data-task-card-merge-options]');return live!==lockedOptions&&live.querySelector('[data-task-card-merge-action]')},'clean retry refresh');card=document.getElementById('task-merge-browser-task');trigger=card.querySelector('[data-task-card-menu-trigger]');var merge=card.querySelector('[data-merge-type="merge"]');merge.focus();merge.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));if(document.activeElement!==trigger||trigger.getAttribute('aria-expanded')!=='false')fail('Escape did not dismiss menu to trigger');var escapedOptions=card.querySelector('[data-task-card-merge-options]');clickTrigger(trigger);await waitFor(function(){return card.querySelector('[data-task-card-merge-options]')!==escapedOptions},'options after Escape reopen');merge=card.querySelector('[data-merge-type="merge"]');var mergeMenu=merge.closest('.dropdown-content');var mergeMenuRect=mergeMenu.getBoundingClientRect();var oldBoardForMerge=document.getElementById('kanban-board');merge.click();if(trigger.getAttribute('aria-expanded')!=='false'||getComputedStyle(mergeMenu).display!=='none')fail('direct merge did not close menu immediately');if(merge.disabled)fail('direct merge changed menu row geometry by disabling the action');merge.click();await new Promise(function(resolve){setTimeout(resolve,300)});var count=await fetch('/post-count').then(function(r){return r.text()});if(count.trim()!=='1')fail('duplicate direct merge was not blocked, posts='+count);if(mergeMenuRect.width<=0||mergeMenuRect.height<=0)fail('merge menu had no stable pre-action geometry');if(document.getElementById('new_task_modal').open)fail('merge conflict opened New Task modal');if(!document.getElementById('title-error').classList.contains('hidden'))fail('merge conflict populated New Task title error');merge.click();await waitFor(function(){return document.getElementById('kanban-board')!==oldBoardForMerge},'successful direct retry');await waitFor(function(){var restored=document.getElementById('task-merge-browser-task');return restored&&restored.classList.contains('task-selected')},'settled selection restoration');var next=document.getElementById('task-merge-browser-task');if(!next)fail('authoritative board replacement lost card');if(!next.classList.contains('task-selected'))fail('selection was not restored after board replacement');var nextTrigger=next.querySelector('[data-task-card-menu-trigger]');if(document.activeElement!==nextTrigger)fail('focus was not restored to replacement card trigger');if(nextTrigger.getAttribute('aria-expanded')!=='false')fail('replacement card menu remained open after success');var mergedOptions=next.querySelector('[data-task-card-merge-options]');clickTrigger(nextTrigger);await waitFor(function(){var live=next.querySelector('[data-task-card-merge-options]');return live&&live!==mergedOptions&&!live.textContent.includes('Merge unavailable')&&!live.querySelector('[data-task-card-merge-action]')},'stale eligibility refresh on reopen');var visibleMenu=next.querySelector('.dropdown-content');document.getElementById('kanban-board').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,cancelable:true}));if(nextTrigger.getAttribute('aria-expanded')!=='false'||getComputedStyle(visibleMenu).display!=='none')fail('outside activity left task menu visibly open');clickTrigger(nextTrigger);await waitFor(function(){return nextTrigger.getAttribute('aria-expanded')==='true'},'menu reopen before history restore');document.dispatchEvent(new CustomEvent('htmx:historyRestore'));if(nextTrigger.getAttribute('aria-expanded')!=='false'||getComputedStyle(visibleMenu).display!=='none')fail('history restoration left task menu visibly open');clickTrigger(nextTrigger);var edit=next.querySelector('.dropdown-content a[hx-get]');if(!edit)fail('missing in-menu navigation action');edit.click();await waitFor(function(){return nextTrigger.getAttribute('aria-expanded')==='false'&&!nextTrigger.closest('[data-kanban-menu-key]').hasAttribute('data-kanban-menu-open')},'navigation dismissal');count=await fetch('/post-count').then(function(r){return r.text()});if(count.trim()!=='2')fail('retry did not issue exactly one additional request, posts='+count);await report('pass','')})().catch(function(e){report('fail',String(e&&e.stack||e))})});</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmxJS)
		case "/tasks":
			if r.Header.Get("HX-Request") == "true" {
				mu.Lock()
				delay := delayNextBoardRefresh
				if delay {
					delayNextBoardRefresh = false
					boardRefreshStarted = true
				}
				mu.Unlock()
				if delay {
					select {
					case <-releaseBoardRefresh:
					case <-r.Context().Done():
						return
					}
				}
				_ = components.KanbanBoard([]models.Task{task}, project.ID, "", "", nil, nil).Render(r.Context(), w)
				return
			}
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, []models.Task{task}, nil, nil, "", "").Render(context.Background(), &out); err != nil {
				t.Fatal(err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", fixtureCSS+runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/arm-delayed-board-refresh":
			mu.Lock()
			delayNextBoardRefresh = true
			boardRefreshStarted = false
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/board-refresh-started":
			mu.Lock()
			started := boardRefreshStarted
			mu.Unlock()
			_, _ = fmt.Fprintf(w, "%t", started)
		case "/release-board-refresh":
			select {
			case releaseBoardRefresh <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/merge-browser-task/card/merge-options":
			mu.Lock()
			optionGets++
			rebaseAvailable := !dirtyWorktree && !lockedWorktree
			locked := lockedWorktree
			mu.Unlock()
			eligible := task.MergeStatus != models.MergeStatusMerged && !locked
			_ = components.TaskCardMergeOptions(&task, project.ID, eligible, rebaseAvailable, nil, !locked).Render(r.Context(), w)
		case "/dirty-worktree":
			mu.Lock()
			dirtyWorktree = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/lock-worktree":
			mu.Lock()
			lockedWorktree = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/clean-worktree":
			mu.Lock()
			dirtyWorktree = false
			lockedWorktree = false
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case "/option-count":
			mu.Lock()
			defer mu.Unlock()
			_, _ = fmt.Fprintf(w, "%d", optionGets)
		case "/board-refresh", "/navigate", "/tasks/merge-browser-task":
			_ = components.KanbanBoard([]models.Task{task}, project.ID, "", "", nil, nil).Render(r.Context(), w)
		case "/tasks/merge-browser-task/worktree/merge":
			mu.Lock()
			posts++
			n := posts
			mu.Unlock()
			if n == 1 {
				time.Sleep(150 * time.Millisecond)
				w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Local merge has conflicts. Resolve conflicts or abort merge.","type":"failed"}}`)
				http.Error(w, "Local merge has conflicts. Resolve conflicts or abort merge.", http.StatusConflict)
				return
			}
			task.MergeStatus = models.MergeStatusMerged
			_ = components.KanbanBoard([]models.Task{task}, project.ID, "", "", nil, nil).Render(r.Context(), w)
		case "/post-count":
			mu.Lock()
			defer mu.Unlock()
			_, _ = fmt.Fprintf(w, "%d", posts)
		case "/browser-result":
			result <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer func() {
		select {
		case releaseBoardRefresh <- struct{}{}:
		default:
		}
	}()
	stderrPath := filepath.Join(t.TempDir(), "task-card-merge-browser.stderr")
	stderr, _ := os.Create(stderrPath)
	defer stderr.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--disable-background-networking", "--window-size=390,760", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/tasks?project_id="+project.ID)
	cmd.Stderr = stderr
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	var outcome string
	select {
	case outcome = <-result:
	case <-time.After(15 * time.Second):
		outcome = "fail:timeout"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		data, _ := os.ReadFile(stderrPath)
		mu.Lock()
		state := fmt.Sprintf("option_gets=%d posts=%d board_refresh_started=%t", optionGets, posts, boardRefreshStarted)
		mu.Unlock()
		t.Fatalf("task card merge browser regression failed: %s (%s)\n%s", outcome, state, data)
	}
}

func TestTaskCardKebabMenuEscapesCardAndRepositionsAtDropZoneBottomInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-task-menu-browser", Name: "Task Menu Browser"}
	base := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	tasks := make([]models.Task, 0, 7)
	for i := 0; i < 7; i++ {
		id := "edge-task-" + strconv.Itoa(i)
		title := "Edge Task " + strconv.Itoa(i)
		if i == 6 {
			id = "edge-last"
			title = "Bottom Edge Task"
		}
		tasks = append(tasks, models.Task{
			ID:           id,
			ProjectID:    project.ID,
			Title:        title,
			Category:     models.CategoryBacklog,
			Status:       models.StatusPending,
			CreatedAt:    base.Add(time.Duration(i) * time.Minute),
			UpdatedAt:    base.Add(time.Duration(i) * time.Minute),
			DisplayOrder: i,
		})
	}

	fixtureCSS := `<style>
	#kanban-board { display: grid; grid-template-columns: repeat(3, minmax(0, 260px)); gap: 16px; height: 300px; overflow: hidden; align-items: stretch; }
	.kanban-column { min-width: 0; height: 300px; display: flex; flex-direction: column; padding: 8px; border: 1px solid #ddd; border-radius: 8px; }
	.category-drop-zone, .task-drop-zone { position: relative; min-height: 0; flex: 1 1 auto; overflow-y: auto; padding: 4px; border: 1px dashed transparent; }
	.card { position: relative; height: 92px; margin-bottom: 8px; background: white; border: 1px solid #bbb; border-radius: 8px; }
	.card.overflow-visible, .overflow-visible { overflow: visible !important; }
	.card.overflow-hidden, .overflow-hidden { overflow: hidden !important; }
	.card-body { padding: 12px; padding-top: 48px; }
	.dropdown { position: relative; display: inline-block; }
	.card > .dropdown { position: absolute; top: 8px; right: 32px; z-index: 30; }
	.dropdown-content { display: none; position: absolute; top: 100%; right: 0; width: 128px; height: 120px; padding: 4px; background: white; border: 1px solid #333; border-radius: 6px; z-index: 100; }
	.dropdown:focus-within > .dropdown-content { display: block; }
	.dropdown.dropdown-top > .dropdown-content { top: auto; bottom: 100%; }
	.btn { display: inline-flex; align-items: center; justify-content: center; width: 24px; height: 24px; }
	</style>`
	runner := `<script>
	window.addEventListener('DOMContentLoaded', function() {
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
	  function fail(message) { throw new Error(message); }
	  function frame() { return new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); }); }
	  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
	  (async function() {
	    var card = document.getElementById('task-edge-last');
	    if (!card) fail('missing bottom-edge task card');
	    var zone = card.closest('.category-drop-zone');
	    if (!zone) fail('missing task drop zone');
	    zone.scrollTop = zone.scrollHeight;
	    await frame();
	    var dropdown = card.querySelector('.dropdown');
	    var label = dropdown && dropdown.querySelector('label');
	    var menu = dropdown && dropdown.querySelector('.dropdown-content');
	    if (!dropdown || !label || !menu) fail('missing task card dropdown controls');
	    if (!card.classList.contains('overflow-visible')) fail('task card root does not opt out of overflow clipping');
	    label.focus();
	    label.click();
	    await frame();
	    var zoneRect = zone.getBoundingClientRect();
	    var cardRect = card.getBoundingClientRect();
	    var menuRect = menu.getBoundingClientRect();
	    var visibleBottom = Math.min(window.innerHeight, zoneRect.bottom);
	    if (!dropdown.classList.contains('dropdown-top')) fail('bottom-edge dropdown did not switch to dropdown-top');
	    if (menuRect.bottom > visibleBottom + 1) fail('menu bottom is clipped by visible scroll boundary: menu=' + JSON.stringify({top:menuRect.top,bottom:menuRect.bottom}) + ' zone=' + JSON.stringify({top:zoneRect.top,bottom:zoneRect.bottom}));
	    if (menuRect.top >= cardRect.top) fail('menu did not render outside the card above its top edge');
	    var hitY = Math.max(menuRect.top + 8, Math.min(menuRect.bottom - 8, cardRect.top - 8));
	    var hit = document.elementFromPoint(menuRect.left + 12, hitY);
	    if (!hit || !menu.contains(hit)) fail('menu is not hit-testable outside the card bounds; hit=' + (hit && hit.outerHTML));
	    await report('pass', '');
	  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
	});
	</script>`

	browserResult := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/tasks":
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_asc", "completed_desc").Render(context.Background(), &out); err != nil {
				t.Fatalf("render Tasks page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", fixtureCSS+runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "tasks-menu-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=900,520", "--user-data-dir="+filepath.Join(t.TempDir(), "tasks-menu-browser-profile"),
		server.URL+"/tasks?project_id="+project.ID,
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
		t.Fatalf("Task card menu browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
func TestTaskBoardDeleteAllConfirmationFlowInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-task-delete-modal", Name: "Task Delete Modal"}
	otherProject := models.Project{ID: "project-task-delete-foreign", Name: "Foreign Project"}
	base := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	initialTasks := []models.Task{
		{ID: "completed-one", ProjectID: project.ID, Title: "Completed One", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base, UpdatedAt: base},
		{ID: "completed-two", ProjectID: project.ID, Title: "Completed Two", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
		{ID: "backlog-one", ProjectID: project.ID, Title: "Backlog One", Category: models.CategoryBacklog, Status: models.StatusPending, CreatedAt: base, UpdatedAt: base},
		{ID: "foreign-completed", ProjectID: otherProject.ID, Title: "Foreign Completed", Category: models.CategoryCompleted, Status: models.StatusCompleted, CreatedAt: base, UpdatedAt: base},
		{ID: "foreign-backlog", ProjectID: otherProject.ID, Title: "Foreign Backlog", Category: models.CategoryBacklog, Status: models.StatusPending, CreatedAt: base, UpdatedAt: base},
	}
	var mu sync.Mutex
	tasks := append([]models.Task(nil), initialTasks...)
	failNextCompletedDelete := true
	deleteRequests := 0

	projectTasks := func() []models.Task {
		mu.Lock()
		defer mu.Unlock()
		out := make([]models.Task, 0, len(tasks))
		for _, task := range tasks {
			if task.ProjectID == project.ID {
				out = append(out, task)
			}
		}
		return out
	}
	renderBoard := func() string {
		var out bytes.Buffer
		if err := components.KanbanBoard(projectTasks(), project.ID, "created_desc", "completed_desc", nil, nil).Render(context.Background(), &out); err != nil {
			t.Fatalf("render delete-all board: %v", err)
		}
		return out.String()
	}
	renderPage := func() string {
		var out bytes.Buffer
		if err := Tasks([]models.Project{project}, &project, projectTasks(), nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out); err != nil {
			t.Fatalf("render delete-all page: %v", err)
		}
		return out.String()
	}
	state := func() string {
		mu.Lock()
		defer mu.Unlock()
		counts := map[string]int{}
		for _, task := range tasks {
			key := task.ProjectID + "-" + string(task.Category)
			counts[key]++
		}
		return "project-completed=" + strconv.Itoa(counts[project.ID+"-completed"]) +
			";project-backlog=" + strconv.Itoa(counts[project.ID+"-backlog"]) +
			";foreign-completed=" + strconv.Itoa(counts[otherProject.ID+"-completed"]) +
			";foreign-backlog=" + strconv.Itoa(counts[otherProject.ID+"-backlog"]) +
			";requests=" + strconv.Itoa(deleteRequests)
	}

	fixtureCSS := `<style>
.dropdown { position: relative; }
.dropdown-content { display: none; }
.dropdown:focus-within > .dropdown-content { display: block; }
dialog:not([open]) { display: none; }
.modal-action { display: flex; gap: 8px; }
</style>`
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > 8000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function currentState() { return fetch('/browser-state').then(function(response) { return response.text(); }); }
  function action(category) {
    return document.querySelector('button[data-delete-all-tasks-category="' + category + '"]');
  }
  function openAction(category) {
    var button = action(category);
    if (!button) fail('missing ' + category + ' Delete All action');
    var dropdown = button.closest('.dropdown');
    var menuTrigger = dropdown && dropdown.querySelector('label');
    if (!menuTrigger) fail('missing ' + category + ' dropzone menu trigger');
    menuTrigger.focus();
    menuTrigger.click();
    button.focus();
    button.click();
    return button;
  }
  function modalCancel(modal) { return modal.querySelector('.modal-action button:not(.btn-error)'); }
  function modalConfirm(modal) { return modal.querySelector('.modal-action button.btn-error'); }
  function assertDropzoneMenuClosed(button, label) {
    var dropdown = button && button.closest('.dropdown');
    var menu = dropdown && dropdown.querySelector('.dropdown-content');
    if (!dropdown || !menu) fail('missing ' + label + ' dropzone menu');
    if (menu.getClientRects().length) fail(label + ' confirmation reopened the dropzone menu');
    if (dropdown.contains(document.activeElement)) fail(label + ' confirmation restored focus into the closed dropzone menu');
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    await waitFor(function() { return window.htmx && action('completed') && action('backlog'); }, 'task-board delete controls');
    htmx.process(document.body);
    var modal = document.getElementById('delete_all_tasks_confirm_modal');
    if (!modal) fail('missing shared delete-all confirmation modal');
    if (window.innerWidth <= 500) {
      var modalRect = modal.getBoundingClientRect();
      if (Math.abs(modalRect.width - window.innerWidth) > 1) fail('mobile confirmation modal is not viewport width: ' + modalRect.width + ' vs ' + window.innerWidth);
    }
    var initialState = await currentState();
    var completedAction = openAction('completed');
    await waitFor(function() { return modal.open; }, 'completed confirmation modal');
    if (modal.querySelector('#delete_all_tasks_confirm_name').textContent !== 'completed tasks') fail('completed confirmation name was not rendered');
    var cancel = modalCancel(modal);
    var confirm = modalConfirm(modal);
    if (!cancel || !confirm) fail('shared confirmation modal is missing semantic actions');
    if (document.activeElement !== cancel) fail('opening confirmation did not focus Cancel');
    if ((await currentState()) !== initialState) fail('opening confirmation deleted tasks before explicit confirmation');
    cancel.click();
    await waitFor(function() { return !modal.open; }, 'cancel close');
    await new Promise(function(resolve) { setTimeout(resolve, 50); });
    var completedMenuTrigger = completedAction.closest('.dropdown').querySelector('label');
    if (document.activeElement !== completedMenuTrigger) fail('cancel focus restoration active=' + (document.activeElement && document.activeElement.outerHTML || 'none'));
    if ((await currentState()) !== initialState) fail('cancelling confirmation changed task state');

    completedAction = openAction('completed');
    await waitFor(function() { return modal.open; }, 'Escape confirmation modal');
    cancel = modalCancel(modal);
    cancel.dispatchEvent(new KeyboardEvent('keydown', {bubbles:true, cancelable:true, key:'Escape'}));
    completedMenuTrigger = completedAction.closest('.dropdown').querySelector('label');
    await waitFor(function() { return !modal.open && document.activeElement === completedMenuTrigger; }, 'Escape close and focus restoration');
    if ((await currentState()) !== initialState) fail('Escape cancellation changed task state');

    completedAction = openAction('completed');
    await waitFor(function() { return modal.open; }, 'failed completed confirmation modal');
    confirm = modalConfirm(modal);
    confirm.focus();
    confirm.click();
    confirm.click();
    await new Promise(function(resolve) { setTimeout(resolve, 50); });
    assertDropzoneMenuClosed(completedAction, 'failed delete');
    await waitFor(function() { return !window.deleteAllTasksRequestInFlight; }, 'failed delete request completion');
    if (modal.open) fail('failed delete request left confirmation modal open');
    if (document.getElementById('task-completed-one') === null) fail('failed delete request removed a task');
    var failedState = await currentState();
    if (failedState !== 'project-completed=2;project-backlog=1;foreign-completed=1;foreign-backlog=1;requests=1') fail('failed delete state was unexpected: ' + failedState);

    completedAction = openAction('completed');
    await waitFor(function() { return modal.open; }, 'retry completed confirmation modal');
    confirm = modalConfirm(modal);
    confirm.focus();
    confirm.click();
    confirm.click();
    await waitFor(function() { return !document.getElementById('task-completed-one') && document.querySelector('[data-category="completed"] .text-center'); }, 'successful completed board refresh');
    var completedState = await currentState();
    if (completedState !== 'project-completed=0;project-backlog=1;foreign-completed=1;foreign-backlog=1;requests=2') fail('successful completed delete state was unexpected: ' + completedState);

    var backlogAction = openAction('backlog');
    await waitFor(function() { return modal.open; }, 'backlog confirmation modal');
    if (modal.querySelector('#delete_all_tasks_confirm_name').textContent !== 'backlog tasks') fail('backlog confirmation name was not rendered');
    modalCancel(modal).click();
    var backlogMenuTrigger = backlogAction.closest('.dropdown').querySelector('label');
    await waitFor(function() { return !modal.open && document.activeElement === backlogMenuTrigger; }, 'backlog cancel focus restoration');
    backlogAction = openAction('backlog');
    await waitFor(function() { return modal.open; }, 'backlog confirmation retry');
    confirm = modalConfirm(modal);
    confirm.focus();
    confirm.click();
    confirm.click();
    await waitFor(function() { return !document.getElementById('task-backlog-one') && document.querySelector('[data-category="backlog"] .text-center'); }, 'successful backlog board refresh');
    var finalState = await currentState();
    if (finalState !== 'project-completed=0;project-backlog=0;foreign-completed=1;foreign-backlog=1;requests=3') fail('successful backlog delete state was unexpected: ' + finalState);
    await report('pass', 'delete-all confirmation flow');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/tasks" && r.Method == http.MethodGet:
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(renderBoard()))
				return
			}
			page := strings.Replace(renderPage(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", fixtureCSS+runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case (r.URL.Path == "/tasks/completed" || r.URL.Path == "/tasks/backlog") && r.Method == http.MethodDelete:
			mu.Lock()
			deleteRequests++
			category := strings.TrimPrefix(r.URL.Path, "/tasks/")
			if r.URL.Query().Get("project_id") != project.ID {
				mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte("wrong project"))
				return
			}
			if category == "completed" && failNextCompletedDelete {
				failNextCompletedDelete = false
				mu.Unlock()
				time.Sleep(150 * time.Millisecond)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("delete failed"))
				return
			}
			remaining := tasks[:0]
			for _, task := range tasks {
				if task.ProjectID != project.ID || string(task.Category) != category {
					remaining = append(remaining, task)
				}
			}
			tasks = remaining
			mu.Unlock()
			_, _ = w.Write([]byte(renderBoard()))
		case r.URL.Path == "/browser-state":
			_, _ = w.Write([]byte(state()))
		case r.URL.Path == "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "tasks-delete-all-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "tasks-delete-all-browser-profile"),
		server.URL+"/tasks?project_id="+project.ID,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		mu.Lock()
		requestCount := deleteRequests
		mu.Unlock()
		t.Fatalf("Task-board delete-all browser regression failed: %s (delete requests=%d)\nChrome:\n%s", outcome, requestCount, strings.TrimSpace(string(stderr)))
	}
}

func TestTaskBoardDeleteAllConfirmationResponsiveInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-task-delete-mobile", Name: "Task Delete Mobile"}
	tasks := []models.Task{{
		ID:        "mobile-completed",
		ProjectID: project.ID,
		Title:     "Mobile Completed",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		CreatedAt: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC),
	}}
	var browserResult = make(chan string, 2)
	fixtureCSS := `<style>
.dropdown { position: relative; }
.dropdown-content { display: none; }
.dropdown:focus-within > .dropdown-content { display: block; }
dialog:not([open]) { display: none; }
.modal-action { display: flex; gap: 8px; }
</style>`
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) return resolve(); } catch (error) { return reject(error); }
        if (performance.now() - started > 8000) return reject(new Error('timed out waiting for ' + label));
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  (async function() {
    await waitFor(function() { return window.htmx && document.querySelector('button[data-delete-all-tasks-category="completed"]'); }, 'mobile task-board delete control');
    htmx.process(document.body);
    if (window.innerWidth > 500) fail('mobile regression ran at desktop width: ' + window.innerWidth);
    var modal = document.getElementById('delete_all_tasks_confirm_modal');
    var action = document.querySelector('button[data-delete-all-tasks-category="completed"]');
    var menuTrigger = action && action.closest('.dropdown').querySelector('label');
    if (!modal || !action || !menuTrigger) fail('mobile confirmation controls are missing');
    menuTrigger.focus();
    menuTrigger.click();
    action.focus();
    action.click();
    await waitFor(function() { return modal.open; }, 'mobile confirmation modal');
    var modalRect = modal.getBoundingClientRect();
    var modalBox = modal.querySelector('.modal-box');
    var modalBoxRect = modalBox.getBoundingClientRect();
    var renderedViewportWidth = document.body.getBoundingClientRect().width;
    if (Math.abs(modalRect.width - renderedViewportWidth) > 1 || Math.abs(modalBoxRect.width - renderedViewportWidth) > 1) fail('mobile confirmation is not rendered viewport width: dialog=' + modalRect.width + ' box=' + modalBoxRect.width + ' viewport=' + renderedViewportWidth);
    var cancel = modal.querySelector('.modal-action button:not(.btn-error)');
    if (!cancel || document.activeElement !== cancel) fail('mobile confirmation did not focus Cancel');
    cancel.click();
    await waitFor(function() { return !modal.open && document.activeElement === menuTrigger; }, 'mobile cancellation focus restoration');
    await report('pass', 'mobile delete-all confirmation');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/tasks":
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out); err != nil {
				t.Fatalf("render mobile delete-all page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", fixtureCSS+runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "tasks-delete-all-mobile-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=375,667", "--user-data-dir="+filepath.Join(t.TempDir(), "tasks-delete-all-mobile-browser-profile"),
		server.URL+"/tasks?project_id="+project.ID,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail: timed out waiting for browser result"
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Task-board delete-all mobile browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
