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

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ThreadCacheAndVirtualRowsInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmx, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatal(err)
	}
	var base bytes.Buffer
	if err := layout.Base("Thread performance", nil, "project-test").Render(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	html := base.String()
	start := strings.Index(html, "// One owner for full-page fragment navigation")
	if start < 0 {
		t.Fatal("missing navigation owner")
	}
	end := strings.Index(html[start:], "// Scroll position restoration for drop zones")
	if end < 0 {
		t.Fatal("missing navigation end")
	}
	navigation := html[start : start+end]
	var deleted atomic.Bool
	var version atomic.Int32
	version.Store(1)
	fragment := func(name string) string {
		var rows strings.Builder
		for i := 0; i < 200; i++ {
			fmt.Fprintf(&rows, `<div class="stream-tool" data-row="%d"><button>Tool %d</button><pre>%s</pre></div>`, i, i, strings.Repeat("tool output\n", 4))
		}
		return fmt.Sprintf(`<section data-page="%s"><span data-openvibely-page-title="%s" hidden></span><div id="task-thread-view" data-task-id="%s" data-thread-revision="v%d"><span id="version">%d</span><div id="task-thread-messages"><div id="exec-%s" data-execution-pair="true" data-exec-id="exec-%s" data-exec-status="completed" data-execution-revision="stable">%s</div><div data-execution-pair="true" data-exec-status="running"><div class="stream-tool" id="live-row">live output</div></div></div><form id="task-thread-form"><textarea id="draft"></textarea></form></div></section><script>window.refreshVirtualChatRows(document.getElementById('task-thread-messages'));</script>`, name, name, name, version.Load(), version.Load(), name, name, rows.String())
	}
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
 const wait = ms => new Promise(r => setTimeout(r, ms));
 const page = () => document.querySelector('[data-page]')?.getAttribute('data-page');
 const messages = () => document.getElementById('task-thread-messages');
 const assert = (value, message) => { if (!value) throw new Error(message); };
 async function until(check, label) { for (let i=0;i<400;i++) { if(check())return; await wait(10); } throw new Error('Timeout: '+label); }
 (async function() {
   window.refreshVirtualChatRows(messages());
   await until(() => document.querySelectorAll('[data-virtual-chat-row]').length > 100, 'virtual rows');
   assert(document.querySelectorAll('.stream-tool').length < 40, 'too many mounted rows');
   assert(document.getElementById('live-row'), 'live output was detached');
   const height = messages().scrollHeight;
   messages().scrollTop = messages().scrollHeight;
   await until(() => document.querySelector('[data-row="199"]'), 'bottom row remounted');
   assert(Math.abs(messages().scrollHeight-height)<3, 'virtualization changed scroll height');
   messages().scrollTop = 0;
   await until(() => document.querySelector('[data-row="0"]'), 'first row remounted');
   document.querySelector('[data-row="0"] button').focus();
   messages().scrollTop = messages().scrollHeight;
   await wait(60);
   assert(document.querySelector('[data-row="0"]'), 'focused row detached');
   document.activeElement.blur();
   await window.openVibelyNavigate('/tasks/b');
   assert(page()==='b', 'cold navigation failed');
   await fetch('/version');
   let done = false;
   const warm = window.openVibelyNavigate('/tasks/a').then(()=>{done=true});
   await until(() => page()==='a', 'warm cached page');
   assert(!done, 'warm thread waited for network');
   assert(new URL(window.openVibelyDisplayedURL()).pathname==='/tasks/a', 'cached controls use outgoing URL');
   assert(location.pathname==='/tasks/b', 'preview committed URL before response');
   assert(document.getElementById('version').textContent==='1', 'did not show cached version');
   const form = document.getElementById('task-thread-form');
   document.getElementById('draft').value = 'typed during refresh';
   await warm;
   assert(document.getElementById('version').textContent==='2', 'authoritative refresh missing');
   assert(document.getElementById('task-thread-form')===form, 'composer was replaced');
   assert(document.getElementById('draft').value==='typed during refresh', 'draft lost');
   assert(location.pathname==='/tasks/a', 'warm URL not committed');
   history.back();
   await until(() => page()==='b' && location.pathname==='/tasks/b', 'back to b');
   history.forward();
   await until(() => page()==='a' && location.pathname==='/tasks/a', 'forward to a');
   await window.openVibelyNavigate('/tasks/b');
   const slow = window.openVibelyNavigate('/tasks/a').catch(()=>{});
   await until(() => page()==='a', 'warm a before race');
   const fast = window.openVibelyNavigate('/tasks/c');
   await fast; await slow; await wait(500);
   assert(page()==='c' && location.pathname==='/tasks/c', 'late response replaced destination');
   history.back();
   await until(() => page()==='b' && location.pathname==='/tasks/b', 'history after aborted warm navigation');
   history.forward();
   await until(() => page()==='c' && location.pathname==='/tasks/c', 'forward after aborted warm navigation');
   // Mutations invalidate cached snapshots, including edits made with HTMX.
   htmx.trigger(document.body, 'htmx:beforeRequest', {requestConfig:{verb:'post'}, target:document.body});
   const cold = window.openVibelyNavigate('/tasks/a');
   await wait(50);
   assert(page()==='c', 'mutation left a stale cached thread');
   await cold;
   document.dispatchEvent(new KeyboardEvent('keydown', {key:'f',ctrlKey:true,bubbles:true}));
   assert(document.querySelectorAll('[data-row]').length===200, 'find omitted offscreen messages');
   document.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape',bubbles:true}));
   // Capacity and idle expiry both fall back to the normal request path.
   for (let i=0;i<21;i++) await window.openVibelyNavigate('/tasks/entry-'+i);
   const evicted = window.openVibelyNavigate('/tasks/a');
   await wait(50); assert(page()==='entry-20', 'cache exceeded twenty entries'); await evicted;
   await window.openVibelyNavigate('/tasks/b');
   const realNow = Date.now;
   Date.now = () => realNow()+30*60*1000+1;
   const expired = window.openVibelyNavigate('/tasks/a');
   await wait(50); assert(page()==='b', 'expired cache was displayed'); await expired;
   Date.now = realNow;
   await window.openVibelyNavigate('/tasks/b');
   const tabRefresh = window.openVibelyNavigate('/tasks/a');
   await until(() => page()==='a', 'cached page before tab change');
   document.getElementById('draft').value = 'edited while changing tabs';
   assert(window.updateCachedThreadNavigationURL('/tasks/a?tab=changes'), 'pending tab update rejected');
   assert(new URL(window.openVibelyDisplayedURL()).searchParams.get('tab')==='changes', 'pending tab URL missing');
   await tabRefresh;
   assert(location.pathname==='/tasks/a' && location.search==='?tab=changes', 'pending tab URL not committed');
   assert(document.getElementById('draft').value==='edited while changing tabs', 'tab change lost local view');
   await window.openVibelyNavigate('/tasks/a');
   await window.openVibelyNavigate('/tasks/b');
   document.querySelector('[data-page]').setAttribute('hx-history','false');
   localStorage.removeItem('htmx-history-cache');
   await window.openVibelyNavigate('/tasks/a');
   assert(!(localStorage.getItem('htmx-history-cache')||'').includes('/tasks/b'), 'sensitive outgoing page leaked into history');
   await window.openVibelyNavigate('/tasks/b');
   await fetch('/delete');
   await window.openVibelyNavigate('/tasks/a').catch(()=>{});
   assert(!document.getElementById('task-thread-view'), 'deleted thread remained visible from cache');
   await fetch('/result?status=pass');
 })().catch(error => fetch('/result?status='+encodeURIComponent(error.stack||String(error))));
});
</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx)
		case "/delete":
			deleted.Store(true)
			w.WriteHeader(204)
		case "/version":
			version.Add(1)
			w.WriteHeader(204)
		case "/result":
			select {
			case result <- r.URL.Query().Get("status"):
			default:
			}
			w.WriteHeader(204)
		default:
			if !strings.HasPrefix(r.URL.Path, "/tasks/") {
				http.NotFound(w, r)
				return
			}
			name := strings.TrimPrefix(r.URL.Path, "/tasks/")
			w.Header().Set("Content-Type", "text/html")
			if r.Header.Get("HX-Request") == "true" {
				if name == "a" {
					time.Sleep(350 * time.Millisecond)
				}
				if name == "a" && deleted.Load() {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(fragment(name)))
				return
			}
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><script src="/htmx.js"></script><script src="%s"></script><script src="%s"></script><script>%s</script><style>#task-thread-messages{height:400px;overflow:auto}.stream-tool{padding:8px;margin:12px 0;border:1px solid}pre{margin:0}button{height:24px}</style>%s</head><body><main id="main-content" hx-history-elt>%s</main></body></html>`, static.URL("thread-navigation.js"), static.URL("chat-virtual-list.js"), navigation, runner, fragment(name))
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+"/tasks/a")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	var outcome string
	select {
	case outcome = <-result:
	case <-ctx.Done():
		outcome = "timeout"
	}
	stopBrowserProcess(cmd)
	if outcome != "pass" {
		t.Fatalf("thread performance: %s\n%s", outcome, stderr.String())
	}
}
