package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestProjectRepo_DeleteWithCleanupManifestRollsBackBeforeDeleteFailure(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := &models.Project{Name: "Atomic project deletion"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tasks (id, project_id, title, prompt) VALUES ('atomic-project-task', ?, 'Atomic task', 'test')`, project.ID); err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected pre-delete failure")
	manifest, deleted, err := projectRepo.DeleteWithCleanupManifest(ctx, project.ID, func(manifest TaskDeletionManifest) error {
		if len(manifest.TaskIDs) != 1 || manifest.TaskIDs[0] != "atomic-project-task" {
			t.Fatalf("captured task IDs = %v", manifest.TaskIDs)
		}
		return injected
	})
	if !errors.Is(err, injected) || deleted {
		t.Fatalf("DeleteWithCleanupManifest = manifest=%+v deleted=%v err=%v", manifest, deleted, err)
	}
	var projectCount, taskCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, project.ID).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = 'atomic-project-task'`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if projectCount != 1 || taskCount != 1 {
		t.Fatalf("rollback retained project=%d task=%d, want 1/1", projectCount, taskCount)
	}

	manifest, deleted, err = projectRepo.DeleteWithCleanupManifest(ctx, project.ID, nil)
	if err != nil || !deleted || len(manifest.TaskIDs) != 1 {
		t.Fatalf("successful DeleteWithCleanupManifest = manifest=%+v deleted=%v err=%v", manifest, deleted, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM projects WHERE id = ?`, project.ID).Scan(&projectCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = 'atomic-project-task'`).Scan(&taskCount); err != nil {
		t.Fatal(err)
	}
	if projectCount != 0 || taskCount != 0 {
		t.Fatalf("successful deletion retained project=%d task=%d, want 0/0", projectCount, taskCount)
	}
}

func TestProjectRepo_ListRepoRootsUsesCompactUnorderedProjection(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	project := &models.Project{
		Name:        "Compact Root",
		Description: strings.Repeat("d", 16<<10),
		RepoPath:    "/repos/compact",
		RepoURL:     "https://example.test/" + strings.Repeat("u", 2<<10),
	}
	if err := repo.Create(ctx, project); err != nil {
		t.Fatalf("Create: %v", err)
	}

	counter.Reset()
	counter.SetEnabled(true)
	roots, err := repo.ListRepoRoots(ctx)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ListRepoRoots: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal("expected repository roots")
	}
	var found bool
	for _, root := range roots {
		if root.ID == project.ID {
			found = true
			if root.RepoPath != project.RepoPath {
				t.Fatalf("RepoPath = %q, want %q", root.RepoPath, project.RepoPath)
			}
		}
	}
	if !found {
		t.Fatalf("project %q not returned", project.ID)
	}
	full, err := repo.GetByID(ctx, project.ID)
	if err != nil || full == nil {
		t.Fatalf("GetByID: project=%v err=%v", full, err)
	}
	if full.Description != project.Description || full.RepoURL != project.RepoURL {
		t.Fatal("full project read did not retain rich metadata")
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %d, want 1: %v", len(statements), statements)
	}
	query := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if query != "select id, repo_path from projects" {
		t.Fatalf("unexpected compact query: %s", statements[0])
	}

	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+statements[0])
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	planText := strings.ToUpper(plan.String())
	if strings.Contains(planText, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("compact query uses temporary ORDER BY sort:\n%s", plan.String())
	}
	if !strings.Contains(planText, "COVERING INDEX IDX_PROJECTS_REPO_ROOTS") {
		t.Fatalf("compact query does not use repository-root covering index:\n%s", plan.String())
	}
}

func TestProjectRepo_CreateAndGetByID(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	p := &models.Project{
		Name:        "Test Project",
		Description: "A test project",
		RepoPath:    "/path/to/repo",
		RepoURL:     "https://github.example.com/acme/widgets",
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

func TestProjectRepo_MaxWorkersAllowsHighValuesAndRejectsNegative(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewProjectRepo(db)
	ctx := context.Background()

	high := 100
	project := &models.Project{Name: "High Limit", MaxWorkers: &high}
	if err := repo.Create(ctx, project); err != nil {
		t.Fatalf("Create high-limit project: %v", err)
	}
	got, err := repo.GetByID(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetByID high-limit project: %v", err)
	}
	if got.MaxWorkers == nil || *got.MaxWorkers != high {
		t.Fatalf("high project MaxWorkers = %v, want %d", got.MaxWorkers, high)
	}

	negative := -1
	project.MaxWorkers = &negative
	if err := repo.Update(ctx, project); err == nil {
		t.Fatal("Update with negative project limit succeeded")
	}
	got, err = repo.GetByID(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetByID after rejected update: %v", err)
	}
	if got.MaxWorkers == nil || *got.MaxWorkers != high {
		t.Fatalf("rejected negative update changed MaxWorkers to %v", got.MaxWorkers)
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
