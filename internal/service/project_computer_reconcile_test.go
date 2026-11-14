package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type cleanupProjectStore struct{ persistence.ProjectStore }

func (s cleanupProjectStore) Computers() persistence.ProjectComputerStore {
	return failingCleanupStore{s.ProjectStore.Computers()}
}

type failingCleanupStore struct {
	persistence.ProjectComputerStore
}

func (s failingCleanupStore) TaskIDsForDesktopCleanup(context.Context, string) ([]string, error) {
	return nil, errors.New("database unavailable")
}

func TestDeletedDesktopReconciliationRetriesAfterRestart(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "reconciliation"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	active := &models.Task{ProjectID: project.ID, Title: "Keep", Category: models.CategoryActive, Status: models.StatusRunning, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), active))
	// No session row is required to protect an existing task.
	orphan := ComputerDesktopID("already-deleted-task")
	var mu sync.Mutex
	running := map[string]bool{orphan: true, ComputerDesktopID(active.ID): true, "task-../../bad": true}
	attempts := 0
	fail := true
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		require.Equal(t, "Bearer secret", r.Header.Get("Authorization"))
		if r.URL.Path == "/desktop-sessions" {
			json.NewEncoder(w).Encode(running)
			return
		}
		if r.Method != "DELETE" || r.URL.Path != "/desktops/"+orphan+"/" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", 500)
			return
		}
		attempts++
		if fail {
			http.Error(w, "temporary failure", 503)
			return
		}
		delete(running, orphan)
		w.WriteHeader(204)
	}))
	defer backend.Close()
	provider := &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: backend.URL, Token: "secret"}}
	svc := NewProjectComputerService(db.Projects, provider)
	require.NoError(t, svc.repo.Configure(t.Context(), project.ID, "local"))
	if err := svc.repo.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, svc.repo.SetIdleMinutes(t.Context(), project.ID, 0))
	require.NoError(t, svc.repo.SetEnabled(t.Context(), project.ID, false))
	// A database failure must not be treated as an empty task list.
	broken := NewProjectComputerService(cleanupProjectStore{db.Projects}, provider)
	require.ErrorContains(t, broken.reconcileDeletedDesktops(t.Context(), project.ID), "database unavailable")
	mu.Lock()
	require.Zero(t, attempts)
	mu.Unlock()
	require.Error(t, svc.reconcileDeletedDesktops(t.Context(), project.ID))
	mu.Lock()
	require.Equal(t, 1, attempts)
	require.True(t, running[orphan])
	fail = false
	mu.Unlock()
	// A fresh service has no remembered deletion queue. It still rediscovers it.
	restarted := NewProjectComputerService(db.Projects, provider)
	require.NoError(t, restarted.reconcileDeletedDesktops(t.Context(), project.ID))
	require.NoError(t, restarted.reconcileDeletedDesktops(t.Context(), project.ID))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, attempts)
	require.True(t, running[ComputerDesktopID(active.ID)])
	require.NotContains(t, running, orphan)
}

func TestDeletedDesktopReconciliationMaintenanceStartup(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "startup"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	stopped := make(chan struct{}, 1)
	name := ComputerDesktopID("deleted")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/desktop-sessions" {
			json.NewEncoder(w).Encode(map[string]bool{name: true})
			return
		}
		if r.Method == "DELETE" && strings.Contains(r.URL.Path, name) {
			stopped <- struct{}{}
			w.WriteHeader(204)
			return
		}
		t.Errorf("unexpected request %s", r.URL.Path)
		http.Error(w, "unexpected", 500)
	}))
	defer backend.Close()
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: backend.URL}})
	require.NoError(t, svc.repo.Configure(t.Context(), project.ID, "local"))
	if err := svc.repo.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, svc.repo.SetIdleMinutes(t.Context(), project.ID, 0))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	svc.StartIdleMaintenance(ctx)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("no startup cleanup")
	}
	cancel()
	require.Eventually(t, func() bool { _, busy := svc.active.Load("idle-maintenance:" + project.ID); return !busy }, time.Second, time.Millisecond)
}
