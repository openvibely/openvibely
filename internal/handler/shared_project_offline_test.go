package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/web/templates/pages"
	"github.com/stretchr/testify/require"
)

// switchableRemote fronts a fake remote OV with a proxy that can be taken
// offline, which also cuts its open sockets.
type switchableRemote struct {
	*httptest.Server
	offline atomic.Bool
	hang    atomic.Bool
	mu      sync.Mutex
	conns   map[*http.Request]context.CancelFunc
}

func newSwitchableRemote(t *testing.T, target string) *switchableRemote {
	t.Helper()
	u, err := url.Parse(target)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(u)
	s := &switchableRemote{conns: map[*http.Request]context.CancelFunc{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hang.Load() {
			<-r.Context().Done()
			return
		}
		if s.offline.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		ctx, cancel := context.WithCancel(r.Context())
		s.mu.Lock()
		s.conns[r] = cancel
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.conns, r)
			s.mu.Unlock()
			cancel()
		}()
		proxy.ServeHTTP(w, r.WithContext(ctx))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *switchableRemote) setOffline(off bool) {
	s.offline.Store(off)
	if !off {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.conns {
		cancel()
	}
	s.Server.CloseClientConnections()
}

func TestSharedProjectTabSurvivesOfflineMachine(t *testing.T) {
	h, e := setupHubHandler(t)
	backend, _, owner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	const repo = "https://github.com/acme/foo.git"

	localProject := createProject(t, h, "Foo")
	setRepoURL(t, h, localProject.ID, repo)
	createTask(t, h, localProject.ID, "Local shared task")
	remoteProject := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteProject.ID, repo)
	createTask(t, owner, remoteProject.ID, "Remote shared task")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	ctx := context.Background()
	require.Eventually(t, func() bool { return len(h.sharedProjects(ctx, h.remoteServerGroups(ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	sharedID := h.sharedProjects(ctx, h.remoteServerGroups(ctx))[0].ID

	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, target, nil)))
		return rec
	}
	pin := func() string {
		req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"pinned_project_ids":["`+sharedID+`"]}`))
		rec := httptest.NewRecorder()
		ec := echo.New()
		c := ec.NewContext(signedIn(req), rec)
		require.NoError(t, h.SaveUIPreferences(c))
		got, err := h.settingsRepo.Get(ctx, uiPreferencePinnedProjectIDsKey)
		require.NoError(t, err)
		return got
	}
	require.Equal(t, `["`+sharedID+`"]`, pin())

	box.setOffline(true)
	require.Eventually(t, func() bool { return len(h.remoteServerGroups(ctx)) == 0 }, 10*time.Second, 50*time.Millisecond, "Box should count as offline")

	redirect := get("/tasks?project_id=" + sharedID)
	require.Equal(t, http.StatusFound, redirect.Code, "with one copy left the tab shows that copy")
	require.Equal(t, "/tasks?project_id="+localProject.ID, redirect.Header().Get("Location"))
	require.Equal(t, `["`+sharedID+`"]`, pin(), "the pin stays while one copy is reachable")

	page := get("/tasks?project_id=" + localProject.ID)
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	require.Contains(t, body, "Local shared task")
	require.NotContains(t, body, "Remote shared task")
	require.Contains(t, body, `data-pinned-projects="[&#34;`+sharedID+`&#34;]"`, "the open tab is still listed")
	require.Contains(t, body, `data-project-tab="`+sharedID+`"`)
	require.Contains(t, body, `aria-selected="true" tabindex="0" data-project-tab="`+sharedID+`"`, "the local copy's page selects the repository tab")
	require.NotContains(t, body, "data-shared-project-group", "+ list offers no combined entry with one copy")
	require.Equal(t, 1, strings.Count(body, `data-searchable-selector-id="`+localProject.ID+`"`), "+ lists Foo once")

	box.setOffline(false)
	require.Eventually(t, func() bool {
		return strings.Contains(get("/tasks?project_id="+sharedID).Body.String(), "Remote shared task")
	}, 20*time.Second, 100*time.Millisecond, "Box's tasks come back in the same tab")

	require.Equal(t, http.StatusNotFound, get("/tasks?project_id="+models.SharedProjectID("github.com/nobody/none")).Code)
}

func TestSharedProjectGoneWhenNoCopyIsReachable(t *testing.T) {
	h, e := setupHubHandler(t)
	p := createProject(t, h, "Foo")
	setRepoURL(t, h, p.ID, "https://github.com/acme/foo.git")
	groups := h.repoGroups(context.Background(), nil)
	require.Len(t, groups, 1)
	require.False(t, groups[0].Combined())
	id := groups[0].ID

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/tasks?project_id="+id, nil)))
	require.Equal(t, http.StatusFound, rec.Code, "a one-copy repository link opens that copy")
	require.Equal(t, "/tasks?project_id="+p.ID, rec.Header().Get("Location"))

	setRepoURL(t, h, p.ID, "")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/tasks?project_id="+id, nil)))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestLivePagesHearWhenRemoteServerChanges(t *testing.T) {
	h, e := setupHubHandler(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.ServeHTTP(w, r.WithContext(models.WithPrincipal(r.Context(), models.Principal{UserID: models.BootstrapUserID})))
	}))
	t.Cleanup(server.Close)
	client := dialLive(t, server.URL)
	h.remoteSessions.serverChanged(models.BootstrapUserID, "box")
	client.next(t, "", "servers_changed")
}

func TestBoxOnlyTabWaitsForItsServer(t *testing.T) {
	h, e := setupHubHandler(t)
	backend, _, owner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	remoteProject := createProject(t, owner, "Foo")
	createTask(t, owner, remoteProject.ID, "Remote task")
	local := createProject(t, h, "Bar")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	foo := remoteid.ID(savedServerID(t, h), remoteProject.ID)

	ctx := context.Background()
	get := func(target string) *httptest.ResponseRecorder {
		req := signedIn(httptest.NewRequest(http.MethodGet, target, nil))
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	catalog := func() string { return get("/ui/project-catalog?current=" + foo).Body.String() }
	require.Eventually(t, func() bool { return strings.Contains(catalog(), `value="`+foo+`"`) }, 5*time.Second, 50*time.Millisecond)
	body := catalog()
	require.Contains(t, body, `value="`+local.ID+`"`)
	require.Contains(t, body, `data-searchable-selector-id="`+foo+`"`, "+ list offers Box's Foo")
	require.Contains(t, body, `id="project-catalog-shared"`)

	pin := func(ids string) string {
		req := httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(`{"pinned_project_ids":`+ids+`}`))
		c := echo.New().NewContext(signedIn(req), httptest.NewRecorder())
		require.NoError(t, h.SaveUIPreferences(c))
		got, err := h.settingsRepo.Get(ctx, uiPreferencePinnedProjectIDsKey)
		require.NoError(t, err)
		return got
	}
	both := `["` + foo + `","` + local.ID + `"]`
	require.Equal(t, both, pin(both))

	box.setOffline(true)
	require.Eventually(t, func() bool { return !strings.Contains(catalog(), `value="`+foo+`"`) }, 10*time.Second, 50*time.Millisecond, "+ list drops Box's Foo while Box is down")
	require.Equal(t, both, pin(both), "Box's tab is kept while Box is down")

	page := get("/tasks?project_id=" + foo)
	require.Equal(t, http.StatusOK, page.Code, "Box's tab waits instead of redirecting")
	require.Contains(t, page.Body.String(), "data-server-waiting")
	require.Contains(t, page.Body.String(), `data-pinned-projects="[&#34;`+foo+`&#34;,&#34;`+local.ID+`&#34;]"`)

	box.setOffline(false)
	require.Eventually(t, func() bool {
		return strings.Contains(get("/tasks?project_id="+foo).Body.String(), "Remote task")
	}, 20*time.Second, 100*time.Millisecond, "Box's tab loads once Box is back")
	require.Contains(t, catalog(), `value="`+foo+`"`)
}

func TestFirstPagesDoNotWaitForSavedServers(t *testing.T) {
	h, e := setupHubHandler(t)
	backend, _, owner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	const repo = "https://github.com/acme/foo.git"
	local := createProject(t, h, "Foo")
	setRepoURL(t, h, local.ID, repo)
	createTask(t, h, local.ID, "Local task")
	remoteProject := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteProject.ID, repo)
	remoteOnly := createProject(t, owner, "Bar")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	server := savedServerID(t, h)
	ctx := context.Background()
	require.Eventually(t, func() bool { return len(h.sharedProjects(ctx, h.remoteServerGroups(ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	sharedID := h.sharedProjects(ctx, h.remoteServerGroups(ctx))[0].ID

	// Simulate a restart while Box accepts connections but never answers.
	h.remoteHub().Close(models.BootstrapUserID, server)
	h.remoteSessions.Lock()
	delete(h.remoteSessions.entries, remoteSessionKey{userID: models.BootstrapUserID, serverID: server})
	h.remoteSessions.Unlock()
	box.hang.Store(true)
	restoreCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	h.StartRemoteRestore(restoreCtx)
	require.True(t, h.restoring())

	get := func(target string) *httptest.ResponseRecorder {
		t.Helper()
		req := signedIn(httptest.NewRequest(http.MethodGet, target, nil))
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		start := time.Now()
		e.ServeHTTP(rec, req)
		require.Less(t, time.Since(start), time.Second, target)
		return rec
	}
	page := get("/tasks?project_id=" + local.ID)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "Local task")

	page = get("/tasks?project_id=" + remoteid.ID(server, remoteOnly.ID))
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "data-server-waiting")

	page = get("/tasks?project_id=" + sharedID)
	require.Equal(t, http.StatusFound, page.Code, "the repository tab opens the local copy")
	require.Equal(t, "/tasks?project_id="+local.ID, page.Header().Get("Location"))
	require.True(t, h.restoring(), "Box is still being restored")
}

func TestServersPageShowsOfflineWhenConnectionIsDown(t *testing.T) {
	h, e := setupHubHandler(t)
	backend, _, _ := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	status := func() string {
		servers, err := h.serverViews(ctx)
		require.NoError(t, err)
		require.Len(t, servers, 1)
		return servers[0].Status
	}
	require.Eventually(t, func() bool { return status() == pages.ServerStatusConnected }, 5*time.Second, 50*time.Millisecond)
	box.setOffline(true)
	require.Eventually(t, func() bool { return status() == pages.ServerStatusOffline }, 10*time.Second, 50*time.Millisecond)
	box.setOffline(false)
	require.Eventually(t, func() bool { return status() == pages.ServerStatusConnected }, 20*time.Second, 100*time.Millisecond)
}

func TestSharedLiveEventsFlowWhileCopiesReload(t *testing.T) {
	h, e := setupHubHandler(t)
	h.broadcaster = events.NewBroadcaster()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.ServeHTTP(w, signedIn(r))
	}))
	t.Cleanup(server.Close)
	a := createProject(t, h, "Foo")
	b := createProject(t, h, "Foo")
	setRepoURL(t, h, a.ID, "https://github.com/acme/foo.git")
	setRepoURL(t, h, b.ID, "https://github.com/acme/foo.git")
	sharedID := models.SharedProjectID("github.com/acme/foo")

	release := make(chan struct{})
	reloads := make(chan struct{}, 4)
	load := liveMembers
	liveMembers = func(ctx context.Context, h *Handler, projectID string) map[string]bool {
		select {
		case reloads <- struct{}{}:
		default:
		}
		if len(reloads) > 1 {
			<-release
		}
		return load(ctx, h, projectID)
	}

	client := dialLive(t, server.URL)
	client.send(t, liveCommand{Op: "live", ID: "live", ProjectID: sharedID})
	client.next(t, "live", "subscribed")

	h.remoteSessions.serverChanged(models.BootstrapUserID, "box")
	require.Eventually(t, func() bool { return len(reloads) == 2 }, 5*time.Second, 10*time.Millisecond, "reload started")
	for i := 0; i < 30; i++ {
		h.broadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, ProjectID: a.ID, TaskID: fmt.Sprint(i)})
		f := client.next(t, "live", string(events.TaskStatusChanged))
		require.Contains(t, string(mustJSON(t, f.Data)), `"task_id":"`+fmt.Sprint(i)+`"`, "event delivered during the reload")
	}
	close(release)
	client.conn.CloseNow()
	server.Close()
	require.Eventually(t, func() bool { return h.liveSubscriptionCount.Load() == 0 }, 5*time.Second, 10*time.Millisecond)
	liveMembers = load
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

func TestSavedSharedProjectWaitsForItsOnlyServer(t *testing.T) {
	h, e := setupHubHandler(t)
	backend, _, owner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	remoteProject := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteProject.ID, "https://github.com/acme/foo.git")
	createTask(t, owner, remoteProject.ID, "Remote task")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {box.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	ctx := context.Background()
	require.Eventually(t, func() bool { return len(h.repoGroups(ctx, h.remoteServerGroups(ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	sharedID := models.SharedProjectID("github.com/acme/foo")
	require.NoError(t, h.settingsRepo.Set(ctx, uiPreferenceSelectedProjectIDKey, sharedID))

	get := func() *httptest.ResponseRecorder {
		req := signedIn(httptest.NewRequest(http.MethodGet, "/tasks", nil))
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	box.setOffline(true)
	require.Eventually(t, func() bool { return len(h.remoteServerGroups(ctx)) == 0 }, 10*time.Second, 50*time.Millisecond)
	page := get()
	require.Equal(t, http.StatusOK, page.Code, "the saved repository waits instead of 404")
	require.Contains(t, page.Body.String(), "data-server-waiting")
	require.Contains(t, page.Body.String(), "Connecting to Box")

	box.setOffline(false)
	require.Eventually(t, func() bool {
		rec := get()
		return rec.Code == http.StatusFound && rec.Header().Get("Location") == "/tasks?project_id="+remoteid.ID(savedServerID(t, h), remoteProject.ID)
	}, 20*time.Second, 100*time.Millisecond, "the repository opens Box's copy once Box is back")

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/servers/"+savedServerID(t, h), nil))
	missing := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+models.SharedProjectID("github.com/nobody/none"), nil)
	missing.Header.Set("Accept", "text/html")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, signedIn(missing))
	require.Equal(t, http.StatusNotFound, rec.Code, "with every server connected an unknown repository is not found")
}
