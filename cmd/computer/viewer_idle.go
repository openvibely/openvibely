//go:build linux

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const viewerIdleClose = websocket.StatusCode(4001)

type viewerLease struct {
	last    time.Time
	timeout time.Duration
}
type viewerLeases struct {
	sync.Mutex
	sessions map[string]*viewerLease
}

func (v *viewerLeases) activity(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
		http.Error(w, "Invalid session", 400)
		return
	}
	v.Lock()
	lease := v.sessions[input.ID]
	if lease != nil {
		lease.last = time.Now()
	}
	v.Unlock()
	if lease == nil {
		http.Error(w, "Viewer disconnected", 410)
		return
	}
	w.WriteHeader(204)
}

func (v *viewerLeases) policy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ID      string `json:"id"`
		Seconds int    `json:"idle_seconds"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil || input.Seconds < 0 || input.Seconds > 604800 {
		http.Error(w, "Invalid policy", 400)
		return
	}
	v.Lock()
	lease := v.sessions[input.ID]
	if lease != nil {
		lease.timeout = time.Duration(input.Seconds) * time.Second
	}
	v.Unlock()
	w.WriteHeader(204)
}

// Only the trusted app can reach these routes. Screen updates and websocket
// maintenance never count as activity; the app forwards explicit user actions.
func (v *viewerLeases) proxy(w http.ResponseWriter, r *http.Request, target string) {
	id := r.Header.Get("X-OV-Viewer-ID")
	seconds, err := strconv.Atoi(r.Header.Get("X-OV-Viewer-Idle-Seconds"))
	if id == "" || len(id) > 128 || err != nil || seconds < 0 || seconds > 604800 {
		http.Error(w, "Viewer policy required; update OV", 400)
		return
	}
	lease := &viewerLease{last: time.Now(), timeout: time.Duration(seconds) * time.Second}
	v.Lock()
	if v.sessions == nil {
		v.sessions = map[string]*viewerLease{}
	}
	if v.sessions[id] != nil {
		v.Unlock()
		http.Error(w, "Viewer already connected", 409)
		return
	}
	v.sessions[id] = lease
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
		}
	}
	go copyFrames(downstream, upstream)
	go copyFrames(upstream, downstream)
	// A connected viewer protects its desktop, even without keyboard or mouse
	// input. Only the desktop reaper owns an inactivity timer.
	select {
	case <-done:
	case <-ctx.Done():
	}
}
