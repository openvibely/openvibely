package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
