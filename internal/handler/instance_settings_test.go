package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestInstanceSettingsIgnoreRemoteProjectSelection(t *testing.T) {
	ctx := context.Background()
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	stores := teststores.New(t)
	wrapped := remote.Wrap(stores.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, app := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	h.SetAgentRepo(wrapped.Agents)
	h.SetAgentSkillRoot(t.TempDir())
	srv, _, owner := fakeRemoteOV(t, "secret")
	owner.SetAgentRepo(owner.remoteExecutor.Stores.Agents)
	project := &models.Project{Name: "Project 1", RepoPath: t.TempDir()}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(ctx, project))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, serversRequest("POST", "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	server := savedServerID(t, h)
	remoteProject := remoteid.ID(server, project.ID)
	localCalls, remoteCalls := 0, 0
	h.slackSvc = &fakeSlackService{disconnectFn: func(context.Context) error { localCalls++; return nil }, testFn: func(context.Context) error { localCalls++; return nil }}
	owner.slackSvc = &fakeSlackService{disconnectFn: func(context.Context) error { remoteCalls++; return nil }, testFn: func(context.Context) error { remoteCalls++; return nil }}
	require.NoError(t, owner.settingsRepo.Set(ctx, service.SlackSettingClientID, "remote-client"))
	require.NoError(t, owner.settingsRepo.Set(ctx, "worktree_cleanup", "false"))
	remoteWorkers, err := owner.workerRepo.GetMaxWorkers(ctx)
	require.NoError(t, err)
	for i, scope := range []string{"", remoteProject, models.AllProjectsID, models.SharedProjectID("github.com/team/project"), remoteid.ID("fedcba9876543210", "project1")} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			post := func(path string, values url.Values) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				req := serversRequest(http.MethodPost, path+"?project_id="+url.QueryEscape(scope), values)
				req.Header.Set("Accept", "application/json")
				app.ServeHTTP(rec, req)
				require.Less(t, rec.Code, 400, rec.Body.String())
				return rec
			}
			post("/channels/slack/test", nil)
			post("/channels/slack/disconnect", nil)
			post("/channels/slack/configure", url.Values{"slack_client_id": {"local-client"}, "slack_client_secret": {"test-secret"}, "slack_app_token": {"test-token"}})
			got, err := h.settingsRepo.Get(ctx, service.SlackSettingClientID)
			require.NoError(t, err)
			require.Equal(t, "local-client", got)
			got, err = owner.settingsRepo.Get(ctx, service.SlackSettingClientID)
			require.NoError(t, err)
			require.Equal(t, "remote-client", got)
			post("/settings/worktree", url.Values{"worktree_cleanup": {"true"}})
			cleanup, err := stores.Stores.Settings.Get(ctx, "worktree_cleanup")
			require.NoError(t, err)
			require.Equal(t, "true", cleanup)
			cleanup, err = owner.settingsRepo.Get(ctx, "worktree_cleanup")
			require.NoError(t, err)
			require.Equal(t, "false", cleanup)
			post("/workers", url.Values{"max_workers": {"0"}})
			n, err := h.workerRepo.GetMaxWorkers(ctx)
			require.NoError(t, err)
			require.Zero(t, n)
			n, err = owner.workerRepo.GetMaxWorkers(ctx)
			require.NoError(t, err)
			require.Equal(t, remoteWorkers, n)
			for _, path := range []string{"/workers", "/workers/stats/models", "/models"} {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("GET", path+"?project_id="+url.QueryEscape(scope), nil)
				req.Header.Set("HX-Request", "true")
				app.ServeHTTP(rec, req)
				require.Equal(t, 200, rec.Code, rec.Body.String())
			}
		})
	}
	t.Run("global lists do not require the selected remote connection", func(t *testing.T) {
		for _, path := range []string{"/agents", "/skills"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("GET", path+"?scope=global&project_id="+remoteid.ID("fedcba9876543210", "project1"), nil)
			req.Header.Set("HX-Request", "true")
			app.ServeHTTP(rec, req)
			require.Equal(t, 200, rec.Code, rec.Body.String())
		}
	})
	t.Run("agents keep Local globals and remote project definitions separate", func(t *testing.T) {
		remoteGlobal := &models.Agent{Name: "Remote global agent", Key: "remote_global", Scope: models.AgentScopeGlobal}
		remoteOwned := &models.Agent{Name: "Remote project agent", Key: "remote_project", Scope: models.AgentScopeProject, ProjectID: project.ID}
		require.NoError(t, owner.remoteExecutor.Stores.Agents.Create(ctx, remoteGlobal))
		require.NoError(t, owner.remoteExecutor.Stores.Agents.Create(ctx, remoteOwned))
		rec := httptest.NewRecorder()
		req := serversRequest("POST", "/agents?project_id="+remoteProject, url.Values{"name": {"Local global agent"}, "key": {"local_global"}, "scope": {"global"}, "model": {"inherit"}, "enabled": {"true"}})
		req.Header.Set("HX-Request", "true")
		app.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		require.Contains(t, rec.Body.String(), "Local global agent")
		require.Contains(t, rec.Body.String(), "Remote project agent")
		require.NotContains(t, rec.Body.String(), `data-agent-name="Remote global agent"`)
		localAgents, err := stores.Stores.Agents.ListPageFiltered(ctx, 100, 0, persistence.AgentPageFilter{Scope: "global"})
		require.NoError(t, err)
		found := false
		for _, a := range localAgents {
			if a.Name == "Local global agent" {
				found = true
			}
		}
		require.True(t, found)
		remoteAgents, err := owner.remoteExecutor.Stores.Agents.ListPageFiltered(ctx, 100, 0, persistence.AgentPageFilter{Scope: "global"})
		require.NoError(t, err)
		for _, a := range remoteAgents {
			require.NotEqual(t, "Local global agent", a.Name)
		}
		// Remote tasks can still select their server's global definitions.
		caller := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
		options, err := h.agentRepo.ListPickerOptionsForProject(caller, remoteProject)
		require.NoError(t, err)
		found = false
		for _, a := range options {
			if a.ID == remoteid.ID(server, remoteGlobal.ID) {
				found = true
			}
		}
		require.True(t, found)
		rec = httptest.NewRecorder()
		app.ServeHTTP(rec, serversRequest("DELETE", "/agents/"+remoteid.ID(server, remoteGlobal.ID)+"?project_id="+remoteProject, nil))
		require.Equal(t, 400, rec.Code, rec.Body.String())
		rec = httptest.NewRecorder()
		req = httptest.NewRequest("DELETE", "/agents/bulk?project_id="+remoteProject, strings.NewReader(fmt.Sprintf(`{"ids":[%q]}`, remoteid.ID(server, remoteGlobal.ID))))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(rec, req)
		require.Equal(t, 400, rec.Code, rec.Body.String())
		originalInstall := installPluginFn
		defer func() { installPluginFn = originalInstall }()
		installs := 0
		installPluginFn = func(context.Context, string, string) error {
			installs++
			return fmt.Errorf("unexpected plugin install")
		}
		rec = httptest.NewRecorder()
		req = httptest.NewRequest("POST", "/agents/plugins/install?project_id="+remoteProject, strings.NewReader(fmt.Sprintf(`{"plugin_id":"test-plugin","scope":"user","agent_id":%q}`, remoteid.ID(server, remoteGlobal.ID))))
		req.Header.Set("Content-Type", "application/json")
		app.ServeHTTP(rec, req)
		require.Equal(t, 400, rec.Code, rec.Body.String())
		require.Zero(t, installs)
		kept, err := owner.remoteExecutor.Stores.Agents.GetByID(ctx, remoteGlobal.ID)
		require.NoError(t, err)
		require.NotNil(t, kept)
	})
	require.Equal(t, 10, localCalls)
	require.Zero(t, remoteCalls)
}
