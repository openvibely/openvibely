package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/web/templates/pages"
)

// The same scenarios run on every native OS/architecture in the CI matrix.
// Only the executable build and native window setup vary by platform.
func TestNativeDesktopNavigation(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("set OPENVIBELY_RUN_NATIVE_UI=1 with a native desktop session (Linux requires GTK3/WebKit and a display)")
	}
	dir := t.TempDir()
	for _, source := range []string{"desktop_proxy.go", "testdata/navigation_check.go"} {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(source)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	name := "navigation-check"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(dir, name)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	args := []string{"build"}
	if runtime.GOOS == "linux" {
		args = append(args, "-tags", "gtk3")
	}
	args = append(args, "-o", binary, filepath.Join(dir, "navigation_check.go"), filepath.Join(dir, "desktop_proxy.go"))
	command := exec.CommandContext(ctx, "go", args...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build native navigation check: %v\n%s", err, output)
	}
	var fixture bytes.Buffer
	fixture.WriteString(`<!doctype html><html><head><script src="/htmx.js"></script></head><body><main id="main-content">`)
	if err := pages.PluginSettingsContent(pages.PluginSettingsView{ProjectID: "navigation-test"}).Render(t.Context(), &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.WriteString(`</main><script>window.addEventListener('load',()=>{const dialog=document.getElementById('plugin-describe-modal');dialog.showModal();const form=dialog.querySelector('form');form.elements.title.value='Navigation test';form.elements.description.value='Test navigation';setTimeout(()=>form.requestSubmit(),100);});</script></body></html>`)
	fixturePath := filepath.Join(dir, "describe.html")
	if err := os.WriteFile(fixturePath, fixture.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	htmx, err := filepath.Abs("../../web/static/dist/vendor/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	authBackend := desktopAuthBackend(t)
	for _, mode := range []string{"auth", "login", "logout", "invite", "original-create", "create", "automation", "get-307", "organization", "absolute", "session-expired", "refresh", "external"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
			defer cancel()
			profile := testutil.NativeBrowserTempDir(t)
			command := exec.CommandContext(ctx, binary, mode, fixturePath, htmx, authBackend.BaseURL, profile)
			output, err := command.CombinedOutput()
			t.Logf("%s", output)
			if err != nil {
				t.Fatalf("native navigation failed on %s/%s: %v", runtime.GOOS, runtime.GOARCH, err)
			}
		})
	}
}
