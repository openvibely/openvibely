package service

import (
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAutonomousTriggerService_GetBuildSummary_NoChain(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Create a project
	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a standalone task (no chain)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Feature Discovery",
		Category:  models.CategoryCompleted,
		Priority:  2,
		Status:    models.StatusCompleted,
		Prompt:    "test prompt",
		Tag:       models.TagFeature,
	}
	if err := taskSvc.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// GetBuildSummary should return empty summary (no summary task in chain)
	summary, err := svc.GetBuildSummary(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetBuildSummary: %v", err)
	}
	if summary.Summary != "" {
		t.Errorf("expected empty summary, got %q", summary.Summary)
	}
	if summary.Phase != "in_progress" {
		t.Errorf("expected phase in_progress, got %q", summary.Phase)
	}
}

func TestAutonomousTriggerService_GetBuildSummary_WithSummaryTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Get default agent for FK constraint
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil || agent == nil {
		t.Fatal("no default agent config found")
	}

	// Create a project
	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create root discovery task
	rootTask := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Feature Discovery",
		Category:  models.CategoryCompleted,
		Priority:  2,
		Status:    models.StatusCompleted,
		Prompt:    "discovery prompt",
		Tag:       models.TagFeature,
	}
	if err := taskSvc.Create(ctx, rootTask); err != nil {
		t.Fatalf("create root task: %v", err)
	}

	// Create summary task as child of root
	summaryTask := &models.Task{
		ProjectID:    project.ID,
		Title:        "[Auto Build] Build Summary",
		Category:     models.CategoryCompleted,
		Priority:     2,
		Status:       models.StatusCompleted,
		Prompt:       "summary prompt",
		Tag:          models.TagFeature,
		ParentTaskID: &rootTask.ID,
	}
	if err := taskSvc.Create(ctx, summaryTask); err != nil {
		t.Fatalf("create summary task: %v", err)
	}

	// Create execution for the summary task (Create then Complete with output)
	exec := &models.Execution{
		TaskID:        summaryTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "summary prompt",
	}
	if err := execRepo.Create(ctx, exec); err != nil {
		t.Fatalf("create execution: %v", err)
	}
	summaryOutput := "## Dashboard Analytics\n\n**What it does:** Provides real-time analytics.\n\n**How to access:** Navigate to /analytics"
	if err := execRepo.Complete(ctx, exec.ID, models.ExecCompleted, summaryOutput, "", 100, 5000); err != nil {
		t.Fatalf("complete execution: %v", err)
	}

	// GetBuildSummary should return the summary
	summary, err := svc.GetBuildSummary(ctx, rootTask.ID)
	if err != nil {
		t.Fatalf("GetBuildSummary: %v", err)
	}
	if summary.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if summary.Phase != "summary" {
		t.Errorf("expected phase summary, got %q", summary.Phase)
	}
	if !summary.Completed {
		t.Error("expected completed=true")
	}
}

func TestAutonomousTriggerService_GetBuildChainStatus(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Create a project
	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create root task
	rootTask := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Feature Discovery",
		Category:  models.CategoryCompleted,
		Priority:  2,
		Status:    models.StatusCompleted,
		Prompt:    "discovery prompt",
		Tag:       models.TagFeature,
	}
	if err := taskSvc.Create(ctx, rootTask); err != nil {
		t.Fatalf("create root task: %v", err)
	}

	// Create child task
	childTask := &models.Task{
		ProjectID:    project.ID,
		Title:        "[Auto Build] Feature Selection",
		Category:     models.CategoryActive,
		Priority:     2,
		Status:       models.StatusRunning,
		Prompt:       "selection prompt",
		Tag:          models.TagFeature,
		ParentTaskID: &rootTask.ID,
	}
	if err := taskSvc.Create(ctx, childTask); err != nil {
		t.Fatalf("create child task: %v", err)
	}

	// GetBuildChainStatus should return both tasks
	chain, err := svc.GetBuildChainStatus(ctx, rootTask.ID)
	if err != nil {
		t.Fatalf("GetBuildChainStatus: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected 2 tasks in chain, got %d", len(chain))
	}
	if chain[0].ID != rootTask.ID {
		t.Errorf("expected first task to be root, got %s", chain[0].ID)
	}
	if chain[1].ID != childTask.ID {
		t.Errorf("expected second task to be child, got %s", chain[1].ID)
	}
}

func TestAutonomousTriggerService_TriggerBuild_IncludesSummaryPhase(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Create a project
	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Trigger a build
	task, err := svc.TriggerBuild(ctx, project.ID)
	if err != nil {
		t.Fatalf("TriggerBuild: %v", err)
	}
	if task == nil {
		t.Fatal("expected task, got nil")
	}

	// Verify the chain config includes the summary phase
	config, err := task.ParseChainConfig()
	if err != nil {
		t.Fatalf("ParseChainConfig: %v", err)
	}
	if !config.Enabled {
		t.Fatal("expected chain to be enabled")
	}

	// Walk chain config to find summary phase
	current := config
	found := false
	for current != nil && current.Enabled {
		if current.ChildTitle == "[Auto Build] Build Summary" {
			found = true
			break
		}
		current = current.ChildChainConfig
	}
	if !found {
		t.Error("expected chain config to include Build Summary phase")
	}
}

func TestAutonomousTriggerService_ListRecentBuilds(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Create a project
	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create a root build task
	rootTask := &models.Task{
		ProjectID: project.ID,
		Title:     "[Auto Build] Feature Discovery",
		Category:  models.CategoryCompleted,
		Priority:  2,
		Status:    models.StatusCompleted,
		Prompt:    "test",
		Tag:       models.TagFeature,
	}
	if err := taskSvc.Create(ctx, rootTask); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// Create a non-root build task (has parent, different title)
	childTask := &models.Task{
		ProjectID:    project.ID,
		Title:        "[Auto Build] Feature Selection",
		Category:     models.CategoryCompleted,
		Priority:     2,
		Status:       models.StatusCompleted,
		Prompt:       "test",
		Tag:          models.TagFeature,
		ParentTaskID: &rootTask.ID,
	}
	if err := taskSvc.Create(ctx, childTask); err != nil {
		t.Fatalf("create child task: %v", err)
	}

	builds, err := svc.ListRecentBuilds(ctx, project.ID)
	if err != nil {
		t.Fatalf("ListRecentBuilds: %v", err)
	}
	// Should only return root task, not the child
	if len(builds) != 1 {
		t.Fatalf("expected 1 build, got %d", len(builds))
	}
	if builds[0].ID != rootTask.ID {
		t.Errorf("expected root task, got %s", builds[0].ID)
	}
}

func TestAutonomousTriggerService_TriggerBuild_WithTrendData(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	// Set up trend service
	trendSvc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)
	svc.SetTrendIntelligenceService(trendSvc)

	// Create a project
	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Add some trend data
	pattern := &models.TrendPattern{
		ProjectID:   project.ID,
		PatternType: models.PatternFeatureRequest,
		Title:       "Multi-agent workflows",
		Description: "Users want multi-agent collaboration",
		Evidence:    "[]",
		Confidence:  0.9,
		SignalCount: 10,
		Status:      models.PatternStatusActive,
	}
	if err := trendRepo.CreatePattern(ctx, pattern); err != nil {
		t.Fatalf("create pattern: %v", err)
	}

	// Trigger a build - should include trend context in the discovery prompt
	task, err := svc.TriggerBuild(ctx, project.ID)
	if err != nil {
		t.Fatalf("TriggerBuild: %v", err)
	}
	if task == nil {
		t.Fatal("expected task, got nil")
	}

	// Verify the discovery prompt contains trend intelligence
	if !strings.Contains(task.Prompt, "Multi-agent workflows") {
		t.Error("expected discovery prompt to include trend pattern title")
	}
	if !strings.Contains(task.Prompt, "External Trend Intelligence") {
		t.Error("expected discovery prompt to include trend intelligence section header")
	}

	// Verify cost analysis context is in the selection phase chain config
	config, _ := task.ParseChainConfig()
	if config == nil || !config.Enabled {
		t.Fatal("expected chain to be enabled")
	}
	if !strings.Contains(config.ChildPromptPrefix, "Cost & Scale Analysis") {
		t.Error("expected selection prompt to include cost analysis context")
	}
}

func TestAutonomousTriggerService_TriggerBuild_WithoutTrendData(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	autonomousRepo := repository.NewAutonomousRepo(db)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, attachmentRepo, workerSvc)

	// No trend service set
	svc := NewAutonomousTriggerService(taskSvc, projectRepo, taskRepo, execRepo, autonomousRepo)

	project := &models.Project{Name: "test-project", Description: "test", RepoPath: "/tmp/test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Trigger a build - should still work without trend service
	task, err := svc.TriggerBuild(ctx, project.ID)
	if err != nil {
		t.Fatalf("TriggerBuild: %v", err)
	}
	if task == nil {
		t.Fatal("expected task, got nil")
	}

	// Prompt should not contain trend intelligence section
	if strings.Contains(task.Prompt, "External Trend Intelligence") {
		t.Error("expected discovery prompt NOT to include trend intelligence when no trend service set")
	}
}
