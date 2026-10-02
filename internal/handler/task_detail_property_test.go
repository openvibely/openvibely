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

func TestTaskDefaultSelectionUsesProjectModel(t *testing.T) {
	h, e, repo := setupTestHandler(t)
	ctx := context.Background()
	global := createAgent(t, repo, func(a *models.LLMConfig) { a.IsDefault = true })
	projectModel := createAgent(t, repo, func(a *models.LLMConfig) { a.IsDefault = false; a.Name = "Project model" })
	project := createProject(t, h, "Project default")
	project.DefaultAgentConfigID = &projectModel.ID
	if err := h.projectRepo.Update(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := createTask(t, h, project.ID, "Default selection")
	form := url.Values{"field": {"agent_id"}, "value": {"default"}}
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+task.ID+"/details/property?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	stored, err := h.taskRepo.GetByID(ctx, task.ID)
	if err != nil || stored.AgentID == nil || *stored.AgentID != projectModel.ID {
		t.Fatalf("project default not assigned: %v %v", stored, err)
	}
	form = url.Values{"message": {"Use the project model"}, "agent_id": {"default"}}
	req = httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	stored, err = h.taskRepo.GetByID(ctx, rec.Header().Get("X-Created-Task-ID"))
	if err != nil || stored == nil || stored.AgentID == nil || *stored.AgentID != projectModel.ID {
		t.Fatalf("first send default not assigned: %v %v", stored, err)
	}
	selected, err := h.selectTaskAgent(ctx, project.ID, global.ID, "", false)
	if err != nil || selected.ID != global.ID {
		t.Fatal("explicit model overridden", err)
	}
	project.DefaultAgentConfigID = nil
	if err := h.projectRepo.Update(ctx, project); err != nil {
		t.Fatal(err)
	}
	selected, err = h.selectTaskAgent(ctx, project.ID, "default", "", false)
	if err != nil || selected.ID != global.ID {
		t.Fatal("global fallback failed", err)
	}
}
