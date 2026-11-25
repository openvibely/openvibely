package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestProjectTabsRegroupWhileServerModalOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetDesktopMode(true)
	h.insightsSvc = service.NewInsightsService(stores.Insights, stores.Tasks, stores.Projects, stores.LLMConfigs)
	h.broadcaster = events.NewBroadcaster()
	runner := `<script>
(async()=>{
 const pause=ms=>new Promise(r=>setTimeout(r,ms));
 try {
 for(let i=0;i<200&&!window.openVibelyProjectTabsRender;i++)await pause(30);
 await pause(500);
 const sel=document.getElementById('project-selector');
 const local=sel.value;
 const group='__repo_123456789abcdef0';
 const dialog=document.createElement('dialog');dialog.id='server_modal';document.body.append(dialog);if(!location.hash)dialog.showModal();
 const original=window.fetch;
 let refreshed=false;
 window.fetch=function(input,options){
 if(String(input).includes('/ui/project-catalog')){
 refreshed=true;
 const html='<select data-project-catalog-options>'+sel.innerHTML+'<option value="'+group+'">OpenVibely on several copies</option></select><div data-project-catalog-results></div><script type="application/json" id="project-catalog-shared">'+JSON.stringify({[group]:[local,'remote-copy']})+'</scr'+'ipt><script type="application/json" id="project-catalog-shared-machines">{}</scr'+'ipt>';
 return Promise.resolve(new Response(html));
 }return original(input,options);
 };
 window.dispatchEvent(new CustomEvent('openvibely-servers-changed'));
 await pause(500);
 if(!refreshed||window.openVibelyProjectTab(local)!==group)throw Error('catalog update not processed');
 await pause(500);
 const tabs=Array.from(document.querySelectorAll('[data-project-tab]')).map(x=>x.dataset.projectTab);
 console.log(JSON.stringify({local,selected:sel.value,url:location.href,tabs}));
 if(sel.value!==group)throw Error('page did not consolidate');
 if((!location.hash&&!dialog.open)||!dialog.isConnected)throw Error('modal lost');
 if(document.getElementById('tasks-page').dataset.projectId!==group)throw Error('task scope stale');
 if(!document.querySelector('[data-regroup-board]'))throw Error('board not refreshed');
 const add=document.querySelector('a[aria-label="Add Task"]');
 if(new URL(add.href).searchParams.get('project_id')!==group)throw Error('Add Task link retains old scope');
 if(new URL(add.getAttribute('hx-get'),location.href).searchParams.get('project_id')!==group)throw Error('Add Task request retains old scope');

 if(tabs.includes(local)||tabs.filter(id=>id===group).length!==1)throw Error('duplicate tabs: '+JSON.stringify(tabs));
 await original('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)})}
})();
</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/events/ws" {
			app.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/browser-publish" {
			h.broadcaster.Publish(events.TaskEvent{Type: events.TaskEventType(r.URL.Query().Get("type")), ProjectID: r.URL.Query().Get("project")})
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8000))
			select {
			case result <- string(body):
			default:
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/tasks" && r.URL.Query().Get("project_id") == "__repo_123456789abcdef0" && r.URL.Query().Get("refresh_source") == "sse" {
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<div id="kanban-board" data-regroup-board>Combined tasks</div>`)
			return
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
	}))
	defer server.Close()
	for _, size := range []string{"open", "closed"} {
		t.Run(size, func(t *testing.T) {
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", server.URL+"/tasks?project_id=default"+map[string]string{"closed": "#closed"}[size])
			require.NoError(t, startHandlerBrowserProcess(t, cmd))
			defer stopHandlerBrowserProcess(t, cmd)
			select {
			case outcome := <-result:
				require.Equal(t, "pass", outcome)
			case <-time.After(50 * time.Second):
				t.Fatal("browser timed out")
			}
		})
	}
}
