package pageplugins

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func newTestManager(t *testing.T) (*Manager, *teststores.Env, string, string) {
	t.Helper()
	env := teststores.New(t)
	ctx := context.Background()
	a := &models.Project{Name: "Alpha"}
	b := &models.Project{Name: "Beta"}
	if err := env.Projects.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := env.Projects.Create(ctx, b); err != nil {
		t.Fatal(err)
	}
	m := NewManager(env.PagePlugins, t.TempDir())
	t.Cleanup(m.StopAll)
	return m, env, a.ID, b.ID
}

func enabledSales(t *testing.T, m *Manager, projectID string) *models.PagePluginInstance {
	t.Helper()
	ctx := context.Background()
	pkg, err := m.InstallFromDir(ctx, "testdata/sales", "admin")
	if err != nil {
		t.Fatal(err)
	}
	in, err := m.CreateInstance(ctx, projectID, pkg.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	return in
}

func revisionOf(t *testing.T, raw json.RawMessage) int {
	t.Helper()
	var r struct {
		Revision int `json:"revision"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r.Revision
}

func TestSalesExampleTwoProjectsIsolated(t *testing.T) {
	m, _, projA, projB := newTestManager(t)
	ctx := context.Background()
	inA := enabledSales(t, m, projA)
	inB := enabledSales(t, m, projB)

	if _, err := m.Resolve(ctx, inA.ID, projB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-project resolve allowed: %v", err)
	}
	if _, err := m.Resolve(ctx, inA.ID, models.AllProjectsID); err != nil {
		t.Fatalf("all-projects resolve denied: %v", err)
	}
	rA, err := m.Resolve(ctx, inA.ID, projA)
	if err != nil {
		t.Fatal(err)
	}
	rB, _ := m.Resolve(ctx, inB.ID, projB)
	ia, ib := m.Integration(rA), m.Integration(rB)

	events, unsub, _ := m.Hub.Subscribe(inA.ID)
	defer unsub()
	out, err := pageintegration.Run(ctx, ia, pageintegration.Caller{Via: "page"}, "refresh_data", nil)
	if err != nil {
		t.Fatal(err)
	}
	if revisionOf(t, out) != 2 {
		t.Fatalf("page refresh revision = %d", revisionOf(t, out))
	}
	select {
	case ev := <-events:
		if ev.Type != "data.changed" || ev.Operation != "refresh_data" {
			t.Fatalf("bad event %+v", ev)
		}
	default:
		t.Fatal("refresh did not publish data.changed")
	}

	tools := pageintegration.BuildRuntimeTools(ia, m.Resolver(inA.ID, projA), pageintegration.Caller{PrincipalID: "u"})
	res, _, isErr, err := tools.Executor(ctx, pageintegration.ToolName("refresh_data"), json.RawMessage(`{}`))
	if err != nil || isErr || revisionOf(t, json.RawMessage(res)) != 3 {
		t.Fatalf("agent tool did not share page operation state: %s %v", res, err)
	}

	outB, err := pageintegration.Run(ctx, ib, pageintegration.Caller{Via: "page"}, "get_report", nil)
	if err != nil || revisionOf(t, outB) != 1 {
		t.Fatalf("project B state affected by project A: %s %v", outB, err)
	}

	if err := m.RemoveInstance(ctx, inA.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(ctx, inB.ID, projB); err != nil {
		t.Fatalf("removing A broke B: %v", err)
	}
	if _, err := pageintegration.Run(ctx, ia, pageintegration.Caller{}, "get_report", nil); err == nil {
		t.Fatal("removed instance still served operations")
	}
	list, _ := m.ListEnabled(ctx, models.AllProjectsID)
	if len(list) != 1 || list[0].ID != inB.ID {
		t.Fatalf("all-projects list = %+v", list)
	}
}

func TestDisableRetiresRuntimeResources(t *testing.T) {
	m, _, projA, _ := newTestManager(t)
	ctx := context.Background()
	in := enabledSales(t, m, projA)
	r, _ := m.Resolve(ctx, in.ID, projA)
	h, err := m.IssueHandle(r, "u")
	if err != nil {
		t.Fatal(err)
	}
	ch, _, _ := m.Hub.Subscribe(in.ID)
	m.Contexts.Set("u", r.Ref(), pageintegration.Context{Summary: "x"}, "page")

	if err := m.Disable(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.LookupHandle(h.Token); ok {
		t.Fatal("handle survived disable")
	}
	if _, open := <-ch; open {
		t.Fatal("event subscription survived disable")
	}
	if _, ok := m.Contexts.Get("u", r.Ref()); ok {
		t.Fatal("context survived disable")
	}
	if _, err := m.Resolve(ctx, in.ID, projA); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled instance resolved: %v", err)
	}
	tools := pageintegration.BuildRuntimeTools(m.Integration(r), m.Resolver(in.ID, projA), pageintegration.Caller{})
	if out, _, isErr, _ := tools.Executor(ctx, pageintegration.ToolName("get_report"), nil); !isErr || !strings.Contains(out, "unavailable") {
		t.Fatalf("disabled page served agent tool: %s", out)
	}
}

func TestPackagePathsAndInstallSafety(t *testing.T) {
	m, _, projA, _ := newTestManager(t)
	ctx := context.Background()
	pkg, err := m.InstallFromDir(ctx, "testdata/sales", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pkg.InstallPath, m.Root()) {
		t.Fatalf("package stored outside managed root: %s", pkg.InstallPath)
	}
	again, err := m.InstallFromDir(ctx, "testdata/sales", "admin")
	if err != nil || again.ID != pkg.ID {
		t.Fatalf("idempotent reinstall failed: %v", err)
	}
	in, _ := m.CreateInstance(ctx, projA, pkg.ID, "")
	m.Enable(ctx, in.ID)
	r, _ := m.Resolve(ctx, in.ID, projA)
	if _, err := m.AssetPath(r, "../../etc/passwd"); err == nil {
		t.Fatal("asset traversal accepted")
	}
	if p, err := m.AssetPath(r, "app.js"); err != nil || !strings.HasSuffix(p, "app.js") {
		t.Fatalf("asset lookup failed: %v", err)
	}
	if err := m.RemovePackage(ctx, pkg.ID); !errors.Is(err, ErrPackageInUse) {
		t.Fatalf("in-use package removed: %v", err)
	}
	m.RemoveInstance(ctx, in.ID)
	if err := m.RemovePackage(ctx, pkg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pkg.InstallPath); !os.IsNotExist(err) {
		t.Fatal("package files not removed")
	}
}

// writeProcessPackage builds a plugin bundle whose backend is this test binary.
func writeProcessPackage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, _ := os.Open(self)
	defer src.Close()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	dst, _ := os.OpenFile(filepath.Join(dir, "bin", "backend"), os.O_CREATE|os.O_WRONLY, 0o755)
	io.Copy(dst, src)
	dst.Close()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html></html>"), 0o644)
	man := `{"schema_version":1,"id":"test.proc","name":"Proc","version":"0.1.0",
	  "pages":[{"id":"main","title":"Proc","entry":"index.html"}],
	  "backend":{"executables":{"` + runtime.GOOS + "-" + runtime.GOARCH + `":"bin/backend"},"args":["--pp-helper-backend"]},
	  "permissions":["backend.exec","storage","events"],
	  "operations":[{"name":"echo","description":"Echo","kind":"read","expose_as_tool":true},
	                {"name":"notify","description":"Notify","kind":"read","emits":["data.changed"]}]}`
	os.WriteFile(filepath.Join(dir, ManifestFile), []byte(man), 0o644)
	return dir
}

func TestProcessBackendStartsWithoutSeparateApproval(t *testing.T) {
	if testing.Short() {
		t.Skip("copies the test binary")
	}
	m, env, projA, projB := newTestManager(t)
	defer m.StopAll()
	ctx := context.Background()
	pkg, err := m.InstallFromDir(ctx, writeProcessPackage(t), "admin")
	if err != nil {
		t.Fatal(err)
	}
	inA, _ := m.CreateInstance(ctx, projA, pkg.ID, "")
	inB, _ := m.CreateInstance(ctx, projB, pkg.ID, "")
	if err := m.Enable(ctx, inA.ID); err != nil {
		t.Fatalf("backend did not start: %v", err)
	}
	if err := m.Enable(ctx, inA.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.ProcessStatus(inA.ID); st != "running" {
		t.Fatalf("process status %s", st)
	}
	if err := m.Enable(ctx, inB.ID); err != nil {
		t.Fatal("second project backend failed")
	}

	r, _ := m.Resolve(ctx, inA.ID, projA)
	out, err := pageintegration.Run(ctx, m.Integration(r), pageintegration.Caller{Via: "page"}, "echo", json.RawMessage(`{"x":1}`))
	if err != nil || !strings.Contains(string(out), `"x":1`) {
		t.Fatalf("echo via backend: %s %v", out, err)
	}
	if _, err := pageintegration.Run(ctx, m.Integration(r), pageintegration.Caller{Via: "page"}, "notify", nil); err != nil {
		t.Fatal(err)
	}
	fresh, _ := env.PagePlugins.GetInstance(ctx, inA.ID)
	if !strings.Contains(fresh.StateJSON, `"n":7`) {
		t.Fatalf("state/set notification not persisted: %s", fresh.StateJSON)
	}

	if err := m.Disable(ctx, inA.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.ProcessStatus(inA.ID); st != "none" {
		t.Fatalf("process survived disable: %s", st)
	}

	// Change the page files without publishing: the saved content hash no longer matches.
	os.WriteFile(filepath.Join(m.pageSourceDir(inA.ID), "index.html"), []byte("<html>edited</html>"), 0o644)
	if err := m.Enable(ctx, inA.ID); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("tampered package started: %v", err)
	}
	if after, _ := env.PagePlugins.GetInstance(ctx, inA.ID); after.Enabled {
		t.Fatal("instance left enabled after backend refused to start")
	}
}

func TestDisableCancelsNativeOperationAndRejectsStaleIntegration(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	defer m.StopAll()
	ctx := context.Background()
	dir := writeProcessPackage(t)
	raw, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"operations":[`, `"operations":[{"name":"slow","description":"Wait","kind":"read"},`, 1))
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := m.InstallFromDir(ctx, dir, "admin")
	if err != nil {
		t.Fatal(err)
	}
	in, err := m.CreateInstance(ctx, project, pkg.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	r, err := m.Resolve(ctx, in.ID, project)
	if err != nil {
		t.Fatal(err)
	}
	integration := m.Integration(r)
	events, unsub, _ := m.Hub.Subscribe(in.ID)
	defer unsub()
	result := make(chan error, 1)
	go func() { _, err := integration.Invoke(ctx, pageintegration.Caller{}, "slow", nil); result <- err }()
	select {
	case <-events:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not start")
	}
	disabled := make(chan error, 1)
	go func() { disabled <- m.Disable(ctx, in.ID) }()
	select {
	case err := <-disabled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disable blocked on backend")
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("operation survived disable")
		}
	case <-time.After(time.Second):
		t.Fatal("operation not cancelled")
	}
	if _, err := integration.Invoke(ctx, pageintegration.Caller{}, "echo", nil); err == nil {
		t.Fatal("stale integration restarted disabled backend")
	}
	if state, _ := m.ProcessStatus(in.ID); state != "none" {
		t.Fatalf("backend remains: %s", state)
	}
	if err := m.Enable(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	fresh, err := m.Resolve(ctx, in.ID, project)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Integration(fresh).Invoke(ctx, pageintegration.Caller{}, "echo", nil); err != nil {
		t.Fatal(err)
	}
}

func TestDisableInterruptsNativeStartup(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	defer m.StopAll()
	ctx := context.Background()
	dir := writeProcessPackage(t)
	marker := filepath.Join(t.TempDir(), "starting")
	raw, _ := os.ReadFile(filepath.Join(dir, ManifestFile))
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Backend.Args = append(manifest.Backend.Args, "--block-init", marker)
	raw, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	pkg, err := m.InstallFromDir(ctx, dir, "admin")
	if err != nil {
		t.Fatal(err)
	}
	in, err := m.CreateInstance(ctx, project, pkg.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	enabled := make(chan error, 1)
	go func() { enabled <- m.Enable(ctx, in.ID) }()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("startup did not begin")
		}
	}
	disabled := make(chan error, 1)
	go func() { disabled <- m.Disable(ctx, in.ID) }()
	select {
	case err := <-disabled:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disable did not interrupt handshake")
	}
	if err := <-enabled; err == nil {
		t.Fatal("startup survived disable")
	}
	if state, _ := m.ProcessStatus(in.ID); state != "none" {
		t.Fatalf("backend remains: %s", state)
	}
	if _, err := m.Resolve(ctx, in.ID, project); err == nil {
		t.Fatal("instance still enabled")
	}
}

func TestCrashRecoveryVerifiesInstalledPackageBeforeEveryLaunch(t *testing.T) {
	for _, changed := range []string{"index.html", "bin/backend"} {
		t.Run(changed, func(t *testing.T) {
			m, _, project, _ := newTestManager(t)
			defer m.StopAll()
			ctx := context.Background()
			pkg, err := m.InstallFromDir(ctx, writeProcessPackage(t), "admin")
			if err != nil {
				t.Fatal(err)
			}
			in, err := m.CreateInstance(ctx, project, pkg.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := m.Enable(ctx, in.ID); err != nil {
				t.Fatal(err)
			}
			r, err := m.Resolve(ctx, in.ID, project)
			if err != nil {
				t.Fatal(err)
			}
			integration := m.Integration(r)
			m.mu.Lock()
			proc := m.procs[in.ID].proc
			m.mu.Unlock()
			crash := func() {
				t.Helper()
				proc.mu.Lock()
				exited := proc.exited
				proc.mu.Unlock()
				if _, err := proc.Call(ctx, "operation/invoke", map[string]any{"operation": "crash"}); err == nil {
					t.Fatal("backend did not crash")
				}
				select {
				case <-exited:
				case <-time.After(3 * time.Second):
					t.Fatal("backend did not exit")
				}
			}
			crash()
			// An unchanged approved package must still recover automatically.
			if _, err := integration.Invoke(ctx, pageintegration.Caller{}, "echo", nil); err != nil {
				t.Fatalf("unchanged recovery: %v", err)
			}
			m.mu.Lock()
			if m.procs[in.ID].proc != proc {
				t.Error("test must exercise the cached process restart path")
			}
			m.mu.Unlock()
			crash()
			want := ErrContentChanged

			f, err := os.OpenFile(filepath.Join(m.pageSourceDir(in.ID), changed), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			_, writeErr := f.WriteString("changed after installation")
			closeErr := f.Close()
			if writeErr != nil {
				t.Fatal(writeErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}

			// Call the cached process directly as well as through the manager. Neither
			// path may spawn anything after files change.
			if _, err := proc.Call(ctx, "operation/invoke", map[string]any{"operation": "echo"}); !errors.Is(err, want) {
				t.Fatalf("cached restart: got %v, want %v", err, want)
			}
			if proc.Running() || proc.PID() != 0 {
				t.Fatal("modified backend was launched")
			}
			if _, err := integration.Invoke(ctx, pageintegration.Caller{}, "echo", nil); !errors.Is(err, want) {
				t.Fatalf("manager restart: got %v, want %v", err, want)
			}
			if proc.Running() || proc.PID() != 0 {
				t.Fatal("manager launched modified backend")
			}
		})
	}
}

func TestHandleLifetimeRetireExpireAndDisable(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	defer m.StopAll()
	pkg, err := m.InstallFromDir(t.Context(), "testdata/sales", "admin")
	if err != nil {
		t.Fatal(err)
	}
	in, err := m.CreateInstance(t.Context(), project, pkg.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
	r, err := m.Resolve(t.Context(), in.ID, project)
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.IssueHandle(r, "local")
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.IssueHandle(r, "local")
	if err != nil {
		t.Fatal(err)
	}
	m.RetireHandle(first.Token)
	m.RetireHandle(first.Token)
	select {
	case <-first.Done():
	default:
		t.Fatal("retirement did not close handle")
	}
	select {
	case <-second.Done():
		t.Fatal("retirement affected another tab")
	default:
	}
	now := time.Now()
	m.now = func() time.Time { return now.Add(HandleTTL + time.Second) }
	if _, ok := m.LookupHandle(second.Token); ok {
		t.Fatal("expired handle accepted")
	}
	select {
	case <-second.Done():
	default:
		t.Fatal("expiration did not close handle")
	}
	m.now = time.Now
	third, err := m.IssueHandle(r, "local")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Disable(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-third.Done():
	default:
		t.Fatal("disable did not retire handle")
	}
}

func TestCrashRecoveryLoadsLatestSavedStatePerInstance(t *testing.T) {
	m, _, projectA, projectB := newTestManager(t)
	pkg, err := m.InstallFromDir(t.Context(), writeProcessPackage(t), "admin")
	if err != nil {
		t.Fatal(err)
	}
	start := func(project, state string) (*models.PagePluginInstance, *Process) {
		t.Helper()
		in, err := m.CreateInstance(t.Context(), project, pkg.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := m.Store().UpdateInstanceState(t.Context(), in.ID, state); err != nil {
			t.Fatal(err)
		}
		if err := m.Enable(t.Context(), in.ID); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		proc := m.procs[in.ID].proc
		m.mu.Unlock()
		return in, proc
	}
	a, pa := start(projectA, `{"n":1}`)
	b, pb := start(projectB, `{"n":20}`)
	assertState := func(p *Process, in *models.PagePluginInstance, want string) {
		t.Helper()
		raw, err := p.Call(t.Context(), "operation/invoke", map[string]any{"operation": "echo"})
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			State    json.RawMessage `json:"initialized_state"`
			Instance struct {
				ID        string `json:"id"`
				ProjectID string `json:"project_id"`
			} `json:"initialized_instance"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if string(out.State) != want || out.Instance.ID != in.ID || out.Instance.ProjectID != in.ProjectID {
			t.Fatalf("wrong restart snapshot: %s", raw)
		}
	}
	crash := func(p *Process) {
		t.Helper()
		p.mu.Lock()
		exited := p.exited
		p.mu.Unlock()
		if _, err := p.Call(t.Context(), "operation/invoke", map[string]any{"operation": "crash"}); err == nil {
			t.Fatal("backend did not crash")
		}
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Fatal("backend did not exit")
		}
	}
	assertState(pa, a, `{"n":1}`)
	// Exercise the real backend notification and persisted store, not an in-memory update.
	if _, err := pa.Call(t.Context(), "operation/invoke", map[string]any{"operation": "notify"}); err != nil {
		t.Fatal(err)
	}
	saved, err := m.Store().GetInstance(t.Context(), a.ID)
	if err != nil || saved.StateJSON != `{"n":7}` {
		t.Fatalf("state not saved: %+v %v", saved, err)
	}
	crash(pa)
	// The cached Process restarts directly: ensureProcess is not involved.
	assertState(pa, a, `{"n":7}`)
	crash(pb)
	assertState(pb, b, `{"n":20}`)
	// Clearing state is a valid update; do not fall back to an older nonempty value.
	if err := m.Store().UpdateInstanceState(t.Context(), a.ID, `{}`); err != nil {
		t.Fatal(err)
	}
	crash(pa)
	assertState(pa, a, `{}`)
}

func TestStartupObservations(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	in := enabledSales(t, m, project)
	r, err := m.Resolve(t.Context(), in.ID, project)
	if err != nil {
		t.Fatal(err)
	}
	status := func() StartupStatus { return m.StartupStatus(in.ID, r.Package.ContentHash) }
	if status().State != "not_opened" {
		t.Fatal(status())
	}
	now := time.Now()
	m.now = func() time.Time { return now }
	first, _ := m.IssueHandle(r, "local")
	if status().State != "loading" {
		t.Fatal(status())
	}
	if err := m.ReportStartup(first.Token, "loaded", ""); err != nil {
		t.Fatal(err)
	}
	if status().State != "loaded" {
		t.Fatal(status())
	}
	second, _ := m.IssueHandle(r, "local")
	_ = m.ReportStartup(first.Token, "failed", "old tab")
	if status().State != "loading" {
		t.Fatal("old tab overwrote latest load")
	}
	_ = m.ReportStartup(second.Token, "failed", "syntax error")
	_ = m.ReportStartup(second.Token, "loaded", "")
	if status().State != "failed" || status().Error != "syntax error" {
		t.Fatal(status())
	}
	third, _ := m.IssueHandle(r, "local")
	now = now.Add(StartupTimeout)
	if status().State != "failed" {
		t.Fatal("timeout missing")
	}
	if m.StartupStatus(in.ID, "changed").State != "not_opened" {
		t.Fatal("wrong package observation")
	}
	m.RetireHandle(third.Token)
	if m.ReportStartup(third.Token, "loaded", "") == nil {
		t.Fatal("retired token accepted")
	}
	if m.ReportStartup(second.Token, "failed", strings.Repeat("x", 2049)) == nil {
		t.Fatal("unbounded error")
	}
}

func TestManagerPreservesExhaustedRestartBudget(t *testing.T) {
	m, _, project, _ := newTestManager(t)
	pkg, err := m.InstallFromDir(t.Context(), writeProcessPackage(t), "admin")
	if err != nil {
		t.Fatal(err)
	}
	in, err := m.CreateInstance(t.Context(), project, pkg.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Enable(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
	r, err := m.Resolve(t.Context(), in.ID, project)
	if err != nil {
		t.Fatal(err)
	}
	integration := m.Integration(r)
	original := m.procs[in.ID].proc
	for i := 0; i <= MaxRestarts; i++ {
		original.mu.Lock()
		exited := original.exited
		original.mu.Unlock()
		_, _ = original.Call(t.Context(), "operation/invoke", map[string]any{"operation": "crash"})
		select {
		case <-exited:
		case <-time.After(3 * time.Second):
			t.Fatal("did not crash")
		}
		_, err = integration.Invoke(t.Context(), pageintegration.Caller{}, "echo", json.RawMessage(`{}`))
		if i < MaxRestarts && err != nil {
			t.Fatal(err)
		}
		if i == MaxRestarts && !errors.Is(err, ErrBackendFailed) {
			t.Fatalf("budget not enforced: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		_, err = integration.Invoke(t.Context(), pageintegration.Caller{}, "echo", json.RawMessage(`{}`))
		if !errors.Is(err, ErrBackendFailed) {
			t.Fatalf("budget reset: %v", err)
		}
		if m.procs[in.ID].proc != original {
			t.Fatal("replaced failed supervisor")
		}
	}
	if err = m.Enable(t.Context(), in.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = integration.Invoke(t.Context(), pageintegration.Caller{}, "echo", json.RawMessage(`{}`)); err != nil {
		t.Fatal("explicit retry failed", err)
	}
}

func TestRemovePackageKeepsRecordWhenFileDeletionFails(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX directory permissions as non-root")
	}
	m, _, _, _ := newTestManager(t)
	ctx := context.Background()
	pkg, err := m.InstallFromDir(ctx, "testdata/sales", "admin")
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(pkg.InstallPath)
	if err := os.Chmod(parent, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o755) })

	if err := m.RemovePackage(ctx, pkg.ID); err == nil {
		t.Fatal("file deletion failure was not reported")
	}
	if kept, err := m.store.GetPackage(ctx, pkg.ID); err != nil || kept == nil {
		t.Fatalf("package record deleted despite leftover files: %v", err)
	}

	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := m.RemovePackage(ctx, pkg.ID); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if _, err := os.Stat(pkg.InstallPath); !os.IsNotExist(err) {
		t.Fatal("package files not removed on retry")
	}
	if gone, _ := m.store.GetPackage(ctx, pkg.ID); gone != nil {
		t.Fatal("package record not removed on retry")
	}
}
