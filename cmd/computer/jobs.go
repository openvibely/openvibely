//go:build linux

package main

import (
	"encoding/json"
	"github.com/openvibely/openvibely/pkg/agenttools"
	"net/http"
	"path/filepath"
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
}

// Standard names and explicit computer tools use the same task-local manager.
func processRoutes(mux *http.ServeMux, manager *agenttools.ProcessManager) {
	executions := &computerExecutions{entries: make(map[string]*computerExecution), manager: manager}
	mux.HandleFunc("POST /executions/cancel", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			ID string `json:"execution_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&p); err != nil {
			http.Error(w, "Invalid execution", 400)
			return
		}
		if err := executions.cancel(r.Context(), p.ID); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		json.NewEncoder(w).Encode(map[string]bool{"cancelled": true})
	})
	mux.HandleFunc("POST /process-tools", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Name        string          `json:"name"`
			ExecutionID string          `json:"execution_id"`
			Input       json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&p); err != nil {
			http.Error(w, "Invalid process call", 400)
			return
		}
		owner, err := executions.owner(p.ExecutionID)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		out, err := manager.Execute(agenttools.WithProcessOwner(r.Context(), owner), filepath.Join(computerHome(), "Files"), p.Name, p.Input)
		result := map[string]any{"output": out}
		if err != nil {
			result["error"] = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})
}
