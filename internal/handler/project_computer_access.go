package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// Subscribe before checking authorization so a concurrent revocation cannot
// fall between the initial permission check and registration of the connection.
func (h *Handler) computerStreamAccess(parent context.Context, project string) (context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(parent)
	principal, _ := models.PrincipalFromContext(ctx)
	var projectWatch, userWatch *service.AccessWatch
	var projectChanges, userChanges <-chan struct{}
	if h.projectAccessSvc != nil {
		projectWatch = h.projectAccessSvc.Watch(principal.UserID)
		projectWatch.SetProjects(map[string]bool{project: true}, false)
		projectChanges = projectWatch.C()
	}
	if h.organizationSvc != nil {
		userWatch = h.organizationSvc.Watch(principal.UserID)
		userChanges = userWatch.C()
	}
	cleanup := func() { projectWatch.Close(); userWatch.Close() }
	authorized := func() error {
		if h.projectAccessSvc != nil {
			return h.projectAccessSvc.Require(ctx, project, authz.Edit)
		}
		return nil
	}
	if err := authorized(); err != nil {
		cleanup()
		cancel()
		return ctx, cancel, err
	}
	go func() {
		defer cleanup()
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case <-projectChanges:
				projectWatch.Drain()
				if authorized() != nil {
					return
				}
			case <-userChanges:
				for _, change := range userWatch.Drain() {
					if change.CloseConnections {
						return
					}
				}
				if authorized() != nil {
					return
				}
			}
		}
	}()
	return ctx, cancel, nil
}
