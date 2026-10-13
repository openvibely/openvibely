-- +goose Up
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_webhook_endpoints_project;
CREATE INDEX IF NOT EXISTS idx_webhook_endpoints_project_name_id
  ON webhook_endpoints(project_id, name, id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_webhook_endpoints_project_name_id;
CREATE INDEX IF NOT EXISTS idx_webhook_endpoints_project
  ON webhook_endpoints(project_id);
-- +goose StatementEnd
