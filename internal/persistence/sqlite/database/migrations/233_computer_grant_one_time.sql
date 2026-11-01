-- +goose Up
ALTER TABLE computer_chrome_grants ADD COLUMN one_time INTEGER NOT NULL DEFAULT 0;
-- +goose Down
ALTER TABLE computer_chrome_grants DROP COLUMN one_time;
