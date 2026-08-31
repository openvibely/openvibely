package agentlibrary

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SupportFileKind is the allow-list of directories permitted inside a
// standalone skill package. Support files outside these directories are
// rejected.
type SupportFileKind string

const (
	SupportReferences SupportFileKind = "references"
	SupportTemplates  SupportFileKind = "templates"
	SupportScripts    SupportFileKind = "scripts"
	SupportAssets     SupportFileKind = "assets"
)

// SkillRoots tells the importer where the global and project skill libraries
// live on disk. Either may be empty if the target scope is not supported in
// the current deployment.
type SkillRoots struct {
	Global  string
	Project string
}

// RootForScope returns the configured root for a skill scope. An empty result
// means the importer cannot write skills of that scope.
func (r SkillRoots) RootForScope(scope string) string {
	switch scope {
	case "global":
		return r.Global
	case "project", "":
		if r.Project != "" {
			return r.Project
		}
		return r.Global
	}
	return ""
}

func (r SkillRoots) ExactRootForScope(scope string) string {
	switch scope {
	case "global":
		return r.Global
	case "project":
		return r.Project
	case "":
		return r.RootForScope(scope)
	}
	return ""
}

// SkillPackageFile is one non-SKILL.md support file included in a skill package
// import. Paths are package-relative using slash separators.
type SkillPackageFile struct {
	Path    string
	Content []byte
}

// SkillPackage captures normalized package input before it is persisted.
type SkillPackage struct {
	Decl         *SkillDeclaration
	Body         string
	PackageFiles []SkillPackageFile
}

// NormalizeStandaloneSkillPackage accepts either full OpenVibely SKILL.md
// declarations or common standalone skill frontmatter with top-level
// name/description fields and converts it into the declaration shape rendered by
// RenderSkillMarkdown. Existing valid frontmatter is normalized, not replaced.
func NormalizeStandaloneSkillPackage(content, packageName, scope string) (*SkillDeclaration, string, error) {
	decl, body, err := ParseDeclaration(content)
	if err == nil {
		if decl.IsAgentRootDeclaration() || strings.TrimSpace(decl.Agent.Key) != "" {
			return nil, body, errors.New("standalone skill packages must not set agent.key")
		}
		decl.Agent.Key = ""
		decl.Kind = "openvibely.agent_skill"
		if decl.Version <= 0 {
			decl.Version = 1
		}
		if strings.TrimSpace(decl.Skill.Scope) == "" {
			decl.Skill.Scope = scope
		} else if strings.TrimSpace(scope) != "" {
			decl.Skill.Scope = scope
		}
		if strings.TrimSpace(decl.Skill.Name) == "" {
			decl.Skill.Name = inferSkillName("", body)
			if decl.Skill.Name == "skill" {
				decl.Skill.Name = firstNonEmpty(packageName, decl.Skill.Key)
			}
		}
		if strings.TrimSpace(decl.Skill.Description) == "" {
			decl.Skill.Description = firstMarkdownParagraph(body)
		}
		if decl.Skill.Enabled == nil {
			decl.Skill.Enabled = boolPtr(true)
		}
		return decl, body, nil
	}
	front, body, ok := SplitFrontmatter(content)
	if !ok {
		name := inferSkillName(packageName, content)
		handle := packageName
		if !isSlug(handle) {
			handle = slugifySkillName(name)
		}
		if !isSlug(handle) {
			return nil, content, fmt.Errorf("raw skill package name %q is not a valid skill key", name)
		}
		return &SkillDeclaration{
			Kind:    "openvibely.agent_skill",
			Version: 1,
			Skill: SkillBlock{
				Key:         handle,
				Name:        name,
				Scope:       scope,
				Enabled:     boolPtr(true),
				Description: firstMarkdownParagraph(content),
			},
		}, content, nil
	}
	var standard struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		Kind        string `yaml:"kind"`
		Enabled     *bool  `yaml:"enabled"`
	}
	if yamlErr := yaml.Unmarshal([]byte(front), &standard); yamlErr != nil {
		return nil, body, fmt.Errorf("standard skill frontmatter: invalid YAML: %w", yamlErr)
	}
	name := strings.TrimSpace(standard.Name)
	if name == "" {
		return nil, body, err
	}
	handle := name
	if !isSlug(handle) && isSlug(packageName) {
		handle = packageName
	}
	if !isSlug(handle) {
		handle = slugifySkillName(name)
	}
	if !isSlug(handle) {
		return nil, body, fmt.Errorf("standard skill frontmatter name %q is not a valid skill key", name)
	}
	decl = &SkillDeclaration{
		Kind:    "openvibely.agent_skill",
		Version: 1,
		Skill: SkillBlock{
			Key:         handle,
			Name:        name,
			Scope:       scope,
			Enabled:     normalizeEnabledPointer(standard.Enabled),
			Description: strings.TrimSpace(standard.Description),
		},
	}
	return decl, body, nil
}

// ImportSkillPackage normalizes and writes a standalone skill package plus any
// allowed support files, returning the regular importer result.
func (i *Importer) ImportSkillPackage(ctx context.Context, content, packageName, scope string, files []SkillPackageFile) (*ImportResult, error) {
	decl, body, err := NormalizeStandaloneSkillPackage(content, packageName, scope)
	if err != nil {
		return nil, err
	}
	if decl.IsAgentRootDeclaration() || strings.TrimSpace(decl.Agent.Key) != "" {
		return nil, errors.New("standalone skill packages must not set agent.key")
	}
	decl.Agent.Key = ""
	decl.Skill.Scope = scope
	res, err := i.WriteSkill(ctx, decl, body)
	if err != nil {
		return res, err
	}
	for _, file := range files {
		kind, rel, err := splitPackageSupportPath(file.Path)
		if err != nil {
			return res, err
		}
		fileRes, err := i.WriteSupportFile(ctx, decl.Skill.Scope, decl.Skill.Key, kind, rel, file.Content)
		if err != nil {
			return res, err
		}
		res.ChangedPaths = append(res.ChangedPaths, fileRes.ChangedPaths...)
		res.Created = append(res.Created, fileRes.Created...)
		res.Updated = append(res.Updated, fileRes.Updated...)
	}
	return res, nil
}

// ReadSkillPackageFromPath reads a package source from a SKILL.md file or a
// directory containing SKILL.md. Only OpenVibely support directories are loaded.
func ReadSkillPackageFromPath(source string) (skillMD string, packageName string, files []SkillPackageFile, err error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", "", nil, errors.New("skill_import: source path is required")
	}
	info, err := os.Stat(source)
	if err != nil {
		return "", "", nil, fmt.Errorf("skill_import: stat %q: %w", source, err)
	}
	var skillPath, baseDir string
	if info.IsDir() {
		baseDir = source
		skillPath = filepath.Join(source, "SKILL.md")
		packageName = filepath.Base(filepath.Clean(source))
	} else {
		skillPath = source
		baseDir = filepath.Dir(source)
		packageName = filepath.Base(filepath.Clean(baseDir))
		if filepath.Base(source) != "SKILL.md" {
			return "", "", nil, fmt.Errorf("skill_import: source file must be SKILL.md, got %q", filepath.Base(source))
		}
	}
	data, err := os.ReadFile(skillPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("skill_import: read %s: %w", skillPath, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", "", nil, errors.New("skill_import: SKILL.md is empty")
	}
	files, err = readSkillPackageSupportFiles(baseDir)
	if err != nil {
		return "", "", nil, err
	}
	return string(data), packageName, files, nil
}

func readSkillPackageSupportFiles(baseDir string) ([]SkillPackageFile, error) {
	var files []SkillPackageFile
	for _, dir := range []string{string(SupportReferences), string(SupportTemplates), string(SupportScripts), string(SupportAssets)} {
		root := filepath.Join(baseDir, dir)
		info, err := os.Stat(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("skill_import: stat %s: %w", root, err)
		}
		if !info.IsDir() {
			continue
		}
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if strings.HasPrefix(entry.Name(), ".") && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			relFromBase, err := filepath.Rel(baseDir, path)
			if err != nil {
				return err
			}
			relFromBase = filepath.ToSlash(relFromBase)
			if _, _, err := splitPackageSupportPath(relFromBase); err != nil {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			files = append(files, SkillPackageFile{Path: relFromBase, Content: content})
			return nil
		}); err != nil {
			return nil, fmt.Errorf("skill_import: walk %s: %w", root, err)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func splitPackageSupportPath(path string) (SupportFileKind, string, error) {
	rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(path)))
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("skill_import: support file path %q is not allowed", path)
	}
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", "", fmt.Errorf("skill_import: support file path %q must be under references/, templates/, scripts/, or assets/", path)
	}
	kind := SupportFileKind(parts[0])
	if !validSupportKind(kind) {
		return "", "", fmt.Errorf("skill_import: support directory %q is not allowed", parts[0])
	}
	supportRel := filepath.ToSlash(filepath.Clean(parts[1]))
	if supportRel == "." || supportRel == ".." || strings.HasPrefix(supportRel, "../") || strings.Contains(supportRel, "/../") || strings.HasPrefix(filepath.Base(supportRel), ".") {
		return "", "", fmt.Errorf("skill_import: support file path %q is not allowed", path)
	}
	return kind, supportRel, nil
}

func normalizeEnabledPointer(enabled *bool) *bool {
	if enabled == nil {
		return boolPtr(true)
	}
	return enabled
}

func boolPtr(v bool) *bool { return &v }

func inferSkillName(packageName, content string) string {
	if strings.TrimSpace(packageName) != "" {
		return strings.TrimSpace(packageName)
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	return "skill"
}

func firstMarkdownParagraph(content string) string {
	for _, para := range strings.Split(strings.TrimSpace(content), "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" || strings.HasPrefix(para, "#") {
			continue
		}
		para = strings.Join(strings.Fields(para), " ")
		if len(para) > 160 {
			para = strings.TrimSpace(para[:160])
		}
		return para
	}
	return "Imported skill package."
}

func slugifySkillName(name string) string {
	var b strings.Builder
	lastSep := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastSep = false
		case r == '_' || r == '-' || r == '.':
			if b.Len() > 0 && !lastSep {
				b.WriteRune('_')
				lastSep = true
			}
		default:
			if b.Len() > 0 && !lastSep {
				b.WriteRune('_')
				lastSep = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "skill"
	}
	return out
}

// Applier is the backend authority for agent/skill changes. The importer
// proposes; the applier validates against agent config storage, protection
// flags, and project policy, then persists. Returning an error blocks the
// mutation (runbook §Backend Validation line 1773).
//
// Implementations typically wrap AgentRepo and the agent system-profile config
// helpers; see internal/service.AgentService for the production binding.
type Applier interface {
	// ApplyDeclaration upserts agent and skill records from the declaration.
	// Returns the list of config keys that actually changed for audit purposes.
	ApplyDeclaration(ctx context.Context, decl *SkillDeclaration) ([]string, error)
	// ArchiveAgent disables the agent and stores absorbed_into metadata. An
	// empty absorbedInto means the agent is archived with no replacement.
	ArchiveAgent(ctx context.Context, agentKey, absorbedInto, reason string) error
	// ArchiveSkill disables the skill within its owning agent's config.
	ArchiveSkill(ctx context.Context, handle, absorbedInto, reason string) error
	// IsProtected reports whether the given agent or skill key is protected
	// from autonomous edits (bundled, hub-installed, pinned, locked, or
	// manually protected per runbook §Backend Validation line 1780).
	IsProtected(ctx context.Context, targetType, key string) (bool, string, error)
}

// ImportResult is returned by the importer for every Apply call. The fields
// match the structured result the mutation tools surface to the model
// (runbook §Mutation Tool Contracts line 1656).
type ImportResult struct {
	Applied              bool     `json:"applied"`
	Created              []string `json:"created"`
	Updated              []string `json:"updated"`
	Archived             []string `json:"archived"`
	Blocked              []string `json:"blocked"`
	ChangedPaths         []string `json:"changed_paths"`
	ImportedConfigChange []string `json:"imported_config_changes"`
	EvidenceRefs         []string `json:"evidence_refs"`
}

// Importer applies SkillDeclaration objects to the filesystem and the agent
// config store. Filesystem writes are limited to the configured skill roots;
// arbitrary paths are rejected (runbook §Backend Validation line 1782).
//
// The importer writes standalone SKILL.md files, agent root SKILLS.md
// declarations, and DB records for agent declarations. It also maintains the
// minimal top-level skills/SKILLS.md index needed for catalog loading.
type Importer struct {
	roots   SkillRoots
	applier Applier
}

// NewImporter wires the importer with its roots and the backend applier.
func NewImporter(roots SkillRoots, applier Applier) *Importer {
	return &Importer{roots: roots, applier: applier}
}

// WriteSkill renders the declaration as SKILL.md (with original body
// preserved) and persists it under the configured root for the declared
// scope. The applier then upserts agent/skill config from the declaration.
//
// The top-level skills/SKILLS.md skill-link index is updated here so successful
// standalone skill mutations are catalog-visible immediately.
func (i *Importer) WriteSkill(ctx context.Context, decl *SkillDeclaration, body string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	if decl == nil {
		return nil, errors.New("importer: nil declaration")
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	if decl.IsAgentRootDeclaration() {
		return nil, errors.New("importer: WriteSkill requires skill.key; use WriteAgentRootDeclaration for agent SKILLS.md metadata")
	}
	root := i.roots.RootForScope(decl.Skill.Scope)
	if root == "" {
		return nil, fmt.Errorf("importer: no root configured for scope %q", decl.Skill.Scope)
	}
	if i.applier != nil {
		if protected, reason, err := i.applier.IsProtected(ctx, "skill", decl.Handle()); err != nil {
			return nil, fmt.Errorf("importer: protection check: %w", err)
		} else if protected {
			return &ImportResult{Blocked: []string{decl.Handle()}, ImportedConfigChange: []string{"protected: " + reason}}, nil
		}
	}

	skillDir, err := SkillDir(root, decl.Skill.Key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return nil, fmt.Errorf("importer: mkdir %s: %w", skillDir, err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		return nil, err
	}
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return nil, fmt.Errorf("importer: write %s: %w", path, err)
	}
	result := &ImportResult{
		Applied:      true,
		ChangedPaths: []string{path},
	}
	if indexPath, changed, err := i.ensureStandaloneSkillIndexEntry(root, decl); err != nil {
		return result, err
	} else if changed {
		result.ChangedPaths = append(result.ChangedPaths, indexPath)
	}
	if created {
		result.Created = []string{decl.Handle()}
	} else {
		result.Updated = []string{decl.Handle()}
	}
	return result, nil
}

// WriteAgentOwnedSkill renders the declaration as SKILL.md under
// <root>/agents/<agent>/skills/<skill>/SKILL.md and maintains that agent's
// SKILLS.md discovery index. The agent key is provided by the server-scoped
// caller, not by model input.
func (i *Importer) WriteAgentOwnedSkill(ctx context.Context, scope, agentKey string, decl *SkillDeclaration, body string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	if decl == nil {
		return nil, errors.New("importer: nil declaration")
	}
	if strings.TrimSpace(agentKey) == "" || !isSlug(agentKey) {
		return nil, fmt.Errorf("importer: invalid agent key %q", agentKey)
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	if decl.IsAgentRootDeclaration() {
		return nil, errors.New("importer: agent-owned skill declaration requires skill.key")
	}
	if strings.TrimSpace(decl.Agent.Key) != "" && decl.Agent.Key != agentKey {
		return nil, fmt.Errorf("importer: declaration agent.key %q does not match scoped agent %q", decl.Agent.Key, agentKey)
	}
	if scope == "" {
		scope = strings.TrimSpace(decl.Skill.Scope)
	}
	if scope == "" {
		return nil, errors.New("importer: agent-owned skill scope is required")
	}
	root := i.roots.ExactRootForScope(scope)
	if root == "" {
		return nil, fmt.Errorf("importer: no root configured for scope %q", scope)
	}
	handle := agentKey + "/" + decl.Skill.Key
	if i.applier != nil {
		if protected, reason, err := i.applier.IsProtected(ctx, "skill", handle); err != nil {
			return nil, fmt.Errorf("importer: protection check: %w", err)
		} else if protected {
			return &ImportResult{Blocked: []string{handle}, ImportedConfigChange: []string{"protected: " + reason}}, nil
		}
	}
	decl.Agent.Key = ""
	decl.Skill.Scope = ""
	skillDir, err := AgentSkillDir(root, agentKey, decl.Skill.Key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return nil, fmt.Errorf("importer: mkdir %s: %w", skillDir, err)
	}
	path := filepath.Join(skillDir, "SKILL.md")
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		return nil, err
	}
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return nil, fmt.Errorf("importer: write %s: %w", path, err)
	}
	result := &ImportResult{Applied: true, ChangedPaths: []string{path}}
	if indexPath, changed, err := i.ensureAgentSkillIndexEntry(root, agentKey, decl); err != nil {
		return result, err
	} else if changed {
		result.ChangedPaths = append(result.ChangedPaths, indexPath)
	}
	if created {
		result.Created = []string{handle}
	} else {
		result.Updated = []string{handle}
	}
	return result, nil
}

// WriteAgentOwnedSupportFile writes one references/templates/scripts/assets file
// under an existing skill package owned by the server-scoped agent.
func (i *Importer) WriteAgentOwnedSupportFile(ctx context.Context, scope, agentKey, handle string, kind SupportFileKind, relPath string, content []byte) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	abs, rel, key, err := i.resolveAgentOwnedSupportFilePath(ctx, scope, agentKey, handle, kind, relPath)
	if err != nil {
		return nil, err
	}
	item := agentKey + "/" + key + "/" + string(kind) + "/" + rel
	return writeResolvedSupportFile(abs, kind, content, item)
}

// RemoveAgentOwnedSupportFile removes one support file under a server-scoped
// agent-owned skill package.
func (i *Importer) RemoveAgentOwnedSupportFile(ctx context.Context, scope, agentKey, handle string, kind SupportFileKind, relPath string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	abs, rel, key, err := i.resolveAgentOwnedSupportFilePath(ctx, scope, agentKey, handle, kind, relPath)
	if err != nil {
		return nil, err
	}
	item := agentKey + "/" + key + "/" + string(kind) + "/" + rel
	return removeResolvedSupportFile(abs, relPath, item)
}

// WriteAgentRootDeclaration writes the agent-level declaration and Markdown index
// to <root>/agents/<agent>/SKILLS.md, then applies agent metadata and hooks. A
// root declaration must omit skill.key so agent metadata does not masquerade as a
// normal skill prompt.
func (i *Importer) WriteAgentRootDeclaration(ctx context.Context, decl *SkillDeclaration, body string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	if decl == nil {
		return nil, errors.New("importer: nil declaration")
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}
	if !decl.IsAgentRootDeclaration() {
		return nil, errors.New("importer: agent root declaration must omit skill.key")
	}
	root := i.roots.RootForScope(decl.Agent.Scope)
	if root == "" {
		return nil, fmt.Errorf("importer: no root configured for scope %q", decl.Agent.Scope)
	}
	if i.applier != nil {
		if protected, reason, err := i.applier.IsProtected(ctx, "agent", decl.Agent.Key); err != nil {
			return nil, fmt.Errorf("importer: protection check: %w", err)
		} else if protected {
			return &ImportResult{Blocked: []string{decl.Agent.Key}, ImportedConfigChange: []string{"protected: " + reason}}, nil
		}
	}
	agentDir := filepath.Join(root, "agents", decl.Agent.Key)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return nil, fmt.Errorf("importer: mkdir %s: %w", agentDir, err)
	}
	path := filepath.Join(agentDir, "SKILLS.md")
	if existing, err := os.ReadFile(path); err == nil {
		_, existingBody, _ := SplitFrontmatter(string(existing))
		body = mergeRootSkillIndexBodies(decl.Agent.Key, existingBody, body)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("importer: read %s: %w", path, err)
	}
	rendered, err := RenderSkillMarkdown(decl, body)
	if err != nil {
		return nil, err
	}
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		created = true
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return nil, fmt.Errorf("importer: write %s: %w", path, err)
	}
	result := &ImportResult{Applied: true, ChangedPaths: []string{path}}
	if created {
		result.Created = []string{decl.Agent.Key}
	} else {
		result.Updated = []string{decl.Agent.Key}
	}
	if indexPath, changed, err := i.ensureAgentRootIndexEntry(root, decl); err != nil {
		return result, err
	} else if changed {
		result.ChangedPaths = append(result.ChangedPaths, indexPath)
	}
	if i.applier != nil {
		changes, err := i.applier.ApplyDeclaration(ctx, decl)
		if err != nil {
			return result, fmt.Errorf("importer: apply: %w", err)
		}
		result.ImportedConfigChange = append(result.ImportedConfigChange, changes...)
	}
	return result, nil
}

// WriteSupportFile writes one references/templates/scripts/assets file under an
// existing skill folder. Path traversal and unknown subdirectories are rejected.
func (i *Importer) WriteSupportFile(ctx context.Context, scope, handle string, kind SupportFileKind, relPath string, content []byte) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	abs, rel, err := i.resolveSupportFilePath(ctx, scope, handle, kind, relPath)
	if err != nil {
		return nil, err
	}
	item := handle + "/" + string(kind) + "/" + rel
	return writeResolvedSupportFile(abs, kind, content, item)
}

// RemoveSupportFile removes one support file under an existing standalone skill
// package. It never removes SKILL.md or directories outside the support kind.
func (i *Importer) RemoveSupportFile(ctx context.Context, scope, handle string, kind SupportFileKind, relPath string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	abs, rel, err := i.resolveSupportFilePath(ctx, scope, handle, kind, relPath)
	if err != nil {
		return nil, err
	}
	item := handle + "/" + string(kind) + "/" + rel
	return removeResolvedSupportFile(abs, relPath, item)
}

func writeResolvedSupportFile(abs string, kind SupportFileKind, content []byte, item string) (*ImportResult, error) {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("importer: mkdir %s: %w", filepath.Dir(abs), err)
	}
	created := false
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		created = true
	}
	perm := os.FileMode(0o644)
	if kind == SupportScripts {
		perm = 0o755
	}
	if err := os.WriteFile(abs, content, perm); err != nil {
		return nil, fmt.Errorf("importer: write %s: %w", abs, err)
	}
	if kind == SupportScripts {
		if err := os.Chmod(abs, perm); err != nil {
			return nil, fmt.Errorf("importer: chmod %s: %w", abs, err)
		}
	}
	result := &ImportResult{Applied: true, ChangedPaths: []string{abs}}
	if created {
		result.Created = []string{item}
	} else {
		result.Updated = []string{item}
	}
	return result, nil
}

func removeResolvedSupportFile(abs, relPath, item string) (*ImportResult, error) {
	if err := os.Remove(abs); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("importer: support file %q not found", relPath)
		}
		return nil, fmt.Errorf("importer: remove %s: %w", abs, err)
	}
	return &ImportResult{Applied: true, Archived: []string{item}, ChangedPaths: []string{abs}}, nil
}

func (i *Importer) resolveAgentOwnedSupportFilePath(ctx context.Context, scope, agentKey, handle string, kind SupportFileKind, relPath string) (string, string, string, error) {
	if strings.TrimSpace(agentKey) == "" || !isSlug(agentKey) {
		return "", "", "", fmt.Errorf("importer: invalid agent key %q", agentKey)
	}
	if scope == "" {
		return "", "", "", errors.New("importer: agent-owned support file scope is required")
	}
	root := i.roots.ExactRootForScope(scope)
	if root == "" {
		return "", "", "", fmt.Errorf("importer: no root configured for scope %q", scope)
	}
	if strings.Contains(handle, "/") {
		return "", "", "", fmt.Errorf("invalid agent-owned skill handle %q; pass only the skill key", handle)
	}
	if !isSlug(handle) {
		return "", "", "", fmt.Errorf("invalid skill key %q", handle)
	}
	if !validSupportKind(kind) {
		return "", "", "", fmt.Errorf("importer: support kind %q not allowed", kind)
	}
	rel := filepath.Clean(relPath)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
		return "", "", "", fmt.Errorf("importer: invalid support file path %q", relPath)
	}
	protectedHandle := agentKey + "/" + handle
	if i.applier != nil {
		if protected, reason, err := i.applier.IsProtected(ctx, "skill", protectedHandle); err != nil {
			return "", "", "", fmt.Errorf("importer: protection check: %w", err)
		} else if protected {
			return "", "", "", fmt.Errorf("protected: %s", reason)
		}
	}
	skillDir, err := AgentSkillDir(root, agentKey, handle)
	if err != nil {
		return "", "", "", err
	}
	abs := filepath.Join(skillDir, string(kind), rel)
	skillDirAbs, err := filepath.Abs(skillDir)
	if err != nil {
		return "", "", "", err
	}
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return "", "", "", err
	}
	if !strings.HasPrefix(absResolved, skillDirAbs+string(filepath.Separator)) && absResolved != skillDirAbs {
		return "", "", "", fmt.Errorf("importer: support path %q escapes skill folder", relPath)
	}
	return abs, rel, handle, nil
}

func (i *Importer) resolveSupportFilePath(ctx context.Context, scope, handle string, kind SupportFileKind, relPath string) (string, string, error) {
	root := i.roots.RootForScope(scope)
	if root == "" {
		return "", "", fmt.Errorf("importer: no root configured for scope %q", scope)
	}
	_, skillKey, err := SplitHandle(handle)
	if err != nil {
		return "", "", err
	}
	if !validSupportKind(kind) {
		return "", "", fmt.Errorf("importer: support kind %q not allowed", kind)
	}
	rel := filepath.Clean(relPath)
	if rel == "" || rel == "." || strings.HasPrefix(rel, "..") || strings.Contains(rel, "..") || filepath.IsAbs(rel) {
		return "", "", fmt.Errorf("importer: invalid support file path %q", relPath)
	}
	if i.applier != nil {
		if protected, reason, err := i.applier.IsProtected(ctx, "skill", handle); err != nil {
			return "", "", fmt.Errorf("importer: protection check: %w", err)
		} else if protected {
			return "", "", fmt.Errorf("protected: %s", reason)
		}
	}
	skillDir, err := SkillDir(root, skillKey)
	if err != nil {
		return "", "", err
	}
	abs := filepath.Join(skillDir, string(kind), rel)
	skillDirAbs, err := filepath.Abs(skillDir)
	if err != nil {
		return "", "", err
	}
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(absResolved, skillDirAbs+string(filepath.Separator)) && absResolved != skillDirAbs {
		return "", "", fmt.Errorf("importer: support path %q escapes skill folder", relPath)
	}
	return abs, rel, nil
}

// ArchiveSkill marks a standalone skill archived on disk. Filesystem files are
// not deleted; the frontmatter records absorbed_into/reason and the top-level
// skills/SKILLS.md discovery index is updated. If an applier is configured, it
// is used only for protection checks and legacy audit compatibility.
func (i *Importer) ArchiveSkill(ctx context.Context, handle, absorbedInto, reason string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	if _, _, err := SplitHandle(handle); err != nil {
		return nil, err
	}
	var imported []string
	if i.applier != nil {
		if protected, why, err := i.applier.IsProtected(ctx, "skill", handle); err != nil {
			return nil, err
		} else if protected {
			return &ImportResult{Blocked: []string{handle}, ImportedConfigChange: []string{"protected: " + why}}, nil
		}
		if err := i.applier.ArchiveSkill(ctx, handle, absorbedInto, reason); err != nil {
			return nil, err
		}
		imported = append(imported, "skill:"+handle)
	}
	res := &ImportResult{Applied: true, Archived: []string{handle}, ImportedConfigChange: imported}
	changed, err := i.markArchivedSkillOnDisk(handle, absorbedInto, reason)
	if err != nil {
		return res, err
	}
	res.ChangedPaths = append(res.ChangedPaths, changed...)
	return res, nil
}

// ArchiveAgent marks an agent archived through the applier.
func (i *Importer) ArchiveAgent(ctx context.Context, agentKey, absorbedInto, reason string) (*ImportResult, error) {
	if i == nil {
		return nil, errors.New("importer: nil")
	}
	if i.applier == nil {
		return nil, errors.New("importer: archive requires applier")
	}
	if protected, why, err := i.applier.IsProtected(ctx, "agent", agentKey); err != nil {
		return nil, err
	} else if protected {
		return &ImportResult{Blocked: []string{agentKey}, ImportedConfigChange: []string{"protected: " + why}}, nil
	}
	if err := i.applier.ArchiveAgent(ctx, agentKey, absorbedInto, reason); err != nil {
		return nil, err
	}
	return &ImportResult{Applied: true, Archived: []string{agentKey}}, nil
}

// SkillDir returns the conventional standalone skill directory beneath a root
// after validating the slug. The segment must be slug-safe to prevent path
// traversal or hidden-directory writes.
func SkillDir(root, skillKey string) (string, error) {
	if !isSlug(skillKey) {
		return "", fmt.Errorf("invalid skill key %q", skillKey)
	}
	return filepath.Join(root, "skills", skillKey), nil
}

// AgentSkillDir returns the legacy/agent-owned skill directory. It is used for
// protected system hook skills and manually owned agent implementation skills,
// not for routed standalone skill_manage writes.
func AgentSkillDir(root, agentKey, skillKey string) (string, error) {
	if !isSlug(agentKey) {
		return "", fmt.Errorf("invalid agent key %q", agentKey)
	}
	if !isSlug(skillKey) {
		return "", fmt.Errorf("invalid skill key %q", skillKey)
	}
	return filepath.Join(root, "agents", agentKey, "skills", skillKey), nil
}

// SplitHandle parses a standalone skill handle. Legacy "<agent>/<skill>" handles
// are rejected by skill_manage so generated skills cannot masquerade as agent
// owned skills.
func SplitHandle(handle string) (agentKey, skillKey string, err error) {
	if strings.Contains(handle, "/") || !isSlug(handle) {
		return "", "", fmt.Errorf("invalid standalone skill handle %q", handle)
	}
	return "", handle, nil
}

func validSupportKind(k SupportFileKind) bool {
	switch k {
	case SupportReferences, SupportTemplates, SupportScripts, SupportAssets:
		return true
	}
	return false
}

// RenderSkillMarkdown produces SKILL.md content from a declaration and body.
// Standalone skills intentionally render only skill metadata; agent identity,
// permissions, hooks, tools, and model defaults belong to agent root declarations.
func RenderSkillMarkdown(decl *SkillDeclaration, body string) (string, error) {
	if decl == nil {
		return "", errors.New("render: nil declaration")
	}
	renderDecl := any(decl)
	if !decl.IsAgentRootDeclaration() {
		renderDecl = standaloneSkillFrontmatter{
			Kind:    decl.Kind,
			Version: decl.Version,
			Skill:   standaloneSkillBlockFrom(decl.Skill),
			Routing: standaloneRoutingBlockFrom(decl.Routing),
		}
	}
	raw, err := yaml.Marshal(renderDecl)
	if err != nil {
		return "", fmt.Errorf("render: marshal: %w", err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(raw)
	if !strings.HasSuffix(string(raw), "\n") {
		b.WriteString("\n")
	}
	b.WriteString("---\n")
	if body != "" {
		if !strings.HasPrefix(body, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(body)
	}
	return b.String(), nil
}

type standaloneSkillFrontmatter struct {
	Kind    string                  `yaml:"kind"`
	Version int                     `yaml:"version"`
	Skill   standaloneSkillBlock    `yaml:"skill"`
	Routing *standaloneRoutingBlock `yaml:"routing,omitempty"`
}

type standaloneSkillBlock struct {
	Key           string `yaml:"key"`
	Name          string `yaml:"name,omitempty"`
	Scope         string `yaml:"scope,omitempty"`
	Enabled       *bool  `yaml:"enabled,omitempty"`
	Description   string `yaml:"description,omitempty"`
	Archived      bool   `yaml:"archived,omitempty"`
	AbsorbedInto  string `yaml:"absorbed_into,omitempty"`
	ArchiveReason string `yaml:"archive_reason,omitempty"`
}

type standaloneRoutingBlock struct {
	Triggers    []string `yaml:"triggers,omitempty"`
	Priority    int      `yaml:"priority,omitempty"`
	Description string   `yaml:"description,omitempty"`
}

func standaloneSkillBlockFrom(s SkillBlock) standaloneSkillBlock {
	return standaloneSkillBlock{
		Key:           s.Key,
		Name:          s.Name,
		Scope:         s.Scope,
		Enabled:       s.Enabled,
		Description:   s.Description,
		Archived:      s.Archived,
		AbsorbedInto:  s.AbsorbedInto,
		ArchiveReason: s.ArchiveReason,
	}
}

func standaloneRoutingBlockFrom(r RoutingBlock) *standaloneRoutingBlock {
	out := standaloneRoutingBlock{
		Triggers:    r.Triggers,
		Priority:    r.Priority,
		Description: r.Description,
	}
	if len(out.Triggers) == 0 && out.Priority == 0 && out.Description == "" {
		return nil
	}
	return &out
}

func (i *Importer) markArchivedSkillOnDisk(handle, absorbedInto, reason string) ([]string, error) {
	_, skillKey, err := SplitHandle(handle)
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, root := range []string{i.roots.Project, i.roots.Global} {
		if root == "" {
			continue
		}
		skillPath := filepath.Join(root, "skills", skillKey, "SKILL.md")
		data, readErr := os.ReadFile(skillPath)
		if readErr != nil {
			if errors.Is(readErr, os.ErrNotExist) {
				continue
			}
			return changed, fmt.Errorf("importer: read %s: %w", skillPath, readErr)
		}
		decl, body, parseErr := ParseDeclaration(string(data))
		if parseErr == nil && decl.Skill.Key == skillKey {
			falseValue := false
			decl.Skill.Enabled = &falseValue
			decl.Skill.Archived = true
			decl.Skill.AbsorbedInto = absorbedInto
			decl.Skill.ArchiveReason = reason
			rendered, renderErr := RenderSkillMarkdown(decl, body)
			if renderErr != nil {
				return changed, renderErr
			}
			if string(data) != rendered {
				if err := os.WriteFile(skillPath, []byte(rendered), 0o644); err != nil {
					return changed, fmt.Errorf("importer: write %s: %w", skillPath, err)
				}
				changed = append(changed, skillPath)
			}
		}
		rootPath := filepath.Join(root, "skills", "SKILLS.md")
		if rootChanged, rootErr := removeSkillIndexEntry(rootPath, handle); rootErr != nil {
			return changed, rootErr
		} else if rootChanged {
			changed = append(changed, rootPath)
		}
	}
	return changed, nil
}

func (i *Importer) ensureAgentRootIndexEntry(root string, decl *SkillDeclaration) (string, bool, error) {
	agentKey := strings.TrimSpace(decl.Agent.Key)
	if agentKey == "" {
		return "", false, nil
	}
	agentsDir := filepath.Join(root, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return "", false, fmt.Errorf("importer: mkdir %s: %w", agentsDir, err)
	}
	path := filepath.Join(agentsDir, "AGENTS.md")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return path, false, fmt.Errorf("importer: read %s: %w", path, err)
	}
	body := ""
	if err == nil {
		body = string(data)
	}
	merged := appendAgentRootIndexEntry(body, decl)
	if strings.TrimSpace(merged) == strings.TrimSpace(body) && err == nil {
		return path, false, nil
	}
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		return path, false, fmt.Errorf("importer: write %s: %w", path, err)
	}
	return path, true, nil
}

func (i *Importer) ensureAgentSkillIndexEntry(root, agentKey string, decl *SkillDeclaration) (string, bool, error) {
	if decl == nil || decl.IsAgentRootDeclaration() {
		return "", false, nil
	}
	agentDir := filepath.Join(root, "agents", agentKey)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return "", false, fmt.Errorf("importer: mkdir %s: %w", agentDir, err)
	}
	path := filepath.Join(agentDir, "SKILLS.md")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return path, false, fmt.Errorf("importer: read %s: %w", path, err)
	}
	body := ""
	if err == nil {
		front, existingBody, ok := SplitFrontmatter(string(data))
		if ok {
			_ = front
			body = existingBody
		} else {
			body = string(data)
		}
	}
	merged := appendAgentSkillIndexEntry(body, agentKey, decl)
	if strings.TrimSpace(merged) == strings.TrimSpace(body) && err == nil {
		return path, false, nil
	}
	if err == nil {
		front, _, ok := SplitFrontmatter(string(data))
		if ok {
			merged = "---\n" + strings.TrimSpace(front) + "\n---\n\n" + merged
		}
	}
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		return path, false, fmt.Errorf("importer: write %s: %w", path, err)
	}
	return path, true, nil
}

func (i *Importer) ensureStandaloneSkillIndexEntry(root string, decl *SkillDeclaration) (string, bool, error) {
	if decl == nil || decl.IsAgentRootDeclaration() {
		return "", false, nil
	}
	skillsDir := filepath.Join(root, "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return "", false, fmt.Errorf("importer: mkdir %s: %w", skillsDir, err)
	}
	path := filepath.Join(skillsDir, "SKILLS.md")
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return path, false, fmt.Errorf("importer: read %s: %w", path, err)
	}
	body := ""
	if err == nil {
		body = string(data)
	}
	merged := appendSkillIndexEntry(body, decl)
	if strings.TrimSpace(merged) == strings.TrimSpace(body) && err == nil {
		return path, false, nil
	}
	if err := os.WriteFile(path, []byte(merged), 0o644); err != nil {
		return path, false, fmt.Errorf("importer: write %s: %w", path, err)
	}
	return path, true, nil
}

func mergeRootSkillIndexBodies(agentKey, existingBody, newBody string) string {
	merged := strings.TrimRight(newBody, "\n")
	for _, section := range splitSkillIndexSections(existingBody) {
		handle := sectionHandle(section)
		if handle == "" || !strings.HasPrefix(handle, agentKey+"/") || bodyHasSkillHandle(merged, handle) {
			continue
		}
		if strings.TrimSpace(merged) != "" {
			merged += "\n\n"
		}
		merged += strings.Trim(section, "\n")
	}
	if strings.TrimSpace(merged) == "" {
		return newBody
	}
	return merged + "\n"
}

func removeSkillIndexEntry(path, handle string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("importer: read %s: %w", path, err)
	}
	frontmatter, body, hasFrontmatter := SplitFrontmatter(string(data))
	updated := removeSkillIndexSection(body, handle)
	if strings.TrimSpace(updated) == strings.TrimSpace(body) {
		return false, nil
	}
	var rendered string
	if hasFrontmatter {
		rendered = "---\n" + strings.TrimSpace(frontmatter) + "\n---\n"
		if updated != "" && !strings.HasPrefix(updated, "\n") {
			rendered += "\n"
		}
		rendered += updated
	} else {
		rendered = updated
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		return false, fmt.Errorf("importer: write %s: %w", path, err)
	}
	return true, nil
}

// RemoveAgentIndexEntry removes the ## <key> section for the given agent key
// from the AGENTS.md index at path. It is a no-op when the file does not exist
// or the key is not present. Returns true when the file was modified.
func RemoveAgentIndexEntry(path, key string) (bool, error) {
	return removeSkillIndexEntry(path, key)
}

func removeSkillIndexSection(body, handle string) string {
	lines := strings.Split(body, "\n")
	sections := splitSkillIndexSections(body)
	if len(sections) == 0 {
		return body
	}
	removeStart := -1
	removeEnd := -1
	for _, section := range sections {
		if sectionHandle(section) != handle {
			continue
		}
		sectionLines := strings.Split(section, "\n")
		for i := 0; i <= len(lines)-len(sectionLines); i++ {
			if strings.Join(lines[i:i+len(sectionLines)], "\n") == section {
				removeStart = i
				removeEnd = i + len(sectionLines)
				break
			}
		}
		break
	}
	if removeStart < 0 {
		return body
	}
	for removeStart > 0 && strings.TrimSpace(lines[removeStart-1]) == "" {
		removeStart--
		break
	}
	for removeEnd < len(lines) && strings.TrimSpace(lines[removeEnd]) == "" {
		removeEnd++
		break
	}
	updated := append([]string{}, lines[:removeStart]...)
	updated = append(updated, lines[removeEnd:]...)
	return strings.TrimRight(strings.Join(updated, "\n"), "\n") + "\n"
}

func appendAgentRootIndexEntry(body string, decl *SkillDeclaration) string {
	agentKey := strings.TrimSpace(decl.Agent.Key)
	if agentKey == "" {
		return body
	}
	section := renderAgentRootIndexSection(decl)
	trimmedBody := strings.TrimRight(body, "\n")
	sections := splitSkillIndexSections(trimmedBody)
	for _, existing := range sections {
		if sectionHandle(existing) != agentKey {
			continue
		}
		updated := strings.Replace(trimmedBody, strings.TrimRight(existing, "\n"), strings.TrimRight(section, "\n"), 1)
		return strings.TrimRight(updated, "\n") + "\n"
	}
	if strings.TrimSpace(trimmedBody) == "" {
		trimmedBody = "# Agents"
	}
	if strings.TrimSpace(trimmedBody) != "" {
		trimmedBody += "\n\n"
	}
	return trimmedBody + section
}

func renderAgentRootIndexSection(decl *SkillDeclaration) string {
	agentKey := strings.TrimSpace(decl.Agent.Key)
	name := firstNonEmpty(decl.Agent.AgentDisplayName(), titleFromSlug(agentKey))
	desc := strings.TrimSpace(decl.Agent.Description)
	line := fmt.Sprintf("[%s](%s/SKILLS.md)", name, agentKey)
	if desc != "" {
		line += " — " + desc
	}
	return fmt.Sprintf("## %s\n\n%s\n", agentKey, line)
}

func appendAgentSkillIndexEntry(body, agentKey string, decl *SkillDeclaration) string {
	skillKey := decl.Handle()
	if skillKey == "" {
		return body
	}
	handle := agentKey + "/" + skillKey
	if bodyHasSkillHandle(body, handle) {
		return body
	}
	trimmed := strings.TrimRight(body, "\n")
	if strings.TrimSpace(trimmed) == "" {
		trimmed = "# " + titleFromSlug(agentKey) + " Skills"
	}
	if strings.TrimSpace(trimmed) != "" {
		trimmed += "\n\n"
	}
	name := firstNonEmpty(decl.Skill.Name, titleFromSlug(skillKey))
	desc := strings.TrimSpace(decl.Skill.Description)
	line := fmt.Sprintf("[%s](skills/%s/SKILL.md)", name, skillKey)
	if desc != "" {
		line += " — " + desc
	}
	return fmt.Sprintf("%s## %s\n\n%s\n", trimmed, handle, line)
}

func appendSkillIndexEntry(body string, decl *SkillDeclaration) string {
	handle := decl.Handle()
	if handle == "" {
		return body
	}
	section := renderSkillIndexSection(decl)
	trimmed := strings.TrimRight(body, "\n")
	for _, existing := range splitSkillIndexSections(trimmed) {
		if sectionHandle(existing) != handle {
			continue
		}
		updated := strings.Replace(trimmed, strings.TrimRight(existing, "\n"), strings.TrimRight(section, "\n"), 1)
		return strings.TrimRight(updated, "\n") + "\n"
	}
	if strings.TrimSpace(trimmed) == "" {
		trimmed = "# Standalone Skills"
	}
	if strings.TrimSpace(trimmed) != "" {
		trimmed += "\n\n"
	}
	return trimmed + section
}

func renderSkillIndexSection(decl *SkillDeclaration) string {
	name := firstNonEmpty(decl.Skill.Name, titleFromSlug(decl.Skill.Key))
	desc := strings.TrimSpace(decl.Skill.Description)
	line := fmt.Sprintf("[%s](%s/SKILL.md)", name, decl.Skill.Key)
	if desc != "" {
		line += " — " + desc
	}
	return fmt.Sprintf("## %s\n\n%s\n", decl.Handle(), line)
}

func splitSkillIndexSections(body string) []string {
	lines := strings.Split(body, "\n")
	var sections []string
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "## ") {
			if start >= 0 {
				sections = append(sections, strings.Join(lines[start:i], "\n"))
			}
			start = i
		}
	}
	if start >= 0 {
		sections = append(sections, strings.Join(lines[start:], "\n"))
	}
	return sections
}

func sectionHandle(section string) string {
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "## "))
		}
	}
	return ""
}

func bodyHasSkillHandle(body, handle string) bool {
	for _, section := range splitSkillIndexSections(body) {
		if sectionHandle(section) == handle {
			return true
		}
	}
	return false
}

func titleFromSlug(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '_' || r == '-' || r == '.' })
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, " ")
}
