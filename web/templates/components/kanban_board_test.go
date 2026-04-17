package components

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestKanbanColumn_DropdownTriggersUseLabelForDesktopWebviewCompatibility(t *testing.T) {
	var buf bytes.Buffer
	err := KanbanColumn([]models.Task{}, "project-1", models.CategoryBacklog, "", "", nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render backlog column: %v", err)
	}
	html := buf.String()

	if !strings.Contains(html, `<label tabindex="0" class="btn btn-xs btn-ghost" title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected backlog kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost" title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("unexpected <button> dropdown trigger in backlog column")
	}

	buf.Reset()
	err = KanbanColumn([]models.Task{}, "project-1", models.CategoryCompleted, "", "", nil, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render completed column: %v", err)
	}
	html = buf.String()
	if !strings.Contains(html, `<label tabindex="0" class="btn btn-xs btn-ghost" title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("expected completed kebab trigger to use <label> for stable dropdown focus behavior")
	}
	if strings.Contains(html, `<button tabindex="0" class="btn btn-xs btn-ghost" title="More actions" onclick="handleDropdownToggle(event)">`) {
		t.Fatal("unexpected <button> dropdown trigger in completed column")
	}
}
