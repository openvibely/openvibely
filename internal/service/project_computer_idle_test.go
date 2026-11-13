package service

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestComputerIdlePolicyProtectsWorkAndHandoffs(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Idle policy"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	var candidates []string
	seconds, calls := 0, 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/desktop-reap", r.URL.Path)
		var input struct {
			Candidates []string `json:"candidates"`
			Seconds    int      `json:"idle_seconds"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
		candidates = input.Candidates
		seconds = input.Seconds
		calls++
		w.Write([]byte(`[]`))
	}))
	defer backend.Close()
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: backend.URL}})
	require.NoError(t, svc.repo.Configure(t.Context(), project.ID, "local"))
	state, err := svc.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, 30, state.IdleMinutes)
	eligible := []string{}
	var unlocks []func()
	for _, kind := range []string{"completed", "pending", "running", "queued", "held", "handoff", "takeover", "lease", "operation", "control"} {
		status := models.StatusPending
		if kind == "running" {
			status = models.StatusRunning
		}
		if kind == "queued" {
			status = models.StatusQueued
		}
		if kind == "completed" {
			status = models.StatusCompleted
		}
		task := &models.Task{ProjectID: project.ID, Title: kind, Status: status, Category: models.CategoryActive, Priority: 2}
		require.NoError(t, db.Tasks.Create(t.Context(), task))
		scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
		require.NoError(t, err)
		switch kind {
		case "operation":
			lock := scoped.lock(project.ID)
			lock.Lock()
			unlocks = append(unlocks, lock.Unlock)
		case "control":
			value, _ := svc.locks.LoadOrStore("control:"+project.ID+":"+task.ID, &sync.Mutex{})
			lock := value.(*sync.Mutex)
			lock.Lock()
			unlocks = append(unlocks, lock.Unlock)
		case "completed", "pending":
			eligible = append(eligible, ComputerDesktopID(task.ID))
		case "held":
			require.NoError(t, scoped.repo.Hold(t.Context(), project.ID, true))
		case "handoff":
			require.NoError(t, scoped.repo.RequestHandoff(t.Context(), project.ID, "request_computer_help", "", "help", ""))
		case "takeover":
			require.NoError(t, scoped.repo.SetTakeoverPending(t.Context(), project.ID, true))
		case "lease":
			require.NoError(t, scoped.repo.Reserve(t.Context(), project.ID, "agent"))
		}
	}
	require.NoError(t, svc.reapIdleDesktops(t.Context(), project.ID))
	require.ElementsMatch(t, eligible, candidates)
	require.Equal(t, 1800, seconds)
	for _, unlock := range unlocks {
		unlock()
	}
	require.NoError(t, svc.SetIdleMinutes(t.Context(), project.ID, 0))
	require.NoError(t, svc.reapIdleDesktops(t.Context(), project.ID))
	require.Equal(t, 1, calls, "Never must not reap")
	require.Error(t, svc.SetIdleMinutes(t.Context(), project.ID, -1))
	require.Error(t, svc.SetIdleMinutes(t.Context(), project.ID, 10081))
	require.NoError(t, svc.SetIdleMinutes(t.Context(), project.ID, 15))
	require.NoError(t, svc.reapIdleDesktops(t.Context(), project.ID))
	require.Equal(t, 900, seconds)
}

func TestComputerIdleMaintenanceRestoresAndRetries(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "existing desktop"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	calls := make(chan struct{}, 16)
	var count atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			http.Error(w, "temporary failure", 503)
		} else {
			w.Write([]byte(`[]`))
		}
		calls <- struct{}{}
	}))
	defer backend.Close()
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: backend.URL}})
	require.NoError(t, svc.repo.Configure(t.Context(), project.ID, "local"))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); svc.runIdleMaintenance(ctx, 20*time.Millisecond) }()
	// Neither starting nor connecting to a desktop is necessary after app startup.
	for i := 0; i < 2; i++ {
		select {
		case <-calls:
		case <-time.After(5 * time.Second):
			t.Fatal("maintenance did not start or retry")
		}
	}
	cancel()
	<-done
	require.Eventually(t, func() bool { _, busy := svc.active.Load("idle-maintenance:" + project.ID); return !busy }, time.Second, time.Millisecond)
}
