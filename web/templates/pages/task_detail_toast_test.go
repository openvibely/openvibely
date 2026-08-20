package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskDetailContent_DoesNotRenderPageLocalToastSystem(t *testing.T) {
	task := &models.Task{
		ID:        "task-toast-1",
		Title:     "Task toast",
		ProjectID: "project-1",
		Status:    models.StatusCompleted,
		Category:  models.CategoryCompleted,
	}

	var buf bytes.Buffer
	if err := TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "details", nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render task detail content: %v", err)
	}
	body := buf.String()

	for _, forbidden := range []string{
		"_showToastListenerRegistered",
		"addEventListener('showToast'",
		"Listen for showToast events from HTMX responses",
		"event.detail.type === 'success'",
		"toast.innerHTML = icon + '<span>' + event.detail.message + '</span>'",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("task detail must not render duplicated toast logic containing %q", forbidden)
		}
	}
}
