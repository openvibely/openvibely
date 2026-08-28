package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAlertService_SetDecisionPublishesInvalidationWithoutReloadingAlert(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	alertRepo := repository.NewAlertRepo(db)
	broadcaster := events.NewBroadcaster()
	alertSvc := NewAlertService(alertRepo, broadcaster)

	project := &models.Project{Name: "Decision Event Race Test"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	alert, err := alertSvc.CreateActionable(ctx, &models.Alert{
		ProjectID: project.ID,
		Type:      models.AlertCustom,
		Title:     "Decision race",
		Severity:  models.SeverityInfo,
	})
	if err != nil {
		t.Fatalf("create actionable alert: %v", err)
	}

	sub, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer broadcaster.Unsubscribe(sub)

	if err := alertSvc.SetDecision(ctx, project.ID, alert.ID, models.AlertDecisionApproved); err != nil {
		t.Fatalf("set decision: %v", err)
	}
	if err := alertRepo.Delete(ctx, project.ID, alert.ID); err != nil {
		t.Fatalf("delete alert after decision: %v", err)
	}

	select {
	case event := <-sub:
		if event.Type != events.AlertCreated || event.ProjectID != project.ID || event.AlertID != alert.ID {
			t.Fatalf("unexpected decision invalidation event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for decision invalidation event")
	}
}
