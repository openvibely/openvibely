package pages

import (
	"bytes"
	"context"
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

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
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
    var link = document.querySelector('a[hx-post*="/tasks/' + category + '/sort"][hx-post*="sort=' + key + '"]');
    return link && link.classList.contains('active');
  }
  function clickSort(category, key) {
    htmx.process(document.getElementById('kanban-board'));
    var link = document.querySelector('a[hx-post*="/tasks/' + category + '/sort"][hx-post*="sort=' + key + '"]');
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
    if (!activeSort('backlog', 'created_desc')) fail('Backlog default sort control is not active');
    if (!activeSort('completed', 'completed_desc')) fail('Completed default sort control is not active');

    await fetch('/browser-add?phase=default', {method:'POST'});
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_updated', project_id:'project-tasks-browser'}}));
    await waitFor(function() { return document.getElementById('task-backlog-live') && ids('backlog')[0] === 'backlog-live' && taskState('backlog-old') === 'failed' && taskState('completed-old') === 'merged'; }, 'default live refresh and state icon morph');
    assertOrder('backlog', ['backlog-live', 'backlog-new', 'backlog-old'], 'live Backlog creation order');
    assertOrder('completed', ['completed-live', 'completed-new', 'completed-legacy', 'completed-old'], 'live Completed completion order');
    assertStateIconBeforeTitle('backlog-old', 'failed');
    assertStateIconBeforeTitle('completed-old', 'merged');

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
