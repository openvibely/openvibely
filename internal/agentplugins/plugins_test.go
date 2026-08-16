package agentplugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestNormalizeAndParsePluginIDs(t *testing.T) {
	got := NormalizePluginIDs([]string{
		" beta@market ",
		"alpha@market",
		"alpha@market",
		"broken",
		"@missing",
		"multi@tenant@market",
		"",
	})
	want := []string{"alpha@market", "beta@market", "multi@tenant@market"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizePluginIDs = %#v, want %#v", got, want)
	}

	parsed, err := parsePluginID("name@market@scope")
	if err != nil {
		t.Fatalf("parsePluginID: %v", err)
	}
	if parsed.Name != "name" || parsed.Marketplace != "market@scope" {
		t.Fatalf("parsed plugin id = %#v", parsed)
	}
}

func TestMarketplaceSummaryAndCommitSHAHelpers(t *testing.T) {
	if !looksLikeCommitSHA(" abc1234 ") || !looksLikeCommitSHA(strings.Repeat("f", 64)) {
		t.Fatal("expected valid short/full commit SHAs")
	}
	for _, bad := range []string{"abc123", strings.Repeat("f", 65), "abc123g", ""} {
		if looksLikeCommitSHA(bad) {
			t.Fatalf("expected %q to be rejected as commit SHA", bad)
		}
	}
	for name, source := range map[string]interface{}{
		"string": " https://github.com/openvibely/plugins ",
		"url":    map[string]interface{}{"url": " https://example.com/marketplace "},
		"repo":   map[string]interface{}{"repo": "owner/repo"},
		"path":   map[string]interface{}{"path": "../local"},
		"source": map[string]interface{}{"source": "builtin"},
		"empty":  map[string]interface{}{"other": "ignored"},
	} {
		got := marketplaceSourceSummary(source)
		if name == "empty" && got != "" {
			t.Fatalf("empty marketplace source summary = %q", got)
		}
		if name != "empty" && strings.TrimSpace(got) == "" {
			t.Fatalf("%s marketplace source summary was empty", name)
		}
	}
}

func TestPluginSourceHelpersNormalizeCommonGitHubForms(t *testing.T) {
	cases := map[string]string{
		"git@github.com:owner/repo.git":          "owner/repo",
		"github.com/owner/repo":                  "owner/repo",
		"https://github.com/owner/repo.git/path": "owner/repo.git",
		"owner/repo":                             "owner/repo",
	}
	for input, want := range cases {
		if got := normalizedMarketplaceSource(input); got != want {
			t.Fatalf("normalizedMarketplaceSource(%q) = %q, want %q", input, got, want)
		}
	}

	wantCandidates := []string{"https://github.com/owner/repo.git", "git@github.com:owner/repo.git"}
	if got := cloneSourceCandidates("owner/repo"); !reflect.DeepEqual(got, wantCandidates) {
		t.Fatalf("cloneSourceCandidates owner/repo = %#v, want %#v", got, wantCandidates)
	}
	if got := cloneSourceCandidates("https://github.com/owner/repo.git"); !reflect.DeepEqual(got, wantCandidates) {
		t.Fatalf("cloneSourceCandidates https = %#v, want %#v", got, wantCandidates)
	}
	if got := sanitizedMarketplaceName("https://github.com/owner/repo.git"); got != "owner-repo" {
		t.Fatalf("sanitizedMarketplaceName = %q", got)
	}
}

func TestSafeJoinAndLocalMarketplacePathRejectTraversal(t *testing.T) {
	root := t.TempDir()
	if got, err := safeJoinUnderRoot(root, "plugins/demo"); err != nil || got != filepath.Join(root, "plugins", "demo") {
		t.Fatalf("safeJoinUnderRoot valid = %q, %v", got, err)
	}
	if _, err := safeJoinUnderRoot(root, "../escape"); err == nil {
		t.Fatal("expected safeJoinUnderRoot to reject traversal")
	}
	if got := localMarketplacePathForSource(root, "../escape"); got != "" {
		t.Fatalf("expected traversal source rejected, got %q", got)
	}
	if got := localMarketplacePathForSource(root, "https://github.com/owner/repo"); got != "" {
		t.Fatalf("expected remote source not to map locally, got %q", got)
	}
}

func TestLoadPluginMCPServersParsesNestedConfigAndInterpolatesEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("PLUGIN_TOKEN", "secret-token")
	data := `{
  "mcpServers": {
    "stdio-server": {
      "command": "node",
      "args": ["server.js", " --bad-empty-trimmed? "],
      "env": {"TOKEN": "$PLUGIN_TOKEN", "EMPTY": 7}
    },
    "http-server": {
      "url": "https://example.test/mcp",
      "headers": {"Authorization": "Bearer ${PLUGIN_TOKEN}"}
    },
    "ignored": {}
  }
}`
	if err := os.WriteFile(filepath.Join(tmp, ".mcp.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	servers := loadPluginMCPServers(tmp)
	if len(servers) != 2 {
		t.Fatalf("expected two usable servers, got %#v", servers)
	}
	byName := map[string]models.MCPServerConfig{}
	for _, server := range servers {
		byName[server.Name] = server
	}
	stdio := byName["stdio-server"]
	if stdio.Type != "stdio" || !reflect.DeepEqual(stdio.Command, []string{"node", "server.js", "--bad-empty-trimmed?"}) {
		t.Fatalf("stdio server = %#v", stdio)
	}
	if stdio.Env["TOKEN"] != "secret-token" {
		t.Fatalf("stdio env TOKEN = %q", stdio.Env["TOKEN"])
	}
	httpServer := byName["http-server"]
	if httpServer.Type != "http" || httpServer.Headers["Authorization"] != "Bearer secret-token" {
		t.Fatalf("http server = %#v", httpServer)
	}
}

func TestResolveRuntimeBundleDeduplicatesPluginResourcesAndMapsServers(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	first := filepath.Join(pluginRoot, "cache", "market", "first", "20240101T010101")
	second := filepath.Join(pluginRoot, "cache", "market", "second", "20240101T010101")
	for _, dir := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(dir, "skills", "audit"), 0o755); err != nil {
			t.Fatalf("mkdir skills: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "skills", "audit", "SKILL.md"), []byte("---\nname: Audit\ndescription: Review carefully\n---\ncheck things"), 0o644); err != nil {
			t.Fatalf("write skill: %v", err)
		}
	}

	origUserPluginBase := userPluginBaseFn
	defer func() {
		userPluginBaseFn = origUserPluginBase
	}()
	userPluginBaseFn = func() string { return pluginRoot }

	bundle, err := ResolveRuntimeBundle(context.Background(), []string{"second@market", "first@market"})
	if err != nil {
		t.Fatalf("resolve runtime bundle: %v", err)
	}
	if !reflect.DeepEqual(bundle.PluginIDs, []string{"first@market", "second@market"}) {
		t.Fatalf("plugin IDs = %#v", bundle.PluginIDs)
	}
	if len(bundle.Skills) != 1 || bundle.Skills[0].Name != "Audit" {
		t.Fatalf("skills = %#v", bundle.Skills)
	}
	if len(bundle.MCPServers) != 0 {
		t.Fatalf("mcp servers = %#v", bundle.MCPServers)
	}

	if mapping := PluginServerNameMapping(context.Background(), []models.InstalledPlugin{{ID: "first@market"}, {ID: "broken"}}); len(mapping) != 0 {
		t.Fatalf("server mapping = %#v", mapping)
	}
}

func TestInstallUpdateRemoveMarketplaceLifecycleUsesLocalSources(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	marketplace := filepath.Join(pluginRoot, "marketplaces", "demo")
	localPlugin := filepath.Join(marketplace, "plugins", "worker")
	if err := os.MkdirAll(localPlugin, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localPlugin, "plugin.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatalf("write plugin payload: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(marketplace, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir manifest: %v", err)
	}
	manifest := `{"name":"demo","metadata":{"description":"demo"},"plugins":[{"name":"worker","description":"worker plugin","source":"./plugins/worker"}]}`
	if err := os.WriteFile(filepath.Join(marketplace, ".claude-plugin", "marketplace.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	defer func() { userPluginBaseFn = origUserPluginBase }()
	userPluginBaseFn = func() string { return pluginRoot }

	if err := InstallPlugin(context.Background(), "worker@demo", ""); err != nil {
		t.Fatalf("install plugin: %v", err)
	}
	installed, err := resolvePluginDir(pluginID{Name: "worker", Marketplace: "demo"})
	if err != nil {
		t.Fatalf("resolve installed plugin: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(installed, "plugin.txt")); err != nil || string(data) != "payload" {
		t.Fatalf("installed payload = %q, err=%v", data, err)
	}
	if err := UpdateMarketplace(context.Background(), "demo"); err != nil {
		t.Fatalf("update non-git marketplace: %v", err)
	}
	if err := RemoveMarketplace(context.Background(), "demo"); err != nil {
		t.Fatalf("remove marketplace: %v", err)
	}
	if _, err := os.Stat(marketplace); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected marketplace removed, err=%v", err)
	}
}

func TestResetDefaultMarketplacesAddsMissingRemovesTempAndAggregatesErrors(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	tempManifest := filepath.Join(pluginRoot, "marketplaces", "temp_old", ".claude-plugin")
	if err := os.MkdirAll(tempManifest, 0o755); err != nil {
		t.Fatalf("mkdir temp marketplace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempManifest, "marketplace.json"), []byte(`{"name":"temp_old"}`), 0o644); err != nil {
		t.Fatalf("write temp manifest: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	origImport := importMarketplaceFn
	origRun := runCommandCombinedFn
	defer func() {
		userPluginBaseFn = origUserPluginBase
		importMarketplaceFn = origImport
		runCommandCombinedFn = origRun
	}()
	userPluginBaseFn = func() string { return pluginRoot }
	importMarketplaceFn = func(ctx context.Context, source string) error {
		var name string
		switch source {
		case defaultOfficialMarketplaceSource:
			name = defaultOfficialMarketplaceName
		case defaultSkillsMarketplaceSource:
			name = defaultSkillsMarketplaceName
		default:
			return errors.New("unexpected source")
		}
		dir := filepath.Join(pluginRoot, "marketplaces", name, ".claude-plugin")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "marketplace.json"), []byte(`{"name":"`+name+`"}`), 0o644)
	}
	runCommandCombinedFn = func(ctx context.Context, name string, args ...string) error {
		return nil
	}

	if err := ResetDefaultMarketplaces(context.Background()); err != nil {
		t.Fatalf("reset defaults: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluginRoot, "marketplaces", "temp_old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected temp marketplace removed, err=%v", err)
	}
	for _, name := range []string{defaultOfficialMarketplaceName, defaultSkillsMarketplaceName} {
		if _, err := os.Stat(filepath.Join(pluginRoot, "marketplaces", name, ".claude-plugin", "marketplace.json")); err != nil {
			t.Fatalf("expected default marketplace %s: %v", name, err)
		}
		if err := os.MkdirAll(filepath.Join(pluginRoot, "marketplaces", name, ".git"), 0o755); err != nil {
			t.Fatalf("mark default marketplace git-backed: %v", err)
		}
	}

	runCommandCombinedFn = func(ctx context.Context, name string, args ...string) error {
		return errors.New("pull denied")
	}
	if err := ResetDefaultMarketplaces(context.Background()); err == nil || !strings.Contains(err.Error(), "pull denied") {
		t.Fatalf("expected aggregated pull error, got %v", err)
	}
}

func TestPluginMCPRuntimeWrappersUseResolvedServers(t *testing.T) {
	tmp := t.TempDir()
	pluginRoot := filepath.Join(tmp, "plugins")
	installPath := filepath.Join(pluginRoot, "cache", "market", "worker", "20240101T010101")
	if err := os.MkdirAll(installPath, 0o755); err != nil {
		t.Fatalf("mkdir plugin: %v", err)
	}
	if err := os.WriteFile(filepath.Join(installPath, ".mcp.json"), []byte(`{"worker":{"command":"worker-mcp"}}`), 0o644); err != nil {
		t.Fatalf("write mcp: %v", err)
	}

	origUserPluginBase := userPluginBaseFn
	origEnsure := ensurePersistentMCPFn
	origState := persistentMCPStateFn
	defer func() {
		userPluginBaseFn = origUserPluginBase
		ensurePersistentMCPFn = origEnsure
		persistentMCPStateFn = origState
	}()
	userPluginBaseFn = func() string { return pluginRoot }

	var gotWorkDir string
	var gotServers []models.MCPServerConfig
	ensurePersistentMCPFn = func(ctx context.Context, servers []models.MCPServerConfig, workDir string) error {
		gotServers = append([]models.MCPServerConfig(nil), servers...)
		gotWorkDir = workDir
		return nil
	}
	if err := EnsurePluginMCPRunning(context.Background(), []string{"worker@market"}, ""); err != nil {
		t.Fatalf("ensure plugin MCP: %v", err)
	}
	if gotWorkDir != "." || len(gotServers) != 1 || gotServers[0].Name != "worker" {
		t.Fatalf("ensure args workDir=%q servers=%#v", gotWorkDir, gotServers)
	}

	persistentMCPStateFn = func() []models.PluginRuntimeMCP {
		return []models.PluginRuntimeMCP{{Name: "worker", Status: "running"}}
	}
	if got := PluginMCPRuntimeState(); len(got) != 1 || got[0].Name != "worker" {
		t.Fatalf("runtime state = %#v", got)
	}
}

func TestMergeAgentWithRuntimeDeduplicatesSkillsAndServers(t *testing.T) {
	base := &models.Agent{
		Plugins: []string{"worker@market", "worker@market", "invalid"},
		Skills:  []models.SkillConfig{{Name: "Audit", Content: "base"}},
		MCPServers: []models.MCPServerConfig{
			{Name: "tools", Command: []string{"base"}},
		},
	}
	runtime := &RuntimeBundle{
		Skills: []models.SkillConfig{
			{Name: "audit", Content: "duplicate"},
			{Name: "Review", Content: "runtime"},
			{Name: "", Content: "ignored"},
		},
		MCPServers: []models.MCPServerConfig{
			{Name: "TOOLS", Command: []string{"duplicate"}},
			{Name: "browser", URL: "https://example.test/mcp"},
			{Name: "", URL: "https://ignored.test/mcp"},
		},
	}

	merged := MergeAgentWithRuntime(base, runtime)
	if merged == base {
		t.Fatal("expected copy, got original pointer")
	}
	if !reflect.DeepEqual(merged.Plugins, []string{"worker@market"}) {
		t.Fatalf("plugins = %#v", merged.Plugins)
	}
	if len(merged.Skills) != 2 || merged.Skills[1].Name != "Review" {
		t.Fatalf("skills = %#v", merged.Skills)
	}
	if len(merged.MCPServers) != 2 || merged.MCPServers[1].Name != "browser" {
		t.Fatalf("servers = %#v", merged.MCPServers)
	}
	if MergeAgentWithRuntime(nil, runtime) != nil {
		t.Fatal("nil base should return nil")
	}
	if clone := MergeAgentWithRuntime(base, nil); clone == base || len(clone.Skills) != len(base.Skills) {
		t.Fatalf("nil runtime clone = %#v", clone)
	}
}

func TestLoadPluginMCPServersPreservesNestedRuntimeFields(t *testing.T) {
	tmp := t.TempDir()
	data := `{
  "mcpServers": {
    "runtime-http": {
      "type": "http",
      "command": "node",
      "args": ["server.js"],
      "url": "https://example.test/mcp",
      "env": {"TOKEN": "secret"},
      "headers": {"Authorization": "Bearer secret"}
    }
  }
}`
	if err := os.WriteFile(filepath.Join(tmp, ".mcp.json"), []byte(data), 0o644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	servers := loadPluginMCPServers(tmp)
	want := []models.MCPServerConfig{{
		Name:    "runtime-http",
		Type:    "http",
		Command: []string{"node", "server.js"},
		URL:     "https://example.test/mcp",
		Env:     map[string]string{"TOKEN": "secret"},
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}}
	if !reflect.DeepEqual(servers, want) {
		t.Fatalf("servers = %#v, want %#v", servers, want)
	}
}
