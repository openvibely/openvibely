-- +goose Up
-- Forward guard for databases that recorded migration 202 but still retain
-- provider-delivery receipts. Provider-owned steering is never replayed
-- automatically because the provider may already have acted on it. Receipts
-- deleted by an older migration draft cannot be reconstructed safely.
UPDATE thread_input_provider_steering
SET delivery_state = 'accepted_ambiguous',
    updated_at = CURRENT_TIMESTAMP
WHERE delivery_state = 'accepted_pending';

-- +goose Down
SELECT 1;
