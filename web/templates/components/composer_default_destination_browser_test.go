package components

import (
	"bytes"
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
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
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `<!doctype html><body data-test-result="pending"><input id="destination" value=""><div id="messages"></div>`+content.String()+fmt.Sprintf(`<script>
let changes=0;document.getElementById('destination').addEventListener('change',()=>changes++);
setTimeout(()=>{try {
 const enabled=%t, input=document.getElementById('destination'), label=document.querySelector('[data-project-input] .chat-custom-select-label');
 if(input.value!==(enabled?'local':''))throw Error('incorrect default');
 if(changes!==(enabled?1:0))throw Error('destination listeners did not run');
 if(enabled&&label.textContent.trim()!=='Local')throw Error('incorrect Local label');
 document.body.dataset.testResult='pass';
}catch(e){document.body.dataset.testResult='fail';document.body.dataset.testError=e.stack}},200);
</script>`, enabled))
			}))
			defer srv.Close()
			runHeadlessChromeCDPFixture(t, chrome, srv.URL, "local default", 1200, 900, 10*time.Second)
		})
	}
}
