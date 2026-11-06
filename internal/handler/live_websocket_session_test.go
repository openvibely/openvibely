package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestLiveWebSocketIdleSessionLogoutStopsStatusDelivery(t *testing.T) {
	tc, e := organizationTestApp(t, true)
	token, err := tc.handler.organizationSvc.NewSession(t.Context(), models.BootstrapUserID, time.Hour)
	require.NoError(t, err)
	server := httptest.NewServer(e)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var frame liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &frame))
	require.Equal(t, "ready", frame.Event)
	// No subscriptions or commands: logout must stop the connection-wide status
	// frame too, without waiting for the old fifteen-second ping interval.
	require.NoError(t, tc.handler.organizationSvc.Logout(t.Context(), token))
	require.Error(t, wsjson.Read(ctx, conn, &frame), "revoked session received %s", frame.Event)
	require.NoError(t, ctx.Err(), "server must close before the read deadline")
}

func TestLiveWebSocketIdleLegacySessionExpiryClosesConnection(t *testing.T) {
	tc := NewStoreTestContext(t)
	cfg := auth.Config{Enabled: true, Username: "alice", Password: "password", SessionSecret: "websocket-expiry-test", SessionTTL: 2 * time.Second}.Normalized()
	tc.handler.SetAuthConfig(cfg)
	require.Nil(t, tc.handler.organizationSvc, "exercise the legacy session path without organization notifications")
	e := echo.New()
	e.Use(tc.handler.AuthMiddleware())
	e.GET("/events/ws", tc.handler.LiveWebSocket)
	server := httptest.NewServer(e)
	defer server.Close()
	token, err := cfg.SignToken(time.Now())
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {cfg.CookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var frame liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &frame))
	require.Equal(t, "ready", frame.Event)
	for wsjson.Read(ctx, conn, &frame) == nil {
		require.Equal(t, "system_update", frame.Event)
	}
	require.NoError(t, ctx.Err(), "expired session must close at the existing ping interval")
}
