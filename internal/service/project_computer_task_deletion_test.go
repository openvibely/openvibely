package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerTaskDeletionDoesNotBlockOtherTask(t *testing.T) {
	db := teststores.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	project := &models.Project{Name: "Deletion isolation"}
	require.NoError(t, db.Projects.Create(ctx, project))
	deleting := &models.Task{ProjectID: project.ID, Title: "Delete", Category: models.CategoryCompleted, Status: models.StatusCompleted, Priority: 2}
	other := &models.Task{ProjectID: project.ID, Title: "Keep running", Category: models.CategoryActive, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, db.Tasks.Create(ctx, deleting))
	require.NoError(t, db.Tasks.Create(ctx, other))
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/desktops/"+ComputerDesktopID(deleting.ID)+"/" {
			t.Errorf("unexpected desktop cleanup: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	defer close(release)
	root := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL}})
	require.NoError(t, root.SetEnabled(ctx, project.ID, true))
	first, err := root.ForTask(ctx, project.ID, deleting.ID)
	require.NoError(t, err)
	second, err := root.ForTask(ctx, project.ID, other.ID)
	require.NoError(t, err)
	require.NoError(t, first.SetTaskEnabled(ctx, project.ID, true))
	require.NoError(t, second.SetTaskEnabled(ctx, project.ID, true))
	viewerCtx, cancelViewer := context.WithCancel(ctx)
	defer cancelViewer()
	_, detachFirst, err := first.RegisterViewer(ctx, project.ID, "user", "first", true, cancelViewer)
	require.NoError(t, err)
	defer detachFirst()
	token, detachSecond, err := second.RegisterViewer(ctx, project.ID, "user", "second", true)
	require.NoError(t, err)
	defer detachSecond()
	worker := &WorkerService{}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	registration := worker.RegisterCancelOwned(other.ID, cancelRun)
	defer worker.DeregisterCancelOwned(other.ID, registration)
	tasks := NewTaskService(db.Tasks, nil, worker)
	tasks.SetProjectComputerService(root)
	finished := make(chan error, 1)
	go func() { finished <- tasks.Delete(ctx, deleting.ID) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("deletion did not reach desktop cleanup")
	}
	require.ErrorIs(t, viewerCtx.Err(), context.Canceled)
	// While the deleted desktop is slow to stop, other task preferences, viewer
	// input, and database writes must still work.
	check, stopCheck := context.WithTimeout(ctx, time.Second)
	defer stopCheck()
	reloaded, err := root.ForTask(check, project.ID, other.ID)
	require.NoError(t, err)
	state, err := reloaded.Get(check, project.ID)
	require.NoError(t, err)
	require.True(t, state.TaskEnabled)
	require.True(t, state.Enabled)
	require.NoError(t, second.ForwardViewerInput(project.ID, "second", token, func() error { return nil }))
	require.NoError(t, reloaded.WithHumanInput(check, project.ID, func() error { return nil }))
	require.NoError(t, db.Tasks.Create(check, &models.Task{ProjectID: project.ID, Title: "New task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}))
	require.NoError(t, db.Tasks.UpdateStatus(check, other.ID, models.StatusCompleted))
	require.NoError(t, runCtx.Err(), "deleting another task cancelled this execution")
	// Cancel the cleanup request rather than letting it wait for the desktop.
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled deletion did not finish")
	}
}
