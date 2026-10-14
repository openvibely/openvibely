// Package storefixture defines backend-independent setup and inspection operations
// for tests. Implementations share the database used by the real stores, but
// deliberately bypass application transitions when constructing test state.
package storefixture

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// ExecutionOutputState distinguishes committed transcript text from pending buffered segments.
type ExecutionOutputState struct {
	Materialized    string
	PendingSegments int
}

type SkillEventFilter struct {
	ProjectID, TaskID, SkillHandle, AgentID, EventType, Source, Surface, SkillScope string
}

// Fixtures describes test domain state, never tables, query text, or connections.
type Fixtures interface {
	CountProducedCommitStats(context.Context) (int, error)
	LatestExecution(context.Context) (*models.Execution, error)
	SeedDiscordAuthorizedUsers(context.Context, []models.DiscordAuthorizedUser) error
	// WriteExecutionSnapshotBaseline performs the legacy full-output overwrite,
	// without chunk compaction, for comparison against incremental persistence.
	WriteExecutionSnapshotBaseline(context.Context, string, string) error
	SeedOrphanSchedule(context.Context, string, string, time.Time) error
	SeedOverflowSchedule(context.Context, string, string, time.Time) error
	// FailTaskDeletion aborts durable deletion, including the enclosing transaction.
	FailTaskDeletion(context.Context, string, string) error
	FailTaskBacklogMove(context.Context, string) error
	// SetTaskStatus bypasses lifecycle cleanup to construct stale reservations.
	SetTaskStatus(context.Context, string, models.TaskStatus) error
	SetVerifiedOAuthPrincipal(context.Context, string, string) error
	SetTaskCreatedAt(context.Context, string, time.Time) error
	SetExecutionCompletedAt(context.Context, string, time.Time) error
	KnowledgeEntry(context.Context, string, string) (*models.KnowledgeEntry, error)
	// PromoteQueuedTaskInput atomically reserves an input and execution without model-selection validation.
	PromoteQueuedTaskInput(context.Context, string, *models.Execution) error
	// SetModelOAuthTokens simulates credential replacement by a competing refresher.
	SetModelOAuthTokens(context.Context, string, string, string, int64) error
	SeedAccountUsageSnapshot(context.Context, *models.AccountUsageSnapshot) error
	CountAccountUsageSnapshots(context.Context) (int, error)
	LinkModelOAuthConnection(context.Context, string, string) error
	SkillEvents(context.Context, SkillEventFilter) ([]models.SkillAnalyticsEvent, error)
	ProducedCommitStats(context.Context, string, time.Time) ([]models.TaskCommitStat, error)
	SeedChannelTarget(context.Context, models.ChannelTarget) error
	ChannelMessageSends(context.Context, string) ([]models.ChannelMessageSend, error)

	// SetLegacyAgentName bypasses name normalization for pre-validation saved data.
	SetLegacyAgentName(context.Context, string, string) error
	ExecutionOutputState(context.Context, string) (ExecutionOutputState, error)

	// ClaimScheduleOccurrence constructs the durable scheduler admission used by recovery tests.
	ClaimScheduleOccurrence(context.Context, models.Schedule, time.Time, *time.Time) (bool, error)
	SetScheduleRunTimes(context.Context, string, time.Time, *time.Time) error
	// PauseScheduleAfterEnable simulates a concurrent pause winning before the service rereads state.
	PauseScheduleAfterEnable(context.Context, string) error

	SetExecutionStartedAt(context.Context, string, time.Time) error
	SetTaskUpdatedAt(context.Context, string, time.Time) error
	SetTaskCompletedAt(context.Context, string, time.Time) error
	CountAutomations(context.Context) (int, error)
}

// IOStats measures physical backend work, not calls to persistence interfaces.
// SQLite counts executed statements; other backends must count their database
// requests, including requests made inside a single store method.
type IOStats struct {
	Requests      int
	ReadTextBytes int
}

type IOCounter interface {
	Reset()
	SetEnabled(bool)
	Snapshot() IOStats
}
