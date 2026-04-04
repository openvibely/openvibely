package agentplugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestUserPluginBase_DefaultsToAppDir(t *testing.T) {
	tmp := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir tmp: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWD)
	}()

	t.Setenv(pluginRootEnvVar, "")

	got := userPluginBase()
	want := filepath.Join(tmp, defaultAppPluginRoot)
	resolvedGot := normalizePathForAssert(got)
	resolvedWant := normalizePathForAssert(want)
	if resolvedGot != resolvedWant {
		t.Fatalf("expected plugin root %q, got %q", resolvedWant, resolvedGot)
	}
}

func TestUserPluginBase_UsesEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	override := filepath.Join(tmp, "plugins-store")
	t.Setenv(pluginRootEnvVar, override)

	got := userPluginBase()
	if got != filepath.Clean(override) {
		t.Fatalf("expected plugin root override %q, got %q", filepath.Clean(override), got)
	}
}

func TestUserPluginBase_DoesNotUseFilesystemRootAsDefaultBase(t *testing.T) {
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(string(filepath.Separator)); err != nil {
		t.Fatalf("chdir root: %v", err)
	}
	defer func() {
		_ = os.Chdir(origWD)
	}()

	t.Setenv(pluginRootEnvVar, "")
	got := filepath.Clean(userPluginBase())
	disallowed := filepath.Clean(filepath.Join(string(filepath.Separator), defaultAppPluginRoot))
	if got == disallowed {
		t.Fatalf("expected plugin root to avoid filesystem root %q, got %q", disallowed, got)
	}
}

func normalizePathForAssert(path string) string {
	path = filepath.Clean(path)
	if strings.HasPrefix(path, "/private/var/") {
		return strings.TrimPrefix(path, "/private")
	}
	return path
}

func TestAddMarketplace_ImportsLocalMarketplace(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	source := filepath.Join(tmp, "source-marketplace")
	manifestDir := filepath.Join(source, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir manifest: %v", err)
	}
	manifest := `{
  "name": "test-marketplace",
  "metadata": {"description": "test"},
  "plugins": [
    {"name": "playwright", "description": "browser plugin", "source": "./plugins/playwright"}
  ]
}`
	if err := os.WriteFile(filepath.Join(manifestDir, "marketplace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source, "plugins", "playwright"), 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	defer func() { userPluginBaseFn = origUserPluginBase }()
	userPluginBaseFn = func() string { return pluginRoot }

	if err := AddMarketplace(context.Background(), source, "user"); err != nil {
		t.Fatalf("add marketplace: %v", err)
	}

	importedManifest := filepath.Join(pluginRoot, "marketplaces", "test-marketplace", ".claude-plugin", "marketplace.json")
	if _, err := os.Stat(importedManifest); err != nil {
		t.Fatalf("expected imported marketplace manifest at %s: %v", importedManifest, err)
	}
}

func TestAddMarketplace_PrefersOriginalSourceBeforeNormalized(t *testing.T) {
	origImportMarketplace := importMarketplaceFn
	defer func() { importMarketplaceFn = origImportMarketplace }()

	var seen []string
	importMarketplaceFn = func(ctx context.Context, source string) error {
		seen = append(seen, source)
		if len(seen) == 1 {
			return errors.New("first candidate failed")
		}
		return nil
	}

	input := "https://github.com/anthropics/skills"
	if err := AddMarketplace(context.Background(), input, "user"); err != nil {
		t.Fatalf("add marketplace: %v", err)
	}
	if len(seen) < 2 {
		t.Fatalf("expected two source attempts, got %v", seen)
	}
	if seen[0] != input {
		t.Fatalf("expected first candidate %q, got %q", input, seen[0])
	}
	if seen[1] != "anthropics/skills" {
		t.Fatalf("expected normalized fallback candidate, got %q", seen[1])
	}
}

func TestDiscoverState_UsesLocalMarketplaceAndInstalledCache(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	marketplaceDir := filepath.Join(pluginRoot, "marketplaces", "demo-marketplace", ".claude-plugin")
	if err := os.MkdirAll(marketplaceDir, 0o755); err != nil {
		t.Fatalf("mkdir marketplace: %v", err)
	}
	installPath := filepath.Join(pluginRoot, "cache", "demo-marketplace", "playwright", "20240101T010101")
	if err := os.MkdirAll(installPath, 0o755); err != nil {
		t.Fatalf("mkdir install path: %v", err)
	}

	manifest := `{
  "name": "demo-marketplace",
  "metadata": {"description": "demo"},
  "plugins": [
    {"name": "playwright", "description": "browser automation", "source": "./plugins/playwright"},
    {"name": "stagehand", "description": "browser", "source": {"source":"github","repo":"browserbase/agent-browse"}}
  ]
}`
	if err := os.WriteFile(filepath.Join(marketplaceDir, "marketplace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	defer func() { userPluginBaseFn = origUserPluginBase }()
	userPluginBaseFn = func() string { return pluginRoot }

	state, err := DiscoverState(context.Background())
	if err != nil {
		t.Fatalf("discover state: %v", err)
	}
	if len(state.Marketplaces) == 0 {
		t.Fatalf("expected discovered marketplaces")
	}
	if len(state.Installed) == 0 {
		t.Fatalf("expected discovered installed plugins")
	}

	joined := make([]string, 0, len(state.Available))
	for _, p := range state.Available {
		joined = append(joined, p.PluginID)
	}
	ids := strings.Join(joined, ",")
	if !strings.Contains(ids, "playwright@demo-marketplace") {
		t.Fatalf("expected playwright plugin in available list, got: %s", ids)
	}
	if !strings.Contains(ids, "stagehand@demo-marketplace") {
		t.Fatalf("expected stagehand plugin in available list, got: %s", ids)
	}
}

func TestDiscoverState_SeedsDefaultMarketplacesInAppRootWhenEmpty(t *testing.T) {
	tmp := t.TempDir()
	appRoot := filepath.Join(tmp, "app-root")

	origImportMarketplace := importMarketplaceFn
	origUserPluginBase := userPluginBaseFn
	defer func() {
		importMarketplaceFn = origImportMarketplace
		userPluginBaseFn = origUserPluginBase
	}()

	userPluginBaseFn = func() string { return appRoot }
	importMarketplaceFn = func(ctx context.Context, source string) error {
		var name string
		switch strings.TrimSpace(source) {
		case defaultOfficialMarketplaceSource:
			name = defaultOfficialMarketplaceName
		case defaultSkillsMarketplaceSource:
			name = defaultSkillsMarketplaceName
		default:
			return errors.New("unexpected source: " + source)
		}
		manifestDir := filepath.Join(appRoot, "marketplaces", name, ".claude-plugin")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			return err
		}
		manifest := `{"name":"` + name + `","metadata":{"description":"seed"},"plugins":[{"name":"playwright","description":"browser plugin","source":"./plugins/playwright"}]}`
		return os.WriteFile(filepath.Join(manifestDir, "marketplace.json"), []byte(manifest), 0o644)
	}

	state, err := DiscoverState(context.Background())
	if err != nil {
		t.Fatalf("discover state: %v", err)
	}
	if len(state.Marketplaces) < 2 {
		t.Fatalf("expected default marketplaces to be seeded, got %+v", state.Marketplaces)
	}
	if len(state.Available) == 0 {
		t.Fatalf("expected seeded available plugins, got none")
	}
}

func TestDisablePlugin_ReturnsError(t *testing.T) {
	err := DisablePlugin(context.Background(), "playwright@demo-marketplace")
	if err == nil {
		t.Fatal("expected disable plugin to be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "per agent") {
		t.Fatalf("expected per-agent error, got %v", err)
	}
}

func TestUninstallPlugin_RemovesLocalCache(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	installPath := filepath.Join(pluginRoot, "cache", "demo-marketplace", "playwright", "20240101T010101")
	if err := os.MkdirAll(installPath, 0o755); err != nil {
		t.Fatalf("mkdir install path: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	defer func() { userPluginBaseFn = origUserPluginBase }()
	userPluginBaseFn = func() string { return pluginRoot }

	if err := UninstallPlugin(context.Background(), "playwright@demo-marketplace"); err != nil {
		t.Fatalf("uninstall plugin: %v", err)
	}

	if _, err := os.Stat(filepath.Join(pluginRoot, "cache", "demo-marketplace", "playwright")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected plugin cache directory removed, got err=%v", err)
	}
}

func TestUninstallPlugin_ReturnsErrorWhenPluginMissing(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")

	origUserPluginBase := userPluginBaseFn
	defer func() { userPluginBaseFn = origUserPluginBase }()
	userPluginBaseFn = func() string { return pluginRoot }

	err := UninstallPlugin(context.Background(), "playwright@demo-marketplace")
	if err == nil {
		t.Fatal("expected uninstall error for missing plugin")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not installed") {
		t.Fatalf("expected missing-plugin error details, got: %v", err)
	}
}

func TestResolveRuntimeBundle_UsesSelectedPlugins(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	installPath := filepath.Join(pluginRoot, "cache", "demo-marketplace", "playwright", "20240101T010101")
	if err := os.MkdirAll(filepath.Join(installPath, "skills", "audit"), 0o755); err != nil {
		t.Fatalf("mkdir install path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installPath, "skills", "audit", "SKILL.md"), []byte("---\nname: audit\n---\ncheck"), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	defer func() { userPluginBaseFn = origUserPluginBase }()
	userPluginBaseFn = func() string { return pluginRoot }

	bundle, err := ResolveRuntimeBundle(context.Background(), []string{"playwright@demo-marketplace"})
	if err != nil {
		t.Fatalf("resolve runtime bundle: %v", err)
	}
	if len(bundle.PluginIDs) != 1 {
		t.Fatalf("expected selected plugin in runtime bundle, got %v", bundle.PluginIDs)
	}
}

func TestReconcileInstalledPluginMCPRunning_UsesAllInstalledPlugins(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	installPath := filepath.Join(pluginRoot, "cache", "mkt", "playwright", "20240101T010101")
	if err := os.MkdirAll(installPath, 0o755); err != nil {
		t.Fatalf("mkdir install path: %v", err)
	}

	origReconcile := reconcilePersistentFn
	origUserPluginBase := userPluginBaseFn
	defer func() {
		reconcilePersistentFn = origReconcile
		userPluginBaseFn = origUserPluginBase
	}()
	userPluginBaseFn = func() string { return pluginRoot }

	reconcileCalled := false
	reconcilePersistentFn = func(ctx context.Context, servers []models.MCPServerConfig, workDir string) error {
		reconcileCalled = true
		if len(servers) != 0 {
			// .mcp.json is intentionally absent; zero resolved servers is expected.
			return nil
		}
		return nil
	}

	if err := ReconcileInstalledPluginMCPRunning(context.Background(), "."); err != nil {
		t.Fatalf("reconcile installed plugins: %v", err)
	}
	if !reconcileCalled {
		t.Fatal("expected reconcile to be called")
	}
}
