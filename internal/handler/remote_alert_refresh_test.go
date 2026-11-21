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

func TestRemoteAlertActionKeepsAllProjects(t *testing.T) {
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	env := wireTestHandler(t, remote.Wrap(local.Stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	p := &models.Project{Name: "Project 1"}
	require.NoError(t, local.Stores.Projects.Create(context.Background(), p))
	require.NoError(t, local.Stores.Alerts.Create(context.Background(), &models.Alert{ProjectID: p.ID, Title: "Local alert", Type: models.AlertCustom, Severity: models.SeverityInfo}))
	srv, _, owner := fakeRemoteOV(t, "secret")
	rp := &models.Project{Name: "Project 2"}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(context.Background(), rp))
	a := &models.Alert{ProjectID: rp.ID, Title: "Remote alert", Type: models.AlertCustom, Severity: models.SeverityInfo}
	require.NoError(t, owner.remoteExecutor.Stores.Alerts.Create(context.Background(), a))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/alerts/"+remoteid.ID(sid, a.ID)+"/read?project_id="+models.AllProjectsID, nil).WithContext(ctx))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Local alert")
	updated, err := owner.remoteExecutor.Stores.Alerts.GetByIDForProject(context.Background(), rp.ID, a.ID)
	require.NoError(t, err)
	require.True(t, updated.IsRead)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/alerts/"+remoteid.ID(sid, a.ID)+"?project_id="+models.AllProjectsID, nil).WithContext(ctx))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Local alert")
	require.NotContains(t, rec.Body.String(), "Remote alert")
}
