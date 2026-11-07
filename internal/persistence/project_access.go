package persistence

import (
	"context"
	"errors"

	"github.com/openvibely/openvibely/internal/models"
)

// ErrProjectDenied deliberately does not distinguish a missing project from a denied one.
var ErrProjectDenied = errors.New("project not found")

// ProjectAccessStore resolves roles from current membership, never client claims.
// Policy changes and their audit event are committed in the same transaction.
type ProjectAccessStore interface {
	ChatResourceAllowed(ctx context.Context, kind, id string) error
	ChatResourceOwner(ctx context.Context, kind, id string) (string, error)
	Get(context.Context, string) (*models.ProjectAccess, error)
	Role(ctx context.Context, userID, projectID string) (string, error)
	Members(context.Context, string) ([]models.ProjectMember, error)
	Update(ctx context.Context, actorID string, policy models.ProjectAccess) error
	SetMember(ctx context.Context, actorID, projectID, userID, role string) error
	Audit(context.Context, string) ([]models.ProjectAuditEvent, error)
	Record(ctx context.Context, projectID, actorID, action, targetID, details string) error
}
