package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

type AgentLibraryMaintenanceService struct {
	taskRepo       *repository.TaskRepo
	scheduleRepo   *repository.ScheduleRepo
	agentRepo      *repository.AgentRepo
	lifecycleRepo  *repository.LifecycleRepo
	agentsRootPath string

	declarationMu     sync.Mutex
	declarationCache  map[string]declarationFingerprint
	declarationReads  atomic.Uint64
	declarationParses atomic.Uint64
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
		s.declarationCache = make(map[string]declarationFingerprint)
	}
	applier := agentlibrary.NewRepoApplier(s.agentRepo, s.lifecycleRepo)
	changed := false
	for _, root := range compactStrings([]string{s.agentsRootPath, projectRoot}) {
		rootChanged, err := s.syncRootDeclarationsFromRoot(ctx, root, applier)
		if err != nil {
			return changed, err
		}
		changed = changed || rootChanged
	}
	return changed, nil
}

func (s *AgentLibraryMaintenanceService) syncRootDeclarationsFromRoot(ctx context.Context, root string, applier *agentlibrary.RepoApplier) (bool, error) {
	if root == "" || applier == nil {
		return false, nil
	}
	agentsDir := filepath.Join(root, "agents")
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read agents root %s: %w", agentsDir, err)
	}
	changed := false
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(agentsDir, entry.Name(), "SKILLS.md")
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				delete(s.declarationCache, path)
				continue
			}
			return changed, fmt.Errorf("stat agent root declaration %s: %w", path, err)
		}
		fingerprint := fingerprintDeclaration(info)
		if cached, ok := s.declarationCache[path]; ok && cached == fingerprint {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				delete(s.declarationCache, path)
				continue
			}
			return changed, fmt.Errorf("read agent root declaration %s: %w", path, err)
		}
		s.declarationReads.Add(1)
		decl, body, err := agentlibrary.ParseDeclaration(string(data))
		s.declarationParses.Add(1)
		if err != nil {
			s.declarationCache[path] = fingerprint
			continue
		}
		if !decl.IsAgentRootDeclaration() || decl.Agent.Key == "" {
			s.declarationCache[path] = fingerprint
			continue
		}
		if decl.Agent.Key == "skill_curator" || decl.Agent.Key == "memory_curator" || decl.Agent.Key == "goal" {
			s.declarationCache[path] = fingerprint
			continue
		}
		if decl.Agent.SystemPrompt == "" {
			decl.Agent.SystemPrompt = strings.TrimSpace(body)
		}
		changes, err := applier.ApplyDeclaration(ctx, decl)
		if err != nil {
			return changed, fmt.Errorf("apply agent root declaration %s: %w", path, err)
		}
		s.declarationCache[path] = fingerprint
		changed = changed || len(changes) > 0
	}
	return changed, nil
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
	want := agentFromBundledDeclaration(decl)

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

	changed := applyBundledSkillCuratorDeclaration(agent, want)
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
	decl, err := s.loadBundledSystemAgentDeclaration(bundledSkillCuratorDeclarationPath, "skill_curator", "Skill Curator")
	if err != nil {
		return nil, err
	}
	sanitizeSystemSkillCuratorDeclaration(decl)
	return decl, nil
}

func (s *AgentLibraryMaintenanceService) loadGoalAgentDeclaration() (*agentlibrary.SkillDeclaration, error) {
	return s.loadBundledSystemAgentDeclaration(bundledGoalAgentDeclarationPath, models.AgentSystemKindGoal, "Goal Agent")
}

func (s *AgentLibraryMaintenanceService) loadBundledSystemAgentDeclaration(relPath, key, label string) (*agentlibrary.SkillDeclaration, error) {
	path := ""
	var data []byte
	var err error
	if strings.TrimSpace(s.agentsRootPath) != "" {
		path = filepath.Join(s.agentsRootPath, relPath)
		data, err = os.ReadFile(path)
	}
	if len(data) == 0 {
		path = filepath.ToSlash(filepath.Join("builtin", relPath))
		data, err = builtinskills.FS.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s declaration %s: %w", label, path, err)
	}
	decl, body, err := agentlibrary.ParseDeclaration(string(data))
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
	return decl, nil
}

func (s *AgentLibraryMaintenanceService) ensureGoalAgent(ctx context.Context) (*models.Agent, error) {
	decl, err := s.loadGoalAgentDeclaration()
	if err != nil {
		return nil, err
	}
	want := agentFromBundledDeclaration(decl)
	want.SystemKind = models.AgentSystemKindGoal
	want.Key = models.AgentSystemKindGoal
	want.SourceRefs = []string{bundledGoalAgentDeclarationPath}

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
	if applyBundledSkillCuratorDeclaration(agent, want) {
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

func agentFromBundledDeclaration(decl *agentlibrary.SkillDeclaration) *models.Agent {
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
		SystemKind:          models.AgentSystemKindSkillCurator,
		Key:                 "skill_curator",
		Scope:               models.AgentScope(firstNonEmptyString(decl.Agent.Scope, string(models.AgentScopeGlobal))),
		ProjectID:           decl.Agent.ProjectID,
		SelectableAsPrimary: decl.Agent.SelectableAsPrimary,
		Enabled:             true,
		GeneratedStatus:     models.AgentStatusProtected,
		CreatedBy:           models.AgentCreatedBySystem,
		PermissionDefaults: models.AgentPermissionDefaults{
			ReadTaskPrompt:       decl.Permissions.ReadTaskPrompt,
			ReadTaskExecution:    decl.Permissions.ReadTaskExecution,
			ReadProjectMemory:    decl.Permissions.ReadProjectMemory,
			WriteProjectMemory:   decl.Permissions.WriteProjectMemory,
			ReadAgents:           decl.Permissions.ReadAgents,
			WriteAgents:          decl.Permissions.WriteAgents,
			ReadSkills:           decl.Permissions.ReadSkills,
			WriteSkills:          decl.Permissions.WriteSkills,
			ReadRepositoryFiles:  decl.Permissions.ReadRepositoryFiles,
			WriteRepositoryFiles: decl.Permissions.WriteRepositoryFiles,
			UseShellOrTools:      decl.Permissions.UseShellOrTools,
		},
		ModelDefaults: models.AgentModelDefaults{
			Model:       decl.ModelDefaults.Model,
			Temperature: decl.ModelDefaults.Temperature,
			MaxTokens:   decl.ModelDefaults.MaxTokens,
		},
		SourceRefs: []string{bundledSkillCuratorDeclarationPath},
	}
	if decl.Agent.Enabled != nil {
		agent.Enabled = *decl.Agent.Enabled
	}
	return agent
}

func applyBundledSkillCuratorDeclaration(agent, want *models.Agent) bool {
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
	set(agent.Key == "" || agent.Key == "sys_skill_curator" || agent.Key != want.Key, func() { agent.Key = want.Key })
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

func (s *AgentLibraryMaintenanceService) ensureSystemAgentHooks(ctx context.Context, agentID string, decl *agentlibrary.SkillDeclaration) error {
	if s.lifecycleRepo == nil || decl == nil || len(decl.LifecycleHooks) == 0 {
		return nil
	}
	existing, err := s.lifecycleRepo.HooksByAgent(ctx, agentID)
	if err != nil {
		return err
	}
	for when, hookDecl := range decl.LifecycleHooks {
		if when == "primary" {
			continue
		}
		hook := lifecycleHookFromAgentDeclaration(agentID, models.LifecycleWhen(when), hookDecl)
		match := findAgentLifecycleHook(existing, hook.When, hook.SkillKey)
		if match == nil {
			if err := s.lifecycleRepo.CreateHook(ctx, hook); err != nil {
				return err
			}
			continue
		}
		hook.ID = match.ID
		if !sameLifecycleHook(match, hook) {
			if err := s.lifecycleRepo.UpdateHook(ctx, hook); err != nil {
				return err
			}
		}
	}
	return nil
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
		strings.TrimSpace(a.ScheduleJSON) == strings.TrimSpace(b.ScheduleJSON)
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
