//go:build linux

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

type computerJob struct {
	id       string
	created  time.Time
	finished time.Time
	out      limitedOutput
	done     chan struct{}
	cancel   context.CancelFunc
	code     int
	timedOut bool
}

func (j *computerJob) snapshot() map[string]any {
	text, truncated := j.out.snapshot()
	result := map[string]any{"job_id": j.id, "output": text, "truncated": truncated, "running": true, "created_at": j.created}
	select {
	case <-j.done:
		result["running"] = false
		result["finished_at"] = j.finished
		result["exit_code"] = j.code
		result["timed_out"] = j.timedOut
	default:
	}
	return result
}

type computerJobs struct {
	mu   sync.Mutex
	jobs map[string]*computerJob
}

func (m *computerJobs) start(parent context.Context, command, directory string, timeout int) (*computerJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.jobs) >= 64 {
		var oldest *computerJob
		for _, j := range m.jobs {
			select {
			case <-j.done:
				if oldest == nil || j.created.Before(oldest.created) {
					oldest = j
				}
			default:
			}
		}
		if oldest == nil {
			return nil, errors.New("too many active jobs")
		}
		delete(m.jobs, oldest.id)
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeout)*time.Second)
	j := &computerJob{id: hex.EncodeToString(token[:]), created: time.Now(), done: make(chan struct{}), cancel: cancel}
	cmd, err := shellCommand(ctx, command, directory)
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Dir = directory
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Stdout = &j.out
	cmd.Stderr = &j.out
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, errors.New("could not start command")
	}
	m.jobs[j.id] = j
	go func() {
		err := cmd.Wait()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		j.code = 0
		if err != nil {
			j.code = -1
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				j.code = exit.ExitCode()
			}
		}
		j.finished = time.Now()
		j.timedOut = ctx.Err() == context.DeadlineExceeded
		cancel()
		close(j.done)
	}()
	return j, nil
}
func shellRoutes(mux *http.ServeMux) {
	manager := &computerJobs{jobs: map[string]*computerJob{}}
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, r *http.Request) {
		manager.mu.Lock()
		defer manager.mu.Unlock()
		jobs := []*computerJob{}
		for _, j := range manager.jobs {
			jobs = append(jobs, j)
		}
		sort.Slice(jobs, func(a, b int) bool { return jobs[a].created.Before(jobs[b].created) })
		values := []map[string]any{}
		for _, j := range jobs {
			if id := r.URL.Query().Get("job_id"); id == "" || id == j.id {
				values = append(values, j.snapshot())
			}
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
		manager.mu.Lock()
		jobs := []*computerJob{}
		for _, j := range manager.jobs {
			if args.ID == "" || j.id == args.ID {
				j.cancel()
				jobs = append(jobs, j)
			}
		}
		manager.mu.Unlock()
		for _, j := range jobs {
			select {
			case <-j.done:
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
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 100000)).Decode(&args) != nil || strings.TrimSpace(args.Command) == "" {
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
		timeout := computerShellTimeout(args.Timeout, args.Background)
		job, err := manager.start(parent, args.Command, filepath.Join(computerHome(), path), timeout)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		if args.Background {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(job.snapshot())
			return
		}
		defer job.cancel()
		if r.Header.Get("Accept") != "application/x-ndjson" {
			<-job.done
			json.NewEncoder(w).Encode(job.snapshot())
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		enc := json.NewEncoder(w)
		flush := http.NewResponseController(w)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			value := job.snapshot()
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
			case <-job.done:
			case <-tick.C:
			case <-r.Context().Done():
				return
			}
		}
	})
}
