-- +goose Up
CREATE INDEX idx_x_authorized_users_user_project
    ON x_authorized_users(x_user_id, project_id);

-- +goose Down
DROP INDEX idx_x_authorized_users_user_project;
