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
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestThreadNavigationToListPagesInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	h, e, configs, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Stores.Automations), nil)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Thread navigation")
	task := createTask(t, h, project.ID, "Cached task", func(task *models.Task) {
		task.Status = models.StatusCompleted
		task.Category = models.CategoryCompleted
	})

	ex := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "Read tool output"
	})
	output := "[Using tool: shell | echo example]\n[Tool shell done]\n" + strings.Repeat("line of tool output\n", 200) + "[/Tool]\n"
	require.NoError(t, h.execRepo.Complete(t.Context(), ex.ID, models.ExecCompleted, output, "", 0, 0))
	path := "/tasks/" + task.ID
	runner := fmt.Sprintf(`<script>
const requests=[]; document.addEventListener('htmx:afterRequest',e=>requests.push([e.detail.requestConfig?.path,e.detail.xhr?.status]));
const errors=[]; window.addEventListener('error',e=>errors.push(e.message));
window.addEventListener('unhandledrejection',e=>errors.push(String(e.reason)));
window.addEventListener('DOMContentLoaded',function(){
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 const assert=(ok,message)=>{if(!ok)throw Error(message)};
 async function until(check,label){for(let i=0;i<400;i++){if(check())return;await wait(10)}throw Error('Timeout: '+label+' errors='+JSON.stringify(errors)+' requests='+JSON.stringify(requests)+' url='+location.href+' content='+document.getElementById('main-content').innerText.slice(0,1000))}
 (async function(){
   await until(()=>document.querySelector('#task-thread-view'),'initial thread');
   for(let round=0;round<2;round++){
     for(const [path,selector] of [['/tasks','#tasks-page'],['/automations','#automations-container']]){
       const link=document.querySelector('[data-nav-base="'+path+'"]');
       assert(link,'missing sidebar '+path); link.click();
       await until(()=>document.querySelector('#main-content '+selector),path+' navigation');
       await wait(200);
       const rect=document.querySelector(selector).getBoundingClientRect();
       assert(rect.width>100 && rect.height>100,path+' has collapsed layout '+JSON.stringify(rect));
       assert(location.pathname===path,path+' URL did not update');
     }
     await openVibelyNavigate(%q);
     await until(()=>document.querySelector('#task-thread-view'),'return to thread');
   }
   assert(!errors.length,'browser errors: '+JSON.stringify(errors));
   await fetch('/browser-result?status=pass');
 })().catch(e=>fetch('/browser-result?status='+encodeURIComponent(e.stack||String(e))));
});</script>`, path)

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
	require.EqualValues(t, 2, warmRequests.Load(), "returns from the list pages must exercise the thread cache")
	require.EqualValues(t, 1, threadRequests.Load(), "only the cold thread visit should use lazy loading")

}
