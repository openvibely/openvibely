//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/computerrecording"
)

// Uses real Chromium focus metadata and real x11vnc input, through the production
// viewer proxy and recording HTTP save/retry endpoints. No Security injection.
func TestRecordingBrowserViewerToSavedTranscript(t *testing.T) {
	if os.Getenv("OV_RECORDING_LIVE_TEST") != "1" {
		t.Skip("requires disposable computer image")
	}
	t.Setenv("DISPLAY", ":78")
	t.Setenv("OPENVIBELY_DESKTOP_PORT", "19100")
	start := func(name string, args ...string) *exec.Cmd {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); cmd.Wait() })
		return cmd
	}
	start("Xvfb", ":78", "-screen", "0", "800x600x24", "-ac")
	time.Sleep(300 * time.Millisecond)
	browser := start("chromium", "--no-sandbox", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check", "--user-data-dir="+t.TempDir(), "--remote-debugging-port=19101", "--window-size=800,600", "about:blank")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var endpoint string
	for ctx.Err() == nil {
		res, err := http.Get("http://127.0.0.1:19101/json/list")
		if err == nil {
			var targets []struct {
				Type                 string
				WebSocketDebuggerURL string
			}
			json.NewDecoder(res.Body).Decode(&targets)
			res.Body.Close()
			for _, target := range targets {
				if target.Type == "page" {
					endpoint = target.WebSocketDebuggerURL
				}
			}
		}
		if endpoint != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if endpoint == "" {
		t.Fatal("browser did not start")
	}
	cdp, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cdp.CloseNow()
	id := 0
	evaluate := func(expression string) string {
		t.Helper()
		id++
		var result struct {
			Result           struct{ Value string }
			ExceptionDetails json.RawMessage
		}
		if err := recordingCDP(ctx, cdp, id, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &result); err != nil {
			t.Fatal(err)
		}
		if len(result.ExceptionDetails) != 0 {
			t.Fatal(string(result.ExceptionDetails))
		}
		return result.Result.Value
	}
	evaluate(`document.body.innerHTML='<input id="plain"><input id="password" type="password"><textarea id="other"></textarea>'; document.title='Recording field test'; 'ready'`)
	id++
	var empty map[string]any
	if err := recordingCDP(ctx, cdp, id, "Page.bringToFront", map[string]any{}, &empty); err != nil {
		t.Fatal(err)
	}
	// Focus the real X window, not just the DOM element in an inactive tab.
	var ids []string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		windows, _ := exec.Command("xdotool", "search", "--onlyvisible", "--class", "chromium").Output()
		ids = strings.Fields(string(windows))
		if len(ids) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(ids) == 0 {
		t.Fatal("no browser window")
	}
	if err := exec.Command("xdotool", "windowfocus", "--sync", ids[0]).Run(); err != nil {
		t.Fatal(err)
	}
	focus := func(field string, want computerrecording.InputSecurity) {
		t.Helper()
		evaluate("document.getElementById('" + field + "').focus(); 'focused'")
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if focusedInputSecurity() == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("field %s classification=%v want %v", field, focusedInputSecurity(), want)
	}
	focus("plain", computerrecording.InputOrdinary)
	start("x11vnc", "-display", ":78", "-rfbport", "19102", "-nopw", "-forever", "-shared")
	start("websockify", "19104", "127.0.0.1:19102")
	time.Sleep(300 * time.Millisecond)
	mux := http.NewServeMux()
	d := recordingRoutes(mux)
	viewers := &viewerConnections{recorder: d}
	mux.HandleFunc("/rfb-control", func(w http.ResponseWriter, r *http.Request) { viewers.proxy(w, r, "ws://127.0.0.1:19104") })
	server := httptest.NewServer(mux)
	defer server.Close()
	call := func(action string) computerrecording.Result {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": action, "id": "browser-live", "limit": 3})
		res, err := http.Post(server.URL+"/recording", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("%s: %s", action, b)
		}
		var result computerrecording.Result
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	ws, _, err := websocket.Dial(ctx, "ws"+server.URL[4:]+"/rfb-control", &websocket.DialOptions{HTTPHeader: http.Header{"X-OV-Viewer-ID": []string{"browser-live"}}, Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	read := func(n int) []byte {
		t.Helper()
		b := make([]byte, n)
		if _, err := io.ReadFull(conn, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	write := func(b []byte) {
		t.Helper()
		if _, err := conn.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if string(read(12)) != "RFB 003.008\n" {
		t.Fatal("unexpected RFB version")
	}
	write([]byte("RFB 003.008\n"))
	n := int(read(1)[0])
	types := read(n)
	if !bytes.Contains(types, []byte{1}) {
		t.Fatal("None auth unavailable")
	}
	write([]byte{1})
	read(4)
	write([]byte{1})
	init := read(24)
	read(int(binary.BigEndian.Uint32(init[20:])))
	call("start")
	typeText := func(text string) {
		t.Helper()
		var packet []byte
		for _, ch := range text {
			for _, down := range []byte{1, 0} {
				key := []byte{4, down, 0, 0, 0, 0, 0, 0}
				binary.BigEndian.PutUint32(key[4:], uint32(ch))
				packet = append(packet, key...)
			}
		}
		write(packet)
	}
	waitValue := func(field, want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if evaluate("document.getElementById('"+field+"').value") == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("typing did not reach %s", field)
	}
	typeText("he")
	waitValue("plain", "he")
	// Navigation and wheel events must not close the open typing chunk.
	var navigation []byte
	for i := 0; i < 3; i++ {
		for _, down := range []byte{1, 0} {
			navigation = append(navigation, 4, down, 0, 0, 0, 0, 0xff, 0x54)
		}
	}
	navigation = append(navigation, 5, 8, 0, 30, 0, 100, 5, 0, 0, 30, 0, 100)
	write(navigation)
	typeText("llo")
	waitValue("plain", "hello")
	// Wait for observer processing as well as upstream input delivery.
	waitEvent := func(text string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(d.recording.Status().Transcript(), text) {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("missing %q: %s", text, d.recording.Status().Transcript())
	}
	waitEvent(`Type "hello"`)
	focus("password", computerrecording.InputSecure)
	typeText("privatevalue")
	waitValue("password", "privatevalue")
	waitEvent("[secure input]")
	focus("other", computerrecording.InputOrdinary)
	typeText("again")
	waitValue("other", "again")
	waitEvent(`Type "again"`)
	// Masked text, password hints, and ordinary fields update on focus changes.
	evaluate(`document.body.insertAdjacentHTML('beforeend', '<input id="masked" style="-webkit-text-security:disc"><input id="hint" autocomplete="new-password"><div id="shadow"></div>'); document.getElementById('shadow').attachShadow({mode:'open'}).innerHTML='<input type="password">'; 'ready'`)
	focus("masked", computerrecording.InputSecure)
	focus("hint", computerrecording.InputSecure)
	evaluate(`document.getElementById('shadow').shadowRoot.querySelector('input').focus(); 'focused'`)
	if focusedInputSecurity() != computerrecording.InputSecure {
		t.Fatal("open shadow password not identified")
	}
	focus("other", computerrecording.InputOrdinary)
	// An unavailable detector must not reuse its previous classification or
	// withhold all text. Here the browser is gone but RFB remains connected.
	syscall.Kill(-browser.Process.Pid, syscall.SIGKILL)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && focusedInputSecurity() != computerrecording.InputUnknown {
		time.Sleep(10 * time.Millisecond)
	}
	if focusedInputSecurity() != computerrecording.InputUnknown {
		t.Fatal("stale classification")
	}
	typeText("fallback")
	waitEvent(`Type "fallback"`)
	call("pause")
	saved := call("finish")
	transcript := saved.Transcript()
	for _, want := range []string{`Type "hello"`, "Type [secure input]", `Type "again"`, `Type "fallback"`, "Press Down ×3", "Scroll up"} {
		if !strings.Contains(transcript, want) {
			t.Fatal(transcript)
		}
	}
	if strings.Contains(transcript, "privatevalue") {
		t.Fatal("password exposed")
	}
	if retry := call("finish"); retry.Transcript() != transcript {
		t.Fatal("save retry changed transcript")
	}
	call("ack")
}
