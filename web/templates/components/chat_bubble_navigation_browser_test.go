package components

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserFunctional_ChatBubbleNavigation(t *testing.T) {
	chrome := testChromePath(t)
	var script bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &script); err != nil {
		t.Fatal(err)
	}
	var bubbles bytes.Buffer
	for _, role := range []string{"User", "Assistant", "User", "Assistant"} {
		if err := ChatBubble(role, "Message").Render(context.Background(), &bubbles); err != nil {
			t.Fatal(err)
		}
	}
	fixture := `<!doctype html><html><head><style>
 .transcript {height:250px;overflow:auto;width:500px;} .chat-bubble-user-msg,.chat-bubble-assistant-msg {height:100px;margin-bottom:10px;}
 </style></head><body data-test-result="pending"><div id="browser-result">pending</div>` + script.String() + `<script>
 (async function() {
 function assert(ok, message) { if (!ok) throw Error(message); }
 function key(target, name, options) { var event = new KeyboardEvent('keydown', Object.assign({key:name,metaKey:true,bubbles:true,cancelable:true},options)); target.dispatchEvent(event); return event.defaultPrevented; }
 try {
 for (var id of ['chat-messages','task-thread-messages']) {
  var host=document.createElement('section');
  host.innerHTML='<div class="transcript" id="'+id+'">` + bubbles.String() + `</div><form data-input-id="composer" hx-target="#'+id+'"><textarea id="composer">my draft</textarea></form>';
  document.body.appendChild(host);
  var el=document.getElementById(id), input=host.querySelector('textarea'), tracker=new window.ChatScrollTracker(el);
  var items=el.querySelectorAll('.chat-bubble-user-msg,.chat-bubble-assistant-msg');
  input.focus(); el.scrollTop=el.scrollHeight;
  assert(key(input,'ArrowUp'),id+' shortcut not handled in composer');
  assert(tracker._bubbleJump.bubble===items[1],id+' previous bubble from viewport');
  assert(el.scrollTop===110,id+' previous bubble alignment');
  key(input,'ArrowUp'); assert(el.scrollTop===0,id+' first bubble');
  key(input,'ArrowUp'); assert(el.scrollTop===0,id+' must not wrap');
  key(input,'ArrowDown'); assert(el.scrollTop===110,id+' next bubble');
  key(input,'ArrowDown'); key(input,'ArrowDown');
  assert(tracker._bubbleJump.bubble===items[3],id+' clamped bottom navigation skipped bubble');
  key(input,'ArrowUp'); assert(tracker._bubbleJump.bubble===items[2],id+' reverse from clamped bottom');
  assert(input.value==='my draft' && document.activeElement===input,id+' draft or focus changed');
  assert(!tracker.shouldAutoScroll(),id+' streaming must remain parked');
  assert(!key(input,'ArrowUp',{metaKey:false}),id+' plain arrow intercepted');
  assert(!key(input,'ArrowUp',{shiftKey:true}),id+' selection shortcut intercepted');
  assert(!key(input,'ArrowUp',{isComposing:true}),id+' composition intercepted');
  el.scrollTop=50; key(document.body,'ArrowDown'); assert(el.scrollTop===110,id+' manual scroll not respected');
  host.dataset.workspaceHidden='true'; assert(!key(document.body,'ArrowUp'),id+' hidden workspace intercepted'); delete host.dataset.workspaceHidden;
  var dialog=document.createElement('dialog'); document.body.appendChild(dialog); dialog.showModal();
  assert(!key(document.body,'ArrowUp'),id+' modal shortcut intercepted'); dialog.remove();
  // Replacement bubbles must be discovered without rebinding listeners.
  el.innerHTML=el.innerHTML; el.scrollTop=0; key(input,'ArrowDown'); assert(el.scrollTop===110,id+' swapped transcript failed');
  tracker.returnToLatest(false); assert(tracker.shouldAutoScroll(),id+' latest did not restore streaming');
  tracker.destroy(); assert(!key(input,'ArrowUp'),id+' destroyed tracker still listening'); host.remove();
 }
 document.body.setAttribute('data-test-result','pass'); document.getElementById('browser-result').textContent='PASS';
 } catch(error) { document.body.setAttribute('data-test-result','fail'); document.body.setAttribute('data-test-error',error.stack || String(error)); }
 })();</script></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(fixture))
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "chat bubble navigation", 10000, 25*time.Second)
}
