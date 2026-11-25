package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
)

type tabEnv struct {
	h   *Handler
	e   *echo.Echo
	ctx context.Context
}

func (te tabEnv) addServer(t *testing.T, name, target string) string {
	t.Helper()
	before, err := te.h.loadSavedServers(te.ctx)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	te.e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {name}, "url": {target}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after, err := te.h.loadSavedServers(te.ctx)
	require.NoError(t, err)
	require.Len(t, after, len(before)+1)
	for _, s := range after {
		if s.Name == name {
			return s.ID
		}
	}
	t.Fatalf("server %s not saved", name)
	return ""
}

func (te tabEnv) pin(t *testing.T, ids ...string) {
	t.Helper()
	body := `{"pinned_project_ids":["` + strings.Join(ids, `","`) + `"]}`
	c := echo.New().NewContext(signedIn(httptest.NewRequest(http.MethodPost, "/ui/preferences", strings.NewReader(body))), httptest.NewRecorder())
	require.NoError(t, te.h.SaveUIPreferences(c))
}

func (te tabEnv) delete(t *testing.T, projectID string) {
	t.Helper()
	rec := httptest.NewRecorder()
	te.e.ServeHTTP(rec, serversRequest(http.MethodDelete, "/projects/"+projectID, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func (te tabEnv) page(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := signedIn(httptest.NewRequest(http.MethodGet, target, nil))
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	te.e.ServeHTTP(rec, req)
	return rec
}

func newTabEnv(t *testing.T) tabEnv {
	h, e := setupHubHandler(t)
	createProject(t, h, "Default")
	return tabEnv{h: h, e: e, ctx: models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})}
}

const fooRepo = "https://github.com/acme/foo.git"

func TestDeletingAProjectClosesItsTab(t *testing.T) {
	te := newTabEnv(t)
	keep := createProject(t, te.h, "Keep")
	gone := createProject(t, te.h, "Gone")
	te.pin(t, keep.ID, gone.ID)
	te.delete(t, gone.ID)
	require.Equal(t, []string{keep.ID}, te.h.pinnedProjectIDs(te.ctx))
}

func TestDeletingACopyKeepsTheCombinedTabWhileAnotherIsReachable(t *testing.T) {
	te := newTabEnv(t)
	pi, _, piOwner := fakeRemoteOV(t, "secret")
	local := createProject(t, te.h, "Foo")
	setRepoURL(t, te.h, local.ID, fooRepo)
	piFoo := createProject(t, piOwner, "Foo")
	setRepoURL(t, piOwner, piFoo.ID, fooRepo)
	te.addServer(t, "Pi", pi.URL)
	require.Eventually(t, func() bool { return len(te.h.sharedProjects(te.ctx, te.h.remoteServerGroups(te.ctx))) == 1 }, 5*time.Second, 50*time.Millisecond)
	tab := te.h.sharedTabOf(te.ctx, local.ID)
	te.pin(t, tab)

	te.delete(t, local.ID)
	require.Equal(t, []string{tab}, te.h.pinnedProjectIDs(te.ctx), "Pi still has Foo")
}

func TestDeletingTheLastReachableCopyClosesTheCombinedTab(t *testing.T) {
	te := newTabEnv(t)
	backend, _, boxOwner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	local := createProject(t, te.h, "Foo")
	setRepoURL(t, te.h, local.ID, fooRepo)
	boxFoo := createProject(t, boxOwner, "Foo")
	setRepoURL(t, boxOwner, boxFoo.ID, fooRepo)
	te.addServer(t, "Box", box.URL)
	require.Eventually(t, func() bool {
		return te.h.sharedTabOf(te.ctx, local.ID) != "" && te.h.sharedProject(te.ctx, te.h.sharedTabOf(te.ctx, local.ID)).Combined()
	}, 5*time.Second, 50*time.Millisecond)
	tab := te.h.sharedTabOf(te.ctx, local.ID)
	te.pin(t, tab)

	box.setOffline(true)
	require.Eventually(t, func() bool { return len(te.h.remoteServerGroups(te.ctx)) == 0 }, 10*time.Second, 50*time.Millisecond)
	te.delete(t, local.ID)
	require.Empty(t, te.h.pinnedProjectIDs(te.ctx), "Box's copy is offline, so no copy is reachable")

	box.setOffline(false)
	require.Eventually(t, func() bool { return len(te.h.remoteServerGroups(te.ctx)) == 1 }, 20*time.Second, 100*time.Millisecond)
	require.Empty(t, te.h.pinnedProjectIDs(te.ctx), "Box's Foo returns to the + list, not the tabs")
}

func TestDeletingARemoteCopyClosesItsTab(t *testing.T) {
	te := newTabEnv(t)
	pi, _, piOwner := fakeRemoteOV(t, "secret")
	createProject(t, piOwner, "Default")
	bar := createProject(t, piOwner, "Bar")
	server := te.addServer(t, "Pi", pi.URL)
	barID := remoteid.ID(server, bar.ID)
	require.Eventually(t, func() bool { return !te.h.remoteDown(te.ctx, server) && len(te.h.remoteServerGroups(te.ctx)) == 1 }, 5*time.Second, 50*time.Millisecond)
	te.pin(t, barID)
	require.Equal(t, []string{barID}, te.h.pinnedProjectIDs(te.ctx))
	te.delete(t, barID)
	require.Empty(t, te.h.pinnedProjectIDs(te.ctx))
}

func TestCombinedLinkWaitsOnlyWhenPinned(t *testing.T) {
	te := newTabEnv(t)
	backend, _, boxOwner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	pi, _, piOwner := fakeRemoteOV(t, "secret")
	for _, owner := range []*Handler{boxOwner, piOwner} {
		p := createProject(t, owner, "Foo")
		setRepoURL(t, owner, p.ID, fooRepo)
	}
	te.addServer(t, "Box", box.URL)
	te.addServer(t, "Pi", pi.URL)
	var tab string
	require.Eventually(t, func() bool {
		shared := te.h.sharedProjects(te.ctx, te.h.remoteServerGroups(te.ctx))
		if len(shared) == 1 {
			tab = shared[0].ID
		}
		return tab != ""
	}, 5*time.Second, 50*time.Millisecond)
	te.pin(t, tab)

	box.setOffline(true)
	require.Eventually(t, func() bool { return len(te.h.remoteServerGroups(te.ctx)) == 1 }, 10*time.Second, 50*time.Millisecond)
	require.Equal(t, http.StatusFound, te.page(t, "/tasks?project_id="+tab).Code, "Pi's copy opens directly")

	unknown := models.SharedProjectID("github.com/nobody/none")
	require.Equal(t, http.StatusNotFound, te.page(t, "/tasks?project_id="+unknown).Code, "an unpinned link is not found while Box is off")

	te.pin(t, tab, unknown)
	require.Equal(t, []string{tab}, te.h.pinnedProjectIDs(te.ctx), "an unknown link cannot be newly pinned while Box is off")
}

func TestPinnedCombinedTabWaitsWhileItsCopiesAreOffline(t *testing.T) {
	te := newTabEnv(t)
	backend, _, boxOwner := fakeRemoteOV(t, "secret")
	box := newSwitchableRemote(t, backend.URL)
	backend2, _, piOwner := fakeRemoteOV(t, "secret")
	pi := newSwitchableRemote(t, backend2.URL)
	for _, owner := range []*Handler{boxOwner, piOwner} {
		p := createProject(t, owner, "Foo")
		setRepoURL(t, owner, p.ID, fooRepo)
	}
	te.addServer(t, "Box", box.URL)
	te.addServer(t, "Pi", pi.URL)
	var tab string
	require.Eventually(t, func() bool {
		shared := te.h.sharedProjects(te.ctx, te.h.remoteServerGroups(te.ctx))
		if len(shared) == 1 {
			tab = shared[0].ID
		}
		return tab != ""
	}, 5*time.Second, 50*time.Millisecond)
	te.pin(t, tab)

	box.setOffline(true)
	pi.setOffline(true)
	require.Eventually(t, func() bool { return len(te.h.remoteServerGroups(te.ctx)) == 0 }, 10*time.Second, 50*time.Millisecond)
	page := te.page(t, "/tasks?project_id="+tab)
	require.Equal(t, http.StatusOK, page.Code)
	require.Contains(t, page.Body.String(), "data-server-waiting")
	te.pin(t, tab)
	require.Equal(t, []string{tab}, te.h.pinnedProjectIDs(te.ctx), "the pin survives while its servers are off")
}
