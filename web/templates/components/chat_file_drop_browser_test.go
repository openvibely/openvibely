package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserFunctional_ComposerFileDrops(t *testing.T) {
	chrome := testChromePath(t)
	var form bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{FormID: "chat-form", InputID: "message-input", PostEndpoint: "/chat/send", TargetID: "chat-messages"}).Render(context.Background(), &form); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><body data-test-result="pending"><div id="chat-messages"></div>%s<div id="browser-result"></div><script>
(async function() {
 function assert(ok, message) { if (!ok) throw new Error(message); }
 try {
  var uploads = [];
  window.fetch = async function(url, options) {
   assert(url === '/chat/attachments', 'wrong upload endpoint');
   uploads.push(options.body.getAll('files'));
   return {ok:true, json:async function() { return {session_id:'drop-session', attachments:[{filename:'hello.txt', size:5}]}; }};
  };
  var transfer = new DataTransfer();
  transfer.items.add(new File(['hello'], 'hello.txt', {type:'text/plain'}));
  var outside = new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:transfer});
  document.body.dispatchEvent(outside);
  assert(outside.defaultPrevented, 'outside file drop could navigate');
  var text = new DataTransfer(); text.setData('text/plain', 'text');
  var textDrop = new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:text});
  document.body.dispatchEvent(textDrop);
  assert(!textDrop.defaultPrevented, 'plain text drops were blocked');
  var input = document.getElementById('message-input');
  var over = new DragEvent('dragover', {bubbles:true, cancelable:true, dataTransfer:transfer});
  input.dispatchEvent(over);
  assert(over.defaultPrevented, 'composer did not accept drop');
  input.dispatchEvent(new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:transfer}));
  await new Promise(resolve => setTimeout(resolve, 100));
  assert(uploads.length === 1 && uploads[0].length === 1, 'file was not uploaded exactly once');
  assert(await uploads[0][0].text() === 'hello', 'file content was lost');
  assert(document.querySelector('input[name="attachment_session_id"]').value === 'drop-session', 'attachment session missing');
  document.body.setAttribute('data-test-result', 'pass');
 } catch(error) {
  document.body.setAttribute('data-test-result', 'fail');
  document.body.setAttribute('data-test-error', error.message);
 }
})();</script></body>`, form.String())
	}))
	defer server.Close()
	runHeadlessChromeFixture(t, chrome, server.URL, "composer file drops", 3000, 20*time.Second)
}
