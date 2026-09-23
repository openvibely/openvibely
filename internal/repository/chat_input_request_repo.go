package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type ChatInputRequestStatus string

const (
	ChatInputRequestPending  ChatInputRequestStatus = "pending"
	ChatInputRequestResolved ChatInputRequestStatus = "resolved"
)

type ChatInputRequestRecord struct {
	ID            string
	ProjectID     string
	ExecutionID   string
	QuestionsJSON string
	AnswersJSON   string
	Status        ChatInputRequestStatus
	CreatedAt     time.Time
	ExpiresAt     time.Time
	ResolvedAt    *time.Time
}

type ChatInputRequestRepo struct {
	db *sql.DB
}

func NewChatInputRequestRepo(db *sql.DB) *ChatInputRequestRepo {
	return &ChatInputRequestRepo{db: db}
}

func (r *ChatInputRequestRepo) Create(ctx context.Context, rec ChatInputRequestRecord) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("chat input request repository unavailable")
	}
	_, err := execBoundSQLite(ctx, r.db, `
		INSERT INTO chat_input_requests (id, project_id, execution_id, questions_json, expires_at)
		SELECT ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM executions WHERE id = ?)`, strings.TrimSpace(rec.ID), strings.TrimSpace(rec.ProjectID), strings.TrimSpace(rec.ExecutionID), rec.QuestionsJSON, rec.ExpiresAt.UTC(), strings.TrimSpace(rec.ExecutionID))
	if err != nil {
		return fmt.Errorf("creating chat input request: %w", err)
	}
	return nil
}

func (r *ChatInputRequestRepo) Get(ctx context.Context, id string) (*ChatInputRequestRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("chat input request repository unavailable")
	}
	return scanChatInputRequest(r.db.QueryRowContext(ctx, `
		SELECT id, project_id, execution_id, questions_json, answers_json, status, created_at, expires_at, resolved_at
		FROM chat_input_requests WHERE id = ?`, strings.TrimSpace(id)))
}

func (r *ChatInputRequestRepo) ListActiveByProject(ctx context.Context, projectID string, now time.Time) ([]ChatInputRequestRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("chat input request repository unavailable")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, execution_id, questions_json, answers_json, status, created_at, expires_at, resolved_at
		FROM chat_input_requests
		WHERE project_id = ? AND expires_at > ?
		ORDER BY created_at ASC, id ASC`, strings.TrimSpace(projectID), now.UTC())
	if err != nil {
		return nil, fmt.Errorf("listing chat input requests: %w", err)
	}
	defer rows.Close()
	var out []ChatInputRequestRecord
	for rows.Next() {
		rec, err := scanChatInputRequest(rows)
		if err != nil {
			return nil, err
		}
		if rec != nil {
			out = append(out, *rec)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating chat input requests: %w", err)
	}
	return out, nil
}

func (r *ChatInputRequestRepo) Resolve(ctx context.Context, id, projectID, answersJSON string) (*ChatInputRequestRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("chat input request repository unavailable")
	}
	now := time.Now().UTC()
	_, err := execBoundSQLite(ctx, r.db, `
		UPDATE chat_input_requests
		SET status = ?, answers_json = ?, resolved_at = ?
		WHERE id = ? AND project_id = ? AND status = ?`, string(ChatInputRequestResolved), answersJSON, now, strings.TrimSpace(id), strings.TrimSpace(projectID), string(ChatInputRequestPending))
	if err != nil {
		return nil, fmt.Errorf("resolving chat input request: %w", err)
	}
	return r.Get(ctx, id)
}

func scanChatInputRequest(scanner interface{ Scan(dest ...any) error }) (*ChatInputRequestRecord, error) {
	var rec ChatInputRequestRecord
	var status string
	var resolvedAt sql.NullTime
	if err := scanner.Scan(&rec.ID, &rec.ProjectID, &rec.ExecutionID, &rec.QuestionsJSON, &rec.AnswersJSON, &status, &rec.CreatedAt, &rec.ExpiresAt, &resolvedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning chat input request: %w", err)
	}
	rec.Status = ChatInputRequestStatus(status)
	if resolvedAt.Valid {
		resolved := resolvedAt.Time
		rec.ResolvedAt = &resolved
	}
	return &rec, nil
}
