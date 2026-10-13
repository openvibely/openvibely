package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_PrunedChatEarlierLoaderUsesCurrentCursor(t *testing.T) {
	chrome := testChromePath(t)
	var rendered bytes.Buffer
	if err := ChatAutoScrollScript().Render(context.Background(), &rendered); err != nil {
		t.Fatal(err)
	}
	source := rendered.String()
	start := strings.Index(source, "window.pruneChatExecutionWindow = function(")
	if start < 0 {
		t.Fatal("missing pruning helper")
	}
	end := strings.Index(source[start:], "// resolveScrollTracker")
	if end < 0 {
		t.Fatal("missing pruning helper boundary")
	}
	helper := source[start : start+end]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/chat" {
			if r.URL.Query().Get("before") == "" {
				fmt.Fprint(w, `<div id="chat-page-root"><div id="chat-messages"></div><button data-latest>Latest message</button><form id="chat-form"></form></div>`)
			} else {
				fmt.Fprintf(w, `<div data-returned-cursor="%s">Earlier message</div>`, r.URL.Query().Get("before"))
			}
			return
		}
		fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending"><script src="/static/vendor/htmx.min.js"></script><div id="chat-page-root"><div id="chat-messages" data-window-limit="2" data-earlier-url-base="/chat?project_id=test&limit=2"><div data-execution-pair="true" data-exec-id="one"></div><div data-execution-pair="true" data-exec-id="two"></div><div data-execution-pair="true" data-exec-id="three"></div></div><button data-latest>Latest message</button><form id="chat-form"></form></div><script>%s
window.addEventListener('load', async function() {
 try {
  const messages = document.getElementById('chat-messages');
  window.pruneChatExecutionWindow(messages);
  // Advance the cursor again on an already processed loader.
  const pair = document.createElement('div');
  pair.setAttribute('data-execution-pair', 'true'); pair.setAttribute('data-exec-id', 'four');
  messages.appendChild(pair);
  window.pruneChatExecutionWindow(messages);
  const loader = messages.querySelector('[data-earlier-loader]');
  const done = new Promise(resolve => document.body.addEventListener('htmx:afterSettle', resolve, {once:true}));
  htmx.trigger(loader, 'ov:load-earlier');
  await done;
  if (document.querySelectorAll('#chat-form').length !== 1 || document.querySelectorAll('[data-latest]').length !== 1) throw new Error('earlier loader inserted a full chat panel: duplicate composer and Latest message');
  if (!messages.querySelector('[data-returned-cursor="three"]')) throw new Error('earlier loader used a stale cursor');
  document.body.setAttribute('data-test-result', 'pass');
 } catch(error) { document.body.setAttribute('data-test-result', 'fail'); document.body.setAttribute('data-test-error', String(error)); }
});</script></body></html>`, helper)
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "chat-pruned-earlier", 5000, 20*time.Second)
}
