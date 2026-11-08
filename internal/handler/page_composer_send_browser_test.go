package handler

import (
	"encoding/json"
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

func TestPageComposerSingleSendBrowser(t *testing.T) {
	for _, liveFirst := range []bool{true, false} {
		name := "send-response-first"
		if liveFirst {
			name = "live-event-first"
		}
		t.Run(name, func(t *testing.T) { testPageComposerSingleSendBrowser(t, liveFirst) })
	}
}
func testPageComposerSingleSendBrowser(t *testing.T, liveFirst bool) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	env := setupPluginPages(t)
	runner := `<script>(async function(){
 const wait=async(fn,label)=>{const until=Date.now()+6000;while(Date.now()<until){if(fn())return;await new Promise(r=>setTimeout(r,30));}throw Error(label);};
 try{
 if(document.readyState==='loading')await new Promise(r=>document.addEventListener('DOMContentLoaded',r,{once:true}));
 document.querySelector('[data-page-chat-toggle]').click();
 const form=document.getElementById('task-thread-form');
 const input=form.querySelector('textarea');
 input.value='analyze the data for me';input.dispatchEvent(new Event('input',{bubbles:true}));
 form.addEventListener('htmx:beforeRequest',async event=>{
  if(event.detail.elt!==form)return;
  let active;
  for(let i=0;i<100;i++){
   active=await (await fetch('/browser-active')).json();
   if(active.exec_id)break;
   await new Promise(r=>setTimeout(r,10));
  }
  window.dispatchEvent(new CustomEvent('sse-task-event',{detail:active}));
 },{once:true});
 form.requestSubmit(form.querySelector('button[type="submit"]'));
 await wait(()=>document.querySelector('#task-thread-messages [data-execution-pair]'),'no response');
 await new Promise(r=>setTimeout(r,1000));
 const pairs=document.querySelectorAll('#task-thread-messages [data-execution-pair]');
 if(pairs.length!==1)throw Error('Expected one pair, got '+pairs.length+' ids='+Array.from(pairs,p=>p.dataset.execId));
 if(!document.querySelector('#task-thread-form-primary-action[data-composer-running="true"]'))throw Error('Stop control missing');
 if(document.getElementById('task-thread-messages').textContent.includes('Ask a question about this page.'))throw Error('Empty placeholder remains');
 const frame=document.getElementById('plugin-frame');
 const priorSrc=frame.src;
 input.value='unsent follow-up';
 window.dispatchEvent(new CustomEvent('plugin-page-updated',{detail:{task_id:window.location.pathname.split('/').pop()}}));
 await wait(()=>frame.src!==priorSrc,'updated frame was not refreshed');
 if(document.getElementById('task-thread-form')!==form)throw Error('page update replaced the chat');
 if(input.value!=='unsent follow-up')throw Error('page update lost unsent composer text');
 if(document.querySelectorAll('#task-thread-messages [data-execution-pair]').length!==1)throw Error('page update lost chat turn');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)});}
})();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-active" {
			resolved, err := env.mgr.Resolve(r.Context(), env.inA.ID, env.projA.ID)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			taskID, _ := env.mgr.Conversation(resolved.Ref()).Store.TaskID(r.Context())
			active, _ := env.h.pageChatActiveExecution(r.Context(), taskID)
			data := map[string]string{"type": "task_thread_execution_started", "task_id": taskID}
			if active != nil {
				data["exec_id"] = active.ID
			}
			_ = json.NewEncoder(w).Encode(data)
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

		rec := httptest.NewRecorder()
		env.e.ServeHTTP(rec, r)
		if (liveFirst && r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/chat")) || (!liveFirst && strings.HasSuffix(r.URL.Path, "/fragment")) {
			time.Sleep(500 * time.Millisecond)
		} else if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/chat") {
			time.Sleep(100 * time.Millisecond)
		}
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
		require.Len(t, env.starts, 1)
	case <-time.After(30 * time.Second):
		t.Fatal("browser timed out")
	}
}
