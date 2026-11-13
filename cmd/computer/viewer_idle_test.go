//go:build linux

package main

import (
	"context"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestViewerIdleIgnoresFramesAndProtectsOtherSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if c.Write(ctx, websocket.MessageBinary, []byte("screen refresh")) != nil {
					return
				}
			}
		}
	}))
	defer backend.Close()
	leases := &viewerLeases{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leases.proxy(w, r, strings.Replace(backend.URL, "http", "ws", 1))
	}))
	defer server.Close()
	dial := func(id, seconds string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1), &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": {id}, "X-OV-Viewer-Idle-Seconds": {seconds}}, Subprotocols: []string{"binary"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	idle := dial("idle", "1")
	defer idle.CloseNow()
	never := dial("never", "0")
	defer never.CloseNow()
	for {
		_, _, err := idle.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != viewerIdleClose {
				t.Fatal(err)
			}
			break
		}
	}
	if _, _, err := never.Read(ctx); err != nil {
		t.Fatalf("other viewer was disconnected: %v", err)
	}
}
func TestViewerActivityOnlyTouchesSelectedLease(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	leases := &viewerLeases{sessions: map[string]*viewerLease{"one": {last: old}, "two": {last: old}}}
	rec := httptest.NewRecorder()
	leases.activity(rec, httptest.NewRequest("POST", "/viewer-activity", strings.NewReader(`{"id":"one"}`)))
	if rec.Code != 204 || !leases.sessions["one"].last.After(old) || !leases.sessions["two"].last.Equal(old) {
		t.Fatal("activity affected wrong session")
	}
	rec = httptest.NewRecorder()
	leases.activity(rec, httptest.NewRequest("POST", "/viewer-activity", strings.NewReader(`{"id":"missing"}`)))
	if rec.Code != 410 {
		t.Fatal(rec.Code)
	}
}

func TestViewerPolicyNeverDoesNotCountAsActivity(t *testing.T) {
	old := time.Now().Add(-time.Hour)
	leases := &viewerLeases{sessions: map[string]*viewerLease{"one": {last: old, timeout: time.Second}}}
	rec := httptest.NewRecorder()
	leases.policy(rec, httptest.NewRequest("POST", "/viewer-policy", strings.NewReader(`{"id":"one","idle_seconds":0}`)))
	if rec.Code != 204 || leases.sessions["one"].timeout != 0 || !leases.sessions["one"].last.Equal(old) {
		t.Fatal("policy must change without renewing activity")
	}
}
