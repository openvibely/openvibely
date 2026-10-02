package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTaskDetailPropertyIsScopedAndPreservesOtherFields(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	p := createProject(t, h, "Details")
	other := createProject(t, h, "Other")
	task := createTask(t, h, p.ID, "Task", func(task *models.Task) { task.Status = models.StatusRunning; task.Category = models.CategoryActive })
	options, err := h.llmConfigRepo.ListBadgeOptions(context.Background())
	if err != nil || len(options) == 0 {
		t.Fatalf("fixture models: %v", err)
	}
	for _, test := range []struct {
		field, value, project string
		status                int
	}{
		{"auto_merge", "true", p.ID, 200},
		{"auto_merge_on_goal_achieved", "true", p.ID, 200},
		{"auto_merge", "false", p.ID, 200},
		{"auto_merge", "true", p.ID, 200},
		{"auto_merge", "invalid", p.ID, 400},
		{"auto_merge", "false", other.ID, 400},
		{"priority", "4", p.ID, 200}, {"tag", "bug", p.ID, 200},
		{"priority", "99", p.ID, 400}, {"status", "running", p.ID, 400},
		{"agent_id", "missing", p.ID, 400}, {"prompt", "", p.ID, 400},
		{"agent_id", "default", p.ID, 200},
		{"agent_id", options[0].ID, p.ID, 200}, {"agent_id", "", p.ID, 200},
		{"agent_definition_id", "missing", p.ID, 400},
		{"priority", "1", "", 400},
		{"category", "scheduled", p.ID, 400},
		{"priority", "1", other.ID, 400},
	} {
		body := url.Values{"field": {test.field}, "value": {test.value}}
		req := httptest.NewRequest(http.MethodPatch, "/tasks/"+task.ID+"/details/property?project_id="+test.project, strings.NewReader(body.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("%s: got %d: %s", test.field, rec.Code, rec.Body.String())
		}
	}
	got, err := h.taskRepo.GetByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AutoMerge || !got.AutoMergeOnGoalAchieved || got.Priority != 4 || got.Tag != "bug" || got.Title != task.Title || got.Prompt != task.Prompt || got.Status != task.Status {
		t.Fatalf("unexpected task changes: %+v", got)
	}
}
