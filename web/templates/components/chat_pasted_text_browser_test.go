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
 function assertEditorLocked() {
  assert(cards()[0].disabled,'submitted card editor is enabled');
  cards()[0].click();
  cards()[0].dispatchEvent(new MouseEvent('click',{bubbles:true}));
  assert(!document.querySelector('dialog[open]'),'submitted card opened editor');
 }

 function paste(text) {const data=new DataTransfer();data.setData('text/plain',text);const event=new ClipboardEvent('paste',{clipboardData:data,bubbles:true,cancelable:true});input.dispatchEvent(event);return event.defaultPrevented;}
 function button(label){return Array.from(document.querySelectorAll('dialog button')).find(b=>b.textContent===label);}
 function send(){input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true,cancelable:true}));}
 const large='hello <script> literal & text\n'.repeat(250);
 assert(!paste('short text'),'short paste intercepted');
 input.value='Explain this'; input.setSelectionRange(12,12);
 assert(paste(large),'large paste not intercepted');
 assert(cards().length===1 && input.value==='Explain this','card must keep composer compact');
 assert(form._captureChatSubmission().message==='Explain this\n\n'+large,'full paste missing');
 cards()[0].click(); document.querySelector('dialog textarea').value='discard';button('Cancel').click();await wait();
 assert(form._captureChatSubmission().message.endsWith(large),'cancel changed paste');
 cards()[0].click();document.querySelector('dialog textarea').value='edited text';button('Save').click();await wait();
 assert(form._captureChatSubmission().message==='Explain this\n\nedited text','edit not saved');
 holdSend=true;send();assertEditorLocked();
 paste('new draft '.repeat(500));
 assert(!cards()[1].disabled,'new unsent card should stay editable');
 form.querySelectorAll('[aria-label="Remove pasted text"]')[1].click();
 assertEditorLocked();releaseSend();await wait();
 assert(document.getElementById('messages').textContent==='Explain this\n\nedited text','send lost content');
 assert(cards().length===0 && input.value==='','accepted draft not cleared');
 paste(large); cards()[0].click();document.querySelector('dialog textarea').value='fail';button('Save').click();await wait();holdSend=true;send();assertEditorLocked();releaseSend();await wait();
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
 holdSend=true;input.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',ctrlKey:true,metaKey:true,bubbles:true,cancelable:true}));assertEditorLocked();releaseSend();await wait();
 assert(document.getElementById('pending-thread-inputs').textContent===expected,'steering lost or duplicated pasted text');
 assert(cards().length===0,'accepted steering left cards');
 paste(large);
 const waiting=form._captureChatSubmission();
 assert(!cards()[0].disabled,'capturing a snapshot should not lock cards');
 form._beginChatSubmission(waiting);assertEditorLocked();
 const second=form._captureChatSubmission();form._beginChatSubmission(second);
 form._chatSubmissionFailed(waiting);assertEditorLocked();
 form._chatSubmissionFailed(second);assert(!cards()[0].disabled,'failed preparation left editor locked');

 // Send A, navigate back into a fresh composer, add B, then accept A.
 form.querySelector('[aria-label="Remove pasted text"]').click();
 async function remount() {
  await htmx.ajax('GET','/composer',{target:'#composer-fixture',swap:'innerHTML'});
  await wait();
  form=document.getElementById('paste-form');input=document.getElementById('paste-input');
 }
 paste(large);holdSend=true;send();
 const releaseA=releaseSend;
 await remount();
 assertEditorLocked();
 const newer='new card B '.repeat(500);
 paste(newer);input.value='new instructions';input.dispatchEvent(new Event('input',{bubbles:true}));
 assert(cards().length===2 && !cards()[1].disabled,'replacement draft missing cards');
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
 const pasteKey=form.getAttribute('data-attachment-state-key')+'-pasted-text';
 delete window._ovPasteDrafts[pasteKey];await remount();
 assert(form._captureChatSubmission().message===large,'surviving card was not persisted');
 document.body.setAttribute('data-test-result','pass');document.getElementById('browser-result').textContent='PASS';
})().catch(error=>{document.body.setAttribute('data-test-result','fail');document.body.setAttribute('data-test-error',error.stack);const failure=document.createElement('pre');failure.textContent=error.stack;document.body.append(failure)});
</script></body></html>`, markup.String())
		}
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "pasted text composer", 10000, 25*time.Second)
}
