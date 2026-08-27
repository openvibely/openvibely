package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/buildinfo"
	wailsupdater "github.com/wailsapp/wails/v3/pkg/updater"
)

func TestDesktopInstallerReplacesCompleteBundleAndRollsBack(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("desktop app bundle installer is macOS-specific")
	}
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	staged := filepath.Join(root, "stage", "OpenVibely.app")
	if err := os.MkdirAll(filepath.Join(installed, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "Contents", "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(staged, "Contents", "Resources"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "Contents", "new"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	installer := &DesktopInstaller{}
	value := LocalStagedUpdate{ArtifactPath: staged, InstallPath: installed, BackupPath: installed + ".openvibely-backup", Version: "0.6.0"}
	if err := installer.Apply(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(installed, "Contents", "new")); err != nil {
		t.Fatal("complete new bundle not installed:", err)
	}
	if _, err := os.Stat(filepath.Join(installed, "Contents", "old")); !os.IsNotExist(err) {
		t.Fatal("old bundle contents leaked into replacement")
	}
	if err := installer.Rollback(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(installed, "Contents", "old")); err != nil {
		t.Fatal("old bundle not restored:", err)
	}
}

func TestWailsInstallerRetainsAndRestoresCompleteBundle(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	backup := installed + ".openvibely-backup"
	if err := os.MkdirAll(filepath.Join(installed, "Contents", "Frameworks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "Contents", "old"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../old", filepath.Join(installed, "Contents", "Frameworks", "old-link")); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{InstallPath: installed, BackupPath: backup, Version: "0.6.0"}
	if err := retainDesktopBundle(staged, filepath.Join(root, "AppData")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(installed); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(installed, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "Contents", "wrong-version"), []byte("wrong"), 0o644); err != nil {
		t.Fatal(err)
	}
	relaunched := ""
	installer := &WailsInstaller{ProtectedDataPaths: []string{filepath.Join(root, "AppData")}, Relaunch: func(path string) error { relaunched = path; return nil }}
	if err := installer.Rollback(context.Background(), staged); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(installed, "Contents", "old")); err != nil || string(data) != "old" {
		t.Fatalf("old bundle not restored: data=%q err=%v", data, err)
	}
	if target, err := os.Readlink(filepath.Join(installed, "Contents", "Frameworks", "old-link")); err != nil || target != filepath.FromSlash("../old") {
		t.Fatalf("bundle symlink not restored: target=%q err=%v", target, err)
	}
	if relaunched != installed {
		t.Fatalf("relaunched %q", relaunched)
	}
}

func TestWailsBundleReplacementAndRollbackPreserveApplicationData(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "Applications", "OpenVibely.app")
	appData := filepath.Join(root, "Application Data", "OpenVibely")
	for path, data := range map[string]string{
		"openvibely.db":                  "sqlite-database-bytes",
		"openvibely.db-wal":              "sqlite-wal-bytes",
		"projects/project-1/config.json": `{"name":"project"}`,
		"memories/profile.md":            "memory bytes",
		"skills/reviewer/SKILL.md":       "skill bytes",
		"agents/reviewer.yaml":           "agent bytes",
		"config.env":                     "MODEL=example",
		"credentials/provider.token":     "secret-token-bytes",
	} {
		full := filepath.Join(appData, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(installed, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "Contents", "MacOS", "OpenVibely"), []byte("old-app"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := snapshotTestDirectory(t, appData)
	staged := LocalStagedUpdate{InstallPath: installed, BackupPath: installed + ".openvibely-backup", Version: "0.6.0"}

	if err := retainDesktopBundle(staged, appData); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(installed); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(installed, "Contents", "MacOS"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "Contents", "MacOS", "OpenVibely"), []byte("new-app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if after := snapshotTestDirectory(t, appData); !reflect.DeepEqual(after, before) {
		t.Fatalf("application data changed during successful bundle replacement:\nbefore=%#v\nafter=%#v", before, after)
	}

	if err := restoreDesktopBundle(staged, appData); err != nil {
		t.Fatal(err)
	}
	if after := snapshotTestDirectory(t, appData); !reflect.DeepEqual(after, before) {
		t.Fatalf("application data changed during bundle rollback:\nbefore=%#v\nafter=%#v", before, after)
	}
}

type testDirectoryEntry struct {
	Mode os.FileMode
	Data string
	Link string
}

func snapshotTestDirectory(t *testing.T, root string) map[string]testDirectoryEntry {
	t.Helper()
	snapshot := make(map[string]testDirectoryEntry)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		record := testDirectoryEntry{Mode: info.Mode()}
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			record.Link, err = os.Readlink(path)
		case entry.Type().IsRegular():
			var data []byte
			data, err = os.ReadFile(path)
			record.Data = string(data)
		}
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(relative)] = record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestWailsBundleOperationsRejectApplicationDataInsideBundle(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	appData := filepath.Join(installed, "Contents", "UserData")
	if err := os.MkdirAll(appData, 0o700); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{InstallPath: installed, BackupPath: installed + ".openvibely-backup", Version: "0.6.0"}
	if err := retainDesktopBundle(staged, appData); err == nil {
		t.Fatal("bundle backup accepted application data inside the replaceable bundle")
	}
	if err := restoreDesktopBundle(staged, appData); err == nil {
		t.Fatal("bundle rollback accepted application data inside the replaceable bundle")
	}

	externalLink := filepath.Join(root, "AppData")
	if err := os.Symlink(appData, externalLink); err != nil {
		t.Skipf("application-data symlink fixture unavailable: %v", err)
	}
	if err := retainDesktopBundle(staged, externalLink); err == nil {
		t.Fatal("bundle backup accepted an external application-data symlink into the replaceable bundle")
	}
}

func TestAppBundleUpdateHelperHandoffPassesExecutableRelativeMetadata(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	executable := filepath.Join(installed, "Contents", "MacOS", "OpenVibely")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("signed-desktop-executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{
		InstallPath: installed, ArtifactPath: installed + ".openvibely-new", BackupPath: installed + ".openvibely-backup",
		Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "desktop-operation-1",
	}
	relative, err := desktopExecutableRelative(staged.InstallPath, executable)
	if err != nil {
		t.Fatal(err)
	}
	var helper *exec.Cmd
	if err := startPackagedUpdateHelperHandoff(context.Background(), packagedUpdateHelperHandoffConfig{
		Staged:           staged,
		HelperSourcePath: executable,
		CommandName:      AppBundleUpdateHelperCommand,
		HealthURL:        "http://127.0.0.1/health",
		RelaunchMetadata: packagedUpdateRelaunchMetadata{
			Arguments:          []string{executable, "--from-user"},
			WorkingDirectory:   root,
			ExecutableRelative: filepath.ToSlash(relative),
		},
		MetadataTransport:  packagedHelperMetadataStdin,
		StartHelper:        func(cmd *exec.Cmd) error { helper = cmd; return nil },
		AwaitHelperHandoff: acknowledgePackagedUpdateHelperForTest,
	}); err != nil {
		t.Fatal(err)
	}
	if helper == nil {
		t.Fatal("helper was not started")
	}
	if strings.Contains(strings.Join(helper.Args, "\x00"), "--relaunch-metadata") {
		t.Fatalf("app-bundle update helper used metadata file argv: %q", helper.Args)
	}
	var metadata packagedUpdateRelaunchMetadata
	if err := json.NewDecoder(helper.Stdin).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.ExecutableRelative != "Contents/MacOS/OpenVibely" {
		t.Fatalf("executable_relative = %q", metadata.ExecutableRelative)
	}
	if runtime.GOOS == "darwin" {
		if helper.Path != executable {
			t.Fatalf("macOS app helper path = %q, want signed bundle executable %q", helper.Path, executable)
		}
		if _, err := os.Stat(packagedUpdateHelperPath(installed, AppBundleUpdateHelperCommand)); !os.IsNotExist(err) {
			t.Fatalf("macOS app helper should not be copied outside signed bundle: %v", err)
		}
	} else if data, err := os.ReadFile(packagedUpdateHelperPath(installed, AppBundleUpdateHelperCommand)); err != nil || string(data) != "signed-desktop-executable" {
		t.Fatalf("helper copy = %q, err = %v", data, err)
	}
}

func TestDesktopExecutableRelativeResolvesParentSymlinks(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	installPath := filepath.Join(realRoot, "OpenVibely.app")
	executable := filepath.Join(installPath, "Contents", "MacOS", "OpenVibely")
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("desktop"), 0o755); err != nil {
		t.Fatal(err)
	}
	aliasRoot := filepath.Join(root, "alias")
	if err := os.Symlink(realRoot, aliasRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	relative, err := desktopExecutableRelative(filepath.Join(aliasRoot, "OpenVibely.app"), executable)
	if err != nil {
		t.Fatal(err)
	}
	if relative != filepath.Join("Contents", "MacOS", "OpenVibely") {
		t.Fatalf("relative executable = %q", relative)
	}
}

func TestPackagedUpdateHelperRunsInPlaceForMacAppBundles(t *testing.T) {
	for _, test := range []struct {
		name        string
		goos        string
		installPath string
		want        bool
	}{
		{name: "macOS app bundle", goos: "darwin", installPath: "/Applications/OpenVibely.app", want: true},
		{name: "macOS standalone binary", goos: "darwin", installPath: "/usr/local/bin/openvibely", want: false},
		{name: "Linux desktop executable", goos: "linux", installPath: "/home/me/.local/share/openvibely/bin/openvibely-desktop", want: false},
		{name: "Windows desktop executable", goos: "windows", installPath: `C:\Users\me\AppData\Local\Programs\OpenVibely Desktop\openvibely-desktop.exe`, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := runPackagedUpdateHelperInPlace(test.goos, test.installPath); got != test.want {
				t.Fatalf("runPackagedUpdateHelperInPlace() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWailsInstallerApplyUsesJournaledHealthValidatingHelper(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "openvibely-desktop")
	staged := LocalStagedUpdate{
		InstallPath: installed, ArtifactPath: installed + ".openvibely-new", BackupPath: installed + ".openvibely-backup",
		Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "desktop-operation-1",
	}
	if err := os.WriteFile(installed, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged.ArtifactPath, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	shutdown := false
	var helper *exec.Cmd
	installer := &WailsInstaller{
		ProtectedDataPaths: []string{filepath.Join(root, "data")}, HealthURL: "http://127.0.0.1/health",
		Arguments: []string{installed, "--user-argument"}, WorkingDirectory: root,
		StartHelper:        func(cmd *exec.Cmd) error { helper = cmd; return nil },
		awaitHelperHandoff: acknowledgePackagedUpdateHelperForTest,
		Shutdown:           func() { shutdown = true },
	}
	if err := installer.Apply(context.Background(), staged); err != nil {
		t.Fatal(err)
	}
	if helper == nil || helper.Args[1] != AppBundleUpdateHelperCommand || shutdown {
		t.Fatalf("helper=%#v shutdown=%v", helper, shutdown)
	}
	installer.ShutdownForRestart()
	if !shutdown {
		t.Fatal("shutdown was not requested after apply returned")
	}
	if strings.Contains(strings.Join(helper.Args, "\x00"), "--user-argument") {
		t.Fatal("desktop relaunch arguments leaked into helper argv")
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil || outcome.State != packagedUpdateOutcomeAuthorized {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
}

func TestWailsInstallUnitSupportsNativeDesktopLayouts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		install string
		isDir   bool
	}{
		{name: "macOS app bundle", install: "OpenVibely.app", isDir: true},
		{name: "Windows executable", install: "openvibely-desktop.exe"},
		{name: "Linux executable", install: "openvibely-desktop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			installed := filepath.Join(root, tc.install)
			if tc.isDir {
				if err := os.MkdirAll(filepath.Join(installed, "Contents", "MacOS"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(installed, "Contents", "MacOS", "OpenVibely"), []byte("old"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(installed, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			protectedRoot := filepath.Join(root, "data")
			protectedFile := filepath.Join(protectedRoot, "openvibely.db")
			if err := os.MkdirAll(protectedRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(protectedFile, []byte("user-data"), 0o600); err != nil {
				t.Fatal(err)
			}
			before := snapshotTestDirectory(t, protectedRoot)
			staged := LocalStagedUpdate{InstallPath: installed, BackupPath: installed + ".openvibely-backup", Version: "0.6.0"}
			protected := []string{protectedRoot}
			if err := retainDesktopInstallUnit(staged, protected); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(installed); err != nil {
				t.Fatal(err)
			}
			if tc.isDir {
				if err := os.MkdirAll(filepath.Join(installed, "Contents", "MacOS"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(installed, "Contents", "MacOS", "OpenVibely"), []byte("new"), 0o755); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(installed, []byte("new"), 0o755); err != nil {
				t.Fatal(err)
			}
			if after := snapshotTestDirectory(t, protectedRoot); !reflect.DeepEqual(after, before) {
				t.Fatalf("protected data changed during replacement: before=%#v after=%#v", before, after)
			}
			if err := restoreDesktopInstallUnit(staged, protected); err != nil {
				t.Fatal(err)
			}
			if after := snapshotTestDirectory(t, protectedRoot); !reflect.DeepEqual(after, before) {
				t.Fatalf("protected data changed during rollback: before=%#v after=%#v", before, after)
			}
			var restored []byte
			var err error
			if tc.isDir {
				restored, err = os.ReadFile(filepath.Join(installed, "Contents", "MacOS", "OpenVibely"))
			} else {
				restored, err = os.ReadFile(installed)
			}
			if err != nil || string(restored) != "old" {
				t.Fatalf("restored install unit = %q, err = %v", restored, err)
			}
		})
	}
}

func TestWailsInstallUnitRejectsEveryProtectedDataPathInsideBoundary(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	if err := os.MkdirAll(filepath.Join(installed, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{InstallPath: installed, BackupPath: installed + ".openvibely-backup", Version: "0.6.0"}
	for _, name := range []string{"app-data", "database", "project-root", "desktop-config", "plugin-root"} {
		t.Run(name, func(t *testing.T) {
			inside := filepath.Join(installed, "Contents", name)
			if err := os.MkdirAll(inside, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := validateDesktopDataBoundaries(staged, []string{filepath.Join(root, "safe"), inside}); err == nil {
				t.Fatalf("accepted %s inside replaceable install unit", name)
			}
			external := filepath.Join(root, name+"-link")
			if err := os.Symlink(inside, external); err != nil {
				t.Skipf("symlink fixture unavailable: %v", err)
			}
			if err := validateDesktopDataBoundaries(staged, []string{external}); err == nil {
				t.Fatalf("accepted symlinked %s inside replaceable install unit", name)
			}
		})
	}
}

func TestWailsInstallUnitRejectsEveryUpdaterOwnedDeletionBoundary(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	if err := os.MkdirAll(filepath.Join(installed, "Contents"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{
		InstallPath:  installed,
		ArtifactPath: installed + ".openvibely-new",
		BackupPath:   installed + ".openvibely-backup",
		Version:      "0.6.0",
		OutcomeID:    "desktop-operation-1",
	}
	helperPath := packagedUpdateHelperPath(staged.InstallPath, AppBundleUpdateHelperCommand)
	for _, boundary := range []string{
		staged.ArtifactPath,
		staged.BackupPath + ".partial",
		staged.BackupPath + ".stale",
		staged.InstallPath + ".openvibely-failed",
		staged.InstallPath + ".bak",
		helperPath,
		helperPath + ".partial",
		packagedUpdateHelperOutcomePath(staged.InstallPath),
		packagedUpdateHelperOutcomePath(staged.InstallPath) + ".tmp",
		packagedUpdateHelperPreparedPath(staged.InstallPath),
		packagedUpdateHelperAuthorizedPath(staged.InstallPath),
		packagedUpdateHelperCancelledPath(staged.InstallPath),
		packagedUpdateHelperRecoveryReadyPath(staged.InstallPath),
		packagedUpdateHelperRecoveryClaimPath(staged.InstallPath),
		packagedUpdateHelperTransitionLeasePath(staged),
		packagedUpdateHelperLeasePath(staged),
	} {
		t.Run(filepath.Base(boundary), func(t *testing.T) {
			protected := filepath.Join(boundary, "user-data")
			if err := os.MkdirAll(protected, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := validateDesktopDataBoundaries(staged, []string{protected}); err == nil {
				t.Fatalf("accepted protected data under updater-owned path %s", boundary)
			}
		})
	}
}

func TestDesktopRollbackRejectsInterruptedBackupAndPreservesHealthyBundle(t *testing.T) {
	root := t.TempDir()
	installed := filepath.Join(root, "OpenVibely.app")
	backup := installed + ".openvibely-backup"
	contents := filepath.Join(installed, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contents, "a-healthy"), []byte("healthy"), 0o644); err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(contents, "z-socket")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("Unix socket fixture unavailable: %v", err)
	}
	defer listener.Close()
	staged := LocalStagedUpdate{InstallPath: installed, BackupPath: backup, Version: "0.6.0"}
	if err := retainDesktopBundle(staged, filepath.Join(root, "AppData")); err == nil {
		t.Fatal("interrupted backup copy unexpectedly succeeded")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("uncommitted backup was published: %v", err)
	}
	if _, err := os.Stat(backup + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("partial backup was retained: %v", err)
	}
	for _, crashArtifact := range []string{backup + ".partial", backup + ".stale"} {
		if err := os.MkdirAll(filepath.Join(crashArtifact, "Contents"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(crashArtifact, "Contents", "incomplete"), []byte("incomplete"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := restoreDesktopBundle(staged, filepath.Join(root, "AppData")); err == nil {
		t.Fatal("rollback accepted an interrupted backup")
	}
	if data, err := os.ReadFile(filepath.Join(contents, "a-healthy")); err != nil || string(data) != "healthy" {
		t.Fatalf("healthy bundle was replaced: data=%q err=%v", data, err)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("healthy bundle entry was removed: %v", err)
	}
}

func TestExtractApplicationBundleRejectsTraversalAndMultipleRoots(t *testing.T) {
	for name, entries := range map[string][]string{"traversal": {"../evil"}, "multiple": {"OpenVibely.app/Contents/a", "Other.app/Contents/b"}} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "app.zip")
			f, _ := os.Create(archive)
			zw := zip.NewWriter(f)
			for _, entry := range entries {
				w, _ := zw.Create(entry)
				_, _ = w.Write([]byte("x"))
			}
			_ = zw.Close()
			_ = f.Close()
			if _, err := extractApplicationBundle(archive, filepath.Join(t.TempDir(), "out")); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
}

func acknowledgePackagedUpdateHelperForTest(_ context.Context, staged LocalStagedUpdate) error {
	if err := claimPackagedUpdateHelperHandoff(context.Background(), staged); err != nil {
		return err
	}
	return authorizePackagedUpdateHelperHandoff(staged)
}

func authorizePackagedUpdateHelperForTest(t *testing.T, staged LocalStagedUpdate) {
	t.Helper()
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			outcome, err := readPackagedUpdateHelperOutcome(staged)
			if err == nil && outcome.State == packagedUpdateOutcomePending {
				_ = authorizePackagedUpdateHelperHandoff(staged)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

func bindBinaryRestartOrigin(staged LocalStagedUpdate, _ *BinaryInstaller) LocalStagedUpdate {
	return staged
}

func TestBinaryInstallerStagePersistsExactOutcomeIdentity(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	payload := []byte("new-binary")
	format := "zip"
	filename := "openvibely_server.zip"
	artifact := zipBinaryFixture(t, "openvibely", payload)
	if runtime.GOOS == "windows" {
		artifact = zipBinaryFixture(t, "openvibely.exe", payload)
	} else if runtime.GOOS == "linux" {
		format, filename = "tar.gz", "openvibely_server.tar.gz"
		artifact = tarGzipBinaryFixture(t, "openvibely", payload)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(artifact)
	}))
	defer server.Close()
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	installer := &BinaryInstaller{Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: runtime.GOOS}, Distribution: buildinfo.DistributionBinary}, Executable: current}
	value, err := installer.Stage(context.Background(), VerifiedRelease{
		Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)},
		Target:   Target{Kind: "binary", OS: runtime.GOOS, URL: server.URL, Filename: filename, Filetype: format, Size: int64(len(artifact)), SHA256: hex.EncodeToString(digest[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	staged, ok := value.(LocalStagedUpdate)
	if !ok || staged.PreviousVersion != "0.5.0" || staged.Version != "0.6.0" || staged.OutcomeID == "" {
		t.Fatalf("staged outcome identity = %#v", value)
	}
}

func TestBinaryInstallerStagesBesideResolvedExecutableSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on many Windows hosts")
	}
	now := time.Unix(1000, 0).UTC()
	payload := []byte("new-binary")
	format := "zip"
	filename := "openvibely_server.zip"
	artifact := zipBinaryFixture(t, "openvibely", payload)
	if runtime.GOOS == "linux" {
		format, filename = "tar.gz", "openvibely_server.tar.gz"
		artifact = tarGzipBinaryFixture(t, "openvibely", payload)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(artifact)
	}))
	defer server.Close()
	root := t.TempDir()
	appBin := filepath.Join(root, "app", "openvibely")
	if err := os.MkdirAll(filepath.Dir(appBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appBin, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(root, "bin", "openvibely")
	if err := os.MkdirAll(filepath.Dir(commandPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(appBin, commandPath); err != nil {
		t.Fatal(err)
	}
	resolvedAppBin, err := filepath.EvalSymlinks(appBin)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(artifact)
	client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
	installer := &BinaryInstaller{Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: runtime.GOOS}, Distribution: buildinfo.DistributionBinary}, Executable: commandPath}
	value, err := installer.Stage(context.Background(), VerifiedRelease{
		Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)},
		Target:   Target{Kind: "binary", OS: runtime.GOOS, URL: server.URL, Filename: filename, Filetype: format, Size: int64(len(artifact)), SHA256: hex.EncodeToString(digest[:])},
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := value.(LocalStagedUpdate)
	if staged.InstallPath != resolvedAppBin {
		t.Fatalf("InstallPath = %q, want resolved executable %q", staged.InstallPath, resolvedAppBin)
	}
	if !strings.HasPrefix(staged.ArtifactPath, resolvedAppBin+".") {
		t.Fatalf("ArtifactPath = %q, want staged beside resolved executable %q", staged.ArtifactPath, resolvedAppBin)
	}
}

func TestBinaryInstallerStagesOfficialPackagedArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name      string
		goos      string
		filetype  string
		filename  string
		entryName string
		archive   func(*testing.T, string, []byte) []byte
	}{
		{name: "macOS zip", goos: "darwin", filetype: "zip", filename: "openvibely_0.6.0_darwin_arm64_server.zip", entryName: "openvibely", archive: zipBinaryFixture},
		{name: "Windows zip", goos: "windows", filetype: "zip", filename: "openvibely_0.6.0_windows_amd64_server.zip", entryName: "openvibely.exe", archive: zipBinaryFixture},
		{name: "Linux tar gzip", goos: "linux", filetype: "tar.gz", filename: "openvibely_0.6.0_linux_amd64_server.tar.gz", entryName: "openvibely", archive: tarGzipBinaryFixture},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0).UTC()
			payload := []byte("replacement-executable")
			archive := tc.archive(t, tc.entryName, payload)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
			defer server.Close()
			root := t.TempDir()
			current := filepath.Join(root, "openvibely")
			if tc.goos == "windows" {
				current += ".exe"
			}
			if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(archive)
			client := NewClient(ClientConfig{Channel: "stable", StatePath: filepath.Join(root, "client.json"), Now: func() time.Time { return now }})
			installer := &BinaryInstaller{Client: client, Current: CurrentBuild{Build: buildinfo.Build{Version: "0.5.0", OS: tc.goos}, Distribution: buildinfo.DistributionBinary}, Executable: current}
			value, err := installer.Stage(context.Background(), VerifiedRelease{
				Metadata: ReleaseMetadata{Version: "0.6.0", Channel: "stable", ExpiresAt: now.Add(time.Hour)},
				Target: Target{Kind: "binary", OS: tc.goos, URL: server.URL, Filename: tc.filename, Filetype: tc.filetype,
					Size: int64(len(archive)), SHA256: hex.EncodeToString(digest[:])},
			})
			if err != nil {
				t.Fatal(err)
			}
			staged := value.(LocalStagedUpdate)
			if got, err := os.ReadFile(staged.ArtifactPath); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("staged executable = %q, err = %v", got, err)
			}
		})
	}
}

func TestBinaryInstallerRejectsUnsafeOrAmbiguousPackages(t *testing.T) {
	for name, archive := range map[string][]byte{
		"traversal zip":       zipBinaryFixture(t, "../openvibely", []byte("bad")),
		"normalized path zip": zipBinaryFixture(t, "bin/../openvibely", []byte("bad")),
		"multiple zip entries": zipBinaryFixtures(t, map[string][]byte{
			"openvibely":     []byte("one"),
			"openvibely.exe": []byte("two"),
		}),
		"nested tar entry": tarGzipBinaryFixture(t, "bin/openvibely", []byte("bad")),
		"multiple tar entries": tarGzipBinaryFixtures(t, []tarBinaryFixture{
			{name: "openvibely", payload: []byte("one")},
			{name: "openvibely.exe", payload: []byte("two")},
		}),
	} {
		t.Run(name, func(t *testing.T) {
			format := "zip"
			if strings.Contains(name, "tar") {
				format = "tar.gz"
			}
			output := filepath.Join(t.TempDir(), "openvibely-new")
			if err := extractPackagedBinary(bytes.NewReader(archive), int64(len(archive)), format, output); err == nil {
				t.Fatal("unsafe or ambiguous package accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("unsafe package published output: %v", err)
			}
		})
	}
}

func TestBinaryInstallerIgnoresArchiveMetadataEntries(t *testing.T) {
	payload := []byte("new-openvibely")
	for name, tc := range map[string]struct {
		format  string
		archive []byte
	}{
		"zip apple metadata": {
			format:  "zip",
			archive: zipBinaryFixtures(t, map[string][]byte{"openvibely-desktop": payload, "._openvibely-desktop": []byte("metadata"), "__MACOSX/._openvibely-desktop": []byte("metadata")}),
		},
		"tar apple metadata": {
			format: "tar.gz",
			archive: tarGzipBinaryFixtures(t, []tarBinaryFixture{
				{name: "._openvibely-desktop", payload: []byte("metadata")},
				{name: "openvibely-desktop", payload: payload},
				{name: "__MACOSX/._openvibely-desktop", payload: []byte("metadata")},
			}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "openvibely-new")
			if err := extractPackagedBinary(bytes.NewReader(tc.archive), int64(len(tc.archive)), tc.format, output); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("payload = %q, want %q", got, payload)
			}
		})
	}
}

func zipBinaryFixture(t *testing.T, name string, payload []byte) []byte {
	return zipBinaryFixtures(t, map[string][]byte{name: payload})
}

func zipBinaryFixtures(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for name, payload := range entries {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o755)
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

type tarBinaryFixture struct {
	name    string
	payload []byte
}

type tarFixture struct {
	name     string
	payload  []byte
	mode     int64
	typeflag byte
}

func tarGzipBinaryFixture(t *testing.T, name string, payload []byte) []byte {
	return tarGzipBinaryFixtures(t, []tarBinaryFixture{{name: name, payload: payload}})
}

func tarGzipBinaryFixtures(t *testing.T, entries []tarBinaryFixture) []byte {
	fixtures := make([]tarFixture, 0, len(entries))
	for _, entry := range entries {
		fixtures = append(fixtures, tarFixture{name: entry.name, payload: entry.payload, mode: 0o755, typeflag: tar.TypeReg})
	}
	return tarGzipFixtures(t, fixtures)
}

func tarGzipFixtures(t *testing.T, entries []tarFixture) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.name, Mode: mode, Size: int64(len(entry.payload)), Typeflag: entry.typeflag}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestBinaryInstallerPassesRelaunchContextOutsideCommandLineAndDurableState(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("signed-original"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	secret := strings.Repeat("secret-value-not-for-helper-argv-or-state", 4096)
	installer := &BinaryInstaller{
		HealthURL:        "http://127.0.0.1/health",
		Arguments:        []string{current, "--credential", secret},
		WorkingDirectory: root,
		StartHelper: func(cmd *exec.Cmd) error {
			if strings.Contains(strings.Join(cmd.Args, "\x00"), secret) {
				t.Fatal("original arguments leaked into helper command line")
			}
			cfg, err := ParseExecutableUpdateHelperArgs(cmd.Args[2:])
			if err != nil {
				return err
			}
			if cfg.RelaunchMetadataPath == "" {
				t.Fatal("helper command omitted relaunch metadata path")
			}
			if err := LoadExecutableUpdateHelperRelaunchFile(cfg.RelaunchMetadataPath, &cfg); err != nil {
				return err
			}
			if strings.Join(cfg.Arguments, "\x00") != strings.Join([]string{current, "--credential", secret}, "\x00") || cfg.WorkingDirectory != root {
				t.Fatalf("relaunch metadata = %#v", cfg)
			}
			return nil
		},
		awaitHelperHandoff: acknowledgePackagedUpdateHelperForTest,
		Shutdown:           func() {},
	}
	if err := installer.Apply(context.Background(), staged); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{packagedUpdateHelperAuthorizedPath(current), packagedUpdateHelperOutcomePath(current), packagedUpdateHelperPreparedPath(current)} {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) || strings.Contains(string(data), root) {
			t.Fatalf("relaunch context leaked into durable state %s", path)
		}
	}
	if helper, err := os.ReadFile(packagedUpdateHelperPath(current, ExecutableUpdateHelperCommand)); err != nil || string(helper) != "signed-original" {
		t.Fatalf("helper copy = %q, err = %v", helper, err)
	}
}

func TestBinaryInstallerRecoveryRequiresInitializedHealthURL(t *testing.T) {
	installer := &BinaryInstaller{}
	if installer.RecoveryReady() {
		t.Fatal("executable update recovery became ready before health URL initialization")
	}
	installer.HealthURL = "http://127.0.0.1:1234/api/system/health"
	if !installer.RecoveryReady() {
		t.Fatal("executable update recovery remained blocked after health URL initialization")
	}
}

func TestBinaryInstallerApplyWrapsHelperHandoffPhaseErrors(t *testing.T) {
	for _, test := range []struct {
		name        string
		setup       func(t *testing.T, staged LocalStagedUpdate)
		mutate      func(staged LocalStagedUpdate) LocalStagedUpdate
		wantMessage string
	}{
		{
			name: "prepare helper",
			setup: func(t *testing.T, staged LocalStagedUpdate) {
				t.Helper()
				if err := os.Remove(staged.InstallPath); err != nil {
					t.Fatal(err)
				}
			},
			wantMessage: "prepare executable update helper",
		},
		{
			name: "clear prior handoff",
			setup: func(t *testing.T, staged LocalStagedUpdate) {
				t.Helper()
				blocked := packagedUpdateHelperOutcomePath(staged.InstallPath)
				if err := os.Mkdir(blocked, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(blocked, "child"), []byte("block remove"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantMessage: "clear prior executable update helper handoff",
		},
		{
			name: "persist preparation",
			mutate: func(staged LocalStagedUpdate) LocalStagedUpdate {
				staged.OutcomeID = ""
				return staged
			},
			wantMessage: "persist executable update helper preparation",
		},
		{
			name: "persist metadata",
			setup: func(t *testing.T, staged LocalStagedUpdate) {
				t.Helper()
				blocked := packagedUpdateHelperRelaunchMetadataPath(staged.InstallPath)
				if err := os.Mkdir(blocked, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(blocked, "child"), []byte("block replace"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantMessage: "persist executable update helper relaunch metadata",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			current := filepath.Join(root, "openvibely")
			if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
			if test.mutate != nil {
				staged = test.mutate(staged)
			}
			if test.setup != nil {
				test.setup(t, staged)
			}
			installer := &BinaryInstaller{
				HealthURL:          "http://127.0.0.1/health",
				StartHelper:        func(*exec.Cmd) error { return nil },
				awaitHelperHandoff: acknowledgePackagedUpdateHelperForTest,
				Shutdown:           func() { t.Fatal("shutdown requested after failed helper handoff") },
			}
			err := installer.Apply(context.Background(), bindBinaryRestartOrigin(staged, installer))
			if err == nil {
				t.Fatal("binary apply succeeded after helper handoff phase failure")
			}
			if !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("binary apply error = %v, want context %q", err, test.wantMessage)
			}
		})
	}
}

func TestBinaryInstallerDefersShutdownUntilApplyReturns(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	started, shutdown := false, false
	var helper *exec.Cmd
	installer := &BinaryInstaller{HealthURL: "http://127.0.0.1/health", StartHelper: func(cmd *exec.Cmd) error {
		started, helper = true, cmd
		prepared, err := readPackagedUpdateHelperPrepared(staged)
		if err != nil || prepared.State != packagedUpdateOutcomePrepared {
			t.Fatalf("pre-launch helper outcome = %#v, err = %v", prepared, err)
		}
		if _, err := os.Stat(packagedUpdateHelperOutcomePath(current)); !os.IsNotExist(err) {
			t.Fatalf("active outcome existed before helper claim: %v", err)
		}
		return nil
	}, awaitHelperHandoff: acknowledgePackagedUpdateHelperForTest}
	if err := installer.Apply(context.Background(), bindBinaryRestartOrigin(staged, installer)); err == nil {
		t.Fatal("binary apply accepted missing shutdown handoff")
	}
	installer.Shutdown = func() { shutdown = true }
	if err := installer.Apply(context.Background(), bindBinaryRestartOrigin(staged, installer)); err != nil {
		t.Fatal(err)
	}
	if !started || shutdown {
		t.Fatalf("started=%v shutdown=%v", started, shutdown)
	}
	installer.ShutdownForRestart()
	if !shutdown {
		t.Fatal("shutdown was not requested after apply returned")
	}
	helperPath := packagedUpdateHelperPath(current, ExecutableUpdateHelperCommand)
	if data, err := os.ReadFile(helperPath); err != nil || string(data) != "old" {
		t.Fatalf("helper copy = %q, err = %v", data, err)
	}
	joined := strings.Join(helper.Args, " ")
	for _, required := range []string{helperPath, "--current " + current} {
		if !strings.Contains(joined, required) {
			t.Fatalf("detached helper args %q lack %q", helper.Args, required)
		}
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil || outcome.State != packagedUpdateOutcomeAuthorized {
		t.Fatalf("helper outcome = %#v, err = %v", outcome, err)
	}
}

func TestBinaryInstallerClaimedPreparedDoesNotAuthorizeShutdown(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	shutdown := false
	installer := &BinaryInstaller{
		HealthURL: "http://127.0.0.1/health",
		StartHelper: func(_ *exec.Cmd) error {
			return os.Rename(packagedUpdateHelperPreparedPath(current), packagedUpdateHelperOutcomePath(current))
		},
		Shutdown: func() { shutdown = true },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := installer.Apply(ctx, bindBinaryRestartOrigin(staged, installer))
	if err == nil {
		t.Fatal("claimed prepared outcome authorized parent shutdown")
	}
	if !strings.Contains(err.Error(), "confirm executable update helper handoff") {
		t.Fatalf("executable update helper handoff failure lacked apply context: %v", err)
	}
	if shutdown {
		t.Fatal("parent shutdown was requested before durable pending publication")
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil || outcome.State != packagedUpdateOutcomePrepared {
		t.Fatalf("claimed outcome = %#v, err = %v", outcome, err)
	}
}

func TestBinaryInstallerTimeoutDoesNotAuthorizeLatePendingHelper(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	lateAck := make(chan error, 1)
	shutdown := false
	installer := &BinaryInstaller{
		HealthURL: "http://127.0.0.1/health",
		StartHelper: func(_ *exec.Cmd) error {
			go func() {
				time.Sleep(75 * time.Millisecond)
				lateAck <- claimPackagedUpdateHelperHandoff(context.Background(), staged)
			}()
			return nil
		},
		Shutdown: func() { shutdown = true },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := installer.Apply(ctx, bindBinaryRestartOrigin(staged, installer)); err == nil {
		t.Fatal("binary apply accepted a helper that acknowledged after timeout")
	}
	if err := <-lateAck; err != nil {
		t.Fatal(err)
	}
	outcome, err := readPackagedUpdateHelperOutcome(staged)
	if err != nil || outcome.State != packagedUpdateOutcomePending {
		t.Fatalf("late helper outcome = %#v, err = %v", outcome, err)
	}
	if shutdown {
		t.Fatal("late helper acknowledgment requested parent shutdown")
	}
}

func TestPackagedUpdateHelperLiveParentProcess(t *testing.T) {
	if os.Getenv("OPENVIBELY_TEST_LIVE_PARENT") != "1" {
		return
	}
	time.Sleep(time.Minute)
}

func TestPackagedUpdateHelperAuthorizedParentExitFailurePublishesCancellation(t *testing.T) {
	originalWaitForProcessExit := waitForProcessExit
	t.Cleanup(func() { waitForProcessExit = originalWaitForProcessExit })

	for _, test := range []struct {
		name       string
		cancelWait bool
		wantStarts int
	}{
		{name: "timeout", wantStarts: 1},
		{name: "context cancellation", cancelWait: true, wantStarts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			current := filepath.Join(root, "openvibely")
			stagedPath := current + ".openvibely-new"
			backup := current + ".openvibely-backup"
			if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stagedPath, []byte("new"), 0o755); err != nil {
				t.Fatal(err)
			}
			staged := LocalStagedUpdate{ArtifactPath: stagedPath, InstallPath: current, BackupPath: backup, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
			if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomePending); err != nil {
				t.Fatal(err)
			}
			if err := authorizePackagedUpdateHelperHandoff(staged); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waitForProcessExit = func(waitCtx context.Context, pid int, timeout time.Duration) error {
				if pid != 99999999 {
					t.Fatalf("parent PID = %d", pid)
				}
				if timeout != 50*time.Millisecond {
					t.Fatalf("wait timeout = %s", timeout)
				}
				if test.cancelWait {
					cancel()
					<-waitCtx.Done()
					return waitCtx.Err()
				}
				return errors.New("timed out waiting for parent process to exit")
			}

			starts := 0
			err := RunExecutableUpdateHelper(ctx, ExecutableUpdateHelperConfig{
				ParentPID: 99999999, Current: current, Staged: stagedPath, Backup: backup, HealthURL: "http://127.0.0.1/health",
				ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1", WaitTimeout: 50 * time.Millisecond,
				StartCommand: func(string, string) (func(context.Context) error, error) {
					outcome, readErr := readPackagedUpdateHelperOutcome(staged)
					if readErr != nil || outcome.State != packagedUpdateOutcomeCancelled {
						t.Fatalf("restart before durable cancellation: outcome=%#v err=%v", outcome, readErr)
					}
					starts++
					return func(context.Context) error { return nil }, nil
				},
			})
			if err == nil {
				t.Fatal("authorized parent-exit failure succeeded")
			}
			if starts != test.wantStarts {
				t.Fatalf("prior service starts = %d, want %d", starts, test.wantStarts)
			}
			if data, readErr := os.ReadFile(current); readErr != nil || string(data) != "old" {
				t.Fatalf("current binary = %q, err = %v", data, readErr)
			}
			outcome, readErr := readPackagedUpdateHelperOutcome(staged)
			if readErr != nil || outcome.State != packagedUpdateOutcomeCancelled {
				t.Fatalf("helper outcome = %#v, err = %v", outcome, readErr)
			}
		})
	}
}

func TestPackagedUpdateHelperCannotActWithoutInstallerAuthorization(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	stagedPath := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagedPath, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{ArtifactPath: stagedPath, InstallPath: current, BackupPath: backup, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomePrepared); err != nil {
		t.Fatal(err)
	}
	started := false
	err := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
		ParentPID:         99999999,
		Current:           current,
		Staged:            stagedPath,
		Backup:            backup,
		HealthURL:         "http://127.0.0.1/health",
		ExpectedVersion:   "0.6.0",
		PreviousVersion:   "0.5.0",
		OutcomeID:         "operation-1",
		WaitTimeout:       25 * time.Millisecond,
		ValidationTimeout: 25 * time.Millisecond,
		StartCommand: func(string, string) (func(context.Context) error, error) {
			started = true
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("unauthorized helper completed replacement")
	}
	if started {
		t.Fatal("unauthorized helper started a successor")
	}
	if data, readErr := os.ReadFile(current); readErr != nil || string(data) != "old" {
		t.Fatalf("current binary = %q, err = %v", data, readErr)
	}
	outcome, outcomeErr := readPackagedUpdateHelperOutcome(staged)
	if outcomeErr != nil || outcome.State != "cancelled" {
		t.Fatalf("unauthorized helper outcome = %#v, err = %v", outcome, outcomeErr)
	}
}

func TestPackagedUpdateHelperAuthorizationAndCancellationHaveOneAtomicWinner(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		root := t.TempDir()
		current := filepath.Join(root, "openvibely")
		staged := LocalStagedUpdate{InstallPath: current, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-" + strconv.Itoa(iteration)}
		if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomePrepared); err != nil {
			t.Fatal(err)
		}
		if err := claimPackagedUpdateHelperHandoff(context.Background(), staged); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		authorized := make(chan bool, 1)
		cancelled := make(chan bool, 1)
		go func() {
			<-start
			authorized <- authorizePackagedUpdateHelperHandoff(staged) == nil
		}()
		go func() {
			<-start
			won, err := cancelPackagedUpdateHelperHandoff(staged)
			cancelled <- err == nil && won
		}()
		close(start)
		authorizedWon, cancelledWon := <-authorized, <-cancelled
		if authorizedWon == cancelledWon {
			t.Fatalf("iteration %d: authorized=%v cancelled=%v", iteration, authorizedWon, cancelledWon)
		}
		outcome, err := readPackagedUpdateHelperOutcome(staged)
		if err != nil {
			t.Fatal(err)
		}
		if authorizedWon && outcome.State != packagedUpdateOutcomeAuthorized {
			t.Fatalf("iteration %d: authorized winner left %q", iteration, outcome.State)
		}
		if cancelledWon && outcome.State != packagedUpdateOutcomeCancelled {
			t.Fatalf("iteration %d: cancellation winner left %q", iteration, outcome.State)
		}
	}
}

func TestPackagedUpdateHelperClaimAndPreparedCancellationHaveOneAtomicWinner(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		root := t.TempDir()
		current := filepath.Join(root, "openvibely")
		staged := LocalStagedUpdate{InstallPath: current, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-" + strconv.Itoa(iteration)}
		if err := writePackagedUpdateHelperOutcome(staged, packagedUpdateOutcomePrepared); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		claimed := make(chan bool, 1)
		cancelled := make(chan bool, 1)
		go func() {
			<-start
			claimed <- claimPackagedUpdateHelperHandoff(context.Background(), staged) == nil
		}()
		go func() {
			<-start
			won, err := cancelPreparedPackagedUpdateHelperHandoff(staged)
			cancelled <- err == nil && won
		}()
		close(start)
		claimedWon, cancelledWon := <-claimed, <-cancelled
		if claimedWon == cancelledWon {
			t.Fatalf("iteration %d: claimed=%v cancelled=%v", iteration, claimedWon, cancelledWon)
		}
		if claimedWon {
			outcome, err := readPackagedUpdateHelperOutcome(staged)
			if err != nil || outcome.State != packagedUpdateOutcomePending {
				t.Fatalf("iteration %d: outcome=%#v err=%v", iteration, outcome, err)
			}
			data, err := os.ReadFile(packagedUpdateHelperOutcomePath(current))
			if err != nil || !bytes.Contains(data, []byte(`"state":"pending"`)) {
				t.Fatalf("iteration %d: active claim=%s err=%v", iteration, data, err)
			}
		} else if _, err := os.Stat(packagedUpdateHelperPreparedPath(current)); !os.IsNotExist(err) {
			t.Fatalf("iteration %d: cancelled prepared claim remains: %v", iteration, err)
		}
	}
}

func TestBinaryInstallerRecoveryWaitsForDurableHelperReadiness(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	if err := os.WriteFile(current, []byte("published-target"), 0o755); err != nil {
		t.Fatal(err)
	}
	phase, err := marshalPackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeTargetPublished)
	if err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteState(packagedUpdateHelperAuthorizedPath(current), phase); err != nil {
		t.Fatal(err)
	}
	shutdown := false
	var command *exec.Cmd
	installer := &BinaryInstaller{
		Current:   CurrentBuild{Build: buildinfo.Build{Version: "0.5.0"}},
		HealthURL: "http://127.0.0.1/health",
		StartHelper: func(cmd *exec.Cmd) error {
			command = cmd
			if shutdown {
				t.Fatal("shutdown preceded recovery helper readiness")
			}
			ready, err := marshalPackagedUpdateHelperOutcome(staged, packagedUpdateOutcomeRecovering)
			if err != nil {
				return err
			}
			return atomicWriteState(packagedUpdateHelperRecoveryReadyPath(current), ready)
		},
		Shutdown: func() { shutdown = true },
	}
	if err := installer.RecoverPackagedUpdateRestart(context.Background(), bindBinaryRestartOrigin(staged, installer)); err != nil {
		t.Fatal(err)
	}
	if shutdown || command == nil {
		t.Fatalf("shutdown=%v command=%#v", shutdown, command)
	}
	installer.ShutdownForRestart()
	if !shutdown {
		t.Fatal("shutdown was not requested after recovery returned")
	}
	joined := strings.Join(command.Args, " ")
	for _, required := range []string{"--recovery true", "--running-version 0.5.0", "--outcome-id operation-1"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("recovery command %q lacks %q", command.Args, required)
		}
	}
	helperPath := packagedUpdateHelperPath(current, ExecutableUpdateHelperCommand)
	if data, err := os.ReadFile(helperPath); err != nil || string(data) != "published-target" {
		t.Fatalf("recovery helper image=%q err=%v", data, err)
	}
}

func TestPackagedUpdateHelperResumesDurablePostAuthorizationPhases(t *testing.T) {
	for _, tc := range []struct {
		name        string
		phase       string
		current     string
		staged      bool
		health      string
		wantOutcome string
		wantCurrent string
	}{
		{name: "backup published", phase: packagedUpdateOutcomeBackupPublished, current: "old", staged: true, health: "0.6.0", wantOutcome: packagedUpdateOutcomeSucceeded, wantCurrent: "new"},
		{name: "target published", phase: packagedUpdateOutcomeTargetPublished, current: "new", health: "0.6.0", wantOutcome: packagedUpdateOutcomeSucceeded, wantCurrent: "new"},
		{name: "rollback started", phase: packagedUpdateOutcomeRollingBack, current: "new", health: "0.5.0", wantOutcome: packagedUpdateOutcomeRolledBack, wantCurrent: "old"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			current := filepath.Join(root, "openvibely")
			stagedPath := current + ".openvibely-new"
			backup := current + ".openvibely-backup"
			if err := os.WriteFile(current, []byte(tc.current), 0o755); err != nil {
				t.Fatal(err)
			}
			if tc.staged {
				if err := os.WriteFile(stagedPath, []byte("new"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(backup, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			staged := LocalStagedUpdate{ArtifactPath: stagedPath, InstallPath: current, BackupPath: backup, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
			phase, err := marshalPackagedUpdateHelperOutcome(staged, tc.phase)
			if err != nil {
				t.Fatal(err)
			}
			if err := atomicWriteState(packagedUpdateHelperAuthorizedPath(current), phase); err != nil {
				t.Fatal(err)
			}
			health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"ready":true,"version":%q}`, tc.health)
			}))
			defer health.Close()
			err = RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
				ParentPID: 99999999, Current: current, Staged: stagedPath, Backup: backup, HealthURL: health.URL,
				ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1",
				WaitTimeout: 50 * time.Millisecond, ValidationTimeout: 25 * time.Millisecond,
				StartCommand: func(string, string) (func(context.Context) error, error) {
					return func(context.Context) error { return nil }, nil
				},
			})
			if tc.wantOutcome == packagedUpdateOutcomeRolledBack {
				if err == nil {
					t.Fatal("resumed rollback returned success")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			outcome, readErr := readPackagedUpdateHelperOutcome(staged)
			if readErr != nil || outcome.State != tc.wantOutcome {
				t.Fatalf("outcome=%#v err=%v", outcome, readErr)
			}
			data, readErr := os.ReadFile(current)
			if readErr != nil || string(data) != tc.wantCurrent {
				t.Fatalf("current=%q err=%v", data, readErr)
			}
		})
	}
}

func TestExecutableUpdateRecoveryHelperSettlesPostAuthorizationCrashResidue(t *testing.T) {
	for _, tc := range []struct {
		name         string
		phase        string
		running      string
		stagedExists bool
		health       string
		wantOutcome  string
		wantCurrent  string
		wantStarts   int
		wantStops    int
	}{
		{name: "before target publication", phase: packagedUpdateOutcomeBackupPublished, running: "0.5.0", stagedExists: true, health: "0.5.0", wantOutcome: packagedUpdateOutcomeCancelled, wantCurrent: "old", wantStarts: 1},
		{name: "published target validates", phase: packagedUpdateOutcomeTargetPublished, running: "0.5.0", health: "0.6.0", wantOutcome: packagedUpdateOutcomeSucceeded, wantCurrent: "new", wantStarts: 1},
		{name: "published target rolls back", phase: packagedUpdateOutcomeValidating, running: "0.5.0", health: "wrong", wantOutcome: packagedUpdateOutcomeRolledBack, wantCurrent: "old", wantStarts: 2, wantStops: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			current := filepath.Join(root, "openvibely")
			stagedPath := current + ".openvibely-new"
			backup := current + ".openvibely-backup"
			currentData := "new"
			if tc.stagedExists {
				currentData = "old"
				if err := os.WriteFile(stagedPath, []byte("new"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(current, []byte(currentData), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(backup, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			staged := LocalStagedUpdate{ArtifactPath: stagedPath, InstallPath: current, BackupPath: backup, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
			phase, err := marshalPackagedUpdateHelperOutcome(staged, tc.phase)
			if err != nil {
				t.Fatal(err)
			}
			if err := atomicWriteState(packagedUpdateHelperAuthorizedPath(current), phase); err != nil {
				t.Fatal(err)
			}
			health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"ready":true,"version":%q}`, tc.health)
			}))
			defer health.Close()
			starts, stops := 0, 0
			if err := writePackagedUpdateHelperRecoveryClaim(staged); err != nil {
				t.Fatal(err)
			}
			err = RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
				ParentPID: 99999999, Current: current, Staged: stagedPath, Backup: backup, HealthURL: health.URL,
				ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1",
				RunningVersion: tc.running, Recovery: true, WaitTimeout: 50 * time.Millisecond, ValidationTimeout: 25 * time.Millisecond,
				StartCommand: func(string, string) (func(context.Context) error, error) {
					starts++
					return func(context.Context) error { stops++; return nil }, nil
				},
			})
			if tc.wantOutcome == packagedUpdateOutcomeRolledBack {
				if err == nil {
					t.Fatal("rollback recovery returned success")
				}
				beforeRestart := starts
				if retryErr := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
					ParentPID: 99999999, Current: current, Staged: stagedPath, Backup: backup, HealthURL: health.URL,
					ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1",
					RunningVersion: tc.running, Recovery: true, WaitTimeout: 50 * time.Millisecond, ValidationTimeout: 25 * time.Millisecond,
					StartCommand: func(string, string) (func(context.Context) error, error) {
						starts++
						return nil, nil
					},
				}); retryErr != nil || starts != beforeRestart {
					t.Fatalf("terminal recovery manager retry: err=%v starts=%d before=%d", retryErr, starts, beforeRestart)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			outcome, readErr := readPackagedUpdateHelperOutcome(staged)
			if readErr != nil || outcome.State != tc.wantOutcome {
				t.Fatalf("outcome=%#v err=%v", outcome, readErr)
			}
			data, readErr := os.ReadFile(current)
			if readErr != nil || string(data) != tc.wantCurrent {
				t.Fatalf("current=%q err=%v", data, readErr)
			}
			if starts != tc.wantStarts || stops != tc.wantStops {
				t.Fatalf("starts=%d stops=%d", starts, stops)
			}
		})
	}
}

func TestPackagedUpdateHelperExecRestartsPriorBinaryAfterPreInstallFailure(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	starts := 0
	handoff := LocalStagedUpdate{InstallPath: current, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	if err := writePackagedUpdateHelperOutcome(handoff, packagedUpdateOutcomePrepared); err != nil {
		t.Fatal(err)
	}
	authorizePackagedUpdateHelperForTest(t, handoff)
	err := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
		ParentPID: 99999999, Current: current, Staged: current + ".openvibely-new", Backup: current + ".openvibely-backup", HealthURL: "http://127.0.0.1/health",
		ExpectedVersion: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1", WaitTimeout: time.Second,
		StartCommand: func(mode, target string) (func(context.Context) error, error) {
			starts++
			if mode != "exec" || target != current {
				t.Fatalf("restart = %s %s", mode, target)
			}
			return func(context.Context) error { return nil }, nil
		},
	})
	if err == nil {
		t.Fatal("missing staged binary succeeded")
	}
	if starts != 1 {
		t.Fatalf("prior binary restart attempts = %d", starts)
	}
	if data, readErr := os.ReadFile(current); readErr != nil || string(data) != "old" {
		t.Fatalf("current = %q, err = %v", data, readErr)
	}
	outcome, readErr := readPackagedUpdateHelperOutcome(LocalStagedUpdate{InstallPath: current, Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"})
	if readErr != nil || outcome.State != packagedUpdateOutcomeRolledBack {
		t.Fatalf("helper outcome = %#v, err = %v", outcome, readErr)
	}
}

func TestBinaryInstallerHelperLaunchFailurePreservesCurrent(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	if err := os.WriteFile(current, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}
	staged := LocalStagedUpdate{ArtifactPath: current + ".openvibely-new", InstallPath: current, BackupPath: current + ".openvibely-backup", Version: "0.6.0", PreviousVersion: "0.5.0", OutcomeID: "operation-1"}
	shutdown := false
	var helperPath string
	installer := &BinaryInstaller{
		HealthURL: "http://127.0.0.1/health",
		StartHelper: func(cmd *exec.Cmd) error {
			helperPath = cmd.Path
			return errors.New("start failed")
		},
		awaitHelperHandoff: acknowledgePackagedUpdateHelperForTest,
		Shutdown:           func() { shutdown = true },
	}
	err := installer.Apply(context.Background(), bindBinaryRestartOrigin(staged, installer))
	if err == nil {
		t.Fatal("helper launch failure succeeded")
	}
	if !strings.Contains(err.Error(), "start executable update helper") {
		t.Fatalf("helper launch failure lacked apply context: %v", err)
	}
	if shutdown {
		t.Fatal("shutdown requested after helper launch failure")
	}
	if data, err := os.ReadFile(current); err != nil || string(data) != "old" {
		t.Fatalf("current = %q, err = %v", data, err)
	}
	for _, path := range []string{
		helperPath,
		packagedUpdateHelperRelaunchMetadataPath(current),
		packagedUpdateHelperOutcomePath(current),
		packagedUpdateHelperPreparedPath(current),
		packagedUpdateHelperAuthorizedPath(current),
		packagedUpdateHelperCancelledPath(current),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("helper-start failure retained %s: %v", path, err)
		}
	}
}

func TestPackagedUpdateHelperRecoversInterruptedSwapBeforePublishingStagedBinary(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(backup, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.6.0"}`))
	}))
	defer health.Close()

	cfg := ExecutableUpdateHelperConfig{ParentPID: 99999999, Current: current, Staged: staged, Backup: backup, HealthURL: health.URL, ExpectedVersion: "0.6.0", WaitTimeout: time.Second, ValidationTimeout: time.Second, StartCommand: func(string, string) (func(context.Context) error, error) { return nil, nil }}
	if err := RunExecutableUpdateHelper(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(current); err != nil || string(data) != "new" {
		t.Fatalf("current = %q, err = %v", data, err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "old" {
		t.Fatalf("backup = %q, err = %v", data, err)
	}
}

func TestInstallStagedBinaryKeepsCurrentExecutableWhenPublicationFails(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(current, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}

	if err := installStagedBinary(current, staged, backup, 0o751); err == nil {
		t.Fatal("missing staged binary was published")
	}
	if data, err := os.ReadFile(current); err != nil || string(data) != "old" {
		t.Fatalf("current = %q, err = %v", data, err)
	}
	if data, err := os.ReadFile(backup); err != nil || string(data) != "old" {
		t.Fatalf("backup = %q, err = %v", data, err)
	}
}

func TestPackagedUpdateHelperAtomicReplacementPreservesPermissionsAndValidatesVersion(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(current, []byte("old"), 0o751); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(current)
	if err != nil {
		t.Fatal(err)
	}
	expectedPermissions := originalInfo.Mode().Perm()
	if err := os.WriteFile(staged, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.6.0"}`))
	}))
	defer health.Close()
	cfg := ExecutableUpdateHelperConfig{ParentPID: 99999999, Current: current, Staged: staged, Backup: backup, HealthURL: health.URL, ExpectedVersion: "0.6.0", WaitTimeout: time.Second, ValidationTimeout: time.Second, StartCommand: func(string, string) (func(context.Context) error, error) { return nil, nil }}
	if err := RunExecutableUpdateHelper(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(current)
	if string(data) != "new" {
		t.Fatalf("current = %q", data)
	}
	info, err := os.Stat(current)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != expectedPermissions {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	old, _ := os.ReadFile(backup)
	if string(old) != "old" {
		t.Fatalf("backup = %q", old)
	}
	backupInfo, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if backupInfo.Mode().Perm() != expectedPermissions {
		t.Fatalf("backup permissions = %o", backupInfo.Mode().Perm())
	}
}

func TestPackagedUpdateHelperStopsFailedSuccessorBeforeRollback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific; Windows uses the same successor lifecycle contract")
	}
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	pidPath := filepath.Join(root, "successor.pid")
	oldScript := "#!/bin/sh\nexit 0\n"
	newScript := "#!/bin/sh\necho $$ > " + pidPath + "\nwhile :; do sleep 1; done\n"
	if err := os.WriteFile(current, []byte(oldScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte(newScript), 0o755); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.5.0"}`))
	}))
	defer health.Close()

	starts := 0
	err := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
		ParentPID: 99999999, Current: current, Staged: staged, Backup: backup, HealthURL: health.URL,
		ExpectedVersion: "0.6.0", WaitTimeout: time.Second, ValidationTimeout: 100 * time.Millisecond,
		StartCommand: func(_ string, target string) (func(context.Context) error, error) {
			starts++
			cmd := exec.Command(target)
			if err := cmd.Start(); err != nil {
				return nil, err
			}
			stop := func(context.Context) error { return stopStartedProcess(cmd) }
			if starts == 1 {
				deadline := time.Now().Add(time.Second)
				for {
					if _, err := os.Stat(pidPath); err == nil {
						return stop, nil
					}
					if time.Now().After(deadline) {
						_ = stop(context.Background())
						return nil, errors.New("successor did not publish PID")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			return stop, nil
		},
	})
	if err == nil {
		t.Fatal("version mismatch succeeded")
	}
	pidData, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	process, findErr := os.FindProcess(pid)
	if findErr != nil {
		t.Fatal(findErr)
	}
	if signalErr := process.Signal(syscall.Signal(0)); signalErr == nil {
		_ = process.Kill()
		t.Fatal("failed successor remained running after rollback")
	}
	if data, readErr := os.ReadFile(current); readErr != nil || string(data) != oldScript {
		t.Fatalf("rollback current=%q err=%v", data, readErr)
	}
}

func TestPackagedUpdateHelperDoesNotRollbackUntilSuccessorExitIsConfirmed(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.5.0"}`))
	}))
	defer health.Close()

	err := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
		ParentPID: 99999999, Current: current, Staged: staged, Backup: backup, HealthURL: health.URL,
		ExpectedVersion: "0.6.0", WaitTimeout: time.Second, ValidationTimeout: 20 * time.Millisecond,
		StartCommand: func(string, string) (func(context.Context) error, error) {
			return func(context.Context) error { return errors.New("successor still running") }, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "stopping failed successor") {
		t.Fatalf("error = %v", err)
	}
	if data, readErr := os.ReadFile(current); readErr != nil || string(data) != "new" {
		t.Fatalf("current = %q, err = %v", data, readErr)
	}
	if data, readErr := os.ReadFile(backup); readErr != nil || string(data) != "old" {
		t.Fatalf("backup = %q, err = %v", data, readErr)
	}
}

func TestPackagedUpdateHelperRollsBackAfterDefinitiveSuccessorStartFailure(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	if err := os.WriteFile(current, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	starts := 0
	err := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{
		ParentPID: 99999999, Current: current, Staged: staged, Backup: backup, HealthURL: "http://127.0.0.1/health",
		ExpectedVersion: "0.6.0", WaitTimeout: time.Second, ValidationTimeout: time.Second,
		StartCommand: func(string, string) (func(context.Context) error, error) {
			starts++
			if starts == 1 {
				return nil, errors.New("successor launch failed")
			}
			return func(context.Context) error { return nil }, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "prior binary was restored") {
		t.Fatalf("error = %v", err)
	}
	if data, readErr := os.ReadFile(current); readErr != nil || string(data) != "old" {
		t.Fatalf("current = %q, err = %v", data, readErr)
	}
	if starts != 2 {
		t.Fatalf("starts = %d", starts)
	}
}

func TestPackagedUpdateHelperRollsBackWhenReportedVersionDoesNotMatch(t *testing.T) {
	root := t.TempDir()
	current := filepath.Join(root, "openvibely")
	staged := current + ".openvibely-new"
	backup := current + ".openvibely-backup"
	_ = os.WriteFile(current, []byte("old"), 0o755)
	_ = os.WriteFile(staged, []byte("new"), 0o755)
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true,"version":"0.5.0"}`))
	}))
	defer health.Close()
	starts := 0
	err := RunExecutableUpdateHelper(context.Background(), ExecutableUpdateHelperConfig{ParentPID: 99999999, Current: current, Staged: staged, Backup: backup, HealthURL: health.URL, ExpectedVersion: "0.6.0", WaitTimeout: time.Second, ValidationTimeout: 20 * time.Millisecond, StartCommand: func(string, string) (func(context.Context) error, error) {
		starts++
		return func(context.Context) error { return nil }, nil
	}})
	if err == nil {
		t.Fatal("version mismatch succeeded")
	}
	data, _ := os.ReadFile(current)
	if string(data) != "old" || starts != 2 {
		t.Fatalf("rollback current=%q starts=%d", data, starts)
	}
}

func TestWailsProviderCheckAndDownloadContracts(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	artifact := []byte("desktop artifact payload")
	digest := sha256.Sum256(artifact)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/artifact.zip" {
			t.Fatalf("unexpected artifact path %s", r.URL.Path)
		}
		_, _ = w.Write(artifact)
	}))
	defer server.Close()

	client := NewClient(ClientConfig{Channel: "stable", Now: func() time.Time { return now }, HTTPClient: server.Client()})
	current := CurrentBuild{Build: buildinfo.Build{Version: "1.0.0", OS: "linux", Arch: "amd64"}}
	release := VerifiedRelease{
		Metadata: ReleaseMetadata{SchemaVersion: 1, Version: "1.1.0", Channel: "stable", PublishedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), ReleaseNotesURL: "https://example.test/notes"},
		Target:   Target{Kind: "desktop", OS: "linux", Arch: "amd64", URL: server.URL + "/artifact.zip", Filename: "openvibely.zip", Filetype: "zip", Size: int64(len(artifact)), SHA256: hex.EncodeToString(digest[:])},
		Action:   "install",
	}
	provider := &WailsProvider{Client: client, Current: current, Release: &release}
	if provider.Name() != "openvibely" {
		t.Fatalf("unexpected provider name %q", provider.Name())
	}
	checked, err := provider.Check(ctx, wailsupdater.CheckRequest{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if checked == nil || checked.Version != "1.1.0" || checked.Artifact.Filename != "openvibely.zip" || checked.Verification == nil || !bytes.Equal(checked.Verification.Digest, digest[:]) {
		t.Fatalf("unexpected checked release: %#v", checked)
	}
	var downloaded bytes.Buffer
	var progress []int64
	if err := provider.Download(ctx, checked, &downloaded, func(written, total int64) { progress = append(progress, written, total) }); err != nil {
		t.Fatalf("Download: %v", err)
	}
	if downloaded.String() != string(artifact) {
		t.Fatalf("unexpected downloaded artifact %q", downloaded.String())
	}
	if len(progress) != 2 || progress[0] != int64(len(artifact)) || progress[1] != int64(len(artifact)) {
		t.Fatalf("unexpected download progress %#v", progress)
	}

	provider.Release = nil
	if checked, err := provider.Check(ctx, wailsupdater.CheckRequest{}); err != nil || checked != nil {
		t.Fatalf("nil release Check = %#v, %v", checked, err)
	}
	if err := provider.Download(ctx, nil, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "no verified desktop release") {
		t.Fatalf("expected missing release Download error, got %v", err)
	}
	badDigest := release
	badDigest.Target.SHA256 = "not-hex"
	provider.Release = &badDigest
	if _, err := provider.Check(ctx, wailsupdater.CheckRequest{}); err == nil || !strings.Contains(err.Error(), "invalid byte") {
		t.Fatalf("expected bad digest Check error, got %v", err)
	}
	manual := release
	manual.Action = "manual"
	provider.Release = &manual
	if _, err := provider.Check(ctx, wailsupdater.CheckRequest{}); err == nil || !strings.Contains(err.Error(), "manual-only") {
		t.Fatalf("expected validation Check error, got %v", err)
	}
}

func TestWailsInstallerValidationAndRestartContracts(t *testing.T) {
	installer := &WailsInstaller{}
	if err := installer.Validate(context.Background(), ReleaseMetadata{}); err == nil || !strings.Contains(err.Error(), "version is empty") {
		t.Fatalf("expected empty version validation error, got %v", err)
	}
	if err := installer.Validate(context.Background(), ReleaseMetadata{Version: "1.2.3"}); err != nil {
		t.Fatalf("Validate with version: %v", err)
	}
	if !installer.RequiresRestartValidation() {
		t.Fatal("Wails installer should require restart validation")
	}
	if installer.RecoveryReady() {
		t.Fatal("installer without health/shutdown should not be recovery-ready")
	}
	installer.HealthURL = "http://127.0.0.1:1234/health"
	installer.Shutdown = func() {}
	if !installer.RecoveryReady() {
		t.Fatal("installer with health/shutdown should be recovery-ready")
	}
	if err := installer.Apply(context.Background(), "not staged"); err == nil || !strings.Contains(err.Error(), "invalid Wails desktop staged update") {
		t.Fatalf("expected invalid apply value error, got %v", err)
	}
	if err := installer.Rollback(context.Background(), "not staged"); err == nil || !strings.Contains(err.Error(), "invalid Wails desktop staged update") {
		t.Fatalf("expected invalid rollback value error, got %v", err)
	}
}
