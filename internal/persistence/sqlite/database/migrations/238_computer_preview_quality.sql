-- +goose Up
ALTER TABLE project_computers ADD COLUMN preview_quality TEXT NOT NULL DEFAULT 'balanced' CHECK(preview_quality IN ('low','balanced','high'));

-- +goose Down
ALTER TABLE project_computers DROP COLUMN preview_quality;
