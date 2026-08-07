package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// EmailSenderProjectRepo persists active project selection per email sender.
type EmailSenderProjectRepo struct {
	db *sql.DB
}

func NewEmailSenderProjectRepo(db *sql.DB) *EmailSenderProjectRepo {
	return &EmailSenderProjectRepo{db: db}
}

// SetSenderProject writes the active project selection for a normalized sender email.
func (r *EmailSenderProjectRepo) SetSenderProject(ctx context.Context, emailAddress, projectID string) error {
	emailAddress = NormalizeEmailAddress(emailAddress)
	if emailAddress == "" {
		return fmt.Errorf("set email sender project: email address is required")
	}
	return upsertUserProject(ctx, r.db, "email_sender_projects", "email_address", emailAddress, projectID, "email sender project")
}

// GetSenderProject returns the active project ID for a normalized sender email, or "" if not set.
func (r *EmailSenderProjectRepo) GetSenderProject(ctx context.Context, emailAddress string) (string, error) {
	emailAddress = NormalizeEmailAddress(emailAddress)
	return getUserProject(ctx, r.db, "email_sender_projects", "email_address", emailAddress, "email sender project")
}

// DeleteSenderProject removes the active project selection for a normalized sender email.
func (r *EmailSenderProjectRepo) DeleteSenderProject(ctx context.Context, emailAddress string) error {
	emailAddress = NormalizeEmailAddress(emailAddress)
	return deleteUserProject(ctx, r.db, "email_sender_projects", "email_address", emailAddress, "email sender project")
}
