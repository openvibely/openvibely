package pages

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestTaskThreadNavigationScrollStateInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	var base bytes.Buffer
	if err := layout.Base("Task Thread navigation fixture", nil, "project-browser").Render(context.Background(), &base); err != nil {
		t.Fatalf("render base navigation script: %v", err)
	}
	baseHTML := base.String()
	navStart := strings.Index(baseHTML, "window.openVibelyNavigate = function")
	navEnd := strings.Index(baseHTML[navStart:], "// Scroll position restoration for drop zones")
	if navStart < 0 || navEnd < 0 {
		t.Fatal("could not isolate production HTMX navigation helper")
	}
	navigationScript := baseHTML[navStart : navStart+navEnd]

	task := &models.Task{
		ID:        "task-browser",
		ProjectID: "project-browser",
		Title:     "Task Thread navigation fixture",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}
	executions := make([]models.Execution, 0, 14)
	for i := 0; i < 14; i++ {
		executions = append(executions, models.Execution{
			ID:         fmt.Sprintf("thread-history-%02d", i),
			TaskID:     task.ID,
			Status:     models.ExecCompleted,
			PromptSent: strings.Repeat(fmt.Sprintf("thread-user-%02d ", i), 8),
			Output:     "",
			StartedAt:  time.Unix(int64(i+1), 0),
		})
	}

	attachments := map[string][]models.ChatAttachment{
		executions[0].ID: {
			{
				ID:          "delayed-image",
				ExecutionID: executions[0].ID,
				FileName:    "delayed-history.svg",
				MediaType:   "image/svg+xml",
				FileSize:    128,
			},
			{
				ID:          "stalled-image",
				ExecutionID: executions[0].ID,
				FileName:    "stalled-history.svg",
				MediaType:   "image/svg+xml",
				FileSize:    128,
			},
		},
	}

	renderTaskFragment := func() string {
		var rendered bytes.Buffer
		if err := components.TaskThreadView(task, executions, nil, nil, attachments, nil, false, 30).Render(context.Background(), &rendered); err != nil {
			t.Fatalf("render production TaskThreadView: %v", err)
		}
		return `<div id="task-page-root"><span hidden data-openvibely-page-title="Task Thread navigation fixture - OpenVibely"></span><div id="thread-content" data-task-id="task-browser" data-loaded="true">` + rendered.String() + `</div></div>`
	}

	runner := `<script>
window.requestAnimationFrame = function(callback) { return setTimeout(callback, 0); };
window.cancelAnimationFrame = function(handle) { clearTimeout(handle); };
window.renderChatMarkdown = function(text) {
  var div = document.createElement('div');
  div.textContent = String(text || '');
  return div.innerHTML;
};
window.renderChatMarkdownAsync = null;
window.addCodeCopyButtons = function() {};
window.addEventListener('DOMContentLoaded', function() {
  window._taskThreadTestSwapTargets = [];
  document.body.addEventListener('htmx:beforeSwap', function(event) {
    window._taskThreadTestSwapTargets.push('before:' + ((event.detail && event.detail.target && event.detail.target.id) || ''));
  });
  document.body.addEventListener('htmx:afterSwap', function(event) {
    window._taskThreadTestSwapTargets.push('after:' + ((event.detail && event.detail.target && event.detail.target.id) || ''));
  });
  function fail(message) { throw new Error(message); }
  function waitFor(check, label, timeout) {
    var started = performance.now();
    return new Promise(function(resolve, reject) {
      function poll() {
        try { if (check()) { resolve(); return; } } catch (error) { reject(error); return; }
        if (performance.now() - started > (timeout || 4000)) { reject(new Error('timed out waiting for ' + label)); return; }
        setTimeout(poll, 10);
      }
      poll();
    });
  }
  function messages() { return document.getElementById('task-thread-messages'); }
  function historyImage() { return messages() && messages().querySelector('img[alt="delayed-history.svg"]'); }
  function stalledHistoryImage() { return messages() && messages().querySelector('img[alt="stalled-history.svg"]'); }
  function bottomDistance() { var el = messages(); return el.scrollHeight - el.scrollTop - el.clientHeight; }
  function waitForTaskDOM(stage) {
    return waitFor(function() {
      return document.getElementById('task-page-root') && messages() && historyImage();
    }, stage + ' Task Thread DOM');
  }
  function waitForTask(stage) {
    return waitFor(function() {
      return document.getElementById('task-page-root') && messages() && messages().style.visibility !== 'hidden';
    }, stage + ' Task Thread restoration');
  }
  function waitForOther() { return waitFor(function() { return document.getElementById('other-page'); }, 'other page'); }
  function reportResult(status, message) {
    return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method: 'POST'}).catch(function() {});
  }
  function markFailure(error) {
    var message = String(error && error.stack || error);
    var root = document.getElementById('task-page-root') || document.getElementById('other-page') || document.body;
    root.setAttribute('data-test-result', 'fail');
    root.setAttribute('data-test-error', message);
    reportResult('fail', message);
  }
  async function assertPinned(stage) {
    try {
      await waitFor(function() { return bottomDistance() <= 1; }, stage + ' true bottom');
    } catch (error) {
      var state = window._taskThreadScrollStates && window._taskThreadScrollStates['task-thread-scroll-task-browser'];
      fail(stage + ' did not restore true bottom: distance=' + bottomDistance() + '; top=' + messages().scrollTop + '; height=' + messages().scrollHeight + '; trackerUp=' + !!(window._taskThreadPageTracker && window._taskThreadPageTracker.userScrolledUp) + '; state=' + JSON.stringify(state) + '; swaps=' + JSON.stringify(window._taskThreadTestSwapTargets) + '; fresh=' + JSON.stringify(window._taskThreadFreshOpenTasks));
    }
    if (bottomDistance() > 1) fail(stage + ' did not restore true bottom: ' + bottomDistance());
  }
  async function assertFreshBottom(stage) {
    await assertPinned(stage);
    var state = window._taskThreadScrollStates && window._taskThreadScrollStates['task-thread-scroll-task-browser'];
    if (window._taskThreadPageTracker && window._taskThreadPageTracker.userScrolledUp) {
      fail(stage + ' restored stale upward intent');
    }
    if (state && state.userScrolledUp) fail(stage + ' retained stale upward snapshot: ' + JSON.stringify(state));
  }

  (async function() {
    await waitForTaskDOM('initial delayed image');
    await waitFor(function() { return !historyImage().complete; }, 'delayed historical image request');
    await new Promise(function(resolve) { setTimeout(resolve, 80); });
    if (messages().style.visibility !== 'hidden') {
      fail('initial transcript became visible before delayed historical image loaded');
    }
    await waitForTask('initial');
    if (!historyImage().complete || historyImage().naturalHeight < 1) {
      fail('initial transcript became visible before historical image decode completed');
    }
    if (stalledHistoryImage().style.display !== 'none' || stalledHistoryImage().hasAttribute('src')) {
      fail('stalled historical image was not removed from layout before reveal');
    }
    await assertPinned('direct load');

    await window.openVibelyNavigate('/other');
    await waitForOther();
    await window.openVibelyNavigate('/tasks/task-browser?tab=chat');
    await waitForTask('direct pinned return');
    await assertPinned('direct pinned return');

    await window.openVibelyNavigate('/other');
    await waitForOther();
    history.back();
    await waitForTask('pinned Back');
    await assertPinned('pinned Back');
    history.forward();
    await waitForOther();
    history.back();
    await waitForTask('pinned Forward/Back');
    await assertPinned('pinned Forward/Back');

    var readingMessages = messages();
    readingMessages.dispatchEvent(new WheelEvent('wheel', {deltaY: -160, bubbles: true}));
    readingMessages.scrollTop = 80;
    readingMessages.dispatchEvent(new Event('scroll'));
    await waitFor(function() {
      return window._taskThreadPageTracker && window._taskThreadPageTracker.userScrolledUp;
    }, 'upward reading intent');

    await window.openVibelyNavigate('/other');
    await waitForOther();
    await window.openVibelyNavigate('/tasks/task-browser?tab=chat');
    await waitForTask('direct reading return');
    await assertFreshBottom('direct reading return');

    var reopenedMessages = messages();
    reopenedMessages.dispatchEvent(new WheelEvent('wheel', {deltaY: -160, bubbles: true}));
    reopenedMessages.scrollTop = 80;
    reopenedMessages.dispatchEvent(new Event('scroll'));
    await waitFor(function() {
      return window._taskThreadPageTracker && window._taskThreadPageTracker.userScrolledUp;
    }, 'post-open upward reading intent');
    var postOpenTop = reopenedMessages.scrollTop;
    var growth = document.createElement('div');
    growth.setAttribute('data-execution-pair', 'true');
    growth.style.minHeight = '240px';
    reopenedMessages.appendChild(growth);
    await new Promise(function(resolve) { setTimeout(resolve, 80); });
    if (Math.abs(reopenedMessages.scrollTop - postOpenTop) > 2) {
      fail('post-open asynchronous growth overrode upward reading intent: before=' + postOpenTop + '; after=' + reopenedMessages.scrollTop);
    }

    await window.openVibelyNavigate('/other');
    await waitForOther();
    history.back();
    await waitForTask('reading Back');
    await assertFreshBottom('reading Back');
    history.forward();
    await waitForOther();
    history.back();
    await waitForTask('reading Forward/Back');
    await assertFreshBottom('reading Forward/Back');

    document.getElementById('task-page-root').setAttribute('data-test-result', 'pass');
    await reportResult('pass', '');
  })().catch(markFailure);
});
</script>`

	style := `<style>
html, body, #main-content { height: 100%; margin: 0; }
#task-page-root { height: 620px; }
#task-thread-messages { display: block; height: 260px; flex: none; overflow-y: auto; }
#task-thread-messages > [data-execution-pair="true"] { min-height: 92px; display: block; }
.chat-stream-content { white-space: pre-wrap; overflow-wrap: anywhere; max-width: 420px; }
#other-page { height: 400px; }
.hidden { display: none !important; }
</style>`

	browserResult := make(chan string, 16)
	var stalledImageRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/chat/attachments/delayed-image/download":
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "no-store")
			time.Sleep(300 * time.Millisecond)
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="320" height="480" viewBox="0 0 320 480"><rect width="320" height="480" fill="#7480ff"/></svg>`))
		case "/chat/attachments/stalled-image/download":
			if stalledImageRequests.Add(1) == 1 {
				<-r.Context().Done()
				return
			}
			http.Error(w, "fixture image unavailable", http.StatusNotFound)
		case "/tasks/task-browser":
			fragment := renderTaskFragment()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(fragment))
				return
			}
			document := `<!doctype html><html><head><meta charset="utf-8"><script src="/htmx-2.0.4.min.js"></script><script>` + navigationScript + `</script>` + style + runner + `</head><body><main id="main-content">` + fragment + `</main></body></html>`
			_, _ = w.Write([]byte(document))
		case "/other":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(`<div id="other-page"><span hidden data-openvibely-page-title="Other - OpenVibely"></span><h1>Other</h1></div>`))
		case "/browser-result":
			result := r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			select {
			case browserResult <- result:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "task-thread-navigation.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderrFile.Close()

	cmd := exec.Command(chrome,
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-software-rasterizer",
		"--disable-dev-shm-usage",
		"--disable-background-networking",
		"--disable-background-timer-throttling",
		"--no-first-run",
		"--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "task-thread-navigation-profile"),
		server.URL+"/tasks/task-browser?tab=chat",
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome Task Thread navigation fixture: %v", err)
	}

	var outcome string
	select {
	case outcome = <-browserResult:
	case <-time.After(30 * time.Second):
		outcome = "fail:timed out waiting for browser result callback"
	}
	stopBrowserProcess(cmd)

	if outcome != "pass:" {
		stderr, _ := os.ReadFile(stderrPath)
		if len(stderr) > 8000 {
			stderr = stderr[len(stderr)-8000:]
		}
		t.Fatalf("real HTMX Task Thread navigation fixture failed: %s\nChrome stderr tail:\n%s", outcome, stderr)
	}
}
