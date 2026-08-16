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
    if (!activeSort('backlog', 'created_desc')) fail('Backlog default sort control is not active');
    if (!activeSort('completed', 'completed_desc')) fail('Completed default sort control is not active');

    await fetch('/browser-add?phase=default', {method:'POST'});
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail:{type:'task_updated', project_id:'project-tasks-browser'}}));
    await waitFor(function() { return document.getElementById('task-backlog-live') && ids('backlog')[0] === 'backlog-live'; }, 'default live refresh');
    assertOrder('backlog', ['backlog-live', 'backlog-new', 'backlog-old'], 'live Backlog creation order');
    assertOrder('completed', ['completed-live', 'completed-new', 'completed-legacy', 'completed-old'], 'live Completed completion order');

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
