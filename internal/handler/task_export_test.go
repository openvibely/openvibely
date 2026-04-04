package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestTasksPage_NoImportExportControls(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Regression Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "Test prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	t.Run("full tasks page has no import export controls", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		assertNotContains(t, rec, "Import Tasks")
		assertNotContains(t, rec, "Export All")
		assertNotContains(t, rec, "import_tasks_modal")
		assertNotContains(t, rec, "openImportModal")
		assertNotContains(t, rec, "import-file-input")
		assertNotContains(t, rec, "handleBacklogImport")
		assertNotContains(t, rec, "/tasks/import")
		assertNotContains(t, rec, "/tasks/export")
	})

	t.Run("kanban fragment has no import export controls", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", "kanban-board")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d", rec.Code)
		}

		assertNotContains(t, rec, "Import/Export")
		assertNotContains(t, rec, "Import Tasks")
		assertNotContains(t, rec, "Export")
		assertNotContains(t, rec, "backlog-import-file")
		assertNotContains(t, rec, "/tasks/import")
		assertNotContains(t, rec, "/tasks/export")
	})
}

func TestTasksImportExportRoutes_NotAccessible(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	t.Run("GET /tasks/export returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/tasks/export?project_id=default", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected status 404, got %d", rec.Code)
		}
	})

	t.Run("POST /tasks/import is inaccessible", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/tasks/import?project_id=default", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code == http.StatusOK {
			t.Fatalf("expected non-200 status, got %d", rec.Code)
		}
	})
}

func TestTasksFlowsRemainStableWithoutImportExport(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	task := &models.Task{
		ProjectID: "default",
		Title:     "Backlog Task",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		Prompt:    "Ship it",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	moveRec := htmxPatch(e, "/tasks/"+task.ID+"/category", url.Values{"category": {"active"}})
	if moveRec.Code != http.StatusOK {
		t.Fatalf("expected category update status 200, got %d", moveRec.Code)
	}

	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to fetch updated task: %v", err)
	}
	if updated == nil || updated.Category != models.CategoryActive {
		t.Fatalf("expected task category active, got %+v", updated)
	}
}
