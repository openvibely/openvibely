package service

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type ProjectService struct {
	repo *repository.ProjectRepo
}

func NewProjectService(repo *repository.ProjectRepo) *ProjectService {
	return &ProjectService{repo: repo}
}

func (s *ProjectService) List(ctx context.Context) ([]models.Project, error) {
	return s.repo.List(ctx)
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
	return s.repo.Delete(ctx, id)
}

// ValidateRepoPaths checks all projects with configured repo_path values and
// logs actionable warnings for paths that no longer exist on disk. This is
// critical for containerized deployments where ephemeral filesystem paths can
// disappear on restart if they were not under a persistent volume mount.
func (s *ProjectService) ValidateRepoPaths(ctx context.Context) []string {
	projects, err := s.repo.List(ctx)
	if err != nil {
		log.Printf("warning: could not list projects for repo path validation: %v", err)
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
			log.Printf("WARNING: %s", msg)
		}
	}
	return missing
}
