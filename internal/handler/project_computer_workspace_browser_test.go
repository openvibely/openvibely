package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
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

	html = strings.Replace(html, "const root=document.getElementById('project-computer');", `
 window.addEventListener('live-transport-disconnected',event=>event.stopImmediatePropagation());
 window.computerStatusReads=0;const statusFetch=window.fetch;
 window.fetch=(url,options)=>{if(String(url).includes('/computer/status'))window.computerStatusReads++;return statusFetch(url,options)};
 window.computerSubscriptions=0;window.closedComputerSubscriptions=0;
 window.openVibelyLive.computer=(project,task,options,handlers)=>{
  window.computerSubscriptions++;let closed=false;
  const push=async()=>{const state=await (await statusFetch('/fixture-snapshot?task_id='+task)).json();if(!closed)handlers.listeners.computer_status({data:JSON.stringify({...state,project_id:project,task_id:task,viewer_mode:options.viewer_mode||''})});};
  if(task){window.computerLive=handlers;window.pushComputer=push;}else window.permissionLive=handlers;
  setTimeout(()=>{if(!closed){handlers.onopen?.();push();}},0);
  return {close(){if(!closed)window.closedComputerSubscriptions++;closed=true}};
 };
 const root=document.getElementById('project-computer');`, 1)
	// Count retry-controller attempts, not status subscriptions also opened by
	// startup and visibility events while the initial failure is settling.
	html = strings.Replace(html, "const screenRetry=window.openVibelyRetry(()=>refresh());", "window.screenRetryAttempts=[];const screenRetry=window.openVibelyRetry(()=>{window.screenRetryAttempts.push(performance.now());return refresh();});", 1)
	script := `<script>
window.fixtureErrors=[];window.addEventListener('error',e=>window.fixtureErrors.push(e.message));window.addEventListener('unhandledrejection',e=>window.fixtureErrors.push(String(e.reason)));
Object.defineProperty(navigator,'platform',{value:'MacIntel',configurable:true});
Object.defineProperty(navigator,'clipboard',{value:{writeText:async text=>{window.copiedRemote=text}},configurable:true});
(async()=>{
 const q=s=>document.querySelector(s);const wait=async (f,label="")=>{for(let n=0;n<320;n++){if(f())return;await new Promise(r=>setTimeout(r,25));}throw Error('Timed out '+label);};
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
 const outageStart=window.screenRetryAttempts.length;await new Promise(r=>setTimeout(r,7500));
 const outageAttempts=window.screenRetryAttempts.slice(outageStart);
 if(outageAttempts.length<2||outageAttempts.length>3)throw Error('Unavailable screen retries did not back off: '+JSON.stringify(outageAttempts));
 for(let i=1;i<outageAttempts.length;i++){if(outageAttempts[i]-outageAttempts[i-1]<1900*Math.pow(2,i-1))throw Error('Screen retry delay did not increase: '+JSON.stringify(outageAttempts));}
 if(q('[data-computer-status]'))throw Error('Obsolete status element remains');
 q('[data-computer-action="ensure"]').click();await wait(()=>startupErrors.length===2,'retry error toast');
 window.fetch=startupFetch;window.showToast=startupToast;
 q('[data-computer-action="ensure"]').click();
 await wait(()=>window.fixtureRFB?.connected);
 if(q('[data-computer-connection]').style.display==='none'||q('[data-retained-frame]'))throw Error('Initial connection must show loading on plain background');
 if(!q('[data-computer-connection]').textContent.includes('Connecting to computer'))throw Error('Missing initial connection status');
 await wait(()=>window.fixtureRFB?.painted);
 if(!q('#tab-computer').contains(q('[data-computer-preview]')))throw Error('Preview not in Computer tab');
 if(q('[data-computer-files]'))throw Error('Obsolete Files section remains');
 window.holdComputerFrame=true;const initialRFB=window.fixtureRFB;q('[data-tab="details"]').click();await new Promise(r=>setTimeout(r,100));if(!initialRFB.disconnected)throw Error('Hidden preview stayed connected');q('[data-tab="computer"]').click();await wait(()=>window.fixtureRFB!==initialRFB,'preview reconnect');
 if(q('#tab-computer').classList.contains('hidden'))throw Error('Computer tab hidden');
 await wait(()=>window.fixtureRFB.connected,'connected before first frame');
 const overlay=q('[data-computer-connection]');
 if(overlay.style.display==='none'||!q('[data-retained-frame]'))throw Error('Reconnect must retain thumbnail with overlay');
 if(!overlay.textContent.includes('Reconnecting to computer'))throw Error('Missing reconnect status');
 q('[data-computer-preview-open]').click();
 if(!q('[data-computer-dialog]').open||!q('[data-computer-expanded]').contains(overlay))throw Error('Overlay did not follow expansion');
 if(!window.fixtureRFB.viewOnly||q('[aria-label="Remote computer keyboard"]'))throw Error('Input enabled before live frame');
 await new Promise(r=>setTimeout(r,150));if(overlay.style.display==='none')throw Error('Handshake hid loading overlay before frame');
 // Flush close explicitly: virtual time can advance before Chrome delivers the native dialog event.
 q('[data-computer-dialog] [data-fullscreen-close]').click();if(!q('[data-computer-dialog]').open)q('[data-computer-dialog]').dispatchEvent(new Event('close'));await wait(()=>!q('[data-computer-dialog]').open&&q('[data-computer-preview]').contains(q('[data-computer-screen]')),'viewer closed');
 if(q('[data-computer-dialog]').open||!q('[data-computer-preview]').contains(overlay)||overlay.style.display==='none')throw Error('Collapse while reconnecting lost loading overlay');
 if(!q('[data-retained-frame]')||!window.fixtureRFB.viewOnly)throw Error('Collapse while reconnecting lost retained frame or enabled input');
 q('[data-computer-preview-open]').click();
 if(!q('[data-computer-dialog]').open||!q('[data-computer-expanded]').contains(overlay)||!window.fixtureRFB.viewOnly)throw Error('Reopening while reconnecting must preserve overlay and block input');
 window.holdComputerFrame=false;
 await wait(()=>overlay.style.display==='none'&&!window.fixtureRFB.viewOnly,'live frame enables input');
 // Flush close explicitly: virtual time can advance before Chrome delivers the native dialog event.
 q('[data-computer-dialog] [data-fullscreen-close]').click();if(!q('[data-computer-dialog]').open)q('[data-computer-dialog]').dispatchEvent(new Event('close'));await wait(()=>!q('[data-computer-dialog]').open&&q('[data-computer-preview]').contains(q('[data-computer-screen]')),'viewer closed');
 if(!window.fixtureRFB.viewOnly||overlay.style.display!=='none')throw Error('Live collapse changed connection readiness');

 if(q('#task-thread-form textarea')?.value)throw Error('Preview required a message');
 await wait(()=>window.fixtureRFB.painted && !q('[data-retained-frame]'),'mini preview settled');
 await new Promise(r=>setTimeout(r,250));const miniReads=window.computerStatusReads;
 await new Promise(r=>setTimeout(r,5500));if(window.computerStatusReads!==miniReads)throw Error('Mini preview still polls status');
 const expandedRFB=window.fixtureRFB;q('[data-computer-preview-open]').click();
 q('[data-computer-dialog]').dispatchEvent(new Event('close'));
 if(!q('[data-computer-expanded]').contains(q('[data-computer-screen]')))throw Error('Stale close event moved reopened viewer');
 if(overlay.style.display!=='none')throw Error('Live expansion showed loading overlay');
 if(expandedRFB.viewOnly)throw Error('Expansion waited for a server control acknowledgment');
 if(!q('[data-computer-screen]').contains(document.activeElement))throw Error('Expanded preview must initially focus the computer screen');
 await wait(()=>q('[data-computer-dialog]').open);
 if(q('[data-computer-action="takeover"], [data-computer-action="release"], [data-computer-expand]'))throw Error('Unexpected viewer buttons');
 await wait(()=>window.fixtureRFB && !window.fixtureRFB.viewOnly,'takeover');if(window.fixtureRFB!==expandedRFB||expandedRFB.disconnected)throw Error('Expansion replaced screen connection');


 await wait(()=>window.fixtureRFB.painted && !q('[data-retained-frame]'),'control first frame');
 await new Promise(r=>setTimeout(r,250));const controlReads=window.computerStatusReads;
 await new Promise(r=>setTimeout(r,5500));if(window.computerStatusReads!==controlReads)throw Error('Expanded preview still polls status');
 window.computerLive.listeners.computer_status({data:JSON.stringify({project_id:'other',task_id:'task'})});
 window.computerLive.listeners.computer_status({data:JSON.stringify({project_id:'project',task_id:'other'})});
 await new Promise(r=>setTimeout(r,100));if(window.computerStatusReads!==0)throw Error('Status was fetched over HTTP');
 window.computerLive.onopen();await window.pushComputer();
 const subscriptions=window.computerSubscriptions;
 window.computerLive.listeners.retry();await new Promise(r=>setTimeout(r,1200));
 if(window.computerSubscriptions!==subscriptions)throw Error('Preview duplicated transport subscription retry');
 window.computerLive.onopen();await window.pushComputer();
 await new Promise(r=>setTimeout(r,100));if(window.computerStatusReads!==0)throw Error('Retry fetched HTTP status');

 q('[data-teach-open]').click();await wait(()=>!q('[data-teach-stop]').hidden&&!q('[data-teach-stop]').disabled,'recording started');
 if(q('[data-teach-review]')||q('[data-teach-goal]'))throw Error('Old draft form remains');
 const teachBounds=q('[data-computer-teach]').getBoundingClientRect(),desktopBounds=q('[data-computer-expanded]').getBoundingClientRect();
 if(teachBounds.height>180||desktopBounds.height<innerHeight/2)throw Error('Recording displaced desktop');
 await wait(()=>q('[data-teach-timer]').textContent!=='0:00','recording timer');
 const ime=q('[aria-label="Remote computer keyboard"]');if(!ime)throw Error('No IME field');
 ime.value='private-value';ime.dispatchEvent(new Event('input',{bubbles:true}));
 ime.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));ime.value='你';ime.dispatchEvent(new Event('input',{bubbles:true}));ime.dispatchEvent(new CompositionEvent('compositionend',{bubbles:true}));await Promise.resolve();
 if(window.fixtureRFB.keys.filter(k=>k[0]===0x01004f60&&k[2]===true).length!==1)throw Error('Composed text not sent');
 ime.dispatchEvent(new KeyboardEvent('keydown',{key:'c',code:'KeyC',metaKey:true,bubbles:true,cancelable:true}));
 if(!window.fixtureRFB.keys.some(k=>k[0]===0xffe3&&k[2]===true))throw Error('Command did not become Control');
 const paste=new Event('paste',{bubbles:true,cancelable:true});Object.defineProperty(paste,'clipboardData',{value:{getData:()=> 'private-paste'}});ime.dispatchEvent(paste);
 if(window.fixtureRFB.pasted!=='private-paste')throw Error('Paste not bridged');
 if(q('[data-computer-clipboard]'))throw Error('Manual clipboard control remains');window.fixtureRFB.listeners.clipboard({detail:{text:'remote-copy'}});await wait(()=>window.copiedRemote==='remote-copy','clipboard');
 const recordingStatus=async()=>await(await fetch('/projects/project/computer/recording?task_id=task')).json();
 await new Promise(r=>setTimeout(r,1500));const first=await recordingStatus();if(first.image_count!==1)throw Error('Initial capture not persisted');
 await new Promise(r=>setTimeout(r,1400));if((await recordingStatus()).image_count!==1)throw Error('Unchanged screen captured twice');
 window.fixtureRFB.canvas.getContext('2d').fillStyle='white';window.fixtureRFB.canvas.getContext('2d').fillRect(0,0,300,150);
 await new Promise(r=>setTimeout(r,1500));if((await recordingStatus()).image_count!==2)throw Error('Changed screen not recorded');
 await fetch('/fixture-private');await window.pushComputer();await new Promise(r=>setTimeout(r,1500));
 if((await recordingStatus()).recording.state!=='stopped')throw Error('Private handoff did not stop recording');
 await fetch('/fixture-public');await window.pushComputer();q('[data-teach-open]').click();await wait(()=>!q('[data-teach-stop]').disabled&&!q('[data-teach-stop]').hidden);
 q('[data-teach-stop]').click();await wait(()=>!q('[data-computer-dialog]').open,'save minimizes desktop');
 // Deliver the dialog close event when running under Chrome virtual time.
 q('[data-computer-dialog]').dispatchEvent(new Event('close'));
 if((await recordingStatus()).recording.state!=='saved')throw Error('Save did not submit recording');
 await wait(()=>document.activeElement===q('#task-message-input'),'save returns focus to chat');
 const originalFetch=window.fetch;
 q('[data-computer-preview-open]').click();await wait(()=>!window.fixtureRFB.viewOnly);
 q('[data-teach-open]').click();await wait(()=>!q('[data-teach-stop]').disabled&&!q('[data-teach-stop]').hidden);
 q('[data-teach-discard]').click();await wait(()=>q('[data-computer-teach]').hidden,'discard');
 if((await recordingStatus()).recording.state!=='discarded')throw Error('Discard was not persisted');
 const dropped=window.fixtureRFB;dropped.disconnect();dropped.listeners.disconnect({});
 if(overlay.style.display==='none')throw Error('Disconnect did not show overlay immediately');
 const disconnectedFrame=q('[data-retained-frame]');
 if(!disconnectedFrame||!disconnectedFrame.getContext('2d').getImageData(1,1,1,1).data[3])throw Error('Unexpected disconnect lost the last painted frame');
 await wait(()=>overlay.textContent.includes('Retrying in 1 second'),'retry countdown');
 await wait(()=>window.fixtureRFB!==dropped&&window.fixtureRFB.painted,'reconnected');
 Object.defineProperty(document,'hidden',{value:true,configurable:true});document.dispatchEvent(new Event('visibilitychange'));const hiddenRFB=window.fixtureRFB;
 if(!hiddenRFB.viewOnly)throw Error('Hidden viewer kept input enabled');
 await window.pushComputer();await new Promise(r=>setTimeout(r,250));
 if(hiddenRFB.disconnected)throw Error('Brief app switch disconnected viewer');
 Object.defineProperty(document,'hidden',{value:false,configurable:true});document.dispatchEvent(new Event('visibilitychange'));
 await wait(()=>!hiddenRFB.viewOnly,'input restored after app switch');
 if(window.fixtureRFB!==hiddenRFB)throw Error('Brief app switch replaced viewer');
 Object.defineProperty(document,'hidden',{value:true,configurable:true});document.dispatchEvent(new Event('visibilitychange'));
 await new Promise(r=>setTimeout(r,600100));
 if(!hiddenRFB.disconnected)throw Error('Hidden page kept streaming past grace period');
 Object.defineProperty(document,'hidden',{value:false,configurable:true});document.dispatchEvent(new Event('visibilitychange'));await wait(()=>window.fixtureRFB!==hiddenRFB&&window.fixtureRFB.painted,'visible reconnect');
 // Painting precedes the viewer's frame check; capture requires live input,
 // after the retained frame is removed and control is restored.
 await wait(()=>!window.fixtureRFB.viewOnly&&!q('[data-retained-frame]')&&q('[data-computer-screen]').getAttribute('aria-busy')==='false','visible viewer ready for capture');
 const closingRFB=window.fixtureRFB;q('[data-computer-dialog] [data-fullscreen-close]').click();q('[data-computer-dialog]').dispatchEvent(new Event('close'));
 await wait(()=>!q('[data-computer-dialog]').open&&q('[data-computer-preview]').contains(q('[data-computer-screen]')),'viewer closed');
 if(window.fixtureRFB!==closingRFB||closingRFB.disconnected||!closingRFB.canvas.isConnected)throw Error('Closing viewer replaced screen connection');if(!window.fixtureRFB.viewOnly)throw Error('Collapse did not disable input');

 await wait(()=>window.fixtureRFB.painted && !q('[data-retained-frame]'),'preview first frame');

 await wait(()=>!q('[data-task-computer-toggle]').disabled);q('[data-task-computer-toggle]').click();await wait(()=>q('[data-computer-preview]').hidden,'task disabled');
 window.permissionLive.listeners.computer_status({data:JSON.stringify({project_id:'project',task_id:'',enabled:false})});
 await wait(()=>q('[data-tab="computer"]').hidden,'project permission hides tab');
 if(q('[data-tab="details"]').getAttribute('aria-selected')!=='true')throw Error('Disabled computer tab did not return to Details');
 window.taskWorkspace.openPanel('computer');
 if(!q('#tab-computer').classList.contains('hidden'))throw Error('Disabled tab could be reopened');
 window.fetch=originalFetch;
 if(takeoverCalls)throw Error('Fullscreen interrupted the task through takeover/release');
 window.permissionLive.listeners.computer_status({data:JSON.stringify({project_id:'project',task_id:'',enabled:true})});
 window.taskWorkspace.openPanel('computer');
 await wait(()=>!q('[data-task-computer-toggle]').disabled);q('[data-task-computer-toggle]').click();
 await wait(()=>window.fixtureRFB.painted&&!window.fixtureRFB.disconnected,'stream before cleanup');
 const liveBeforeCleanup=window.fixtureRFB;
 htmx.trigger(q('[data-teach-timer]'),'htmx:beforeCleanupElement');
 if(liveBeforeCleanup.disconnected)throw Error('Child cleanup disconnected preview');
 const closedBeforeCleanup=window.closedComputerSubscriptions;
 htmx.swap(q('#task-detail-content'),'<div id="cleaned-task">Task removed</div>',{swapStyle:'outerHTML',settleDelay:0});
 if(!liveBeforeCleanup.disconnected)throw Error('Task cleanup left screen connected');
 if(window.closedComputerSubscriptions<=closedBeforeCleanup)throw Error('Task cleanup left status subscription open');
 const subscriptionsAfterCleanup=window.computerSubscriptions;
 document.dispatchEvent(new Event('visibilitychange'));document.dispatchEvent(new Event('computer-tab-change'));
 await new Promise(resolve=>setTimeout(resolve,100));
 if(window.computerSubscriptions!==subscriptionsAfterCleanup)throw Error('Removed preview reopened subscription');

 finish('pass','Saved task preview, automatic startup, expansion, takeover, release and project toggle');
 }catch(e){finish('fail',(e.stack||e.message)+'; errors='+JSON.stringify(window.fixtureErrors)+'; dialog='+q('[data-computer-dialog]')?.open+'; preview='+q('[data-computer-preview]')?.contains(q('[data-computer-screen]')));}
})();</script>`
	html = strings.Replace(html, "</body>", script+"</body>", 1)
	var mu sync.Mutex
	enabled, ready, held := false, false, false
	starts := 0

	fixtureRecording := computerTeachRecording{State: "idle"}
	saveCount := 0
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
			fmt.Fprint(w, `export default class RFB{constructor(el,url){if(!String(url).includes('/stream/control'))throw Error('Expected one authorized control connection');this.keys=[];this.listeners={};this.el=el;this.canvas=document.createElement('canvas');el.append(this.canvas);window.fixtureRFB=this;setTimeout(()=>{this.connected=true;this.listeners.connect?.({})},10);const paint=()=>{if(window.holdComputerFrame){this.timer=setTimeout(paint,25);return;}this.canvas.getContext('2d').fillRect(0,0,300,150);this.painted=true;};this.timer=setTimeout(paint,350);}addEventListener(name,fn){this.listeners[name]=fn;}sendKey(...args){this.keys.push(args)}clipboardPasteFrom(text){this.pasted=text}disconnect(){this.disconnected=true;clearTimeout(this.timer);this.canvas.remove();}}`)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/thread"):
			w.Header().Set("Content-Type", "text/html")
			require.NoError(t, components.TaskThreadView(&models.Task{ID: "task", ProjectID: "project", Title: "Preview", Category: models.CategoryActive, Status: models.StatusPending}, nil, nil, nil, nil, nil, false, 50).Render(r.Context(), w))
		case strings.HasSuffix(r.URL.Path, "/idle-timeout"):

			fmt.Fprint(w, `{}`)
		case r.URL.Path == "/fixture-snapshot":
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
		case strings.HasSuffix(r.URL.Path, "/recording"):
			if r.Method == "POST" {
				var input struct {
					Action, ID, RequestID string
					Revision              int
					Steps, Images         []string
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				switch input.Action {
				case "start":
					if fixtureRecording.State != "stopped" && fixtureRecording.State != "recording" {
						fixtureRecording = computerTeachRecording{ID: "fixture", StartedAt: time.Now(), Deadline: time.Now().Add(10 * time.Minute)}
					}
					fixtureRecording.State = "recording"
				case "append":
					fixtureRecording.Steps = append(fixtureRecording.Steps, input.Steps...)
					fixtureRecording.Images = append(fixtureRecording.Images, input.Images...)
					fixtureRecording.Revision++
				case "pause":
					fixtureRecording.State = "stopped"
				case "discard":
					fixtureRecording.State = "discarded"
				case "save":
					saveCount++
					demonstration.Steps = fixtureRecording.Steps
					demonstration.Images = fixtureRecording.Images
					fixtureRecording.State = "saved"
				}
			}
			copy := fixtureRecording
			copy.Steps = nil
			copy.Images = nil
			_ = json.NewEncoder(w).Encode(map[string]any{"recording": copy, "step_count": len(fixtureRecording.Steps), "image_count": len(fixtureRecording.Images)})
		case strings.HasSuffix(r.URL.Path, "/ensure"):
			starts++
			ready = true
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
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, chrome, "--headless=new", "--disable-gpu", "--no-sandbox", "--disable-dev-shm-usage", "--user-data-dir="+t.TempDir(), "--virtual-time-budget=660000", "--dump-dom", server.URL)
	out, err := runHandlerBrowserProcess(t, cmd)
	require.NoError(t, err)
	if !bytes.Contains(out, []byte(`id="browser-result" data-status="pass"`)) {
		t.Fatalf("workspace: %s", out[max(0, len(out)-2500):])
	}
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, starts) // Initial opt-in, then re-enable for cleanup coverage.

	require.True(t, enabled) // Removing the preview must not disable the computer.
	require.Equal(t, 1, saveCount)
	require.GreaterOrEqual(t, len(demonstration.Images), 2)
	require.NotContains(t, strings.Join(demonstration.Steps, " "), "private-value")
	require.NotContains(t, strings.Join(demonstration.Steps, " "), "private-paste")
	require.Contains(t, strings.Join(demonstration.Steps, " "), "Capture:")
}
