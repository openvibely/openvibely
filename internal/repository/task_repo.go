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

const taskSelectColumns = `id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, swarm_role, swarm_status, swarm_config, swarm_sequence, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at, completed_at`

// activeTaskAdmissionSelectColumns contains only the task fields needed by the
// scheduler to order, log, route, and enqueue active pending work. Full task
// payloads remain on the authoritative worker claim/detail path.
const activeTaskAdmissionSelectColumns = `id, project_id, title, category, priority, status, agent_id, agent_definition_id, parent_task_id, swarm_role`

const taskThreadRenderMetadataColumns = `id, project_id, category, status, agent_id, agent_definition_id`

const worktreeCleanupTaskSelectColumns = `id, project_id, status, worktree_path, worktree_branch, merge_target_branch, merge_status`

const swarmChildTaskSelectColumns = `id, project_id, title, category, priority, status, agent_id, agent_definition_id, tag, display_order, parent_task_id, swarm_role, swarm_status, swarm_config, swarm_sequence, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id, created_at, updated_at, completed_at`

const scheduleCalendarTaskSelectColumns = `t.id, t.project_id, t.title, t.category, t.status`

const taskSelectColumnsWithGoal = `t.id, t.project_id, t.title, t.category, t.priority, t.status, t.prompt, t.agent_id, t.agent_definition_id, t.tag, t.display_order, t.parent_task_id, t.chain_config, t.swarm_role, t.swarm_status, t.swarm_config, t.swarm_sequence, t.worktree_path, t.worktree_branch, t.auto_merge, t.merge_target_branch, t.merge_status, t.base_branch, t.base_commit_sha, t.lineage_depth, t.created_via, t.telegram_chat_id,
			EXISTS(SELECT 1 FROM task_goals g WHERE g.task_id = t.id AND g.status != 'cleared') AS has_goal,
			0 AS automation_capacity_queued,
			t.created_at, t.updated_at, t.completed_at`

const BoardPromptPreviewCodePoints = 300

var taskBoardSelectColumnsWithGoal = fmt.Sprintf(`t.id, t.project_id, t.title, t.category, t.priority, t.status, substr(t.prompt, 1, %d), t.agent_id, t.agent_definition_id, t.tag, t.display_order, t.parent_task_id, t.chain_config, t.swarm_role, t.swarm_status, t.swarm_config, t.swarm_sequence, t.worktree_path, t.worktree_branch, t.auto_merge, t.merge_target_branch, t.merge_status, t.base_branch, t.base_commit_sha, t.lineage_depth, t.created_via, t.telegram_chat_id,
			EXISTS(SELECT 1 FROM task_goals g WHERE g.task_id = t.id AND g.status != 'cleared') AS has_goal,
			EXISTS(SELECT 1 FROM automation_dispatch_outbox d
				JOIN automation_task_run_reservations r ON r.dispatch_id = d.id AND r.task_id = d.task_id
				WHERE d.task_id = t.id AND d.execution_id IS NULL AND d.status IN ('pending', 'processing', 'submitted')) AS automation_capacity_queued,
			t.created_at, t.updated_at, t.completed_at`, BoardPromptPreviewCodePoints)

type TaskRepo struct {
	db          *sql.DB
	broadcaster *events.Broadcaster
}

// ActiveTaskAdmission is the compact row returned by
// ListActivePendingAdmissions. It is an admission hint, not a complete task;
// callers that need prompt, workflow, worktree, or other execution detail must
// reload the task through GetByID.
type ActiveTaskAdmission struct {
	ID                string
	ProjectID         string
	Title             string
	Category          models.TaskCategory
	Priority          int
	Status            models.TaskStatus
	AgentID           *string
	AgentDefinitionID *string
	ParentTaskID      *string
	SwarmRole         models.SwarmRole
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

// HasPendingAutomationDispatch reports whether a task has an unfinished,
// execution-free Automation dispatch holding a durable run reservation.
func (r *TaskRepo) HasPendingAutomationDispatch(ctx context.Context, taskID string) (bool, error) {
	var queued bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM automation_dispatch_outbox d
		JOIN automation_task_run_reservations r ON r.dispatch_id = d.id AND r.task_id = d.task_id
		WHERE d.task_id = ? AND d.execution_id IS NULL AND d.status IN ('pending', 'processing', 'submitted')
	)`, taskID).Scan(&queued)
	return queued, err
}

func (r *TaskRepo) ListAutomationReusableTasks(ctx context.Context, projectID string, limit int) ([]models.Task, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, project_id, title, category, status
		FROM tasks WHERE project_id = ? AND category IN ('backlog','scheduled','active')
		ORDER BY title ASC, id ASC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing reusable automation tasks: %w", err)
	}
	defer rows.Close()
	var tasks []models.Task
	for rows.Next() {
		var task models.Task
		if err := rows.Scan(&task.ID, &task.ProjectID, &task.Title, &task.Category, &task.Status); err != nil {
			return nil, fmt.Errorf("scanning reusable automation task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) ListByProjectWithSort(ctx context.Context, projectID string, category string, sortBy string) ([]models.Task, error) {
	return r.ListByProjectWithCategorySorts(ctx, projectID, category, sortBy, "")
}

func (r *TaskRepo) ListByProjectWithCategorySorts(ctx context.Context, projectID string, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	return r.listByProjectWithCategorySorts(ctx, taskSelectColumnsWithGoal, projectID, category, backlogSort, completedSort)
}

// ListBoardByProjectWithCategorySorts returns the complete task metadata needed by
// Kanban cards while projecting Prompt to a bounded Unicode-safe preview.
func (r *TaskRepo) ListBoardByProjectWithCategorySorts(ctx context.Context, projectID string, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	return r.listByProjectWithCategorySorts(ctx, taskBoardSelectColumnsWithGoal, projectID, category, backlogSort, completedSort)
}

func (r *TaskRepo) listByProjectWithCategorySorts(ctx context.Context, selectColumns string, projectID string, category string, backlogSort string, completedSort string) ([]models.Task, error) {
	query := `SELECT ` + selectColumns + `
				 FROM tasks t WHERE t.project_id = ?`
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
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.HasGoal, &t.AutomationCapacityQueued, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
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
	case "completed_asc":
		aTime := completedSortTime(a)
		bTime := completedSortTime(b)
		if !aTime.Equal(bTime) {
			return aTime.Before(bTime)
		}
		return taskDisplayOrderAsc(a, b)
	case "completed_desc":
		aTime := completedSortTime(a)
		bTime := completedSortTime(b)
		if !aTime.Equal(bTime) {
			return aTime.After(bTime)
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

// completedSortTime returns CompletedAt if set, falling back to UpdatedAt.
// Tasks in the completed category always have CompletedAt set after migration 096.
func completedSortTime(t models.Task) time.Time {
	if t.CompletedAt != nil {
		return *t.CompletedAt
	}
	return t.UpdatedAt
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
	return r.getOne(ctx,
		`SELECT `+taskSelectColumns+`
			 FROM tasks WHERE id = ?`, id)
}

// GetThreadRenderMetadata returns the compact task fields needed by recurring
// task-thread polling. The returned Task is intentionally incomplete and must
// not be used for full detail/edit rendering or full-record persistence.
func (r *TaskRepo) GetThreadRenderMetadata(ctx context.Context, id string) (*models.Task, error) {
	var t models.Task
	err := r.db.QueryRowContext(ctx,
		`SELECT `+taskThreadRenderMetadataColumns+`
			 FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &t.ProjectID, &t.Category, &t.Status, &t.AgentID, &t.AgentDefinitionID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task thread render metadata: %w", err)
	}
	return &t, nil
}

// FilterNonChatTaskIDs returns the referenced task IDs that exist and are not
// internal Chat tasks. The result is a set so callers can preserve their own
// marker ordering, including duplicate markers, without hydrating full tasks.
func (r *TaskRepo) FilterNonChatTaskIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids)+1)
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	if len(placeholders) == 0 {
		return map[string]bool{}, nil
	}
	args = append(args, models.CategoryChat)
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM tasks WHERE id IN (`+strings.Join(placeholders, ",")+`) AND category != ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("filtering non-chat task ids: %w", err)
	}
	defer rows.Close()

	allowed := make(map[string]bool, len(placeholders))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning non-chat task id: %w", err)
		}
		allowed[id] = true
	}
	return allowed, rows.Err()
}

func (r *TaskRepo) GetByProjectAndTitle(ctx context.Context, projectID, title string) (*models.Task, error) {
	return r.getOne(ctx,
		`SELECT `+taskSelectColumns+`
		 FROM tasks WHERE project_id = ? AND title = ? LIMIT 1`, projectID, title)
}

func (r *TaskRepo) getOne(ctx context.Context, query string, args ...any) (*models.Task, error) {
	return getTaskWithExecutor(ctx, r.db, query, args...)
}

func getTaskWithExecutor(ctx context.Context, exec sqlExecutor, query string, args ...any) (*models.Task, error) {
	var t models.Task
	err := exec.QueryRowContext(ctx, query, args...).
		Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task: %w", err)
	}
	return &t, nil
}

func defaultJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

func (r *TaskRepo) Create(ctx context.Context, t *models.Task) error {
	return withImmediateTx(ctx, r.db, func(exec sqlExecutor) error {
		return r.createWithExecutor(ctx, exec, t)
	})
}

// CreateWithExecutor persists a task using the caller's transaction.
func (r *TaskRepo) CreateWithExecutor(ctx context.Context, exec SQLExecutor, t *models.Task) error {
	return r.createWithExecutor(ctx, exec, t)
}

func (r *TaskRepo) CreateWithGoal(ctx context.Context, t *models.Task, goal *models.TaskGoal) error {
	if goal == nil || strings.TrimSpace(goal.Objective) == "" {
		return r.Create(ctx, t)
	}
	return withImmediateTx(ctx, r.db, func(exec sqlExecutor) error {
		if err := r.createWithExecutor(ctx, exec, t); err != nil {
			return err
		}
		return createTaskGoalWithExecutor(ctx, exec, t.ID, goal)
	})
}

func createTaskGoalWithExecutor(ctx context.Context, exec sqlExecutor, taskID string, goal *models.TaskGoal) error {
	goal.TaskID = taskID
	if goal.GoalID == "" {
		goal.GoalID = NewID()
	}
	if goal.Status == "" {
		goal.Status = models.TaskGoalStatusActive
	}
	created, err := scanTaskGoal(exec.QueryRowContext(ctx, `
		INSERT INTO task_goals (task_id, goal_id, objective, status, reason, blocker_key, blocker_count, blocker_reason, blocker_last_seen_at, last_checked_at, achieved_at)
		VALUES (?, ?, ?, ?, ?, '', 0, '', NULL, NULL, NULL)
		RETURNING `+taskGoalSelectColumns, goal.TaskID, goal.GoalID, goal.Objective, goal.Status, goal.Reason))
	if err != nil {
		return fmt.Errorf("creating task goal: %w", err)
	}
	*goal = *created
	return nil
}

func setTaskGoalWithExecutor(ctx context.Context, exec sqlExecutor, taskID, objective, reason string) error {
	objective = strings.TrimSpace(objective)
	var existingObjective string
	err := exec.QueryRowContext(ctx, `SELECT objective FROM task_goals WHERE task_id = ?`, taskID).Scan(&existingObjective)
	if err == nil && existingObjective == objective {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("loading task goal: %w", err)
	}
	goal := &models.TaskGoal{GoalID: NewID(), Objective: objective, Status: models.TaskGoalStatusActive, Reason: reason}
	if err == sql.ErrNoRows {
		return createTaskGoalWithExecutor(ctx, exec, taskID, goal)
	}
	updated, err := scanTaskGoal(exec.QueryRowContext(ctx, `
		UPDATE task_goals
		SET goal_id = ?, objective = ?, status = 'active', reason = ?, blocker_key = '', blocker_count = 0, blocker_reason = '',
			blocker_last_seen_at = NULL, last_checked_at = NULL, achieved_at = NULL, updated_at = datetime('now')
		WHERE task_id = ?
		RETURNING `+taskGoalSelectColumns, goal.GoalID, goal.Objective, goal.Reason, taskID))
	if err != nil {
		return fmt.Errorf("updating task goal: %w", err)
	}
	*goal = *updated
	return nil
}

func (r *TaskRepo) createWithExecutor(ctx context.Context, exec sqlExecutor, t *models.Task) error {
	// Get the max display_order for this project and category, then add 1
	var maxOrder sql.NullInt64
	err := exec.QueryRowContext(ctx,
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
	err = exec.QueryRowContext(ctx,
		`INSERT INTO tasks (id, project_id, title, category, priority, status, prompt, agent_id, agent_definition_id, tag, display_order, parent_task_id, chain_config, swarm_role, swarm_status, swarm_config, swarm_sequence, worktree_path, worktree_branch, auto_merge, merge_target_branch, merge_status, base_branch, base_commit_sha, lineage_depth, created_via, telegram_chat_id)
			 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 RETURNING id, created_at, updated_at, completed_at`,
		t.ProjectID, t.Title, t.Category, t.Priority, t.Status, t.Prompt, t.AgentID, t.AgentDefinitionID, t.Tag, displayOrder, t.ParentTaskID, t.ChainConfig, t.SwarmRole, t.SwarmStatus, defaultJSON(t.SwarmConfig), t.SwarmSequence, t.WorktreePath, t.WorktreeBranch, autoMerge, t.MergeTargetBranch, t.MergeStatus, t.BaseBranch, t.BaseCommitSHA, t.LineageDepth, t.CreatedVia, t.TelegramChatID).
		Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt)
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
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET title = ?, category = ?, priority = ?, status = ?,
			 prompt = ?, agent_id = ?, agent_definition_id = ?, tag = ?, display_order = ?, parent_task_id = ?, chain_config = ?,
			 swarm_role = ?, swarm_status = ?, swarm_config = ?, swarm_sequence = ?, auto_merge = ?, merge_target_branch = ?, base_branch = ?, base_commit_sha = ?, lineage_depth = ?, updated_at = datetime('now')
			 WHERE id = ?`,
		t.Title, t.Category, t.Priority, t.Status, t.Prompt, t.AgentID, t.AgentDefinitionID, t.Tag, t.DisplayOrder, t.ParentTaskID, t.ChainConfig, t.SwarmRole, t.SwarmStatus, defaultJSON(t.SwarmConfig), t.SwarmSequence, autoMerge, t.MergeTargetBranch, t.BaseBranch, t.BaseCommitSHA, t.LineageDepth, t.ID)
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

	_, err = execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET category = ?, display_order = ?, updated_at = datetime('now'), completed_at = CASE WHEN ? = 'completed' THEN datetime('now') ELSE NULL END WHERE id = ?`,
		category, displayOrder, string(category), id)
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

	_, err = execBoundSQLite(ctx, r.db,
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

	result, err := execBoundSQLite(ctx, r.db,
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

// SetPendingIfNotRunningOrQueuedForEnabledSchedule sets status to pending only
// while the task is still eligible through an enabled, runnable schedule. The
// schedule predicate is evaluated in the same UPDATE as the task mutation so a
// pause that commits first prevents linked-task reactivation.
func (r *TaskRepo) SetPendingIfNotRunningOrQueuedForEnabledSchedule(ctx context.Context, id, scheduleID string) (bool, error) {
	task, err := r.GetByID(ctx, id)
	if err != nil {
		return false, fmt.Errorf("getting task before scheduled pending update: %w", err)
	}
	if task == nil {
		return false, fmt.Errorf("task not found: %s", id)
	}

	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET status = 'pending', updated_at = datetime('now')
			 WHERE id = ? AND status NOT IN ('running', 'queued')
			   AND EXISTS (
					SELECT 1 FROM schedules
					 WHERE schedules.id = ?
					   AND schedules.task_id = tasks.id
					   AND schedules.enabled = 1
					   AND (schedules.repeat_type <> ? OR schedules.next_run IS NOT NULL)
				)`,
		id, scheduleID, models.RepeatOnce)
	if err != nil {
		return false, fmt.Errorf("setting scheduled task pending with guard: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("scheduled pending update rows affected: %w", err)
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

const taskThreadInputOwnsAdmissionPredicate = `EXISTS (
	SELECT 1 FROM thread_inputs i
	WHERE i.scope = 'task_thread' AND i.task_id = tasks.id AND i.input_status = 'pending'
	  AND EXISTS (SELECT 1 FROM executions history WHERE history.task_id = tasks.id)
)`

// ClaimTask atomically sets status to running only if the task is currently pending.
// Returns true if the claim succeeded, false if the task was already running/completed/failed.
func (r *TaskRepo) ClaimTask(ctx context.Context, id string) (bool, error) {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return false, fmt.Errorf("beginning task claim: %w", err)
	}
	defer cleanup()

	task, err := getTaskWithExecutor(ctx, tx, `SELECT `+taskSelectColumns+` FROM tasks WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("getting task before claim: %w", err)
	}
	if task == nil {
		return false, fmt.Errorf("task not found: %s", id)
	}

	result, err := tx.ExecContext(ctx,
		`UPDATE tasks SET status = 'running', updated_at = datetime('now')
		 WHERE id = ? AND status = 'pending'
		   AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)
		   AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = tasks.id AND e.status = 'running')
		   AND NOT `+taskThreadInputOwnsAdmissionPredicate,
		id)
	if err != nil {
		return false, fmt.Errorf("claiming task: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("claiming task rows affected: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("committing task claim: %w", err)
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

// TaskDispatchClaim is the authoritative ordinary-worker dispatch state loaded
// in the same write transaction that promotes its Task to running.
type TaskDispatchClaim struct {
	Task              models.Task
	AutomationContext models.AutomationContext
}

// ClaimTaskForDispatch atomically validates worker admission, claims the Task,
// and loads its exact persisted behavior plus current Automation bindings. This
// prevents a concurrent Automation Save from mixing a stale queued snapshot
// with a replacement graph.
func (r *TaskRepo) ClaimTaskForDispatch(ctx context.Context, id string) (*TaskDispatchClaim, bool, error) {
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return nil, false, err
	}
	defer finishImmediate()

	task, err := getTaskWithExecutor(ctx, conn, `SELECT `+taskSelectColumns+` FROM tasks WHERE id = ?`, id)
	if err != nil {
		return nil, false, fmt.Errorf("loading task for dispatch claim: %w", err)
	}
	if task == nil {
		return nil, false, fmt.Errorf("task not found: %s", id)
	}
	if task.Status != models.StatusPending || (task.Category != models.CategoryActive && task.Category != models.CategoryScheduled) {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, false, err
		}
		return &TaskDispatchClaim{Task: *task}, false, nil
	}

	automationContext, err := contextForTaskWithExecutor(ctx, conn, task.ProjectID, task.ID)
	if err != nil {
		return nil, false, fmt.Errorf("loading task Automation context for dispatch claim: %w", err)
	}
	if IsAutomationTaskCreatedVia(task.CreatedVia) {
		automationContext.ProjectID = task.ProjectID
		automationContext.OriginTask = true
	}
	checkedBindings := map[string]bool{}
	for _, binding := range automationContext.Bindings {
		bindingKey := binding.AutomationID + "\x00" + binding.VersionID
		if checkedBindings[bindingKey] {
			continue
		}
		checkedBindings[bindingKey] = true
		var lifecycle models.AutomationLifecycleState
		err := conn.QueryRowContext(ctx, `SELECT lifecycle_state FROM automations
			WHERE project_id = ? AND id = ? AND published_version_id = ?`, task.ProjectID, binding.AutomationID, binding.VersionID).Scan(&lifecycle)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("loading Automation lifecycle for dispatch claim: %w", err)
		}
		if lifecycle == models.AutomationActive {
			continue
		}
		if lifecycle == models.AutomationPaused && task.Category == models.CategoryActive {
			if _, err := conn.ExecContext(ctx, `INSERT INTO automation_paused_task_admissions
				(task_id, project_id, automation_id, version_id)
				SELECT ?, ?, ?, ? WHERE EXISTS (
					SELECT 1 FROM automation_activity_resources resource
					JOIN automation_activities activity ON activity.id = resource.activity_id
					WHERE resource.resource_type = 'task' AND resource.resource_id = ? AND resource.relation = 'child'
						AND activity.project_id = ? AND activity.automation_id = ? AND activity.version_id = ?
						AND activity.activity_type = 'create_task')
				ON CONFLICT(task_id) DO NOTHING`, task.ID, task.ProjectID, binding.AutomationID, binding.VersionID,
				task.ID, task.ProjectID, binding.AutomationID, binding.VersionID); err != nil {
				return nil, false, fmt.Errorf("preserving paused Automation task admission: %w", err)
			}
		} else if lifecycle == models.AutomationArchived {
			if _, err := conn.ExecContext(ctx, `DELETE FROM automation_paused_task_admissions WHERE task_id = ?`, task.ID); err != nil {
				return nil, false, fmt.Errorf("removing archived Automation task admission: %w", err)
			}
		}
		if _, err := conn.ExecContext(ctx, `UPDATE tasks SET category = 'backlog', updated_at = datetime('now')
			WHERE id = ? AND project_id = ? AND status = 'pending' AND category IN ('active','scheduled')`, task.ID, task.ProjectID); err != nil {
			return nil, false, fmt.Errorf("demoting inactive Automation task before dispatch: %w", err)
		}
		task.Category = models.CategoryBacklog
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, false, err
		}
		return &TaskDispatchClaim{Task: *task, AutomationContext: automationContext}, false, nil
	}
	result, err := conn.ExecContext(ctx, `UPDATE tasks SET status = 'running', updated_at = datetime('now')
		WHERE id = ? AND status = 'pending' AND category IN ('active','scheduled')
		  AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)
		  AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = tasks.id AND e.status = 'running')
		  AND NOT `+taskThreadInputOwnsAdmissionPredicate, id)
	if err != nil {
		return nil, false, fmt.Errorf("claiming task for dispatch: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("dispatch claim rows affected: %w", err)
	}
	if rows != 1 {
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, false, err
		}
		return &TaskDispatchClaim{Task: *task}, false, nil
	}
	task.Status = models.StatusRunning
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, false, err
	}

	if r.broadcaster != nil {
		r.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, TaskID: task.ID, TaskName: task.Title,
			ProjectID: task.ProjectID, Status: string(models.StatusRunning), OldStatus: string(models.StatusPending),
			Category: string(task.Category)})
	}
	return &TaskDispatchClaim{Task: *task, AutomationContext: automationContext}, true, nil
}

// SearchByTitle searches for non-chat tasks matching a title substring within a project.
// Returns tasks ordered by relevance: exact match first, then prefix match, then contains.
// Excludes chat tasks (CategoryChat) since those are internal chat messages, not user tasks.
func (r *TaskRepo) SearchByTitle(ctx context.Context, projectID string, titleQuery string) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+taskSelectColumns+`
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
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// TaskDiscoveryFilter bounds a read-only, current-project task discovery query.
type TaskDiscoveryFilter struct {
	// Query is an optional partial (case-insensitive substring) title match.
	Query string
	// Category optionally restricts results to a single non-chat category.
	Category string
	// Status optionally restricts results to a single task status.
	Status string
	// Limit caps the number of returned rows. Callers should clamp before use.
	Limit int
	// Offset skips the first N rows for pagination.
	Offset int
}

// taskDiscoveryRow is the private compact projection used by list_tasks. Keep it
// aligned with the response fields in service.taskDiscoverySummary.
type taskDiscoveryRow struct {
	ID           string
	Title        string
	Category     models.TaskCategory
	Status       models.TaskStatus
	Priority     int
	UpdatedAt    time.Time
	ParentTaskID sql.NullString
	SwarmRole    models.SwarmRole
}

const taskDiscoverySelectColumns = `id, title, category, status, priority, updated_at, parent_task_id, swarm_role`

// swarmInspectionSelectColumns is the compact projection used by read-only Chat
// swarm inspection. It intentionally omits prompt, execution output, chain_config,
// swarm_config, and local worktree paths.
const swarmInspectionSelectColumns = `id, project_id, title, category, priority, status, updated_at, parent_task_id, swarm_role, swarm_status, swarm_sequence, worktree_branch, merge_status`

// ListTasksForDiscovery returns a bounded, deterministic page of non-chat tasks for
// a single project, plus the total number of matching rows for pagination. It never
// crosses project boundaries and always excludes internal chat rows (CategoryChat).
//
// Ordering is deterministic: when a title query is supplied, results are ranked by
// relevance (exact, then prefix, then contains) and then by updated_at DESC with a
// task id tiebreaker; without a query, results are ordered by updated_at DESC, id ASC.
func (r *TaskRepo) ListTasksForDiscovery(ctx context.Context, projectID string, filter TaskDiscoveryFilter) ([]models.Task, int, error) {
	where := `project_id = ? AND category != 'chat'`
	args := []any{projectID}

	query := strings.TrimSpace(filter.Query)
	if query != "" {
		where += ` AND title LIKE ?`
		args = append(args, "%"+query+"%")
	}
	if cat := strings.TrimSpace(filter.Category); cat != "" {
		where += ` AND category = ?`
		args = append(args, cat)
	}
	if status := strings.TrimSpace(filter.Status); status != "" {
		where += ` AND status = ?`
		args = append(args, status)
	}

	var total int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("counting tasks for discovery: %w", err)
	}

	orderClause := ` ORDER BY updated_at DESC, id ASC`
	selectArgs := append([]any{}, args...)
	if query != "" {
		orderClause = ` ORDER BY
			   CASE WHEN LOWER(title) = LOWER(?) THEN 0
			        WHEN LOWER(title) LIKE LOWER(? || '%') THEN 1
			        ELSE 2 END,
			   updated_at DESC, id ASC`
		selectArgs = append(selectArgs, query, query)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 20
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}
	selectArgs = append(selectArgs, limit, offset)

	rows, err := r.db.QueryContext(ctx,
		`SELECT `+taskDiscoverySelectColumns+`
		 FROM tasks WHERE `+where+orderClause+`
		 LIMIT ? OFFSET ?`,
		selectArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("listing tasks for discovery: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var row taskDiscoveryRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Category, &row.Status, &row.Priority, &row.UpdatedAt, &row.ParentTaskID, &row.SwarmRole); err != nil {
			return nil, 0, fmt.Errorf("scanning task discovery row: %w", err)
		}
		task := models.Task{
			ID:        row.ID,
			Title:     row.Title,
			Category:  row.Category,
			Status:    row.Status,
			Priority:  row.Priority,
			UpdatedAt: row.UpdatedAt,
			SwarmRole: row.SwarmRole,
		}
		if row.ParentTaskID.Valid {
			parentTaskID := row.ParentTaskID.String
			task.ParentTaskID = &parentTaskID
		}
		tasks = append(tasks, task)
	}
	return tasks, total, rows.Err()
}

// GetTaskForSwarmInspection loads one non-chat task by ID or exact title using a
// compact projection scoped to the supplied project. Exactly one selector must be
// supplied by callers.
func (r *TaskRepo) GetTaskForSwarmInspection(ctx context.Context, projectID, taskID, title string) (*models.Task, error) {
	projectID = strings.TrimSpace(projectID)
	taskID = strings.TrimSpace(taskID)
	title = strings.TrimSpace(title)
	if projectID == "" {
		return nil, fmt.Errorf("getting swarm inspection task: project id is required")
	}
	where := `project_id = ? AND category != 'chat'`
	args := []any{projectID}
	if taskID != "" {
		where += ` AND id = ?`
		args = append(args, taskID)
	} else if title != "" {
		where += ` AND title = ?`
		args = append(args, title)
	} else {
		return nil, fmt.Errorf("getting swarm inspection task: task id or title is required")
	}
	return r.getOneSwarmInspectionTask(ctx, `SELECT `+swarmInspectionSelectColumns+` FROM tasks WHERE `+where+` LIMIT 1`, args...)
}

// ListSwarmChildrenForInspection returns compact, ordered child summaries for a
// swarm parent without selecting prompt, execution output, or config payloads.
func (r *TaskRepo) ListSwarmChildrenForInspection(ctx context.Context, projectID, parentTaskID string) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+swarmInspectionSelectColumns+`
		 FROM tasks
		 WHERE project_id = ? AND parent_task_id = ? AND category != 'chat'
		   AND swarm_role IN ('planner','worker','reviewer','merger','integrator')
		 ORDER BY swarm_sequence ASC,
		 CASE swarm_role WHEN 'planner' THEN 0 WHEN 'worker' THEN 1 WHEN 'reviewer' THEN 2 WHEN 'merger' THEN 3 WHEN 'integrator' THEN 3 ELSE 9 END,
		 created_at ASC, id ASC`, strings.TrimSpace(projectID), strings.TrimSpace(parentTaskID))
	if err != nil {
		return nil, fmt.Errorf("listing swarm children for inspection: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		task, err := scanSwarmInspectionTask(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning swarm inspection child: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TaskRepo) getOneSwarmInspectionTask(ctx context.Context, query string, args ...any) (*models.Task, error) {
	t, err := scanSwarmInspectionTask(r.db.QueryRowContext(ctx, query, args...).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting swarm inspection task: %w", err)
	}
	return &t, nil
}

func scanSwarmInspectionTask(scan func(dest ...any) error) (models.Task, error) {
	var t models.Task
	var parentTaskID sql.NullString
	err := scan(&t.ID, &t.ProjectID, &t.Title, &t.Category, &t.Priority, &t.Status, &t.UpdatedAt, &parentTaskID, &t.SwarmRole, &t.SwarmStatus, &t.SwarmSequence, &t.WorktreeBranch, &t.MergeStatus)
	if parentTaskID.Valid {
		value := parentTaskID.String
		t.ParentTaskID = &value
	}
	return t, err
}

type TaskDeletionManifest struct {
	TaskAttachmentPaths      []string
	ExecutionAttachmentPaths []string
	PendingUploadSessionIDs  []string
	SwarmChildTaskIDs        []string
}

func (r *TaskRepo) DeleteWithCleanupManifest(ctx context.Context, id string, beforeDelete func(TaskDeletionManifest) error) (TaskDeletionManifest, bool, error) {
	return r.deleteWithCleanupManifest(ctx, id, "", "", beforeDelete)
}

func (r *TaskRepo) DeleteWithCleanupManifestIfCategory(ctx context.Context, id, projectID string, category models.TaskCategory, beforeDelete func(TaskDeletionManifest) error) (TaskDeletionManifest, bool, error) {
	return r.deleteWithCleanupManifest(ctx, id, projectID, string(category), beforeDelete)
}

func (r *TaskRepo) deleteWithCleanupManifest(ctx context.Context, id, projectID, category string, beforeDelete func(TaskDeletionManifest) error) (manifest TaskDeletionManifest, deleted bool, err error) {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return manifest, false, fmt.Errorf("beginning task deletion: %w", err)
	}
	defer cleanup()

	where := `id = ?`
	args := []any{id}
	if projectID != "" || category != "" {
		where += ` AND project_id = ? AND category = ?`
		args = append(args, projectID, category)
	}
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE `+where, args...).Scan(&exists); err != nil {
		return manifest, false, fmt.Errorf("checking task for deletion: %w", err)
	}
	if exists == 0 {
		if err = tx.Commit(); err != nil {
			return manifest, false, fmt.Errorf("committing empty task deletion: %w", err)
		}
		return manifest, false, nil
	}

	readPaths := func(query string, destination *[]string, queryArgs ...any) error {
		rows, queryErr := tx.QueryContext(ctx, query, queryArgs...)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if scanErr := rows.Scan(&value); scanErr != nil {
				return scanErr
			}
			*destination = append(*destination, value)
		}
		return rows.Err()
	}
	if err = readPaths(`
		SELECT id
		FROM tasks
		WHERE parent_task_id = ?
		  AND swarm_role IN ('planner','worker','reviewer','merger','integrator')
		  AND EXISTS (
			SELECT 1 FROM tasks parent
			WHERE parent.id = ? AND parent.swarm_role = 'swarm_parent'
		  )
		ORDER BY swarm_sequence, created_at, id`, &manifest.SwarmChildTaskIDs, id, id); err != nil {
		return manifest, false, fmt.Errorf("listing swarm children for task deletion: %w", err)
	}
	if err = readPaths(`
		SELECT ta.file_path
		FROM task_attachments ta
		WHERE ta.task_id = ?
		  AND NOT EXISTS (
			SELECT 1 FROM task_attachments other
			WHERE other.file_path = ta.file_path AND other.task_id <> ta.task_id
		  )`, &manifest.TaskAttachmentPaths, id); err != nil {
		return manifest, false, fmt.Errorf("listing task attachment paths for deletion: %w", err)
	}
	if err = readPaths(`
		SELECT ca.file_path
		FROM chat_attachments ca
		JOIN executions e ON e.id = ca.execution_id
		WHERE e.task_id = ?
		  AND NOT EXISTS (
			SELECT 1
			FROM chat_attachments other_ca
			JOIN executions other_e ON other_e.id = other_ca.execution_id
			WHERE other_ca.file_path = ca.file_path AND other_e.task_id <> e.task_id
		  )`, &manifest.ExecutionAttachmentPaths, id); err != nil {
		return manifest, false, fmt.Errorf("listing execution attachment paths for deletion: %w", err)
	}
	if err = readPaths(`
		WITH owned_sessions(attachment_session_id) AS (
			SELECT ti.attachment_session_id
			FROM thread_inputs ti
			WHERE ti.task_id = ?
			  AND ti.attachment_session_id IS NOT NULL AND ti.attachment_session_id <> ''
			UNION
			SELECT ti.attachment_session_id
			FROM executions owner_execution
			CROSS JOIN thread_inputs ti INDEXED BY idx_thread_inputs_steering_turn
				ON ti.run_execution_id = owner_execution.id
			WHERE owner_execution.task_id = ? AND ti.task_id IS NULL
			  AND ti.attachment_session_id IS NOT NULL AND ti.attachment_session_id <> ''
		)
		SELECT owned.attachment_session_id
		FROM owned_sessions owned
		WHERE NOT EXISTS (
			SELECT 1 FROM thread_inputs other
			WHERE other.attachment_session_id IS NOT NULL AND other.attachment_session_id <> ''
			  AND other.attachment_session_id = owned.attachment_session_id
			  AND (
				(other.task_id IS NOT NULL AND other.task_id <> ?)
				OR (
					other.task_id IS NULL
					AND NOT EXISTS (
						SELECT 1 FROM executions other_execution
						WHERE other_execution.id = other.run_execution_id AND other_execution.task_id = ?
					)
				)
			  )
		  )`, &manifest.PendingUploadSessionIDs, id, id, id, id); err != nil {
		return manifest, false, fmt.Errorf("listing pending upload sessions for deletion: %w", err)
	}
	for _, sessionID := range manifest.PendingUploadSessionIDs {
		if !isTaskDeletionUploadSessionID(sessionID) {
			return manifest, false, fmt.Errorf("invalid pending attachment session for task deletion: %q", sessionID)
		}
	}

	// Establish the durable cleanup boundary while the deletion transaction owns
	// the database connection. The migration triggers reject any later thread
	// input that attempts to acquire one of these sessions after commit.
	for _, sessionID := range manifest.PendingUploadSessionIDs {
		if _, err = tx.ExecContext(ctx, `INSERT INTO retired_attachment_sessions(session_id) VALUES (?)`, sessionID); err != nil {
			return manifest, false, fmt.Errorf("retiring pending attachment session for task deletion: %w", err)
		}
	}

	if beforeDelete != nil {
		if err = beforeDelete(manifest); err != nil {
			return manifest, false, err
		}
	}
	if len(manifest.SwarmChildTaskIDs) > 0 {
		if _, err = tx.ExecContext(ctx, `
			UPDATE tasks
			SET category = 'completed',
				status = CASE
					WHEN status IN ('pending','queued','running','blocked') THEN 'cancelled'
					ELSE status
				END,
				swarm_status = CASE
					WHEN status IN ('pending','queued','running','blocked') THEN 'cancelled'
					ELSE swarm_status
				END,
				updated_at = datetime('now')
			WHERE parent_task_id = ?
			  AND swarm_role IN ('planner','worker','reviewer','merger','integrator')`, id); err != nil {
			return manifest, false, fmt.Errorf("terminalizing swarm children for task deletion: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM tasks WHERE `+where, args...)
	if err != nil {
		return manifest, false, fmt.Errorf("deleting task: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return manifest, false, fmt.Errorf("getting deleted task count: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return manifest, false, fmt.Errorf("committing task deletion: %w", err)
	}
	return manifest, rows > 0, nil
}

func isTaskDeletionUploadSessionID(sessionID string) bool {
	if len(sessionID) != 32 {
		return false
	}
	for _, ch := range sessionID {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func (r *TaskRepo) Delete(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM tasks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting task: %w", err)
	}
	return nil
}

// FindBlockedChildByParent finds a blocked child task for the given parent task ID.
// Returns nil, nil if no blocked child exists.
func (r *TaskRepo) FindBlockedChildByParent(ctx context.Context, parentTaskID string) (*models.Task, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+taskSelectColumns+`
		 FROM tasks WHERE parent_task_id = ? AND status = ?
		 LIMIT 1`,
		parentTaskID, models.StatusBlocked)

	var t models.Task
	if err := row.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
		&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("finding blocked child: %w", err)
	}
	return &t, nil
}

// DeleteBlockedChildrenByParent removes all blocked child tasks for the given parent task ID.
func (r *TaskRepo) DeleteBlockedChildrenByParent(ctx context.Context, parentTaskID string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`DELETE FROM tasks WHERE parent_task_id = ? AND status = ?`,
		parentTaskID, models.StatusBlocked)
	if err != nil {
		return fmt.Errorf("deleting blocked children: %w", err)
	}
	return nil
}

// ListSwarmChildren returns the child metadata needed for swarm orchestration and
// rendering. It intentionally omits Prompt and ChainConfig so repeated status
// checks do not materialize full child instructions or chaining payloads.
func (r *TaskRepo) ListSwarmChildren(ctx context.Context, parentTaskID string) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+swarmChildTaskSelectColumns+`
				 FROM tasks WHERE parent_task_id = ? AND swarm_role IN ('planner','worker','reviewer','merger','integrator')
				 ORDER BY swarm_sequence ASC,
				 CASE swarm_role WHEN 'planner' THEN 0 WHEN 'worker' THEN 1 WHEN 'reviewer' THEN 2 WHEN 'merger' THEN 3 WHEN 'integrator' THEN 3 ELSE 9 END,
				 created_at ASC`, parentTaskID)
	if err != nil {
		return nil, fmt.Errorf("listing swarm children: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		t, err := scanSwarmChildTask(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning swarm child: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// FindSwarmChildByRole returns the child metadata needed for swarm role
// orchestration. Use GetByID when callers need full Prompt or ChainConfig data.
func (r *TaskRepo) FindSwarmChildByRole(ctx context.Context, parentTaskID string, role models.SwarmRole) (*models.Task, error) {
	if role == models.SwarmRoleMerger {
		return r.getOneSwarmChild(ctx,
			`SELECT `+swarmChildTaskSelectColumns+`
				 FROM tasks WHERE parent_task_id = ? AND swarm_role IN ('merger','integrator')
				 ORDER BY swarm_sequence ASC, CASE swarm_role WHEN 'merger' THEN 0 WHEN 'integrator' THEN 1 ELSE 9 END, created_at ASC LIMIT 1`, parentTaskID)
	}
	return r.getOneSwarmChild(ctx,
		`SELECT `+swarmChildTaskSelectColumns+`
				 FROM tasks WHERE parent_task_id = ? AND swarm_role = ?
				 ORDER BY swarm_sequence ASC, created_at ASC LIMIT 1`, parentTaskID, role)
}

func (r *TaskRepo) getOneSwarmChild(ctx context.Context, query string, args ...any) (*models.Task, error) {
	t, err := scanSwarmChildTask(r.db.QueryRowContext(ctx, query, args...).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting swarm child: %w", err)
	}
	return &t, nil
}

func scanSwarmChildTask(scan func(dest ...any) error) (models.Task, error) {
	var t models.Task
	err := scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
		&t.Priority, &t.Status, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt)
	return t, err
}

func (r *TaskRepo) UpdateSwarmFields(ctx context.Context, id string, role models.SwarmRole, status, config string, sequence int) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET swarm_role = ?, swarm_status = ?, swarm_config = ?, swarm_sequence = ?, updated_at = datetime('now') WHERE id = ?`,
		role, status, defaultJSON(config), sequence, id)
	if err != nil {
		return fmt.Errorf("updating swarm fields: %w", err)
	}
	return nil
}

// ListActivePendingAdmissions returns the compact scheduler admission rows for
// active pending tasks. Keep these eligibility predicates in sync with the
// authoritative dispatch guards: reservations, queued/running executions, and
// pending task-thread inputs own admission and must remain excluded.
func (r *TaskRepo) ListActivePendingAdmissions(ctx context.Context) ([]ActiveTaskAdmission, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+activeTaskAdmissionSelectColumns+`
			 FROM tasks WHERE category = 'active' AND status = 'pending'
			 AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)
			 AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = tasks.id AND e.status IN ('queued', 'running'))
			 AND NOT `+taskThreadInputOwnsAdmissionPredicate+`
			 ORDER BY priority DESC, display_order ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing active pending task admissions: %w", err)
	}
	defer rows.Close()

	var admissions []ActiveTaskAdmission
	for rows.Next() {
		var admission ActiveTaskAdmission
		if err := rows.Scan(&admission.ID, &admission.ProjectID, &admission.Title,
			&admission.Category, &admission.Priority, &admission.Status, &admission.AgentID,
			&admission.AgentDefinitionID, &admission.ParentTaskID, &admission.SwarmRole); err != nil {
			return nil, fmt.Errorf("scanning active pending task admission: %w", err)
		}
		admissions = append(admissions, admission)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return admissions, nil
}

func (r *TaskRepo) ListActivePending(ctx context.Context) ([]models.Task, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+taskSelectColumns+`
		 FROM tasks WHERE category = 'active' AND status = 'pending'
		 AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)
		 AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = tasks.id AND e.status IN ('queued', 'running'))
		 AND NOT `+taskThreadInputOwnsAdmissionPredicate+`
		 ORDER BY priority DESC, display_order ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing active pending tasks: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
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
		`SELECT `+taskSelectColumns+`
		 FROM tasks WHERE category = 'active' AND status = 'queued' AND updated_at < ?
		 AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)
		 AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = tasks.id AND e.status = 'running')
		 AND NOT EXISTS (SELECT 1 FROM thread_inputs i
		                 WHERE i.scope = 'task_thread' AND i.task_id = tasks.id AND i.input_status = 'pending')
		 ORDER BY priority DESC, display_order ASC, created_at ASC`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("listing stale queued tasks: %w", err)
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var t models.Task
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Title, &t.Category,
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// ReclaimStaleQueuedTask atomically returns an ownerless stale queued task to
// pending. The ownership guards close the window between stale-task listing and
// recovery so a newly admitted follow-up or reservation cannot be overwritten.
func (r *TaskRepo) ReclaimStaleQueuedTask(ctx context.Context, id string, staleDuration time.Duration) (bool, error) {
	cutoff := time.Now().UTC().Add(-staleDuration).Format("2006-01-02 15:04:05")
	result, err := execBoundSQLite(ctx, r.db, `UPDATE tasks
		SET status = 'pending', updated_at = datetime('now')
		WHERE id = ? AND category = 'active' AND status = 'queued' AND updated_at < ?
		  AND NOT EXISTS (SELECT 1 FROM automation_task_run_reservations r WHERE r.task_id = tasks.id)
		  AND NOT EXISTS (SELECT 1 FROM executions e WHERE e.task_id = tasks.id AND e.status IN ('queued', 'running'))
		  AND NOT EXISTS (SELECT 1 FROM thread_inputs i
		                  WHERE i.scope = 'task_thread' AND i.task_id = tasks.id AND i.input_status = 'pending')`, id, cutoff)
	if err != nil {
		return false, fmt.Errorf("reclaiming stale queued task: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking stale queued task reclaim: %w", err)
	}
	return changed == 1, nil
}

// TaskWithSchedule represents a task with its schedule information for calendar view
type TaskWithSchedule struct {
	Task                   models.Task
	Schedule               *models.Schedule
	AutomationScheduleName string
}

func (r *TaskRepo) ListWithSchedulesByProject(ctx context.Context, projectID string) ([]TaskWithSchedule, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+scheduleCalendarTaskSelectColumns+`,
			 s.id, s.task_id, s.run_at, s.repeat_type, s.repeat_interval, s.enabled, s.clear_context_on_start, s.next_run, s.last_run, s.created_at, s.updated_at,
			 COALESCE(automation_node.name, '')
			 FROM tasks t
			 LEFT JOIN schedules s ON t.id = s.task_id
			 LEFT JOIN automation_trigger_owners automation_owner ON automation_owner.schedule_id = s.id AND automation_owner.project_id = t.project_id
			 LEFT JOIN automation_nodes automation_node ON automation_node.id = automation_owner.node_id
				AND automation_node.version_id = automation_owner.version_id
				AND automation_node.automation_id = automation_owner.automation_id
				AND automation_node.project_id = automation_owner.project_id
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
		var schedRepeatType, schedRepeatInterval, schedEnabled, schedClearContext sql.NullString
		var schedNextRun, schedLastRun sql.NullTime

		if err := rows.Scan(
			&tws.Task.ID, &tws.Task.ProjectID, &tws.Task.Title, &tws.Task.Category, &tws.Task.Status,
			&schedID, &schedTaskID, &schedRunAt, &schedRepeatType, &schedRepeatInterval, &schedEnabled, &schedClearContext, &schedNextRun, &schedLastRun, &schedCreatedAt, &schedUpdatedAt,
			&tws.AutomationScheduleName,
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
				ID:                  schedID.String,
				TaskID:              schedTaskID.String,
				RunAt:               schedRunAt.Time,
				RepeatType:          models.RepeatType(schedRepeatType.String),
				RepeatInterval:      repeatInterval,
				Enabled:             enabled,
				ClearContextOnStart: schedClearContext.String == "1" || schedClearContext.String == "true",
				CreatedAt:           schedCreatedAt.Time,
				UpdatedAt:           schedUpdatedAt.Time,
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

// CountPendingByProject returns the number of active tasks waiting for worker
// execution for each project. This includes ordinary active pending tasks and
// active queued tasks that are blocked behind task-thread FIFO/capacity.
func (r *TaskRepo) CountPendingByProject(ctx context.Context) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT project_id, COUNT(*) FROM tasks WHERE category = 'active' AND status IN ('pending', 'queued') GROUP BY project_id`)
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

// ResetOrphanedRunning reclaims tasks whose process-local runner was lost.
// Tasks with durable queued follow-ups remain queue-owned (failed/backlog) until
// FIFO promotion; ownerless ordinary tasks return to pending for scheduler retry.
func (r *TaskRepo) ResetOrphanedRunning(ctx context.Context) (int, error) {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks
		 SET status = CASE
		       WHEN EXISTS (SELECT 1 FROM thread_inputs i
		                    WHERE i.scope = 'task_thread' AND i.task_id = tasks.id AND i.input_status = 'pending')
		       THEN 'failed' ELSE 'pending' END,
		     category = CASE
		       WHEN category != 'chat' AND EXISTS (SELECT 1 FROM thread_inputs i
		                    WHERE i.scope = 'task_thread' AND i.task_id = tasks.id AND i.input_status = 'pending')
		       THEN 'backlog' ELSE category END,
		     updated_at = datetime('now')
		 WHERE status = 'running'
		   AND NOT EXISTS (
		     SELECT 1 FROM automation_task_run_reservations r
		     JOIN automation_dispatch_outbox d ON d.id = r.dispatch_id
		     JOIN executions e ON e.dispatch_id = d.id AND e.task_id = tasks.id
		     WHERE r.task_id = tasks.id AND d.status IN ('processing','submitted') AND e.status = 'running'
		   )`)
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
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET category = 'completed', updated_at = datetime('now'), completed_at = datetime('now')
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
	query := `SELECT ` + taskSelectColumns + `
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
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// DeleteAllCompleted deletes all tasks in the 'completed' category.
// Returns the number of tasks deleted.
func (r *TaskRepo) DeleteAllCompleted(ctx context.Context, projectID string) (int, error) {
	result, err := execBoundSQLite(ctx, r.db,
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
	result, err := execBoundSQLite(ctx, r.db,
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
	result, err := execBoundSQLite(ctx, r.db,
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
		`SELECT id FROM tasks WHERE category = 'chat' AND project_id = ? AND status IN ('pending', 'queued', 'running')`, projectID)
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
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET category = 'active', status = 'pending'
		 WHERE category = 'backlog'
		   AND project_id = ?
		   AND (status != 'blocked' OR swarm_role = 'swarm_parent')`, projectID)
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
		_, err = execBoundSQLite(ctx, r.db,
			`UPDATE tasks
			 SET display_order = display_order + 1, updated_at = datetime('now')
			 WHERE project_id = ? AND category = ? AND display_order >= ? AND display_order < ?`,
			task.ProjectID, task.Category, newPosition, oldPosition)
	} else {
		// Moving down: decrement display_order of tasks between oldPosition and newPosition
		_, err = execBoundSQLite(ctx, r.db,
			`UPDATE tasks
			 SET display_order = display_order - 1, updated_at = datetime('now')
			 WHERE project_id = ? AND category = ? AND display_order > ? AND display_order <= ?`,
			task.ProjectID, task.Category, oldPosition, newPosition)
	}

	if err != nil {
		return fmt.Errorf("updating task positions: %w", err)
	}

	// Update the task's position
	_, err = execBoundSQLite(ctx, r.db,
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
	query := `SELECT ` + taskSelectColumns + `
		 FROM tasks WHERE category = 'backlog' AND project_id = ? AND (status IN ('pending', 'failed', 'cancelled', 'completed') OR (status = 'blocked' AND swarm_role = 'swarm_parent'))`
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
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
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
		 WHERE category = 'backlog' AND project_id = ? AND (status IN ('pending', 'failed', 'cancelled', 'completed') OR (status = 'blocked' AND swarm_role = 'swarm_parent'))
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
	query := `SELECT ` + taskSelectColumns + `
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
			&t.Priority, &t.Status, &t.Prompt, &t.AgentID, &t.AgentDefinitionID, &t.Tag, &t.DisplayOrder, &t.ParentTaskID, &t.ChainConfig, &t.SwarmRole, &t.SwarmStatus, &t.SwarmConfig, &t.SwarmSequence, &t.WorktreePath, &t.WorktreeBranch, &t.AutoMerge, &t.MergeTargetBranch, &t.MergeStatus, &t.BaseBranch, &t.BaseCommitSHA, &t.LineageDepth, &t.CreatedVia, &t.TelegramChatID, &t.CreatedAt, &t.UpdatedAt, &t.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// UpdateAgentID persists the task's assigned model config ID. Passing an empty
// agentID clears the assignment (falls back to project/global default resolution).
// This is used to persist an explicit task-thread composer model selection so it
// remains the task's ongoing assigned model for future sends, per Task.AgentID
// task-assignment semantics.
func (r *TaskRepo) UpdateAgentID(ctx context.Context, id, agentID string) error {
	var value *string
	if agentID != "" {
		value = &agentID
	}
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET agent_id = ?, updated_at = datetime('now') WHERE id = ?`,
		value, id)
	if err != nil {
		return fmt.Errorf("updating task agent id: %w", err)
	}
	return nil
}

// UpdateWorktreeInfo sets the worktree path and branch for a task.
func (r *TaskRepo) UpdateWorktreeInfo(ctx context.Context, id, worktreePath, branch string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET worktree_path = ?, worktree_branch = ?, updated_at = datetime('now') WHERE id = ?`,
		worktreePath, branch, id)
	if err != nil {
		return fmt.Errorf("updating worktree info: %w", err)
	}
	return nil
}

// UpdateMergeStatus sets the merge status for a task.
func (r *TaskRepo) UpdateMergeStatus(ctx context.Context, id string, status models.MergeStatus) error {
	_, err := execBoundSQLite(ctx, r.db,
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
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET auto_merge = ?, merge_target_branch = ?, updated_at = datetime('now') WHERE id = ?`,
		am, targetBranch, id)
	if err != nil {
		return fmt.Errorf("updating auto merge: %w", err)
	}
	return nil
}

// ClearWorktreeInfo removes worktree path/branch from a task (after cleanup).
func (r *TaskRepo) ClearWorktreeInfo(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET worktree_path = '', worktree_branch = '', updated_at = datetime('now') WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("clearing worktree info: %w", err)
	}
	return nil
}

// ListWithWorktrees returns all tasks that have active worktrees with only
// the columns needed by periodic cleanup decisions.
func (r *TaskRepo) ListWithWorktrees(ctx context.Context) ([]models.Task, error) {
	query := `SELECT ` + worktreeCleanupTaskSelectColumns + `
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
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.Status, &t.WorktreePath, &t.WorktreeBranch, &t.MergeTargetBranch, &t.MergeStatus); err != nil {
			return nil, fmt.Errorf("scanning task worktree cleanup row: %w", err)
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
	_, err := execBoundSQLite(ctx, r.db,
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
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET created_via = 'telegram', telegram_chat_id = ?, updated_at = datetime('now') WHERE id = ?`,
		chatID, id)
	if err != nil {
		return fmt.Errorf("updating telegram origin: %w", err)
	}
	return nil
}

// UpdateSlackOrigin marks a task as created via Slack.
func (r *TaskRepo) UpdateSlackOrigin(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET created_via = 'slack', updated_at = datetime('now') WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("updating slack origin: %w", err)
	}
	return nil
}

// UpdateEmailOrigin marks a task as created via Email.
func (r *TaskRepo) UpdateEmailOrigin(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET created_via = 'email', updated_at = datetime('now') WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("updating email origin: %w", err)
	}
	return nil
}

// UpdateDiscordOrigin marks a task as created via Discord.
func (r *TaskRepo) UpdateDiscordOrigin(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE tasks SET created_via = 'discord', updated_at = datetime('now') WHERE id = ?`,
		id)
	if err != nil {
		return fmt.Errorf("updating discord origin: %w", err)
	}
	return nil
}

// UpdateXOrigin marks a task as created through X.
func (r *TaskRepo) UpdateXOrigin(ctx context.Context, id string) error {
	_, err := execBoundSQLite(ctx, r.db, `UPDATE tasks SET created_via = 'x', updated_at = datetime('now') WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("updating X origin: %w", err)
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
