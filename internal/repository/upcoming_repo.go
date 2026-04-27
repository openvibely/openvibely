package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type UpcomingRepo struct {
	db *sql.DB
}

func NewUpcomingRepo(db *sql.DB) *UpcomingRepo {
	return &UpcomingRepo{db: db}
}

// UpcomingTaskRow is an intermediate struct for scanning joined task+agent+schedule data
type UpcomingTaskRow struct {
	// Task fields
	TaskID       string
	ProjectID    string
	Title        string
	Category     string
	Priority     int
	Status       string
	Prompt       string
	AgentID      *string
	Tag          string
	DisplayOrder int
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// Agent name
	AgentName *string
	// Schedule fields (nullable)
	ScheduleID       *string
	RunAt            *time.Time
	RepeatType       *string
	RepeatInterval   *int
	ScheduleEnabled  *bool
	NextRun          *time.Time
	LastRun          *time.Time
}

// ListRunningTasks returns tasks currently being executed for a project
func (r *UpcomingRepo) ListRunningTasks(ctx context.Context, projectID string) ([]models.UpcomingTask, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT t.id, t.project_id, t.title, t.category, t.priority, t.status, t.prompt,
			t.agent_id, t.tag, t.display_order, t.created_at, t.updated_at,
			ac.name as agent_name,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL
		 FROM tasks t
		 LEFT JOIN agent_configs ac ON ac.id = t.agent_id
		 WHERE t.project_id = ? AND t.status = 'running' AND t.category != 'chat'
		 ORDER BY t.updated_at DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing running tasks: %w", err)
	}
	defer rows.Close()
	return r.scanUpcomingTasks(rows)
}

// ListPendingActiveTasks returns active tasks with pending status (queued for execution)
func (r *UpcomingRepo) ListPendingActiveTasks(ctx context.Context, projectID string) ([]models.UpcomingTask, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT t.id, t.project_id, t.title, t.category, t.priority, t.status, t.prompt,
			t.agent_id, t.tag, t.display_order, t.created_at, t.updated_at,
			ac.name as agent_name,
			NULL, NULL, NULL, NULL, NULL, NULL, NULL
		 FROM tasks t
		 LEFT JOIN agent_configs ac ON ac.id = t.agent_id
		 WHERE t.project_id = ? AND t.category = 'active' AND t.status = 'pending'
		 ORDER BY t.priority DESC, t.display_order ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing pending active tasks: %w", err)
	}
	defer rows.Close()
	return r.scanUpcomingTasks(rows)
}

// ListUpcomingScheduledTasks returns scheduled tasks with a future next_run
func (r *UpcomingRepo) ListUpcomingScheduledTasks(ctx context.Context, projectID string, until time.Time) ([]models.UpcomingTask, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT t.id, t.project_id, t.title, t.category, t.priority, t.status, t.prompt,
			t.agent_id, t.tag, t.display_order, t.created_at, t.updated_at,
			ac.name as agent_name,
			s.id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.next_run, s.last_run
		 FROM tasks t
		 JOIN schedules s ON s.task_id = t.id
		 LEFT JOIN agent_configs ac ON ac.id = t.agent_id
		 WHERE t.project_id = ? AND s.enabled = 1 AND s.next_run IS NOT NULL AND s.next_run <= ?
		 ORDER BY s.next_run ASC`, projectID, until)
	if err != nil {
		return nil, fmt.Errorf("listing upcoming scheduled tasks: %w", err)
	}
	defer rows.Close()
	return r.scanUpcomingTasks(rows)
}

// ListRecentExecutions returns executions completed within the given time range
func (r *UpcomingRepo) ListRecentExecutions(ctx context.Context, projectID string, since time.Time) ([]models.HistoryExecution, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT e.id, e.task_id, COALESCE(e.agent_config_id, ''), e.status, e.prompt_sent, e.output,
			e.error_message, e.tokens_used, e.duration_ms, e.started_at, e.completed_at,
			t.title as task_title,
			COALESCE(ac.name, '') as agent_name
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 LEFT JOIN agent_configs ac ON ac.id = e.agent_config_id
		 WHERE t.project_id = ? AND t.category != 'chat' AND e.started_at >= ?
		 ORDER BY e.started_at DESC`, projectID, since)
	if err != nil {
		return nil, fmt.Errorf("listing recent executions: %w", err)
	}
	defer rows.Close()

	var results []models.HistoryExecution
	for rows.Next() {
		var de models.HistoryExecution
		if err := rows.Scan(
			&de.Execution.ID, &de.Execution.TaskID, &de.Execution.AgentConfigID,
			&de.Execution.Status, &de.Execution.PromptSent, &de.Execution.Output,
			&de.Execution.ErrorMessage, &de.Execution.TokensUsed, &de.Execution.DurationMs,
			&de.Execution.StartedAt, &de.Execution.CompletedAt,
			&de.TaskTitle, &de.AgentName,
		); err != nil {
			return nil, fmt.Errorf("scanning history execution: %w", err)
		}
		results = append(results, de)
	}
	return results, rows.Err()
}

// GetHistorySummary returns aggregate metrics for executions in a time range
func (r *UpcomingRepo) GetHistorySummary(ctx context.Context, projectID string, since time.Time) (models.HistorySummary, error) {
	var s models.HistorySummary
	var avgDuration float64
	err := r.db.QueryRowContext(ctx,
		`SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN e.status = 'completed' THEN 1 ELSE 0 END), 0) as success,
			COALESCE(SUM(CASE WHEN e.status = 'failed' THEN 1 ELSE 0 END), 0) as failure,
			COALESCE(SUM(CASE WHEN e.status = 'cancelled' THEN 1 ELSE 0 END), 0) as cancelled,
			COALESCE(SUM(e.duration_ms), 0) as total_duration,
			COALESCE(AVG(CASE WHEN e.duration_ms > 0 THEN e.duration_ms END), 0) as avg_duration
		 FROM executions e
		 JOIN tasks t ON t.id = e.task_id
		 WHERE t.project_id = ? AND t.category != 'chat' AND e.started_at >= ?`,
		projectID, since).
		Scan(&s.TotalExecutions, &s.SuccessCount, &s.FailureCount, &s.CancelledCount,
			&s.TotalDurationMs, &avgDuration)
	if err != nil {
		return s, fmt.Errorf("getting history summary: %w", err)
	}
	s.AvgDurationMs = int64(avgDuration)
	return s, nil
}

// GetTaskSummary returns high-level task metrics for the Pulse dashboard
func (r *UpcomingRepo) GetTaskSummary(ctx context.Context, projectID string, now time.Time) (*models.TaskSummary, error) {
	s := &models.TaskSummary{}

	// Get counts by status (excluding chat and completed-category tasks)
	rows, err := r.db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM tasks
		 WHERE project_id = ? AND category != 'chat'
		 GROUP BY status`, projectID)
	if err != nil {
		return nil, fmt.Errorf("getting task status counts: %w", err)
	}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning status count: %w", err)
		}
		switch models.TaskStatus(status) {
		case models.StatusPending:
			s.PendingCount = count
		case models.StatusRunning:
			s.RunningCount = count
		case models.StatusCompleted:
			s.CompletedCount = count
		case models.StatusFailed:
			s.FailedCount = count
		}
	}
	rows.Close()

	// Get counts by category (excluding chat)
	rows, err = r.db.QueryContext(ctx,
		`SELECT category, COUNT(*) FROM tasks
		 WHERE project_id = ? AND category != 'chat'
		 GROUP BY category`, projectID)
	if err != nil {
		return nil, fmt.Errorf("getting task category counts: %w", err)
	}
	for rows.Next() {
		var category string
		var count int
		if err := rows.Scan(&category, &count); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning category count: %w", err)
		}
		switch models.TaskCategory(category) {
		case models.CategoryActive:
			s.ActiveCount = count
		case models.CategoryBacklog:
			s.BacklogCount = count
		case models.CategoryScheduled:
			s.ScheduledCount = count
		}
	}
	rows.Close()

	// Get counts by priority for non-completed tasks (active + backlog + scheduled)
	rows, err = r.db.QueryContext(ctx,
		`SELECT priority, COUNT(*) FROM tasks
		 WHERE project_id = ? AND category IN ('active', 'backlog', 'scheduled')
		   AND status NOT IN ('completed', 'cancelled')
		 GROUP BY priority`, projectID)
	if err != nil {
		return nil, fmt.Errorf("getting task priority counts: %w", err)
	}
	for rows.Next() {
		var priority, count int
		if err := rows.Scan(&priority, &count); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scanning priority count: %w", err)
		}
		switch priority {
		case 4:
			s.UrgentCount = count
		case 3:
			s.HighCount = count
		case 2:
			s.NormalCount = count
		case 1:
			s.LowCount = count
		}
		s.TotalPending += count
	}
	rows.Close()

	// Get scheduled task counts: today, this week, overdue
	endOfToday := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, now.Location())
	endOfWeek := now.AddDate(0, 0, 7)

	err = r.db.QueryRowContext(ctx,
		`SELECT
			COALESCE(SUM(CASE WHEN s.next_run <= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN s.next_run <= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN s.next_run < ? THEN 1 ELSE 0 END), 0)
		 FROM tasks t
		 JOIN schedules s ON s.task_id = t.id
		 WHERE t.project_id = ? AND s.enabled = 1 AND s.next_run IS NOT NULL`,
		endOfToday, endOfWeek, now, projectID).
		Scan(&s.ScheduledToday, &s.ScheduledThisWeek, &s.OverdueCount)
	if err != nil {
		return nil, fmt.Errorf("getting schedule counts: %w", err)
	}

	return s, nil
}

func (r *UpcomingRepo) scanUpcomingTasks(rows *sql.Rows) ([]models.UpcomingTask, error) {
	var results []models.UpcomingTask
	for rows.Next() {
		var row UpcomingTaskRow
		if err := rows.Scan(
			&row.TaskID, &row.ProjectID, &row.Title, &row.Category, &row.Priority,
			&row.Status, &row.Prompt, &row.AgentID, &row.Tag, &row.DisplayOrder,
			&row.CreatedAt, &row.UpdatedAt,
			&row.AgentName,
			&row.ScheduleID, &row.RunAt, &row.RepeatType, &row.RepeatInterval,
			&row.ScheduleEnabled, &row.NextRun, &row.LastRun,
		); err != nil {
			return nil, fmt.Errorf("scanning upcoming task: %w", err)
		}

		bt := models.UpcomingTask{
			Task: models.Task{
				ID:           row.TaskID,
				ProjectID:    row.ProjectID,
				Title:        row.Title,
				Category:     models.TaskCategory(row.Category),
				Priority:     row.Priority,
				Status:       models.TaskStatus(row.Status),
				Prompt:       row.Prompt,
				AgentID:      row.AgentID,
				Tag:          models.TaskTag(row.Tag),
				DisplayOrder: row.DisplayOrder,
				CreatedAt:    row.CreatedAt,
				UpdatedAt:    row.UpdatedAt,
			},
		}
		if row.AgentName != nil {
			bt.AgentName = *row.AgentName
		} else {
			bt.AgentName = "Default Agent"
		}
		if row.NextRun != nil {
			bt.NextRun = row.NextRun
		}
		if row.ScheduleID != nil {
			bt.Schedule = &models.Schedule{
				ID:             *row.ScheduleID,
				TaskID:         row.TaskID,
				RepeatType:     models.RepeatType(*row.RepeatType),
				RepeatInterval: *row.RepeatInterval,
				Enabled:        *row.ScheduleEnabled,
				NextRun:        row.NextRun,
				LastRun:        row.LastRun,
			}
			if row.RunAt != nil {
				bt.Schedule.RunAt = *row.RunAt
			}
		}

		results = append(results, bt)
	}
	return results, rows.Err()
}
