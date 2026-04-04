package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupArchitectTest(t *testing.T) (*ArchitectService, *repository.ProjectRepo, *repository.TaskRepo) {
	t.Helper()
	db := testutil.NewTestDB(t)
	architectRepo := repository.NewArchitectRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewArchitectService(architectRepo, taskRepo, projectRepo, llmConfigRepo)
	return svc, projectRepo, taskRepo
}

func TestArchitectService_CreateSession(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := svc.CreateSession(ctx, project.ID, "My App", "A cool app", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if session.ID == "" {
		t.Fatal("expected session ID")
	}
	if session.Title != "My App" {
		t.Errorf("expected title 'My App', got %q", session.Title)
	}
	if session.Phase != models.PhaseVisionRefinement {
		t.Errorf("expected phase vision_refinement, got %q", session.Phase)
	}

	// Should have initial message
	detail, err := svc.GetSessionDetail(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSessionDetail: %v", err)
	}
	if len(detail.Messages) != 1 {
		t.Errorf("expected 1 initial message, got %d", len(detail.Messages))
	}
	if detail.Messages[0].Role != "assistant" {
		t.Errorf("expected initial message role 'assistant', got %q", detail.Messages[0].Role)
	}
}

func TestArchitectService_GetDashboard(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create sessions
	svc.CreateSession(ctx, project.ID, "Session 1", "desc", nil)
	svc.CreateSession(ctx, project.ID, "Session 2", "desc", nil)

	dashboard, err := svc.GetDashboard(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	if len(dashboard.ActiveSessions) != 2 {
		t.Errorf("expected 2 active sessions, got %d", len(dashboard.ActiveSessions))
	}
	if len(dashboard.Templates) < 3 {
		t.Errorf("expected at least 3 templates, got %d", len(dashboard.Templates))
	}
}

func TestArchitectService_SendMessage(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := svc.CreateSession(ctx, project.ID, "My App", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Send message (will use fallback since no LLM service)
	reply, err := svc.SendMessage(ctx, session.ID, "I'm building a task management app")
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if reply.Role != "assistant" {
		t.Errorf("expected role 'assistant', got %q", reply.Role)
	}
	if reply.Content == "" {
		t.Error("expected non-empty content")
	}

	// Check messages are stored
	detail, err := svc.GetSessionDetail(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSessionDetail: %v", err)
	}
	// 1 initial + 1 user + 1 assistant
	if len(detail.Messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(detail.Messages))
	}
}

func TestArchitectService_AdvancePhase(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := svc.CreateSession(ctx, project.ID, "My App", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Advance from vision_refinement to architecture
	updated, err := svc.AdvancePhase(ctx, session.ID)
	if err != nil {
		t.Fatalf("AdvancePhase: %v", err)
	}
	if updated.Phase != models.PhaseArchitecture {
		t.Errorf("expected phase architecture, got %q", updated.Phase)
	}

	// Advance to risk_analysis
	updated, err = svc.AdvancePhase(ctx, session.ID)
	if err != nil {
		t.Fatalf("AdvancePhase: %v", err)
	}
	if updated.Phase != models.PhaseRiskAnalysis {
		t.Errorf("expected phase risk_analysis, got %q", updated.Phase)
	}
}

func TestArchitectService_AbandonSession(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := svc.CreateSession(ctx, project.ID, "My App", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if err := svc.AbandonSession(ctx, session.ID); err != nil {
		t.Fatalf("AbandonSession: %v", err)
	}

	detail, err := svc.GetSessionDetail(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetSessionDetail: %v", err)
	}
	if detail.Session.Status != models.ArchitectStatusAbandoned {
		t.Errorf("expected status abandoned, got %q", detail.Session.Status)
	}
}

func TestArchitectService_SaveAsTemplate(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := svc.CreateSession(ctx, project.ID, "My App", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	tmpl, err := svc.SaveAsTemplate(ctx, session.ID, "My Template", "A custom template", "web_app")
	if err != nil {
		t.Fatalf("SaveAsTemplate: %v", err)
	}
	if tmpl.ID == "" {
		t.Fatal("expected template ID")
	}
	if tmpl.Name != "My Template" {
		t.Errorf("expected name 'My Template', got %q", tmpl.Name)
	}
	if tmpl.Category != "web_app" {
		t.Errorf("expected category 'web_app', got %q", tmpl.Category)
	}
}

func TestArchitectService_CreateSessionFromTemplate(t *testing.T) {
	svc, projectRepo, _ := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Get existing templates (seeded by migration)
	templates, err := svc.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(templates) == 0 {
		t.Fatal("expected at least 1 seeded template")
	}

	tmplID := templates[0].ID
	session, err := svc.CreateSession(ctx, project.ID, "From Template", "desc", &tmplID)
	if err != nil {
		t.Fatalf("CreateSession from template: %v", err)
	}
	if session.VisionData == "{}" {
		t.Error("expected vision data to be pre-populated from template")
	}
}

func TestArchitectService_ParseJSONFromAI(t *testing.T) {
	// Test plain JSON
	var result models.RiskAnalysis
	input := `{"risks": [], "summary": "No risks"}`
	if err := parseArchitectJSONFromAI(input, &result); err != nil {
		t.Errorf("failed to parse plain JSON: %v", err)
	}
	if result.Summary != "No risks" {
		t.Errorf("expected 'No risks', got %q", result.Summary)
	}

	// Test with markdown fences
	input2 := "```json\n{\"risks\": [], \"summary\": \"With fences\"}\n```"
	var result2 models.RiskAnalysis
	if err := parseArchitectJSONFromAI(input2, &result2); err != nil {
		t.Errorf("failed to parse fenced JSON: %v", err)
	}
	if result2.Summary != "With fences" {
		t.Errorf("expected 'With fences', got %q", result2.Summary)
	}

	// Test with surrounding text
	input3 := "Here is the analysis:\n{\"risks\": [], \"summary\": \"Embedded\"}\nThat's it."
	var result3 models.RiskAnalysis
	if err := parseArchitectJSONFromAI(input3, &result3); err != nil {
		t.Errorf("failed to parse embedded JSON: %v", err)
	}
	if result3.Summary != "Embedded" {
		t.Errorf("expected 'Embedded', got %q", result3.Summary)
	}
}

func TestArchitectService_BuildPhaseBreakdown(t *testing.T) {
	tasks := []models.ArchitectTask{
		{Phase: models.TaskPhaseMVP, Title: "Task 1", EstHours: 8},
		{Phase: models.TaskPhaseMVP, Title: "Task 2", EstHours: 4},
		{Phase: models.TaskPhaseTwo, Title: "Task 3", EstHours: 16},
		{Phase: models.TaskPhaseThree, Title: "Task 4", EstHours: 6},
	}

	breakdown := buildArchitectPhaseBreakdown(tasks)
	if len(breakdown.MVP.Features) != 2 {
		t.Errorf("expected 2 MVP features, got %d", len(breakdown.MVP.Features))
	}
	if len(breakdown.Phase2.Features) != 1 {
		t.Errorf("expected 1 Phase 2 feature, got %d", len(breakdown.Phase2.Features))
	}
	if len(breakdown.Phase3.Features) != 1 {
		t.Errorf("expected 1 Phase 3 feature, got %d", len(breakdown.Phase3.Features))
	}
}

func TestArchitectService_ComputeResourceEstimate(t *testing.T) {
	tasks := []models.ArchitectTask{
		{Phase: models.TaskPhaseMVP, EstHours: 8},
		{Phase: models.TaskPhaseMVP, EstHours: 4},
		{Phase: models.TaskPhaseTwo, EstHours: 16},
	}

	est := computeArchitectResourceEstimate(tasks)
	if est.TotalTasks != 3 {
		t.Errorf("expected 3 total tasks, got %d", est.TotalTasks)
	}
	if est.TotalHours != 28 {
		t.Errorf("expected 28 total hours, got %.0f", est.TotalHours)
	}
	if est.Complexity != "simple" {
		t.Errorf("expected simple complexity, got %q", est.Complexity)
	}
	if _, ok := est.ByPhase["mvp"]; !ok {
		t.Error("expected mvp phase in breakdown")
	}
}

func TestArchitectService_GetNextPhase(t *testing.T) {
	tests := []struct {
		current  models.ArchitectPhase
		expected models.ArchitectPhase
	}{
		{models.PhaseVisionRefinement, models.PhaseArchitecture},
		{models.PhaseArchitecture, models.PhaseRiskAnalysis},
		{models.PhaseRiskAnalysis, models.PhasePhasing},
		{models.PhasePhasing, models.PhaseDependencies},
		{models.PhaseDependencies, models.PhaseEstimation},
		{models.PhaseEstimation, models.PhaseReview},
		{models.PhaseReview, models.PhaseComplete},
		{models.PhaseComplete, models.PhaseComplete}, // stays at complete
	}
	for _, tt := range tests {
		got := getNextArchitectPhase(tt.current)
		if got != tt.expected {
			t.Errorf("getNextArchitectPhase(%s) = %s, want %s", tt.current, got, tt.expected)
		}
	}
}

func TestArchitectService_ActivatePhase(t *testing.T) {
	svc, projectRepo, taskRepo := setupArchitectTest(t)
	ctx := context.Background()

	project := &models.Project{Name: "Test", Description: "desc", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	session, err := svc.CreateSession(ctx, project.ID, "My App", "desc", nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Manually create some architect tasks (since we can't use LLM in tests)
	architectRepo := svc.architectRepo
	task1 := &models.ArchitectTask{
		SessionID: session.ID, Title: "Setup", Prompt: "Setup the project",
		Phase: models.TaskPhaseMVP, Priority: 4, DependsOn: "[]", Complexity: "low", EstHours: 2,
	}
	task2 := &models.ArchitectTask{
		SessionID: session.ID, Title: "Auth", Prompt: "Add authentication",
		Phase: models.TaskPhaseMVP, Priority: 3, DependsOn: "[]", Complexity: "medium", EstHours: 8,
	}
	if err := architectRepo.CreateTask(ctx, task1); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := architectRepo.CreateTask(ctx, task2); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Activate MVP phase
	count, err := svc.ActivatePhase(ctx, session.ID, models.TaskPhaseMVP, project.ID)
	if err != nil {
		t.Fatalf("ActivatePhase: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 activated, got %d", count)
	}

	// Verify real tasks were created
	realTasks, err := taskRepo.ListByProject(ctx, project.ID, "backlog")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(realTasks) != 2 {
		t.Errorf("expected 2 real tasks, got %d", len(realTasks))
	}

	// Try activating again — should not duplicate
	count2, err := svc.ActivatePhase(ctx, session.ID, models.TaskPhaseMVP, project.ID)
	if err != nil {
		t.Fatalf("ActivatePhase second time: %v", err)
	}
	if count2 != 0 {
		t.Errorf("expected 0 activated on second run, got %d", count2)
	}
}

func TestArchitectService_ExtractVisionSummary(t *testing.T) {
	messages := []models.ArchitectMessage{
		{Role: "assistant", Content: "What is your project?", Phase: models.PhaseVisionRefinement},
		{Role: "user", Content: "A task management app", Phase: models.PhaseVisionRefinement},
		{Role: "assistant", Content: "Great! Who are the users?", Phase: models.PhaseVisionRefinement},
		{Role: "user", Content: "Developers and PMs", Phase: models.PhaseVisionRefinement},
		{Role: "user", Content: "Architecture discussion", Phase: models.PhaseArchitecture}, // different phase
	}

	summary := extractArchitectVisionSummary(messages)
	var data models.VisionRefinementData
	if err := json.Unmarshal([]byte(summary), &data); err != nil {
		t.Fatalf("failed to parse summary: %v", err)
	}
	if data.Summary == "" {
		t.Error("expected non-empty summary")
	}
	// Should only include vision_refinement phase user messages
	if !architectContains(data.Summary, "task management") {
		t.Error("expected summary to contain 'task management'")
	}
	if !architectContains(data.Summary, "Developers and PMs") {
		t.Error("expected summary to contain 'Developers and PMs'")
	}
	if architectContains(data.Summary, "Architecture discussion") {
		t.Error("did not expect architecture phase messages in vision summary")
	}
}

func architectContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
