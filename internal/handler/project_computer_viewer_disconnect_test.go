package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerViewerDisconnectPreservesPrivateHandoff(t *testing.T) {
	for _, kind := range []string{"request_browser_login", "request_computer_help"} {
		t.Run(kind, func(t *testing.T) {
			db := teststores.New(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			disconnect := make(chan struct{})
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-OV-Viewer-ID") == "" {
					http.Error(w, "missing policy", 400)
					return
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				select {
				case <-disconnect:
				case <-ctx.Done():
				}
			}))
			defer backend.Close()
			project := &models.Project{Name: "Viewer disconnect"}
			require.NoError(t, db.Projects.Create(ctx, project))
			task := &models.Task{ProjectID: project.ID, Title: "Private handoff", Category: models.CategoryActive, Status: models.StatusPending, Priority: 2}
			require.NoError(t, db.Tasks.Create(ctx, task))
			svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: backend.URL, Token: "test"}})
			require.NoError(t, svc.SetEnabled(ctx, project.ID, true))
			scoped, err := svc.ForTask(ctx, project.ID, task.ID)
			require.NoError(t, err)
			require.NoError(t, scoped.SetTaskEnabled(ctx, project.ID, true))
			repo, err := db.Projects.Computers().ForTask(ctx, project.ID, task.ID)
			require.NoError(t, err)
			require.NoError(t, repo.RequestHandoff(ctx, project.ID, kind, "https://example.test", "Private request", ""))
			h := &Handler{projectRepo: db.Projects, taskRepo: db.Tasks, computerSvc: svc}
			e := echo.New()
			finished := make(chan struct{})
			e.GET("/projects/:id/stream/:mode", func(c echo.Context) error {
				defer close(finished)
				return h.ProjectComputerStream(c)
			})
			server := httptest.NewServer(e)
			defer server.Close()
			conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/projects/"+project.ID+"/stream/control?task_id="+task.ID, &websocket.DialOptions{Subprotocols: []string{"binary"}})
			require.NoError(t, err)
			defer conn.CloseNow()
			close(disconnect)
			_, _, err = conn.Read(ctx)
			require.Error(t, err)
			select {
			case <-finished:
			case <-ctx.Done():
				t.Fatal("stream handler did not finish")
			}
			state, err := scoped.Get(ctx, project.ID)
			require.NoError(t, err)
			require.True(t, state.Held)
			require.Equal(t, kind, state.HandoffKind)
			inputs, err := db.ThreadInputs.ListPendingForTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, inputs, "disconnect must not queue task continuation")
		})
	}
}
