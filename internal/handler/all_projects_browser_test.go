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
