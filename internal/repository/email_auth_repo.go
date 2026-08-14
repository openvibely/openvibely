package repository

import (
	"context"
	"database/sql"
	netmail "net/mail"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// EmailAuthRepo handles database operations for Email authorized senders.
type EmailAuthRepo struct {
	allowlist singleIdentifierAllowlist[models.EmailAuthorizedSender]
}

// NewEmailAuthRepo creates a new EmailAuthRepo.
func NewEmailAuthRepo(db *sql.DB) *EmailAuthRepo {
	return &EmailAuthRepo{allowlist: singleIdentifierAllowlist[models.EmailAuthorizedSender]{
		db:                          db,
		table:                       "email_authorized_senders",
		identityColumn:              "email_address",
		conflictTarget:              "lower(email_address)",
		matchClause:                 `lower(email_address) = lower(?)`,
		listErrLabel:                "email authorized senders",
		scanErrLabel:                "email authorized sender",
		getErrLabel:                 "get email authorized sender",
		deleteEntityLabel:           "email authorized sender",
		countAnyErrLabel:            "email authorized senders anywhere",
		checkAnywhereErrLabel:       "check email authorization anywhere",
		checkProjectErrLabel:        "check email project authorization",
		updateAddedByOnEmptyDisplay: false,
		normalize:                   NormalizeEmailAddress,
		scan: func(scanner taskContextScanner) (models.EmailAuthorizedSender, error) {
			var s models.EmailAuthorizedSender
			err := scanner.Scan(&s.ID, &s.ProjectID, &s.EmailAddress, &s.DisplayName, &s.AddedAt, &s.AddedBy)
			return s, err
		},
	}}
}

// NormalizeEmailAddress extracts the mailbox address when possible, then trims and lowercases it for storage and matching.
func NormalizeEmailAddress(email string) string {
	email = strings.TrimSpace(email)
	if email == "" {
		return ""
	}
	if addr, err := netmail.ParseAddress(email); err == nil && addr != nil && strings.TrimSpace(addr.Address) != "" {
		email = addr.Address
	}
	return strings.ToLower(strings.TrimSpace(email))
}

// ListByProject returns all system-level authorized email senders.
// projectID is accepted for UI compatibility but does not scope inbound authorization.
func (r *EmailAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.EmailAuthorizedSender, error) {
	return r.allowlist.List(ctx)
}

// IsAuthorized checks whether an email address is authorized at the system channel level.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *EmailAuthRepo) IsAuthorized(ctx context.Context, projectID, emailAddress string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, emailAddress)
}

// IsAuthorizedForProject checks the legacy project-scoped row ownership for diagnostics/tests.
func (r *EmailAuthRepo) IsAuthorizedForProject(ctx context.Context, projectID, emailAddress string) (bool, error) {
	return r.allowlist.IsAuthorizedForProject(ctx, projectID, emailAddress)
}

// HasAnyAuthorizedUsers checks whether any system-level email authorized senders are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *EmailAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return r.HasAnyAuthorizedUsersAnywhere(ctx)
}

// HasAnyAuthorizedUsersAnywhere checks whether any project has email authorized senders configured.
func (r *EmailAuthRepo) HasAnyAuthorizedUsersAnywhere(ctx context.Context) (bool, error) {
	return r.allowlist.HasAny(ctx)
}

// IsAuthorizedAnywhere checks whether an email address is authorized in any project.
func (r *EmailAuthRepo) IsAuthorizedAnywhere(ctx context.Context, emailAddress string) (bool, error) {
	return r.allowlist.IsAuthorizedAnywhere(ctx, emailAddress)
}

// Create adds a system-level authorized email sender.
func (r *EmailAuthRepo) Create(ctx context.Context, s *models.EmailAuthorizedSender) error {
	identity, id, addedAt, err := r.allowlist.Create(ctx, s.ProjectID, s.EmailAddress, s.DisplayName, s.AddedBy)
	if err != nil {
		return err
	}
	s.EmailAddress = identity
	s.ID = id
	s.AddedAt = addedAt
	return nil
}

// Delete removes an authorized email sender by ID.
func (r *EmailAuthRepo) Delete(ctx context.Context, id string) error {
	return r.allowlist.Delete(ctx, id)
}

// GetByID returns a single authorized email sender by ID.
func (r *EmailAuthRepo) GetByID(ctx context.Context, id string) (*models.EmailAuthorizedSender, error) {
	return r.allowlist.GetByID(ctx, id)
}
