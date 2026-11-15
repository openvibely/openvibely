package handler

import (
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestComputerStatusUsesExistingLiveWebSocket(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, "")
	svc := service.NewProjectComputerService(tc.handler.projectRepo, liveComputerFixture{})
	svc.SetBroadcaster(tc.handler.broadcaster)
	tc.handler.computerSvc = svc
	client := dialLive(t, server.URL)
	cmd := liveCommand{Op: "computer", ID: "computer", ProjectID: "default", TaskID: exec.TaskID, ViewerMode: "view"}
	client.send(t, cmd)
	client.next(t, cmd.ID, "subscribed")
	initial := client.next(t, cmd.ID, "computer_status").Data.(map[string]any)
	require.Equal(t, false, initial["enabled"])
	require.NoError(t, svc.SetEnabled(t.Context(), "default", true))
	client.next(t, cmd.ID, "computer_status")
	scoped, err := svc.ForTask(t.Context(), "default", exec.TaskID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), "default", true))
	for {
		state := client.next(t, cmd.ID, "computer_status").Data.(map[string]any)
		if state["enabled"] == true {
			require.Equal(t, exec.TaskID, state["task_id"])
			require.Contains(t, state, "ready")
			require.Contains(t, state, "handoff_kind")
			break
		}
	}
	// A replacement subscription receives current state without a new mutation.
	client.send(t, liveCommand{Op: "unsubscribe", ID: cmd.ID})
	cmd.ID = "reconnect"
	client.send(t, cmd)
	client.next(t, cmd.ID, "subscribed")
	require.Equal(t, true, client.next(t, cmd.ID, "computer_status").Data.(map[string]any)["enabled"])
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskDeleted, ProjectID: "default", TaskID: exec.TaskID})
	client.next(t, cmd.ID, "task_deleted")
	client.next(t, cmd.ID, "error")
}

func TestComputerStatusRecoversMissedNotification(t *testing.T) {
	tc, server := liveTestServer(t)
	svc := service.NewProjectComputerService(tc.handler.projectRepo, liveComputerFixture{})
	tc.handler.computerSvc = svc
	require.NoError(t, svc.SetEnabled(t.Context(), "default", false))
	client := dialLive(t, server.URL)
	cmd := liveCommand{Op: "computer", ID: "computer", ProjectID: "default"}
	client.send(t, cmd)
	client.next(t, cmd.ID, "subscribed")
	require.Equal(t, false, client.next(t, cmd.ID, "computer_status").Data.(map[string]any)["enabled"])
	// Bypass publication to simulate a dropped change notification.
	require.NoError(t, tc.handler.projectRepo.Computers().SetEnabled(t.Context(), "default", true))
	// The transport sends this pair upon the app-wide reconcile signal.
	client.send(t, liveCommand{Op: "unsubscribe", ID: cmd.ID})
	client.send(t, cmd)
	client.next(t, cmd.ID, "subscribed")
	require.Equal(t, true, client.next(t, cmd.ID, "computer_status").Data.(map[string]any)["enabled"])
}
