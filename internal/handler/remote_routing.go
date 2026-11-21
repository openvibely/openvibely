package handler

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func sameOriginRequest(r *http.Request, scheme string, desktop bool) bool {
	raw := r.Header.Get("Origin")
	if raw == "" {
		raw = r.Header.Get("Referer")
	}
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return desktop && nativeLiveOrigin(raw)
	}
	if desktop && nativeLiveOrigin(u.Scheme+"://"+u.Host) {
		return true
	}
	return strings.EqualFold(u.Host, r.Host) && u.Scheme == scheme
}

func routingSkipped(path string) bool {
	return path == "/events/ws" || path == "/login" || path == "/logout" || strings.HasPrefix(path, "/servers") ||
		strings.HasPrefix(path, "/ui/") || strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/auth/")
}

// remoteRouting scopes a remote project's page reads to its server, and asks
// the user to sign in again when a request names a server with no live socket.
func (h *Handler) remoteRouting(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		r := c.Request()
		// Models are installation-wide settings. Project selection is navigation
		// state only; it must never select a remote settings store.
		if modelManagementPath(r.URL.Path) {
			if _, remote := remoteid.FindServer(r.URL.Path); remote {
				return echo.NewHTTPError(http.StatusBadRequest, "Manage remote models on their server's OV app.")
			}
			for _, field := range []string{"model_config_id", "config_id", "oauth_connection_id", "new_default_id"} {
				for _, value := range []string{c.QueryParam(field), c.FormValue(field)} {
					if _, remote := remoteid.ServerOf(value); remote {
						return echo.NewHTTPError(http.StatusBadRequest, "Manage remote models on their server's OV app.")
					}
				}
			}
			c.SetRequest(r.WithContext(remote.WithServer(r.Context(), "")))
			return next(c)
		}
		if capacityAPIPath(r.URL.Path) {
			if _, isRemote := remoteid.FindServer(r.URL.Path); isRemote {
				return echo.NewHTTPError(http.StatusBadRequest, "Read remote capacity on its server's OV API.")
			}
		}
		if instanceSettingsPath(r.URL.Path) || instanceAccountProjection(c) {
			c.SetRequest(r.WithContext(remote.WithServer(r.Context(), "")))
			return next(c)
		}
		if mixedSettingsPath(r.URL.Path) {
			c.SetRequest(r.WithContext(remote.WithServer(r.Context(), "")))
			err := h.checkSettingsAgentOwner(c)
			if err == nil {
				err = next(c)
			}
			var reconnect reconnectError
			if errors.As(err, &reconnect) {
				return remoteReconnect(c, reconnect.server)
			}
			return err
		}
		if routingSkipped(r.URL.Path) {
			return next(c)
		}
		// Only the path and project_id name a page's server; other query values
		// may be free text, and remote IDs in them route their own store calls.
		server, _ := remoteid.FindServer(r.URL.Path)
		if server == "" {
			server, _ = remoteid.ServerOf(r.URL.Query().Get("project_id"))
		}
		if server != "" {
			page := r.Method == http.MethodGet && (isHTMX(c) || strings.Contains(r.Header.Get("Accept"), "text/html"))
			signedIn := h.remoteSignedIn(r.Context(), server)
			if page && (!signedIn && h.restoring() || signedIn && h.remoteDown(r.Context(), server)) {
				return h.serverWaiting(c, server)
			}
			if !signedIn {
				return remoteReconnect(c, server)
			}
			c.SetRequest(r.WithContext(remote.WithServer(r.Context(), server)))
		}
		err := next(c)
		var reconnect reconnectError
		if errors.As(err, &reconnect) {
			return remoteReconnect(c, reconnect.server)
		}
		return err
	}
}

// reconnectError reports an action for a saved server the user is not signed in to.
type reconnectError struct{ server string }

func (e reconnectError) Error() string { return "remote server session expired" }

func (h *Handler) remoteSignedIn(ctx context.Context, server string) bool {
	userID := principalUserID(ctx)
	s := h.remoteSessions.get(userID, server)
	return s != nil && !s.expired && h.remoteHub().Get(userID, server) != nil
}

// remoteDown reports a signed-in server whose socket is not open, including
// one still making its first attempt.
func (h *Handler) remoteDown(ctx context.Context, server string) bool {
	conn := h.remoteHub().Get(principalUserID(ctx), server)
	return conn != nil && !conn.Connected()
}

// waitingServers maps the names of enabled saved servers that are not connected, by ID.
func (h *Handler) waitingServers(ctx context.Context, connected []layout.RemoteServer) (map[string]string, error) {
	saved, err := h.loadSavedServers(ctx)
	if err != nil {
		return nil, err
	}
	up := map[string]bool{}
	for _, server := range connected {
		up[server.ID] = true
	}
	waiting := map[string]string{}
	for _, server := range saved {
		if server.Enabled && !up[server.ID] {
			waiting[server.ID] = server.Name
		}
	}
	return waiting, nil
}

func (h *Handler) serverWaiting(c echo.Context, serverID string) error {
	name := "the server"
	if saved, err := h.findSavedServer(c.Request().Context(), serverID); err == nil && saved != nil && saved.Name != "" {
		name = saved.Name
	}
	return h.serverWaitingNamed(c, name)
}

func (h *Handler) serverWaitingNamed(c echo.Context, name string) error {
	ctx := c.Request().Context()
	c.Response().Header().Set("Cache-Control", "no-store")
	if isHTMX(c) {
		return render(c, http.StatusOK, layout.ServerWaitingContent(name))
	}
	projects, err := h.projectSvc.ListSelectorOptions(ctx)
	if err != nil {
		return err
	}
	current := c.QueryParam("project_id")
	if _, ok := remoteid.ServerOf(current); ok || models.IsSharedProjectID(current) {
		// The project's own name arrives with the server; until then its tab shows the server.
		projects = append(projects, models.Project{ID: current, Name: name})
	}
	return render(c, http.StatusOK, layout.ServerWaiting(projects, current, name))
}

func remoteReconnect(c echo.Context, serverID string) error {
	target := "/servers?reconnect=" + url.QueryEscape(serverID)
	if c.Request().Header.Get("HX-Request") == "true" {
		c.Response().Header().Set("HX-Redirect", target)
		return c.NoContent(http.StatusUnauthorized)
	}
	if c.Request().Method == http.MethodGet && strings.Contains(c.Request().Header.Get("Accept"), "text/html") {
		return c.Redirect(http.StatusFound, target)
	}
	return c.JSON(http.StatusUnauthorized, map[string]any{"error": "remote server session expired", "reconnect": target})
}

// remoteServerURL is the saved URL of the server owning a remote entity, or "".
func (h *Handler) remoteServerURL(ctx context.Context, entityID string) string {
	server, ok := remoteid.ServerOf(entityID)
	if !ok {
		return ""
	}
	if saved, err := h.findSavedServer(ctx, server); err == nil && saved != nil {
		return saved.URL
	}
	return ""
}

func modelManagementPath(path string) bool {
	return path == "/models" || strings.HasPrefix(path, "/models/")
}
