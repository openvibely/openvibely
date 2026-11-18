package handler

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) computerProject(c echo.Context) error {
	if h.computerSvc == nil || h.projectRepo == nil {
		return echo.NewHTTPError(503, "Project computers unavailable")
	}
	if project, err := h.projectRepo.GetByID(c.Request().Context(), c.Param("id")); err != nil || project == nil {
		return echo.NewHTTPError(404, "Project not found")
	}
	// A desktop provides shell and signed-in browser access, so even viewing it
	// requires editor access, including connections using native stream tickets.
	if h.projectAccessSvc != nil {
		permission := authz.Edit
		switch c.Param("action") {
		case "enable", "disable", "time-zone", "idle-timeout":
			permission = authz.Manage
		}
		if err := h.projectAccessSvc.Require(c.Request().Context(), c.Param("id"), permission); err != nil {
			return err
		}
	}
	if taskID := c.QueryParam("task_id"); taskID != "" {
		scoped, err := h.computerSvc.ForTask(c.Request().Context(), c.Param("id"), taskID)
		if err != nil {
			return echo.NewHTTPError(404, "Task not found in this project")
		}
		c.Set("computer_service", scoped)
	}
	return nil
}
func (h *Handler) computerService(c echo.Context) *service.ProjectComputerService {
	if scoped, ok := c.Get("computer_service").(*service.ProjectComputerService); ok {
		return scoped
	}
	return h.computerSvc
}
func (h *Handler) ProjectComputerPage(c echo.Context) error {
	// Keep old project links working; task viewer routes still render their desktop.
	if c.QueryParam("task_id") == "" {
		if err := h.computerProject(c); err != nil {
			return err
		}
		return c.Redirect(http.StatusSeeOther, "/settings/computer?project_id="+url.QueryEscape(c.Param("id")))
	}
	if err := h.computerProject(c); err != nil {
		return err
	}
	var taskIDs []string
	if id := c.QueryParam("task_id"); id != "" {
		taskIDs = append(taskIDs, id)
	}
	if c.Request().Header.Get("HX-Request") == "true" {
		c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
		return render(c, http.StatusOK, pages.ProjectComputerContent(c.Param("id"), taskIDs...))
	}
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return err
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	return render(c, http.StatusOK, pages.ProjectComputer(projects, c.Param("id"), taskIDs...))
}
func (h *Handler) ProjectComputerStatus(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	status, err := h.computerStatus(c.Request().Context(), h.computerService(c), c.Param("id"), c.QueryParam("task_id"), c.QueryParam("viewer_id"), c.QueryParam("viewer_mode"), c.Request().Header.Get("X-OpenVibely-Native-Window") == "1")
	if err != nil {
		return err
	}
	return c.JSON(200, status)
}

// computerStatus is shared by HTTP callers and authorized WebSocket subscriptions.
func (h *Handler) computerAvailability(ctx context.Context, svc *service.ProjectComputerService, projectID string) (map[string]any, error) {
	state, err := svc.Get(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"configured": false, "enabled": false}, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"configured": true, "enabled": state.Enabled}, nil
}

func (h *Handler) computerStatus(ctx context.Context, svc *service.ProjectComputerService, projectID, taskID, viewerID, viewerMode string, native bool) (map[string]any, error) {
	state, err := svc.Get(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"idle_minutes": 30, "time_zone": "UTC", "configured": false, "ready": false, "enabled": false}, nil
	}
	if err != nil {
		return nil, err
	}
	status := map[string]any{"preview_quality": state.PreviewQuality, "idle_minutes": state.IdleMinutes, "time_zone": state.TimeZone, "configured": true, "enabled": state.Enabled, "task_enabled": state.TaskEnabled, "project_enabled": state.ProjectEnabled, "ready": svc.Ready(ctx, projectID), "held": state.Held, "handoff_kind": state.HandoffKind, "handoff_reason": state.HandoffReason, "tool_target": state.ToolTarget, "handoff_url": state.HandoffURL, "chrome_available": svc.ChromeImportAvailable(), "busy": state.Owner != "" && state.LeaseUntil >= time.Now().Unix()}
	if h.desktopMode && h.computerDesktopURL != "" && native && status["ready"] == true {
		mode := "view"
		// Permission is checked when the stream connects; never silently issue
		// a view-only connection that cannot later accept input on expansion.
		if viewerMode == "control" {
			mode = "control"
		}
		ticket, err := h.issueComputerTicket(ctx, projectID, mode, taskID, viewerID)
		if err != nil {
			return nil, echo.NewHTTPError(429, err.Error())
		}
		status["stream_url"] = h.computerDesktopURL + "/computer-stream"
		status["ticket"] = ticket
	}
	return status, nil
}
func computerSameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host
	}
	return true
}
func (h *Handler) ProjectComputerLifecycle(c echo.Context) error {
	if err := h.computerProject(c); err != nil {
		return err
	}
	if !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403, "Cross-origin computer request rejected")
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), 15*time.Minute)
	defer cancel()
	var err error
	switch c.Param("action") {
	case "idle-timeout":
		minutes, parseErr := strconv.Atoi(c.FormValue("idle_minutes"))
		if parseErr != nil {
			return echo.NewHTTPError(400, "Choose an idle timeout in minutes")
		}
		err = h.computerSvc.SetIdleMinutes(ctx, c.Param("id"), minutes)
	case "time-zone":
		err = h.computerSvc.SetTimeZone(ctx, c.Param("id"), c.FormValue("time_zone"))
	case "enable", "disable":
		err = h.computerSvc.SetEnabled(ctx, c.Param("id"), c.Param("action") == "enable")
	case "task-enable", "task-disable":
		err = h.computerService(c).SetTaskEnabled(ctx, c.Param("id"), c.Param("action") == "task-enable")
	case "ensure":
		err = h.computerService(c).EnsureStarted(ctx, c.Param("id"))
	case "login-done":
		err = h.computerService(c).CompleteLogin(ctx, c.Param("id"))
	case "login-cancel":
		err = h.computerService(c).CancelHandoff(ctx, c.Param("id"))
	case "target-computer", "target-worktree":
		err = h.computerService(c).SetToolTarget(ctx, c.Param("id"), strings.TrimPrefix(c.Param("action"), "target-"))
	case "start":
		err = h.computerService(c).Start(ctx, c.Param("id"))
	case "stop":
		err = h.computerService(c).Stop(ctx, c.Param("id"))
	default:
		return echo.NewHTTPError(404)
	}
	if errors.Is(err, persistence.ErrComputerBusy) {
		return echo.NewHTTPError(409, err.Error())
	}
	if err != nil {
		return echo.NewHTTPError(400, err.Error())
	}
	if c.Param("action") == "login-done" || c.Param("action") == "login-cancel" {
		h.PromoteQueuedTaskThreadInput(c.QueryParam("task_id"))
	}
	return c.JSON(200, map[string]bool{"ok": true})
}
func (h *Handler) ProjectComputerStream(c echo.Context) error {
	ctx, cancel, err := h.computerStreamAccess(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	defer cancel()
	c.SetRequest(c.Request().WithContext(ctx))
	if err := h.computerProject(c); err != nil {
		return err
	}
	_, nativeTicket := c.Get(computerTicketContextKey).(computerStreamTicket)
	if !nativeTicket && !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(403)
	}
	mode := c.Param("mode")
	if mode != "view" && mode != "control" {
		return echo.NewHTTPError(404)
	}
	id := c.Param("id")

	conn, err := h.computerService(c).Connection(ctx, id)
	if err != nil {
		return echo.NewHTTPError(503, "Start the project computer")
	}
	u, err := url.Parse(conn.Endpoint)
	if err != nil {
		return err
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	session := c.QueryParam("viewer_id")
	if session == "" {
		session = rand.Text()
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{16,64}$`).MatchString(session) {
		return echo.NewHTTPError(400, "Invalid viewer session")
	}
	principal, _ := models.PrincipalFromContext(ctx)
	svc := h.computerService(c)
	token, detach, err := svc.RegisterViewer(ctx, id, principal.UserID, session, mode == "control", cancel)
	if err != nil {
		return echo.NewHTTPError(409, err.Error())
	}
	defer detach()
	taskID := c.QueryParam("task_id")
	u.Path = strings.TrimRight(u.Path, "/") + "/rfb-" + mode
	upstream, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + conn.Token}, "X-OV-Viewer-ID": {token}}, Subprotocols: []string{"binary"}})
	if err != nil {
		applog.Infof("computer viewer upstream failed project=%s task=%s mode=%s: %v", id, taskID, mode, err)
		return echo.NewHTTPError(502, "Computer viewer unavailable")
	}
	defer upstream.CloseNow()
	downstream, err := websocket.Accept(c.Response(), c.Request(), &websocket.AcceptOptions{Subprotocols: []string{"binary"}, InsecureSkipVerify: nativeTicket})
	if err != nil {
		return err
	}
	defer downstream.CloseNow()
	upstream.SetReadLimit(16 << 20)
	downstream.SetReadLimit(1 << 20)
	done := make(chan error, 2)
	go func() {
		done <- forwardComputerScreen(ctx, downstream, upstream)
	}()
	go func() {
		for {
			kind, data, err := downstream.Read(ctx)
			if err == nil {
				write := func() error { return upstream.Write(ctx, kind, data) }
				if mode == "control" {
					err = svc.ForwardViewerInput(id, session, token, write)
				} else {
					err = write()
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
	}()
	<-done
	cancel()
	return nil
}

// forwardComputerScreen preserves message boundaries without buffering entire
// screen updates. A failed partial message is abandoned when the caller closes
// the connection; it must not be finalized as a successful truncated update.
func forwardComputerScreen(ctx context.Context, dst, src *websocket.Conn) error {
	buffer := make([]byte, 32<<10)
	for {
		kind, reader, err := src.Reader(ctx)
		if err != nil {
			return err
		}
		writer, err := dst.Writer(ctx, kind)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(writer, reader, buffer); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
}

func (h *Handler) ProjectComputerSelectedPage(c echo.Context) error {
	id := c.QueryParam("project_id")
	if id == "" {
		return c.Redirect(http.StatusSeeOther, "/chat")
	}
	c.SetParamNames("id")
	c.SetParamValues(id)
	return h.ProjectComputerPage(c)
}
