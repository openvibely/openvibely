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
	client := dialLive(t, server.URL)
	client.send(t, liveCommand{Op: "live", ID: "computer", ProjectID: "default", TaskID: exec.TaskID})
	client.next(t, "computer", "subscribed")
	svc := service.NewProjectComputerService(tc.handler.projectRepo, liveComputerFixture{})
	svc.SetBroadcaster(tc.handler.broadcaster)
	require.NoError(t, svc.SetEnabled(t.Context(), "default", true))
	frame := client.next(t, "computer", string(events.ComputerStatusChanged))
	payload, ok := frame.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "default", payload["project_id"])
	require.Equal(t, "", payload["task_id"])
	scoped, err := svc.ForTask(t.Context(), "default", exec.TaskID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), "default", true))
	frame = client.next(t, "computer", string(events.ComputerStatusChanged))
	payload = frame.Data.(map[string]any)
	require.Equal(t, exec.TaskID, payload["task_id"])
	require.NotContains(t, payload, "message")
}
