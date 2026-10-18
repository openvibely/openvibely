package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
)

func (w *WorkerService) buildSkillCatalog(ctx context.Context, task models.Task) *agentskills.Catalog {
	projectRoot := projectSkillRoot(ctx, w.projectRepo, task.ProjectID)
	if agent := w.taskAgentDefinition(ctx, task); agent != nil && agent.Key != "" {
		if !agent.Enabled || agent.ArchivedAt != nil || agent.ProjectID != "" && agent.ProjectID != task.ProjectID {
			return agentskills.NewCatalog(task.ID, nil)
		}
		catalog, err := agentskills.BuildAgentCatalog(task.ID, w.globalSkillRoot, projectRoot, agent.Key)
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

func (w *WorkerService) renderAvailableSkillsForTask(ctx context.Context, task models.Task, projectRoot string) string {
	if agent := w.taskAgentDefinition(ctx, task); agent != nil && agent.Key != "" {
		if !agent.Enabled || agent.ArchivedAt != nil || agent.ProjectID != "" && agent.ProjectID != task.ProjectID {
			return ""
		}
		return agentskills.RenderAvailableAgentSkillsMarkdown(w.globalSkillRoot, projectRoot, agent.Key)
	}
	return agentskills.RenderAvailableSkillsMarkdown(w.globalSkillRoot, projectRoot)
}

func (w *WorkerService) buildLifecycleReadRuntimeTools(task models.Task, catalog *agentskills.Catalog) *llmcontracts.RuntimeTools {
	if catalog == nil {
		return nil
	}
	var tools *llmcontracts.RuntimeTools
	if catalog.IsAgentOwned() {
		tools = agentskills.InspectSelectedSkillRuntimeTools(catalog)
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
	agent, err := w.agentRepo.GetByID(ctx, *task.AgentDefinitionID)
	if err != nil {
		return nil
	}
	return agent
}

func (w *WorkerService) buildLifecycleRuntimeTools(task models.Task, catalog *agentskills.Catalog) *llmcontracts.RuntimeTools {
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
	assignedAgent := w.taskAgentDefinition(context.Background(), task)
	assignedAgentKey := ""
	agentScope := "project"
	if assignedAgent != nil {
		assignedAgentKey = strings.TrimSpace(assignedAgent.Key)
		if strings.TrimSpace(string(assignedAgent.Scope)) == "global" {
			agentScope = "global"
		}
	}
	tools := lifecycleRuntimeTools(catalog, inspector, importer, recorder, w.globalSkillRoot, projectRoot, assignedAgentKey, agentScope)
	// Detached lifecycle hooks do not inherit the main turn's permission guard.
	// Rebuild their mutation importer on each call so revocations take effect and
	// a disabled global-write grant never leaves a writable global root available.
	tools.Executor = func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		permissions := projectManagedPermissions(ctx, w.projectRepo, task.ProjectID)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "skill_manage", "skill_import", "agent_skill_manage":
			if !permissions.WriteSkills {
				return "skill writing is not permitted", true, true, nil
			}
			roots := agentlibrary.SkillRoots{Project: projectRoot}
			if permissions.AllowGlobalSkillWrites {
				roots.Global = w.globalSkillRoot
			}
			scopedImporter := agentlibrary.NewImporter(roots, agentlibrary.NewRepoApplier(w.agentRepo, w.lifecycleRepo))
			return lifecycleRuntimeTools(catalog, inspector, scopedImporter, recorder, w.globalSkillRoot, projectRoot, assignedAgentKey, agentScope).Executor(ctx, name, input)
		default:
			return lifecycleRuntimeTools(catalog, inspector, importer, recorder, w.globalSkillRoot, projectRoot, assignedAgentKey, agentScope).Executor(ctx, name, input)
		}
	}
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
