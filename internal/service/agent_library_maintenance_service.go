package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/builtinskills"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const agentLibraryMaintenanceTaskTitle = "System: Skill Library Maintenance"

const agentLibraryMaintenanceTaskPrompt = `Review and maintain this project's skill library.

Use the skill tools to inspect the standalone skill library, load support files when needed, consolidate duplicate standalone skills, archive standalone skills that are clearly obsolete, and improve reusable SKILL.md instructions when a completed-task pattern proves they should change. Use agent reads only when assigned-agent context is relevant.

Agents are standalone user-managed configurations. Do not create, edit, archive, route, or reassign agents. Do not change project memory. Prefer small, evidence-backed skill updates. When done, respond with a concise summary of what changed or why nothing changed.`

const bundledSkillCuratorDeclarationPath = "agents/skill_curator/SKILLS.md"
const bundledGoalAgentDeclarationPath = "agents/goal/SKILLS.md"

type AgentDeclarationSyncMetrics struct {
	ContentReads uint64
	Parses       uint64
}

type declarationFingerprint struct {
	size       int64
	modTime    int64
	mode       os.FileMode
	filesystem string
}

type declarationCacheEntry struct {
	fingerprint declarationFingerprint
	declaration *agentlibrary.SkillDeclaration
}

type declarationRootSyncResult struct {
	changed   bool
	active    map[string]struct{}
	displaced map[string]struct{}
}

type AgentLibraryMaintenanceService struct {
	taskRepo       *repository.TaskRepo
	scheduleRepo   *repository.ScheduleRepo
	agentRepo      *repository.AgentRepo
	lifecycleRepo  *repository.LifecycleRepo
	agentsRootPath string

	declarationMu          sync.Mutex
	declarationCache       map[string]declarationCacheEntry
	activeProjectRoot      string
	activeProjectRootKnown bool
	protectedDeclarationMu sync.Mutex
	protectedDeclarations  map[string]declarationCacheEntry
	declarationReads       atomic.Uint64
	declarationParses      atomic.Uint64
}

func NewAgentLibraryMaintenanceService(taskRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo, agentRepo *repository.AgentRepo) *AgentLibraryMaintenanceService {
	return &AgentLibraryMaintenanceService{taskRepo: taskRepo, scheduleRepo: scheduleRepo, agentRepo: agentRepo}
}

func (s *AgentLibraryMaintenanceService) SetLifecycleRepo(repo *repository.LifecycleRepo) {
	if s != nil {
		s.lifecycleRepo = repo
	}
}

func (s *AgentLibraryMaintenanceService) SetAgentsRootPath(root string) {
	if s != nil {
		s.agentsRootPath = root
	}
}

func (s *AgentLibraryMaintenanceService) DeclarationSyncMetrics() AgentDeclarationSyncMetrics {
	if s == nil {
		return AgentDeclarationSyncMetrics{}
	}
	return AgentDeclarationSyncMetrics{
		ContentReads: s.declarationReads.Load(),
		Parses:       s.declarationParses.Load(),
	}
}

func (s *AgentLibraryMaintenanceService) SyncRootDeclarations(ctx context.Context, projectRoot string) error {
	_, err := s.syncRootDeclarations(ctx, projectRoot)
	return err
}

func (s *AgentLibraryMaintenanceService) syncRootDeclarations(ctx context.Context, projectRoot string) (bool, error) {
	if s == nil || s.agentRepo == nil {
		return false, nil
	}
	s.declarationMu.Lock()
	defer s.declarationMu.Unlock()
	if err := s.EnsureGlobalAgents(ctx); err != nil {
		return false, err
	}
	if s.declarationCache == nil {
		s.declarationCache = make(map[string]declarationCacheEntry)
	}
	var hookStore agentlibrary.HookStore
	if s.lifecycleRepo != nil {
		hookStore = s.lifecycleRepo
	}
	applier := agentlibrary.NewRepoApplier(s.agentRepo, hookStore)
	projectChanged := !s.activeProjectRootKnown || s.activeProjectRoot != projectRoot
	globalResult, err := s.syncRootDeclarationsFromRoot(ctx, s.agentsRootPath, applier, projectChanged)
	if err != nil {
		return globalResult.changed, err
	}
	changed := globalResult.changed
	if projectRoot != "" {
		projectResult, syncErr := s.syncRootDeclarationsFromRoot(ctx, projectRoot, applier, projectChanged || changed)
		if syncErr != nil {
			return changed, syncErr
		}
		changed = changed || projectResult.changed
		for key := range projectResult.active {
			delete(projectResult.displaced, key)
		}
		restored, restoreErr := s.applyCachedGlobalDeclarations(ctx, applier, projectResult.displaced)
		if restoreErr != nil {
			return changed, restoreErr
		}
		changed = changed || restored
	}
	s.activeProjectRoot = projectRoot
	s.activeProjectRootKnown = true
	return changed, nil
}

func (s *AgentLibraryMaintenanceService) syncRootDeclarationsFromRoot(ctx context.Context, root string, applier *agentlibrary.RepoApplier, forceCached bool) (declarationRootSyncResult, error) {
	result := declarationRootSyncResult{active: map[string]struct{}{}, displaced: map[string]struct{}{}}
	if root == "" || applier == nil {
		return result, nil
	}
	agentsDir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			s.pruneMissingDeclarationPaths(agentsDir, nil, result.displaced)
			return result, nil
		}
		return result, fmt.Errorf("read agents root %s: %w", agentsDir, err)
	}
	currentPaths := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name(), "SKILLS.md")
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, fmt.Errorf("stat agent root declaration %s: %w", path, err)
		}
		currentPaths[path] = struct{}{}
		fingerprint := fingerprintDeclaration(info)
		cached, cachedOK := s.declarationCache[path]
		if cachedOK && cached.fingerprint == fingerprint {
			if cached.declaration != nil {
				result.active[cached.declaration.Agent.Key] = struct{}{}
			}
			if !forceCached || cached.declaration == nil {
				continue
			}
			applied, applyErr := applyCachedRootDeclaration(ctx, applier, cached.declaration, path)
			if applyErr != nil {
				return result, applyErr
			}
			result.changed = result.changed || applied
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return result, fmt.Errorf("read agent root declaration %s: %w", path, err)
		}
		s.declarationReads.Add(1)
		decl, body, err := agentlibrary.ParseDeclaration(string(data))
		s.declarationParses.Add(1)
		if err != nil || !decl.IsAgentRootDeclaration() || decl.Agent.Key == "" || decl.Agent.Key == "skill_curator" || decl.Agent.Key == "memory_curator" || decl.Agent.Key == "goal" {
			if cachedOK && cached.declaration != nil {
				result.displaced[cached.declaration.Agent.Key] = struct{}{}
			}
			s.declarationCache[path] = declarationCacheEntry{fingerprint: fingerprint}
			continue
		}
		if decl.Agent.SystemPrompt == "" {
			decl.Agent.SystemPrompt = strings.TrimSpace(body)
		}
		if cachedOK && cached.declaration != nil && cached.declaration.Agent.Key != decl.Agent.Key {
			result.displaced[cached.declaration.Agent.Key] = struct{}{}
		}
		result.active[decl.Agent.Key] = struct{}{}
		applied, applyErr := applyCachedRootDeclaration(ctx, applier, decl, path)
		if applyErr != nil {
			return result, applyErr
		}
		s.declarationCache[path] = declarationCacheEntry{fingerprint: fingerprint, declaration: decl}
		result.changed = result.changed || applied
	}
	s.pruneMissingDeclarationPaths(agentsDir, currentPaths, result.displaced)
	return result, nil
}

func (s *AgentLibraryMaintenanceService) pruneMissingDeclarationPaths(agentsDir string, currentPaths map[string]struct{}, displaced map[string]struct{}) {
	prefix := filepath.Clean(agentsDir) + string(os.PathSeparator)
	for path, cached := range s.declarationCache {
		if !strings.HasPrefix(filepath.Clean(path), prefix) {
			continue
		}
		if _, exists := currentPaths[path]; exists {
			continue
		}
		if cached.declaration != nil {
			displaced[cached.declaration.Agent.Key] = struct{}{}
		}
		delete(s.declarationCache, path)
	}
}

func (s *AgentLibraryMaintenanceService) applyCachedGlobalDeclarations(ctx context.Context, applier *agentlibrary.RepoApplier, keys map[string]struct{}) (bool, error) {
	if len(keys) == 0 || s.agentsRootPath == "" {
		return false, nil
	}
	prefix := filepath.Clean(filepath.Join(s.agentsRootPath, "agents")) + string(os.PathSeparator)
	paths := make([]string, 0)
	for path, cached := range s.declarationCache {
		if cached.declaration == nil || !strings.HasPrefix(filepath.Clean(path), prefix) {
			continue
		}
		if _, wanted := keys[cached.declaration.Agent.Key]; wanted {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	changed := false
	for _, path := range paths {
		applied, err := applyCachedRootDeclaration(ctx, applier, s.declarationCache[path].declaration, path)
		if err != nil {
			return changed, err
		}
		changed = changed || applied
	}
	return changed, nil
}

func applyCachedRootDeclaration(ctx context.Context, applier *agentlibrary.RepoApplier, decl *agentlibrary.SkillDeclaration, path string) (bool, error) {
	changes, err := applier.ApplyDeclaration(ctx, decl)
	if err != nil {
		return false, fmt.Errorf("apply agent root declaration %s: %w", path, err)
	}
	return len(changes) > 0, nil
}

func fingerprintDeclaration(info os.FileInfo) declarationFingerprint {
	fingerprint := declarationFingerprint{
		size:    info.Size(),
		modTime: info.ModTime().UnixNano(),
		mode:    info.Mode(),
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer && !value.IsNil() {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return fingerprint
	}
	for _, name := range []string{"Dev", "Ino", "Gen", "Ctim", "Ctimespec", "Birthtimespec"} {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			fingerprint.filesystem += fmt.Sprintf("%s=%v;", name, field.Interface())
		}
	}
	return fingerprint
}

// EnsureGlobalAgents reconciles protected system agents owned by the agent
// library subsystem independently from per-project scheduled maintenance tasks.
func (s *AgentLibraryMaintenanceService) EnsureGlobalAgents(ctx context.Context) error {
	if s == nil || s.agentRepo == nil {
		return nil
	}
	if _, err := s.ensureSkillCuratorAgent(ctx); err != nil {
		return err
	}
	if _, err := s.ensureGoalAgent(ctx); err != nil {
		return err
	}
	return nil
}

func (s *AgentLibraryMaintenanceService) EnsureProject(ctx context.Context, projectID string) error {
	if s == nil || s.taskRepo == nil || s.scheduleRepo == nil || s.agentRepo == nil {
		return nil
	}
	if err := s.EnsureGlobalAgents(ctx); err != nil {
		return err
	}
	agent, err := s.ensureSkillCuratorAgent(ctx)
	if err != nil {
		return err
	}
	task, err := s.taskRepo.GetByProjectAndTitle(ctx, projectID, agentLibraryMaintenanceTaskTitle)
	if err != nil {
		return err
	}
	if task == nil {
		agentID := agent.ID
		task = &models.Task{
			ProjectID:         projectID,
			Title:             agentLibraryMaintenanceTaskTitle,
			Category:          models.CategoryScheduled,
			Priority:          0,
			Status:            models.StatusPending,
			Prompt:            agentLibraryMaintenanceTaskPrompt,
			AgentDefinitionID: &agentID,
			Tag:               models.TagNone,
			ChainConfig:       "{}",
			CreatedVia:        models.TaskOriginWeb,
		}
		if err := s.taskRepo.Create(ctx, task); err != nil {
			if err != repository.ErrDuplicateTask {
				return fmt.Errorf("create skill library maintenance task: %w", err)
			}
			task, err = s.taskRepo.GetByProjectAndTitle(ctx, projectID, agentLibraryMaintenanceTaskTitle)
			if err != nil || task == nil {
				return err
			}
		}
	}
	if task.Prompt != agentLibraryMaintenanceTaskPrompt || task.Title != agentLibraryMaintenanceTaskTitle || task.Category != models.CategoryScheduled || task.AgentDefinitionID == nil || *task.AgentDefinitionID != agent.ID {
		agentID := agent.ID
		task.Title = agentLibraryMaintenanceTaskTitle
		task.Category = models.CategoryScheduled
		task.Prompt = agentLibraryMaintenanceTaskPrompt
		task.AgentDefinitionID = &agentID
		task.Tag = models.TagNone
		if task.ChainConfig == "" {
			task.ChainConfig = "{}"
		}
		if err := s.taskRepo.Update(ctx, task); err != nil {
			return fmt.Errorf("repair skill library maintenance task: %w", err)
		}
	}
	schedules, err := s.scheduleRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if len(schedules) > 0 {
		for _, schedule := range schedules {
			if schedule.ClearContextOnStart {
				continue
			}
			if err := s.scheduleRepo.UpdateClearContextOnStart(ctx, schedule.ID, task.ID, true); err != nil {
				return fmt.Errorf("repair skill library maintenance schedule clear context on start: %w", err)
			}
		}
		return nil
	}
	runAt := time.Now().UTC().Add(24 * time.Hour)
	return s.scheduleRepo.Create(ctx, &models.Schedule{
		TaskID:              task.ID,
		RunAt:               runAt,
		RepeatType:          models.RepeatDaily,
		RepeatInterval:      1,
		Enabled:             true,
		ClearContextOnStart: true,
		NextRun:             &runAt,
	})
}

func (s *AgentLibraryMaintenanceService) ensureSkillCuratorAgent(ctx context.Context) (*models.Agent, error) {
	decl, err := s.loadSkillCuratorDeclaration()
	if err != nil {
		return nil, err
	}
	want := systemAgentFromDeclaration(decl, systemAgentDeclarationSpec{
		systemKind:     models.AgentSystemKindSkillCurator,
		key:            models.AgentSystemKindSkillCurator,
		sourceRefs:     []string{bundledSkillCuratorDeclarationPath},
		permissionMode: systemAgentFullPermissions,
	})

	agent, err := s.agentRepo.GetBySystemKind(ctx, models.AgentSystemKindSkillCurator)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		agent = want
		if err := s.agentRepo.Create(ctx, agent); err != nil {
			return nil, err
		}
		if err := s.ensureSystemAgentHooks(ctx, agent.ID, decl); err != nil {
			return nil, err
		}
		return agent, nil
	}

	changed := applySystemAgentDeclaration(agent, want)
	if changed {
		if err := s.agentRepo.Update(ctx, agent); err != nil {
			return nil, err
		}
	}
	if err := s.ensureSystemAgentHooks(ctx, agent.ID, decl); err != nil {
		return nil, err
	}
	return agent, nil
}

func (s *AgentLibraryMaintenanceService) loadSkillCuratorDeclaration() (*agentlibrary.SkillDeclaration, error) {
	return s.loadBundledSystemAgentDeclaration(bundledSkillCuratorDeclarationPath, "skill_curator", "Skill Curator")
}

func (s *AgentLibraryMaintenanceService) loadGoalAgentDeclaration() (*agentlibrary.SkillDeclaration, error) {
	return s.loadBundledSystemAgentDeclaration(bundledGoalAgentDeclarationPath, models.AgentSystemKindGoal, "Goal Agent")
}

func (s *AgentLibraryMaintenanceService) loadBundledSystemAgentDeclaration(relPath, key, label string) (*agentlibrary.SkillDeclaration, error) {
	s.protectedDeclarationMu.Lock()
	defer s.protectedDeclarationMu.Unlock()
	if s.protectedDeclarations == nil {
		s.protectedDeclarations = make(map[string]declarationCacheEntry)
	}

	path := ""
	cacheKey := ""
	var fingerprint declarationFingerprint
	var data []byte
	var err error
	if strings.TrimSpace(s.agentsRootPath) != "" {
		path = filepath.Join(s.agentsRootPath, relPath)
		if info, statErr := os.Stat(path); statErr == nil {
			fingerprint = fingerprintDeclaration(info)
			cacheKey = "file:" + path
			if cached, ok := s.protectedDeclarations[cacheKey]; ok && cached.fingerprint == fingerprint {
				return cached.declaration, nil
			}
			data, err = os.ReadFile(path)
			if err == nil {
				s.declarationReads.Add(1)
			}
		}
	}
	if len(data) == 0 {
		path = filepath.ToSlash(filepath.Join("builtin", relPath))
		cacheKey = "builtin:" + path
		if cached, ok := s.protectedDeclarations[cacheKey]; ok {
			return cached.declaration, nil
		}
		data, err = builtinskills.FS.ReadFile(path)
		if err == nil {
			s.declarationReads.Add(1)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("read %s declaration %s: %w", label, path, err)
	}
	decl, body, err := agentlibrary.ParseDeclaration(string(data))
	s.declarationParses.Add(1)
	if err != nil {
		return nil, fmt.Errorf("parse %s declaration %s: %w", label, path, err)
	}
	if decl.Agent.Key != key {
		return nil, fmt.Errorf("%s declaration must declare %s, got %s", label, key, decl.Agent.Key)
	}
	if decl.Agent.SystemPrompt == "" {
		decl.Agent.SystemPrompt = strings.TrimSpace(body)
	}
	decl.Skill.Key = ""
	if key == models.AgentSystemKindSkillCurator {
		sanitizeSystemSkillCuratorDeclaration(decl)
	}
	s.protectedDeclarations[cacheKey] = declarationCacheEntry{fingerprint: fingerprint, declaration: decl}
	return decl, nil
}

func (s *AgentLibraryMaintenanceService) ensureGoalAgent(ctx context.Context) (*models.Agent, error) {
	decl, err := s.loadGoalAgentDeclaration()
	if err != nil {
		return nil, err
	}
	want := systemAgentFromDeclaration(decl, systemAgentDeclarationSpec{
		systemKind:     models.AgentSystemKindGoal,
		key:            models.AgentSystemKindGoal,
		sourceRefs:     []string{bundledGoalAgentDeclarationPath},
		permissionMode: systemAgentFullPermissions,
	})

	agent, err := s.agentRepo.GetBySystemKind(ctx, models.AgentSystemKindGoal)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		agent, err = s.agentRepo.GetByKey(ctx, models.AgentSystemKindGoal)
		if err != nil {
			return nil, err
		}
	}
	if agent == nil {
		agent = want
		if err := s.agentRepo.Create(ctx, agent); err != nil {
			return nil, err
		}
		if err := s.ensureSystemAgentHooks(ctx, agent.ID, decl); err != nil {
			return nil, err
		}
		return agent, nil
	}
	if applySystemAgentDeclaration(agent, want) {
		if err := s.agentRepo.Update(ctx, agent); err != nil {
			return nil, err
		}
	}
	if err := s.ensureSystemAgentHooks(ctx, agent.ID, decl); err != nil {
		return nil, err
	}
	return agent, nil
}

func sanitizeSystemSkillCuratorDeclaration(decl *agentlibrary.SkillDeclaration) {
	if decl == nil || decl.Agent.Key != "skill_curator" {
		return
	}
	decl.Tools = removeStrings(compactStrings(decl.Tools), models.AgentToolScopedFiles, "agent_manage")
	decl.ToolConfig = agentlibrary.ToolConfigBlock{}
	decl.Permissions.WriteAgents = false
	decl.Permissions.ReadRepositoryFiles = false
	decl.Permissions.WriteRepositoryFiles = false
}

func (s *AgentLibraryMaintenanceService) ensureSystemAgentHooks(ctx context.Context, agentID string, decl *agentlibrary.SkillDeclaration) error {
	_, err := reconcileDeclaredSystemAgentLifecycleHooks(ctx, s.lifecycleRepo, agentID, decl)
	return err
}

func skillsFromBundledAgentIndex(indexBody string) []models.SkillConfig {
	skills := []models.SkillConfig{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(indexBody, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "## ") {
			continue
		}
		header := strings.TrimSpace(strings.TrimPrefix(line, "## "))
		if i := strings.Index(header, "/"); i >= 0 {
			header = header[i+1:]
		}
		header = strings.TrimSpace(header)
		if header == "" {
			continue
		}
		if _, ok := seen[header]; ok {
			continue
		}
		seen[header] = struct{}{}
		skills = append(skills, models.SkillConfig{Name: header})
	}
	return skills
}

func lifecycleHookFromAgentDeclaration(agentID string, when models.LifecycleWhen, h agentlibrary.HookDecl) *models.AgentLifecycleHook {
	permissions, _ := json.Marshal(h.Permissions)
	runPolicy, _ := json.Marshal(map[string]any{"when": firstNonEmptyString(h.RunPolicy, "always")})
	var schedule string
	if cron := firstNonEmptyString(h.ScheduleCron, h.Schedule); cron != "" {
		b, _ := json.Marshal(map[string]string{"cron": cron})
		schedule = string(b)
	}
	enabled := true
	if h.Enabled != nil {
		enabled = *h.Enabled
	}
	payload := ""
	if len(h.Payload) > 0 {
		b, _ := json.Marshal(map[string]any{"blocks": h.Payload})
		payload = string(b)
	}
	return &models.AgentLifecycleHook{
		AgentID:         agentID,
		When:            when,
		SkillKey:        firstNonEmptyString(h.Skill, "maintain_skill_library"),
		PromptOverride:  h.PromptOverride,
		OutputContract:  models.LifecycleOutputContract(h.OutputContract),
		Blocking:        h.Blocking,
		Enabled:         enabled,
		PermissionsJSON: string(permissions),
		RunPolicyJSON:   string(runPolicy),
		ScheduleJSON:    schedule,
		PayloadJSON:     payload,
	}
}

func findAgentLifecycleHook(hooks []models.AgentLifecycleHook, when models.LifecycleWhen, skill string) *models.AgentLifecycleHook {
	for i := range hooks {
		if hooks[i].When == when && hooks[i].SkillKey == skill {
			return &hooks[i]
		}
	}
	return nil
}

// normalizedHookPayloadJSON collapses the two ways "this hook declared no
// payload" is spelled: an empty string from a declaration, and the "{}" default
// the hooks table stores. Without this, every warm sync of an undeclared hook
// would look like a change and rewrite the row.
func normalizedHookPayloadJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "{}" {
		return ""
	}
	return raw
}

func sameLifecycleHook(a *models.AgentLifecycleHook, b *models.AgentLifecycleHook) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.AgentID == b.AgentID &&
		a.When == b.When &&
		a.SkillKey == b.SkillKey &&
		a.PromptOverride == b.PromptOverride &&
		a.OutputContract == b.OutputContract &&
		a.Blocking == b.Blocking &&
		a.Enabled == b.Enabled &&
		strings.TrimSpace(a.PermissionsJSON) == strings.TrimSpace(b.PermissionsJSON) &&
		strings.TrimSpace(a.RunPolicyJSON) == strings.TrimSpace(b.RunPolicyJSON) &&
		strings.TrimSpace(a.ScheduleJSON) == strings.TrimSpace(b.ScheduleJSON) &&
		normalizedHookPayloadJSON(a.PayloadJSON) == normalizedHookPayloadJSON(b.PayloadJSON)
}

func toolConfigFromAgentDeclaration(cfg agentlibrary.ToolConfigBlock) models.AgentToolConfig {
	out := models.AgentToolConfig{
		SkipDefaultTools:       cfg.SkipDefaultTools,
		DisableRuntimeWorktree: cfg.DisableRuntimeWorktree,
	}
	for _, scope := range cfg.ScopedFiles {
		out.ScopedFiles = append(out.ScopedFiles, models.ScopedFilesConfig{
			Directory:   strings.TrimSpace(scope.Directory),
			Permissions: append([]string(nil), scope.Permissions...),
		})
	}
	return out
}

func mcpServersFromAgentDeclaration(servers []string) []models.MCPServerConfig {
	if len(servers) == 0 {
		return []models.MCPServerConfig{}
	}
	out := make([]models.MCPServerConfig, 0, len(servers))
	for _, name := range servers {
		name = strings.TrimSpace(name)
		if name != "" {
			out = append(out, models.MCPServerConfig{Name: name})
		}
	}
	return out
}

// sameAgentToolsList compares two tool lists in order. Order matters here
// because the bundled declaration is canonical and any deviation should trigger a
// repair write.
func sameAgentToolsList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameSkillConfigs(a, b []models.SkillConfig) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Description != b[i].Description || a[i].Content != b[i].Content || a[i].Tools != b[i].Tools {
			return false
		}
	}
	return true
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func compactStrings(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func removeStrings(in []string, denied ...string) []string {
	if len(in) == 0 || len(denied) == 0 {
		return in
	}
	blocked := make(map[string]struct{}, len(denied))
	for _, value := range denied {
		blocked[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	out := make([]string, 0, len(in))
	for _, value := range in {
		if _, ok := blocked[strings.ToLower(strings.TrimSpace(value))]; ok {
			continue
		}
		out = append(out, value)
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
