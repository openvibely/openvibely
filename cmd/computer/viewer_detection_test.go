//go:build linux

package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestViewerSkipsDetectionForNonTextInput(t *testing.T) {
	for _, extended := range []bool{false, true} {
		t.Run(fmt.Sprint(extended), func(t *testing.T) {
			var probes atomic.Int32
			detector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probes.Add(1)
				<-r.Context().Done() // Simulate an unavailable detector until its deadline.
			}))
			defer detector.Close()
			endpoint, _ := url.Parse(detector.URL)
			port, _ := strconv.Atoi(endpoint.Port())
			t.Setenv("OPENVIBELY_DESKTOP_PORT", strconv.Itoa(port-1))
			received := make(chan struct{}, 32)
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
			d := &desktopRecorder{}
			if err := d.recording.Start("navigation"); err != nil {
				t.Fatal(err)
			}
			v := &viewerConnections{recorder: d}
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { v.proxy(w, r, "ws"+upstream.URL[4:]) }))
			defer proxy.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws"+proxy.URL[4:]+"/rfb-control", &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": []string{"nontext"}}, Subprotocols: []string{"binary"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			send := func(data []byte) {
				t.Helper()
				if err := c.Write(ctx, websocket.MessageBinary, data); err != nil {
					t.Fatal(err)
				}
				select {
				case <-received:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			key := func(k uint32, down bool) []byte {
				b := []byte{4, 0, 0, 0, 0, 0, 0, 0}
				if down {
					b[1] = 1
				}
				binary.BigEndian.PutUint32(b[4:], k)
				if extended {
					b = append([]byte{255, 0, 0, b[1]}, b[4:]...)
					b = append(b, 0, 0, 0, 0)
				}
				return b
			}
			send(append([]byte("RFB 003.008\n"), 1, 1))
			began := time.Now()
			for i := 0; i < 8; i++ {
				send(key(0xff54, true))
			}
			t.Logf("eight navigation frames forwarded in %s", time.Since(began))
			send([]byte{5, 0, 0, 10, 0, 20})
			send([]byte{5, 1, 0, 10, 0, 20, 5, 0, 0, 10, 0, 20, 5, 8, 0, 10, 0, 20})
			send(key(0xffe3, true))
			send(key('v', true))
			send(key('v', false))
			send(key(0xffe3, false))
			send(append([]byte{6, 0, 0, 0, 0, 0, 0, 6}, []byte("secret")...))
			send(key(0xff08, true))
			send(key(0xffbe, true))
			waitText := func(text string) {
				t.Helper()
				for ctx.Err() == nil {
					if strings.Contains(d.recording.Status().Transcript(), text) {
						return
					}
					time.Sleep(time.Millisecond)
				}
				t.Fatal(ctx.Err())
			}
			waitText("Press F1")
			if got := probes.Load(); got != 0 {
				t.Fatalf("non-text input invoked detection %d times", got)
			}
			if !strings.Contains(d.recording.Status().Transcript(), "Press Down ×8") {
				t.Fatal(d.recording.Status().Transcript())
			}
			// Real typing must still take both fresh probes, then retain unknown input.
			var text []byte
			for _, ch := range "hello" {
				text = append(text, key(uint32(ch), true)...)
			}
			send(text)
			waitText(`Type "hello"`)
			if got := probes.Load(); got != 2 {
				t.Fatalf("typing made %d probes, want 2", got)
			}
			if strings.Contains(d.recording.Status().Transcript(), "secret") {
				t.Fatal("clipboard captured")
			}
		})
	}
}
