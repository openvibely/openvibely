package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/web/templates/components"
)

// webhookTestContext sets up a test context with webhook repo wired in.
type webhookTestContext struct {
	*TestContext
	webhookRepo *repository.WebhookRepo
	agentRepo   *repository.AgentRepo
}

func newWebhookTestContext(t *testing.T) *webhookTestContext {
	t.Helper()
	tc := NewTestContext(t)
	webhookRepo := repository.NewWebhookRepo(tc.db)
	agentRepo := repository.NewAgentRepo(tc.db)
	tc.handler.SetWebhookRepo(webhookRepo)
	tc.handler.SetAgentRepo(agentRepo)
	return &webhookTestContext{
		TestContext: tc,
		webhookRepo: webhookRepo,
		agentRepo:   agentRepo,
	}
}

func (wtc *webhookTestContext) createEndpoint(t *testing.T, projectID, name string, enabled bool) *models.WebhookEndpoint {
	t.Helper()
	w := &models.WebhookEndpoint{
		ProjectID:       projectID,
		Name:            name,
		Enabled:         enabled,
		DefaultPriority: 2,
	}
	if err := wtc.webhookRepo.Create(context.Background(), w); err != nil {
		t.Fatalf("create webhook endpoint: %v", err)
	}
	return w
}

func (wtc *webhookTestContext) createAgent(t *testing.T, name string) *models.Agent {
	t.Helper()
	a := &models.Agent{Name: name, SystemPrompt: "test agent", Enabled: true, SelectableAsPrimary: true}
	if err := wtc.agentRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return a
}

func (wtc *webhookTestContext) createProjectAgent(t *testing.T, projectID, name string) *models.Agent {
	t.Helper()
	a := &models.Agent{
		Name:                name,
		SystemPrompt:        "test project agent",
		Scope:               models.AgentScopeProject,
		ProjectID:           projectID,
		Enabled:             true,
		SelectableAsPrimary: true,
	}
	if err := wtc.agentRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("create project agent: %v", err)
	}
	return a
}

func (wtc *webhookTestContext) endpointAgentIDs(t *testing.T, endpointID string) []string {
	t.Helper()
	assigned, err := wtc.webhookRepo.GetEndpointAgents(context.Background(), endpointID)
	if err != nil {
		t.Fatalf("GetEndpointAgents: %v", err)
	}
	ids := make([]string, 0, len(assigned))
	for _, assignment := range assigned {
		ids = append(ids, assignment.AgentDefinitionID)
	}
	return ids
}

func expectStringSlice(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d values %#v, want %d values %#v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("value %d = %q in %#v, want %q in %#v", i, got[i], got, want[i], want)
		}
	}
}

func (wtc *webhookTestContext) expectSubmittedTasks(t *testing.T, count int) []models.Task {
	t.Helper()
	if wtc.handler.workerSvc == nil {
		t.Fatal("worker service is not configured")
	}

	submitted := make([]models.Task, 0, count)
	deadline := time.After(2 * time.Second)
	for len(submitted) < count {
		select {
		case task := <-wtc.handler.workerSvc.Submitted():
			submitted = append(submitted, task)
		case <-deadline:
			t.Fatalf("timed out waiting for %d worker submissions, got %d", count, len(submitted))
		}
	}
	select {
	case task := <-wtc.handler.workerSvc.Submitted():
		t.Fatalf("unexpected extra worker submission for task %s", task.ID)
	default:
	}
	return submitted
}

// jsonRequest makes a JSON request to the echo server with custom headers.
func (wtc *webhookTestContext) jsonRequest(method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)
	return rec
}

func TestWebhookInbound_NotFound(t *testing.T) {
	wtc := newWebhookTestContext(t)
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/nonexistent", `{}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestWebhookInbound_Disabled(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Disabled").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "Disabled", false)

	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, `{}`,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for disabled endpoint, got %d", rec.Code)
	}
}

func TestWebhookInbound_AuthFail(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Auth").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "Auth", true)

	// Wrong secret
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, `{}`,
		map[string]string{"X-Webhook-Secret": "wrong-secret"})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong secret, got %d", rec.Code)
	}

	// No auth header at all
	rec = wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, `{}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for no auth, got %d", rec.Code)
	}
}

func TestWebhookInbound_InvalidJSON(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH JSON").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "JSONTest", true)

	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, `{not json}`,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", rec.Code)
	}
}

func TestWebhookInbound_TaskRepoNilReturnsInternalError(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Task Repo Nil").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "TaskRepoNil", true)

	// Simulate misconfigured handler dependency. Regression: should not panic.
	wtc.handler.taskRepo = nil

	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, `{"event_type":"incident.triggered"}`,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 when task repo is nil, got %d; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"error":"internal error"`) {
		t.Fatalf("expected generic internal error body, got: %s", rec.Body.String())
	}
}

func TestWebhookInbound_CreatesOneActiveTask(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Task").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "CreateTask", true)

	payload := `{"event_type":"incident.triggered","summary":"Server on fire","source":"pagerduty"}`
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, payload,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}

	// Verify response contains task info
	body := rec.Body.String()
	if !strings.Contains(body, `"category":"active"`) {
		t.Error("expected category=active in response")
	}
	if !strings.Contains(body, `"created_via":"webhook"`) {
		t.Error("expected created_via=webhook in response")
	}
	if !strings.Contains(body, `"task_id"`) {
		t.Error("expected task_id in response")
	}

	// Verify exactly one task was created
	tasks, err := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected exactly 1 task, got %d", len(tasks))
	}

	task := tasks[0]
	if task.Category != models.CategoryActive {
		t.Errorf("task category = %q, want %q", task.Category, models.CategoryActive)
	}
	if task.Status != models.StatusPending {
		t.Errorf("task status = %q, want %q", task.Status, models.StatusPending)
	}
	if task.CreatedVia != models.TaskOriginWebhook {
		t.Errorf("task created_via = %q, want %q", task.CreatedVia, models.TaskOriginWebhook)
	}
	if task.Priority != 2 {
		t.Errorf("task priority = %d, want 2", task.Priority)
	}
}

func TestWebhookInbound_DuplicatePayloadCreatesSecondTask(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Duplicate Incident").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "payments-api", true)

	payload := `{
		"event_type":"incident.triggered",
		"summary":"Nil pointer exception in /Users/dubee/go/src/github.com/openvibely/openvibely/tmp/npe_main.go",
		"severity":"critical",
		"source":"pagerduty",
		"service":{"name":"payments-api","environment":"production"},
		"incident":{"id":"P123456","status":"triggered"}
	}`

	rec1 := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, payload,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first webhook: expected 202, got %d; body=%s", rec1.Code, rec1.Body.String())
	}

	rec2 := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, payload,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec2.Code != http.StatusAccepted {
		t.Fatalf("second webhook: expected 202, got %d; body=%s", rec2.Code, rec2.Body.String())
	}

	submitted := wtc.expectSubmittedTasks(t, 2)
	if submitted[0].ID == submitted[1].ID {
		t.Fatalf("expected two distinct worker submissions, both used task %s", submitted[0].ID)
	}

	tasks, err := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
	if tasks[0].Title == tasks[1].Title {
		t.Fatalf("expected unique task titles, both were %q", tasks[0].Title)
	}
}

func TestWebhookInbound_PayloadEmbeddedInPrompt(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Prompt").Build()

	w := &models.WebhookEndpoint{
		ProjectID:          project.ID,
		Name:               "PromptTest",
		Enabled:            true,
		SystemInstructions: "You are an incident responder.",
		PromptTemplate:     "Handle this {{event_type}} event.",
		DefaultPriority:    1,
	}
	if err := wtc.webhookRepo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	payload := `{"event_type":"alert","summary":"CPU 100%"}`
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+w.PathToken, payload,
		map[string]string{"X-Webhook-Secret": w.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}

	tasks, _ := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	prompt := tasks[0].Prompt
	if !strings.Contains(prompt, "You are an incident responder.") {
		t.Error("expected system instructions in prompt")
	}
	if !strings.Contains(prompt, "Handle this alert event.") {
		t.Error("expected prompt template with event_type substituted")
	}
	if !strings.Contains(prompt, `"event_type"`) {
		t.Error("expected raw JSON payload in prompt")
	}
	if !strings.Contains(prompt, "Webhook Payload") {
		t.Error("expected payload section header in prompt")
	}
}

func TestChannelsUI_WebhookAgentPickerIsProjectScoped(t *testing.T) {
	wtc := newWebhookTestContext(t)
	projectA := wtc.CreateProject().WithName("Webhook Project A").Build()
	projectB := wtc.CreateProject().WithName("Webhook Project B").Build()

	globalAgent := wtc.createAgent(t, "Global Webhook Agent")
	projectAAgent := wtc.createProjectAgent(t, projectA.ID, "Project A Webhook Agent")
	projectBAgent := wtc.createProjectAgent(t, projectB.ID, "Project B Webhook Agent")

	rec := wtc.HTMX().Get("/channels?project_id=" + projectA.ID).Execute()
	wtc.Assert(rec).StatusCode(http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, globalAgent.ID) {
		t.Fatalf("global agent %q missing from project A webhook picker", globalAgent.ID)
	}
	if !strings.Contains(body, projectAAgent.ID) {
		t.Fatalf("project A agent %q missing from project A webhook picker", projectAAgent.ID)
	}
	if strings.Contains(body, projectBAgent.ID) {
		t.Fatalf("project B agent %q leaked into project A webhook picker", projectBAgent.ID)
	}
}

func TestWebhookCRUD_RejectsUnavailableAgentsWithoutMutation(t *testing.T) {
	wtc := newWebhookTestContext(t)
	projectA := wtc.CreateProject().WithName("Webhook Assignment A").Build()
	projectB := wtc.CreateProject().WithName("Webhook Assignment B").Build()

	validAgent := wtc.createAgent(t, "Valid Webhook Agent")
	foreignAgent := wtc.createProjectAgent(t, projectB.ID, "Foreign Webhook Agent")
	disabledAgent := wtc.createAgent(t, "Disabled Webhook Agent")
	disabledAgent.Enabled = false
	if err := wtc.agentRepo.Update(context.Background(), disabledAgent); err != nil {
		t.Fatalf("disable agent: %v", err)
	}
	nonSelectableAgent := wtc.createAgent(t, "Non-selectable Webhook Agent")
	nonSelectableAgent.SelectableAsPrimary = false
	if err := wtc.agentRepo.Update(context.Background(), nonSelectableAgent); err != nil {
		t.Fatalf("make agent non-selectable: %v", err)
	}
	archivedAgent := wtc.createAgent(t, "Archived Webhook Agent")
	archivedAgent.GeneratedStatus = models.AgentStatusArchived
	if err := wtc.agentRepo.Update(context.Background(), archivedAgent); err != nil {
		t.Fatalf("archive agent: %v", err)
	}

	createForm := url.Values{
		"project_id": {projectA.ID},
		"name":       {"Rejected Webhook"},
		"agent_ids":  {foreignAgent.ID},
	}
	createReq := httptest.NewRequest("POST", "/channels/webhooks?project_id="+projectA.ID, strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusBadRequest {
		t.Fatalf("foreign create: expected 400, got %d; body=%s", createRec.Code, createRec.Body.String())
	}
	createdWebhooks, err := wtc.webhookRepo.ListByProject(context.Background(), projectA.ID)
	if err != nil {
		t.Fatalf("list project A webhooks after rejected create: %v", err)
	}
	if len(createdWebhooks) != 0 {
		t.Fatalf("rejected create persisted %d webhook(s)", len(createdWebhooks))
	}

	endpoint := wtc.createEndpoint(t, projectA.ID, "Original Webhook", true)
	if err := wtc.webhookRepo.SetEndpointAgents(context.Background(), endpoint.ID, []string{validAgent.ID}); err != nil {
		t.Fatalf("set initial endpoint agent: %v", err)
	}
	for _, testCase := range []struct {
		name    string
		agentID string
	}{
		{name: "foreign", agentID: foreignAgent.ID},
		{name: "unknown", agentID: "missing-webhook-agent"},
		{name: "disabled", agentID: disabledAgent.ID},
		{name: "archived", agentID: archivedAgent.ID},
		{name: "non-selectable", agentID: nonSelectableAgent.ID},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			updateForm := url.Values{
				"name":      {"Should Not Save"},
				"enabled":   {"true"},
				"agent_ids": {testCase.agentID},
			}
			updateReq := httptest.NewRequest("PUT", "/channels/webhooks/"+endpoint.ID+"?project_id="+projectA.ID, strings.NewReader(updateForm.Encode()))
			updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			updateRec := httptest.NewRecorder()
			wtc.echo.ServeHTTP(updateRec, updateReq)
			if updateRec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d; body=%s", updateRec.Code, updateRec.Body.String())
			}

			updated, err := wtc.webhookRepo.GetByID(context.Background(), endpoint.ID)
			if err != nil {
				t.Fatalf("get endpoint after rejected update: %v", err)
			}
			if updated == nil || updated.Name != endpoint.Name {
				t.Fatalf("rejected update changed endpoint: %#v", updated)
			}
			expectStringSlice(t, wtc.endpointAgentIDs(t, endpoint.ID), []string{validAgent.ID})
		})
	}
}

func TestWebhookInboundAndTest_RejectIncompatibleLegacyAssignment(t *testing.T) {
	wtc := newWebhookTestContext(t)
	projectA := wtc.CreateProject().WithName("Legacy Webhook A").Build()
	projectB := wtc.CreateProject().WithName("Legacy Webhook B").Build()
	foreignAgent := wtc.createProjectAgent(t, projectB.ID, "Legacy Foreign Agent")
	endpoint := wtc.createEndpoint(t, projectA.ID, "Legacy Assignment Webhook", true)
	if err := wtc.webhookRepo.SetEndpointAgents(context.Background(), endpoint.ID, []string{foreignAgent.ID}); err != nil {
		t.Fatalf("seed legacy endpoint assignment: %v", err)
	}

	inboundRec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, `{"event_type":"legacy"}`,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if inboundRec.Code == http.StatusAccepted {
		t.Fatalf("inbound accepted incompatible legacy assignment: %s", inboundRec.Body.String())
	}

	testReq := httptest.NewRequest("POST", "/channels/webhooks/"+endpoint.ID+"/test", nil)
	testRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(testRec, testReq)
	if testRec.Code == http.StatusAccepted {
		t.Fatalf("test accepted incompatible legacy assignment: %s", testRec.Body.String())
	}

	tasks, err := wtc.taskRepo.ListByProject(context.Background(), projectA.ID, "")
	if err != nil {
		t.Fatalf("list tasks after rejected legacy assignment: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("incompatible legacy assignment created %d task(s)", len(tasks))
	}
	expectStringSlice(t, wtc.endpointAgentIDs(t, endpoint.ID), []string{foreignAgent.ID})
}

func TestWebhookInbound_PrimaryAgentMapping(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Agent").Build()
	agent1 := wtc.createAgent(t, "Primary Agent")
	agent2 := wtc.createAgent(t, "Secondary Agent")

	endpoint := wtc.createEndpoint(t, project.ID, "AgentMap", true)

	// Assign agents (agent1 first, agent2 second)
	if err := wtc.webhookRepo.SetEndpointAgents(context.Background(), endpoint.ID,
		[]string{agent1.ID, agent2.ID}); err != nil {
		t.Fatalf("SetEndpointAgents: %v", err)
	}

	payload := `{"event_type":"test"}`
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, payload,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}

	tasks, _ := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}

	task := tasks[0]
	// Primary agent should be the first one
	if task.AgentDefinitionID == nil {
		t.Fatal("expected non-nil AgentDefinitionID")
	}
	if *task.AgentDefinitionID != agent1.ID {
		t.Errorf("primary agent = %q, want %q", *task.AgentDefinitionID, agent1.ID)
	}
}

func TestWebhookInbound_TaskAgentAssignmentsPersisted(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Assignments").Build()
	agent1 := wtc.createAgent(t, "First")
	agent2 := wtc.createProjectAgent(t, project.ID, "Second")

	endpoint := wtc.createEndpoint(t, project.ID, "AssignTest", true)
	if err := wtc.webhookRepo.SetEndpointAgents(context.Background(), endpoint.ID,
		[]string{agent1.ID, agent2.ID}); err != nil {
		t.Fatalf("SetEndpointAgents: %v", err)
	}

	payload := `{"event_type":"deploy"}`
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, payload,
		map[string]string{"X-Webhook-Secret": endpoint.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	tasks, _ := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task")
	}

	assignments, err := wtc.webhookRepo.GetTaskAgentAssignments(context.Background(), tasks[0].ID)
	if err != nil {
		t.Fatalf("GetTaskAgentAssignments: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 task agent assignments, got %d", len(assignments))
	}
	if assignments[0].AgentDefinitionID != agent1.ID {
		t.Errorf("first assignment = %q, want %q", assignments[0].AgentDefinitionID, agent1.ID)
	}
	if assignments[1].AgentDefinitionID != agent2.ID {
		t.Errorf("second assignment = %q, want %q", assignments[1].AgentDefinitionID, agent2.ID)
	}
}

func TestWebhookDetail_ReturnsFullEditPayloadAndSelectedAgents(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Detail").Build()
	otherProject := wtc.CreateProject().WithName("WH Detail Other").Build()
	agent1 := wtc.createAgent(t, "Agent One")
	agent2 := wtc.createAgent(t, "Agent Two")

	endpoint := &models.WebhookEndpoint{
		ProjectID:          project.ID,
		Name:               "Detailed Hook",
		Enabled:            true,
		SystemInstructions: "Full system instructions",
		TitleTemplate:      "Incident: {{summary}}",
		PromptTemplate:     "Payload: {{payload}}",
		DefaultPriority:    4,
	}
	if err := wtc.webhookRepo.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("create webhook endpoint: %v", err)
	}
	if err := wtc.webhookRepo.SetEndpointAgents(context.Background(), endpoint.ID, []string{agent2.ID, agent1.ID}); err != nil {
		t.Fatalf("set endpoint agents: %v", err)
	}

	req := httptest.NewRequest("GET", "/channels/webhooks/"+endpoint.ID+"?project_id="+project.ID, nil)
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	var detail webhookDetailResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detail.ID != endpoint.ID || detail.ProjectID != project.ID || detail.Name != endpoint.Name || !detail.Enabled {
		t.Fatalf("detail returned wrong identity fields: %#v", detail)
	}
	if detail.Secret != endpoint.Secret || detail.SystemInstructions != endpoint.SystemInstructions || detail.TitleTemplate != endpoint.TitleTemplate || detail.PromptTemplate != endpoint.PromptTemplate || detail.DefaultPriority != endpoint.DefaultPriority {
		t.Fatalf("detail missing full edit payload: %#v", detail)
	}
	if len(detail.AgentIDs) != 2 || detail.AgentIDs[0] != agent2.ID || detail.AgentIDs[1] != agent1.ID {
		t.Fatalf("detail agent IDs = %#v, want selected agents in position order", detail.AgentIDs)
	}

	crossProjectReq := httptest.NewRequest("GET", "/channels/webhooks/"+endpoint.ID+"?project_id="+otherProject.ID, nil)
	crossProjectRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(crossProjectRec, crossProjectReq)
	if crossProjectRec.Code != http.StatusNotFound {
		t.Fatalf("cross-project detail: expected 404, got %d; body=%s", crossProjectRec.Code, crossProjectRec.Body.String())
	}
}

func TestWebhookCRUD_CreateViaForm(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH CRUD").Build()
	agent1 := wtc.createAgent(t, "Agent One")
	agent2 := wtc.createProjectAgent(t, project.ID, "Agent Two")

	form := url.Values{
		"name":                {"My Webhook"},
		"system_instructions": {"You handle alerts"},
		"default_priority":    {"1"},
		"agent_ids":           {agent1.ID, agent2.ID},
	}
	req := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d; body=%s", rec.Code, rec.Body.String())
	}

	webhooks, _ := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(webhooks))
	}
	if webhooks[0].Name != "My Webhook" {
		t.Errorf("name = %q, want My Webhook", webhooks[0].Name)
	}
	expectStringSlice(t, wtc.endpointAgentIDs(t, webhooks[0].ID), []string{agent1.ID, agent2.ID})
}

func TestWebhookCRUD_CreatePreservesEnabledFormValue(t *testing.T) {
	tests := []struct {
		name         string
		enabledValue string
		wantEnabled  bool
	}{
		{name: "omitted", wantEnabled: false},
		{name: "true", enabledValue: "true", wantEnabled: true},
		{name: "one", enabledValue: "1", wantEnabled: true},
		{name: "on", enabledValue: "on", wantEnabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wtc := newWebhookTestContext(t)
			project := wtc.CreateProject().WithName("WH Enabled Form").Build()

			form := url.Values{
				"name": {"Form Webhook " + tt.name},
			}
			if tt.enabledValue != "" {
				form.Set("enabled", tt.enabledValue)
			}
			req := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			wtc.echo.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create: expected 201, got %d; body=%s", rec.Code, rec.Body.String())
			}

			webhooks, err := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
			if err != nil {
				t.Fatalf("ListByProject: %v", err)
			}
			if len(webhooks) != 1 {
				t.Fatalf("expected 1 webhook, got %d", len(webhooks))
			}
			created, err := wtc.webhookRepo.GetByID(context.Background(), webhooks[0].ID)
			if err != nil || created == nil {
				t.Fatalf("GetByID after create: %v", err)
			}
			if created.Enabled != tt.wantEnabled {
				t.Fatalf("created webhook Enabled = %t, want %t", created.Enabled, tt.wantEnabled)
			}

			if !tt.wantEnabled {
				channelsRec := wtc.HTMX().Get("/channels?project_id=" + project.ID).Execute()
				wtc.Assert(channelsRec).StatusCode(http.StatusOK)
				card := webhookCardSectionByName(channelsRec.Body.String(), created.Name)
				if !strings.Contains(card, `badge badge-sm badge-ghost">Disabled`) {
					t.Fatalf("expected disabled webhook card badge, got %q", card)
				}
			}

			payload := `{"event_type":"form_test","summary":"Form webhook event"}`
			mac := hmac.New(sha256.New, []byte(created.Secret))
			mac.Write([]byte(payload))
			sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
			inboundRec := wtc.jsonRequest("POST", "/webhooks/inbound/"+created.PathToken, payload,
				map[string]string{"X-Hub-Signature-256": sig})

			tasks, err := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
			if err != nil {
				t.Fatalf("ListByProject tasks: %v", err)
			}
			if !tt.wantEnabled {
				if inboundRec.Code != http.StatusForbidden {
					t.Fatalf("disabled inbound: expected 403, got %d; body=%s", inboundRec.Code, inboundRec.Body.String())
				}
				if len(tasks) != 0 {
					t.Fatalf("disabled inbound created %d tasks, want 0", len(tasks))
				}
				select {
				case submitted := <-wtc.handler.workerSvc.Submitted():
					t.Fatalf("disabled inbound submitted task %s", submitted.ID)
				default:
				}
				return
			}

			if inboundRec.Code != http.StatusAccepted {
				t.Fatalf("enabled inbound: expected 202, got %d; body=%s", inboundRec.Code, inboundRec.Body.String())
			}
			if len(tasks) != 1 {
				t.Fatalf("enabled inbound created %d tasks, want 1", len(tasks))
			}
			wtc.expectSubmittedTasks(t, 1)
		})
	}
}

func TestWebhookCRUD_CreateAndUpdateNormalizeEditableFieldsAndAgents(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Form Parity").Build()
	agent1 := wtc.createAgent(t, "Agent One")
	agent2 := wtc.createProjectAgent(t, project.ID, "Agent Two")
	agent3 := wtc.createAgent(t, "Agent Three")

	createForm := url.Values{
		"project_id":          {project.ID},
		"name":                {"  Created Hook  "},
		"enabled":             {"true"},
		"system_instructions": {"  Created system  "},
		"default_priority":    {"4"},
		"title_template":      {"  Created {{summary}}  "},
		"prompt_template":     {"  Created prompt  "},
		"agent_ids":           {agent1.ID + "," + agent2.ID, agent2.ID + "," + agent3.ID},
	}
	createReq := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d; body=%s", createRec.Code, createRec.Body.String())
	}

	webhooks, err := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(webhooks))
	}
	created := webhooks[0]
	if created.Name != "Created Hook" || !created.Enabled || created.SystemInstructions != "Created system" || created.TitleTemplate != "Created {{summary}}" || created.PromptTemplate != "Created prompt" || created.DefaultPriority != 4 {
		t.Fatalf("created webhook fields were not normalized/persisted: %#v", created)
	}
	if created.PathToken == "" || created.Secret == "" {
		t.Fatalf("created webhook missing generated token/secret: %#v", created)
	}
	expectStringSlice(t, wtc.endpointAgentIDs(t, created.ID), []string{agent1.ID, agent2.ID, agent3.ID})

	updateForm := url.Values{
		"name":                {"  Updated Hook  "},
		"system_instructions": {"  Updated system  "},
		"default_priority":    {"3"},
		"title_template":      {"  Updated {{event_type}}  "},
		"prompt_template":     {"  Updated prompt  "},
		"agent_ids":           {agent3.ID + "," + agent1.ID},
	}
	updateReq := httptest.NewRequest("PUT", "/channels/webhooks/"+created.ID, strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d; body=%s", updateRec.Code, updateRec.Body.String())
	}

	updated, err := wtc.webhookRepo.GetByID(context.Background(), created.ID)
	if err != nil || updated == nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if updated.Name != "Updated Hook" || updated.Enabled || updated.SystemInstructions != "Updated system" || updated.TitleTemplate != "Updated {{event_type}}" || updated.PromptTemplate != "Updated prompt" || updated.DefaultPriority != 3 {
		t.Fatalf("updated webhook fields were not normalized/persisted: %#v", updated)
	}
	if updated.PathToken != created.PathToken || updated.Secret != created.Secret {
		t.Fatalf("update changed generated token/secret: before=%#v after=%#v", created, updated)
	}
	expectStringSlice(t, wtc.endpointAgentIDs(t, updated.ID), []string{agent3.ID, agent1.ID})
}

func TestWebhookCRUD_BlankNameLifecycleBehavior(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Blank Names").Build()

	createForm := url.Values{
		"project_id": {project.ID},
		"name":       {"   "},
	}
	createReq := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create blank name: expected 201, got %d; body=%s", createRec.Code, createRec.Body.String())
	}
	webhooks, err := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(webhooks))
	}
	if webhooks[0].Name != "New Webhook" {
		t.Fatalf("blank create name = %q, want New Webhook", webhooks[0].Name)
	}

	existing := wtc.createEndpoint(t, project.ID, "Keep Existing Name", true)
	updateForm := url.Values{
		"name":             {"   "},
		"default_priority": {"2"},
	}
	updateReq := httptest.NewRequest("PUT", "/channels/webhooks/"+existing.ID, strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update blank name: expected 200, got %d; body=%s", updateRec.Code, updateRec.Body.String())
	}
	updated, err := wtc.webhookRepo.GetByID(context.Background(), existing.ID)
	if err != nil || updated == nil {
		t.Fatalf("GetByID after blank name update: %v", err)
	}
	if updated.Name != "Keep Existing Name" {
		t.Fatalf("blank update name = %q, want existing name preserved", updated.Name)
	}
}

// TestWebhookCRUD_DefaultPriorityMatchesCanonicalScale proves the webhook
// Default Priority dropdown options persist on the canonical task priority
// scale (1=Low, 2=Normal, 3=High, 4=Urgent) for both create and update.
func TestWebhookCRUD_DefaultPriorityMatchesCanonicalScale(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Priority Scale").Build()

	for _, want := range []int{1, 2, 3, 4} {
		form := url.Values{
			"project_id":       {project.ID},
			"name":             {"Priority Hook"},
			"default_priority": {strconv.Itoa(want)},
		}
		req := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		wtc.echo.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create priority=%d: expected 201, got %d; body=%s", want, rec.Code, rec.Body.String())
		}

		webhooks, _ := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
		var created *models.WebhookEndpoint
		for i := range webhooks {
			if webhooks[i].DefaultPriority == want {
				created = &webhooks[i]
			}
		}
		if created == nil {
			t.Fatalf("expected a created webhook with DefaultPriority=%d, got %#v", want, webhooks)
		}

		// Now update it to a different value on the canonical scale and confirm
		// the persisted value matches exactly (no inversion/remapping).
		other := want%4 + 1
		updateForm := url.Values{
			"name":             {"Priority Hook Updated"},
			"default_priority": {strconv.Itoa(other)},
		}
		updateReq := httptest.NewRequest("PUT", "/channels/webhooks/"+created.ID, strings.NewReader(updateForm.Encode()))
		updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		updateRec := httptest.NewRecorder()
		wtc.echo.ServeHTTP(updateRec, updateReq)
		if updateRec.Code != http.StatusOK {
			t.Fatalf("update priority=%d: expected 200, got %d; body=%s", other, updateRec.Code, updateRec.Body.String())
		}

		updated, err := wtc.webhookRepo.GetByID(context.Background(), created.ID)
		if err != nil || updated == nil {
			t.Fatalf("GetByID after update: %v", err)
		}
		if updated.DefaultPriority != other {
			t.Errorf("updated DefaultPriority = %d, want %d", updated.DefaultPriority, other)
		}
	}
}

// TestWebhookCRUD_BlankDefaultPriorityDoesNotProduceLegacyZero proves blank/omitted
// default_priority form submissions never resolve to the legacy no-badge value 0.
func TestWebhookCRUD_BlankDefaultPriorityDoesNotProduceLegacyZero(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Priority Blank").Build()

	// Create with default_priority omitted entirely.
	createForm := url.Values{
		"project_id": {project.ID},
		"name":       {"Blank Priority Hook"},
	}
	req := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(createForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d; body=%s", rec.Code, rec.Body.String())
	}

	webhooks, _ := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook, got %d", len(webhooks))
	}
	if webhooks[0].DefaultPriority == 0 {
		t.Fatalf("blank default_priority resolved to legacy no-badge 0")
	}
	if webhooks[0].DefaultPriority != 1 {
		t.Errorf("blank default_priority = %d, want the sane default (1, Low)", webhooks[0].DefaultPriority)
	}

	// Update with default_priority sent as an explicit blank string.
	updateForm := url.Values{
		"name":             {"Blank Priority Hook"},
		"default_priority": {""},
	}
	updateReq := httptest.NewRequest("PUT", "/channels/webhooks/"+webhooks[0].ID, strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d; body=%s", updateRec.Code, updateRec.Body.String())
	}

	updated, err := wtc.webhookRepo.GetByID(context.Background(), webhooks[0].ID)
	if err != nil || updated == nil {
		t.Fatalf("GetByID after update: %v", err)
	}
	if updated.DefaultPriority == 0 {
		t.Fatalf("blank default_priority on update resolved to legacy no-badge 0")
	}
	if updated.DefaultPriority != 1 {
		t.Errorf("blank default_priority on update = %d, want the sane default (1, Low)", updated.DefaultPriority)
	}
}

// TestWebhookInbound_UrgentDefaultPriorityProducesCanonicalUrgentTask proves a
// webhook configured with the canonical "Urgent" (4) default priority creates
// a task that renders the Urgent badge and sorts above lower-priority tasks.
func TestWebhookInbound_UrgentDefaultPriorityProducesCanonicalUrgentTask(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Urgent Priority").Build()

	w := &models.WebhookEndpoint{
		ProjectID:       project.ID,
		Name:            "UrgentHook",
		Enabled:         true,
		DefaultPriority: 4, // Urgent on the canonical scale.
	}
	if err := wtc.webhookRepo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	payload := `{"event_type":"incident.triggered","summary":"Everything is down"}`
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+w.PathToken, payload,
		map[string]string{"X-Webhook-Secret": w.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}

	tasks, err := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Priority != 4 {
		t.Fatalf("task priority = %d, want 4 (canonical Urgent)", tasks[0].Priority)
	}
	if components.PriorityLabel(tasks[0].Priority) != "Urgent" {
		t.Fatalf("PriorityLabel(%d) = %q, want Urgent", tasks[0].Priority, components.PriorityLabel(tasks[0].Priority))
	}
}

// TestWebhookInbound_LegacyZeroDefaultPriorityDoesNotProduceNoBadgeTask proves
// an existing webhook endpoint with a stored legacy DefaultPriority=0 does not
// silently produce a badge-less, bottom-sorted task.
func TestWebhookInbound_LegacyZeroDefaultPriorityDoesNotProduceNoBadgeTask(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Legacy Zero Priority").Build()

	w := &models.WebhookEndpoint{
		ProjectID:       project.ID,
		Name:            "LegacyZeroHook",
		Enabled:         true,
		DefaultPriority: 0, // legacy stored value from before the scale fix.
	}
	if err := wtc.webhookRepo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Force the legacy value past repository-level validation, if any, by
	// writing it directly so this test exercises a genuinely stored 0.
	if _, err := wtc.db.Exec(`UPDATE webhook_endpoints SET default_priority = 0 WHERE id = ?`, w.ID); err != nil {
		t.Fatalf("force legacy default_priority=0: %v", err)
	}

	payload := `{"event_type":"incident.triggered","summary":"Legacy priority event"}`
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+w.PathToken, payload,
		map[string]string{"X-Webhook-Secret": w.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}

	tasks, err := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Priority == 0 {
		t.Fatalf("legacy stored DefaultPriority=0 produced a badge-less, bottom-sorted task")
	}
	if components.PriorityLabel(tasks[0].Priority) == "" {
		t.Fatalf("legacy stored DefaultPriority=0 produced a task with no priority badge")
	}
}

func TestWebhookCRUD_HTMXMutationsTriggerChannelsRefresh(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH HTMX Refresh").Build()
	agent := wtc.createAgent(t, "Webhook Agent")

	createForm := url.Values{
		"project_id":            {project.ID},
		"name":                  {"Created Hook"},
		"system_instructions":   {"Handle created hooks"},
		"default_priority":      {"1"},
		"webhook_agent_ids_csv": {agent.ID},
	}
	createReq := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(createForm.Encode()))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.Header.Set("HX-Request", "true")
	createRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d; body=%s", createRec.Code, createRec.Body.String())
	}
	assertChannelsRefreshTrigger(t, createRec)

	webhooks, _ := wtc.webhookRepo.ListByProject(context.Background(), project.ID)
	if len(webhooks) != 1 {
		t.Fatalf("expected 1 webhook after create, got %d", len(webhooks))
	}
	endpoint := webhooks[0]
	if endpoint.Name != "Created Hook" {
		t.Fatalf("created webhook name = %q", endpoint.Name)
	}

	updateForm := url.Values{
		"name":                  {"Updated Hook"},
		"enabled":               {"on"},
		"system_instructions":   {"Handle updated hooks"},
		"default_priority":      {"3"},
		"webhook_agent_ids_csv": {agent.ID},
	}
	updateReq := httptest.NewRequest("PUT", "/channels/webhooks/"+endpoint.ID, strings.NewReader(updateForm.Encode()))
	updateReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	updateReq.Header.Set("HX-Request", "true")
	updateRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(updateRec, updateReq)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d; body=%s", updateRec.Code, updateRec.Body.String())
	}
	assertChannelsRefreshTrigger(t, updateRec)

	updated, _ := wtc.webhookRepo.GetByID(context.Background(), endpoint.ID)
	if updated == nil || updated.Name != "Updated Hook" || updated.DefaultPriority != 3 {
		t.Fatalf("webhook was not updated correctly: %#v", updated)
	}

	oldSecret := updated.Secret
	rotateReq := httptest.NewRequest("POST", "/channels/webhooks/"+endpoint.ID+"/rotate-secret", nil)
	rotateReq.Header.Set("HX-Request", "true")
	rotateRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rotateRec, rotateReq)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("rotate: expected 200, got %d; body=%s", rotateRec.Code, rotateRec.Body.String())
	}
	assertChannelsRefreshTrigger(t, rotateRec)

	rotated, _ := wtc.webhookRepo.GetByID(context.Background(), endpoint.ID)
	if rotated == nil || rotated.Secret == oldSecret {
		t.Fatalf("expected rotated secret to change")
	}

	deleteReq := httptest.NewRequest("DELETE", "/channels/webhooks/"+endpoint.ID, nil)
	deleteReq.Header.Set("HX-Request", "true")
	deleteRec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d; body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	assertChannelsRefreshTrigger(t, deleteRec)

	deleted, _ := wtc.webhookRepo.GetByID(context.Background(), endpoint.ID)
	if deleted != nil {
		t.Fatalf("expected webhook to be deleted")
	}
}

func TestWebhookCRUD_Delete(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Delete").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "ToDelete", true)

	req := httptest.NewRequest("DELETE", "/channels/webhooks/"+endpoint.ID, nil)
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	got, _ := wtc.webhookRepo.GetByID(context.Background(), endpoint.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestWebhookCRUD_RotateSecret(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Rotate").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "Rotate", true)
	origSecret := endpoint.Secret

	req := httptest.NewRequest("POST", "/channels/webhooks/"+endpoint.ID+"/rotate-secret", nil)
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}

	got, _ := wtc.webhookRepo.GetByID(context.Background(), endpoint.ID)
	if got.Secret == origSecret {
		t.Error("expected different secret after rotation")
	}
}

func TestWebhookCRUD_TestRepeatedClicksCreateUniqueTasksWithAgentsAndWorkerSubmissions(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Test").Build()
	agent1 := wtc.createAgent(t, "Primary Test Agent")
	agent2 := wtc.createAgent(t, "Secondary Test Agent")
	endpoint := wtc.createEndpoint(t, project.ID, "TestEndpoint", true)
	if err := wtc.webhookRepo.SetEndpointAgents(context.Background(), endpoint.ID, []string{agent1.ID, agent2.ID}); err != nil {
		t.Fatalf("SetEndpointAgents: %v", err)
	}

	for i := 1; i <= 2; i++ {
		req := httptest.NewRequest("POST", "/channels/webhooks/"+endpoint.ID+"/test", nil)
		rec := httptest.NewRecorder()
		wtc.echo.ServeHTTP(rec, req)

		if rec.Code != http.StatusAccepted {
			t.Fatalf("test click %d: expected 202, got %d; body=%s", i, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("test click %d: decode response: %v", i, err)
		}
		if body["task_id"] == "" {
			t.Fatalf("test click %d: expected task_id response, got %s", i, rec.Body.String())
		}
	}

	submitted := wtc.expectSubmittedTasks(t, 2)
	if submitted[0].ID == submitted[1].ID {
		t.Fatalf("expected two distinct worker submissions, both used task %s", submitted[0].ID)
	}

	tasks, err := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks from repeated tests, got %d", len(tasks))
	}
	if tasks[0].Title == tasks[1].Title {
		t.Fatalf("expected unique task titles, both were %q", tasks[0].Title)
	}
	for _, task := range tasks {
		if task.CreatedVia != models.TaskOriginWebhook {
			t.Errorf("expected created_via=webhook, got %q", task.CreatedVia)
		}
		if task.AgentDefinitionID == nil || *task.AgentDefinitionID != agent1.ID {
			t.Fatalf("task %s primary agent = %v, want %s", task.ID, task.AgentDefinitionID, agent1.ID)
		}
		assignments, err := wtc.webhookRepo.GetTaskAgentAssignments(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("GetTaskAgentAssignments(%s): %v", task.ID, err)
		}
		if len(assignments) != 2 || assignments[0].AgentDefinitionID != agent1.ID || assignments[1].AgentDefinitionID != agent2.ID {
			t.Fatalf("task %s assignments = %#v, want [%s %s] in order", task.ID, assignments, agent1.ID, agent2.ID)
		}
	}
}

func TestWebhookInbound_MultipleWebhooksPerProject(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Multi").Build()

	ep1 := wtc.createEndpoint(t, project.ID, "EP1", true)
	ep2 := wtc.createEndpoint(t, project.ID, "EP2", true)

	// Send to first endpoint
	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+ep1.PathToken, `{"src":"ep1"}`,
		map[string]string{"X-Webhook-Secret": ep1.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ep1: expected 202, got %d", rec.Code)
	}

	// Send to second endpoint
	rec = wtc.jsonRequest("POST", "/webhooks/inbound/"+ep2.PathToken, `{"src":"ep2"}`,
		map[string]string{"X-Webhook-Secret": ep2.Secret})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ep2: expected 202, got %d", rec.Code)
	}

	// Both should have created tasks
	tasks, _ := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if len(tasks) != 2 {
		t.Fatalf("expected 2 tasks, got %d", len(tasks))
	}
}

func TestChannelsUI_WebhookInAddMenu(t *testing.T) {
	wtc := newWebhookTestContext(t)
	_ = wtc.CreateProject().WithName("UI Test").Build()

	rec := wtc.HTMX().Get("/channels").Execute()
	wtc.Assert(rec).StatusCode(http.StatusOK)

	body := rec.Body.String()
	if !strings.Contains(body, "Webhook") {
		t.Error("expected 'Webhook' in Add Channel menu")
	}
}

func TestChannelsUI_WebhookCardsRender(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("UI Cards").Build()

	_ = wtc.createEndpoint(t, project.ID, "My Alert Hook", true)
	_ = wtc.createEndpoint(t, project.ID, "Deploy Hook", false)
	agent1 := wtc.createAgent(t, "Webhook Agent One")
	agent2 := wtc.createAgent(t, "Webhook Agent Two")

	rec := wtc.HTMX().Get("/channels?project_id=" + project.ID).Execute()
	wtc.Assert(rec).StatusCode(http.StatusOK)

	body := rec.Body.String()
	if !strings.Contains(body, "My Alert Hook") {
		t.Error("expected 'My Alert Hook' webhook card")
	}
	if !strings.Contains(body, "Deploy Hook") {
		t.Error("expected 'Deploy Hook' webhook card")
	}

	activeCard := webhookCardSectionByName(body, "My Alert Hook")
	if activeCard == "" {
		t.Fatal("expected webhook card section for 'My Alert Hook'")
	}
	disabledCard := webhookCardSectionByName(body, "Deploy Hook")
	if disabledCard == "" {
		t.Fatal("expected webhook card section for 'Deploy Hook'")
	}

	if strings.Contains(activeCard, "Inbound webhook endpoint") || strings.Contains(disabledCard, "Inbound webhook endpoint") {
		t.Error("did not expect legacy inbound webhook endpoint text on webhook cards")
	}
	activeVisibleCard := activeCard
	if end := strings.Index(activeVisibleCard, ">"); end != -1 {
		activeVisibleCard = activeVisibleCard[end+1:]
	}
	disabledVisibleCard := disabledCard
	if end := strings.Index(disabledVisibleCard, ">"); end != -1 {
		disabledVisibleCard = disabledVisibleCard[end+1:]
	}
	if strings.Contains(activeVisibleCard, "/webhooks/inbound/") || strings.Contains(disabledVisibleCard, "/webhooks/inbound/") {
		t.Error("did not expect raw webhook endpoint URL text rendered on webhook cards")
	}
	if !strings.Contains(activeCard, ">Copy URL<") || !strings.Contains(disabledCard, ">Copy URL<") {
		t.Error("expected Copy URL button on webhook cards")
	}
	if !strings.Contains(activeCard, "badge badge-sm badge-success\">Active") {
		t.Error("expected webhook active badge to match shared channel badge style")
	}
	if strings.Contains(activeCard, "Active Inbound webhook endpoint") || strings.Contains(disabledCard, "Active Inbound webhook endpoint") {
		t.Error("did not expect legacy active webhook status row text")
	}

	if !strings.Contains(body, `id="webhook_title_template"`) {
		t.Error("expected webhook title template field in webhook modal")
	}
	if !strings.Contains(body, `id="webhook_prompt_template"`) {
		t.Error("expected webhook prompt template field in webhook modal")
	}
	if strings.Contains(body, "Agents (comma-separated IDs)") {
		t.Error("did not expect legacy webhook comma-separated agents input")
	}
	if strings.Contains(body, `Available: <code`) {
		t.Error("did not expect legacy available agents helper list in webhook modal")
	}
	if !strings.Contains(body, `data-webhook-section-tab="config"`) {
		t.Error("expected webhook config tab")
	}
	if !strings.Contains(body, `data-webhook-section-tab="agents"`) {
		t.Error("expected webhook agents tab")
	}
	if !strings.Contains(body, `data-webhook-section-panel="config"`) {
		t.Error("expected webhook config panel")
	}
	if !strings.Contains(body, `data-webhook-section-panel="agents"`) {
		t.Error("expected webhook agents panel")
	}
	if !strings.Contains(body, `id="webhook_agent_search_input"`) {
		t.Error("expected webhook agents search input")
	}
	if !strings.Contains(body, `id="webhook_agent_list"`) {
		t.Error("expected webhook agents list container")
	}
	if !strings.Contains(body, `id="webhook_agent_ids_hidden"`) {
		t.Error("expected hidden field for agent IDs")
	}
	if !strings.Contains(body, "copyWebhookEndpointUrl") {
		t.Error("expected webhook card copy action handler to be wired")
	}
	if !strings.Contains(body, "initializeWebhookAgents") {
		t.Error("expected webhook agent initialization function")
	}
	if !strings.Contains(body, "renderWebhookAgentList") {
		t.Error("expected webhook agent list rendering function")
	}
	if !strings.Contains(body, "setWebhookSection") {
		t.Error("expected webhook tab switching function")
	}
	// Check that agents are available in the JavaScript initialization (look for the agent names/IDs in the init function)
	if !strings.Contains(body, "webhookAvailableAgents") {
		t.Error("expected webhookAvailableAgents array initialization")
	}
	// The agents should be in the initializeWebhookAgents function as JSON
	// Look for agent1.ID or agent1.Name in JSON format
	agent1Found := strings.Contains(body, `"`+agent1.ID+`"`) || strings.Contains(body, agent1.Name)
	agent2Found := strings.Contains(body, `"`+agent2.ID+`"`) || strings.Contains(body, agent2.Name)
	if !agent1Found {
		t.Errorf("expected first agent (%s / %s) to be in webhook agents initialization", agent1.ID, agent1.Name)
	}
	if !agent2Found {
		t.Errorf("expected second agent (%s / %s) to be in webhook agents initialization", agent2.ID, agent2.Name)
	}
}

func webhookCardSectionByName(body, webhookName string) string {
	marker := `data-webhook-name="` + webhookName + `"`
	start := strings.Index(body, marker)
	if start == -1 {
		return ""
	}

	end := len(body)
	if next := strings.Index(body[start+len(marker):], `data-channel-type="`); next >= 0 {
		end = start + len(marker) + next
	}
	for _, boundaryMarker := range []string{`<dialog id="delete_channel_confirm_modal"`, `<!-- Channel Configuration Modal -->`, `<script>`} {
		if boundary := strings.Index(body[start:], boundaryMarker); boundary >= 0 && start+boundary < end {
			end = start + boundary
		}
	}
	if end <= start {
		return ""
	}
	return body[start:end]
}

func TestWebhookCreate_ShowsOnChannelsPage(t *testing.T) {
	// Regression: creating a webhook via form POST and then loading
	// the channels page with the same project_id must show the webhook.
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("ShowTest").Build()

	// Create webhook via HTMX form POST (include project_id in URL like the fixed form does)
	form := url.Values{
		"name": {"ShowMe Webhook"},
	}
	req := httptest.NewRequest("POST", "/channels/webhooks?project_id="+project.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("create: expected 200, got %d; body=%s", rec.Code, rec.Body.String())
	}
	assertChannelsRefreshTrigger(t, rec)

	// Simulate refreshed channels page for that project
	rec2 := wtc.HTMX().Get("/channels?project_id=" + project.ID).Execute()
	wtc.Assert(rec2).StatusCode(http.StatusOK)
	body := rec2.Body.String()
	if !strings.Contains(body, "ShowMe Webhook") {
		t.Error("expected 'ShowMe Webhook' to appear on channels page after creation")
	}
}

// TestWebhookCreate_WithoutProjectID verifies robust project resolution:
// if project_id is omitted from URL, form body project_id is accepted.
func TestWebhookCreate_WithoutProjectID(t *testing.T) {
	wtc := newWebhookTestContext(t)
	proj1 := wtc.CreateProject().WithName("Project Alpha").Build()

	// Create webhook WITHOUT project_id in URL, but WITH form body project_id
	// (matches browser modal hidden-field behavior).
	form := url.Values{
		"name":       {"Visible Webhook"},
		"project_id": {proj1.ID},
	}
	req := httptest.NewRequest("POST", "/channels/webhooks", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d; body=%s", rec.Code, rec.Body.String())
	}

	webhooksProj1, _ := wtc.webhookRepo.ListByProject(context.Background(), proj1.ID)
	if len(webhooksProj1) != 1 {
		t.Fatalf("expected 1 webhook for proj1, got %d", len(webhooksProj1))
	}
	if webhooksProj1[0].Name != "Visible Webhook" {
		t.Fatalf("unexpected webhook name: %s", webhooksProj1[0].Name)
	}
}

func TestWebhookInbound_HMACSha256Auth(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH HMAC").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "HMAC", true)

	payload := `{"event_type":"hmac_test"}`

	// Compute HMAC-SHA256 signature
	mac := hmac.New(sha256.New, []byte(endpoint.Secret))
	mac.Write([]byte(payload))
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	rec := wtc.jsonRequest("POST", "/webhooks/inbound/"+endpoint.PathToken, payload,
		map[string]string{"X-Hub-Signature-256": sig})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}
}
