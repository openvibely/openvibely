package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type CreateSwarmTaskRequest struct {
	ProjectID         string
	Title             string
	Prompt            string
	Goal              string
	Category          models.TaskCategory
	Priority          int
	AgentID           *string
	AgentDefinitionID *string
	Tag               models.TaskTag
	MaxWorkers        int
	WorkerIsolation   string
	ReviewerEnabled   bool
	MergerEnabled     bool
	StartImmediately  *bool
	MergeTargetBranch string
}

type PlannerOutput struct {
	Workers                []PlannerWorker `json:"workers"`
	ReviewerPrompt         string          `json:"reviewer_prompt"`
	MergerPrompt           string          `json:"merger_prompt"`
	LegacyIntegratorPrompt string          `json:"integrator_prompt,omitempty"`
	Notes                  string          `json:"notes"`
}

func (o *PlannerOutput) NormalizeMergerFields() {
	if o == nil {
		return
	}
	if strings.TrimSpace(o.MergerPrompt) == "" {
		o.MergerPrompt = o.LegacyIntegratorPrompt
	}
	// Keep the legacy input-only field out of downstream comparisons/logging.
	o.LegacyIntegratorPrompt = ""
}

type PlannerWorker struct {
	TaskID       string   `json:"task_id,omitempty"`
	Title        string   `json:"title"`
	Prompt       string   `json:"prompt"`
	WorkerKind   string   `json:"worker_kind"`
	Ownership    []string `json:"ownership"`
	Isolation    string   `json:"isolation"`
	WriteScope   []string `json:"write_scope"`
	ReadScope    []string `json:"read_scope"`
	Required     bool     `json:"required"`
	ReadOnly     bool     `json:"read_only"`
	AllowOverlap bool     `json:"allow_overlap"`
}

var ErrSwarmRoleActive = errors.New("swarm role already has an active execution")

type SwarmService struct {
	taskSvc       *TaskService
	taskRepo      *repository.TaskRepo
	execRepo      *repository.ExecutionRepo
	workerSvc     *WorkerService
	llmConfigRepo *repository.LLMConfigRepo
	projectRepo   *repository.ProjectRepo
	orchestration sync.Mutex
}

func NewSwarmService(taskSvc *TaskService, taskRepo *repository.TaskRepo, execRepo *repository.ExecutionRepo, workerSvc *WorkerService) *SwarmService {
	return &SwarmService{taskSvc: taskSvc, taskRepo: taskRepo, execRepo: execRepo, workerSvc: workerSvc}
}

func (s *SwarmService) SetModelSelectionRepos(llmConfigRepo *repository.LLMConfigRepo, projectRepo *repository.ProjectRepo) {
	if s == nil {
		return
	}
	s.llmConfigRepo = llmConfigRepo
	s.projectRepo = projectRepo
}

func (s *SwarmService) resolveAssignedAgentID(ctx context.Context, projectID string, requested *string) *string {
	if requested != nil {
		trimmed := strings.TrimSpace(*requested)
		if trimmed != "" && trimmed != "auto" && trimmed != "default" {
			return &trimmed
		}
	}
	if s == nil || s.llmConfigRepo == nil {
		return requested
	}
	if strings.TrimSpace(projectID) != "" && s.projectRepo != nil {
		project, err := s.projectRepo.GetByID(ctx, projectID)
		if err == nil && project != nil && project.DefaultAgentConfigID != nil && strings.TrimSpace(*project.DefaultAgentConfigID) != "" {
			id := strings.TrimSpace(*project.DefaultAgentConfigID)
			if agent, agentErr := s.llmConfigRepo.GetByID(ctx, id); agentErr == nil && agent != nil {
				return &id
			}
		}
	}
	if agent, err := s.llmConfigRepo.GetDefault(ctx); err == nil && agent != nil {
		id := agent.ID
		return &id
	}
	return requested
}

func (s *SwarmService) CreateSwarmTask(ctx context.Context, req CreateSwarmTaskRequest) (*models.Task, error) {
	if s == nil || s.taskSvc == nil || s.taskRepo == nil {
		return nil, errors.New("swarm service unavailable")
	}
	if strings.TrimSpace(req.ProjectID) == "" || strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Prompt) == "" {
		return nil, errors.New("project_id, title, and prompt are required")
	}
	if req.Category == "" {
		req.Category = models.CategoryActive
	}
	if req.Priority == 0 {
		req.Priority = 2
	}
	if req.MaxWorkers <= 0 {
		req.MaxWorkers = 3
	}
	if req.MaxWorkers > 8 {
		req.MaxWorkers = 8
	}
	if req.WorkerIsolation == "" {
		req.WorkerIsolation = "worktree"
	}
	req.AgentID = s.resolveAssignedAgentID(ctx, req.ProjectID, req.AgentID)
	cfg := models.SwarmConfig{
		Mode:                             "autonomous",
		DefaultWorkerIsolation:           req.WorkerIsolation,
		MaxWorkers:                       req.MaxWorkers,
		ReviewerEnabled:                  req.ReviewerEnabled,
		MergerEnabled:                    req.MergerEnabled,
		RerunReviewerAfterWorkerFollowup: true,
		RerunMergerAfterReviewer:         true,
		Generation:                       1,
		MergeStrategy:                    "patch_apply",
	}
	cfgJSON, err := cfg.JSON()
	if err != nil {
		return nil, err
	}
	parent := &models.Task{
		ProjectID:         req.ProjectID,
		Title:             req.Title,
		Prompt:            req.Prompt,
		Category:          req.Category,
		Priority:          req.Priority,
		Status:            models.StatusBlocked,
		AgentID:           req.AgentID,
		AgentDefinitionID: req.AgentDefinitionID,
		Tag:               req.Tag,
		MergeTargetBranch: req.MergeTargetBranch,
		SwarmRole:         models.SwarmRoleParent,
		SwarmStatus:       "planning",
		SwarmConfig:       cfgJSON,
	}
	if err := s.taskSvc.CreateWithGoal(ctx, parent, req.Goal); err != nil {
		return nil, err
	}
	if err := s.ensureIntegrationWorktreeMetadata(ctx, parent); err != nil {
		return nil, err
	}
	if parent.Category == models.CategoryActive {
		if err := s.StartPlanner(ctx, parent.ID); err != nil {
			return nil, err
		}
	}
	return parent, nil
}

func (s *SwarmService) StartPlanner(ctx context.Context, parentTaskID string) error {
	return s.startPlanner(ctx, parentTaskID, false)
}

// StartPlannerForScheduledRun starts or resumes the planner for one scheduled
// occurrence. startsNewContext is transient dispatch metadata for the actual
// planner execution; it is not persisted on the parent or planner task.
func (s *SwarmService) StartPlannerForScheduledRun(ctx context.Context, parentTaskID string, startsNewContext bool) error {
	return s.startPlanner(ctx, parentTaskID, startsNewContext)
}

func (s *SwarmService) startPlanner(ctx context.Context, parentTaskID string, startsNewContext bool) error {
	s.orchestration.Lock()
	defer s.orchestration.Unlock()
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil || parent == nil {
		return fmt.Errorf("loading swarm parent: %w", err)
	}
	if parent.Category != models.CategoryActive {
		if err := s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
			return err
		}
		parent.Category = models.CategoryActive
	}
	if parent.Status != models.StatusBlocked {
		if err := s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusBlocked); err != nil {
			return err
		}
		parent.Status = models.StatusBlocked
	}
	if existing, err := s.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner); err != nil {
		return err
	} else if existing != nil {
		existing.StartsNewContext = startsNewContext
		return s.submitIfRunnable(ctx, existing)
	}
	prompt := plannerPrompt(parent.Prompt, maxWorkers(parent))
	child := &models.Task{
		ProjectID:         parent.ProjectID,
		Title:             parent.Title + " · Planner",
		Prompt:            prompt,
		Category:          models.CategoryActive,
		Priority:          parent.Priority,
		Status:            models.StatusPending,
		AgentID:           parent.AgentID,
		AgentDefinitionID: parent.AgentDefinitionID,
		ParentTaskID:      &parent.ID,
		SwarmRole:         models.SwarmRolePlanner,
		SwarmStatus:       "planning",
		SwarmConfig:       `{"isolation":"read_only","rerun_generation":1,"required":true}`,
		SwarmSequence:     0,
		StartsNewContext:  startsNewContext,
	}
	if err := s.createSwarmChild(ctx, parent, child); err != nil {
		return err
	}
	return nil
}

func swarmChildTitle(baseTitle, parentID string, sequence, attempt int) string {
	if len(parentID) > 8 {
		parentID = parentID[:8]
	}
	switch attempt {
	case 0:
		return baseTitle
	case 1:
		return fmt.Sprintf("%s · %s-%d", baseTitle, parentID, sequence)
	default:
		return fmt.Sprintf("%s · %s-%d-%d", baseTitle, parentID, sequence, attempt)
	}
}

func (s *SwarmService) createSwarmChild(ctx context.Context, parent *models.Task, child *models.Task) error {
	baseTitle := child.Title
	for attempt := 0; ; attempt++ {
		child.Title = swarmChildTitle(baseTitle, parent.ID, child.SwarmSequence, attempt)
		if err := s.taskSvc.Create(ctx, child); err != nil {
			if !errors.Is(err, ErrDuplicateTask) {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

func (s *SwarmService) updateSwarmChild(ctx context.Context, parent *models.Task, child *models.Task) error {
	baseTitle := child.Title
	for attempt := 0; ; attempt++ {
		child.Title = swarmChildTitle(baseTitle, parent.ID, child.SwarmSequence, attempt)
		if err := s.taskSvc.Update(ctx, child); err != nil {
			if !errors.Is(err, ErrDuplicateTask) {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

func (s *SwarmService) ApplyPlannerOutput(ctx context.Context, plannerTaskID string, output PlannerOutput) error {
	s.orchestration.Lock()
	defer s.orchestration.Unlock()
	output.NormalizeMergerFields()
	planner, err := s.taskRepo.GetByID(ctx, plannerTaskID)
	if err != nil || planner == nil {
		return fmt.Errorf("loading planner task: %w", err)
	}
	if planner.ParentTaskID == nil {
		return errors.New("planner has no swarm parent")
	}
	parent, err := s.taskRepo.GetByID(ctx, *planner.ParentTaskID)
	if err != nil || parent == nil {
		return fmt.Errorf("loading swarm parent: %w", err)
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	if err := validatePlannerOutput(output, parentCfg); err != nil {
		planner.SwarmStatus = "invalid_plan"
		cfg, _ := models.ParseSwarmConfig(planner.SwarmConfig)
		cfg.LastError = err.Error()
		planner.SwarmConfig, _ = cfg.JSON()
		_ = s.taskRepo.UpdateSwarmFields(ctx, planner.ID, planner.SwarmRole, planner.SwarmStatus, planner.SwarmConfig, planner.SwarmSequence)
		_ = s.taskRepo.UpdateStatus(ctx, planner.ID, models.StatusFailed)
		parent.SwarmStatus = "blocked"
		parentCfg.LastError = err.Error()
		parent.SwarmConfig, _ = parentCfg.JSON()
		_ = s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, parent.SwarmStatus, parent.SwarmConfig, parent.SwarmSequence)
		_ = s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusBlocked)
		return err
	}
	existing, err := s.taskRepo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		return err
	}
	if isFollowupPlannerApplication(planner, parent) {
		return s.applyFollowupPlannerOutput(ctx, parent, planner, output, existing, parentCfg)
	}
	if planner.SwarmStatus == "planned" {
		return s.recomputeParentStatusLocked(ctx, parent.ID)
	}
	parentCfg.ReviewerPrompt = output.ReviewerPrompt
	parentCfg.MergerPrompt = output.MergerPrompt
	parentCfg.PlannerNotes = output.Notes
	parentCfg.LastError = ""
	parentCfgJSON, _ := parentCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "workers_running", parentCfgJSON, parent.SwarmSequence); err != nil {
		return err
	}
	for i, worker := range output.Workers {
		sequence := 10 + i
		if findWorkerBySequence(existing, sequence) != nil {
			continue
		}
		created, err := s.createWorkerFromPlan(ctx, parent, worker, parentCfg, sequence)
		if err != nil {
			return err
		}
		existing = append(existing, *created)
	}
	if parentCfg.ReviewerEnabled && findChild(existing, models.SwarmRoleReviewer) == nil {
		cfg := models.SwarmConfig{Isolation: "read_only", DependsOnRoles: []string{"worker"}, RerunGeneration: parentCfg.Generation, Required: true}
		cfgJSON, _ := cfg.JSON()
		child := &models.Task{ProjectID: parent.ProjectID, Title: parent.Title + " · Reviewer", Prompt: reviewerPrompt(parent.Prompt, output.ReviewerPrompt), Category: models.CategoryActive, Priority: parent.Priority, Status: models.StatusBlocked, AgentID: parent.AgentID, AgentDefinitionID: parent.AgentDefinitionID, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleReviewer, SwarmStatus: "pending", SwarmConfig: cfgJSON, SwarmSequence: 900}
		if err := s.createSwarmChild(ctx, parent, child); err != nil {
			return err
		}
		existing = append(existing, *child)
	}
	if parentCfg.MergerEnabled && findMergerChild(existing) == nil {
		cfg := models.SwarmConfig{Isolation: "worktree", DependsOnRoles: []string{"reviewer"}, RerunGeneration: parentCfg.Generation, Required: true}
		cfgJSON, _ := cfg.JSON()
		child := &models.Task{ProjectID: parent.ProjectID, Title: parent.Title + " · Merger", Prompt: mergerPrompt(parent.Prompt, output.MergerPrompt, parent.WorktreePath), Category: models.CategoryActive, Priority: parent.Priority, Status: models.StatusBlocked, AgentID: parent.AgentID, AgentDefinitionID: parent.AgentDefinitionID, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleMerger, SwarmStatus: "pending", SwarmConfig: cfgJSON, SwarmSequence: 1000, WorktreePath: parent.WorktreePath, WorktreeBranch: parent.WorktreeBranch}
		if err := s.createSwarmChild(ctx, parent, child); err != nil {
			return err
		}
	}
	plannerCfg, _ := models.ParseSwarmConfig(planner.SwarmConfig)
	plannerCfg.LastError = ""
	planner.SwarmConfig, _ = plannerCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, planner.ID, planner.SwarmRole, "planned", planner.SwarmConfig, planner.SwarmSequence); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateStatus(ctx, planner.ID, models.StatusCompleted); err != nil {
		return err
	}
	return s.StartReadyWorkers(ctx, parent.ID)
}

func (s *SwarmService) createWorkerFromPlan(ctx context.Context, parent *models.Task, worker PlannerWorker, parentCfg models.SwarmConfig, sequence int) (*models.Task, error) {
	cfg := models.SwarmConfig{WorkerKind: worker.WorkerKind, Ownership: worker.Ownership, Isolation: workerIsolation(worker, parentCfg), DependsOnRoles: []string{"planner"}, RerunGeneration: parentCfg.Generation, Required: worker.Required || !worker.ReadOnly, WriteScope: worker.WriteScope, ReadScope: worker.ReadScope}
	cfgJSON, _ := cfg.JSON()
	child := &models.Task{ProjectID: parent.ProjectID, Title: worker.Title, Prompt: workerPrompt(parent.Prompt, worker, ""), Category: models.CategoryActive, Priority: parent.Priority, Status: models.StatusPending, AgentID: parent.AgentID, AgentDefinitionID: parent.AgentDefinitionID, ParentTaskID: &parent.ID, SwarmRole: models.SwarmRoleWorker, SwarmStatus: "running", SwarmConfig: cfgJSON, SwarmSequence: sequence}
	if cfg.Isolation == "worktree" {
		child.WorktreeBranch = swarmBranch(parent.Title, worker.WorkerKind, "pending")
		child.WorktreePath = filepath.Join(".worktrees", child.WorktreeBranch)
	}
	if err := s.createSwarmChild(ctx, parent, child); err != nil {
		return nil, err
	}
	if cfg.Isolation == "worktree" {
		branch := swarmBranch(parent.Title, worker.WorkerKind, child.ID)
		path := filepath.Join(".worktrees", branch)
		_ = s.taskRepo.UpdateWorktreeInfo(ctx, child.ID, path, branch)
		child.WorktreeBranch = branch
		child.WorktreePath = path
	}
	return child, nil
}

func (s *SwarmService) applyFollowupPlannerOutput(ctx context.Context, parent *models.Task, planner *models.Task, output PlannerOutput, existing []models.Task, parentCfg models.SwarmConfig) error {
	if err := validateFollowupPlannerOutput(output, existing, parentCfg); err != nil {
		return err
	}
	parentCfg.ReviewerPrompt = output.ReviewerPrompt
	parentCfg.MergerPrompt = output.MergerPrompt
	parentCfg.PlannerNotes = output.Notes
	parentCfg.LastError = ""
	parentCfg.ReviewedGeneration = 0
	parentCfg.MergedGeneration = 0
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "workers_running", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		return err
	}
	workersByID := map[string]models.Task{}
	affected := map[string]bool{}
	maxSeq := 9
	for _, child := range existing {
		if child.SwarmRole == models.SwarmRoleWorker {
			workersByID[child.ID] = child
			if child.SwarmSequence > maxSeq {
				maxSeq = child.SwarmSequence
			}
		}
	}
	createdWorkers := followupCreatedWorkers(output, existing, parentCfg.Generation)
	newWorkerIndex := 0
	for _, worker := range output.Workers {
		if strings.TrimSpace(worker.TaskID) == "" {
			if newWorkerIndex < len(createdWorkers) {
				affected[createdWorkers[newWorkerIndex].ID] = true
				newWorkerIndex++
				continue
			}
			maxSeq++
			created, err := s.createWorkerFromPlan(ctx, parent, worker, parentCfg, maxSeq)
			if err != nil {
				return err
			}
			affected[created.ID] = true
			newWorkerIndex++
			continue
		}
		compactChild, ok := workersByID[worker.TaskID]
		if !ok {
			return fmt.Errorf("coordinator referenced unknown worker task %s", worker.TaskID)
		}
		fullChild, err := s.taskRepo.GetByID(ctx, compactChild.ID)
		if err != nil {
			return err
		}
		if fullChild == nil {
			return fmt.Errorf("loading worker task %s: task not found", compactChild.ID)
		}
		child := *fullChild
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		cfg.WorkerKind = worker.WorkerKind
		cfg.Ownership = worker.Ownership
		cfg.Isolation = workerIsolation(worker, parentCfg)
		cfg.RerunGeneration = parentCfg.Generation
		cfg.Required = worker.Required || !worker.ReadOnly
		cfg.WriteScope = worker.WriteScope
		cfg.ReadScope = worker.ReadScope
		child.Title = worker.Title
		child.Prompt = workerPrompt(parent.Prompt, worker, child.WorktreePath)
		child.Status = models.StatusPending
		child.Category = models.CategoryActive
		child.SwarmStatus = "rerun_pending"
		child.SwarmConfig, _ = cfg.JSON()
		if err := s.updateSwarmChild(ctx, parent, &child); err != nil {
			return err
		}
		affected[child.ID] = true
		if s.workerSvc != nil {
			s.workerSvc.Submit(child)
		}
	}
	for _, child := range existing {
		if child.SwarmRole != models.SwarmRoleWorker || affected[child.ID] || child.Status != models.StatusCompleted {
			continue
		}
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		if cfg.CompletedGeneration < parentCfg.Generation {
			cfg.CompletedGeneration = parentCfg.Generation
			child.SwarmConfig, _ = cfg.JSON()
			if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				return err
			}
		}
	}
	for _, child := range existing {
		switch child.SwarmRole {
		case models.SwarmRoleReviewer, models.SwarmRoleMerger, models.SwarmRoleLegacyIntegrator:
			cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
			cfg.RerunGeneration = parentCfg.Generation
			child.SwarmConfig, _ = cfg.JSON()
			if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, "pending", child.SwarmConfig, child.SwarmSequence); err != nil {
				return err
			}
			if err := s.taskRepo.UpdateStatus(ctx, child.ID, models.StatusBlocked); err != nil {
				return err
			}
		}
	}
	plannerCfg, _ := models.ParseSwarmConfig(planner.SwarmConfig)
	plannerCfg.LastError = ""
	planner.SwarmConfig, _ = plannerCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, planner.ID, planner.SwarmRole, "planned", planner.SwarmConfig, planner.SwarmSequence); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateStatus(ctx, planner.ID, models.StatusCompleted); err != nil {
		return err
	}
	return s.StartReadyWorkers(ctx, parent.ID)
}

func isFollowupPlannerApplication(planner, parent *models.Task) bool {
	if planner == nil || parent == nil {
		return false
	}
	plannerCfg, _ := models.ParseSwarmConfig(planner.SwarmConfig)
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	if plannerCfg.RerunGeneration <= 1 || plannerCfg.RerunGeneration != parentCfg.Generation {
		return false
	}
	return planner.SwarmStatus == "coordinating" || planner.SwarmStatus == "plan_apply_failed"
}

func findWorkerBySequence(children []models.Task, sequence int) *models.Task {
	for i := range children {
		if children[i].SwarmRole == models.SwarmRoleWorker && children[i].SwarmSequence == sequence {
			return &children[i]
		}
	}
	return nil
}

func hasExecutionChildren(children []models.Task) bool {
	for _, child := range children {
		if child.SwarmRole == models.SwarmRoleWorker || child.SwarmRole == models.SwarmRoleReviewer || isMergerRole(child.SwarmRole) {
			return true
		}
	}
	return false
}

func (s *SwarmService) StartReadyWorkers(ctx context.Context, parentTaskID string) error {
	children, err := s.taskRepo.ListSwarmChildren(ctx, parentTaskID)
	if err != nil {
		return err
	}
	for i := range children {
		if children[i].SwarmRole == models.SwarmRoleWorker && children[i].Status == models.StatusPending && s.workerSvc != nil {
			s.workerSvc.Submit(children[i])
		}
	}
	return nil
}

func (s *SwarmService) OnChildCompleted(ctx context.Context, childTaskID string) error {
	child, err := s.taskRepo.GetByID(ctx, childTaskID)
	if err != nil || child == nil || child.ParentTaskID == nil || !models.IsSwarmChildRole(child.SwarmRole) {
		return err
	}
	if child.SwarmRole == models.SwarmRolePlanner && child.Status == models.StatusCompleted {
		return s.applyCompletedPlannerExecution(ctx, child)
	}
	s.orchestration.Lock()
	defer s.orchestration.Unlock()
	return s.onChildCompletedLocked(ctx, childTaskID)
}

func (s *SwarmService) onChildCompletedLocked(ctx context.Context, childTaskID string) error {
	child, err := s.taskRepo.GetByID(ctx, childTaskID)
	if err != nil || child == nil || child.ParentTaskID == nil {
		return err
	}
	parent, err := s.taskRepo.GetByID(ctx, *child.ParentTaskID)
	if err != nil || parent == nil {
		return err
	}
	children, err := s.taskRepo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		return err
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	if child.Status == models.StatusFailed {
		failedStatus := "failed"
		if child.SwarmStatus == "followup_pending" || child.SwarmStatus == "followup_failed" {
			failedStatus = "followup_failed"
		}
		if child.SwarmStatus != failedStatus {
			if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, failedStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
				return err
			}
		}
		return s.recomputeParentStatusLocked(ctx, parent.ID)
	}
	if child.Status == models.StatusCancelled {
		return s.handleChildCancelled(ctx, parent, child, parentCfg)
	}
	if child.SwarmRole == models.SwarmRoleWorker && child.Status == models.StatusCompleted {
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		targetGeneration := cfg.RerunGeneration
		if targetGeneration <= 0 {
			targetGeneration = cfg.CompletedGeneration
		}
		if targetGeneration != parentCfg.Generation {
			return s.recomputeParentStatusLocked(ctx, parent.ID)
		}
		if cfg.CompletedGeneration < targetGeneration || child.SwarmStatus != "completed" {
			cfg.CompletedGeneration = targetGeneration
			child.SwarmConfig, _ = cfg.JSON()
			if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, "completed", child.SwarmConfig, child.SwarmSequence); err != nil {
				return err
			}
			child.SwarmStatus = "completed"
		}
		if refreshed, refreshErr := s.taskRepo.ListSwarmChildren(ctx, parent.ID); refreshErr == nil {
			children = refreshed
		}
	}
	if child.SwarmRole == models.SwarmRoleWorker && child.Status == models.StatusCompleted && allRequiredWorkersCompleted(children, parentCfg.Generation) && parentCfg.ReviewerEnabled {
		if reviewer := findChild(children, models.SwarmRoleReviewer); reviewer != nil {
			revCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
			if revCfg.ReviewedGeneration < parentCfg.Generation && reviewer.Status != models.StatusRunning && reviewer.Status != models.StatusPending && (reviewer.Status != models.StatusCompleted || revCfg.RerunGeneration < parentCfg.Generation) && child.Status != models.StatusFailed {
				revCfg.RerunGeneration = parentCfg.Generation
				reviewer.SwarmConfig, _ = revCfg.JSON()
				_ = s.taskRepo.UpdateSwarmFields(ctx, reviewer.ID, reviewer.SwarmRole, "ready", reviewer.SwarmConfig, reviewer.SwarmSequence)
				_ = s.taskRepo.UpdateCategory(ctx, reviewer.ID, models.CategoryActive)
				_ = s.taskRepo.UpdateStatus(ctx, reviewer.ID, models.StatusPending)
				reviewer.Category = models.CategoryActive
				reviewer.Status = models.StatusPending
				if s.workerSvc != nil {
					s.workerSvc.Submit(*reviewer)
				}
			}
		}
	}
	if child.SwarmRole == models.SwarmRoleReviewer && child.Status == models.StatusCompleted {
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		targetGeneration := cfg.RerunGeneration
		if targetGeneration <= 0 {
			targetGeneration = cfg.ReviewedGeneration
		}
		if targetGeneration != parentCfg.Generation || !allRequiredWorkersCompleted(children, targetGeneration) {
			return s.recomputeParentStatusLocked(ctx, parent.ID)
		}
		if cfg.ReviewedGeneration < targetGeneration || child.SwarmStatus != "reviewed" {
			cfg.ReviewedGeneration = targetGeneration
			child.SwarmConfig, _ = cfg.JSON()
			if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, "reviewed", child.SwarmConfig, child.SwarmSequence); err != nil {
				return err
			}
			child.SwarmStatus = "reviewed"
		}
	}
	children, _ = s.taskRepo.ListSwarmChildren(ctx, parent.ID)
	integrationTargetGeneration := 0
	if parentCfg.ReviewerEnabled {
		if reviewer := findChild(children, models.SwarmRoleReviewer); reviewer != nil {
			revCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
			integrationTargetGeneration = revCfg.ReviewedGeneration
		}
	} else if allRequiredWorkersCompleted(children, parentCfg.Generation) {
		integrationTargetGeneration = parentCfg.Generation
	}
	if parentCfg.MergerEnabled && integrationTargetGeneration > parentCfg.MergedGeneration && !(isMergerRole(child.SwarmRole) && child.Status == models.StatusCompleted) {
		if merger := findMergerChild(children); merger != nil {
			intCfg, _ := models.ParseSwarmConfig(merger.SwarmConfig)
			if merger.Status != models.StatusRunning && merger.Status != models.StatusPending && (merger.Status != models.StatusCompleted || intCfg.RerunGeneration < integrationTargetGeneration || intCfg.MergedGeneration >= integrationTargetGeneration) {
				intCfg.RerunGeneration = integrationTargetGeneration
				merger.SwarmConfig, _ = intCfg.JSON()
				_ = s.taskRepo.UpdateSwarmFields(ctx, merger.ID, merger.SwarmRole, "ready", merger.SwarmConfig, merger.SwarmSequence)
				_ = s.taskRepo.UpdateCategory(ctx, merger.ID, models.CategoryActive)
				_ = s.taskRepo.UpdateStatus(ctx, merger.ID, models.StatusPending)
				merger.Category = models.CategoryActive
				merger.Status = models.StatusPending
				if s.workerSvc != nil {
					s.workerSvc.Submit(*merger)
				}
			}
		}
	}
	if isMergerRole(child.SwarmRole) && child.Status == models.StatusCompleted {
		if refreshed, refreshErr := s.taskRepo.ListSwarmChildren(ctx, parent.ID); refreshErr == nil {
			children = refreshed
		}
		child, _ = s.taskRepo.GetByID(ctx, child.ID)
		if child == nil {
			return nil
		}
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		targetGeneration := cfg.RerunGeneration
		if targetGeneration <= 0 {
			targetGeneration = cfg.MergedGeneration
		}
		reviewedCurrent := !parentCfg.ReviewerEnabled
		if reviewer := findChild(children, models.SwarmRoleReviewer); reviewer != nil && parentCfg.ReviewerEnabled {
			revCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
			reviewedCurrent = revCfg.ReviewedGeneration >= targetGeneration && reviewer.Status == models.StatusCompleted
		}
		if targetGeneration <= parentCfg.MergedGeneration || targetGeneration != parentCfg.Generation || !reviewedCurrent || hasActiveSwarmWorkBeforeIntegration(children) {
			return s.recomputeParentStatusLocked(ctx, parent.ID)
		}
		cfg.MergedGeneration = targetGeneration
		child.SwarmConfig, _ = cfg.JSON()
		if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, "integrated", child.SwarmConfig, child.SwarmSequence); err != nil {
			return err
		}
		child.SwarmStatus = "integrated"
		if err := s.persistMergerResultToParent(ctx, parent.ID, child.ID); err != nil {
			return err
		}
		parentCfg.MergedGeneration = targetGeneration
		parent.SwarmConfig, _ = parentCfg.JSON()
		_ = s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "current", parent.SwarmConfig, parent.SwarmSequence)
		_ = s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusCompleted)
		_ = s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryCompleted)
	}
	return s.recomputeParentStatusLocked(ctx, parent.ID)
}

func hasActiveSwarmWorkBeforeIntegration(children []models.Task) bool {
	for _, child := range children {
		if isMergerRole(child.SwarmRole) {
			continue
		}
		switch child.Status {
		case models.StatusPending, models.StatusQueued, models.StatusRunning:
			return true
		}
	}
	return false
}

func (s *SwarmService) handleChildCancelled(ctx context.Context, parent *models.Task, child *models.Task, parentCfg models.SwarmConfig) error {
	if parent == nil || child == nil {
		return nil
	}
	swarmStatus := "blocked"
	parentStatus := models.StatusBlocked
	switch child.SwarmRole {
	case models.SwarmRoleWorker:
		swarmStatus = "needs_coordination"
		parentCfg.LastError = fmt.Sprintf("worker %s was cancelled; coordinator must decide whether it is still required", child.ID)
	case models.SwarmRoleReviewer:
		swarmStatus = "needs_review"
		parentCfg.ReviewedGeneration = 0
		parentCfg.LastError = fmt.Sprintf("reviewer %s was cancelled; review must be rerun", child.ID)
	default:
		if !isMergerRole(child.SwarmRole) {
			return s.recomputeParentStatusLocked(ctx, parent.ID)
		}
		swarmStatus = "needs_integration"
		parentCfg.MergedGeneration = 0
		parentCfg.LastError = fmt.Sprintf("merger %s was cancelled; merge must be rerun", child.ID)
	}
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, swarmStatus, parent.SwarmConfig, parent.SwarmSequence); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateStatus(ctx, parent.ID, parentStatus); err != nil {
		return err
	}
	if parent.Category == models.CategoryCompleted {
		return s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive)
	}
	return nil
}

func (s *SwarmService) HandleParentFollowup(ctx context.Context, parentTaskID string, message string) error {
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil || parent == nil {
		return err
	}
	cfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	cfg.Generation++
	if cfg.Generation == 0 {
		cfg.Generation = 1
	}
	cfg.MergedGeneration = 0
	parent.SwarmConfig, _ = cfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_coordination", parent.SwarmConfig, parent.SwarmSequence); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
		return err
	}
	planner, err := s.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
	if err != nil {
		return err
	}
	if planner == nil {
		return s.StartPlanner(ctx, parent.ID)
	}
	planner, err = s.taskRepo.GetByID(ctx, planner.ID)
	if err != nil {
		return err
	}
	if planner == nil {
		return fmt.Errorf("loading planner task for parent %s: task not found", parent.ID)
	}
	planner.Prompt = coordinatorFollowupPrompt(parent.Prompt, message, cfg.Generation)
	planner.Status = models.StatusPending
	planner.Category = models.CategoryActive
	planner.SwarmStatus = "coordinating"
	plannerCfg, _ := models.ParseSwarmConfig(planner.SwarmConfig)
	plannerCfg.RerunGeneration = cfg.Generation
	planner.SwarmConfig, _ = plannerCfg.JSON()
	if err := s.taskRepo.Update(ctx, planner); err != nil {
		return err
	}
	if s.workerSvc != nil {
		s.workerSvc.Submit(*planner)
	}
	return nil
}

func (s *SwarmService) HandleChildFollowup(ctx context.Context, childTaskID string, message string) error {
	s.orchestration.Lock()
	defer s.orchestration.Unlock()
	child, err := s.taskRepo.GetByID(ctx, childTaskID)
	if err != nil || child == nil || child.ParentTaskID == nil || !models.IsSwarmChildRole(child.SwarmRole) {
		return err
	}
	parent, err := s.taskRepo.GetByID(ctx, *child.ParentTaskID)
	if err != nil || parent == nil {
		return err
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	childCfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
	repairingFailure := child.Status == models.StatusFailed || child.SwarmStatus == "failed" || child.SwarmStatus == "followup_failed"
	swarmStatus := ""
	switch child.SwarmRole {
	case models.SwarmRoleWorker:
		if repairingFailure {
			if parentCfg.Generation <= 0 {
				parentCfg.Generation = 1
			}
		} else {
			parentCfg.Generation++
			if parentCfg.Generation == 0 {
				parentCfg.Generation = 1
			}
		}
		parentCfg.ReviewedGeneration = min(parentCfg.ReviewedGeneration, parentCfg.Generation-1)
		parentCfg.MergedGeneration = min(parentCfg.MergedGeneration, parentCfg.Generation-1)
		childCfg.RerunGeneration = parentCfg.Generation
		childCfg.CompletedGeneration = min(childCfg.CompletedGeneration, parentCfg.Generation-1)
		swarmStatus = "needs_review"
	case models.SwarmRoleReviewer:
		if parentCfg.Generation <= 0 {
			parentCfg.Generation = 1
		}
		parentCfg.MergedGeneration = min(parentCfg.MergedGeneration, parentCfg.Generation-1)
		childCfg.RerunGeneration = max(childCfg.RerunGeneration, parentCfg.Generation)
		childCfg.ReviewedGeneration = min(childCfg.ReviewedGeneration, parentCfg.Generation-1)
		swarmStatus = "needs_review"
	default:
		if !isMergerRole(child.SwarmRole) {
			return nil
		}
		if parentCfg.Generation <= 0 {
			parentCfg.Generation = 1
		}
		parentCfg.MergedGeneration = min(parentCfg.MergedGeneration, parentCfg.Generation-1)
		childCfg.RerunGeneration = max(childCfg.RerunGeneration, max(parentCfg.ReviewedGeneration, parentCfg.Generation))
		childCfg.MergedGeneration = min(childCfg.MergedGeneration, parentCfg.Generation-1)
		swarmStatus = "needs_integration"
	}
	if parentCfg.ReviewedGeneration < 0 {
		parentCfg.ReviewedGeneration = 0
	}
	if parentCfg.MergedGeneration < 0 {
		parentCfg.MergedGeneration = 0
	}
	if childCfg.CompletedGeneration < 0 {
		childCfg.CompletedGeneration = 0
	}
	if childCfg.ReviewedGeneration < 0 {
		childCfg.ReviewedGeneration = 0
	}
	if childCfg.MergedGeneration < 0 {
		childCfg.MergedGeneration = 0
	}
	parent.SwarmConfig, _ = parentCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, swarmStatus, parent.SwarmConfig, parent.SwarmSequence); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
		return err
	}
	child.SwarmConfig, _ = childCfg.JSON()
	child.SwarmStatus = "followup_pending"
	return s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence)
}

func (s *SwarmService) ReactivateParentForChildFollowupRetry(ctx context.Context, childTaskID string) error {
	s.orchestration.Lock()
	defer s.orchestration.Unlock()
	child, err := s.taskRepo.GetByID(ctx, childTaskID)
	if err != nil || child == nil || child.ParentTaskID == nil || !models.IsSwarmChildRole(child.SwarmRole) || (child.SwarmStatus != "followup_pending" && child.SwarmStatus != "followup_failed") {
		return err
	}
	parent, err := s.taskRepo.GetByID(ctx, *child.ParentTaskID)
	if err != nil || parent == nil {
		return err
	}
	if err := s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		return err
	}
	if err := s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
		return err
	}
	if child.SwarmStatus == "followup_failed" {
		return s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, "followup_pending", child.SwarmConfig, child.SwarmSequence)
	}
	return nil
}

func (s *SwarmService) RerunRole(ctx context.Context, parentTaskID string, role models.SwarmRole) (*models.Task, error) {
	if role != models.SwarmRoleReviewer && !isMergerRole(role) {
		return nil, fmt.Errorf("unsupported swarm rerun role %q", role)
	}
	mergerRerun := isMergerRole(role)
	if mergerRerun {
		role = models.SwarmRoleMerger
	}
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil || parent == nil {
		return nil, err
	}
	child, err := s.taskRepo.FindSwarmChildByRole(ctx, parentTaskID, role)
	if err == nil && child == nil && mergerRerun {
		child, err = s.taskRepo.FindSwarmChildByRole(ctx, parentTaskID, models.SwarmRoleLegacyIntegrator)
	}
	if err != nil || child == nil {
		return child, err
	}
	if child.Status == models.StatusRunning || child.Status == models.StatusQueued || child.Status == models.StatusPending {
		return nil, ErrSwarmRoleActive
	}
	if s.execRepo != nil {
		active, err := s.execRepo.HasActiveTaskExecution(ctx, child.ID, "")
		if err != nil {
			return nil, err
		}
		if active {
			return nil, ErrSwarmRoleActive
		}
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	childCfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
	childCfg.RerunGeneration = max(childCfg.RerunGeneration, parentCfg.Generation)
	swarmStatus := "pending"
	if role == models.SwarmRoleReviewer {
		if parentCfg.MergedGeneration >= parentCfg.Generation {
			parentCfg.MergedGeneration = parentCfg.Generation - 1
			if parentCfg.MergedGeneration < 0 {
				parentCfg.MergedGeneration = 0
			}
		}
		parent.SwarmConfig, _ = parentCfg.JSON()
		if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_review", parent.SwarmConfig, parent.SwarmSequence); err != nil {
			return nil, err
		}
		swarmStatus = "rerun_review"
	} else {
		if parentCfg.ReviewedGeneration > 0 && childCfg.RerunGeneration < parentCfg.ReviewedGeneration {
			childCfg.RerunGeneration = parentCfg.ReviewedGeneration
		}
		if parentCfg.MergedGeneration >= childCfg.RerunGeneration {
			parentCfg.MergedGeneration = childCfg.RerunGeneration - 1
			if parentCfg.MergedGeneration < 0 {
				parentCfg.MergedGeneration = 0
			}
		}
		parent.SwarmConfig, _ = parentCfg.JSON()
		if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "needs_integration", parent.SwarmConfig, parent.SwarmSequence); err != nil {
			return nil, err
		}
		swarmStatus = "rerun_integration"
	}
	if err := s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning); err != nil {
		return nil, err
	}
	if err := s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryActive); err != nil {
		return nil, err
	}
	child.SwarmStatus = swarmStatus
	child.SwarmConfig, _ = childCfg.JSON()
	if err := s.taskRepo.UpdateSwarmFields(ctx, child.ID, child.SwarmRole, child.SwarmStatus, child.SwarmConfig, child.SwarmSequence); err != nil {
		return nil, err
	}
	if err := s.taskRepo.UpdateCategory(ctx, child.ID, models.CategoryActive); err != nil {
		return nil, err
	}
	if err := s.taskRepo.UpdateStatus(ctx, child.ID, models.StatusPending); err != nil {
		return nil, err
	}
	child.Category = models.CategoryActive
	child.Status = models.StatusPending
	if s.workerSvc != nil {
		s.workerSvc.Submit(*child)
	}
	return child, nil
}

func (s *SwarmService) RecomputeParentStatus(ctx context.Context, parentTaskID string) error {
	s.orchestration.Lock()
	defer s.orchestration.Unlock()

	children, err := s.taskRepo.ListSwarmChildren(ctx, parentTaskID)
	if err != nil {
		return err
	}
	for _, role := range []models.SwarmRole{models.SwarmRoleWorker, models.SwarmRoleReviewer, models.SwarmRoleMerger, models.SwarmRoleLegacyIntegrator} {
		for i := range children {
			child := &children[i]
			if child.SwarmRole != role {
				continue
			}
			switch child.Status {
			case models.StatusCompleted, models.StatusFailed, models.StatusCancelled:
				if err := s.onChildCompletedLocked(ctx, child.ID); err != nil {
					return err
				}
			}
		}
	}
	return s.recomputeParentStatusLocked(ctx, parentTaskID)
}

func (s *SwarmService) recomputeParentStatusLocked(ctx context.Context, parentTaskID string) error {
	parent, err := s.taskRepo.GetByID(ctx, parentTaskID)
	if err != nil || parent == nil || parent.SwarmRole != models.SwarmRoleParent {
		return err
	}
	children, err := s.taskRepo.ListSwarmChildren(ctx, parent.ID)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		return nil
	}

	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	status := models.StatusBlocked
	anyRunnable := false
	anyFailed := false
	anyBlocked := false
	for _, child := range children {
		switch child.Status {
		case models.StatusRunning, models.StatusPending, models.StatusQueued:
			anyRunnable = true
		case models.StatusFailed:
			anyFailed = true
		case models.StatusBlocked:
			anyBlocked = true
		}
	}
	switch {
	case anyRunnable:
		status = models.StatusRunning
	case anyFailed:
		status = models.StatusFailed
	case anyBlocked:
		status = models.StatusBlocked
	case parentCfg.Generation > 0 && parentCfg.MergerEnabled && parentCfg.MergedGeneration >= parentCfg.Generation:
		if merger := findMergerChild(children); merger != nil && merger.Status == models.StatusCompleted {
			status = models.StatusCompleted
		}
	case swarmCompleteWithoutMerger(children, parentCfg):
		status = models.StatusCompleted
		if parentCfg.MergedGeneration < parentCfg.Generation {
			parentCfg.MergedGeneration = parentCfg.Generation
			parent.SwarmConfig, _ = parentCfg.JSON()
			if err := s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "current", parent.SwarmConfig, parent.SwarmSequence); err != nil {
				return err
			}
			parent.SwarmStatus = "current"
		}
	}
	if parent.Status != status {
		if err := s.taskRepo.UpdateStatus(ctx, parent.ID, status); err != nil {
			return err
		}
	}
	if status == models.StatusCompleted && parent.Category != models.CategoryCompleted {
		return s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryCompleted)
	}
	if status == models.StatusBlocked && !anyRunnable && parent.Category == models.CategoryActive {
		return s.taskRepo.UpdateCategory(ctx, parent.ID, models.CategoryBacklog)
	}
	return nil
}

func swarmCompleteWithoutMerger(children []models.Task, parentCfg models.SwarmConfig) bool {
	if parentCfg.Generation <= 0 || parentCfg.MergerEnabled || !allRequiredWorkersCompleted(children, parentCfg.Generation) {
		return false
	}
	if !parentCfg.ReviewerEnabled {
		return true
	}
	reviewer := findChild(children, models.SwarmRoleReviewer)
	if reviewer == nil || reviewer.Status != models.StatusCompleted {
		return false
	}
	reviewerCfg, _ := models.ParseSwarmConfig(reviewer.SwarmConfig)
	return reviewerCfg.ReviewedGeneration >= parentCfg.Generation
}

func (s *SwarmService) CancelSwarm(ctx context.Context, parentTaskID string) error {
	children, err := s.taskRepo.ListSwarmChildren(ctx, parentTaskID)
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.Status == models.StatusRunning && s.workerSvc != nil {
			s.workerSvc.CancelRunningTask(child.ID)
		}
		if child.Status == models.StatusPending || child.Status == models.StatusQueued || child.Status == models.StatusRunning || child.Status == models.StatusBlocked {
			_ = s.taskRepo.UpdateStatus(ctx, child.ID, models.StatusCancelled)
		}
	}
	if parent, err := s.taskRepo.GetByID(ctx, parentTaskID); err == nil && parent != nil {
		return s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusCancelled)
	}
	return nil
}

func (s *SwarmService) applyCompletedPlannerExecution(ctx context.Context, planner *models.Task) (retErr error) {
	defer func() {
		if retErr != nil && planner != nil && planner.SwarmStatus != "planned" && planner.SwarmStatus != "invalid_plan" {
			s.recordPlannerApplicationError(planner, retErr)
		}
	}()
	children, err := s.taskRepo.ListSwarmChildren(ctx, *planner.ParentTaskID)
	if err != nil {
		return err
	}
	if hasExecutionChildren(children) && planner.SwarmStatus == "planned" {
		return s.RecomputeParentStatus(ctx, *planner.ParentTaskID)
	}
	if hasExecutionChildren(children) && planner.SwarmStatus != "planned" {
		parent, err := s.taskRepo.GetByID(ctx, *planner.ParentTaskID)
		if err != nil || parent == nil {
			return err
		}
		if !isFollowupPlannerApplication(planner, parent) && planner.SwarmStatus == "coordinating" {
			return s.RecomputeParentStatus(ctx, *planner.ParentTaskID)
		}
	}
	if s.execRepo == nil {
		return errors.New("swarm planner output unavailable: execution repository is not configured")
	}
	exec, err := s.execRepo.GetLatestCompletedByTask(ctx, planner.ID)
	if err != nil {
		return err
	}
	if exec == nil || strings.TrimSpace(exec.Output) == "" {
		return s.markPlannerOutputInvalid(ctx, planner, "planner completed without JSON output")
	}
	output, err := ParsePlannerOutputJSON(exec.Output)
	if err != nil {
		return s.markPlannerOutputInvalid(ctx, planner, fmt.Sprintf("invalid planner JSON: %v", err))
	}
	return s.ApplyPlannerOutput(ctx, planner.ID, output)
}

func (s *SwarmService) recordPlannerApplicationError(planner *models.Task, cause error) {
	if planner == nil || planner.ParentTaskID == nil || cause == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	storedPlanner, err := s.taskRepo.GetByID(ctx, planner.ID)
	if err != nil || storedPlanner == nil || storedPlanner.SwarmStatus == "invalid_plan" || storedPlanner.SwarmStatus == "planned" {
		return
	}
	message := fmt.Sprintf("applying planner output: %v", cause)
	parent, err := s.taskRepo.GetByID(ctx, *storedPlanner.ParentTaskID)
	if err != nil || parent == nil {
		return
	}
	followup := isFollowupPlannerApplication(storedPlanner, parent)

	plannerCfg, _ := models.ParseSwarmConfig(storedPlanner.SwarmConfig)
	plannerCfg.LastError = message
	storedPlanner.SwarmConfig, _ = plannerCfg.JSON()
	storedPlanner.SwarmStatus = "plan_apply_failed"
	if followup {
		storedPlanner.SwarmStatus = "coordinating"
	}
	_ = s.taskRepo.UpdateSwarmFields(ctx, storedPlanner.ID, storedPlanner.SwarmRole, storedPlanner.SwarmStatus, storedPlanner.SwarmConfig, storedPlanner.SwarmSequence)

	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.LastError = message
	parent.SwarmConfig, _ = parentCfg.JSON()
	parent.SwarmStatus = "blocked"
	if followup {
		parent.SwarmStatus = "needs_coordination"
	}
	_ = s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, parent.SwarmStatus, parent.SwarmConfig, parent.SwarmSequence)
	_ = s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusBlocked)
}

func (s *SwarmService) persistMergerResultToParent(ctx context.Context, parentTaskID, mergerTaskID string) error {
	if s.execRepo == nil {
		return errors.New("swarm merger result unavailable: execution repository is not configured")
	}
	exec, err := s.execRepo.GetLatestCompletedByTask(ctx, mergerTaskID)
	if err != nil {
		return err
	}
	if exec == nil {
		return errors.New("swarm merger completed without a final execution")
	}
	if err := s.execRepo.UpsertSwarmParentResult(ctx, parentTaskID, exec.ID, exec.Output, exec.DiffOutput, exec.DurationMs); err != nil {
		return err
	}
	if strings.TrimSpace(exec.DiffOutput) != "" {
		return s.taskRepo.UpdateMergeStatus(ctx, parentTaskID, models.MergeStatusPending)
	}
	return nil
}

func (s *SwarmService) markPlannerOutputInvalid(ctx context.Context, planner *models.Task, message string) error {
	if planner == nil || planner.ParentTaskID == nil {
		return errors.New(message)
	}
	cfg, _ := models.ParseSwarmConfig(planner.SwarmConfig)
	cfg.LastError = message
	planner.SwarmConfig, _ = cfg.JSON()
	planner.SwarmStatus = "invalid_plan"
	_ = s.taskRepo.UpdateSwarmFields(ctx, planner.ID, planner.SwarmRole, planner.SwarmStatus, planner.SwarmConfig, planner.SwarmSequence)
	_ = s.taskRepo.UpdateStatus(ctx, planner.ID, models.StatusFailed)
	parent, err := s.taskRepo.GetByID(ctx, *planner.ParentTaskID)
	if err != nil || parent == nil {
		return errors.New(message)
	}
	parentCfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	parentCfg.LastError = message
	parent.SwarmConfig, _ = parentCfg.JSON()
	_ = s.taskRepo.UpdateSwarmFields(ctx, parent.ID, parent.SwarmRole, "blocked", parent.SwarmConfig, parent.SwarmSequence)
	_ = s.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusBlocked)
	return errors.New(message)
}

func (s *SwarmService) submitIfRunnable(ctx context.Context, task *models.Task) error {
	if task.Status == models.StatusBlocked || task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return nil
	}
	if task.Category != models.CategoryActive && task.Category != models.CategoryScheduled {
		if err := s.taskRepo.UpdateCategory(ctx, task.ID, models.CategoryActive); err != nil {
			return err
		}
		task.Category = models.CategoryActive
	}
	if task.Status != models.StatusPending {
		if err := s.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
			return err
		}
		task.Status = models.StatusPending
	}
	if s.workerSvc != nil {
		s.workerSvc.Submit(*task)
	}
	return nil
}

func (s *SwarmService) ensureIntegrationWorktreeMetadata(ctx context.Context, parent *models.Task) error {
	if parent.WorktreePath != "" && parent.WorktreeBranch != "" {
		return nil
	}
	branch := swarmBranch(parent.Title, "integration", parent.ID)
	path := filepath.Join(".worktrees", branch)
	parent.WorktreePath = path
	parent.WorktreeBranch = branch
	return s.taskRepo.UpdateWorktreeInfo(ctx, parent.ID, path, branch)
}

func validatePlannerOutput(output PlannerOutput, parentCfg models.SwarmConfig) error {
	if len(output.Workers) == 0 {
		return errors.New("planner output must include at least one worker")
	}
	if parentCfg.MaxWorkers > 0 && len(output.Workers) > parentCfg.MaxWorkers {
		return fmt.Errorf("planner output has %d workers, max is %d", len(output.Workers), parentCfg.MaxWorkers)
	}
	if parentCfg.ReviewerEnabled && strings.TrimSpace(output.ReviewerPrompt) == "" {
		return errors.New("reviewer_prompt is required")
	}
	if parentCfg.MergerEnabled && strings.TrimSpace(output.MergerPrompt) == "" {
		return errors.New("merger_prompt is required")
	}
	for i, w := range output.Workers {
		if strings.TrimSpace(w.Title) == "" || strings.TrimSpace(w.Prompt) == "" || strings.TrimSpace(w.WorkerKind) == "" || len(w.Ownership) == 0 {
			return fmt.Errorf("worker %d must include title, prompt, worker_kind, and ownership", i+1)
		}
		iso := workerIsolation(w, parentCfg)
		if iso != "read_only" && iso != "worktree" && iso != "shared" {
			return fmt.Errorf("worker %q has invalid isolation %q", w.Title, iso)
		}
		if iso == "shared" && parentCfg.DefaultWorkerIsolation != "shared" {
			return fmt.Errorf("worker %q requested shared isolation but parent does not allow it", w.Title)
		}
		if iso != "read_only" && iso != "worktree" {
			return fmt.Errorf("code-changing worker %q must use worktree isolation", w.Title)
		}
	}
	return nil
}

func validateFollowupPlannerOutput(output PlannerOutput, existing []models.Task, parentCfg models.SwarmConfig) error {
	workersByID := map[string]struct{}{}
	workerCount := 0
	newWorkers := 0
	for _, child := range existing {
		if child.SwarmRole == models.SwarmRoleWorker {
			workersByID[child.ID] = struct{}{}
			workerCount++
		}
	}
	for _, worker := range output.Workers {
		if strings.TrimSpace(worker.TaskID) == "" {
			newWorkers++
			continue
		}
		if _, ok := workersByID[worker.TaskID]; !ok {
			return fmt.Errorf("coordinator referenced unknown worker task %s", worker.TaskID)
		}
	}
	reconciledWorkers := len(followupCreatedWorkers(output, existing, parentCfg.Generation))
	missingWorkers := max(0, newWorkers-reconciledWorkers)
	if parentCfg.MaxWorkers > 0 && workerCount+missingWorkers > parentCfg.MaxWorkers {
		return fmt.Errorf("coordinator output would create %d workers, max is %d", workerCount+missingWorkers, parentCfg.MaxWorkers)
	}
	return nil
}

func followupCreatedWorkers(output PlannerOutput, existing []models.Task, generation int) []models.Task {
	referencedWorkerIDs := make(map[string]struct{}, len(output.Workers))
	for _, worker := range output.Workers {
		if taskID := strings.TrimSpace(worker.TaskID); taskID != "" {
			referencedWorkerIDs[taskID] = struct{}{}
		}
	}
	workers := make([]models.Task, 0)
	for _, child := range existing {
		if child.SwarmRole != models.SwarmRoleWorker {
			continue
		}
		if _, referenced := referencedWorkerIDs[child.ID]; referenced {
			continue
		}
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		if cfg.RerunGeneration == generation {
			workers = append(workers, child)
		}
	}
	sort.SliceStable(workers, func(i, j int) bool {
		if workers[i].SwarmSequence != workers[j].SwarmSequence {
			return workers[i].SwarmSequence < workers[j].SwarmSequence
		}
		return workers[i].ID < workers[j].ID
	})
	return workers
}

func ParsePlannerOutputJSON(raw string) (PlannerOutput, error) {
	trimmed := strings.TrimSpace(raw)
	var output PlannerOutput
	if err := json.Unmarshal([]byte(trimmed), &output); err == nil {
		output.NormalizeMergerFields()
		return output, nil
	}
	var lastValidCandidate *PlannerOutput
	for _, candidate := range plannerOutputJSONCandidates(trimmed) {
		var candidateOutput PlannerOutput
		if err := json.Unmarshal([]byte(candidate), &candidateOutput); err != nil {
			continue
		}
		if len(candidateOutput.Workers) == 0 {
			continue
		}
		candidateOutput.NormalizeMergerFields()
		lastValidCandidate = &candidateOutput
	}
	if lastValidCandidate != nil {
		return *lastValidCandidate, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &output); err != nil {
		return output, err
	}
	output.NormalizeMergerFields()
	return output, nil
}

func plannerOutputJSONCandidates(raw string) []string {
	var candidates []string
	addCandidate := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}
	for _, candidate := range balancedPlannerJSONObjects(raw) {
		addCandidate(candidate)
	}
	return candidates
}

func balancedPlannerJSONObjects(raw string) []string {
	var candidates []string
	inString := false
	escaped := false
	depth := 0
	start := -1
	for i, r := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == '"' {
				inString = false
			}
			continue
		}
		switch r {
		case '"':
			if depth > 0 {
				inString = true
			}
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 && start >= 0 {
				candidates = append(candidates, raw[start:i+1])
				start = -1
			}
		}
	}
	return candidates
}

func maxWorkers(parent *models.Task) int {
	cfg, _ := models.ParseSwarmConfig(parent.SwarmConfig)
	if cfg.MaxWorkers <= 0 {
		return 3
	}
	return cfg.MaxWorkers
}

func workerIsolation(w PlannerWorker, parentCfg models.SwarmConfig) string {
	if w.Isolation != "" {
		return w.Isolation
	}
	if parentCfg.DefaultWorkerIsolation != "" {
		return parentCfg.DefaultWorkerIsolation
	}
	return "worktree"
}

func allRequiredWorkersCompleted(children []models.Task, generation int) bool {
	workers := 0
	for _, child := range children {
		if child.SwarmRole != models.SwarmRoleWorker {
			continue
		}
		workers++
		cfg, _ := models.ParseSwarmConfig(child.SwarmConfig)
		if cfg.Required && (child.Status != models.StatusCompleted || cfg.CompletedGeneration < generation) {
			return false
		}
	}
	return workers > 0
}

func findChild(children []models.Task, role models.SwarmRole) *models.Task {
	for i := range children {
		if children[i].SwarmRole == role {
			return &children[i]
		}
	}
	return nil
}

func isMergerRole(role models.SwarmRole) bool {
	return role == models.SwarmRoleMerger || role == models.SwarmRoleLegacyIntegrator
}

func findMergerChild(children []models.Task) *models.Task {
	if child := findChild(children, models.SwarmRoleMerger); child != nil {
		return child
	}
	return findChild(children, models.SwarmRoleLegacyIntegrator)
}

func swarmBranch(title, kind, id string) string {
	return "swarm-" + slug(title) + "-" + slug(kind) + "-" + shortID(id)
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = nonSlug.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 24 {
		s = strings.Trim(s[:24], "-")
	}
	if s == "" {
		return "task"
	}
	return s
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[:8]
	}
	if id == "" || id == "pending" {
		return "pending"
	}
	return id
}

func plannerPrompt(parentPrompt string, maxWorkers int) string {
	return fmt.Sprintf(`You are the planner for a swarm task.

Your only job is to decompose the goal into worker tasks and handoff instructions. You are not a worker, reviewer, or merger.

Goal:
%s

Role boundaries:
- Do not implement the requested feature or bug fix yourself.
- Do not modify, create, delete, format, or regenerate files.
- Do not run build, test, formatter, generator, git, or shell commands.
- Do not investigate the repository beyond what is necessary to name high-level ownership and read/write scopes.
- Delegate all implementation, verification, review, and merge work to workers, reviewer, and merger through the JSON fields.

Worker scheduling constraint:
- Every entry in workers runs immediately and in parallel with every other worker. There is no dependency scheduling between workers: you cannot make one worker wait for another worker to finish, and natural-language phrasing like "after the other workers complete" or "once implementation is done" inside a worker's title/prompt will NOT be enforced.
- Never create a worker whose job is to validate, test, or review the output of other workers in this same plan. That work belongs in reviewer_prompt (runs only after all required workers complete) and merger_prompt (runs only after the reviewer). Only put independent, immediately-runnable implementation objectives in workers.

Output contract:
- Return exactly one raw JSON object and nothing else.
- Do not wrap the JSON in Markdown fences.
- Do not include progress notes, tool transcripts, analysis, or prose outside the JSON object.
- The JSON object must contain workers and may contain reviewer_prompt, merger_prompt, and notes.

Create up to %d workers. Each worker must have a bounded objective, clear ownership, and an isolation mode that can start immediately without waiting on any other worker. Prefer non-overlapping write scopes.

JSON schema:
	{"workers":[{"title":"Backend worker","prompt":"Implement API/service changes...","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","write_scope":["internal/service"],"read_scope":["."],"required":true}],"reviewer_prompt":"Review worker diffs...","merger_prompt":"Merge accepted worker outputs...","notes":"Optional short plan summary."}`, parentPrompt, maxWorkers)
}

func coordinatorFollowupPrompt(parentPrompt, followup string, generation int) string {
	return fmt.Sprintf(`You are coordinating a follow-up for an existing swarm task.

Your only job is to decide which workers need new delegated work. You are not a worker, reviewer, or merger.

Parent goal:
%s

Follow-up request:
%s

Role boundaries:
- Do not implement the follow-up yourself.
- Do not modify, create, delete, format, or regenerate files.
- Do not run build, test, formatter, generator, git, or shell commands.
- Do not investigate the repository beyond what is necessary to name affected workers and high-level scopes.
- Delegate all implementation, verification, review, and merge work to workers, reviewer, and merger through the JSON fields.

Worker scheduling constraint:
- Every entry in workers runs immediately and in parallel with every other worker, including carried-forward existing workers. There is no dependency scheduling between workers: natural-language phrasing like "after the other workers complete" inside a worker's title/prompt will NOT be enforced.
- Never create or update a worker whose job is to validate, test, or review the output of other workers in this plan. That work belongs in reviewer_prompt (runs only after all required workers complete) and merger_prompt (runs only after the reviewer).

Output contract:
- Return exactly one raw JSON object and nothing else.
- Do not wrap the JSON in Markdown fences.
- Do not include progress notes, tool transcripts, analysis, or prose outside the JSON object.
- The JSON object must contain workers and may contain reviewer_prompt, merger_prompt, and notes.

For existing affected workers, include their existing task_id. Omit unaffected workers. For new workers, omit task_id.

This is swarm generation %d. JSON schema:
	{"workers":[{"task_id":"existing-worker-task-id-if-applicable","title":"Backend worker","prompt":"Specific follow-up for this worker...","worker_kind":"backend","ownership":["internal/service"],"isolation":"worktree","write_scope":["internal/service"],"read_scope":["."],"required":true}],"reviewer_prompt":"Review only the changed/new worker outputs plus carried-forward completed workers...","merger_prompt":"Merge the updated accepted outputs into the parent final result...","notes":"Optional coordination summary."}`, parentPrompt, followup, generation)
}

func workerPrompt(parentPrompt string, worker PlannerWorker, worktreePath string) string {
	return fmt.Sprintf(`You are a worker in an OpenVibely swarm task.

Parent goal:
%s

Your objective:
%s

Ownership:
%s

Worktree:
%s

Stay inside your ownership unless the task is impossible without crossing it. If you cross ownership, say exactly what changed and why. Produce a concise summary, tests run, changed files, and any handoff notes for reviewer/merger.`, parentPrompt, worker.Prompt, strings.Join(worker.Ownership, ", "), worktreePath)
}

func reviewerPrompt(parentPrompt, prompt string) string {
	return fmt.Sprintf(`You are the reviewer for an OpenVibely swarm task.

Parent goal:
%s

Review instruction:
%s

Review the worker changes before merging. Check correctness, tests, conflicts, missing requirements, ownership violations, and cross-worker incompatibilities. Do not edit source files.

Return a structured report with accepted worker task ids, rejected or blocked worker task ids, required fixes, merge risks, and tests that should be run by the merger.`, parentPrompt, prompt)
}

func mergerPrompt(parentPrompt, prompt, worktree string) string {
	return fmt.Sprintf(`You are the merger for an OpenVibely swarm task.

Parent goal:
%s

Merge worktree:
%s

Merge instruction:
%s

Apply accepted worker changes into the parent merge worktree. Do not use a worker worktree as the final destination. If conflicts occur, stop and report the conflict. When merging succeeds, produce the final parent-facing summary, tests run, changed files, and final diff status.`, parentPrompt, worktree, prompt)
}

func AttachSwarmChildren(tasks []models.Task) []models.Task {
	childrenByParent := map[string][]models.Task{}
	out := make([]models.Task, 0, len(tasks))
	for _, task := range tasks {
		if models.IsSwarmChildRole(task.SwarmRole) && task.ParentTaskID != nil {
			childrenByParent[*task.ParentTaskID] = append(childrenByParent[*task.ParentTaskID], task)
			continue
		}
		out = append(out, task)
	}
	for i := range out {
		if out[i].SwarmRole == models.SwarmRoleParent {
			children := childrenByParent[out[i].ID]
			if len(children) == 0 {
				children = out[i].SwarmChildren
			}
			sort.SliceStable(children, func(a, b int) bool {
				if children[a].SwarmSequence != children[b].SwarmSequence {
					return children[a].SwarmSequence < children[b].SwarmSequence
				}
				return children[a].CreatedAt.Before(children[b].CreatedAt)
			})
			out[i].SwarmChildren = children
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
