package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerStatusEventsScopeAndHandoffs(t *testing.T) {
	_, svc, project, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{}`) })
	b := events.NewBroadcaster()
	svc.SetBroadcaster(b)
	updates, err := b.SubscribeProject(project.ID)
	require.NoError(t, err)
	defer b.Unsubscribe(updates)
	foreign, err := b.SubscribeProject("unrelated")
	require.NoError(t, err)
	defer b.Unsubscribe(foreign)
	expect := func(taskID string) {
		t.Helper()
		select {
		case ev := <-updates:
			require.Equal(t, events.ComputerStatusChanged, ev.Type)
			require.Equal(t, project.ID, ev.ProjectID)
			require.Equal(t, taskID, ev.TaskID)
			require.Empty(t, ev.Message, "private handoff details must not enter task notifications")
		case <-time.After(time.Second):
			t.Fatal("missing status event")
		}
		for len(updates) > 0 {
			<-updates
		}
		select {
		case <-foreign:
			t.Fatal("cross-project status event")
		default:
		}
	}
	require.NoError(t, svc.SetEnabled(t.Context(), project.ID, true))
	expect("")
	// ForTask must inherit the broadcaster, including agent-requested handoffs.
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetToolTarget(t.Context(), project.ID, "computer"))
	expect(task.ID)
	rt := scoped.runtimeTools(t.Context(), project.ID, task.ID)
	_, _, _, err = rt.Executor(t.Context(), "request_computer_help", json.RawMessage(`{"reason":"Private help text"}`))
	require.NoError(t, err)
	expect(task.ID)
	require.NoError(t, scoped.CancelHandoff(t.Context(), project.ID))
	expect(task.ID)
	_, _, _, err = rt.Executor(t.Context(), "release_computer", json.RawMessage(`{}`))
	require.NoError(t, err)
	expect(task.ID)
	require.NoError(t, scoped.Control(t.Context(), project.ID, true, nil))
	expect(task.ID)
	require.NoError(t, scoped.Control(t.Context(), project.ID, false, nil))
	expect(task.ID)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, false))
	expect(task.ID)
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	expect(task.ID)
}

func TestComputerReadyUsesSingleInspectionAndPreservesImageChecks(t *testing.T) {
	db := teststores.New(t)
	project := &models.Project{Name: "single inspection"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"isolation":1,"viewer_connections":1}`)
	}))
	defer backend.Close()
	port := strings.TrimPrefix(backend.URL, "http://127.0.0.1:")
	t.Setenv("OPENVIBELY_COMPUTER_IMAGE", "current-image")
	image, isolation, inspections := "current-image", "1", 0
	p := &LocalComputerProvisioner{run: func(ctx context.Context, in io.Reader, args ...string) ([]byte, error) {
		require.Equal(t, []string{"inspect", computerContainerName(project.ID)}, args)
		inspections++
		return json.Marshal([]any{map[string]any{
			"Config":          map[string]any{"Image": image, "Labels": map[string]string{"openvibely.project": project.ID, "openvibely.computer-isolation": isolation}, "Env": []string{"OPENVIBELY_COMPUTER_TOKEN=fixture"}},
			"State":           map[string]bool{"Running": true},
			"NetworkSettings": map[string]any{"Ports": map[string]any{"8090/tcp": []map[string]string{{"HostIP": "127.0.0.1", "HostPort": port}}}},
		}})
	}}
	svc := NewProjectComputerService(db.Projects, p)
	require.NoError(t, svc.SetEnabled(t.Context(), project.ID, true))
	require.True(t, svc.Ready(t.Context(), project.ID))
	require.Equal(t, 1, inspections)
	image = "old-image"
	inspections = 0
	require.False(t, svc.Ready(t.Context(), project.ID))
	require.Equal(t, 1, inspections)
	_, err := svc.Connection(t.Context(), project.ID)
	require.NoError(t, err, "cleanup must still resolve old containers")
	image = "current-image"
	isolation = ""
	inspections = 0
	require.False(t, svc.Ready(t.Context(), project.ID))
	require.Equal(t, 1, inspections)
}
