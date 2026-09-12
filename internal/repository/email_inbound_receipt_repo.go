package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const emailInboundReceiptBatchSize = 500

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

// ExistsBatch returns the receipt keys already present for one mailbox. Queries
// are deliberately chunked so callers cannot turn a large unread batch into an
// unbounded SQLite statement or exceed SQLite's bind-variable limit.
func (r *EmailInboundReceiptRepo) ExistsBatch(ctx context.Context, mailboxAddress string, messageKeys []string) (map[string]struct{}, error) {
	existing := make(map[string]struct{}, len(messageKeys))
	uniqueKeys := make([]string, 0, len(messageKeys))
	seen := make(map[string]struct{}, len(messageKeys))
	for _, messageKey := range messageKeys {
		if _, ok := seen[messageKey]; ok {
			continue
		}
		seen[messageKey] = struct{}{}
		uniqueKeys = append(uniqueKeys, messageKey)
	}
	for start := 0; start < len(uniqueKeys); start += emailInboundReceiptBatchSize {
		end := start + emailInboundReceiptBatchSize
		if end > len(uniqueKeys) {
			end = len(uniqueKeys)
		}
		chunk := uniqueKeys[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]interface{}, 0, len(chunk)+1)
		args = append(args, mailboxAddress)
		for _, messageKey := range chunk {
			args = append(args, messageKey)
		}
		rows, err := r.db.QueryContext(ctx,
			`SELECT message_key FROM email_inbound_receipts WHERE mailbox_address = ? AND message_key IN (`+placeholders+`)`,
			args...,
		)
		if err != nil {
			return nil, fmt.Errorf("check email inbound receipt batch: %w", err)
		}
		for rows.Next() {
			var messageKey string
			if err := rows.Scan(&messageKey); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan email inbound receipt batch: %w", err)
			}
			existing[messageKey] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("read email inbound receipt batch: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close email inbound receipt batch: %w", err)
		}
	}
	return existing, nil
}

func (r *EmailInboundReceiptRepo) Record(ctx context.Context, mailboxAddress, messageKey string) error {
	_, err := execBoundSQLite(ctx, r.db,
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
