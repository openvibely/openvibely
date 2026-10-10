package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTasksContentHasMobileSafeShell(t *testing.T) {
	project := &models.Project{ID: "project-1", Name: "Project"}
	var buf bytes.Buffer
	if err := TasksContent(project, nil, nil, nil, "", "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render tasks content: %v", err)
	}

	body := buf.String()
	for _, want := range []string{
		"overflow-x-hidden",
		"overflow-y-auto",
		"min-h-0",
		"flex items-center justify-between mb-6 flex-shrink-0",
		"btn btn-ghost btn-sm btn-square",
		`aria-label="Add Task"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected tasks page shell to contain %q, got %s", want, body)
		}
	}
	for _, unwanted := range []string{
		"w-full sm:w-auto",
		"sm:flex-row",
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("expected tasks page add button header to follow models placement without %q, got %s", unwanted, body)
		}
	}
}
