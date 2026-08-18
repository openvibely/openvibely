package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type systemAgentPermissionMode int

const (
	systemAgentMemoryPermissions systemAgentPermissionMode = iota
	systemAgentFullPermissions
)

type systemAgentDeclarationSpec struct {
	systemKind     string
	key            string
	sourceRefs     []string
	permissionMode systemAgentPermissionMode
}

type systemAgentLifecycleHookReconcileResult struct {
	existing []models.AgentLifecycleHook
	desired  map[string]struct{}
}

func systemAgentFromDeclaration(decl *agentlibrary.SkillDeclaration, spec systemAgentDeclarationSpec) *models.Agent {
	agent := &models.Agent{
		Name:                decl.Agent.AgentDisplayName(),
		Description:         decl.Agent.Description,
		SystemPrompt:        decl.Agent.SystemPrompt,
		Model:               firstNonEmptyString(decl.ModelDefaults.Model, "inherit"),
		Tools:               compactStrings(decl.Tools),
		ToolConfig:          toolConfigFromAgentDeclaration(decl.ToolConfig),
		Plugins:             compactStrings(decl.Plugins),
		MCPServers:          mcpServersFromAgentDeclaration(decl.MCPServers),
		Skills:              skillsFromBundledAgentIndex(decl.Agent.SystemPrompt),
		SystemKind:          spec.systemKind,
		Key:                 spec.key,
		Scope:               models.AgentScope(firstNonEmptyString(decl.Agent.Scope, string(models.AgentScopeGlobal))),
		ProjectID:           decl.Agent.ProjectID,
		SelectableAsPrimary: decl.Agent.SelectableAsPrimary,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
		PermissionDefaults:  systemAgentPermissionDefaults(decl, spec.permissionMode),
		ModelDefaults: models.AgentModelDefaults{
			Model:       decl.ModelDefaults.Model,
			Temperature: decl.ModelDefaults.Temperature,
			MaxTokens:   decl.ModelDefaults.MaxTokens,
		},
		SourceRefs: append([]string(nil), spec.sourceRefs...),
	}
	if decl.Agent.Enabled != nil {
		agent.Enabled = *decl.Agent.Enabled
	}
	return agent
}

func systemAgentPermissionDefaults(decl *agentlibrary.SkillDeclaration, mode systemAgentPermissionMode) models.AgentPermissionDefaults {
	defaults := models.AgentPermissionDefaults{
		ReadTaskPrompt:     decl.Permissions.ReadTaskPrompt,
		ReadTaskExecution:  decl.Permissions.ReadTaskExecution,
		ReadProjectMemory:  decl.Permissions.ReadProjectMemory,
		WriteProjectMemory: decl.Permissions.WriteProjectMemory,
		UseShellOrTools:    decl.Permissions.UseShellOrTools,
	}
	if mode == systemAgentFullPermissions {
		defaults.ReadAgents = decl.Permissions.ReadAgents
		defaults.WriteAgents = decl.Permissions.WriteAgents
		defaults.ReadSkills = decl.Permissions.ReadSkills
		defaults.WriteSkills = decl.Permissions.WriteSkills
		defaults.ReadRepositoryFiles = decl.Permissions.ReadRepositoryFiles
		defaults.WriteRepositoryFiles = decl.Permissions.WriteRepositoryFiles
	}
	return defaults
}

func applySystemAgentDeclaration(agent, want *models.Agent) bool {
	changed := false
	set := func(ok bool, apply func()) {
		if ok {
			apply()
			changed = true
		}
	}
	set(agent.Name != want.Name, func() { agent.Name = want.Name })
	set(agent.Description != want.Description, func() { agent.Description = want.Description })
	set(agent.SystemPrompt != want.SystemPrompt, func() { agent.SystemPrompt = want.SystemPrompt })
	set(agent.Model != want.Model, func() { agent.Model = want.Model })
	set(!sameAgentToolsList(agent.Tools, want.Tools), func() { agent.Tools = append([]string(nil), want.Tools...) })
	set(!sameScopedToolConfig(agent.ToolConfig, want.ToolConfig), func() { agent.ToolConfig = want.ToolConfig })
	set(!sameSkillConfigs(agent.Skills, want.Skills), func() { agent.Skills = append([]models.SkillConfig(nil), want.Skills...) })
	set(agent.SystemKind != want.SystemKind, func() { agent.SystemKind = want.SystemKind })
	set(agent.Key != want.Key, func() { agent.Key = want.Key })
	set(agent.Scope != want.Scope, func() { agent.Scope = want.Scope })
	set(agent.ProjectID != want.ProjectID, func() { agent.ProjectID = want.ProjectID })
	set(agent.SelectableAsPrimary != want.SelectableAsPrimary, func() { agent.SelectableAsPrimary = want.SelectableAsPrimary })
	set(agent.Enabled != want.Enabled, func() { agent.Enabled = want.Enabled })
	set(agent.GeneratedStatus != want.GeneratedStatus, func() { agent.GeneratedStatus = want.GeneratedStatus })
	set(agent.CreatedBy == "" || agent.CreatedBy != want.CreatedBy, func() { agent.CreatedBy = want.CreatedBy })
	set(!sameStringSlice(agent.SourceRefs, want.SourceRefs), func() { agent.SourceRefs = append([]string(nil), want.SourceRefs...) })
	if !sameJSON(agent.PermissionDefaults, want.PermissionDefaults) {
		agent.PermissionDefaults = want.PermissionDefaults
		changed = true
	}
	if !sameJSON(agent.ModelDefaults, want.ModelDefaults) {
		agent.ModelDefaults = want.ModelDefaults
		changed = true
	}
	return changed
}

func reconcileDeclaredSystemAgentLifecycleHooks(ctx context.Context, lifecycleRepo *repository.LifecycleRepo, agentID string, decl *agentlibrary.SkillDeclaration) (systemAgentLifecycleHookReconcileResult, error) {
	result := systemAgentLifecycleHookReconcileResult{desired: map[string]struct{}{}}
	if lifecycleRepo == nil || decl == nil || len(decl.LifecycleHooks) == 0 {
		return result, nil
	}
	existing, err := lifecycleRepo.HooksByAgent(ctx, agentID)
	if err != nil {
		return result, err
	}
	result.existing = existing
	for when, hookDecl := range decl.LifecycleHooks {
		if when == "primary" {
			continue
		}
		hook := lifecycleHookFromAgentDeclaration(agentID, models.LifecycleWhen(when), hookDecl)
		result.desired[string(hook.When)+"/"+hook.SkillKey] = struct{}{}
		match := findAgentLifecycleHook(existing, hook.When, hook.SkillKey)
		if match == nil {
			if err := lifecycleRepo.CreateHook(ctx, hook); err != nil {
				return result, err
			}
			continue
		}
		hook.ID = match.ID
		if !sameLifecycleHook(match, hook) {
			if err := lifecycleRepo.UpdateHook(ctx, hook); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}
