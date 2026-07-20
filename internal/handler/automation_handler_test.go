package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
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
	require.Contains(t, emptyHistory.Body.String(), "getComputedStyle(root).color")
	require.Contains(t, emptyHistory.Body.String(), "labels: { color: theme.text }")
	require.Contains(t, emptyHistory.Body.String(), `aria-label="Automation views"`)
	require.Contains(t, emptyHistory.Body.String(), `data-automation-view="history"`)
	require.Contains(t, emptyHistory.Body.String(), `aria-selected="true"`)

	portfolio := tc.HTTP().Get(fmt.Sprintf("/automations?project_id=%s", project.ID)).Execute()
	require.Equal(t, 200, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), "Native SDLC")
	require.Contains(t, portfolio.Body.String(), fmt.Sprintf(`href="/automations/%s?project_id=%s"`, definition.Automation.ID, project.ID), "published Automations must continue to open Live")
	require.Contains(t, portfolio.Body.String(), "Published autonomous processes")
	require.NotContains(t, portfolio.Body.String(), "Register Existing")

	detail := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, detail.Code)
	require.Contains(t, detail.Body.String(), "Active invocations")
	require.Contains(t, detail.Body.String(), "Node resources")
	require.Contains(t, detail.Body.String(), "Live automation graph")
	require.Contains(t, detail.Body.String(), `class="automation-graph-node automation-graph-node--idle"`)
	require.Contains(t, detail.Body.String(), `@keyframes automation-running-pulse`)
	require.Contains(t, detail.Body.String(), `.automation-graph-node--running {`)
	require.Contains(t, detail.Body.String(), `animation: automation-running-pulse`)
	require.Contains(t, detail.Body.String(), `@media (prefers-reduced-motion: reduce)`)
	require.Contains(t, detail.Body.String(), `.automation-graph-node--running { animation: none; }`)
	require.Contains(t, detail.Body.String(), `class="automation-node-content"`)
	require.Contains(t, detail.Body.String(), "No active work")
	require.Contains(t, detail.Body.String(), `viewBox="-`)
	require.NotContains(t, detail.Body.String(), "0 run · 0 wait · 0 block · 0 fail · 0 done")
	require.Contains(t, detail.Body.String(), `fill: oklch(var(--b2));`)
	require.Contains(t, detail.Body.String(), `fill: oklch(var(--bc));`)
	require.NotContains(t, detail.Body.String(), "fill-base-content")
	require.NotContains(t, detail.Body.String(), "fill-base-200")
	require.Contains(t, detail.Body.String(), "sse-automation-event")
	require.Contains(t, detail.Body.String(), `document.addEventListener('visibilitychange'`)
	require.Contains(t, detail.Body.String(), `htmx.trigger(root, 'automation-visible')`, "returning to a visible tab must immediately refetch the local projection")
	require.Contains(t, detail.Body.String(), `htmx.ajax('POST', root.dataset.externalRefreshUrl`, "visible-tab reconciliation must use the explicit cached external refresh endpoint")
	require.Contains(t, detail.Body.String(), `aria-label="Automation views"`)
	require.Contains(t, detail.Body.String(), `data-automation-view="live"`)
	require.Contains(t, detail.Body.String(), `aria-selected="true"`)
	require.Contains(t, detail.Body.String(), `id="delete-automation-modal"`)
	require.Contains(t, detail.Body.String(), "Delete automation")
	require.Contains(t, detail.Body.String(), "owned trigger schedules will be disabled")
	require.NotContains(t, detail.Body.String(), ">Archive<")
	require.NotContains(t, detail.Body.String(), "/archive?")
	require.NotContains(t, detail.Body.String(), task.Prompt)

	livePartial := tc.HTMX().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, livePartial.Code)
	require.Contains(t, livePartial.Body.String(), `id="automation-live"`)

	definitionView := tc.HTMX().Get(fmt.Sprintf("/automations/%s/definition?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, definitionView.Code)
	require.Contains(t, definitionView.Body.String(), "Published definition")
	require.Contains(t, definitionView.Body.String(), "Native Producer")
	require.Contains(t, definitionView.Body.String(), `class="automation-graph-node automation-graph-node--idle"`)
	require.Contains(t, definitionView.Body.String(), `class="automation-node-content"`)
	require.Contains(t, definitionView.Body.String(), `viewBox="-`)
	require.Contains(t, definitionView.Body.String(), `aria-label="Automation views"`)
	require.Contains(t, definitionView.Body.String(), `data-automation-view="definition"`)
	require.Contains(t, definitionView.Body.String(), `aria-selected="true"`)
	require.NotContains(t, definitionView.Body.String(), "fill-base-content")
	require.NotContains(t, definitionView.Body.String(), "fill-base-200")

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
	_, err = tc.db.Exec(`UPDATE automations SET health_state = 'degraded', health_reason = 'One recent task failure' WHERE id = ?`, definition.Automation.ID)
	require.NoError(t, err)
	portfolio = tc.HTTP().Get(fmt.Sprintf("/automations?project_id=%s", project.ID)).Execute()
	require.Equal(t, 200, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), `data-automation-counter="running"`)
	require.Contains(t, portfolio.Body.String(), "1 running")
	require.Contains(t, portfolio.Body.String(), "One recent task failure")
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

func TestAutomationNodeResourcesResolveEverySupportedDetailDestination(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Node Resource Destinations").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)

	task := models.Task{ProjectID: project.ID, Title: "Resource task", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "private"}
	require.NoError(t, tc.taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &schedule))
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native/destinations", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: task.ID},
	}})
	require.NoError(t, err)
	var producer models.AutomationNode
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_producer" {
			producer = node
			break
		}
	}
	require.NotEmpty(t, producer.ID)

	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "private"}
	require.NoError(t, tc.execRepo.Create(ctx, &execution))
	alert := models.Alert{ProjectID: project.ID, Type: models.AlertCustom, Severity: models.SeverityInfo, Title: "Exact alert", Source: "test"}
	require.NoError(t, tc.alertRepo.Create(ctx, &alert))
	_, err = tc.db.ExecContext(ctx, `INSERT INTO task_goals (task_id, goal_id, objective, status) VALUES (?, 'goal-destination', 'private', 'active')`, task.ID)
	require.NoError(t, err)
	var workflowID, workflowExecutionID string
	require.NoError(t, tc.db.QueryRowContext(ctx, `INSERT INTO workflows (project_id, name) VALUES (?, 'Destination workflow') RETURNING id`, project.ID).Scan(&workflowID))
	require.NoError(t, tc.db.QueryRowContext(ctx, `INSERT INTO workflow_executions (workflow_id, task_id) VALUES (?, ?) RETURNING id`, workflowID, task.ID).Scan(&workflowExecutionID))

	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: producer.ID}
	_, _, err = automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "resource-destinations:item", ActivityKey: "resource-destinations", ActivityType: "test", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{
			{ResourceType: "task", ResourceID: task.ID},
			{ResourceType: "execution", ResourceID: execution.ID},
			{ResourceType: "alert", ResourceID: alert.ID},
			{ResourceType: "goal", ResourceID: task.ID},
			{ResourceType: "workflow_execution", ResourceID: workflowExecutionID},
			{ResourceType: "github_issue", ResourceID: "github:example/runtime:issue:6"},
			{ResourceType: "pull_request", ResourceID: "github:example/runtime:pull:7"},
			{ResourceType: "review", ResourceID: "github:example/runtime:review:7:99"},
		},
	})
	require.NoError(t, err)
	project.RepoURL = "https://github.com/example/runtime"
	require.NoError(t, tc.projectRepo.Update(ctx, project))
	pullRequests := repository.NewTaskPullRequestRepo(tc.db)
	pull := models.TaskPullRequest{TaskID: task.ID, PRNumber: 7, PRURL: "https://github.com/example/runtime/pull/7", PRState: "open"}
	require.NoError(t, pullRequests.Upsert(ctx, &pull))
	staleAt := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	_, err = tc.db.ExecContext(ctx, `UPDATE task_pull_requests SET updated_at = datetime(?) WHERE id = ?`, staleAt, pull.ID)
	require.NoError(t, err)
	refreshCalls := 0
	github := &fakeGitHubService{
		resolveRepoFn: func(context.Context, string, string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
		},
		getPullRequestFn: func(context.Context, *service.GitHubRepoRef, int) (*service.GitHubPullRequest, error) {
			refreshCalls++
			return &service.GitHubPullRequest{Number: 7, URL: pull.PRURL, State: "open"}, nil
		},
	}
	tc.handler.SetAutomationExternalStateService(service.NewAutomationExternalStateService(automationRepo, pullRequests, tc.projectRepo, github))

	response := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s", definition.Automation.ID, producer.ID, project.ID)).Execute()
	require.Equal(t, http.StatusOK, response.Code)
	body := html.UnescapeString(response.Body.String())
	for _, destination := range []string{
		"/tasks/" + task.ID,
		"/executions/" + execution.ID,
		"/alerts?project_id=" + url.QueryEscape(project.ID) + "&alert_id=" + url.QueryEscape(alert.ID),
		"/tasks/" + task.ID + "?project_id=" + url.QueryEscape(project.ID) + "#task-goal-panel",
		"/workflows/executions/" + workflowExecutionID,
		"https://github.com/example/runtime/issues/6",
		"https://github.com/example/runtime/pull/7",
		"https://github.com/example/runtime/pull/7#pullrequestreview-99",
	} {
		require.Contains(t, body, destination)
	}
	alertDetail := tc.HTTP().Get("/alerts?project_id=" + project.ID + "&alert_id=" + alert.ID).Execute()
	require.Equal(t, http.StatusOK, alertDetail.Code)
	require.Contains(t, alertDetail.Body.String(), "Exact alert")
	require.Equal(t, 1, strings.Count(alertDetail.Body.String(), `data-alert-id="`), "alert detail destination must resolve one exact project-scoped notification")

	live := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, http.StatusOK, live.Code)
	require.Contains(t, live.Body.String(), `data-automation-external-state`)
	require.Contains(t, live.Body.String(), `data-external-refresh-url="/automations/`+definition.Automation.ID+`/refresh-external?project_id=`+project.ID+`"`)
	require.Contains(t, live.Body.String(), ">Stale<")
	require.Contains(t, live.Body.String(), "Refresh GitHub state")
	refreshed := tc.HTMX().Post(fmt.Sprintf("/automations/%s/refresh-external?project_id=%s", definition.Automation.ID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusOK, refreshed.Code)
	require.Equal(t, 1, refreshCalls)
	require.Contains(t, refreshed.Body.String(), ">Fresh<")
}

func TestAutomationNodeResourcesUseStableBoundedPagination(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Node Resource Pagination").Build()
	other := tc.CreateProject().WithName("Node Resource Pagination Other").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)

	triggerTask := models.Task{ProjectID: project.ID, Title: "Trigger", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "trigger"}
	require.NoError(t, tc.taskRepo.Create(ctx, &triggerTask))
	schedule := models.Schedule{TaskID: triggerTask.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &schedule))
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native/pagination", Resources: []models.AutomationResourceBinding{
		{NodeKey: "suggestion_trigger", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "suggestion_producer", ResourceType: "task", ResourceID: triggerTask.ID},
	}})
	require.NoError(t, err)
	var driver models.AutomationNode
	var otherNode models.AutomationNode
	for _, node := range definition.Nodes {
		if node.NodeKey == "suggestion_producer" {
			driver = node
		} else if otherNode.ID == "" {
			otherNode = node
		}
	}
	require.NotEmpty(t, driver.ID)
	require.NotEmpty(t, otherNode.ID)
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: driver.ID}
	planRows, err := tc.db.Query(`EXPLAIN QUERY PLAN SELECT ar.id FROM automation_activities a
		JOIN automation_activity_resources ar ON ar.activity_id = a.id
		WHERE a.project_id = ? AND a.automation_id = ? AND a.version_id = ? AND a.node_id = ?
		ORDER BY a.started_at DESC, a.id DESC, ar.id DESC LIMIT 51`, project.ID, definition.Automation.ID, definition.Version.ID, driver.ID)
	require.NoError(t, err)
	var queryPlan strings.Builder
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, planRows.Scan(&id, &parent, &unused, &detail))
		queryPlan.WriteString(detail)
		queryPlan.WriteByte('\n')
	}
	require.NoError(t, planRows.Close())
	require.Contains(t, queryPlan.String(), "idx_automation_activities_node_resources")
	require.Contains(t, queryPlan.String(), "idx_automation_activity_resources_activity_page")
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for i := 0; i < 51; i++ {
		task := models.Task{ProjectID: project.ID, Title: fmt.Sprintf("Resource %02d", i), Category: models.CategoryBacklog, Priority: 2, Status: models.StatusPending, Prompt: "must not render"}
		require.NoError(t, tc.taskRepo.Create(ctx, &task))
		_, activity, recordErr := automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
			Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
			WorkItemKey: fmt.Sprintf("pagination:item:%02d", i), ActivityKey: fmt.Sprintf("pagination:activity:%02d", i),
			ActivityType: "pagination", ActivityStatus: models.AutomationActivityCompleted,
			Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
		})
		require.NoError(t, recordErr)
		_, recordErr = tc.db.Exec(`UPDATE automation_activities SET started_at = ? WHERE id = ?`, base.Add(time.Duration(i)*time.Second).Format("2006-01-02 15:04:05"), activity.ID)
		require.NoError(t, recordErr)
	}

	first := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s", definition.Automation.ID, driver.ID, project.ID)).Execute()
	require.Equal(t, 200, first.Code)
	require.Contains(t, first.Body.String(), "Resource 50")
	require.Contains(t, first.Body.String(), "Resource 01")
	require.NotContains(t, first.Body.String(), "Resource 00")
	require.Equal(t, 50, strings.Count(first.Body.String(), `data-automation-node-resource`))
	require.Contains(t, first.Body.String(), "Next resources")
	cursorMatch := regexp.MustCompile(`cursor=([^"&]+)`).FindStringSubmatch(first.Body.String())
	require.Len(t, cursorMatch, 2)

	second := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s&cursor=%s", definition.Automation.ID, driver.ID, project.ID, cursorMatch[1])).Execute()
	require.Equal(t, 200, second.Code)
	require.Contains(t, second.Body.String(), "Resource 00")
	require.NotContains(t, second.Body.String(), "Resource 50")
	require.Equal(t, 1, strings.Count(second.Body.String(), `data-automation-node-resource`))
	require.NotContains(t, second.Body.String(), "Next resources")

	bad := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s&cursor=tampered", definition.Automation.ID, driver.ID, project.ID)).Execute()
	require.Equal(t, 400, bad.Code)
	crossNode := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s&cursor=%s", definition.Automation.ID, otherNode.ID, project.ID, cursorMatch[1])).Execute()
	require.Equal(t, 400, crossNode.Code)
	foreign := tc.HTMX().Get(fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s", definition.Automation.ID, driver.ID, other.ID)).Execute()
	require.Equal(t, 404, foreign.Code)
}

func TestAutomationDraftPortfolioSelectionOpensBuilderAndNamePersists(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Draft Selection Project").Build()
	other := tc.CreateProject().WithName("Draft Selection Other").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	drafts := service.NewAutomationDraftService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, nil, nil, nil, nil)

	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	candidate.Name = "Customer Intake"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "collect", Name: "Collect request", Type: models.AutomationNodeHumanGate, Role: "custom_human_gate", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
		{Key: "review", Name: "Review request", Type: models.AutomationNodeAgentTask, Role: "custom_agent_task", Config: map[string]any{"prompt": "Review the request", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 280, Y: 0}},
	}
	candidate.Edges = []models.AutomationDraftEdge{{Key: "collect_review", From: "collect", To: "review", Condition: map[string]any{}}}
	created, err := drafts.CreateDraft(context.Background(), service.AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	canonicalURL := fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", created.Definition.Automation.ID, created.Definition.Version.ID, project.ID)

	portfolio := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, 200, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), canonicalURL, "selecting an unpublished Automation must open its working graph")
	require.NotContains(t, portfolio.Body.String(), fmt.Sprintf(`href="/automations/%s?project_id=%s"`, created.Definition.Automation.ID, project.ID))

	direct := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", created.Definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 303, direct.Code)
	require.Equal(t, canonicalURL, direct.Header().Get("Location"), "copied draft detail URLs must resolve to the builder")

	selected := tc.HTMX().Get(fmt.Sprintf("/automations/%s?project_id=%s", created.Definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, selected.Code)
	require.Equal(t, canonicalURL, selected.Header().Get("HX-Push-Url"))
	require.Contains(t, selected.Body.String(), `id="automation-builder"`)
	require.Contains(t, selected.Body.String(), `data-node-key="collect"`)
	require.Contains(t, selected.Body.String(), `data-edge-key="collect_review"`)
	require.Contains(t, selected.Body.String(), `name="automation_name"`)
	require.Contains(t, selected.Body.String(), "Delete connection")

	foreignDetail := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", created.Definition.Automation.ID, other.ID)).Execute()
	require.Equal(t, 404, foreignDetail.Code)
	foreignDraft := tc.HTTP().Get(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", created.Definition.Automation.ID, created.Definition.Version.ID, other.ID)).Execute()
	require.Equal(t, 404, foreignDraft.Code)

	rawCandidate, err := json.Marshal(candidate)
	require.NoError(t, err)
	renamed := tc.HTMX().Post(canonicalURL).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawCandidate)}, "automation_name": {"Support Triage"},
	}).Execute()
	require.Equal(t, 200, renamed.Code)
	require.Contains(t, renamed.Body.String(), `value="Support Triage"`)
	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	saved, err := metadata.Candidate()
	require.NoError(t, err)
	require.Equal(t, "Support Triage", saved.Name)
	require.Len(t, saved.Edges, 1, "renaming must preserve the graph")
}

func TestAutomationBlankBuilderIsEmptyInteractiveAndPersistsNodeActions(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Blank Builder Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, nil, nil, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	newPage := tc.HTTP().Get("/automations/new?project_id=" + project.ID).Execute()
	require.Equal(t, 200, newPage.Code)
	require.NotContains(t, newPage.Body.String(), `aria-label="Blank topology"`)
	require.NotContains(t, newPage.Body.String(), `<option value="vision_driver">Vision Driver</option><option value="native_sdlc">Native SDLC</option>`)
	require.Contains(t, newPage.Body.String(), "Build your own runnable Automation")
	require.Contains(t, newPage.Body.String(), "Open custom builder")
	created := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, 200, created.Code)
	require.Contains(t, created.Body.String(), `data-automation-draft-canvas`)
	require.Contains(t, created.Body.String(), `data-automation-node-tool`)
	require.Contains(t, created.Body.String(), `data-automation-add-node-open`, "blank canvas must expose an obvious add-node action")
	require.Contains(t, created.Body.String(), `data-automation-add-first-node`, "empty canvas must expose an in-canvas first-node action")
	require.Contains(t, created.Body.String(), `data-automation-node-dialog`, "node creation must use a reliable native dialog")
	require.Contains(t, created.Body.String(), `data-automation-disconnect-edge`, "canvas toolbar must expose connection removal")
	require.Contains(t, created.Body.String(), `data-automation-fit`)
	require.Contains(t, created.Body.String(), `data-automation-reset`)
	require.Contains(t, created.Body.String(), `name="candidate_json"`)
	require.Contains(t, created.Body.String(), "Add node")
	require.Contains(t, created.Body.String(), "Connect from either side")
	require.Contains(t, created.Body.String(), `data-automation-validation-summary`)
	require.NotContains(t, created.Body.String(), "Draft needs attention")
	require.NotContains(t, created.Body.String(), `alert alert-warning`, "incomplete graph guidance must not dominate the workspace")
	require.Contains(t, created.Body.String(), `min-h-[calc(100dvh-15rem)]`, "builder canvas must use the available viewport")
	require.NotContains(t, created.Body.String(), `xl:grid-cols-[minmax(0,1fr)_18rem]`, "node creation must not permanently narrow the canvas")
	require.Contains(t, created.Body.String(), "Save changes")
	require.Contains(t, created.Body.String(), `data-delete-automation-open`, "unpublished builders must expose Automation deletion")
	require.Contains(t, created.Body.String(), `id="delete-automation-modal"`)
	require.Contains(t, created.Body.String(), `/automations/`, "delete confirmation must submit through the project-scoped lifecycle route")
	require.NotContains(t, created.Body.String(), "Save draft")
	require.NotContains(t, created.Body.String(), "Suggested nodes", "blank drafts must not show a template-derived node list")
	require.Contains(t, created.Body.String(), `data-automation-create-node`, "blank drafts must create named nodes directly")
	require.Contains(t, created.Body.String(), "Add a Schedule and an Agent task")
	require.NotContains(t, created.Body.String(), "required nodes remain")
	require.NotContains(t, created.Body.String(), "required connections remain")
	require.NotContains(t, created.Body.String(), "Add transitions")
	require.NotContains(t, created.Body.String(), `class="automation-draft-node"`, "blank canvas must not start with template nodes")

	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.id, v.id FROM automations a JOIN automation_versions v ON v.automation_id = a.id WHERE a.project_id = ?`, project.ID).Scan(&automationID, &versionID))
	require.Contains(t, created.Body.String(), fmt.Sprintf(`/automations/%s/delete?project_id=%s`, automationID, project.ID))
	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err := metadata.Candidate()
	require.NoError(t, err)
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)

	added := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"create_node"}, "node_kind": {"schedule"}, "node_name": {"Schedule"},
	}).Execute()
	require.Equal(t, 200, added.Code)
	require.Contains(t, added.Body.String(), `data-node-key="schedule"`)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Len(t, candidate.Nodes, 1)

	candidateJSON, err = json.Marshal(candidate)
	require.NoError(t, err)
	added = tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"create_node"}, "node_kind": {"agent_task"}, "node_name": {"Task"},
	}).Execute()
	require.Equal(t, 200, added.Code)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Contains(t, added.Body.String(), `data-connect-port="schedule"`)
	require.Contains(t, added.Body.String(), `data-connect-port="task"`)
	require.Equal(t, 4, strings.Count(added.Body.String(), `class="automation-connect-handle" data-connect-port=`), "each node must connect from either side")
	require.Contains(t, added.Body.String(), `data-delete-node`, "nodes must be deletable on the canvas")
	candidateJSON, err = json.Marshal(candidate)
	require.NoError(t, err)
	invalidConnection := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"connect_nodes"}, "from_key": {"schedule"}, "to_key": {"schedule"},
	}).Execute()
	require.Equal(t, 400, invalidConnection.Code)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Empty(t, candidate.Edges, "invalid endpoint pairs must not persist arbitrary transitions")
	candidateJSON, err = json.Marshal(candidate)
	require.NoError(t, err)
	connected := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"connect_nodes"}, "from_key": {"schedule"}, "to_key": {"task"},
	}).Execute()
	require.Equal(t, 200, connected.Code)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Len(t, candidate.Nodes, 2)
	require.Len(t, candidate.Edges, 1)
	require.Contains(t, connected.Body.String(), `data-delete-edge`, "connections must be deletable on the canvas")
	require.Contains(t, connected.Body.String(), `data-reconnect-edge`, "connections must expose draggable endpoints")

	candidate.Edges[0].Label = "Keep this transition label"
	candidateJSON, err = json.Marshal(candidate)
	require.NoError(t, err)
	unchanged := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)},
	}).Execute()
	require.Equal(t, 200, unchanged.Code)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Equal(t, "Keep this transition label", candidate.Edges[0].Label, "palette actions must preserve fields absent from their submitted form")

	deleted := tc.HTMX().Post(fmt.Sprintf("/automations/%s/delete?project_id=%s", automationID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 204, deleted.Code)
	require.Equal(t, "/automations?project_id="+project.ID, deleted.Header().Get("HX-Redirect"))
	gone, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestAutomationBlankBuildsCustomRunnableTaskAndSchedule(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Publishable Custom Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationPublicationPlanner(automationRepo, tc.taskRepo, tc.scheduleRepo, registry, drafts)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, nil, nil, nil)

	created := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, 200, created.Code)
	require.Contains(t, created.Body.String(), `name="node_kind"`)
	require.Contains(t, created.Body.String(), "Node purpose")
	require.Contains(t, created.Body.String(), `value="schedule"`)
	require.Contains(t, created.Body.String(), `value="agent_task"`)
	require.Contains(t, created.Body.String(), `value="create_notification"`)
	require.Contains(t, created.Body.String(), `value="human_approval"`)
	require.Contains(t, created.Body.String(), `value="create_github_issue"`)
	require.Contains(t, created.Body.String(), `value="human_assignment"`)
	require.Contains(t, created.Body.String(), `value="github_inbox"`)
	require.Contains(t, created.Body.String(), `value="implementation"`)
	require.Contains(t, created.Body.String(), `value="open_pull_request"`)
	require.Contains(t, created.Body.String(), `value="human_review"`)
	require.Contains(t, created.Body.String(), `value="outcome"`)
	require.Contains(t, created.Body.String(), "Custom")
	require.NotContains(t, created.Body.String(), "Runtime behavior")
	require.NotContains(t, created.Body.String(), "Design-only type")
	require.NotContains(t, created.Body.String(), `name="runtime_node_key"`)
	require.NotContains(t, created.Body.String(), "Vision Schedule")
	require.NotContains(t, created.Body.String(), "Suggested nodes")

	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.id, v.id FROM automations a JOIN automation_versions v ON v.automation_id = a.id WHERE a.project_id = ?`, project.ID).Scan(&automationID, &versionID))
	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err := metadata.Candidate()
	require.NoError(t, err)
	require.Equal(t, service.AutomationAdapterCustom, candidate.AdapterKey)
	post := func(values url.Values) string {
		t.Helper()
		raw, marshalErr := json.Marshal(candidate)
		require.NoError(t, marshalErr)
		values.Set("project_id", project.ID)
		values.Set("candidate_json", string(raw))
		response := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(values).Execute()
		require.Equal(t, 200, response.Code, response.Body.String())
		metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
		require.NoError(t, err)
		candidate, err = metadata.Candidate()
		require.NoError(t, err)
		return response.Body.String()
	}

	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"schedule"}, "node_name": {"Weekday review"}})
	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"agent_task"}, "node_name": {"Review support queue"}})
	require.Len(t, candidate.Nodes, 2)
	require.Equal(t, models.AutomationNodeTrigger, candidate.Nodes[0].Type)
	require.Equal(t, models.AutomationNodeAgentTask, candidate.Nodes[1].Type)
	post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {candidate.Nodes[0].Key}, "to_key": {candidate.Nodes[1].Key}})

	require.Len(t, candidate.Edges, 1)
	require.Equal(t, candidate.Nodes[1].Key, candidate.Nodes[0].Config["target_node_key"])
	require.Empty(t, drafts.ValidateCandidate(candidate), "a user-defined Schedule → Agent task graph must be publishable")
	plan, err := planner.Plan(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	require.Len(t, plan.Effects, 3)
	var effectTypes []string
	for _, effect := range plan.Effects {
		effectTypes = append(effectTypes, effect.ResourceType)
	}
	require.ElementsMatch(t, []string{"task", "task", "schedule"}, effectTypes)

	notificationHTML := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"create_notification"}, "node_name": {"Request approval"}})
	require.Contains(t, notificationHTML, `name="node_request_approval_notification_type"`)
	require.Contains(t, notificationHTML, "The Alert is created only when that task runs")
	require.Equal(t, "create_notification", candidate.Nodes[2].Role)
	approvalHTML := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"human_approval"}, "node_name": {"Human decision"}})
	require.Contains(t, approvalHTML, "Native Alert approval")
	require.Equal(t, "native_approval", candidate.Nodes[3].Role)
	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"outcome"}, "node_name": {"Approved"}})
	post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {candidate.Nodes[3].Key}, "to_key": {candidate.Nodes[4].Key}})
	edge := candidate.Edges[len(candidate.Edges)-1]
	conditionHTML := post(url.Values{"edge_" + edge.Key + "_state": {"approved"}})
	require.Contains(t, conditionHTML, "Human result")
	require.Equal(t, map[string]any{"state": "approved"}, candidate.Edges[len(candidate.Edges)-1].Condition)

	githubIssueHTML := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"create_github_issue"}, "node_name": {"Open suggestion issue"}})
	require.Contains(t, githubIssueHTML, `name="node_open_suggestion_issue_labels"`)
	require.Contains(t, githubIssueHTML, "Assignment is intentionally unavailable here")
	require.Equal(t, "create_github_issue", candidate.Nodes[len(candidate.Nodes)-1].Role)
	assignmentHTML := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"human_assignment"}, "node_name": {"Assigned by human"}})
	require.Contains(t, assignmentHTML, "GitHub assignment is the approval signal")
	require.Equal(t, "github_assignment", candidate.Nodes[len(candidate.Nodes)-1].Role)
	assignmentKey := candidate.Nodes[len(candidate.Nodes)-1].Key
	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"github_inbox"}, "node_name": {"Assigned issue inbox"}})
	require.Equal(t, "github_inbox", candidate.Nodes[len(candidate.Nodes)-1].Role)
	inboxKey := candidate.Nodes[len(candidate.Nodes)-1].Key
	post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {assignmentKey}, "to_key": {inboxKey}})
	assignmentEdge := candidate.Edges[len(candidate.Edges)-1]
	require.Empty(t, assignmentEdge.Condition, "a newly connected human gate must persist before its result is selected")
	assignedHTML := post(url.Values{"edge_" + assignmentEdge.Key + "_state": {"assigned"}})
	require.Contains(t, assignedHTML, "Assigned in GitHub")
	require.Equal(t, map[string]any{"state": "assigned"}, candidate.Edges[len(candidate.Edges)-1].Condition)
	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"implementation"}, "node_name": {"Issue implementation"}})
	require.Equal(t, "implementation", candidate.Nodes[len(candidate.Nodes)-1].Role)
	prHTML := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"open_pull_request"}, "node_name": {"Open review PR"}})
	require.Contains(t, prHTML, `name="node_open_review_pr_base"`)
	require.Contains(t, prHTML, "Human review and merge remain outside Automation authority")
	require.Equal(t, "open_pull_request", candidate.Nodes[len(candidate.Nodes)-1].Role)
	reviewHTML := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"human_review"}, "node_name": {"Human PR review"}})
	require.Contains(t, reviewHTML, "Automation only observes the linked PR")
	require.Equal(t, "pull_request_review", candidate.Nodes[len(candidate.Nodes)-1].Role)
}

func TestAutomationBlankAppliedScheduleUsesScheduleNodeNameOnSchedulePage(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Automation Schedule Projection").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationPublicationPlanner(automationRepo, tc.taskRepo, tc.scheduleRepo, registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("schedule-projection-secret-32bytes"))
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, confirmation, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	created := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, 200, created.Code, created.Body.String())

	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.id, v.id FROM automations a JOIN automation_versions v ON v.automation_id = a.id WHERE a.project_id = ?`, project.ID).Scan(&automationID, &versionID))
	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err := metadata.Candidate()
	require.NoError(t, err)
	post := func(values url.Values) {
		t.Helper()
		raw, marshalErr := json.Marshal(candidate)
		require.NoError(t, marshalErr)
		values.Set("project_id", project.ID)
		values.Set("candidate_json", string(raw))
		response := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(values).Execute()
		require.Equal(t, 200, response.Code, response.Body.String())
		metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
		require.NoError(t, err)
		candidate, err = metadata.Candidate()
		require.NoError(t, err)
	}
	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"schedule"}, "node_name": {"Weekday review"}})
	post(url.Values{"builder_action": {"create_node"}, "node_kind": {"agent_task"}, "node_name": {"Review support queue"}})
	post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {candidate.Nodes[0].Key}, "to_key": {candidate.Nodes[1].Key}})

	planPage := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s/plan?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 200, planPage.Code, planPage.Body.String())
	tokenMatch := regexp.MustCompile(`name="confirmation_token" value="([^"]+)"`).FindStringSubmatch(planPage.Body.String())
	require.Len(t, tokenMatch, 2)
	revisionMatch := regexp.MustCompile(`name="plan_revision" value="([^"]+)"`).FindStringSubmatch(planPage.Body.String())
	require.Len(t, revisionMatch, 2)
	published := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s/publish?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "plan_revision": {revisionMatch[1]}, "confirmation_token": {tokenMatch[1]},
	}).Execute()
	require.Equal(t, 204, published.Code, published.Body.String())

	var scheduleTaskID, agentTaskID string
	require.NoError(t, tc.db.QueryRow(`SELECT resource_id FROM automation_definition_resources r
		JOIN automation_nodes n ON n.id = r.node_id AND n.version_id = r.version_id
		WHERE r.automation_id = ? AND r.version_id = ? AND n.node_key = ? AND r.resource_type = 'task'`,
		automationID, versionID, candidate.Nodes[0].Key).Scan(&scheduleTaskID))
	require.NoError(t, tc.db.QueryRow(`SELECT resource_id FROM automation_definition_resources r
		JOIN automation_nodes n ON n.id = r.node_id AND n.version_id = r.version_id
		WHERE r.automation_id = ? AND r.version_id = ? AND n.node_key = ? AND r.resource_type = 'task'`,
		automationID, versionID, candidate.Nodes[1].Key).Scan(&agentTaskID))
	require.NotEqual(t, scheduleTaskID, agentTaskID, "Schedule and Agent Task nodes must compile to distinct tasks")
	var linkedTaskID string
	require.NoError(t, tc.db.QueryRow(`SELECT task_id FROM schedules WHERE id IN
		(SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ?)`, automationID).Scan(&linkedTaskID))
	require.Equal(t, scheduleTaskID, linkedTaskID, "the Schedules page entry must be backed by the Schedule node's task")
	scheduleTask, err := tc.taskRepo.GetByID(context.Background(), scheduleTaskID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, scheduleTask.Category)
	agentTask, err := tc.taskRepo.GetByID(context.Background(), agentTaskID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, agentTask.Category, "the connected Agent Task must remain an ordinary task")

	schedulePage := tc.HTTP().Get("/schedule?project_id=" + project.ID).Execute()
	require.Equal(t, 200, schedulePage.Code, schedulePage.Body.String())
	require.Contains(t, schedulePage.Body.String(), `title="Weekday review"`, "the Schedules page must identify the Automation schedule node, not masquerade it as its target agent task")
	require.NotRegexp(t, `title="[^"]*Review support queue`, schedulePage.Body.String(), "the target agent task must not replace the Automation schedule identity on the calendar")
}

func TestAutomationBuilderSavesUnsupportedCustomConnectionsWithoutExecutingThem(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Freeform Builder Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	drafts := service.NewAutomationDraftService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, nil, nil, nil, nil)

	created := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, 200, created.Code)
	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.id, v.id FROM automations a JOIN automation_versions v ON v.automation_id = a.id WHERE a.project_id = ?`, project.ID).Scan(&automationID, &versionID))

	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err := metadata.Candidate()
	require.NoError(t, err)
	post := func(values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		raw, marshalErr := json.Marshal(candidate)
		require.NoError(t, marshalErr)
		values.Set("project_id", project.ID)
		values.Set("candidate_json", string(raw))
		response := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(values).Execute()
		if response.Code == 200 {
			metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
			require.NoError(t, err)
			candidate, err = metadata.Candidate()
			require.NoError(t, err)
		}
		return response
	}

	for _, node := range []struct{ name, purpose string }{{"Every morning", "schedule"}, {"Review project", "agent_task"}, {"Reviewed", "outcome"}} {
		response := post(url.Values{"builder_action": {"create_node"}, "node_name": {node.name}, "node_kind": {node.purpose}})
		require.Equal(t, 200, response.Code)
	}
	require.Len(t, candidate.Nodes, 3)
	keys := []string{candidate.Nodes[0].Key, candidate.Nodes[1].Key, candidate.Nodes[2].Key}
	for _, endpoints := range [][2]string{{keys[0], keys[1]}, {keys[1], keys[2]}, {keys[2], keys[0]}} {
		response := post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {endpoints[0]}, "to_key": {endpoints[1]}})
		require.Equal(t, 200, response.Code)
	}
	require.Len(t, candidate.Edges, 3)
	require.Contains(t, issueCodesHandler(candidate, drafts), "unsupported_handoff", "unsupported custom handoffs may be saved but must not publish")
}

func issueCodesHandler(candidate models.AutomationDraftCandidate, drafts *service.AutomationDraftService) []string {
	issues := drafts.ValidateCandidate(candidate)
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func TestAutomationBuilderWebFlowIsExplicitDraftOnlyAndProjectScoped(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Builder Project").Build()
	other := tc.CreateProject().WithName("Builder Other").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationPublicationPlanner(automationRepo, tc.taskRepo, tc.scheduleRepo, registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("handler-confirmation-secret-32-bytes"))
	lifecycle := service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo)
	agentRepo := repository.NewAgentRepo(tc.db)
	architect := models.Agent{Name: "Builder Architect", Key: "builder_architect", Scope: models.AgentScopeProject, ProjectID: project.ID,
		Enabled: true, SelectableAsPrimary: true, Skills: []models.SkillConfig{{Name: "project-guidance", Description: "Guide", Content: "safe"}}}
	require.NoError(t, agentRepo.Create(context.Background(), &architect))
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, agentRepo, tc.taskRepo, tc.settingsRepo)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	planner.SetAgentRepository(agentRepo)
	compiler.SetAgentRepository(agentRepo)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, planner, compiler, confirmation, lifecycle)

	newPage := tc.HTTP().Get("/automations/new?project_id=" + project.ID).Execute()
	require.Equal(t, 200, newPage.Code)
	for _, label := range []string{"Template", "Describe It", "Blank"} {
		require.Contains(t, newPage.Body.String(), label)
	}
	require.NotContains(t, newPage.Body.String(), "Register Existing")
	newPartial := tc.HTMX().Get("/automations/new?project_id=" + project.ID).Execute()
	require.Equal(t, 200, newPartial.Code)
	require.Contains(t, newPartial.Body.String(), `id="automation-new"`)
	require.NotContains(t, newPartial.Body.String(), "<!DOCTYPE html>")

	created := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"template"}, "template_key": {service.AutomationAdapterVisionDriver},
	}).Execute()
	require.Equal(t, 200, created.Code)
	require.Contains(t, created.Body.String(), `id="automation-builder"`)
	require.Contains(t, created.Body.String(), "Build the design, save your work, then apply it when it is ready to run")
	require.Contains(t, created.Body.String(), `class="automation-graph-node automation-graph-node--idle"`)
	require.Contains(t, created.Body.String(), `class="automation-node-content"`)
	require.Contains(t, created.Body.String(), `class="automation-draft-node`)
	require.Contains(t, created.Body.String(), `name="node_vision_driver_agent_ref"`)
	require.Contains(t, created.Body.String(), `value="builder_architect"`)
	require.Contains(t, created.Body.String(), `name="node_vision_driver_skills"`)
	require.Contains(t, created.Body.String(), `value="builder_architect:project-guidance"`)
	require.Contains(t, created.Body.String(), `name="node_vision_driver_source_files"`)
	require.NotContains(t, created.Body.String(), "fill-base-content")
	require.NotContains(t, created.Body.String(), "fill-base-200")
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.id, v.id FROM automations a JOIN automation_versions v ON v.automation_id = a.id WHERE a.project_id = ?`, project.ID).Scan(&automationID, &versionID))
	require.Equal(t, fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID), created.Header().Get("HX-Push-Url"), "draft creation must give HTMX history the canonical builder URL")
	configured := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "node_vision_driver_agent_ref": {"builder_architect"},
		"node_vision_driver_skills": {"builder_architect:project-guidance"}, "node_vision_driver_source_files": {},
	}).Execute()
	require.Equal(t, 200, configured.Code)
	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	configuredCandidate, err := metadata.Candidate()
	require.NoError(t, err)
	require.Equal(t, "builder_architect", configuredCandidate.Nodes[1].Config["agent_ref"])
	require.Equal(t, []any{"builder_architect:project-guidance"}, configuredCandidate.Nodes[1].Config["skills"])
	directPlan, err := planner.Plan(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	require.Empty(t, directPlan.Validation, "configured builder references must remain publishable: %+v", directPlan.Validation)
	planPage := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s/plan?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 200, planPage.Code)
	require.Contains(t, planPage.Body.String(), "Review changes")
	require.Contains(t, planPage.Body.String(), "create task")
	require.Zero(t, tableCountHandler(t, tc, "tasks"), "planning must remain read-only")
	tokenMatch := regexp.MustCompile(`name="confirmation_token" value="([^"]+)"`).FindStringSubmatch(planPage.Body.String())
	require.Len(t, tokenMatch, 2)
	revisionMatch := regexp.MustCompile(`name="plan_revision" value="([^"]+)"`).FindStringSubmatch(planPage.Body.String())
	require.Len(t, revisionMatch, 2)

	published := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s/publish?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "plan_revision": {revisionMatch[1]}, "confirmation_token": {tokenMatch[1]},
	}).Execute()
	require.Equal(t, 204, published.Code)
	require.Contains(t, published.Header().Get("HX-Redirect"), "/automations/"+automationID)
	require.NotZero(t, tableCountHandler(t, tc, "tasks"))
	require.NotZero(t, tableCountHandler(t, tc, "schedules"))
	compiledTasks, err := tc.taskRepo.ListByProject(context.Background(), project.ID, "")
	require.NoError(t, err)
	require.NotEmpty(t, compiledTasks)
	for _, task := range compiledTasks {
		require.NotNil(t, task.AgentDefinitionID)
		require.Equal(t, architect.ID, *task.AgentDefinitionID)
	}

	live := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", automationID, project.ID)).Execute()
	require.Equal(t, 200, live.Code)
	require.Contains(t, live.Body.String(), ">Edit automation</button>")
	require.NotContains(t, live.Body.String(), "Edit as new draft")
	cloned := tc.HTMX().Post("/automations/" + automationID + "/drafts?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 200, cloned.Code)
	require.Contains(t, cloned.Body.String(), `id="automation-builder"`)
	var publishedCount, draftCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'published'`, automationID).Scan(&publishedCount))
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, automationID).Scan(&draftCount))
	require.Equal(t, 1, publishedCount)
	require.Equal(t, 1, draftCount)
	var clonedVersionID string
	require.NoError(t, tc.db.QueryRow(`SELECT id FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, automationID).Scan(&clonedVersionID))
	require.Equal(t, fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, clonedVersionID, project.ID), cloned.Header().Get("HX-Push-Url"), "cloning must not cache draft DOM under the published automation URL")

	foreign := tc.HTTP().Post("/automations/" + automationID + "/drafts?project_id=" + other.ID).WithForm(url.Values{"project_id": {other.ID}}).Execute()
	require.Equal(t, 404, foreign.Code)
	foreignDraft := tc.HTTP().Get(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, clonedVersionID, other.ID)).Execute()
	require.Equal(t, 404, foreignDraft.Code)
	foreignPlan := tc.HTTP().Post(fmt.Sprintf("/automations/%s/drafts/%s/plan?project_id=%s", automationID, clonedVersionID, other.ID)).WithForm(url.Values{"project_id": {other.ID}}).Execute()
	require.Equal(t, 404, foreignPlan.Code)
	foreignPublish := tc.HTTP().Post(fmt.Sprintf("/automations/%s/drafts/%s/publish?project_id=%s", automationID, clonedVersionID, other.ID)).WithForm(url.Values{"project_id": {other.ID}, "plan_revision": {"foreign"}, "confirmation_token": {"foreign"}}).Execute()
	require.Equal(t, 404, foreignPublish.Code)

	var ownedScheduleID string
	require.NoError(t, tc.db.QueryRow(`SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ?`, automationID).Scan(&ownedScheduleID))
	taskCountBeforeDelete := tableCountHandler(t, tc, "tasks")
	scheduleCountBeforeDelete := tableCountHandler(t, tc, "schedules")
	foreignDelete := tc.HTMX().Post(fmt.Sprintf("/automations/%s/delete?project_id=%s", automationID, other.ID)).WithForm(url.Values{"project_id": {other.ID}}).Execute()
	require.Equal(t, 404, foreignDelete.Code)
	stillPresent, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
	require.NoError(t, err)
	require.NotNil(t, stillPresent)

	deleted := tc.HTMX().Post(fmt.Sprintf("/automations/%s/delete?project_id=%s", automationID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 204, deleted.Code)
	require.Equal(t, "/automations?project_id="+project.ID, deleted.Header().Get("HX-Redirect"))
	gone, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
	require.NoError(t, err)
	require.Nil(t, gone)
	require.Equal(t, taskCountBeforeDelete, tableCountHandler(t, tc, "tasks"), "deleting an Automation must preserve existing tasks")
	require.Equal(t, scheduleCountBeforeDelete, tableCountHandler(t, tc, "schedules"), "deleting an Automation must preserve existing schedules")
	ownedSchedule, err := tc.scheduleRepo.GetByID(context.Background(), ownedScheduleID)
	require.NoError(t, err)
	require.NotNil(t, ownedSchedule)
	require.False(t, ownedSchedule.Enabled, "deleting an Automation must disable its owned trigger schedule")
}

func tableCountHandler(t *testing.T, tc *TestContext, table string) int {
	t.Helper()
	var count int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
	return count
}

func automationChatCustomApprovalCandidate(t *testing.T, drafts *service.AutomationDraftService) models.AutomationDraftCandidate {
	t.Helper()
	candidate, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Custom approval review"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "morning", Name: "Morning", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "review", Name: "Review changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review one focused change.", "category": "backlog", "priority": 2}},
		{Key: "notify", Name: "Request approval", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Summarize the proposed change."}},
		{Key: "approval", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "accepted", Name: "Accepted", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "rejected", Name: "Rejected", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "morning_review", From: "morning", To: "review", Condition: map[string]any{}},
		{Key: "review_notify", From: "review", To: "notify", Condition: map[string]any{}},
		{Key: "notify_approval", From: "notify", To: "approval", Condition: map[string]any{}},
		{Key: "approval_accepted", From: "approval", To: "accepted", Condition: map[string]any{"state": "approved"}},
		{Key: "approval_rejected", From: "approval", To: "rejected", Condition: map[string]any{"state": "rejected"}},
	}
	normalized, err := drafts.NormalizeCandidate(candidate)
	require.NoError(t, err)
	require.Empty(t, drafts.ValidateCandidate(normalized))
	return normalized
}

func TestAutomationChatDraftCreationRejectsCandidateIdentity(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Automation Chat Identity").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	drafts := service.NewAutomationDraftService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationBuilderServices(drafts, nil, nil, nil, nil, nil)
	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterVisionDriver)
	require.NoError(t, err)
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)

	_, err = tc.handler.executeAutomationCreateDraftAction(context.Background(), streamingResponseParams{ProjectID: project.ID},
		json.RawMessage(fmt.Sprintf(`{"source":"candidate","candidate":%s}`, raw)))
	require.ErrorContains(t, err, "template, describe, or blank")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
}

func TestAutomationChatActionsUseCanonicalPipelineAndDeferConfirmationReceipt(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation Chat").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationPublicationPlanner(automationRepo, tc.taskRepo, tc.scheduleRepo, registry, drafts)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("chat-confirmation-secret-32-bytes"))
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, planner, compiler, confirmation, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	model := models.LLMConfig{Name: "Automation parity generator", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))
	mock := testutil.NewMockLLMCaller()
	mock.Response = string(candidateJSON)
	tc.handler.llmSvc.SetLLMCaller(mock)
	webCreated := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"describe"}, "description": {"Review a proposed change and ask for approval"},
	}).Execute()
	require.Equal(t, 200, webCreated.Code, webCreated.Body.String())
	var webCandidateJSON string
	require.NoError(t, tc.db.QueryRow(`SELECT metadata.candidate_json FROM automation_draft_metadata metadata
		JOIN automations automation ON automation.id = metadata.automation_id
		WHERE metadata.project_id = ? AND automation.created_via = 'web' ORDER BY automation.created_at DESC LIMIT 1`, project.ID).Scan(&webCandidateJSON))
	createdJSON, err := tc.handler.executeAutomationCreateDraftAction(ctx, streamingResponseParams{ProjectID: project.ID}, json.RawMessage(`{"source":"describe","description":"Review vision daily"}`))
	require.NoError(t, err)
	var created struct {
		AutomationID string                          `json:"automation_id"`
		VersionID    string                          `json:"version_id"`
		Candidate    models.AutomationDraftCandidate `json:"candidate"`
		Active       bool                            `json:"active"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdJSON), &created))
	actualCandidateJSON, err := json.Marshal(created.Candidate)
	require.NoError(t, err)
	require.JSONEq(t, string(candidateJSON), string(actualCandidateJSON), "page and Chat fixed custom candidates must normalize identically")
	require.JSONEq(t, string(candidateJSON), webCandidateJSON, "Describe It on the page and Chat must use the same expanded custom contract")
	require.Equal(t, service.AutomationAdapterCustom, created.Candidate.AdapterKey)
	require.Equal(t, 2, mock.CallCount())
	require.False(t, created.Active)
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	chatTask := models.Task{ProjectID: project.ID, Title: "Automation planning chat", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, tc.taskRepo.Create(ctx, &chatTask))
	planExecution := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "plan it"}
	require.NoError(t, tc.execRepo.Create(ctx, &planExecution))
	collector := newChatActionSummaryCollector()
	planOutput, err := tc.handler.executeAutomationPlanAction(ctx, streamingResponseParams{ProjectID: project.ID, TaskID: chatTask.ID, ExecID: planExecution.ID, PrincipalID: "alice"},
		json.RawMessage(fmt.Sprintf(`{"automation_id":%q,"version_id":%q}`, created.AutomationID, created.VersionID)), collector)
	require.NoError(t, err)
	require.NotContains(t, planOutput, "confirmation_token")
	require.Len(t, collector.pendingAutomationPlan, 1)
	require.Zero(t, tableCountHandler(t, tc, "automation_chat_confirmation_receipts"), "receipt cannot precede durable assistant plan completion")
	storedOutput := collector.appendAutomationPlans("Plan ready.")
	require.Contains(t, storedOutput, "Automation publication plan")
	require.Contains(t, storedOutput, "Nothing has been created or activated")
	require.NoError(t, tc.execRepo.Complete(ctx, planExecution.ID, models.ExecCompleted, storedOutput, "", 0, 1))
	tc.handler.issueStoredAutomationPlanConfirmations(ctx, collector)
	require.Equal(t, 1, tableCountHandler(t, tc, "automation_chat_confirmation_receipts"))

	threadDefs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, false)
	for _, def := range threadDefs {
		require.NotEqual(t, "create_automation_draft", def.Name)
		require.NotEqual(t, "publish_automation_draft", def.Name)
	}
}

func TestAutomationCanonicalChatRuntimeExecutesPreviewDraftPlanAndConfirmedPublish(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation runtime actions").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationPublicationPlanner(automationRepo, tc.taskRepo, tc.scheduleRepo, registry, drafts)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("runtime-confirmation-secret-32-bytes"))
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, planner, compiler, confirmation, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	model := models.LLMConfig{Name: "Automation generator", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))
	mock := testutil.NewMockLLMCaller()
	mock.Response = string(candidateJSON)
	tc.handler.llmSvc.SetLLMCaller(mock)

	chatTask := models.Task{ProjectID: project.ID, Title: "Automation action thread", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, tc.taskRepo.Create(ctx, &chatTask))
	planExecution := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "plan automation"}
	require.NoError(t, tc.execRepo.Create(ctx, &planExecution))
	_, err = tc.db.Exec(`UPDATE executions SET started_at = ? WHERE id = ?`, time.Now().UTC().Add(-time.Minute), planExecution.ID)
	require.NoError(t, err)
	params := streamingResponseParams{ProjectID: project.ID, TaskID: chatTask.ID, ExecID: planExecution.ID, PrincipalID: "alice"}
	collector := newChatActionSummaryCollector()
	defs := chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true)
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, collector, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	execute := func(name string, input json.RawMessage) string {
		t.Helper()
		output, handled, isError, executeErr := runtime.Executor(ctx, name, input)
		require.NoError(t, executeErr)
		require.True(t, handled, "%s must execute through the canonical runtime", name)
		require.False(t, isError, "%s returned a tool error: %s", name, output)
		return output
	}
	previewOutput := execute("preview_automation_description", json.RawMessage(`{"description":"Review vision daily and request approval"}`))
	require.Contains(t, previewOutput, `"persisted":false`)
	require.Equal(t, 1, mock.CallCount())

	mock.Response = "not valid automation JSON"
	failedDescribe := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"describe"}, "description": {"Describe an unsupported draft"},
	}).Execute()
	require.Equal(t, 422, failedDescribe.Code)
	require.Contains(t, failedDescribe.Body.String(), `id="automation-new"`)
	require.Contains(t, failedDescribe.Body.String(), "automation generation repair failed")
	require.Contains(t, failedDescribe.Body.String(), "Generating and validating design")
	mock.Response = string(candidateJSON)

	createdOutput := execute("create_automation_draft", json.RawMessage(`{"source":"describe","description":"Review vision daily and request approval"}`))
	var created struct {
		AutomationID string `json:"automation_id"`
		VersionID    string `json:"version_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(createdOutput), &created))
	require.NotEmpty(t, created.AutomationID)
	require.Equal(t, 1, tableCountHandler(t, tc, "tasks"), "draft creation must not add a runtime task beyond the existing Chat thread")
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	planOutput := execute("plan_automation_publication", json.RawMessage(fmt.Sprintf(`{"automation_id":%q,"version_id":%q}`, created.AutomationID, created.VersionID)))
	require.Contains(t, planOutput, `"confirmation_required":true`)
	require.Len(t, collector.pendingAutomationPlan, 1)
	storedPlan := collector.appendAutomationPlans("Plan ready.")
	require.NoError(t, tc.execRepo.Complete(ctx, planExecution.ID, models.ExecCompleted, storedPlan, "", 0, 1))
	tc.handler.issueStoredAutomationPlanConfirmations(ctx, collector)

	confirming := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "publish " + candidate.Name}
	require.NoError(t, tc.execRepo.Create(ctx, &confirming))
	prepared, err := confirmation.PrepareChatConfirmation(ctx, project.ID, "alice", chatTask.ID, confirming.ID, confirming.PromptSent)
	require.NoError(t, err)
	require.NotNil(t, prepared)
	publishedOutput := execute("publish_automation_draft", json.RawMessage(fmt.Sprintf(`{"automation_id":%q,"version_id":%q,"plan_revision":%q,"confirmation_token":%q,"confirming_user_input_id":%q}`,
		prepared.AutomationID, prepared.VersionID, prepared.PlanRevision, prepared.Token, prepared.ConfirmingUserInputID)))
	require.Contains(t, publishedOutput, `"active":true`)
	require.Equal(t, 3, tableCountHandler(t, tc, "tasks"), "publication adds the Schedule node's task and the ordinary Agent Task beside the Chat thread")
	require.Equal(t, 1, tableCountHandler(t, tc, "schedules"))
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
