-- +goose Up
-- Reserved storage owner for the cross-project conversation. It is excluded
-- from project catalogs and can only own internal chat tasks.
INSERT INTO projects(id, name, description) VALUES('__all_projects__', 'All projects', 'Cross-project chat history');
-- +goose StatementBegin
CREATE TRIGGER all_projects_chat_only_insert BEFORE INSERT ON tasks
WHEN NEW.project_id = '__all_projects__' AND NEW.category != 'chat'
BEGIN SELECT RAISE(ABORT, 'All projects can only own chat history'); END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER all_projects_chat_only_update BEFORE UPDATE OF project_id, category ON tasks
WHEN NEW.project_id = '__all_projects__' AND NEW.category != 'chat'
BEGIN SELECT RAISE(ABORT, 'All projects can only own chat history'); END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER all_projects_chat_only_update;
DROP TRIGGER all_projects_chat_only_insert;
DELETE FROM projects WHERE id = '__all_projects__';
