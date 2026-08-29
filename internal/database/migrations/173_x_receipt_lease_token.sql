-- +goose Up
ALTER TABLE x_inbound_receipts ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';

-- Existing processing leases predate ownership tokens and cannot safely commit
-- durable handoff work. Expire them so the next poll reclaims them with a token.
UPDATE x_inbound_receipts
SET lease_expires_at = datetime('now', '-1 second')
WHERE status = 'processing';

-- +goose Down
ALTER TABLE x_inbound_receipts DROP COLUMN lease_token;
