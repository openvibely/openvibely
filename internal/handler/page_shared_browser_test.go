package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestPageSharedHistoryAndSteeringBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("browser regression")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not installed")
	}
	env := setupPluginPages(t)
	sendPageTest(t, env, env.inA, "oldest")
	first := env.starts[0]
	require.NoError(t, env.h.execRepo.Complete(t.Context(), first.ExecID, models.ExecCompleted, "oldest answer", "", 0, 0))
	for i := 0; i < 60; i++ {
		e := &models.Execution{TaskID: first.TaskID, AgentConfigID: env.agent.ID, Status: models.ExecCompleted, PromptSent: fmt.Sprintf("past-%d", i), IsFollowup: true}
		require.NoError(t, env.h.execRepo.Create(t.Context(), e))
		if i == 59 {
			var output strings.Builder
			for row := 0; row < 160; row++ {
				fmt.Fprintf(&output, "[Using tool: bash | echo row-%d]\n[Tool bash done]\nresult-%d\n[/Tool]\n", row, row)
			}
			require.NoError(t, env.h.execRepo.Complete(t.Context(), e.ID, models.ExecCompleted, output.String(), "", 0, 0))
		}
	}
	require.NoError(t, env.h.taskRepo.UpdateStatus(t.Context(), first.TaskID, models.StatusCompleted))
	sendPageTest(t, env, env.inA, "active")
	sendPageTest(t, env, env.inA, "queued")
	runner := `<script>(async function(){
 const wait=async(fn,label)=>{const until=Date.now()+6000;while(Date.now()<until){if(fn())return;await new Promise(r=>setTimeout(r,30));}throw Error(label);};
 try{
 if(document.readyState==='loading')await new Promise(r=>document.addEventListener('DOMContentLoaded',r,{once:true}));
 const panel=document.getElementById('page-side-chat');
 if(sessionStorage.getItem('shared-conversation-reloaded') && panel.hidden)throw Error('reload did not restore open page chat');
 if(panel.hidden)document.querySelector('[data-page-chat-toggle]').click();
 const box=document.getElementById('task-thread-messages');
 await wait(()=>box.querySelector('[data-earlier-loader]'),'history loader missing');
 await wait(()=>box.querySelector('[data-chat-virtual-owner]'),'shared virtualization missing');
 if(box.querySelectorAll('.stream-tool').length>=160)throw Error('all tool rows mounted');
 if(!sessionStorage.getItem('shared-conversation-reloaded')){
  sessionStorage.setItem('shared-conversation-reloaded','true');location.reload();return;
 }
 box.querySelector('[data-earlier-loader]').dispatchEvent(new CustomEvent('ov:load-earlier',{bubbles:true}));
 await wait(()=>document.getElementById('chat-execution-OLDEST'),'earlier history missing');
 await wait(()=>box.dataset.earlierRequestLoading!=='true','paging did not settle');
 await window.openVibelyPageChat.refresh();
 if(!document.getElementById('chat-execution-OLDEST'))throw Error('refresh discarded loaded history');
 const form=document.getElementById('task-thread-form');
 if(!form.dataset.steerEndpoint)throw Error('composer steering not wired');
 await wait(()=>form.querySelector('[aria-label="Edit message"]'),'queue editor missing');
 form.querySelector('[aria-label="Edit message"]').click();
 await wait(()=>document.getElementById('pending-input-editor'),'shared editor did not open');
 document.querySelector('#pending-input-editor [data-cancel]').click();
 await wait(()=>!document.getElementById('pending-input-editor'),'editor did not close');
 await window.openVibelyPageChat.refresh();
 form.querySelector('[aria-label="Convert queued follow-up to steering"]').click();
 await wait(()=>form.querySelector('[data-input-mode="steering"]'),'queued steer failed');
 if(!document.getElementById('chat-execution-OLDEST'))throw Error('steering refresh discarded history');
 document.querySelector('[data-page-chat-toggle]').click();
 document.querySelector('[data-page-chat-toggle]').click();
 await wait(()=>box.querySelector('[data-chat-virtual-owner]'),'reopening lost virtual rows');
 const done=await (await fetch('/complete-active')).json();
 window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:done}));
 await wait(()=>!window._taskThreadStreamingActive,'completion did not release stream ownership');
 window.dispatchEvent(new CustomEvent('sse-live-connected',{detail:{reconnected:true}}));
 await window.openVibelyPageChat.refresh();
 await wait(()=>document.querySelector('[data-execution-pair][data-exec-id="'+done.exec_id+'"]').dataset.execStatus==='completed','reconnect did not reconcile completion');
 if(!document.getElementById('chat-execution-OLDEST'))throw Error('reconnect discarded loaded history');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)});}
})();</script>`
	runner = strings.ReplaceAll(runner, "OLDEST", first.ExecID)
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

		if r.URL.Path == "/complete-active" {
			active := env.starts[1]
			if err := env.h.execRepo.Complete(r.Context(), active.ExecID, models.ExecCompleted, "finished answer", "", 0, 0); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "chat_response_done", "task_id": active.TaskID, "exec_id": active.ExecID, "completed_output": "finished answer", "status": "completed"})
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
