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

func TestRemoteAlertBulkAndAllActions(t *testing.T) {
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

	t.Run("bulk read", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/alerts/read-bulk?project_id="+models.AllProjectsID, strings.NewReader(fmt.Sprintf(`{"ids":[%q]}`, remoteid.ID(sid, a.ID))))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("HX-Request", "true")
		e.ServeHTTP(rec, req.WithContext(ctx))
		require.Equal(t, 200, rec.Code, rec.Body.String())
	})
	// A second server has an unloaded alert; "all" must still include it.
	srv2, _, owner2 := fakeRemoteOV(t, "secret")
	p2 := &models.Project{Name: "Project 2"}
	require.NoError(t, owner2.remoteExecutor.Stores.Projects.Create(context.Background(), p2))
	a2 := &models.Alert{ProjectID: p2.ID, Title: "Remote 2 alert", Type: models.AlertCustom, Severity: models.SeverityInfo}
	require.NoError(t, owner2.remoteExecutor.Stores.Alerts.Create(ctx, a2))
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 2"}, "url": {srv2.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code)
	// Fresh unread alert on Remote 1 ensures read-all isn't relying on the preceding selected action.
	a.ID = ""
	a.IsRead = false
	require.NoError(t, owner.remoteExecutor.Stores.Alerts.Create(ctx, a))

	t.Run("read all", func(t *testing.T) {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, serversRequest(http.MethodPost, "/alerts/read-all?project_id="+models.AllProjectsID, nil).WithContext(ctx))
		require.Equal(t, 200, rec.Code)
		updated, err := owner.remoteExecutor.Stores.Alerts.GetByIDForProject(context.Background(), rp.ID, a.ID)
		require.NoError(t, err)
		require.True(t, updated.IsRead, "Remote alert should be marked read too")
		second, err := owner2.remoteExecutor.Stores.Alerts.GetByIDForProject(ctx, p2.ID, a2.ID)
		require.NoError(t, err)
		require.True(t, second.IsRead)
		count, err := local.Stores.Alerts.CountUnread(ctx, p.ID)
		require.NoError(t, err)
		require.Zero(t, count)
	})
	t.Run("delete all", func(t *testing.T) {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/alerts?project_id="+models.AllProjectsID, nil).WithContext(ctx))
		require.Equal(t, 200, rec.Code)
		_, err := owner.remoteExecutor.Stores.Alerts.GetByIDForProject(context.Background(), rp.ID, a.ID)
		require.Error(t, err, "Remote alert should be deleted too")
		_, err = owner2.remoteExecutor.Stores.Alerts.GetByIDForProject(ctx, p2.ID, a2.ID)
		require.Error(t, err)

	})
	t.Run("disconnected server prevents local-only success", func(t *testing.T) {
		fresh := &models.Alert{ProjectID: p.ID, Title: "Keep Local", Type: models.AlertCustom, Severity: models.SeverityInfo}
		require.NoError(t, local.Stores.Alerts.Create(context.Background(), fresh))
		hub.Close(models.BootstrapUserID, sid)
		err := h.mutateAllAlerts(ctx, models.AllProjectsID, "delete_all")
		require.Error(t, err)
		_, err = local.Stores.Alerts.GetByIDForProject(ctx, p.ID, fresh.ID)
		require.NoError(t, err)
	})

}
