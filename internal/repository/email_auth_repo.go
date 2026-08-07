package repository

import (
	"context"
	"database/sql"
	"fmt"
	netmail "net/mail"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// EmailAuthRepo handles database operations for Email authorized senders.
type EmailAuthRepo struct {
	db *sql.DB
}

// NewEmailAuthRepo creates a new EmailAuthRepo.
func NewEmailAuthRepo(db *sql.DB) *EmailAuthRepo {
	return &EmailAuthRepo{db: db}
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
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, email_address, display_name, added_at, added_by
		 FROM email_authorized_senders
		 ORDER BY added_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list email authorized senders: %w", err)
	}
	defer rows.Close()

	var senders []models.EmailAuthorizedSender
	for rows.Next() {
		var s models.EmailAuthorizedSender
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.EmailAddress, &s.DisplayName, &s.AddedAt, &s.AddedBy); err != nil {
			return nil, fmt.Errorf("scan email authorized sender: %w", err)
		}
		senders = append(senders, s)
	}
	return senders, rows.Err()
}

// IsAuthorized checks whether an email address is authorized at the system channel level.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *EmailAuthRepo) IsAuthorized(ctx context.Context, projectID, emailAddress string) (bool, error) {
	return r.IsAuthorizedAnywhere(ctx, emailAddress)
}

// IsAuthorizedForProject checks the legacy project-scoped row ownership for diagnostics/tests.
func (r *EmailAuthRepo) IsAuthorizedForProject(ctx context.Context, projectID, emailAddress string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM email_authorized_senders
		 WHERE project_id = ? AND lower(email_address) = lower(?)`,
		projectID, NormalizeEmailAddress(emailAddress)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check email project authorization: %w", err)
	}
	return count > 0, nil
}

// HasAnyAuthorizedUsers checks whether any system-level email authorized senders are configured.
// projectID is accepted for compatibility but does not scope inbound authorization.
func (r *EmailAuthRepo) HasAnyAuthorizedUsers(ctx context.Context, projectID string) (bool, error) {
	return r.HasAnyAuthorizedUsersAnywhere(ctx)
}

// HasAnyAuthorizedUsersAnywhere checks whether any project has email authorized senders configured.
func (r *EmailAuthRepo) HasAnyAuthorizedUsersAnywhere(ctx context.Context) (bool, error) {
	return countAny(ctx, r.db, "email_authorized_senders", "email authorized senders anywhere")
}

// IsAuthorizedAnywhere checks whether an email address is authorized in any project.
func (r *EmailAuthRepo) IsAuthorizedAnywhere(ctx context.Context, emailAddress string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM email_authorized_senders WHERE lower(email_address) = lower(?)`,
		NormalizeEmailAddress(emailAddress)).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check email authorization anywhere: %w", err)
	}
	return count > 0, nil
}

// Create adds a system-level authorized email sender.
func (r *EmailAuthRepo) Create(ctx context.Context, s *models.EmailAuthorizedSender) error {
	s.EmailAddress = NormalizeEmailAddress(s.EmailAddress)
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO email_authorized_senders (project_id, email_address, display_name, added_by)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT DO NOTHING
		 RETURNING id, added_at`,
		s.ProjectID, s.EmailAddress, s.DisplayName, s.AddedBy).
		Scan(&s.ID, &s.AddedAt)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	if s.DisplayName != "" {
		if _, updateErr := r.db.ExecContext(ctx, `UPDATE email_authorized_senders SET display_name = ?, added_by = ? WHERE lower(email_address) = lower(?)`, s.DisplayName, s.AddedBy, s.EmailAddress); updateErr != nil {
			return updateErr
		}
	}
	return r.db.QueryRowContext(ctx,
		`SELECT id, added_at FROM email_authorized_senders WHERE lower(email_address) = lower(?)`,
		s.EmailAddress).Scan(&s.ID, &s.AddedAt)
}

// Delete removes an authorized email sender by ID.
func (r *EmailAuthRepo) Delete(ctx context.Context, id string) error {
	return deleteByID(ctx, r.db, "email_authorized_senders", "email authorized sender", id)
}

// GetByID returns a single authorized email sender by ID.
func (r *EmailAuthRepo) GetByID(ctx context.Context, id string) (*models.EmailAuthorizedSender, error) {
	var s models.EmailAuthorizedSender
	err := r.db.QueryRowContext(ctx,
		`SELECT id, project_id, email_address, display_name, added_at, added_by
		 FROM email_authorized_senders WHERE id = ?`, id).
		Scan(&s.ID, &s.ProjectID, &s.EmailAddress, &s.DisplayName, &s.AddedAt, &s.AddedBy)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get email authorized sender: %w", err)
	}
	return &s, nil
}
