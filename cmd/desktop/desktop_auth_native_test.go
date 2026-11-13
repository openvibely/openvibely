//go:build darwin && openvibely_native_ui

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
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
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__desktop_auth_result" {
			fmt.Println("DESKTOP AUTH " + r.URL.Query().Get("result"))
			w.WriteHeader(http.StatusNoContent)
			app.Quit()
			return
		}
		proxy.ServeHTTP(w, r)
	})
	app = application.New(application.Options{
		Name:   "OpenVibely authentication test",
		Assets: application.AssetOptions{Handler: handler},
		Mac:    application.MacOptions{ApplicationShouldTerminateAfterLastWindowClosed: true},
	})
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name: "auth-test", Title: "OpenVibely authentication test", URL: "/chat", Width: 1000, Height: 700,
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
	return app.Run()
}
