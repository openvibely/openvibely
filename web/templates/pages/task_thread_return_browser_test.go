package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_TaskThreadReturnPaintsAtBottom(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}
	var updated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/advance" {
			updated.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/tasks/")
		id = strings.TrimSuffix(id, "/thread")
		if id != "one" && id != "two" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		task := &models.Task{ID: id, ProjectID: id, Title: id, Status: models.StatusCompleted, Category: models.CategoryCompleted}
		executions := []models.Execution{{ID: "exec-" + id, TaskID: id, Status: models.ExecCompleted, PromptSent: "Read the project", Output: strings.Repeat("A formatted **response**.\n\n[Using tool: shell | echo example]\n[Tool shell done]\nresult\n[/Tool]\n", 60) + "Final answer", StartedAt: time.Unix(1, 0)}}
		if updated.Load() && id == "one" {
			executions[0].Output += "\n\nUpdated server answer"
		}
		thread := components.TaskThreadView(task, executions, nil, nil, nil, nil, false, 30)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if strings.HasSuffix(r.URL.Path, "/thread") {
			_ = thread.Render(r.Context(), w)
			return
		}
		if r.Header.Get("HX-Request") == "true" {
			if r.Header.Get("X-OpenVibely-Thread-Refresh") == "true" {
				time.Sleep(200 * time.Millisecond)
				_ = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil, thread).Render(r.Context(), w)
			} else {
				_ = TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(r.Context(), w)
			}
			return
		}
		_ = TaskDetailPage(projects, task, nil, nil, nil, nil, nil, nil, "chat", nil).Render(layout.WithUIPreferences(layout.WithDesktopMode(r.Context(), true), layout.UIPreferences{PinnedProjectIDs: []string{"one", "two"}, ProjectLocations: `{"one":"/tasks/one?project_id=one","two":"/tasks/two?project_id=two"}`}), w)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/one?project_id=one", "thread-return", func(b *composerFocusCDP) {
		b.waitFor("rendered transcript", `String(!!document.querySelector('#task-thread-messages .stream-tool') && !document.querySelector('#task-thread-messages').hasAttribute('data-transcript-hydrating'))`, "true")
		result := b.evaluateAwait(`(async()=>{
 const paint=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
 const trace=[];
 let recording=false,seenVisible=false,blink=false,stopped=false;
 function frame(){
  if(stopped)return;
  const m=document.getElementById('task-thread-messages');
  if(recording&&m&&m.clientHeight&&document.getElementById('task-thread-view')?.dataset.taskId==='one'){
   const visible=m.checkVisibility({visibilityProperty:true});
   if(visible)seenVisible=true;else if(seenVisible)blink=true;
   trace.push({visible,distance:m.scrollHeight-m.clientHeight-m.scrollTop});
  }
  requestAnimationFrame(frame);
 }
 async function switchTo(id){
  document.querySelector('[data-project-tab="'+id+'"]').click();
  const deadline=performance.now()+5000;
  while(location.pathname!=='/tasks/'+id||window.openVibelyProtectedProjectID){
   if(performance.now()>deadline)throw Error('Project switch did not finish: '+id);
   await paint();
  }
  await paint();await paint();
 }
 requestAnimationFrame(frame);
 try {
  for(let i=0;i<3;i++){
   await switchTo('two');
   seenVisible=false;recording=true;
   await switchTo('one');recording=false;
   if(!document.getElementById('task-thread-messages').textContent.includes('Final answer'))throw Error('Lost preserved output');
  }
  if(blink)throw Error('Visible cached transcript was hidden again during server refresh');
  const bad=trace.filter(x=>x.visible&&Math.abs(x.distance)>3);
  if(bad.length)throw Error('Visible return frames away from bottom: '+JSON.stringify(bad));
  if(!trace.some(x=>x.visible))throw Error('No visible return frames');
  // A changed server transcript must still replace the cached snapshot.
  await switchTo('two');await fetch('/advance');await switchTo('one');
  if(!document.getElementById('task-thread-messages').textContent.includes('Updated server answer'))throw Error('Kept stale transcript');
  return 'pass';
 } finally {stopped=true;}
})()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}
