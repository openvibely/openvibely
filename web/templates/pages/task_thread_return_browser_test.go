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
	testThreadReturn(t, false, false)
}

func TestBrowserFunctional_ChatReturnPaintsAtBottom(t *testing.T) {
	testThreadReturn(t, true, false)
}

func TestBrowserFunctional_RunningTaskReturnPaintsAtBottom(t *testing.T) {
	testThreadReturn(t, false, true)
}

func TestBrowserFunctional_RunningChatReturnPaintsAtBottom(t *testing.T) {
	testThreadReturn(t, true, true)
}

func testThreadReturn(t *testing.T, chat, running bool) {
	t.Helper()
	chrome := chatNavigationChromePath(t)
	projects := []models.Project{{ID: "one", Name: "One"}, {ID: "two", Name: "Two"}}
	var updated atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/advance" {
			updated.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/tasks/")
		if chat && r.URL.Path == "/chat" {
			id = r.URL.Query().Get("project_id")
		}
		id = strings.TrimSuffix(id, "/thread")
		if id != "one" && id != "two" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		task := &models.Task{ID: id, ProjectID: id, Title: id, Status: models.StatusCompleted, Category: models.CategoryCompleted}
		executions := []models.Execution{{ID: "exec-" + id, TaskID: id, Status: models.ExecCompleted, PromptSent: "Read the project", Output: strings.Repeat("A formatted **response**.\n\n[Using tool: shell | echo example]\n[Tool shell done]\nresult\n[/Tool]\n", 60) + "Final answer", StartedAt: time.Unix(1, 0)}}
		if updated.Load() > 0 && id == "one" {
			executions[0].Output += "\n\nUpdated server answer" + strings.Repeat(".", int(updated.Load()))
		}
		if running {
			executions[0].Status = models.ExecRunning
			task.Status = models.StatusRunning
		}
		ctx := layout.WithUIPreferences(layout.WithDesktopMode(r.Context(), true), layout.UIPreferences{PinnedProjectIDs: []string{"one", "two"}, ProjectLocations: `{"one":"/tasks/one?project_id=one","two":"/tasks/two?project_id=two"}`})
		if chat {
			ctx = layout.WithUIPreferences(ctx, layout.UIPreferences{PinnedProjectIDs: []string{"one", "two"}, ProjectLocations: `{"one":"/chat?project_id=one","two":"/chat?project_id=two"}`})
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			if r.Header.Get("HX-Request") == "true" {
				time.Sleep(200 * time.Millisecond)
				_ = ChatContent(nil, executions, id, nil, nil, nil, false, false, 30).Render(ctx, w)
			} else {
				_ = Chat(projects, id, nil, executions, nil, nil, nil, false, false, 30).Render(ctx, w)
			}
			return
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
	entry := "/tasks/one?project_id=one"
	if chat {
		entry = "/chat?project_id=one"
	}
	runComposerFocusCDP(t, chrome, server.URL+entry, "thread-return", func(b *composerFocusCDP) {
		b.waitFor("rendered transcript", `String(!!document.querySelector('#task-thread-messages .stream-tool, #chat-messages .stream-tool') && !document.querySelector('#task-thread-messages, #chat-messages').hasAttribute('data-transcript-hydrating'))`, "true")
		result := b.evaluateAwait(`(async()=>{
 const paint=()=>new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)));
 const messages=()=>document.querySelector('#task-thread-messages, #chat-messages');
 const trace=[];
 // Exercise asynchronous hydration while the server refresh is on screen.
 const renderVisible=window.renderVisibleChatRows;
 window.renderVisibleChatRows=async function(...args){
  if(document.querySelector('[data-thread-refresh-preview]'))await new Promise(resolve=>setTimeout(resolve,60));
  return renderVisible.apply(this,args);
 };
 let recording=false,seenVisible=false,blink=false,stopped=false,unsafePreview=false;
 function frame(){
  if(stopped)return;
  const m=messages();
  if(recording&&m&&m.clientHeight&&(document.getElementById('task-thread-view')?.dataset.taskId||document.getElementById('chat-page-root')?.dataset.projectId)==='one'){
   const preview=document.querySelector('[data-thread-refresh-preview]');
   if(preview && (preview.querySelector('[id], [data-exec-id], [data-raw-content], script, iframe') || !preview.inert))unsafePreview=true;
   const visible=m.checkVisibility({visibilityProperty:true})||!!preview;
   if(visible)seenVisible=true;else if(seenVisible)blink=true;
   trace.push({visible,distance:preview ? preview.scrollHeight-preview.clientHeight-preview.scrollTop : m.scrollHeight-m.clientHeight-m.scrollTop});
  }
  requestAnimationFrame(frame);
 }
 async function switchTo(id){
  document.querySelector('[data-project-tab="'+id+'"]').click();
  const deadline=performance.now()+5000;
  while(new URLSearchParams(location.search).get('project_id')!==id||window.openVibelyProtectedProjectID){
   if(performance.now()>deadline)throw Error('Project switch did not finish: '+id);
   await paint();
  }
  while(!messages() || messages().hasAttribute('data-transcript-hydrating')){
   if(performance.now()>deadline)throw Error('Transcript did not become ready: '+id);
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
   if(!messages().textContent.includes('Final answer'))throw Error('Lost preserved output');
  }
  if(blink)throw Error('Visible cached transcript was hidden again during server refresh');
  const bad=trace.filter(x=>x.visible&&Math.abs(x.distance)>3);
  if(bad.length)throw Error('Visible return frames away from bottom: '+JSON.stringify(bad));
  if(!trace.some(x=>x.visible))throw Error('No visible return frames');
  // A changed server transcript must still replace the cached snapshot.
  await switchTo('two');await fetch('/advance');seenVisible=false;recording=true;await switchTo('one');recording=false;
  if(blink)throw Error('Changed refresh flickered');
  if(unsafePreview)throw Error('Preview retained active transcript identity');
  if(trace.some(x=>x.visible&&Math.abs(x.distance)>3))throw Error('Refresh moved the visible transcript');
  if(document.querySelector('[data-thread-refresh-preview]'))throw Error('Refresh preview was not released');
  if(!messages().textContent.includes('Updated server answer'))throw Error('Kept stale transcript');
  // Leave while a replacement is still rendering: no static preview or
  // renderer waiter may survive the next project navigation.
  await switchTo('two');await fetch('/advance');
  let release;
  const gate=new Promise(resolve=>{release=resolve});
  window.renderVisibleChatRows=async function(...args){
   if(document.querySelector('[data-thread-refresh-preview]'))await gate;
   return renderVisible.apply(this,args);
  };
  document.querySelector('[data-project-tab="one"]').click();
  const deadline=performance.now()+5000;
  try {
   while(!document.querySelector('[data-thread-refresh-preview]')){
    if(performance.now()>deadline)throw Error('Missing preview during blocked hydration');
    await paint();
   }
   const departingPreview=document.querySelector('[data-thread-refresh-preview]');
   document.querySelector('[data-project-tab="two"]').click();
   while(new URLSearchParams(location.search).get('project_id')!=='two'||window.openVibelyProtectedProjectID){
    if(performance.now()>deadline)throw Error('Could not leave hydrating thread');
    await paint();
   }
   if(departingPreview.isConnected)throw Error('Navigation retained stale preview');
  } finally {release();}
  while(document.querySelector('[data-thread-refresh-preview]')){
   if(performance.now()>deadline)throw Error('Destination preview did not finish');
   await paint();
  }
  return 'pass';
 } finally {stopped=true;window.renderVisibleChatRows=renderVisible;}
})()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}
