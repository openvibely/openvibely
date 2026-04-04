package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrDuplicateTask = errors.New("task with this name already exists in this project")

type TaskRepo struct {
	db          *sql.DB
	broadcaster *events.Broadcaster
}

func NewTaskRepo(db *sql.DB, broadcaster *events.Broadcaster) *TaskRepo {
	return &TaskRepo{
		db:          db,
		broadcaster: broadcaster,
	}
}

func (r *TaskRepo) ListByProject(ctx context.Context, projectID string, category string) ([]models.Task, error) {
	return r.ListByProjectWithSort(ctx, projectID, category, "")
}

func (r *TaskRepo) ListByProjectWithSort(ctx context.Context, projectID string, category string, sortBy string) ([]models.Task, error) {
	return r.ListByProjectWithCategorySorts(ctx, projectID, category, sortBy, "")
}

func (r *TaskRepo) ListByProjectWithCategorySorts(ctx context.Context, projectID string, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	query := `SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE project_id = ?`
	args := []any{projectID}

	if category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}

	// Fetch in stable default order and apply category-specific sorts in memory.
	query += ` ORDER BY display_order ASC, created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	switch category {
	case string(models.CategoryBacklog):
		sortTasks(tasks, backlogSort)
	case string(models.CategoryCompleted):
		sortTasks(tasks, completedSort)
	default:
		sortCategoryTasks(tasks, models.CategoryBacklog, backlogSort)
		sortCategoryTasks(tasks, models.CategoryCompleted, completedSort)
	}

	return tasks, nil
}

func sortCategoryTasks(tasks []models.Task, category models.TaskCategory, sortBy string) {
	if len(tasks) < 2 || sortBy == "" {
		return
	}

	categoryIndexes := make([]int, 0, len(tasks))
	categoryTasks := make([]models.Task, 0, len(tasks))
	for i, task := range tasks {
		if task.Category == category {
			categoryIndexes = append(categoryIndexes, i)
			categoryTasks = append(categoryTasks, task)
		}
	}
	if len(categoryTasks) < 2 {
		return
	}

	sortTasks(categoryTasks, sortBy)
	for i, idx := range categoryIndexes {
		tasks[idx] = categoryTasks[i]
	}
}

func sortTasks(tasks []models.Task, sortBy string) {
	if len(tasks) < 2 || sortBy == "" {
		return
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		return taskSortLess(tasks[i], tasks[j], sortBy)
	})
}

func taskSortLess(a models.Task, b models.Task, sortBy string) bool {
	switch sortBy {
	case "title_asc":
		aTitle := strings.ToLower(a.Title)
		bTitle := strings.ToLower(b.Title)
		if aTitle != bTitle {
			return aTitle < bTitle
		}
		return taskDisplayOrderAsc(a, b)
	case "title_desc":
		aTitle := strings.ToLower(a.Title)
		bTitle := strings.ToLower(b.Title)
		if aTitle != bTitle {
			return aTitle > bTitle
		}
		return taskDisplayOrderAsc(a, b)
	case "created_asc":
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return taskDisplayOrderAsc(a, b)
	case "created_desc":
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return taskDisplayOrderDesc(a, b)
	case "priority_asc":
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return taskDisplayOrderAsc(a, b)
	case "priority_desc":
		if a.Priority != b.Priority {
			return a.Priority > b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return taskDisplayOrderDesc(a, b)
	default:
		return taskDisplayOrderAsc(a, b)
	}
}

func taskDisplayOrderAsc(a models.Task, b models.Task) bool {
	if a.DisplayOrder != b.DisplayOrder {
		return a.DisplayOrder < b.DisplayOrder
	}
	return a.CreatedAt.Before(b.CreatedAt)
}

func taskDisplayOrderDesc(a models.Task, b models.Task) bool {
	if a.DisplayOrder != b.DisplayOrder {
		return a.DisplayOrder > b.DisplayOrder
	}
	return a.CreatedAt.After(b.CreatedAt)
}

func (r *TaskRepo) GetByID(ctx context.Context, id string) (*models.Task, error) {
	var t models.Task
	err := r.db.QueryRowContext(ctx,
		`SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}
	return &t, nil
}

func (r *TaskRepo) Create(ctx context.Context, t *models.Task) error {
	// Get the max display_order for this project and category, then add 1
	var maxOrder sql.NullInt64
	err := r.db.QueryRowContext(ctx,
		`SELECT MAX(display_order) FROM tasks WHERE project_id = ? AND category = ?`,
		t.ProjectID, t.Category).Scan(&maxOrder)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("getting max display_order: %w", err)
	}

	displayOrder := 0
	if maxOrder.Valid {
		displayOrder = int(maxOrder.Int64) + 1
	}

	autoMerge := 0
	if t.AutoMerge {
		autoMerge = 1
	}
	err = r.db.QueryRowContext(ctx,
		`INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		t.ProjectID, t.Title, t.Category, t.Priority, t.Status, t.Prompt, t.AgentID, t.AgentDefinitionID, t.Tag, displayOrder, t.ParentTaskID, t.ChainConfig, t.WorktreePath, t.WorktreeBranch, autoMerge, t.MergeTargetBranch, t.MergeStatus, t.BaseBranch, t.BaseCommitSHA, t.LineageDepth, t.CreatedVia, t.TelegramChatID).
		Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: tasks.project_id, tasks.title") {
			return ErrDuplicateTask
		}
		return fmt.Errorf("creating task: %w", err)
	}
	t.DisplayOrder = displayOrder
	return nil
}

func (r *TaskRepo) Update(ctx context.Context, t *models.Task) error {
	autoMerge := 0
	if t.AutoMerge {
		autoMerge = 1
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET title = ?, category = ?, priority = ?, status = ?,
		 prompt = ?, agent_id = ?, agent_definition_id = ?, tag = ?, display_order = ?, parent_task_id = ?, chain_config = ?,
		 auto_merge = ?, merge_target_branch = ?, base_branch = ?, base_commit_sha = ?, lineage_depth = ?, updated_at = datetime('now')
		 WHERE id = ?`,
		t.Title, t.Category, t.Priority, t.Status, t.Prompt, t.AgentID, t.AgentDefinitionID, t.Tag, t.DisplayOrder, t.ParentTaskID, t.ChainConfig, autoMerge, t.MergeTargetBranch, t.BaseBranch, t.BaseCommitSHA, t.LineageDepth, t.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed: tasks.project_id, tasks.title") {
			return ErrDuplicateTask
		}
		return fmt.Errorf("updating task: %w", err)
	}
	return nil
}

func (r *TaskRepo) UpdateCategory(ctx context.Context, id string, category models.TaskCategory) error {
	// Get the task first to know the old category and project ID
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("getting task before category update: %w", err)
	}
	if task == nil {
		return fmt.Errorf("task not found: %s", id)
	}

	oldCategory := task.Category

	// Get the max display_order in the new category and add 1
	var maxOrder sql.NullInt64
	err = r.db.QueryRowContext(ctx,
		`SELECT MAX(display_order) FROM tasks WHERE project_id = ? AND category = ?`,
		task.ProjectID, category).Scan(&maxOrder)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("getting max display_order: %w", err)
	}

	displayOrder := 0
	if maxOrder.Valid {
		displayOrder = int(maxOrder.Int64) + 1
	}

	_, err = r.db.ExecContext(ctx,
		`UPDATE tasks SET category = ?, display_order = ?, updated_at = datetime('now') WHERE id = ?`,
		category, displayOrder, id)
	if err != nil {
		return fmt.Errorf("updating task category: %w", err)
	}

	// Publish event if broadcaster is available
	if r.broadcaster != nil && oldCategory != category {
		r.broadcaster.Publish(events.TaskEvent{
			Type:        events.TaskCategoryChanged,
			TaskID:      id,
			TaskName:    task.Title,
			ProjectID:   task.ProjectID,
			Category:    string(category),
			OldCategory: string(oldCategory),
		})
	}

	return nil
}

func (r *TaskRepo) UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error {
	// Get the task first to know the old status and project ID
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return fmt.Errorf("getting task before status update: %w", err)
	}
	if task == nil {
		return fmt.Errorf("task not found: %s", id)
	}

	oldStatus := task.Status

	_, err = r.db.ExecContext(ctx,
		`UPDATE tasks SET status = ?, updated_at = datetime('now') WHERE id = ?`,
		status, id)
	if err != nil {
		return fmt.Errorf("updating task status: %w", err)
	}

	// Publish event if broadcaster is available
	if r.broadcaster != nil && oldStatus != status {
		r.broadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    id,
			TaskName:  task.Title,
			ProjectID: task.ProjectID,
			Status:    string(status),
			OldStatus: string(oldStatus),
			Category:  string(task.Category),
		})
	}

	return nil
}

// SetPendingIfNotRunningOrQueued atomically sets status to pending
// unless the task is already running or queued.
// Returns true if the task row was updated.
func (r *TaskRepo) SetPendingIfNotRunningOrQueued(ctx context.Context, id string) (bool, error) {
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("getting task before pending update: %w", err)
	}
	if task == nil {
		return false, fmt.Errorf("task not found: %s", id)
	}

	result, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'pending', updated_at = datetime('now')
		 WHERE id = ? AND status NOT IN ('running', 'queued')`,
		id)
	if err != nil {
		return false, fmt.Errorf("setting task pending with guard: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("pending update rows affected: %w", err)
	}

	updated := rows > 0

	if updated && r.broadcaster != nil && task.Status != models.StatusPending {
		r.broadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    id,
			TaskName:  task.Title,
			ProjectID: task.ProjectID,
			Status:    string(models.StatusPending),
			OldStatus: string(task.Status),
			Category:  string(task.Category),
		})
	}

	return updated, nil
}

// ClaimTask atomically sets status to running only if the task is currently pending.
// Returns true if the claim succeeded, false if the task was already running/completed/failed.
func (r *TaskRepo) ClaimTask(ctx context.Context, id string) (bool, error) {
	// Get the task first to know the project ID
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("getting task before claim: %w", err)
	}
	if task == nil {
		return false, fmt.Errorf("task not found: %s", id)
	}

	result, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'running', updated_at = datetime('now') WHERE id = ? AND status = 'pending'`,
		id)
	if err != nil {
		return false, fmt.Errorf("claiming task: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claiming task rows affected: %w", err)
	}

	claimed := rows > 0

	// Publish event if the claim succeeded and broadcaster is available
	if claimed && r.broadcaster != nil {
		r.broadcaster.Publish(events.TaskEvent{
			Type:      events.TaskStatusChanged,
			TaskID:    id,
			TaskName:  task.Title,
			ProjectID: task.ProjectID,
			Status:    string(models.StatusRunning),
			OldStatus: string(models.StatusPending),
			Category:  string(task.Category),
		})
	}

	return claimed, nil
}

// SearchByTitle searches for non-chat tasks matching a title substring within a project.
// Returns tasks ordered by relevance: exact match first, then prefix match, then contains.
// Excludes chat tasks (CategoryChat) since those are internal chat messages, not user tasks.
func (r *TaskRepo) SearchByTitle(ctx context.Context, projectID string, titleQuery string) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE project_id = ? AND category != 'chat' AND title LIKE ?
		 ORDER BY
		   CASE WHEN LOWER(title) = LOWER(?) THEN 0
		        WHEN LOWER(title) LIKE LOWER(? || '%') THEN 1
		        ELSE 2 END,
		   updated_at DESC
		 LIMIT 10`,
		projectID, "%"+titleQuery+"%", titleQuery, titleQuery)
	if err != nil {
		return nil, fmt.Errorf("searching tasks by title: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting task: %w", err)
	}
	return nil
}

func (r *TaskRepo) ListActivePending(ctx context.Context) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE category = 'active' AND status = 'pending'
		 ORDER BY priority DESC, display_order ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing active pending tasks: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ListStaleQueuedTasks finds active tasks with status='queued' that have been
// in that state for longer than the given duration. These may be orphaned by
// a thread follow-up goroutine that crashed or timed out without cleaning up.
func (r *TaskRepo) ListStaleQueuedTasks(ctx context.Context, staleDuration time.Duration) ([]models.Task, error) {
	cutoff := time.Now().UTC().Add(-staleDuration).Format("2006-01-02 15:04:05")
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE category = 'active' AND status = 'queued' AND updated_at < ?
		 ORDER BY priority DESC, display_order ASC, created_at ASC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("listing stale queued tasks: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// TaskWithSchedule represents a task with its schedule information for calendar view
type TaskWithSchedule struct {
	Task     models.Task
	Schedule *models.Schedule
}

func (r *TaskRepo) ListWithSchedulesByProject(ctx context.Context, projectID string) ([]TaskWithSchedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT t.id, t.project_id, t.title, t.category, t.priority, t.status, t.prompt, t.agent_id, t.agent_definition_id, t.tag, t.display_order, t.parent_task_id, t.chain_config, t.worktree_path, t.worktree_branch, t.auto_merge, t.merge_target_branch, t.merge_status, t.base_branch, t.base_commit_sha, t.lineage_depth, t.created_via, t.telegram_chat_id, t.created_at, t.updated_at,
		 s.id, s.task_id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.next_run, s.last_run, s.created_at, s.updated_at
		 FROM tasks t
		 LEFT JOIN schedules s ON t.id = s.task_id
		 WHERE t.project_id = ? AND (t.category = 'scheduled' OR s.id IS NOT NULL)
		 ORDER BY s.next_run ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing tasks with schedules: %w", err)
	}
	defer rows.Close()

	var results []TaskWithSchedule
	for rows.Next() {
		var tws TaskWithSchedule
		var schedID, schedTaskID sql.NullString
		var schedRunAt, schedCreatedAt, schedUpdatedAt sql.NullTime
		var schedRepeatType, schedRepeatInterval, schedEnabled sql.NullString
		var schedNextRun, schedLastRun sql.NullTime

		if err := rows.Scan(
			&tws.Task.ID, &tws.Task.ProjectID, &tws.Task.Title, &tws.Task.Category,
			&tws.Task.Priority, &tws.Task.Status, &tws.Task.Prompt, &tws.Task.AgentID, &tws.Task.AgentDefinitionID, &tws.Task.Tag, &tws.Task.DisplayOrder, &tws.Task.ParentTaskID, &tws.Task.ChainConfig, &tws.Task.WorktreePath, &tws.Task.WorktreeBranch, &tws.Task.AutoMerge, &tws.Task.MergeTargetBranch, &tws.Task.MergeStatus, &tws.Task.BaseBranch, &tws.Task.BaseCommitSHA, &tws.Task.LineageDepth, &tws.Task.CreatedVia, &tws.Task.TelegramChatID, &tws.Task.CreatedAt, &tws.Task.UpdatedAt,
			&schedID, &schedTaskID, &schedRunAt, &schedRepeatType, &schedRepeatInterval, &schedEnabled, &schedNextRun, &schedLastRun, &schedCreatedAt, &schedUpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning task with schedule: %w", err)
		}

		if schedID.Valid {
			var enabled bool
			if schedEnabled.String == "1" || schedEnabled.String == "true" {
				enabled = true
			}
			var repeatInterval int
			if schedRepeatInterval.Valid {
				fmt.Sscanf(schedRepeatInterval.String, "%d", &repeatInterval)
			}
			tws.Schedule = &models.Schedule{
				ID:             schedID.String,
				TaskID:         schedTaskID.String,
				RunAt:          schedRunAt.Time,
				RepeatType:     models.RepeatType(schedRepeatType.String),
				RepeatInterval: repeatInterval,
				Enabled:        enabled,
				CreatedAt:      schedCreatedAt.Time,
				UpdatedAt:      schedUpdatedAt.Time,
			}
			if schedNextRun.Valid {
				tws.Schedule.NextRun = &schedNextRun.Time
			}
			if schedLastRun.Valid {
				tws.Schedule.LastRun = &schedLastRun.Time
			}
		}

		results = append(results, tws)
	}
	return results, rows.Err()
}

func (r *TaskRepo) CountByProjectAndCategory(ctx context.Context, projectID string) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT category, COUNT(*) FROM tasks WHERE project_id = ? GROUP BY category`, projectID)
	if err != nil {
		return nil, fmt.Errorf("counting tasks: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var category string
		var count int
		if err := rows.Scan(&category, &count); err != nil {
			return nil, fmt.Errorf("scanning count: %w", err)
		}
		counts[category] = count
	}
	return counts, rows.Err()
}

// CountPendingByProject returns the number of active pending tasks for each project.
// These are tasks in the 'active' category with status='pending' — i.e., tasks queued
// for worker execution but waiting because workers are busy or at capacity.
func (r *TaskRepo) CountPendingByProject(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, COUNT(*) FROM tasks WHERE category = 'active' AND status = 'pending' GROUP BY project_id`)
	if err != nil {
		return nil, fmt.Errorf("counting pending tasks by project: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var projectID string
		var count int
		if err := rows.Scan(&projectID, &count); err != nil {
			return nil, fmt.Errorf("scanning pending count: %w", err)
		}
		counts[projectID] = count
	}
	return counts, rows.Err()
}

// ResetOrphanedRunning resets any tasks with status='running' back to 'pending'.
// Called on startup to recover tasks that were left in running state when the server was killed.
func (r *TaskRepo) ResetOrphanedRunning(ctx context.Context) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET status = 'pending', updated_at = datetime('now')
		 WHERE status = 'running'`)
	if err != nil {
		return 0, fmt.Errorf("resetting orphaned running tasks: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}
	return int(rows), nil
}

// MoveCompletedActiveToCompleted moves all tasks with category='active' and status='completed'
// to category='completed'. Returns the number of tasks moved.
func (r *TaskRepo) MoveCompletedActiveToCompleted(ctx context.Context) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET category = 'completed', updated_at = datetime('now')
		 WHERE category = 'active' AND status = 'completed'`)
	if err != nil {
		return 0, fmt.Errorf("moving completed active tasks to completed: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}
	return int(rows), nil
}

// ListByCategory returns all tasks in a specific category across all projects.
func (r *TaskRepo) ListByCategory(ctx context.Context, category models.TaskCategory) ([]models.Task, error) {
	query := `SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE category = ?
		 ORDER BY display_order ASC, created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, category)
	if err != nil {
		return nil, fmt.Errorf("listing tasks by category: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// DeleteAllCompleted deletes all tasks in the 'completed' category.
// Returns the number of tasks deleted.
func (r *TaskRepo) DeleteAllCompleted(ctx context.Context, projectID string) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM tasks WHERE category = 'completed' AND project_id = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("deleting completed tasks: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}
	return int(rows), nil
}

// DeleteAllBacklog deletes all tasks in the 'backlog' category.
// Returns the number of tasks deleted.
func (r *TaskRepo) DeleteAllBacklog(ctx context.Context, projectID string) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM tasks WHERE category = 'backlog' AND project_id = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("deleting backlog tasks: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}
	return int(rows), nil
}

// DeleteAllChat deletes all tasks in the 'chat' category for a project.
// Executions are cascade-deleted via FK constraint.
// Returns the number of tasks deleted.
func (r *TaskRepo) DeleteAllChat(ctx context.Context, projectID string) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM tasks WHERE category = 'chat' AND project_id = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("deleting chat tasks: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}
	return int(rows), nil
}

// ListRunningChatTaskIDs returns IDs of chat tasks that are still running or pending
// for a project. Used to cancel active goroutines before clearing chat history.
func (r *TaskRepo) ListRunningChatTaskIDs(ctx context.Context, projectID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM tasks WHERE category = 'chat' AND project_id = ? AND status IN ('pending', 'running')`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing running chat tasks: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning task id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ActivateAllBacklog moves all tasks in the 'backlog' category to 'active' category
// with status 'pending'. Returns the number of tasks updated.
func (r *TaskRepo) ActivateAllBacklog(ctx context.Context, projectID string) (int, error) {
	result, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET category = 'active', status = 'pending' WHERE category = 'backlog' AND project_id = ?`, projectID)
	if err != nil {
		return 0, fmt.Errorf("activating backlog tasks: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("getting rows affected: %w", err)
	}

	// Emit task updated events for each activated task
	if r.broadcaster != nil && rows > 0 {
		// Get the updated tasks to emit events
		tasks, err := r.ListByProject(ctx, projectID, string(models.CategoryActive))
		if err == nil {
			for _, task := range tasks {
				r.broadcaster.Publish(events.TaskEvent{
					Type:      events.TaskCategoryChanged,
					TaskID:    task.ID,
					TaskName:  task.Title,
					ProjectID: task.ProjectID,
					Category:  string(task.Category),
					Status:    string(task.Status),
				})
			}
		}
	}

	return int(rows), nil
}

// ReorderTask moves a task to a new position within its category.
// The newPosition is the target display_order (0-indexed).
// All tasks between the old and new positions will have their display_order adjusted.
func (r *TaskRepo) ReorderTask(ctx context.Context, taskID string, newPosition int) error {
	// Get the task
	task, err := r.GetByID(ctx, taskID)
	if err != nil {
		return fmt.Errorf("getting task before reorder: %w", err)
	}
	if task == nil {
		return fmt.Errorf("task not found: %s", taskID)
	}

	oldPosition := task.DisplayOrder

	// If position hasn't changed, do nothing
	if oldPosition == newPosition {
		return nil
	}

	// Update positions of affected tasks
	if newPosition < oldPosition {
		// Moving up: increment display_order of tasks between newPosition and oldPosition
		_, err = r.db.ExecContext(ctx,
			`UPDATE tasks
			 SET display_order = display_order + 1, updated_at = datetime('now')
			 WHERE project_id = ? AND category = ? AND display_order >= ? AND display_order < ?`,
			task.ProjectID, task.Category, newPosition, oldPosition)
	} else {
		// Moving down: decrement display_order of tasks between oldPosition and newPosition
		_, err = r.db.ExecContext(ctx,
			`UPDATE tasks
			 SET display_order = display_order - 1, updated_at = datetime('now')
			 WHERE project_id = ? AND category = ? AND display_order > ? AND display_order <= ?`,
			task.ProjectID, task.Category, oldPosition, newPosition)
	}

	if err != nil {
		return fmt.Errorf("updating task positions: %w", err)
	}

	// Update the task's position
	_, err = r.db.ExecContext(ctx,
		`UPDATE tasks SET display_order = ?, updated_at = datetime('now') WHERE id = ?`,
		newPosition, taskID)
	if err != nil {
		return fmt.Errorf("updating task display_order: %w", err)
	}

	return nil
}

// ListBacklogByPriority returns backlog tasks for a project, filtered by priority.
// If priority is 0, returns all backlog tasks. Otherwise returns tasks with that exact priority.
// Only returns tasks with status pending, failed, or cancelled (eligible for execution).
func (r *TaskRepo) ListBacklogByPriority(ctx context.Context, projectID string, priority int) ([]models.Task, error) {
	query := `SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE category = 'backlog' AND project_id = ? AND status IN ('pending', 'failed', 'cancelled', 'completed')`
	args := []any{projectID}

	if priority > 0 {
		query += ` AND priority = ?`
		args = append(args, priority)
	}

	query += ` ORDER BY priority DESC, display_order ASC, created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing backlog tasks by priority: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// CountBacklogByPriority returns a map of priority -> count for backlog tasks in a project.
// Counts tasks with status pending, failed, cancelled, or completed (all eligible for execution).
func (r *TaskRepo) CountBacklogByPriority(ctx context.Context, projectID string) (map[int]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT priority, COUNT(*) FROM tasks
		 WHERE category = 'backlog' AND project_id = ? AND status IN ('pending', 'failed', 'cancelled', 'completed')
		 GROUP BY priority ORDER BY priority DESC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("counting backlog tasks by priority: %w", err)
	}
	defer rows.Close()

	counts := make(map[int]int)
	for rows.Next() {
		var priority, count int
		if err := rows.Scan(&priority, &count); err != nil {
			return nil, fmt.Errorf("scanning count: %w", err)
		}
		counts[priority] = count
	}
	return counts, rows.Err()
}

// ListByTags returns tasks matching the specified tags and optional filters.
// Tags can be a single tag or multiple tags (returns tasks matching ANY of the tags).
// Optional filters: projectID (empty string = all projects), category (empty string = all categories),
// minPriority (0 = no filter), status (empty string = all statuses).
func (r *TaskRepo) ListByTags(ctx context.Context, tags []models.TaskTag, projectID string, category models.TaskCategory, minPriority int, status models.TaskStatus) ([]models.Task, error) {
	query := `SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE 1=1`
	args := []any{}

	// Build tag filter (IN clause) - only if tags are provided
	if len(tags) > 0 {
		tagPlaceholders := make([]string, len(tags))
		for i, tag := range tags {
			tagPlaceholders[i] = "?"
			args = append(args, tag)
		}
		query += fmt.Sprintf(` AND tag IN (%s)`, strings.Join(tagPlaceholders, ","))
	}

	// Apply optional filters
	if projectID != "" {
		query += ` AND project_id = ?`
		args = append(args, projectID)
	}

	if category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}

	if minPriority > 0 {
		query += ` AND priority >= ?`
		args = append(args, minPriority)
	}

	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}

	query += ` ORDER BY priority DESC, display_order ASC, created_at ASC`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tasks by tags: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// UpdateWorktreeInfo sets the worktree path and branch for a task.
func (r *TaskRepo) UpdateWorktreeInfo(ctx context.Context, id, worktreePath, branch string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET worktree_path = ?, worktree_branch = ?, updated_at = datetime('now') WHERE id = ?`,
		worktreePath, branch, id)
	if err != nil {
		return fmt.Errorf("updating worktree info: %w", err)
	}
	return nil
}

// UpdateMergeStatus sets the merge status for a task.
func (r *TaskRepo) UpdateMergeStatus(ctx context.Context, id string, status models.MergeStatus) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET merge_status = ?, updated_at = datetime('now') WHERE id = ?`,
		status, id)
	if err != nil {
		return fmt.Errorf("updating merge status: %w", err)
	}
	return nil
}

// UpdateAutoMerge sets the auto_merge flag and merge target branch for a task.
func (r *TaskRepo) UpdateAutoMerge(ctx context.Context, id string, autoMerge bool, targetBranch string) error {
	am := 0
	if autoMerge {
		am = 1
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET auto_merge = ?, merge_target_branch = ?, updated_at = datetime('now') WHERE id = ?`,
		am, targetBranch, id)
	if err != nil {
		return fmt.Errorf("updating auto merge: %w", err)
	}
	return nil
}

// ClearWorktreeInfo removes worktree path/branch from a task (after cleanup).
func (r *TaskRepo) ClearWorktreeInfo(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET worktree_path = '', worktree_branch = '', updated_at = datetime('now') WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("clearing worktree info: %w", err)
	}
	return nil
}

// ListWithWorktrees returns all tasks that have active worktrees.
func (r *TaskRepo) ListWithWorktrees(ctx context.Context) ([]models.Task, error) {
	query := `SELECT id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at
		 FROM tasks WHERE worktree_path != '' AND worktree_branch != ''
		 ORDER BY created_at ASC`

	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing tasks with worktrees: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return tasks, nil
}

// UpdateLineage sets the base_branch, base_commit_sha, and lineage_depth for a task.
func (r *TaskRepo) UpdateLineage(ctx context.Context, id, baseBranch, baseCommitSHA string, lineageDepth int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET base_branch = ?, base_commit_sha = ?, lineage_depth = ?, updated_at = datetime('now') WHERE id = ?`,
		baseBranch, baseCommitSHA, lineageDepth, id)
	if err != nil {
		return fmt.Errorf("updating lineage: %w", err)
	}
	return nil
}

// HasNonTerminalDescendants returns true if the task has any descendants (direct or transitive)
// that are not in a terminal state (completed/failed/cancelled).
func (r *TaskRepo) HasNonTerminalDescendants(ctx context.Context, parentID string) (bool, error) {
	// Recursive CTE to find all descendants
	var count int
	err := r.db.QueryRowContext(ctx,
		`WITH RECURSIVE descendants(id) AS (
			SELECT id FROM tasks WHERE parent_task_id = ?
			UNION ALL
			SELECT t.id FROM tasks t JOIN descendants d ON t.parent_task_id = d.id
		)
		SELECT COUNT(*) FROM tasks WHERE id IN (SELECT id FROM descendants) AND status NOT IN ('completed', 'failed', 'cancelled')`,
		parentID).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking non-terminal descendants: %w", err)
	}
	return count > 0, nil
}

// UpdateTelegramOrigin marks a task as created via Telegram and stores the chat ID
// for sending completion notifications back.
func (r *TaskRepo) UpdateTelegramOrigin(ctx context.Context, id string, chatID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET created_via = 'telegram', telegram_chat_id = ?, updated_at = datetime('now') WHERE id = ?`,
		chatID, id)
	if err != nil {
		return fmt.Errorf("updating telegram origin: %w", err)
	}
	return nil
}

// UpdateSlackOrigin marks a task as created via Slack.
func (r *TaskRepo) UpdateSlackOrigin(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE tasks SET created_via = 'slack', updated_at = datetime('now') WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("updating slack origin: %w", err)
	}
	return nil
}

func (r *TaskRepo) CountRunningByProject(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE project_id = ? AND status = 'running'`, projectID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting running tasks by project: %w", err)
	}
	return count, nil
}

func (r *TaskRepo) CountRunningTotal(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tasks WHERE status = 'running'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting total running tasks: %w", err)
	}
	return count, nil
}
