package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ComposerFileDrops(t *testing.T) {
	chrome := testChromePath(t)
	var form bytes.Buffer
	if err := ChatInputForm(ChatInputFormConfig{FormID: "chat-form", InputID: "message-input", PostEndpoint: "/chat/send", TargetID: "chat-messages"}).Render(context.Background(), &form); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Exercise the guard rendered by the shared layout, even when no composer
		// is present on a fresh Settings or Alerts document.
		var page bytes.Buffer
		if err := layout.Base(r.URL.Path, nil, "").Render(context.Background(), &page); err != nil {
			t.Error(err)
			http.Error(w, "render layout", http.StatusInternalServerError)
			return
		}
		guard := regexp.MustCompile(`(?s)<script id="file-drop-navigation-guard">.*?</script>`).FindString(page.String())
		if guard == "" {
			t.Error("shared layout is missing the file-drop guard")
		}
		composer := ""
		if r.URL.Path == "/chat" {
			composer = form.String()
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<!doctype html><head>%s</head><body data-test-result="pending"><div id="chat-messages" data-chat-transcript></div>%s<div id="browser-result"></div><script>
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
  var outsideOver = new DragEvent('dragover', {bubbles:true, cancelable:true, dataTransfer:transfer});
  document.body.dispatchEvent(outsideOver);
  assert(outsideOver.defaultPrevented, 'outside file drag did not enable a cancellable drop');
  var outside = new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:transfer});
  document.body.dispatchEvent(outside);
  assert(outside.defaultPrevented, 'outside file drop could navigate');
  var text = new DataTransfer(); text.setData('text/plain', 'text');
  var outsideTextOver = new DragEvent('dragover', {bubbles:true, cancelable:true, dataTransfer:text});
  document.body.dispatchEvent(outsideTextOver);
  assert(!outsideTextOver.defaultPrevented, 'outside plain text drags were blocked');
  var textDrop = new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:text});
  document.body.dispatchEvent(textDrop);
  assert(!textDrop.defaultPrevented, 'plain text drops were blocked');
  var input = document.getElementById('message-input');
  if (!input) {
   assert(uploads.length === 0, 'outside drop unexpectedly uploaded');
   document.body.setAttribute('data-test-result', 'pass');
   return;
  }
  assert(input.closest('form')._chatInputInitialized, 'composer not initialized');
  // Chrome does not apply dropEffect writes to a script-created DataTransfer.
  // Observe the composer's requested operation while retaining its real files;
  // native Wails tests cover the browser/OS drag negotiation.
  var requestedDropEffect;
  Object.defineProperty(transfer, 'dropEffect', {
   get: () => requestedDropEffect,
   set: value => { requestedDropEffect = value; }
  });
  var over = new DragEvent('dragover', {bubbles:true, cancelable:true, dataTransfer:transfer});
  input.dispatchEvent(over);
  assert(over.defaultPrevented, 'composer did not accept drop');
  assert(requestedDropEffect === 'copy', 'composer did not request a copy');
  input.dispatchEvent(new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:transfer}));
  await new Promise(resolve => setTimeout(resolve, 100));
  assert(uploads.length === 1 && uploads[0].length === 1, 'file was not uploaded exactly once');
  // Async Blob reads do not hold --virtual-time-budget, which can expire mid-read.
  var read = new XMLHttpRequest();
  read.open('GET', URL.createObjectURL(uploads[0][0]), false);
  read.send();
  assert(read.responseText === 'hello', 'file content was lost');
  assert(document.querySelector('input[name="attachment_session_id"]').value === 'drop-session', 'attachment session missing');
  var beforeRows = Array.from(document.querySelectorAll('[data-pending-attachment]')).map(row => row.outerHTML);
  assert(beforeRows.length === 1, 'successful upload preview missing');
  var alerts = [];
  window.alert = message => alerts.push(message);
  window.fetch = async function(url, options) {
   assert(options.body.getAll('files').length === 2, 'failed upload should contain two files');
   return {ok:false, text:async function() { return 'failed to process file'; }};
  };
  var failingTransfer = new DataTransfer();
  failingTransfer.items.add(new File(['first'], 'first.txt', {type:'text/plain'}));
  failingTransfer.items.add(new File(['second'], 'second.txt', {type:'text/plain'}));
  input.dispatchEvent(new DragEvent('drop', {bubbles:true, cancelable:true, dataTransfer:failingTransfer}));
  await new Promise(resolve => setTimeout(resolve, 100));
  assert(alerts.length === 1 && alerts[0].includes('Upload failed'), 'upload failure was not displayed');
  assert(JSON.stringify(beforeRows) === JSON.stringify(Array.from(document.querySelectorAll('[data-pending-attachment]')).map(row => row.outerHTML)), 'failed upload changed composer previews');
  assert(document.querySelector('input[name="attachment_session_id"]').value === 'drop-session', 'failed upload changed session');

  // Some drag sources expose file items with an empty FileList. Include a
  // null item and URI metadata, and invalidate the store after dispatch to
  // verify the queued upload retains the PNG File synchronously.
  var png = new File([new Uint8Array([137,80,78,71])], 'picture.png', {type:'image/png'});
  var itemTransfer = {types:['Files', 'text/uri-list'], files:[], items:[
   {kind:'string'}, {kind:'file', getAsFile:() => null}, {kind:'file', getAsFile:() => png}
  ]};
  window.fetch = async function(url, options) {
   var files = options.body.getAll('files');
   assert(files.length === 1 && files[0].name === 'picture.png' && files[0].size === 4, 'PNG item was lost');
   uploads.push(files);
   return {ok:true, json:async function() { return {session_id:'drop-session', attachments:[{filename:'picture.png', size:4}]}; }};
  };
  var itemDrop = new Event('drop', {bubbles:true, cancelable:true});
  Object.defineProperty(itemDrop, 'dataTransfer', {value:itemTransfer});
  input.dispatchEvent(itemDrop);
  png = null;
  itemTransfer.items = [];
  await new Promise(resolve => setTimeout(resolve, 100));
  assert(uploads.length === 2, 'file item fallback did not upload');
  assert(document.querySelectorAll('[data-pending-attachment]').length === 2, 'PNG preview missing');
  var textOver = new DragEvent('dragover', {bubbles:true, cancelable:true, dataTransfer:text});
  input.dispatchEvent(textOver);
  assert(!textOver.defaultPrevented, 'text drag advertised as an attachment');
  document.body.setAttribute('data-test-result', 'pass');
 } catch(error) {
  document.body.setAttribute('data-test-result', 'fail');
  document.body.setAttribute('data-test-error', error.message);
 }
})();</script></body>`, guard, composer)
	}))
	defer server.Close()
	for _, path := range []string{"/settings", "/alerts", "/chat"} {
		t.Run(path, func(t *testing.T) {
			runHeadlessChromeFixture(t, chrome, server.URL+path, "file drops on "+path, 3000, 20*time.Second)
		})
	}
}
