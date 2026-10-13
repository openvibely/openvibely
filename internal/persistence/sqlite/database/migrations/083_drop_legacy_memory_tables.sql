-- +goose Up
-- Memory subsystem responsibilities (recall, extraction, consolidation runs,
-- per-project enable/disable) have moved to the on-disk System: Memory
-- Curator agent and its lifecycle hooks plus a normal scheduled task. These
-- DB tables are no longer referenced by the application.
DROP TABLE IF EXISTS memory_consolidation_runs;
DROP TABLE IF EXISTS memory_extraction_runs;
DROP TABLE IF EXISTS project_memory_settings;

-- +goose Down
-- This migration is irreversible: legacy memory tables previously stored
-- per-project enable/disable flags and historical run rows. The Memory
-- Curator agent does not use that data. If recovery is required, restore
-- from a backup taken before this migration.
SELECT 1;
