package pages

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_KanbanSelectionFiltersAndBatchResults(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "batch-project", Name: "Batch"}
	tasks := []models.Task{
		{ID: "one", Title: "One", ProjectID: project.ID, Category: models.CategoryCompleted, Status: models.StatusCompleted, WorktreeBranch: "one", Priority: 4},
		{ID: "two", Title: "Two", ProjectID: project.ID, Category: models.CategoryCompleted, Status: models.StatusFailed, WorktreeBranch: "two", Priority: 2},
		{ID: "merged", Title: "Merged", ProjectID: project.ID, Category: models.CategoryCompleted, Status: models.StatusCompleted, WorktreeBranch: "merged", MergeStatus: models.MergeStatusMerged},
		{ID: "no-branch", Title: "No branch", ProjectID: project.ID, Category: models.CategoryCompleted, Status: models.StatusCompleted},
		{ID: "backlog", Title: "Backlog", ProjectID: project.ID, Category: models.CategoryBacklog, Status: models.StatusPending},
		{ID: "active", Title: "Active", ProjectID: project.ID, Category: models.CategoryActive, Status: models.StatusRunning},
	}
	var mu sync.Mutex
	var mutations []string
	result := make(chan string, 4)
	runner := `<script>
window.addEventListener('DOMContentLoaded', async function() {
 function check(value, message) { if (!value) throw new Error(message); }
 const col = name => document.querySelector('[data-kanban-category="'+name+'"]');
 const filter = (name, value) => col('completed').querySelector('[data-kanban-filter="'+name+'"][data-value="'+value+'"]');
 const action = name => col('completed').querySelector('[data-kanban-action="'+name+'"]');
 const select = name => col(name).querySelector('[data-kanban-select]');
 const visible = () => Array.from(col('completed').querySelectorAll('.card[data-task-id]')).filter(c => !c.hidden);
 const log = () => fetch('/mutations').then(r => r.text());
 const wait = async predicate => { for(let i=0;i<200;i++) { if(predicate()) return; await new Promise(r=>setTimeout(r,10)); } throw new Error('wait timed out'); };
 try {
  const menu = col('completed').querySelector('[data-kanban-menu-key="column-completed"]');
  menu.querySelector('[data-kanban-menu-trigger]').click();
  await wait(() => menu.getAttribute('data-kanban-menu-positioning') !== 'true');
  const sort = Array.from(menu.querySelectorAll('button')).find(b => b.textContent.includes('Sort by'));
  sort.focus();
  const panel = document.querySelector('[data-task-card-submenu-portaled="true"]');
  check(panel, 'Sort submenu opens');
  let sortRequested = false;
  document.body.addEventListener('htmx:beforeRequest', event => { if ((event.detail.requestConfig.path || '').includes('/completed/sort')) sortRequested = true; });
  panel.querySelector('button[hx-post]').click();
  await wait(() => sortRequested);
  await new Promise(r=>setTimeout(r,100));
  window.closeKanbanMenu(null, false);
  select('completed').click();
  check(window.kanbanSelection.size === 4, 'select all Completed');
  check(col('completed').querySelectorAll('[data-kanban-checkbox]:not(.hidden)').length === 4, 'show checkboxes');
  const checkbox = document.querySelector('#task-two input[type=checkbox]'); checkbox.click();
  check(window.kanbanSelection.size === 3 && !window.kanbanSelection.has('two'), 'deselect a card');
  select('backlog').click(); check(window.kanbanSelection.size === 1 && window.kanbanSelection.has('backlog'), 'selection stays in one column');
  select('active').click(); check(window.kanbanSelection.size === 1 && window.kanbanSelection.has('active'), 'select Active');
  check(!col('active').querySelector('[data-kanban-filter]'), 'Active has no filters');
  check(!col('active').querySelector('[hx-post*="/sort"]'), 'Active has no sort');
  const currentMenu = col('completed').querySelector('[data-kanban-menu-key="column-completed"]');
  currentMenu.querySelector('[data-kanban-menu-trigger]').click();
  await wait(() => currentMenu.getAttribute('data-kanban-menu-positioning') !== 'true');
  Array.from(currentMenu.querySelectorAll('button')).find(b => b.textContent.trim().startsWith('Filter')).focus();
  const filterPanel = document.querySelector('[data-task-card-submenu-portaled="true"]');
  filterPanel.querySelector('summary').click();
  filterPanel.querySelector('[data-kanban-filter="status"][data-value="completed"]').click();
  window.closeKanbanMenu(null, false);
  filter('merge','unmerged').click();
  check(window.kanbanSelection.size === 0, 'filter clears selection');
  check(visible().length === 1 && visible()[0].dataset.taskId === 'one', 'combined filters');
  check(document.getElementById('task-two').getClientRects().length === 0, 'hidden cards must not occupy space');
  await window.kanbanBatch(action('pr'));
  check((await log()) === 'one:pr', 'filtered batch scope');
  await wait(() => !document.querySelector('#kanban-board.htmx-request'));
  await new Promise(r=>setTimeout(r,50));
  check(visible().length === 1, 'filter survives refresh');
  col('completed').querySelector('[data-kanban-filter="clear"]').click();
  await window.kanbanBatch(action('pr'));
  check((await log()) === 'one:pr,one:pr,merged:pr', 'skip existing closed PR and ineligible branch');
  await new Promise(r=>setTimeout(r,100));
  check(col('completed').querySelector('[data-kanban-progress]').textContent.includes('2 skipped'), 'PR skipped results');
  await window.kanbanBatch(action('ff'));
  check((await log()).endsWith('one:ff,two:ff'), 'FF skips merged and no branch');
  await new Promise(r=>setTimeout(r,100));
  check(col('completed').querySelector('[data-kanban-progress]').textContent.includes('1 failed'), 'HTTP 200 failure toast is a failure');
  check(col('completed').querySelector('[data-kanban-progress]').textContent.includes('2 skipped'), 'merge skips');
  select('completed').click();
  for(const id of ['two','merged','no-branch']) document.querySelector('#task-'+id+' input[type=checkbox]').click();
  check(window.kanbanSelection.size===1, 'subset selected');
  await window.kanbanBatch(action('ff'));
  check((await log()).endsWith('two:ff,one:ff'), 'selected batch scope');
  await new Promise(r=>setTimeout(r,100));
  const beforeStop = await log();
  const pending = window.kanbanBatch(action('ff'));
  col('completed').querySelector('[data-kanban-progress] button').click();
  await pending;
  check((await log()) === beforeStop + ',one:ff', 'Stop finishes current operation and leaves remaining tasks untouched');
  check(col('completed').querySelector('[data-kanban-progress]').textContent.includes('Stopped'), 'stopped progress');
  await new Promise(r=>setTimeout(r,100));
  // Hold each mutation so navigation and progress interactions happen between results.
  const originalFetch = window.fetch, originalAjax = window.htmx.ajax;
  const releases = [];
  let finalRefreshes = 0;
  window.fetch = (url, options) => String(url).includes('/worktree/')
    ? new Promise(resolve => releases.push(() => resolve(new Response('<div>done</div>', {status:200}))))
    : originalFetch(url, options);
  window.htmx.ajax = (method, url, options) => {
    if (method === 'GET' && url === '/tasks?project_id=batch-project') finalRefreshes++;
    return originalAjax(method, url, options);
  };
  const lifecycle = window.kanbanBatch(action('ff'), [
    {id:'one', title:'First'}, {id:'one', title:'Second'}, {id:'one', title:'Third'}
  ]);
  await wait(() => releases.length === 1);
  releases.shift()();
  await wait(() => releases.length === 1);
  const host = col('completed').querySelector('[data-kanban-progress]');
  const details = host.querySelector('details'), summary = details.querySelector('summary');
  details.open = true; summary.focus();
  releases.shift()();
  await wait(() => releases.length === 1);
  check(host.querySelector('details') === details && details.open, 'progress preserves expanded Details node');
  check(document.activeElement === summary, 'progress preserves keyboard focus');
  check(details.querySelectorAll('div').length === 2, 'progress appends results');
  const oldBoard = document.getElementById('kanban-board');
  const returnedBoard = oldBoard.cloneNode(true);
  returnedBoard.querySelectorAll('[data-kanban-progress]').forEach(el => el.replaceChildren());
  const away = document.createElement('div');
  oldBoard.replaceWith(away);
  document.dispatchEvent(new Event('htmx:afterSwap'));
  // A different project's board must not receive this batch's results.
  const foreignBoard = returnedBoard.cloneNode(true);
  foreignBoard.querySelectorAll('[data-kanban-category]').forEach(el => el.dataset.projectId = 'foreign');
  away.replaceWith(foreignBoard);
  document.dispatchEvent(new Event('htmx:afterSwap'));
  check(!foreignBoard.querySelector('[data-kanban-progress]').textContent, 'results stay project scoped');
  foreignBoard.replaceWith(returnedBoard);
  document.dispatchEvent(new Event('htmx:afterSwap'));
  check(col('completed').querySelector('[data-kanban-progress]').textContent.includes('2/3'), 'return restores current progress');
  releases.shift()();
  await lifecycle;
  check(col('completed').querySelector('[data-kanban-progress]').textContent.includes('3/3'), 'returned board receives final progress');
  check(finalRefreshes === 1, 'returned board receives authoritative completion refresh');
  window.fetch = originalFetch; window.htmx.ajax = originalAjax;
  await fetch('/result?status=pass', {method:'POST'});
 } catch(error) { await fetch('/result?status='+encodeURIComponent(error.stack), {method:'POST'}); }
});
</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch {
		case r.URL.Path == "/result":
			result <- r.URL.Query().Get("status")
		case r.URL.Path == "/mutations":
			mu.Lock()
			fmt.Fprint(w, strings.Join(mutations, ","))
			mu.Unlock()
		case r.URL.Path == "/tasks" || r.URL.Path == "/tasks/completed/sort":
			var out bytes.Buffer
			if r.Header.Get("HX-Request") == "true" {
				_ = components.KanbanBoard(tasks, project.ID, "", "", nil, nil).Render(context.Background(), &out)
			} else {
				_ = Tasks([]models.Project{project}, &project, tasks, nil, nil, "", "").Render(context.Background(), &out)
			}
			fmt.Fprint(w, strings.Replace(out.String(), "</head>", runner+"</head>", 1))
		case strings.HasSuffix(r.URL.Path, "/card/merge-options"):
			id := strings.Split(r.URL.Path, "/")[2]
			for _, task := range tasks {
				if task.ID == id {
					state := components.TaskCardMergeMenuState{LocalEligible: task.WorktreeBranch != "", FastForwardEligible: task.WorktreeBranch != "", PullEligible: task.WorktreeBranch != ""}
					if id == "two" {
						state.PullRequest = &models.TaskPullRequest{PRURL: "https://example.com/pr/1", PRState: "closed"}
					}
					_ = components.TaskCardMergeSubmenus(&task, project.ID, state).Render(context.Background(), w)
				}
			}
		case strings.Contains(r.URL.Path, "/worktree/"):
			_ = r.ParseForm()
			if r.FormValue("project_id") != project.ID || r.FormValue("merge_source") != "task_card" {
				http.Error(w, "bad scope", 400)
				return
			}
			id := strings.Split(r.URL.Path, "/")[2]
			action := r.FormValue("merge_type")
			mu.Lock()
			mutations = append(mutations, id+":"+action)
			mu.Unlock()
			if id == "two" {
				w.Header().Set("HX-Trigger", `{"openvibelyToast":{"status":"failed","message":"Cannot fast-forward"}}`)
			}
			fmt.Fprint(w, "<div>refreshed</div>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cmd := exec.Command(chrome, "--headless=new", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check", "--window-size=1280,900", "--user-data-dir="+filepath.Join(t.TempDir(), "chrome"), server.URL+"/tasks?project_id="+project.ID)
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatal(err)
	}
	defer stopBrowserProcess(cmd)
	select {
	case outcome := <-result:
		if outcome != "pass" {
			t.Fatal(outcome)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("browser timed out")
	}
}
