package components

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestKanbanBoardRefreshPreservesSelectionAndOpenMenus(t *testing.T) {
	var buf bytes.Buffer
	tasks := []models.Task{{ID: "selected-task", ProjectID: "project-1", Title: "Selected", Category: models.CategoryBacklog, Status: models.StatusPending}}
	if err := KanbanBoard(tasks, "project-1", "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render kanban board: %v", err)
	}
	html := buf.String()
	for _, required := range []string{
		`data-kanban-menu-key="column-backlog"`,
		`data-kanban-menu-key="column-completed"`,
		`data-kanban-menu-key="task-selected-task"`,
		`data-kanban-menu-trigger`,
		`aria-expanded="false"`,
		`data-kanban-menu-content`,
		`<button type="button" tabindex="0"`,
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("kanban refresh state requires rendered contract %q", required)
		}
	}

	for _, unsupported := range []string{`aria-haspopup="menu"`, `role="menu"`} {
		if strings.Contains(html, unsupported) {
			t.Fatalf("kanban dropdown must not claim unsupported ARIA menu semantics %q", unsupported)
		}
	}

	if strings.Contains(html, `<a tabindex="0"`) {
		t.Fatal("column HTMX actions must use native keyboard-operable buttons, not focusable anchors without href")
	}

	layoutSource, err := os.ReadFile(filepath.Join("..", "layout", "base.templ"))
	if err != nil {
		t.Fatalf("read base template: %v", err)
	}
	for _, required := range []string{"savedKanbanInteraction", "restoreKanbanInteraction", "selectedTaskIDs", "focusKey", "aria-expanded"} {
		if !bytes.Contains(layoutSource, []byte(required)) {
			t.Fatalf("kanban refresh script must preserve %q", required)
		}
	}
}

func TestActiveColumnContent_GroupsOnlyRunningTasksInProgress(t *testing.T) {
	tasks := []models.Task{
		{ID: "running", ProjectID: "new-project", Title: "Running task", Category: models.CategoryActive, Status: models.StatusRunning},
		{ID: "pending", ProjectID: "new-project", Title: "Pending task", Category: models.CategoryActive, Status: models.StatusPending},
		{ID: "queued", ProjectID: "new-project", Title: "Queued task", Category: models.CategoryActive, Status: models.StatusQueued},
	}

	var buf bytes.Buffer
	if err := activeColumnContent(tasks, "new-project", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render active column: %v", err)
	}
	html := buf.String()
	inProgressStart := strings.Index(html, ">In Progress</h4>")
	queuedStart := strings.Index(html, ">Queued</h4>")
	if inProgressStart < 0 || queuedStart < 0 || queuedStart <= inProgressStart {
		t.Fatalf("active column is missing ordered In Progress and Queued sections")
	}
	inProgressHTML := html[inProgressStart:queuedStart]
	queuedHTML := html[queuedStart:]
	if !strings.Contains(inProgressHTML, `id="task-running"`) {
		t.Fatal("running task missing from In Progress section")
	}
	if strings.Contains(inProgressHTML, `id="task-pending"`) || strings.Contains(inProgressHTML, `id="task-queued"`) {
		t.Fatal("pending or queued task rendered in In Progress section")
	}
	if !strings.Contains(queuedHTML, `id="task-pending"`) || !strings.Contains(queuedHTML, `id="task-queued"`) {
		t.Fatal("pending and queued tasks must render in Queued section")
	}
}

func TestKanbanColumn_DropdownTriggersUseLabelForDesktopWebviewCompatibility(t *testing.T) {
	var buf bytes.Buffer
	err := KanbanColumn([]models.Task{}, "project-1", models.CategoryBacklog, "", "", nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render backlog column: %v", err)
	}
	html := buf.String()

	for _, want := range []string{
		`<label tabindex="0" class="btn btn-xs btn-ghost`,
		`title="More actions"`,
		`onclick="handleDropdownToggle(event)"`,
		`data-kanban-menu-trigger`,
		`aria-label="More actions"`,
		`aria-expanded="false"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected backlog kebab trigger to contain %q for stable dropdown focus behavior", want)
		}
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost`) {
		t.Fatal("unexpected <button> dropdown trigger in backlog column")
	}

	buf.Reset()
	err = KanbanColumn([]models.Task{}, "project-1", models.CategoryCompleted, "", "", nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render completed column: %v", err)
	}
	html = buf.String()
	for _, want := range []string{
		`<label tabindex="0" class="btn btn-xs btn-ghost`,
		`title="More actions"`,
		`onclick="handleDropdownToggle(event)"`,
		`data-kanban-menu-trigger`,
		`aria-label="More actions"`,
		`aria-expanded="false"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected completed kebab trigger to contain %q for stable dropdown focus behavior", want)
		}
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost`) {
		t.Fatal("unexpected <button> dropdown trigger in completed column")
	}
}

func TestKanbanColumnSharedActionsAndActiveFIFO(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryBacklog, models.CategoryActive, models.CategoryCompleted} {
		body := renderKanbanColumnForCategoryTest(t, category, []models.Task{{ID: "task", Category: category, Status: models.StatusPending}})
		for _, want := range []string{`data-kanban-select`, `data-kanban-action="delete"`, `data-kanban-progress`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s missing %s", category, want)
			}
		}
		hasFilters := strings.Contains(body, `data-kanban-filter`)
		hasSort := strings.Contains(body, `/sort?`)
		if category == models.CategoryActive {
			if hasFilters || hasSort {
				t.Fatal("Active must preserve the full FIFO queue")
			}
			if !strings.Contains(body, `data-kanban-action="cancel"`) {
				t.Fatal("missing Stop")
			}
		} else if !hasFilters || !hasSort {
			t.Fatalf("%s needs filters and sorting", category)
		}
	}
	body := renderKanbanColumnForCategoryTest(t, models.CategoryCompleted, nil)
	for _, action := range []string{"merge", "ff", "squash", "rebase", "pr"} {
		if !strings.Contains(body, `data-kanban-action="`+action+`"`) {
			t.Fatalf("missing bulk %s", action)
		}
	}
	body = renderKanbanColumnForTest(t, nil)
	if !strings.Contains(body, `data-kanban-action="run"`) || strings.Contains(body, "hx-confirm") {
		t.Fatal("Run must execute directly")
	}
	for _, priority := range []int{4, 3, 2, 1} {
		if !strings.Contains(body, PriorityLabel(priority)) {
			t.Fatal("missing priority filter")
		}
	}
}

func renderKanbanColumnForTest(t *testing.T, tasks []models.Task) string {
	return renderKanbanColumnForCategoryTest(t, models.CategoryBacklog, tasks)
}

func renderKanbanColumnForCategoryTest(t *testing.T, category models.TaskCategory, tasks []models.Task) string {
	t.Helper()
	var buf bytes.Buffer
	if err := KanbanColumn(tasks, "project-1", category, "", "", nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render %s column: %v", category, err)
	}
	return buf.String()
}

func TestKanbanBoardReservedCapacityWaitUsesQueuedLane(t *testing.T) {
	tasks := []models.Task{
		{ID: "executing", Category: models.CategoryActive, Status: models.StatusRunning},
		{ID: "waiting", Category: models.CategoryActive, Status: models.StatusRunning, WorkerCapacityQueued: true},
	}
	running, queued := filterRunningTasks(tasks), filterPendingTasks(tasks)
	if len(running) != 1 || running[0].ID != "executing" || len(queued) != 1 || queued[0].ID != "waiting" {
		t.Fatalf("running=%+v queued=%+v", running, queued)
	}
	if state := taskCardDisplayState(tasks[1]); state.Key != "queued" {
		t.Fatalf("waiting card state = %+v", state)
	}
	tasks[1].WorkerCapacityQueued = false
	if len(filterRunningTasks(tasks)) != 2 || len(filterPendingTasks(tasks)) != 0 {
		t.Fatal("admitted task did not move to running")
	}
}

func TestKanbanEmptyColumnControls(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryBacklog, models.CategoryActive, models.CategoryCompleted} {
		body := renderKanbanColumnForCategoryTest(t, category, nil)
		if !strings.Contains(body, "min-h-11") {
			t.Fatal("empty header must retain its height")
		}
		if category == models.CategoryActive {
			if strings.Contains(body, `data-kanban-menu-key="column-active"`) {
				t.Fatal("empty Active must hide its menu")
			}
		} else if !strings.Contains(body, `data-kanban-select disabled`) {
			t.Fatalf("%s must disable empty selection", category)
		}
	}
}
