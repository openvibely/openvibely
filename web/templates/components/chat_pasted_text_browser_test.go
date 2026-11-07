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
 function assertDraftCleared() {assert(cards().length===0 && input.value==='', 'submitted draft not cleared at handoff');}

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
  const ids=form._captureChatSubmission().pastedTextIDs;
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
 holdSend=true;send();assertDraftCleared();
 paste('new draft '.repeat(500));
 assert(!cards()[0].disabled,'new unsent card should stay editable');
 form.querySelectorAll('[aria-label="Remove pasted text"]')[0].click();
 assertDraftCleared();releaseSend();await wait();
 assert(document.getElementById('messages').textContent==='Explain this\n\nedited text','send lost content');
 assert(cards().length===0 && input.value==='','accepted draft not cleared');
 paste(large); cards()[0].click();document.querySelector('dialog textarea').value='fail';button('Save').click();await wait();holdSend=true;send();assertDraftCleared();releaseSend();await wait();
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
 holdSend=true;input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,metaKey:true,bubbles:true,cancelable:true}));assertDraftCleared();releaseSend();await wait();
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
 assertDraftCleared();
 const newer='new card B '.repeat(500);
 paste(newer);input.value='new instructions';input.dispatchEvent(new Event('input',{bubbles:true}));
 assert(cards().length===1 && !cards()[0].disabled,'replacement draft missing cards');
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
 // Failure after navigation restores the submitted draft alongside newer content.
 form.querySelector('[aria-label="Remove pasted text"]').click();
 input.value='fail';holdSend=true;send();assertDraftCleared();const releaseFailure=releaseSend;
 await remount();input.value='new text';paste(large);
 releaseFailure();await wait();
 assert(input.value==='new text','failure overwrote newer text');
 assert(cards().length===2,'failure did not preserve both drafts');
 assert(form._captureChatSubmission().message==='new text\n\n'+large+'\n\nfail','failed text was not recovered separately');
 form.querySelectorAll('[aria-label="Remove pasted text"]')[1].click();input.value='';
 const pasteKey=form.getAttribute('data-attachment-state-key')+'-pasted-text';
 delete window._ovPasteDrafts[pasteKey];await remount();
 assert(form._captureChatSubmission().message===large,'surviving card was not persisted');

 // A queued send never reaches the server after its composer is removed.
 // Restore it alongside the newer draft, without restoring the in-flight send.
 const auditSends=[],previousSend=XMLHttpRequest.prototype.send;
 XMLHttpRequest.prototype.send=function(body){auditSends.push(new URLSearchParams(body).get('message'));return previousSend.call(this,body);};
 holdSend=true;send();const releaseFirst=releaseSend;
 const queuedText='queued second card '.repeat(500),nextDraft='new unsent card '.repeat(500);
 input.value='queued instructions';paste(queuedText);send();assertDraftCleared();
 paste(nextDraft);await remount();
 assert(cards().length===3,'navigation did not restore queued text and card alongside newer draft');
 const recovered=form._captureChatSubmission().message;
 assert(recovered===nextDraft+'\n\nqueued instructions\n\n'+queuedText,'queued recovery changed draft contents');
 releaseFirst();await wait();await wait();
 assert(auditSends.filter(Boolean).length===1,'cancelled queued send reached the server');
 assert(form._captureChatSubmission().message===recovered,'first completion erased recovered draft');
 assert(window._ovPendingComposerSubmissions.size===0,'pending submission registry leaked entries');
 await remount();assert(form._captureChatSubmission().message===recovered,'recovered draft did not survive another remount');
 form.querySelectorAll('[aria-label="Remove pasted text"]').forEach(button=>button.click());paste(large);
 XMLHttpRequest.prototype.send=previousSend;
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
