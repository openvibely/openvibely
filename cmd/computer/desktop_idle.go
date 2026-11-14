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
	handler := func(w http.ResponseWriter, r *http.Request) {
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
		timeout := time.Duration(input.IdleSeconds) * time.Second
		names := append([]string(nil), input.Candidates...)
		allowed := map[string]bool{}
		for _, name := range names {
			allowed[name] = true
		}
		mu.Lock()
		for name, p := range sessions {
			if r.URL.Path != "/desktop-reap-one" && !allowed[name] {
				p.lastActivity = time.Now()
			}
		}
		sort.SliceStable(names, func(i, j int) bool {
			a, b := sessions[names[i]], sessions[names[j]]
			return a != nil && (b == nil || a.lastActivity.Before(b.lastActivity))
		})
		mu.Unlock()
		if r.URL.Path == "/desktop-idle-protection" {
			json.NewEncoder(w).Encode(stopped)
			return
		}
		for _, name := range names {
			if r.Context().Err() != nil {
				break
			}
			if reapDesktop(mu, sessions, name, timeout) {
				stopped = append(stopped, name)
			}
		}
		json.NewEncoder(w).Encode(stopped)
	}
	mux.HandleFunc("POST /desktop-reap-one", handler)
	mux.HandleFunc("POST /desktop-idle-protection", handler)
}

// reapDesktop stops one idle desktop. Only the shared lock's quick checks run
// under it; the job probe and shutdown run unlocked while the desktop is
// claimed, so requests for other desktops never wait on them. A request for
// this desktop cannot start work between the final idle check and shutdown:
// it waits on the claim, then starts a fresh desktop.
func reapDesktop(mu *sync.Mutex, sessions map[string]*desktopProcess, name string, timeout time.Duration) bool {
	mu.Lock()
	p := sessions[name]
	if p == nil || p.reaping != nil || !desktopIdleEligible(p, time.Now(), timeout) {
		mu.Unlock()
		return false
	}
	select {
	case <-p.done:
		delete(sessions, name)
		mu.Unlock()
		return false
	default:
	}
	claim := make(chan struct{})
	p.reaping = claim
	mu.Unlock()
	release := func() {
		p.reaping = nil
		close(claim)
		mu.Unlock()
	}

	idle, finished := desktopJobsIdleFn(name)
	mu.Lock()
	if !idle {
		p.lastActivity = time.Now()
		release()
		return false
	}
	if finished.After(p.lastActivity) {
		p.lastActivity = finished
	}
	if !desktopIdleEligible(p, time.Now(), timeout) {
		release()
		return false
	}
	mu.Unlock()

	stopDesktopProcess(p)
	timer := time.NewTimer(12 * time.Second)
	select {
	case <-p.done:
		timer.Stop()
	case <-timer.C:
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
	mu.Lock()
	if sessions[name] == p {
		delete(sessions, name)
	}
	release()
	return true
}

func desktopIdleEligible(p *desktopProcess, now time.Time, timeout time.Duration) bool {
	return timeout > 0 && p.requests == 0 && !p.lastActivity.IsZero() && now.Sub(p.lastActivity) >= timeout
}

var desktopJobsIdleFn = desktopJobsIdle

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
