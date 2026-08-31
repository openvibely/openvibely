package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/agentlibrary"
	"github.com/openvibely/openvibely/internal/builtinskills"
	"github.com/openvibely/openvibely/internal/memory"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// MemoryService owns storage setup and reconciliation for OpenVibely's
// managed memory subsystem. The actual memory work (selecting relevant
// memories on task start, updating memories on task completion, and
// periodic consolidation) is delegated to the built-in System: Memory
// Curator agent and its on-disk skills via lifecycle hooks plus a normal
// scheduled task. This service exists to:
//
//   - Resolve the per-project repo-local managed memory directory and
//     migrate any legacy .openvibely/memory directory to .openvibely/memories,
//     then migrate any legacy MEMORY.md to MEMORIES.md.
//   - Reconcile the protected Memory Curator agent DB row from its
//     embedded on-disk declaration (mirroring SkillCuratorService).
//   - Reconcile the Memory Curator lifecycle hook rows from that
//     declaration so route_task / after_complete bindings exist.//   - Create and maintain the visible per-project "System: Memory
//     Consolidation" scheduled task assigned to the Memory Curator agent.
type MemoryService struct {
	taskRepo      *repository.TaskRepo
	scheduleRepo  *repository.ScheduleRepo
	agentRepo     *repository.AgentRepo
	lifecycleRepo *repository.LifecycleRepo
	projectRepo   *repository.ProjectRepo
	store         *memory.FileStore
	pathResolver  *memory.PathResolver
}

// NewMemoryService builds a memory service. Only the dependencies needed
// for storage setup, agent/hook reconciliation, and scheduled-task wiring
// are required; task-time memory behavior runs through the Memory Curator
// agent and is independent of this service.
func NewMemoryService(
	taskRepo *repository.TaskRepo,
	scheduleRepo *repository.ScheduleRepo,
	agentRepo *repository.AgentRepo,
	projectRepo *repository.ProjectRepo,
	store *memory.FileStore,
	resolver *memory.PathResolver,
) *MemoryService {
	return &MemoryService{
		taskRepo:     taskRepo,
		scheduleRepo: scheduleRepo,
		agentRepo:    agentRepo,
		projectRepo:  projectRepo,
		store:        store,
		pathResolver: resolver,
	}
}

// SetLifecycleRepo wires lifecycle hook persistence for the built-in Memory Curator agent.
func (s *MemoryService) SetLifecycleRepo(repo *repository.LifecycleRepo) {
	if s != nil {
		s.lifecycleRepo = repo
	}
}

// EnsureProject ensures the on-disk memory directory exists, that any legacy
// .openvibely/memory directory has been migrated to .openvibely/memories, that
// any legacy MEMORY.md has been migrated to MEMORIES.md, and that the per-project
// Memory Consolidation scheduled task is wired to the Memory Curator agent.
// Idempotent on every server boot and project creation.
func (s *MemoryService) EnsureProject(ctx context.Context, projectID string) error {
	if err := s.EnsureGlobalAgents(ctx); err != nil {
		return err
	}
	if _, err := s.ensureProjectMemoryDir(ctx, projectID); err != nil {
		return err
	}
	return s.ensureConsolidationTaskSchedule(ctx, projectID)
}

// EnsureGlobalAgents reconciles protected system Memory Curator identity and
// lifecycle hooks independently from per-project memory-file setup. Startup calls
// this even before any project-specific memory directories are available so the
// built-in agent is always visible on fresh installs.
func (s *MemoryService) EnsureGlobalAgents(ctx context.Context) error {
	if s == nil || s.agentRepo == nil {
		return nil
	}
	_, err := s.ensureMemoryAgent(ctx)
	return err
}

// EnsureProjectSchedules reconciles the visible per-project Memory Consolidation
// scheduled task without requiring the project to have a local repo_path. The
// scheduled task itself is normal task/schedule state; runtime memory-file access
// is validated when the task executes.
func (s *MemoryService) EnsureProjectSchedules(ctx context.Context, projectID string) error {
	if err := s.EnsureGlobalAgents(ctx); err != nil {
		return err
	}
	return s.ensureConsolidationTaskSchedule(ctx, projectID)
}

func (s *MemoryService) ensureProjectMemoryDir(ctx context.Context, projectID string) (string, error) {
	if err := s.refreshProjectMemoryDir(ctx, projectID); err != nil {
		return "", err
	}
	return s.store.EnsureProject(projectID)
}

func (s *MemoryService) refreshProjectMemoryDir(ctx context.Context, projectID string) error {
	if s.projectRepo == nil || s.pathResolver == nil {
		return nil
	}
	project, err := s.projectRepo.GetByID(ctx, projectID)
	if err != nil || project == nil {
		return err
	}
	if strings.TrimSpace(project.RepoPath) == "" {
		return fmt.Errorf("memory: project %s has no local repo_path", projectID)
	}
	if err := migrateLegacyMemoryDir(project.RepoPath); err != nil {
		return err
	}
	dir, err := memory.SharedRepoMemoryDir(project.RepoPath)
	if err != nil {
		return err
	}
	return s.pathResolver.SetProjectDirOverride(projectID, dir)
}

func migrateLegacyMemoryDir(repoPath string) error {
	legacyDir, err := memory.SharedRepoLegacyMemoryDir(repoPath)
	if err != nil {
		return err
	}
	newDir, err := memory.SharedRepoMemoryDir(repoPath)
	if err != nil {
		return err
	}
	if legacyDir == newDir {
		return nil
	}
	legacyInfo, err := os.Stat(legacyDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat legacy memory dir %s: %w", legacyDir, err)
	}
	if !legacyInfo.IsDir() {
		return fmt.Errorf("legacy memory path %s is not a directory", legacyDir)
	}
	newInfo, err := os.Stat(newDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat memory dir %s: %w", newDir, err)
		}
		if err := os.MkdirAll(filepath.Dir(newDir), 0o755); err != nil {
			return fmt.Errorf("create memory parent dir %s: %w", filepath.Dir(newDir), err)
		}
		if err := os.Rename(legacyDir, newDir); err != nil {
			return fmt.Errorf("migrate legacy memory dir %s to %s: %w", legacyDir, newDir, err)
		}
		return nil
	}
	if !newInfo.IsDir() {
		return fmt.Errorf("memory path %s is not a directory", newDir)
	}
	if err := mergeLegacyMemoryDir(legacyDir, newDir); err != nil {
		return err
	}
	if err := os.Remove(legacyDir); err != nil {
		return fmt.Errorf("remove migrated legacy memory dir %s: %w", legacyDir, err)
	}
	return nil
}

func mergeLegacyMemoryDir(legacyDir, newDir string) error {
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		return fmt.Errorf("read legacy memory dir %s: %w", legacyDir, err)
	}
	for _, entry := range entries {
		legacyPath := filepath.Join(legacyDir, entry.Name())
		newPath := filepath.Join(newDir, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(newPath, 0o755); err != nil {
				return fmt.Errorf("create migrated memory subdir %s: %w", newPath, err)
			}
			if err := mergeLegacyMemoryDir(legacyPath, newPath); err != nil {
				return err
			}
			if err := os.Remove(legacyPath); err != nil {
				return fmt.Errorf("remove migrated legacy memory subdir %s: %w", legacyPath, err)
			}
			continue
		}
		if _, err := os.Stat(newPath); err == nil {
			if err := moveConflictingLegacyMemoryFile(legacyPath, newDir, entry.Name()); err != nil {
				return err
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat memory file %s: %w", newPath, err)
		}
		if err := os.Rename(legacyPath, newPath); err != nil {
			return fmt.Errorf("migrate legacy memory file %s to %s: %w", legacyPath, newPath, err)
		}
	}
	return nil
}

func moveConflictingLegacyMemoryFile(legacyPath, newDir, name string) error {
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil {
		return fmt.Errorf("read legacy memory file %s: %w", legacyPath, err)
	}
	newPath := filepath.Join(newDir, name)
	newData, err := os.ReadFile(newPath)
	if err != nil {
		return fmt.Errorf("read memory file %s: %w", newPath, err)
	}
	if string(legacyData) == string(newData) {
		if err := os.Remove(legacyPath); err != nil {
			return fmt.Errorf("remove duplicate legacy memory file %s: %w", legacyPath, err)
		}
		return nil
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	ext := filepath.Ext(name)
	if base == "" {
		base = name
		ext = ""
	}
	for i := 1; ; i++ {
		candidate := filepath.Join(newDir, fmt.Sprintf("%s.legacy%d%s", base, i, ext))
		if _, err := os.Stat(candidate); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat migrated legacy memory conflict %s: %w", candidate, err)
		}
		if err := os.Rename(legacyPath, candidate); err != nil {
			return fmt.Errorf("preserve conflicting legacy memory file %s as %s: %w", legacyPath, candidate, err)
		}
		return nil
	}
}

const memoryAgentName = "System: Memory Curator"
const memoryConsolidationTaskTitle = "System: Memory Consolidation"
const bundledMemoryDeclarationPath = "agents/memory_curator/SKILLS.md"

const memoryConsolidationTaskPrompt = `Consolidate this project's durable memory.

Use the scoped file tools to inspect and update the managed memory directory. Keep MEMORIES.md as the compact index. Merge duplicate or stale topic files. Preserve durable project facts, preferences, architecture decisions, workflow constraints, current-state facts, incidents, and repeated feedback. Do not store transient logs, raw transcripts, secrets, task-by-task summaries, or procedure-only runbooks.

When done, respond with a short summary of what changed.`

func sameScopedToolConfig(a, b models.AgentToolConfig) bool {
	if a.SkipDefaultTools != b.SkipDefaultTools || a.DisableRuntimeWorktree != b.DisableRuntimeWorktree || len(a.ScopedFiles) != len(b.ScopedFiles) {
		return false
	}
	for i := range a.ScopedFiles {
		if a.ScopedFiles[i].Directory != b.ScopedFiles[i].Directory || strings.Join(a.ScopedFiles[i].Permissions, ",") != strings.Join(b.ScopedFiles[i].Permissions, ",") {
			return false
		}
	}
	return true
}

func (s *MemoryService) ensureConsolidationTaskSchedule(ctx context.Context, projectID string) error {
	if s.taskRepo == nil || s.scheduleRepo == nil || s.agentRepo == nil {
		return nil
	}
	agent, err := s.ensureMemoryAgent(ctx)
	if err != nil {
		return err
	}
	task, err := s.taskRepo.GetByProjectAndTitle(ctx, projectID, memoryConsolidationTaskTitle)
	if err != nil {
		return err
	}
	if task == nil {
		agentID := agent.ID
		task = &models.Task{
			ProjectID:         projectID,
			Title:             memoryConsolidationTaskTitle,
			Category:          models.CategoryScheduled,
			Priority:          0,
			Status:            models.StatusPending,
			Prompt:            memoryConsolidationTaskPrompt,
			AgentDefinitionID: &agentID,
			Tag:               models.TagNone,
			ChainConfig:       "{}",
			CreatedVia:        models.TaskOriginWeb,
		}
		if err := s.taskRepo.Create(ctx, task); err != nil {
			if err != repository.ErrDuplicateTask {
				return err
			}
			task, err = s.taskRepo.GetByProjectAndTitle(ctx, projectID, memoryConsolidationTaskTitle)
			if err != nil || task == nil {
				return err
			}
		}
	}
	if task.Prompt != memoryConsolidationTaskPrompt || task.Title != memoryConsolidationTaskTitle || task.Category != models.CategoryScheduled || task.AgentDefinitionID == nil || *task.AgentDefinitionID != agent.ID {
		agentID := agent.ID
		task.Title = memoryConsolidationTaskTitle
		task.Category = models.CategoryScheduled
		task.Prompt = memoryConsolidationTaskPrompt
		task.AgentDefinitionID = &agentID
		task.Tag = models.TagNone
		if task.ChainConfig == "" {
			task.ChainConfig = "{}"
		}
		if err := s.taskRepo.Update(ctx, task); err != nil {
			return err
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
				return err
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

func (s *MemoryService) ensureMemoryAgent(ctx context.Context) (*models.Agent, error) {
	decl, err := loadBundledMemoryDeclaration()
	if err != nil {
		return nil, err
	}
	want := systemAgentFromDeclaration(decl, systemAgentDeclarationSpec{
		systemKind:     models.AgentSystemKindMemoryCurator,
		key:            models.AgentSystemKindMemoryCurator,
		sourceRefs:     []string{bundledMemoryDeclarationPath},
		permissionMode: systemAgentMemoryPermissions,
	})

	agent, err := s.findCanonicalMemoryAgent(ctx)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		agent = want
		if err := s.agentRepo.Create(ctx, agent); err != nil {
			return nil, err
		}
	} else if applySystemAgentDeclaration(agent, want) {
		if err := s.agentRepo.Update(ctx, agent); err != nil {
			return nil, err
		}
	}
	if err := s.deleteLegacyMemoryAgents(ctx, agent.ID); err != nil {
		return nil, err
	}
	if err := s.ensureMemoryHooks(ctx, agent.ID, decl); err != nil {
		return nil, err
	}
	return agent, nil
}

func (s *MemoryService) findCanonicalMemoryAgent(ctx context.Context) (*models.Agent, error) {
	if s.agentRepo == nil {
		return nil, nil
	}
	agents, err := s.agentRepo.ListBySystemKind(ctx, models.AgentSystemKindMemoryCurator)
	if err != nil {
		return nil, err
	}
	if len(agents) > 0 {
		return &agents[0], nil
	}
	if agent, err := s.agentRepo.GetByKey(ctx, "memory_curator"); err != nil || agent != nil {
		return agent, err
	}
	if agent, err := s.agentRepo.GetByKey(ctx, "memory"); err != nil || agent != nil {
		return agent, err
	}
	return s.agentRepo.GetByName(ctx, memoryAgentName)
}

func (s *MemoryService) deleteLegacyMemoryAgents(ctx context.Context, canonicalID string) error {
	if s.agentRepo == nil || canonicalID == "" {
		return nil
	}
	agents, err := s.agentRepo.ListIncludingArchived(ctx)
	if err != nil {
		return err
	}
	for i := range agents {
		agent := &agents[i]
		if agent.ID == canonicalID || !isSupersededMemoryAgent(agent) {
			continue
		}
		if err := s.agentRepo.Delete(ctx, agent.ID); err != nil {
			return err
		}
	}
	return nil
}

func isSupersededMemoryAgent(agent *models.Agent) bool {
	if agent == nil {
		return false
	}
	switch agent.SystemKind {
	case models.AgentSystemKindMemoryCurator, "memory", "memory_consolidator":
		return true
	}
	switch agent.Key {
	case "memory_curator", "memory", "memory_consolidator":
		return true
	}
	switch agent.Name {
	case memoryAgentName, "System: Memory", "System: Memory Consolidator":
		return true
	}
	return false
}

func loadBundledMemoryDeclaration() (*agentlibrary.SkillDeclaration, error) {
	path := filepath.ToSlash(filepath.Join("builtin", bundledMemoryDeclarationPath))
	data, err := builtinskills.FS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Memory declaration %s: %w", path, err)
	}
	decl, body, err := agentlibrary.ParseDeclaration(string(data))
	if err != nil {
		return nil, fmt.Errorf("parse Memory declaration %s: %w", path, err)
	}
	if decl.Agent.Key != "memory_curator" {
		return nil, fmt.Errorf("Memory declaration must declare memory_curator, got %s", decl.Agent.Key)
	}
	if decl.Agent.SystemPrompt == "" {
		decl.Agent.SystemPrompt = strings.TrimSpace(body)
	}
	decl.Skill.Key = ""
	return decl, nil
}

func (s *MemoryService) ensureMemoryHooks(ctx context.Context, agentID string, decl *agentlibrary.SkillDeclaration) error {
	result, err := reconcileDeclaredSystemAgentLifecycleHooks(ctx, s.lifecycleRepo, agentID, decl)
	if err != nil {
		return err
	}
	for _, hook := range result.existing {
		if _, ok := result.desired[string(hook.When)+"/"+hook.SkillKey]; ok {
			continue
		}
		if hook.AgentID == agentID && hook.SkillKey == "recall_memory" && hook.When == models.LifecycleBeforeRun {
			if err := s.lifecycleRepo.DeleteHook(ctx, hook.ID); err != nil {
				return err
			}
			continue
		}
		if hook.AgentID == agentID && hook.SkillKey == "consolidate_memory" && hook.When == models.LifecycleScheduled {
			if err := s.lifecycleRepo.DeleteHook(ctx, hook.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
