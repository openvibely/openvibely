package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	llmtranscript "github.com/openvibely/openvibely/internal/llm/transcript"
	"github.com/openvibely/openvibely/internal/models"
)

type ExecutionRepo struct {
	db             *sql.DB
	automationRepo *AutomationRepo
}

func NewExecutionRepo(db *sql.DB) *ExecutionRepo {
	return &ExecutionRepo{db: db}
}

func (r *ExecutionRepo) SetAutomationRepo(repo *AutomationRepo) {
	r.automationRepo = repo
}

func (r *ExecutionRepo) DB() *sql.DB {
	if r == nil {
		return nil
	}
	return r.db
}

const executionSelectColumns = `id, task_id, COALESCE(agent_config_id, ''), status, prompt_sent, output, reasoning_content, error_message,
		tokens_used, duration_ms, is_followup, starts_new_context, diff_output, cli_session_id, COALESCE(dispatch_id, ''), started_at, completed_at`

// executionSelectColumnsLight omits reasoning_content and diff_output (substituting
// empty strings) so list/pagination queries don't load potentially very large blobs.
// The scan shape matches executionSelectColumns, so scanExecutionRow still works; the
// resulting Execution will have ReasoningContent == "" and DiffOutput == "".
const executionSelectColumnsLight = `id, task_id, COALESCE(agent_config_id, ''), status, prompt_sent, output, '' AS reasoning_content, error_message,
		tokens_used, duration_ms, is_followup, starts_new_context, '' AS diff_output, cli_session_id, COALESCE(dispatch_id, ''), started_at, completed_at`

const executionSelectColumnsAliasLight = `e.id, e.task_id, COALESCE(e.agent_config_id, ''), e.status, e.prompt_sent, e.output, '' AS reasoning_content, e.error_message,
		e.tokens_used, e.duration_ms, e.is_followup, e.starts_new_context, '' AS diff_output, e.cli_session_id, COALESCE(e.dispatch_id, ''), e.started_at, e.completed_at`

const taskExecutionHistoryPageSQL = `SELECT ` + executionSelectColumnsLight + ` FROM executions WHERE task_id = ? ORDER BY started_at DESC, rowid DESC LIMIT ?`

func scanExecutionRow(scanner interface {
	Scan(dest ...interface{}) error
}) (models.Execution, error) {
	var e models.Execution
	err := scanner.Scan(&e.ID, &e.TaskID, &e.AgentConfigID, &e.Status, &e.PromptSent,
		&e.Output, &e.ReasoningContent, &e.ErrorMessage, &e.TokensUsed, &e.DurationMs, &e.IsFollowup,
		&e.StartsNewContext, &e.DiffOutput, &e.CliSessionID, &e.DispatchID, &e.StartedAt, &e.CompletedAt)
	return e, err
}

func (r *ExecutionRepo) ListByTask(ctx context.Context, taskID string) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+executionSelectColumnsLight+` FROM executions WHERE task_id = ? ORDER BY started_at DESC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing executions: %w", err)
	}
	defer rows.Close()

	return scanExecutionsNewestFirst(rows)
}

// ListByTaskHistoryPage returns the latest executions for a task in display order
// for the execution-history panel. Callers request limit+1 rows to discover
// whether an older page exists without loading the whole task history.
func (r *ExecutionRepo) ListByTaskHistoryPage(ctx context.Context, taskID string, limit int) ([]models.Execution, error) {
	if limit <= 0 {
		return []models.Execution{}, nil
	}
	rows, err := r.db.QueryContext(ctx, taskExecutionHistoryPageSQL, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing execution history page: %w", err)
	}
	defer rows.Close()

	return scanExecutionsNewestFirst(rows)
}

func (r *ExecutionRepo) ListByTaskIDs(ctx context.Context, taskIDs []string) (map[string][]models.Execution, error) {
	if len(taskIDs) == 0 {
		return map[string][]models.Execution{}, nil
	}
	placeholders := make([]string, len(taskIDs))
	args := make([]interface{}, len(taskIDs))
	for i, id := range taskIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+executionSelectColumnsLight+` FROM executions WHERE task_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY started_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch listing executions: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]models.Execution, len(taskIDs))
	for rows.Next() {
		e, err := scanExecutionRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		result[e.TaskID] = append(result[e.TaskID], e)
	}
	return result, rows.Err()
}

func (r *ExecutionRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+executionSelectColumnsAliasLight+`
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE t.project_id = ?
		 ORDER BY e.started_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing project executions: %w", err)
	}
	defer rows.Close()

	var execs []models.Execution
	for rows.Next() {
		e, err := scanExecutionRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

// ListByProjectExcludingChat returns recent executions for a project but
// excludes executions whose owning task is the internal Chat category. This
// is used by memory consolidation so Chat page prompts and mode-control text
// never contribute to durable memory; task and task-thread follow-up
// executions are still included.
func (r *ExecutionRepo) ListByProjectExcludingChat(ctx context.Context, projectID string, limit int) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+executionSelectColumnsAliasLight+`
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE t.project_id = ? AND t.category != 'chat'
		 ORDER BY e.started_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing project executions excluding chat: %w", err)
	}
	defer rows.Close()

	var execs []models.Execution
	for rows.Next() {
		e, err := scanExecutionRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

func (r *ExecutionRepo) GetByID(ctx context.Context, id string) (*models.Execution, error) {
	e, err := scanExecutionRow(r.db.QueryRowContext(ctx,
		`SELECT `+executionSelectColumns+` FROM executions WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting execution: %w", err)
	}
	return &e, nil
}

func (r *ExecutionRepo) GetLatestCompletedByTask(ctx context.Context, taskID string) (*models.Execution, error) {
	e, err := scanExecutionRow(r.db.QueryRowContext(ctx,
		`SELECT `+executionSelectColumns+` FROM executions WHERE task_id = ? AND status = ? ORDER BY completed_at DESC, started_at DESC LIMIT 1`,
		taskID, models.ExecCompleted))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest completed execution: %w", err)
	}
	return &e, nil
}

func (r *ExecutionRepo) Create(ctx context.Context, e *models.Execution) error {
	return r.CreateWithExecutor(ctx, r.db, e)
}

// CreateWithExecutor persists an execution using the caller's transaction.
func (r *ExecutionRepo) CreateWithExecutor(ctx context.Context, exec SQLExecutor, e *models.Execution) error {
	isFollowup := 0
	if e.IsFollowup {
		isFollowup = 1
	}
	err := exec.QueryRowContext(ctx,
		`INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent, is_followup, starts_new_context)
			 VALUES (COALESCE(NULLIF(?, ''), lower(hex(randomblob(16)))), ?, NULLIF(?, ''), ?, ?, ?, ?)
			 RETURNING id, started_at`,
		e.ID, e.TaskID, e.AgentConfigID, e.Status, e.PromptSent, isFollowup, e.StartsNewContext).Scan(&e.ID, &e.StartedAt)
	if err != nil {
		return fmt.Errorf("creating execution: %w", err)
	}
	return nil
}

// CreateDirectTaskFollowupOrQueue atomically either starts a direct follow-up or
// appends it to the task's durable FIFO queue when another run owns admission.
// The task status is the ordinary-worker claim before its execution row exists;
// Automation reservations provide the equivalent prepared-run ownership signal.
func (r *ExecutionRepo) CreateDirectTaskFollowupOrQueue(ctx context.Context, e *models.Execution, input *models.ThreadInput) (bool, error) {
	if e == nil || input == nil {
		return false, fmt.Errorf("execution and queued input are required")
	}
	threadRepo := NewThreadInputRepo(r.db)
	started := false
	err := threadRepo.WithImmediateTx(ctx, func(dbexec SQLExecutor) error {
		var status models.TaskStatus
		var projectID string
		if err := dbexec.QueryRowContext(ctx, `SELECT status, project_id FROM tasks WHERE id = ?`, e.TaskID).Scan(&status, &projectID); err != nil {
			return fmt.Errorf("loading task for follow-up admission: %w", err)
		}
		var protected int
		if err := dbexec.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM executions WHERE task_id = ? AND status = 'running'
			UNION ALL SELECT 1 FROM automation_task_run_reservations WHERE task_id = ?
			UNION ALL SELECT 1 FROM thread_inputs
			 WHERE scope = 'task_thread' AND task_id = ? AND input_status = 'pending'
		)`, e.TaskID, e.TaskID, e.TaskID).Scan(&protected); err != nil {
			return fmt.Errorf("checking task follow-up admission: %w", err)
		}
		if status == models.StatusRunning || status == models.StatusQueued || protected != 0 {
			input.Scope = models.ThreadInputScopeTask
			input.ProjectID = projectID
			input.TaskID = e.TaskID
			input.InputMode = models.ThreadInputModeQueued
			input.InputStatus = models.ThreadInputPending
			return threadRepo.CreateQueuedWithExecutor(ctx, dbexec, input)
		}
		if _, err := dbexec.ExecContext(ctx, `UPDATE tasks
			SET status = 'queued', category = 'active', updated_at = datetime('now') WHERE id = ?`, e.TaskID); err != nil {
			return fmt.Errorf("reactivating task for direct follow-up: %w", err)
		}
		isFollowup := 0
		if e.IsFollowup {
			isFollowup = 1
		}
		if err := dbexec.QueryRowContext(ctx, `INSERT INTO executions
			(id, task_id, agent_config_id, status, prompt_sent, is_followup, starts_new_context)
			VALUES (lower(hex(randomblob(16))), ?, NULLIF(?, ''), ?, ?, ?, ?)
			RETURNING id, started_at`, e.TaskID, e.AgentConfigID, e.Status, e.PromptSent, isFollowup, e.StartsNewContext).
			Scan(&e.ID, &e.StartedAt); err != nil {
			return fmt.Errorf("creating direct task follow-up execution: %w", err)
		}
		started = true
		return nil
	})
	return started, err
}

var ErrTaskExecutionAdmissionReserved = errors.New("task execution admission is reserved")

func (r *ExecutionRepo) CreateDirectTaskFollowup(ctx context.Context, e *models.Execution) error {
	if e == nil {
		return fmt.Errorf("execution is required")
	}
	input := &models.ThreadInput{AgentConfigID: e.AgentConfigID, Content: e.PromptSent}
	started, err := r.CreateDirectTaskFollowupOrQueue(ctx, e, input)
	if err != nil {
		return err
	}
	if !started {
		return ErrTaskExecutionAdmissionReserved
	}
	return nil
}

func (r *ExecutionRepo) SetAgentConfigIfEmpty(ctx context.Context, id, agentConfigID string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE executions SET agent_config_id = NULLIF(?, '')
		WHERE id = ? AND (agent_config_id IS NULL OR agent_config_id = '')`, agentConfigID, id)
	if err != nil {
		return fmt.Errorf("updating execution agent config: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) UpdateOutput(ctx context.Context, id string, output string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET output = ? WHERE id = ?`, output, id)
	if err != nil {
		return fmt.Errorf("updating execution output: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) UpdateReasoningContent(ctx context.Context, id, reasoningContent string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET reasoning_content = ? WHERE id = ?`, reasoningContent, id)
	if err != nil {
		return fmt.Errorf("updating execution reasoning content: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) ReplaceReasoningReplay(ctx context.Context, id, reasoningContent string, messages []models.ExecutionReplayMessage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting execution replay transaction: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE executions SET reasoning_content = ? WHERE id = ?`, reasoningContent, id); err != nil {
		return fmt.Errorf("updating execution reasoning content: %w", err)
	}
	if err := replaceExecutionReplayMessages(ctx, tx, id, messages); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing execution replay messages: %w", err)
	}
	return nil
}

func replaceExecutionReplayMessages(ctx context.Context, tx *sql.Tx, id string, messages []models.ExecutionReplayMessage) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM execution_replay_messages WHERE execution_id = ?`, id); err != nil {
		return fmt.Errorf("clearing execution replay messages: %w", err)
	}
	for sequence, message := range messages {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO execution_replay_messages (
				execution_id, sequence, user_content, assistant_content, reasoning_content, transcript_json
			) VALUES (?, ?, ?, ?, ?, ?)`,
			id, sequence, message.UserContent, message.AssistantContent, message.ReasoningContent, message.TranscriptJSON); err != nil {
			return fmt.Errorf("inserting execution replay message %d: %w", sequence, err)
		}
	}
	return nil
}

func (r *ExecutionRepo) ReplayMessagesByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error) {
	result := make(map[string][]models.ExecutionReplayMessage)
	placeholders, args := uniqueExecutionIDArgs(ids)
	if len(args) == 0 {
		return result, nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT execution_id, user_content, assistant_content, reasoning_content, transcript_json
		FROM execution_replay_messages
		WHERE execution_id IN (`+strings.Join(placeholders, ",")+`)
		ORDER BY execution_id, sequence`, args...)
	if err != nil {
		return nil, fmt.Errorf("loading execution replay messages: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id string
		var message models.ExecutionReplayMessage
		if err := rows.Scan(&id, &message.UserContent, &message.AssistantContent, &message.ReasoningContent, &message.TranscriptJSON); err != nil {
			return nil, fmt.Errorf("scanning execution replay message: %w", err)
		}
		result[id] = append(result[id], message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating execution replay messages: %w", err)
	}
	return result, nil
}

func (r *ExecutionRepo) ReasoningContentByIDs(ctx context.Context, ids []string) (map[string]string, error) {
	result := make(map[string]string)
	placeholders, args := uniqueExecutionIDArgs(ids)
	if len(args) == 0 {
		return result, nil
	}

	rows, err := r.db.QueryContext(ctx,
		`SELECT id, COALESCE(reasoning_content, '') FROM executions WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("loading execution reasoning content: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, reasoningContent string
		if err := rows.Scan(&id, &reasoningContent); err != nil {
			return nil, fmt.Errorf("scanning execution reasoning content: %w", err)
		}
		if reasoningContent != "" {
			result[id] = reasoningContent
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating execution reasoning content: %w", err)
	}
	return result, nil
}

func uniqueExecutionIDArgs(ids []string) ([]string, []interface{}) {
	placeholders := make([]string, 0, len(ids))
	args := make([]interface{}, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	return placeholders, args
}

func (r *ExecutionRepo) Complete(ctx context.Context, id string, status models.ExecutionStatus, output, errMsg string, tokensUsed int, durationMs int64) error {
	output = llmtranscript.NormalizeMarkers(output)
	// When output is empty, preserve any partial output already written by the
	// streaming writer during LLM execution. Failure completion paths frequently
	// call Complete with empty output while the streamed transcript already exists
	// in the row; preserving it keeps thread continuity after failures/retries.
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET status = ?, output = CASE WHEN ? = '' THEN output ELSE ? END, error_message = ?,
		 tokens_used = ?, duration_ms = ?, completed_at = datetime('now')
		 WHERE id = ?`,
		status, output, output, errMsg, tokensUsed, durationMs, id)
	if err != nil {
		return fmt.Errorf("completing execution: %w", err)
	}
	if err := r.syncAutomationActivitiesForExecution(ctx, id, status, errMsg); err != nil {
		applog.Infof("[execution-repo] automation activity projection deferred execution=%s: %v", id, err)
		return nil
	}
	if err := r.finalizeAutomationExecution(ctx, id, status); err != nil {
		applog.Infof("[execution-repo] automation terminal projection deferred execution=%s: %v", id, err)
	}
	return nil
}

func (r *ExecutionRepo) finalizeAutomationExecution(ctx context.Context, executionID string, status models.ExecutionStatus) error {
	if r.automationRepo == nil {
		return nil
	}
	var projectID string
	if err := r.db.QueryRowContext(ctx, `SELECT t.project_id FROM executions e JOIN tasks t ON t.id = e.task_id WHERE e.id = ?`, executionID).Scan(&projectID); err != nil {
		return fmt.Errorf("loading automation execution project: %w", err)
	}
	if err := r.automationRepo.FinalizeExecutionProjection(ctx, projectID, executionID, status); err != nil {
		return fmt.Errorf("finalizing automation execution projection: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) syncAutomationActivitiesForExecution(ctx context.Context, executionID string, status models.ExecutionStatus, message string) error {
	activityStatus := models.AutomationActivityCompleted
	if status == models.ExecFailed {
		activityStatus = models.AutomationActivityFailed
	} else if status == models.ExecCancelled {
		activityStatus = models.AutomationActivityCancelled
	} else if status == models.ExecRunning {
		activityStatus = models.AutomationActivityRunning
	}
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, `UPDATE automation_activities SET status = ?,
		completed_at = CASE WHEN ? IN ('completed','failed','cancelled') THEN CURRENT_TIMESTAMP ELSE NULL END,
		error_message = ? WHERE activity_type IN ('task_execution','thread_input_execution')
		AND id IN (SELECT activity_id FROM automation_activity_resources
		WHERE resource_type = 'execution' AND resource_id = ?) RETURNING id`, activityStatus, activityStatus, strings.TrimSpace(message), executionID)
	if err != nil {
		return fmt.Errorf("updating automation execution activities: %w", err)
	}
	if err := syncAutomationLiveActivityStateRows(ctx, conn, rows); err != nil {
		return fmt.Errorf("syncing automation execution activity state: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) CancelRunningByTask(ctx context.Context, taskID string) (int64, error) {
	ids, err := r.CancelRunningByTaskReturningIDs(ctx, taskID)
	if err != nil {
		return 0, err
	}
	return int64(len(ids)), nil
}

func (r *ExecutionRepo) CancelRunningByTaskReturningIDs(ctx context.Context, taskID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`UPDATE executions
			 SET status = ?, error_message = 'cancelled', completed_at = datetime('now')
			 WHERE task_id = ? AND status = ?
			 RETURNING id`,
		models.ExecCancelled, taskID, models.ExecRunning)
	if err != nil {
		return nil, fmt.Errorf("cancelling running task executions: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning cancelled execution id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanning cancelled execution ids: %w", err)
	}
	return ids, nil
}

type CompleteSuccessOutcome string

const (
	CompleteSuccessCompleted       CompleteSuccessOutcome = "completed"
	CompleteSuccessPendingSteering CompleteSuccessOutcome = "pending_steering"
	CompleteSuccessAlreadyTerminal CompleteSuccessOutcome = "already_terminal"
)

func (r *ExecutionRepo) CompleteSuccessIfNoPendingSteering(ctx context.Context, id string, output string, tokensUsed int, durationMs int64) (CompleteSuccessOutcome, error) {
	output = llmtranscript.NormalizeMarkers(output)
	// When output is empty, preserve any partial output already written by the
	// streaming writer during LLM execution. Failure completion paths frequently
	// call Complete with empty output while the streamed transcript already exists
	// in the row; preserving it keeps thread continuity after failures/retries.
	res, err := r.db.ExecContext(ctx,
		`UPDATE executions SET status = ?, output = CASE WHEN ? = '' THEN output ELSE ? END, error_message = '',
		 tokens_used = ?, duration_ms = ?, completed_at = datetime('now')
		 WHERE id = ?
		   AND status = 'running'
		   AND NOT EXISTS (
		       SELECT 1 FROM thread_inputs
		       WHERE run_execution_id = executions.id
		         AND turn_id = executions.id
		         AND input_mode = 'steering'
		         AND input_status = 'pending'
		   )`,
		models.ExecCompleted, output, output, tokensUsed, durationMs, id)
	if err != nil {
		return "", fmt.Errorf("completing execution: %w", err)
	}
	changed, _ := res.RowsAffected()
	if changed > 0 {
		if err := r.syncAutomationActivitiesForExecution(ctx, id, models.ExecCompleted, ""); err != nil {
			applog.Infof("[execution-repo] automation steering activity projection deferred execution=%s: %v", id, err)
		} else if err := r.finalizeAutomationExecution(ctx, id, models.ExecCompleted); err != nil {
			applog.Infof("[execution-repo] automation steering terminal projection deferred execution=%s: %v", id, err)
		}
		return CompleteSuccessCompleted, nil
	}

	var status models.ExecutionStatus
	if err := r.db.QueryRowContext(ctx, `SELECT status FROM executions WHERE id = ?`, id).Scan(&status); err != nil {
		if err == sql.ErrNoRows {
			return CompleteSuccessAlreadyTerminal, nil
		}
		return "", fmt.Errorf("checking execution completion state: %w", err)
	}
	if status != models.ExecRunning {
		return CompleteSuccessAlreadyTerminal, nil
	}
	return CompleteSuccessPendingSteering, nil
}

func (r *ExecutionRepo) RecoverPreRestartRunningTaskExecutions(ctx context.Context) (int64, error) {
	threadRepo := NewThreadInputRepo(r.db)
	var recovered int64
	err := threadRepo.WithImmediateTx(ctx, func(exec SQLExecutor) error {
		// No direct or promoted task-thread runner survives a process restart.
		// Automation dispatches retain a durable dispatch identity/reservation and
		// are deliberately left to Automation reconciliation.
		if _, err := exec.ExecContext(ctx, `
			UPDATE thread_inputs
			SET input_mode = 'queued', turn_id = NULL, expected_turn_id = NULL, updated_at = datetime('now')
			WHERE input_status = 'pending'
			  AND input_mode = 'steering'
			  AND run_execution_id IN (
			      SELECT e.id
			      FROM executions e
			      JOIN tasks t ON t.id = e.task_id
			      WHERE e.status = 'running'
			        AND e.dispatch_id IS NULL
			        AND t.category != 'chat'
			        AND t.status = 'queued'
			        AND NOT EXISTS (
			          SELECT 1 FROM automation_task_run_reservations reservation
			          WHERE reservation.task_id = t.id
			        )
			  )`); err != nil {
			return fmt.Errorf("requeueing pre-restart task steering inputs: %w", err)
		}
		if _, err := exec.ExecContext(ctx, `
			UPDATE tasks
			SET status = 'failed', category = 'backlog', updated_at = datetime('now')
			WHERE status = 'queued'
			  AND category != 'chat'
			  AND NOT EXISTS (
			    SELECT 1 FROM automation_task_run_reservations reservation
			    WHERE reservation.task_id = tasks.id
			  )
			  AND EXISTS (
			    SELECT 1 FROM executions interrupted
			    WHERE interrupted.task_id = tasks.id
			      AND interrupted.status = 'running'
			      AND interrupted.dispatch_id IS NULL
			  )`); err != nil {
			return fmt.Errorf("terminalizing pre-restart task-thread tasks: %w", err)
		}
		result, err := exec.ExecContext(ctx, `
			UPDATE executions
			SET status = 'failed',
			    error_message = CASE
			      WHEN COALESCE(error_message, '') = '' THEN 'Recovered interrupted task-thread execution after restart'
			      ELSE error_message
			    END,
			    completed_at = datetime('now')
			WHERE status = 'running'
			  AND dispatch_id IS NULL
			  AND EXISTS (
			    SELECT 1 FROM tasks t
			    WHERE t.id = executions.task_id
			      AND t.category = 'backlog'
			      AND t.status = 'failed'
			      AND NOT EXISTS (
			        SELECT 1 FROM automation_task_run_reservations reservation
			        WHERE reservation.task_id = t.id
			      )
			  )`)
		if err != nil {
			return fmt.Errorf("recovering pre-restart task-thread executions: %w", err)
		}
		recovered, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("counting pre-restart task-thread executions: %w", err)
		}
		return nil
	})
	return recovered, err
}

func (r *ExecutionRepo) RecoverStaleRunningTaskExecutions(ctx context.Context) (int64, error) {
	// Automation dispatch executions are recovered by AutomationReconciler from
	// their durable outbox/reservation identity. Generic recovery must not
	// terminalize them before prepared resubmission can occur.
	staleTaskPredicate := `
			t.category != 'chat'
			AND (t.status NOT IN ('queued', 'running')
			     OR t.category NOT IN ('active', 'scheduled'))`
	if _, err := r.db.ExecContext(ctx, `
		UPDATE thread_inputs
		SET input_mode = 'queued', turn_id = NULL, expected_turn_id = NULL, updated_at = datetime('now')
		WHERE input_status = 'pending'
		  AND input_mode = 'steering'
		  AND run_execution_id IN (
		      SELECT e.id
		      FROM executions e
		      JOIN tasks t ON t.id = e.task_id
		      WHERE e.status = 'running'
		        AND e.dispatch_id IS NULL
		        AND `+staleTaskPredicate+`
		  )`); err != nil {
		return 0, fmt.Errorf("requeueing stale running task steering inputs: %w", err)
	}
	res, err := r.db.ExecContext(ctx, `
		UPDATE executions
		SET status = CASE
				WHEN (SELECT t.status FROM tasks t WHERE t.id = executions.task_id) = 'cancelled' THEN 'cancelled'
				ELSE 'failed'
			END,
			error_message = CASE
				WHEN COALESCE(error_message, '') = '' THEN 'Recovered stale running execution: owning task is terminal or inactive'
				ELSE error_message
			END,
			completed_at = datetime('now')
		WHERE status = 'running'
		  AND dispatch_id IS NULL
		  AND EXISTS (
		      SELECT 1
		      FROM tasks t
		      WHERE t.id = executions.task_id
		        AND `+staleTaskPredicate+`
		  )`)
	if err != nil {
		return 0, fmt.Errorf("recovering stale running task executions: %w", err)
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("recovering stale running task executions rows affected: %w", err)
	}
	return changed, nil
}

func (r *ExecutionRepo) FindActiveTaskExecution(ctx context.Context, taskID, excludeExecID string) (*models.Execution, error) {
	if _, err := r.RecoverStaleRunningTaskExecutions(ctx); err != nil {
		return nil, err
	}
	e, err := scanExecutionRow(r.db.QueryRowContext(ctx,
		`SELECT `+executionSelectColumnsAliasLight+`
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE e.task_id = ? AND e.id != ? AND e.status = 'running'
		   AND t.category = 'active' AND t.status IN ('queued', 'running')
		 ORDER BY e.started_at DESC, e.rowid DESC LIMIT 1`, taskID, excludeExecID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding active task execution: %w", err)
	}
	return &e, nil
}

func (r *ExecutionRepo) HasActiveTaskExecution(ctx context.Context, taskID, excludeExecID string) (bool, error) {
	if _, err := r.RecoverStaleRunningTaskExecutions(ctx); err != nil {
		return false, err
	}
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*)
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE e.task_id = ? AND e.id != ? AND e.status = 'running'
		   AND t.category = 'active' AND t.status IN ('queued', 'running')`, taskID, excludeExecID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking active task execution: %w", err)
	}
	return count > 0, nil
}

func (r *ExecutionRepo) FindLatestActiveChatExecution(ctx context.Context, projectID string) (*models.Execution, error) {
	e, err := scanExecutionRow(r.db.QueryRowContext(ctx, latestActiveChatExecutionSQL(), projectID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding active chat execution: %w", err)
	}
	return &e, nil
}

func (r *ExecutionRepo) FindLatestActiveEmailChatExecution(ctx context.Context, projectID, sessionKey string) (*models.Execution, error) {
	e, err := scanExecutionRow(r.db.QueryRowContext(ctx,
		`SELECT `+executionSelectColumnsAliasLight+`
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 JOIN email_task_context etc ON etc.task_id = t.id
		 WHERE t.project_id = ? AND t.category = 'chat' AND e.status = 'running' AND etc.email_session_key = ?
		 ORDER BY e.started_at DESC, e.rowid DESC LIMIT 1`, projectID, sessionKey))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding active email chat execution: %w", err)
	}
	return &e, nil
}

// ListChatHistory returns the latest chat executions (CategoryChat tasks) for a project,
// ordered chronologically for prompt/UI consumption.
func (r *ExecutionRepo) ListChatHistory(ctx context.Context, projectID string, limit int) ([]models.Execution, error) {
	return r.listChatHistoryPage(ctx, projectID, "", limit)
}

// ListChatHistoryBefore returns the latest chat executions older than beforeExecID,
// ordered chronologically for prepending into a visible transcript window.
func (r *ExecutionRepo) ListChatHistoryBefore(ctx context.Context, projectID, beforeExecID string, limit int) ([]models.Execution, error) {
	return r.listChatHistoryPage(ctx, projectID, beforeExecID, limit)
}

func (r *ExecutionRepo) ListEmailChatHistory(ctx context.Context, projectID, sessionKey string, limit int) ([]models.Execution, error) {
	if limit <= 0 {
		return []models.Execution{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+executionSelectColumnsAliasLight+`
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 JOIN email_task_context etc ON etc.task_id = t.id
		 WHERE t.project_id = ? AND t.category = 'chat' AND etc.email_session_key = ?
		 ORDER BY e.started_at DESC, e.rowid DESC LIMIT ?`, projectID, sessionKey, limit)
	if err != nil {
		return nil, fmt.Errorf("listing email chat history: %w", err)
	}
	defer rows.Close()
	return scanExecutionsNewestFirstAsChronological(rows)
}

func (r *ExecutionRepo) listChatHistoryPage(ctx context.Context, projectID, beforeExecID string, limit int) ([]models.Execution, error) {
	if limit <= 0 {
		return []models.Execution{}, nil
	}
	query, args := chatHistoryPageSQL(projectID, beforeExecID, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing chat history: %w", err)
	}
	defer rows.Close()

	return scanExecutionsNewestFirstAsChronological(rows)
}

func latestActiveChatExecutionSQL() string {
	return `SELECT ` + executionSelectColumnsAliasLight + `
		 FROM executions e
		 WHERE e.task_project_id = ? AND e.task_category = 'chat' AND e.status = 'running'
		 ORDER BY e.started_at DESC, e.history_order DESC LIMIT 1`
}

func chatHistoryPageSQL(projectID, beforeExecID string, limit int) (string, []interface{}) {
	query := `SELECT ` + executionSelectColumnsAliasLight + `
		 FROM executions e
		 WHERE e.task_project_id = ? AND e.task_category = 'chat'`
	args := []interface{}{projectID}
	if beforeExecID != "" {
		query += ` AND (e.started_at < (SELECT started_at FROM executions WHERE id = ?)
			OR (e.started_at = (SELECT started_at FROM executions WHERE id = ?) AND e.history_order < (SELECT history_order FROM executions WHERE id = ?)))`
		args = append(args, beforeExecID, beforeExecID, beforeExecID)
	}
	query += ` ORDER BY e.started_at DESC, e.history_order DESC LIMIT ?`
	args = append(args, limit)
	return query, args
}

// ListByTaskChronological returns all executions for a task ordered chronologically (oldest first).
func (r *ExecutionRepo) ListByTaskChronological(ctx context.Context, taskID string) ([]models.Execution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+executionSelectColumnsLight+` FROM executions WHERE task_id = ? ORDER BY started_at ASC, rowid ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing executions chronological: %w", err)
	}
	defer rows.Close()

	return scanExecutionsChronological(rows)
}

func (r *ExecutionRepo) GetLatestFailedFollowupByTask(ctx context.Context, taskID string) (*models.Execution, error) {
	e, err := scanExecutionRow(r.db.QueryRowContext(ctx,
		`SELECT `+executionSelectColumns+`
		 FROM executions
		 WHERE task_id = ?
		 ORDER BY started_at DESC, rowid DESC
		 LIMIT 1`, taskID))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting latest task execution: %w", err)
	}
	if e.Status != models.ExecFailed || !e.IsFollowup {
		return nil, nil
	}
	return &e, nil
}

// ListByTaskChronologicalLimit returns the latest executions for a task, ordered chronologically.
func (r *ExecutionRepo) ListByTaskChronologicalLimit(ctx context.Context, taskID string, limit int) ([]models.Execution, error) {
	return r.listTaskExecutionPage(ctx, taskID, "", limit)
}

// ListByTaskChronologicalBefore returns the latest executions older than beforeExecID for a task,
// ordered chronologically for prepending into a visible transcript window.
func (r *ExecutionRepo) ListByTaskChronologicalBefore(ctx context.Context, taskID, beforeExecID string, limit int) ([]models.Execution, error) {
	return r.listTaskExecutionPage(ctx, taskID, beforeExecID, limit)
}

func (r *ExecutionRepo) listTaskExecutionPage(ctx context.Context, taskID, beforeExecID string, limit int) ([]models.Execution, error) {
	if limit <= 0 {
		return []models.Execution{}, nil
	}
	query := `SELECT ` + executionSelectColumnsLight + ` FROM executions WHERE task_id = ?`
	args := []interface{}{taskID}
	if beforeExecID != "" {
		query += ` AND (started_at < (SELECT started_at FROM executions WHERE id = ?)
			OR (started_at = (SELECT started_at FROM executions WHERE id = ?) AND rowid < (SELECT rowid FROM executions WHERE id = ?)))`
		args = append(args, beforeExecID, beforeExecID, beforeExecID)
	}
	query += ` ORDER BY started_at DESC, rowid DESC LIMIT ?`
	args = append(args, limit)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing executions chronological page: %w", err)
	}
	defer rows.Close()

	return scanExecutionsNewestFirstAsChronological(rows)
}

func scanExecutionsNewestFirst(rows *sql.Rows) ([]models.Execution, error) {
	var execs []models.Execution
	for rows.Next() {
		e, err := scanExecutionRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning execution: %w", err)
		}
		execs = append(execs, e)
	}
	return execs, rows.Err()
}

func scanExecutionsChronological(rows *sql.Rows) ([]models.Execution, error) {
	return scanExecutionsNewestFirst(rows)
}

func scanExecutionsNewestFirstAsChronological(rows *sql.Rows) ([]models.Execution, error) {
	execs, err := scanExecutionsChronological(rows)
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(execs)-1; i < j; i, j = i+1, j-1 {
		execs[i], execs[j] = execs[j], execs[i]
	}
	return execs, nil
}

func (r *ExecutionRepo) UpdateDiffOutput(ctx context.Context, id string, diffOutput string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET diff_output = ? WHERE id = ?`, diffOutput, id)
	if err != nil {
		return fmt.Errorf("updating execution diff output: %w", err)
	}
	return nil
}

func (r *ExecutionRepo) UpsertSwarmParentResult(ctx context.Context, parentTaskID, mergerExecutionID, output, diffOutput string, durationMs int64) error {
	prompt := "Swarm merger final result from execution " + mergerExecutionID
	res, err := r.db.ExecContext(ctx,
		`UPDATE executions
		 SET output = ?, diff_output = ?, duration_ms = ?, completed_at = datetime('now')
		 WHERE task_id = ? AND prompt_sent = ? AND status = ?`,
		output, diffOutput, durationMs, parentTaskID, prompt, models.ExecCompleted)
	if err != nil {
		return fmt.Errorf("updating swarm parent result execution: %w", err)
	}
	if rows, err := res.RowsAffected(); err == nil && rows > 0 {
		return nil
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent, output, diff_output, duration_ms, completed_at)
		 VALUES (lower(hex(randomblob(16))), ?, NULL, ?, ?, ?, ?, ?, datetime('now'))`,
		parentTaskID, models.ExecCompleted, prompt, output, diffOutput, durationMs)
	if err != nil {
		return fmt.Errorf("creating swarm parent result execution: %w", err)
	}
	return nil
}

// GetLatestNonEmptyDiffOutput returns the diff_output from the most recent execution for
// the given task that has a non-empty diff_output. Returns "" with no error when none exist.
// This avoids loading all execution rows just to find the preserved diff.
func (r *ExecutionRepo) GetLatestNonEmptyDiffOutput(ctx context.Context, taskID string) (string, error) {
	var diffOutput string
	err := r.db.QueryRowContext(ctx,
		`SELECT diff_output FROM executions WHERE task_id = ? AND diff_output != '' ORDER BY started_at DESC, rowid DESC LIMIT 1`,
		taskID).Scan(&diffOutput)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting latest non-empty diff output: %w", err)
	}
	return diffOutput, nil
}

func (r *ExecutionRepo) UpdateCliSessionID(ctx context.Context, id string, sessionID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE executions SET cli_session_id = ? WHERE id = ?`, sessionID, id)
	if err != nil {
		return fmt.Errorf("updating execution cli_session_id: %w", err)
	}
	return nil
}

// Analytics queries

// SuccessFailureRate represents success/failure rates for a time period
type SuccessFailureRate struct {
	Period       string
	TotalCount   int
	SuccessCount int
	FailureCount int
	SuccessRate  float64
}

// GetSuccessFailureRates returns success/failure rates grouped by time period
// groupBy: "day", "week", or "month"
// dateFrom/dateTo: optional date range filters (RFC3339 format)
func (r *ExecutionRepo) GetSuccessFailureRates(ctx context.Context, projectID string, groupBy string, dateFrom, dateTo string) ([]SuccessFailureRate, error) {
	var dateFormat string
	switch groupBy {
	case "day":
		dateFormat = "%Y-%m-%d"
	case "week":
		dateFormat = "%Y-W%W"
	case "month":
		dateFormat = "%Y-%m"
	default:
		dateFormat = "%Y-%m-%d"
	}

	query := `
		SELECT
			strftime(?, e.started_at, 'localtime') as period,
			COUNT(*) as total_count,
			SUM(CASE WHEN e.status = 'completed' THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN e.status = 'failed' THEN 1 ELSE 0 END) as failure_count
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ? AND e.status IN ('completed', 'failed')
	`
	args := []interface{}{dateFormat, projectID}

	if dateFrom != "" {
		query += ` AND e.started_at >= ?`
		args = append(args, dateFrom)
	}
	if dateTo != "" {
		query += ` AND e.started_at <= ?`
		args = append(args, dateTo)
	}

	query += ` GROUP BY period ORDER BY period ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting success/failure rates: %w", err)
	}
	defer rows.Close()

	rates := []SuccessFailureRate{}
	for rows.Next() {
		var rate SuccessFailureRate
		if err := rows.Scan(&rate.Period, &rate.TotalCount, &rate.SuccessCount, &rate.FailureCount); err != nil {
			return nil, fmt.Errorf("scanning success/failure rate: %w", err)
		}
		if rate.TotalCount > 0 {
			rate.SuccessRate = float64(rate.SuccessCount) / float64(rate.TotalCount) * 100
		}
		rates = append(rates, rate)
	}
	return rates, rows.Err()
}

// AvgExecutionTime represents average execution time
type AvgExecutionTime struct {
	ID    string
	Name  string
	AvgMs float64
	Count int
	MinMs int64
	MaxMs int64
}

// GetAvgExecutionTimeByTask returns average execution times per task
func (r *ExecutionRepo) GetAvgExecutionTimeByTask(ctx context.Context, projectID string, limit int) ([]AvgExecutionTime, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			t.id,
			t.title,
			AVG(e.duration_ms) as avg_ms,
			COUNT(*) as count,
			MIN(e.duration_ms) as min_ms,
			MAX(e.duration_ms) as max_ms
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ? AND e.status = 'completed' AND e.duration_ms > 0
		GROUP BY t.id, t.title
		ORDER BY avg_ms DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting avg execution time by task: %w", err)
	}
	defer rows.Close()

	times := []AvgExecutionTime{}
	for rows.Next() {
		var t AvgExecutionTime
		if err := rows.Scan(&t.ID, &t.Name, &t.AvgMs, &t.Count, &t.MinMs, &t.MaxMs); err != nil {
			return nil, fmt.Errorf("scanning avg execution time: %w", err)
		}
		times = append(times, t)
	}
	return times, rows.Err()
}

// GetAvgExecutionTimeByAgent returns average execution times per agent
func (r *ExecutionRepo) GetAvgExecutionTimeByAgent(ctx context.Context, projectID string) ([]AvgExecutionTime, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			ac.id,
			ac.name,
			AVG(e.duration_ms) as avg_ms,
			COUNT(*) as count,
			MIN(e.duration_ms) as min_ms,
			MAX(e.duration_ms) as max_ms
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		JOIN agent_configs ac ON ac.id = e.agent_config_id
		WHERE t.project_id = ? AND e.status = 'completed' AND e.duration_ms > 0
		GROUP BY ac.id, ac.name
		ORDER BY count DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("getting avg execution time by agent: %w", err)
	}
	defer rows.Close()

	times := []AvgExecutionTime{}
	for rows.Next() {
		var t AvgExecutionTime
		if err := rows.Scan(&t.ID, &t.Name, &t.AvgMs, &t.Count, &t.MinMs, &t.MaxMs); err != nil {
			return nil, fmt.Errorf("scanning avg execution time: %w", err)
		}
		times = append(times, t)
	}
	return times, rows.Err()
}

// ExecutionTrend represents execution frequency data
type ExecutionTrend struct {
	Hour  int
	Count int
}

// GetExecutionTrendsByHour returns execution counts by hour of day
func (r *ExecutionRepo) GetExecutionTrendsByHour(ctx context.Context, projectID string, dateFrom, dateTo string) ([]ExecutionTrend, error) {
	query := `
		SELECT
			CAST(strftime('%H', e.started_at, 'localtime') as INTEGER) as hour,
			COUNT(*) as count
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ?
	`
	args := []interface{}{projectID}

	if dateFrom != "" {
		query += ` AND e.started_at >= ?`
		args = append(args, dateFrom)
	}
	if dateTo != "" {
		query += ` AND e.started_at <= ?`
		args = append(args, dateTo)
	}

	query += ` GROUP BY hour ORDER BY hour ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting execution trends by hour: %w", err)
	}
	defer rows.Close()

	trends := []ExecutionTrend{}
	for rows.Next() {
		var trend ExecutionTrend
		if err := rows.Scan(&trend.Hour, &trend.Count); err != nil {
			return nil, fmt.Errorf("scanning execution trend: %w", err)
		}
		trends = append(trends, trend)
	}
	return trends, rows.Err()
}

// AgentUsage represents agent usage statistics
type AgentUsage struct {
	AgentID        string
	AgentName      string
	ProjectID      string
	ProjectName    string
	ExecutionCount int
	SuccessCount   int
	FailureCount   int
}

// GetAgentUsageByProject returns agent usage breakdown by project
func (r *ExecutionRepo) GetAgentUsageByProject(ctx context.Context, projectID string) ([]AgentUsage, error) {
	query := `
		SELECT
			ac.id as agent_id,
			ac.name as agent_name,
			p.id as project_id,
			p.name as project_name,
			COUNT(*) as execution_count,
			SUM(CASE WHEN e.status = 'completed' THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN e.status = 'failed' THEN 1 ELSE 0 END) as failure_count
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		JOIN projects p ON p.id = t.project_id
		JOIN agent_configs ac ON ac.id = e.agent_config_id
		WHERE 1=1
	`
	args := []interface{}{}

	if projectID != "" {
		query += ` AND t.project_id = ?`
		args = append(args, projectID)
	}

	query += ` GROUP BY ac.id, ac.name, p.id, p.name ORDER BY execution_count DESC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("getting agent usage by project: %w", err)
	}
	defer rows.Close()

	usage := []AgentUsage{}
	for rows.Next() {
		var u AgentUsage
		if err := rows.Scan(&u.AgentID, &u.AgentName, &u.ProjectID, &u.ProjectName, &u.ExecutionCount, &u.SuccessCount, &u.FailureCount); err != nil {
			return nil, fmt.Errorf("scanning agent usage: %w", err)
		}
		usage = append(usage, u)
	}
	return usage, rows.Err()
}

// TaskFrequency represents task execution frequency
type TaskFrequency struct {
	TaskID         string
	TaskTitle      string
	ExecutionCount int
	LastExecutedAt string
}

// GetMostFrequentTasks returns the most frequently executed tasks
func (r *ExecutionRepo) GetMostFrequentTasks(ctx context.Context, projectID string, limit int) ([]TaskFrequency, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			t.id,
			t.title,
			COUNT(*) as execution_count,
			MAX(e.started_at) as last_executed_at
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ?
		GROUP BY t.id, t.title
		ORDER BY execution_count DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting most frequent tasks: %w", err)
	}
	defer rows.Close()

	frequencies := []TaskFrequency{}
	for rows.Next() {
		var freq TaskFrequency
		if err := rows.Scan(&freq.TaskID, &freq.TaskTitle, &freq.ExecutionCount, &freq.LastExecutedAt); err != nil {
			return nil, fmt.Errorf("scanning task frequency: %w", err)
		}
		frequencies = append(frequencies, freq)
	}
	return frequencies, rows.Err()
}

// FailedTaskPattern represents failed task pattern
type FailedTaskPattern struct {
	TaskID       string
	TaskTitle    string
	FailureCount int
	LastError    string
	LastFailedAt string
}

// GetFailedTaskPatterns returns tasks with failure patterns
func (r *ExecutionRepo) GetFailedTaskPatterns(ctx context.Context, projectID string, limit int) ([]FailedTaskPattern, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT
			t.id,
			t.title,
			COUNT(*) as failure_count,
			e.error_message as last_error,
			strftime('%Y-%m-%dT%H:%M:%SZ', MAX(e.started_at)) as last_failed_at
		FROM executions e
		JOIN tasks t ON t.id = e.task_id
		WHERE t.project_id = ? AND e.status = 'failed'
		GROUP BY t.id, t.title, e.error_message
		ORDER BY failure_count DESC, last_failed_at DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("getting failed task patterns: %w", err)
	}
	defer rows.Close()

	patterns := []FailedTaskPattern{}
	for rows.Next() {
		var pattern FailedTaskPattern
		if err := rows.Scan(&pattern.TaskID, &pattern.TaskTitle, &pattern.FailureCount, &pattern.LastError, &pattern.LastFailedAt); err != nil {
			return nil, fmt.Errorf("scanning failed task pattern: %w", err)
		}
		patterns = append(patterns, pattern)
	}
	return patterns, rows.Err()
}
