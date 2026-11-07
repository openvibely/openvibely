-- +goose Up

ALTER TABLE thread_inputs ADD COLUMN telegram_user_id INTEGER NOT NULL DEFAULT 0;

-- Channel identities are global. Keep original rows for migration review.

CREATE TABLE channel_sender_migration_review (channel TEXT NOT NULL, original_id TEXT NOT NULL, details TEXT NOT NULL, PRIMARY KEY(channel,original_id));

CREATE TABLE channel_sender_audit (id INTEGER PRIMARY KEY, channel TEXT NOT NULL, sender_id TEXT NOT NULL, actor_user_id TEXT NOT NULL, action TEXT NOT NULL, before_user_id TEXT NOT NULL, after_user_id TEXT NOT NULL, created_at DATETIME NOT NULL DEFAULT (datetime('now')));

CREATE TEMP TABLE old_telegram AS SELECT *, CASE WHEN telegram_user_id != 0 THEN 'id:' || telegram_user_id ELSE 'name:' || lower(telegram_username) END AS identity_key FROM telegram_authorized_users;

INSERT INTO channel_sender_migration_review SELECT 'telegram',id,json_object('project_id',project_id,'identity',identity_key,'added_by',added_by) FROM old_telegram;

DROP TABLE telegram_authorized_users;

CREATE TABLE telegram_authorized_users (
 id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
 project_id TEXT NOT NULL DEFAULT '',
 telegram_user_id INTEGER NOT NULL,
 telegram_username TEXT NOT NULL DEFAULT '',
 display_name TEXT NOT NULL DEFAULT '', added_at DATETIME NOT NULL DEFAULT (datetime('now')), added_by TEXT NOT NULL DEFAULT 'web',
 application_user_id TEXT NOT NULL DEFAULT '', mapping_review INTEGER NOT NULL DEFAULT 0,
 team_id TEXT NOT NULL DEFAULT ''
);

INSERT INTO telegram_authorized_users (id,telegram_user_id,telegram_username,display_name,added_at,added_by,mapping_review) SELECT id,telegram_user_id,telegram_username,display_name,added_at,added_by,1 FROM (SELECT *,row_number() OVER(PARTITION BY identity_key ORDER BY added_at,id) AS rn FROM old_telegram) WHERE rn=1;

DROP TABLE old_telegram;

CREATE UNIQUE INDEX idx_telegram_auth_unique_user_id ON telegram_authorized_users(telegram_user_id) WHERE telegram_user_id != 0;

CREATE UNIQUE INDEX idx_telegram_auth_unique_username ON telegram_authorized_users(lower(telegram_username)) WHERE telegram_user_id=0 AND telegram_username<>'';

CREATE TEMP TABLE old_slack AS SELECT *, slack_user_id AS identity_key FROM slack_authorized_users;

CREATE TEMP TABLE old_slack_bindings AS SELECT s.*,a.identity_key FROM slack_sender_identities s JOIN old_slack a ON a.id=s.allowlist_id;

INSERT INTO channel_sender_migration_review SELECT 'slack',id,json_object('project_id',project_id,'identity',identity_key,'added_by',added_by) FROM old_slack;

DROP TABLE slack_authorized_users;

CREATE TABLE slack_authorized_users (
 id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
 project_id TEXT NOT NULL DEFAULT '',
 slack_user_id TEXT NOT NULL,
 
 display_name TEXT NOT NULL DEFAULT '', added_at DATETIME NOT NULL DEFAULT (datetime('now')), added_by TEXT NOT NULL DEFAULT 'web',
 application_user_id TEXT NOT NULL DEFAULT '', mapping_review INTEGER NOT NULL DEFAULT 0,
 team_id TEXT NOT NULL DEFAULT ''
);

INSERT INTO slack_authorized_users (id,slack_user_id,display_name,added_at,added_by,mapping_review) SELECT id,slack_user_id,display_name,added_at,added_by,1 FROM (SELECT *,row_number() OVER(PARTITION BY identity_key ORDER BY added_at,id) AS rn FROM old_slack) WHERE rn=1;

UPDATE slack_authorized_users SET application_user_id=COALESCE((SELECT min(application_user_id) FROM old_slack_bindings WHERE identity_key=slack_authorized_users.slack_user_id GROUP BY identity_key HAVING count(DISTINCT application_user_id)=1 AND count(DISTINCT team_id)=1), '');

UPDATE slack_authorized_users SET team_id=COALESCE((SELECT min(team_id) FROM old_slack_bindings WHERE identity_key=slack_authorized_users.slack_user_id GROUP BY identity_key HAVING count(DISTINCT team_id)=1),'');

UPDATE slack_authorized_users SET mapping_review=0 WHERE application_user_id<>'';

INSERT INTO slack_sender_identities(allowlist_id,team_id,application_user_id,verified_by) SELECT a.id,a.team_id,a.application_user_id,min(b.verified_by) FROM slack_authorized_users a JOIN old_slack_bindings b ON b.identity_key=a.slack_user_id WHERE a.application_user_id<>'' GROUP BY a.id;

UPDATE channel_sender_migration_review SET details=json_set(details,'$.bindings',json(COALESCE((SELECT json_group_array(json_object('user_id',application_user_id,'verified_by',verified_by)) FROM old_slack_bindings WHERE allowlist_id=original_id),'[]'))) WHERE channel='slack';

DROP TABLE old_slack_bindings;

DROP TABLE old_slack;

CREATE UNIQUE INDEX idx_slack_auth_global_identity ON slack_authorized_users(slack_user_id);

CREATE TEMP TABLE old_discord AS SELECT *, discord_user_id AS identity_key FROM discord_authorized_users;

CREATE TEMP TABLE old_discord_bindings AS SELECT s.*,a.identity_key FROM discord_sender_identities s JOIN old_discord a ON a.id=s.allowlist_id;

INSERT INTO channel_sender_migration_review SELECT 'discord',id,json_object('project_id',project_id,'identity',identity_key,'added_by',added_by) FROM old_discord;

DROP TABLE discord_authorized_users;

CREATE TABLE discord_authorized_users (
 id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
 project_id TEXT NOT NULL DEFAULT '',
 discord_user_id TEXT NOT NULL,
 
 display_name TEXT NOT NULL DEFAULT '', added_at DATETIME NOT NULL DEFAULT (datetime('now')), added_by TEXT NOT NULL DEFAULT 'web',
 application_user_id TEXT NOT NULL DEFAULT '', mapping_review INTEGER NOT NULL DEFAULT 0,
 team_id TEXT NOT NULL DEFAULT ''
);

INSERT INTO discord_authorized_users (id,discord_user_id,display_name,added_at,added_by,mapping_review) SELECT id,discord_user_id,display_name,added_at,added_by,1 FROM (SELECT *,row_number() OVER(PARTITION BY identity_key ORDER BY added_at,id) AS rn FROM old_discord) WHERE rn=1;

UPDATE discord_authorized_users SET application_user_id=COALESCE((SELECT min(application_user_id) FROM old_discord_bindings WHERE identity_key=discord_authorized_users.discord_user_id GROUP BY identity_key HAVING count(DISTINCT application_user_id)=1 ), '');

UPDATE discord_authorized_users SET mapping_review=0 WHERE application_user_id<>'';

INSERT INTO discord_sender_identities(allowlist_id,application_user_id,verified_by) SELECT a.id,a.application_user_id,min(b.verified_by) FROM discord_authorized_users a JOIN old_discord_bindings b ON b.identity_key=a.discord_user_id WHERE a.application_user_id<>'' GROUP BY a.id;

UPDATE channel_sender_migration_review SET details=json_set(details,'$.bindings',json(COALESCE((SELECT json_group_array(json_object('user_id',application_user_id,'verified_by',verified_by)) FROM old_discord_bindings WHERE allowlist_id=original_id),'[]'))) WHERE channel='discord';

DROP TABLE old_discord_bindings;

DROP TABLE old_discord;

CREATE UNIQUE INDEX idx_discord_auth_global_identity ON discord_authorized_users(discord_user_id);

CREATE TEMP TABLE old_email AS SELECT *, lower(email_address) AS identity_key FROM email_authorized_senders;

INSERT INTO channel_sender_migration_review SELECT 'email',id,json_object('project_id',project_id,'identity',identity_key,'added_by',added_by) FROM old_email;

DROP TABLE email_authorized_senders;

CREATE TABLE email_authorized_senders (
 id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
 project_id TEXT NOT NULL DEFAULT '',
 email_address TEXT NOT NULL,
 
 display_name TEXT NOT NULL DEFAULT '', added_at DATETIME NOT NULL DEFAULT (datetime('now')), added_by TEXT NOT NULL DEFAULT 'web',
 application_user_id TEXT NOT NULL DEFAULT '', mapping_review INTEGER NOT NULL DEFAULT 0,
 team_id TEXT NOT NULL DEFAULT ''
);

INSERT INTO email_authorized_senders (id,email_address,display_name,added_at,added_by,mapping_review) SELECT id,email_address,display_name,added_at,added_by,1 FROM (SELECT *,row_number() OVER(PARTITION BY identity_key ORDER BY added_at,id) AS rn FROM old_email) WHERE rn=1;

DROP TABLE old_email;

CREATE UNIQUE INDEX idx_email_auth_global_identity ON email_authorized_senders(lower(email_address));

CREATE TABLE telegram_task_provenance (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
 external_user_id INTEGER NOT NULL,
 user_id TEXT NOT NULL REFERENCES users(id)
);

CREATE INDEX idx_telegram_auth_user ON telegram_authorized_users(telegram_user_id);
CREATE INDEX idx_telegram_auth_unknown_username ON telegram_authorized_users(lower(telegram_username)) WHERE telegram_user_id=0 AND telegram_username<>'';



CREATE INDEX IF NOT EXISTS idx_telegram_auth_list_covering
  ON telegram_authorized_users(added_at, id, project_id, telegram_user_id, telegram_username, display_name, added_by, application_user_id, mapping_review, team_id);
CREATE INDEX IF NOT EXISTS idx_slack_auth_list_covering
  ON slack_authorized_users(added_at, id, project_id, slack_user_id, display_name, added_by, application_user_id, mapping_review, team_id);
CREATE INDEX IF NOT EXISTS idx_discord_auth_list_covering
  ON discord_authorized_users(added_at, id, project_id, discord_user_id, display_name, added_by, application_user_id, mapping_review, team_id);
CREATE INDEX IF NOT EXISTS idx_email_auth_list_covering
  ON email_authorized_senders(added_at, id, project_id, email_address, display_name, added_by, application_user_id, mapping_review, team_id);



CREATE INDEX idx_telegram_auth_username_lower_unknown_id ON telegram_authorized_users(telegram_user_id, lower(telegram_username)) WHERE telegram_user_id=0 AND telegram_username != '';

-- +goose Down
-- Downgrading places global senders in the default project; global mapping audits are removed.
CREATE TEMP TABLE downgrade_telegram AS SELECT * FROM telegram_authorized_users;
DROP TABLE telegram_authorized_users;
CREATE TABLE "telegram_authorized_users" (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    telegram_user_id INTEGER NOT NULL,
    telegram_username TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web'
);
CREATE UNIQUE INDEX idx_telegram_auth_unique_user_id
    ON telegram_authorized_users(project_id, telegram_user_id)
    WHERE telegram_user_id != 0;
CREATE UNIQUE INDEX idx_telegram_auth_unique_username
    ON telegram_authorized_users(project_id, telegram_username)
    WHERE telegram_username != '';
CREATE INDEX idx_telegram_auth_project ON telegram_authorized_users(project_id);
CREATE INDEX idx_telegram_auth_user ON telegram_authorized_users(telegram_user_id);
INSERT INTO telegram_authorized_users(id,project_id,telegram_user_id,telegram_username,display_name,added_at,added_by) SELECT id,'default',telegram_user_id,telegram_username,display_name,added_at,added_by FROM downgrade_telegram;
DROP TABLE downgrade_telegram;
CREATE TEMP TABLE downgrade_slack AS SELECT * FROM slack_authorized_users;
CREATE TEMP TABLE downgrade_slack_bindings AS SELECT * FROM slack_sender_identities;
DROP TABLE slack_authorized_users;
CREATE TABLE slack_authorized_users (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    slack_user_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web'
);
CREATE UNIQUE INDEX idx_slack_auth_unique_user_id ON slack_authorized_users(project_id, slack_user_id);
CREATE INDEX idx_slack_auth_project ON slack_authorized_users(project_id);
CREATE INDEX idx_slack_auth_user ON slack_authorized_users(slack_user_id);
INSERT INTO slack_authorized_users(id,project_id,slack_user_id,display_name,added_at,added_by) SELECT id,'default',slack_user_id,display_name,added_at,added_by FROM downgrade_slack;
DROP TABLE downgrade_slack;
INSERT INTO slack_sender_identities SELECT * FROM downgrade_slack_bindings;
DROP TABLE downgrade_slack_bindings;
CREATE TEMP TABLE downgrade_discord AS SELECT * FROM discord_authorized_users;
CREATE TEMP TABLE downgrade_discord_bindings AS SELECT * FROM discord_sender_identities;
DROP TABLE discord_authorized_users;
CREATE TABLE discord_authorized_users (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    discord_user_id TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web'
);
CREATE UNIQUE INDEX idx_discord_auth_unique_user_id ON discord_authorized_users(project_id, discord_user_id);
CREATE INDEX idx_discord_auth_project ON discord_authorized_users(project_id);
CREATE INDEX idx_discord_auth_user ON discord_authorized_users(discord_user_id);
INSERT INTO discord_authorized_users(id,project_id,discord_user_id,display_name,added_at,added_by) SELECT id,'default',discord_user_id,display_name,added_at,added_by FROM downgrade_discord;
DROP TABLE downgrade_discord;
INSERT INTO discord_sender_identities SELECT * FROM downgrade_discord_bindings;
DROP TABLE downgrade_discord_bindings;
CREATE TEMP TABLE downgrade_email AS SELECT * FROM email_authorized_senders;
DROP TABLE email_authorized_senders;
CREATE TABLE email_authorized_senders (
    id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    email_address TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    added_at DATETIME NOT NULL DEFAULT (datetime('now')),
    added_by TEXT NOT NULL DEFAULT 'web'
);
CREATE UNIQUE INDEX idx_email_auth_unique_address ON email_authorized_senders(project_id, lower(email_address));
CREATE INDEX idx_email_auth_project ON email_authorized_senders(project_id);
CREATE INDEX idx_email_auth_address ON email_authorized_senders(lower(email_address));
INSERT INTO email_authorized_senders(id,project_id,email_address,display_name,added_at,added_by) SELECT id,'default',email_address,display_name,added_at,added_by FROM downgrade_email;
DROP TABLE downgrade_email;


CREATE INDEX IF NOT EXISTS idx_telegram_auth_list_covering
  ON telegram_authorized_users(added_at, id, project_id, telegram_user_id, telegram_username, display_name, added_by);
CREATE INDEX IF NOT EXISTS idx_slack_auth_list_covering
  ON slack_authorized_users(added_at, id, project_id, slack_user_id, display_name, added_by);
CREATE INDEX IF NOT EXISTS idx_discord_auth_list_covering
  ON discord_authorized_users(added_at, id, project_id, discord_user_id, display_name, added_by);
CREATE INDEX IF NOT EXISTS idx_email_auth_list_covering
  ON email_authorized_senders(added_at, id, project_id, email_address, display_name, added_by);


CREATE INDEX idx_telegram_auth_username_lower_unknown_id ON telegram_authorized_users(telegram_user_id, lower(telegram_username)) WHERE telegram_user_id=0 AND telegram_username != '';
DROP TABLE telegram_task_provenance;
ALTER TABLE thread_inputs DROP COLUMN telegram_user_id;
DROP TABLE channel_sender_audit;
DROP TABLE channel_sender_migration_review;
