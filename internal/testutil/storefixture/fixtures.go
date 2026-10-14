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
