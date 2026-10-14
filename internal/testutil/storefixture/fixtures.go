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
type AutomationStateCounts struct{ Automations, Versions, Nodes, Edges, Resources, Tasks, Schedules, Invocations, WorkItems, Activities, ChatConfirmations, Dispatches int }

type ProjectAutomationState struct{ Automations, Tasks, Schedules int }

// TaskDiscoveryWorkload describes mixed active/chat tasks with deterministic ordering and large hidden payloads.
type TaskDiscoveryWorkload struct {
	ProjectID                              string
	Count, PromptBytes, ConfigurationBytes int
}

// AutomationExecutionOwnership preserves exact activity and execution-resource multiplicity.
type AutomationExecutionOwnership struct{ Activities, ExecutionResources int }

// ExecutionHistoryEntry describes historical output without applying live execution transitions.
type ExecutionHistoryEntry struct {
	ID, TaskID, AgentConfigID, PromptSent, Output, ErrorMessage string
	Status                                                      models.ExecutionStatus
	TokensUsed                                                  int
	DurationMs                                                  int64
	StartedAt                                                   time.Time
	CompletedAt                                                 *time.Time
}

// TaskPayload captures task instructions and orchestration configuration without lifecycle changes.
type TaskPayload struct{ Prompt, ChainConfig, SwarmConfig string }

// PendingTask describes initial state with a stable identity for load fixtures.
type PendingTask struct {
	ID, ProjectID, Title, Prompt string
	Category                     models.TaskCategory
}

// Projection matches describe domain identities and state; empty fields leave that identity unconstrained.
type ActivityMatch struct {
	ProjectID, AutomationID, VersionID, ID, Key, Type, Status, WorkItemID string
	HasWorkItem                                                           bool
}
type WorkItemMatch struct{ ProjectID, AutomationID, VersionID, ID, Key, Kind, Status string }
type PositionMatch struct{ ProjectID, AutomationID, VersionID, WorkItemID, NodeID, NodeKey, State, WorkItemKey string }
type TransitionMatch struct{ ProjectID, AutomationID, VersionID, WorkItemID, EdgeKey, State string }

type AutomationResourceMatch struct{ ProjectID, AutomationID, VersionID, NodeKey, ResourceType string }

type GitHubIssueClaim struct {
	OwnerToken, ProjectionSource, MutationState string
	IssueNumber                                 *int
}
type IssueProjectionFailure struct{ AutomationID, AssignmentEventKey string }
type Fixtures interface {
	SetHistoricalExecutionCompletion(context.Context, string, time.Time, int64) error
	SetDanglingTaskAgent(context.Context, string, string) error

	SetDefaultProject(context.Context, string) error
	FailClearingSetting(context.Context, string) error
	FailConflictStatus(context.Context) error
	MoveTaskProject(context.Context, string, string) error
	SetTaskDates(context.Context, string, time.Time, time.Time, *time.Time) error
	LinkHistoricalAlertTask(context.Context, string, string, string) error
	FailCapacitySources(context.Context) error

	SeedReservedAutomationTask(context.Context, string, string, string) error
	CountTaskThreadInputs(context.Context, string) (int, error)

	GitHubIssueClaims(context.Context, string, string, string) ([]GitHubIssueClaim, error)
	ExpireGitHubIssueClaim(context.Context, string, string, string, time.Time) error
	SetIssueProjectionFailure(context.Context, *IssueProjectionFailure) error

	AutomationDefinitionResources(context.Context, AutomationResourceMatch) ([]models.AutomationDefinitionResource, error)
	AutomationOwnedScheduleIDs(context.Context, string) ([]string, error)
	ClearAutomationTemplateRevision(context.Context, string) error

	AutomationActivityResources(context.Context, ActivityMatch) ([]models.AutomationActivityResource, error)

	CountMatchingActivities(context.Context, ActivityMatch) (int, error)
	CountMatchingWorkItems(context.Context, WorkItemMatch) (int, error)
	CountMatchingPositions(context.Context, PositionMatch) (int, error)
	CountMatchingTransitions(context.Context, TransitionMatch) (int, error)
	AutomationActivityStatus(context.Context, string) (models.AutomationActivityStatus, error)
	SeedAutomationPosition(context.Context, models.AutomationWorkItemPosition) error
	SeedAutomationWorkItem(context.Context, models.AutomationWorkItem) error
	SetLiveAutomationActivity(context.Context, string, string) error

	SeedAutomationTriggerOwnership(context.Context, models.AutomationNode, string, string) error
	AutomationDispatchOrder(context.Context, []string) ([]string, error)
	CountTaskAutomationReservations(context.Context, []string) (int, error)

	EditPendingAutomationTask(context.Context, string, string, *string) error
	ExpireAutomationDispatch(context.Context, string, time.Time) error
	InspectAutomationDispatch(context.Context, string) (AutomationDispatchOutcome, error)

	// Projection inventories count exact durable state without history pagination.
	CountAutomationActivities(context.Context, string, string, models.AutomationActivityStatus) (int, error)
	CountAutomationWorkItems(context.Context, string, string) (int, error)
	CountAutomationTransitions(context.Context, string, string) (int, error)
	CountAutomationResourceLinks(context.Context, string, string) (int, error)
	AutomationContextForWorkItem(context.Context, string, string) (models.AutomationContext, error)

	SeedAutomationWorkItemPosition(context.Context, models.AutomationWorkItem, models.AutomationWorkItemPosition) error
	SetAutomationPositionState(context.Context, string, string, models.AutomationPositionState) error

	// ClearProjects removes default and user projects to construct an empty workspace.
	ClearProjects(context.Context) error
	SeedPendingTasks(context.Context, []PendingTask) error

	SeedAnalyticsWorkload(context.Context, AnalyticsWorkload) (string, error)
	SeedTaskFrequencyWorkload(context.Context, string, string, int) ([]string, error)

	FailChatAttachmentCreation(context.Context) error
	FailTaskClaim(context.Context) error
	RetiredAttachmentSessions(context.Context) ([]string, error)

	SetTaskPayload(context.Context, string, TaskPayload) error
	SeedExecutionHistory(context.Context, []ExecutionHistoryEntry) error
	AsyncToolCallStatus(context.Context, string) (string, error)
	RemoveAutomationTaskBinding(context.Context, string, string) error
	AutomationExecutionOwnership(context.Context, string, string) (AutomationExecutionOwnership, error)
	CountDispatchExecutions(context.Context, string) (int, error)
	// CountAutomationInvocations includes all retained history; an empty trigger ID includes every trigger.
	CountAutomationInvocations(context.Context, string, string, string) (int, error)
	SeedTaskDiscoveryWorkload(context.Context, TaskDiscoveryWorkload) error
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
	ExecutionMetricsReads, ExecutionPayloadReads, AgentLabelReads, ThreadMetadataReads, TaskPayloadReads int

	ExecutionReads                                                                                                     int
	AgentTaskUIReads, ProjectCapacityReads, ProjectOtherOrderedReads, PendingProjectCountReads                         int
	ModelWorkerReads, ModelBadgeReads, ModelDetailReads, NewTaskSelectionReads                                         int
	TaskReads, TaskActionReads                                                                                         int
	AutomationIdentityReads, AutomationGraphReads, AutomationDefinitionNodeReads                                       int
	AutomationPageReads, AutomationDirectReads, AutomationSelectedMetricsReads                                         int
	AutomationHealthWrites, AgentReferenceReads, AgentDetailReads                                                      int
	AutomationVersionReads, AutomationNodeReads, AutomationEdgeReads, AutomationResourceReads, AutomationMetadataReads int

	AgentPageReads, UnboundedAgentListReads, MutationRequests, AgentDeclarationMutationRequests int

	// ExecutionWindowReads counts bounded newest-first history requests; UnboundedExecutionHistoryReads counts full chronological history requests.
	ExecutionWindowReads, UnboundedExecutionHistoryReads int
	// AgentReads counts physical reads of saved agent definitions.
	AgentReads                                                                  int
	ModelRuntimeListReads, ModelRuntimeLookupReads, ModelCreationSelectionReads int
	ExecutionCountReads, ExecutionPageReads, AlertReads, ProjectReads           int
	// ModelReads and TaskContextReads count physical model-catalog/detail and task-context reads.
	ModelReads, TaskContextReads int
	// ModelSelectionReads counts physical compact model-selection reads, excluding full detail hydration.
	ModelSelectionReads int
	// ModelFullCatalogReads counts physical model catalog reads including credentials/configuration.
	ModelFullCatalogReads                              int
	ModelCatalogReads, ModelPickerReads, ScheduleReads int
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

type ReadOperation string

const (
	ExecutionWindowRead  ReadOperation = "execution_window"
	ExecutionMetricsRead ReadOperation = "execution_metrics"
	AgentRead            ReadOperation = "agent"
)

type IOCounter interface {
	ObserveReads(ReadOperation, func(context.Context))

	// ObserveRequests observes requests at the backend driver boundary, including uncounted requests.
	ObserveRequests(func(context.Context))

	Reset()
	SetEnabled(bool)
	Snapshot() IOStats
}

// AutomationDispatchState exposes durable dispatch outcome and outstanding task reservations.
type AutomationDispatchState struct {
	Status           string
	TaskReservations int
	Executions       int
}

// AutomationIssueTaskOrigin records the issue and graph node that produced a task.
type AutomationIssueTaskOrigin struct {
	ProjectID, AutomationID, TaskID, IssueResourceID, ImplementationNodeKey string
}

// BackendContention reports physical backend admission and waiting, never store calls.
type BackendContention struct{ Reads, Writes IOContention }
type IOContention struct {
	Capacity, InUse int
	WaitCount       int64
	WaitDuration    time.Duration
}

// AnalyticsWorkload selects reproducible dashboard histories at production-scale sizes.
type AnalyticsWorkload string

const (
	AnalyticsContention    AnalyticsWorkload = "contention"
	AnalyticsAgentEvidence AnalyticsWorkload = "agent_evidence"
	AnalyticsOverview      AnalyticsWorkload = "overview"
)

// AutomationDispatchOutcome observes dispatch ownership and execution state in one backend read.
type AutomationDispatchOutcome struct {
	Status, ExecutionID, TaskStatus, ExecutionStatus, InvocationStatus, ActivityStatus string
	ExecutionError, PromptSent, AgentConfigID                                          string
	Reservations, Executions, Activities, ExecutionActivities                          int
	ExecutionCompleted, InvocationCompleted                                            bool
	NextAttemptAt                                                                      time.Time
}
