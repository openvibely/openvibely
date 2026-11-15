package handler

import (
	"context"
	"testing"
	"time"

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
	tc, _ := liveTestServer(t)
	svc := service.NewProjectComputerService(tc.handler.projectRepo, liveComputerFixture{})
	tc.handler.computerSvc = svc
	require.NoError(t, svc.SetEnabled(t.Context(), "default", false))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan liveFrame, 20)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tc.handler.streamComputerStatusEvery(ctx, liveCommand{Op: "computer", ProjectID: "default"}, func(f liveFrame) bool {
			select {
			case frames <- f:
				return true
			case <-ctx.Done():
				return false
			}
		}, 20*time.Millisecond)
	}()
	next := func() map[string]any {
		t.Helper()
		for {
			select {
			case f := <-frames:
				if f.Event == "computer_status" {
					return f.Data.(map[string]any)
				}
				require.NotEqual(t, "error", f.Event)
			case <-time.After(time.Second):
				t.Fatal("missing status snapshot")
				return nil
			}
		}
	}
	require.Equal(t, false, next()["enabled"])
	// Deliberately bypass publication: equivalent to losing its notification.
	require.NoError(t, tc.handler.projectRepo.Computers().SetEnabled(t.Context(), "default", true))
	require.Eventually(t, func() bool { return next()["enabled"] == true }, time.Second, time.Millisecond)
	cancel()
	<-done
}
