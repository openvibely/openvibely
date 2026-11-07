package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestPageQueuedMessageLiveCancellationBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	env := setupPluginPages(t)
	sendPageTest(t, env, env.inA, "active")
	first := env.starts[0]
	runner := `<script>(async function(){
 const wait=async(fn,label)=>{const until=Date.now()+6000;while(Date.now()<until){if(fn())return;await new Promise(r=>setTimeout(r,30));}throw Error(label);};
 try{
 if(document.readyState==='loading')await new Promise(r=>document.addEventListener('DOMContentLoaded',r,{once:true}));
 document.querySelector('[data-page-chat-toggle]').click();
 const form=document.getElementById('task-thread-form');
 await htmx.ajax('POST',form.getAttribute('hx-post'),{target:'#task-thread-messages',swap:'beforeend',values:{message:'queued follow-up'}});
 await wait(()=>document.querySelector('.queued-input-row'),'queued row missing');
 const row=document.querySelector('.queued-input-row');const id=row.dataset.threadInputId;const task=document.getElementById('task-thread-view').dataset.taskId;
 const cancelled=await fetch('/thread-inputs/'+id+'/cancel',{method:'POST'});
 if(!cancelled.ok)throw Error('Cancellation request failed: '+cancelled.status);
 window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_thread_input_cancelled',task_id:task,pending_input_id:id}}));
 // The shared listener must remove the row immediately, without polling.

 if(document.getElementById('thread-input-'+id))throw Error('Cancelled queued row still visible; data-task-id='+JSON.stringify(row.dataset.taskId));
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)});}
})();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(204)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/tasks/"+first.TaskID+"/thread") {
			t.Errorf("page bypassed its authorized adapter: %s", r.URL.String())
			http.Error(w, "wrong surface endpoint", 500)
			return
		}
		rec := httptest.NewRecorder()
		env.e.ServeHTTP(rec, r)
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		body := rec.Body.String()
		if r.URL.Path == "/p/"+env.inA.ID {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--user-data-dir="+t.TempDir(), server.URL+"/p/"+env.inA.ID+"?project_id="+env.projA.ID)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(30 * time.Second):
		t.Fatal("browser timed out")
	}
}
