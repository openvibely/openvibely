-- +goose Up
-- Pending attachment directories are removed outside SQLite so deletion does not
-- hold the database connection during filesystem work. Retiring a session inside
-- the durable transaction prevents a later thread input from taking ownership in
-- the commit-to-filesystem-cleanup gap.
CREATE TABLE retired_attachment_sessions (
    session_id TEXT PRIMARY KEY,
    retired_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose StatementBegin
CREATE TRIGGER reject_retired_attachment_session_insert
BEFORE INSERT ON thread_inputs
WHEN NEW.attachment_session_id IS NOT NULL
 AND NEW.attachment_session_id <> ''
 AND EXISTS (
    SELECT 1 FROM retired_attachment_sessions retired
    WHERE retired.session_id = NEW.attachment_session_id
 )
BEGIN
    SELECT RAISE(ABORT, 'attachment session retired');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER reject_retired_attachment_session_update
BEFORE UPDATE OF attachment_session_id ON thread_inputs
WHEN NEW.attachment_session_id IS NOT NULL
 AND NEW.attachment_session_id <> ''
 AND EXISTS (
    SELECT 1 FROM retired_attachment_sessions retired
    WHERE retired.session_id = NEW.attachment_session_id
 )
BEGIN
    SELECT RAISE(ABORT, 'attachment session retired');
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS reject_retired_attachment_session_update;
DROP TRIGGER IF EXISTS reject_retired_attachment_session_insert;
DROP TABLE IF EXISTS retired_attachment_sessions;
