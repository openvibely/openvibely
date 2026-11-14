package sqlite

import (
	"context"
	"database/sql"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"time"
)

type ProjectComputerRepo struct {
	db     *sql.DB
	taskID string
}

func (r *ProjectRepo) Computers() persistence.ProjectComputerStore {
	return &ProjectComputerRepo{db: r.db}
}
func (r *ProjectComputerRepo) Get(ctx context.Context, id string) (*models.ProjectComputer, error) {
	if r.taskID != "" {
		return r.getSession(ctx, id)
	}
	c := &models.ProjectComputer{}
	err := r.db.QueryRowContext(ctx, `SELECT project_id,provider,held,owner,lease_until,enabled,time_zone,takeover_pending,idle_minutes,cleanup_required FROM project_computers WHERE project_id=?`, id).Scan(&c.ProjectID, &c.Provider, &c.Held, &c.Owner, &c.LeaseUntil, &c.Enabled, &c.TimeZone, &c.TakeoverPending, &c.IdleMinutes, &c.CleanupRequired)
	return c, err
}
func (r *ProjectComputerRepo) Configure(ctx context.Context, id, provider string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO project_computers(project_id,provider) VALUES (?,?) ON CONFLICT(project_id) DO NOTHING`, id, provider)
		return err
	})
}
func (r *ProjectComputerRepo) Hold(ctx context.Context, id string, held bool) error {
	if r.taskID != "" {
		return r.sessionHold(ctx, id, held)
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		res, err := tx.ExecContext(ctx, `UPDATE project_computers SET held=?,owner='',lease_until=0 WHERE project_id=?`, held, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	})
}
func (r *ProjectComputerRepo) Reserve(ctx context.Context, id, owner string) error {
	if r.taskID != "" {
		return r.sessionReserve(ctx, id, owner)
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		now := time.Now().Unix()
		res, err := tx.ExecContext(ctx, `UPDATE project_computers SET owner=?,lease_until=? WHERE project_id=? AND held=0 AND (owner='' OR owner=? OR lease_until<?)`, owner, now+300, id, owner, now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return persistence.ErrComputerBusy
		}
		return err
	})
}
func (r *ProjectComputerRepo) Release(ctx context.Context, id, owner string) error {
	if r.taskID != "" {
		return r.sessionRelease(ctx, id, owner)
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE project_computers SET owner='',lease_until=0 WHERE project_id=? AND owner=?`, id, owner)
		return err
	})
}

// Renew never acquires a released desktop; it only extends the same live turn.
func (r *ProjectComputerRepo) Renew(ctx context.Context, id, owner string) error {
	if r.taskID != "" {
		return r.sessionRenew(ctx, id, owner)
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE project_computers SET lease_until=? WHERE project_id=? AND owner=? AND held=0`, time.Now().Unix()+300, id, owner)
		return err
	})
}

func (r *ProjectComputerRepo) sessionHold(ctx context.Context, id string, held bool) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		if !held {
			var resume bool
			if err := tx.QueryRowContext(ctx, `SELECT resume_after_takeover=1 AND held=1 AND handoff_kind='' FROM task_computer_sessions WHERE project_id=? AND task_id=?`, id, r.taskID).Scan(&resume); err != nil {
				return err
			}
			if resume {
				if err := NewThreadInputRepo(r.db).createQueuedWithExecutor(ctx, tx, &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: id, TaskID: r.taskID, Content: "The user returned control of your computer. Continue the original task. Inspect the current screen before acting.", Source: "project_computer", ChatMode: models.ChatModeOrchestrate}); err != nil {
					return err
				}
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE task_computer_sessions SET resume_after_takeover=CASE WHEN ?=0 THEN 0 WHEN held=0 AND handoff_kind='' AND EXISTS(SELECT 1 FROM tasks WHERE id=? AND status IN ('running','queued')) THEN 1 ELSE resume_after_takeover END,held=?,owner='',lease_until=0 WHERE project_id=? AND task_id=?`, held, r.taskID, held, id, r.taskID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	})
}

func (r *ProjectComputerRepo) sessionReserve(ctx context.Context, id, owner string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		now := time.Now().Unix()
		res, err := tx.ExecContext(ctx, `UPDATE task_computer_sessions SET owner=?,lease_until=? WHERE project_id=? AND task_id=? AND held=0 AND (owner='' OR owner=? OR lease_until<?)`, owner, now+300, id, r.taskID, owner, now)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err == nil && n == 0 {
			return persistence.ErrComputerBusy
		}
		return err
	})
}

func (r *ProjectComputerRepo) sessionRelease(ctx context.Context, id, owner string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE task_computer_sessions SET owner='',lease_until=0 WHERE project_id=? AND task_id=? AND owner=?`, id, r.taskID, owner)
		return err
	})
}

func (r *ProjectComputerRepo) sessionRenew(ctx context.Context, id, owner string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE task_computer_sessions SET lease_until=? WHERE project_id=? AND task_id=? AND owner=? AND held=0`, time.Now().Unix()+300, id, r.taskID, owner)
		return err
	})
}

func (r *ProjectComputerRepo) ForTask(ctx context.Context, projectID, taskID string) (persistence.ProjectComputerStore, error) {
	var valid int
	if err := r.db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE id=? AND project_id=?", taskID, projectID).Scan(&valid); err != nil {
		return nil, err
	}
	if valid != 1 {
		return nil, sql.ErrNoRows
	}
	scoped := &ProjectComputerRepo{db: r.db, taskID: taskID}
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO task_computer_sessions(task_id,project_id) SELECT ?,project_id FROM project_computers WHERE project_id=? ON CONFLICT(task_id) DO NOTHING`, taskID, projectID)
		return err
	})
	return scoped, err
}
func (r *ProjectComputerRepo) getSession(ctx context.Context, id string) (*models.ProjectComputer, error) {
	c := &models.ProjectComputer{}
	err := r.db.QueryRowContext(ctx, `SELECT s.project_id,p.provider,s.held,s.owner,s.lease_until,s.handoff_kind,s.handoff_url,s.handoff_reason,s.handoff_expected_text,s.handoff_alert_id,s.tool_target,p.enabled,p.time_zone,s.takeover_pending,p.idle_minutes FROM task_computer_sessions s JOIN project_computers p ON p.project_id=s.project_id WHERE s.project_id=? AND s.task_id=?`, id, r.taskID).Scan(&c.ProjectID, &c.Provider, &c.Held, &c.Owner, &c.LeaseUntil, &c.HandoffKind, &c.HandoffURL, &c.HandoffReason, &c.HandoffExpectedText, &c.HandoffAlertID, &c.ToolTarget, &c.Enabled, &c.TimeZone, &c.TakeoverPending, &c.IdleMinutes)
	return c, err
}
func (r *ProjectComputerRepo) SessionsBusy(ctx context.Context, id string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM task_computer_sessions WHERE project_id=? AND (held=1 OR (owner<>'' AND lease_until>=?))`, id, time.Now().Unix()).Scan(&count)
	return count > 0, err
}

// TransferRoot resolves only the current task's worktree, falling back to its project repo.
func (r *ProjectComputerRepo) TransferRoot(ctx context.Context, projectID string) (string, error) {
	var path string
	err := r.db.QueryRowContext(ctx, `SELECT CASE WHEN COALESCE(t.worktree_path,'')<>'' THEN t.worktree_path ELSE p.repo_path END FROM tasks t JOIN projects p ON p.id=t.project_id WHERE t.id=? AND t.project_id=?`, r.taskID, projectID).Scan(&path)
	return path, err
}

func (r *ProjectComputerRepo) Attachments() persistence.AttachmentStore {
	return NewAttachmentRepo(r.db)
}

func (r *ProjectComputerRepo) SetEnabled(ctx context.Context, id string, enabled bool) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE project_computers SET enabled=? WHERE project_id=?`, enabled, id)
		return err
	})
}

func (r *ProjectComputerRepo) SetTimeZone(ctx context.Context, id, zone string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE project_computers SET time_zone=? WHERE project_id=?`, zone, id)
		return err
	})
}

func (r *ProjectComputerRepo) SetTakeoverPending(ctx context.Context, id string, pending bool) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		query := `UPDATE project_computers SET takeover_pending=? WHERE project_id=?`
		args := []any{pending, id}
		if r.taskID != "" {
			query = `UPDATE task_computer_sessions SET takeover_pending=? WHERE project_id=? AND task_id=?`
			args = append(args, r.taskID)
		}
		result, err := tx.ExecContext(ctx, query, args...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	})
}

func (r *ProjectComputerRepo) SetIdleMinutes(ctx context.Context, id string, minutes int) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE project_computers SET idle_minutes=? WHERE project_id=?`, minutes, id)
		return err
	})
}
func (r *ProjectComputerRepo) IdleDesktopCandidates(ctx context.Context, id string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT s.task_id FROM task_computer_sessions s JOIN tasks t ON t.id=s.task_id AND t.project_id=s.project_id WHERE s.project_id=? AND s.held=0 AND s.takeover_pending=0 AND s.handoff_kind='' AND (s.owner='' OR s.lease_until<?) AND t.status NOT IN ('running','queued')`, id, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Include all tasks, even those without a session row or with idle cleanup disabled.
func (r *ProjectComputerRepo) TaskIDsForDesktopCleanup(ctx context.Context, id string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM tasks WHERE project_id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var task string
		if err := rows.Scan(&task); err != nil {
			return nil, err
		}
		ids = append(ids, task)
	}
	return ids, rows.Err()
}

// Record intent before provisioning so an interrupted or partially failed start
// cannot leave resources behind without a cleanup record.
func (r *ProjectComputerRepo) MarkCleanupRequired(ctx context.Context, id string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		result, err := tx.ExecContext(ctx, `UPDATE project_computers SET cleanup_required=1 WHERE project_id=?`, id)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n == 0 {
			return sql.ErrNoRows
		}
		return err
	})
}
