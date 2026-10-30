-- +goose Up
CREATE TABLE users (
 id TEXT PRIMARY KEY,
 username TEXT NOT NULL COLLATE NOCASE UNIQUE,
 display_name TEXT NOT NULL DEFAULT '',
 password_hash TEXT NOT NULL DEFAULT '',
 disabled INTEGER NOT NULL DEFAULT 0 CHECK(disabled IN (0,1)),
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE organizations (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE org_memberships (
 org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('owner','admin','member')),
 PRIMARY KEY(org_id,user_id)
);
CREATE TABLE user_sessions (
 token_hash TEXT PRIMARY KEY,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 expires_at INTEGER NOT NULL
);
CREATE INDEX user_sessions_user ON user_sessions(user_id);
CREATE TABLE org_invitations (
 id TEXT PRIMARY KEY,
 org_id TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 token_hash TEXT NOT NULL UNIQUE,
 role TEXT NOT NULL CHECK(role IN ('admin','member')),
 created_by TEXT NOT NULL REFERENCES users(id),
 expires_at INTEGER NOT NULL,
 used INTEGER NOT NULL DEFAULT 0,
 revoked INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE user_preferences (
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 key TEXT NOT NULL,
 value TEXT NOT NULL,
 PRIMARY KEY(user_id,key)
);
CREATE TABLE user_identities (
 provider TEXT NOT NULL,
 subject TEXT NOT NULL,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 PRIMARY KEY(provider,subject),
 UNIQUE(provider,user_id)
);
INSERT INTO users(id,username,display_name) VALUES('local-owner','local','Owner');
INSERT INTO organizations(id,name) VALUES('default','My Organization');
INSERT INTO org_memberships VALUES('default','local-owner','owner');
INSERT INTO user_preferences(user_id,key,value) SELECT 'local-owner',key,value FROM app_settings WHERE key LIKE 'ui.%';
DELETE FROM app_settings WHERE key LIKE 'ui.%';
-- All projects remain shared within this installation until project ACLs ship.
CREATE TABLE project_organizations (
 project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
 org_id TEXT NOT NULL REFERENCES organizations(id),
 owner_user_id TEXT NOT NULL REFERENCES users(id)
);
INSERT INTO project_organizations SELECT id,'default','local-owner' FROM projects;
-- +goose StatementBegin
CREATE TRIGGER project_default_organization AFTER INSERT ON projects BEGIN
 INSERT INTO project_organizations VALUES(NEW.id,'default','local-owner');
END;
-- +goose StatementEnd

ALTER TABLE tasks ADD COLUMN author_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN author_name TEXT NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN author_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE executions ADD COLUMN author_name TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN author_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE thread_inputs ADD COLUMN author_name TEXT NOT NULL DEFAULT '';


-- +goose Down
ALTER TABLE thread_inputs DROP COLUMN author_name;
ALTER TABLE thread_inputs DROP COLUMN author_user_id;
ALTER TABLE executions DROP COLUMN author_name;
ALTER TABLE executions DROP COLUMN author_user_id;
ALTER TABLE tasks DROP COLUMN author_name;
ALTER TABLE tasks DROP COLUMN author_user_id;

DROP TRIGGER project_default_organization;
DROP TABLE project_organizations;
INSERT OR IGNORE INTO app_settings(key,value) SELECT key,value FROM user_preferences WHERE user_id='local-owner';
DROP TABLE user_identities;
DROP TABLE user_preferences;
DROP TABLE org_invitations;
DROP TABLE user_sessions;
DROP TABLE org_memberships;
DROP TABLE organizations;
DROP TABLE users;
