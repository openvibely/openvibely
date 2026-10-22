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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestTaskCompletedWhileProjectAwayInChrome(t *testing.T) { testTaskProjectReturn(t, "normal") }
func TestTaskProjectReturnIgnoresLateFragmentInChrome(t *testing.T) {
	testTaskProjectReturn(t, "fragment")
}
func TestTaskProjectReturnIgnoresLatePollInChrome(t *testing.T) { testTaskProjectReturn(t, "poll") }

func testTaskProjectReturn(t *testing.T, mode string) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome not found")
	}
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Running project")
	other := createProject(t, h, "Other project")
	otherTask := createTask(t, h, other.ID, "Other task", func(task *models.Task) { task.Status = models.StatusCompleted })
	otherExec := createExec(t, h, otherTask.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecCompleted })
	require.NoError(t, h.execRepo.Complete(t.Context(), otherExec.ID, models.ExecCompleted, "Other task answer", "", 0, 0))
	task := createTask(t, h, project.ID, "Finishes while away", func(task *models.Task) { task.Status = models.StatusRunning; task.Category = models.CategoryActive })
	for i := 0; i < 5; i++ {
		old := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
			ex.Status = models.ExecCompleted
			ex.PromptSent = fmt.Sprintf("old prompt %d", i)
			ex.StartedAt = time.Now().Add(time.Duration(i-10) * time.Hour)
		})
		require.NoError(t, h.execRepo.Complete(t.Context(), old.ID, models.ExecCompleted, strings.Repeat("Old answer paragraph.\n\n", 30), "", 0, 0))
	}
	current := &models.Execution{ID: "new-followup", TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "CURRENT RUN PROMPT", IsFollowup: true}
	path := "/tasks/" + task.ID
	url := path + "?project_id=" + project.ID
	runner := fmt.Sprintf(`<script>
window.addEventListener('DOMContentLoaded',()=>{
const mode=%q;
const wait=ms=>new Promise(r=>setTimeout(r,ms));
const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
const view=()=>document.getElementById('task-thread-view');
document.addEventListener('htmx:beforeRequest',e=>{
 const path=e.detail.requestConfig?.path||'';
 if ((mode==='fragment' && path.includes('/fragment')) || path.includes('late_poll=1')) {
   window._lateFragmentRequested=true;
   const source=e.detail.elt, xhr=e.detail.xhr;
   const finished=event=>{if(event.detail.xhr!==xhr)return;source.removeEventListener('htmx:afterRequest',finished);window._lateResponseFinished=true};
   source.addEventListener('htmx:afterRequest',finished);
 }
});
const messages=()=>document.getElementById('task-thread-messages');
const debug=()=>JSON.stringify({url:location.href,executions:Array.from(messages()?.querySelectorAll('[data-execution-pair]')||[],p=>({id:p.dataset.execId,status:p.dataset.execStatus})),top:messages()?.scrollTop,height:messages()?.scrollHeight,active:window._taskThreadStreamingActive});
async function until(fn,label){for(let i=0;i<600;i++){if(fn())return;await wait(10)}throw Error(label+' '+debug())}
(async()=>{
 await until(()=>messages() && !messages().hasAttribute('data-transcript-hydrating'),'initial transcript');
 await fetch('/start');
 window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_thread_execution_started',task_id:view().dataset.taskId,exec_id:'new-followup'}}));
 if(mode==='fragment') {
   await until(()=>window._lateFragmentRequested,'pending fragment');
   // A concurrent history snapshot can display the execution while its original
   // execution-start fragment is still in flight.
   await htmx.ajax('GET',location.pathname+'/thread?poll=1',{target:'#task-thread-view',swap:'morph:outerHTML'});
 }
 await until(()=>messages()?.querySelector('[data-exec-id="%s"]') && !messages().hasAttribute('data-transcript-hydrating'),'initial running transcript');
 if(mode==='poll') {
   // Exercise a request whose source, as well as target, will be detached.
   const source=document.createElement('button');view().appendChild(source);
   htmx.ajax('GET',location.pathname+'/thread?poll=1&late_poll=1',{source,target:'#task-thread-view',swap:'morph:outerHTML'});
   await until(()=>window._lateFragmentRequested,'pending poll');
 }
 await wait(100);
 messages().dispatchEvent(new WheelEvent("wheel",{deltaY:-600,bubbles:true}));messages().scrollTop=0;await wait(100);
 sessionStorage.setItem('openvibely.project-location.'+%q, %q);
 const selector=document.getElementById('project-selector');
 selector.value=%q; selector.dispatchEvent(new Event('change',{bubbles:true}));
 await until(()=>new URL(location.href).searchParams.get('project_id')===%q && view()?.dataset.taskId!==%q,'away');
 await fetch('/finish');
 document.querySelector('[data-project-tab="%s"]').click();
 await until(()=>location.pathname===%q && new URL(location.href).searchParams.get('project_id')===%q,'returned URL');
 await until(()=>view()?.dataset.taskStatus==='completed','completed status');
 await until(()=>messages()?.textContent.includes('LATEST COMPLETED ANSWER'),'latest answer rendered');
 await wait(300);
 const pair=messages().querySelector('[data-execution-pair][data-exec-id="%s"]');
 assert(pair && pair.dataset.execStatus==='completed','current execution missing or stale '+debug());
 assert(getComputedStyle(messages()).visibility==='visible','hidden transcript '+debug());
 assert(messages().scrollHeight-messages().scrollTop-messages().clientHeight<20,'return shows older execution '+debug());
 await fetch('/release-fragment');
 if(mode!=='normal') await until(()=>window._lateResponseFinished,'old request finished');
 await new Promise(requestAnimationFrame);
 assert(view().dataset.taskActive==='false','old callback reactivated the completed thread');
 assert(messages().querySelector('[data-execution-pair][data-exec-id="new-followup"]')?.textContent.includes('LATEST COMPLETED ANSWER'),'late response removed the current execution '+debug());
 await fetch('/browser-result?status=pass');
})().catch(e=>fetch('/browser-result?status='+encodeURIComponent(e.stack||e)));
});</script>`, mode, current.ID, other.ID, "/tasks/"+otherTask.ID+"?project_id="+other.ID, other.ID, other.ID, task.ID, project.ID, path, project.ID, current.ID)
	results := make(chan string, 1)
	var warm atomic.Int32
	releaseFragment := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseFragment) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			select {
			case results <- r.URL.Query().Get("status"):
			default:
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/release-fragment" {
			release()
			w.WriteHeader(204)
			return
		}
		if (mode == "fragment" && strings.HasSuffix(r.URL.Path, "/fragment")) || r.URL.Query().Get("late_poll") == "1" {
			select {
			case <-releaseFragment:
			case <-r.Context().Done():
				return
			}
		}
		if r.URL.Path == "/start" {
			if err := h.execRepo.Create(r.Context(), current); err != nil {
				t.Error(err)
			}
			if err := h.execRepo.AppendOutput(r.Context(), current.ID, strings.Repeat("**Partial work**\n[Using tool: shell | inspect]\n[Tool shell done]\nresult\n[/Tool]\n", 180)); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/finish" {
			if err := h.execRepo.Complete(r.Context(), current.ID, models.ExecCompleted, strings.Repeat("**Partial work**\n[Using tool: shell | inspect]\n[Tool shell done]\nresult\n[/Tool]\n", 180)+strings.Repeat("Latest response paragraph.\n\n", 10)+"LATEST COMPLETED ANSWER", "", 0, 0); err != nil {
				t.Error(err)
			}
			if err := h.taskRepo.UpdateStatus(r.Context(), task.ID, models.StatusCompleted); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == path && r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
			warm.Add(1)
			time.Sleep(500 * time.Millisecond)
		}
		if r.URL.Path == path && r.Header.Get("HX-Request") != "true" {
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, r)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-background-timer-throttling", "--no-first-run", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "profile"), server.URL+url)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, startHandlerBrowserProcess(cmd))
	var result string
	select {
	case result = <-results:
	case <-ctx.Done():
		result = "timeout"
	}
	stopHandlerBrowserProcess(cmd)
	require.Equal(t, "pass", result, stderr.String())
	require.EqualValues(t, 1, warm.Load(), "project return should refresh cached task")
}
