package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/attachmentsession"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type ManagedProjectRepoResolver interface {
	IsManagedProjectRepo(ctx context.Context, projectID, repoPath string) (bool, error)
}

type ProjectService struct {
	repo                          *repository.ProjectRepo
	taskSvc                       *TaskService
	workerRepo                    *repository.WorkerRepo
	managedRepoResolver           ManagedProjectRepoResolver
	beforeRelationalDeleteForTest func()
}

type projectDeletionMove struct {
	source     string
	quarantine string
}

type projectDeletionFileStage struct {
	moves          []projectDeletionMove
	branches       []string
	sessionUnlocks []func()
	localRepoPath  string
	pruneWorktrees bool
}

type ProjectDeletionCleanupError struct {
	Err error
}

func (e *ProjectDeletionCleanupError) Error() string {
	return fmt.Sprintf("project deleted but filesystem cleanup failed: %v", e.Err)
}

func (e *ProjectDeletionCleanupError) Unwrap() error {
	return e.Err
}

func NewProjectService(repo *repository.ProjectRepo) *ProjectService {
	return &ProjectService{repo: repo}
}

func (s *ProjectService) SetTaskService(taskSvc *TaskService) {
	s.taskSvc = taskSvc
}

func (s *ProjectService) SetManagedProjectRepoResolver(resolver ManagedProjectRepoResolver) {
	s.managedRepoResolver = resolver
}

func (s *ProjectService) SetWorkerRepo(workerRepo *repository.WorkerRepo) {
	s.workerRepo = workerRepo
}

func (s *ProjectService) List(ctx context.Context) ([]models.Project, error) {
	return s.repo.List(ctx)
}

// ListAPIProjects returns the compact project projection used by GET
// /api/projects. Full project callers must continue using List or GetByID.
func (s *ProjectService) ListAPIProjects(ctx context.Context) ([]models.ProjectAPIItem, error) {
	return s.repo.ListAPIProjects(ctx)
}

// ListWorkerCapacityProjects returns the compact project projection used to
// construct Workers capacity rows. Full project callers must use List or GetByID.
func (s *ProjectService) ListWorkerCapacityProjects(ctx context.Context) ([]models.ProjectWorkerCapacity, error) {
	return s.repo.ListWorkerCapacityProjects(ctx)
}

// ListSelectorOptions returns a compact project projection (id, name,
// is_default) for shared page-shell selector rendering and current-project
// fallback. Callers that need full project records must use List or GetByID.
func (s *ProjectService) ListSelectorOptions(ctx context.Context) ([]models.Project, error) {
	return s.repo.ListSelectorOptions(ctx)
}

func (s *ProjectService) GetByID(ctx context.Context, id string) (*models.Project, error) {
	return s.repo.GetByID(ctx, id)
}

func (s *ProjectService) Create(ctx context.Context, p *models.Project) error {
	if err := s.validateProjectWorkerLimit(ctx, p, false); err != nil {
		return err
	}
	return s.repo.Create(ctx, p)
}

func (s *ProjectService) Update(ctx context.Context, p *models.Project) error {
	if err := s.validateProjectWorkerLimit(ctx, p, true); err != nil {
		return err
	}
	return s.repo.Update(ctx, p)
}

func (s *ProjectService) validateProjectWorkerLimit(ctx context.Context, p *models.Project, allowUnchanged bool) error {
	if p.MaxWorkers != nil && *p.MaxWorkers == 0 {
		p.MaxWorkers = nil
	}

	globalMaxWorkers := 0
	if s.workerRepo != nil {
		var err error
		globalMaxWorkers, err = s.workerRepo.GetMaxWorkers(ctx)
		if err != nil {
			return err
		}
		if err := models.ValidateGlobalWorkerLimit(globalMaxWorkers); err != nil {
			return err
		}
	}

	if allowUnchanged && s.workerRepo != nil {
		current, err := s.repo.GetByID(ctx, p.ID)
		if err != nil {
			return err
		}
		if current != nil && projectWorkerLimitsEqual(current.MaxWorkers, p.MaxWorkers) {
			return nil
		}
	}
	return models.ValidateProjectWorkerLimit(p.MaxWorkers, globalMaxWorkers)
}

func projectWorkerLimitsEqual(a, b *int) bool {
	if a == nil || b == nil {
		return (a == nil || *a == 0) && (b == nil || *b == 0)
	}
	return *a == *b
}

func (s *ProjectService) Delete(ctx context.Context, id string) error {
	project, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if project == nil || project.IsDefault {
		return fmt.Errorf("project not found or is the default project")
	}

	managedRepo := false
	if strings.TrimSpace(project.RepoPath) != "" && s.managedRepoResolver != nil {
		managedRepo, err = s.managedRepoResolver.IsManagedProjectRepo(ctx, project.ID, project.RepoPath)
		if err != nil {
			return fmt.Errorf("checking managed project repository ownership: %w", err)
		}
	}

	var stage *projectDeletionFileStage
	deleteRelational := func() error {
		var preview repository.TaskDeletionManifest
		previewComplete := errors.New("project deletion manifest preview complete")
		_, _, previewErr := s.repo.DeleteWithCleanupManifest(ctx, id, func(manifest repository.TaskDeletionManifest) error {
			preview = manifest
			return previewComplete
		})
		if !errors.Is(previewErr, previewComplete) {
			return previewErr
		}
		if len(preview.TaskIDs) > 0 && s.taskSvc == nil {
			return fmt.Errorf("task service is unavailable for project deletion")
		}
		if s.taskSvc != nil {
			if validateErr := s.taskSvc.validateDeletion(preview); validateErr != nil {
				return validateErr
			}
		}
		stage, err = s.stageProjectDeletionFiles(ctx, project, preview, managedRepo)
		if err != nil {
			return err
		}

		if s.beforeRelationalDeleteForTest != nil {
			s.beforeRelationalDeleteForTest()
		}
		manifest, _, deleteErr := s.repo.DeleteWithCleanupManifest(ctx, id, func(manifest repository.TaskDeletionManifest) error {
			if !projectDeletionManifestsEqual(preview, manifest) {
				return errors.New("project-owned tasks or files changed during deletion")
			}
			return nil
		})
		if deleteErr != nil {
			if stage != nil {
				if rollbackErr := stage.rollback(); rollbackErr != nil {
					return errors.Join(deleteErr, fmt.Errorf("restoring staged project files: %w", rollbackErr))
				}
			}
			return deleteErr
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cleanupCancel()
		var cleanupErrors []error
		if s.taskSvc != nil {
			if runtimeErr := s.taskSvc.cleanupDeletedProjectRuntime(cleanupCtx, manifest.TaskIDs); runtimeErr != nil {
				cleanupErrors = append(cleanupErrors, runtimeErr)
			}
		}
		repositoryStage, repositoryStageErr := s.stageProjectRepositoryFiles(cleanupCtx, project, manifest, managedRepo)
		if repositoryStageErr != nil {
			cleanupErrors = append(cleanupErrors, repositoryStageErr)
		} else if repositoryStage != nil {
			if finalizeErr := repositoryStage.finalize(cleanupCtx); finalizeErr != nil {
				cleanupErrors = append(cleanupErrors, finalizeErr)
			}
		}
		if stage != nil {
			if attachmentStageErr := s.stageProjectDeletionFilesAfterCommit(cleanupCtx, project, manifest, managedRepo, stage); attachmentStageErr != nil {
				cleanupErrors = append(cleanupErrors, attachmentStageErr)
			} else if finalizeErr := stage.finalize(cleanupCtx); finalizeErr != nil {
				cleanupErrors = append(cleanupErrors, finalizeErr)
			}
		}
		if cleanupErr := errors.Join(cleanupErrors...); cleanupErr != nil {
			return &ProjectDeletionCleanupError{Err: cleanupErr}
		}
		return nil
	}
	if strings.TrimSpace(project.RepoPath) != "" {
		return WithRepositoryMutation(project.RepoPath, deleteRelational)
	}
	return deleteRelational()
}

func projectDeletionManifestsEqual(a, b repository.TaskDeletionManifest) bool {
	stringSetEqual := func(left, right []string) bool {
		if len(left) != len(right) {
			return false
		}
		counts := make(map[string]int, len(left))
		for _, value := range left {
			counts[value]++
		}
		for _, value := range right {
			counts[value]--
			if counts[value] < 0 {
				return false
			}
		}
		return true
	}
	worktreeSet := func(values []repository.TaskWorktreeCleanup) []string {
		keys := make([]string, 0, len(values))
		for _, value := range values {
			keys = append(keys, fmt.Sprintf("%s\x00%s\x00%s\x00%t", value.TaskID, value.WorktreePath, value.WorktreeBranch, value.DeleteBranch))
		}
		return keys
	}
	return stringSetEqual(a.TaskIDs, b.TaskIDs) &&
		stringSetEqual(worktreeSet(a.TaskWorktrees), worktreeSet(b.TaskWorktrees)) &&
		stringSetEqual(a.TaskAttachmentPaths, b.TaskAttachmentPaths) &&
		stringSetEqual(a.ExecutionAttachmentPaths, b.ExecutionAttachmentPaths) &&
		stringSetEqual(a.PendingUploadSessionIDs, b.PendingUploadSessionIDs)
}

// stageProjectDeletionFiles performs read-only path validation while acquiring
// pending-session locks in the same order used by upload and retirement flows.
// It deliberately does not rename any path before the relational delete commits.
func (s *ProjectService) stageProjectDeletionFiles(ctx context.Context, project *models.Project, manifest repository.TaskDeletionManifest, managedRepo bool) (*projectDeletionFileStage, error) {
	stage := &projectDeletionFileStage{}
	fail := func(err error) (*projectDeletionFileStage, error) {
		stage.releaseSessions()
		return nil, err
	}

	if s.taskSvc != nil {
		for _, path := range append(append([]string{}, manifest.TaskAttachmentPaths...), manifest.ExecutionAttachmentPaths...) {
			if managedRepo && pathWithin(project.RepoPath, path) {
				continue
			}
			if err := validateProjectDeletionPath(ctx, path, "attachment", false); err != nil {
				return fail(err)
			}
		}
		for _, sessionID := range manifest.PendingUploadSessionIDs {
			unlock := attachmentsession.Lock(sessionID)
			stage.sessionUnlocks = append(stage.sessionUnlocks, unlock)
			path := filepath.Join(s.taskSvc.uploadsDir, "chat", "pending", sessionID)
			if err := validateProjectDeletionPath(ctx, path, "pending upload session", true); err != nil {
				return fail(err)
			}
		}
	}
	return stage, nil
}

// stageProjectDeletionFilesAfterCommit quarantines attachment paths only after
// the relational delete has committed and task runtime cancellation has begun.
func (s *ProjectService) stageProjectDeletionFilesAfterCommit(ctx context.Context, project *models.Project, manifest repository.TaskDeletionManifest, managedRepo bool, stage *projectDeletionFileStage) error {
	fail := func(err error) error {
		if rollbackErr := stage.rollback(); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restoring staged project files: %w", rollbackErr))
		}
		return err
	}
	for _, path := range append(append([]string{}, manifest.TaskAttachmentPaths...), manifest.ExecutionAttachmentPaths...) {
		if managedRepo && pathWithin(project.RepoPath, path) {
			continue
		}
		if err := stage.stagePath(ctx, path, "attachment", false); err != nil {
			return fail(err)
		}
	}
	for _, sessionID := range manifest.PendingUploadSessionIDs {
		path := filepath.Join(s.taskSvc.uploadsDir, "chat", "pending", sessionID)
		if err := stage.stagePath(ctx, path, "pending upload session", true); err != nil {
			return fail(err)
		}
	}
	return nil
}

func (s *ProjectService) stageProjectRepositoryFiles(ctx context.Context, project *models.Project, manifest repository.TaskDeletionManifest, managedRepo bool) (*projectDeletionFileStage, error) {
	if strings.TrimSpace(project.RepoPath) == "" {
		return nil, nil
	}
	stage := &projectDeletionFileStage{}
	fail := func(err error) (*projectDeletionFileStage, error) {
		if rollbackErr := stage.rollback(); rollbackErr != nil {
			return nil, errors.Join(err, fmt.Errorf("restoring staged project repository files: %w", rollbackErr))
		}
		return nil, err
	}
	if managedRepo {
		if err := stage.stagePath(ctx, project.RepoPath, "managed project repository", true); err != nil {
			return fail(err)
		}
		return stage, nil
	}

	worktreeRoot := filepath.Join(project.RepoPath, ".worktrees")
	for _, worktree := range manifest.TaskWorktrees {
		worktreePath := worktree.WorktreePath
		if !filepath.IsAbs(worktreePath) {
			worktreePath = filepath.Join(project.RepoPath, worktreePath)
		}
		rel, err := filepath.Rel(worktreeRoot, worktreePath)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.Contains(rel, string(filepath.Separator)) {
			return fail(fmt.Errorf("refusing to delete task worktree outside managed worktree root: %q", worktree.WorktreePath))
		}
		base := filepath.Base(worktreePath)
		expected := "task_" + worktree.TaskID
		if base != expected && !strings.HasPrefix(base, expected+"_followup_") {
			return fail(fmt.Errorf("refusing to delete unrecognized task worktree %q", worktree.WorktreePath))
		}
		if err := stage.stagePath(ctx, worktreePath, "task worktree", true); err != nil {
			return fail(err)
		}
		if worktree.DeleteBranch && worktree.WorktreeBranch != "" {
			taskPrefix := worktree.TaskID
			if len(taskPrefix) > 8 {
				taskPrefix = taskPrefix[:8]
			}
			if strings.HasPrefix(worktree.WorktreeBranch, "task/"+taskPrefix+"-") {
				stage.branches = append(stage.branches, worktree.WorktreeBranch)
			}
		}
		stage.pruneWorktrees = true
	}
	stage.localRepoPath = project.RepoPath
	return stage, nil
}

func validateProjectDeletionPath(ctx context.Context, path, kind string, wantDirectory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspecting %s %s: %w", kind, path, err)
	}
	if wantDirectory != info.IsDir() {
		return fmt.Errorf("refusing to stage %s with unexpected file type: %s", kind, path)
	}
	return nil
}

func (s *projectDeletionFileStage) stagePath(ctx context.Context, path, kind string, wantDirectory bool) error {
	if err := validateProjectDeletionPath(ctx, path, kind, wantDirectory); err != nil {
		return err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return fmt.Errorf("inspecting %s %s: %w", kind, path, err)
	}
	quarantine := path + ".openvibely-delete-" + repository.NewID()
	if err := os.Rename(path, quarantine); err != nil {
		return fmt.Errorf("staging %s %s: %w", kind, path, err)
	}
	s.moves = append(s.moves, projectDeletionMove{source: path, quarantine: quarantine})
	return nil
}

func pathWithin(base, candidate string) bool {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(baseAbs, candidateAbs)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *projectDeletionFileStage) releaseSessions() {
	for i := len(s.sessionUnlocks) - 1; i >= 0; i-- {
		s.sessionUnlocks[i]()
	}
	s.sessionUnlocks = nil
}

func (s *projectDeletionFileStage) rollback() error {
	defer s.releaseSessions()
	var rollbackErrors []error
	for i := len(s.moves) - 1; i >= 0; i-- {
		move := s.moves[i]
		if err := os.Rename(move.quarantine, move.source); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restoring %s: %w", move.source, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func (s *projectDeletionFileStage) finalize(ctx context.Context) error {
	defer s.releaseSessions()
	var cleanupErrors []error
	if s.pruneWorktrees {
		cmd := exec.CommandContext(ctx, "git", "worktree", "prune")
		cmd.Dir = s.localRepoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("pruning deleted task worktrees: %w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	seenBranches := make(map[string]struct{}, len(s.branches))
	for _, branch := range s.branches {
		if _, seen := seenBranches[branch]; seen {
			continue
		}
		seenBranches[branch] = struct{}{}
		cmd := exec.CommandContext(ctx, "git", "branch", "-D", branch)
		cmd.Dir = s.localRepoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("deleting project task branch %s: %w: %s", branch, err, strings.TrimSpace(string(out))))
		}
	}
	for _, move := range s.moves {
		if err := os.RemoveAll(move.quarantine); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("removing staged project file %s: %w", move.quarantine, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

// ValidateRepoPaths checks all projects with configured repo_path values and
// logs actionable warnings for paths that no longer exist on disk. This is
// critical for containerized deployments where ephemeral filesystem paths can
// disappear on restart if they were not under a persistent volume mount.
func (s *ProjectService) ValidateRepoPaths(ctx context.Context) []string {
	projects, err := s.repo.ListRepoValidationProjects(ctx)
	if err != nil {
		applog.Infof("warning: could not list projects for repo path validation: %v", err)
		return nil
	}
	var missing []string
	for _, p := range projects {
		if p.RepoPath == "" {
			continue
		}
		if _, err := os.Stat(p.RepoPath); os.IsNotExist(err) {
			msg := fmt.Sprintf("project %q (id=%s): repo_path %q does not exist on disk", p.Name, p.ID, p.RepoPath)
			if p.RepoURL != "" {
				msg += fmt.Sprintf(" (repo_url=%s — may need re-clone or volume mount fix)", p.RepoURL)
			} else {
				msg += " (local repo — ensure the path is mounted into the container)"
			}
			missing = append(missing, msg)
			applog.Infof("WARNING: %s", msg)
		}
	}
	return missing
}
