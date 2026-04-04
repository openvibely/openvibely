package agentplugins

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	mcpclient "github.com/openvibely/openvibely/pkg/mcp_client"
)

const (
	defaultAppPluginRoot = ".openvibely/plugins"
	pluginRootEnvVar     = "OPENVIBELY_PLUGIN_ROOT"
	defaultPluginScope   = "user"
)

var (
	envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

	runCommandCombinedFn  = runCommandCombined
	importMarketplaceFn   = importMarketplaceLocally
	userPluginBaseFn      = userPluginBase
	ensurePersistentMCPFn = mcpclient.EnsurePersistentServers
	reconcilePersistentFn = mcpclient.ReconcilePersistentServers
	persistentMCPStateFn  = mcpclient.PersistentRuntimeState
)

const (
	defaultOfficialMarketplaceName   = "claude-plugins-official"
	defaultOfficialMarketplaceSource = "anthropics/claude-plugins-official"
	defaultSkillsMarketplaceName     = "anthropic-agent-skills"
	defaultSkillsMarketplaceSource   = "https://github.com/anthropics/skills.git"
)

// RuntimeBundle is the plugin-derived runtime payload merged into an agent.
type RuntimeBundle struct {
	PluginIDs    []string
	PluginDirs   []string
	Skills       []models.SkillConfig
	MCPServers   []models.MCPServerConfig
	MCPToolNames []string
}

type pluginID struct {
	Name        string
	Marketplace string
}

type marketplaceManifest struct {
	Name     string `json:"name"`
	Metadata struct {
		Description string `json:"description"`
	} `json:"metadata"`
	Plugins []struct {
		Name        string      `json:"name"`
		Description string      `json:"description"`
		Source      interface{} `json:"source"`
	} `json:"plugins"`
}

// NormalizePluginIDs deduplicates and trims plugin IDs.
func NormalizePluginIDs(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		parsed, err := parsePluginID(id)
		if err != nil {
			continue
		}
		canonical := parsed.Name + "@" + parsed.Marketplace
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
	}
	sort.Strings(out)
	return out
}

func parsePluginID(raw string) (pluginID, error) {
	parts := strings.Split(raw, "@")
	if len(parts) < 2 {
		return pluginID{}, fmt.Errorf("invalid plugin id: %q", raw)
	}
	name := strings.TrimSpace(parts[0])
	marketplace := strings.TrimSpace(strings.Join(parts[1:], "@"))
	if name == "" || marketplace == "" {
		return pluginID{}, fmt.Errorf("invalid plugin id: %q", raw)
	}
	return pluginID{Name: name, Marketplace: marketplace}, nil
}

func normalizedMarketplaceSource(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// GitHub scp-like URL: git@github.com:owner/repo(.git)
	if strings.HasPrefix(raw, "git@github.com:") {
		repo := strings.TrimPrefix(raw, "git@github.com:")
		repo = strings.TrimSuffix(repo, ".git")
		repo = strings.Trim(repo, "/")
		if strings.Count(repo, "/") >= 1 {
			return repo
		}
	}

	candidate := raw
	if !strings.Contains(raw, "://") && strings.HasPrefix(raw, "github.com/") {
		candidate = "https://" + raw
	}
	u, err := url.Parse(candidate)
	if err == nil && strings.EqualFold(u.Host, "github.com") {
		path := strings.Trim(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")
		parts := strings.Split(path, "/")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[1]
		}
	}

	return raw
}

func userPluginBase() string {
	base := strings.TrimSpace(os.Getenv(pluginRootEnvVar))
	if base == "" {
		if fallback := defaultPluginBaseFromRuntime(); fallback != "" {
			return filepath.Clean(filepath.Join(fallback, defaultAppPluginRoot))
		}
		return ""
	}
	if !filepath.IsAbs(base) {
		cwd, err := os.Getwd()
		if err != nil || cwd == "" {
			if fallback := defaultPluginBaseFromRuntime(); fallback != "" {
				return filepath.Clean(filepath.Join(fallback, base))
			}
			return ""
		}
		base = filepath.Join(cwd, base)
	}
	return filepath.Clean(base)
}

func defaultPluginBaseFromRuntime() string {
	cwd, err := os.Getwd()
	if err == nil {
		cwd = filepath.Clean(strings.TrimSpace(cwd))
		if cwd != "" && cwd != "." && cwd != string(filepath.Separator) {
			return cwd
		}
	}
	exe, err := os.Executable()
	if err != nil || strings.TrimSpace(exe) == "" {
		return ""
	}
	base := filepath.Dir(filepath.Clean(exe))
	// If binary lives under <app>/bin/openvibely, prefer <app> as app root.
	if filepath.Base(base) == "bin" {
		parent := filepath.Dir(base)
		if parent != "" && parent != string(filepath.Separator) {
			base = parent
		}
	}
	return base
}

func runCommandCombined(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

// DiscoverState returns marketplaces + installed + available plugins from local app-managed state.
func DiscoverState(ctx context.Context) (models.PluginState, error) {
	state := discoverStateLocal()
	localMarketplaces := listLocalMarketplaceDirsFromRoot(userPluginBaseFn())
	var seedErr error
	if len(state.Available) == 0 && len(localMarketplaces) == 0 {
		seedErr = seedDefaultMarketplaces(ctx)
		state = discoverStateLocal()
	}
	if seedErr != nil && len(state.Marketplaces) == 0 && len(state.Available) == 0 && len(state.Installed) == 0 {
		return state, seedErr
	}
	return state, nil
}

func discoverStateLocal() models.PluginState {
	var state models.PluginState
	augmentStateFromLocalMarketplaces(&state)
	augmentInstalledFromLocalCache(&state)
	return state
}

func seedDefaultMarketplaces(ctx context.Context) error {
	defaults := []struct {
		Name   string
		Source string
	}{
		{Name: defaultOfficialMarketplaceName, Source: defaultOfficialMarketplaceSource},
		{Name: defaultSkillsMarketplaceName, Source: defaultSkillsMarketplaceSource},
	}
	var errs []string
	for _, d := range defaults {
		if err := importMarketplaceFn(ctx, d.Source); err != nil {
			errs = append(errs, fmt.Sprintf("%s import: %v", d.Name, err))
			continue
		}
		if err := updateMarketplaceLocally(ctx, d.Name); err != nil {
			errs = append(errs, fmt.Sprintf("%s update: %v", d.Name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("default marketplace seed failed: %s", strings.Join(errs, " | "))
	}
	return nil
}

// AddMarketplace imports a marketplace into app-local plugin storage.
func AddMarketplace(ctx context.Context, source, scope string) error {
	source = strings.TrimSpace(source)
	scope = strings.TrimSpace(scope)
	if source == "" {
		return fmt.Errorf("source is required")
	}
	if scope == "" {
		scope = defaultPluginScope
	}

	normalized := normalizedMarketplaceSource(source)
	candidates := []string{source}
	if normalized != source {
		candidates = append(candidates, normalized)
	}

	var errs []string
	for _, candidate := range candidates {
		if err := importMarketplaceFn(ctx, candidate); err == nil {
			return nil
		} else {
			errs = append(errs, err.Error())
		}
	}

	return fmt.Errorf("marketplace add failed: %s", strings.Join(errs, " | "))
}

// UpdateMarketplace refreshes a locally imported marketplace (git pull when available).
func UpdateMarketplace(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if err := updateMarketplaceLocally(ctx, name); err != nil {
		return fmt.Errorf("marketplace update failed: %s", err.Error())
	}
	return nil
}

// RemoveMarketplace removes a local marketplace.
func RemoveMarketplace(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if err := removeMarketplaceLocally(name); err != nil {
		return fmt.Errorf("marketplace remove failed: %s", err.Error())
	}
	return nil
}

// InstallPlugin materializes plugin sources into local cache.
func InstallPlugin(ctx context.Context, pluginID, scope string) error {
	pluginID = strings.TrimSpace(pluginID)
	scope = strings.TrimSpace(scope)
	if pluginID == "" {
		return fmt.Errorf("plugin_id is required")
	}
	if scope == "" {
		scope = defaultPluginScope
	}
	_ = scope // scope retained for API compatibility
	parsed, err := parsePluginID(pluginID)
	if err != nil {
		return err
	}
	if err := installPluginLocally(ctx, parsed); err != nil {
		return fmt.Errorf("plugin install failed: %s", err.Error())
	}
	return nil
}

// DisablePlugin is deprecated because plugin enablement is managed per-agent.
func DisablePlugin(ctx context.Context, pluginID string) error {
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("plugin_id is required")
	}
	return fmt.Errorf("global plugin disable is not supported; disable plugins per agent")
}

func installedPluginIDs(installed []models.InstalledPlugin) []string {
	ids := make([]string, 0, len(installed))
	for _, p := range installed {
		id := strings.TrimSpace(p.ID)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return NormalizePluginIDs(ids)
}

// UninstallPlugin removes an installed plugin and local plugin cache.
func UninstallPlugin(ctx context.Context, pluginID string) error {
	_ = ctx
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return fmt.Errorf("plugin_id is required")
	}
	if err := removeLocalPluginInstall(pluginID); err != nil {
		return fmt.Errorf("plugin uninstall cleanup failed: %s", err.Error())
	}
	return nil
}

// ReconcileInstalledPluginMCPRunning reconciles persistent plugin MCP servers to all installed plugins.
func ReconcileInstalledPluginMCPRunning(ctx context.Context, workDir string) error {
	state, err := DiscoverState(ctx)
	if err != nil && len(state.Installed) == 0 {
		return err
	}
	servers, resolveErr := pluginServersForIDs(ctx, installedPluginIDs(state.Installed))
	if resolveErr != nil {
		return resolveErr
	}
	return reconcilePersistentFn(ctx, servers, workDir)
}

// ResetDefaultMarketplaces ensures the default official and skills marketplaces exist
// and refreshes them. It also removes stale temp marketplace entries.
func ResetDefaultMarketplaces(ctx context.Context) error {
	state, err := DiscoverState(ctx)
	if err != nil {
		return err
	}

	existing := map[string]models.PluginMarketplace{}
	for _, mp := range state.Marketplaces {
		existing[strings.ToLower(strings.TrimSpace(mp.Name))] = mp
	}

	// Cleanup stale temporary marketplace directories sometimes left by failed adds.
	for _, mp := range state.Marketplaces {
		name := strings.TrimSpace(mp.Name)
		if strings.HasPrefix(strings.ToLower(name), "temp_") {
			_ = RemoveMarketplace(ctx, name)
		}
	}

	defaults := []struct {
		Name   string
		Source string
	}{
		{Name: defaultOfficialMarketplaceName, Source: defaultOfficialMarketplaceSource},
		{Name: defaultSkillsMarketplaceName, Source: defaultSkillsMarketplaceSource},
	}

	var errs []string
	for _, d := range defaults {
		_, exists := existing[strings.ToLower(d.Name)]
		if !exists {
			if err := AddMarketplace(ctx, d.Source, defaultPluginScope); err != nil {
				errs = append(errs, err.Error())
				continue
			}
		}
		if err := UpdateMarketplace(ctx, d.Name); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("reset default marketplaces failed: %s", strings.Join(errs, " | "))
	}
	return nil
}

// ResolveRuntimeBundle resolves selected plugin IDs into local plugin resources.
func ResolveRuntimeBundle(ctx context.Context, pluginIDs []string) (*RuntimeBundle, error) {
	activePluginIDs := NormalizePluginIDs(pluginIDs)
	bundle := &RuntimeBundle{PluginIDs: activePluginIDs}
	if len(activePluginIDs) == 0 {
		return bundle, nil
	}

	type pluginAccum struct {
		dir     string
		skills  []models.SkillConfig
		servers []models.MCPServerConfig
	}
	accum := make([]pluginAccum, 0, len(activePluginIDs))
	var errs []string

	for _, id := range activePluginIDs {
		parsed, err := parsePluginID(id)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		dir, err := resolvePluginDir(parsed)
		if err != nil {
			// If plugin is not locally materialized yet, install it from marketplace metadata and retry.
			if installErr := installPluginLocally(ctx, parsed); installErr == nil {
				dir, err = resolvePluginDir(parsed)
			}
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", id, err))
				continue
			}
		}
		skills := loadPluginSkills(dir)
		servers := loadPluginMCPServers(dir)
		accum = append(accum, pluginAccum{
			dir:     dir,
			skills:  skills,
			servers: servers,
		})
	}

	dirSeen := map[string]struct{}{}
	skillSeen := map[string]struct{}{}
	serverSeen := map[string]struct{}{}
	for _, p := range accum {
		if _, ok := dirSeen[p.dir]; !ok && p.dir != "" {
			dirSeen[p.dir] = struct{}{}
			bundle.PluginDirs = append(bundle.PluginDirs, p.dir)
		}
		for _, skill := range p.skills {
			key := strings.ToLower(strings.TrimSpace(skill.Name))
			if key == "" {
				continue
			}
			if _, ok := skillSeen[key]; ok {
				continue
			}
			skillSeen[key] = struct{}{}
			bundle.Skills = append(bundle.Skills, skill)
		}
		for _, srv := range p.servers {
			key := strings.ToLower(strings.TrimSpace(srv.Name))
			if key == "" {
				continue
			}
			if _, ok := serverSeen[key]; ok {
				continue
			}
			serverSeen[key] = struct{}{}
			bundle.MCPServers = append(bundle.MCPServers, srv)
		}
	}

	sort.Strings(bundle.PluginDirs)
	sort.Slice(bundle.Skills, func(i, j int) bool {
		return strings.ToLower(bundle.Skills[i].Name) < strings.ToLower(bundle.Skills[j].Name)
	})
	sort.Slice(bundle.MCPServers, func(i, j int) bool {
		return strings.ToLower(bundle.MCPServers[i].Name) < strings.ToLower(bundle.MCPServers[j].Name)
	})

	if len(bundle.MCPServers) > 0 {
		toolNames := IntrospectMCPToolNames(ctx, bundle.MCPServers)
		bundle.MCPToolNames = toolNames
	}

	if len(accum) == 0 && len(errs) > 0 {
		return bundle, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return bundle, nil
}

func pluginServersForIDs(ctx context.Context, pluginIDs []string) ([]models.MCPServerConfig, error) {
	activePluginIDs := NormalizePluginIDs(pluginIDs)
	if len(activePluginIDs) == 0 {
		return nil, nil
	}

	serversByName := map[string]models.MCPServerConfig{}
	for _, id := range activePluginIDs {
		parsed, err := parsePluginID(id)
		if err != nil {
			return nil, err
		}
		dir, err := resolvePluginDir(parsed)
		if err != nil {
			if installErr := installPluginLocally(ctx, parsed); installErr == nil {
				dir, err = resolvePluginDir(parsed)
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", id, err)
			}
		}
		for _, srv := range loadPluginMCPServers(dir) {
			key := strings.ToLower(strings.TrimSpace(srv.Name))
			if key == "" {
				continue
			}
			if _, exists := serversByName[key]; exists {
				continue
			}
			serversByName[key] = srv
		}
	}

	if len(serversByName) == 0 {
		return nil, nil
	}
	servers := make([]models.MCPServerConfig, 0, len(serversByName))
	for _, srv := range serversByName {
		servers = append(servers, srv)
	}
	sort.Slice(servers, func(i, j int) bool {
		return strings.ToLower(servers[i].Name) < strings.ToLower(servers[j].Name)
	})
	return servers, nil
}

// EnsurePluginMCPRunning starts MCP servers for selected plugins in persistent mode.
// It records failures in runtime state so the UI can surface startup issues.
func EnsurePluginMCPRunning(ctx context.Context, pluginIDs []string, workDir string) error {
	if strings.TrimSpace(workDir) == "" {
		workDir = "."
	}
	servers, err := pluginServersForIDs(ctx, pluginIDs)
	if err != nil {
		return err
	}
	if len(servers) == 0 {
		return nil
	}
	return ensurePersistentMCPFn(ctx, servers, workDir)
}

// EnsureInstalledPluginMCPRunning starts MCP servers for all currently installed plugins.
func EnsureInstalledPluginMCPRunning(ctx context.Context, workDir string) error {
	return ReconcileInstalledPluginMCPRunning(ctx, workDir)
}

// PluginMCPRuntimeState returns UI-ready runtime status for persistent plugin MCP servers.
func PluginMCPRuntimeState() []models.PluginRuntimeMCP {
	return persistentMCPStateFn()
}

// PluginServerNameMapping returns a map of lowercase MCP server name → plugin ID
// for all installed plugins. This allows the UI to match runtime status entries
// (keyed by MCP server name) back to plugin IDs.
func PluginServerNameMapping(ctx context.Context, installed []models.InstalledPlugin) map[string]string {
	mapping := make(map[string]string)
	for _, p := range installed {
		parsed, err := parsePluginID(p.ID)
		if err != nil {
			continue
		}
		dir, err := resolvePluginDir(parsed)
		if err != nil {
			continue
		}
		for _, srv := range loadPluginMCPServers(dir) {
			key := strings.ToLower(strings.TrimSpace(srv.Name))
			if key != "" {
				mapping[key] = p.ID
			}
		}
	}
	return mapping
}

func resolvePluginDir(id pluginID) (string, error) {
	root := userPluginBaseFn()
	if root == "" {
		return "", fmt.Errorf("could not determine plugin root")
	}

	cacheRoot := filepath.Join(root, "cache", id.Marketplace, id.Name)
	if cacheDir := newestVersionedDir(cacheRoot); cacheDir != "" {
		return cacheDir, nil
	}

	marketRoot := filepath.Join(root, "marketplaces", id.Marketplace)
	manifestPath := filepath.Join(marketRoot, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", fmt.Errorf("plugin not installed and marketplace manifest unreadable: %w", err)
	}

	var manifest marketplaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("invalid marketplace manifest: %w", err)
	}

	for _, p := range manifest.Plugins {
		if p.Name != id.Name {
			continue
		}
		if source, ok := p.Source.(string); ok && source != "" {
			if candidate := localMarketplacePathForSource(marketRoot, source); candidate != "" && dirExists(candidate) {
				return candidate, nil
			}
		}
		if sourceMap, ok := p.Source.(map[string]interface{}); ok {
			if path, ok := sourceMap["path"].(string); ok && path != "" {
				if candidate := localMarketplacePathForSource(marketRoot, path); candidate != "" && dirExists(candidate) {
					return candidate, nil
				}
			}
		}
	}

	for _, fallback := range []string{
		filepath.Join(marketRoot, "plugins", id.Name),
		filepath.Join(marketRoot, "external_plugins", id.Name),
	} {
		if dirExists(fallback) {
			return fallback, nil
		}
	}

	return "", fmt.Errorf("plugin directory not found")
}

func newestVersionedDir(root string) string {
	if !dirExists(root) {
		return ""
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	type candidate struct {
		path string
		mod  time.Time
	}
	cands := make([]candidate, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(root, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		cands = append(cands, candidate{path: path, mod: info.ModTime()})
	}
	if len(cands) == 0 {
		if dirExists(root) {
			return root
		}
		return ""
	}
	sort.Slice(cands, func(i, j int) bool {
		if cands[i].mod.Equal(cands[j].mod) {
			return cands[i].path > cands[j].path
		}
		return cands[i].mod.After(cands[j].mod)
	})
	return cands[0].path
}

func loadPluginSkills(pluginDir string) []models.SkillConfig {
	root := filepath.Join(pluginDir, "skills")
	if !dirExists(root) {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	out := make([]models.SkillConfig, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillFile := filepath.Join(root, e.Name(), "SKILL.md")
		data, err := os.ReadFile(skillFile)
		if err != nil {
			continue
		}
		fm, body := parseFrontmatterAndBody(string(data))
		name := strings.TrimSpace(fm["name"])
		if name == "" {
			name = strings.TrimSpace(e.Name())
		}
		content := strings.TrimSpace(body)
		if name == "" || content == "" {
			continue
		}
		out = append(out, models.SkillConfig{
			Name:        name,
			Description: strings.TrimSpace(fm["description"]),
			Tools:       strings.TrimSpace(fm["tools"]),
			Content:     content,
		})
	}
	return out
}

func loadPluginMCPServers(pluginDir string) []models.MCPServerConfig {
	path := filepath.Join(pluginDir, ".mcp.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}

	serverRaw := root
	if mcpServersRaw, ok := root["mcpServers"]; ok {
		var nested map[string]json.RawMessage
		if err := json.Unmarshal(mcpServersRaw, &nested); err == nil {
			serverRaw = nested
		}
	}

	out := make([]models.MCPServerConfig, 0, len(serverRaw))
	for name, raw := range serverRaw {
		var cfg map[string]interface{}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			continue
		}
		server := models.MCPServerConfig{
			Name:    strings.TrimSpace(name),
			Type:    strings.TrimSpace(asString(cfg["type"])),
			URL:     strings.TrimSpace(asString(cfg["url"])),
			Env:     stringMap(cfg["env"]),
			Headers: stringMap(cfg["headers"]),
		}
		server.Env = interpolateMapEnv(server.Env)
		server.Headers = interpolateMapEnv(server.Headers)

		command := commandParts(cfg["command"])
		command = append(command, commandParts(cfg["args"])...)
		server.Command = command
		if server.Type == "" {
			if server.URL != "" {
				server.Type = "http"
			} else {
				server.Type = "stdio"
			}
		}
		if server.Name == "" {
			continue
		}
		if len(server.Command) == 0 && server.URL == "" {
			continue
		}
		out = append(out, server)
	}
	return out
}

func augmentStateFromLocalMarketplaces(state *models.PluginState) {
	augmentStateFromLocalMarketplacesAtRoot(state, userPluginBaseFn())
}

func augmentStateFromLocalMarketplacesAtRoot(state *models.PluginState, root string) {
	if state == nil {
		return
	}
	dirs := listLocalMarketplaceDirsFromRoot(root)
	if len(dirs) == 0 {
		return
	}

	marketSeen := map[string]struct{}{}
	for _, mp := range state.Marketplaces {
		key := strings.ToLower(strings.TrimSpace(mp.Name))
		if key != "" {
			marketSeen[key] = struct{}{}
		}
	}
	availSeen := map[string]struct{}{}
	for _, ap := range state.Available {
		key := strings.ToLower(strings.TrimSpace(ap.PluginID))
		if key != "" {
			availSeen[key] = struct{}{}
		}
	}

	for _, dir := range dirs {
		manifest, err := loadMarketplaceManifestFromDir(dir)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(manifest.Name)
		if name == "" {
			name = filepath.Base(dir)
		}
		nameKey := strings.ToLower(name)
		if _, ok := marketSeen[nameKey]; !ok && name != "" {
			source := discoverGitRemote(dir)
			if source == "" {
				source = dir
			}
			state.Marketplaces = append(state.Marketplaces, models.PluginMarketplace{
				Name:   name,
				Source: source,
			})
			marketSeen[nameKey] = struct{}{}
		}

		for _, plugin := range manifest.Plugins {
			pluginName := strings.TrimSpace(plugin.Name)
			if pluginName == "" || name == "" {
				continue
			}
			pluginID := pluginName + "@" + name
			idKey := strings.ToLower(pluginID)
			if _, ok := availSeen[idKey]; ok {
				continue
			}
			state.Available = append(state.Available, models.AvailablePlugin{
				PluginID:        pluginID,
				Name:            pluginName,
				Description:     strings.TrimSpace(plugin.Description),
				MarketplaceName: name,
				Source:          marketplaceSourceSummary(plugin.Source),
			})
			availSeen[idKey] = struct{}{}
		}
	}

	sort.Slice(state.Marketplaces, func(i, j int) bool {
		return strings.ToLower(state.Marketplaces[i].Name) < strings.ToLower(state.Marketplaces[j].Name)
	})
	sort.Slice(state.Available, func(i, j int) bool {
		return strings.ToLower(state.Available[i].PluginID) < strings.ToLower(state.Available[j].PluginID)
	})
}

func augmentInstalledFromLocalCache(state *models.PluginState) {
	augmentInstalledFromLocalCacheAtRoot(state, userPluginBaseFn())
}

func augmentInstalledFromLocalCacheAtRoot(state *models.PluginState, root string) {
	if state == nil {
		return
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return
	}
	cacheRoot := filepath.Join(root, "cache")
	if !dirExists(cacheRoot) {
		return
	}
	installedIndex := map[string]int{}
	for i := range state.Installed {
		id := strings.ToLower(strings.TrimSpace(state.Installed[i].ID))
		if id == "" {
			continue
		}
		installedIndex[id] = i
		state.Installed[i].Enabled = true
	}

	marketDirs, err := os.ReadDir(cacheRoot)
	if err != nil {
		return
	}
	for _, marketDir := range marketDirs {
		if !marketDir.IsDir() {
			continue
		}
		marketName := strings.TrimSpace(marketDir.Name())
		if marketName == "" {
			continue
		}
		pluginsPath := filepath.Join(cacheRoot, marketDir.Name())
		pluginDirs, err := os.ReadDir(pluginsPath)
		if err != nil {
			continue
		}
		for _, pluginDir := range pluginDirs {
			if !pluginDir.IsDir() {
				continue
			}
			pluginName := strings.TrimSpace(pluginDir.Name())
			if pluginName == "" {
				continue
			}
			id := pluginName + "@" + marketName
			idKey := strings.ToLower(id)
			if _, exists := installedIndex[idKey]; exists {
				continue
			}
			installPath := newestVersionedDir(filepath.Join(pluginsPath, pluginName))
			if installPath == "" {
				continue
			}
			state.Installed = append(state.Installed, models.InstalledPlugin{
				ID:          id,
				Enabled:     true,
				InstallPath: installPath,
			})
			installedIndex[idKey] = len(state.Installed) - 1
		}
	}

	sort.Slice(state.Installed, func(i, j int) bool {
		return strings.ToLower(state.Installed[i].ID) < strings.ToLower(state.Installed[j].ID)
	})
}

func removeLocalPluginInstall(pluginID string) error {
	parsed, err := parsePluginID(pluginID)
	if err != nil {
		return err
	}
	canonicalID := parsed.Name + "@" + parsed.Marketplace

	root := userPluginBaseFn()
	if root == "" {
		return fmt.Errorf("could not determine plugin root")
	}

	removePaths := []string{filepath.Join(root, "cache", parsed.Marketplace, parsed.Name)}
	state, discoverErr := DiscoverState(context.Background())
	if discoverErr == nil {
		found := false
		for _, installed := range state.Installed {
			if !strings.EqualFold(strings.TrimSpace(installed.ID), canonicalID) {
				continue
			}
			found = true
			installPath := strings.TrimSpace(installed.InstallPath)
			if installPath != "" {
				removePaths = append(removePaths, installPath)
			}
		}
		if !found {
			return fmt.Errorf("plugin %q is not installed", pluginID)
		}
	}

	seen := map[string]struct{}{}
	existingPathFound := false
	for _, removePath := range removePaths {
		removePath = strings.TrimSpace(removePath)
		if removePath == "" {
			continue
		}
		if _, ok := seen[removePath]; ok {
			continue
		}
		seen[removePath] = struct{}{}
		if !dirExists(removePath) {
			continue
		}
		existingPathFound = true
		if err := os.RemoveAll(removePath); err != nil {
			return err
		}
	}
	if !existingPathFound {
		return fmt.Errorf("plugin %q is not installed", pluginID)
	}

	return nil
}

func importMarketplaceLocally(ctx context.Context, source string) error {
	source = strings.TrimSpace(source)
	if source == "" {
		return fmt.Errorf("source is required")
	}

	root := userPluginBaseFn()
	if root == "" {
		return fmt.Errorf("could not determine plugin root")
	}
	marketRoot := filepath.Join(root, "marketplaces")
	if err := os.MkdirAll(marketRoot, 0o755); err != nil {
		return err
	}

	tempDir, err := os.MkdirTemp(marketRoot, "temp_")
	if err != nil {
		return err
	}
	cleanupTemp := true
	defer func() {
		if cleanupTemp {
			_ = os.RemoveAll(tempDir)
		}
	}()

	if localPath, ok := resolveLocalPath(source); ok {
		if err := copyDir(localPath, tempDir); err != nil {
			return err
		}
	} else {
		if err := cloneFromSourceCandidates(ctx, source, tempDir); err != nil {
			return err
		}
	}

	manifest, err := loadMarketplaceManifestFromDir(tempDir)
	if err != nil {
		return fmt.Errorf("marketplace import failed: %w", err)
	}
	name := strings.TrimSpace(manifest.Name)
	if name == "" {
		name = sanitizedMarketplaceName(source)
	}
	if name == "" {
		return fmt.Errorf("marketplace import failed: invalid marketplace name")
	}

	targetDir := filepath.Join(marketRoot, name)
	_ = os.RemoveAll(targetDir)
	if err := os.Rename(tempDir, targetDir); err != nil {
		if err := copyDir(tempDir, targetDir); err != nil {
			return err
		}
	}
	cleanupTemp = false
	return nil
}

func updateMarketplaceLocally(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	dir, _, err := findMarketplaceDirByName(name)
	if err != nil {
		return err
	}
	if !dirExists(filepath.Join(dir, ".git")) {
		return nil
	}
	return runCommandCombinedFn(ctx, "git", "-C", dir, "pull", "--ff-only")
}

func removeMarketplaceLocally(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("name is required")
	}
	dir, _, err := findMarketplaceDirByName(name)
	if err != nil {
		root := userPluginBaseFn()
		if root == "" {
			return err
		}
		fallback := filepath.Join(root, "marketplaces", name)
		if dirExists(fallback) {
			return os.RemoveAll(fallback)
		}
		return err
	}
	return os.RemoveAll(dir)
}

func installPluginLocally(ctx context.Context, id pluginID) error {
	marketDir, manifest, err := findMarketplaceDirByName(id.Marketplace)
	if err != nil {
		return err
	}
	pluginIndex := -1
	for i := range manifest.Plugins {
		if strings.EqualFold(strings.TrimSpace(manifest.Plugins[i].Name), id.Name) {
			pluginIndex = i
			break
		}
	}
	if pluginIndex < 0 {
		return fmt.Errorf("plugin %q not found in marketplace %q", id.Name, id.Marketplace)
	}
	plugin := manifest.Plugins[pluginIndex]

	root := userPluginBaseFn()
	if root == "" {
		return fmt.Errorf("could not determine plugin root")
	}
	installRoot := filepath.Join(root, "cache", id.Marketplace, id.Name)
	if err := os.MkdirAll(installRoot, 0o755); err != nil {
		return err
	}
	version := time.Now().UTC().Format("20060102T150405")
	tempInstall := filepath.Join(installRoot, ".tmp-"+version)
	finalInstall := filepath.Join(installRoot, version)
	_ = os.RemoveAll(tempInstall)
	if err := os.MkdirAll(tempInstall, 0o755); err != nil {
		return err
	}

	if err := materializePluginSource(ctx, marketDir, plugin.Source, tempInstall); err != nil {
		_ = os.RemoveAll(tempInstall)
		return err
	}

	_ = os.RemoveAll(finalInstall)
	if err := os.Rename(tempInstall, finalInstall); err != nil {
		if err := copyDir(tempInstall, finalInstall); err != nil {
			_ = os.RemoveAll(tempInstall)
			return err
		}
		_ = os.RemoveAll(tempInstall)
	}
	return nil
}

func materializePluginSource(ctx context.Context, marketplaceDir string, source interface{}, targetDir string) error {
	if source == nil {
		return fmt.Errorf("plugin source is missing")
	}
	if src, ok := source.(string); ok {
		src = strings.TrimSpace(src)
		if src == "" {
			return fmt.Errorf("plugin source is empty")
		}
		if local := localMarketplacePathForSource(marketplaceDir, src); local != "" && dirExists(local) {
			return copyDir(local, targetDir)
		}
		return clonePluginFromRemote(ctx, src, "", "", "", targetDir)
	}
	srcMap, ok := source.(map[string]interface{})
	if !ok {
		return fmt.Errorf("unsupported plugin source type %T", source)
	}
	sourceType := strings.ToLower(strings.TrimSpace(asString(srcMap["source"])))
	sourceURL := strings.TrimSpace(asString(srcMap["url"]))
	sourceRepo := strings.TrimSpace(asString(srcMap["repo"]))
	sourcePath := strings.TrimSpace(asString(srcMap["path"]))
	sourceRef := strings.TrimSpace(asString(srcMap["ref"]))
	sourceSHA := strings.TrimSpace(asString(srcMap["sha"]))

	if sourceURL == "" && sourceRepo != "" {
		repo := strings.Trim(strings.TrimSpace(sourceRepo), "/")
		if strings.Count(repo, "/") >= 1 {
			sourceURL = "https://github.com/" + strings.TrimSuffix(repo, ".git") + ".git"
		}
	}

	if local := localMarketplacePathForSource(marketplaceDir, sourcePath); local != "" && dirExists(local) {
		return copyDir(local, targetDir)
	}

	switch sourceType {
	case "", "url", "git-subdir", "github":
		return clonePluginFromRemote(ctx, sourceURL, sourcePath, sourceRef, sourceSHA, targetDir)
	default:
		return fmt.Errorf("unsupported plugin source mode: %s", sourceType)
	}
}

func clonePluginFromRemote(ctx context.Context, sourceURL, sourcePath, sourceRef, sourceSHA, targetDir string) error {
	sourceURL = strings.TrimSpace(sourceURL)
	if sourceURL == "" {
		return fmt.Errorf("remote plugin source URL is required")
	}
	tmpRepo, err := os.MkdirTemp("", "openvibely-plugin-src-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpRepo)

	candidates := cloneSourceCandidates(sourceURL)
	if len(candidates) == 0 {
		candidates = []string{sourceURL}
	}

	var cloneErrs []string
	cloned := false
	for _, candidate := range candidates {
		_ = os.RemoveAll(tmpRepo)
		if err := os.MkdirAll(filepath.Dir(tmpRepo), 0o755); err != nil {
			return err
		}
		args := []string{"clone", "--depth", "1"}
		if sourceRef != "" && !looksLikeCommitSHA(sourceRef) {
			args = append(args, "--branch", sourceRef)
		}
		args = append(args, candidate, tmpRepo)
		if err := runCommandCombinedFn(ctx, "git", args...); err != nil {
			cloneErrs = append(cloneErrs, err.Error())
			continue
		}
		cloned = true
		break
	}
	if !cloned {
		return fmt.Errorf("failed to clone plugin source: %s", strings.Join(cloneErrs, " | "))
	}

	if sourceSHA != "" {
		if err := runCommandCombinedFn(ctx, "git", "-C", tmpRepo, "checkout", sourceSHA); err != nil {
			return err
		}
	} else if sourceRef != "" && looksLikeCommitSHA(sourceRef) {
		if err := runCommandCombinedFn(ctx, "git", "-C", tmpRepo, "checkout", sourceRef); err != nil {
			return err
		}
	}

	srcDir := tmpRepo
	if sourcePath != "" {
		joined, err := safeJoinUnderRoot(tmpRepo, sourcePath)
		if err != nil {
			return err
		}
		srcDir = joined
	}
	if !dirExists(srcDir) {
		return fmt.Errorf("plugin source path not found: %s", srcDir)
	}
	return copyDir(srcDir, targetDir)
}

func safeJoinUnderRoot(root, relPath string) (string, error) {
	root = filepath.Clean(root)
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return root, nil
	}
	clean := filepath.Clean(relPath)
	if clean == "." {
		return root, nil
	}
	joined := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid relative path outside repository: %s", relPath)
	}
	return joined, nil
}

func cloneFromSourceCandidates(ctx context.Context, source, targetDir string) error {
	candidates := cloneSourceCandidates(source)
	if len(candidates) == 0 {
		return fmt.Errorf("unsupported marketplace source: %s", source)
	}
	var errs []string
	for _, candidate := range candidates {
		_ = os.RemoveAll(targetDir)
		args := []string{"clone", "--depth", "1", candidate, targetDir}
		if err := runCommandCombinedFn(ctx, "git", args...); err == nil {
			return nil
		} else {
			errs = append(errs, err.Error())
		}
	}
	return fmt.Errorf("failed to clone marketplace source: %s", strings.Join(errs, " | "))
}

func cloneSourceCandidates(source string) []string {
	source = strings.TrimSpace(source)
	if source == "" {
		return nil
	}
	seen := map[string]struct{}{}
	add := func(v string, out *[]string) {
		v = strings.TrimSpace(v)
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		*out = append(*out, v)
	}
	out := []string{}

	if localPath, ok := resolveLocalPath(source); ok && dirExists(localPath) {
		add(localPath, &out)
		return out
	}

	if strings.HasPrefix(source, "git@") {
		add(source, &out)
		return out
	}
	if strings.Contains(source, "://") {
		add(source, &out)
		if strings.HasPrefix(source, "https://github.com/") {
			repoPath := strings.TrimPrefix(source, "https://github.com/")
			repoPath = strings.Trim(repoPath, "/")
			repoPath = strings.TrimSuffix(repoPath, ".git")
			add("git@github.com:"+repoPath+".git", &out)
		}
		return out
	}
	if strings.HasPrefix(source, "github.com/") {
		repoPath := strings.TrimPrefix(source, "github.com/")
		repoPath = strings.Trim(repoPath, "/")
		repoPath = strings.TrimSuffix(repoPath, ".git")
		add("https://github.com/"+repoPath+".git", &out)
		add("git@github.com:"+repoPath+".git", &out)
		return out
	}
	if strings.Count(source, "/") == 1 && !strings.Contains(source, " ") {
		repoPath := strings.Trim(source, "/")
		repoPath = strings.TrimSuffix(repoPath, ".git")
		add("https://github.com/"+repoPath+".git", &out)
		add("git@github.com:"+repoPath+".git", &out)
		return out
	}

	return out
}

func resolveLocalPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "~") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~"))
		}
	}
	if !filepath.IsAbs(raw) {
		cwd, err := os.Getwd()
		if err == nil {
			raw = filepath.Join(cwd, raw)
		}
	}
	if dirExists(raw) {
		return raw, true
	}
	return "", false
}

func sanitizedMarketplaceName(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.Contains(source, "://") {
		u, err := url.Parse(source)
		if err == nil {
			base := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
			base = strings.ReplaceAll(base, "/", "-")
			if base != "" {
				return base
			}
		}
	}
	source = strings.TrimSuffix(source, ".git")
	source = strings.TrimPrefix(source, "git@github.com:")
	source = strings.TrimPrefix(source, "github.com/")
	source = strings.TrimPrefix(source, "https://github.com/")
	source = strings.TrimPrefix(source, "http://github.com/")
	source = strings.Trim(source, "/")
	source = strings.ReplaceAll(source, "/", "-")
	source = strings.ReplaceAll(source, " ", "-")
	return source
}

func findMarketplaceDirByName(name string) (string, marketplaceManifest, error) {
	var empty marketplaceManifest
	name = strings.TrimSpace(name)
	if name == "" {
		return "", empty, fmt.Errorf("marketplace name is required")
	}
	root := userPluginBaseFn()
	if root == "" {
		return "", empty, fmt.Errorf("could not determine plugin root")
	}
	direct := filepath.Join(root, "marketplaces", name)
	if dirExists(direct) {
		manifest, err := loadMarketplaceManifestFromDir(direct)
		if err == nil {
			return direct, manifest, nil
		}
	}
	dirs := listLocalMarketplaceDirs()
	for _, dir := range dirs {
		manifest, err := loadMarketplaceManifestFromDir(dir)
		if err != nil {
			continue
		}
		manifestName := strings.TrimSpace(manifest.Name)
		if manifestName == "" {
			manifestName = filepath.Base(dir)
		}
		if strings.EqualFold(manifestName, name) {
			return dir, manifest, nil
		}
	}
	return "", empty, fmt.Errorf("marketplace %q not found", name)
}

func loadMarketplaceManifestFromDir(dir string) (marketplaceManifest, error) {
	var manifest marketplaceManifest
	path := filepath.Join(dir, ".claude-plugin", "marketplace.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func listLocalMarketplaceDirs() []string {
	return listLocalMarketplaceDirsFromRoot(userPluginBaseFn())
}

func listLocalMarketplaceDirsFromRoot(root string) []string {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	marketRoot := filepath.Join(root, "marketplaces")
	entries, err := os.ReadDir(marketRoot)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(marketRoot, entry.Name())
		if _, err := loadMarketplaceManifestFromDir(path); err == nil {
			out = append(out, path)
		}
	}
	sort.Strings(out)
	return out
}

func discoverGitRemote(dir string) string {
	configPath := filepath.Join(dir, ".git", "config")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	inOrigin := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inOrigin = strings.EqualFold(trimmed, `[remote "origin"]`)
			continue
		}
		if inOrigin && strings.HasPrefix(strings.ToLower(trimmed), "url = ") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "url = "))
		}
	}
	return ""
}

func localMarketplacePathForSource(marketRoot, source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if strings.Contains(source, "://") || strings.HasPrefix(source, "git@") {
		return ""
	}
	clean := filepath.Clean(source)
	if clean == "." {
		return marketRoot
	}
	if filepath.IsAbs(clean) {
		return clean
	}
	joined := filepath.Join(marketRoot, clean)
	rel, err := filepath.Rel(marketRoot, joined)
	if err != nil {
		return ""
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return joined
}

func marketplaceSourceSummary(source interface{}) string {
	switch s := source.(type) {
	case string:
		return strings.TrimSpace(s)
	case map[string]interface{}:
		if v := strings.TrimSpace(asString(s["url"])); v != "" {
			return v
		}
		if v := strings.TrimSpace(asString(s["repo"])); v != "" {
			return v
		}
		if v := strings.TrimSpace(asString(s["path"])); v != "" {
			return v
		}
		if v := strings.TrimSpace(asString(s["source"])); v != "" {
			return v
		}
	}
	return ""
}

func looksLikeCommitSHA(v string) bool {
	v = strings.TrimSpace(v)
	if len(v) < 7 || len(v) > 64 {
		return false
	}
	for _, r := range v {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func copyDir(src, dst string) error {
	src = filepath.Clean(src)
	dst = filepath.Clean(dst)
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	return filepath.Walk(src, func(path string, fi os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, fi.Mode().Perm())
		}
		return copyFile(path, target, fi.Mode().Perm())
	})
}

func copyFile(src, dst string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func parseFrontmatterAndBody(content string) (map[string]string, string) {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	fm := map[string]string{}
	if !strings.HasPrefix(content, "---\n") {
		return fm, content
	}
	rest := content[len("---\n"):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return fm, content
	}
	rawFM := rest[:idx]
	body := rest[idx+len("\n---\n"):]
	for _, line := range strings.Split(rawFM, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		colon := strings.Index(line, ":")
		if colon <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:colon])
		v := strings.TrimSpace(line[colon+1:])
		v = strings.Trim(v, `"`)
		fm[strings.ToLower(k)] = v
	}
	return fm, body
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func stringMap(v interface{}) map[string]string {
	out := map[string]string{}
	m, ok := v.(map[string]interface{})
	if !ok {
		return out
	}
	for k, vv := range m {
		if s, ok := vv.(string); ok {
			trimmedKey := strings.TrimSpace(k)
			if trimmedKey != "" {
				out[trimmedKey] = strings.TrimSpace(s)
			}
		}
	}
	return out
}

func commandParts(v interface{}) []string {
	var parts []string
	switch vv := v.(type) {
	case string:
		vv = strings.TrimSpace(vv)
		if vv != "" {
			parts = append(parts, vv)
		}
	case []interface{}:
		for _, item := range vv {
			if s, ok := item.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					parts = append(parts, s)
				}
			}
		}
	}
	return parts
}

func interpolateMapEnv(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = interpolateEnvRef(v)
	}
	return out
}

func interpolateEnvRef(input string) string {
	return envRefRe.ReplaceAllStringFunc(input, func(match string) string {
		groups := envRefRe.FindStringSubmatch(match)
		name := groups[1]
		if name == "" {
			name = groups[2]
		}
		return os.Getenv(name)
	})
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// MergeAgentWithRuntime appends plugin skills + MCP servers to an agent.
func MergeAgentWithRuntime(base *models.Agent, runtime *RuntimeBundle) *models.Agent {
	if base == nil {
		return nil
	}
	if runtime == nil {
		c := *base
		return &c
	}
	merged := *base
	merged.Plugins = NormalizePluginIDs(base.Plugins)
	merged.Skills = append([]models.SkillConfig{}, base.Skills...)
	merged.MCPServers = append([]models.MCPServerConfig{}, base.MCPServers...)

	skillSeen := map[string]struct{}{}
	for _, s := range merged.Skills {
		skillSeen[strings.ToLower(strings.TrimSpace(s.Name))] = struct{}{}
	}
	for _, s := range runtime.Skills {
		key := strings.ToLower(strings.TrimSpace(s.Name))
		if key == "" {
			continue
		}
		if _, ok := skillSeen[key]; ok {
			continue
		}
		skillSeen[key] = struct{}{}
		merged.Skills = append(merged.Skills, s)
	}

	serverSeen := map[string]struct{}{}
	for _, s := range merged.MCPServers {
		serverSeen[strings.ToLower(strings.TrimSpace(s.Name))] = struct{}{}
	}
	for _, s := range runtime.MCPServers {
		key := strings.ToLower(strings.TrimSpace(s.Name))
		if key == "" {
			continue
		}
		if _, ok := serverSeen[key]; ok {
			continue
		}
		serverSeen[key] = struct{}{}
		merged.MCPServers = append(merged.MCPServers, s)
	}

	return &merged
}

// IntrospectMCPToolNames tries to enumerate tool names from configured MCP servers.
func IntrospectMCPToolNames(ctx context.Context, servers []models.MCPServerConfig) []string {
	if len(servers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	workDir, _ := os.Getwd()
	manager, err := mcpclient.NewMCPManager(ctx, servers, workDir)
	if err != nil {
		return nil
	}
	defer manager.Close()

	defs := manager.ToolDefinitions()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		if d.Name != "" {
			names = append(names, d.Name)
		}
	}
	sort.Strings(names)
	return names
}
