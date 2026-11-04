package database

import (
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence/sqlite/database/migrations"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
)

func TestAutomationInvocationMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "invocation.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_invocation_users`).Scan(&count))
	require.Zero(t, count, "historical invocation identities are unknown")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestLaunchAuthorityMigrationDoesNotInventHistory(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "launch-authority.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_launch_authority`).Scan(&count))
	require.Zero(t, count, "deleted graph authority cannot be inferred")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestAutomationPublisherMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "publisher.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_version_publishers`).Scan(&count))
	require.Zero(t, count, "historical publisher identities are unknown")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestDiscordInvocationMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-invocation.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM discord_invocation_provenance`).Scan(&count))
	require.Zero(t, count, "historical invocation identities are unknown")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestDiscordPublisherMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-publisher.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM discord_publisher_provenance`).Scan(&count))
	require.Zero(t, count, "historical publisher channel identities are unknown")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestDiscordScheduleMigrationDoesNotInventProvenance(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-schedule.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('legacy-task','default','Legacy'); INSERT INTO schedules(id,task_id,run_at,repeat_type,repeat_interval,enabled) VALUES('legacy-schedule','legacy-task',datetime('now'),'daily',1,1)`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_schedule_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	var status string
	require.NoError(t, db.QueryRow(`SELECT repeat_type FROM schedules WHERE id='legacy-schedule'`).Scan(&status))
	require.Equal(t, "daily", status)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestDiscordSenderMigrationDoesNotInferIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO discord_authorized_users(id,project_id,discord_user_id,display_name) VALUES('legacy','default','123456789','owner')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_sender_identities`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_authorized_users WHERE id='legacy'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_authorized_users WHERE id='legacy'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_sender_identities`).Scan(&count))
	require.Zero(t, count)
}

func TestDiscordTaskProvenanceMigrationPreservesLegacyTasks(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-task.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,created_via) VALUES('legacy','default','Legacy Discord','discord')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_task_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE id='legacy'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tasks WHERE id='legacy'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_task_provenance`).Scan(&count))
	require.Zero(t, count)
}

func TestDiscordExecutionMigrationDoesNotInventEvidence(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-execution.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('task','default','Legacy run'); INSERT INTO executions(id,task_id,status) VALUES('run','task','running')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_execution_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM executions WHERE id='run'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_execution_provenance`).Scan(&count))
	require.Zero(t, count)
}

func TestDiscordInputMigrationPreservesUnboundQueue(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-input.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('task','default','Legacy queue'); INSERT INTO thread_inputs(id,scope,project_id,task_id,content,input_mode,input_status,queue_position) VALUES('input','task_thread','default','task','legacy','queued','pending',1)`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_input_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_inputs WHERE id='input'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_input_provenance`).Scan(&count))
	require.Zero(t, count)
}

func TestDiscordConsumedMigrationDoesNotInventEvidence(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "discord-consumed.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('task','default','Steering');
 INSERT INTO executions(id,task_id,status) VALUES('run','task','running');
 INSERT INTO thread_inputs(id,scope,project_id,task_id,content,input_mode,input_status,queue_position,run_execution_id) VALUES
 ('applied','task_thread','default','task','a','steering','applied',1,'run'),
 ('pending','task_thread','default','task','p','steering','pending',2,'run');`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_consumed_provenance`).Scan(&count))
	require.Zero(t, count)
	_, err = db.Exec(`DELETE FROM thread_inputs`)
	require.NoError(t, err)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM discord_consumed_provenance`).Scan(&count))
	require.Zero(t, count)
}

func TestExecutionGoalSnapshotMigrationPreservesExistingRunIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "execution-goal.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,author_user_id) VALUES('saved-task','default','Existing schedule','departed-user');
 INSERT INTO executions(id,task_id) VALUES('saved-execution','saved-task');
 INSERT INTO schedules(id,task_id,run_at) VALUES('schedule','saved-task',datetime('now'));
 INSERT INTO schedule_dispatch_admissions(task_id,schedule_id,execution_id,starts_new_context) VALUES('saved-task','schedule','saved-execution',0)`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var userID, goalID string
	var continuation bool
	require.NoError(t, db.QueryRow(`SELECT user_id,goal_id,goal_continuation FROM execution_run_identities WHERE execution_id='saved-execution'`).Scan(&userID, &goalID, &continuation))
	require.Equal(t, "departed-user", userID)
	require.Empty(t, goalID)
	require.False(t, continuation, "ordinary calendar admissions must not become goal continuations")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT author_user_id FROM tasks WHERE id='saved-task'`).Scan(&userID))
	require.Equal(t, "departed-user", userID)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestGitHubReviewerMigrationPreservesKnownEvidence(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "reviewers.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('task','default','Review');
 INSERT INTO executions(id,task_id,status) VALUES('run','task','running');
 INSERT INTO task_pull_requests(id,task_id,pr_number,pr_url) VALUES('pr','task',42,'https://github.com/example/repo/pull/42');
 INSERT INTO thread_inputs(id,scope,project_id,task_id,content,input_mode,input_status,queue_position,run_execution_id) VALUES
 ('pending','task_thread','default','task','pending','queued','pending',1,'run'),
 ('applied','task_thread','default','task','applied','steering','applied',2,'run'),
 ('ordinary','task_thread','default','task','ordinary','queued','pending',3,NULL);
 INSERT INTO github_pr_feedback_forwarded(task_pull_request_id,task_id,repo_full_name,pr_number,feedback_kind,github_id,author_login,created_at,queued_thread_input_id) VALUES
 ('pr','task','example/repo',42,'review','1','PendingReviewer',datetime('now'),'pending'),
 ('pr','task','example/repo',42,'review','2','ConsumedReviewer',datetime('now'),'applied');`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_input_provenance`).Scan(&count))
	require.Equal(t, 2, count)
	var reviewer string
	require.NoError(t, db.QueryRow(`SELECT reviewer_login FROM github_reviewer_execution_provenance WHERE execution_id='run'`).Scan(&reviewer))
	require.Equal(t, "consumedreviewer", reviewer)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_execution_provenance`).Scan(&count))
	require.Equal(t, 1, count, "pending input must not authorize or attach to an execution yet")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_inputs`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
	_, err = db.Exec(`DELETE FROM task_pull_requests; DELETE FROM thread_inputs;`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_execution_provenance`).Scan(&count))
	require.Equal(t, 1, count, "execution evidence survives source cleanup")
}

func TestGitHubProviderReviewerMigrationRetainsAmbiguousEvidence(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "provider-reviewers.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('task','default','Review');
 INSERT INTO executions(id,task_id,status) VALUES('run','task','running');
 INSERT INTO thread_inputs(id,scope,project_id,task_id,content,input_mode,input_status,queue_position,run_execution_id) VALUES
 ('pending','task_thread','default','task','pending','steering','pending',1,'run'),
 ('ambiguous','task_thread','default','task','ambiguous','steering','pending',2,'run'),
 ('confirmed','task_thread','default','task','confirmed','steering','applied',3,'run'),
 ('unsent','task_thread','default','task','unsent','steering','pending',4,'run');
 INSERT INTO task_pull_requests(id,task_id,pr_number,pr_url) VALUES('pr','task',42,'https://github.com/example/repo/pull/42');
 INSERT INTO github_pr_feedback_forwarded(task_pull_request_id,task_id,repo_full_name,pr_number,feedback_kind,github_id,author_login,created_at,queued_thread_input_id)
 SELECT 'pr','task','example/repo',42,'review',id,id||'-reviewer',datetime('now'),id FROM thread_inputs;
 INSERT INTO thread_input_provider_steering(thread_input_id,steering_id,delivery_state) VALUES
 ('pending','s1','accepted_pending'),('ambiguous','s2','accepted_ambiguous'),('confirmed','s3','accepted_confirmed');`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_execution_provenance`).Scan(&count))
	require.Equal(t, 3, count)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_execution_provenance WHERE reviewer_login='unsent-reviewer'`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM thread_input_provider_steering`).Scan(&count))
	require.Equal(t, 3, count, "rollback preserves the source delivery records")
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_execution_provenance`).Scan(&count))
	require.Equal(t, 3, count, "backfill is idempotent")
}

func TestGitHubReviewerBindingMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "bindings.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO github_authorized_actors(github_login,github_user_id) VALUES('legacy',123)`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	for _, table := range []string{"github_reviewer_identities", "github_reviewer_input_identities", "github_reviewer_execution_identities"} {
		require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		require.Zero(t, count)
	}
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_authorized_actors WHERE github_login='legacy' AND github_user_id=123`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM github_reviewer_identities`).Scan(&count))
	require.Zero(t, count)
}

func TestGoalContinuationSnapshotMigrationPreservesUnattributedQueue(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "goal-snapshot.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO projects (id, name) VALUES ('snapshot-project', 'Snapshot');
 INSERT INTO thread_inputs (id, scope, project_id, input_mode, content, queue_position, source, origin_agent)
 VALUES ('old-goal-input', 'task_thread', 'snapshot-project', 'queued', 'Continue', 1, 'system_agent', 'goal')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var goalID, content, status string
	require.NoError(t, db.QueryRow(`SELECT goal_id, content, input_status FROM thread_inputs WHERE id='old-goal-input'`).Scan(&goalID, &content, &status))
	require.Empty(t, goalID, "migration must not guess which goal produced an old continuation")
	require.Equal(t, "Continue", content)
	require.Equal(t, "pending", status)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT content FROM thread_inputs WHERE id='old-goal-input'`).Scan(&content))
	require.Equal(t, "Continue", content)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestProjectAuthorizationMigrationPreservesProjectsAndDefaults(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "project-access.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO projects(id,name,repo_path) VALUES('existing','Existing','/repo')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	check := func(id string) {
		t.Helper()
		var owner, visibility, role, path string
		require.NoError(t, db.QueryRow(`SELECT po.owner_user_id,po.visibility,po.org_role,p.repo_path FROM project_organizations po JOIN projects p ON p.id=po.project_id WHERE p.id=?`, id).Scan(&owner, &visibility, &role, &path))
		require.Equal(t, "local-owner", owner)
		require.Equal(t, "org", visibility)
		require.Equal(t, "editor", role)
		require.Equal(t, "/repo", path)
	}
	check("existing")
	_, err = db.Exec(`INSERT INTO projects(id,name,repo_path) VALUES('new','New','/repo')`)
	require.NoError(t, err)
	check("new")
	_, err = db.Exec(`INSERT INTO project_members VALUES('new','local-owner','maintainer'); INSERT INTO project_audit_events(project_id,actor_user_id,action) VALUES('new','local-owner','test'); DELETE FROM projects WHERE id='new'`)
	require.NoError(t, err)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_members`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM project_audit_events`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	_, err = db.Exec(`INSERT INTO projects(id,name,repo_path) VALUES('rollback','Rollback','/repo')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	check("existing")
	check("rollback")
}

func TestProjectAuthorizationMigrationPreservesTaskRunIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "run-identity.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,author_user_id) VALUES('old','default','Old',''),('named','default','Named','departed-user')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var user string
	require.NoError(t, db.QueryRow(`SELECT user_id FROM task_run_identities WHERE task_id='old'`).Scan(&user))
	require.Equal(t, "local-owner", user)
	require.NoError(t, db.QueryRow(`SELECT user_id FROM task_run_identities WHERE task_id='named'`).Scan(&user))
	require.Equal(t, "departed-user", user, "migration must not elevate an unavailable user to owner")
	require.NoError(t, goose.DownTo(db, ".", 223))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id IN ('old','named')`).Scan(&count))
	require.Equal(t, 2, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
	_, err = db.Exec(`DELETE FROM tasks WHERE id='old'`)
	require.NoError(t, err)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_run_identities WHERE task_id='old'`).Scan(&count))
	require.Zero(t, count)
}

func TestProjectAuthorizationMigrationPreservesScheduleAndAdmissionIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "schedule-identity.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,author_user_id) VALUES('task','default','Scheduled','departed-user');
 INSERT INTO schedules(id,task_id,run_at) VALUES('schedule','task',datetime('now'));
 INSERT INTO executions(id,task_id,status) VALUES('execution','task','queued');
 INSERT INTO schedule_dispatch_admissions(task_id,schedule_id,execution_id,starts_new_context) VALUES('task','schedule','execution',0);`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	for _, query := range []string{
		`SELECT user_id FROM schedule_run_identities WHERE schedule_id='schedule'`,
		`SELECT user_id FROM execution_run_identities WHERE execution_id='execution'`,
		`SELECT run_as_user_id FROM schedule_dispatch_admissions WHERE task_id='task'`,
	} {
		var user string
		require.NoError(t, db.QueryRow(query).Scan(&user))
		require.Equal(t, "departed-user", user, "never replace a removed creator with the owner")
	}
	require.NoError(t, goose.DownTo(db, ".", 223))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM schedule_dispatch_admissions WHERE task_id='task'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestProjectAuthorizationMigrationBackfillsWebhookOwner(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "webhook-identity.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO webhook_endpoints(id,project_id,name,path_token,secret) VALUES('hook','default','Existing','token','secret')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var id string
	require.NoError(t, db.QueryRow(`SELECT run_as_user_id FROM webhook_endpoints WHERE id='hook'`).Scan(&id))
	require.Equal(t, "local-owner", id)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT path_token FROM webhook_endpoints WHERE id='hook'`).Scan(&id))
	require.Equal(t, "token", id)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestProjectAuthorizationMigrationPreservesGoalUser(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "goal-user.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,author_user_id) VALUES('task','default','Goal task','departed-user'); INSERT INTO task_goals(task_id,goal_id,objective,status) VALUES('task','goal','Finish','active')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var user string
	require.NoError(t, db.QueryRow(`SELECT run_as_user_id FROM task_goals WHERE task_id='task'`).Scan(&user))
	require.Equal(t, "departed-user", user)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT objective FROM task_goals WHERE task_id='task'`).Scan(&user))
	require.Equal(t, "Finish", user)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackExecutionMigrationDoesNotInventProvenance(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "slack-execution.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('legacy-task','default','Legacy'); INSERT INTO executions(id,task_id,status) VALUES('legacy-execution','legacy-task','queued')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_execution_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM executions WHERE id='legacy-execution'`).Scan(&status))
	require.Equal(t, "queued", status)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackInputMigrationPreservesLegacyQueue(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "queue.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO thread_inputs(id,scope,project_id,content,input_mode,input_status,queue_position) VALUES('legacy','chat','default','Continue','queued','pending',1)`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_input_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	var status string
	require.NoError(t, db.QueryRow(`SELECT input_status FROM thread_inputs WHERE id='legacy'`).Scan(&status))
	require.Equal(t, "pending", status)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackInvocationMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "slack-invocation.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM slack_invocation_provenance`).Scan(&count))
	require.Zero(t, count, "historical invocation identities are unknown")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackPublisherMigrationDoesNotInventIdentity(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "slack-publisher.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('existing','default','Existing task')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM slack_publisher_provenance`).Scan(&count))
	require.Zero(t, count, "historical publisher channel identities are unknown")
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM tasks WHERE id='existing'`).Scan(&count))
	require.Equal(t, 1, count)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackScheduleMigrationDoesNotInventProvenance(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "slack-schedule.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('legacy-task','default','Legacy'); INSERT INTO schedules(id,task_id,run_at,repeat_type,repeat_interval,enabled) VALUES('legacy-schedule','legacy-task',datetime('now'),'daily',1,1)`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_schedule_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	var status string
	require.NoError(t, db.QueryRow(`SELECT repeat_type FROM schedules WHERE id='legacy-schedule'`).Scan(&status))
	require.Equal(t, "daily", status)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackSenderMigrationPreservesUnboundUsers(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "slack.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO slack_authorized_users(id,project_id,slack_user_id) VALUES('legacy','default','U123')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM slack_sender_identities`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	var id string
	require.NoError(t, db.QueryRow(`SELECT slack_user_id FROM slack_authorized_users WHERE id='legacy'`).Scan(&id))
	require.Equal(t, "U123", id)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestSlackTaskMigrationDoesNotInventProvenance(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "slack-task.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title) VALUES('legacy-task','default','Legacy'); INSERT INTO executions(id,task_id,status) VALUES('legacy-execution','legacy-task','queued')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM slack_task_provenance`).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, goose.DownTo(db, ".", 223))
	var status string
	require.NoError(t, db.QueryRow(`SELECT status FROM executions WHERE id='legacy-execution'`).Scan(&status))
	require.Equal(t, "queued", status)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestXSenderMigrationPreservesUnmappedAllowlist(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "sender.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	_, err := db.Exec(`INSERT INTO x_authorized_users(id,project_id,x_user_id,username) VALUES('old','default','123','old')`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	var user, verifier string
	require.NoError(t, db.QueryRow(`SELECT application_user_id,verified_by FROM x_authorized_users WHERE id='old'`).Scan(&user, &verifier))
	require.Empty(t, user)
	require.Empty(t, verifier)
	require.NoError(t, goose.DownTo(db, ".", 223))
	require.NoError(t, db.QueryRow(`SELECT x_user_id FROM x_authorized_users WHERE id='old'`).Scan(&user))
	require.Equal(t, "123", user)
	require.NoError(t, goose.UpTo(db, ".", 224))
}

func TestProjectAuthorizationMigrationFailureRollsBackWholeUpgrade(t *testing.T) {
	db := openMigrationTestDB(t, filepath.Join(t.TempDir(), "atomic-upgrade.db"))
	goose.SetBaseFS(migrations.FS)
	require.NoError(t, goose.SetDialect("sqlite3"))
	require.NoError(t, goose.UpTo(db, ".", 223))
	// Force a conflict at the final table creation, after permission and identity backfills.
	_, err := db.Exec(`INSERT INTO tasks(id,project_id,title,author_user_id) VALUES('existing','default','Existing','departed-user');
 CREATE TABLE discord_publisher_provenance(sentinel TEXT);`)
	require.NoError(t, err)
	require.ErrorContains(t, goose.UpTo(db, ".", 224), "already exists")
	version, err := goose.GetDBVersion(db)
	require.NoError(t, err)
	require.EqualValues(t, 223, version)
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('project_members','task_run_identities','discord_execution_provenance')`).Scan(&count))
	require.Zero(t, count, "failed upgrade must leave no partial authorization schema")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pragma_table_info('project_organizations') WHERE name IN ('visibility','org_role')`).Scan(&count))
	require.Zero(t, count)
	var author string
	require.NoError(t, db.QueryRow(`SELECT author_user_id FROM tasks WHERE id='existing'`).Scan(&author))
	require.Equal(t, "departed-user", author)
	_, err = db.Exec(`DROP TABLE discord_publisher_provenance`)
	require.NoError(t, err)
	require.NoError(t, goose.UpTo(db, ".", 224))
	require.NoError(t, db.QueryRow(`SELECT user_id FROM task_run_identities WHERE task_id='existing'`).Scan(&author))
	require.Equal(t, "departed-user", author)
}
