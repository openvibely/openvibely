package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/update"
)

// AutomationDispatcher is a durable adapter into WorkerService. It owns no
// worker pool, task executor, lifecycle runner, or model invocation path.
type AutomationDispatcher struct {
	automationRepo *repository.AutomationRepo
	taskRepo       *repository.TaskRepo
	workerSvc      *WorkerService
	claimant       string
	interval       time.Duration
	lease          time.Duration
	cancel         context.CancelFunc
	wg             sync.WaitGroup
	updateTracker  *update.WorkTracker
}

func NewAutomationDispatcher(automationRepo *repository.AutomationRepo, taskRepo *repository.TaskRepo, workerSvc *WorkerService) *AutomationDispatcher {
	return &AutomationDispatcher{
		automationRepo: automationRepo,
		taskRepo:       taskRepo,
		workerSvc:      workerSvc,
		claimant:       "automation-dispatcher-" + repository.NewID(),
		interval:       time.Second,
		lease:          time.Minute,
	}
}

func (d *AutomationDispatcher) SetUpdateWorkTracker(tracker *update.WorkTracker) {
	d.updateTracker = tracker
}

func (d *AutomationDispatcher) Start(ctx context.Context) {
	if d == nil || d.automationRepo == nil || d.taskRepo == nil || d.workerSvc == nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.run(runCtx)
	}()
}

func (d *AutomationDispatcher) Stop() {
	if d == nil {
		return
	}
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
}

func (d *AutomationDispatcher) run(ctx context.Context) {
	d.drain(ctx)
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.drain(ctx)
		}
	}
}

func (d *AutomationDispatcher) drain(ctx context.Context) {
	for {
		dispatched, err := d.DispatchOne(ctx)
		if err != nil {
			applog.Infof("[automation-dispatcher] dispatch failed: %v", err)
			return
		}
		if !dispatched {
			return
		}
	}
}

func (d *AutomationDispatcher) DispatchOne(ctx context.Context) (bool, error) {
	if d.updateTracker != nil {
		done, err := d.updateTracker.Start(update.WorkAutomation)
		if err != nil {
			return false, nil
		}
		defer done()
	}
	now := time.Now().UTC()
	dispatch, err := d.automationRepo.LeaseNextDispatch(ctx, d.claimant, now, d.lease)
	if err != nil || dispatch == nil {
		return false, err
	}
	fail := func(cause error) (bool, error) {
		if failErr := d.automationRepo.FailDispatch(context.WithoutCancel(ctx), dispatch.ID, d.claimant, cause.Error(), 5, time.Now().UTC()); failErr != nil {
			return true, fmt.Errorf("%v; recording dispatch failure: %w", cause, failErr)
		}
		return true, cause
	}
	envelope, err := d.automationRepo.GetDispatchEnvelope(ctx, dispatch.ID)
	if err != nil || envelope == nil {
		if err == nil {
			err = fmt.Errorf("automation dispatch envelope not found")
		}
		return fail(err)
	}
	if dispatch.ExecutionID != "" {
		execution, err := d.taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, d.claimant)
		if err != nil {
			if errors.Is(err, repository.ErrAutomationTaskBusy) {
				if cancelErr := d.automationRepo.CancelDispatchesForTask(context.WithoutCancel(ctx), dispatch.TaskID, "Automation task was cancelled or is no longer runnable"); cancelErr != nil {
					return true, fmt.Errorf("cancelling non-runnable automation dispatch: %w", cancelErr)
				}
				return true, nil
			}
			return fail(err)
		}
		if execution.Status == models.ExecCompleted || execution.Status == models.ExecFailed || execution.Status == models.ExecCancelled {
			if err := d.automationRepo.CompleteDispatch(context.WithoutCancel(ctx), dispatch.ID, execution.ID, execution.Status, execution.ErrorMessage); err != nil {
				return fail(err)
			}
			return true, nil
		}
		if err := d.workerSvc.SubmitPrepared(*envelope, execution.ID); err != nil && !errors.Is(err, ErrTaskAlreadyQueuedOrRunning) {
			return fail(err)
		}
		if err := d.automationRepo.MarkDispatchSubmitted(context.WithoutCancel(ctx), dispatch.ID, d.claimant, execution.ID); err != nil {
			applog.Infof("[automation-dispatcher] legacy submit acknowledgement failed dispatch=%s execution=%s: %v", dispatch.ID, execution.ID, err)
		}
		return true, nil
	}
	if err := d.automationRepo.MarkDispatchQueued(context.WithoutCancel(ctx), dispatch.ID, d.claimant); err != nil {
		return fail(err)
	}
	if err := d.workerSvc.SubmitPrepared(*envelope, ""); err != nil {
		if !errors.Is(err, ErrTaskAlreadyQueuedOrRunning) {
			return true, err
		}
		// The durable submitted dispatch is recoverable. An existing task queue
		// entry will either admit it or be pruned before reconciliation retries.
	}
	return true, nil
}
