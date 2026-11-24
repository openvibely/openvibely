//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/computerrecording"
)

// Exercise the production proxy and HTTP lifecycle handlers while a field
// inspection is pending, both before and after forwarding the input frame.
func TestRecordingSaveWaitsForViewerInput(t *testing.T) {
	if os.Getenv("OV_RECORDING_LIVE_TEST") != "1" {
		t.Skip("requires isolated computer image")
	}
	t.Setenv("DISPLAY", ":79")
	display := exec.Command("Xvfb", ":79", "-screen", "0", "800x600x24", "-ac")
	if err := display.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { display.Process.Kill(); display.Wait() }()
	time.Sleep(300 * time.Millisecond)
	for _, action := range []string{"pause", "finish"} {
		for _, probe := range []int32{1, 2} {
			t.Run(fmt.Sprintf("%s/probe%d", action, probe), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				blocked, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				var calls atomic.Int32
				detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if calls.Add(1) == probe {
						close(blocked)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					w.Write([]byte("[]"))
				}))
				defer detector.Close()
				defer unblock()
				endpoint, _ := url.Parse(detector.URL)
				port, _ := strconv.Atoi(endpoint.Port())
				t.Setenv("OPENVIBELY_DESKTOP_PORT", strconv.Itoa(port-1))
				received := make(chan []byte, 8)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
					if err != nil {
						return
					}
					defer c.CloseNow()
					for {
						_, data, err := c.Read(r.Context())
						if err != nil {
							return
						}
						select {
						case received <- data:
						case <-r.Context().Done():
							return
						}
					}
				}))
				defer upstream.Close()
				mux := http.NewServeMux()
				d := recordingRoutes(mux)
				viewers := &viewerConnections{recorder: d}
				mux.HandleFunc("/rfb-control", func(w http.ResponseWriter, r *http.Request) { viewers.proxy(w, r, "ws"+upstream.URL[4:]) })
				server := httptest.NewServer(mux)
				defer server.Close()
				ws, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/rfb-control", &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": []string{"save-race"}}, Subprotocols: []string{"binary"}})
				if err != nil {
					t.Fatal(err)
				}
				defer ws.CloseNow()
				send := func(data []byte) {
					t.Helper()
					if err := ws.Write(ctx, websocket.MessageBinary, data); err != nil {
						t.Fatal(err)
					}
				}
				receive := func() {
					t.Helper()
					select {
					case <-received:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				send(append([]byte("RFB 003.008\n"), 1, 1))
				receive()
				call := func(action string) *httptest.ResponseRecorder {
					response := httptest.NewRecorder()
					mux.ServeHTTP(response, httptest.NewRequest("POST", "/recording", strings.NewReader(fmt.Sprintf(`{"action":%q,"id":"save-race","limit":2}`, action))))
					return response
				}
				if response := call("start"); response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				var packet []byte
				for _, ch := range "hello" {
					for _, down := range []byte{1, 0} {
						key := []byte{4, down, 0, 0, 0, 0, 0, 0}
						binary.BigEndian.PutUint32(key[4:], uint32(ch))
						packet = append(packet, key...)
					}
				}
				send(packet)
				select {
				case <-blocked:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if probe == 2 {
					receive()
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- call(action) }()
				select {
				case response := <-done:
					t.Fatalf("%s completed before pending input: HTTP %d", action, response.Code)
				case <-time.After(80 * time.Millisecond):
				}
				unblock()
				var response *httptest.ResponseRecorder
				select {
				case response = <-done:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				if response.Code != 200 {
					t.Fatal(response.Body.String())
				}
				var result computerrecording.Result
				if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(result.Transcript(), `Type "hello"`) {
					t.Fatalf("final typing lost: %s", result.Transcript())
				}
				first := call("finish")
				retry := call("finish")
				if first.Code != 200 || retry.Code != 200 || !bytes.Equal(first.Body.Bytes(), retry.Body.Bytes()) {
					t.Fatal("finish retry changed recording")
				}
				if !strings.Contains(first.Body.String(), `hello`) {
					t.Fatal("finish lost typing")
				}
				if response := call("ack"); response.Code != 200 {
					t.Fatal(response.Body.String())
				}
			})
		}
	}
}
