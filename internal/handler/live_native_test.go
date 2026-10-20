package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"
)

func TestNativeLiveTicketPreservesAuthenticationAndIsSingleUse(t *testing.T) {
	h, e := authTestHandler(t)
	h.desktopMode = true
	server := httptest.NewServer(e)
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"username": {"admin"}, "password": {"secret"}}
	login, err := client.PostForm(server.URL+"/login", form)
	require.NoError(t, err)
	login.Body.Close()
	require.Equal(t, http.StatusFound, login.StatusCode)
	issue := func() map[string]string {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/events/native-ticket", nil)
		require.NoError(t, err)
		for _, cookie := range login.Cookies() {
			request.AddCookie(cookie)
		}
		request.Header.Set("Origin", "null")
		request.Header.Set("X-OpenVibely-Native-Window", "1")
		request.Header.Set("X-OpenVibely-Live", "1")
		response, err := client.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		require.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		var ticket map[string]string
		require.NoError(t, json.NewDecoder(response.Body).Decode(&ticket))
		return ticket
	}
	ticket := issue()
	// The loopback socket has no Wails cookie. Only the single-use capability can
	// restore its original authenticated session; arbitrary null origins fail.
	options := &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Origin": {"null"}}, Subprotocols: []string{"openvibely-live", "ticket." + ticket["ticket"]}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, ticket["url"], options)
	require.NoError(t, err)
	defer conn.CloseNow()
	var ready liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &ready))
	require.Equal(t, "ready", ready.Event)
	require.Equal(t, "openvibely-live", conn.Subprotocol())
	reused, response, err := websocket.Dial(ctx, ticket["url"], options)
	if reused != nil {
		reused.CloseNow()
	}
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
	// A regular browser still needs the normal login session.
	unauth, response, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Origin": {server.URL}}})
	if unauth != nil {
		unauth.CloseNow()
	}
	require.Error(t, err)
	require.Equal(t, http.StatusFound, response.StatusCode)
	// A valid capability is never accepted from a foreign website.
	ticket = issue()
	options.Subprotocols = []string{"openvibely-live", "ticket." + ticket["ticket"]}
	options.HTTPHeader.Set("Origin", "https://evil.example")
	foreign, response, err := websocket.Dial(ctx, ticket["url"], options)
	if foreign != nil {
		foreign.CloseNow()
	}
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, response.StatusCode)
}
