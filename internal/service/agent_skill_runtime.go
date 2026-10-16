package service

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func (s *LLMService) agentDeclaredSkillRuntimeTools(ctx context.Context, task models.Task, agent *models.Agent, workDir string) *llmcontracts.RuntimeTools {
	if s == nil || agent == nil || !agentExplicitlyAllowsAnyTool(agent, "skill_view", "skills_list", "agent_list", "agent_view", "skill_manage", "skill_import", "agent_skill_manage") {
		return nil
	}
	selectedCatalog := lifecycleTurnFromContext(ctx).Catalog
	projectRoot := ""
	if s.projectRepo != nil {
		projectRoot = projectSkillRoot(ctx, s.projectRepo, task.ProjectID)
	}
	standaloneCatalog := s.standaloneSkillCatalog(task, projectRoot)

	var inspector agentskills.AgentInspector
	if s.agentRepo != nil {
		inspector = newAgentInspector(s.agentRepo, s.lifecycleRepo, nil)
	}
	var readers *llmcontracts.RuntimeTools
	readerCatalog := selectedCatalog
	if agentExplicitlyAllowsAnyTool(agent, "skills_list", "agent_list", "agent_view") {
		readerCatalog = mergeSkillCatalogs(task.ID+":skill-tools", standaloneCatalog, selectedCatalog)
		readers = agentskills.ExecutingSkillRuntimeTools(readerCatalog, s.globalSkillRoot, projectRoot, inspector)
	} else if agentExplicitlyAllowsTool(agent, "skill_view") {
		readers = agentskills.SelectedSkillRuntimeTools(selectedCatalog)
	}
	turn := lifecycleTurnFromContext(ctx)
	readers = s.instrumentSkillRuntimeTools(readers, readerCatalog, skillAnalyticsContext{ProjectID: task.ProjectID, TaskID: task.ID, ThreadID: turnThreadID(task.ID, turn), AgentID: agent.ID, Source: models.SkillEventSourceManual, Surface: skillAnalyticsSurface(task, turn)})
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
