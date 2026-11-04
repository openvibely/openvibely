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
	return NewManager(env.PagePlugins, t.TempDir()), env, a.ID, b.ID
}

func enabledSales(t *testing.T, m *Manager, projectID string) *models.PagePluginInstance {
	t.Helper()
	ctx := context.Background()
	pkg, err := m.InstallExample(ctx, "admin")
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

	tools := pageintegration.BuildRuntimeTools(ia, m.Resolver(inA.ID, projA), m.Approvals, pageintegration.Caller{PrincipalID: "u"})
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
	tools := pageintegration.BuildRuntimeTools(m.Integration(r), m.Resolver(in.ID, projA), nil, pageintegration.Caller{})
	if out, _, isErr, _ := tools.Executor(ctx, pageintegration.ToolName("get_report"), nil); !isErr || !strings.Contains(out, "unavailable") {
		t.Fatalf("disabled page served agent tool: %s", out)
	}
}

func TestPackagePathsAndInstallSafety(t *testing.T) {
	m, _, projA, _ := newTestManager(t)
	ctx := context.Background()
	pkg, err := m.InstallExample(ctx, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(pkg.InstallPath, m.Root()) {
		t.Fatalf("package stored outside managed root: %s", pkg.InstallPath)
	}
	again, err := m.InstallExample(ctx, "admin")
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

func TestProcessBackendRequiresHashBoundApproval(t *testing.T) {
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
	if err := m.Enable(ctx, inA.ID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("backend enabled without approval: %v", err)
	}
	if err := m.Approve(ctx, inA.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(ctx, inA.ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := m.ProcessStatus(inA.ID); st != "running" {
		t.Fatalf("process status %s", st)
	}
	if err := m.Enable(ctx, inB.ID); !errors.Is(err, ErrApprovalRequired) {
		t.Fatal("approval leaked across project instances")
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

	// Tamper with the installed package: the approval no longer matches what would run.
	os.WriteFile(filepath.Join(pkg.InstallPath, "index.html"), []byte("<html>evil</html>"), 0o644)
	if err := m.Enable(ctx, inA.ID); !errors.Is(err, ErrContentChanged) {
		t.Fatalf("tampered package started: %v", err)
	}
	if after, _ := env.PagePlugins.GetInstance(ctx, inA.ID); after.Enabled {
		t.Fatal("instance left enabled after backend refused to start")
	}
}
