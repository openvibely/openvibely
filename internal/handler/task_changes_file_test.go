package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func TestHandler_GetTaskChangesFile_LoadsRequestedInlineFile(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projects, err := projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("expected default project, err=%v", err)
	}
	project := projects[0]

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Per-file load test",
		Category:  models.CategoryCompleted,
		Status:    models.StatusCompleted,
		Prompt:    "test",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	agents, err := h.llmConfigRepo.List(ctx)
	if err != nil || len(agents) == 0 {
		t.Fatalf("list agents: %v", err)
	}
	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agents[0].ID,
		Status:        models.ExecCompleted,
		PromptSent:    "prompt",
	}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	diff := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-old
+new
diff --git a/b.txt b/b.txt
--- a/b.txt
+++ b/b.txt
@@ -1 +1 @@
-foo
+bar
`
	if err := h.execRepo.UpdateDiffOutput(ctx, exec.ID, diff); err != nil {
		t.Fatalf("update diff output: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/file?file_index=1&view=inline&review=true", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	if err := h.GetTaskChangesFile(c); err != nil {
		t.Fatalf("GetTaskChangesFile failed: %v", err)
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="diff-file-1"`) {
		t.Fatalf("expected inline file card for file index 1, got: %s", body)
	}
	if !strings.Contains(body, "b.txt") {
		t.Fatalf("expected loaded card to include requested file name, got: %s", body)
	}
}

func TestHandler_GetTaskChangesFile_InvalidIndexReturnsBadRequest(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	defer db.Close()

	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	projects, err := projectRepo.List(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("expected default project, err=%v", err)
	}
	project := projects[0]
	task := createTask(t, h, project.ID, "Invalid index task", func(task *models.Task) {
		task.Category = models.CategoryCompleted
		task.Status = models.StatusCompleted
	})

	req := httptest.NewRequest(http.MethodGet, "/tasks/"+task.ID+"/changes/file?file_index=oops&view=inline", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("taskId")
	c.SetParamValues(task.ID)

	err = h.GetTaskChangesFile(c)
	if err == nil {
		t.Fatal("expected bad request error")
	}
	httpErr, ok := err.(*echo.HTTPError)
	if !ok {
		t.Fatalf("expected echo.HTTPError, got %T", err)
	}
	if httpErr.Code != http.StatusBadRequest {
		t.Fatalf("expected status 400, got %d", httpErr.Code)
	}
}
