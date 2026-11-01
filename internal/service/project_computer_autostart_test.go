package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type startingComputerFixture struct {
	mu       sync.Mutex
	endpoint string
	running  bool
	starts   int
}

func (p *startingComputerFixture) Provider() string { return "local" }
func (p *startingComputerFixture) Resolve(context.Context, string) (ComputerConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return ComputerConnection{}, errors.New("stopped")
	}
	return ComputerConnection{Endpoint: p.endpoint}, nil
}
func (p *startingComputerFixture) Start(context.Context, string) (ComputerConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = true
	p.starts++
	return ComputerConnection{Endpoint: p.endpoint}, nil
}
func (p *startingComputerFixture) Stop(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running = false
	return nil
}

func TestComputerAutomaticStartAndProjectPreference(t *testing.T) {
	db := teststores.New(t)
	p := &models.Project{Name: "Automatic computer"}
	require.NoError(t, db.Projects.Create(t.Context(), p))
	task := &models.Task{ProjectID: p.ID, Title: "Never messaged", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ready":true}`))
	}))
	defer server.Close()
	provider := &startingComputerFixture{endpoint: server.URL}
	svc := NewProjectComputerService(db.Projects, provider)
	scoped, err := svc.ForTask(t.Context(), p.ID, task.ID)
	require.NoError(t, err)
	state, err := scoped.Get(t.Context(), p.ID)
	require.NoError(t, err)
	require.True(t, state.Enabled)
	require.Equal(t, 0, provider.starts, "viewing task metadata must not start Docker")
	tools := svc.RuntimeTools(t.Context(), p.ID, task.ID)
	require.NotNil(t, tools)
	_, handled, bad, err := tools.Executor(t.Context(), "computer_files", json.RawMessage(`{"operation":"list_files","input":{}}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	require.Equal(t, 1, provider.starts)
	// A stopped container must recover even while the task retains its reservation.
	require.NoError(t, provider.Stop(t.Context(), p.ID))
	require.NoError(t, scoped.EnsureStarted(t.Context(), p.ID))
	require.Equal(t, 2, provider.starts)
	require.NoError(t, scoped.EnsureStarted(t.Context(), p.ID))
	require.Equal(t, 2, provider.starts)
	_, _, _, err = tools.Executor(t.Context(), "release_computer", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.NoError(t, svc.SetEnabled(t.Context(), p.ID, false))
	require.False(t, provider.running)
	require.Nil(t, svc.RuntimeTools(t.Context(), p.ID, task.ID))
	require.Error(t, scoped.EnsureStarted(t.Context(), p.ID))
	require.Equal(t, 2, provider.starts)
	require.NoError(t, svc.SetEnabled(t.Context(), p.ID, true))
	require.NoError(t, scoped.EnsureStarted(t.Context(), p.ID))
	require.Equal(t, 3, provider.starts)
}
