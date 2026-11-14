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

func TestComputerViewerDisconnectAndAutomaticHandback(t *testing.T) {
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
			conn.CloseNow()
		case <-ctx.Done():
		}
	}))
	defer backend.Close()
	project := &models.Project{Name: "Viewer disconnect"}
	require.NoError(t, db.Projects.Create(ctx, project))
	svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: backend.URL, Token: "test"}})
	require.NoError(t, db.Projects.Computers().Configure(ctx, project.ID, "local"))
	require.NoError(t, db.Projects.Computers().Hold(ctx, project.ID, true))
	h := &Handler{projectRepo: db.Projects, computerSvc: svc}
	e := echo.New()
	e.GET("/projects/:id/stream/:mode", h.ProjectComputerStream)
	server := httptest.NewServer(e)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/projects/"+project.ID+"/stream/control", &websocket.DialOptions{Subprotocols: []string{"binary"}})
	require.NoError(t, err)
	defer conn.CloseNow()
	close(disconnect)
	_, _, err = conn.Read(ctx)
	require.Error(t, err)
	require.Eventually(t, func() bool { state, err := svc.Get(ctx, project.ID); return err == nil && !state.Held }, 5*time.Second, 20*time.Millisecond)
}
