package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestGetDefaultAgentForTask_ProjectDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	projectAgent := &models.LLMConfig{
		Name:     "Project Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	if err := llmConfigRepo.Create(ctx, projectAgent); err != nil {
		t.Fatalf("Create project agent: %v", err)
	}

	project := &models.Project{
		Name:                 "Test Project Default Agent",
		DefaultAgentConfigID: &projectAgent.ID,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	agent, err := svc.getDefaultAgentForTask(ctx, project.ID)
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected agent, got nil")
	}
	if agent.ID != projectAgent.ID {
		t.Errorf("expected project agent ID=%s, got %s", projectAgent.ID, agent.ID)
	}
	if agent.Name != "Project Agent" {
		t.Errorf("expected agent Name=Project Agent, got %q", agent.Name)
	}
}

func TestGetDefaultAgentForTask_FallsBackToGlobalDefault(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	project := &models.Project{
		Name: "No Default Agent Project",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	agent, err := svc.getDefaultAgentForTask(ctx, project.ID)
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected global default agent, got nil")
	}
	if !agent.IsDefault {
		t.Error("expected the global default agent (IsDefault=true)")
	}
}

func TestGetDefaultAgentForTask_EmptyProjectID(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	agent, err := svc.getDefaultAgentForTask(ctx, "")
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected global default agent, got nil")
	}
	if !agent.IsDefault {
		t.Error("expected the global default agent (IsDefault=true)")
	}
}

func TestGetDefaultAgentForTask_DeletedProjectAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(testutil.NewMockLLMCaller())

	projectAgent := &models.LLMConfig{
		Name:     "Temporary Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	llmConfigRepo.Create(ctx, projectAgent)

	project := &models.Project{
		Name:                 "Project With Deleted Agent",
		DefaultAgentConfigID: &projectAgent.ID,
	}
	projectRepo.Create(ctx, project)

	llmConfigRepo.Delete(ctx, projectAgent.ID)

	agent, err := svc.getDefaultAgentForTask(ctx, project.ID)
	if err != nil {
		t.Fatalf("getDefaultAgentForTask: %v", err)
	}
	if agent == nil {
		t.Fatal("expected global default agent after project agent deleted, got nil")
	}
	if !agent.IsDefault {
		t.Error("expected the global default agent (IsDefault=true)")
	}
}

func TestLLMService_ExecuteTaskWithAgent_UsesProjectRepoPathAsWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	project := &models.Project{
		Name:     "Chrome Plugin",
		RepoPath: "/Users/dubee/go/src/github.com/openvibely/chrome-plugin",
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Debug Microphone Issue",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "debug the microphone issue",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != project.RepoPath {
		t.Errorf("expected workDir=%q, got %q", project.RepoPath, got)
	}
}

func TestLLMService_CallClaudeCLI_SetsWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	projectDir := "/tmp/test-workdir-claude"
	project := &models.Project{
		Name:     "Test Project",
		RepoPath: projectDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "WorkDir Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != projectDir {
		t.Errorf("expected workDir=%q, got %q", projectDir, got)
	}
}

func TestLLMService_CallClaudeCLI_NoWorkDirWhenProjectHasNoRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	task := &models.Task{
		ProjectID: "default",
		Title:     "No RepoPath Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	exec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != "" {
		t.Errorf("expected empty workDir for project without RepoPath, got %q", got)
	}
}

func TestLLMService_CallCodexCLI_SetsWorkDir(t *testing.T) {
	db := testutil.NewTestDB(t)
	execRepo := repository.NewExecutionRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	ctx := context.Background()

	mock := testutil.NewMockLLMCaller()
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	svc.SetLLMCaller(mock)

	projectDir := "/tmp/test-workdir-codex"
	project := &models.Project{
		Name:     "Codex WorkDir Project",
		RepoPath: projectDir,
	}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Codex WorkDir Test",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agent := ensureDefaultAgent(t, llmConfigRepo)

	execRec, err := svc.ExecuteTaskWithAgent(ctx, *task, *agent)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if execRec == nil {
		t.Fatal("expected execution record")
	}

	if mock.CallCount() == 0 {
		t.Fatal("expected mock to be called")
	}
	if got := mock.LastCall().WorkDir; got != projectDir {
		t.Errorf("expected workDir=%q, got %q", projectDir, got)
	}
}
