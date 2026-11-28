package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/stretchr/testify/require"
)

func TestGroupSharedProjects(t *testing.T) {
	local := []models.ProjectAPIItem{
		{ID: "l1", Name: "Foo", RepoKey: "github.com/acme/foo"},
		{ID: "l2", Name: "Only here", RepoKey: "github.com/acme/solo"},
		{ID: "l3", Name: "No repo"},
	}
	servers := []layout.RemoteServer{{ID: "s1", Name: "Box", Projects: []layout.RemoteProject{
		{ID: "r-s1-a", Name: "Foo remote", RepoKey: "github.com/acme/foo"},
		{ID: "r-s1-b", Name: "No repo"},
	}}}
	got := groupSharedProjects(local, servers)
	require.Len(t, got, 2, "every repository has one group; projects without one have none")
	require.Equal(t, models.SharedProjectID("github.com/acme/foo"), got[0].ID)
	require.Equal(t, "Foo", got[0].Name, "this computer's name wins")
	require.Equal(t, []string{"l1", "r-s1-a"}, sharedMemberIDs(&got[0]))
	require.True(t, got[0].Combined())
	require.Equal(t, got[0].ID, got[0].PageID())
	require.False(t, got[1].Combined())
	require.Equal(t, "l2", got[1].PageID(), "a single copy's pages use its own ID")

	remoteOnly := groupSharedProjects(nil, servers)
	require.Equal(t, "Foo remote", remoteOnly[0].Name, "without a local copy the first server's name is used")
}

func TestTwoClonesOnOneMachineShareATab(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	local := []models.ProjectAPIItem{
		{ID: "l1", Name: "Foo", RepoKey: "k", RepoPath: filepath.Join(home, "src", "foo")},
		{ID: "l2", Name: "Foo", RepoKey: "k", RepoPath: filepath.Join(home, "src", "foo2")},
	}
	got := groupSharedProjects(local, nil)
	require.Len(t, got, 1)
	require.True(t, got[0].Combined())
	require.Equal(t, "This computer", got[0].Machines())
	options := sharedMemberOptions(got[0])
	require.Equal(t, "Foo · This computer ("+filepath.Join("~", "src", "foo")+")", options[0].Name)
	require.Equal(t, "Foo · This computer ("+filepath.Join("~", "src", "foo2")+")", options[1].Name)
}

func TestTabIDMapsCopiesToTheirRepositoryTab(t *testing.T) {
	ctx := layout.WithSharedProjects(context.Background(), []layout.SharedProject{{ID: "__repo_1", Members: []layout.SharedMember{{ID: "a"}, {ID: "b"}}}})
	require.Equal(t, "__repo_1", layout.TabID(ctx, "a"))
	require.Equal(t, "__repo_1", layout.TabID(ctx, "__repo_1"))
	require.Equal(t, "c", layout.TabID(ctx, "c"))
}

func signedIn(req *http.Request) *http.Request {
	return req.WithContext(models.WithPrincipal(req.Context(), models.Principal{UserID: models.BootstrapUserID}))
}

func setRepoURL(t *testing.T, h *Handler, id, repoURL string) {
	t.Helper()
	p, err := h.projectSvc.GetByID(context.Background(), id)
	require.NoError(t, err)
	p.RepoURL = repoURL
	require.NoError(t, h.projectSvc.Update(context.Background(), p))
}

func TestSharedProjectAcrossMachines(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	const repo = "https://github.com/acme/foo.git"

	localProject := createProject(t, h, "Foo")
	setRepoURL(t, h, localProject.ID, repo)
	createTask(t, h, localProject.ID, "Local shared task")
	other := createProject(t, h, "Other")
	createTask(t, h, other.ID, "Unrelated task")

	remoteProject := createProject(t, owner, "Foo")
	setRepoURL(t, owner, remoteProject.ID, repo)
	createTask(t, owner, remoteProject.ID, "Remote shared task")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Box"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	serverID := savedServerID(t, h)

	ctx := context.Background()
	require.Eventually(t, func() bool {
		return len(h.sharedProjects(ctx, h.remoteServerGroups(ctx))) == 1
	}, 5*time.Second, 50*time.Millisecond)
	shared := h.sharedProjects(ctx, h.remoteServerGroups(ctx))[0]
	require.ElementsMatch(t, []string{localProject.ID, remoteid.ID(serverID, remoteProject.ID)}, sharedMemberIDs(&shared))

	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, target, nil)))
		return rec
	}

	list := get("/tasks?project_id=" + shared.ID)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.Contains(t, list.Body.String(), "Local shared task")
	require.Contains(t, list.Body.String(), "Remote shared task")
	require.NotContains(t, list.Body.String(), "Unrelated task")

	require.Equal(t, http.StatusNotFound, get("/tasks?project_id="+models.SharedProjectID("github.com/nobody/none")).Code)

	chat := get("/chat?project_id=" + shared.ID)
	require.Equal(t, http.StatusOK, chat.Code, chat.Body.String())
	require.Contains(t, chat.Body.String(), `id="shared-chat-form-project-select"`)
	for id, label := range map[string]string{
		localProject.ID:                         "Local",
		remoteid.ID(serverID, remoteProject.ID): "Box",
	} {
		_, after, found := strings.Cut(chat.Body.String(), `<li data-value="`+id+`"`)
		require.True(t, found, "missing destination %s", id)
		option, _, closed := strings.Cut(after, "</li>")
		require.True(t, closed)
		require.True(t, strings.HasSuffix(strings.TrimSpace(option), "</span>"+label), "destination %s: %s", id, option)
	}
	require.Contains(t, chat.Body.String(), `data-project-send-url="/chat/send?project_id="`)
	require.Equal(t, http.StatusBadRequest, func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/chat/send?project_id="+shared.ID, strings.NewReader("message=hi"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		e.ServeHTTP(rec, signedIn(req))
		return rec.Code
	}())

	create := func(destination string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+shared.ID+"&from=new", strings.NewReader("destination_project="+destination+"&title=New+shared&prompt=Do+it&category=backlog&priority=2"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		e.ServeHTTP(rec, signedIn(req))
		return rec
	}
	require.Equal(t, http.StatusBadRequest, create(other.ID).Code)
	created := create(localProject.ID)
	require.Equal(t, http.StatusOK, created.Code, created.Body.String())
	tasks, err := h.taskRepo.ListByProject(ctx, localProject.ID, "")
	require.NoError(t, err)
	var titles []string
	for _, task := range tasks {
		titles = append(titles, task.Title)
	}
	require.Contains(t, titles, "New shared")
}
