package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBrowserFunctional_DestinationAttachments(t *testing.T) {
	for _, scope := range []string{"task", "chat"} {
		t.Run(scope, func(t *testing.T) {
			chrome := testChromePath(t)
			var content bytes.Buffer
			config := ChatInputFormConfig{FormID: "attachment-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/chat/send", ShowModelSelector: true, Agents: []models.LLMConfig{{ID: "model", Name: "Model", Provider: models.ProviderOpenAI}}, ProjectChoices: []models.Project{{ID: "local", Name: "Local"}, {ID: "remote", Name: "Remote"}}, ProjectInputID: "destination"}
			if scope == "chat" {
				config.ProjectSendURL = "/chat/send?project_id="
			}
			if err := ChatInputForm(config).Render(context.Background(), &content); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/tasks/new" {
					fmt.Fprint(w, `<li data-value="default">Default</li>`)
					return
				}
				if r.URL.Path == "/conversation/model-settings" {
					fmt.Fprint(w, `{"agent_id":"default"}`)
					return
				}
				if r.URL.Path == "/ui/model-favorites" {
					fmt.Fprint(w, `{}`)
					return
				}
				destination := "local"
				if r.URL.Query().Get("restored") == "1" {
					destination = "remote"
				}
				fmt.Fprint(w, `<!doctype html><html><body data-test-result="pending"><input id="destination" value="`+destination+`"><div id="messages"></div><script>window.htmx={process:function(){}};</script>`+content.String()+`<script>
(async()=>{try {
 const wait=()=>new Promise(r=>setTimeout(r,20)), assert=(v,m)=>{if(!v)throw Error(m)};
 const form=document.getElementById('attachment-form'), dest=document.getElementById('destination'), session=form.querySelector('[name="attachment_session_id"]'), send=form.querySelector('button[type="submit"]');
 const original=window.fetch;let release,hold=false,fail=false,requests=[],downloads=0;
 window.fetch=async(url,options)=>{
  if(String(url).startsWith('/chat/attachments?')) {
   const project=new URL(url,location.href).searchParams.get('project_id');
   const files=options.body.getAll('files');requests.push(project);
   assert(!options.body.has('attachment_session_id'),'fresh session on destination transfer');
   assert(await files[0].text()==='screenshot bytes','attachment bytes preserved');
   if(hold){hold=false;await new Promise(r=>release=r)}
   if(fail)return new Response('offline',{status:503});
   return new Response(JSON.stringify({session_id:project+'-session',attachments:files.map(f=>({filename:f.name,size:f.size,media_type:f.type,session_id:project+'-session'}))}));
  }
  if(String(url).startsWith('/chat/attachments/pending/')){downloads++;return new Response('screenshot bytes')};
  return original(url,options);
 };
 const until=async(check)=>{for(let i=0;i<200&&!check();i++)await wait();assert(check(),'timed out')};
 const change=value=>{dest.value=value;dest.dispatchEvent(new Event('change',{bubbles:true}))};
 const blocked=()=>{const e=new Event('submit',{bubbles:true,cancelable:true});form.dispatchEvent(e);assert(e.defaultPrevented && send.disabled,'submission blocked')};
 if(location.search.includes('restored=1')) {
  assert(session.value==='remote-session','source session restored');
  change('local');blocked();await until(()=>session.value==='local-session'&&!send.disabled);
  assert(downloads===1,'restored attachment fetched from original session');
  assert(form.querySelectorAll('[data-pending-attachment]').length===1,'restored preview kept');
  document.body.dataset.testResult='pass';return;
 }
 const input=form.querySelector('input[type="file"]'),dt=new DataTransfer();dt.items.add(new File(['screenshot bytes'],'shot.png',{type:'image/png'}));input.files=dt.files;input.dispatchEvent(new Event('change'));
 await until(()=>session.value==='local-session');
 hold=true;change('remote');await until(()=>!!release);blocked();
 assert(form.querySelector('[data-pending-attachment-filename="shot.png"]'),'preview retained during transfer');
 assert(session.value==='','old session cannot be sent');
 change('local');await until(()=>session.value==='local-session');release();await wait();
 assert(session.value==='local-session','stale remote upload ignored');
 fail=true;change('remote');const retry=[...form.querySelectorAll('button')].find(b=>b.textContent==='Attachments unavailable — Retry');await until(()=>!retry.hidden);blocked();
 assert(form.querySelector('[data-pending-attachment]'),'failed transfer retains attachment');
 fail=false;retry.click();await until(()=>session.value==='remote-session'&&!send.disabled);assert(retry.hidden,'retry hidden after success');
 assert(form.querySelectorAll('[data-pending-attachment]').length===1,'no duplicate previews');
 // Exercise restored metadata (no original browser File) in a new composer instance.
 const state=window.saveChatPendingAttachmentState(form);
 assert(state.attachments[0].session_id==='remote-session','restorable source recorded');
 location.replace('/?restored=1');
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack}})();
</script></body></html>`)
			}))
			defer srv.Close()
			runHeadlessChromeCDPFixture(t, chrome, srv.URL, "destination attachment "+scope, 1200, 900, 20*time.Second)
		})
	}
}
