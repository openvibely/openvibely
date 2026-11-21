package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestRemoteSkillsOperateOnOwningServer(t *testing.T) {
	hub := &remote.Hub{}
	t.Cleanup(hub.Shutdown)
	stores := teststores.New(t)
	env := wireTestHandler(t, remote.Wrap(stores.Stores, &remote.Router{Hub: hub}), testHandlerOptions{})
	h, e := env.Handler, env.Echo
	h.SetRemote(hub, nil)
	localRoot := t.TempDir()
	h.SetAgentSkillRoot(localRoot)
	writeStandaloneSkill(t, localRoot, "backup", "Local backup", "local description", "global")
	srv, _, owner := fakeRemoteOV(t, "secret")
	remoteRoot := t.TempDir()
	owner.SetAgentSkillRoot(remoteRoot)
	writeStandaloneSkill(t, remoteRoot, "backup", "Remote backup", "remote description", "global")
	p := &models.Project{Name: "Project 1", RepoPath: t.TempDir()}
	require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(t.Context(), p))
	projectRoot := filepath.Join(p.RepoPath, ".openvibely")
	writeStandaloneSkill(t, projectRoot, "project_backup", "Project backup", "project description", "project")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
	sid := savedServerID(t, h)
	project := remoteid.ID(sid, p.ID)
	ctx := models.WithPrincipal(context.Background(), models.Principal{UserID: models.BootstrapUserID})
	require.Eventually(t, func() bool { return !h.remoteDown(ctx, sid) }, 5*time.Second, 20*time.Millisecond)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	rec = request("GET", "/skills?project_id="+project, "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.NotContains(t, rec.Body.String(), "Remote backup")
	require.Contains(t, rec.Body.String(), "Local backup")
	rec = request("GET", "/skills/backup/details?project_id="+project+"&scope=global", "")
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "local description")
	// Global actions stay Local even while the selected project is remote.
	rec = request("POST", "/skills/backup/enabled?project_id="+models.AllProjectsID+"&skill_project_id="+project, `{"scope":"global","enabled":false}`)
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "Local backup")
	require.NotContains(t, rec.Body.String(), "Remote backup")
	require.Contains(t, rec.Body.String(), "Project backup")

	payload, err := json.Marshal(bulkSkillsRequest{Skills: []bulkSkillRef{{ProjectID: project, Scope: "project", Handle: "project_backup"}}})
	require.NoError(t, err)
	rec = request("DELETE", "/skills/bulk?project_id="+models.AllProjectsID, string(payload))
	require.Equal(t, 200, rec.Code, rec.Body.String())
	require.FileExists(t, filepath.Join(localRoot, "skills", "backup", "SKILL.md"))
	require.FileExists(t, filepath.Join(remoteRoot, "skills", "backup", "SKILL.md"))
	require.NoFileExists(t, filepath.Join(projectRoot, "skills", "project_backup", "SKILL.md"))
	// Even a tagged project whose path exists on this machine must never be resolved locally.
	req := httptest.NewRequest("GET", "/skills?project_id="+project, nil).WithContext(ctx)
	require.Empty(t, h.currentProjectSkillRoot(e.NewContext(req, httptest.NewRecorder())))
}
