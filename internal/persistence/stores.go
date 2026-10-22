package persistence

import (
	"context"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

// AgentStore persists agent definitions.
type AgentStore interface {
	Create(ctx context.Context, a *models.Agent) error
	Delete(ctx context.Context, id string) error
	DeleteBulk(ctx context.Context, ids []string) error
	GetByID(ctx context.Context, id string) (*models.Agent, error)
	GetByKey(ctx context.Context, key string) (*models.Agent, error)
	GetByKeyIncludingArchived(ctx context.Context, key string) (*models.Agent, error)
	GetByName(ctx context.Context, name string) (*models.Agent, error)
	GetBySystemKind(ctx context.Context, systemKind string) (*models.Agent, error)
	GetTaskDetailAgentLabel(ctx context.Context, projectID string, agentID string) (*AgentPickerOption, error)
	GetUniqueSelectableByName(ctx context.Context, name string) (*models.Agent, error)
	ListAgentListSummaries(ctx context.Context) ([]AgentListSummary, error)
	ListByName(ctx context.Context, name string) ([]models.Agent, error)
	ListBySystemKind(ctx context.Context, systemKind string) ([]models.Agent, error)
	ListChatAssignableDefinitions(ctx context.Context) ([]models.ChatAssignableAgentDefinition, error)
	ListIncludingArchived(ctx context.Context) ([]models.Agent, error)
	ListPage(ctx context.Context, limit int, offset int, search string) ([]models.Agent, error)
	ListPageFiltered(ctx context.Context, limit int, offset int, filter AgentPageFilter) ([]models.Agent, error)
	ListPickerOptionsForProject(ctx context.Context, projectID string) ([]AgentPickerOption, error)
	ListRuntimeSummaries(ctx context.Context) ([]AgentRuntimeSummary, error)
	ListScheduleOptions(ctx context.Context, projectID string) ([]AgentScheduleOption, error)
	ListSelectableForProject(ctx context.Context, projectID string, limit int) ([]models.Agent, error)
	ListSelectableReferencesForProject(ctx context.Context, projectID string, limit int) ([]AgentSelectableReference, error)
	ListSkillCatalogRefs(ctx context.Context) ([]AgentSkillCatalogRef, error)
	ListTaskUIOptions(ctx context.Context) ([]AgentTaskUIOption, error)
	MarkArchived(ctx context.Context, id string, absorbedInto string, reason string) error
	Update(ctx context.Context, a *models.Agent) error
}

// AlertStore persists project alerts and actionable notifications.
type AlertStore interface {
	AlertOwnedByAutomation(ctx context.Context, projectID string, alertID string, bindings []models.AutomationBinding) (bool, error)
	ClaimApproved(ctx context.Context, projectID string, id string, claimant string, lease time.Duration) (*models.Alert, error)
	CountPending(ctx context.Context, projectID string) (int, error)
	CountUnread(ctx context.Context, projectID string) (int, error)
	Create(ctx context.Context, a *models.Alert) error
	CreateIdempotent(ctx context.Context, a *models.Alert) (*models.Alert, error)
	CreateImplementationTask(ctx context.Context, projectID string, alertID string, claimant string, input models.AlertImplementationTaskInput) (*models.Task, error)
	Delete(ctx context.Context, projectID string, id string) error
	DeleteAll(ctx context.Context, projectID string) error
	DeleteBulk(ctx context.Context, projectID string, ids []string) error
	GetByIDAdmin(ctx context.Context, id string) (*models.Alert, error)
	GetByIDForProject(ctx context.Context, projectID string, id string) (*models.Alert, error)
	LinkImplementationTask(ctx context.Context, projectID string, id string, claimant string, taskID string) error
	ListByProject(ctx context.Context, projectID string, limit int) ([]models.Alert, error)
	ListFiltered(ctx context.Context, projectID string, filter models.AlertListFilter) ([]models.Alert, error)
	ListFilteredSummaries(ctx context.Context, projectID string, filter models.AlertListFilter) ([]models.AlertSummary, error)
	ListSummariesByProject(ctx context.Context, projectID string, limit int) ([]models.AlertSummary, error)
	MarkAllRead(ctx context.Context, projectID string) error
	MarkProcessing(ctx context.Context, projectID string, id string, claimant string, state models.AlertProcessingState, message string) error
	MarkRead(ctx context.Context, projectID string, id string) error
	MarkReadBulk(ctx context.Context, projectID string, ids []string) error
	NativeInboxBindings(ctx context.Context, automationContext models.AutomationContext) ([]models.AutomationBinding, error)
	RebindAlertToAutomationInbox(ctx context.Context, projectID string, alertID string, bindings []models.AutomationBinding) error
	ReleaseClaim(ctx context.Context, projectID string, id string, claimant string) error
	SetDecision(ctx context.Context, projectID string, id string, state models.AlertDecisionState) error
}

// AttachmentStore persists task attachments.
type AttachmentStore interface {
	CreateGeneratedFile(context.Context, *models.GeneratedFile) error
	GeneratedFile(context.Context, string, string) (*models.GeneratedFile, error)
	ListGeneratedFiles(context.Context, string, string) ([]models.GeneratedFile, error)
	ListUploadedFiles(context.Context, string, string) ([]models.FileListItem, error)
	CleanupOrphanedFiles(ctx context.Context, uploadsDir string) (int, error)
	Create(ctx context.Context, att *models.Attachment) error
	Delete(ctx context.Context, id string) error
	DeleteByIDForProject(ctx context.Context, id string, projectID string) error
	GetByID(ctx context.Context, id string) (*models.Attachment, error)
	GetByIDForProject(ctx context.Context, id string, projectID string) (*models.Attachment, error)
	ListByTask(ctx context.Context, taskID string) ([]models.Attachment, error)
	ListByExecutionIDs(ctx context.Context, execIDs []string) (map[string][]models.Attachment, error)
}

// AutomationStore persists automations, their bindings, dispatches, history, and health.
type AutomationStore interface {
	AcquireGitHubIssueDedupLease(ctx context.Context, projectID string, repositoryFullName string, titleFingerprint string, ownerToken string, source AutomationGitHubIssueDedupSource, now time.Time, leaseDuration time.Duration) (AutomationGitHubIssueDedupClaim, error)
	AutomationExternalState(ctx context.Context, projectID string, automationID string, staleBefore time.Time) (models.AutomationExternalState, error)
	BindingsForActivityResource(ctx context.Context, projectID string, automationID string, resourceType string, resourceID string) (models.AutomationContext, error)
	BindingsForExecutionResource(ctx context.Context, projectID string, executionID string, resourceType string, resourceID string) (models.AutomationContext, error)
	CancelDispatchesForTask(ctx context.Context, taskID string, message string) error
	ClaimManualAutomationRun(ctx context.Context, projectID string, automationID string, now time.Time) ([]models.AutomationInvocation, []models.AutomationDispatch, error)
	ClaimScheduledOccurrence(ctx context.Context, schedule models.Schedule, now time.Time, nextRun *time.Time) (*models.AutomationInvocation, *models.AutomationDispatch, error)
	CompleteDispatch(ctx context.Context, dispatchID string, executionID string, status models.ExecutionStatus, message string) error
	CompleteGitHubIssueDedupLease(ctx context.Context, projectID string, repositoryFullName string, titleFingerprint string, ownerToken string, issueNumber int) error
	ContextForExecution(ctx context.Context, projectID string, executionID string) (models.AutomationContext, error)
	ContextForTask(ctx context.Context, projectID string, taskID string) (models.AutomationContext, error)
	ContextForThreadInput(ctx context.Context, projectID string, inputID string) (models.AutomationContext, error)
	CreateAutomationConfirmationReceipt(ctx context.Context, receipt *models.AutomationChatConfirmationReceipt) error
	CreateOrGetGitHubIssueTask(ctx context.Context, in AutomationGitHubIssueTaskCreation) (*models.Task, bool, error)
	CurrentActiveBindingForLaunchNode(ctx context.Context, projectID string, binding models.AutomationBinding, targetRole string) (models.AutomationBinding, bool, error)
	CurrentActiveBindingForNodeKey(ctx context.Context, projectID string, automationID string, nodeKey string, targetRole string) (models.AutomationBinding, bool, error)
	DeleteAutomation(ctx context.Context, projectID string, automationID string) error
	DeleteAutomations(ctx context.Context, projectID string, automationIDs []string) error
	Exists(ctx context.Context, projectID string, automationID string) (bool, error)
	FailDispatch(ctx context.Context, dispatchID string, claimant string, message string, maxAttempts int, now time.Time) error
	GetAutomationConfirmationReceipt(ctx context.Context, tokenID string) (*models.AutomationChatConfirmationReceipt, error)
	GetAutomationGraphMetadata(ctx context.Context, projectID string, automationID string, graphID string) (*models.AutomationGraphMetadata, error)
	GetAutomationInvocation(ctx context.Context, projectID string, automationID string, invocationID string) (*models.AutomationInvocation, error)
	GetAutomationMetrics(ctx context.Context, projectID string, automationID string, versionID string, now time.Time) (models.AutomationMetrics, error)
	GetAutomationWorkItem(ctx context.Context, projectID string, automationID string, workItemID string) (*models.AutomationWorkItem, error)
	GetByStableKey(ctx context.Context, projectID string, stableKey string) (*models.Automation, error)
	GetConnectedNodeByRole(ctx context.Context, projectID string, automationID string, versionID string, nodeID string, role string, outgoing bool) (*models.AutomationNode, error)
	GetCustomNotificationHandoff(ctx context.Context, projectID string, automationID string, versionID string, sourceNodeID string) (*models.AutomationNode, error)
	GetDefinition(ctx context.Context, projectID string, automationID string) (*models.AutomationDefinition, error)
	GetDefinitionVersion(ctx context.Context, projectID string, automationID string, versionID string) (*models.AutomationDefinition, error)
	GetDispatchEnvelope(ctx context.Context, dispatchID string) (*models.AutomationDispatchEnvelope, error)
	GetNodeByKey(ctx context.Context, projectID string, automationID string, versionID string, nodeKey string) (*models.AutomationNode, error)
	GetPendingAutomationConfirmation(ctx context.Context, projectID string, principalID string, threadID string, now time.Time) (*models.AutomationChatConfirmationReceipt, string, error)
	GetRuntimeAutomationCard(ctx context.Context, projectID string, automationID string) (*models.AutomationCard, error)
	GetTriggerOwner(ctx context.Context, scheduleID string) (*models.AutomationTriggerOwner, error)
	GitHubIssueResourceForTask(ctx context.Context, projectID string, taskID string) (string, error)
	GitHubIssueTaskProvenance(ctx context.Context, projectID string, taskID string) (*AutomationGitHubIssueTaskProvenance, error)
	HasAutomationConfirmationInput(ctx context.Context, marker AutomationConfirmationInputMarker) (bool, error)
	IsCurrentActiveBinding(ctx context.Context, projectID string, binding models.AutomationBinding) (bool, error)
	LeaseNextDispatch(ctx context.Context, claimant string, now time.Time, lease time.Duration) (*models.AutomationDispatch, error)
	ListAbandonedQueuedDispatches(ctx context.Context, limit int) ([]models.AutomationDispatch, error)
	ListAutomationActivities(ctx context.Context, projectID string, automationID string, invocationID string, workItemID string, limit int, cursorValue string) (models.AutomationActivityPage, error)
	ListAutomationInvocationNodeIDs(ctx context.Context, projectID string, automationID string, invocationID string, limit int) ([]string, error)
	ListAutomationInvocations(ctx context.Context, projectID string, automationID string, limit int, cursorValue string) (models.AutomationInvocationPage, error)
	ListAutomationPullRequests(ctx context.Context, projectID string, automationID string, limit int) ([]models.TaskPullRequest, error)
	ListAutomationTransitions(ctx context.Context, projectID string, automationID string, invocationID string, workItemID string, limit int, cursorValue string) (models.AutomationTransitionPage, error)
	ListAutomationWorkItems(ctx context.Context, projectID string, automationID string, status string, limit int, cursorValue string) (models.AutomationWorkItemPage, error)
	ListAutomationsWithStaleExternalPullRequests(ctx context.Context, staleBefore time.Time, limit int) ([][2]string, error)
	ListBreadcrumbSelector(ctx context.Context, projectID string, search string, currentID string, limit int) ([]models.BreadcrumbSelectorItem, error)
	ListCustomTaskHandoffs(ctx context.Context, projectID string, automationID string, versionID string, sourceNodeID string) (bool, []CustomAutomationTaskHandoff, error)
	ListExecutionProjectionRepairs(ctx context.Context, limit int) ([]AutomationExecutionProjectionRepair, error)
	ListNodeRuntimeResources(ctx context.Context, projectID string, automationID string, versionID string, nodeID string, limit int, cursorValue string) (models.AutomationNodeResourcePage, error)
	ListPortfolioCards(ctx context.Context, projectID string) ([]models.AutomationCard, error)
	ListPortfolioCardsPageFiltered(ctx context.Context, projectID string, limit int, offset int, filter AutomationCardListFilter) ([]models.AutomationCard, error)
	ListRecoverablePreparedDispatches(ctx context.Context, limit int) ([]models.AutomationDispatch, error)
	ListResourceSummaries(ctx context.Context, projectID string, automationID string, versionID string, limit int) ([]models.AutomationResourceSummary, error)
	ListRuntimeAutomationCards(ctx context.Context, projectID string, limit int, offset int) ([]models.AutomationCard, bool, error)
	ListTerminalUnfinalizedDispatches(ctx context.Context, limit int) ([]models.AutomationDispatch, error)
	LiveEdgeCounts(ctx context.Context, projectID string, automationID string, versionID string, recentCutoff time.Time) (map[string][2]int, error)
	LiveNodeCounts(ctx context.Context, projectID string, automationID string, versionID string, recentCutoff time.Time) (map[string]models.AutomationNodeCounts, int, int, error)
	MarkAutomationConfirmationInput(ctx context.Context, marker AutomationConfirmationInputMarker) error
	MarkDispatchQueued(ctx context.Context, dispatchID string, claimant string) error
	MarkDispatchSubmitted(ctx context.Context, dispatchID string, claimant string, executionID string) error
	MarkGitHubIssueDedupDispatched(ctx context.Context, projectID string, repositoryFullName string, titleFingerprint string, ownerToken string) error
	PortfolioOperationalCounts(ctx context.Context, projectID string, recentCutoff time.Time) (map[string]models.AutomationNodeCounts, error)
	PortfolioOperationalCountsForAutomations(ctx context.Context, projectID string, automationIDs []string, recentCutoff time.Time) (map[string]models.AutomationNodeCounts, error)
	ProjectScheduleResource(ctx context.Context, projectID string, scheduleID string) (AutomationScheduleResource, bool, error)
	ProjectTaskResource(ctx context.Context, projectID string, taskID string) (AutomationTaskResource, bool, error)
	PruneTerminalizedAutomationPositions(ctx context.Context, limit int) (int64, error)
	PublishInvalidation(eventType events.TaskEventType, projectID string, binding models.AutomationBinding)
	PublishRegistered(ctx context.Context, in models.AutomationRegisteredPublication) (*models.AutomationDefinition, bool, error)
	RecomputeAutomationHealth(ctx context.Context, projectID string, automationID string, now time.Time) (models.AutomationHealth, error)
	RecomputeAutomationHealthForAll(ctx context.Context, now time.Time, limit int) error
	ReconcileInvocationCompletions(ctx context.Context, limit int) (int64, error)
	RecordProjectionEvent(ctx context.Context, in AutomationProjectionEvent) (*models.AutomationWorkItem, *models.AutomationActivity, error)
	ReleaseExternalActivityReservation(ctx context.Context, projectID string, binding models.AutomationBinding, activityKey string) error
	ReleaseGitHubIssueDedupLease(ctx context.Context, projectID string, repositoryFullName string, titleFingerprint string, ownerToken string) error
	RepairExecutionProjection(ctx context.Context, repair AutomationExecutionProjectionRepair) error
	ReplayAutomationTransitionPage(ctx context.Context, projectID string, automationID string, workItemID string, cursorValue string, transitions []models.AutomationTransition) ([]models.AutomationReplayFrame, error)
	ReserveExternalActivity(ctx context.Context, projectID string, binding models.AutomationBinding, activityKey string, activityType string, resourceType string) (string, error)
	ResumeAutomation(ctx context.Context, projectID string, automationID string) ([]models.Task, error)
	SaveCurrentGraph(ctx context.Context, in AutomationSaveWrite) (*models.AutomationDefinition, []models.Task, error)
	SetAutomationLifecycle(ctx context.Context, projectID string, automationID string, state models.AutomationLifecycleState) error
}

// ChannelTargetStore persists saved outbound channel targets.
type ChannelTargetStore interface {
	FindByName(ctx context.Context, projectID string, platform string, name string) (*models.ChannelTarget, error)
	FindByTarget(ctx context.Context, projectID string, platform string, targetID string, threadID string) (*models.ChannelTarget, error)
	FindByTargetAndKind(ctx context.Context, projectID string, platform string, targetID string, threadID string, targetKind string) (*models.ChannelTarget, error)
	FindHome(ctx context.Context, projectID string, platform string) (*models.ChannelTarget, error)
	GetByID(ctx context.Context, id string) (*models.ChannelTarget, error)
	ListByProject(ctx context.Context, projectID string) ([]models.ChannelTarget, error)
	ListByProjectPage(ctx context.Context, projectID string, limit int, offset int) ([]models.ChannelTarget, error)
	RecordSend(ctx context.Context, send models.ChannelMessageSend) error
	ReplaceProjectTargets(ctx context.Context, projectID string, targets []models.ChannelTarget) error
	SummarizeByProject(ctx context.Context, projectID string) (ChannelTargetProjectSummary, error)
}

// ChatAttachmentStore persists chat message attachments.
type ChatAttachmentStore interface {
	CleanupOrphanedFiles(ctx context.Context, uploadsDir string) (int, error)
	Create(ctx context.Context, att *models.ChatAttachment) error
	CreateInTx(ctx context.Context, tx Tx, att *models.ChatAttachment) error
	Delete(ctx context.Context, id string) error
	DeleteByIDForProject(ctx context.Context, id string, projectID string) error
	GetByIDForProject(ctx context.Context, id string, projectID string) (*models.ChatAttachment, error)
	ListByExecution(ctx context.Context, executionID string) ([]models.ChatAttachment, error)
	ListByExecutionIDs(ctx context.Context, execIDs []string) (map[string][]models.ChatAttachment, error)
	ListByTask(ctx context.Context, taskID string) ([]models.ChatAttachment, error)
	ListByProject(ctx context.Context, projectID string) ([]models.ChatAttachment, error)
}

// ChatInputRequestStore persists pending chat input requests.
type ChatInputRequestStore interface {
	Create(ctx context.Context, rec ChatInputRequestRecord) error
	Get(ctx context.Context, id string) (*ChatInputRequestRecord, error)
	ListActiveByProject(ctx context.Context, projectID string, now time.Time) ([]ChatInputRequestRecord, error)
	Resolve(ctx context.Context, id string, projectID string, answersJSON string) (*ChatInputRequestRecord, error)
}

// CustomPersonalityStore persists user-defined chat personalities.
type CustomPersonalityStore interface {
	Count(ctx context.Context) (int, error)
	Create(ctx context.Context, p *models.CustomPersonality) error
	Delete(ctx context.Context, key string) error
	DeleteBulk(ctx context.Context, keys []string) error
	GetByKey(ctx context.Context, key string) (*models.CustomPersonality, error)
	List(ctx context.Context) ([]models.CustomPersonality, error)
	ListRuntimePage(ctx context.Context, limit int, offset int) ([]models.CustomPersonality, error)
	Update(ctx context.Context, key string, p *models.CustomPersonality) error
}

// DiscordAuthStore persists authorized Discord users.
type DiscordAuthStore interface {
	CountByProject(ctx context.Context, projectID string) (int, error)
	Create(ctx context.Context, u *models.DiscordAuthorizedUser) error
	DeleteByProject(ctx context.Context, projectID string) error
	DeleteForProject(ctx context.Context, projectID string, id string) error
	GetByID(ctx context.Context, id string) (*models.DiscordAuthorizedUser, error)
	IsAuthorized(ctx context.Context, projectID string, discordUserID string) (bool, error)
	IsAuthorizedAnywhere(ctx context.Context, discordUserID string) (bool, error)
	ListByProject(ctx context.Context, projectID string) ([]models.DiscordAuthorizedUser, error)
}

// DiscordTaskContextStore persists Discord reply context for tasks.
type DiscordTaskContextStore interface {
	GetByTaskID(ctx context.Context, taskID string) (*models.DiscordTaskContext, error)
	Upsert(ctx context.Context, dtc *models.DiscordTaskContext) error
}

// DiscordUserProjectStore persists per-user Discord project selection.
type DiscordUserProjectStore interface {
	GetUserProject(ctx context.Context, discordUserID string) (string, error)
	SetUserProject(ctx context.Context, discordUserID string, projectID string) error
}

// EmailAuthStore persists authorized email senders.
type EmailAuthStore interface {
	CountByProject(ctx context.Context, projectID string) (int, error)
	Create(ctx context.Context, s *models.EmailAuthorizedSender) error
	DeleteForProject(ctx context.Context, projectID string, id string) error
	GetByID(ctx context.Context, id string) (*models.EmailAuthorizedSender, error)
	IsAuthorized(ctx context.Context, projectID string, emailAddress string) (bool, error)
	ListByProject(ctx context.Context, projectID string) ([]models.EmailAuthorizedSender, error)
}

// EmailInboundReceiptStore persists inbound email deduplication receipts.
type EmailInboundReceiptStore interface {
	Exists(ctx context.Context, mailboxAddress string, messageKey string) (bool, error)
	ExistsBatch(ctx context.Context, mailboxAddress string, messageKeys []string) (map[string]struct{}, error)
	Record(ctx context.Context, mailboxAddress string, messageKey string) error
	WithHandoff(ctx context.Context, mailboxAddress string, messageKey string, persist func(Tx) error) (alreadyHandedOff bool, err error)
}

// EmailSenderProjectStore persists per-sender email project selection.
type EmailSenderProjectStore interface {
	GetSenderProject(ctx context.Context, emailAddress string) (string, error)
	SetSenderProject(ctx context.Context, emailAddress string, projectID string) error
}

// EmailTaskContextStore persists email reply context for tasks.
type EmailTaskContextStore interface {
	GetByTaskID(ctx context.Context, taskID string) (*models.EmailTaskContext, error)
	RecordOutboundMessageRef(ctx context.Context, projectID string, sender string, outboundMessageID string, sessionKey string) error
	ResolveOutboundMessageSessionKey(ctx context.Context, projectID string, sender string, outboundMessageID string) (string, error)
	Upsert(ctx context.Context, etc *models.EmailTaskContext) error
}

// ExecutionOutputStore is the narrow execution surface used to stream output while an execution runs.
type ExecutionOutputStore interface {
	AppendOutput(ctx context.Context, id string, output string) error
	FinalizeOutput(ctx context.Context, id string, output string) error
	GetByID(ctx context.Context, id string) (*models.Execution, error)
}

// ExecutionStore persists task and chat executions, their output, and execution analytics.
type ExecutionStore interface {
	ExecutionOutputStore

	CancelActiveByTaskThroughHistoryOrderReturningIDs(ctx context.Context, taskID string, cutoff int64) ([]string, error)
	CancelQueuedOrdinaryByTask(ctx context.Context, taskID string, message string) error
	Complete(ctx context.Context, id string, status models.ExecutionStatus, output string, errMsg string, tokensUsed int, durationMs int64) error
	CountByTask(ctx context.Context, taskID string) (int, error)
	Create(ctx context.Context, e *models.Execution) error
	CreateDirectTaskFollowupOrQueue(ctx context.Context, e *models.Execution, input *models.ThreadInput) (bool, error)
	CreateInTx(ctx context.Context, tx Tx, e *models.Execution) error
	FindActiveTaskExecution(ctx context.Context, taskID string, excludeExecID string) (*models.Execution, error)
	FindActiveTaskThreadExecution(ctx context.Context, taskID string, excludeExecID string) (*models.Execution, error)
	FindLatestActiveChatExecution(ctx context.Context, projectID string) (*models.Execution, error)
	FindLatestActiveEmailChatExecution(ctx context.Context, projectID string, sessionKey string) (*models.Execution, error)
	GetAPIChatStatusByID(ctx context.Context, id string) (*models.Execution, error)
	GetAgentUsageByProject(ctx context.Context, projectID string, bounds ...string) ([]AgentUsage, error)
	GetAnalyticsDashboard(ctx context.Context, filter AnalyticsDashboardFilter) (models.AnalyticsDashboard, error)
	GetAvgExecutionTimeByAgent(ctx context.Context, projectID string, bounds ...string) ([]AvgExecutionTime, error)
	GetAvgExecutionTimeByTask(ctx context.Context, projectID string, limit int, bounds ...string) ([]AvgExecutionTime, error)
	GetByIDForProject(ctx context.Context, id string, projectID string) (*models.Execution, error)
	GetChatCompactionCheckpoint(ctx context.Context, scopeType string, scopeID string) (*models.ChatCompactionCheckpoint, error)
	GetChatContextUsage(ctx context.Context, scopeType string, scopeID string, modelKey string) (*models.ChatContextUsage, error)
	GetExecutionTrendsByHour(ctx context.Context, projectID string, dateFrom string, dateTo string, dimensions ...string) ([]ExecutionTrend, error)
	GetFailedTaskPatternsInRange(ctx context.Context, projectID string, limit int, bounds ...string) ([]FailedTaskPattern, error)
	GetLatestCompletedByTask(ctx context.Context, taskID string) (*models.Execution, error)
	GetLatestFailedFollowupByTask(ctx context.Context, taskID string) (*models.Execution, error)
	GetLatestNonEmptyDiffOutput(ctx context.Context, taskID string) (string, error)
	GetMostFrequentTasks(ctx context.Context, projectID string, limit int, bounds ...string) ([]TaskFrequency, error)
	GetSuccessFailureRates(ctx context.Context, projectID string, groupBy string, dateFrom string, dateTo string, dimensions ...string) ([]SuccessFailureRate, error)
	GetTaskExecutionMetrics(ctx context.Context, taskID string) (models.TaskExecutionMetrics, error)
	GetTaskRunActivity(ctx context.Context, f AnalyticsDashboardFilter) (TaskRunActivity, error)
	HasActiveTaskExecution(ctx context.Context, taskID string, excludeExecID string) (bool, error)
	HasActiveTaskThreadExecution(ctx context.Context, taskID string, excludeExecID string) (bool, error)
	IsTaskExecutionAtHistoryCutoff(ctx context.Context, taskID string, execID string, cutoff int64) (bool, error)
	ListByTask(ctx context.Context, taskID string) ([]models.Execution, error)
	ListByTaskChronological(ctx context.Context, taskID string) ([]models.Execution, error)
	ListByTaskChronologicalBefore(ctx context.Context, taskID string, beforeExecID string, limit int) ([]models.Execution, error)
	ListByTaskChronologicalLimit(ctx context.Context, taskID string, limit int) ([]models.Execution, error)
	ListByTaskChronologicalPage(ctx context.Context, taskID string, offset int, limit int) ([]models.Execution, error)
	ListChatHistory(ctx context.Context, projectID string, limit int) ([]models.Execution, error)
	ListChatHistoryBefore(ctx context.Context, projectID string, beforeExecID string, limit int) ([]models.Execution, error)
	ListEmailChatHistory(ctx context.Context, projectID string, sessionKey string, limit int) ([]models.Execution, error)
	MarkRunning(ctx context.Context, id string) error
	ReasoningContentByIDs(ctx context.Context, ids []string) (map[string]string, error)
	RecoverPreRestartRunningTaskExecutions(ctx context.Context) (int64, error)
	RecoverStaleRunningTaskExecutions(ctx context.Context) (int64, error)
	ReplaceReasoningReplay(ctx context.Context, id string, reasoningContent string, messages []models.ExecutionReplayMessage) error
	ReplayMessagesByExecutionIDs(ctx context.Context, ids []string) (map[string][]models.ExecutionReplayMessage, error)
	SaveChatContextUsage(ctx context.Context, scopeType string, scopeID string, modelKey string, usage models.ChatContextUsage) error
	SetAgentConfigIfEmpty(ctx context.Context, id string, agentConfigID string) error
	SuccessCompletionReadiness(ctx context.Context, id string) (CompleteSuccessOutcome, error)
	TaskExecutionHistoryCutoff(ctx context.Context, taskID string) (int64, error)
	UpdateDiffOutput(ctx context.Context, id string, diffOutput string) error
	UpsertChatCompactionCheckpoint(ctx context.Context, checkpoint models.ChatCompactionCheckpoint) error
	SaveInTurnCompactionCheckpoint(ctx context.Context, checkpoint models.ChatCompactionCheckpoint) error
	UpsertChatProviderSessionState(ctx context.Context, checkpoint models.ChatCompactionCheckpoint) error
	UpsertSwarmParentResult(ctx context.Context, parentTaskID string, mergerExecutionID string, output string, diffOutput string, durationMs int64) error
}

// GitHubAuthStore persists authorized GitHub users.
type GitHubAuthStore interface {
	DeleteAuthorizedActor(ctx context.Context, id string) error
	GetEnabledProjectInbox(ctx context.Context, projectID string) (*models.GitHubProjectInbox, error)
	GetProjectInbox(ctx context.Context, projectID string) (*models.GitHubProjectInbox, error)
	IsActorAuthorized(ctx context.Context, githubLogin string) (bool, error)
	ListAuthorizedActors(ctx context.Context) ([]models.GitHubAuthorizedActor, error)
	ListAuthorizedInboxAssignees(ctx context.Context) ([]models.GitHubAuthorizedActor, error)
	UpsertAuthorizedActor(ctx context.Context, actor *models.GitHubAuthorizedActor) error
	UpsertProjectInbox(ctx context.Context, inbox *models.GitHubProjectInbox) error
}

// GitHubPRFeedbackStore persists forwarded GitHub pull request feedback.
type GitHubPRFeedbackStore interface {
	AlreadyForwarded(ctx context.Context, repoFullName string, prNumber int, feedbackKind string, githubID string) (bool, error)
	RecordForwardedAndQueue(ctx context.Context, feedback *models.GitHubPRFeedbackForwarded, input *models.ThreadInput) (bool, error)
}

// InsightsStore persists project insights.
type InsightsStore interface {
	AvgConfidence(ctx context.Context, projectID string) (float64, error)
	CountByStatus(ctx context.Context, projectID string) (map[string]int, error)
	CountByType(ctx context.Context, projectID string) (map[string]int, error)
	CreateHealthCheck(ctx context.Context, hc *models.HealthCheck) error
	CreateIdeaGrade(ctx context.Context, ig *models.IdeaGrade) error
	CreateInsight(ctx context.Context, i *models.Insight) error
	CreateKnowledge(ctx context.Context, k *models.KnowledgeEntry) error
	CreateReport(ctx context.Context, rpt *models.InsightReport) error
	DeleteInsight(ctx context.Context, projectID string, id string) error
	DeleteKnowledge(ctx context.Context, projectID string, id string) error
	ExistingInsight(ctx context.Context, projectID string, title string, insightType models.InsightType) (bool, error)
	GetCompletedBugFixes(ctx context.Context, projectID string, limit int) ([]models.Task, error)
	GetFailedTaskPatterns(ctx context.Context, projectID string, minFailures int) ([]InsightFailedTaskPattern, error)
	GetInsight(ctx context.Context, projectID string, id string) (*models.Insight, error)
	GetLatestHealthCheck(ctx context.Context, projectID string) (*models.HealthCheck, error)
	GetLatestIdeaGrade(ctx context.Context, projectID string) (*models.IdeaGrade, error)
	GetLatestReport(ctx context.Context, projectID string) (*models.InsightReport, error)
	GetProjectTaskStats(ctx context.Context, projectID string) (total int, completed int, failed int, pending int, backlog int, err error)
	GetRecentCompletionRate(ctx context.Context, projectID string, days int) (float64, error)
	GetSlowExecutions(ctx context.Context, projectID string, minDurationSecs int, limit int) ([]struct {
		TaskTitle string
		Duration  int
		AgentName string
	}, error)
	GetTagDistribution(ctx context.Context, projectID string) (map[string]int, error)
	GetTaskActivityTrend(ctx context.Context, projectID string, days int) ([]struct {
		Date  string
		Count int
	}, error)
	GetTaskPriorityDistribution(ctx context.Context, projectID string) (map[int]int, error)
	LinkTask(ctx context.Context, projectID string, insightID string, taskID string) error
	ListByProject(ctx context.Context, projectID string, limit int) ([]models.Insight, error)
	ListByStatus(ctx context.Context, projectID string, status models.InsightStatus, limit int) ([]models.Insight, error)
	ListByType(ctx context.Context, projectID string, insightType models.InsightType, limit int) ([]models.Insight, error)
	ListHealthChecks(ctx context.Context, projectID string, limit int) ([]models.HealthCheck, error)
	ListIdeaGrades(ctx context.Context, projectID string, limit int) ([]models.IdeaGrade, error)
	ListKnowledge(ctx context.Context, projectID string, limit int) ([]models.KnowledgeEntry, error)
	ListReports(ctx context.Context, projectID string, limit int) ([]models.InsightReport, error)
	UpdateStatus(ctx context.Context, projectID string, id string, status models.InsightStatus) error
}

// OAuthRefreshStore is the narrow model configuration surface used to refresh and invalidate OAuth credentials.
type OAuthRefreshStore interface {
	GetByID(ctx context.Context, id string) (*models.LLMConfig, error)
	MarkLinkedOAuthConnectionNeedsReauthIfRevision(ctx context.Context, modelID string, connectionID string, expectedRevision int64, provider models.LLMProvider) (bool, error)
	MarkOAuthNeedsReauthIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider) (bool, error)
	ReleaseOAuthRefreshLease(ctx context.Context, configID string, ownerToken string) error
	TryAcquireOAuthRefreshLease(ctx context.Context, configID string, ownerToken string, now time.Time, leaseDuration time.Duration) (bool, error)
	UpdateLinkedOAuthConnectionTokensIfRevision(ctx context.Context, modelID string, connectionID string, expectedRevision int64, provider models.LLMProvider, accessToken string, refreshToken string, expiresAt int64, accountID ...string) (bool, error)
	UpdateStandardOAuthTokensIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider, accessToken string, refreshToken string, expiresAt int64, accountID ...string) (bool, error)
}

// OAuthConnectionStore persists shared OAuth connections and their links to model configurations.
type OAuthConnectionStore interface {
	AdoptUnrefreshableOpenAIConnectionIfPrincipalMatches(ctx context.Context, modelID string, sourceID string, expectedRevision int64, principalHash string) (bool, error)
	DeleteOAuthConnection(ctx context.Context, connectionID string) error
	DisconnectLinkedOAuthConnection(ctx context.Context, modelID string, connectionID string, expectedRevision int64, provider models.LLMProvider) (bool, error)
	GetOAuthConnectionByID(ctx context.Context, id string) (*models.OAuthConnection, error)
	ListOAuthConnections(ctx context.Context, provider models.LLMProvider) ([]models.OAuthConnection, error)
	MoveModelsToOAuthConnection(ctx context.Context, modelIDs []string, connectionID string) error
	OAuthConnectionSnapshots(ctx context.Context, provider models.LLMProvider) (map[string]models.OAuthCredentialSnapshot, error)
	RenameOAuthConnection(ctx context.Context, connectionID string, name string) error
	ReplaceLinkedOAuthConnectionWithProfileIfRevision(ctx context.Context, modelID string, connectionID string, expectedRevision int64, provider models.LLMProvider, accessToken string, refreshToken string, expiresAt int64, accountID string, displayName string, principalHash string) (bool, error)
	UpdateLinkedOAuthConnectionAccountIDIfRevision(ctx context.Context, modelID string, connectionID string, expectedRevision int64, provider models.LLMProvider, accountID string) (bool, error)
	UpdateLinkedOAuthConnectionProfileIfRevision(ctx context.Context, modelID string, connectionID string, expectedRevision int64, provider models.LLMProvider, accountID string, displayName string, principalHash string) (bool, error)
}

// LLMConfigStore persists model configurations.
type LLMConfigStore interface {
	OAuthRefreshStore
	OAuthConnectionStore

	AdvanceCustomOAuthRevision(ctx context.Context, id string) (int64, bool, error)
	Create(ctx context.Context, a *models.LLMConfig) error
	Delete(ctx context.Context, id string) error
	DeleteBulk(ctx context.Context, ids []string) error
	ExistsByID(ctx context.Context, id string) (bool, error)
	GetByIDs(ctx context.Context, ids []string) (map[string]*models.LLMConfig, error)
	GetDefault(ctx context.Context) (*models.LLMConfig, error)
	GetDefaultID(ctx context.Context) (*string, error)
	GetRuntimeSummary(ctx context.Context, id string, name string) (*models.LLMConfig, error)
	HasAny(ctx context.Context) (bool, error)
	List(ctx context.Context) ([]models.LLMConfig, error)
	ListBadgeOptions(ctx context.Context) ([]models.LLMConfig, error)
	ListCardsPageFiltered(ctx context.Context, limit int, offset int, filter ModelCardListFilter) ([]models.LLMConfig, error)
	ListChatSelectionOptions(ctx context.Context) ([]models.LLMConfig, error)
	ListMixtureDefinitions(ctx context.Context) ([]models.LLMConfig, error)
	ListModelCardOptions(ctx context.Context) ([]models.LLMConfig, error)
	ListPickerOptions(ctx context.Context) ([]models.LLMConfig, error)
	ListRefreshableOAuth(ctx context.Context) ([]models.LLMConfig, error)
	ListRuntimeSummaries(ctx context.Context) ([]models.LLMConfig, error)
	ListTaskCreationSelectionOptions(ctx context.Context) ([]models.LLMConfig, error)
	ListUnrefreshableOpenAIOAuth(ctx context.Context) ([]UnrefreshableOpenAIOAuth, error)
	ListVisionSelectionOptions(ctx context.Context) ([]models.LLMConfig, error)
	ListWorkerCapacities(ctx context.Context) ([]models.LLMConfig, error)
	OAuthProviderPresence(ctx context.Context) (OAuthProviderPresence, error)
	TransferDefaultAndDelete(ctx context.Context, deleteID string, newDefaultID string) error
	Update(ctx context.Context, a *models.LLMConfig) error
	UpdateCustomOAuthConnectionIfRevision(ctx context.Context, id string, expectedRevision int64, accessToken string, refreshToken string, expiresAt int64, stateJSON string) (bool, error)
	UpdateCustomOAuthTokensIfRevision(ctx context.Context, id string, expectedRevision int64, accessToken string, refreshToken string, expiresAt int64) (bool, error)
	UpdateOAuthAccountIDIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider, accountID string) (bool, error)
	UpdateStandardOAuthConnectionIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider, accessToken string, refreshToken string, expiresAt int64, accountID string) (bool, error)
	ValidateNameAvailable(ctx context.Context, name string, excludeID string) (string, error)
}

// LifecycleStore persists lifecycle runs, steps, and approvals.
type LifecycleStore interface {
	CreateExecution(ctx context.Context, e *models.LifecycleExecution) error
	CreateHook(ctx context.Context, h *models.AgentLifecycleHook) error
	DeleteHook(ctx context.Context, id string) error
	FindExecutionByIdempotencyKey(ctx context.Context, key string) (*models.LifecycleExecution, error)
	GetExecutionForTaskProject(ctx context.Context, taskID string, executionID string, projectID string) (*models.LifecycleExecution, bool, error)
	HooksByAgent(ctx context.Context, agentID string) ([]models.AgentLifecycleHook, error)
	HooksForWhen(ctx context.Context, when models.LifecycleWhen) ([]models.AgentLifecycleHook, error)
	ListExecutionEventsForProject(ctx context.Context, lifecycleExecutionID string, projectID string) ([]models.LifecycleExecutionEvent, bool, error)
	ListExecutionsForTaskNewerPage(ctx context.Context, taskID string, limit int, afterValue string) (models.LifecycleExecutionPage, error)
	ListExecutionsForTaskPage(ctx context.Context, taskID string, limit int, cursorValue string) (models.LifecycleExecutionPage, error)
	PatchExecutionOutputSkills(ctx context.Context, execID string, handles []string) error
	TaskRunFreshness(ctx context.Context, taskID string, taskRunID string) (LifecycleTaskRunFreshness, error)
	UpdateExecution(ctx context.Context, e *models.LifecycleExecution) error
	UpdateHook(ctx context.Context, h *models.AgentLifecycleHook) error
}

// OpenAIAsyncToolCallStore persists OpenAI asynchronous tool call state.
type OpenAIAsyncToolCallStore interface {
	ClaimForRun(ctx context.Context, id string, now time.Time) (bool, error)
	ClaimRecoverableForRun(ctx context.Context, id string, now time.Time) (bool, error)
	CreatePending(ctx context.Context, call *models.OpenAIAsyncToolCall) (*models.OpenAIAsyncToolCall, error)
	ExpireDue(ctx context.Context, now time.Time) (int64, error)
	ListRecoverableByExecution(ctx context.Context, executionID string, now time.Time, limit int) ([]models.OpenAIAsyncToolCall, error)
	MarkCompleted(ctx context.Context, id string, result string, isError bool, now time.Time) error
	MarkDelivered(ctx context.Context, id string, now time.Time) (bool, error)
	MarkProviderRejected(ctx context.Context, id string, message string, now time.Time) error
	MarkStale(ctx context.Context, id string, message string, now time.Time) error
}

// ProjectStore persists projects.
type ProjectStore interface {
	Create(ctx context.Context, p *models.Project) error
	DeleteWithCleanupManifest(ctx context.Context, id string, beforeDelete func(TaskDeletionManifest) error) (manifest TaskDeletionManifest, deleted bool, err error)
	ForEachRepoRoot(ctx context.Context, visit func(ProjectRepoRoot)) error
	GetByID(ctx context.Context, id string) (*models.Project, error)
	GetDefaultAgentConfigID(ctx context.Context, id string) (*string, error)
	GetIdentityByID(ctx context.Context, id string) (*models.ProjectIdentity, error)
	List(ctx context.Context) ([]models.Project, error)
	ListAPIProjects(ctx context.Context) ([]models.ProjectAPIItem, error)
	ListRepoValidationProjects(ctx context.Context) ([]ProjectRepoValidationProject, error)
	ListSelectorOptions(ctx context.Context) ([]models.Project, error)
	ListWorkerCapacityProjects(ctx context.Context) ([]models.ProjectWorkerCapacity, error)
	Update(ctx context.Context, p *models.Project) error
}

// ReviewCommentStore persists task review comments.
type ReviewCommentStore interface {
	Create(ctx context.Context, c *models.ReviewComment) error
	Delete(ctx context.Context, id string) error
	DeleteByTask(ctx context.Context, taskID string) error
	ListByTask(ctx context.Context, taskID string) ([]models.ReviewComment, error)
	UpdateText(ctx context.Context, id string, commentText string) error
}

// ScheduleStore persists task schedules.
type ScheduleStore interface {
	AdvanceSuppressedOccurrence(ctx context.Context, schedule models.Schedule, now time.Time) (bool, error)
	ApplyCalendarAction(ctx context.Context, projectID string, action models.ScheduleCalendarAction, now time.Time) (models.ScheduleCalendarAction, error)
	CalendarState(ctx context.Context, projectID string) (models.ScheduleCalendarState, error)
	Create(ctx context.Context, s *models.Schedule) error
	Delete(ctx context.Context, id string) error
	DeleteOrphan(ctx context.Context, id string, taskID string) error
	GetByID(ctx context.Context, id string) (*models.Schedule, error)
	ListByProject(ctx context.Context, projectID string) ([]models.Schedule, error)
	ListByTask(ctx context.Context, taskID string) ([]models.Schedule, error)
	ListCalendarAdmissions(ctx context.Context) ([]ActiveLaneTaskAdmission, error)
	ListDue(ctx context.Context, now time.Time) ([]models.Schedule, error)
	ListSchedulesForDiscovery(ctx context.Context, projectID string, filter ScheduleDiscoveryFilter) ([]ScheduleDiscoveryRow, int, error)
	SetEnabledForTask(ctx context.Context, id string, taskID string, enabled bool) (*models.Schedule, error)
	ToggleEnabledForTask(ctx context.Context, id string, taskID string) (*models.Schedule, error)
	Update(ctx context.Context, s *models.Schedule) error
	UpdateBatchForProject(ctx context.Context, projectID string, scheduleIDs []string, move func(*models.Schedule) error) error
	UpdateClearContextOnStart(ctx context.Context, id string, taskID string, clearContextOnStart bool) error
	UpdateForTask(ctx context.Context, s *models.Schedule, taskID string) error
	UpdateNextRunIfCurrent(ctx context.Context, id string, taskID string, expected *time.Time, nextRun *time.Time) (bool, error)
}

// SettingsStore persists key/value application settings.
type SettingsStore interface {
	CompareAndSet(ctx context.Context, key string, expected string, value string, guards map[string]string) (bool, error)
	CompareAndSetMany(ctx context.Context, expected map[string]string, values map[string]string, guards map[string]string) (bool, error)
	Get(ctx context.Context, key string) (string, error)
	GetMany(ctx context.Context, keys []string) (map[string]string, error)
	MatchesInTx(ctx context.Context, tx Tx, expected map[string]string) (bool, error)
	RemoveChannelsBulk(ctx context.Context, projectID string, values map[string]string, webhookIDs []string, removeDiscordAuthorization bool) error
	Set(ctx context.Context, key string, value string) error
	SetMany(ctx context.Context, values map[string]string) error
}

// SkillAnalyticsStore persists skill usage analytics events.
type SkillAnalyticsStore interface {
	BuildDashboard(ctx context.Context, filter SkillAnalyticsFilter, enabledSkills []EnabledSkillInfo) (*models.SkillAnalyticsDashboard, error)
	RecordEvent(ctx context.Context, event *models.SkillAnalyticsEvent) error
}

// SlackAuthStore persists authorized Slack users.
type SlackAuthStore interface {
	CountByProject(ctx context.Context, projectID string) (int, error)
	Create(ctx context.Context, u *models.SlackAuthorizedUser) error
	DeleteForProject(ctx context.Context, projectID string, id string) error
	GetByID(ctx context.Context, id string) (*models.SlackAuthorizedUser, error)
	IsAuthorized(ctx context.Context, projectID string, slackUserID string) (bool, error)
	IsAuthorizedAnywhere(ctx context.Context, slackUserID string) (bool, error)
	ListByProject(ctx context.Context, projectID string) ([]models.SlackAuthorizedUser, error)
}

// SlackInboundReceiptStore persists inbound Slack event deduplication receipts.
type SlackInboundReceiptStore interface {
	Exists(ctx context.Context, eventKey string) (bool, error)
	WithHandoff(ctx context.Context, eventKey string, persist func(Tx) error) (alreadyHandedOff bool, err error)
}

// SlackTaskContextStore persists Slack reply context for tasks.
type SlackTaskContextStore interface {
	GetByTaskID(ctx context.Context, taskID string) (*models.SlackTaskContext, error)
	Upsert(ctx context.Context, stc *models.SlackTaskContext) error
	UpsertInTx(ctx context.Context, tx Tx, stc *models.SlackTaskContext) error
}

// SlackUserProjectStore persists per-user Slack project selection.
type SlackUserProjectStore interface {
	GetUserProject(ctx context.Context, teamID string, userID string) (string, error)
	SetUserProject(ctx context.Context, teamID string, userID string, projectID string) error
}

// TaskCommitStatStore persists per-task commit statistics.
type TaskCommitStatStore interface {
	FirstProducedCommitStatTime(ctx context.Context, projectID string) (time.Time, error)
	ForEachProducedCommitStatChangedFilesJSON(ctx context.Context, projectID string, since time.Time, fn func(changedFilesJSON string) error) error
	ForEachProducedCommitStatSubject(ctx context.Context, projectID string, since time.Time, fn func(subject string) error) error
	ListProducedCommitStatCommits(ctx context.Context, projectID string, since time.Time, limit int) ([]models.GitCommit, error)
	ProducedCommitStatAggregate(ctx context.Context, projectID string, since time.Time) (ProducedCommitStatAggregate, error)
	UpsertProducedCommitStat(ctx context.Context, stat *models.TaskCommitStat) error
}

// TaskGoalStore persists task goals.
type TaskGoalStore interface {
	Clear(ctx context.Context, taskID string, reason string) error
	ClearBlockedReport(ctx context.Context, taskID string, blockerKey string, reason string) (*models.TaskGoal, error)
	CreateOrReplace(ctx context.Context, goal *models.TaskGoal) error
	GetByTaskID(ctx context.Context, taskID string) (*models.TaskGoal, error)
	MarkAchieved(ctx context.Context, taskID string, goalID string, reason string) (*models.TaskGoal, error)
	ReactivateAchieved(ctx context.Context, taskID string, reason string) (*models.TaskGoal, error)
	RecordBlockedReport(ctx context.Context, taskID string, goalID string, blockerKey string, reason string) (*models.TaskGoal, error)
	UpdateStatus(ctx context.Context, taskID string, goalID string, status models.TaskGoalStatus, reason string, clearAudit bool) (*models.TaskGoal, error)
}

// TaskPullRequestStore persists task pull request records.
type TaskPullRequestStore interface {
	GetByTaskID(ctx context.Context, taskID string) (*models.TaskPullRequest, error)
	ListOpenByProjectID(ctx context.Context, projectID string) ([]models.TaskPullRequest, error)
	SetNeedsRepublish(ctx context.Context, taskID string, needsRepublish bool) error
	Upsert(ctx context.Context, pr *models.TaskPullRequest) error
}

// TaskStore persists tasks and their lifecycle state.
type TaskStore interface {
	ActivateAllBacklog(ctx context.Context, projectID string) (int, error)
	ActivateAutomationChainedTask(ctx context.Context, parent models.Task, child *models.Task, event AutomationProjectionEvent) (*models.AutomationWorkItem, *models.AutomationActivity, bool, error)
	ActiveMergeConflictOwner(ctx context.Context, projectID string) (string, error)
	AdmitScheduledCalendarOccurrence(ctx context.Context, schedule models.Schedule, now time.Time, next *time.Time) (bool, error)
	ApplyModelEffort(ctx context.Context, taskID string, agent *models.LLMConfig) error
	ClaimAutomationDispatch(ctx context.Context, dispatchID string, claimant string) (*models.Execution, error)
	ClaimQueuedAutomationDispatch(ctx context.Context, dispatchID string) (*QueuedAutomationDispatchClaim, error)
	ClaimReservedTaskForDispatch(ctx context.Context, id string, executionID string) (*TaskDispatchClaim, bool, error)
	ClaimTask(ctx context.Context, id string) (bool, error)
	ClaimTaskForDispatch(ctx context.Context, id string) (*TaskDispatchClaim, bool, error)
	ClearActiveMergeConflictOwner(ctx context.Context, taskID string) error
	ClearWorktreeInfo(ctx context.Context, id string) error
	CompleteExecutionSuccess(ctx context.Context, executionID string, output string, tokensUsed int, durationMs int64) (CompleteSuccessOutcome, error)
	ConfirmAutomationChainedTaskAdmission(ctx context.Context, projectID string, binding models.AutomationBinding, taskID string) (bool, error)
	CountBacklogByPriority(ctx context.Context, projectID string) (map[int]int, error)
	CountByProjectAndCategory(ctx context.Context, projectID string) (map[string]int, error)
	CountPendingByProject(ctx context.Context) (map[string]int, error)
	CountProjectStatus(ctx context.Context, projectID string) (ProjectTaskStatusCounts, error)
	Create(ctx context.Context, t *models.Task) error
	CreateInTx(ctx context.Context, tx Tx, t *models.Task) error
	CreateWithGoalAndCallback(ctx context.Context, t *models.Task, goal *models.TaskGoal, callback func(Tx) error) error
	Delete(ctx context.Context, id string) error
	DeleteBlockedChildrenByParent(ctx context.Context, parentTaskID string) error
	DeleteWithCleanupManifest(ctx context.Context, id string, beforeDelete func(TaskDeletionManifest) error) (TaskDeletionManifest, bool, error)
	DeleteWithCleanupManifestIfCategory(ctx context.Context, id string, projectID string, category models.TaskCategory, beforeDelete func(TaskDeletionManifest) error) (TaskDeletionManifest, bool, error)
	DeleteWithCleanupManifestIfObserved(ctx context.Context, task *models.Task, beforeDelete func(TaskDeletionManifest) error) (TaskDeletionManifest, bool, error)
	FilterNonChatTaskIDs(ctx context.Context, ids []string) (map[string]bool, error)
	FinalizeExecutionCancellation(ctx context.Context, taskID string, executionID string) (bool, error)
	FindBlockedChildByParent(ctx context.Context, parentTaskID string) (*models.Task, error)
	FindChainChildByParent(ctx context.Context, parentTaskID string) (*models.Task, error)
	FindSwarmChildByRole(ctx context.Context, parentTaskID string, role models.SwarmRole) (*models.Task, error)
	GetByID(ctx context.Context, id string) (*models.Task, error)
	GetByIDForProject(ctx context.Context, projectID string, id string) (*models.Task, error)
	GetByProjectAndTitle(ctx context.Context, projectID string, title string) (*models.Task, error)
	GetDetailActionMetadata(ctx context.Context, id string) (*models.TaskDetailActionMetadata, error)
	GetTaskForSwarmInspection(ctx context.Context, projectID string, taskID string, title string) (*models.Task, error)
	GetThreadRenderMetadata(ctx context.Context, id string) (*models.Task, error)
	HasNonTerminalDescendants(ctx context.Context, parentID string) (bool, error)
	HasPendingAutomationDispatch(ctx context.Context, taskID string) (bool, error)
	ListActivePendingAdmissions(ctx context.Context) ([]ActiveTaskAdmission, error)
	ListAutomationReusableTasks(ctx context.Context, projectID string, limit int) ([]models.Task, error)
	ListBacklogByPriority(ctx context.Context, projectID string, priority int) ([]models.Task, error)
	ListBoardByProjectWithCategorySorts(ctx context.Context, projectID string, category string, backlogSort string, completedSort string) ([]models.Task, error)
	ListBreadcrumbSelector(ctx context.Context, projectID string, search string, currentID string, scheduleOnly bool, limit int) ([]models.BreadcrumbSelectorItem, error)
	ListByProject(ctx context.Context, projectID string, category string) ([]models.Task, error)
	ListByProjectWithCategorySorts(ctx context.Context, projectID string, category string, backlogSort string, completedSort string) ([]models.Task, error)
	ListByTags(ctx context.Context, tags []models.TaskTag, projectID string, category models.TaskCategory, minPriority int, status models.TaskStatus) ([]models.Task, error)
	ListChatContextByProject(ctx context.Context, projectID string) ([]ChatTaskContextRow, error)
	ListReservedActiveLaneAdmissions(ctx context.Context) ([]ActiveLaneTaskAdmission, error)
	ListRunningChatTaskIDs(ctx context.Context, projectID string) ([]string, error)
	ListStaleQueuedTasks(ctx context.Context, staleDuration time.Duration) ([]models.Task, error)
	ListSwarmChildren(ctx context.Context, parentTaskID string) ([]models.Task, error)
	ListSwarmChildrenForInspection(ctx context.Context, projectID string, parentTaskID string) ([]models.Task, error)
	ListTaskReferences(ctx context.Context, projectID string) ([]TaskReference, error)
	ListTasksForDiscovery(ctx context.Context, projectID string, filter TaskDiscoveryFilter) ([]models.Task, int, error)
	ListWithSchedulesByProject(ctx context.Context, projectID string) ([]TaskWithSchedule, error)
	ListWithWorktrees(ctx context.Context) ([]models.Task, error)
	ModelEffort(ctx context.Context, taskID string, agent models.LLMConfig) (string, error)
	MoveCancelledToBacklogIfStillCancelled(ctx context.Context, id string) (bool, error)
	MoveCompletedActiveToCompleted(ctx context.Context, projectID string) (int, error)
	MoveTasksToActiveLane(ctx context.Context, projectID string, moves []ActiveLaneTaskMove, status models.TaskStatus) ([]ActiveLaneTaskAdmission, error)
	NormalizeProjectedActiveTerminalTasks(ctx context.Context, projectID string) ([]ProjectedTaskCategoryChange, error)
	PrepareSwarmParentForActiveLane(ctx context.Context, id string) error
	ReclaimStaleQueuedTask(ctx context.Context, id string, staleDuration time.Duration) (bool, error)
	ReorderTask(ctx context.Context, taskID string, newPosition int) error
	ReserveCalendarPlanner(ctx context.Context, parentID string, makePlanner func(*models.Task) (*models.Task, error)) (*ActiveLaneTaskAdmission, error)
	ResetOrphanedRunning(ctx context.Context) (int, error)
	RestoreBoardState(ctx context.Context, task models.Task) error
	SearchByTitle(ctx context.Context, projectID string, titleQuery string) ([]models.Task, error)
	SetActiveMergeConflictOwner(ctx context.Context, projectID string, taskID string) error
	SetModelEffort(ctx context.Context, taskID string, agent models.LLMConfig, effort string) error
	SetPendingIfNotRunningOrQueued(ctx context.Context, id string) (bool, error)
	SetPendingIfNotRunningOrQueuedForEnabledSchedule(ctx context.Context, id string, scheduleID string) (bool, error)
	TaskExecutionHistoryCutoff(ctx context.Context, taskID string) (int64, error)
	Update(ctx context.Context, t *models.Task) error
	UpdateAgentDefinition(ctx context.Context, taskID string, agentDefinitionID *string) error
	UpdateAgentID(ctx context.Context, id string, agentID string) error
	UpdateAutoMerge(ctx context.Context, id string, autoMerge bool, targetBranch string) error
	UpdateAutoMergeSettings(ctx context.Context, id string, autoMerge bool, autoMergeOnGoalAchieved *bool, targetBranch string) error
	UpdateCategory(ctx context.Context, id string, category models.TaskCategory) error
	UpdateDetailProperty(ctx context.Context, id string, field string, value string) error
	UpdateDiscordOrigin(ctx context.Context, id string) error
	UpdateEmailOrigin(ctx context.Context, id string) error
	UpdateMergeStatus(ctx context.Context, id string, status models.MergeStatus) error
	UpdateSlackOrigin(ctx context.Context, id string) error
	UpdateStatus(ctx context.Context, id string, status models.TaskStatus) error
	UpdateSwarmFields(ctx context.Context, id string, role models.SwarmRole, status string, config string, sequence int) error
	UpdateTelegramOrigin(ctx context.Context, id string, chatID int64) error
	UpdateWorktreeInfo(ctx context.Context, id string, worktreePath string, branch string) error
	UpdateXOrigin(ctx context.Context, id string) error
	UpdateXOriginInTx(ctx context.Context, tx Tx, id string) error
}

// TelegramAuthStore persists authorized Telegram users.
type TelegramAuthStore interface {
	BackfillUserID(ctx context.Context, projectID string, username string, userID int64) error
	CountByProject(ctx context.Context, projectID string) (int, error)
	Create(ctx context.Context, u *models.TelegramAuthorizedUser) error
	DeleteForProject(ctx context.Context, projectID string, id string) error
	GetByID(ctx context.Context, id string) (*models.TelegramAuthorizedUser, error)
	IsAuthorized(ctx context.Context, projectID string, telegramUserID int64, username string) (bool, error)
	IsAuthorizedAnywhere(ctx context.Context, telegramUserID int64, username string) (bool, error)
	ListByProject(ctx context.Context, projectID string) ([]models.TelegramAuthorizedUser, error)
}

// TelegramUserProjectStore persists per-user Telegram project selection.
type TelegramUserProjectStore interface {
	GetUserProject(ctx context.Context, telegramUserID string) (string, error)
	SetUserProject(ctx context.Context, telegramUserID string, projectID string) error
}

// ThreadInputStore persists queued task thread and chat inputs.
type ThreadInputStore interface {
	ListAppliedSteeringByExecution(ctx context.Context, executionID string) ([]models.ThreadInput, error)
	BindPreExecutionQueuedTaskInputs(ctx context.Context, taskID string, executionID string) error
	CancelPending(ctx context.Context, id string) (*models.ThreadInput, error)
	CancelPendingForChat(ctx context.Context, projectID string) error
	CancelPendingForTask(ctx context.Context, taskID string) error
	ClaimQueuedForChatExecution(ctx context.Context, inputID string, task *models.Task, exec *models.Execution, slackContext *models.SlackTaskContext, emailContext *models.EmailTaskContext, discordContext *models.DiscordTaskContext) error
	ClaimQueuedForTaskExecutionWithModel(ctx context.Context, inputID string, exec *models.Execution, expectedAgentID *string, agent *models.LLMConfig) error
	ClearProviderSteering(ctx context.Context, steeringIDs []string) error
	CommitLocalSteering(ctx context.Context, commit LocalSteeringCommit) error
	ConvertQueuedToSteering(ctx context.Context, id string, runExecutionID string, expectedTurnID string) (*models.ThreadInput, error)
	CreateQueued(ctx context.Context, input *models.ThreadInput) error
	CreateQueuedInTx(ctx context.Context, tx Tx, input *models.ThreadInput) error
	CreateQueuedWithAutomationContext(ctx context.Context, input *models.ThreadInput, automationContext models.AutomationContext, bindingKey string) error
	CreateSteeringForActiveExecution(ctx context.Context, input *models.ThreadInput, activeExecutionID string) error
	EditPending(ctx context.Context, id string, content string, tokens ...string) (*models.ThreadInput, error)
	FindOldestQueuedForChat(ctx context.Context, projectID string) (*models.ThreadInput, error)
	FindOldestQueuedForTask(ctx context.Context, taskID string) (*models.ThreadInput, error)
	GetByID(ctx context.Context, id string) (*models.ThreadInput, error)
	HasPendingSteering(ctx context.Context, execID string) (bool, error)
	IsAttachmentSessionRetired(ctx context.Context, sessionID string) (bool, error)
	ListPendingForChat(ctx context.Context, projectID string) ([]models.ThreadInput, error)
	ListPendingForTask(ctx context.Context, taskID string) ([]models.ThreadInput, error)
	ListRecoverableQueuedChatProjectIDsAfter(ctx context.Context, afterProjectID string, limit int) ([]string, error)
	ListRecoverableQueuedTaskIDsAfter(ctx context.Context, afterTaskID string, limit int) ([]string, error)
	MarkApplied(ctx context.Context, id string, runExecutionID string, turnID string) error
	MarkProviderSteeringAmbiguous(ctx context.Context, steeringIDs []string) error
	PreparePendingSteering(ctx context.Context, runExecutionID string, turnID string) ([]models.ThreadInput, error)
	PreparePendingTextSteering(ctx context.Context, runExecutionID string, turnID string) ([]models.ThreadInput, error)
	ReleaseExpiredEditHolds(ctx context.Context) ([]models.ThreadInput, error)
	RenewEditHold(ctx context.Context, id string, tokens ...string) error
	RequeuePendingSteering(ctx context.Context, ids []string, runExecutionID string) ([]models.ThreadInput, error)
	RequeuePendingSteeringForExecution(ctx context.Context, runExecutionID string) ([]models.ThreadInput, error)
	RestorePreparedSteering(ctx context.Context, ids []string, runExecutionID string, expectedTurnID string) error
	RetireAttachmentSessionIfUnowned(ctx context.Context, sessionID string) (retired bool, err error)
	SetEditHold(ctx context.Context, id string, hold bool, tokens ...string) (*models.ThreadInput, error)
	SubscribeSteeringWakeups(activeExecutionID string) (<-chan struct{}, func())
}

// UpcomingStore persists the upcoming-work agenda projection.
type UpcomingStore interface {
	GetHistorySummary(ctx context.Context, projectID string, since time.Time) (models.HistorySummary, error)
	GetTaskSummary(ctx context.Context, projectID string, now time.Time) (*models.TaskSummary, error)
	ListBlockedTasks(ctx context.Context, projectID string) ([]models.UpcomingTask, error)
	ListRecentExecutions(ctx context.Context, projectID string, since time.Time) ([]models.HistoryExecution, error)
	ListRunningTasks(ctx context.Context, projectID string) ([]models.UpcomingTask, error)
	ListUpcomingScheduledTasks(ctx context.Context, projectID string, until time.Time) ([]models.UpcomingTask, error)
	ListWaitingActiveTasks(ctx context.Context, projectID string) ([]models.UpcomingTask, error)
}

// UsageStore persists model usage records and account limits.
type UsageStore interface {
	CreateAccountUsageSnapshotIfOAuthRevision(ctx context.Context, snapshot *models.AccountUsageSnapshot, configID string, expectedRevision int64, provider models.LLMProvider) (bool, error)
	GetDailyUsage(ctx context.Context, filter UsageFilter) ([]models.DailyUsagePoint, error)
	GetDailyUsageByModel(ctx context.Context, filter UsageFilter) ([]models.DailyUsagePoint, error)
	GetEvidence(ctx context.Context, filter UsageFilter) ([]models.UsageEvidenceRow, int, error)
	GetLatestAccountUsageSnapshots(ctx context.Context, provider string) ([]models.AccountUsageSnapshot, error)
	GetLatestUsageEventTime(ctx context.Context, filter UsageFilter) (*time.Time, error)
	GetModelUsageBreakdown(ctx context.Context, filter UsageFilter) ([]models.ModelUsagePoint, error)
	GetUsagePage(ctx context.Context, filter UsageFilter) (*models.AnalyticsUsageViewModel, error)
	GetUsageRateBuckets(ctx context.Context, filter UsageFilter) ([]models.UsageRatePoint, error)
	GetUsageRateBucketsByModel(ctx context.Context, filter UsageFilter) ([]models.UsageRatePoint, error)
	GetUsageTotals(ctx context.Context, filter UsageFilter) (*models.UsageTotals, error)
	RecordUsageEvent(ctx context.Context, event *models.LLMUsageEvent) error
}

// WebhookStore persists webhook endpoints and deliveries.
type WebhookStore interface {
	Create(ctx context.Context, w *models.WebhookEndpoint) error
	DeleteBulk(ctx context.Context, projectID string, ids []string) error
	DeleteForProject(ctx context.Context, projectID string, id string) error
	GetByID(ctx context.Context, id string) (*models.WebhookEndpoint, error)
	GetByIDForProject(ctx context.Context, id string, projectID string) (*models.WebhookEndpoint, error)
	GetByPathToken(ctx context.Context, pathToken string) (*models.WebhookEndpoint, error)
	GetEndpointAgents(ctx context.Context, endpointID string) ([]models.WebhookEndpointAgent, error)
	ListCardsByProject(ctx context.Context, projectID string) ([]models.WebhookEndpoint, error)
	ListCardsByProjectPageFiltered(ctx context.Context, projectID string, limit int, offset int, filter WebhookCardFilter) ([]models.WebhookEndpoint, error)
	RotateSecret(ctx context.Context, id string) (string, error)
	SetEndpointAgents(ctx context.Context, endpointID string, agentIDs []string) error
	SetTaskAgentAssignments(ctx context.Context, taskID string, agentIDs []string) error
	Update(ctx context.Context, w *models.WebhookEndpoint) error
}

// WorkerStore persists worker capacity settings.
type WorkerStore interface {
	CountQueuedWork(ctx context.Context) (int, error)
	GetMaxWorkers(ctx context.Context) (int, error)
	SetMaxWorkers(ctx context.Context, n int) error
}

// XAuthStore persists authorized X users.
type XAuthStore interface {
	CountByProject(ctx context.Context, projectID string) (int, error)
	Create(ctx context.Context, u *models.XAuthorizedUser) error
	Delete(ctx context.Context, projectID string, id string) error
	FirstAuthorizedProject(ctx context.Context, userID string) (string, error)
	IsAuthorized(ctx context.Context, projectID string, userID string) (bool, error)
	ListByProject(ctx context.Context, projectID string) ([]models.XAuthorizedUser, error)
}

// XInboundReceiptStore persists inbound X mention claims.
type XInboundReceiptStore interface {
	Claim(ctx context.Context, tweetID string, projectID string, now time.Time, lease time.Duration) (XReceiptClaim, error)
	CompleteWithHandoff(ctx context.Context, tweetID string, token string, taskID *string, persist func(Tx) error) (alreadyHandedOff bool, err error)
	Release(ctx context.Context, tweetID string, token string) error
}

// XTaskContextStore persists X reply context for tasks.
type XTaskContextStore interface {
	GetByTaskID(ctx context.Context, taskID string) (*models.XTaskContext, error)
	Upsert(ctx context.Context, v *models.XTaskContext) error
	UpsertInTx(ctx context.Context, tx Tx, v *models.XTaskContext) error
}

// XUserProjectStore persists per-user X project selection.
type XUserProjectStore interface {
	GetUserProject(ctx context.Context, userID string) (string, error)
	SetUserProject(ctx context.Context, userID string, projectID string) error
}
