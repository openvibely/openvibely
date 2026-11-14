package service

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComputerFailedTakeoverKeepsInputBlocked(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Pending takeover"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	task := &models.Task{ProjectID: project.ID, Title: "Task", Prompt: "Task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) }))
	defer bridge.Close()
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: bridge.URL}})
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	require.ErrorIs(t, scoped.Control(t.Context(), project.ID, true, func() error { return context.DeadlineExceeded }), context.DeadlineExceeded)
	blocked := func() {
		require.Error(t, scoped.WithHumanInput(t.Context(), project.ID, func() error { t.Fatal("input accepted before interruption completed"); return nil }))
	}
	blocked()
	reloaded, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, reloaded.SetTaskEnabled(t.Context(), project.ID, true))
	state, err := reloaded.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.True(t, state.TakeoverPending)
	require.NoError(t, reloaded.Control(t.Context(), project.ID, true, func() error { blocked(); return nil }))
	require.NoError(t, reloaded.WithHumanInput(t.Context(), project.ID, func() error { return nil }))
	require.Error(t, reloaded.Control(t.Context(), project.ID, true, func() error { return errors.New("interruption failed") }))
	require.NoError(t, reloaded.Control(t.Context(), project.ID, false, nil))
	state, err = reloaded.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.False(t, state.TakeoverPending)
	require.False(t, state.Held)
}
