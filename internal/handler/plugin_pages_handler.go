package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// pagePluginChatCategory hides page side-chat tasks from boards and from the
// main project chat history (which is keyed by category=chat).
const pagePluginChatCategory = models.TaskCategory("freezer")

const (
	maxPluginChatMessage   = 8000
	maxPluginAnalyzePrompt = 4000
	pluginEventHeartbeat   = 25 * time.Second
)

var errPluginChatBusy = errors.New("a response is already in progress for this page")

// SetPagePlugins wires the plugin page manager.
func (h *Handler) SetPagePlugins(m *pageplugins.Manager) {
	h.pagePlugins = m
	if m != nil {
		m.Approvals.OnPending = func(a pageintegration.Approval) {
			m.Hub.Publish(a.InstanceID, pageintegration.Event{Type: "approval.pending", Operation: a.Operation, At: time.Now().UTC()})
		}
	}
}

func (h *Handler) registerPluginPageRoutes(e *echo.Echo) {
	e.GET("/p/:instanceId", h.PluginPage)
	e.GET("/plugin-pages/nav", h.PluginPagesNav)
	e.GET("/plugin-assets/:handle/*", h.PluginAsset)
	api := e.Group("/api/plugin-pages/:instanceId")
	api.POST("/bridge", h.PluginBridge)
	api.GET("/events", h.PluginEvents)
	api.POST("/handle/retire", h.PluginRetireHandle)
	api.POST("/chat", h.PluginChatSend)
	api.POST("/chat/stop", h.PluginChatStop)
	api.GET("/approvals", h.PluginApprovals)
	api.POST("/approvals/:approvalId/:decision", h.PluginApprovalDecide)
	e.GET("/settings/plugin-pages", h.PluginSettings)
	e.POST("/settings/plugin-pages/install", h.PluginSettingsInstall)
	e.POST("/settings/plugin-pages/install-example", h.PluginSettingsInstallExample)
	e.POST("/settings/plugin-pages/instances", h.PluginSettingsCreateInstance)
	e.POST("/settings/plugin-pages/instances/:instanceId/:action", h.PluginSettingsInstanceAction)
	e.POST("/settings/plugin-pages/packages/:packageId/remove", h.PluginSettingsRemovePackage)
}

func pluginNotFound() error { return echo.NewHTTPError(http.StatusNotFound, "plugin page not found") }

func (h *Handler) requirePagePlugins() error {
	if h.pagePlugins == nil {
		return pluginNotFound()
	}
	return nil
}

// requirePluginMutation blocks cross-site form posts: every mutating plugin
// request must carry a non-simple header (HTMX or the host bridge) and, when
// the browser sends one, a same-origin Origin header.
func requirePluginMutation(c echo.Context) error {
	r := c.Request()
	if r.Header.Get("HX-Request") != "true" && r.Header.Get("X-OpenVibely-Plugin-Bridge") != "1" {
		return echo.NewHTTPError(http.StatusForbidden, "plugin requests must come from the OpenVibely page")
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return echo.NewHTTPError(http.StatusForbidden, "same-origin request required")
		}
	}
	return nil
}

// resolvePluginForRequest authorizes the instance against the request's project.
// API calls must name the owning project; the instance is never looked up by a
// project ID supplied by the plugin itself.
func (h *Handler) resolvePluginForRequest(c echo.Context, allowAllProjects bool) (*pageplugins.Resolved, error) {
	if err := h.requirePagePlugins(); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" || (projectID == models.AllProjectsID && !allowAllProjects) {
		return nil, pluginNotFound()
	}
	r, err := h.pagePlugins.Resolve(c.Request().Context(), c.Param("instanceId"), projectID)
	if err != nil {
		if errors.Is(err, pageplugins.ErrNotFound) || errors.Is(err, pageplugins.ErrDisabled) {
			return nil, pluginNotFound()
		}
		return nil, err
	}
	return r, nil
}

func (h *Handler) pluginHandleFor(c echo.Context, r *pageplugins.Resolved, token string) (pageplugins.Handle, error) {
	hd, ok := h.pagePlugins.LookupHandle(token)
	if !ok || hd.InstanceID != r.Instance.ID || hd.ProjectID != r.Instance.ProjectID || hd.PrincipalID != h.authPrincipalID(c) {
		return pageplugins.Handle{}, echo.NewHTTPError(http.StatusForbidden, "plugin page session expired; reload the page")
	}
	return hd, nil
}

func pluginView(r *pageplugins.Resolved, requestProjectID, projectName string, hd pageplugins.Handle) pages.PluginPageView {
	return pages.PluginPageView{
		InstanceID:       r.Instance.ID,
		Title:            r.Instance.Title,
		PluginName:       r.Manifest.Name,
		Version:          r.Manifest.Version,
		OwningProjectID:  r.Instance.ProjectID,
		OwningProject:    projectName,
		RequestProjectID: requestProjectID,
		Handle:           hd.Token,
		AssetURL:         "/plugin-assets/" + url.PathEscape(hd.Token) + "/" + strings.TrimPrefix(r.Page.Entry, "/") + "?handle=" + url.QueryEscape(hd.Token),
		ChatEnabled:      r.Manifest.HasPermission(pageplugins.PermissionAgent),
	}
}

// PluginPage renders a plugin page instance in the main content area.
func (h *Handler) PluginPage(c echo.Context) error {
	if err := h.requirePagePlugins(); err != nil {
		return err
	}
	ctx := c.Request().Context()
	requested := strings.TrimSpace(c.QueryParam("project_id"))
	in, err := h.pagePlugins.Store().GetInstance(ctx, c.Param("instanceId"))
	if err != nil {
		return err
	}
	if in == nil {
		return pluginNotFound()
	}
	if requested == "" || requested == models.AllProjectsID {
		// Opening from All Projects (or a bare deep link) keeps the page's own project.
		target := "/p/" + url.PathEscape(in.ID) + "?project_id=" + url.QueryEscape(in.ProjectID)
		if isHTMX(c) {
			c.Response().Header().Set("HX-Redirect", target)
			return c.NoContent(http.StatusOK)
		}
		return c.Redirect(http.StatusSeeOther, target)
	}
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	hd, err := h.pagePlugins.IssueHandle(r, h.authPrincipalID(c))
	if err != nil {
		return err
	}
	projectName := r.Instance.ProjectID
	if p, err := h.projectSvc.GetByID(ctx, r.Instance.ProjectID); err == nil && p != nil {
		projectName = p.Name
	}
	v := pluginView(r, requested, projectName, hd)
	if v.ChatEnabled {
		if err := h.loadPluginChatHistory(ctx, &v); err != nil {
			applog.Infof("[plugin-pages] history load failed instance=%s: %v", r.Instance.ID, err)
		}
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.PluginPageContent(v))
	}
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.PluginPage(projects, r.Instance.ProjectID, v))
}

func (h *Handler) loadPluginChatHistory(ctx context.Context, v *pages.PluginPageView) error {
	taskID, err := h.pagePlugins.Store().GetConversationTask(ctx, v.InstanceID)
	if err != nil || taskID == "" {
		return err
	}
	task, err := h.taskRepo.GetByID(ctx, taskID)
	if err != nil || task == nil || task.ProjectID != v.OwningProjectID {
		return err
	}
	execs, err := h.execRepo.ListByTaskChronologicalLimit(ctx, taskID, 50)
	if err != nil {
		return err
	}
	v.ChatTaskID, v.ChatTask, v.ChatExecutions = taskID, task, execs
	return nil
}

// PluginPagesNav renders enabled plugin pages for the left rail.
func (h *Handler) PluginPagesNav(c echo.Context) error {
	if h.pagePlugins == nil {
		return c.NoContent(http.StatusOK)
	}
	projectID, err := h.getCurrentProjectID(c)
	if err != nil || projectID == "" {
		return c.NoContent(http.StatusOK)
	}
	ins, err := h.pagePlugins.ListEnabled(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	entries := make([]pages.PluginNavEntry, 0, len(ins))
	for _, in := range ins {
		label := in.ProjectName
		if label == "" {
			label = in.ProjectID
		}
		entries = append(entries, pages.PluginNavEntry{InstanceID: in.ID, Title: in.Title, ProjectID: in.ProjectID, ProjectLabel: label})
	}
	return render(c, http.StatusOK, pages.PluginNav(entries, projectID == models.AllProjectsID))
}

var pluginAssetTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".htm": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json", ".svg": "image/svg+xml", ".png": "image/png", ".jpg": "image/jpeg",
	".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".ico": "image/x-icon", ".woff": "font/woff", ".woff2": "font/woff2", ".txt": "text/plain; charset=utf-8", ".map": "application/json",
}

const pluginAssetCSP = "sandbox allow-scripts allow-forms; default-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' data: blob:; connect-src 'none'; frame-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"

// PluginAsset serves a static file from the instance's package. The unguessable
// per-load handle is the capability: sandboxed (opaque-origin) frames do not
// send session cookies for subresources.
func (h *Handler) PluginAsset(c echo.Context) error {
	if err := h.requirePagePlugins(); err != nil {
		return err
	}
	hd, ok := h.pagePlugins.LookupHandle(c.Param("handle"))
	if !ok {
		return pluginNotFound()
	}
	// The capability replaces cookie authentication only for static assets.
	// Recheck its issuing user so disabling an account revokes asset access too.
	if h.organizationSvc != nil {
		if !strings.HasPrefix(hd.PrincipalID, "user:") {
			return pluginNotFound()
		}
		user, err := h.organizationSvc.Store.UserByID(c.Request().Context(), strings.TrimPrefix(hd.PrincipalID, "user:"))
		if err != nil || user == nil || user.Disabled || user.Role == "" {
			return pluginNotFound()
		}
	}
	r, err := h.pagePlugins.Resolve(c.Request().Context(), hd.InstanceID, hd.ProjectID)
	if err != nil {
		return pluginNotFound()
	}
	rel := c.Param("*")
	if unescaped, err := url.PathUnescape(rel); err == nil {
		rel = unescaped
	}
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if rel == pageplugins.ManifestFile || r.Manifest.Backend != nil && isPluginExecutable(r.Manifest.Backend, rel) {
		return pluginNotFound()
	}
	ctype, ok := pluginAssetTypes[strings.ToLower(path.Ext(rel))]
	if !ok {
		if guessed := mime.TypeByExtension(path.Ext(rel)); strings.HasPrefix(guessed, "image/") {
			ctype = guessed
		} else {
			return pluginNotFound()
		}
	}
	full, err := h.pagePlugins.AssetPath(r, rel)
	if err != nil {
		return pluginNotFound()
	}
	f, err := os.Open(full)
	if err != nil {
		return pluginNotFound()
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return pluginNotFound()
	}
	hdr := c.Response().Header()
	hdr.Set("Content-Security-Policy", pluginAssetCSP)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Content-Type", ctype)
	hdr.Set("X-Frame-Options", "SAMEORIGIN")
	return c.Stream(http.StatusOK, ctype, f)
}

func isPluginExecutable(b *pageplugins.Backend, rel string) bool {
	for _, exe := range b.Executables {
		if clean, err := pageplugins.CleanRelPath(exe); err == nil && clean == rel {
			return true
		}
	}
	return false
}

type pluginBridgeResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func bridgeError(c echo.Context, status int, msg string) error {
	return c.JSON(status, pluginBridgeResponse{OK: false, Error: msg})
}

func bridgeResult(c echo.Context, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, pluginBridgeResponse{OK: true, Result: raw})
}

// PluginBridge executes one validated v1 bridge message relayed by the host page.
func (h *Handler) PluginBridge(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, pageplugins.MaxBridgeMessageBytes+1))
	if err != nil {
		return bridgeError(c, http.StatusBadRequest, "unreadable bridge message")
	}
	var envelope struct {
		Handle string `json:"handle"`
	}
	_ = json.Unmarshal(raw, &envelope)
	hd, err := h.pluginHandleFor(c, r, envelope.Handle)
	if err != nil {
		return bridgeError(c, http.StatusForbidden, "plugin page session expired; reload the page")
	}
	msg, err := pageplugins.ParseBridgeMessage(raw, hd.Token)
	if err != nil {
		return bridgeError(c, http.StatusBadRequest, err.Error())
	}
	ctx := c.Request().Context()
	integration := h.pagePlugins.Integration(r)
	principal := h.authPrincipalID(c)
	switch msg.Type {
	case pageplugins.BridgeReady:
		type opInfo struct {
			Name        string `json:"name"`
			Kind        string `json:"kind"`
			Description string `json:"description"`
		}
		ops := []opInfo{}
		for _, op := range integration.Operations() {
			ops = append(ops, opInfo{Name: op.Name, Kind: string(op.Kind), Description: op.Description})
		}
		return bridgeResult(c, map[string]any{
			"bridge_version": pageplugins.BridgeVersion,
			"instance":       map[string]string{"id": r.Instance.ID, "page_id": r.Page.ID, "title": r.Instance.Title, "project_id": r.Instance.ProjectID},
			"permissions":    r.Manifest.Permissions,
			"operations":     ops,
		})
	case pageplugins.BridgeOpInvoke:
		var p pageplugins.OpInvokePayload
		if err := pageplugins.DecodeStrict(msg.Payload, &p); err != nil {
			return bridgeError(c, http.StatusBadRequest, "invalid op.invoke payload")
		}
		out, err := pageintegration.Run(ctx, integration, pageintegration.Caller{PrincipalID: principal, Via: "page"}, p.Operation, p.Input)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, pageintegration.ErrOperationNotFound) {
				status = http.StatusNotFound
			}
			return bridgeError(c, status, err.Error())
		}
		return c.JSON(http.StatusOK, pluginBridgeResponse{OK: true, Result: out})
	case pageplugins.BridgeContextSet:
		var pc pageintegration.Context
		if err := pageplugins.DecodeStrict(msg.Payload, &pc); err != nil {
			return bridgeError(c, http.StatusBadRequest, "invalid context payload")
		}
		snap, err := h.pagePlugins.Contexts.Set(principal, r.Ref(), pc, "page:"+r.Page.ID)
		if err != nil {
			return bridgeError(c, http.StatusBadRequest, err.Error())
		}
		return bridgeResult(c, map[string]any{"revision": snap.Revision})
	case pageplugins.BridgeEventsSubscribe:
		if !r.Manifest.HasPermission(pageplugins.PermissionEvents) {
			return bridgeError(c, http.StatusForbidden, "plugin lacks the events permission")
		}
		return bridgeResult(c, map[string]bool{"subscribed": true})
	case pageplugins.BridgeAnalyzeRequest:
		if !r.Manifest.HasPermission(pageplugins.PermissionAgent) {
			return bridgeError(c, http.StatusForbidden, "plugin lacks the agent permission")
		}
		var p pageplugins.AnalyzePayload
		if err := pageplugins.DecodeStrict(msg.Payload, &p); err != nil || strings.TrimSpace(p.Prompt) == "" || utf8.RuneCountInString(p.Prompt) > maxPluginAnalyzePrompt {
			return bridgeError(c, http.StatusBadRequest, "analyze.request needs a prompt (max 4000 characters)")
		}
		exec, err := h.startPluginChatTurn(c, r, strings.TrimSpace(p.Prompt))
		if err != nil {
			return bridgeError(c, pluginChatStatus(err), err.Error())
		}
		return bridgeResult(c, map[string]string{"exec_id": exec.ID})
	}
	return bridgeError(c, http.StatusBadRequest, "unsupported message")
}

func pluginChatStatus(err error) int {
	var he *echo.HTTPError
	if errors.As(err, &he) {
		return he.Code
	}
	if errors.Is(err, errPluginChatBusy) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// PluginEvents streams instance events (refreshes, approval prompts) to the host page.
func (h *Handler) PluginEvents(c echo.Context) error {
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	if _, err := h.pluginHandleFor(c, r, c.QueryParam("handle")); err != nil {
		return err
	}
	ch, unsubscribe, ok := h.pagePlugins.Hub.Subscribe(r.Instance.ID)
	if !ok {
		return echo.NewHTTPError(http.StatusTooManyRequests, "too many subscribers for this page")
	}
	defer unsubscribe()
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, ": ok\n\n")
	w.Flush()
	ticker := time.NewTicker(pluginEventHeartbeat)
	defer ticker.Stop()
	ctx := c.Request().Context()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return nil
			}
			w.Flush()
		case ev, ok := <-ch:
			if !ok {
				_, _ = io.WriteString(w, "event: closed\ndata: {}\n\n")
				w.Flush()
				return nil
			}
			raw, _ := json.Marshal(ev)
			if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
				return nil
			}
			w.Flush()
		}
	}
}

// PluginRetireHandle invalidates a page-load handle (unload or navigation).
func (h *Handler) PluginRetireHandle(c echo.Context) error {
	if err := h.requirePagePlugins(); err != nil {
		return err
	}
	if hd, ok := h.pagePlugins.LookupHandle(c.QueryParam("handle")); ok && hd.InstanceID == c.Param("instanceId") && hd.PrincipalID == h.authPrincipalID(c) {
		h.pagePlugins.RetireHandle(hd.Token)
	}
	return c.NoContent(http.StatusNoContent)
}

// PluginChatSend starts a side-chat turn bound to this page instance.
func (h *Handler) PluginChatSend(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	if !r.Manifest.HasPermission(pageplugins.PermissionAgent) {
		return pluginNotFound()
	}
	message := strings.TrimSpace(c.FormValue("message"))
	if message == "" || utf8.RuneCountInString(message) > maxPluginChatMessage {
		return echo.NewHTTPError(http.StatusBadRequest, "message is required (max 8000 characters)")
	}
	exec, err := h.startPluginChatTurn(c, r, message)
	if err != nil {
		if pluginChatStatus(err) == http.StatusConflict {
			return echo.NewHTTPError(http.StatusConflict, err.Error())
		}
		return err
	}
	return render(c, http.StatusOK, components.ChatFollowupResponse(message, exec.ID, "plugin-chat-messages", "", false, nil, r.Instance.ProjectID, *exec))
}

// pluginConversationTask returns the instance's persisted side-chat task, creating it on first use.
func (h *Handler) pluginConversationTask(ctx context.Context, r *pageplugins.Resolved, message, agentID string) (*models.Task, error) {
	store := h.pagePlugins.Store()
	taskID, err := store.GetConversationTask(ctx, r.Instance.ID)
	if err != nil {
		return nil, err
	}
	if taskID != "" {
		task, err := h.taskRepo.GetByID(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if task != nil && task.ProjectID == r.Instance.ProjectID {
			return task, nil
		}
	}
	task := &models.Task{
		ProjectID: r.Instance.ProjectID,
		Title:     "Page chat: " + r.Instance.Title,
		Prompt:    message,
		Status:    models.StatusPending,
		Category:  pagePluginChatCategory,
		AgentID:   &agentID,
	}
	if err := h.taskRepo.Create(ctx, task); err != nil {
		return nil, err
	}
	if err := store.SetConversationTask(ctx, r.Instance.ID, task.ID); err != nil {
		_ = h.taskRepo.Delete(ctx, task.ID)
		return nil, err
	}
	return task, nil
}

// startPluginChatTurn runs one side-chat turn through the shared streaming
// pipeline with a per-turn page context snapshot and server-resolved page tools.
func (h *Handler) startPluginChatTurn(c echo.Context, r *pageplugins.Resolved, message string) (*models.Execution, error) {
	ctx := c.Request().Context()
	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		return nil, err
	}
	if !hasModels {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "configure a model before chatting")
	}
	projectID := r.Instance.ProjectID
	agent, err := h.selectTaskAgent(ctx, projectID, "", message, false)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	lock := persistence.LockTaskLifecycle("page-chat:" + r.Instance.ID)
	defer lock()
	task, err := h.pluginConversationTask(ctx, r, message, agent.ID)
	if err != nil {
		return nil, err
	}
	active, err := h.execRepo.HasActiveTaskThreadExecution(ctx, task.ID, "")
	if err != nil {
		return nil, err
	}
	if active || task.Status == models.StatusRunning || task.Status == models.StatusQueued {
		return nil, errPluginChatBusy
	}
	if task.Status != models.StatusPending {
		if err := h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusPending); err != nil {
			return nil, err
		}
	}
	claimed, err := h.taskRepo.ClaimTask(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, errPluginChatBusy
	}
	exec := &models.Execution{TaskID: task.ID, AgentConfigID: agent.ID, Status: models.ExecRunning, PromptSent: message, IsFollowup: true}
	if err := h.execRepo.Create(ctx, exec); err != nil {
		_ = h.taskRepo.UpdateStatus(ctx, task.ID, models.StatusFailed)
		return nil, err
	}
	principal := h.authPrincipalID(c)
	ref := r.Ref()
	var snap *pageintegration.Snapshot
	if s, ok := h.pagePlugins.Contexts.Get(principal, ref); ok {
		snap = &s
		raw, _ := json.Marshal(s)
		if err := h.pagePlugins.Store().SaveTurnContext(ctx, exec.ID, r.Instance.ID, string(raw)); err != nil {
			applog.Infof("[plugin-pages] save turn context exec=%s: %v", exec.ID, err)
		}
	}
	integration := h.pagePlugins.Integration(r)
	pageContext := pageintegration.RenderContextForPrompt(ref, snap, integration.Operations())
	tools := pageintegration.BuildRuntimeTools(integration, h.pagePlugins.Resolver(r.Instance.ID, projectID), h.pagePlugins.Approvals, pageintegration.Caller{PrincipalID: principal})

	history, err := h.execRepo.ListByTaskChronologicalLimit(persistence.WithModelHistory(ctx, agent.Provider), task.ID, taskThreadHistoryLimit)
	if err != nil {
		history = nil
	}
	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:         exec.ID,
		TaskID:         task.ID,
		Message:        message,
		Agent:          *agent,
		ChatHistory:    filterChatHistory(history, exec.ID),
		ProjectID:      projectID,
		PrincipalID:    principal,
		SystemContext:  combineContexts(h.getPersonalityContext(ctx, projectID), pageContext),
		WorkDir:        h.resolveWorkDir(ctx, projectID),
		IsTaskFollowup: false,
		TaskScopedChat: true,
		ChatMode:       models.ChatModeOrchestrate,
		Surface:        chatcontrol.SurfaceWeb,
		RuntimeTools:   tools,
	}); err != nil {
		return nil, echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}
	return exec, nil
}

// PluginChatStop cancels the running side-chat turn for this page.
func (h *Handler) PluginChatStop(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	taskID, err := h.pagePlugins.Store().GetConversationTask(ctx, r.Instance.ID)
	if err != nil {
		return err
	}
	if taskID == "" {
		return c.NoContent(http.StatusNoContent)
	}
	observed, cutoff, err := h.taskSvc.ObserveTaskCancellation(ctx, taskID)
	if err != nil || observed == nil || observed.ProjectID != r.Instance.ProjectID {
		return c.NoContent(http.StatusNoContent)
	}
	if observed.Status != models.StatusRunning && observed.Status != models.StatusQueued {
		return c.NoContent(http.StatusNoContent)
	}
	if err := h.taskSvc.CancelTaskObserved(ctx, observed, cutoff); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := h.taskRepo.UpdateCategory(ctx, taskID, pagePluginChatCategory); err != nil {
		return err
	}
	h.cancelActiveExecutionsAndPublish(ctx, taskID, "PluginChatStop", cutoff)
	return c.NoContent(http.StatusNoContent)
}

// PluginApprovals lists pending agent actions awaiting confirmation on this page.
func (h *Handler) PluginApprovals(c echo.Context) error {
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	v := pages.PluginPageView{InstanceID: r.Instance.ID, OwningProjectID: r.Instance.ProjectID}
	var cards []templ.Component
	for _, a := range h.pagePlugins.Approvals.List(r.Instance.ID, r.Instance.ProjectID) {
		cards = append(cards, pages.PluginApprovalCard(v, a.ID, a.Operation, string(a.Input)))
	}
	return render(c, http.StatusOK, templ.Join(cards...))
}

// PluginApprovalDecide approves or denies one pending agent action.
func (h *Handler) PluginApprovalDecide(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	decision := c.Param("decision")
	if decision != "approve" && decision != "deny" {
		return pluginNotFound()
	}
	if !h.pagePlugins.Approvals.Resolve(c.Param("approvalId"), r.Instance.ID, r.Instance.ProjectID, decision == "approve") {
		return pluginNotFound()
	}
	return c.HTML(http.StatusOK, "")
}

// requirePluginAdmin enforces administrator access for install/approval actions.
func (h *Handler) requirePluginAdmin(c echo.Context) (string, error) {
	if err := h.requirePagePlugins(); err != nil {
		return "", err
	}
	if c.Request().Method != http.MethodGet {
		if err := requirePluginMutation(c); err != nil {
			return "", err
		}
	}
	if h.organizationSvc != nil {
		p, ok := models.PrincipalFromContext(c.Request().Context())
		if !ok || !p.IsOrgAdmin() {
			return "", echo.NewHTTPError(http.StatusForbidden, "organization administrator required")
		}
	}
	return h.authPrincipalID(c), nil
}

func (h *Handler) pluginSettingsProject(c echo.Context) (string, error) {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return "", err
	}
	if projectID == "" || projectID == models.AllProjectsID {
		return "", echo.NewHTTPError(http.StatusBadRequest, "choose a project to manage its plugin pages")
	}
	return projectID, nil
}

func (h *Handler) pluginSettingsView(ctx context.Context, projectID string) (pages.PluginSettingsView, error) {
	v := pages.PluginSettingsView{ProjectID: projectID}
	store := h.pagePlugins.Store()
	pkgs, err := store.ListPackages(ctx)
	if err != nil {
		return v, err
	}
	for i := range pkgs {
		man, err := h.pagePlugins.Manifest(&pkgs[i])
		if err != nil {
			continue
		}
		n, _ := store.CountInstancesForPackage(ctx, pkgs[i].ID)
		row := pages.PluginSettingsPackage{Package: pkgs[i], RunsProcess: man.RunsProcess(), InUse: n, Permissions: pluginPermissions(man)}
		for _, p := range man.Pages {
			row.Pages = append(row.Pages, p.ID)
		}
		v.Packages = append(v.Packages, row)
	}
	ins, err := store.ListInstances(ctx, projectID, false)
	if err != nil {
		return v, err
	}
	for _, in := range ins {
		pkg, err := store.GetPackage(ctx, in.PackageID)
		if err != nil || pkg == nil {
			continue
		}
		man, err := h.pagePlugins.Manifest(pkg)
		if err != nil {
			continue
		}
		status, _ := h.pagePlugins.ProcessStatus(in.ID)
		v.Instances = append(v.Instances, pages.PluginSettingsRow{Instance: in, NeedsApproval: h.pagePlugins.NeedsApproval(&in, man), RunsProcess: man.RunsProcess(), Permissions: pluginPermissions(man), ProcessStatus: status})
	}
	return v, nil
}

func pluginPermissions(man *pageplugins.Manifest) []pages.PluginPermission {
	out := make([]pages.PluginPermission, 0, len(man.Permissions))
	for _, p := range man.Permissions {
		out = append(out, pages.PluginPermission{Name: p, Description: pageplugins.PermissionDescription(p)})
	}
	return out
}

func (h *Handler) renderPluginSettings(c echo.Context, projectID, notice string, actionErr error) error {
	ctx := c.Request().Context()
	v, err := h.pluginSettingsView(ctx, projectID)
	if err != nil {
		return err
	}
	v.Notice = notice
	if actionErr != nil {
		v.Error = actionErr.Error()
		v.Notice = ""
	} else if c.Request().Method == http.MethodPost {
		c.Response().Header().Set("HX-Trigger", "plugin-nav-refresh")
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.PluginSettingsContent(v))
	}
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.PluginSettings(projects, projectID, v))
}

// PluginSettings shows installed packages and this project's page instances.
func (h *Handler) PluginSettings(c echo.Context) error {
	if _, err := h.requirePluginAdmin(c); err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	return h.renderPluginSettings(c, projectID, "", nil)
}

func (h *Handler) PluginSettingsInstall(c echo.Context) error {
	by, err := h.requirePluginAdmin(c)
	if err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	pkg, err := h.pagePlugins.InstallFromDir(c.Request().Context(), strings.TrimSpace(c.FormValue("path")), by)
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	return h.renderPluginSettings(c, projectID, fmt.Sprintf("Installed %s %s", pkg.Name, pkg.Version), nil)
}

func (h *Handler) PluginSettingsInstallExample(c echo.Context) error {
	by, err := h.requirePluginAdmin(c)
	if err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	pkg, err := h.pagePlugins.InstallExample(c.Request().Context(), by)
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	return h.renderPluginSettings(c, projectID, fmt.Sprintf("Installed %s %s", pkg.Name, pkg.Version), nil)
}

func (h *Handler) PluginSettingsCreateInstance(c echo.Context) error {
	if _, err := h.requirePluginAdmin(c); err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	in, err := h.pagePlugins.CreateInstance(c.Request().Context(), projectID, c.FormValue("package_id"), c.FormValue("page_id"))
	if err != nil {
		return h.renderPluginSettings(c, projectID, "", err)
	}
	return h.renderPluginSettings(c, projectID, fmt.Sprintf("Added %q to this project (disabled until enabled)", in.Title), nil)
}

// PluginSettingsInstanceAction approves, enables, disables, or removes one project instance.
func (h *Handler) PluginSettingsInstanceAction(c echo.Context) error {
	by, err := h.requirePluginAdmin(c)
	if err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	ctx := c.Request().Context()
	in, err := h.pagePlugins.Store().GetInstance(ctx, c.Param("instanceId"))
	if err != nil {
		return err
	}
	if in == nil || in.ProjectID != projectID {
		return pluginNotFound()
	}
	var actionErr error
	notice := ""
	switch c.Param("action") {
	case "approve":
		actionErr, notice = h.pagePlugins.Approve(ctx, in.ID, by), "Approved backend for this package version"
	case "enable":
		actionErr, notice = h.pagePlugins.Enable(ctx, in.ID), "Enabled"
	case "disable":
		actionErr, notice = h.pagePlugins.Disable(ctx, in.ID), "Disabled"
	case "remove":
		actionErr, notice = h.pagePlugins.RemoveInstance(ctx, in.ID), "Removed"
	default:
		return pluginNotFound()
	}
	return h.renderPluginSettings(c, projectID, notice, actionErr)
}

func (h *Handler) PluginSettingsRemovePackage(c echo.Context) error {
	if _, err := h.requirePluginAdmin(c); err != nil {
		return err
	}
	projectID, err := h.pluginSettingsProject(c)
	if err != nil {
		return err
	}
	err = h.pagePlugins.RemovePackage(c.Request().Context(), c.Param("packageId"))
	return h.renderPluginSettings(c, projectID, "Package removed", err)
}
