-- +goose Up
ALTER TABLE automations ADD COLUMN template_revision INTEGER;

-- +goose Down
ALTER TABLE automations DROP COLUMN template_revision;
