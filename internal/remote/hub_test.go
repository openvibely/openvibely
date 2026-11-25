package remote

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestSilentRemoteIsMarkedDown(t *testing.T) {
	old := silenceTimeout
	silenceTimeout = 300 * time.Millisecond
	t.Cleanup(func() { silenceTimeout = old })

	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"openvibely-live"}})
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_ = wsjson.Write(r.Context(), ws, Frame{Event: "ready"})
		// Stay connected but send nothing, like a host that lost power.
		<-hold
	}))
	t.Cleanup(srv.Close)

	var changes atomic.Int32
	hub := &Hub{OnChange: func(string, string) { changes.Add(1) }}
	base, _ := url.Parse(srv.URL)
	jar, _ := cookiejar.New(nil)
	hub.Connect("u", "box", base, jar, nil)
	t.Cleanup(func() { hub.Close("u", "box") })

	deadline := time.Now().Add(5 * time.Second)
	for !hub.Get("u", "box").Down() {
		if time.Now().After(deadline) {
			t.Fatal("silent remote was never marked down")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(hub.Servers("u")) != 0 {
		t.Fatal("a down server is still listed")
	}
	if changes.Load() < 2 {
		t.Fatalf("pages were not told: %d changes", changes.Load())
	}
}

func TestPingingRemoteStaysConnected(t *testing.T) {
	old := silenceTimeout
	silenceTimeout = 300 * time.Millisecond
	t.Cleanup(func() { silenceTimeout = old })

	var dials atomic.Int32
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"openvibely-live"}})
		if err != nil {
			return
		}
		dials.Add(1)
		defer ws.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		go func() {
			defer cancel()
			for {
				if _, _, err := ws.Read(ctx); err != nil {
					return
				}
			}
		}()
		_ = wsjson.Write(ctx, ws, Frame{Event: "ready"})
		// Send only pings, like an idle remote.
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-hold:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if ws.Ping(ctx) != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)

	hub := &Hub{}
	base, _ := url.Parse(srv.URL)
	jar, _ := cookiejar.New(nil)
	hub.Connect("u", "remote1", base, jar, nil)
	t.Cleanup(func() { hub.Close("u", "remote1") })

	deadline := time.Now().Add(5 * time.Second)
	for dials.Load() == 0 || hub.Get("u", "remote1").Down() {
		if time.Now().After(deadline) {
			t.Fatal("remote never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(4 * silenceTimeout)
	if hub.Get("u", "remote1").Down() || dials.Load() != 1 {
		t.Fatalf("idle remote that pings was dropped: down=%v dials=%d", hub.Get("u", "remote1").Down(), dials.Load())
	}
}

func TestOnlySignInRedirectsExpireTheSession(t *testing.T) {
	for name, tc := range map[string]struct {
		location string
		expired  bool
	}{
		"login":          {"/login?next=abc", true},
		"hosted sso":     {"/auth/sso/start?next=abc", true},
		"absolute login": {"{base}/login?next=abc", true},
		"https upgrade":  {"https://{host}/events/ws", false},
		"other host":     {"https://elsewhere.example/login", false},
		"maintenance":    {"/maintenance", false},
	} {
		t.Run(name, func(t *testing.T) {
			var srv *httptest.Server
			srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				loc := strings.ReplaceAll(strings.ReplaceAll(tc.location, "{base}", srv.URL), "{host}", r.Host)
				http.Redirect(w, r, loc, http.StatusFound)
			}))
			t.Cleanup(srv.Close)
			expired := make(chan struct{}, 1)
			hub := &Hub{}
			base, _ := url.Parse(srv.URL)
			jar, _ := cookiejar.New(nil)
			hub.Connect("u", "box", base, jar, func() { expired <- struct{}{} })
			t.Cleanup(func() { hub.Close("u", "box") })
			select {
			case <-expired:
				if !tc.expired {
					t.Fatal("a non-sign-in redirect signed the user out")
				}
			case <-time.After(500 * time.Millisecond):
				if tc.expired {
					t.Fatal("a sign-in redirect did not expire the session")
				}
				if !hub.Get("u", "box").Down() {
					t.Fatal("server should be shown offline")
				}
			}
		})
	}
}

func TestShutdownClosesEverySocket(t *testing.T) {
	var open atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"openvibely-live"}})
		if err != nil {
			return
		}
		open.Add(1)
		defer open.Add(-1)
		defer ws.CloseNow()
		_ = wsjson.Write(r.Context(), ws, Frame{Event: "ready"})
		for {
			if _, _, err := ws.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	hub := &Hub{}
	base, _ := url.Parse(srv.URL)
	for _, server := range []string{"a", "b"} {
		jar, _ := cookiejar.New(nil)
		hub.Connect("u", server, base, jar, nil)
	}
	deadline := time.Now().Add(5 * time.Second)
	for open.Load() != 2 {
		if time.Now().After(deadline) {
			t.Fatalf("sockets did not open: %d", open.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	hub.Shutdown()
	if len(hub.AllServers("u")) != 0 {
		t.Fatal("sockets still listed after shutdown")
	}
	for open.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("remote still has %d sockets", open.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	jar, _ := cookiejar.New(nil)
	hub.Connect("u", "c", base, jar, nil)
	if hub.Get("u", "c") != nil {
		t.Fatal("a stopped hub accepted a new socket")
	}
}
