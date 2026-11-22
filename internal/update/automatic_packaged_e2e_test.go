package update

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These cases run real server/desktop processes and their actual update helpers.
// Only the release service and model response are fixtures. No fake installer is
// used. Each distribution builds its three versions once for the entire suite.
func TestAutomaticPackagedUpdateE2E(t *testing.T) {
	if os.Getenv(updateE2EEnv) != "1" {
		t.Skip(updateE2EEnv + "=1 is required")
	}
	distribution := os.Getenv("OPENVIBELY_UPDATE_E2E_DISTRIBUTION")
	assertPackagedUpdateMatrixTarget(t, distribution)
	if distribution != "binary" && distribution != "desktop" {
		t.Fatal("select binary or desktop")
	}
	buildRoot := t.TempDir()
	builds := map[string]string{}
	for _, version := range []string{"0.5.0", "0.6.0", "0.7.0"} {
		output := filepath.Join(buildRoot, version, executableName("openvibely"))
		if distribution == "desktop" {
			buildDesktopCommand(t, output, version)
		} else {
			buildGoCommand(t, "./cmd/server", output, map[string]string{"github.com/openvibely/openvibely/internal/buildinfo.Version": version, "github.com/openvibely/openvibely/internal/buildinfo.Artifact": "binary"})
		}
		builds[version] = output
	}
	for _, scenario := range []string{"default_on", "disable_enable", "wait_for_work", "cancel_restart_retry", "rollback_no_auto_retry", "failed_install_retry", "missing_stage", "corrupt_stage", "retry_download_failure", "interrupted_retry_download", "expired_metadata_retry", "reuse_stage"} {
		t.Run(scenario, func(t *testing.T) { runAutomaticPackagedScenario(t, distribution, builds, scenario) })
	}
}

func disableAutomaticUpdatesForE2E(t *testing.T, data string) {
	t.Helper()
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "update-coordinator.json"), []byte(`{"state":"idle","disable_automatic_updates":true}`), 0600); err != nil {
		t.Fatal(err)
	}
}

type automaticAppE2E struct {
	exited                                                                  chan struct{}
	t                                                                       *testing.T
	root, data, installed, installUnit, distribution, baseURL, service, key string
	cmd                                                                     *exec.Cmd
	logs                                                                    func() string
	sequence                                                                int
}

func (a *automaticAppE2E) start() {
	a.t.Helper()
	a.sequence++
	stdout, stderr, logs := openCommandLogs(a.t, a.root, fmt.Sprintf("app-%d", a.sequence))
	a.logs = logs
	cmd := exec.Command(a.installed)
	cmd.Dir = a.root
	cmd.Env = append(envWithout("PORT", "OPENVIBELY_DESKTOP_CONFIG_FILE"),
		"DATABASE_PATH="+filepath.Join(a.data, "openvibely.db"), "OPENVIBELY_APP_DATA_DIR="+a.data,
		"PROJECT_REPO_ROOT="+filepath.Join(a.root, "projects"), "OPENVIBELY_PLUGIN_ROOT="+filepath.Join(a.data, "plugins"),
		"OPENVIBELY_UPDATE_SERVICE_URL="+a.service, "OPENVIBELY_UPDATE_PUBLIC_KEY_FILE="+a.key,
		"DISABLE_UPDATE_NOTIFICATIONS=false", "OPENVIBELY_DISABLE_INSTALL_ID=1", "AUTH_ENABLED=false",
		"OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS=true", packagedE2EValidationTimeoutEnv(packagedUpdateOutcomeRolledBack),
		updateIntegrationHelperLogEnv+"="+filepath.Join(a.root, "helper.log"))
	if a.distribution == "binary" {
		port := freeTCPPort(a.t)
		a.baseURL = "http://127.0.0.1:" + port
		cmd.Env = append(cmd.Env, "PORT="+port)
	} else {
		config := writeRealDesktopConfig(a.t, a.root, a.data, a.service, a.key)
		cmd.Env = append(cmd.Env, "OPENVIBELY_DESKTOP_CONFIG_FILE="+config)
		cmd.Env = append(cmd.Env, desktopTestEnvironment()...)
	}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		a.t.Fatal(err)
	}
	a.cmd = cmd
	a.exited = make(chan struct{})
	go func() { defer close(a.exited); _ = cmd.Wait() }()
	if a.distribution == "desktop" {
		a.baseURL = waitForDesktopBaseURLFromLogs(a.t, logs)
	}
	a.wait(func(s CoordinatorSnapshot) bool { return s.CurrentVersion == "0.5.0" }, "initial version")
}
func (a *automaticAppE2E) stop() {
	if a.cmd != nil {
		_ = a.cmd.Process.Kill()
		<-a.exited
		a.cmd = nil
	}
	if a.baseURL != "" {
		u, _ := url.Parse(a.baseURL)
		if u != nil {
			killPort(a.t, u.Port())
		}
	}
	killExecutable(a.t, a.installed)
	command := ExecutableUpdateHelperCommand
	if a.distribution == "desktop" && runtime.GOOS == "darwin" {
		command = AppBundleUpdateHelperCommand
	}
	killExecutable(a.t, packagedUpdateHelperPath(a.installUnit, command))
}
func (a *automaticAppE2E) snapshot() (CoordinatorSnapshot, error) {
	var s CoordinatorSnapshot
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(a.baseURL + "/api/system/update")
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return s, fmt.Errorf("snapshot HTTP %d", resp.StatusCode)
	}
	err = json.NewDecoder(resp.Body).Decode(&s)
	return s, err
}
func (a *automaticAppE2E) wait(check func(CoordinatorSnapshot) bool, label string) CoordinatorSnapshot {
	a.t.Helper()
	deadline := time.Now().Add(150 * time.Second)
	var s CoordinatorSnapshot
	var err error
	for time.Now().Before(deadline) {
		s, err = a.snapshot()
		if err == nil && check(s) {
			return s
		}
		time.Sleep(200 * time.Millisecond)
	}
	a.t.Fatalf("waiting for %s: snapshot=%+v err=%v\n%s", label, s, err, a.logs())
	return s
}
func (a *automaticAppE2E) post(path, body string, want int) {
	a.t.Helper()
	client := &http.Client{Timeout: 150 * time.Second}
	req, err := http.NewRequest(http.MethodPost, a.baseURL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != want {
		a.t.Fatalf("POST %s HTTP %d: %s\n%s", path, resp.StatusCode, data, a.logs())
	}
}
func (a *automaticAppE2E) settings(enabled bool) {
	a.post("/settings/updates", fmt.Sprintf("automatic_updates=%t", enabled), 200)
	a.wait(func(s CoordinatorSnapshot) bool { return s.AutomaticUpdates == enabled }, "saved preference")
}
func (a *automaticAppE2E) stage() LocalStagedUpdate {
	a.t.Helper()
	var saved struct {
		Staged *LocalStagedUpdate `json:"staged_local"`
	}
	data, err := os.ReadFile(filepath.Join(a.data, "update-coordinator.json"))
	if err != nil {
		a.t.Fatal(err)
	}
	if err = json.Unmarshal(data, &saved); err != nil {
		a.t.Fatal(err)
	}
	if saved.Staged == nil {
		a.t.Fatal("missing staged record")
	}
	return *saved.Staged
}
func (a *automaticAppE2E) success() {
	a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateSucceeded && s.CurrentVersion == "0.6.0" }, "replacement health and success")
	assertGoBuildInfoVersion(a.t, a.installed, "0.6.0")
}

// Observe at least one real minute ticker, rather than treating an immediate
// snapshot as evidence that an automatic retry cannot happen.
func (a *automaticAppE2E) quiet(downloads *atomic.Int32, want int32) {
	a.t.Helper()
	deadline := time.Now().Add(65 * time.Second)
	for time.Now().Before(deadline) {
		s, err := a.snapshot()
		if err != nil {
			a.t.Fatal(err)
		}
		if s.CurrentVersion != "0.5.0" || s.State == StateWaitingForIdle || s.State == StateApplying || s.State == StateRestarting {
			a.t.Fatalf("unexpected automatic installation: %+v", s)
		}
		if downloads.Load() != want {
			a.t.Fatalf("unexpected download count %d, want %d", downloads.Load(), want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func runAutomaticPackagedScenario(t *testing.T, distribution string, builds map[string]string, scenario string) {
	root := t.TempDir()
	a := &automaticAppE2E{t: t, root: root, data: filepath.Join(root, "data"), distribution: distribution}
	if distribution == "desktop" && runtime.GOOS == "darwin" {
		a.installUnit, a.installed, _ = installDesktopUnit(t, root, "OpenVibely.app", builds["0.5.0"])
	} else {
		a.installed = filepath.Join(root, executableName("openvibely"))
		a.installUnit = a.installed
		if err := copyFile(builds["0.5.0"], a.installed, 0755); err != nil {
			t.Fatal(err)
		}
	}
	// Start with no release delivered until health and, where needed, work are ready.
	releaseGate := make(chan struct{})
	var releaseOnce sync.Once
	publish := func() { releaseOnce.Do(func() { close(releaseGate) }) }
	defer publish()
	var downloads atomic.Int32
	var failDownload atomic.Bool
	var downloadGate atomic.Pointer[chan struct{}]
	var expiry atomic.Int64
	replacement := "0.6.0"
	if scenario == "rollback_no_auto_retry" {
		replacement = "0.7.0"
	}
	var artifact []byte
	var filetype, filename, kind string
	if distribution == "desktop" {
		artifact, filetype, filename, kind = packageDesktopArtifact(t, builds[replacement])
	} else {
		artifact, filetype, filename = packageBinaryArtifact(t, builds[replacement])
		kind = "executable"
	}
	key, private := writeE2ETrustRoot(t, root)
	a.key = key
	server := serveControlledSignedRelease(t, artifact, filetype, filename, kind, "0.6.0", private, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/updates/check" {
			select {
			case <-releaseGate:
			case <-r.Context().Done():
				return true
			}
		}
		if strings.HasPrefix(r.URL.Path, "/artifact/") {
			downloads.Add(1)
			if gate := downloadGate.Load(); gate != nil {
				select {
				case <-*gate:
				case <-r.Context().Done():
					return true
				}
			}
			if failDownload.Load() {
				// Exceed the HTTP retry budget so this is one failed staging attempt.
				w.Header().Set("Retry-After", "121")
				http.Error(w, "injected download outage", 503)
				return true
			}
		}
		return false
	}, func(m *ReleaseMetadata) {
		if value := expiry.Load(); value != 0 {
			m.ExpiresAt = time.Unix(value, 0).UTC()
		}
	})
	a.service = server.URL
	t.Cleanup(server.Close)
	t.Cleanup(a.stop)
	manual := scenario == "disable_enable" || scenario == "reuse_stage"
	if scenario == "reuse_stage" {
		disableAutomaticUpdatesForE2E(t, a.data)
	}
	needsWork := scenario != "default_on" && scenario != "disable_enable" && scenario != "reuse_stage" && scenario != "rollback_no_auto_retry"
	workGate := make(chan struct{})
	var workOnce sync.Once
	finish := func() { workOnce.Do(func() { close(workGate) }) }
	defer finish()
	var work *pendingApprovalE2E
	if needsWork {
		work = preparePendingApprovalE2E(t, a.data, workGate)
	}
	a.start()
	if scenario == "disable_enable" {
		a.settings(false)
	}
	if scenario == "default_on" {
		s, err := a.snapshot()
		if err != nil || !s.AutomaticUpdates {
			t.Fatalf("default not enabled: %+v %v", s, err)
		}
	}
	if needsWork {
		a.post("/tasks/"+work.task.ID+"/run?project_id="+work.task.ProjectID, "", http.StatusNoContent)
		a.wait(func(s CoordinatorSnapshot) bool { return s.Drain.Active.Total() > 0 && work.requests.Load() > 0 }, "active task")
	}
	if scenario == "expired_metadata_retry" {
		expiry.Store(time.Now().Add(120 * time.Second).Unix())
	}
	publish()
	if scenario == "default_on" {
		a.success()
		return
	}
	if scenario == "rollback_no_auto_retry" {
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateRolledBack && s.CurrentVersion == "0.5.0" }, "automatic rollback")
		a.quiet(&downloads, 1)
		return
	}
	if manual {
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateAvailable && s.Staged }, "manual staged release")
		a.quiet(&downloads, 1)
		a.stop()
		a.start()
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateAvailable && s.Staged && !s.AutomaticUpdates }, "persisted disabled preference and stage")
		a.quiet(&downloads, 1)
		if scenario == "disable_enable" {
			a.settings(true)
		} else {
			a.post("/api/system/update/apply", "", 202)
		}
		a.success()
		if downloads.Load() != 1 {
			t.Fatal("reused stage downloaded again")
		}
		return
	}
	a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateWaitingForIdle && s.Drain.Active.Total() > 0 }, "automatic update waiting for task")
	if scenario == "wait_for_work" {
		// The real task remains blocked while automatic acceptance owns the drain.
		time.Sleep(time.Second)
		s, err := a.snapshot()
		if err != nil || s.State != StateWaitingForIdle || s.CurrentVersion != "0.5.0" {
			t.Fatalf("installed during work: %+v %v", s, err)
		}
		finish()
		a.success()
		return
	}
	staged := a.stage()
	if scenario == "failed_install_retry" {
		// A nonempty directory at the handoff-record path causes a real filesystem
		// failure in Apply on every platform, before replacement is authorized.
		blocker := packagedUpdateHelperOutcomePath(staged.InstallPath)
		if err := os.MkdirAll(blocker, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(blocker, "block"), []byte("blocked"), 0600); err != nil {
			t.Fatal(err)
		}
		finish()
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateFailed }, "failed installer handoff")
		if err := os.RemoveAll(blocker); err != nil {
			t.Fatal(err)
		}
	} else {
		a.post("/api/system/update/cancel", "", 200)
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateAvailable }, "cancelled update")
		finish()
		a.wait(func(s CoordinatorSnapshot) bool { return s.Drain.Active.Total() == 0 }, "completed task")
	}
	if scenario == "cancel_restart_retry" {
		a.stop()
		a.start()
		a.quiet(&downloads, 1)
	}
	switch scenario {
	case "missing_stage", "retry_download_failure", "interrupted_retry_download":
		if err := os.RemoveAll(staged.ArtifactPath); err != nil {
			t.Fatal(err)
		}
	case "corrupt_stage":
		path := staged.ArtifactPath
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			path = filepath.Join(path, "unexpected-file")
		}
		if err := os.WriteFile(path, []byte("corrupt staged artifact"), 0644); err != nil {
			t.Fatal(err)
		}
	case "expired_metadata_retry":
		a.stop()
		// Wait only for the signed test metadata, not the production refresh interval.
		for time.Now().Unix() <= expiry.Load() {
			time.Sleep(200 * time.Millisecond)
		}
		expiry.Store(0)
		// Simulate the daily check becoming due while the app was closed. Preserve
		// rollback protection, signature metadata and the automatic-attempt guard.
		path := filepath.Join(a.data, "update-state.json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var state persistedClientState
		if err = json.Unmarshal(raw, &state); err != nil {
			t.Fatal(err)
		}
		state.LastSuccessfulCheck = time.Now().Add(-25 * time.Hour)
		raw, err = json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		a.start()
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateAvailable && s.Release != nil }, "renewed metadata")
	}
	if scenario == "retry_download_failure" {
		failDownload.Store(true)
		a.post("/api/system/update/apply", "", 409)
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateFailed && !s.Staged }, "failed retry download")
		failDownload.Store(false)
		a.quiet(&downloads, 2)
	}
	if scenario == "interrupted_retry_download" {
		gate := make(chan struct{})
		downloadGate.Store(&gate)
		completed := make(chan struct{})
		go func() {
			defer close(completed)
			client := &http.Client{Timeout: 150 * time.Second}
			resp, _ := client.Post(a.baseURL+"/api/system/update/apply", "application/json", nil)
			if resp != nil {
				resp.Body.Close()
			}
		}()
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateStaging && downloads.Load() == 2 }, "blocked retry download")
		a.stop()
		close(gate)
		downloadGate.Store(nil)
		<-completed
		a.start()
		a.wait(func(s CoordinatorSnapshot) bool { return s.State == StateFailed }, "interrupted retry recovery")
		a.quiet(&downloads, 2)
	}
	a.post("/api/system/update/apply", "", 202)
	a.success()
	want := int32(2)
	if scenario == "cancel_restart_retry" || scenario == "failed_install_retry" {
		want = 1
	}
	if scenario == "retry_download_failure" || scenario == "interrupted_retry_download" {
		want = 3
	}
	if downloads.Load() != want {
		t.Fatalf("downloads=%d, want %d", downloads.Load(), want)
	}
}
