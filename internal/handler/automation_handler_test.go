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
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestAutomationPortfolioCardKebabEnablesAndDisablesInPlace(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Automation card lifecycle").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)
	tc.handler.SetAutomationBuilderServices(nil, nil, nil, nil, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	task := models.Task{ProjectID: project.ID, Title: "Card lifecycle schedule", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "run"}
	require.NoError(t, tc.taskRepo.Create(context.Background(), &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(context.Background(), &schedule))
	definition, _, err := registration.Register(context.Background(), service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/card-lifecycle",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)

	portfolio := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), fmt.Sprintf(`hx-post="/automations/%s/pause?project_id=%s"`, definition.Automation.ID, project.ID))
	require.Contains(t, portfolio.Body.String(), ">Disable</button>")
	require.NotContains(t, portfolio.Body.String(), ">Pause</button>")
	require.NotContains(t, portfolio.Body.String(), fmt.Sprintf(`hx-post="/automations/%s/resume?project_id=%s"`, definition.Automation.ID, project.ID))

	paused := tc.HTMX().Post(fmt.Sprintf("/automations/%s/pause?project_id=%s&lifecycle_state=active&sort=name_desc", definition.Automation.ID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "return_to": {"portfolio"},
	}).Execute()
	require.Equal(t, http.StatusOK, paused.Code, paused.Body.String())
	require.Contains(t, paused.Body.String(), `id="automations-container"`)
	require.Contains(t, paused.Body.String(), `data-card-pagination-url="/automations?lifecycle_state=active&amp;project_id=`+project.ID+`&amp;sort=name_desc"`)
	require.NotContains(t, paused.Body.String(), `data-card-select-id="`+definition.Automation.ID+`"`)
	storedSchedule, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
	require.NoError(t, err)
	require.False(t, storedSchedule.Enabled)

	pausedPortfolio := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, pausedPortfolio.Code, pausedPortfolio.Body.String())
	require.Contains(t, pausedPortfolio.Body.String(), fmt.Sprintf(`hx-post="/automations/%s/resume?project_id=%s"`, definition.Automation.ID, project.ID))
	require.Contains(t, pausedPortfolio.Body.String(), ">Enable</button>")
	require.NotContains(t, pausedPortfolio.Body.String(), ">Resume</button>")

	resumed := tc.HTMX().Post(fmt.Sprintf("/automations/%s/resume?project_id=%s&lifecycle_state=paused&sort=name_desc", definition.Automation.ID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "return_to": {"portfolio"},
	}).Execute()
	require.Equal(t, http.StatusOK, resumed.Code, resumed.Body.String())
	require.Contains(t, resumed.Body.String(), `data-card-pagination-url="/automations?lifecycle_state=paused&amp;project_id=`+project.ID+`&amp;sort=name_desc"`)
	require.NotContains(t, resumed.Body.String(), `data-card-select-id="`+definition.Automation.ID+`"`)
	storedSchedule, err = tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
	require.NoError(t, err)
	require.True(t, storedSchedule.Enabled)
}

func TestAutomationPortfolioRunNowQueuesManualDispatchWithoutChangingCadence(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Automation card run now").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	lifecycle := service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)
	tc.handler.SetAutomationBuilderServices(nil, nil, nil, nil, nil, lifecycle)

	task := models.Task{ProjectID: project.ID, Title: "Run now schedule", Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, Prompt: "persisted run now prompt"}
	require.NoError(t, tc.taskRepo.Create(context.Background(), &task))
	runAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	nextRun := runAt.Add(24 * time.Hour)
	schedule := models.Schedule{TaskID: task.ID, RunAt: runAt, NextRun: &nextRun, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, tc.scheduleRepo.Create(context.Background(), &schedule))
	definition, _, err := registration.Register(context.Background(), service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/card-run-now",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)
	before, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
	require.NoError(t, err)

	portfolio := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), fmt.Sprintf(`hx-post="/automations/%s/run-now?project_id=%s"`, definition.Automation.ID, project.ID))
	require.Contains(t, portfolio.Body.String(), ">Run now</button>")

	response := tc.HTMX().Post(fmt.Sprintf("/automations/%s/run-now?project_id=%s", definition.Automation.ID, project.ID)).WithForm(url.Values{
		"project_id": {project.ID}, "return_to": {"portfolio"},
	}).Execute()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `id="automations-container"`)
	hxTrigger := response.Header().Get("HX-Trigger")
	require.Contains(t, hxTrigger, "openvibelyToast")
	require.Contains(t, hxTrigger, definition.Automation.Name+" is now running.")
	require.Contains(t, hxTrigger, `"status":"info"`)
	require.Contains(t, hxTrigger, `"toast_key":"automation:`)
	require.Contains(t, hxTrigger, `"click_url":"/automations/`+definition.Automation.ID+`?project_id=`+project.ID+`"`)

	var triggerType, triggerResourceID, prompt string
	require.NoError(t, tc.db.QueryRow(`SELECT i.trigger_resource_type, i.trigger_resource_id, t.prompt
		FROM automation_invocations i JOIN automation_dispatch_outbox d ON d.invocation_id = i.id
		JOIN tasks t ON t.id = d.task_id WHERE i.automation_id = ?`, definition.Automation.ID).Scan(&triggerType, &triggerResourceID, &prompt))
	require.Equal(t, "manual", triggerType)
	require.Equal(t, schedule.ID, triggerResourceID)
	require.Equal(t, task.Prompt, prompt)
	after, err := tc.scheduleRepo.GetByID(context.Background(), schedule.ID)
	require.NoError(t, err)
	require.Equal(t, before.RunAt, after.RunAt)
	require.Equal(t, before.LastRun, after.LastRun)
	require.Equal(t, before.NextRun, after.NextRun)

	require.NoError(t, lifecycle.Pause(context.Background(), project.ID, definition.Automation.ID))
	paused := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, paused.Code)
	require.NotContains(t, paused.Body.String(), fmt.Sprintf(`/automations/%s/run-now`, definition.Automation.ID))
	denied := tc.HTTP().Post(fmt.Sprintf("/automations/%s/run-now?project_id=%s", definition.Automation.ID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusBadRequest, denied.Code)
}

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
		{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
	}})
	require.NoError(t, err)

	portfolio := tc.HTTP().Get(fmt.Sprintf("/automations?project_id=%s", project.ID)).Execute()
	require.Equal(t, 200, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), "Native SDLC")
	require.Contains(t, portfolio.Body.String(), fmt.Sprintf(`data-automation-url="/automations/%s?project_id=%s"`, definition.Automation.ID, project.ID), "published Automation cards must continue to open Live")
	require.Contains(t, portfolio.Body.String(), fmt.Sprintf(`data-automation-edit-url="/automations/%s/builder?project_id=%s"`, definition.Automation.ID, project.ID))
	require.Contains(t, portfolio.Body.String(), `onclick="event.stopPropagation(); openAutomationCardEdit(this)"`)
	require.Contains(t, portfolio.Body.String(), fmt.Sprintf(`data-automation-delete-url="/automations/%s/delete?project_id=%s"`, definition.Automation.ID, project.ID))
	require.NotContains(t, portfolio.Body.String(), "Published autonomous processes")
	require.Contains(t, portfolio.Body.String(), `data-card-search="automations"`)
	require.Contains(t, portfolio.Body.String(), `class="card-body relative"`)
	require.NotContains(t, portfolio.Body.String(), "Register Existing")

	detail := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, detail.Code)
	require.NotContains(t, detail.Body.String(), "Active invocations")
	require.NotContains(t, detail.Body.String(), "Open work items")
	require.NotContains(t, detail.Body.String(), ">Nodes<")
	require.NotContains(t, detail.Body.String(), "Recent since")
	require.NotContains(t, detail.Body.String(), "Node resources")
	require.NotContains(t, detail.Body.String(), `id="automation-node-resources"`)
	require.Contains(t, detail.Body.String(), fmt.Sprintf(`href="/tasks/%s?project_id=%s&amp;from=automation&amp;automation_id=%s&amp;automation_name=%s"`, task.ID, url.QueryEscape(project.ID), url.QueryEscape(definition.Automation.ID), url.QueryEscape(definition.Automation.Name)), "Schedule-backed nodes must open their scheduled task with a breadcrumb path back to the corresponding Automation")
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
	require.Contains(t, detail.Body.String(), `id="automation-live-run-now-form"`)
	require.Contains(t, detail.Body.String(), `onsubmit="event.preventDefault(); Promise.resolve(window.openVibelyAutomationLiveRefresh('POST', this.getAttribute('action'))).then(function() { window.openVibelyAutomationLiveRefresh('GET'); }); return false;"`, "Run now must reconcile the authoritative Live fragment after its mutation response settles")
	require.Contains(t, detail.Body.String(), `window.openVibelyAutomationLiveRefresh('GET')`, "returning to a visible tab must immediately refetch the local projection through the ordered coordinator")
	require.NotContains(t, detail.Body.String(), "GitHub state", "the manual GitHub state panel and refresh button were removed now that external state refreshes automatically in the background")
	require.NotContains(t, detail.Body.String(), "externalRefreshUrl")
	require.NotContains(t, detail.Body.String(), `aria-label="Automation views"`)
	require.NotContains(t, detail.Body.String(), `data-automation-view="live"`)
	require.NotContains(t, detail.Body.String(), `data-automation-view="history"`)
	require.NotContains(t, detail.Body.String(), `data-automation-view="definition"`)
	require.NotContains(t, detail.Body.String(), ">Definition<")
	require.NotContains(t, detail.Body.String(), ">Topology<")
	require.NotContains(t, detail.Body.String(), "Earlier activity")
	require.NotContains(t, detail.Body.String(), "invocation and work-item history")
	require.NotContains(t, detail.Body.String(), "topology v")
	require.NotContains(t, detail.Body.String(), `aria-selected="true"`)
	require.Contains(t, detail.Body.String(), `id="delete-automation-modal"`)
	require.Contains(t, detail.Body.String(), "Delete automation")
	require.Contains(t, detail.Body.String(), "owned trigger schedules will be deleted")
	require.NotContains(t, detail.Body.String(), ">Archive<")
	require.NotContains(t, detail.Body.String(), "/archive?")
	require.Contains(t, detail.Body.String(), `data-automation-view-yaml`)
	require.Contains(t, detail.Body.String(), `data-automation-view-details`)
	require.Contains(t, detail.Body.String(), `data-automation-yaml-panel`)
	require.Contains(t, detail.Body.String(), `data-automation-live-details-panel`)
	require.Contains(t, detail.Body.String(), `data-automation-live-node-details`)
	require.NotContains(t, detail.Body.String(), `name="automation_yaml"`)
	require.Contains(t, detail.Body.String(), task.Prompt)

	livePartial := tc.HTMX().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, 200, livePartial.Code)
	require.Contains(t, livePartial.Body.String(), `id="automation-live"`)

	definitionView := tc.HTMX().Get(fmt.Sprintf("/automations/%s/definition?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, http.StatusNotFound, definitionView.Code, "Definition is not a user-facing Automation view")

	producerNode := definition.Nodes[0]
	for _, node := range definition.Nodes {
		if node.NodeKey == "vision_suggestions" {
			producerNode = node
		}
	}
	for _, removedURL := range []string{
		fmt.Sprintf("/automations/%s/history?project_id=%s", definition.Automation.ID, project.ID),
		fmt.Sprintf("/automations/%s/invocations/removed?project_id=%s", definition.Automation.ID, project.ID),
		fmt.Sprintf("/automations/%s/work-items/removed?project_id=%s", definition.Automation.ID, project.ID),
		fmt.Sprintf("/automations/%s/nodes/%s/resources?project_id=%s", definition.Automation.ID, producerNode.ID, project.ID),
	} {
		response := tc.HTTP().Get(removedURL).Execute()
		require.Equal(t, http.StatusNotFound, response.Code, "removed Automation auxiliary route %s must stay unavailable", removedURL)
	}

	foreign := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", definition.Automation.ID, other.ID)).Execute()
	require.Equal(t, 404, foreign.Code)
}

func TestAutomationBrowserSaveRejectsExplicitInvalidSemanticConfiguration(t *testing.T) {
	for _, test := range []struct {
		name             string
		maintained       bool
		githubMaintained bool
		wantMessage      string
		mutate           func(*models.AutomationDraftCandidate)
		overlay          url.Values
	}{
		{name: "empty maintained node name", maintained: true, mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[0].Name = ""
		}},
		{name: "empty automation name form overlay", overlay: url.Values{"automation_name": {""}}},
		{name: "empty node name form overlay", overlay: url.Values{"node_schedule_name": {""}}},
		{name: "malformed priority form overlay", overlay: url.Values{"node_followup_priority": {"not-a-number"}}},
		{name: "malformed repeat interval form overlay", overlay: url.Values{"node_schedule_repeat_interval": {"not-a-number"}}},
		{name: "automation type", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.AutomationType = "github_sdlc"
		}},
		{name: "compatibility-only Task role", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[1].Role = "implementation"
		}},
		{name: "Task category", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[1].Config["category"] = "scheduled"
		}},
		{name: "GitHub Implementation category", githubMaintained: true, wantMessage: "GitHub implementation task category must be active", mutate: func(candidate *models.AutomationDraftCandidate) {
			for i := range candidate.Nodes {
				if candidate.Nodes[i].Role == "implementation" {
					candidate.Nodes[i].Config["category"] = "backlog"
				}
			}
		}},
		{name: "Schedule category", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[0].Config["category"] = "active"
		}},
		{name: "custom Schedule target", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[0].Config["target_node_key"] = "different_task"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().WithName("Strict browser Save " + test.name).Build()
			automationRepo := repository.NewAutomationRepo(tc.db)
			registry := service.NewAutomationAdapterRegistry()
			drafts := service.NewAutomationDraftService(automationRepo, registry)
			validator := service.NewAutomationSaveValidator(registry, drafts)
			compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
			tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
			tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

			candidate, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
			require.NoError(t, err)
			candidate.Name = "Strict browser Save"
			candidate.Nodes = []models.AutomationDraftNode{
				{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
				{Key: "followup", Name: "Follow up", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Run after the schedule.", "category": "backlog", "priority": 2}},
			}
			candidate.Edges = []models.AutomationDraftEdge{{Key: "schedule_followup", From: "schedule", To: "followup", FromPort: "right", ToPort: "left", Condition: map[string]any{}}}
			if test.maintained {
				candidate, err = drafts.TemplateCandidate(service.AutomationAdapterVisionDriver)
				require.NoError(t, err)
			}
			if test.githubMaintained {
				candidate, err = drafts.TemplateCandidate(service.AutomationAdapterGitHubSDLC)
				require.NoError(t, err)
			}
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			raw, err := json.Marshal(candidate)
			require.NoError(t, err)

			form := url.Values{
				"project_id": {project.ID}, "candidate_json": {string(raw)}, "save_changes": {"true"},
			}
			for key, values := range test.overlay {
				form[key] = values
			}
			response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(form).Execute()
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), "Save did not apply")
			if test.wantMessage != "" {
				require.Contains(t, response.Body.String(), test.wantMessage)
			}
			require.Empty(t, response.Header().Get("HX-Redirect"))
			require.Zero(t, tableCountHandler(t, tc, "automations"))
			require.Zero(t, tableCountHandler(t, tc, "tasks"))
			require.Zero(t, tableCountHandler(t, tc, "schedules"))
		})
	}
}

func TestAutomationWebBuilderKeepsUnsavedChangesBrowserLocal(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Browser Local Builder Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	opened := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, http.StatusOK, opened.Code, opened.Body.String())
	require.Zero(t, tableCountHandler(t, tc, "automations"), "opening a new builder must not persist an Automation")
	require.Zero(t, tableCountHandler(t, tc, "automation_versions"), "opening a new builder must not persist a draft version")
	require.NotContains(t, opened.Body.String(), `data-delete-automation-open`, "an unsaved Automation does not exist yet")

	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	rawCandidate, err := json.Marshal(candidate)
	require.NoError(t, err)
	added := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawCandidate)}, "builder_action": {"create_node"},
		"node_kind": {"agent_task"}, "node_name": {"Review support queue"},
	}).Execute()
	require.Equal(t, http.StatusOK, added.Code, added.Body.String())
	require.Contains(t, added.Body.String(), `data-node-key="review_support_queue"`)
	require.Zero(t, tableCountHandler(t, tc, "automations"), "adding a node must remain browser-local")
	require.Zero(t, tableCountHandler(t, tc, "automation_versions"), "adding a node must not create an editable draft")

	invalid := candidate
	invalid.Nodes = []models.AutomationDraftNode{{Key: "bad", Name: "Bad", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "approval_request", "instructions": "Review"}}}
	rawInvalid, err := json.Marshal(invalid)
	require.NoError(t, err)
	failedSave := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawInvalid)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusOK, failedSave.Code, failedSave.Body.String())
	require.Empty(t, failedSave.Header().Get("HX-Redirect"))
	require.Zero(t, tableCountHandler(t, tc, "automations"), "invalid Save must not persist an Automation")
	require.Zero(t, tableCountHandler(t, tc, "automation_versions"), "invalid Save must not persist a draft version")

	plannerInvalid := candidate
	plannerInvalid.Name = "Unavailable GitHub automation"
	plannerInvalid.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Inspect the project.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one focused issue.", "labels": []string{}}},
	}
	plannerInvalid.Edges = []models.AutomationDraftEdge{{Key: "schedule_issue", From: "schedule", To: "issue", FromPort: "right", ToPort: "left", Condition: map[string]any{}}}
	rawPlannerInvalid, err := json.Marshal(plannerInvalid)
	require.NoError(t, err)
	failedPlan := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"save"}, "candidate_json": {string(rawPlannerInvalid)}, "save_changes": {"true"},
		"node_schedule_enabled": {"true"},
	}).Execute()
	require.Equal(t, http.StatusOK, failedPlan.Code, failedPlan.Body.String())
	require.Empty(t, failedPlan.Header().Get("HX-Redirect"))
	require.Contains(t, failedPlan.Body.String(), "GitHub")
	require.Zero(t, tableCountHandler(t, tc, "automations"), "planner rejection must discard its temporary Automation")
	require.Zero(t, tableCountHandler(t, tc, "automation_versions"), "planner rejection must discard its temporary version")

	valid := candidate
	valid.Name = "Saved task automation"
	valid.Nodes = []models.AutomationDraftNode{{Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review one request.", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}}}
	rawValid, err := json.Marshal(valid)
	require.NoError(t, err)
	saved := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawValid)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, saved.Code, saved.Body.String())
	require.NotEmpty(t, saved.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automations"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automation_versions"))
	var automationID string
	require.NoError(t, tc.db.QueryRow(`SELECT id FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID))

	edited := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusOK, edited.Code, edited.Body.String())
	require.Contains(t, edited.Body.String(), "Saved task automation")
	var draftCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, automationID).Scan(&draftCount))
	require.Zero(t, draftCount, "opening Edit automation must not clone a persisted draft")

	malicious := valid
	malicious.Name = "GET must not save this name"
	rawMalicious, err := json.Marshal(malicious)
	require.NoError(t, err)
	openedByGET := tc.HTTP().Get("/automations/" + automationID + "/builder?" + url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawMalicious)}, "save_changes": {"true"},
	}.Encode()).Execute()
	require.Equal(t, http.StatusOK, openedByGET.Code, openedByGET.Body.String())
	var persistedName string
	require.NoError(t, tc.db.QueryRow(`SELECT name FROM automations WHERE id = ?`, automationID).Scan(&persistedName))
	require.Equal(t, "Saved task automation", persistedName, "hard-refreshable Edit GET must never process mutation form values")

	valid.Name = "Unsaved browser name"
	rawEdited, err := json.Marshal(valid)
	require.NoError(t, err)
	mutated := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawEdited)}, "builder_action": {"create_node"},
		"node_kind": {"outcome"}, "node_name": {"Reviewed"},
	}).Execute()
	require.Equal(t, http.StatusOK, mutated.Code, mutated.Body.String())
	require.Contains(t, mutated.Body.String(), "Unsaved browser name")
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, automationID).Scan(&draftCount))
	require.Zero(t, draftCount, "builder mutations must not persist a draft")

	refreshed := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusOK, refreshed.Code, refreshed.Body.String())
	require.Contains(t, refreshed.Body.String(), "Saved task automation")
	require.NotContains(t, refreshed.Body.String(), "Unsaved browser name", "refresh/re-entry must restore the published graph")

	var priorVersionID, priorNodeID string
	require.NoError(t, tc.db.QueryRow(`SELECT a.published_version_id, n.id
		FROM automations a JOIN automation_nodes n ON n.version_id = a.published_version_id
		WHERE a.id = ? AND n.node_key = 'review'`, automationID).Scan(&priorVersionID, &priorNodeID))
	_, err = tc.db.Exec(`INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status)
		VALUES (?, ?, ?, ?, 'task', 'running-task', 'save-while-running', 'running')`,
		project.ID, automationID, priorVersionID, priorNodeID)
	require.NoError(t, err)

	valid.Name = "Saved replacement without version token"
	valid.Nodes[0].Config["prompt"] = "Review using the replacement instructions."
	rawReplacement, err := json.Marshal(valid)
	require.NoError(t, err)
	savedEdit := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawReplacement)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, savedEdit.Code, savedEdit.Body.String())
	require.NotEmpty(t, savedEdit.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automation_versions"), "Save must replace the prior graph instead of retaining version history")
	var invocationCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_invocations WHERE automation_id = ?`, automationID).Scan(&invocationCount))
	require.Zero(t, invocationCount, "Save while running must discard runtime rows tied to the replaced graph")

	invalidEdit := valid
	invalidEdit.Nodes = nil
	invalidEdit.Edges = nil
	rawInvalidEdit, err := json.Marshal(invalidEdit)
	require.NoError(t, err)
	failedEditSave := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {string(rawInvalidEdit)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusOK, failedEditSave.Code, failedEditSave.Body.String())
	require.Empty(t, failedEditSave.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automation_versions"), "an invalid replacement must leave the current saved graph unchanged")

	var publishedCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'published'`, automationID).Scan(&publishedCount))
	require.Equal(t, 1, publishedCount)
	var savedGraphSequence int
	require.NoError(t, tc.db.QueryRow(`SELECT version FROM automation_versions WHERE automation_id = ?`, automationID).Scan(&savedGraphSequence))
	require.Equal(t, 1, savedGraphSequence, "the single current graph must not expose an accumulating version sequence")
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, automationID).Scan(&draftCount))
	require.Zero(t, draftCount)
	var savedName, savedTaskPrompt string
	require.NoError(t, tc.db.QueryRow(`SELECT name FROM automations WHERE id = ?`, automationID).Scan(&savedName))
	require.Equal(t, "Saved replacement without version token", savedName)
	require.NoError(t, tc.db.QueryRow(`SELECT prompt FROM tasks WHERE created_via = ?`, repository.AutomationCompilerTaskCreatedVia(automationID, "review")).Scan(&savedTaskPrompt))
	require.Equal(t, "Review using the replacement instructions.", savedTaskPrompt)
}

func TestAutomationTemplateBuilderAddsAndSavesCustomNodes(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Template Custom Nodes Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	opened := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"template"}, "template_key": {service.AutomationAdapterNativeSDLC},
	}).Execute()
	require.Equal(t, http.StatusOK, opened.Code, opened.Body.String())
	require.Contains(t, opened.Body.String(), `name="automation_yaml"`)
	require.Contains(t, opened.Body.String(), `data-automation-yaml-editor`)

	candidate := automationCandidateFromResponse(t, opened)
	post := func(values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(candidate)
		require.NoError(t, err)
		values.Set("project_id", project.ID)
		values.Set("builder_source", "template")
		values.Set("candidate_json", string(raw))
		response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(values).Execute()
		if response.Code == http.StatusOK {
			candidate = automationCandidateFromResponse(t, response)
		}
		return response
	}

	addedSchedule := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"schedule"}, "node_name": {"Extra review"}})
	require.Equal(t, http.StatusOK, addedSchedule.Code, addedSchedule.Body.String())
	require.NotContains(t, addedSchedule.Body.String(), `name="node_extra_review_skills"`)
	require.NotContains(t, addedSchedule.Body.String(), `name="node_extra_review_source_files"`)
	addedTask := post(url.Values{"builder_action": {"create_node"}, "node_kind": {"task"}, "node_name": {"Extra follow-up"}})
	require.Equal(t, http.StatusOK, addedTask.Code, addedTask.Body.String())
	require.NotContains(t, addedTask.Body.String(), `name="node_extra_follow_up_skills"`)
	require.NotContains(t, addedTask.Body.String(), `name="node_extra_follow_up_source_files"`)
	require.Equal(t, http.StatusOK, post(url.Values{"builder_action": {"connect_nodes"}, "from_key": {"extra_review"}, "to_key": {"extra_follow_up"}}).Code)

	// An editor page that was open before template-only controls were removed
	// has already synchronized those values into candidate_json. Preserve that
	// browser shape rather than only sending stale standalone form fields.
	for i := range candidate.Nodes {
		switch candidate.Nodes[i].Key {
		case "extra_review", "extra_follow_up":
			candidate.Nodes[i].Config["skills"] = []string{"example:review"}
			candidate.Nodes[i].Config["source_files"] = []string{"README.md"}
		}
	}

	// The browser synchronizes all rendered template settings into candidate_json.
	// These stale extra-node values model a form submission from before the controls
	// were removed; the handler must not merge template-only settings into custom nodes.
	staleTemplateFields := url.Values{
		"node_vision_suggestions_skills":       {""},
		"node_vision_suggestions_source_files": {""},
		"node_extra_review_skills":             {"example:review"},
		"node_extra_review_source_files":       {"README.md"},
		"node_extra_follow_up_skills":          {"example:review"},
		"node_extra_follow_up_source_files":    {"README.md"},
	}
	preview := post(staleTemplateFields)
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	for _, key := range []string{"extra_review", "extra_follow_up"} {
		node := automationDraftNodeByKeyHandler(t, candidate, key)
		_, hasSkills := node.Config["skills"]
		require.False(t, hasSkills, "preview must remove stale skills from custom node %s", key)
		_, hasSourceFiles := node.Config["source_files"]
		require.False(t, hasSourceFiles, "preview must remove stale source_files from custom node %s", key)
	}

	// Save the same browser-local candidate only after the preview mutation.
	saved := post(url.Values{"save_changes": {"true"}})
	require.Equal(t, http.StatusNoContent, saved.Code, saved.Body.String())
	require.NotEmpty(t, saved.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automations"))

	// Reintroduce the stale browser fields and exercise the same mutation sequence
	// through the existing-Automation builder before any edit Save.
	for i := range candidate.Nodes {
		switch candidate.Nodes[i].Key {
		case "extra_review", "extra_follow_up":
			candidate.Nodes[i].Config["skills"] = []string{"example:review"}
			candidate.Nodes[i].Config["source_files"] = []string{"README.md"}
		}
	}
	rawEditCandidate, err := json.Marshal(candidate)
	require.NoError(t, err)
	var automationID string
	require.NoError(t, tc.db.QueryRow(`SELECT id FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID))
	editedPreview := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id":                        {project.ID},
		"builder_source":                    {"template"},
		"candidate_json":                    {string(rawEditCandidate)},
		"automation_name":                   {"Edited template preview"},
		"builder_action":                    {"create_node"},
		"node_kind":                         {"outcome"},
		"node_name":                         {"Edit preview result"},
		"node_extra_review_skills":          {"example:review"},
		"node_extra_review_source_files":    {"README.md"},
		"node_extra_follow_up_skills":       {"example:review"},
		"node_extra_follow_up_source_files": {"README.md"},
	}).Execute()
	require.Equal(t, http.StatusOK, editedPreview.Code, editedPreview.Body.String())
	editedCandidate := automationCandidateFromResponse(t, editedPreview)
	require.Equal(t, "Edited template preview", editedCandidate.Name)
	require.Equal(t, "Edit preview result", automationDraftNodeByKeyHandler(t, editedCandidate, "edit_preview_result").Name)
	for _, key := range []string{"extra_review", "extra_follow_up"} {
		node := automationDraftNodeByKeyHandler(t, editedCandidate, key)
		_, hasSkills := node.Config["skills"]
		require.False(t, hasSkills, "edit preview must remove stale skills from custom node %s", key)
		_, hasSourceFiles := node.Config["source_files"]
		require.False(t, hasSourceFiles, "edit preview must remove stale source_files from custom node %s", key)
	}
	require.Equal(t, 1, tableCountHandler(t, tc, "automation_versions"), "edit preview must remain browser-local")

	countNodeResources := func(nodeKey, resourceType string) int {
		t.Helper()
		var count int
		require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_definition_resources r
			JOIN automation_nodes n ON n.id = r.node_id AND n.version_id = r.version_id
			WHERE r.project_id = ? AND n.node_key = ? AND r.resource_type = ?`, project.ID, nodeKey, resourceType).Scan(&count))
		return count
	}
	require.Equal(t, 1, countNodeResources("extra_review", "schedule"))
	require.Equal(t, 1, countNodeResources("extra_review", "task"))
	require.Equal(t, 1, countNodeResources("extra_follow_up", "task"))
}

func TestAutomationBlankBuilderOffersGraphYAMLAndDetailsViews(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Blank Builder Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	portfolio := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, portfolio.Code)
	require.Contains(t, portfolio.Body.String(), `data-automation-new-custom`)
	require.Contains(t, portfolio.Body.String(), `name="source" value="blank"`)
	opened := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, http.StatusOK, opened.Code)
	for _, marker := range []string{`data-automation-yaml-builder`, `data-automation-yaml-editor`, `data-automation-view-yaml`, `data-automation-view-details`, `data-automation-graph-panel`, `data-automation-details-panel`, `data-automation-details-form`, `data-automation-node-details`, `data-automation-edge-details`, `name="automation_yaml"`, `name="candidate_json"`, `data-automation-draft-canvas`, `data-automation-add-node-open`, `data-automation-node-dialog`, `data-automation-add-first-node`, `data-automation-fit`, `data-automation-builder-header`, `data-automation-editable-breadcrumb`, `class="rounded-box border border-base-300 bg-base-100 mb-0 p-4 flex flex-1 min-h-[20rem] flex-col"`, `class="automation-canvas-shell relative w-full overflow-hidden rounded-box border border-base-300 bg-base-200/30 flex-1 min-h-[20rem]"`} {
		require.Contains(t, opened.Body.String(), marker)
	}
	for _, marker := range []string{`data-automation-builder-name`, `<h3 class="font-semibold">Canvas</h3>`, "Drag nodes to arrange them and empty space to pan.", "Connect steps:"} {
		require.NotContains(t, opened.Body.String(), marker)
	}
	require.NotContains(t, opened.Body.String(), `data-automation-yaml-preview`)
	require.NotContains(t, opened.Body.String(), "Automation YAML")
	require.NotContains(t, opened.Body.String(), "YAML controls node and connection configuration")
	require.Contains(t, opened.Body.String(), `name="save_changes" value="true"`)
	require.NotContains(t, opened.Body.String(), "Review and apply")
	require.NotContains(t, opened.Body.String(), "Apply changes")
	require.NotContains(t, opened.Body.String(), `data-delete-automation-open`, "an unsaved browser design is not an Automation yet")
	require.NotContains(t, opened.Body.String(), "Suggested nodes")
	require.Zero(t, tableCountHandler(t, tc, "automations"))

	candidate := automationCandidateFromResponse(t, opened)
	require.Empty(t, candidate.Nodes)
	require.Empty(t, candidate.Edges)
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationBlankBuilderUsesYAMLForCustomTopology(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("YAML Custom Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, nil)

	created := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, http.StatusOK, created.Code)
	candidate := automationCandidateFromResponse(t, created)
	candidate.Name = "YAML custom topology"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Weekday review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Review work.", "goal": "", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "task", Name: "Follow up", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Follow up.", "goal": "", "category": "backlog", "priority": 2}},
	}
	candidate.Edges = []models.AutomationDraftEdge{{Key: "schedule_task", From: "schedule", To: "task", FromPort: "right", ToPort: "left", Condition: map[string]any{}}}
	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	preview := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {yaml}}).Execute()
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Body.String(), `automation-draft-node`)
	require.Contains(t, preview.Body.String(), `data-node-key="schedule"`)
	require.Contains(t, preview.Body.String(), `data-node-key="task"`)
	require.Contains(t, preview.Body.String(), `data-edge-key="schedule_task"`)
	require.Contains(t, preview.Body.String(), `data-automation-add-node-open`)
	require.Contains(t, preview.Body.String(), `data-automation-details-panel`)
	require.Contains(t, preview.Body.String(), `data-automation-details-form`)
	require.Contains(t, preview.Body.String(), `data-automation-node-detail="schedule"`)
	require.Contains(t, preview.Body.String(), `data-automation-node-detail="task"`)
	require.Contains(t, preview.Body.String(), `data-automation-edge-detail="schedule_task"`)
	require.Contains(t, preview.Body.String(), "Task prompt")
	require.Contains(t, preview.Body.String(), "Task goal (optional)")
	require.NotContains(t, preview.Body.String(), "Human result")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
}

func TestAutomationBuilderPreviewRestoresDetailsView(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Details preview project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, nil)

	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	candidate.Name = "Details preview"
	candidate.Nodes = []models.AutomationDraftNode{{
		Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 2},
	}}
	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)

	response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {yaml}, "initial_view": {"details"},
	}).Execute()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `name="initial_view" value="details" data-automation-initial-view`)
	require.Contains(t, response.Body.String(), `if (initialView && initialView.value === 'details') selectAutomationBuilderView('details');`)
}

func TestAutomationYAMLParseReportsMalformedDocumentsWithoutSideEffects(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("YAML parse-only project").Build()

	invalid := tc.HTTP().Post("/automations/yaml/parse?project_id=" + project.ID).WithForm(url.Values{
		"automation_yaml": {"schema_version: ["},
	}).Execute()
	require.Equal(t, http.StatusOK, invalid.Code, invalid.Body.String())
	var invalidResult struct {
		Valid   bool   `json:"valid"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(invalid.Body.Bytes(), &invalidResult))
	require.False(t, invalidResult.Valid)
	require.Contains(t, invalidResult.Message, "Malformed YAML:")
	require.Contains(t, invalidResult.Message, "line 1")

	validYAML, err := service.EncodeAutomationDraftYAML(models.AutomationDraftCandidate{
		SchemaVersion: 1, Name: "Syntax only", AutomationType: "custom", AdapterKey: "custom",
	})
	require.NoError(t, err)
	valid := tc.HTTP().Post("/automations/yaml/parse?project_id=" + project.ID).WithForm(url.Values{
		"automation_yaml": {validYAML},
	}).Execute()
	require.Equal(t, http.StatusOK, valid.Code, valid.Body.String())
	var validResult struct {
		Valid bool `json:"valid"`
	}
	require.NoError(t, json.Unmarshal(valid.Body.Bytes(), &validResult))
	require.True(t, validResult.Valid)

	for _, table := range []string{"automations", "automation_versions"} {
		require.Zero(t, tableCountHandler(t, tc, table), "parse-only validation must not change %s", table)
	}
}

func TestAutomationBuilderVisualActionsDecodeAndReserializeYAML(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Visual YAML Builder Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	candidate.Name = "Manual YAML source"
	candidate.Description = "The visual action must start from this YAML document."
	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)

	response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {yaml},
		"builder_action": {"create_node"}, "node_kind": {"task"}, "node_name": {"Review queue"},
	}).Execute()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	updated := automationCandidateFromResponse(t, response)
	require.Equal(t, "Manual YAML source", updated.Name)
	require.Equal(t, "The visual action must start from this YAML document.", updated.Description)
	require.Len(t, updated.Nodes, 1)
	require.Equal(t, "review_queue", updated.Nodes[0].Key)
	require.Equal(t, "Review queue", updated.Nodes[0].Name)
	require.Contains(t, response.Body.String(), `data-automation-details-panel`)
	require.Contains(t, response.Body.String(), `data-automation-node-detail="review_queue"`)
	require.Contains(t, response.Body.String(), "Task prompt")
	require.Contains(t, response.Body.String(), "Task goal (optional)")
	require.NotContains(t, response.Body.String(), "Human result")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "automation_versions"))
}

func TestAutomationBuilderPreviewUsesCompilerValidation(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Compiler Preview Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	validator.SetAgentRepository(repository.NewAgentRepo(tc.db))
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, nil)

	t.Run("GitHub capability", func(t *testing.T) {
		candidate, err := drafts.TemplateCandidate(service.AutomationAdapterGitHubSDLC)
		require.NoError(t, err)
		yaml, err := service.EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)

		response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
			"project_id": {project.ID}, "builder_source": {"template"}, "automation_yaml": {yaml},
		}).Execute()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "Configure the selected GitHub authentication mode")
		require.Contains(t, response.Body.String(), "GitHub Authorized User")
		require.Zero(t, tableCountHandler(t, tc, "automations"))
	})

	t.Run("agent selection", func(t *testing.T) {
		candidate, err := drafts.BlankCandidate("")
		require.NoError(t, err)
		candidate.Name = "Missing agent"
		candidate.Nodes = []models.AutomationDraftNode{{
			Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"prompt": "Review one request.", "category": "backlog", "priority": 2, "agent_ref": "missing-agent"},
		}}
		yaml, err := service.EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)

		response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
			"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {yaml},
		}).Execute()
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Contains(t, response.Body.String(), "Agent selection is unavailable in this project.")
		require.Zero(t, tableCountHandler(t, tc, "automations"))
	})
}

func TestAutomationBuilderPreviewUsesSingleCompactAgentValidationPass(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, _, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Single Agent validation preview"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{Name: "Preview Agent", Key: "preview-agent", SystemPrompt: "private preview prompt", Model: "inherit", Tools: []string{"Read"}, Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agent))
	h.SetAgentRepo(agentRepo)

	registry := service.NewAutomationAdapterRegistry()
	automationRepo := repository.NewAutomationRepo(db)
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, nil, nil)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, h.taskSvc, h.taskRepo, h.scheduleRepo, validator)
	h.SetAutomationBuilderServices(drafts, capabilities, validator, compiler, nil, nil)

	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	candidate.Name = "Single Agent validation preview"
	candidate.Nodes = []models.AutomationDraftNode{{
		Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 2, "agent_ref": agent.Key},
	}}

	counter.Reset()
	counter.SetEnabled(true)
	result, err := h.previewAutomationBuilderCandidate(ctx, project.ID, candidate, nil)
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Empty(t, result.ValidationErrors)
	agentStatements := make([]string, 0)
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from agents") {
			agentStatements = append(agentStatements, statement)
		}
	}
	require.Len(t, agentStatements, 1)
	projection := strings.ToLower(strings.SplitN(strings.Join(strings.Fields(agentStatements[0]), " "), " from agents", 2)[0])
	require.Contains(t, projection, "select id")
	require.Contains(t, projection, "coalesce(key, '')")
	require.Contains(t, projection, "project_id")
	for _, forbidden := range []string{"system_prompt", "tools", "tool_config", "plugins", "mcp_servers", "skills", "permission_defaults_json", "model_defaults_json", "source_refs_json", "created_at", "updated_at"} {
		require.NotContains(t, projection, forbidden)
	}

	delete(candidate.Nodes[0].Config, "agent_ref")
	counter.Reset()
	counter.SetEnabled(true)
	result, err = h.previewAutomationBuilderCandidate(ctx, project.ID, candidate, nil)
	counter.SetEnabled(false)
	require.NoError(t, err)
	require.Empty(t, result.ValidationErrors)
	agentStatements = agentStatements[:0]
	for _, statement := range counter.Statements() {
		if strings.Contains(strings.ToLower(statement), "from agents") {
			agentStatements = append(agentStatements, statement)
		}
	}
	require.Empty(t, agentStatements)
}

func TestAutomationBuilderEditUsesSingleCompactAgentValidationPass(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, _ := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Single Agent validation edit"}
	require.NoError(t, projectRepo.Create(ctx, project))
	agentRepo := repository.NewAgentRepo(db)
	agent := &models.Agent{Name: "Edit Agent", Key: "edit-agent", SystemPrompt: "private edit prompt", Model: "inherit", Tools: []string{"Read"}, Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, agent))
	h.SetAgentRepo(agentRepo)

	registry := service.NewAutomationAdapterRegistry()
	automationRepo := repository.NewAutomationRepo(db)
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, nil, nil)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, h.taskSvc, h.taskRepo, h.scheduleRepo, validator)
	h.SetAutomationBuilderServices(drafts, capabilities, validator, compiler, nil, nil)

	candidate, err := drafts.BlankCandidate("")
	require.NoError(t, err)
	candidate.Name = "Single Agent validation edit"
	candidate.Nodes = []models.AutomationDraftNode{{
		Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review the request.", "category": "backlog", "priority": 2, "agent_ref": agent.Key},
	}}
	saved, err := compiler.Save(ctx, service.AutomationSaveRequest{
		ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
	})
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.NotNil(t, saved.Definition)

	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	form := url.Values{"project_id": {project.ID}, "automation_yaml": {yaml}}
	req := httptest.NewRequest(http.MethodPost, "/automations/"+saved.Definition.Automation.ID+"/builder?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	response := httptest.NewRecorder()
	counter.Reset()
	counter.SetEnabled(true)
	e.ServeHTTP(response, req)
	counter.SetEnabled(false)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())

	countAgentQueries := func() (compact, rich int) {
		for _, statement := range counter.Statements() {
			query := strings.ToLower(strings.Join(strings.Fields(statement), " "))
			if !strings.Contains(query, "from agents") {
				continue
			}
			projection := strings.SplitN(query, " from agents", 2)[0]
			if strings.Contains(projection, "system_prompt") {
				rich++
				continue
			}
			if strings.Contains(projection, "coalesce(key, '')") && strings.Contains(projection, "coalesce(project_id, '')") {
				compact++
			}
		}
		return compact, rich
	}
	compactAgentQueries, richAgentQueries := countAgentQueries()
	require.Equal(t, 1, compactAgentQueries, "edit preview must read selectable Agent identities once")
	require.Equal(t, 1, richAgentQueries, "edit rendering must retain its separate rich Agent picker read")

	form.Set("save_changes", "true")
	req = httptest.NewRequest(http.MethodPost, "/automations/"+saved.Definition.Automation.ID+"/builder?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	response = httptest.NewRecorder()
	counter.Reset()
	counter.SetEnabled(true)
	e.ServeHTTP(response, req)
	counter.SetEnabled(false)
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	compactAgentQueries, richAgentQueries = countAgentQueries()
	require.Equal(t, 1, compactAgentQueries, "edit save must reuse preview validation")
	require.Equal(t, 1, richAgentQueries, "edit save must retain its separate rich Agent materialization read")

	_, err = db.ExecContext(ctx, `UPDATE agents SET enabled = 0 WHERE id = ?`, agent.ID)
	require.NoError(t, err)
	malformedForm := url.Values{"project_id": {project.ID}, "automation_yaml": {""}, "save_changes": {"true"}}
	malformedRequest := httptest.NewRequest(http.MethodPost, "/automations/"+saved.Definition.Automation.ID+"/builder?project_id="+project.ID, strings.NewReader(malformedForm.Encode()))
	malformedRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	malformedRequest.Header.Set("HX-Request", "true")
	malformedResponse := httptest.NewRecorder()
	e.ServeHTTP(malformedResponse, malformedRequest)
	require.Equal(t, http.StatusOK, malformedResponse.Code, malformedResponse.Body.String())
	require.Empty(t, malformedResponse.Header().Get("HX-Redirect"))
	require.Contains(t, malformedResponse.Body.String(), "YAML did not parse")
	require.Contains(t, malformedResponse.Body.String(), "Agent selection is unavailable in this project.")
}

func TestAutomationBuilderRejectsUnsafeAndUnsupportedYAMLWithoutSideEffects(t *testing.T) {
	newBuilder := func(t *testing.T) (*TestContext, *models.Project, *repository.AutomationRepo, *service.AutomationDraftService) {
		t.Helper()
		tc := NewTestContext(t)
		project := tc.CreateProject().WithName("YAML configuration validation project").Build()
		automationRepo := repository.NewAutomationRepo(tc.db)
		registry := service.NewAutomationAdapterRegistry()
		drafts := service.NewAutomationDraftService(automationRepo, registry)
		validator := service.NewAutomationSaveValidator(registry, drafts)
		compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
		tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
		tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, nil)
		return tc, project, automationRepo, drafts
	}
	validCandidate := func(t *testing.T, drafts *service.AutomationDraftService) models.AutomationDraftCandidate {
		t.Helper()
		candidate, err := drafts.BlankCandidate("")
		require.NoError(t, err)
		candidate.Name = "Validated YAML Automation"
		candidate.Nodes = []models.AutomationDraftNode{{
			Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"prompt": "Review one request.", "category": "backlog", "priority": 2},
		}}
		return candidate
	}
	invalidCases := []struct {
		name    string
		mutate  func(models.AutomationDraftCandidate)
		message string
	}{
		{name: "unsupported configuration", mutate: func(candidate models.AutomationDraftCandidate) {
			candidate.Nodes[0].Config["unsupported_field"] = "nope"
		}, message: "unsupported_field"},
		{name: "unsafe configuration", mutate: func(candidate models.AutomationDraftCandidate) {
			candidate.Nodes[0].Config["prompt"] = "Review https://example.invalid/unsafe"
		}, message: "contains an unsupported value"},
	}

	for _, testCase := range invalidCases {
		t.Run("new "+testCase.name, func(t *testing.T) {
			tc, project, _, drafts := newBuilder(t)
			candidate := validCandidate(t, drafts)
			testCase.mutate(candidate)
			yaml, err := service.EncodeAutomationDraftYAML(candidate)
			require.NoError(t, err)

			response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
				"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {yaml}, "save_changes": {"true"},
			}).Execute()
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Empty(t, response.Header().Get("HX-Redirect"))
			require.Contains(t, response.Body.String(), testCase.message)
			require.Zero(t, tableCountHandler(t, tc, "automations"))
			require.Zero(t, tableCountHandler(t, tc, "automation_versions"))
			require.Zero(t, tableCountHandler(t, tc, "tasks"))
			require.Zero(t, tableCountHandler(t, tc, "schedules"))
		})
	}

	for _, testCase := range invalidCases {
		t.Run("saved edit "+testCase.name, func(t *testing.T) {
			tc, project, automationRepo, drafts := newBuilder(t)
			candidate := validCandidate(t, drafts)
			yaml, err := service.EncodeAutomationDraftYAML(candidate)
			require.NoError(t, err)
			created := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
				"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {yaml}, "save_changes": {"true"},
			}).Execute()
			require.Equal(t, http.StatusNoContent, created.Code, created.Body.String())
			var automationID, versionID string
			require.NoError(t, tc.db.QueryRow(`SELECT id, published_version_id FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID, &versionID))
			before, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
			require.NoError(t, err)
			baselineTasks := tableCountHandler(t, tc, "tasks")
			baselineSchedules := tableCountHandler(t, tc, "schedules")

			invalid := validCandidate(t, drafts)
			testCase.mutate(invalid)
			invalidYAML, err := service.EncodeAutomationDraftYAML(invalid)
			require.NoError(t, err)
			response := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
				"project_id": {project.ID}, "automation_yaml": {invalidYAML}, "save_changes": {"true"},
			}).Execute()
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Empty(t, response.Header().Get("HX-Redirect"))
			require.Contains(t, response.Body.String(), testCase.message)
			var currentVersionID string
			require.NoError(t, tc.db.QueryRow(`SELECT published_version_id FROM automations WHERE id = ?`, automationID).Scan(&currentVersionID))
			require.Equal(t, versionID, currentVersionID)
			after, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			require.Equal(t, baselineTasks, tableCountHandler(t, tc, "tasks"))
			require.Equal(t, baselineSchedules, tableCountHandler(t, tc, "schedules"))
		})
	}
}

func TestAutomationBuilderRejectsEmptyYAMLWithoutSideEffects(t *testing.T) {
	newBuilder := func(t *testing.T) (*TestContext, *models.Project, *repository.AutomationRepo, *service.AutomationDraftService) {
		t.Helper()
		tc := NewTestContext(t)
		project := tc.CreateProject().WithName("Empty YAML Builder Project").Build()
		automationRepo := repository.NewAutomationRepo(tc.db)
		registry := service.NewAutomationAdapterRegistry()
		drafts := service.NewAutomationDraftService(automationRepo, registry)
		validator := service.NewAutomationSaveValidator(registry, drafts)
		compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
		tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
		tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
		return tc, project, automationRepo, drafts
	}

	t.Run("new template", func(t *testing.T) {
		tc, project, _, _ := newBuilder(t)
		response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
			"project_id": {project.ID}, "builder_source": {"template"}, "template_key": {service.AutomationAdapterNativeSDLC},
			"automation_yaml": {""}, "save_changes": {"true"},
		}).Execute()

		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Empty(t, response.Header().Get("HX-Redirect"))
		require.Contains(t, response.Body.String(), "YAML did not parse")
		require.Regexp(t, `(?s)<textarea[^>]*name="automation_yaml"[^>]*>\s*</textarea>`, response.Body.String())
		require.Zero(t, tableCountHandler(t, tc, "automations"))
		require.Zero(t, tableCountHandler(t, tc, "tasks"))
		require.Zero(t, tableCountHandler(t, tc, "schedules"))
	})

	t.Run("saved edit", func(t *testing.T) {
		tc, project, automationRepo, drafts := newBuilder(t)
		candidate, err := drafts.TemplateCandidate(service.AutomationAdapterNativeSDLC)
		require.NoError(t, err)
		yaml, err := service.EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)
		created := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
			"project_id": {project.ID}, "builder_source": {"template"}, "automation_yaml": {yaml}, "save_changes": {"true"},
		}).Execute()
		require.Equal(t, http.StatusNoContent, created.Code, created.Body.String())
		var automationID, versionID string
		require.NoError(t, tc.db.QueryRow(`SELECT id, published_version_id FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID, &versionID))
		before, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
		require.NoError(t, err)

		response := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
			"project_id": {project.ID}, "automation_yaml": {""}, "save_changes": {"true"},
		}).Execute()

		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		require.Empty(t, response.Header().Get("HX-Redirect"))
		require.Contains(t, response.Body.String(), "YAML did not parse")
		require.Regexp(t, `(?s)<textarea[^>]*name="automation_yaml"[^>]*>\s*</textarea>`, response.Body.String())
		var currentVersionID string
		require.NoError(t, tc.db.QueryRow(`SELECT published_version_id FROM automations WHERE id = ?`, automationID).Scan(&currentVersionID))
		require.Equal(t, versionID, currentVersionID)
		after, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
		require.NoError(t, err)
		require.Equal(t, before, after)
	})
}

func TestAutomationBuilderRejectsMalformedYAMLWithoutSideEffects(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Malformed YAML Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	drafts := service.NewAutomationDraftService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, nil, nil, nil, nil)

	raw := "schema_version: 1\nname: duplicate\nname: duplicate\n"
	response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"blank"}, "automation_yaml": {raw}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "YAML did not parse")
	require.Contains(t, response.Body.String(), `name="automation_yaml"`)
	require.Contains(t, response.Body.String(), "name: duplicate")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationBuilderManualInboxDefaultsMatchMaintainedTemplateDefaults(t *testing.T) {
	for _, tcSpec := range []struct {
		name        string
		adapterKey  string
		nodeKind    string
		templateKey string
	}{
		{name: "native", adapterKey: service.AutomationAdapterNativeSDLC, nodeKind: "native_inbox", templateKey: "inbox"},
		{name: "github", adapterKey: service.AutomationAdapterGitHubSDLC, nodeKind: "github_inbox", templateKey: "dev_inbox"},
	} {
		t.Run(tcSpec.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().WithName("Inbox Defaults Project").Build()
			automationRepo := repository.NewAutomationRepo(tc.db)
			registry := service.NewAutomationAdapterRegistry()
			drafts := service.NewAutomationDraftService(automationRepo, registry)
			planner := service.NewAutomationSaveValidator(registry, drafts)
			compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
			tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
			tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, nil)

			opened := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
				"project_id": {project.ID}, "source": {"template"}, "template_key": {tcSpec.adapterKey},
			}).Execute()
			require.Equal(t, http.StatusOK, opened.Code, opened.Body.String())
			candidate := automationCandidateFromResponse(t, opened)
			templateNode := automationDraftNodeByKeyHandler(t, candidate, tcSpec.templateKey)

			raw, err := json.Marshal(candidate)
			require.NoError(t, err)
			added := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
				"project_id":     {project.ID},
				"builder_source": {"template"},
				"candidate_json": {string(raw)},
				"builder_action": {"create_node"},
				"node_kind":      {tcSpec.nodeKind},
				"node_name":      {"Manual inbox"},
			}).Execute()
			require.Equal(t, http.StatusOK, added.Code, added.Body.String())
			candidate = automationCandidateFromResponse(t, added)
			manualNode := automationDraftNodeByKeyHandler(t, candidate, "manual_inbox")

			for _, key := range []string{"category", "priority", "model_config_id", "goal", "repeat_type", "repeat_interval", "run_at", "enabled", "clear_context_on_start"} {
				require.Equal(t, templateNode.Config[key], manualNode.Config[key], "%s default %s", tcSpec.name, key)
			}
			require.NotEqual(t, "", manualNode.Config["model_config_id"])
			require.Equal(t, "10:00", manualNode.Config["run_at"])
		})
	}
}

func TestAutomationBuilderSavesUnsupportedCustomConnectionsWithoutExecutingThem(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Freeform Builder Project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, nil)

	created := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"blank"},
	}).Execute()
	require.Equal(t, 200, created.Code)
	candidate := automationCandidateFromResponse(t, created)
	post := func(values url.Values) *httptest.ResponseRecorder {
		t.Helper()
		raw, marshalErr := json.Marshal(candidate)
		require.NoError(t, marshalErr)
		values.Set("project_id", project.ID)
		values.Set("builder_source", "blank")
		values.Set("candidate_json", string(raw))
		response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(values).Execute()
		if response.Code == http.StatusOK {
			candidate = automationCandidateFromResponse(t, response)
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
	rawCandidate, err := json.Marshal(candidate)
	require.NoError(t, err)
	saved := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"blank"}, "candidate_json": {string(rawCandidate)}, "save_changes": {"true"},
		"node_" + candidate.Nodes[0].Key + "_enabled": {"true"},
	}).Execute()
	require.Equal(t, 200, saved.Code, saved.Body.String())
	require.Empty(t, saved.Header().Get("HX-Redirect"), "invalid designs must remain in the editor")
	require.Contains(t, saved.Body.String(), "Save did not apply. Resolve the setup items below and try again.")
	require.Zero(t, tableCountHandler(t, tc, "tasks"), "invalid Save must not create partial task resources")
	require.Zero(t, tableCountHandler(t, tc, "schedules"), "invalid Save must not create partial schedule resources")
}

func automationDraftNodeByKeyHandler(t *testing.T, candidate models.AutomationDraftCandidate, key string) models.AutomationDraftNode {
	t.Helper()
	for _, node := range candidate.Nodes {
		if node.Key == key {
			return node
		}
	}
	require.Failf(t, "node not found", "node %q not found", key)
	return models.AutomationDraftNode{}
}

func automationCandidateFromResponse(t *testing.T, response *httptest.ResponseRecorder) models.AutomationDraftCandidate {
	t.Helper()
	match := regexp.MustCompile(`(?s)<textarea[^>]*name="automation_yaml"[^>]*>(.*?)</textarea>`).FindStringSubmatch(response.Body.String())
	require.Len(t, match, 2, response.Body.String())
	candidate, err := service.DecodeAutomationDraftYAML([]byte(html.UnescapeString(match[1])))
	require.NoError(t, err)
	return candidate
}

func automationHiddenValueFromResponse(t *testing.T, response *httptest.ResponseRecorder, name string) string {
	t.Helper()
	match := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]*)"`).FindStringSubmatch(response.Body.String())
	require.Len(t, match, 2, response.Body.String())
	return html.UnescapeString(match[1])
}

func automationDraftCandidateJSONForTest(t *testing.T, candidate models.AutomationDraftCandidate) string {
	t.Helper()
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)
	return string(raw)
}

func automationCandidateWithoutNodeHandler(candidate models.AutomationDraftCandidate, key string) models.AutomationDraftCandidate {
	nodes := make([]models.AutomationDraftNode, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		if node.Key != key {
			nodes = append(nodes, node)
		}
	}
	edges := make([]models.AutomationDraftEdge, 0, len(candidate.Edges))
	for _, edge := range candidate.Edges {
		if edge.From != key && edge.To != key {
			edges = append(edges, edge)
		}
	}
	candidate.Nodes = nodes
	candidate.Edges = edges
	return candidate
}

func issueCodesHandler(candidate models.AutomationDraftCandidate, drafts *service.AutomationDraftService) []string {
	issues := drafts.ValidateCandidate(candidate)
	codes := make([]string, 0, len(issues))
	for _, issue := range issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func TestAutomationBuilderConfiguresGoalsForOwnedAndImplementationTasks(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation task goals").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterNativeSDLC)
	require.NoError(t, err)

	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	preview := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"}, "automation_yaml": {yaml},
	}).Execute()
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.Contains(t, preview.Body.String(), `data-automation-yaml-editor`)
	require.Contains(t, preview.Body.String(), `goal:`)

	for i := range candidate.Nodes {
		switch candidate.Nodes[i].Key {
		case "vision_suggestions":
			candidate.Nodes[i].Config["goal"] = "Produce one reviewable product suggestion."
		case "implementation":
			candidate.Nodes[i].Config["goal"] = "Implement the approved change with tests passing."
		}
	}
	yaml, err = service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	saved := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"},
		"automation_yaml": {yaml}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, saved.Code, saved.Body.String())

	var taskID string
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT resource_id FROM automation_definition_resources resource
		JOIN automation_nodes node ON node.id = resource.node_id
		WHERE node.node_key = 'vision_suggestions' AND resource.resource_type = 'task'`).Scan(&taskID))
	goal, err := repository.NewTaskGoalRepo(tc.db).GetByTaskID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, goal)
	require.Equal(t, "Produce one reviewable product suggestion.", goal.Objective)
}

func TestAutomationLegacyMaintainedTemplateCanReplaceWithLatestRevision(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Legacy maintained template update").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), service.NewAutomationRegistrationService(automationRepo, registry))
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	saved := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, candidate)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, saved.Code, saved.Body.String())

	var automationID string
	var templateRevision *int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT id, template_revision FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID, &templateRevision))
	require.NotNil(t, templateRevision)
	require.Equal(t, service.CurrentAutomationTemplateRevision(service.AutomationAdapterNativeSDLC), *templateRevision)

	_, err = tc.db.ExecContext(ctx, `UPDATE automations SET template_revision = 0 WHERE id = ?`, automationID)
	require.NoError(t, err)
	liveOutdated := tc.HTTP().Get("/automations/" + automationID + "?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, liveOutdated.Code, liveOutdated.Body.String())
	require.Contains(t, liveOutdated.Body.String(), `data-automation-live-update-template`)
	require.Contains(t, liveOutdated.Body.String(), `id="update-automation-template-modal"`)
	require.Contains(t, liveOutdated.Body.String(), `action="/automations/`+automationID+`/builder?project_id=`+project.ID+`"`)
	require.Contains(t, liveOutdated.Body.String(), `name="update_template" value="true"`)
	portfolioOutdated := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, portfolioOutdated.Code, portfolioOutdated.Body.String())
	require.Contains(t, portfolioOutdated.Body.String(), `data-automation-card-update-template="`+automationID+`"`)
	require.Contains(t, portfolioOutdated.Body.String(), `data-automation-update-template-url="/automations/`+automationID+`/builder?project_id=`+project.ID+`"`)
	require.Contains(t, portfolioOutdated.Body.String(), `id="update-automation-card-template-modal"`)
	outdated := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusOK, outdated.Code, outdated.Body.String())
	require.Contains(t, outdated.Body.String(), `data-update-automation-template-open`)
	require.Contains(t, outdated.Body.String(), `data-automation-builder-cancel`)
	require.Contains(t, outdated.Body.String(), `href="/automations/`+automationID+`?project_id=`+project.ID+`"`)
	require.Contains(t, outdated.Body.String(), `hx-get="/automations/`+automationID+`?project_id=`+project.ID+`"`)
	_, err = tc.db.ExecContext(ctx, `UPDATE automations SET template_revision = NULL WHERE id = ?`, automationID)
	require.NoError(t, err)
	withoutVision := automationCandidateWithoutNodeHandler(candidate, "vision_suggestions")
	edited := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "candidate_json": {automationDraftCandidateJSONForTest(t, withoutVision)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, edited.Code, edited.Body.String())
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT template_revision FROM automations WHERE id = ?`, automationID).Scan(&templateRevision))
	require.Nil(t, templateRevision, "ordinary edits must not claim a legacy graph matches the latest template")

	opened := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusOK, opened.Code, opened.Body.String())
	require.Contains(t, opened.Body.String(), `data-update-automation-template-open`)
	require.Contains(t, opened.Body.String(), "replaces your current nodes, connections, prompts, and schedules")

	updated := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "update_template": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, updated.Code, updated.Body.String())
	require.NotEmpty(t, updated.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automation_versions"), "template update must replace the graph without adding history")
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT template_revision FROM automations WHERE id = ?`, automationID).Scan(&templateRevision))
	require.NotNil(t, templateRevision)
	require.Equal(t, service.CurrentAutomationTemplateRevision(service.AutomationAdapterNativeSDLC), *templateRevision)
	definition, err := automationRepo.GetDefinition(ctx, project.ID, automationID)
	require.NoError(t, err)
	require.NotNil(t, definition)
	require.NotNil(t, automationNodeByKeyHandler(definition.Nodes, "vision_suggestions"), "updating replaces customizations with the canonical latest template")

	current := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, http.StatusOK, current.Code, current.Body.String())
	require.NotContains(t, current.Body.String(), `data-update-automation-template-open`)
	liveCurrent := tc.HTTP().Get("/automations/" + automationID + "?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, liveCurrent.Code, liveCurrent.Body.String())
	require.NotContains(t, liveCurrent.Body.String(), `data-automation-live-update-template`)
	portfolioCurrent := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, portfolioCurrent.Code, portfolioCurrent.Body.String())
	require.NotContains(t, portfolioCurrent.Body.String(), `data-automation-card-update-template`)
}

func automationNodeByKeyHandler(nodes []models.AutomationNode, key string) *models.AutomationNode {
	for i := range nodes {
		if nodes[i].NodeKey == key {
			return &nodes[i]
		}
	}
	return nil
}

func TestAutomationGitHubTemplateSaveExplainsUnavailableProjectSetup(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	projectDefault := createAgent(t, tc.llmConfigRepo, func(a *models.LLMConfig) {
		a.Name = "Project default automation model"
		a.IsDefault = false
	})
	project := tc.CreateProject().WithName("GitHub template without setup").Build()
	project.DefaultAgentConfigID = &projectDefault.ID
	require.NoError(t, tc.projectRepo.Update(ctx, project))
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	githubAuthRepo := repository.NewGitHubAuthRepo(tc.db)
	validator.SetCapabilityDependencies(tc.projectRepo, tc.settingsRepo, githubAuthRepo)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	capabilities.SetLLMConfigRepository(tc.llmConfigRepo)
	capabilities.SetGitHubAuthRepository(githubAuthRepo)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	preview := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"template"}, "template_key": {service.AutomationAdapterGitHubSDLC},
	}).Execute()
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	require.NotContains(t, preview.Body.String(), "required_capabilities")
	candidate := automationCandidateFromResponse(t, preview)
	for _, node := range candidate.Nodes {
		if node.Type == models.AutomationNodeTrigger || node.Type == models.AutomationNodeAgentTask {
			if _, hasPrompt := node.Config["prompt"]; hasPrompt || node.Role == "implementation" {
				modelConfigID, _ := node.Config["model_config_id"].(string)
				require.Equal(t, "default", modelConfigID, "node %s should use dynamic project default model", node.Key)
				require.NotEqual(t, projectDefault.ID, modelConfigID, "node %s should not pin the current project model ID", node.Key)
			}
		}
	}

	saved := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, candidate)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusOK, saved.Code, saved.Body.String())
	require.Empty(t, saved.Header().Get("HX-Redirect"))
	require.Contains(t, saved.Body.String(), "Save did not apply")
	require.Contains(t, saved.Body.String(), `data-automation-yaml-validation`)
	require.Contains(t, saved.Body.String(), "Configure connected GitHub authentication")
	require.Contains(t, saved.Body.String(), "at least one GitHub Authorized User")
	require.Contains(t, saved.Body.String(), "a project GitHub repository URL or a GitHub remote")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationGitHubTemplateSaveUsesVisibleGitHubSetupAndBrowserActionFields(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Configured GitHub template").Build()
	project.RepoURL = "https://github.com/openvibely/openvibely"
	require.NoError(t, tc.projectRepo.Update(ctx, project))
	require.NoError(t, tc.settingsRepo.Set(ctx, service.GitHubSettingAuthMode, service.GitHubAuthModePAT))
	require.NoError(t, tc.settingsRepo.Set(ctx, service.GitHubSettingPAT, "configured-test-pat"))
	githubAuthRepo := repository.NewGitHubAuthRepo(tc.db)
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "configured-user"}))
	github := &fakeGitHubService{
		statusFn: func(context.Context) (service.GitHubConnectionStatus, error) {
			return service.GitHubConnectionStatus{AuthMode: service.GitHubAuthModePAT, Configured: true, Connected: true, HasPAT: true}, nil
		},
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			require.Equal(t, project.RepoURL, repoURL)
			require.Empty(t, repoPath)
			return &service.GitHubRepoRef{Owner: "openvibely", Name: "openvibely", FullName: "openvibely/openvibely", HTMLURL: "https://github.com/openvibely/openvibely"}, nil
		},
	}
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	validator.SetCapabilityDependencies(tc.projectRepo, tc.settingsRepo, githubAuthRepo)
	validator.SetGitHubConnectionProvider(github)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	capabilities.SetGitHubAuthRepository(githubAuthRepo)
	capabilities.SetGitHubConnectionProvider(github)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	for i := range candidate.Nodes {
		switch candidate.Nodes[i].Role {
		case "create_github_issue":
			candidate.Nodes[i].Config["instructions"] = "Open one focused, reviewable GitHub issue."
			candidate.Nodes[i].Config["labels"] = []string{"suggestion"}
		case "open_pull_request":
			candidate.Nodes[i].Config["instructions"] = "Open a reviewable pull request linked to the source issue."
			candidate.Nodes[i].Config["base"] = ""
			candidate.Nodes[i].Config["draft"] = false
		}
	}

	saved := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, candidate)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, saved.Code, saved.Body.String())
	require.NotEmpty(t, saved.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automations"))
	require.NotZero(t, tableCountHandler(t, tc, "tasks"))
	require.NotZero(t, tableCountHandler(t, tc, "schedules"))
	var legacyInboxRows int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM github_project_inboxes WHERE project_id = ?`, project.ID).Scan(&legacyInboxRows))
	require.Zero(t, legacyInboxRows, "visible Authorized Users must not require a hidden legacy inbox row")

	var automationID string
	var templateRevision *int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT id, template_revision FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID, &templateRevision))
	require.NotNil(t, templateRevision)
	require.Equal(t, service.CurrentAutomationTemplateRevision(service.AutomationAdapterGitHubSDLC), *templateRevision)
	withoutVision := automationCandidateWithoutNodeHandler(candidate, "vision_suggestions")
	edited := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"edit"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, withoutVision)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, edited.Code, edited.Body.String())
	require.NotEmpty(t, edited.Header().Get("HX-Redirect"))
	definition, err := automationRepo.GetDefinition(ctx, project.ID, automationID)
	require.NoError(t, err)
	for _, node := range definition.Nodes {
		require.NotEqual(t, "vision_suggestions", node.NodeKey)
	}
	for _, resource := range definition.Resources {
		require.NotEqual(t, "vision_suggestions", resource.NodeKey)
	}

	withoutRequiredOutcome := automationCandidateWithoutNodeHandler(withoutVision, "completed")
	invalid := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"edit"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, withoutRequiredOutcome)}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusOK, invalid.Code, invalid.Body.String())
	require.Empty(t, invalid.Header().Get("HX-Redirect"))
	require.Contains(t, invalid.Body.String(), "Human review node")
	require.Contains(t, invalid.Body.String(), "review")
	require.Contains(t, invalid.Body.String(), "terminal Outcome")
}

func TestAutomationBrowserRejectsForgedNewVisionDriverCandidateAndAllowsExistingEdit(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Forged Vision Driver candidate").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate, err := drafts.TemplateCandidate(service.AutomationAdapterVisionDriver)
	require.NoError(t, err)
	response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, candidate)}, "save_changes": {"true"},
		"node_vision_driver_enabled": {"true"},
	}).Execute()

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Empty(t, response.Header().Get("HX-Redirect"))
	require.Contains(t, response.Body.String(), "Save did not apply")
	require.Contains(t, response.Body.String(), "Vision Driver")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	existing := seedExistingVisionDriverForHandler(t, tc, automationRepo, project.ID, candidate)
	candidate.Description = "Edited existing Vision Driver through the browser"
	candidate.Nodes[0].Config["prompt"] = "Use the edited browser prompt."
	edited := tc.HTMX().Post("/automations/" + existing.Automation.ID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"edit"},
		"candidate_json": {automationDraftCandidateJSONForTest(t, candidate)}, "save_changes": {"true"},
		"node_vision_driver_enabled": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, edited.Code, edited.Body.String())
	require.NotEmpty(t, edited.Header().Get("HX-Redirect"))
	require.Equal(t, 1, tableCountHandler(t, tc, "automations"))
	stored, err := automationRepo.GetDefinition(context.Background(), project.ID, existing.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, candidate.Description, stored.Automation.Description)
}

func seedExistingVisionDriverForHandler(t *testing.T, tc *TestContext, automationRepo *repository.AutomationRepo, projectID string, candidate models.AutomationDraftCandidate) *models.AutomationDefinition {
	t.Helper()
	ctx := context.Background()
	task := &models.Task{ProjectID: projectID, Title: "Existing Vision Driver", Prompt: candidate.Nodes[0].Config["prompt"].(string),
		Category: models.CategoryScheduled, Priority: 2, Status: models.StatusPending, CreatedVia: "test-existing-vision-driver"}
	require.NoError(t, tc.taskRepo.Create(ctx, task))
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, schedule))

	nodes := make([]models.AutomationNodeSpec, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		config, err := json.Marshal(node.Config)
		require.NoError(t, err)
		position := models.AutomationDraftPoint{}
		if node.Position != nil {
			position = *node.Position
		}
		nodes = append(nodes, models.AutomationNodeSpec{Key: node.Key, Name: node.Name, Type: node.Type, Role: node.Role,
			ConfigJSON: string(config), PositionX: position.X, PositionY: position.Y})
	}
	edges := make([]models.AutomationEdgeSpec, 0, len(candidate.Edges))
	for i, edge := range candidate.Edges {
		condition, err := json.Marshal(edge.Condition)
		require.NoError(t, err)
		edges = append(edges, models.AutomationEdgeSpec{Key: edge.Key, SourceNodeKey: edge.From, TargetNodeKey: edge.To,
			Label: edge.Label, ConditionJSON: string(condition), DisplayOrder: i})
	}
	definition, reused, err := automationRepo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: projectID, StableKey: "vision-driver/existing-handler", Name: candidate.Name, Description: candidate.Description,
		AutomationType: candidate.AutomationType, AdapterKey: candidate.AdapterKey, CreatedVia: "test", Nodes: nodes, Edges: edges,
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_driver", ResourceType: "task", ResourceID: task.ID, Relation: "owned"},
			{NodeKey: "vision_driver", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"},
		},
	})
	require.NoError(t, err)
	require.False(t, reused)
	return definition
}

func TestAutomationBuilderWebSaveIsBrowserLocalUntilAtomicSaveAndProjectScoped(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Builder Project").Build()
	other := tc.CreateProject().WithName("Builder Other").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("handler-confirmation-secret-32-bytes"))
	lifecycle := service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo)
	agentRepo := repository.NewAgentRepo(tc.db)
	architect := models.Agent{Name: "Builder Architect", Key: "builder_architect", Scope: models.AgentScopeProject, ProjectID: project.ID,
		Enabled: true, SelectableAsPrimary: true, Skills: []models.SkillConfig{{Name: "project-guidance", Description: "Guide", Content: "safe"}}}
	require.NoError(t, agentRepo.Create(context.Background(), &architect))
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, agentRepo, tc.taskRepo, tc.settingsRepo)
	capabilities.SetLLMConfigRepository(tc.llmConfigRepo)
	drafts.SetCapabilitySnapshotBuilder(capabilities)
	planner.SetAgentRepository(agentRepo)
	compiler.SetAgentRepository(agentRepo)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, planner, compiler, confirmation, lifecycle)

	portfolio := tc.HTTP().Get("/automations?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, portfolio.Code)
	body := portfolio.Body.String()
	for _, marker := range []string{
		`>+ New Automation</`,
		`Create one from Template, Describe, or Custom.`,
		`data-automation-new-menu`,
		`data-automation-new-template`,
		`data-automation-new-describe`,
		`data-automation-new-custom`,
		`id="automation-template-modal"`,
		`id="automation-describe-modal"`,
		`name="source" value="template"`,
		`name="source" value="describe"`,
		`name="source" value="blank"`,
		`Native SDLC`,
		`GitHub SDLC`,
		`data-template-description`,
	} {
		require.Contains(t, body, marker)
	}
	require.NotContains(t, body, `data-template-key="vision_driver"`)
	require.NotContains(t, body, `Create one from Template, Describe It, or Blank.`)
	require.NotContains(t, body, ">Vision Driver</option>")
	require.NotContains(t, body, "Register Existing")

	retiredNewPage := tc.HTTP().Get("/automations/new?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusNotFound, retiredNewPage.Code)
	retiredNewPartial := tc.HTMX().Get("/automations/new?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusNotFound, retiredNewPartial.Code)

	rejectedVisionTemplate := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"template"}, "template_key": {service.AutomationAdapterVisionDriver},
	}).Execute()
	require.Equal(t, http.StatusBadRequest, rejectedVisionTemplate.Code, rejectedVisionTemplate.Body.String())
	require.Contains(t, rejectedVisionTemplate.Body.String(), "unsupported automation template")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	preview := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"template"}, "template_key": {service.AutomationAdapterNativeSDLC},
	}).Execute()
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	previewBody := preview.Body.String()
	require.Contains(t, previewBody, `id="automation-builder"`)
	require.Contains(t, previewBody, `data-automation-editable-breadcrumb`)
	require.Contains(t, previewBody, `data-automation-builder-header-actions`)
	require.Contains(t, previewBody, `data-automation-builder-save`)
	require.Contains(t, previewBody, `name="automation_name" value="Native SDLC"`)
	require.NotContains(t, previewBody, "This template is browser-local until you save its YAML definition.")
	require.NotContains(t, previewBody, `New Automation`)
	require.NotContains(t, previewBody, `Saving validates and applies this Automation immediately.`)
	require.NotContains(t, previewBody, "Suggested nodes")
	require.NotContains(t, previewBody, "Quick-add nodes understood by this Automation’s runtime.")
	require.NotContains(t, previewBody, "Enabled when applied")
	require.NotContains(t, previewBody, `_enabled\"`)
	require.Empty(t, preview.Header().Get("HX-Redirect"), "selecting a template must remain browser-local until Save changes")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))

	candidate := automationCandidateFromResponse(t, preview)
	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	saveValues := url.Values{
		"project_id": {project.ID}, "builder_source": {"template"}, "automation_yaml": {yaml}, "save_changes": {"true"},
	}
	created := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(saveValues).Execute()
	require.Equal(t, http.StatusNoContent, created.Code, created.Body.String())
	var automationID, versionID string
	require.NoError(t, tc.db.QueryRow(`SELECT id, published_version_id FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID, &versionID))
	require.Equal(t, fmt.Sprintf("/automations/%s?project_id=%s", automationID, project.ID), created.Header().Get("HX-Redirect"))
	require.NotZero(t, tableCountHandler(t, tc, "tasks"))
	require.NotZero(t, tableCountHandler(t, tc, "schedules"))
	var enabledSchedules int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE enabled = 1`).Scan(&enabledSchedules))
	require.Equal(t, tableCountHandler(t, tc, "schedules"), enabledSchedules, "active Automation schedules must always be enabled after Save")
	var schedulesKeepingContext int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM schedules WHERE clear_context_on_start = 0`).Scan(&schedulesKeepingContext))
	require.Zero(t, schedulesKeepingContext, "template YAML preserves clear_context_on_start for its owned schedules")
	compiledTasks, err := tc.taskRepo.ListByProject(context.Background(), project.ID, "")
	require.NoError(t, err)
	require.NotEmpty(t, compiledTasks)

	live := tc.HTTP().Get(fmt.Sprintf("/automations/%s?project_id=%s", automationID, project.ID)).Execute()
	require.Equal(t, 200, live.Code)
	require.Contains(t, live.Body.String(), `data-automation-live-edit`)
	require.NotContains(t, live.Body.String(), "Edit as new draft")
	cloned := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 200, cloned.Code)
	require.Contains(t, cloned.Body.String(), `id="automation-builder"`)
	var publishedCount, draftCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'published'`, automationID).Scan(&publishedCount))
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, automationID).Scan(&draftCount))
	require.Equal(t, 1, publishedCount)
	require.Zero(t, draftCount, "opening Edit automation must not clone a persisted draft")

	foreign := tc.HTTP().Post("/automations/" + automationID + "/builder?project_id=" + other.ID).WithForm(url.Values{"project_id": {other.ID}}).Execute()
	require.Equal(t, http.StatusNotFound, foreign.Code)
	foreignDraft := tc.HTTP().Get(fmt.Sprintf("/automations/%s/drafts/%s?project_id=%s", automationID, versionID, other.ID)).Execute()
	require.Equal(t, http.StatusNotFound, foreignDraft.Code)

	var ownedScheduleID string
	require.NoError(t, tc.db.QueryRow(`SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ? LIMIT 1`, automationID).Scan(&ownedScheduleID))
	var ownedScheduleCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automation_trigger_owners WHERE automation_id = ?`, automationID).Scan(&ownedScheduleCount))
	taskCountBeforeDelete := tableCountHandler(t, tc, "tasks")
	scheduleCountBeforeDelete := tableCountHandler(t, tc, "schedules")
	foreignDelete := tc.HTMX().Post(fmt.Sprintf("/automations/%s/delete?project_id=%s", automationID, other.ID)).WithForm(url.Values{"project_id": {other.ID}}).Execute()
	require.Equal(t, 404, foreignDelete.Code)
	stillPresent, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
	require.NoError(t, err)
	require.NotNil(t, stillPresent)

	deleted := tc.HTMX().Post(fmt.Sprintf("/automations/%s/delete?project_id=%s&search=needle&lifecycle_state=paused&health_state=degraded&automation_type=custom&adapter=custom&sort=name_desc", automationID, project.ID)).WithForm(url.Values{"project_id": {project.ID}}).Execute()
	require.Equal(t, 204, deleted.Code)
	require.Equal(t, "/automations?adapter=custom&automation_type=custom&health_state=degraded&lifecycle_state=paused&project_id="+project.ID+"&search=needle&sort=name_desc", deleted.Header().Get("HX-Redirect"))
	gone, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
	require.NoError(t, err)
	require.Nil(t, gone)
	require.Equal(t, taskCountBeforeDelete, tableCountHandler(t, tc, "tasks"), "deleting an Automation must preserve existing tasks")
	require.Equal(t, scheduleCountBeforeDelete-ownedScheduleCount, tableCountHandler(t, tc, "schedules"), "deleting an Automation must delete its owned trigger schedules")
	ownedSchedule, err := tc.scheduleRepo.GetByID(context.Background(), ownedScheduleID)
	require.NoError(t, err)
	require.Nil(t, ownedSchedule, "deleted Automation trigger must not remain as a paused schedule")
}

func TestAutomationBuilderRecoversDeletedRetainedNativeResources(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Recover Native automation resources").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	preview := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"template"}, "template_key": {service.AutomationAdapterNativeSDLC},
	}).Execute()
	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	candidate := automationCandidateFromResponse(t, preview)
	yaml, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	created := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"}, "automation_yaml": {yaml}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, created.Code, created.Body.String())

	var automationID string
	require.NoError(t, tc.db.QueryRowContext(context.Background(), `SELECT id FROM automations WHERE project_id = ?`, project.ID).Scan(&automationID))
	edit := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID},
	}).Execute()
	require.Equal(t, http.StatusOK, edit.Code, edit.Body.String())
	require.Contains(t, edit.Body.String(), `data-automation-node-detail="optimization_finder"`)
	require.Contains(t, edit.Body.String(), "optimization_finder")
	unchanged := automationCandidateFromResponse(t, edit)
	unchangedYAML, err := service.EncodeAutomationDraftYAML(unchanged)
	require.NoError(t, err)

	resourceID := func(nodeKey, resourceType string) string {
		t.Helper()
		var id string
		require.NoError(t, tc.db.QueryRowContext(context.Background(), `SELECT resource.resource_id
			FROM automation_definition_resources resource JOIN automation_nodes node ON node.id = resource.node_id
			WHERE resource.project_id = ? AND resource.automation_id = ? AND resource.resource_type = ? AND node.node_key = ?`,
			project.ID, automationID, resourceType, nodeKey).Scan(&id))
		return id
	}
	oldTaskID := resourceID("optimization_finder", "task")
	oldScheduleID := resourceID("optimization_finder", "schedule")
	require.NoError(t, tc.scheduleRepo.Delete(context.Background(), oldScheduleID))
	require.NoError(t, tc.taskRepo.Delete(context.Background(), oldTaskID))
	staleEdit := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID},
	}).Execute()
	require.Equal(t, http.StatusOK, staleEdit.Code, staleEdit.Body.String())
	require.Contains(t, staleEdit.Body.String(), `data-automation-node-detail="optimization_finder"`)
	require.Contains(t, staleEdit.Body.String(), "optimization_finder")
	staleLive := tc.HTTP().Get("/automations/" + automationID + "?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, staleLive.Code, staleLive.Body.String())
	require.Contains(t, staleLive.Body.String(), `data-automation-live-node-detail="optimization_finder"`)
	require.Contains(t, staleLive.Body.String(), "optimization_finder")

	saved := tc.HTMX().Post("/automations/" + automationID + "/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "builder_source": {"template"}, "automation_yaml": {unchangedYAML}, "save_changes": {"true"},
	}).Execute()
	require.Equal(t, http.StatusNoContent, saved.Code, saved.Body.String())
	require.Equal(t, "/automations/"+automationID+"?project_id="+project.ID, saved.Header().Get("HX-Redirect"))

	definition, err := automationRepo.GetDefinition(context.Background(), project.ID, automationID)
	require.NoError(t, err)
	require.Len(t, definition.Nodes, len(unchanged.Nodes))
	newTaskID := resourceID("optimization_finder", "task")
	newScheduleID := resourceID("optimization_finder", "schedule")
	require.NotEqual(t, oldTaskID, newTaskID)
	require.NotEqual(t, oldScheduleID, newScheduleID)
	var recoveredResourceCount int
	require.NoError(t, tc.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM automation_definition_resources resource
		JOIN automation_nodes node ON node.id = resource.node_id AND node.version_id = resource.version_id
		WHERE resource.project_id = ? AND resource.automation_id = ? AND resource.version_id = ? AND node.node_key = 'optimization_finder'`,
		project.ID, automationID, definition.Version.ID).Scan(&recoveredResourceCount))
	require.Equal(t, 2, recoveredResourceCount)

	live := tc.HTTP().Get("/automations/" + automationID + "?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusOK, live.Code, live.Body.String())
	require.Contains(t, live.Body.String(), `data-automation-live-node-detail="optimization_finder"`)
	require.Contains(t, live.Body.String(), "optimization_finder")
	require.NotContains(t, live.Body.String(), "Save did not apply: schedule for node")
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
		{Key: "morning", Name: "Morning", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "review", Name: "Review changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review one focused change.", "category": "backlog", "priority": 2}},
		{Key: "notify", Name: "Request approval", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Summarize the proposed change."}},
		{Key: "approval", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "accepted", Name: "Accepted", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "rejected", Name: "Rejected", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "morning_review", From: "morning", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "review_notify", From: "review", To: "notify", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "notify_approval", From: "notify", To: "approval", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "approval_accepted", From: "approval", To: "accepted", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "approved"}},
		{Key: "approval_rejected", From: "approval", To: "rejected", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "rejected"}},
	}
	normalized, err := drafts.NormalizeCandidate(candidate)
	require.NoError(t, err)
	require.Empty(t, drafts.ValidateCandidate(normalized))
	return normalized
}

func TestAutomationBrowserSaveRejectsMalformedConnectorPorts(t *testing.T) {
	for _, test := range []struct {
		name     string
		fromPort string
		toPort   string
	}{
		{name: "reversed", fromPort: "left", toPort: "right"},
		{name: "missing source", toPort: "left"},
		{name: "missing target", fromPort: "right"},
	} {
		t.Run(test.name, func(t *testing.T) {
			tc := NewTestContext(t)
			project := tc.CreateProject().WithName("Strict connector Save").Build()
			automationRepo := repository.NewAutomationRepo(tc.db)
			registry := service.NewAutomationAdapterRegistry()
			drafts := service.NewAutomationDraftService(automationRepo, registry)
			planner := service.NewAutomationSaveValidator(registry, drafts)
			compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
			tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
			candidate, err := drafts.BlankCandidate(service.AutomationAdapterCustom)
			require.NoError(t, err)
			candidate.Name = "Strict connector Save"
			candidate.Nodes = []models.AutomationDraftNode{
				{Key: "first", Name: "First", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "First task.", "category": "active", "priority": 2}},
				{Key: "second", Name: "Second", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Second task.", "category": "active", "priority": 2}},
			}
			candidate.Edges = []models.AutomationDraftEdge{{Key: "first_second", From: "first", To: "second", FromPort: test.fromPort, ToPort: test.toPort, Condition: map[string]any{}}}
			encoded, err := json.Marshal(candidate)
			require.NoError(t, err)

			response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
				"project_id": {project.ID}, "source": {"blank"}, "candidate_json": {string(encoded)}, "save_changes": {"true"},
			}).Execute()
			require.Equal(t, http.StatusOK, response.Code)
			require.Contains(t, response.Body.String(), "Graph connections must run from a source OUT port to a target IN port.")
			require.Zero(t, tableCountHandler(t, tc, "automations"), "malformed browser geometry must not stage an Automation")
			require.Zero(t, tableCountHandler(t, tc, "tasks"))
			require.Zero(t, tableCountHandler(t, tc, "schedules"))
		})
	}
}

func TestAutomationChatExplicitSavePersistsInSingleToolCall(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation direct Chat save").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	model := models.LLMConfig{Name: "Direct save generator", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))
	mock := testutil.NewMockLLMCaller()
	mock.Response = string(candidateJSON)
	tc.handler.llmSvc.SetLLMCaller(mock)

	chatTask := models.Task{ProjectID: project.ID, Title: "Automation creation chat", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, tc.taskRepo.Create(ctx, &chatTask))
	execution := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "Create an automation that reviews vision daily"}
	require.NoError(t, tc.execRepo.Create(ctx, &execution))
	params := streamingResponseParams{ProjectID: project.ID, TaskID: chatTask.ID, ExecID: execution.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	output, handled, isError, err := runtime.Executor(ctx, "save_automation", json.RawMessage(`{"source":"describe","description":"Review vision daily and request approval"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError, output)
	require.Contains(t, output, `"active":true`)
	require.Contains(t, output, `"url":"/automations/`)
	require.NotContains(t, output, "confirmation")
	require.Equal(t, 1, tableCountHandler(t, tc, "automations"))
	require.Equal(t, 3, tableCountHandler(t, tc, "tasks"), "Save creates two runtime tasks beside the Chat thread")
	require.Equal(t, 1, tableCountHandler(t, tc, "schedules"))
	require.Nil(t, chatcontrol.Get("plan_automation_save"))
	require.NotNil(t, chatcontrol.Get("save_automation"))
	for _, def := range chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true) {
		require.NotEqual(t, "save_automation", def.Name)
	}
}

func configureAutomationChatRuntimeTestServices(t *testing.T, tc *TestContext) *service.AutomationDraftService {
	t.Helper()
	drafts, _ := configureAutomationChatRuntimeTestServicesWithRepo(t, tc)
	return drafts
}

func configureAutomationChatRuntimeTestServicesWithRepo(t *testing.T, tc *TestContext) (*service.AutomationDraftService, *repository.AutomationRepo) {
	t.Helper()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
	return drafts, automationRepo
}

func composedChannelAutomationRuntimeForTest(ctx context.Context, h *Handler, params streamingResponseParams, mode models.ChatMode, surface chatcontrol.Surface) *llmcontracts.RuntimeTools {
	channelHandlers := map[string]chatcontrol.RuntimeActionHandler{
		"get_current_project": func(context.Context, json.RawMessage) (string, error) {
			return "channel current project", nil
		},
	}
	channelRT := &llmcontracts.RuntimeTools{
		Definitions: chatcontrol.ToolDefsForContext(mode, surface, mode == models.ChatModeOrchestrate),
		Executor: chatcontrol.BuildRuntimeToolExecutorForActions(mode, surface, channelHandlers, map[string]bool{
			"get_current_project": true,
		}),
	}
	params.Surface = surface
	params.RuntimeTools = channelRT
	return h.buildStreamingResponseActionRuntime(ctx, params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(mode, surface, mode == models.ChatModeOrchestrate), mode, surface)
}

func countRowsForProject(t *testing.T, tc *TestContext, table, projectID string) int {
	t.Helper()
	var count int
	switch table {
	case "automations", "tasks":
		require.NoError(t, tc.db.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE project_id = ?`, table), projectID).Scan(&count))
	case "schedules":
		require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE t.project_id = ?`, projectID).Scan(&count))
	default:
		t.Fatalf("unsupported project row count table %q", table)
	}
	return count
}

func TestAutomationChatChannelRuntimeTemplateSavePersistsInResolvedProject(t *testing.T) {
	for _, surface := range []chatcontrol.Surface{chatcontrol.SurfaceSlack, chatcontrol.SurfaceTelegram, chatcontrol.SurfaceDiscord, chatcontrol.SurfaceEmail} {
		t.Run(string(surface), func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			project := tc.CreateProject().WithName("Channel Automation Save").Build()
			foreign := tc.CreateProject().WithName("Foreign Automation Save").Build()
			configureAutomationChatRuntimeTestServices(t, tc)
			model := models.LLMConfig{Name: "Channel default", Provider: models.ProviderTest, Model: "test", IsDefault: true}
			require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))

			runtime := composedChannelAutomationRuntimeForTest(ctx, tc.handler, streamingResponseParams{ProjectID: project.ID, PrincipalID: "channel-user"}, models.ChatModeOrchestrate, surface)
			require.True(t, runtime.HasDefinition("preview_automation_description"))
			require.True(t, runtime.HasDefinition("save_automation"))

			output, handled, isError, err := runtime.Executor(ctx, "save_automation", json.RawMessage(`{"source":"template","template_key":"native_sdlc","project_id":"`+foreign.ID+`"}`))
			require.NoError(t, err)
			require.True(t, handled)
			require.False(t, isError, output)
			require.Contains(t, output, `"active":true`)
			require.Contains(t, output, `"url":"/automations/`)
			require.Contains(t, output, `project_id=`+project.ID)
			require.Equal(t, 1, countRowsForProject(t, tc, "automations", project.ID))
			require.Zero(t, countRowsForProject(t, tc, "automations", foreign.ID))
			require.NotZero(t, countRowsForProject(t, tc, "tasks", project.ID))
			require.Zero(t, countRowsForProject(t, tc, "tasks", foreign.ID))
			require.NotZero(t, countRowsForProject(t, tc, "schedules", project.ID))
			require.Zero(t, countRowsForProject(t, tc, "schedules", foreign.ID))
		})
	}
}

func TestAutomationChatChannelRuntimeInvalidYAMLReturnsValidationWithoutPersisting(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Channel Automation Invalid").Build()
	configureAutomationChatRuntimeTestServices(t, tc)
	runtime := composedChannelAutomationRuntimeForTest(ctx, tc.handler, streamingResponseParams{ProjectID: project.ID, PrincipalID: "channel-user"}, models.ChatModeOrchestrate, chatcontrol.SurfaceDiscord)

	output, handled, isError, err := runtime.Executor(ctx, "save_automation", json.RawMessage(`{"source":"yaml","automation_yaml":"schema_version: 1\nname: duplicate\nname: duplicate\n"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError, output)
	require.Contains(t, output, `"active":false`)
	require.Contains(t, output, "invalid_yaml")
	require.Zero(t, countRowsForProject(t, tc, "automations", project.ID))
	require.Zero(t, countRowsForProject(t, tc, "tasks", project.ID))
	require.Zero(t, countRowsForProject(t, tc, "schedules", project.ID))
}

func TestAutomationChatChannelRuntimePlanModePreviewOnly(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Channel Automation Plan").Build()
	drafts := configureAutomationChatRuntimeTestServices(t, tc)
	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	model := models.LLMConfig{Name: "Channel preview model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))
	mock := testutil.NewMockLLMCaller()
	mock.Response = string(candidateJSON)
	tc.handler.llmSvc.SetLLMCaller(mock)

	runtime := composedChannelAutomationRuntimeForTest(ctx, tc.handler, streamingResponseParams{ProjectID: project.ID, PrincipalID: "channel-user"}, models.ChatModePlan, chatcontrol.SurfaceSlack)
	require.True(t, runtime.HasDefinition("preview_automation_description"))
	require.False(t, runtime.HasDefinition("save_automation"))

	preview, handled, isError, err := runtime.Executor(ctx, "preview_automation_description", json.RawMessage(`{"description":"Review vision daily"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError, preview)
	require.Contains(t, preview, `"persisted":false`)

	output, handled, isError, err := runtime.Executor(ctx, "save_automation", json.RawMessage(`{"source":"template","template_key":"native_sdlc"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isError)
	require.Contains(t, output, "requires orchestrate mode")
	require.Zero(t, countRowsForProject(t, tc, "automations", project.ID))
}

func TestAutomationChatLifecycleActionsRunPauseAndResumeSavedAutomation(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation lifecycle Chat").Build()
	drafts := configureAutomationChatRuntimeTestServices(t, tc)
	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidate.Name = "Nightly review loop"
	yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": yamlDocument})
	require.NoError(t, err)
	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	executeOK := func(name string, input json.RawMessage) map[string]any {
		t.Helper()
		output, handled, isError, execErr := runtime.Executor(ctx, name, input)
		require.NoError(t, execErr)
		require.True(t, handled)
		require.False(t, isError, output)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		return result
	}

	saved := executeOK("save_automation", payload)
	automationID, _ := saved["automation_id"].(string)
	require.NotEmpty(t, automationID)
	require.True(t, saved["active"].(bool))

	runNow := executeOK("run_automation_now", json.RawMessage(`{"name":"Nightly review loop"}`))
	require.Equal(t, automationID, runNow["automation_id"])
	require.Equal(t, "Nightly review loop", runNow["name"])
	require.Equal(t, string(models.AutomationActive), runNow["lifecycle_state"])
	require.Equal(t, "/automations/"+automationID+"?project_id="+project.ID, runNow["url"])
	require.True(t, runNow["started"].(bool))
	require.NotEmpty(t, runNow["started_invocation_ids"])

	pause := executeOK("pause_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	require.Equal(t, string(models.AutomationPaused), pause["lifecycle_state"])
	var enabled bool
	require.NoError(t, tc.db.QueryRow(`SELECT enabled FROM schedules WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ?)`, automationID).Scan(&enabled))
	require.False(t, enabled, "pausing from Chat must disable the Automation-owned trigger schedule")

	resume := executeOK("resume_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	require.Equal(t, string(models.AutomationActive), resume["lifecycle_state"])
	require.NoError(t, tc.db.QueryRow(`SELECT enabled FROM schedules WHERE id IN (SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ?)`, automationID).Scan(&enabled))
	require.True(t, enabled, "resuming from Chat must re-enable the Automation-owned trigger schedule")

	get := executeOK("get_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	automation, _ := get["automation"].(map[string]any)
	require.Equal(t, string(models.AutomationActive), automation["status"])
}

func TestAutomationChatDeleteAutomationByIDAndExactNamePreservesDomainTasks(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation delete Chat").Build()
	drafts, automationRepo := configureAutomationChatRuntimeTestServicesWithRepo(t, tc)

	domainTask := &models.Task{ProjectID: project.ID, Title: "Authoritative domain task", Prompt: "Keep this task", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 2}
	require.NoError(t, tc.taskRepo.Create(ctx, domainTask))
	domainRunAt := time.Now().UTC().Add(time.Hour)
	domainSchedule := &models.Schedule{TaskID: domainTask.ID, RunAt: domainRunAt, NextRun: &domainRunAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, domainSchedule))

	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	save := func(name string) string {
		t.Helper()
		candidate := automationChatCustomApprovalCandidate(t, drafts)
		candidate.Name = name
		yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)
		payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": yamlDocument})
		require.NoError(t, err)
		output, handled, isError, execErr := runtime.Executor(ctx, "save_automation", payload)
		require.NoError(t, execErr)
		require.True(t, handled)
		require.False(t, isError, output)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		automationID, _ := result["automation_id"].(string)
		require.NotEmpty(t, automationID)
		return automationID
	}
	delete := func(input string) map[string]any {
		t.Helper()
		output, handled, isError, execErr := runtime.Executor(ctx, "delete_automation", json.RawMessage(input))
		require.NoError(t, execErr)
		require.True(t, handled)
		require.False(t, isError, output)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		return result
	}

	firstID := save("Nightly review loop")
	secondID := save("Weekly review loop")
	taskCountBeforeDelete := countRowsForProject(t, tc, "tasks", project.ID)
	scheduleCountBeforeDelete := countRowsForProject(t, tc, "schedules", project.ID)
	var firstScheduleID, secondScheduleID string
	require.NoError(t, tc.db.QueryRow(`SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ?`, firstID).Scan(&firstScheduleID))
	require.NoError(t, tc.db.QueryRow(`SELECT schedule_id FROM automation_trigger_owners WHERE automation_id = ?`, secondID).Scan(&secondScheduleID))

	byName := delete(`{"name":"nightly review loop"}`)
	require.Equal(t, "delete_automation", byName["action"])
	require.Equal(t, firstID, byName["automation_id"])
	require.Equal(t, "Nightly review loop", byName["name"])
	require.Equal(t, true, byName["deleted"])
	require.Equal(t, "deleted", byName["lifecycle_state"])
	require.Contains(t, byName["message"], "Nightly review loop")
	firstDefinition, err := automationRepo.GetDefinition(ctx, project.ID, firstID)
	require.NoError(t, err)
	require.Nil(t, firstDefinition)
	firstSchedule, err := tc.scheduleRepo.GetByID(ctx, firstScheduleID)
	require.NoError(t, err)
	require.Nil(t, firstSchedule)

	byID := delete(fmt.Sprintf(`{"automation_id":%q}`, secondID))
	require.Equal(t, secondID, byID["automation_id"])
	require.Equal(t, "Weekly review loop", byID["name"])
	require.Equal(t, true, byID["deleted"])
	secondDefinition, err := automationRepo.GetDefinition(ctx, project.ID, secondID)
	require.NoError(t, err)
	require.Nil(t, secondDefinition)
	secondSchedule, err := tc.scheduleRepo.GetByID(ctx, secondScheduleID)
	require.NoError(t, err)
	require.Nil(t, secondSchedule)

	require.Equal(t, taskCountBeforeDelete, countRowsForProject(t, tc, "tasks", project.ID), "deleting Automations must preserve existing domain and Automation-created tasks")
	require.Equal(t, scheduleCountBeforeDelete-2, countRowsForProject(t, tc, "schedules", project.ID), "deleting Automations must remove only their owned trigger schedules")
	retainedDomainTask, err := tc.taskRepo.GetByID(ctx, domainTask.ID)
	require.NoError(t, err)
	require.NotNil(t, retainedDomainTask)
	retainedDomainSchedule, err := tc.scheduleRepo.GetByID(ctx, domainSchedule.ID)
	require.NoError(t, err)
	require.NotNil(t, retainedDomainSchedule)
}

func TestAutomationChatDeleteAutomationRejectsInvalidTargetsAndPlanMode(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation delete guards").Build()
	foreign := tc.CreateProject().WithName("Foreign Automation delete guards").Build()
	drafts, automationRepo := configureAutomationChatRuntimeTestServicesWithRepo(t, tc)
	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	foreignRuntime := tc.handler.buildChatActionToolRuntimeFromDefs(streamingResponseParams{ProjectID: foreign.ID, PrincipalID: "alice"}, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	save := func(rt *llmcontracts.RuntimeTools, name string) string {
		t.Helper()
		candidate := automationChatCustomApprovalCandidate(t, drafts)
		candidate.Name = name
		yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)
		payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": yamlDocument})
		require.NoError(t, err)
		output, handled, isError, execErr := rt.Executor(ctx, "save_automation", payload)
		require.NoError(t, execErr)
		require.True(t, handled)
		require.False(t, isError, output)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		automationID, _ := result["automation_id"].(string)
		require.NotEmpty(t, automationID)
		return automationID
	}
	firstID := save(runtime, "Duplicate review loop")
	secondID := save(runtime, "Duplicate review loop")
	foreignID := save(foreignRuntime, "Foreign review loop")

	for _, testCase := range []struct {
		input string
		want  string
	}{
		{input: `{}`, want: "automation_id or name is required"},
		{input: `{"automation_id":"missing-automation"}`, want: "not found in current project"},
		{input: `{"name":"Duplicate review loop"}`, want: "ambiguous"},
	} {
		_, handled, isError, err := runtime.Executor(ctx, "delete_automation", json.RawMessage(testCase.input))
		require.True(t, handled)
		require.True(t, isError)
		require.ErrorContains(t, err, testCase.want)
	}
	_, handled, isError, err := runtime.Executor(ctx, "delete_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, foreignID)))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "not found in current project")

	for _, automationID := range []string{firstID, secondID, foreignID} {
		projectID := project.ID
		if automationID == foreignID {
			projectID = foreign.ID
		}
		definition, getErr := automationRepo.GetDefinition(ctx, projectID, automationID)
		require.NoError(t, getErr)
		require.NotNil(t, definition, "invalid deletion target must remain intact: %s", automationID)
	}

	planRuntime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true), models.ChatModePlan, chatcontrol.SurfaceWeb)
	require.False(t, planRuntime.HasDefinition("delete_automation"))
	output, handled, isError, err := planRuntime.Executor(ctx, "delete_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, firstID)))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isError)
	require.Contains(t, output, "requires orchestrate mode")
	definition, err := automationRepo.GetDefinition(ctx, project.ID, firstID)
	require.NoError(t, err)
	require.NotNil(t, definition)
}

func TestAutomationChatDeleteAutomationRejectsInFlightDispatch(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation delete in-flight guard").Build()
	drafts, automationRepo := configureAutomationChatRuntimeTestServicesWithRepo(t, tc)
	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidate.Name = "In-flight review loop"
	yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": yamlDocument})
	require.NoError(t, err)
	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	output, handled, isError, err := runtime.Executor(ctx, "save_automation", payload)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError, output)
	var saved map[string]any
	require.NoError(t, json.Unmarshal([]byte(output), &saved))
	automationID, _ := saved["automation_id"].(string)
	require.NotEmpty(t, automationID)

	runOutput, handled, isError, err := runtime.Executor(ctx, "run_automation_now", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError, runOutput)

	_, handled, isError, err = runtime.Executor(ctx, "delete_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "in-flight dispatch work")
	definition, err := automationRepo.GetDefinition(ctx, project.ID, automationID)
	require.NoError(t, err)
	require.NotNil(t, definition)
}

func TestAutomationChatUpdateTemplateAppliesNoopsAndRejectsUnsupportedTargets(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation template update Chat").Build()
	foreign := tc.CreateProject().WithName("Foreign Automation template update Chat").Build()
	drafts := configureAutomationChatRuntimeTestServices(t, tc)
	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	planRuntime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true), models.ChatModePlan, chatcontrol.SurfaceWeb)
	require.True(t, runtime.HasDefinition("update_automation_template"))
	require.False(t, planRuntime.HasDefinition("update_automation_template"))

	executeOK := func(rt *llmcontracts.RuntimeTools, name string, input json.RawMessage) map[string]any {
		t.Helper()
		output, handled, isError, execErr := rt.Executor(ctx, name, input)
		require.NoError(t, execErr)
		require.True(t, handled)
		require.False(t, isError, output)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		return result
	}

	saved := executeOK(runtime, "save_automation", json.RawMessage(`{"source":"template","template_key":"native_sdlc"}`))
	automationID, _ := saved["automation_id"].(string)
	require.NotEmpty(t, automationID)
	currentRevision := service.CurrentAutomationTemplateRevision(service.AutomationAdapterNativeSDLC)
	require.Positive(t, currentRevision)
	_, err := tc.db.Exec(`UPDATE automations SET template_revision = 0 WHERE id = ? AND project_id = ?`, automationID, project.ID)
	require.NoError(t, err)

	listed := executeOK(runtime, "list_automations", nil)
	automations, _ := listed["automations"].([]any)
	require.Len(t, automations, 1)
	listedAutomation, _ := automations[0].(map[string]any)
	require.Equal(t, true, listedAutomation["template_update_available"])
	require.Equal(t, float64(currentRevision), listedAutomation["current_template_revision"])

	got := executeOK(runtime, "get_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	gotAutomation, _ := got["automation"].(map[string]any)
	require.Equal(t, true, gotAutomation["template_update_available"])

	beforeGraphID := currentAutomationPublishedGraphID(t, tc, automationID)
	updated := executeOK(runtime, "update_automation_template", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	require.Equal(t, true, updated["ok"])
	require.Equal(t, true, updated["applied"])
	require.Equal(t, float64(currentRevision), updated["template_revision"])
	updatedAutomation, _ := updated["automation"].(map[string]any)
	require.Equal(t, false, updatedAutomation["template_update_available"])
	require.Equal(t, float64(currentRevision), updatedAutomation["template_revision"])
	afterGraphID := currentAutomationPublishedGraphID(t, tc, automationID)
	require.NotEqual(t, beforeGraphID, afterGraphID, "outdated template update should replace the saved graph")

	alreadyCurrent := executeOK(runtime, "update_automation_template", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, automationID)))
	require.Equal(t, true, alreadyCurrent["ok"])
	require.Equal(t, false, alreadyCurrent["applied"])
	require.Equal(t, "already_current", alreadyCurrent["reason"])
	require.Equal(t, afterGraphID, currentAutomationPublishedGraphID(t, tc, automationID), "already-current update must not replace the graph")

	custom := automationChatCustomApprovalCandidate(t, drafts)
	custom.Name = "Custom approval update guard"
	customYAML, err := service.EncodeAutomationDraftYAML(custom)
	require.NoError(t, err)
	customPayload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": customYAML})
	require.NoError(t, err)
	customSaved := executeOK(runtime, "save_automation", customPayload)
	customID, _ := customSaved["automation_id"].(string)
	customGraphID := currentAutomationPublishedGraphID(t, tc, customID)
	unsupported := executeOK(runtime, "update_automation_template", json.RawMessage(`{"name":"Custom approval update guard"}`))
	require.Equal(t, false, unsupported["applied"])
	require.Equal(t, "unsupported_template", unsupported["reason"])
	require.Equal(t, customGraphID, currentAutomationPublishedGraphID(t, tc, customID), "custom Automation update must not mutate")

	foreignRuntime := tc.handler.buildChatActionToolRuntimeFromDefs(streamingResponseParams{ProjectID: foreign.ID, PrincipalID: "alice"}, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	foreignSaved := executeOK(foreignRuntime, "save_automation", json.RawMessage(`{"source":"template","template_key":"native_sdlc"}`))
	foreignID, _ := foreignSaved["automation_id"].(string)
	foreignGraphID := currentAutomationPublishedGraphID(t, tc, foreignID)
	_, handled, isError, err := runtime.Executor(ctx, "update_automation_template", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, foreignID)))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "not found in current project")
	require.Equal(t, foreignGraphID, currentAutomationPublishedGraphID(t, tc, foreignID), "foreign Automation update must not mutate")
}

func currentAutomationPublishedGraphID(t *testing.T, tc *TestContext, automationID string) string {
	t.Helper()
	var graphID string
	require.NoError(t, tc.db.QueryRow(`SELECT published_version_id FROM automations WHERE id = ?`, automationID).Scan(&graphID))
	return graphID
}

func TestAutomationChatLifecycleActionsRejectAmbiguousForeignPlanAndArchivedTargets(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation lifecycle guards").Build()
	foreign := tc.CreateProject().WithName("Foreign Automation lifecycle guards").Build()
	drafts, automationRepo := configureAutomationChatRuntimeTestServicesWithRepo(t, tc)
	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	foreignRuntime := tc.handler.buildChatActionToolRuntimeFromDefs(streamingResponseParams{ProjectID: foreign.ID, PrincipalID: "alice"}, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	save := func(rt *llmcontracts.RuntimeTools, name string) string {
		t.Helper()
		candidate := automationChatCustomApprovalCandidate(t, drafts)
		candidate.Name = name
		yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)
		payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": yamlDocument})
		require.NoError(t, err)
		output, handled, isError, execErr := rt.Executor(ctx, "save_automation", payload)
		require.NoError(t, execErr)
		require.True(t, handled)
		require.False(t, isError, output)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(output), &result))
		automationID, _ := result["automation_id"].(string)
		require.NotEmpty(t, automationID)
		return automationID
	}
	firstID := save(runtime, "Duplicate review loop")
	secondID := save(runtime, "Duplicate review loop")
	foreignID := save(foreignRuntime, "Foreign review loop")
	require.NotEqual(t, firstID, secondID)

	_, handled, isError, err := runtime.Executor(ctx, "pause_automation", json.RawMessage(`{"name":"Duplicate review loop"}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "ambiguous")
	var pausedCount int
	require.NoError(t, tc.db.QueryRow(`SELECT COUNT(*) FROM automations WHERE project_id = ? AND lifecycle_state = 'paused'`, project.ID).Scan(&pausedCount))
	require.Zero(t, pausedCount, "ambiguous name must not mutate either Automation")

	_, handled, isError, err = runtime.Executor(ctx, "pause_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, foreignID)))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "not found in current project")
	foreignDefinition, err := automationRepo.GetDefinition(ctx, foreign.ID, foreignID)
	require.NoError(t, err)
	require.NotNil(t, foreignDefinition)
	require.Equal(t, models.AutomationActive, foreignDefinition.Automation.LifecycleState)

	planRuntime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModePlan, chatcontrol.SurfaceWeb, true), models.ChatModePlan, chatcontrol.SurfaceWeb)
	require.False(t, planRuntime.HasDefinition("pause_automation"))
	output, handled, isError, err := planRuntime.Executor(ctx, "pause_automation", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, firstID)))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, isError)
	require.Contains(t, output, "requires orchestrate mode")

	require.NoError(t, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo).Archive(ctx, project.ID, firstID))
	_, handled, isError, err = runtime.Executor(ctx, "run_automation_now", json.RawMessage(fmt.Sprintf(`{"automation_id":%q}`, firstID)))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "automation must be active")
}

func TestAutomationChatSaveYAMLPersistsThroughCompilerPipeline(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation YAML Chat save").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	candidate := automationChatCustomApprovalCandidate(t, drafts)
	yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": yamlDocument})
	require.NoError(t, err)
	params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
	runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

	output, handled, isError, err := runtime.Executor(ctx, "save_automation", payload)
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, isError, output)
	require.Contains(t, output, `"active":true`)
	require.Contains(t, output, `"status":"active"`)
	require.Contains(t, output, `"url":"/automations/`)
	require.Equal(t, 1, tableCountHandler(t, tc, "automations"))
	require.Equal(t, 2, tableCountHandler(t, tc, "tasks"))
	require.Equal(t, 1, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationChatSaveYAMLRejectsCandidateIdentityFieldsWithoutSideEffects(t *testing.T) {
	for _, field := range []string{"automation_id", "version_id", "candidate", "candidate_json", "token_id"} {
		t.Run(field, func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			project := tc.CreateProject().WithName("Automation YAML Chat identity").Build()
			automationRepo := repository.NewAutomationRepo(tc.db)
			registry := service.NewAutomationAdapterRegistry()
			drafts := service.NewAutomationDraftService(automationRepo, registry)
			planner := service.NewAutomationSaveValidator(registry, drafts)
			compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
			tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
			tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
			candidate := automationChatCustomApprovalCandidate(t, drafts)
			yamlDocument, err := service.EncodeAutomationDraftYAML(candidate)
			require.NoError(t, err)
			payload := map[string]any{"source": "yaml", "automation_yaml": yamlDocument, field: "raw-identity"}
			if field == "candidate" {
				payload[field] = candidate
			}
			raw, err := json.Marshal(payload)
			require.NoError(t, err)
			params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
			runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

			output, handled, isError, err := runtime.Executor(ctx, "save_automation", raw)
			require.NoError(t, err)
			require.True(t, handled)
			require.False(t, isError, output)
			require.Contains(t, output, `"active":false`)
			require.Contains(t, output, "unsupported_candidate_identity")
			require.Contains(t, output, field)
			require.Zero(t, tableCountHandler(t, tc, "automations"))
			require.Zero(t, tableCountHandler(t, tc, "tasks"))
			require.Zero(t, tableCountHandler(t, tc, "schedules"))
		})
	}
}

func TestAutomationChatSaveYAMLRejectsInvalidDefinitionsWithoutSideEffects(t *testing.T) {
	tests := []struct {
		name        string
		yaml        func(t *testing.T, drafts *service.AutomationDraftService) string
		wantCode    string
		wantMessage string
	}{
		{
			name: "malformed YAML",
			yaml: func(t *testing.T, drafts *service.AutomationDraftService) string {
				return "schema_version: 1\nname: duplicate\nname: duplicate\n"
			},
			wantCode:    "invalid_yaml",
			wantMessage: "duplicate key",
		},
		{
			name: "unsafe YAML alias",
			yaml: func(t *testing.T, drafts *service.AutomationDraftService) string {
				return "schema_version: 1\nname: anchored\ndescription: ''\nautomation_type: custom\nadapter_key: custom\nnodes: &nodes []\nedges: *nodes\n"
			},
			wantCode:    "invalid_yaml",
			wantMessage: "aliases and anchors are not supported",
		},
		{
			name: "unsupported YAML topology",
			yaml: func(t *testing.T, drafts *service.AutomationDraftService) string {
				candidate := automationChatCustomApprovalCandidate(t, drafts)
				candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{Key: "rejected_morning", From: "rejected", To: "morning", FromPort: "right", ToPort: "left", Condition: map[string]any{}})
				document, err := service.EncodeAutomationDraftYAML(candidate)
				require.NoError(t, err)
				return document
			},
			wantCode:    "unsupported_handoff",
			wantMessage: "supported OpenVibely capability handoff",
		},
		{
			name: "invalid project capability reference",
			yaml: func(t *testing.T, drafts *service.AutomationDraftService) string {
				candidate := automationChatCustomApprovalCandidate(t, drafts)
				candidate.Nodes[1].Config["agent_ref"] = "missing-agent"
				document, err := service.EncodeAutomationDraftYAML(candidate)
				require.NoError(t, err)
				return document
			},
			wantCode:    "agent_ref",
			wantMessage: "Agent selection is unavailable in this project",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tc := NewTestContext(t)
			ctx := context.Background()
			project := tc.CreateProject().WithName("Automation YAML Chat invalid").Build()
			automationRepo := repository.NewAutomationRepo(tc.db)
			registry := service.NewAutomationAdapterRegistry()
			drafts := service.NewAutomationDraftService(automationRepo, registry)
			planner := service.NewAutomationSaveValidator(registry, drafts)
			compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
			tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
			tc.handler.SetAutomationBuilderServices(drafts, nil, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
			payload, err := json.Marshal(map[string]string{"source": "yaml", "automation_yaml": test.yaml(t, drafts)})
			require.NoError(t, err)
			params := streamingResponseParams{ProjectID: project.ID, PrincipalID: "alice"}
			runtime := tc.handler.buildChatActionToolRuntimeFromDefs(params, newChatActionSummaryCollector(), chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)

			output, handled, isError, err := runtime.Executor(ctx, "save_automation", payload)
			require.NoError(t, err)
			require.True(t, handled)
			require.False(t, isError, output)
			require.Contains(t, output, `"active":false`)
			require.Contains(t, output, test.wantCode)
			require.Contains(t, output, test.wantMessage)
			require.Zero(t, tableCountHandler(t, tc, "automations"))
			require.Zero(t, tableCountHandler(t, tc, "tasks"))
			require.Zero(t, tableCountHandler(t, tc, "schedules"))
		})
	}
}

func TestAutomationChatAndWebCreationHaveNoDraftSurfaceBeforeSave(t *testing.T) {
	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Automation save surfaces").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	tc.handler.SetAutomationBuilderServices(drafts, nil, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

	for _, removed := range []string{"create_automation_draft", "plan_automation_publication", "publish_automation_draft", "plan_automation_save"} {
		require.Nil(t, chatcontrol.Get(removed), "%s must not remain a Chat capability", removed)
	}
	saveDef := chatcontrol.Get("save_automation")
	require.NotNil(t, saveDef)
	require.NotContains(t, strings.ToLower(string(saveDef.Parameters)), "confirmation")
	for _, encoded := range []string{saveDef.Description, string(saveDef.Parameters)} {
		require.NotContains(t, strings.ToLower(encoded), "draft")
		require.NotContains(t, encoded, "version_id")
		require.NotContains(t, encoded, "automation_id")
	}

	oldRoute := tc.HTMX().Post("/automations/drafts?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}, "source": {"blank"}}).Execute()
	require.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, oldRoute.Code)
	builder := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{"project_id": {project.ID}, "source": {"blank"}}).Execute()
	require.Equal(t, http.StatusOK, builder.Code)
	require.Zero(t, tableCountHandler(t, tc, "automations"), "opening the browser-local builder must not persist an Automation")
}

func TestAutomationChatSaveRejectsCandidateIdentity(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation Chat Identity").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	candidates := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, candidates)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	confirmation := service.NewAutomationConfirmationService(automationRepo, tc.execRepo, []byte("candidate-identity-secret-32-bytes"))
	tc.handler.SetAutomationBuilderServices(candidates, nil, planner, compiler, confirmation, nil)
	candidate, err := candidates.TemplateCandidate(service.AutomationAdapterVisionDriver)
	require.NoError(t, err)
	raw, err := json.Marshal(candidate)
	require.NoError(t, err)
	chatTask := models.Task{ProjectID: project.ID, Title: "Automation planning chat", Prompt: "chat", Category: models.CategoryChat, Priority: 2, Status: models.StatusRunning}
	require.NoError(t, tc.taskRepo.Create(ctx, &chatTask))
	planExecution := models.Execution{TaskID: chatTask.ID, Status: models.ExecRunning, PromptSent: "plan it"}
	require.NoError(t, tc.execRepo.Create(ctx, &planExecution))
	initialTasks := tableCountHandler(t, tc, "tasks")

	output, err := tc.handler.executeAutomationSaveAction(ctx, streamingResponseParams{ProjectID: project.ID, TaskID: chatTask.ID, ExecID: planExecution.ID},
		json.RawMessage(fmt.Sprintf(`{"source":"candidate","candidate":%s}`, raw)))
	require.NoError(t, err)
	require.Contains(t, output, `"active":false`)
	require.Contains(t, output, "unsupported_candidate_identity")
	require.Contains(t, output, "candidate")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Equal(t, initialTasks, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationDescribeFailureIsVisibleAndPreservesInput(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Unsupported stock monitor").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, nil, nil, nil, nil)
	model := models.LLMConfig{Name: "Automation generator", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))
	mock := testutil.NewMockLLMCaller()
	mock.Response = `{"schema_version":1,"name":"Stock monitor","description":"Monitor a stock and buy or sell","automation_type":"custom","adapter_key":"custom","nodes":[],"edges":[{"key":"price_change","from":"price","to":"buy","condition":{"state":"price_increased"}}],"assumptions":[],"warnings":[]}`
	tc.handler.llmSvc.SetLLMCaller(mock)
	description := "Monitor a stock for price increases or decreases so I can buy or sell depending on the result"

	response := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"describe"}, "description": {description},
	}).Execute()

	require.Equal(t, http.StatusOK, response.Code, "HTMX only swaps successful responses by default")
	require.Equal(t, "#automation-describe-modal-content", response.Header().Get("HX-Retarget"))
	require.Equal(t, "outerHTML", response.Header().Get("HX-Reswap"))
	require.Contains(t, response.Body.String(), `id="automation-describe-modal-content"`)
	require.Contains(t, response.Body.String(), `role="alert"`)
	require.Contains(t, response.Body.String(), "Could not generate a supported Automation")
	require.Contains(t, response.Body.String(), html.EscapeString(description))
	require.Equal(t, 2, mock.CallCount(), "invalid generation receives one bounded repair attempt")
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationDescribePreviewUsesEditShell(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Describe save surface").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, validator)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, validator, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))
	model := models.LLMConfig{Name: "Automation generator", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &model))
	candidate := automationChatCustomApprovalCandidate(t, drafts)
	candidate.Name = "Review Vision Daily"
	candidate.Description = "Review the vision every day and ask for approval."
	candidateJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	mock := testutil.NewMockLLMCaller()
	mock.Response = string(candidateJSON)
	tc.handler.llmSvc.SetLLMCaller(mock)

	preview := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"describe"}, "description": {"Review vision daily and request approval"},
	}).Execute()

	require.Equal(t, http.StatusOK, preview.Code, preview.Body.String())
	previewBody := preview.Body.String()
	require.Contains(t, previewBody, `id="automation-builder"`)
	require.Contains(t, previewBody, `data-automation-editable-breadcrumb`)
	require.Contains(t, previewBody, `data-automation-builder-header-actions`)
	require.Contains(t, previewBody, `data-automation-builder-save`)
	require.Contains(t, previewBody, `name="automation_name" value="Review Vision Daily"`)
	require.NotContains(t, previewBody, "This generated design is browser-local.")
	require.NotContains(t, previewBody, `New Automation`)
	require.NotContains(t, previewBody, `Saving validates and applies this Automation immediately.`)
	require.Empty(t, preview.Header().Get("HX-Redirect"), "Describe preview must remain browser-local until Save")
	require.Equal(t, 1, mock.CallCount())
	require.Zero(t, tableCountHandler(t, tc, "automations"))
	require.Zero(t, tableCountHandler(t, tc, "tasks"))
	require.Zero(t, tableCountHandler(t, tc, "schedules"))
}

func TestAutomationCanonicalChatRuntimeExecutesPreviewAndDirectSave(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation runtime actions").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	planner := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo, tc.handler.taskSvc, tc.taskRepo, tc.scheduleRepo, planner)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(tc.projectRepo, repository.NewAgentRepo(tc.db), tc.taskRepo, tc.settingsRepo)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), nil)
	tc.handler.SetAutomationBuilderServices(drafts, capabilities, planner, compiler, nil, service.NewAutomationLifecycleService(automationRepo, tc.scheduleRepo))

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
	failedDescribe := tc.HTMX().Post("/automations/builder?project_id=" + project.ID).WithForm(url.Values{
		"project_id": {project.ID}, "source": {"describe"}, "description": {"Describe an unsupported draft"},
	}).Execute()
	require.Equal(t, http.StatusOK, failedDescribe.Code)
	require.Equal(t, "#automation-describe-modal-content", failedDescribe.Header().Get("HX-Retarget"))
	require.Equal(t, "outerHTML", failedDescribe.Header().Get("HX-Reswap"))
	require.Contains(t, failedDescribe.Body.String(), `id="automation-describe-modal-content"`)
	require.Contains(t, failedDescribe.Body.String(), "Could not generate a supported Automation")
	require.Contains(t, failedDescribe.Body.String(), "automation generation repair failed")
	require.Contains(t, failedDescribe.Body.String(), "Describe an unsupported draft")
	require.Contains(t, failedDescribe.Body.String(), "Generating and validating design")
	mock.Response = string(candidateJSON)

	savedOutput := execute("save_automation", json.RawMessage(`{"source":"describe","description":"Review vision daily and request approval"}`))
	require.Contains(t, savedOutput, `"active":true`)
	require.NotContains(t, savedOutput, `"version_id"`)
	require.NotContains(t, strings.ToLower(savedOutput), "confirmation")
	require.Contains(t, savedOutput, `"url":"/automations/`)
	require.Equal(t, 3, tableCountHandler(t, tc, "tasks"), "Save creates the Schedule-owned task and its explicit downstream Task beside the Chat thread")
	require.Equal(t, 1, tableCountHandler(t, tc, "schedules"))
	require.Zero(t, tableCountHandler(t, tc, "automation_chat_confirmation_receipts"))
}

func TestAutomationTaskFollowupGitHubToolsUseHardenedRuntime(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation follow-up GitHub safety").Build()
	project.RepoURL = "https://github.com/example/runtime.git"
	project.RepoPath = t.TempDir()
	require.NoError(t, tc.projectRepo.Update(ctx, project))
	task := models.Task{ProjectID: project.ID, Title: "Automation bug finder", Category: models.CategoryScheduled,
		Priority: 2, Status: models.StatusPending, Prompt: "find bugs"}
	require.NoError(t, tc.taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &schedule))
	automationRepo := repository.NewAutomationRepo(tc.db)
	definition, _, err := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry()).Register(ctx,
		service.AutomationRegistrationRequest{ProjectID: project.ID, AdapterKey: service.AutomationAdapterGitHubSDLC,
			StableKey: "github-sdlc/followup-safe", Resources: []models.AutomationResourceBinding{
				{NodeKey: "bug_finder", ResourceType: "task", ResourceID: task.ID},
				{NodeKey: "bug_finder", ResourceType: "schedule", ResourceID: schedule.ID},
			}})
	require.NoError(t, err)
	bugFinder := definition.Nodes[0]
	for _, node := range definition.Nodes {
		if node.NodeKey == "bug_finder" {
			bugFinder = node
		}
	}
	var invocationID string
	require.NoError(t, tc.db.QueryRowContext(ctx, `INSERT INTO automation_invocations
		(project_id, automation_id, version_id, trigger_node_id, trigger_resource_type, trigger_resource_id, occurrence_key, status, started_at)
		VALUES (?, ?, ?, ?, 'schedule', ?, 'followup-safe', 'running', CURRENT_TIMESTAMP) RETURNING id`, project.ID,
		definition.Automation.ID, definition.Version.ID, bugFinder.ID, schedule.ID).Scan(&invocationID))
	execution := models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "follow-up"}
	require.NoError(t, tc.execRepo.Create(ctx, &execution))
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID,
		InvocationID: invocationID, NodeID: bugFinder.ID}
	causalCtx := service.WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}})
	causalCtx = service.WithAutomationExecution(causalCtx, task.ID, execution.ID)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry()))
	retryItem, _, err := automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "followup-safe:retry", WorkItemKind: "task", ActivityKey: "followup-safe:retry:execution",
		ActivityType: "task_execution", ActivityStatus: models.AutomationActivityRunning,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}, {ResourceType: "execution", ResourceID: execution.ID}},
	})
	require.NoError(t, err)
	binding.WorkItemID = retryItem.ID

	var createCalls int
	github := &fakeGitHubService{
		resolveRepoFn: func(_ context.Context, repoURL, repoPath string) (*service.GitHubRepoRef, error) {
			require.Equal(t, project.RepoURL, repoURL)
			require.Empty(t, repoPath)
			return &service.GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *service.GitHubRepoRef, req service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
			createCalls++
			return &service.GitHubIssue{Number: 91, URL: "https://github.com/example/runtime/issues/91", Title: req.Title, State: "open", Labels: req.Labels}, nil
		},
	}
	tc.handler.SetGitHubService(github)
	tc.handler.llmSvc.SetGitHubIssueRuntimeProvider(github)
	tc.handler.llmSvc.SetAutomationRepo(automationRepo)
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, false)
	pathShapes := []struct {
		name   string
		params streamingResponseParams
	}{
		{name: "idle web", params: streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, ExecID: "idle-web-exec", IsTaskFollowup: true, Task: &task}},
		{name: "queued channel", params: streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, ExecID: "queued-channel-exec", IsTaskFollowup: true, AutomationContext: &models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}}},
		{name: "failed retry", params: streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, ExecID: execution.ID, IsTaskFollowup: true}},
	}
	for _, pathShape := range pathShapes {
		t.Run(pathShape.name, func(t *testing.T) {
			params := pathShape.params
			require.NoError(t, tc.handler.prepareAutomationTaskFollowup(ctx, &params))
			require.NotNil(t, params.Task)
			require.NotNil(t, params.AutomationContext)
			require.NotEmpty(t, params.AutomationContext.Bindings)
			preparedCtx := service.WithAutomationContext(ctx, *params.AutomationContext)
			preparedCtx = service.WithAutomationExecution(preparedCtx, task.ID, params.ExecID)
			hardened := tc.handler.llmSvc.AutomationGitHubRuntimeTools(preparedCtx, *params.Task, defs)
			generic := tc.handler.buildChatActionToolRuntimeFromDefs(params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
			runtime := llmcontracts.CompositeRuntimeTools(hardened, generic)
			_, handled, isError, runtimeErr := runtime.Executor(preparedCtx, "github_create_issue", json.RawMessage(`{"title":"Safe follow-up issue","assignees":["bot"]}`))
			require.True(t, handled)
			require.True(t, isError)
			require.ErrorContains(t, runtimeErr, "human GitHub assignment")
		})
	}
	require.Zero(t, createCalls, "every real task-thread entry shape must use the Automation human gate")

	params := streamingResponseParams{ProjectID: project.ID, TaskID: task.ID, ExecID: execution.ID, IsTaskFollowup: true, Task: &task, AutomationContext: &models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}}
	assembledRuntime := tc.handler.buildStreamingResponseActionRuntime(causalCtx, params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	require.False(t, assembledRuntime.HasDefinition("github_comment_on_issue"), "Automation follow-ups must not expose issue status commenting through a generic fallback")
	require.True(t, assembledRuntime.HasDefinition("github_open_pull_request"), "Automation follow-ups must retain PR publication and issue linking")
	_, handled, isError, commentErr := assembledRuntime.Executor(causalCtx, "github_comment_on_issue", json.RawMessage(`{"issue_number":91,"body":"status"}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, commentErr, "status comments are disabled")
	hardened := tc.handler.llmSvc.AutomationGitHubRuntimeTools(causalCtx, task, defs)
	generic := tc.handler.buildChatActionToolRuntimeFromDefs(params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
	runtime := llmcontracts.CompositeRuntimeTools(hardened, generic)

	_, handled, isError, err = runtime.Executor(causalCtx, "github_create_issue", json.RawMessage(`{"title":"Safe follow-up issue","assignees":["bot"]}`))
	require.True(t, handled)
	require.True(t, isError)
	require.ErrorContains(t, err, "human GitHub assignment")
	require.Zero(t, createCalls, "generic follow-up handler must not bypass the Automation human gate")

	output, handled, isError, err := runtime.Executor(causalCtx, "github_create_issue", json.RawMessage(`{"title":"Safe follow-up issue","body":"body"}`))
	require.True(t, handled)
	require.False(t, isError)
	require.NoError(t, err)
	require.Contains(t, output, `"Number":91`)
	require.Equal(t, 1, createCalls)
	var provenance int
	require.NoError(t, tc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_activity_resources
		WHERE resource_type = 'github_issue' AND resource_id = 'github:example/runtime:issue:91'`).Scan(&provenance))
	require.Equal(t, 1, provenance, "follow-up issue creation must retain Automation provenance")
}

func TestReplacedAutomationOriginTaskGitHubMutationsRemainFailClosed(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Replaced Automation origin").Build()
	project.RepoURL = "https://github.com/example/runtime.git"
	project.RepoPath = t.TempDir()
	require.NoError(t, tc.projectRepo.Update(ctx, project))

	automationRepo := repository.NewAutomationRepo(tc.db)
	registry := service.NewAutomationAdapterRegistry()
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	validator := service.NewAutomationSaveValidator(registry, drafts)
	compiler := service.NewAutomationCompiler(automationRepo,
		service.NewTaskService(tc.taskRepo, repository.NewAttachmentRepo(tc.db), nil), tc.taskRepo, tc.scheduleRepo, validator)
	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Replace task graph", Description: "Original graph",
		AutomationType: "custom", AdapterKey: service.AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{{Key: "original", Name: "Original task", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"prompt": "Run original work.", "category": "backlog", "priority": 2}}}}
	first, err := compiler.Save(ctx, service.AutomationSaveRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	originalTaskID := ""
	for _, resource := range first.Definition.Resources {
		if resource.NodeKey == "original" && resource.ResourceType == "task" {
			originalTaskID = resource.ResourceID
		}
	}
	require.NotEmpty(t, originalTaskID)
	originalTask, err := tc.taskRepo.GetByID(ctx, originalTaskID)
	require.NoError(t, err)
	require.True(t, repository.IsAutomationTaskCreatedVia(originalTask.CreatedVia))
	originalNodeID := ""
	for _, node := range first.Definition.Nodes {
		if node.NodeKey == "original" {
			originalNodeID = node.ID
		}
	}
	require.NotEmpty(t, originalNodeID)
	oldBinding := models.AutomationBinding{AutomationID: first.Definition.Automation.ID, VersionID: first.Definition.Version.ID, NodeID: originalNodeID}

	// Simulate an issue-specific Task persisted by the first atomic create_task
	// implementation before it wrote the durable CreatedVia marker. The exact
	// work-item activity key and child relation are feature-owned compatibility
	// evidence; a generic create_task activity must not be backfilled.
	legacyWorkItemID := "legacy-issue-work-item"
	require.NoError(t, tc.db.QueryRowContext(ctx, `INSERT INTO automation_work_items
		(id, project_id, automation_id, origin_version_id, work_item_key, kind, title, status)
		VALUES (?, ?, ?, ?, 'github:example/runtime:issue:17', 'github_issue', 'Issue 17', 'active') RETURNING id`,
		legacyWorkItemID, project.ID, first.Definition.Automation.ID, first.Definition.Version.ID).Scan(&legacyWorkItemID))
	legacyActivityID := "legacy-issue-create-task"
	require.NoError(t, tc.db.QueryRowContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'create_task', 'completed', CURRENT_TIMESTAMP) RETURNING id`, legacyActivityID,
		project.ID, first.Definition.Automation.ID, first.Definition.Version.ID, originalNodeID, legacyWorkItemID,
		"work-item:"+legacyWorkItemID+":implementation-task").Scan(&legacyActivityID))
	_, err = tc.db.ExecContext(ctx, `INSERT INTO automation_activity_resources
		(activity_id, resource_type, resource_id, relation) VALUES (?, 'task', ?, 'child')`, legacyActivityID, originalTask.ID)
	require.NoError(t, err)
	_, err = tc.db.ExecContext(ctx, `UPDATE tasks SET created_via = '' WHERE id = ?`, originalTask.ID)
	require.NoError(t, err)
	originalTask.CreatedVia = ""

	unrelatedTask := models.Task{ProjectID: project.ID, Title: "Unrelated pre-feature task", Category: models.CategoryBacklog,
		Priority: 2, Status: models.StatusPending, Prompt: "Remain unrelated."}
	require.NoError(t, tc.taskRepo.Create(ctx, &unrelatedTask))
	genericActivityID := "generic-create-task"
	require.NoError(t, tc.db.QueryRowContext(ctx, `INSERT INTO automation_activities
		(id, project_id, automation_id, version_id, node_id, work_item_id, activity_key, activity_type, status, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, 'execution:legacy:create-task:unrelated', 'create_task', 'completed', CURRENT_TIMESTAMP) RETURNING id`,
		genericActivityID, project.ID, first.Definition.Automation.ID, first.Definition.Version.ID, originalNodeID, legacyWorkItemID).Scan(&genericActivityID))
	_, err = tc.db.ExecContext(ctx, `INSERT INTO automation_activity_resources
		(activity_id, resource_type, resource_id, relation) VALUES (?, 'task', ?, 'child')`, genericActivityID, unrelatedTask.ID)
	require.NoError(t, err)

	replacement := candidate
	replacement.Description = "Replacement graph"
	replacement.Nodes = []models.AutomationDraftNode{{Key: "replacement", Name: "Replacement task", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Run replacement work.", "category": "backlog", "priority": 2}}}
	_, err = compiler.Save(ctx, service.AutomationSaveRequest{ProjectID: project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: replacement})
	require.NoError(t, err)
	originalTask, err = tc.taskRepo.GetByID(ctx, originalTask.ID)
	require.NoError(t, err)
	require.True(t, repository.IsAutomationTaskCreatedVia(originalTask.CreatedVia), "legacy issue-specific task must gain durable origin before projection deletion")
	unrelatedTaskAfter, err := tc.taskRepo.GetByID(ctx, unrelatedTask.ID)
	require.NoError(t, err)
	require.Empty(t, unrelatedTaskAfter.CreatedVia, "generic pre-feature create_task resources must not be registered as Automation origins")
	staleContext, err := automationRepo.ContextForTask(ctx, project.ID, originalTask.ID)
	require.NoError(t, err)
	require.Empty(t, staleContext.Bindings, "replacement removes old causal projection but preserves the domain Task")

	var issueCalls, pullRequestCalls, branchReplacementCalls int
	github := &fakeGitHubService{
		resolveRepoFn: func(_ context.Context, _, _ string) (*service.GitHubRepoRef, error) {
			return &service.GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime", HTMLURL: "https://github.com/example/runtime"}, nil
		},
		createIssueFn: func(_ context.Context, _ *service.GitHubRepoRef, _ service.GitHubCreateIssueRequest) (*service.GitHubIssue, error) {
			issueCalls++
			return &service.GitHubIssue{Number: 1}, nil
		},
		createPRFn: func(_ context.Context, _ *service.GitHubRepoRef, _ service.GitHubCreatePullRequestRequest) (*service.GitHubPullRequest, error) {
			pullRequestCalls++
			return &service.GitHubPullRequest{Number: 1}, nil
		},
		replaceBranchHeadFn: func(_ context.Context, _ *service.GitHubRepoRef, _ service.GitHubReplaceBranchHeadRequest) error {
			branchReplacementCalls++
			return nil
		},
	}
	graphSvc := service.NewAutomationGraphService(automationRepo)
	tc.handler.SetAutomationServices(graphSvc, service.NewAutomationRegistrationService(automationRepo, registry))
	tc.handler.SetGitHubService(github)
	tc.handler.llmSvc.SetGitHubIssueRuntimeProvider(github)
	tc.handler.llmSvc.SetAutomationRepo(automationRepo)

	params := streamingResponseParams{ProjectID: project.ID, TaskID: originalTask.ID, ExecID: "replacement-followup", IsTaskFollowup: true, Task: originalTask}
	require.NoError(t, tc.handler.prepareAutomationTaskFollowup(ctx, &params))
	require.NotNil(t, params.AutomationContext)
	require.True(t, params.AutomationContext.OriginTask)
	require.Empty(t, params.AutomationContext.Bindings)
	stalePrepared := streamingResponseParams{ProjectID: project.ID, TaskID: originalTask.ID, ExecID: "replacement-stale-bindings", IsTaskFollowup: true, Task: originalTask,
		AutomationContext: &models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{oldBinding}}}
	require.NoError(t, tc.handler.prepareAutomationTaskFollowup(ctx, &stalePrepared))
	require.NotNil(t, stalePrepared.AutomationContext)
	require.True(t, stalePrepared.AutomationContext.OriginTask)
	require.NotEmpty(t, stalePrepared.AutomationContext.Bindings)
	tc.handler.automationGraphSvc = nil
	withoutLookup := streamingResponseParams{ProjectID: project.ID, TaskID: originalTask.ID, ExecID: "replacement-without-lookup", IsTaskFollowup: true, Task: originalTask}
	require.NoError(t, tc.handler.prepareAutomationTaskFollowup(ctx, &withoutLookup))
	require.NotNil(t, withoutLookup.AutomationContext)
	require.True(t, withoutLookup.AutomationContext.OriginTask, "missing graph lookup must not permit generic GitHub mutation fallback")
	tc.handler.automationGraphSvc = graphSvc
	preparedCtx := service.WithAutomationContext(ctx, *params.AutomationContext)
	preparedCtx = service.WithAutomationExecution(preparedCtx, originalTask.ID, params.ExecID)
	staleQueuedCtx := service.WithAutomationContext(ctx, models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{oldBinding}})
	staleQueuedCtx = service.WithAutomationExecution(staleQueuedCtx, originalTask.ID, params.ExecID)
	defs := filterTaskThreadRuntimeToolDefs(chatcontrol.ToolDefsForContext(models.ChatModeOrchestrate, chatcontrol.SurfaceWeb, true), nil, false)
	for _, runtimeContext := range []struct {
		name string
		ctx  context.Context
	}{{name: "reconstructed origin", ctx: preparedCtx}, {name: "already prepared stale queued binding", ctx: staleQueuedCtx}} {
		hardened := tc.handler.llmSvc.AutomationGitHubRuntimeTools(runtimeContext.ctx, *params.Task, defs)
		require.NotNil(t, hardened)
		generic := tc.handler.buildChatActionToolRuntimeFromDefs(params, nil, defs, models.ChatModeOrchestrate, chatcontrol.SurfaceWeb)
		runtime := llmcontracts.CompositeRuntimeTools(hardened, generic)
		writeCount := 0
		for _, def := range hardened.Definitions {
			if def.Access != llmcontracts.RuntimeToolAccessWrite {
				continue
			}
			if runtimeContext.name == "reconstructed origin" && def.Name == "github_open_pull_request" {
				continue
			}
			writeCount++
			_, handled, isError, callErr := runtime.Executor(runtimeContext.ctx, def.Name, json.RawMessage(`{}`))
			require.True(t, handled, runtimeContext.name+": "+def.Name)
			require.True(t, isError, runtimeContext.name+": "+def.Name)
			require.ErrorContains(t, callErr, "not authorized", runtimeContext.name+": "+def.Name)
		}
		require.Greater(t, writeCount, 0, "fixture must cover every exposed hardened GitHub write")
	}
	require.Zero(t, issueCalls)
	require.Zero(t, pullRequestCalls)
	require.Zero(t, branchReplacementCalls)
}

func TestGoalAgentAutomationImplementationContinuationProjectsRunningNode(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Goal continuation Automation projection").Build()
	agent := tc.CreateLLMConfig().Build()
	task := models.Task{ProjectID: project.ID, Title: "Automation implementation", Category: models.CategoryCompleted,
		Status: models.StatusCompleted, Priority: 2, Prompt: "Implement the approved work", AgentID: &agent.ID}
	require.NoError(t, tc.taskRepo.Create(ctx, &task))
	_, err := tc.handler.taskGoalSvc.SetGoal(ctx, task.ID, "Finish implementation and validation", service.GoalOptions{})
	require.NoError(t, err)

	producerTask := models.Task{ProjectID: project.ID, Title: "Automation producer", Category: models.CategoryScheduled,
		Status: models.StatusPending, Priority: 2, Prompt: "Discover approved implementation work", AgentID: &agent.ID}
	require.NoError(t, tc.taskRepo.Create(ctx, &producerTask))
	producerSchedule := models.Schedule{TaskID: producerTask.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &producerSchedule))

	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/goal-continuation",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: producerTask.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: producerSchedule.ID},
		},
	})
	require.NoError(t, err)
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)

	var implementation models.AutomationNode
	for _, node := range definition.Nodes {
		if node.NodeKey == "implementation" {
			implementation = node
			break
		}
	}
	require.NotEmpty(t, implementation.ID)
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: implementation.ID}
	workItem, _, err := automationRepo.RecordProjectionEvent(ctx, repository.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: project.ID, Bindings: []models.AutomationBinding{binding}}, Binding: binding,
		WorkItemKey: "goal-continuation:implementation", WorkItemKind: "implementation",
		ActivityKey: "goal-continuation:implementation:created", ActivityType: "create_implementation_task",
		ActivityStatus: models.AutomationActivityCompleted,
		Resources:      []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, workItem.ID)
	active := models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: "Finishing the prior implementation run"}
	require.NoError(t, tc.execRepo.Create(ctx, &active))

	started := make(chan testutil.MockLLMCall, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseFollowup := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseFollowup)
	mock := testutil.NewMockLLMCaller()
	mock.Response = "continued"
	mock.TextOnly = "continued"
	mock.OnCall = func(_ context.Context, call testutil.MockLLMCall) {
		started <- call
		<-release
	}
	tc.handler.llmSvc.SetLLMCaller(mock)

	goalContext := lifecycle.WithHookAgent(context.Background(), lifecycle.HookAgent{AgentID: "goal-agent", SystemKind: models.AgentSystemKindGoal})
	output, err := tc.handler.executeSendToTaskTool(goalContext, streamingResponseParams{
		ProjectID: project.ID, TaskID: task.ID, IsTaskFollowup: true,
	}, json.RawMessage(`{"task_id":"current","message":"Continue the Automation implementation task."}`))
	require.NoError(t, err)
	var result struct {
		QueuedMessageID string `json:"queued_message_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(output), &result))
	require.NotEmpty(t, result.QueuedMessageID)

	queuedContext, err := automationRepo.ContextForThreadInput(ctx, project.ID, result.QueuedMessageID)
	require.NoError(t, err)
	require.Equal(t, []models.AutomationBinding{{
		AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: implementation.ID, WorkItemID: workItem.ID,
	}}, queuedContext.Bindings)

	counts, _, _, err := automationRepo.LiveNodeCounts(ctx, project.ID, definition.Automation.ID, definition.Version.ID, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, counts[implementation.ID].Running, "a queued Goal Agent continuation is active Automation work")
	require.Zero(t, counts[implementation.ID].CompletedRecently)
	portfolioCounts, err := automationRepo.PortfolioOperationalCounts(ctx, project.ID, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, portfolioCounts[definition.Automation.ID].Running)
	require.Zero(t, portfolioCounts[definition.Automation.ID].CompletedRecently)

	require.NoError(t, tc.execRepo.Complete(ctx, active.ID, models.ExecCompleted, "", "", 0, 0))
	// Completing an execution directly bypasses the normal LLM-service completion
	// callback, so explicitly run the same promotion hook. The enqueue path also
	// starts a best-effort promotion check; either contender may win the claim.
	tc.handler.PromoteQueuedTaskThreadInput(task.ID)
	promotedExecID := ""
	select {
	case call := <-started:
		require.Equal(t, "Continue the Automation implementation task.", call.Prompt)
		promotedExecID = call.ExecID
	case <-time.After(10 * time.Second):
		promoted, getErr := tc.handler.threadInputRepo.GetByID(ctx, result.QueuedMessageID)
		require.NoError(t, getErr)
		execs, listErr := tc.execRepo.ListByTaskChronological(ctx, task.ID)
		require.NoError(t, listErr)
		t.Fatalf("timed out waiting for queued Automation continuation to start (input status=%s run_exec=%s mock_calls=%d task_execs=%d)", promoted.InputStatus, promoted.RunExecutionID, mock.CallCount(), len(execs))
	}
	counts, _, _, err = automationRepo.LiveNodeCounts(ctx, project.ID, definition.Automation.ID, definition.Version.ID, time.Now().UTC().Add(-time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, counts[implementation.ID].Running)
	require.Zero(t, counts[implementation.ID].CompletedRecently)

	releaseFollowup()
	require.Eventually(t, func() bool {
		promoted, getErr := tc.handler.threadInputRepo.GetByID(ctx, result.QueuedMessageID)
		if getErr != nil || promoted == nil || promoted.InputStatus != models.ThreadInputApplied {
			return false
		}
		exec, getErr := tc.execRepo.GetByID(ctx, promotedExecID)
		if getErr != nil || exec == nil || exec.Status != models.ExecCompleted {
			return false
		}
		updatedTask, getErr := tc.taskRepo.GetByID(ctx, task.ID)
		return getErr == nil && updatedTask != nil && updatedTask.Status == models.StatusCompleted
	}, 2*time.Second, 10*time.Millisecond)
}

func TestAutomationSendToTaskPersistsCausalBindingsWithQueuedInput(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("Automation queue project").Build()
	agent := models.LLMConfig{Name: "Automation queue model", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, tc.llmConfigRepo.Create(ctx, &agent))
	agentID := agent.ID
	task := models.Task{ProjectID: project.ID, Title: "Automation queue task", Category: models.CategoryScheduled,
		Priority: 2, Status: models.StatusPending, Prompt: "causal task", AgentID: &agentID}
	require.NoError(t, tc.taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &schedule))
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{ProjectID: project.ID,
		AdapterKey: service.AutomationAdapterNativeSDLC, StableKey: "native-sdlc/queue", Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
		}})
	require.NoError(t, err)
	producer := definition.Nodes[0]
	for _, node := range definition.Nodes {
		if node.NodeKey == "vision_suggestions" {
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

type handlerAutomationExternalProvider struct {
	calls int
}

func (f *handlerAutomationExternalProvider) ResolveRepo(context.Context, string, string) (*service.GitHubRepoRef, error) {
	return &service.GitHubRepoRef{Owner: "example", Name: "runtime", FullName: "example/runtime"}, nil
}

func (f *handlerAutomationExternalProvider) GetPullRequest(context.Context, *service.GitHubRepoRef, int) (*service.GitHubPullRequest, error) {
	f.calls++
	return &service.GitHubPullRequest{}, nil
}

func TestAutomationLiveYAMLUsesLoadedGraphWithoutSecondDefinitionRead(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	h, e, llmConfigRepo := setupTestHandlerForDB(t, db)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Automation Live YAML query budget"}
	require.NoError(t, projectRepo.Create(ctx, project))

	taskRepo := repository.NewTaskRepo(db, nil)
	task := &models.Task{ProjectID: project.ID, Title: "Live YAML task", Category: models.CategoryScheduled,
		Priority: 2, Status: models.StatusPending, Prompt: "Review the current request."}
	require.NoError(t, taskRepo.Create(ctx, task))
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour),
		RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true, ClearContextOnStart: true}
	require.NoError(t, repository.NewScheduleRepo(db).Create(ctx, schedule))

	automationRepo := repository.NewAutomationRepo(db)
	registry := service.NewAutomationAdapterRegistry()
	registration := service.NewAutomationRegistrationService(automationRepo, registry)
	drafts := service.NewAutomationDraftService(automationRepo, registry)
	capabilities := service.NewAutomationCapabilitySnapshotBuilder(projectRepo, nil, taskRepo, nil)
	capabilities.SetLLMConfigRepository(llmConfigRepo)
	h.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)
	h.SetAutomationBuilderServices(drafts, capabilities, nil, nil, nil, nil)
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterNativeSDLC,
		StableKey: "native-sdlc/live-yaml-query-budget",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_suggestions", ResourceType: "task", ResourceID: task.ID},
			{NodeKey: "vision_suggestions", ResourceType: "schedule", ResourceID: schedule.ID},
		},
	})
	require.NoError(t, err)

	counter.Reset()
	counter.SetEnabled(true)
	response := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/automations/"+definition.Automation.ID+"?project_id="+project.ID, nil)
	req.Header.Set("HX-Request", "true")
	e.ServeHTTP(response, req)
	counter.SetEnabled(false)

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), "schema_version")
	statements := counter.Statements()
	count := func(fragment string) int {
		count := 0
		for _, statement := range statements {
			if strings.Contains(strings.ToLower(strings.Join(strings.Fields(statement), " ")), fragment) {
				count++
			}
		}
		return count
	}
	require.Equal(t, 1, count("from automation_versions"), "Live YAML must not reload the published version")
	require.Equal(t, 1, count("from automation_nodes"), "Live YAML must not reload automation nodes")
	require.Equal(t, 1, count("from automation_edges"), "Live YAML must not reload automation edges")
	require.Equal(t, 1, count("select adr.id, adr.project_id"), "Live YAML must not reload definition resources")
	require.Equal(t, 1, count("from automation_graph_metadata"), "Live YAML should read candidate metadata once")
	require.Zero(t, count("from agent_configs"), "Live YAML must not run capability validation")
	require.Equal(t, 13, len(statements), "Live YAML should perform only the bounded metadata read after GetLive plus the route project lookup; statements: %#v", statements)
}

func TestRefreshAutomationExternalStateRouteRendersLiveGraphAndPreservesProjectScope(t *testing.T) {
	tc := NewTestContext(t)
	ctx := context.Background()
	project := tc.CreateProject().WithName("External refresh handler project").Build()
	automationRepo := repository.NewAutomationRepo(tc.db)
	registration := service.NewAutomationRegistrationService(automationRepo, service.NewAutomationAdapterRegistry())
	tc.handler.SetAutomationServices(service.NewAutomationGraphService(automationRepo), registration)

	task := models.Task{ProjectID: project.ID, Title: "Refresh route task", Category: models.CategoryScheduled, Priority: 1, Status: models.StatusPending, Prompt: "refresh"}
	require.NoError(t, tc.taskRepo.Create(ctx, &task))
	schedule := models.Schedule{TaskID: task.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	require.NoError(t, tc.scheduleRepo.Create(ctx, &schedule))
	definition, _, err := registration.Register(ctx, service.AutomationRegistrationRequest{
		ProjectID: project.ID, AdapterKey: service.AutomationAdapterGitHubSDLC, StableKey: "github-sdlc/external-refresh-handler",
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "dev_inbox", ResourceType: "schedule", ResourceID: schedule.ID},
			{NodeKey: "dev_inbox", ResourceType: "task", ResourceID: task.ID},
		},
	})
	require.NoError(t, err)
	provider := &handlerAutomationExternalProvider{}
	tc.handler.SetAutomationExternalStateService(service.NewAutomationExternalStateService(automationRepo, repository.NewTaskPullRequestRepo(tc.db), tc.projectRepo, provider))

	response := tc.HTMX().Post(fmt.Sprintf("/automations/%s/refresh-external?project_id=%s", definition.Automation.ID, project.ID)).Execute()
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Equal(t, "automationExternalRefreshed", response.Header().Get("HX-Trigger"))
	require.Contains(t, response.Body.String(), `id="automation-live"`)
	for _, node := range definition.Nodes {
		require.Contains(t, response.Body.String(), node.Name, "the refresh route must render every Live graph node")
	}
	require.NotEmpty(t, definition.Edges)
	require.Zero(t, provider.calls, "a zero-pull refresh must not call GitHub")

	missing := tc.HTMX().Post("/automations/missing/refresh-external?project_id=" + project.ID).Execute()
	require.Equal(t, http.StatusNotFound, missing.Code)
	foreign := tc.CreateProject().WithName("External refresh other project").Build()
	mismatched := tc.HTMX().Post(fmt.Sprintf("/automations/%s/refresh-external?project_id=%s", definition.Automation.ID, foreign.ID)).Execute()
	require.Equal(t, http.StatusNotFound, mismatched.Code)
	require.Zero(t, provider.calls, "missing and project-mismatched refreshes must not call GitHub")
}
