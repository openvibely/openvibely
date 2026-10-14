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

// ExecutionUsageRecord describes one recorded provider usage result.
type ExecutionUsageRecord struct {
	TotalTokens       int
	Status, Operation string
}

// Fixtures describes test domain state, never tables, query text, or connections.
type ChannelProjectSelectionCounts struct{ Discord, Slack, Telegram, Email int }

// AutomationStateCounts includes retained graphs and unbound resources, so failed saves cannot hide orphaned state.
type AutomationStateCounts struct{ Automations, Versions, Nodes, Edges, Resources, Tasks, Schedules, Invocations, WorkItems, Activities, ChatConfirmations int }

type ProjectAutomationState struct{ Automations, Tasks, Schedules int }

type Fixtures interface {
	// SetAutomationLifecycleSnapshot bypasses queue pruning to construct a stale dispatch candidate.
	SetAutomationLifecycleSnapshot(context.Context, string, string, models.AutomationLifecycleState) error
	SetTaskCreationOrigin(context.Context, string, string) error
	CountTasksByCreationOrigin(context.Context, string, string) (int, error)
	CountAutomationTriggerOwners(context.Context, string, string, string) (int, error)

	ProjectAutomationState(context.Context, string) (ProjectAutomationState, error)
	SeedAutomationEdge(context.Context, models.AutomationEdge) error
	SeedAutomationResource(context.Context, models.AutomationDefinitionResource) error
	SetAutomationHealth(context.Context, string, models.AutomationHealthState, string) error
	SetSlackAuthorizationUnavailable(context.Context, bool) error
	AutomationState(context.Context) (AutomationStateCounts, error)
	FailScheduleCreation(context.Context) error
	FailScheduleUpdate(context.Context) error
	RemoveAutomationCandidate(context.Context, string, string, string) error
	SetAutomationCandidate(context.Context, string, models.AutomationDraftCandidate) error
	SetAutomationHealthEvaluationTime(context.Context, string, *time.Time) error
	SetAutomationPublishedVersion(context.Context, string, string) error
	SetAutomationInvocationStart(context.Context, string, time.Time) error
	SetAutomationActivityStart(context.Context, string, time.Time) error
	SetAutomationTransitionTime(context.Context, string, string, time.Time) error
	SetAutomationWorkItemHistory(context.Context, string, time.Time, bool) error
	// SeedAutomationActivity preserves historical resource identities without current normalization.
	SeedAutomationActivity(context.Context, models.AutomationActivity, []models.AutomationActivityResource) error
	SetAutomationInvocationCompletionTime(context.Context, string, time.Time) error
	SetTaskPullRequestUpdatedAt(context.Context, string, time.Time) error
	// SetTaskCategory constructs board state without publishing application events.
	SetTaskCategory(context.Context, string, models.TaskCategory) error
	// FailLatestActiveAutomationInvocation simulates a terminal runtime failure before projection reconciliation.
	FailLatestActiveAutomationInvocation(context.Context, string, string) (string, error)
	// MakeProjectDetailsUnreadable constructs damaged detail metadata while preserving selector identity.
	MakeProjectDetailsUnreadable(context.Context, string) error
	ChannelProjectSelections(context.Context) (ChannelProjectSelectionCounts, error)
	PendingSteering(context.Context, string, string) ([]models.ThreadInput, error)
	SetTaskDisplayOrder(context.Context, string, int) error
	SeedProjects(context.Context, []models.Project) error
	// SetTaskPromptCreationFailure rejects durable task inserts containing the given prompt fragment until disabled.
	SetTaskPromptCreationFailure(context.Context, string, bool) error
	// SetQueuedInputCreationFailure rejects durable input inserts containing the given content fragment until disabled.
	SetQueuedInputCreationFailure(context.Context, string, bool) error
	ProjectExecutions(context.Context, string, int) ([]models.Execution, error)
	SeedSlackAuthorizedUsers(context.Context, []models.SlackAuthorizedUser) error
	SetSlackProjectSelectionUnavailable(context.Context, bool) error
	FailSettingWrites(context.Context, string) error
	// SetExecutionTerminalState constructs an execution completed before dispatch reconciliation.
	SetExecutionTerminalState(context.Context, string, models.ExecutionStatus, time.Time) error
	ExecutionUsage(context.Context, string) ([]ExecutionUsageRecord, error)
	SetExecutionReasoning(context.Context, string, string) error
	FailTaskCompletion(context.Context) error
	CountProjectAgentMutations(context.Context, string) (int, error)
	CountTasks(context.Context) (int, error)
	SetProjectDeletionFailure(context.Context, string, bool) error
	SeedAutomationIssueTaskOrigin(context.Context, AutomationIssueTaskOrigin) error
	SetProjectDefault(context.Context, string, bool) error
	FailXTaskOriginWrites(context.Context) error
	FailXTaskContextWrites(context.Context) error
	SeedTelegramAuthorizedUsers(context.Context, []models.TelegramAuthorizedUser) error
	FailExecutionCreation(context.Context) error
	FailTelegramProjectSelectionWrites(context.Context) error
	// SetTaskCreationFailure fails inserts for a title until disabled, allowing retry/recovery tests.
	SetTaskCreationFailure(context.Context, string, bool) error
	// SetStaleProjectDefaultModel constructs a legacy reference to a missing model.
	SetStaleProjectDefaultModel(context.Context, string, string) error
	// ObserveAgentDeclarationWrites starts observing durable agent updates and lifecycle-hook mutations.
	ObserveAgentDeclarationWrites(context.Context) error
	AgentDeclarationWrites(context.Context) (int, error)
	// SeedForwardedGitHubFeedback preserves legacy repository spelling without queuing another input.
	SeedForwardedGitHubFeedback(context.Context, models.GitHubPRFeedbackForwarded) error
	// SetTaskProject simulates ownership changing while an operation waits for a lease.
	SetTaskProject(context.Context, string, string) error
	FailExecutionCancellation(context.Context, string) error
	FailTaskAttachmentCreation(context.Context) error
	FailTaskActiveLaneMove(context.Context, string) error
	SetAgentArchivedAt(context.Context, string, time.Time) error
	RenewAutomationDispatchLease(context.Context, string, string, time.Time) error
	AutomationDispatchState(context.Context, string) (AutomationDispatchState, error)
	CompleteAutomationActivity(context.Context, string, time.Time) error
	SeedAutomationNode(context.Context, models.AutomationNode) error
	SetAutomationUpdatedAt(context.Context, string, time.Time) error
	SeedAutomationInvocation(context.Context, models.AutomationInvocation) error
	// SeedRetainedScheduleOwnership attaches a schedule to a retained graph without publication.
	SeedRetainedScheduleOwnership(context.Context, models.AutomationNode, string) error

	// SetLegacyModelName bypasses current uniqueness validation for pre-validation records.
	SetLegacyModelName(context.Context, string, string) error
	SetAutomationTemplateRevision(context.Context, string, string, int) error
	CountAlertsWithIdempotencyKey(context.Context, string, string) (int, error)
	PublishedAutomationVersionID(context.Context, string) (string, error)
	SetScheduleNextRun(context.Context, string, *time.Time) error

	// RemoveAllProjects constructs the empty-installation state, including removal of the protected default.
	RemoveAllProjects(context.Context) error
	// SeedSavedChannelTargets preserves historical timestamps and home flags without applying save transitions.
	SeedSavedChannelTargets(context.Context, []models.ChannelTarget) error
	WebhookEndpoints(context.Context, string) ([]models.WebhookEndpoint, error)
	TaskAgentAssignments(context.Context, string) ([]models.TaskAgentAssignment, error)
	SetLegacyWebhookPriority(context.Context, string, int) error
	SetInputEditHoldUntil(context.Context, string, time.Time) error
	FailExecutionReplayWrites(context.Context) error
	FailSteeringPreparation(context.Context) error
	CountProjectExecutions(context.Context, string) (int, error)
	SeedAutomation(context.Context, models.Automation) error
	SeedAutomationVersion(context.Context, models.AutomationVersion) error
	AutomationVersions(context.Context, string) ([]models.AutomationVersion, error)
	SetAutomationNodeConfig(context.Context, string, string) error
	SeedLifecycleEvent(context.Context, *models.LifecycleExecutionEvent) error
	SetLifecycleExecutionIdentity(context.Context, string, string, time.Time) error
	CountLifecycleHooks(context.Context) (int, error)
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
	// AgentReads counts physical reads of saved agent definitions.
	AgentReads                                                                  int
	ModelRuntimeListReads, ModelRuntimeLookupReads, ModelCreationSelectionReads int
	ExecutionCountReads, ExecutionPageReads, AlertReads, ProjectReads           int
	// ModelReads and TaskContextReads count physical model-catalog/detail and task-context reads.
	ModelReads, TaskContextReads int
	// ModelSelectionReads counts physical compact model-selection reads, excluding full detail hydration.
	ModelSelectionReads int
	// TaskWrites and ExecutionWrites count physical mutation requests, including batched requests.
	TaskWrites, ExecutionWrites int
	// SlackAuthorizationReads counts physical reads checking Slack authorization.
	SlackAuthorizationReads int
	// SettingsSingleReads counts physical reads loading one setting by key.
	SettingsSingleReads int
	// SettingsSnapshotReads counts physical reads loading a group of settings.
	SettingsSnapshotReads int
	// ProjectCatalogReads counts physical unfiltered project-list reads.
	ProjectCatalogReads int
	// TelegramAuthorizationReads counts physical reads checking Telegram authorization.
	TelegramAuthorizationReads int
	// GitHubAuthorizationReads counts physical reads checking authorized GitHub actors.
	GitHubAuthorizationReads int
	// ExecutionHistoryReads counts physical reads of task execution transcripts.
	ExecutionHistoryReads int
	Requests              int
	ReadTextBytes         int
}

type IOCounter interface {
	Reset()
	SetEnabled(bool)
	Snapshot() IOStats
}

// AutomationDispatchState exposes durable dispatch outcome and outstanding task reservations.
type AutomationDispatchState struct {
	Status           string
	TaskReservations int
}

// AutomationIssueTaskOrigin records the issue and graph node that produced a task.
type AutomationIssueTaskOrigin struct {
	ProjectID, AutomationID, TaskID, IssueResourceID, ImplementationNodeKey string
}
