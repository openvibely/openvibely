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

func TestRemoteHistoryPaginationUsesOwnerExecutionID(t *testing.T) {
	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	ctx := context.Background()
	project := createProject(t, owner, "History owner")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Owner"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	serverID := savedServerID(t, h)
	caller := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	store := h.execRepo.(interface {
		ListDisplayExecutions(context.Context, string, string, string, int, int) ([]models.Execution, bool, error)
	})
	for _, category := range []models.TaskCategory{models.CategoryCompleted, models.CategoryChat} {
		t.Run(string(category), func(t *testing.T) {
			task := &models.Task{ProjectID: project.ID, Title: "History " + string(category), Status: models.StatusCompleted, Category: category}
			require.NoError(t, owner.taskRepo.Create(ctx, task))
			var executions []*models.Execution
			for i := 0; i < 3; i++ {
				execution := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted}
				require.NoError(t, owner.execRepo.Create(ctx, execution))
				executions = append(executions, execution)
			}
			var projectID, taskID string
			if category == models.CategoryChat {
				projectID = remoteid.ID(serverID, project.ID)
			} else {
				taskID = remoteid.ID(serverID, task.ID)
			}
			// Wait for the saved connection and verify the unpaginated read first.
			require.Eventually(t, func() bool {
				rows, _, err := store.ListDisplayExecutions(caller, projectID, taskID, "", 1, 1024)
				return err == nil && len(rows) == 1 && rows[0].ID == remoteid.ID(serverID, executions[2].ID)
			}, 5*time.Second, 20*time.Millisecond)
			rows, hasEarlier, err := store.ListDisplayExecutions(caller, projectID, taskID, remoteid.ID(serverID, executions[2].ID), 1, 1024)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, remoteid.ID(serverID, executions[1].ID), rows[0].ID)
			require.True(t, hasEarlier)
			rows, hasEarlier, err = store.ListDisplayExecutions(caller, projectID, taskID, rows[0].ID, 1, 1024)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, remoteid.ID(serverID, executions[0].ID), rows[0].ID)
			require.False(t, hasEarlier)
		})
	}
}
