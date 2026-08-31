-- +goose Up
-- Lets a lifecycle hook declare which context blocks its skill reads, the same
-- way it already declares permissions, run policy, and schedule. An empty
-- payload keeps the previous behavior: the hook receives every block the slot
-- produced.
ALTER TABLE agent_lifecycle_hooks ADD COLUMN payload_json TEXT NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE agent_lifecycle_hooks DROP COLUMN payload_json;
