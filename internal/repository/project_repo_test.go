package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProjectRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	p := &models.Project{
		Name:        "Test Project",
		Description: "A test project",
		RepoPath:    "/path/to/repo",
	}

	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected ID to be set after Create")
	}

	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected project, got nil")
	}
	if got.Name != "Test Project" {
		t.Errorf("expected Name=Test Project, got %q", got.Name)
	}
	if got.DefaultAgentConfigID != nil {
		t.Errorf("expected DefaultAgentConfigID=nil, got %v", got.DefaultAgentConfigID)
	}
}

func TestProjectRepo_CreateWithDefaultAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create an agent
	agent := &models.LLMConfig{
		Name:     "Test Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	if err := agentRepo.Create(ctx, agent); err != nil {
		t.Fatalf("Create agent: %v", err)
	}

	// Create project with default agent
	p := &models.Project{
		Name:                 "Project With Agent",
		Description:          "Has a default agent",
		DefaultAgentConfigID: &agent.ID,
	}
	if err := projectRepo.Create(ctx, p); err != nil {
		t.Fatalf("Create project: %v", err)
	}

	got, err := projectRepo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.DefaultAgentConfigID == nil {
		t.Fatal("expected DefaultAgentConfigID to be set")
	}
	if *got.DefaultAgentConfigID != agent.ID {
		t.Errorf("expected DefaultAgentConfigID=%s, got %s", agent.ID, *got.DefaultAgentConfigID)
	}
}

func TestProjectRepo_UpdateDefaultAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create two agents
	agent1 := &models.LLMConfig{
		Name:     "Agent 1",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	agent2 := &models.LLMConfig{
		Name:     "Agent 2",
		Provider: models.ProviderAnthropic,
		Model:    "claude-haiku-4-5-20251001",
		APIKey:   "sk-test",
	}
	agentRepo.Create(ctx, agent1)
	agentRepo.Create(ctx, agent2)

	// Create project without default agent
	p := &models.Project{
		Name: "Test Update",
	}
	projectRepo.Create(ctx, p)

	// Update to set default agent
	p.DefaultAgentConfigID = &agent1.ID
	if err := projectRepo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := projectRepo.GetByID(ctx, p.ID)
	if got.DefaultAgentConfigID == nil || *got.DefaultAgentConfigID != agent1.ID {
		t.Errorf("expected DefaultAgentConfigID=%s, got %v", agent1.ID, got.DefaultAgentConfigID)
	}

	// Update to change default agent
	p.DefaultAgentConfigID = &agent2.ID
	if err := projectRepo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ = projectRepo.GetByID(ctx, p.ID)
	if got.DefaultAgentConfigID == nil || *got.DefaultAgentConfigID != agent2.ID {
		t.Errorf("expected DefaultAgentConfigID=%s, got %v", agent2.ID, got.DefaultAgentConfigID)
	}

	// Update to clear default agent
	p.DefaultAgentConfigID = nil
	if err := projectRepo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ = projectRepo.GetByID(ctx, p.ID)
	if got.DefaultAgentConfigID != nil {
		t.Errorf("expected DefaultAgentConfigID=nil, got %v", got.DefaultAgentConfigID)
	}
}

func TestProjectRepo_List_IncludesDefaultAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:     "Test Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	agentRepo.Create(ctx, agent)

	// Create projects: one with default agent, one without
	p1 := &models.Project{
		Name:                 "With Agent",
		DefaultAgentConfigID: &agent.ID,
	}
	p2 := &models.Project{
		Name: "Without Agent",
	}
	projectRepo.Create(ctx, p1)
	projectRepo.Create(ctx, p2)

	projects, err := projectRepo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Find our created projects (seeded default project may also exist)
	var withAgent, withoutAgent *models.Project
	for i := range projects {
		switch projects[i].Name {
		case "With Agent":
			withAgent = &projects[i]
		case "Without Agent":
			withoutAgent = &projects[i]
		}
	}

	if withAgent == nil {
		t.Fatal("expected 'With Agent' project in list")
	}
	if withAgent.DefaultAgentConfigID == nil || *withAgent.DefaultAgentConfigID != agent.ID {
		t.Errorf("expected DefaultAgentConfigID=%s for 'With Agent', got %v", agent.ID, withAgent.DefaultAgentConfigID)
	}

	if withoutAgent == nil {
		t.Fatal("expected 'Without Agent' project in list")
	}
	if withoutAgent.DefaultAgentConfigID != nil {
		t.Errorf("expected DefaultAgentConfigID=nil for 'Without Agent', got %v", withoutAgent.DefaultAgentConfigID)
	}
}

func TestProjectRepo_CreatePreservesRepoPath(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	specifiedPath := "/Users/testuser/go/src/github.com/myorg/my-project"
	p := &models.Project{
		Name:        "Path Test Project",
		Description: "Testing repo_path preservation",
		RepoPath:    specifiedPath,
	}

	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected ID to be set after Create")
	}
	// Verify repo_path is preserved in the returned struct
	if p.RepoPath != specifiedPath {
		t.Errorf("expected RepoPath=%q after Create, got %q", specifiedPath, p.RepoPath)
	}

	// Verify repo_path is correctly stored in the database
	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.RepoPath != specifiedPath {
		t.Errorf("expected RepoPath=%q from DB, got %q", specifiedPath, got.RepoPath)
	}
}

func TestProjectRepo_MaxWorkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	// Create project without max_workers (should be nil)
	p := &models.Project{
		Name: "No Limit Project",
	}
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.MaxWorkers != nil {
		t.Errorf("expected MaxWorkers=nil, got %v", got.MaxWorkers)
	}

	// Create project with max_workers set
	maxW := 3
	p2 := &models.Project{
		Name:       "Limited Project",
		MaxWorkers: &maxW,
	}
	if err := repo.Create(ctx, p2); err != nil {
		t.Fatalf("Create limited: %v", err)
	}

	got2, err := repo.GetByID(ctx, p2.ID)
	if err != nil {
		t.Fatalf("GetByID limited: %v", err)
	}
	if got2.MaxWorkers == nil {
		t.Fatal("expected MaxWorkers to be set")
	}
	if *got2.MaxWorkers != 3 {
		t.Errorf("expected MaxWorkers=3, got %d", *got2.MaxWorkers)
	}

	// Update max_workers
	newMax := 5
	p.MaxWorkers = &newMax
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ = repo.GetByID(ctx, p.ID)
	if got.MaxWorkers == nil || *got.MaxWorkers != 5 {
		t.Errorf("expected MaxWorkers=5, got %v", got.MaxWorkers)
	}

	// Clear max_workers
	p.MaxWorkers = nil
	if err := repo.Update(ctx, p); err != nil {
		t.Fatalf("Update clear: %v", err)
	}

	got, _ = repo.GetByID(ctx, p.ID)
	if got.MaxWorkers != nil {
		t.Errorf("expected MaxWorkers=nil after clear, got %v", got.MaxWorkers)
	}
}

func TestProjectRepo_List_IncludesMaxWorkers(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	maxW := 2
	p1 := &models.Project{
		Name:       "Limited",
		MaxWorkers: &maxW,
	}
	p2 := &models.Project{
		Name: "Unlimited",
	}
	repo.Create(ctx, p1)
	repo.Create(ctx, p2)

	projects, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	var limited, unlimited *models.Project
	for i := range projects {
		switch projects[i].Name {
		case "Limited":
			limited = &projects[i]
		case "Unlimited":
			unlimited = &projects[i]
		}
	}

	if limited == nil {
		t.Fatal("expected 'Limited' project in list")
	}
	if limited.MaxWorkers == nil || *limited.MaxWorkers != 2 {
		t.Errorf("expected MaxWorkers=2 for 'Limited', got %v", limited.MaxWorkers)
	}

	if unlimited == nil {
		t.Fatal("expected 'Unlimited' project in list")
	}
	if unlimited.MaxWorkers != nil {
		t.Errorf("expected MaxWorkers=nil for 'Unlimited', got %v", unlimited.MaxWorkers)
	}
}

func TestProjectRepo_DefaultAgentFKConstraint(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := NewProjectRepo(db)
	agentRepo := NewLLMConfigRepo(db)
	ctx := context.Background()

	// Create an agent and a project using it as default
	agent := &models.LLMConfig{
		Name:     "FK Test Agent",
		Provider: models.ProviderAnthropic,
		Model:    "claude-sonnet-4-5-20250929",
	}
	agentRepo.Create(ctx, agent)

	p := &models.Project{
		Name:                 "FK Test Project",
		DefaultAgentConfigID: &agent.ID,
	}
	projectRepo.Create(ctx, p)

	// Delete the agent - ON DELETE SET NULL should clear the FK
	agentRepo.Delete(ctx, agent.ID)

	got, err := projectRepo.GetByID(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetByID after agent delete: %v", err)
	}
	if got.DefaultAgentConfigID != nil {
		t.Errorf("expected DefaultAgentConfigID=nil after agent deleted (ON DELETE SET NULL), got %v", got.DefaultAgentConfigID)
	}
}
