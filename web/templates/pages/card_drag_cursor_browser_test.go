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
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func TestTaskAndScheduleCardsKeepClosedHandCursorDuringBrowserDrag(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	project := models.Project{ID: "project-card-drag-cursor", Name: "Card Drag Cursor"}
	now := time.Now().Local().Truncate(time.Hour)
	tasks := []models.Task{{
		ID:           "task-drag-cursor",
		ProjectID:    project.ID,
		Title:        "Drag cursor task",
		Category:     models.CategoryBacklog,
		Status:       models.StatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
		DisplayOrder: 0,
	}}
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
  function dragDataTransfer() {
    if (typeof DataTransfer !== 'function') fail('DataTransfer constructor unavailable in browser fixture');
    return new DataTransfer();
  }
  function dispatch(target, event) {
    target.dispatchEvent(event);
    return event;
  }
  function assertClosedHandDragState(card, label, movedX, movedY) {
    var html = document.documentElement;
    var body = document.body;
    if (!html.classList.contains('drag-cursor-active')) fail(label + ': html missing drag-cursor-active');
    if (!body.classList.contains('drag-cursor-active')) fail(label + ': body missing drag-cursor-active');
    var cursor = getComputedStyle(card).cursor;
    if (cursor !== 'none') fail(label + ': native cursor should be hidden while custom closed hand is shown, got ' + cursor);
    var indicator = document.getElementById('drag-cursor-indicator');
    if (!indicator) fail(label + ': missing custom closed-hand cursor indicator');
    var transform = indicator.style.transform || '';
    if (!transform || transform.indexOf('-9999') !== -1) fail(label + ': closed-hand indicator was not visible after movement: ' + transform);
    var expectedX = String(movedX - 14);
    var expectedY = String(movedY - 14);
    if (transform.indexOf(expectedX) === -1 || transform.indexOf(expectedY) === -1) fail(label + ': indicator did not follow browser drag movement, got ' + transform + ', expected around ' + expectedX + ',' + expectedY);
    var dragImage = document.getElementById('drag-cursor-drag-image');
    if (!dragImage || dragImage.querySelector('svg') === null) fail(label + ': custom closed-hand drag image was not installed');
  }
  async function exerciseCard(selector, label) {
    await waitFor(function() { return document.querySelector(selector) && window.beginNativeDragCursor && window.clearNativeDragCursor; }, label + ' ready');
    var card = document.querySelector(selector);
    var rect = card.getBoundingClientRect();
    var startX = Math.round(rect.left + Math.max(4, Math.min(20, rect.width / 2)));
    var startY = Math.round(rect.top + Math.max(4, Math.min(20, rect.height / 2)));
    var movedX = startX + 48;
    var movedY = startY + 36;

    dispatch(card, new PointerEvent('pointerdown', {bubbles:true, cancelable:true, button:0, clientX:startX, clientY:startY}));
    if (!document.documentElement.classList.contains('drag-cursor-pressed')) fail(label + ': pointerdown did not apply pressed cursor state');

    // Chrome may deliver pointercancel as native dragging takes over. This must not clear the closed hand.
    dispatch(card, new PointerEvent('pointercancel', {bubbles:true, cancelable:true, clientX:startX + 8, clientY:startY + 8}));
    if (!document.documentElement.classList.contains('drag-cursor-pressed')) fail(label + ': pointercancel cleared pressed cursor during drag handoff');

    var dt = dragDataTransfer();
    var dragStart = new DragEvent('dragstart', {bubbles:true, cancelable:true, clientX:startX, clientY:startY, dataTransfer:dt});
    dispatch(card, dragStart);
    if (!card.classList.contains('dragging')) fail(label + ': dragstart did not mark card as dragging');
    dispatch(card, new DragEvent('drag', {bubbles:true, cancelable:true, clientX:movedX, clientY:movedY, dataTransfer:dt}));
    dispatch(document, new DragEvent('dragover', {bubbles:true, cancelable:true, clientX:movedX, clientY:movedY, dataTransfer:dt}));
    assertClosedHandDragState(card, label, movedX, movedY);

    dispatch(card, new DragEvent('dragend', {bubbles:true, cancelable:true, clientX:movedX, clientY:movedY, dataTransfer:dt}));
    await waitFor(function() { return !document.documentElement.classList.contains('drag-cursor-active'); }, label + ' cleanup');
    if (card.classList.contains('dragging')) fail(label + ': dragend did not remove dragging class');
    var indicator = document.getElementById('drag-cursor-indicator');
    if (indicator && (indicator.style.transform || '').indexOf('-9999') === -1) fail(label + ': indicator remained visible after dragend');
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    if (location.pathname === '/tasks') {
      await exerciseCard('#task-task-drag-cursor', 'task card');
      location.href = '/schedule?project_id=project-card-drag-cursor';
      return;
    }
    if (location.pathname === '/schedule') {
      await exerciseCard('[data-schedule-id="schedule-drag-cursor"]', 'schedule card');
      await report('pass', '');
      return;
    }
    fail('unexpected fixture path ' + location.pathname);
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
		case "/tasks":
			var out bytes.Buffer
			if err := Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out); err != nil {
				t.Fatalf("render Tasks page: %v", err)
			}
			page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			page = strings.Replace(page, "</head>", runner+"</head>", 1)
			_, _ = w.Write([]byte(page))
		case "/schedule":
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
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Card drag cursor browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
}
