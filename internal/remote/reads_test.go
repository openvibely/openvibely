package remote

import (
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/persistence"
)

// writeMethods are store methods that must never run for another server.
var writeMethods = map[string][]string{
	"Agents": {
		"Create", "Delete", "DeleteBulk", "MarkArchived", "Update",
	},
	"Alerts": {
		"ClaimApproved", "Create", "CreateIdempotent", "CreateImplementationTask", "Delete", "DeleteAll",
		"DeleteBulk", "LinkImplementationTask", "MarkAllRead", "MarkProcessing", "MarkRead", "MarkReadBulk",
		"RebindAlertToAutomationInbox", "ReleaseClaim", "SetDecision",
	},
	"Attachments": {
		"CleanupOrphanedFiles", "Create", "CreateGeneratedFile", "CreateInTx", "Delete", "DeleteByIDForProject",
	},
	"Automations": {
		"AcquireGitHubIssueDedupLease", "CancelDispatchesForTaskReturningIDs", "ClaimManualAutomationRun",
		"ClaimScheduledOccurrence", "CompleteDispatch", "CompleteGitHubIssueDedupLease",
		"CreateAutomationConfirmationReceipt", "CreateOrGetGitHubIssueTask", "DeleteAutomation", "DeleteAutomations",
		"FailDispatch", "LeaseNextDispatch", "MarkAutomationConfirmationInput", "MarkDispatchQueued",
		"MarkDispatchSubmitted", "MarkGitHubIssueDedupDispatched", "PruneTerminalizedAutomationPositions",
		"PublishInvalidation", "PublishRegistered", "RecomputeAutomationHealth", "RecomputeAutomationHealthForAll",
		"ReconcileInvocationCompletions", "RecordProjectionEvent", "ReleaseExternalActivityReservation",
		"ReleaseGitHubIssueDedupLease", "RepairExecutionProjection", "ReserveExternalActivity", "ResumeAutomation",
		"SaveCurrentGraph", "SetAutomationLifecycle",
	},
	"ChannelTargets": {
		"RecordSend", "ReplaceProjectTargets",
	},
	"ChatAttachments": {
		"CleanupOrphanedFiles", "Create", "CreateInTx", "Delete", "DeleteByIDForProject",
	},
	"ChatInputRequests": {
		"ClaimInstallationAnswer", "Create", "Resolve", "SaveInstallationContinuationResult",
	},
	"CustomPersonalities": {
		"Create", "Delete", "DeleteBulk", "Update",
	},
	"Executions": {
		"AppendConversationChanges", "AppendOutput", "CancelActiveByTaskThroughHistoryOrderReturningIDs",
		"CancelQueuedOrdinaryByTaskReturningIDs", "Complete", "Create", "CreateDirectTaskFollowupOrQueue",
		"CreateInTx", "FinalizeOutput", "InvalidateDiffOutput", "MarkRunning", "ReasoningContentByIDs",
		"RecoverPreRestartRunningTaskExecutions", "RecoverStaleRunningTaskExecutions", "ReplaceReasoningReplay",
		"SaveChatContextUsage", "SaveInTurnCompactionCheckpoint", "SetAgentConfigIfEmpty",
		"SuccessCompletionReadiness", "UpdateDiffOutput", "UpsertChatCompactionCheckpoint",
		"UpsertChatProviderSessionState", "UpsertSwarmParentResult",
	},
	"Insights": {
		"CreateHealthCheck", "CreateIdeaGrade", "CreateInsight", "CreateKnowledge", "CreateReport",
		"DeleteInsight", "DeleteKnowledge", "ExistingInsight", "UpdateStatus",
	},
	"LLMConfigs": {
		"AdoptUnrefreshableOpenAIConnectionIfPrincipalMatches", "AdvanceCustomOAuthRevision", "Create", "Delete",
		"DeleteBulk", "DeleteOAuthConnection", "DisconnectLinkedOAuthConnection",
		"MarkLinkedOAuthConnectionNeedsReauthIfRevision", "MarkOAuthNeedsReauthIfRevision",
		"MoveModelsToOAuthConnection", "OAuthConnectionSnapshots", "ReleaseOAuthRefreshLease",
		"RenameOAuthConnection", "ReplaceLinkedOAuthConnectionWithProfileIfRevision", "TransferDefaultAndDelete",
		"TryAcquireOAuthRefreshLease", "Update", "UpdateCustomOAuthConnectionIfRevision",
		"UpdateCustomOAuthTokensIfRevision", "UpdateLinkedOAuthConnectionAccountIDIfRevision",
		"UpdateLinkedOAuthConnectionProfileIfRevision", "UpdateLinkedOAuthConnectionTokensIfRevision",
		"UpdateOAuthAccountIDIfRevision", "UpdateStandardOAuthConnectionIfRevision",
		"UpdateStandardOAuthTokensIfRevision", "ValidateNameAvailable",
	},
	"Lifecycle": {
		"CreateExecution", "CreateHook", "DeleteHook", "HooksByAgent", "HooksForWhen", "PatchExecutionOutputSkills",
		"TaskRunFreshness", "UpdateExecution", "UpdateHook",
	},
	"ProjectAccess": {
		"Audit", "ChatResourceAllowed", "ChatResourceOwner", "Get", "Members", "Record", "SetMember", "SetMembers",
		"Update",
	},
	"Projects": {
		"Computers", "Create", "DeleteWithCleanupManifest", "ForEachRepoRoot", "Update",
	},
	"ReviewComments": {
		"DeleteSubmitted",
		"Create", "Delete", "DeleteByTask", "DeleteForProject", "UpdateText", "UpdateTextForProject",
	},
	"Schedules": {
		"AdvanceSuppressedOccurrence", "ApplyCalendarAction", "CalendarState", "Create", "Delete", "DeleteOrphan",
		"SetEnabledForTask", "ToggleEnabledForTask", "Update", "UpdateBatchForProject", "UpdateClearContextOnStart",
		"UpdateForTask", "UpdateNextRunIfCurrent",
	},
	"SkillAnalytics": {
		"BuildDashboard", "RecordEvent",
	},
	"TaskCommitStats": {
		"FirstProducedCommitStatTime", "ForEachProducedCommitStatChangedFilesJSON",
		"ForEachProducedCommitStatSubject", "ListProducedCommitStatCommits", "ProducedCommitStatAggregate",
		"UpsertProducedCommitStat",
	},
	"TaskGoals": {
		"Clear", "ClearBlockedReport", "CreateOrReplace", "MarkAchieved", "ReactivateAchieved", "RecordBlockedReport",
		"UpdateStatus",
	},
	"TaskPullRequests": {
		"SetNeedsRepublish", "Upsert",
	},
	"Tasks": {
		"ActivateAllBacklog", "ActivateAutomationChainedTask", "ActiveMergeConflictOwner",
		"AdmitScheduledCalendarOccurrence", "ApplyModelEffort", "ApplySwarmPlan", "ClaimAutomationDispatch",
		"ClaimQueuedAutomationDispatch", "ClaimReservedTaskForDispatch", "ClaimTask", "ClaimTaskForDispatch",
		"ClearActiveMergeConflictOwner", "ClearWorktreeInfo", "CompleteExecutionSuccess",
		"ConfirmAutomationChainedTaskAdmission", "Create", "CreateInTx", "CreateWithGoalAndCallback", "Delete",
		"DeleteBlockedChildrenByParent", "DeleteWithCleanupManifest", "DeleteWithCleanupManifestIfCategory",
		"DeleteWithCleanupManifestIfObserved", "FinalizeExecutionCancellation",
		"MoveCancelledToBacklogIfStillCancelled", "MoveCompletedActiveToCompleted", "MoveTasksToActiveLane",
		"NormalizeProjectedActiveTerminalTasks", "PreparePlannerFollowup", "PrepareSwarmParentForActiveLane",
		"PrepareSwarmRerun", "PrepareSwarmSuccessor", "ReclaimStaleQueuedTask", "ReorderTask", "ReorderTaskRelative",
		"ReserveCalendarPlanner", "ResetOrphanedRunning", "RestoreBoardState", "SetActiveMergeConflictOwner",
		"SetModelEffort", "SetModelOverrides", "SetModelServiceTier", "SetPendingIfNotRunningOrQueued",
		"SetPendingIfNotRunningOrQueuedForEnabledSchedule", "TaskModelSettings", "Update", "UpdateAgentDefinition",
		"UpdateAgentID", "UpdateAutoMerge", "UpdateAutoMergeSettings", "UpdateCategory", "UpdateDetailProperty",
		"UpdateDiscordOrigin", "UpdateEmailOrigin", "UpdateGeneratedTitle", "UpdateMergeStatus",
		"UpdateMergeTargetBranch", "UpdatePreservingTitle", "UpdateSlackOrigin", "UpdateStatus", "UpdateSwarmFields",
		"UpdateTelegramOrigin", "UpdateWorktreeInfo", "UpdateXOrigin", "UpdateXOriginInTx",
	},
	"ThreadInputs": {
		"BindPreExecutionQueuedTaskInputs", "CancelPending", "CancelPendingForChat", "CancelPendingForTask",
		"CheckSteeringDelivery", "ClaimQueuedForChatExecution", "ClaimQueuedForPageExecution",
		"ClaimQueuedForTaskExecutionWithModel", "ClearProviderSteering", "CommitLocalSteering",
		"ConvertQueuedToSteering", "CreateQueued", "CreateQueuedInTx", "CreateQueuedWithAutomationContext",
		"CreateSteeringForActiveExecution", "EditPending", "MarkApplied", "MarkProviderSteeringAmbiguous",
		"PreparePendingSteering", "PreparePendingTextSteering", "ReleaseExpiredEditHolds", "RenewEditHold",
		"RequeuePendingSteering", "RequeuePendingSteeringForExecution", "RestorePreparedSteering",
		"RetireAttachmentSessionIfUnowned", "SetEditHold", "SubscribeSteeringWakeups",
	},
	"Usage": {
		"RecordUsageEvent",
	},
	"Webhooks": {
		"Create", "DeleteBulk", "DeleteForProject", "RotateSecret", "SetEndpointAgents", "SetTaskAgentAssignments",
		"Update",
	},
	"Workers": {
		"SetMaxWorkers",
	},
}

// TestEveryRoutableMethodIsClassified fails when a routable store gains a method
// that is neither an allowed read nor a known write.
func TestEveryRoutableMethodIsClassified(t *testing.T) {
	stores := reflect.TypeOf(persistence.Stores{})
	for i := 0; i < stores.NumField(); i++ {
		f := stores.Field(i)
		if f.Type.Kind() != reflect.Interface || !routable(f.Name) {
			continue
		}
		writes := map[string]bool{}
		for _, m := range writeMethods[f.Name] {
			writes[m] = true
		}
		for j := 0; j < f.Type.NumMethod(); j++ {
			m := f.Type.Method(j).Name
			read := IsRead(f.Name, m)
			switch {
			case read && writes[m]:
				t.Errorf("%s.%s is listed as both a read and a write", f.Name, m)
			case !read && !writes[m]:
				t.Errorf("%s.%s is unclassified: add it to readMethods (reads.go) only if it never writes, otherwise to writeMethods", f.Name, m)
			}
		}
	}
}

func TestReadMethodsAreKnown(t *testing.T) {
	for store, methods := range readMethods {
		if !routable(store) {
			t.Errorf("%s is not a routable store", store)
		}
		for m := range methods {
			if _, ok := paramNames[store][m]; !ok {
				t.Errorf("%s.%s has no parameter names", store, m)
			}
		}
	}
}
