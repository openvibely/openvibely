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
  var apple = idleInput.placeholder.includes('⌘Enter queues');
  var modifier = apple ? {metaKey:true} : {ctrlKey:true};
  if (apple && !activeInput.placeholder.includes('⌘click steers')) fail('Apple shortcut copy was not applied');
  if (!apple && (!idleInput.placeholder.includes('Ctrl+Enter queues') || !activeInput.placeholder.includes('Ctrl+click steers'))) fail('Windows/Linux shortcut copy was not applied');

  idleInput.value = 'composing'; key(idleInput, {isComposing:true});
  activeInput.value = 'newline'; key(activeInput, {shiftKey:true});
  await wait();

  idleInput.value = 'idle enter'; key(idleInput); await wait();
  activeInput.value = 'explicit queue'; key(activeInput, modifier); await wait();
  activeInput.value = 'steer with attachment'; document.querySelector('#task-thread-form input[name="attachment_session_id"]').value = 'session-1';
  click(document.querySelector('#task-thread-form-primary-action button'), modifier); await wait();
  activeInput.value = 'preserved stop draft'; click(document.querySelector('#task-thread-form-primary-action button')); await wait();

  var response = await fetch('/records');
  var records = await response.json();
  if (records.length !== 4) fail('request count was ' + records.length + ', want 4');
  var paths = records.map(function(record) { return record.Path; }).join(',');
  if (paths !== '/chat/send,/tasks/task-1/thread,/tasks/task-1/thread/steer,/tasks/task-1/cancel') fail('request paths were ' + paths);
  if (records[2].Form.expected_turn_id[0] !== 'active-turn') fail('steer omitted expected-turn guard');
  if (records[2].Form.attachment_session_id[0] !== 'session-1') fail('steer omitted attachment session');
  if (activeInput.value !== 'preserved stop draft') fail('normal Stop cleared the draft');
  document.getElementById('browser-result').textContent = 'PASS';
  document.body.setAttribute('data-test-result', 'pass');
})();
</script></body></html>`, idle, active)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runHeadlessChromeFixture(t, chrome, server.URL+"/", "composer shortcuts", 10000, 25*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 4 {
		t.Fatalf("recorded requests = %d, want 4: %+v", len(records), records)
	}
	if strings.TrimSpace(records[0].Form.Get("message")) != "idle enter" || strings.TrimSpace(records[1].Form.Get("message")) != "explicit queue" || strings.TrimSpace(records[2].Form.Get("message")) != "steer with attachment" {
		t.Fatalf("unexpected submitted drafts: %+v", records)
	}
}
