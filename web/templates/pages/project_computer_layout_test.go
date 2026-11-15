package pages

import (
	"bytes"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestComputerPreviewLeadsDedicatedPanel(t *testing.T) {
	var out bytes.Buffer
	if err := ProjectComputerContent("project", "task").Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	preview := strings.Index(html, "data-computer-preview")
	toggle := strings.Index(html, "data-task-computer-preference")
	controls := strings.Index(html, "data-computer-dialog")
	if preview < 0 || toggle < preview || controls < preview {
		t.Fatal("preview must precede toggle and controls")
	}
	for _, unwanted := range []string{`data-computer-action="takeover"`, `data-computer-action="release"`, `data-computer-expand>`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("unexpected viewer button %s", unwanted)
		}
	}
	out.Reset()
	if err := TaskDetailContent(&models.Task{ID: "task", ProjectID: "project"}, nil, nil, nil, nil, nil, nil, "details", nil).Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	_, details, _ := strings.Cut(out.String(), `id="tab-details"`)
	details, _, _ = strings.Cut(details, `id="tab-schedules"`)
	if strings.Contains(details, "data-task-computer-preference") {
		t.Fatal("computer setting remains in Details")
	}
}

func TestComputerPermissionsAvailableWithoutTask(t *testing.T) {
	var out bytes.Buffer
	if err := ProjectComputerContent("project").Render(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	// Check rendered controls, not their selectors in the embedded script.
	for _, control := range []string{"<div data-computer-grants data-grants-project=\"project\"", "data-computer-grants-open aria-haspopup=\"dialog\"", "id=\"computer-grants-picker\"", "<div data-options=\"grants\" data-computer-grant-list>"} {
		if !strings.Contains(out.String(), control) {
			t.Fatalf("project page missing %s", control)
		}
	}
}

func TestComputerProjectPageHasNoDesktopRuntime(t *testing.T) {
	var project, task bytes.Buffer
	if err := ProjectComputerContent("project").Render(t.Context(), &project); err != nil {
		t.Fatal(err)
	}
	if err := ProjectComputerContent("project", "task").Render(t.Context(), &task); err != nil {
		t.Fatal(err)
	}
	for _, viewer := range []string{"data-computer-preview", "data-computer-dialog", "new RFB(", "scoped('/ensure')", "window.openVibelyLive.computer("} {
		if strings.Contains(project.String(), viewer) {
			t.Fatalf("project page includes desktop runtime: %s", viewer)
		}
		if !strings.Contains(task.String(), viewer) {
			t.Fatalf("task page lost desktop runtime: %s", viewer)
		}
	}
	for _, scope := range []struct{ html, want string }{{project.String(), `data-grants-task=""`}, {task.String(), `data-grants-task="task"`}} {
		if !strings.Contains(scope.html, scope.want) {
			t.Fatalf("incorrect grant scope: want %s", scope.want)
		}
	}
}
