package handler

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestPluginNativeAssetPolicy(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	for _, tc := range []struct {
		name            string
		desktop, native bool
	}{
		{"browser", false, false}, {"browser-on-desktop-server", true, false},
		{"untrusted-native-header", false, true}, {"native-window", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.h.desktopMode = tc.desktop
			headers := map[string]string{}
			if tc.native {
				headers["X-OpenVibely-Native-Window"] = "1"
			}
			rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/index.html", "", headers)
			if rec.Code != 200 {
				t.Fatal(rec.Body.String())
			}
			want := pluginAssetCSP
			if tc.desktop && tc.native {
				want = nativePluginAssetCSP(handle)
			}
			if got := rec.Header().Get("Content-Security-Policy"); got != want {
				t.Fatalf("policy: %s", got)
			}
		})
	}
	policy := nativePluginAssetCSP(handle)
	for _, directive := range []string{"script-src", "style-src", "img-src", "font-src", "media-src"} {
		if !strings.Contains(policy, directive+" 'self' wails://localhost/plugin-assets/"+handle+"/") {
			t.Fatalf("missing scoped %s", directive)
		}
	}
	for _, restriction := range []string{"sandbox allow-scripts allow-forms;", "connect-src 'none'", "frame-src 'none'", "frame-ancestors 'self'"} {
		if !strings.Contains(policy, restriction) {
			t.Fatalf("lost restriction %s", restriction)
		}
	}
}

// Exercise the actual macOS engine and custom scheme, rather than Chromium or
// an HTTP-backed native window: neither reproduces WebKit's CSP self mismatch.
func TestPluginNativeWebKitAssets(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires macOS graphical session and OPENVIBELY_RUN_NATIVE_UI=1")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "plugin-webkit")
	if out, err := exec.CommandContext(ctx, "clang", "-framework", "Cocoa", "-framework", "WebKit", "testdata/plugin_webkit.m", "-o", bin).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	out, err := exec.CommandContext(ctx, bin, nativePluginAssetCSP("test")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "REPORT SCRIPT LOADED") || strings.Contains(string(out), "REPORT CSP") {
		t.Fatalf("WebKit script failed: %v\n%s", err, out)
	}
}
