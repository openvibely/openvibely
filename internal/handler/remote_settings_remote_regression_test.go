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
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRemoteSettingsBulkDeletesOnOwner(t *testing.T) {
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	env := wireTestHandler(t, remote.Wrap(local.Stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	h.agentRepo = remote.Wrap(local.Stores, &remote.Router{Hub: hub}).Agents
	p := &models.Project{Name: "Project 1"}
	require.NoError(t, local.Stores.Projects.Create(context.Background(), p))
	require.NoError(t, local.Stores.Alerts.Create(context.Background(), &models.Alert{ProjectID: p.ID, Title: "Local alert", Type: models.AlertCustom, Severity: models.SeverityInfo}))
	srv, _, owner := fakeRemoteOV(t, "secret")
	owner.SetAgentRepo(owner.remoteExecutor.Stores.Agents)
	rp := &models.Project{Name: "Project 2"}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(context.Background(), rp))
	agent := &models.Agent{Name: "Remote agent", Key: "remote_agent", Scope: models.AgentScopeProject, ProjectID: rp.ID}
	require.NoError(t, owner.remoteExecutor.Stores.Agents.Create(context.Background(), agent))
	model := &models.LLMConfig{Name: "Remote model", Provider: models.ProviderTest, Model: "test"}
	require.NoError(t, owner.remoteExecutor.Stores.LLMConfigs.Create(context.Background(), model))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})

	for _, tc := range []struct{ path, id string }{{"/agents/bulk", remoteid.ID(sid, agent.ID)}, {"/models/bulk", remoteid.ID(sid, model.ID)}} {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, tc.path+"?project_id="+remoteid.ID(sid, rp.ID), strings.NewReader(fmt.Sprintf(`{"ids":[%q]}`, tc.id)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://example.com")
			req.Header.Set("HX-Request", "true")
			e.ServeHTTP(rec, req.WithContext(ctx))
			if tc.path == "/models/bulk" {
				require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			} else {
				require.Equal(t, 200, rec.Code, rec.Body.String())
			}
		})
	}
	remainingAgent, err := owner.remoteExecutor.Stores.Agents.GetByID(ctx, agent.ID)
	require.NoError(t, err)
	require.Nil(t, remainingAgent)
	remainingModel, err := owner.remoteExecutor.Stores.LLMConfigs.GetByID(ctx, model.ID)
	require.NoError(t, err)
	require.NotNil(t, remainingModel)
}
