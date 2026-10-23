//go:build openvibely_memory_diagnostics && darwin

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sync"
	"time"
)

// startDesktopMemoryDiagnostics exposes only memory diagnostics, over a socket
// inside a private directory. It is both build-tagged and explicitly opt-in;
// nothing is registered with the application's HTTP server or DefaultServeMux.
func startDesktopMemoryDiagnostics() (func(), error) {
	if os.Getenv("OPENVIBELY_MEMORY_DIAGNOSTICS") != "1" {
		return func() {}, nil
	}
	// Darwin's Unix socket paths are short. Using /tmp avoids the long per-user
	// TMPDIR path; MkdirTemp creates a unique directory with permissions 0700.
	dir, err := os.MkdirTemp("/tmp", "ov-heap-")
	if err != nil {
		return nil, fmt.Errorf("create memory diagnostics directory: %w", err)
	}
	socket := filepath.Join(dir, "profile.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("listen for memory diagnostics: %w", err)
	}
	if err := os.Chmod(socket, 0600); err != nil {
		listener.Close()
		os.RemoveAll(dir)
		return nil, fmt.Errorf("protect memory diagnostics socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			PID              int
			Time             time.Time
			Memory           runtime.MemStats
			ModerncAllocator any `json:",omitempty"`
		}{os.Getpid(), time.Now(), stats, desktopModerncAllocatorStats()})
	})
	var capture sync.Mutex
	for _, name := range []string{"heap", "allocs"} {
		mux.HandleFunc("GET /"+name, func(w http.ResponseWriter, r *http.Request) {
			capture.Lock()
			defer capture.Unlock()
			if r.URL.Query().Get("gc") == "1" {
				runtime.GC()
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Cache-Control", "no-store")
			if err := pprof.Lookup(name).WriteTo(w, 0); err != nil {
				log.Printf("[memory-diagnostics] %s capture failed: %v", name, err)
			}
		})
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second, WriteTimeout: time.Minute}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("[memory-diagnostics] server stopped: %v", err)
		}
	}()
	log.Printf("[memory-diagnostics] pid=%d socket=%s", os.Getpid(), socket)
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = server.Close()
			_ = os.RemoveAll(dir)
		})
	}, nil
}
