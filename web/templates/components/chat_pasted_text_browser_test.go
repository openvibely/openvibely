package components

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserFunctional_ComposerPastedText(t *testing.T) {
	chrome := testChromePath(t)
	var markup bytes.Buffer
	err := ChatInputForm(ChatInputFormConfig{FormID: "paste-form", InputID: "paste-input", PostEndpoint: "/send", SteerEndpoint: "/steer", TargetID: "messages"}).Render(context.Background(), &markup)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx2011)
		case "/composer":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, markup.String())
		case "/controls":
			w.Header().Set("Content-Type", "text/html")
			if err := ChatQueuedInputRow("audit", "Queued message", "/queue-steer", false).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/thread-inputs/audit/cancel", "/queue-steer":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<span data-control-complete="`+html.EscapeString(r.URL.Path)+`"></span>`)
		case "/send", "/steer":
			r.ParseForm()
			if r.FormValue("message") == "fail" {
				w.WriteHeader(500)
				return
			}
			// Echo through textContent in the fixture to verify exact transmitted data.
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, html.EscapeString(r.FormValue("message")))
		default:
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!doctype html><html><body data-test-result="pending"><script src="/htmx.js"></script><div id="messages"></div><div id="pending-thread-inputs"></div><div id="composer-fixture">%s</div><div id="browser-result"></div><script>
(async function() {
 let form=document.getElementById('paste-form'), input=document.getElementById('paste-input');
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 const wait=()=>new Promise(resolve=>setTimeout(resolve,180));
 const cards=()=>form.querySelectorAll('[aria-label="Edit pasted text"]');
 const originalSend=XMLHttpRequest.prototype.send;
 let holdSend=false, releaseSend;
 XMLHttpRequest.prototype.send=function(body) {
  if (!holdSend) return originalSend.call(this,body);
  holdSend=false; const xhr=this; releaseSend=()=>originalSend.call(xhr,body);
 };
 function assertDraftCleared() {assert(cards().length===0 && input.value==='', 'accepted draft not cleared');}

 function paste(text) {const data=new DataTransfer();data.setData('text/plain',text);const event=new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true});input.dispatchEvent(event);return event.defaultPrevented;}
 function button(label){return Array.from(document.querySelectorAll('dialog button')).find(b=>b.textContent===label);}
 function send(){input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));}
 const large='hello <script> literal & text\n'.repeat(250);
 assert(!paste('short text'),'short paste intercepted');
 // Model a plain HTTP browser: randomUUID is absent, getRandomValues still works.
 const nativeUUID=crypto.randomUUID, nativeRandomValues=crypto.getRandomValues;
 try {
  crypto.randomUUID=undefined;
  let randomCalls=0;
  crypto.getRandomValues=function(bytes) {randomCalls++;return nativeRandomValues.call(this,bytes)};
  input.value='before SELECTED after';input.setSelectionRange(7,15);
  assert(paste(large),'UUID fallback did not create a card');
  assert(input.value==='before  after','fallback paste changed surrounding text');
  assert(form._captureChatSubmission().pastedTextSnapshot[0].text===large,'fallback lost pasted content');
  input.setSelectionRange(input.value.length,input.value.length);
  assert(paste(large),'second fallback paste failed');
  const ids=form._captureChatSubmission().pastedTextSnapshot.map(item=>item.id);
  assert(randomCalls===2 && ids.length===2 && ids[0]!==ids[1],'fallback IDs are not distinct');
  assert(ids.every(id=>/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(id)),'fallback did not produce UUID v4');
  form.querySelectorAll('[aria-label="Remove pasted text"]').forEach(button=>button.click());
  // Synthetic paste has no browser default insertion; assert that we leave it allowed.
  for (const unavailable of [undefined,function(){throw Error('random generation failed')}]) {
   crypto.getRandomValues=unavailable;
   input.value='keep SELECTED text';input.setSelectionRange(5,13);
   assert(!paste(large),'failed card preparation suppressed normal paste');
   assert(input.value==='keep SELECTED text' && input.selectionStart===5 && input.selectionEnd===13,'failed card preparation deleted the selection');
   assert(cards().length===0,'failed preparation added a card');
  }
 } finally {crypto.randomUUID=nativeUUID;crypto.getRandomValues=nativeRandomValues;}
 input.value='Explain this'; input.setSelectionRange(12,12);
 assert(paste(large),'large paste not intercepted');
 assert(cards().length===1 && input.value==='Explain this','card must keep composer compact');
 assert(form._captureChatSubmission().message==='Explain this\n\n'+large,'full paste missing');
 cards()[0].click(); document.querySelector('dialog textarea').value='discard';button('Cancel').click();await wait();
 assert(form._captureChatSubmission().message.endsWith(large),'cancel changed paste');
 cards()[0].click();document.querySelector('dialog textarea').value='edited text';button('Save').click();await wait();
 assert(form._captureChatSubmission().message==='Explain this\n\nedited text','edit not saved');
 holdSend=true;send();
 assert(cards().length===1 && input.value==='Explain this','draft cleared before acceptance');
 releaseSend();await wait();
 assert(document.getElementById('messages').textContent==='Explain this\n\nedited text','send lost content');
 assert(cards().length===0 && input.value==='','accepted draft not cleared');
 paste(large); cards()[0].click();document.querySelector('dialog textarea').value='fail';button('Save').click();await wait();holdSend=true;send();assert(cards().length===1,'pending send removed card');releaseSend();await wait();
 assert(cards().length===1 && form._captureChatSubmission().message==='fail','failed send lost card');
 assert(!cards()[0].disabled,'failed send did not unlock editor');
 cards()[0].click();assert(document.querySelector('dialog[open]'),'editor unavailable after failure');button('Cancel').click();

 form.querySelector('[aria-label="Remove pasted text"]').click();assert(cards().length===0,'remove failed');
 paste(large);send();await wait();assert(cards().length===0,'card-only send failed');
 assert(document.getElementById('messages').textContent.endsWith(large),'card-only text changed');
 const primary=document.getElementById('paste-form-primary-action');
 primary.setAttribute('data-composer-running','true');primary.setAttribute('data-active-turn-id','turn-1');
 paste(large);paste('second paste '.repeat(500));
 const expected=form._captureChatSubmission().message;
 assert(cards().length===2,'multiple pastes must have separate cards');
 holdSend=true;input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,metaKey:true,bubbles:true,cancelable:true}));assert(cards().length===2,'steer cleared cards before acceptance');releaseSend();await wait();
 assert(document.getElementById('pending-thread-inputs').textContent===expected,'steering lost or duplicated pasted text');
 assert(cards().length===0,'accepted steering left cards');
 // Send A, navigate back into a fresh composer, add B, then accept A.
 async function remount() {
  await htmx.ajax('GET','/composer',{target:'#composer-fixture',swap:'innerHTML'});
  await wait();
  form=document.getElementById('paste-form');input=document.getElementById('paste-input');
 }
 paste(large);holdSend=true;send();
 const releaseA=releaseSend;
 await remount();
 assert(cards().length===1,'pending card not retained across navigation');
 const newer='new card B '.repeat(500);
 paste(newer);input.value='new instructions';input.dispatchEvent(new Event('input',{bubbles:true}));
 assert(cards().length===2,'replacement draft missing cards');
 releaseA();await wait();
 assert(cards().length===1,'old send did not reconcile the live replacement');
 assert(form._captureChatSubmission().message==='new instructions\n\n'+newer,'old send erased newer text or cards');
 await remount();
 assert(cards().length===1 && form._captureChatSubmission().message===newer,'new card lost on next remount');
 // The same cleanup also removes submitted cards when a new card is added without navigation.
 holdSend=true;send();const releaseB=releaseSend;paste(large);
 releaseB();await wait();
 assert(cards().length===1 && form._captureChatSubmission().message===large,'same-composer send cleared new card or retained sent card');
 await remount();
 assert(cards().length===1 && !cards()[0].disabled,'completed send left a stale lock');
 // Editing an already submitted card preserves the new version on acceptance.
 holdSend=true;send();const releaseEdited=releaseSend;
 cards()[0].click();document.querySelector('dialog textarea').value='new edit';button('Save').click();
 releaseEdited();await wait();
 assert(form._captureChatSubmission().message==='new edit','acceptance erased an edited card');
 // An editor already open at acceptance keeps a later Save as a new draft.
 holdSend=true;send();const releaseOpen=releaseSend;
 cards()[0].click();document.querySelector('dialog textarea').value='edit after acceptance';
 releaseOpen();await wait();button('Save').click();
 assert(form._captureChatSubmission().message==='edit after acceptance','open editor lost its changes');
 form.querySelector('[aria-label="Remove pasted text"]').click();paste(large);
 const pasteKey=form.getAttribute('data-attachment-state-key')+'-pasted-text';
 delete window._ovPasteDrafts[pasteKey];await remount();
 assert(form._captureChatSubmission().message===large,'card not persisted');

 // A destination-project guard can cancel and retry without losing the draft.
 let needsProject=true;
 const guard=event=>{if(event.detail.elt===form && needsProject) event.preventDefault();};
 form.addEventListener('htmx:configRequest',guard);
 input.value='project instructions';send();await wait();
 assert(form._captureChatSubmission().message==='project instructions\n\n'+large,'project guard cleared the draft');
 needsProject=false;send();await wait();
 assert(document.getElementById('messages').textContent.endsWith('project instructions\n\n'+large),'project retry lost message');
 assertDraftCleared();form.removeEventListener('htmx:configRequest',guard);
 paste(large);
 // Real queued-message controls remain usable after a composer send.
 const controls=document.createElement('div');controls.id='audit-controls';form.append(controls);
 for(const [label,path] of [['Cancel queued follow-up','/thread-inputs/audit/cancel'],['Convert queued follow-up to steering','/queue-steer']]) {
  await htmx.ajax('GET','/controls',{target:'#audit-controls',swap:'innerHTML'});await wait();
  controls.querySelector('[aria-label="'+label+'"]').click();await wait();
  assert(controls.querySelector('[data-control-complete]')?.dataset.controlComplete===path,'queued control failed after Send: '+label);
 }
 // Controls invoked during a send still run when the original request completes.
 for(const [label,path] of [['Cancel queued follow-up','/thread-inputs/audit/cancel'],['Convert queued follow-up to steering','/queue-steer']]) {
  await htmx.ajax('GET','/controls',{target:'#audit-controls',swap:'innerHTML'});await wait();
  holdSend=true;send();const releaseControlSend=releaseSend;
  controls.querySelector('[aria-label="'+label+'"]').click();
  releaseControlSend();await wait();await wait();
  assert(controls.querySelector('[data-control-complete]')?.dataset.controlComplete===path,'queued control failed during Send: '+label);
  paste(large);
 }
 // Navigating with an open editor must discard its unsaved edit and remove the dialog.
 cards()[0].click();
 const oldDialog=document.querySelector('dialog[open]'), staleSave=button('Save');
 oldDialog.querySelector('textarea').value='stale edit';
 await remount();
 assert(!oldDialog.isConnected && !document.querySelector('dialog[open]'),'navigation left an orphan editor');
 staleSave.click();
 assert(form._captureChatSubmission().message===large,'stale editor changed the replacement composer');
 assert(window._ovPasteDrafts[pasteKey][0].text===large,'stale editor changed the saved draft');
 // Native close and browser history navigation also dispose the editor.
 cards()[0].click();const closedDialog=document.querySelector('dialog[open]');closedDialog.close();await wait();
 assert(!closedDialog.isConnected,'native close leaked editor');
 cards()[0].click();window.dispatchEvent(new PopStateEvent('popstate'));await wait();
 assert(!document.querySelector('dialog'),'history navigation leaked editor');
 // Guard Save even before the mutation observer notices a non-HTMX removal.
 cards()[0].click();const detachedSave=button('Save');
 document.querySelector('dialog textarea').value='detached edit';form.remove();detachedSave.click();await wait();
 assert(!document.querySelector('dialog'),'detached composer left editor behind');
 assert(window._ovPasteDrafts[pasteKey][0].text===large,'detached editor overwrote saved draft');
 document.body.setAttribute('data-test-result','pass');document.getElementById('browser-result').textContent='PASS';
})().catch(error=>{document.body.setAttribute('data-test-result','fail');document.body.setAttribute('data-test-error',error.stack);const failure=document.createElement('pre');failure.textContent=error.stack;document.body.append(failure)});
</script></body></html>`, markup.String())
		}
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "pasted text composer", 10000, 25*time.Second)
}
