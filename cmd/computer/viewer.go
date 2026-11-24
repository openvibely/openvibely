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
	if v.recorder != nil && strings.HasSuffix(r.URL.Path, "rfb-control") {
		observer.Action = v.recorder.recording.Action
		observer.Text = v.recorder.recording.Text
		observer.Backspace = v.recorder.recording.Backspace
		// RFB/X11 provide no secure-field classification. Keep Security nil
		// (unknown/redacted) until a detector or explicit consent policy is approved.
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
			if dst.Write(ctx, kind, data) != nil {
				return
			}
			observer.Feed(data)
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
