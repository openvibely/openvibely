package handler

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// This scope is internal to action registration; clients cannot use it as a project.
const instanceActionProject = "__instance_action__"

func (h *Handler) requireActionAdmin(ctx context.Context) error {
	ctx = h.projectRuntimePrincipalContext(ctx)
	var store persistence.OrganizationStore
	if h.organizationSvc != nil {
		store = h.organizationSvc.Store
	} else if h.remoteExecutor != nil {
		store = h.remoteExecutor.Stores.Organizations
	}
	if store == nil {
		if h.projectAccessSvc == nil && h.remoteExecutor == nil {
			return nil
		}
		return authz.ErrDenied
	}
	p, ok := models.PrincipalFromContext(ctx)
	if !ok {
		return authz.ErrDenied
	}
	user, err := store.UserByID(ctx, p.UserID)
	if err != nil {
		return err
	}
	if user == nil || user.Disabled || !user.IsOrgAdmin() {
		return authz.ErrDenied
	}
	return nil
}

type cardBulkArgs struct {
	Kind, Operation, ProjectID string
	IDs                        []string
	CheckOnly                  bool
}

// Split before sending: remote actions never receive IDs belonging to another server.
func (h *Handler) runCardBulk(ctx context.Context, kind, operation, project string, ids []string) error {
	return h.cardBulk(ctx, kind, operation, project, ids, false)
}

func (h *Handler) cardBulk(ctx context.Context, kind, operation, project string, ids []string, checkOnly bool) error {
	groups := map[string][]string{}
	projectServer, _ := remoteid.ServerOf(project)
	for _, id := range ids {
		server, _ := remoteid.ServerOf(id)
		groups[server] = append(groups[server], id)
	}
	// Shared repository scopes are selected on the desktop; verify membership
	// before dropping that context at the server boundary.
	if _, scoped := models.ScopeProjects(ctx); scoped && (kind == "alerts" || kind == "automations" || kind == "webhooks") {
		if _, err := h.cardBulkProjects(ctx, cardBulkArgs{Kind: kind, ProjectID: project, IDs: ids}); err != nil {
			return actionHTTPError(err)
		}
	}

	servers := make([]string, 0, len(groups))
	for server := range groups {
		servers = append(servers, server)
	}
	sort.Strings(servers)
	call := func(server string, check bool) error {
		a := cardBulkArgs{Kind: kind, Operation: operation, ProjectID: project, IDs: groups[server], CheckOnly: check}
		if projectServer != "" && server != projectServer {
			return statusErr(http.StatusBadRequest, "selection is outside the displayed project")
		}
		if server != "" {
			if !h.remoteSignedIn(ctx, server) {
				return reconnectError{server}
			}
			var out struct{}
			return actionHTTPError(h.remoteHub().Act(ctx, server, "cards.bulk", a, &out))
		}
		_, err := actCardBulk(h, remote.WithServer(ctx, ""), a)
		return err
	}
	// Validate every destination before the first write. Runtime failures can still be partial.
	for _, server := range servers {
		if err := call(server, true); err != nil {
			return actionHTTPError(err)
		}
	}
	if checkOnly {
		return nil
	}

	var failures []string
	for _, server := range servers {
		if err := call(server, false); err != nil {
			if len(servers) == 1 {
				return actionHTTPError(err)
			}
			name := server
			if name == "" {
				name = "Local"
			}
			failures = append(failures, fmt.Sprintf("%s: %v", name, err))
		}
	}
	if len(failures) > 0 {
		return actionHTTPError(statusErr(http.StatusBadGateway, "Some actions failed; other selected items may have changed. "+strings.Join(failures, "; ")))
	}
	return nil
}

func (h *Handler) cardBulkProjects(ctx context.Context, a cardBulkArgs) ([]string, error) {
	var projects []string
	for _, id := range a.IDs {
		var project string
		switch a.Kind {
		case "alerts":
			item, err := h.alertSvc.GetByID(ctx, a.ProjectID, id)
			if err != nil {
				return nil, statusErr(http.StatusBadRequest, "all selected items must exist in the current scope")
			}
			if item == nil {
				return nil, statusErr(http.StatusBadRequest, "selected item not found")
			}
			project = item.ProjectID
		case "automations":
			projectID, err := h.automationLifecycleSvc.ProjectForAction(ctx, a.ProjectID, id)
			if err != nil {
				return nil, statusErr(http.StatusBadRequest, "all selected automations must exist in the current scope")
			}
			project = projectID
		case "webhooks":
			item, err := h.webhookRepo.GetByID(ctx, id)
			if err != nil {
				return nil, statusErr(http.StatusBadRequest, "all selected items must exist in the current scope")
			}
			if item == nil {
				return nil, statusErr(http.StatusBadRequest, "selection is outside the displayed project")
			}
			if item == nil {
				return nil, statusErr(http.StatusBadRequest, "selected item not found")
			}
			project = item.ProjectID
		default:
			return nil, statusErr(http.StatusBadRequest, "unknown card collection")
		}
		if !models.IsAggregateProject(a.ProjectID) && project != a.ProjectID {
			return nil, statusErr(http.StatusBadRequest, "selection is outside the displayed project")
		}
		if !models.InScope(ctx, project) {
			return nil, statusErr(http.StatusBadRequest, "selection is outside the displayed project")
		}
		projects = append(projects, project)
	}
	return projects, nil
}

var actCardBulk = defineAction("cards.bulk", func(h *Handler, ctx context.Context, a cardBulkArgs) (string, authz.Permission, error) {
	if len(a.IDs) == 0 || len(a.IDs) > bulkDeleteMaxItems {
		return "", authz.Edit, statusErr(http.StatusBadRequest, "invalid selection size")
	}
	switch a.Kind {
	case "models", "agents":
		return instanceActionProject, authz.Edit, h.requireActionAdmin(ctx)
	}
	projects, err := h.cardBulkProjects(ctx, a)
	if err != nil {
		return "", authz.Edit, err
	}
	for _, p := range projects {
		if err := h.requireActionProject(h.projectRuntimePrincipalContext(ctx), p, authz.Edit); err != nil {
			return "", authz.Edit, err
		}
	}
	return projects[0], authz.Edit, nil
}, func(h *Handler, ctx context.Context, a cardBulkArgs) (struct{}, error) {
	var err error
	switch a.Kind {
	case "models":
		err = h.deleteModels(ctx, a.IDs, a.CheckOnly)
	case "agents":
		err = h.deleteAgents(ctx, a.ProjectID, a.IDs, a.CheckOnly)
	default:
		projects, e := h.cardBulkProjects(ctx, a)
		if e != nil {
			return struct{}{}, e
		}
		if a.CheckOnly {
			return struct{}{}, nil
		}
		groups := map[string][]string{}
		for i, p := range projects {
			groups[p] = append(groups[p], a.IDs[i])
		}
		for p, ids := range groups {
			switch a.Kind {
			case "alerts":
				if a.Operation == "read" {
					err = h.alertSvc.MarkReadBulk(ctx, p, ids)
				} else if a.Operation == "delete" {
					err = h.alertSvc.DeleteBulk(ctx, p, ids)
				} else {
					err = statusErr(http.StatusBadRequest, "unknown alert operation")
				}
			case "automations":
				err = h.automationLifecycleSvc.DeleteBulk(ctx, p, ids)
			case "webhooks":
				err = h.webhookRepo.DeleteBulk(ctx, p, ids)
			}
			if err != nil {
				break
			}
		}
	}
	return struct{}{}, err
})

// Enumerate the actual project scope, including saved servers that are temporarily down.
// A down server must cause an error, never a misleading successful local-only operation.
func (h *Handler) cardActionProjects(ctx context.Context, scope string) ([]string, error) {
	if !models.IsAggregateProject(scope) {
		return []string{scope}, nil
	}
	if scope != models.AllProjectsID {
		ids, _ := models.ScopeProjects(ctx)
		return ids, nil
	}
	local, err := h.projectSvc.ListSelectorOptions(remote.WithServer(ctx, ""))
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(local))
	for _, p := range local {
		ids = append(ids, p.ID)
	}
	saved, err := h.loadSavedServers(ctx)
	if err != nil {
		return nil, err
	}
	for _, server := range saved {
		if !server.Enabled {
			continue
		}
		if !h.remoteSignedIn(ctx, server.ID) {
			return nil, reconnectError{server.ID}
		}
		var projects []models.Project
		var readErr error
		err = h.remoteHub().Read(ctx, server.ID, "Projects", "ListSelectorOptions", nil, &projects, &readErr)
		if err != nil {
			return nil, err
		}
		if readErr != nil {
			return nil, readErr
		}
		for _, p := range projects {
			ids = append(ids, p.ID)
		}
	}
	return ids, nil
}

func (h *Handler) mutateAllAlerts(ctx context.Context, scope, operation string) error {
	projects, err := h.cardActionProjects(ctx, scope)
	if err != nil {
		return err
	}
	// Preflight the same owner permissions used by each server's mutation.
	for _, p := range projects {
		if _, err := actAlertMutation(h, ctx, alertMutationArgs{ProjectID: p, Operation: "check"}); err != nil {
			return actionHTTPError(err)
		}
	}
	var failures []string
	for _, p := range projects {
		if _, err := actAlertMutation(h, ctx, alertMutationArgs{ProjectID: p, Operation: operation}); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", p, err))
		}
	}
	if len(failures) > 0 {
		return actionHTTPError(statusErr(http.StatusBadGateway, "Some alert actions failed; refresh to see remaining alerts. "+strings.Join(failures, "; ")))
	}
	return nil
}

var actChannelsBulk = defineAction("channels.bulk", func(h *Handler, ctx context.Context, a cardBulkArgs) (string, authz.Permission, error) {
	if len(a.IDs) == 0 || len(a.IDs) > bulkDeleteMaxItems {
		return "", authz.Edit, statusErr(http.StatusBadRequest, "invalid selection size")
	}
	return instanceActionProject, authz.Edit, h.requireActionAdmin(ctx)
}, func(h *Handler, ctx context.Context, a cardBulkArgs) (struct{}, error) {
	return struct{}{}, h.deleteChannels(ctx, a.ProjectID, a.IDs)
})
