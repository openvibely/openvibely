package skilldeps

import (
	"archive/tar"
	"bytes"
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func officeTar(t *testing.T, entries []*tar.Header) *bytes.Reader {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, h := range entries {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write(bytes.Repeat([]byte("x"), int(h.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(b.Bytes())
}
func TestOfficeDEBPayloadExtraction(t *testing.T) {
	root := t.TempDir()
	x := extractor{ctx: context.Background(), root: root, links: map[string]string{}}
	prefix := ""
	r := officeTar(t, []*tar.Header{
		{Name: "./opt/libreoffice26.8/program/soffice", Mode: 0755, Size: 1},
		{Name: "./opt/libreoffice26.8/program/soffice.bin", Mode: 0755, Size: 1},
		{Name: "./usr/share/applications/libreoffice.desktop", Mode: 0644, Size: 1},
		{Name: "./opt/libreoffice26.8/program/alias", Typeflag: tar.TypeSymlink, Linkname: "soffice"},
	})
	if err := extractOfficePayload(&x, r, &prefix); err != nil {
		t.Fatal(err)
	}
	if err := x.finishLinks(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"program/soffice", "program/soffice.bin", "program/alias"} {
		if b, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(b) != "x" {
			t.Fatalf("%s: %q %v", name, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "usr")); !os.IsNotExist(err) {
		t.Fatal("desktop integration was extracted")
	}
}
func TestOfficeDEBRejectsUnsafePayload(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "opt/libreoffice26.8/../../escape", Mode: 0644, Size: 1},
		{Name: "opt/libreoffice26.8/program/link", Typeflag: tar.TypeSymlink, Linkname: "../../escape"},
		{Name: "opt/libreoffice26.8/program/link", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"},
	} {
		t.Run(h.Name+string(h.Typeflag), func(t *testing.T) {
			x := extractor{ctx: context.Background(), root: t.TempDir(), links: map[string]string{}}
			prefix := ""
			if err := extractOfficePayload(&x, officeTar(t, []*tar.Header{h}), &prefix); err == nil {
				t.Fatal("unsafe payload accepted")
			}
		})
	}
}
func TestMoveOfficeMSITree(t *testing.T) {
	for _, prefix := range []string{"", "LibreOffice", "Program Files/LibreOffice"} {
		t.Run(prefix, func(t *testing.T) {
			stage, root := t.TempDir(), t.TempDir()
			for _, name := range []string{"program/soffice.exe", "program/soffice.bin", "share/registry/main.xcd"} {
				p := filepath.Join(stage, prefix, name)
				if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(name), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := moveOfficeTree(stage, root); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"program/soffice.exe", "program/soffice.bin", "share/registry/main.xcd"} {
				if b, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(b) != name {
					t.Fatalf("%s: %s %v", name, b, err)
				}
			}
		})
	}
}
func TestMoveOfficeMSIRejectsMissingOrAmbiguousTree(t *testing.T) {
	stage := t.TempDir()
	if err := moveOfficeTree(stage, t.TempDir()); err == nil {
		t.Fatal("empty MSI accepted")
	}
	for _, prefix := range []string{"one", "two"} {
		p := filepath.Join(stage, prefix, "program")
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "soffice.exe"), []byte("exe"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := moveOfficeTree(stage, t.TempDir()); err == nil {
		t.Fatal("ambiguous MSI accepted")
	}
}
func TestOfficeFormatsRequireMatchingPlatform(t *testing.T) {
	for _, tc := range []struct{ format, exe, platform string }{
		{"libreoffice-msi", "program/soffice.exe", "windows/arm64"},
		{"libreoffice-deb", "program/soffice", "linux/arm64"},
	} {
		d := Dependency{Name: "office", Version: "1.0.0", HealthCheck: []string{"--version"}, Packages: map[string]Package{tc.platform: {Format: tc.format, Executable: tc.exe, URL: "https://example.com/office", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
		if err := Validate([]Dependency{d}); err != nil {
			t.Fatal(err)
		}
		d.Packages["darwin/arm64"] = d.Packages[tc.platform]
		delete(d.Packages, tc.platform)
		if err := Validate([]Dependency{d}); err == nil {
			t.Fatal("wrong platform accepted")
		}
	}
}

// Opt-in real package test, run in a disposable native-platform environment.
func TestOfficePackageAcceptance(t *testing.T) {
	archive := os.Getenv("OPENVIBELY_TEST_OFFICE_BUNDLE")
	if archive == "" {
		t.Skip("set OPENVIBELY_TEST_OFFICE_BUNDLE to a verified native LibreOffice package")
	}
	platform := runtime.GOOS + "/" + runtime.GOARCH
	hashes := map[string]string{
		"linux/amd64":   "30903df3b9f61360d9660cd707de48cd2831469114492a5008ed58a0ac77d044",
		"linux/arm64":   "1b069c20dd237f6decad3ea02cb02f39fdf458b03d09c2a9facb7b9b71e6a27a",
		"windows/amd64": "c6298b10cfa1748bcbedad94c62ce771e240e5021bfd6d7a967cd571b04b5312",
		"windows/arm64": "97c2173752325af1da3d8731f41c8e09a7c4ad491cf55d9b1069fadb5668e6d7",
	}
	want, ok := hashes[platform]
	if !ok {
		t.Fatal("unsupported acceptance-test platform")
	}
	actual, err := fileHash(archive)
	if err != nil || actual != want {
		t.Fatalf("wrong archive checksum: %s %v", actual, err)
	}
	stage := t.TempDir()
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(stage, "package")
	if err := os.WriteFile(local, data, 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(stage, "content")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	format, exe := "libreoffice-deb", "program/soffice"
	if runtime.GOOS == "windows" {
		format, exe = "libreoffice-msi", "program/soffice.exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := prepare(ctx, Package{Format: format, Executable: exe}, local, root); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, filepath.FromSlash(exe))
	if err := health(ctx, executable, []string{"--headless", "--version"}); err != nil {
		output, _ := exec.CommandContext(ctx, executable, "--headless", "--version").CombinedOutput()
		t.Fatalf("%v: %s", err, output)
	}
	input := filepath.Join(stage, "sample.txt")
	if err := os.WriteFile(input, []byte("LibreOffice cache acceptance test"), 0600); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.ToSlash(filepath.Join(stage, "profile"))
	if runtime.GOOS == "windows" {
		profilePath = "/" + profilePath
	}
	profile := (&url.URL{Scheme: "file", Path: profilePath}).String()
	output, err := exec.CommandContext(ctx, executable, "--headless", "-env:UserInstallation="+profile, "--convert-to", "pdf", "--outdir", stage, input).CombinedOutput()
	if err != nil {
		t.Fatalf("convert: %s %v", output, err)
	}
	pdf, err := os.ReadFile(filepath.Join(stage, "sample.pdf"))
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("missing rendered PDF: %s %v", output, err)
	}
}
