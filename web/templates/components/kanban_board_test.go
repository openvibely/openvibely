package components

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

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

	if !strings.Contains(html, `<label tabindex="0" class="btn btn-xs btn-ghost`) ||
		!strings.Contains(html, `title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected backlog kebab trigger to use <label> for stable dropdown focus behavior")
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
	if !strings.Contains(html, `<label tabindex="0" class="btn btn-xs btn-ghost`) ||
		!strings.Contains(html, `title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected completed kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost`) {
		t.Fatal("unexpected <button> dropdown trigger in completed column")
	}
}
