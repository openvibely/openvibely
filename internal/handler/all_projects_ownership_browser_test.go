package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsOwnershipInChrome(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAgentRepo(stores.Agents)
	h.SetLifecycleRepo(stores.Lifecycle)
	h.SetWebhookRepo(stores.Webhooks)
	h.SetAgentSkillRoot(t.TempDir())
	a, b := createProject(t, h, "Owner Alpha"), createProject(t, h, "Owner Beta")
	for _, p := range []*models.Project{a, b} {
		p.RepoPath = t.TempDir()
		require.NoError(t, h.projectRepo.Update(t.Context(), p))
		require.NoError(t, stores.Agents.Create(t.Context(), &models.Agent{Name: p.Name + " agent", Scope: models.AgentScopeProject, ProjectID: p.ID, Key: p.ID + "-review", Model: "inherit", Enabled: true, SelectableAsPrimary: true, SystemPrompt: p.Name + " prompt"}))
		writeAgentOwnedSkillForTest(t, filepath.Join(p.RepoPath, ".openvibely"), p.ID+"-review", "owned-review", p.Name+" owned skill", p.Name+" skill body")
		writeStandaloneSkillWithBody(t, filepath.Join(p.RepoPath, ".openvibely"), "same-skill", "Same skill", "Same description", "project", p.Name+" body")
		require.NoError(t, stores.Webhooks.Create(t.Context(), &models.WebhookEndpoint{ProjectID: p.ID, Name: p.Name + " webhook", Enabled: true, DefaultPriority: 2}))
	}
	require.NoError(t, stores.Agents.Create(t.Context(), &models.Agent{Name: "Shared webhook agent", Scope: models.AgentScopeGlobal, Key: "shared-webhook-agent", Model: "inherit", Enabled: true, SelectableAsPrimary: true, SystemPrompt: "Shared"}))
	runner := `<script>(async function(){
 const wait=p=>new Promise((resolve,reject)=>{const end=Date.now()+15000;(function poll(){if(p()&&!document.querySelector('.htmx-request,.htmx-settling,.htmx-swapping'))return resolve();if(Date.now()>end)return reject(Error('timeout '+p));setTimeout(poll,30)})();});
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 try {
 await wait(()=>window.editSkillFromData&&window.openVibelyNavigate);
 const cards=()=>Array.from(document.querySelectorAll('[data-skill-handle="same-skill"][data-skill-project]'));
 assert(cards().length===2,'missing project skills');
 assert(cards().every(c=>c.querySelector('[data-project-label]')),'missing project badges');
 for(const owner of ['` + a.ID + `','` + b.ID + `']){
  const card=cards().find(c=>c.dataset.skillProject===owner);
  editSkillFromData(card);
  await wait(()=>document.getElementById('skill_body').disabled===false);
  const label=card.querySelector('[data-project-label]').textContent;
  assert(document.getElementById('skill_body').value.includes(label+' body'),'wrong skill body');
  assert(document.getElementById('skill_project_owner').value===owner,'wrong skill owner');
  document.getElementById('skill_modal').close();
 }
 window.openVibelyNavigate('/channels?project_id=__all_projects__');
 await wait(()=>document.querySelectorAll('[data-webhook-project]').length===2);
 for(const owner of ['` + a.ID + `','` + b.ID + `']){
  const card=document.querySelector('[data-webhook-project="'+owner+'"]');
  assert(card.querySelector('[data-project-label]'),'missing webhook project badge');
  openWebhookEditModal(card.dataset.webhookId);
  await wait(()=>document.getElementById('webhook_form').dataset.webhookDetailReady==='true');
  assert(document.querySelector('#webhook_form [name=project_id]').value===owner,'wrong webhook owner');
  assert(document.getElementById('webhook_project_owner').value===owner,'wrong webhook owner label');
  assert(document.getElementById('webhook_agent_list').textContent.includes(card.dataset.webhookName.replace(' webhook',' agent')),'owner Agent missing');
  assert(!document.getElementById('webhook_agent_list').textContent.includes(owner==='` + a.ID + `'?'Owner Beta agent':'Owner Alpha agent'),'foreign Agent offered in editor');
  closeWebhookModal();
 }
 openWebhookCreateModal();
 const ownerChoice=document.getElementById('webhook_project_owner');
 assert(!document.getElementById('webhook_agent_list').textContent.includes('Owner Alpha agent')&&!document.getElementById('webhook_agent_list').textContent.includes('Owner Beta agent'),'unassigned webhook offers project Agents');
 const shared=Array.from(document.querySelectorAll('#webhook_agent_list .webhook-agent-checkbox')).find(el=>el.parentElement.textContent.includes('Shared webhook agent'));shared.checked=true;shared.dispatchEvent(new Event('change',{bubbles:true}));
 ownerChoice.value='` + a.ID + `';ownerChoice.dispatchEvent(new Event('change',{bubbles:true}));
 assert(document.getElementById('webhook_agent_list').textContent.includes('Owner Alpha agent')&&!document.getElementById('webhook_agent_list').textContent.includes('Owner Beta agent'),'wrong creation Agent choices');
 const owned=Array.from(document.querySelectorAll('#webhook_agent_list .webhook-agent-checkbox')).find(el=>el.parentElement.textContent.includes('Owner Alpha agent'));
 owned.checked=true;owned.dispatchEvent(new Event('change',{bubbles:true}));
 ownerChoice.value='` + b.ID + `';ownerChoice.dispatchEvent(new Event('change',{bubbles:true}));
 serializeWebhookAgentData();
 assert(document.getElementById('webhook_agent_ids_hidden').value===shared.value,'project switch must clear foreign Agent and preserve global Agent');
 assert(document.getElementById('webhook_agent_list').textContent.includes('Owner Beta agent'),'new owner Agent missing');
 assert(!document.getElementById('webhook_agent_list').textContent.includes('Owner Alpha agent'),'previous owner Agent remains');
 closeWebhookModal();
 window.openVibelyNavigate('/agents?project_id=__all_projects__');
 await wait(()=>document.querySelectorAll('[data-agent-scope="project"]').length===2);
 for(const ownerName of ['Owner Alpha','Owner Beta']) {
  const card=Array.from(document.querySelectorAll('[data-agent-id]')).find(c=>c.dataset.agentName===ownerName+' agent');
  assert(card,'missing Agent');
  editAgentFromData(card);
  await wait(()=>!document.getElementById('agent_save_btn').disabled);
  assert(document.getElementById('agent_system_prompt').value===ownerName+' prompt','wrong Agent details');
 await wait(()=>document.getElementById('agent_skills_list').textContent.includes(ownerName+' owned skill'));
 assert(!document.getElementById('agent_skill_new_btn').disabled,'cannot manage Agent skills');
  await new Promise(resolve=>{const modal=document.getElementById('agent_modal');modal.addEventListener('close',resolve,{once:true});modal.close();});
 }

 openNewAgentModal();
 const scope=document.getElementById('advanced_scope');
 const ownerSelect=document.getElementById('agent-project-owner');
 assert(ownerSelect && ownerSelect.disabled,'global Agent must not submit an owner');
 scope.value='project'; scope.dispatchEvent(new Event('change',{bubbles:true}));
 assert(!ownerSelect.disabled && ownerSelect.required,'project selector not enabled');
 assert(!document.getElementById('agent-project-choice').classList.contains('hidden'),'project selector hidden');
 ownerSelect.value='` + b.ID + `';
 assert(new FormData(document.getElementById('agent_form')).get('agent_project_id')===ownerSelect.value,'owner missing from form');
 const originalFetch=window.fetch;
 const generatedURLs=[];
 window.fetch=function(url,options){if(String(url).startsWith('/agents/generate')) generatedURLs.push(String(url));return originalFetch.apply(this,arguments);};
 document.getElementById('agent_description').value='Browser audit';
 await generateAgentFromPrompt();
 assert(new URL(generatedURLs[0],location.href).searchParams.get('project_id')===ownerSelect.value,'generation ignored chosen owner');

 scope.value='global'; scope.dispatchEvent(new Event('change',{bubbles:true}));
 assert(!new FormData(document.getElementById('agent_form')).has('agent_project_id'),'global submits stale owner');
 await generateAgentFromPrompt();
 assert(new URL(generatedURLs[1],location.href).searchParams.get('project_id')==='__all_projects__','global generation reused stale owner');
 window.fetch=originalFetch;

 await new Promise(resolve=>{const modal=document.getElementById('agent_modal');modal.addEventListener('close',resolve,{once:true});modal.close();});
 assert(document.getElementById('project-selector').value==='__all_projects__','left All projects');
 await fetch('/browser-result',{method:'POST',body:'pass'});
 }catch(e){await fetch('/browser-result',{method:'POST',body:String(e.stack||e)})}
})();</script>`
	results := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8000))
			select {
			case results <- string(body):
			default:
			}
			w.WriteHeader(204)
			return
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, r)
		for k, values := range rec.Header() {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, strings.Replace(rec.Body.String(), "</body>", runner+"</body>", 1))
	}))
	defer server.Close()
	for _, size := range []string{"1280,900", "390,844"} {
		t.Run(size, func(t *testing.T) {
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--window-size="+size, server.URL+"/skills?project_id=__all_projects__")
			require.NoError(t, startHandlerBrowserProcess(t, cmd))
			defer stopHandlerBrowserProcess(cmd)
			select {
			case outcome := <-results:
				require.Equal(t, "pass", outcome)
			case <-time.After(50 * time.Second):
				t.Fatal("browser timed out")
			}
		})
	}
}
