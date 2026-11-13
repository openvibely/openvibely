//go:build linux

package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"syscall"
	"time"
)

// The application supplies candidates after checking task execution, leases and
// private handoffs. The supervisor independently checks live connections and jobs.
// No cached authorization is used: each sweep needs a fresh application request.
func registerDesktopReaper(mux *http.ServeMux, mu *sync.Mutex, sessions map[string]*desktopProcess) {
	mux.HandleFunc("POST /desktop-reap", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			IdleSeconds int      `json:"idle_seconds"`
			Candidates  []string `json:"candidates"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input) != nil || input.IdleSeconds < 0 || input.IdleSeconds > 604800 {
			http.Error(w, "Invalid idle policy", 400)
			return
		}
		stopped := []string{}
		if input.IdleSeconds == 0 {
			json.NewEncoder(w).Encode(stopped)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		// Serialize selection with new requests; a desktop cannot acquire a viewer or
		// start work between its final idle check and shutdown.
		names := append([]string(nil), input.Candidates...)
		allowed := map[string]bool{}
		for _, name := range names {
			allowed[name] = true
		}
		for name, p := range sessions {
			if !allowed[name] {
				p.lastActivity = time.Now()
			}
		}
		sort.SliceStable(names, func(i, j int) bool {
			a, b := sessions[names[i]], sessions[names[j]]
			return a != nil && (b == nil || a.lastActivity.Before(b.lastActivity))
		})
		for _, name := range names {
			if r.Context().Err() != nil {
				break
			}
			p := sessions[name]
			if p == nil || !desktopIdleEligible(p, time.Now(), time.Duration(input.IdleSeconds)*time.Second) {
				continue
			}
			select {
			case <-p.done:
				delete(sessions, name)
				continue
			default:
			}
			idle, finished := desktopJobsIdle(name)
			if !idle {
				p.lastActivity = time.Now()
				continue
			}
			if finished.After(p.lastActivity) {
				p.lastActivity = finished
			}
			if !desktopIdleEligible(p, time.Now(), time.Duration(input.IdleSeconds)*time.Second) {
				continue
			}
			stopDesktopProcess(p)
			timer := time.NewTimer(12 * time.Second)
			select {
			case <-p.done:
				timer.Stop()
			case <-timer.C:
				syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
				<-p.done
			}
			delete(sessions, name)
			stopped = append(stopped, name)
		}
		json.NewEncoder(w).Encode(stopped)
	})
}

func desktopIdleEligible(p *desktopProcess, now time.Time, timeout time.Duration) bool {
	return timeout > 0 && p.requests == 0 && !p.lastActivity.IsZero() && now.Sub(p.lastActivity) >= timeout
}

// Query the private socket directly: a maintenance probe must not wake or touch
// a desktop's user-activity clock. Unknown/unreachable job state fails closed.
func desktopJobsIdle(name string) (bool, time.Time) {
	transport := desktopTransport(name)
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 2 * time.Second}
	res, err := client.Get("http://desktop/jobs")
	if err != nil {
		return false, time.Time{}
	}
	defer res.Body.Close()
	var state struct {
		ShellProcesses bool `json:"shell_processes_running"`
		Jobs           *[]struct {
			Running  bool      `json:"running"`
			Finished time.Time `json:"finished_at"`
		} `json:"jobs"`
	}
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&state) != nil || state.Jobs == nil || state.ShellProcesses {
		return false, time.Time{}
	}
	var finished time.Time
	for _, job := range *state.Jobs {
		if job.Finished.After(finished) {
			finished = job.Finished
		}
		if job.Running {
			return false, time.Time{}
		}
	}
	return true, finished
}
