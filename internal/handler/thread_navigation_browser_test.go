package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestHandler_GetTaskCachedRefreshIncludesBoundedThread(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Cached task")
	task := createTask(t, h, project.ID, "Cached task", func(task *models.Task) {
		task.Status = models.StatusCompleted
	})
	for i := 0; i < 8; i++ {
		ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = fmt.Sprintf("prompt-%d", i)
			ex.StartedAt = time.Unix(int64(i+1), 0)
		})
		require.NoError(t, h.execRepo.Complete(t.Context(), ex.ID, models.ExecCompleted, fmt.Sprintf("output-%d", i), "", 0, 0))
	}
	// Ordinary navigation retains lazy loading; only warm refreshes opt in.
	cold := htmxGet(e, "/tasks/"+task.ID)
	assertCode(t, cold, http.StatusOK)
	require.Contains(t, cold.Body.String(), "Thread is loading...")

	for _, tab := range []string{"chat", "details", "changes"} {
		t.Run(tab, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"?tab="+tab+"&poll=1&before=missing&limit=1", nil)
			req.Header.Set("HX-Request", "true")
			req.Header.Set("X-OpenVibely-Thread-Refresh", "true")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			assertCode(t, rec, http.StatusOK)
			body := rec.Body.String()
			require.Contains(t, body, `data-initial-tab="`+tab+`"`)
			require.Contains(t, body, `id="task-thread-view"`)
			require.Contains(t, body, `id="task-thread-form"`)
			require.NotContains(t, body, "Thread is loading...")
			require.Contains(t, body, "output-7")
			require.Contains(t, body, "output-3")
			require.NotContains(t, body, "output-2")
		})
	}
}

// Exercise the real task endpoint and its lazy /thread request, rather than a
// fixture that always embeds the transcript in every navigation response.
func TestTaskCachedNavigationRefreshInChrome(t *testing.T) {
	testTaskCachedNavigationRefresh(t, false)
}

func TestTaskLazyCachedNavigationInChrome(t *testing.T) {
	testTaskCachedNavigationRefresh(t, true)
}

func testTaskCachedNavigationRefresh(t *testing.T, large bool) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Thread navigation")
	task := createTask(t, h, project.ID, "Cached task", func(task *models.Task) {
		task.Status = models.StatusCompleted
		task.Category = models.CategoryCompleted
	})
	other := createTask(t, h, project.ID, "Other task")
	ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Read tool output"
	})
	output := "[Using tool: shell | echo example]\n[Tool shell done]\n" + strings.Repeat("line of tool output\n", 200) + "[/Tool]\n"
	if large {
		output = strings.Repeat("**Earlier message**\n"+output, 180)
	}
	require.NoError(t, h.execRepo.Complete(t.Context(), ex.ID, models.ExecCompleted, output, "", 0, 0))
	path := "/tasks/" + task.ID
	runner := fmt.Sprintf(`<script>
window.addEventListener('DOMContentLoaded',function(){
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 const assert=(ok,message)=>{if(!ok)throw Error(message)};
 async function until(check,label){for(let i=0;i<400;i++){if(check())return;await wait(10)}throw Error('Timeout: '+label)}
 let parsedSources=0;
 const originalLink=window.linkStreamingToolResults;
 window.linkStreamingToolResults=function(...args){parsedSources++;return originalLink.apply(this,args)};
 const view=()=>document.getElementById('task-thread-view');
 const toggle=()=>document.querySelector('.stream-tool-output-toggle');
 async function checkToggle(){
   await until(()=>toggle() && toggle().isConnected,'tool hydration');
   await until(()=>document.querySelector('.chat-stream-content')._renderedRevision,'render revision');
   assert(getComputedStyle(document.getElementById('task-thread-messages')).visibility==='visible','rendered transcript is hidden');
   const button=toggle(), expanded=button.getAttribute('aria-expanded');
   button.click(); assert(button.getAttribute('aria-expanded')!==expanded,'tool toggle lost its handler');
 }
 (async function(){
   await until(()=>view() && toggle(),'initial lazy thread');
   await checkToggle();
   assert(document.querySelectorAll('.stream-tool').length<40,'initial task page rendered offscreen tools');
   await openVibelyNavigate(%q);
   await until(()=>view()?.dataset.taskId===%q,'other thread');
   await fetch('/advance');
   let warmURL=%q;
   for(let round=0;round<2;round++){
     const beforeWarmParses=parsedSources;
     let done=false;
     const warm=openVibelyNavigate(warmURL).then(()=>{done=true});
     await until(()=>view()?.dataset.taskId===%q,'cached preview');
     assert(!done,'preview waited for network');
     await checkToggle();
     assert(parsedSources===beforeWarmParses,'cached preview reparsed the execution');
     assert(document.querySelectorAll('.stream-tool').length<40,'cached preview rendered offscreen tools');
     const form=document.getElementById('task-thread-form');
     if(round===0){
       assert(!form.querySelector('#pending-thread-inputs').textContent.includes('queued-while-away'),'preview unexpectedly has new queue');
       // Simulate stale active-turn controls in a cached composer.
       const action=form.querySelector('#task-thread-form-primary-action');
       action.dataset.composerRunning='true'; action.dataset.activeTurnId='old-turn';
       const guard=document.createElement('input'); guard.type='hidden'; guard.name='expected_turn_id'; guard.value='old-turn'; form.appendChild(guard);
     }
     window.taskWorkspace.togglePanel();
     const panelOpen=round===0;
     assert(document.getElementById('task-details-panel').hidden!==panelOpen,'panel did not toggle during refresh');
     const input=form.querySelector('textarea'); input.value='draft typed during refresh'; input.dispatchEvent(new Event('input',{bubbles:true}));
     const messages=document.getElementById('task-thread-messages');
     let readingHistory=false;
     if(round===1 && messages.querySelectorAll('[data-virtual-chat-row]').length>100){
       messages.dispatchEvent(new WheelEvent('wheel',{deltaY:-600,bubbles:true}));
       messages.scrollTop=Math.max(0,messages.scrollTop-600);
       await window.renderVisibleChatRows(messages); await wait(40);
       readingHistory=true;
     }
     let lost=false;
     const observer=new MutationObserver(()=>{if(!view() || document.getElementById('task-thread-form')!==form)lost=true});
     observer.observe(document.getElementById('main-content'),{childList:true,subtree:true});
     await warm; await wait(150); observer.disconnect();
     assert(!lost,'refresh removed the cached transcript or composer');
     assert(document.getElementById('task-thread-form')===form && input.value==='draft typed during refresh','refresh lost composer edits');
     await until(()=>Array.from(view().querySelectorAll('[data-raw-content]')).some(el=>el.getAttribute('data-raw-content').includes('authoritative-update')),'fresh server output');
     if(readingHistory){const freshMessages=document.getElementById('task-thread-messages');assert(freshMessages.scrollHeight-freshMessages.scrollTop-freshMessages.clientHeight>100,'refresh pulled the history reader to the bottom')}
     assert(document.getElementById('task-details-panel').hidden!==panelOpen,'refresh reset the chosen panel');
     assert(new URL(location.href).searchParams.get('tab')===(panelOpen?'details':'chat'),'refresh committed the wrong tab URL');
     assert(form.querySelector('#pending-thread-inputs').textContent.includes('queued-while-away'),'fresh queued input missing from preserved composer');
     assert(form.querySelector('#task-thread-form-primary-action').dataset.composerRunning==='false','stale running state survived refresh');
     assert(!form.querySelector('input[name="expected_turn_id"]'),'stale turn guard survived refresh');
     await checkToggle();
     if(round===0){warmURL=location.pathname+location.search;await openVibelyNavigate(%q);await until(()=>view()?.dataset.taskId===%q,'other thread again')}
     else {
       form.querySelector('#pending-thread-inputs button[aria-label="Cancel queued follow-up"]').click();
       await until(()=>!form.querySelector('#pending-thread-inputs').textContent.includes('queued-while-away'),'refreshed queue cancel handler');
       assert(input.value==='draft typed during refresh','queue action lost the draft');
     }
   }
   const currentTaskID=view().dataset.taskId;
   history.back();
   await until(()=>view() && view().dataset.taskId!==currentTaskID,'browser back to other task');
   history.forward();
   await until(()=>view()?.dataset.taskId===currentTaskID,'browser forward to task');
   await checkToggle(); await wait(100);
   assert(getComputedStyle(document.getElementById('task-thread-messages')).visibility==='visible','history restored a hidden transcript');
   assert(document.querySelectorAll('.stream-tool').length<40,'history restoration rendered offscreen tools');
   await fetch('/browser-result?status=pass');
 })().catch(e=>fetch('/browser-result?status='+encodeURIComponent(e.stack||String(e))));
});</script>`, "/tasks/"+other.ID, other.ID, path, task.ID, "/tasks/"+other.ID, other.ID)
	var threadRequests atomic.Int32
	var warmRequests atomic.Int32
	results := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			select {
			case results <- r.URL.Query().Get("status"):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/advance" {
			if err := h.execRepo.Complete(r.Context(), ex.ID, models.ExecCompleted, output+"\nauthoritative-update", "", 0, 0); err != nil {
				t.Error(err)
			}
			pending := &models.ThreadInput{
				Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID,
				RunExecutionID: ex.ID, AgentConfigID: agent.ID,
				InputMode: models.ThreadInputModeQueued, InputStatus: models.ThreadInputPending,
				Content: "queued-while-away",
			}
			if err := h.threadInputRepo.CreateQueued(r.Context(), pending); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/events") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == path+"/thread" {
			threadRequests.Add(1)
		}
		if r.URL.Path == path && r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
			warmRequests.Add(1)
			time.Sleep(500 * time.Millisecond)
		}
		if r.URL.Path == path && r.Header.Get("HX-Request") != "true" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1)))
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, startHandlerBrowserProcess(cmd))
	var outcome string
	select {
	case outcome = <-results:
	case <-ctx.Done():
		outcome = "timeout"
	}
	stopHandlerBrowserProcess(cmd)
	require.Equal(t, "pass", outcome, stderr.String())
	require.EqualValues(t, 2, warmRequests.Load(), "both returns should request an embedded thread")
	require.EqualValues(t, 1, threadRequests.Load(), "only the initial cold visit should fetch the lazy thread")
}
