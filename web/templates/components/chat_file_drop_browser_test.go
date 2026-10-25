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
		fmt.Fprintf(w, `<!doctype html><head>%s</head><body data-test-result="pending"><div id="chat-messages"></div>%s<div id="browser-result"></div><script>
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
  if (!input) {
   assert(uploads.length === 0, 'outside drop unexpectedly uploaded');
   document.body.setAttribute('data-test-result', 'pass');
   return;
  }
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
})();</script></body>`, guard, composer)
	}))
	defer server.Close()
	for _, path := range []string{"/settings", "/alerts", "/chat"} {
		t.Run(path, func(t *testing.T) {
			runHeadlessChromeFixture(t, chrome, server.URL+path, "file drops on "+path, 3000, 20*time.Second)
		})
	}
}
