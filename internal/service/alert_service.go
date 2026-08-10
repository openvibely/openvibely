package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type AlertService struct {
	alertRepo   *repository.AlertRepo
	broadcaster *events.Broadcaster
}

func NewAlertService(alertRepo *repository.AlertRepo, broadcaster *events.Broadcaster) *AlertService {
	return &AlertService{alertRepo: alertRepo, broadcaster: broadcaster}
}

func (s *AlertService) publish(a *models.Alert) {
	if s.broadcaster == nil || a == nil {
		return
	}
	s.broadcaster.Publish(events.TaskEvent{Type: events.AlertCreated, ProjectID: a.ProjectID, AlertID: a.ID})
}

func (s *AlertService) publishProjectAlert(projectID, alertID string) {
	s.publish(&models.Alert{ProjectID: projectID, ID: alertID})
}

func (s *AlertService) Create(ctx context.Context, a *models.Alert) error {
	if err := s.alertRepo.Create(ctx, a); err != nil {
		return fmt.Errorf("creating alert: %w", err)
	}
	applog.Infof("[alert-svc] created alert id=%s project=%s type=%s", a.ID, a.ProjectID, a.Type)
	s.publish(a)
	return nil
}

func (s *AlertService) CreateActionable(ctx context.Context, a *models.Alert) (*models.Alert, error) {
	if a == nil {
		return nil, fmt.Errorf("alert is required")
	}
	a.Scope = models.AlertScopeProject
	a.DecisionState = models.AlertDecisionPending
	a.ProcessingState = models.AlertProcessingUnclaimed
	if strings.TrimSpace(a.Source) == "" {
		a.Source = "agent"
	}
	if automationContext, ok := AutomationContextFromContext(ctx); ok && automationContext.ProjectID == a.ProjectID {
		a.AutomationContext = &automationContext
		if _, executionID, executionOK := AutomationExecutionFromContext(ctx); executionOK {
			a.ExecutionID = &executionID
		}
	}
	created, err := s.alertRepo.CreateIdempotent(ctx, a)
	if err != nil {
		return nil, err
	}
	*a = *created
	s.publish(created)
	return created, nil
}

func (s *AlertService) GetByID(ctx context.Context, projectID, id string) (*models.Alert, error) {
	return s.alertRepo.GetByIDForProject(ctx, projectID, id)
}

// GetByIDAdmin is explicit unscoped administrative visibility. Runtime and HTTP paths must not call it.
func (s *AlertService) GetByIDAdmin(ctx context.Context, id string) (*models.Alert, error) {
	return s.alertRepo.GetByIDAdmin(ctx, id)
}

func (s *AlertService) CreateTaskFailedAlert(ctx context.Context, projectID, taskID, executionID, taskTitle, errMsg string) error {
	a := &models.Alert{ProjectID: projectID, TaskID: &taskID, ExecutionID: &executionID, SourceTaskID: &taskID,
		Type: models.AlertTaskFailed, Severity: models.SeverityError, Title: fmt.Sprintf("Task failed: %s", taskTitle),
		Message: errMsg, Body: errMsg, Source: "task_execution"}
	return s.Create(ctx, a)
}

func (s *AlertService) CreateTaskNeedsFollowupAlert(ctx context.Context, projectID, taskID, executionID, taskTitle, reason string) error {
	a := &models.Alert{ProjectID: projectID, TaskID: &taskID, ExecutionID: &executionID, SourceTaskID: &taskID,
		Type: models.AlertTaskNeedsFollowup, Severity: models.SeverityWarning, Title: fmt.Sprintf("Follow-up needed: %s", taskTitle),
		Message: reason, Body: reason, Source: "task_execution"}
	return s.Create(ctx, a)
}

func (s *AlertService) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Alert, error) {
	return s.alertRepo.ListByProject(ctx, projectID, limit)
}

func (s *AlertService) ListSummariesByProject(ctx context.Context, projectID string, limit int) ([]models.AlertSummary, error) {
	return s.alertRepo.ListSummariesByProject(ctx, projectID, limit)
}

func (s *AlertService) ListFiltered(ctx context.Context, projectID string, filter models.AlertListFilter) ([]models.Alert, error) {
	return s.alertRepo.ListFiltered(ctx, projectID, filter)
}

func (s *AlertService) ListFilteredSummariesForRuntime(ctx context.Context, projectID string, filter models.AlertListFilter) ([]models.AlertSummary, error) {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if !automationBound {
		return s.alertRepo.ListFilteredSummaries(ctx, projectID, filter)
	}
	if automationContext.ProjectID != projectID {
		return nil, fmt.Errorf("alert Automation project mismatch")
	}
	bindings, err := s.alertRepo.NativeInboxBindings(ctx, automationContext)
	if err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return s.alertRepo.ListFilteredSummaries(ctx, projectID, filter)
	}
	filter.AutomationInboxBindings = bindings
	return s.alertRepo.ListFilteredSummaries(ctx, projectID, filter)
}

func (s *AlertService) RequireAutomationInboxOwnership(ctx context.Context, projectID, alertID string) error {
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if !automationBound {
		return nil
	}
	if automationContext.ProjectID != projectID {
		return fmt.Errorf("alert Automation project mismatch")
	}
	bindings, err := s.alertRepo.NativeInboxBindings(ctx, automationContext)
	if err != nil {
		return err
	}
	owned, err := s.alertRepo.AlertOwnedByAutomationInbox(ctx, projectID, alertID, bindings)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("notification is not owned by this Automation inbox")
	}
	if err := s.alertRepo.RebindAlertToAutomationInbox(ctx, projectID, alertID, bindings); err != nil {
		return fmt.Errorf("projecting notification onto the current Automation graph: %w", err)
	}
	return nil
}

func (s *AlertService) CountUnread(ctx context.Context, projectID string) (int, error) {
	return s.alertRepo.CountUnread(ctx, projectID)
}

func (s *AlertService) MarkRead(ctx context.Context, projectID, id string) error {
	if err := s.alertRepo.MarkRead(ctx, projectID, id); err != nil {
		return err
	}
	s.publishProjectAlert(projectID, id)
	return nil
}

func (s *AlertService) MarkAllRead(ctx context.Context, projectID string) error {
	if err := s.alertRepo.MarkAllRead(ctx, projectID); err != nil {
		return err
	}
	s.publishProjectAlert(projectID, "")
	return nil
}

func (s *AlertService) Delete(ctx context.Context, projectID, id string) error {
	if err := s.alertRepo.Delete(ctx, projectID, id); err != nil {
		return err
	}
	s.publishProjectAlert(projectID, id)
	return nil
}

func (s *AlertService) DeleteAll(ctx context.Context, projectID string) error {
	if err := s.alertRepo.DeleteAll(ctx, projectID); err != nil {
		return fmt.Errorf("deleting all alerts: %w", err)
	}
	if s.broadcaster != nil {
		s.broadcaster.Publish(events.TaskEvent{Type: events.AlertCreated, ProjectID: projectID})
	}
	return nil
}

func (s *AlertService) SetDecision(ctx context.Context, projectID, id string, state models.AlertDecisionState) error {
	if err := s.alertRepo.SetDecision(ctx, projectID, id, state); err != nil {
		return err
	}
	a, _ := s.GetByID(ctx, projectID, id)
	s.publish(a)
	return nil
}

func (s *AlertService) ClaimApproved(ctx context.Context, projectID, id, claimant string, lease time.Duration) (*models.Alert, error) {
	a, err := s.alertRepo.ClaimApproved(ctx, projectID, id, claimant, lease)
	if err != nil {
		return nil, err
	}
	s.publish(a)
	return a, nil
}

func (s *AlertService) ReleaseClaim(ctx context.Context, projectID, id, claimant string) error {
	if err := s.alertRepo.ReleaseClaim(ctx, projectID, id, claimant); err != nil {
		return err
	}
	s.publishProjectAlert(projectID, id)
	return nil
}

func (s *AlertService) LinkImplementationTask(ctx context.Context, projectID, id, claimant, taskID string) error {
	if err := s.alertRepo.LinkImplementationTask(ctx, projectID, id, claimant, taskID); err != nil {
		return err
	}
	s.publishProjectAlert(projectID, id)
	return nil
}

func (s *AlertService) CreateImplementationTask(ctx context.Context, projectID, id, claimant string, input models.AlertImplementationTaskInput) (*models.Task, error) {
	task, err := s.alertRepo.CreateImplementationTask(ctx, projectID, id, claimant, input)
	if err != nil {
		return nil, err
	}
	s.publishProjectAlert(projectID, id)
	return task, nil
}

func (s *AlertService) MarkProcessing(ctx context.Context, projectID, id, claimant string, state models.AlertProcessingState, message string) error {
	if err := s.alertRepo.MarkProcessing(ctx, projectID, id, claimant, state, message); err != nil {
		return err
	}
	s.publishProjectAlert(projectID, id)
	return nil
}
