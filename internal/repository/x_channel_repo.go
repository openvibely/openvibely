package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type XAuthRepo struct{ db *sql.DB }

func NewXAuthRepo(db *sql.DB) *XAuthRepo { return &XAuthRepo{db: db} }

func (r *XAuthRepo) ListByProject(ctx context.Context, projectID string) ([]models.XAuthorizedUser, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, project_id, x_user_id, username, added_at FROM x_authorized_users WHERE project_id = ? ORDER BY username, x_user_id`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, fmt.Errorf("list X authorized users: %w", err)
	}
	defer rows.Close()
	var users []models.XAuthorizedUser
	for rows.Next() {
		var u models.XAuthorizedUser
		if err := rows.Scan(&u.ID, &u.ProjectID, &u.XUserID, &u.Username, &u.AddedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

func (r *XAuthRepo) IsAuthorized(ctx context.Context, projectID, userID string) (bool, error) {
	var found int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM x_authorized_users WHERE project_id = ? AND x_user_id = ? LIMIT 1`, strings.TrimSpace(projectID), strings.TrimSpace(userID)).Scan(&found)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

const xAuthorizedProjectQuery = `SELECT p.id
	FROM x_authorized_users AS au
	JOIN projects AS p ON p.id = au.project_id
	WHERE au.x_user_id = ?
	ORDER BY p.is_default DESC, p.name ASC
	LIMIT 1`

// FirstAuthorizedProject returns the first project that authorizes userID in
// the same order used by the project selector fallback.
func (r *XAuthRepo) FirstAuthorizedProject(ctx context.Context, userID string) (string, error) {
	var projectID string
	err := r.db.QueryRowContext(ctx, xAuthorizedProjectQuery, strings.TrimSpace(userID)).Scan(&projectID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find first X authorized project: %w", err)
	}
	return projectID, nil
}

func (r *XAuthRepo) Create(ctx context.Context, u *models.XAuthorizedUser) error {
	if u == nil || strings.TrimSpace(u.ProjectID) == "" || strings.TrimSpace(u.XUserID) == "" {
		return fmt.Errorf("project and X user ID are required")
	}
	u.ID = NewID()
	row := queryRowBoundSQLite(ctx, r.db, `INSERT INTO x_authorized_users(id, project_id, x_user_id, username) VALUES(?, ?, ?, ?) RETURNING added_at`, u.ID, strings.TrimSpace(u.ProjectID), strings.TrimSpace(u.XUserID), strings.TrimPrefix(strings.TrimSpace(u.Username), "@"))
	if err := row.Scan(&u.AddedAt); err != nil {
		return fmt.Errorf("create X authorized user: %w", err)
	}
	return nil
}

func (r *XAuthRepo) Delete(ctx context.Context, projectID, id string) error {
	res, err := execBoundSQLite(ctx, r.db, `DELETE FROM x_authorized_users WHERE id = ? AND project_id = ?`, strings.TrimSpace(id), strings.TrimSpace(projectID))
	if err != nil {
		return fmt.Errorf("delete X authorized user: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *XAuthRepo) CountByProject(ctx context.Context, projectID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM x_authorized_users WHERE project_id = ?`, strings.TrimSpace(projectID)).Scan(&n)
	return n, err
}

type XUserProjectRepo struct{ db *sql.DB }

func NewXUserProjectRepo(db *sql.DB) *XUserProjectRepo { return &XUserProjectRepo{db: db} }
func (r *XUserProjectRepo) SetUserProject(ctx context.Context, userID, projectID string) error {
	return upsertUserProject(ctx, r.db, "x_user_projects", "x_user_id", strings.TrimSpace(userID), strings.TrimSpace(projectID), "X user project")
}
func (r *XUserProjectRepo) GetUserProject(ctx context.Context, userID string) (string, error) {
	return getUserProject(ctx, r.db, "x_user_projects", "x_user_id", strings.TrimSpace(userID), "X user project")
}

type XTaskContextRepo struct{ db *sql.DB }

func NewXTaskContextRepo(db *sql.DB) *XTaskContextRepo { return &XTaskContextRepo{db: db} }

var xTaskContextLifecycle = taskContextLifecycle[models.XTaskContext]{
	table: "x_task_context", errLabel: "X task context",
	metadataColumns: []string{"project_id", "account_id", "conversation_id", "reply_to_tweet_id", "x_user_id", "username"},
	selectColumns:   "task_id, project_id, account_id, conversation_id, reply_to_tweet_id, x_user_id, username, created_at, updated_at",
	values: func(v models.XTaskContext) (string, []any) {
		return v.TaskID, []any{v.ProjectID, v.AccountID, v.ConversationID, v.ReplyToTweetID, v.XUserID, v.Username}
	},
	scan: func(row taskContextScanner) (models.XTaskContext, error) {
		var v models.XTaskContext
		err := row.Scan(&v.TaskID, &v.ProjectID, &v.AccountID, &v.ConversationID, &v.ReplyToTweetID, &v.XUserID, &v.Username, &v.CreatedAt, &v.UpdatedAt)
		return v, err
	},
}

func (r *XTaskContextRepo) Upsert(ctx context.Context, v *models.XTaskContext) error {
	if v == nil {
		return fmt.Errorf("X task context is nil")
	}
	return withBoundSQLiteConn(ctx, r.db, func(conn *sql.Conn) error { return r.UpsertWithExecutor(ctx, conn, v) })
}
func (r *XTaskContextRepo) UpsertWithExecutor(ctx context.Context, exec SQLExecutor, v *models.XTaskContext) error {
	if v == nil {
		return fmt.Errorf("X task context is nil")
	}
	return xTaskContextLifecycle.Upsert(ctx, exec, *v)
}
func (r *XTaskContextRepo) GetByTaskID(ctx context.Context, taskID string) (*models.XTaskContext, error) {
	return xTaskContextLifecycle.GetByTaskID(ctx, r.db, taskID)
}

type XReceiptClaimResult string

const (
	XReceiptClaimed   XReceiptClaimResult = "claimed"
	XReceiptActive    XReceiptClaimResult = "active"
	XReceiptCompleted XReceiptClaimResult = "completed"
)

var ErrXReceiptLeaseLost = fmt.Errorf("X receipt lease is no longer owned")

type XReceiptClaim struct {
	Result XReceiptClaimResult
	Token  string
}

type XInboundReceiptRepo struct{ db *sql.DB }

func NewXInboundReceiptRepo(db *sql.DB) *XInboundReceiptRepo { return &XInboundReceiptRepo{db: db} }
func (r *XInboundReceiptRepo) Claim(ctx context.Context, tweetID, projectID string, now time.Time, lease time.Duration) (XReceiptClaim, error) {
	var status string
	var leaseExpiresAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT status, lease_expires_at FROM x_inbound_receipts WHERE tweet_id = ?`, tweetID).Scan(&status, &leaseExpiresAt)
	if err != nil && err != sql.ErrNoRows {
		return XReceiptClaim{}, fmt.Errorf("load X receipt before claim: %w", err)
	}
	if err == nil {
		if status == "completed" {
			return XReceiptClaim{Result: XReceiptCompleted}, nil
		}
		if leaseExpiresAt.Valid && leaseExpiresAt.Time.After(now) {
			return XReceiptClaim{Result: XReceiptActive}, nil
		}
	}
	token := NewID()
	row := queryRowBoundSQLite(ctx, r.db, `INSERT INTO x_inbound_receipts(tweet_id, project_id, status, lease_expires_at, lease_token) VALUES(?, ?, 'processing', ?, ?)
		ON CONFLICT(tweet_id) DO UPDATE SET project_id=excluded.project_id, status='processing', lease_expires_at=excluded.lease_expires_at, lease_token=excluded.lease_token, updated_at=datetime('now')
		WHERE x_inbound_receipts.status='processing' AND x_inbound_receipts.lease_expires_at <= ? RETURNING tweet_id`, tweetID, projectID, now.Add(lease).UTC(), token, now.UTC())
	var id string
	err = row.Scan(&id)
	if err == sql.ErrNoRows {
		// A concurrent claimant or completer won after the advisory read.
		err = r.db.QueryRowContext(ctx, `SELECT status FROM x_inbound_receipts WHERE tweet_id = ?`, tweetID).Scan(&status)
		if err != nil {
			return XReceiptClaim{}, fmt.Errorf("reload X receipt after claim conflict: %w", err)
		}
		if status == "completed" {
			return XReceiptClaim{Result: XReceiptCompleted}, nil
		}
		return XReceiptClaim{Result: XReceiptActive}, nil
	}
	if err != nil {
		return XReceiptClaim{}, fmt.Errorf("claim X receipt: %w", err)
	}
	return XReceiptClaim{Result: XReceiptClaimed, Token: token}, nil
}

// CompleteWithHandoff commits durable ingress work and receipt completion in one
// immediate transaction. The lease token prevents an expired claimant from
// completing or releasing a receipt reclaimed by a newer worker.
func (r *XInboundReceiptRepo) CompleteWithHandoff(ctx context.Context, tweetID, token string, taskID *string, persist func(SQLExecutor) error) (alreadyHandedOff bool, err error) {
	err = withImmediateTx(ctx, r.db, func(tx SQLExecutor) error {
		var status, currentToken string
		if err := tx.QueryRowContext(ctx, `SELECT status, lease_token FROM x_inbound_receipts WHERE tweet_id = ?`, tweetID).Scan(&status, &currentToken); err != nil {
			return fmt.Errorf("load X receipt for handoff: %w", err)
		}
		if status == "completed" {
			alreadyHandedOff = true
			return nil
		}
		if token == "" || currentToken != token {
			return ErrXReceiptLeaseLost
		}
		if persist != nil {
			if err := persist(tx); err != nil {
				return err
			}
		}
		resolvedTaskID := ""
		if taskID != nil {
			resolvedTaskID = *taskID
		}
		res, err := tx.ExecContext(ctx, `UPDATE x_inbound_receipts SET status='completed', lease_expires_at=NULL, lease_token='', task_id=NULLIF(?, ''), updated_at=datetime('now') WHERE tweet_id=? AND status='processing' AND lease_token=?`, resolvedTaskID, tweetID, token)
		if err != nil {
			return err
		}
		changed, _ := res.RowsAffected()
		if changed != 1 {
			return ErrXReceiptLeaseLost
		}
		return nil
	})
	return alreadyHandedOff, err
}

func (r *XInboundReceiptRepo) Release(ctx context.Context, tweetID, token string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM x_inbound_receipts WHERE tweet_id=? AND status='processing' AND lease_token=?`, tweetID, token)
	return err
}
