package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

const remoteTaskID = "0123456789abcdef0123456789abcdef"

// fakeRemoteOV is a remote OpenVibely server: a real handler serving the live
// socket, behind a stand-in login that issues the session cookie.
func fakeRemoteOV(t *testing.T, password string) (*httptest.Server, *atomic.Bool, *Handler) {
	t.Helper()
	revoked := new(atomic.Bool)
	signedIn := func(r *http.Request) bool {
		c, err := r.Cookie("ov_session")
		return err == nil && c.Value == "good" && !revoked.Load()
	}
	h, e, _, env := setupTestHandlerWithStores(t)
	access := &authz.Authorizer{Store: env.Stores.ProjectAccess}
	h.SetRemote(nil, &remote.Executor{Stores: env.Stores, Projects: authz.NewProjectStore(env.Stores.Projects, access), Authorizer: access})
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = r.ParseForm()
		if r.FormValue("username") != "alice" || r.FormValue("password") != password {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "ov_session", Value: "good", Path: "/"})
		http.Redirect(w, r, "/", http.StatusFound)
	})
	mux.HandleFunc("/auth/me", func(w http.ResponseWriter, r *http.Request) {
		if !signedIn(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"authenticated":true,"username":"alice","display":"Alice"}`))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !signedIn(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		e.ServeHTTP(w, r.WithContext(models.WithPrincipal(r.Context(), models.Principal{UserID: models.BootstrapUserID})))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, revoked, h
}

// setupHubHandler builds a handler whose stores route remote IDs over its hub.
func setupHubHandler(t *testing.T) (*Handler, *echo.Echo) {
	t.Helper()
	hub := &remote.Hub{}
	env := wireTestHandler(t, remote.Wrap(teststores.New(t).Stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	h := env.Handler
	h.SetRemote(hub, nil)
	t.Cleanup(hub.Shutdown)
	return h, env.Echo
}

func serversRequest(method, target string, form url.Values) *http.Request {
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationForm)
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("HX-Request", "true")
	return req
}

func savedServerID(t *testing.T, h *Handler) string {
	t.Helper()
	servers, err := h.loadSavedServers(context.Background())
	require.NoError(t, err)
	require.Len(t, servers, 1)
	return servers[0].ID
}

func TestNormalizeServerURL(t *testing.T) {
	for raw, want := range map[string]string{
		"ov.example.com":              "https://ov.example.com",
		"HTTP://OV.Example.com:8080/": "http://ov.example.com:8080",
		"https://host/prefix/":        "https://host/prefix",
	} {
		got, err := normalizeServerURL(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{"", "ftp://host", "https://u:p@host", "https://host/?a=1", "https://host/#x"} {
		_, err := normalizeServerURL(raw)
		require.Error(t, err, raw)
	}
}

func TestSaveServerConnectsAndRunsRemoteActions(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, revoked, owner := fakeRemoteOV(t, "secret")
	project := createProject(t, owner, "Remote One")
	task := createTask(t, owner, project.ID, "Remote task", func(tk *models.Task) { tk.Category = models.CategoryBacklog })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Team"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Connected")
	require.Contains(t, rec.Body.String(), "Signed in as Alice")
	id := savedServerID(t, h)

	raw, err := h.settingsRepo.Get(context.Background(), uiPreferenceSavedServersKey)
	require.NoError(t, err)
	require.Contains(t, raw, `"password":"secret"`)
	saved, err := h.findSavedServer(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "secret", saved.Password)
	require.Contains(t, rec.Body.String(), `data-server-password="secret"`)

	require.Eventually(t, func() bool {
		groups := h.remoteServerGroups(context.Background())
		return len(groups) == 1 && len(groups[0].Projects) > 0
	}, 5*time.Second, 50*time.Millisecond)
	groups := h.remoteServerGroups(context.Background())
	var names []string
	for _, p := range groups[0].Projects {
		names = append(names, p.Name)
		if p.Name == "Remote One" {
			require.Equal(t, remoteid.ID(id, project.ID), p.ID)
		}
	}
	require.Contains(t, names, "Remote One")

	remoteTask := remoteid.ID(id, task.ID)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPatch, "/tasks/"+remoteTask+"/category", url.Values{"category": {string(models.CategoryCompleted)}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	moved, err := owner.taskRepo.GetByID(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryCompleted, moved.Category)

	revoked.Store(true)
	h.remoteHub().Close(models.BootstrapUserID, id)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPatch, "/tasks/"+remoteTask+"/category", url.Values{"category": {string(models.CategoryBacklog)}}))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "/servers?reconnect="+id, rec.Header().Get("HX-Redirect"))
}

func TestCapacityAPIRejectsRemoteProject(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := createProject(t, owner, "Remote Busy")
	for i := 0; i < 3; i++ {
		createTask(t, owner, project.ID, fmt.Sprintf("Pending %d", i), func(tk *models.Task) { tk.Status = models.StatusQueued })
	}

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := savedServerID(t, h)
	require.Eventually(t, func() bool {
		return h.remoteSignedIn(context.Background(), id) && !h.remoteDown(context.Background(), id)
	}, 5*time.Second, 50*time.Millisecond)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/api/capacity/projects/"+remoteid.ID(id, project.ID), nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Read remote capacity")
}

func TestFailedReconnectKeepsExistingSession(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	remote, _, _ := fakeRemoteOV(t, "secret")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"url": {remote.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	id := savedServerID(t, h)
	before := h.remoteSessions.get(principalUserID(context.Background()), id)
	require.NotNil(t, before)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id+"/connect", url.Values{"password": {"wrong"}}))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Header().Get("HX-Trigger"), "incorrect username or password")
	require.Same(t, before, h.remoteSessions.get(principalUserID(context.Background()), id))
}

func TestServersRejectCrossOriginMutations(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	req := serversRequest(http.MethodPost, "/servers", url.Values{"url": {"https://example.org"}})
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServersAcceptNullOriginOnlyInDesktopMode(t *testing.T) {
	for _, desktop := range []bool{false, true} {
		h, e, _ := setupTestHandler(t)
		h.desktopMode = desktop
		req := serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {"https://example.org"}})
		req.Header.Set("Origin", "null")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if desktop {
			require.NotEqual(t, http.StatusForbidden, rec.Code, rec.Body.String())
		} else {
			require.Equal(t, http.StatusForbidden, rec.Code)
		}
	}
}

func TestRemoteWriteWithoutSessionAsksToReconnect(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/tasks/"+remoteid.ID("0123456789abcdef", remoteTaskID)+"/run", url.Values{}))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Equal(t, "/servers?reconnect=0123456789abcdef", rec.Header().Get("HX-Redirect"))
}

func TestRemoteRoutingReadsOnlyPathAndProjectID(t *testing.T) {
	_, e, _ := setupTestHandler(t)
	remoteProject := remoteid.ID("0123456789abcdef", remoteTaskID)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/tasks?project_id="+url.QueryEscape(remoteProject), nil))
	require.Equal(t, "/servers?reconnect=0123456789abcdef", rec.Header().Get("HX-Redirect"))

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/tasks?search="+url.QueryEscape(remoteProject), nil))
	require.Empty(t, rec.Header().Get("HX-Redirect"))
}

// signInTwoUsers saves a server as the admin and signs a second user in to it.
func signInTwoUsers(t *testing.T, h *Handler, e *echo.Echo, serverURL string) (string, pages.SavedServer) {
	t.Helper()
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Work"}, "url": {serverURL}, "username": {"alice"}, "password": {"secret"}, "enabled": {"true"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := savedServerID(t, h)
	server, err := h.findSavedServer(context.Background(), id)
	require.NoError(t, err)
	session, err := h.remoteSessions.login(context.Background(), *server, "secret")
	require.NoError(t, err)
	h.remoteSessions.put("bob", id, session)
	t.Cleanup(func() { h.remoteHub().Close("bob", id) })
	for _, user := range []string{models.BootstrapUserID, "bob"} {
		require.NotNil(t, h.remoteSessions.get(user, id), user)
		require.NotNil(t, h.remoteHub().Get(user, id), user)
	}
	return id, *server
}

func requireNobodySignedIn(t *testing.T, h *Handler, id string) {
	t.Helper()
	for _, user := range []string{models.BootstrapUserID, "bob"} {
		require.Nil(t, h.remoteSessions.get(user, id), user)
		require.Nil(t, h.remoteHub().Get(user, id), user)
	}
}

func TestDeleteServerSignsEveryoneOut(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "secret")
	id, _ := signInTwoUsers(t, h, e, srv.URL)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/servers/"+id, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	requireNobodySignedIn(t, h, id)
}

func TestDisableServerSignsEveryoneOutAndBlocksSignIn(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "secret")
	id, _ := signInTwoUsers(t, h, e, srv.URL)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id, url.Values{"name": {"Work"}, "url": {srv.URL}, "username": {"alice"}, "enabled": {"false"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	requireNobodySignedIn(t, h, id)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id+"/connect", url.Values{"password": {"secret"}}))
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Header().Get("HX-Trigger"), "This server is disabled.")
	requireNobodySignedIn(t, h, id)
}

func TestChangeServerURLSignsEveryoneOut(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "secret")
	other, _, _ := fakeRemoteOV(t, "secret")
	id, _ := signInTwoUsers(t, h, e, srv.URL)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id, url.Values{"name": {"Work"}, "url": {other.URL}, "username": {"alice"}, "enabled": {"true"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	requireNobodySignedIn(t, h, id)
}

func TestStaleExpiryKeepsNewSession(t *testing.T) {
	var r remoteSessions
	old, fresh := &remoteSession{}, &remoteSession{}
	r.put("bob", "0123456789abcdef", old)
	r.put("bob", "0123456789abcdef", fresh)
	r.markExpired("bob", "0123456789abcdef", old)
	require.False(t, fresh.expired)
	r.markExpired("bob", "0123456789abcdef", fresh)
	require.True(t, fresh.expired)
}

func TestServerChangedDuringSignInDropsSession(t *testing.T) {
	for name, change := range map[string]func(id, target string) *http.Request{
		"delete": func(id, _ string) *http.Request {
			return serversRequest(http.MethodDelete, "/servers/"+id, nil)
		},
		"disable": func(id, target string) *http.Request {
			return serversRequest(http.MethodPost, "/servers/"+id, url.Values{"name": {"Work"}, "url": {target}, "username": {"alice"}, "enabled": {"false"}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, e := setupHubHandler(t)
			srv, _, _ := fakeRemoteOV(t, "secret")
			upstream, _ := url.Parse(srv.URL)
			proxy := httputil.NewSingleHostReverseProxy(upstream)
			var id string
			var changed *httptest.ResponseRecorder
			var gate *httptest.Server
			gate = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/login" && r.Method == http.MethodPost {
					changed = httptest.NewRecorder()
					e.ServeHTTP(changed, change(id, gate.URL))
				}
				proxy.ServeHTTP(w, r)
			}))
			t.Cleanup(gate.Close)

			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Work"}, "url": {gate.URL}, "username": {"alice"}}))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			id = savedServerID(t, h)

			rec = httptest.NewRecorder()
			e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id+"/connect", url.Values{"password": {"secret"}}))
			require.NotNil(t, changed)
			require.Equal(t, http.StatusOK, changed.Code, changed.Body.String())
			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Header().Get("HX-Trigger"), "changed while signing in")
			requireNobodySignedIn(t, h, id)
		})
	}
}

func TestServerSSOStartRequiresSameOrigin(t *testing.T) {
	h, e := setupHubHandler(t)
	backend, _, _ := fakeRemoteOV(t, "secret")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {backend.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	target := "/servers/" + savedServerID(t, h) + "/sso"
	for name, tc := range map[string]struct {
		headers map[string]string
		code    int
	}{
		"origin":         {map[string]string{"Origin": "http://example.com"}, http.StatusFound},
		"referer":        {map[string]string{"Referer": "http://example.com/servers"}, http.StatusFound},
		"fetch metadata": {map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusFound},
		"cross site":     {map[string]string{"Origin": "https://evil.test", "Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		"no headers":     {nil, http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, tc.code, rec.Code, name)
		if tc.code == http.StatusFound {
			require.Equal(t, backend.URL+"/auth/sso/start", rec.Header().Get("Location"), name)
		}
	}
}

func TestServersLocalServicesStoppingRefresh(t *testing.T) {
	var out strings.Builder
	require.NoError(t, pages.ServersCardList(pages.ServersPageData{DesktopMode: true, LocalBackend: "stopping"}).Render(t.Context(), &out))
	require.Contains(t, out.String(), "Run Local services")
	require.Contains(t, out.String(), "Stopping Local services")
	require.Contains(t, out.String(), `hx-trigger="every 2s"`)
	require.Contains(t, out.String(), `hx-select="#servers-card-list"`)
	out.Reset()
	require.NoError(t, pages.ServersCardList(pages.ServersPageData{DesktopMode: true, LocalBackend: "off"}).Render(t.Context(), &out))
	require.NotContains(t, out.String(), `hx-trigger="every 2s"`)
	require.NotContains(t, out.String(), "Stopping Local services")
}

func TestServerPasswordReplacementAndDeletion(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, _ := fakeRemoteOV(t, "updated-password")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"url": {srv.URL}, "username": {"alice"}, "password": {"old-password"}, "enabled": {"false"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	id := savedServerID(t, h)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers/"+id, url.Values{"url": {srv.URL}, "username": {"alice"}, "password": {"updated-password"}, "enabled": {"true"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Signed in as Alice")
	saved, err := h.findSavedServer(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, "updated-password", saved.Password)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/servers/"+id, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	raw, err := h.settingsRepo.Get(context.Background(), uiPreferenceSavedServersKey)
	require.NoError(t, err)
	require.NotContains(t, raw, "updated-password")
	require.NotContains(t, raw, "old-password")
}
