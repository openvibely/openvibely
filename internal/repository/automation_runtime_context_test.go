package repository

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAutomationRepoRuntimeGraphContextAndResources(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{
		"trigger": "trigger",
		"task":    "task",
		"github":  "github_inbox",
		"notify":  "create_notification",
	})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	agentID := defaultAgentConfigID(t, ctx, db)
	implementation := createRuntimeContextTask(t, ctx, taskRepo, fixture.ProjectID, "Implementation")
	parent := createRuntimeContextTask(t, ctx, taskRepo, fixture.ProjectID, "Parent")
	child := createRuntimeContextTask(t, ctx, taskRepo, fixture.ProjectID, "Child")
	child.ParentTaskID = &parent.ID
	if err := taskRepo.Update(ctx, child); err != nil {
		t.Fatalf("link child to parent: %v", err)
	}
	executionID := "runtime-context-exec"
	if _, err := db.ExecContext(ctx, `INSERT INTO executions (id, task_id, agent_config_id, status, prompt_sent)
		VALUES (?, ?, ?, 'running', 'go')`, executionID, implementation.ID, agentID); err != nil {
		t.Fatalf("insert execution: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_edges
		(id, project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, display_order)
		VALUES
		('runtime-edge-task', ?, ?, ?, ?, ?, 'trigger-to-task', 0),
		('runtime-edge-github', ?, ?, ?, ?, ?, 'trigger-to-github', 1),
		('runtime-edge-notify', ?, ?, ?, ?, ?, 'task-to-notify', 0)`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"], fixture.Nodes["task"],
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"], fixture.Nodes["github"],
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], fixture.Nodes["notify"]); err != nil {
		t.Fatalf("insert automation edges: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE automation_nodes SET node_type = 'action'
		WHERE project_id = ? AND automation_id = ? AND version_id = ? AND id = ?`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["notify"]); err != nil {
		t.Fatalf("mark notify node as action: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_definition_resources
		(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
		VALUES (?, ?, ?, ?, 'task', ?, 'owned')`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], implementation.ID); err != nil {
		t.Fatalf("insert definition resource: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status)
		VALUES ('runtime-context-invocation', ?, ?, ?, ?, 'manual', 'run-now', 'manual:runtime-context', 'running')`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"]); err != nil {
		t.Fatalf("insert invocation: %v", err)
	}
	binding := models.AutomationBinding{
		AutomationID: fixture.AutomationID,
		VersionID:    fixture.VersionID,
		InvocationID: "runtime-context-invocation",
		NodeID:       fixture.Nodes["task"],
	}
	workItem, activity, err := repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context:       models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding:       binding,
		WorkItemKey:   "github:owner/repo:issue:42",
		WorkItemKind:  "github_issue",
		WorkItemTitle: "Issue 42",
		ActivityKey:   "work-item:github:owner/repo:issue:42:implementation-task",
		ActivityType:  "create_task",
		Resources: []models.AutomationActivityResource{
			{ResourceType: "task", ResourceID: implementation.ID, Relation: "child"},
			{ResourceType: "task", ResourceID: parent.ID, Relation: "parent"},
			{ResourceType: "execution", ResourceID: executionID, Relation: "run"},
			{ResourceType: "github_issue", ResourceID: "github:owner/repo:issue:42", Relation: "source"},
			{ResourceType: "pull_request", ResourceID: "github:owner/repo:pull:7", Relation: "output"},
			{ResourceType: "review", ResourceID: "github:owner/repo:review:7:99", Relation: "feedback"},
		},
		ActivityStatus: models.AutomationActivityRunning,
	})
	if err != nil {
		t.Fatalf("RecordProjectionEvent: %v", err)
	}
	if _, _, err := repo.RecordProjectionEvent(ctx, AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: fixture.ProjectID},
		Binding: models.AutomationBinding{
			AutomationID: fixture.AutomationID,
			VersionID:    fixture.VersionID,
			InvocationID: "runtime-context-invocation",
			NodeID:       fixture.Nodes["task"],
			WorkItemID:   workItem.ID,
		},
		ActivityKey:    "work-item:" + workItem.ID + ":implementation-task",
		ActivityType:   "create_task",
		ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{
			{ResourceType: "task", ResourceID: implementation.ID, Relation: "child"},
			{ResourceType: "github_issue", ResourceID: "github:owner/repo:issue:42", Relation: "source"},
		},
	}); err != nil {
		t.Fatalf("record provenance activity: %v", err)
	}

	node, err := repo.GetNodeByKey(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, "task")
	if err != nil || node == nil || node.ID != fixture.Nodes["task"] {
		t.Fatalf("GetNodeByKey = %#v, %v", node, err)
	}
	taskConnected, err := repo.GetConnectedNodeByRole(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"], "task", true)
	if err != nil || taskConnected == nil || taskConnected.ID != fixture.Nodes["task"] {
		t.Fatalf("GetConnectedNodeByRole outgoing = %#v, %v", taskConnected, err)
	}
	triggerConnected, err := repo.GetConnectedNodeByRole(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], "trigger", false)
	if err != nil || triggerConnected == nil || triggerConnected.ID != fixture.Nodes["trigger"] {
		t.Fatalf("GetConnectedNodeByRole incoming = %#v, %v", triggerConnected, err)
	}
	if ok, err := repo.IsCurrentActiveBinding(ctx, fixture.ProjectID, binding); err != nil || !ok {
		t.Fatalf("IsCurrentActiveBinding = %v, %v", ok, err)
	}
	current, ok, err := repo.CurrentActiveBindingForNodeKey(ctx, fixture.ProjectID, fixture.AutomationID, "trigger", "task")
	if err != nil || !ok || current.NodeID != fixture.Nodes["trigger"] || current.VersionID != fixture.VersionID {
		t.Fatalf("CurrentActiveBindingForNodeKey = %#v, %v, %v", current, ok, err)
	}
	launched, ok, err := repo.CurrentActiveBindingForLaunchNode(ctx, fixture.ProjectID, models.AutomationBinding{
		AutomationID: fixture.AutomationID,
		VersionID:    fixture.VersionID,
		NodeID:       fixture.Nodes["trigger"],
		InvocationID: "runtime-context-invocation",
	}, "task")
	if err != nil || !ok || launched.NodeID != fixture.Nodes["trigger"] {
		t.Fatalf("CurrentActiveBindingForLaunchNode = %#v, %v, %v", launched, ok, err)
	}

	custom, handoffs, err := repo.ListCustomTaskHandoffs(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"])
	if err != nil || !custom || len(handoffs) != 1 || handoffs[0].TaskID != implementation.ID {
		t.Fatalf("ListCustomTaskHandoffs = %v %#v, %v", custom, handoffs, err)
	}
	custom, handoffNode, taskID, err := repo.GetCustomTaskHandoff(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["trigger"])
	if err != nil || !custom || handoffNode == nil || taskID != implementation.ID {
		t.Fatalf("GetCustomTaskHandoff = %v %#v %q, %v", custom, handoffNode, taskID, err)
	}
	notify, err := repo.GetCustomNotificationHandoff(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"])
	if err != nil || notify == nil || notify.ID != fixture.Nodes["notify"] {
		t.Fatalf("GetCustomNotificationHandoff = %#v, %v", notify, err)
	}

	page, err := repo.ListNodeRuntimeResources(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], 3, "")
	if err != nil {
		t.Fatalf("ListNodeRuntimeResources first page: %v", err)
	}
	if len(page.Items) != 3 || page.NextCursor == "" {
		t.Fatalf("first resource page = %#v, want 3 items and next cursor", page)
	}
	nextPage, err := repo.ListNodeRuntimeResources(ctx, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["task"], 10, page.NextCursor)
	if err != nil {
		t.Fatalf("ListNodeRuntimeResources second page: %v", err)
	}
	if len(nextPage.Items) < 3 {
		t.Fatalf("second resource page length = %d, want at least 3", len(nextPage.Items))
	}
	resourceURLs := make(map[string]string, len(page.Items)+len(nextPage.Items))
	foundImplementationTaskURL := false
	for _, item := range append(page.Items, nextPage.Items...) {
		resourceURLs[item.ResourceType] = item.URL
		if item.ResourceType == "task" && item.ResourceID == implementation.ID && item.URL == "/tasks/"+implementation.ID {
			foundImplementationTaskURL = true
		}
	}
	if !foundImplementationTaskURL ||
		resourceURLs["execution"] != "/executions/"+executionID ||
		resourceURLs["github_issue"] != "https://github.com/owner/repo/issues/42" ||
		resourceURLs["pull_request"] != "https://github.com/owner/repo/pull/7" ||
		resourceURLs["review"] != "https://github.com/owner/repo/pull/7#pullrequestreview-99" {
		t.Fatalf("resource URLs = %#v", resourceURLs)
	}

	activityContext, err := repo.BindingsForActivityResource(ctx, fixture.ProjectID, fixture.AutomationID, "task", implementation.ID)
	if err != nil || len(activityContext.Bindings) != 1 || activityContext.Bindings[0].WorkItemID != workItem.ID {
		t.Fatalf("BindingsForActivityResource = %#v, %v", activityContext, err)
	}
	if got, err := repo.FindActivityResource(ctx, fixture.ProjectID, binding, activity.ActivityKey, "github_issue"); err != nil || got != "github:owner/repo:issue:42" {
		t.Fatalf("FindActivityResource = %q, %v", got, err)
	}
	workContext, err := repo.BindingsForWorkItemKey(ctx, fixture.ProjectID, "github:owner/repo:issue:42")
	if err != nil || len(workContext.Bindings) != 1 || workContext.Bindings[0].WorkItemID != workItem.ID {
		t.Fatalf("BindingsForWorkItemKey = %#v, %v", workContext, err)
	}
	executionContext, err := repo.ContextForExecution(ctx, fixture.ProjectID, executionID)
	if err != nil || len(executionContext.Bindings) != 1 || executionContext.Bindings[0].WorkItemID != workItem.ID {
		t.Fatalf("ContextForExecution = %#v, %v", executionContext, err)
	}
	resourceContext, err := repo.BindingsForExecutionResource(ctx, fixture.ProjectID, executionID, "task", implementation.ID)
	if err != nil || len(resourceContext.Bindings) != 1 || resourceContext.Bindings[0].WorkItemID != workItem.ID {
		t.Fatalf("BindingsForExecutionResource = %#v, %v", resourceContext, err)
	}
	taskContext, err := repo.ContextForTask(ctx, fixture.ProjectID, child.ID)
	if err != nil || len(taskContext.Bindings) != 1 || taskContext.Bindings[0].WorkItemID != workItem.ID {
		t.Fatalf("ContextForTask child lineage = %#v, %v", taskContext, err)
	}
	provenance, err := repo.GitHubIssueTaskProvenance(ctx, fixture.ProjectID, implementation.ID)
	if err != nil || provenance == nil || provenance.IssueResourceID != "github:owner/repo:issue:42" {
		t.Fatalf("GitHubIssueTaskProvenance = %#v, %v", provenance, err)
	}
	if issue, err := repo.GitHubIssueResourceForTask(ctx, fixture.ProjectID, implementation.ID); err != nil || issue != "github:owner/repo:issue:42" {
		t.Fatalf("GitHubIssueResourceForTask = %q, %v", issue, err)
	}

	inputID := "runtime-context-input"
	input := &models.ThreadInput{
		ID:            inputID,
		Scope:         models.ThreadInputScopeTask,
		ProjectID:     fixture.ProjectID,
		TaskID:        implementation.ID,
		AgentConfigID: agentID,
		Content:       "follow up",
	}
	if err := NewThreadInputRepo(db).CreateQueued(ctx, input); err != nil {
		t.Fatalf("CreateQueued: %v", err)
	}
	inputID = input.ID
	if err := repo.BindThreadInput(ctx, inputID, activityContext, "runtime"); err != nil {
		t.Fatalf("BindThreadInput: %v", err)
	}
	inputContext, err := repo.ContextForThreadInput(ctx, fixture.ProjectID, inputID)
	if err != nil || !reflect.DeepEqual(inputContext.Bindings, activityContext.Bindings) {
		t.Fatalf("ContextForThreadInput = %#v, want %#v, err %v", inputContext, activityContext, err)
	}
}

func TestAutomationRuntimeResourceURLCoverage(t *testing.T) {
	tests := map[string]struct {
		resourceType string
		resourceID   string
		want         string
	}{
		"alert":         {"alert", "alert id", "/alerts?project_id=proj+id&alert_id=alert+id"},
		"goal":          {"goal", "task/goal", "/tasks/task%2Fgoal?project_id=proj+id#task-goal-panel"},
		"invalid issue": {"github_issue", "github:owner:issue:42", ""},
		"unknown":       {"something_else", "id", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := automationRuntimeResourceURL("proj id", tt.resourceType, tt.resourceID); got != tt.want {
				t.Fatalf("automationRuntimeResourceURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAutomationGitHubIssueDedupSourceComparisonIgnoresBindingOrder(t *testing.T) {
	left := AutomationGitHubIssueDedupSource{
		Context: models.AutomationContext{ProjectID: " project ", Bindings: []models.AutomationBinding{
			{AutomationID: "auto", VersionID: "v1", NodeID: "n1"},
			{AutomationID: "auto", VersionID: "v1", NodeID: "n2"},
		}},
		TaskID:      " task ",
		ExecutionID: "one",
	}
	right := AutomationGitHubIssueDedupSource{
		Context: models.AutomationContext{ProjectID: "project", Bindings: []models.AutomationBinding{
			{AutomationID: "auto", VersionID: "v1", NodeID: "n2"},
			{AutomationID: "auto", VersionID: "v1", NodeID: "n1"},
		}},
		TaskID:      "task",
		ExecutionID: "two",
	}
	if !sameAutomationGitHubIssueDedupSource(left, right) {
		t.Fatal("expected equivalent sources with bindings in different order")
	}
	right.Context.Bindings[0].NodeID = "other"
	if sameAutomationGitHubIssueDedupSource(left, right) {
		t.Fatal("expected different node bindings to be non-equivalent")
	}
}

func TestAutomationRuntimeSmallHelpers(t *testing.T) {
	due := time.Date(2026, 8, 14, 12, 0, 0, 1, time.FixedZone("offset", -4*60*60))
	if got := automationOccurrenceKey("sched", due); got != "schedule:sched:2026-08-14T16:00:00.000000001Z" {
		t.Fatalf("automationOccurrenceKey = %q", got)
	}
	for _, status := range []models.AutomationInvocationStatus{
		models.AutomationInvocationCompleted,
		models.AutomationInvocationFailed,
		models.AutomationInvocationCancelled,
		models.AutomationInvocationSkipped,
	} {
		if !automationInvocationTerminal(status) {
			t.Fatalf("%s should be terminal", status)
		}
	}
	if automationInvocationTerminal(models.AutomationInvocationRunning) {
		t.Fatal("running should not be terminal")
	}
}

func TestAutomationRepoScheduledDispatchLifecycle(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Scheduled lifecycle")
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], due)

	owner, err := repo.GetTriggerOwner(ctx, schedule.ID)
	if err != nil || owner == nil || owner.NodeID != fixture.Nodes["trigger"] {
		t.Fatalf("GetTriggerOwner = %#v, %v", owner, err)
	}
	invocation, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("ClaimScheduledOccurrence: %v", err)
	}
	if invocation.Status != models.AutomationInvocationClaimed || dispatch == nil || dispatch.TaskID != task.ID {
		t.Fatalf("claim result invocation=%#v dispatch=%#v", invocation, dispatch)
	}
	repeatedInvocation, repeatedDispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), nil)
	if err != nil {
		t.Fatalf("ClaimScheduledOccurrence repeated: %v", err)
	}
	if repeatedInvocation.ID != invocation.ID || repeatedDispatch == nil || repeatedDispatch.ID != dispatch.ID {
		t.Fatalf("repeated claim invocation=%#v dispatch=%#v", repeatedInvocation, repeatedDispatch)
	}

	now := time.Now().UTC()
	leased, err := repo.LeaseNextDispatch(ctx, "owner", now, time.Minute)
	if err != nil || leased == nil || leased.ID != dispatch.ID || leased.Attempts != 1 {
		t.Fatalf("LeaseNextDispatch = %#v, %v", leased, err)
	}
	execution, err := taskRepo.ClaimAutomationDispatch(ctx, leased.ID, "owner")
	if err != nil {
		t.Fatalf("ClaimAutomationDispatch: %v", err)
	}
	envelope, err := repo.GetDispatchEnvelope(ctx, leased.ID)
	if err != nil || envelope == nil || envelope.Task.ID != task.ID || len(envelope.Context.Bindings) != 1 {
		t.Fatalf("GetDispatchEnvelope = %#v, %v", envelope, err)
	}
	if err := repo.RenewDispatchLease(ctx, leased.ID, "not-owner", now.Add(2*time.Minute)); !errors.Is(err, ErrAutomationDispatchLease) {
		t.Fatalf("RenewDispatchLease wrong owner err = %v", err)
	}
	if err := repo.RenewDispatchLease(ctx, leased.ID, "owner", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("RenewDispatchLease: %v", err)
	}
	if err := repo.MarkDispatchSubmitted(ctx, leased.ID, "not-owner", execution.ID); !errors.Is(err, ErrAutomationDispatchLease) {
		t.Fatalf("MarkDispatchSubmitted wrong owner err = %v", err)
	}
	if err := repo.MarkDispatchSubmitted(ctx, leased.ID, "owner", execution.ID); err != nil {
		t.Fatalf("MarkDispatchSubmitted: %v", err)
	}
	if err := repo.CompleteDispatch(ctx, leased.ID, execution.ID, models.ExecCompleted, "done"); err != nil {
		t.Fatalf("CompleteDispatch: %v", err)
	}
	if changed, err := repo.ReconcileInvocationCompletions(ctx, 10); err != nil || changed != 0 {
		t.Fatalf("ReconcileInvocationCompletions = %d, %v", changed, err)
	}
}

func TestAutomationRepoDispatchRetryAndAbandonQueued(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	now := time.Now().UTC().Truncate(time.Second)

	retryTask := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Retry dispatch")
	retrySchedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, retryTask.ID, fixture.Nodes["trigger"], now.Add(-time.Minute))
	_, retryDispatch, err := repo.ClaimScheduledOccurrence(ctx, retrySchedule, now, nil)
	if err != nil {
		t.Fatalf("claim retry schedule: %v", err)
	}
	leased, err := repo.LeaseNextDispatch(ctx, "retry-owner", now, time.Minute)
	if err != nil || leased == nil {
		t.Fatalf("lease retry dispatch = %#v, %v", leased, err)
	}
	if err := repo.FailDispatch(ctx, leased.ID, "retry-owner", "temporary", 3, now); err != nil {
		t.Fatalf("FailDispatch retry: %v", err)
	}
	leasedAgain, err := repo.LeaseNextDispatch(ctx, "retry-owner", now.Add(3*time.Second), time.Minute)
	if err != nil || leasedAgain == nil || leasedAgain.ID != retryDispatch.ID || leasedAgain.Attempts != 2 {
		t.Fatalf("lease retry again = %#v, %v", leasedAgain, err)
	}
	if err := repo.FailDispatch(ctx, leasedAgain.ID, "retry-owner", "permanent", 2, now.Add(3*time.Second)); err != nil {
		t.Fatalf("FailDispatch terminal: %v", err)
	}

	abandonTask := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Abandon dispatch")
	abandonSchedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, abandonTask.ID, fixture.Nodes["trigger"], now.Add(-30*time.Second))
	_, abandonDispatch, err := repo.ClaimScheduledOccurrence(ctx, abandonSchedule, now, nil)
	if err != nil {
		t.Fatalf("claim abandoned schedule: %v", err)
	}
	abandonLease, err := repo.LeaseNextDispatch(ctx, "abandon-owner", now, time.Minute)
	if err != nil || abandonLease == nil || abandonLease.ID != abandonDispatch.ID {
		t.Fatalf("lease abandoned dispatch = %#v, %v", abandonLease, err)
	}
	if err := repo.MarkDispatchQueued(ctx, abandonLease.ID, "abandon-owner"); err != nil {
		t.Fatalf("MarkDispatchQueued: %v", err)
	}
	if err := taskRepo.UpdateCategory(ctx, abandonTask.ID, models.CategoryBacklog); err != nil {
		t.Fatalf("move task out of runnable category: %v", err)
	}
	abandoned, err := repo.ListAbandonedQueuedDispatches(ctx, 0)
	if err != nil || len(abandoned) != 1 || abandoned[0].ID != abandonDispatch.ID {
		t.Fatalf("ListAbandonedQueuedDispatches = %#v, %v", abandoned, err)
	}
	if err := repo.AbandonQueuedDispatch(ctx, abandonDispatch.ID, "lost capacity"); err != nil {
		t.Fatalf("AbandonQueuedDispatch: %v", err)
	}
	abandoned, err = repo.ListAbandonedQueuedDispatches(ctx, 10)
	if err != nil || len(abandoned) != 0 {
		t.Fatalf("abandoned after cleanup = %#v, %v", abandoned, err)
	}
}

func TestAutomationRepoGitHubDedupAndExternalActivityLeases(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"github": "github_inbox"})
	repo := NewAutomationRepo(db)
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status)
		VALUES ('dedup-invocation', ?, ?, ?, ?, 'manual', 'run', 'manual:dedup', 'running')`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, fixture.Nodes["github"]); err != nil {
		t.Fatalf("insert invocation: %v", err)
	}
	binding := models.AutomationBinding{
		AutomationID: fixture.AutomationID,
		VersionID:    fixture.VersionID,
		InvocationID: "dedup-invocation",
		NodeID:       fixture.Nodes["github"],
	}
	source := AutomationGitHubIssueDedupSource{
		Context:     models.AutomationContext{ProjectID: fixture.ProjectID, Bindings: []models.AutomationBinding{binding}},
		TaskID:      "task-source",
		ExecutionID: "exec-source",
	}

	claim, err := repo.AcquireGitHubIssueDedupLease(ctx, fixture.ProjectID, "Owner/Repo", "title-fp", "owner-1", source, now, time.Minute)
	if err != nil || claim.OwnerToken != "owner-1" {
		t.Fatalf("AcquireGitHubIssueDedupLease = %#v, %v", claim, err)
	}
	if _, err := repo.AcquireGitHubIssueDedupLease(ctx, fixture.ProjectID, "owner/repo", "title-fp", "owner-2", source, now.Add(10*time.Second), time.Minute); !errors.Is(err, ErrAutomationGitHubIssueDedupBusy) {
		t.Fatalf("expected busy lease, got %v", err)
	}
	if err := repo.ReleaseGitHubIssueDedupLease(ctx, fixture.ProjectID, "owner/repo", "title-fp", "owner-1"); err != nil {
		t.Fatalf("ReleaseGitHubIssueDedupLease: %v", err)
	}
	claim, err = repo.AcquireGitHubIssueDedupLease(ctx, fixture.ProjectID, "owner/repo", "title-fp", "owner-2", source, now.Add(2*time.Minute), time.Minute)
	if err != nil || claim.OwnerToken != "owner-2" {
		t.Fatalf("reacquire lease = %#v, %v", claim, err)
	}
	if err := repo.MarkGitHubIssueDedupDispatched(ctx, fixture.ProjectID, "owner/repo", "title-fp", "owner-2"); err != nil {
		t.Fatalf("MarkGitHubIssueDedupDispatched: %v", err)
	}
	if err := repo.CompleteGitHubIssueDedupLease(ctx, fixture.ProjectID, "owner/repo", "title-fp", "owner-2", 42); err != nil {
		t.Fatalf("CompleteGitHubIssueDedupLease: %v", err)
	}
	completed, err := repo.AcquireGitHubIssueDedupLease(ctx, fixture.ProjectID, "owner/repo", "title-fp", "owner-3", source, now.Add(3*time.Minute), time.Minute)
	if err != nil || completed.IssueNumber != 42 || completed.OwnerToken != "owner-2" {
		t.Fatalf("completed lease = %#v, %v", completed, err)
	}

	resourceID, err := repo.ReserveExternalActivity(ctx, fixture.ProjectID, binding, "create-issue", "create_github_issue", "github_issue")
	if err != nil || resourceID != "" {
		t.Fatalf("ReserveExternalActivity initial = %q, %v", resourceID, err)
	}
	if resourceID, err := repo.ReserveExternalActivity(ctx, fixture.ProjectID, binding, "create-issue", "create_github_issue", "github_issue"); !errors.Is(err, ErrAutomationExternalReconciliation) || resourceID != "" {
		t.Fatalf("ReserveExternalActivity duplicate = %q, %v", resourceID, err)
	}
	if err := repo.ReleaseExternalActivityReservation(ctx, fixture.ProjectID, binding, "create-issue"); err != nil {
		t.Fatalf("ReleaseExternalActivityReservation: %v", err)
	}
	if resourceID, err := repo.ReserveExternalActivity(ctx, fixture.ProjectID, binding, "create-issue", "create_github_issue", "github_issue"); err != nil || resourceID != "" {
		t.Fatalf("ReserveExternalActivity after release = %q, %v", resourceID, err)
	}
	var activityID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM automation_activities WHERE automation_id = ? AND version_id = ? AND activity_key = 'create-issue'`,
		fixture.AutomationID, fixture.VersionID).Scan(&activityID); err != nil {
		t.Fatalf("load reserved activity: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_activity_resources (activity_id, resource_type, resource_id, relation)
		VALUES (?, 'github_issue', 'github:owner/repo:issue:42', 'created')`, activityID); err != nil {
		t.Fatalf("insert external resource: %v", err)
	}
	resourceID, err = repo.ReserveExternalActivity(ctx, fixture.ProjectID, binding, "create-issue", "create_github_issue", "github_issue")
	if err != nil || resourceID != "github:owner/repo:issue:42" {
		t.Fatalf("ReserveExternalActivity existing resource = %q, %v", resourceID, err)
	}
}

func createRuntimeContextTask(t *testing.T, ctx context.Context, repo *TaskRepo, projectID, title string) *models.Task {
	t.Helper()
	task := &models.Task{
		ProjectID: projectID,
		Title:     title,
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Priority:  1,
		Prompt:    title,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create task %q: %v", title, err)
	}
	return task
}

func createRuntimeScheduledTask(t *testing.T, ctx context.Context, repo *TaskRepo, projectID, title string) *models.Task {
	t.Helper()
	task := &models.Task{
		ProjectID: projectID,
		Title:     title,
		Category:  models.CategoryScheduled,
		Status:    models.StatusPending,
		Priority:  1,
		Prompt:    title,
	}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("create scheduled task %q: %v", title, err)
	}
	return task
}

func createRuntimeAutomationSchedule(t *testing.T, ctx context.Context, db *sql.DB, fixture automationLiveCountsFixture, taskID, nodeID string, due time.Time) models.Schedule {
	t.Helper()
	schedule := models.Schedule{
		TaskID:         taskID,
		RunAt:          due,
		RepeatType:     models.RepeatOnce,
		RepeatInterval: 1,
		Enabled:        true,
		NextRun:        &due,
	}
	if err := NewScheduleRepo(db).Create(ctx, &schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_definition_resources
		(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
		VALUES
		(?, ?, ?, ?, 'schedule', ?, 'owned'),
		(?, ?, ?, ?, 'task', ?, 'owned')`,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, nodeID, schedule.ID,
		fixture.ProjectID, fixture.AutomationID, fixture.VersionID, nodeID, taskID); err != nil {
		t.Fatalf("insert schedule definition resources: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_trigger_owners
		(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
		VALUES (?, ?, ?, ?, ?, 'active')`, schedule.ID, fixture.ProjectID, fixture.AutomationID, fixture.VersionID, nodeID); err != nil {
		t.Fatalf("insert trigger owner: %v", err)
	}
	return schedule
}
