package service

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestComputerViewerReleaseProtectsOtherControllers(t *testing.T) {
	db := teststores.New(t)
	ctx := t.Context()
	project := &models.Project{Name: "viewers"}
	require.NoError(t, db.Projects.Create(ctx, project))
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{})
	require.NoError(t, svc.repo.Configure(ctx, project.ID, "local"))
	// No backend work is necessary to test the ownership/lifecycle boundary.
	require.NoError(t, svc.repo.Hold(ctx, project.ID, true))
	_, leaveA, err := svc.RegisterViewer(project.ID, "alice", "a", true)
	require.NoError(t, err)
	_, leaveB, err := svc.RegisterViewer(project.ID, "bob", "b", true)
	require.NoError(t, err)
	leaveA()
	released, err := svc.ReleaseUnviewed(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, released)
	state, err := svc.Get(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, state.Held)
	require.Error(t, svc.ViewerActivity(ctx, project.ID, "alice", "b"), "another user's activity is rejected")
	leaveB()
	// A failed/pending takeover must not be silently cleared by disconnection.
	require.NoError(t, svc.repo.SetTakeoverPending(ctx, project.ID, true))
	released, err = svc.ReleaseUnviewed(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, released)
	require.NoError(t, svc.repo.SetTakeoverPending(ctx, project.ID, false))
	released, err = svc.ReleaseUnviewed(ctx, project.ID)
	require.NoError(t, err)
	require.True(t, released)
}

func TestComputerViewerDisconnectKeepsPrivateHandoff(t *testing.T) {
	db := teststores.New(t)
	ctx := t.Context()
	project := &models.Project{Name: "private viewer"}
	require.NoError(t, db.Projects.Create(ctx, project))
	task := &models.Task{ProjectID: project.ID, Title: "sign in", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
	require.NoError(t, db.Tasks.Create(ctx, task))
	root := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{})
	require.NoError(t, root.repo.Configure(ctx, project.ID, "local"))
	svc, err := root.ForTask(ctx, project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, svc.repo.RequestHandoff(ctx, project.ID, "login", "https://example.com", "Sign in", ""))
	released, err := svc.ReleaseUnviewed(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, released)
	state, err := svc.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "login", state.HandoffKind)
	require.True(t, state.Held)
}
