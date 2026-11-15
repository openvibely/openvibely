package handler

import (
	"bytes"
	"context"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNativeTaskPanelPreferences(t *testing.T) {
	if testing.Short() || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires native UI")
	}
	host := buildNativeChatHost(t)
	results := make(chan string, 1)
	started := make(chan struct{}, 1)
	tasks := []models.Task{{ID: "one", ProjectID: "project", Title: "Computer task", Status: models.StatusCompleted, Category: models.CategoryCompleted}, {ID: "two", ProjectID: "project", Title: "Other task", Status: models.StatusCompleted, Category: models.CategoryCompleted}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		ctx := layout.WithDesktopMode(r.Context(), true)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/native-test-bridge":
			w.WriteHeader(204)
		case "/browser-result":
			results <- r.URL.Query().Get("status")
			w.WriteHeader(204)
		case "/browser-stage":
			t.Log(r.URL.Query().Get("name"))
			select {
			case started <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
		case "/tasks/sidebar":
			_ = layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(ctx, w)
		case "/fixture":
			var out bytes.Buffer
			_ = layout.Base("Panel preferences", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(ctx, &out)
			fmt.Fprint(w, strings.Replace(out.String(), "</body>", "<script>"+nativePanelPreferencesScenario+"</script></body>", 1))
		default:
			for _, task := range tasks {
				if r.URL.Path == "/tasks/"+task.ID {
					thread := components.TaskThreadView(&task, nil, nil, nil, nil, nil, false, 30)
					_ = pages.TaskDetailContent(&task, nil, nil, nil, nil, nil, nil, "chat", nil, thread).Render(ctx, w)
					return
				}
			}
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, host, server.URL+"/fixture?project_id=project")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := startHandlerBrowserProcess(t, cmd); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(t, cmd)
		}
	}()
	var result string
	select {
	case result = <-results:
	case <-started:
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		select {
		case result = <-results:
		case <-timer.C:
			result = "scenario timeout"
		case <-ctx.Done():
			result = "host deadline exceeded"
		}
	case <-ctx.Done():
		result = "native host startup timeout"
	}
	// Stop before reading the output buffer; the host may still be writing logs.
	stopHandlerBrowserProcess(t, cmd)
	stopped = true
	if result != "pass" {
		t.Fatalf("%s\nNative host output:\n%s", result, output.String())
	}
}

const nativePanelPreferencesScenario = `(async()=>{
const wait=async(fn,label)=>{const until=Date.now()+10000;while(!fn()){if(Date.now()>until)throw Error(label+'; task='+document.querySelector('#task-detail-content')?.dataset.taskId+'; open='+!document.querySelector('#task-details-panel')?.hidden+'; tab='+document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab+'; saved='+JSON.stringify({...localStorage}));await new Promise(r=>setTimeout(r,25));}};
try{
await fetch('/browser-stage?name=started');
await wait(()=>document.readyState==='complete' && window.openVibelyLive,'ready');
window.openVibelyLive.computer=function(project,task,params,options){let closed=false;setTimeout(()=>{if(!closed)options.listeners.computer_status?.({data:JSON.stringify({project_id:project,task_id:task,enabled:!task,project_enabled:true,task_enabled:false,ready:false})});},50);return {close(){closed=true}};};
for(const id of ['one','two']){localStorage.removeItem('task-inspector-open-'+id);localStorage.removeItem('task-inspector-tab-'+id);}
await wait(()=>document.querySelector('[data-task-list-row="one"]'),'sidebar');
async function visit(id){await new Promise(r=>setTimeout(r,250));fetch('/browser-stage?name=visit-'+id);document.querySelector('[data-task-list-row="'+id+'"]').click();await wait(()=>document.querySelector('#task-detail-content')?.dataset.taskId===id && !document.querySelector('#main-content.htmx-settling'),'visit '+id);await new Promise(r=>setTimeout(r,250));}
await visit('two');
await wait(()=>document.querySelector('#task-details-panel').hidden,'second initially closed');
await visit('one');
fetch('/browser-stage?name=open-panel');document.querySelector('#task-details-opener').click();
await wait(()=>!document.querySelector('[data-tab="computer"]').hidden,'computer allowed');
document.querySelector('[data-tab="computer"]').click();
await wait(()=>document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab==='computer','selected Computer');
await visit('two');
await wait(()=>document.querySelector('#task-details-panel').hidden,'second remains closed');
await visit('one');
await wait(()=>document.querySelector('#task-details-panel [aria-selected="true"]')?.dataset.tab==='computer','Computer restored');
await fetch('/browser-result?status=pass');
}catch(e){await fetch('/browser-result?status='+encodeURIComponent(e.message));}
})();`
