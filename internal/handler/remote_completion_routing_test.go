package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/stretchr/testify/require"
)

func TestRemoteCompletionKeepsOwnerWithOtherServerInOutput(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	ctx := context.Background()
	ownerStores := remote.Wrap(owner.remoteExecutor.Stores, &remote.Router{})
	project := createProject(t, owner, "Completion owner")
	task := &models.Task{ProjectID: project.ID, Title: "Remote execution", Status: models.StatusRunning, Category: models.CategoryActive}
	require.NoError(t, ownerStores.Tasks.Create(ctx, task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, ownerStores.Executions.Create(ctx, execution))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Owner"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	serverID := savedServerID(t, h)
	output := "Test output mentions " + remoteid.ID("fedcba9876543210", "another-project")
	outcome, err := ownerStores.Tasks.CompleteExecutionSuccess(ctx, execution.ID, output, 2, 3)
	require.NoError(t, err)
	require.Equal(t, persistence.CompleteSuccessCompleted, outcome)
	caller := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.Eventually(t, func() bool {
		got, err := h.execRepo.GetByID(caller, remoteid.ID(serverID, execution.ID))
		return err == nil && got != nil && got.Status == models.ExecCompleted && got.Output == output
	}, 5*time.Second, 20*time.Millisecond)
	local, err := h.execRepo.GetByID(ctx, execution.ID)
	require.NoError(t, err)
	require.Nil(t, local, "remote completion must not create a local execution")
}
