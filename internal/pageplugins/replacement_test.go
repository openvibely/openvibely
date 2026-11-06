package pageplugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func replacementBundle(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	manifest := `{"schema_version":1,"id":"custom.update","name":"Update","version":"` + version + `","pages":[{"id":"report","title":"Report","entry":"index.html"}]}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><h1>"+version+"</h1>"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestPageReplacementPreservesIdentityAndOtherProject(t *testing.T) {
	m, env, a, b := newTestManager(t)
	ctx := t.Context()
	v1 := replacementBundle(t, "1.0.0")
	first, err := m.Publish(ctx, v1, a, "", "task")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Publish(ctx, v1, b, "", "task")
	if err != nil {
		t.Fatal(err)
	}
	id := first.Instance.ID
	oldPkg, err := m.store.GetPackage(ctx, first.Instance.PackageID)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.store.UpdateInstanceState(ctx, id, `{"target":20000}`); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: a, Title: "Page chat", Category: models.TaskCategory("freezer"), Status: models.StatusPending}
	if err = env.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = m.store.SetConversationTask(ctx, id, task.ID); err != nil {
		t.Fatal(err)
	}
	r, _ := m.Resolve(ctx, id, a)
	hd, _ := m.IssueHandle(r, "local")
	updated, err := m.Publish(ctx, replacementBundle(t, "1.1.0"), a, "", "task")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Instance.ID != id || updated.Instance.Version != "1.1.0" || updated.Instance.StateJSON != `{"target":20000}` || !updated.Instance.Enabled {
		t.Fatalf("lost instance: %+v", updated.Instance)
	}
	chat, err := m.store.GetConversationTask(ctx, id)
	if err != nil || chat != task.ID {
		t.Fatal("chat lost", chat, err)
	}
	if _, ok := m.LookupHandle(hd.Token); ok {
		t.Fatal("old handle still active")
	}
	unchanged, _ := m.store.GetInstance(ctx, other.Instance.ID)
	if unchanged.Version != "1.0.0" || !unchanged.Enabled {
		t.Fatal("other project changed")
	}
	if _, err = os.Stat(oldPkg.InstallPath); err != nil {
		t.Fatal("deleted shared package", err)
	}
	if _, err = m.Publish(ctx, replacementBundle(t, "1.1.0"), b, "", "task"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(oldPkg.InstallPath); !os.IsNotExist(err) {
		t.Fatal("unused old package not removed", err)
	}
	pending, err := m.store.GetReplacement(ctx, id)
	if err != nil || pending != nil {
		t.Fatal("recovery record left after success", err)
	}
	_, err = m.PublishToInstance(ctx, replacementBundle(t, "1.2.0"), b, "", "task", id)
	if err == nil {
		t.Fatal("cross-project update accepted")
	}
}

func TestNativeReplacementApprovalAndRollback(t *testing.T) {
	m, _, a, _ := newTestManager(t)
	ctx := t.Context()
	source := writeProcessPackage(t)
	pkg, err := m.InstallFromDir(ctx, source, "admin")
	if err != nil {
		t.Fatal(err)
	}
	in, err := m.CreateInstance(ctx, a, pkg.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Approve(ctx, in.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = m.Enable(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	if err = m.store.UpdateInstanceState(ctx, in.ID, `{"n":42}`); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(source, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err = json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	man.Version = "2.0.0"
	exe, _ := man.Backend.SelectHostExecutable()
	if err = os.WriteFile(filepath.Join(source, exe), []byte("not executable"), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(man)
	if err = os.WriteFile(filepath.Join(source, "plugin.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := m.PublishToInstance(ctx, source, a, in.PageID, "task", in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ApprovalRequired || result.Instance.Enabled {
		t.Fatal("backend executed without approval")
	}
	if err = m.Enable(ctx, in.ID); err != ErrApprovalRequired {
		t.Fatal("approval bypass", err)
	}
	pending, err := m.store.GetReplacement(ctx, in.ID)
	if err != nil || pending == nil {
		t.Fatal("backup missing", err)
	}
	if err = m.RemovePackage(ctx, pkg.ID); err != ErrPackageInUse {
		t.Fatal("pending backup could be removed", err)
	}
	// Recreate the manager to exercise recovery metadata surviving an OV restart.
	m.StopAll()
	m = NewManager(m.store, m.root)
	t.Cleanup(m.StopAll)
	if err = m.Approve(ctx, in.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = m.Enable(ctx, in.ID); err == nil || !strings.Contains(err.Error(), "previous package restored") {
		t.Fatal("broken backend update accepted", err)
	}
	restored, err := m.store.GetInstance(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.PackageID != pkg.ID || !restored.Enabled || restored.StateJSON != `{"n":42}` || restored.ApprovedHash != pkg.ContentHash {
		t.Fatalf("rollback lost state: %+v", restored)
	}
	if p, _ := m.store.GetPackage(ctx, result.Instance.PackageID); p != nil {
		t.Fatal("failed replacement not cleaned up")
	}
	// A valid approved replacement commits and removes the old package.
	good := writeProcessPackage(t)
	bytes, err := os.ReadFile(filepath.Join(good, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(bytes, &man); err != nil {
		t.Fatal(err)
	}
	man.Version = "2.1.0"
	bytes, _ = json.Marshal(man)
	if err = os.WriteFile(filepath.Join(good, "plugin.json"), bytes, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = m.PublishToInstance(ctx, good, a, in.PageID, "task", in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Approve(ctx, in.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = m.Enable(ctx, in.ID); err != nil {
		t.Fatal(err)
	}
	current, _ := m.store.GetInstance(ctx, in.ID)
	if current.ID != in.ID || current.Version != "2.1.0" || current.StateJSON != `{"n":42}` || !current.Enabled {
		t.Fatalf("commit changed identity/state: %+v", current)
	}
	if old, _ := m.store.GetPackage(ctx, pkg.ID); old != nil {
		t.Fatal("old package retained after success")
	}
	if pending, _ := m.store.GetReplacement(ctx, in.ID); pending != nil {
		t.Fatal("backup retained after success")
	}

}
