package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func TestAutomationClaimsRefreshCapacityQueuedBoardThroughLiveEventsInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	htmxJS, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	for _, trigger := range []string{"scheduled", "manual"} {
		t.Run(trigger, func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			broadcaster := events.NewBroadcaster()
			tc.handler.broadcaster = broadcaster
			automationRepo := repository.NewAutomationRepo(tc.db)
			automationRepo.SetBroadcaster(broadcaster)

			project := models.Project{Name: "Automation " + trigger + " live board"}
			if err := tc.projectRepo.Create(ctx, &project); err != nil {
				t.Fatalf("create project: %v", err)
			}
			automationTask := createAutomationLiveBoardTask(t, ctx, tc.taskRepo, project.ID, "Capacity queued "+trigger+" Automation", models.CategoryScheduled)
			due := time.Now().UTC().Add(-time.Minute)
			schedule := models.Schedule{TaskID: automationTask.ID, RunAt: due, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, NextRun: &due}
			if err := tc.scheduleRepo.Create(ctx, &schedule); err != nil {
				t.Fatalf("create Automation schedule: %v", err)
			}
			definition, _, err := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry()).Register(ctx, service.AutomationRegistrationRequest{
				ProjectID:  project.ID,
				AdapterKey: service.AutomationAdapterNativeSDLC,
				StableKey:  "native-sdlc/live-board-" + trigger,
				Resources: []models.AutomationResourceBinding{
					{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: automationTask.ID},
					{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
				},
			})
			if err != nil {
				t.Fatalf("register Automation: %v", err)
			}

			decoy := createAutomationLiveBoardTask(t, ctx, tc.taskRepo, project.ID, "Project-scoped decoy", models.CategoryScheduled)
			ordinary := createAutomationLiveBoardTask(t, ctx, tc.taskRepo, project.ID, "Ordinary future schedule", models.CategoryScheduled)
			failed := createAutomationLiveBoardTask(t, ctx, tc.taskRepo, project.ID, "Terminal failed Automation", models.CategoryCompleted)
			if _, err := tc.db.ExecContext(ctx, `UPDATE tasks SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, failed.ID); err != nil {
				t.Fatalf("terminalize failed fixture: %v", err)
			}
			cancelled := createAutomationLiveBoardTask(t, ctx, tc.taskRepo, project.ID, "Terminal cancelled Automation", models.CategoryBacklog)
			if _, err := tc.db.ExecContext(ctx, `UPDATE tasks SET status = 'cancelled' WHERE id = ?`, cancelled.ID); err != nil {
				t.Fatalf("terminalize cancelled fixture: %v", err)
			}

			var boardRefreshes atomic.Int32
			browserResult := make(chan string, 4)
			runner := automationLiveBoardBrowserRunner(automationTask.ID, decoy.ID, ordinary.ID, failed.ID, cancelled.ID)
			e := echo.New()
			e.GET("/events/live", tc.handler.LiveEventsSSE)
			mux := http.NewServeMux()
			mux.Handle("/events/live", e)
			mux.HandleFunc("/htmx-2.0.4.min.js", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
				_, _ = w.Write(htmxJS)
			})
			mux.HandleFunc("/foreign-event", func(w http.ResponseWriter, _ *http.Request) {
				if err := tc.taskRepo.UpdateCategory(ctx, decoy.ID, models.CategoryActive); err != nil {
					t.Fatalf("make decoy board-visible: %v", err)
				}
				broadcaster.Publish(events.TaskEvent{Type: events.TaskBoardUpdated, ProjectID: "foreign-project", TaskID: decoy.ID})
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("/claim", func(w http.ResponseWriter, _ *http.Request) {
				now := time.Now().UTC()
				switch trigger {
				case "scheduled":
					stored, err := tc.scheduleRepo.GetByID(ctx, schedule.ID)
					if err != nil || stored == nil {
						t.Fatalf("load scheduled claim fixture: %#v, %v", stored, err)
					}
					if _, dispatch, err := automationRepo.ClaimScheduledOccurrence(ctx, *stored, now, stored.ComputeNextRun(now)); err != nil || dispatch == nil {
						t.Fatalf("claim scheduled occurrence: %#v, %v", dispatch, err)
					}
				case "manual":
					if _, dispatches, err := automationRepo.ClaimManualAutomationRun(ctx, project.ID, definition.Automation.ID, now); err != nil || len(dispatches) != 1 {
						t.Fatalf("claim manual run: %#v, %v", dispatches, err)
					}
				}
				w.WriteHeader(http.StatusNoContent)
			})
			mux.HandleFunc("/tasks", func(w http.ResponseWriter, r *http.Request) {
				tasks, err := tc.taskRepo.ListBoardByProjectWithCategorySorts(r.Context(), project.ID, "", "created_desc", "completed_desc")
				if err != nil {
					t.Fatalf("list authoritative board: %v", err)
				}
				var out bytes.Buffer
				if r.Header.Get("HX-Request") != "" {
					boardRefreshes.Add(1)
					err = components.KanbanBoard(tasks, project.ID, "created_desc", "completed_desc", nil, nil).Render(r.Context(), &out)
				} else {
					err = pages.Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(r.Context(), &out)
				}
				if err != nil {
					t.Fatalf("render authoritative board: %v", err)
				}
				page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
				if r.Header.Get("HX-Request") == "" {
					page = strings.Replace(page, "</head>", runner+"</head>", 1)
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = w.Write([]byte(page))
			})
			mux.HandleFunc("/browser-result", func(w http.ResponseWriter, r *http.Request) {
				browserResult <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
				w.WriteHeader(http.StatusNoContent)
			})

			server := httptest.NewServer(mux)
			defer server.Close()
			stderrPath := filepath.Join(t.TempDir(), "automation-live-board.stderr")
			stderrFile, err := os.Create(stderrPath)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(chrome,
				"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage",
				"--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check",
				"--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/tasks?project_id="+project.ID,
			)
			cmd.Stderr = stderrFile
			if err := startHandlerBrowserProcess(cmd); err != nil {
				_ = stderrFile.Close()
				t.Fatalf("start Chrome: %v", err)
			}
			var outcome string
			select {
			case outcome = <-browserResult:
			// A saturated hosted runner can spend close to 20 seconds starting
			// Chrome and establishing the shared SSE connection. The in-page
			// waits retain tighter behavioral deadlines once the browser is ready.
			case <-time.After(45 * time.Second):
				outcome = "fail:timed out waiting for browser result"
			}
			stopHandlerBrowserProcess(cmd)
			_ = stderrFile.Close()
			if !strings.HasPrefix(outcome, "pass:") {
				stderr, _ := os.ReadFile(stderrPath)
				t.Fatalf("%s live-board browser regression failed: %s\nChrome:\n%s", trigger, outcome, strings.TrimSpace(string(stderr)))
			}
			if boardRefreshes.Load() != 1 {
				t.Fatalf("authoritative board refreshes = %d, want exactly one after the scoped claim event", boardRefreshes.Load())
			}
		})
	}
}

func TestAutomationRunNowSupersedesFailedLiveStateAcrossRefreshAndReloadInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	htmxJS, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	tc := NewTestContext(t)
	ctx := context.Background()
	broadcaster := events.NewBroadcaster()
	tc.handler.broadcaster = broadcaster
	project := tc.CreateProject().WithName("Automation failed Run now freshness").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	automationRepo.SetBroadcaster(broadcaster)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	lifecycle := service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)
	tc.handler.SetAutomationBuilderServices(nil, nil, nil, nil, nil, lifecycle)

	task := createAutomationLiveBoardTask(t, ctx, tc.taskRepo, project.ID, "Previously failed scheduled task", models.CategoryScheduled)
	runAt := time.Now().UTC().Add(time.Hour)
	schedule := models.Schedule{TaskID: task.ID, RunAt: runAt, NextRun: &runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := tc.scheduleRepo.Create(ctx, &schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/failed-run-now-live",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	if err != nil {
		t.Fatalf("register Automation: %v", err)
	}
	triggerNode := automationNodeByKeyHandler(definition.Nodes, "vision_suggestions")
	if triggerNode == nil {
		t.Fatal("vision_suggestions trigger node not found")
	}
	if _, err := tc.db.ExecContext(ctx, `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at, completed_at)
		VALUES ('browser-previous-failure', ?, ?, ?, ?, 'manual', ?, 'manual:browser-previous-failure', 'failed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		project.ID, definition.Automation.ID, definition.Version.ID, triggerNode.ID, schedule.ID); err != nil {
		t.Fatalf("insert prior failed invocation: %v", err)
	}
	if _, _, err := automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID},
		Binding: models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID,
			NodeID: triggerNode.ID, InvocationID: "browser-previous-failure"},
		ActivityKey: "browser-previous-failure:task", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityFailed,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
	}); err != nil {
		t.Fatalf("record prior failed projection: %v", err)
	}
	if _, err := tc.db.ExecContext(ctx, `UPDATE tasks SET status = 'failed' WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("fail scheduled task: %v", err)
	}
	failedGraph, err := service.NewAutomationGraphService(automationRepo).GetLive(ctx, project.ID, definition.Automation.ID, time.Now().UTC())
	if err != nil || failedGraph == nil {
		t.Fatalf("load failed Live snapshot: %#v, %v", failedGraph, err)
	}
	var stale bytes.Buffer
	if err := pages.AutomationLiveContent(*failedGraph, project.ID, true).Render(ctx, &stale); err != nil {
		t.Fatalf("render stale Live snapshot: %v", err)
	}

	browserResult := make(chan string, 1)
	tc.echo.GET("/stale-live", func(c echo.Context) error {
		time.Sleep(500 * time.Millisecond)
		return c.HTML(http.StatusOK, stale.String())
	})
	tc.echo.POST("/terminal-failure", func(c echo.Context) error {
		var invocationID string
		if err := tc.db.QueryRowContext(c.Request().Context(), `SELECT id FROM automation_invocations
			WHERE project_id = ? AND automation_id = ? AND status IN ('claimed','dispatched','running') ORDER BY rowid DESC LIMIT 1`,
			project.ID, definition.Automation.ID).Scan(&invocationID); err != nil {
			return err
		}
		if _, err := tc.db.ExecContext(c.Request().Context(), `UPDATE automation_invocations SET status = 'failed', completed_at = CURRENT_TIMESTAMP WHERE id = ?`, invocationID); err != nil {
			return err
		}
		if _, _, err := automationRepo.RecordProjectionEvent(c.Request().Context(), repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: project.ID},
			Binding: models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID,
				NodeID: triggerNode.ID, InvocationID: invocationID},
			ActivityKey: "browser-current-failure:task", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityFailed,
			Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
		}); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	tc.echo.POST("/browser-result", func(c echo.Context) error {
		browserResult <- c.QueryParam("status") + ":" + c.QueryParam("message")
		return c.NoContent(http.StatusNoContent)
	})

	var sseConnections atomic.Int32
	var firstSSEMu sync.Mutex
	var firstSSECancel context.CancelFunc
	liveEcho := echo.New()
	liveEcho.GET("/events/live", func(c echo.Context) error {
		connection := sseConnections.Add(1)
		if connection == 1 {
			streamCtx, cancel := context.WithCancel(c.Request().Context())
			firstSSEMu.Lock()
			firstSSECancel = cancel
			firstSSEMu.Unlock()
			c.SetRequest(c.Request().WithContext(streamCtx))
		}
		return tc.handler.LiveEventsSSE(c)
	})

	mux := http.NewServeMux()
	mux.Handle("/events/live", liveEcho)
	mux.HandleFunc("/disconnect-live", func(w http.ResponseWriter, _ *http.Request) {
		firstSSEMu.Lock()
		cancel := firstSSECancel
		firstSSEMu.Unlock()
		if cancel == nil {
			http.Error(w, "initial SSE stream is not connected", http.StatusConflict)
			return
		}
		cancel()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/htmx-2.0.4.min.js", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(htmxJS)
	})
	mux.HandleFunc("/browser-fixture", func(w http.ResponseWriter, r *http.Request) {
		var out bytes.Buffer
		if err := pages.AutomationLive([]models.Project{*project}, project.ID, *failedGraph, true).Render(r.Context(), &out); err != nil {
			t.Fatalf("render Automation Live fixture: %v", err)
		}
		page := strings.Replace(out.String(), "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
		page = strings.Replace(page, "</head>", automationRunNowFreshnessBrowserRunner(definition.Automation.ID)+"</head>", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	mux.Handle("/", tc.echo)

	server := httptest.NewServer(mux)
	defer server.Close()
	stderrPath := filepath.Join(t.TempDir(), "automation-run-now-freshness.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check",
		"--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/browser-fixture")
	cmd.Stderr = stderrFile
	if err := startHandlerBrowserProcess(cmd); err != nil {
		_ = stderrFile.Close()
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(20 * time.Second):
		outcome = "fail:timed out waiting for browser result"
	}
	stopHandlerBrowserProcess(cmd)
	_ = stderrFile.Close()
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("Run now freshness browser regression failed: %s\nChrome:\n%s", outcome, strings.TrimSpace(string(stderr)))
	}
	if got := sseConnections.Load(); got < 2 {
		t.Fatalf("shared SSE connections = %d, want at least 2 to prove native EventSource reconnection", got)
	}
}

func automationRunNowFreshnessBrowserRunner(automationID string) string {
	return fmt.Sprintf(`<script>
		window._automationRunNowSSEConnections = 0;
		window.addEventListener('sse-live-connected', function() { window._automationRunNowSSEConnections++; });
		document.addEventListener('DOMContentLoaded', function() {
	  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
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
	  function stateNode(state) { return document.querySelector('.automation-graph-node--' + state); }
	  (async function() {
		    await waitFor(function() { return document.getElementById('automation-live') && window.openVibelyAutomationLiveRefresh; }, 'initial Automation Live');
		    await waitFor(function() { return window._automationRunNowSSEConnections >= 1; }, 'initial shared SSE connection');
	    var phase = sessionStorage.getItem('automation-run-now-freshness');
	    if (!phase) {
	      if (!stateNode('failed')) throw new Error('initial failed scheduled task was not rendered');
	      window.openVibelyAutomationLiveRefresh('GET', '/stale-live');
	      document.querySelector('[data-automation-live-run-now="%s"]').click();
	      await waitFor(function() { return stateNode('running') && !stateNode('failed'); }, 'authoritative running state after Run now');
	      await window.openVibelyAutomationLiveRefresh('GET');
	      if (!stateNode('running') || stateNode('failed')) throw new Error('subsequent refresh restored stale failure');
	      await new Promise(function(resolve) { setTimeout(resolve, 650); });
	      if (!stateNode('running') || stateNode('failed')) throw new Error('delayed pre-dispatch response won the refresh race');
	      var staleResponse = await fetch('/stale-live');
	      var staleDocument = new DOMParser().parseFromString(await staleResponse.text(), 'text/html');
	      document.getElementById('automation-live').replaceWith(staleDocument.getElementById('automation-live'));
		      if (!stateNode('failed') || stateNode('running')) throw new Error('failed to restore disconnected stale snapshot');
		      var disconnectResponse = await fetch('/disconnect-live', {method:'POST'});
		      if (!disconnectResponse.ok) throw new Error('failed to disconnect production shared SSE stream: ' + disconnectResponse.status);
		      await waitFor(function() { return window._automationRunNowSSEConnections >= 2; }, 'native shared SSE reconnection');
		      await waitFor(function() { return stateNode('running') && !stateNode('failed'); }, 'authoritative running state after SSE reconnect');
	      sessionStorage.setItem('automation-run-now-freshness', 'reloaded');
	      location.reload();
	      return;
	    }
	    if (phase !== 'reloaded') throw new Error('unexpected browser phase ' + phase);
	    await waitFor(function() { return stateNode('running') && !stateNode('failed'); }, 'running state after hard reload');
	    await fetch('/terminal-failure', {method:'POST'});
	    await window.openVibelyAutomationLiveRefresh('GET');
	    await waitFor(function() { return stateNode('failed') && !stateNode('running'); }, 'new terminal failure state');
	    sessionStorage.removeItem('automation-run-now-freshness');
	    await report('pass', '');
	  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
	});
	</script>`, automationID)
}

func createAutomationLiveBoardTask(t *testing.T, ctx context.Context, repo *repository.TaskRepo, projectID, title string, category models.TaskCategory) *models.Task {
	t.Helper()
	task := &models.Task{ProjectID: projectID, Title: title, Category: category, Status: models.StatusPending, Priority: 2, Prompt: title}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

func automationLiveBoardBrowserRunner(automationTaskID, decoyID, ordinaryID, failedID, cancelledID string) string {
	return fmt.Sprintf(`<script>
(function() {
  var NativeEventSource = window.EventSource;
  window._taskBoardNamedFrames = 0;
  window.EventSource = function(url, options) {
    var source = new NativeEventSource(url, options);
    source.addEventListener('task_board_updated', function() { window._taskBoardNamedFrames++; });
    return source;
  };
  window.EventSource.prototype = NativeEventSource.prototype;
  var liveReady = new Promise(function(resolve) { window.addEventListener('sse-live-connected', resolve, {once:true}); });
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
      await Promise.race([liveReady, new Promise(function(_, reject) { setTimeout(function() { reject(new Error('live SSE did not connect')); }, 5000); })]);
      if (document.getElementById('task-%s')) fail('capacity task was visible before repository claim');
      if (document.getElementById('task-%s')) fail('project-scoping decoy was initially visible');
      if (document.getElementById('task-%s')) fail('ordinary scheduled task was projected onto the board');
      var completed = document.querySelector('.category-drop-zone[data-category="completed"]');
      var backlog = document.querySelector('.category-drop-zone[data-category="backlog"]');
      if (!completed || !completed.contains(document.getElementById('task-%s'))) fail('terminal failed Automation is not visible in Completed');
      if (!backlog || !backlog.contains(document.getElementById('task-%s'))) fail('terminal cancelled Automation is not visible in Backlog');

      await fetch('/foreign-event', {method:'POST'});
      await new Promise(function(resolve) { setTimeout(resolve, 700); });
      if (document.getElementById('task-%s')) fail('foreign-project task_board_updated refreshed the selected project');
      var namedFramesBeforeClaim = window._taskBoardNamedFrames;

      await fetch('/claim', {method:'POST'});
      await waitFor(function() { return document.getElementById('task-%s') && document.getElementById('task-%s'); }, 'repository claim board refresh');
      var pending = document.querySelector('.task-drop-zone[data-status="pending"][data-category="active"]');
      var queued = document.getElementById('task-%s');
      if (!pending || !pending.contains(queued)) fail('capacity-queued Automation is not in Active pending dropzone');
      if (queued.dataset.taskCategory !== 'scheduled' || queued.dataset.taskStatus !== 'pending') fail('capacity-queued card lost persisted category/status');
      if (window._taskBoardNamedFrames <= namedFramesBeforeClaim) fail('repository claim task_board_updated frame did not reach the production EventSource');
      await report('pass', '');
    })().catch(function(error) { report('fail', String(error && error.stack || error)); });
  });
})();
	</script>`, automationTaskID, decoyID, ordinaryID, failedID, cancelledID, decoyID, automationTaskID, decoyID, automationTaskID)
}
