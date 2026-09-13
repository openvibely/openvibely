package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestUpcomingStopControlUsesConfirmationRefreshAndPreservesCardNavigationInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	projectID := "project-pulse-browser"
	running := models.UpcomingTask{Task: models.Task{
		ID: "pulse-running", ProjectID: projectID, Title: "Running Pulse task",
		Category: models.CategoryActive, Status: models.StatusRunning,
	}}
	stale := models.UpcomingTask{Task: models.Task{
		ID: "pulse-stale", ProjectID: projectID, Title: "Stale Pulse task",
		Category: models.CategoryActive, Status: models.StatusRunning,
	}}
	pending := models.UpcomingTask{Task: models.Task{
		ID: "pulse-pending", ProjectID: projectID, Title: "Pending Pulse task",
		Category: models.CategoryActive, Status: models.StatusPending,
	}}
	queued := models.UpcomingTask{Task: models.Task{
		ID: "pulse-queued", ProjectID: projectID, Title: "Queued Pulse task",
		Category: models.CategoryActive, Status: models.StatusQueued,
	}}
	scheduled := models.UpcomingTask{Task: models.Task{
		ID: "pulse-scheduled", ProjectID: projectID, Title: "Scheduled Pulse task",
		Category: models.CategoryScheduled, Status: models.StatusPending,
	}}
	initial := &models.Upcoming{
		GeneratedAt:    time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
		ProjectID:      projectID,
		RunningTasks:   []models.UpcomingTask{running, stale},
		WaitingTasks:   []models.UpcomingTask{pending, queued},
		PendingTasks:   []models.UpcomingTask{pending},
		QueuedTasks:    []models.UpcomingTask{queued},
		ScheduledTasks: []models.UpcomingTask{scheduled},
	}
	refreshed := &models.Upcoming{
		GeneratedAt:    time.Date(2026, 8, 20, 12, 1, 0, 0, time.UTC),
		ProjectID:      projectID,
		RunningTasks:   []models.UpcomingTask{stale},
		WaitingTasks:   []models.UpcomingTask{pending, queued},
		PendingTasks:   []models.UpcomingTask{pending},
		QueuedTasks:    []models.UpcomingTask{queued},
		ScheduledTasks: []models.UpcomingTask{scheduled},
	}
	render := func(upcoming *models.Upcoming) string {
		t.Helper()
		var body bytes.Buffer
		if err := UpcomingContent(upcoming, projectID).Render(context.Background(), &body); err != nil {
			t.Fatalf("render Pulse content: %v", err)
		}
		return body.String()
	}
	page := func(upcoming *models.Upcoming) string {
		return `<!doctype html><html><head><script src="/htmx-2.0.4.min.js"></script></head><body>` + render(upcoming) + `<script>
			window.__pulseConfirm = false;
			window.__pulseConfirmMessages = [];
			window.confirm = function(message) { window.__pulseConfirmMessages.push(String(message)); return window.__pulseConfirm; };
			window.__pulseNavigations = [];
			window.openVibelyNavigate = function(url) { window.__pulseNavigations.push(url); };
			window.__pulseErrors = [];
				document.body.addEventListener('openvibelyToast', function(event) {
					var detail = event && event.detail ? event.detail : {};
					if (detail.status === 'failed') {
						window.__pulseErrors.push(String(detail.message || detail));
					}
				});
		</script></body></html>`
	}

	var cancelRequests atomic.Int32
	var failedCancelRequests atomic.Int32
	releaseCancel := make(chan struct{})
	var released atomic.Bool
	var invalidCancelRequest atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case r.URL.Path == "/upcoming" && r.Method == http.MethodGet:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page(initial)))
		case r.URL.Path == "/tasks/pulse-running/cancel" && r.Method == http.MethodPost:
			if r.URL.Query().Get("pulse") != "1" || r.URL.Query().Get("project_id") != projectID {
				invalidCancelRequest.Store(true)
			}
			requestNumber := cancelRequests.Add(1)
			if requestNumber == 1 {
				<-releaseCancel
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"Task stopped.","status":"success"}}`)
			_, _ = w.Write([]byte(render(refreshed)))
		case r.URL.Path == "/tasks/pulse-stale/cancel" && r.Method == http.MethodPost:
			failedCancelRequests.Add(1)
			w.Header().Set("HX-Trigger", `{"openvibelyToast":{"message":"This task is no longer cancellable.","status":"failed"}}`)
			w.WriteHeader(http.StatusConflict)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer func() {
		if !released.Swap(true) {
			close(releaseCancel)
		}
	}()

	runComposerFocusCDP(t, chrome, server.URL+"/upcoming?project_id="+projectID, "upcoming-stop-control", func(browser *composerFocusCDP) {
		browser.waitFor("Pulse content", `document.readyState+':'+Boolean(document.querySelector('#upcoming-container'))`, "complete:true")
		eligibility := browser.evaluate(`(function(){return ['pulse-running','pulse-stale','pulse-pending','pulse-queued','pulse-scheduled'].map(function(id){return id+':'+Boolean(document.querySelector('[data-upcoming-task-id="'+id+'"] [data-upcoming-stop]'));}).join('|')})()`)
		if eligibility != "pulse-running:true|pulse-stale:true|pulse-pending:true|pulse-queued:true|pulse-scheduled:false" {
			t.Fatalf("unexpected browser Stop eligibility: %s", eligibility)
		}

		browser.evaluate(`window.__pulseConfirm = false; 'ready'`)
		browser.click(`[data-upcoming-task-id="pulse-running"] [data-upcoming-stop]`)
		browser.waitFor("cancel confirmation", `String(window.__pulseConfirmMessages.length)`, "1")
		if cancelRequests.Load() != 0 {
			t.Fatalf("cancelled confirmation still issued %d request(s)", cancelRequests.Load())
		}
		if got := browser.evaluate(`String(window.__pulseNavigations.length)`); got != "0" {
			t.Fatalf("cancel control opened task navigation after confirmation was declined: %s", got)
		}

		browser.evaluate(`window.__pulseConfirm = true; 'ready'`)
		browser.click(`[data-upcoming-task-id="pulse-stale"] [data-upcoming-stop]`)
		browser.waitFor("stale cancellation confirmation", `String(window.__pulseConfirmMessages.length)`, "2")
		browser.waitFor("Pulse cancellation error", `String(window.__pulseErrors.length)`, "1")
		if got := browser.evaluate(`Boolean(document.querySelector('[data-upcoming-task-id="pulse-stale"]'))+':'+String(document.querySelector('[data-upcoming-task-id="pulse-stale"] [data-upcoming-stop]').disabled)`); got != "true:false" {
			t.Fatalf("stale Pulse card was not retained and re-enabled: %s", got)
		}
		if got := browser.evaluate(`String(window.__pulseNavigations.length)`); got != "0" {
			t.Fatalf("rejected Stop opened task navigation: %s", got)
		}

		browser.click(`[data-upcoming-task-id="pulse-running"] [data-upcoming-stop]`)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && cancelRequests.Load() != 1 {
			time.Sleep(10 * time.Millisecond)
		}
		if cancelRequests.Load() != 1 {
			t.Fatalf("confirmed Stop did not issue one request")
		}
		if invalidCancelRequest.Load() {
			t.Fatal("Stop request did not preserve the Pulse project context")
		}
		if got := browser.evaluate(`String(document.querySelector('[data-upcoming-task-id="pulse-running"] [data-upcoming-stop]').disabled)`); got != "true" {
			t.Fatalf("Stop control was not disabled while request was in flight: %s", got)
		}
		browser.click(`[data-upcoming-task-id="pulse-running"] [data-upcoming-stop]`)
		if cancelRequests.Load() != 1 {
			t.Fatalf("duplicate click issued %d cancellation requests", cancelRequests.Load())
		}
		if !released.Swap(true) {
			close(releaseCancel)
		}
		browser.waitFor("Pulse cancellation refresh", `Boolean(document.querySelector('[data-upcoming-task-id="pulse-running"]'))+':'+Boolean(document.querySelector('[data-upcoming-task-id="pulse-pending"]'))+':'+Boolean(document.querySelector('[data-upcoming-task-id="pulse-stale"]'))`, "false:true:true")
		if got := browser.evaluate(`String(window.__pulseNavigations.length)`); got != "0" {
			t.Fatalf("successful Stop opened task navigation: %s", got)
		}

		browser.click(`[data-upcoming-task-id="pulse-pending"] .card-body`)
		browser.waitFor("remaining Pulse card navigation", `window.__pulseNavigations[window.__pulseNavigations.length - 1] || ''`, "/tasks/pulse-pending?tab=details")
	})

	if cancelRequests.Load() != 1 {
		t.Fatalf("server observed %d cancel requests, want exactly one", cancelRequests.Load())
	}
	if failedCancelRequests.Load() != 1 {
		t.Fatalf("server observed %d rejected cancel requests, want exactly one", failedCancelRequests.Load())
	}
}
