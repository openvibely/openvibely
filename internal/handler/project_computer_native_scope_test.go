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
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestComputerNativeStreamPreservesTicketScopeWithCachedQuery(t *testing.T) {
	for _, query := range []string{"", "?task_id=untrusted-task&viewer_id=untrusted-viewer"} {
		t.Run(query, func(t *testing.T) {
			h, _, _ := setupTestHandler(t)
			project := createProject(t, h, "Native scope")
			task := createTask(t, h, project.ID, "Desktop")
			paths := make(chan string, 1)
			bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths <- r.URL.Path
				if r.URL.Path != "/desktops/"+service.ComputerDesktopID(task.ID)+"/rfb-view" {
					http.NotFound(w, r)
					return
				}
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				_ = conn.Write(r.Context(), websocket.MessageBinary, []byte("RFB 003.008\n"))
				_, _, _ = conn.Read(r.Context())
			}))
			defer bridge.Close()
			h.computerSvc = service.NewProjectComputerService(h.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL, Token: "fixture"}})
			require.NoError(t, h.projectRepo.Computers().Configure(t.Context(), project.ID, "local"))
			h.SetDesktopMode(true)
			e := echo.New()
			e.Use(h.AuthMiddleware())
			// Production workspace middleware parses and caches query parameters
			// before the native handler applies its authenticated ticket scope.
			e.Use(h.allProjectsScope)
			e.GET("/computer-stream", h.ProjectComputerNativeStream)
			server := httptest.NewServer(e)
			defer server.Close()
			h.SetComputerDesktopBackendURL(server.URL)
			ticket, err := h.issueComputerTicket(t.Context(), project.ID, "view", task.ID, "native-viewer-12345")
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/computer-stream"+query, &websocket.DialOptions{Subprotocols: []string{"binary", ticket}})
			require.NoError(t, err)
			defer conn.CloseNow()
			_, data, err := conn.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, "RFB 003.008\n", string(data))
			require.Equal(t, "/desktops/"+service.ComputerDesktopID(task.ID)+"/rfb-view", <-paths)
		})
	}
}
