package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrAlertNotFound = errors.New("alert not found")

type AlertRepo struct {
	db             *sql.DB
	automationRepo *AutomationRepo
}

func NewAlertRepo(db *sql.DB) *AlertRepo { return &AlertRepo{db: db} }

func (r *AlertRepo) SetAutomationRepo(repo *AutomationRepo) { r.automationRepo = repo }

const alertSelectColumns = `id, project_id, scope, task_id, execution_id, source_task_id, type, severity,
	title, message, body, source, metadata_json, COALESCE(idempotency_key, ''), decision_state, decided_at,
	processing_state, COALESCE(claimant, ''), claimed_at, claim_expires_at, implementation_task_id,
	processing_error, is_read, created_at, updated_at`

const alertSummarySelectColumns = `id, project_id, scope, task_id, execution_id, source_task_id, type, severity,
	title, message, source, COALESCE(idempotency_key, ''), decision_state, decided_at,
	processing_state, COALESCE(claimant, ''), claimed_at, claim_expires_at, implementation_task_id,
	processing_error, is_read, created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanAlert(row rowScanner) (*models.Alert, error) {
	var a models.Alert
	var metadata string
	if err := row.Scan(&a.ID, &a.ProjectID, &a.Scope, &a.TaskID, &a.ExecutionID, &a.SourceTaskID,
		&a.Type, &a.Severity, &a.Title, &a.Message, &a.Body, &a.Source, &metadata, &a.IdempotencyKey,
		&a.DecisionState, &a.DecidedAt, &a.ProcessingState, &a.Claimant, &a.ClaimedAt, &a.ClaimExpiresAt,
		&a.ImplementationTaskID, &a.ProcessingError, &a.IsRead, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.Metadata = map[string]any{}
	if metadata != "" {
		if err := json.Unmarshal([]byte(metadata), &a.Metadata); err != nil {
			return nil, fmt.Errorf("decoding alert metadata: %w", err)
		}
	}
	return &a, nil
}

func scanAlertSummary(row rowScanner) (*models.AlertSummary, error) {
	var a models.AlertSummary
	if err := row.Scan(&a.ID, &a.ProjectID, &a.Scope, &a.TaskID, &a.ExecutionID, &a.SourceTaskID,
		&a.Type, &a.Severity, &a.Title, &a.Message, &a.Source, &a.IdempotencyKey,
		&a.DecisionState, &a.DecidedAt, &a.ProcessingState, &a.Claimant, &a.ClaimedAt, &a.ClaimExpiresAt,
		&a.ImplementationTaskID, &a.ProcessingError, &a.IsRead, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

func normalizeAlert(a *models.Alert) error {
	if strings.TrimSpace(a.ProjectID) == "" {
		return errors.New("alert project_id is required")
	}
	if strings.TrimSpace(a.Title) == "" {
		return errors.New("alert title is required")
	}
	if a.Scope == "" {
		a.Scope = models.AlertScopeProject
	}
	if a.Severity == "" {
		a.Severity = models.SeverityInfo
	}
	if a.Type == "" {
		a.Type = models.AlertCustom
	}
	if a.Body == "" {
		a.Body = a.Message
	}
	if a.Source == "" {
		a.Source = "operational"
	}
	if a.Metadata == nil {
		a.Metadata = map[string]any{}
	}
	if a.DecisionState == "" {
		a.DecisionState = models.AlertDecisionNotRequired
	}
	if a.ProcessingState == "" {
		if a.DecisionState == models.AlertDecisionPending {
			a.ProcessingState = models.AlertProcessingUnclaimed
		} else {
			a.ProcessingState = models.AlertProcessingNotApplicable
		}
	}
	return nil
}

func (r *AlertRepo) Create(ctx context.Context, a *models.Alert) error {
	created, err := r.CreateIdempotent(ctx, a)
	if err == nil {
		*a = *created
	}
	return err
}

func (r *AlertRepo) CreateIdempotent(ctx context.Context, a *models.Alert) (*models.Alert, error) {
	if err := normalizeAlert(a); err != nil {
		return nil, err
	}
	hasAutomationContext := a.AutomationContext != nil && r.automationRepo != nil
	var automationContext models.AutomationContext
	if hasAutomationContext {
		automationContext = *a.AutomationContext
	}
	var created *models.Alert
	err := r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		if hasAutomationContext {
			effectiveContext, err := effectiveAlertCreationAutomationContext(ctx, conn, a, automationContext)
			if err != nil {
				return err
			}
			automationContext = effectiveContext
			if err := applyAlertAutomationProvenance(ctx, conn, a, automationContext); err != nil {
				return err
			}
		}
		metadata, err := json.Marshal(a.Metadata)
		if err != nil {
			return fmt.Errorf("encoding alert metadata: %w", err)
		}
		createdNew := true
		created, err = scanAlert(conn.QueryRowContext(ctx, `INSERT INTO alerts (
			project_id, scope, task_id, execution_id, source_task_id, type, severity, title, message, body,
			source, metadata_json, idempotency_key, decision_state, processing_state)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
			ON CONFLICT(project_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> '' DO NOTHING
			RETURNING `+alertSelectColumns,
			a.ProjectID, a.Scope, a.TaskID, a.ExecutionID, a.SourceTaskID, a.Type, a.Severity, a.Title,
			a.Message, a.Body, a.Source, string(metadata), strings.TrimSpace(a.IdempotencyKey), a.DecisionState, a.ProcessingState))
		if errors.Is(err, sql.ErrNoRows) && strings.TrimSpace(a.IdempotencyKey) != "" {
			createdNew = false
			created, err = scanAlert(conn.QueryRowContext(ctx, `SELECT `+alertSelectColumns+` FROM alerts
				WHERE project_id = ? AND idempotency_key = ?`, a.ProjectID, strings.TrimSpace(a.IdempotencyKey)))
		}
		if err != nil {
			return fmt.Errorf("creating alert: %w", err)
		}
		if hasAutomationContext {
			if !createdNew {
				for _, binding := range automationContext.Bindings {
					var exists bool
					if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM automation_artifact_mailbox_owners
							WHERE project_id = ? AND automation_id = ? AND artifact_type = 'alert' AND artifact_id = ?)`,
						a.ProjectID, binding.AutomationID, created.ID).Scan(&exists); err != nil {
						return err
					}
					if !exists {
						return fmt.Errorf("alert idempotency key is already bound outside this Automation source")
					}
				}
			}
			if err := recordAlertCreatedProjection(ctx, conn, created, automationContext); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if hasAutomationContext && created != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationWorkItemUpdated, created.ProjectID, "alert", created.ID)
		}
	})
	if err != nil {
		return nil, err
	}
	*a = *created
	return created, nil
}

func (r *AlertRepo) GetByIdempotencyKey(ctx context.Context, projectID, key string) (*models.Alert, error) {
	a, err := scanAlert(r.db.QueryRowContext(ctx, `SELECT `+alertSelectColumns+` FROM alerts WHERE project_id = ? AND idempotency_key = ?`, projectID, key))
	return alertResult(a, err)
}

func (r *AlertRepo) GetByIDForProject(ctx context.Context, projectID, id string) (*models.Alert, error) {
	a, err := scanAlert(r.db.QueryRowContext(ctx, `SELECT `+alertSelectColumns+` FROM alerts WHERE project_id = ? AND id = ?`, projectID, id))
	return alertResult(a, err)
}

// GetByIDAdmin is intentionally unscoped and is reserved for explicit administrative/internal diagnostics.
func (r *AlertRepo) GetByIDAdmin(ctx context.Context, id string) (*models.Alert, error) {
	a, err := scanAlert(r.db.QueryRowContext(ctx, `SELECT `+alertSelectColumns+` FROM alerts WHERE id = ?`, id))
	return alertResult(a, err)
}

func alertResult(a *models.Alert, err error) (*models.Alert, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAlertNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting alert: %w", err)
	}
	return a, nil
}

func (r *AlertRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Alert, error) {
	return r.ListFiltered(ctx, projectID, models.AlertListFilter{Limit: limit})
}

func (r *AlertRepo) ListSummariesByProject(ctx context.Context, projectID string, limit int) ([]models.AlertSummary, error) {
	return r.ListFilteredSummaries(ctx, projectID, models.AlertListFilter{Limit: limit})
}

func (r *AlertRepo) NativeInboxBindings(ctx context.Context, automationContext models.AutomationContext) ([]models.AutomationBinding, error) {
	bindings := make([]models.AutomationBinding, 0, len(automationContext.Bindings))
	for _, binding := range automationContext.Bindings {
		var role string
		err := r.db.QueryRowContext(ctx, `SELECT n.role FROM automation_nodes n
			JOIN automations a ON a.project_id = n.project_id AND a.id = n.automation_id
				AND a.published_version_id = n.version_id AND a.lifecycle_state = 'active'
			WHERE n.project_id = ? AND n.automation_id = ? AND n.version_id = ? AND n.id = ?`,
			automationContext.ProjectID, binding.AutomationID, binding.VersionID, binding.NodeID).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if role == "native_inbox" {
			bindings = append(bindings, binding)
		}
	}
	return bindings, nil
}

func (r *AlertRepo) AlertOwnedByAutomation(ctx context.Context, projectID, alertID string, bindings []models.AutomationBinding) (bool, error) {
	for _, binding := range bindings {
		automationID := strings.TrimSpace(binding.AutomationID)
		if automationID == "" {
			continue
		}
		var owned bool
		if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
					SELECT 1
					FROM automation_artifact_mailbox_owners owner
					WHERE owner.project_id = ? AND owner.automation_id = ?
						AND owner.artifact_type = 'alert' AND owner.artifact_id = ?
				)`, projectID, automationID, alertID).Scan(&owned); err != nil {
			return false, err
		}
		if owned {
			return true, nil
		}
	}
	return false, nil
}

func (r *AlertRepo) RebindAlertToAutomationInbox(ctx context.Context, projectID, alertID string, bindings []models.AutomationBinding) error {
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		return rebindAlertAutomationProjection(ctx, conn, projectID, alertID, bindings)
	}, nil)
}

func (r *AlertRepo) ListFiltered(ctx context.Context, projectID string, filter models.AlertListFilter) ([]models.Alert, error) {
	filter = normalizeAlertListFilter(filter)
	query, args := buildAlertListQuery(alertSelectColumns, projectID, filter)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing alerts: %w", err)
	}
	defer rows.Close()
	alerts := []models.Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning alert: %w", err)
		}
		alerts = append(alerts, *a)
	}
	return alerts, rows.Err()
}

func (r *AlertRepo) ListFilteredSummaries(ctx context.Context, projectID string, filter models.AlertListFilter) ([]models.AlertSummary, error) {
	filter = normalizeAlertListFilter(filter)
	query, args := buildAlertListQuery(alertSummarySelectColumns, projectID, filter)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing alert summaries: %w", err)
	}
	defer rows.Close()
	summaries := []models.AlertSummary{}
	for rows.Next() {
		a, err := scanAlertSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning alert summary: %w", err)
		}
		summaries = append(summaries, *a)
	}
	return summaries, rows.Err()
}

func normalizeAlertListFilter(filter models.AlertListFilter) models.AlertListFilter {
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	if filter.Limit > 100 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	return filter
}

func buildAlertListQuery(columns, projectID string, filter models.AlertListFilter) (string, []any) {
	query := `SELECT ` + columns + ` FROM alerts WHERE project_id = ?`
	args := []any{projectID}
	if filter.DecisionState != "" {
		query += ` AND decision_state = ?`
		args = append(args, filter.DecisionState)
	}
	if filter.ProcessingState != "" {
		query += ` AND processing_state = ?`
		args = append(args, filter.ProcessingState)
	}
	if filter.Type != "" {
		query += ` AND type = ?`
		args = append(args, filter.Type)
	}
	if filter.Severity != "" {
		query += ` AND severity = ?`
		args = append(args, filter.Severity)
	}
	if strings.TrimSpace(filter.Source) != "" {
		query += ` AND source = ?`
		args = append(args, strings.TrimSpace(filter.Source))
	}
	if filter.Read != nil {
		query += ` AND is_read = ?`
		args = append(args, *filter.Read)
	}
	if filter.ImplementationTaskLinked != nil {
		query += ` AND implementation_task_was_linked = ?`
		args = append(args, *filter.ImplementationTaskLinked)
	}
	if strings.TrimSpace(filter.Search) != "" {
		query += ` AND INSTR(LOWER(
				COALESCE(title, '') || ' ' || COALESCE(message, '') || ' ' ||
				COALESCE(severity, '') || ' ' || COALESCE(decision_state, '') || ' ' ||
				COALESCE(processing_state, '') || ' ' || COALESCE(source, '') || ' ' ||
				COALESCE(
				CASE strftime('%m', created_at, 'localtime')
					WHEN '01' THEN 'Jan' WHEN '02' THEN 'Feb' WHEN '03' THEN 'Mar'
					WHEN '04' THEN 'Apr' WHEN '05' THEN 'May' WHEN '06' THEN 'Jun'
					WHEN '07' THEN 'Jul' WHEN '08' THEN 'Aug' WHEN '09' THEN 'Sep'
					WHEN '10' THEN 'Oct' WHEN '11' THEN 'Nov' WHEN '12' THEN 'Dec'
				END || ' ' ||
				CAST(CAST(strftime('%d', created_at, 'localtime') AS INTEGER) AS TEXT) || ', ' ||
				strftime('%Y', created_at, 'localtime') || ' ' ||
				CAST(((CAST(strftime('%H', created_at, 'localtime') AS INTEGER) + 11) % 12) + 1 AS TEXT) || ':' ||
				strftime('%M', created_at, 'localtime') || ' ' ||
				CASE WHEN CAST(strftime('%H', created_at, 'localtime') AS INTEGER) < 12 THEN 'AM' ELSE 'PM' END,
				''
			)
		), ?) > 0`
		args = append(args, strings.ToLower(strings.TrimSpace(filter.Search)))
	}
	if len(filter.AutomationInboxBindings) > 0 {
		query += ` AND (`
		for i, binding := range filter.AutomationInboxBindings {
			if i > 0 {
				query += ` OR `
			}
			query += `EXISTS (
							SELECT 1
							FROM automation_artifact_mailbox_owners owner
							WHERE owner.project_id = alerts.project_id AND owner.automation_id = ?
								AND owner.artifact_type = 'alert' AND owner.artifact_id = alerts.id)`
			args = append(args, binding.AutomationID)
		}
		query += `)`
	}
	switch filter.Sort {
	case "oldest":
		query += ` ORDER BY created_at ASC, id ASC`
	case "severity":
		query += ` ORDER BY CASE severity WHEN 'error' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END ASC, created_at DESC, id DESC`
	case "unread_first":
		query += ` ORDER BY is_read ASC, created_at DESC, id DESC`
	default:
		query += ` ORDER BY created_at DESC, id DESC`
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)
	return query, args
}

func (r *AlertRepo) CountUnread(ctx context.Context, projectID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE project_id = ? AND is_read = 0`, projectID).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting unread alerts: %w", err)
	}
	return count, nil
}

func requireAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrAlertNotFound
	}
	return nil
}

func (r *AlertRepo) withImmediateAlertMutation(ctx context.Context, mutate func(*sql.Conn) error, afterCommit func()) error {
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return err
	}
	finished := false
	defer func() {
		if !finished {
			finishImmediate()
		}
	}()
	if err := mutate(conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	finishImmediate()
	finished = true
	if afterCommit != nil {
		afterCommit()
	}
	return nil
}

func (r *AlertRepo) MarkRead(ctx context.Context, projectID, id string) error {
	result, err := execBoundSQLite(ctx, r.db, `UPDATE alerts SET is_read = 1, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("marking alert read: %w", err)
	}
	return requireAffected(result)
}

func (r *AlertRepo) MarkReadBulk(ctx context.Context, projectID string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one alert is required")
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, projectID)
	for _, id := range ids {
		args = append(args, id)
	}
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE project_id = ? AND id IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
			return err
		}
		if count != len(ids) {
			return ErrAlertNotFound
		}
		_, err := conn.ExecContext(ctx, `UPDATE alerts SET is_read = 1, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id IN (`+placeholders+`)`, args...)
		if err != nil {
			return fmt.Errorf("marking alerts read: %w", err)
		}
		return nil
	}, nil)
}

func (r *AlertRepo) MarkAllRead(ctx context.Context, projectID string) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE alerts SET is_read = 1, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND is_read = 0`, projectID)
	if err != nil {
		return fmt.Errorf("marking all alerts read: %w", err)
	}
	return nil
}

func (r *AlertRepo) Delete(ctx context.Context, projectID, id string) error {
	result, err := execBoundSQLite(ctx, r.db, `DELETE FROM alerts WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return fmt.Errorf("deleting alert: %w", err)
	}
	return requireAffected(result)
}

func (r *AlertRepo) DeleteBulk(ctx context.Context, projectID string, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one alert is required")
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	args = append(args, projectID)
	for _, id := range ids {
		args = append(args, id)
	}
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		var count int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE project_id = ? AND id IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
			return err
		}
		if count != len(ids) {
			return ErrAlertNotFound
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM alerts WHERE project_id = ? AND id IN (`+placeholders+`)`, args...); err != nil {
			return fmt.Errorf("deleting alerts: %w", err)
		}
		return nil
	}, nil)
}

func (r *AlertRepo) DeleteAll(ctx context.Context, projectID string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM alerts WHERE project_id = ?`, projectID)
	if err != nil {
		return fmt.Errorf("deleting all alerts: %w", err)
	}
	return nil
}

func (r *AlertRepo) SetDecision(ctx context.Context, projectID, id string, state models.AlertDecisionState) error {
	if state != models.AlertDecisionApproved && state != models.AlertDecisionRejected && state != models.AlertDecisionDismissed {
		return fmt.Errorf("invalid alert decision state %q", state)
	}
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE alerts
			SET decision_state = ?, decided_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND id = ? AND decision_state = 'pending'`, state, projectID, id)
		if err != nil {
			return fmt.Errorf("setting alert decision: %w", err)
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			var current models.AlertDecisionState
			if err := conn.QueryRowContext(ctx, `SELECT decision_state FROM alerts WHERE project_id = ? AND id = ?`, projectID, id).Scan(&current); err != nil {
				return err
			}
			if current != state {
				return fmt.Errorf("alert decision is %s, not pending", current)
			}
		}
		if r.automationRepo != nil {
			if err := recordAlertDecisionProjection(ctx, conn, projectID, id, state); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if r.automationRepo != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationTransitionCreated, projectID, "alert", id)
		}
	})
}

func (r *AlertRepo) ClaimApproved(ctx context.Context, projectID, id, claimant string, lease time.Duration) (*models.Alert, error) {
	claimant = strings.TrimSpace(claimant)
	if claimant == "" {
		return nil, errors.New("claimant is required")
	}
	if lease <= 0 || lease > 24*time.Hour {
		lease = 30 * time.Minute
	}
	now := time.Now().UTC()
	expires := now.Add(lease)
	var a *models.Alert
	err := r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		var err error
		a, err = scanAlert(conn.QueryRowContext(ctx, `UPDATE alerts SET
			processing_state = 'claimed', claimant = ?, claimed_at = ?, claim_expires_at = ?, processing_error = '', updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND id = ? AND decision_state = 'approved' AND implementation_task_id IS NULL
			AND (processing_state IN ('unclaimed', 'failed') OR (processing_state = 'claimed' AND claim_expires_at <= ?))
			RETURNING `+alertSelectColumns, claimant, now, expires, projectID, id, now))
		if errors.Is(err, sql.ErrNoRows) {
			a, err = scanAlert(conn.QueryRowContext(ctx, `SELECT `+alertSelectColumns+` FROM alerts WHERE project_id = ? AND id = ?`, projectID, id))
			if err == nil && (a.ProcessingState != models.AlertProcessingClaimed || a.Claimant != claimant) {
				return fmt.Errorf("alert is not claimable")
			}
		}
		if err != nil {
			return fmt.Errorf("claiming alert: %w", err)
		}
		if r.automationRepo != nil {
			if err := recordAlertClaimProjection(ctx, conn, projectID, id, claimant); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if r.automationRepo != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationWorkItemUpdated, projectID, "alert", id)
		}
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (r *AlertRepo) ReleaseClaim(ctx context.Context, projectID, id, claimant string) error {
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE alerts SET processing_state = 'unclaimed', claimant = NULL,
			claimed_at = NULL, claim_expires_at = NULL, processing_error = '', updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND id = ? AND processing_state = 'claimed' AND claimant = ? AND implementation_task_id IS NULL`,
			projectID, id, claimant)
		if err != nil {
			return fmt.Errorf("releasing alert claim: %w", err)
		}
		if err := requireAffected(result); err != nil {
			return err
		}
		if r.automationRepo != nil {
			if err := recordAlertClaimReleasedProjection(ctx, conn, projectID, id, claimant); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if r.automationRepo != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationWorkItemUpdated, projectID, "alert", id)
		}
	})
}

func (r *AlertRepo) LinkImplementationTask(ctx context.Context, projectID, id, claimant, taskID string) error {
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE alerts SET implementation_task_id = ?, implementation_task_was_linked = 1,
			processing_state = 'implementation_task_linked', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND id = ? AND processing_state = 'claimed' AND claimant = ?
			AND EXISTS (SELECT 1 FROM tasks WHERE id = ? AND project_id = ?)`, taskID, projectID, id, claimant, taskID, projectID)
		if err != nil {
			return fmt.Errorf("linking implementation task: %w", err)
		}
		if changed, _ := result.RowsAffected(); changed == 0 {
			var linked sql.NullString
			if err := conn.QueryRowContext(ctx, `SELECT implementation_task_id FROM alerts WHERE project_id = ? AND id = ?`, projectID, id).Scan(&linked); err != nil {
				return ErrAlertNotFound
			}
			if !linked.Valid || linked.String != taskID {
				return ErrAlertNotFound
			}
		}
		if r.automationRepo != nil {
			if err := recordAlertImplementationProjection(ctx, conn, projectID, id, taskID); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if r.automationRepo != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationResourceLinked, projectID, "alert", id)
		}
	})
}

func (r *AlertRepo) CreateImplementationTask(ctx context.Context, projectID, alertID, claimant string, input models.AlertImplementationTaskInput) (*models.Task, error) {
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Prompt) == "" {
		return nil, errors.New("implementation task title and prompt are required")
	}
	if input.Priority < 1 || input.Priority > 4 {
		input.Priority = 2
	}
	var taskID string
	err := r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		var linked sql.NullString
		var state models.AlertProcessingState
		var storedClaimant string
		if err := conn.QueryRowContext(ctx, `SELECT implementation_task_id, processing_state, COALESCE(claimant, '')
			FROM alerts WHERE project_id = ? AND id = ?`, projectID, alertID).Scan(&linked, &state, &storedClaimant); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrAlertNotFound
			}
			return err
		}
		if linked.Valid {
			taskID = linked.String
			if r.automationRepo != nil {
				if err := recordAlertImplementationProjection(ctx, conn, projectID, alertID, taskID); err != nil {
					return err
				}
			}
			return nil
		}
		if state != models.AlertProcessingClaimed || storedClaimant != claimant {
			return errors.New("alert is not claimed by this caller")
		}
		var displayOrder int
		if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(display_order), -1) + 1 FROM tasks WHERE project_id = ? AND category = 'backlog'`, projectID).Scan(&displayOrder); err != nil {
			return err
		}
		agentID := strings.TrimSpace(input.AgentID)
		var agentIDValue any
		if agentID != "" {
			agentIDValue = agentID
		}
		if err := conn.QueryRowContext(ctx, `INSERT INTO tasks
				(project_id, title, category, priority, status, prompt, tag, agent_id, display_order, created_via)
				VALUES (?, ?, 'backlog', ?, 'pending', ?, ?, ?, ?, ?)
				RETURNING id`, projectID, strings.TrimSpace(input.Title), input.Priority, input.Prompt, input.Tag, agentIDValue, displayOrder, models.TaskOriginSystemAgent).Scan(&taskID); err != nil {
			return fmt.Errorf("creating implementation task: %w", err)
		}
		if goal := strings.TrimSpace(input.Goal); goal != "" {
			if err := createTaskGoalWithExecutor(ctx, conn, taskID, &models.TaskGoal{GoalID: NewID(), Objective: goal, Status: models.TaskGoalStatusActive, Reason: "set at task creation"}); err != nil {
				return err
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE alerts SET implementation_task_id = ?, implementation_task_was_linked = 1,
				processing_state = 'implementation_task_linked', claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, taskID, projectID, alertID); err != nil {
			return err
		}
		if r.automationRepo != nil {
			if err := recordAlertImplementationProjection(ctx, conn, projectID, alertID, taskID); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if r.automationRepo != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationResourceLinked, projectID, "alert", alertID)
		}
	})
	if err != nil {
		return nil, err
	}
	return NewTaskRepo(r.db, nil).GetByID(ctx, taskID)
}

func (r *AlertRepo) MarkProcessing(ctx context.Context, projectID, id, claimant string, state models.AlertProcessingState, message string) error {
	if state != models.AlertProcessingCompleted && state != models.AlertProcessingFailed {
		return fmt.Errorf("invalid terminal processing state %q", state)
	}
	return r.withImmediateAlertMutation(ctx, func(conn *sql.Conn) error {
		result, err := conn.ExecContext(ctx, `UPDATE alerts SET processing_state = ?, processing_error = ?,
			claim_expires_at = NULL, updated_at = CURRENT_TIMESTAMP
			WHERE project_id = ? AND id = ? AND claimant = ? AND processing_state IN ('claimed', 'implementation_task_linked', 'failed')`,
			state, strings.TrimSpace(message), projectID, id, claimant)
		if err != nil {
			return fmt.Errorf("marking alert processing: %w", err)
		}
		if err := requireAffected(result); err != nil {
			return err
		}
		if r.automationRepo != nil {
			if err := recordAlertProcessingProjection(ctx, conn, projectID, id, state, message); err != nil {
				return err
			}
		}
		return nil
	}, func() {
		if r.automationRepo != nil {
			r.automationRepo.PublishResourceInvalidations(context.WithoutCancel(ctx), events.AutomationTransitionCreated, projectID, "alert", id)
		}
	})
}
