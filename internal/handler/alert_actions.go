package handler

import (
	"context"
	"net/http"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type alertMutationArgs struct {
	ProjectID, AlertID string
	AlertIDs           []string
	Operation          string
	State              models.AlertDecisionState
}

func (h *Handler) mutateAlert(ctx context.Context, a alertMutationArgs) error {
	_, err := actAlertMutation(h, ctx, a)
	return err
}

var actAlertMutation = defineAction("alert.mutate", func(h *Handler, ctx context.Context, a alertMutationArgs) (string, authz.Permission, error) {
	if a.AlertID != "" && models.IsAggregateProject(a.ProjectID) {
		alert, err := h.alertSvc.GetByID(ctx, a.ProjectID, a.AlertID)
		if err != nil {
			return "", authz.Edit, err
		}
		return alert.ProjectID, authz.Edit, nil
	}
	return a.ProjectID, authz.Edit, h.requireActionProject(h.projectRuntimePrincipalContext(ctx), a.ProjectID, authz.Edit)
}, func(h *Handler, ctx context.Context, a alertMutationArgs) (struct{}, error) {
	var err error
	switch a.Operation {
	case "check":
		return struct{}{}, nil
	case "decision":
		err = h.alertSvc.SetDecision(ctx, a.ProjectID, a.AlertID, a.State)
	case "read":
		err = h.alertSvc.MarkRead(ctx, a.ProjectID, a.AlertID)
	case "read_bulk":
		err = h.alertSvc.MarkReadBulk(ctx, a.ProjectID, a.AlertIDs)
	case "read_all":
		err = h.alertSvc.MarkAllRead(ctx, a.ProjectID)
	case "delete":
		err = h.alertSvc.Delete(ctx, a.ProjectID, a.AlertID)
	case "delete_bulk":
		err = h.alertSvc.DeleteBulk(ctx, a.ProjectID, a.AlertIDs)
	case "delete_all":
		err = h.alertSvc.DeleteAll(ctx, a.ProjectID)
	default:
		err = statusErr(http.StatusBadRequest, "unknown alert action")
	}
	return struct{}{}, err
})
