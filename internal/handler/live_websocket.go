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
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/repokey"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// A subscription ID belongs to one view generation, not to an execution. Late
// frames from an unsubscribed view can therefore never modify its replacement.
type liveCommand struct {
	InstanceID string `json:"instance_id,omitempty"`
	Handle     string `json:"handle,omitempty"`
	Op         string `json:"op"`
	ID         string `json:"id"`
	ProjectID  string `json:"project_id"`
	TaskID     string `json:"task_id"`
	ExecID     string `json:"exec_id"`
	Offset     int    `json:"offset"`
	ViewerID   string `json:"viewer_id,omitempty"`
	ViewerMode string `json:"viewer_mode,omitempty"`
	Native     bool   `json:"native,omitempty"`
	// Read carries an "op":"read" store call from a desktop/hub client.
	Read *remote.Read `json:"read,omitempty"`
	// Action carries an "op":"action" server action from a desktop/hub client.
	Action *remote.Action `json:"action,omitempty"`
}
type liveFrame struct {
	// Internal owner identity prevents an old worker's terminal frame from
	// retiring a replacement that reused the same wire ID.
	subscription *liveSubscription
	finished     bool   // Internal queue marker; never written to the socket.
	ID           string `json:"id,omitempty"`
	Event        string `json:"event"`
	Data         any    `json:"data,omitempty"`
	Start        int    `json:"start,omitempty"`
	Offset       int    `json:"offset,omitempty"`
}

type liveSubscription struct {
	command liveCommand
	stop    context.CancelFunc
}

// dispatchLiveFrame runs only on the socket loop. Completion shares the output
// queue so it cannot overtake a worker's final output. Cancelled/replaced owners
// are filtered at delivery time, including frames queued before cancellation.
func dispatchLiveFrame(subscriptions map[string]*liveSubscription, frame liveFrame, write func(liveFrame) bool) bool {
	if owner := frame.subscription; owner != nil {
		if subscriptions[frame.ID] != owner {
			return true
		}
		if frame.finished || frame.Event == "retry" || frame.Event == "done" || frame.Event == "error" {
			// Retire before writing: the client can retry immediately on receipt.
			owner.stop()
			delete(subscriptions, frame.ID)
		}
	}
	if frame.finished {
		return true
	}
	if frame.Event == "reconcile" {
		frame.ID = ""
	}
	return write(frame)
}

// liveProjectVisibility is loaded at subscription admission and access changes.
type liveProjectVisibility map[string]bool

func (h *Handler) liveEventVisible(ctx context.Context, cache liveProjectVisibility, frame liveFrame) bool {
	if h.projectAccessSvc == nil {
		return true
	}
	var projectID, remoteUser string
	switch event := frame.Data.(type) {
	case events.TaskEvent:
		if event.Type == events.AccountUsageUpdated {
			// Global delivery is authorized by subscription admission and revocation.
			return frame.subscription != nil && frame.subscription.command.Op == "account_usage"
		}
		projectID, remoteUser = event.ProjectID, event.RemoteUserID
	case events.ChatEvent:
		if event.ProjectID == models.AllProjectsID {
			p, ok := models.PrincipalFromContext(ctx)
			return !ok || (event.AudienceUserID != "" && event.AudienceUserID == p.UserID)
		}
		projectID, remoteUser = event.ProjectID, event.RemoteUserID
	default:
		return true
	}
	if projectID == "" {
		return false
	}
	if _, ok := remoteid.ServerOf(projectID); ok {
		// The remote filtered this event for the account behind the delivering socket.
		return remoteUser != "" && remoteUser == principalUserID(ctx)
	}
	return cache[projectID]
}

// Refresh only on subscription admission or an access-change notification.
func (h *Handler) refreshLiveVisibility(ctx context.Context, visible liveProjectVisibility) error {
	clear(visible)
	if h.projectAccessSvc == nil {
		return nil
	}
	projects, err := h.projectRepo.ListSelectorOptions(ctx)
	if err != nil {
		return err
	}
	for _, project := range projects {
		visible[project.ID] = h.projectAccessSvc.Require(ctx, project.ID, authz.View) == nil
	}
	return nil
}

// LiveWebSocket uses the normal session middleware and additionally checks the
// browser origin, including its scheme. Native windows require a one-use ticket.
func (h *Handler) LiveWebSocket(c echo.Context) error {
	if err := h.consumeNativeLiveTicket(c); err != nil {
		return err
	}
	r := c.Request()
	native := c.Get(nativeLiveContextKey) == true
	// Proxies such as Traefik report ws/wss for upgraded requests. Browser
	// origins still use the page's http/https scheme, including behind TLS
	// termination. Normalize only the transport scheme, not the supplied origin.
	scheme := c.Scheme()
	switch scheme {
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	}
	origin, err := url.Parse(r.Header.Get("Origin"))
	if !native && (err != nil || origin == nil || origin.Host != r.Host || origin.Scheme != scheme || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "") {
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
	conn.SetReadLimit(8 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	authorized := func() bool {
		if !h.authEnabled() {
			return true
		}
		session := h.recognizeSession(r, time.Now())
		return session.localUser != nil || session.hostedClaims != nil
	}
	visible := liveProjectVisibility{}
	writeFrame := func(f liveFrame) bool {
		if !h.liveEventVisible(ctx, visible, f) {
			return true
		}
		// Only a dead peer ends the socket; a slow one applies backpressure.
		writeCtx, stop := context.WithTimeout(ctx, 2*time.Minute)
		defer stop()
		if wsjson.Write(writeCtx, conn, f) != nil {
			cancel()
			return false
		}
		return true
	}
	var reconciliation liveReconcileSchedule
	// Apply pacing after dispatch's ownership check. All subscription types
	// share this schedule, and ordinary frames never wait for its cooldown.
	writeSubscriptionFrame := func(f liveFrame) bool {
		if f.Event == "reconcile" && !reconciliation.request(time.Now()) {
			return true
		}
		return writeFrame(f)
	}
	out := make(chan liveFrame, 128)
	// A full queue blocks the producer instead of closing the socket. Execution
	// streams then overflow their hub subscription and catch up from persisted
	// output; live notifications are coalesced by the broadcaster and reconcile.
	send := func(f liveFrame) bool {
		select {
		case out <- f:
			return true
		case <-ctx.Done():
			return false
		}
	}
	reads := make(chan struct{}, 8)
	commands := make(chan liveCommand, 16)
	go func() {
		defer cancel()
		for {
			var cmd liveCommand
			if wsjson.Read(ctx, conn, &cmd) != nil {
				return
			}
			if cmd.Op == "read" || cmd.Op == "action" {
				// Reads answer on the shared queue; the slot limit pushes back on the
				// peer's socket rather than on the loop that drains the queue. Actions
				// are uncapped, like HTTP requests, so a slow one never stalls reads.
				if cmd.Op == "read" {
					select {
					case reads <- struct{}{}:
					case <-ctx.Done():
						return
					}
				}
				go func(cmd liveCommand) {
					if cmd.Op == "read" {
						defer func() { <-reads }()
					}
					result := remote.Result{Error: "read unavailable"}
					switch {
					case cmd.ID == "" || len(cmd.ID) > 128 || !authorized():
					case cmd.Read != nil && h.remoteExecutor != nil:
						result = h.remoteExecutor.Run(ctx, *cmd.Read)
					case cmd.Action != nil:
						result = h.runRemoteAction(ctx, *cmd.Action)
					}
					send(liveFrame{ID: cmd.ID, Event: "result", Data: result})
				}(cmd)
				continue
			}
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	subscriptions := make(map[string]*liveSubscription)
	defer func() {
		for _, subscription := range subscriptions {
			subscription.stop()
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastUpdate := ""
	ticks := 0
	// Access is checked when a subscription starts and again only when a change
	// names this user (or a project policy). Frames are never rechecked.
	principal, _ := models.PrincipalFromContext(ctx)
	var projectWatch, userWatch *service.AccessWatch
	var projectChanges, userChanges <-chan struct{}
	if h.projectAccessSvc != nil {
		projectWatch = h.projectAccessSvc.Watch(principal.UserID)
		defer projectWatch.Close()
		projectChanges = projectWatch.C()
	}
	if h.organizationSvc != nil {
		userWatch = h.organizationSvc.Watch(principal.UserID)
		defer userWatch.Close()
		userChanges = userWatch.C()
	}
	updateWatchProjects := func() {
		if projectWatch == nil {
			return
		}
		projects := map[string]bool{}
		all := false
		for _, sub := range subscriptions {
			if sub.command.Op == "account_usage" {
				continue
			}
			projects[sub.command.ProjectID] = true
			all = all || models.IsAggregateProject(sub.command.ProjectID)
		}
		projectWatch.SetProjects(projects, all)
	}
	updateWatchProjects()
	reauthorize := func(changes []service.AccessChange) {
		if len(changes) == 0 {
			return
		}
		all := false
		projects := map[string]bool{}
		for _, change := range changes {
			if change.ProjectID == "" {
				all = true
			}
			projects[change.ProjectID] = true
		}
		if all {
			if err := h.refreshLiveVisibility(ctx, visible); err != nil {
				cancel()
				return
			}
		} else {
			for id := range projects {
				visible[id] = h.projectAccessSvc.Require(ctx, id, authz.View) == nil
			}
		}
		for id, subscription := range subscriptions {
			cmd := subscription.command
			if !all && !projects[cmd.ProjectID] && !models.IsAggregateProject(cmd.ProjectID) {
				continue
			}
			if !h.liveSubscriptionAllowed(ctx, cmd) {
				subscription.stop()
				delete(subscriptions, id)
				writeFrame(liveFrame{ID: id, Event: "error", Data: "access blocked"})
			}
		}
		updateWatchProjects()
	}
	if !authorized() {
		return nil
	}
	repositoryChanged, unwatchRepositories := h.repositoryChanges()
	defer unwatchRepositories()
	serversChanged, unwatchServers := h.remoteSessions.watch(principalUserID(ctx))
	defer unwatchServers()
	var repositorySnapshot map[string]models.ProjectAPIItem
	writeFrame(liveFrame{Event: "ready", Data: map[string]string{"version": h.buildIdentity.Version}})
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-repositoryChanged:
			if !authorized() {
				return nil
			}
			var projects []models.ProjectAPIItem
			var err error
			if h.remoteExecutor != nil && h.remoteExecutor.Projects != nil {
				projects, err = h.remoteExecutor.Projects.ListAPIProjects(repokey.CachedOnly(ctx))
			} else {
				projects, err = h.projectSvc.ListAPIProjects(repokey.CachedOnly(ctx))
			}
			if err == nil {
				next := make(map[string]models.ProjectAPIItem, len(projects))
				changes := remote.ProjectChanges{}
				for _, p := range projects {
					next[p.ID] = p
					if previous, ok := repositorySnapshot[p.ID]; !ok || previous != p {
						changes.Projects = append(changes.Projects, p)
					}
				}
				for id := range repositorySnapshot {
					if _, ok := next[id]; !ok {
						changes.RemovedIDs = append(changes.RemovedIDs, id)
					}
				}
				if repositorySnapshot == nil {
					writeFrame(liveFrame{Event: "repository_projects", Data: projects})
				} else if len(changes.Projects)+len(changes.RemovedIDs) > 0 {
					writeFrame(liveFrame{Event: "repository_project_changes", Data: changes})
				}
				if repositorySnapshot == nil || len(changes.Projects)+len(changes.RemovedIDs) > 0 {
					writeFrame(liveFrame{Event: "servers_changed"})
				}
				repositorySnapshot = next
			}
		case <-serversChanged:
			// A saved server came or went: pages reload their project list and content.
			writeFrame(liveFrame{Event: "servers_changed"})
		case <-projectChanges:
			reauthorize(projectWatch.Drain())
		case <-userChanges:
			changes := userWatch.Drain()
			if len(changes) == 0 {
				continue
			}
			for _, change := range changes {
				if change.CloseConnections {
					return nil
				}
			}
			if !authorized() {
				return nil
			}
			reauthorize([]service.AccessChange{{UserID: principal.UserID}})
		case cmd := <-commands:
			if cmd.ID == "" || len(cmd.ID) > 128 {
				return nil
			}
			if cmd.Op == "unsubscribe" {
				if subscription := subscriptions[cmd.ID]; subscription != nil {
					subscription.stop()
					delete(subscriptions, cmd.ID)
					updateWatchProjects()
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
			if cmd.Op != "execution" && cmd.Op != "live" && cmd.Op != "computer" && cmd.Op != "account_usage" && cmd.Op != "page" {
				return nil
			}
			if !authorized() {
				return nil
			}
			// Register interest before admission so a concurrent revocation is queued.
			// Existing subscriptions remain registered throughout this check.
			if projectWatch != nil {
				projects := map[string]bool{}
				if cmd.Op != "account_usage" {
					projects[cmd.ProjectID] = true
				}
				all := models.IsAggregateProject(cmd.ProjectID)
				for _, sub := range subscriptions {
					if sub.command.Op == "account_usage" {
						continue
					}
					projects[sub.command.ProjectID] = true
					all = all || models.IsAggregateProject(sub.command.ProjectID)
				}
				projectWatch.SetProjects(projects, all)
			}
			if server, remote := remoteid.ServerOf(cmd.ProjectID); remote && cmd.Op == "computer" && !h.remoteSignedIn(ctx, server) {
				updateWatchProjects()
				writeFrame(liveFrame{ID: cmd.ID, Event: "error", Data: map[string]string{"reconnect": "/servers?reconnect=" + url.QueryEscape(server)}})
				continue
			}
			if cmd.Offset < 0 || !h.liveSubscriptionAllowed(ctx, cmd) {
				updateWatchProjects()
				writeFrame(liveFrame{ID: cmd.ID, Event: "error", Data: "subscription denied"})
				continue
			}
			if models.IsAggregateProject(cmd.ProjectID) {
				if err := h.refreshLiveVisibility(ctx, visible); err != nil {
					updateWatchProjects()
					writeFrame(liveFrame{ID: cmd.ID, Event: "error", Data: "subscription denied"})
					continue
				}
			} else if cmd.Op != "account_usage" {
				visible[cmd.ProjectID] = true
			}
			subctx, stop := context.WithCancel(ctx)
			subscription := &liveSubscription{stop: stop, command: cmd}
			subscriptions[cmd.ID] = subscription
			updateWatchProjects()
			// Apply changes queued during admission before starting any output.
			if projectWatch != nil {
				reauthorize(projectWatch.Drain())
				if subscriptions[cmd.ID] != subscription {
					continue
				}
			}
			go func(cmd liveCommand) {
				defer func() {
					// Use the connection queue even after subctx is cancelled.
					// Ownership checks discard obsolete completion markers.
					send(liveFrame{ID: cmd.ID, subscription: subscription, finished: true})
				}()
				emit := func(f liveFrame) bool {
					if subctx.Err() != nil {
						return false
					}
					f.ID = cmd.ID
					f.subscription = subscription
					return send(f)
				}
				if cmd.Op == "execution" {
					h.streamExecution(subctx, cmd, emit)
				} else if cmd.Op == "page" {
					h.streamPluginEvents(subctx, cmd, emit)
				} else if cmd.Op == "computer" {
					if server, remote := remoteid.ServerOf(cmd.ProjectID); remote {
						h.relayComputerStatus(subctx, server, cmd, emit)
					} else {
						h.streamComputerStatus(subctx, cmd, emit)
					}
				} else {
					h.streamLive(subctx, cmd, emit)
				}
			}(cmd)
		case f := <-out:
			// Frames from a subscription retired by revocation are discarded here.
			if !dispatchLiveFrame(subscriptions, f, writeSubscriptionFrame) {
				return nil
			}
			if f.finished || f.Event == "done" || f.Event == "error" || f.Event == "retry" {
				updateWatchProjects()
			}
		case <-ticker.C:
			if reconciliation.due(time.Now()) && !writeFrame(liveFrame{Event: "reconcile"}) {
				return nil
			}
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

// Check project access and ownership at subscription admission and access changes.
func (h *Handler) liveSubscriptionAllowed(ctx context.Context, cmd liveCommand) bool {
	if cmd.Op == "account_usage" {
		if h.organizationSvc == nil {
			return false
		}
		principal, ok := models.PrincipalFromContext(ctx)
		if !ok {
			return false
		}
		user, err := h.organizationSvc.Store.UserByID(ctx, principal.UserID)
		return err == nil && user != nil && !user.Disabled && user.IsOrgAdmin()
	}

	if cmd.ProjectID == "" || h.projectRepo == nil {
		return false
	}
	if models.IsAggregateProject(cmd.ProjectID) && cmd.Op == "live" {
		// The virtual scope receives notifications filtered by current project access
		// at delivery. Execution streams still require their real project.
		var shared *layout.SharedProject
		if cmd.ProjectID != models.AllProjectsID {
			if shared = h.sharedProject(ctx, cmd.ProjectID); shared == nil {
				return false
			}
		}
		if cmd.TaskID == "" {
			return true
		}
		if h.taskRepo == nil {
			return false
		}
		task, err := h.taskRepo.GetByID(ctx, cmd.TaskID)
		return err == nil && task != nil && (shared == nil || sharedHasMember(shared, task.ProjectID)) && (h.projectAccessSvc == nil || h.projectAccessSvc.Require(ctx, task.ProjectID, authz.View) == nil)
	}
	permission := authz.View
	if cmd.Op == "computer" {
		permission = authz.Edit
		_, remote := remoteid.ServerOf(cmd.ProjectID)
		if !remote && h.computerSvc == nil || cmd.ProjectID == models.AllProjectsID {
			return false
		}
	}
	if h.projectAccessSvc != nil && h.projectAccessSvc.Require(ctx, cmd.ProjectID, permission) != nil {
		return false
	}
	project, err := h.projectRepo.GetByID(ctx, cmd.ProjectID)
	if err != nil || project == nil {
		return false
	}
	if cmd.Op == "page" {
		projectID, _, err := pluginHandleOwner(h, ctx, PluginHandleArgs{PluginRefArgs: PluginRefArgs{ProjectID: cmd.ProjectID, InstanceID: cmd.InstanceID}, Handle: cmd.Handle})
		if err != nil || projectID != cmd.ProjectID {
			return false
		}
		return true
	}
	taskID := cmd.TaskID
	if cmd.Op == "execution" {
		if _, remote := remoteid.ServerOf(cmd.ExecID); remote {
			if _, sameServer := remoteid.ServerOf(cmd.ProjectID); !sameServer {
				return false
			}
		}
		if h.execRepo == nil {
			return false
		}
		var err error
		taskID, err = h.execRepo.GetExecutionTaskID(ctx, cmd.ExecID)
		if err != nil || taskID == "" {
			return false
		}
	}
	if taskID != "" {
		if h.taskRepo == nil {
			return false
		}
		task, err := h.taskRepo.GetThreadRenderMetadata(ctx, taskID)
		return err == nil && task != nil && task.ProjectID == cmd.ProjectID
	}

	return cmd.Op == "live" || cmd.Op == "computer"
}

// liveMembers lists the copies a shared project's live events come from.
var liveMembers = func(ctx context.Context, h *Handler, projectID string) map[string]bool {
	members := map[string]bool{}
	if shared := h.sharedProject(ctx, projectID); shared != nil {
		for _, id := range sharedMemberIDs(shared) {
			members[id] = true
		}
	}
	return members
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
	var taskMissed, chatMissed, fileMissed <-chan struct{}
	var err error
	load := liveMembers
	members := load(ctx, h, cmd.ProjectID)
	// Remote project lists can take seconds; reload them off the event loop so
	// events keep flowing, filtered by the previous copies, meanwhile.
	reloaded := make(chan map[string]bool, 1)
	reloading, stale := false, false
	reload := func() {
		reloading = true
		go func() { reloaded <- load(ctx, h, cmd.ProjectID) }()
	}
	var serversChanged <-chan struct{}
	if models.IsSharedProjectID(cmd.ProjectID) {
		ch, unwatch := h.remoteSessions.watch(principalUserID(ctx))
		defer unwatch()
		serversChanged = ch
	}
	if h.broadcaster != nil {
		if cmd.Op == "account_usage" {
			tasks, err = h.broadcaster.SubscribeAccountUsage()
		} else if models.IsAggregateProject(cmd.ProjectID) {
			tasks, err = h.broadcaster.Subscribe()
		} else {
			tasks, err = h.broadcaster.SubscribeProject(cmd.ProjectID)
		}
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.broadcaster.Unsubscribe(tasks)
		taskMissed = h.broadcaster.Missed(tasks)
	}
	if h.chatBroadcaster != nil && cmd.Op != "account_usage" {
		chats, err = h.chatBroadcaster.SubscribeProject(cmd.ProjectID)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.chatBroadcaster.Unsubscribe(chats)
		chatMissed = h.chatBroadcaster.Missed(chats)
	}
	if h.fileChangeBroadcaster != nil && cmd.TaskID != "" && cmd.Op != "account_usage" {
		files, err = h.fileChangeBroadcaster.SubscribeTask(cmd.TaskID)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "live subscription unavailable"})
			return
		}
		defer h.fileChangeBroadcaster.Unsubscribe(files)
		fileMissed = h.fileChangeBroadcaster.Missed(files)
	}
	if !send(liveFrame{Event: "subscribed"}) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-serversChanged:
			if reloading {
				stale = true
			} else {
				reload()
			}
		case members = <-reloaded:
			reloading = false
			if stale {
				stale = false
				reload()
			}
		case <-taskMissed:
			discardLiveBacklog(tasks)
			if !send(liveFrame{Event: "reconcile"}) {
				return
			}
		case <-chatMissed:
			discardLiveBacklog(chats)
			if !send(liveFrame{Event: "reconcile"}) {
				return
			}
		case <-fileMissed:
			discardLiveBacklog(files)
			if !send(liveFrame{Event: "reconcile"}) {
				return
			}
		case ev, ok := <-tasks:
			if !ok {
				return
			}
			if (ev.Type == events.AccountUsageUpdated && cmd.Op != "account_usage") || (models.IsSharedProjectID(cmd.ProjectID) && !members[ev.ProjectID]) {
				continue
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
	if server, ok := remoteid.ServerOf(cmd.ExecID); ok {
		h.relayExecution(ctx, server, cmd, send)
		return
	}
	sub, unsubscribe, err := h.executionStreamHub.Subscribe(cmd.ExecID)
	if err != nil {
		send(liveFrame{Event: "retry", Data: "execution subscription unavailable"})
		return
	}
	defer unsubscribe()
	sent := cmd.Offset
	replay := func() bool {
		exec, err := h.execRepo.ReadOutputAfter(ctx, cmd.ExecID, sent)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "execution replay unavailable"})
			return true
		}
		if exec == nil {
			send(liveFrame{Event: "error", Data: "execution not found"})
			return true
		}
		if exec.Total < sent && (exec.Status == models.ExecCompleted || exec.Status == models.ExecCancelled || exec.Status == models.ExecFailed) {
			if !send(liveFrame{Event: "reset", Data: exec.Output, Offset: exec.Total}) {
				return true
			}
			sent = exec.Total
		}
		if exec.Total > sent {
			start := 0
			// Bound frame/queue memory even for very large historical outputs.
			for start < len(exec.Output) {
				end := start + 32*1024
				if end > len(exec.Output) {
					end = len(exec.Output)
				}
				end = clampOffsetToUTF8Boundary(end, exec.Output)
				if !send(liveFrame{Event: "message", Data: exec.Output[start:end], Start: exec.Start + start, Offset: exec.Start + end}) {
					return true
				}
				start = end
				sent = exec.Start + end
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
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub:
			if !ok {
				// A closed hub channel indicates terminal delivery or overflow.
				// Retry the subscription instead of leaving a silently idle view.
				if !replay() {
					send(liveFrame{Event: "retry", Data: "execution stream interrupted"})
				}
				return
			}
			if ev.Type == events.ExecutionStreamPersisted && ev.Offset <= sent {
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
					send(liveFrame{Event: "retry", Data: "execution output gap"})
					return
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

// RunningExecutions uses the same instance-wide session authentication as the
// WebSocket. Subscription authorization still checks each execution's project.
func (h *Handler) RunningExecutions(c echo.Context) error {
	ctx := c.Request().Context()
	items, err := h.execRepo.ListLiveExecutions(ctx)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "Could not list running executions")
	}
	items = append(items, h.remoteLiveExecutions(ctx)...)
	if h.projectAccessSvc != nil {
		visible := items[:0]
		allowed := map[string]bool{}
		for _, item := range items {
			ok, checked := allowed[item.ProjectID]
			if _, remote := remoteid.ServerOf(item.ProjectID); remote {
				ok, checked = true, true
			}
			if !checked {
				ok = h.projectAccessSvc.Require(ctx, item.ProjectID, authz.View) == nil
				allowed[item.ProjectID] = ok
			}
			if ok {
				visible = append(visible, item)
			}
		}
		items = visible
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(http.StatusOK, items)
}

// relayExecution streams a remote execution over the server's hub socket. If
// this reader falls behind, the hub drops the stream and it resumes from the
// last delivered offset, so the remote socket never blocks.
func (h *Handler) relayExecution(ctx context.Context, server string, cmd liveCommand, send func(liveFrame) bool) {
	conn := h.remoteHub().Get(principalUserID(ctx), server)
	if conn == nil {
		send(liveFrame{Event: "retry", Data: "remote server unavailable"})
		return
	}
	strip := func(id string) string { return remoteid.Strip(server, id) }
	sent := cmd.Offset
	for ctx.Err() == nil {
		frames, overflowed, stop, err := conn.Stream(ctx, strip(cmd.ProjectID), strip(cmd.TaskID), strip(cmd.ExecID), sent)
		if err != nil {
			send(liveFrame{Event: "retry", Data: "remote server unavailable"})
			return
		}
		for f := range frames {
			if f.Event == "subscribed" {
				continue
			}
			var data any
			if len(f.Data) > 0 {
				_ = json.Unmarshal(f.Data, &data)
			}
			if !send(liveFrame{Event: f.Event, Data: data, Start: f.Start, Offset: f.Offset}) {
				stop()
				return
			}
			if f.Offset > sent {
				sent = f.Offset
			}
			if f.Event == "done" || f.Event == "error" || f.Event == "retry" {
				stop()
				return
			}
		}
		stop()
		if !overflowed() {
			send(liveFrame{Event: "retry", Data: "remote stream interrupted"})
			return
		}
	}
}

// Once delivery has a gap, discard the already-buffered stale notifications
// before requesting authoritative state. Bound draining so publishers cannot
// starve recovery by continuing to produce events.
func discardLiveBacklog[T any](events <-chan T) {
	for remaining := len(events); remaining > 0; remaining-- {
		select {
		case <-events:
		default:
			return
		}
	}
}
