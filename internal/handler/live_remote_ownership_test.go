package handler

import (
	"net/http/cookiejar"
	"net/url"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
)

func TestLiveHubExportsOnlyOwnedEventsWhileBrowserReceivesRemoteEvents(t *testing.T) {
	tc, srv := liveTestServer(t)
	// This test isolates ownership filtering from project authorization.
	tc.handler.projectAccessSvc = nil
	browser := dialLive(t, srv.URL)
	browser.send(t, liveCommand{Op: "live", ID: "browser", ProjectID: models.AllProjectsID})
	browser.next(t, "browser", "subscribed")

	tasks := make(chan events.TaskEvent, 8)
	chats := make(chan events.ChatEvent, 8)
	hub := &remote.Hub{
		OnTask: func(e events.TaskEvent) { tasks <- e },
		OnChat: func(e events.ChatEvent) { chats <- e },
	}
	defer hub.Shutdown()
	base, err := url.Parse(srv.URL)
	require.NoError(t, err)
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	const peer = "0123456789abcdef"
	hub.Connect("viewer", peer, base, jar, nil)
	require.Eventually(t, func() bool {
		return tc.handler.broadcaster.SubscriberCount() == 2 && tc.handler.chatBroadcaster.SubscriberCount() == 2
	}, 5*time.Second, 10*time.Millisecond)

	importedProject := remoteid.ID("fedcba9876543210", "project")
	// Publish imported events first, then local markers on the same ordered queues.
	// The hub's first delivery must be the local marker; the browser gets both.
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: importedProject, TaskID: "imported", RemoteUserID: "owner", Status: "completed"})
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: "default", TaskID: "local", Status: "completed"})
	tc.handler.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatResponseDone, ProjectID: models.AllProjectsID, ExecID: "imported", RemoteUserID: "owner"})
	tc.handler.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatResponseDone, ProjectID: models.AllProjectsID, ExecID: "local"})

	seen := map[string]int{}
	for i := 0; i < 4; {
		frame := browser.nextAny(t)
		if frame.Event == "system_update" {
			continue
		}
		i++
		require.Contains(t, []string{string(events.TaskStatusChanged), string(events.ChatResponseDone)}, frame.Event)
		data, ok := frame.Data.(map[string]any)
		require.True(t, ok)
		seen[data["project_id"].(string)]++
	}
	require.Equal(t, map[string]int{importedProject: 1, "default": 1, models.AllProjectsID: 2}, seen)
	select {
	case e := <-tasks:
		require.Equal(t, remoteid.ID(peer, "local"), e.TaskID)
	case <-time.After(5 * time.Second):
		t.Fatal("owning server's completion was not exported")
	}
	select {
	case e := <-chats:
		require.Equal(t, remoteid.ID(peer, "local"), e.ExecID)
	case <-time.After(5 * time.Second):
		t.Fatal("owning server's chat event was not exported")
	}
}
