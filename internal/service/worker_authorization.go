package service

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// SetRunAuthorization enables durable identity checks on production workers.
// Tests and embedded users without an authorization store retain legacy behavior.
func (w *WorkerService) SetRunAuthorization(identities persistence.RunIdentityStore, organizations persistence.OrganizationStore, access persistence.ProjectAccessStore, authOff bool) {
	w.runIdentities, w.runOrganizations, w.runAccess, w.runAuthOff = identities, organizations, access, authOff
}

func (w *WorkerService) runPrincipalContext(ctx context.Context, task models.Task) (context.Context, error) {
	if w.runIdentities == nil {
		return ctx, nil
	}
	userID, err := w.runIdentities.TaskUser(ctx, task.ID)
	if err != nil {
		return ctx, err
	}
	return w.userRunPrincipalContext(ctx, task, userID)
}

func (w *WorkerService) userRunPrincipalContext(ctx context.Context, task models.Task, userID string) (context.Context, error) {
	ctx = w.reviewerRecoveryContext(ctx)
	if w.runIdentities == nil {
		return ctx, nil
	}
	if userID == "" && w.runAuthOff {
		userID = models.BootstrapUserID
	}
	if userID == "" {
		return ctx, authz.ErrDenied
	}
	user, err := w.runOrganizations.UserByID(ctx, userID)
	if err != nil {
		return ctx, err
	}
	if user == nil || user.Disabled || user.Role == "" {
		return ctx, authz.ErrDenied
	}
	ctx = models.WithPrincipal(ctx, user.Principal)
	return ctx, w.checkRunPermission(ctx, task)
}

type runExecutionKey struct{}

func (w *WorkerService) checkRunPermission(ctx context.Context, task models.Task) error {
	if w.runOrganizations != nil && !w.runAuthOff && authz.UnmappedChannelOrigin(task.CreatedVia) {
		return authz.ErrDenied
	}
	ctx = w.reviewerRecoveryContext(ctx)
	if executionID, _ := ctx.Value(runExecutionKey{}).(string); executionID != "" && w.runIdentities != nil {
		if err := w.runIdentities.CheckExecutionGoal(ctx, executionID, task.ID); err != nil {
			return err
		}
	}
	permission := authz.Edit
	if task.AutoMerge || task.AutoMergeOnGoalAchieved || task.CreatedVia == models.TaskOriginWebhook || persistence.IsAutomationTaskCreatedVia(task.CreatedVia) {
		permission = authz.Manage
	}
	if automation, ok := AutomationContextFromContext(ctx); ok && (automation.OriginTask || len(automation.Bindings) > 0) {
		permission = authz.Manage
	}
	return (&authz.Authorizer{Store: w.runAccess}).Require(ctx, task.ProjectID, permission)
}

// watchRunAuthorization keeps the same principal throughout the run. It never
// picks up a later submitter's identity when checking revoked access.
func (w *WorkerService) watchRunAuthorization(ctx context.Context, cancel context.CancelFunc, task models.Task) {
	if w.runIdentities == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := w.checkRunPermission(ctx, task); err != nil {
					cancel()
					return
				}
			}
		}
	}()
}

// SubmitAs saves the requesting principal before admitting ordinary queued work.
// Queue recovery uses Submit and resolves this saved identity again at startup.
func (w *WorkerService) SubmitAs(ctx context.Context, task models.Task) error {
	if task.Category == models.CategoryChat {
		return nil
	}
	if w.runIdentities == nil {
		w.Submit(task)
		return nil
	}
	if _, ok := models.PrincipalFromContext(ctx); !ok {
		if !w.runAuthOff {
			return authz.ErrDenied
		}
		ctx = models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	}
	// Check the full run policy before changing the durable identity or queue.
	// BindTask's editor check alone does not authorize merge/webhook work.
	if err := w.checkRunPermission(ctx, task); err != nil {
		return err
	}
	// Serialize binding with admission so a duplicate cannot replace the queued
	// or running task's principal. dispatchNext must run after releasing the lock.
	w.mu.Lock()
	if w.pending[task.ID] {
		w.mu.Unlock()
		return nil
	}
	if err := w.runIdentities.BindTask(ctx, task.ID); err != nil {
		w.mu.Unlock()
		return err
	}
	w.pending[task.ID] = true
	w.queue = append(w.queue, task)
	w.mu.Unlock()
	select {
	case w.submitted <- task:
	default:
	}
	w.dispatchNext()
	return nil
}

func (w *WorkerService) schedulePrincipalContext(ctx context.Context, task models.Task, scheduleID string) (context.Context, error) {
	if w.runIdentities == nil {
		return ctx, nil
	}
	userID, err := w.runIdentities.ScheduleUser(ctx, scheduleID)
	if err != nil {
		return ctx, err
	}
	return w.userRunPrincipalContext(ctx, task, userID)
}

func (w *WorkerService) executionPrincipalContext(ctx context.Context, task models.Task, executionID string) (context.Context, error) {
	ctx = w.reviewerRecoveryContext(ctx)
	if w.runIdentities != nil && executionID != "" {
		// Validate execution ownership even for legacy rows without an identity
		// snapshot; otherwise fallback can authorize a foreign execution.
		if err := w.runIdentities.CheckExecutionGoal(ctx, executionID, task.ID); err != nil {
			return ctx, err
		}
		userID, found, err := w.runIdentities.ExecutionUser(ctx, executionID)
		if err != nil {
			return ctx, err
		}
		if found {
			return w.userRunPrincipalContext(context.WithValue(ctx, runExecutionKey{}, executionID), task, userID)
		}
	}
	return w.runPrincipalContext(ctx, task)
}

func (w *WorkerService) automationPrincipalContext(ctx context.Context, envelope models.AutomationDispatchEnvelope, executionID string) (context.Context, error) {
	// Capacity waits can outlive the original dispatch check. Reload the durable
	// envelope so revoked invocation bindings cannot use a cached admission.
	if executionID == "" && w.runIdentities != nil && w.automationRepo != nil {
		fresh, err := w.automationRepo.GetDispatchEnvelope(ctx, envelope.DispatchID)
		if err != nil {
			return ctx, err
		}
		if fresh == nil || fresh.Task.ID != envelope.Task.ID || fresh.Task.ProjectID != envelope.Task.ProjectID || fresh.RunAsUserID != envelope.RunAsUserID {
			return ctx, authz.ErrDenied
		}
		envelope = *fresh
	}
	ctx = WithAutomationContext(ctx, envelope.Context)
	if executionID == "" && envelope.RunAsUserID != "" {
		return w.userRunPrincipalContext(ctx, envelope.Task, envelope.RunAsUserID)
	}
	return w.executionPrincipalContext(ctx, envelope.Task, executionID)
}

// Preserve the policy in the returned execution context for provider checks,
// tool calls and later watcher ticks, including recovery without ingress data.
func (w *WorkerService) reviewerRecoveryContext(ctx context.Context) context.Context {
	if w.runIdentities != nil && !w.runAuthOff {
		return models.WithRequiredReviewerIdentity(ctx)
	}
	return ctx
}
