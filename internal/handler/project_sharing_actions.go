package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
)

type projectSharingState struct {
	Policy  *models.ProjectAccess
	Members []models.ProjectMember
	Users   []models.OrgUser
	Audit   []models.ProjectAuditEvent
	Owner   bool
}

func sharingOwner(h *Handler, ctx context.Context, id string, permission authz.Permission) (string, authz.Permission, error) {
	if h.projectAccessSvc == nil || h.organizationSvc == nil {
		return "", "", statusErr(http.StatusNotFound, "project sharing unavailable")
	}
	return id, permission, h.projectAccessSvc.Require(ctx, id, permission)
}

var actProjectSharing = defineAction("project.sharing", func(h *Handler, ctx context.Context, id string) (string, authz.Permission, error) {
	return sharingOwner(h, ctx, id, authz.Manage)
}, func(h *Handler, ctx context.Context, id string) (projectSharingState, error) {
	var out projectSharingState
	var err error
	if out.Policy, err = h.projectAccessSvc.Policy(ctx, id); err != nil {
		return out, err
	}
	if out.Members, err = h.projectAccessSvc.Members(ctx, id); err != nil {
		return out, err
	}
	if out.Users, err = h.organizationSvc.Store.Members(ctx); err != nil {
		return out, err
	}
	if out.Audit, err = h.projectAccessSvc.Audit(ctx, id); err != nil {
		return out, err
	}
	out.Owner = h.projectAccessSvc.Require(ctx, id, authz.Own) == nil
	return out, nil
})

type projectRoleChange struct{ UserID, Role string }
type projectSharingArgs struct {
	ProjectID, UserID, Role, Operation string
	Changes                            []projectRoleChange
}
type projectSharingResult struct {
	Message     string
	Transferred bool
}

var actUpdateProjectSharing = defineAction("project.sharing.update", func(h *Handler, ctx context.Context, a projectSharingArgs) (string, authz.Permission, error) {
	return sharingOwner(h, ctx, a.ProjectID, authz.Own)
}, func(h *Handler, ctx context.Context, a projectSharingArgs) (projectSharingResult, error) {
	var out projectSharingResult
	policy, err := h.projectAccessSvc.Policy(ctx, a.ProjectID)
	if err != nil {
		return out, err
	}
	switch a.Operation {
	case "members":
		roles := make(map[string]string, len(a.Changes))
		for _, change := range a.Changes {
			roles[change.UserID] = change.Role
		}
		err = h.projectAccessSvc.SetMembers(ctx, a.ProjectID, roles)
	case "member":
		err = h.projectAccessSvc.SetMember(ctx, a.ProjectID, a.UserID, a.Role)
	case "transfer":
		policy.OwnerUserID = a.UserID
		err = h.projectAccessSvc.Update(ctx, *policy)
	default:
		out.Message = "Choose a valid sharing action."
		return out, nil
	}
	if err != nil {
		if a.Operation == "members" {
			var denied *authz.InsufficientRole
			if errors.As(err, &denied) {
				return out, err
			}
			if !errors.Is(err, authz.ErrDenied) {
				return out, err
			}
			if accessErr := h.projectAccessSvc.Require(ctx, a.ProjectID, authz.Own); accessErr != nil {
				return out, accessErr
			}
			return out, statusErr(http.StatusBadRequest, "Could not save membership changes. Refresh and check the selected members.")
		}
		if errors.Is(err, authz.ErrDenied) {
			out.Message = "This change is not allowed. Check the member and your project role."
			return out, nil
		}
		if errors.Is(err, authz.ErrSandboxRequired) {
			out.Message = "Private projects are not available until sandboxing is implemented."
			return out, nil
		}
		return out, err
	}
	out.Transferred = h.projectAccessSvc.Require(ctx, a.ProjectID, authz.Own) != nil
	return out, nil
})
