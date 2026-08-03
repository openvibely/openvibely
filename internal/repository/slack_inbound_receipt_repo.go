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
	if r == nil || r.db == nil {
		return false, fmt.Errorf("Slack inbound receipt repository is not configured")
	}
	if persist == nil {
		return false, fmt.Errorf("Slack inbound handoff persistence is required")
	}
	threadRepo := NewThreadInputRepo(r.db)
	err = threadRepo.WithImmediateTx(ctx, func(exec SQLExecutor) error {
		result, err := exec.ExecContext(ctx,
			`INSERT INTO slack_inbound_receipts (event_key) VALUES (?)
			 ON CONFLICT(event_key) DO NOTHING`,
			eventKey,
		)
		if err != nil {
			return fmt.Errorf("record Slack inbound receipt: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check Slack inbound receipt insertion: %w", err)
		}
		if inserted == 0 {
			alreadyHandedOff = true
			return nil
		}
		return persist(exec)
	})
	return alreadyHandedOff, err
}
