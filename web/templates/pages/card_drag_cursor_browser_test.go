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

func TestTaskAndScheduleCardsUseNativeDragWithGrabCursor(t *testing.T) {
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
  function targetDropZoneFor(card, label) {
    if (label === 'task card') return document.querySelector('.category-drop-zone[data-category="completed"]');
    var source = card.closest('.drop-zone');
    if (!source) return null;
    var date = source.dataset.date;
    var hour = Number(source.dataset.hour);
    for (var offset of [1, -1, 2, -2]) {
      var candidate = document.querySelector('.drop-zone[data-date="' + date + '"][data-hour="' + (hour + offset) + '"]');
      if (candidate && candidate !== source) return candidate;
    }
    return Array.from(document.querySelectorAll('.drop-zone')).find(function(zone) { return zone !== source; });
  }
  async function exerciseNativeCard(selector, label) {
    await waitFor(function() { return document.querySelector(selector); }, label + ' ready');
    var card = document.querySelector(selector);
    if (!card.hasAttribute('draggable')) fail(label + ': card must preserve native HTML draggable behavior');
    if (document.querySelector('.drag-card-preview')) fail(label + ': must not create a custom pointer-drag preview');
    if (document.getElementById('drag-cursor-indicator')) fail(label + ': must not create a custom cursor indicator');
    if (getComputedStyle(card).cursor !== 'grab') fail(label + ': idle cursor should be grab, got ' + getComputedStyle(card).cursor);
    var targetZone = targetDropZoneFor(card, label);
    if (!targetZone) fail(label + ': could not find target drop zone');
    var dataTransfer = new DataTransfer();
    var started = card.dispatchEvent(new DragEvent('dragstart', {bubbles:true, cancelable:true, dataTransfer:dataTransfer}));
    if (!started) fail(label + ': dragstart should not be cancelled');
    if (!card.classList.contains('dragging')) fail(label + ': dragstart should mark source card as dragging');
    if (getComputedStyle(card).cursor !== 'grabbing') fail(label + ': dragstart should use grabbing cursor, got ' + getComputedStyle(card).cursor);
    targetZone.dispatchEvent(new DragEvent('dragover', {bubbles:true, cancelable:true, dataTransfer:dataTransfer}));
    targetZone.dispatchEvent(new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:dataTransfer}));
    card.dispatchEvent(new DragEvent('dragend', {bubbles:true, cancelable:true, dataTransfer:dataTransfer}));
    await waitFor(function() { return !card.classList.contains('dragging'); }, label + ' cleanup');
    if (document.querySelector('.drag-card-preview') || document.getElementById('drag-cursor-indicator')) fail(label + ': custom drag UI should not remain after dragend');
    await new Promise(function(resolve) { setTimeout(resolve, 100); });
  }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    if (location.pathname === '/tasks') {
      await exerciseNativeCard('#task-task-drag-cursor', 'task card');
      location.href = '/schedule?project_id=project-card-drag-cursor';
      return;
    }
    if (location.pathname === '/schedule') {
      await exerciseNativeCard('[data-schedule-id="schedule-drag-cursor"]', 'schedule card');
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
		"PATCH /schedules/schedule-drag-cursor/reschedule",
	} {
		if !strings.Contains(requestList, want) {
			t.Fatalf("browser drag should preserve drop behavior; missing request %q in:\n%s", want, requestList)
		}
	}
}
