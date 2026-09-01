package repository

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
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

func TestAutomationGitHubIssueDedupSourceAutomationCountsUseStableIdentity(t *testing.T) {
	source := AutomationGitHubIssueDedupSource{
		Context: models.AutomationContext{ProjectID: "project", Bindings: []models.AutomationBinding{
			{AutomationID: "auto", VersionID: "v1", NodeID: "n1"},
			{AutomationID: "auto", VersionID: "v2", NodeID: "replacement-node"},
		}},
		StableBindings: []AutomationGitHubIssueDedupNodeSource{
			{AutomationID: "auto", NodeKey: "bug_finder"},
			{AutomationID: "other-auto", NodeKey: "bug_finder"},
		},
	}
	counts := dedupSourceAutomationCounts(source)
	if len(counts) != 2 || counts["auto"] != 1 || counts["other-auto"] != 1 {
		t.Fatalf("dedupSourceAutomationCounts() = %#v, want one count per stable Automation identity", counts)
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

func TestAutomationTaskAdmissionPersistsClaimedAndReservedResults(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	taskRepo := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Shared admission task")
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET category = 'active' WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("make task active: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	claim := func(input automationTaskAdmissionInput) automationTaskAdmissionResult {
		conn, finish, err := beginImmediateConn(ctx, db)
		if err != nil {
			t.Fatalf("begin admission transaction: %v", err)
		}
		defer finish()
		result, err := claimAutomationTaskAdmission(ctx, conn, input)
		if err != nil {
			t.Fatalf("claim admission: %v", err)
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			t.Fatalf("commit admission: %v", err)
		}
		return result
	}

	claimed := claim(automationTaskAdmissionInput{
		projectID: fixture.ProjectID, automationID: fixture.AutomationID, versionID: fixture.VersionID,
		triggerNodeID: fixture.Nodes["trigger"], triggerResourceType: "manual", triggerResourceID: "schedule-1",
		occurrenceKey: "manual:shared-admission", adapterKey: "native_sdlc", taskID: task.ID,
		taskStatus: models.StatusPending, taskCategory: models.CategoryActive, now: now,
	})
	if claimed.invocation.Status != models.AutomationInvocationClaimed || claimed.invocation.TriggerResourceType != "manual" || claimed.invocation.ScheduledFor != nil {
		t.Fatalf("claimed invocation = %#v", claimed.invocation)
	}
	if claimed.dispatch == nil || claimed.dispatch.TaskID != task.ID || claimed.effectiveCategory != models.CategoryActive || claimed.skippedReason != "" {
		t.Fatalf("claimed admission = %#v", claimed)
	}

	var taskStatus, taskCategory string
	var completedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT status, category, completed_at FROM tasks WHERE id = ?`, task.ID).
		Scan(&taskStatus, &taskCategory, &completedAt); err != nil {
		t.Fatalf("load claimed task: %v", err)
	}
	if taskStatus != string(models.StatusPending) || taskCategory != string(models.CategoryActive) || completedAt.Valid {
		t.Fatalf("claimed task status=%q category=%q completed_at=%v", taskStatus, taskCategory, completedAt)
	}
	var invocations, dispatches, reservations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations`).Scan(&invocations); err != nil {
		t.Fatalf("count claimed invocations: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_dispatch_outbox`).Scan(&dispatches); err != nil {
		t.Fatalf("count claimed dispatches: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations`).Scan(&reservations); err != nil {
		t.Fatalf("count claimed reservations: %v", err)
	}
	if invocations != 1 || dispatches != 1 || reservations != 1 {
		t.Fatalf("claimed rows invocations=%d dispatches=%d reservations=%d", invocations, dispatches, reservations)
	}

	due := now.Add(-time.Minute)
	skipped := claim(automationTaskAdmissionInput{
		projectID: fixture.ProjectID, automationID: fixture.AutomationID, versionID: fixture.VersionID,
		triggerNodeID: fixture.Nodes["trigger"], triggerResourceType: "schedule", triggerResourceID: "schedule-1",
		occurrenceKey: "schedule:schedule-1:" + due.Format(time.RFC3339Nano), scheduledFor: &due,
		adapterKey: "native_sdlc", taskID: task.ID, taskStatus: models.StatusPending, taskCategory: models.CategoryActive, now: now,
	})
	if skipped.invocation.Status != models.AutomationInvocationSkipped || skipped.invocation.SkippedReason != "task_reserved" || skipped.dispatch != nil {
		t.Fatalf("reserved admission = %#v", skipped)
	}
	if skipped.invocation.ScheduledFor == nil || !skipped.invocation.ScheduledFor.Equal(due) || skipped.invocation.StartedAt == nil || skipped.invocation.CompletedAt == nil {
		t.Fatalf("reserved invocation timestamps = %#v", skipped.invocation)
	}
	if skipped.effectiveCategory != models.CategoryActive {
		t.Fatalf("reserved effective category = %q, want %q", skipped.effectiveCategory, models.CategoryActive)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_invocations`).Scan(&invocations); err != nil {
		t.Fatalf("count skipped invocations: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_dispatch_outbox`).Scan(&dispatches); err != nil {
		t.Fatalf("count skipped dispatches: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations`).Scan(&reservations); err != nil {
		t.Fatalf("count skipped reservations: %v", err)
	}
	if invocations != 2 || dispatches != 1 || reservations != 1 {
		t.Fatalf("skipped rows invocations=%d dispatches=%d reservations=%d", invocations, dispatches, reservations)
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

func TestAutomationRepoClaimsPublishBoardProjectionEventWithoutTaskColumnChange(t *testing.T) {
	for _, trigger := range []string{"scheduled", "manual"} {
		t.Run(trigger, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
			repo := NewAutomationRepo(db)
			broadcaster := events.NewBroadcaster()
			repo.SetBroadcaster(broadcaster)
			subscriber, err := broadcaster.Subscribe()
			if err != nil {
				t.Fatalf("subscribe: %v", err)
			}
			defer broadcaster.Unsubscribe(subscriber)

			taskRepo := NewTaskRepo(db, nil)
			task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "First "+trigger+" capacity queue")
			now := time.Now().UTC()
			schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], now.Add(-time.Minute))

			switch trigger {
			case "scheduled":
				if _, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, now, schedule.ComputeNextRun(now)); err != nil || dispatch == nil {
					t.Fatalf("claim scheduled occurrence = %#v, %v", dispatch, err)
				}
			case "manual":
				if _, dispatches, err := repo.ClaimManualAutomationRun(ctx, fixture.ProjectID, fixture.AutomationID, now); err != nil || len(dispatches) != 1 {
					t.Fatalf("claim manual run dispatches = %#v, %v", dispatches, err)
				}
			}

			timeout := time.After(time.Second)
			for {
				select {
				case event := <-subscriber:
					if event.Type != events.TaskEventType("task_board_updated") {
						continue
					}
					if event.ProjectID != fixture.ProjectID || event.TaskID != task.ID {
						t.Fatalf("board event = %#v, want project %q task %q", event, fixture.ProjectID, task.ID)
					}
					return
				case <-timeout:
					t.Fatal("capacity queue projection did not publish a task-board refresh event")
				}
			}
		})
	}
}

func TestAutomationRepoFailedCompletedCustomTaskCanBeClaimedAgain(t *testing.T) {
	for _, trigger := range []string{"scheduled", "manual"} {
		t.Run(trigger, func(t *testing.T) {
			db := testutil.NewTestDB(t)
			ctx := context.Background()
			fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
			repo := NewAutomationRepo(db)
			taskRepo := NewTaskRepo(db, nil)
			task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Retry "+trigger+" failed Automation")
			now := time.Now().UTC()
			schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], now.Add(-time.Minute))
			if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'failed', category = 'completed', completed_at = ? WHERE id = ?`, now, task.ID); err != nil {
				t.Fatalf("seed terminal task: %v", err)
			}

			var dispatch *models.AutomationDispatch
			var err error
			if trigger == "scheduled" {
				_, dispatch, err = repo.ClaimScheduledOccurrence(ctx, schedule, now, schedule.ComputeNextRun(now))
			} else {
				var dispatches []models.AutomationDispatch
				_, dispatches, err = repo.ClaimManualAutomationRun(ctx, fixture.ProjectID, fixture.AutomationID, now)
				if len(dispatches) == 1 {
					dispatch = &dispatches[0]
				}
			}
			if err != nil || dispatch == nil {
				t.Fatalf("claim failed/completed task = %#v, %v", dispatch, err)
			}
			stored, err := taskRepo.GetByID(ctx, task.ID)
			if err != nil {
				t.Fatalf("load readmitted task: %v", err)
			}
			var reservations int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations); err != nil {
				t.Fatalf("load readmitted reservation: %v", err)
			}
			if stored == nil || stored.Status != models.StatusPending || stored.Category != models.CategoryScheduled || stored.CompletedAt != nil || reservations != 1 {
				t.Fatalf("readmitted task = %#v reservations=%d, want pending/scheduled without completion and one reservation", stored, reservations)
			}
		})
	}
}

func TestAutomationRepoTerminalExecutionBearingDispatchFailureConvergesAndAllowsLaterOccurrence(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	now := time.Now().UTC().Truncate(time.Second)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Recovered prepared dispatch failure")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], now.Add(-time.Minute))
	schedule.RunAt = now.Add(-time.Hour)
	schedule.RepeatType = models.RepeatHours
	schedule.RepeatInterval = 1
	if err := NewScheduleRepo(db).Update(ctx, &schedule); err != nil {
		t.Fatalf("make dispatch schedule repeating: %v", err)
	}

	_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, now, schedule.ComputeNextRun(now))
	if err != nil {
		t.Fatalf("claim first occurrence: %v", err)
	}
	leased, err := repo.LeaseNextDispatch(ctx, "prepared-failure-owner", now.Add(time.Second), time.Minute)
	if err != nil || leased == nil || leased.ID != dispatch.ID {
		t.Fatalf("lease first dispatch = %#v, %v", leased, err)
	}
	execution, err := taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "prepared-failure-owner")
	if err != nil {
		t.Fatalf("claim prepared execution: %v", err)
	}
	failedAt := now.Add(2 * time.Second)
	if err := repo.FailDispatch(ctx, dispatch.ID, "prepared-failure-owner", "prepared recovery exhausted", 1, failedAt); err != nil {
		t.Fatalf("terminalize prepared dispatch: %v", err)
	}

	var taskStatus, taskCategory, executionStatus, executionError, dispatchStatus, invocationStatus, activityStatus string
	var taskCompletedAt, executionCompletedAt, invocationCompletedAt, activityCompletedAt sql.NullTime
	var reservations int
	if err := db.QueryRowContext(ctx, `SELECT status, category, completed_at FROM tasks WHERE id = ?`, task.ID).
		Scan(&taskStatus, &taskCategory, &taskCompletedAt); err != nil {
		t.Fatalf("load terminal task: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, error_message, completed_at FROM executions WHERE id = ?`, execution.ID).
		Scan(&executionStatus, &executionError, &executionCompletedAt); err != nil {
		t.Fatalf("load terminal execution: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&dispatchStatus); err != nil {
		t.Fatalf("load terminal dispatch: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, completed_at FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).
		Scan(&invocationStatus, &invocationCompletedAt); err != nil {
		t.Fatalf("load terminal invocation: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, completed_at FROM automation_activities WHERE invocation_id = ? AND activity_key = ?`,
		dispatch.InvocationID, "dispatch:"+dispatch.ID+":execute").Scan(&activityStatus, &activityCompletedAt); err != nil {
		t.Fatalf("load terminal activity: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations); err != nil {
		t.Fatalf("load terminal reservation count: %v", err)
	}
	if taskStatus != string(models.StatusFailed) || taskCategory != string(models.CategoryCompleted) || !taskCompletedAt.Valid ||
		executionStatus != string(models.ExecFailed) || executionError != "prepared recovery exhausted" || !executionCompletedAt.Valid ||
		dispatchStatus != "failed" || invocationStatus != string(models.AutomationInvocationFailed) || !invocationCompletedAt.Valid ||
		activityStatus != string(models.AutomationActivityFailed) || !activityCompletedAt.Valid || reservations != 0 {
		t.Fatalf("terminal prepared state task=%q/%q completed=%v execution=%q/%q completed=%v dispatch=%q invocation=%q completed=%v activity=%q completed=%v reservations=%d",
			taskStatus, taskCategory, taskCompletedAt.Valid, executionStatus, executionError, executionCompletedAt.Valid,
			dispatchStatus, invocationStatus, invocationCompletedAt.Valid, activityStatus, activityCompletedAt.Valid, reservations)
	}
	recoverable, err := repo.ListRecoverablePreparedDispatches(ctx, 10)
	if err != nil || len(recoverable) != 0 {
		t.Fatalf("terminal dispatch remained recoverable = %#v, %v", recoverable, err)
	}

	storedSchedule, err := NewScheduleRepo(db).GetByID(ctx, schedule.ID)
	if err != nil || storedSchedule == nil || storedSchedule.NextRun == nil {
		t.Fatalf("load later schedule = %#v, %v", storedSchedule, err)
	}
	laterDue := storedSchedule.NextRun.UTC()
	_, laterDispatch, err := repo.ClaimScheduledOccurrence(ctx, *storedSchedule, laterDue, storedSchedule.ComputeNextRun(laterDue))
	if err != nil || laterDispatch == nil || laterDispatch.ID == dispatch.ID {
		t.Fatalf("claim later occurrence = %#v, %v", laterDispatch, err)
	}
	laterLease, err := repo.LeaseNextDispatch(ctx, "later-owner", laterDue.Add(time.Second), time.Minute)
	if err != nil || laterLease == nil || laterLease.ID != laterDispatch.ID {
		t.Fatalf("lease later dispatch = %#v, %v", laterLease, err)
	}
	if err := repo.MarkDispatchQueued(ctx, laterDispatch.ID, "later-owner"); err != nil {
		t.Fatalf("queue later dispatch: %v", err)
	}
	recoverable, err = repo.ListRecoverablePreparedDispatches(ctx, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != laterDispatch.ID {
		t.Fatalf("later dispatch recovery candidates = %#v, %v", recoverable, err)
	}
	laterExecution, err := taskRepo.ClaimQueuedAutomationDispatch(ctx, laterDispatch.ID)
	if err != nil {
		t.Fatalf("claim recovered later dispatch: %v", err)
	}
	if err := repo.CompleteDispatch(ctx, laterDispatch.ID, laterExecution.Execution.ID, models.ExecCompleted, "later occurrence completed"); err != nil {
		t.Fatalf("complete later occurrence: %v", err)
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
	leaseNow := time.Now().UTC().Add(time.Second)
	leased, err := repo.LeaseNextDispatch(ctx, "retry-owner", leaseNow, time.Minute)
	if err != nil || leased == nil {
		t.Fatalf("lease retry dispatch = %#v, %v", leased, err)
	}
	if err := repo.FailDispatch(ctx, leased.ID, "retry-owner", "temporary", 3, leaseNow); err != nil {
		t.Fatalf("FailDispatch retry: %v", err)
	}
	leasedAgain, err := repo.LeaseNextDispatch(ctx, "retry-owner", leaseNow.Add(3*time.Second), time.Minute)
	if err != nil || leasedAgain == nil || leasedAgain.ID != retryDispatch.ID || leasedAgain.Attempts != 2 {
		t.Fatalf("lease retry again = %#v, %v", leasedAgain, err)
	}
	if err := repo.FailDispatch(ctx, leasedAgain.ID, "retry-owner", "permanent", 2, leaseNow.Add(3*time.Second)); err != nil {
		t.Fatalf("FailDispatch terminal: %v", err)
	}
	var failedStatus, failedCategory string
	var failedCompletedAt sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT status, category, completed_at FROM tasks WHERE id = ?`, retryTask.ID).
		Scan(&failedStatus, &failedCategory, &failedCompletedAt); err != nil {
		t.Fatalf("load terminal dispatch task: %v", err)
	}
	if failedStatus != string(models.StatusFailed) || failedCategory != string(models.CategoryCompleted) || !failedCompletedAt.Valid {
		t.Fatalf("terminal dispatch task = %q/%q completed=%v, want failed/completed with completion time",
			failedStatus, failedCategory, failedCompletedAt.Valid)
	}

	abandonTask := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Abandon dispatch")
	abandonSchedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, abandonTask.ID, fixture.Nodes["trigger"], now.Add(-30*time.Second))
	_, abandonDispatch, err := repo.ClaimScheduledOccurrence(ctx, abandonSchedule, now, nil)
	if err != nil {
		t.Fatalf("claim abandoned schedule: %v", err)
	}
	abandonLease, err := repo.LeaseNextDispatch(ctx, "abandon-owner", time.Now().UTC().Add(time.Second), time.Minute)
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

func TestAutomationRepoCancelsQueuedDispatchAndPreparedExecution(t *testing.T) {
	t.Run("capacity queued dispatch", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
		repo := NewAutomationRepo(db)
		taskRepo := NewTaskRepo(db, nil)
		task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Capacity queued cancellation")
		schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
		_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), schedule.ComputeNextRun(time.Now().UTC()))
		if err != nil {
			t.Fatalf("claim capacity queued occurrence: %v", err)
		}
		leased, err := repo.LeaseNextDispatch(ctx, "queued-cancellation-owner", time.Now().UTC(), time.Minute)
		if err != nil || leased == nil || leased.ID != dispatch.ID {
			t.Fatalf("lease capacity queued dispatch = %#v, %v", leased, err)
		}
		if err := repo.MarkDispatchQueued(ctx, dispatch.ID, "queued-cancellation-owner"); err != nil {
			t.Fatalf("mark dispatch queued: %v", err)
		}
		boardTasks, err := taskRepo.ListBoardByProjectWithCategorySorts(ctx, fixture.ProjectID, "", "", "")
		if err != nil {
			t.Fatalf("list board tasks while capacity queued: %v", err)
		}
		var projected *models.Task
		for i := range boardTasks {
			if boardTasks[i].ID == task.ID {
				projected = &boardTasks[i]
				break
			}
		}
		if projected == nil || !projected.AutomationCapacityQueued {
			t.Fatalf("board projection did not mark durable capacity queue: %#v", projected)
		}
		if err := repo.CancelDispatchesForTask(ctx, task.ID, "cancelled while waiting for capacity"); err != nil {
			t.Fatalf("CancelDispatchesForTask: %v", err)
		}

		var dispatchStatus, invocationStatus string
		var reservations int
		if err := db.QueryRowContext(ctx, `SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&dispatchStatus); err != nil {
			t.Fatalf("load dispatch status: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus); err != nil {
			t.Fatalf("load invocation status: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations); err != nil {
			t.Fatalf("load reservation count: %v", err)
		}
		if dispatchStatus != "failed" || invocationStatus != string(models.AutomationInvocationCancelled) || reservations != 0 {
			t.Fatalf("queued cancellation state dispatch=%q invocation=%q reservations=%d", dispatchStatus, invocationStatus, reservations)
		}
		stored, err := taskRepo.GetByID(ctx, task.ID)
		if err != nil {
			t.Fatalf("load task after queued cancellation: %v", err)
		}
		if stored == nil || stored.Status != models.StatusPending || stored.Category != models.CategoryScheduled {
			t.Fatalf("queued cancellation changed task unexpectedly: %#v", stored)
		}
	})

	t.Run("prepared execution", func(t *testing.T) {
		db := testutil.NewTestDB(t)
		ctx := context.Background()
		fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
		repo := NewAutomationRepo(db)
		taskRepo := NewTaskRepo(db, nil)
		task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Prepared execution cancellation")
		schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
		_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), schedule.ComputeNextRun(time.Now().UTC()))
		if err != nil {
			t.Fatalf("claim prepared occurrence: %v", err)
		}
		leased, err := repo.LeaseNextDispatch(ctx, "prepared-cancellation-owner", time.Now().UTC(), time.Minute)
		if err != nil || leased == nil || leased.ID != dispatch.ID {
			t.Fatalf("lease prepared dispatch = %#v, %v", leased, err)
		}
		execution, err := taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "prepared-cancellation-owner")
		if err != nil {
			t.Fatalf("claim prepared execution: %v", err)
		}
		if err := repo.MarkDispatchSubmitted(ctx, dispatch.ID, "prepared-cancellation-owner", execution.ID); err != nil {
			t.Fatalf("mark prepared dispatch submitted: %v", err)
		}
		if err := repo.CancelDispatchesForTask(ctx, task.ID, "prepared execution cancelled"); err != nil {
			t.Fatalf("CancelDispatchesForTask prepared execution: %v", err)
		}

		var taskStatus, taskCategory, executionStatus, dispatchStatus, invocationStatus, activityStatus string
		var reservations int
		if err := db.QueryRowContext(ctx, `SELECT status, category FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus, &taskCategory); err != nil {
			t.Fatalf("load prepared task state: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM executions WHERE id = ?`, execution.ID).Scan(&executionStatus); err != nil {
			t.Fatalf("load prepared execution status: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&dispatchStatus); err != nil {
			t.Fatalf("load prepared dispatch status: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus); err != nil {
			t.Fatalf("load prepared invocation status: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT status FROM automation_activities WHERE invocation_id = ? AND activity_key = ?`, dispatch.InvocationID, "dispatch:"+dispatch.ID+":execute").Scan(&activityStatus); err != nil {
			t.Fatalf("load prepared activity status: %v", err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations); err != nil {
			t.Fatalf("load prepared reservation count: %v", err)
		}
		if taskStatus != string(models.StatusCancelled) || taskCategory != string(models.CategoryBacklog) || executionStatus != string(models.ExecCancelled) ||
			dispatchStatus != "failed" || invocationStatus != string(models.AutomationInvocationCancelled) ||
			activityStatus != string(models.AutomationActivityCancelled) || reservations != 0 {
			t.Fatalf("prepared cancellation state task=%q/%q execution=%q dispatch=%q invocation=%q activity=%q reservations=%d",
				taskStatus, taskCategory, executionStatus, dispatchStatus, invocationStatus, activityStatus, reservations)
		}
	})
}

func TestAutomationRepoTerminalDispatchFailurePreservesCancelledTask(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Cancelled during dispatch retry")
	now := time.Now().UTC()
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], now.Add(-time.Minute))
	_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, now, schedule.ComputeNextRun(now))
	if err != nil {
		t.Fatalf("claim occurrence: %v", err)
	}
	leased, err := repo.LeaseNextDispatch(ctx, "cancel-race-owner", now, time.Minute)
	if err != nil || leased == nil || leased.ID != dispatch.ID {
		t.Fatalf("lease dispatch = %#v, %v", leased, err)
	}
	execution, err := taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "cancel-race-owner")
	if err != nil {
		t.Fatalf("claim execution before cancellation: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'cancelled', category = 'backlog', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, task.ID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	if err := repo.FailDispatch(ctx, dispatch.ID, "cancel-race-owner", "cancelled during retry", 1, now); err != nil {
		t.Fatalf("terminalize cancelled dispatch: %v", err)
	}

	var taskStatus, taskCategory, executionStatus, invocationStatus, activityStatus string
	if err := db.QueryRowContext(ctx, `SELECT status, category FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus, &taskCategory); err != nil {
		t.Fatalf("load cancelled task: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM executions WHERE id = ?`, execution.ID).Scan(&executionStatus); err != nil {
		t.Fatalf("load cancelled execution: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus); err != nil {
		t.Fatalf("load cancelled invocation: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_activities WHERE invocation_id = ? AND activity_key = ?`,
		dispatch.InvocationID, "dispatch:"+dispatch.ID+":execute").Scan(&activityStatus); err != nil {
		t.Fatalf("load cancelled activity: %v", err)
	}
	if taskStatus != string(models.StatusCancelled) || taskCategory != string(models.CategoryBacklog) ||
		executionStatus != string(models.ExecCancelled) || invocationStatus != string(models.AutomationInvocationCancelled) ||
		activityStatus != string(models.AutomationActivityCancelled) {
		t.Fatalf("cancel race state task=%q/%q execution=%q invocation=%q activity=%q",
			taskStatus, taskCategory, executionStatus, invocationStatus, activityStatus)
	}
}

func TestAutomationRepoTerminalAutomationFailureMovesScheduledTaskToCompleted(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Terminal Automation failure")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), schedule.ComputeNextRun(time.Now().UTC()))
	if err != nil {
		t.Fatalf("claim terminal failure occurrence: %v", err)
	}
	leased, err := repo.LeaseNextDispatch(ctx, "terminal-failure-owner", time.Now().UTC(), time.Minute)
	if err != nil || leased == nil || leased.ID != dispatch.ID {
		t.Fatalf("lease terminal failure dispatch = %#v, %v", leased, err)
	}
	execution, err := taskRepo.ClaimAutomationDispatch(ctx, dispatch.ID, "terminal-failure-owner")
	if err != nil {
		t.Fatalf("claim terminal failure execution: %v", err)
	}
	if err := repo.MarkDispatchSubmitted(ctx, dispatch.ID, "terminal-failure-owner", execution.ID); err != nil {
		t.Fatalf("mark terminal failure dispatch submitted: %v", err)
	}
	if err := repo.CompleteDispatch(ctx, dispatch.ID, execution.ID, models.ExecFailed, "provider failed"); err != nil {
		t.Fatalf("complete terminal failure dispatch: %v", err)
	}

	stored, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("load terminal failure task: %v", err)
	}
	if stored == nil || stored.Status != models.StatusFailed || stored.Category != models.CategoryCompleted {
		t.Fatalf("terminal Automation failure task = %#v, want failed/completed", stored)
	}
}
func TestAutomationRepoLifecyclePauseCancelsScheduledCapacityQueue(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Lifecycle queued task")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), schedule.ComputeNextRun(time.Now().UTC()))
	if err != nil {
		t.Fatalf("claim lifecycle occurrence: %v", err)
	}
	leased, err := repo.LeaseNextDispatch(ctx, "lifecycle-owner", time.Now().UTC(), time.Minute)
	if err != nil || leased == nil || leased.ID != dispatch.ID {
		t.Fatalf("lease lifecycle dispatch = %#v, %v", leased, err)
	}
	if err := repo.MarkDispatchQueued(ctx, dispatch.ID, "lifecycle-owner"); err != nil {
		t.Fatalf("mark lifecycle dispatch queued: %v", err)
	}
	if err := repo.SetAutomationLifecycle(ctx, fixture.ProjectID, fixture.AutomationID, models.AutomationPaused); err != nil {
		t.Fatalf("pause Automation: %v", err)
	}

	var dispatchStatus, invocationStatus, taskStatus, taskCategory string
	var reservations int
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&dispatchStatus); err != nil {
		t.Fatalf("load lifecycle dispatch status: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus); err != nil {
		t.Fatalf("load lifecycle invocation status: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, category FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus, &taskCategory); err != nil {
		t.Fatalf("load lifecycle task state: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations); err != nil {
		t.Fatalf("load lifecycle reservation count: %v", err)
	}
	if dispatchStatus != "failed" || invocationStatus != string(models.AutomationInvocationCancelled) ||
		taskStatus != string(models.StatusPending) || taskCategory != string(models.CategoryBacklog) || reservations != 0 {
		t.Fatalf("lifecycle pause state dispatch=%q invocation=%q task=%q/%q reservations=%d",
			dispatchStatus, invocationStatus, taskStatus, taskCategory, reservations)
	}
}
func TestAutomationRepoResumePreservesScheduledCapacityQueue(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	fixture := seedAutomationLiveCountsDefinition(t, db, map[string]string{"trigger": "trigger"})
	repo := NewAutomationRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	task := createRuntimeScheduledTask(t, ctx, taskRepo, fixture.ProjectID, "Resume queued task")
	schedule := createRuntimeAutomationSchedule(t, ctx, db, fixture, task.ID, fixture.Nodes["trigger"], time.Now().UTC().Add(-time.Minute))
	_, dispatch, err := repo.ClaimScheduledOccurrence(ctx, schedule, time.Now().UTC(), schedule.ComputeNextRun(time.Now().UTC()))
	if err != nil {
		t.Fatalf("claim resume occurrence: %v", err)
	}
	leased, err := repo.LeaseNextDispatch(ctx, "resume-owner", time.Now().UTC(), time.Minute)
	if err != nil || leased == nil || leased.ID != dispatch.ID {
		t.Fatalf("lease resume dispatch = %#v, %v", leased, err)
	}
	if err := repo.MarkDispatchQueued(ctx, dispatch.ID, "resume-owner"); err != nil {
		t.Fatalf("mark resume dispatch queued: %v", err)
	}
	if err := repo.SetAutomationLifecycle(ctx, fixture.ProjectID, fixture.AutomationID, models.AutomationActive); err != nil {
		t.Fatalf("resume Automation: %v", err)
	}

	var dispatchStatus, invocationStatus, taskStatus, taskCategory string
	var reservations int
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_dispatch_outbox WHERE id = ?`, dispatch.ID).Scan(&dispatchStatus); err != nil {
		t.Fatalf("load resumed dispatch status: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM automation_invocations WHERE id = ?`, dispatch.InvocationID).Scan(&invocationStatus); err != nil {
		t.Fatalf("load resumed invocation status: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status, category FROM tasks WHERE id = ?`, task.ID).Scan(&taskStatus, &taskCategory); err != nil {
		t.Fatalf("load resumed task state: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_task_run_reservations WHERE dispatch_id = ?`, dispatch.ID).Scan(&reservations); err != nil {
		t.Fatalf("load resumed reservation count: %v", err)
	}
	if dispatchStatus != "submitted" || invocationStatus != string(models.AutomationInvocationClaimed) ||
		taskStatus != string(models.StatusPending) || taskCategory != string(models.CategoryScheduled) || reservations != 1 {
		t.Fatalf("resume queue state dispatch=%q invocation=%q task=%q/%q reservations=%d",
			dispatchStatus, invocationStatus, taskStatus, taskCategory, reservations)
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
