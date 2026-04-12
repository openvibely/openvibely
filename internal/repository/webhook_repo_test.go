package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func createWebhookTestProject(t *testing.T, projectRepo *ProjectRepo) *models.Project {
	t.Helper()
	p := &models.Project{Name: "webhook-test-project"}
	if err := projectRepo.Create(context.Background(), p); err != nil {
		t.Fatalf("creating test project: %v", err)
	}
	return p
}

func createWebhookTestAgent(t *testing.T, agentRepo *AgentRepo, name string) *models.Agent {
	t.Helper()
	a := &models.Agent{Name: name, SystemPrompt: "test"}
	if err := agentRepo.Create(context.Background(), a); err != nil {
		t.Fatalf("creating test agent: %v", err)
	}
	return a
}

func TestWebhookRepo_CreateAndGet(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	w := &models.WebhookEndpoint{
		ProjectID:       project.ID,
		Name:            "PagerDuty Alerts",
		Enabled:         true,
		DefaultPriority: 1,
	}
	err := repo.Create(context.Background(), w)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if w.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if w.PathToken == "" {
		t.Fatal("expected auto-generated path token")
	}
	if w.Secret == "" {
		t.Fatal("expected auto-generated secret")
	}

	// Get by ID
	got, err := repo.GetByID(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil webhook")
	}
	if got.Name != "PagerDuty Alerts" {
		t.Errorf("Name = %q, want %q", got.Name, "PagerDuty Alerts")
	}
	if !got.Enabled {
		t.Error("expected Enabled = true")
	}
	if got.DefaultPriority != 1 {
		t.Errorf("DefaultPriority = %d, want 1", got.DefaultPriority)
	}

	// Get by path token
	byToken, err := repo.GetByPathToken(context.Background(), w.PathToken)
	if err != nil {
		t.Fatalf("GetByPathToken: %v", err)
	}
	if byToken == nil || byToken.ID != w.ID {
		t.Error("GetByPathToken returned wrong webhook")
	}
}

func TestWebhookRepo_ListByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	// Create two webhooks
	for _, name := range []string{"Webhook A", "Webhook B"} {
		w := &models.WebhookEndpoint{
			ProjectID: project.ID,
			Name:      name,
			Enabled:   true,
		}
		if err := repo.Create(context.Background(), w); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	list, err := repo.ListByProject(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("ListByProject: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 webhooks, got %d", len(list))
	}
	// Sorted by name ASC
	if list[0].Name != "Webhook A" {
		t.Errorf("first webhook name = %q, want Webhook A", list[0].Name)
	}
	if list[1].Name != "Webhook B" {
		t.Errorf("second webhook name = %q, want Webhook B", list[1].Name)
	}
}

func TestWebhookRepo_Update(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	w := &models.WebhookEndpoint{
		ProjectID: project.ID,
		Name:      "Original",
		Enabled:   true,
	}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	w.Name = "Updated"
	w.Enabled = false
	w.DefaultPriority = 3
	if err := repo.Update(context.Background(), w); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := repo.GetByID(context.Background(), w.ID)
	if got.Name != "Updated" {
		t.Errorf("Name = %q, want Updated", got.Name)
	}
	if got.Enabled {
		t.Error("expected Enabled = false")
	}
	if got.DefaultPriority != 3 {
		t.Errorf("DefaultPriority = %d, want 3", got.DefaultPriority)
	}
}

func TestWebhookRepo_Delete(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	w := &models.WebhookEndpoint{
		ProjectID: project.ID,
		Name:      "ToDelete",
		Enabled:   true,
	}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(context.Background(), w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, _ := repo.GetByID(context.Background(), w.ID)
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestWebhookRepo_RotateSecret(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	w := &models.WebhookEndpoint{
		ProjectID: project.ID,
		Name:      "SecretTest",
		Enabled:   true,
	}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	originalSecret := w.Secret

	newSecret, err := repo.RotateSecret(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("RotateSecret: %v", err)
	}
	if newSecret == "" {
		t.Fatal("expected non-empty new secret")
	}
	if newSecret == originalSecret {
		t.Error("expected different secret after rotation")
	}

	got, _ := repo.GetByID(context.Background(), w.ID)
	if got.Secret != newSecret {
		t.Errorf("stored secret = %q, want %q", got.Secret, newSecret)
	}
}

func TestWebhookRepo_UniquePathToken(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	// Create two webhooks and verify they have different path tokens
	w1 := &models.WebhookEndpoint{ProjectID: project.ID, Name: "One", Enabled: true}
	w2 := &models.WebhookEndpoint{ProjectID: project.ID, Name: "Two", Enabled: true}
	if err := repo.Create(context.Background(), w1); err != nil {
		t.Fatalf("Create w1: %v", err)
	}
	if err := repo.Create(context.Background(), w2); err != nil {
		t.Fatalf("Create w2: %v", err)
	}
	if w1.PathToken == w2.PathToken {
		t.Error("expected unique path tokens")
	}
}

func TestWebhookRepo_EndpointAgents(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewAgentRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	agent1 := createWebhookTestAgent(t, agentRepo, "Agent 1")
	agent2 := createWebhookTestAgent(t, agentRepo, "Agent 2")

	w := &models.WebhookEndpoint{ProjectID: project.ID, Name: "AgentTest", Enabled: true}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Set agents in order
	if err := repo.SetEndpointAgents(context.Background(), w.ID, []string{agent2.ID, agent1.ID}); err != nil {
		t.Fatalf("SetEndpointAgents: %v", err)
	}

	agents, err := repo.GetEndpointAgents(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("GetEndpointAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(agents))
	}
	if agents[0].AgentDefinitionID != agent2.ID {
		t.Errorf("first agent = %q, want %q", agents[0].AgentDefinitionID, agent2.ID)
	}
	if agents[0].Position != 0 {
		t.Errorf("first position = %d, want 0", agents[0].Position)
	}
	if agents[1].AgentDefinitionID != agent1.ID {
		t.Errorf("second agent = %q, want %q", agents[1].AgentDefinitionID, agent1.ID)
	}
	if agents[1].Position != 1 {
		t.Errorf("second position = %d, want 1", agents[1].Position)
	}

	// Replace agents
	if err := repo.SetEndpointAgents(context.Background(), w.ID, []string{agent1.ID}); err != nil {
		t.Fatalf("SetEndpointAgents replace: %v", err)
	}
	agents, _ = repo.GetEndpointAgents(context.Background(), w.ID)
	if len(agents) != 1 {
		t.Fatalf("expected 1 agent after replace, got %d", len(agents))
	}
}

func TestWebhookRepo_TaskAgentAssignments(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewAgentRepo(db)
	taskRepo := NewTaskRepo(db, nil)
	project := createWebhookTestProject(t, projectRepo)

	agent1 := createWebhookTestAgent(t, agentRepo, "Agent A")
	agent2 := createWebhookTestAgent(t, agentRepo, "Agent B")

	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Webhook Task",
		Category:  models.CategoryActive,
		Status:    models.StatusPending,
		Prompt:    "test",
	}
	if err := taskRepo.Create(context.Background(), task); err != nil {
		t.Fatalf("Create task: %v", err)
	}

	if err := repo.SetTaskAgentAssignments(context.Background(), task.ID, []string{agent1.ID, agent2.ID}); err != nil {
		t.Fatalf("SetTaskAgentAssignments: %v", err)
	}

	assignments, err := repo.GetTaskAgentAssignments(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("GetTaskAgentAssignments: %v", err)
	}
	if len(assignments) != 2 {
		t.Fatalf("expected 2 assignments, got %d", len(assignments))
	}
	if assignments[0].AgentDefinitionID != agent1.ID {
		t.Errorf("first assignment agent = %q, want %q", assignments[0].AgentDefinitionID, agent1.ID)
	}
	if assignments[1].AgentDefinitionID != agent2.ID {
		t.Errorf("second assignment agent = %q, want %q", assignments[1].AgentDefinitionID, agent2.ID)
	}
}

func TestWebhookRepo_GetByPathToken_NotFound(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)

	got, err := repo.GetByPathToken(context.Background(), "nonexistent")
	if err != nil {
		t.Fatalf("GetByPathToken: %v", err)
	}
	if got != nil {
		t.Error("expected nil for nonexistent token")
	}
}

func TestWebhookRepo_CascadeDeleteEndpointAgents(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewWebhookRepo(db)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewAgentRepo(db)
	project := createWebhookTestProject(t, projectRepo)

	agent := createWebhookTestAgent(t, agentRepo, "CascadeAgent")

	w := &models.WebhookEndpoint{ProjectID: project.ID, Name: "CascadeTest", Enabled: true}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.SetEndpointAgents(context.Background(), w.ID, []string{agent.ID}); err != nil {
		t.Fatalf("SetEndpointAgents: %v", err)
	}

	// Delete endpoint should cascade to agents
	if err := repo.Delete(context.Background(), w.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	agents, _ := repo.GetEndpointAgents(context.Background(), w.ID)
	if len(agents) != 0 {
		t.Errorf("expected 0 agents after cascade delete, got %d", len(agents))
	}
}
