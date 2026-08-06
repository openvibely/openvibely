package components

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChatComposerShortcutsInChrome(t *testing.T) {
	chrome := testChromePath(t)

	type requestRecord struct {
		Path string
		Form url.Values
	}
	var mu sync.Mutex
	var records []requestRecord

	renderForm := func(config ChatInputFormConfig) string {
		var buf bytes.Buffer
		if err := ChatInputForm(config).Render(context.Background(), &buf); err != nil {
			t.Fatalf("render composer: %v", err)
		}
		return buf.String()
	}
	idle := renderForm(ChatInputFormConfig{FormID: "chat-form", InputID: "message-input", PostEndpoint: "/chat/send", SteerEndpoint: "/chat/steer", TargetID: "chat-messages"})
	active := renderForm(ChatInputFormConfig{FormID: "task-thread-form", InputID: "task-message-input", PostEndpoint: "/tasks/task-1/thread", SteerEndpoint: "/tasks/task-1/thread/steer", StopEndpoint: "/tasks/task-1/cancel?composer_stop=1", TargetID: "task-thread-messages", TaskID: "task-1", IsRunning: true, ActiveTurnID: "active-turn"})

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx204)
		case "/records":
			mu.Lock()
			defer mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(records)
		case "/chat/send", "/tasks/task-1/thread", "/tasks/task-1/thread/steer", "/tasks/task-1/cancel":
			_ = r.ParseForm()
			mu.Lock()
			records = append(records, requestRecord{Path: r.URL.Path, Form: r.PostForm})
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			if r.URL.Path == "/tasks/task-1/thread/steer" {
				_, _ = w.Write([]byte(`<div class="steering-input-row" data-test-steering-row="true">steering pending</div>`))
				return
			}
			_, _ = w.Write([]byte(`<div data-accepted="true">accepted</div>`))
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending">
<script src="/htmx.js"></script>
<div id="chat-messages"></div>%s
<div id="task-thread-messages"><div data-execution-pair="true" data-exec-status="running" data-exec-id="active-turn"></div></div>%s
<div id="browser-result">pending</div>
<script>
(async function() {
  function fail(message) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', message); document.getElementById('browser-result').textContent = 'FAIL:' + message; throw new Error(message); }
  function key(input, options) { input.dispatchEvent(new KeyboardEvent('keydown', Object.assign({key:'Enter', bubbles:true, cancelable:true}, options || {}))); }
  function click(button, options) { button.dispatchEvent(new MouseEvent('click', Object.assign({bubbles:true, cancelable:true}, options || {}))); }
  function wait() { return new Promise(function(resolve) { setTimeout(resolve, 150); }); }
  var idleInput = document.getElementById('message-input');
  var activeInput = document.getElementById('task-message-input');
  var apple = idleInput.placeholder.includes('⌘+Enter steers');
  var modifier = apple ? {metaKey:true} : {ctrlKey:true};
  var expectedHint = apple ? 'Enter sends or queues · ⌘+Enter steers' : 'Enter sends or queues · Ctrl+Enter steers';
  if (idleInput.placeholder !== expectedHint || activeInput.placeholder !== expectedHint) fail('shortcut copy was not concise or platform appropriate');
  if (idleInput.placeholder.includes('click') || idleInput.placeholder.includes('Shift+Enter')) fail('shortcut copy advertised extra shortcuts');

  idleInput.value = 'composing'; key(idleInput, {isComposing:true});
  activeInput.value = 'newline'; key(activeInput, {shiftKey:true});
  await wait();

  idleInput.value = 'idle enter'; key(idleInput); await wait();
  idleInput.value = 'idle steer fallback'; key(idleInput, modifier); await wait();
  idleInput.value = 'idle modifier click fallback'; click(document.querySelector('#chat-form-primary-action button'), modifier); await wait();
  document.getElementById('chat-form').remove();
  var activeActionHTML = document.getElementById('task-thread-form-primary-action').outerHTML;
  document.getElementById('task-thread-form-primary-action').outerHTML = '<div id="task-thread-form-primary-action" data-composer-running="false"><button type="submit" aria-label="Send message">Send</button></div>';
  activeInput.value = 'keyboard steer'; key(activeInput, modifier); await wait();
  activeInput.value = 'steer with attachment'; document.querySelector('#task-thread-form input[name="attachment_session_id"]').value = 'session-1';
  click(document.querySelector('#task-thread-form-primary-action button'), modifier); await wait();
  document.getElementById('task-thread-form-primary-action').outerHTML = activeActionHTML;
  htmx.process(document.getElementById('task-thread-form-primary-action'));
  if (!document.querySelector('#task-thread-form #pending-thread-inputs [data-test-steering-row="true"]')) fail('steering response was not inserted into pending inputs');
  if (document.querySelector('#task-thread-messages [data-test-steering-row="true"]')) fail('steering response was inserted into the transcript');
  activeInput.value = 'preserved stop draft'; click(document.querySelector('#task-thread-form-primary-action button')); await wait();
  if (activeInput.value !== 'preserved stop draft') fail('normal Stop cleared the draft');

  var activePair = document.querySelector('#task-thread-messages [data-execution-pair="true"]');
  activePair.setAttribute('data-exec-status', 'completed');
  document.getElementById('task-thread-form-primary-action').outerHTML = '<div id="task-thread-form-primary-action" data-composer-running="false"><button type="submit" aria-label="Send message">Send</button></div>';
  await wait();
  if (document.querySelector('#task-thread-form input[name="expected_turn_id"]')) fail('idle action refresh retained stale expected-turn guard');
  activeInput.value = 'transition idle enter fallback'; key(activeInput, modifier); await wait();
  activeInput.value = 'transition idle click fallback'; click(document.querySelector('#task-thread-form-primary-action button'), modifier); await wait();

  var response = await fetch('/records');
  var records = await response.json();
  if (records.length !== 8) fail('request count was ' + records.length + ', want 8');
  var paths = records.map(function(record) { return record.Path; }).join(',');
  if (paths !== '/chat/send,/chat/send,/chat/send,/tasks/task-1/thread/steer,/tasks/task-1/thread/steer,/tasks/task-1/cancel,/tasks/task-1/thread,/tasks/task-1/thread') fail('request paths were ' + paths);
  if (records[0].Form.message[0] !== 'idle enter') fail('plain idle Enter lost or changed its draft');
  if (records[1].Form.message[0] !== 'idle steer fallback') fail('idle steer fallback lost or changed its draft');
  if (records[2].Form.message[0] !== 'idle modifier click fallback') fail('idle modifier-click fallback lost or changed its draft');
  if (records[3].Form.expected_turn_id[0] !== 'active-turn') fail('keyboard steer omitted expected-turn guard');
  if (records[4].Form.expected_turn_id[0] !== 'active-turn') fail('click steer omitted expected-turn guard');
  if (records[4].Form.attachment_session_id[0] !== 'session-1') fail('steer omitted attachment session');
  if (records[6].Form.message[0] !== 'transition idle enter fallback') fail('active-to-idle Enter fallback lost or changed its draft');
  if (records[7].Form.message[0] !== 'transition idle click fallback') fail('active-to-idle click fallback lost or changed its draft');
  if (activeInput.value !== '') fail('successful active-to-idle fallbacks did not clear the draft');
  document.getElementById('browser-result').textContent = 'PASS';
  document.body.setAttribute('data-test-result', 'pass');
})();
</script></body></html>`, idle, active)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		t.Logf("composer shortcut requests: %+v", records)
	})

	runHeadlessChromeFixture(t, chrome, server.URL+"/", "composer shortcuts", 10000, 25*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 8 {
		t.Fatalf("recorded requests = %d, want 8: %+v", len(records), records)
	}
	wantDrafts := []string{
		"idle enter",
		"idle steer fallback",
		"idle modifier click fallback",
		"keyboard steer",
		"steer with attachment",
	}
	for i, want := range wantDrafts {
		if got := strings.TrimSpace(records[i].Form.Get("message")); got != want {
			t.Fatalf("request %d draft = %q, want %q; records: %+v", i, got, want, records)
		}
	}
	if got := strings.TrimSpace(records[6].Form.Get("message")); got != "transition idle enter fallback" {
		t.Fatalf("active-to-idle Enter fallback draft = %q; records: %+v", got, records)
	}
	if got := strings.TrimSpace(records[7].Form.Get("message")); got != "transition idle click fallback" {
		t.Fatalf("active-to-idle click fallback draft = %q; records: %+v", got, records)
	}
}
