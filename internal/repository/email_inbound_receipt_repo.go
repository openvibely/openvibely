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

// WithHandoff atomically records an inbound message receipt with the durable
// work created for that message. If the receipt already exists, persist is not
// called and alreadyHandedOff is true.
func (r *EmailInboundReceiptRepo) WithHandoff(ctx context.Context, mailboxAddress, messageKey string, persist func(SQLExecutor) error) (alreadyHandedOff bool, err error) {
	var db *sql.DB
	if r != nil {
		db = r.db
	}
	return inboundReceiptHandoff(ctx, db, inboundReceiptHandoffSpec{
		notConfiguredError:   "email inbound receipt repository is not configured",
		persistRequiredError: "email inbound handoff persistence is required",
		insertSQL: `INSERT INTO email_inbound_receipts (mailbox_address, message_key) VALUES (?, ?)
			 ON CONFLICT(mailbox_address, message_key) DO NOTHING`,
		insertArgs:        []interface{}{mailboxAddress, messageKey},
		recordError:       "record email inbound receipt",
		rowsAffectedError: "check email inbound receipt insertion",
	}, persist)
}
