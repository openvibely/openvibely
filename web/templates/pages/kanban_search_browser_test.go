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
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_KanbanHeaderSearch(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	project := models.Project{ID: "search-project", Name: "Search"}
	tasks := []models.Task{
		{ID: "alpha", Title: "Alpha login fix", Prompt: "repair oauth", ProjectID: project.ID, Category: models.CategoryBacklog, Status: models.StatusPending},
		{ID: "beta", Title: "Beta docs", Prompt: "write guide", ProjectID: project.ID, Category: models.CategoryBacklog, Status: models.StatusPending},
		{ID: "gamma", Title: "Gamma", Prompt: "OAuth refresh", ProjectID: project.ID, Category: models.CategoryCompleted, Status: models.StatusCompleted},
		{ID: "delta", Title: "Delta", ProjectID: project.ID, Category: models.CategoryActive, Status: models.StatusRunning},
	}
	result := make(chan string, 1)
	runner := `<script>
window.addEventListener('DOMContentLoaded', async function() {
 function check(value, message) { if (!value) throw new Error(message); }
 const wait = async predicate => { for(let i=0;i<100;i++) { if(predicate()) return; await new Promise(r=>setTimeout(r,10)); } throw new Error('wait timed out'); };
 const visible = () => Array.from(document.querySelectorAll('#kanban-board .card[data-task-id]')).filter(c => !c.hidden).map(c => c.dataset.taskId).sort().join(',');
 const type = value => { input.value = value; input.dispatchEvent(new Event('input', {bubbles:true})); };
 const search = document.querySelector('[data-page-header] [data-kanban-search]');
 const toggle = search && search.querySelector('[data-kanban-search-toggle]');
 const input = search && search.querySelector('[data-kanban-search-input]');
 const addTask = Array.from(document.querySelectorAll('[data-page-header] a')).find(a => a.textContent.includes('Add Task'));
 try {
  check(search && toggle && input, 'search renders in header');
  check(search.nextElementSibling === addTask, 'search sits next to Add Task');
  check(search.dataset.open === 'false' && search.getBoundingClientRect().width <= 33, 'collapsed to icon by default');
  toggle.click();
  check(search.dataset.open === 'true' && document.activeElement === input, 'click opens and focuses input');
  await wait(() => search.getBoundingClientRect().width > 100);
  type('oauth');
  check(visible() === 'alpha,gamma', 'filters by title and prompt across columns, got ' + visible());
  check(document.querySelector('[data-kanban-category="backlog"] [data-kanban-count]').textContent === '1', 'counts reflect search');
  type('DELTA');
  check(visible() === 'delta', 'case-insensitive and includes active column');
  document.querySelector('h2').dispatchEvent(new MouseEvent('mousedown', {bubbles:true}));
  input.blur();
  check(search.dataset.open === 'false', 'clicking elsewhere collapses');
  await wait(() => search.getBoundingClientRect().width <= 33);
  check(visible() === 'delta' && search.dataset.active === 'true', 'term keeps filtering while collapsed');
  toggle.click();
  input.dispatchEvent(new KeyboardEvent('keydown', {key:'Escape', bubbles:true}));
  check(search.dataset.open === 'false' && input.value === '' && visible() === 'alpha,beta,delta,gamma', 'Escape clears and collapses');
  check(document.activeElement === toggle, 'Escape returns focus to icon');
  await fetch('/result?status=pass', {method:'POST'});
 } catch(error) { await fetch('/result?status='+encodeURIComponent(error.stack), {method:'POST'}); }
});
</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/result":
			result <- r.URL.Query().Get("status")
		case "/tasks":
			var out bytes.Buffer
			_ = Tasks([]models.Project{project}, &project, tasks, nil, nil, "created_desc", "completed_desc").Render(context.Background(), &out)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, strings.Replace(out.String(), "</head>", runner+"</head>", 1))
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
