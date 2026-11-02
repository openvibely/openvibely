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
	h.insightsSvc = service.NewInsightsService(stores.Insights, stores.Tasks, stores.Projects, stores.LLMConfigs, stores.Executions)
	h.broadcaster = events.NewBroadcaster()
	h.SetAutomationServices(service.NewAutomationGraphService(stores.Automations), nil)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	project := createProject(t, h, "Second browser project")
	h.SetAgentRepo(stores.Agents)
	scopedAgent := createRichTaskUIAgent(t, stores.Agents, "Destination project agent")
	scopedAgent.Scope = models.AgentScopeProject
	scopedAgent.ProjectID = project.ID
	require.NoError(t, stores.Agents.Update(t.Context(), scopedAgent))
	createRichTaskUIAgent(t, stores.Agents, "Shared draft agent")

	task := models.Task{ProjectID: project.ID, Title: "Cross project browser task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(t.Context(), &task))
	other := models.Task{ProjectID: "default", Title: "Other project task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(t.Context(), &other))
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(stores.Automations, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(stores.Automations, h.taskSvc, stores.Tasks, stores.Schedules, validator)
	h.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, nil)
	seed, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
	require.NoError(t, err)
	seed.Name = "Browser automation"
	seed.Nodes = []models.AutomationDraftNode{{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review changes", "category": "backlog", "priority": 2}}}
	_, err = compiler.Save(t.Context(), service.AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: seed})
	require.NoError(t, err)

	runner := `<script>
 (async function(){
 function wait(predicate){return new Promise((resolve,reject)=>{let deadline=Date.now()+15000;(function poll(){if(predicate() && !document.querySelector('.htmx-request, .htmx-settling, .htmx-swapping'))return resolve();if(Date.now()>deadline)return reject(Error('timeout: '+predicate));setTimeout(poll,30)})();})}
 function assert(ok,msg){if(!ok)throw Error(msg)}
 let liveReady=false;window.addEventListener('sse-live-connected',()=>liveReady=true);
 async function liveRefresh(target,type){await wait(()=>liveReady);const marker=document.createElement('span');marker.textContent='stale';document.querySelector(target).append(marker);await fetch('/browser-publish?project=` + project.ID + `&type='+type,{method:'POST'});await wait(()=>!marker.isConnected);}
 try {
 await wait(()=>window.openVibelyNavigate && document.getElementById('kanban-board'));
 async function navigate(path, selector){window.openVibelyNavigate(path+'?project_id=__all_projects__');await wait(()=>location.pathname===path && document.querySelector(selector));assert(document.getElementById('project-selector').value==='__all_projects__','lost workspace: '+path);assert(!document.getElementById('all-projects-results'),'custom page rendered');}
 await navigate('/tasks','#kanban-board');
 assert(document.getElementById('kanban-board').innerText.includes('Cross project browser task'),'missing second project');
 assert(document.getElementById('kanban-board').innerText.includes('Other project task'),'missing default');
 await liveRefresh('#kanban-board','task_board_updated');
 const card=document.querySelector('#kanban-board [data-task-id="` + task.ID + `"]');
 for(const button of [card.querySelector('[hx-post$="/run"]'),card.querySelector('[hx-delete]')]) {
  const config=await new Promise((resolve,reject)=>{
   const timer=setTimeout(()=>reject(Error('No task-card request')),3000);
   const capture=e=>{e.preventDefault();clearTimeout(timer);document.body.removeEventListener('htmx:beforeRequest',capture);resolve(e.detail.requestConfig)};
   document.body.addEventListener('htmx:beforeRequest',capture);button.click();
  });
  const scope=new URL(config.path,location.href).searchParams.get('project_id')||config.parameters.project_id;
  assert(scope==='__all_projects__','task-card mutation missing workspace: '+config.path+' params='+JSON.stringify(config.parameters));
 }
 assert(card,'native task card missing');
 const link=card.querySelector('a[href*="/tasks/"]');
 assert(link,'native task link missing');link.click();
 await wait(()=>location.pathname==='/tasks/` + task.ID + `' && document.getElementById('task-detail-content'));
 assert(document.getElementById('project-selector').value==='__all_projects__','task switched workspace');
 const results=document.createElement('div');
 results.textContent='"Created task" (backlog) [TASK_ID:` + task.ID + `]\n"Edited task" (updated: title) [TASK_EDITED:` + task.ID + `]';
 document.body.appendChild(results);
 window.convertTaskLinksInMessage(results);window.convertTaskEditLinksInMessage(results);
 const resultLinks=results.querySelectorAll('a');
 assert(resultLinks.length===2,'task result links missing');
 for(const resultLink of resultLinks){
  assert(new URL(resultLink.href).searchParams.get('project_id')==='__all_projects__','native task result link lost scope');
  const response=await fetch(resultLink.href);
  assert(response.ok,'native task result URL failed');
 }
 const savedFetch=window.fetch;let startURL='';
 window.fetch=function(url,options){startURL=String(url);return Promise.resolve(new Response(null,{status:204}));};
 results.querySelector('button').click();
 await wait(()=>results.querySelector('button').textContent.includes('Started'));
 window.fetch=savedFetch;
 assert(new URL(startURL,location.href).searchParams.get('project_id')==='__all_projects__','Start Task lost scope');
 results.remove();

 const owner=document.querySelector('#task-detail-metrics [data-task-project]');
 assert(owner && owner.textContent.includes('` + project.Name + `'),'task owner missing from Details');
 assert(owner.previousElementSibling.tagName==='H3' && owner.previousElementSibling.textContent==='Task','project must follow Task heading');

 await navigate('/upcoming','#upcoming-container');
 await navigate('/history','#history-container');
 await navigate('/schedule','#schedule-content');
 const scheduleProject=document.querySelector('#new_scheduled_task_modal [name=destination_project]');
 assert(scheduleProject,'inline schedule project missing');
 scheduleProject.value='` + project.ID + `';scheduleProject.dispatchEvent(new Event('change',{bubbles:true}));
 const scheduleAgent=document.querySelector('#new_scheduled_task_modal [name=agent_definition_id] option[value="` + scopedAgent.ID + `"]');
 assert(scheduleAgent && !scheduleAgent.disabled && !scheduleAgent.hidden,'destination schedule agent missing');

 await navigate('/alerts','#alerts-container');
 await navigate('/insights','#health-check-section');
 await navigate('/analytics','#analytics-data');
 await navigate('/automations','#automations-container');
 assert(document.querySelector('[data-card-list-toolbar]'),'native toolbar missing');
 assert(document.getElementById('automations-container').innerText.includes('Browser automation'),'missing automation');
 await liveRefresh('#automations-card-list','automation_definition_updated');
 document.querySelector('[data-automation-url]').click();
 await wait(()=>document.getElementById('automation-live') || document.getElementById('automation-builder'));
 assert(document.getElementById('project-selector').value==='__all_projects__','automation switched workspace');
 await navigate('/tasks/new','#new-task-project');
 assert(document.getElementById('new-task-project'),'inline destination missing');
 const projectRow=document.getElementById('new-task-project').closest('.inspector-property-row');
 assert(projectRow && projectRow.parentElement.id==='task-detail-metrics','project selector must share the task property grid');
 assert(projectRow.previousElementSibling.tagName==='H3' && projectRow.previousElementSibling.textContent==='Task','new project selector must follow Task heading');
 window.taskWorkspace.openPanel('details');
 assert(projectRow.tagName==='BUTTON','project must use property button');
 const arrow=projectRow.querySelector('[aria-hidden="true"]');
 assert(!arrow.classList.contains('opacity-60'),'project arrow must match other rows');
 arrow.click();
 const projectOption=document.querySelector('#task-property-picker [data-options="destination_project"] button[data-value="` + project.ID + `"]');
 await wait(()=>projectOption.getClientRects().length>0);
 projectOption.click();
 await wait(()=>document.getElementById('new-task-project').value==='` + project.ID + `');
 assert(projectRow.querySelector('[data-property-label]').textContent==='Second browser project','project label did not update');
 assert(new FormData(document.getElementById('task-thread-form')).get('destination_project')==='` + project.ID + `','destination not submitted');
 assert(document.getElementById('project-selector').value==='__all_projects__','project picker switched tabs');
 // A blocked Send resumes once the destination is selected, exactly once.
 const draftForm=document.getElementById('task-thread-form');
 const destination=document.getElementById('new-task-project');
 destination.value='';
 document.getElementById('task-message-input').value='Run after choosing the project';
 let resumedRequests=[];
 const captureSend=function(event){
  if(event.detail.elt!==draftForm)return;
  event.preventDefault();
  resumedRequests.push(event.detail.requestConfig.parameters);
 };
 draftForm.addEventListener('htmx:beforeRequest',captureSend);
 draftForm.requestSubmit();
 await wait(()=>projectOption.getClientRects().length>0);
 assert(resumedRequests.length===0,'request sent before choosing owner');
 projectOption.click();
 await wait(()=>resumedRequests.length===1);
 assert(resumedRequests[0].destination_project==='` + project.ID + `','resumed send lost project');
 assert(resumedRequests[0].category==='active','resumed send lost Active category');
 assert(resumedRequests[0].message==='Run after choosing the project','resumed send lost message');
 destination.dispatchEvent(new Event('change',{bubbles:true}));
 await new Promise(resolve=>setTimeout(resolve,80));
 assert(resumedRequests.length===1,'selection sent twice');
 // Cancelling the picker cancels the pending Send, not the draft.
 destination.value='';
 draftForm.requestSubmit();
 await wait(()=>projectOption.getClientRects().length>0);
 projectOption.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 projectRow.click();
 await wait(()=>projectOption.getClientRects().length>0);
 projectOption.click();
 await new Promise(resolve=>setTimeout(resolve,80));
 assert(resumedRequests.length===1,'cancelled send resumed on later selection');
 draftForm.removeEventListener('htmx:beforeRequest',captureSend);

 for (const scope of ['` + project.ID + `','__all_projects__']) {
  window.openVibelyNavigate('/tasks/` + task.ID + `?project_id='+scope);
  await wait(()=>document.getElementById('project-selector').value===scope && location.pathname==='/tasks/` + task.ID + `');
  for (const action of ['merge','rebase','abort','resolve']) {
   const detail={path:'/tasks/` + task.ID + `/worktree/'+action,verb:'post',parameters:{}};
   document.body.dispatchEvent(new CustomEvent('htmx:configRequest',{bubbles:true,detail:detail}));
   assert(new URL(detail.path,location.href).searchParams.get('project_id')===scope,'worktree action missing scope: '+scope+' '+action);
  }
 }



 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)+' url='+location.href})}
 })();</script>`
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
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size="+size, server.URL+"/tasks?project_id=default")
			require.NoError(t, startHandlerBrowserProcess(t, cmd))
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
	h.insightsSvc = service.NewInsightsService(stores.Insights, stores.Tasks, stores.Projects, stores.LLMConfigs, stores.Executions)
	h.SetDesktopMode(true)
	h.usageAnalyticsSvc = service.NewUsageAnalyticsService(stores.Usage, stores.LLMConfigs)
	require.NoError(t, h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, models.AllProjectsID))
	require.NoError(t, h.settingsRepo.Set(t.Context(), "ui.project_locations", `{"__all_projects__":"/analytics?project_id=__all_projects__&range=7d#usage"}`))
	runner := `<script>
 (async()=>{try{
 const deadline=Date.now()+12000;
 while(!document.querySelector('#analytics-data') || document.querySelector('.htmx-request,.htmx-settling')){if(Date.now()>deadline)throw Error('restore timeout: '+location.href);await new Promise(r=>setTimeout(r,30));}
 if(location.pathname+location.search+location.hash!=='/analytics?project_id=__all_projects__&range=7d#usage')throw Error('wrong location '+location.href);
 if(document.getElementById('project-selector').value!=='__all_projects__')throw Error('wrong project');
 if(document.getElementById('dateRange').value!=='7')throw Error('lost range');
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
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--window-size=1280,900", server.URL+"/chat")
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(cmd)
	select {
	case outcome := <-result:
		require.Equal(t, "pass", outcome)
	case <-time.After(30 * time.Second):
		t.Fatal("desktop restoration timed out")
	}
}
