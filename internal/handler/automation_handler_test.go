package handler

import (
	"context"
	"encoding/json"
	"fmt"
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
	require.Contains(t, detail.Body.String(), `class="automation-node-content"`)
	require.Contains(t, detail.Body.String(), "No active work")
	require.Contains(t, detail.Body.String(), `viewBox="-`)
	require.NotContains(t, detail.Body.String(), "0 run · 0 wait · 0 block · 0 fail · 0 done")
	require.Contains(t, detail.Body.String(), `fill: oklch(var(--b2));`)
	require.Contains(t, detail.Body.String(), `fill: oklch(var(--bc));`)
	require.NotContains(t, detail.Body.String(), "fill-base-content")
	require.NotContains(t, detail.Body.String(), "fill-base-200")
	require.Contains(t, detail.Body.String(), "sse-automation-event")
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
	require.Contains(t, created.Body.String(), "6 required nodes remain")
	require.Contains(t, created.Body.String(), "5 required connections remain")
	require.Equal(t, 1, strings.Count(created.Body.String(), "required nodes remain"))
	require.Equal(t, 1, strings.Count(created.Body.String(), "required connections remain"))
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
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"add_node"}, "node_key": {"vision_trigger"},
	}).Execute()
	require.Equal(t, 200, added.Code)
	require.Contains(t, added.Body.String(), `data-node-key="vision_trigger"`)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Len(t, candidate.Nodes, 1)

	candidateJSON, err = json.Marshal(candidate)
	require.NoError(t, err)
	added = tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"add_node"}, "node_key": {"vision_driver"},
	}).Execute()
	require.Equal(t, 200, added.Code)
	metadata, err = automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, automationID, versionID)
	require.NoError(t, err)
	candidate, err = metadata.Candidate()
	require.NoError(t, err)
	require.Contains(t, added.Body.String(), `data-connect-port="vision_trigger"`)
	require.Contains(t, added.Body.String(), `data-connect-port="vision_driver"`)
	require.Equal(t, 4, strings.Count(added.Body.String(), `class="automation-connect-handle" data-connect-port=`), "each node must connect from either side")
	require.Contains(t, added.Body.String(), `data-delete-node`, "nodes must be deletable on the canvas")
	candidateJSON, err = json.Marshal(candidate)
	require.NoError(t, err)
	invalidConnection := tc.HTMX().Post(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"connect_nodes"}, "from_key": {"vision_trigger"}, "to_key": {"vision_trigger"},
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
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"connect_nodes"}, "from_key": {"vision_trigger"}, "to_key": {"vision_driver"},
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
		"project_id": {project.ID}, "candidate_json": {string(candidateJSON)}, "builder_action": {"add_node"}, "node_key": {"vision_driver"},
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

func TestAutomationBuilderCreatesCustomNodesAndCyclicDraftConnections(t *testing.T) {
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

	for _, node := range []struct{ name, nodeType string }{{"Alpha", "agent_task"}, {"Beta", "condition"}, {"Gamma", "action"}} {
		response := post(url.Values{"builder_action": {"create_node"}, "node_name": {node.name}, "node_type": {node.nodeType}})
		require.Equal(t, 200, response.Code)
	}
	require.Len(t, candidate.Nodes, 3)
	keys := []string{candidate.Nodes[0].Key, candidate.Nodes[1].Key, candidate.Nodes[2].Key}
	for _, endpoints := range [][2]string{{keys[0], keys[1]}, {keys[1], keys[2]}, {keys[2], keys[0]}} {
		response := post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {endpoints[0]}, "to_key": {endpoints[1]}})
		require.Equal(t, 200, response.Code)
	}
	require.Len(t, candidate.Edges, 3)
	require.Contains(t, issueCodesHandler(candidate, drafts), "unsupported_topology", "freeform graph must be saved but remain unpublished")
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
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
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
	require.NotContains(t, created.Body.String(), "fill-base-content")
	require.NotContains(t, created.Body.String(), "fill-base-200")
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.id, v.id FROM automations a JOIN automation_versions v ON v.automation_id = a.id WHERE a.project_id = ?`, project.ID).Scan(&automationID, &versionID))
	require.Equal(t, fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, project.ID), created.Header().Get("HX-Push-Url"), "draft creation must give HTMX history the canonical builder URL")
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

func TestAutomationChatActionsUseCanonicalPipelineAndDeferConfirmationReceipt(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation Chat").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationPublicationPlanner(automationRepo, tc.taskRepo, tc.scheduleRepo, registry, drafts)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("chat-confirmation-secret-32-bytes"))
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, confirmation, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	createdJSON, err := tc.handler.executeAutomationCreateDraftAction(ctx, streamingResponseParams{ProjectID: project.ID}, json.RawMessage(fmt.Sprintf(`{"source":"candidate","candidate":%s}`, candidateJSON)))
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
	require.JSONEq(t, string(candidateJSON), string(actualCandidateJSON), "page and Chat fixed candidates must normalize identically")
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

	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterVisionDriver)
	require.NoError(t, err)
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

	createdOutput := execute("create_automation_draft", json.RawMessage(fmt.Sprintf(`{"source":"candidate","candidate":%s}`, candidateJSON)))
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
	require.Equal(t, 2, tableCountHandler(t, tc, "tasks"), "publication adds one visible Automation task beside the Chat thread")
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
