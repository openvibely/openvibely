//go:build darwin && openvibely_native_ui

package pages

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// This native regression uses Wails' wails:// asset handler and a real WKWebView.
// Run it from an interactive macOS session with:
// OPENVIBELY_RUN_NATIVE_UI=1 go test -tags openvibely_native_ui ./web/templates/pages -run TestNativeWebKitAnalyticsProviderRequestsDoNotBlockTaskNavigation -count=1 -timeout 90s
func TestNativeWebKitAnalyticsProviderRequestsDoNotBlockTaskNavigation(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("native Wails UI coverage is opt-in; set OPENVIBELY_RUN_NATIVE_UI=1")
	}

	project := models.Project{ID: "project-native-analytics", Name: "Native Analytics project"}
	page := renderNativeAnalyticsNavigationPage(t, project)
	var providerStarted atomic.Int32
	var providerCanceled atomic.Int32
	var requestLogMu sync.Mutex
	var requestLog []string
	navigationRequests := make(chan string, 1)
	results := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/native-test/provider-state" {
			requestLogMu.Lock()
			requestLog = append(requestLog, r.Method+" "+r.URL.RequestURI()+" HX="+r.Header.Get("HX-Request"))
			requestLogMu.Unlock()
		}
		if static.ServeAsset(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/analytics":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
		case r.URL.Path == "/tasks/sidebar":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<a data-task-list-row="task-native" href="/tasks/task-native?project_id=project-native-analytics" hx-get="/tasks/task-native?project_id=project-native-analytics" hx-target="#main-content" hx-swap="innerHTML" hx-push-url="true">Native task</a>`)
		case r.URL.Path == "/tasks/task-native":
			navigationRequests <- r.Header.Get("HX-Request")
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = io.WriteString(w, `<div data-native-navigation-destination="task-native">Task detail loaded</div>`)
		case r.URL.Path == "/api/analytics/usage" && r.URL.Query().Get("projection") == "account_limits":
			providerStarted.Add(1)
			<-r.Context().Done()
			providerCanceled.Add(1)
		case r.URL.Path == "/native-test/provider-state":
			_, _ = fmt.Fprintf(w, "%d", providerStarted.Load())
		case r.URL.Path == "/native-test/result":
			result := r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			select {
			case results <- result:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/ui/preferences":
			w.WriteHeader(http.StatusNoContent)
		default:
			if r.Header.Get("HX-Request") == "true" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var childOutput bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(),
		"OPENVIBELY_NATIVE_UI_HELPER=1",
		"OPENVIBELY_NATIVE_UI_ASSET_PROXY=1",
		"OPENVIBELY_NATIVE_UI_URL="+server.URL,
		"OPENVIBELY_NATIVE_UI_WINDOW_URL=/analytics?project_id="+project.ID+"&view=usage&provider_network=1&provider_count=3",
	)
	cmd.Stdout = &childOutput
	cmd.Stderr = &childOutput
	if err := cmd.Start(); err != nil {
		t.Fatalf("start native Wails helper process: %v", err)
	}
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	stopChild := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-childDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("native Wails helper did not terminate; output:\n%s", childOutput.String())
		}
	}
	defer stopChild()

	var outcome string
	select {
	case outcome = <-results:
	case err := <-childDone:
		t.Fatalf("native Wails helper exited before reporting a browser result: %v\n%s", err, childOutput.String())
	case <-time.After(35 * time.Second):
		requestLogMu.Lock()
		requests := strings.Join(requestLog, "\n")
		requestLogMu.Unlock()
		t.Fatalf("timed out waiting for native WebKit navigation result\nrequests:\n%s\nhelper output:\n%s", requests, childOutput.String())
	}
	if !strings.HasPrefix(outcome, "pass:") {
		t.Fatalf("native WebKit navigation fixture failed: %s\n%s", outcome, childOutput.String())
	}
	select {
	case header := <-navigationRequests:
		if header != "true" {
			t.Fatalf("task navigation HX-Request header = %q, want true", header)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native WebKit did not send the task navigation request")
	}
	deadline := time.Now().Add(5 * time.Second)
	for providerCanceled.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := providerStarted.Load(); got != 3 {
		t.Fatalf("native WebKit started %d provider requests, want 3", got)
	}
	if got := providerCanceled.Load(); got != 3 {
		t.Fatalf("native WebKit canceled %d provider requests on navigation, want 3", got)
	}
}

func renderNativeAnalyticsNavigationPage(t *testing.T, project models.Project) []byte {
	t.Helper()
	analytics, err := renderAnalyticsComponent(&project)
	if err != nil {
		t.Fatal(err)
	}
	child := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		if _, err := io.WriteString(w, analyticsProjectSwitchFetchFixture); err != nil {
			return err
		}
		if _, err := w.Write(analytics); err != nil {
			return err
		}
		_, err := io.WriteString(w, nativeAnalyticsNavigationRunner)
		return err
	})
	var rendered bytes.Buffer
	ctx := templ.WithChildren(context.Background(), child)
	if err := layout.Base("Native Analytics navigation", []models.Project{project}, project.ID).Render(layout.WithDesktopMode(ctx, true), &rendered); err != nil {
		t.Fatal(err)
	}
	return rendered.Bytes()
}

const nativeAnalyticsNavigationRunner = `<script>
(function(){
 function wait(ms){return new Promise(function(resolve){setTimeout(resolve,ms);});}
 function waitFor(check,label,timeout){var start=performance.now();return new Promise(function(resolve,reject){function poll(){try{if(check()){resolve();return;}}catch(e){reject(e);return;}if(performance.now()-start>(timeout||10000)){reject(new Error('timed out waiting for '+label));return;}setTimeout(poll,20);}poll();});}
 async function waitForProviderRequests(){var start=performance.now();while(performance.now()-start<10000){var response=await window.nativeFetch('/native-test/provider-state');if(await response.text()==='3')return;await wait(20);}throw new Error('three provider requests did not reach the Wails backend');}
 async function report(status,message){try{await window.nativeFetch('/native-test/result?status='+encodeURIComponent(status)+'&message='+encodeURIComponent(message||''));}catch(e){}}
 window.addEventListener('DOMContentLoaded',function(){(async function(){try{
  await waitFor(function(){return window.pendingProviderRequests&&window.pendingProviderRequests.length===3;},'three Analytics provider requests');
  await waitForProviderRequests();
	  await waitFor(function(){return document.querySelector('#task-list-groups [data-task-list-row="task-native"]');},'task list row');
	  var taskRow=document.querySelector('#task-list-groups [data-task-list-row="task-native"]');
	  if(window.htmx)window.htmx.process(taskRow);
	  if(!window.pendingProviderRequests.every(function(request){return request.signal&&!request.signal.aborted;}))throw new Error('provider requests were not pending before navigation');
  document.querySelector('#task-list-groups [data-task-list-row="task-native"]').click();
  await waitFor(function(){return document.querySelector('#main-content [data-native-navigation-destination="task-native"]');},'task detail destination',10000);
  await waitFor(function(){return window.pendingProviderRequests.every(function(request){return request.signal.aborted;});},'provider request cancellation');
  await report('pass','task navigation completed while provider requests were pending');
 }catch(error){await report('fail',String(error&&error.stack||error));}})();},{once:true});
})();
</script>`
