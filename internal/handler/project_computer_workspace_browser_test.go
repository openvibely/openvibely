package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSavedTaskComputerTabStartsBeforeFirstMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome unavailable")
	}
	var page bytes.Buffer
	require.NoError(t, pages.TaskDetailPage(nil, &models.Task{ID: "task", ProjectID: "project", Title: "Preview", Category: models.CategoryActive, Status: models.StatusPending}, nil, nil, nil, nil, nil, nil, "computer", nil).Render(t.Context(), &page))
	html := strings.ReplaceAll(page.String(), static.URL("vendor/novnc/core/rfb.js"), "/fixture-rfb.js")
	script := `<script>
Object.defineProperty(navigator,'platform',{value:'MacIntel',configurable:true});
Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copiedRemote=text}},configurable:true});
(async()=>{
 const q=s=>document.querySelector(s);const wait=async (f,label="")=>{for(let n=0;n<200;n++){if(f())return;await new Promise(r=>setTimeout(r,25));}throw Error('Timed out '+label);};
 const finish=(state,message)=>{const node=document.createElement('div');node.id='browser-result';node.dataset.status=state;node.textContent=message;document.body.append(node);};
 try{
 await wait(()=>window.taskWorkspace && q('[data-tab="computer"]').hasAttribute('data-computer-allowed'));q('[data-tab="computer"]').click();
 await wait(()=>!q('[data-task-computer-toggle]').disabled);
 if(window.fixtureRFB)throw Error('Computer started without opt-in');
 let takeoverCalls=0;const viewerFetch=window.fetch;window.fetch=(url,options)=>{if(/\/(takeover|release)(?:\?|$)/.test(String(url)))takeoverCalls++;return viewerFetch(url,options);};const startupFetch=window.fetch,startupToast=window.showToast;const startupErrors=[];
 window.showToast=(message,type)=>startupErrors.push({message,type});
 window.fetch=(url,options)=>String(url).includes('/ensure')?Promise.resolve(new Response(JSON.stringify({message:'Docker unavailable'}),{status:502,headers:{'Content-Type':'application/json'}})):startupFetch(url,options);
 q('[data-task-computer-toggle]').click();
 await wait(()=>startupErrors.length===1,'startup error toast');
 if(startupErrors[0].message!=='Docker unavailable'||startupErrors[0].type!=='error')throw Error('Missing startup failure toast');
 if(q('[data-computer-status]'))throw Error('Obsolete status element remains');
 q('[data-computer-action="ensure"]').click();await wait(()=>startupErrors.length===2,'retry error toast');
 window.fetch=startupFetch;window.showToast=startupToast;
 q('[data-computer-action="ensure"]').click();
 await wait(()=>window.fixtureRFB?.painted);
 if(!q('#tab-computer').contains(q('[data-computer-preview]')))throw Error('Preview not in Computer tab');
 if(q('[data-computer-files]'))throw Error('Obsolete Files section remains');
 const initialRFB=window.fixtureRFB;q('[data-tab="details"]').click();await new Promise(r=>setTimeout(r,100));if(!initialRFB.disconnected)throw Error('Hidden preview stayed connected');q('[data-tab="computer"]').click();await wait(()=>window.fixtureRFB!==initialRFB,'preview reconnect');
 if(q('#tab-computer').classList.contains('hidden'))throw Error('Computer tab hidden');
 if(q('#task-thread-form textarea')?.value)throw Error('Preview required a message');
 q('[data-computer-preview-open]').click();await wait(()=>q('[data-computer-dialog]').open);
 if(q('[data-computer-action="takeover"], [data-computer-action="release"], [data-computer-expand]'))throw Error('Unexpected viewer buttons');
 await wait(()=>window.fixtureRFB && !window.fixtureRFB.viewOnly,'takeover');


 await wait(()=>window.fixtureRFB.painted && !q('[data-retained-frame]'),'control first frame');

 q('[data-teach-open]').click();q('[data-teach-goal]').value='Download a report';q('[data-teach-record]').click();
 const ime=q('[aria-label="Remote computer keyboard"]');if(!ime)throw Error('No IME field');
 ime.value='private-value';ime.dispatchEvent(new Event('input',{bubbles:true}));
 ime.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));ime.value='你';ime.dispatchEvent(new Event('input',{bubbles:true}));ime.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true}));await Promise.resolve();
 if(window.fixtureRFB.keys.filter(k=>k[0]===0x01004f60&&k[2]===true).length!==1)throw Error('Composed text not sent');
 ime.dispatchEvent(new KeyboardEvent('keydown',{key:'c',code:'KeyC',metaKey:true,bubbles:true,cancelable:true}));
 if(!window.fixtureRFB.keys.some(k=>k[0]===0xffe3&&k[2]===true))throw Error('Command did not become Control');
 const paste=new Event('paste',{bubbles:true,cancelable:true});Object.defineProperty(paste,'clipboardData',{value:{getData:()=> 'private-paste'}});ime.dispatchEvent(paste);
 if(window.fixtureRFB.pasted!=='private-paste')throw Error('Paste not bridged');
 if(q('[data-computer-clipboard]'))throw Error('Manual clipboard control remains');window.fixtureRFB.listeners.clipboard({detail:{text:'remote-copy'}});await wait(()=>window.copiedRemote==='remote-copy','clipboard');
 q('[data-teach-note]').value='Choose Reports';q('[data-teach-capture]').click();if(!q('[data-teach-images] img'))throw Error('Capture missing');
 if(q('[data-teach-steps]').textContent.includes('private-value'))throw Error('Typed value recorded');
 q('[data-teach-auto]').checked=true;
 await wait(()=>q('[data-teach-images]').children.length===2,'automatic initial capture');
 await new Promise(r=>setTimeout(r,1400));if(q('[data-teach-images]').children.length!==2)throw Error('Unchanged screen captured twice');
 window.fixtureRFB.canvas.getContext('2d').fillStyle='white';window.fixtureRFB.canvas.getContext('2d').fillRect(0,0,300,150);
 await wait(()=>q('[data-teach-images]').children.length===3,'changed screen capture');
 q('[data-teach-record]').click();window.fixtureRFB.canvas.getContext('2d').fillStyle='red';window.fixtureRFB.canvas.getContext('2d').fillRect(0,0,300,150);
 await new Promise(r=>setTimeout(r,1400));if(q('[data-teach-images]').children.length!==3)throw Error('Captured while paused');
 q('[data-teach-record]').click();await fetch('/fixture-private');document.dispatchEvent(new Event('computer-tab-change'));
 await wait(()=>q('[data-teach-record]').textContent==='Record steps','private recording pause');
 const privateCount=q('[data-teach-images]').children.length;window.fixtureRFB.canvas.getContext('2d').fillStyle='blue';window.fixtureRFB.canvas.getContext('2d').fillRect(0,0,300,150);
 await new Promise(r=>setTimeout(r,1400));if(q('[data-teach-images]').children.length!==privateCount)throw Error('Captured private handoff');
 await fetch('/fixture-public');document.dispatchEvent(new Event('computer-tab-change'));await wait(()=>!q('[data-teach-open]').hidden);q('[data-teach-open]').click();
 q('[data-teach-save]').click();await wait(()=>!q('[data-teach-draft]').hidden,'generated workflow');
 if(q('[data-workflow-name]').value!=='Reusable report')throw Error('Generated draft missing');
 q('[data-workflow-instructions]').value='Reviewed report instructions';q('[data-workflow-save]').click();await wait(()=>q('[data-teach-status]').textContent.includes('Reusable workflow saved'),'saved workflow');
 if(!q('[data-teach-output]').textContent.includes('Generated'))throw Error('No streamed generation feedback');
 await wait(()=>q('[data-workflow-list] button'));[...q('[data-workflow-list]').querySelectorAll('button')].find(b=>b.textContent==='Edit workflow').click();if(q('[data-teach-draft]').hidden)throw Error('Saved workflow cannot be edited');q('[data-workflow-save]').click();await wait(()=>q('[data-teach-draft]').hidden,'saved edit');
 await wait(()=>q('[data-workflow-list] button'));[...q('[data-workflow-list]').querySelectorAll('button')].find(b=>b.textContent==='Create task from workflow').click();await wait(()=>q('[data-teach-status] a'),'reuse workflow');
 const originalFetch=window.fetch;let cancelledGeneration=false;window.fetch=(url,options)=>{if(String(url).includes('/teach')&&options?.signal){return new Promise((resolve,reject)=>{options.signal.addEventListener('abort',()=>{cancelledGeneration=true;reject(new DOMException('Cancelled','AbortError'));});});}return originalFetch(url,options);};
 q('[data-teach-save]').click();await wait(()=>q('[data-teach-save]').disabled);q('[data-teach-discard]').click();await wait(()=>cancelledGeneration&&!q('[data-teach-save]').disabled,'discard cancellation');window.fetch=originalFetch;
 const dropped=window.fixtureRFB;dropped.disconnect();dropped.listeners.disconnect({});
 document.dispatchEvent(new Event('computer-tab-change'));await wait(()=>window.fixtureRFB!==dropped&&window.fixtureRFB.painted,'reconnected');
 Object.defineProperty(document,'hidden',{value:true,configurable:true});document.dispatchEvent(new Event('visibilitychange'));const hiddenRFB=window.fixtureRFB;if(!hiddenRFB.disconnected)throw Error('Hidden page kept streaming');
 Object.defineProperty(document,'hidden',{value:false,configurable:true});document.dispatchEvent(new Event('visibilitychange'));await wait(()=>window.fixtureRFB!==hiddenRFB&&window.fixtureRFB.painted,'visible reconnect');
 cancelledGeneration=false;window.fetch=(url,options)=>{if(String(url).includes('/teach')&&options?.signal){return new Promise((resolve,reject)=>{options.signal.addEventListener('abort',()=>{cancelledGeneration=true;reject(new DOMException('Cancelled','AbortError'));});});}return originalFetch(url,options);};q('[data-teach-note]').value='New capture';q('[data-teach-capture]').click();q('[data-teach-save]').click();await wait(()=>q('[data-teach-save]').disabled,'second generation');
 q('[data-computer-close]').click();if(!cancelledGeneration)throw Error('Closing viewer did not cancel generation');window.fetch=originalFetch;if(!q('[data-retained-frame]'))throw Error('Desktop disappeared immediately on close');if(q('[data-computer-dialog]').open)throw Error('Did not close');await wait(()=>window.fixtureRFB.viewOnly,'handback');

 await wait(()=>window.fixtureRFB.painted && !q('[data-retained-frame]'),'preview first frame');

 await wait(()=>!q('[data-task-computer-toggle]').disabled);q('[data-task-computer-toggle]').click();await wait(()=>q('[data-computer-preview]').hidden,'task disabled');
 window.fetch=(url,options)=>String(url).endsWith('/computer/status')?Promise.resolve(new Response(JSON.stringify({enabled:false}),{status:200})):originalFetch(url,options);
 document.dispatchEvent(new Event('computer-setting-changed'));
 await wait(()=>q('[data-tab="computer"]').hidden,'project permission hides tab');
 if(q('[data-tab="details"]').getAttribute('aria-selected')!=='true')throw Error('Disabled computer tab did not return to Details');
 window.taskWorkspace.openPanel('computer');
 if(!q('#tab-computer').classList.contains('hidden'))throw Error('Disabled tab could be reopened');
 window.fetch=originalFetch;
 if(takeoverCalls)throw Error('Fullscreen interrupted the task through takeover/release');
 finish('pass','Saved task preview, automatic startup, expansion, takeover, release and project toggle');
 }catch(e){finish('fail',e.message);}
})();</script>`
	html = strings.Replace(html, "</body>", script+"</body>", 1)
	var mu sync.Mutex
	enabled, ready, held := false, false, false
	starts := 0

	savedWorkflow := false
	handoff := ""
	var demonstration struct {
		Goal   string   `json:"goal"`
		Steps  []string `json:"steps"`
		Images []string `json:"images"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, html)
			return
		}
		if r.URL.Path == "/fixture-rfb.js" {
			w.Header().Set("Content-Type", "text/javascript")
			fmt.Fprint(w, `export default class RFB{constructor(el){this.keys=[];this.listeners={};this.el=el;this.canvas=document.createElement('canvas');el.append(this.canvas);window.fixtureRFB=this;setTimeout(()=>this.listeners.connect?.({}),10);this.timer=setTimeout(()=>{this.canvas.getContext('2d').fillRect(0,0,300,150);this.painted=true;},350);}addEventListener(name,fn){this.listeners[name]=fn;}sendKey(...args){this.keys.push(args)}clipboardPasteFrom(text){this.pasted=text}disconnect(){this.disconnected=true;clearTimeout(this.timer);this.canvas.remove();}}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/idle-timeout"):

			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/status"):
			if r.URL.Query().Get("task_id") == "" {
				fmt.Fprint(w, `{"enabled":true}`)
				return
			}
			fmt.Fprintf(w, `{"project_enabled":true,"task_enabled":%t,"enabled":%t,"ready":%t,"held":%t,"handoff_kind":%q}`, enabled, enabled, ready, held, handoff)
		case r.URL.Path == "/fixture-private":
			handoff = "request_browser_login"
			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/fixture-public":
			handoff = ""
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/teach"):
			_ = json.NewDecoder(r.Body).Decode(&demonstration)
			fmt.Fprint(w, "{\"text\":\"Generated\"}\n{\"draft\":{\"name\":\"Reusable report\",\"instructions\":\"Generated instructions\"}}\n")
		case strings.HasSuffix(r.URL.Path, "/workflows"):
			if r.Method == "GET" {
				if savedWorkflow {
					fmt.Fprint(w, `[{"id":"workflow","name":"Reusable report","instructions":"Reviewed report instructions"}]`)
				} else {
					fmt.Fprint(w, `[]`)
				}
				return
			}
			var input map[string]string
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["action"] == "save" {
				if savedWorkflow {
					require.Equal(t, "workflow", input["id"])
				}
				savedWorkflow = true
				require.Equal(t, "Reviewed report instructions", input["instructions"])
				fmt.Fprint(w, `{"id":"workflow"}`)
			} else {
				fmt.Fprint(w, `{"url":"/tasks/demonstration","task_id":"demonstration"}`)
			}
		case strings.HasSuffix(r.URL.Path, "/ensure"):
			starts++
			ready = true
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/takeover"):
			held = true
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/release"):
			held = false
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/task-enable"):
			enabled = true
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/task-disable"):
			enabled = false
			ready = false
			fmt.Fprint(w, `{}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=25000", "--dump-dom", server.URL)
	out, err := runHandlerBrowserProcess(t, cmd)
	require.NoError(t, err)
	if !bytes.Contains(out, []byte(`id="browser-result" data-status="pass"`)) {
		t.Fatalf("workspace: %s", out[max(0, len(out)-2500):])
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, starts)

	require.False(t, enabled)
	require.Equal(t, "Download a report", demonstration.Goal)
	require.Len(t, demonstration.Images, 3)
	require.NotContains(t, strings.Join(demonstration.Steps, " "), "private-value")
	require.NotContains(t, strings.Join(demonstration.Steps, " "), "private-paste")
	require.Contains(t, strings.Join(demonstration.Steps, " "), "Choose Reports")
}
