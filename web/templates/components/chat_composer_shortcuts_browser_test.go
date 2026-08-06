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
		case "/chat/send", "/chat/steer", "/tasks/task-1/thread", "/tasks/task-1/thread/steer", "/tasks/task-1/cancel":
			_ = r.ParseForm()
			mu.Lock()
			records = append(records, requestRecord{Path: r.URL.Path, Form: r.PostForm})
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			if r.URL.Path == "/chat/steer" || r.URL.Path == "/tasks/task-1/thread/steer" {
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
  var apple = idleInput.placeholder.includes('⌘+⏎ steers');
  var modifier = apple ? {metaKey:true} : {ctrlKey:true};
  var expectedHint = apple ? '⏎ sends or queues · ⌘+⏎ steers' : 'Enter sends or queues · Ctrl+Enter steers';
  if (idleInput.placeholder !== expectedHint || activeInput.placeholder !== expectedHint) fail('shortcut copy was not concise or platform appropriate');
  if (idleInput.placeholder.includes('click') || idleInput.placeholder.includes('Shift+Enter')) fail('shortcut copy advertised extra shortcuts');

  idleInput.value = 'composing'; key(idleInput, {isComposing:true});
  activeInput.value = 'newline'; key(activeInput, {shiftKey:true});
  await wait();

  idleInput.value = ''; key(idleInput); await wait();
  idleInput.value = 'validation recovery'; key(idleInput, modifier); await wait();

  var pendingNormalXHR = null;
  function beginPendingNormalSend(initialDraft) {
    var chatForm = document.getElementById('chat-form');
    pendingNormalXHR = {};
    chatForm.setAttribute('data-submit-intent', 'normal');
    idleInput.value = initialDraft;
    chatForm.dispatchEvent(new CustomEvent('htmx:beforeRequest', {bubbles:true, detail:{elt:chatForm, xhr:pendingNormalXHR}}));
  }
  function acceptPendingNormalSend(turnID) {
    var chatForm = document.getElementById('chat-form');
    document.getElementById('chat-messages').innerHTML = '<div data-execution-pair="true" data-exec-status="running" data-exec-id="' + turnID + '"></div>';
    document.getElementById('chat-form-primary-action').outerHTML = '<div id="chat-form-primary-action" data-composer-running="true"><button type="button" aria-label="Stop response">Stop</button></div>';
    pendingNormalXHR.responseText = 'accepted';
    chatForm.dispatchEvent(new CustomEvent('htmx:afterRequest', {bubbles:true, detail:{elt:chatForm, successful:true, xhr:pendingNormalXHR}}));
    pendingNormalXHR = null;
  }

  beginPendingNormalSend('immediate initial keyboard');
  idleInput.value = 'immediate keyboard steer'; key(idleInput, modifier);
  acceptPendingNormalSend('keyboard-turn'); await wait(); await wait();
  if (!document.querySelector('#chat-form #pending-thread-inputs [data-test-steering-row="true"]')) fail('immediate keyboard steer did not render pending');
  document.querySelector('#chat-form #pending-thread-inputs').innerHTML = '';
  document.getElementById('chat-messages').innerHTML = '';
  document.getElementById('chat-form-primary-action').outerHTML = '<div id="chat-form-primary-action" data-composer-running="false"><button type="submit" aria-label="Send message">Send</button></div>';
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
  if (records.length !== 10) fail('request count was ' + records.length + ', want 10');
  var paths = records.map(function(record) { return record.Path; }).join(',');
  if (paths !== '/chat/send,/chat/steer,/chat/send,/chat/send,/chat/send,/tasks/task-1/thread/steer,/tasks/task-1/thread/steer,/tasks/task-1/cancel,/tasks/task-1/thread,/tasks/task-1/thread') fail('request paths were ' + paths);
  if (records[0].Form.message[0] !== 'validation recovery') fail('validation-blocked submit stranded or changed the recovery draft');
  if (records[1].Form.message[0] !== 'immediate keyboard steer' || records[1].Form.expected_turn_id[0] !== 'keyboard-turn') fail('immediate keyboard steer was not deferred to the accepted turn');
  if (records[2].Form.message[0] !== 'idle enter') fail('plain idle Enter lost or changed its draft');
  if (records[3].Form.message[0] !== 'idle steer fallback') fail('idle steer fallback lost or changed its draft');
  if (records[4].Form.message[0] !== 'idle modifier click fallback') fail('idle modifier-click fallback lost or changed its draft');
  if (records[5].Form.expected_turn_id[0] !== 'active-turn') fail('keyboard steer omitted expected-turn guard');
  if (records[6].Form.expected_turn_id[0] !== 'active-turn') fail('click steer omitted expected-turn guard');
  if (records[6].Form.attachment_session_id[0] !== 'session-1') fail('steer omitted attachment session');
  if (records[8].Form.message[0] !== 'transition idle enter fallback') fail('active-to-idle Enter fallback lost or changed its draft');
  if (records[9].Form.message[0] !== 'transition idle click fallback') fail('active-to-idle click fallback lost or changed its draft');
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
	if len(records) != 10 {
		t.Fatalf("recorded requests = %d, want 10: %+v", len(records), records)
	}
	wantDrafts := []string{
		"validation recovery",
		"immediate keyboard steer",
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
	if got := strings.TrimSpace(records[8].Form.Get("message")); got != "transition idle enter fallback" {
		t.Fatalf("active-to-idle Enter fallback draft = %q; records: %+v", got, records)
	}
	if got := strings.TrimSpace(records[9].Form.Get("message")); got != "transition idle click fallback" {
		t.Fatalf("active-to-idle click fallback draft = %q; records: %+v", got, records)
	}
}

func TestChatComposerImmediateModifierClickSteersInChrome(t *testing.T) {
	chrome := testChromePath(t)

	type requestRecord struct {
		Path string
		Form url.Values
	}
	var mu sync.Mutex
	var records []requestRecord

	var form bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{
		FormID:        "chat-form",
		InputID:       "message-input",
		PostEndpoint:  "/chat/send",
		SteerEndpoint: "/chat/steer",
		TargetID:      "chat-messages",
	}).Render(context.Background(), &form); err != nil {
		t.Fatalf("render composer: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx204)
		case "/chat/steer":
			_ = r.ParseForm()
			mu.Lock()
			records = append(records, requestRecord{Path: r.URL.Path, Form: r.PostForm})
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div data-test-steering-row="true">steering pending</div>`))
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending">
<script src="/htmx.js"></script>
<div id="chat-messages"></div>%s
<div id="browser-result">pending</div>
<script>
(async function() {
  function fail(message) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', message); document.getElementById('browser-result').textContent = 'FAIL:' + message; throw new Error(message); }
  function wait() { return new Promise(function(resolve) { setTimeout(resolve, 150); }); }
  var composer = document.getElementById('chat-form');
  var input = document.getElementById('message-input');
  var session = composer.querySelector('input[name="attachment_session_id"]');
  var apple = input.placeholder.includes('⌘+⏎ steers');
  var modifier = apple ? {metaKey:true} : {ctrlKey:true};
  var pendingXHR = {};

  composer.setAttribute('data-submit-intent', 'normal');
  input.value = 'initial message';
  composer.dispatchEvent(new CustomEvent('htmx:beforeRequest', {bubbles:true, detail:{elt:composer, xhr:pendingXHR}}));

  input.value = 'immediate click steer';
  session.value = 'session-immediate';
  document.querySelector('#chat-form-primary-action button').dispatchEvent(new MouseEvent('click', Object.assign({bubbles:true, cancelable:true}, modifier)));
  await wait();

  document.getElementById('chat-messages').innerHTML = '<div data-execution-pair="true" data-exec-status="running" data-exec-id="click-turn"></div>';
  document.getElementById('chat-form-primary-action').outerHTML = '<div id="chat-form-primary-action" data-composer-running="true"><button type="button" aria-label="Stop response">Stop</button></div>';
  pendingXHR.responseText = 'accepted';
  composer.dispatchEvent(new CustomEvent('htmx:afterRequest', {bubbles:true, detail:{elt:composer, successful:true, xhr:pendingXHR}}));
  await wait(); await wait();

  if (!document.querySelector('#chat-form #pending-thread-inputs [data-test-steering-row="true"]')) fail('deferred click steer did not render in pending inputs');
  if (document.querySelector('#chat-messages [data-test-steering-row="true"]')) fail('deferred click steer rendered in transcript');
  if (input.value !== '') fail('accepted deferred click steer did not clear its draft');
  if (session.value !== '') fail('accepted deferred click steer did not clear its attachment session');
  document.getElementById('browser-result').textContent = 'PASS';
  document.body.setAttribute('data-test-result', 'pass');
})();
</script></body></html>`, form.String())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runHeadlessChromeFixture(t, chrome, server.URL+"/", "immediate modifier-click steer", 10000, 25*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 1 {
		t.Fatalf("recorded requests = %d, want 1: %+v", len(records), records)
	}
	if records[0].Path != "/chat/steer" {
		t.Fatalf("request path = %q, want /chat/steer", records[0].Path)
	}
	if got := records[0].Form.Get("message"); got != "immediate click steer" {
		t.Fatalf("steer message = %q, want immediate click steer", got)
	}
	if got := records[0].Form.Get("expected_turn_id"); got != "click-turn" {
		t.Fatalf("expected turn = %q, want click-turn", got)
	}
	if got := records[0].Form.Get("attachment_session_id"); got != "session-immediate" {
		t.Fatalf("attachment session = %q, want session-immediate", got)
	}
}

func TestChatComposerRunningActionModifierSteersInChrome(t *testing.T) {
	chrome := testChromePath(t)

	type requestRecord struct {
		Path string
		Form url.Values
	}
	var mu sync.Mutex
	var records []requestRecord

	var form bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{
		FormID:        "chat-form",
		InputID:       "message-input",
		PostEndpoint:  "/chat/send",
		SteerEndpoint: "/chat/steer",
		TargetID:      "chat-messages",
	}).Render(context.Background(), &form); err != nil {
		t.Fatalf("render composer: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx204)
		case "/chat/steer", "/chat/stop":
			_ = r.ParseForm()
			mu.Lock()
			records = append(records, requestRecord{Path: r.URL.Path, Form: r.PostForm})
			mu.Unlock()
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<div data-test-steering-row="true">steering pending</div>`))
		case "/":
			w.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending">
<script src="/htmx.js"></script>
<div id="chat-messages"></div>%s
<div id="browser-result">pending</div>
<script>
(async function() {
  function fail(message) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', message); document.getElementById('browser-result').textContent = 'FAIL:' + message; throw new Error(message); }
  function key(input, options) { input.dispatchEvent(new KeyboardEvent('keydown', Object.assign({key:'Enter', bubbles:true, cancelable:true}, options || {}))); }
  function wait() { return new Promise(function(resolve) { setTimeout(resolve, 150); }); }
  var input = document.getElementById('message-input');
  var apple = input.placeholder.includes('⌘+⏎ steers');
  var modifier = apple ? {metaKey:true} : {ctrlKey:true};
  var modifierKey = apple ? 'Meta' : 'Control';
  var oldAction = document.getElementById('chat-form-primary-action');
  oldAction.outerHTML = '<div id="chat-form-primary-action" data-composer-running="true" data-active-turn-id="new-turn"><button type="button" aria-label="Stop response" hx-post="/chat/stop" hx-swap="none"><svg data-composer-stop-icon="true"></svg><svg data-composer-steer-icon="true" class="hidden"></svg></button></div>';
  var action = document.querySelector('#chat-form-primary-action button');
  htmx.process(action.parentElement);

  document.dispatchEvent(new KeyboardEvent('keydown', Object.assign({key:modifierKey, bubbles:true}, modifier)));
  if (!action.querySelector('[data-composer-stop-icon]').classList.contains('hidden')) fail('holding modifier did not hide Stop icon');
  if (action.querySelector('[data-composer-steer-icon]').classList.contains('hidden')) fail('holding modifier did not show Send icon');

  input.value = 'keyboard after send'; key(input, modifier); await wait();
  input.value = 'click after send'; action.dispatchEvent(new MouseEvent('click', Object.assign({bubbles:true, cancelable:true}, modifier))); await wait();
  document.getElementById('browser-result').textContent = 'PASS';
  document.body.setAttribute('data-test-result', 'pass');
})().catch(function(error) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', error.message); });
</script></body></html>`, form.String())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runHeadlessChromeFixture(t, chrome, server.URL+"/", "running action modifier steer", 5000, 20*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(records) != 2 {
		t.Fatalf("recorded requests = %d, want 2: %+v", len(records), records)
	}
	wantMessages := []string{"keyboard after send", "click after send"}
	for i := range records {
		if records[i].Path != "/chat/steer" {
			t.Fatalf("request %d path = %q, want /chat/steer: %+v", i, records[i].Path, records)
		}
		if got := records[i].Form.Get("message"); got != wantMessages[i] {
			t.Fatalf("request %d message = %q, want %q", i, got, wantMessages[i])
		}
		if got := records[i].Form.Get("expected_turn_id"); got != "new-turn" {
			t.Fatalf("request %d expected turn = %q, want new-turn", i, got)
		}
	}
}
