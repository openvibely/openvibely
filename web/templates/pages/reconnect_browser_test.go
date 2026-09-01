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
	"github.com/openvibely/openvibely/internal/events"
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

func reconnectChatEventJSON(t *testing.T, event events.ChatEvent) string {
	t.Helper()
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal reconnect Chat event: %v", err)
	}
	return string(encoded)
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
window.__swaps = [];
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
    if (String(url).indexOf('composer-action') !== -1) {
      if (window.__composerActionHTML && options.target) {
        var actionTarget = typeof options.target === 'string' ? document.querySelector(options.target) : options.target;
        var actionHolder = document.createElement('template');
        actionHolder.innerHTML = window.__composerActionHTML;
        var nextAction = actionTarget ? actionHolder.content.querySelector('#' + actionTarget.id) : null;
        if (actionTarget && nextAction) actionTarget.replaceWith(nextAction.cloneNode(true));
      }
      return Promise.resolve(true);
    }
    if (!options.target || String(url).indexOf('pending-inputs') !== -1) return Promise.resolve(true);
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
      window.__swaps.push({target: target.id, replacement: replacement.id || ''});
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
	if err := startBrowserProcess(cmd); err != nil {
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
	stopBrowserProcess(cmd)
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
	doneEvent := reconnectChatEventJSON(t, events.ChatEvent{Type: events.ChatResponseDone, ProjectID: "project-focus", ExecID: "chat-live", CompletedOutput: "partial missed", Status: string(models.ExecCompleted)})

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
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: ` + doneEvent + `}));
    await window.__wait(120);
    var expectedHolder = document.createElement('template');
    expectedHolder.innerHTML = window.__snapshots.terminal;
    var expectedRevision = expectedHolder.content.querySelector('#chat-page-root').getAttribute('data-chat-revision');
    var actualRevision = document.getElementById('chat-page-root').getAttribute('data-chat-revision');
    if (actualRevision !== expectedRevision) fail('Chat revision did not synchronize automatically: actual=' + actualRevision + ' expected=' + expectedRevision + ' calls=' + JSON.stringify(window.__revisionSyncCalls) + ' known=' + !!window._chatKnownExecIds['chat-live']);
    if (window._chatReconnectCatchupTimer) fail('Chat revision sync left a stale reconnect timer');
    if (document.getElementById('chat-execution-chat-done') !== completedNode || document.getElementById('chat-execution-chat-live') !== liveNode) fail('automatic Chat revision sync replaced execution nodes');
    messages = document.getElementById('chat-messages');
    messages.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
    messages.scrollTop = 37;
    messages.dispatchEvent(new Event('scroll'));
    if (!window._chatPageTracker || !window._chatPageTracker.userScrolledUp) fail('Chat fixture did not establish intentional upward scroll state');
    var terminalVisibilityTransition = window.__hiddenToVisible();
    if (!terminalVisibilityTransition.paused || !terminalVisibilityTransition.resumed) fail('terminal Chat visibility transition did not resume managed realtime state');
    await window.__wait(80);

    if (document.getElementById('chat-execution-chat-done') !== completedNode) fail('no-op visible reconnect replaced completed Chat DOM');
    if (document.getElementById('chat-execution-chat-live') !== liveNode) fail('no-op visible reconnect replaced terminal Chat DOM');
    if (document.getElementById('chat-expanded-tool') !== tool || tool.getAttribute('aria-expanded') !== 'true') fail('Chat tool state was lost');
    if (document.getElementById('message-input') !== draft || draft.value !== 'unsent draft') fail('Chat draft was lost');
    if (document.getElementById('chat-form-session-id') !== session || session.value !== 'pending-session') fail('Chat attachment session was lost');
    var finalMessages = document.getElementById('chat-messages');
    if (finalMessages.scrollTop !== 37) fail('Chat scroll position changed: actual=' + finalMessages.scrollTop + '; height=' + finalMessages.scrollHeight + '; client=' + finalMessages.clientHeight + '; trackerUp=' + !!(window._chatPageTracker && window._chatPageTracker.userScrolledUp) + '; intent=' + (window._chatPageTracker && window._chatPageTracker.intentRevision));
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func TestChatReconnectDiscoversMissedActiveExecutionAndAttachesStream(t *testing.T) {
	running := models.Execution{ID: "chat-missed-active", Status: models.ExecRunning, PromptSent: "missed start", Output: "partial"}
	initialHTML := renderReconnectComponent(t, ChatContent(nil, nil, "project-missed-active", nil, nil, false, false, 30))
	runningHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{running}, "project-missed-active", nil, nil, false, false, 30))
	stopAction := renderReconnectComponent(t, components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id=project-missed-active", true, "missed-active-turn"))
	stopActionJSON, err := json.Marshal(stopAction)
	if err != nil {
		t.Fatalf("marshal missed-active Chat composer action: %v", err)
	}
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML, "running": runningHTML})

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(300);
    window.renderStreamingContent = function(el, text) { el.textContent = text; if (window.setChatRawContent) window.setChatRawContent(el, text); else el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    var initialAction = document.querySelector('#chat-form-primary-action button');
    if (!initialAction || initialAction.getAttribute('aria-label') !== 'Send message') fail('missed-active Chat fixture did not start with Send');
    window.__composerActionHTML = ` + string(stopActionJSON) + `;
    if (window.__streamFor('chat-missed-active')) fail('missed active Chat stream existed before reconciliation');

    window.__hideManagedTab();
    window.__phase = 'running';
    var transition = window.__showManagedTab();
    if (!transition.paused || !transition.resumed) fail('missed-active Chat visibility transition did not reconnect');
    await window.__wait(120);

    var pair = document.getElementById('chat-execution-chat-missed-active');
    if (!pair || pair.getAttribute('data-exec-status') !== 'running') fail('reconnect did not morph in the authoritative running Chat execution');
    var recoveredAction = document.querySelector('#chat-form-primary-action button');
    if (!recoveredAction || recoveredAction.getAttribute('aria-label') !== 'Stop response') fail('recovered active Chat execution did not change Send to Stop');
    var stream = window.__streamFor('chat-missed-active');
    if (!stream) fail('recovered active Chat execution did not attach a per-execution stream: init=' + typeof window._initThreadStreaming + ' resumes=' + document.querySelectorAll('[data-streaming-resume="true"]').length + ' connected=' + !!document.getElementById('streaming-message-chat-missed-active')._sseConnected + ' sources=' + window.__eventSources.map(function(source) { return {url: source.url, closed: source.closed, onmessage: typeof source.onmessage}; }).map(JSON.stringify).join(','));
    if (stream.url.indexOf('offset=7') === -1) fail('recovered Chat stream did not resume from persisted UTF-8 offset: ' + stream.url);
    if (window.__emitFor('chat-missed-active', 'message', ' missed') !== 1) fail('recovered Chat execution attached duplicate streams');
    await window.__wait(60);
    var output = document.getElementById('streaming-message-chat-missed-active');
    var raw = output && (output.getAttribute('data-raw-content') || output.textContent || '');
    if (!output || raw.indexOf('partial missed') === -1) fail('recovered Chat stream did not catch up missed output: ' + raw);
    var swapsBeforeSecondReconnect = window.__swaps.length;
    window.__hiddenToVisible();
    await window.__wait(40);
    if (document.getElementById('chat-execution-chat-missed-active') !== pair) fail('second active Chat reconnect replaced the recovered execution');
    if (window.__emitFor('chat-missed-active', 'message', ' again') !== 1) fail('second active Chat reconnect duplicated or dropped the recovered stream');
    var transcriptSwaps = window.__swaps.slice(swapsBeforeSecondReconnect).filter(function(swap) { return swap.target === 'chat-messages'; });
    if (transcriptSwaps.length !== 0) fail('second active Chat reconnect morphed the current transcript');
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func runChatExecutionStreamOnlyTerminalComposerCase(t *testing.T, terminalStatus models.ExecutionStatus) {
	t.Helper()
	execID := "chat-stream-only-" + string(terminalStatus)
	running := models.Execution{ID: execID, Status: models.ExecRunning, PromptSent: "stream-only terminal"}
	terminal := running
	terminal.Status = terminalStatus
	terminal.Output = "terminal output"
	if terminalStatus == models.ExecFailed {
		terminal.ErrorMessage = "provider failed"
	}
	initialHTML := renderReconnectComponent(t, ChatContent(nil, nil, "project-stream-only", nil, nil, false, false, 30))
	runningHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{running}, "project-stream-only", nil, nil, false, false, 30))
	terminalHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{terminal}, "project-stream-only", nil, nil, false, false, 30))
	stopAction := renderReconnectComponent(t, components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id=project-stream-only", true, "stream-only-turn"))
	sendAction := renderReconnectComponent(t, components.ChatComposerActionButtonOOB("chat-form-primary-action", "/chat/stop?project_id=project-stream-only", false, ""))
	stopActionJSON, err := json.Marshal(stopAction)
	if err != nil {
		t.Fatalf("marshal stream-only Stop action: %v", err)
	}
	sendActionJSON, err := json.Marshal(sendAction)
	if err != nil {
		t.Fatalf("marshal stream-only Send action: %v", err)
	}
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML, "running": runningHTML, "terminal": terminalHTML})
	streamEvent := "done"
	streamData := string(terminalStatus)
	if terminalStatus == models.ExecFailed {
		streamEvent = "error"
		streamData = "provider failed"
	}

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(300);
    window.renderStreamingContent = function(el, text) { el.textContent = text; if (window.setChatRawContent) window.setChatRawContent(el, text); else el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    var draft = document.getElementById('message-input');
    var session = document.getElementById('chat-form-session-id');
    draft.value = '';
    session.value = 'preserved-stream-only-session';
    window.__phase = 'running';
    window.__composerActionHTML = ` + string(stopActionJSON) + `;
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {type: 'chat_new_message', project_id: 'project-stream-only', exec_id: '` + execID + `', message: 'stream-only terminal', source: 'api'}}));
    await window.__wait(100);
    var pair = document.getElementById('chat-execution-` + execID + `');
    var stream = window.__streamFor('` + execID + `');
    var stopButton = document.querySelector('#chat-form-primary-action button');
    if (!pair || !stream) fail('stream-only terminal fixture did not attach its execution stream');
    if (!stopButton || stopButton.getAttribute('aria-label') !== 'Stop response') fail('normal live Chat start did not change Send to Stop');
    if (document.getElementById('message-input') !== draft || draft.value !== '') fail('live start replaced or changed empty Chat draft');
    if (document.getElementById('chat-form-session-id') !== session || session.value !== 'preserved-stream-only-session') fail('live start replaced or cleared Chat attachment session');

    window.__phase = 'terminal';
    window.__composerActionHTML = ` + string(sendActionJSON) + `;
    stream.emit('message', 'terminal output');
    stream.emit('` + streamEvent + `', '` + streamData + `');
    await window.__wait(180);
    var sendButton = document.querySelector('#chat-form-primary-action button');
    if (!sendButton || sendButton.getAttribute('aria-label') !== 'Send message') fail('per-execution-only ` + string(terminalStatus) + ` terminal did not change Stop to Send');
    if (document.getElementById('chat-execution-` + execID + `') !== pair) fail('stream-only terminal replaced the Chat execution node');
    if (pair.getAttribute('data-exec-status') !== '` + string(terminalStatus) + `') fail('stream-only terminal status was not authoritative: ' + pair.getAttribute('data-exec-status'));
    draft.value = 'preserved stream-only draft';
    var expectedHolder = document.createElement('template');
    expectedHolder.innerHTML = window.__snapshots.terminal;
    var expectedRevision = expectedHolder.content.querySelector('#chat-page-root').getAttribute('data-chat-revision');
    if (document.getElementById('chat-page-root').getAttribute('data-chat-revision') !== expectedRevision) fail('stream-only terminal revision stayed stale');
    var swapsBeforeRefocus = window.__swaps.length;
    window.__hiddenToVisible();
    await window.__wait(100);
    if (document.getElementById('chat-execution-` + execID + `') !== pair) fail('stream-only terminal refocus replaced the Chat execution node');
    if (window.__swaps.slice(swapsBeforeRefocus).some(function(swap) { return swap.target === 'chat-messages'; })) fail('stream-only terminal no-op refocus morphed the transcript');
    if (document.getElementById('message-input') !== draft || draft.value !== 'preserved stream-only draft') fail('stream-only terminal lost Chat draft');
    if (document.getElementById('chat-form-session-id') !== session || session.value !== 'preserved-stream-only-session') fail('stream-only terminal lost Chat attachment session');
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func TestChatExecutionStreamOnlyTerminalRefreshesComposerAction(t *testing.T) {
	for _, status := range []models.ExecutionStatus{models.ExecCompleted, models.ExecFailed, models.ExecCancelled} {
		t.Run(string(status), func(t *testing.T) {
			runChatExecutionStreamOnlyTerminalComposerCase(t, status)
		})
	}
}

func TestTaskThreadQueuedPromotionRefreshesComposerActionToStop(t *testing.T) {
	task := &models.Task{ID: "thread-promotion", ProjectID: "project-promotion", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	completed := models.Execution{ID: "thread-before-promotion", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "old", Output: "done"}
	promoted := models.Execution{ID: "thread-promoted", TaskID: task.ID, Status: models.ExecRunning, PromptSent: "queued next", IsFollowup: true}
	initialHTML := renderReconnectComponent(t, components.TaskThreadView(task, []models.Execution{completed}, nil, nil, nil, nil, false, 30))
	promotedFragment := renderReconnectComponent(t, components.TaskThreadFollowupResponse(promoted.PromptSent, promoted.ID, nil, task.ProjectID))
	stopAction := renderReconnectComponent(t, components.ChatComposerActionButtonOOB("task-thread-form-primary-action", "/tasks/thread-promotion/cancel?composer_stop=1", true, "thread-promotion-turn"))
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML})
	promotedJSON, err := json.Marshal(promotedFragment)
	if err != nil {
		t.Fatalf("marshal promoted fragment: %v", err)
	}
	stopActionJSON, err := json.Marshal(stopAction)
	if err != nil {
		t.Fatalf("marshal promoted composer action: %v", err)
	}

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(300);
    window.renderStreamingContent = function(el, text) { el.textContent = text; if (window.setChatRawContent) window.setChatRawContent(el, text); else el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    var originalAjax = window.htmx.ajax;
    window.htmx.ajax = function(method, url, options) {
      options = options || {};
      if (String(url).indexOf('/thread/executions/thread-promoted/fragment') !== -1) {
        window.__ajaxCalls.push({method: method, url: url, options: options});
        var holder = document.createElement('template');
        holder.innerHTML = ` + string(promotedJSON) + `;
        var target = document.querySelector(options.target);
        Array.prototype.slice.call(holder.content.children).forEach(function(node) {
          var clone = node.cloneNode(true);
          target.appendChild(clone);
          clone.querySelectorAll('script').forEach(function(oldScript) {
            var liveScript = document.createElement('script');
            liveScript.textContent = oldScript.textContent;
            oldScript.replaceWith(liveScript);
          });
        });
        return Promise.resolve(true);
      }
      if (String(url).indexOf('/thread/composer-action') !== -1) {
        window.__ajaxCalls.push({method: method, url: url, options: options});
        var holder = document.createElement('template');
        holder.innerHTML = ` + string(stopActionJSON) + `;
        var next = holder.content.querySelector('#task-thread-form-primary-action');
        var current = document.getElementById('task-thread-form-primary-action');
        if (next && current) current.replaceWith(next.cloneNode(true));
        return Promise.resolve(true);
      }
      return originalAjax(method, url, options);
    };

    var initialAction = document.getElementById('task-thread-form-primary-action');
    if (!initialAction || !initialAction.querySelector('[aria-label="Send message"]')) fail('promotion fixture did not start with Send action');
    window.dispatchEvent(new CustomEvent('sse-task-event', {detail: {type: 'task_thread_input_applied', task_id: 'thread-promotion', exec_id: 'thread-promoted', pending_input_id: 'queued-input'}}));
    await window.__wait(120);

    var pair = document.getElementById('chat-execution-thread-promoted');
    if (!pair) fail('queued promotion did not append its authoritative execution fragment');
    if (!window.__streamFor('thread-promoted')) fail('queued promotion did not attach its execution stream');
    var action = document.getElementById('task-thread-form-primary-action');
    if (!action || !action.querySelector('[aria-label="Stop response"]')) fail('queued promotion left the primary composer action as Send');
    var actionCalls = window.__ajaxCalls.filter(function(call) { return String(call.url).indexOf('/thread/composer-action') !== -1; });
    if (actionCalls.length !== 1) fail('queued promotion composer action refresh count was ' + actionCalls.length);
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
	doneEvent := reconnectChatEventJSON(t, events.ChatEvent{Type: events.ChatResponseDone, ProjectID: task.ProjectID, TaskID: task.ID, ExecID: "thread-live", CompletedOutput: "partial missed", Status: string(models.ExecCompleted), IsTaskFollowup: true})

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
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: ` + doneEvent + `}));
	    await window.__wait(120);
	    var expectedThreadHolder = document.createElement('template');
	    expectedThreadHolder.innerHTML = window.__snapshots.terminal;
	    var expectedThreadRevision = expectedThreadHolder.content.querySelector('#task-thread-view').getAttribute('data-thread-revision');
	    var actualThreadRevision = document.getElementById('task-thread-view').getAttribute('data-thread-revision');
    if (actualThreadRevision !== expectedThreadRevision) fail('Task Thread revision did not synchronize automatically: actual=' + actualThreadRevision + ' expected=' + expectedThreadRevision);
    messages = document.getElementById('task-thread-messages');
    messages.dispatchEvent(new WheelEvent('wheel', {deltaY: -120, bubbles: true}));
    messages.scrollTop = 29;
    messages.dispatchEvent(new Event('scroll'));
    if (!window._taskThreadPageTracker || !window._taskThreadPageTracker.userScrolledUp) fail('Task Thread fixture did not establish intentional upward scroll state');
    draft.value = 'thread draft';

    var attachmentVisibilityTransition = window.__hiddenToVisible();
    if (attachmentVisibilityTransition.hidden) fail('Task Thread attachment visibility transition remained hidden');
    await window.__wait(80);
    if (document.getElementById('chat-execution-thread-done') !== completedNode) fail('attachment reconnect replaced completed Task Thread DOM');
    if (document.getElementById('chat-execution-thread-live') !== liveNode) fail('attachment reconnect replaced terminal Task Thread DOM');
    if (document.getElementById('thread-expanded-tool') !== tool || tool.getAttribute('aria-expanded') !== 'true') fail('Task Thread tool state was lost');
    if (document.getElementById('task-message-input') !== draft || draft.value !== 'thread draft') fail('Task Thread draft was lost');
    if (document.getElementById('task-thread-form-session-id') !== session || session.value !== 'thread-pending-session') fail('Task Thread attachment session was lost');
    var finalMessages = document.getElementById('task-thread-messages');
    if (finalMessages.scrollTop !== 29) fail('Task Thread scroll position changed: actual=' + finalMessages.scrollTop + ' expected=29 height=' + finalMessages.scrollHeight + ' trackerUp=' + !!(window._taskThreadPageTracker && window._taskThreadPageTracker.userScrolledUp));

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

func runChatTerminalOrderingReconnectCase(t *testing.T, terminalStatus models.ExecutionStatus, sharedEventFirst bool) {
	t.Helper()
	execID := "chat-terminal-" + string(terminalStatus)
	running := models.Execution{ID: execID, Status: models.ExecRunning, PromptSent: "terminal race", Output: "partial"}
	terminal := running
	terminal.Status = terminalStatus
	terminal.Output = "partial authoritative"
	if terminalStatus == models.ExecFailed {
		terminal.ErrorMessage = "provider failed"
	}
	initialHTML := renderReconnectComponent(t, ChatContent(nil, nil, "project-terminal", nil, nil, false, false, 30))
	runningHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{running}, "project-terminal", nil, nil, false, false, 30))
	terminalHTML := renderReconnectComponent(t, ChatContent(nil, []models.Execution{terminal}, "project-terminal", nil, nil, false, false, 30))
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML, "running": runningHTML, "terminal": terminalHTML})
	terminalEvent := reconnectChatEventJSON(t, events.ChatEvent{
		Type:            events.ChatResponseDone,
		ProjectID:       "project-terminal",
		ExecID:          execID,
		CompletedOutput: "partial authoritative",
		Status:          string(terminalStatus),
	})
	streamEvent := "done"
	streamData := string(terminalStatus)
	if terminalStatus == models.ExecFailed {
		streamEvent = "error"
		streamData = "provider failed"
	}

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(300);
    window.renderStreamingContent = function(el, text) { el.textContent = text; if (window.setChatRawContent) window.setChatRawContent(el, text); else el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    window.__syncResults = [];
    var originalSync = window.syncChatTranscriptRevision;
    window.syncChatTranscriptRevision = function(id) {
      var syncPair = document.getElementById('chat-execution-' + id);
      var syncOutput = syncPair ? syncPair.querySelector('[data-raw-content]') : null;
      var call = {id: id, phase: window.__phase, result: null, status: syncPair && syncPair.getAttribute('data-exec-status'), raw: syncOutput && syncOutput.getAttribute('data-raw-content')};
      window.__syncResults.push(call);
      return originalSync(id).then(function(value) { call.result = value; return value; });
    };
    window.__phase = 'running';
    window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: {type: 'chat_new_message', project_id: 'project-terminal', exec_id: '` + execID + `', message: 'terminal race', source: 'api'}}));
    await window.__wait(80);
    var pair = document.getElementById('chat-execution-` + execID + `');
    var stream = window.__streamFor('` + execID + `');
    if (!pair || !stream) fail('Chat terminal fixture did not attach its execution stream');
    window.__phase = 'terminal';
    var sharedEvent = ` + terminalEvent + `;
    if (` + fmt.Sprintf("%t", sharedEventFirst) + `) {
      window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: sharedEvent}));
      await window.__wait(20);
      stream.emit('` + streamEvent + `', '` + streamData + `');
    } else {
      stream.emit('` + streamEvent + `', '` + streamData + `');
      await window.__wait(20);
      window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: sharedEvent}));
    }
    await window.__wait(160);
    var currentPair = document.getElementById('chat-execution-` + execID + `');
    if (currentPair !== pair) fail('Chat terminal reconciliation replaced the execution node');
    if (pair.getAttribute('data-exec-status') !== '` + string(terminalStatus) + `') fail('Chat terminal status did not remain authoritative: ' + pair.getAttribute('data-exec-status'));
    var terminalOutput = pair.querySelector('[data-raw-content]');
    if (!terminalOutput || terminalOutput.getAttribute('data-raw-content') !== 'partial authoritative') fail('Chat late terminal event overwrote authoritative output: ' + (terminalOutput && terminalOutput.getAttribute('data-raw-content')));
    if (terminalOutput.getAttribute('data-authoritative-terminal-content') !== 'true' || terminalOutput._authoritativeTerminalContent !== 'partial authoritative') fail('Chat terminal handler discarded the authoritative output guard');
    var expectedHolder = document.createElement('template');
    expectedHolder.innerHTML = window.__snapshots.terminal;
    var expectedRevision = expectedHolder.content.querySelector('#chat-page-root').getAttribute('data-chat-revision');
    var root = document.getElementById('chat-page-root');
    if (root.getAttribute('data-chat-revision') !== expectedRevision) {
      var nextRoot = expectedHolder.content.querySelector('#chat-page-root');
      var currentOutput = pair.querySelector('[data-raw-content]');
      var nextPair = nextRoot.querySelector('[data-execution-pair="true"]');
      var nextOutput = nextPair ? nextPair.querySelector('[data-raw-content]') : null;
      fail('Chat terminal revision stayed stale: current=' + root.getAttribute('data-chat-revision') + ' expected=' + expectedRevision + ' match=' + window.chatTranscriptSnapshotMatches(root, nextRoot, 'chat-messages', '` + execID + `') + ' currentRaw=' + (currentOutput && currentOutput.getAttribute('data-raw-content')) + ' nextRaw=' + (nextOutput && nextOutput.getAttribute('data-raw-content')) + ' currentStatus=' + pair.getAttribute('data-exec-status') + ' nextStatus=' + (nextPair && nextPair.getAttribute('data-exec-status')) + ' syncResults=' + JSON.stringify(window.__syncResults) + ' known=' + !!window._chatKnownExecIds['` + execID + `'] + ' ajax=' + JSON.stringify(window.__ajaxCalls));
    }
    var swapsBeforeRefocus = window.__swaps.length;
    window.__hiddenToVisible();
    await window.__wait(100);
    if (document.getElementById('chat-execution-` + execID + `') !== pair) fail('Chat no-op terminal refocus morphed the current node');
    var transcriptSwaps = window.__swaps.slice(swapsBeforeRefocus).filter(function(swap) { return swap.target === 'chat-messages'; });
    if (transcriptSwaps.length !== 0) fail('Chat no-op terminal refocus completed a transcript morph');
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func TestChatFailedAndCancelledTerminalOrderingKeepsNoOpRefocusStable(t *testing.T) {
	for _, status := range []models.ExecutionStatus{models.ExecFailed, models.ExecCancelled} {
		for _, sharedEventFirst := range []bool{false, true} {
			order := "stream terminal before shared terminal"
			if sharedEventFirst {
				order = "shared terminal before stream terminal"
			}
			t.Run(string(status)+"/"+order, func(t *testing.T) {
				runChatTerminalOrderingReconnectCase(t, status, sharedEventFirst)
			})
		}
	}
}

func runTaskThreadTerminalOrderingReconnectCase(t *testing.T, terminalStatus models.ExecutionStatus, sharedEventFirst bool) {
	t.Helper()
	execID := "thread-terminal-" + string(terminalStatus)
	task := &models.Task{ID: "thread-terminal-task", ProjectID: "project-terminal", Status: models.StatusRunning, Category: models.CategoryActive}
	running := models.Execution{ID: execID, TaskID: task.ID, Status: models.ExecRunning, PromptSent: "terminal race", Output: "partial", IsFollowup: true}
	terminalTask := *task
	terminal := running
	terminal.Status = terminalStatus
	terminal.Output = "partial authoritative"
	if terminalStatus == models.ExecFailed {
		terminal.ErrorMessage = "provider failed"
		terminalTask.Status = models.StatusFailed
	} else {
		terminalTask.Status = models.StatusCancelled
	}
	terminalTask.Category = models.CategoryBacklog
	initialHTML := renderReconnectComponent(t, components.TaskThreadView(task, []models.Execution{running}, nil, nil, nil, nil, false, 30))
	terminalHTML := renderReconnectComponent(t, components.TaskThreadView(&terminalTask, []models.Execution{terminal}, nil, nil, nil, nil, false, 30))
	prelude := reconnectFixturePrelude(t, map[string]string{"initial": initialHTML, "terminal": terminalHTML})
	terminalEvent := reconnectChatEventJSON(t, events.ChatEvent{
		Type:            events.ChatResponseDone,
		ProjectID:       task.ProjectID,
		TaskID:          task.ID,
		ExecID:          execID,
		CompletedOutput: "partial authoritative",
		Status:          string(terminalStatus),
		IsTaskFollowup:  true,
	})
	streamEvent := "done"
	streamData := string(terminalStatus)
	if terminalStatus == models.ExecFailed {
		streamEvent = "error"
		streamData = "provider failed"
	}

	testScript := `<main id="reconnect-result"></main><script>
window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function fail(message) { throw new Error(message); }
  try {
    await window.__wait(300);
    window.renderStreamingContent = function(el, text) { el.textContent = text; if (window.setChatRawContent) window.setChatRawContent(el, text); else el.setAttribute('data-raw-content', text); return Promise.resolve(true); };
    window.renderLiveChatContent = window.renderStreamingContent;
    var pair = document.getElementById('chat-execution-` + execID + `');
    var stream = window.__streamFor('` + execID + `');
    if (!pair || !stream) fail('Task Thread terminal fixture did not attach its execution stream');
    window.__phase = 'terminal';
    var sharedEvent = ` + terminalEvent + `;
    if (` + fmt.Sprintf("%t", sharedEventFirst) + `) {
      window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: sharedEvent}));
      await window.__wait(20);
      var markedOutput = pair.querySelector('[data-raw-content]');
      if (!markedOutput || markedOutput.getAttribute('data-authoritative-terminal-content') !== 'true' || markedOutput._authoritativeTerminalContent !== 'partial authoritative') fail('Task Thread shared terminal snapshot was not installed before stream terminal: marker=' + (markedOutput && markedOutput.getAttribute('data-authoritative-terminal-content')) + ' authoritative=' + (markedOutput && markedOutput._authoritativeTerminalContent) + ' raw=' + (markedOutput && markedOutput.getAttribute('data-raw-content')));
      stream.emit('` + streamEvent + `', '` + streamData + `');
    } else {
      stream.emit('` + streamEvent + `', '` + streamData + `');
      await window.__wait(20);
      window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: sharedEvent}));
    }
    await window.__wait(160);
    var currentPair = document.getElementById('chat-execution-` + execID + `');
    if (currentPair !== pair) fail('Task Thread terminal reconciliation replaced the execution node');
    if (pair.getAttribute('data-exec-status') !== '` + string(terminalStatus) + `') fail('Task Thread terminal status did not remain authoritative: ' + pair.getAttribute('data-exec-status'));
    var terminalOutput = pair.querySelector('[data-raw-content]');
    if (!terminalOutput || terminalOutput.getAttribute('data-raw-content') !== 'partial authoritative') fail('Task Thread late terminal event overwrote authoritative output: ' + (terminalOutput && terminalOutput.getAttribute('data-raw-content')));
    if (terminalOutput.getAttribute('data-authoritative-terminal-content') !== 'true' || terminalOutput._authoritativeTerminalContent !== 'partial authoritative') fail('Task Thread terminal handler discarded the authoritative output guard');
    var expectedHolder = document.createElement('template');
    expectedHolder.innerHTML = window.__snapshots.terminal;
    var expectedRevision = expectedHolder.content.querySelector('#task-thread-view').getAttribute('data-thread-revision');
    var view = document.getElementById('task-thread-view');
    if (view.getAttribute('data-thread-revision') !== expectedRevision) fail('Task Thread terminal revision stayed stale');
    var swapsBeforeRefocus = window.__swaps.length;
    window.__hiddenToVisible();
    await window.__wait(100);
    if (document.getElementById('chat-execution-` + execID + `') !== pair) fail('Task Thread no-op terminal refocus morphed the current node');
    var threadSwaps = window.__swaps.slice(swapsBeforeRefocus).filter(function(swap) { return swap.target === 'task-thread-view' || swap.target === 'thread-content'; });
    if (threadSwaps.length !== 0) fail('Task Thread no-op terminal refocus completed a transcript morph');
    result.setAttribute('data-test-result', 'pass');
  } catch (error) {
    result.setAttribute('data-test-result', 'fail');
    result.setAttribute('data-test-error', String(error && error.stack || error));
  }
});
</script>`
	runReconnectChromeFixture(t, prelude+initialHTML+testScript)
}

func TestTaskThreadFailedAndCancelledTerminalOrderingKeepsNoOpRefocusStable(t *testing.T) {
	for _, status := range []models.ExecutionStatus{models.ExecFailed, models.ExecCancelled} {
		for _, sharedEventFirst := range []bool{false, true} {
			order := "stream terminal before shared terminal"
			if sharedEventFirst {
				order = "shared terminal before stream terminal"
			}
			t.Run(string(status)+"/"+order, func(t *testing.T) {
				runTaskThreadTerminalOrderingReconnectCase(t, status, sharedEventFirst)
			})
		}
	}
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
