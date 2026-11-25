package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/a-h/templ"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageplugins"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/pages"
)

type PluginRefArgs struct{ ProjectID, InstanceID string }

func (h *Handler) pluginOwner(ctx context.Context, a PluginRefArgs) (*pageplugins.Resolved, error) {
	if h.pagePlugins == nil {
		return nil, statusErr(http.StatusNotFound, "plugin page not found")
	}
	in, err := h.pagePlugins.Store().GetInstance(ctx, a.InstanceID)
	if err != nil {
		return nil, err
	}
	if in == nil || (a.ProjectID != "" && !models.IsAggregateProject(a.ProjectID) && a.ProjectID != in.ProjectID) {
		return nil, statusErr(http.StatusNotFound, "plugin page not found")
	}
	return h.pagePlugins.Resolve(ctx, in.ID, in.ProjectID)
}
func pluginActionOwner(permission authz.Permission) func(*Handler, context.Context, PluginRefArgs) (string, authz.Permission, error) {
	return func(h *Handler, ctx context.Context, a PluginRefArgs) (string, authz.Permission, error) {
		r, err := h.pluginOwner(ctx, a)
		if err != nil {
			return "", permission, err
		}
		return r.Instance.ProjectID, permission, nil
	}
}
func pluginPrincipal(ctx context.Context) string {
	if p, ok := models.PrincipalFromContext(ctx); ok {
		return "user:" + p.UserID
	}
	return "local"
}

var actPluginNav = defineAction("page.nav", func(_ *Handler, _ context.Context, a PluginRefArgs) (string, authz.Permission, error) {
	return a.ProjectID, authz.View, nil
}, func(h *Handler, ctx context.Context, a PluginRefArgs) ([]pages.PluginNavEntry, error) {
	return h.localPluginNavigation(ctx, a.ProjectID)
})

func (h *Handler) pluginNavigation(ctx context.Context, projectID string) ([]pages.PluginNavEntry, error) {
	ids := []string{projectID}
	if models.IsAggregateProject(projectID) {
		var err error
		ids, err = h.cardActionProjects(ctx, projectID)
		if err != nil {
			return nil, err
		}
	}
	var entries []pages.PluginNavEntry
	for _, id := range ids {
		rows, err := actPluginNav(h, ctx, PluginRefArgs{ProjectID: id})
		if err != nil {
			return nil, err
		}
		entries = append(entries, rows...)
	}
	return entries, nil
}

type pluginOpenResult struct {
	View      pages.PluginPageView
	Entry     string
	ExpiresAt time.Time
}

var actPluginOpen = defineAction("page.open", pluginActionOwner(authz.View), func(h *Handler, ctx context.Context, a PluginRefArgs) (pluginOpenResult, error) {
	r, err := h.pluginOwner(ctx, a)
	if err != nil {
		return pluginOpenResult{}, err
	}
	hd, err := h.pagePlugins.IssueHandle(r, pluginPrincipal(ctx))
	if err != nil {
		return pluginOpenResult{}, err
	}
	v := pluginView(r, hd)
	v.ChatAgents, err = h.llmConfigRepo.ListPickerOptions(ctx)
	if err != nil {
		return pluginOpenResult{}, err
	}
	binding := h.pagePlugins.Conversation(r.Ref())
	unlock := persistence.LockTaskLifecycle(binding.LockKey())
	_, err = h.pageConversationTask(ctx, binding, "", "")
	unlock()
	if err != nil {
		return pluginOpenResult{}, err
	}
	if err = h.loadPluginChatHistory(ctx, &v); err != nil {
		return pluginOpenResult{}, err
	}
	return pluginOpenResult{View: v, Entry: r.Page.Entry, ExpiresAt: hd.ExpiresAt}, nil
})

// The asset capability is bound to the desktop principal that opened it. Assets
// are cookie-free, but every chunk is still authorized by the owning server.
type remotePluginHandle struct {
	Args         PluginHandleArgs
	Principal    models.Principal
	HasPrincipal bool
	ExpiresAt    time.Time
}
type PluginHandleArgs struct {
	PluginRefArgs
	Handle string
}

func pluginHandleOwner(h *Handler, ctx context.Context, a PluginHandleArgs) (string, authz.Permission, error) {
	r, err := h.pluginOwner(ctx, a.PluginRefArgs)
	if err != nil {
		return "", authz.View, err
	}
	hd, ok := h.pagePlugins.LookupHandle(a.Handle)
	if !ok || hd.InstanceID != r.Instance.ID || hd.ProjectID != r.Instance.ProjectID || hd.PrincipalID != pluginPrincipal(ctx) {
		return "", authz.View, statusErr(http.StatusNotFound, "plugin page not found")
	}
	return r.Instance.ProjectID, authz.View, nil
}
func (h *Handler) remotePluginPage(c echo.Context) error {
	a := PluginRefArgs{InstanceID: c.Param("instanceId"), ProjectID: c.QueryParam("project_id")}
	if a.ProjectID == "" || models.IsAggregateProject(a.ProjectID) {
		in, err := actPluginInstance(h, c.Request().Context(), a)
		if err != nil {
			return err
		}
		target := "/p/" + url.PathEscape(in.ID) + "?project_id=" + url.QueryEscape(in.ProjectID)
		if isHTMX(c) {
			c.Response().Header().Set("HX-Redirect", target)
			return c.NoContent(http.StatusOK)
		}
		return c.Redirect(http.StatusSeeOther, target)
	}
	out, err := actPluginOpen(h, c.Request().Context(), a)
	if err != nil {
		return err
	}
	v := out.View
	server, _ := remoteid.ServerOf(v.InstanceID)
	v.OwningProjectID = remoteid.ID(server, v.OwningProjectID)
	// Attachment maps have explicit execution-ID keys.
	attachments := make(map[string][]models.ChatAttachment, len(v.ChatAttachments))
	for id, rows := range v.ChatAttachments {
		attachments[remoteid.ID(server, id)] = rows
	}
	v.ChatAttachments = attachments
	v.Handle = remoteid.ID(server, v.Handle)
	p, has := models.PrincipalFromContext(c.Request().Context())
	saved := remotePluginHandle{Args: PluginHandleArgs{PluginRefArgs: PluginRefArgs{InstanceID: v.InstanceID, ProjectID: v.OwningProjectID}, Handle: v.Handle}, Principal: p, HasPrincipal: has, ExpiresAt: out.ExpiresAt}
	h.remotePluginHandles.Store(v.Handle, saved)
	time.AfterFunc(time.Until(out.ExpiresAt), func() { h.remotePluginHandles.Delete(v.Handle) })
	v.AssetURL = "/plugin-assets/" + url.PathEscape(v.Handle) + "/" + out.Entry + "?handle=" + url.QueryEscape(v.Handle)
	c.SetRequest(c.Request().WithContext(components.WithThreadSurface(c.Request().Context(), pluginThreadSurface(v.InstanceID, v.OwningProjectID))))
	c.Response().Header().Set("Cache-Control", "no-store")
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.PluginPageContent(v))
	}
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.PluginPage(projects, v.OwningProjectID, v))
}

const pluginAssetChunkSize = 128 * 1024

type pluginAssetArgs struct {
	PluginHandleArgs
	Path   string
	Offset int64
}
type pluginAssetChunk struct {
	Data        []byte
	Size        int64
	ContentType string
	Entry       bool
}

var actPluginAsset = defineAction("page.asset", func(h *Handler, ctx context.Context, a pluginAssetArgs) (string, authz.Permission, error) {
	return pluginHandleOwner(h, ctx, a.PluginHandleArgs)
}, func(h *Handler, ctx context.Context, a pluginAssetArgs) (pluginAssetChunk, error) {
	if a.Offset < 0 {
		return pluginAssetChunk{}, pluginNotFound()
	}
	r, err := h.pluginOwner(ctx, a.PluginRefArgs)
	if err != nil {
		return pluginAssetChunk{}, err
	}
	rel := strings.TrimPrefix(path.Clean("/"+a.Path), "/")
	if rel == pageplugins.ManifestFile || r.Manifest.Backend != nil && isPluginExecutable(r.Manifest.Backend, rel) {
		return pluginAssetChunk{}, pluginNotFound()
	}
	ctype, ok := pluginAssetTypes[strings.ToLower(path.Ext(rel))]
	if !ok {
		ctype = mime.TypeByExtension(strings.ToLower(path.Ext(rel)))
		if !strings.HasPrefix(ctype, "image/") {
			return pluginAssetChunk{}, pluginNotFound()
		}
	}
	full, err := h.pagePlugins.AssetPath(r, rel)
	if err != nil {
		return pluginAssetChunk{}, pluginNotFound()
	}
	f, err := os.Open(full)
	if err != nil {
		return pluginAssetChunk{}, pluginNotFound()
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || a.Offset > st.Size() {
		return pluginAssetChunk{}, pluginNotFound()
	}
	data := make([]byte, pluginAssetChunkSize)
	n, err := f.ReadAt(data, a.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return pluginAssetChunk{}, err
	}
	return pluginAssetChunk{Data: data[:n], Size: st.Size(), ContentType: ctype, Entry: rel == r.Page.Entry}, nil
})

func (h *Handler) remotePluginAsset(c echo.Context) error {
	token := c.Param("handle")
	value, ok := h.remotePluginHandles.Load(token)
	if !ok {
		return pluginNotFound()
	}
	saved := value.(remotePluginHandle)
	if time.Now().After(saved.ExpiresAt) {
		h.remotePluginHandles.Delete(token)
		return pluginNotFound()
	}
	ctx := c.Request().Context()
	if saved.HasPrincipal {
		if h.organizationSvc != nil {
			user, err := h.organizationSvc.Store.UserByID(ctx, saved.Principal.UserID)
			if err != nil || user == nil || user.Disabled || user.Role == "" {
				return pluginNotFound()
			}
		}
		ctx = models.WithPrincipal(ctx, saved.Principal)
	}
	a := pluginAssetArgs{PluginHandleArgs: saved.Args, Path: c.Param("*")}
	chunk, err := actPluginAsset(h, ctx, a)
	if err != nil {
		return err
	}
	policy := pluginAssetCSP
	if h.desktopMode && c.Request().Header.Get("X-OpenVibely-Native-Window") == "1" {
		policy = nativePluginAssetCSP(token)
	}
	hdr := c.Response().Header()
	hdr.Set("Content-Security-Policy", policy)
	hdr.Set("X-Content-Type-Options", "nosniff")
	hdr.Set("Referrer-Policy", "no-referrer")
	hdr.Set("Cache-Control", "no-store")
	hdr.Set("Content-Type", chunk.ContentType)
	hdr.Set("Access-Control-Allow-Origin", "*")
	hdr.Set("X-Frame-Options", "SAMEORIGIN")
	c.Response().WriteHeader(http.StatusOK)
	for {
		data := chunk.Data
		if a.Offset == 0 && chunk.Entry {
			data = []byte(instrumentPluginHTML(string(data), token))
		}
		if _, err = c.Response().Write(data); err != nil {
			return err
		}
		a.Offset += int64(len(chunk.Data))
		if a.Offset >= chunk.Size {
			return nil
		}
		if len(chunk.Data) == 0 {
			return io.ErrUnexpectedEOF
		}
		chunk, err = actPluginAsset(h, ctx, a)
		if err != nil {
			return err
		}
	}
}

type pluginBridgeReadyInstance struct {
	ID, Title, ProjectID string
	PageID               string `remote:"raw"`
}
type pluginBridgeActionResult struct {
	Data     json.RawMessage `remote:"raw"`
	ExecID   string
	Instance *pluginBridgeReadyInstance
}
type pluginBridgeArgs struct {
	PluginHandleArgs
	AgentID string
	Effort  effortForm
	Type    string
	Payload json.RawMessage `remote:"raw"`
}

var actPluginBridge = defineAction("page.bridge", func(h *Handler, ctx context.Context, a pluginBridgeArgs) (string, authz.Permission, error) {
	return pluginHandleOwner(h, ctx, a.PluginHandleArgs)
}, func(h *Handler, ctx context.Context, a pluginBridgeArgs) (pluginBridgeActionResult, error) {
	r, err := h.pluginOwner(ctx, a.PluginRefArgs)
	if err != nil {
		return pluginBridgeActionResult{}, err
	}
	raw, err := json.Marshal(map[string]any{"v": pageplugins.BridgeVersion, "id": "remote", "type": a.Type, "handle": a.Handle, "payload": a.Payload})
	if err != nil {
		return pluginBridgeActionResult{}, err
	}
	msg, err := pageplugins.ParseBridgeMessage(raw, a.Handle)
	if err != nil {
		return pluginBridgeActionResult{}, statusErr(http.StatusBadRequest, err.Error())
	}
	hd, ok := h.pagePlugins.LookupHandle(a.Handle)
	if !ok {
		return pluginBridgeActionResult{}, pluginNotFound()
	}
	raw, err = h.runPluginBridge(ctx, r, hd, msg, pluginPrincipal(ctx), a.AgentID, a.Effort)
	if err != nil {
		return pluginBridgeActionResult{}, err
	}
	out := pluginBridgeActionResult{Data: raw}
	if a.Type == pageplugins.BridgeAnalyzeRequest {
		var result struct {
			ExecID string `json:"exec_id"`
		}
		if err = json.Unmarshal(raw, &result); err != nil {
			return out, err
		}
		out.ExecID = result.ExecID
		out.Data = nil
	}
	if a.Type == pageplugins.BridgeReady {
		out.Instance = &pluginBridgeReadyInstance{ID: r.Instance.ID, PageID: r.Page.ID, Title: r.Instance.Title, ProjectID: r.Instance.ProjectID}
	}
	return out, nil
})

func (h *Handler) remotePluginBridge(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(c.Request().Body, pageplugins.MaxBridgeMessageBytes+1))
	if err != nil {
		return err
	}
	var envelope struct {
		Handle string `json:"handle"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return bridgeError(c, http.StatusBadRequest, "invalid bridge message")
	}
	msg, err := pageplugins.ParseBridgeMessage(raw, envelope.Handle)
	if err != nil {
		return bridgeError(c, http.StatusBadRequest, err.Error())
	}
	out, err := actPluginBridge(h, c.Request().Context(), pluginBridgeArgs{PluginHandleArgs: PluginHandleArgs{PluginRefArgs: PluginRefArgs{ProjectID: c.QueryParam("project_id"), InstanceID: c.Param("instanceId")}, Handle: envelope.Handle}, Type: msg.Type, Payload: msg.Payload, AgentID: c.QueryParam("agent_id"), Effort: parseEffortForm(c)})
	if err != nil {
		return err
	}
	raw = out.Data
	if out.ExecID != "" {
		raw, _ = json.Marshal(map[string]string{"exec_id": out.ExecID})
	}
	if out.Instance != nil {
		var ready map[string]json.RawMessage
		if err = json.Unmarshal(raw, &ready); err != nil {
			return err
		}
		ready["instance"], _ = json.Marshal(map[string]string{"id": out.Instance.ID, "page_id": out.Instance.PageID, "title": out.Instance.Title, "project_id": out.Instance.ProjectID})
		raw, _ = json.Marshal(ready)
	}
	return c.JSON(http.StatusOK, pluginBridgeResponse{OK: true, Result: raw})
}

type pluginChatArgs struct {
	PluginRefArgs
	Message string `remote:"raw"`
	Input   pageChatInput
	Effort  effortForm
}
type pluginChatResult struct {
	Exec                  *models.Execution
	Attachments           []models.ChatAttachment
	Queued                *models.ThreadInput
	ProjectID, InstanceID string
}

var actPluginChat = defineAction("page.chat", func(h *Handler, ctx context.Context, a pluginChatArgs) (string, authz.Permission, error) {
	return pluginActionOwner(authz.Edit)(h, ctx, a.PluginRefArgs)
}, func(h *Handler, ctx context.Context, a pluginChatArgs) (pluginChatResult, error) {
	r, err := h.pluginOwner(ctx, a.PluginRefArgs)
	if err != nil {
		return pluginChatResult{}, err
	}
	message := strings.TrimSpace(a.Message)
	if message == "" || utf8.RuneCountInString(message) > maxPluginChatMessage {
		return pluginChatResult{}, statusErr(http.StatusBadRequest, "message is required (max 8000 characters)")
	}
	exec, attachments, err := h.startPageChatTurnOnOwner(ctx, h.pagePlugins.Conversation(r.Ref()), message, a.Input, pluginPrincipal(ctx), a.Effort)
	out := pluginChatResult{Exec: exec, Attachments: attachments, InstanceID: r.Instance.ID, ProjectID: r.Instance.ProjectID}
	var queued *pageChatQueued
	if errors.As(err, &queued) {
		out.Queued = &queued.Input
		err = nil
	}
	return out, err
})

type pluginThreadResult struct{ ProjectID, InstanceID, TaskID, ExecID string }

var actPluginThread = defineAction("page.thread", pluginActionOwner(authz.View), func(h *Handler, ctx context.Context, a PluginRefArgs) (pluginThreadResult, error) {
	r, err := h.pluginOwner(ctx, a)
	if err != nil {
		return pluginThreadResult{}, err
	}
	taskID, err := h.pagePlugins.Conversation(r.Ref()).Store.TaskID(ctx)
	if err != nil {
		return pluginThreadResult{}, err
	}
	execID, err := h.pluginChatRunningExec(ctx, r.Instance.ID)
	return pluginThreadResult{ProjectID: r.Instance.ProjectID, InstanceID: r.Instance.ID, TaskID: taskID, ExecID: execID}, err
})

type pluginStopArgs struct {
	PluginRefArgs
	ExecID string
}

var actPluginStop = defineAction("page.stop", func(h *Handler, ctx context.Context, a pluginStopArgs) (string, authz.Permission, error) {
	return pluginActionOwner(authz.Edit)(h, ctx, a.PluginRefArgs)
}, func(h *Handler, ctx context.Context, a pluginStopArgs) (bool, error) {
	return true, h.cancelPluginConversation(ctx, a.InstanceID, a.ExecID)
})
var actPluginRetire = defineAction("page.retire", func(h *Handler, ctx context.Context, a PluginHandleArgs) (string, authz.Permission, error) {
	return pluginActionOwner(authz.View)(h, ctx, a.PluginRefArgs)
}, func(h *Handler, ctx context.Context, a PluginHandleArgs) (bool, error) {
	if hd, ok := h.pagePlugins.LookupHandle(a.Handle); ok && hd.InstanceID == a.InstanceID && hd.PrincipalID == pluginPrincipal(ctx) {
		h.pagePlugins.RetireHandle(a.Handle)
	}
	return true, nil
})

func pluginRequestRef(c echo.Context) PluginRefArgs {
	return PluginRefArgs{InstanceID: c.Param("instanceId"), ProjectID: c.QueryParam("project_id")}
}
func (h *Handler) remotePluginChat(c echo.Context) error {
	if err := requirePluginMutation(c); err != nil {
		return err
	}
	out, err := actPluginChat(h, c.Request().Context(), pluginChatArgs{PluginRefArgs: pluginRequestRef(c), Message: c.FormValue("message"), Input: pageChatInput{AllowQueue: true, AgentID: c.FormValue("agent_id"), AttachmentSessionID: strings.TrimSpace(c.FormValue("attachment_session_id"))}, Effort: parseEffortForm(c)})
	if err != nil {
		return err
	}
	if q := out.Queued; q != nil {
		return render(c, http.StatusOK, components.ChatQueuedInputRowOOBForTask(q.ID, q.Content, pluginQueuedSteerURL(out.InstanceID, out.ProjectID, *q), q.AttachmentSessionID != "", q.TaskID, q.AuthorName))
	}
	return render(c, http.StatusOK, templ.Join(components.TaskThreadFollowupResponse(strings.TrimSpace(c.FormValue("message")), out.Exec.ID, out.Attachments, out.ProjectID, *out.Exec), components.ChatComposerActionButtonOOB(components.PageChatFormID+"-primary-action", pluginAPI(out.InstanceID, out.ProjectID, "/chat/stop"), true, out.Exec.ID)))
}
func (h *Handler) remotePluginThreadContext(c echo.Context) error {
	out, err := actPluginThread(h, c.Request().Context(), pluginRequestRef(c))
	if err != nil {
		return err
	}
	if out.TaskID == "" {
		return pluginNotFound()
	}
	names, values := append([]string{}, c.ParamNames()...), append([]string{}, c.ParamValues()...)
	c.SetParamNames(append(names, "taskId")...)
	c.SetParamValues(append(values, out.TaskID)...)
	c.SetRequest(c.Request().WithContext(components.WithThreadSurface(c.Request().Context(), pluginThreadSurface(out.InstanceID, out.ProjectID))))
	c.Response().Header().Set("Cache-Control", "no-store")
	return nil
}

func (h *Handler) streamPluginEvents(ctx context.Context, cmd liveCommand, send func(liveFrame) bool) {
	if !h.liveSubscriptionAllowed(ctx, cmd) {
		send(liveFrame{Event: "error", Data: "page unavailable"})
		return
	}
	hd, ok := h.pagePlugins.LookupHandle(cmd.Handle)
	if !ok {
		return
	}
	ch, unsubscribe, ok := h.pagePlugins.Hub.Subscribe(cmd.InstanceID)
	if !ok {
		send(liveFrame{Event: "error", Data: "too many page subscribers"})
		return
	}
	defer unsubscribe()
	tick := time.NewTicker(pluginEventHeartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hd.Done():
			send(liveFrame{Event: "done"})
			return
		case <-tick.C:
			if !h.liveSubscriptionAllowed(ctx, cmd) {
				send(liveFrame{Event: "done"})
				return
			}
		case ev, ok := <-ch:
			if !ok || !h.liveSubscriptionAllowed(ctx, cmd) {
				send(liveFrame{Event: "done"})
				return
			}
			if !send(liveFrame{Event: "page_event", Data: ev}) {
				return
			}
		}
	}
}
func (h *Handler) remotePluginEvents(c echo.Context) error {
	ctx := c.Request().Context()
	a := PluginHandleArgs{PluginRefArgs: pluginRequestRef(c), Handle: c.QueryParam("handle")}
	// Validate the capability before committing an SSE response.
	out, err := actPluginEventAccess(h, ctx, a)
	if err != nil {
		return err
	}
	server, _ := remoteid.ServerOf(out.ProjectID)
	conn := h.remoteHub().Get(principalUserID(ctx), server)
	if conn == nil || !h.remoteSignedIn(ctx, server) {
		return reconnectError{server}
	}
	strip := func(id string) string { return remoteid.Strip(server, id) }
	frames, _, stop, err := conn.Subscribe(ctx, map[string]any{"op": "page", "project_id": strip(out.ProjectID), "instance_id": strip(out.InstanceID), "handle": strip(a.Handle)})
	if err != nil {
		return err
	}
	defer stop()
	w := c.Response()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if _, err = io.WriteString(w, ": ok\n\n"); err != nil {
		return nil
	}
	w.Flush()
	tick := time.NewTicker(pluginEventHeartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
			if _, err = io.WriteString(w, ": ping\n\n"); err != nil {
				return nil
			}
			w.Flush()
		case frame, ok := <-frames:
			if !ok || frame.Event == "retry" {
				return nil
			}
			if frame.Event == "done" || frame.Event == "error" {
				_, _ = io.WriteString(w, "event: closed\ndata: {}\n\n")
				w.Flush()
				return nil
			}
			if frame.Event == "page_event" {
				if _, err = fmt.Fprintf(w, "data: %s\n\n", frame.Data); err != nil {
					return nil
				}
				w.Flush()
			}
		}
	}
}

var actPluginEventAccess = defineAction("page.events", pluginHandleOwner, func(h *Handler, ctx context.Context, a PluginHandleArgs) (pluginThreadResult, error) {
	r, err := h.pluginOwner(ctx, a.PluginRefArgs)
	if err != nil {
		return pluginThreadResult{}, err
	}
	return pluginThreadResult{ProjectID: r.Instance.ProjectID, InstanceID: r.Instance.ID}, nil
})

var actPluginInstance = defineAction("page.instance", pluginActionOwner(authz.View), func(h *Handler, ctx context.Context, a PluginRefArgs) (*models.PagePluginInstance, error) {
	r, err := h.pluginOwner(ctx, a)
	if err != nil {
		return nil, err
	}
	return &r.Instance, nil
})
