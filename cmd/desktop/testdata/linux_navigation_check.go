// Built with the production desktop_proxy.go by TestNativeLinuxNavigation.
// Each process owns only a disposable window and a fake backend; it does not
// connect to the running app or send keyboard/pointer events to the desktop.
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

func main() {
	mode, fixturePath, htmxPath := os.Args[1], os.Args[2], os.Args[3]
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		panic(err)
	}
	htmx, err := os.ReadFile(htmxPath)
	if err != nil {
		panic(err)
	}
	done := make(chan struct{}, 1)
	var submissions atomic.Int32
	var badForm atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("REQUEST %s %s\n", r.Method, r.URL.Path)
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Write(htmx)
		case "/login", "/logout", "/invite", "/tasks", "/automations/builder":
			submissions.Add(1)
			if r.Method != "POST" {
				badForm.Store(true)
			}
			if mode == "create" && (r.FormValue("title") != "Navigation test" || r.FormValue("category") != "active" || !strings.Contains(r.FormValue("prompt"), "Requirements: Test navigation")) {
				badForm.Store(true)
			}
			http.SetCookie(w, &http.Cookie{Name: "navigation-test", Value: "accepted", Path: "/", HttpOnly: true})
			switch mode {
			case "create":
				w.Header().Set("HX-Location", `{"path":"/done","target":"#main-content","swap":"innerHTML"}`)
			case "automation":
				w.Header().Set("HX-Redirect", "/done")
				w.WriteHeader(204)
			case "original-create":
				http.Redirect(w, r, "/done", 303)
			default:
				http.Redirect(w, r, "/", 302)
			}
		case "/":
			http.Redirect(w, r, "/done", 303) // Login's default target redirects again.
		case "/done":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<!doctype html><html><body><p id="done">Destination loaded</p><script>fetch('/report')</script></body></html>`)
		case "/report":
			select {
			case done <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
		default:
			w.Header().Set("Content-Type", "text/html")
			if mode == "create" {
				w.Write(fixture)
				return
			}
			endpoint := "/" + mode
			if mode == "original-create" {
				endpoint = "/tasks"
			}
			attr := `method="post" action="` + endpoint + `"`
			if mode == "automation" {
				attr = `hx-post="/automations/builder" hx-target="#main-content"`
			}
			fmt.Fprintf(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body><main id="main-content"><dialog id="modal"><form %s><input name="title" value="Test"><button type="submit">Submit</button></form></dialog></main><script>window.addEventListener('load',()=>{modal.showModal();setTimeout(()=>document.querySelector('form').requestSubmit(),100);});</script></body></html>`, attr)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	app := application.New(application.Options{Name: "OpenVibely navigation test", Assets: application.AssetOptions{Handler: desktopAssetProxy(target)}})
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{Name: "probe", Title: "Navigation test", URL: "/start", Width: 450, Height: 250, Frameless: true, BackgroundType: application.BackgroundTypeTransparent})
	var passed bool
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			reached := false
			select {
			case <-done:
				reached = true
			case <-time.After(10 * time.Second):
			}
			time.AfterFunc(200*time.Millisecond, func() {
				application.InvokeAsync(func() {
					passed = reached && win.IsVisible() && submissions.Load() == 1 && !badForm.Load()
					fmt.Printf("RESULT mode=%s reached=%t visible=%t submissions=%d validForm=%t\n", mode, reached, win.IsVisible(), submissions.Load(), !badForm.Load())
					app.Quit()
				})
			})
		}()
	})
	if err := app.Run(); err != nil {
		panic(err)
	}
	if !passed {
		os.Exit(1)
	}
}
