package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_KanbanScrollbarsClearCards(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "scrollbar-project", Name: "Scrollbar geometry"}
	var tasks []models.Task
	for _, lane := range []struct {
		category models.TaskCategory
		status   models.TaskStatus
	}{
		{models.CategoryBacklog, models.StatusPending},
		{models.CategoryCompleted, models.StatusCompleted},
		{models.CategoryActive, models.StatusRunning},
		{models.CategoryActive, models.StatusPending},
	} {
		for i := 0; i < 12; i++ {
			tasks = append(tasks, models.Task{ID: fmt.Sprintf("%s-%s-%d", lane.category, lane.status, i), ProjectID: project.ID, Title: "Scrollbar clearance", Prompt: "A card filling the available lane width", Category: lane.category, Status: lane.status})
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/tasks" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := Tasks([]models.Project{project}, &project, tasks, nil, nil, "", "").Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks?project_id="+project.ID, "kanban-scrollbar", func(b *composerFocusCDP) {
		b.waitFor("board ready", `String(document.querySelectorAll('#kanban-board .category-drop-zone').length===4)`, "true")
		for _, width := range []int{1500, 1101, 800, 500} {
			b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
			b.evaluate(`document.documentElement.classList.add('task-list-collapsed'); 'ok'`)
			b.waitFor(fmt.Sprintf("scrollbar clearance at %dpx", width), `(function(){
    var zones=Array.from(document.querySelectorAll('#kanban-board .category-drop-zone'));
    for(var z of zones){
     var card=z.querySelector('[data-task-id]'),column=z.closest('.kanban-column');
     if(!card)return 'missing card';
     var zr=z.getBoundingClientRect(),cr=card.getBoundingClientRect(),outer=column.getBoundingClientRect(),style=getComputedStyle(column),zs=getComputedStyle(z);
     var edge=outer.right-parseFloat(style.borderRightWidth);
     if(Math.abs(zr.right-edge)>1)return 'scrollport does not reach column edge';
     if(zr.right-cr.right<15)return 'scrollbar overlaps card';
     if(z.scrollWidth>z.clientWidth+1)return 'horizontal overflow';
     if(z.scrollHeight<=z.clientHeight)return 'fixture must overflow';
     // Compare against the old geometry: the gutter moves, the cards do not.
     var before=cr.width,oldMargin=z.style.marginRight,oldPadding=z.style.paddingRight;
     z.style.marginRight='0px';z.style.paddingRight='2px';
     var oldWidth=card.getBoundingClientRect().width;
     z.style.marginRight=oldMargin;z.style.paddingRight=oldPadding;
     if(Math.abs(before-oldWidth)>1)return 'card width changed';
     z.scrollTop=80;
     if(z.scrollTop<1)return 'lane cannot scroll';
     z.scrollTop=0;
    }
    return 'ok';
   })()`, "ok")
		}
	})
}
