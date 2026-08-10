package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
	repo        *repository.TaskGoalRepo
	taskRepo    *repository.TaskRepo
	broadcaster *events.Broadcaster
}

func NewTaskGoalService(repo *repository.TaskGoalRepo, taskRepo *repository.TaskRepo, broadcaster *events.Broadcaster) *TaskGoalService {
	return &TaskGoalService{repo: repo, taskRepo: taskRepo, broadcaster: broadcaster}
}

func (s *TaskGoalService) SetGoal(ctx context.Context, taskID string, objective string, opts GoalOptions) (*models.TaskGoal, error) {
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
		GoalID:    repository.NewID(),
		Objective: objective,
		Status:    models.TaskGoalStatusActive,
		Reason:    reason,
	}
	if err := s.repo.CreateOrReplace(ctx, goal); err != nil {
		return nil, err
	}
	s.publishGoalEvent(events.TaskGoalUpdated, goal)
	return goal, nil
}

func (s *TaskGoalService) GetGoal(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	return s.repo.GetByTaskID(ctx, taskID)
}

func (s *TaskGoalService) PublishEvaluatedGoal(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	goal, err := s.GetGoal(ctx, taskID)
	if err != nil || goal == nil || goal.Status == models.TaskGoalStatusCleared {
		return goal, err
	}
	s.publishGoalEvent(events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) ClearGoal(ctx context.Context, taskID string, actor string) error {
	reason := reasonForActor(actor, "cleared")
	if err := s.repo.Clear(ctx, taskID, reason); err != nil {
		return err
	}
	goal, _ := s.repo.GetByTaskID(ctx, taskID)
	if goal != nil {
		s.publishGoalEvent(events.TaskGoalCleared, goal)
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
	s.publishGoalEvent(events.TaskGoalResumed, updated)
	return updated, nil
}

func (s *TaskGoalService) pauseGoal(ctx context.Context, taskID string, reason string, activeOnly bool) error {
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
	s.publishGoalEvent(events.TaskGoalPaused, goal)
	return nil
}

func (s *TaskGoalService) ResumeGoal(ctx context.Context, taskID string, actor string) error {
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
	s.publishGoalEvent(events.TaskGoalResumed, goal)
	return nil
}

func (s *TaskGoalService) ReactivateAchievedGoal(ctx context.Context, taskID string, actor string) (*models.TaskGoal, error) {
	goal, err := s.repo.ReactivateAchieved(ctx, taskID, reasonForActor(actor, "reactivated for follow-up"))
	if err != nil {
		return nil, err
	}
	if goal != nil {
		s.publishGoalEvent(events.TaskGoalUpdated, goal)
	}
	return goal, nil
}

func (s *TaskGoalService) MarkAchieved(ctx context.Context, taskID string, goalID string, reason string) (*models.TaskGoal, error) {
	goal, err := s.repo.MarkAchieved(ctx, taskID, goalID, strings.TrimSpace(reason))
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) RecordBlockedReport(ctx context.Context, taskID string, goalID string, blockerKey string, reason string) (*models.TaskGoal, error) {
	blockerKey = strings.TrimSpace(blockerKey)
	if blockerKey == "" {
		return nil, errors.New("blocker_key is required")
	}
	goal, err := s.repo.RecordBlockedReport(ctx, taskID, goalID, blockerKey, strings.TrimSpace(reason))
	if err != nil {
		return nil, err
	}
	if goal == nil {
		return nil, ErrTaskGoalStaleUpdate
	}
	s.publishGoalEvent(events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) ClearBlockedReport(ctx context.Context, taskID string, blockerKey string, reason string) (*models.TaskGoal, error) {
	blockerKey = strings.TrimSpace(blockerKey)
	if blockerKey == "" {
		return nil, errors.New("blocker_key is required")
	}
	goal, err := s.repo.ClearBlockedReport(ctx, taskID, blockerKey, strings.TrimSpace(reason))
	if err != nil || goal == nil {
		return goal, err
	}
	s.publishGoalEvent(events.TaskGoalEvaluated, goal)
	return goal, nil
}

func (s *TaskGoalService) GetEvaluableGoal(ctx context.Context, taskID string) (*models.TaskGoal, error) {
	goal, err := s.repo.GetByTaskID(ctx, taskID)
	if err != nil || goal == nil || !goal.IsEvaluable() {
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

func (s *TaskGoalService) publishGoalEvent(eventType events.TaskEventType, goal *models.TaskGoal) {
	if s.broadcaster == nil || goal == nil {
		return
	}
	s.broadcaster.Publish(events.TaskEvent{
		Type:          eventType,
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
