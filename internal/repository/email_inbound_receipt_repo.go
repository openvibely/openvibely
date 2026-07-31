package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// EmailInboundReceiptRepo records messages whose durable Email ingress handoff
// completed, allowing IMAP acknowledgement retries without repeating the work.
type EmailInboundReceiptRepo struct {
	db *sql.DB
}

func NewEmailInboundReceiptRepo(db *sql.DB) *EmailInboundReceiptRepo {
	return &EmailInboundReceiptRepo{db: db}
}

func (r *EmailInboundReceiptRepo) Exists(ctx context.Context, mailboxAddress, messageKey string) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM email_inbound_receipts WHERE mailbox_address = ? AND message_key = ?)`,
		mailboxAddress, messageKey,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check email inbound receipt: %w", err)
	}
	return exists, nil
}

func (r *EmailInboundReceiptRepo) Record(ctx context.Context, mailboxAddress, messageKey string) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO email_inbound_receipts (mailbox_address, message_key) VALUES (?, ?)
		 ON CONFLICT(mailbox_address, message_key) DO NOTHING`,
		mailboxAddress, messageKey,
	)
	if err != nil {
		return fmt.Errorf("record email inbound receipt: %w", err)
	}
	return nil
}
