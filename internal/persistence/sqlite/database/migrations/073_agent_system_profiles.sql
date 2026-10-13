-- +goose Up
-- Built-in/system agent metadata.

ALTER TABLE agents ADD COLUMN system_kind TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_agents_system_kind ON agents(system_kind);
