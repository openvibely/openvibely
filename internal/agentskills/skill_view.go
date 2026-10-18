package agentskills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

// handleShape matches the canonical bare skill handle form. We deliberately
// reject paths, parents, agent prefixes, and trailing /SKILL.md for bare handles.
var handleShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// qualifiedSkillViewHandleShape accepts explicit maintenance/runtime view handles
// returned by skills_list or agent-owned catalogs.
var qualifiedSkillViewHandleShape = regexp.MustCompile(`^(standalone|skill):[A-Za-z0-9][A-Za-z0-9_.-]*$|^agent:[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// agentKeyShape matches the slug constraints we apply elsewhere. agent_view
// rejects keys that don't match before calling the inspector.
var agentKeyShape = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// AgentSummary is the prompt-safe agent index record returned by agent_list.
type AgentSummary struct {
	Key             string   `json:"key"`
	Name            string   `json:"name,omitempty"`
	Description     string   `json:"description,omitempty"`
	Scope           string   `json:"scope,omitempty"`
	Enabled         bool     `json:"enabled"`
	Selectable      bool     `json:"selectable_as_primary,omitempty"`
	AttachedSkills  []string `json:"attached_skills,omitempty"`
	GeneratedStatus string   `json:"generated_status,omitempty"`
}

// AgentDetails is the prompt-safe agent record returned by agent_view.
type AgentDetails struct {
	Key             string          `json:"key"`
	Name            string          `json:"name,omitempty"`
	Description     string          `json:"description,omitempty"`
	SystemPrompt    string          `json:"system_prompt,omitempty"`
	Scope           string          `json:"scope,omitempty"`
	Enabled         bool            `json:"enabled"`
	Selectable      bool            `json:"selectable_as_primary,omitempty"`
	ToolGrants      []string        `json:"tool_grants,omitempty"`
	Permissions     map[string]any  `json:"permissions,omitempty"`
	ModelDefaults   map[string]any  `json:"model_defaults,omitempty"`
	Hooks           []AgentHookView `json:"hooks,omitempty"`
	AttachedSkills  []string        `json:"attached_skills,omitempty"`
	GeneratedStatus string          `json:"generated_status,omitempty"`
}

type AgentHookView struct {
	When           string `json:"when"`
	SkillKey       string `json:"skill_key"`
	OutputContract string `json:"output_contract,omitempty"`
	Blocking       bool   `json:"blocking,omitempty"`
	Enabled        bool   `json:"enabled"`
}

type AgentInspector interface {
	ListAgents(ctx context.Context) ([]AgentSummary, error)
	InspectAgent(ctx context.Context, agentKey string) (*AgentDetails, error)
}

// SkillRuntimeTools returns request-scoped runtime tools lifecycle hooks use for
// read-only standalone skill discovery.
func SkillRuntimeTools(catalog *Catalog, globalRoot, projectRoot string, inspector AgentInspector) *llmcontracts.RuntimeTools {
	return skillRuntimeTools(catalog, globalRoot, projectRoot, inspector, true, false)
}

// SelectedSkillRuntimeTools returns skill_view scoped to the permitted turn catalog.
// The main model receives that catalog as an index; broader library discovery
// tools are reserved for maintenance.
func SelectedSkillRuntimeTools(catalog *Catalog) *llmcontracts.RuntimeTools {
	return skillRuntimeTools(catalog, "", "", nil, false, true)
}

func skillRuntimeTools(catalog *Catalog, globalRoot, projectRoot string, inspector AgentInspector, includeLibraryTools, prepareDependencies bool) *llmcontracts.RuntimeTools {
	if catalog == nil {
		return nil
	}

	definitions := []llmcontracts.RuntimeToolDefinition{skillView}
	tools := map[string]struct{}{"skill_view": {}}
	if includeLibraryTools {

		definitions = append(definitions, skillsList)
		tools["skills_list"] = struct{}{}
		if inspector != nil {

			definitions = append(definitions, agentList, agentView)
			tools["agent_list"] = struct{}{}
			tools["agent_view"] = struct{}{}
		}
	}
	isMine := func(name string) bool {
		_, ok := tools[strings.ToLower(strings.TrimSpace(name))]
		return ok
	}
	return &llmcontracts.RuntimeTools{
		Definitions: definitions,
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "skill_view":
				body, err := resolveSkillView(catalog, input)
				if err == nil && prepareDependencies {
					body, err = prepareSkillView(ctx, body)
				}
				if err != nil {
					return err.Error(), true, true, nil
				}
				return body, true, false, nil
			case "skills_list":
				if !includeLibraryTools {
					break
				}
				body, err := resolveSkillsList(globalRoot, projectRoot, input)
				if err != nil {
					return err.Error(), true, true, nil
				}
				return body, true, false, nil
			case "agent_list":
				if !includeLibraryTools {
					break
				}
				if inspector == nil {
					return "agent_list: no inspector configured", true, true, nil
				}
				body, err := resolveAgentList(ctx, inspector, input)
				if err != nil {
					return err.Error(), true, true, nil
				}
				return body, true, false, nil
			case "agent_view":
				if !includeLibraryTools {
					break
				}
				if inspector == nil {
					return "agent_view: no inspector configured", true, true, nil
				}
				body, err := resolveAgentView(ctx, inspector, input)
				if err != nil {
					return err.Error(), true, true, nil
				}
				return body, true, false, nil
			}
			return "", false, false, nil
		},
		Filter: func(toolName string) (bool, bool) {
			if isMine(toolName) {
				return true, true
			}
			return false, false
		},
	}
}

func resolveSkillsList(globalRoot, projectRoot string, input json.RawMessage) (string, error) {
	scope := parseScope(input)
	var out strings.Builder
	hits := 0
	if (scope == "global" || scope == "all") && globalRoot != "" {
		skillsDir := filepath.Join(globalRoot, SkillsDir)
		if body, ok := filteredIndexBody(SkillsIndexPath(globalRoot), skillsDir, ""); ok {
			fmt.Fprintf(&out, "=== global ===\n%s\n", strings.TrimRight(body, "\n"))
			hits++
		}
	}
	if (scope == "project" || scope == "all") && projectRoot != "" {
		skillsDir := filepath.Join(projectRoot, SkillsDir)
		if body, ok := filteredIndexBody(SkillsIndexPath(projectRoot), skillsDir, ""); ok {
			if hits > 0 {
				out.WriteString("\n")
			}
			fmt.Fprintf(&out, "=== project ===\n%s\n", strings.TrimRight(body, "\n"))
			hits++
		}
	}
	if hits == 0 {
		return "(no top-level skills/SKILLS.md found for the requested scope)", nil
	}
	handles, err := standaloneViewHandlesFromCatalog(scope, globalRoot, projectRoot)
	if err != nil {
		return "", err
	}
	if len(handles) > 0 {
		out.WriteString("\n\n=== view_handles ===\n")
		for _, handle := range handles {
			fmt.Fprintf(&out, "- %s\n", handle)
		}
	}
	return out.String(), nil
}

func standaloneViewHandlesFromCatalog(scope, globalRoot, projectRoot string) ([]string, error) {
	useGlobal := scope == "global" || scope == "all"
	useProject := scope == "project" || scope == "all"
	catalogGlobal := ""
	catalogProject := ""
	if useGlobal {
		catalogGlobal = globalRoot
	}
	if useProject {
		catalogProject = projectRoot
	}
	catalog, err := BuildCatalog("skills-list", catalogGlobal, catalogProject)
	if err != nil {
		return nil, fmt.Errorf("skills_list: build view handles: %w", err)
	}
	var handles []string
	for _, entry := range catalog.Entries() {
		for _, handle := range qualifiedSkillHandles(entry) {
			if strings.HasPrefix(handle, "standalone:") {
				handles = append(handles, handle)
			}
		}
	}
	return uniqueSortedStrings(handles), nil
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func parseScope(input json.RawMessage) string {
	var params struct {
		Scope string `json:"scope"`
	}
	if len(input) > 0 {
		_ = json.Unmarshal(input, &params)
	}
	scope := strings.ToLower(strings.TrimSpace(params.Scope))
	if scope == "" {
		scope = "all"
	}
	switch scope {
	case "global", "project", "all":
		return scope
	default:
		return "all"
	}
}

func isValidSkillViewHandle(handle string) bool {
	return handleShape.MatchString(handle) || qualifiedSkillViewHandleShape.MatchString(handle)
}

func resolveSkillView(catalog *Catalog, input json.RawMessage) (string, error) {
	var params struct {
		Handle   string `json:"handle"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("skill_view: invalid input: %w", err)
	}
	handle := strings.TrimSpace(params.Handle)
	if !isValidSkillViewHandle(handle) {
		return "", fmt.Errorf("skill_view: handle %q is not a valid skill_view handle", handle)
	}
	entry, ok, ambiguous := catalog.ResolveSkillHandle(handle)
	if ambiguous {
		return "", fmt.Errorf("skill_view: handle %q is ambiguous in this turn; use a qualified handle such as standalone:%s or agent:<agent>/%s", handle, handle, handle)
	}
	if !ok {
		return "", fmt.Errorf("skill_view: handle %q is not in this turn's authorized index", handle)
	}
	if skillDisabledOnDisk(entry.AbsolutePath) {
		return "", fmt.Errorf("skill_view: skill is disabled")
	}
	if strings.TrimSpace(params.FilePath) != "" {
		return resolveSkillSupportFile(entry, params.FilePath)
	}
	body, err := os.ReadFile(entry.AbsolutePath)
	if err != nil {
		return "", fmt.Errorf("skill_view: read %q: %w", handle, err)
	}
	skillDir := filepath.Dir(entry.AbsolutePath)
	view := skillViewResponse{
		Handle:      handle,
		Source:      string(entry.Source),
		AgentKey:    entry.AgentKey,
		Body:        substituteSkillDirTokens(string(body), skillDir),
		SkillDir:    skillDir,
		ScriptsDir:  filepath.Join(skillDir, "scripts"),
		LinkedFiles: listLinkedSupportFiles(skillDir),
	}
	encoded, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return "", fmt.Errorf("skill_view: encode %q: %w", handle, err)
	}
	return string(encoded), nil
}

type skillViewResponse struct {
	Dependencies []skilldeps.Result  `json:"dependencies,omitempty"`
	Environment  map[string]string   `json:"environment,omitempty"`
	Handle       string              `json:"handle"`
	Source       string              `json:"source,omitempty"`
	AgentKey     string              `json:"agent_key,omitempty"`
	Body         string              `json:"body"`
	SkillDir     string              `json:"skill_dir"`
	ScriptsDir   string              `json:"scripts_dir"`
	LinkedFiles  map[string][]string `json:"linked_files,omitempty"`
}

func substituteSkillDirTokens(body, skillDir string) string {
	if skillDir == "" || body == "" {
		return body
	}
	body = strings.ReplaceAll(body, "${OPENVIBELY_SKILL_DIR}", skillDir)
	body = strings.ReplaceAll(body, "$OPENVIBELY_SKILL_DIR", skillDir)
	return body
}

type skillSupportFileResponse struct {
	Handle   string `json:"handle"`
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func resolveSkillSupportFile(entry Entry, requested string) (string, error) {
	rel, err := cleanSupportFilePath(requested)
	if err != nil {
		return "", fmt.Errorf("skill_view: %w", err)
	}
	skillDir := filepath.Dir(entry.AbsolutePath)
	abs := filepath.Join(skillDir, rel)
	if err := ensureInsideSkillDir(skillDir, abs); err != nil {
		return "", fmt.Errorf("skill_view: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("skill_view: read support file %q: %w", rel, err)
	}
	encoded, err := json.MarshalIndent(skillSupportFileResponse{Handle: entry.Handle, FilePath: rel, Content: string(data)}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("skill_view: encode support file %q: %w", rel, err)
	}
	return string(encoded), nil
}

func listLinkedSupportFiles(skillDir string) map[string][]string {
	out := map[string][]string{}
	for _, kind := range []string{"references", "templates", "scripts", "assets"} {
		root := filepath.Join(skillDir, kind)
		var files []string
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if _, err := cleanSupportFilePath(kind + "/" + rel); err != nil {
				return nil
			}
			files = append(files, rel)
			return nil
		})
		if len(files) > 0 {
			sort.Strings(files)
			out[kind] = files
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cleanSupportFilePath(requested string) (string, error) {
	req := filepath.ToSlash(strings.TrimSpace(requested))
	if req == "" || strings.HasPrefix(req, "/") || strings.Contains(req, "\\") {
		return "", fmt.Errorf("invalid support file path %q", requested)
	}
	clean := filepath.Clean(req)
	clean = filepath.ToSlash(clean)
	if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("invalid support file path %q", requested)
	}
	parts := strings.Split(clean, "/")
	if len(parts) < 2 || !validSupportDir(parts[0]) {
		return "", fmt.Errorf("support file path %q must start with references/, templates/, scripts/, or assets/", requested)
	}
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("invalid support file path %q", requested)
		}
	}
	return clean, nil
}

func validSupportDir(kind string) bool {
	switch kind {
	case "references", "templates", "scripts", "assets":
		return true
	default:
		return false
	}
}

func ensureInsideSkillDir(skillDir, abs string) error {
	skillDirAbs, err := filepath.Abs(skillDir)
	if err != nil {
		return err
	}
	absResolved, err := filepath.Abs(abs)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(absResolved, skillDirAbs+string(filepath.Separator)) {
		return fmt.Errorf("support path escapes skill folder")
	}
	return nil
}

func resolveAgentList(ctx context.Context, inspector AgentInspector, input json.RawMessage) (string, error) {
	if len(input) > 0 {
		var params map[string]any
		if err := json.Unmarshal(input, &params); err != nil {
			return "", fmt.Errorf("agent_list: invalid input: %w", err)
		}
		if len(params) > 0 {
			return "", fmt.Errorf("agent_list: no parameters are supported")
		}
	}
	agents, err := inspector.ListAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("agent_list: %w", err)
	}
	if agents == nil {
		agents = []AgentSummary{}
	}
	b, err := json.MarshalIndent(agents, "", "  ")
	if err != nil {
		return "", fmt.Errorf("agent_list: encode: %w", err)
	}
	return string(b), nil
}

func resolveAgentView(ctx context.Context, inspector AgentInspector, input json.RawMessage) (string, error) {
	var params struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return "", fmt.Errorf("agent_view: invalid input: %w", err)
	}
	key := strings.TrimSpace(params.Key)
	if !agentKeyShape.MatchString(key) {
		return "", fmt.Errorf("agent_view: key %q is not a valid slug", key)
	}
	details, err := inspector.InspectAgent(ctx, key)
	if err != nil {
		return "", fmt.Errorf("agent_view: %w", err)
	}
	if details == nil {
		return "", fmt.Errorf("agent_view: agent %q not found", key)
	}
	b, err := json.MarshalIndent(details, "", "  ")
	if err != nil {
		return "", fmt.Errorf("agent_view: encode: %w", err)
	}
	return string(b), nil
}

var skillView = llmcontracts.RuntimeToolDefinition{
	Name:        "skill_view",
	Access:      llmcontracts.RuntimeToolAccessRead,
	Description: "Load an authorized skill for this turn. Use an available bare handle from the permitted skill index, or a qualified view handle such as standalone:<skill> or agent:<agent>/<skill> when library tools return one. With file_path, loads one support file under references/, templates/, scripts/, or assets/.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"handle": {
				Type:        "string",
				Description: "Selected bare handle or qualified view handle, e.g. debug_go_tests, standalone:debug_go_tests, or agent:reviewer/review_code",
			},
			"file_path": {
				Type:        "string",
				Description: "Optional support file path, e.g. references/common-failures.md or scripts/check.sh",
			},
		},
		Required:             []string{"handle"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var skillsList = llmcontracts.RuntimeToolDefinition{
	Name:        "skills_list",
	Description: "Return top-level skills/SKILLS.md contents plus canonical view_handle values. Optional 'scope' ('global'|'project'|'all', default 'all') filters which roots to include. Use the returned view_handle with skill_view when available.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"scope": {
				Type: "string",
				Enum: []any{"global", "project", "all"},
			},
		},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var agentList = llmcontracts.RuntimeToolDefinition{
	Name:        "agent_list",
	Description: "List enabled user-managed agents that may have maintainable agent-owned skills. Returns prompt-safe summaries; use agent_view with a returned key for details.",
	Schema: &toolschema.Schema{
		Type:                 "object",
		Properties:           map[string]*toolschema.Schema{},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

var agentView = llmcontracts.RuntimeToolDefinition{
	Name:        "agent_view",
	Description: "Inspect one assigned agent: prompt, permissions, tool grants, lifecycle hooks, and attached/manual skills. Accepts an agent key/slug.",
	Schema: &toolschema.Schema{
		Type: "object",
		Properties: map[string]*toolschema.Schema{
			"key": {
				Type:        "string",
				Description: "Agent key/slug to inspect",
			},
		},
		Required:             []string{"key"},
		AdditionalProperties: toolschema.Pointer(false),
	},
}

// InspectSelectedSkillRuntimeTools reads selected metadata without installing or running tools.
func InspectSelectedSkillRuntimeTools(catalog *Catalog) *llmcontracts.RuntimeTools {
	return skillRuntimeTools(catalog, "", "", nil, false, false)
}

// ExecutingSkillRuntimeTools also prepares dependencies when an executing agent
// explicitly loads an authorized skill from the library.
func ExecutingSkillRuntimeTools(catalog *Catalog, globalRoot, projectRoot string, inspector AgentInspector) *llmcontracts.RuntimeTools {
	return skillRuntimeTools(catalog, globalRoot, projectRoot, inspector, true, true)
}

func prepareSkillView(ctx context.Context, output string) (string, error) {
	var view skillViewResponse
	if err := json.Unmarshal([]byte(output), &view); err != nil {
		return "", err
	}
	// Support-file responses are reads, not another skill activation.
	if view.SkillDir == "" {
		return output, nil
	}
	deps, err := skilldeps.Parse([]byte(view.Body))
	if err != nil {
		return "", fmt.Errorf("skill dependencies: %w", err)
	}
	if len(deps) == 0 {
		return output, nil
	}
	manager, err := skilldeps.DefaultManager()
	if err != nil {
		return "", err
	}
	report := skilldeps.ProgressFromContext(ctx)
	manager.Progress = func(p skilldeps.Progress) {
		llmcontracts.NotifyActivity(ctx)
		if report != nil {
			report(p)
		}
	}
	view.Dependencies, err = manager.Resolve(ctx, deps)
	if err != nil {
		return "", err
	}
	view.Environment = map[string]string{}
	for _, dependency := range view.Dependencies {
		if dependency.Executable != "" && dependency.EnvironmentVariable != "" {
			view.Environment[dependency.EnvironmentVariable] = dependency.Executable
		}
	}
	encoded, err := json.MarshalIndent(view, "", "  ")
	return string(encoded), err
}
