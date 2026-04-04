package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createArchitectTestProject(t *testing.T, projectRepo *ProjectRepo) *models.Project {
	t.Helper()
	p := &models.Project{Name: "Test Project", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}
	return p
}

func TestArchitectRepo_SessionCRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewArchitectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := createArchitectTestProject(t, projectRepo)

	// Create session
	session := &models.ArchitectSession{
		ProjectID:   project.ID,
		Title:       "Test Vision",
		Description: "Building a test app",
		Status:      models.ArchitectStatusActive,
		Phase:       models.PhaseVisionRefinement,
		VisionData:  "{}",
		ArchData:    "{}",
		RiskData:    "{}",
		PhaseData:   "{}",
		DepData:     "{}",
		EstData:     "{}",
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}
	if session.ID == "" {
		t.Fatal("expected session ID to be set")
	}

	// Get session
	got, err := repo.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected session, got nil")
	}
	if got.Title != "Test Vision" {
		t.Errorf("expected title 'Test Vision', got %q", got.Title)
	}
	if got.Phase != models.PhaseVisionRefinement {
		t.Errorf("expected phase vision_refinement, got %q", got.Phase)
	}

	// Update session
	session.Phase = models.PhaseArchitecture
	session.ArchData = `{"summary":"test arch"}`
	if err := repo.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession failed: %v", err)
	}
	got, _ = repo.GetSession(ctx, session.ID)
	if got.Phase != models.PhaseArchitecture {
		t.Errorf("expected phase architecture, got %q", got.Phase)
	}

	// List by project
	sessions, err := repo.ListSessionsByProject(ctx, project.ID, models.ArchitectStatusActive)
	if err != nil {
		t.Fatalf("ListSessionsByProject failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session, got %d", len(sessions))
	}

	// Delete session
	if err := repo.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}
	got, _ = repo.GetSession(ctx, session.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestArchitectRepo_Messages(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewArchitectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := createArchitectTestProject(t, projectRepo)
	session := &models.ArchitectSession{
		ProjectID: project.ID, Title: "Vision", Status: models.ArchitectStatusActive,
		Phase: models.PhaseVisionRefinement, VisionData: "{}", ArchData: "{}",
		RiskData: "{}", PhaseData: "{}", DepData: "{}", EstData: "{}",
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Create messages
	msg1 := &models.ArchitectMessage{SessionID: session.ID, Role: "assistant", Content: "What is your project about?", Phase: models.PhaseVisionRefinement}
	msg2 := &models.ArchitectMessage{SessionID: session.ID, Role: "user", Content: "A task management app", Phase: models.PhaseVisionRefinement}
	if err := repo.CreateMessage(ctx, msg1); err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}
	if err := repo.CreateMessage(ctx, msg2); err != nil {
		t.Fatalf("CreateMessage failed: %v", err)
	}

	messages, err := repo.ListMessages(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListMessages failed: %v", err)
	}
	if len(messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(messages))
	}
	if messages[0].Role != "assistant" {
		t.Errorf("expected first message role assistant, got %q", messages[0].Role)
	}
	if messages[1].Content != "A task management app" {
		t.Errorf("expected second message content, got %q", messages[1].Content)
	}
}

func TestArchitectRepo_Tasks(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewArchitectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := createArchitectTestProject(t, projectRepo)
	session := &models.ArchitectSession{
		ProjectID: project.ID, Title: "Vision", Status: models.ArchitectStatusActive,
		Phase: models.PhaseVisionRefinement, VisionData: "{}", ArchData: "{}",
		RiskData: "{}", PhaseData: "{}", DepData: "{}", EstData: "{}",
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Create tasks
	task1 := &models.ArchitectTask{
		SessionID: session.ID, Title: "Setup project", Prompt: "Create scaffold",
		Phase: models.TaskPhaseMVP, Priority: 4, DependsOn: "[]",
		IsBlocking: true, Complexity: "low", EstHours: 2,
	}
	task2 := &models.ArchitectTask{
		SessionID: session.ID, Title: "Add auth", Prompt: "Implement auth",
		Phase: models.TaskPhaseTwo, Priority: 3, DependsOn: "[]",
		Complexity: "high", EstHours: 10,
	}
	if err := repo.CreateTask(ctx, task1); err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if err := repo.CreateTask(ctx, task2); err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// List all tasks
	tasks, err := repo.ListTasksBySession(ctx, session.ID)
	if err != nil {
		t.Fatalf("ListTasksBySession failed: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("expected 2 tasks, got %d", len(tasks))
	}

	// List by phase
	mvpTasks, err := repo.ListTasksByPhase(ctx, session.ID, models.TaskPhaseMVP)
	if err != nil {
		t.Fatalf("ListTasksByPhase failed: %v", err)
	}
	if len(mvpTasks) != 1 {
		t.Errorf("expected 1 MVP task, got %d", len(mvpTasks))
	}
	if !mvpTasks[0].IsBlocking {
		t.Error("expected task to be blocking")
	}

	// Create a real task to satisfy FK constraint
	taskRepo := NewTaskRepo(db, nil)
	realTask := &models.Task{
		ProjectID: project.ID,
		Title:     "Real Task",
		Prompt:    "test",
		Category:  models.CategoryBacklog,
		Priority:  2,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, realTask); err != nil {
		t.Fatalf("create real task: %v", err)
	}

	// Activate task
	if err := repo.ActivateTask(ctx, task1.ID, realTask.ID); err != nil {
		t.Fatalf("ActivateTask failed: %v", err)
	}
	tasks, _ = repo.ListTasksBySession(ctx, session.ID)
	for _, task := range tasks {
		if task.ID == task1.ID {
			if !task.IsActivated {
				t.Error("expected task to be activated")
			}
			if task.TaskID == nil || *task.TaskID != realTask.ID {
				t.Error("expected real task ID to be set")
			}
		}
	}
}

func TestArchitectRepo_Templates(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewArchitectRepo(db)
	ctx := context.Background()

	// List built-in templates (seeded by migration)
	templates, err := repo.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates failed: %v", err)
	}
	if len(templates) < 3 {
		t.Errorf("expected at least 3 seeded templates, got %d", len(templates))
	}

	// Create a custom template
	tmpl := &models.ArchitectTemplate{
		Name:        "Custom Template",
		Description: "A custom project template",
		Category:    "custom",
		VisionData:  `{"goals":"test"}`,
		ArchData:    `{"summary":"test"}`,
		TasksData:   `[]`,
	}
	if err := repo.CreateTemplate(ctx, tmpl); err != nil {
		t.Fatalf("CreateTemplate failed: %v", err)
	}
	if tmpl.ID == "" {
		t.Fatal("expected template ID to be set")
	}

	// Get template
	got, err := repo.GetTemplate(ctx, tmpl.ID)
	if err != nil {
		t.Fatalf("GetTemplate failed: %v", err)
	}
	if got.Name != "Custom Template" {
		t.Errorf("expected name 'Custom Template', got %q", got.Name)
	}

	// Increment usage
	if err := repo.IncrementTemplateUsage(ctx, tmpl.ID); err != nil {
		t.Fatalf("IncrementTemplateUsage failed: %v", err)
	}
	got, _ = repo.GetTemplate(ctx, tmpl.ID)
	if got.UsageCount != 1 {
		t.Errorf("expected usage_count 1, got %d", got.UsageCount)
	}

	// Delete template
	if err := repo.DeleteTemplate(ctx, tmpl.ID); err != nil {
		t.Fatalf("DeleteTemplate failed: %v", err)
	}
	got, _ = repo.GetTemplate(ctx, tmpl.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestArchitectRepo_EmptyLists(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewArchitectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := createArchitectTestProject(t, projectRepo)

	// Empty sessions list should return empty slice, not nil
	sessions, err := repo.ListSessionsByProject(ctx, project.ID, models.ArchitectStatusActive)
	if err != nil {
		t.Fatalf("ListSessionsByProject failed: %v", err)
	}
	if sessions == nil {
		t.Error("expected empty slice, got nil")
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestArchitectRepo_CascadeDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewArchitectRepo(db)
	projectRepo := NewProjectRepo(db)
	ctx := context.Background()

	project := createArchitectTestProject(t, projectRepo)
	session := &models.ArchitectSession{
		ProjectID: project.ID, Title: "Vision", Status: models.ArchitectStatusActive,
		Phase: models.PhaseVisionRefinement, VisionData: "{}", ArchData: "{}",
		RiskData: "{}", PhaseData: "{}", DepData: "{}", EstData: "{}",
	}
	if err := repo.CreateSession(ctx, session); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Add messages and tasks
	msg := &models.ArchitectMessage{SessionID: session.ID, Role: "user", Content: "test", Phase: models.PhaseVisionRefinement}
	repo.CreateMessage(ctx, msg)
	task := &models.ArchitectTask{
		SessionID: session.ID, Title: "Test", Prompt: "Test",
		Phase: models.TaskPhaseMVP, Priority: 2, DependsOn: "[]", Complexity: "low",
	}
	repo.CreateTask(ctx, task)

	// Delete session should cascade
	if err := repo.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	messages, _ := repo.ListMessages(ctx, session.ID)
	if len(messages) != 0 {
		t.Errorf("expected messages to be cascade deleted, got %d", len(messages))
	}
	tasks, _ := repo.ListTasksBySession(ctx, session.ID)
	if len(tasks) != 0 {
		t.Errorf("expected tasks to be cascade deleted, got %d", len(tasks))
	}
}
