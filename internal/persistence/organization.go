package persistence

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrOrgDenied = errors.New("organization action is not allowed")
var ErrInvitationInvalid = errors.New("invitation is invalid, expired, or already used")
var ErrAccountExists = errors.New("username is unavailable; sign in to join with an existing account")
var ErrLastOwner = errors.New("the last owner cannot be removed or demoted")

// OrganizationStore keeps membership changes and invitation consumption atomic.
// Actor IDs are verified against persisted membership inside each mutation.
type OrganizationStore interface {
	Bootstrap(context.Context, string, string) error
	Organization(context.Context) (*models.Organization, error)
	RenameOrganization(context.Context, string, string) error
	UserByID(context.Context, string) (*models.OrgUser, error)
	UserByUsername(context.Context, string) (*models.OrgUser, error)
	Members(context.Context) ([]models.OrgUser, error)
	ChangeMember(ctx context.Context, actorID, userID, role string, remove bool) error
	CreateInvitation(ctx context.Context, actorID, id, tokenHash, role string, expiresAt int64) error
	Invitations(context.Context) ([]models.OrgInvitation, error)
	RevokeInvitation(ctx context.Context, actorID, id string) error
	AcceptInvitation(ctx context.Context, tokenHash string, now int64, newUser models.OrgUser, existingUserID string) (*models.OrgUser, error)
	CreateSession(ctx context.Context, tokenHash, userID string, expiresAt int64) error
	SessionUser(ctx context.Context, tokenHash string, now int64) (*models.OrgUser, error)
	RevokeSession(context.Context, string) error
	BindHostedOwner(ctx context.Context, subject string) (*models.OrgUser, error)
	Preferences(ctx context.Context, userID string, keys []string) (map[string]string, error)
	SetPreferences(ctx context.Context, userID string, values map[string]string) error
	ComparePreferences(ctx context.Context, userID string, expected, values, guards map[string]string) (bool, error)
}
