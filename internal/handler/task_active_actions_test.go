package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestActiveMenuIgnoresOldSortPreference(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	createTask(t, h, "default", "Zebra active")
	createTask(t, h, "default", "Apple active")
	for _, fragment := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
		req.AddCookie(&http.Cookie{Name: "active_sort", Value: "title_asc"})
		if fragment {
			req.Header.Set("HX-Request", "true")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("refresh: %d", rec.Code)
		}
		body := rec.Body.String()
		for _, text := range []string{"column-active", `data-kanban-action="cancel"`, `data-kanban-action="delete"`} {
			if !strings.Contains(body, text) {
				t.Errorf("missing %s", text)
			}
		}
		for _, text := range []string{"/tasks/active/sort", "Manual order"} {
			if strings.Contains(body, text) {
				t.Errorf("obsolete Active sorting: %s", text)
			}
		}
		z, a := strings.Index(body, "Zebra active"), strings.Index(body, "Apple active")
		if z < 0 || a < 0 || z > a {
			t.Fatal("old sort cookie changed Active order")
		}
		if !strings.Contains(body, "/tasks/backlog/sort") || !strings.Contains(body, "/tasks/completed/sort") {
			t.Fatal("other column sorting missing")
		}
	}
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

func TestActiveDeletionSkipsChangedScheduledState(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	ctx := context.Background()
	task := createTask(t, h, "default", "Scheduled run", func(task *models.Task) {
		task.Category = models.CategoryScheduled
		task.Status = models.StatusRunning
	})
	observed, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.taskSvc.UpdateStatus(ctx, task.ID, models.StatusCompleted); err != nil {
		t.Fatal(err)
	}
	if err := h.taskSvc.DeleteObservedTask(ctx, observed); err != nil {
		t.Fatal(err)
	}
	current, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil || current == nil || current.Status != models.StatusCompleted {
		t.Fatalf("finished scheduled task was deleted: %+v %v", current, err)
	}
	if err := h.taskSvc.DeleteObservedTask(ctx, current); err != nil {
		t.Fatal(err)
	}
	current, err = h.taskSvc.GetByID(ctx, task.ID)
	if err != nil || current != nil {
		t.Fatalf("unchanged observation not deleted: %+v %v", current, err)
	}
}
