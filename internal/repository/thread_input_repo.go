package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type ThreadInputRepo struct {
	db                     *sql.DB
	slackTaskContextRepo   *SlackTaskContextRepo
	emailTaskContextRepo   *EmailTaskContextRepo
	discordTaskContextRepo *DiscordTaskContextRepo
}

var (
	ErrNoActiveTurn      = errors.New("no active turn")
	ErrExpectedTurnEmpty = errors.New("expected turn id is required")
	ErrActiveTurnChanged = errors.New("active turn changed")
	ErrInputNotPending   = errors.New("input is no longer pending")
)

func NewThreadInputRepo(db *sql.DB) *ThreadInputRepo {
	return &ThreadInputRepo{
		db:                     db,
		slackTaskContextRepo:   NewSlackTaskContextRepo(db),
		emailTaskContextRepo:   NewEmailTaskContextRepo(db),
		discordTaskContextRepo: NewDiscordTaskContextRepo(db),
	}
}

func defaultThreadTaskJSON(raw string) string {
	if raw == "" {
		return "{}"
	}
	return raw
}

const threadInputSelectColumns = `id, scope, project_id, COALESCE(task_id, ''), COALESCE(run_execution_id, ''), COALESCE(agent_config_id, ''), input_mode, input_status, COALESCE(turn_id, ''), COALESCE(expected_turn_id, ''), content, COALESCE(attachment_session_id, ''), queue_position, COALESCE(chat_mode, ''), COALESCE(source, ''), COALESCE(origin_agent, ''), COALESCE(telegram_chat_id, 0), COALESCE(slack_team_id, ''), COALESCE(slack_channel_id, ''), COALESCE(slack_thread_ts, ''), COALESCE(slack_user_id, ''), COALESCE(email_from, ''), COALESCE(email_message_id, ''), COALESCE(email_references, ''), COALESCE(email_subject, ''), COALESCE(email_session_key, ''), COALESCE(discord_channel_id, ''), COALESCE(discord_thread_id, ''), COALESCE(discord_message_id, ''), COALESCE(discord_user_id, ''), created_at, updated_at, applied_at`

func scanThreadInput(scanner interface {
	Scan(dest ...interface{}) error
}) (models.ThreadInput, error) {
	var input models.ThreadInput
	err := scanner.Scan(
		&input.ID,
		&input.Scope,
		&input.ProjectID,
		&input.TaskID,
		&input.RunExecutionID,
		&input.AgentConfigID,
		&input.InputMode,
		&input.InputStatus,
		&input.TurnID,
		&input.ExpectedTurnID,
		&input.Content,
		&input.AttachmentSessionID,
		&input.QueuePosition,
		&input.ChatMode,
		&input.Source,
		&input.OriginAgent,
		&input.TelegramChatID,
		&input.SlackTeamID,
		&input.SlackChannelID,
		&input.SlackThreadTS,
		&input.SlackUserID,
		&input.EmailFrom,
		&input.EmailMessageID,
		&input.EmailReferences,
		&input.EmailSubject,
		&input.EmailSessionKey,
		&input.DiscordChannelID,
		&input.DiscordThreadID,
		&input.DiscordMessageID,
		&input.DiscordUserID,
		&input.CreatedAt,
		&input.UpdatedAt,
		&input.AppliedAt,
	)
	return input, err
}

func (r *ThreadInputRepo) CreateQueued(ctx context.Context, input *models.ThreadInput) error {
	if input.InputMode == "" {
		input.InputMode = models.ThreadInputModeQueued
	}
	if input.InputStatus == "" {
		input.InputStatus = models.ThreadInputPending
	}
	return r.WithImmediateTx(ctx, func(exec SQLExecutor) error {
		return r.CreateQueuedWithExecutor(ctx, exec, input)
	})
}

// CreateQueuedWithAutomationContext persists a normal queued input and its
// server-derived causal bindings in one transaction. The existing thread_inputs
// queue remains authoritative for admission, cancellation, and promotion.
func (r *ThreadInputRepo) CreateQueuedWithAutomationContext(ctx context.Context, input *models.ThreadInput, automationContext models.AutomationContext, bindingKey string) error {
	if automationContext.ProjectID == "" || automationContext.ProjectID != input.ProjectID || len(automationContext.Bindings) == 0 {
		return errors.New("queued automation context does not match input project")
	}
	if bindingKey == "" {
		return errors.New("automation binding key is required")
	}
	return r.WithImmediateTx(ctx, func(exec SQLExecutor) error {
		if err := r.CreateQueuedWithExecutor(ctx, exec, input); err != nil {
			return err
		}
		return bindAutomationThreadInputWithExecutor(ctx, exec, input.ID, automationContext, bindingKey)
	})
}

func bindAutomationThreadInputWithExecutor(ctx context.Context, exec SQLExecutor, inputID string, automationContext models.AutomationContext, bindingKey string) error {
	var inputProjectID string
	if err := exec.QueryRowContext(ctx, `SELECT project_id FROM thread_inputs WHERE id = ?`, inputID).Scan(&inputProjectID); err != nil {
		return fmt.Errorf("loading automation thread input: %w", err)
	}
	if inputProjectID != automationContext.ProjectID {
		return errors.New("automation thread input project mismatch")
	}
	for i, binding := range automationContext.Bindings {
		key := fmt.Sprintf("%s:%d", bindingKey, i)
		if _, err := exec.ExecContext(ctx, `INSERT INTO automation_thread_input_bindings
			(thread_input_id, project_id, automation_id, version_id, node_id, invocation_id, work_item_id, binding_key)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?)
			ON CONFLICT(thread_input_id, binding_key) DO NOTHING`, inputID, automationContext.ProjectID,
			binding.AutomationID, binding.VersionID, binding.NodeID, binding.InvocationID, binding.WorkItemID, key); err != nil {
			return fmt.Errorf("binding automation thread input: %w", err)
		}
	}
	return nil
}

func (r *ThreadInputRepo) CreateQueuedWithExecutor(ctx context.Context, exec SQLExecutor, input *models.ThreadInput) error {
	if input.InputMode == "" {
		input.InputMode = models.ThreadInputModeQueued
	}
	if input.InputStatus == "" {
		input.InputStatus = models.ThreadInputPending
	}
	return r.createWithExecutor(ctx, exec, input)
}

func (r *ThreadInputRepo) CreateSteeringForActiveExecution(ctx context.Context, input *models.ThreadInput, activeExecutionID string) error {
	if input.ExpectedTurnID == "" {
		return ErrExpectedTurnEmpty
	}
	if input.ExpectedTurnID != activeExecutionID {
		return ErrActiveTurnChanged
	}
	return r.withTx(ctx, func(tx *sql.Tx) error {
		input.RunExecutionID = activeExecutionID
		input.TurnID = activeExecutionID
		input.InputMode = models.ThreadInputModeSteering
		input.InputStatus = models.ThreadInputPending
		if input.QueuePosition == 0 {
			var next sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_position), 0) + 1 FROM thread_inputs WHERE scope = ? AND project_id = ? AND COALESCE(task_id, '') = ?`, input.Scope, input.ProjectID, input.TaskID).Scan(&next); err != nil {
				return fmt.Errorf("allocating input queue position: %w", err)
			}
			if next.Valid {
				input.QueuePosition = next.Int64
			}
		}
		row := tx.QueryRowContext(ctx, `
				INSERT INTO thread_inputs (
					id, scope, project_id, task_id, run_execution_id, agent_config_id, input_mode, input_status,
						turn_id, expected_turn_id, content, attachment_session_id, queue_position, chat_mode,
							source, origin_agent, telegram_chat_id, slack_team_id, slack_channel_id, slack_thread_ts, slack_user_id,
							email_from, email_message_id, email_references, email_subject, email_session_key,
							discord_channel_id, discord_thread_id, discord_message_id, discord_user_id
						)
						SELECT lower(hex(randomblob(16))), ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
					WHERE EXISTS (
					SELECT 1 FROM executions e JOIN tasks t ON t.id = e.task_id
					WHERE e.id = ? AND e.status = 'running'
					  AND (
					    (? = ? AND e.task_id = ?)
					    OR (? = ? AND t.project_id = ? AND t.category = 'chat')
					  )
				)
				RETURNING `+threadInputSelectColumns,
			input.Scope,
			input.ProjectID,
			input.TaskID,
			activeExecutionID,
			input.AgentConfigID,
			input.InputMode,
			input.InputStatus,
			activeExecutionID,
			activeExecutionID,
			input.Content,
			input.AttachmentSessionID,
			input.QueuePosition,
			input.ChatMode,
			input.Source,
			input.OriginAgent,
			input.TelegramChatID,
			input.SlackTeamID,
			input.SlackChannelID,
			input.SlackThreadTS,
			input.SlackUserID,
			input.EmailFrom,
			input.EmailMessageID,
			input.EmailReferences,
			input.EmailSubject,
			input.EmailSessionKey,
			input.DiscordChannelID,
			input.DiscordThreadID,
			input.DiscordMessageID,
			input.DiscordUserID,
			activeExecutionID,
			input.Scope,
			models.ThreadInputScopeTask,
			input.TaskID,
			input.Scope,
			models.ThreadInputScopeChat,
			input.ProjectID,
		)
		created, err := scanThreadInput(row)
		if err == sql.ErrNoRows {
			return ErrNoActiveTurn
		}
		if err != nil {
			return fmt.Errorf("creating steering input: %w", err)
		}
		*input = created
		return nil
	})
}

func (r *ThreadInputRepo) createWithExecutor(ctx context.Context, exec sqlExecutor, input *models.ThreadInput) error {
	if input.QueuePosition == 0 {
		var next sql.NullInt64
		if err := exec.QueryRowContext(ctx, `SELECT COALESCE(MAX(queue_position), 0) + 1 FROM thread_inputs WHERE scope = ? AND project_id = ? AND COALESCE(task_id, '') = ?`, input.Scope, input.ProjectID, input.TaskID).Scan(&next); err != nil {
			return fmt.Errorf("allocating input queue position: %w", err)
		}
		if next.Valid {
			input.QueuePosition = next.Int64
		}
	}
	row := exec.QueryRowContext(ctx, `
			INSERT INTO thread_inputs (
				id, scope, project_id, task_id, run_execution_id, agent_config_id, input_mode, input_status,
					turn_id, expected_turn_id, content, attachment_session_id, queue_position, chat_mode,
						source, origin_agent, telegram_chat_id, slack_team_id, slack_channel_id, slack_thread_ts, slack_user_id,
						email_from, email_message_id, email_references, email_subject, email_session_key,
						discord_channel_id, discord_thread_id, discord_message_id, discord_user_id
					) VALUES (lower(hex(randomblob(16))), ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, NULLIF(?, ''), ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING `+threadInputSelectColumns,
		input.Scope,
		input.ProjectID,
		input.TaskID,
		input.RunExecutionID,
		input.AgentConfigID,
		input.InputMode,
		input.InputStatus,
		input.TurnID,
		input.ExpectedTurnID,
		input.Content,
		input.AttachmentSessionID,
		input.QueuePosition,
		input.ChatMode,
		input.Source,
		input.OriginAgent,
		input.TelegramChatID,
		input.SlackTeamID,
		input.SlackChannelID,
		input.SlackThreadTS,
		input.SlackUserID,
		input.EmailFrom,
		input.EmailMessageID,
		input.EmailReferences,
		input.EmailSubject,
		input.EmailSessionKey,
		input.DiscordChannelID,
		input.DiscordThreadID,
		input.DiscordMessageID,
		input.DiscordUserID,
	)
	created, err := scanThreadInput(row)
	if err != nil {
		return fmt.Errorf("creating thread input: %w", err)
	}
	*input = created
	return nil
}

func (r *ThreadInputRepo) GetByID(ctx context.Context, id string) (*models.ThreadInput, error) {
	input, err := scanThreadInput(r.db.QueryRowContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting thread input: %w", err)
	}
	return &input, nil
}

func (r *ThreadInputRepo) BindPreExecutionQueuedTaskInputs(ctx context.Context, taskID, executionID string) error {
	if taskID == "" || executionID == "" {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE thread_inputs
		SET run_execution_id = ?, updated_at = datetime('now')
		WHERE scope = 'task_thread'
		  AND task_id = ?
		  AND input_mode = 'queued'
		  AND input_status = 'pending'
		  AND COALESCE(run_execution_id, '') = ''`, executionID, taskID)
	if err != nil {
		return fmt.Errorf("binding pre-execution queued task inputs: %w", err)
	}
	return nil
}

func (r *ThreadInputRepo) ListPendingForTask(ctx context.Context, taskID string) ([]models.ThreadInput, error) {
	// Exclude prepared/in-flight steering rows (expected_turn_id cleared by PreparePendingTextSteering).
	// These rows have been sent to the provider but not yet committed; the SSE applied event already
	// removed them from the composer UI at prepare time. Including them on refresh would show a stale
	// "Steering pending" row that the user cannot delete (it's protected while in-flight).
	return r.list(ctx, `WHERE task_id = ? AND input_status = 'pending' AND NOT (input_mode = 'steering' AND COALESCE(expected_turn_id, '') = '') ORDER BY queue_position ASC, created_at ASC, rowid ASC`, taskID)
}

func (r *ThreadInputRepo) ListPendingForChat(ctx context.Context, projectID string) ([]models.ThreadInput, error) {
	// Exclude prepared/in-flight steering rows for the same reason as ListPendingForTask.
	return r.list(ctx, `WHERE scope = 'chat' AND project_id = ? AND input_status = 'pending' AND NOT (input_mode = 'steering' AND COALESCE(expected_turn_id, '') = '') ORDER BY queue_position ASC, created_at ASC, rowid ASC`, projectID)
}

func (r *ThreadInputRepo) ListPendingSteering(ctx context.Context, runExecutionID, turnID string) ([]models.ThreadInput, error) {
	return r.list(ctx, `WHERE run_execution_id = ? AND turn_id = ? AND input_mode = 'steering' AND input_status = 'pending' AND COALESCE(expected_turn_id, '') != '' ORDER BY created_at ASC, rowid ASC`, runExecutionID, turnID)
}

func (r *ThreadInputRepo) PreparePendingSteering(ctx context.Context, runExecutionID, turnID string) ([]models.ThreadInput, error) {
	return r.preparePendingSteering(ctx, runExecutionID, turnID, false)
}

func (r *ThreadInputRepo) PreparePendingTextSteering(ctx context.Context, runExecutionID, turnID string) ([]models.ThreadInput, error) {
	return r.preparePendingSteering(ctx, runExecutionID, turnID, true)
}

func (r *ThreadInputRepo) preparePendingSteering(ctx context.Context, runExecutionID, turnID string, textOnly bool) ([]models.ThreadInput, error) {
	var prepared []models.ThreadInput
	err := r.withTx(ctx, func(tx *sql.Tx) error {
		where := `WHERE run_execution_id = ? AND turn_id = ? AND input_mode = 'steering' AND input_status = 'pending' AND COALESCE(expected_turn_id, '') != ''`
		if textOnly {
			where += ` AND COALESCE(attachment_session_id, '') = ''`
		}
		where += ` ORDER BY created_at ASC, rowid ASC`
		inputs, err := r.listWithExecutor(ctx, tx, where, runExecutionID, turnID)
		if err != nil {
			return err
		}
		if len(inputs) == 0 {
			return nil
		}
		for _, input := range inputs {
			res, err := tx.ExecContext(ctx, `
				UPDATE thread_inputs
				SET expected_turn_id = NULL, updated_at = datetime('now')
				WHERE id = ? AND run_execution_id = ? AND turn_id = ? AND input_mode = 'steering' AND input_status = 'pending' AND COALESCE(expected_turn_id, '') != ''`, input.ID, runExecutionID, turnID)
			if err != nil {
				return fmt.Errorf("preparing steering input: %w", err)
			}
			changed, _ := res.RowsAffected()
			if changed > 0 {
				input.ExpectedTurnID = ""
				prepared = append(prepared, input)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return prepared, nil
}

func (r *ThreadInputRepo) FindOldestQueuedForTask(ctx context.Context, taskID string) (*models.ThreadInput, error) {
	return r.findOldestQueued(ctx, models.ThreadInputScopeTask, "", taskID)
}

func (r *ThreadInputRepo) FindOldestQueuedForChat(ctx context.Context, projectID string) (*models.ThreadInput, error) {
	return r.findOldestQueued(ctx, models.ThreadInputScopeChat, projectID, "")
}

func (r *ThreadInputRepo) ListRecoverableQueuedChatProjectIDsAfter(ctx context.Context, afterProjectID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT ti.project_id
		FROM thread_inputs ti
		LEFT JOIN executions guarded ON guarded.id = ti.run_execution_id
		WHERE ti.scope = 'chat'
		  AND ti.input_mode = 'queued'
		  AND ti.input_status = 'pending'
		  AND COALESCE(ti.project_id, '') != ''
		  AND ti.project_id > ?
		  AND (ti.run_execution_id IS NULL OR guarded.status IN ('completed', 'failed', 'cancelled'))
		  AND NOT EXISTS (
		    SELECT 1
		    FROM executions active
		    JOIN tasks active_task ON active_task.id = active.task_id
		    WHERE active_task.project_id = ti.project_id
		      AND active_task.category = 'chat'
		      AND active.status = 'running'
		  )
		GROUP BY ti.project_id
		ORDER BY ti.project_id
		LIMIT ?`, afterProjectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing recoverable queued chat project ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning recoverable queued chat project id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing recoverable queued chat project ids: %w", err)
	}
	return ids, nil
}

func (r *ThreadInputRepo) ListRecoverableQueuedTaskIDs(ctx context.Context, limit int) ([]string, error) {
	return r.ListRecoverableQueuedTaskIDsAfter(ctx, "", limit)
}

// ListRecoverableQueuedTaskIDsAfter returns a stable keyset page of tasks whose
// oldest pending follow-up can be promoted. Task-ID ordering is only for paging;
// promotion still claims each task's oldest queue_position in FIFO order.
func (r *ThreadInputRepo) ListRecoverableQueuedTaskIDsAfter(ctx context.Context, afterTaskID string, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT ti.task_id
		FROM thread_inputs ti
		LEFT JOIN executions guarded ON guarded.id = ti.run_execution_id
		JOIN tasks t ON t.id = ti.task_id
		WHERE ti.scope = 'task_thread'
		  AND ti.input_mode = 'queued'
		  AND ti.input_status = 'pending'
		  AND COALESCE(ti.task_id, '') != ''
		  AND ti.task_id > ?
		  AND (ti.run_execution_id IS NULL OR guarded.status IN ('completed', 'failed', 'cancelled'))
		  AND NOT EXISTS (
		    SELECT 1 FROM executions active
		    WHERE active.task_id = ti.task_id AND active.status = 'running'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM automation_task_run_reservations reservation
		    WHERE reservation.task_id = ti.task_id
		  )
		GROUP BY ti.task_id
		ORDER BY ti.task_id
		LIMIT ?`, afterTaskID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing recoverable queued task ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning recoverable queued task id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing recoverable queued task ids: %w", err)
	}
	return ids, nil
}

func (r *ThreadInputRepo) findOldestQueued(ctx context.Context, scope models.ThreadInputScope, projectID, taskID string) (*models.ThreadInput, error) {
	query := `SELECT ` + threadInputSelectColumns + ` FROM thread_inputs WHERE scope = ? AND input_mode = 'queued' AND input_status = 'pending'`
	args := []interface{}{scope}
	if projectID != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}
	if taskID != "" {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY queue_position ASC, created_at ASC, rowid ASC LIMIT 1`
	input, err := scanThreadInput(r.db.QueryRowContext(ctx, query, args...))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("finding queued thread input: %w", err)
	}
	return &input, nil
}

func retargetRemainingQueuedInputGuards(ctx context.Context, exec sqlExecutor, promoted models.ThreadInput, newExecutionID string) error {
	if promoted.RunExecutionID == "" || newExecutionID == "" {
		return nil
	}
	query := `
		UPDATE thread_inputs
		SET run_execution_id = ?, updated_at = datetime('now')
		WHERE scope = ? AND input_mode = 'queued' AND input_status = 'pending' AND run_execution_id = ?`
	args := []interface{}{newExecutionID, promoted.Scope, promoted.RunExecutionID}
	switch promoted.Scope {
	case models.ThreadInputScopeChat:
		query += ` AND project_id = ? AND COALESCE(task_id, '') = ''`
		args = append(args, promoted.ProjectID)
	case models.ThreadInputScopeTask:
		query += ` AND task_id = ?`
		args = append(args, promoted.TaskID)
	default:
		return nil
	}
	if _, err := exec.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("retargeting queued input guards: %w", err)
	}
	return nil
}

func (r *ThreadInputRepo) list(ctx context.Context, where string, args ...interface{}) ([]models.ThreadInput, error) {
	return r.listWithExecutor(ctx, r.db, where, args...)
}

func (r *ThreadInputRepo) listWithExecutor(ctx context.Context, exec queryExecutor, where string, args ...interface{}) ([]models.ThreadInput, error) {
	rows, err := exec.QueryContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("listing thread inputs: %w", err)
	}
	defer rows.Close()
	var inputs []models.ThreadInput
	for rows.Next() {
		input, err := scanThreadInput(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning thread input: %w", err)
		}
		inputs = append(inputs, input)
	}
	return inputs, rows.Err()
}

func (r *ThreadInputRepo) ConvertQueuedToSteering(ctx context.Context, id, runExecutionID, expectedTurnID string) (*models.ThreadInput, error) {
	if expectedTurnID == "" {
		return nil, ErrExpectedTurnEmpty
	}
	var converted *models.ThreadInput
	err := r.withTx(ctx, func(tx *sql.Tx) error {
		queued, err := scanThreadInput(tx.QueryRowContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs WHERE id = ?`, id))
		if err == sql.ErrNoRows {
			return ErrInputNotPending
		}
		if err != nil {
			return fmt.Errorf("loading queued input guard: %w", err)
		}
		active, err := r.executionIsRunningForInput(ctx, tx, runExecutionID, &queued)
		if err != nil {
			return err
		}
		if !active {
			return ErrNoActiveTurn
		}
		if expectedTurnID != runExecutionID {
			return ErrActiveTurnChanged
		}
		res, err := tx.ExecContext(ctx, `
				UPDATE thread_inputs
				SET input_mode = 'steering', run_execution_id = ?, turn_id = ?, expected_turn_id = ?, updated_at = datetime('now')
				WHERE id = ? AND input_mode = 'queued' AND input_status = 'pending' AND run_execution_id = ?
				  AND EXISTS (SELECT 1 FROM executions WHERE id = ? AND status = 'running')`, runExecutionID, expectedTurnID, expectedTurnID, id, expectedTurnID, runExecutionID)
		if err != nil {
			return fmt.Errorf("converting queued input to steering: %w", err)
		}
		changed, _ := res.RowsAffected()
		if changed == 0 {
			var pendingQueued int
			if checkErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM thread_inputs WHERE id = ? AND input_mode = 'queued' AND input_status = 'pending'`, id).Scan(&pendingQueued); checkErr != nil {
				return fmt.Errorf("checking queued input guard: %w", checkErr)
			}
			if pendingQueued > 0 {
				var stillRunning int
				if runErr := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM executions WHERE id = ? AND status = 'running'`, runExecutionID).Scan(&stillRunning); runErr != nil {
					return fmt.Errorf("checking active turn guard: %w", runErr)
				}
				if stillRunning == 0 {
					return ErrNoActiveTurn
				}
				return ErrActiveTurnChanged
			}
			return ErrInputNotPending
		}
		input, err := scanThreadInput(tx.QueryRowContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs WHERE id = ?`, id))
		if err != nil {
			return fmt.Errorf("loading converted queued input: %w", err)
		}
		converted = &input
		return nil
	})
	if err != nil {
		return nil, err
	}
	return converted, nil
}

func (r *ThreadInputRepo) MarkApplied(ctx context.Context, id, runExecutionID, turnID string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE thread_inputs
		SET input_status = 'applied', run_execution_id = NULLIF(?, ''), turn_id = NULLIF(?, ''), applied_at = datetime('now'), updated_at = datetime('now')
		WHERE id = ? AND input_status = 'pending'`, runExecutionID, turnID, id)
	if err != nil {
		return fmt.Errorf("marking thread input applied: %w", err)
	}
	changed, _ := res.RowsAffected()
	if changed == 0 {
		return ErrInputNotPending
	}
	return nil
}

func (r *ThreadInputRepo) RestorePreparedSteering(ctx context.Context, ids []string, runExecutionID, expectedTurnID string) error {
	if len(ids) == 0 {
		return nil
	}
	return r.withTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `
				UPDATE thread_inputs
				SET expected_turn_id = NULLIF(?, ''), updated_at = datetime('now')
				WHERE id = ? AND input_mode = 'steering' AND input_status = 'pending' AND run_execution_id = ?`, expectedTurnID, id, runExecutionID); err != nil {
				return fmt.Errorf("restoring prepared steering input: %w", err)
			}
		}
		return nil
	})
}

func (r *ThreadInputRepo) RequeuePendingSteering(ctx context.Context, ids []string, runExecutionID string) ([]models.ThreadInput, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var requeued []models.ThreadInput
	err := r.withTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `
						UPDATE thread_inputs
						SET input_mode = 'queued', turn_id = NULL, expected_turn_id = NULL, updated_at = datetime('now')
						WHERE id = ? AND input_mode = 'steering' AND input_status = 'pending' AND run_execution_id = ?`, id, runExecutionID); err != nil {
				return fmt.Errorf("requeueing steering input: %w", err)
			}
			input, err := scanThreadInput(tx.QueryRowContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs WHERE id = ? AND input_mode = 'queued' AND input_status = 'pending' AND run_execution_id = ?`, id, runExecutionID))
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return fmt.Errorf("loading requeued steering input: %w", err)
			}
			requeued = append(requeued, input)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return requeued, nil
}

func (r *ThreadInputRepo) RequeuePendingSteeringForExecution(ctx context.Context, runExecutionID string) ([]models.ThreadInput, error) {
	if runExecutionID == "" {
		return nil, nil
	}
	var requeued []models.ThreadInput
	err := r.withTx(ctx, func(tx *sql.Tx) error {
		inputs, err := r.listWithExecutor(ctx, tx, `WHERE input_mode = 'steering' AND input_status = 'pending' AND run_execution_id = ? ORDER BY queue_position ASC, created_at ASC, rowid ASC`, runExecutionID)
		if err != nil {
			return err
		}
		for _, input := range inputs {
			res, err := tx.ExecContext(ctx, `
				UPDATE thread_inputs
				SET input_mode = 'queued', turn_id = NULL, expected_turn_id = NULL, updated_at = datetime('now')
				WHERE id = ? AND input_mode = 'steering' AND input_status = 'pending' AND run_execution_id = ?`, input.ID, runExecutionID)
			if err != nil {
				return fmt.Errorf("requeueing steering input for execution: %w", err)
			}
			changed, _ := res.RowsAffected()
			if changed > 0 {
				input.InputMode = models.ThreadInputModeQueued
				input.TurnID = ""
				input.ExpectedTurnID = ""
				requeued = append(requeued, input)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("requeueing steering inputs for execution: %w", err)
	}
	return requeued, nil
}

func (r *ThreadInputRepo) ClaimQueuedForTaskExecution(ctx context.Context, inputID string, exec *models.Execution) error {
	if exec == nil {
		return fmt.Errorf("execution is required")
	}
	return r.WithImmediateTx(ctx, func(dbexec SQLExecutor) error {
		promoted, err := scanThreadInput(dbexec.QueryRowContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs WHERE id = ?`, inputID))
		if err == sql.ErrNoRows {
			return ErrInputNotPending
		}
		if err != nil {
			return fmt.Errorf("loading queued input before claim: %w", err)
		}
		if promoted.Scope != models.ThreadInputScopeTask || promoted.TaskID != exec.TaskID || promoted.InputMode != models.ThreadInputModeQueued || promoted.InputStatus != models.ThreadInputPending {
			return ErrInputNotPending
		}
		if exec.TaskID == "" {
			return fmt.Errorf("execution task id is required")
		}
		var surfaceOK int
		if err := dbexec.QueryRowContext(ctx, `
			SELECT 1
			FROM tasks
			WHERE id = ? AND project_id = ?`, exec.TaskID, promoted.ProjectID).Scan(&surfaceOK); err != nil {
			if err == sql.ErrNoRows {
				return ErrInputNotPending
			}
			return fmt.Errorf("validating queued input task surface: %w", err)
		}
		var activeCount int
		if err := dbexec.QueryRowContext(ctx, `SELECT COUNT(*) FROM executions WHERE task_id = ? AND status = 'running'`, exec.TaskID).Scan(&activeCount); err != nil {
			return fmt.Errorf("checking active task execution before queued claim: %w", err)
		}
		if activeCount > 0 {
			return ErrActiveTurnChanged
		}
		if err := dbexec.QueryRowContext(ctx, `
			SELECT 1
			FROM tasks
			WHERE id = ? AND status NOT IN ('running', 'queued')
			  AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)`, exec.TaskID).Scan(&surfaceOK); err != nil {
			if err == sql.ErrNoRows {
				return ErrInputNotPending
			}
			return fmt.Errorf("checking task admission before queued claim: %w", err)
		}
		res, err := dbexec.ExecContext(ctx, `
			UPDATE thread_inputs
			SET expected_turn_id = id, updated_at = datetime('now')
			WHERE id = ? AND scope = 'task_thread' AND task_id = ? AND input_mode = 'queued' AND input_status = 'pending'`, inputID, exec.TaskID)
		if err != nil {
			return fmt.Errorf("claiming queued input: %w", err)
		}
		changed, _ := res.RowsAffected()
		if changed == 0 {
			return ErrInputNotPending
		}
		if _, err := dbexec.ExecContext(ctx, `
				UPDATE tasks
				SET status = 'queued', category = 'active', updated_at = datetime('now')
				WHERE id = ?`, exec.TaskID); err != nil {
			return fmt.Errorf("reactivating task for queued input: %w", err)
		}
		isFollowup := 0
		if exec.IsFollowup {
			isFollowup = 1
		}
		if err := dbexec.QueryRowContext(ctx, `
					INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent, is_followup)
					VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?)
					RETURNING id, started_at`, exec.TaskID, exec.AgentConfigID, exec.Status, exec.PromptSent, isFollowup).Scan(&exec.ID, &exec.StartedAt); err != nil {
			return fmt.Errorf("creating promoted task execution: %w", err)
		}
		bindingRows, err := dbexec.QueryContext(ctx, `SELECT automation_id, version_id, node_id,
				COALESCE(invocation_id, ''), COALESCE(work_item_id, ''), binding_key
				FROM automation_thread_input_bindings WHERE thread_input_id = ? AND project_id = ?
				ORDER BY binding_key, id`, inputID, promoted.ProjectID)
		if err != nil {
			return fmt.Errorf("loading automation input bindings: %w", err)
		}
		for bindingRows.Next() {
			var automationID, versionID, nodeID, invocationID, workItemID, bindingKey string
			if err := bindingRows.Scan(&automationID, &versionID, &nodeID, &invocationID, &workItemID, &bindingKey); err != nil {
				_ = bindingRows.Close()
				return err
			}
			activityKey := "thread-input:" + inputID + ":" + bindingKey + ":execute"
			var activityID string
			if err := dbexec.QueryRowContext(ctx, `INSERT INTO automation_activities
					(project_id, automation_id, version_id, node_id, invocation_id, work_item_id, activity_key, activity_type, status)
					VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), ?, 'thread_input_execution', 'running')
					ON CONFLICT(automation_id, version_id, activity_key) DO UPDATE SET status = 'running'
					RETURNING id`, promoted.ProjectID, automationID, versionID, nodeID, invocationID, workItemID, activityKey).Scan(&activityID); err != nil {
				_ = bindingRows.Close()
				return fmt.Errorf("upserting automation input activity: %w", err)
			}
			for _, resource := range []struct{ kind, id string }{{"task", exec.TaskID}, {"execution", exec.ID}} {
				if _, err := dbexec.ExecContext(ctx, `INSERT INTO automation_activity_resources
							(activity_id, resource_type, resource_id, relation) VALUES (?, ?, ?, 'subject')
							ON CONFLICT(activity_id, resource_type, resource_id, relation) DO NOTHING`, activityID, resource.kind, resource.id); err != nil {
					_ = bindingRows.Close()
					return err
				}
			}
			if err := syncAutomationLiveActivityState(ctx, dbexec, activityID); err != nil {
				_ = bindingRows.Close()
				return err
			}
		}
		if err := bindingRows.Close(); err != nil {
			return err
		}
		res, err = dbexec.ExecContext(ctx, `				UPDATE thread_inputs
				SET input_status = 'applied', run_execution_id = ?, turn_id = ?, expected_turn_id = NULL, applied_at = datetime('now'), updated_at = datetime('now')
				WHERE id = ? AND scope = 'task_thread' AND task_id = ? AND input_mode = 'queued' AND input_status = 'pending' AND expected_turn_id = id`, exec.ID, exec.ID, inputID, exec.TaskID)
		if err != nil {
			return fmt.Errorf("applying queued input claim: %w", err)
		}
		changed, _ = res.RowsAffected()
		if changed == 0 {
			return ErrInputNotPending
		}
		return retargetRemainingQueuedInputGuards(ctx, dbexec, promoted, exec.ID)
	})
}
func (r *ThreadInputRepo) ClaimQueuedForChatExecution(ctx context.Context, inputID string, task *models.Task, exec *models.Execution, slackContext *models.SlackTaskContext, emailContext *models.EmailTaskContext, discordContext *models.DiscordTaskContext) error {
	if task == nil || exec == nil {
		return fmt.Errorf("task and execution are required")
	}
	return r.withTx(ctx, func(tx *sql.Tx) error {
		promoted, err := scanThreadInput(tx.QueryRowContext(ctx, `SELECT `+threadInputSelectColumns+` FROM thread_inputs WHERE id = ?`, inputID))
		if err == sql.ErrNoRows {
			return ErrInputNotPending
		}
		if err != nil {
			return fmt.Errorf("loading queued chat input before claim: %w", err)
		}
		var maxOrder sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(display_order) FROM tasks WHERE project_id = ? AND category = ?`, task.ProjectID, task.Category).Scan(&maxOrder); err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("getting max display_order: %w", err)
		}
		displayOrder := 0
		if maxOrder.Valid {
			displayOrder = int(maxOrder.Int64) + 1
		}
		autoMerge := 0
		if task.AutoMerge {
			autoMerge = 1
		}
		if err := tx.QueryRowContext(ctx, `
				INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, swarm_role, swarm_status, swarm_config, swarm_sequence, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id)
					VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)			RETURNING id, created_at, updated_at`, task.ProjectID, task.Title, task.Category, task.Priority, task.Status, task.Prompt, task.AgentID, task.AgentDefinitionID, task.Tag, displayOrder, task.ParentTaskID, task.ChainConfig, task.SwarmRole, task.SwarmStatus, defaultThreadTaskJSON(task.SwarmConfig), task.SwarmSequence, task.WorktreePath, task.WorktreeBranch, autoMerge, task.MergeTargetBranch, task.MergeStatus, task.BaseBranch, task.BaseCommitSHA, task.LineageDepth, task.CreatedVia, task.TelegramChatID).Scan(&task.ID, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return fmt.Errorf("creating queued chat task: %w", err)
		}
		task.DisplayOrder = displayOrder

		if slackContext != nil {
			slackContext.TaskID = task.ID
			if err := r.slackTaskContextRepo.UpsertWithExecutor(ctx, tx, slackContext); err != nil {
				return fmt.Errorf("creating queued slack context: %w", err)
			}
		}
		if emailContext != nil {
			emailContext.TaskID = task.ID
			if err := r.emailTaskContextRepo.UpsertWithExecutor(ctx, tx, emailContext); err != nil {
				return fmt.Errorf("creating queued email context: %w", err)
			}
		}

		if discordContext != nil {
			discordContext.TaskID = task.ID
			if err := r.discordTaskContextRepo.UpsertWithExecutor(ctx, tx, discordContext); err != nil {
				return fmt.Errorf("creating queued discord context: %w", err)
			}
		}

		isFollowup := 0
		if exec.IsFollowup {
			isFollowup = 1
		}
		exec.TaskID = task.ID
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent, is_followup)
			VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?)
			RETURNING id, started_at`, exec.TaskID, exec.AgentConfigID, exec.Status, exec.PromptSent, isFollowup).Scan(&exec.ID, &exec.StartedAt); err != nil {
			return fmt.Errorf("creating queued chat execution: %w", err)
		}

		res, err := tx.ExecContext(ctx, `
				UPDATE thread_inputs
				SET input_status = 'applied', run_execution_id = ?, turn_id = ?, applied_at = datetime('now'), updated_at = datetime('now')
				WHERE id = ? AND scope = 'chat' AND project_id = ? AND input_mode = 'queued' AND input_status = 'pending'`, exec.ID, exec.ID, inputID, task.ProjectID)
		if err != nil {
			return fmt.Errorf("claiming queued chat input: %w", err)
		}
		changed, _ := res.RowsAffected()
		if changed == 0 {
			return ErrInputNotPending
		}
		return retargetRemainingQueuedInputGuards(ctx, tx, promoted, exec.ID)
	})
}

func (r *ThreadInputRepo) CancelPending(ctx context.Context, id string) (*models.ThreadInput, error) {
	cancelled, err := scanThreadInput(r.db.QueryRowContext(ctx, `
		UPDATE thread_inputs
		SET input_status = 'cancelled', updated_at = datetime('now')
		WHERE id = ? AND input_status = 'pending'
			  AND NOT (
			    input_mode = 'steering'
			    AND COALESCE(expected_turn_id, '') = ''
			    AND COALESCE(run_execution_id, '') != ''
			    AND EXISTS (SELECT 1 FROM executions WHERE id = thread_inputs.run_execution_id AND status = 'running')
			  )
		RETURNING `+threadInputSelectColumns, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInputNotPending
	}
	if err != nil {
		return nil, fmt.Errorf("cancelling thread input: %w", err)
	}
	return &cancelled, nil
}

func (r *ThreadInputRepo) CancelPendingForTask(ctx context.Context, taskID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE thread_inputs
		SET input_status = 'cancelled', updated_at = datetime('now')
		WHERE task_id = ? AND input_status = 'pending'
			  AND NOT (
			    input_mode = 'steering'
			    AND COALESCE(expected_turn_id, '') = ''
			    AND COALESCE(run_execution_id, '') != ''
			    AND EXISTS (SELECT 1 FROM executions WHERE id = thread_inputs.run_execution_id AND status = 'running')
			  )`, taskID)
	if err != nil {
		return fmt.Errorf("cancelling task thread inputs: %w", err)
	}
	return nil
}

func (r *ThreadInputRepo) AttachmentSessionReferenced(ctx context.Context, sessionID string) (bool, error) {
	var referenced int
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM thread_inputs
			WHERE attachment_session_id = ?
			  AND attachment_session_id IS NOT NULL
			  AND attachment_session_id <> ''
		)`, sessionID).Scan(&referenced)
	if err != nil {
		return false, fmt.Errorf("checking attachment session ownership: %w", err)
	}
	return referenced != 0, nil
}

func (r *ThreadInputRepo) IsAttachmentSessionRetired(ctx context.Context, sessionID string) (bool, error) {
	var retired int
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM retired_attachment_sessions
			WHERE session_id = ?
		)`, sessionID).Scan(&retired)
	if err != nil {
		return false, fmt.Errorf("checking attachment session retirement: %w", err)
	}
	return retired != 0, nil
}

func (r *ThreadInputRepo) RetireAttachmentSessionIfUnowned(ctx context.Context, sessionID string) (retired bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("beginning attachment session retirement: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var referenced int
	if err = tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM thread_inputs
			WHERE attachment_session_id = ?
			  AND attachment_session_id IS NOT NULL
			  AND attachment_session_id <> ''
		)`, sessionID).Scan(&referenced); err != nil {
		return false, fmt.Errorf("checking attachment session ownership before retirement: %w", err)
	}
	if referenced != 0 {
		if err = tx.Commit(); err != nil {
			return false, fmt.Errorf("committing owned attachment session check: %w", err)
		}
		return false, nil
	}
	if _, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO retired_attachment_sessions(session_id) VALUES (?)`, sessionID); err != nil {
		return false, fmt.Errorf("retiring unowned attachment session: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return false, fmt.Errorf("committing attachment session retirement: %w", err)
	}
	return true, nil
}

func (r *ThreadInputRepo) CancelPendingForChat(ctx context.Context, projectID string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE thread_inputs
		SET input_status = 'cancelled', updated_at = datetime('now')
		WHERE scope = 'chat' AND project_id = ? AND input_status = 'pending'
			  AND NOT (
			    input_mode = 'steering'
			    AND COALESCE(expected_turn_id, '') = ''
			    AND COALESCE(run_execution_id, '') != ''
			    AND EXISTS (SELECT 1 FROM executions WHERE id = thread_inputs.run_execution_id AND status = 'running')
			  )`, projectID)
	if err != nil {
		return fmt.Errorf("cancelling chat inputs: %w", err)
	}
	return nil
}

type SQLExecutor interface {
	ExecContext(context.Context, string, ...interface{}) (sql.Result, error)
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

type sqlExecutor = SQLExecutor

type queryExecutor interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

func (r *ThreadInputRepo) withTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *ThreadInputRepo) WithImmediateTx(ctx context.Context, fn func(SQLExecutor) error) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	restoreBusyTimeout, err := boundSQLiteBusyTimeoutToContext(ctx, conn)
	if err != nil {
		return err
	}
	defer restoreBusyTimeout()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	tx := &manualTx{conn: conn, ctx: ctx}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

const sqliteBusyTimeoutRestoreReserve = 20 * time.Millisecond

func boundSQLiteBusyTimeoutToContext(ctx context.Context, conn *sql.Conn) (func(), error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return func() {}, nil
	}
	var previousMS int
	if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&previousMS); err != nil {
		return nil, err
	}
	remaining := time.Until(deadline) - sqliteBusyTimeoutRestoreReserve
	boundedMS := int(remaining / time.Millisecond)
	if boundedMS < 1 {
		boundedMS = 1
	}
	if previousMS > 0 && previousMS <= boundedMS {
		return func() {}, nil
	}
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, boundedMS)); err != nil {
		return nil, err
	}
	return func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), sqliteBusyTimeoutRestoreReserve)
		defer cancel()
		_, _ = conn.ExecContext(restoreCtx, fmt.Sprintf(`PRAGMA busy_timeout=%d`, previousMS))
	}, nil
}

type manualTx struct {
	conn *sql.Conn
	ctx  context.Context
	done bool
}

func (t *manualTx) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return t.conn.ExecContext(ctx, query, args...)
}

func (t *manualTx) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return t.conn.QueryContext(ctx, query, args...)
}

func (t *manualTx) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return t.conn.QueryRowContext(ctx, query, args...)
}

func (t *manualTx) Commit() error {
	if t.done {
		return nil
	}
	_, err := t.conn.ExecContext(t.ctx, `COMMIT`)
	if err == nil {
		t.done = true
	}
	return err
}

func (t *manualTx) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	rollbackCtx := t.ctx
	cancel := func() {}
	if rollbackCtx.Err() != nil {
		rollbackCtx, cancel = context.WithTimeout(context.WithoutCancel(t.ctx), sqliteBusyTimeoutRestoreReserve)
	}
	defer cancel()
	_, err := t.conn.ExecContext(rollbackCtx, `ROLLBACK`)
	return err
}

func (r *ThreadInputRepo) executionIsRunningForInput(ctx context.Context, exec sqlExecutor, executionID string, input *models.ThreadInput) (bool, error) {
	if executionID == "" || input == nil {
		return false, nil
	}
	query := `SELECT COUNT(*) FROM executions e JOIN tasks t ON t.id = e.task_id WHERE e.id = ? AND e.status = 'running'`
	args := []interface{}{executionID}
	switch input.Scope {
	case models.ThreadInputScopeTask:
		if input.TaskID == "" {
			return false, nil
		}
		query += ` AND e.task_id = ?`
		args = append(args, input.TaskID)
	case models.ThreadInputScopeChat:
		if input.ProjectID == "" {
			return false, nil
		}
		query += ` AND t.project_id = ? AND t.category = 'chat'`
		args = append(args, input.ProjectID)
	default:
		return false, nil
	}
	var count int
	if err := exec.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return false, fmt.Errorf("checking active execution: %w", err)
	}
	return count > 0, nil
}
