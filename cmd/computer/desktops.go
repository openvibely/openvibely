//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var desktopName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

type desktopProcess struct {
	initPID      int
	cmd          *exec.Cmd
	uid          uint32
	done         chan struct{}
	lastActivity time.Time
	requests     int
	// reaping is set while idle cleanup checks or stops this desktop and is
	// closed when it finishes; requests for this desktop wait on it.
	reaping chan struct{}
}

func desktopRoutes(mux *http.ServeMux) {
	var mu sync.Mutex
	sessions := map[string]*desktopProcess{}
	registerDesktopReaper(mux, &mu, sessions)
	mux.HandleFunc("POST /desktop-shutdown", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		all := []*desktopProcess{}
		for _, p := range sessions {
			all = append(all, p)
			stopDesktopProcess(p)
		}
		mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		for _, p := range all {
			select {
			case <-p.done:
			case <-ctx.Done():
				syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /desktop-sessions", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]bool{}
		for name, p := range sessions {
			select {
			case <-p.done:
			default:
				out[name] = true
			}
		}
		json.NewEncoder(w).Encode(out)
	})

	ensure := func(ctx context.Context, name string, activity bool) (*desktopProcess, error) {
		mu.Lock()
		defer mu.Unlock()
		// Status polling must neither wake a stopped desktop nor delay shutdown.
		if !activity {
			p := sessions[name]
			if p == nil || p.reaping != nil {
				return nil, nil
			}
			select {
			case <-p.done:
				return nil, nil
			default:
			}
		}
		for {
			p := sessions[name]
			if p == nil || p.reaping == nil {
				break
			}
			reaping := p.reaping
			mu.Unlock()
			select {
			case <-reaping:
			case <-ctx.Done():
				mu.Lock()
				return nil, fmt.Errorf("Desktop is stopping; retry shortly")
			}
			mu.Lock()
		}
		if p := sessions[name]; p != nil {
			select {
			case <-p.done:
				delete(sessions, name)
			default:
				p.requests++
				if activity {
					p.lastActivity = time.Now()
				}
				return p, nil
			}
		}
		for key, p := range sessions {
			select {
			case <-p.done:
				delete(sessions, key)
			default:
			}
		}
		if len(sessions) >= 32 {
			return nil, fmt.Errorf("All 32 desktop slots are occupied. Close unused viewers or wait for the idle timeout; active desktops are kept running.")
		}
		uid := desktopUID(name)
		for key, existing := range sessions {
			if key != name && existing.uid == uid {
				return nil, fmt.Errorf("desktop identity collision")
			}
		}
		p, err := startIsolatedDesktop(name)
		if err != nil {
			return nil, err
		}
		p.uid = uid
		p.requests = 1
		p.lastActivity = time.Now()
		sessions[name] = p
		return p, nil
	}
	mux.HandleFunc("/desktops/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/desktops/"), "/", 2)
		if len(parts) != 2 || !desktopName.MatchString(parts[0]) || !strings.HasPrefix(parts[0], "task-") {
			http.Error(w, "Invalid desktop", 400)
			return
		}
		name := parts[0]
		if r.Method == "DELETE" && parts[1] == "" {
			mu.Lock()
			p := sessions[name]
			mu.Unlock()
			if p != nil {
				stopDesktopProcess(p)
				select {
				case <-p.done:
				case <-time.After(12 * time.Second):
					syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
					<-p.done
				}
				mu.Lock()
				if sessions[name] == p {
					delete(sessions, name)
				}
				mu.Unlock()
			}
			w.WriteHeader(204)
			return
		}
		activity := !(r.Method == http.MethodGet && parts[1] == "jobs")
		p, err := ensure(r.Context(), name, activity)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		if p == nil {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"jobs":[],"shell_processes_running":false}`)
			return
		}
		defer func() {
			mu.Lock()
			p.requests--
			if activity {
				p.lastActivity = time.Now()
			}
			mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		transport := desktopTransport(name)
		defer transport.CloseIdleConnections()
		client := http.Client{Timeout: time.Second, Transport: transport}
		target, _ := url.Parse("http://desktop")
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", target.String()+"/health", nil)
			res, err := client.Do(req)
			ready := false
			if err == nil {
				ready = res.StatusCode == 200
				res.Body.Close()
			}
			if ready {
				break
			}
			select {
			case <-p.done:
				http.Error(w, "Desktop failed to start", 503)
				return
			case <-ctx.Done():
				http.Error(w, "Desktop is starting; retry shortly", 503)
				return
			case <-time.After(150 * time.Millisecond):
			}
		}
		prefix := "/desktops/" + name
		host := r.Host
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.Transport = transport
		director := proxy.Director
		proxy.Director = func(req *http.Request) { director(req); req.Host = target.Host; req.URL.Path = "/" + parts[1] }
		if strings.HasPrefix(parts[1], "json/") || strings.HasPrefix(parts[1], "public-cdp/json/") {
			proxy.ModifyResponse = func(res *http.Response) error {
				data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				if err != nil {
					return err
				}
				data = bytes.ReplaceAll(data, []byte("ws://"+target.Host+"/"), []byte("ws://"+host+prefix+"/"))
				res.Body = io.NopCloser(bytes.NewReader(data))
				res.ContentLength = int64(len(data))
				res.Header.Set("Content-Length", strconv.Itoa(len(data)))
				return nil
			}
		}
		proxy.ServeHTTP(w, r)
	})
}
