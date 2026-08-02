package agentlibrary

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// AgentStore is the subset of repository.AgentRepo the production Applier
// needs. Extracted so the applier can be unit-tested without a real database.
type AgentStore interface {
	GetByKey(ctx context.Context, key string) (*models.Agent, error)
	Create(ctx context.Context, a *models.Agent) error
	Update(ctx context.Context, a *models.Agent) error
	MarkArchived(ctx context.Context, id, absorbedInto, reason string) error
}

type agentStoreIncludingArchived interface {
	GetByKeyIncludingArchived(ctx context.Context, key string) (*models.Agent, error)
}

// HookStore is the subset of repository.LifecycleRepo the production Applier
// needs. It writes the agent_lifecycle_hooks rows declared by the importer.
type HookStore interface {
	HooksByAgent(ctx context.Context, agentID string) ([]models.AgentLifecycleHook, error)
	CreateHook(ctx context.Context, h *models.AgentLifecycleHook) error
	UpdateHook(ctx context.Context, h *models.AgentLifecycleHook) error
	DeleteHook(ctx context.Context, id string) error
}

// RepoApplier persists imported declarations through the durable agent/hook
// repos. Protection enforcement is keyed off the agent's `generated_status`
// (runbook §Backend Validation line 1780).
type RepoApplier struct {
	Agents AgentStore
	Hooks  HookStore
}

// NewRepoApplier wires the agent and hook stores. Either may be nil if the
// caller only wants partial behavior (for example, skill-only deployments).
func NewRepoApplier(agents AgentStore, hooks HookStore) *RepoApplier {
	return &RepoApplier{Agents: agents, Hooks: hooks}
}

// ApplyDeclaration upserts the agent and its declared lifecycle hooks. Returns
// the list of human-readable change descriptions for audit (consumed by the
// mutation_tools result/`imported_config_changes`).
func (a *RepoApplier) ApplyDeclaration(ctx context.Context, decl *SkillDeclaration) ([]string, error) {
	if a == nil {
		return nil, errors.New("applier: nil")
	}
	if decl == nil {
		return nil, errors.New("applier: nil declaration")
	}
	if a.Agents == nil {
		return nil, errors.New("applier: AgentStore not configured")
	}
	if err := decl.Validate(); err != nil {
		return nil, err
	}

	existing, err := a.Agents.GetByKey(ctx, decl.Agent.Key)
	if err != nil {
		return nil, fmt.Errorf("lookup agent %q: %w", decl.Agent.Key, err)
	}
	if existing != nil && existing.GeneratedStatus == models.AgentStatusProtected {
		return nil, fmt.Errorf("agent %q is protected", decl.Agent.Key)
	}

	changes := []string{}
	wantHooks := importHooks(decl)
	target := agentFromDeclaration(decl, existing)

	if existing == nil {
		target.CreatedBy = models.AgentCreatedByAgent
		if target.GeneratedStatus == "" {
			target.GeneratedStatus = models.AgentStatusGenerated
		}
		if err := a.Agents.Create(ctx, target); err != nil {
			return nil, fmt.Errorf("create agent: %w", err)
		}
		changes = append(changes, "agent:create:"+target.Key)
	} else {
		target.ID = existing.ID
		// Preserve original CreatedBy unless the existing record was generated
		// by an agent and the user has not since taken ownership.
		target.CreatedBy = existing.CreatedBy
		if target.CreatedBy == "" {
			target.CreatedBy = models.AgentCreatedByAgent
		}
		if existing.GeneratedStatus == models.AgentStatusGenerated {
			target.GeneratedStatus = models.AgentStatusGenerated
		} else if existing.GeneratedStatus != "" {
			target.GeneratedStatus = existing.GeneratedStatus
		}
		target.CreatedAt = existing.CreatedAt
		if !agentConfigurationEqual(existing, target) {
			if err := a.Agents.Update(ctx, target); err != nil {
				return nil, fmt.Errorf("update agent: %w", err)
			}
			changes = append(changes, "agent:update:"+target.Key)
		}
	}

	// Hooks are diffed by (when, skill_key). Existing hooks with a matching
	// pair are updated; declared hooks without a counterpart are inserted.
	// Existing hooks no longer declared are left alone — autonomous edits
	// should not silently archive hooks the user may have configured.
	if a.Hooks != nil && len(wantHooks) > 0 {
		existingHooks, err := a.Hooks.HooksByAgent(ctx, target.ID)
		if err != nil {
			return nil, fmt.Errorf("list hooks: %w", err)
		}
		for _, want := range wantHooks {
			want.AgentID = target.ID
			match := findHookByWhenAndSkill(existingHooks, want.When, want.SkillKey)
			if match == nil {
				if err := a.Hooks.CreateHook(ctx, &want); err != nil {
					return nil, fmt.Errorf("create hook %s/%s: %w", want.When, want.SkillKey, err)
				}
				changes = append(changes, fmt.Sprintf("hook:create:%s:%s", want.When, want.SkillKey))
			} else {
				want.ID = match.ID
				if hookConfigurationEqual(match, &want) {
					continue
				}
				if err := a.Hooks.UpdateHook(ctx, &want); err != nil {
					return nil, fmt.Errorf("update hook %s/%s: %w", want.When, want.SkillKey, err)
				}
				changes = append(changes, fmt.Sprintf("hook:update:%s:%s", want.When, want.SkillKey))
			}
		}
	}

	return changes, nil
}

func agentConfigurationEqual(existing, target *models.Agent) bool {
	if existing == nil || target == nil {
		return existing == target
	}
	left, right := *existing, *target
	left.ID, right.ID = "", ""
	left.CreatedAt, right.CreatedAt = right.CreatedAt, right.CreatedAt
	left.UpdatedAt, right.UpdatedAt = right.UpdatedAt, right.UpdatedAt
	normalizeEmptyAgentSlices(&left)
	normalizeEmptyAgentSlices(&right)
	return reflect.DeepEqual(left, right)
}

func normalizeEmptyAgentSlices(agent *models.Agent) {
	if len(agent.Tools) == 0 {
		agent.Tools = nil
	}
	if len(agent.ToolConfig.ScopedFiles) == 0 {
		agent.ToolConfig.ScopedFiles = nil
	}
	if len(agent.Plugins) == 0 {
		agent.Plugins = nil
	}
	if len(agent.MCPServers) == 0 {
		agent.MCPServers = nil
	}
	if len(agent.Skills) == 0 {
		agent.Skills = nil
	}
	if len(agent.SourceRefs) == 0 {
		agent.SourceRefs = nil
	}
}

func hookConfigurationEqual(existing, target *models.AgentLifecycleHook) bool {
	if existing == nil || target == nil {
		return existing == target
	}
	left, right := *existing, *target
	left.ID, right.ID = "", ""
	left.AgentID, right.AgentID = "", ""
	left.CreatedAt, right.CreatedAt = right.CreatedAt, right.CreatedAt
	left.UpdatedAt, right.UpdatedAt = right.UpdatedAt, right.UpdatedAt
	return reflect.DeepEqual(left, right)
}

// ArchiveAgent flips the agent's generated_status to archived. Protected
// agents cannot be archived autonomously.
func (a *RepoApplier) ArchiveAgent(ctx context.Context, agentKey, absorbedInto, reason string) error {
	if a == nil || a.Agents == nil {
		return errors.New("applier: AgentStore not configured")
	}
	existing, err := a.Agents.GetByKey(ctx, agentKey)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("agent %q not found", agentKey)
	}
	if existing.GeneratedStatus == models.AgentStatusProtected {
		return fmt.Errorf("agent %q is protected", agentKey)
	}
	return a.Agents.MarkArchived(ctx, existing.ID, absorbedInto, reason)
}

// ArchiveSkill is retained for the importer interface. Standalone generated
// skills are represented by files and mutation audit rows, not embedded agent DB
// skill configs, so archiving a skill requires no agent-row mutation.
func (a *RepoApplier) ArchiveSkill(ctx context.Context, handle, absorbedInto, reason string) error {
	_, _, err := SplitHandle(handle)
	return err
}

// IsProtected reports whether the target is protected from autonomous edits.
// Skills inherit their agent's protection state.
func isBuiltInSystemAgentKey(key string) bool {
	switch strings.TrimSpace(key) {
	case models.AgentSystemKindSkillCurator, models.AgentSystemKindMemoryCurator, models.AgentSystemKindGoal:
		return true
	default:
		return false
	}
}

func (a *RepoApplier) IsProtected(ctx context.Context, targetType, key string) (bool, string, error) {
	if a == nil || a.Agents == nil {
		return false, "", nil
	}
	var agentKey string
	agentOwnedSkill := false
	switch targetType {
	case "agent":
		agentKey = key
	case "skill":
		parts := strings.Split(strings.TrimSpace(key), "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			// Standalone generated skills do not inherit protection from an agent.
			return false, "", nil
		}
		agentKey = parts[0]
		agentOwnedSkill = true
	default:
		return false, "", nil
	}
	existing, err := a.Agents.GetByKey(ctx, agentKey)
	if agentOwnedSkill {
		if archivedStore, ok := a.Agents.(agentStoreIncludingArchived); ok {
			existing, err = archivedStore.GetByKeyIncludingArchived(ctx, agentKey)
		}
	}
	if err != nil {
		return false, "", err
	}
	if existing == nil {
		if isBuiltInSystemAgentKey(agentKey) {
			return true, "agent " + agentKey + " is protected", nil
		}
		return false, "", nil
	}
	if existing.GeneratedStatus == models.AgentStatusProtected || isBuiltInSystemAgentKey(agentKey) {
		return true, "agent " + agentKey + " is protected", nil
	}
	if agentOwnedSkill {
		if !existing.Enabled {
			return true, "agent " + agentKey + " is disabled", nil
		}
		if existing.GeneratedStatus == models.AgentStatusArchived || existing.ArchivedAt != nil {
			return true, "agent " + agentKey + " is archived", nil
		}
	}
	return false, "", nil
}

// agentFromDeclaration produces the persisted agent struct for upsert. The
// existing record (if any) is used to fill fields the declaration does not
// override.
func agentFromDeclaration(decl *SkillDeclaration, existing *models.Agent) *models.Agent {
	if !decl.IsAgentRootDeclaration() {
		return agentFromSkillDeclaration(decl, existing)
	}
	// Respect the declaration's enabled field. When absent (nil), default to
	// true so legacy declarations that never set the field stay enabled.
	enabled := true
	if decl.Agent.Enabled != nil {
		enabled = *decl.Agent.Enabled
	}
	a := &models.Agent{
		Name:                decl.Agent.AgentDisplayName(),
		Description:         decl.Agent.Description,
		SystemPrompt:        decl.Agent.SystemPrompt,
		Model:               firstNonEmpty(decl.ModelDefaults.Model, "inherit"),
		Tools:               nonNilStrings(decl.Tools),
		ToolConfig:          toolConfigFromDecl(decl.ToolConfig),
		Plugins:             nonNilStrings(decl.Plugins),
		MCPServers:          mcpFromStrings(decl.MCPServers),
		Skills:              skillFromDeclaration(decl, existing),
		Key:                 decl.Agent.Key,
		Scope:               models.AgentScope(firstNonEmpty(decl.Agent.Scope, "global")),
		ProjectID:           decl.Agent.ProjectID,
		SelectableAsPrimary: decl.Agent.SelectableAsPrimary,
		Enabled:             enabled,
		PermissionDefaults:  permissionDefaultsFromDecl(decl),
		ModelDefaults: models.AgentModelDefaults{
			Model:       decl.ModelDefaults.Model,
			Temperature: decl.ModelDefaults.Temperature,
			MaxTokens:   decl.ModelDefaults.MaxTokens,
		},
		SourceRefs: append([]string(nil), decl.EvidenceRefs...),
	}
	if existing != nil {
		if a.SystemPrompt == "" {
			a.SystemPrompt = existing.SystemPrompt
		}
		if a.Description == "" {
			a.Description = existing.Description
		}
		if a.Model == "inherit" && existing.Model != "" {
			a.Model = existing.Model
		}
		if len(decl.Tools) == 0 {
			a.Tools = append([]string(nil), existing.Tools...)
		}
		if len(decl.Plugins) == 0 {
			a.Plugins = append([]string(nil), existing.Plugins...)
		}
		if len(decl.MCPServers) == 0 {
			a.MCPServers = append([]models.MCPServerConfig(nil), existing.MCPServers...)
		}
		if emptyToolConfigBlock(decl.ToolConfig) {
			a.ToolConfig = existing.ToolConfig
		}
		if decl.Permissions == (PermissionsBlock{}) {
			a.PermissionDefaults = existing.PermissionDefaults
		}
		if decl.ModelDefaults == (ModelDefaultsBlock{}) {
			a.ModelDefaults = existing.ModelDefaults
		}
		if len(decl.EvidenceRefs) == 0 {
			a.SourceRefs = append([]string(nil), existing.SourceRefs...)
		}
		// Preserve archived flag explicitly; the importer should not
		// resurrect archived rows by writing without an Update path.
		if existing.GeneratedStatus == models.AgentStatusArchived {
			a.Enabled = false
		}
	}
	return a
}

func agentFromSkillDeclaration(decl *SkillDeclaration, existing *models.Agent) *models.Agent {
	if existing != nil {
		cp := *existing
		cp.Skills = mergeDeclaredSkill(existing.Skills, decl)
		return &cp
	}
	return &models.Agent{
		Name:                firstNonEmpty(decl.Agent.AgentDisplayName(), decl.Agent.Key),
		Description:         decl.Agent.Description,
		SystemPrompt:        decl.Agent.SystemPrompt,
		Model:               "inherit",
		Tools:               []string{},
		ToolConfig:          models.AgentToolConfig{},
		Plugins:             []string{},
		MCPServers:          []models.MCPServerConfig{},
		Skills:              mergeDeclaredSkill(nil, decl),
		Key:                 decl.Agent.Key,
		Scope:               models.AgentScope(firstNonEmpty(decl.Agent.Scope, "global")),
		ProjectID:           decl.Agent.ProjectID,
		SelectableAsPrimary: decl.Agent.SelectableAsPrimary,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusGenerated,
		CreatedBy:           models.AgentCreatedByAgent,
	}
}

func importHooks(decl *SkillDeclaration) []models.AgentLifecycleHook {
	if decl == nil || !decl.IsAgentRootDeclaration() {
		return nil
	}
	out := make([]models.AgentLifecycleHook, 0, len(decl.LifecycleHooks))
	for when, h := range decl.LifecycleHooks {
		permJSON, _ := json.Marshal(h.Permissions)
		runJSON, _ := json.Marshal(map[string]any{"when": firstNonEmpty(h.RunPolicy, "always")})
		var scheduleJSON string
		cron := firstNonEmpty(h.ScheduleCron, h.Schedule)
		if cron != "" {
			b, _ := json.Marshal(map[string]string{"cron": cron})
			scheduleJSON = string(b)
		}
		enabled := true
		if h.Enabled != nil {
			enabled = *h.Enabled
		}
		out = append(out, models.AgentLifecycleHook{
			When:            models.LifecycleWhen(when),
			SkillKey:        firstNonEmpty(h.Skill, decl.Skill.Key),
			PromptOverride:  h.PromptOverride,
			OutputContract:  models.LifecycleOutputContract(h.OutputContract),
			Blocking:        h.Blocking,
			Enabled:         enabled,
			PermissionsJSON: string(permJSON),
			RunPolicyJSON:   string(runJSON),
			ScheduleJSON:    scheduleJSON,
		})
	}
	return out
}

func findHookByWhenAndSkill(hooks []models.AgentLifecycleHook, when models.LifecycleWhen, skill string) *models.AgentLifecycleHook {
	for i := range hooks {
		if hooks[i].When == when && hooks[i].SkillKey == skill {
			return &hooks[i]
		}
	}
	return nil
}

func skillFromDeclaration(decl *SkillDeclaration, existing *models.Agent) []models.SkillConfig {
	if existing != nil {
		return append([]models.SkillConfig(nil), existing.Skills...)
	}
	return []models.SkillConfig{}
}

func mergeDeclaredSkill(existing []models.SkillConfig, decl *SkillDeclaration) []models.SkillConfig {
	if decl == nil || decl.Skill.Key == "" {
		return append([]models.SkillConfig(nil), existing...)
	}
	next := models.SkillConfig{
		Name:        decl.Skill.Key,
		Description: decl.Skill.Description,
		Tools:       strings.Join(decl.Tools, ","),
	}
	out := append([]models.SkillConfig(nil), existing...)
	for i := range out {
		if strings.EqualFold(out[i].Name, decl.Skill.Key) {
			if next.Description == "" {
				next.Description = out[i].Description
			}
			if next.Tools == "" {
				next.Tools = out[i].Tools
			}
			if next.Content == "" {
				next.Content = out[i].Content
			}
			out[i] = next
			return out
		}
	}
	return append(out, next)
}

func emptyToolConfigBlock(cfg ToolConfigBlock) bool {
	return len(cfg.ScopedFiles) == 0 && !cfg.SkipDefaultTools && !cfg.DisableRuntimeWorktree
}

func toolConfigFromDecl(cfg ToolConfigBlock) models.AgentToolConfig {
	out := models.AgentToolConfig{
		SkipDefaultTools:       cfg.SkipDefaultTools,
		DisableRuntimeWorktree: cfg.DisableRuntimeWorktree,
	}
	if len(cfg.ScopedFiles) > 0 {
		out.ScopedFiles = make([]models.ScopedFilesConfig, 0, len(cfg.ScopedFiles))
		for _, scope := range cfg.ScopedFiles {
			out.ScopedFiles = append(out.ScopedFiles, models.ScopedFilesConfig{
				Directory:   strings.TrimSpace(scope.Directory),
				Permissions: append([]string(nil), scope.Permissions...),
			})
		}
	}
	return out
}

func mcpFromStrings(servers []string) []models.MCPServerConfig {
	if len(servers) == 0 {
		return nil
	}
	out := make([]models.MCPServerConfig, 0, len(servers))
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, models.MCPServerConfig{Name: s})
	}
	return out
}

func permissionDefaultsFromDecl(decl *SkillDeclaration) models.AgentPermissionDefaults {
	p := decl.Permissions
	return models.AgentPermissionDefaults{
		ReadTaskPrompt:       p.ReadTaskPrompt,
		ReadTaskExecution:    p.ReadTaskExecution,
		ReadProjectMemory:    p.ReadProjectMemory,
		WriteProjectMemory:   p.WriteProjectMemory,
		ReadAgents:           p.ReadAgents,
		WriteAgents:          p.WriteAgents,
		ReadSkills:           p.ReadSkills,
		WriteSkills:          p.WriteSkills,
		ReadRepositoryFiles:  p.ReadRepositoryFiles,
		WriteRepositoryFiles: p.WriteRepositoryFiles,
		UseShellOrTools:      p.UseShellOrTools,
	}
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
