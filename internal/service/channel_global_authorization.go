package service

import (
	"context"
	"encoding/json"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Channel utilities use the same authorization stores configured on their
// worker. Unconfigured embedded runtimes retain the worker's legacy behavior.
func guardChannelGlobalActions(handlers map[string]chatcontrol.RuntimeActionHandler, worker *WorkerService, projectID string) {
	if worker == nil || worker.runOrganizations == nil {
		return
	}
	for _, name := range []string{"view_settings", "list_channels", "list_models", "get_model", "list_personalities", "get_personality", "set_personality", "save_custom_personality", "list_agents", "create_agent", "update_agent"} {
		handler := handlers[name]
		if handler == nil {
			continue
		}
		handlers[name] = func(ctx context.Context, input json.RawMessage) (string, error) {
			ctx, err := channelCredentialPrincipalContext(ctx, worker)
			if err != nil {
				return "", err
			}
			if err := (&authz.Authorizer{Store: worker.runAccess}).Require(ctx, projectID, authz.View); err != nil {
				return "", err
			}
			return handler(ctx, input)
		}
	}
}

// Resolve a fresh administrator before channel credential work. Production
// workers configure the stores; embedded callers without them retain compatibility.
func channelCredentialPrincipalContext(ctx context.Context, worker *WorkerService) (context.Context, error) {
	if worker == nil || worker.runOrganizations == nil {
		return ctx, nil
	}
	return credentialPrincipalContext(ctx, worker.runOrganizations, worker.runAuthOff)
}

func credentialPrincipalContext(ctx context.Context, organizations persistence.OrganizationStore, authOff bool) (context.Context, error) {
	if organizations == nil {
		return ctx, nil
	}
	principal, ok := models.PrincipalFromContext(ctx)
	if !ok || principal.UserID == "" {
		if !authOff {
			return ctx, authz.ErrDenied
		}
		principal = models.Principal{UserID: models.BootstrapUserID}
	}
	user, err := organizations.UserByID(ctx, principal.UserID)
	if err != nil {
		return ctx, err
	}
	if user == nil || user.Disabled || !user.IsOrgAdmin() {
		return ctx, authz.ErrDenied
	}
	return models.WithPrincipal(ctx, user.Principal), nil
}
