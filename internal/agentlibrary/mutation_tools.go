package agentlibrary

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/openvibely/openvibely/pkg/knowledgecache"
	"strings"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/pkg/toolschema"
)

// MutationActor identifies who is invoking the mutation tools. Audit rows
// reference this for traceability.
type MutationActor struct {
	LifecycleExecutionID string
	TaskID               string
	TaskRunID            string
	ProjectID            string
	ActorAgentID         string
}

// MutationRecorder persists agent_config_mutations audit rows. The mutation
// tools always record both applied and blocked proposals so debugging can
// answer why an artifact did or did not change (runbook §Data Model line
// 2452 + §Backend Validation line 1773).
type MutationRecorder interface {
	Record(ctx context.Context, action string, target string, key string, payload []byte, result *ImportResult, blocked error) error
}

// MutationTools returns the request-scoped runtime tool for autonomous
// standalone skill maintenance. Agents are user-managed through the agent
// dialog, so this runtime surface intentionally exposes skill_manage only.
//
// Actor context (lifecycle execution, task, project, actor agent) should be
// baked into the recorder at construction time via NewRepoRecorder so every
// row this runtime tool inserts is correctly attributed.
func MutationTools(importer *Importer, recorder MutationRecorder) *llmcontracts.RuntimeTools {
	return SkillMutationTools(importer, recorder)
}

// AgentSkillMutationTools returns agent_skill_manage for after-complete learning
// hooks. The tool is server-scoped to the task's assigned agent; model input can
// choose only a skill key and action, never an arbitrary agent path.
func AgentSkillMutationTools(importer *Importer, recorder MutationRecorder, assignedAgentKey, scope string) *llmcontracts.RuntimeTools {
	if importer == nil || strings.TrimSpace(assignedAgentKey) == "" {
		return nil
	}
	return agentSkillMutationTools(importer, recorder, assignedAgentKey, scope, false)
}

// LibraryAgentSkillMutationTools returns agent_skill_manage for explicit skill
// library maintenance agents. The caller may target any non-protected agent via
// an `agent` field; RepoApplier protection blocks system/protected agents.
func LibraryAgentSkillMutationTools(importer *Importer, recorder MutationRecorder) *llmcontracts.RuntimeTools {
	if importer == nil {
		return nil
	}
	return agentSkillMutationTools(importer, recorder, "", "", true)
}

func agentSkillMutationTools(importer *Importer, recorder MutationRecorder, assignedAgentKey, scope string, allowExplicitAgent bool) *llmcontracts.RuntimeTools {
	exec := func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if strings.ToLower(strings.TrimSpace(name)) != "agent_skill_manage" {
			return "", false, false, nil
		}
		res, err := runAgentSkillManage(ctx, importer, recorder, assignedAgentKey, scope, allowExplicitAgent, input)
		if err == nil && importer != nil {
			if importer.roots.Global != "" {
				knowledgecache.Invalidate(importer.roots.Global)
			}
			if importer.roots.Project != "" {
				knowledgecache.Invalidate(importer.roots.Project)
			}
		}
		return marshalToolResult(res, err)
	}
	description := agentSkillManageDescription(assignedAgentKey)
	schema := agentSkillManageSchema
	if allowExplicitAgent {
		description = libraryAgentSkillManageDescription
		copy := *agentSkillManageSchema
		copy.Required = append(append([]string(nil), copy.Required...), "agent")
		schema = &copy
	}
	return &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{
			Name:        "agent_skill_manage",
			Description: description,
			Schema:      schema,
		}},
		Executor: exec,
		Filter: func(name string) (bool, bool) {
			if strings.ToLower(strings.TrimSpace(name)) == "agent_skill_manage" {
				return true, true
			}
			return false, false
		},
	}
}

// SkillMutationTools returns skill_manage for generated standalone skills.
func SkillMutationTools(importer *Importer, recorder MutationRecorder) *llmcontracts.RuntimeTools {
	if importer == nil {
		return nil
	}
	exec := func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "skill_manage":
			res, err := runSkillManage(ctx, importer, recorder, input)
			if err == nil && importer != nil {
				if importer.roots.Global != "" {
					knowledgecache.Invalidate(importer.roots.Global)
				}
				if importer.roots.Project != "" {
					knowledgecache.Invalidate(importer.roots.Project)
				}
			}
			return marshalToolResult(res, err)
		case "skill_import":
			res, err := runSkillImport(ctx, importer, recorder, input)
			if err == nil && importer != nil {
				if importer.roots.Global != "" {
					knowledgecache.Invalidate(importer.roots.Global)
				}
				if importer.roots.Project != "" {
					knowledgecache.Invalidate(importer.roots.Project)
				}
			}
			return marshalToolResult(res, err)
		default:
			return "", false, false, nil
		}
	}
	return &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{
				Name:        "skill_manage",
				Description: skillManageDescription,
				Schema:      skillManageSchema,
			},
			{
				Name:        "skill_import",
				Description: skillImportDescription,
				Schema:      skillImportSchema,
			},
		},
		Executor: exec,
		Filter: func(name string) (bool, bool) {
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "skill_manage", "skill_import":
				return true, true
			default:
				return false, false
			}
		},
	}
}

const skillDeclarationExample = "---\n" +
	"kind: openvibely.agent_skill\n" +
	"version: 1\n" +
	"skill:\n" +
	"  key: <skill_key>\n" +
	"  name: <Skill Name>\n" +
	"  scope: project\n" +
	"  description: <one-line purpose>\n" +
	"---\n" +
	"<instructions for this skill>"

const skillDeclarationFieldDoc = "Full skills/<skill>/SKILL.md content including YAML frontmatter delimited by '---' lines, " +
	"followed by the instructions for that one skill. " +
	"REQUIRED frontmatter fields: kind (must be 'openvibely.agent_skill'), version (integer >= 1), skill.key (lowercase slug, [a-z0-9_]+). " +
	"Do not set agent.key for standalone skills. Optional: skill.name, skill.scope (global|project), skill.description, routing. " + "Agent-level tools, permissions, lifecycle_hooks, routing defaults, model_defaults, and tool_config belong in the agent root SKILLS.md declaration, not in an individual SKILL.md unless intentionally patching legacy metadata. " +
	"Example:\n" + skillDeclarationExample

const indexUpdateReminder = " After a successful write, the mutation tool maintains the top-level skills/SKILLS.md skill-link index. " +
	"Use the global skills root for scope=global mutations and the project skills root for scope=project."
const skillManageDescription = "Create, patch, archive, write, or remove a support file for a standalone skill. " +
	"Actions: create (new skill), patch (update existing), write_file/remove_file (manage references/templates/scripts/assets), archive (deprecate). " +
	"For create/patch you MUST pass a complete standalone skills/<skill>/SKILL.md in the 'declaration' field (see field description for required frontmatter). " +
	"Path traversal, unknown support directories, and protected artifacts are rejected." + indexUpdateReminder

const skillImportDescription = "Import a standalone skill package into the project/global skill catalog from a local path or inline SKILL.md content. " +
	"Accepts a directory containing SKILL.md, a SKILL.md file path, or inline content. " +
	"The importer normalizes or generates OpenVibely YAML frontmatter with kind, version, skill.key/name/scope/description/enabled, writes skills/<skill>/SKILL.md, copies allowed support files, and updates skills/SKILLS.md."

func agentSkillManageDescription(agentKey string) string {
	return "Create, patch, or manage support files for skills owned by the task's assigned agent `" + agentKey + "`. " +
		"Use this only for learning specific to that assigned agent's role, workflow, or selected agent-owned skills. " +
		"Use skill_manage instead for reusable standalone/project/global skill learning. The backend scopes all writes to agents/" + agentKey + "/skills; do not pass agent paths."
}

const libraryAgentSkillManageDescription = "Create, patch, or manage support files for skills owned by user-managed agents. " +
	"Use this for skill library maintenance that consolidates or updates agent-specific skills. " +
	"Pass the target agent key in 'agent' and the bare skill key in 'handle'; protected agents are rejected by the backend. " +
	"Use skill_manage instead for standalone project/global skills."

var agentSkillManageSchema = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"action": {
			Type:        "string",
			Enum:        []any{"create", "patch", "write_file", "remove_file"},
			Description: "What to do. 'create' and 'patch' require 'declaration'. 'write_file' and 'remove_file' require 'handle' and 'support'.",
		},
		"agent": {
			Type:        "string",
			Description: "Target agent key for explicit library maintenance mode. Omit in assigned-agent learning mode, where the backend scopes this tool to the assigned agent.",
		},
		"scope": {
			Type:        "string",
			Enum:        []any{"global", "project"},
			Description: "Target root for explicit library maintenance mode. Omit in assigned-agent learning mode, where the backend supplies the assigned agent scope.",
		},
		"handle": {
			Type:        "string",
			Description: "Agent-owned skill key only, for example 'review_migrations'. Do not include an agent prefix.",
		},
		"declaration": {
			Type:        "string",
			Description: "REQUIRED for create/patch. Full agent-owned skills/<skill>/SKILL.md content including YAML frontmatter. Required fields: kind=openvibely.agent_skill, version>=1, skill.key. In explicit library maintenance mode, agent.key must either be omitted or match the target agent.",
		},
		"support": {
			Type:        "object",
			Description: "Required only for action='write_file' or action='remove_file'. Manages a file under references/, templates/, scripts/, or assets/ of the target agent-owned skill.",
			Properties: map[string]*toolschema.Schema{
				"kind": {
					Type: "string",
					Enum: []any{"references", "templates", "scripts", "assets"},
				},
				"path": {
					Type:        "string",
					Description: "Path relative to the chosen support directory, for example 'foo.md' or 'nested/foo.md'.",
				},
				"content_base64": {
					Type:        "string",
					Description: "Base64-encoded file content. Use this OR 'content'.",
				},
				"content": {
					Type:        "string",
					Description: "Plain-text file content. Use this OR 'content_base64'.",
				},
			},
		},
		"reason": {
			Type:        "string",
			Description: "Free-text reason. Recorded in the mutation audit log.",
		},
	},
	Required:             []string{"action"},
	AdditionalProperties: toolschema.Pointer(false),
}

var skillImportSchema = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"source_path": {
			Type:        "string",
			Description: "Local file or directory path to import. Directory sources must contain SKILL.md. File sources must be SKILL.md.",
		},
		"content": {
			Type:        "string",
			Description: "Inline SKILL.md content to import instead of source_path.",
		},
		"package_name": {
			Type:        "string",
			Description: "Optional package folder/name used to derive the skill handle when importing inline or standard-format skills.",
		},
		"scope": {
			Type:        "string",
			Enum:        []any{"global", "project"},
			Description: "Where to import the skill. Defaults to project.",
		},
		"files": {
			Type:        "array",
			Description: "Optional inline support files under references/, templates/, scripts/, or assets/.",
			Items: &toolschema.Schema{
				Type: "object",
				Properties: map[string]*toolschema.Schema{
					"path": {
						Type: "string"},
					"content": {
						Type: "string"},
					"content_base64": {
						Type: "string"},
				},
				Required:             []string{"path"},
				AdditionalProperties: toolschema.Pointer(false),
			},
		},
		"reason": {
			Type:        "string",
			Description: "Free-text reason. Recorded in the mutation audit log.",
		},
	},
	AdditionalProperties: toolschema.Pointer(false),
}

var skillManageSchema = &toolschema.Schema{
	Type: "object",
	Properties: map[string]*toolschema.Schema{
		"action": {
			Type:        "string",
			Enum:        []any{"create", "patch", "write_file", "remove_file", "archive"},
			Description: "What to do. 'create' and 'patch' require 'declaration'. 'write_file' and 'remove_file' require 'handle' and 'support'. 'archive' requires 'handle'.",
		},
		"handle": {
			Type:        "string",
			Description: "Standalone <skill_key> handle. Required for write_file, remove_file, and archive.",
		},
		"scope": {
			Type:        "string",
			Enum:        []any{"global", "project"},
			Description: "Where to write the skill. Defaults to the scope declared in the declaration's skill.scope or 'project'.",
		},
		"declaration": {
			Type:        "string",
			Description: "REQUIRED for create/patch. " + skillDeclarationFieldDoc,
		},
		"support": {
			Type:        "object",
			Description: "Required only for action='write_file' or action='remove_file'. Manages a file under references/, templates/, scripts/, or assets/ of the skill.",
			Properties: map[string]*toolschema.Schema{
				"kind": {
					Type: "string",
					Enum: []any{"references", "templates", "scripts", "assets"},
				},
				"path": {
					Type:        "string",
					Description: "Path relative to the chosen support directory, for example 'foo.md' or 'nested/foo.md'. Do not include references/, templates/, scripts/, or assets/.",
				},
				"content_base64": {
					Type:        "string",
					Description: "Base64-encoded file content. Use this OR 'content'.",
				},
				"content": {
					Type:        "string",
					Description: "Plain-text file content. Use this OR 'content_base64'.",
				},
			},
		},
		"absorbed_into": {
			Type:        "string",
			Description: "Optional. When archiving, the handle of the skill that supersedes this one.",
		},
		"reason": {
			Type:        "string",
			Description: "Free-text reason. Recorded in the mutation audit log.",
		},
	},
	Required:             []string{"action"},
	AdditionalProperties: toolschema.Pointer(false),
}

type agentSkillManageParams struct {
	Action      string `json:"action"`
	Agent       string `json:"agent"`
	Scope       string `json:"scope"`
	Handle      string `json:"handle"`
	Declaration string `json:"declaration"`
	Support     struct {
		Kind          string `json:"kind"`
		Path          string `json:"path"`
		ContentBase64 string `json:"content_base64"`
		Content       string `json:"content"`
	} `json:"support"`
	Reason string `json:"reason"`
}

type skillManageParams struct {
	Action      string `json:"action"`
	Handle      string `json:"handle"`
	Scope       string `json:"scope"`
	Declaration string `json:"declaration"`
	Support     struct {
		Kind          string `json:"kind"`
		Path          string `json:"path"`
		ContentBase64 string `json:"content_base64"`
		Content       string `json:"content"`
	} `json:"support"`
	AbsorbedInto string `json:"absorbed_into"`
	Reason       string `json:"reason"`
}

type skillImportParams struct {
	SourcePath  string `json:"source_path"`
	Content     string `json:"content"`
	PackageName string `json:"package_name"`
	Scope       string `json:"scope"`
	Files       []struct {
		Path          string `json:"path"`
		Content       string `json:"content"`
		ContentBase64 string `json:"content_base64"`
	} `json:"files"`
	Reason string `json:"reason"`
}

func runAgentSkillManage(ctx context.Context, importer *Importer, recorder MutationRecorder, assignedAgentKey, scope string, allowExplicitAgent bool, input json.RawMessage) (*ImportResult, error) {
	var p agentSkillManageParams
	if err := json.Unmarshal(input, &p); err != nil {
		return nil, fmt.Errorf("agent_skill_manage: invalid input: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	targetAgentKey := strings.TrimSpace(assignedAgentKey)
	targetScope := strings.TrimSpace(scope)
	if allowExplicitAgent {
		targetAgentKey = strings.TrimSpace(p.Agent)
		targetScope = strings.TrimSpace(p.Scope)
		if targetScope == "" {
			targetScope = "project"
		}
		if err := validateScopeValue(targetScope); err != nil {
			return blockedResult(ctx, recorder, action, "agent_skill", targetAgentKey, []byte(p.Declaration), fmt.Errorf("agent_skill_manage: %w", err))
		}
		if targetAgentKey == "" {
			return blockedResult(ctx, recorder, action, "agent_skill", targetAgentKey, []byte(p.Declaration), errors.New("agent_skill_manage: agent is required"))
		}
	} else if strings.TrimSpace(p.Agent) != "" && strings.TrimSpace(p.Agent) != targetAgentKey {
		return blockedResult(ctx, recorder, action, "agent_skill", strings.TrimSpace(p.Agent), []byte(p.Declaration), fmt.Errorf("agent_skill_manage is scoped to assigned agent %q", targetAgentKey))
	}
	mutationKey := targetAgentKey
	if p.Handle != "" {
		mutationKey += "/" + p.Handle
	}
	switch action {
	case "create", "patch":
		if p.Declaration == "" {
			return nil, errors.New("agent_skill_manage: declaration is required for create/patch")
		}
		decl, body, err := ParseDeclaration(p.Declaration)
		if err != nil {
			return blockedResult(ctx, recorder, action, "agent_skill", mutationKey, []byte(p.Declaration), err)
		}
		if decl.IsAgentRootDeclaration() {
			return blockedResult(ctx, recorder, action, "agent_skill", targetAgentKey, []byte(p.Declaration), errors.New("agent_skill_manage requires skill.key and cannot edit agent root declarations"))
		}
		if strings.TrimSpace(decl.Agent.Key) != "" && decl.Agent.Key != targetAgentKey {
			return blockedResult(ctx, recorder, action, "agent_skill", mutationKey, []byte(p.Declaration), fmt.Errorf("agent_skill_manage is scoped to agent %q", targetAgentKey))
		}
		if p.Handle != "" && p.Handle != decl.Handle() {
			return blockedResult(ctx, recorder, action, "agent_skill", mutationKey, []byte(p.Declaration), fmt.Errorf("handle mismatch: declaration is %s but handle is %s", decl.Handle(), p.Handle))
		}
		res, err := importer.WriteAgentOwnedSkill(ctx, targetScope, targetAgentKey, decl, body)
		recordMutation(ctx, recorder, action, "agent_skill", targetAgentKey+"/"+decl.Handle(), []byte(p.Declaration), res, err)
		return res, err
	case "write_file", "remove_file":
		if p.Handle == "" {
			return nil, fmt.Errorf("agent_skill_manage: handle is required for %s", action)
		}
		var (
			res   *ImportResult
			err   error
			bytes int
		)
		if action == "write_file" {
			content, decErr := decodeSupportContent(p.Support.Content, p.Support.ContentBase64)
			if decErr != nil {
				return nil, fmt.Errorf("agent_skill_manage: %w", decErr)
			}
			bytes = len(content)
			res, err = importer.WriteAgentOwnedSupportFile(ctx, targetScope, targetAgentKey, p.Handle, SupportFileKind(p.Support.Kind), p.Support.Path, content)
		} else {
			res, err = importer.RemoveAgentOwnedSupportFile(ctx, targetScope, targetAgentKey, p.Handle, SupportFileKind(p.Support.Kind), p.Support.Path)
		}
		payload, _ := json.Marshal(struct {
			Agent  string `json:"agent"`
			Handle string `json:"handle"`
			Kind   string `json:"kind"`
			Path   string `json:"path"`
			Bytes  int    `json:"bytes,omitempty"`
		}{targetAgentKey, p.Handle, p.Support.Kind, p.Support.Path, bytes})
		recordMutation(ctx, recorder, action, "agent_support_file", targetAgentKey+"/"+p.Handle, payload, res, err)
		return res, err
	default:
		return nil, fmt.Errorf("agent_skill_manage: unknown action %q", p.Action)
	}
}

func runSkillImport(ctx context.Context, importer *Importer, recorder MutationRecorder, input json.RawMessage) (*ImportResult, error) {
	var p skillImportParams
	if err := json.Unmarshal(input, &p); err != nil {
		return nil, fmt.Errorf("skill_import: invalid input: %w", err)
	}
	scope := strings.TrimSpace(p.Scope)
	if scope == "" {
		scope = "project"
	}
	if err := validateScopeValue(scope); err != nil {
		return blockedResult(ctx, recorder, "import", "skill", strings.TrimSpace(p.PackageName), input, fmt.Errorf("skill_import: %w", err))
	}
	content := p.Content
	packageName := strings.TrimSpace(p.PackageName)
	files := make([]SkillPackageFile, 0, len(p.Files))
	if strings.TrimSpace(p.SourcePath) != "" {
		if strings.TrimSpace(content) != "" || len(p.Files) > 0 {
			return blockedResult(ctx, recorder, "import", "skill", packageName, input, errors.New("skill_import: use either source_path or inline content/files, not both"))
		}
		var err error
		content, packageName, files, err = ReadSkillPackageFromPath(p.SourcePath)
		if err != nil {
			return blockedResult(ctx, recorder, "import", "skill", packageName, input, err)
		}
	} else {
		if strings.TrimSpace(content) == "" {
			return blockedResult(ctx, recorder, "import", "skill", packageName, input, errors.New("skill_import: source_path or content is required"))
		}
		for _, f := range p.Files {
			decoded, err := decodeOptionalSupportContent(f.Content, f.ContentBase64)
			if err != nil {
				return blockedResult(ctx, recorder, "import", "support_file", f.Path, input, fmt.Errorf("skill_import: %w", err))
			}
			files = append(files, SkillPackageFile{Path: f.Path, Content: decoded})
		}
	}
	decl, _, err := NormalizeStandaloneSkillPackage(content, packageName, scope)
	if err != nil {
		return blockedResult(ctx, recorder, "import", "skill", packageName, []byte(content), err)
	}
	recordPayload, _ := json.Marshal(struct {
		SourcePath  string `json:"source_path,omitempty"`
		PackageName string `json:"package_name,omitempty"`
		Scope       string `json:"scope"`
		Handle      string `json:"handle"`
		Files       int    `json:"files"`
		Reason      string `json:"reason,omitempty"`
	}{p.SourcePath, packageName, scope, decl.Handle(), len(files), p.Reason})
	res, err := importer.ImportSkillPackage(ctx, content, packageName, scope, files)
	recordMutation(ctx, recorder, "import", "skill", decl.Handle(), recordPayload, res, err)
	return res, err
}

func decodeOptionalSupportContent(plain, b64 string) ([]byte, error) {
	if b64 != "" || plain != "" {
		return decodeSupportContent(plain, b64)
	}
	return []byte{}, nil
}

func runSkillManage(ctx context.Context, importer *Importer, recorder MutationRecorder, input json.RawMessage) (*ImportResult, error) {
	var p skillManageParams
	if err := json.Unmarshal(input, &p); err != nil {
		return nil, fmt.Errorf("skill_manage: invalid input: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	switch action {
	case "create", "patch":
		if p.Declaration == "" {
			return nil, errors.New("skill_manage: declaration is required for create/patch")
		}
		decl, body, err := ParseDeclaration(p.Declaration)
		if err != nil {
			return blockedResult(ctx, recorder, action, "skill", "", []byte(p.Declaration), err)
		}
		if decl.IsAgentRootDeclaration() {
			return blockedResult(ctx, recorder, action, "skill", decl.Agent.Key, []byte(p.Declaration), errors.New("skill_manage requires skill.key; agents are managed in the agent dialog"))
		}
		if strings.TrimSpace(decl.Agent.Key) != "" {
			return blockedResult(ctx, recorder, action, "skill", decl.Handle(), []byte(p.Declaration), errors.New("skill_manage creates standalone skills; omit agent.key"))
		}
		if p.Handle != "" && p.Handle != decl.Handle() {
			return blockedResult(ctx, recorder, action, "skill", p.Handle, []byte(p.Declaration), fmt.Errorf("handle mismatch: declaration is %s but handle is %s", decl.Handle(), p.Handle))
		}
		if p.Scope != "" {
			decl.Skill.Scope = p.Scope
		}
		if err := validateMutationScope(decl); err != nil {
			return blockedResult(ctx, recorder, action, "skill", decl.Handle(), []byte(p.Declaration), err)
		}
		res, err := importer.WriteSkill(ctx, decl, body)
		recordMutation(ctx, recorder, action, "skill", decl.Handle(), []byte(p.Declaration), res, err)
		return res, err
	case "write_file", "remove_file":
		if p.Handle == "" {
			return nil, fmt.Errorf("skill_manage: handle is required for %s", action)
		}
		scope := p.Scope
		if scope == "" {
			scope = "project"
		}
		if err := validateScopeValue(scope); err != nil {
			return blockedResult(ctx, recorder, action, "support_file", p.Handle, nil, err)
		}
		var (
			res   *ImportResult
			err   error
			bytes int
		)
		if action == "write_file" {
			content, decErr := decodeSupportContent(p.Support.Content, p.Support.ContentBase64)
			if decErr != nil {
				return nil, fmt.Errorf("skill_manage: %w", decErr)
			}
			bytes = len(content)
			res, err = importer.WriteSupportFile(ctx, scope, p.Handle, SupportFileKind(p.Support.Kind), p.Support.Path, content)
		} else {
			res, err = importer.RemoveSupportFile(ctx, scope, p.Handle, SupportFileKind(p.Support.Kind), p.Support.Path)
		}
		payload, _ := json.Marshal(struct {
			Handle string `json:"handle"`
			Kind   string `json:"kind"`
			Path   string `json:"path"`
			Bytes  int    `json:"bytes,omitempty"`
		}{p.Handle, p.Support.Kind, p.Support.Path, bytes})
		recordMutation(ctx, recorder, action, "support_file", p.Handle, payload, res, err)
		return res, err
	case "archive":
		if p.Handle == "" {
			return nil, errors.New("skill_manage: handle is required for archive")
		}
		res, err := importer.ArchiveSkill(ctx, p.Handle, p.AbsorbedInto, p.Reason)
		payload, _ := json.Marshal(struct {
			Handle       string `json:"handle"`
			AbsorbedInto string `json:"absorbed_into,omitempty"`
			Reason       string `json:"reason,omitempty"`
		}{p.Handle, p.AbsorbedInto, p.Reason})
		recordMutation(ctx, recorder, action, "skill", p.Handle, payload, res, err)
		return res, err
	default:
		return nil, fmt.Errorf("skill_manage: unknown action %q", p.Action)
	}
}

// validateMutationScope normalizes empty scopes to "project" and rejects any
// value other than "global" or "project". Both supported scopes are allowed;
// the importer maps them to the configured global and project skill roots.
func validateMutationScope(decl *SkillDeclaration) error {
	if decl == nil {
		return errors.New("declaration is required")
	}
	if err := validateScopeValue(decl.Agent.Scope); err != nil {
		return fmt.Errorf("agent.%w", err)
	}
	if !decl.IsAgentRootDeclaration() {
		if err := validateScopeValue(decl.Skill.Scope); err != nil {
			return fmt.Errorf("skill.%w", err)
		}
	}
	if strings.TrimSpace(decl.Agent.Scope) == "" {
		decl.Agent.Scope = "project"
	}
	if !decl.IsAgentRootDeclaration() && strings.TrimSpace(decl.Skill.Scope) == "" {
		decl.Skill.Scope = "project"
	}
	return nil
}

func validateScopeValue(scope string) error {
	switch strings.TrimSpace(scope) {
	case "", "global", "project":
		return nil
	default:
		return fmt.Errorf("scope must be global or project, got %q", scope)
	}
}

func decodeSupportContent(plain, b64 string) ([]byte, error) {
	if b64 != "" {
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("support content_base64: %w", err)
		}
		return raw, nil
	}
	if plain != "" {
		return []byte(plain), nil
	}
	return nil, errors.New("support content is empty")
}

func blockedResult(ctx context.Context, recorder MutationRecorder, action, target, key string, payload []byte, cause error) (*ImportResult, error) {
	res := &ImportResult{Applied: false, Blocked: []string{key}}
	recordMutation(ctx, recorder, action, target, key, payload, res, cause)
	return res, cause
}

func recordMutation(ctx context.Context, recorder MutationRecorder, action, target, key string, payload []byte, result *ImportResult, cause error) {
	if recorder == nil {
		return
	}
	_ = recorder.Record(ctx, action, target, key, payload, result, cause)
}

func marshalToolResult(res *ImportResult, err error) (string, bool, bool, error) {
	if err != nil && res == nil {
		return err.Error(), true, true, nil
	}
	if res == nil {
		res = &ImportResult{}
	}
	if err != nil {
		// Surface the cause inline; result.Applied=false already conveys block.
		merged := struct {
			*ImportResult
			Error string `json:"error,omitempty"`
		}{ImportResult: res, Error: err.Error()}
		raw, _ := json.Marshal(merged)
		return string(raw), true, true, nil
	}
	raw, mErr := json.Marshal(res)
	if mErr != nil {
		return mErr.Error(), true, true, nil
	}
	return string(raw), true, false, nil
}
