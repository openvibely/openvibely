package service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/update"
)

// AutomationReconciler repairs Automation projections and resubmits durable
// prepared executions after a process restart. Existing Task and Execution rows
// remain authoritative; this service never rewrites their state.
type AutomationReconciler struct {
	automationRepo      *repository.AutomationRepo
	executionRepo       *repository.ExecutionRepo
	workerSvc           *WorkerService
	externalStateSvc    *AutomationExternalStateService
	liveViewTracker     *AutomationLiveViewTracker
	interval            time.Duration
	externalRefreshEach time.Duration
	liveViewWindow      time.Duration
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	updateTracker       *update.WorkTracker
}

func NewAutomationReconciler(automationRepo *repository.AutomationRepo, executionRepo *repository.ExecutionRepo, workerSvc *WorkerService) *AutomationReconciler {
	return &AutomationReconciler{automationRepo: automationRepo, executionRepo: executionRepo, workerSvc: workerSvc, interval: 15 * time.Second, externalRefreshEach: 5 * time.Minute, liveViewWindow: time.Minute}
}

func (r *AutomationReconciler) SetUpdateWorkTracker(tracker *update.WorkTracker) {
	r.updateTracker = tracker
}

// SetAutomationExternalStateService enables background refresh of Automations'
// tracked GitHub pull request state while their Live/Preview page is open, so
// it stays fresh without requiring a manual click.
func (r *AutomationReconciler) SetAutomationExternalStateService(svc *AutomationExternalStateService) {
	r.externalStateSvc = svc
}

// SetAutomationLiveViewTracker scopes background external-state refresh to
// Automations whose Live page was recently viewed, avoiding unnecessary
// GitHub API calls for automations nobody is looking at.
func (r *AutomationReconciler) SetAutomationLiveViewTracker(tracker *AutomationLiveViewTracker) {
	r.liveViewTracker = tracker
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
	if r.updateTracker != nil {
		done, err := r.updateTracker.Start(update.WorkAutomation)
		if err != nil {
			return nil
		}
		defer done()
	}
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
		automationobs.Event("automation.reconciliation.dispatch_finalized",
			automationobs.String("dispatch_id", dispatch.ID), automationobs.String("invocation_id", dispatch.InvocationID),
			automationobs.String("execution_id", execution.ID), automationobs.String("status", string(execution.Status)))
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
		automationobs.Event("automation.reconciliation.projection_repaired",
			automationobs.String("project_id", repair.ProjectID), automationobs.String("execution_id", repair.ExecutionID),
			automationobs.String("status", string(repair.Status)))
		applog.Infof("[automation-reconciler] repaired execution projection execution=%s", repair.ExecutionID)
	}
	if pruned, err := r.automationRepo.PruneTerminalizedAutomationPositions(ctx, 100); err != nil {
		return err
	} else if pruned > 0 {
		applog.Infof("[automation-reconciler] pruned %d terminalized automation position(s)", pruned)
	}
	if completed, err := r.automationRepo.ReconcileInvocationCompletions(ctx, 100); err != nil {
		return err
	} else if completed > 0 {
		applog.Infof("[automation-reconciler] completed %d invocation projection(s)", completed)
	}

	abandoned, err := r.automationRepo.ListAbandonedQueuedDispatches(ctx, 100)
	if err != nil {
		return err
	}
	for _, dispatch := range abandoned {
		if err := r.automationRepo.AbandonQueuedDispatch(ctx, dispatch.ID, "Automation task was cancelled or is no longer runnable"); err != nil {
			return err
		}
		applog.Infof("[automation-reconciler] abandoned queued dispatch=%s", dispatch.ID)
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
		automationobs.Event("automation.reconciliation.dispatch_resubmitted",
			automationobs.String("dispatch_id", dispatch.ID), automationobs.String("invocation_id", dispatch.InvocationID),
			automationobs.String("execution_id", dispatch.ExecutionID))
		applog.Infof("[automation-reconciler] resubmitted prepared dispatch=%s execution=%s", dispatch.ID, dispatch.ExecutionID)
	}
	if err := r.automationRepo.RecomputeAutomationHealthForAll(ctx, time.Now().UTC(), 100); err != nil {
		return err
	}
	return r.refreshStaleExternalState(ctx)
}

// refreshStaleExternalState proactively refreshes tracked GitHub pull request
// state for Automations whose state has gone stale, so their health and graph
// stay current without requiring a manual "Refresh GitHub state" click.
func (r *AutomationReconciler) refreshStaleExternalState(ctx context.Context) error {
	if r.externalStateSvc == nil {
		return nil
	}
	return r.refreshStaleExternalStateWith(ctx, func(ctx context.Context, projectID, automationID string, now time.Time) error {
		_, err := r.externalStateSvc.Refresh(ctx, projectID, automationID, now)
		return err
	})
}

func (r *AutomationReconciler) refreshStaleExternalStateWith(ctx context.Context, refresh func(context.Context, string, string, time.Time) error) error {
	if refresh == nil {
		return nil
	}
	now := time.Now().UTC()
	stale, err := r.automationRepo.ListAutomationsWithStaleExternalPullRequests(ctx, now.Add(-r.externalRefreshEach), 100)
	if err != nil {
		return err
	}
	for _, pair := range stale {
		projectID, automationID := pair[0], pair[1]
		if r.liveViewTracker != nil && !r.liveViewTracker.IsRecentlyViewed(projectID, automationID, r.liveViewWindow) {
			continue
		}
		if err := refresh(ctx, projectID, automationID, now); err != nil {
			applog.Infof("[automation-reconciler] external state refresh failed project=%s automation=%s: %v", projectID, automationID, err)
			continue
		}
		applog.Infof("[automation-reconciler] refreshed external GitHub state project=%s automation=%s", projectID, automationID)
	}
	return nil
}
