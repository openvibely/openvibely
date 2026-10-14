// Package sqlitefixtures implements test state manipulation for SQLite.
package sqlitefixtures

import (
	"context"
	"database/sql"
	"strings"
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
	result, err := f.db.ExecContext(ctx, query, at.UTC(), id)
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
