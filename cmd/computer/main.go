//go:build linux

// A Linux desktop bridge. Only the Go app accesses this loopback-published API.
package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var inputMu sync.Mutex
var keyPattern = regexp.MustCompile(`^[a-zA-Z0-9_+]{1,100}$`)

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	b, e := exec.CommandContext(ctx, name, args...).Output()
	if e != nil {
		return nil, fmt.Errorf("%s failed: %w", name, e)
	}
	return b, nil
}
func screenshot(ctx context.Context) ([]byte, error) {
	return command(ctx, "import", "-window", "root", "png:-")
}

// waitForDesktop is an explicit model-requested wait, never a readiness signal.
func waitForDesktop(ctx context.Context, values map[string]any) error {
	ms, err := number(values, "duration_ms", 10000)
	if err != nil {
		return err
	}
	duration, _ := strconv.Atoi(ms)
	timer := time.NewTimer(time.Duration(duration) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
func number(v map[string]any, key string, max int) (string, error) {
	n, ok := v[key].(float64)
	if !ok || n < 0 || n > float64(max) || n != float64(int(n)) {
		return "", fmt.Errorf("invalid %s", key)
	}
	return strconv.Itoa(int(n)), nil
}

// macOS Command shortcuts map to Control inside the Linux desktop.
func linuxKey(key string, raw any) (string, error) {
	aliases := map[string]string{"cmd": "ctrl", "command": "ctrl", "meta": "ctrl", "control": "ctrl", "option": "alt", "return": "Return", "enter": "Return", "escape": "Escape", "tab": "Tab", "delete": "BackSpace", "backspace": "BackSpace", "pageup": "Prior", "pagedown": "Next", "up": "Up", "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End", "space": "space"}
	parts := []string{}
	if raw != nil {
		modifiers, ok := raw.([]any)
		if !ok {
			return "", fmt.Errorf("invalid modifiers")
		}
		for _, m := range modifiers {
			value, ok := m.(string)
			if !ok {
				return "", fmt.Errorf("invalid modifier")
			}
			value = strings.ToLower(value)
			switch value {
			case "cmd", "command", "meta", "ctrl", "control", "shift", "alt", "option":
				parts = append(parts, value)
			default:
				return "", fmt.Errorf("invalid modifier")
			}
		}
	}
	parts = append(parts, strings.Split(key, "+")...)
	for i, p := range parts {
		if !keyPattern.MatchString(p) {
			return "", fmt.Errorf("invalid key")
		}
		if alias := aliases[strings.ToLower(p)]; alias != "" {
			parts[i] = alias
		}
	}
	return strings.Join(parts, "+"), nil
}
func action(ctx context.Context, name string, v map[string]any) (any, error) {
	inputMu.Lock()
	defer inputMu.Unlock()
	var args []string
	point := func(x, y string) ([]string, error) {
		a, e := number(v, x, 1439)
		if e != nil {
			return nil, e
		}
		b, e := number(v, y, 899)
		return []string{"mousemove", a, b}, e
	}
	switch name {
	case "get_desktop_state", "get_window_state":
	case "wait":
		if err := waitForDesktop(ctx, v); err != nil {
			return nil, err
		}
	case "get_screen_size":
		return map[string]int{"width": 1440, "height": 900}, nil
	case "get_cursor_position":
		b, e := command(ctx, "xdotool", "getmouselocation", "--shell")
		return string(b), e
	case "list_windows", "list_apps":
		b, e := command(ctx, "wmctrl", "-l")
		return string(b), e
	case "click", "double_click", "right_click":
		var e error
		args, e = point("x", "y")
		if e != nil {
			return nil, e
		}
		button := "1"
		if v["button"] == "right" || name == "right_click" {
			button = "3"
		}
		if v["button"] == "middle" {
			button = "2"
		}
		count := "1"
		if name == "double_click" || v["count"] == float64(2) {
			count = "2"
		}
		args = append(args, "click", "--repeat", count, "--delay", "100", button)
	case "drag":
		var e error
		args, e = point("from_x", "from_y")
		if e != nil {
			return nil, e
		}
		dest, e := point("to_x", "to_y")
		if e != nil {
			return nil, e
		}
		args = append(args, "mousedown", "1")
		args = append(args, dest...)
		args = append(args, "mouseup", "1")
	case "scroll":
		var e error
		args, e = point("x", "y")
		if e != nil {
			return nil, e
		}
		buttons := map[string]string{"up": "4", "down": "5", "left": "6", "right": "7"}
		direction, _ := v["direction"].(string)
		if buttons[direction] == "" {
			return nil, fmt.Errorf("invalid scroll direction")
		}
		amount, e := scrollAmount(v)
		if e != nil {
			return nil, e
		}
		args = append(args, "click", "--repeat", amount, "--delay", "30", buttons[direction])
	case "press_key", "hotkey":
		key, _ := v["key"].(string)
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("invalid key")
		}
		var e error
		key, e = linuxKey(key, v["modifiers"])
		if e != nil {
			return nil, e
		}
		args = []string{"key", "--clearmodifiers", key}
	case "type_text":
		text, _ := v["text"].(string)
		if text == "" || len(text) > 10000 {
			return nil, fmt.Errorf("enter up to 10000 characters")
		}
		c := exec.CommandContext(ctx, "xdotool", "type", "--clearmodifiers", "--file", "-")
		c.Stdin = strings.NewReader(text)
		if e := c.Run(); e != nil {
			return nil, fmt.Errorf("typing failed: %w", e)
		}
	default:
		return nil, fmt.Errorf("unsupported Linux computer action: %s", name)
	}
	if len(args) > 0 {
		if name != "press_key" && name != "hotkey" && v["modifiers"] != nil {
			chord, err := linuxKey("a", v["modifiers"])
			if err != nil {
				return nil, err
			}
			modifiers := strings.Split(chord, "+")
			modifiers = modifiers[:len(modifiers)-1]
			if len(modifiers) > 0 {
				if _, err = command(ctx, "xdotool", append([]string{"keydown"}, modifiers...)...); err != nil {
					return nil, err
				}
				defer func() {
					release, cancel := context.WithTimeout(context.Background(), time.Second)
					defer cancel()
					command(release, "xdotool", append([]string{"keyup"}, modifiers...)...)
				}()
			}
		}
		if _, e := command(ctx, "xdotool", args...); e != nil {
			return nil, e
		}
	}
	image, e := screenshot(ctx)
	if e != nil {
		return nil, e
	}
	return map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString(image)}}}, nil
}
func computerPort(offset int) string {
	base, _ := strconv.Atoi(os.Getenv("OPENVIBELY_DESKTOP_PORT"))
	if base == 0 {
		switch offset {
		case 0:
			return "8090"
		case 1:
			return "9222"
		case 2:
			return "5900"
		case 3:
			return "5901"
		case 4:
			return "6080"
		case 5:
			return "6081"
		case 6:
			return "9223"
		}
	}
	return strconv.Itoa(base + offset)
}
func server() http.Handler {
	mux := http.NewServeMux()
	workspaceRoutes(mux)
	browserRoutes(mux)
	if os.Getenv("OPENVIBELY_DESKTOP_PORT") == "" {
		desktopRoutes(mux)
	}
	publicBrowserProxy(mux)
	for route, port := range map[string]string{"/vnc/": computerPort(4), "/rfb-view": computerPort(4), "/rfb-control": computerPort(5)} {
		target, _ := url.Parse("http://127.0.0.1:" + port)
		proxy := httputil.NewSingleHostReverseProxy(target)
		original := proxy.Director
		mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
			original(r)
			r.Host = target.Host
			if route == "/vnc/" {
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/vnc")
			} else {
				r.URL.Path = "/websockify"
			}
			proxy.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		client := http.Client{Timeout: 3 * time.Second}
		b, e := client.Get("http://127.0.0.1:" + computerPort(1) + "/json/version")
		if e != nil {
			http.Error(w, "Browser is starting", 503)
			return
		}
		defer b.Body.Close()
		if b.StatusCode != 200 {
			http.Error(w, "Browser is starting", 503)
			return
		}
		for _, port := range []string{computerPort(2), computerPort(3), computerPort(4), computerPort(5), computerPort(6)} {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+port, 300*time.Millisecond)
			if err != nil {
				http.Error(w, "Desktop is starting", 503)
				return
			}
			conn.Close()
		}
		io.WriteString(w, `{"ready":true,"version":6,"features":["desktop","shell","files"]}`)
	})
	mux.HandleFunc("GET /screen", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		b, e := screenshot(ctx)
		if e != nil {
			http.Error(w, e.Error(), 502)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(b)
	})
	mux.HandleFunc("POST /action", func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Action string
			Values map[string]any
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 100000)).Decode(&p) != nil {
			http.Error(w, "Invalid action", 400)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		out, e := action(ctx, p.Action, p.Values)
		if e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
	target, _ := url.Parse("http://127.0.0.1:" + computerPort(1))
	proxy := httputil.NewSingleHostReverseProxy(target)
	director := proxy.Director
	proxy.Director = func(r *http.Request) { director(r); r.Host = "127.0.0.1:" + computerPort(1) }
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/json/") && !strings.HasPrefix(r.URL.Path, "/devtools/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/json/") {
			p := *proxy
			p.ModifyResponse = func(res *http.Response) error {
				b, e := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				if e != nil {
					return e
				}
				b = bytes.ReplaceAll(b, []byte("ws://127.0.0.1:"+computerPort(1)+"/"), []byte("ws://"+r.Host+"/"))
				b = bytes.ReplaceAll(b, []byte("ws://localhost:"+computerPort(1)+"/"), []byte("ws://"+r.Host+"/"))
				res.Body = io.NopCloser(bytes.NewReader(b))
				res.ContentLength = int64(len(b))
				res.Header.Set("Content-Length", strconv.Itoa(len(b)))
				return nil
			}
			p.ServeHTTP(w, r)
		} else {
			proxy.ServeHTTP(w, r)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		token := os.Getenv("OPENVIBELY_COMPUTER_TOKEN")
		local, _, _ := net.SplitHostPort(r.RemoteAddr)
		internal := net.ParseIP(local) != nil && net.ParseIP(local).IsLoopback()
		authorized := token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) == 1
		if r.Header.Get("Origin") != "" || (!authorized && !(internal && (host == "127.0.0.1" || host == "localhost" || host == "::1"))) {
			http.Error(w, "Use the application to access this computer", 403)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func main() {
	s := &http.Server{Addr: ":" + computerPort(0), Handler: server(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(s.ListenAndServe())
}

// Scroll amounts are wheel steps, not pixels. Keep each gesture bounded.
func scrollAmount(v map[string]any) (string, error) {
	n, exists := v["amount"]
	if !exists {
		return "3", nil
	}
	value, ok := n.(float64)
	if !ok || value <= 0 || value != float64(int(value)) {
		return "", fmt.Errorf("scroll amount must be a positive number of wheel steps (1–20)")
	}
	if value > 20 {
		value = 20
	}
	return strconv.Itoa(int(value)), nil
}

func publicBrowserProxy(mux *http.ServeMux) {
	target, _ := url.Parse("http://127.0.0.1:" + computerPort(6))
	mux.HandleFunc("/public-cdp/", func(w http.ResponseWriter, r *http.Request) {
		externalHost := r.Host
		proxy := httputil.NewSingleHostReverseProxy(target)
		director := proxy.Director
		proxy.Director = func(r *http.Request) {
			director(r)
			r.Host = target.Host
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/public-cdp")
		}
		if !strings.HasPrefix(r.URL.Path, "/public-cdp/json/") && !strings.HasPrefix(r.URL.Path, "/public-cdp/devtools/") {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/public-cdp/json/") {
			proxy.ModifyResponse = func(res *http.Response) error {
				b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
				res.Body.Close()
				if err != nil {
					return err
				}
				b = bytes.ReplaceAll(b, []byte("ws://127.0.0.1:9223/"), []byte("ws://"+externalHost+"/public-cdp/"))
				b = bytes.ReplaceAll(b, []byte("ws://localhost:9223/"), []byte("ws://"+externalHost+"/public-cdp/"))
				res.Body = io.NopCloser(bytes.NewReader(b))
				res.ContentLength = int64(len(b))
				res.Header.Set("Content-Length", strconv.Itoa(len(b)))
				return nil
			}
		}
		proxy.ServeHTTP(w, r)
	})
}
