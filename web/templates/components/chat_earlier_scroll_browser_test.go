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
						fmt.Fprint(w, `<div id="oldest" data-execution-pair="true" style="height:500px">Oldest messages</div><div id="older" data-execution-pair="true" style="height:500px">Older messages</div>`)
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
      messages.dispatchEvent(new WheelEvent('wheel', {deltaY:80}));
      messages.scrollTop += 80;
      messages.dispatchEvent(new Event('scroll'));
      const readingOffset = offset();
      document.getElementById('older').style.height = '750px';
      await wait(60);
      check(offset(), readingOffset, 'hydration resize');
      // Streaming below the anchor must not affect its position.
      messages.lastElementChild.style.height = '900px';
      await wait(60);
      check(offset(), readingOffset, 'streaming below reader');
      // Move into the newly loaded page before hydration completes.
      messages.dispatchEvent(new WheelEvent('wheel', {deltaY:-1000}));
      messages.scrollTop = 100;
      // Let the browser queue scroll while rendering changes in the same turn.
      const oldest = document.getElementById('oldest');
      const olderOffset = () => oldest.getBoundingClientRect().top - messages.getBoundingClientRect().top;
      const readingOlderOffset = olderOffset();
      document.getElementById('older').style.height = '1050px';
      await wait(60);
      check(olderOffset(), readingOlderOffset, 'hydration below newly visible reader');
      // Conversely, a queued downward scroll into the next row must retain
      // compensation for growth above that row in the very same turn.
      messages.dispatchEvent(new WheelEvent('wheel', {deltaY:500}));
      messages.scrollTop = 600;
      const older = document.getElementById('older');
      const nextOffset = () => older.getBoundingClientRect().top - messages.getBoundingClientRect().top;
      const readingNextOffset = nextOffset();
      oldest.style.height = '800px';
      await wait(60);
      check(nextOffset(), readingNextOffset, 'queued scroll with hydration above reader');
      finishHydration();
      await wait(80);
      check(nextOffset(), readingNextOffset, 'settled prepend');
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

func TestBrowserFunctional_EarlierLoaderContinuousUpwardInput(t *testing.T) {
	chrome := testChromePath(t)
	for _, id := range []string{"chat-messages", "task-thread-messages"} {
		for _, input := range []string{"wheel", "touch"} {
			t.Run(id+"/"+input, func(t *testing.T) {
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
						fmt.Fprint(w, loader.String()+`<div data-execution-pair="true" style="height:400px">Older</div>`)
						return
					}
					fmt.Fprintf(w, `<!doctype html><html><head><style>.hidden{display:none}</style></head>
<body data-test-result="pending"><script src="/static/vendor/htmx.min.js"></script>
<div id="%s" style="height:240px;overflow:auto">%s<div data-execution-pair="true" style="height:800px">Latest</div></div>%s<script>
window.addEventListener('load', async () => {
 try {
  const messages = document.getElementById(%q);
  const wait = ms => new Promise(resolve => setTimeout(resolve, ms));
  const until = async check => { for(let i=0;i<100;i++){ if(check())return; await wait(10); } throw new Error('timed out'); };
  window.resolveScrollTracker('scrollTracker_'+messages.id, messages);
  window.initChatEarlierLoader(messages);
  let requests=0, finishHydration;
  document.body.addEventListener('htmx:beforeRequest', () => requests++);
  window.applyChatBubbleTransforms = () => new Promise(resolve => { finishHydration=resolve; });
  // Keep every wheel event in one continuous gesture, without depending on CPU speed.
  const realNow = Date.now;
  Date.now = () => 10000;
  const input = %q;
  const touchEvent = (type, y) => { const event = new Event(type); Object.defineProperty(event, 'touches', {value:[{clientY:y}]}); messages.dispatchEvent(event); };
  let touchY=0;
  if(input==='touch') touchEvent('touchstart', touchY);
  const up = () => input==='wheel' ? messages.dispatchEvent(new WheelEvent('wheel', {deltaY:-100})) : touchEvent('touchmove', touchY+=10);
  up();
  await until(() => !!finishHydration);
  // Reach the next boundary while hydration is still active. No downward scroll,
  // click, or pause occurs between the two requests.
  messages.scrollTop=0;
  for(let i=0;i<5;i++){ up(); await wait(10); }
  if(requests!==1) throw new Error('overlapping earlier requests');
  finishHydration();
  await until(() => messages.dataset.earlierAnchorRestoring==='false');
  for(let i=0;i<5;i++){ messages.dispatchEvent(new Event('scroll')); await wait(10); }
  if(requests!==1) throw new Error('loaded another page without further input');
  finishHydration=null;
  for(let i=0;i<10 && requests===1;i++){ up(); await wait(20); }
  if(requests!==2) throw new Error('continuous upward input stuck at loader: '+requests+' requests');
  await until(() => !!finishHydration);
  finishHydration();
  await until(() => messages.dataset.earlierAnchorRestoring==='false');
  Date.now=realNow;
  document.body.dataset.testResult='pass';
 } catch(error) { document.body.dataset.testResult='fail'; document.body.dataset.testError=String(error); }
});</script></body></html>`, id, loader.String(), script.String(), id, input)
				}))
				defer server.Close()
				runHeadlessChromeCDPFixture(t, chrome, server.URL, "continuous earlier scrolling", 1000, 800, 20*time.Second)
			})
		}
	}
}
