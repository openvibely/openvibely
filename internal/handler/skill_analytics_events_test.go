package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil/storefixture"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestSkillWriteEventType(t *testing.T) {
	tests := []struct {
		name string
		res  *agentlibrary.ImportResult
		want string
	}{
		{name: "nil result", res: nil, want: models.SkillEventEdited},
		{name: "empty result", res: &agentlibrary.ImportResult{}, want: models.SkillEventEdited},
		{name: "updated only", res: &agentlibrary.ImportResult{Updated: []string{"existing"}}, want: models.SkillEventEdited},
		{name: "created result", res: &agentlibrary.ImportResult{Created: []string{"new"}}, want: models.SkillEventCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, skillWriteEventType(tt.res))
		})
	}
}

func TestCreateSkillRecordsCreatedThenEditedEvents(t *testing.T) {
	h, e, _, db := setupTestHandlerWithStores(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	project := createProject(t, h, "Skill Analytics Project")

	payload, err := json.Marshal(skillSaveRequest{
		Handle: "analytics_skill",
		Name:   "Analytics Skill",
		Scope:  "global",
		Body:   "Track this write.",
	})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/skills?project_id="+project.ID, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	events := skillAnalyticsEventsForTest(t, db, "analytics_skill")
	require.Len(t, events, 2)
	require.Equal(t, models.SkillEventCreated, events[0].EventType)
	require.Equal(t, models.SkillEventEdited, events[1].EventType)
	for _, event := range events {
		require.Equal(t, project.ID, event.ProjectID)
		require.Equal(t, models.SkillScopeGlobal, event.SkillScope)
		require.Equal(t, models.SkillEventSourceManual, event.Source)
		require.Equal(t, models.SkillSurfaceTaskThread, event.Surface)
	}
}

func TestImportSkillPackageRecordsCreatedThenEditedEvents(t *testing.T) {
	h, e, _, db := setupTestHandlerWithStores(t)
	root := t.TempDir()
	h.SetAgentSkillRoot(root)
	project := createProject(t, h, "Imported Skill Analytics Project")

	for i := 0; i < 2; i++ {
		rec := submitSkillPackageImportForTest(t, e, project.ID, "imported_analytics_skill")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}

	events := skillAnalyticsEventsForTest(t, db, "imported_analytics_skill")
	require.Len(t, events, 2)
	require.Equal(t, models.SkillEventCreated, events[0].EventType)
	require.Equal(t, models.SkillEventEdited, events[1].EventType)
	require.Equal(t, project.ID, events[0].ProjectID)
	require.Equal(t, models.SkillScopeGlobal, events[0].SkillScope)
	require.Equal(t, models.SkillEventSourceManual, events[0].Source)
	require.Equal(t, models.SkillSurfaceTaskThread, events[0].Surface)
}

func TestCreateAgentOwnedSkillRecordsCreatedEvent(t *testing.T) {
	db := teststores.New(t)
	agentRepo := db.Agents
	lifecycleRepo := db.Lifecycle
	projectRepo := db.Projects
	skillAnalyticsRepo := db.SkillAnalytics
	agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, Enabled: true}
	require.NoError(t, agentRepo.Create(t.Context(), agent))
	project := &models.Project{Name: "Agent Skill Analytics Project", RepoPath: t.TempDir()}
	require.NoError(t, projectRepo.Create(t.Context(), project))

	h := &Handler{
		agentRepo:          agentRepo,
		lifecycleRepo:      lifecycleRepo,
		projectRepo:        projectRepo,
		skillAnalyticsRepo: skillAnalyticsRepo,
		agentSkillRoot:     t.TempDir(),
	}
	body, err := json.Marshal(agentSkillSaveRequest{
		Handle: "owned_analytics_skill",
		Scope:  "project",
		Body:   "Track the owning agent.",
	})
	require.NoError(t, err)
	rec := performAgentSkillsRequest(t, h.CreateAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills?project_id="+project.ID, body, agent.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	events := skillAnalyticsEventsForTest(t, db, "owned_analytics_skill")
	require.Len(t, events, 1)
	require.Equal(t, project.ID, events[0].ProjectID)
	require.Equal(t, agent.ID, events[0].AgentID)
	require.Equal(t, models.SkillScopeAgentOwned, events[0].SkillScope)
	require.Equal(t, models.SkillEventCreated, events[0].EventType)
	require.Equal(t, models.SkillEventSourceManual, events[0].Source)
	require.Equal(t, models.SkillSurfaceTaskThread, events[0].Surface)
}

func TestEditedAgentOwnedSkillFlowsKeepEditedEvents(t *testing.T) {
	db := teststores.New(t)
	projectRepo := db.Projects
	project := &models.Project{Name: "Edited Agent Skill Analytics Project", RepoPath: t.TempDir()}
	require.NoError(t, projectRepo.Create(t.Context(), project))
	agentRepo := db.Agents
	agent := &models.Agent{
		Name:      "Reviewer",
		Key:       "reviewer",
		Scope:     models.AgentScopeProject,
		ProjectID: project.ID,
		Enabled:   true,
	}
	require.NoError(t, agentRepo.Create(t.Context(), agent))
	lifecycleRepo := db.Lifecycle
	h := &Handler{
		agentRepo:          agentRepo,
		lifecycleRepo:      lifecycleRepo,
		projectRepo:        projectRepo,
		skillAnalyticsRepo: db.SkillAnalytics,
		agentSkillRoot:     t.TempDir(),
	}
	projectRoot := filepath.Join(project.RepoPath, ".openvibely")
	writeAgentOwnedSkillForTest(t, projectRoot, agent.Key, "edited_owned_skill", "Edited Owned Skill", "Original body")

	updateBody, err := json.Marshal(agentSkillSaveRequest{
		Handle: "edited_owned_skill",
		Scope:  "project",
		Body:   "Updated body.",
	})
	require.NoError(t, err)
	rec := performAgentSkillsRequest(t, h.UpdateAgentOwnedSkill, http.MethodPut, "/agents/"+agent.ID+"/skills/edited_owned_skill?project_id="+project.ID, updateBody, agent.ID, "edited_owned_skill")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rec = performAgentSkillsRequest(t, h.ArchiveAgentOwnedSkill, http.MethodPost, "/agents/"+agent.ID+"/skills/edited_owned_skill/archive?project_id="+project.ID, []byte(`{"reason":"merged"}`), agent.ID, "edited_owned_skill")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	events := skillAnalyticsEventsForTest(t, db, "edited_owned_skill")
	require.Len(t, events, 2)
	for _, event := range events {
		require.Equal(t, project.ID, event.ProjectID)
		require.Equal(t, agent.ID, event.AgentID)
		require.Equal(t, models.SkillScopeAgentOwned, event.SkillScope)
		require.Equal(t, models.SkillEventEdited, event.EventType)
		require.Equal(t, models.SkillEventSourceManual, event.Source)
		require.Equal(t, models.SkillSurfaceTaskThread, event.Surface)
	}
}

func skillAnalyticsEventsForTest(t testing.TB, env *teststores.Env, handle string) []models.SkillAnalyticsEvent {
	t.Helper()
	events, err := env.Fixtures.SkillEvents(t.Context(), storefixture.SkillEventFilter{SkillHandle: handle})
	require.NoError(t, err)
	return events
}

func submitSkillPackageImportForTest(t *testing.T, e *echo.Echo, projectID, handle string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("scope", "global"))
	addMultipartFile(t, writer, "files", "SKILL.md", `---
kind: openvibely.agent_skill
version: 1
skill:
    key: `+handle+`
    name: Imported Analytics Skill
---

Imported analytics body.
`)
	require.NoError(t, writer.WriteField("paths", handle+"/SKILL.md"))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/skills/import?project_id="+projectID, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}
