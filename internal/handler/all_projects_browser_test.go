package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsNavigationInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	project := createProject(t, h, "Second browser project")
	task := models.Task{ProjectID: project.ID, Title: "Cross project browser task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(t.Context(), &task))
	runner := `<script>
 (async function(){
 function wait(predicate){return new Promise((resolve,reject)=>{let deadline=Date.now()+12000;(function poll(){if(predicate() && !document.querySelector('.htmx-request, .htmx-settling, .htmx-swapping'))return resolve();if(Date.now()>deadline)return reject(Error('timeout: '+predicate));setTimeout(poll,30)})();})}
 function assert(ok,msg){if(!ok)throw Error(msg)}
 try{
 await wait(()=>window.openVibelyProjectTabsSync && window.htmx);
 if(innerWidth<1024){const select=document.getElementById('project-selector');select.value='__all_projects__';select.dispatchEvent(new Event('change',{bubbles:true}));}else{document.querySelector('[data-project-tab="__all_projects__"]').click();}
 await wait(()=>document.querySelector('[data-all-projects-view="tasks"]'));
 assert(document.getElementById('project-selector').value==='__all_projects__','selection mismatch');
 const card=document.querySelector('[data-all-projects-task]');assert(card && card.textContent.includes('Cross project browser task'),'missing cross project task');
 assert(!window._sidebarSSE,'virtual project opened live connection');
 await wait(()=>document.querySelector('#task-list-groups [data-task-list-project]'));
 const row=document.querySelector('#task-list-groups [data-task-list-row]');
 assert(row.textContent.includes('Second browser project'),'task list missing project label');
 assert(new URL(row.href).searchParams.get('project_id')==='__all_projects__','task list lost workspace scope');
 if(innerWidth>=1024){
 assert(getComputedStyle(document.getElementById('task-list-panel')).display!=='none','task list hidden');
 row.click();
 await wait(()=>document.getElementById('task-detail-content'));
 assert(document.getElementById('project-selector').value==='__all_projects__','opening left-list task switched tabs');
 assert(document.getElementById('task-detail-content').dataset.projectId===row.querySelector('[data-task-list-project]').dataset.taskListProject,'task owner changed');
 assert(document.querySelector('#task-list-groups [data-task-list-project]'),'combined left list lost');
 document.getElementById('task-back-btn').click();
 await wait(()=>document.querySelector('[data-all-projects-view="tasks"]'));
 }

 document.querySelector('[data-all-projects-task]').click();
 await wait(()=>document.getElementById('task-detail-content'));
 assert(document.getElementById('project-selector').value==='__all_projects__','opening board task switched tabs');
 assert(new URL(location.href).searchParams.get('project_id')==='__all_projects__','task URL lost scope');
 const detail=document.getElementById('task-detail-content');
 const owner=detail.dataset.projectId;
 await htmx.ajax('PUT','/tasks/'+detail.dataset.taskId,{source:detail,target:'#main-content',swap:'innerHTML',values:{title:'Cross project browser task',prompt:'Saved from All projects',priority:'2',category:'backlog'}});
 await wait(()=>document.getElementById('task-detail-content'));
 assert(document.getElementById('task-detail-content').dataset.projectId===owner,'edit lost task owner');
 assert(document.getElementById('project-selector').value==='__all_projects__','edit switched tabs');

 document.getElementById('task-back-btn').click();
 await wait(()=>document.querySelector('[data-all-projects-view="tasks"]'));

 document.querySelector('nav[aria-label="All projects views"] a[href^="/analytics"]').click();
 await wait(()=>document.querySelector('[data-all-projects-usage]'));
 document.querySelector('nav[aria-label="All projects views"] a[href^="/automations"]').click();
 await wait(()=>document.querySelector('[data-all-projects-view="automations"]'));
 document.querySelector('nav[aria-label="All projects views"] a[href^="/tasks"]').click();
 await wait(()=>document.querySelector('[data-all-projects-task]'));
 document.querySelector('#all-projects-page a[href^="/tasks/new"]').click();
 await wait(()=>document.getElementById('all-projects-choose'));
 const destination=Array.from(document.querySelectorAll('#all-projects-choose a')).find(a=>a.textContent==='Second browser project');
 assert(destination,'missing project choice');destination.click();
 await wait(()=>location.pathname==='/tasks/new' && document.getElementById('project-selector').value!=='__all_projects__');
 assert(!document.getElementById('all-projects-choose'),'chooser did not navigate');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)+' url='+location.href+' select='+document.getElementById('project-selector').value+' content='+document.getElementById('main-content').innerText.slice(0,1000)})}
 })();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
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
	for _, size := range []string{"1280,900", "390,844"} {
		t.Run(size, func(t *testing.T) {
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+t.TempDir(), "--window-size="+size, server.URL+"/tasks?project_id=default")
			require.NoError(t, startHandlerBrowserProcess(cmd))
			defer stopHandlerBrowserProcess(cmd)
			select {
			case outcome := <-result:
				require.Equal(t, "pass", outcome)
			case <-time.After(50 * time.Second):
				t.Fatal("browser timed out")
			}
		})
	}
}

func TestAllProjectsDesktopRestoresSavedLocationInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetDesktopMode(true)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	require.NoError(t, h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, models.AllProjectsID))
	require.NoError(t, h.settingsRepo.Set(t.Context(), "ui.project_locations", `{"__all_projects__":"/analytics?project_id=__all_projects__&range=7d#usage"}`))
	runner := `<script>
 (async()=>{try{
 const deadline=Date.now()+12000;
 while(!document.querySelector('[data-all-projects-usage]') || document.querySelector('.htmx-request,.htmx-settling')){if(Date.now()>deadline)throw Error('restore timeout: '+location.href);await new Promise(r=>setTimeout(r,30));}
 if(location.pathname+location.search+location.hash!=='/analytics?project_id=__all_projects__&range=7d#usage')throw Error('wrong location '+location.href);
 if(document.getElementById('project-selector').value!=='__all_projects__')throw Error('wrong project');
 if(document.querySelector('select[name="range"]').value!=='7d')throw Error('lost range');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)})}})();
 </script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
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
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--user-data-dir="+t.TempDir(), "--window-size=1280,900", server.URL+"/chat")
	require.NoError(t, startHandlerBrowserProcess(cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(30 * time.Second):
		t.Fatal("desktop restoration timed out")
	}
}

func TestAllProjectsStaleFilterRecoversInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	project := createProject(t, h, "Second browser project")
	task := models.Task{ProjectID: project.ID, Title: "Cross project browser task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(t.Context(), &task))
	runner := `<script>
    (async()=>{try{
      while(!window.openVibelyProjectTabsSync || document.readyState!=='complete')await new Promise(r=>setTimeout(r,30));
      sessionStorage.setItem('openvibely.project-location.__all_projects__','/tasks?project_id=__all_projects__&filter_project=deleted-project');
      let finished=false;
      document.body.addEventListener('htmx:afterRequest',e=>{if((e.detail.requestConfig.path||'').includes('filter_project=deleted-project'))finished=true;});
      document.querySelector('[data-project-tab="__all_projects__"]').click();
      const deadline=Date.now()+10000;
      while(!finished || window.openVibelyProtectedProjectID){if(Date.now()>deadline)throw Error('request timeout');await new Promise(r=>setTimeout(r,30));}
      if(document.getElementById('project-selector').value!=='__all_projects__')throw Error('All projects tab failed to open: selection='+document.getElementById('project-selector').value+' URL='+location.href);
      if(new URL(location.href).searchParams.has('filter_project'))throw Error('stale filter remains in URL');
      if(!document.querySelector('[data-all-projects-view="tasks"]'))throw Error('aggregate page not loaded');
      await fetch('/browser-result',{method:'POST',body:'pass'});
    }catch(e){await fetch('/browser-result',{method:'POST',body:String(e)})}})();</script>`
	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
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
	for _, size := range []string{"1280,900"} {
		t.Run(size, func(t *testing.T) {
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+t.TempDir(), "--window-size="+size, server.URL+"/tasks?project_id=default")
			require.NoError(t, startHandlerBrowserProcess(cmd))
			defer stopHandlerBrowserProcess(cmd)
			select {
			case outcome := <-result:
				require.Equal(t, "pass", outcome)
			case <-time.After(50 * time.Second):
				t.Fatal("browser timed out")
			}
		})
	}
}

func TestAllProjectsStalePollRecoversInChrome(t *testing.T) {
	for _, scenario := range []string{"recover", "newer-navigation", "pending-navigation"} {
		t.Run(scenario, func(t *testing.T) { runAllProjectsStalePollScenario(t, scenario) })
	}
}

func runAllProjectsStalePollScenario(t *testing.T, scenario string) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	project := createProject(t, h, "Second browser project")
	task := models.Task{ProjectID: project.ID, Title: "Cross project browser task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(t.Context(), &task))
	runner := `<script>
    (async()=>{try{
      while(!window.openVibelyNavigate || document.readyState!=='complete')await new Promise(r=>setTimeout(r,30));
      const scenario=location.hash.slice(1);
      const project=Array.from(document.getElementById('project-selector').options).find(o=>o.textContent.trim()==='Second browser project').value;
      await window.openVibelyNavigate('/tasks?project_id=__all_projects__&filter_project='+project);
      while(document.querySelector('.htmx-settling,.htmx-request'))await new Promise(r=>setTimeout(r,30));
      const response=await fetch('/audit-delete-project',{method:'POST'});if(!response.ok)throw Error('fixture deletion failed');
      const results=document.getElementById('all-projects-results');
      if(scenario==='newer-navigation'){
        const pending=htmx.ajax('GET',results.getAttribute('hx-get'),{source:results,target:results,swap:'outerHTML',headers:{'X-Test-Hold-Poll':'true'}});
        await fetch('/test-wait-poll');
        await window.openVibelyNavigate('/tasks?project_id=default');
        await fetch('/test-release-poll',{method:'POST'});
        await pending;
        if(new URL(location.href).searchParams.get('project_id')!=='default' || document.getElementById('all-projects-page'))throw Error('stale poll replaced newer navigation');
      }else if(scenario==='pending-navigation'){
        const navigation=window.openVibelyNavigate('/tasks?project_id=default&hold_main=1');
        await fetch('/test-wait-poll');
        await htmx.ajax('GET',results.getAttribute('hx-get'),{source:results,target:results,swap:'outerHTML'});
        await fetch('/test-release-poll',{method:'POST'});
        await navigation;
        const deadline=Date.now()+10000;
        while(document.querySelector('.htmx-request,.htmx-settling')){if(Date.now()>deadline)throw Error('settle timeout');await new Promise(r=>setTimeout(r,30));}
        if(new URL(location.href).searchParams.get('project_id')!=='default' || document.getElementById('all-projects-page'))throw Error('old-view poll cancelled in-flight project navigation');
      }else{
      await htmx.ajax('GET',results.getAttribute('hx-get'),{source:results,target:results,swap:'outerHTML'});
      const deadline=Date.now()+10000;
      while(new URL(location.href).searchParams.has('filter_project') || document.querySelector('.htmx-request,.htmx-settling')){if(Date.now()>deadline)throw Error('recovery timeout');await new Promise(r=>setTimeout(r,30));}
      if(document.querySelector('select[name="filter_project"]').value!=='')throw Error('stale poll was not recovered: filter='+document.querySelector('select[name="filter_project"]').value+' url='+location.href);
      }
      await fetch('/browser-result',{method:'POST',body:'pass'});
    }catch(e){await fetch('/browser-result',{method:'POST',body:String(e)})}})();</script>`
	result := make(chan string, 1)
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/test-wait-poll" {
			select {
			case <-pollStarted:
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Second):
				http.Error(w, "poll not started", 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/test-release-poll" {
			close(releasePoll)
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/audit-delete-project" {
			if err := h.projectSvc.Delete(t.Context(), project.ID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if static.ServeAsset(w, r) {
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
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		if r.Header.Get("X-Test-Hold-Poll") == "true" || r.URL.Query().Get("hold_main") == "1" {
			close(pollStarted)
			select {
			case <-releasePoll:
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Second):
			}
		}
		for key, values := range rec.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
	}))
	defer func() {
		select {
		case <-releasePoll:
		default:
			close(releasePoll)
		}
		server.Close()
	}()
	suffix := "#" + scenario
	for _, size := range []string{"1280,900"} {
		t.Run(size, func(t *testing.T) {
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+t.TempDir(), "--window-size="+size, server.URL+"/tasks?project_id=default"+suffix)
			require.NoError(t, startHandlerBrowserProcess(cmd))
			defer stopHandlerBrowserProcess(cmd)
			select {
			case outcome := <-result:
				require.Equal(t, "pass", outcome)
			case <-time.After(50 * time.Second):
				t.Fatal("browser timed out")
			}
		})
	}
}
