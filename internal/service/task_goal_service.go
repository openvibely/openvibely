package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

const MaxTaskGoalLength = 2000

const GitHubPRPublicationBlockerKey = "github_pr_publication_automation_authorization"

var (
	ErrTaskGoalEmpty       = errors.New("task goal cannot be empty")
	ErrTaskGoalTooLong     = errors.New("task goal is too long")
	ErrTaskGoalNotFound    = errors.New("task goal not found")
	ErrTaskGoalNotPaused   = errors.New("task goal is not paused")
	ErrTaskGoalStaleUpdate = errors.New("task goal update did not match the active goal_id")
)

type GoalOptions struct {
	Actor  string
	Reason string
}

type TaskGoalService struct {
	access              *ProjectAccessService
	authOff             bool
	repo                persistence.TaskGoalStore
	taskRepo            persistence.TaskStore
	broadcaster         *events.Broadcaster
	goalAchievedHandler func(context.Context, string, string)
}

func NewTaskGoalService(repo persistence.TaskGoalStore, taskRepo persistence.TaskStore, broadcaster *events.Broadcaster) *TaskGoalService {
	return &TaskGoalService{repo: repo, taskRepo: taskRepo, broadcaster: broadcaster}
}

// SetGoalAchievedHandler registers the post-transition action for achieved goals.
func (s *TaskGoalService) SetGoalAchievedHandler(handler func(context.Context, string, string)) {
	s.goalAchievedHandler = handler
}

func (s *TaskGoalService) SetGoal(ctx context.Context, taskID string, objective string, opts GoalOptions) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, ErrTaskGoalEmpty
	}
	if len(objective) > MaxTaskGoalLength {
		return nil, ErrTaskGoalTooLong
	}
	if s.taskRepo != nil {
		task, err := s.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task == nil {
			return nil, fmt.Errorf("task not found: %s", taskID)
		}
	}
	reason := strings.TrimSpace(opts.Reason)
	if reason == "" && opts.Actor != "" {
		reason = fmt.Sprintf("set by %s", opts.Actor)
	}
	goal := &models.TaskGoal{
		TaskID:    taskID,
		GoalID:    persistence.NewID(),
		Objective: objective,
		Status:    models.TaskGoalStatusActive,
		Reason:    reason,
	}
	if err := s.repo.CreateOrReplace(ctx, goal); err != nil {
		return nil, err
	}
	s.publishGoalEvent(ctx, events.TaskGoalUpdated, goal)
	return goal, nil
}

func (s *TaskGoalService) GetGoal(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.View)
	if err := accessErr; err != nil {
		return nil, err
	}

	return s.repo.GetByTaskID(ctx, taskID)
}

func (s *TaskGoalService) PublishEvaluatedGoal(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	goal, err := s.GetGoal(ctx, taskID)
	if err != nil || goal == nil || goal.Status == models.TaskGoalStatusCleared {
		return goal, err
	}
	s.publishGoalEvent(ctx, events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) ClearGoal(ctx context.Context, taskID string, actor string) error {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return err
	}

	reason := reasonForActor(actor, "cleared")
	if err := s.repo.Clear(ctx, taskID, reason); err != nil {
		return err
	}
	goal, _ := s.repo.GetByTaskID(ctx, taskID)
	if goal != nil {
		s.publishGoalEvent(ctx, events.TaskGoalCleared, goal)
	}
	return nil
}

func (s *TaskGoalService) PauseGoal(ctx context.Context, taskID string, actor string) error {
	return s.pauseGoal(ctx, taskID, reasonForActor(actor, "paused"), false)
}

const TaskGoalStoppedByUserReason = "stopped by user"

func (s *TaskGoalService) PauseActiveGoalStoppedByUser(ctx context.Context, taskID string) error {
	return s.pauseGoal(ctx, taskID, TaskGoalStoppedByUserReason, true)
}

func (s *TaskGoalService) ResumeGoalStoppedByUser(ctx context.Context, taskID string, actor string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	goal, err := s.repo.GetByTaskID(ctx, taskID)
	if err != nil || goal == nil {
		return goal, err
	}
	if goal.Status != models.TaskGoalStatusPaused || strings.TrimSpace(goal.Reason) != TaskGoalStoppedByUserReason {
		return nil, nil
	}
	updated, err := s.repo.UpdateStatus(ctx, taskID, goal.GoalID, models.TaskGoalStatusActive, reasonForActor(actor, "resumed"), true)
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(ctx, events.TaskGoalResumed, updated)
	return updated, nil
}

func (s *TaskGoalService) pauseGoal(ctx context.Context, taskID string, reason string, activeOnly bool) error {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return err
	}

	goal, err := s.repo.GetByTaskID(ctx, taskID)
	if err != nil {
		return err
	}
	if goal == nil {
		return ErrTaskGoalNotFound
	}
	if activeOnly && goal.Status != models.TaskGoalStatusActive {
		return nil
	}
	goal, err = s.repo.UpdateStatus(ctx, taskID, goal.GoalID, models.TaskGoalStatusPaused, strings.TrimSpace(reason), false)
	if err != nil {
		return err
	}
	if goal == nil {
		return ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(ctx, events.TaskGoalPaused, goal)
	return nil
}

func (s *TaskGoalService) ResumeGoal(ctx context.Context, taskID string, actor string) error {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return err
	}

	goal, err := s.repo.GetByTaskID(ctx, taskID)
	if err != nil {
		return err
	}
	if goal == nil {
		return ErrTaskGoalNotFound
	}
	if goal.Status != models.TaskGoalStatusPaused && goal.Status != models.TaskGoalStatusBlocked {
		return ErrTaskGoalNotPaused
	}
	goal, err = s.repo.UpdateStatus(ctx, taskID, goal.GoalID, models.TaskGoalStatusActive, reasonForActor(actor, "resumed"), true)
	if err != nil {
		return err
	}
	if goal == nil {
		return ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(ctx, events.TaskGoalResumed, goal)
	return nil
}

func (s *TaskGoalService) ReactivateAchievedGoal(ctx context.Context, taskID string, actor string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	goal, err := s.repo.ReactivateAchieved(ctx, taskID, reasonForActor(actor, "reactivated for follow-up"))
	if err != nil {
		return nil, err
	}
	if goal != nil {
		s.publishGoalEvent(ctx, events.TaskGoalUpdated, goal)
	}
	return goal, nil
}

func (s *TaskGoalService) MarkAchieved(ctx context.Context, taskID string, goalID string, reason string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	if s.access != nil {
		saved, err := s.repo.GetByTaskID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if saved == nil {
			return nil, ErrTaskGoalNotFound
		}
		if err := s.authorizeGoalRun(ctx, saved); err != nil {
			return nil, err
		}
	}

	goal, err := s.repo.MarkAchieved(ctx, taskID, goalID, strings.TrimSpace(reason))
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(ctx, events.TaskGoalEvaluated, goal)
	if s.goalAchievedHandler != nil {
		s.goalAchievedHandler(ctx, taskID, goal.GoalID)
	}
	return goal, nil
}

func (s *TaskGoalService) RecordBlockedReport(ctx context.Context, taskID string, goalID string, blockerKey string, reason string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	blockerKey = strings.TrimSpace(blockerKey)
	if blockerKey == "" {
		return nil, errors.New("blocker_key is required")
	}
	if s.access != nil {
		saved, err := s.repo.GetByTaskID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if saved == nil {
			return nil, ErrTaskGoalNotFound
		}
		if err := s.authorizeGoalRun(ctx, saved); err != nil {
			return nil, err
		}
	}

	goal, err := s.repo.RecordBlockedReport(ctx, taskID, goalID, blockerKey, strings.TrimSpace(reason))
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(ctx, events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) ClearBlockedReport(ctx context.Context, taskID string, blockerKey string, reason string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	blockerKey = strings.TrimSpace(blockerKey)
	if blockerKey == "" {
		return nil, errors.New("blocker_key is required")
	}
	if s.access != nil {
		saved, err := s.repo.GetByTaskID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if saved == nil {
			return nil, ErrTaskGoalNotFound
		}
		if err := s.authorizeGoalRun(ctx, saved); err != nil {
			return nil, err
		}
	}

	goal, err := s.repo.ClearBlockedReport(ctx, taskID, blockerKey, strings.TrimSpace(reason))
	if err != nil || goal == nil {
		return goal, err
	}
	s.publishGoalEvent(ctx, events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) GetEvaluableGoal(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	var accessErr error
	ctx, accessErr = s.authorizeGoal(ctx, taskID, authz.Edit)
	if err := accessErr; err != nil {
		return nil, err
	}

	goal, err := s.repo.GetByTaskID(ctx, taskID)
	if err != nil || goal == nil || !goal.IsEvaluable() {
		return nil, err
	}
	if err := s.authorizeGoalRun(ctx, goal); err != nil {
		return nil, err
	}

	return goal, nil
}

func reasonForActor(actor, action string) string {
	actor = strings.TrimSpace(actor)
	if actor == "" {
		return action
	}
	return fmt.Sprintf("%s by %s", action, actor)
}

func (s *TaskGoalService) publishGoalEvent(ctx context.Context, eventType events.TaskEventType, goal *models.TaskGoal) {
	if s.broadcaster == nil || goal == nil {
		return
	}
	// Scoped delivery and the all-projects filter both need the project.
	projectID := ""
	if s.taskRepo != nil {
		if task, err := s.taskRepo.GetByID(context.WithoutCancel(ctx), goal.TaskID); err == nil && task != nil {
			projectID = task.ProjectID
		}
	}
	s.broadcaster.Publish(events.TaskEvent{
		Type:          eventType,
		ProjectID:     projectID,
		TaskID:        goal.TaskID,
		Status:        string(goal.Status),
		GoalID:        goal.GoalID,
		GoalStatus:    string(goal.Status),
		GoalObjective: goal.Objective,
		GoalReason:    goal.Reason,
		BlockerKey:    goal.BlockerKey,
		BlockerCount:  goal.BlockerCount,
	})
}

// SetProjectAuthorization installs current-membership checks for every caller,
// including service-owned tools and lifecycle evaluation.
func (s *TaskGoalService) SetProjectAuthorization(access *ProjectAccessService, authOff bool) {
	s.access, s.authOff = access, authOff
}

func (s *TaskGoalService) authorizeGoal(ctx context.Context, taskID string, permission authz.Permission) (context.Context, error) {
	if s.access == nil {
		return ctx, nil
	}
	if _, ok := models.PrincipalFromContext(ctx); !ok {
		if !s.authOff {
			return ctx, authz.ErrDenied
		}
		ctx = models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	}
	if s.taskRepo == nil {
		return ctx, authz.ErrDenied
	}
	task, err := s.taskRepo.GetByID(ctx, taskID)
	if err != nil {
		return ctx, err
	}
	if task == nil {
		return ctx, authz.ErrDenied
	}
	return ctx, s.access.Require(ctx, task.ProjectID, permission)
}

// authorizeGoalRun checks the durable goal identity separately from the current
// caller. A later task submitter cannot lend authority to a revoked goal creator.
func (s *TaskGoalService) authorizeGoalRun(ctx context.Context, goal *models.TaskGoal) error {
	if s.access == nil {
		return nil
	}
	userID := goal.RunAsUserID
	if userID == "" && s.authOff {
		userID = models.BootstrapUserID
	}
	if userID == "" {
		return authz.ErrDenied
	}
	task, err := s.taskRepo.GetByID(ctx, goal.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return authz.ErrDenied
	}
	permission := authz.Edit
	if task.AutoMergeOnGoalAchieved {
		permission = authz.Manage
	}
	// The active caller also needs merge authority before an achievement callback.
	if err := s.access.Require(ctx, task.ProjectID, permission); err != nil {
		return err
	}
	return s.access.Require(models.WithPrincipal(ctx, models.Principal{UserID: userID}), task.ProjectID, permission)
}
