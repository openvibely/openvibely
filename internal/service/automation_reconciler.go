package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/repository"
)

// AutomationReconciler repairs Automation projections and resubmits durable
// prepared executions after a process restart. Existing Task and Execution rows
// remain authoritative; this service never rewrites their state.
type AutomationReconciler struct {
	automationRepo *repository.AutomationRepo
	executionRepo  *repository.ExecutionRepo
	workerSvc      *WorkerService
	interval       time.Duration
	cancel         context.CancelFunc
	wg             sync.WaitGroup
}

func NewAutomationReconciler(automationRepo *repository.AutomationRepo, executionRepo *repository.ExecutionRepo, workerSvc *WorkerService) *AutomationReconciler {
	return &AutomationReconciler{automationRepo: automationRepo, executionRepo: executionRepo, workerSvc: workerSvc, interval: 15 * time.Second}
}

func (r *AutomationReconciler) Start(ctx context.Context) {
	if r == nil || r.automationRepo == nil || r.executionRepo == nil || r.workerSvc == nil {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.run(runCtx)
	}()
}

func (r *AutomationReconciler) Stop() {
	if r == nil {
		return
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}

func (r *AutomationReconciler) run(ctx context.Context) {
	r.reconcile(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reconcile(ctx)
		}
	}
}

func (r *AutomationReconciler) reconcile(ctx context.Context) {
	if err := r.ReconcileOnce(ctx); err != nil {
		applog.Infof("[automation-reconciler] reconciliation failed: %v", err)
	}
}

func (r *AutomationReconciler) ReconcileOnce(ctx context.Context) error {
	terminal, err := r.automationRepo.ListTerminalUnfinalizedDispatches(ctx, 100)
	if err != nil {
		return err
	}
	for _, dispatch := range terminal {
		execution, err := r.executionRepo.GetByID(ctx, dispatch.ExecutionID)
		if err != nil {
			return err
		}
		if execution == nil {
			continue
		}
		if err := r.automationRepo.CompleteDispatch(ctx, dispatch.ID, execution.ID, execution.Status, execution.ErrorMessage); err != nil {
			return err
		}
		applog.Infof("[automation-reconciler] finalized dispatch=%s execution=%s", dispatch.ID, execution.ID)
	}

	repairs, err := r.automationRepo.ListExecutionProjectionRepairs(ctx, 100)
	if err != nil {
		return err
	}
	for _, repair := range repairs {
		if err := r.automationRepo.RepairExecutionProjection(ctx, repair); err != nil {
			return err
		}
		applog.Infof("[automation-reconciler] repaired execution projection execution=%s", repair.ExecutionID)
	}
	if completed, err := r.automationRepo.ReconcileInvocationCompletions(ctx, 100); err != nil {
		return err
	} else if completed > 0 {
		applog.Infof("[automation-reconciler] completed %d invocation projection(s)", completed)
	}

	recoverable, err := r.automationRepo.ListRecoverablePreparedDispatches(ctx, 100)
	if err != nil {
		return err
	}
	for _, dispatch := range recoverable {
		envelope, err := r.automationRepo.GetDispatchEnvelope(ctx, dispatch.ID)
		if err != nil {
			return err
		}
		if envelope == nil {
			continue
		}
		if err := r.workerSvc.SubmitPrepared(*envelope, dispatch.ExecutionID); err != nil {
			if errors.Is(err, ErrTaskAlreadyQueuedOrRunning) {
				continue
			}
			return err
		}
		applog.Infof("[automation-reconciler] resubmitted prepared dispatch=%s execution=%s", dispatch.ID, dispatch.ExecutionID)
	}
	return r.automationRepo.RecomputeAutomationHealthForAll(ctx, time.Now().UTC(), 100)
}
