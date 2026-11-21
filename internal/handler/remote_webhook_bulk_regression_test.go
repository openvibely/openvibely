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
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRemoteWebhookBulkDeletesOnOwner(t *testing.T) {
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	env := wireTestHandler(t, remote.Wrap(local.Stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	h.webhookRepo = remote.Wrap(local.Stores, &remote.Router{Hub: hub}).Webhooks
	p := &models.Project{Name: "Project 1"}
	require.NoError(t, local.Stores.Projects.Create(context.Background(), p))
	require.NoError(t, local.Stores.Alerts.Create(context.Background(), &models.Alert{ProjectID: p.ID, Title: "Local alert", Type: models.AlertCustom, Severity: models.SeverityInfo}))
	srv, _, owner := fakeRemoteOV(t, "secret")
	owner.webhookRepo = owner.remoteExecutor.Stores.Webhooks
	rp := &models.Project{Name: "Project 2"}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(context.Background(), rp))
	a := &models.WebhookEndpoint{ProjectID: rp.ID, Name: "Remote webhook", DefaultPriority: 2, RunAsUserID: models.BootstrapUserID}
	require.NoError(t, owner.remoteExecutor.Stores.Webhooks.Create(context.Background(), a))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code)
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})

	for _, path := range []string{"/channels/webhooks/bulk", "/channels/bulk"} {
		t.Run(path, func(t *testing.T) {
			a.ID = ""
			a.PathToken = ""
			require.NoError(t, owner.remoteExecutor.Stores.Webhooks.Create(context.Background(), a))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, path+"?project_id="+models.AllProjectsID, strings.NewReader(fmt.Sprintf(`{"ids":[%q]}`, remoteid.ID(sid, a.ID))))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://example.com")
			req.Header.Set("HX-Request", "true")
			e.ServeHTTP(rec, req.WithContext(ctx))
			require.Equal(t, 200, rec.Code, rec.Body.String())
			remaining, err := owner.webhookRepo.GetByIDForProject(ctx, a.ID, rp.ID)
			require.NoError(t, err)
			require.Nil(t, remaining)
		})
	}
	t.Run("channel connections stay Local with remote project selected", func(t *testing.T) {
		key := service.SlackSettingTeamName
		require.NoError(t, local.Stores.Settings.Set(ctx, key, "Local team"))
		require.NoError(t, owner.remoteExecutor.Stores.Settings.Set(ctx, key, "Remote team"))
		req := httptest.NewRequest(http.MethodDelete, "/channels/bulk?project_id="+remoteid.ID(sid, rp.ID), strings.NewReader(`{"ids":["channel:slack"]}`)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, 200, rec.Code, rec.Body.String())
		value, err := local.Stores.Settings.Get(ctx, key)
		require.NoError(t, err)
		require.Empty(t, value)
		value, err = owner.remoteExecutor.Stores.Settings.Get(ctx, key)
		require.NoError(t, err)
		require.Equal(t, "Remote team", value)
	})

}
