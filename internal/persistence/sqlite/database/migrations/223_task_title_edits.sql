-- +goose Up
-- A manual title save permanently disqualifies a pending automatic title result,
-- even if the user saves the same text or later returns to the provisional title.
ALTER TABLE tasks ADD COLUMN title_explicitly_set INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE tasks DROP COLUMN title_explicitly_set;
