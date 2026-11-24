//go:build linux

package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/computerrecording"
)

type blockedRecordingResponse struct {
	header           http.Header
	entered, release chan struct{}
	once             sync.Once
	body             bytes.Buffer
	status           int
}

func (w *blockedRecordingResponse) Header() http.Header    { return w.header }
func (w *blockedRecordingResponse) WriteHeader(status int) { w.status = status }
func (w *blockedRecordingResponse) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return w.body.Write(data)
}

func TestRecordingResponseDoesNotBlockViewer(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		status        int
	}{
		{"finish", `{"action":"finish","id":"slow-save","limit":2}`, 200},
		{"pause", `{"action":"pause","id":"slow-save"}`, 200},
		{"error", `{"action":"finish","id":"wrong-id","limit":2}`, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			d := recordingRoutes(mux)
			if err := d.recording.Start("slow-save"); err != nil {
				t.Fatal(err)
			}
			g, _, _ := d.recording.Ticket()
			d.recording.Capture(g, 0, []byte("image"), nil)
			for _, ch := range "hello" {
				d.recording.Text(ch, computerrecording.InputUnknown)
			}
			// Model the pause that precedes the normal save transfer; no screen capture
			// dependencies are needed once the recording is already frozen.
			d.recording.Pause()
			received := make(chan struct{}, 8)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
				if err != nil {
					return
				}
				defer c.CloseNow()
				for {
					if _, _, err := c.Read(r.Context()); err != nil {
						return
					}
					select {
					case received <- struct{}{}:
					case <-r.Context().Done():
						return
					}
				}
			}))
			defer upstream.Close()
			viewers := &viewerConnections{recorder: d}
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { viewers.proxy(w, r, "ws"+upstream.URL[4:]) }))
			defer proxy.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+proxy.URL[4:]+"/rfb-control", &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": []string{"slow-save"}}, Subprotocols: []string{"binary"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			send := func(data []byte) {
				t.Helper()
				if err := c.Write(ctx, websocket.MessageBinary, data); err != nil {
					t.Fatal(err)
				}
			}
			receive := func() {
				t.Helper()
				select {
				case <-received:
				case <-ctx.Done():
					t.Fatal("desktop input blocked by HTTP response")
				}
			}
			send(append([]byte("RFB 003.008\n"), 1, 1))
			receive()
			response := &blockedRecordingResponse{header: http.Header{}, entered: make(chan struct{}), release: make(chan struct{}), status: 200}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(response.release) }) }
			defer release()
			done := make(chan struct{})
			go func() {
				defer close(done)
				mux.ServeHTTP(response, httptest.NewRequest("POST", "/recording", strings.NewReader(tc.command)))
			}()
			select {
			case <-response.entered:
			case <-ctx.Done():
				t.Fatal("response did not start")
			}
			// Both keyboard and pointer input must reach the desktop while the HTTP
			// writer is still blocked, not merely after the save request completes.
			send([]byte{4, 1, 0, 0, 0, 0, 0xff, 0x54})
			receive()
			send([]byte{5, 0, 0, 10, 0, 20})
			receive()
			select {
			case <-done:
				t.Fatal("response unexpectedly finished")
			default:
			}
			release()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if response.status != tc.status {
				t.Fatalf("HTTP %d, want %d", response.status, tc.status)
			}
			if tc.status == 200 && !strings.Contains(response.body.String(), "hello") {
				t.Fatal("snapshot lost typed text")
			}
			if tc.name == "finish" {
				retry := httptest.NewRecorder()
				mux.ServeHTTP(retry, httptest.NewRequest("POST", "/recording", strings.NewReader(tc.command)))
				if retry.Code != 200 || !bytes.Equal(response.body.Bytes(), retry.Body.Bytes()) {
					t.Fatal("save retry changed snapshot")
				}
			}
		})
	}
}
