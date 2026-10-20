package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

// A subscription ID belongs to one view generation, not to an execution. Late
// frames from an unsubscribed view can therefore never modify its replacement.
type liveCommand struct {
	Op        string `json:"op"`
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	ExecID    string `json:"exec_id"`
	Offset    int    `json:"offset"`
}
type liveFrame struct {
	ID     string `json:"id,omitempty"`
	Event  string `json:"event"`
	Data   any    `json:"data,omitempty"`
	Start  int    `json:"start,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

type liveSubscription struct {
	id   string
	stop context.CancelFunc
}

// LiveWebSocket uses the normal session middleware and additionally checks the
// browser origin, including its scheme. Native windows require a one-use ticket.
func (h *Handler) LiveWebSocket(c echo.Context) error {
	if err := h.consumeNativeLiveTicket(c); err != nil {
		return err
	}
	r := c.Request()
	native := c.Get(nativeLiveContextKey) == true
	origin, err := url.Parse(r.Header.Get("Origin"))
	if !native && (err != nil || origin == nil || origin.Host != r.Host || origin.Scheme != c.Scheme() || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "") {
		return echo.NewHTTPError(http.StatusForbidden, "invalid WebSocket origin")
	}
	limits := h.liveLimits.WithDefaults()
	if h.liveSocketCount.Add(1) > int64(limits.Connections) {
		h.liveSocketCount.Add(-1)
		return echo.NewHTTPError(http.StatusServiceUnavailable, "live connection limit reached")
	}
	defer h.liveSocketCount.Add(-1)
	conn, err := websocket.Accept(c.Response(), r, &websocket.AcceptOptions{InsecureSkipVerify: native, Subprotocols: []string{"openvibely-live"}})
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4096)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	writeFrame := func(f liveFrame) bool {
		writeCtx, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		if wsjson.Write(writeCtx, conn, f) != nil {
			cancel()
			return false
		}
		return true
	}
	out := make(chan liveFrame, 128)
	// Never block a broadcaster or accumulate unbounded output for a slow window.
	// Disconnecting makes its next subscription replay from the last applied byte.
	send := func(f liveFrame) bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		select {
		case out <- f:
			return true
		default:
		}
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		select {
		case out <- f:
			return true
		case <-ctx.Done():
			return false
		case <-timer.C:
			cancel()
			return false
		}
	}
	commands := make(chan liveCommand, 16)
	go func() {
		defer cancel()
		for {
			var cmd liveCommand
			if wsjson.Read(ctx, conn, &cmd) != nil {
				return
			}
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	subscriptions := make(map[string]*liveSubscription)
	// Completed workers are retired without ending the connection.
	finished := make(chan *liveSubscription, 128)
	defer func() {
		for _, subscription := range subscriptions {
			subscription.stop()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastUpdate := ""
	ticks := 0
	authorized := func() bool {
		if !h.authEnabled() {
			return true
		}
		session := h.recognizeSession(r, time.Now())
		return session.localUser != nil || session.hostedClaims != nil
	}
	writeFrame(liveFrame{Event: "ready"})
	for {
		select {
		case <-ctx.Done():
			return nil
		case subscription := <-finished:
			if subscriptions[subscription.id] == subscription {
				subscription.stop()
				delete(subscriptions, subscription.id)
			}
		case cmd := <-commands:
			if !authorized() {
				return nil
			}
			if cmd.ID == "" || len(cmd.ID) > 128 {
				return nil
			}
			if cmd.Op == "unsubscribe" {
				if subscription := subscriptions[cmd.ID]; subscription != nil {
					subscription.stop()
					delete(subscriptions, cmd.ID)
				}
				continue
			}
			if _, exists := subscriptions[cmd.ID]; exists {
				continue
			}
			if len(subscriptions) >= limits.SubscriptionsPerConnection {
				writeFrame(liveFrame{ID: cmd.ID, Event: "error", Data: "subscription limit reached"})
				continue
			}
			if cmd.Op != "execution" && cmd.Op != "live" {
				return nil
			}
			if cmd.Offset < 0 || !h.liveSubscriptionAllowed(ctx, cmd) {
				writeFrame(liveFrame{ID: cmd.ID, Event: "error", Data: "subscription denied"})
				continue
			}
			subctx, stop := context.WithCancel(ctx)
			subscription := &liveSubscription{id: cmd.ID, stop: stop}
			subscriptions[cmd.ID] = subscription
			go func(cmd liveCommand) {
				defer func() {
					select {
					case finished <- subscription:
					case <-ctx.Done():
					}
				}()
				emit := func(f liveFrame) bool {
					if subctx.Err() != nil {
						return false
					}
					f.ID = cmd.ID
					return send(f)
				}
				if cmd.Op == "execution" {
					h.streamExecution(subctx, cmd, emit)
				} else {
					h.streamLive(subctx, cmd, emit)
				}
			}(cmd)
		case f := <-out:
			if !writeFrame(f) {
				return nil
			}
		case <-ticker.C:
			ticks++
			if ticks%15 == 0 {
				if !authorized() {
					return nil
				}
				// Ping runs alongside the reader; a stalled peer is bounded by the timeout.
				go func() {
					pingctx, stop := context.WithTimeout(ctx, 10*time.Second)
					defer stop()
					if conn.Ping(pingctx) != nil {
						cancel()
					}
				}()
			}
			if ticks%30 == 0 {
				writeFrame(liveFrame{Event: "reconcile"})
			}
			var snapshot any
			if h.updateCoordinator != nil && h.updateCoordinator.Visible() {
				snapshot = h.updateCoordinator.Snapshot()
			}
			encoded, _ := json.Marshal(snapshot)
			if string(encoded) != lastUpdate {
				lastUpdate = string(encoded)
				writeFrame(liveFrame{Event: "system_update", Data: snapshot})
			}
		}
	}
}

// Authentication is instance-wide in OpenVibely. Project/task membership is
// checked here on every subscription, rather than trusting browser filters.
func (h *Handler) liveSubscriptionAllowed(ctx context.Context, cmd liveCommand) bool {
	if cmd.ProjectID == "" || h.projectRepo == nil {
		return false
	}
	project, err := h.projectRepo.GetByID(ctx, cmd.ProjectID)
	if err != nil || project == nil {
		return false
	}
	taskID := cmd.TaskID
	if cmd.Op == "execution" {
		if h.execRepo == nil {
			return false
		}
		exec, err := h.execRepo.GetByID(ctx, cmd.ExecID)
		if err != nil || exec == nil {
			return false
		}
		taskID = exec.TaskID
	}
	if taskID != "" {
		if h.taskRepo == nil {
			return false
		}
		task, err := h.taskRepo.GetByID(ctx, taskID)
		return err == nil && task != nil && task.ProjectID == cmd.ProjectID
	}
	return cmd.Op == "live"
}

func (h *Handler) streamLive(ctx context.Context, cmd liveCommand, send func(liveFrame) bool) {
	// One live subscription can attach to several broadcasters; count it once,
	// across all projects and connections, and release it on every exit path.
	if h.liveSubscriptionCount.Add(1) > int64(h.liveLimits.WithDefaults().LiveSubscriptions) {
		h.liveSubscriptionCount.Add(-1)
		send(liveFrame{Event: "retry", Data: "live subscription limit reached"})
		return
	}
	defer h.liveSubscriptionCount.Add(-1)
	var tasks events.Subscriber
	var chats events.ChatSubscriber
	var files events.FileChangeSubscriber
	var err error
	if h.broadcaster != nil {
		tasks, err = h.broadcaster.SubscribeProject(cmd.ProjectID)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.broadcaster.Unsubscribe(tasks)
	}
	if h.chatBroadcaster != nil {
		chats, err = h.chatBroadcaster.SubscribeProject(cmd.ProjectID)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.chatBroadcaster.Unsubscribe(chats)
	}
	if h.fileChangeBroadcaster != nil && cmd.TaskID != "" {
		files, err = h.fileChangeBroadcaster.SubscribeTask(cmd.TaskID)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.fileChangeBroadcaster.Unsubscribe(files)
	}
	if !send(liveFrame{Event: "subscribed"}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-tasks:
			if !ok {
				return
			}
			if !send(liveFrame{Event: string(ev.Type), Data: ev}) {
				return
			}
		case ev, ok := <-chats:
			if !ok {
				return
			}
			if !send(liveFrame{Event: string(ev.Type), Data: ev}) {
				return
			}
		case ev, ok := <-files:
			if !ok {
				return
			}
			if !send(liveFrame{Event: string(ev.Type), Data: ev}) {
				return
			}
		}
	}
}

// Subscribe before reading persistence, closing the replay/live race. Hub events
// can precede persistence, so never move the delivered byte cursor backwards.
func (h *Handler) streamExecution(ctx context.Context, cmd liveCommand, send func(liveFrame) bool) {
	sub, unsubscribe, err := h.executionStreamHub.Subscribe(cmd.ExecID)
	if err != nil {
		send(liveFrame{Event: "retry", Data: "execution subscription unavailable"})
		return
	}
	defer unsubscribe()
	sent := cmd.Offset
	replay := func() bool {
		exec, err := h.execRepo.GetByID(ctx, cmd.ExecID)
		if err != nil || exec == nil {
			send(liveFrame{Event: "error", Data: "execution not found"})
			return true
		}
		if len(exec.Output) < sent && (exec.Status == models.ExecCompleted || exec.Status == models.ExecCancelled || exec.Status == models.ExecFailed) {
			if !send(liveFrame{Event: "reset", Data: exec.Output, Offset: len(exec.Output)}) {
				return true
			}
			sent = len(exec.Output)
		}
		if len(exec.Output) > sent {
			start := clampOffsetToUTF8Boundary(sent, exec.Output)
			// Bound frame/queue memory even for very large historical outputs.
			for start < len(exec.Output) {
				end := start + 32*1024
				if end > len(exec.Output) {
					end = len(exec.Output)
				}
				end = clampOffsetToUTF8Boundary(end, exec.Output)
				if !send(liveFrame{Event: "message", Data: exec.Output[start:end], Start: start, Offset: end}) {
					return true
				}
				start = end
				sent = end
			}
		}
		switch exec.Status {
		case models.ExecCompleted, models.ExecCancelled:
			send(liveFrame{Event: "done", Data: string(exec.Status), Offset: sent})
			return true
		case models.ExecFailed:
			send(liveFrame{Event: "error", Data: exec.ErrorMessage, Offset: sent})
			return true
		}
		return false
	}
	if replay() {
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if replay() {
				return
			}
		case ev, ok := <-sub:
			if !ok {
				sub = nil
				if replay() {
					return
				}
				continue
			}
			if ev.Type != events.ExecutionStreamDelta {
				if replay() {
					return
				}
				// Terminal hub events remain authoritative even if the final
				// status write failed; retain the original error/status contract.
				if ev.Type == events.ExecutionStreamError {
					send(liveFrame{Event: "error", Data: ev.Error, Offset: sent})
					return
				}
				if ev.Type == events.ExecutionStreamDone {
					status := ev.Status
					if status == "" {
						status = string(models.ExecCompleted)
					}
					send(liveFrame{Event: "done", Data: status, Offset: sent})
					return
				}
				continue
			}
			if ev.Offset <= sent {
				continue
			}
			start := ev.Offset - len(ev.Delta)
			if start > sent {
				if replay() {
					return
				}
				if start > sent {
					continue
				}
			}
			if ev.Offset <= sent {
				continue
			}
			skip := sent - start
			if skip < 0 || skip > len(ev.Delta) {
				continue
			}
			if !send(liveFrame{Event: "message", Data: ev.Delta[skip:], Start: sent, Offset: ev.Offset}) {
				return
			}
			sent = ev.Offset
		}
	}
}

// Kept with the transport because offsets are UTF-8 byte counts, not JS lengths.
func clampOffsetToUTF8Boundary(offset int, s string) int {
	if offset <= 0 {
		return 0
	}
	if offset >= len(s) {
		return len(s)
	}
	for offset > 0 && !utf8.RuneStart(s[offset]) {
		offset--
	}
	return offset
}
