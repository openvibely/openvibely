package handler

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestLiveNotificationLossTriggersReconcile(t *testing.T) {
	for _, kind := range []string{"task", "chat", "file", "computer"} {
		t.Run(kind, func(t *testing.T) {
			tc, _ := liveTestServer(t)
			h := tc.handler
			h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			subscribed := make(chan struct{})
			release := make(chan struct{})
			frames := make(chan liveFrame, 256)
			done := make(chan struct{})
			cmd := liveCommand{ProjectID: "default", TaskID: "task", Op: "live"}
			if kind == "computer" {
				cmd.Op = "computer"
				cmd.TaskID = ""
			}
			go func() {
				defer close(done)
				send := func(f liveFrame) bool {
					if f.Event == "subscribed" {
						close(subscribed)
						select {
						case <-release:
						case <-ctx.Done():
							return false
						}
					}
					select {
					case frames <- f:
						return true
					case <-ctx.Done():
						return false
					}
				}
				if kind == "computer" {
					h.streamComputerStatus(ctx, cmd, send)
				} else {
					h.streamLive(ctx, cmd, send)
				}
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("worker did not stop")
				}
			}()
			select {
			case <-subscribed:
			case <-time.After(5 * time.Second):
				t.Fatal("subscribe timed out")
			}
			// The worker is blocked after subscribing. Fill its actual bounded queue.
			for i := 0; i < 100; i++ {
				switch kind {
				case "task", "computer":
					h.broadcaster.Publish(events.TaskEvent{ProjectID: "default", Type: events.TaskBoardUpdated})
				case "chat":
					h.chatBroadcaster.Publish(events.ChatEvent{ProjectID: "default", Type: events.ChatNewMessage})
				case "file":
					h.fileChangeBroadcaster.Publish(events.FileChangeEvent{TaskID: "task", Type: events.FileModified})
				}
			}
			close(release)
			timeout := time.After(5 * time.Second)
			for {
				select {
				case f := <-frames:
					if f.Event == "error" || f.Event == "retry" {
						t.Fatalf("unexpected failure: %+v", f)
					}
					if f.Event == "reconcile" {
						return
					}
				case <-timeout:
					t.Fatal("overflow did not trigger recovery")
				}
			}
		})
	}
}

func TestLiveReconcileRetainsOwnerCheckButIsConnectionWide(t *testing.T) {
	owner := &liveSubscription{stop: func() {}}
	replacement := &liveSubscription{stop: func() {}}
	subscriptions := map[string]*liveSubscription{"sub": owner}
	frame := liveFrame{ID: "sub", Event: "reconcile", subscription: owner}
	writes := 0
	write := func(f liveFrame) bool {
		writes++
		require.Empty(t, f.ID)
		require.Equal(t, "reconcile", f.Event)
		return true
	}
	require.True(t, dispatchLiveFrame(subscriptions, frame, write))
	require.Equal(t, 1, writes)
	subscriptions["sub"] = replacement
	require.True(t, dispatchLiveFrame(subscriptions, frame, write))
	require.Equal(t, 1, writes, "old subscription must not trigger recovery after replacement")
}

func TestLiveNormalDeliveryDoesNotReconcile(t *testing.T) {
	tc, _ := liveTestServer(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	frames := make(chan liveFrame, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tc.handler.streamLive(ctx, liveCommand{ProjectID: "default"}, func(f liveFrame) bool {
			select {
			case frames <- f:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("worker did not stop")
		}
	}()
	next := func() liveFrame {
		select {
		case f := <-frames:
			return f
		case <-time.After(5 * time.Second):
			t.Fatal("frame timeout")
			return liveFrame{}
		}
	}
	require.Equal(t, "subscribed", next().Event)
	for i := 0; i < 20; i++ {
		tc.handler.broadcaster.Publish(events.TaskEvent{ProjectID: "default", Type: events.TaskStatusChanged})
		require.Equal(t, string(events.TaskStatusChanged), next().Event)
	}
	cancel()
	<-done
	require.Empty(t, frames, "normal delivery must not request recovery")
}
