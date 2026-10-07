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

func TestBrowserFunctional_ConversationModelPicker(t *testing.T) {
	chrome := testChromePath(t)
	var agents []models.LLMConfig
	for i := 0; i < 24; i++ {
		provider := models.ProviderOpenAI
		model := "gpt-5.5"
		if i >= 12 {
			provider = models.ProviderAnthropic
			model = "claude-opus-5-5"
		}
		agents = append(agents, models.LLMConfig{ID: fmt.Sprint("m", i), Name: fmt.Sprintf("Configuration %02d", i), Provider: provider, Model: model, ReasoningEffort: "medium"})
	}
	var content bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{FormID: "chat-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/chat/send", ProjectID: "test", Agents: agents, SelectedAgentID: "m0", ShowModelSelector: true}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><style>body{background:#1d232a;color:#eee;font-family:sans-serif;margin:20px}form{margin-top:650px}button{font:inherit}</style></head><body data-test-result="pending"><script>localStorage.clear();window.htmx={process:function(){}};</script>`+content.String()+`<div id="browser-result"></div><script>
 (async function(){
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)},wait=()=>new Promise(r=>setTimeout(r,30));
 try{
 const trigger=document.getElementById('chat-form-agent-select');
 trigger.click();await wait();
 const panel=document.getElementById('conversation-model-picker'),sub=document.getElementById('conversation-provider-models');
 assert(!panel.hidden,'picker opens');
 assert(panel.querySelectorAll('.ov-mp-provider').length===2,'provider groups');
 panel.querySelector('.ov-mp-provider').click();
 assert(!sub.hidden && sub.querySelectorAll('.ov-mp-pick').length===12,'provider submenu');
 sub.querySelector('.ov-mp-star').click();
 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===1,'favorite moved to main panel');
 assert(sub.querySelectorAll('.ov-mp-pick').length===11,'favorite not duplicated');
 const search=panel.querySelector('input');search.value='Configuration 23';search.dispatchEvent(new Event('input'));
 assert(sub.hidden && panel.querySelectorAll('.ov-mp-pick').length===1,'search all providers');
 panel.querySelector('.ov-mp-pick').click();
 assert(!panel.hidden && trigger.dataset.currentValue==='m23','select keeps picker open');
 const slider=panel.querySelector('input[type=range]');slider.value='2';slider.dispatchEvent(new Event('input'));slider.dispatchEvent(new Event('change'));
 assert(document.querySelector('[name=reasoning_effort]').value==='high','effort submitted');
 panel.querySelector('.ov-mp-reset').click();
 assert(document.querySelector('[name=reasoning_effort]').value==='','restore model default');
 assert(panel.getBoundingClientRect().left>=0 && panel.getBoundingClientRect().right<=innerWidth,'panel fits');
 document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
 assert(panel.hidden,'escape closes');
 document.body.setAttribute('data-test-result','pass');
 }catch(e){document.body.setAttribute('data-test-result','fail');document.body.setAttribute('data-test-error',e.stack);document.getElementById('browser-result').textContent=String(e);}
 })();</script></body></html>`)
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "conversation picker", 1200, 900, 25*time.Second)
}

func TestBrowserFunctional_TaskModelPickerPersistence(t *testing.T) {
	chrome := testChromePath(t)
	agents := []models.LLMConfig{
		{ID: "a", Name: "First", Provider: models.ProviderOpenAI, Model: "gpt-5.5", ReasoningEffort: "medium"},
		{ID: "b", Name: "Second", Provider: models.ProviderOpenAI, Model: "gpt-5.5", ReasoningEffort: "medium"},
		{ID: "c", Name: "Local", Provider: models.ProviderOllama, Model: "qwen"},
		{ID: "mix", Name: "Ensemble", Provider: models.ProviderMixture},
	}
	var content bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{FormID: "task-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/tasks/t/thread", TaskID: "t", Agents: agents, SelectedAgentID: "a", ShowModelSelector: true}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><style>body{font-family:sans-serif}form{margin-top:550px}</style></head><body data-test-result="pending"><script>
 localStorage.clear();window.htmx={process:function(){}};
 const stored={a:'high',b:'low'},posts=[];
 window.fetch=async function(url,options){
 if(options?.method==='POST'){const body=options.body;await new Promise(r=>setTimeout(r,20));stored[body.get('agent_id')]=body.get('reasoning_effort');posts.push(body.get('agent_id'));return {ok:true};}
 const id=new URL(url,location.href).searchParams.get('agent_id');await new Promise(r=>setTimeout(r,id==='a'?80:20));return {ok:true,json:async()=>({reasoning_effort:stored[id]||''})};
 };
 </script>`+content.String()+`<div id="browser-result"></div><script>
 (async()=>{const assert=(v,m)=>{if(!v)throw Error(m)},wait=ms=>new Promise(r=>setTimeout(r,ms));try{
 const trigger=document.getElementById('task-form-agent-select'),panel=document.getElementById('conversation-model-picker'),effort=document.querySelector('[name=reasoning_effort]');
 trigger.click();assert([...panel.querySelectorAll('.ov-mp-provider')].some(b=>b.dataset.provider==='Mixture'),'mixture configurations are grouped');assert(panel.querySelector('input[type=range]').disabled,'disable until saved effort loads');
 await trigger._ovModelState.ready;assert(effort.value==='high','load saved task effort');
 function choose(id){const search=panel.querySelector('input');search.value=id==='a'?'First':id==='b'?'Second':'Local';search.dispatchEvent(new Event('input'));panel.querySelector('.ov-mp-pick').click();}
 choose('b');await trigger._ovModelState.ready;assert(effort.value==='low'&&stored.b==='low','restore other model effort');
 choose('a');choose('b');await trigger._ovModelState.ready;await wait(100);
 assert(trigger.dataset.currentValue==='b'&&effort.value==='low'&&posts.at(-1)==='b','ignore stale model lookup');
 const slider=panel.querySelector('input[type=range]');slider.value='2';slider.dispatchEvent(new Event('input'));slider.dispatchEvent(new Event('change'));await trigger._ovModelState.pending;
 assert(stored.b==='high'&&stored.a==='high','persist override for selected configuration');
 choose('c');await trigger._ovModelState.ready;assert(panel.querySelector('.ov-mp-effort').hidden&&effort.value==='','unsupported model has no slider or stale effort');
 // Selecting from a provider keeps the selected row visible even without effort controls.
 const search=panel.querySelector('input');search.value='';search.dispatchEvent(new Event('input'));
 [...panel.querySelectorAll('.ov-mp-provider')].find(b=>b.dataset.provider==='Ollama').click();
 const sub=document.getElementById('conversation-provider-models');
 sub.querySelector('[data-model=c]').click();await trigger._ovModelState.ready;
 assert(!sub.hidden && sub.querySelector('[data-model=c]').getAttribute('aria-pressed')==='true','selected model remains visible without effort');
 assert(sub.querySelector('[data-model=c] .ov-mp-check').textContent==='✓','selected checkmark without effort');
 window.ovModelPicker.close(false);trigger.click();
 assert(!sub.hidden && sub.querySelector('[data-model=c]').getAttribute('aria-pressed')==='true','reopen selected provider');
 assert(panel.getBoundingClientRect().right<=innerWidth,'small viewport bounds');
 document.body.dataset.testResult='pass';
 }catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e)}})();
 </script></body></html>`)
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "task picker persistence", 400, 800, 25*time.Second)
}
