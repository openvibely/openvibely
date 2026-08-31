package repository

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

var (
	ErrAutomationCursor         = errors.New("invalid automation history cursor")
	ErrAutomationWorkItemStatus = errors.New("invalid automation work-item status")
)

type automationHistoryCursor struct {
	Kind string    `json:"kind"`
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

func automationPageLimit(limit int) int {
	if limit <= 0 || limit > 50 {
		return 50
	}
	return limit
}

func encodeAutomationCursor(kind string, at time.Time, id string) string {
	value, _ := json.Marshal(automationHistoryCursor{Kind: kind, Time: at.UTC(), ID: id})
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeAutomationCursor(kind, value string) (*automationHistoryCursor, error) {
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, ErrAutomationCursor
	}
	var cursor automationHistoryCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.Kind != kind || cursor.Time.IsZero() || strings.TrimSpace(cursor.ID) == "" {
		return nil, ErrAutomationCursor
	}
	return &cursor, nil
}

func automationCursorSQLTime(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05")
}

func automationCursorKind(collection string, values ...string) string {
	return collection + ":" + strings.Join(values, ":")
}

func (r *AutomationRepo) ListAutomationInvocations(ctx context.Context, projectID, automationID string, limit int, cursorValue string) (models.AutomationInvocationPage, error) {
	limit = automationPageLimit(limit)
	kind := automationCursorKind("invocations", automationID)
	cursor, err := decodeAutomationCursor(kind, cursorValue)
	if err != nil {
		return models.AutomationInvocationPage{}, err
	}
	query := `SELECT id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type,
		trigger_resource_id, occurrence_key, scheduled_for, status, skipped_reason, started_at, completed_at,
		created_at, updated_at, error_message
		FROM automation_invocations WHERE project_id = ? AND automation_id = ?`
	args := []any{projectID, automationID}
	if cursor != nil {
		query += ` AND (datetime(COALESCE(scheduled_for, started_at, created_at)) < datetime(?) OR
			(datetime(COALESCE(scheduled_for, started_at, created_at)) = datetime(?) AND id < ?))`
		args = append(args, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), cursor.ID)
	}
	query += ` ORDER BY COALESCE(scheduled_for, started_at, created_at) DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return models.AutomationInvocationPage{}, fmt.Errorf("listing automation invocations: %w", err)
	}
	defer rows.Close()
	page := models.AutomationInvocationPage{}
	var cursorTimes []time.Time
	for rows.Next() {
		var invocation models.AutomationInvocation
		if err := scanAutomationInvocationHistoryRow(rows, &invocation); err != nil {
			return models.AutomationInvocationPage{}, err
		}
		page.Items = append(page.Items, invocation)
		cursorTimes = append(cursorTimes, automationInvocationTime(invocation))
	}
	if err := rows.Err(); err != nil {
		return models.AutomationInvocationPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = encodeAutomationCursor(kind, cursorTimes[limit-1], page.Items[limit-1].ID)
	}
	return page, nil
}

func automationInvocationTime(invocation models.AutomationInvocation) time.Time {
	if invocation.ScheduledFor != nil {
		return *invocation.ScheduledFor
	}
	if invocation.StartedAt != nil {
		return *invocation.StartedAt
	}
	return invocation.CreatedAt
}

func scanAutomationInvocationHistoryRow(row automationRowScanner, invocation *models.AutomationInvocation) error {
	return row.Scan(&invocation.ID, &invocation.ProjectID, &invocation.AutomationID, &invocation.VersionID,
		&invocation.TriggerNodeID, &invocation.TriggerResourceType, &invocation.TriggerResourceID,
		&invocation.OccurrenceKey, &invocation.ScheduledFor, &invocation.Status, &invocation.SkippedReason,
		&invocation.StartedAt, &invocation.CompletedAt, &invocation.CreatedAt, &invocation.UpdatedAt,
		&invocation.ErrorMessage)
}

func (r *AutomationRepo) GetAutomationInvocation(ctx context.Context, projectID, automationID, invocationID string) (*models.AutomationInvocation, error) {
	var invocation models.AutomationInvocation
	err := scanAutomationInvocationHistoryRow(r.db.QueryRowContext(ctx, `SELECT id, project_id, automation_id, version_id,
		trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, scheduled_for, status,
		skipped_reason, started_at, completed_at, created_at, updated_at, error_message
		FROM automation_invocations WHERE project_id = ? AND automation_id = ? AND id = ?`, projectID, automationID, invocationID), &invocation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &invocation, err
}

func (r *AutomationRepo) ListAutomationInvocationNodeIDs(ctx context.Context, projectID, automationID, invocationID string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT node_id FROM (
		SELECT node_id FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND invocation_id = ?
		UNION
		SELECT from_node_id FROM automation_transitions
		WHERE project_id = ? AND automation_id = ? AND invocation_id = ? AND from_node_id IS NOT NULL
		UNION
		SELECT to_node_id FROM automation_transitions
		WHERE project_id = ? AND automation_id = ? AND invocation_id = ?
	) ORDER BY node_id LIMIT ?`, projectID, automationID, invocationID,
		projectID, automationID, invocationID, projectID, automationID, invocationID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing automation invocation nodes: %w", err)
	}
	defer rows.Close()
	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	return nodeIDs, rows.Err()
}

func validAutomationWorkItemStatus(status string) bool {
	switch models.AutomationWorkItemStatus(status) {
	case models.AutomationWorkItemActive, models.AutomationWorkItemWaiting, models.AutomationWorkItemBlocked,
		models.AutomationWorkItemFailed, models.AutomationWorkItemCompleted, models.AutomationWorkItemCancelled:
		return true
	default:
		return status == ""
	}
}

func (r *AutomationRepo) ListAutomationWorkItems(ctx context.Context, projectID, automationID, status string, limit int, cursorValue string) (models.AutomationWorkItemPage, error) {
	limit = automationPageLimit(limit)
	if !validAutomationWorkItemStatus(status) {
		return models.AutomationWorkItemPage{}, fmt.Errorf("%w %q", ErrAutomationWorkItemStatus, status)
	}
	kind := automationCursorKind("work-items", automationID, status)
	cursor, err := decodeAutomationCursor(kind, cursorValue)
	if err != nil {
		return models.AutomationWorkItemPage{}, err
	}
	query := `SELECT id, project_id, automation_id, origin_version_id, COALESCE(origin_invocation_id, ''),
		COALESCE(parent_work_item_id, ''), work_item_key, kind, title, status, created_at, updated_at, completed_at
		FROM automation_work_items WHERE project_id = ? AND automation_id = ?`
	args := []any{projectID, automationID}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if cursor != nil {
		query += ` AND (datetime(created_at) < datetime(?) OR (datetime(created_at) = datetime(?) AND id < ?))`
		args = append(args, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), cursor.ID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return models.AutomationWorkItemPage{}, fmt.Errorf("listing automation work items: %w", err)
	}
	defer rows.Close()
	page := models.AutomationWorkItemPage{}
	for rows.Next() {
		var item models.AutomationWorkItem
		if err := scanAutomationWorkItemHistoryRow(rows, &item); err != nil {
			return models.AutomationWorkItemPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return models.AutomationWorkItemPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeAutomationCursor(kind, last.CreatedAt, last.ID)
	}
	return page, nil
}

func scanAutomationWorkItemHistoryRow(row automationRowScanner, item *models.AutomationWorkItem) error {
	return row.Scan(&item.ID, &item.ProjectID, &item.AutomationID, &item.OriginVersionID,
		&item.OriginInvocationID, &item.ParentWorkItemID, &item.WorkItemKey, &item.Kind, &item.Title,
		&item.Status, &item.CreatedAt, &item.UpdatedAt, &item.CompletedAt)
}

func (r *AutomationRepo) GetAutomationWorkItem(ctx context.Context, projectID, automationID, workItemID string) (*models.AutomationWorkItem, error) {
	var item models.AutomationWorkItem
	err := scanAutomationWorkItemHistoryRow(r.db.QueryRowContext(ctx, `SELECT id, project_id, automation_id, origin_version_id,
		COALESCE(origin_invocation_id, ''), COALESCE(parent_work_item_id, ''), work_item_key, kind, title,
		status, created_at, updated_at, completed_at FROM automation_work_items
		WHERE project_id = ? AND automation_id = ? AND id = ?`, projectID, automationID, workItemID), &item)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &item, err
}

func (r *AutomationRepo) ListAutomationActivities(ctx context.Context, projectID, automationID, invocationID, workItemID string, limit int, cursorValue string) (models.AutomationActivityPage, error) {
	limit = automationPageLimit(limit)
	kind := automationCursorKind("activities", automationID, invocationID, workItemID)
	cursor, err := decodeAutomationCursor(kind, cursorValue)
	if err != nil {
		return models.AutomationActivityPage{}, err
	}
	query := `SELECT id, project_id, automation_id, version_id, node_id, COALESCE(invocation_id, ''),
		COALESCE(work_item_id, ''), activity_key, activity_type, status, started_at, completed_at, error_message
		FROM automation_activities WHERE project_id = ? AND automation_id = ?`
	args := []any{projectID, automationID}
	if invocationID != "" {
		query += ` AND invocation_id = ?`
		args = append(args, invocationID)
	}
	if workItemID != "" {
		query += ` AND work_item_id = ?`
		args = append(args, workItemID)
	}
	if cursor != nil {
		query += ` AND (datetime(started_at) > datetime(?) OR (datetime(started_at) = datetime(?) AND id > ?))`
		args = append(args, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), cursor.ID)
	}
	query += ` ORDER BY started_at, id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return models.AutomationActivityPage{}, err
	}
	defer rows.Close()
	page := models.AutomationActivityPage{}
	for rows.Next() {
		var activity models.AutomationActivity
		if err := rows.Scan(&activity.ID, &activity.ProjectID, &activity.AutomationID, &activity.VersionID,
			&activity.NodeID, &activity.InvocationID, &activity.WorkItemID, &activity.ActivityKey,
			&activity.ActivityType, &activity.Status, &activity.StartedAt, &activity.CompletedAt,
			&activity.ErrorMessage); err != nil {
			return models.AutomationActivityPage{}, err
		}
		page.Items = append(page.Items, activity)
	}
	if err := rows.Err(); err != nil {
		return models.AutomationActivityPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeAutomationCursor(kind, last.StartedAt, last.ID)
	}
	return page, nil
}

func (r *AutomationRepo) ListAutomationTransitions(ctx context.Context, projectID, automationID, invocationID, workItemID string, limit int, cursorValue string) (models.AutomationTransitionPage, error) {
	limit = automationPageLimit(limit)
	kind := automationCursorKind("transitions", automationID, invocationID, workItemID)
	cursor, err := decodeAutomationCursor(kind, cursorValue)
	if err != nil {
		return models.AutomationTransitionPage{}, err
	}
	query := `SELECT id, project_id, automation_id, version_id, work_item_id, COALESCE(invocation_id, ''),
		COALESCE(activity_id, ''), COALESCE(from_node_id, ''), to_node_id, COALESCE(edge_id, ''),
		event_key, state, metadata_json, occurred_at FROM automation_transitions
		WHERE project_id = ? AND automation_id = ?`
	args := []any{projectID, automationID}
	if invocationID != "" {
		query += ` AND invocation_id = ?`
		args = append(args, invocationID)
	}
	if workItemID != "" {
		query += ` AND work_item_id = ?`
		args = append(args, workItemID)
	}
	if cursor != nil {
		query += ` AND (datetime(occurred_at) > datetime(?) OR (datetime(occurred_at) = datetime(?) AND id > ?))`
		args = append(args, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), cursor.ID)
	}
	query += ` ORDER BY occurred_at, id LIMIT ?`
	args = append(args, limit+1)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return models.AutomationTransitionPage{}, err
	}
	defer rows.Close()
	page := models.AutomationTransitionPage{}
	for rows.Next() {
		var transition models.AutomationTransition
		if err := rows.Scan(&transition.ID, &transition.ProjectID, &transition.AutomationID,
			&transition.VersionID, &transition.WorkItemID, &transition.InvocationID, &transition.ActivityID,
			&transition.FromNodeID, &transition.ToNodeID, &transition.EdgeID, &transition.EventKey,
			&transition.State, &transition.MetadataJSON, &transition.OccurredAt); err != nil {
			return models.AutomationTransitionPage{}, err
		}
		page.Items = append(page.Items, transition)
	}
	if err := rows.Err(); err != nil {
		return models.AutomationTransitionPage{}, err
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		last := page.Items[limit-1]
		page.NextCursor = encodeAutomationCursor(kind, last.OccurredAt, last.ID)
	}
	return page, nil
}

func (r *AutomationRepo) GetDefinitionVersion(ctx context.Context, projectID, automationID, versionID string) (*models.AutomationDefinition, error) {
	var automation models.Automation
	err := scanAutomation(r.db.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description,
		automation_type, lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id, template_revision,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID), &automation)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	definition, err := r.loadDefinition(ctx, r.db, automation, versionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return definition, err
}

func ReplayAutomationTransitions(transitions []models.AutomationTransition) []models.AutomationReplayFrame {
	return replayAutomationTransitionsFrom(nil, transitions)
}

func replayAutomationTransitionsFrom(seed []models.AutomationReplayPosition, transitions []models.AutomationTransition) []models.AutomationReplayFrame {
	positions := make(map[string]models.AutomationPositionState, len(seed))
	for _, position := range seed {
		positions[position.NodeID] = position.State
	}
	frames := make([]models.AutomationReplayFrame, 0, len(transitions))
	for _, transition := range transitions {
		if transition.FromNodeID != "" {
			delete(positions, transition.FromNodeID)
		}
		switch transition.State {
		case models.AutomationTransitionCompleted, models.AutomationTransitionCancelled:
			delete(positions, transition.ToNodeID)
		case models.AutomationTransitionWaiting:
			positions[transition.ToNodeID] = models.AutomationPositionWaiting
		case models.AutomationTransitionBlocked:
			positions[transition.ToNodeID] = models.AutomationPositionBlocked
		case models.AutomationTransitionFailed:
			positions[transition.ToNodeID] = models.AutomationPositionFailed
		default:
			positions[transition.ToNodeID] = models.AutomationPositionActive
		}
		nodeIDs := make([]string, 0, len(positions))
		for nodeID := range positions {
			nodeIDs = append(nodeIDs, nodeID)
		}
		sort.Strings(nodeIDs)
		frame := models.AutomationReplayFrame{State: transition.State, OccurredAt: transition.OccurredAt}
		for _, nodeID := range nodeIDs {
			frame.Positions = append(frame.Positions, models.AutomationReplayPosition{NodeID: nodeID, State: positions[nodeID]})
		}
		frames = append(frames, frame)
	}
	return frames
}

func (r *AutomationRepo) ReplayAutomationTransitionPage(ctx context.Context, projectID, automationID, workItemID, cursorValue string, transitions []models.AutomationTransition) ([]models.AutomationReplayFrame, error) {
	kind := automationCursorKind("transitions", automationID, "", workItemID)
	cursor, err := decodeAutomationCursor(kind, cursorValue)
	if err != nil {
		return nil, err
	}
	if cursor == nil {
		return ReplayAutomationTransitions(transitions), nil
	}
	rows, err := r.db.QueryContext(ctx, `WITH events AS (
		SELECT from_node_id AS node_id, 0 AS active, '' AS state, occurred_at, id, 0 AS sequence
		FROM automation_transitions
		WHERE project_id = ? AND automation_id = ? AND work_item_id = ? AND from_node_id IS NOT NULL
			AND (datetime(occurred_at) < datetime(?) OR (datetime(occurred_at) = datetime(?) AND id <= ?))
		UNION ALL
		SELECT to_node_id, CASE WHEN state IN ('completed','cancelled') THEN 0 ELSE 1 END,
			CASE state WHEN 'waiting' THEN 'waiting' WHEN 'blocked' THEN 'blocked' WHEN 'failed' THEN 'failed' ELSE 'active' END,
			occurred_at, id, 1
		FROM automation_transitions
		WHERE project_id = ? AND automation_id = ? AND work_item_id = ?
			AND (datetime(occurred_at) < datetime(?) OR (datetime(occurred_at) = datetime(?) AND id <= ?))
	), ranked AS (
		SELECT node_id, active, state, ROW_NUMBER() OVER (PARTITION BY node_id ORDER BY datetime(occurred_at) DESC, id DESC, sequence DESC) AS rank
		FROM events
	) SELECT node_id, state FROM ranked WHERE rank = 1 AND active = 1 ORDER BY node_id`,
		projectID, automationID, workItemID, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), cursor.ID,
		projectID, automationID, workItemID, automationCursorSQLTime(cursor.Time), automationCursorSQLTime(cursor.Time), cursor.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var seed []models.AutomationReplayPosition
	for rows.Next() {
		var position models.AutomationReplayPosition
		if err := rows.Scan(&position.NodeID, &position.State); err != nil {
			return nil, err
		}
		seed = append(seed, position)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return replayAutomationTransitionsFrom(seed, transitions), nil
}

func (r *AutomationRepo) GetAutomationMetrics(ctx context.Context, projectID, automationID, versionID string, now time.Time) (models.AutomationMetrics, error) {
	metrics := models.AutomationMetrics{}
	rows, err := r.db.QueryContext(ctx, `SELECT n.id, n.name, COUNT(DISTINCT CASE WHEN t.state = 'entered' THEN t.work_item_id END)
		FROM automation_nodes n LEFT JOIN automation_transitions t ON t.project_id = n.project_id
			AND t.automation_id = n.automation_id AND t.version_id = n.version_id AND t.to_node_id = n.id
		WHERE n.project_id = ? AND n.automation_id = ? AND n.version_id = ?
		GROUP BY n.id, n.name, n.position_x, n.position_y, n.node_key
		ORDER BY n.position_x, n.position_y, n.node_key`, projectID, automationID, versionID)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var point models.AutomationFunnelPoint
		if err := rows.Scan(&point.NodeID, &point.NodeName, &point.EnteredCount); err != nil {
			rows.Close()
			return metrics, err
		}
		metrics.Funnel = append(metrics.Funnel, point)
	}
	if err := rows.Close(); err != nil {
		return metrics, err
	}
	base := 0
	for _, point := range metrics.Funnel {
		if point.EnteredCount > 0 {
			base = point.EnteredCount
			break
		}
	}
	for i := range metrics.Funnel {
		if base > 0 {
			metrics.Funnel[i].ConversionPercent = float64(metrics.Funnel[i].EnteredCount) * 100 / float64(base)
		}
	}

	rows, err = r.db.QueryContext(ctx, `WITH ordered AS (
		SELECT work_item_id, to_node_id, occurred_at, id,
			LEAD(occurred_at) OVER (PARTITION BY work_item_id ORDER BY datetime(occurred_at), id) AS next_at
		FROM automation_transitions WHERE project_id = ? AND automation_id = ? AND version_id = ?
	), entries AS (
		SELECT work_item_id, to_node_id, occurred_at, next_at,
			ROW_NUMBER() OVER (PARTITION BY work_item_id, to_node_id ORDER BY datetime(occurred_at), id) AS entry_rank
		FROM ordered
	) SELECT n.id, n.name, COUNT(*), AVG((julianday(e.next_at) - julianday(e.occurred_at)) * 86400.0)
		FROM entries e JOIN automation_nodes n ON n.id = e.to_node_id AND n.version_id = ?
		WHERE e.entry_rank = 1 AND e.next_at IS NOT NULL
		GROUP BY n.id, n.name ORDER BY n.position_x, n.position_y, n.node_key`, projectID, automationID, versionID, versionID)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var point models.AutomationDurationPoint
		if err := rows.Scan(&point.NodeID, &point.NodeName, &point.SampleCount, &point.AverageSeconds); err != nil {
			rows.Close()
			return metrics, err
		}
		metrics.Durations = append(metrics.Durations, point)
	}
	if err := rows.Close(); err != nil {
		return metrics, err
	}

	failureCutoff := now.UTC().AddDate(0, 0, -30)
	rows, err = r.db.QueryContext(ctx, `WITH failures AS (
		SELECT node_id, completed_at AS failed_at FROM automation_activities
		WHERE project_id = ? AND automation_id = ? AND status = 'failed' AND completed_at >= ?
		UNION ALL
		SELECT trigger_node_id, completed_at FROM automation_invocations
		WHERE project_id = ? AND automation_id = ? AND status = 'failed' AND completed_at >= ?
	) SELECT n.id, n.name, COUNT(*), MAX(f.failed_at) FROM failures f JOIN automation_nodes n ON n.id = f.node_id
		GROUP BY n.id, n.name ORDER BY COUNT(*) DESC, MAX(f.failed_at) DESC LIMIT 10`, projectID, automationID, failureCutoff, projectID, automationID, failureCutoff)
	if err != nil {
		return metrics, err
	}
	for rows.Next() {
		var summary models.AutomationFailureSummary
		var lastFailure string
		if err := rows.Scan(&summary.NodeID, &summary.NodeName, &summary.Count, &lastFailure); err != nil {
			rows.Close()
			return metrics, err
		}
		summary.LastFailure = parseSQLiteTime(lastFailure)
		if summary.LastFailure.IsZero() {
			rows.Close()
			return metrics, fmt.Errorf("invalid automation failure timestamp %q", lastFailure)
		}
		metrics.Failures = append(metrics.Failures, summary)
	}
	if err := rows.Close(); err != nil {
		return metrics, err
	}

	rows, err = r.db.QueryContext(ctx, `SELECT n.id, n.name,
		SUM(CASE WHEN p.state = 'waiting' THEN 1 ELSE 0 END),
		SUM(CASE WHEN p.state = 'blocked' THEN 1 ELSE 0 END)
		FROM automation_work_item_positions p JOIN automation_nodes n ON n.id = p.node_id AND n.version_id = p.version_id
		WHERE p.project_id = ? AND p.automation_id = ?
		GROUP BY n.id, n.name HAVING COUNT(*) > 0 ORDER BY 3 DESC, 4 DESC, n.name LIMIT 10`, projectID, automationID)
	if err != nil {
		return metrics, err
	}
	defer rows.Close()
	for rows.Next() {
		var summary models.AutomationBottleneckSummary
		if err := rows.Scan(&summary.NodeID, &summary.NodeName, &summary.Waiting, &summary.Blocked); err != nil {
			return metrics, err
		}
		metrics.Bottlenecks = append(metrics.Bottlenecks, summary)
	}
	return metrics, rows.Err()
}

func (r *AutomationRepo) RecomputeAutomationHealth(ctx context.Context, projectID, automationID string, now time.Time) (models.AutomationHealth, error) {
	health := models.AutomationHealth{State: models.AutomationHealthUnknown, Reason: "No terminal invocation yet", EvaluatedAt: now.UTC()}
	var blocked int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_work_item_positions
		WHERE project_id = ? AND automation_id = ? AND state IN ('blocked','failed')`, projectID, automationID).Scan(&blocked); err != nil {
		return health, err
	}
	var recentCount, recentFailures int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) FROM (
		SELECT status FROM automation_invocations WHERE project_id = ? AND automation_id = ?
			AND status IN ('completed','failed')
		ORDER BY COALESCE(completed_at, updated_at) DESC, id DESC LIMIT 3)`, projectID, automationID).Scan(&recentCount, &recentFailures); err != nil {
		return health, err
	}
	externalState, err := r.AutomationExternalState(ctx, projectID, automationID, now.UTC().Add(-AutomationExternalStaleAfter))
	if err != nil {
		return health, err
	}
	switch {
	case recentCount == 3 && recentFailures == 3:
		health.State = models.AutomationHealthUnhealthy
		health.Reason = "Three consecutive trigger or dispatch failures"
	case recentFailures > 0 || blocked > 0 || externalState.Stale:
		health.State = models.AutomationHealthDegraded
		health.Reason = fmt.Sprintf("%d recent failed invocation(s), %d blocked or failed position(s)", recentFailures, blocked)
		if externalState.Stale {
			health.Reason += ", external GitHub state is stale"
		}
	case recentCount > 0:
		health.State = models.AutomationHealthHealthy
		health.Reason = "Recent triggers and dispatches completed without systemic errors"
	}
	result, err := execBoundSQLite(ctx, r.db, `UPDATE automations SET health_state = ?, health_reason = ?,
		health_evaluated_at = ? WHERE project_id = ? AND id = ?`, health.State, health.Reason, health.EvaluatedAt, projectID, automationID)
	if err != nil {
		return health, err
	}
	if changed, _ := result.RowsAffected(); changed == 0 {
		return models.AutomationHealth{}, sql.ErrNoRows
	}
	return health, nil
}

func (r *AutomationRepo) RecomputeAutomationHealthForAll(ctx context.Context, now time.Time, limit int) error {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var afterAutomationID string
	for {
		rows, err := r.db.QueryContext(ctx, `SELECT project_id, id FROM automations
			WHERE published_version_id IS NOT NULL AND id > ?
			ORDER BY id LIMIT ?`, afterAutomationID, limit)
		if err != nil {
			return err
		}
		var ids [][2]string
		for rows.Next() {
			var value [2]string
			if err := rows.Scan(&value[0], &value[1]); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, value)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, value := range ids {
			if _, err := r.RecomputeAutomationHealth(ctx, value[0], value[1], now); err != nil {
				return err
			}
		}
		if len(ids) < limit {
			return nil
		}
		afterAutomationID = ids[len(ids)-1][1]
	}
}
