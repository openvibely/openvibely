package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComposerDestinationModelOptions(t *testing.T) {
	ctx := context.Background()
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := &models.Project{Name: "Remote destination", RepoPath: t.TempDir()}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(ctx, project))
	lm := &models.LLMConfig{Name: "Only on desktop", Provider: models.ProviderAnthropic, Model: "claude-sonnet-4-5-20250929"}
	rm := &models.LLMConfig{Name: "Only on remote", Provider: models.ProviderAnthropic, Model: lm.Model}
	require.NoError(t, local.Stores.LLMConfigs.Create(ctx, lm))
	require.NoError(t, owner.remoteExecutor.Stores.LLMConfigs.Create(ctx, rm))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	require.Eventually(t, func() bool { return hub.Get(models.BootstrapUserID, sid).Connected() }, 5*time.Second, 20*time.Millisecond)
	get := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/tasks/new?model_options=1&project_id="+url.QueryEscape(id), nil)
		e.ServeHTTP(r, req)
		return r
	}
	localAgent := &models.Agent{Name: "Local selectable agent", Key: "local-agent", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true}
	remoteAgent := &models.Agent{Name: "Remote selectable agent", Key: "remote-agent", Scope: models.AgentScopeGlobal, Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, local.Stores.Agents.Create(ctx, localAgent))
	require.NoError(t, owner.remoteExecutor.Stores.Agents.Create(ctx, remoteAgent))
	h.SetAgentRepo(wrapped.Agents)
	owner.SetAgentRepo(owner.remoteExecutor.Stores.Agents)
	getAgents := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRecorder()
		e.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/tasks/new?agent_options=1&project_id="+url.QueryEscape(id), nil))
		return r
	}
	ar := getAgents(remoteid.ID(sid, project.ID))
	require.Equal(t, http.StatusOK, ar.Code, ar.Body.String())
	require.Contains(t, ar.Body.String(), remoteid.ID(sid, remoteAgent.ID))
	require.NotContains(t, ar.Body.String(), localAgent.Name)
	ar = getAgents("default")
	require.Equal(t, http.StatusOK, ar.Code, ar.Body.String())
	require.Contains(t, ar.Body.String(), localAgent.Name)
	require.NotContains(t, ar.Body.String(), remoteAgent.Name)
	r := get(remoteid.ID(sid, project.ID))
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), rm.Name)
	require.Contains(t, r.Body.String(), remoteid.ID(sid, rm.ID))
	require.NotContains(t, r.Body.String(), lm.Name)
	require.NotContains(t, r.Body.String(), "task-detail-content")
	r = get("default")
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	require.Contains(t, r.Body.String(), lm.Name)
	require.NotContains(t, r.Body.String(), rm.Name)
	hub.Close(models.BootstrapUserID, sid)
	ar = getAgents(remoteid.ID(sid, project.ID))
	require.NotEqual(t, http.StatusOK, ar.Code)
	require.NotContains(t, ar.Body.String(), localAgent.Name)
	r = get(remoteid.ID(sid, project.ID))
	require.NotEqual(t, http.StatusOK, r.Code)
	require.NotContains(t, r.Body.String(), lm.Name)
}
