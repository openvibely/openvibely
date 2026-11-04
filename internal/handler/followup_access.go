package handler

import (
	"context"
	"errors"
	"time"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

func (h *Handler) followupUserContext(ctx context.Context, userID string) (context.Context, error) {
	ctx = h.followupAuthorizationContext(ctx)
	if h.projectAccessSvc == nil {
		return ctx, nil
	}
	if userID == "" && h.authMode == auth.AuthModeDisabled {
		userID = models.BootstrapUserID
	}
	if userID == "" || h.organizationSvc == nil {
		return ctx, authz.ErrDenied
	}
	user, err := h.organizationSvc.Store.UserByID(ctx, userID)
	if err != nil {
		return ctx, err
	}
	if user == nil || user.Disabled || user.Role == "" {
		return ctx, authz.ErrDenied
	}
	return models.WithPrincipal(ctx, user.Principal), nil
}

func (h *Handler) requireFollowupAccess(ctx context.Context, params streamingResponseParams) error {
	ctx = h.followupAuthorizationContext(ctx)
	if h.projectAccessSvc == nil {
		return nil
	}
	if isSlackChannelRun(params) && h.authMode != auth.AuthModeDisabled {
		principal, ok := models.PrincipalFromContext(ctx)
		if !ok || principal.UserID == "" || h.slackAuthRepo == nil || params.ChannelReply.SlackTeamID == "" || params.ChannelReply.SlackUserID == "" {
			return authz.ErrDenied
		}
		mapped, err := h.slackAuthRepo.ResolveApplicationUser(ctx, params.ProjectID, params.ChannelReply.SlackTeamID, params.ChannelReply.SlackUserID)
		if err != nil {
			return err
		}
		if mapped != principal.UserID {
			return authz.ErrDenied
		}
	}
	if h.taskRepo == nil {
		return authz.ErrDenied
	}
	task, err := h.taskRepo.GetByID(ctx, params.TaskID)
	if err != nil {
		return err
	}
	if task == nil || task.ProjectID != params.ProjectID {
		return authz.ErrDenied
	}
	if h.authMode != auth.AuthModeDisabled && (authz.UnmappedChannelOrigin(task.CreatedVia) || authz.UnmappedChannelOrigin(params.ChannelReply.Source)) {
		return authz.ErrDenied
	}
	if params.ExecID != "" {
		if h.runIdentities == nil {
			return authz.ErrDenied
		}
		userID, found, err := h.runIdentities.ExecutionUser(ctx, params.ExecID)
		if err != nil {
			return err
		}
		if found {
			if userID == "" && h.authMode == auth.AuthModeDisabled {
				userID = models.BootstrapUserID
			}
			principal, ok := models.PrincipalFromContext(ctx)
			if !ok || userID == "" || principal.UserID != userID {
				return authz.ErrDenied
			}
		} else if h.authMode != auth.AuthModeDisabled {
			return authz.ErrDenied
		}
		// Scope and provenance checks also apply to auth-off legacy runs that
		// have no saved executor; absence is never permission to borrow a run.
		if err := h.runIdentities.CheckExecutionGoal(ctx, params.ExecID, params.TaskID); err != nil {
			return err
		}
	}
	permission := authz.Edit
	if task.AutoMerge || task.AutoMergeOnGoalAchieved || task.CreatedVia == models.TaskOriginWebhook {
		permission = authz.Manage
	}
	if params.AutomationContext != nil && (params.AutomationContext.OriginTask || len(params.AutomationContext.Bindings) > 0) {
		permission = authz.Manage
	}
	if err := h.projectAccessSvc.Require(ctx, task.ProjectID, permission); err != nil {
		return err
	}
	if isGoalContinuation(params.InputOrigin, params.InputOriginAgent) {
		p, _ := models.PrincipalFromContext(ctx)
		_, err := h.queuedGoalContinuationContext(ctx, models.ThreadInput{TaskID: params.TaskID, GoalID: params.GoalID, AuthorUserID: p.UserID, Source: params.InputOrigin, OriginAgent: params.InputOriginAgent})
		return err
	}
	return nil
}

func (h *Handler) watchFollowupAccess(ctx context.Context, cancel context.CancelFunc, params streamingResponseParams) {
	if h.projectAccessSvc == nil {
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
				if h.requireFollowupAccess(ctx, params) != nil {
					cancel()
					return
				}
			}
		}
	}()
}

func (h *Handler) rejectUnauthorizedQueuedFollowup(ctx context.Context, input models.ThreadInput, cause error) error {
	if !errors.Is(cause, authz.ErrDenied) || h.threadInputRepo == nil || input.ID == "" {
		return cause
	}
	// Database outages remain retryable. Only an explicit authorization denial
	// cancels the row; CancelPending cannot alter an already-applied input.
	if _, err := h.threadInputRepo.CancelPending(context.WithoutCancel(ctx), input.ID); err != nil {
		return cause
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{Type: events.TaskThreadInputCancelled, ProjectID: input.ProjectID, TaskID: input.TaskID, PendingInputID: input.ID, Message: "Queued follow-up cancelled because access is no longer available"})
	}
	go h.PromoteQueuedTaskThreadInput(input.TaskID)
	return cause
}

func isSlackChannelRun(params streamingResponseParams) bool {
	return params.ChannelReply.Source == models.TaskOriginSlack || params.Surface == chatcontrol.SurfaceSlack
}

func (h *Handler) followupAuthorizationContext(ctx context.Context) context.Context {
	if h.projectAccessSvc != nil && h.authMode != auth.AuthModeDisabled {
		return models.WithRequiredReviewerIdentity(ctx)
	}
	return ctx
}
