-- +goose Up
CREATE TABLE email_inbound_receipts (
    mailbox_address TEXT NOT NULL,
    message_key TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (mailbox_address, message_key)
);

-- +goose Down
DROP TABLE IF EXISTS email_inbound_receipts;
