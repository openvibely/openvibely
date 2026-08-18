package service

import (
	"context"
	"fmt"
	"os"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type ProjectService struct {
	repo    *repository.ProjectRepo
	taskSvc *TaskService
}

func NewProjectService(repo *repository.ProjectRepo) *ProjectService {
	return &ProjectService{repo: repo}
}

func (s *ProjectService) SetTaskService(taskSvc *TaskService) {
	s.taskSvc = taskSvc
}

func (s *ProjectService) List(ctx context.Context) ([]models.Project, error) {
	return s.repo.List(ctx)
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
	return s.repo.Create(ctx, p)
}

func (s *ProjectService) Update(ctx context.Context, p *models.Project) error {
	return s.repo.Update(ctx, p)
}

func (s *ProjectService) Delete(ctx context.Context, id string) error {
	project, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if project == nil || project.IsDefault {
		return fmt.Errorf("project not found or is the default project")
	}
	hasTasks, err := s.repo.HasTasks(ctx, id)
	if err != nil {
		return err
	}
	if hasTasks {
		if s.taskSvc == nil {
			return fmt.Errorf("task service is unavailable for project deletion")
		}
		if err := s.taskSvc.DeleteProjectTasks(ctx, id); err != nil {
			return err
		}
	}
	return s.repo.Delete(ctx, id)
}

// ValidateRepoPaths checks all projects with configured repo_path values and
// logs actionable warnings for paths that no longer exist on disk. This is
// critical for containerized deployments where ephemeral filesystem paths can
// disappear on restart if they were not under a persistent volume mount.
func (s *ProjectService) ValidateRepoPaths(ctx context.Context) []string {
	projects, err := s.repo.List(ctx)
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
