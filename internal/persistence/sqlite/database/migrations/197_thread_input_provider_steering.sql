-- +goose Up
CREATE TABLE thread_input_provider_steering (
    thread_input_id      TEXT PRIMARY KEY,
    steering_id         TEXT NOT NULL,
    previous_response_id TEXT NOT NULL DEFAULT '',
    response_id         TEXT NOT NULL DEFAULT '',
    delivery_state      TEXT NOT NULL CHECK (delivery_state IN ('accepted_pending', 'accepted_ambiguous', 'accepted_confirmed')),
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (thread_input_id) REFERENCES thread_inputs(id) ON DELETE CASCADE
);

CREATE INDEX idx_thread_input_provider_steering_id
    ON thread_input_provider_steering(steering_id, delivery_state);

-- +goose Down
DROP INDEX IF EXISTS idx_thread_input_provider_steering_id;
DROP TABLE IF EXISTS thread_input_provider_steering;
