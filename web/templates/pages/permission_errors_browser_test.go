package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_PermissionErrorsPreserveForms(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, kind := range []string{"organization", "agents", "models", "skills", "webhooks", "personality", "reviews", "yaml", "alerts"} {
		t.Run(kind, func(t *testing.T) {
			var page bytes.Buffer
			var component templ.Component = AgentsContent(nil, nil)
			if kind == "alerts" {
				component = AlertsContentPageWithState(nil, "project", 0, false, "", "", CardListState{})
			}
			if kind == "reviews" {
				component = components.DiffViewer("diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new\n", "task", nil, "inline")
			}
			if kind == "yaml" {
				component = AutomationBuilderContent(models.AutomationBuilderPage{Source: "blank", YAML: "name: Keep draft", Result: models.AutomationDraftResult{Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Test", AutomationType: "custom", AdapterKey: "custom"}}}, "project")
			}

			if kind == "personality" {
				component = AppSettingsContent("", "project", nil)
			}
			if kind == "webhooks" {
				component = templ.Raw(renderSettingsContentForWebhookTest(t, nil))
			}
			if kind == "skills" {
				component = SkillsContent(nil, true)
			}
			if kind == "models" {
				component = ModelsContent(nil, nil, false)
			}
			if kind == "organization" {
				component = OrganizationContent(templ.Raw(`<div id="organization-container"><form id="permission-form"><input value="Keep draft"></form><div id="organization-error" class="hidden"></div></div>`))
			}
			if err := component.Render(context.Background(), &page); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				fmt.Fprintf(w, `<!doctype html><html><body><script>window.permissionToasts=[];window.showToast=(message,status)=>permissionToasts.push({message,status});window.htmx={process(){}};</script><script src="%s"></script>%s</body></html>`, static.URL("permission-errors.js"), page.String())
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "permission-"+kind, func(browser *composerFocusCDP) {
				browser.waitFor("permission helper", `typeof window.openVibelyPermissionErrors`, "object")
				if kind == "alerts" {
					result := browser.evaluateAwait(`(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Access denied'}),{status:403});
 const details=document.createElement('details');details.open=true;details.dataset.alertDetailUrl='/alerts/example/details';
 details.innerHTML='<div data-alert-detail-container>Keep detail</div>';document.body.append(details);const before=details.innerHTML;
 loadAlertDetail(details);for(let i=0;i<100&&permissionToasts.length===0;i++)await new Promise(r=>setTimeout(r,10));await new Promise(r=>setTimeout(r,20));
 return permissionToasts.length===1&&permissionToasts[0].message==='Access denied'&&details.innerHTML===before?'ok':'alert changed';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
					return
				}

				if kind == "reviews" {
					result := browser.evaluateAwait(`(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Editor access required'}),{status:403});
 const row=document.createElement('tr');row.className='review-comment-form';row.dataset.commentId='comment';row.innerHTML='<td><textarea>Keep draft</textarea></td>';document.body.append(row);const before=row.outerHTML;
 updateReviewComment(row);for(let i=0;i<100&&permissionToasts.length<1;i++)await new Promise(r=>setTimeout(r,10));
 deleteReviewComment({stopPropagation(){},target:row});for(let i=0;i<100&&permissionToasts.length<2;i++)await new Promise(r=>setTimeout(r,10));
 submitReviewComment('task','file.go','1','new','Keep draft',row);for(let i=0;i<100&&permissionToasts.length<3;i++)await new Promise(r=>setTimeout(r,10));
 await new Promise(r=>setTimeout(r,20));
 return permissionToasts.length===3&&permissionToasts.every(t=>t.message==='Editor access required')&&row.isConnected&&row.outerHTML===before?'ok':'review changed';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
					return
				}
				if kind == "yaml" {
					result := browser.evaluateAwait(`(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Editor access required'}),{status:403});
 const editor=document.querySelector('[data-automation-yaml-editor]'),diagnostic=document.querySelector('[data-automation-yaml-diagnostic]');
 editor.value='name: Keep draft';const before=diagnostic.outerHTML;
 editor.dispatchEvent(new Event('input',{bubbles:true}));
 for(let i=0;i<100&&permissionToasts.length<1;i++)await new Promise(r=>setTimeout(r,10));
 await new Promise(r=>setTimeout(r,20));
 return permissionToasts.length===1&&permissionToasts[0].message==='Editor access required'&&diagnostic.outerHTML===before&&editor.value==='name: Keep draft'?'ok':'YAML changed';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
					return
				}

				if kind == "personality" {
					result := browser.evaluateAwait(`(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Admin access required'}),{status:403});
 const form=document.getElementById('personality_form'), modal=document.getElementById('personality_modal');
 document.getElementById('personality_prompt').value='Keep draft';const before=form.innerHTML;
 editPersonalityFromData({dataset:{personalityKey:'custom',personalityName:'Custom'}});
 for(let i=0;i<100&&permissionToasts.length===0;i++)await new Promise(r=>setTimeout(r,10));
 await new Promise(r=>setTimeout(r,20));
 return permissionToasts.length===1&&permissionToasts[0].message==='Admin access required'&&!modal.open&&form.innerHTML===before&&document.getElementById('personality_prompt').value==='Keep draft'?'ok':'personality UI changed';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
					return
				}

				if kind == "webhooks" {
					result := browser.evaluateAwait(`(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Maintainer access required'}),{status:403});
 const form=document.getElementById('webhook_form'),modal=document.getElementById('webhook_modal');document.getElementById('webhook_name').value='Keep webhook';const before=form.innerHTML;
 openWebhookEditModal('restricted');for(let i=0;i<100&&permissionToasts.length===0;i++)await new Promise(r=>setTimeout(r,10));
 return permissionToasts.length===1&&permissionToasts[0].message==='Maintainer access required'&&!modal.open&&form.innerHTML===before&&document.getElementById('webhook_name').value==='Keep webhook'?'ok':'webhook UI changed';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
					return
				}
				if kind == "skills" {
					browser.evaluate(`window.permissionAlerts=[];window.alert=m=>permissionAlerts.push(m);window.openVibelyConfirm=async()=>true;window.fetch=async()=>new Response(JSON.stringify({message:'Editor access required'}),{status:403});window.skillBefore=document.getElementById('skills-container').innerHTML;window.skillButton=document.createElement('button');skillButton.dataset.skillHandle='example';skillButton.dataset.skillScope='project';setSkillEnabled(skillButton,false);'started';`)
					browser.waitFor("enable denial toast", `String(permissionToasts.length)`, "1")
					browser.evaluate(`setSkillAlwaysUse(skillButton,true);'started';`)
					browser.waitFor("always-use denial toast", `String(permissionToasts.length)`, "2")
					if result := browser.evaluate(`permissionAlerts.length===0 && document.getElementById('skills-container').innerHTML===skillBefore && permissionToasts.every(t=>t.message==='Editor access required') ? 'ok' : 'failed'`); result != "ok" {
						t.Fatal(result)
					}
					result := browser.evaluateAwait(`(async()=>{
 const form=document.getElementById('skill_form'),modal=document.getElementById('skill_modal');modal.showModal();
 document.getElementById('skill_handle').value='example';document.getElementById('skill_body').value='Keep draft';
 const priorBody=document.getElementById('skill_body').value;
 editSkillFromData({dataset:{skillHandle:'denied',skillScope:'project'}});
 for(let i=0;i<100&&permissionToasts.length===2;i++)await new Promise(r=>setTimeout(r,10));
 if(permissionToasts.length!==3||document.getElementById('skill_body').value!==priorBody||document.getElementById('skill_handle').value!=='example')return 'denied details changed editor';
 for(const mode of ['create','edit']){
 form.dataset.mode=mode;form.dataset.originalHandle='example';submitSkillForm({preventDefault(){}});
 for(let i=0;i<100&&document.getElementById('skill_submit_btn').disabled;i++)await new Promise(r=>setTimeout(r,10));
 if(!modal.open||document.getElementById('skill_error').textContent||document.getElementById('skill_body').value!=='Keep draft')return 'skill form changed';
 }
 modal.close();const importer=document.getElementById('skill_import_modal');importer.showModal();
 const files=new DataTransfer();files.items.add(new File(['sample'],'SKILL.md'));document.getElementById('skill_import_files').files=files.files;
 submitSkillImportForm({preventDefault(){}});
 for(let i=0;i<100&&document.getElementById('skill_import_submit_btn').disabled;i++)await new Promise(r=>setTimeout(r,10));
 return permissionToasts.length===6&&importer.open&&!document.getElementById('skill_import_error').textContent&&document.getElementById('skill_import_files').files.length===1?'ok':'import changed';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
					return
				}
				result := browser.evaluate(`(()=>{
 const form=document.querySelector('#organization-container form')||document.getElementById('agent_form')||document.getElementById('model_form');
 if(!form)return 'missing form';
 const target=document.getElementById('agents-container')||form;
 const modal=document.getElementById('agent_modal');if(modal?.open)modal.close();
 const before=form.innerHTML;
 const message='Organization administrator required';
 const xhr={status:403,responseText:JSON.stringify({message}),getResponseHeader:()=>null};
 form.dispatchEvent(new CustomEvent('htmx:responseError',{bubbles:true,detail:{elt:form,target,xhr}}));
 if(permissionToasts.length!==1||permissionToasts[0].message!==message)return 'missing permission toast';
 if(form.innerHTML!==before||modal?.open)return 'form changed or modal opened';
 const inline=document.getElementById('organization-error')||document.getElementById('model_form_error');if(inline&&!inline.classList.contains('hidden'))return 'inline permission error';
 // HTMX already dispatched the server toast before responseError.
 xhr.getResponseHeader=()=>JSON.stringify({openvibelyToast:{message,status:'failed'}});
 form.dispatchEvent(new CustomEvent('htmx:responseError',{bubbles:true,detail:{elt:form,target,xhr}}));
 if(permissionToasts.length!==1)return 'duplicate toast';
 return 'ok';})()`)
				if result != "ok" {
					t.Fatal(result)
				}
				if kind == "models" {
					result := browser.evaluateAwait(`(async()=>{
 const originalFetch=window.fetch;window.fetch=async()=>new Response(JSON.stringify({message:'Admin access required'}),{status:403});permissionToasts.length=0;
 const button=document.getElementById('model_oauth_connect_action');button.dataset.desktop='true';
 document.getElementById('new_model_modal').showModal();document.getElementById('model_name').value='Keep model';
 await connectModelOAuth(button);await startCustomModelOAuth(button);
 const ok=permissionToasts.length===2&&permissionToasts.every(t=>t.message==='Admin access required')&&!document.getElementById('model_oauth_connection_status').textContent&&!document.getElementById('model_oauth_setup_status').textContent&&document.getElementById('model_form_error').classList.contains('hidden')&&document.getElementById('model_name').value==='Keep model'&&!button.disabled;
 if(!ok)return 'OAuth changed form';
 window.alert=()=>{throw Error('Unexpected browser alert')};
 const count=permissionToasts.length;editModelFromData({dataset:{modelId:'restricted'}});
 for(let i=0;i<100&&permissionToasts.length===count;i++)await new Promise(r=>setTimeout(r,10));
 if(permissionToasts.length!==count+1)return 'missing edit toast';
 document.getElementById('model_provider').value='ollama';
 const select=document.getElementById('model_id');select.replaceChildren(new Option('Keep model','keep-model'));select.value='keep-model';
 discoverOllamaModels();
 for(let i=0;i<100&&permissionToasts.length===count+1;i++)await new Promise(r=>setTimeout(r,10));
 await new Promise(r=>setTimeout(r,20));
 if(permissionToasts.length!==count+2||select.value!=='keep-model'||document.getElementById('openai_compatible_discovery_status').textContent)return 'discovery changed form';
 discoverOllamaContext();
 for(let i=0;i<100&&permissionToasts.length===count+2;i++)await new Promise(r=>setTimeout(r,10));
 if(permissionToasts.length!==count+3)return 'missing context toast';
 window.fetch=originalFetch;return 'ok';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
				}
				if kind == "agents" {
					result := browser.evaluateAwait(`(async()=>{
 const originalFetch=window.fetch;window.fetch=async()=>new Response(JSON.stringify({message:'Admin access required'}),{status:403});window.openVibelyConfirm=async()=>true;
 permissionToasts.length=0;document.getElementById('plugin_marketplace_source').value='example';
 for(const action of [()=>addPluginMarketplace(),()=>resetDefaultPluginMarketplaces(),()=>updatePluginMarketplace('example'),()=>deletePluginMarketplace('example')]) {
 await action();if(document.getElementById('plugin_marketplace_action_status').textContent)return 'inline marketplace error';
 }
 if(permissionToasts.length!==4)return 'wrong marketplace toast count';
 document.getElementById('agent_description').value='Keep this draft';document.getElementById('advanced_scope').value='user';await generateAgentFromPrompt();
 if(permissionToasts.length!==5||permissionToasts.some(t=>t.message!=='Admin access required'))return 'wrong denial message';
 if(document.getElementById('agent_description').value!=='Keep this draft')return 'draft changed';
 deleteAgentID='denied';const confirmation=document.getElementById('delete_agent_confirm_modal');confirmation.showModal();
 await confirmDeleteAgent();if(!confirmation.open||permissionToasts.length!==6||permissionToasts[5].message!=='Admin access required')return 'delete denial changed modal';confirmation.close();
 activeAgentID='denied';const hooks=document.getElementById('lifecycle_hooks_list');hooks.textContent='Keep hooks';const status=document.getElementById('lifecycle_hooks_status'),previous=status.outerHTML;
 await loadLifecycleHooksForAgent('denied',agentModalGeneration);
 if(permissionToasts.length!==7||hooks.textContent!=='Keep hooks'||status.outerHTML!==previous)return 'hooks changed';
 const skills=document.getElementById('agent_skills_list');skills.textContent='Keep skills';await loadAgentSkillsForAgent('denied');
 if(permissionToasts.length!==8||skills.textContent!=='Keep skills')return 'skills list changed';
 agentPluginState={marketplaces:[],installed:[],available:[],runtime:[]};renderPluginCatalog();const pluginState=agentPluginState;
 await loadPluginState(true);
 if(permissionToasts.length!==9||agentPluginState!==pluginState||document.getElementById('plugin_marketplace_action_status').textContent)return 'plugin catalog changed';
 window.fetch=originalFetch;return 'ok';})()`)
					if result != "ok" {
						t.Fatal(result)
					}
				}
				result = browser.evaluateAwait(`(async()=>{
 permissionToasts.length=0;
 const response=new Response(JSON.stringify({message:'Viewer access cannot perform this action'}),{status:403});
 if(!await openVibelyPermissionErrors.fetch(response))return 'denial not handled';
 if(permissionToasts.length!==1||!permissionToasts[0].message.includes('Viewer access'))return 'missing fetch toast';
 if(!(await response.json()).message)return 'response consumed';
 if(await openVibelyPermissionErrors.fetch(new Response('bad input',{status:400})))return 'validation incorrectly handled';
 if(permissionToasts.length!==1)return 'unexpected toast';
 return 'ok';})()`)
				if result != "ok" {
					t.Fatal(result)
				}
			})
		})
	}
}
