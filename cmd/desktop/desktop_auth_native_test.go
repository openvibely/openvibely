//go:build darwin && openvibely_native_ui

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func TestDesktopAuthenticationInNativeWebView(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1 and a graphical macOS session")
	}
	backend := desktopAuthBackend(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "OPENVIBELY_AUTH_NATIVE_HELPER="+backend.BaseURL)
	testutil.ConfigureNativeBrowserStorage(t, cmd)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "DESKTOP AUTH PASS") {
		t.Fatalf("native authentication failed: %v\n%s", err, output)
	}
}

// AppKit must run on the process's main goroutine, not a test goroutine.
func TestMain(m *testing.M) {
	if backend := os.Getenv("OPENVIBELY_AUTH_NATIVE_HELPER"); backend != "" {
		if err := runDesktopAuthNativeHelper(backend); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

func runDesktopAuthNativeHelper(backend string) error {
	target, err := url.Parse(backend)
	if err != nil {
		return err
	}
	proxy := desktopAssetProxy(target)
	var app *application.App
	// Record document requests at the proxy. A redirect page may execute its
	// script before the WebView test hook gets to inspect the document.
	var navigationMu sync.Mutex
	var submitted bool
	var documents []string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__desktop_auth_result" {
			result := r.URL.Query().Get("result")
			navigationMu.Lock()
			if result == "PASS" && !slices.Equal(documents, []string{"GET /chat"}) {
				result = fmt.Sprintf("FAIL: documents requested after submitting login: %v", documents)
			}
			navigationMu.Unlock()
			fmt.Println("DESKTOP AUTH " + result)
			w.WriteHeader(http.StatusNoContent)
			app.Quit()
			return
		}
		navigationMu.Lock()
		if r.Method == http.MethodPost && r.URL.Path == "/login" {
			submitted = true
		}
		dest := r.Header.Get("Sec-Fetch-Dest")
		if submitted && (dest == "document" || (dest == "" && r.Header.Get("Upgrade-Insecure-Requests") == "1" && strings.Contains(r.Header.Get("Accept"), "text/html"))) {
			documents = append(documents, r.Method+" "+r.URL.RequestURI())
		}
		navigationMu.Unlock()
		proxy.ServeHTTP(w, r)
	})
	app = application.New(application.Options{
		Name:   "OpenVibely authentication test",
		Assets: application.AssetOptions{Handler: handler},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "auth-test", Title: "OpenVibely authentication test", URL: "/chat", Width: 1000, Height: 700,
		Frameless: true, Mac: application.MacWindow{CornerRadius: 20},
		JS: `(async () => {
  const report = result => fetch('/__desktop_auth_result?result=' + encodeURIComponent(result));
  try {
    if (document.readyState === 'loading') await new Promise(resolve => document.addEventListener('DOMContentLoaded', resolve, {once:true}));
    if (location.pathname === '/login') {
      // A redirect document can change location before Wails runs this hook.
      // Wait for the login document; navigation discards the old document's timer.
      for (let i = 0; i < 100 && !document.querySelector('form[action="/login"]'); i++) await new Promise(resolve => setTimeout(resolve, 50));
      const form = document.querySelector('form[action="/login"]');
      if (!form) throw Error('login form missing: ' + document.body.innerText.slice(0, 500));
      form.elements.username.value = 'alice';
      form.elements.password.value = 'incorrect-password';
      form.requestSubmit();
      const button = form.querySelector('button[type=submit]');
      for (let i = 0; i < 100 && !document.getElementById('login-error'); i++) await new Promise(resolve => setTimeout(resolve, 50));
      const error = document.getElementById('login-error');
      if (!error || error.textContent !== 'Incorrect username or password.' || !error.getClientRects().length) throw Error('missing visible credential error');
      if (!form.isConnected || button.disabled) throw Error('sign-in form unavailable for retry');
      form.elements.password.value = 'alice-password-long';
      form.requestSubmit();
    } else if (location.pathname === '/chat' && document.getElementById('main-content')) {
      const response = await fetch('/auth/me');
      const user = await response.json();
      if (!response.ok || user.username !== 'alice') throw Error('session missing');
      report('PASS');
    }
  } catch (error) { report('FAIL: ' + error.message); }
})();`,
	})
	registerPageBackground(app, window)
	return app.Run()
}
