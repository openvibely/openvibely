package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestActiveMenuSortAndRefresh(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	createTask(t, h, "default", "Zebra active")
	createTask(t, h, "default", "Apple active")
	req := httptest.NewRequest(http.MethodPost, "/tasks/active/sort?project_id=default&sort=title_asc", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("sort: %d %s", rec.Code, rec.Body.String())
	}
	check := func(body string) {
		t.Helper()
		for _, text := range []string{"column-active", "Stop All", `data-delete-all-tasks-category="active"`, "/tasks/active/stop?project_id=default", "Priority (High to Low)"} {
			if !strings.Contains(body, text) {
				t.Errorf("missing %s", text)
			}
		}
		a, z := strings.Index(body, "Apple active"), strings.Index(body, "Zebra active")
		if a < 0 || z < 0 || a > z {
			t.Error("Active tasks not sorted by name")
		}
	}
	check(rec.Body.String())
	req = httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "active_sort" {
			req.AddCookie(c)
			found = true
		}
	}
	if !found {
		t.Fatal("missing sort cookie")
	}
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d", rec.Code)
	}
	check(rec.Body.String())
}

func TestActiveBulkActionsScope(t *testing.T) {
	for _, deleting := range []bool{false, true} {
		name := "stop"
		if deleting {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			h, e, configRepo := setupTestHandler(t)
			ctx := context.Background()
			agent := createAgent(t, configRepo)
			foreign := createProject(t, h, "foreign")
			active := createTask(t, h, "default", "Active")
			running := createTask(t, h, "default", "Scheduled running", func(t *models.Task) { t.Category = models.CategoryScheduled; t.Status = models.StatusRunning })
			execution := &models.Execution{TaskID: running.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "run"}
			if err := h.execRepo.Create(ctx, execution); err != nil {
				t.Fatal(err)
			}

			backlog := createTask(t, h, "default", "Backlog", func(t *models.Task) { t.Category = models.CategoryBacklog })
			other := createTask(t, h, foreign.ID, "Other")
			scheduled := createTask(t, h, "default", "Scheduled idle", func(t *models.Task) { t.Category = models.CategoryScheduled })
			method, path := http.MethodPost, "/tasks/active/stop"
			if deleting {
				method, path = http.MethodDelete, "/tasks/active"
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("missing project: %d", rec.Code)
			}
			rec = httptest.NewRecorder()
			req := httptest.NewRequest(method, path+"?project_id=default", nil)
			req.Header.Set("HX-Request", "true")
			e.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("action: %d %s", rec.Code, rec.Body.String())
			}
			for _, task := range []*models.Task{active, running} {
				got, err := h.taskSvc.GetByID(ctx, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				if deleting {
					if got != nil {
						t.Errorf("task %s not deleted", task.ID)
					}
				} else if got == nil || got.Status != models.StatusCancelled {
					t.Errorf("task %s not stopped: %+v", task.ID, got)
				}
			}
			for _, task := range []*models.Task{backlog, other, scheduled} {
				got, err := h.taskSvc.GetByID(ctx, task.ID)
				if err != nil || got == nil || got.Status != task.Status || got.Category != task.Category {
					t.Errorf("unrelated task changed: %s", task.ID)
				}
			}
		})
	}
}
