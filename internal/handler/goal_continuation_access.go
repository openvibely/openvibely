package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type continuationGoalKey struct{}

func isGoalContinuation(origin, agent string) bool {
	return origin == models.TaskOriginSystemAgent && agent == models.AgentSystemKindGoal
}

// resolveGoalContinuation validates the caller as well as the saved goal user.
// On promotion, expectedUser is the durable queue author, never a new submitter.
func (h *Handler) resolveGoalContinuation(ctx context.Context, taskID, expectedUser string) (context.Context, error) {
	if h.projectAccessSvc == nil {
		return ctx, nil
	}
	if h.taskGoalSvc == nil {
		return ctx, authz.ErrDenied
	}
	goal, err := h.taskGoalSvc.GetEvaluableGoal(ctx, taskID)
	if err != nil {
		return ctx, err
	}
	if goal == nil {
		return ctx, authz.ErrDenied
	}
	userID := goal.RunAsUserID
	if userID == "" && h.authMode == auth.AuthModeDisabled {
		userID = models.BootstrapUserID
	}
	if userID == "" || (expectedUser != "" && expectedUser != userID) {
		return ctx, authz.ErrDenied
	}
	if h.organizationSvc == nil {
		return ctx, authz.ErrDenied
	}
	user, err := h.organizationSvc.Store.UserByID(ctx, userID)
	if err != nil {
		return ctx, err
	}
	if user == nil || user.Disabled || user.Role == "" {
		return ctx, authz.ErrDenied
	}
	return context.WithValue(models.WithPrincipal(ctx, user.Principal), continuationGoalKey{}, goal.GoalID), nil
}

func (h *Handler) queuedGoalContinuationContext(ctx context.Context, input models.ThreadInput) (context.Context, error) {
	if h.projectAccessSvc == nil || !isGoalContinuation(string(input.Source), input.OriginAgent) {
		return ctx, nil
	}
	userID := input.AuthorUserID
	if userID == "" && h.authMode == auth.AuthModeDisabled {
		userID = models.BootstrapUserID
	}
	if userID == "" {
		return ctx, authz.ErrDenied
	}
	ctx = models.WithPrincipal(ctx, models.Principal{UserID: userID})
	resolved, err := h.resolveGoalContinuation(ctx, input.TaskID, userID)
	if err != nil {
		return ctx, err
	}
	goalID, _ := resolved.Value(continuationGoalKey{}).(string)
	if input.GoalID == "" || input.GoalID != goalID {
		return ctx, authz.ErrDenied
	}
	return resolved, nil
}
