package pages

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func renderNewTaskOptionsForSwarmTest(t *testing.T) string {
	t.Helper()

	var buf bytes.Buffer
	if err := NewTaskOptions(nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render tasks content: %v", err)
	}
	return buf.String()
}

func TestNewTaskOptions_SwarmOptionsHiddenByDefault(t *testing.T) {
	body := renderNewTaskOptionsForSwarmTest(t)

	if !strings.Contains(body, `name="swarm_mode" id="create-task-swarm-mode-toggle"`) {
		t.Fatal("expected swarm mode toggle to expose the create-task visibility binding id")
	}
	if !strings.Contains(body, `id="create-task-swarm-options" class="hidden space-y-3"`) {
		t.Fatal("expected swarm sub-options container to be hidden by default")
	}

	optionsStart := strings.Index(body, `id="create-task-swarm-options"`)
	if optionsStart == -1 {
		t.Fatal("expected swarm sub-options container to render")
	}
	for _, field := range []string{
		"Swarm planning starts when the parent task becomes Active",
		"Max workers",
		"Worker isolation",
		"Reviewer enabled",
		"Merger enabled",
	} {
		fieldIndex := strings.Index(body, field)
		if fieldIndex == -1 {
			t.Fatalf("expected %q to remain rendered inside the hidden swarm options section", field)
		}
		if fieldIndex < optionsStart {
			t.Fatalf("expected %q to render inside create-task-swarm-options", field)
		}
	}
}

func TestTasksContentOmitsObsoleteCreationDialog(t *testing.T) {
	var buf bytes.Buffer
	if err := TasksContent(&models.Project{ID: "project-1"}, nil, nil, nil, "", "").Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{"new_task_modal", "openNewTaskModal", "data-new-task-form", "create-task-files-hidden"} {
		if strings.Contains(buf.String(), old) {
			t.Fatalf("obsolete task dialog retained: %s", old)
		}
	}
	if !strings.Contains(buf.String(), "/tasks/new?project_id=project-1") {
		t.Fatal("missing dedicated task creation link")
	}
}

func TestNewTaskOptions_HiddenSwarmOptionsPreserveSubmittedFieldValues(t *testing.T) {
	body := renderNewTaskOptionsForSwarmTest(t)

	if !strings.Contains(body, "Swarm planning starts when the parent task becomes Active") || !strings.Contains(body, "Choose Backlog to defer the planner") {
		t.Fatal("expected swarm options to explain category-driven planner start semantics")
	}

	swarmOptionControls := []string{
		`name="swarm_max_workers" class="input input-bordered" min="1" max="8" value="3"`,
		`name="swarm_worker_isolation" class="select select-bordered"`,
		`name="swarm_reviewer_enabled" value="true" class="checkbox checkbox-sm" checked`,
		`name="swarm_merger_enabled" value="true" class="checkbox checkbox-sm" checked`,
	}
	for _, control := range swarmOptionControls {
		if !strings.Contains(body, control) {
			t.Fatalf("expected preserved swarm option control %q", control)
		}
	}

	for _, name := range []string{
		"swarm_max_workers",
		"swarm_worker_isolation",
		"swarm_reviewer_enabled",
		"swarm_merger_enabled",
	} {
		disabledPattern := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `"[^>]*\sdisabled(?:[\s=>]|$)`)
		if disabledPattern.MatchString(body) {
			t.Fatalf("expected hidden swarm option %s to stay enabled so its value is preserved while hidden", name)
		}
	}
}

func TestTaskDetailEditForm_DoesNotRenderSwarmCreateControls(t *testing.T) {
	task := &models.Task{
		ID:        "task-1",
		Title:     "Task",
		ProjectID: "project-1",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail content: %v", err)
	}
	body := buf.String()

	for _, unexpected := range []string{
		`name="swarm_mode"`,
		`name="swarm_autonomous_planner"`,
		`name="swarm_max_workers"`,
		`name="swarm_worker_isolation"`,
		`name="swarm_reviewer_enabled"`,
		`name="swarm_merger_enabled"`,
		`id="create-task-swarm-options"`,
	} {
		if strings.Contains(body, unexpected) {
			t.Fatalf("task edit form should not render create-task swarm control %q", unexpected)
		}
	}
}
