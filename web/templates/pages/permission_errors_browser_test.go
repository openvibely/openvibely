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

// UI tests use the explicit permission-denial contract. Handler integration tests
// independently verify which real authorization failures emit that contract.
func TestBrowserFunctional_PermissionErrorsPreserveForms(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	cases := []struct {
		name      string
		component templ.Component
		script    string
	}{
		{"organization", OrganizationContent(templ.Raw(`<div id="organization-container"><form><input value="Keep draft"></form><div id="organization-error" class="hidden"></div></div>`)), `(()=>{
 const form=document.querySelector('#organization-container form'),before=form.innerHTML;
 const message='organization administrator required';
 const xhr={status:403,responseText:JSON.stringify({message}),getResponseHeader:name=>name==='X-OpenVibely-Permission-Denied'?'true':null};
 form.dispatchEvent(new CustomEvent('htmx:responseError',{bubbles:true,detail:{elt:form,target:form,xhr}}));
 if(permissionToasts.length!==1||permissionToasts[0].message!==message||form.innerHTML!==before||!document.getElementById('organization-error').classList.contains('hidden'))return 'organization denial changed form';
 return 'ok';})()`},
		{"webhooks", templ.Raw(renderSettingsContentForWebhookTest(t, nil)), `(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Maintainer access required'}),{status:403,headers:{'X-OpenVibely-Permission-Denied':'true'}});
 const form=document.getElementById('webhook_form'),modal=document.getElementById('webhook_modal');document.getElementById('webhook_name').value='Keep webhook';
 openWebhookEditModal('restricted');for(let i=0;i<100&&permissionToasts.length===0;i++)await new Promise(r=>setTimeout(r,10));
 return permissionToasts.length===1&&permissionToasts[0].message==='Maintainer access required'&&!modal.open?'ok':'webhook UI changed';})()`},
		{"reviews", components.DiffViewer("diff --git a/file.go b/file.go\n--- a/file.go\n+++ b/file.go\n@@ -1 +1 @@\n-old\n+new\n", "task", nil, "inline"), `(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Editor access required'}),{status:403,headers:{'X-OpenVibely-Permission-Denied':'true'}});
 const row=document.createElement('tr');row.className='review-comment-form';row.dataset.commentId='comment';row.innerHTML='<td><textarea>Keep draft</textarea></td>';document.body.append(row);const before=row.outerHTML;
 updateReviewComment(row);for(let i=0;i<100&&permissionToasts.length<1;i++)await new Promise(r=>setTimeout(r,10));
 deleteReviewComment({stopPropagation(){},target:row});for(let i=0;i<100&&permissionToasts.length<2;i++)await new Promise(r=>setTimeout(r,10));
 submitReviewComment('task','file.go','1','new','Keep draft',row);for(let i=0;i<100&&permissionToasts.length<3;i++)await new Promise(r=>setTimeout(r,10));
 await new Promise(r=>setTimeout(r,20));
 return permissionToasts.length===3&&permissionToasts.every(t=>t.message==='Editor access required')&&row.isConnected&&row.outerHTML===before?'ok':'review changed';})()`},
		{"yaml", AutomationBuilderContent(models.AutomationBuilderPage{Source: "blank", YAML: "name: Keep draft", Result: models.AutomationDraftResult{Candidate: models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Test", AutomationType: "custom", AdapterKey: "custom"}}}, "project"), `(async()=>{
 window.fetch=async()=>new Response(JSON.stringify({message:'Editor access required'}),{status:403,headers:{'X-OpenVibely-Permission-Denied':'true'}});
 const editor=document.querySelector('[data-automation-yaml-editor]'),diagnostic=document.querySelector('[data-automation-yaml-diagnostic]');
 editor.value='name: Keep draft';const before=diagnostic.outerHTML;
 editor.dispatchEvent(new Event('input',{bubbles:true}));
 for(let i=0;i<100&&permissionToasts.length<1;i++)await new Promise(r=>setTimeout(r,10));
 await new Promise(r=>setTimeout(r,20));
 return permissionToasts.length===1&&permissionToasts[0].message==='Editor access required'&&diagnostic.outerHTML===before&&editor.value==='name: Keep draft'?'ok':'YAML changed';})()`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var page bytes.Buffer
			if err := tc.component.Render(context.Background(), &page); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				fmt.Fprintf(w, `<!doctype html><html><body><script>window.permissionToasts=[];window.showToast=(message,status)=>permissionToasts.push({message,status});window.htmx={process(){}};</script><script src="%s"></script>%s</body></html>`, static.URL("permission-errors.js"), page.String())
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "permission-"+tc.name, func(browser *composerFocusCDP) {
				browser.waitFor("permission helper", `typeof window.openVibelyPermissionErrors`, "object")
				if result := browser.evaluateAwait(tc.script); result != "ok" {
					t.Fatal(result)
				}
				if result := browser.evaluateAwait(`(async()=>{
 permissionToasts.length=0;
 for(const status of [400,403,404,500]) {
 const response=new Response(JSON.stringify({message:'Not a role denial'}),{status});
 if(await openVibelyPermissionErrors.fetch(response))return 'non-permission response intercepted';
 }
 if(openVibelyPermissionErrors.htmx({status:403,getResponseHeader:()=>null}))return 'unmarked HTMX error intercepted';
 return permissionToasts.length===0?'ok':'unexpected toast';})()`); result != "ok" {
					t.Fatal(result)
				}
			})
		})
	}
}
