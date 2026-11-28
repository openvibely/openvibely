package components

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	pickerSettings := browserConversationSettings("m0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pickerSettings(w, r) {
			return
		}
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
 // A shared Chat destination is outside the form, unlike ordinary project forms.
 const projectField=document.querySelector('[name="_project_id"]');
 const originalProject=projectField.value;projectField.removeAttribute('name');
 const destination=document.createElement('button');destination.dataset.projectInput='remote-destination';trigger.closest('form').append(destination);
 const remoteInput=document.createElement('input');remoteInput.id='remote-destination';remoteInput.value='r-1234567890abcdef-project';document.body.append(remoteInput);
 const scopeFetch=window.fetch;let requestedFavorites='';
 window.fetch=async(url,opts)=>{if(String(url).startsWith('/ui/model-favorites')){requestedFavorites=String(url);return new Response(JSON.stringify({m0:true}),{headers:{'Content-Type':'application/json'}});}return scopeFetch(url,opts);};
 trigger.click();for(let i=0;i<100&&!document.querySelector('#conversation-model-picker .ov-mp-star[aria-pressed=true]');i++)await wait();
 assert(requestedFavorites.includes('project_id=r-1234567890abcdef-project'),'shared Chat favorites use chosen destination');
 assert(document.querySelector('#conversation-model-picker .ov-mp-star[aria-pressed=true]'),'remote favorite is displayed');
 window.ovModelPicker.close();window.fetch=scopeFetch;destination.remove();remoteInput.remove();projectField.name='_project_id';projectField.value=originalProject;
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
	   assert(picker.hidden && document.activeElement===trigger && trigger.getAttribute('aria-expanded')==='false','shortcut closes picker and restores trigger focus on '+platform);
	   assert(shortcut({metaKey:mac,ctrlKey:!mac}).defaultPrevented && !picker.hidden && document.activeElement===picker.querySelector('input'),'shortcut reopens picker on '+platform);
	   window.ovModelPicker.close(false);
	   if(originalPlatform)Object.defineProperty(navigator,'platform',originalPlatform);else delete navigator.platform;
	   if(originalData)Object.defineProperty(navigator,'userAgentData',originalData);else delete navigator.userAgentData;
	 }
	 trigger.click();await wait();await favoritesLoaded();
	 const panel=document.getElementById('conversation-model-picker');
	 assert(!panel.hidden,'picker opens');
	 assert(!document.getElementById('conversation-provider-models'),'unified left-rail picker omits detached submenu');
	 assert(panel.querySelectorAll('.ov-mp-provider').length===2,'provider rail icons');
	 const providers=[...panel.querySelectorAll('.ov-mp-provider')];
	 assert(providers.every(b=>!!b.querySelector('svg')),'every provider rail icon renders an SVG');
	 assert(!!panel.querySelector('.ov-mp-rail-action svg'),'manage models rail action renders an SVG');
	 // Active model m0 (OpenAI) is not favorited yet, so opening shows OpenAI with m0 selected.
	 assert(panel.querySelector('.ov-mp-provider[data-provider="OpenAI"]').getAttribute('aria-selected')==='true','unfavorited active model opens on its provider tab');
	 assert(panel.querySelector('.ov-mp-pick[data-model="m0"]').closest('.ov-mp-row').dataset.selected==='true','active model visible and marked selected');
	 const originalLeft=panel.getBoundingClientRect().left;
	 const originalTriggerRect=trigger.getBoundingClientRect.bind(trigger);
	 trigger.getBoundingClientRect=()=>{const r=originalTriggerRect();return {...r,left:r.left-2};};
	 // Hovering a provider icon immediately switches the right-side model list without clicking.
	 providers[0].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
	 assert(panel.querySelector('.ov-mp-provider[data-provider="Anthropic"]').getAttribute('aria-selected')==='true','hover activates Anthropic rail icon');
	 assert(panel.querySelectorAll('.ov-mp-pick').length===12,'Anthropic provider lists its 12 models');
	 assert(panel.getBoundingClientRect().left===originalLeft,'switching providers does not shift picker');
	 trigger.getBoundingClientRect=originalTriggerRect;
	 const anthStarRight=panel.querySelector('.ov-mp-star').getBoundingClientRect().right;
	 const pinsBtn=panel.querySelector('.ov-mp-rail-btn[data-tab="pins"]');
	 pinsBtn.dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
	 assert(pinsBtn.getAttribute('aria-selected')==='true','hover switches to Favorites rail icon');
	 assert(panel.querySelectorAll('.ov-mp-pick').length===2,'Favorites without pins shows Auto and Default');
	 assert(!panel.querySelector('.ov-mp-heading'),'no redundant section headings');
	 // Switch back to Anthropic and verify row structure, hover highlight, and reserved scrollbar width.
	 providers[0].dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
	 const row=panel.querySelector('.ov-mp-row');
	 assert(row.firstElementChild.classList.contains('ov-mp-pick')&&row.firstElementChild.firstElementChild.classList.contains('ov-mp-copy')&&row.lastElementChild.classList.contains('ov-mp-star'),'text starts at normal inset, favorite on right');
	 row.dispatchEvent(new PointerEvent('pointermove'));
	 assert(row.hasAttribute('data-picker-active'),'hover activates model row');
	 document.documentElement.style.setProperty('--bc','0.8 0 0');
	 document.documentElement.style.setProperty('--shell-row-selected','rgb(40, 45, 55)');
	 const hoverColor=getComputedStyle(row).backgroundColor;
	 const originalSelected=row.dataset.selected;row.dataset.selected='true';
	 assert(getComputedStyle(row).backgroundColor==='rgb(40, 45, 55)'&&hoverColor!==getComputedStyle(row).backgroundColor,'selection uses task-list color and remains distinct from hover');
	 row.dataset.selected=originalSelected;

	 const hoverObserver=new MutationObserver(()=>{});hoverObserver.observe(panel,{attributes:true,subtree:true,attributeFilter:['data-picker-active']});
	 for(let i=0;i<10;i++)row.dispatchEvent(new PointerEvent('pointermove'));
	 assert(hoverObserver.takeRecords().length===0,'moving within model row does not rewrite highlight');hoverObserver.disconnect();

	 const next=row.nextElementSibling;next.dispatchEvent(new PointerEvent('pointermove'));
	 assert(!row.hasAttribute('data-picker-active')&&next.hasAttribute('data-picker-active'),'hover moves highlight');
	 const hover=(target)=>target.dispatchEvent(new PointerEvent('pointerover',{bubbles:true,pointerType:'mouse'}));
	 hover(panel.querySelector('.ov-mp-search input'));
	 assert(!panel.querySelector('[data-picker-active]'),'search hover clears row highlight');
	 next.dispatchEvent(new PointerEvent('pointermove'));
	 hover(panel.querySelector('.ov-mp-effort'));
	 assert(!panel.querySelector('[data-picker-active]'),'effort hover clears row highlight');
	 // Pin an Anthropic model; it stays in Anthropic (marked favorited) and appears on top in Favorites.
	 panel.querySelector('.ov-mp-star').click();
	 assert(panel.querySelectorAll('.ov-mp-pick').length===12,'favorited model remains listed in its provider tab');
	 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===1,'favorite star updated in provider tab');
	 pinsBtn.dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
	 const favStarRight=panel.querySelector('.ov-mp-star').getBoundingClientRect().right;
	 assert(Math.abs(favStarRight-anthStarRight)<0.5,'reserved scrollbar gutter keeps stars aligned between scrolling and non-scrolling lists');
	 const menuRows=[...panel.querySelector('.ov-mp-list').children];
	 const firstFavIndex=menuRows.findIndex(e=>e.querySelector('.ov-mp-star[aria-pressed=true]'));
	 const autoIndex=menuRows.findIndex(e=>e.querySelector('[data-model="auto"]'));
	 const defaultIndex=menuRows.findIndex(e=>e.querySelector('[data-model="default"]'));
	 assert(firstFavIndex===0&&firstFavIndex<autoIndex&&autoIndex<defaultIndex,'Favorites tab puts pinned models on top above Auto and Default');
	 // Search across all providers.
	 const search=panel.querySelector('input');search.value='Configuration 23';search.dispatchEvent(new Event('input'));
	 assert(panel.querySelectorAll('.ov-mp-pick').length===1,'search all providers');
	 panel.querySelector('.ov-mp-pick').click();
	 assert(!panel.hidden && trigger.dataset.currentValue==='m23','select keeps picker open');
	 await trigger._ovModelState.ready;
	 const slider=panel.querySelector('input[type=range]');
	 document.documentElement.dataset.theme='light';
	 assert(getComputedStyle(slider).getPropertyValue('--ov-mp-track').trim()==='#d5d8df','light track is light gray');
	 const accent=getComputedStyle(slider).getPropertyValue('--ov-mp-accent');
	 assert(panel.querySelector('.ov-mp-effort-head').hidden,'effort header hidden without override (no Reasoning effort label)');
	 slider.value='1';slider.dispatchEvent(new Event('input'));
	 assert(!panel.querySelector('.ov-mp-effort-head').hidden && panel.querySelector('.ov-mp-effort-head').textContent==='Use default','reset keeps its action label without Reasoning effort heading');
	 assert(getComputedStyle(slider).getPropertyValue('--ov-mp-accent')===accent,'adjustment keeps same accent');
	 document.documentElement.dataset.theme='dark';
	 assert(getComputedStyle(slider).getPropertyValue('--ov-mp-track').trim()==='#4b5260','dark track is gray');
	 slider.value='2';slider.dispatchEvent(new Event('input'));slider.dispatchEvent(new Event('change'));
	 assert(document.querySelector('[name=reasoning_effort]').value==='high','effort submitted');
	 const bottomBeforeReset=panel.getBoundingClientRect().bottom;
	 panel.querySelector('.ov-mp-reset').click();
	 assert(document.querySelector('[name=reasoning_effort]').value==='','restore model default');
	 assert(Math.abs(panel.getBoundingClientRect().bottom-bottomBeforeReset)<2,'resetting effort keeps menu anchored above trigger');
	 assert(panel.getBoundingClientRect().left>=0 && panel.getBoundingClientRect().right<=innerWidth,'panel fits');
	 search.value='';search.dispatchEvent(new Event('input'));await wait();
	 const list=panel.querySelector('.ov-mp-list'),railTabs=panel.querySelector('.ov-mp-rail-tabs');
	 assert(!panel.querySelector('.ov-mp-scroll-hints'),'no more-above/below controls');
	 // Test WebKit wheel scrolling on both the model list and the left rail.
	 list.style.maxHeight='75px';await wait();
	 assert(list.scrollHeight>list.clientHeight&&list.offsetWidth>list.clientWidth,'visible scrollbar when overflowing');
	 list.scrollTop=0;
	 const wheel=(target,dy,extra={})=>{const e=new WheelEvent('wheel',{deltaY:dy,bubbles:true,cancelable:true,...extra});target.dispatchEvent(e);return e;};
	 assert(wheel(list.firstElementChild,50).defaultPrevented&&list.scrollTop>0,'wheel on model row scrolls overflowing list');
	 assert(wheel(list,-50).defaultPrevented&&list.scrollTop===0,'wheel scrolls back to first model');
	 wheel(list,2,{deltaMode:1});assert(list.scrollTop===32,'line-mode wheel units');
	 assert(!wheel(list,50,{ctrlKey:true}).defaultPrevented&&list.scrollTop===32,'preserve zoom gestures');
	 assert(!wheel(list,5,{deltaX:60}).defaultPrevented&&list.scrollTop===32,'preserve horizontal gestures');
	 wheel(list,1,{deltaMode:2});assert(list.scrollTop>32,'page-mode wheel units');
	 assert(!panel.querySelector('.ov-mp-effort').hidden,'effort stays accessible while list scrolls');
	 list.style.maxHeight='';await wait();
	 railTabs.style.maxHeight='50px';railTabs.scrollTop=0;await wait();
	 assert(railTabs.scrollHeight>railTabs.clientHeight,'rail overflows when constrained');
	 assert(wheel(railTabs.firstElementChild,40).defaultPrevented&&railTabs.scrollTop>0,'wheel on rail icon scrolls overflowing left rail');
	 assert(wheel(railTabs,-40).defaultPrevented&&railTabs.scrollTop===0,'wheel scrolls left rail back to top');
	 railTabs.style.maxHeight='';await wait();
	 // Default option lives in Favorites tab and exposes effective model effort.
	 pinsBtn.click();
	 const defaultRow=panel.querySelector('.ov-mp-pick[data-model=default]');
	 assert(defaultRow.querySelector('.ov-mp-name').textContent==='Default'&&defaultRow.querySelector('.ov-mp-detail').textContent==='OpenAI · gpt-5.5','global default uses provider and model subtext');
	 defaultRow.click();await trigger._ovModelState.ready;
	 assert(trigger.dataset.currentValue==='default'&&!panel.querySelector('.ov-mp-effort').hidden,'global default exposes effort without changing selection');
	 const defaultSlider=panel.querySelector('input[type=range]');defaultSlider.value='2';defaultSlider.dispatchEvent(new Event('input'));
	 assert(document.querySelector('[name=agent_id]').value==='default'&&document.querySelector('[name=reasoning_effort]').value==='high','global default sends selected effort');
	 trigger.scrollIntoView({block:'end'});await nextFrame();
	 panel.querySelector('.ov-mp-provider[data-provider="OpenAI"]').click();
	 for(let i=0;i<8;i++){
	   const unpinned=panel.querySelector('.ov-mp-star[aria-pressed=false]');
	   unpinned.click();
	   const bounds=panel.getBoundingClientRect();
	   assert(bounds.top>=8&&bounds.bottom<=innerHeight-8,'favorite growth keeps primary menu in viewport: '+JSON.stringify({top:bounds.top,bottom:bounds.bottom,height:innerHeight,trigger:trigger.getBoundingClientRect().top}));
	   assert(bounds.bottom<=trigger.getBoundingClientRect().top,'favorite growth keeps menu anchored above trigger');
	 }
	 for(let i=0;i<40;i++) { const saved=await (await fetch('/ui/model-favorites')).json(); if(Object.keys(saved).length===9) break; await wait(); }
	 const favoritesSettled=async()=>{for(let i=0;i<200&&panel.querySelector('.ov-mp-star:disabled');i++)await wait();};
	 window.ovModelPicker.close(false);localStorage.clear();trigger.click();await favoritesSettled();
	 assert(panel.querySelector('.ov-mp-rail-btn[data-tab="pins"]').getAttribute('aria-selected')==='true','reopening with Default opens Favorites tab');
	 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===9,'favorites load from server after local storage is cleared');
	 // Select a favorited model (m0) and verify reopen opens on Favorites with m0 visible.
	 panel.querySelector('.ov-mp-pick[data-model="m0"]').click();await trigger._ovModelState.ready;
	 window.ovModelPicker.close(false);localStorage.setItem('openvibely-model-favorites',JSON.stringify({m10:true}));trigger.click();await favoritesSettled();
	 assert(panel.querySelector('.ov-mp-rail-btn[data-tab="pins"]').getAttribute('aria-selected')==='true','reopening with favorited model opens Favorites tab');
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
	 assert(panel.contains(document.activeElement),'unfavoriting from Favorites tab retains focus inside picker');
	 window.ovModelPicker.close(false);trigger.click();
	 await wait();failFavoriteWrite();await favoritesSettled();
	 assert(panel.querySelectorAll('.ov-mp-star[aria-pressed=true]').length===10,'failed write is rolled back from server');
	 assert([...panel.querySelectorAll('.ov-mp-star')].every(star=>!star.disabled),'successful reopen enables favorites after a pending write fails');
	 // Keyboard navigation across rail tabs and model list.
	 const railTab0=panel.querySelector('.ov-mp-rail-btn[data-tab="pins"]');
	 railTab0.focus();
	 railTab0.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowDown',bubbles:true,cancelable:true}));
	 assert(document.activeElement.dataset.provider==='Anthropic'&&list.getAttribute('aria-labelledby')===document.activeElement.id,'ArrowDown moves rail focus and updates tabpanel label');
	 document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowRight',bubbles:true,cancelable:true}));
	 assert(document.activeElement.classList.contains('ov-mp-pick'),'ArrowRight moves focus from rail into model list');
	 document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'ArrowLeft',bubbles:true,cancelable:true}));
	 assert(document.activeElement.dataset.provider==='Anthropic','ArrowLeft returns focus to active rail tab');
	 // Adding many providers must scroll inside the left rail without stretching the menu height.
	 const heightWithTwoProviders=panel.getBoundingClientRect().height;
	 const optionList=trigger.parentElement.querySelector('ul');
	 const extraProviders=['Cerebras','DeepInfra','DeepSeek','Fireworks','Google Gemini','Groq','LM Studio','Local vLLM','Mistral','Mixture','Moonshot','NVIDIA NIM','Ollama','OpenRouter','Together'];
	 const addedOptions=extraProviders.map((p,i)=>{const li=document.createElement('li');li.dataset.value='extra-'+i;li.dataset.pickerName=p+' Model';li.dataset.pickerModel='m-'+i;li.dataset.pickerProvider=p;li.textContent=p+' Model';optionList.append(li);return li;});
	 window.ovModelPicker.close(false);trigger.click();await favoritesSettled();
	 const manyRect=panel.getBoundingClientRect();
	 assert(Math.abs(manyRect.height-heightWithTwoProviders)<2,'many providers do not stretch menu height: '+manyRect.height+' vs '+heightWithTwoProviders);
	 assert(railTabs.scrollHeight>railTabs.clientHeight,'many providers overflow inside left rail');
	 railTabs.scrollTop=0;
	 assert(wheel(railTabs.firstElementChild,80).defaultPrevented&&railTabs.scrollTop>0,'wheel scrolls overflowing 17-provider rail');
	 addedOptions.forEach(li=>li.remove());
	 window.ovModelPicker.close(false);trigger.click();await favoritesSettled();
	 // While favorites are loading, switching tabs manually must not be overridden when favorites finish loading.
	 let heldFavorites;
	 const holdNextFavorites=()=>new Promise(r=>{heldFavorites=r;});
	 window.fetch=async(url,options)=>{
	   const response=await originalFetch(url,options);
	   if(url==='/ui/model-favorites'&&!options){
	     const saved=await response.json();saved.m13=true;
	     return new Promise(resolve=>heldFavorites(()=>resolve(new Response(JSON.stringify(saved),{status:200}))));
	   }
	   return response;
	 };
	 let finishFavorites=holdNextFavorites();
	 window.ovModelPicker.close(false);trigger.click();await wait();
	 panel.querySelector('.ov-mp-provider[data-provider="Anthropic"]').dispatchEvent(new PointerEvent('pointerenter',{pointerType:'mouse'}));
	 (await finishFavorites)();await wait();
	 assert(panel.querySelector('.ov-mp-provider[data-provider="Anthropic"]').getAttribute('aria-selected')==='true','loading favorites does not override user tab switch');
	 window.fetch=originalFetch;

	 document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));
	 assert(panel.hidden,'escape closes unified picker in one press'); document.body.setAttribute('data-test-result','pass');
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
	if err := ChatInputForm(ChatInputFormConfig{FormID: "task-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/tasks/t/thread", TaskID: "t", ProjectID: "task-project", Agents: agents, DefaultAgentID: "b", SelectedAgentID: "a", ShowModelSelector: true}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<!doctype html><html><head><style>body{font-family:sans-serif}form{margin-top:550px}</style></head><body data-test-result="pending"><script>
 localStorage.clear();window.htmx={process:function(){}};
 const stored={a:'high',b:'low'},posts=[];let failNextWrite=false,failNextRead=false,denyNextWrite=false;
 const permissionMessage="You have viewer access. Editor access is required to change the model or its settings.",toasts=[];window.showToast=(message,status)=>toasts.push({message,status});
 window.fetch=async function(url,options){
 if(String(url).startsWith('/ui/model-favorites'))return {ok:true,json:async()=>({})};
 // Another project is selected globally; task requests must identify their own project.
 if(new URL(url,location.href).searchParams.get('project_id')!=='task-project')throw Error('task settings request used the globally selected project');
 if(options?.method==='POST'){if(denyNextWrite){denyNextWrite=false;return new Response(JSON.stringify({message:permissionMessage}),{status:403,headers:{'X-OpenVibely-Permission-Denied':'true','HX-Trigger':JSON.stringify({openvibelyToast:{message:permissionMessage,status:'failed'}})}});}if(failNextWrite){failNextWrite=false;return {ok:false};}const body=options.body;await new Promise(r=>setTimeout(r,20));stored[body.get('agent_id')==='default'?'b':body.get('agent_id')]=body.get('reasoning_effort');posts.push(body.get('agent_id'));return {ok:true};}
 if(failNextRead){failNextRead=false;return {ok:false};}const selected=new URL(url,location.href).searchParams.get('agent_id')||'a',id=selected==='default'?'b':selected;await new Promise(r=>setTimeout(r,id==='a'?80:20));return {ok:true,json:async()=>({reasoning_effort:stored[id]||''})};
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
 // Hold the pressed-button scale while opening, then release it before selecting.
 // Positioning must use the stable wrapper rather than the animated button bounds.
 window.ovModelPicker.close(false);
 trigger.style.transform='scale(0.95)';trigger.click();
 const openedLeft=panel.getBoundingClientRect().left;
 trigger.style.transform='';
 choose('b');await trigger._ovModelState.ready;assert(effort.value==='low'&&stored.b==='low','restore other model effort');
 assert(Math.abs(panel.getBoundingClientRect().left-openedLeft)<0.5,'model selection keeps menu horizontally anchored after button press');
 choose('a');choose('b');await trigger._ovModelState.ready;await wait(100);
 assert(trigger.dataset.currentValue==='b'&&effort.value==='low'&&posts.at(-1)==='b','ignore stale model lookup');
 const slider=panel.querySelector('input[type=range]');slider.value='2';slider.dispatchEvent(new Event('input'));slider.dispatchEvent(new Event('change'));await trigger._ovModelState.pending;
 assert(stored.b==='high'&&stored.a==='high','persist override for selected configuration');
 choose('c');await trigger._ovModelState.ready;assert(panel.querySelector('.ov-mp-effort').hidden&&effort.value==='','unsupported model has no slider or stale effort');
	 // Selecting from a provider keeps the selected row visible even without effort controls.
	 const search=panel.querySelector('input');search.value='';search.dispatchEvent(new Event('input'));
	 [...panel.querySelectorAll('.ov-mp-provider')].find(b=>b.dataset.provider==='Ollama').click();
	 panel.querySelector('[data-model=c]').click();await trigger._ovModelState.ready;
	 assert(panel.querySelector('[data-model=c]').getAttribute('aria-pressed')==='true','selected model remains visible without effort');
	 assert(panel.querySelector('[data-model=c]').closest('.ov-mp-row').dataset.selected==='true'&&!panel.querySelector('.ov-mp-check'),'selected row without checkmark or effort');
	 window.ovModelPicker.close(false);trigger.click();
	 assert(panel.querySelector('.ov-mp-provider[data-provider="Ollama"]').getAttribute('aria-selected')==='true','reopen shows unfavorited active model provider tab');
	 assert(panel.querySelector('[data-model=c]').getAttribute('aria-pressed')==='true','selected model remains marked on reopen');
	 assert(panel.getBoundingClientRect().right<=innerWidth,'small viewport bounds'); failNextRead=true;choose('b');await trigger._ovModelState.ready;
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
 const writesBeforeDenial=posts.length;
 denyNextWrite=true;choose('b');await trigger._ovModelState.ready;
 assert(posts.length===writesBeforeDenial,'denied preference was not saved');
 assert(toasts.length===1&&toasts[0].message===permissionMessage&&toasts[0].status==='failed','permission explanation shown as one error toast');
 assert(trigger._ovModelState.error===permissionMessage&&panel.querySelector('.ov-mp-status').textContent==='', 'permission error must be toast-only');
 assert(!panel.textContent.includes(permissionMessage),'permission explanation must not appear in picker');
 panel.querySelector('input').dispatchEvent(new Event('input'));
 assert(!panel.textContent.includes(permissionMessage),'rerender must not restore inline permission error');
 assert(!panel.querySelector('input[type=range]').disabled,'permission error must not disable picker controls');
 choose('a');await trigger._ovModelState.ready;

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
	 panel.querySelector('.ov-mp-rail-btn[data-tab="pins"]').click();
	 const defaultRow=panel.querySelector('.ov-mp-pick[data-model=default]');
	 assert(defaultRow,'Favorites tab exposes the default option'); assert(defaultRow.querySelector('.ov-mp-name').textContent==='Default'&&defaultRow.querySelector('.ov-mp-detail').textContent==='OpenAI · gpt-5.5','project default uses provider and model subtext');
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
	if err := ChatInputForm(ChatInputFormConfig{FormID: "task-form", InputID: "message", TargetID: "messages", PostEndpoint: "/send", TaskID: "t", ProjectID: "task-project", Agents: agents, SelectedAgentID: "a", ShowModelSelector: true, IsRunning: true, StopEndpoint: "/stop", ActiveTurnID: "turn"}).Render(context.Background(), &content); err != nil {
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
			_, _ = w.Write(htmx2011)
		case "/conversation/model-settings", "/ui/model-favorites":
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
 // Cards use the same acceptance lifecycle as text while settings are saving.
 function paste(text) {const data=new DataTransfer();data.setData('text/plain',text);message.dispatchEvent(new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true}));}
 const cards=()=>form.querySelectorAll('[aria-label="Edit pasted text"]');
 const large='large paste '.repeat(500),newer='next paste '.repeat(500);
 message.value='instructions';paste(large);
 let releaseSettings;picker._ovModelState.pending=new Promise(resolve=>{releaseSettings=resolve;});
 form.requestSubmit();
 assert(message.value==='instructions' && cards().length===1,'settings wait cleared the draft');
 message.value='new draft';paste(newer);
 releaseSettings();await until(()=>completed.length===4);
 records=await (await fetch('/records')).json();
 assert(records[3].message==='instructions\n\n'+large,'settings wait lost submitted content');
 assert(form._captureChatSubmission().message==='new draft\n\n'+newer,'settings completion erased new draft');
 form.querySelector('[aria-label="Remove pasted text"]').click();
 message.value='retry instructions';paste(large);
 let rejectSettings;picker._ovModelState.pending=new Promise((resolve,reject)=>{rejectSettings=reject;});
 form.requestSubmit();rejectSettings(Error('settings save failed'));
 await until(()=>!form._chatSubmitAfterModelSavePending);await wait();
 assert(form._captureChatSubmission().message==='retry instructions\n\n'+large,'settings failure changed draft');
 window.ovModelPicker.close(false);

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
const id=new URL(url,location.href).searchParams.get('agent_id')||'fast';return {ok:true,json:async()=>({service_tier:stored[id]||'',reasoning_effort:''})};};
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
assert(localStorage.getItem('chat-service-tier-fast-project')===null,'preferences are not stored in localStorage');
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

// Models a shared persistent API for the browser fixtures.
func browserConversationSettings(selected string) func(http.ResponseWriter, *http.Request) bool {
	var mu sync.Mutex
	overrides := map[string]map[string]string{}
	saved := false
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/conversation/model-settings" {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost {
			if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
				var payload struct {
					AgentID    string                       `json:"agent_id"`
					ImportOnly bool                         `json:"import_only"`
					Overrides  map[string]map[string]string `json:"overrides"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				if !saved {
					selected = payload.AgentID
					for key, value := range payload.Overrides {
						overrides[strings.SplitN(key, ":", 2)[0]] = value
					}
					saved = true
				}
				w.WriteHeader(http.StatusNoContent)
				return true
			}
			saved = true
			_ = r.ParseForm()
			selected = r.FormValue("agent_id")
			overrides[selected] = map[string]string{"reasoning_effort": r.FormValue("reasoning_effort"), "service_tier": r.FormValue("service_tier")}
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		id := r.URL.Query().Get("agent_id")
		if id == "" {
			id = selected
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"agent_id": id, "reasoning_effort": overrides[id]["reasoning_effort"], "service_tier": overrides[id]["service_tier"]})
		return true
	}
}

func TestBrowserFunctional_ConversationSettingsSurviveStorageClear(t *testing.T) {
	chrome := testChromePath(t)
	for _, scope := range []string{"chat", "task"} {
		t.Run(scope, func(t *testing.T) {
			config := ChatInputFormConfig{FormID: "persist-form", InputID: "message", TargetID: "messages", PostEndpoint: "/send", ProjectID: "owner", SelectedAgentID: "a", ShowModelSelector: true, Agents: []models.LLMConfig{
				{ID: "a", Name: "Assigned", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol"},
				{ID: "b", Name: "Legacy chat", Provider: models.ProviderOpenAI, Model: "gpt-6.1-sol"},
			}}
			if scope == "task" {
				config.TaskID = "task"
			}
			var content bytes.Buffer
			if err := ChatInputForm(config).Render(context.Background(), &content); err != nil {
				t.Fatal(err)
			}
			settings := browserConversationSettings("a")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/conversation/model-settings" {
					if r.URL.Query().Get("project_id") != "owner" || (scope == "task" && r.URL.Query().Get("task_id") != "task") {
						http.Error(w, "wrong project", 404)
						return
					}
					settings(w, r)
					return
				}
				if r.URL.Path == "/ui/model-favorites" {
					fmt.Fprint(w, `{}`)
					return
				}
				fmt.Fprint(w, `<!doctype html><html><body data-test-result="pending"><script>
window.htmx={process(){}};
if(!sessionStorage.getItem('reloaded')){
 localStorage.setItem('chat-model-owner','b');
 localStorage.setItem('chat-effort-owner',JSON.stringify({'b:gpt-6.1-sol':'medium'}));
 localStorage.setItem('chat-service-tier-owner',JSON.stringify({'b:gpt-6.1-sol':'priority'}));
}
</script>`+content.String()+`<div id="browser-result"></div><script>
(async()=>{try{
const assert=(v,m)=>{if(!v)throw Error(m)},trigger=document.getElementById('persist-form-agent-select'),form=trigger.closest('form');
const submitted=new Promise(resolve=>form.addEventListener('submit',event=>{event.preventDefault();resolve(new FormData(form));},{once:true}));
form.querySelector('[name=message]').value='send before settings finish';
form.requestSubmit();
const sent=await submitted;
await trigger._ovModelState.ready;
const task=!!form.dataset.conversationTaskId;
assert(!trigger._ovModelState.error,trigger._ovModelState.error);
assert(trigger.dataset.currentValue===(task?'a':'b'),'selection comes from task assignment or migrated Chat settings');
assert(sent.get('agent_id')===(task?'a':'b'),'Send waits for the restored model selection');
if(!sessionStorage.getItem('reloaded')){
 if(!task){assert(form.querySelector('[name=reasoning_effort]').value==='medium','legacy effort imported');assert(form.querySelector('[name=service_tier]').value==='priority','legacy Fast imported');}
 window.ovModelPicker.open(trigger);
 const panel=document.getElementById('conversation-model-picker'),slider=panel.querySelector('input[type=range]');
 slider.value='2';slider.dispatchEvent(new Event('input'));slider.dispatchEvent(new Event('change'));await trigger._ovModelState.pending;
 assert(!trigger._ovModelState.error,'save succeeded');
 localStorage.clear();sessionStorage.setItem('reloaded','1');location.reload();return;
}
assert(form.querySelector('[name=reasoning_effort]').value==='high','saved effort survives clearing storage and reloading');
assert(localStorage.getItem('chat-model-owner')===null,'does not recreate local preferences');
document.body.dataset.testResult='pass';
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;document.getElementById('browser-result').textContent=String(e);}})();
</script></body></html>`)
			}))
			defer srv.Close()
			runHeadlessChromeCDPFixture(t, chrome, srv.URL, "shared preference persistence "+scope, 1000, 800, 20*time.Second)
		})
	}
}

func TestBrowserFunctional_SharedChatDestinationModels(t *testing.T) {
	chrome := testChromePath(t)
	var content bytes.Buffer
	var mu sync.Mutex
	saved := "r-1234567890abcdef-model"
	writes := 0
	err := ChatInputForm(ChatInputFormConfig{FormID: "shared-chat-form", InputID: "message-input", TargetID: "messages", PostEndpoint: "/chat/send", Agents: []models.LLMConfig{{ID: "local-model", Name: "Local model", Provider: models.ProviderOpenAI}}, ShowModelSelector: true, ProjectChoices: []models.Project{{ID: "local", Name: "Local"}, {ID: "r-1234567890abcdef-project", Name: "Remote"}}, ProjectInputID: "destination", ProjectSendURL: "/chat/send?project_id="}).Render(context.Background(), &content)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/conversation/model-settings" {
			if r.URL.Query().Get("project_id") != "r-1234567890abcdef-project" {
				t.Errorf("wrong preferences destination: %s", r.URL.RawQuery)
			}
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodPost {
				writes++
				_ = r.ParseForm()
				saved = r.FormValue("agent_id")
			}
			selected := r.URL.Query().Get("agent_id")
			if selected == "" {
				selected = saved
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"agent_id": selected, "writes": writes})
			return
		}
		if r.URL.Path == "/tasks/new" {
			if r.URL.Query().Get("project_id") != "r-1234567890abcdef-project" {
				t.Errorf("unexpected destination %s", r.URL.RawQuery)
			}
			_ = ComposerModelOptions([]models.LLMConfig{{ID: "r-1234567890abcdef-model", Name: "Remote model", Provider: models.ProviderOpenAI}}, "").Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/ui/model-favorites" {
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `<!doctype html><html><body data-test-result="pending"><input id="destination"><div id="messages"></div><script>window.htmx={process:function(){}};</script>`+content.String()+`<script>
 (async function(){try {
 const wait=()=>new Promise(r=>setTimeout(r,30)),assert=(v,m)=>{if(!v)throw Error(m)};
 const destination=document.getElementById('destination'),form=document.getElementById('shared-chat-form');
 const originalFetch=window.fetch;
 let releaseModels,releasePrefs;
 const modelsGate=new Promise(r=>releaseModels=r),prefsGate=new Promise(r=>releasePrefs=r);
 window.fetch=async function(url,opts){
  if(String(url).startsWith('/tasks/new'))await modelsGate;
  if(String(url).startsWith('/conversation/model-settings') && !opts?.method)await prefsGate;
  return originalFetch(url,opts);
 };
 const send=form.querySelector('button[type="submit"]');
 function assertBlocked(stage){
  assert(send.disabled,stage+' disables Send');
  const event=new Event('submit',{bubbles:true,cancelable:true});form.dispatchEvent(event);
  assert(event.defaultPrevented,stage+' blocks keyboard/synthetic submission');
 }
 form.querySelector('[name="message"]').value='keep my draft';
 form.querySelector('[name="agent_id"]').value='local-model';
 destination.value='r-1234567890abcdef-project';destination.dispatchEvent(new Event('change',{bubbles:true}));
 const picker=document.getElementById('shared-chat-form-agent-select');
 assertBlocked('model list loading');
 releaseModels();
 for(let i=0;i<100&&!picker.parentElement.querySelector('[data-value="r-1234567890abcdef-model"]');i++)await wait();
 assert(picker.parentElement.querySelector('[data-value="r-1234567890abcdef-model"]'),'remote models loaded');
 assert(!picker.parentElement.querySelector('[data-value="local-model"]'),'local options removed');

 assertBlocked('preferences loading');
 releasePrefs();
 await picker._ovModelState.ready;
 await wait();
 assert(!send.disabled && !picker.disabled,'ready to send');
 assert(form.querySelector('[name="message"]').value==='keep my draft','draft preserved');
 assert(form.querySelector('[name="agent_id"]').value==='r-1234567890abcdef-model','saved remote choice restored');
 const before=await fetch('/conversation/model-settings?project_id=r-1234567890abcdef-project').then(r=>r.json());
 assert(before.writes===0,'switching destination must not save preferences');
 window._setChatCustomSelectValue(picker,'default');
 picker.dispatchEvent(new CustomEvent('chat-select-change',{detail:{value:'default'},bubbles:true}));
 await picker._ovModelState.ready;
 await picker._ovModelState.pending;
 const saved=await fetch('/conversation/model-settings?project_id=r-1234567890abcdef-project').then(r=>r.json());
 assert(saved.agent_id==='default' && saved.writes===1,'explicit model choice saved once');
 window.fetch=(url,opts)=>String(url).startsWith('/conversation/model-settings')
  ? Promise.resolve(new Response('unavailable',{status:503})) : originalFetch(url,opts);
 destination.dispatchEvent(new Event('change',{bubbles:true}));
 const retry=[...form.querySelectorAll('button')].find(b=>b.textContent.includes('Models unavailable — Retry'));
 for(let i=0;i<100&&retry.hidden;i++)await wait();
 assert(!retry.hidden,'failed preference load offers retry');
 assertBlocked('failed preferences');
 assert(form.querySelector('[name="message"]').value==='keep my draft','failed load preserves draft');
 window.fetch=originalFetch;
 retry.click();
 for(let i=0;i<100&&send.disabled;i++)await wait();
 assert(!send.disabled && retry.hidden,'successful retry enables Send');

 document.body.dataset.testResult='pass';
 }catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;}})();</script></body></html>`)
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "shared Chat destination", 1200, 900, 15*time.Second)
}

func TestBrowserFunctional_TaskDraftDoesNotSaveChatModel(t *testing.T) {
	chrome := testChromePath(t)
	var content bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{
		FormID: "task-thread-form", InputID: "task-message-input", TargetID: "messages", PostEndpoint: "/tasks",
		Agents:            []models.LLMConfig{{ID: "codex", Name: "Codex", Provider: models.ProviderOpenAI}},
		ShowModelSelector: true, SelectedAgentID: "default",
		ProjectChoices: []models.Project{{ID: "remote-project", Name: "Remote"}}, ProjectInputID: "new-task-project",
	}).Render(context.Background(), &content)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/conversation/model-settings" {
			t.Errorf("task draft must not access Chat preferences: %s %s", r.Method, r.URL)
			fmt.Fprint(w, `{"agent_id":"astra"}`)
			return
		}
		fmt.Fprint(w, `<!doctype html><html><body data-test-result="pending"><input id="new-task-project" value="remote-project"><div id="messages"></div><script>window.htmx={process:function(){}};</script>`+content.String()+`<script>
(async function(){try {
 const picker=document.getElementById('task-thread-form-agent-select');
 await picker._ovModelState.ready;
 window._setChatCustomSelectValue(picker,'codex');
 picker.dispatchEvent(new CustomEvent('chat-select-change',{detail:{value:'codex'},bubbles:true}));
 await picker._ovModelState.ready;
 await picker._ovModelState.pending;
 if(document.querySelector('#task-thread-form [name="agent_id"]').value!=='codex')throw Error('task selection lost');
 document.body.dataset.testResult='pass';
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack;}})();
</script></body></html>`)
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "task draft preference isolation", 1200, 900, 15*time.Second)
}
