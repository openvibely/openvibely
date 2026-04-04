package service

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/util"
)

type WorkflowService struct {
	workflowRepo  *repository.WorkflowRepo
	llmConfigRepo *repository.LLMConfigRepo
	taskRepo      *repository.TaskRepo
	llmSvc        *LLMService
	alertSvc      *AlertService

	mu             sync.Mutex
	runningEngines map[string]context.CancelFunc // workflowExecID -> cancel
}

func NewWorkflowService(
	workflowRepo *repository.WorkflowRepo,
	llmConfigRepo *repository.LLMConfigRepo,
	taskRepo *repository.TaskRepo,
	llmSvc *LLMService,
) *WorkflowService {
	return &WorkflowService{
		workflowRepo:   workflowRepo,
		llmConfigRepo:  llmConfigRepo,
		taskRepo:       taskRepo,
		llmSvc:         llmSvc,
		runningEngines: make(map[string]context.CancelFunc),
	}
}

func (s *WorkflowService) SetAlertService(alertSvc *AlertService) {
	s.alertSvc = alertSvc
}

// --- Template Operations ---

func (s *WorkflowService) ListTemplates(ctx context.Context) ([]models.WorkflowTemplate, error) {
	return s.workflowRepo.ListTemplates(ctx)
}

func (s *WorkflowService) GetTemplate(ctx context.Context, id string) (*models.WorkflowTemplate, error) {
	return s.workflowRepo.GetTemplate(ctx, id)
}

// --- Workflow CRUD ---

func (s *WorkflowService) CreateWorkflow(ctx context.Context, w *models.Workflow) error {
	return s.workflowRepo.CreateWorkflow(ctx, w)
}

func (s *WorkflowService) GetWorkflow(ctx context.Context, id string) (*models.Workflow, error) {
	return s.workflowRepo.GetWorkflow(ctx, id)
}

func (s *WorkflowService) ListWorkflows(ctx context.Context, projectID string) ([]models.Workflow, error) {
	return s.workflowRepo.ListWorkflows(ctx, projectID)
}

func (s *WorkflowService) UpdateWorkflow(ctx context.Context, w *models.Workflow) error {
	return s.workflowRepo.UpdateWorkflow(ctx, w)
}

func (s *WorkflowService) DeleteWorkflow(ctx context.Context, id string) error {
	return s.workflowRepo.DeleteWorkflow(ctx, id)
}

// --- Step Operations ---

func (s *WorkflowService) CreateStep(ctx context.Context, step *models.WorkflowStep) error {
	return s.workflowRepo.CreateStep(ctx, step)
}

func (s *WorkflowService) ListSteps(ctx context.Context, workflowID string) ([]models.WorkflowStep, error) {
	return s.workflowRepo.ListSteps(ctx, workflowID)
}

func (s *WorkflowService) DeleteStep(ctx context.Context, id string) error {
	return s.workflowRepo.DeleteStep(ctx, id)
}

// --- Workflow Execution Query ---

func (s *WorkflowService) GetWorkflowExecution(ctx context.Context, id string) (*models.WorkflowExecution, error) {
	return s.workflowRepo.GetWorkflowExecution(ctx, id)
}

func (s *WorkflowService) ListWorkflowExecutions(ctx context.Context, workflowID string) ([]models.WorkflowExecution, error) {
	return s.workflowRepo.ListWorkflowExecutions(ctx, workflowID)
}

func (s *WorkflowService) ListStepExecutions(ctx context.Context, workflowExecID string) ([]models.StepExecution, error) {
	return s.workflowRepo.ListStepExecutions(ctx, workflowExecID)
}

func (s *WorkflowService) ListVoteRecords(ctx context.Context, stepExecID string) ([]models.VoteRecord, error) {
	return s.workflowRepo.ListVoteRecords(ctx, stepExecID)
}

// --- Performance Metrics ---

func (s *WorkflowService) GetAgentMetrics(ctx context.Context, agentConfigID string) ([]models.AgentPerformanceMetric, error) {
	return s.workflowRepo.GetAgentMetrics(ctx, agentConfigID)
}

func (s *WorkflowService) GetAllMetrics(ctx context.Context) ([]models.AgentPerformanceMetric, error) {
	return s.workflowRepo.GetAllMetrics(ctx)
}

func (s *WorkflowService) RecordMetric(ctx context.Context, agentConfigID, taskType string, success bool, durationMs int64, costCents int, qualityScore float64) error {
	return s.workflowRepo.UpsertMetric(ctx, agentConfigID, taskType, success, durationMs, costCents, qualityScore)
}

// --- Create Workflow from Template ---

func (s *WorkflowService) CreateWorkflowFromTemplate(ctx context.Context, projectID, templateID, name string) (*models.Workflow, error) {
	tmpl, err := s.workflowRepo.GetTemplate(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("get template: %w", err)
	}

	def, err := tmpl.ParseTemplateDefinition()
	if err != nil {
		return nil, fmt.Errorf("parse template definition: %w", err)
	}

	if name == "" {
		name = tmpl.Name
	}

	// Create workflow
	w := &models.Workflow{
		ProjectID:   projectID,
		Name:        name,
		Description: tmpl.Description,
		Strategy:    def.Strategy,
		TemplateID:  &templateID,
	}
	if err := w.SetWorkflowConfig(&def.Config); err != nil {
		return nil, fmt.Errorf("set workflow config: %w", err)
	}
	if err := s.workflowRepo.CreateWorkflow(ctx, w); err != nil {
		return nil, fmt.Errorf("create workflow: %w", err)
	}

	// Create steps from template
	stepIDMap := make(map[int]string) // template step index -> real step ID
	for i, ts := range def.Steps {
		step := &models.WorkflowStep{
			WorkflowID: w.ID,
			Name:       ts.Name,
			StepType:   ts.StepType,
			StepOrder:  ts.StepOrder,
			Prompt:     ts.Prompt,
		}

		// Resolve agent based on role if adaptive routing enabled
		agentID, err := s.resolveAgentForRole(ctx, projectID, models.AgentRole(ts.AgentRole))
		if err != nil {
			log.Printf("[workflow-svc] could not resolve agent for role %q: %v", ts.AgentRole, err)
		} else if agentID != "" {
			step.AgentID = &agentID
		}

		// Set dependencies (convert template step indexes to real step IDs)
		var deps []string
		for _, depIdx := range ts.DependsOn {
			if depID, ok := stepIDMap[depIdx]; ok {
				deps = append(deps, depID)
			}
		}
		if err := step.SetDependsOn(deps); err != nil {
			return nil, fmt.Errorf("set depends_on for step %d: %w", i, err)
		}

		if err := step.SetStepConfig(&ts.Config); err != nil {
			return nil, fmt.Errorf("set step config for step %d: %w", i, err)
		}

		if err := s.workflowRepo.CreateStep(ctx, step); err != nil {
			return nil, fmt.Errorf("create step %d: %w", i, err)
		}
		stepIDMap[i] = step.ID
	}

	return w, nil
}

// resolveAgentForRole tries to find the best agent for a role based on performance data
func (s *WorkflowService) resolveAgentForRole(ctx context.Context, projectID string, role models.AgentRole) (string, error) {
	// Map roles to task types for performance lookup
	taskType := ""
	switch role {
	case models.RolePlanner:
		taskType = models.TaskTypeArchitecture
	case models.RoleImplementer:
		taskType = models.TaskTypeGeneral
	case models.RoleReviewer:
		taskType = models.TaskTypeGeneral
	case models.RoleTester:
		taskType = models.TaskTypeTesting
	default:
		taskType = models.TaskTypeGeneral
	}

	// Try to find the best agent based on performance metrics
	metric, err := s.workflowRepo.GetBestAgentForTaskType(ctx, taskType)
	if err == nil && metric != nil {
		return metric.AgentConfigID, nil
	}

	// Fall back to default agent
	return "", nil
}

// --- Workflow Execution Engine ---

// ExecuteWorkflow starts executing a workflow for a given task
func (s *WorkflowService) ExecuteWorkflow(ctx context.Context, workflowID, taskID string) (*models.WorkflowExecution, error) {
	workflow, err := s.workflowRepo.GetWorkflow(ctx, workflowID)
	if err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}

	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}

	// Build initial handoff context
	hctx := &models.HandoffContext{
		StepOutputs: make(map[string]string),
		TaskPrompt:  task.Prompt,
	}
	ctxJSON, err := hctx.ToJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal context: %w", err)
	}

	// Create workflow execution record
	we := &models.WorkflowExecution{
		WorkflowID: workflowID,
		TaskID:     taskID,
		Status:     models.WorkflowPending,
		Context:    ctxJSON,
	}
	if err := s.workflowRepo.CreateWorkflowExecution(ctx, we); err != nil {
		return nil, fmt.Errorf("create workflow execution: %w", err)
	}

	// Claim it atomically
	claimed, err := s.workflowRepo.ClaimWorkflowExecution(ctx, we.ID)
	if err != nil || !claimed {
		return nil, fmt.Errorf("claim workflow execution: %w", err)
	}

	// Start engine in background
	engineCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.runningEngines[we.ID] = cancel
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.runningEngines, we.ID)
			s.mu.Unlock()
			cancel()
		}()
		s.runWorkflowEngine(engineCtx, we.ID, workflow, task)
	}()

	return we, nil
}

// CancelWorkflowExecution cancels a running workflow execution
func (s *WorkflowService) CancelWorkflowExecution(ctx context.Context, id string) error {
	s.mu.Lock()
	cancel, ok := s.runningEngines[id]
	s.mu.Unlock()

	if ok {
		cancel()
	}

	return s.workflowRepo.CompleteWorkflowExecution(ctx, id, models.WorkflowCancelled, "cancelled by user")
}

// runWorkflowEngine is the main orchestration loop
func (s *WorkflowService) runWorkflowEngine(ctx context.Context, execID string, workflow *models.Workflow, task *models.Task) {
	log.Printf("[workflow-engine] starting workflow %q (exec=%s) for task %q", workflow.Name, execID, task.Title)

	steps, err := s.workflowRepo.ListSteps(ctx, workflow.ID)
	if err != nil {
		s.failWorkflowExecution(ctx, execID, fmt.Sprintf("list steps: %v", err), workflow, task)
		return
	}

	if len(steps) == 0 {
		s.failWorkflowExecution(ctx, execID, "workflow has no steps", workflow, task)
		return
	}

	wfConfig, err := workflow.ParseWorkflowConfig()
	if err != nil {
		s.failWorkflowExecution(ctx, execID, fmt.Sprintf("parse config: %v", err), workflow, task)
		return
	}

	// Build step map for quick lookup
	stepMap := make(map[string]*models.WorkflowStep)
	for i := range steps {
		stepMap[steps[i].ID] = &steps[i]
	}

	// Group steps by order (same order = parallel)
	stepGroups := groupStepsByOrder(steps)

	// Execute step groups in order
	for _, group := range stepGroups {
		select {
		case <-ctx.Done():
			s.failWorkflowExecution(ctx, execID, "cancelled", workflow, task)
			return
		default:
		}

		// Update current step
		we, err := s.workflowRepo.GetWorkflowExecution(ctx, execID)
		if err != nil {
			log.Printf("[workflow-engine] error getting execution: %v", err)
			continue
		}
		if we.Status == models.WorkflowCancelled || we.Status == models.WorkflowFailed {
			return
		}

		if len(group) == 1 {
			// Sequential step
			err = s.executeStep(ctx, execID, group[0], wfConfig, task)
		} else {
			// Parallel steps
			err = s.executeParallelSteps(ctx, execID, group, wfConfig, task)
		}

		if err != nil {
			if wfConfig.AutoRollback {
				s.rollbackWorkflow(ctx, execID, group)
			}
			s.failWorkflowExecution(ctx, execID, fmt.Sprintf("step execution failed: %v", err), workflow, task)
			return
		}
	}

	// All steps completed successfully
	s.completeWorkflowExecution(ctx, execID, workflow, task)
}

// executeStep runs a single workflow step
func (s *WorkflowService) executeStep(ctx context.Context, execID string, step *models.WorkflowStep, wfConfig *models.WorkflowConfig, task *models.Task) error {
	log.Printf("[workflow-engine] executing step %q (type=%s)", step.Name, step.StepType)

	// Update current step
	we, err := s.workflowRepo.GetWorkflowExecution(ctx, execID)
	if err != nil {
		return fmt.Errorf("get execution: %w", err)
	}
	we.CurrentStepID = &step.ID
	if err := s.workflowRepo.UpdateWorkflowExecution(ctx, we); err != nil {
		return fmt.Errorf("update execution current step: %w", err)
	}

	stepConfig, err := step.ParseStepConfig()
	if err != nil {
		return fmt.Errorf("parse step config: %w", err)
	}

	switch step.StepType {
	case models.StepTypeExecute:
		return s.executeAgentStep(ctx, execID, step, wfConfig, task)
	case models.StepTypeReview:
		return s.executeAgentStep(ctx, execID, step, wfConfig, task)
	case models.StepTypeGate:
		return s.executeGateStep(ctx, execID, step, stepConfig, wfConfig, task)
	case models.StepTypeVote:
		return s.executeVoteStep(ctx, execID, step, stepConfig, task)
	case models.StepTypeMerge:
		return s.executeMergeStep(ctx, execID, step, stepConfig, task)
	case models.StepTypeHandoff:
		return s.executeHandoffStep(ctx, execID, step, task)
	default:
		return fmt.Errorf("unknown step type: %s", step.StepType)
	}
}

// executeParallelSteps runs multiple steps concurrently
func (s *WorkflowService) executeParallelSteps(ctx context.Context, execID string, steps []*models.WorkflowStep, wfConfig *models.WorkflowConfig, task *models.Task) error {
	log.Printf("[workflow-engine] executing %d parallel steps", len(steps))

	var wg sync.WaitGroup
	errCh := make(chan error, len(steps))

	for _, step := range steps {
		wg.Add(1)
		go func(st *models.WorkflowStep) {
			defer wg.Done()
			if err := s.executeStep(ctx, execID, st, wfConfig, task); err != nil {
				errCh <- fmt.Errorf("step %q failed: %w", st.Name, err)
			}
		}(step)
	}

	wg.Wait()
	close(errCh)

	// Collect errors
	var errs []string
	for err := range errCh {
		errs = append(errs, err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("parallel steps failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// executeAgentStep runs a step using an LLM agent
func (s *WorkflowService) executeAgentStep(ctx context.Context, execID string, step *models.WorkflowStep, wfConfig *models.WorkflowConfig, task *models.Task) error {
	// Resolve agent
	agent, err := s.resolveStepAgent(ctx, step, wfConfig)
	if err != nil {
		return fmt.Errorf("resolve agent: %w", err)
	}

	// Build prompt with variable substitution
	prompt, err := s.buildStepPrompt(ctx, execID, step, task)
	if err != nil {
		return fmt.Errorf("build prompt: %w", err)
	}

	// Create step execution record
	se := &models.StepExecution{
		WorkflowExecutionID: execID,
		StepID:              step.ID,
		AgentConfigID:       &agent.ID,
		Status:              models.StepRunning,
		Input:               prompt,
	}
	if err := s.workflowRepo.CreateStepExecution(ctx, se); err != nil {
		return fmt.Errorf("create step execution: %w", err)
	}

	// Execute via LLM
	startTime := time.Now()
	output, err := s.callAgentForWorkflow(ctx, prompt, agent, task)
	durationMs := time.Since(startTime).Milliseconds()

	now := time.Now().UTC()
	se.CompletedAt = &now
	se.DurationMs = durationMs

	if err != nil {
		se.Status = models.StepFailed
		se.ErrorMessage = err.Error()
		if updateErr := s.workflowRepo.UpdateStepExecution(ctx, se); updateErr != nil {
			log.Printf("[workflow-engine] error updating failed step execution: %v", updateErr)
		}
		return fmt.Errorf("agent execution: %w", err)
	}

	se.Status = models.StepCompleted
	se.Output = output
	if updateErr := s.workflowRepo.UpdateStepExecution(ctx, se); updateErr != nil {
		log.Printf("[workflow-engine] error updating completed step execution: %v", updateErr)
	}

	// Update handoff context with step output
	s.updateHandoffContext(ctx, execID, step.ID, output)

	// Update cost tracking
	s.updateWorkflowCost(ctx, execID, se.CostCents)

	// Record performance metric
	if agent.ID != "" {
		taskType := s.inferTaskType(step, task)
		if metricErr := s.workflowRepo.UpsertMetric(ctx, agent.ID, taskType, true, durationMs, se.CostCents, 0.8); metricErr != nil {
			log.Printf("[workflow-engine] error recording metric: %v", metricErr)
		}
	}

	return nil
}

// executeGateStep runs a quality gate step with iterative refinement
func (s *WorkflowService) executeGateStep(ctx context.Context, execID string, step *models.WorkflowStep, stepConfig *models.StepConfig, wfConfig *models.WorkflowConfig, task *models.Task) error {
	maxIterations := stepConfig.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 1
	}

	threshold := stepConfig.PassThreshold
	if threshold <= 0 {
		threshold = wfConfig.QualityThreshold
	}

	for iteration := 0; iteration < maxIterations; iteration++ {
		log.Printf("[workflow-engine] gate step %q iteration %d/%d", step.Name, iteration+1, maxIterations)

		// Run the review agent
		agent, err := s.resolveStepAgent(ctx, step, wfConfig)
		if err != nil {
			return fmt.Errorf("resolve gate agent: %w", err)
		}

		prompt, err := s.buildStepPrompt(ctx, execID, step, task)
		if err != nil {
			return fmt.Errorf("build gate prompt: %w", err)
		}

		se := &models.StepExecution{
			WorkflowExecutionID: execID,
			StepID:              step.ID,
			AgentConfigID:       &agent.ID,
			Status:              models.StepRunning,
			Iteration:           iteration,
			Input:               prompt,
		}
		if err := s.workflowRepo.CreateStepExecution(ctx, se); err != nil {
			return fmt.Errorf("create gate step execution: %w", err)
		}

		startTime := time.Now()
		output, err := s.callAgentForWorkflow(ctx, prompt, agent, task)
		durationMs := time.Since(startTime).Milliseconds()

		now := time.Now().UTC()
		se.CompletedAt = &now
		se.DurationMs = durationMs

		if err != nil {
			se.Status = models.StepFailed
			se.ErrorMessage = err.Error()
			s.workflowRepo.UpdateStepExecution(ctx, se)
			return fmt.Errorf("gate execution: %w", err)
		}

		// Extract quality score from output
		score := extractQualityScore(output)
		se.Score = &score
		se.Output = output
		se.Status = models.StepCompleted
		s.workflowRepo.UpdateStepExecution(ctx, se)

		// Update context
		s.updateHandoffContext(ctx, execID, step.ID, output)

		if score >= threshold {
			log.Printf("[workflow-engine] gate step %q passed: score=%.2f threshold=%.2f", step.Name, score, threshold)
			return nil
		}

		log.Printf("[workflow-engine] gate step %q failed: score=%.2f threshold=%.2f, iteration %d/%d",
			step.Name, score, threshold, iteration+1, maxIterations)

		if iteration == maxIterations-1 {
			// Last iteration - handle failure
			switch stepConfig.FailAction {
			case "rollback":
				return fmt.Errorf("gate failed after %d iterations (score=%.2f, threshold=%.2f), rolling back", maxIterations, score, threshold)
			case "skip":
				log.Printf("[workflow-engine] gate step %q: skipping due to fail_action=skip", step.Name)
				return nil
			case "pause":
				we, _ := s.workflowRepo.GetWorkflowExecution(ctx, execID)
				if we != nil {
					we.Status = models.WorkflowPaused
					s.workflowRepo.UpdateWorkflowExecution(ctx, we)
				}
				return fmt.Errorf("gate step paused: quality threshold not met (score=%.2f, threshold=%.2f)", score, threshold)
			default: // "retry" - continue to next iteration (handled by loop)
				return fmt.Errorf("gate failed after %d iterations (score=%.2f, threshold=%.2f)", maxIterations, score, threshold)
			}
		}
	}
	return nil
}

// executeVoteStep runs a voting step with multiple agents
func (s *WorkflowService) executeVoteStep(ctx context.Context, execID string, step *models.WorkflowStep, stepConfig *models.StepConfig, task *models.Task) error {
	log.Printf("[workflow-engine] executing vote step %q with strategy=%s", step.Name, stepConfig.VoteStrategy)

	// Get voter agents
	voterIDs := stepConfig.VoterAgentIDs
	if len(voterIDs) == 0 {
		// Use all available agents
		configs, err := s.llmConfigRepo.List(ctx)
		if err != nil {
			return fmt.Errorf("list agents for voting: %w", err)
		}
		for _, c := range configs {
			voterIDs = append(voterIDs, c.ID)
		}
	}

	if len(voterIDs) < 2 {
		return fmt.Errorf("voting requires at least 2 agents, have %d", len(voterIDs))
	}

	// Create step execution for the vote
	se := &models.StepExecution{
		WorkflowExecutionID: execID,
		StepID:              step.ID,
		Status:              models.StepRunning,
	}
	if err := s.workflowRepo.CreateStepExecution(ctx, se); err != nil {
		return fmt.Errorf("create vote step execution: %w", err)
	}

	// Build the vote prompt
	prompt, err := s.buildStepPrompt(ctx, execID, step, task)
	if err != nil {
		return fmt.Errorf("build vote prompt: %w", err)
	}

	votePrompt := prompt + "\n\nProvide your response in the format:\nCHOICE: <your choice>\nCONFIDENCE: <0.0-1.0>\nREASONING: <your reasoning>"

	// Collect votes from all agents in parallel
	type voteResult struct {
		agentID    string
		choice     string
		confidence float64
		reasoning  string
		err        error
	}

	var wg sync.WaitGroup
	results := make(chan voteResult, len(voterIDs))

	for _, agentID := range voterIDs {
		wg.Add(1)
		go func(aID string) {
			defer wg.Done()
			agent, err := s.llmConfigRepo.GetByID(ctx, aID)
			if err != nil {
				results <- voteResult{agentID: aID, err: err}
				return
			}

			output, err := s.callAgentForWorkflow(ctx, votePrompt, agent, task)
			if err != nil {
				results <- voteResult{agentID: aID, err: err}
				return
			}

			choice, confidence, reasoning := parseVoteOutput(output)
			results <- voteResult{
				agentID:    aID,
				choice:     choice,
				confidence: confidence,
				reasoning:  reasoning,
			}
		}(agentID)
	}

	wg.Wait()
	close(results)

	// Record votes and tally
	votes := make(map[string]int)
	weightedVotes := make(map[string]float64)
	var voteDetails []string

	for result := range results {
		if result.err != nil {
			log.Printf("[workflow-engine] vote from agent %s failed: %v", result.agentID, result.err)
			continue
		}

		vr := &models.VoteRecord{
			StepExecutionID: se.ID,
			AgentConfigID:   result.agentID,
			Choice:          result.choice,
			Reasoning:       result.reasoning,
			Confidence:      result.confidence,
		}
		if err := s.workflowRepo.CreateVoteRecord(ctx, vr); err != nil {
			log.Printf("[workflow-engine] error recording vote: %v", err)
		}

		votes[result.choice]++
		weightedVotes[result.choice] += result.confidence
		voteDetails = append(voteDetails, fmt.Sprintf("Agent %s: %s (confidence: %.2f) - %s", result.agentID, result.choice, result.confidence, result.reasoning))
	}

	// Determine winner based on strategy
	winner := ""
	switch stepConfig.VoteStrategy {
	case "weighted":
		maxWeight := 0.0
		for choice, weight := range weightedVotes {
			if weight > maxWeight {
				maxWeight = weight
				winner = choice
			}
		}
	default: // "majority"
		maxVotes := 0
		for choice, count := range votes {
			if count > maxVotes {
				maxVotes = count
				winner = choice
			}
		}
	}

	// Update step execution with result
	now := time.Now().UTC()
	se.CompletedAt = &now
	se.Status = models.StepCompleted
	se.Output = fmt.Sprintf("Vote Result: %s\n\nDetails:\n%s", winner, strings.Join(voteDetails, "\n"))
	s.workflowRepo.UpdateStepExecution(ctx, se)

	// Update handoff context
	s.updateHandoffContext(ctx, execID, step.ID, se.Output)

	return nil
}

// executeMergeStep merges outputs from parallel steps
func (s *WorkflowService) executeMergeStep(ctx context.Context, execID string, step *models.WorkflowStep, stepConfig *models.StepConfig, task *models.Task) error {
	log.Printf("[workflow-engine] executing merge step %q with strategy=%s", step.Name, stepConfig.MergeStrategy)

	// Get outputs from dependency steps
	deps, err := step.ParseDependsOn()
	if err != nil {
		return fmt.Errorf("parse merge dependencies: %w", err)
	}

	var outputs []string
	for _, depID := range deps {
		latestExec, err := s.workflowRepo.GetLatestStepExecution(ctx, execID, depID)
		if err != nil {
			log.Printf("[workflow-engine] merge: could not get output for step %s: %v", depID, err)
			continue
		}
		outputs = append(outputs, fmt.Sprintf("--- Output from step %s ---\n%s", depID, latestExec.Output))
	}

	mergedOutput := ""
	switch stepConfig.MergeStrategy {
	case "concatenate":
		mergedOutput = strings.Join(outputs, "\n\n")
	case "select_best":
		// Use a coordinator agent to select the best
		if stepConfig.CoordinatorAgent != "" {
			agent, err := s.llmConfigRepo.GetByID(ctx, stepConfig.CoordinatorAgent)
			if err == nil {
				selectPrompt := fmt.Sprintf("Select the best output from the following parallel work:\n\n%s\n\nOriginal task: %s\n\nReturn only the best output with a brief justification.",
					strings.Join(outputs, "\n\n"), task.Prompt)
				mergedOutput, err = s.callAgentForWorkflow(ctx, selectPrompt, agent, task)
				if err != nil {
					log.Printf("[workflow-engine] merge coordinator failed, falling back to concatenate: %v", err)
					mergedOutput = strings.Join(outputs, "\n\n")
				}
			}
		} else {
			mergedOutput = strings.Join(outputs, "\n\n")
		}
	default: // "coordinate"
		// Use coordinator to merge and reconcile
		mergePrompt := fmt.Sprintf("Merge and reconcile the following parallel work outputs into a single coherent result. Resolve any conflicts.\n\n%s\n\nOriginal task: %s",
			strings.Join(outputs, "\n\n"), task.Prompt)

		agent, err := s.resolveStepAgent(ctx, step, nil)
		if err == nil {
			mergedOutput, err = s.callAgentForWorkflow(ctx, mergePrompt, agent, task)
			if err != nil {
				mergedOutput = strings.Join(outputs, "\n\n")
			}
		} else {
			mergedOutput = strings.Join(outputs, "\n\n")
		}
	}

	// Save merge result
	se := &models.StepExecution{
		WorkflowExecutionID: execID,
		StepID:              step.ID,
		Status:              models.StepCompleted,
		Output:              mergedOutput,
	}
	now := time.Now().UTC()
	se.CompletedAt = &now
	if err := s.workflowRepo.CreateStepExecution(ctx, se); err != nil {
		return fmt.Errorf("create merge step execution: %w", err)
	}

	s.updateHandoffContext(ctx, execID, step.ID, mergedOutput)
	return nil
}

// executeHandoffStep generates a context summary for the next agent
func (s *WorkflowService) executeHandoffStep(ctx context.Context, execID string, step *models.WorkflowStep, task *models.Task) error {
	log.Printf("[workflow-engine] executing handoff step %q", step.Name)

	// Get current handoff context
	we, err := s.workflowRepo.GetWorkflowExecution(ctx, execID)
	if err != nil {
		return fmt.Errorf("get workflow execution: %w", err)
	}

	hctx, err := models.ParseHandoffContext(we.Context)
	if err != nil {
		return fmt.Errorf("parse handoff context: %w", err)
	}

	// Build context summary from all previous step outputs
	var contextParts []string
	for stepID, output := range hctx.StepOutputs {
		contextParts = append(contextParts, fmt.Sprintf("Step %s output:\n%s", stepID, util.Truncate(output, 2000)))
	}

	// Use an agent to generate a concise summary
	agent, err := s.resolveStepAgent(ctx, step, nil)
	if err != nil {
		// Fall back to raw concatenation
		summary := strings.Join(contextParts, "\n\n---\n\n")
		hctx.Summary = summary
		ctxJSON, _ := hctx.ToJSON()
		we.Context = ctxJSON
		return s.workflowRepo.UpdateWorkflowExecution(ctx, we)
	}

	summaryPrompt := fmt.Sprintf(`Generate a concise context summary for the next agent in the workflow. Include:
1. What was done so far
2. Key decisions made
3. Issues encountered
4. What needs to be done next

Previous work:
%s

Original task: %s`, strings.Join(contextParts, "\n\n---\n\n"), task.Prompt)

	summary, err := s.callAgentForWorkflow(ctx, summaryPrompt, agent, task)
	if err != nil {
		summary = strings.Join(contextParts, "\n\n---\n\n")
	}

	hctx.Summary = summary

	se := &models.StepExecution{
		WorkflowExecutionID: execID,
		StepID:              step.ID,
		Status:              models.StepCompleted,
		Output:              summary,
	}
	now := time.Now().UTC()
	se.CompletedAt = &now
	if err := s.workflowRepo.CreateStepExecution(ctx, se); err != nil {
		log.Printf("[workflow-engine] error creating handoff step execution: %v", err)
	}

	ctxJSON, _ := hctx.ToJSON()
	we.Context = ctxJSON
	return s.workflowRepo.UpdateWorkflowExecution(ctx, we)
}

// --- Helper Functions ---

// resolveStepAgent resolves the LLM agent for a step
func (s *WorkflowService) resolveStepAgent(ctx context.Context, step *models.WorkflowStep, wfConfig *models.WorkflowConfig) (*models.LLMConfig, error) {
	// Step-specific agent
	if step.AgentID != nil && *step.AgentID != "" {
		agent, err := s.llmConfigRepo.GetByID(ctx, *step.AgentID)
		if err == nil {
			return agent, nil
		}
		log.Printf("[workflow-engine] step agent %s not found, falling back to adaptive: %v", *step.AgentID, err)
	}

	// Adaptive routing based on performance
	if wfConfig != nil && wfConfig.AdaptiveRouting {
		taskType := s.inferTaskTypeFromStep(step)
		metric, err := s.workflowRepo.GetBestAgentForTaskType(ctx, taskType)
		if err == nil {
			agent, err := s.llmConfigRepo.GetByID(ctx, metric.AgentConfigID)
			if err == nil {
				log.Printf("[workflow-engine] adaptive routing: selected agent %q for task type %q (quality=%.2f)",
					agent.Name, taskType, metric.AvgQualityScore)
				return agent, nil
			}
		}
	}

	// Fall back to default agent
	agent, err := s.llmConfigRepo.GetDefault(ctx)
	if err != nil {
		return nil, fmt.Errorf("no default agent: %w", err)
	}
	return agent, nil
}

// buildStepPrompt constructs the prompt for a step with variable substitution
func (s *WorkflowService) buildStepPrompt(ctx context.Context, execID string, step *models.WorkflowStep, task *models.Task) (string, error) {
	prompt := step.Prompt

	// Get handoff context
	we, err := s.workflowRepo.GetWorkflowExecution(ctx, execID)
	if err != nil {
		return prompt, err
	}

	hctx, err := models.ParseHandoffContext(we.Context)
	if err != nil {
		return prompt, err
	}

	// Replace variables
	prompt = strings.ReplaceAll(prompt, "{{task_prompt}}", task.Prompt)
	prompt = strings.ReplaceAll(prompt, "{{context_summary}}", hctx.Summary)

	// Replace {{prev_output}} with the output from dependency steps
	deps, _ := step.ParseDependsOn()
	if len(deps) > 0 {
		var prevOutputs []string
		for _, depID := range deps {
			if output, ok := hctx.StepOutputs[depID]; ok {
				prevOutputs = append(prevOutputs, output)
			}
		}
		prompt = strings.ReplaceAll(prompt, "{{prev_output}}", strings.Join(prevOutputs, "\n\n---\n\n"))
	} else {
		// No deps - check if there's any previous step output
		var lastOutput string
		stepExecs, _ := s.workflowRepo.ListStepExecutions(ctx, execID)
		if len(stepExecs) > 0 {
			lastOutput = stepExecs[len(stepExecs)-1].Output
		}
		prompt = strings.ReplaceAll(prompt, "{{prev_output}}", lastOutput)
	}

	return prompt, nil
}

// callAgentForWorkflow calls an LLM agent and returns the text output
func (s *WorkflowService) callAgentForWorkflow(ctx context.Context, prompt string, agent *models.LLMConfig, task *models.Task) (string, error) {
	return s.llmSvc.CallAgentForWorkflow(ctx, prompt, agent, task.ProjectID)
}

// updateHandoffContext adds step output to the workflow execution context
func (s *WorkflowService) updateHandoffContext(ctx context.Context, execID, stepID, output string) {
	we, err := s.workflowRepo.GetWorkflowExecution(ctx, execID)
	if err != nil {
		log.Printf("[workflow-engine] error getting execution for context update: %v", err)
		return
	}

	hctx, err := models.ParseHandoffContext(we.Context)
	if err != nil {
		log.Printf("[workflow-engine] error parsing handoff context: %v", err)
		return
	}

	hctx.StepOutputs[stepID] = output

	ctxJSON, err := hctx.ToJSON()
	if err != nil {
		log.Printf("[workflow-engine] error marshaling handoff context: %v", err)
		return
	}

	we.Context = ctxJSON
	if err := s.workflowRepo.UpdateWorkflowExecution(ctx, we); err != nil {
		log.Printf("[workflow-engine] error updating handoff context: %v", err)
	}
}

// updateWorkflowCost adds step cost to the workflow execution total
func (s *WorkflowService) updateWorkflowCost(ctx context.Context, execID string, costCents int) {
	we, err := s.workflowRepo.GetWorkflowExecution(ctx, execID)
	if err != nil {
		return
	}
	we.TotalCostCents += costCents
	s.workflowRepo.UpdateWorkflowExecution(ctx, we)
}

// failWorkflowExecution marks a workflow execution as failed and creates an alert
func (s *WorkflowService) failWorkflowExecution(ctx context.Context, execID, errorMsg string, workflow *models.Workflow, task *models.Task) {
	log.Printf("[workflow-engine] workflow execution %s failed: %s", execID, errorMsg)

	if err := s.workflowRepo.CompleteWorkflowExecution(ctx, execID, models.WorkflowFailed, errorMsg); err != nil {
		log.Printf("[workflow-engine] error completing failed execution: %v", err)
	}

	if s.alertSvc != nil {
		s.alertSvc.CreateTaskFailedAlert(ctx, task.ProjectID, task.ID, execID,
			fmt.Sprintf("Workflow %q failed", workflow.Name), errorMsg)
	}
}

// completeWorkflowExecution marks a workflow execution as completed
func (s *WorkflowService) completeWorkflowExecution(ctx context.Context, execID string, workflow *models.Workflow, task *models.Task) {
	log.Printf("[workflow-engine] workflow execution %s completed successfully", execID)

	if err := s.workflowRepo.CompleteWorkflowExecution(ctx, execID, models.WorkflowCompleted, ""); err != nil {
		log.Printf("[workflow-engine] error completing execution: %v", err)
	}
}

// rollbackWorkflow marks failed steps as rolled back
func (s *WorkflowService) rollbackWorkflow(ctx context.Context, execID string, failedSteps []*models.WorkflowStep) {
	log.Printf("[workflow-engine] rolling back %d steps", len(failedSteps))

	for _, step := range failedSteps {
		latestExec, err := s.workflowRepo.GetLatestStepExecution(ctx, execID, step.ID)
		if err != nil {
			continue
		}
		latestExec.Status = models.StepRolledBack
		s.workflowRepo.UpdateStepExecution(ctx, latestExec)
	}
}

// inferTaskType determines the task type based on step and task context
func (s *WorkflowService) inferTaskType(step *models.WorkflowStep, task *models.Task) string {
	return s.inferTaskTypeFromStep(step)
}

// inferTaskTypeFromStep determines task type based on step characteristics
func (s *WorkflowService) inferTaskTypeFromStep(step *models.WorkflowStep) string {
	nameLower := strings.ToLower(step.Name)
	promptLower := strings.ToLower(step.Prompt)

	switch {
	case strings.Contains(nameLower, "test") || strings.Contains(promptLower, "test"):
		return models.TaskTypeTesting
	case strings.Contains(nameLower, "review") || strings.Contains(nameLower, "gate"):
		return models.TaskTypeGeneral
	case strings.Contains(nameLower, "refactor"):
		return models.TaskTypeRefactor
	case strings.Contains(nameLower, "bug") || strings.Contains(nameLower, "fix"):
		return models.TaskTypeBugfix
	case strings.Contains(nameLower, "plan") || strings.Contains(nameLower, "architecture") || strings.Contains(nameLower, "research"):
		return models.TaskTypeArchitecture
	case strings.Contains(promptLower, "frontend") || strings.Contains(promptLower, "template") || strings.Contains(promptLower, "html"):
		return models.TaskTypeFrontend
	case strings.Contains(promptLower, "backend") || strings.Contains(promptLower, "api") || strings.Contains(promptLower, "database"):
		return models.TaskTypeBackend
	default:
		return models.TaskTypeGeneral
	}
}

// groupStepsByOrder groups steps that have the same step_order (for parallel execution)
func groupStepsByOrder(steps []models.WorkflowStep) [][]*models.WorkflowStep {
	orderMap := make(map[int][]*models.WorkflowStep)
	for i := range steps {
		orderMap[steps[i].StepOrder] = append(orderMap[steps[i].StepOrder], &steps[i])
	}

	// Sort by order
	var orders []int
	for order := range orderMap {
		orders = append(orders, order)
	}
	sort.Ints(orders)

	var groups [][]*models.WorkflowStep
	for _, order := range orders {
		groups = append(groups, orderMap[order])
	}
	return groups
}

// extractQualityScore parses a quality score from agent output
func extractQualityScore(output string) float64 {
	re := regexp.MustCompile(`QUALITY_SCORE:\s*([\d.]+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 2 {
		score, err := strconv.ParseFloat(matches[1], 64)
		if err == nil && score >= 0 && score <= 1 {
			return score
		}
	}
	// Fall back: look for any score-like pattern
	re2 := regexp.MustCompile(`(?i)score[:\s]+(\d+\.?\d*)\s*(?:/\s*1(?:\.0)?|out of 1)`)
	matches = re2.FindStringSubmatch(output)
	if len(matches) >= 2 {
		score, err := strconv.ParseFloat(matches[1], 64)
		if err == nil && score >= 0 && score <= 1 {
			return score
		}
	}
	return 0.5 // default to middle score if unable to parse
}

// parseVoteOutput extracts vote details from agent output
func parseVoteOutput(output string) (choice string, confidence float64, reasoning string) {
	choice = "unknown"
	confidence = 0.5
	reasoning = output

	choiceRe := regexp.MustCompile(`(?i)CHOICE:\s*(.+?)(?:\n|$)`)
	if matches := choiceRe.FindStringSubmatch(output); len(matches) >= 2 {
		choice = strings.TrimSpace(matches[1])
	}

	confRe := regexp.MustCompile(`(?i)CONFIDENCE:\s*([\d.]+)`)
	if matches := confRe.FindStringSubmatch(output); len(matches) >= 2 {
		if v, err := strconv.ParseFloat(matches[1], 64); err == nil && v >= 0 && v <= 1 {
			confidence = v
		}
	}

	reasonRe := regexp.MustCompile(`(?i)REASONING:\s*(.+)`)
	if matches := reasonRe.FindStringSubmatch(output); len(matches) >= 2 {
		reasoning = strings.TrimSpace(matches[1])
	}

	return
}

// AnalyzeTaskComplexity analyzes a task's complexity to recommend an optimal workflow strategy
func (s *WorkflowService) AnalyzeTaskComplexity(ctx context.Context, task *models.Task) (*WorkflowRecommendation, error) {
	promptLen := len(task.Prompt)
	promptLower := strings.ToLower(task.Prompt)

	rec := &WorkflowRecommendation{}

	// Analyze complexity signals
	hasMultipleFiles := strings.Contains(promptLower, "multiple files") || strings.Contains(promptLower, "across files")
	hasFrontendAndBackend := (strings.Contains(promptLower, "frontend") || strings.Contains(promptLower, "ui")) &&
		(strings.Contains(promptLower, "backend") || strings.Contains(promptLower, "api"))
	isBugFix := strings.Contains(promptLower, "bug") || strings.Contains(promptLower, "fix") || strings.Contains(promptLower, "broken")
	isRefactor := strings.Contains(promptLower, "refactor") || strings.Contains(promptLower, "restructure") || strings.Contains(promptLower, "rename")
	isFeature := strings.Contains(promptLower, "add") || strings.Contains(promptLower, "implement") || strings.Contains(promptLower, "create") || strings.Contains(promptLower, "new feature")
	isResearch := strings.Contains(promptLower, "research") || strings.Contains(promptLower, "investigate") || strings.Contains(promptLower, "explore")

	// Determine complexity score
	complexityScore := 0
	if promptLen > 500 {
		complexityScore++
	}
	if promptLen > 1000 {
		complexityScore++
	}
	if hasMultipleFiles {
		complexityScore += 2
	}
	if hasFrontendAndBackend {
		complexityScore += 2
	}

	// Recommend strategy
	switch {
	case complexityScore <= 1:
		rec.Strategy = models.StrategySequential
		rec.Reason = "Simple task - sequential execution is sufficient"
	case hasFrontendAndBackend:
		rec.Strategy = models.StrategyParallel
		rec.Reason = "Task involves both frontend and backend work that can run in parallel"
	case complexityScore >= 3:
		rec.Strategy = models.StrategyHybrid
		rec.Reason = "Complex task benefits from hybrid parallel/sequential execution"
	default:
		rec.Strategy = models.StrategySequential
		rec.Reason = "Moderate complexity - sequential execution with quality gates recommended"
	}

	// Recommend template
	switch {
	case isBugFix:
		rec.TemplateCategory = models.TemplateCategoryBugfix
		rec.TemplateName = "Bug Hunt"
	case isRefactor:
		rec.TemplateCategory = models.TemplateCategoryRefactor
		rec.TemplateName = "Refactor"
	case isResearch:
		rec.TemplateCategory = models.TemplateCategoryResearch
		rec.TemplateName = "Research & Implement"
	case isFeature:
		rec.TemplateCategory = models.TemplateCategoryFeature
		rec.TemplateName = "Full Feature"
	default:
		rec.TemplateCategory = models.TemplateCategoryFeature
		rec.TemplateName = "Full Feature"
	}

	// Estimate steps needed
	rec.EstimatedSteps = 3
	if complexityScore >= 3 {
		rec.EstimatedSteps = 5
	}

	return rec, nil
}

// WorkflowRecommendation is the result of task complexity analysis
type WorkflowRecommendation struct {
	Strategy         models.WorkflowStrategy        `json:"strategy"`
	Reason           string                          `json:"reason"`
	TemplateCategory models.WorkflowTemplateCategory `json:"template_category"`
	TemplateName     string                          `json:"template_name"`
	EstimatedSteps   int                             `json:"estimated_steps"`
}

// GetWorkflowExecutionsByTask returns workflow executions for a task
func (s *WorkflowService) GetWorkflowExecutionsByTask(ctx context.Context, taskID string) ([]models.WorkflowExecution, error) {
	return s.workflowRepo.ListWorkflowExecutionsByTask(ctx, taskID)
}

// GetBestAgentForTaskType returns the best performing agent for a task type
func (s *WorkflowService) GetBestAgentForTaskType(ctx context.Context, taskType string) (*models.AgentPerformanceMetric, error) {
	return s.workflowRepo.GetBestAgentForTaskType(ctx, taskType)
}

// GetCheapestAgentForTaskType returns the cheapest agent meeting quality threshold
func (s *WorkflowService) GetCheapestAgentForTaskType(ctx context.Context, taskType string, minQuality float64) (*models.AgentPerformanceMetric, error) {
	return s.workflowRepo.GetCheapestAgentForTaskType(ctx, taskType, minQuality)
}

// GetStepExecutionsByWorkflowExec gets all step executions for a workflow execution
func (s *WorkflowService) GetStepExecutionsByWorkflowExec(ctx context.Context, wfExecID string) ([]models.StepExecution, error) {
	return s.workflowRepo.ListStepExecutions(ctx, wfExecID)
}
