package handler

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/buildinfo"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/update"
	"github.com/stretchr/testify/require"
)

type liveTestClient struct {
	conn   *websocket.Conn
	frames chan liveFrame
	ctx    context.Context
}

func dialLive(t testing.TB, serverURL string) *liveTestClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, strings.Replace(serverURL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {serverURL}}})
	require.NoError(t, err)
	conn.SetReadLimit(64 * 1024)
	t.Cleanup(func() { conn.CloseNow() })
	client := &liveTestClient{conn: conn, ctx: ctx, frames: make(chan liveFrame, 1024)}
	go func() {
		defer close(client.frames)
		for {
			var f liveFrame
			if wsjson.Read(ctx, conn, &f) != nil {
				return
			}
			select {
			case client.frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	client.next(t, "", "ready")
	return client
}
func (c *liveTestClient) send(t testing.TB, cmd liveCommand) {
	t.Helper()
	require.NoError(t, wsjson.Write(c.ctx, c.conn, cmd))
}
func (c *liveTestClient) next(t testing.TB, id, event string) liveFrame {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f, ok := <-c.frames:
			require.True(t, ok, "connection closed waiting for %s/%s", id, event)
			if f.ID == id && f.Event == event {
				return f
			}
			if f.Event == "error" {
				t.Fatalf("unexpected error: %+v", f)
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for %s/%s", id, event)
		}
	}
}
func liveTestServer(t *testing.T) (*TestContext, *httptest.Server) {
	tc := NewStoreTestContext(t)
	tc.handler.SetExecutionStreamHub(events.NewExecutionStreamHub())
	tc.handler.broadcaster = events.NewBroadcaster()
	tc.handler.chatBroadcaster = events.NewChatBroadcaster()
	tc.handler.fileChangeBroadcaster = events.NewFileChangeBroadcaster()
	e := echo.New()
	e.Use(tc.handler.AuthMiddleware())
	e.GET("/events/ws", tc.handler.LiveWebSocket)
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)
	return tc, server
}
func executionCommand(id string, exec *models.Execution, offset int) liveCommand {
	return liveCommand{Op: "execution", ID: id, ProjectID: "default", ExecID: exec.ID, Offset: offset}
}

func TestLiveWebSocketConcurrentExecutionsCompletionCancellationAndFollowup(t *testing.T) {
	tc, server := liveTestServer(t)
	client := dialLive(t, server.URL)
	client.send(t, liveCommand{Op: "live", ID: "live", ProjectID: "default"})
	client.next(t, "live", "subscribed")
	chatExecution := createRunningChatStreamExecution(t, tc, "")
	task := createTask(t, tc.handler, "default", "Task output", func(task *models.Task) { task.Category = models.CategoryActive; task.Status = models.StatusRunning })
	taskExecution := createExec(t, tc.handler, task.ID, chatExecution.AgentConfigID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	executions := []*models.Execution{chatExecution, taskExecution}
	for i, exec := range executions {
		client.send(t, executionCommand(fmt.Sprint(i), exec, 0))
	}
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 2)
	for i, exec := range executions {
		text := fmt.Sprintf("execution %d 日本語\n", i)
		tc.handler.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: exec.ID, Type: events.ExecutionStreamDelta, Delta: text, Offset: len(text)})
		frame := client.next(t, fmt.Sprint(i), "message")
		require.Equal(t, text, frame.Data)
		require.Equal(t, len(text), frame.Offset)
	}
	for i, exec := range executions {
		status := models.ExecCompleted
		if i == 1 {
			status = models.ExecCancelled
		}
		output := fmt.Sprintf("execution %d 日本語\nfinal", i)
		require.NoError(t, tc.execRepo.Complete(context.Background(), exec.ID, status, output, "", 0, 0))
		tc.handler.executionStreamHub.CloseTerminal(exec.ID, status, "")
		require.Equal(t, "final", client.next(t, fmt.Sprint(i), "message").Data)
		require.Equal(t, string(status), client.next(t, fmt.Sprint(i), "done").Data)
	}
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 0)
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: "default", TaskID: executions[0].TaskID, Status: "completed"})
	client.next(t, "live", "task_status_changed")
	followup := createExec(t, tc.handler, executions[0].TaskID, executions[0].AgentConfigID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	require.NoError(t, tc.execRepo.FinalizeOutput(context.Background(), followup.ID, "follow-up"))
	client.send(t, executionCommand("followup", followup, 0))
	require.Equal(t, "follow-up", client.next(t, "followup", "message").Data)
	client.send(t, liveCommand{Op: "unsubscribe", ID: "followup"})
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 0)
	// The same physical socket accepts another run after completion and unsubscribe.
	client.send(t, executionCommand("new-view", followup, len("follow-")))
	require.Equal(t, "up", client.next(t, "new-view", "message").Data)
}

func TestLiveWebSocketReconnectReplayAndIndependentWindows(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, "héllo")
	first := dialLive(t, server.URL)
	second := dialLive(t, server.URL)
	first.send(t, executionCommand("first", exec, 0))
	second.send(t, executionCommand("second", exec, 0))
	require.Equal(t, "héllo", first.next(t, "first", "message").Data)
	second.next(t, "second", "message")
	first.conn.CloseNow()
	require.NoError(t, tc.execRepo.FinalizeOutput(context.Background(), exec.ID, "héllo world"))
	tc.handler.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: exec.ID, Type: events.ExecutionStreamDelta, Delta: " world", Offset: len("héllo world")})
	require.Equal(t, " world", second.next(t, "second", "message").Data)
	recovered := dialLive(t, server.URL)
	recovered.send(t, executionCommand("recovered", exec, len("héllo")))
	frame := recovered.next(t, "recovered", "message")
	require.Equal(t, " world", frame.Data)
	require.Equal(t, len("héllo"), frame.Start)
	require.NoError(t, tc.execRepo.Complete(context.Background(), exec.ID, models.ExecCancelled, "héllo world!", "", 0, 0))
	tc.handler.executionStreamHub.CloseTerminal(exec.ID, models.ExecCancelled, "")
	recovered.next(t, "recovered", "message")
	require.Equal(t, "cancelled", recovered.next(t, "recovered", "done").Data)
	// A fresh server-side hub has no replay log; persistence still recovers completion.
	tc.handler.SetExecutionStreamHub(events.NewExecutionStreamHub())
	restarted := dialLive(t, server.URL)
	restarted.send(t, executionCommand("restart", exec, len("héllo")))
	require.Equal(t, " world!", restarted.next(t, "restart", "message").Data)
	restarted.next(t, "restart", "done")
}

func TestLiveWebSocketGapRecoveryAndTerminalFailure(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, "")
	client := dialLive(t, server.URL)
	client.send(t, executionCommand("run", exec, 0))
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 1)
	// A lost hub delta followed by another delta before persistence catches up.
	tc.handler.executionStreamHub.Publish(events.ExecutionStreamEvent{ExecID: exec.ID, Type: events.ExecutionStreamDelta, Delta: "tail", Offset: 11})
	require.NoError(t, tc.execRepo.FinalizeOutput(context.Background(), exec.ID, "missed tail"))
	require.Equal(t, "missed tail", client.next(t, "run", "message").Data)
	require.NoError(t, tc.execRepo.Complete(context.Background(), exec.ID, models.ExecFailed, "missed tail!", "line one\nline two", 0, 0))
	tc.handler.executionStreamHub.CloseTerminal(exec.ID, models.ExecFailed, "line one\nline two")
	require.Equal(t, "!", client.next(t, "run", "message").Data)
	require.Equal(t, "line one\nline two", client.next(t, "run", "error").Data)
}

func TestLiveWebSocketRejectsOriginAndUnauthorizedSubscriptions(t *testing.T) {
	tc, server := liveTestServer(t)
	for _, origin := range []string{"", "null", "https://evil.example", strings.Replace(server.URL, "http:", "https:", 1)} {
		conn, response, err := websocket.Dial(context.Background(), strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
		if conn != nil {
			conn.CloseNow()
		}
		require.Error(t, err)
		require.Equal(t, http.StatusForbidden, response.StatusCode)
	}
	exec := createRunningChatStreamExecution(t, tc, "")
	other := createProject(t, tc.handler, "other")
	client := dialLive(t, server.URL)
	bad := executionCommand("foreign", exec, 0)
	bad.ProjectID = other.ID
	client.send(t, bad)
	require.Equal(t, "subscription denied", client.next(t, "foreign", "error").Data)
	client.send(t, liveCommand{Op: "live", ID: "foreign-task", ProjectID: other.ID, TaskID: exec.TaskID})
	client.next(t, "foreign-task", "error")
	client.send(t, liveCommand{Op: "execution", ID: "missing", ProjectID: "default", ExecID: "absent"})
	client.next(t, "missing", "error")
	require.Equal(t, 0, tc.handler.executionStreamHub.SubscriberCount())
	// Rejection affects only that subscription, not valid subscriptions on the socket.
	client.send(t, executionCommand("valid", exec, 0))
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 1)
}

func TestLiveWebSocketScopedNotificationsAndFiles(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, "")
	client := dialLive(t, server.URL)
	client.send(t, liveCommand{Op: "live", ID: "project", ProjectID: "default"})
	client.next(t, "project", "subscribed")
	require.Equal(t, 0, tc.handler.fileChangeBroadcaster.SubscriberCount())
	client.send(t, liveCommand{Op: "unsubscribe", ID: "project"})
	client.send(t, liveCommand{Op: "live", ID: "task", ProjectID: "default", TaskID: exec.TaskID})
	client.next(t, "task", "subscribed")
	tc.handler.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: "foreign"})
	tc.handler.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: "default", ExecID: exec.ID})
	client.next(t, "task", "chat_new_message")
	tc.handler.fileChangeBroadcaster.Publish(events.FileChangeEvent{Type: events.FileModified, TaskID: exec.TaskID})
	client.next(t, "task", "file_modified")
	// System-update state is on this connection even with an execution subscribed.
	client.send(t, executionCommand("exec", exec, 0))
	client.next(t, "", "system_update")
}

func TestLiveWebSocketLargeReplayAndRemovedRoutes(t *testing.T) {
	tc, server := liveTestServer(t)
	output := strings.Repeat("日本語\n", 600000)
	exec := createChatStreamExecution(t, tc, models.ExecCompleted, models.StatusCompleted, output, "")
	client := dialLive(t, server.URL)
	client.send(t, executionCommand("large", exec, 0))
	var got strings.Builder
	for {
		f := client.nextAny(t)
		if f.ID != "large" {
			continue
		}
		if f.Event == "done" {
			break
		}
		require.Equal(t, "message", f.Event)
		require.Equal(t, got.Len(), f.Start)
		got.WriteString(f.Data.(string))
	}
	require.Equal(t, output, got.String())
	_, e, _ := setupTestHandler(t)
	for _, path := range []string{"/events/live", "/events/chat/unused", "/api/system/update/events"} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}
func (c *liveTestClient) nextAny(t testing.TB) liveFrame {
	t.Helper()
	select {
	case f, ok := <-c.frames:
		require.True(t, ok)
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("frame timeout")
		return liveFrame{}
	}
}

func TestLiveWebSocketSlowConsumerIsDisconnected(t *testing.T) {
	if testing.Short() {
		t.Skip("backpressure timeout regression")
	}
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, strings.Repeat("x", 32<<20))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var ready liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &ready))
	require.NoError(t, wsjson.Write(ctx, conn, executionCommand("slow", exec, 0)))
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 1)
	// Deliberately stop reading. The writer and bounded queue must release all
	// execution subscriptions instead of retaining this window indefinitely.
	require.Eventually(t, func() bool { return tc.handler.executionStreamHub.SubscriberCount() == 0 }, 12*time.Second, 20*time.Millisecond)
}

func TestLiveWebSocketTerminalReplayResetsUnpersistedTail(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createChatStreamExecution(t, tc, models.ExecCancelled, models.StatusCancelled, "durable", "")
	client := dialLive(t, server.URL)
	client.send(t, executionCommand("restart", exec, len("durable lost tail")))
	frame := client.next(t, "restart", "reset")
	require.Equal(t, "durable", frame.Data)
	require.Equal(t, len("durable"), frame.Offset)
	require.Equal(t, "cancelled", client.next(t, "restart", "done").Data)
}

func TestLiveWebSocketSystemUpdateProgressSharesExecutionConnection(t *testing.T) {
	tc, server := liveTestServer(t)
	coordinator := update.NewCoordinator(nil, update.CurrentBuild{Build: buildinfo.Build{Version: "1.0.0"}, Distribution: "docker"}, "stable", nil, nil, true, "", nil)
	var state atomic.Value
	state.Store(update.ManagedUpdateState{Active: true, State: update.StateWaitingForIdle, DesiredVersion: "1.2.3"})
	coordinator.SetManagedStateProvider(func() update.ManagedUpdateState { return state.Load().(update.ManagedUpdateState) })
	tc.handler.SetUpdateCoordinator(coordinator)
	exec := createRunningChatStreamExecution(t, tc, "output")
	client := dialLive(t, server.URL)
	client.send(t, executionCommand("exec", exec, 0))
	require.Equal(t, "output", client.next(t, "exec", "message").Data)
	snapshot := client.next(t, "", "system_update").Data.(map[string]any)
	require.Equal(t, string(update.StateWaitingForIdle), snapshot["state"])
	state.Store(update.ManagedUpdateState{Active: true, State: update.StateRestarting, DesiredVersion: "1.2.3"})
	snapshot = client.next(t, "", "system_update").Data.(map[string]any)
	require.Equal(t, string(update.StateRestarting), snapshot["state"])
	require.Equal(t, int32(1), tc.handler.liveSocketCount.Load())
}

func TestLiveWebSocketHubErrorReplaysDurableTailBeforeExactError(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, "durable tail")
	client := dialLive(t, server.URL)
	client.send(t, executionCommand("exec", exec, len("durable ")))
	require.Equal(t, "tail", client.next(t, "exec", "message").Data)
	tc.handler.executionStreamHub.Close(exec.ID, events.ExecutionStreamEvent{ExecID: exec.ID, Type: events.ExecutionStreamError, Error: "final write failed\nretry later"})
	require.Equal(t, "final write failed\nretry later", client.next(t, "exec", "error").Data)
}

func TestLiveWebSocketServerRestartRecoversFromPersistence(t *testing.T) {
	tc, server := liveTestServer(t)
	exec := createRunningChatStreamExecution(t, tc, "before")
	client := dialLive(t, server.URL)
	client.send(t, executionCommand("before", exec, 0))
	client.next(t, "before", "message")
	client.conn.CloseNow()
	waitForExecutionStreamSubscribers(t, tc.handler.executionStreamHub, 0)
	address := server.Listener.Addr().String()
	server.Close()
	require.NoError(t, tc.execRepo.Complete(context.Background(), exec.ID, models.ExecCompleted, "before after restart", "", 0, 0))
	// New handler and hub, same database and listening address: there is no
	// in-memory stream state available to the replacement server.
	h := &Handler{execRepo: tc.handler.execRepo, taskRepo: tc.handler.taskRepo, projectRepo: tc.handler.projectRepo, executionStreamHub: events.NewExecutionStreamHub()}
	e := echo.New()
	e.GET("/events/ws", h.LiveWebSocket)
	listener, err := net.Listen("tcp", address)
	require.NoError(t, err)
	restarted := httptest.NewUnstartedServer(e)
	restarted.Listener.Close()
	restarted.Listener = listener
	restarted.Start()
	defer restarted.Close()
	recovered := dialLive(t, server.URL)
	recovered.send(t, executionCommand("after", exec, len("before")))
	require.Equal(t, " after restart", recovered.next(t, "after", "message").Data)
	require.Equal(t, "completed", recovered.next(t, "after", "done").Data)
}
