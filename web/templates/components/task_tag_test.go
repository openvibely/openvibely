package components

import (
	"bytes"
	"context"
	"github.com/openvibely/openvibely/internal/models"
	"strings"
	"testing"
)

func TestCustomTagRenderingEscapesText(t *testing.T) {
	tag := models.TaskTag(`<script>alert("tag")</script>`)
	if TagLabel(tag) != string(tag) || TagBadgeClass(tag) != "badge-neutral" {
		t.Fatal("custom tag should have a readable neutral fallback")
	}
	var out bytes.Buffer
	task := models.Task{ID: "task", Title: "Task", ProjectID: "default", Category: models.CategoryBacklog, Tag: tag}
	if err := TaskCard(task, "default", "backlog", nil, nil).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), string(tag)) || !strings.Contains(out.String(), "&lt;script&gt;") {
		t.Fatal("tag not escaped in badge")
	}
	out.Reset()
	if err := TaskTagInput(tag).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), string(tag)) || !strings.Contains(out.String(), "&lt;script&gt;") {
		t.Fatal("tag not escaped in input")
	}
}
