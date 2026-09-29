-- +goose Up
-- Do not replay provider-owned steering whose final delivery is unknown. The
-- provider may already have acted on it, so converting it to an ordinary
-- queued input could repeat tool side effects. These rows stay quarantined and
-- visible for explicit user resolution.
UPDATE thread_input_provider_steering
SET delivery_state = 'accepted_ambiguous',
    updated_at = CURRENT_TIMESTAMP
WHERE delivery_state = 'accepted_pending';

-- +goose Down
SELECT 1;
