package skilldeps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Opt-in acceptance for the exact pinned macOS bundle, without downloading in CI.
func TestMacOfficeBundleAcceptance(t *testing.T) {
	archive := os.Getenv("OPENVIBELY_TEST_OFFICE_DMG")
	fixtures := os.Getenv("OPENVIBELY_TEST_OFFICE_FIXTURES")
	if archive == "" || fixtures == "" {
		t.Skip("set OPENVIBELY_TEST_OFFICE_DMG and OPENVIBELY_TEST_OFFICE_FIXTURES")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("pinned bundle is darwin/arm64")
	}
	const hash = "ca074e0b13571efb30a31fc531c5f95e997030637325e5d5de92d67aefdf0dfc"
	actual, err := fileHash(archive)
	if err != nil || actual != hash {
		t.Fatalf("bundle checksum: %s %v", actual, err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.ServeFile(w, r, archive) }))
	defer server.Close()
	m := &Manager{CacheDir: t.TempDir(), Client: server.Client()}
	dep := Dependency{Name: "libreoffice", Version: "26.8.1", HealthCheck: []string{"--headless", "--version"}, Packages: map[string]Package{"darwin/arm64": {URL: server.URL, SHA256: hash, Format: "dmg", Executable: "LibreOffice.app/Contents/MacOS/soffice"}}}
	r, err := m.Resolve(context.Background(), []Dependency{dep})
	if err != nil || len(r) != 1 || r[0].Error != "" {
		t.Fatalf("install: %v %v", r, err)
	}
	for _, extension := range []string{"docx", "xlsx", "pptx"} {
		t.Run(extension, func(t *testing.T) {
			profile := &url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(t.TempDir(), "profile"))}
			output := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, r[0].Executable, "--headless", "-env:UserInstallation="+profile.String(), "--convert-to", "pdf", "--outdir", output, filepath.Join(fixtures, "sample."+extension))
			if log, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("convert: %s %v", log, err)
			}
			data, err := os.ReadFile(filepath.Join(output, "sample.pdf"))
			if err != nil || !strings.HasPrefix(string(data), "%PDF-") {
				t.Fatalf("missing PDF: %v", err)
			}
		})
	}
	server.Close()
	r, err = m.Resolve(context.Background(), []Dependency{dep})
	if err != nil || r[0].Status != "cached" {
		t.Fatalf("offline cache: %v %v", r, err)
	}
}
