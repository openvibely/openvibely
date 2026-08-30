package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/agentskills"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type lifecycleTurnContextKey struct{}

// lifecycleTurnContext carries the app-owned, frozen-per-turn skill catalog and
// prepared prompt blocks from lifecycle hooks into LLMService prompt assembly.
type lifecycleTurnContext struct {
	Catalog                   *agentskills.Catalog
	SkillIndex                string
	PreparedBlocks            string
	SelectedSkillHandles      []string
	SelectedSkillsProvenance  agentskills.SkillSelectionProvenance
	AssignedAgent             *models.Agent
	AfterCompleteRuntimeTools *llmcontracts.RuntimeTools
	TaskThreadTurn            bool
	TurnPrompt                string
	TaskRunID                 string
}

func withLifecycleTurnContext(ctx context.Context, turn lifecycleTurnContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, lifecycleTurnContextKey{}, turn)
}

func lifecycleTurnFromContext(ctx context.Context) lifecycleTurnContext {
	if ctx == nil {
		return lifecycleTurnContext{}
	}
	turn, _ := ctx.Value(lifecycleTurnContextKey{}).(lifecycleTurnContext)
	return turn
}

func SelectedSkillHandlesFromContext(ctx context.Context) []string {
	turn := lifecycleTurnFromContext(ctx)
	return append([]string(nil), turn.SelectedSkillHandles...)
}

func WithTaskThreadLifecycleTurn(ctx context.Context) context.Context {
	return WithTaskThreadLifecycleTurnPrompt(ctx, "")
}

func WithTaskThreadLifecycleTurnPrompt(ctx context.Context, prompt string) context.Context {
	turn := lifecycleTurnFromContext(ctx)
	turn.TaskThreadTurn = true
	turn.TurnPrompt = strings.TrimSpace(prompt)
	return withLifecycleTurnContext(ctx, turn)
}

func WithAfterCompleteRuntimeTools(ctx context.Context, rt *llmcontracts.RuntimeTools) context.Context {
	turn := lifecycleTurnFromContext(ctx)
	turn.AfterCompleteRuntimeTools = llmcontracts.CompositeRuntimeTools(turn.AfterCompleteRuntimeTools, rt)
	return withLifecycleTurnContext(ctx, turn)
}

// CatalogSkillResolver resolves lifecycle hook skill bodies. Hook skills are
// resolved under the owning agent's private skill folder. This is separate from
// task-turn skill selection, where route_task may select standalone skills or
// skills owned by the assigned task agent.
type CatalogSkillResolver struct {
	agentRepo   *repository.AgentRepo
	catalogFn   func() *agentskills.Catalog
	globalRoot  string
	projectRoot func(context.Context, string) string
}

func NewCatalogSkillResolver(agentRepo *repository.AgentRepo, catalogFn func() *agentskills.Catalog, globalRoot string, projectRoot func(context.Context, string) string) *CatalogSkillResolver {
	return &CatalogSkillResolver{agentRepo: agentRepo, catalogFn: catalogFn, globalRoot: globalRoot, projectRoot: projectRoot}
}

func (r *CatalogSkillResolver) ResolveSkill(ctx context.Context, hook models.AgentLifecycleHook) (string, error) {
	if r == nil {
		return "", nil
	}
	skillKey := strings.TrimSpace(hook.SkillKey)
	if skillKey == "" {
		return "", nil
	}
	agentKey := ""
	projectID := ""
	if r.agentRepo != nil && hook.AgentID != "" {
		agent, err := r.agentRepo.GetByID(ctx, hook.AgentID)
		if err != nil {
			return "", err
		}
		if agent != nil {
			agentKey = strings.TrimSpace(agent.Key)
			projectID = agent.ProjectID
		}
	}
	if agentKey != "" {
		if body, ok, err := r.readAgentOwnedSkill(ctx, projectID, agentKey, skillKey); err != nil {
			return "", err
		} else if ok {
			return body, nil
		}
		return "", fmt.Errorf("lifecycle hook skill %q is not available under owning agent %s/%s", skillKey, agentKey, skillKey)
	}
	turn := lifecycleTurnFromContext(ctx)
	catalog := turn.Catalog
	if catalog == nil && r.catalogFn != nil {
		catalog = r.catalogFn()
	}
	if catalog == nil {
		return "", nil
	}
	entry, ok := catalog.Lookup(skillKey)
	if !ok {
		return "", fmt.Errorf("skill %q is not in current turn catalog", skillKey)
	}
	body, err := os.ReadFile(entry.AbsolutePath)
	if err != nil {
		return "", fmt.Errorf("read skill %q: %w", skillKey, err)
	}
	return string(body), nil
}

func (r *CatalogSkillResolver) readAgentOwnedSkill(ctx context.Context, projectID, agentKey, skillKey string) (string, bool, error) {
	roots := []string{}
	if r.projectRoot != nil && projectID != "" {
		if root := r.projectRoot(ctx, projectID); root != "" {
			roots = append(roots, root)
		}
	}
	if r.globalRoot != "" {
		roots = append(roots, r.globalRoot)
	}
	for _, root := range roots {
		path, err := agentlibrary.AgentSkillDir(root, agentKey, skillKey)
		if err != nil {
			return "", false, err
		}
		body, err := os.ReadFile(filepath.Join(path, agentskills.SkillFile))
		if err == nil {
			return string(body), true, nil
		}
		if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("read skill %q: %w", agentKey+"/"+skillKey, err)
		}
	}
	return "", false, nil
}

type agentInspector struct {
	agentRepo     *repository.AgentRepo
	lifecycleRepo *repository.LifecycleRepo
	catalogFn     func() *agentskills.Catalog
}

func newAgentInspector(agentRepo *repository.AgentRepo, lifecycleRepo *repository.LifecycleRepo, catalogFn func() *agentskills.Catalog) *agentInspector {
	return &agentInspector{agentRepo: agentRepo, lifecycleRepo: lifecycleRepo, catalogFn: catalogFn}
}

func isBuiltInSystemAgentKeyForList(key string) bool {
	switch strings.TrimSpace(key) {
	case models.AgentSystemKindSkillCurator, models.AgentSystemKindMemoryCurator, models.AgentSystemKindGoal:
		return true
	default:
		return false
	}
}

func (i *agentInspector) ListAgents(ctx context.Context) ([]agentskills.AgentSummary, error) {
	if i == nil || i.agentRepo == nil {
		return nil, nil
	}
	agentSummaries, err := i.agentRepo.ListAgentListSummaries(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]agentskills.AgentSummary, 0, len(agentSummaries))
	for _, agent := range agentSummaries {
		if !agent.Enabled || agent.GeneratedStatus == models.AgentStatusProtected || agent.GeneratedStatus == models.AgentStatusArchived || agent.ArchivedAt != nil || strings.TrimSpace(agent.SystemKind) != "" || isBuiltInSystemAgentKeyForList(agent.Key) {
			continue
		}
		out = append(out, agentskills.AgentSummary{
			Key:             agent.Key,
			Name:            agent.Name,
			Description:     agent.Description,
			Scope:           string(agent.Scope),
			Enabled:         agent.Enabled,
			Selectable:      agent.SelectableAsPrimary,
			GeneratedStatus: string(agent.GeneratedStatus),
			AttachedSkills:  trimmedAgentSkillNames(agent.AttachedSkillNames),
		})
	}
	return out, nil
}

func (i *agentInspector) InspectAgent(ctx context.Context, agentKey string) (*agentskills.AgentDetails, error) {
	if i == nil || i.agentRepo == nil {
		return nil, nil
	}
	agent, err := i.agentRepo.GetByKey(ctx, agentKey)
	if err != nil || agent == nil {
		return nil, err
	}
	details := &agentskills.AgentDetails{
		Key:             agent.Key,
		Name:            agent.Name,
		Description:     agent.Description,
		SystemPrompt:    agent.SystemPrompt,
		Scope:           string(agent.Scope),
		Enabled:         agent.Enabled,
		Selectable:      agent.SelectableAsPrimary,
		GeneratedStatus: string(agent.GeneratedStatus),
		ToolGrants:      append([]string(nil), agent.Tools...),
		AttachedSkills:  embeddedAgentSkillNames(agent.Skills),
	}
	if raw, _ := json.Marshal(agent.PermissionDefaults); string(raw) != "{}" {
		_ = json.Unmarshal(raw, &details.Permissions)
	}
	if raw, _ := json.Marshal(agent.ModelDefaults); string(raw) != "{}" {
		_ = json.Unmarshal(raw, &details.ModelDefaults)
	}
	if i.lifecycleRepo != nil {
		hooks, err := i.lifecycleRepo.HooksByAgent(ctx, agent.ID)
		if err != nil {
			return nil, err
		}
		for _, h := range hooks {
			details.Hooks = append(details.Hooks, agentskills.AgentHookView{
				When:           string(h.When),
				SkillKey:       h.SkillKey,
				OutputContract: string(h.OutputContract),
				Blocking:       h.Blocking,
				Enabled:        h.Enabled,
			})
		}
	}
	return details, nil
}

func embeddedAgentSkillNames(skills []models.SkillConfig) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func trimmedAgentSkillNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func projectRepoPath(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	if projectRepo == nil || projectID == "" {
		return ""
	}
	project, err := projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil || project.RepoPath == "" {
		return ""
	}
	return project.RepoPath
}

func projectSkillRoot(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	if repoPath := projectRepoPath(ctx, projectRepo, projectID); repoPath != "" {
		return filepath.Join(repoPath, ".openvibely")
	}
	return ""
}

// ProjectSkillRootForResolver exposes the project-scoped skill root calculation
// for lifecycle hook resolver wiring without exposing repository internals.
func ProjectSkillRootForResolver(ctx context.Context, projectRepo *repository.ProjectRepo, projectID string) string {
	return projectSkillRoot(ctx, projectRepo, projectID)
}

func buildLifecyclePromptContext(skillIndex, contextBlocks string) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(contextBlocks) != "" {
		parts = append(parts, "## Lifecycle Context\n\n"+strings.TrimSpace(contextBlocks))
	}
	if strings.TrimSpace(skillIndex) != "" {
		parts = append(parts, strings.TrimSpace(skillIndex))
	}
	return strings.Join(parts, "\n\n")
}

func lifecycleRuntimeTools(catalog *agentskills.Catalog, inspector agentskills.AgentInspector, importer *agentlibrary.Importer, recorder agentlibrary.MutationRecorder, globalRoot, projectRoot, assignedAgentKey, assignedAgentScope string) *llmcontracts.RuntimeTools {
	return llmcontracts.CompositeRuntimeTools(
		agentskills.SkillRuntimeTools(catalog, globalRoot, projectRoot, inspector),
		agentlibrary.SkillMutationTools(importer, recorder),
		agentlibrary.AgentSkillMutationTools(importer, recorder, assignedAgentKey, assignedAgentScope),
	)
}
