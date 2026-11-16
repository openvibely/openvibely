package service

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestComputerViewerDisconnectProtectsOtherControllers(t *testing.T) {
	db := teststores.New(t)
	ctx := t.Context()
	project := &models.Project{Name: "viewers"}
	require.NoError(t, db.Projects.Create(ctx, project))
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{})
	require.NoError(t, svc.repo.Configure(ctx, project.ID, "local"))
	if err := svc.repo.SetEnabled(ctx, project.ID, true); err != nil {
		t.Fatal(err)
	}
	tokenA, leaveA, err := svc.RegisterViewer(t.Context(), project.ID, "alice", "a", true)
	require.NoError(t, err)
	tokenB, leaveB, err := svc.RegisterViewer(t.Context(), project.ID, "bob", "b", true)
	require.NoError(t, err)
	_, _, err = svc.RegisterViewer(t.Context(), project.ID, "alice", "b", true)
	require.Error(t, err, "another user cannot replace a viewer")
	require.NoError(t, svc.ForwardViewerInput(project.ID, "a", tokenA, func() error { return nil }))
	require.NoError(t, svc.ForwardViewerInput(project.ID, "b", tokenB, func() error { return nil }))
	leaveA()
	require.NoError(t, svc.ForwardViewerInput(project.ID, "b", tokenB, func() error { return nil }), "closing Alice's viewer must not interrupt Bob")

	leaveB()
	require.Error(t, svc.ForwardViewerInput(project.ID, "b", tokenB, func() error { t.Fatal("detached viewer forwarded input"); return nil }))
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
	if err := root.repo.SetEnabled(ctx, project.ID, true); err != nil {
		t.Fatal(err)
	}
	svc, err := root.ForTask(ctx, project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, svc.SetTaskEnabled(ctx, project.ID, true))
	for _, kind := range []string{"request_browser_login", "request_computer_help"} {
		t.Run(kind, func(t *testing.T) {
			require.NoError(t, svc.repo.RequestHandoff(ctx, project.ID, kind, "https://example.com", "Private help", ""))
			_, leave, err := svc.RegisterViewer(ctx, project.ID, "alice", "preview", true)
			require.NoError(t, err)
			leave()
			state, err := svc.Get(ctx, project.ID)
			require.NoError(t, err)
			require.Equal(t, kind, state.HandoffKind)
			require.True(t, state.Held)
			require.NoError(t, svc.repo.FinishHandoff(ctx, project.ID, true))
		})
	}
}

// Embedding the store lets this test reject any database read after registration.
type viewerReadGuard struct {
	persistence.ProjectComputerStore
	reads     int
	forbidden bool
}

func (r *viewerReadGuard) Get(ctx context.Context, id string) (*models.ProjectComputer, error) {
	r.reads++
	if r.forbidden {
		return nil, errors.New("unexpected permission query")
	}
	return r.ProjectComputerStore.Get(ctx, id)
}

func TestComputerViewerForwardingAndRevocation(t *testing.T) {
	_, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	guard := &viewerReadGuard{ProjectComputerStore: svc.repo}
	svc.repo = guard
	cancelled := false
	token, detach, err := svc.RegisterViewer(t.Context(), project.ID, "user", "session", true, func() { cancelled = true })
	require.NoError(t, err)
	defer detach()
	require.Equal(t, 1, guard.reads)
	guard.forbidden = true
	writes := 0
	for range 100 {
		require.NoError(t, svc.ForwardViewerInput(project.ID, "session", token, func() error { writes++; return nil }))
	}
	require.Equal(t, 100, writes)
	require.Equal(t, 1, guard.reads, "input must not query the database")
	guard.forbidden = false
	require.NoError(t, svc.SetTaskEnabled(t.Context(), project.ID, false))
	require.True(t, cancelled)
	require.Error(t, svc.ForwardViewerInput(project.ID, "session", token, func() error { t.Fatal("revoked input forwarded"); return nil }))
	_, _, err = svc.RegisterViewer(t.Context(), project.ID, "user", "new", true)
	require.Error(t, err, "disabled desktop must reject reconnect")
	require.NoError(t, svc.SetTaskEnabled(t.Context(), project.ID, true))
	fresh, leave, err := svc.RegisterViewer(t.Context(), project.ID, "user", "session", true)
	require.NoError(t, err)
	defer leave()
	require.Error(t, svc.ForwardViewerInput(project.ID, "session", token, func() error { t.Fatal("old token forwarded"); return nil }))
	require.NoError(t, svc.ForwardViewerInput(project.ID, "session", fresh, func() error { return nil }))
}

func TestComputerViewerPrivateHandoffReconnect(t *testing.T) {
	_, svc, project, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	token, detach, err := svc.RegisterViewer(t.Context(), project.ID, "user", "session", true)
	require.NoError(t, err)
	defer detach()
	rt := svc.runtimeTools(t.Context(), project.ID, task.ID)
	_, _, _, err = rt.Executor(t.Context(), "request_computer_help", json.RawMessage(`{"reason":"Approve the browser prompt"}`))
	require.NoError(t, err)
	require.Error(t, svc.ForwardViewerInput(project.ID, "session", token, func() error { t.Fatal("old viewer forwarded input"); return nil }))
	// Handoffs pause the agent, not the human using the desktop.
	handoff, end, err := svc.RegisterViewer(t.Context(), project.ID, "user", "handoff", true)
	require.NoError(t, err)
	defer end()
	require.NoError(t, svc.ForwardViewerInput(project.ID, "handoff", handoff, func() error { return nil }))
	require.NoError(t, svc.CancelHandoff(t.Context(), project.ID))
	require.Error(t, svc.ForwardViewerInput(project.ID, "handoff", handoff, func() error { t.Fatal("old handoff input forwarded"); return nil }))
}

func TestComputerViewerProjectDisableRevokesAllTaskControllers(t *testing.T) {
	_, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	root := *svc
	root.taskID = ""
	root.repo = svc.projects.Computers()
	taskToken, leaveTask, err := svc.RegisterViewer(t.Context(), project.ID, "user", "task", true)
	require.NoError(t, err)
	defer leaveTask()
	rootToken, leaveRoot, err := root.RegisterViewer(t.Context(), project.ID, "user", "root", true)
	require.NoError(t, err)
	defer leaveRoot()
	_, leavePreview, err := svc.RegisterViewer(t.Context(), project.ID, "user", "preview", false)
	require.NoError(t, err)
	defer leavePreview()
	require.NoError(t, root.SetEnabled(t.Context(), project.ID, false))
	blocked := func() error { t.Fatal("disabled project forwarded input"); return nil }
	require.Error(t, svc.ForwardViewerInput(project.ID, "task", taskToken, blocked))
	require.Error(t, root.ForwardViewerInput(project.ID, "root", rootToken, blocked))
	_, exists := svc.active.Load(svc.viewerPrefix(project.ID) + "preview")
	require.True(t, exists, "revocation should not remove view-only registrations")
	_, _, err = svc.RegisterViewer(t.Context(), project.ID, "user", "retry", true)
	require.Error(t, err)
}

func TestComputerViewerRevocationWaitsForInflightInput(t *testing.T) {
	_, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	token, leave, err := svc.RegisterViewer(t.Context(), project.ID, "user", "session", true)
	require.NoError(t, err)
	defer leave()
	entered, release := make(chan struct{}), make(chan struct{})
	written, revoked := make(chan error, 1), make(chan error, 1)
	go func() {
		written <- svc.ForwardViewerInput(project.ID, "session", token, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	go func() { revoked <- svc.SetTaskEnabled(t.Context(), project.ID, false) }()
	select {
	case err := <-revoked:
		close(release)
		t.Fatalf("permission changed while input was still forwarding: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-written)
	require.NoError(t, <-revoked)
	require.Error(t, svc.ForwardViewerInput(project.ID, "session", token, func() error { t.Fatal("input after revocation"); return nil }))
}

func TestComputerViewerRevocationCancelsBlockedWrite(t *testing.T) {
	_, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	token, leave, err := svc.RegisterViewer(ctx, project.ID, "user", "session", true, cancel)
	require.NoError(t, err)
	defer leave()
	entered, done := make(chan struct{}), make(chan error, 1)
	go func() {
		done <- svc.ForwardViewerInput(project.ID, "session", token, func() error { close(entered); <-ctx.Done(); return ctx.Err() })
	}()
	<-entered
	revoked := make(chan error, 1)
	go func() { revoked <- svc.SetTaskEnabled(t.Context(), project.ID, false) }()
	select {
	case err := <-revoked:
		require.NoError(t, err)
	case <-time.After(time.Second):
		cancel()
		t.Fatal("revocation did not cancel the blocked network write")
	}
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestComputerDeletedTaskRevokesViewerBeforeFailedCleanup(t *testing.T) {
	db, svc, project, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	root := *svc
	root.taskID = ""
	root.repo = db.Projects.Computers()
	cancelled := false
	token, leave, err := svc.RegisterViewer(t.Context(), project.ID, "user", "session", true, func() { cancelled = true })
	require.NoError(t, err)
	defer leave()
	require.NoError(t, db.Tasks.Delete(t.Context(), task.ID))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, root.CloseTaskDesktop(ctx, project.ID, task.ID), context.Canceled)
	require.True(t, cancelled, "cleanup failure must not leave the connection open")
	require.Error(t, svc.ForwardViewerInput(project.ID, "session", token, func() error {
		t.Fatal("deleted task forwarded input after cleanup failed")
		return nil
	}))
	_, _, err = svc.RegisterViewer(t.Context(), project.ID, "user", "retry", true)
	require.Error(t, err, "deleted task must reject reconnect")
}
