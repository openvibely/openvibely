package remote

// readMethods are the store methods a saved server may call remotely. Each one
// only reads; a method missing here never runs on another server.
var readMethods = map[string]map[string]bool{
	"Agents": {
		"GetByID": true, "GetByKey": true, "GetByKeyIncludingArchived": true, "GetByName": true, "GetBySystemKind": true, "GetTaskDetailAgentLabel": true, "GetUniqueSelectableByName": true, "ListAgentListSummaries": true,
		"ListByName": true, "ListBySystemKind": true, "ListChatAssignableDefinitions": true, "ListIncludingArchived": true, "ListPage": true, "ListPageFiltered": true, "ListPickerOptionsForProject": true, "ListRuntimeSummaries": true, "ListScheduleOptions": true, "ListSelectableForProject": true, "ListSelectableReferencesForProject": true, "ListSkillCatalogRefs": true, "ListTaskUIOptions": true,
	},
	"Alerts": {
		"AlertOwnedByAutomation": true, "CountPending": true, "CountUnread": true, "GetByIDAdmin": true,
		"GetByIDForProject": true, "GetByIdempotencyKey": true, "ListByProject": true, "ListFiltered": true,
		"ListFilteredSummaries": true, "ListOwnerProjectIDs": true, "ListSummariesByProject": true,
		"NativeInboxBindings": true,
	},
	"Attachments": {
		"GeneratedFile": true, "GetByID": true, "GetByIDForProject": true, "ListByExecutionIDs": true, "ListByTask": true, "ListGeneratedFiles": true, "ListUploadedFiles": true,
	},
	"Automations": {
		"AutomationExternalState": true, "BindingsForActivityResource": true, "BindingsForExecutionResource": true,
		"ContextForExecution": true, "ContextForTask": true, "ContextForThreadInput": true,
		"CurrentActiveBindingForLaunchNode": true, "CurrentActiveBindingForNodeKey": true, "Exists": true,
		"GetAutomationConfirmationReceipt": true, "GetAutomationGraphMetadata": true, "GetAutomationInvocation": true,
		"GetAutomationMetrics": true, "GetAutomationWorkItem": true, "GetByStableKey": true, "GetConnectedNodeByRole": true, "GetCustomNotificationHandoff": true, "GetDefinition": true, "GetDefinitionVersion": true,
		"GetDispatchEnvelope": true, "GetNodeByKey": true, "GetPendingAutomationConfirmation": true,
		"GetRuntimeAutomationCard": true, "GetTriggerOwner": true, "GitHubIssueResourceForTask": true,
		"GitHubIssueTaskProvenance": true, "HasAutomationConfirmationInput": true, "IsCurrentActiveBinding": true,
		"ListAbandonedQueuedDispatches": true, "ListAutomationActivities": true, "ListAutomationInvocationNodeIDs": true, "ListAutomationInvocations": true, "ListAutomationPullRequests": true, "ListAutomationTransitions": true, "ListAutomationWorkItems": true, "ListAutomationsWithStaleExternalPullRequests": true,
		"ListBreadcrumbSelector": true, "ListCustomTaskHandoffs": true, "ListExecutionProjectionRepairs": true,
		"ListNodeRuntimeResources": true, "ListPortfolioCards": true, "ListPortfolioCardsPageFiltered": true,
		"ListRecoverablePreparedDispatches": true, "ListResourceSummaries": true, "ListRuntimeAutomationCards": true,
		"ListTerminalUnfinalizedDispatches": true, "LiveEdgeCounts": true, "LiveNodeCounts": true,
		"PortfolioOperationalCounts": true, "PortfolioOperationalCountsForAutomations": true,
		"ProjectScheduleResource": true, "ProjectTaskResource": true, "ReplayAutomationTransitionPage": true,
	},
	"ChannelTargets": {
		"FindByName": true, "FindByTarget": true, "FindByTargetAndKind": true, "FindHome": true, "GetByID": true,
		"ListByProject": true, "ListByProjectPage": true, "SummarizeByProject": true,
	},
	"ChatAttachments": {
		"GetByIDForProject": true, "ListByExecution": true, "ListByExecutionIDs": true, "ListByProject": true,
		"ListByTask": true,
	},
	"ChatInputRequests": {
		"Get": true, "ListActiveByProject": true, "ListByExecutions": true,
		"ListInstallationAnswersAwaitingChatRecovery": true, "ListReadyInstallationAnswers": true,
		"LoadInstallationContinuation": true,
	},
	"Executions": {
		"ConversationRevision": true, "CountByTask": true, "FindActiveTaskExecution": true,
		"FindActiveTaskThreadExecution": true, "FindLatestActiveChatExecution": true,
		"FindLatestActiveEmailChatExecution": true, "GetAPIChatStatusByID": true, "GetAgentUsageByProject": true,
		"GetAnalyticsDashboard": true, "GetAvgExecutionTimeByAgent": true, "GetAvgExecutionTimeByTask": true,
		"GetByID": true, "GetByIDForProject": true, "GetChatCompactionCheckpoint": true,
		"GetChatCompactionCheckpointIfChanged": true, "GetChatContextUsage": true, "GetDisplayExecution": true,
		"GetDisplayExecutionTail": true, "GetExecutionTaskID": true, "GetExecutionTrendsByHour": true,
		"GetFailedTaskPatternsInRange": true, "GetLatestCompletedByTask": true, "GetLatestFailedFollowupByTask": true,
		"GetLatestNonEmptyDiffOutput": true, "GetMostFrequentTasks": true, "GetSuccessFailureRates": true,
		"GetTaskExecutionMetrics": true, "GetTaskRunActivity": true, "HasActiveTaskExecution": true,
		"HasActiveTaskThreadExecution": true, "IsTaskExecutionAtHistoryCutoff": true, "ListByTask": true,
		"ListByTaskChronological": true, "ListByTaskChronologicalBefore": true, "ListByTaskChronologicalBetween": true, "ListByTaskChronologicalLimit": true, "ListByTaskChronologicalPage": true, "ListChatHistory": true,
		"ListChatHistoryBefore": true, "ListChatModelHistoryBefore": true, "ListDisplayExecutions": true,
		"ListEmailChatHistory": true, "ListLiveExecutions": true, "ReadConversationChanges": true,
		"ReadConversationHeads": true, "ReadConversationPage": true, "ReadOutputAfter": true,
		"ReadProjectDisplayTail": true, "ReadProjectDisplayTailRevision": true, "ReplayContinuationRowsByExecutionIDs": true, "ReplayMessagesByExecutionIDs": true, "ReplayRowsByExecutionIDs": true, "TaskExecutionHistoryCutoff": true,
	},
	"Insights": {
		"GetCompletedBugFixes": true, "GetFailedTaskPatterns": true,
		"GetInsight":        true,
		"GetSlowExecutions": true,
		"ListByType":        true, "ListReports": true,
	},
	"LLMConfigs": {
		"ExistsByID": true, "GetByID": true, "GetByIDs": true, "GetDefault": true, "GetDefaultID": true,
		"GetOAuthConnectionByID": true, "GetRuntimeSummary": true, "HasAny": true, "List": true, "ListBadgeOptions": true, "ListCardsPageFiltered": true, "ListChatSelectionOptions": true, "ListMixtureDefinitions": true,
		"ListModelCardOptions": true, "ListOAuthConnections": true, "ListPickerOptions": true, "ListRefreshableOAuth": true, "ListRuntimeSummaries": true, "ListTaskCreationSelectionOptions": true, "ListUnrefreshableOpenAIOAuth": true, "ListVisionSelectionOptions": true, "ListWorkerCapacities": true, "OAuthProviderPresence": true,
	},
	"Lifecycle": {
		"FindExecutionByIdempotencyKey": true, "GetExecutionForTaskProject": true, "ListExecutionEventsForProject": true, "ListExecutionsForTaskNewerPage": true, "ListExecutionsForTaskPage": true,
	},
	"ProjectAccess": {
		"Role": true,
	},
	"Projects": {
		"GetByID": true, "GetDefaultAgentConfigID": true, "GetIdentityByID": true, "GetManagedPermissions": true,
		"List": true, "ListAPIProjects": true, "ListRepoValidationProjects": true, "ListSelectorOptions": true,
		"ListWorkerCapacityProjects": true,
	},
	"ReviewComments": {
		"ListByTask": true,
	},
	"Schedules": {
		"GetByID": true, "ListByProject": true, "ListByTask": true, "ListCalendarAdmissions": true, "ListDue": true,
		"ListSchedulesForDiscovery": true,
	},
	"TaskGoals": {
		"GetByTaskID": true,
	},
	"TaskPullRequests": {
		"GetByTaskID": true, "ListOpenByProjectID": true,
	},
	"Tasks": {
		"ListCustomTags":         true,
		"CountBacklogByPriority": true, "CountByProjectAndCategory": true, "CountPendingByProject": true,
		"CountPendingForProject": true, "CountProjectStatus": true, "FilterNonChatTaskIDs": true,
		"FindBlockedChildByParent": true, "FindChainChildByParent": true, "FindSwarmChildByRole": true, "GetByID": true, "GetByIDForProject": true, "GetByProjectAndTitle": true, "GetDetailActionMetadata": true,
		"GetTaskForSwarmInspection": true, "GetThreadRenderMetadata": true, "HasNonTerminalDescendants": true,
		"HasPendingAutomationDispatch": true, "IsPageConversation": true, "ListActivePendingAdmissions": true,
		"ListAutomationReusableTasks": true, "ListBacklogByPriority": true, "ListBoardByProjectWithCategorySorts": true, "ListBreadcrumbSelector": true, "ListByProject": true, "ListByProjectWithCategorySorts": true,
		"ListByTags": true, "ListChatContextByProject": true, "ListReservedActiveLaneAdmissions": true,
		"ListRunningChatTaskIDs": true, "ListStaleQueuedTasks": true, "ListSwarmChildren": true,
		"ListSwarmChildrenForInspection": true, "ListTaskReferences": true, "ListTasksForDiscovery": true,
		"ListWithSchedulesByProject": true, "ListWithWorktrees": true, "ModelEffort": true, "ModelServiceTier": true,
		"SearchByTitle": true, "TaskExecutionHistoryCutoff": true,
	},
	"ThreadInputs": {
		"FindOldestQueuedForChat": true, "FindOldestQueuedForTask": true, "GetByID": true, "HasPendingSteering": true,
		"IsAttachmentSessionRetired": true, "ListAppliedSteeringByExecution": true, "ListPendingForChat": true,
		"ListPendingForTask": true, "ListRecoverableQueuedChatProjectIDsAfter": true,
		"ListRecoverableQueuedTaskIDsAfter": true,
	},
	"Upcoming": {
		"GetHistorySummary": true, "GetTaskSummary": true, "ListBlockedTasks": true, "ListRecentExecutions": true,
		"ListRunningTasks": true, "ListUpcomingScheduledTasks": true, "ListWaitingActiveTasks": true,
	},
	"Usage": {
		"GetDailyUsage": true, "GetDailyUsageByModel": true, "GetEvidence": true, "GetLatestUsageEventTime": true,
		"GetModelUsageBreakdown": true, "GetUsagePage": true, "GetUsageRateBuckets": true,
		"GetUsageRateBucketsByModel": true, "GetUsageTotals": true,
	},
	"Webhooks": {
		"GetByID": true, "GetByIDForProject": true, "GetByPathToken": true, "GetEndpointAgents": true,
		"ListCardsByProject": true, "ListCardsByProjectPageFiltered": true,
	},
	"Workers": {
		"CountQueuedWork": true, "GetMaxWorkers": true,
	},
}

// IsRead reports whether store.method is a remotely callable read.
func IsRead(store, method string) bool { return readMethods[store][method] }
