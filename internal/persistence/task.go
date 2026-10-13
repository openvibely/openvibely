package persistence

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrDuplicateTask = errors.New("task with this name already exists in this project")

// ProjectTaskStatusCounts contains only the task predicates needed by the
// terminal status view. It intentionally does not hydrate task cards.
type ProjectTaskStatusCounts struct {
	ActiveTasks int
	QueuedTasks int
	FailedTasks int
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

// ChatTaskContextRow is the bounded task projection used to build external
// channel Chat context. It is intentionally not a models.Task so callers
// cannot accidentally treat it as a full record for mutation or execution.
type ChatTaskContextRow struct {
	ID            string
	Title         string
	Category      models.TaskCategory
	Priority      int
	Status        models.TaskStatus
	PromptPreview string
	AgentID       *string
	Tag           models.TaskTag
	ParentTaskID  *string
	ChainConfig   string
}

// ProjectedTaskCategoryChange describes one compact task row moved by a
// project-scoped normalization update.
type ProjectedTaskCategoryChange struct {
	ID          string
	Title       string
	OldCategory models.TaskCategory
	Category    models.TaskCategory
}

// TaskReference is the compact task projection used by machine-facing reference
// lookup. It deliberately omits detail-only task payloads.
type TaskReference struct {
	ID                       string
	ProjectID                string
	Title                    string
	Prompt                   string
	Category                 models.TaskCategory
	Priority                 int
	Status                   models.TaskStatus
	AgentID                  *string
	AgentDefinitionID        *string
	Tag                      models.TaskTag
	DisplayOrder             int
	ParentTaskID             *string
	ChainEnabled             bool
	SwarmRole                models.SwarmRole
	HasRunnableSwarmChild    bool
	HasGoal                  bool
	AutomationCapacityQueued bool
}

var ErrActiveLaneTaskChanged = errors.New("active lane task changed")

var ErrActiveLaneLifecycleOwned = errors.New("active lane task requires lifecycle-owned activation")

type ActiveLaneTaskMove struct {
	ID               string              `json:"id"`
	ExpectedCategory models.TaskCategory `json:"category"`
	ExpectedStatus   models.TaskStatus   `json:"status"`
}

type activeLaneExpectedStateKey struct{}

// WithActiveLaneExpectedState carries a browser-observed state fence into a
// lifecycle owner's first durable mutation.
func WithActiveLaneExpectedState(ctx context.Context, move ActiveLaneTaskMove) context.Context {
	return context.WithValue(ctx, activeLaneExpectedStateKey{}, move)
}

func activeLaneExpectedState(ctx context.Context, taskID string) (ActiveLaneTaskMove, bool) {
	if ctx == nil {
		return ActiveLaneTaskMove{}, false
	}
	move, ok := ctx.Value(activeLaneExpectedStateKey{}).(ActiveLaneTaskMove)
	return move, ok && move.ID == taskID && move.ExpectedCategory != "" && move.ExpectedStatus != ""
}

// ActiveLaneExpectedState exposes the guarded move to lifecycle-owning services.
func ActiveLaneExpectedState(ctx context.Context, taskID string) (ActiveLaneTaskMove, bool) {
	return activeLaneExpectedState(ctx, taskID)
}

type ActiveLaneTaskAdmission struct {
	Task        models.Task
	ExecutionID string
}

// TaskDispatchClaim is the authoritative ordinary-worker dispatch state loaded
// in the same write transaction that promotes its Task to running.
type TaskDispatchClaim struct {
	Task              models.Task
	AutomationContext models.AutomationContext
}

// TaskDiscoveryFilter bounds a read-only, current-project task discovery query.
type TaskDiscoveryFilter struct {
	// Query is an optional partial (case-insensitive substring) title match.
	Query string
	// Category optionally restricts results to a single non-chat category.
	Category string
	// Status optionally restricts results to a single task status.
	Status string
	// MergeStatus optionally restricts results by stored merge status. The value
	// "unmerged" matches tasks with a worktree branch that is not yet merged.
	MergeStatus string
	// Limit caps the number of returned rows. Callers should clamp before use.
	Limit int
	// Offset skips the first N rows for pagination.
	Offset int
}

type TaskWorktreeCleanup struct {
	TaskID         string
	WorktreePath   string
	WorktreeBranch string
	DeleteBranch   bool
}

type TaskDeletionManifest struct {
	TaskIDs                  []string
	TaskWorktrees            []TaskWorktreeCleanup
	TaskAttachmentPaths      []string
	ExecutionAttachmentPaths []string
	PendingUploadSessionIDs  []string
	SwarmChildTaskIDs        []string
}

// TaskWithSchedule represents a task with its schedule information for calendar view
type TaskWithSchedule struct {
	Task                   models.Task
	Schedule               *models.Schedule
	AutomationScheduleName string
}
