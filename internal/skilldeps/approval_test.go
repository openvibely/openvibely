package skilldeps

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func installationFixture(t *testing.T) (*Manager, Dependency, *atomic.Int32) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell executable fixture")
	}
	script := []byte("#!/bin/sh\nexit 0\n")
	downloads := new(atomic.Int32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { downloads.Add(1); w.Write(script) }))
	t.Cleanup(server.Close)
	platform := runtime.GOOS + "/" + runtime.GOARCH
	d := Dependency{Name: "tool", Version: "1.0.0", HealthCheck: []string{"--version"}, Packages: map[string]Package{platform: {URL: server.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(script)), Format: "raw", Executable: "tool"}}}
	return &Manager{CacheDir: filepath.Join(t.TempDir(), "cache"), Platform: platform, Client: server.Client()}, d, downloads
}
func testSkill() SkillIdentity {
	return SkillIdentity{ProjectID: "project-a", Source: "project", Skill: "example"}
}

func TestMissingDependenciesAskBeforeDownloading(t *testing.T) {
	m, d, downloads := installationFixture(t)
	asked := 0
	deny := errors.New("declined")
	_, err := m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, func(_ context.Context, q InstallationRequest) error {
		asked++
		if q.Platform != m.Platform || len(q.Dependencies) != 1 || q.Dependencies[0].Name != d.Name {
			t.Fatalf("wrong question: %+v", q)
		}
		if downloads.Load() != 0 {
			t.Fatal("download before answer")
		}
		return deny
	})
	if !errors.Is(err, deny) || asked != 1 || downloads.Load() != 0 {
		t.Fatalf("deny: %v asked=%d downloads=%d", err, asked, downloads.Load())
	}
	if _, err := os.Stat(m.CacheDir); !os.IsNotExist(err) {
		t.Fatalf("setup before answer: %v", err)
	}
	_, err = m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, nil)
	if !errors.Is(err, ErrApprovalRequired) || downloads.Load() != 0 {
		t.Fatalf("missing callback: %v", err)
	}
}

func TestInstalledDependenciesProceedWithoutPromptAcrossProjects(t *testing.T) {
	m, d, downloads := installationFixture(t)
	result, err := m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, func(context.Context, InstallationRequest) error { return nil })
	if err != nil || result[0].Error != "" || downloads.Load() != 1 {
		t.Fatalf("install: %+v %v", result, err)
	}
	for _, project := range []string{"project-a", "project-b"} {
		identity := testSkill()
		identity.ProjectID = project
		result, err = m.ResolveWithInstallPrompt(context.Background(), identity, []Dependency{d}, nil)
		if err != nil || result[0].Status != "cached" || result[0].Error != "" || downloads.Load() != 1 {
			t.Fatalf("installed dependency requested consent: %+v %v", result, err)
		}
	}
	// A changed version has no installed copy, so it asks before any new download.
	d.Version = "2.0.0"
	_, err = m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, nil)
	if !errors.Is(err, ErrApprovalRequired) || downloads.Load() != 1 {
		t.Fatalf("missing version downloaded: %v", err)
	}
}

func TestDamagedInstallationAsksBeforeRepair(t *testing.T) {
	m, d, downloads := installationFixture(t)
	result, err := m.Resolve(context.Background(), []Dependency{d})
	if err != nil || result[0].Error != "" {
		t.Fatalf("fixture install: %+v %v", result, err)
	}
	if err := os.WriteFile(result[0].Executable, []byte("damaged"), 0755); err != nil {
		t.Fatal(err)
	}
	_, err = m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, nil)
	if !errors.Is(err, ErrApprovalRequired) || downloads.Load() != 1 {
		t.Fatalf("repair without answer: %v", err)
	}
	result, err = m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, func(context.Context, InstallationRequest) error { return nil })
	if err != nil || result[0].Error != "" || downloads.Load() != 2 {
		t.Fatalf("allowed repair: %+v %v", result, err)
	}
}

func TestLocalOverrideDoesNotAskOrDownload(t *testing.T) {
	m, d, downloads := installationFixture(t)
	local := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(local, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	m.LookupEnv = func(string) string { return local }
	result, err := m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, nil)
	if err != nil || result[0].Status != "local" || result[0].Error != "" || downloads.Load() != 0 {
		t.Fatalf("local override: %+v %v", result, err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	result, err = m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, nil)
	if err != nil || result[0].Error == "" || downloads.Load() != 0 {
		t.Fatalf("broken override silently downloaded: %+v %v", result, err)
	}
}

func TestInstallationQuestionOnlyIncludesMissingDependencies(t *testing.T) {
	m, d, downloads := installationFixture(t)
	if result, err := m.Resolve(context.Background(), []Dependency{d}); err != nil || result[0].Error != "" {
		t.Fatalf("fixture: %+v %v", result, err)
	}
	missing := d
	missing.Name = "second"
	_, err := m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d, missing}, func(_ context.Context, q InstallationRequest) error {
		if len(q.Dependencies) != 1 || q.Dependencies[0].Name != "second" {
			t.Fatalf("question includes installed packages: %+v", q)
		}
		return ErrApprovalRequired
	})
	if !errors.Is(err, ErrApprovalRequired) || downloads.Load() != 1 {
		t.Fatalf("missing dependency: %v", err)
	}
}

func TestCancellationAndUnsupportedPlatformNeverDownload(t *testing.T) {
	m, d, downloads := installationFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := m.ResolveWithInstallPrompt(ctx, testSkill(), []Dependency{d}, func(context.Context, InstallationRequest) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || downloads.Load() != 0 {
		t.Fatalf("cancelled install: %v", err)
	}
	m.Platform = "unsupported/unsupported"
	result, err := m.ResolveWithInstallPrompt(context.Background(), testSkill(), []Dependency{d}, nil)
	if err != nil || result[0].Status != "unsupported" || !strings.Contains(result[0].Error, "no verified") || downloads.Load() != 0 {
		t.Fatalf("unsupported: %+v %v", result, err)
	}
}
