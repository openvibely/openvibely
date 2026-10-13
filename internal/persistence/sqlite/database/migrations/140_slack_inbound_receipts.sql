-- +goose Up
CREATE TABLE slack_inbound_receipts (
    event_key TEXT PRIMARY KEY,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS slack_inbound_receipts;
