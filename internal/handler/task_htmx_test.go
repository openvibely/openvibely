package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

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
	if !strings.Contains(body, `title="Agent: Reviewer Bot"`) {
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
