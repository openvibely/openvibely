package service

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/builtinskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func (s *LLMService) agentDeclaredSkillRuntimeTools(ctx context.Context, task models.Task, agent *models.Agent, workDir string) *llmcontracts.RuntimeTools {
	if s == nil || agent == nil || !agentExplicitlyAllowsAnyTool(agent, "skill_view", "skills_list", "agent_list", "agent_view", "skill_manage", "skill_import", "agent_skill_manage") {
		return nil
	}
	if agent.SystemKind != models.AgentSystemKindSkillCurator || task.Category != models.CategoryScheduled || lifecycleTurnFromContext(ctx).TaskThreadTurn {
		if _, prepared := ctx.Value(managedTurnKey{}).(managedTurn); prepared {
			// The prepared reader resolves the current cached catalog on each call.
			// Do not shadow it with a reader bound to the initial turn catalog.
			return nil
		}
		return agentskills.SelectedSkillRuntimeTools(lifecycleTurnFromContext(ctx).Catalog)
	}
	projectRoot := ""
	if s.projectRepo != nil {
		projectRoot = projectSkillRoot(ctx, s.projectRepo, task.ProjectID)
	}
	var inspector agentskills.AgentInspector
	if s.agentRepo != nil {
		inspector = newAgentInspector(s.agentRepo, s.lifecycleRepo, nil)
	}
	turn := lifecycleTurnFromContext(ctx)
	buildReaders := func(callCtx context.Context) *llmcontracts.RuntimeTools {
		// Rebuild the authorized scopes together so project overrides and newly
		// created skills take effect before the next read in this execution.
		catalog, err := agentskills.BuildAgentCatalog(task.ID, s.globalSkillRoot, projectRoot, agent.Key)
		if err != nil {
			catalog = agentskills.NewCatalog(task.ID, nil)
		}
		// Scheduled curation inspects skill instructions; it does not activate
		// their dependencies or wait for permission to install document tools.
		var readers *llmcontracts.RuntimeTools
		if agentExplicitlyAllowsAnyTool(agent, "skills_list", "agent_list", "agent_view") {
			catalog = mergeSkillCatalogs(task.ID+":skill-tools", s.standaloneSkillCatalog(task, projectRoot), catalog)
			readers = agentskills.FilteredSkillRuntimeTools(catalog, s.globalSkillRoot, projectRoot, inspector, func(path string) bool {
				return curatorMayInspectSkill(s.globalSkillRoot, path)
			})
		} else if agentExplicitlyAllowsTool(agent, "skill_view") {
			readers = agentskills.InspectSelectedSkillRuntimeTools(catalog)
		}
		return s.instrumentSkillRuntimeTools(readers, catalog, skillAnalyticsContext{ProjectID: task.ProjectID, TaskID: task.ID, ThreadID: turnThreadID(task.ID, turn), AgentID: agent.ID, Source: models.SkillEventSourceManual, Surface: skillAnalyticsSurface(task, turn)})
	}
	readers := refreshingSkillReaders(buildReaders(ctx), buildReaders)
	var writers []*llmcontracts.RuntimeTools
	if agentExplicitlyAllowsTool(agent, "skill_manage") || agentExplicitlyAllowsTool(agent, "skill_import") || agentExplicitlyAllowsTool(agent, "agent_skill_manage") {
		importer := s.agentSkillImporter(task)
		var recorder agentlibrary.MutationRecorder
		if s.mutationRecorder != nil {
			recorder = s.mutationRecorder(task)
		}
		editMeta := skillAnalyticsContext{ProjectID: task.ProjectID, TaskID: task.ID, ThreadID: turnThreadID(task.ID, turn), AgentID: agent.ID, Source: models.SkillEventSourceManual, Surface: skillAnalyticsSurface(task, turn)}
		if agentExplicitlyAllowsTool(agent, "skill_manage") || agentExplicitlyAllowsTool(agent, "skill_import") {
			writers = append(writers, instrumentSkillEditRuntimeTools(s.skillAnalyticsRepo, agentlibrary.SkillMutationTools(importer, recorder), editMeta))
		}
		if agentExplicitlyAllowsTool(agent, "agent_skill_manage") {
			writers = append(writers, instrumentSkillEditRuntimeTools(s.skillAnalyticsRepo, agentlibrary.LibraryAgentSkillMutationTools(importer, recorder), editMeta))
		}
	}
	parts := []*llmcontracts.RuntimeTools{readers}
	parts = append(parts, writers...)
	return llmcontracts.CompositeRuntimeTools(parts...)
}

// Bundled library packages are app-owned, not curator maintenance targets.
// The curator still needs its own bundled instructions to perform maintenance.
// Project overrides are user-owned even when their names match bundled skills.
func curatorMayInspectSkill(globalRoot, skillPath string) bool {
	if globalRoot == "" {
		return true
	}
	rel, err := filepath.Rel(globalRoot, skillPath)
	if err != nil {
		return true
	}
	if strings.HasPrefix(filepath.ToSlash(rel), "agents/skill_curator/skills/") {
		return true
	}
	return !builtinskills.IsBundledFile(rel)
}

func (s *LLMService) standaloneSkillCatalog(task models.Task, projectRoot string) *agentskills.Catalog {
	if s == nil {
		return nil
	}
	catalog, err := agentskills.BuildCatalog(task.ID+":standalone-skills", s.globalSkillRoot, projectRoot)
	if err != nil {
		return nil
	}
	return catalog
}

func mergeSkillCatalogs(turnID string, catalogs ...*agentskills.Catalog) *agentskills.Catalog {
	var entries []agentskills.Entry
	for _, catalog := range catalogs {
		if catalog == nil {
			continue
		}
		entries = append(entries, catalog.Entries()...)
	}
	if len(entries) == 0 {
		return nil
	}
	return agentskills.NewCatalog(turnID, entries)
}

func (s *LLMService) agentSkillImporter(task models.Task) *agentlibrary.Importer {
	if s == nil || s.agentRepo == nil || s.lifecycleRepo == nil {
		return nil
	}
	projectRoot := ""
	if s.projectRepo != nil && task.ProjectID != "" {
		projectRoot = projectSkillRoot(context.Background(), s.projectRepo, task.ProjectID)
	}
	roots := agentlibrary.SkillRoots{Global: s.globalSkillRoot, Project: projectRoot}
	return agentlibrary.NewImporter(roots, agentlibrary.NewRepoApplier(s.agentRepo, s.lifecycleRepo))
}

func agentExplicitlyAllowsAnyTool(agent *models.Agent, names ...string) bool {
	for _, name := range names {
		if agentExplicitlyAllowsTool(agent, name) {
			return true
		}
	}
	return false
}

func agentExplicitlyAllowsTool(agent *models.Agent, name string) bool {
	if agent == nil || strings.TrimSpace(name) == "" {
		return false
	}
	want := strings.ToLower(strings.TrimSpace(name))
	for _, tool := range agent.Tools {
		if strings.ToLower(strings.TrimSpace(tool)) == want {
			return true
		}
	}
	return false
}

// refreshingSkillReaders shares per-call catalog refresh across main turns and
// maintenance. The factory owns scope and analytics; the outer runtime retains
// its definitions and permission filters.
func refreshingSkillReaders(initial *llmcontracts.RuntimeTools, build func(context.Context) *llmcontracts.RuntimeTools) *llmcontracts.RuntimeTools {
	if initial == nil {
		return nil
	}
	out := *initial
	out.Executor = func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		if !initial.HasDefinition(name) {
			return "", false, false, nil
		}
		if err := ctx.Err(); err != nil {
			return "", true, true, err
		}
		current := build(ctx)
		if current == nil || current.Executor == nil {
			return "Skill reader unavailable", true, true, nil
		}
		return current.Executor(ctx, name, input)
	}
	return &out
}
