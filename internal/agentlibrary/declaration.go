// Package agentlibrary owns the *write* side of the agent/skill library:
// parsing the rich SKILL.md frontmatter declared by the runbook §Markdown
// Skills And Active Frontmatter, validating the declaration, and applying it
// through the importer.
//
// The read-only side (catalog, skill_view, skills_list, agent_view) lives in
// internal/agentskills. The two packages share the
// filesystem layout `<root>/skills/<skill>/SKILL.md` for standalone generated
// skills. Agent-owned implementation skills may still live under agent roots,
// but skill_manage operates on the standalone catalog.
package agentlibrary

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// SkillDeclaration is the typed representation of a SKILL.md frontmatter block
// per the runbook's example at §Markdown Skills And Active Frontmatter
// (line 1026). The same struct is consumed by both the importer and the
// skill_manage mutation tool and agent declaration import path.
type SkillDeclaration struct {
	Kind           string              `yaml:"kind"`
	Version        int                 `yaml:"version"`
	Agent          AgentDeclaration    `yaml:"agent"`
	Skill          SkillBlock          `yaml:"skill"`
	Routing        RoutingBlock        `yaml:"routing"`
	Tools          []string            `yaml:"tools"`
	ToolConfig     ToolConfigBlock     `yaml:"tool_config"`
	Plugins        []string            `yaml:"plugins"`
	MCPServers     []string            `yaml:"mcp_servers"`
	Permissions    PermissionsBlock    `yaml:"permissions"`
	ModelDefaults  ModelDefaultsBlock  `yaml:"model_defaults"`
	LifecycleHooks map[string]HookDecl `yaml:"lifecycle_hooks"`
	EvidenceRefs   []string            `yaml:"evidence_refs"`
}

// AgentDeclaration captures the agent identity fields that may be set through
// SKILL.md frontmatter. Generated agents may declare all of these; user-edited
// agents must preserve user intent (the importer applies only the supplied
// subset).
type AgentDeclaration struct {
	Key                 string `yaml:"key"`
	Name                string `yaml:"name"`
	DisplayName         string `yaml:"display_name"`
	Description         string `yaml:"description"`
	Enabled             *bool  `yaml:"enabled"`
	PrimaryAgentEnabled *bool  `yaml:"primary_agent_enabled"`
	SelectableAsPrimary bool   `yaml:"selectable_as_primary"`
	Scope               string `yaml:"scope"` // global | project
	ProjectID           string `yaml:"project_id"`
	SystemPrompt        string `yaml:"system_prompt"`
}

// SkillBlock identifies the skill the declaration declares.
type SkillBlock struct {
	Key           string `yaml:"key"`
	Name          string `yaml:"name"`
	Scope         string `yaml:"scope"` // global | project
	Enabled       *bool  `yaml:"enabled"`
	Description   string `yaml:"description"`
	Archived      bool   `yaml:"archived"`
	AbsorbedInto  string `yaml:"absorbed_into"`
	ArchiveReason string `yaml:"archive_reason"`
}

// RoutingBlock holds standalone skill selection metadata. It is not used for
// automatic agent selection; agents are assigned manually/defaulted.
type RoutingBlock struct {
	Triggers    []string `yaml:"triggers"`
	Priority    int      `yaml:"priority"`
	Description string   `yaml:"description"`
}

// ToolConfigBlock stores structured configuration for parameterized agent tools.
// Today this is primarily used by the ScopedFiles runtime tool, but it is kept
// separate from the flat `tools` grant list so declarations can say both "this
// agent may use ScopedFiles" and "these are the scoped roots/permissions".
type ToolConfigBlock struct {
	ScopedFiles            []ScopedFilesConfigBlock `yaml:"scoped_files"`
	SkipDefaultTools       bool                     `yaml:"skip_default_tools"`
	DisableRuntimeWorktree bool                     `yaml:"disable_runtime_worktree"`
}

type ScopedFilesConfigBlock struct {
	Directory   string   `yaml:"directory"`
	Permissions []string `yaml:"permissions"`
}

// PermissionsBlock mirrors the agent permissions enumeration in §Permissions
// Tab (lines 2253-2266). The importer copies these into the agent's defaults
// and each declared lifecycle hook's `permissions_json`.
type PermissionsBlock struct {
	ReadTaskPrompt       bool `yaml:"read_task_prompt"`
	ReadTaskExecution    bool `yaml:"read_task_execution"`
	ReadProjectMemory    bool `yaml:"read_project_memory"`
	WriteProjectMemory   bool `yaml:"write_project_memory"`
	ReadAgents           bool `yaml:"read_agents"`
	WriteAgents          bool `yaml:"write_agents"`
	ReadSkills           bool `yaml:"read_skills"`
	WriteSkills          bool `yaml:"write_skills"`
	ReadRepositoryFiles  bool `yaml:"read_repository_files"`
	WriteRepositoryFiles bool `yaml:"write_repository_files"`
	UseShellOrTools      bool `yaml:"use_shell_or_tools"`
}

// ModelDefaultsBlock holds optional model/temperature/max-token defaults.
type ModelDefaultsBlock struct {
	Model       string  `yaml:"model"`
	Temperature float64 `yaml:"temperature"`
	MaxTokens   int     `yaml:"max_tokens"`
}

// HookDecl declares one lifecycle hook on the owning agent. Each map key in
// LifecycleHooks names the `when` slot (route_task | before_run | after_complete
// | scheduled), or `primary` for the agent's primary-mode flag.
type HookDecl struct {
	Enabled        *bool           `yaml:"enabled"`
	Skill          string          `yaml:"skill"`
	Blocking       bool            `yaml:"blocking"`
	OutputContract string          `yaml:"output_contract"`
	PromptOverride string          `yaml:"prompt_override"`
	ScheduleCron   string          `yaml:"schedule_cron"`
	Schedule       string          `yaml:"schedule"`
	RunPolicy      string          `yaml:"run_policy"`
	Permissions    map[string]bool `yaml:"permissions"`
	// Payload names the lifecycle context blocks this hook's skill reads, for
	// example conversation_transcript or task_goal. Omit it to receive every
	// block the slot produced; declare it to keep unread context out of the
	// hook's prompt.
	Payload []string `yaml:"payload"`
}

// SplitFrontmatter returns the YAML frontmatter block and the remaining body.
// A missing or malformed block returns ("", content, false).
func SplitFrontmatter(content string) (frontmatter, body string, ok bool) {
	if !strings.HasPrefix(content, "---") {
		return "", content, false
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimPrefix(rest, "\r")
	rest = strings.TrimPrefix(rest, "\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", content, false
	}
	header := rest[:end]
	body = rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\r")
	body = strings.TrimPrefix(body, "\n")
	return header, body, true
}

// ParseDeclaration unmarshals the YAML frontmatter and runs minimal shape
// checks. Field-level policy/permission validation happens in the importer
// (see Importer.Apply) so callers see one consistent error path.
func ParseDeclaration(content string) (*SkillDeclaration, string, error) {
	front, body, ok := SplitFrontmatter(content)
	if !ok {
		return nil, content, errors.New("declaration: missing or malformed frontmatter")
	}
	var decl SkillDeclaration
	if err := yaml.Unmarshal([]byte(front), &decl); err != nil {
		return nil, body, fmt.Errorf("declaration: invalid YAML: %w", err)
	}
	if err := decl.Validate(); err != nil {
		return &decl, body, err
	}
	return &decl, body, nil
}

// Validate runs the runbook's basic shape rules. The importer adds further
// policy checks (protected artifacts, available tools/plugins, etc.).
func (d *SkillDeclaration) Validate() error {
	if d == nil {
		return errors.New("declaration: nil")
	}
	if strings.TrimSpace(d.Kind) != "openvibely.agent_skill" {
		return fmt.Errorf("declaration: kind must be openvibely.agent_skill, got %q", d.Kind)
	}
	if d.Version <= 0 {
		return errors.New("declaration: version must be >= 1")
	}
	if strings.TrimSpace(d.Agent.Key) != "" && !isSlug(d.Agent.Key) {
		return fmt.Errorf("declaration: agent.key %q is not a valid slug", d.Agent.Key)
	}
	if d.IsAgentRootDeclaration() && !isSlug(d.Agent.Key) {
		return fmt.Errorf("declaration: agent.key %q is required for agent root declarations", d.Agent.Key)
	}
	if strings.TrimSpace(d.Skill.Key) != "" && !isSlug(d.Skill.Key) {
		return fmt.Errorf("declaration: skill.key %q is not a valid slug", d.Skill.Key)
	}
	switch d.Skill.Scope {
	case "", "global", "project":
	default:
		return fmt.Errorf("declaration: skill.scope must be global or project, got %q", d.Skill.Scope)
	}
	for when, hook := range d.LifecycleHooks {
		switch when {
		case "primary", "route_task", "before_run", "after_complete", "scheduled":
		default:
			return fmt.Errorf("declaration: unknown lifecycle hook slot %q", when)
		}
		if when == "primary" {
			continue
		}
		switch hook.OutputContract {
		case "", "selected_mode", "selected_skills", "selected_memories", "context_block", "activity_summary", "learning_summary", "library_update_summary":
		default:
			return fmt.Errorf("declaration: hook %s has unknown output_contract %q", when, hook.OutputContract)
		}
		if when == "scheduled" && hook.ScheduleCron == "" {
			// runbook does not strictly require cron here (Schedule tab can configure
			// interval too) but a scheduled hook with no policy and no cron is a
			// configuration smell. We accept it; the schedule editor enforces details.
		}
	}
	return nil
}

func isSlug(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '-' || s[0] == '_' || s[0] == '.' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}

// Handle returns the canonical standalone skill handle for skill declarations.
func (d *SkillDeclaration) Handle() string {
	if d == nil || strings.TrimSpace(d.Skill.Key) == "" {
		return ""
	}
	return d.Skill.Key
}

func (d *SkillDeclaration) IsAgentRootDeclaration() bool {
	return d != nil && strings.TrimSpace(d.Skill.Key) == ""
}

// AgentDisplayName returns the agent's display label, falling back to Name.
// Generated agent declarations may set either field; the importer should pick
// whichever is non-empty.
func (a *AgentDeclaration) AgentDisplayName() string {
	if a == nil {
		return ""
	}
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.Name
}
