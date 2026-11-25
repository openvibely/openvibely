package handler

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) serversPageData(c echo.Context) (pages.ServersPageData, error) {
	ctx := c.Request().Context()
	servers, err := h.serverViews(ctx)
	if err != nil {
		return pages.ServersPageData{}, err
	}
	data := pages.ServersPageData{
		Servers:     servers,
		Search:      c.QueryParam("search"),
		Reconnect:   c.QueryParam("reconnect"),
		DesktopMode: h.desktopMode,
	}
	if h.desktopMode && h.localBackend != nil {
		data.LocalBackend = h.localBackend.Mode()
	}
	return data, nil
}

func (h *Handler) Servers(c echo.Context) error {
	data, err := h.serversPageData(c)
	if err != nil {
		return err
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ServersContent(data))
	}
	projectID, _ := h.getCurrentProjectID(c)
	projects, err := h.projectSvc.List(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.Servers(projects, projectID, data))
}

func (h *Handler) renderServersList(c echo.Context, status int) error {
	data, err := h.serversPageData(c)
	if err != nil {
		return err
	}
	return render(c, status, pages.ServersCardList(data))
}

func serverFormError(c echo.Context, message string) error {
	c.Response().Header().Set("HX-Reswap", "none")
	setHTMXToast(c, message, "failed")
	return c.JSON(http.StatusBadRequest, map[string]string{"error": message})
}

type serverForm struct {
	name, url, username, password string
	enabled                       bool
}

func readServerForm(c echo.Context) (serverForm, error) {
	f := serverForm{
		name:     strings.TrimSpace(c.FormValue("name")),
		username: strings.TrimSpace(c.FormValue("username")),
		password: c.FormValue("password"),
		enabled:  c.FormValue("enabled") != "false",
	}
	normalized, err := normalizeServerURL(c.FormValue("url"))
	if err != nil {
		return f, err
	}
	f.url = normalized
	if f.name == "" {
		u, _ := url.Parse(normalized)
		f.name = u.Host
	}
	if len(f.name) > 80 || len(f.username) > 200 || len(f.password) > 1024 {
		return f, errors.New("a field is too long")
	}
	return f, nil
}

func (h *Handler) requireServersSameOrigin(c echo.Context) error {
	if !sameOriginRequest(c.Request(), c.Scheme(), h.desktopMode) {
		c.Response().Header().Set("HX-Reswap", "none")
		setHTMXToast(c, "Server request rejected: same-origin request required.", "failed")
		return echo.NewHTTPError(http.StatusForbidden, "same-origin request required")
	}
	return nil
}

// requireServersAdmin guards the shared saved-server list; connecting is per user.
func (h *Handler) requireServersAdmin(c echo.Context) error {
	if err := h.requireServersSameOrigin(c); err != nil {
		return err
	}
	if h.organizationSvc != nil && h.requireCurrentOrgAdmin(c) != nil {
		c.Response().Header().Set("HX-Reswap", "none")
		setHTMXToast(c, "Organization administrator required to manage servers.", "failed")
		return echo.NewHTTPError(http.StatusForbidden, "organization administrator required")
	}
	return nil
}

// SaveServer creates or updates a saved server, then signs in when a password is
// supplied. A failed sign-in keeps the current connection.
func (h *Handler) SaveServer(c echo.Context) error {
	if err := h.requireServersAdmin(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	form, err := readServerForm(c)
	if err != nil {
		return serverFormError(c, err.Error())
	}
	servers, err := h.loadSavedServers(ctx)
	if err != nil {
		return err
	}
	id := c.Param("id")
	index := -1
	if id != "" {
		for i := range servers {
			if servers[i].ID == id {
				index = i
			}
		}
		if index < 0 {
			return echo.NewHTTPError(http.StatusNotFound, "server not found")
		}
	}
	for i := range servers {
		if i != index && servers[i].URL == form.url && servers[i].Username == form.username {
			return serverFormError(c, "This server and username are already saved.")
		}
	}
	if index < 0 {
		if len(servers) >= 50 {
			return serverFormError(c, "Too many saved servers.")
		}
		servers = append(servers, pages.SavedServer{ID: newServerID()})
		index = len(servers) - 1
	}
	previous := servers[index]
	servers[index].Name, servers[index].URL, servers[index].Username, servers[index].Enabled = form.name, form.url, form.username, form.enabled
	if err := h.storeSavedServers(ctx, servers); err != nil {
		return err
	}
	// Sign-out follows the write so a concurrent sign-in either sees the new
	// settings or is removed here.
	if previous.URL != form.url || previous.Username != form.username || !form.enabled {
		h.remoteSessions.dropServer(servers[index].ID)
	}
	saved := servers[index]
	if form.enabled && form.password != "" {
		if err := h.connectServer(c, saved, form.password); err != nil {
			c.Response().Header().Set("HX-Retarget", "#servers-card-list")
			c.Response().Header().Set("HX-Reswap", "outerHTML")
			setHTMXToastWithOptionsAndTriggers(c, "Saved, but sign-in failed: "+err.Error(), "failed", "", "", "", "", "", map[string]any{"server-saved-id": saved.ID})
			return h.renderServersList(c, http.StatusOK)
		}
		setHTMXToast(c, "Connected to "+saved.Name, "completed")
	} else {
		setHTMXToast(c, "Saved "+saved.Name, "completed")
	}
	c.Response().Header().Set("HX-Trigger-After-Settle", `{"servers-saved":true}`)
	return h.renderServersList(c, http.StatusOK)
}

func (h *Handler) connectServer(c echo.Context, server pages.SavedServer, password string) error {
	ctx := c.Request().Context()
	session, err := h.remoteSessions.login(ctx, server, password)
	if errors.Is(err, errRemoteHostedSSO) {
		return errors.New("this server uses single sign-on; use Sign in with SSO")
	}
	if err != nil {
		return err
	}
	current := func() bool {
		saved, err := h.findSavedServer(ctx, server.ID)
		return err == nil && saved != nil && saved.Enabled && saved.URL == server.URL && saved.Username == server.Username
	}
	if !h.remoteSessions.connect(principalUserID(ctx), server.ID, session, current) {
		go h.remoteSessions.logout(session)
		return errors.New("the server was changed while signing in; try again")
	}
	return nil
}

func (h *Handler) ConnectServer(c echo.Context) error {
	if err := h.requireServersSameOrigin(c); err != nil {
		return err
	}
	server, err := h.findSavedServer(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if server == nil {
		return echo.NewHTTPError(http.StatusNotFound, "server not found")
	}
	if !server.Enabled {
		return serverFormError(c, "This server is disabled.")
	}
	if c.FormValue("password") == "" {
		return serverFormError(c, "Enter the password to sign in.")
	}
	if err := h.connectServer(c, *server, c.FormValue("password")); err != nil {
		return serverFormError(c, "Sign-in failed: "+err.Error())
	}
	setHTMXToast(c, "Connected to "+server.Name, "completed")
	c.Response().Header().Set("HX-Trigger-After-Settle", `{"servers-saved":true}`)
	return h.renderServersList(c, http.StatusOK)
}

func (h *Handler) DisconnectServer(c echo.Context) error {
	if err := h.requireServersSameOrigin(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	id := c.Param("id")
	server, err := h.findSavedServer(ctx, id)
	if err != nil {
		return err
	}
	if server == nil {
		return echo.NewHTTPError(http.StatusNotFound, "server not found")
	}
	if session := h.remoteSessions.get(principalUserID(ctx), id); session != nil {
		go h.remoteSessions.logout(session)
	}
	h.remoteSessions.put(principalUserID(ctx), id, nil)
	return h.renderServersList(c, http.StatusOK)
}

func (h *Handler) DeleteServer(c echo.Context) error {
	if err := h.requireServersAdmin(c); err != nil {
		return err
	}
	ctx := c.Request().Context()
	id := c.Param("id")
	servers, err := h.loadSavedServers(ctx)
	if err != nil {
		return err
	}
	kept := servers[:0]
	found := false
	for _, s := range servers {
		if s.ID == id {
			found = true
			continue
		}
		kept = append(kept, s)
	}
	if !found {
		return echo.NewHTTPError(http.StatusNotFound, "server not found")
	}
	if err := h.storeSavedServers(ctx, kept); err != nil {
		return err
	}
	h.remoteSessions.dropServer(id)
	return h.renderServersList(c, http.StatusOK)
}

// ServerSSOStart opens the remote server's own hosted sign-in flow. The remote
// session cookie is set on the remote origin, so the gateway cannot reuse it;
// the user finishes in a browser tab on that server.
func (h *Handler) ServerSSOStart(c echo.Context) error {
	if c.Request().Header.Get("Sec-Fetch-Site") != "same-origin" {
		if err := h.requireServersSameOrigin(c); err != nil {
			return err
		}
	}
	server, err := h.findSavedServer(c.Request().Context(), c.Param("id"))
	if err != nil {
		return err
	}
	if server == nil {
		return echo.NewHTTPError(http.StatusNotFound, "server not found")
	}
	return c.Redirect(http.StatusFound, server.URL+"/auth/sso/start")
}

// UpdateLocalBackend persists the desktop local-backend mode.
func (h *Handler) UpdateLocalBackend(c echo.Context) error {
	if err := h.requireServersAdmin(c); err != nil {
		return err
	}
	if !h.desktopMode || h.localBackend == nil {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	enabled := c.FormValue("enabled") == "true"
	if err := h.localBackend.SetEnabled(enabled); err != nil {
		return serverFormError(c, err.Error())
	}
	if enabled {
		setHTMXToast(c, "Local services enabled.", "completed")
	} else {
		setHTMXToast(c, "Local services are stopping. Active Local work can finish; remote servers stay connected.", "completed")
	}
	return h.renderServersList(c, http.StatusOK)
}
