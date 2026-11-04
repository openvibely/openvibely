package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProjectAccessWebSocketRevokesOnlyDeniedSubscription(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	access := service.NewProjectAccessService(tc.stores.ProjectAccess)
	tc.handler.SetProjectAccessService(access)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	org := tc.handler.organizationSvc
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	bob, err := org.Accept(ctx, invite, "bob", "bob-password-long", "")
	require.NoError(t, err)
	token, err := org.NewSession(ctx, bob.UserID, time.Hour)
	require.NoError(t, err)
	project := &models.Project{Name: "Restricted live fixture"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, access.SetMember(ownerCtx, project.ID, bob.UserID, "viewer"))
	server := httptest.NewServer(e)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	next := func(id, event string) {
		t.Helper()
		for {
			var frame liveFrame
			require.NoError(t, wsjson.Read(ctx, conn, &frame))
			if frame.ID == id {
				require.Equal(t, event, frame.Event)
				return
			}
		}
	}
	next("", "ready")
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "live", ID: "private", ProjectID: project.ID}))
	next("private", "subscribed")
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "live", ID: "shared", ProjectID: "default"}))
	next("shared", "subscribed")
	require.NoError(t, access.SetMember(ownerCtx, project.ID, bob.UserID, ""))
	next("private", "error")
	// The same socket remains usable for accessible projects.
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "live", ID: "still-shared", ProjectID: "default"}))
	next("still-shared", "subscribed")
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "execution", ID: "replay", ProjectID: project.ID, ExecID: "any-execution", Offset: 0}))
	next("replay", "error")
}

func TestProjectRevocationDiscardsBufferedFrames(t *testing.T) {
	stopped := false
	revoked := &liveSubscription{command: liveCommand{ProjectID: "private"}, stop: func() { stopped = true }}
	shared := &liveSubscription{command: liveCommand{ProjectID: "shared"}, stop: func() {}}
	subscriptions := map[string]*liveSubscription{"private": revoked, "shared": shared}
	var written []liveFrame
	write := func(f liveFrame) bool { written = append(written, f); return true }
	allowed := func(cmd liveCommand) bool { return cmd.ProjectID == "shared" }
	for i := 0; i < 3; i++ {
		require.True(t, dispatchAuthorizedLiveFrame(subscriptions, liveFrame{ID: "private", subscription: revoked, Event: "output", Data: "secret"}, allowed, write))
	}
	require.True(t, stopped)
	require.True(t, dispatchAuthorizedLiveFrame(subscriptions, liveFrame{ID: "shared", subscription: shared, Event: "output", Data: "shared output"}, allowed, write))
	require.Len(t, written, 2)
	require.Equal(t, "error", written[0].Event)
	require.Equal(t, "shared output", written[1].Data)
}
