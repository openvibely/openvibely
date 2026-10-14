package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_EarlierPrependPreservesMovingReader(t *testing.T) {
	chrome := testChromePath(t)
	for _, id := range []string{"chat-messages", "task-thread-messages"} {
		for _, anchoring := range []string{"auto", "none"} {
			t.Run(id+"/"+anchoring, func(t *testing.T) {
				var script, loader bytes.Buffer
				if err := ChatAutoScrollScript().Render(context.Background(), &script); err != nil {
					t.Fatal(err)
				}
				if err := ChatEarlierMessagesLoader(id, "/earlier", 20).Render(context.Background(), &loader); err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if static.ServeAsset(w, r) {
						return
					}
					w.Header().Set("Content-Type", "text/html")
					if r.URL.Path == "/earlier" {
						// Give continued wheel input time to change the tracker revision.
						time.Sleep(120 * time.Millisecond)
						fmt.Fprint(w, `<div id="older" data-execution-pair="true" style="height:500px">Older messages</div>`)
						return
					}
					fmt.Fprintf(w, `<!doctype html><html><head><style>
     .hidden { display:none } .messages {height:240px; overflow-y:auto; overflow-anchor:%s} .pair {height:400px}
    </style></head><body data-test-result="pending"><script src="/static/vendor/htmx.min.js"></script>
    <div id="%s" class="messages">%s<div id="anchor" class="pair" data-execution-pair="true">Reading here</div><div class="pair" data-execution-pair="true">Latest</div></div>%s<script>
    window.addEventListener('load', async function() {
     try {
      const messages = document.querySelector('.messages');
      const anchor = document.getElementById('anchor');
      const offset = () => anchor.getBoundingClientRect().top - messages.getBoundingClientRect().top;
      const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
      const check = (actual, expected, label) => { if (Math.abs(actual-expected)>1) throw new Error(label+': '+actual+' expected '+expected); };
      const tracker = window.resolveScrollTracker('scrollTracker_'+messages.id, messages);
      window.initChatEarlierLoader(messages);
      let finishHydration;
      window.applyChatBubbleTransforms = () => new Promise(resolve => { finishHydration = resolve; });
      const initialOffset = offset();
      const swapped = new Promise(resolve => document.body.addEventListener('htmx:afterSwap', resolve, {once:true}));
      messages.dispatchEvent(new WheelEvent('wheel', {deltaY:-200}));
      // Moving off the edge while the response is pending must not cancel compensation.
      messages.scrollTop = 8;
      messages.dispatchEvent(new Event('scroll'));
      await swapped;
      check(offset(), initialOffset-8, 'immediate prepend');
      // Aggressive input during cooperative rendering must remain relative to
      // the same message, even as an older message changes height above it.
      messages.dispatchEvent(new WheelEvent('wheel', {deltaY:-80}));
      messages.scrollTop -= 80;
      messages.dispatchEvent(new Event('scroll'));
      const readingOffset = offset();
      document.getElementById('older').style.height = '750px';
      await wait(60);
      check(offset(), readingOffset, 'hydration resize');
      // Streaming below the anchor must not affect its position.
      messages.lastElementChild.style.height = '900px';
      await wait(60);
      check(offset(), readingOffset, 'streaming below reader');
      finishHydration();
      await wait(80);
      check(offset(), readingOffset, 'settled prepend');
      if (!tracker.userScrolledUp) throw new Error('reader became pinned');
      if (messages._earlierAnchor || messages.dataset.earlierRequestLoading === 'true' || messages.style.overflowAnchor) throw new Error('transaction not cleaned up');
      if (messages.querySelector('[data-earlier-loader]')) throw new Error('final page retained loader');
      document.body.dataset.testResult = 'pass';
     } catch (error) { document.body.dataset.testResult = 'fail'; document.body.dataset.testError = String(error); }
    });</script></body></html>`, anchoring, id, loader.String(), script.String())
				}))
				defer server.Close()
				runHeadlessChromeCDPFixture(t, chrome, server.URL, "earlier prepend moving reader", 1000, 800, 20*time.Second)
			})
		}
	}
}
