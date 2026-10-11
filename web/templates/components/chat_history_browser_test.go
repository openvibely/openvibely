package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx204)
		case "/release":
			releaseOnce.Do(func() { close(releaseSend) })
			w.WriteHeader(http.StatusNoContent)
		case "/tasks":
			w.Header().Set("X-Created-Task-ID", "a")
			w.Header().Set("HX-Retarget", "#main-content")
			w.Header().Set("HX-Reswap", "innerHTML")
			_, _ = w.Write([]byte(forms["a"]))
		case "/tasks/a", "/tasks/b":
			_, _ = w.Write([]byte(forms[r.URL.Path[len("/tasks/"):]]))
		case "/tasks/a/thread", "/tasks/b/thread":
			_ = r.ParseForm()
			if r.FormValue("message") == "task a second" {
				<-releaseSend
			}
			_, _ = w.Write([]byte(`<div>accepted</div>`))
		case "/":
			_, _ = fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending" hx-ext="morph"><script src="/htmx.js"></script><script src="%s"></script><main id="main-content" hx-history-elt>%s</main><div id="browser-result">pending</div><script>
window.addEventListener('load', async function() {
 try {
  function input() { return document.getElementById('task-message-input'); }
  function key(name) { input().dispatchEvent(new KeyboardEvent('keydown', {key:name,bubbles:true,cancelable:true})); }
  function check(value) { if (input().value !== value) throw new Error('expected '+JSON.stringify(value)+' got '+JSON.stringify(input().value)+' history='+localStorage.getItem('openvibely-task-thread-message-history-a')+' cursor='+input().selectionStart); }
  async function waitFor(check) { var start=performance.now(); while (!check()) { if(performance.now()-start>2000) throw new Error('timed out'); await new Promise(r=>setTimeout(r,20)); } }
  async function send(text) { input().value=text; input().dispatchEvent(new Event('input',{bubbles:true})); key('Enter'); await waitFor(()=>input().value===''); }
  async function visit(id) { var link=document.createElement('a'); link.setAttribute('hx-push-url','true'); document.body.appendChild(link); await htmx.ajax('GET','/tasks/'+id,{source:link,target:'#main-content',swap:'innerHTML'}); link.remove(); await new Promise(r=>setTimeout(r,50)); }
  await send('task a first'); await htmx.ajax('GET','/tasks/a',{target:'#task-thread-view',swap:'morph:outerHTML'}); input().value='task a second'; key('Enter');
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
  history.back(); await waitFor(()=>location.pathname==='/'); await new Promise(r=>setTimeout(r,100)); check(''); key('ArrowUp'); check('task a second'); key('ArrowUp'); check('task a first'); key('ArrowDown'); check('task a second'); key('ArrowDown'); check('');
  // Restored composers must rebind draft listeners; cache attributes alone are insufficient.
  input().value='task a unsent'; input().dispatchEvent(new Event('input',{bubbles:true}));
  await visit('b'); key('ArrowUp'); check('task b only'); key('ArrowDown'); check('');
  await visit('a'); check('task a unsent'); input().setSelectionRange(0,0);
  key('ArrowUp'); check('task a second'); key('ArrowDown'); check('task a unsent');
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
