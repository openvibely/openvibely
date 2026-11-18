package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerTimeZoneReachesDockerAndReplacesChangedContainer(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "Desktop zone"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"isolation":1,"viewer_connections":1}`))
	}))
	defer server.Close()
	port := strings.TrimPrefix(server.URL, "http://127.0.0.1:")
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "timezone-fixture")
	exists := false
	zone := ""
	isolation := "1"
	runs, removes, updates := 0, 0, 0
	var mount string
	p := &LocalComputerProvisioner{run: func(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
		switch args[0] {
		case "inspect":
			if !exists {
				return nil, errors.New("not found")
			}
			return json.Marshal([]any{map[string]any{"Config": map[string]any{"Labels": map[string]string{"openvibely.project": project.ID, "openvibely.computer-isolation": isolation}, "Image": "timezone-fixture", "Env": []string{"TZ=" + zone, "OPENVIBELY_COMPUTER_TOKEN=fixture"}}, "State": map[string]bool{"Running": true}, "NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": port}}}}}})
		case "update":
			require.Equal(t, []string{"update", "--pids-limit", "4096", computerContainerName(project.ID)}, args)
			updates++
		case "run":
			require.Contains(t, strings.Join(args, " "), "--pids-limit 4096")
			require.Contains(t, strings.Join(args, " "), "--memory 4g")
			runs++
			isolation = "1"
			exists = true
			for i, arg := range args {
				if arg == "--env" && strings.HasPrefix(args[i+1], "TZ=") {
					zone = strings.TrimPrefix(args[i+1], "TZ=")
				}
				if arg == "--mount" {
					if mount != "" {
						require.Equal(t, mount, args[i+1])
					}
					mount = args[i+1]
				}
			}
		case "container":
			return []byte(computerContainerName(project.ID)), nil
		case "rm":
			removes++
			exists = false
		}
		return nil, nil
	}}
	svc := NewProjectComputerService(db.Projects, p)
	require.NoError(t, svc.SetTimeZone(t.Context(), project.ID, "America/New_York"))
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, svc.Start(t.Context(), project.ID))
	require.Equal(t, "America/New_York", zone)
	require.Equal(t, 1, runs)
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, svc.Start(t.Context(), project.ID))
	require.Equal(t, 1, runs, "unchanged zone should reuse container")
	require.Equal(t, 1, updates, "reused containers should receive the current process limit")
	require.NoError(t, svc.SetTimeZone(t.Context(), project.ID, "Asia/Kathmandu"))
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, svc.Start(t.Context(), project.ID))
	require.Equal(t, "Asia/Kathmandu", zone)
	require.Equal(t, 2, runs)
	require.Equal(t, 1, removes)
	isolation = "" // Older containers must be replaced even with the same image tag.
	if err := svc.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, svc.Start(t.Context(), project.ID))
	require.Equal(t, 3, runs)
	require.Equal(t, 2, removes)
	for _, bad := range []string{"", "Local", "Not/AZone"} {
		require.Error(t, svc.SetTimeZone(t.Context(), project.ID, bad))
	}
	task := &models.Task{ProjectID: project.ID, Title: "Zone inheritance", Prompt: "test", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	if err := scoped.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	state, err := scoped.Get(t.Context(), project.ID)
	require.NoError(t, err)
	require.Equal(t, "Asia/Kathmandu", state.TimeZone)
}
