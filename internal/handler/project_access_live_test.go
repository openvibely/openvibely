package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestProjectAccessWebSocketRevokesOnlyDeniedSubscription(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	tc.handler.broadcaster = events.NewBroadcaster()
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

func TestProjectAccessChangeEndsOnlyThatUsersSubscriptions(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	tc.handler.broadcaster = events.NewBroadcaster()
	access := service.NewProjectAccessService(tc.stores.ProjectAccess)
	tc.handler.SetProjectAccessService(access)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	org := tc.handler.organizationSvc
	ownerCtx := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	project := &models.Project{Name: "Shared live fixture"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	server := httptest.NewServer(e)
	defer server.Close()
	type client struct {
		conn  *websocket.Conn
		token string
	}
	dial := func(name string) client {
		invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
		require.NoError(t, err)
		user, err := org.Accept(ctx, invite, name, name+"-password-long", "")
		require.NoError(t, err)
		require.NoError(t, access.SetMember(ownerCtx, project.ID, user.UserID, "viewer"))
		token, err := org.NewSession(ctx, user.UserID, time.Hour)
		require.NoError(t, err)
		conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
		require.NoError(t, err)
		t.Cleanup(func() { conn.CloseNow() })
		return client{conn: conn, token: token}
	}
	next := func(c client, id string) liveFrame {
		t.Helper()
		for {
			var frame liveFrame
			require.NoError(t, wsjson.Read(ctx, c.conn, &frame))
			if frame.ID == id {
				return frame
			}
		}
	}
	bob, carol := dial("bob"), dial("carol")
	for _, c := range []client{bob, carol} {
		require.Equal(t, "ready", next(c, "").Event)
		require.NoError(t, wsjson.Write(ctx, c.conn, liveCommand{Op: "live", ID: "watch", ProjectID: project.ID}))
		require.Equal(t, "subscribed", next(c, "watch").Event)
	}
	bobUser, err := org.Session(ctx, bob.token)
	require.NoError(t, err)
	require.NoError(t, access.SetMember(ownerCtx, project.ID, bobUser.UserID, ""))
	denied := next(bob, "watch")
	require.Equal(t, "error", denied.Event)
	require.Equal(t, "access blocked", denied.Data)
	require.NoError(t, wsjson.Write(ctx, bob.conn, liveCommand{Op: "live", ID: "again", ProjectID: project.ID}))
	require.Equal(t, "error", next(bob, "again").Event, "a removed user cannot subscribe again")

	// Carol's subscription is untouched: a broadcast still reaches her.
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: project.ID, TaskID: "still-visible"})
	frame := next(carol, "watch")
	require.Equal(t, string(events.TaskStatusChanged), frame.Event)

	// Logout closes that user's socket.
	require.NoError(t, org.Logout(ctx, carol.token))
	readCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	for {
		var f liveFrame
		if err := wsjson.Read(readCtx, carol.conn, &f); err != nil {
			require.NoError(t, readCtx.Err(), "logout must close the socket, not wait for a timeout")
			break
		}
	}
}

func TestLiveFramesDoNoAccessQueries(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	sessions := &countingSessionStore{OrganizationStore: tc.handler.organizationSvc.Store}
	tc.handler.organizationSvc.Store = sessions
	tc.handler.broadcaster = events.NewBroadcaster()
	counting := &countingAccessStore{ProjectAccessStore: tc.stores.ProjectAccess}
	tc.handler.SetProjectAccessService(service.NewProjectAccessService(counting))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	token, err := tc.handler.organizationSvc.NewSession(ctx, models.BootstrapUserID, time.Hour)
	require.NoError(t, err)
	server := httptest.NewServer(e)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	read := func(id string) liveFrame {
		for {
			var frame liveFrame
			require.NoError(t, wsjson.Read(ctx, conn, &frame))
			if frame.ID == id {
				return frame
			}
		}
	}
	read("")
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "live", ID: "all", ProjectID: models.AllProjectsID}))
	require.Equal(t, "subscribed", read("all").Event)
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: "default", TaskID: "first"})
	read("all")
	before := counting.roles.Load()
	sessionBefore := sessions.calls.Load()
	for i := 0; i < 20; i++ {
		tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: "default", TaskID: "next"})
		read("all")
	}
	// Cross the removed one-second authorization tick too.
	timer := time.NewTimer(1200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Equal(t, before, counting.roles.Load(), "frames and idle ticks must not recheck access")
	require.Equal(t, sessionBefore, sessions.calls.Load(), "frames must not reload the session")
}

type countingAccessStore struct {
	persistence.ProjectAccessStore
	roles atomic.Int64
}

func (s *countingAccessStore) Role(ctx context.Context, userID, projectID string) (string, error) {
	s.roles.Add(1)
	return s.ProjectAccessStore.Role(ctx, userID, projectID)
}

type countingSessionStore struct {
	persistence.OrganizationStore
	calls atomic.Int64
}

func (s *countingSessionStore) SessionUser(ctx context.Context, hash string, now int64) (*models.OrgUser, error) {
	s.calls.Add(1)
	return s.OrganizationStore.SessionUser(ctx, hash, now)
}

func TestDisabledUserLiveConnectionCloses(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	org := tc.handler.organizationSvc
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "disable-live", "long-password-value", "")
	require.NoError(t, err)
	token, err := org.NewSession(ctx, user.UserID, time.Hour)
	require.NoError(t, err)
	server := httptest.NewServer(e)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var frame liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &frame))
	require.Equal(t, "ready", frame.Event)
	require.NoError(t, org.Store.ChangeMember(ctx, models.BootstrapUserID, user.UserID, "member", true))
	org.NotifyUser(user.UserID)
	for wsjson.Read(ctx, conn, &frame) == nil {
	}
	require.NoError(t, ctx.Err(), "disabled account should close immediately")
}
