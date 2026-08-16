package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func TestHandler_GetTask_HTMXHistoryCacheMissReturnsFullTitledDocument(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().Build()
	task := tc.CreateTask(project.ID).
		WithTitle("Restore <task> & title").
		WithCategory(models.CategoryBacklog).
		Build()
	path := "/tasks/" + task.ID + "?tab=details&project_id=" + project.ID

	partialReq := httptest.NewRequest(http.MethodGet, path, nil)
	partialReq.Header.Set("HX-Request", "true")
	partialReq.Header.Set("HX-Target", "main-content")
	partialRec := httptest.NewRecorder()
	tc.echo.ServeHTTP(partialRec, partialReq)
	if partialRec.Code != http.StatusOK {
		t.Fatalf("ordinary HTMX request: expected 200, got %d body=%s", partialRec.Code, partialRec.Body.String())
	}
	partialBody := partialRec.Body.String()
	if strings.Contains(partialBody, "<!doctype html>") {
		t.Fatal("ordinary HTMX navigation must remain a content fragment")
	}
	if !strings.Contains(partialBody, `data-openvibely-page-title="Restore &lt;task&gt; &amp; title - OpenVibely"`) {
		t.Fatal("ordinary HTMX navigation fragment is missing its authoritative title marker")
	}

	restoreReq := httptest.NewRequest(http.MethodGet, path, nil)
	restoreReq.Header.Set("HX-Request", "true")
	restoreReq.Header.Set("HX-History-Restore-Request", "true")
	restoreRec := httptest.NewRecorder()
	tc.echo.ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("HTMX history cache miss: expected 200, got %d body=%s", restoreRec.Code, restoreRec.Body.String())
	}
	restoreBody := restoreRec.Body.String()
	if !strings.Contains(restoreBody, "<!doctype html>") || !strings.Contains(restoreBody, `id="main-content"`) {
		t.Fatal("HTMX history cache miss must return the complete application document")
	}
	if !strings.Contains(restoreBody, `<title>Restore &lt;task&gt; &amp; title - OpenVibely</title>`) {
		t.Fatal("HTMX history cache miss document is missing its authoritative escaped title")
	}
}

func TestHandler_ListTasks_HTMXNavigation(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Simulate HTMX navigation from sidebar (target: #main-content)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "main-content")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify that the response includes the header with "Tasks" and "+ Add Task"
	if !strings.Contains(body, "Tasks") {
		t.Error("expected response to contain 'Tasks' header, but it was missing")
	}
	if !strings.Contains(body, "+ Add Task") {
		t.Error("expected response to contain '+ Add Task' button, but it was missing")
	}

	// Should also contain the kanban board
	if !strings.Contains(body, "kanban-board") {
		t.Error("expected response to contain kanban board")
	}
}

func TestHandler_ListTasks_HTMXUpdate(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Simulate HTMX update (like SSE refresh, target: #kanban-board)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "kanban-board")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify that the response includes the kanban board
	if !strings.Contains(body, "kanban-board") {
		t.Error("expected response to contain kanban board")
	}

	// But should NOT include the page header (just the board for updates)
	// The header would be wrapped in a div with class containing "flex items-center justify-between"
	// Since we're only returning the kanban board, the first element should be the board itself
	if !strings.HasPrefix(strings.TrimSpace(body), "<div id=\"kanban-board\"") {
		t.Error("expected response to start with kanban-board div (no header wrapper)")
	}
}

func TestHandler_ListTasks_HTMXUpdate_DefaultsDateSortsNewestFirstAndPreservesCookies(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()

	createTask := func(title string, category models.TaskCategory) *models.Task {
		t.Helper()
		task := &models.Task{
			ProjectID: "default",
			Title:     title,
			Category:  category,
			Status:    models.StatusPending,
			Prompt:    "test prompt",
			Priority:  2,
		}
		if category == models.CategoryCompleted {
			task.Status = models.StatusCompleted
		}
		if err := h.taskSvc.Create(ctx, task); err != nil {
			t.Fatalf("create task %q: %v", title, err)
		}
		return task
	}

	oldBacklog := createTask("Zulu Backlog", models.CategoryBacklog)
	newBacklog := createTask("Alpha Backlog", models.CategoryBacklog)
	oldCompleted := createTask("Zulu Completed", models.CategoryCompleted)
	legacyCompleted := createTask("Mike Legacy Completed", models.CategoryCompleted)
	newCompleted := createTask("Alpha Completed", models.CategoryCompleted)

	updates := []struct {
		id          string
		createdAt   string
		updatedAt   string
		completedAt any
	}{
		{oldBacklog.ID, "2024-01-01 10:00:00", "2024-01-01 10:00:00", nil},
		{newBacklog.ID, "2024-01-02 10:00:00", "2024-01-02 10:00:00", nil},
		{oldCompleted.ID, "2024-03-03 10:00:00", "2024-06-03 10:00:00", "2024-01-01 10:00:00"},
		{legacyCompleted.ID, "2024-03-02 10:00:00", "2024-01-02 10:00:00", nil},
		{newCompleted.ID, "2024-03-01 10:00:00", "2024-06-01 10:00:00", "2024-01-03 10:00:00"},
	}
	for _, update := range updates {
		if _, err := db.ExecContext(ctx, `UPDATE tasks SET created_at = ?, updated_at = ?, completed_at = ? WHERE id = ?`,
			update.createdAt, update.updatedAt, update.completedAt, update.id); err != nil {
			t.Fatalf("set timestamps for task %s: %v", update.id, err)
		}
	}

	renderBoard := func(cookies ...*http.Cookie) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", "kanban-board")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("list tasks status = %d, body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	defaultBody := renderBoard()
	assertTaskOrder(t, backlogDropZone(defaultBody), "Alpha Backlog", "Zulu Backlog")
	assertTaskOrder(t, completedDropZone(defaultBody), "Alpha Completed", "Mike Legacy Completed", "Zulu Completed")
	assertSortControlActive(t, defaultBody, "sort=created_desc")
	assertSortControlActive(t, defaultBody, "sort=completed_desc")

	explicitBody := renderBoard(
		&http.Cookie{Name: backlogSortCookieName, Value: "title_desc"},
		&http.Cookie{Name: completedSortCookieName, Value: "title_desc"},
	)
	assertTaskOrder(t, backlogDropZone(explicitBody), "Zulu Backlog", "Alpha Backlog")
	assertTaskOrder(t, completedDropZone(explicitBody), "Zulu Completed", "Mike Legacy Completed", "Alpha Completed")
	assertSortControlActive(t, explicitBody, "sort=title_desc")
}

func TestHandler_UpdateTaskCategory_HTMXRefreshPreservesSortCookies(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	for _, title := range []string{"Alpha Backlog", "Zulu Backlog"} {
		if err := h.taskSvc.Create(ctx, &models.Task{
			ProjectID: "default",
			Title:     title,
			Category:  models.CategoryBacklog,
			Status:    models.StatusPending,
			Prompt:    "test prompt",
			Priority:  2,
		}); err != nil {
			t.Fatalf("create backlog task %q: %v", title, err)
		}
	}
	movingTask := &models.Task{
		ProjectID: "default",
		Title:     "Mike Moving",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test prompt",
		Priority:  2,
	}
	if err := h.taskSvc.Create(ctx, movingTask); err != nil {
		t.Fatalf("create moving task: %v", err)
	}

	form := url.Values{"category": {string(models.CategoryBacklog)}}
	req := httptest.NewRequest(http.MethodPatch, "/tasks/"+movingTask.ID+"/category", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.AddCookie(&http.Cookie{Name: backlogSortCookieName, Value: "title_desc"})
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("update category status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	assertTaskOrder(t, backlogDropZone(body), "Zulu Backlog", "Mike Moving", "Alpha Backlog")
	assertSortControlActive(t, body, "sort=title_desc")
}

func TestHandler_SetBacklogSort_HTMXRouteRendersSortedBoard(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	for _, title := range []string{"Zulu Sort Route", "Alpha Sort Route", "Mike Sort Route"} {
		if err := h.taskSvc.Create(ctx, &models.Task{
			ProjectID: "default",
			Title:     title,
			Category:  models.CategoryBacklog,
			Status:    models.StatusPending,
			Prompt:    "test prompt",
			Priority:  2,
		}); err != nil {
			t.Fatalf("create backlog task %q: %v", title, err)
		}
	}

	rec := htmxPost(e, "/tasks/backlog/sort?project_id=default&sort=title_asc", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("set backlog sort status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.HasPrefix(strings.TrimSpace(body), "<div id=\"kanban-board\"") {
		t.Fatalf("expected sorted route response to start with kanban board, body=%s", body)
	}
	assertTaskOrder(t, backlogDropZone(body), "Alpha Sort Route", "Mike Sort Route", "Zulu Sort Route")
	assertSortControlActive(t, body, "sort=title_asc")

	foundSortCookie := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == backlogSortCookieName {
			foundSortCookie = true
			if cookie.Value != "title_asc" {
				t.Fatalf("backlog sort cookie = %q, want title_asc", cookie.Value)
			}
		}
	}
	if !foundSortCookie {
		t.Fatal("expected backlog sort cookie to be set")
	}
}

func TestHandler_TaskBoardMutationRefreshAssemblyIsCentralized(t *testing.T) {
	source, err := os.ReadFile("task_handler.go")
	if err != nil {
		t.Fatalf("read task handler source: %v", err)
	}
	body := string(source)
	helperBody := handlerFunctionBody(t, body, "renderTaskBoardRefresh")
	for _, required := range []string{"getSortPreferences(c)", "ListBoardByProjectWithCategorySorts", "llmConfigRepo.List", "renderKanbanBoard"} {
		if !strings.Contains(helperBody, required) {
			t.Fatalf("renderTaskBoardRefresh missing %q", required)
		}
	}

	for _, handlerName := range []string{
		"CreateTask",
		"DeleteTask",
		"CancelTask",
		"UpdateTaskCategory",
		"MoveCompletedActiveToCompleted",
		"UpdateTaskStatus",
		"BatchUpdateTaskCategory",
		"DeleteAllCompletedTasks",
		"DeleteAllBacklogTasks",
		"ActivateAllBacklogTasks",
		"ReorderTask",
		"ExecuteBacklogTasks",
		"SetBacklogSort",
		"SetCompletedSort",
	} {
		handlerBody := handlerFunctionBody(t, body, handlerName)
		if strings.Contains(handlerBody, "ListBoardByProjectWithCategorySorts") {
			t.Fatalf("%s must delegate board listing to renderTaskBoardRefresh", handlerName)
		}
		if !strings.Contains(handlerBody, "renderTaskBoardRefresh") {
			t.Fatalf("%s must use renderTaskBoardRefresh for task-board HTMX refreshes", handlerName)
		}
	}
}

func TestHandler_TaskBoardHTMXMutationRoutesReturnKanbanRefresh(t *testing.T) {
	tests := []struct {
		name       string
		seed       func(*TestContext) string
		request    func(*TestContext, string) *httptest.ResponseRecorder
		want       []string
		wantAbsent []string
	}{
		{
			name: "create",
			request: func(tc *TestContext, _ string) *httptest.ResponseRecorder {
				return tc.HTMX().Post("/tasks?project_id=default").WithForm(url.Values{
					"title":    {"Created Refresh Task"},
					"prompt":   {"created prompt"},
					"category": {string(models.CategoryBacklog)},
					"priority": {"2"},
				}).Execute()
			},
			want: []string{"Created Refresh Task"},
		},
		{
			name: "delete",
			seed: func(tc *TestContext) string {
				deleted := tc.CreateTask("default").WithTitle("Deleted Refresh Task").WithCategory(models.CategoryBacklog).Build()
				tc.CreateTask("default").WithTitle("Remaining Refresh Task").WithCategory(models.CategoryBacklog).Build()
				return deleted.ID
			},
			request: func(tc *TestContext, id string) *httptest.ResponseRecorder {
				return tc.HTMX().Delete("/tasks/" + id).Execute()
			},
			want:       []string{"Remaining Refresh Task"},
			wantAbsent: []string{"Deleted Refresh Task"},
		},
		{
			name: "move category",
			seed: func(tc *TestContext) string {
				return tc.CreateTask("default").WithTitle("Moved Refresh Task").WithCategory(models.CategoryActive).Build().ID
			},
			request: func(tc *TestContext, id string) *httptest.ResponseRecorder {
				return tc.HTMX().Patch("/tasks/" + id + "/category").WithForm(url.Values{"category": {string(models.CategoryBacklog)}}).Execute()
			},
			want: []string{"Moved Refresh Task", `data-category="backlog"`},
		},
		{
			name: "status",
			seed: func(tc *TestContext) string {
				return tc.CreateTask("default").WithTitle("Status Refresh Task").WithCategory(models.CategoryActive).Build().ID
			},
			request: func(tc *TestContext, id string) *httptest.ResponseRecorder {
				return tc.HTMX().Patch("/tasks/" + id + "/status").WithForm(url.Values{"status": {string(models.StatusCancelled)}}).Execute()
			},
			want: []string{"Status Refresh Task"},
		},
		{
			name: "reorder",
			seed: func(tc *TestContext) string {
				return tc.CreateTask("default").WithTitle("Reordered Refresh Task").WithCategory(models.CategoryBacklog).Build().ID
			},
			request: func(tc *TestContext, id string) *httptest.ResponseRecorder {
				return tc.HTMX().Patch("/tasks/" + id + "/reorder").WithForm(url.Values{"position": {"1"}}).Execute()
			},
			want: []string{"Reordered Refresh Task"},
		},
		{
			name: "batch category",
			seed: func(tc *TestContext) string {
				first := tc.CreateTask("default").WithTitle("Batch Refresh One").WithCategory(models.CategoryActive).Build()
				second := tc.CreateTask("default").WithTitle("Batch Refresh Two").WithCategory(models.CategoryActive).Build()
				return first.ID + "," + second.ID
			},
			request: func(tc *TestContext, ids string) *httptest.ResponseRecorder {
				return tc.HTMX().Patch("/tasks/batch-category").WithForm(url.Values{
					"project_id": {"default"},
					"task_ids":   {ids},
					"category":   {string(models.CategoryBacklog)},
				}).Execute()
			},
			want: []string{"Batch Refresh One", "Batch Refresh Two", `data-category="backlog"`},
		},
		{
			name: "delete completed bulk",
			seed: func(tc *TestContext) string {
				tc.CreateTask("default").WithTitle("Completed Bulk Removed").WithCategory(models.CategoryCompleted).WithStatus(models.StatusCompleted).Build()
				tc.CreateTask("default").WithTitle("Active Bulk Survivor").WithCategory(models.CategoryActive).Build()
				return ""
			},
			request: func(tc *TestContext, _ string) *httptest.ResponseRecorder {
				return tc.HTMX().Delete("/tasks/completed?project_id=default").Execute()
			},
			want:       []string{"Active Bulk Survivor"},
			wantAbsent: []string{"Completed Bulk Removed"},
		},
		{
			name: "activate backlog bulk",
			seed: func(tc *TestContext) string {
				tc.CreateTask("default").WithTitle("Activated Bulk Refresh").WithCategory(models.CategoryBacklog).Build()
				return ""
			},
			request: func(tc *TestContext, _ string) *httptest.ResponseRecorder {
				return tc.HTMX().Post("/tasks/backlog/activate?project_id=default").Execute()
			},
			want: []string{"Activated Bulk Refresh", `data-category="active"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTestContext(t)
			var id string
			if tt.seed != nil {
				id = tt.seed(tc)
			}
			rec := tt.request(tc, id)
			assertKanbanBoardRefresh(t, rec)
			body := rec.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Fatalf("response missing %q: %s", want, body)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(body, absent) {
					t.Fatalf("response unexpectedly contained %q: %s", absent, body)
				}
			}
		})
	}
}

func TestHandler_TaskBoardSortRoutesRenderSelectedSortImmediatelyOverStaleCookies(t *testing.T) {
	tests := []struct {
		name         string
		path         string
		activeMarker string
		cookieName   string
		cookieSort   string
		wantOrder    []string
	}{
		{
			name:         "backlog sort",
			path:         "/tasks/backlog/sort?project_id=default&sort=title_asc",
			activeMarker: "/tasks/backlog/sort?project_id=default&amp;sort=title_asc",
			cookieName:   backlogSortCookieName,
			cookieSort:   "title_desc",
			wantOrder:    []string{"Alpha Immediate Backlog", "Zulu Immediate Backlog"},
		},
		{
			name:         "completed sort",
			path:         "/tasks/completed/sort?project_id=default&sort=title_asc",
			activeMarker: "/tasks/completed/sort?project_id=default&amp;sort=title_asc",
			cookieName:   completedSortCookieName,
			cookieSort:   "title_desc",
			wantOrder:    []string{"Alpha Immediate Completed", "Zulu Immediate Completed"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTestContext(t)
			if tt.cookieName == backlogSortCookieName {
				tc.CreateTask("default").WithTitle("Zulu Immediate Backlog").WithCategory(models.CategoryBacklog).Build()
				tc.CreateTask("default").WithTitle("Alpha Immediate Backlog").WithCategory(models.CategoryBacklog).Build()
			} else {
				tc.CreateTask("default").WithTitle("Zulu Immediate Completed").WithCategory(models.CategoryCompleted).WithStatus(models.StatusCompleted).Build()
				tc.CreateTask("default").WithTitle("Alpha Immediate Completed").WithCategory(models.CategoryCompleted).WithStatus(models.StatusCompleted).Build()
			}

			req := httptest.NewRequest(http.MethodPost, tt.path, nil)
			req.Header.Set("HX-Request", "true")
			req.AddCookie(&http.Cookie{Name: tt.cookieName, Value: tt.cookieSort})
			rec := httptest.NewRecorder()
			tc.echo.ServeHTTP(rec, req)

			assertKanbanBoardRefresh(t, rec)
			body := rec.Body.String()
			assertTaskOrder(t, body, tt.wantOrder...)
			assertSortControlActive(t, body, tt.activeMarker)
		})
	}
}

func TestHandler_CancelTaskComposerStopReturnsOnlyComposerActionOOB(t *testing.T) {
	tc := NewTestContext(t)
	task := tc.CreateTask("default").WithTitle("Composer Stop Refresh Guard").WithCategory(models.CategoryActive).WithStatus(models.StatusRunning).Build()

	rec := tc.HTMX().Post("/tasks/" + task.ID + "/cancel?composer_stop=1").Execute()
	if rec.Code != http.StatusOK {
		t.Fatalf("cancel composer stop status = %d, body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="task-thread-form-primary-action"`) || !strings.Contains(body, `hx-swap-oob="outerHTML"`) {
		t.Fatalf("expected composer action OOB fragment, got %s", body)
	}
	if strings.Contains(body, `id="kanban-board"`) {
		t.Fatalf("composer stop must not refresh the full kanban board: %s", body)
	}
}

func TestHandler_TaskBoardMutationRefreshListErrorsAreSurfacedConsistently(t *testing.T) {
	tests := []struct {
		name    string
		request func(*TestContext) *httptest.ResponseRecorder
	}{
		{
			name: "create",
			request: func(tc *TestContext) *httptest.ResponseRecorder {
				return tc.HTMX().Post("/tasks?project_id=default").WithForm(url.Values{
					"title":    {"Create Before Board Error"},
					"prompt":   {"prompt"},
					"category": {string(models.CategoryBacklog)},
					"priority": {"2"},
				}).Execute()
			},
		},
		{
			name: "delete",
			request: func(tc *TestContext) *httptest.ResponseRecorder {
				victim := tc.CreateTask("default").WithTitle("Delete Before Board Error").WithCategory(models.CategoryBacklog).Build()
				return tc.HTMX().Delete("/tasks/" + victim.ID).Execute()
			},
		},
		{
			name: "move category",
			request: func(tc *TestContext) *httptest.ResponseRecorder {
				moving := tc.CreateTask("default").WithTitle("Move Before Board Error").WithCategory(models.CategoryActive).Build()
				return tc.HTMX().Patch("/tasks/" + moving.ID + "/category").WithForm(url.Values{"category": {string(models.CategoryBacklog)}}).Execute()
			},
		},
		{
			name: "sort",
			request: func(tc *TestContext) *httptest.ResponseRecorder {
				return tc.HTMX().Post("/tasks/backlog/sort?project_id=default&sort=title_asc").Execute()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := NewTestContext(t)
			seedForcedBoardListingError(t, tc)
			rec := tt.request(tc)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("expected forced board listing error to return 500, got %d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_TaskBoardRefreshNonHTMXAndNoContentResponsesRemainUnchanged(t *testing.T) {
	t.Run("delete redirects outside htmx", func(t *testing.T) {
		tc := NewTestContext(t)
		task := tc.CreateTask("default").WithTitle("Native Delete Redirect").WithCategory(models.CategoryBacklog).Build()
		rec := tc.HTTP().Delete("/tasks/" + task.ID).Execute()
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("delete status = %d, body=%s", rec.Code, rec.Body.String())
		}
		if location := rec.Header().Get("Location"); location != "/tasks?project_id=default" {
			t.Fatalf("delete redirect Location = %q", location)
		}
	})

	t.Run("status returns no content outside htmx", func(t *testing.T) {
		tc := NewTestContext(t)
		task := tc.CreateTask("default").WithTitle("Native Status No Content").WithCategory(models.CategoryBacklog).Build()
		rec := tc.HTTP().Patch("/tasks/" + task.ID + "/status").WithForm(url.Values{"status": {string(models.StatusBlocked)}}).Execute()
		if rec.Code != http.StatusOK {
			t.Fatalf("status code = %d, body=%s", rec.Code, rec.Body.String())
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("expected empty no-content body, got %s", rec.Body.String())
		}
	})
}

func assertSortControlActive(t *testing.T, body string, sortQuery string) {
	t.Helper()
	start := strings.Index(body, sortQuery)
	if start == -1 {
		t.Fatalf("sort control %q not found", sortQuery)
	}
	end := strings.Index(body[start:], "</a>")
	if end == -1 {
		t.Fatalf("sort control %q has no closing anchor", sortQuery)
	}
	if control := body[start : start+end]; !strings.Contains(control, `class="text-sm min-h-11 active font-semibold"`) {
		t.Fatalf("sort control %q is not rendered active: %s", sortQuery, control)
	}
}

func assertKanbanBoardRefresh(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	body := strings.TrimSpace(rec.Body.String())
	if !strings.HasPrefix(body, `<div id="kanban-board"`) {
		t.Fatalf("expected response to start with kanban board, got %s", rec.Body.String())
	}
}

func handlerFunctionBody(t *testing.T, source, name string) string {
	t.Helper()
	marker := "func (h *Handler) " + name + "("
	start := strings.Index(source, marker)
	if start == -1 {
		t.Fatalf("handler function %s not found", name)
	}
	next := strings.Index(source[start+len(marker):], "\nfunc ")
	if next == -1 {
		return source[start:]
	}
	return source[start : start+len(marker)+next]
}

func seedForcedBoardListingError(t *testing.T, tc *TestContext) {
	t.Helper()
	poison := &models.Task{
		ProjectID: "default",
		Title:     "Forced Board Listing Error",
		Category:  models.CategoryActive,
		Status:    models.StatusFailed,
		Prompt:    "forces ListBoardByProjectWithCategorySorts normalization to fail",
	}
	if err := tc.taskRepo.Create(context.Background(), poison); err != nil {
		t.Fatalf("create poison task: %v", err)
	}
	trigger := fmt.Sprintf(`
		CREATE TRIGGER fail_board_refresh_normalize_%s
		BEFORE UPDATE OF category ON tasks
		WHEN OLD.id = %q AND NEW.category = 'backlog'
		BEGIN
			SELECT RAISE(ABORT, 'forced board list error');
		END;`, strings.ReplaceAll(poison.ID, "-", "_"), poison.ID)
	if _, err := tc.db.ExecContext(context.Background(), trigger); err != nil {
		t.Fatalf("create board listing failure trigger: %v", err)
	}
}

func assertTaskOrder(t *testing.T, body string, titles ...string) {
	t.Helper()
	previous := -1
	for _, title := range titles {
		position := strings.Index(body, title)
		if position == -1 {
			t.Fatalf("task %q not found in dropzone", title)
		}
		if position <= previous {
			t.Fatalf("task %q at position %d is not after previous position %d", title, position, previous)
		}
		previous = position
	}
}

func backlogDropZone(body string) string {
	const backlogMarker = `data-category="backlog"`
	start := strings.Index(body, backlogMarker)
	if start == -1 {
		return ""
	}
	body = body[start:]
	if end := strings.Index(body, `data-category="active"`); end != -1 {
		return body[:end]
	}
	return body
}

func TestHandler_ListTasks_NonHTMX(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Regular page load (non-HTMX)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Should include the full page with header and content
	if !strings.Contains(body, "Tasks") {
		t.Error("expected page to contain 'Tasks' header")
	}
	if !strings.Contains(body, "+ Add Task") {
		t.Error("expected page to contain '+ Add Task' button")
	}
	if !strings.Contains(body, "kanban-board") {
		t.Error("expected page to contain kanban board")
	}
	// Full page should include OpenVibely branding from the layout
	if !strings.Contains(body, "OpenVibely") {
		t.Error("expected page to contain OpenVibely branding from layout")
	}
}

func TestHandler_TaskCard_SelectionHandling(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task
	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Get the kanban board
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "kanban-board")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify task card has handleTaskSelect onclick handler
	if !strings.Contains(body, `onclick="handleTaskSelect(event)"`) {
		t.Error("expected task card to have handleTaskSelect onclick handler")
	}

	// Verify task title link has HTMX attributes (HTMX handles click without manual preventDefault)
	if !strings.Contains(body, `hx-get="/tasks/`) {
		t.Error("expected task title link to have hx-get attribute")
	}
}

func TestHandler_ListTasks_HTMXUpdate_ShowsAgentDefinitionBadge(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	ctx := context.Background()
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agentDef := &models.Agent{
		Name:         "Reviewer Bot",
		Description:  "Reviews code changes",
		SystemPrompt: "Review and suggest improvements.",
		Model:        "inherit",
		Tools:        []string{"Read", "Grep"},
		Skills:       []models.SkillConfig{},
		MCPServers:   []models.MCPServerConfig{},
	}
	if err := agentRepo.Create(ctx, agentDef); err != nil {
		t.Fatalf("failed to create agent definition: %v", err)
	}

	task := &models.Task{
		ProjectID:         "default",
		Title:             "Task With Agent Definition",
		Category:          models.CategoryActive,
		Status:            models.StatusPending,
		Prompt:            "Do a review",
		AgentDefinitionID: &agentDef.ID,
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "kanban-board")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `title="Assigned agent: Reviewer Bot"`) {
		t.Errorf("expected kanban card to include agent definition badge, body=%s", body)
	}
}

func TestHandler_BaseLayout_SelectionCleanup(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Create a test task so the page loads properly
	task := &models.Task{
		ProjectID: "default",
		Title:     "Test Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Test prompt",
	}
	if err := h.taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Get full page to include base layout
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	// Verify the base layout includes HTMX afterSwap listener for selection cleanup
	if !strings.Contains(body, `htmx:afterSwap`) {
		t.Error("expected base layout to have htmx:afterSwap listener for selection cleanup")
	}

	// Verify clearSelection function exists
	if !strings.Contains(body, `function clearSelection()`) {
		t.Error("expected base layout to have clearSelection function")
	}
}

func TestHandler_ListTasks_HTMXUpdate_ActiveDropZones_NoExtraPadding(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	runningTask := &models.Task{
		ProjectID: "default",
		Title:     "Running Task",
		Category:  models.CategoryActive,
		Status:    models.StatusRunning,
		Prompt:    "Running prompt",
	}
	if err := h.taskSvc.Create(ctx, runningTask); err != nil {
		t.Fatalf("failed to create running task: %v", err)
	}

	queuedTask := &models.Task{
		ProjectID: "default",
		Title:     "Queued Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "Queued prompt",
	}
	if err := h.taskSvc.Create(ctx, queuedTask); err != nil {
		t.Fatalf("failed to create queued task: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id=default", nil)
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", "kanban-board")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	body := rec.Body.String()

	if strings.Contains(body, `task-drop-zone category-drop-zone space-y-2 flex-1 p-2`) {
		t.Fatal("expected active dropzones to avoid extra inner padding class p-2")
	}

	activeDropZoneClass := `task-drop-zone category-drop-zone space-y-2 flex-1 rounded-lg border-2 border-dashed border-transparent transition-colors overflow-y-auto`
	if strings.Count(body, activeDropZoneClass) < 2 {
		t.Fatalf("expected both active sub-dropzones to share the same class without extra width-shrinking padding")
	}
}
