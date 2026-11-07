-- +goose Up
-- Unreleased Phase 2 schema, upgrading directly from version 223.
-- Preserve existing project access and recorded users; never infer channel bindings.

ALTER TABLE project_organizations ADD COLUMN visibility TEXT NOT NULL DEFAULT 'org' CHECK(visibility IN ('org','private'));
ALTER TABLE project_organizations ADD COLUMN org_role TEXT NOT NULL DEFAULT 'editor' CHECK(org_role IN ('viewer','editor'));
DROP TRIGGER project_default_organization;
-- +goose StatementBegin
CREATE TRIGGER project_default_organization AFTER INSERT ON projects BEGIN
 INSERT INTO project_organizations(project_id,org_id,owner_user_id) VALUES(NEW.id,'default','local-owner');
END;
-- +goose StatementEnd
CREATE TABLE project_members (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role TEXT NOT NULL CHECK(role IN ('viewer','editor','maintainer')),
 PRIMARY KEY(project_id,user_id)
);
CREATE TABLE project_audit_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 project_id TEXT NOT NULL,
 actor_user_id TEXT NOT NULL,
 action TEXT NOT NULL,
 target_id TEXT NOT NULL DEFAULT '',
 details TEXT NOT NULL DEFAULT '',
 created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX project_audit_events_project ON project_audit_events(project_id,id);

CREATE TABLE task_run_identities (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL,
 updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
-- Preserve the recorded author when available. Pre-account tasks belonged to
-- the bootstrap owner. Do not cascade on user removal: losing identity must
-- deny execution, never silently substitute another user.
INSERT INTO task_run_identities(task_id,user_id)
 SELECT id, COALESCE(NULLIF(author_user_id,''),'local-owner') FROM tasks;

CREATE TABLE schedule_run_identities (
 schedule_id TEXT PRIMARY KEY REFERENCES schedules(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL
);
INSERT INTO schedule_run_identities(schedule_id,user_id)
 SELECT s.id, r.user_id FROM schedules s JOIN task_run_identities r ON r.task_id=s.task_id;
CREATE TABLE execution_run_identities (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL,
 goal_id TEXT NOT NULL DEFAULT '',
 goal_continuation INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE schedule_dispatch_admissions ADD COLUMN run_as_user_id TEXT NOT NULL DEFAULT '';
UPDATE schedule_dispatch_admissions SET run_as_user_id=COALESCE((SELECT user_id FROM schedule_run_identities WHERE schedule_id=schedule_dispatch_admissions.schedule_id),'');
INSERT INTO execution_run_identities(execution_id,user_id)
 SELECT execution_id,run_as_user_id FROM schedule_dispatch_admissions WHERE execution_id IS NOT NULL;

ALTER TABLE webhook_endpoints ADD COLUMN run_as_user_id TEXT NOT NULL DEFAULT '';
-- Historical endpoints have no creator; retain access under their project owner.
UPDATE webhook_endpoints SET run_as_user_id=COALESCE((SELECT owner_user_id FROM project_organizations WHERE project_id=webhook_endpoints.project_id),'');

ALTER TABLE task_goals ADD COLUMN run_as_user_id TEXT NOT NULL DEFAULT '';
-- Preserve the saved task user for historical goals; never substitute a departed user.
UPDATE task_goals SET run_as_user_id=COALESCE((SELECT user_id FROM task_run_identities WHERE task_id=task_goals.task_id),'');

ALTER TABLE thread_inputs ADD COLUMN goal_id TEXT NOT NULL DEFAULT '';
-- Historical rows have no provable goal identity. Leave empty: goal continuations
-- fail closed rather than adopting whichever goal currently belongs to the task.

CREATE TABLE automation_version_publishers (
 version_id TEXT PRIMARY KEY REFERENCES automation_versions(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL
);

CREATE TABLE automation_invocation_users (
 invocation_id TEXT PRIMARY KEY REFERENCES automation_invocations(id) ON DELETE CASCADE,
 user_id TEXT NOT NULL
);

ALTER TABLE x_authorized_users ADD COLUMN application_user_id TEXT NOT NULL DEFAULT '';
ALTER TABLE x_authorized_users ADD COLUMN verified_by TEXT NOT NULL DEFAULT '';

CREATE TABLE slack_sender_identities (
 allowlist_id TEXT NOT NULL REFERENCES slack_authorized_users(id) ON DELETE CASCADE,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 application_user_id TEXT NOT NULL REFERENCES users(id),
 verified_by TEXT NOT NULL REFERENCES users(id),
 PRIMARY KEY(allowlist_id, team_id)
);

CREATE TABLE slack_execution_provenance (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE slack_input_provenance (
 input_id TEXT PRIMARY KEY REFERENCES thread_inputs(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE slack_task_provenance (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE slack_schedule_provenance (
 schedule_id TEXT PRIMARY KEY REFERENCES schedules(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE slack_invocation_provenance (
 invocation_id TEXT PRIMARY KEY REFERENCES automation_invocations(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE slack_publisher_provenance (
 version_id TEXT PRIMARY KEY REFERENCES automation_versions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 team_id TEXT NOT NULL CHECK(team_id <> ''),
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE automation_launch_authority (
 invocation_id TEXT NOT NULL,
 node_id TEXT NOT NULL,
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 automation_id TEXT NOT NULL REFERENCES automations(id) ON DELETE CASCADE,
 version_id TEXT NOT NULL,
 publisher_id TEXT NOT NULL,
 initiator_id TEXT NOT NULL,
 publisher_team TEXT NOT NULL,
 publisher_sender TEXT NOT NULL,
 initiator_team TEXT NOT NULL,
 initiator_sender TEXT NOT NULL,
 initiator_discord_sender TEXT NOT NULL DEFAULT '',
 publisher_discord_sender TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(invocation_id,node_id)
);

-- Reviewer evidence must survive PR/feedback cleanup and allowlist removal.
CREATE TABLE github_reviewer_input_provenance (
 input_id TEXT NOT NULL REFERENCES thread_inputs(id) ON DELETE CASCADE,
 reviewer_login TEXT NOT NULL CHECK(reviewer_login <> ''),
 PRIMARY KEY(input_id, reviewer_login)
);
CREATE TABLE github_reviewer_execution_provenance (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 reviewer_login TEXT NOT NULL CHECK(reviewer_login <> ''),
 PRIMARY KEY(execution_id, reviewer_login)
);
INSERT OR IGNORE INTO github_reviewer_input_provenance
 SELECT i.id, lower(trim(f.author_login)) FROM github_pr_feedback_forwarded f
 JOIN thread_inputs i ON i.id=f.queued_thread_input_id WHERE trim(f.author_login)<>'';
INSERT OR IGNORE INTO github_reviewer_execution_provenance
 SELECT e.id,p.reviewer_login FROM github_reviewer_input_provenance p
 JOIN thread_inputs i ON i.id=p.input_id JOIN executions e ON e.id=i.run_execution_id
 WHERE i.input_status='applied';

-- Pending/ambiguous acceptance may already have reached the provider. Preserve
-- known reviewer evidence independently of provider and input retention.
INSERT OR IGNORE INTO github_reviewer_execution_provenance(execution_id,reviewer_login)
 SELECT e.id,p.reviewer_login FROM github_reviewer_input_provenance p
 JOIN thread_inputs i ON i.id=p.input_id
 JOIN thread_input_provider_steering s ON s.thread_input_id=i.id
 JOIN executions e ON e.id=i.run_execution_id
 WHERE s.delivery_state IN ('accepted_pending','accepted_ambiguous','accepted_confirmed');

-- Administrators attest immutable provider identity separately for each project.
-- Legacy allowlists intentionally receive no inferred application identity.
CREATE TABLE github_reviewer_identities (
 project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
 actor_id TEXT NOT NULL REFERENCES github_authorized_actors(id) ON DELETE CASCADE,
 host TEXT NOT NULL CHECK(host <> ''),
 external_user_id INTEGER NOT NULL CHECK(external_user_id > 0),
 application_user_id TEXT NOT NULL REFERENCES users(id),
 verified_by TEXT NOT NULL REFERENCES users(id),
 PRIMARY KEY(project_id,actor_id),
 UNIQUE(project_id,host,external_user_id)
);
-- Snapshots deliberately do not reference the mutable binding or allowlist.
CREATE TABLE github_reviewer_input_identities (
 input_id TEXT NOT NULL REFERENCES thread_inputs(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, actor_id TEXT NOT NULL, host TEXT NOT NULL,
 external_user_id INTEGER NOT NULL, application_user_id TEXT NOT NULL,
 PRIMARY KEY(input_id,actor_id)
);
CREATE TABLE github_reviewer_execution_identities (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL, actor_id TEXT NOT NULL, host TEXT NOT NULL,
 external_user_id INTEGER NOT NULL, application_user_id TEXT NOT NULL,
 PRIMARY KEY(execution_id,actor_id,host,external_user_id,application_user_id)
);

-- No inferred identities for existing allowlist entries.
CREATE TABLE discord_sender_identities (
 allowlist_id TEXT PRIMARY KEY REFERENCES discord_authorized_users(id) ON DELETE CASCADE,
 application_user_id TEXT NOT NULL REFERENCES users(id),
 verified_by TEXT NOT NULL REFERENCES users(id)
);

-- Snapshots survive live binding/allowlist removal. Historical tasks are not inferred.
CREATE TABLE discord_task_provenance (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL,
 user_id TEXT NOT NULL
);

CREATE TABLE discord_execution_provenance (
 execution_id TEXT PRIMARY KEY REFERENCES executions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL,
 user_id TEXT NOT NULL
);

-- No live-binding foreign key: revocation must retain the input's evidence.
CREATE TABLE discord_input_provenance (
 input_id TEXT PRIMARY KEY REFERENCES thread_inputs(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL,
 user_id TEXT NOT NULL
);

CREATE TABLE discord_consumed_provenance (
 execution_id TEXT NOT NULL REFERENCES executions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL,
 user_id TEXT NOT NULL,
 PRIMARY KEY(execution_id,project_id,external_user_id,user_id)
);

CREATE TABLE discord_schedule_provenance (
 schedule_id TEXT PRIMARY KEY REFERENCES schedules(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE discord_invocation_provenance (
 invocation_id TEXT PRIMARY KEY REFERENCES automation_invocations(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

CREATE TABLE discord_publisher_provenance (
 version_id TEXT PRIMARY KEY REFERENCES automation_versions(id) ON DELETE CASCADE,
 project_id TEXT NOT NULL,
 external_user_id TEXT NOT NULL CHECK(external_user_id <> ''),
 user_id TEXT NOT NULL CHECK(user_id <> '')
);

-- Global channel sender mappings. Existing senders require an explicit OV user assignment.
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

DROP TABLE old_slack;

CREATE UNIQUE INDEX idx_slack_auth_global_identity ON slack_authorized_users(slack_user_id);

CREATE TEMP TABLE old_discord AS SELECT *, discord_user_id AS identity_key FROM discord_authorized_users;

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

DROP TABLE discord_publisher_provenance;

DROP TABLE discord_invocation_provenance;

DROP TABLE discord_schedule_provenance;

DROP TABLE discord_consumed_provenance;

DROP TABLE discord_input_provenance;

DROP TABLE discord_execution_provenance;

DROP TABLE discord_task_provenance;

DROP TABLE discord_sender_identities;

DROP TABLE github_reviewer_execution_identities;
DROP TABLE github_reviewer_input_identities;
DROP TABLE github_reviewer_identities;

DROP TABLE github_reviewer_execution_provenance;
DROP TABLE github_reviewer_input_provenance;

DROP TABLE automation_launch_authority;

DROP TABLE slack_publisher_provenance;

DROP TABLE slack_invocation_provenance;

DROP TABLE slack_schedule_provenance;

DROP TABLE slack_task_provenance;

DROP TABLE slack_input_provenance;

DROP TABLE slack_execution_provenance;

DROP TABLE slack_sender_identities;

ALTER TABLE x_authorized_users DROP COLUMN verified_by;
ALTER TABLE x_authorized_users DROP COLUMN application_user_id;

DROP TABLE automation_invocation_users;

DROP TABLE automation_version_publishers;

ALTER TABLE thread_inputs DROP COLUMN goal_id;

ALTER TABLE task_goals DROP COLUMN run_as_user_id;

ALTER TABLE webhook_endpoints DROP COLUMN run_as_user_id;

ALTER TABLE schedule_dispatch_admissions DROP COLUMN run_as_user_id;
DROP TABLE execution_run_identities;
DROP TABLE schedule_run_identities;

DROP TABLE task_run_identities;

DROP TABLE project_audit_events;
DROP TABLE project_members;
DROP TRIGGER project_default_organization;
ALTER TABLE project_organizations DROP COLUMN org_role;
ALTER TABLE project_organizations DROP COLUMN visibility;
-- +goose StatementBegin
CREATE TRIGGER project_default_organization AFTER INSERT ON projects BEGIN
 INSERT INTO project_organizations VALUES(NEW.id,'default','local-owner');
END;
-- +goose StatementEnd
