//go:build linux

package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"
)

func browserRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /browser", func(w http.ResponseWriter, r *http.Request) {
		inputMu.Lock()
		defer inputMu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", "/usr/local/lib/openvibely-browser.cjs")
		cmd.Env = append(os.Environ(), "OPENVIBELY_CDP_PORT="+computerPort(1))
		cmd.Stdin = http.MaxBytesReader(w, r.Body, 2<<20)
		out, err := cmd.Output()
		if err != nil {
			http.Error(w, "Browser operation failed. Inspect the desktop and retry.", 502)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, string(out))
	})
}
