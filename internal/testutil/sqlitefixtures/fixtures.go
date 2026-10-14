// Package sqlitefixtures implements test state manipulation for SQLite.
package sqlitefixtures

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"

	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
)

type fixtures struct{ db *sql.DB }

// New uses the same writable database as the test's stores. The caller owns it.
func New(db *sql.DB) storefixture.Fixtures { return &fixtures{db: db} }

func (f *fixtures) setTime(ctx context.Context, query, id string, at time.Time) error {
	result, err := f.db.ExecContext(ctx, query, at.UTC().Format("2006-01-02 15:04:05.999999999"), id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return persistence.ErrNotFound
	}
	return nil
}

func (f *fixtures) SetExecutionStartedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE executions SET started_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) SetTaskUpdatedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE tasks SET updated_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) SetTaskCompletedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE tasks SET completed_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) CountAutomations(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automations`).Scan(&count)
	return count, err
}

func (f *fixtures) ClaimScheduleOccurrence(ctx context.Context, schedule models.Schedule, now time.Time, next *time.Time) (bool, error) {
	return sqlite.NewScheduleRepo(f.db).ClaimCalendarOccurrence(ctx, schedule, now, next)
}
func (f *fixtures) SetScheduleRunTimes(ctx context.Context, id string, last time.Time, next *time.Time) error {
	return sqlite.NewScheduleRepo(f.db).MarkRan(ctx, id, last, next)
}
func (f *fixtures) PauseScheduleAfterEnable(ctx context.Context, id string) error {
	// SQLite trigger definitions cannot bind parameters. Quote the fixture identifier as a literal.
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER pause_schedule_after_enable
 AFTER UPDATE OF enabled ON schedules WHEN NEW.id = '`+strings.ReplaceAll(id, "'", "''")+`' AND NEW.enabled = 1
 BEGIN UPDATE schedules SET enabled = 0 WHERE id = NEW.id; END`)
	return err
}

func (f *fixtures) ExecutionOutputState(ctx context.Context, id string) (storefixture.ExecutionOutputState, error) {
	var state storefixture.ExecutionOutputState
	err := f.db.QueryRowContext(ctx, `SELECT output, (SELECT COUNT(*) FROM execution_output_chunks WHERE execution_id = executions.id) FROM executions WHERE id = ?`, id).Scan(&state.Materialized, &state.PendingSegments)
	if err == sql.ErrNoRows {
		err = persistence.ErrNotFound
	}
	return state, err
}

func (f *fixtures) SetLegacyAgentName(ctx context.Context, id, name string) error {
	result, err := f.db.ExecContext(ctx, `UPDATE agents SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return persistence.ErrNotFound
	}
	return err
}

func (f *fixtures) SeedChannelTarget(ctx context.Context, target models.ChannelTarget) error {
	return sqlite.NewChannelTargetRepo(f.db).Upsert(ctx, target)
}
func (f *fixtures) ChannelMessageSends(ctx context.Context, projectID string) ([]models.ChannelMessageSend, error) {
	return sqlite.NewChannelTargetRepo(f.db).ListSendsByProject(ctx, projectID)
}

func (f *fixtures) ProducedCommitStats(ctx context.Context, projectID string, since time.Time) ([]models.TaskCommitStat, error) {
	return sqlite.NewTaskCommitStatRepo(f.db).ListProducedCommitStats(ctx, projectID, since)
}

// SkillEvents returns persisted evidence without dashboard aggregation.
func (f *fixtures) SkillEvents(ctx context.Context, filter storefixture.SkillEventFilter) ([]models.SkillAnalyticsEvent, error) {
	query := `SELECT id, created_at, COALESCE(project_id,''), COALESCE(task_id,''), COALESCE(execution_id,''), COALESCE(thread_id,''), COALESCE(agent_id,''), skill_scope, skill_handle, event_type, source, surface FROM skill_analytics_events WHERE 1=1`
	var args []any
	for _, term := range []struct{ column, value string }{
		{"project_id", filter.ProjectID}, {"task_id", filter.TaskID}, {"skill_handle", filter.SkillHandle}, {"agent_id", filter.AgentID}, {"event_type", filter.EventType}, {"source", filter.Source}, {"surface", filter.Surface}, {"skill_scope", filter.SkillScope},
	} {
		if term.value != "" {
			query += " AND " + term.column + " = ?"
			args = append(args, term.value)
		}
	}
	rows, err := f.db.QueryContext(ctx, query+" ORDER BY rowid", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []models.SkillAnalyticsEvent
	for rows.Next() {
		var e models.SkillAnalyticsEvent
		if err := rows.Scan(&e.ID, &e.CreatedAt, &e.ProjectID, &e.TaskID, &e.ExecutionID, &e.ThreadID, &e.AgentID, &e.SkillScope, &e.SkillHandle, &e.EventType, &e.Source, &e.Surface); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (f *fixtures) SeedAccountUsageSnapshot(ctx context.Context, snapshot *models.AccountUsageSnapshot) error {
	return sqlite.NewUsageRepo(f.db).CreateAccountUsageSnapshot(ctx, snapshot)
}
func (f *fixtures) CountAccountUsageSnapshots(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_usage_snapshots`).Scan(&count)
	return count, err
}
func (f *fixtures) LinkModelOAuthConnection(ctx context.Context, modelID, connectionID string) error {
	return sqlite.NewLLMConfigRepo(f.db).LinkOAuthConnection(ctx, modelID, connectionID)
}

func (f *fixtures) SetModelOAuthTokens(ctx context.Context, id, access, refresh string, expiresAt int64) error {
	return sqlite.NewLLMConfigRepo(f.db).UpdateOAuthTokens(ctx, id, access, refresh, expiresAt)
}

func (f *fixtures) PromoteQueuedTaskInput(ctx context.Context, id string, execution *models.Execution) error {
	return sqlite.NewThreadInputRepo(f.db).ClaimQueuedForTaskExecution(ctx, id, execution)
}

func (f *fixtures) SetTaskCreatedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE tasks SET created_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) SetExecutionCompletedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE executions SET completed_at = ? WHERE id = ?`, id, at)
}
func (f *fixtures) KnowledgeEntry(ctx context.Context, projectID, id string) (*models.KnowledgeEntry, error) {
	return sqlite.NewInsightsRepo(f.db).GetKnowledge(ctx, projectID, id)
}

func (f *fixtures) SetVerifiedOAuthPrincipal(ctx context.Context, id, principal string) error {
	result, err := f.db.ExecContext(ctx, `UPDATE oauth_connections SET oauth_principal_hash = ?, oauth_principal_verified = 1 WHERE id = ?`, principal, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return persistence.ErrNotFound
	}
	return err
}

func sqliteLiteral(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func (f *fixtures) FailTaskDeletion(ctx context.Context, id, message string) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_fixture_task_delete BEFORE DELETE ON tasks WHEN OLD.id = `+sqliteLiteral(id)+` BEGIN SELECT RAISE(ABORT, `+sqliteLiteral(message)+`); END`)
	return err
}
func (f *fixtures) FailTaskBacklogMove(ctx context.Context, id string) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_fixture_task_backlog_move BEFORE UPDATE OF category ON tasks WHEN NEW.id = `+sqliteLiteral(id)+` AND NEW.category = 'backlog' BEGIN SELECT RAISE(FAIL, 'forced category update failure'); END`)
	return err
}
func (f *fixtures) SetTaskStatus(ctx context.Context, id string, status models.TaskStatus) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET status = ? WHERE id = ?`, status, id)
	return err
}

// SeedOrphanSchedule confines constraint disabling to a checked-out connection.
func (f *fixtures) SeedOrphanSchedule(ctx context.Context, id, missingTaskID string, at time.Time) error {
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	_, insertErr := conn.ExecContext(ctx, `INSERT INTO schedules (id,task_id,run_at,repeat_type,repeat_interval,enabled,next_run) VALUES (?,?,?,'once',1,1,?)`, id, missingTaskID, at.UTC().Format("2006-01-02 15:04:05.999999999"), at.UTC().Format("2006-01-02 15:04:05.999999999"))
	_, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
	return errors.Join(insertErr, restoreErr)
}
func (f *fixtures) SeedOverflowSchedule(ctx context.Context, id, taskID string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO schedules (id,task_id,run_at,repeat_type,repeat_interval,enabled,next_run) VALUES (?,?,?,'seconds',9223372036854775807,1,?)`, id, taskID, at.UTC().Format("2006-01-02 15:04:05.999999999"), at.UTC().Format("2006-01-02 15:04:05.999999999"))
	return err
}

func (f *fixtures) WriteExecutionSnapshotBaseline(ctx context.Context, id, output string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE executions SET output = ? WHERE id = ? AND status = 'running'`, output, id)
	return err
}

func (f *fixtures) LatestExecution(ctx context.Context) (*models.Execution, error) {
	var id string
	err := f.db.QueryRowContext(ctx, `SELECT id FROM executions ORDER BY started_at DESC,rowid DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return sqlite.NewExecutionRepo(f.db).GetByID(ctx, id)
}
func (f *fixtures) SeedDiscordAuthorizedUsers(ctx context.Context, users []models.DiscordAuthorizedUser) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO discord_authorized_users (project_id,discord_user_id,display_name,added_by) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range users {
		if _, err := stmt.ExecContext(ctx, u.ProjectID, u.DiscordUserID, u.DisplayName, u.AddedBy); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *fixtures) CountProducedCommitStats(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_commit_stats`).Scan(&count)
	return count, err
}

func (f *fixtures) CountLifecycleHooks(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_lifecycle_hooks`).Scan(&count)
	return count, err
}

func (f *fixtures) SeedLifecycleEvent(ctx context.Context, event *models.LifecycleExecutionEvent) error {
	return sqlite.NewLifecycleRepo(f.db).AppendExecutionEvent(ctx, event)
}
func (f *fixtures) SetLifecycleExecutionIdentity(ctx context.Context, oldID, id string, at time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE lifecycle_executions SET id = ?, started_at = ? WHERE id = ?`, id, at.UTC().Format("2006-01-02 15:04:05.999999999"), oldID)
	return err
}

func (f *fixtures) FailExecutionReplayWrites(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_replay_insert BEFORE INSERT ON execution_replay_messages BEGIN SELECT RAISE(FAIL, 'injected replay failure'); END`)
	return err
}
func (f *fixtures) FailSteeringPreparation(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_steering_prepare BEFORE UPDATE ON thread_inputs WHEN NEW.input_mode = 'steering' BEGIN SELECT RAISE(FAIL, 'injected preparation failure'); END`)
	return err
}
func (f *fixtures) CountProjectExecutions(ctx context.Context, projectID string) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM executions e JOIN tasks t ON t.id=e.task_id WHERE t.project_id=?`, projectID).Scan(&count)
	return count, err
}

func (f *fixtures) SetInputEditHoldUntil(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE thread_inputs SET edit_hold_until = ? WHERE id = ?`, id, at)
}

func (f *fixtures) WebhookEndpoints(ctx context.Context, projectID string) ([]models.WebhookEndpoint, error) {
	return sqlite.NewWebhookRepo(f.db).ListByProject(ctx, projectID)
}
func (f *fixtures) TaskAgentAssignments(ctx context.Context, taskID string) ([]models.TaskAgentAssignment, error) {
	return sqlite.NewWebhookRepo(f.db).GetTaskAgentAssignments(ctx, taskID)
}
func (f *fixtures) SetLegacyWebhookPriority(ctx context.Context, id string, priority int) error {
	_, err := f.db.ExecContext(ctx, `UPDATE webhook_endpoints SET default_priority = ? WHERE id = ?`, priority, id)
	return err
}

func (f *fixtures) RemoveAllProjects(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `DELETE FROM projects WHERE 1 = 1`)
	return err
}
func (f *fixtures) SeedSavedChannelTargets(ctx context.Context, targets []models.ChannelTarget) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, v := range targets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO channel_targets (id,project_id,platform,target_kind,name,target_id,thread_id,is_home,default_subject,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`, v.ID, v.ProjectID, v.Platform, v.TargetKind, v.Name, v.TargetID, v.ThreadID, v.Home, v.DefaultSubject, v.UpdatedAt.UTC().Format("2006-01-02 15:04:05")); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *fixtures) CountAlertsWithIdempotencyKey(ctx context.Context, projectID, key string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE project_id=? AND idempotency_key=?`, projectID, key).Scan(&n)
	return n, err
}
func (f *fixtures) PublishedAutomationVersionID(ctx context.Context, id string) (string, error) {
	var version string
	err := f.db.QueryRowContext(ctx, `SELECT published_version_id FROM automations WHERE id=?`, id).Scan(&version)
	return version, err
}
func (f *fixtures) SetScheduleNextRun(ctx context.Context, id string, next *time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE schedules SET next_run=? WHERE id=?`, next, id)
	return err
}

func (f *fixtures) SetLegacyModelName(ctx context.Context, id, name string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE agent_configs SET name=? WHERE id=?`, name, id)
	return err
}

func (f *fixtures) FailTaskActiveLaneMove(ctx context.Context, id string) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_fixture_active_lane BEFORE UPDATE ON tasks WHEN OLD.id = `+sqliteLiteral(id)+` AND NEW.category = 'active' BEGIN SELECT RAISE(FAIL, 'forced handler later move failure'); END`)
	return err
}
func (f *fixtures) SetAgentArchivedAt(ctx context.Context, id string, at time.Time) error {
	return f.setTime(ctx, `UPDATE agents SET archived_at = ? WHERE id = ?`, id, at)
}

func (f *fixtures) FailTaskAttachmentCreation(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_fixture_attachment_insert BEFORE INSERT ON task_attachments BEGIN SELECT RAISE(ABORT, 'forced attachment insert failure'); END`)
	return err
}

func (f *fixtures) FailExecutionCancellation(ctx context.Context, id string) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_fixture_execution_cancel BEFORE UPDATE OF status ON executions WHEN OLD.id = `+sqliteLiteral(id)+` AND NEW.status = 'cancelled' BEGIN SELECT RAISE(ABORT, 'forced execution cancellation failure'); END`)
	return err
}

func (f *fixtures) SetTaskProject(ctx context.Context, id, projectID string) error {
	result, err := f.db.ExecContext(ctx, `UPDATE tasks SET project_id = ? WHERE id = ?`, projectID, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return persistence.ErrNotFound
	}
	return nil
}

func (f *fixtures) SeedForwardedGitHubFeedback(ctx context.Context, v models.GitHubPRFeedbackForwarded) error {
	_, err := f.db.ExecContext(ctx, `INSERT INTO github_pr_feedback_forwarded (task_pull_request_id,task_id,repo_full_name,pr_number,feedback_kind,github_id,author_login,body,created_at) VALUES (?,?,?,?,?,?,?,?,?)`, v.TaskPullRequestID, v.TaskID, v.RepoFullName, v.PRNumber, v.FeedbackKind, v.GitHubID, v.AuthorLogin, v.Body, v.CreatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (f *fixtures) ObserveAgentDeclarationWrites(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `
		CREATE TEMP TABLE declaration_write_counts (kind TEXT NOT NULL);
		CREATE TEMP TRIGGER count_agent_updates AFTER UPDATE ON agents BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('agent_update');
		END;
		CREATE TEMP TRIGGER count_hook_inserts AFTER INSERT ON agent_lifecycle_hooks BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('hook_insert');
		END;
		CREATE TEMP TRIGGER count_hook_updates AFTER UPDATE ON agent_lifecycle_hooks BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('hook_update');
		END;
		CREATE TEMP TRIGGER count_hook_deletes AFTER DELETE ON agent_lifecycle_hooks BEGIN
			INSERT INTO declaration_write_counts(kind) VALUES ('hook_delete');
		END;
	`)
	return err
}
func (f *fixtures) AgentDeclarationWrites(ctx context.Context) (int, error) {
	var count int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM declaration_write_counts`).Scan(&count)
	return count, err
}

func (f *fixtures) SetTaskCreationFailure(ctx context.Context, title string, enabled bool) error {
	if _, err := f.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fixture_fail_task_creation`); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_fail_task_creation BEFORE INSERT ON tasks WHEN NEW.title = `+sqliteLiteral(title)+` BEGIN SELECT RAISE(ABORT, 'forced task creation failure'); END`)
	return err
}
func (f *fixtures) SetStaleProjectDefaultModel(ctx context.Context, projectID, modelID string) (err error) {
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		err = errors.Join(err, restoreErr)
	}()
	_, err = conn.ExecContext(ctx, `UPDATE projects SET default_agent_config_id=? WHERE id=?`, modelID, projectID)
	return err
}

func (f *fixtures) FailExecutionCreation(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_fail_execution_creation BEFORE INSERT ON executions BEGIN SELECT RAISE(FAIL, 'forced execution create failure'); END`)
	return err
}
func (f *fixtures) FailTelegramProjectSelectionWrites(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_telegram_user_project_insert BEFORE INSERT ON telegram_user_projects BEGIN SELECT RAISE(ABORT, 'forced telegram user project write failure'); END;
 CREATE TRIGGER fail_telegram_user_project_update BEFORE UPDATE ON telegram_user_projects BEGIN SELECT RAISE(ABORT, 'forced telegram user project write failure'); END;`)
	return err
}

func (f *fixtures) SeedTelegramAuthorizedUsers(ctx context.Context, users []models.TelegramAuthorizedUser) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO telegram_authorized_users (project_id, telegram_user_id, display_name, added_by) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, u := range users {
		if _, err = stmt.ExecContext(ctx, u.ProjectID, u.TelegramUserID, u.DisplayName, u.AddedBy); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *fixtures) SetProjectDefault(ctx context.Context, id string, value bool) error {
	_, err := f.db.ExecContext(ctx, `UPDATE projects SET is_default=? WHERE id=?`, value, id)
	return err
}
func (f *fixtures) FailXTaskOriginWrites(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_x_origin BEFORE UPDATE OF created_via ON tasks WHEN NEW.created_via = 'x' BEGIN SELECT RAISE(ABORT, 'forced X origin failure'); END`)
	return err
}
func (f *fixtures) FailXTaskContextWrites(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_x_context BEFORE INSERT ON x_task_context BEGIN SELECT RAISE(ABORT, 'forced X context failure'); END`)
	return err
}

func (f *fixtures) CountTasks(ctx context.Context) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks`).Scan(&n)
	return n, err
}

// SetProjectDeletionFailure fails the final delete, after dependent cleanup has begun.
func (f *fixtures) SetProjectDeletionFailure(ctx context.Context, id string, enabled bool) error {
	if _, err := f.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fixture_fail_project_deletion`); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_fail_project_deletion BEFORE DELETE ON projects WHEN OLD.id = '`+strings.ReplaceAll(id, "'", "''")+`' BEGIN SELECT RAISE(ABORT, 'forced project deletion failure'); END`)
	return err
}

func (f *fixtures) CountProjectAgentMutations(ctx context.Context, id string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_config_mutations WHERE project_id = ?`, id).Scan(&n)
	return n, err
}

func (f *fixtures) SetExecutionReasoning(ctx context.Context, id, content string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE executions SET reasoning_content = ? WHERE id = ?`, content, id)
	return err
}
func (f *fixtures) FailTaskCompletion(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER reject_task_completion BEFORE UPDATE OF status ON tasks WHEN NEW.status = 'completed' BEGIN SELECT RAISE(ABORT, 'task completion rejected'); END`)
	return err
}
func (f *fixtures) ExecutionUsage(ctx context.Context, id string) ([]storefixture.ExecutionUsageRecord, error) {
	rows, err := f.db.QueryContext(ctx, `SELECT total_tokens,status,operation FROM llm_usage_events WHERE execution_id = ? ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var records []storefixture.ExecutionUsageRecord
	for rows.Next() {
		var rec storefixture.ExecutionUsageRecord
		if err := rows.Scan(&rec.TotalTokens, &rec.Status, &rec.Operation); err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, rows.Err()
}

func (f *fixtures) SetExecutionTerminalState(ctx context.Context, id string, status models.ExecutionStatus, completedAt time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE executions SET status = ?, completed_at = ? WHERE id = ?`, status, completedAt, id)
	return err
}

func (f *fixtures) SeedSlackAuthorizedUsers(ctx context.Context, users []models.SlackAuthorizedUser) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, u := range users {
		if _, err = tx.ExecContext(ctx, `INSERT INTO slack_authorized_users(project_id,slack_user_id,display_name,added_by) VALUES (?,?,?,?)`, u.ProjectID, u.SlackUserID, u.DisplayName, u.AddedBy); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (f *fixtures) SetSlackProjectSelectionUnavailable(ctx context.Context, unavailable bool) error {
	query := `ALTER TABLE slack_user_projects_unavailable RENAME TO slack_user_projects`
	if unavailable {
		query = `ALTER TABLE slack_user_projects RENAME TO slack_user_projects_unavailable`
	}
	_, err := f.db.ExecContext(ctx, query)
	return err
}
func (f *fixtures) FailSettingWrites(ctx context.Context, key string) error {
	for _, event := range []string{"INSERT", "UPDATE"} {
		_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_fail_setting_`+event+` BEFORE `+event+` ON app_settings WHEN NEW.key = '`+strings.ReplaceAll(key, "'", "''")+`' BEGIN SELECT RAISE(ABORT, 'forced settings write failure'); END`)
		if err != nil {
			return err
		}
	}
	return nil
}

func (f *fixtures) ProjectExecutions(ctx context.Context, projectID string, limit int) ([]models.Execution, error) {
	return sqlite.NewExecutionRepo(f.db).ListByProject(ctx, projectID, limit)
}

func (f *fixtures) SetTaskPromptCreationFailure(ctx context.Context, fragment string, enabled bool) error {
	if _, err := f.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fixture_SetTaskPromptCreationFailure`); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_SetTaskPromptCreationFailure BEFORE INSERT ON tasks WHEN instr(NEW.prompt, '`+strings.ReplaceAll(fragment, "'", "''")+`') > 0 BEGIN SELECT RAISE(FAIL, 'forced durable creation failure'); END`)
	return err
}

func (f *fixtures) SetQueuedInputCreationFailure(ctx context.Context, fragment string, enabled bool) error {
	if _, err := f.db.ExecContext(ctx, `DROP TRIGGER IF EXISTS fixture_SetQueuedInputCreationFailure`); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fixture_SetQueuedInputCreationFailure BEFORE INSERT ON thread_inputs WHEN instr(NEW.content, '`+strings.ReplaceAll(fragment, "'", "''")+`') > 0 BEGIN SELECT RAISE(FAIL, 'forced durable creation failure'); END`)
	return err
}

// SeedProjects preserves supplied identities and default flags for historical/test catalogs.
func (f *fixtures) SeedProjects(ctx context.Context, projects []models.Project) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range projects {
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id,name,description,repo_path,repo_url,is_default) VALUES (?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.RepoPath, p.RepoURL, p.IsDefault); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f *fixtures) SetTaskDisplayOrder(ctx context.Context, id string, order int) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET display_order = ? WHERE id = ?`, order, id)
	return err
}

func (f *fixtures) ChannelProjectSelections(ctx context.Context) (storefixture.ChannelProjectSelectionCounts, error) {
	var counts storefixture.ChannelProjectSelectionCounts
	err := f.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM discord_user_projects),(SELECT COUNT(*) FROM slack_user_projects),(SELECT COUNT(*) FROM telegram_user_projects),(SELECT COUNT(*) FROM email_sender_projects)`).Scan(&counts.Discord, &counts.Slack, &counts.Telegram, &counts.Email)
	return counts, err
}
func (f *fixtures) PendingSteering(ctx context.Context, runID, turnID string) ([]models.ThreadInput, error) {
	return sqlite.NewThreadInputRepo(f.db).ListPendingSteering(ctx, runID, turnID)
}

func (f *fixtures) MakeProjectDetailsUnreadable(ctx context.Context, id string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE projects SET created_at = 'not-a-timestamp', updated_at = 'not-a-timestamp' WHERE id = ?`, id)
	return err
}

func (f *fixtures) SetTaskCategory(ctx context.Context, id string, category models.TaskCategory) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET category = ? WHERE id = ?`, category, id)
	return err
}

// SuspendIO reserves each single-connection pool without exposing the handles.
func SuspendIO(pools ...*sql.DB) func(context.Context) (func() error, error) {
	return func(ctx context.Context) (func() error, error) {
		var conns []*sql.Conn
		var once sync.Once
		var releaseErr error
		release := func() error {
			once.Do(func() {
				for _, conn := range conns {
					releaseErr = errors.Join(releaseErr, conn.Close())
				}
			})
			return releaseErr
		}
		for _, pool := range pools {
			conn, err := pool.Conn(ctx)
			if err != nil {
				return nil, errors.Join(err, release())
			}
			conns = append(conns, conn)
		}
		return release, nil
	}
}
func (f *fixtures) SetSlackAuthorizationUnavailable(ctx context.Context, unavailable bool) error {
	query := `ALTER TABLE slack_authorized_users RENAME TO slack_authorized_users_unavailable`
	if !unavailable {
		query = `ALTER TABLE slack_authorized_users_unavailable RENAME TO slack_authorized_users`
	}
	_, err := f.db.ExecContext(ctx, query)
	return err
}

func (f *fixtures) AsyncToolCallStatus(ctx context.Context, id string) (string, error) {
	var status string
	err := f.db.QueryRowContext(ctx, `SELECT status FROM openai_async_tool_calls WHERE id = ?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", persistence.ErrNotFound
	}
	return status, err
}

func (f *fixtures) ClearProjects(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `DELETE FROM projects`)
	return err
}

func (f *fixtures) FailClearingSetting(ctx context.Context, key string) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER fail_channel_setting_reset BEFORE UPDATE ON app_settings WHEN NEW.key = `+sqliteLiteral(key)+` AND NEW.value = '' BEGIN SELECT RAISE(FAIL, 'injected settings write failure'); END`)
	return err
}
func (f *fixtures) FailConflictStatus(ctx context.Context) error {
	_, err := f.db.ExecContext(ctx, `CREATE TRIGGER reject_conflict_status BEFORE UPDATE OF merge_status ON tasks WHEN NEW.merge_status = 'conflict' BEGIN SELECT RAISE(ABORT, 'injected conflict status failure'); END`)
	return err
}
func (f *fixtures) MoveTaskProject(ctx context.Context, id, project string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET project_id=? WHERE id=?`, project, id)
	return err
}
func (f *fixtures) SetTaskDates(ctx context.Context, id string, created, updated time.Time, completed *time.Time) error {
	_, err := f.db.ExecContext(ctx, `UPDATE tasks SET created_at=?,updated_at=?,completed_at=? WHERE id=?`, created, updated, completed, id)
	return err
}
func (f *fixtures) LinkHistoricalAlertTask(ctx context.Context, project, id, task string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE alerts SET implementation_task_id=?,implementation_task_was_linked=1 WHERE id=? AND project_id=?`, task, id, project)
	return err
}
func (f *fixtures) FailCapacitySources(ctx context.Context) error {
	for _, q := range []string{`ALTER TABLE projects RENAME TO unavailable_projects_fixture`, `ALTER TABLE agent_configs RENAME TO unavailable_agent_configs_fixture`} {
		if _, err := f.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func (f *fixtures) SetDefaultProject(ctx context.Context, id string) error {
	_, err := f.db.ExecContext(ctx, `UPDATE projects SET is_default=(id=?)`, id)
	return err
}
