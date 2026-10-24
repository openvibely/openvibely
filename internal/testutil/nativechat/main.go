//go:build openvibely_native_ui

// Nativechat is a test-only Wails host for the real HTTP task-thread fixtures.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

type wheel struct {
	X, Y, DX, DY, Scale float64
	Count               int
	phase               int
}

func main() {
	started := time.Now()
	stage := func(message string) {
		log.Printf("[native-test] %s %s", time.Since(started).Round(time.Millisecond), message)
	}
	stage("host entry")
	target, err := url.Parse(os.Args[len(os.Args)-1])
	if err != nil || target.Scheme != "http" || target.Hostname() != "127.0.0.1" {
		log.Fatal("expected loopback fixture URL")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	stage("before application.New")
	app := application.New(application.Options{Name: "Native chat regression", Mac: application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true}})
	stage("after application.New")
	endpoint := "http://" + listener.Addr().String() + "/wheel"
	bridge := fmt.Sprintf(`window.nativeTestInput={postMessage(data){return fetch(%q,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({...data,scale:window.devicePixelRatio||1})}).then(r=>{if(!r.ok)return r.text().then(message=>{throw Error('Native input failed: '+r.status+' '+message)});if(data.count)window._nativeBurstDone=true}).catch(e=>{fetch('/browser-result?status='+encodeURIComponent(String(e)));throw e})}};`, endpoint)
	// URL-backed Wails windows do not consistently apply Options.JS (WebView2
	// only uses it for HTML-backed windows). Register it with the fixture before
	// navigating, so every engine receives the identical script in its document.
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Post(target.Scheme+"://"+target.Host+"/native-test-bridge", "text/javascript", strings.NewReader(bridge))
	if err != nil {
		log.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		log.Fatalf("register native bridge: %s", response.Status)
	}
	stage("bridge registered; before window creation")
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{Name: "native-chat-test", Title: "Native chat regression", URL: target.String(), Width: 1280, Height: 800, Windows: application.WindowsWindow{WebView2CompositionHosting: true}})
	stage("after window creation")
	var input sync.Mutex
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != target.Scheme+"://"+target.Host {
			http.Error(w, "fixture origin required", 403)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", r.Header.Get("Origin"))
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		if r.Method != "POST" || r.URL.Path != "/wheel" {
			http.NotFound(w, r)
			return
		}
		var event wheel
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&event) != nil || event.Count < 0 || event.Count > 1000 {
			http.Error(w, "invalid wheel", 400)
			return
		}
		input.Lock()
		defer input.Unlock()
		count := event.Count
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			if r.Context().Err() != nil {
				return
			}
			event.phase = 2 // changed
			if i == 0 {
				event.phase = 1 // began
			}
			var dispatchErr error
			application.InvokeSync(func() { dispatchErr = nativeWheel(window.NativeWindow(), event) })
			if dispatchErr != nil {
				http.Error(w, dispatchErr.Error(), http.StatusServiceUnavailable)
				return
			}
			if event.Count > 0 {
				time.Sleep(8 * time.Millisecond)
			}
		}
		// End the gesture without adding movement. Platforms without gesture
		// phases treat this zero-delta dispatch as a no-op.
		event.DX, event.DY, event.phase = 0, 0, 4
		var dispatchErr error
		application.InvokeSync(func() { dispatchErr = nativeWheel(window.NativeWindow(), event) })
		if dispatchErr != nil {
			http.Error(w, dispatchErr.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(204)
	})}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Print(err)
		}
	}()
	defer server.Close()
	stage("before app.Run")
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
