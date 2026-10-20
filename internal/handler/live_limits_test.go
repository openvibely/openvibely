package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/livelimits"
	"github.com/stretchr/testify/require"
)

func TestLiveWebSocketConnectionLimitAndRecovery(t *testing.T) {
	tc, server := liveTestServerWithLimits(t, livelimits.Limits{Connections: 2})
	first := dialLive(t, server.URL)
	second := dialLive(t, server.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	if conn != nil {
		conn.CloseNow()
	}
	require.Error(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Equal(t, int64(2), tc.handler.liveSocketCount.Load())
	first.conn.CloseNow()
	require.Eventually(t, func() bool { return tc.handler.liveSocketCount.Load() == 1 }, 5*time.Second, time.Millisecond)
	replacement := dialLive(t, server.URL)
	require.Equal(t, int64(2), tc.handler.liveSocketCount.Load())
	second.conn.CloseNow()
	replacement.conn.CloseNow()
	require.Eventually(t, func() bool { return tc.handler.liveSocketCount.Load() == 0 }, 5*time.Second, time.Millisecond)

	// A failed HTTP upgrade must return its reserved connection slot too.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/events/ws", nil)
	require.NoError(t, err)
	req.Header.Set("Origin", server.URL)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	response.Body.Close()
	require.NotEqual(t, http.StatusSwitchingProtocols, response.StatusCode)
	require.Eventually(t, func() bool { return tc.handler.liveSocketCount.Load() == 0 }, 5*time.Second, time.Millisecond)
}

func TestLiveWebSocketCombinedSubscriptionLimit(t *testing.T) {
	for _, limit := range []int{4, livelimits.DefaultSubscriptionsPerConnection} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			tc, server := liveTestServerWithLimits(t, livelimits.Limits{SubscriptionsPerConnection: limit})
			client := dialLive(t, server.URL)
			client.send(t, liveCommand{Op: "live", ID: "project", ProjectID: "default"})
			client.next(t, "project", "subscribed")
			exec := createRunningChatStreamExecution(t, tc, "")
			for i := 0; i < limit-1; i++ {
				client.send(t, executionCommand(fmt.Sprint(i), exec, 0))
			}
			waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, limit-1)
			client.send(t, executionCommand("overflow", exec, 0))
			require.Equal(t, "subscription limit reached", client.next(t, "overflow", "error").Data)
			client.send(t, liveCommand{Op: "unsubscribe", ID: "0"})
			waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, limit-2)
			client.send(t, liveCommand{Op: "live", ID: "replacement", ProjectID: "default"})
			client.next(t, "replacement", "subscribed")
			require.Equal(t, int64(1), tc.handler.liveSocketCount.Load())
			client.conn.CloseNow()
			waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 0)
			require.Eventually(t, func() bool { return tc.handler.liveSubscriptionCount.Load() == 0 }, 5*time.Second, time.Millisecond)
		})
	}
}

func TestLiveWebSocketGlobalLiveLimitAcrossProjectsAndWindows(t *testing.T) {
	tc, server := liveTestServerWithLimits(t, livelimits.Limits{LiveSubscriptions: 2})
	other := createProject(t, tc.handler, "Other project")
	first, second, third := dialLive(t, server.URL), dialLive(t, server.URL), dialLive(t, server.URL)
	first.send(t, liveCommand{Op: "live", ID: "first", ProjectID: "default"})
	first.next(t, "first", "subscribed")
	second.send(t, liveCommand{Op: "live", ID: "second", ProjectID: other.ID})
	second.next(t, "second", "subscribed")
	third.send(t, liveCommand{Op: "live", ID: "overflow", ProjectID: "default"})
	third.next(t, "overflow", "retry")
	require.Equal(t, int64(2), tc.handler.liveSubscriptionCount.Load())
	first.send(t, liveCommand{Op: "unsubscribe", ID: "first"})
	require.Eventually(t, func() bool { return tc.handler.liveSubscriptionCount.Load() == 1 }, 5*time.Second, time.Millisecond)
	third.send(t, liveCommand{Op: "live", ID: "replacement", ProjectID: "default"})
	third.next(t, "replacement", "subscribed")
	require.Equal(t, int64(2), tc.handler.liveSubscriptionCount.Load())
	first.conn.CloseNow()
	second.conn.CloseNow()
	third.conn.CloseNow()
	require.Eventually(t, func() bool {
		return tc.handler.liveSubscriptionCount.Load() == 0 && tc.handler.broadcaster.SubscriberCount() == 0 && tc.handler.chatBroadcaster.SubscriberCount() == 0
	}, 5*time.Second, time.Millisecond)
}

func TestLiveWebSocketLiveBudgetReleasedOnBroadcasterFailure(t *testing.T) {
	tc, server := liveTestServerWithLimits(t, livelimits.Limits{LiveSubscriptions: 1})
	// Force the second broadcaster registration to fail, after the first succeeds.
	blocker, err := tc.handler.chatBroadcaster.Subscribe()
	require.NoError(t, err)
	client := dialLive(t, server.URL)
	client.send(t, liveCommand{Op: "live", ID: "blocked", ProjectID: "default"})
	client.next(t, "blocked", "retry")
	require.Eventually(t, func() bool {
		return tc.handler.liveSubscriptionCount.Load() == 0 && tc.handler.broadcaster.SubscriberCount() == 0
	}, 5*time.Second, time.Millisecond)
	tc.handler.chatBroadcaster.Unsubscribe(blocker)
	client.send(t, liveCommand{Op: "live", ID: "replacement", ProjectID: "default"})
	client.next(t, "replacement", "subscribed")
}

func TestLiveWebSocketGlobalExecutionBudgetAndCompletionRecovery(t *testing.T) {
	tc, server := liveTestServerWithLimits(t, livelimits.Limits{ExecutionSubscriptions: 2})
	first, second := dialLive(t, server.URL), dialLive(t, server.URL)
	exec := createRunningChatStreamExecution(t, tc, "")
	first.send(t, executionCommand("one", exec, 0))
	second.send(t, executionCommand("two", exec, 0))
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 2)
	second.send(t, executionCommand("overflow", exec, 0))
	second.next(t, "overflow", "retry")
	// The execution budget does not consume the separate live budget.
	second.send(t, liveCommand{Op: "live", ID: "project", ProjectID: "default"})
	second.next(t, "project", "subscribed")
	tc.handler.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: exec.ID, Type: events.ExecutionStreamDone})
	first.next(t, "one", "done")
	second.next(t, "two", "done")
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 0)
	followup := createRunningChatStreamExecution(t, tc, "")
	first.send(t, executionCommand("followup", followup, 0))
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 1)
	require.Equal(t, int64(2), tc.handler.liveSocketCount.Load())
}
