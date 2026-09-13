package service

import (
	"context"
	"strings"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
)

func (w *WorkerService) buildSkillCatalog(ctx context.Context, task models.Task, assignedAgent *models.Agent) *agentskills.Catalog {
	projectRoot := projectSkillRoot(ctx, w.projectRepo, task.ProjectID)
	if assignedAgent != nil && assignedAgent.Key != "" {
		catalog, err := agentskills.BuildAgentCatalog(task.ID, w.globalSkillRoot, projectRoot, assignedAgent.Key)
		if err != nil {
			return agentskills.NewCatalog(task.ID, nil)
		}
		return catalog
	}
	catalog, err := agentskills.BuildCatalog(task.ID, w.globalSkillRoot, projectRoot)
	if err != nil {
		// Catalog construction should never fail the user task; it only limits
		// available skills for this turn.
		return agentskills.NewCatalog(task.ID, nil)
	}
	return catalog
}

func (w *WorkerService) renderAvailableSkillsForTask(ctx context.Context, task models.Task, projectRoot string, assignedAgent *models.Agent) string {
	if assignedAgent != nil && assignedAgent.Key != "" {
		return agentskills.RenderAvailableAgentSkillsMarkdown(w.globalSkillRoot, projectRoot, assignedAgent.Key)
	}
	return agentskills.RenderAvailableSkillsMarkdown(w.globalSkillRoot, projectRoot)
}

func (w *WorkerService) buildLifecycleReadRuntimeTools(task models.Task, catalog *agentskills.Catalog) *llmcontracts.RuntimeTools {
	if catalog == nil {
		return nil
	}
	var tools *llmcontracts.RuntimeTools
	if catalog.IsAgentOwned() {
		tools = agentskills.SelectedSkillRuntimeTools(catalog)
	} else {
		var inspector agentskills.AgentInspector
		if w.agentRepo != nil {
			inspector = newAgentInspector(w.agentRepo, w.lifecycleRepo, w.CurrentLifecycleCatalog)
		}
		projectRoot := projectSkillRoot(context.Background(), w.projectRepo, task.ProjectID)
		tools = agentskills.SkillRuntimeTools(catalog, w.globalSkillRoot, projectRoot, inspector)
	}
	return w.instrumentSkillRuntimeTools(tools, catalog, skillAnalyticsContext{ProjectID: task.ProjectID, TaskID: task.ID, Source: models.SkillEventSourceLifecycleHook, Surface: models.SkillSurfaceLifecycleHook})
}

func (w *WorkerService) buildTaskSkillRuntimeTools(ctx context.Context, task models.Task, catalog *agentskills.Catalog) *llmcontracts.RuntimeTools {
	if catalog == nil {
		return nil
	}
	turn := lifecycleTurnFromContext(ctx)
	agentID := ""
	if turn.AssignedAgent != nil {
		agentID = turn.AssignedAgent.ID
	}
	return w.instrumentSkillRuntimeTools(agentskills.SelectedSkillRuntimeTools(catalog), catalog, skillAnalyticsContext{ProjectID: task.ProjectID, TaskID: task.ID, ThreadID: turnThreadID(task.ID, turn), AgentID: agentID, Source: models.SkillEventSourceManual, Surface: skillAnalyticsSurface(task, turn)})
}

func (w *WorkerService) buildTaskMemoryRuntimeTools(ctx context.Context, task models.Task, memories []memory.SelectedMemory) *llmcontracts.RuntimeTools {
	if w == nil || len(memories) == 0 || !w.taskAgentAllowsMemoryView(ctx, task) {
		return nil
	}
	repoPath := projectRepoPath(ctx, w.projectRepo, task.ProjectID)
	if strings.TrimSpace(repoPath) == "" {
		return nil
	}
	return memory.SelectedMemoryRuntimeTools(repoPath, memories)
}

func (w *WorkerService) taskAgentAllowsMemoryView(ctx context.Context, task models.Task) bool {
	agent := w.taskAgentDefinition(ctx, task)
	if agent == nil {
		return true
	}
	for _, tool := range agent.Tools {
		if strings.EqualFold(strings.TrimSpace(tool), "memory_view") {
			return true
		}
	}
	return false
}

func (w *WorkerService) taskAgentDefinition(ctx context.Context, task models.Task) *models.Agent {
	if task.AgentDefinitionID == nil || *task.AgentDefinitionID == "" || w == nil || w.agentRepo == nil {
		return nil
	}
	if cache := lifecycle.AgentDefinitionCacheFromContext(ctx); cache != nil {
		agent, err := cache.GetByID(ctx, *task.AgentDefinitionID)
		if err != nil {
			return nil
		}
		return agent
	}
	agent, err := w.agentRepo.GetByID(ctx, *task.AgentDefinitionID)
	if err != nil {
		return nil
	}
	return agent
}

func (w *WorkerService) buildLifecycleRuntimeTools(ctx context.Context, task models.Task, catalog *agentskills.Catalog, assignedAgent *models.Agent) *llmcontracts.RuntimeTools {
	if catalog == nil {
		return nil
	}
	var inspector agentskills.AgentInspector
	if w.agentRepo != nil {
		inspector = newAgentInspector(w.agentRepo, w.lifecycleRepo, w.CurrentLifecycleCatalog)
	}
	importer := w.buildLifecycleImporter(task)
	var recorder agentlibrary.MutationRecorder
	if w.mutationRecorder != nil {
		recorder = w.mutationRecorder(task)
	}
	projectRoot := projectSkillRoot(context.Background(), w.projectRepo, task.ProjectID)
	assignedAgentKey := ""
	agentScope := "project"
	if assignedAgent != nil {
		assignedAgentKey = strings.TrimSpace(assignedAgent.Key)
		if strings.TrimSpace(string(assignedAgent.Scope)) == "global" {
			agentScope = "global"
		}
	}
	tools := lifecycleRuntimeTools(catalog, inspector, importer, recorder, w.globalSkillRoot, projectRoot, assignedAgentKey, agentScope)
	return instrumentSkillEditRuntimeTools(w.skillAnalyticsRepo, tools, skillAnalyticsContext{ProjectID: task.ProjectID, TaskID: task.ID, AgentID: agentIDFromAgent(assignedAgent), Source: models.SkillEventSourceLifecycleHook, Surface: models.SkillSurfaceLifecycleHook})
}

func (w *WorkerService) buildLifecycleImporter(task models.Task) *agentlibrary.Importer {
	if w.agentRepo == nil || w.lifecycleRepo == nil {
		return nil
	}
	projectRoot := ""
	if w.projectRepo != nil && task.ProjectID != "" {
		projectRoot = projectSkillRoot(context.Background(), w.projectRepo, task.ProjectID)
	}
	roots := agentlibrary.SkillRoots{Global: w.globalSkillRoot, Project: projectRoot}
	return agentlibrary.NewImporter(roots, agentlibrary.NewRepoApplier(w.agentRepo, w.lifecycleRepo))
}
