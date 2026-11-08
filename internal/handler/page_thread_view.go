package handler

import (
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

func pluginThreadSurface(instanceID, projectID string) components.ThreadSurface {
	return components.ThreadSurface{BaseURL: "/api/plugin-pages/" + url.PathEscape(instanceID) + "/chat/thread", ProjectID: projectID, SendURL: pluginAPI(instanceID, projectID, "/chat"), StopURL: pluginAPI(instanceID, projectID, "/chat/stop")}
}

// Resolve the owning page before delegating to the proven task-thread readers.
// Client-supplied task IDs never select a conversation here.
func (h *Handler) pluginThreadContext(c echo.Context) error {
	r, err := h.resolvePluginForRequest(c, false)
	if err != nil {
		return err
	}
	taskID, err := h.pagePlugins.Conversation(r.Ref()).Store.TaskID(c.Request().Context())
	if err != nil {
		return err
	}
	if taskID == "" {
		return pluginNotFound()
	}
	names, values := append([]string{}, c.ParamNames()...), append([]string{}, c.ParamValues()...)
	c.SetParamNames(append(names, "taskId")...)
	c.SetParamValues(append(values, taskID)...)
	c.SetRequest(c.Request().WithContext(components.WithThreadSurface(c.Request().Context(), pluginThreadSurface(r.Instance.ID, r.Instance.ProjectID))))
	c.Response().Header().Set("Cache-Control", "no-store")
	return nil
}
func (h *Handler) PluginChatThread(c echo.Context) error {
	if err := h.pluginThreadContext(c); err != nil {
		return err
	}
	if c.QueryParam("limit") == "" {
		q := c.QueryParams()
		q.Set("limit", "50")
		c.Request().URL.RawQuery = q.Encode()
	}
	return h.GetTaskThread(c)
}
func (h *Handler) PluginChatThreadFragment(c echo.Context) error {
	if err := h.pluginThreadContext(c); err != nil {
		return err
	}
	return h.GetTaskThreadExecutionFragment(c)
}
func (h *Handler) PluginChatThreadPending(c echo.Context) error {
	if err := h.pluginThreadContext(c); err != nil {
		return err
	}
	inputs, err := h.threadInputRepo.ListPendingForTask(c.Request().Context(), c.Param("taskId"))
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, components.ChatComposerQueuedInputRowsForTask(inputs, func(input models.ThreadInput) string {
		return pluginQueuedSteerURL(c.Param("instanceId"), c.QueryParam("project_id"), input)
	}, c.Param("taskId")))
}
