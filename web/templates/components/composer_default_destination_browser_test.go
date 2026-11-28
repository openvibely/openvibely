package components

import (
	"bytes"
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	templateui "github.com/openvibely/openvibely/web/templates"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBrowserFunctional_ComposerLocalDefault(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			chrome := testChromePath(t)
			var content bytes.Buffer
			ctx := templateui.WithLocalComposerEnabled(context.Background(), enabled)
			if err := ChatInputForm(ChatInputFormConfig{FormID: "default-form", InputID: "message", PostEndpoint: "/chat/send", TargetID: "messages", ProjectServerOnly: true, ProjectInputID: "destination", ProjectChoices: []models.Project{{ID: "local", Name: "Repo · This computer"}}}).Render(ctx, &content); err != nil {
				t.Fatal(err)
			}
			composerParsed := make(chan struct{}, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/composer-parsed" {
					composerParsed <- struct{}{}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				if r.URL.Path != "/" {
					http.NotFound(w, r)
					return
				}
				// Let timers queued by the composer run before the parser receives
				// the page-specific listener, as can happen with streamed HTML.
				fmt.Fprint(w, `<!doctype html><body data-test-result="pending"><input id="destination" value=""><div id="messages"></div>`+content.String()+`<script>setTimeout(()=>fetch('/composer-parsed'),0)</script>`)
				w.(http.Flusher).Flush()
				select {
				case <-composerParsed:
				case <-r.Context().Done():
					return
				}
				fmt.Fprintf(w, `<script>
let changes=0;document.getElementById('destination').addEventListener('change',()=>changes++);
document.addEventListener('DOMContentLoaded',()=>setTimeout(()=>{try {
 const enabled=%t, input=document.getElementById('destination'), label=document.querySelector('[data-project-input] .chat-custom-select-label');
 if(input.value!==(enabled?'local':''))throw Error('incorrect default');
 if(changes!==(enabled?1:0))throw Error('destination listeners did not run');
 if(enabled&&label.textContent.trim()!=='Local')throw Error('incorrect Local label');
 document.body.dataset.testResult='pass';
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack}},0),{once:true});
</script>`, enabled)
			}))
			defer srv.Close()
			runHeadlessChromeCDPFixture(t, chrome, srv.URL, "local default", 1200, 900, 10*time.Second)
		})
	}
}

func TestBrowserFunctional_ComposerRemembersDestination(t *testing.T) {
	chrome := testChromePath(t)
	remote := remoteid.ID("0123456789abcdef", "project")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var content bytes.Buffer
		formID := "task-form"
		if r.URL.Query().Get("chat") == "1" {
			formID = "chat-form"
		}
		if err := ChatInputForm(ChatInputFormConfig{FormID: formID, InputID: "message", PostEndpoint: "/send", TargetID: "messages", ProjectServerOnly: true, DestinationScope: "shared-test", ProjectInputID: "destination", ProjectChoices: []models.Project{{ID: "local", Name: "Repo · This computer"}, {ID: remote, Name: "Remote"}}}).Render(context.Background(), &content); err != nil {
			t.Error(err)
			return
		}
		fmt.Fprint(w, `<!doctype html><body data-test-result="pending"><input id="destination" value=""><div id="messages"></div>`+content.String()+fmt.Sprintf(`<script>
setTimeout(()=>{try {
 const remote=%q, input=document.getElementById('destination');
 if(location.search.includes('chat=1')) {
  if(input.value!==remote)throw Error('blank Chat did not restore task destination');
  document.body.dataset.testResult='pass';return;
 }
 if(input.value!=='local')throw Error('initial default not Local');
 document.querySelector('[data-project-input]').dispatchEvent(new CustomEvent('chat-select-change',{detail:{value:remote}}));
 if(localStorage.getItem('ov.composer.destination.shared-test')!==remote)throw Error('explicit selection not saved');
 location.replace('/?chat=1');
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack}},200);
</script>`, remote))
	}))
	defer srv.Close()
	runHeadlessChromeCDPFixture(t, chrome, srv.URL, "remembered destination", 1200, 900, 10*time.Second)
}
