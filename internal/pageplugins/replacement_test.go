package pageplugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/persistence"
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
	first, err := m.PublishToInstance(ctx, v1, a, "", "task", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.PublishToInstance(ctx, v1, b, "", "task", "")
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
	task := &models.Task{ProjectID: a, Title: "Page chat", Category: models.CategoryPageChat, Status: models.StatusPending}
	if err = env.Tasks.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err = m.store.SetConversationTask(ctx, id, task.ID); err != nil {
		t.Fatal(err)
	}
	r, _ := m.Resolve(ctx, id, a)
	hd, _ := m.IssueHandle(r, "local")
	updated, err := m.PublishToInstance(ctx, replacementBundle(t, "1.1.0"), a, "", "task", "")
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
	if _, err = m.PublishToInstance(ctx, replacementBundle(t, "1.1.0"), b, "", "task", ""); err != nil {
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

func TestNativeReplacementRetainsFailedEditsAndCanRetry(t *testing.T) {
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
	_, err = m.PublishToInstance(ctx, source, a, in.PageID, "task", in.ID)
	if err == nil {
		t.Fatal("invalid backend unexpectedly started")
	}
	// Failed publication retains the edited files and chat; no code rollback.
	m.StopAll()
	m = NewManager(m.store, m.root)
	t.Cleanup(m.StopAll)
	failed, err := m.store.GetInstance(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Version != "2.0.0" || !failed.Enabled || failed.StateJSON != `{"n":42}` {
		t.Fatalf("failed update lost identity/state: %+v", failed)
	}
	body, err := os.ReadFile(filepath.Join(m.pageSourceDir(in.ID), exe))
	if err != nil || string(body) != "not executable" {
		t.Fatalf("failed edits lost: %q %v", body, err)
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
	_, err = m.PublishToInstance(ctx, good, a, in.PageID, "task", in.ID)
	if err != nil {
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

func TestStaleIntegrationCannotRunOperationsAfterUpdate(t *testing.T) {
	m, _, a, _ := newTestManager(t)
	ctx := t.Context()
	in := enabledSales(t, m, a)
	r, err := m.Resolve(ctx, in.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	stale := m.Integration(r)
	if err = m.store.UpdateInstanceState(ctx, in.ID, `{"n":100}`); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	manifest := `{"schema_version":1,"id":"openvibely.sales-example","name":"Sales","version":"2.0.0","pages":[{"id":"sales","title":"Sales","entry":"index.html"}]}`
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = m.PublishToInstance(ctx, dir, a, in.PageID, "task", in.ID); err != nil {
		t.Fatal(err)
	}

	if _, err = stale.Invoke(ctx, pageintegration.Caller{Via: "agent"}, "refresh_data", nil); !errors.Is(err, ErrPackageChanged) {
		t.Fatalf("stale operation not rejected: %v", err)
	}
	fresh, _ := m.store.GetInstance(ctx, in.ID)
	if fresh.StateJSON != `{"n":100}` {
		t.Fatalf("stale operation changed state: %s", fresh.StateJSON)
	}
}

func TestRepublishKeepsAdminDisabledPageOff(t *testing.T) {
	m, _, a, _ := newTestManager(t)
	ctx := t.Context()
	first, err := m.PublishToInstance(ctx, replacementBundle(t, "1.0.0"), a, "", "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Instance.Enabled {
		t.Fatal("new frontend-only page was not enabled")
	}
	id := first.Instance.ID
	if err = m.Disable(ctx, id); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"1.0.0", "1.1.0"} {
		result, err := m.PublishToInstance(ctx, replacementBundle(t, version), a, "", "task", "")
		if err != nil {
			t.Fatal(err)
		}
		if result.Instance.ID != id || result.Instance.Enabled || result.Instance.Version != version {
			t.Fatalf("republish %s: %+v", version, result.Instance)
		}
		if pending, _ := m.store.GetReplacement(ctx, id); pending != nil {
			t.Fatalf("republish %s left an unfinished update", version)
		}
	}
	if err = m.Enable(ctx, id); err != nil {
		t.Fatal(err)
	}
	result, err := m.PublishToInstance(ctx, replacementBundle(t, "1.2.0"), a, "", "task", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Instance.Enabled || result.Instance.Version != "1.2.0" {
		t.Fatalf("enabled page not kept on after update: %+v", result.Instance)
	}
}

type failingCreateInstanceStore struct {
	persistence.PagePluginStore
}

func (failingCreateInstanceStore) CreateInstance(context.Context, *models.PagePluginInstance) error {
	return errors.New("create instance failed")
}

func TestPublishDiscardsNewPackageWhenPageCreationFails(t *testing.T) {
	m, env, a, _ := newTestManager(t)
	ctx := t.Context()
	m.store = failingCreateInstanceStore{PagePluginStore: env.PagePlugins}
	if _, err := m.PublishToInstance(ctx, replacementBundle(t, "1.0.0"), a, "", "task", ""); err == nil {
		t.Fatal("publish should fail")
	}
	pkgs, err := env.PagePlugins.ListPackages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 0 {
		t.Fatalf("orphaned packages left behind: %d", len(pkgs))
	}
	if entries, _ := os.ReadDir(filepath.Join(m.Root(), "custom.update")); len(entries) != 0 {
		t.Fatalf("orphaned package files left behind: %d", len(entries))
	}
}

func TestPublishKeepsPreexistingPackageWhenPageCreationFails(t *testing.T) {
	m, env, a, b := newTestManager(t)
	ctx := t.Context()
	bundle := replacementBundle(t, "1.0.0")
	if _, err := m.PublishToInstance(ctx, bundle, a, "", "task", ""); err != nil {
		t.Fatal(err)
	}
	m.store = failingCreateInstanceStore{PagePluginStore: env.PagePlugins}
	if _, err := m.PublishToInstance(ctx, bundle, b, "", "task", ""); err == nil {
		t.Fatal("publish should fail")
	}
	pkgs, err := env.PagePlugins.ListPackages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("shared package must remain, got %d", len(pkgs))
	}
}
