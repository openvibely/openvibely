package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_TaskListGoalEventsAndSearch(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var achieved atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			task := models.Task{ID: "goal-task", ProjectID: "goal-project", Title: "Goal task", Status: models.StatusCompleted, Category: models.CategoryCompleted, GoalMet: achieved.Load()}
			if err := layout.TaskListPanelContent(task.ProjectID, layout.TaskListGroups([]models.Task{task})).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "goal-project", Name: "Goal project"}}, "goal-project").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-goal-events", func(b *composerFocusCDP) {
		state := `document.querySelector('[data-task-list-row="goal-task"] [data-task-state]')?.dataset.taskState || ''`
		b.waitFor("initial completed icon", state, "completed")
		b.waitFor("web toggle centered above icon rail", `(()=>{const root=document.documentElement; if(root.dataset.openvibelyRuntime!=='web')return 'wrong runtime';const toggle=document.getElementById('task-list-toggle').getBoundingClientRect();const rail=parseFloat(getComputedStyle(root).getPropertyValue('--rail-width'));return String(Math.abs(toggle.left+toggle.width/2-rail/2)<1)})()`, "true")
		b.waitFor("web header actions stay inside task list", `(()=>{const panel=document.getElementById('task-list-panel').getBoundingClientRect();const actions=document.querySelector('#task-list-header .task-list-actions').getBoundingClientRect();return String(actions.right<=panel.right && actions.left>panel.left)})()`, "true")
		b.click("#task-list-toggle")
		b.waitFor("collapsed web detached tabs align with page edge", `(()=>{const tab=document.querySelector('.desktop-project-tab').getBoundingClientRect();const page=document.getElementById('main-content').getBoundingClientRect();return String(document.documentElement.classList.contains('task-list-collapsed') && Math.abs(tab.left-page.left)<0.5)})()`, "true")
		b.click("#task-list-toggle")
		b.waitFor("task list reopened", `String(!document.documentElement.classList.contains('task-list-collapsed'))`, "true")
		// Search must reveal a collapsed group without saving the temporary expansion.
		b.evaluate(`document.querySelector('[data-task-list-group="Completed"]').open=false; document.getElementById('task-list-search-toggle').click(); var input=document.getElementById('task-list-search-input'); input.value='Goal task'; input.dispatchEvent(new Event('input')); 'searched'`)
		visible := `String(document.querySelector('[data-task-list-group="Completed"]').open && !document.querySelector('[data-task-list-row="goal-task"]').hidden)`
		b.waitFor("search opens matching group", visible, "true")
		achieved.Store(true)
		// Goal service events omit project_id; they must still refresh this list.
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_goal_evaluated',task_id:'goal-task',goal_status:'achieved'}})); 'sent'`)
		b.waitFor("goal achieved icon", state, "goal-met")
		achieved.Store(false)
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_goal_cleared',task_id:'goal-task'}})); 'sent'`)
		b.waitFor("cleared goal restores completed icon", state, "completed")
		b.waitFor("search survives live list replacements", visible, "true")
		b.evaluate(`document.getElementById('task-list-search-input').value=''; document.getElementById('task-list-search-input').dispatchEvent(new Event('input')); 'cleared'`)
		b.waitFor("clearing search restores collapsed group", `String(document.querySelector('[data-task-list-group="Completed"]').open)`, "false")
		b.waitFor("temporary expansion not persisted", `String(JSON.parse(localStorage.getItem('openvibely.taskListGroups'))[document.querySelector('[data-task-list-group="Completed"]').dataset.taskListProjectScope+':Completed'].closed)`, "true")
		b.evaluate(`document.querySelector('[data-task-list-group="Completed"]').open=true; var input=document.getElementById('task-list-search-input'); input.value='no match'; input.dispatchEvent(new Event('input')); input.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 'closed search'`)
		b.waitFor("closing search preserves previously open group", `String(document.querySelector('[data-task-list-group="Completed"]').open && !document.querySelector('[data-task-list-group="Completed"]').hidden)`, "true")
	})
}

func TestBrowserFunctional_TaskListClickUsesDisplayedTask(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			tasks := []models.Task{
				{ID: "task-a", Title: "Task A", Category: models.CategoryCompleted},
				{ID: "task-b", Title: "Task B", Category: models.CategoryCompleted},
			}
			if err := layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-displayed-task", func(b *composerFocusCDP) {
		b.waitFor("task rows ready", `String(!!document.querySelector('[data-task-list-row="task-a"]'))`, "true")
		// A cached preview is already showing B while A remains in the address bar.
		b.evaluate(`htmx.process(document.getElementById('task-list-groups')); history.replaceState({},'', '/tasks/task-a?project_id=project'); document.getElementById('main-content').innerHTML='<div id="task-thread-view" data-task-id="task-b"></div>'; window.taskRowRequests=0; document.body.addEventListener('htmx:beforeRequest',e=>{if(e.detail.elt.matches('[data-task-list-row]')){window.taskRowRequests++;e.preventDefault();}}); document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click back to A starts navigation", `String(window.taskRowRequests)`, "1")
		b.evaluate(`document.getElementById('task-thread-view').dataset.taskId='task-a'; document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click on displayed task remains a no-op", `String(window.taskRowRequests)`, "1")
		// Cold navigation hides A behind the loading state; clicking A must cancel it too.
		b.evaluate(`document.getElementById('main-content').setAttribute('data-page-loading',''); document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click back during cold navigation is allowed", `String(window.taskRowRequests)`, "2")
		// Changes view retains the thread DOM but hides its workspace.
		b.evaluate(`document.getElementById('main-content').removeAttribute('data-page-loading'); document.getElementById('task-thread-view').outerHTML='<div id="tab-chat" class="hidden" data-workspace-hidden="true"><div id="task-thread-view" data-task-id="task-a"></div></div>'; document.querySelector('[data-task-list-row="task-a"]').click(); 'clicked'`)
		b.waitFor("click returns from changes to conversation", `String(window.taskRowRequests)`, "3")
	})
}

func TestBrowserFunctional_TaskListSearchFiltersExtraRows(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var revision atomic.Int32
	tasks := []models.Task{}
	for _, title := range []string{"login-first", "other-2", "other-3", "other-4", "other-5", "other-6", "other-7", "login-extra", "unrelated-extra"} {
		tasks = append(tasks, models.Task{ID: title, Title: title, Category: models.CategoryCompleted})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			fmt.Fprintf(w, "<!-- revision %d -->", revision.Add(1))
			// Live refreshes can still be in flight when the browser shuts down.
			if err := layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(r.Context(), w); err != nil && r.Context().Err() == nil && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-extra-search", func(b *composerFocusCDP) {
		visible := `JSON.stringify(Array.from(document.querySelectorAll('[data-task-list-row]')).filter(e=>getComputedStyle(e).display!=='none').map(e=>e.dataset.taskListRow))`
		b.waitFor("preview group loaded", `String(!!document.querySelector('.task-list-group'))`, "true")
		b.evaluate(`var group=document.querySelector('.task-list-group');document.getElementById('task-list-groups').style.maxHeight=(group.querySelector('summary').getBoundingClientRect().height+10+30+7*34)+'px'; 'limit fixture height'`)

		b.waitFor("initial seven rows", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7"]`)
		b.evaluate(`document.getElementById('task-list-search-toggle').click(); var search=document.getElementById('task-list-search-input'); search.value='login'; search.dispatchEvent(new Event('input')); 'searched'`)
		b.waitFor("only matching rows including eighth task", visible, `["login-first","login-extra"]`)
		b.evaluate(`search.value=''; search.dispatchEvent(new Event('input')); window.previewRowWidth=document.querySelector('.task-list-row').getBoundingClientRect().width; window.previewHeight=document.querySelector('.task-list-group').getBoundingClientRect().height; document.querySelector('.task-list-more').focus(); 'ready to browse'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "text": "\r", "windowsVirtualKeyCode": 13}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.waitFor("Browse all focuses first task", `document.activeElement.dataset.taskListRow || ''`, "login-first")
		b.waitFor("show more reveals all nine rows", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7","login-extra","unrelated-extra"]`)
		b.waitFor("Show less precedes browse hint outside scroller", `(()=>{var group=document.querySelector('.task-list-group'),footer=group.querySelector('.task-list-browse-controls');return String(footer.matches('button.task-list-done') && Math.abs(footer.getBoundingClientRect().width-group.getBoundingClientRect().width)<2 && getComputedStyle(footer).display==='flex' && Array.from(footer.children).map(el=>el.textContent.trim()).join(' ')==='Show less' && footer.previousElementSibling.matches('.task-list-more') && !group.querySelector('summary .task-list-done') && footer.getBoundingClientRect().top>=group.querySelector('.task-list-rows').getBoundingClientRect().bottom && getComputedStyle(group.querySelector('.task-list-more')).display==='none')})()`, "true")
		b.waitFor("browsing keeps preview height", `String(Math.abs(document.querySelector('.task-list-group').getBoundingClientRect().height-window.previewHeight)<2)`, "true")
		b.evaluateAwait(`Promise.all(document.querySelector(".task-list-search-slot").getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))).then(()=>"search animation settled")`)
		b.waitFor("browse scrollbar visible before scrolling", `(()=>{var rail=document.querySelector('.task-list-scroll-rail'),thumb=rail.firstElementChild;return String(rail.getBoundingClientRect().width===8 && thumb.getBoundingClientRect().height>=24 && getComputedStyle(rail).display==='block')})()`, "true")
		var railPoint struct{ X, Y float64 }
		if err := json.Unmarshal([]byte(b.evaluate(`(()=>{var r=document.querySelector('.task-list-scroll-rail').getBoundingClientRect();return JSON.stringify({X:r.left+4,Y:r.bottom-4})})()`)), &railPoint); err != nil {
			t.Fatal(err)
		}
		b.evaluate(`window.railOuterScroll=document.getElementById('task-list-groups').scrollTop;'capture outer scroll'`)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": railPoint.X, "y": railPoint.Y, "deltaX": 0, "deltaY": 40}, nil)
		b.waitFor("wheel over track scrolls only its category", `String(document.querySelector('.task-list-rows').scrollTop>0 && document.getElementById('task-list-groups').scrollTop===window.railOuterScroll)`, "true")
		b.evaluate(`var rows=document.querySelector('.task-list-rows');rows.scrollTop=rows.scrollHeight;var event=new WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:40});document.querySelector('.task-list-scroll-thumb').dispatchEvent(event);window.thumbWheelPrevented=event.defaultPrevented;'wheel over thumb at bottom'`)
		b.waitFor("thumb wheel stays contained at boundary", `String(window.thumbWheelPrevented && document.getElementById('task-list-groups').scrollTop===window.railOuterScroll)`, "true")
		b.evaluate(`document.querySelector('.task-list-scroll-thumb').dispatchEvent(new WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:-1000}));'wheel thumb upwards'`)
		b.waitFor("thumb wheel returns to start", `String(document.querySelector('.task-list-rows').scrollTop===0)`, "true")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "button": "left", "clickCount": 1, "x": railPoint.X, "y": railPoint.Y}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "button": "left", "clickCount": 1, "x": railPoint.X, "y": railPoint.Y}, nil)
		b.waitFor("scrollbar track click scrolls category", `String(document.querySelector('.task-list-rows').scrollTop>0)`, "true")
		b.evaluate(`document.querySelector('.task-list-rows').scrollTop=0;'reset rail test'`)
		b.waitFor("browsing keeps task row width", `String(Math.abs(document.querySelector('.task-list-row').getBoundingClientRect().width-window.previewRowWidth)<1)`, "true")
		var footerPoint struct{ X, Y float64 }
		if err := json.Unmarshal([]byte(b.evaluate(`(()=>{var r=document.querySelector('.task-list-done').getBoundingClientRect();window.footerTop=r.top;window.sidebarScroll=document.getElementById('task-list-groups').scrollTop;return JSON.stringify({X:r.left+r.width/2,Y:r.top+r.height/2})})()`)), &footerPoint); err != nil {
			t.Fatal(err)
		}
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": footerPoint.X, "y": footerPoint.Y, "deltaX": 0, "deltaY": 40}, nil)
		b.waitFor("wheel over Done scrolls category without moving footer", `String(document.querySelector('.task-list-rows').scrollTop>0 && document.querySelector('.task-list-done').getBoundingClientRect().top===window.footerTop && document.getElementById('task-list-groups').scrollTop===window.sidebarScroll)`, "true")
		b.evaluate(`var footer=document.querySelector('.task-list-browse-controls');footer.dispatchEvent(new WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:10000}));window.endScroll=document.querySelector('.task-list-rows').scrollTop;window.edgeWheel=new WheelEvent('wheel',{bubbles:true,cancelable:true,deltaY:40});footer.dispatchEvent(window.edgeWheel);'at end'`)
		b.waitFor("footer wheel stays contained at category end", `String(window.edgeWheel.defaultPrevented && document.querySelector('.task-list-rows').scrollTop===window.endScroll && document.getElementById('task-list-groups').scrollTop===window.sidebarScroll)`, "true")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseWheel", "x": footerPoint.X, "y": footerPoint.Y, "deltaX": 0, "deltaY": -1000}, nil)
		b.waitFor("footer wheel scrolls back to start", `String(document.querySelector('.task-list-rows').scrollTop===0)`, "true")
		b.evaluate(`document.querySelector('.task-list-rows').scrollTop=50; 'scrolled'`)
		b.waitFor("category scrolls independently", `String(document.querySelector('.task-list-rows').scrollTop===50)`, "true")
		b.evaluate(`window.oldRows=document.querySelector('.task-list-rows'); htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); 'refresh'`)
		b.waitFor("refresh retains browsing position", `JSON.stringify({replaced:document.querySelector('.task-list-rows')!==window.oldRows,scroll:document.querySelector('.task-list-rows').scrollTop,browsing:document.querySelector('.task-list-group').hasAttribute('data-browsing')})`, `{"replaced":true,"scroll":50,"browsing":true}`)

		b.evaluate(`search.value='login'; search.dispatchEvent(new Event('input')); 'searched expanded group'`)
		b.waitFor("expanded group still excludes nonmatches", visible, `["login-first","login-extra"]`)
		b.evaluate(`search.value=''; search.dispatchEvent(new Event('input')); 'cleared'`)
		b.waitFor("clearing search preserves show more", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7","login-extra","unrelated-extra"]`)
		b.evaluate(`document.querySelector('.task-list-done').focus(); 'ready to finish'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "text": "\r", "windowsVirtualKeyCode": 13}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.waitFor("Done restores focus to Browse all", `String(document.activeElement===document.querySelector('.task-list-more') && getComputedStyle(document.activeElement).visibility==='visible')`, "true")
		b.waitFor("Done restores seven rows and keeps category open", `String(document.querySelector('.task-list-group').open && !document.querySelector('.task-list-group').hasAttribute('data-browsing') && document.querySelector('.task-list-rows').scrollTop===0)`, "true")
		b.waitFor("preview restored", visible, `["login-first","other-2","other-3","other-4","other-5","other-6","other-7"]`)
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 1200, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.evaluateAwait(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve('resized'))))`)
		b.evaluate(`document.querySelector('.task-list-more').click();if(document.getElementById('task-list-search-toggle').getAttribute('aria-expanded')!=='true') document.getElementById('task-list-search-toggle').click();document.getElementById('task-list-groups').style.marginTop='260px';'positioned'`)
		b.evaluateAwait(`new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve('layout settled'))))`)
		b.evaluateAwait(`Promise.all(document.querySelector(".task-list-search-slot").getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))).then(()=>"search animation settled")`)
		b.evaluate(`var row=document.querySelector('[data-task-list-row="other-6"]'),r=row.getBoundingClientRect(),area=document.querySelector('.task-list-rows').getBoundingClientRect();window.outerScroll=document.getElementById('task-list-groups').scrollTop;row.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,pointerId:9,clientX:r.left+30,clientY:r.top+10}));window.dispatchEvent(new PointerEvent('pointermove',{bubbles:true,cancelable:true,pointerId:9,clientX:area.left+30,clientY:area.bottom-4}));'dragging'`)
		b.waitFor("drag scrolls browsing category with empty search still open", `String(document.getElementById('task-list-search-toggle').getAttribute('aria-expanded')==='true' && document.querySelector('.task-list-rows').scrollTop>30 && document.getElementById('task-list-groups').scrollTop===window.outerScroll)`, "true")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 'cancelled'`)
		b.waitFor("drag cleanup in scroller", `String(!window.isDragging && !document.querySelector('[data-pointer-drag-placeholder]'))`, "true")

	})
}

func TestBrowserFunctional_TaskListContextMenu(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := models.Task{ID: "menu-task", ProjectID: "owner", Title: "Menu task", Category: models.CategoryBacklog, Status: models.StatusPending}
	var runs, merges atomic.Int32
	var revision atomic.Int32
	var running atomic.Bool
	var deleted atomic.Bool
	var rejectDelete atomic.Bool
	runReceived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			task := task
			if revision.Load() > 0 {
				task.Title = fmt.Sprintf("Updated task %d", revision.Load())
			}
			if running.Load() {
				task.Status = models.StatusRunning
			}
			menus := map[string]templ.Component{task.ID: components.TaskListMenuItems(task)}
			tasks := []models.Task{task}
			if deleted.Load() {
				tasks = nil
			}
			if err := layout.TaskListPanelContent(models.AllProjectsID, layout.TaskListGroups(tasks), menus).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/test-board", "/tasks":
			tasks := []models.Task{task}
			if deleted.Load() {
				tasks = nil
			}
			if err := TasksContent(&models.Project{ID: models.AllProjectsID}, tasks, nil, nil, "", "").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/menu-task/card/merge-options":
			if r.URL.Query().Get("project_id") != "owner" {
				t.Error("wrong merge options project")
			}
			if err := components.TaskCardMergeOptions(&task, task.ProjectID, true, true, nil, true).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/menu-task/run":
			if r.FormValue("project_id") != "owner" {
				t.Error("wrong Run project")
			}
			defer func() {
				select {
				case runReceived <- struct{}{}:
				default:
				}
			}()
			if runs.Add(1) > 1 {
				revision.Store(2)
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/tasks/menu-task":
			if r.Method != http.MethodDelete {
				t.Error("wrong Delete request")
			}
			if rejectDelete.Load() {
				w.WriteHeader(http.StatusConflict)
				return
			}
			deleted.Store(true)
			w.Header().Set("HX-Trigger", `{"taskDeleted":{"type":"task_deleted","task_id":"menu-task","project_id":"owner"}}`)
			if err := components.KanbanBoard(nil, models.AllProjectsID, "", "", nil, nil).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/menu-task/cancel":
			revision.Store(3)
			w.WriteHeader(http.StatusConflict)
		case "/tasks/menu-task/worktree/merge":
			if r.FormValue("project_id") != "owner" || r.FormValue("merge_source") != "task_card" || r.FormValue("merge_type") != "ff" {
				t.Error("wrong merge request")
			}
			merges.Add(1)
			w.Write([]byte(`<div id="unexpected-card">Card response</div>`))
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "owner", Name: "Owner"}}, models.AllProjectsID).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-context-menu", func(b *composerFocusCDP) {
		b.waitFor("sidebar loaded", `String(!!document.querySelector('[data-task-list-row="menu-task"]'))`, "true")
		b.evaluate(`document.querySelector('.task-list-group').open=true; 'open category'`)
		open := `(()=>{var row=document.querySelector('[data-task-list-row="menu-task"]');row.dispatchEvent(new MouseEvent('contextmenu',{bubbles:true,cancelable:true,clientX:innerWidth-5,clientY:innerHeight-5}));return 'opened'})()`
		b.evaluate(open)
		b.waitFor("shared Git actions loaded", `String(!!document.querySelector('[data-task-list-context-menu] [data-merge-type="ff"]'))`, "true")
		b.waitFor("menu fits viewport", `(()=>{var menu=document.querySelector('[data-task-list-context-menu] [data-menu-content]');var r=menu.getBoundingClientRect();return String(r.width>0 && r.left>=0 && r.right<=innerWidth && r.top>=0 && r.bottom<=innerHeight)})()`, "true")
		b.waitFor("Edit preserves All projects", `String(document.querySelector('[data-task-list-context-menu] a[hx-get]').getAttribute('href').includes('project_id=__all_projects__'))`, "true")
		b.waitFor("menu shown", `String(document.activeElement.matches('[data-task-list-context-menu] button[hx-post]'))`, "true")
		b.click(`[data-task-list-context-menu] button[hx-post]`)
		b.waitFor("Run closes menu", `String(!document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open'))`, "true")
		select {
		case <-runReceived:
		case <-time.After(10 * time.Second):
			t.Fatal("Run request was not sent")
		}
		if runs.Load() != 1 {
			t.Fatalf("Run requests = %d", runs.Load())
		}
		b.evaluate(open)
		b.waitFor("Git actions reloaded", `String(!!document.querySelector('[data-task-list-context-menu] [data-merge-type="ff"]'))`, "true")
		b.click(`[data-task-list-context-menu] [data-task-card-local-submenu] > button`)
		b.waitFor("Local submenu open", `String(!!document.querySelector('[data-menu-submenu-portaled="true"] [data-merge-type="ff"]'))`, "true")
		b.click(`[data-menu-submenu-portaled="true"] [data-merge-type="ff"]`)
		b.waitFor("merge completed", `String(!window._taskCardActionRequest && !document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open'))`, "true")
		if merges.Load() != 1 {
			t.Fatalf("merge requests = %d", merges.Load())
		}
		b.waitFor("card response does not replace sidebar or workspace", `String(!document.getElementById('unexpected-card') && !!document.querySelector('[data-task-list-row="menu-task"]'))`, "true")
		b.evaluate(`var row=document.querySelector('[data-task-list-row="menu-task"]');row.focus();row.dispatchEvent(new KeyboardEvent('keydown',{key:'F10',shiftKey:true,bubbles:true,cancelable:true})); 'opened'`)
		b.waitFor("keyboard opens menu", `String(document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open'))`, "true")
		b.waitFor("keyboard focus settled before Escape", `String(document.activeElement.matches('[data-task-list-context-menu] button[hx-post]'))`, "true")
		b.evaluate(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true})); 'closed'`)
		b.waitFor("Escape closes and restores row focus", `String(!document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open') && document.activeElement.matches('[data-task-list-row="menu-task"]'))`, "true")

		key := func(key string) {
			b.evaluate(fmt.Sprintf(`document.activeElement.dispatchEvent(new KeyboardEvent('keydown',{key:%q,bubbles:true,cancelable:true})); 'key'`, key))
		}
		b.evaluate(open)
		b.waitFor("first action focused", `document.activeElement.textContent.trim().slice(0,100)`, "Run")
		b.waitFor("Git actions loaded for keyboard", `String(!!document.querySelector('[data-task-list-context-menu] [data-task-card-local-submenu]'))`, "true")
		key("ArrowDown")
		b.waitFor("Edit reachable", `document.activeElement.textContent.trim().slice(0,100)`, "Edit")
		key("ArrowDown")
		b.waitFor("Local reachable", `document.activeElement.textContent.trim().slice(0,100)`, "Local")
		key("ArrowRight")
		b.waitFor("Local submenu entered", `document.activeElement.textContent.trim().slice(0,100)`, "Merge commit")
		b.evaluate(`document.activeElement.closest('ul').querySelector('[data-merge-type="ff"]').disabled=true; 'disabled'`)
		key("ArrowDown")
		b.waitFor("disabled action skipped", `document.activeElement.dataset.mergeType`, "rebase")
		key("ArrowUp")
		b.waitFor("up skips disabled action", `document.activeElement.dataset.mergeType`, "merge")
		key("ArrowLeft")
		b.waitFor("Local focus restored", `document.activeElement.textContent.trim().slice(0,100)`, "Local")
		key("End")
		b.waitFor("Delete reachable", `document.activeElement.textContent.trim().slice(0,100)`, "Delete")
		key("ArrowUp")
		b.waitFor("GitHub reachable", `document.activeElement.textContent.trim().slice(0,100)`, "GitHub")
		key("ArrowRight")
		b.waitFor("GitHub submenu entered", `document.activeElement.textContent.trim().slice(0,100)`, "Create PR")
		key("ArrowLeft")
		key("Home")
		b.waitFor("Home returns to first", `document.activeElement.textContent.trim().slice(0,100)`, "Run")
		key("Tab")
		b.waitFor("Tab stays inside menu", `String(document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open') && !!document.activeElement.closest('[data-task-list-context-menu]'))`, "true")
		key("Escape")
		b.waitFor("Escape releases focus after Tab", `String(!document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open') && document.activeElement.matches('[data-task-list-row="menu-task"]'))`, "true")

		for _, submenu := range []string{"local", "github"} {
			b.evaluate(open)
			b.waitFor("menu focus settled", `String(document.activeElement.matches('[data-task-list-context-menu] button[hx-post]'))`, "true")
			b.waitFor("submenu loaded", fmt.Sprintf(`String(!!document.querySelector('[data-task-list-context-menu] [data-task-card-%s-submenu] > button'))`, submenu), "true")
			b.evaluate(fmt.Sprintf(`document.querySelector('[data-task-list-context-menu] [data-task-card-%s-submenu] > button').focus(); 'focused'`, submenu))
			key("ArrowRight")
			b.waitFor("submenu focus settled", `String(!!document.activeElement.closest('[data-menu-submenu-portaled="true"]'))`, "true")
			key("Escape")
			b.waitFor("submenu Escape restores task row", `String(!document.querySelector('[data-task-list-context-menu]').hasAttribute('data-kanban-menu-open') && document.activeElement.matches('[data-task-list-row="menu-task"]') && !document.querySelector('[data-menu-submenu-portaled="true"]'))`, "true")
		}

		// A shared close must release a refresh deferred while the menu was open.
		b.evaluate(open)
		b.waitFor("menu focused", `document.activeElement.textContent.trim().slice(0,100)`, "Run")
		revision.Store(1)
		b.evaluate(`window.auditDeferred=false;document.body.addEventListener('htmx:beforeSwap',e=>{if(e.detail.target.id==='task-list-groups')window.auditDeferred=!e.detail.shouldSwap});htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); 'refresh'`)
		b.waitFor("refresh deferred", `String(window.auditDeferred)`, "true")
		b.evaluate(`window.closeKanbanMenu(null,false); 'closed'`)
		b.waitFor("shared close releases deferred refresh", `document.querySelector('[data-task-list-row="menu-task"] .task-list-title').textContent`, "Updated task 1")
		b.evaluate(open)
		b.waitFor("Run focused for conflict", `document.activeElement.textContent.trim().slice(0,100)`, "Run")
		b.click(`[data-task-list-context-menu] button[hx-post]`)
		b.waitFor("Run conflict still refreshes sidebar", `document.querySelector('[data-task-list-row="menu-task"] .task-list-title').textContent`, "Updated task 2")
		running.Store(true)
		b.evaluate(`htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); 'refresh'`)
		b.waitFor("running state loaded", `String(!!document.querySelector('[data-task-list-menu]').content.querySelector('[hx-post$="/cancel"]'))`, "true")
		b.evaluate(open)
		b.waitFor("Cancel ready", `String(!!document.querySelector('[data-task-list-context-menu] [hx-post$="/cancel"]') && document.activeElement.matches('[data-task-list-context-menu] button[hx-post]'))`, "true")
		b.click(`[data-task-list-context-menu] [hx-post$="/cancel"]`)
		b.waitFor("Cancel confirmation", `String(document.getElementById("app_confirm_modal").open)`, "true")
		b.click(`#app_confirm_modal [data-confirm-accept]`)
		b.waitFor("Cancel conflict still refreshes sidebar", `document.querySelector('[data-task-list-row="menu-task"] .task-list-title').textContent`, "Updated task 3")

		b.evaluate(`htmx.ajax('GET','/test-board',{target:'#main-content',swap:'innerHTML'}); 'board'`)
		b.waitFor("board card loaded", `String(!!document.getElementById('task-menu-task'))`, "true")
		rejectDelete.Store(true)
		b.evaluate(`window.deleteRejected=false;document.body.addEventListener('htmx:responseError',e=>{if(e.detail.xhr.status===409)window.deleteRejected=true}); 'watch'`)
		b.click(`#task-menu-task [hx-delete]`)
		b.waitFor("delete rejected", `String(window.deleteRejected)`, "true")
		b.waitFor("failed delete keeps both entries", `String(!!document.getElementById('task-menu-task') && !!document.querySelector('[data-task-list-row="menu-task"]'))`, "true")
		rejectDelete.Store(false)
		b.evaluate(open)
		b.waitFor("menu focused before Delete", `String(document.activeElement.matches('[data-task-list-context-menu] button[hx-post]'))`, "true")
		b.click(`[data-task-list-context-menu] [hx-delete]`)
		b.waitFor("deleted task removed from sidebar", `String(!document.querySelector('[data-task-list-row="menu-task"]'))`, "true")
		b.waitFor("deleted card removed from board", `String(!document.getElementById('task-menu-task'))`, "true")
		if !deleted.Load() {
			t.Fatal("Delete request was not sent")
		}
		deleted.Store(false)
		b.evaluate(`htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); htmx.ajax('GET','/test-board',{target:'#main-content',swap:'innerHTML'}); 'restore for card'`)
		b.waitFor("card deletion row ready", `String(!!document.querySelector('[data-task-list-row="menu-task"]'))`, "true")
		b.waitFor("card deletion card ready", `String(!!document.getElementById('task-menu-task'))`, "true")
		b.click(`#task-menu-task [hx-delete]`)
		b.waitFor("card delete removes both entries", `String(!document.querySelector('[data-task-list-row="menu-task"]') && !document.getElementById('task-menu-task') && !!document.getElementById('kanban-board'))`, "true")
		// Restore the fixture, then exercise a deletion arriving from another session.
		deleted.Store(false)
		b.evaluate(`htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); htmx.ajax('GET','/test-board',{target:'#main-content',swap:'innerHTML'}); 'restore'`)
		b.waitFor("remote deletion row ready", `String(!!document.querySelector('[data-task-list-row="menu-task"]'))`, "true")
		b.waitFor("remote deletion card ready", `String(!!document.getElementById('task-menu-task'))`, "true")
		deleted.Store(true)
		b.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_deleted',task_id:'menu-task',project_id:'owner'}})); 'deleted'`)
		b.waitFor("remote deletion removes both entries", `String(!document.querySelector('[data-task-list-row="menu-task"]') && !document.getElementById('task-menu-task'))`, "true")
		b.evaluate(`htmx.swap('#main-content','<div id="task-detail-content" data-task-id="menu-task">Deleted thread</div>',{swapStyle:'innerHTML'}); window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'task_deleted',task_id:'menu-task',project_id:'owner'}})); 'detail deleted'`)
		b.waitFor("deleted detail navigates to scoped tasks", `String(location.pathname==='/tasks' && new URLSearchParams(location.search).get('project_id')==='__all_projects__' && !document.getElementById('task-detail-content') && !!document.getElementById('kanban-board'))`, "true")
		b.waitFor("Delete preserves workspace", `String(!!document.getElementById('main-content') && !document.getElementById('unexpected-delete-board'))`, "true")
	})
}

func TestBrowserFunctional_TaskListDragDrop(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var moved, reordered, reject atomic.Bool
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			tasks := []models.Task{
				{ID: "drag-a", ProjectID: "owner", Title: "First task", Category: models.CategoryBacklog, Status: models.StatusPending},
				{ID: "drag-b", ProjectID: "other", Title: "Second task", Category: models.CategoryBacklog, Status: models.StatusPending},
			}
			if reordered.Load() {
				tasks[0], tasks[1] = tasks[1], tasks[0]
			}
			if moved.Load() {
				for i := range tasks {
					if tasks[i].ID == "drag-a" {
						tasks[i].Category = models.CategoryCompleted
					}
				}
			}
			menus := map[string]templ.Component{}
			for _, task := range tasks {
				menus[task.ID] = components.TaskListMenuItems(task)
			}
			if err := layout.TaskListPanelContent(models.AllProjectsID, layout.TaskListGroups(tasks), menus).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/tasks/drag-a/reorder", "/tasks/drag-a/category":
			requests.Add(1)
			if r.Method != http.MethodPatch || r.URL.Query().Get("project_id") != models.AllProjectsID {
				t.Errorf("wrong drag request: %s %v", r.Method, r.Form)
			}
			if reject.Load() {
				w.WriteHeader(http.StatusConflict)
				return
			}
			if r.URL.Path == "/tasks/drag-a/reorder" {
				if r.FormValue("after_task_id") != "drag-b" {
					t.Errorf("wrong relative anchor: %v", r.Form)
				}
				reordered.Store(true)
			} else {
				if r.FormValue("category") != "completed" {
					t.Errorf("wrong category: %v", r.Form)
				}
				moved.Store(true)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "owner", Name: "Owner"}, {ID: "other", Name: "Other"}}, models.AllProjectsID).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-drag", func(b *composerFocusCDP) {
		b.waitFor("backlog loaded", `String(!!document.querySelector('[data-task-list-group="Backlog"]'))`, "true")
		b.evaluate(`document.querySelector('[data-task-list-group="Backlog"]').open=true; 'open backlog'`)

		b.waitFor("drag rows loaded", `String(!!document.querySelector('[data-task-list-row="drag-b"]'))`, "true")
		// A press that never crosses the drag threshold must release deferred updates too.
		b.evaluate(`document.body.addEventListener('htmx:beforeSwap',e=>{if(e.detail.target.id==='task-list-groups')window.pressUpdateDeferred=!e.detail.shouldSwap}); 'watch'`)
		for _, end := range []string{
			`document.querySelector('[data-task-list-row="drag-a"]').dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:21}))`,
			`document.querySelector('[data-task-list-row="drag-a"]').dispatchEvent(new PointerEvent('pointercancel',{bubbles:true,pointerId:21}))`,
			`window.dispatchEvent(new Event('blur'))`,
			`document.dispatchEvent(new KeyboardEvent('keydown',{bubbles:true,key:'Escape'}))`,
		} {
			b.evaluate(`window.pressUpdateDeferred=false;document.querySelector('[data-task-list-row="drag-a"]').dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,pointerId:21})); 'pressed'`)
			reordered.Store(true)
			b.evaluate(`htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); 'refresh'`)
			b.waitFor("refresh deferred during ordinary press", `String(window.pressUpdateDeferred)`, "true")
			b.waitFor("pressed row retained", `Array.from(document.querySelectorAll('[data-task-list-row]')).map(r=>r.dataset.taskListRow).join(',')`, "drag-a,drag-b")
			b.evaluate(end + `; 'released'`)
			b.waitFor("release applies deferred update", `Array.from(document.querySelectorAll('[data-task-list-row]')).map(r=>r.dataset.taskListRow).join(',')`, "drag-b,drag-a")
			if requests.Load() != 0 {
				t.Fatal("ordinary press sent a mutation")
			}
			reordered.Store(false)
			b.evaluate(`htmx.trigger(document.getElementById('task-list-groups'),'taskListRefresh'); 'reset'`)
			b.waitFor("fixture reset", `Array.from(document.querySelectorAll('[data-task-list-row]')).map(r=>r.dataset.taskListRow).join(',')`, "drag-a,drag-b")
		}
		start := `(()=>{var row=document.querySelector('[data-task-list-row="drag-a"]'),r=row.getBoundingClientRect();row.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,pointerId:9,clientX:r.left+30,clientY:r.top+10}));window.dispatchEvent(new PointerEvent('pointermove',{bubbles:true,cancelable:true,pointerId:9,clientX:r.left+40,clientY:r.top+20}));return 'started'})()`
		mouse := func(kind, selector string, bottom bool) {
			var point struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			}
			expr := fmt.Sprintf(`(()=>{var r=document.querySelector(%q).getBoundingClientRect();return JSON.stringify({x:r.left+30,y:%s})})()`, selector, "r.top+10")
			if bottom {
				expr = fmt.Sprintf(`(()=>{var r=document.querySelector(%q).getBoundingClientRect();return JSON.stringify({x:r.left+30,y:r.bottom-2})})()`, selector)
			}
			if err := json.Unmarshal([]byte(b.evaluate(expr)), &point); err != nil {
				t.Fatal(err)
			}
			b.call("Input.dispatchMouseEvent", map[string]any{"type": kind, "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1}, nil)
		}
		// Position the source lower down to exercise an upward panel without covering the reorder target.
		b.evaluate(`document.getElementById('task-list-groups').style.paddingTop='260px';window.dragRowTop=document.querySelector('[data-task-list-row="drag-b"]').getBoundingClientRect().top; 'positioned'`)
		mouse("mousePressed", `[data-task-list-row="drag-a"]`, false)
		mouse("mouseMoved", `[data-task-list-row="drag-b"]`, false)
		b.waitFor("drag exposes empty category targets", `String(!document.querySelector('.task-list-drop-targets').hidden && window.isDragging)`, "true")
		b.waitFor("panel floats above original entry without shifting rows", `(()=>{var panel=document.querySelector('.task-list-drop-targets'),slot=document.querySelector('[data-pointer-drag-placeholder]');return String(panel.parentElement===document.body && panel.getBoundingClientRect().bottom < slot.getBoundingClientRect().top && Math.abs(document.querySelector('[data-task-list-row="drag-b"]').getBoundingClientRect().top-window.dragRowTop)<2)})()`, "true")
		b.waitFor("stacked targets match dragged entry width", `(()=>{var targets=Array.from(document.querySelectorAll('[data-task-list-drop-category]')),row=document.querySelector('.task-list-dragging').getBoundingClientRect();return String(targets.every((t,i)=>t.getBoundingClientRect().width>=row.width-1 && (!i || t.getBoundingClientRect().top>=targets[i-1].getBoundingClientRect().bottom)) && targets.length===4 && document.querySelector('.task-list-drop-targets > .drop-heading').textContent==='Move to')})()`, "true")
		b.waitFor("actual row follows pointer", `String(document.querySelector('[data-task-list-row="drag-a"]').parentElement===document.body && document.querySelector('[data-task-list-row="drag-a"]').style.transform.includes('translate3d') && !!document.querySelector('#task-list-groups [data-pointer-drag-placeholder]'))`, "true")
		b.evaluate(`document.querySelector('[data-task-list-row="drag-b"]').dispatchEvent(new MouseEvent('mouseover',{bubbles:true}));window.openVibelyTooltip.show(document.querySelector('[data-task-list-row="drag-b"]'),'Task tooltip',false); 'hover during drag'`)
		b.evaluateAwait(`new Promise(resolve=>setTimeout(()=>resolve('waited for hover delay'),550))`)
		b.waitFor("task previews suppressed throughout drag", `String(document.querySelector('.task-list-hovercard').hidden && document.getElementById('ov-shared-tooltip').hidden)`, "true")
		mouse("mouseMoved", `[data-task-list-row="drag-b"]`, true)
		mouse("mouseReleased", `[data-task-list-row="drag-b"]`, true)
		b.waitFor("relative reorder persisted", `Array.from(document.querySelectorAll('[data-task-list-row]')).map(r=>r.dataset.taskListRow).join(',')`, "drag-b,drag-a")
		if !reordered.Load() {
			t.Fatal("reorder was not sent")
		}
		b.waitFor("drag cleanup", `String(!window.isDragging && !document.querySelector('.task-list-dragging') && !document.querySelector('[data-pointer-drag-placeholder]'))`, "true")
		b.waitFor("row appearance restored after drop", `String(document.querySelector('[data-task-list-row="drag-a"]').closest('#task-list-groups') !== null && !document.querySelector('[data-task-list-row="drag-a"]').hasAttribute('style'))`, "true")
		b.evaluate(`document.getElementById('task-list-groups').style.paddingTop='';'reset position'`)
		b.evaluate(start)
		b.waitFor("panel opens below near viewport top", `String(document.querySelector('.task-list-drop-targets').getBoundingClientRect().top > document.querySelector('[data-pointer-drag-placeholder]').getBoundingClientRect().bottom)`, "true")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 'cancelled'`)
		b.waitFor("Escape cancels drag", `String(!window.isDragging && document.querySelector('.task-list-drop-targets').hidden)`, "true")
		if requests.Load() != 1 {
			t.Fatal("cancelled drag made a request")
		}
		reject.Store(true)
		drop := `(()=>{var r=document.querySelector('[data-task-list-drop-category="completed"]').getBoundingClientRect();window.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:9,clientX:r.left+20,clientY:r.top+10}));return 'dropped'})()`
		b.evaluate(`window.dragFailed=false;window.dragFailureToast=false;var originalToast=window.showToast;window.showToast=function(message,...args){if(message.includes('move task'))window.dragFailureToast=true;return originalToast(message,...args)};document.body.addEventListener('htmx:responseError',()=>window.dragFailed=true);'watch'`)
		b.evaluate(start)
		b.evaluate(drop)
		b.waitFor("rejected drag handled", `String(window.dragFailed)`, "true")
		b.waitFor("rejected drag explains failure", `String(window.dragFailureToast)`, "true")
		b.waitFor("failed move keeps category", `document.querySelector('[data-task-list-row="drag-a"]').closest('[data-task-list-group]').dataset.taskListGroup`, "Backlog")
		// Wait for the request's finally handler to release subsequent dragging.
		b.waitFor("failed move refresh complete", `String(!document.querySelector('#task-list-groups.htmx-request'))`, "true")
		reject.Store(false)
		b.evaluate(start)
		b.waitFor("second drag ready", `String(window.isDragging)`, "true")
		b.evaluate(drop)
		b.waitFor("move to previously empty category persisted", `document.querySelector('[data-task-list-row="drag-a"]').closest('[data-task-list-group]').dataset.taskListGroup`, "Completed")
		if !moved.Load() {
			t.Fatal("category move was not sent")
		}
		b.waitFor("drag did not navigate", `location.pathname`, "/")
	})
}

func TestBrowserFunctional_AllProjectsCompletedMoveScope(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, surface := range []string{"board", "sidebar"} {
		t.Run(surface, func(t *testing.T) {
			var moved atomic.Bool
			project := models.Project{ID: models.AllProjectsID, Name: "All projects"}
			tasks := func() []models.Task {
				category := models.CategoryCompleted
				if moved.Load() {
					category = models.CategoryBacklog
				}
				return []models.Task{{ID: "merged-task", ProjectID: "owner", Title: "Merged task", Category: category, Status: models.StatusCompleted, MergeStatus: models.MergeStatusMerged}}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				switch r.URL.Path {
				case "/tasks/merged-task/category":
					if r.URL.Query().Get("project_id") != models.AllProjectsID {
						http.Error(w, "task not found: missing workspace scope", http.StatusNotFound)
						return
					}
					if r.Method != http.MethodPatch || r.FormValue("category") != "backlog" {
						t.Errorf("unexpected move: %s %v", r.Method, r.Form)
						w.WriteHeader(400)
						return
					}
					moved.Store(true)
					_ = components.KanbanBoard(tasks(), project.ID, "", "", nil, nil).Render(r.Context(), w)
				case "/tasks/sidebar":
					_ = layout.TaskListPanelContent(project.ID, layout.TaskListGroups(tasks())).Render(r.Context(), w)
				case "/tasks":
					_ = components.KanbanBoard(tasks(), project.ID, "", "", nil, nil).Render(r.Context(), w)
				case "/":
					_ = Tasks([]models.Project{{ID: "owner", Name: "Owner"}}, &project, tasks(), nil, nil, "", "").Render(r.Context(), w)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "all-projects-move-"+surface, func(b *composerFocusCDP) {
				b.waitFor("sidebar loaded", `String(!!document.querySelector('[data-task-list-row="merged-task"]'))`, "true")
				if surface == "board" {
					b.evaluate(`(()=>{var card=document.getElementById('task-merged-task'),zone=document.querySelector('.category-drop-zone[data-category="backlog"]');var r=card.getBoundingClientRect(),z=zone.getBoundingClientRect();card.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,pointerId:9,clientX:r.left+30,clientY:r.top+20}));window.dispatchEvent(new PointerEvent('pointermove',{bubbles:true,cancelable:true,pointerId:9,clientX:z.left+20,clientY:z.top+20}));window.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:9,clientX:z.left+20,clientY:z.top+20}));return 'dropped'})()`)
					b.waitFor("board moved", `String(!!document.querySelector('[data-kanban-category="backlog"] #task-merged-task'))`, "true")
				} else {
					b.evaluate(`(()=>{var row=document.querySelector('[data-task-list-row="merged-task"]'),r=row.getBoundingClientRect();row.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,button:0,pointerId:9,clientX:r.left+30,clientY:r.top+10}));window.dispatchEvent(new PointerEvent('pointermove',{bubbles:true,cancelable:true,pointerId:9,clientX:r.left+40,clientY:r.top+20}));var target=document.querySelector('[data-task-list-drop-category="backlog"]').getBoundingClientRect();window.dispatchEvent(new PointerEvent('pointerup',{bubbles:true,pointerId:9,clientX:target.left+20,clientY:target.top+10}));return 'dropped'})()`)
				}
				b.waitFor("persisted move reaches sidebar", `String(!!document.querySelector('[data-task-list-group="Backlog"] [data-task-list-row="merged-task"]'))`, "true")
				if !moved.Load() {
					t.Fatal("move not persisted")
				}
			})
		})
	}
}

func TestBrowserFunctional_TaskListBrowseScrollPerProject(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			projectID := r.URL.Query().Get("project_id")
			var tasks []models.Task
			for i := 0; i < 30; i++ {
				tasks = append(tasks, models.Task{ID: fmt.Sprintf("%s-%d", projectID, i), ProjectID: projectID, Title: fmt.Sprintf("Task %d", i), Category: models.CategoryCompleted})
			}
			if err := layout.TaskListPanelContent(projectID, layout.TaskListGroups(tasks)).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/":
			if err := layout.Base("Tasks", []models.Project{{ID: "first", Name: "First"}, {ID: "second", Name: "Second"}}, "first").Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "task-list-project-scroll", func(b *composerFocusCDP) {
		b.waitFor("first project loaded", `String(!!document.querySelector('[data-task-list-row="first-0"]'))`, "true")
		b.evaluate(`document.querySelector('.task-list-more').click();document.querySelector('.task-list-rows').scrollTop=180;'browsing first'`)
		b.waitFor("first position", `String(document.querySelector('.task-list-rows').scrollTop)`, "180")
		b.waitFor("sidebar and nested rows clear scrollbars", `(function(){var outer=document.getElementById('task-list-groups'),inner=document.querySelector('.task-list-rows'),row=inner.querySelector('.task-list-row'),panel=document.getElementById('task-list-panel');var o=outer.getBoundingClientRect(),i=inner.getBoundingClientRect(),r=row.getBoundingClientRect(),p=panel.getBoundingClientRect();return String(Math.abs(i.right-r.right)<2 && document.querySelector('.task-list-scroll-rail').getBoundingClientRect().left>=r.right && document.querySelector('.task-list-scroll-rail').getBoundingClientRect().right<=o.right && o.right-i.right>=8 && o.right<=p.right && outer.scrollWidth<=outer.clientWidth && inner.scrollWidth<=inner.clientWidth)})()`, "true")
		switchTo := func(project string, scroll int, browsing bool) {
			// Use the actual project synchronization path: the request scope changes
			// before the outgoing rows are replaced by the HTTP response.
			b.evaluate(fmt.Sprintf(`history.replaceState({},'', '/?project_id=%s');window.dispatchEvent(new PopStateEvent('popstate'));'switched'`, project))
			b.waitFor("incoming rows and independent position", `JSON.stringify({project:document.querySelector('.task-list-group').dataset.taskListProjectScope,scroll:document.querySelector('.task-list-rows').scrollTop,browsing:document.querySelector('.task-list-group').hasAttribute('data-browsing')})`, fmt.Sprintf(`{"project":%q,"scroll":%d,"browsing":%t}`, project, scroll, browsing))
		}
		switchTo("second", 0, false)
		b.evaluate(`document.querySelector('.task-list-more').click(); 'browse second'`)
		b.evaluate(`document.querySelector('.task-list-rows').scrollTop=70;'browsing second'`)
		b.waitFor("second position", `String(document.querySelector('.task-list-rows').scrollTop)`, "70")
		switchTo("first", 180, true)
		switchTo("second", 70, true)
		switchTo(models.AllProjectsID, 0, false)
		b.evaluate(`document.querySelector('.task-list-more').click(); 'browse all projects'`)
		switchTo("first", 180, true)
		b.evaluate(`document.querySelector('.task-list-done').click(); 'finish first'`)
		switchTo(models.AllProjectsID, 0, true)
		switchTo("first", 0, false)
		switchTo("second", 70, true)
	})
}

func TestBrowserFunctional_TaskListAdaptivePreviews(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			var tasks []models.Task
			for _, category := range []models.TaskCategory{models.CategoryActive, models.CategoryCompleted, models.CategoryBacklog, models.CategoryScheduled} {
				for i := 0; i < 100; i++ {
					if category == models.CategoryActive && i >= 20 {
						continue
					}
					tasks = append(tasks, models.Task{ID: fmt.Sprintf("%s-%d", category, i), ProjectID: "project", Title: fmt.Sprintf("%s task %d", category, i), Category: category, Status: models.StatusPending})
				}
			}
			_ = layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(r.Context(), w)
		case "/":
			_ = layout.Base("Tasks", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "adaptive-task-previews", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 1200, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("all categories loaded", `String(document.querySelectorAll('.task-list-group').length)`, "4")
		b.evaluate(`window.groupFor=name=>document.querySelector('[data-task-list-group="'+name+'"]');window.previewCount=name=>groupFor(name).querySelectorAll('[data-task-list-row]:not([data-task-list-extra])').length;'helpers'`)
		b.waitFor("default categories and larger previews", `String(groupFor('Active').open && groupFor('Completed').open && !groupFor('Backlog').open && !groupFor('Scheduled').open && previewCount('Active')>7 && previewCount('Completed')>7)`, "true")
		b.evaluate(`window.initialCount=previewCount('Active');groupFor('Backlog').querySelector('summary').click();'open backlog'`)
		b.waitFor("opening backlog shares space", `String(previewCount('Active')<window.initialCount && previewCount('Backlog')>=3 && groupFor('Completed').open)`, "true")
		b.evaluate(`window.threeCount=previewCount('Active');groupFor('Scheduled').querySelector('summary').click();'open scheduled'`)
		b.waitFor("four open categories share space", `String(previewCount('Active')<window.threeCount && previewCount('Scheduled')>=3 && document.getElementById('task-list-groups').scrollHeight<=document.getElementById('task-list-groups').clientHeight+1)`, "true")
		b.evaluate(`window.previewHeight=groupFor('Active').getBoundingClientRect().height;groupFor('Active').querySelector('.task-list-more').click();'browse'`)
		b.waitFor("browse preserves allocation", `String(groupFor('Active').hasAttribute('data-browsing') && Math.abs(groupFor('Active').getBoundingClientRect().height-window.previewHeight)<2)`, "true")
		b.evaluate(`groupFor('Scheduled').querySelector('summary').click();groupFor('Backlog').querySelector('summary').click();'close other categories'`)
		b.waitFor("space returns while browsing", `String(previewCount('Active')===window.initialCount && groupFor('Active').hasAttribute('data-browsing'))`, "true")
		b.evaluate(`groupFor('Completed').querySelector('summary').click();'give active all available space'`)
		b.waitFor("all active tasks fit while browsing", `String(!groupFor('Active').hasAttribute('data-has-overflow') && groupFor('Active').hasAttribute('data-browsing'))`, "true")
		b.evaluate(`groupFor('Active').querySelector('.task-list-done').focus();'focus done'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Enter", "code": "Enter", "text": "\r", "windowsVirtualKeyCode": 13}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13}, nil)
		b.waitFor("Done focuses header when Browse all is hidden", `String(document.activeElement===groupFor('Active').querySelector('summary') && !groupFor('Active').hasAttribute('data-browsing') && groupFor('Active').open)`, "true")
		b.evaluate(`groupFor('Completed').querySelector('summary').click();'restore completed'`)
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1280, "height": 500, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.evaluate(`groupFor('Scheduled').open=true;groupFor('Backlog').open=true;'all open in short window'`)
		b.waitFor("minimum previews scroll instead of disappearing", `String(['Active','Completed','Backlog','Scheduled'].every(name=>previewCount(name)>=3) && document.getElementById('task-list-groups').scrollHeight>document.getElementById('task-list-groups').clientHeight)`, "true")
	})
}

func TestBrowserFunctional_SidebarContextSearch(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/tasks/sidebar":
			_ = layout.TaskListPanelContent("project", layout.TaskListGroups([]models.Task{{ID: "search-one", Title: "Find sidebar bug", Category: models.CategoryActive}, {ID: "search-two", Title: "Review keyboard", Category: models.CategoryActive}})).Render(r.Context(), w)
		case "/":
			_ = layout.Base("Search", []models.Project{{ID: "project", Name: "Project"}}, "project").Render(r.Context(), w)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "sidebar-context-search", func(b *composerFocusCDP) {
		b.waitFor("task rows", `String(!!document.querySelector('[data-task-list-row="search-one"]'))`, "true")
		b.call("Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-reduced-motion", "value": "reduce"}}}, nil)
		b.waitFor("closed search respects reduced motion", `getComputedStyle(document.querySelector('.task-list-search')).transitionDuration`, "0s")
		b.evaluate(`document.getElementById('task-list-search-toggle').click();'open with reduced motion'`)
		b.waitFor("open search respects reduced motion", `String(getComputedStyle(document.querySelector('.task-list-search')).transitionDuration==='0s' && getComputedStyle(document.getElementById('task-list-search-input')).transitionDuration==='0s')`, "true")
		b.evaluate(`document.getElementById('task-list-search-toggle').click();'close with reduced motion'`)
		b.waitFor("closed search has no visibility delay", `String(getComputedStyle(document.querySelector('.task-list-search')).visibility==='hidden' && getComputedStyle(document.querySelector('.task-list-search')).transitionDelay==='0s')`, "true")
		b.call("Emulation.setEmulatedMedia", map[string]any{"features": []map[string]string{{"name": "prefers-reduced-motion", "value": "no-preference"}}}, nil)

		b.evaluate(`window.search=document.getElementById('task-list-search-input');window.toggle=document.getElementById('task-list-search-toggle');toggle.click();'open'`)
		b.waitFor("input opens in sidebar", `String(document.activeElement===search && !!search.closest('#task-list-panel') && toggle.getAttribute('aria-expanded')==='true')`, "true")
		b.waitFor("search fits the sidebar", `(()=>{var panel=document.getElementById('task-list-panel').getBoundingClientRect(),field=search.getBoundingClientRect();return String(field.width>60 && field.left>=panel.left && field.right<=panel.right && field.top>=panel.top)})()`, "true")
		b.evaluateAwait(`Promise.all(document.querySelector('.task-list-search-slot').getAnimations({subtree:true}).map(a=>a.finished.catch(()=>{}))).then(()=>"settled")`)
		var togglePoint struct{ X, Y float64 }
		if err := json.Unmarshal([]byte(b.evaluate(`(()=>{var r=toggle.getBoundingClientRect();return JSON.stringify({X:r.left+r.width/2,Y:r.top+r.height/2})})()`)), &togglePoint); err != nil {
			t.Fatal(err)
		}
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "button": "left", "clickCount": 1, "x": togglePoint.X, "y": togglePoint.Y}, nil)
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "button": "left", "clickCount": 1, "x": togglePoint.X, "y": togglePoint.Y}, nil)
		b.waitFor("second magnifier click closes search", `toggle.getAttribute('aria-expanded')`, "false")
		b.waitFor("list keeps search space during field exit", `String(document.querySelector('.task-list-search-slot').getBoundingClientRect().height===38)`, "true")
		b.evaluateAwait(`Promise.all(document.querySelector('.task-list-search-slot').getAnimations({subtree:true}).map(a=>a.finished.catch(()=>{}))).then(()=>"closed")`)
		b.waitFor("search stays closed after exit", `String(toggle.getAttribute('aria-expanded')==='false' && document.querySelector('.task-list-search-slot').getBoundingClientRect().height===0)`, "true")
		b.evaluate(`toggle.click();'reopen'`)

		b.evaluate(`window.searchClick='';document.addEventListener('click',function(e){window.searchClick=e.target.closest('[data-task-list-row]')?.dataset.taskListRow || '';e.preventDefault();e.stopImmediatePropagation();},{capture:true,once:true});'capture click'`)
		b.evaluateAwait(`Promise.all(document.querySelector(".task-list-search-slot").getAnimations({subtree:true}).map(animation=>animation.finished.catch(()=>{}))).then(()=>"search animation settled")`)
		var point struct{ X, Y float64 }
		if err := json.Unmarshal([]byte(b.evaluate(`(()=>{var r=document.querySelector('[data-task-list-row="search-one"]').getBoundingClientRect();window.rowTop=r.top;return JSON.stringify({X:r.left+r.width/2,Y:r.top+r.height/2})})()`)), &point); err != nil {
			t.Fatal(err)
		}
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "button": "left", "clickCount": 1, "x": point.X, "y": point.Y}, nil)
		b.waitFor("search stays open and row stationary during press", `String(toggle.getAttribute('aria-expanded')==='true' && document.querySelector('[data-task-list-row="search-one"]').getBoundingClientRect().top===window.rowTop)`, "true")
		b.call("Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "button": "left", "clickCount": 1, "x": point.X, "y": point.Y}, nil)
		b.waitFor("click reaches original task", `window.searchClick`, "search-one")
		b.waitFor("empty search collapses outside", `toggle.getAttribute('aria-expanded')`, "false")
		b.evaluate(`toggle.click();search.value='sidebar';search.dispatchEvent(new Event('input'));document.body.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true}));document.body.dispatchEvent(new PointerEvent('pointerup',{bubbles:true}));'filter'`)
		b.waitFor("query remains visible", `String(toggle.getAttribute('aria-expanded')==='true' && document.querySelector('[data-task-list-row="search-two"]').hidden)`, "true")
		b.evaluate(`document.getElementById('task-list-search-clear').click();'clear'`)
		b.waitFor("clear restores tasks and focus", `String(!document.querySelector('[data-task-list-row="search-two"]').hidden && document.activeElement===search)`, "true")
		b.evaluate(`document.getElementById('main-content').innerHTML='<div id="models-container"></div>';window.syncAppearanceSettings();'settings'`)
		b.waitFor("settings search scope", `search.getAttribute('aria-label')`, "Search settings")
		b.evaluate(`document.querySelector('#appearance-settings-nav details').open=false;toggle.click();search.value='themes';search.dispatchEvent(new Event('input'));'settings filter'`)
		b.waitFor("settings child match expands parent", `String(search.getBoundingClientRect().height>0 && document.querySelector('#appearance-settings-nav details').open && !document.querySelector('#appearance-settings-nav [data-settings-link="themes"]').hidden && document.querySelector('#appearance-settings-nav [data-settings-link="layout"]').hidden && !document.querySelector('[data-task-list-row="search-two"]').hidden)`, "true")
		b.evaluate(`search.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));'escape'`)
		b.waitFor("escape restores settings and focus", `String(!document.querySelector('#appearance-settings-nav details').open && !document.querySelector('#appearance-settings-nav [data-settings-link="layout"]').hidden && document.activeElement===toggle && toggle.getAttribute('aria-expanded')==='false')`, "true")
		b.evaluate(`document.getElementById('main-content').innerHTML='<div id="changes-content" data-task-id="diff-task"><div id="tab-changes"><div id="diff-viewer"><div data-diff-index="0" data-diff-path="src/alpha.go"></div><div data-diff-index="1" data-diff-path="docs/beta.md"></div></div></div></div>';document.documentElement.classList.add('changes-explorer-active');window.syncChangesExplorer(true);'changes'`)
		b.waitFor("file scope", `search.getAttribute('aria-label')`, "Filter files")
		b.evaluate(`toggle.click();search.value='alpha';search.dispatchEvent(new Event('input'));'files filter'`)
		b.waitFor("menu filters actual explorer", `String(search.getBoundingClientRect().height>0 && document.querySelectorAll('[data-explorer-path]').length===1 && document.querySelector('[data-explorer-path]').dataset.explorerPath==='src/alpha.go' && document.documentElement.classList.contains('changes-explorer-active'))`, "true")
		b.evaluate(`document.getElementById('task-list-search-clear').click();'clear files'`)
		b.waitFor("clear restores files", `String(document.querySelectorAll('[data-explorer-path]').length)`, "2")
		b.evaluate(`document.getElementById('main-content').replaceChildren();window.syncAppearanceSettings();'tasks again'`)
		b.waitFor("return restores task scope", `String(search.getAttribute('aria-label')==='Search tasks' && toggle.getAttribute('aria-expanded')==='false')`, "true")
	})
}
