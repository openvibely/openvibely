package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type automationTaskMutationService interface {
	Create(context.Context, *models.Task) error
	Update(context.Context, *models.Task) error
}

type AutomationCompiler struct {
	automationRepo *repository.AutomationRepo
	taskSvc        automationTaskMutationService
	taskRepo       *repository.TaskRepo
	scheduleRepo   *repository.ScheduleRepo
	planner        *AutomationPublicationPlanner
	now            func() time.Time
}

type AutomationPublishRequest struct {
	ProjectID    string
	AutomationID string
	VersionID    string
	PlanRevision string
}

type AutomationPublishResult struct {
	Definition *models.AutomationDefinition        `json:"definition"`
	Attempt    models.AutomationPublicationAttempt `json:"attempt"`
	Resources  []models.AutomationPublicationStep  `json:"resources"`
}

func NewAutomationCompiler(automationRepo *repository.AutomationRepo, taskSvc automationTaskMutationService, taskRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo, planner *AutomationPublicationPlanner) *AutomationCompiler {
	return &AutomationCompiler{automationRepo: automationRepo, taskSvc: taskSvc, taskRepo: taskRepo, scheduleRepo: scheduleRepo, planner: planner, now: time.Now}
}

func (c *AutomationCompiler) Publish(ctx context.Context, request AutomationPublishRequest) (*AutomationPublishResult, error) {
	if c == nil || c.automationRepo == nil || c.planner == nil || c.taskSvc == nil || c.taskRepo == nil || c.scheduleRepo == nil {
		return nil, errors.New("automation compiler is unavailable")
	}
	if existing, err := c.automationRepo.GetPublicationAttempt(ctx, request.ProjectID, request.AutomationID, request.VersionID, request.PlanRevision); err != nil {
		return nil, err
	} else if existing != nil && existing.Attempt.Status == "completed" {
		definition, err := c.automationRepo.GetDefinitionVersion(ctx, request.ProjectID, request.AutomationID, request.VersionID)
		if err != nil {
			return nil, err
		}
		return publishResult(definition, existing), nil
	}
	plan, err := c.planner.Plan(ctx, request.ProjectID, request.AutomationID, request.VersionID)
	if err != nil {
		return nil, err
	}
	if len(plan.Validation) > 0 {
		return nil, fmt.Errorf("automation draft is invalid: %s", plan.Validation[0].Message)
	}
	if request.PlanRevision == "" || request.PlanRevision != plan.PlanRevision {
		return nil, errors.New("stale publication plan; preview the publication again")
	}
	snapshot, err := c.automationRepo.ReservePublicationAttempt(ctx, request.ProjectID, request.AutomationID, request.VersionID, request.PlanRevision, plan.Effects)
	if err != nil {
		return nil, err
	}
	claimOwner := repository.NewID()
	if err := c.automationRepo.AcquirePublicationAttempt(ctx, snapshot.Attempt.ID, claimOwner, c.now().UTC(), 2*time.Minute); err != nil {
		return nil, err
	}
	defer func() {
		_ = c.automationRepo.ReleasePublicationAttempt(context.Background(), snapshot.Attempt.ID, claimOwner)
	}()
	metadata, err := c.automationRepo.GetAutomationDraftMetadata(ctx, request.ProjectID, request.AutomationID, request.VersionID)
	if err != nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
	}
	if metadata == nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, errors.New("automation draft metadata not found"))
	}
	candidate, err := metadata.Candidate()
	if err != nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
	}
	definition, err := c.automationRepo.GetDefinitionVersion(ctx, request.ProjectID, request.AutomationID, request.VersionID)
	if err != nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
	}
	if definition == nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, errors.New("automation draft not found"))
	}
	effects := publicationEffectMap(plan.Effects)
	resources := make(map[string]string)
	for _, step := range snapshot.Steps {
		if step.ResourceID != "" {
			resources[step.StepKey] = step.ResourceID
		}
	}
	for _, step := range snapshot.Steps {
		if step.Status == "completed" {
			continue
		}
		effect, ok := effects[step.StepKey]
		if !ok {
			err := fmt.Errorf("publication step %q is absent from the confirmed plan", step.StepKey)
			return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
		}
		if err := c.automationRepo.MarkPublicationStep(ctx, snapshot.Attempt.ID, step.StepKey, "running", step.ResourceID, ""); err != nil {
			return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
		}
		if effect.ResourceID == "" {
			effect.ResourceID = step.ResourceID
		}
		resourceID, compileErr := c.compileEffect(ctx, snapshot.Attempt.ID, request, definition, candidate, effect, resources)
		if compileErr != nil {
			ambiguousResourceID := step.ResourceID
			if ambiguousResourceID == "" && effect.ResourceType == "task" {
				nodeKey := strings.TrimPrefix(effect.TargetKey, "task:")
				if reconciled, reconcileErr := c.automationRepo.FindCompilerTask(ctx, request.ProjectID, request.AutomationID, nodeKey); reconcileErr == nil && reconciled != nil {
					ambiguousResourceID = reconciled.ID
				}
			}
			_ = c.automationRepo.MarkPublicationStep(ctx, snapshot.Attempt.ID, step.StepKey, "ambiguous", ambiguousResourceID, compileErr.Error())
			return c.failPublication(ctx, request, snapshot.Attempt.ID, compileErr)
		}
		completedStatus := "completed"
		if effect.ResourceType == "task" && effect.Operation == "update" {
			completedStatus = "running"
		}
		if err := c.automationRepo.MarkPublicationStep(ctx, snapshot.Attempt.ID, step.StepKey, completedStatus, resourceID, ""); err != nil {
			return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
		}
		resources[step.StepKey] = resourceID
	}
	taskUpdates, err := publicationTaskUpdates(definition, candidate, effects, resources)
	if err != nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
	}
	published, err := c.automationRepo.PublishDraftVersion(ctx, snapshot.Attempt.ID, taskUpdates)
	if err != nil {
		return c.failPublication(ctx, request, snapshot.Attempt.ID, err)
	}
	completed, err := c.automationRepo.GetPublicationAttempt(ctx, request.ProjectID, request.AutomationID, request.VersionID, request.PlanRevision)
	if err != nil {
		return nil, err
	}
	automationobs.Event("automation.publication.completed",
		automationobs.String("project_id", request.ProjectID), automationobs.String("automation_id", request.AutomationID),
		automationobs.String("version_id", request.VersionID), automationobs.String("attempt_id", snapshot.Attempt.ID))
	return publishResult(published, completed), nil
}

func (c *AutomationCompiler) failPublication(ctx context.Context, request AutomationPublishRequest, attemptID string, cause error) (*AutomationPublishResult, error) {
	_ = c.automationRepo.MarkPublicationAttemptFailed(ctx, attemptID, cause)
	automationobs.Event("automation.publication.failed",
		automationobs.String("project_id", request.ProjectID), automationobs.String("automation_id", request.AutomationID),
		automationobs.String("version_id", request.VersionID), automationobs.String("attempt_id", attemptID))
	snapshot, snapshotErr := c.automationRepo.GetPublicationAttempt(ctx, request.ProjectID, request.AutomationID, request.VersionID, request.PlanRevision)
	if snapshotErr != nil {
		return nil, cause
	}
	definition, _ := c.automationRepo.GetDefinitionVersion(ctx, request.ProjectID, request.AutomationID, request.VersionID)
	return publishResult(definition, snapshot), cause
}

func (c *AutomationCompiler) compileEffect(ctx context.Context, attemptID string, request AutomationPublishRequest, definition *models.AutomationDefinition, candidate models.AutomationDraftCandidate, effect models.AutomationPublicationEffect, resources map[string]string) (string, error) {
	nodeKey := strings.TrimPrefix(effect.TargetKey, effect.ResourceType+":")
	node, ok := findDraftNode(candidate, nodeKey)
	if !ok {
		return "", fmt.Errorf("publication target %q has no draft node", effect.TargetKey)
	}
	switch effect.ResourceType {
	case "task":
		return c.compileTask(ctx, request, definition, node, effect)
	case "schedule":
		return c.compileSchedule(ctx, attemptID, request, node, effect, resources)
	default:
		return effect.ResourceID, nil
	}
}

func (c *AutomationCompiler) compileTask(ctx context.Context, request AutomationPublishRequest, definition *models.AutomationDefinition, node models.AutomationDraftNode, effect models.AutomationPublicationEffect) (string, error) {
	task := (*models.Task)(nil)
	var err error
	if effect.ResourceID != "" {
		task, err = c.taskRepo.GetByID(ctx, effect.ResourceID)
		if err != nil {
			return "", err
		}
	}
	if task == nil {
		task, err = c.automationRepo.FindCompilerTask(ctx, request.ProjectID, request.AutomationID, node.Key)
		if err != nil {
			return "", err
		}
	}
	prompt, _ := node.Config["prompt"].(string)
	category, _ := node.Config["category"].(string)
	priority, _ := draftInt(node.Config["priority"])
	if task == nil {
		task = &models.Task{ProjectID: request.ProjectID, Title: automationTaskTitle(definition.Automation, node), Prompt: prompt,
			Category: models.TaskCategory(category), Priority: priority, Status: models.StatusPending,
			CreatedVia: repository.AutomationCompilerTaskCreatedVia(request.AutomationID, node.Key)}
		if err := c.taskSvc.Create(ctx, task); err != nil {
			if errors.Is(err, ErrDuplicateTask) {
				return "", errors.New("automation task title conflicts with existing visible work")
			}
			return "", err
		}
		return task.ID, nil
	}
	if task.ProjectID != request.ProjectID || task.CreatedVia != repository.AutomationCompilerTaskCreatedVia(request.AutomationID, node.Key) && effect.Operation == "create" {
		return "", errors.New("publication task reconciliation found unrelated work")
	}
	if effect.Operation == "update" {
		// Existing task behavior belongs to the active published version until the
		// replacement version is durable. The desired fields are applied inside
		// PublishDraftVersion's transaction after every external resource step has
		// completed, so a failed or crashed attempt cannot partially switch behavior.
		return task.ID, nil
	}
	return task.ID, nil
}

func publicationTaskUpdates(definition *models.AutomationDefinition, candidate models.AutomationDraftCandidate, effects map[string]models.AutomationPublicationEffect, resources map[string]string) ([]repository.AutomationPublicationTaskUpdate, error) {
	var updates []repository.AutomationPublicationTaskUpdate
	for _, node := range candidate.Nodes {
		stepKey := "task:" + node.Key
		effect, ok := effects[stepKey]
		if !ok || effect.ResourceType != "task" || effect.Operation != "update" {
			continue
		}
		taskID := resources[stepKey]
		if taskID == "" {
			return nil, fmt.Errorf("publication task update %q has no reconciled resource", stepKey)
		}
		prompt, _ := node.Config["prompt"].(string)
		category, _ := node.Config["category"].(string)
		priority, _ := draftInt(node.Config["priority"])
		updates = append(updates, repository.AutomationPublicationTaskUpdate{
			StepKey: stepKey, TaskID: taskID, Title: automationTaskTitle(definition.Automation, node),
			Prompt: prompt, Category: models.TaskCategory(category), Priority: priority,
		})
	}
	return updates, nil
}

func (c *AutomationCompiler) compileSchedule(ctx context.Context, attemptID string, request AutomationPublishRequest, node models.AutomationDraftNode, effect models.AutomationPublicationEffect, resources map[string]string) (string, error) {
	if effect.Operation == "reuse" && effect.ResourceID != "" {
		return effect.ResourceID, nil
	}
	targetKey, _ := node.Config["target_node_key"].(string)
	taskID := resources["task:"+targetKey]
	if taskID == "" {
		return "", fmt.Errorf("trigger node %q has no compiled task target", node.Key)
	}
	desired, err := c.scheduleFromNode(taskID, node)
	if err != nil {
		return "", err
	}
	// A new trigger remains disabled until PublishDraftVersion atomically claims
	// ownership and publishes the immutable version. This prevents an ordinary
	// scheduler poll from firing an unpublished Automation trigger.
	desired.Enabled = false
	if effect.ResourceID != "" {
		existing, getErr := c.scheduleRepo.GetByID(ctx, effect.ResourceID)
		if getErr != nil {
			return "", getErr
		}
		if existing == nil || !schedulesMatchConfiguration(*existing, desired) {
			return "", fmt.Errorf("publication schedule reconciliation found unrelated work")
		}
		return existing.ID, nil
	}
	if err := c.scheduleRepo.CreateForAutomationPublication(ctx, &desired, attemptID, effect.StepKey); err != nil {
		return "", err
	}
	return desired.ID, nil
}

func (c *AutomationCompiler) scheduleFromNode(taskID string, node models.AutomationDraftNode) (models.Schedule, error) {
	runAtText, _ := node.Config["run_at"].(string)
	clock, err := time.ParseInLocation("15:04", runAtText, time.Local)
	if err != nil {
		return models.Schedule{}, fmt.Errorf("invalid trigger time for %q", node.Key)
	}
	now := c.now().In(time.Local)
	runAt := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, time.Local)
	if !runAt.After(now) {
		runAt = runAt.AddDate(0, 0, 1)
	}
	repeat, _ := node.Config["repeat_type"].(string)
	interval, _ := draftInt(node.Config["repeat_interval"])
	enabled, _ := node.Config["enabled"].(bool)
	return models.Schedule{TaskID: taskID, RunAt: runAt.UTC(), RepeatType: models.RepeatType(repeat), RepeatInterval: interval, Enabled: enabled}, nil
}

func schedulesMatchConfiguration(existing, desired models.Schedule) bool {
	return existing.TaskID == desired.TaskID && existing.RunAt.Format("15:04") == desired.RunAt.Format("15:04") &&
		existing.RepeatType == desired.RepeatType && existing.RepeatInterval == desired.RepeatInterval && existing.Enabled == desired.Enabled
}

func publishResult(definition *models.AutomationDefinition, snapshot *repository.AutomationPublicationSnapshot) *AutomationPublishResult {
	result := &AutomationPublishResult{Definition: definition}
	if snapshot == nil {
		return result
	}
	result.Attempt = snapshot.Attempt
	for _, step := range snapshot.Steps {
		if step.ResourceID != "" && step.Operation != "disable" {
			result.Resources = append(result.Resources, step)
		}
	}
	return result
}

type AutomationLifecycleService struct {
	repo         *repository.AutomationRepo
	scheduleRepo *repository.ScheduleRepo
}

func NewAutomationLifecycleService(repo *repository.AutomationRepo, scheduleRepo *repository.ScheduleRepo) *AutomationLifecycleService {
	return &AutomationLifecycleService{repo: repo, scheduleRepo: scheduleRepo}
}

func (s *AutomationLifecycleService) Pause(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.SetAutomationLifecycle(ctx, projectID, automationID, models.AutomationPaused)
}

func (s *AutomationLifecycleService) Resume(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.SetAutomationLifecycle(ctx, projectID, automationID, models.AutomationActive)
}

func (s *AutomationLifecycleService) Archive(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.SetAutomationLifecycle(ctx, projectID, automationID, models.AutomationArchived)
}

func (s *AutomationLifecycleService) Delete(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.DeleteAutomation(ctx, projectID, automationID)
}
