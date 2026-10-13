-- +goose Up
-- Structured per-agent tool configuration for parameterized capabilities such as ScopedFiles.

ALTER TABLE agents ADD COLUMN tool_config TEXT NOT NULL DEFAULT '{}';

-- +goose Down
-- SQLite cannot drop columns portably without table rebuild; leave column in place.
