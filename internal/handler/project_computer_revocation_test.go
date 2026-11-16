package handler

import (
	"context"
	"github.com/coder/websocket"
	"github.com/labstack/echo/v4"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestComputerStreamAccessRevocation(t *testing.T) {
	for _, role := range []string{"", "viewer"} {
		t.Run("new-role-"+role, func(t *testing.T) {
			tc, _ := organizationTestApp(t, true)
			access := service.NewProjectAccessService(tc.stores.ProjectAccess)
			tc.handler.SetProjectAccessService(access)
			ctx := t.Context()
			owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
			invite, err := tc.handler.organizationSvc.Invite(ctx, models.BootstrapUserID, "member")
			require.NoError(t, err)
			bob, err := tc.handler.organizationSvc.Accept(ctx, invite, "bob", "bob-password-long", "")
			require.NoError(t, err)
			project := &models.Project{Name: "Computer access"}
			require.NoError(t, tc.stores.Projects.Create(ctx, project))
			policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
			require.NoError(t, err)
			policy.Visibility = "private"
			require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
			require.NoError(t, access.SetMember(owner, project.ID, bob.UserID, "editor"))
			bridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
				if err != nil {
					return
				}
				defer conn.CloseNow()
				for {
					kind, data, err := conn.Read(r.Context())
					if err != nil {
						return
					}
					if conn.Write(r.Context(), kind, data) != nil {
						return
					}
				}
			}))
			defer bridge.Close()
			tc.handler.computerSvc = service.NewProjectComputerService(tc.stores.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: bridge.URL, Token: "fixture"}})
			require.NoError(t, tc.stores.Projects.Computers().Configure(ctx, project.ID, "local"))
			if err := tc.stores.Projects.Computers().SetEnabled(ctx, project.ID, true); err != nil {
				t.Fatal(err)
			}
			bobCtx := models.WithPrincipal(ctx, bob.Principal)
			e := echo.New()
			e.GET("/projects/:id/stream/:mode", func(c echo.Context) error {
				c.SetRequest(c.Request().WithContext(models.WithPrincipal(c.Request().Context(), bob.Principal)))
				return tc.handler.ProjectComputerStream(c)
			})
			tc.handler.SetDesktopMode(true)
			e.GET("/native", func(c echo.Context) error {
				if !tc.handler.consumeComputerTicket(c) {
					return echo.NewHTTPError(403)
				}
				return tc.handler.ProjectComputerNativeStream(c)
			})
			server := httptest.NewServer(e)
			defer server.Close()
			tc.handler.SetComputerDesktopBackendURL(server.URL)
			token, err := tc.handler.issueComputerTicket(bobCtx, project.ID, "view")
			require.NoError(t, err)
			sockets := []*websocket.Conn{}
			for _, mode := range []string{"view", "control", "native"} {
				url := strings.Replace(server.URL, "http", "ws", 1) + "/projects/" + project.ID + "/stream/" + mode
				protocols := []string{"binary"}
				if mode == "native" {
					url = strings.Replace(server.URL, "http", "ws", 1) + "/native"
					protocols = append(protocols, token)
				}
				conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: protocols})
				require.NoError(t, err)
				defer conn.CloseNow()
				require.NoError(t, conn.Write(ctx, websocket.MessageBinary, []byte("frame")))
				_, frame, err := conn.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "frame", string(frame))
				sockets = append(sockets, conn)
			}
			stream, cancel, err := tc.handler.computerStreamAccess(bobCtx, project.ID)
			require.NoError(t, err)
			defer cancel()
			ownerStream, ownerCancel, err := tc.handler.computerStreamAccess(owner, project.ID)
			require.NoError(t, err)
			defer ownerCancel()
			require.NoError(t, access.SetMember(owner, project.ID, bob.UserID, "editor"))
			require.NoError(t, access.SetMember(owner, project.ID, bob.UserID, role))
			select {
			case <-stream.Done():
			case <-time.After(5 * time.Second):
				t.Fatal("revoked computer stream remained open")
			}
			for _, conn := range sockets {
				readCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				_, _, err := conn.Read(readCtx)
				require.Error(t, err)
				require.NoError(t, readCtx.Err(), "server must disconnect, not wait for read timeout")
				stop()
			}
			require.NoError(t, ownerStream.Err(), "other users must retain their connections")
			_, deniedCancel, err := tc.handler.computerStreamAccess(bobCtx, project.ID)
			defer deniedCancel()
			require.Error(t, err, "reconnect must not restore revoked access")
			require.ErrorIs(t, stream.Err(), context.Canceled)
		})
	}
}
