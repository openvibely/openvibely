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
		writeStandaloneSkillWithBody(t, filepath.Join(p.RepoPath, ".openvibely"), "same-skill", "Same skill", "Same description", "project", p.Name+" body")
		require.NoError(t, stores.Webhooks.Create(t.Context(), &models.WebhookEndpoint{ProjectID: p.ID, Name: p.Name + " webhook", Enabled: true, DefaultPriority: 2}))
	}
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
  closeWebhookModal();
 }
 window.openVibelyNavigate('/agents?project_id=__all_projects__');
 await wait(()=>document.querySelectorAll('[data-agent-scope="project"]').length===2);
 for(const ownerName of ['Owner Alpha','Owner Beta']) {
  const card=Array.from(document.querySelectorAll('[data-agent-id]')).find(c=>c.dataset.agentName===ownerName+' agent');
  assert(card,'missing Agent');
  editAgentFromData(card);
  await wait(()=>!document.getElementById('agent_save_btn').disabled);
  assert(document.getElementById('agent_system_prompt').value===ownerName+' prompt','wrong Agent details');
  await new Promise(resolve=>{const modal=document.getElementById('agent_modal');modal.addEventListener('close',resolve,{once:true});modal.close();});
 }
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
			cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+t.TempDir(), "--window-size="+size, server.URL+"/skills?project_id=__all_projects__")
			require.NoError(t, startHandlerBrowserProcess(cmd))
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
