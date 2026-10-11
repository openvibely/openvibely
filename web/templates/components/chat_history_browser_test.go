package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_ComposerHistoryAcrossTasks(t *testing.T) {
	for _, mode := range []string{"away", "returned", "edited"} {
		t.Run(mode, func(t *testing.T) { testComposerHistoryAcrossTasks(t, mode) })
	}
}

func testComposerHistoryAcrossTasks(t *testing.T, mode string) {
	chrome := testChromePath(t)
	forms := map[string]string{}
	for _, id := range []string{"a", "b"} {
		var buf bytes.Buffer
		err := TaskThreadView(&models.Task{ID: id, Status: models.StatusCompleted}, nil, nil, nil, nil, nil, false, 20).Render(context.Background(), &buf)
		if err != nil {
			t.Fatal(err)
		}
		forms[id] = buf.String()
	}
	var newForm bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{FormID: "task-thread-form", InputID: "task-message-input", PostEndpoint: "/tasks", TargetID: "main-content", DiscardDraftOnLeave: true}).Render(context.Background(), &newForm); err != nil {
		t.Fatal(err)
	}
	releaseSend := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/release":
			releaseOnce.Do(func() { close(releaseSend) })
			w.WriteHeader(http.StatusNoContent)
		case "/tasks":
			w.Header().Set("X-Created-Task-ID", "a")
			w.Header().Set("HX-Retarget", "#main-content")
			w.Header().Set("HX-Reswap", "innerHTML")
			_, _ = w.Write([]byte(forms["a"]))
		case "/tasks/a", "/tasks/b":
			html := forms[r.URL.Path[len("/tasks/"):]]
			if r.URL.Query().Get("refresh") != "" {
				html = strings.Replace(html, `data-thread-revision="`, `data-thread-revision="changed-`, 1)
			}
			_, _ = w.Write([]byte(html))
		case "/tasks/a/thread", "/tasks/b/thread":
			_ = r.ParseForm()
			if r.FormValue("message") == "task a second" {
				<-releaseSend
			}
			_, _ = w.Write([]byte(`<div>accepted</div>`))
		case "/":
			_, _ = fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending" hx-ext="morph"><script src="/static/vendor/htmx.min.js"></script><script src="%s"></script><main id="main-content" hx-history-elt>%s</main><div id="browser-result">pending</div><script>
window.addEventListener('load', async function() {
 try {
  function input() { return document.getElementById('task-message-input'); }
  function key(name) { input().dispatchEvent(new KeyboardEvent('keydown', {key:name,bubbles:true,cancelable:true})); }
  function check(value) { if (input().value !== value) throw new Error('expected '+JSON.stringify(value)+' got '+JSON.stringify(input().value)+' history='+localStorage.getItem('openvibely-task-thread-message-history-a')+' cursor='+input().selectionStart); }
  async function waitFor(check) { var start=performance.now(); while (!check()) { if(performance.now()-start>2000) throw new Error('timed out'); await new Promise(r=>setTimeout(r,20)); } }
  async function send(text) { input().value=text; input().dispatchEvent(new Event('input',{bubbles:true})); key('Enter'); await waitFor(()=>input().value===''); await new Promise(r=>setTimeout(r,50)); }
  async function visit(id) { var link=document.createElement('a'); link.setAttribute('hx-push-url','true'); document.body.appendChild(link); await htmx.ajax('GET','/tasks/'+id,{source:link,target:'#main-content',swap:'innerHTML'}); link.remove(); await new Promise(r=>setTimeout(r,50)); }
  await send('task a first'); await send('before refresh'); await htmx.ajax('GET','/tasks/a?refresh=1',{target:'#task-thread-view',swap:'morph:outerHTML'}); key('ArrowUp'); check('before refresh'); key('ArrowUp'); check('task a first'); key('ArrowDown'); check('before refresh'); key('ArrowDown'); check(''); input().value='task a second'; key('Enter');
  var mode = %q;
  // Finish A's send only after B is mounted with a separate draft.
  document.body.addEventListener('htmx:afterSwap', function release(event) {
    if (event.detail.target.id !== 'main-content') return;
    document.body.removeEventListener('htmx:afterSwap', release);
    input().value='task b draft'; input().dispatchEvent(new Event('input',{bubbles:true}));
    if (mode === 'away') fetch('/release');
  });
  await visit('b');
  if (mode !== 'away') {
    await visit('a'); check('task a second');
    if (mode === 'edited') {
      input().value='new draft'; input().dispatchEvent(new Event('input',{bubbles:true}));
    }
    await fetch('/release');
  }
  await waitFor(()=>JSON.parse(localStorage.getItem('openvibely-task-thread-message-history-a') || '[]').includes('task a second'));
  if (mode !== 'away') {
    check(mode === 'edited' ? 'new draft' : '');
    await visit('b');
    await visit('a'); check(mode === 'edited' ? 'new draft' : '');
    input().value=''; input().dispatchEvent(new Event('input',{bubbles:true}));
    key('ArrowUp'); check('task a second'); key('ArrowDown'); check('');
    document.body.dataset.testResult='pass';
    return;
  }
  check('task b draft');
  await send('task b only');
  history.back(); await waitFor(()=>location.pathname==='/'); await new Promise(r=>setTimeout(r,100)); check(''); key('ArrowUp'); check('task a second'); key('ArrowUp'); check('before refresh'); key('ArrowUp'); check('task a first'); key('ArrowDown'); check('before refresh'); key('ArrowDown'); check('task a second'); key('ArrowDown'); check('');
  // Restored composers must rebind draft listeners; cache attributes alone are insufficient.
  input().value='task a unsent'; input().dispatchEvent(new Event('input',{bubbles:true}));
  await visit('b'); key('ArrowUp'); check('task b only'); key('ArrowDown'); check('');
  await visit('a'); check('task a unsent'); input().setSelectionRange(input().value.length,input().value.length);
  key('ArrowUp'); check('task a second');
  input().setSelectionRange(input().value.length,input().value.length);
  key('ArrowDown'); check('task a unsent');
  input().value='first line\nsecond line'; input().dispatchEvent(new Event('input',{bubbles:true}));
  input().setSelectionRange(input().value.length,input().value.length);
  key('ArrowUp'); check('first line\nsecond line');
  input().setSelectionRange(3,3); key('ArrowUp'); check('task a second');
  key('ArrowDown'); check('first line\nsecond line');
  input().setSelectionRange(1,4); key('ArrowUp'); check('first line\nsecond line');
  await send('sent first line\nsent second line');
  key('ArrowUp'); check('sent first line\nsent second line'); key('ArrowDown'); check('');
  document.body.dataset.testResult='pass'; document.getElementById('browser-result').textContent='PASS';
 } catch(e) { document.body.dataset.testResult='fail'; document.body.dataset.testError=e.message; document.getElementById('browser-result').textContent='FAIL:'+e.message; }
});
</script></body></html>`, static.URL("vendor/idiomorph-ext.min.js"), newForm.String(), mode)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer releaseOnce.Do(func() { close(releaseSend) })
	runHeadlessChromeCDPFixture(t, chrome, server.URL+"/", "composer history across tasks", 1200, 700, 25*time.Second)
}

func TestBrowserFunctional_ComposerHistoryWithoutBrowserCache(t *testing.T) {
	for _, storage := range []string{"empty", "unavailable", "malformed", "read-only", "stale", "stale-session", "disjoint"} {
		t.Run(storage, func(t *testing.T) {
			chrome := testChromePath(t)
			forms := map[string]string{}
			for _, id := range []string{"a", "b"} {
				var buf bytes.Buffer
				executions := []models.Execution{
					{ID: id + "-first", TaskID: id, Status: models.ExecCompleted, IsFollowup: true, PromptSent: id + " first", Output: "answer"},
					{ID: id + "-second", TaskID: id, Status: models.ExecCompleted, IsFollowup: true, PromptSent: id + " second\nsecond line", Output: "answer"},
				}
				if err := TaskThreadView(&models.Task{ID: id, Status: models.StatusCompleted}, executions, nil, nil, nil, nil, false, 20).Render(context.Background(), &buf); err != nil {
					t.Fatal(err)
				}
				forms[id] = buf.String()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				switch r.URL.Path {
				case "/tasks/a", "/tasks/b":
					fmt.Fprint(w, forms[strings.TrimPrefix(r.URL.Path, "/tasks/")])
				case "/tasks/a/thread":
					fmt.Fprint(w, `<div>accepted</div>`)
				case "/":
					fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending">
<script src="/static/vendor/htmx.min.js"></script>
<script>
var storageMode = %q;
if (storageMode === 'stale') localStorage.setItem('openvibely-task-thread-message-history-a', JSON.stringify(['a first']));
if (storageMode === 'disjoint') localStorage.setItem('openvibely-task-thread-message-history-a', JSON.stringify(['older unloaded message']));
if (storageMode === 'stale-session') window._chatMessageHistories = {'openvibely-task-thread-message-history-a':['a first']};
if (storageMode === 'malformed') localStorage.setItem('openvibely-task-thread-message-history-a', '{');
if (storageMode === 'read-only') {
 localStorage.setItem('openvibely-task-thread-message-history-a', JSON.stringify(['a first','a second\nsecond line']));
 Storage.prototype.setItem = function() { throw new Error('quota exceeded'); };
}
if (storageMode === 'unavailable') {
 Storage.prototype.getItem = function() { throw new Error('storage unavailable'); };
 Storage.prototype.setItem = function() { throw new Error('storage unavailable'); };
}
</script><main id="main-content">%s</main><script>
window.addEventListener('load', async function() {
 try {
  function input() { return document.getElementById('task-message-input'); }
  function check(value) { if(input().value!==value) throw new Error('expected '+JSON.stringify(value)+' got '+JSON.stringify(input().value)); }
  function key(key) { input().dispatchEvent(new KeyboardEvent('keydown',{key:key,bubbles:true,cancelable:true})); }
  async function visit(id) { await htmx.ajax('GET','/tasks/'+id,{target:'#main-content',swap:'innerHTML'}); await new Promise(r=>setTimeout(r,50)); }
  function recall(id) {
   key('ArrowUp'); check(id+' second\nsecond line');
   key('ArrowUp'); check(id+' first');
   key('ArrowDown'); check(id+' second\nsecond line');
   key('ArrowDown'); check('');
  }
  recall('a'); await visit('b'); recall('b'); await visit('a'); recall('a');
  // Accepted messages must survive navigation even when storage writes fail.
  input().value='a just sent'; input().dispatchEvent(new Event('input',{bubbles:true})); key('Enter');
  var started=performance.now();
  while(input().value!=='') {
   if(performance.now()-started>2000) throw new Error('send did not complete');
   await new Promise(r=>setTimeout(r,20));
  }
  await visit('b'); recall('b'); await visit('a');
  key('ArrowUp'); check('a just sent'); key('ArrowUp'); check('a second\nsecond line');
  key('ArrowUp'); check('a first'); key('Escape'); check('');
  document.body.dataset.testResult='pass';
 } catch(e) { document.body.dataset.testResult='fail'; document.body.dataset.testError=e.message; }
});
</script></body></html>`, storage, forms["a"])
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			runHeadlessChromeCDPFixture(t, chrome, server.URL+"/", "history without browser cache", 1200, 700, 25*time.Second)
		})
	}
}
