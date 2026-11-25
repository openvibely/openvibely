package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
)

// ComputerArgs names a project computer, optionally a task's session of it.
type ComputerArgs struct {
	ProjectID, TaskID string
}

func (h *Handler) computerFor(ctx context.Context, a ComputerArgs) (*service.ProjectComputerService, error) {
	if h.computerSvc == nil {
		return nil, statusErr(http.StatusServiceUnavailable, "Project computers unavailable")
	}
	if project, err := h.projectRepo.GetByID(ctx, a.ProjectID); err != nil || project == nil {
		return nil, statusErr(http.StatusNotFound, "Project not found")
	}
	if a.TaskID == "" {
		return h.computerSvc, nil
	}
	scoped, err := h.computerSvc.ForTask(ctx, a.ProjectID, a.TaskID)
	if err != nil {
		return nil, statusErr(http.StatusNotFound, "Task not found in this project")
	}
	return scoped, nil
}

type computerScoped interface{ project() string }

func (a ComputerArgs) project() string { return a.ProjectID }

func computerOwner[A computerScoped](permission authz.Permission) func(*Handler, context.Context, A) (string, authz.Permission, error) {
	return func(_ *Handler, _ context.Context, a A) (string, authz.Permission, error) {
		return a.project(), permission, nil
	}
}

var actComputerEnabled = defineAction("computer.enabled", computerOwner[ComputerArgs](authz.View), func(h *Handler, ctx context.Context, a ComputerArgs) (bool, error) {
	if h.computerSvc == nil {
		return false, nil
	}
	state, err := h.computerSvc.Get(ctx, a.ProjectID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return state.Enabled, nil
})

// actComputerAccess applies the computer page's checks on the server owning the project.
var actComputerAccess = defineAction("computer.access", computerOwner[ComputerArgs](authz.Edit), func(h *Handler, ctx context.Context, a ComputerArgs) (struct{}, error) {
	_, err := h.computerFor(ctx, a)
	return struct{}{}, err
})

type computerLifecycleArgs struct {
	ComputerArgs
	Action, Value string
	Minutes       int
}

func computerLifecyclePermission(action string) authz.Permission {
	switch action {
	case "enable", "disable", "time-zone", "idle-timeout":
		return authz.Manage
	}
	return authz.Edit
}

var actComputerLifecycle = defineAction("computer.lifecycle", func(_ *Handler, _ context.Context, a computerLifecycleArgs) (string, authz.Permission, error) {
	return a.ProjectID, computerLifecyclePermission(a.Action), nil
}, func(h *Handler, ctx context.Context, a computerLifecycleArgs) (struct{}, error) {
	svc, err := h.computerFor(ctx, a.ComputerArgs)
	if err != nil {
		return struct{}{}, err
	}
	id := a.ProjectID
	switch a.Action {
	case "idle-timeout":
		err = h.computerSvc.SetIdleMinutes(ctx, id, a.Minutes)
	case "time-zone":
		err = h.computerSvc.SetTimeZone(ctx, id, a.Value)
	case "enable", "disable":
		err = h.computerSvc.SetEnabled(ctx, id, a.Action == "enable")
	case "task-enable", "task-disable":
		err = svc.SetTaskEnabled(ctx, id, a.Action == "task-enable")
	case "ensure":
		err = svc.EnsureStarted(ctx, id)
	case "login-done":
		err = svc.CompleteLogin(ctx, id)
	case "login-cancel":
		err = svc.CancelHandoff(ctx, id)
	case "target-computer", "target-worktree":
		err = svc.SetToolTarget(ctx, id, a.Action[len("target-"):])
	case "start":
		err = svc.Start(ctx, id)
	case "stop":
		err = svc.Stop(ctx, id)
	default:
		return struct{}{}, statusErr(http.StatusNotFound, "Not found")
	}
	if errors.Is(err, persistence.ErrComputerBusy) {
		return struct{}{}, statusErr(http.StatusConflict, err.Error())
	}
	if err != nil {
		return struct{}{}, statusErr(http.StatusBadRequest, err.Error())
	}
	if a.Action == "login-done" || a.Action == "login-cancel" {
		h.PromoteQueuedTaskThreadInput(a.TaskID)
	}
	return struct{}{}, nil
})

type computerChromeArgs struct {
	ComputerArgs
	Profile string
}

type computerChromeResult struct {
	Cookies  []models.ChromeCookieSelection
	Profiles []string
	Pending  *models.ComputerChromeGrant
}

var actComputerChrome = defineAction("computer.chrome", computerOwner[computerChromeArgs](authz.Edit), func(h *Handler, ctx context.Context, a computerChromeArgs) (computerChromeResult, error) {
	svc, err := h.computerFor(ctx, a.ComputerArgs)
	if err != nil {
		return computerChromeResult{}, err
	}
	state, err := svc.Get(ctx, a.ProjectID)
	if err != nil || !state.Held || state.HandoffKind != "request_chrome_login" {
		return computerChromeResult{}, statusErr(http.StatusConflict, "No Chrome import awaiting approval")
	}
	if a.Profile != "" {
		sites, err := svc.ChromeCookieChoices(ctx, a.Profile)
		if err != nil {
			return computerChromeResult{}, statusErr(http.StatusBadRequest, err.Error())
		}
		return computerChromeResult{Cookies: sites}, nil
	}
	profiles, err := svc.ChromeProfiles()
	if err != nil {
		return computerChromeResult{}, statusErr(http.StatusBadRequest, err.Error())
	}
	pending, err := svc.PendingChromeImport(ctx, a.ProjectID)
	if err != nil {
		return computerChromeResult{}, err
	}
	return computerChromeResult{Profiles: profiles, Pending: pending}, nil
})

type computerChromeImportArgs struct {
	ComputerArgs
	GrantID, Scope string
	Hours          int
	Selections     []models.ChromeCookieSelection
}

type computerChromeImportResult struct {
	Imported int
	// Incomplete names the grant to resume when only some profiles imported.
	Incomplete, Message string
}

var actComputerChromeImport = defineAction("computer.chrome_import", computerOwner[computerChromeImportArgs](authz.Edit), func(h *Handler, ctx context.Context, a computerChromeImportArgs) (computerChromeImportResult, error) {
	svc, err := h.computerFor(ctx, a.ComputerArgs)
	if err != nil {
		return computerChromeImportResult{}, err
	}
	var count int
	if a.GrantID != "" {
		count, err = svc.ResumeChromeImport(ctx, a.ProjectID, a.GrantID)
	} else {
		count, err = svc.ImportChromeSelection(ctx, a.ProjectID, a.Scope, a.Selections, a.Hours)
	}
	var partial *service.ChromeImportIncomplete
	if errors.As(err, &partial) {
		return computerChromeImportResult{Imported: count, Incomplete: partial.GrantID, Message: err.Error()}, nil
	}
	if err != nil {
		return computerChromeImportResult{}, statusErr(http.StatusBadRequest, err.Error())
	}
	return computerChromeImportResult{Imported: count}, nil
})

type computerGrantsArgs struct {
	ComputerArgs
	Revoke string
	Offset int
}

type computerGrantsResult struct {
	Grants []models.ComputerChromeGrant
	More   bool
}

var actComputerGrants = defineAction("computer.chrome_grants", computerOwner[computerGrantsArgs](authz.Edit), func(h *Handler, ctx context.Context, a computerGrantsArgs) (computerGrantsResult, error) {
	svc, err := h.computerFor(ctx, a.ComputerArgs)
	if err != nil {
		return computerGrantsResult{}, err
	}
	if a.Revoke != "" {
		if err := svc.RevokeChromeGrant(ctx, a.ProjectID, a.Revoke); err != nil {
			return computerGrantsResult{}, statusErr(http.StatusBadRequest, err.Error())
		}
	}
	grants, more, err := svc.ChromeGrantsPage(ctx, a.ProjectID, a.Offset)
	return computerGrantsResult{Grants: grants, More: more}, err
})

// relayComputerStatus relays a remote project's computer status subscription
// over that server's live socket. The remote authorizes it.
func (h *Handler) relayComputerStatus(ctx context.Context, server string, cmd liveCommand, send func(liveFrame) bool) {
	conn := h.remoteHub().Get(principalUserID(ctx), server)
	if conn == nil || !h.remoteSignedIn(ctx, server) {
		send(liveFrame{Event: "error", Data: map[string]string{"reconnect": "/servers?reconnect=" + url.QueryEscape(server)}})
		return
	}
	strip := func(id string) string { return remoteid.Strip(server, id) }
	frames, _, stop, err := conn.Subscribe(ctx, map[string]any{"op": "computer", "project_id": strip(cmd.ProjectID), "task_id": strip(cmd.TaskID), "viewer_id": cmd.ViewerID, "viewer_mode": cmd.ViewerMode})
	if err != nil {
		send(liveFrame{Event: "retry", Data: "remote server unavailable"})
		return
	}
	defer stop()
	for f := range frames {
		var data any
		if f.Event == "computer_status" {
			var status map[string]any
			if json.Unmarshal(f.Data, &status) != nil {
				continue
			}
			status["project_id"], status["task_id"] = cmd.ProjectID, cmd.TaskID
			// The desktop relays the screen itself, so drop the remote's direct stream address.
			delete(status, "stream_url")
			delete(status, "ticket")
			if h.desktopMode && h.computerDesktopURL != "" && cmd.Native && status["ready"] == true {
				mode := "view"
				if cmd.ViewerMode == "control" {
					mode = "control"
				}
				ticket, err := h.issueComputerTicket(ctx, cmd.ProjectID, mode, cmd.TaskID, cmd.ViewerID)
				if err != nil {
					send(liveFrame{Event: "retry", Data: err.Error()})
					return
				}
				status["stream_url"], status["ticket"] = h.computerDesktopURL+"/computer-stream", ticket
			}
			data = status
		} else if len(f.Data) > 0 {
			_ = json.Unmarshal(f.Data, &data)
		}
		if !send(liveFrame{Event: f.Event, Data: data}) {
			return
		}
		if f.Event == "error" || f.Event == "retry" {
			return
		}
	}
	send(liveFrame{Event: "retry", Data: "remote stream interrupted"})
}

// relayComputerScreen opens a separate socket to the server owning a remote
// project and copies the screen both ways. The remote enforces access and control.
func (h *Handler) relayComputerScreen(c echo.Context, server, mode string) error {
	ctx := c.Request().Context()
	conn := h.remoteHub().Get(principalUserID(ctx), server)
	if conn == nil || !h.remoteSignedIn(ctx, server) {
		return reconnectError{server}
	}
	strip := func(id string) string { return remoteid.Strip(server, id) }
	query := url.Values{}
	if task := c.QueryParam("task_id"); task != "" {
		query.Set("task_id", strip(task))
	}
	if viewer := c.QueryParam("viewer_id"); viewer != "" {
		query.Set("viewer_id", viewer)
	}
	upstream, err := conn.Dial(ctx, "/projects/"+url.PathEscape(strip(c.Param("id")))+"/computer/stream/"+mode, query, []string{"binary"})
	if errors.Is(err, remote.ErrSessionExpired) {
		return reconnectError{server}
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusBadGateway, "Computer viewer unavailable")
	}
	defer upstream.CloseNow()
	_, nativeTicket := c.Get(computerTicketContextKey).(computerStreamTicket)
	downstream, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{Subprotocols: []string{"binary"}, InsecureSkipVerify: nativeTicket})
	if err != nil {
		return err
	}
	defer downstream.CloseNow()
	upstream.SetReadLimit(16 << 20)
	downstream.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pipe := func(from, to *websocket.Conn) {
		defer cancel()
		for {
			kind, data, err := from.Read(ctx)
			if err != nil || to.Write(ctx, kind, data) != nil {
				return
			}
		}
	}
	go pipe(upstream, downstream)
	pipe(downstream, upstream)
	return nil
}
