package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationPublicationPlanIsDeterministicAndCompilerIsIdempotent(t *testing.T) {
	automationobs.ResetForTest()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Publish")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)

	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	first, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	second, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, first.PlanRevision, second.PlanRevision)
	require.NotEmpty(t, first.Effects)
	require.Empty(t, first.Validation)

	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: first.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, models.AutomationActive, published.Definition.Automation.LifecycleState)
	require.Equal(t, models.AutomationVersionPublished, published.Definition.Version.State)
	require.NotEmpty(t, published.Resources)
	require.Greater(t, automationobs.Snapshot()["automation.publication.completed"].Count, uint64(0))
	storedSchedule, err := scheduleRepo.GetByID(context.Background(), publishedScheduleID(t, published))
	require.NoError(t, err)
	require.True(t, storedSchedule.Enabled, "trigger becomes runnable only with the published version")

	taskCount, scheduleCount := tableCount(t, db, "tasks"), tableCount(t, db, "schedules")
	retried, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: first.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, published.Attempt.ID, retried.Attempt.ID)
	require.Equal(t, taskCount, tableCount(t, db, "tasks"))
	require.Equal(t, scheduleCount, tableCount(t, db, "schedules"))
}

func TestCustomAutomationPublicationCreatesUserConfiguredTaskAndSchedule(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Custom publish")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	candidate.Name = "My support review"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "weekday_schedule", Name: "Weekday schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "08:30", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
		{Key: "review_support", Name: "Review support queue", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review unresolved support requests and propose the next action.", "category": "scheduled", "priority": 3}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
		{Key: "reviewed", Name: "Reviewed", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 520, Y: 0}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_to_review", From: "weekday_schedule", To: "review_support", Condition: map[string]any{}},
		{Key: "review_to_done", From: "review_support", To: "reviewed", Condition: map[string]any{}},
	}
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Empty(t, created.ValidationErrors)

	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	require.ElementsMatch(t, []string{"task", "schedule"}, []string{plan.Effects[0].ResourceType, plan.Effects[1].ResourceType})

	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, "custom", published.Definition.Version.AdapterKey)
	require.Equal(t, models.AutomationActive, published.Definition.Automation.LifecycleState)
	require.Len(t, published.Definition.Resources, 2)

	var taskID, scheduleID string
	for _, resource := range published.Definition.Resources {
		switch resource.ResourceType {
		case "task":
			taskID = resource.ResourceID
		case "schedule":
			scheduleID = resource.ResourceID
		}
	}
	task, err := taskRepo.GetByID(context.Background(), taskID)
	require.NoError(t, err)
	require.Contains(t, task.Title, "My support review: Review support queue")
	require.Equal(t, "Review unresolved support requests and propose the next action.", task.Prompt)
	require.Equal(t, models.CategoryScheduled, task.Category)
	require.Equal(t, 3, task.Priority)
	schedule, err := scheduleRepo.GetByID(context.Background(), scheduleID)
	require.NoError(t, err)
	require.Equal(t, task.ID, schedule.TaskID)
	require.Equal(t, models.RepeatDaily, schedule.RepeatType)
	require.True(t, schedule.Enabled)

	var triggerNodeID, taskNodeID, outcomeNodeID string
	for _, node := range published.Definition.Nodes {
		if node.NodeKey == "weekday_schedule" {
			triggerNodeID = node.ID
		}
		if node.NodeKey == "review_support" {
			taskNodeID = node.ID
		}
		if node.NodeKey == "reviewed" {
			outcomeNodeID = node.ID
		}
	}
	execRepo := repository.NewExecutionRepo(db)
	invocationID := repository.NewID()
	_, err = db.Exec(`INSERT INTO automation_invocations
		(id, project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status)
		VALUES (?, ?, ?, ?, ?, 'schedule', ?, ?, 'running')`, invocationID, project.ID, published.Definition.Automation.ID,
		published.Definition.Version.ID, triggerNodeID, schedule.ID, "test:"+repository.NewID())
	require.NoError(t, err)
	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: task.Prompt}
	require.NoError(t, execRepo.Create(context.Background(), &execution))
	binding := models.AutomationBinding{AutomationID: published.Definition.Automation.ID, VersionID: published.Definition.Version.ID, InvocationID: invocationID, NodeID: taskNodeID}
	_, _, err = automationRepo.RecordProjectionEvent(context.Background(), repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		ActivityKey: "execution:" + execution.ID + ":run", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}, {ResourceType: "execution", ResourceID: execution.ID}},
	})
	require.NoError(t, err)
	require.NoError(t, automationRepo.FinalizeExecutionProjection(context.Background(), project.ID, execution.ID, models.ExecCompleted))
	var completed int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ? AND from_node_id = ? AND to_node_id = ? AND state = 'completed'`,
		published.Definition.Automation.ID, taskNodeID, outcomeNodeID).Scan(&completed))
	require.Equal(t, 1, completed, "a single terminal custom task must project its real completion onto the connected Outcome")
}

func TestCustomAutomationPublicationRunsNativeAlertApprovalOnExactUserNodes(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Custom approval")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Change approval"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "morning", Name: "Morning", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "08:30", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "research", Name: "Research changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Research likely changes.", "category": "scheduled", "priority": 2}},
		{Key: "inspect", Name: "Inspect changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Inspect likely changes.", "category": "active", "priority": 2}},
		{Key: "ask_human", Name: "Ask a human", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Summarize one proposed change and why it is useful."}},
		{Key: "decision", Name: "Human decides", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "go_ahead", Name: "Approved", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "stop_here", Name: "Rejected", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "morning_research", From: "morning", To: "research", Condition: map[string]any{}},
		{Key: "research_inspect", From: "research", To: "inspect", Condition: map[string]any{}},
		{Key: "inspect_ask", From: "inspect", To: "ask_human", Condition: map[string]any{}},
		{Key: "ask_decision", From: "ask_human", To: "decision", Condition: map[string]any{}},
		{Key: "decision_yes", From: "decision", To: "go_ahead", Label: "approved", Condition: map[string]any{"state": "approved"}},
		{Key: "decision_no", From: "decision", To: "stop_here", Label: "rejected", Condition: map[string]any{"state": "rejected"}},
	}
	created, err := drafts.CreateDraft(ctx, AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Empty(t, created.ValidationErrors)

	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	var effectTypes []string
	for _, effect := range plan.Effects {
		effectTypes = append(effectTypes, effect.ResourceType)
	}
	require.ElementsMatch(t, []string{"task", "task", "schedule", "alert_configuration", "human_approval"}, effectTypes,
		"Review and apply must distinguish real resources from the configured runtime Alert handoff")
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(ctx, AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)

	resources := map[string]string{}
	nodeIDs := map[string]string{}
	for _, resource := range published.Definition.Resources {
		resources[resource.NodeKey+":"+resource.ResourceType] = resource.ResourceID
	}
	for _, node := range published.Definition.Nodes {
		nodeIDs[node.NodeKey] = node.ID
	}
	task, err := taskRepo.GetByID(ctx, resources["inspect:task"])
	require.NoError(t, err)
	require.Contains(t, task.Prompt, "create_notification")
	require.Contains(t, task.Prompt, "change_proposal")
	require.Contains(t, task.Prompt, "Summarize one proposed change")
	researchTask, err := taskRepo.GetByID(ctx, resources["research:task"])
	require.NoError(t, err)
	chain, err := researchTask.ParseChainConfig()
	require.NoError(t, err)
	require.Contains(t, chain.ChildPromptPrefix, "create_notification", "the existing task chain must carry the configured approval handoff")

	task.Prompt = "tampered mutable prompt"
	require.NoError(t, taskRepo.Update(ctx, task))
	parentExecution := models.Execution{TaskID: researchTask.ID, Status: models.ExecCompleted, PromptSent: researchTask.Prompt}
	execRepo := repository.NewExecutionRepo(db)
	require.NoError(t, execRepo.Create(ctx, &parentExecution))
	parentBinding := models.AutomationBinding{AutomationID: published.Definition.Automation.ID, VersionID: published.Definition.Version.ID, NodeID: nodeIDs["research"]}
	chainCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{parentBinding}})
	chainCtx = withAutomationExecution(chainCtx, researchTask.ID, parentExecution.ID)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), workerSvc)
	llmSvc := NewLLMService(repository.NewLLMConfigRepo(db), execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, repository.NewAttachmentRepo(db))
	llmSvc.SetTaskService(taskSvc)
	llmSvc.SetAutomationRepo(automationRepo)
	require.NoError(t, llmSvc.triggerTaskChain(chainCtx, *researchTask, "Research findings"))
	task, err = taskRepo.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Contains(t, task.Prompt, "create_notification", "immutable Automation topology must restore the native approval handoff")
	require.NotContains(t, task.Prompt, "tampered mutable prompt")

	alertRepo := repository.NewAlertRepo(db)
	alertRepo.SetAutomationRepo(automationRepo)
	alertSvc := NewAlertService(alertRepo, nil)
	binding := models.AutomationBinding{AutomationID: published.Definition.Automation.ID, VersionID: published.Definition.Version.ID, NodeID: nodeIDs["inspect"]}
	runtimeCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}})

	unboundAlert, err := alertSvc.CreateActionable(ctx, &models.Alert{ProjectID: project.ID, SourceTaskID: &task.ID, Type: "change_proposal", Title: "Existing unbound alert", IdempotencyKey: "custom-approval-collision"})
	require.NoError(t, err)
	_, err = alertSvc.CreateActionable(runtimeCtx, &models.Alert{ProjectID: project.ID, SourceTaskID: &task.ID, Type: "change_proposal", Title: "Must not be adopted", IdempotencyKey: "custom-approval-collision"})
	require.ErrorContains(t, err, "idempotency")
	var inferredWorkItems int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_work_items wi
		JOIN automation_activities activity ON activity.work_item_id = wi.id
		JOIN automation_activity_resources resource ON resource.activity_id = activity.id
		WHERE wi.automation_id = ? AND resource.resource_type = 'alert' AND resource.resource_id = ?`,
		published.Definition.Automation.ID, unboundAlert.ID).Scan(&inferredWorkItems))
	require.Zero(t, inferredWorkItems, "an existing unbound Alert must never be inferred into an Automation")

	approvedAlert, err := alertSvc.CreateActionable(runtimeCtx, &models.Alert{ProjectID: project.ID, SourceTaskID: &task.ID, Type: "change_proposal", Title: "Approve this", Body: "private proposal", IdempotencyKey: "custom-approval-yes"})
	require.NoError(t, err)
	retriedAlert, err := alertSvc.CreateActionable(runtimeCtx, &models.Alert{ProjectID: project.ID, SourceTaskID: &task.ID, Type: "change_proposal", Title: "Approve this", IdempotencyKey: "custom-approval-yes"})
	require.NoError(t, err)
	require.Equal(t, approvedAlert.ID, retriedAlert.ID, "a retry from the same immutable Automation source must reuse its Alert")

	graph, err := NewAutomationGraphService(automationRepo).GetLive(ctx, project.ID, published.Definition.Automation.ID, time.Now())
	require.NoError(t, err)
	for _, node := range graph.Nodes {
		if node.NodeKey == "decision" {
			require.Equal(t, 1, node.Counts.Waiting, "the real pending Alert must wait on the configured human gate")
		}
	}
	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, approvedAlert.ID, models.AlertDecisionApproved))

	rejectedAlert, err := alertSvc.CreateActionable(runtimeCtx, &models.Alert{ProjectID: project.ID, SourceTaskID: &task.ID, Type: "change_proposal", Title: "Reject this", IdempotencyKey: "custom-approval-no"})
	require.NoError(t, err)
	require.NoError(t, alertSvc.SetDecision(ctx, project.ID, rejectedAlert.ID, models.AlertDecisionRejected))

	for _, expected := range []struct {
		edgeKey string
		count   int
	}{{"inspect_ask", 2}, {"ask_decision", 2}, {"decision_yes", 1}, {"decision_no", 1}} {
		var transitions int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_transitions tr
			JOIN automation_edges e ON e.id = tr.edge_id WHERE tr.automation_id = ? AND e.edge_key = ?`,
			published.Definition.Automation.ID, expected.edgeKey).Scan(&transitions))
		require.Equal(t, expected.count, transitions, expected.edgeKey+" must carry the exact native Alert transitions")
	}
	graph, err = NewAutomationGraphService(automationRepo).GetLive(ctx, project.ID, published.Definition.Automation.ID, time.Now())
	require.NoError(t, err)
	require.Equal(t, 1, graph.ActiveWorkItems, "the still-active chained Agent task remains distinct from both terminal Alert decisions")
	for _, key := range []string{"go_ahead", "stop_here"} {
		for _, node := range graph.Nodes {
			if node.NodeKey == key {
				require.Equal(t, 1, node.Counts.CompletedRecently, key+" must show the real human decision outcome")
			}
		}
	}
}

func TestCustomAutomationPublicationCompilesLinearTaskHandoffIntoExistingTaskChain(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	project := automationTestProject(t, repository.NewProjectRepo(db), "Custom task handoff")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Research then implement"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Daily", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "08:30", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "research", Name: "Research", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Research the requested change.", "category": "scheduled", "priority": 2}},
		{Key: "implement", Name: "Implement", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the researched change.", "category": "active", "priority": 3}},
		{Key: "done", Name: "Done", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_research", From: "schedule", To: "research", Condition: map[string]any{}},
		{Key: "research_implement", From: "research", To: "implement", Condition: map[string]any{}},
		{Key: "implement_done", From: "implement", To: "done", Condition: map[string]any{}},
	}
	created, err := drafts.CreateDraft(ctx, AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Empty(t, created.ValidationErrors)

	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(ctx, AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)

	resources := map[string]string{}
	for _, resource := range published.Definition.Resources {
		resources[resource.NodeKey+":"+resource.ResourceType] = resource.ResourceID
	}
	research, err := taskRepo.GetByID(ctx, resources["research:task"])
	require.NoError(t, err)
	implement, err := taskRepo.GetByID(ctx, resources["implement:task"])
	require.NoError(t, err)
	require.NotNil(t, research)
	require.NotNil(t, implement)
	require.Equal(t, models.StatusPending, research.Status)
	require.Equal(t, models.StatusBlocked, implement.Status)
	require.NotNil(t, implement.ParentTaskID)
	require.Equal(t, research.ID, *implement.ParentTaskID)
	chain, err := research.ParseChainConfig()
	require.NoError(t, err)
	require.True(t, chain.Enabled)
	require.Equal(t, "on_completion", chain.Trigger)
	require.Equal(t, implement.ID, chain.ChildTaskID)
	require.Equal(t, "implement", chain.ChildAutomationNodeKey)
	require.Equal(t, string(models.CategoryActive), chain.ChildCategory)
	require.Equal(t, "Implement the researched change.", chain.ChildPromptPrefix)

	chain.ChildTaskID = repository.NewID()
	chain.ChildAutomationNodeKey = "newer_version_target"
	require.NoError(t, research.SetChainConfig(chain))
	require.NoError(t, taskRepo.Update(ctx, research))
	research, err = taskRepo.GetByID(ctx, research.ID)
	require.NoError(t, err)

	before := tableCount(t, db, "tasks")
	_, err = compiler.Publish(ctx, AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, before, tableCount(t, db, "tasks"), "publication retry must reuse the same chained tasks")

	var researchNodeID, implementNodeID, outcomeNodeID string
	for _, node := range published.Definition.Nodes {
		switch node.NodeKey {
		case "research":
			researchNodeID = node.ID
		case "implement":
			implementNodeID = node.ID
		case "done":
			outcomeNodeID = node.ID
		}
	}
	require.NotEmpty(t, researchNodeID)
	require.NotEmpty(t, implementNodeID)
	require.NotEmpty(t, outcomeNodeID)
	execRepo := repository.NewExecutionRepo(db)
	parentExecution := models.Execution{TaskID: research.ID, Status: models.ExecCompleted, PromptSent: research.Prompt}
	require.NoError(t, execRepo.Create(ctx, &parentExecution))
	binding := models.AutomationBinding{AutomationID: published.Definition.Automation.ID, VersionID: published.Definition.Version.ID, NodeID: researchNodeID}
	runtimeContext := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}})
	runtimeContext = withAutomationExecution(runtimeContext, research.ID, parentExecution.ID)
	workerSvc := NewWorkerService(nil, 0, nil)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), workerSvc)
	llmSvc := NewLLMService(repository.NewLLMConfigRepo(db), execRepo, taskRepo, repository.NewProjectRepo(db), scheduleRepo, repository.NewAttachmentRepo(db))
	llmSvc.SetTaskService(taskSvc)
	llmSvc.SetAutomationRepo(automationRepo)
	require.NoError(t, llmSvc.triggerTaskChain(runtimeContext, *research, "Research findings"))
	activated, err := taskRepo.GetByID(ctx, implement.ID)
	require.NoError(t, err)
	require.Equal(t, models.StatusPending, activated.Status)
	require.Equal(t, models.CategoryActive, activated.Category)
	require.Equal(t, "Implement the researched change.\n\nResearch findings", activated.Prompt)
	childContext, err := automationRepo.ContextForTask(ctx, project.ID, activated.ID)
	require.NoError(t, err)
	require.Len(t, childContext.Bindings, 1)
	require.Equal(t, implementNodeID, childContext.Bindings[0].NodeID)
	require.NotEmpty(t, childContext.Bindings[0].WorkItemID)
	var handoffs int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ? AND from_node_id = ? AND to_node_id = ?`,
		published.Definition.Automation.ID, researchNodeID, implementNodeID).Scan(&handoffs))
	require.Equal(t, 1, handoffs)
	require.NoError(t, llmSvc.triggerTaskChain(runtimeContext, *research, "Research findings"), "retry must resolve the persisted handoff")
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ? AND from_node_id = ? AND to_node_id = ?`,
		published.Definition.Automation.ID, researchNodeID, implementNodeID).Scan(&handoffs))
	require.Equal(t, 1, handoffs)

	require.NoError(t, taskRepo.UpdateStatus(ctx, activated.ID, models.StatusRunning))
	secondParentExecution := models.Execution{TaskID: research.ID, Status: models.ExecCompleted, PromptSent: research.Prompt}
	require.NoError(t, execRepo.Create(ctx, &secondParentExecution))
	secondRuntimeContext := WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}})
	secondRuntimeContext = withAutomationExecution(secondRuntimeContext, research.ID, secondParentExecution.ID)
	require.ErrorIs(t, llmSvc.triggerTaskChain(secondRuntimeContext, *research, "New research findings"), repository.ErrAutomationChainChildBusy)
	var blocked int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_transitions WHERE automation_id = ? AND from_node_id = ? AND to_node_id = ? AND state = 'blocked'`,
		published.Definition.Automation.ID, researchNodeID, implementNodeID).Scan(&blocked))
	require.Equal(t, 1, blocked, "a busy fixed child must leave visible blocked provenance instead of losing the handoff")

	childExecution := models.Execution{TaskID: activated.ID, Status: models.ExecRunning, PromptSent: activated.Prompt}
	require.NoError(t, execRepo.Create(ctx, &childExecution))
	childBinding := childContext.Bindings[0]
	_, _, err = automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: childContext, Binding: childBinding,
		ActivityKey: "execution:" + childExecution.ID + ":run", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: activated.ID}, {ResourceType: "execution", ResourceID: childExecution.ID}},
	})
	require.NoError(t, err)
	require.NoError(t, automationRepo.FinalizeExecutionProjection(ctx, project.ID, childExecution.ID, models.ExecCompleted))
	var terminalState string
	require.NoError(t, db.QueryRow(`SELECT state FROM automation_transitions WHERE automation_id = ? AND from_node_id = ? AND to_node_id = ?`,
		published.Definition.Automation.ID, implementNodeID, outcomeNodeID).Scan(&terminalState))
	require.Equal(t, string(models.AutomationTransitionCompleted), terminalState)
}

func TestAutomationPublicationRejectsStalePlanAndLifecycleTouchesOwnedTriggersOnly(t *testing.T) {
	automationobs.ResetForTest()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Lifecycle")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)

	candidate.Name = "Changed after preview"
	_, err = drafts.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, candidate)
	require.NoError(t, err)
	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, planner)
	_, err = compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.ErrorContains(t, err, "stale publication plan")
	require.Zero(t, tableCount(t, db, "tasks"))

	fresh, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: fresh.PlanRevision})
	require.NoError(t, err)
	ownedSchedule := publishedScheduleID(t, published)

	sharedTask, sharedSchedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Unrelated shared worker")
	_ = sharedTask
	lifecycle := NewAutomationLifecycleService(automationRepo, scheduleRepo)
	require.NoError(t, lifecycle.Pause(context.Background(), project.ID, created.Definition.Automation.ID))
	owned, err := scheduleRepo.GetByID(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.False(t, owned.Enabled)
	shared, err := scheduleRepo.GetByID(context.Background(), sharedSchedule.ID)
	require.NoError(t, err)
	require.True(t, shared.Enabled, "unowned schedules must remain untouched")
	require.NoError(t, lifecycle.Resume(context.Background(), project.ID, created.Definition.Automation.ID))
	owned, err = scheduleRepo.GetByID(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.True(t, owned.Enabled)
	require.NoError(t, lifecycle.Archive(context.Background(), project.ID, created.Definition.Automation.ID))
	owned, err = scheduleRepo.GetByID(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.False(t, owned.Enabled)
	owner, err := automationRepo.GetTriggerOwner(context.Background(), ownedSchedule)
	require.NoError(t, err)
	require.Nil(t, owner, "archive must release exclusive trigger ownership after disabling it")
	require.ErrorContains(t, lifecycle.Resume(context.Background(), project.ID, created.Definition.Automation.ID), "archived")
	metrics := automationobs.Snapshot()
	require.Equal(t, uint64(1), metrics["automation.lifecycle.paused"].Count)
	require.Equal(t, uint64(1), metrics["automation.lifecycle.resumed"].Count)
	require.Equal(t, uint64(1), metrics["automation.lifecycle.archived"].Count)
}

func TestAutomationDeleteDisablesOwnedTriggersAndRemovesOnlyAutomationRecords(t *testing.T) {
	automationobs.ResetForTest()
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Delete Automation")
	other := automationTestProject(t, projectRepo, "Delete Automation Other")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	task, schedule := automationTestTaskAndSchedule(t, taskRepo, scheduleRepo, project.ID, "Delete-owned trigger")
	registration := NewAutomationRegistrationService(automationRepo, NewAutomationAdapterRegistry())
	definition, _, err := registration.Register(ctx, AutomationRegistrationRequest{
		ProjectID:  project.ID,
		AdapterKey: AutomationAdapterNativeSDLC,
		StableKey:  "native-sdlc/delete-test",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
		},
	})
	require.NoError(t, err)
	var triggerNodeID string
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_trigger" {
			triggerNodeID = node.ID
		}
	}
	require.NotEmpty(t, triggerNodeID)
	_, err = db.ExecContext(ctx, `INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, completed_at)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'delete-history', 'completed', CURRENT_TIMESTAMP)`, project.ID, definition.Automation.ID, definition.Version.ID, triggerNodeID, schedule.ID)
	require.NoError(t, err)

	lifecycle := NewAutomationLifecycleService(automationRepo, scheduleRepo)
	require.ErrorContains(t, lifecycle.Delete(ctx, other.ID, definition.Automation.ID), "not found")
	stillOwned, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.True(t, stillOwned.Enabled, "cross-project delete must not touch the owned trigger")

	require.NoError(t, lifecycle.Delete(ctx, project.ID, definition.Automation.ID))
	gone, err := automationRepo.GetDefinition(ctx, project.ID, definition.Automation.ID)
	require.NoError(t, err)
	require.Nil(t, gone)
	require.Zero(t, tableCountWhere(t, db, "automation_versions", "automation_id", definition.Automation.ID))
	require.Zero(t, tableCountWhere(t, db, "automation_invocations", "automation_id", definition.Automation.ID))
	require.Equal(t, 1, tableCountWhere(t, db, "tasks", "id", task.ID), "existing tasks remain authoritative")
	require.Equal(t, 1, tableCountWhere(t, db, "schedules", "id", schedule.ID), "existing schedules remain authoritative")
	preservedSchedule, err := scheduleRepo.GetByID(ctx, schedule.ID)
	require.NoError(t, err)
	require.False(t, preservedSchedule.Enabled)
	owner, err := automationRepo.GetTriggerOwner(ctx, schedule.ID)
	require.NoError(t, err)
	require.Nil(t, owner)
	metrics := automationobs.Snapshot()
	require.Equal(t, uint64(1), metrics["automation.lifecycle.deleted"].Count)
}

func tableCountWhere(t *testing.T, db *sql.DB, table, column, value string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ?`, value).Scan(&count))
	return count
}

func TestAutomationPublicationPlanGoldenRevisionExcludesLayoutAndMessages(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Golden")
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	definition, err := automationRepo.CreateAutomationDraft(context.Background(), repository.AutomationDraftWrite{
		ProjectID: project.ID, AutomationID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", VersionID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		StableKey: "golden/vision", Source: "template", Candidate: candidate,
	})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), registry, drafts)
	first, err := planner.Plan(context.Background(), project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, "d8e499a753cb4a29a55c6446648a4845a983444a0f3d31af6ff4979b43f631ca", first.PlanRevision)

	candidate.Assumptions = []string{"Layout-only author note"}
	candidate.Warnings = []string{"Operational observation"}
	_, err = drafts.UpdateDraft(context.Background(), definition.Automation.ID, definition.Version.ID, project.ID, candidate)
	require.NoError(t, err)
	second, err := planner.Plan(context.Background(), project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, first.PlanRevision, second.PlanRevision, "non-compilation messages must not stale a confirmed plan")
}

func TestAutomationPublicationPlanGoldenGitHubDependenciesAndConfigurationChanges(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{ID: "cccccccccccccccccccccccccccccccc", Name: "GitHub Golden", RepoURL: "https://github.com/example/repository"}
	_, err := db.Exec(`INSERT INTO projects (id, name, description, repo_path, repo_url) VALUES (?, ?, '', '', ?)`, project.ID, project.Name, project.RepoURL)
	require.NoError(t, err)
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "github-golden-token"))
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	userID := int64(42)
	inbox := &models.GitHubProjectInbox{ProjectID: project.ID, GitHubLogin: "dev-inbox", GitHubUserID: &userID, Enabled: true}
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, inbox))
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	definition, err := automationRepo.CreateAutomationDraft(ctx, repository.AutomationDraftWrite{
		ProjectID: project.ID, AutomationID: "dddddddddddddddddddddddddddddddd", VersionID: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		StableKey: "golden/github", Source: "template", Candidate: candidate,
	})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), registry, drafts)
	planner.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
	first, err := planner.Plan(ctx, project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, "9ffe54519fb72be9ab264b1ae0d090801ab3fff8848f7adab049e32112e12152", first.PlanRevision)

	inbox.Enabled = false
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, inbox))
	second, err := planner.Plan(ctx, project.ID, definition.Automation.ID, definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, second.Effects)
	require.Equal(t, "github_approval_inbox_unavailable", second.Validation[0].Code)
}

func TestAutomationGitHubPublicationRequiresExecutableIntegrationAndApprovalCapabilities(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "GitHub publication readiness")
	project.RepoURL = "https://github.com/example/automation-readiness"
	require.NoError(t, projectRepo.Update(ctx, &project))
	settingsRepo := repository.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	githubAuthRepo := repository.NewGitHubAuthRepo(db)
	automationRepo := repository.NewAutomationRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(ctx, AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, repository.NewTaskRepo(db, nil), repository.NewScheduleRepo(db), registry, drafts)
	planner.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)

	plan, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Effects, "an unavailable integration must produce no publication effects")
	require.Equal(t, "github_auth_unavailable", plan.Validation[0].Code)

	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "configured-token"))
	plan, err = planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Effects)
	require.Equal(t, "github_approval_inbox_unavailable", plan.Validation[0].Code)

	inbox := &models.GitHubProjectInbox{ProjectID: project.ID, GitHubLogin: "approver", Enabled: false}
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, inbox))
	plan, err = planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Effects)
	require.Equal(t, "github_approval_inbox_unavailable", plan.Validation[0].Code)

	inbox.Enabled = true
	require.NoError(t, githubAuthRepo.UpsertProjectInbox(ctx, inbox))
	project.RepoURL = ""
	require.NoError(t, projectRepo.Update(ctx, &project))
	plan, err = planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Effects)
	require.Equal(t, "github_repository_unavailable", plan.Validation[0].Code)

	project.RepoURL = "https://github.com/example/automation-readiness"
	require.NoError(t, projectRepo.Update(ctx, &project))
	plan, err = planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	require.NotEmpty(t, plan.Effects)

	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModeApp))
	plan, err = planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Effects)
	require.Equal(t, "github_auth_unavailable", plan.Validation[0].Code)
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAppID, "123"))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAppSlug, "automation-app"))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAppPrivateKey, "configured-private-key"))
	require.NoError(t, settingsRepo.Set(ctx, githubSettingInstallationID, "456"))
	plan, err = planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	require.NotEmpty(t, plan.Effects)
}

func TestAutomationPublicationScheduleIsJournaledDisabledBeforePublish(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Disabled trigger")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	drafts := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry())
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	effect := models.AutomationPublicationEffect{StepKey: "schedule:vision_trigger", Operation: "create", TargetKey: "schedule:vision_trigger", ResourceType: "schedule", Name: "Vision Trigger"}
	snapshot, err := automationRepo.ReservePublicationAttempt(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID, "prepublish", []models.AutomationPublicationEffect{effect})
	require.NoError(t, err)
	require.NoError(t, automationRepo.MarkPublicationStep(context.Background(), snapshot.Attempt.ID, effect.StepKey, "running", "", ""))
	task := models.Task{ProjectID: project.ID, Title: "Target", Prompt: "target", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending}
	require.NoError(t, taskRepo.Create(context.Background(), &task))
	runAt := time.Now().UTC().Add(time.Hour)
	schedule := models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, scheduleRepo.CreateForAutomationPublication(context.Background(), &schedule, snapshot.Attempt.ID, effect.StepKey))
	require.False(t, schedule.Enabled)
	stored, err := scheduleRepo.GetByID(context.Background(), schedule.ID)
	require.NoError(t, err)
	require.False(t, stored.Enabled, "an unpublished trigger must not be runnable")
	journal, err := automationRepo.GetPublicationAttempt(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID, "prepublish")
	require.NoError(t, err)
	require.Equal(t, schedule.ID, journal.Steps[0].ResourceID, "schedule creation and journal identity must commit together")
}

type blockingAutomationTaskMutationService struct {
	inner   automationTaskMutationService
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAutomationTaskMutationService) Create(ctx context.Context, task *models.Task) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
	}
	return s.inner.Create(ctx, task)
}

func (s *blockingAutomationTaskMutationService) Update(ctx context.Context, task *models.Task) error {
	return s.inner.Update(ctx, task)
}

type createThenFailAutomationTaskMutationService struct {
	inner automationTaskMutationService
	once  sync.Once
}

func (s *createThenFailAutomationTaskMutationService) Create(ctx context.Context, task *models.Task) error {
	var result error
	s.once.Do(func() {
		result = s.inner.Create(ctx, task)
		if result == nil {
			result = errors.New("simulated response loss after task commit")
		}
	})
	if result == nil {
		return s.inner.Create(ctx, task)
	}
	return result
}

func (s *createThenFailAutomationTaskMutationService) Update(ctx context.Context, task *models.Task) error {
	return s.inner.Update(ctx, task)
}

func TestAutomationPublicationLeaseSerializesCompilers(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Concurrent compiler")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	realTaskService := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	blocking := &blockingAutomationTaskMutationService{inner: realTaskService, started: make(chan struct{}), release: make(chan struct{})}
	firstCompiler := NewAutomationCompiler(automationRepo, blocking, taskRepo, scheduleRepo, planner)
	secondCompiler := NewAutomationCompiler(automationRepo, realTaskService, taskRepo, scheduleRepo, planner)
	request := AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision}

	firstResult := make(chan error, 1)
	go func() {
		_, publishErr := firstCompiler.Publish(context.Background(), request)
		firstResult <- publishErr
	}()
	select {
	case <-blocking.started:
	case <-time.After(2 * time.Second):
		t.Fatal("first compiler did not reach task creation")
	}
	_, err = secondCompiler.Publish(context.Background(), request)
	require.ErrorIs(t, err, repository.ErrAutomationPublicationInProgress)
	close(blocking.release)
	require.NoError(t, <-firstResult)
	require.Equal(t, 1, tableCount(t, db, "tasks"))
	require.Equal(t, 1, tableCount(t, db, "schedules"))
}

func TestAutomationPublicationAmbiguousTaskCreationReportsAndReconcilesResource(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Ambiguous compiler")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	realTaskService := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	failing := &createThenFailAutomationTaskMutationService{inner: realTaskService}
	compiler := NewAutomationCompiler(automationRepo, failing, taskRepo, scheduleRepo, planner)
	request := AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision}

	failed, err := compiler.Publish(context.Background(), request)
	require.ErrorContains(t, err, "response loss")
	require.NotNil(t, failed)
	require.Len(t, failed.Resources, 1)
	require.Equal(t, "ambiguous", failed.Resources[0].Status)
	require.NotEmpty(t, failed.Resources[0].ResourceID, "partial result must identify the committed visible task")
	require.Equal(t, models.AutomationVersionDraft, failed.Definition.Version.State)
	require.Equal(t, 1, tableCount(t, db, "tasks"))
	require.Zero(t, tableCount(t, db, "schedules"))

	retryCompiler := NewAutomationCompiler(automationRepo, realTaskService, taskRepo, scheduleRepo, planner)
	published, err := retryCompiler.Publish(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, models.AutomationVersionPublished, published.Definition.Version.State)
	require.Equal(t, 1, tableCount(t, db, "tasks"), "retry must reconcile the stable compiler task")
	require.Equal(t, 1, tableCount(t, db, "schedules"))
}

func TestAutomationResumePreservesConfiguredDisabledTrigger(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Disabled resume")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Nodes[0].Config["enabled"] = false
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	scheduleID := publishedScheduleID(t, published)
	stored, err := scheduleRepo.GetByID(context.Background(), scheduleID)
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	lifecycle := NewAutomationLifecycleService(automationRepo, scheduleRepo)
	require.NoError(t, lifecycle.Pause(context.Background(), project.ID, created.Definition.Automation.ID))
	require.NoError(t, lifecycle.Resume(context.Background(), project.ID, created.Definition.Automation.ID))
	stored, err = scheduleRepo.GetByID(context.Background(), scheduleID)
	require.NoError(t, err)
	require.False(t, stored.Enabled, "resume must restore the published trigger configuration, not enable every owner row")
}

func TestAutomationReplacementFailureKeepsPriorTaskBehaviorAndSuccessfulRetrySwitchesTrigger(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Replacement publication")
	project.RepoPath = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project.RepoPath, "VISION.md"), []byte("vision"), 0o600))
	require.NoError(t, projectRepo.Update(ctx, &project))
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	agentRepo := repository.NewAgentRepo(db)
	firstAgent := models.Agent{Name: "First Architect", Key: "first_architect", Scope: models.AgentScopeProject, ProjectID: project.ID, Enabled: true, SelectableAsPrimary: true,
		Skills: []models.SkillConfig{{Name: "project-guidance", Description: "Guide", Content: "safe"}}}
	secondAgent := models.Agent{Name: "Second Architect", Key: "second_architect", Scope: models.AgentScopeProject, ProjectID: project.ID, Enabled: true, SelectableAsPrimary: true,
		Skills: []models.SkillConfig{{Name: "implementation", Description: "Implement", Content: "safe"}}}
	require.NoError(t, agentRepo.Create(ctx, &firstAgent))
	require.NoError(t, agentRepo.Create(ctx, &secondAgent))
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, taskRepo, repository.NewSettingsRepo(db))
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	for i := range candidate.Nodes {
		if _, ok := candidate.Nodes[i].Config["prompt"]; ok {
			candidate.Nodes[i].Config["agent_ref"] = "first_architect"
			candidate.Nodes[i].Config["skills"] = []any{"first_architect:project-guidance"}
			candidate.Nodes[i].Config["source_files"] = []any{"VISION.md"}
		}
	}
	created, err := drafts.CreateDraft(ctx, AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	planner.SetAgentRepository(agentRepo)
	taskService := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	compiler := NewAutomationCompiler(automationRepo, taskService, taskRepo, scheduleRepo, planner)
	compiler.SetAgentRepository(agentRepo)
	plan, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.True(t, publicationPlanHasEffect(plan, "agent", firstAgent.ID, "reuse"))
	require.True(t, publicationPlanHasEffect(plan, "skill", "first_architect:project-guidance", "reuse"))
	require.True(t, publicationPlanHasEffect(plan, "source_file", "VISION.md", "reuse"))
	first, err := compiler.Publish(ctx, AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	firstScheduleID := publishedScheduleID(t, first)
	firstTaskID := publishedTaskID(t, first)
	priorTask, err := taskRepo.GetByID(ctx, firstTaskID)
	require.NoError(t, err)
	priorPrompt := priorTask.Prompt
	require.Contains(t, priorPrompt, "Configured Agent skills:\n- project-guidance")
	require.Contains(t, priorPrompt, "Focus source files:\n- VISION.md")
	require.NotNil(t, priorTask.AgentDefinitionID)
	require.Equal(t, firstAgent.ID, *priorTask.AgentDefinitionID)

	cloned, err := drafts.ClonePublishedVersion(ctx, project.ID, created.Definition.Automation.ID)
	require.NoError(t, err)
	operationalBaseline, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, cloned.Definition.Version.ID)
	require.NoError(t, err)
	lastRun := time.Now().UTC()
	nextRun := lastRun.Add(24 * time.Hour)
	require.NoError(t, scheduleRepo.MarkRan(ctx, firstScheduleID, lastRun, &nextRun))
	operationalUpdate, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, cloned.Definition.Version.ID)
	require.NoError(t, err)
	require.Equal(t, operationalBaseline.PlanRevision, operationalUpdate.PlanRevision, "volatile next-run and last-run state must not stale a publication plan")
	for i := range cloned.Candidate.Nodes {
		if _, ok := cloned.Candidate.Nodes[i].Config["prompt"]; ok {
			cloned.Candidate.Nodes[i].Config["prompt"] = cloned.Candidate.Nodes[i].Config["prompt"].(string) + " Updated."
			cloned.Candidate.Nodes[i].Config["agent_ref"] = "second_architect"
			cloned.Candidate.Nodes[i].Config["skills"] = []any{"second_architect:implementation"}
		}
		if _, ok := cloned.Candidate.Nodes[i].Config["run_at"]; ok {
			cloned.Candidate.Nodes[i].Config["run_at"] = "10:30"
		}
	}
	cloned, err = drafts.UpdateDraft(ctx, created.Definition.Automation.ID, cloned.Definition.Version.ID, project.ID, cloned.Candidate)
	require.NoError(t, err)
	replacementPlan, err := planner.Plan(ctx, project.ID, created.Definition.Automation.ID, cloned.Definition.Version.ID)
	require.NoError(t, err)

	_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_automation_replacement_schedule
		BEFORE INSERT ON schedules BEGIN SELECT RAISE(ABORT, 'simulated schedule failure'); END`)
	require.NoError(t, err)
	failed, err := compiler.Publish(ctx, AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: cloned.Definition.Version.ID, PlanRevision: replacementPlan.PlanRevision})
	require.ErrorContains(t, err, "simulated schedule failure")
	require.NotNil(t, failed)
	current, err := automationRepo.GetDefinition(ctx, project.ID, created.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, first.Definition.Version.ID, current.Version.ID)
	stillPriorTask, err := taskRepo.GetByID(ctx, firstTaskID)
	require.NoError(t, err)
	require.Equal(t, priorPrompt, stillPriorTask.Prompt, "failed replacement must not mutate behavior used by the active version")
	require.NotNil(t, stillPriorTask.AgentDefinitionID)
	require.Equal(t, firstAgent.ID, *stillPriorTask.AgentDefinitionID, "failed replacement must not switch the active task Agent")
	var stagedTaskStatus string
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM automation_publication_steps WHERE attempt_id = ? AND step_key LIKE 'task:%'`, failed.Attempt.ID).Scan(&stagedTaskStatus))
	require.Equal(t, "running", stagedTaskStatus, "a task update remains staged until version publication commits")
	firstSchedule, err := scheduleRepo.GetByID(ctx, firstScheduleID)
	require.NoError(t, err)
	require.True(t, firstSchedule.Enabled)

	_, err = db.ExecContext(ctx, `DROP TRIGGER fail_automation_replacement_schedule`)
	require.NoError(t, err)
	second, err := compiler.Publish(ctx, AutomationPublishRequest{ProjectID: project.ID, AutomationID: created.Definition.Automation.ID, VersionID: cloned.Definition.Version.ID, PlanRevision: replacementPlan.PlanRevision})
	require.NoError(t, err)
	require.Equal(t, cloned.Definition.Version.ID, second.Definition.Version.ID)
	require.Equal(t, 1, tableCount(t, db, "tasks"), "replacement reuses the owned task")
	updatedTask, err := taskRepo.GetByID(ctx, firstTaskID)
	require.NoError(t, err)
	require.NotEqual(t, priorPrompt, updatedTask.Prompt)
	require.NotNil(t, updatedTask.AgentDefinitionID)
	require.Equal(t, secondAgent.ID, *updatedTask.AgentDefinitionID, "successful replacement must atomically switch the task Agent")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT status FROM automation_publication_steps WHERE attempt_id = ? AND step_key LIKE 'task:%'`, second.Attempt.ID).Scan(&stagedTaskStatus))
	require.Equal(t, "completed", stagedTaskStatus)
	require.Equal(t, 2, tableCount(t, db, "schedules"), "cadence changes create a new exclusive trigger")
	firstSchedule, err = scheduleRepo.GetByID(ctx, firstScheduleID)
	require.NoError(t, err)
	require.False(t, firstSchedule.Enabled)
	newScheduleID := publishedScheduleID(t, second)
	require.NotEqual(t, firstScheduleID, newScheduleID)
	owner, err := automationRepo.GetTriggerOwner(ctx, newScheduleID)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, second.Definition.Version.ID, owner.VersionID)
}

func publicationPlanHasEffect(plan *models.AutomationPublicationPlan, resourceType, resourceID, operation string) bool {
	if plan == nil {
		return false
	}
	for _, effect := range plan.Effects {
		if effect.ResourceType == resourceType && effect.ResourceID == resourceID && effect.Operation == operation {
			return true
		}
	}
	return false
}

func tableCount(t *testing.T, db interface{ QueryRow(string, ...any) *sql.Row }, table string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
	return count
}

func publishedTaskID(t *testing.T, result *AutomationPublishResult) string {
	t.Helper()
	for _, resource := range result.Resources {
		if resource.ResourceType == "task" {
			return resource.ResourceID
		}
	}
	t.Fatal("published result did not include a task")
	return ""
}

func publishedScheduleID(t *testing.T, result *AutomationPublishResult) string {
	t.Helper()
	for _, resource := range result.Resources {
		if resource.ResourceType == "schedule" {
			return resource.ResourceID
		}
	}
	t.Fatal("published result did not include a schedule")
	return ""
}
