package handler

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Use the same project policy as task threads, including auth-disabled local use.
func (h *Handler) requirePageAccess(ctx context.Context, projectID string, permission authz.Permission) error {
	if h.projectAccessSvc == nil {
		return nil
	}
	return h.projectAccessSvc.Require(h.projectRuntimePrincipalContext(ctx), projectID, permission)
}

// Background work and asset capabilities carry the saved sender, not a browser session.
func (h *Handler) pagePrincipalContext(ctx context.Context, principal, projectID string, permission authz.Permission) (context.Context, error) {
	if h.projectAccessSvc == nil {
		return ctx, nil
	}
	userID := ""
	if strings.HasPrefix(principal, "user:") {
		userID = strings.TrimPrefix(principal, "user:")
	}
	restored, err := h.followupUserContext(ctx, userID)
	if err != nil {
		return ctx, err
	}
	return restored, h.projectAccessSvc.Require(restored, projectID, permission)
}

func (h *Handler) authorizePageOperation(ctx context.Context, caller pageintegration.Caller, projectID string, kind pageintegration.OperationKind) (context.Context, error) {
	permission := authz.View
	if kind == pageintegration.OperationAction {
		permission = authz.Edit
	}
	return h.pagePrincipalContext(ctx, caller.PrincipalID, projectID, permission)
}
