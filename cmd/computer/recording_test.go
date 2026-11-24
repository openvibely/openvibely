//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/computerrecording"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Runs only in the disposable computer image, with a private X display.
func TestRecordingRealDesktop(t *testing.T) {
	if os.Getenv("OV_RECORDING_LIVE_TEST") != "1" {
		t.Skip("requires isolated computer image")
	}
	t.Setenv("DISPLAY", ":77")
	t.Setenv("OPENVIBELY_DESKTOP_PORT", "19000")
	x := exec.Command("Xvfb", ":77", "-screen", "0", "800x600x24", "-ac")
	if err := x.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { x.Process.Kill(); x.Wait() }()
	time.Sleep(time.Second)
	mux := http.NewServeMux()
	d := recordingRoutes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(action string) computerrecording.Result {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"action": action, "id": "live", "limit": 3})
		res, err := http.Post(server.URL+"/recording", "application/json", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d", action, res.StatusCode)
		}
		var out computerrecording.Result
		if err = json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	call("start")
	// Proxy real WebSocket bytes through the same observer used for desktop input.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			if _, _, err = c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	viewers := &viewerConnections{recorder: d}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { viewers.proxy(w, r, "ws"+upstream.URL[4:]) }))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+proxy.URL[4:]+"/rfb-control", &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": []string{"test"}}, Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("RFB 003.008\n"), 1, 1, 5, 1, 0, 40, 0, 80, 5, 0, 0, 40, 0, 80)
	for _, key := range []uint32{'h', 'e', 'l', 'l', 'o', 0xff51, 0xff51, 0xff51} {
		packet := make([]byte, 8)
		packet[0], packet[1] = 4, 1
		binary.BigEndian.PutUint32(packet[4:], key)
		payload = append(payload, packet...)
	}
	if err = c.Write(ctx, websocket.MessageBinary, payload); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	c.CloseNow()
	// Disconnect doesn't lose the recording. Freeze and transfer only selected images.
	time.Sleep(300 * time.Millisecond)
	call("pause")
	out := call("finish")
	if len(out.Frames) < 2 || len(out.Frames) > 3 || len(out.Events) != 6 {
		t.Fatalf("frames=%d events=%v", len(out.Frames), out.Events)
	}
	if !strings.Contains(out.Transcript(), `Type "hello"`) || out.Events[2].Text != `Type "hello"` {
		t.Fatalf("unknown field typing missing: %v", out.Events)
	}
	for _, event := range out.Events[3:] {
		if event.Text != "Press Left" {
			t.Fatal(out.Events)
		}
	}
	before := out.Frames[len(out.Frames)-1].Millis
	time.Sleep(300 * time.Millisecond)
	retry := call("finish")
	if retry.Frames[len(retry.Frames)-1].Millis != before {
		t.Fatal("retry changed recording")
	}
	call("ack")
	if d.recording.Status().ID != "" {
		t.Fatal("not cleaned up")
	}
}

// An unsaved recording is work even when its viewer is disconnected.
func TestRecordingProtectsDesktopFromIdleCleanup(t *testing.T) {
	mux := http.NewServeMux()
	var recorder computerrecording.Recorder
	shellRoutes(mux, func() bool { return recorder.Status().ID != "" })
	check := func(want bool) {
		t.Helper()
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("GET", "/jobs", nil))
		var state struct {
			Pending bool `json:"recording_pending"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Pending != want {
			t.Fatalf("pending=%v, want %v", state.Pending, want)
		}
	}
	check(false)
	if err := recorder.Start("demo"); err != nil {
		t.Fatal(err)
	}
	check(true)
	recorder.Pause()
	check(true)
	if err := recorder.Clear("demo"); err != nil {
		t.Fatal(err)
	}
	check(false)
}
