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
 const providers=[...panel.querySelectorAll('.ov-mp-provider')];
 const originalLeft=panel.getBoundingClientRect().left;
 const originalTriggerRect=trigger.getBoundingClientRect.bind(trigger);
 trigger.getBoundingClientRect=()=>{const r=originalTriggerRect();return {...r,left:r.left-2};};
 providers[0].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
 assert(!sub.hidden && sub.getAttribute('aria-label')===providers[0].dataset.provider+' models','hover opens provider submenu');
 assert(panel.getBoundingClientRect().left===originalLeft,'provider submenu does not move the original picker');
 trigger.getBoundingClientRect=originalTriggerRect;
 providers[0].click();
 assert(!sub.hidden,'click keeps hovered provider open');
 providers[1].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
 assert(sub.getAttribute('aria-label')===providers[1].dataset.provider+' models','hover switches provider submenu');
 providers[0].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
 assert(!sub.hidden && sub.querySelectorAll('.ov-mp-pick').length===12,'provider submenu');
 assert(sub.querySelector('.ov-mp-subhead').hidden,'no redundant submenu heading');
 const row=sub.querySelector('.ov-mp-row');
 assert(row.firstElementChild.classList.contains('ov-mp-pick')&&row.firstElementChild.firstElementChild.classList.contains('ov-mp-check')&&row.lastElementChild.classList.contains('ov-mp-star'),'checkmark on left, favorite on right');
 row.dispatchEvent(new PointerEvent('pointermove'));
 assert(row.hasAttribute('data-picker-active'),'hover activates model row');
 const next=row.nextElementSibling;next.dispatchEvent(new PointerEvent('pointermove'));
 assert(!row.hasAttribute('data-picker-active')&&next.hasAttribute('data-picker-active'),'hover moves highlight');
 sub.querySelector('.ov-mp-star').click();
 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===1,'favorite moved to main panel');
 assert(sub.querySelectorAll('.ov-mp-pick').length===11,'favorite not duplicated');
 const hover=(target)=>target.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,pointerType:'mouse'}));
 const reopen=()=>panel.querySelector('.ov-mp-provider').dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
 hover(panel.querySelector('.ov-mp-list'));assert(!sub.hidden,'crossing list padding keeps submenu open');
 sub.dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
 await new Promise(r=>setTimeout(r,400));assert(!sub.hidden,'entering submenu cancels delayed close');
 hover(sub.querySelector('.ov-mp-row'));assert(!sub.hidden,'moving into submenu keeps it open');
 panel.querySelector('.ov-mp-provider').dispatchEvent(new PointerEvent('pointermove',{bubbles:true,pointerType:'mouse'}));
 assert(panel.querySelector('[data-picker-active]'),'provider hover highlights row');
 hover(panel.querySelector('.ov-mp-search input'));assert(sub.hidden,'search hover closes submenu');
 assert(!panel.querySelector('[data-picker-active]'),'search hover clears row highlight');reopen();
 hover(panel.querySelector('.ov-mp-star[aria-pressed=true]'));assert(sub.hidden,'favorite hover closes submenu');reopen();
 hover(panel.querySelector('.ov-mp-pick[data-model="auto"]'));assert(sub.hidden,'Auto hover closes submenu');reopen();
 hover(panel.querySelector('.ov-mp-pick[data-model="default"]'));assert(sub.hidden,'Default hover closes submenu');reopen();
 panel.querySelector('.ov-mp-provider').dispatchEvent(new PointerEvent('pointermove',{bubbles:true,pointerType:'mouse'}));
 hover(panel.querySelector('.ov-mp-effort'));assert(sub.hidden,'effort hover closes submenu');
 assert(!panel.querySelector('[data-picker-active]'),'effort hover clears row highlight');reopen();
 hover(panel.querySelector('.ov-mp-heading'));assert(!sub.hidden,'heading does not close submenu during travel');
 await new Promise(r=>setTimeout(r,400));assert(sub.hidden,'submenu closes when pointer stays on heading');reopen();
 hover(panel.querySelector('.ov-mp-list'));await new Promise(r=>setTimeout(r,400));
 assert(sub.hidden,'submenu closes when pointer stays on list padding');reopen();
 hover(panel.querySelector('.ov-mp-list'));reopen();
 await new Promise(r=>setTimeout(r,400));assert(!sub.hidden,'returning to provider cancels delayed close');
 hover(panel.querySelector('.ov-mp-provider'));assert(!sub.hidden,'provider hover keeps submenu open');
 const search=panel.querySelector('input');search.value='Configuration 23';search.dispatchEvent(new Event('input'));
 assert(sub.hidden && panel.querySelectorAll('.ov-mp-pick').length===1,'search all providers');
 panel.querySelector('.ov-mp-pick').click();
 assert(!panel.hidden && trigger.dataset.currentValue==='m23','select keeps picker open');
 const slider=panel.querySelector('input[type=range]');
 document.documentElement.dataset.theme='light';
 assert(getComputedStyle(slider).getPropertyValue('--ov-mp-track').trim()==='#d5d8df','light track is light gray');
 const accent=getComputedStyle(slider).getPropertyValue('--ov-mp-accent');
 slider.value='1';slider.dispatchEvent(new Event('input'));
 assert(panel.querySelector('.ov-mp-effort-head').textContent==='Reasoning effortUse default','no repeated level and reset keeps its action label');
 assert(getComputedStyle(slider).getPropertyValue('--ov-mp-accent')===accent,'adjustment keeps same accent');
 document.documentElement.dataset.theme='dark';
 assert(getComputedStyle(slider).getPropertyValue('--ov-mp-track').trim()==='#4b5260','dark track is gray');
 slider.value='2';slider.dispatchEvent(new Event('input'));slider.dispatchEvent(new Event('change'));
 assert(document.querySelector('[name=reasoning_effort]').value==='high','effort submitted');
 panel.querySelector('.ov-mp-reset').click();
 assert(document.querySelector('[name=reasoning_effort]').value==='','restore model default');
 assert(panel.getBoundingClientRect().left>=0 && panel.getBoundingClientRect().right<=innerWidth,'panel fits');
 search.value='';search.dispatchEvent(new Event('input'));await wait();
 const list=panel.querySelector('.ov-mp-list');
 assert(!panel.querySelector('.ov-mp-scroll-hints'),'no more-above/below controls');
 assert(list.offsetWidth===list.clientWidth,'no scrollbar when models fit');
 list.style.maxHeight='75px';await wait();
 assert(list.scrollHeight>list.clientHeight&&list.offsetWidth>list.clientWidth,'visible scrollbar when overflowing');
 list.scrollTop=50;await wait();assert(list.scrollTop>0,'list remains scrollable');
 assert(!panel.querySelector('.ov-mp-effort').hidden,'effort stays accessible while list scrolls');
 list.style.maxHeight='';await wait();
 const defaultRow=panel.querySelector('.ov-mp-pick[data-model=default]');
 assert(defaultRow.textContent.includes('Default — Configuration 00'),'global default identifies its model');
 defaultRow.click();
 assert(trigger.dataset.currentValue==='default'&&!panel.querySelector('.ov-mp-effort').hidden,'global default exposes effort without changing selection');
 const defaultSlider=panel.querySelector('input[type=range]');defaultSlider.value='2';defaultSlider.dispatchEvent(new Event('input'));
 assert(document.querySelector('[name=agent_id]').value==='default'&&document.querySelector('[name=reasoning_effort]').value==='high','global default sends selected effort');
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
		{ID: "a", Name: "First", Provider: models.ProviderOpenAI, Model: "gpt-5.5", ReasoningEffort: "medium", IsDefault: true},
		{ID: "b", Name: "Second", Provider: models.ProviderOpenAI, Model: "gpt-5.5", ReasoningEffort: "medium"},
		{ID: "c", Name: "Local", Provider: models.ProviderOllama, Model: "qwen"},
		{ID: "mix", Name: "Ensemble", Provider: models.ProviderMixture},
	}
	var content bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{FormID: "task-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/tasks/t/thread", TaskID: "t", Agents: agents, DefaultAgentID: "b", SelectedAgentID: "a", ShowModelSelector: true}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><style>body{font-family:sans-serif}form{margin-top:550px}</style></head><body data-test-result="pending"><script>
 localStorage.clear();window.htmx={process:function(){}};
 const stored={a:'high',b:'low'},posts=[];let failNextWrite=false,failNextRead=false;
 window.fetch=async function(url,options){
 if(options?.method==='POST'){if(failNextWrite){failNextWrite=false;return {ok:false};}const body=options.body;await new Promise(r=>setTimeout(r,20));stored[body.get('agent_id')==='default'?'b':body.get('agent_id')]=body.get('reasoning_effort');posts.push(body.get('agent_id'));return {ok:true};}
 if(failNextRead){failNextRead=false;return {ok:false};}const selected=new URL(url,location.href).searchParams.get('agent_id'),id=selected==='default'?'b':selected;await new Promise(r=>setTimeout(r,id==='a'?80:20));return {ok:true,json:async()=>({reasoning_effort:stored[id]||''})};
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
 assert(sub.querySelector('[data-model=c]').closest('.ov-mp-row').querySelector('.ov-mp-check').textContent==='✓','selected checkmark without effort');
 window.ovModelPicker.close(false);trigger.click();
 assert(!sub.hidden && sub.querySelector('[data-model=c]').getAttribute('aria-pressed')==='true','reopen selected provider');
 assert(panel.getBoundingClientRect().right<=innerWidth,'small viewport bounds');
 failNextRead=true;choose('b');await trigger._ovModelState.ready;
 assert(!!trigger._ovModelState.error,'lookup failure shown');
 window.ovModelPicker.close(false);trigger.click();await trigger._ovModelState.ready;
 assert(!trigger._ovModelState.error&&posts.at(-1)==='b','lookup retry persists pending selection');
 failNextWrite=true;choose('b');await trigger._ovModelState.ready;
 assert(!!trigger._ovModelState.error,'failed save reported');
 window.ovModelPicker.close(false);trigger.click();await trigger._ovModelState.ready;
 assert(!trigger._ovModelState.error,'reopen recovers after failed save');
 assert(posts.at(-1)==='b','reopen retries the failed model selection');
 assert(!panel.querySelector('input[type=range]').disabled,'effort controls recover after retry');
 choose('a');await trigger._ovModelState.ready;assert(posts.at(-1)==='a','can save after failure');
 const form=trigger.closest('form'),message=form.querySelector('[name=message]'),attachment=form.querySelector('[name=attachment_session_id]');
 let sent=null;
 form.addEventListener('submit',event=>{event.preventDefault();sent=new FormData(form);},{once:true});
 message.value='original steering message';attachment.value='original-attachment';
 form._chatNextSubmissionPayload={message:message.value,attachmentSessionID:attachment.value,fallbackFromSteer:true};
 form.requestSubmit();
 message.value='newer draft';attachment.value='newer-attachment';
 await wait(40);
 assert(sent?.get('message')==='original steering message','retry sends original message');
 assert(sent?.get('attachment_session_id')==='original-attachment','retry sends original attachments');
 assert(message.value==='newer draft'&&attachment.value==='newer-attachment','retry preserves newer draft');
 form._chatNextSubmissionPayload=null; // The real HTMX beforeRequest handler consumes this.
 const modelInput=form.querySelector('[name=agent_id]');
 const effortSlider=panel.querySelector('input[type=range]');
 effortSlider.value='1';effortSlider.dispatchEvent(new Event('input'));effortSlider.dispatchEvent(new Event('change'));
 assert(modelInput.value==='a'&&effort.value==='medium','pending save uses first model and effort');
 let switchedSend=null;
 form.addEventListener('submit',event=>{event.preventDefault();switchedSend=new FormData(form);},{once:true});
 message.value='send before switching';
 form.requestSubmit();
 choose('b');
 await trigger._ovModelState.ready;await wait(50);
 assert(switchedSend?.get('message')==='send before switching','delayed send keeps its message');
 assert(switchedSend?.get('agent_id')==='a'&&switchedSend?.get('reasoning_effort')==='medium','delayed send keeps the model and effort selected at Send');
 assert(switchedSend?.get('model_selection_managed')==='1'&&!form.querySelector('[name=model_selection_managed]'),'picker send marks selection ownership without changing the composer');
 assert(modelInput.value==='b'&&effort.value==='high','later model selection remains in composer');
 form._chatNextSubmissionPayload=null;
 choose('a');assert(trigger._ovModelState.loading,'first model effort is loading');
 let loadingSend=null;
 form.addEventListener('submit',event=>{event.preventDefault();loadingSend=new FormData(form);},{once:true});
 message.value='send while effort loads';
 form.requestSubmit();
 choose('b');
 await wait(150);
 assert(loadingSend?.get('agent_id')==='a'&&loadingSend?.get('reasoning_effort')==='medium','send uses loaded effort even when model changes during lookup');
 assert(modelInput.value==='b'&&effort.value==='high','lookup does not undo later model selection');
 const defaultRow=panel.querySelector('.ov-mp-pick[data-model=default]');
 assert(defaultRow.textContent.includes('Default — Second'),'project default identifies its model');
 defaultRow.click();await trigger._ovModelState.ready;
 assert(modelInput.value==='default'&&effort.value==='high'&&!panel.querySelector('.ov-mp-effort').hidden,'default keeps its selection and loads project model effort');
 const defaultSlider=panel.querySelector('input[type=range]');defaultSlider.value='1';defaultSlider.dispatchEvent(new Event('input'));defaultSlider.dispatchEvent(new Event('change'));
 await trigger._ovModelState.pending;
 assert(stored.b==='medium'&&posts.at(-1)==='default','default effort saves against the project model');

 document.body.dataset.testResult='pass';
 }catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e)}})();
 </script></body></html>`)
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "task picker persistence", 400, 800, 25*time.Second)
}
