package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRemoteChangesSettingsIgnoreSelectedProject(t *testing.T) {
	ctx := context.Background()
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	local := teststores.New(t)
	wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
	env := wireTestHandler(t, wrapped, testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	srv, _, owner := fakeRemoteOV(t, "secret")
	project := &models.Project{Name: "Remote destination", RepoPath: createHandlerTestGitRepo(t)}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(ctx, project))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	require.Eventually(t, func() bool { return hub.Get(models.BootstrapUserID, sid).Connected() }, 5*time.Second, 20*time.Millisecond)
	task := &models.Task{ProjectID: project.ID, Title: "Remote merge", Category: models.CategoryCompleted, Status: models.StatusCompleted}
	require.NoError(t, owner.remoteExecutor.Stores.Tasks.Create(ctx, task))
	require.NoError(t, local.Stores.Settings.Set(ctx, uiPreferenceSelectedProjectIDKey, "default"))
	r := httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/tasks/"+remoteid.ID(sid, task.ID)+"/worktree/action-settings?project_id="+url.QueryEscape(remoteid.ID(sid, project.ID)), nil))
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	var data changesActionOptions
	require.NoError(t, json.Unmarshal(r.Body.Bytes(), &data))
	require.NotEmpty(t, data.LocalBranches)
	endpoint := "/tasks/" + remoteid.ID(sid, task.ID) + "/worktree/"
	query := "?project_id=" + url.QueryEscape(remoteid.ID(sid, project.ID))
	data.Preferences.Method = "squash"
	raw, err := json.Marshal(data.Preferences)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, endpoint+"action-settings"+query, strings.NewReader(string(raw)))
	req.Header.Set("Content-Type", "application/json")
	r = httptest.NewRecorder()
	e.ServeHTTP(r, req)
	require.Equal(t, http.StatusNoContent, r.Code, r.Body.String())
	saved, err := owner.remoteExecutor.Stores.Settings.Get(ctx, changesPreferenceKey(project.ID))
	require.NoError(t, err)
	require.Contains(t, saved, "squash")
	untouched, err := local.Stores.Settings.Get(ctx, changesPreferenceKey(project.ID))
	require.NoError(t, err)
	require.Empty(t, untouched)
	r = httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest(http.MethodGet, endpoint+"action-local"+query+"&target="+url.QueryEscape(data.LocalBranches[0]), nil))
	require.Equal(t, http.StatusOK, r.Code, r.Body.String())
	r = httptest.NewRecorder()
	e.ServeHTTP(r, httptest.NewRequest(http.MethodGet, endpoint+"action-settings?project_id=unrelated", nil))
	require.Equal(t, http.StatusNotFound, r.Code, r.Body.String())

}
