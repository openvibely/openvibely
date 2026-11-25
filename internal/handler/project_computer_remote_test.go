package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/openvibely/openvibely/internal/remoteid"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

// A desktop uses Remote 1's project computer: settings, status, buttons, live
// status and the screen all come from Remote 1.
func TestRemoteProjectComputerRunsOnRemote(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	screen := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			kind, data, err := conn.Read(ctx)
			if err != nil || conn.Write(ctx, kind, append([]byte("echo:"), data...)) != nil {
				return
			}
		}
	}))
	defer screen.Close()

	h, e := setupHubHandler(t)
	srv, _, owner := fakeRemoteOV(t, "secret")
	owner.computerSvc = service.NewProjectComputerService(owner.projectRepo, liveComputerFixture{service.ComputerConnection{Endpoint: screen.URL, Token: "test"}})
	project := createProject(t, owner, "Project 1")
	require.NoError(t, owner.projectRepo.Computers().Configure(ctx, project.ID, "local"))
	require.NoError(t, owner.computerSvc.SetEnabled(ctx, project.ID, true))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	server := savedServerID(t, h)
	require.Eventually(t, func() bool {
		return h.remoteSignedIn(ctx, server) && !h.remoteDown(ctx, server)
	}, 5*time.Second, 50*time.Millisecond)
	remoteProject := remoteid.ID(server, project.ID)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/projects/"+remoteProject+"/edit", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Regexp(t, `name="computer_enabled"[^>]*checked`, rec.Body.String())

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/projects/"+remoteProject+"/computer", nil))
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())
	require.Equal(t, "/settings/computer?project_id="+url.QueryEscape(remoteProject), rec.Header().Get("Location"))

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/projects/"+remoteid.ID(server, "missing")+"/computer", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodGet, "/projects/"+remoteProject+"/computer/status", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(t, `{"enabled":true}`, rec.Body.String())

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/projects/"+remoteProject+"/computer/time-zone", url.Values{"time_zone": {"Europe/Paris"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	state, err := owner.computerSvc.Get(ctx, project.ID)
	require.NoError(t, err)
	require.Equal(t, "Europe/Paris", state.TimeZone)

	desktop := httptest.NewServer(e)
	defer desktop.Close()
	ws := strings.Replace(desktop.URL, "http", "ws", 1)
	live, _, err := websocket.Dial(ctx, ws+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {desktop.URL}}})
	require.NoError(t, err)
	defer live.CloseNow()
	require.NoError(t, wsjson.Write(ctx, live, liveCommand{Op: "computer", ID: "c1", ProjectID: remoteProject, ViewerID: "viewer-1"}))
	for {
		var frame struct {
			ID, Event string
			Data      map[string]any
		}
		require.NoError(t, wsjson.Read(ctx, live, &frame))
		if frame.ID != "c1" || frame.Event == "subscribed" {
			continue
		}
		require.Equal(t, "computer_status", frame.Event)
		require.Equal(t, remoteProject, frame.Data["project_id"])
		require.Equal(t, "Europe/Paris", frame.Data["time_zone"])
		require.NotContains(t, frame.Data, "stream_url")
		break
	}

	view, _, err := websocket.Dial(ctx, ws+"/projects/"+remoteProject+"/computer/stream/view", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {desktop.URL}}, Subprotocols: []string{"binary"}})
	require.NoError(t, err)
	defer view.CloseNow()
	require.NoError(t, view.Write(ctx, websocket.MessageBinary, []byte("ping")))
	_, data, err := view.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, "echo:ping", string(data))

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/projects/"+remoteProject+"/computer/disable", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	state, err = owner.computerSvc.Get(ctx, project.ID)
	require.NoError(t, err)
	require.False(t, state.Enabled)
}

// When the sign-in to Remote 1 expires, every computer request asks the user
// to sign in again instead of failing with a generic error.
func TestRemoteProjectComputerAsksToReconnectWhenSignedOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	h, e := setupHubHandler(t)
	srv, revoked, owner := fakeRemoteOV(t, "secret")
	owner.computerSvc = service.NewProjectComputerService(owner.projectRepo, liveComputerFixture{})
	project := createProject(t, owner, "Project 1")

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, serversRequest(http.MethodPost, "/servers", url.Values{"name": {"Remote 1"}, "url": {srv.URL}, "username": {"alice"}, "password": {"secret"}}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	server := savedServerID(t, h)
	require.Eventually(t, func() bool {
		return h.remoteSignedIn(ctx, server) && !h.remoteDown(ctx, server)
	}, 5*time.Second, 50*time.Millisecond)
	remoteProject := remoteid.ID(server, project.ID)
	reconnect := "/servers?reconnect=" + server

	// Remote 1 refuses the screen connection, so the desktop learns the sign-in expired.
	revoked.Store(true)
	desktop := httptest.NewServer(e)
	defer desktop.Close()
	ws := strings.Replace(desktop.URL, "http", "ws", 1)
	_, resp, err := websocket.Dial(ctx, ws+"/projects/"+remoteProject+"/computer/stream/view", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {desktop.URL}}, Subprotocols: []string{"binary"}})
	require.Error(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.False(t, h.remoteSignedIn(ctx, server))

	for _, req := range []*http.Request{
		serversRequest(http.MethodGet, "/projects/"+remoteProject+"/computer", nil),
		serversRequest(http.MethodPost, "/projects/"+remoteProject+"/computer/start", url.Values{}),
	} {
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusUnauthorized, rec.Code, req.URL.Path)
		require.Equal(t, reconnect, rec.Header().Get("HX-Redirect"), req.URL.Path)
	}

	teach := httptest.NewRequest(http.MethodPost, "/projects/"+remoteProject+"/computer/teach", strings.NewReader(`{"goal":"Open the site"}`))
	teach.Header.Set("Origin", "http://example.com")
	teach.Header.Set("Accept", "application/x-ndjson")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, teach)
	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), reconnect)

	live, _, err := websocket.Dial(ctx, ws+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {desktop.URL}}})
	require.NoError(t, err)
	defer live.CloseNow()
	require.NoError(t, wsjson.Write(ctx, live, liveCommand{Op: "computer", ID: "c1", ProjectID: remoteProject, ViewerID: "viewer-1"}))
	for {
		var frame struct {
			ID, Event string
			Data      map[string]any
		}
		require.NoError(t, wsjson.Read(ctx, live, &frame))
		if frame.ID != "c1" {
			continue
		}
		require.Equal(t, "error", frame.Event)
		require.Equal(t, reconnect, frame.Data["reconnect"])
		break
	}
}
