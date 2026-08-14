package pages

import (
	"bytes"
	"context"
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
	"github.com/openvibely/openvibely/internal/repository"
)

func TestTaskAndScheduleCardsUsePointerDragWithGrabCursor(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-card-drag-cursor", Name: "Card Drag Cursor"}
	now := time.Now().Local().Truncate(time.Hour)
	tasks := []models.Task{
		{
			ID:           "task-drag-cursor",
			ProjectID:    project.ID,
			Title:        "Drag cursor task",
			Category:     models.CategoryBacklog,
			Status:       models.StatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
			DisplayOrder: 0,
		},
		{
			ID:           "task-active-status-drag",
			ProjectID:    project.ID,
			Title:        "Active queued task",
			Category:     models.CategoryActive,
			Status:       models.StatusPending,
			CreatedAt:    now,
			UpdatedAt:    now,
			DisplayOrder: 0,
		},
	}
	scheduledTasks := []repository.TaskWithSchedule{{
		Task: models.Task{
			ID:        "schedule-task-drag-cursor",
			ProjectID: project.ID,
			Title:     "Drag cursor schedule",
			Category:  models.CategoryBacklog,
			Status:    models.StatusPending,
			CreatedAt: now,
			UpdatedAt: now,
		},
		Schedule: &models.Schedule{
			ID:             "schedule-drag-cursor",
			TaskID:         "schedule-task-drag-cursor",
			RunAt:          now,
			NextRun:        &now,
			RepeatType:     models.RepeatOnce,
			RepeatInterval: 1,
			Enabled:        true,
		},
	}}

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
  async function exerciseOuterAutoScrollDrop(selector, containerSelector, axis, targetSelector, label, pointerId) {
    await waitFor(function() { return document.querySelector(selector) && document.querySelector(containerSelector) && document.querySelector(targetSelector); }, label + ' ready');
    var card = document.querySelector(selector);
    var container = document.querySelector(containerSelector);
    var target = document.querySelector(targetSelector);
    var originalContainerStyle = container.getAttribute('style');
    var childStyles = Array.from(container.children).map(function(child) { return child.getAttribute('style'); });
    if (axis === 'y') {
      var viewportHeight = label === 'task status card' ? 72 : 180;
      container.style.height = viewportHeight + 'px';
      container.style.maxHeight = viewportHeight + 'px';
      container.style.overflowY = 'auto';
      if (containerSelector === '#kanban-board') {
        container.style.display = 'block';
        Array.from(container.children).forEach(function(column) { column.style.minHeight = '240px'; });
      }
    } else {
      container.style.width = '320px';
      container.style.maxWidth = '320px';
      container.style.overflowX = 'auto';
    }
    card.scrollIntoView({block:'center', inline:'center'});
    await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
    var cardRect = card.getBoundingClientRect();
    var containerRect = container.getBoundingClientRect();
    var targetRect = target.getBoundingClientRect();
    var targetIsOffscreen = axis === 'y'
      ? (targetRect.bottom <= containerRect.top || targetRect.top >= containerRect.bottom)
      : (targetRect.right <= containerRect.left || targetRect.left >= containerRect.right);
    if (!targetIsOffscreen) fail(label + ': target must begin outside the scroll viewport');
    var forward = axis === 'y' ? targetRect.top >= containerRect.bottom : targetRect.left >= containerRect.right;
    var edgeX = axis === 'x' ? (forward ? containerRect.right - 4 : containerRect.left + 4) : Math.max(containerRect.left + 8, Math.min(containerRect.right - 8, cardRect.left + 12));
    var edgeY = axis === 'y' ? (forward ? containerRect.bottom - 4 : containerRect.top + 4) : Math.max(containerRect.top + 8, Math.min(containerRect.bottom - 8, cardRect.top + 12));
    var startX = cardRect.left + Math.min(12, cardRect.width / 2);
    var startY = cardRect.top + Math.min(12, cardRect.height / 2);
    if (card.hasAttribute('draggable')) fail(label + ': card must use pointer-driven dragging, not native HTML drag');
    if (getComputedStyle(card).cursor !== 'grab') fail(label + ': idle cursor should be grab');
    card.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', button:0, buttons:1, clientX:startX, clientY:startY}));
    card.dispatchEvent(new PointerEvent('pointermove', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', buttons:1, clientX:edgeX, clientY:edgeY}));
    if (!card.classList.contains('dragging')) fail(label + ': pointer movement should mark the card as dragging');
    if (getComputedStyle(card).position !== 'fixed' || getComputedStyle(card).transform === 'none') fail(label + ': real card should visibly move during auto-scroll');
    if (!document.querySelector('[data-pointer-drag-placeholder]')) fail(label + ': source slot should retain its placeholder during auto-scroll');
    await waitFor(function() {
      var hit = document.elementFromPoint(edgeX, edgeY);
      var zone = hit && hit.closest('.category-drop-zone, .task-drop-zone, .drop-zone');
      return zone === target && (target.classList.contains('drag-over') || target.style.outline !== '');
    }, label + ' off-screen target feedback');
    if (getComputedStyle(card).cursor !== 'grabbing') fail(label + ': auto-scroll should preserve grabbing cursor');
    card.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, cancelable:true, pointerId:pointerId, pointerType:'mouse', button:0, buttons:0, clientX:edgeX, clientY:edgeY}));
    await waitFor(function() { return !card.classList.contains('dragging'); }, label + ' auto-scroll drop cleanup');
    if (card.style.transform || document.querySelector('[data-pointer-drag-placeholder]')) fail(label + ': release should restore card layout');
    if (getComputedStyle(card).cursor !== 'grab') fail(label + ': release should restore grab cursor');
    if (originalContainerStyle === null) container.removeAttribute('style');
    else container.setAttribute('style', originalContainerStyle);
    Array.from(container.children).forEach(function(child, index) {
      if (childStyles[index] === null) child.removeAttribute('style');
      else child.setAttribute('style', childStyles[index]);
    });
    await new Promise(function(resolve) { setTimeout(resolve, 100); });
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    if (location.pathname === '/tasks') {
      await exerciseOuterAutoScrollDrop('#task-task-drag-cursor', '#kanban-board', 'y', '.category-drop-zone[data-category="completed"]', 'task category card', 9);
      await exerciseOuterAutoScrollDrop('#task-task-active-status-drag', '#kanban-board', 'y', '.task-drop-zone[data-status="running"]', 'task status card', 10);
      location.href = '/schedule?project_id=project-card-drag-cursor';
      return;
    }
    if (location.pathname === '/schedule') {
      var scheduleCard = document.querySelector('[data-schedule-id="schedule-drag-cursor"]');
      var sourceZone = scheduleCard && scheduleCard.closest('.drop-zone');
      if (!sourceZone) fail('schedule off-screen drop: source zone missing');
      var sourceHour = Number(sourceZone.dataset.hour);
      var targetHour = sourceHour <= 17 ? sourceHour + 6 : sourceHour - 6;
      var scheduleTargetSelector = '.drop-zone[data-date="' + sourceZone.dataset.date + '"][data-hour="' + targetHour + '"]';
      await exerciseOuterAutoScrollDrop('[data-schedule-id="schedule-drag-cursor"]', '#schedule-timeline-container', 'y', scheduleTargetSelector, 'schedule card vertical', 11);
      await report('pass', '');
      return;
    }
    fail('unexpected fixture path ' + location.pathname);
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	browserResult := make(chan string, 4)
	var requestMu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestMu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		requestMu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/tasks":
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out); err != nil {
				t.Fatalf("render Tasks page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/schedule":
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(`<div id="schedule-content" data-project-id="project-card-drag-cursor"></div>`))
				return
			}
			var out bytes.Buffer
			if err := Schedule([]models.Project{project}, &project, scheduledTasks, 0, nil, nil).Render(context.Background(), &out); err != nil {
				t.Fatalf("render Schedule page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/browser-result":
			browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/task-drag-cursor/category":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected task category move to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/task-active-status-drag/status":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected Active lane status move to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/task-active-status-drag/reorder":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected task reorder to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/schedules/schedule-drag-cursor/reschedule":
			if r.Method != http.MethodPatch {
				t.Fatalf("expected schedule reschedule to use PATCH, got %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/schedule-task-drag-cursor":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "card-drag-cursor-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-background-networking", "--disable-background-timer-throttling",
		"--no-first-run", "--no-default-browser-check", "--window-size=1280,900",
		"--user-data-dir="+filepath.Join(t.TempDir(), "card-drag-cursor-browser-profile"),
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
	requestMu.Lock()
	requestList := strings.Join(requests, "\n")
	requestMu.Unlock()
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Card drag cursor browser regression failed: %s\nRequests:\n%s\nChrome:\n%s", outcome, requestList, strings.TrimSpace(string(stderr)))
	}
	for _, want := range []string{
		"PATCH /tasks/task-drag-cursor/category",
		"PATCH /tasks/task-active-status-drag/status",
		"PATCH /schedules/schedule-drag-cursor/reschedule",
	} {
		if !strings.Contains(requestList, want) {
			t.Fatalf("browser drag should preserve drop behavior; missing request %q in:\n%s", want, requestList)
		}
	}
	if strings.Contains(requestList, "PATCH /tasks/task-active-status-drag/reorder") {
		t.Fatalf("Active status-lane drag must not be routed as a reorder:\n%s", requestList)
	}
}
