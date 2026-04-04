package service

import (
	"context"
	"fmt"
	"log"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type AlertService struct {
	alertRepo   *repository.AlertRepo
	broadcaster *events.Broadcaster
}

func NewAlertService(alertRepo *repository.AlertRepo, broadcaster *events.Broadcaster) *AlertService {
	return &AlertService{
		alertRepo:   alertRepo,
		broadcaster: broadcaster,
	}
}

func (s *AlertService) Create(ctx context.Context, a *models.Alert) error {
	if err := s.alertRepo.Create(ctx, a); err != nil {
		return fmt.Errorf("creating alert: %w", err)
	}
	log.Printf("[alert-svc] created alert id=%s type=%s severity=%s title=%q", a.ID, a.Type, a.Severity, a.Title)

	// Publish event for real-time UI updates
	if s.broadcaster != nil {
		s.broadcaster.Publish(events.TaskEvent{
			Type:      events.AlertCreated,
			ProjectID: a.ProjectID,
			AlertID:   a.ID,
		})
		log.Printf("[alert-svc] published alert_created event for alert=%s project=%s", a.ID, a.ProjectID)
	}

	return nil
}

func (s *AlertService) GetByID(ctx context.Context, id string) (*models.Alert, error) {
	return s.alertRepo.GetByID(ctx, id)
}

func (s *AlertService) CreateTaskFailedAlert(ctx context.Context, projectID, taskID, executionID, taskTitle, errMsg string) error {
	a := &models.Alert{
		ProjectID:   projectID,
		TaskID:      &taskID,
		ExecutionID: &executionID,
		Type:        models.AlertTaskFailed,
		Severity:    models.SeverityError,
		Title:       fmt.Sprintf("Task failed: %s", taskTitle),
		Message:     errMsg,
	}
	return s.Create(ctx, a)
}

func (s *AlertService) CreateTaskNeedsFollowupAlert(ctx context.Context, projectID, taskID, executionID, taskTitle, reason string) error {
	a := &models.Alert{
		ProjectID:   projectID,
		TaskID:      &taskID,
		ExecutionID: &executionID,
		Type:        models.AlertTaskNeedsFollowup,
		Severity:    models.SeverityWarning,
		Title:       fmt.Sprintf("Follow-up needed: %s", taskTitle),
		Message:     reason,
	}
	return s.Create(ctx, a)
}

func (s *AlertService) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Alert, error) {
	return s.alertRepo.ListByProject(ctx, projectID, limit)
}

func (s *AlertService) CountUnread(ctx context.Context, projectID string) (int, error) {
	return s.alertRepo.CountUnread(ctx, projectID)
}

func (s *AlertService) MarkRead(ctx context.Context, id string) error {
	return s.alertRepo.MarkRead(ctx, id)
}

func (s *AlertService) MarkAllRead(ctx context.Context, projectID string) error {
	return s.alertRepo.MarkAllRead(ctx, projectID)
}

func (s *AlertService) Delete(ctx context.Context, id string) error {
	return s.alertRepo.Delete(ctx, id)
}

func (s *AlertService) DeleteAll(ctx context.Context, projectID string) error {
	if err := s.alertRepo.DeleteAll(ctx, projectID); err != nil {
		return fmt.Errorf("deleting all alerts: %w", err)
	}
	log.Printf("[alert-svc] deleted all alerts for project=%s", projectID)

	// Publish event for real-time UI updates
	if s.broadcaster != nil {
		s.broadcaster.Publish(events.TaskEvent{
			Type:      events.AlertCreated, // Reuse AlertCreated to trigger badge refresh
			ProjectID: projectID,
		})
		log.Printf("[alert-svc] published alert event for project=%s after delete all", projectID)
	}

	return nil
}
