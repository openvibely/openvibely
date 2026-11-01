package sqlite

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"strings"
)

func (r *ProjectComputerRepo) RequestLogin(ctx context.Context, id, kind, url string) error {
	return r.RequestHandoff(ctx, id, kind, url, "", "")
}

// The Inbox item and the private desktop hold are committed together.
func (r *ProjectComputerRepo) RequestHandoff(ctx context.Context, id, kind, url, reason, expected string) error {
	if r.taskID == "" {
		return errors.New("handoff requires a task desktop")
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var previous string
		if err := tx.QueryRowContext(ctx, `SELECT handoff_kind FROM task_computer_sessions WHERE project_id=? AND task_id=?`, id, r.taskID).Scan(&previous); err != nil {
			return err
		}
		if previous != "" {
			return errors.New("a computer request is already awaiting your response")
		}
		title := "Computer sign-in requested"
		if kind == "request_computer_help" {
			title = "Help with task computer"
		}
		var alertID string
		err := tx.QueryRowContext(ctx, `INSERT INTO alerts(project_id,scope,task_id,type,severity,title,message,body,source,metadata_json,decision_state,processing_state) VALUES (?,'project',?,'custom','info',?,?,?,'project_computer','{}','pending','not_applicable') RETURNING id`, id, r.taskID, title, reason, "Open the task computer to review this request. Completing it resumes this task.").Scan(&alertID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE task_computer_sessions SET held=1,owner='',lease_until=0,handoff_kind=?,handoff_url=?,handoff_reason=?,handoff_expected_text=?,handoff_alert_id=? WHERE project_id=? AND task_id=?`, kind, url, reason, expected, alertID, id, r.taskID)
		return err
	})
}
func (r *ProjectComputerRepo) CompleteLogin(ctx context.Context, id string) error {
	return r.FinishHandoff(ctx, id, false)
}

// Queueing continuation and clearing the hold are atomic. Duplicate clicks cannot resume twice.
func (r *ProjectComputerRepo) FinishHandoff(ctx context.Context, id string, cancelled bool) error {
	unlock := persistence.LockTaskLifecycle(r.taskID)
	defer unlock()
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var kind, alertID string
		if err := tx.QueryRowContext(ctx, `SELECT handoff_kind,handoff_alert_id FROM task_computer_sessions WHERE project_id=? AND task_id=? AND held=1`, id, r.taskID).Scan(&kind, &alertID); err != nil {
			return err
		}
		if kind == "" {
			return errors.New("no computer request is awaiting completion")
		}
		decision := "approved"
		message := "The user completed your computer request. Continue the original task using its computer. Inspect the current page before acting."
		if cancelled {
			decision = "rejected"
			message = "The user declined your computer request. Do not retry that request automatically. Continue only with available access, or explain what is blocked."
		}
		if err := NewThreadInputRepo(r.db).createQueuedWithExecutor(ctx, tx, &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: id, TaskID: r.taskID, Content: message, Source: "project_computer", ChatMode: models.ChatModeOrchestrate}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE alerts SET decision_state=?,decided_at=CURRENT_TIMESTAMP,processing_state='completed',updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND source='project_computer'`, decision, alertID, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE task_computer_sessions SET handoff_kind='',handoff_url='',handoff_reason='',handoff_expected_text='',handoff_alert_id='',held=0 WHERE project_id=? AND task_id=?`, id, r.taskID)
		return err
	})
}
func (r *ProjectComputerRepo) SetToolTarget(ctx context.Context, id, target string) error {
	if r.taskID == "" || (target != "worktree" && target != "computer") {
		return errors.New("choose worktree or computer for a task")
	}
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE task_computer_sessions SET tool_target=? WHERE task_id=? AND project_id=?`, strings.TrimSpace(target), r.taskID, id)
		return err
	})
}
