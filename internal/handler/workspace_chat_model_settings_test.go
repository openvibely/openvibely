package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceChatModelSettingsArePersonal(t *testing.T) {
	tc, app := organizationTestApp(t, true)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	h.projectRepo = guarded
	h.projectSvc = service.NewProjectService(guarded)
	agent := createAgent(t, h.llmConfigRepo, func(a *models.LLMConfig) { a.Provider = models.ProviderOpenAI; a.Model = "gpt-6.1-sol" })
	endpoint := "/conversation/model-settings?project_id=" + models.AllProjectsID
	legacyKey := "conversation.chat.model." + models.AllProjectsID
	legacy := `{"agent_id":"legacy-shared-choice","overrides":{}}`
	require.NoError(t, h.settingsRepo.Set(ctx, legacyKey, legacy))
	var cookies []*http.Cookie
	for _, name := range []string{"personal-model-alice", "personal-model-bob"} {
		token, err := h.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
		require.NoError(t, err)
		_, err = h.organizationSvc.Accept(ctx, token, name, "long-member-password", "")
		require.NoError(t, err)
		login := orgRequest(app, http.MethodPost, "/login", url.Values{"username": {name}, "password": {"long-member-password"}}, nil)
		cookies = append(cookies, orgCookie(t, login))
	}
	read := func(cookie *http.Cookie) map[string]any {
		response := orgRequest(app, http.MethodGet, endpoint, nil, cookie)
		require.Equal(t, 200, response.Code, response.Body.String())
		var settings map[string]any
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &settings))
		return settings
	}
	require.Equal(t, "auto", read(cookies[0])["agent_id"])
	response := orgRequest(app, http.MethodPost, endpoint, url.Values{"agent_id": {agent.ID}, "reasoning_effort": {"high"}, "service_tier": {"priority"}}, cookies[0])
	require.Equal(t, 204, response.Code, response.Body.String())
	alice := read(cookies[0])
	require.Equal(t, agent.ID, alice["agent_id"])
	require.Equal(t, "high", alice["reasoning_effort"])
	require.Equal(t, "priority", alice["service_tier"])
	bob := read(cookies[1])
	require.Equal(t, "auto", bob["agent_id"])
	require.Equal(t, false, bob["saved"])
	response = orgRequest(app, http.MethodPost, endpoint, url.Values{"agent_id": {agent.ID}, "reasoning_effort": {"medium"}, "service_tier": {"default"}}, cookies[1])
	require.Equal(t, 204, response.Code, response.Body.String())
	require.Equal(t, "medium", read(cookies[1])["reasoning_effort"])
	require.Equal(t, alice, read(cookies[0]), "Bob must not overwrite Alice's preferences")
	stored, err := h.settingsRepo.Get(ctx, legacyKey)
	require.NoError(t, err)
	require.Equal(t, legacy, stored, "leave the old shared setting untouched")
}

func TestWorkspaceChatModelSettingsAuthOffUsesOwner(t *testing.T) {
	tc, app := organizationTestApp(t, false)
	h := tc.handler
	endpoint := "/conversation/model-settings?project_id=" + models.AllProjectsID
	response := orgRequest(app, http.MethodPost, endpoint, url.Values{"agent_id": {"auto"}}, nil)
	require.Equal(t, 204, response.Code, response.Body.String())
	response = orgRequest(app, http.MethodGet, endpoint, nil, nil)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), `"saved":true`)
	value, err := h.settingsRepo.Get(t.Context(), "conversation.chat.model."+models.AllProjectsID+".user."+models.BootstrapUserID)
	require.NoError(t, err)
	require.NotEmpty(t, value)
}

func TestPrivateChatModelSettingsRequireOwnership(t *testing.T) {
	tc, app := organizationTestApp(t, true)
	h, ctx := tc.handler, t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	guarded := authz.NewProjectStore(tc.stores.Projects, &authz.Authorizer{Store: tc.stores.ProjectAccess})
	h.projectRepo = guarded
	h.projectSvc = service.NewProjectService(guarded)
	agent := createAgent(t, h.llmConfigRepo)
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	task := &models.Task{ProjectID: models.AllProjectsID, Title: "Owner private chat", Category: models.CategoryChat, Status: models.StatusCompleted, AgentID: &agent.ID}
	require.NoError(t, h.taskRepo.Create(ownerCtx, task))
	token, err := h.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	member, err := h.organizationSvc.Accept(ctx, token, "audit-private-settings", "long-member-password", "")
	require.NoError(t, err)
	hidden, err := h.taskRepo.GetByID(models.WithPrincipal(ctx, member.Principal), task.ID)
	require.NoError(t, err)
	require.Nil(t, hidden)
	login := orgRequest(app, http.MethodPost, "/login", url.Values{"username": {"audit-private-settings"}, "password": {"long-member-password"}}, nil)
	response := orgRequest(app, http.MethodGet, "/conversation/model-settings?project_id="+models.AllProjectsID+"&task_id="+task.ID, nil, orgCookie(t, login))
	require.Equal(t, 404, response.Code, response.Body.String())
	require.NotContains(t, response.Body.String(), agent.ID)

	cookie := orgCookie(t, login)
	own := &models.Task{ProjectID: models.AllProjectsID, Title: "Member personal chat", Category: models.CategoryChat, Status: models.StatusCompleted, AgentID: &agent.ID}
	require.NoError(t, h.taskRepo.Create(models.WithPrincipal(ctx, member.Principal), own))
	response = orgRequest(app, http.MethodGet, "/conversation/model-settings?project_id="+models.AllProjectsID+"&task_id="+own.ID, nil, cookie)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), agent.ID)
	project := createProject(t, h, "Shared task settings")
	shared := createTask(t, h, project.ID, "Shared thread", func(tk *models.Task) { tk.AgentID = &agent.ID })
	endpoint := "/conversation/model-settings?project_id=" + project.ID + "&task_id=" + shared.ID
	response = orgRequest(app, http.MethodGet, endpoint, nil, cookie)
	require.Equal(t, 404, response.Code, response.Body.String())
	require.NoError(t, tc.stores.ProjectAccess.SetMember(ctx, models.BootstrapUserID, project.ID, member.UserID, "viewer"))
	response = orgRequest(app, http.MethodGet, endpoint, nil, cookie)
	require.Equal(t, 200, response.Code, response.Body.String())
	require.Contains(t, response.Body.String(), agent.ID)
}
