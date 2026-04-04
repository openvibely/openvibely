package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestWorkflowRepo_TemplatesCRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	ctx := context.Background()

	// List built-in templates (seeded by migration)
	templates, err := repo.ListTemplates(ctx)
	if err != nil {
		t.Fatalf("ListTemplates error: %v", err)
	}
	if len(templates) < 4 {
		t.Fatalf("expected at least 4 built-in templates, got %d", len(templates))
	}

	// Verify built-in template content
	var fullFeature *models.WorkflowTemplate
	for i := range templates {
		if templates[i].Name == "Full Feature" {
			fullFeature = &templates[i]
			break
		}
	}
	if fullFeature == nil {
		t.Fatal("Full Feature template not found")
	}
	if fullFeature.Category != models.TemplateCategoryFeature {
		t.Errorf("expected category=feature, got %s", fullFeature.Category)
	}
	if !fullFeature.IsBuiltIn {
		t.Error("expected is_built_in=true")
	}

	// Parse template definition
	def, err := fullFeature.ParseTemplateDefinition()
	if err != nil {
		t.Fatalf("ParseTemplateDefinition error: %v", err)
	}
	if len(def.Steps) == 0 {
		t.Error("expected steps in Full Feature template")
	}

	// Create custom template
	custom := &models.WorkflowTemplate{
		Name:        "My Custom Template",
		Description: "A test template",
		Category:    models.TemplateCategoryCustom,
		Definition:  `{"strategy":"sequential","config":{},"steps":[]}`,
	}
	if err := repo.CreateTemplate(ctx, custom); err != nil {
		t.Fatalf("CreateTemplate error: %v", err)
	}
	if custom.ID == "" {
		t.Error("expected template ID to be set")
	}

	// Get template
	got, err := repo.GetTemplate(ctx, custom.ID)
	if err != nil {
		t.Fatalf("GetTemplate error: %v", err)
	}
	if got.Name != "My Custom Template" {
		t.Errorf("expected name='My Custom Template', got %q", got.Name)
	}

	// Delete custom (should work)
	if err := repo.DeleteTemplate(ctx, custom.ID); err != nil {
		t.Fatalf("DeleteTemplate error: %v", err)
	}

	// Delete built-in (should not delete due to is_built_in=0 filter)
	if err := repo.DeleteTemplate(ctx, fullFeature.ID); err != nil {
		t.Fatalf("DeleteTemplate built-in error: %v", err)
	}
	// Built-in should still exist
	still, err := repo.GetTemplate(ctx, fullFeature.ID)
	if err != nil {
		t.Fatalf("GetTemplate after delete attempt error: %v", err)
	}
	if still == nil {
		t.Error("built-in template should not be deleted")
	}
}

func TestWorkflowRepo_WorkflowCRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	ctx := context.Background()

	// Create workflow
	w := &models.Workflow{
		ProjectID:   "default",
		Name:        "Test Workflow",
		Description: "A test workflow",
		Strategy:    models.StrategySequential,
		Config:      `{"max_retries":2}`,
	}
	if err := repo.CreateWorkflow(ctx, w); err != nil {
		t.Fatalf("CreateWorkflow error: %v", err)
	}
	if w.ID == "" {
		t.Error("expected workflow ID to be set")
	}

	// Get workflow
	got, err := repo.GetWorkflow(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkflow error: %v", err)
	}
	if got.Name != "Test Workflow" {
		t.Errorf("expected name='Test Workflow', got %q", got.Name)
	}
	if got.Strategy != models.StrategySequential {
		t.Errorf("expected strategy=sequential, got %s", got.Strategy)
	}

	// List workflows
	workflows, err := repo.ListWorkflows(ctx, "default")
	if err != nil {
		t.Fatalf("ListWorkflows error: %v", err)
	}
	if len(workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(workflows))
	}

	// Update workflow
	got.Name = "Updated Workflow"
	got.Strategy = models.StrategyParallel
	if err := repo.UpdateWorkflow(ctx, got); err != nil {
		t.Fatalf("UpdateWorkflow error: %v", err)
	}
	updated, err := repo.GetWorkflow(ctx, w.ID)
	if err != nil {
		t.Fatalf("GetWorkflow after update error: %v", err)
	}
	if updated.Name != "Updated Workflow" {
		t.Errorf("expected name='Updated Workflow', got %q", updated.Name)
	}

	// Delete workflow
	if err := repo.DeleteWorkflow(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkflow error: %v", err)
	}
	workflows, err = repo.ListWorkflows(ctx, "default")
	if err != nil {
		t.Fatalf("ListWorkflows after delete error: %v", err)
	}
	if len(workflows) != 0 {
		t.Errorf("expected 0 workflows, got %d", len(workflows))
	}
}

func TestWorkflowRepo_StepsCRUD(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	ctx := context.Background()

	// Create workflow first
	w := &models.Workflow{
		ProjectID: "default",
		Name:      "Step Test Workflow",
		Strategy:  models.StrategySequential,
		Config:    "{}",
	}
	if err := repo.CreateWorkflow(ctx, w); err != nil {
		t.Fatalf("CreateWorkflow error: %v", err)
	}

	// Create steps
	step1 := &models.WorkflowStep{
		WorkflowID: w.ID,
		Name:       "Plan",
		StepType:   models.StepTypeExecute,
		StepOrder:  0,
		Prompt:     "Plan the implementation",
		DependsOn:  "[]",
		Config:     "{}",
	}
	if err := repo.CreateStep(ctx, step1); err != nil {
		t.Fatalf("CreateStep 1 error: %v", err)
	}

	step2 := &models.WorkflowStep{
		WorkflowID: w.ID,
		Name:       "Implement",
		StepType:   models.StepTypeExecute,
		StepOrder:  1,
		Prompt:     "Implement based on plan: {{prev_output}}",
		DependsOn:  "[]",
		Config:     "{}",
	}
	if err := repo.CreateStep(ctx, step2); err != nil {
		t.Fatalf("CreateStep 2 error: %v", err)
	}

	// List steps
	steps, err := repo.ListSteps(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListSteps error: %v", err)
	}
	if len(steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(steps))
	}
	if steps[0].Name != "Plan" {
		t.Errorf("expected first step='Plan', got %q", steps[0].Name)
	}
	if steps[1].Name != "Implement" {
		t.Errorf("expected second step='Implement', got %q", steps[1].Name)
	}

	// Get step
	got, err := repo.GetStep(ctx, step1.ID)
	if err != nil {
		t.Fatalf("GetStep error: %v", err)
	}
	if got.Name != "Plan" {
		t.Errorf("expected name='Plan', got %q", got.Name)
	}

	// Delete step
	if err := repo.DeleteStep(ctx, step1.ID); err != nil {
		t.Fatalf("DeleteStep error: %v", err)
	}
	steps, err = repo.ListSteps(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListSteps after delete error: %v", err)
	}
	if len(steps) != 1 {
		t.Errorf("expected 1 step, got %d", len(steps))
	}
}

func TestWorkflowRepo_WorkflowExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Create a task for the workflow execution
	task := &models.Task{
		ProjectID:   "default",
		Title:       "Workflow Test Task",
		Category:    models.CategoryBacklog,
		Status:      models.StatusPending,
		Prompt:      "Test prompt",
		ChainConfig: "{}",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Create task error: %v", err)
	}

	// Create workflow
	w := &models.Workflow{
		ProjectID: "default",
		Name:      "Exec Test Workflow",
		Strategy:  models.StrategySequential,
		Config:    "{}",
	}
	if err := repo.CreateWorkflow(ctx, w); err != nil {
		t.Fatalf("CreateWorkflow error: %v", err)
	}

	// Create workflow execution
	we := &models.WorkflowExecution{
		WorkflowID: w.ID,
		TaskID:     task.ID,
		Status:     models.WorkflowPending,
		Context:    "{}",
	}
	if err := repo.CreateWorkflowExecution(ctx, we); err != nil {
		t.Fatalf("CreateWorkflowExecution error: %v", err)
	}
	if we.ID == "" {
		t.Error("expected workflow execution ID to be set")
	}

	// Claim execution
	claimed, err := repo.ClaimWorkflowExecution(ctx, we.ID)
	if err != nil {
		t.Fatalf("ClaimWorkflowExecution error: %v", err)
	}
	if !claimed {
		t.Error("expected to claim execution")
	}

	// Try to claim again (should fail)
	claimed2, err := repo.ClaimWorkflowExecution(ctx, we.ID)
	if err != nil {
		t.Fatalf("ClaimWorkflowExecution (2nd) error: %v", err)
	}
	if claimed2 {
		t.Error("should not be able to claim already-running execution")
	}

	// Get execution
	got, err := repo.GetWorkflowExecution(ctx, we.ID)
	if err != nil {
		t.Fatalf("GetWorkflowExecution error: %v", err)
	}
	if got.Status != models.WorkflowRunning {
		t.Errorf("expected status=running, got %s", got.Status)
	}

	// List by workflow
	execs, err := repo.ListWorkflowExecutions(ctx, w.ID)
	if err != nil {
		t.Fatalf("ListWorkflowExecutions error: %v", err)
	}
	if len(execs) != 1 {
		t.Errorf("expected 1 execution, got %d", len(execs))
	}

	// List by task
	taskExecs, err := repo.ListWorkflowExecutionsByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ListWorkflowExecutionsByTask error: %v", err)
	}
	if len(taskExecs) != 1 {
		t.Errorf("expected 1 execution, got %d", len(taskExecs))
	}

	// Complete execution
	if err := repo.CompleteWorkflowExecution(ctx, we.ID, models.WorkflowCompleted, ""); err != nil {
		t.Fatalf("CompleteWorkflowExecution error: %v", err)
	}
	completed, err := repo.GetWorkflowExecution(ctx, we.ID)
	if err != nil {
		t.Fatalf("GetWorkflowExecution after complete error: %v", err)
	}
	if completed.Status != models.WorkflowCompleted {
		t.Errorf("expected status=completed, got %s", completed.Status)
	}
	if completed.CompletedAt == nil {
		t.Error("expected completed_at to be set")
	}
}

func TestWorkflowRepo_StepExecution(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	ctx := context.Background()

	// Set up prerequisite records
	task := &models.Task{
		ProjectID: "default", Title: "SE Test", Category: models.CategoryBacklog,
		Status: models.StatusPending, Prompt: "Test", ChainConfig: "{}",
	}
	taskRepo.Create(ctx, task)

	w := &models.Workflow{ProjectID: "default", Name: "SE Workflow", Strategy: models.StrategySequential, Config: "{}"}
	repo.CreateWorkflow(ctx, w)

	step := &models.WorkflowStep{WorkflowID: w.ID, Name: "Step1", StepType: models.StepTypeExecute, StepOrder: 0, Prompt: "Do something", DependsOn: "[]", Config: "{}"}
	repo.CreateStep(ctx, step)

	we := &models.WorkflowExecution{WorkflowID: w.ID, TaskID: task.ID, Status: models.WorkflowRunning, Context: "{}"}
	repo.CreateWorkflowExecution(ctx, we)

	// Create step execution
	agentID := ""
	se := &models.StepExecution{
		WorkflowExecutionID: we.ID,
		StepID:              step.ID,
		AgentConfigID:       &agentID,
		Status:              models.StepRunning,
		Input:               "Test input",
	}
	// Set AgentConfigID to nil since we don't have a valid agent
	se.AgentConfigID = nil
	if err := repo.CreateStepExecution(ctx, se); err != nil {
		t.Fatalf("CreateStepExecution error: %v", err)
	}
	if se.ID == "" {
		t.Error("expected step execution ID to be set")
	}

	// Get step execution
	got, err := repo.GetStepExecution(ctx, se.ID)
	if err != nil {
		t.Fatalf("GetStepExecution error: %v", err)
	}
	if got.Input != "Test input" {
		t.Errorf("expected input='Test input', got %q", got.Input)
	}

	// Update step execution
	score := 0.85
	got.Status = models.StepCompleted
	got.Output = "Step output here"
	got.Score = &score
	if err := repo.UpdateStepExecution(ctx, got); err != nil {
		t.Fatalf("UpdateStepExecution error: %v", err)
	}

	// Get latest step execution
	latest, err := repo.GetLatestStepExecution(ctx, we.ID, step.ID)
	if err != nil {
		t.Fatalf("GetLatestStepExecution error: %v", err)
	}
	if latest.Output != "Step output here" {
		t.Errorf("expected output='Step output here', got %q", latest.Output)
	}
	if latest.Score == nil || *latest.Score != 0.85 {
		t.Error("expected score=0.85")
	}

	// List step executions
	stepExecs, err := repo.ListStepExecutions(ctx, we.ID)
	if err != nil {
		t.Fatalf("ListStepExecutions error: %v", err)
	}
	if len(stepExecs) != 1 {
		t.Errorf("expected 1 step execution, got %d", len(stepExecs))
	}
}

func TestWorkflowRepo_VoteRecords(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	llmConfigRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Get default agent
	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault agent error: %v", err)
	}

	// Set up chain
	task := &models.Task{
		ProjectID: "default", Title: "Vote Test", Category: models.CategoryBacklog,
		Status: models.StatusPending, Prompt: "Test", ChainConfig: "{}",
	}
	taskRepo.Create(ctx, task)

	w := &models.Workflow{ProjectID: "default", Name: "Vote Workflow", Strategy: models.StrategySequential, Config: "{}"}
	repo.CreateWorkflow(ctx, w)

	step := &models.WorkflowStep{WorkflowID: w.ID, Name: "VoteStep", StepType: models.StepTypeVote, StepOrder: 0, Prompt: "Vote on approach", DependsOn: "[]", Config: "{}"}
	repo.CreateStep(ctx, step)

	we := &models.WorkflowExecution{WorkflowID: w.ID, TaskID: task.ID, Status: models.WorkflowRunning, Context: "{}"}
	repo.CreateWorkflowExecution(ctx, we)

	se := &models.StepExecution{WorkflowExecutionID: we.ID, StepID: step.ID, Status: models.StepRunning, Input: "Vote prompt"}
	repo.CreateStepExecution(ctx, se)

	// Create vote records
	vote1 := &models.VoteRecord{
		StepExecutionID: se.ID,
		AgentConfigID:   agent.ID,
		Choice:          "Option A",
		Reasoning:       "Better performance",
		Confidence:      0.8,
	}
	if err := repo.CreateVoteRecord(ctx, vote1); err != nil {
		t.Fatalf("CreateVoteRecord error: %v", err)
	}
	if vote1.ID == "" {
		t.Error("expected vote record ID to be set")
	}

	// List votes
	votes, err := repo.ListVoteRecords(ctx, se.ID)
	if err != nil {
		t.Fatalf("ListVoteRecords error: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("expected 1 vote, got %d", len(votes))
	}
	if votes[0].Choice != "Option A" {
		t.Errorf("expected choice='Option A', got %q", votes[0].Choice)
	}
	if votes[0].Confidence != 0.8 {
		t.Errorf("expected confidence=0.8, got %f", votes[0].Confidence)
	}
}

func TestWorkflowRepo_AgentPerformanceMetrics(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	llmConfigRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	agent, err := llmConfigRepo.GetDefault(ctx)
	if err != nil {
		t.Fatalf("GetDefault error: %v", err)
	}

	// Upsert metrics
	if err := repo.UpsertMetric(ctx, agent.ID, "frontend", true, 5000, 10, 0.9); err != nil {
		t.Fatalf("UpsertMetric error: %v", err)
	}
	if err := repo.UpsertMetric(ctx, agent.ID, "frontend", true, 6000, 12, 0.85); err != nil {
		t.Fatalf("UpsertMetric (2nd) error: %v", err)
	}
	if err := repo.UpsertMetric(ctx, agent.ID, "frontend", false, 8000, 15, 0.3); err != nil {
		t.Fatalf("UpsertMetric (3rd) error: %v", err)
	}

	// Different task type
	if err := repo.UpsertMetric(ctx, agent.ID, "backend", true, 3000, 5, 0.95); err != nil {
		t.Fatalf("UpsertMetric backend error: %v", err)
	}

	// Get agent metrics
	metrics, err := repo.GetAgentMetrics(ctx, agent.ID)
	if err != nil {
		t.Fatalf("GetAgentMetrics error: %v", err)
	}
	if len(metrics) != 2 {
		t.Fatalf("expected 2 metric types, got %d", len(metrics))
	}

	// Find frontend metric
	var frontendMetric *models.AgentPerformanceMetric
	for i := range metrics {
		if metrics[i].TaskType == "frontend" {
			frontendMetric = &metrics[i]
			break
		}
	}
	if frontendMetric == nil {
		t.Fatal("frontend metric not found")
	}
	if frontendMetric.SuccessCount != 2 {
		t.Errorf("expected success_count=2, got %d", frontendMetric.SuccessCount)
	}
	if frontendMetric.FailureCount != 1 {
		t.Errorf("expected failure_count=1, got %d", frontendMetric.FailureCount)
	}

	// Get all metrics
	allMetrics, err := repo.GetAllMetrics(ctx)
	if err != nil {
		t.Fatalf("GetAllMetrics error: %v", err)
	}
	if len(allMetrics) != 2 {
		t.Errorf("expected 2 total metrics, got %d", len(allMetrics))
	}

	// Best agent (need 3+ data points)
	bestAgent, err := repo.GetBestAgentForTaskType(ctx, "frontend")
	if err != nil {
		t.Fatalf("GetBestAgentForTaskType error: %v", err)
	}
	if bestAgent.AgentConfigID != agent.ID {
		t.Errorf("expected best agent=%s, got %s", agent.ID, bestAgent.AgentConfigID)
	}

	// Cheapest agent
	cheapest, err := repo.GetCheapestAgentForTaskType(ctx, "frontend", 0.5)
	if err != nil {
		t.Fatalf("GetCheapestAgentForTaskType error: %v", err)
	}
	if cheapest.AgentConfigID != agent.ID {
		t.Errorf("expected cheapest agent=%s, got %s", agent.ID, cheapest.AgentConfigID)
	}
}

func TestWorkflowRepo_CascadeDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWorkflowRepo(db)
	ctx := context.Background()

	// Create workflow with steps
	w := &models.Workflow{ProjectID: "default", Name: "Cascade Test", Strategy: models.StrategySequential, Config: "{}"}
	if err := repo.CreateWorkflow(ctx, w); err != nil {
		t.Fatalf("CreateWorkflow error: %v", err)
	}

	step := &models.WorkflowStep{WorkflowID: w.ID, Name: "Step1", StepType: models.StepTypeExecute, StepOrder: 0, Prompt: "Test", DependsOn: "[]", Config: "{}"}
	if err := repo.CreateStep(ctx, step); err != nil {
		t.Fatalf("CreateStep error: %v", err)
	}

	// Verify step exists
	steps, _ := repo.ListSteps(ctx, w.ID)
	if len(steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(steps))
	}

	// Delete workflow - steps should cascade
	if err := repo.DeleteWorkflow(ctx, w.ID); err != nil {
		t.Fatalf("DeleteWorkflow error: %v", err)
	}

	steps, _ = repo.ListSteps(ctx, w.ID)
	if len(steps) != 0 {
		t.Errorf("expected 0 steps after cascade delete, got %d", len(steps))
	}
}
