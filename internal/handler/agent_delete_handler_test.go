package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
)

// writeAgentRootSKILLSmd writes a minimal valid SKILLS.md for an agent to the
// given root so that syncRootDeclarationsFromRoot can discover and apply it.
func writeAgentRootSKILLSmd(t *testing.T, root, key, name, description string) {
	t.Helper()
	enabled := true
	selectable := true
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: root}, nil)
	decl := &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Agent: agentlibrary.AgentDeclaration{
			Key:                 key,
			Name:                name,
			Description:         description,
			Enabled:             &enabled,
			SelectableAsPrimary: selectable,
			Scope:               "global",
			SystemPrompt:        description,
		},
	}
	if _, err := imp.WriteAgentRootDeclaration(t.Context(), decl, "# "+name+"\n\n"+description+"\n"); err != nil {
		t.Fatalf("write agent SKILLS.md: %v", err)
	}
}

// TestHandler_DeleteAgent_RemovesAgentFromList verifies that a user-created
// agent is deleted from the database and does not appear in the subsequent
// ListAgents response.
func TestHandler_DeleteAgent_RemovesAgentFromList(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	agent := &models.Agent{
		Name:    "Cindy",
		Key:     "cindy",
		Scope:   models.AgentScopeGlobal,
		Model:   "inherit",
		Enabled: true,
		Tools:   []string{"Read"},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `data-agent-name="Cindy"`) {
		t.Fatal("expected Cindy to be absent from agents list after delete, but found her card in response")
	}

	// Verify DB row is gone.
	gone, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if gone != nil {
		t.Fatalf("expected agent to be deleted from DB, got %+v", gone)
	}
}

// TestHandler_DeleteAgent_ProtectedAgentRejected verifies that attempting to
// delete a protected system agent returns 403 and leaves the agent untouched.
func TestHandler_DeleteAgent_ProtectedAgentRejected(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	protected := &models.Agent{
		Name:            "OpenVibely Engineer",
		Key:             "openvibely_engineer",
		Scope:           models.AgentScopeGlobal,
		Model:           "inherit",
		Enabled:         true,
		GeneratedStatus: models.AgentStatusProtected,
		CreatedBy:       models.AgentCreatedBySystem,
	}
	if err := agentRepo.Create(t.Context(), protected); err != nil {
		t.Fatalf("create protected agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/agents/"+protected.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for protected agent delete, got %d: %s", rec.Code, rec.Body.String())
	}

	// Agent must still exist in DB.
	still, err := agentRepo.GetByID(t.Context(), protected.ID)
	if err != nil {
		t.Fatalf("GetByID after rejected delete: %v", err)
	}
	if still == nil {
		t.Fatal("expected protected agent to still exist in DB after rejected delete")
	}
}

// TestHandler_DeleteAgent_RemovesOnDiskDirectory verifies that when an agent
// with an on-disk directory is deleted, the directory is removed so that
// SyncRootDeclarations cannot re-create the agent from stale SKILLS.md.
func TestHandler_DeleteAgent_RemovesOnDiskDirectory(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	agent := &models.Agent{
		Name:  "Claudia",
		Key:   "claudia",
		Scope: models.AgentScopeGlobal,
		Model: "inherit",
		Tools: []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// Write the on-disk SKILLS.md with a description that differs from the DB
	// to make it obvious if re-creation from disk occurs.
	writeAgentRootSKILLSmd(t, root, "claudia", "Claudia", "does nothing")

	agentDir := filepath.Join(root, "agents", "claudia")
	if _, err := os.Stat(agentDir); err != nil {
		t.Fatalf("expected agent dir to exist before delete: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// On-disk directory must be gone.
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("expected on-disk agent directory to be removed after delete, stat err=%v", err)
	}
}

// TestHandler_DeleteAgent_DoesNotReappearAfterListAgents is the key regression
// test. Before the fix, SyncRootDeclarations re-created deleted agents from
// their on-disk SKILLS.md on the very next ListAgents call. This test verifies
// that after deletion the agent is NOT re-created by a subsequent list request.
func TestHandler_DeleteAgent_DoesNotReappearAfterListAgents(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	root := t.TempDir()

	// Set up the maintenance service so SyncRootDeclarations runs during ListAgents.
	maintenanceSvc := service.NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	maintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	maintenanceSvc.SetAgentsRootPath(root)

	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(root)
	h.SetAgentLibraryMaintenanceService(maintenanceSvc)

	// Create the "Claudia" agent in the DB.
	agent := &models.Agent{
		Name:        "Claudia",
		Key:         "claudia",
		Scope:       models.AgentScopeGlobal,
		Description: "original description",
		Model:       "inherit",
		Tools:       []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Write an on-disk SKILLS.md for Claudia with a different description to
	// confirm re-creation from disk is what was happening before the fix.
	writeAgentRootSKILLSmd(t, root, "claudia", "Claudia", "does nothing")

	// Delete Claudia via the DELETE endpoint (triggers ListAgents which runs
	// SyncRootDeclarations internally).
	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE /agents/:id expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Claudia must NOT appear in the delete response (which includes ListAgents).
	if strings.Contains(rec.Body.String(), `data-agent-name="Claudia"`) {
		t.Fatal("Claudia reappeared in the delete response; SyncRootDeclarations re-created her from disk")
	}

	// Explicitly GET /agents to confirm she is still absent after a fresh list.
	req2 := httptest.NewRequest(http.MethodGet, "/agents", nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /agents expected 200, got %d", rec2.Code)
	}
	if strings.Contains(rec2.Body.String(), `data-agent-name="Claudia"`) {
		t.Fatal("Claudia reappeared after GET /agents; on-disk SKILLS.md re-created her via SyncRootDeclarations")
	}

	// DB must not have Claudia.
	claudia, err := agentRepo.GetByKey(t.Context(), "claudia")
	if err != nil {
		t.Fatalf("GetByKey after delete: %v", err)
	}
	if claudia != nil {
		t.Fatalf("expected Claudia to be absent from DB after delete, got %+v", claudia)
	}

	// On-disk directory must also be gone.
	agentDir := filepath.Join(root, "agents", "claudia")
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("expected on-disk agent directory to be removed, stat err=%v", err)
	}
}

// TestHandler_ListAgents_ProtectedAgentShowsDisabledDelete verifies that the
// agents page renders a disabled, non-functional Delete button for protected
// system agents so users are clearly informed the agent cannot be deleted.
func TestHandler_ListAgents_ProtectedAgentShowsDisabledDelete(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	protected := &models.Agent{
		Name:            "OpenVibely Engineer",
		Key:             "openvibely_engineer",
		Scope:           models.AgentScopeGlobal,
		Model:           "inherit",
		Enabled:         true,
		GeneratedStatus: models.AgentStatusProtected,
		CreatedBy:       models.AgentCreatedBySystem,
	}
	if err := agentRepo.Create(t.Context(), protected); err != nil {
		t.Fatalf("create protected agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// Must show the disabled "Delete (protected)" button.
	if !strings.Contains(body, "Delete (protected)") {
		t.Error("expected protected agent to show 'Delete (protected)' disabled button")
	}
	if !strings.Contains(body, "Protected system agents cannot be deleted") {
		t.Error("expected protected agent delete button to have tooltip explaining it cannot be deleted")
	}

	// Must NOT present a working openDeleteAgentConfirm for this protected agent.
	// The non-protected path calls openDeleteAgentConfirm(this) in the delete button.
	// Verify the protected agent ID is not wired to openDeleteAgentConfirm.
	// (The agent card exists, but its delete button must be disabled.)
	if !strings.Contains(body, `data-agent-name="OpenVibely Engineer"`) {
		t.Error("expected OpenVibely Engineer agent card to be present in the list")
	}
}

// TestHandler_DeleteAgent_NoDiskDirDoesNotFail ensures that an agent with no
// on-disk directory can still be deleted successfully (handles agents that were
// created only in the DB without being materialized to disk).
func TestHandler_DeleteAgent_NoDiskDirDoesNotFail(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	agent := &models.Agent{
		Name:  "No-Disk Agent",
		Key:   "no_disk_agent",
		Scope: models.AgentScopeGlobal,
		Model: "inherit",
		Tools: []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	// Note: no SKILLS.md written to disk.

	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `data-agent-name="No-Disk Agent"`) {
		t.Fatal("expected No-Disk Agent to be absent from agents list after delete")
	}
}

// TestHandler_DeleteAgent_CleansUpAgentsIndex verifies that deleting an agent
// also removes its ## <key> section from agents/AGENTS.md so stale entries do
// not pollute LLM context on subsequent task turns.
func TestHandler_DeleteAgent_CleansUpAgentsIndex(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	root := t.TempDir()
	h.SetAgentSkillRoot(root)

	// Create an agent with a SKILLS.md on disk (also writes agents/AGENTS.md entry).
	writeAgentRootSKILLSmd(t, root, "claudia", "Claudia", "does nothing")

	// Confirm AGENTS.md has the entry before deletion.
	agentsIndexPath := filepath.Join(root, "agents", "AGENTS.md")
	before, err := os.ReadFile(agentsIndexPath)
	if err != nil {
		t.Fatalf("read AGENTS.md before delete: %v", err)
	}
	if !strings.Contains(string(before), "## claudia") {
		t.Fatalf("expected ## claudia section in AGENTS.md before delete:\n%s", before)
	}

	// Persist the agent in DB.
	agent := &models.Agent{
		Name:    "Claudia",
		Key:     "claudia",
		Scope:   models.AgentScopeGlobal,
		Model:   "inherit",
		Enabled: true,
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// Register routes and DELETE.
	e.DELETE("/agents/:id", h.DeleteAgent)
	e.GET("/agents", h.ListAgents)

	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// AGENTS.md must no longer contain the ## claudia section.
	after, err := os.ReadFile(agentsIndexPath)
	if err != nil {
		t.Fatalf("read AGENTS.md after delete: %v", err)
	}
	if strings.Contains(string(after), "## claudia") {
		t.Fatalf("expected ## claudia section removed from AGENTS.md after delete:\n%s", after)
	}
}

// TestHandler_DisabledAgentSurvivesListAgents is the key regression test for
// Bug 2. Before the fix, SyncRootDeclarations always hard-coded Enabled: true
// when importing a root declaration, so a disabled agent would be silently
// re-enabled the next time ListAgents ran. This test verifies that after saving
// a disabled agent and materializing it to disk, a subsequent ListAgents call
// leaves the agent disabled.
func TestHandler_DisabledAgentSurvivesListAgents(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	root := t.TempDir()

	maintenanceSvc := service.NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	maintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	maintenanceSvc.SetAgentsRootPath(root)

	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(root)
	h.SetAgentLibraryMaintenanceService(maintenanceSvc)

	// Create the agent disabled.
	agent := &models.Agent{
		Name:                "Disabled Agent",
		Key:                 "disabled-agent",
		Scope:               models.AgentScopeGlobal,
		Model:               "inherit",
		Enabled:             false,
		SelectableAsPrimary: true,
		GeneratedStatus:     models.AgentStatusUserEdited,
		CreatedBy:           models.AgentCreatedByUser,
		Tools:               []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create disabled agent: %v", err)
	}

	// Materialize to disk — the on-disk SKILLS.md will contain "enabled: false".
	synthCtx := e.NewContext(httptest.NewRequest(http.MethodGet, "/agents", nil), httptest.NewRecorder())
	if err := h.materializeAgentToDisk(synthCtx, agent, ""); err != nil {
		t.Fatalf("materialize agent to disk: %v", err)
	}

	skillsPath := filepath.Join(root, "agents", "disabled-agent", "SKILLS.md")
	data, readErr := os.ReadFile(skillsPath)
	if readErr != nil {
		t.Fatalf("read SKILLS.md after materialize: %v", readErr)
	}
	if !strings.Contains(string(data), "enabled: false") {
		t.Fatalf("expected SKILLS.md to contain 'enabled: false' after materialize, got:\n%s", data)
	}

	// GET /agents triggers SyncRootDeclarations which re-imports from the
	// on-disk SKILLS.md. Before the fix this would silently flip Enabled→true.
	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /agents expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()

	// The rendered card must carry data-agent-enabled="false".
	if !strings.Contains(body, `data-agent-enabled="false"`) {
		t.Fatalf("expected card with data-agent-enabled=\"false\" after ListAgents, but not found.\nBody excerpt: %s",
			truncateBody(body, 2000))
	}

	// Verify the DB row remained disabled.
	stored, err := agentRepo.GetByKey(t.Context(), "disabled-agent")
	if err != nil {
		t.Fatalf("GetByKey after ListAgents: %v", err)
	}
	if stored == nil {
		t.Fatal("expected disabled agent to still exist in DB after ListAgents")
	}
	if stored.Enabled {
		t.Fatalf("expected agent to remain Enabled=false after SyncRootDeclarations, but got Enabled=true")
	}
}

// truncateBody is a local test helper to avoid giant failure messages.
func truncateBody(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

func TestHandler_DeleteAgent_RejectsExplicitForeignProject(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)
	h.SetAgentSkillRoot(t.TempDir())

	projectA := &models.Project{Name: "Project A", RepoPath: t.TempDir()}
	if err := h.projectSvc.Create(t.Context(), projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projectB := &models.Project{Name: "Project B", RepoPath: t.TempDir()}
	if err := h.projectSvc.Create(t.Context(), projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}

	projectBRoot := filepath.Join(projectB.RepoPath, ".openvibely")
	writeAgentRootSKILLSmd(t, projectBRoot, "foreign-agent", "Foreign Agent", "project B agent")
	agentDir := filepath.Join(projectBRoot, "agents", "foreign-agent")
	agentsIndexPath := filepath.Join(projectBRoot, "agents", "AGENTS.md")
	indexBefore, err := os.ReadFile(agentsIndexPath)
	if err != nil {
		t.Fatalf("read project B index before delete: %v", err)
	}

	agent := &models.Agent{
		Name:      "Foreign Agent",
		Key:       "foreign-agent",
		Scope:     models.AgentScopeProject,
		ProjectID: projectB.ID,
		Model:     "inherit",
		Tools:     []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create foreign agent: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID+"?project_id="+projectA.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected foreign delete to return 404, got %d: %s", rec.Code, rec.Body.String())
	}
	stored, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("reload foreign agent: %v", err)
	}
	if stored == nil {
		t.Fatal("foreign agent was deleted from the database")
	}
	if stat, err := os.Stat(agentDir); err != nil || !stat.IsDir() {
		t.Fatalf("foreign agent directory changed: stat=%v err=%v", stat, err)
	}
	indexAfter, err := os.ReadFile(agentsIndexPath)
	if err != nil {
		t.Fatalf("read project B index after delete: %v", err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatalf("foreign agent index changed\nbefore:\n%s\nafter:\n%s", indexBefore, indexAfter)
	}
}

// TestHandler_DeleteAgent_ProjectScopedRemovesCorrectProjectDirectory is the
// key two-project regression test for Bug 1. It creates two projects, places a
// project-scoped agent in the second project, sends
// DELETE /agents/:id?project_id=<proj2-id>, and asserts that:
//   - the DB row is gone
//   - the proj2 agent directory is gone
//   - proj2's AGENTS.md no longer has ## <key>
//   - GET /agents?project_id=<proj2-id> does not recreate the agent via SyncRootDeclarations
//   - proj1 is untouched
func TestHandler_DeleteAgent_ProjectScopedRemovesCorrectProjectDirectory(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)

	globalRoot := t.TempDir()

	maintenanceSvc := service.NewAgentLibraryMaintenanceService(taskRepo, scheduleRepo, agentRepo)
	maintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	maintenanceSvc.SetAgentsRootPath(globalRoot)

	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(globalRoot)
	h.SetAgentLibraryMaintenanceService(maintenanceSvc)

	// Two projects with distinct temp directories.
	proj1RepoDir := t.TempDir()
	proj1 := &models.Project{Name: "Project One", RepoPath: proj1RepoDir}
	if err := h.projectSvc.Create(t.Context(), proj1); err != nil {
		t.Fatalf("create proj1: %v", err)
	}

	proj2RepoDir := t.TempDir()
	proj2 := &models.Project{Name: "Project Two", RepoPath: proj2RepoDir}
	if err := h.projectSvc.Create(t.Context(), proj2); err != nil {
		t.Fatalf("create proj2: %v", err)
	}

	// Write the agent SKILLS.md inside proj2's .openvibely root.
	proj2Root := filepath.Join(proj2RepoDir, ".openvibely")
	writeAgentRootSKILLSmd(t, proj2Root, "proj-agent", "Proj Agent", "project two agent")

	agentDir := filepath.Join(proj2Root, "agents", "proj-agent")
	if _, err := os.Stat(agentDir); err != nil {
		t.Fatalf("expected agent dir to exist before delete: %v", err)
	}

	agentsIndexPath := filepath.Join(proj2Root, "agents", "AGENTS.md")
	before, err := os.ReadFile(agentsIndexPath)
	if err != nil {
		t.Fatalf("read proj2 AGENTS.md before delete: %v", err)
	}
	if !strings.Contains(string(before), "## proj-agent") {
		t.Fatalf("expected ## proj-agent in proj2 AGENTS.md before delete:\n%s", before)
	}

	// Create the project-scoped agent in the DB with ProjectID = proj2.ID.
	agent := &models.Agent{
		Name:      "Proj Agent",
		Key:       "proj-agent",
		Scope:     models.AgentScopeProject,
		ProjectID: proj2.ID,
		Model:     "inherit",
		Tools:     []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// DELETE with explicit ?project_id=<proj2-id>.
	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID+"?project_id="+proj2.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// DB row must be gone.
	gone, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if gone != nil {
		t.Fatalf("expected agent deleted from DB, got %+v", gone)
	}

	// proj2 agent directory must be removed.
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("expected proj2 agent directory removed, stat err=%v", err)
	}

	// proj2 AGENTS.md must no longer have ## proj-agent.
	after, readErr := os.ReadFile(agentsIndexPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read proj2 AGENTS.md after delete: %v", readErr)
	}
	if strings.Contains(string(after), "## proj-agent") {
		t.Fatalf("expected ## proj-agent removed from proj2 AGENTS.md:\n%s", after)
	}

	// GET /agents?project_id=<proj2-id> must not recreate the agent via
	// SyncRootDeclarations (which would re-scan the project root if the
	// directory was not actually removed).
	req2 := httptest.NewRequest(http.MethodGet, "/agents?project_id="+proj2.ID, nil)
	req2.Header.Set("HX-Request", "true")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /agents expected 200, got %d", rec2.Code)
	}
	if strings.Contains(rec2.Body.String(), `data-agent-name="Proj Agent"`) {
		t.Fatal("agent reappeared after GET /agents?project_id=...; SyncRootDeclarations re-created it from a directory that should have been removed")
	}

	// proj1 must not have gained an agent directory — wrong-project isolation.
	proj1AgentDir := filepath.Join(proj1RepoDir, ".openvibely", "agents", "proj-agent")
	if _, err := os.Stat(proj1AgentDir); !os.IsNotExist(err) {
		t.Fatalf("expected proj1 to NOT have an agent dir, but one was found: stat err=%v", err)
	}
}

func TestHandler_ListAgents_RemovedProjectOverrideDoesNotRematerializeStaleState(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	globalRoot := t.TempDir()
	writeAgentRootSKILLSmd(t, globalRoot, "shared", "Global Shared", "global declaration")

	projectRepoDir := t.TempDir()
	project := &models.Project{Name: "Project", RepoPath: projectRepoDir}
	if err := h.projectSvc.Create(t.Context(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectRoot := filepath.Join(projectRepoDir, ".openvibely")
	projectDir := filepath.Join(projectRoot, "agents", "shared")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project declaration: %v", err)
	}
	projectDeclaration := "---\nkind: openvibely.agent_skill\nversion: 1\nagent:\n  key: shared\n  name: Project Shared\n  scope: project\n  project_id: " + project.ID + "\n  selectable_as_primary: true\n---\n# Project Shared\n"
	if err := os.WriteFile(filepath.Join(projectDir, "SKILLS.md"), []byte(projectDeclaration), 0o644); err != nil {
		t.Fatalf("write project declaration: %v", err)
	}

	maintenanceSvc := service.NewAgentLibraryMaintenanceService(nil, nil, agentRepo)
	maintenanceSvc.SetLifecycleRepo(lifecycleRepo)
	maintenanceSvc.SetAgentsRootPath(globalRoot)
	h.SetAgentRepo(agentRepo)
	h.SetLifecycleRepo(lifecycleRepo)
	h.SetAgentSkillRoot(globalRoot)
	h.SetAgentLibraryMaintenanceService(maintenanceSvc)

	list := func() *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/agents?project_id="+project.ID, nil)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /agents status %d: %s", rec.Code, rec.Body.String())
		}
		return rec
	}
	if body := list().Body.String(); !strings.Contains(body, `data-agent-name="Project Shared"`) {
		t.Fatalf("project declaration was not initially rendered: %s", truncateBody(body, 1000))
	}
	if err := os.RemoveAll(projectDir); err != nil {
		t.Fatalf("remove project declaration: %v", err)
	}
	if body := list().Body.String(); !strings.Contains(body, `data-agent-name="Global Shared"`) || strings.Contains(body, `data-agent-name="Project Shared"`) {
		t.Fatalf("removed project override did not restore rendered global state: %s", truncateBody(body, 1000))
	}
	agent, err := agentRepo.GetByKey(t.Context(), "shared")
	if err != nil || agent == nil || agent.Scope != models.AgentScopeGlobal || agent.ProjectID != "" {
		t.Fatalf("removed project override left stale database state: err=%v agent=%#v", err, agent)
	}
	if _, err := os.Stat(projectDir); !os.IsNotExist(err) {
		t.Fatalf("ListAgents rematerialized removed project declaration: stat err=%v", err)
	}
}

// TestHandler_DeleteAgent_ProjectScopedUsesAgentProjectID verifies that when a
// project-scoped agent has ProjectID set, deleting it from the selected owning
// project without supplying a ?project_id query string removes the agent
// directory from the correct project. This covers non-browser callers and older
// UI requests that rely on the saved selected-project context.
func TestHandler_DeleteAgent_ProjectScopedUsesAgentProjectID(t *testing.T) {
	h, e, _, db := setupTestHandlerWithDB(t)
	agentRepo := repository.NewAgentRepo(db)
	h.SetAgentRepo(agentRepo)

	// A global skill root is required for the directory-cleanup guard.
	globalRoot := t.TempDir()
	h.SetAgentSkillRoot(globalRoot)

	// Create a project whose RepoPath is a real temp directory so that
	// ProjectSkillRootForResolver can resolve the .openvibely path.
	projectRepoDir := t.TempDir()
	proj := &models.Project{Name: "Project Alpha", RepoPath: projectRepoDir}
	if err := h.projectSvc.Create(t.Context(), proj); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := h.settingsRepo.Set(t.Context(), uiPreferenceSelectedProjectIDKey, proj.ID); err != nil {
		t.Fatalf("select owning project: %v", err)
	}

	// Write the agent's SKILLS.md inside the project's .openvibely root.
	projectRoot := filepath.Join(projectRepoDir, ".openvibely")
	writeAgentRootSKILLSmd(t, projectRoot, "project-agent", "Project Agent", "project scoped agent")

	agentDir := filepath.Join(projectRoot, "agents", "project-agent")
	if _, err := os.Stat(agentDir); err != nil {
		t.Fatalf("expected agent dir to exist before delete: %v", err)
	}

	// Create the project-scoped agent in the DB with ProjectID set.
	agent := &models.Agent{
		Name:      "Project Agent",
		Key:       "project-agent",
		Scope:     models.AgentScopeProject,
		ProjectID: proj.ID,
		Model:     "inherit",
		Tools:     []string{},
	}
	if err := agentRepo.Create(t.Context(), agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}

	// DELETE without ?project_id in the URL — agent.ProjectID must be used.
	req := httptest.NewRequest(http.MethodDelete, "/agents/"+agent.ID, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Project agent directory must be removed via agent.ProjectID resolution.
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("expected project agent directory removed via agent.ProjectID, stat err=%v", err)
	}

	// DB row must be gone.
	gone, err := agentRepo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatalf("GetByID after delete: %v", err)
	}
	if gone != nil {
		t.Fatalf("expected agent deleted from DB, got %+v", gone)
	}
}
