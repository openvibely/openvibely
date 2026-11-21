package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/stretchr/testify/require"
)

type countedProjectLookup struct {
	persistence.ProjectStore
	calls atomic.Int32
}

func (s *countedProjectLookup) ListAPIProjects(ctx context.Context) ([]models.ProjectAPIItem, error) {
	s.calls.Add(1)
	return s.ProjectStore.ListAPIProjects(ctx)
}
func TestProjectLookupRequestReuse(t *testing.T) {
	h, _, _ := setupTestHandler(t)
	project := createProject(t, h, "Project 1")
	setRepoURL(t, h, project.ID, "https://example.com/team/project.git")
	counter := &countedProjectLookup{ProjectStore: h.projectRepo}
	h.projectSvc = service.NewProjectService(counter)
	ctx := withProjectLookups(context.Background())
	id := models.SharedProjectID("example.com/team/project")
	require.NotNil(t, h.sharedProject(ctx, id))
	require.NotNil(t, h.virtualProject(ctx, id))
	require.NotEmpty(t, h.repoGroups(layout.WithDesktopMode(ctx, true), nil))
	require.EqualValues(t, 1, counter.calls.Load())
	require.NotNil(t, h.sharedProject(withProjectLookups(context.Background()), id))
	require.EqualValues(t, 2, counter.calls.Load(), "a new request must read current data")
	// A changed principal or routing scope cannot reuse another caller's result.
	selectMap := func(c *projectLookupCache) map[string][]layout.SharedProject { return c.groups }
	calls := 0
	load := func() []layout.SharedProject { calls++; return nil }
	root := withProjectLookups(context.Background())
	memoProjectLookup(root, selectMap, load)
	memoProjectLookup(models.WithPrincipal(root, models.Principal{UserID: "another-user"}), selectMap, load)
	memoProjectLookup(remote.WithServer(root, "0123456789abcdef"), selectMap, load)
	memoProjectLookup(models.WithScopeProjects(root, []string{project.ID}), selectMap, load)
	require.Equal(t, 4, calls)
}

func TestRemoteRepositoryIdentityPushUpdatesCachedProjects(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := createProject(t, owner, "Project 1")
	other := createProject(t, owner, "Project 2")
	setRepoURL(t, owner, other.ID, "https://example.com/team/other.git")
	var deltas atomic.Int32
	originalDelta := h.remoteHub().OnProjectChanges
	h.remoteHub().OnProjectChanges = func(user, server string, changes remote.ProjectChanges) {
		originalDelta(user, server, changes)
		deltas.Add(1)
	}
	setRepoURL(t, owner, project.ID, "https://example.com/team/project.git")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	sid := savedServerID(t, h)
	key := remoteSessionKey{models.BootstrapUserID, sid}
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	require.Eventually(t, func() bool {
		groups := h.remoteServerGroups(ctx)
		return len(groups) > 0 && len(groups[0].Projects) > 0
	}, 5*time.Second, 10*time.Millisecond)
	has := func(want string) bool {
		h.remoteSessions.Lock()
		defer h.remoteSessions.Unlock()
		for _, p := range h.remoteSessions.projects[key].projects {
			if p.Name == "Project 1" {
				return p.RepoKey == want
			}
		}
		return false
	}
	require.True(t, has("example.com/team/project"))
	// Do not poll project APIs: only the pushed snapshot may change this cache.
	setRepoURL(t, owner, project.ID, "https://example.com/team/changed.git")
	require.Eventually(t, func() bool { return has("example.com/team/changed") }, 5*time.Second, 10*time.Millisecond)
	// Removing the saved URL starts a background disk lookup on the OWNER.
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[remote \"origin\"]\n url = https://example.com/team/from-disk.git\n"), 0600))
	p, err := owner.projectSvc.GetByID(context.Background(), project.ID)
	require.NoError(t, err)
	p.RepoURL = ""
	p.RepoPath = root
	require.NoError(t, owner.projectSvc.Update(context.Background(), p))
	require.Eventually(t, func() bool { return has("example.com/team/from-disk") }, 5*time.Second, 10*time.Millisecond)
	p.RepoPath = ""
	require.NoError(t, owner.projectSvc.Update(context.Background(), p))
	require.Eventually(t, func() bool { return has("") }, 5*time.Second, 10*time.Millisecond)
	require.Greater(t, deltas.Load(), int32(0), "subsequent updates must be deltas")
	require.NoError(t, owner.projectSvc.Delete(context.Background(), project.ID))
	require.Eventually(t, func() bool {
		h.remoteSessions.Lock()
		defer h.remoteSessions.Unlock()
		foundOther := false
		for _, p := range h.remoteSessions.projects[key].projects {
			if p.Name == "Project 1" {
				return false
			}
			if p.Name == "Project 2" {
				foundOther = true
			}
		}
		return foundOther
	}, 5*time.Second, 10*time.Millisecond, "deletion must remove only the deleted remote project")

}

func TestRepositoryIdentityPushFiltersPrivateProjects(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	h := tc.handler
	access := &authz.Authorizer{Store: tc.stores.ProjectAccess}
	h.SetRemote(nil, &remote.Executor{Stores: tc.stores.Stores, Projects: authz.NewProjectStore(tc.stores.Projects, access), Authorizer: access})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	invite, err := h.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := h.organizationSvc.Accept(ctx, invite, "repository-viewer", "long-password-value", "")
	require.NoError(t, err)
	token, err := h.organizationSvc.NewSession(ctx, user.UserID, time.Hour)
	require.NoError(t, err)
	project := createProject(t, h, "Project 1")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	server := httptest.NewServer(e)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var frame struct {
		Event string
		Data  json.RawMessage
	}
	require.NoError(t, wsjson.Read(ctx, conn, &frame))
	require.Equal(t, "ready", frame.Event)
	setRepoURL(t, h, project.ID, "https://example.com/team/private.git")
	for {
		require.NoError(t, wsjson.Read(ctx, conn, &frame))
		if frame.Event != "repository_projects" {
			continue
		}
		var items []models.ProjectAPIItem
		require.NoError(t, json.Unmarshal(frame.Data, &items))
		for _, item := range items {
			require.NotEqual(t, project.ID, item.ID)
		}
		break
	}
}

type pendingRepositoryStore struct {
	persistence.ProjectStore
	pending   atomic.Bool
	projectID string
}

func (s *pendingRepositoryStore) ListAPIProjects(ctx context.Context) ([]models.ProjectAPIItem, error) {
	projects, err := s.ProjectStore.ListAPIProjects(ctx)
	for i := range projects {
		if projects[i].ID == s.projectID && s.pending.Load() {
			projects[i].RepoKey = ""
			projects[i].RepoPending = true
		}
	}
	return projects, err
}
func TestPendingRepositoryPreservesSharedTab(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	project := createProject(t, h, "Project 1")
	setRepoURL(t, h, project.ID, "https://example.com/team/project.git")
	store := &pendingRepositoryStore{ProjectStore: h.projectRepo, projectID: project.ID}
	store.pending.Store(true)
	h.projectSvc = service.NewProjectService(store)
	shared := models.SharedProjectID("example.com/team/project")
	pinned := `["` + shared + `"]`
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferencePinnedProjectIDsKey, pinned))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(req))
		return rec
	}
	rec := request(http.MethodGet, "/tasks?project_id="+shared, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "data-repository-waiting")
	rec = request(http.MethodPost, "/ui/preferences", `{"pinned_project_ids":`+pinned+`}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	saved, err := h.settingsRepo.Get(context.Background(), uiPreferencePinnedProjectIDsKey)
	require.NoError(t, err)
	require.Equal(t, pinned, saved)
	store.pending.Store(false)
	rec = request(http.MethodGet, "/tasks?project_id="+shared, "")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Location"), project.ID)
	// A confirmed missing repository must not remain in a loading state.
	setRepoURL(t, h, project.ID, "")
	rec = request(http.MethodGet, "/tasks?project_id="+shared, "")
	require.Equal(t, http.StatusNotFound, rec.Code)
}
func TestRemotePendingRepositoryPreservesSharedTab(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := createProject(t, owner, "Project 1")
	setRepoURL(t, owner, project.ID, "https://example.com/team/project.git")
	store := &pendingRepositoryStore{ProjectStore: owner.remoteExecutor.Projects, projectID: project.ID}
	store.pending.Store(true)
	owner.remoteExecutor.Projects = store
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	sid := savedServerID(t, h)
	key := remoteSessionKey{models.BootstrapUserID, sid}
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	require.Eventually(t, func() bool {
		groups := h.remoteServerGroups(ctx)
		for _, g := range groups {
			for _, p := range g.Projects {
				if p.Name == "Project 1" {
					return p.RepoPending
				}
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	shared := models.SharedProjectID("example.com/team/project")
	pinned := `["` + shared + `"]`
	require.NoError(t, h.settingsRepo.Set(context.Background(), uiPreferencePinnedProjectIDsKey, pinned))
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Accept", "text/html")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(req))
		return rec
	}
	rec = request(http.MethodGet, "/tasks?project_id="+shared, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "data-repository-waiting")
	rec = request(http.MethodPost, "/ui/preferences", `{"pinned_project_ids":`+pinned+`}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	saved, err := h.settingsRepo.Get(context.Background(), uiPreferencePinnedProjectIDsKey)
	require.NoError(t, err)
	require.Equal(t, pinned, saved)
	store.pending.Store(false)
	// Trigger the owner's change notification; do not poll the project API.
	setRepoURL(t, owner, project.ID, "https://example.com/team/project.git")
	require.Eventually(t, func() bool {
		h.remoteSessions.Lock()
		defer h.remoteSessions.Unlock()
		for _, p := range h.remoteSessions.projects[key].projects {
			if p.Name == "Project 1" {
				return !p.RepoPending && p.RepoKey == "example.com/team/project"
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	rec = request(http.MethodGet, "/tasks?project_id="+shared, "")
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
}

func TestKnownSharedProjectIgnoresUnrelatedPendingLookup(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	known := createProject(t, h, "Project 1")
	setRepoURL(t, h, known.ID, "https://example.com/team/known.git")
	other := createProject(t, h, "Project 2")
	store := &pendingRepositoryStore{ProjectStore: h.projectRepo, projectID: other.ID}
	store.pending.Store(true)
	h.projectSvc = service.NewProjectService(store)
	req := httptest.NewRequest(http.MethodGet, "/tasks?project_id="+models.SharedProjectID("example.com/team/known"), nil)
	req.Header.Set("Accept", "text/html")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, signedIn(req))
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	require.Contains(t, rec.Header().Get("Location"), known.ID)
	require.NotContains(t, rec.Body.String(), "data-repository-waiting")
}
