package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func reconnectTestChrome(t *testing.T) string {
	t.Helper()
	chrome := "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	if _, err := os.Stat(chrome); err == nil {
		return chrome
	}
	for _, candidate := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	t.Skip("Chrome or Chromium is required for reconnect DOM behavior validation")
	return ""
}

func renderReconnectComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var buf bytes.Buffer
	if err := component.Render(context.Background(), &buf); err != nil {
		t.Fatalf("render reconnect fixture: %v", err)
	}
	return buf.String()
}

func renderedTabVisibilityManager(t *testing.T) string {
	t.Helper()
	var base bytes.Buffer
	if err := layout.Base("Reconnect fixture", nil, "").Render(context.Background(), &base); err != nil {
		t.Fatalf("render base visibility manager: %v", err)
	}
	html := base.String()
	start := strings.Index(html, "window._tabVisibility = (function() {")
	if start < 0 {
		t.Fatal("tab visibility manager start not found")
	}
	endOffset := strings.Index(html[start:], "// Track which element was focused before mousedown")
	if endOffset < 0 {
		t.Fatal("tab visibility manager end not found")
	}
	return html[start : start+endOffset]
}

func reconnectFixturePrelude(t *testing.T, snapshots map[string]string) string {
	t.Helper()
	encoded, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatalf("marshal reconnect snapshots: %v", err)
	}
	return `<script>
window.__snapshots = ` + string(encoded) + `;
window.__phase = 'initial';
window.__ajaxCalls = [];
window.__eventSources = [];
window.requestAnimationFrame = function(callback) { return setTimeout(function() { callback(Date.now()); }, 0); };
window.cancelAnimationFrame = function(id) { clearTimeout(id); };
window.__snapshotHTML = function() { return window.__snapshots[window.__phase] || ''; };
window.fetch = function() {
  return Promise.resolve({ok: true, status: 200, text: function() { return Promise.resolve(window.__snapshotHTML()); }});
};
window.htmx = {
  process: function() {},
  ajax: function(method, url, options) {
    options = options || {};
    window.__ajaxCalls.push({method: method, url: url, options: options});
    if (!options.target || String(url).indexOf('composer-action') !== -1 || String(url).indexOf('pending-inputs') !== -1) return Promise.resolve(true);
    var target = typeof options.target === 'string' ? document.querySelector(options.target) : options.target;
    if (!target) return Promise.resolve(false);
    var html = window.__snapshotHTML();
    var detail = {target: target, elt: target, xhr: {responseText: html}, requestConfig: {path: url, verb: method}, shouldSwap: true};
    target.dispatchEvent(new CustomEvent('htmx:beforeSwap', {bubbles: true, cancelable: true, detail: detail}));
    if (detail.shouldSwap === false) return Promise.resolve(false);
    var holder = document.createElement('template');
    holder.innerHTML = html;
    var next = options.select ? holder.content.querySelector(options.select) : holder.content.querySelector('#' + target.id);
    if (next && (String(options.swap).indexOf('outerHTML') !== -1 || String(options.swap).indexOf('morph') !== -1)) {
      var replacement = next.cloneNode(true);
      target.replaceWith(replacement);
      replacement.dispatchEvent(new CustomEvent('htmx:afterSwap', {bubbles: true, detail: {target: replacement}}));
    }
    return Promise.resolve(true);
  }
};
window.EventSource = function(url) {
  this.url = url;
  this.listeners = {};
  this.closed = false;
  window.__eventSources.push(this);
};
window.EventSource.prototype.addEventListener = function(name, handler) { this.listeners[name] = handler; };
window.EventSource.prototype.close = function() { this.closed = true; };
window.EventSource.prototype.emit = function(name, data) {
  var event = {data: data};
  if (name === 'message' && this.onmessage) this.onmessage(event);
  if (this.listeners[name]) this.listeners[name](event);
};
window.__wait = function(ms) { return new Promise(function(resolve) { setTimeout(resolve, ms); }); };
window.__streamFor = function(execId) {
  for (var i = window.__eventSources.length - 1; i >= 0; i--) {
    if (!window.__eventSources[i].closed && window.__eventSources[i].url.indexOf('/events/chat/' + execId) !== -1 && typeof window.__eventSources[i].onmessage === 'function') return window.__eventSources[i];
  }
  return null;
};
window.__emitFor = function(execId, name, data) {
  var count = 0;
  window.__eventSources.forEach(function(source) {
    if (!source.closed && source.url.indexOf('/events/chat/' + execId) !== -1 && typeof source.onmessage === 'function') {
      count++;
      source.emit(name, data);
    }
  });
  return count;
};
</script><script>` + renderedTabVisibilityManager(t) + `</script><script>
window.__visibilityHidden = false;
Object.defineProperty(document, 'hidden', {configurable: true, get: function() { return window.__visibilityHidden; }});
window.__managedOpenCount = 0;
window._tabVisibility.registerSSE('reconnect-fixture', '/events/live', {
  onopen: function() {
    var reconnected = window.__managedOpenCount > 0;
    window.__managedOpenCount++;
    window.dispatchEvent(new CustomEvent('sse-live-connected', {detail: {reconnected: reconnected}}));
  }
});
var initialManagedSource = window.__eventSources[window.__eventSources.length - 1];
if (initialManagedSource && initialManagedSource.onopen) initialManagedSource.onopen();
window.__hideManagedTab = function() {
  var poll = document.getElementById('task-thread-view');
  window.__visibilityOriginalTrigger = poll ? (poll.getAttribute('hx-trigger') || '') : '';
  window.__visibilityHidden = true;
  document.dispatchEvent(new Event('visibilitychange'));
  window.__visibilityPollPaused = !poll || poll.getAttribute('hx-trigger') === 'none';
};
window.__showManagedTab = function() {
  var poll = document.getElementById('task-thread-view');
  window.__visibilityHidden = false;
  document.dispatchEvent(new Event('visibilitychange'));
  var resumed = !poll || poll.getAttribute('hx-trigger') === window.__visibilityOriginalTrigger;
  var managedSource = window.__eventSources[window.__eventSources.length - 1];
  if (managedSource && managedSource.onopen) managedSource.onopen();
  return {paused: !!window.__visibilityPollPaused, resumed: resumed, originalTrigger: window.__visibilityOriginalTrigger, currentTrigger: poll ? (poll.getAttribute('hx-trigger') || '') : '', hidden: window._tabVisibility.isHidden()};
};
window.__hiddenToVisible = function() {
  window.__hideManagedTab();
  return window.__showManagedTab();
};
</script>`
}

func runReconnectChromeFixture(t *testing.T, body string) string {
	t.Helper()
	chrome := reconnectTestChrome(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><head><meta charset=\"utf-8\"></head><body>" + body + "</body></html>"))
	}))
	defer server.Close()

	stdoutPath := filepath.Join(t.TempDir(), "chrome-stdout.html")
	stderrPath := filepath.Join(t.TempDir(), "chrome-stderr.log")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create Chrome stdout: %v", err)
	}
	defer stdout.Close()
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatalf("create Chrome stderr: %v", err)
	}
	defer stderr.Close()

	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage",
		"--disable-background-networking", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chrome-profile"),
		"--virtual-time-budget=8000", "--dump-dom", server.URL,
	)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	result := ""
	for time.Now().Before(deadline) {
		if output, readErr := os.ReadFile(stdoutPath); readErr == nil {
			result = string(output)
			if strings.Contains(result, `data-test-result="pass"`) || strings.Contains(result, `data-test-result="fail"`) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	if !strings.Contains(result, `data-test-result="pass"`) {
		resultState := ""
		if start := strings.Index(result, `<main id="reconnect-result"`); start >= 0 {
			if end := strings.Index(result[start:], ">"); end >= 0 {
				resultState = result[start : start+end+1]
			}
		}
		stderrOutput, _ := os.ReadFile(stderrPath)
		if len(result) > 6000 {
			result = result[len(result)-6000:]
		}
		if len(stderrOutput) > 3000 {
			stderrOutput = stderrOutput[len(stderrOutput)-3000:]
		}
		t.Fatalf("reconnect browser fixture failed:\nResult: %s\nDOM: %s\nChrome: %s", resultState, result, stderrOutput)
	}
	return result
}

func TestChatReconnectTransitionsPreserveCurrentDOMState(t *testing.T) {
	completed := models.Execution{ID: "chat-done", Status: models.ExecCompleted, PromptSent: "old", Output: "stable"}
	running := models.Execution{ID: "chat-live", Status: models.ExecRunning, PromptSent: "new", Output: "partial"}
	terminal := running
	terminal.Status = models.ExecCompleted
	terminal.Output = "partial missed"

	initialHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{completed}, "project-focus", nil, nil, false, false, 30))
	runningHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{completed, running}, "project-focus", nil, nil, false, false, 30))
	terminalHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{completed, terminal}, "project-focus", nil, nil, false, false, 30))
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML, "running": runningHTML, "terminal": terminalHTML})

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(30);
    window.__revisionSyncCalls = [];
    var originalChatRevisionSync = window.syncChatTranscriptRevision;
    window.syncChatTranscriptRevision = function(execId) {
      window.__revisionSyncCalls.push({execId: execId, phase: window.__phase});
      return originalChatRevisionSync(execId);
    };
    window.renderStreamingContent = function(el, text) { el.textContent = text; el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    var messages = document.getElementById('chat-messages');
    var completedNode = document.getElementById('chat-execution-chat-done');
    var draft = document.getElementById('message-input');
    var session = document.getElementById('chat-form-session-id');
    messages.style.height = '1px';
    messages.style.overflow = 'auto';
    completedNode.style.minHeight = '200px';
    messages.scrollTop = 37;
    draft.value = 'unsent draft';
    session.value = 'pending-session';
    var tool = document.createElement('button');
    tool.id = 'chat-expanded-tool';
    tool.setAttribute('aria-expanded', 'true');
    completedNode.appendChild(tool);

    window.dispatchEvent(new Event('blur'));
    window.dispatchEvent(new Event('focus'));
    await window.__wait(20);
    if (document.getElementById('chat-execution-chat-done') !== completedNode) fail('blur/focus replaced a completed Chat node');
    if (window.__ajaxCalls.length !== 0) fail('blur/focus triggered Chat reconciliation');

    window.__phase = 'running';
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {type: 'chat_new_message', project_id: 'project-focus', exec_id: 'chat-live', message: 'new', source: 'api'}}));
    await window.__wait(40);
    var liveNode = document.getElementById('chat-execution-chat-live');
    if (!liveNode || liveNode.getAttribute('data-exec-id') !== 'chat-live') fail('live Chat pair lacks stable chat-execution identity');
    var stream = window.__streamFor('chat-live');
    if (!stream) fail('live Chat execution stream was not attached');
    window.__hideManagedTab();
    var chatStreamCount = window.__emitFor('chat-live', 'message', ' missed');
    if (chatStreamCount !== 1) fail('Chat attached duplicate active streams: ' + chatStreamCount);
    await window.__wait(20);
    var streamNode = document.getElementById('streaming-message-chat-live');
    if (!streamNode || streamNode.getAttribute('data-raw-content').indexOf('missed') === -1) fail('active Chat stream did not catch up output missed while hidden');

    var activeVisibilityTransition = window.__showManagedTab();
    if (!activeVisibilityTransition.paused || !activeVisibilityTransition.resumed) fail('Chat hidden-to-visible transition did not pause/resume managed realtime state');
    await window.__wait(30);
    if (document.getElementById('chat-execution-chat-live') !== liveNode) fail('visible reconnect replaced an active Chat node');

    window.__phase = 'terminal';
    stream.emit('done', 'completed');
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {type: 'chat_response_done', project_id: 'project-focus', exec_id: 'chat-live', completed_output: 'partial missed'}}));
    await window.__wait(120);
    var expectedHolder = document.createElement('template');
    expectedHolder.innerHTML = window.__snapshots.terminal;
    var expectedRevision = expectedHolder.content.querySelector('#chat-page-root').getAttribute('data-chat-revision');
    var actualRevision = document.getElementById('chat-page-root').getAttribute('data-chat-revision');
    if (actualRevision !== expectedRevision) fail('Chat revision did not synchronize automatically: actual=' + actualRevision + ' expected=' + expectedRevision + ' calls=' + JSON.stringify(window.__revisionSyncCalls) + ' known=' + !!window._chatKnownExecIds['chat-live']);
    if (window._chatReconnectCatchupTimer) fail('Chat revision sync left a stale reconnect timer');
    if (document.getElementById('chat-execution-chat-done') !== completedNode || document.getElementById('chat-execution-chat-live') !== liveNode) fail('automatic Chat revision sync replaced execution nodes');
    messages = document.getElementById('chat-messages');
    messages.scrollTop = 37;
    var terminalVisibilityTransition = window.__hiddenToVisible();
    if (!terminalVisibilityTransition.paused || !terminalVisibilityTransition.resumed) fail('terminal Chat visibility transition did not resume managed realtime state');
    await window.__wait(80);

    if (document.getElementById('chat-execution-chat-done') !== completedNode) fail('no-op visible reconnect replaced completed Chat DOM');
    if (document.getElementById('chat-execution-chat-live') !== liveNode) fail('no-op visible reconnect replaced terminal Chat DOM');
    if (document.getElementById('chat-expanded-tool') !== tool || tool.getAttribute('aria-expanded') !== 'true') fail('Chat tool state was lost');
    if (document.getElementById('message-input') !== draft || draft.value !== 'unsent draft') fail('Chat draft was lost');
    if (document.getElementById('chat-form-session-id') !== session || session.value !== 'pending-session') fail('Chat attachment session was lost');
    if (document.getElementById('chat-messages').scrollTop !== 37) fail('Chat scroll position changed');
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func TestTaskThreadReconnectTransitionsPreserveCurrentDOMAndPendingAttachment(t *testing.T) {
	task := &models.Task{ID: "thread-focus", ProjectID: "project-focus", Status: models.StatusRunning, Category: models.CategoryActive}
	completed := models.Execution{ID: "thread-done", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "old", Output: "stable"}
	running := models.Execution{ID: "thread-live", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "new", Output: "partial", IsFollowup: true}
	terminalTask := *task
	terminalTask.Status = models.StatusCompleted
	terminalTask.Category = models.CategoryCompleted
	terminal := running
	terminal.Status = models.ExecCompleted
	terminal.Output = "partial missed"

	initialHTML := renderReconnectComponent(t, components.TaskThreadView(task, []models.Execution{completed, running}, nil, nil, nil, nil, false, 30))
	terminalHTML := renderReconnectComponent(t, components.TaskThreadView(&terminalTask, []models.Execution{completed, terminal}, nil, nil, nil, nil, false, 30))
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML, "terminal": terminalHTML})

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(300);
    window.renderStreamingContent = function(el, text) { el.textContent = text; el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    var view = document.getElementById('task-thread-view');
    var messages = document.getElementById('task-thread-messages');
    var completedNode = document.getElementById('chat-execution-thread-done');
    var liveNode = document.getElementById('chat-execution-thread-live');
    var draft = document.getElementById('task-message-input');
    var session = document.getElementById('task-thread-form-session-id');
    var tool = document.createElement('button');
    tool.id = 'thread-expanded-tool';
    tool.setAttribute('aria-expanded', 'true');
    completedNode.appendChild(tool);
    messages.style.height = '1px';
    messages.style.overflow = 'auto';
    completedNode.style.minHeight = '200px';
    messages.scrollTop = 29;
    draft.value = 'thread draft';
    session.value = 'thread-pending-session';

    window.dispatchEvent(new Event('blur'));
    window.dispatchEvent(new Event('focus'));
    await window.__wait(20);
    if (document.getElementById('chat-execution-thread-done') !== completedNode) fail('blur/focus replaced completed Task Thread DOM');

    var stream = window.__streamFor('thread-live');
    if (!stream) fail('Task Thread active stream was not attached');
    window.__hideManagedTab();
    var activeStreamCount = window.__emitFor('thread-live', 'message', ' missed');
    if (activeStreamCount !== 1) fail('Task Thread attached duplicate active streams: ' + activeStreamCount);
    await window.__wait(80);
    var streamNode = document.getElementById('streaming-message-thread-live');
    var caughtUpText = streamNode ? ((streamNode.getAttribute('data-raw-content') || '') + ' ' + (streamNode.textContent || '')) : '';
    if (!streamNode || caughtUpText.indexOf('missed') === -1) fail('Task Thread stream did not catch up output missed while hidden: ' + caughtUpText);
    var activeThreadVisibilityTransition = window.__showManagedTab();
    if (activeThreadVisibilityTransition.hidden || window.__managedOpenCount < 2) fail('Task Thread hidden-to-visible transition did not reconnect managed realtime state: ' + JSON.stringify(activeThreadVisibilityTransition));
    await window.__wait(30);
    if (document.getElementById('chat-execution-thread-live') !== liveNode) fail('visible reconnect replaced active Task Thread DOM');
    if (session.value !== 'thread-pending-session') fail('visible reconnect cleared Task Thread attachment session');

    window._taskThreadStreamingActive = false;
    view.setAttribute('hx-trigger', 'every 3s');
    var pendingPollVisibilityTransition = window.__hiddenToVisible();
    if (!pendingPollVisibilityTransition.paused || !pendingPollVisibilityTransition.resumed) fail('pending-upload Task Thread poll did not pause and resume: ' + JSON.stringify(pendingPollVisibilityTransition));
    var pollEvent = new CustomEvent('htmx:beforeRequest', {bubbles: true, cancelable: true, detail: {elt: view, requestConfig: {path: view.getAttribute('hx-get') || '/tasks/thread-focus/thread?poll=1', verb: 'GET'}}});
    view.dispatchEvent(pollEvent);
    if (!pollEvent.defaultPrevented) fail('Task Thread poll was not blocked for a pending attachment session');

    window.__phase = 'terminal';
    window._taskThreadStreamingActive = true;
    stream.emit('done', 'completed');
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {type: 'chat_response_done', task_id: 'thread-focus', exec_id: 'thread-live', completed_output: 'partial missed', status: 'completed'}}));
	    await window.__wait(120);
	    var expectedThreadHolder = document.createElement('template');
	    expectedThreadHolder.innerHTML = window.__snapshots.terminal;
	    var expectedThreadRevision = expectedThreadHolder.content.querySelector('#task-thread-view').getAttribute('data-thread-revision');
	    var actualThreadRevision = document.getElementById('task-thread-view').getAttribute('data-thread-revision');
	    if (actualThreadRevision !== expectedThreadRevision) fail('Task Thread revision did not synchronize automatically: actual=' + actualThreadRevision + ' expected=' + expectedThreadRevision);
    messages = document.getElementById('task-thread-messages');
    messages.scrollTop = 29;
    draft.value = 'thread draft';

    var attachmentVisibilityTransition = window.__hiddenToVisible();
    if (attachmentVisibilityTransition.hidden) fail('Task Thread attachment visibility transition remained hidden');
    await window.__wait(50);
    if (document.getElementById('chat-execution-thread-done') !== completedNode) fail('attachment reconnect replaced completed Task Thread DOM');
    if (document.getElementById('chat-execution-thread-live') !== liveNode) fail('attachment reconnect replaced terminal Task Thread DOM');
    if (document.getElementById('thread-expanded-tool') !== tool || tool.getAttribute('aria-expanded') !== 'true') fail('Task Thread tool state was lost');
    if (document.getElementById('task-message-input') !== draft || draft.value !== 'thread draft') fail('Task Thread draft was lost');
    if (document.getElementById('task-thread-form-session-id') !== session || session.value !== 'thread-pending-session') fail('Task Thread attachment session was lost');
    if (document.getElementById('task-thread-messages').scrollTop !== 29) fail('Task Thread scroll position changed');

    session.value = '';
    var noOpThreadVisibilityTransition = window.__hiddenToVisible();
    if (noOpThreadVisibilityTransition.hidden) fail('Task Thread no-op visibility transition remained hidden');
    await window.__wait(80);
    if (document.getElementById('chat-execution-thread-done') !== completedNode || document.getElementById('chat-execution-thread-live') !== liveNode) fail('no-op Task Thread reconciliation replaced current execution nodes');
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func TestReconnectFixtureSnapshotsAreDistinct(t *testing.T) {
	// Guard the browser fixtures themselves: if these revisions collapse, the
	// missed-update transition assertions above no longer exercise reconciliation.
	base := []models.Execution{{ID: "exec", Status: models.ExecRunning, PromptSent: "hello", Output: "partial"}}
	changed := append([]models.Execution(nil), base...)
	changed[0].Output = "partial missed"
	if components.ChatTranscriptRevision(base, nil, "scope") == components.ChatTranscriptRevision(changed, nil, "scope") {
		t.Fatal(fmt.Sprintf("fixture revisions unexpectedly match: %q", components.ChatTranscriptRevision(base, nil, "scope")))
	}
}
