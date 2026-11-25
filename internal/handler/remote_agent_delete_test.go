package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type agentDeleteMemberStore struct{ persistence.OrganizationStore }

func (s agentDeleteMemberStore) UserByID(_ context.Context, id string) (*models.OrgUser, error) {
	return &models.OrgUser{Principal: models.Principal{UserID: id, Role: "member"}}, nil
}

func TestSingleRemoteAgentDeletePreservesLocalFiles(t *testing.T) {
	for _, mode := range []string{"allowed", "denied", "disconnected", "protected"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			hub := &remote.Hub{}
			t.Cleanup(hub.Shutdown)
			local := teststores.New(t)
			wrapped := remote.Wrap(local.Stores, &remote.Router{Hub: hub})
			env := wireTestHandler(t, wrapped, testHandlerOptions{})
			h, e := env.Handler, env.Echo
			h.SetRemote(hub, nil)
			h.SetAgentRepo(wrapped.Agents)
			srv, _, owner := fakeRemoteOV(t, "secret")
			owner.SetAgentRepo(owner.remoteExecutor.Stores.Agents)
			project := &models.Project{Name: "Project 1", RepoPath: t.TempDir()}
			require.NoError(t, owner.remoteExecutor.Stores.Projects.Create(ctx, project))
			agent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeProject, ProjectID: project.ID}
			if mode == "protected" {
				agent.GeneratedStatus = models.AgentStatusProtected
			}
			require.NoError(t, owner.remoteExecutor.Stores.Agents.Create(ctx, agent))
			localAgent := &models.Agent{Name: "Reviewer", Key: "reviewer", Scope: models.AgentScopeGlobal}
			require.NoError(t, local.Stores.Agents.Create(ctx, localAgent))
			localRoot, remoteRoot := t.TempDir(), filepath.Join(project.RepoPath, ".openvibely")
			h.SetAgentSkillRoot(localRoot)
			owner.SetAgentSkillRoot(remoteRoot)
			for _, root := range []string{localRoot, remoteRoot} {
				dir := filepath.Join(root, "agents", "reviewer", "skills", "backup")
				require.NoError(t, os.MkdirAll(dir, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill contents"), 0600))
			}
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Eventually(t, func() bool { return len(hub.Servers(models.BootstrapUserID)) == 1 }, 5*time.Second, 20*time.Millisecond)
			sid := savedServerID(t, h)
			if mode == "denied" {
				owner.SetOrganizationService(service.NewOrganizationService(agentDeleteMemberStore{owner.remoteExecutor.Stores.Organizations}, "secret"))
			}
			if mode == "disconnected" {
				hub.Close(models.BootstrapUserID, sid)
			}
			rec = httptest.NewRecorder()
			req := serversRequest(http.MethodDelete, "/agents/"+remoteid.ID(sid, agent.ID)+"?project_id="+remoteid.ID(sid, project.ID), nil)
			e.ServeHTTP(rec, req.WithContext(models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})))
			require.FileExists(t, filepath.Join(localRoot, "agents", "reviewer", "skills", "backup", "SKILL.md"))
			kept, err := local.Stores.Agents.GetByID(ctx, localAgent.ID)
			require.NoError(t, err)
			require.NotNil(t, kept)
			remaining, err := owner.remoteExecutor.Stores.Agents.GetByID(ctx, agent.ID)
			require.NoError(t, err)
			if mode == "allowed" {
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				require.Nil(t, remaining)
				require.NoDirExists(t, filepath.Join(remoteRoot, "agents", "reviewer"))
			} else {
				require.NotEqual(t, http.StatusOK, rec.Code, rec.Body.String())
				require.NotNil(t, remaining)
				require.FileExists(t, filepath.Join(remoteRoot, "agents", "reviewer", "skills", "backup", "SKILL.md"))
			}
		})
	}
}
