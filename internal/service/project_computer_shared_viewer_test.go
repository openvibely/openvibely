package service

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
)

func TestComputerViewerInputDoesNotHoldAgentTools(t *testing.T) {
	_, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	require.NoError(t, s.repo.Reserve(t.Context(), p.ID, task.ID))
	token, leave, err := s.RegisterViewer(t.Context(), p.ID, "user", "session", true)
	require.NoError(t, err)
	defer leave()
	// A long-running agent action must not serialize ordinary viewer input.
	lock := s.lock(p.ID)
	lock.Lock()
	done := make(chan error, 1)
	go func() { done <- s.ForwardViewerInput(p.ID, "session", token, func() error { return nil }) }()
	select {
	case err := <-done:
		lock.Unlock()
		require.NoError(t, err)
	case <-time.After(time.Second):
		lock.Unlock()
		<-done
		t.Fatal("viewer input blocked behind agent action")
	}
	state, err := s.Get(t.Context(), p.ID)
	require.NoError(t, err)
	require.False(t, state.Held)
	require.Equal(t, task.ID, state.Owner)
	require.NoError(t, s.repo.Release(t.Context(), p.ID, task.ID))
	rt := s.runtimeTools(t.Context(), p.ID, task.ID)
	_, handled, bad, err := rt.Executor(t.Context(), "computer", json.RawMessage(`{"action":"get_desktop_state"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)

	require.NoError(t, s.repo.RequestHandoff(t.Context(), p.ID, "request_browser_login", "https://example.test", "Sign in", "Account"))
	require.NoError(t, s.checkViewerPermission(t.Context(), p.ID))
	_, _, _, err = rt.Executor(t.Context(), "computer", json.RawMessage(`{"action":"get_desktop_state"}`))
	require.ErrorIs(t, err, persistence.ErrComputerBusy)
	require.NoError(t, s.repo.SetTakeoverPending(t.Context(), p.ID, true))
	require.ErrorIs(t, s.checkViewerPermission(t.Context(), p.ID), persistence.ErrComputerBusy)
}
