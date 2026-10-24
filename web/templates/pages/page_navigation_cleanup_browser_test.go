package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_PageCleanupWaitsForCommittedNavigationInChrome(t *testing.T) {
	project := &models.Project{ID: "p", Name: "Project"}
	task := &models.Task{ID: "t1", Title: "Task", ProjectID: "p", Status: models.StatusPending, Category: models.CategoryActive}
	pages := map[string]templ.Component{
		"schedule":    ScheduleContent(project, nil, 0, nil, nil),
		"tasks":       TasksContent(project, nil, nil, nil, "", ""),
		"task-detail": TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil),
	}
	for name, component := range pages {
		t.Run(name, func(t *testing.T) {
			var rendered bytes.Buffer
			if err := component.Render(context.Background(), &rendered); err != nil {
				t.Fatal(err)
			}
			markup, _ := json.Marshal(rendered.String())
			kind, _ := json.Marshal(name)
			fixture := `<main id="reconnect-result"></main><main id="main-content"></main><script src="` + static.URL("vendor/htmx.min.js") + `"></script><script>
window.addEventListener('load', async function() {
 const result=document.getElementById('reconnect-result'),main=document.getElementById('main-content');
 const markup=` + string(markup) + `,kind=` + string(kind) + `;
 const wait=()=>new Promise(r=>setTimeout(r,50));
 window.fetch=()=>new Promise(()=>{});
 window.openVibelyNavigate=()=>{};
 const live={
  schedule:()=>!!window._schedIntervalId,
  tasks:()=>!!window._kanbanListenersCleanup,
  'task-detail':()=>!!window._taskDetailFileChangesHandlers
 }[kind];
 function check(ok,message){if(!ok)throw Error(message);}
 try {
  for(let visit=0;visit<2;visit++) {
   history.replaceState({},'','/'+kind+'?project_id=p');
   htmx.swap(main,markup,{swapStyle:'innerHTML',settleDelay:0});await wait();
   check(live(),'page did not install its listeners visit='+visit);
   const cancelled={target:main,shouldSwap:false,xhr:{status:500}};
   document.body.dispatchEvent(new CustomEvent('htmx:beforeSwap',{bubbles:true,detail:cancelled}));
   await wait();
   check(live(),'cancelled navigation tore down the visible page visit='+visit);
   htmx.swap(main,'<p>Other page</p>',{swapStyle:'innerHTML',settleDelay:0});await wait();
   check(!live(),'committed navigation left page listeners installed visit='+visit);
  }
  result.setAttribute('data-test-result','pass');
 } catch(e) {result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(e));}
});</script>`
			runReconnectChromeFixture(t, fixture)
		})
	}
}
