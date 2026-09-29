-- +goose Up
-- Provider-owned response.steer delivery was replaced by locally owned steering.
-- Make unresolved legacy inputs safe to process as ordinary queued follow-ups.
UPDATE thread_inputs
SET input_mode = 'queued',
    turn_id = NULL,
    expected_turn_id = NULL,
    updated_at = CURRENT_TIMESTAMP
WHERE input_status = 'pending'
  AND input_mode = 'steering'
  AND EXISTS (
      SELECT 1
      FROM thread_input_provider_steering provider_steering
      WHERE provider_steering.thread_input_id = thread_inputs.id
        AND provider_steering.delivery_state IN ('accepted_pending', 'accepted_ambiguous')
  );

DELETE FROM thread_input_provider_steering
WHERE delivery_state IN ('accepted_pending', 'accepted_ambiguous');

-- +goose Down
-- Recovered delivery ambiguity cannot be reconstructed safely.
