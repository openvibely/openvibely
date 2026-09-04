package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type skillAnalyticsContext struct {
	ProjectID   string
	TaskID      string
	ExecutionID string
	ThreadID    string
	AgentID     string
	Source      string
	Surface     string
}

func (w *WorkerService) SetSkillAnalyticsRepo(repo *repository.SkillAnalyticsRepo) {
	if w != nil {
		w.skillAnalyticsRepo = repo
	}
}

func (s *LLMService) SetSkillAnalyticsRepo(repo *repository.SkillAnalyticsRepo) {
	if s != nil {
		s.skillAnalyticsRepo = repo
	}
}

func skillAnalyticsSurface(task models.Task, turn lifecycleTurnContext) string {
	if turn.TaskThreadTurn {
		return models.SkillSurfaceTaskThread
	}
	if task.Category == models.CategoryScheduled {
		return models.SkillSurfaceScheduledTask
	}
	if task.Category == models.CategoryChat {
		return models.SkillSurfaceChat
	}
	return models.SkillSurfaceTaskThread
}

func skillScopeFromEntry(entry agentskills.Entry) string {
	if entry.AgentKey != "" || entry.Source == agentskills.SourceAgent {
		return models.SkillScopeAgentOwned
	}
	if entry.Source == agentskills.SourceProject {
		return models.SkillScopeProject
	}
	return models.SkillScopeGlobal
}

func recordSkillAnalyticsEvent(ctx context.Context, repo *repository.SkillAnalyticsRepo, event models.SkillAnalyticsEvent) {
	if repo == nil || strings.TrimSpace(event.SkillHandle) == "" || strings.TrimSpace(event.EventType) == "" {
		return
	}
	if err := repo.RecordEvent(ctx, &event); err != nil {
		applog.Infof("[skill-analytics] record event failed handle=%s type=%s: %v", event.SkillHandle, event.EventType, err)
	}
}

func (w *WorkerService) recordLifecycleHookSkillSelected(ctx context.Context, hook models.AgentLifecycleHook, input lifecycle.HookInput, exec models.LifecycleExecution) {
	if w == nil || w.skillAnalyticsRepo == nil || strings.TrimSpace(hook.SkillKey) == "" {
		return
	}
	recordSkillAnalyticsEvent(ctx, w.skillAnalyticsRepo, models.SkillAnalyticsEvent{
		ProjectID:   strings.TrimSpace(input.ProjectID),
		TaskID:      strings.TrimSpace(input.TaskID),
		ThreadID:    strings.TrimSpace(exec.ID),
		AgentID:     strings.TrimSpace(hook.AgentID),
		SkillScope:  models.SkillScopeAgentOwned,
		SkillHandle: strings.TrimSpace(hook.SkillKey),
		EventType:   models.SkillEventSelected,
		Source:      models.SkillEventSourceLifecycleHook,
		Surface:     models.SkillSurfaceLifecycleHook,
	})
}

func (w *WorkerService) recordSelectedSkillEvents(ctx context.Context, task models.Task, catalog *agentskills.Catalog, handles []string, provenance agentskills.SkillSelectionProvenance, turn lifecycleTurnContext) {
	if w == nil || w.skillAnalyticsRepo == nil || catalog == nil {
		return
	}
	seen := map[string]struct{}{}
	for _, handle := range handles {
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		if _, ok := seen[handle]; ok {
			continue
		}
		seen[handle] = struct{}{}
		entry, ok := catalog.Lookup(handle)
		if !ok {
			continue
		}
		source := models.SkillEventSourceSkillCurator
		if provenance != nil && provenance[handle] == agentskills.ProvenanceAlwaysUse {
			source = models.SkillEventSourceAlwaysUse
		}
		agentID := ""
		if turn.AssignedAgent != nil {
			agentID = turn.AssignedAgent.ID
		}
		recordSkillAnalyticsEvent(ctx, w.skillAnalyticsRepo, models.SkillAnalyticsEvent{
			ProjectID:   task.ProjectID,
			TaskID:      task.ID,
			ThreadID:    turnThreadID(task.ID, turn),
			AgentID:     agentID,
			SkillScope:  skillScopeFromEntry(entry),
			SkillHandle: entry.Handle,
			EventType:   models.SkillEventSelected,
			Source:      source,
			Surface:     skillAnalyticsSurface(task, turn),
		})
	}
}

func agentIDFromAgent(agent *models.Agent) string {
	if agent == nil {
		return ""
	}
	return agent.ID
}

func turnThreadID(taskID string, turn lifecycleTurnContext) string {
	if strings.TrimSpace(turn.TaskRunID) != "" {
		return strings.TrimSpace(turn.TaskRunID)
	}
	if turn.TaskThreadTurn {
		return strings.TrimSpace(taskID)
	}
	return ""
}

func (s *LLMService) instrumentSkillRuntimeTools(base *llmcontracts.RuntimeTools, catalog *agentskills.Catalog, meta skillAnalyticsContext) *llmcontracts.RuntimeTools {
	return instrumentSkillRuntimeTools(s.skillAnalyticsRepo, base, catalog, meta)
}

func (w *WorkerService) instrumentSkillRuntimeTools(base *llmcontracts.RuntimeTools, catalog *agentskills.Catalog, meta skillAnalyticsContext) *llmcontracts.RuntimeTools {
	return instrumentSkillRuntimeTools(w.skillAnalyticsRepo, base, catalog, meta)
}

func instrumentSkillRuntimeTools(repo *repository.SkillAnalyticsRepo, base *llmcontracts.RuntimeTools, catalog *agentskills.Catalog, meta skillAnalyticsContext) *llmcontracts.RuntimeTools {
	if repo == nil || base == nil || base.Executor == nil || catalog == nil {
		return base
	}
	wrapped := *base
	wrapped.Executor = func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		out, handled, isErr, err := base.Executor(ctx, name, input)
		if handled && !isErr && err == nil && strings.EqualFold(strings.TrimSpace(name), "skill_view") {
			if entry, fullBody, ok := skillEntryFromViewInput(catalog, input); ok {
				baseEvent := models.SkillAnalyticsEvent{
					ProjectID:   meta.ProjectID,
					TaskID:      meta.TaskID,
					ExecutionID: meta.ExecutionID,
					ThreadID:    meta.ThreadID,
					AgentID:     meta.AgentID,
					SkillScope:  skillScopeFromEntry(entry),
					SkillHandle: entry.Handle,
					Source:      defaultSkillAnalyticsSource(meta.Source, models.SkillEventSourceManual),
					Surface:     defaultSkillAnalyticsSurface(meta.Surface),
				}
				viewed := baseEvent
				viewed.EventType = models.SkillEventViewed
				recordSkillAnalyticsEvent(ctx, repo, viewed)
				if fullBody {
					loaded := baseEvent
					loaded.EventType = models.SkillEventLoaded
					recordSkillAnalyticsEvent(ctx, repo, loaded)
				}
			}
		}
		return out, handled, isErr, err
	}
	return &wrapped
}

func skillEntryFromViewInput(catalog *agentskills.Catalog, input json.RawMessage) (agentskills.Entry, bool, bool) {
	var params struct {
		Handle   string `json:"handle"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return agentskills.Entry{}, false, false
	}
	entry, ok, ambiguous := catalog.ResolveSkillHandle(strings.TrimSpace(params.Handle))
	if !ok || ambiguous {
		return agentskills.Entry{}, false, false
	}
	return entry, strings.TrimSpace(params.FilePath) == "", true
}

func instrumentSkillEditRuntimeTools(repo *repository.SkillAnalyticsRepo, base *llmcontracts.RuntimeTools, meta skillAnalyticsContext) *llmcontracts.RuntimeTools {
	if repo == nil || base == nil || base.Executor == nil {
		return base
	}
	wrapped := *base
	wrapped.Executor = func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
		out, handled, isErr, err := base.Executor(ctx, name, input)
		tool := strings.ToLower(strings.TrimSpace(name))
		if handled && !isErr && err == nil && (tool == "skill_manage" || tool == "skill_import" || tool == "agent_skill_manage") {
			if handle, scope, eventType := changedSkillFromToolInput(tool, input); handle != "" {
				recordSkillAnalyticsEvent(ctx, repo, models.SkillAnalyticsEvent{
					ProjectID:   meta.ProjectID,
					TaskID:      meta.TaskID,
					ExecutionID: meta.ExecutionID,
					ThreadID:    meta.ThreadID,
					AgentID:     meta.AgentID,
					SkillScope:  scope,
					SkillHandle: handle,
					EventType:   eventType,
					Source:      defaultSkillAnalyticsSource(meta.Source, models.SkillEventSourceManual),
					Surface:     defaultSkillAnalyticsSurface(meta.Surface),
				})
			}
		}
		return out, handled, isErr, err
	}
	return &wrapped
}

func changedSkillFromToolInput(tool string, input json.RawMessage) (string, string, string) {
	var params struct {
		Action      string `json:"action"`
		Handle      string `json:"handle"`
		Scope       string `json:"scope"`
		Declaration string `json:"declaration"`
	}
	_ = json.Unmarshal(input, &params)
	handle := strings.TrimSpace(params.Handle)
	if handle == "" {
		handle = skillHandleFromDeclaration(params.Declaration)
	}
	scope := models.SkillScopeGlobal
	if tool == "agent_skill_manage" {
		scope = models.SkillScopeAgentOwned
	} else if strings.EqualFold(strings.TrimSpace(params.Scope), models.SkillScopeProject) {
		scope = models.SkillScopeProject
	}
	eventType := models.SkillEventEdited
	if strings.EqualFold(strings.TrimSpace(params.Action), "create") {
		eventType = models.SkillEventCreated
	}
	return handle, scope, eventType
}

func skillHandleFromDeclaration(declaration string) string {
	for _, line := range strings.Split(declaration, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "key:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "key:")), `"'`)
		}
	}
	return ""
}

func defaultSkillAnalyticsSource(source, fallback string) string {
	if strings.TrimSpace(source) != "" {
		return source
	}
	return fallback
}

func defaultSkillAnalyticsSurface(surface string) string {
	if strings.TrimSpace(surface) != "" {
		return surface
	}
	return models.SkillSurfaceTaskThread
}
