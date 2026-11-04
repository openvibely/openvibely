package authz

import (
	"context"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// ProjectStore applies current permissions to all project projections. Requests
// carry a principal, including auth-off requests (the bootstrap owner). Calls
// without a principal are existing internal startup/maintenance operations;
// background execution must separately reconstruct and authorize its run-as user.
type ProjectStore struct {
	store      persistence.ProjectStore
	authorizer *Authorizer
}

func NewProjectStore(store persistence.ProjectStore, a *Authorizer) *ProjectStore {
	return &ProjectStore{store: store, authorizer: a}
}

var _ persistence.ProjectStore = (*ProjectStore)(nil)

func (s *ProjectStore) require(ctx context.Context, id string, p Permission) error {
	if _, ok := models.PrincipalFromContext(ctx); !ok {
		return nil
	}
	return s.authorizer.Require(ctx, id, p)
}
func filterProjects[T any](ctx context.Context, s *ProjectStore, rows []T, err error, id func(T) string) ([]T, error) {
	if err != nil {
		return nil, err
	}
	if _, ok := models.PrincipalFromContext(ctx); !ok {
		return rows, nil
	}
	out := make([]T, 0, len(rows))
	for _, row := range rows {
		err := s.require(ctx, id(row), View)
		if errors.Is(err, ErrDenied) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}
func (s *ProjectStore) List(ctx context.Context) ([]models.Project, error) {
	rows, err := s.store.List(ctx)
	return filterProjects(ctx, s, rows, err, func(p models.Project) string { return p.ID })
}
func (s *ProjectStore) ListSelectorOptions(ctx context.Context) ([]models.Project, error) {
	rows, err := s.store.ListSelectorOptions(ctx)
	return filterProjects(ctx, s, rows, err, func(p models.Project) string { return p.ID })
}
func (s *ProjectStore) ListAPIProjects(ctx context.Context) ([]models.ProjectAPIItem, error) {
	rows, err := s.store.ListAPIProjects(ctx)
	return filterProjects(ctx, s, rows, err, func(p models.ProjectAPIItem) string { return p.ID })
}
func (s *ProjectStore) ListWorkerCapacityProjects(ctx context.Context) ([]models.ProjectWorkerCapacity, error) {
	rows, err := s.store.ListWorkerCapacityProjects(ctx)
	return filterProjects(ctx, s, rows, err, func(p models.ProjectWorkerCapacity) string { return p.ID })
}
func (s *ProjectStore) ListRepoValidationProjects(ctx context.Context) ([]persistence.ProjectRepoValidationProject, error) {
	rows, err := s.store.ListRepoValidationProjects(ctx)
	return filterProjects(ctx, s, rows, err, func(p persistence.ProjectRepoValidationProject) string { return p.ID })
}
func (s *ProjectStore) ForEachRepoRoot(ctx context.Context, visit func(persistence.ProjectRepoRoot)) error {
	if _, ok := models.PrincipalFromContext(ctx); !ok {
		return s.store.ForEachRepoRoot(ctx, visit)
	}
	var rows []persistence.ProjectRepoRoot
	err := s.store.ForEachRepoRoot(ctx, func(p persistence.ProjectRepoRoot) { rows = append(rows, p) })
	rows, err = filterProjects(ctx, s, rows, err, func(p persistence.ProjectRepoRoot) string { return p.ID })
	if err != nil {
		return err
	}
	for _, row := range rows {
		visit(row)
	}
	return nil
}
func (s *ProjectStore) GetByID(ctx context.Context, id string) (*models.Project, error) {
	if err := s.require(ctx, id, View); err != nil {
		return nil, err
	}
	return s.store.GetByID(ctx, id)
}
func (s *ProjectStore) GetIdentityByID(ctx context.Context, id string) (*models.ProjectIdentity, error) {
	if err := s.require(ctx, id, View); err != nil {
		return nil, err
	}
	return s.store.GetIdentityByID(ctx, id)
}
func (s *ProjectStore) GetDefaultAgentConfigID(ctx context.Context, id string) (*string, error) {
	if err := s.require(ctx, id, View); err != nil {
		return nil, err
	}
	return s.store.GetDefaultAgentConfigID(ctx, id)
}
func (s *ProjectStore) GetManagedPermissions(ctx context.Context, id string) (models.SkillMemoryPermissions, error) {
	if err := s.require(ctx, id, View); err != nil {
		return models.SkillMemoryPermissions{}, err
	}
	return s.store.GetManagedPermissions(ctx, id)
}
func (s *ProjectStore) Create(ctx context.Context, p *models.Project) error {
	// The underlying store validates current org membership and assigns the creator
	// atomically with insertion. There is no project to authorize before creation.
	return s.store.Create(ctx, p)
}
func (s *ProjectStore) Update(ctx context.Context, p *models.Project) error {
	if err := s.require(ctx, p.ID, Manage); err != nil {
		return err
	}
	return s.store.Update(ctx, p)
}
func (s *ProjectStore) DeleteWithCleanupManifest(ctx context.Context, id string, before func(persistence.TaskDeletionManifest) error) (persistence.TaskDeletionManifest, bool, error) {
	if err := s.require(ctx, id, Own); err != nil {
		return persistence.TaskDeletionManifest{}, false, err
	}
	return s.store.DeleteWithCleanupManifest(ctx, id, before)
}
