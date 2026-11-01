package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAllProjectsLibraryOwnership(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAgentRepo(stores.Agents)
	h.SetAgentSkillRoot(t.TempDir())
	writeStandaloneSkill(t, h.agentSkillRoot, "shared-review", "Shared review", "shared", "global")
	projects := []*models.Project{createProject(t, h, "Ownership Alpha"), createProject(t, h, "Ownership Beta")}
	for _, p := range projects {
		p.RepoPath = t.TempDir()
		require.NoError(t, h.projectRepo.Update(t.Context(), p))
		writeStandaloneSkill(t, filepath.Join(p.RepoPath, ".openvibely"), "same-review", "Same review", p.Name, "project")
		agent := &models.Agent{Name: p.Name + " agent", Key: p.ID + "-agent", Scope: models.AgentScopeProject, ProjectID: p.ID, Enabled: true, SelectableAsPrimary: true, Model: "inherit", SystemPrompt: "Review"}
		require.NoError(t, stores.Agents.Create(t.Context(), agent))
	}
	get := func(path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, 200, rec.Code, rec.Body.String())
		return rec.Body.String()
	}
	for _, route := range []string{"/agents", "/skills"} {
		body := get(route + "?project_id=" + models.AllProjectsID)
		for _, p := range projects {
			require.Contains(t, body, `data-project-label>`+p.Name+`</span>`)
		}
	}
	body := get("/skills?project_id=" + models.AllProjectsID)
	require.Equal(t, 1, strings.Count(body, `data-card-select-id=":global:shared-review"`))
	for _, p := range projects {
		require.Contains(t, body, `data-card-select-id="`+p.ID+`:project:same-review"`)
	}
	// A scoped mutation must change only its owner, and return the combined list.
	req := httptest.NewRequest(http.MethodPost, "/skills/same-review/enabled?project_id="+models.AllProjectsID+"&skill_project_id="+url.QueryEscape(projects[0].ID), strings.NewReader(`{"scope":"project","enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	for _, p := range projects {
		require.Contains(t, rec.Body.String(), `data-skill-project="`+p.ID+`"`)
	}
	untouched, err := os.ReadFile(filepath.Join(projects[1].RepoPath, ".openvibely", "skills", "same-review", "SKILL.md"))
	require.NoError(t, err)
	require.NotContains(t, string(untouched), "enabled: false")
	// Project views must still exclude the other project's Agent.
	isolated := get("/agents?project_id=" + projects[0].ID)
	require.Contains(t, isolated, projects[0].Name+" agent")
	require.NotContains(t, isolated, projects[1].Name+" agent")
}

func TestAllProjectsChannelOwnershipLabels(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetWebhookRepo(stores.Webhooks)
	h.SetChannelTargetRepo(stores.ChannelTargets)
	projects := []*models.Project{createProject(t, h, "Channel Alpha"), createProject(t, h, "Channel Beta")}
	for _, p := range projects {
		wh := &models.WebhookEndpoint{ProjectID: p.ID, Name: "Same webhook", Enabled: true, DefaultPriority: 2}
		require.NoError(t, stores.Webhooks.Create(t.Context(), wh))
		require.NoError(t, stores.Fixtures.SeedChannelTarget(t.Context(), models.ChannelTarget{ID: persistence.NewID(), ProjectID: p.ID, Platform: "slack", Name: p.Name + " target", TargetID: "C123"}))
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/channels?project_id="+models.AllProjectsID, nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	for _, p := range projects {
		require.Contains(t, rec.Body.String(), `data-webhook-project="`+p.ID+`"`)
		require.Contains(t, rec.Body.String(), `data-project-label>`+p.Name+`</span>`)
	}
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/channels/outbound-targets?project_id="+models.AllProjectsID, nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	for _, p := range projects {
		require.Contains(t, rec.Body.String(), strings.ToLower(p.Name)+" target")
		require.Contains(t, rec.Body.String(), `data-project-label>`+p.Name+`</span>`)
	}

}

func TestAllProjectsOutboundTargetsRequireOwner(t *testing.T) {
	h, app, _, _ := setupTestHandlerWithStores(t)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	err := h.saveOutboundTargetsDraft(app.NewContext(req, httptest.NewRecorder()), models.AllProjectsID)
	require.ErrorContains(t, err, "Choose a project")
}

func TestAllProjectsAuthorizedUsersKeepOwnerAndCombinedList(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	projects := []*models.Project{createProject(t, h, "Authorization Alpha"), createProject(t, h, "Authorization Beta")}
	for _, p := range projects {
		form := url.Values{"project_id": {p.ID}, "return_project_id": {models.AllProjectsID}, "slack_user_id": {"U0AQYLJR14Y"}, "display_name": {p.Name}}
		req := httptest.NewRequest(http.MethodPost, "/channels/slack/authorized-users", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/channels/slack/authorized-users?project_id="+models.AllProjectsID, nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	for _, p := range projects {
		require.Contains(t, rec.Body.String(), `data-project-label>`+p.Name+`</span>`)
		users, err := stores.SlackAuth.ListByProject(t.Context(), p.ID)
		require.NoError(t, err)
		require.Len(t, users, 1)
	}
	users, err := stores.SlackAuth.ListByProject(t.Context(), projects[0].ID)
	require.NoError(t, err)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/channels/slack/authorized-users/"+users[0].ID+"?project_id="+projects[0].ID+"&return_project_id="+models.AllProjectsID, nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `data-project-label>`+projects[1].Name+`</span>`)
	remaining, err := stores.SlackAuth.ListByProject(t.Context(), projects[1].ID)
	require.NoError(t, err)
	require.Len(t, remaining, 1)
}

func TestAllProjectsAgentEditorPreservesOwner(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAgentRepo(stores.Agents)
	p := createProject(t, h, "Audit owner")
	a := &models.Agent{Name: "Audit scoped", Scope: models.AgentScopeProject, ProjectID: p.ID, Enabled: true, SelectableAsPrimary: true, Model: "inherit", SystemPrompt: "Review"}
	require.NoError(t, stores.Agents.Create(t.Context(), a))
	for _, scope := range []string{p.ID, models.AllProjectsID} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents/"+a.ID+"/json?project_id="+scope, nil))
		require.Equal(t, 200, rec.Code, "scope=%s response=%s", scope, rec.Body.String())
	}
	form := url.Values{"name": {a.Name}, "system_prompt": {"Updated prompt"}, "model": {"inherit"}, "scope": {"project"}}
	req := httptest.NewRequest(http.MethodPut, "/agents/"+a.ID+"?project_id="+models.AllProjectsID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	saved, err := stores.Agents.GetByID(t.Context(), a.ID)
	require.NoError(t, err)
	require.Equal(t, p.ID, saved.ProjectID)
	require.Equal(t, "Updated prompt", saved.SystemPrompt)
	require.Contains(t, rec.Body.String(), `data-project-label>`+p.Name+`</span>`)
	other := createProject(t, h, "Other owner")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents/"+a.ID+"/json?project_id="+other.ID, nil))
	require.Equal(t, 404, rec.Code)

}

func TestAllProjectsSkillEventOwner(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.skillAnalyticsRepo = stores.SkillAnalytics
	h.SetAgentSkillRoot(t.TempDir())
	p := createProject(t, h, "Audit skill owner")
	p.RepoPath = t.TempDir()
	require.NoError(t, h.projectRepo.Update(t.Context(), p))
	writeStandaloneSkill(t, filepath.Join(p.RepoPath, ".openvibely"), "audit-skill", "Audit skill", "audit", "project")
	req := httptest.NewRequest(http.MethodPost, "/skills/audit-skill/enabled?project_id="+models.AllProjectsID+"&skill_project_id="+p.ID, strings.NewReader(`{"scope":"project","enabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	events := skillAnalyticsEventsForTest(t, stores, "audit-skill")
	require.Len(t, events, 1)
	require.Equal(t, p.ID, events[0].ProjectID)
}

func TestAllProjectsAgentSkillsPreserveOwner(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAgentRepo(stores.Agents)
	h.SetAgentSkillRoot(t.TempDir())
	owner := createProject(t, h, "Skills owner")
	other := createProject(t, h, "Other skills")
	for _, p := range []*models.Project{owner, other} {
		p.RepoPath = t.TempDir()
		require.NoError(t, h.projectRepo.Update(t.Context(), p))
		writeAgentOwnedSkillForTest(t, filepath.Join(p.RepoPath, ".openvibely"), "reviewer", "review", "Review", p.Name+" body")
	}
	a := &models.Agent{Name: "Reviewer", Key: "reviewer", ProjectID: owner.ID, Scope: models.AgentScopeProject, Enabled: true, Model: "inherit", SystemPrompt: "Review"}
	require.NoError(t, stores.Agents.Create(t.Context(), a))
	route := "/agents/" + a.ID + "/skills"
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route+"?project_id="+models.AllProjectsID, nil))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), owner.Name+" body")
	require.NotContains(t, rec.Body.String(), other.Name+" body")
	req := httptest.NewRequest(http.MethodPut, route+"/review?project_id="+models.AllProjectsID, strings.NewReader(`{"handle":"review","name":"Review","scope":"project","body":"Updated through All projects"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	content, err := os.ReadFile(filepath.Join(owner.RepoPath, ".openvibely", "agents", "reviewer", "skills", "review", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(content), "Updated through All projects")
	untouched, err := os.ReadFile(filepath.Join(other.RepoPath, ".openvibely", "agents", "reviewer", "skills", "review", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(untouched), other.Name+" body")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, route+"?project_id="+other.ID, nil))
	require.Equal(t, 404, rec.Code)
}

// A local declaration sync failure must not hide persisted Agents or other projects.
func TestAllProjectsAgentsUnreadableDeclarations(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	h.SetAgentRepo(stores.Agents)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	maintenance := service.NewAgentLibraryMaintenanceService(nil, nil, stores.Agents)
	maintenance.SetAgentsRootPath(root)
	h.SetAgentLibraryMaintenanceService(maintenance)
	projects := []*models.Project{createProject(t, h, "Unreadable declarations"), createProject(t, h, "Healthy declarations")}
	for _, p := range projects {
		p.RepoPath = t.TempDir()
		require.NoError(t, h.projectRepo.Update(t.Context(), p))
		require.NoError(t, stores.Agents.Create(t.Context(), &models.Agent{
			Name: p.Name + " agent", Key: p.ID + "-agent", Scope: models.AgentScopeProject,
			ProjectID: p.ID, Enabled: true, SelectableAsPrimary: true, Model: "inherit", SystemPrompt: "Review",
		}))
	}
	agentsDir := filepath.Join(projects[0].RepoPath, ".openvibely", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0700))
	require.NoError(t, os.Chmod(agentsDir, 0000))
	t.Cleanup(func() { _ = os.Chmod(agentsDir, 0700) })
	if _, err := os.ReadDir(agentsDir); err == nil {
		t.Skip("filesystem permissions do not prevent reading the directory")
	}
	for _, id := range []string{projects[0].ID, models.AllProjectsID} {
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents?project_id="+id, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), projects[0].Name+" agent")
		if id == models.AllProjectsID {
			require.Contains(t, rec.Body.String(), projects[1].Name+" agent")
		}
	}
}
