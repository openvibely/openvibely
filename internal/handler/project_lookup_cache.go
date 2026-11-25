package handler

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/web/templates/layout"
)

type projectLookupKey struct{}
type projectLookupCache struct {
	mu       sync.Mutex
	projects map[string][]models.ProjectAPIItem
	groups   map[string][]layout.SharedProject
	servers  map[string][]layout.RemoteServer
}

// A fresh cache is installed for each HTTP request, never for a socket's lifetime.
// Authorization, routing and workspace scope are part of each cache key.
func withProjectLookups(ctx context.Context) context.Context {
	return context.WithValue(ctx, projectLookupKey{}, &projectLookupCache{projects: map[string][]models.ProjectAPIItem{}, groups: map[string][]layout.SharedProject{}, servers: map[string][]layout.RemoteServer{}})
}
func projectLookupMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		if c.Request().Method == "GET" && c.Request().URL.Path != "/events/ws" {
			c.SetRequest(c.Request().WithContext(withProjectLookups(c.Request().Context())))
		}
		return next(c)
	}
}
func memoProjectLookup[T any](ctx context.Context, selectMap func(*projectLookupCache) map[string][]T, load func() []T) []T {
	cache, _ := ctx.Value(projectLookupKey{}).(*projectLookupCache)
	if cache == nil {
		return load()
	}
	principal, identified := models.PrincipalFromContext(ctx)
	scope, scoped := models.ScopeProjects(ctx)
	encoded, _ := json.Marshal([]any{principal, identified, scope, scoped, remote.RoutingServer(ctx)})
	key := string(encoded)
	cache.mu.Lock()
	values, ok := selectMap(cache)[key]
	cache.mu.Unlock()
	if ok {
		return values
	}
	values = load()
	cache.mu.Lock()
	selectMap(cache)[key] = values
	cache.mu.Unlock()
	return values
}

type repositoryChangeSource interface {
	SubscribeRepositoryChanges() (<-chan struct{}, func())
}

func (h *Handler) repositoryChanges() (<-chan struct{}, func()) {
	var source any = h.projectRepo
	if h.remoteExecutor != nil {
		source = h.remoteExecutor.Stores.Projects
	}
	if source, ok := source.(repositoryChangeSource); ok {
		return source.SubscribeRepositoryChanges()
	}
	return nil, func() {}
}

func (h *Handler) repositoryLookupsPending(ctx context.Context) bool {
	for _, p := range h.repositoryProjects(ctx) {
		if p.RepoPending {
			return true
		}
	}
	for _, server := range h.remoteServerGroups(ctx) {
		for _, p := range server.Projects {
			if p.RepoPending {
				return true
			}
		}
	}
	return false
}
func (h *Handler) repositoryProjects(ctx context.Context) []models.ProjectAPIItem {
	return memoProjectLookup(ctx, func(c *projectLookupCache) map[string][]models.ProjectAPIItem { return c.projects }, func() []models.ProjectAPIItem {
		projects, err := h.projectSvc.ListAPIProjects(ctx)
		if err != nil {
			return nil
		}
		return projects
	})
}
