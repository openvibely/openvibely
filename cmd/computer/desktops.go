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
	"os"
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
	cmd       *exec.Cmd
	port      int
	done      chan struct{}
	authMu    sync.Mutex
	authReady bool
}

func desktopRoutes(mux *http.ServeMux) {
	var mu sync.Mutex
	sessions := map[string]*desktopProcess{}
	next := 10000
	mux.HandleFunc("POST /desktop-shutdown", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		all := []*desktopProcess{}
		for _, p := range sessions {
			all = append(all, p)
			syscall.Kill(p.cmd.Process.Pid, syscall.SIGTERM)
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
		out := map[string]int{"shared": 9222}
		for name, p := range sessions {
			select {
			case <-p.done:
			default:
				out[name] = p.port + 1
			}
		}
		json.NewEncoder(w).Encode(out)
	})

	ensure := func(name string) (*desktopProcess, error) {
		mu.Lock()
		defer mu.Unlock()
		if p := sessions[name]; p != nil {
			select {
			case <-p.done:
				delete(sessions, name)
			default:
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
			return nil, fmt.Errorf("Desktop capacity reached (32 active task desktops)")
		}
		// Port slots are recycled only after every process in the old session stops.
		used := map[int]bool{}
		for _, p := range sessions {
			used[p.port] = true
		}
		next = 10000
		for used[next] {
			next += 10
		}
		p := &desktopProcess{port: next, done: make(chan struct{})}
		p.cmd = exec.Command("/usr/local/bin/start-computer")
		p.cmd.Env = append(os.Environ(), "OPENVIBELY_DESKTOP_ID="+name, "OPENVIBELY_DESKTOP_PORT="+strconv.Itoa(next))
		p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := p.cmd.Start(); err != nil {
			return nil, err
		}
		sessions[name] = p
		go func() { p.cmd.Wait(); syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL); close(p.done) }()
		return p, nil
	}
	mux.HandleFunc("/desktops/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/desktops/"), "/", 2)
		if len(parts) != 2 || !desktopName.MatchString(parts[0]) {
			http.Error(w, "Invalid desktop", 400)
			return
		}
		name := parts[0]
		if r.Method == "DELETE" && parts[1] == "" {
			mu.Lock()
			p := sessions[name]
			mu.Unlock()
			if p != nil {
				syscall.Kill(p.cmd.Process.Pid, syscall.SIGTERM)
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
		p, err := ensure(name)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		client := http.Client{Timeout: time.Second}
		target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", p.port))
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", target.String()+"/health", nil)
			req.Header.Set("Authorization", "Bearer "+os.Getenv("OPENVIBELY_COMPUTER_TOKEN"))
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
		p.authMu.Lock()
		if !p.authReady && !strings.HasPrefix(name, "task-") {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8092/sync?desktop="+name, nil)
			res, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
			if err != nil {
				p.authMu.Unlock()
				http.Error(w, "Sign-in sharing is starting; retry shortly", 503)
				return
			}
			res.Body.Close()
			if res.StatusCode != 204 {
				p.authMu.Unlock()
				http.Error(w, "Sign-in sharing unavailable", 503)
				return
			}
			p.authReady = true
		}
		p.authMu.Unlock()
		prefix := "/desktops/" + name
		host := r.Host
		proxy := httputil.NewSingleHostReverseProxy(target)
		director := proxy.Director
		proxy.Director = func(req *http.Request) { director(req); req.Host = target.Host; req.URL.Path = "/" + parts[1] }
		if strings.HasPrefix(parts[1], "json/") {
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
