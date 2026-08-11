package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// ChannelTargetRepo stores outbound channel destinations and send audit rows.
type ChannelTargetRepo struct {
	db *sql.DB
}

func NewChannelTargetRepo(db *sql.DB) *ChannelTargetRepo { return &ChannelTargetRepo{db: db} }

func (r *ChannelTargetRepo) ListByProject(ctx context.Context, projectID string) ([]models.ChannelTarget, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ?
		ORDER BY platform ASC, is_home DESC, name ASC, target_id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list channel targets: %w", err)
	}
	defer rows.Close()
	var targets []models.ChannelTarget
	for rows.Next() {
		t, err := scanChannelTarget(rows)
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

func (r *ChannelTargetRepo) GetByID(ctx context.Context, id string) (*models.ChannelTarget, error) {
	return r.findOne(ctx, `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE id = ?`, id)
}

func (r *ChannelTargetRepo) FindHome(ctx context.Context, projectID, platform string) (*models.ChannelTarget, error) {
	return r.findOne(ctx, `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND is_home = 1
		ORDER BY updated_at DESC LIMIT 1`, projectID, normalizeChannelTargetField(platform))
}

func (r *ChannelTargetRepo) FindByName(ctx context.Context, projectID, platform, name string) (*models.ChannelTarget, error) {
	return r.findOne(ctx, `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND name = ?`, projectID, normalizeChannelTargetField(platform), normalizeChannelTargetField(name))
}

func (r *ChannelTargetRepo) FindByTarget(ctx context.Context, projectID, platform, targetID, threadID string) (*models.ChannelTarget, error) {
	return r.findOne(ctx, `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND target_id = ? AND thread_id = ?`, projectID, normalizeChannelTargetField(platform), strings.TrimSpace(targetID), strings.TrimSpace(threadID))
}

func (r *ChannelTargetRepo) FindByTargetAndKind(ctx context.Context, projectID, platform, targetID, threadID, targetKind string) (*models.ChannelTarget, error) {
	return r.findOne(ctx, `
		SELECT id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, created_at, updated_at
		FROM channel_targets
		WHERE project_id = ? AND platform = ? AND target_id = ? AND thread_id = ? AND target_kind = ?`,
		projectID, normalizeChannelTargetField(platform), strings.TrimSpace(targetID), strings.TrimSpace(threadID), strings.TrimSpace(targetKind))
}

func (r *ChannelTargetRepo) Upsert(ctx context.Context, target models.ChannelTarget) error {
	return upsertChannelTarget(ctx, r.db, target)
}

func (r *ChannelTargetRepo) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM channel_targets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete channel target: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("channel target not found")
	}
	return nil
}

func (r *ChannelTargetRepo) DeleteForProject(ctx context.Context, projectID, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM channel_targets WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete channel target: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return fmt.Errorf("channel target not found")
	}
	return nil
}

func (r *ChannelTargetRepo) DeleteProjectExcept(ctx context.Context, projectID string, keepIDs []string) error {
	args := []interface{}{projectID}
	query := `DELETE FROM channel_targets WHERE project_id = ?`
	if len(keepIDs) > 0 {
		placeholders := make([]string, 0, len(keepIDs))
		for _, id := range keepIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		if len(placeholders) > 0 {
			query += ` AND id NOT IN (` + strings.Join(placeholders, ",") + `)`
		}
	}
	if _, err := r.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("delete removed channel targets: %w", err)
	}
	return nil
}

func (r *ChannelTargetRepo) ReplaceProjectTargets(ctx context.Context, projectID string, targets []models.ChannelTarget) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace channel targets: %w", err)
	}
	defer tx.Rollback()

	keepIDs := make([]string, 0, len(targets))
	for _, target := range targets {
		id := strings.TrimSpace(target.ID)
		if id == "" {
			return fmt.Errorf("channel target id is required")
		}
		keepIDs = append(keepIDs, id)
	}

	args := []interface{}{projectID}
	query := `DELETE FROM channel_targets WHERE project_id = ?`
	if len(keepIDs) > 0 {
		placeholders := make([]string, 0, len(keepIDs))
		for _, id := range keepIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query += ` AND id NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("delete removed channel targets: %w", err)
	}

	for _, target := range targets {
		target.ProjectID = projectID
		if err := upsertChannelTarget(ctx, tx, target); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace channel targets: %w", err)
	}
	return nil
}

func (r *ChannelTargetRepo) RecordSend(ctx context.Context, send models.ChannelMessageSend) error {
	if strings.TrimSpace(send.ID) == "" {
		return fmt.Errorf("channel message send id is required")
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO channel_message_sends (id, project_id, platform, target_kind, target_id, thread_id, requested_by_surface, requested_by_user, message_preview, success, error)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		send.ID, send.ProjectID, normalizeChannelTargetField(send.Platform), strings.TrimSpace(send.TargetKind), strings.TrimSpace(send.TargetID), strings.TrimSpace(send.ThreadID), strings.TrimSpace(send.RequestedBySurface), strings.TrimSpace(send.RequestedByUser), strings.TrimSpace(send.MessagePreview), send.Success, strings.TrimSpace(send.Error))
	if err != nil {
		return fmt.Errorf("record channel message send: %w", err)
	}
	return nil
}

func (r *ChannelTargetRepo) ListSendsByProject(ctx context.Context, projectID string) ([]models.ChannelMessageSend, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, platform, target_kind, target_id, thread_id, requested_by_surface, requested_by_user, message_preview, success, error, created_at
		FROM channel_message_sends
		WHERE project_id = ?
		ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list channel message sends: %w", err)
	}
	defer rows.Close()
	var sends []models.ChannelMessageSend
	for rows.Next() {
		var s models.ChannelMessageSend
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Platform, &s.TargetKind, &s.TargetID, &s.ThreadID, &s.RequestedBySurface, &s.RequestedByUser, &s.MessagePreview, &s.Success, &s.Error, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan channel message send: %w", err)
		}
		sends = append(sends, s)
	}
	return sends, rows.Err()
}

func (r *ChannelTargetRepo) findOne(ctx context.Context, query string, args ...interface{}) (*models.ChannelTarget, error) {
	row := r.db.QueryRowContext(ctx, query, args...)
	t, err := scanChannelTarget(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

type channelTargetScanner interface {
	Scan(dest ...interface{}) error
}

func scanChannelTarget(row channelTargetScanner) (models.ChannelTarget, error) {
	var t models.ChannelTarget
	if err := row.Scan(&t.ID, &t.ProjectID, &t.Platform, &t.TargetKind, &t.Name, &t.TargetID, &t.ThreadID, &t.Home, &t.DefaultSubject, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return t, err
		}
		return t, fmt.Errorf("scan channel target: %w", err)
	}
	return t, nil
}

func normalizeChannelTargetField(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func upsertChannelTarget(ctx context.Context, exec SQLExecutor, target models.ChannelTarget) error {
	if strings.TrimSpace(target.ID) == "" {
		return fmt.Errorf("channel target id is required")
	}
	platform := normalizeChannelTargetField(target.Platform)
	name := normalizeChannelTargetField(target.Name)
	targetKind := normalizeChannelTargetField(target.TargetKind)
	if targetKind == "" {
		targetKind = models.DefaultChannelTargetKind(platform)
	}
	if target.Home {
		if _, err := exec.ExecContext(ctx, `UPDATE channel_targets SET is_home = 0, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND platform = ?`, target.ProjectID, platform); err != nil {
			return fmt.Errorf("clear channel home target: %w", err)
		}
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO channel_targets (id, project_id, platform, target_kind, name, target_id, thread_id, is_home, default_subject, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			project_id = excluded.project_id,
			platform = excluded.platform,
			target_kind = excluded.target_kind,
			name = excluded.name,
			target_id = excluded.target_id,
			thread_id = excluded.thread_id,
			is_home = excluded.is_home,
			default_subject = excluded.default_subject,
			updated_at = CURRENT_TIMESTAMP`,
		strings.TrimSpace(target.ID), strings.TrimSpace(target.ProjectID), platform, targetKind, name, strings.TrimSpace(target.TargetID), strings.TrimSpace(target.ThreadID), target.Home, strings.TrimSpace(target.DefaultSubject))
	if err != nil {
		return fmt.Errorf("upsert channel target: %w", err)
	}
	return nil
}
