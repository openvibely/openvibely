-- +goose Up
-- Unreleased Phase 2 schema, applied directly to version 221.
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

-- +goose Down
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
