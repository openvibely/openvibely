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
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// pagePluginChatCategory hides page side-chat tasks from boards and from the
// main project chat history (which is keyed by category=chat).
const pagePluginChatCategory = pageConversationCategory

const (
	maxPluginChatMessage   = 8000
	maxPluginAnalyzePrompt = 4000
	pluginEventHeartbeat   = 25 * time.Second
)

var errPluginChatBusy = errPageChatBusy

// SetPagePlugins wires the plugin page manager.
func (h *Handler) SetPagePlugins(m *pageplugins.Manager) {
	h.pagePlugins = m
	if m != nil {
		m.OnDisable = func(ctx context.Context, instanceID string) error {
			return h.cancelPluginConversation(ctx, instanceID)
		}
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
	in, err := h.pagePlugins.Store().GetInstance(ctx, v.InstanceID)
	if err != nil || in == nil {
		return err
	}
	ref := pageintegration.PageRef{Kind: pageplugins.Kind, InstanceID: in.ID, ProjectID: in.ProjectID, PageID: in.PageID, Title: in.Title}
	task, execs, err := h.loadPageChatHistory(ctx, h.pagePlugins.Conversation(ref))
	if err != nil {
		return err
	}
	if task != nil {
		v.ChatTaskID, v.ChatTask, v.ChatExecutions = task.ID, task, execs
	}
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
	if rel == r.Page.Entry {
		data, err := io.ReadAll(io.LimitReader(f, 64<<20))
		if err != nil {
			return err
		}
		return c.HTML(http.StatusOK, instrumentPluginHTML(string(data), hd.Token))
	}
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
	Result json.RawMessage `json:"result,omitempty" swaggertype:"object"`
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
// @Summary Run a plugin page bridge call
// @Description Executes one validated v1 bridge message relayed by the host page. Body is the bridge envelope including the page-load handle.
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Accept json
// @Produce json
// @Success 200 {object} pluginBridgeResponse
// @Failure 400 {object} pluginBridgeResponse
// @Failure 403 {object} pluginBridgeResponse
// @Failure 404 {object} ErrorResponse
// @Router /api/plugin-pages/{instanceId}/bridge [post]
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
	case pageplugins.BridgePageLoaded, pageplugins.BridgePageFailed:
		var report struct {
			Error string `json:"error,omitempty"`
		}
		if err := pageplugins.DecodeStrict(msg.Payload, &report); err != nil {
			return bridgeError(c, http.StatusBadRequest, "invalid startup report")
		}
		state := "loaded"
		if msg.Type == pageplugins.BridgePageFailed {
			state = "failed"
		}
		if err := h.pagePlugins.ReportStartup(hd.Token, state, report.Error); err != nil {
			return bridgeError(c, http.StatusBadRequest, err.Error())
		}
		return bridgeResult(c, h.pagePlugins.StartupStatus(r.Instance.ID, r.Package.ContentHash))
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
// @Summary Stream plugin page events
// @Description Server-sent events for refreshes and approval prompts.
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Param handle query string true "Page-load handle"
// @Produce text/event-stream
// @Success 200 {string} string "event stream"
// @Failure 404 {object} ErrorResponse
// @Router /api/plugin-pages/{instanceId}/events [get]
func (h *Handler) PluginEvents(c echo.Context) error {
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	hd, err := h.pluginHandleFor(c, r, c.QueryParam("handle"))
	if err != nil {
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
		case <-hd.Done():
			_, _ = io.WriteString(w, "event: closed\ndata: {}\n\n")
			w.Flush()
			return nil
		case <-ticker.C:
			if !h.pluginSubscriptionAuthorized(c, r, hd) {
				return nil
			}
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
			if !h.pluginSubscriptionAuthorized(c, r, hd) {
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

// Recheck the capability and current account/instance on heartbeat and before
// delivery. Disable/retire additionally close the handle immediately.
func (h *Handler) pluginSubscriptionAuthorized(c echo.Context, r *pageplugins.Resolved, hd pageplugins.Handle) bool {
	if _, err := h.pluginHandleFor(c, r, hd.Token); err != nil {
		return false
	}
	if _, err := h.pagePlugins.Resolve(c.Request().Context(), r.Instance.ID, r.Instance.ProjectID); err != nil {
		return false
	}
	if h.organizationSvc != nil {
		if !strings.HasPrefix(hd.PrincipalID, "user:") {
			return false
		}
		user, err := h.organizationSvc.Store.UserByID(c.Request().Context(), strings.TrimPrefix(hd.PrincipalID, "user:"))
		if err != nil || user == nil || user.Disabled || user.Role == "" {
			return false
		}
	}
	return true
}

// PluginRetireHandle invalidates a page-load handle (unload or navigation).
// @Summary Retire a page-load handle
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Param handle query string true "Page-load handle"
// @Success 204
// @Router /api/plugin-pages/{instanceId}/handle/retire [post]
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
// @Summary Send a page side-chat message
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Accept x-www-form-urlencoded
// @Param message formData string true "Message (max 8000 characters)"
// @Produce html
// @Success 200 {string} string "Rendered chat turn"
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /api/plugin-pages/{instanceId}/chat [post]
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
	return render(c, http.StatusOK, components.ChatFollowupResponse(message, exec.ID, "page-chat-messages", "", false, nil, r.Instance.ProjectID, *exec))
}

// The plugin adapter supplies identity, authorization, storage and lifetime;
// conversation execution itself is independent of plugins and iframes.
func (h *Handler) startPluginChatTurn(c echo.Context, r *pageplugins.Resolved, message string) (*models.Execution, error) {
	return h.startPageChatTurn(c, h.pagePlugins.Conversation(r.Ref()), message)
}

// PluginChatStop cancels the running side-chat turn for this page.
// @Summary Stop the page side-chat turn
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Success 204
// @Failure 404 {object} ErrorResponse
// @Router /api/plugin-pages/{instanceId}/chat/stop [post]
func (h *Handler) PluginChatStop(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	if err := h.cancelPluginConversation(c.Request().Context(), r.Instance.ID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// Shared by the Stop button and disable/remove. Inline analysis uses the same
// conversation task, so it is cancelled through the same execution machinery.
func (h *Handler) cancelPluginConversation(ctx context.Context, instanceID string) error {
	in, err := h.pagePlugins.Store().GetInstance(ctx, instanceID)
	if err != nil || in == nil {
		return err
	}
	ref := pageintegration.PageRef{Kind: pageplugins.Kind, PageID: in.PageID, InstanceID: in.ID, ProjectID: in.ProjectID, Title: in.Title}
	return h.cancelPageConversation(ctx, h.pagePlugins.Conversation(ref))
}

// PluginApprovals lists pending agent actions awaiting confirmation on this page.
// @Summary List pending page agent approvals
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Produce html
// @Success 200 {string} string "Rendered approval cards"
// @Failure 404 {object} ErrorResponse
// @Router /api/plugin-pages/{instanceId}/approvals [get]
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
// @Summary Approve or deny a page agent action
// @Tags plugin-pages
// @Param instanceId path string true "Plugin page instance ID"
// @Param approvalId path string true "Approval ID"
// @Param decision path string true "Decision" Enums(approve, deny)
// @Success 200
// @Failure 404 {object} ErrorResponse
// @Router /api/plugin-pages/{instanceId}/approvals/{approvalId}/{decision} [post]
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
		actionErr, notice = h.pagePlugins.Approve(ctx, in.ID, c.FormValue("content_hash"), by), "Approved backend for this package version"
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
