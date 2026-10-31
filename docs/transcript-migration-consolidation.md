# Consolidated transcript migration

The branch registers `220_transcript_store.go`. Main owns migrations 217–219.

A fresh database or an upgrade from main applies the complete transcript schema and conversion in one Goose transaction. Downgrade reverses the stages in dependency order: ranges become owned items, metadata is removed, pending stream text becomes display items, references are materialized, then output/replay/checkpoints return to the pre-transcript representation. Unregistered helpers implement those stages and allow tests to exercise them individually.

Startup uses the normal Goose migration path. It does not adopt or rewrite migration records from unpublished intermediate branch builds. Databases from those builds require a pre-upgrade backup or a fresh database.

Tests cover upgrades from main, Unicode output and pending stream text through downgrade, repeated initialization, foreign-key integrity, and individual conversion and rollback stages.
