package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_PermissionErrorsPreserveForms(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, kind := range []string{"organization", "agents", "models", "skills"} {
		t.Run(kind, func(t *testing.T) {
			var page bytes.Buffer
			var component templ.Component = AgentsContent(nil, nil)
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
				if kind == "skills" {
					browser.evaluate(`window.permissionAlerts=[];window.alert=m=>permissionAlerts.push(m);window.openVibelyConfirm=async()=>true;window.fetch=async()=>new Response(JSON.stringify({message:'Editor access required'}),{status:403});window.skillBefore=document.getElementById('skills-container').innerHTML;window.skillButton=document.createElement('button');skillButton.dataset.skillHandle='example';skillButton.dataset.skillScope='project';setSkillEnabled(skillButton,false);'started';`)
					browser.waitFor("enable denial toast", `String(permissionToasts.length)`, "1")
					browser.evaluate(`setSkillAlwaysUse(skillButton,true);'started';`)
					browser.waitFor("always-use denial toast", `String(permissionToasts.length)`, "2")
					if result := browser.evaluate(`permissionAlerts.length===0 && document.getElementById('skills-container').innerHTML===skillBefore && permissionToasts.every(t=>t.message==='Editor access required') ? 'ok' : 'failed'`); result != "ok" {
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
