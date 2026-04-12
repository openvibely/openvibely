package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
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
	a := &models.Agent{Name: name, SystemPrompt: "test agent"}
	if err := wtc.agentRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	return a
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
	agent2 := wtc.createAgent(t, "Second")

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

func TestWebhookCRUD_CreateViaForm(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH CRUD").Build()
	agent1 := wtc.createAgent(t, "Agent One")
	agent2 := wtc.createAgent(t, "Agent Two")

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
	assigned, err := wtc.webhookRepo.GetEndpointAgents(context.Background(), webhooks[0].ID)
	if err != nil {
		t.Fatalf("GetEndpointAgents: %v", err)
	}
	if len(assigned) != 2 {
		t.Fatalf("expected 2 assigned agents, got %d", len(assigned))
	}
	if assigned[0].AgentDefinitionID != agent1.ID || assigned[1].AgentDefinitionID != agent2.ID {
		t.Fatalf("unexpected agent assignment order: %#v", assigned)
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

func TestWebhookCRUD_Test(t *testing.T) {
	wtc := newWebhookTestContext(t)
	project := wtc.CreateProject().WithName("WH Test").Build()
	endpoint := wtc.createEndpoint(t, project.ID, "TestEndpoint", true)

	req := httptest.NewRequest("POST", "/channels/webhooks/"+endpoint.ID+"/test", nil)
	rec := httptest.NewRecorder()
	wtc.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d; body=%s", rec.Code, rec.Body.String())
	}

	// Should have created exactly one task
	tasks, _ := wtc.taskRepo.ListByProject(context.Background(), project.ID, "")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task from test, got %d", len(tasks))
	}
	if tasks[0].CreatedVia != models.TaskOriginWebhook {
		t.Errorf("expected created_via=webhook, got %q", tasks[0].CreatedVia)
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
	if strings.Contains(activeCard, "/webhooks/inbound/") || strings.Contains(disabledCard, "/webhooks/inbound/") {
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

	if strings.Contains(body, "Title Template") {
		t.Error("did not expect webhook title template field in webhook modal")
	}
	if strings.Contains(body, "Prompt Template") {
		t.Error("did not expect webhook prompt template field in webhook modal")
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
	if next := strings.Index(body[start+len(marker):], `data-webhook-name="`); next >= 0 {
		end = start + len(marker) + next
	}
	if sectionBoundary := strings.Index(body[start:], "<!-- Coming Soon Section -->"); sectionBoundary >= 0 {
		boundary := start + sectionBoundary
		if boundary < end {
			end = boundary
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
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Error("expected HX-Refresh: true header")
	}

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
