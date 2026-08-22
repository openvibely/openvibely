package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestHandler_GetAgentSkills_MigratesLegacyEmbeddedSkills(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		Name:    "Skill Agent",
		Key:     "skill_agent",
		Scope:   models.AgentScopeGlobal,
		Enabled: true,
		Skills: []models.SkillConfig{{
			Name:        "Legacy Debug",
			Description: "Legacy debug things",
			Tools:       "Read,Grep",
			Content:     "Use logs",
		}, {
			Name:        "Incomplete Legacy",
			Description: "Preserve this until it can be repaired",
		}},
	}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	enabled := true
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: root}, nil)
	if _, err := imp.WriteAgentOwnedSkill(t.Context(), "global", agent.Key, &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Skill: agentlibrary.SkillBlock{
			Key:         "review_migrations",
			Name:        "Review Migrations",
			Description: "Review DB migrations",
			Enabled:     &enabled,
		},
	}, "Check downgrade safety."); err != nil {
		t.Fatal(err)
	}

	h := &Handler{agentRepo: repo, agentSkillRoot: root}
	rec := performAgentSkillsRequest(t, h.GetAgentSkills, http.MethodGet, "/agents/"+agent.ID+"/skills", nil, agent.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got agentSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !containsAll(rec.Body.String(), "review_migrations", "legacy_debug", "Legacy Debug", "router_index") {
		t.Fatalf("unexpected body: %s", rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "agents", "skill_agent", "skills", "legacy_debug", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(data), "Legacy Debug", "Allowed legacy tools: Read,Grep", "Use logs") {
		t.Fatalf("legacy skill not converted correctly: %s", data)
	}
	index, err := os.ReadFile(filepath.Join(root, "agents", "skill_agent", "SKILLS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "## skill_agent/legacy_debug") {
		t.Fatalf("converted skill not indexed: %s", index)
	}
	stored, err := repo.GetByID(t.Context(), agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Skills) != 1 || stored.Skills[0].Name != "Incomplete Legacy" {
		t.Fatalf("only unconvertible legacy DB skills should remain after migration, got %+v", stored.Skills)
	}
	if !stored.SelectableAsPrimary {
		t.Fatalf("converted legacy skill agent should be selectable as primary: %+v", stored)
	}
}

func TestHandler_GetAgentSkills_MigratesUnscopedLegacyAgentSkillsToGlobalRoot(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	agent := &models.Agent{
		Name:    "Claudia",
		Key:     "claudia",
		Enabled: true,
		Skills: []models.SkillConfig{{
			Name:    "Reusable Global Habit",
			Content: "Use the user's preferred workflow.",
		}},
	}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	globalRoot := t.TempDir()
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}

	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: globalRoot}
	target := "/agents/" + agent.ID + "/skills?project_id=" + project.ID
	rec := performAgentSkillsRequest(t, h.GetAgentSkills, http.MethodGet, target, nil, agent.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "agents", "claudia", "skills", "reusable_global_habit", "SKILL.md")); err != nil {
		t.Fatalf("expected unscoped legacy skill under global root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "agents", "claudia", "skills", "reusable_global_habit", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("expected unscoped legacy skill not to be written under project root, stat err=%v", err)
	}
}

func TestHandler_GetAgentSkills_ProjectSkillOverridesGlobalScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	globalRoot := t.TempDir()
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	projectRepo := repository.NewProjectRepo(db)
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	enabled := true
	globalImp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Global: globalRoot}, nil)
	if _, err := globalImp.WriteAgentOwnedSkill(t.Context(), "global", agent.Key, &agentlibrary.SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: agentlibrary.SkillBlock{Key: "review_migrations", Name: "Global Review", Enabled: &enabled}}, "Global body"); err != nil {
		t.Fatal(err)
	}
	projectImp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Project: projectRoot}, nil)
	if _, err := projectImp.WriteAgentOwnedSkill(t.Context(), "project", agent.Key, &agentlibrary.SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: agentlibrary.SkillBlock{Key: "review_migrations", Name: "Project Review", Enabled: &enabled}}, "Project body"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: globalRoot}
	rec := performAgentSkillsRequest(t, h.GetAgentSkills, http.MethodGet, "/agents/"+agent.ID+"/skills?project_id="+project.ID, nil, agent.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var got agentSkillsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.RoutedSkills) != 1 || got.RoutedSkills[0].Name != "Project Review" || got.RoutedSkills[0].Scope != "project" {
		t.Fatalf("expected project override, got %+v", got.RoutedSkills)
	}
}

func TestHandler_AgentOwnedSkillRoutesRejectMismatchedProjectAndUseAgentProjectWhenOmitted(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	projectARepoPath := t.TempDir()
	projectBRepoPath := t.TempDir()
	projectA := &models.Project{Name: "Project A", RepoPath: projectARepoPath}
	if err := projectRepo.Create(t.Context(), projectA); err != nil {
		t.Fatal(err)
	}
	projectB := &models.Project{Name: "Project B", RepoPath: projectBRepoPath}
	if err := projectRepo.Create(t.Context(), projectB); err != nil {
		t.Fatal(err)
	}
	projectARoot := filepath.Join(projectARepoPath, ".openvibely")
	projectBRoot := filepath.Join(projectBRepoPath, ".openvibely")
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, ProjectID: projectB.ID, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	writeAgentOwnedSkillForTest(t, projectARoot, agent.Key, "review_migrations", "Project A Review", "Project A body")
	writeAgentOwnedSkillForTest(t, projectBRoot, agent.Key, "review_migrations", "Project B Review", "Project B body")
	projectAOriginal := mustReadFileForTest(t, filepath.Join(projectARoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"))
	projectBOriginal := mustReadFileForTest(t, filepath.Join(projectBRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"))
	h := &Handler{agentRepo: repo, lifecycleRepo: lifecycleRepo, projectRepo: projectRepo, agentSkillRoot: t.TempDir()}

	rec := performAgentSkillsRequest(t, h.GetAgentSkills, http.MethodGet, "/agents/"+agent.ID+"/skills?project_id="+projectA.ID, nil, agent.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Project A Review") {
		t.Fatalf("mismatched list leaked Project A skill: %s", rec.Body.String())
	}

	createPayload, _ := json.Marshal(agentSkillSaveRequest{Handle: "new_skill", Name: "New Skill", Scope: "project", Body: "new body"})
	rec = performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills?project_id="+projectA.ID, createPayload, agent.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoFileForTest(t, filepath.Join(projectARoot, "agents", "reviewer", "skills", "new_skill", "SKILL.md"))
	assertNoFileForTest(t, filepath.Join(projectBRoot, "agents", "reviewer", "skills", "new_skill", "SKILL.md"))

	updatePayload, _ := json.Marshal(agentSkillSaveRequest{Handle: "review_migrations", Scope: "project", Body: "Updated body"})
	rec = performAgentSkillsRequest(t, h.UpdateAgentOwnedSkill, http.MethodPut, "/agents/"+agent.ID+"/skills/review_migrations?project_id="+projectA.ID, updatePayload, agent.ID, "review_migrations")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertFileContentForTest(t, filepath.Join(projectARoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"), projectAOriginal)
	assertFileContentForTest(t, filepath.Join(projectBRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"), projectBOriginal)

	rec = performAgentSkillsRequest(t, h.ArchiveAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills/review_migrations/archive?project_id="+projectA.ID, []byte(`{"reason":"wrong project"}`), agent.ID, "review_migrations")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("archive status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertFileContentForTest(t, filepath.Join(projectARoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"), projectAOriginal)
	assertFileContentForTest(t, filepath.Join(projectBRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"), projectBOriginal)

	createPayload, _ = json.Marshal(agentSkillSaveRequest{Handle: "agent_project_skill", Name: "Agent Project Skill", Scope: "project", Body: "owned by Project B"})
	rec = performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills", createPayload, agent.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("omitted project_id POST status=%d body=%s", rec.Code, rec.Body.String())
	}
	assertNoFileForTest(t, filepath.Join(projectARoot, "agents", "reviewer", "skills", "agent_project_skill", "SKILL.md"))
	content := mustReadFileForTest(t, filepath.Join(projectBRoot, "agents", "reviewer", "skills", "agent_project_skill", "SKILL.md"))
	if !containsAll(content, "Agent Project Skill", "owned by Project B") {
		t.Fatalf("omitted project_id did not write Project B skill correctly: %s", content)
	}
}

func TestHandler_CreateAgentOwnedSkill_WritesIndexedSkill(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	lifecycleRepo := repository.NewLifecycleRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	globalRoot := t.TempDir()
	h := &Handler{agentRepo: repo, lifecycleRepo: lifecycleRepo, projectRepo: projectRepo, agentSkillRoot: globalRoot}
	body, _ := json.Marshal(agentSkillSaveRequest{Handle: "review_migrations", Name: "Review Migrations", Description: "Review DB migrations", Scope: "project", Body: "Check downgrade safety."})
	rec := performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills?project_id="+project.ID, body, agent.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	path := filepath.Join(projectRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	index, err := os.ReadFile(filepath.Join(projectRoot, "agents", "reviewer", "SKILLS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(data), "skill:", "review_migrations", "Check downgrade safety") || !strings.Contains(string(index), "## reviewer/review_migrations") {
		t.Fatalf("skill or index not written correctly\nskill=%s\nindex=%s", data, index)
	}
}

func TestHandler_CreateAgentOwnedSkill_RawBodyUsesSharedMetadataNormalization(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: t.TempDir()}
	body, _ := json.Marshal(agentSkillSaveRequest{Handle: "raw_notes", Scope: "project", Body: "Plain body."})
	rec := performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills?project_id="+project.ID, body, agent.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "agents", "reviewer", "skills", "raw_notes", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !containsAll(content, "key: raw_notes", "name: raw_notes", "Plain body.") {
		t.Fatalf("skill file missing normalized raw-body metadata: %s", content)
	}
	for _, unwanted := range []string{"enabled:", "description:"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("raw-body metadata should stay clean, found %q in:\n%s", unwanted, content)
		}
	}
}

func TestHandler_UpdateAgentOwnedSkill_ClearsFrontmatterMetadata(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Project: projectRoot}, nil)
	if _, err := imp.WriteAgentOwnedSkill(t.Context(), "project", agent.Key, &agentlibrary.SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: agentlibrary.SkillBlock{Key: "review_migrations", Name: "Old Name", Description: "Old description"}}, "Old body"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: t.TempDir()}
	payload := agentSkillSaveRequest{
		Handle: "review_migrations",
		Scope:  "project",
		Body:   "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: review_migrations\n    name: Old Name\n    description: Old description\n---\n\nUpdated body.",
	}
	body, _ := json.Marshal(payload)
	rec := performAgentSkillsRequest(t, h.UpdateAgentOwnedSkill, http.MethodPut, "/agents/"+agent.ID+"/skills/review_migrations?project_id="+project.ID, body, agent.ID, "review_migrations")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "Old Name") || strings.Contains(content, "Old description") {
		t.Fatalf("expected cleared metadata not to remain in frontmatter; got\n%s", content)
	}
	if !strings.Contains(content, "Updated body.") {
		t.Fatalf("expected updated body to remain; got\n%s", content)
	}
}

func TestHandler_UpdateAgentOwnedSkill_RejectsFrontmatterSkillKeyMismatch(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Project: projectRoot}, nil)
	if _, err := imp.WriteAgentOwnedSkill(t.Context(), "project", agent.Key, &agentlibrary.SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: agentlibrary.SkillBlock{Key: "review_migrations"}}, "Body"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: t.TempDir()}
	payload := agentSkillSaveRequest{Handle: "review_migrations", Scope: "project", Body: "---\nkind: openvibely.agent_skill\nversion: 1\nskill:\n    key: wrong_skill\n---\n\nBody"}
	body, _ := json.Marshal(payload)
	rec := performAgentSkillsRequest(t, h.UpdateAgentOwnedSkill, http.MethodPut, "/agents/"+agent.ID+"/skills/review_migrations?project_id="+project.ID, body, agent.ID, "review_migrations")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "body frontmatter skill.key must match handle") {
		t.Fatalf("expected skill.key mismatch error, got %s", rec.Body.String())
	}
}

func TestHandler_UpdateAgentOwnedSkill_RejectsMismatchedFrontmatterAgentKey(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Project: projectRoot}, nil)
	if _, err := imp.WriteAgentOwnedSkill(t.Context(), "project", agent.Key, &agentlibrary.SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: agentlibrary.SkillBlock{Key: "review_migrations"}}, "Body"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: t.TempDir()}
	payload := agentSkillSaveRequest{Handle: "review_migrations", Scope: "project", Body: "---\nkind: openvibely.agent_skill\nversion: 1\nagent:\n    key: other_agent\nskill:\n    key: review_migrations\n---\n\nBody"}
	body, _ := json.Marshal(payload)
	rec := performAgentSkillsRequest(t, h.UpdateAgentOwnedSkill, http.MethodPut, "/agents/"+agent.ID+"/skills/review_migrations?project_id="+project.ID, body, agent.ID, "review_migrations")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "does not match scoped agent") {
		t.Fatalf("expected scoped importer agent.key mismatch error, got %s", rec.Body.String())
	}
}

func TestHandler_ArchiveAgentOwnedSkill_MarksFrontmatterArchived(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	projectRepo := repository.NewProjectRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	repoPath := t.TempDir()
	projectRoot := filepath.Join(repoPath, ".openvibely")
	project := &models.Project{Name: "Project", RepoPath: repoPath}
	if err := projectRepo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Project: projectRoot}, nil)
	enabled := true
	if _, err := imp.WriteAgentOwnedSkill(t.Context(), "project", agent.Key, &agentlibrary.SkillDeclaration{Kind: "openvibely.agent_skill", Version: 1, Skill: agentlibrary.SkillBlock{Key: "review_migrations", Enabled: &enabled}}, "Body"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, projectRepo: projectRepo, agentSkillRoot: t.TempDir()}
	payload := []byte(`{"reason":"merged"}`)
	rec := performAgentSkillsRequest(t, h.ArchiveAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills/review_migrations/archive?project_id="+project.ID, payload, agent.ID, "review_migrations")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(projectRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(data), "archived: true", "archive_reason: merged") {
		t.Fatalf("skill not archived: %s", data)
	}
}

func TestHandler_CreateAgentOwnedSkill_RejectsProjectScopeWithoutProjectRoot(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	globalRoot := t.TempDir()
	h := &Handler{agentRepo: repo, agentSkillRoot: globalRoot}
	body, _ := json.Marshal(agentSkillSaveRequest{Handle: "review_migrations", Name: "Review Migrations", Scope: "project", Body: "Body"})
	rec := performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills", body, agent.ID, "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(globalRoot, "agents", "reviewer", "skills", "review_migrations", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatalf("project-scoped skill must not be written to global root, stat err=%v", err)
	}
}

func TestHandler_CreateAgentOwnedSkill_RejectsProtectedAgent(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewAgentRepo(db)
	agent := &models.Agent{Name: "Protected Custom", Key: "protected_custom", Enabled: true, GeneratedStatus: models.AgentStatusProtected}
	if err := repo.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
	h := &Handler{agentRepo: repo, agentSkillRoot: t.TempDir()}
	body, _ := json.Marshal(agentSkillSaveRequest{Handle: "maintain_skill_library", Body: "Body"})
	rec := performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills", body, agent.ID, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func performAgentSkillsRequest(t *testing.T, fn func(echo.Context) error, method, target string, body []byte, agentID, skill string) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	}
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	if skill != "" {
		c.SetParamNames("id", "skill")
		c.SetParamValues(agentID, skill)
	} else {
		c.SetParamNames("id")
		c.SetParamValues(agentID)
	}
	if err := fn(c); err != nil {
		e.HTTPErrorHandler(err, c)
	}
	return rec
}

func containsAll(s string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(s, n) {
			return false
		}
	}
	return true
}

func writeAgentOwnedSkillForTest(t *testing.T, root, agentKey, handle, name, body string) {
	t.Helper()
	imp := agentlibrary.NewImporter(agentlibrary.SkillRoots{Project: root}, nil)
	if _, err := imp.WriteAgentOwnedSkill(t.Context(), "project", agentKey, &agentlibrary.SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Skill: agentlibrary.SkillBlock{
			Key:  handle,
			Name: name,
		},
	}, body); err != nil {
		t.Fatal(err)
	}
}

func mustReadFileForTest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func assertFileContentForTest(t *testing.T, path, want string) {
	t.Helper()
	if got := mustReadFileForTest(t, path); got != want {
		t.Fatalf("file %s changed\nwant:\n%s\ngot:\n%s", path, want, got)
	}
}

func assertNoFileForTest(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no file at %s, stat err=%v", path, err)
	}
}
