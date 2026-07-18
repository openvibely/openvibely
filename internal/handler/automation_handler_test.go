package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAutomationPagesRenderRegisteredDefinitionsAndEnforceProject(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Automation Project").Build()
	other := tc.CreateProject().WithName("Other Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)

	task := models.Task{ProjectID: project.ID, Title: "Native Producer", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "produce notifications"}
	require.NoError(t, tc.taskRepo.Create(context.Background(), &task))
	runAt := time.Now().UTC().Add(time.Hour)
	schedule := models.Schedule{TaskID: task.ID, RunAt: runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(context.Background(), &schedule))
	definition, _, err := registration.Register(context.Background(), service.AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/default", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
	}})
	require.NoError(t, err)

	emptyHistory := tc.HTTP().Get(fmt.Sprintf("/automations/%s/history?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, emptyHistory.Code)
	require.Contains(t, emptyHistory.Body.String(), "No invocation history")
	require.Contains(t, emptyHistory.Body.String(), "No work-item history")
	require.Contains(t, emptyHistory.Body.String(), "No terminal invocation yet")

	portfolio := tc.HTTP().Get(fmt.Sprintf("/automations?project_id=%s", project.ID)).Execute()
	require.Equal(t, 200, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), "Native SDLC")
	require.Contains(t, portfolio.Body.String(), "Published autonomous processes")
	require.NotContains(t, portfolio.Body.String(), "Register Existing")

	detail := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, detail.Code)
	require.Contains(t, detail.Body.String(), "Active invocations")
	require.Contains(t, detail.Body.String(), "Node resources")
	require.Contains(t, detail.Body.String(), "Live automation graph")
	require.Contains(t, detail.Body.String(), "sse-automation-event")
	require.NotContains(t, detail.Body.String(), task.Prompt)

	livePartial := tc.HTMX().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, livePartial.Code)
	require.Contains(t, livePartial.Body.String(), `id="automation-live"`)

	definitionView := tc.HTMX().Get(fmt.Sprintf("/automations/%s/definition?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, definitionView.Code)
	require.Contains(t, definitionView.Body.String(), "Published definition")
	require.Contains(t, definitionView.Body.String(), "Native Producer")

	producerNode := definition.Nodes[0]
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_producer" {
			producerNode = node
		}
	}
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: producerNode.ID}
	item, _, err := automationRepo.RecordProjectionEvent(context.Background(), repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "handler:item", ActivityKey: "handler:activity", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
	})
	require.NoError(t, err)
	resources := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s", definition.Automation.ID, producerNode.ID, project.ID)).Execute()
	require.Equal(t, 200, resources.Code)
	require.Contains(t, resources.Body.String(), "Native Producer")
	require.NotContains(t, resources.Body.String(), task.Prompt)

	triggerNode := definition.Nodes[0]
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_trigger" {
			triggerNode = node
		}
	}
	var invocationID string
	require.NoError(t, tc.db.QueryRow(`INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id,
		 occurrence_key, status, started_at, completed_at)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'handler-history', 'completed', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		RETURNING id`, project.ID, definition.Automation.ID, definition.Version.ID, triggerNode.ID, schedule.ID).Scan(&invocationID))
	invocationBinding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID,
		InvocationID: invocationID, NodeID: producerNode.ID, WorkItemID: item.ID}
	_, _, err = automationRepo.RecordProjectionEvent(context.Background(), repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{invocationBinding}}, Binding: invocationBinding,
		WorkItemKey: "handler:item", ActivityKey: "handler:history", ActivityType: "history", ActivityStatus: models.AutomationActivityCompleted,
		EventKey: "handler:history:entered", ToNodeID: producerNode.ID, Transition: models.AutomationTransitionEntered,
		MetadataJSON: `{"provider_token":"must-not-render"}`,
	})
	require.NoError(t, err)

	history := tc.HTTP().Get(fmt.Sprintf("/automations/%s/history?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, history.Code)
	require.Contains(t, history.Body.String(), "Conversion funnel")
	require.Contains(t, history.Body.String(), "Average node duration")
	require.Contains(t, history.Body.String(), "initAutomationHistoryCharts")
	require.Contains(t, history.Body.String(), "destroyAutomationHistoryCharts")
	require.NotContains(t, history.Body.String(), task.Prompt)
	historyPartial := tc.HTMX().Get(fmt.Sprintf("/automations/%s/history?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, historyPartial.Code)
	require.Contains(t, historyPartial.Body.String(), `id="automation-history"`)
	require.NotContains(t, historyPartial.Body.String(), "<!DOCTYPE html>")

	invocationHistory := tc.HTTP().Get(fmt.Sprintf("/automations/%s/invocations/%s?project_id=%s", definition.Automation.ID, invocationID, project.ID)).Execute()
	require.Equal(t, 200, invocationHistory.Code)
	require.Contains(t, invocationHistory.Body.String(), "Occurrence activities")
	require.Contains(t, invocationHistory.Body.String(), "Invocation graph")
	require.NotContains(t, invocationHistory.Body.String(), task.Prompt)
	invocationPartial := tc.HTMX().Get(fmt.Sprintf("/automations/%s/invocations/%s?project_id=%s", definition.Automation.ID, invocationID, project.ID)).Execute()
	require.Equal(t, 200, invocationPartial.Code)
	require.Contains(t, invocationPartial.Body.String(), `id="automation-invocation-history"`)
	require.NotContains(t, invocationPartial.Body.String(), "<!DOCTYPE html>")
	workItemHistory := tc.HTTP().Get(fmt.Sprintf("/automations/%s/work-items/%s?project_id=%s", definition.Automation.ID, item.ID, project.ID)).Execute()
	require.Equal(t, 200, workItemHistory.Code)
	require.Contains(t, workItemHistory.Body.String(), "Cross-invocation lifetime")
	require.Contains(t, workItemHistory.Body.String(), "Frames come only from append-only persisted transitions")
	require.Contains(t, workItemHistory.Body.String(), "automation-replay-slider")
	require.NotContains(t, workItemHistory.Body.String(), "must-not-render")
	require.NotContains(t, workItemHistory.Body.String(), task.Prompt)
	workItemPartial := tc.HTMX().Get(fmt.Sprintf("/automations/%s/work-items/%s?project_id=%s", definition.Automation.ID, item.ID, project.ID)).Execute()
	require.Equal(t, 200, workItemPartial.Code)
	require.Contains(t, workItemPartial.Body.String(), `id="automation-work-item-history"`)
	require.NotContains(t, workItemPartial.Body.String(), "<!DOCTYPE html>")

	badCursor := tc.HTTP().Get(fmt.Sprintf("/automations/%s/history?project_id=%s&invocation_cursor=tampered", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 400, badCursor.Code)
	badStatus := tc.HTTP().Get(fmt.Sprintf("/automations/%s/history?project_id=%s&work_item_status=not-a-status", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 400, badStatus.Code)
	badActivityCursor := tc.HTTP().Get(fmt.Sprintf("/automations/%s/invocations/%s?project_id=%s&activity_cursor=tampered", definition.Automation.ID, invocationID, project.ID)).Execute()
	require.Equal(t, 400, badActivityCursor.Code)
	foreignHistory := tc.HTTP().Get(fmt.Sprintf("/automations/%s/history?project_id=%s", definition.Automation.ID, other.ID)).Execute()
	require.Equal(t, 404, foreignHistory.Code)
	foreignInvocation := tc.HTTP().Get(fmt.Sprintf("/automations/%s/invocations/%s?project_id=%s", definition.Automation.ID, invocationID, other.ID)).Execute()
	require.Equal(t, 404, foreignInvocation.Code)
	foreignWorkItem := tc.HTTP().Get(fmt.Sprintf("/automations/%s/work-items/%s?project_id=%s", definition.Automation.ID, item.ID, other.ID)).Execute()
	require.Equal(t, 404, foreignWorkItem.Code)

	foreign := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, other.ID)).Execute()
	require.Equal(t, 404, foreign.Code)
}

func TestAutomationSendToTaskPersistsCausalBindingsWithQueuedInput(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation queue project").Build()
	agent := models.LLMConfig{Name: "Automation queue model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &agent))
	agentID := agent.ID
	task := models.Task{ProjectID: project.ID, Title: "Automation queue task", Category: models.CategoryActive,
		Priority: 2, Status: models.StatusPending, Prompt: "causal task", AgentID: &agentID}
	require.NoError(t, tc.taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &schedule))
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{ProjectID: project.ID,
		AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/queue", Resources: []models.AutomationResourceBinding{
			{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
		}})
	require.NoError(t, err)
	producer := definition.Nodes[0]
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_producer" {
			producer = node
		}
	}
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: producer.ID}
	item, _, err := automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "queue:item", ActivityKey: "queue:source", ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
	})
	require.NoError(t, err)
	binding.WorkItemID = item.ID
	require.NoError(t, tc.taskRepo.UpdateStatus(ctx, task.ID, models.StatusRunning))
	active := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: task.Prompt}
	require.NoError(t, tc.execRepo.Create(ctx, &active))
	causalCtx := service.WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}})
	output, err := tc.handler.executeSendToTaskTool(causalCtx, streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true},
		json.RawMessage(fmt.Sprintf(`{"task_id":%q,"message":"continue causal work"}`, task.ID)))
	require.NoError(t, err)
	var result struct {
		QueuedMessageID string `json:"queued_message_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	require.NotEmpty(t, result.QueuedMessageID)
	loaded, err := automationRepo.ContextForThreadInput(ctx, project.ID, result.QueuedMessageID)
	require.NoError(t, err)
	require.Len(t, loaded.Bindings, 1)
	require.Equal(t, item.ID, loaded.Bindings[0].WorkItemID)
}
