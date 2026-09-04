// Package agentskills implements the on-disk standalone skill catalog used for
// lifecycle routing and skill_view.
//
// Routable/generated skills live at:
//
//	<root>/skills/<skill>/SKILL.md
//
// for both global (~/.openvibely) and project-scoped (<repo>/.openvibely) roots.
// The top-level narrative index is:
//
//	<root>/skills/SKILLS.md
//
// Agent-owned implementation skills may also live under
// <root>/agents/<agent>/skills/<skill>/SKILL.md. They are not part of the
// standalone catalog, but assigned-agent turns can build a scoped catalog from
// the assigned agent's SKILLS.md index so the router can select relevant
// agent-owned skills for that turn.
package agentskills

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// AgentsIndexFile is the per-root narrative agent index filename.
const AgentsIndexFile = "AGENTS.md"

// SkillsIndexFile is the standalone skill index filename.
const SkillsIndexFile = "SKILLS.md"

// AgentRootsDir is the relative folder beneath a root where agents live.
const AgentRootsDir = "agents"

// SkillsDir is the relative folder beneath a root or agent where skills live.
const SkillsDir = "skills"

// SkillFile is the conventional filename for the skill body.
const SkillFile = "SKILL.md"

// AgentsIndexPath is "<root>/agents/AGENTS.md".
func AgentsIndexPath(root string) string {
	return filepath.Join(root, AgentRootsDir, AgentsIndexFile)
}

// SkillsIndexPath is "<root>/skills/SKILLS.md" for standalone skills.
func SkillsIndexPath(root string) string {
	return filepath.Join(root, SkillsDir, SkillsIndexFile)
}

// AgentSkillsIndexPath is "<root>/agents/<agent>/SKILLS.md" for agent-owned
// implementation skills. It is intentionally not used by BuildCatalog.
func AgentSkillsIndexPath(root, agent string) string {
	return filepath.Join(root, AgentRootsDir, agent, SkillsIndexFile)
}

// EnsureAgentsRoot creates "<root>/agents" for agent declarations.
func EnsureAgentsRoot(root string) error {
	if root == "" {
		return nil
	}
	return os.MkdirAll(filepath.Join(root, AgentRootsDir), 0o755)
}

// EnsureSkillsRoot creates "<root>/skills" for standalone generated skills.
func EnsureSkillsRoot(root string) error {
	if root == "" {
		return nil
	}
	return os.MkdirAll(filepath.Join(root, SkillsDir), 0o755)
}

// Source identifies where a skill was loaded from. Project sources win over
// global sources for matching handles.
type Source string

const (
	SourceGlobal  Source = "global"
	SourceProject Source = "project"
	SourceAgent   Source = "agent"
)

// Entry is one skill handle authorized for the current turn. The full body is
// not loaded until skill_view resolves the entry. Standalone catalogs use
// SourceGlobal/SourceProject. Assigned-agent catalogs use SourceAgent with
// AgentKey set to the owning agent.
type Entry struct {
	Handle       string // skill slug exposed for this turn, e.g. "debug_go_tests"
	Skill        string // skill slug
	Source       Source // global | project | agent
	AgentKey     string // set for agent-owned selected skills
	AbsolutePath string // resolved SKILL.md path (runtime-only, never sent to the model)
}

// Catalog is the app-owned, frozen-per-turn set of handles the model is allowed
// to call skill_view on.
type Catalog struct {
	entries     map[string]Entry
	byHandle    map[string][]Entry
	byQualified map[string]Entry
	ordered     []Entry
	turnID      string
	agentOwned  bool
}

// NewCatalog freezes the supplied entries into a lookup table keyed by handle.
func NewCatalog(turnID string, entries []Entry) *Catalog {
	return newCatalog(turnID, entries, false)
}

func newCatalog(turnID string, entries []Entry, agentOwned bool) *Catalog {
	c := &Catalog{
		entries:     make(map[string]Entry, len(entries)),
		byHandle:    make(map[string][]Entry, len(entries)),
		byQualified: make(map[string]Entry, len(entries)*3),
		ordered:     make([]Entry, 0, len(entries)),
		turnID:      turnID,
		agentOwned:  agentOwned,
	}
	for _, e := range entries {
		if e.Source == SourceAgent || e.AgentKey != "" {
			c.agentOwned = true
		}
		c.entries[e.Handle] = e
		c.byHandle[e.Handle] = append(c.byHandle[e.Handle], e)
		for _, qualified := range qualifiedSkillHandles(e) {
			c.byQualified[qualified] = e
		}
		c.ordered = append(c.ordered, e)
	}
	return c
}

func qualifiedSkillHandles(e Entry) []string {
	skill := strings.TrimSpace(e.Skill)
	if skill == "" {
		skill = strings.TrimSpace(e.Handle)
	}
	if skill == "" {
		return nil
	}
	if e.Source == SourceAgent || strings.TrimSpace(e.AgentKey) != "" {
		agent := strings.TrimSpace(e.AgentKey)
		if agent == "" {
			return nil
		}
		return []string{"agent:" + agent + "/" + skill}
	}
	return []string{"standalone:" + skill, "skill:" + skill}
}

// TurnID returns the identifier of the model turn this catalog was frozen for.
func (c *Catalog) TurnID() string {
	if c == nil {
		return ""
	}
	return c.turnID
}

// Lookup returns the legacy bare-handle entry, or false if it was not in the
// frozen set. For merged multi-scope skill_view calls, use ResolveSkillHandle so
// duplicate bare handles can be reported as ambiguous instead of shadowed.
func (c *Catalog) Lookup(handle string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	e, ok := c.entries[handle]
	return e, ok
}

// ResolveSkillHandle resolves either a qualified skill_view handle or an
// unambiguous bare handle from the frozen turn catalog.
func (c *Catalog) ResolveSkillHandle(handle string) (Entry, bool, bool) {
	if c == nil {
		return Entry{}, false, false
	}
	handle = strings.TrimSpace(handle)
	if e, ok := c.byQualified[handle]; ok {
		return e, true, false
	}
	entries := c.byHandle[handle]
	if len(entries) == 0 {
		return Entry{}, false, false
	}
	if len(entries) > 1 {
		return Entry{}, true, true
	}
	return entries[0], true, false
}

// Entries returns the frozen entries sorted by handle for deterministic output.
func (c *Catalog) Entries() []Entry {
	if c == nil {
		return nil
	}
	out := append([]Entry(nil), c.ordered...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Handle != out[j].Handle {
			return out[i].Handle < out[j].Handle
		}
		if out[i].AgentKey != out[j].AgentKey {
			return out[i].AgentKey < out[j].AgentKey
		}
		return out[i].Source < out[j].Source
	})
	return out
}

// IsAgentOwned reports whether this turn catalog contains assigned-agent-owned
// skills rather than standalone global/project skills.
func (c *Catalog) IsAgentOwned() bool {
	if c == nil {
		return false
	}
	return c.agentOwned
}

// Filter returns a catalog containing only the requested handles that exist in
// this catalog. Missing handles are ignored so callers can validate/log them
// without exposing unauthorized paths to the model.
func (c *Catalog) Filter(turnID string, handles []string) *Catalog {
	if c == nil {
		return nil
	}
	seen := map[string]struct{}{}
	entries := make([]Entry, 0, len(handles))
	for _, handle := range handles {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		entry, ok := c.Lookup(handle)
		if !ok {
			continue
		}
		seen[handle] = struct{}{}
		entries = append(entries, entry)
	}
	if turnID == "" {
		turnID = c.turnID
	}
	return newCatalog(turnID, entries, c.agentOwned)
}

// BuildCatalog enumerates standalone skill handles authorized in this turn by
// reading <root>/skills/SKILLS.md in each root. The SKILL.md files themselves
// must exist on disk for an indexed handle to be loadable. Disabled skills
// (skill.enabled: false in frontmatter) are excluded from the runtime catalog.
//
// Project entries override global entries for the same handle. Missing SKILLS.md
// is treated as "no standalone skills at this scope"; skill mutations maintain
// a minimal top-level SKILLS.md link index.
func BuildCatalog(turnID, globalRoot, projectRoot string) (*Catalog, error) {
	return buildCatalog(turnID, globalRoot, projectRoot, false)
}

// BuildCatalogAll enumerates all indexed standalone skills including disabled
// ones. Use this for management UIs that need to display and act on disabled
// skills; use BuildCatalog for runtime/task-execution catalogs.
func BuildCatalogAll(turnID, globalRoot, projectRoot string) (*Catalog, error) {
	return buildCatalog(turnID, globalRoot, projectRoot, true)
}

func buildCatalog(turnID, globalRoot, projectRoot string, includeDisabled bool) (*Catalog, error) {
	var entries []Entry
	if globalRoot != "" {
		got, err := loadStandaloneSkills(globalRoot, SourceGlobal, includeDisabled)
		if err != nil {
			return nil, fmt.Errorf("read global skill index: %w", err)
		}
		entries = append(entries, got...)
	}
	if projectRoot != "" {
		got, err := loadStandaloneSkills(projectRoot, SourceProject, includeDisabled)
		if err != nil {
			return nil, fmt.Errorf("read project skill index: %w", err)
		}
		entries = append(entries, got...)
	}

	dedup := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if existing, ok := dedup[e.Handle]; ok && existing.Source == SourceProject && e.Source == SourceGlobal {
			continue
		}
		dedup[e.Handle] = e
	}
	deduped := make([]Entry, 0, len(dedup))
	for _, e := range dedup {
		deduped = append(deduped, e)
	}
	sort.Slice(deduped, func(i, j int) bool { return deduped[i].Handle < deduped[j].Handle })
	return NewCatalog(turnID, deduped), nil
}

func loadStandaloneSkills(root string, source Source, includeDisabled bool) ([]Entry, error) {
	skillsDir := filepath.Join(root, SkillsDir)
	return loadSkillIndexEntries(SkillsIndexPath(root), skillsDir, source, "", includeDisabled)
}

// BuildAgentCatalog enumerates skills owned by one assigned agent from
// <root>/agents/<agent>/SKILLS.md. Project entries override global entries for
// the same skill key. Disabled skills are excluded from the runtime catalog.
func BuildAgentCatalog(turnID, globalRoot, projectRoot, agentKey string) (*Catalog, error) {
	agentKey = strings.TrimSpace(agentKey)
	if agentKey == "" || !isValidSlug(agentKey) {
		return newCatalog(turnID, nil, true), nil
	}
	var entries []Entry
	if globalRoot != "" {
		got, err := loadAgentSkills(globalRoot, agentKey, SourceGlobal, false)
		if err != nil {
			return nil, fmt.Errorf("read global agent skill index: %w", err)
		}
		entries = append(entries, got...)
	}
	if projectRoot != "" {
		got, err := loadAgentSkills(projectRoot, agentKey, SourceProject, false)
		if err != nil {
			return nil, fmt.Errorf("read project agent skill index: %w", err)
		}
		entries = append(entries, got...)
	}
	dedup := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if existing, ok := dedup[e.Handle]; ok && existing.Source == SourceProject && e.Source == SourceGlobal {
			continue
		}
		e.Source = SourceAgent
		dedup[e.Handle] = e
	}
	deduped := make([]Entry, 0, len(dedup))
	for _, e := range dedup {
		deduped = append(deduped, e)
	}
	sort.Slice(deduped, func(i, j int) bool { return deduped[i].Handle < deduped[j].Handle })
	return newCatalog(turnID, deduped, true), nil
}

func loadAgentSkills(root, agentKey string, source Source, includeDisabled bool) ([]Entry, error) {
	agentDir := filepath.Join(root, AgentRootsDir, agentKey)
	skillsDir := filepath.Join(agentDir, SkillsDir)
	return loadSkillIndexEntries(AgentSkillsIndexPath(root, agentKey), skillsDir, source, agentKey, includeDisabled)
}

func loadSkillIndexEntries(indexPath, skillsDir string, source Source, agentKey string, includeDisabled bool) ([]Entry, error) {
	if info, err := os.Stat(skillsDir); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	} else if !info.IsDir() {
		return nil, nil
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]Entry, 0, 16)
	for _, header := range extractH2Headers(string(data)) {
		skill := header
		if agentKey != "" {
			prefix := agentKey + "/"
			if !strings.HasPrefix(header, prefix) {
				continue
			}
			skill = strings.TrimPrefix(header, prefix)
		}
		if strings.Contains(skill, "/") || !isValidSlug(skill) {
			continue
		}
		absPath := filepath.Join(skillsDir, skill, SkillFile)
		if _, err := os.Stat(absPath); err != nil {
			continue
		}
		if !includeDisabled && skillDisabledOnDisk(absPath) {
			continue
		}
		out = append(out, Entry{Handle: skill, Skill: skill, Source: source, AgentKey: agentKey, AbsolutePath: absPath})
	}
	return out, nil
}

const maxSkillFrontmatterBytes = 64 * 1024

// skillDisabledOnDisk reads only bounded YAML frontmatter from SKILL.md at path
// and returns true only when it explicitly sets skill.enabled: false. Missing
// files, parse errors, absent frontmatter, over-cap frontmatter, or an absent
// enabled field default to enabled (returns false).
func skillDisabledOnDisk(path string) bool {
	front, ok := readSkillFrontmatter(path)
	if !ok {
		return false
	}
	var parsed struct {
		Skill struct {
			Enabled *bool `yaml:"enabled"`
		} `yaml:"skill"`
	}
	if err := yaml.Unmarshal(front, &parsed); err != nil {
		return false
	}
	return parsed.Skill.Enabled != nil && !*parsed.Skill.Enabled
}

func readSkillFrontmatter(path string) ([]byte, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	prefix := make([]byte, 3)
	if n, err := io.ReadFull(f, prefix); err != nil || n != len(prefix) || string(prefix) != "---" {
		return nil, false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}

	limited := io.LimitReader(f, maxSkillFrontmatterBytes+1)
	reader := bufio.NewReaderSize(limited, 1024)
	first, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, false
	}
	if strings.TrimRight(first, "\r\n") != "---" || err == io.EOF {
		return nil, false
	}

	var front strings.Builder
	readBytes := len(first)
	for readBytes <= maxSkillFrontmatterBytes {
		line, err := reader.ReadString('\n')
		readBytes += len(line)
		if readBytes > maxSkillFrontmatterBytes {
			return nil, false
		}
		if strings.TrimRight(line, "\r\n") == "---" {
			return []byte(front.String()), true
		}
		if err != nil {
			return nil, false
		}
		front.WriteString(line)
	}
	return nil, false
}

// h2HeaderRegexp captures the slug on a "## <slug>" line. The slug character
// set accepts slashes so legacy agent indexes can be parsed by helper functions;
// standalone catalog loading rejects slash-containing headers.
var h2HeaderRegexp = regexp.MustCompile(`(?m)^##[ \t]+([A-Za-z0-9][A-Za-z0-9_./-]*)\s*$`)

func extractH2Headers(content string) []string {
	matches := h2HeaderRegexp.FindAllStringSubmatch(content, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			out = append(out, strings.TrimSpace(m[1]))
		}
	}
	return out
}

// isValidSlug rejects names that could be path-traversal, hidden directories, or
// names the runtime skill_view tool would later refuse to load.
func isValidSlug(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	first := name[0]
	if !((first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z') || (first >= '0' && first <= '9')) {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '.' || c == '-':
		default:
			return false
		}
	}
	return true
}
