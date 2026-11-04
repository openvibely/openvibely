package service

import (
	"context"
	"sync"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// ProjectAccessService owns policy mutations and permission checks. A change
// wakes subscribers so they can reauthorize against current stored membership.
// It does not treat a notification as permission to deliver buffered events.
type ProjectAccessService struct {
	store      persistence.ProjectAccessStore
	authorizer authz.Authorizer
	mu         sync.Mutex
	changed    chan struct{}
}

func NewProjectAccessService(store persistence.ProjectAccessStore) *ProjectAccessService {
	return &ProjectAccessService{store: store, authorizer: authz.Authorizer{Store: store}, changed: make(chan struct{})}
}
func (s *ProjectAccessService) Require(ctx context.Context, id string, p authz.Permission) error {
	return s.authorizer.Require(ctx, id, p)
}
func (s *ProjectAccessService) Changes() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}
func (s *ProjectAccessService) notify() {
	s.mu.Lock()
	defer s.mu.Unlock()
	close(s.changed)
	s.changed = make(chan struct{})
}
func (s *ProjectAccessService) Policy(ctx context.Context, id string) (*models.ProjectAccess, error) {
	if err := s.Require(ctx, id, authz.Manage); err != nil {
		return nil, err
	}
	return s.store.Get(ctx, id)
}
func (s *ProjectAccessService) Members(ctx context.Context, id string) ([]models.ProjectMember, error) {
	if err := s.Require(ctx, id, authz.Manage); err != nil {
		return nil, err
	}
	return s.store.Members(ctx, id)
}
func (s *ProjectAccessService) Update(ctx context.Context, p models.ProjectAccess) error {
	if err := s.Require(ctx, p.ProjectID, authz.Manage); err != nil {
		return err
	}
	// There is deliberately no environment override. A private-project promise
	// requires kernel isolation as well as application authorization.
	if p.Visibility == "private" {
		return authz.ErrSandboxRequired
	}
	actor, _ := models.PrincipalFromContext(ctx)
	if err := s.store.Update(ctx, actor.UserID, p); err != nil {
		return err
	}
	s.notify()
	return nil
}
func (s *ProjectAccessService) SetMember(ctx context.Context, projectID, userID, role string) error {
	if err := s.Require(ctx, projectID, authz.Manage); err != nil {
		return err
	}
	actor, _ := models.PrincipalFromContext(ctx)
	if err := s.store.SetMember(ctx, actor.UserID, projectID, userID, role); err != nil {
		return err
	}
	s.notify()
	return nil
}
func (s *ProjectAccessService) Audit(ctx context.Context, id string) ([]models.ProjectAuditEvent, error) {
	if err := s.Require(ctx, id, authz.Manage); err != nil {
		return nil, err
	}
	return s.store.Audit(ctx, id)
}

// RecordAction records an external action against the project from which it was
// requested. Callers record intent before side effects and outcomes afterward;
// an external provider operation cannot participate in the database transaction.
func (s *ProjectAccessService) RecordAction(ctx context.Context, projectID, action, target, details string) error {
	if err := s.Require(ctx, projectID, authz.Manage); err != nil {
		return err
	}
	actor, _ := models.PrincipalFromContext(ctx)
	return s.store.Record(ctx, projectID, actor.UserID, action, target, details)
}
