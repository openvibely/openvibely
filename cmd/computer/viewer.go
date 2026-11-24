//go:build linux

package main

import (
	"context"
	"errors"
	"github.com/openvibely/openvibely/internal/computerrecording"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// viewerConnections tracks active proxy connections, not inactivity timers.
type viewerConnections struct {
	sync.Mutex
	sessions map[string]bool
	recorder *desktopRecorder
}

// Only the trusted app can reach this proxy.
func (v *viewerConnections) proxy(w http.ResponseWriter, r *http.Request, target string) {
	id := r.Header.Get("X-OV-Viewer-ID")
	if id == "" || len(id) > 128 {
		http.Error(w, "Viewer session required", 400)
		return
	}
	v.Lock()
	if v.sessions == nil {
		v.sessions = map[string]bool{}
	}
	if v.sessions[id] {
		v.Unlock()
		http.Error(w, "Viewer already connected", 409)
		return
	}
	v.sessions[id] = true
	v.Unlock()
	defer func() { v.Lock(); delete(v.sessions, id); v.Unlock() }()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	upstream, _, err := websocket.Dial(ctx, target, &websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		http.Error(w, "Desktop unavailable", 502)
		return
	}
	defer upstream.CloseNow()
	downstream, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer downstream.CloseNow()
	upstream.SetReadLimit(16 << 20)
	downstream.SetReadLimit(1 << 20)
	done := make(chan struct{}, 2)
	observer := &computerrecording.Input{}
	frameSecurity := computerrecording.InputUnknown
	postSecurity := computerrecording.InputUnknown
	inspectedAfterForward := false
	recordingInput := v.recorder != nil && strings.HasSuffix(r.URL.Path, "rfb-control")
	if recordingInput {
		observer.Action = v.recorder.recording.Action
		observer.ContinueAction = v.recorder.recording.ContinueAction
		observer.Text = v.recorder.recording.Text
		observer.Backspace = v.recorder.recording.Backspace
		// Probe both sides of forwarding once per frame, never across frames.
		// Batching prevents a coalesced typing packet from issuing one CDP probe per character.
		observer.Security = func() computerrecording.InputSecurity {
			if frameSecurity == computerrecording.InputSecure {
				return frameSecurity
			}
			if state, _ := v.recorder.recording.CaptureState(); state != "recording" {
				return computerrecording.InputUnknown
			}
			if !inspectedAfterForward {
				postSecurity = focusedInputSecurity()
				inspectedAfterForward = true
			}
			return postSecurity
		}
		observer.Unsupported = func() { v.recorder.recording.Fail(errors.New("unsupported desktop input protocol; recording stopped")) }
	} else {
		observer.Action = func(string) {}
	}
	copyFrames := func(dst, src *websocket.Conn) {
		defer func() { done <- struct{}{} }()
		for {
			kind, data, err := src.Read(ctx)
			if err != nil {
				return
			}
			err = func() error {
				if recordingInput {
					v.recorder.inputMu.Lock()
					defer v.recorder.inputMu.Unlock()
				}
				// Conservative frame-local protection if typing itself moves focus.
				// This snapshot is discarded for the next frame, never cached across focus changes.
				frameSecurity = computerrecording.InputUnknown
				inspectedAfterForward = false
				postSecurity = computerrecording.InputUnknown
				if recordingInput {
					if state, _ := v.recorder.recording.CaptureState(); state == "recording" {
						frameSecurity = focusedInputSecurity()
					}
				}
				if err := dst.Write(ctx, kind, data); err != nil {
					return err
				}
				observer.Feed(data)
				return nil
			}()
			if err != nil {
				return
			}
		}
	}
	go func() {
		defer func() { done <- struct{}{} }()
		forwardViewerScreen(ctx, downstream, upstream)
	}()
	go copyFrames(upstream, downstream)
	// A connected viewer protects its desktop, even without keyboard or mouse
	// input. Only the desktop reaper owns an inactivity timer.
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// forwardViewerScreen streams each screen message with bounded buffering while
// preserving its type and boundary. Do not close the writer after a copy error:
// that would finalize an incomplete update as a successful message.
func forwardViewerScreen(ctx context.Context, dst, src *websocket.Conn) error {
	buffer := make([]byte, 32<<10)
	for {
		kind, reader, err := src.Reader(ctx)
		if err != nil {
			return err
		}
		writer, err := dst.Writer(ctx, kind)
		if err != nil {
			return err
		}
		if _, err := io.CopyBuffer(writer, reader, buffer); err != nil {
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
	}
}
