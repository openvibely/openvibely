package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerResourceSettings(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Resource settings"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	running := false
	failure := false
	p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		if failure {
			return nil, errors.New("Docker unavailable")
		}
		switch args[0] {
		case "container":
			return []byte(computerContainerName(project.ID)), nil
		case "inspect":
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": map[string]string{"openvibely.project": project.ID}}, "State": map[string]bool{"Running": running}}})
		default:
			t.Fatalf("unexpected Docker mutation: %v", args)
			return nil, nil
		}
	}}
	svc := NewProjectComputerService(db.Projects, p)
	defaults, err := svc.ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, models.DefaultComputerResources(), defaults)
	for _, bad := range []models.ComputerResources{{CPUs: 0, MemoryGiB: 4, PidsLimit: 4096}, {CPUs: 2, MemoryGiB: 0, PidsLimit: 4096}, {CPUs: 2, MemoryGiB: 4, PidsLimit: -1}, {CPUs: 65, MemoryGiB: 4, PidsLimit: 4096}} {
		require.Error(t, svc.SetResourceSettings(t.Context(), project.ID, bad))
	}
	custom := models.ComputerResources{CPUs: 3, MemoryGiB: 6, PidsLimit: 8192}
	require.NoError(t, svc.SetResourceSettings(t.Context(), project.ID, custom))
	saved, err := svc.ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, custom, saved)
	// A fresh service reads the durable per-project settings.
	saved, err = NewProjectComputerService(db.Projects, p).ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, custom, saved)
	running = true
	require.ErrorContains(t, svc.SetResourceSettings(t.Context(), project.ID, defaults), "Stop the project computer")
	custom.PidsLimit = 4096
	require.NoError(t, svc.SetResourceSettings(t.Context(), project.ID, custom))
	failure = true
	require.ErrorContains(t, svc.SetResourceSettings(t.Context(), project.ID, defaults), "Docker unavailable")
	saved, err = svc.ResourceSettings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, custom, saved)
}

func TestComputerProvisionerUsesResourceSettings(t *testing.T) {
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "resource-fixture")
	resources := models.ComputerResources{CPUs: 3, MemoryGiB: 6, PidsLimit: 8192}
	created := false
	updates := 0
	p := &LocalComputerProvisioner{resources: func(context.Context, string) (models.ComputerResources, error) { return resources, nil }}
	p.run = func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			if !created {
				return nil, errors.New("not found")
			}
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Image": "resource-fixture", "Labels": map[string]string{"openvibely.project": "project", "openvibely.computer-isolation": "1"}, "Env": []string{"OPENVIBELY_COMPUTER_TOKEN=fixture", "TZ=UTC", "OPENVIBELY_COMPUTER_CPUS=3", "OPENVIBELY_COMPUTER_MEMORY_GIB=6"}}, "State": map[string]bool{"Running": true}, "HostConfig": map[string]int{"PidsLimit": 4096}, "NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": "1234"}}}}}})
		case "run":
			created = true
			command := strings.Join(args, " ")
			for _, flag := range []string{"--memory 6g", "--cpus 3", "--pids-limit 8192", "OPENVIBELY_COMPUTER_CPUS=3", "OPENVIBELY_COMPUTER_MEMORY_GIB=6"} {
				require.Contains(t, command, flag)
			}
		case "update":
			require.Equal(t, []string{"update", "--pids-limit", "8192", computerContainerName("project")}, args)
			updates++
		default:
			t.Fatalf("unexpected command: %v", args)
		}
		return nil, nil
	}
	_, err := p.Start(t.Context(), "project")
	require.NoError(t, err)
	_, err = p.ResolveCurrent(t.Context(), "project")
	require.NoError(t, err)
	require.Equal(t, 1, updates)
}

func TestComputerSettingsSaveTogether(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Settings save"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	running := true
	exists := false
	p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "container":
			if !exists {
				return nil, nil
			}
			return []byte(computerContainerName(project.ID)), nil
		case "inspect":
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": map[string]string{"openvibely.project": project.ID}}, "State": map[string]bool{"Running": running}}})
		default:
			return nil, errors.New("unexpected Docker command")
		}
	}}
	svc := NewProjectComputerService(db.Projects, p)
	initial, err := svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.False(t, initial.Enabled)
	require.Equal(t, "balanced", initial.PreviewQuality)
	initial.PreviewQuality = "high"
	initial.Enabled = true
	initial.IdleMinutes = 60
	initial.TimeZone = "Europe/London"
	initial.Resources.PidsLimit = 8192
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, initial))
	saved, err := svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, initial, saved)
	task := &models.Task{ProjectID: project.ID, Title: "Quality viewer", Status: models.StatusPending, Category: models.CategoryActive, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	state, err := scoped.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, "high", state.PreviewQuality, "task viewers inherit the project quality")
	exists = true
	bad := initial
	bad.PreviewQuality = "invalid"
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, bad), "preview quality")
	bad = initial
	bad.Resources.CPUs = 3
	bad.IdleMinutes = 120
	require.NoError(t, db.Projects.Computers().Reserve(t.Context(), project.ID, "agent"))
	require.Error(t, svc.SaveSettings(t.Context(), project.ID, bad))
	require.NoError(t, db.Projects.Computers().Release(t.Context(), project.ID, "agent"))
	saved, err = svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, initial, saved, "failed CPU change must not persist idle changes")
	bad = initial
	bad.TimeZone = "Invalid/Zone"
	bad.Resources.PidsLimit = 4096
	require.Error(t, svc.SaveSettings(t.Context(), project.ID, bad))
	saved, err = svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, initial, saved)
	require.NoError(t, db.Projects.Computers().Reserve(t.Context(), project.ID, "agent"))
	bad = initial
	bad.Enabled = false
	bad.IdleMinutes = 15
	require.Error(t, svc.SaveSettings(t.Context(), project.ID, bad))
	saved, err = svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, initial, saved, "busy disable must not persist other fields")
	require.NoError(t, db.Projects.Computers().Release(t.Context(), project.ID, "agent"))
	running = false
	exists = false
	initial.Enabled = false
	initial.Resources.CPUs = 3
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, initial))
	saved, err = svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, initial, saved)
}

func TestComputerSettingsApplyOnSave(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Immediate settings"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"isolation":1,"viewer_connections":1}`))
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "settings-fixture")
	env := []string{"TZ=UTC", "OPENVIBELY_COMPUTER_TOKEN=fixture"}
	runs, updates := 0, 0
	cpus, memory, pids := int64(2e9), int64(4<<30), int64(4096)
	failRemove := true
	failUpdate := false
	ignoreUpdate := false
	running := true
	exists := true
	failRun := false
	failStart := false
	starts := 0
	p := &LocalComputerProvisioner{run: func(_ context.Context, _ io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "container":
			if !exists {
				return nil, nil
			}
			return []byte(computerContainerName(project.ID)), nil
		case "inspect":
			if !exists {
				return nil, errors.New("not found")
			}
			return json.Marshal([]any{map[string]any{
				"Config":          map[string]any{"Labels": map[string]string{"openvibely.project": project.ID, "openvibely.computer-isolation": "1"}, "Image": "settings-fixture", "Env": env},
				"State":           map[string]bool{"Running": running},
				"HostConfig":      map[string]int64{"NanoCpus": cpus, "Memory": memory, "PidsLimit": pids},
				"NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": port}}}},
			}})
		case "stop":
			running = false
		case "rm":
			if failRemove {
				return nil, errors.New("temporary Docker error")
			}
			exists = false
		case "run":
			if failRun {
				return nil, errors.New("creation failed")
			}
			exists = true
			running = true
			cpus = 3e9
			memory = 6 << 30
			pids = 4096
			runs++
			env = nil
			for i, arg := range args {
				if arg == "--env" {
					env = append(env, args[i+1])
				}
				if arg == "--pids-limit" {
					parsed, _ := strconv.ParseInt(args[i+1], 10, 64)
					pids = parsed
				}
			}
			if failStart {
				running = false
				return nil, errors.New("start failed after creation")
			}
		case "start":
			starts++
			if failStart {
				return nil, errors.New("start failed")
			}
			running = true
		case "update":
			updates++
			if failUpdate {
				return nil, errors.New("temporary update error")
			}
			if !ignoreUpdate {
				pids = 8192
			}
			require.Contains(t, strings.Join(args, " "), "--pids-limit 8192")
		}
		return nil, nil
	}}
	svc := NewProjectComputerService(db.Projects, p)
	require.NoError(t, svc.SetEnabled(t.Context(), project.ID, true))
	settings, err := svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	settings.Resources.CPUs = 3
	settings.Resources.MemoryGiB = 6
	settings.TimeZone = "Europe/London"
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, settings), "could not restart")
	failRemove = false
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
	require.Equal(t, 1, runs)
	require.Contains(t, env, "OPENVIBELY_COMPUTER_CPUS=3")
	require.Contains(t, env, "OPENVIBELY_COMPUTER_MEMORY_GIB=6")
	require.Contains(t, env, "TZ=Europe/London")
	settings.Resources.PidsLimit = 8192
	failUpdate = true
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, settings), "could not be applied")
	failUpdate = false
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
	require.Equal(t, 2, updates)
	require.Equal(t, 1, runs, "process limit must not restart")
	settings.PreviewQuality = "low"
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
	require.Equal(t, 2, updates)
	require.Equal(t, 1, runs, "quality must not restart")
	pids = 4096
	ignoreUpdate = true
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, settings), "does not match")
	ignoreUpdate = false
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
	require.Equal(t, int64(8192), pids)
	require.True(t, running, "retry after failed restart restores running computer")

	cpus = 2e9
	failRun = true
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, settings), "creation failed")
	require.False(t, exists)
	failRun = false
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
	require.True(t, exists)
	require.True(t, running)

	// Docker run can create the correctly configured container but fail to start it.
	cpus = 2e9
	failStart = true
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, settings), "start failed after creation")
	require.True(t, exists)
	require.False(t, running)
	createdRuns := runs
	require.ErrorContains(t, svc.SaveSettings(t.Context(), project.ID, settings), "start failed")
	require.False(t, running)
	failStart = false
	require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
	require.True(t, running, "retry must start the correctly configured container")
	require.Equal(t, createdRuns, runs, "retry must reuse the existing container")
	require.Equal(t, 2, starts)

	running = false
	settings.Enabled = false
	settings.TimeZone = "Europe/Berlin"
	beforeRuns, beforeUpdates := runs, updates
	for i := 0; i < 2; i++ {
		require.NoError(t, svc.SaveSettings(t.Context(), project.ID, settings))
		require.False(t, running, "disabled computer must stay stopped on repeated saves")
		require.Equal(t, beforeRuns, runs)
		require.Equal(t, beforeUpdates, updates)
	}
	saved, err := svc.Settings(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, settings, saved)
	require.Contains(t, env, "TZ=Europe/London", "disabled changes are saved without starting the computer")
	require.NoError(t, svc.SetEnabled(t.Context(), project.ID, true))
	require.NoError(t, svc.Start(t.Context(), project.ID))
	require.True(t, running)
	require.Contains(t, env, "TZ=Europe/Berlin", "normal startup applies saved changes")

}
