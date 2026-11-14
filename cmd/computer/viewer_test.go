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

func TestVisibleViewerStaysConnectedWithoutUserInput(t *testing.T) {
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
	leases := &viewerConnections{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leases.proxy(w, r, strings.Replace(backend.URL, "http", "ws", 1))
	}))
	defer server.Close()
	dial := func(id string) *websocket.Conn {
		t.Helper()
		c, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1), &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": {id}}, Subprotocols: []string{"binary"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	idle := dial("first")
	defer idle.CloseNow()
	never := dial("second")
	defer never.CloseNow()
	until := time.Now().Add(2200 * time.Millisecond)
	for time.Now().Before(until) {
		if _, _, err := idle.Read(ctx); err != nil {
			t.Fatalf("visible viewer disconnected without user input: %v", err)
		}
	}
	if _, _, err := never.Read(ctx); err != nil {
		t.Fatalf("other viewer was disconnected: %v", err)
	}
}
