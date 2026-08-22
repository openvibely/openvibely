package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// SlackInboundReceiptRepo records Slack events whose durable ingress handoff
// completed, allowing Socket Mode redelivery without repeating accepted work.
type SlackInboundReceiptRepo struct {
	db *sql.DB
}

func NewSlackInboundReceiptRepo(db *sql.DB) *SlackInboundReceiptRepo {
	return &SlackInboundReceiptRepo{db: db}
}

func (r *SlackInboundReceiptRepo) Exists(ctx context.Context, eventKey string) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("Slack inbound receipt repository is not configured")
	}
	var exists bool
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM slack_inbound_receipts WHERE event_key = ?)`,
		eventKey,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check Slack inbound receipt: %w", err)
	}
	return exists, nil
}

// WithHandoff atomically records a Slack event receipt with its durable work.
// If the receipt already exists, persist is not called and alreadyHandedOff is true.
func (r *SlackInboundReceiptRepo) WithHandoff(ctx context.Context, eventKey string, persist func(SQLExecutor) error) (alreadyHandedOff bool, err error) {
	var db *sql.DB
	if r != nil {
		db = r.db
	}
	return inboundReceiptHandoff(ctx, db, inboundReceiptHandoffSpec{
		notConfiguredError:   "Slack inbound receipt repository is not configured",
		persistRequiredError: "Slack inbound handoff persistence is required",
		insertSQL: `INSERT INTO slack_inbound_receipts (event_key) VALUES (?)
			 ON CONFLICT(event_key) DO NOTHING`,
		insertArgs:        []interface{}{eventKey},
		recordError:       "record Slack inbound receipt",
		rowsAffectedError: "check Slack inbound receipt insertion",
	}, persist)
}
