package service

import (
	"context"
	"errors"
	"fmt"
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

func TestComputerStartWaitsForProjectDeletion(t *testing.T) {
	for _, failDelete := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "deletion rolled back"}[failDelete], func(t *testing.T) {
			db := teststores.New(t)
			project := &models.Project{Name: "Concurrent deletion"}
			require.NoError(t, db.Projects.Create(t.Context(), project))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) }))
			defer server.Close()
			computer := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL}})
			require.NoError(t, db.Projects.Computers().Configure(t.Context(), project.ID, "local"))
			projects := NewProjectService(db.Projects)
			projects.SetProjectComputerService(computer)
			projects.SetTaskService(NewTaskService(db.Tasks, nil, nil))
			deleting := make(chan struct{})
			finishDelete := make(chan struct{})
			projects.beforeRelationalDeleteForTest = func() {
				close(deleting)
				<-finishDelete
				if failDelete {
					// Change the deletion manifest, forcing its transaction to abort.
					task := &models.Task{ProjectID: project.ID, Title: "New task", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: 1}
					require.NoError(t, db.Tasks.Create(t.Context(), task))
				}
			}
			deleteDone := make(chan error, 1)
			go func() { deleteDone <- projects.Delete(t.Context(), project.ID) }()
			<-deleting
			// Deletion must own the SAME lock used by all scoped computer services.
			lock := computer.lifecycle(project.ID)
			if lock.TryLock() {
				lock.Unlock()
				close(finishDelete)
				t.Fatal("deletion did not hold lifecycle lock")
			}
			startDone := make(chan error, 1)
			go func() { startDone <- computer.Start(t.Context(), project.ID) }()
			close(finishDelete)
			deleteErr := <-deleteDone
			startErr := <-startDone
			if failDelete {
				require.Error(t, deleteErr)
				require.NoError(t, startErr)
			} else {
				require.NoError(t, deleteErr)
				require.Error(t, startErr)
			}
		})
	}
}

func TestComputerCleanupFailurePreservesProjectForRetry(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Retry cleanup"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	attempts := 0
	p := &removingComputerProvisioner{removed: func(context.Context, string) error {
		attempts++
		if attempts == 1 {
			return errors.New("volume is in use")
		}
		return nil
	}}
	computer := NewProjectComputerService(db.Projects, p)
	require.NoError(t, db.Projects.Computers().Configure(t.Context(), project.ID, "local"))
	require.NoError(t, db.Projects.Computers().MarkCleanupRequired(t.Context(), project.ID))
	projects := NewProjectService(db.Projects)
	projects.SetProjectComputerService(computer)
	require.ErrorContains(t, projects.Delete(t.Context(), project.ID), "retry project deletion")
	found, err := db.Projects.GetByID(t.Context(), project.ID)
	require.NoError(t, err)
	require.NotNil(t, found)
	state, err := computer.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.True(t, state.CleanupRequired)
	require.NoError(t, projects.Delete(t.Context(), project.ID))
	found, err = db.Projects.GetByID(t.Context(), project.ID)
	require.NoError(t, err)
	require.Nil(t, found)
	require.Equal(t, 2, attempts)
}

func TestComputerVolumeRemovalRetriesWithoutContainer(t *testing.T) {
	name := computerContainerName("project")
	container, volume := true, true
	attempts := 0
	p := &LocalComputerProvisioner{run: func(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "container":
			if container {
				return []byte(name), nil
			}
		case "inspect":
			return []byte(fmt.Sprintf(`[{"Config":{"Labels":{"openvibely.project":"project"}},"State":{"Running":false},"Mounts":[{"Name":%q,"Destination":"/home/ubuntu","Type":"volume"}]}]`, name+"-home")), nil
		case "rm":
			container = false
		case "volume":
			if args[1] == "ls" && volume {
				return []byte(name + "-home"), nil
			}
			if args[1] == "rm" {
				require.Equal(t, name+"-home", args[2])
				attempts++
				if attempts == 1 {
					return nil, errors.New("volume removal failed")
				}
				volume = false
			}
		}
		return nil, nil
	}}
	require.Error(t, p.RemoveProject(t.Context(), "project"))
	require.False(t, container)
	require.True(t, volume)
	require.NoError(t, p.RemoveProject(t.Context(), "project"))
	require.False(t, volume)
	require.NoError(t, p.RemoveProject(t.Context(), "project"))
	require.Equal(t, 2, attempts)
}
