-- +goose Up
CREATE INDEX idx_thread_inputs_pending_queued_count
ON thread_inputs(input_status, input_mode)
WHERE input_status = 'pending' AND input_mode = 'queued';

-- +goose Down
DROP INDEX IF EXISTS idx_thread_inputs_pending_queued_count;
