package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"strings"
	"time"
)

type ChatInputRequestRepo struct {
	db *sql.DB
}

func NewChatInputRequestRepo(db *sql.DB) *ChatInputRequestRepo {
	return &ChatInputRequestRepo{db: db}
}

func (r *ChatInputRequestRepo) Create(ctx context.Context, rec persistence.ChatInputRequestRecord) error {
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

func (r *ChatInputRequestRepo) Get(ctx context.Context, id string) (*persistence.ChatInputRequestRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("chat input request repository unavailable")
	}
	return scanChatInputRequest(r.db.QueryRowContext(ctx, `
		SELECT id, project_id, execution_id, questions_json, answers_json, status, created_at, expires_at, resolved_at
		FROM chat_input_requests WHERE id = ?`, strings.TrimSpace(id)))
}

func (r *ChatInputRequestRepo) ListActiveByProject(ctx context.Context, projectID string, now time.Time) ([]persistence.ChatInputRequestRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("chat input request repository unavailable")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, execution_id, questions_json, answers_json, status, created_at, expires_at, resolved_at
		FROM chat_input_requests
		WHERE project_id = ?
		  AND (expires_at > ? OR json_extract(questions_json,'$.kind')='skill_installation')
		  AND (status='pending' OR json_extract(questions_json,'$.kind') IS NOT 'skill_installation')
		ORDER BY created_at ASC, id ASC`, strings.TrimSpace(projectID), now.UTC())
	if err != nil {
		return nil, fmt.Errorf("listing chat input requests: %w", err)
	}
	defer rows.Close()
	var out []persistence.ChatInputRequestRecord
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

func (r *ChatInputRequestRepo) Resolve(ctx context.Context, id, projectID, answersJSON string) (*persistence.ChatInputRequestRecord, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("chat input request repository unavailable")
	}
	err := withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		_, err := tx.ExecContext(ctx, `UPDATE chat_input_requests SET status=?,answers_json=?,resolved_at=? WHERE id=? AND project_id=? AND status=?`, string(persistence.ChatInputRequestResolved), answersJSON, time.Now().UTC(), strings.TrimSpace(id), strings.TrimSpace(projectID), string(persistence.ChatInputRequestPending))
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("resolving chat input request: %w", err)
	}
	return r.Get(ctx, id)
}

func scanChatInputRequest(scanner interface{ Scan(dest ...any) error }) (*persistence.ChatInputRequestRecord, error) {
	var rec persistence.ChatInputRequestRecord
	var status string
	var resolvedAt sql.NullTime
	if err := scanner.Scan(&rec.ID, &rec.ProjectID, &rec.ExecutionID, &rec.QuestionsJSON, &rec.AnswersJSON, &status, &rec.CreatedAt, &rec.ExpiresAt, &resolvedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning chat input request: %w", err)
	}
	rec.Status = persistence.ChatInputRequestStatus(status)
	if resolvedAt.Valid {
		resolved := resolvedAt.Time
		rec.ResolvedAt = &resolved
	}
	return &rec, nil
}

const chatInputColumns = `id, project_id, execution_id, questions_json, answers_json, status, created_at, expires_at, resolved_at`

func (r *ChatInputRequestRepo) ListByExecutions(ctx context.Context, project string, ids []string) ([]persistence.ChatInputRequestRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := []any{project}
	marks := make([]string, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+chatInputColumns+` FROM chat_input_requests WHERE project_id=? AND execution_id IN (`+strings.Join(marks, ",")+`) ORDER BY created_at,id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistence.ChatInputRequestRecord
	for rows.Next() {
		rec, e := scanChatInputRequest(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

func saveInstallationInput(ctx context.Context, tx sqlExecutor, rec *persistence.ChatInputRequestRecord, meta *persistence.ChatInstallationRequest) error {
	questions, _, err := rec.DecodeQuestions()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(persistence.ChatInputRequestPayload{Kind: "skill_installation", Questions: questions, Installation: meta})
	if err != nil {
		return err
	}
	rec.QuestionsJSON = string(raw)
	_, err = tx.ExecContext(ctx, `UPDATE chat_input_requests SET questions_json=? WHERE id=? AND project_id=?`, rec.QuestionsJSON, rec.ID, rec.ProjectID)
	return err
}

func installationAllowed(rec *persistence.ChatInputRequestRecord) (bool, error) {
	var answers []struct {
		QuestionID string `json:"question_id"`
		Label      string `json:"label"`
		Custom     string `json:"custom_answer"`
	}
	if rec.Status != persistence.ChatInputRequestResolved || json.Unmarshal([]byte(rec.AnswersJSON), &answers) != nil || len(answers) != 1 || answers[0].QuestionID != "install" || answers[0].Custom != "" {
		return false, errors.New("installation answer unavailable")
	}
	switch answers[0].Label {
	case "Install dependencies":
		return true, nil
	case "Not now":
		return false, nil
	}
	return false, errors.New("invalid installation answer")
}

// ClaimInstallationAnswer claims an answer for the live tool or creates its direct
// continuation execution atomically. No follow-up input is created.
func (r *ChatInputRequestRepo) ClaimInstallationAnswer(ctx context.Context, id, project string, continuation bool) (out *persistence.ChatInputRequestRecord, err error) {
	err = withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		return r.claimInstallationAnswer(ctx, tx, id, project, continuation, &out)
	})
	return
}

func (r *ChatInputRequestRepo) claimInstallationAnswer(ctx context.Context, tx sqlExecutor, id, project string, continuation bool, out **persistence.ChatInputRequestRecord) error {
	rec, e := scanChatInputRequest(tx.QueryRowContext(ctx, `SELECT `+chatInputColumns+` FROM chat_input_requests WHERE id=? AND project_id=?`, id, project))
	if e != nil {
		return e
	}
	if rec == nil {
		return sql.ErrNoRows
	}
	_, meta, e := rec.DecodeQuestions()
	if e != nil {
		return e
	}
	if meta == nil {
		return errors.New("not an installation question")
	}
	if meta.Handled || meta.ContinuationExecutionID != "" || rec.Status != persistence.ChatInputRequestResolved {
		return nil
	}
	_, e = installationAllowed(rec)
	if e != nil {
		return e
	}
	var task, status, category, model, agent, execStatus, prompt string
	e = tx.QueryRowContext(ctx, `SELECT t.id,t.status,t.category,COALESCE(e.agent_config_id,''),COALESCE(t.agent_definition_id,''),e.status,e.prompt_sent FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND t.project_id=?`, rec.ExecutionID, project).Scan(&task, &status, &category, &model, &agent, &execStatus, &prompt)
	if e != nil {
		return e
	}
	if (!continuation && (status == "cancelled" || execStatus == "cancelled")) || agent != meta.AgentID {
		return errors.New("task cancelled or Agent assignment changed")
	}
	if continuation {
		// Leave the answer durable but unclaimed until the current run has saved
		// its final transcript. Completion notifications try again.
		var active bool
		e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.status IN ('running','queued') AND (t.id=? OR (?='chat' AND t.category='chat' AND t.project_id=?)))`, task, category, project).Scan(&active)
		if e != nil {
			return e
		}
		if active {
			return nil
		}
		execution := &models.Execution{TaskID: task, AgentConfigID: model, Status: models.ExecQueued, PromptSent: prompt, IsFollowup: true}
		if category == "chat" {
			execution.Status = models.ExecRunning
		}
		if e = createExecutionWithExecutor(ctx, tx, execution); e != nil {
			return e
		}
		// Reactivate inactive tasks without invoking task-start or replaying the
		// task prompt. Chat and scheduled threads retain their existing category.
		if _, e = tx.ExecContext(ctx, `UPDATE tasks SET status='queued', category=CASE WHEN category IN ('backlog','completed') THEN 'active' ELSE category END, updated_at=CURRENT_TIMESTAMP WHERE id=?`, task); e != nil {
			return e
		}
		meta.ContinuationExecutionID = execution.ID
	} else {
		meta.Handled = true
	}
	if e = saveInstallationInput(ctx, tx, rec, meta); e != nil {
		return e
	}
	*out = rec
	return nil
}

func (r *ChatInputRequestRepo) LoadInstallationContinuation(ctx context.Context, project, execution, agent string) (out *persistence.ChatInputRequestRecord, err error) {
	err = withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		rec, e := scanChatInputRequest(tx.QueryRowContext(ctx, `SELECT `+chatInputColumns+` FROM chat_input_requests q WHERE project_id=? AND status='resolved' AND json_extract(questions_json,'$.kind')='skill_installation' AND json_extract(q.questions_json,'$.installation.continuation_execution_id')=? ORDER BY created_at LIMIT 1`, project, execution))
		if e != nil || rec == nil {
			return e
		}
		_, meta, e := rec.DecodeQuestions()
		if e != nil {
			return e
		}
		if meta.AgentID != agent {
			return errors.New("Agent assignment changed")
		}
		if _, e = installationAllowed(rec); e != nil {
			return e
		}
		var status, execStatus, currentAgent string
		if e = tx.QueryRowContext(ctx, `SELECT t.status,e.status,COALESCE(t.agent_definition_id,'') FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=? AND t.project_id=?`, execution, project).Scan(&status, &execStatus, &currentAgent); e != nil {
			return e
		}
		if status == "cancelled" || execStatus != "running" || currentAgent != agent {
			return errors.New("installation execution is not active")
		}
		// Do not mark complete until the actual result is durable.
		out = rec
		return nil
	})
	return
}

// Save the tool result with its answered question. Preserve the original user
// prompt on the execution so later retries use ordinary handling.
func (r *ChatInputRequestRepo) SaveInstallationContinuationResult(ctx context.Context, questionID, executionID, content string) error {
	return withImmediateTx(ctx, r.db, func(tx sqlExecutor) error {
		var savedExecutionID string
		err := tx.QueryRowContext(ctx, `SELECT e.id FROM chat_input_requests q JOIN executions e ON e.id=json_extract(q.questions_json,'$.installation.continuation_execution_id') JOIN tasks t ON t.id=e.task_id WHERE q.id=? AND e.id=? AND e.status='running' AND t.status!='cancelled'`, questionID, executionID).Scan(&savedExecutionID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE chat_input_requests SET questions_json=json_set(questions_json,'$.installation.handled',json('true'),'$.installation.result',?) WHERE id=?`, content, questionID)
		return err
	})
}

// Find committed answers still blocked by a pre-restart Chat
// execution. Ordinary task execution recovery already handles task threads.
func (r *ChatInputRequestRepo) ListInstallationAnswersAwaitingChatRecovery(ctx context.Context, expiresBefore time.Time) ([]persistence.ChatInputRequestRecord, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+chatInputColumns+` FROM chat_input_requests q
 WHERE status='resolved' AND expires_at < ?
 AND json_extract(questions_json,'$.kind')='skill_installation'
 AND COALESCE(json_extract(questions_json,'$.installation.handled'),0)=0
 AND COALESCE(json_extract(questions_json,'$.installation.continuation_execution_id'),'')=''
 AND EXISTS(SELECT 1 FROM executions e JOIN tasks t ON t.id=e.task_id WHERE e.id=q.execution_id AND e.status IN ('running','queued') AND t.category='chat' AND t.status!='cancelled')
 ORDER BY created_at,id LIMIT 100`, expiresBefore.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistence.ChatInputRequestRecord
	for rows.Next() {
		record, err := scanChatInputRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, rows.Err()
}

// ListReadyInstallationAnswers returns answered questions that have not been
// claimed by a live tool or a continuation. Scope filters keep completion work local.
func (r *ChatInputRequestRepo) ListReadyInstallationAnswers(ctx context.Context, project, task string) ([]persistence.ChatInputRequestRecord, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+chatInputColumns+` FROM chat_input_requests q WHERE status='resolved' AND json_extract(questions_json,'$.kind')='skill_installation' AND COALESCE(json_extract(questions_json,'$.installation.handled'),0)=0 AND COALESCE(json_extract(questions_json,'$.installation.continuation_execution_id'),'')='' AND (?='' OR project_id=?) AND (?='' OR EXISTS(SELECT 1 FROM executions e WHERE e.id=q.execution_id AND e.task_id=?)) ORDER BY created_at,id`, project, project, task, task)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []persistence.ChatInputRequestRecord
	for rows.Next() {
		rec, e := scanChatInputRequest(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}
