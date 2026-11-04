package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"

	"github.com/openvibely/openvibely/internal/service/authz"
)

// A callback has no browser principal. Only the actor captured when the flow
// began can authorize it; never substitute the callback caller or current owner.
func (h *Handler) requireOAuthActor(ctx context.Context, userID string) error {
	if h.projectAccessSvc == nil {
		return nil
	} // legacy unconfigured embeddings
	if userID == "" || h.organizationSvc == nil {
		return authz.ErrDenied
	}
	user, err := h.organizationSvc.Store.UserByID(ctx, userID)
	if err != nil {
		return err
	}
	if user == nil || user.Disabled || !user.IsOrgAdmin() {
		return authz.ErrDenied
	}
	return nil
}

// Preserve identity through background credential persistence; legacy test and
// refresh contexts without an initiating actor retain their existing behavior.
func oauthActorContext(ctx context.Context, userID string) context.Context {
	if userID == "" {
		return ctx
	}
	return models.WithPrincipal(ctx, models.Principal{UserID: userID})
}
