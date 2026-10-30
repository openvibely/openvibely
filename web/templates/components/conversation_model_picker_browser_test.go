package components

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
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
	var favoriteMu sync.Mutex
	favorites := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ui/model-favorites" {
			favoriteMu.Lock()
			defer favoriteMu.Unlock()
			if r.Method == http.MethodPost {
				var body struct {
					ModelID   string   `json:"model_id"`
					Favorite  bool     `json:"favorite"`
					ImportIDs []string `json:"import_ids"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if body.ModelID != "" {
					if body.Favorite {
						favorites[body.ModelID] = true
					} else {
						delete(favorites, body.ModelID)
					}
				}
				for _, id := range body.ImportIDs {
					favorites[id] = true
				}
			}
			_ = json.NewEncoder(w).Encode(favorites)
			return
		}
		fmt.Fprint(w, `<!doctype html><html><head><style>body{background:#1d232a;color:#eee;font-family:sans-serif;margin:20px}form{margin-top:650px}button{font:inherit}</style></head><body data-test-result="pending"><script>localStorage.clear();window.htmx={process:function(){}};</script>`+content.String()+`<div id="browser-result"></div><script>
 (async function(){
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)},wait=()=>new Promise(r=>setTimeout(r,30)),nextFrame=()=>new Promise(r=>requestAnimationFrame(r));
 try{
 let favoriteReads=0;const baseFetch=window.fetch.bind(window);
 window.fetch=async(url,options)=>{
   if(url!=='/ui/model-favorites'||options)return baseFetch(url,options);
   favoriteReads++;
   try{const r=await baseFetch(url,options),data=await r.json();return {ok:r.ok,json:async()=>data};}
   finally{setTimeout(()=>favoriteReads--);}
 };
 const favoritesLoaded=async()=>{while(favoriteReads)await wait();};
 const trigger=document.getElementById('chat-form-agent-select');
 trigger.scrollIntoView({block:'end'});await nextFrame();
 const textarea=document.querySelector('textarea');textarea.focus();
 const apple=/Mac|iPhone|iPad|iPod|iOS/i.test(navigator.userAgentData?.platform || navigator.platform || navigator.userAgent || '');
 assert(trigger.title===(apple?'Models (⌘M)':'Models (Ctrl+M)'),'initial model hint before opening menu');
 const shortcut=(extra={})=>{const e=new KeyboardEvent('keydown',{key:'M',metaKey:apple,ctrlKey:!apple,shiftKey:false,bubbles:true,cancelable:true,...extra});document.activeElement.dispatchEvent(e);return e;};
 const picker=document.getElementById('conversation-model-picker');
 for(const extra of [{key:'L'},{shiftKey:true},{ctrlKey:true,metaKey:true},{ctrlKey:apple,metaKey:!apple},{altKey:true},{isComposing:true},{repeat:true}]){
   assert(!shortcut(extra).defaultPrevented && picker.hidden,'ignore other shortcuts and composing/repeated events');
 }
 trigger.disabled=true;assert(!shortcut().defaultPrevented && picker.hidden,'ignore disabled picker');trigger.disabled=false;
 assert(shortcut().defaultPrevented,'consume model shortcut');await wait();
 assert(!picker.hidden && document.activeElement===picker.querySelector('input'),'shortcut opens picker and focuses search from composer');
 document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 assert(picker.hidden && document.activeElement===trigger,'Escape closes picker and restores trigger focus');
 for (const platform of ['Win32','Linux x86_64','MacIntel']) {
   const originalPlatform=Object.getOwnPropertyDescriptor(navigator,'platform');
   const originalData=Object.getOwnPropertyDescriptor(navigator,'userAgentData');
   Object.defineProperty(navigator,'userAgentData',{configurable:true,value:undefined});
   Object.defineProperty(navigator,'platform',{configurable:true,value:platform});
   const mac=platform==='MacIntel';
   textarea.focus();
   assert(shortcut({metaKey:mac,ctrlKey:!mac}).defaultPrevented,'consume shortcut on '+platform);
   assert(!picker.hidden && document.activeElement===picker.querySelector('input'),'focus model search on '+platform);
   assert(trigger.getAttribute('aria-keyshortcuts')===(mac?'Meta+M':'Control+M'),'platform shortcut accessibility hint');
   assert(trigger.title===(mac?'Models (⌘M)':'Models (Ctrl+M)'),'platform tooltip');
   assert(shortcut({metaKey:mac,ctrlKey:!mac}).defaultPrevented,'consume repeated shortcut on '+platform);
   assert(picker.hidden && document.getElementById('conversation-provider-models').hidden && document.activeElement===trigger && trigger.getAttribute('aria-expanded')==='false','shortcut closes picker and restores trigger focus on '+platform);
   assert(shortcut({metaKey:mac,ctrlKey:!mac}).defaultPrevented && !picker.hidden && document.activeElement===picker.querySelector('input'),'shortcut reopens picker on '+platform);
   window.ovModelPicker.close(false);
   if(originalPlatform)Object.defineProperty(navigator,'platform',originalPlatform);else delete navigator.platform;
   if(originalData)Object.defineProperty(navigator,'userAgentData',originalData);else delete navigator.userAgentData;
 }
 trigger.click();await wait();await favoritesLoaded();
 const panel=document.getElementById('conversation-model-picker'),sub=document.getElementById('conversation-provider-models');
 assert(!panel.hidden,'picker opens');
 assert(sub.hidden,'picker opens without a provider submenu');
 assert(panel.querySelectorAll('.ov-mp-provider').length===2,'provider groups');
 const providers=[...panel.querySelectorAll('.ov-mp-provider')];
 const originalLeft=panel.getBoundingClientRect().left;
 const originalTriggerRect=trigger.getBoundingClientRect.bind(trigger);
 trigger.getBoundingClientRect=()=>{const r=originalTriggerRect();return {...r,left:r.left-2};};
 providers[0].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
 assert(!sub.hidden && sub.getAttribute('aria-label')===providers[0].dataset.provider+' models','hover opens provider submenu');
 const firstRect=providers[0].getBoundingClientRect(),secondRect=providers[1].getBoundingClientRect();
 providers[0].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse',clientX:firstRect.left+35,clientY:firstRect.top+12}));
 providers[1].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse',clientX:secondRect.right-12,clientY:secondRect.top+12}));
 assert(sub.getAttribute('aria-label')===providers[0].dataset.provider+' models','diagonal travel keeps current submenu open');
 sub.dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));await new Promise(r=>setTimeout(r,350));
 assert(sub.getAttribute('aria-label')===providers[0].dataset.provider+' models','entering submenu cancels provider switch');
 providers[1].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse',clientX:secondRect.right-12,clientY:secondRect.top+12}));
 await new Promise(r=>setTimeout(r,350));
 assert(sub.getAttribute('aria-label')===providers[1].dataset.provider+' models','staying on another provider switches submenu');
 providers[0].click();
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
 const hoverObserver=new MutationObserver(()=>{});hoverObserver.observe(sub,{attributes:true,subtree:true,attributeFilter:['data-picker-active']});
 for(let i=0;i<10;i++)row.dispatchEvent(new PointerEvent('pointermove'));
 assert(hoverObserver.takeRecords().length===0,'moving within model row does not rewrite highlight');hoverObserver.disconnect();

 const next=row.nextElementSibling;next.dispatchEvent(new PointerEvent('pointermove'));
 assert(!row.hasAttribute('data-picker-active')&&next.hasAttribute('data-picker-active'),'hover moves highlight');
 sub.querySelector('.ov-mp-star').click();
 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===1,'favorite moved to main panel');
 assert(sub.querySelectorAll('.ov-mp-pick').length===11,'favorite not duplicated');
 const menuRows=[...panel.querySelector('.ov-mp-list').children];
 const lastProvider=menuRows.findLastIndex(e=>e.classList.contains('ov-mp-provider'));
 const autoIndex=menuRows.findIndex(e=>e.querySelector('[data-model="auto"]'));
 const defaultIndex=menuRows.findIndex(e=>e.querySelector('[data-model="default"]'));
 const favoritesIndex=menuRows.findIndex(e=>e.classList.contains('ov-mp-heading'));
 assert(lastProvider<autoIndex&&autoIndex<defaultIndex&&defaultIndex<favoritesIndex&&menuRows.at(-1).querySelector('.ov-mp-star'),'providers then Auto/Default then favorites nearest effort');
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
 assert(defaultRow.querySelector('.ov-mp-name').textContent==='Default'&&defaultRow.querySelector('.ov-mp-detail').textContent==='OpenAI · gpt-5.5','global default uses provider and model subtext');
 defaultRow.click();
 assert(trigger.dataset.currentValue==='default'&&!panel.querySelector('.ov-mp-effort').hidden,'global default exposes effort without changing selection');
 const defaultSlider=panel.querySelector('input[type=range]');defaultSlider.value='2';defaultSlider.dispatchEvent(new Event('input'));
 assert(document.querySelector('[name=agent_id]').value==='default'&&document.querySelector('[name=reasoning_effort]').value==='high','global default sends selected effort');
 trigger.scrollIntoView({block:'end'});await nextFrame();
 [...panel.querySelectorAll('.ov-mp-provider')].find(b=>b.dataset.provider==='OpenAI').click();
 for(let i=0;i<8;i++){
   sub.querySelector('.ov-mp-star').click();
   const bounds=panel.getBoundingClientRect();
   assert(bounds.top>=8&&bounds.bottom<=innerHeight-8,'favorite growth keeps primary menu in viewport: '+JSON.stringify({top:bounds.top,bottom:bounds.bottom,height:innerHeight,trigger:trigger.getBoundingClientRect().top}));
   assert(bounds.bottom<=trigger.getBoundingClientRect().top,'favorite growth keeps menu anchored above trigger');
 }
 for(let i=0;i<40;i++) { const saved=await (await fetch('/ui/model-favorites')).json(); if(Object.keys(saved).length===9) break; await wait(); }
 const favoritesSettled=async()=>{for(let i=0;i<200&&panel.querySelector('.ov-mp-star:disabled');i++)await wait();};
 window.ovModelPicker.close(false);localStorage.clear();trigger.click();await favoritesSettled();
 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===9,'favorites load from server after local storage is cleared');
 window.ovModelPicker.close(false);localStorage.setItem('openvibely-model-favorites',JSON.stringify({m10:true}));trigger.click();await favoritesSettled();
 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===10,'legacy browser favorite merges into shared list');
 assert(localStorage.getItem('openvibely-model-favorites')===null,'legacy favorite is removed after import');
 const originalFetch=window.fetch.bind(window);let failFavoriteWrite;
 window.fetch=(url,options)=>{
   if(url==='/ui/model-favorites'&&options?.method==='POST'){
     window.fetch=originalFetch;
     return new Promise(resolve=>{failFavoriteWrite=()=>resolve(new Response('',{status:500}));});
   }
   return originalFetch(url,options);
 };
 panel.querySelector('.ov-mp-star[aria-pressed=true]').click();
 window.ovModelPicker.close(false);trigger.click();
 await wait();failFavoriteWrite();await favoritesSettled();
 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===10,'failed write is rolled back from server');
 assert([...panel.querySelectorAll('.ov-mp-star')].every(star=>!star.disabled),'successful reopen enables favorites after a pending write fails');
 const atFavoritesEnd=()=>Math.abs(list.scrollHeight-list.clientHeight-list.scrollTop)<2;
 list.style.maxHeight='120px';list.scrollTop=0;
 window.ovModelPicker.close(false);trigger.click();await favoritesSettled();
 assert(list.scrollHeight>list.clientHeight&&atFavoritesEnd(),'overflowing picker opens at favorites end');
 let heldFavorites;
 const holdNextFavorites=()=>new Promise(r=>{heldFavorites=r;});
 window.fetch=async(url,options)=>{
   const response=await originalFetch(url,options);
   if(url==='/ui/model-favorites'&&!options){
     const saved=await response.json();saved.m12=true;
     return new Promise(resolve=>heldFavorites(()=>resolve(new Response(JSON.stringify(saved),{status:200}))));
   }
   return response;
 };
 let finishFavorites=holdNextFavorites();
 window.ovModelPicker.close(false);trigger.click();await wait();
 assert(atFavoritesEnd(),'cached favorites visible while loading');
 (await finishFavorites)();await wait();
 assert(atFavoritesEnd(),'loading additional saved favorites keeps bottom visible');
 finishFavorites=holdNextFavorites();
 window.ovModelPicker.close(false);trigger.click();await wait();list.scrollTop=0;
 (await finishFavorites)();await wait();
 assert(list.scrollTop===0,'loading favorites does not undo user scrolling toward providers');
 window.fetch=originalFetch;list.style.maxHeight='';

 document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
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
 if(String(url).startsWith('/ui/model-favorites'))return {ok:true,json:async()=>({})};
 if(options?.method==='POST'){if(failNextWrite){failNextWrite=false;return {ok:false};}const body=options.body;await new Promise(r=>setTimeout(r,20));stored[body.get('agent_id')==='default'?'b':body.get('agent_id')]=body.get('reasoning_effort');posts.push(body.get('agent_id'));return {ok:true};}
 if(failNextRead){failNextRead=false;return {ok:false};}const selected=new URL(url,location.href).searchParams.get('agent_id'),id=selected==='default'?'b':selected;await new Promise(r=>setTimeout(r,id==='a'?80:20));return {ok:true,json:async()=>({reasoning_effort:stored[id]||''})};
 };
 </script>`+content.String()+`<div id="browser-result"></div><script>
 (async()=>{const assert=(v,m)=>{if(!v)throw Error(m)},wait=ms=>new Promise(r=>setTimeout(r,ms));try{
 const trigger=document.getElementById('task-form-agent-select'),panel=document.getElementById('conversation-model-picker'),effort=document.querySelector('[name=reasoning_effort]');
 document.querySelector('textarea').focus();
 const apple=/Mac|iPhone|iPad|iPod|iOS/i.test(navigator.userAgentData?.platform || navigator.platform || navigator.userAgent || '');
 const shortcut=new KeyboardEvent('keydown',{key:'M',metaKey:apple,ctrlKey:!apple,shiftKey:false,bubbles:true,cancelable:true});document.activeElement.dispatchEvent(shortcut);
 assert(shortcut.defaultPrevented && !panel.hidden && document.activeElement===panel.querySelector('input'),'task shortcut opens picker and focuses search');
 assert([...panel.querySelectorAll('.ov-mp-provider')].some(b=>b.dataset.provider==='Mixture'),'mixture configurations are grouped');assert(panel.querySelector('input[type=range]').disabled,'disable until saved effort loads');
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
 assert(sub.hidden,'reopen without automatically opening selected provider');
 [...panel.querySelectorAll('.ov-mp-provider')].find(b=>b.dataset.provider==='Ollama').click();
 assert(!sub.hidden && sub.querySelector('[data-model=c]').getAttribute('aria-pressed')==='true','selected model remains marked when provider opens');
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
 search.value='';search.dispatchEvent(new Event('input'));
 const defaultRow=panel.querySelector('.ov-mp-pick[data-model=default]');
 assert(defaultRow,'clearing search restores the default option');
 assert(defaultRow.querySelector('.ov-mp-name').textContent==='Default'&&defaultRow.querySelector('.ov-mp-detail').textContent==='OpenAI · gpt-5.5','project default uses provider and model subtext');
 defaultRow.click();await trigger._ovModelState.ready;
 assert(modelInput.value==='default'&&effort.value==='high'&&!panel.querySelector('.ov-mp-effort').hidden,'default keeps its selection and loads project model effort');
 const defaultSlider=panel.querySelector('input[type=range]');defaultSlider.value='1';defaultSlider.dispatchEvent(new Event('input'));defaultSlider.dispatchEvent(new Event('change'));
 await trigger._ovModelState.pending;
 assert(stored.b==='medium'&&posts.at(-1)==='default','default effort saves against the project model');

 // Desktop WebViews without requestSubmit enter through the composer's fallback.
 form._chatNextSubmissionPayload=null;
 window.ovModelPicker.close(false);
 const nativeRequestSubmit=form.requestSubmit;
 form.requestSubmit=undefined;
 let desktopSend=null;
 form.addEventListener('submit',event=>{event.preventDefault();desktopSend=new FormData(form);},{once:true});
 message.value='desktop follow-up';message.dispatchEvent(new Event('input',{bubbles:true}));
 message.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));
 await wait(40);
 assert(desktopSend?.get('message')==='desktop follow-up','desktop fallback sends after model settings are ready');
 assert(panel.hidden,'desktop fallback must not open the models menu');
 form.requestSubmit=nativeRequestSubmit;

 // Polling can replace the submitter while the picker waits for settings.
 form._chatNextSubmissionPayload=null;
 let releaseSettings;
 trigger._ovModelState.pending=new Promise(resolve=>{releaseSettings=resolve;});
 let replacedButtonSend=null;
 form.addEventListener('submit',event=>{event.preventDefault();replacedButtonSend=new FormData(form);},{once:true});
 message.value='follow-up after button replacement';
 const oldButton=form.querySelector('button[type=submit]');
 form.requestSubmit(oldButton);
 oldButton.replaceWith(oldButton.cloneNode(true));
 releaseSettings();await wait(40);
 assert(replacedButtonSend?.get('message')==='follow-up after button replacement','send survives composer button replacement');
 assert(panel.hidden,'button replacement must not open the models menu');

 // A failed handoff must not replay its message or attachments on the next Send.
 form._chatNextSubmissionPayload=null;
 message.value='failed original';attachment.value='failed-attachment';
 nativeRequestSubmit.call(form);
 form.requestSubmit=()=>{throw Error('simulated submission failure');};
 await wait(40);
 assert(message.value==='failed original'&&attachment.value==='failed-attachment','failed handoff preserves visible draft');
 assert(!form._chatNextSubmissionPayload,'failed handoff clears its internal snapshot');
 assert(!form.querySelector('[name=model_selection_managed]')&&!trigger._ovResubmit,'failed handoff clears submission guards');
 form.requestSubmit=nativeRequestSubmit;
 window.ovModelPicker.close(false);
 let retrySend=null;
 form.addEventListener('submit',event=>{event.preventDefault();retrySend=new FormData(form);},{once:true});
 message.value='corrected follow-up';attachment.value='corrected-attachment';
 form.requestSubmit();await wait(40);
 assert(retrySend?.get('message')==='corrected follow-up'&&retrySend?.get('attachment_session_id')==='corrected-attachment','retry sends edited text and attachments');
 assert(!form._chatNextSubmissionPayload,'cancelled submission also clears its internal snapshot');

 // A steering snapshot is consumed even if settings fail before resubmission.
 form._chatNextSubmissionPayload={message:'old steering message',attachmentSessionID:'old-steering-attachment',fallbackFromSteer:true};
 trigger._ovModelState.error='simulated settings failure';
 message.value='current visible draft';
 form.requestSubmit();await wait(150);
 assert(!form._chatNextSubmissionPayload,'settings failure clears captured steering snapshot');
 assert(message.value==='current visible draft','settings failure preserves newer draft');

 document.body.dataset.testResult='pass';
 }catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e)}})();
 </script></body></html>`)
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "task picker persistence", 400, 800, 25*time.Second)
}

func TestBrowserFunctional_TaskModelPickerQueuedSnapshot(t *testing.T) {
	chrome := testChromePath(t)
	var content bytes.Buffer
	agents := []models.LLMConfig{
		{ID: "a", Name: "First", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol"},
		{ID: "b", Name: "Second", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol"},
	}
	if err := ChatInputForm(ChatInputFormConfig{FormID: "task-form", InputID: "message", TargetID: "messages", PostEndpoint: "/send", TaskID: "t", Agents: agents, SelectedAgentID: "a", ShowModelSelector: true, IsRunning: true, StopEndpoint: "/stop", ActiveTurnID: "turn"}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var records []map[string]string
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			_, _ = w.Write(htmx204)
		case "/tasks/t/thread/model", "/ui/model-favorites":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{}`)
		case "/stop":
			fmt.Fprint(w, `stopped`)
		case "/release":
			releaseOnce.Do(func() { close(release) })
		case "/records":
			mu.Lock()
			defer mu.Unlock()
			_ = json.NewEncoder(w).Encode(records)
		case "/send":
			_ = r.ParseForm()
			record := make(map[string]string)
			for key := range r.PostForm {
				record[key] = r.PostForm.Get(key)
			}
			mu.Lock()
			records = append(records, record)
			first := len(records) == 1
			mu.Unlock()
			if first {
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprint(w, `<div>accepted</div>`)
		default:
			fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body data-test-result="pending"><div id="messages"></div>`+content.String()+`<div id="browser-result"></div><script>
(async()=>{
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)},wait=()=>new Promise(r=>setTimeout(r,20));
 const until=async(fn)=>{for(let i=0;i<200;i++){if(fn())return;await wait();}throw Error('timed out');};
 try {
 const form=document.getElementById('task-form'),message=form.querySelector('[name=message]'),attachment=form.querySelector('[name=attachment_session_id]'),model=form.querySelector('[name=agent_id]'),effort=form.querySelector('[name=reasoning_effort]'),tier=form.querySelector('[name=service_tier]');
 const picker=form.querySelector('.chat-model-select');await picker._ovModelState.ready;
 const completed=[];let stopped=false;
 assert(form.querySelector('[aria-label="Stop response"]').getAttribute('hx-sync')==='this:drop','rendered Stop has an independent request scope');
 form.addEventListener('htmx:afterRequest',e=>{if(e.detail.elt===form)completed.push({message:e.detail.xhr._chatSubmittedMessage,attachment:e.detail.xhr._chatSubmittedAttachmentSession,fallback:e.detail.xhr._chatSubmittedFallbackFromSteer});else if(e.detail.elt.getAttribute('aria-label')==='Stop response')stopped=true;});
 message.value='first';message.dispatchEvent(new Event('input',{bubbles:true}));form.requestSubmit();
 let records=[];for(let i=0;i<200;i++){records=await (await fetch('/records')).json();if(records?.length)break;await wait();}
 assert(records?.length===1,'first request is held in flight');
 // Simulate a rejected steering request falling back to a normal send.
 form._chatNextSubmissionPayload={message:'queued follow-up',attachmentSessionID:'queued-attachment',fallbackFromSteer:true};
 message.value='queued follow-up';attachment.value='queued-attachment';effort.value='high';tier.value='default';
 let handedOff=false;form.addEventListener('submit',()=>{handedOff=true;},{once:true});
 form.requestSubmit();await until(()=>handedOff);
 // A later send must not replace the waiting steering fallback.
 message.value='third message';attachment.value='third-attachment';model.value='b';effort.value='medium';tier.value='priority';
 handedOff=false;form.addEventListener('submit',()=>{handedOff=true;},{once:true});
 form.requestSubmit();await until(()=>handedOff);
 // Rebuilt Stop controls must still run while sends are waiting.
 message.value='';message.dispatchEvent(new Event('input',{bubbles:true}));
 form.querySelector('[aria-label="Stop response"]').click();await until(()=>stopped);
 assert(completed.length===0,'Stop completes independently of the held send');
 message.value='unsent newer draft';attachment.value='newer-attachment';model.value='b';effort.value='low';tier.value='default';
 message.dispatchEvent(new Event('input',{bubbles:true}));
 await fetch('/release');await until(()=>completed.length>=2);
 records=await (await fetch('/records')).json();
 assert(records[1]?.message==='queued follow-up','later send must not drop the waiting follow-up');
 await until(()=>completed.length===3);
 records=await (await fetch('/records')).json();
 assert(records.length===3,'all three sends reach the server exactly once');
 const sent=records[1];
 assert(sent.message==='queued follow-up','queued request must not send the newer draft: '+sent.message);
 assert(sent.service_tier==='default'&&records[2].service_tier==='priority'&&tier.value==='default','queued sends retain their own tier and preserve draft tier');
 assert(sent.attachment_session_id==='queued-attachment','queued request retains attachments');
 assert(sent.agent_id==='a'&&sent.reasoning_effort==='high'&&sent.model_selection_managed==='1','queued request retains model ownership and effort');
 assert(completed[1].message==='queued follow-up'&&completed[1].attachment==='queued-attachment'&&completed[1].fallback===true,'queued request retains completion metadata');
 assert(records[2].message==='third message'&&records[2].attachment_session_id==='third-attachment'&&records[2].agent_id==='b'&&records[2].reasoning_effort==='medium'&&records[2].model_selection_managed==='1','third send retains its own snapshot');
 assert(completed[2].message==='third message'&&completed[2].fallback===false,'third send retains independent completion metadata');
 assert(message.value==='unsent newer draft'&&attachment.value==='newer-attachment'&&model.value==='b'&&effort.value==='low','completion preserves newer draft and settings');
 assert(!form._chatNextSubmissionPayload,'no stale snapshot on the form');
 document.body.dataset.testResult='pass';
 }catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e);await fetch('/release');}
})();</script></body></html>`)
		}
	}))
	defer func() {
		releaseOnce.Do(func() { close(release) })
		srv.Close()
	}()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "queued model picker snapshot", 1000, 800, 25*time.Second)
}

func TestBrowserFunctional_ModelPickerFastMode(t *testing.T) {
	chrome := testChromePath(t)
	for _, scope := range []string{"chat", "task"} {
		t.Run(scope, func(t *testing.T) {
			agents := []models.LLMConfig{
				{ID: "fast", Name: "Fast model", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol", ServiceTier: "priority"},
				{ID: "other", Name: "Other model", Provider: models.ProviderOpenAI, Model: "gpt-6.1-terra"},
				{ID: "local", Name: "Local model", Provider: models.ProviderOllama, Model: "qwen"},
				{ID: "opus", Name: "Opus model", Provider: models.ProviderAnthropic, Model: "claude-opus-5-5"},
				{ID: "sonnet", Name: "Sonnet model", Provider: models.ProviderAnthropic, Model: "claude-sonnet-5-5"},
			}
			config := ChatInputFormConfig{FormID: "fast-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/send", ProjectID: "fast-project", Agents: agents, SelectedAgentID: "fast", DefaultAgentID: "fast", ShowModelSelector: true}
			if scope == "task" {
				config.TaskID = "fast-task"
			}
			var content bytes.Buffer
			if err := ChatInputForm(config).Render(context.Background(), &content); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<!doctype html><html><body data-test-result="pending"><script>
localStorage.clear();window.htmx={process:function(){}};const stored={},posts=[];
window.fetch=async(url,opts)=>{if(String(url).startsWith('/ui/model-favorites'))return {ok:true,json:async()=>({})};
if(opts?.method==='POST'){const id=opts.body.get('agent_id');stored[id]=opts.body.get('service_tier');posts.push(Object.fromEntries(opts.body));return {ok:true};}
const id=new URL(url,location.href).searchParams.get('agent_id');return {ok:true,json:async()=>({service_tier:stored[id]||'',reasoning_effort:''})};};
</script>`+content.String()+`<div id="browser-result"></div><script>
(async()=>{const assert=(v,m)=>{if(!v)throw Error(m)};try{
const trigger=document.getElementById('fast-form-agent-select');window.ovModelPicker.open(trigger);await trigger._ovModelState.ready;
const panel=document.getElementById('conversation-model-picker'),hidden=document.querySelector('[name=service_tier]');
const waitSave=async()=>{await trigger._ovModelState.pending;};const toggle=()=>panel.querySelector('.ov-mp-fast-toggle');
assert(!panel.querySelector('.ov-mp-speed').hidden,'supported model exposes Fast mode');
assert(toggle().getAttribute('aria-checked')==='true'&&hidden.value==='','inherit Fast model default');
toggle().click();await waitSave();assert(hidden.value==='default'&&toggle().getAttribute('aria-checked')==='false','explicit off selects standard');
const choose=async(name)=>{panel.querySelector('.ov-mp-search input').value=name;panel.querySelector('.ov-mp-search input').dispatchEvent(new Event('input'));panel.querySelector('.ov-mp-pick').click();await trigger._ovModelState.ready;};
await choose('Local model');assert(panel.querySelector('.ov-mp-speed').hidden&&hidden.value==='','unsupported model omits toggle and tier');
await choose('Other model');assert(toggle().getAttribute('aria-checked')==='false'&&hidden.value==='','other model has independent default');
toggle().click();await waitSave();assert(hidden.value==='priority','Fast selection');
await choose('Fast model');assert(hidden.value==='default','restore per-model override');
panel.querySelector('.ov-mp-speed .ov-mp-reset').click();await waitSave();assert(hidden.value===''&&toggle().getAttribute('aria-checked')==='true','Use default restores configured Fast');
if(trigger.closest('form').dataset.modelSelectEndpoint){assert(posts.some(p=>p.agent_id==='fast'&&p.service_tier==='default'),'task override persisted');assert(stored.fast==='','task reset persisted');}
else{assert(JSON.parse(localStorage.getItem('chat-service-tier-fast-project'))['other:gpt-6.1-terra']==='priority','chat override persisted');}
await choose('Opus model');assert(!panel.querySelector('.ov-mp-speed').hidden,'Anthropic supported model exposes Fast');
toggle().click();await waitSave();assert(hidden.value==='priority','Anthropic Fast override');
await choose('Sonnet model');assert(panel.querySelector('.ov-mp-speed').hidden&&hidden.value==='','unsupported Anthropic model omits Fast');
await choose('Opus model');assert(hidden.value==='priority','Anthropic override restored');
panel.querySelector('.ov-mp-speed .ov-mp-reset').click();await waitSave();assert(hidden.value===''&&toggle().getAttribute('aria-checked')==='false','Anthropic reset');
const form=trigger.closest('form');const captured=form._captureChatSubmission();assert(captured.serviceTier==='','submission snapshot includes default override');
document.body.dataset.testResult='pass';
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e);}})();
</script></body></html>`)
			}))
			defer srv.Close()
			runHeadlessChromeCDPFixture(t, chrome, srv.URL, "fast mode "+scope, 1000, 800, 15*time.Second)
		})
	}
}
