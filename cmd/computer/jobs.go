//go:build linux

package main

import (
	"context"
	"encoding/json"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func shellRoutes(mux *http.ServeMux) {
	manager := agenttools.NewProcessManager(shellCommand)
	processRoutes(mux, manager)
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		values := []map[string]any{}
		for _, j := range manager.Jobs(r.URL.Query().Get("job_id")) {
			values = append(values, j.Snapshot())
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"jobs": values, "shell_processes_running": shellProcessesRunning()})
	})
	mux.HandleFunc("POST /jobs/cancel", func(w http.ResponseWriter, r *http.Request) {
		var args struct {
			ID string `json:"job_id"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&args) != nil {
			http.Error(w, "Invalid job", 400)
			return
		}
		jobs := manager.Jobs(args.ID)
		for _, j := range jobs {
			j.Cancel()
		}

		for _, j := range jobs {
			select {
			case <-j.Done():
			case <-r.Context().Done():
				http.Error(w, "Cancellation interrupted", 503)
				return
			}
		}
		if args.ID == "" {
			if err := stopShellProcesses(r.Context()); err != nil {
				http.Error(w, "Shell cleanup incomplete", 503)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]bool{"cancelled": true})
	})
	mux.HandleFunc("POST /shell", func(w http.ResponseWriter, r *http.Request) {
		var args struct {
			Command    string `json:"command"`
			Directory  string `json:"working_directory"`
			Timeout    int    `json:"timeout_seconds"`
			Background bool   `json:"background"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&args) != nil || strings.TrimSpace(args.Command) == "" {
			http.Error(w, "Provide a command", 400)
			return
		}
		path, err := homePath(args.Directory)
		if err != nil {
			http.Error(w, "Invalid directory", 400)
			return
		}
		parent := r.Context()
		if args.Background {
			parent = context.Background()
		}
		timeout := agenttools.NormalizeBashTimeout(args.Timeout)
		job, err := manager.StartJob(agenttools.WithProcessLifetime(parent), filepath.Join(computerHome(), path), args.Command, timeout)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		if args.Background {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(job.Snapshot())
			return
		}
		defer job.Cancel()
		if r.Header.Get("Accept") != "application/x-ndjson" {
			<-job.Done()
			json.NewEncoder(w).Encode(job.Snapshot())
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		flush := http.NewResponseController(w)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			value := job.Snapshot()
			value["type"] = "output"
			if value["running"] == false {
				value["type"] = "result"
			}
			if enc.Encode(value) != nil || flush.Flush() != nil {
				return
			}
			if value["running"] == false {
				return
			}
			select {
			case <-job.Done():
			case <-tick.C:
			case <-r.Context().Done():
				return
			}
		}
	})
}

// Standard names and explicit computer tools use the same task-local manager.
func processRoutes(mux *http.ServeMux, manager *agenttools.ProcessManager) {
	mux.HandleFunc("POST /process-tools", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&p); err != nil {
			http.Error(w, "Invalid process call", 400)
			return
		}
		out, err := manager.Execute(r.Context(), filepath.Join(computerHome(), "Files"), p.Name, p.Input)
		result := map[string]any{"output": out}
		if err != nil {
			result["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}
