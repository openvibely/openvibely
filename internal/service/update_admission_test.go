package service

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/update"
)

func TestUpdateDrainGatesSchedulerAutomationAndRecoveryPaths(t *testing.T) {
	tracker := update.NewWorkTracker()
	tracker.Close()
	scheduler := &SchedulerService{updateTracker: tracker}
	scheduler.checkDueTasks(context.Background())
	dispatcher := &AutomationDispatcher{updateTracker: tracker}
	if dispatched, err := dispatcher.DispatchOne(context.Background()); err != nil || dispatched {
		t.Fatalf("dispatcher = %v, %v", dispatched, err)
	}
	reconciler := &AutomationReconciler{updateTracker: tracker}
	if err := reconciler.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("reconciler = %v", err)
	}
	if got := tracker.Active(); got.Total() != 0 {
		t.Fatalf("active = %#v", got)
	}
}

func TestUpdateDrainLeavesWorkerTaskDurablyQueuedUntilAdmissionReopens(t *testing.T) {
	tracker := update.NewWorkTracker()
	tracker.Close()
	worker := NewWorkerService(nil, 1, nil)
	worker.SetUpdateWorkTracker(tracker)
	worker.SetAdmissionGate(tracker.Admit)
	worker.Start(context.Background())
	defer worker.Stop()
	task := models.Task{ID: "task-1", ProjectID: "project-1", Title: "queued", Category: models.CategoryActive, Status: models.StatusPending}
	worker.Submit(task)
	time.Sleep(10 * time.Millisecond)
	worker.mu.Lock()
	queued := len(worker.queue)
	pending := worker.pending[task.ID]
	worker.mu.Unlock()
	if queued != 1 || !pending || worker.TotalRunning() != 0 {
		t.Fatalf("queued=%d pending=%v running=%d", queued, pending, worker.TotalRunning())
	}
}
