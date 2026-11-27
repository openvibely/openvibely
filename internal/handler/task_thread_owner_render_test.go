package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
)

func TestTaskThreadRenderUsesOwnerInsteadOfCreationScope(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	sid, _ := signInTwoUsers(t, h, e, srv.URL)
	localProject := createProject(t, h, "Local project")
	remoteProject := createProject(t, owner, "Remote project")
	localModel := createAgent(t, h.llmConfigRepo, func(m *models.LLMConfig) { m.Name = "Local unique model" })
	remoteModel := createAgent(t, owner.llmConfigRepo, func(m *models.LLMConfig) { m.Name = "Remote unique model" })
	localTask := createTask(t, h, localProject.ID, "Local task", func(task *models.Task) { task.AgentID = &localModel.ID })
	remoteTask := createTask(t, owner, remoteProject.ID, "Remote task", func(task *models.Task) { task.AgentID = &remoteModel.ID })
	for _, tc := range []struct{ name, taskID, wrongScope, ownerScope, modelID, want, absent string }{
		{"remote", remoteid.ID(sid, remoteTask.ID), "", sid, remoteid.ID(sid, remoteModel.ID), remoteModel.Name, localModel.Name},
		{"local", localTask.ID, sid, "", localModel.ID, localModel.Name, remoteModel.Name},
	} {
		t.Run(tc.name, func(t *testing.T) {
			render := func(scope string, first bool) string {
				req := httptest.NewRequest(http.MethodPost, "/tasks?from=new&thread=1", nil)
				req.Header.Set("HX-Request", "true")
				ctx := models.WithPrincipal(req.Context(), models.Principal{UserID: models.BootstrapUserID})
				req = req.WithContext(remote.WithServer(ctx, scope))
				rec := httptest.NewRecorder()
				c := e.NewContext(req, rec)
				c.SetParamNames("taskId")
				c.SetParamValues(tc.taskID)
				if first {
					c.Set("newTaskThread", true)
				}
				require.NoError(t, h.GetTaskThread(c))
				return rec.Body.String()
			}
			first := render(tc.wrongScope, true)
			refreshed := render(tc.ownerScope, false)
			for _, body := range []string{first, refreshed} {
				require.Contains(t, body, tc.want)
				require.NotContains(t, body, tc.absent)
				require.Contains(t, body, `name="agent_id" value="`+tc.modelID+`"`)
			}
		})
	}
}
