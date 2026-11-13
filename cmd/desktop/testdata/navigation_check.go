// Built with the production desktop_proxy.go by TestNativeDesktopNavigation.
// Each process owns a disposable window and fixture proxy. Authentication
// uses the isolated real backend started by the test. No running app is used.
package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
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
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/report" {
			select {
			case done <- struct{}{}:
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<!doctype html><html><body>External destination<script>fetch('/report')</script></body></html>`)
	}))
	defer external.Close()
	var authBackend http.Handler
	if mode == "auth" {
		target, err := url.Parse(os.Args[4])
		if err != nil {
			panic(err)
		}
		authBackend = httputil.NewSingleHostReverseProxy(target)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("REQUEST %s %s\n", r.Method, r.URL.Path)
		if mode == "auth" && r.URL.Path != "/report" {
			if r.URL.Path == "/start" {
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, nativeAuthDocument)
			} else if r.URL.Path == "/failure" {
				badForm.Store(true)
				fmt.Printf("AUTH FAILED: %s\n", r.URL.RawQuery)
				select {
				case done <- struct{}{}:
				default:
				}
			} else {
				if r.URL.Path == "/login" && r.Method == "POST" {
					submissions.Add(1)
				}
				authBackend.ServeHTTP(w, r)
			}
			return
		}
		switch r.URL.Path {
		case "/htmx.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Write(htmx)
		case "/login", "/logout", "/invite", "/tasks", "/automations/builder", "/organization", "/connect":
			submissions.Add(1)
			if r.Method != "POST" && mode != "get-307" {
				badForm.Store(true)
			}
			if mode == "create" && (r.FormValue("title") != "Navigation test" || r.FormValue("category") != "active" || !strings.Contains(r.FormValue("prompt"), "Requirements: Test navigation")) {
				badForm.Store(true)
			}
			http.SetCookie(w, &http.Cookie{Name: "navigation-test", Value: "accepted", Path: "/", HttpOnly: true})
			switch mode {
			case "create":
				w.Header().Set("HX-Location", `{"path":"/done","target":"#main-content","swap":"innerHTML"}`)
			case "session-expired":
				w.Header().Set("HX-Redirect", "/")
				w.WriteHeader(http.StatusUnauthorized)
			case "refresh":
				w.Header().Set("HX-Refresh", "true")
				w.WriteHeader(http.StatusNoContent)
			case "external":
				http.Redirect(w, r, external.URL+"/done", http.StatusFound)
			case "automation":
				w.Header().Set("HX-Redirect", "/done")
				w.WriteHeader(204)
			case "get-307":
				http.Redirect(w, r, "/done", 307)
			case "organization":
				w.Header().Set("HX-Location", `{"path":"/done","target":"#main-content","swap":"innerHTML"}`)
			case "absolute":
				http.Redirect(w, r, server.URL+"/done", 302)
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
			if mode == "refresh" && submissions.Load() == 1 {
				fmt.Fprint(w, `<!doctype html><html><body>Reloaded<script>fetch('/report')</script></body></html>`)
				return
			}
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
			switch mode {
			case "get-307":
				attr = `method="get" action="/connect"`
			case "organization":
				attr = `hx-post="/organization" hx-target="#main-content"`
			case "session-expired", "refresh":
				attr = `hx-post="/tasks" hx-target="#main-content"`
			case "external", "absolute":
				attr = `method="post" action="/login"`
			}
			fmt.Fprintf(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body><main id="main-content"><dialog id="modal"><form %s><input name="title" value="Test"><button type="submit">Submit</button></form></dialog></main><script>window.addEventListener('load',()=>{modal.showModal();setTimeout(()=>document.querySelector('form').requestSubmit(),100);});</script></body></html>`, attr)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	app := application.New(application.Options{
		Name:    "OpenVibely navigation test",
		Assets:  application.AssetOptions{Handler: desktopAssetProxy(target)},
		Mac:     application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
		Windows: application.WindowsOptions{WebviewUserDataPath: os.Args[5]},
	})
	// Use the production platform-specific window options.
	background := application.BackgroundTypeSolid
	if runtime.GOOS == "linux" {
		background = application.BackgroundTypeTransparent
	}
	win := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "probe", Title: "Navigation test", URL: "/start", Width: 450, Height: 250,
		Frameless: true, BackgroundType: background,
		Mac:     application.MacWindow{CornerRadius: 20},
		Windows: application.WindowsWindow{WebView2CompositionHosting: true},
	})
	var passed bool
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(*application.ApplicationEvent) {
		go func() {
			reached := false
			select {
			case <-done:
				reached = true
			case <-time.After(30 * time.Second):
			}
			time.AfterFunc(200*time.Millisecond, func() {
				application.InvokeAsync(func() {
					passed = reached && win.IsVisible() && submissions.Load() == 1 && !badForm.Load()
					fmt.Printf("RESULT platform=%s/%s mode=%s reached=%t visible=%t submissions=%d validForm=%t\n", runtime.GOOS, runtime.GOARCH, mode, reached, win.IsVisible(), submissions.Load(), !badForm.Load())
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

// Run against the actual application handlers, not the redirect fixtures above.
const nativeAuthDocument = `<!doctype html><html><body>Authentication check<script type="module">
(async()=>{
 const runtime = await import('/wails/runtime.js');
 if (!runtime.Window || !runtime.Events) throw Error('native runtime missing');
 await runtime.Events.Emit('desktop:auth-test', {});
 if(typeof await runtime.Window.IsMaximised() !== 'boolean') throw Error('native window API failed');
 async function request(path, form, expected) {
  const options = form === null ? {} : {method:'POST',headers:{'Content-Type':'application/x-www-form-urlencoded'},body:new URLSearchParams(form)};
  if(path === '/events/native-ticket') options.headers['X-OpenVibely-Live']='1';
  const r = await fetch(path, options);
  // WebView2 follows fetch redirects; custom-scheme WebKit may return the
  // original 302/303. In either case, verify the actual session below.
  const redirected = (expected === 302 || expected === 303) && r.status === 200 && r.redirected;
  if(r.status !== expected && !redirected) throw Error(path+': '+r.status+' expected '+expected+' or its followed redirect');
  return r.text();
 }
 await request('/login', {username:'alice',password:'alice-password-long'},302);
 const owner = JSON.parse(await request('/auth/me',null,200));
 if(owner.role !== 'owner') throw Error('owner session missing');
 await request('/events/native-ticket',{},200);
 const html = await request('/organization/invitations',{role:'member'},200);
 const token = html.match(/\/invite\?token=([A-Za-z0-9_-]+)/);
 if(!token) throw Error('invitation missing');
 await request('/logout',{},302);
 await request('/auth/me',null,401);
 await request('/invite',{token:token[1],username:'native-member',password:'native-member-password'},303);
 const member=JSON.parse(await request('/auth/me',null,200));
 if(member.role !== 'member') throw Error('member session missing');
 await request('/logout',{},302);
 await request('/auth/me',null,401);
 await runtime.Events.Emit('desktop:auth-test', {});
 if(typeof await runtime.Window.IsMaximised() !== 'boolean') throw Error('native window API failed');
 await fetch('/report');
})().catch(e=>fetch('/failure?error='+encodeURIComponent(e.message)));
</script></body></html>`
