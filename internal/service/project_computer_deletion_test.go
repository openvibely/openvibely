package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerUnusedProjectDeletionWithoutDocker(t *testing.T) {
	for _, attempted := range []bool{false, true} {
		t.Run(map[bool]string{false: "never started", true: "Docker unavailable at first start"}[attempted], func(t *testing.T) {
			db := teststores.New(t)
			project := &models.Project{Name: "Unused computer"}
			require.NoError(t, db.Projects.Create(t.Context(), project))
			calls := 0
			provisioner := &LocalComputerProvisioner{run: func(context.Context, io.Reader, ...string) ([]byte, error) {
				calls++
				return nil, errors.New("Docker unavailable")
			}}
			computer := NewProjectComputerService(db.Projects, provisioner)
			task := &models.Task{ProjectID: project.ID, Title: "Ordinary task", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 1}
			require.NoError(t, db.Tasks.Create(t.Context(), task))
			require.NotNil(t, computer.RuntimeTools(t.Context(), project.ID, task.ID))
			if attempted {
				require.Error(t, computer.Start(t.Context(), project.ID))
			}
			state, err := computer.Get(t.Context(), project.ID)
			require.NoError(t, err)
			require.False(t, state.CleanupRequired)
			calls = 0
			projects := NewProjectService(db.Projects)
			projects.SetProjectComputerService(computer)
			projects.SetTaskService(NewTaskService(db.Tasks, nil, nil))
			require.NoError(t, projects.Delete(t.Context(), project.ID))
			require.Zero(t, calls, "deletion must not contact Docker")
			found, err := db.Projects.GetByID(t.Context(), project.ID)
			require.NoError(t, err)
			require.Nil(t, found)
		})
	}
}

func TestComputerProvisionedProjectDeletionRetainsRecordsWhenDockerUnavailable(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Provisioned computer"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }))
	defer server.Close()
	computer := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL}})
	require.NoError(t, computer.Start(t.Context(), project.ID))
	state, err := computer.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.True(t, state.CleanupRequired)
	// Recreate the service to verify cleanup intent survives application restart.
	calls := 0
	computer = NewProjectComputerService(db.Projects, &LocalComputerProvisioner{run: func(context.Context, io.Reader, ...string) ([]byte, error) {
		calls++
		return nil, errors.New("Docker unavailable")
	}})
	projects := NewProjectService(db.Projects)
	projects.SetProjectComputerService(computer)
	projects.SetTaskService(NewTaskService(db.Tasks, nil, nil))
	require.ErrorContains(t, projects.Delete(t.Context(), project.ID), "start Docker and retry")
	require.Positive(t, calls)
	found, err := db.Projects.GetByID(t.Context(), project.ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	state, err = computer.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.True(t, state.CleanupRequired)
}

type partialComputerProvisioner struct{ fakeComputerProvisioner }

func (p *partialComputerProvisioner) Start(context.Context, string) (ComputerConnection, error) {
	return ComputerConnection{}, errors.New("failed after creating resources")
}

func TestComputerFailedProvisioningPreservesCleanupIntent(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Partial computer"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	computer := NewProjectComputerService(db.Projects, &partialComputerProvisioner{})
	require.Error(t, computer.Start(t.Context(), project.ID))
	state, err := computer.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.True(t, state.CleanupRequired)
}
