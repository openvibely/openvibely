package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
)

// ProjectResponse represents a project in the API response
type ProjectResponse struct {
	ID        string `json:"id" example:"abc123"`
	Name      string `json:"name" example:"My Project"`
	Path      string `json:"path" example:"/Users/user/projects/myproject"`
	CreatedAt string `json:"created_at" example:"2024-01-15T10:30:00Z"`
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error string `json:"error" example:"error message"`
}

// ProjectsListResponse represents the list of projects response
type ProjectsListResponse struct {
	Projects []ProjectResponse `json:"projects"`
}

// APIGetProjects godoc
// @Summary List all projects
// @Description Get a list of all available projects with their IDs, names, paths, and creation timestamps
// @Tags projects
// @Produce json
// @Success 200 {object} ProjectsListResponse "Successfully retrieved projects"
// @Failure 500 {object} ErrorResponse "Internal server error"
// @Router /api/projects [get]
func (h *Handler) APIGetProjects(c echo.Context) error {
	// applog.Debugf("[handler] APIGetProjects requested")

	projects, err := h.projectSvc.ListAPIProjects(c.Request().Context())
	if err != nil {
		applog.Infof("[handler] APIGetProjects error: %v", err)
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to fetch projects",
		})
	}

	return writeAPIProjectsResponse(c, projectResponsesFromAPIItems(projects))
}

func projectResponsesFromAPIItems(projects []models.ProjectAPIItem) []ProjectResponse {
	var projectResponses []ProjectResponse
	for _, p := range projects {
		projectResponses = append(projectResponses, ProjectResponse{
			ID:        p.ID,
			Name:      p.Name,
			Path:      p.RepoPath,
			CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return projectResponses
}

func projectResponsesFromFullProjects(projects []models.Project) []ProjectResponse {
	var projectResponses []ProjectResponse
	for _, p := range projects {
		projectResponses = append(projectResponses, ProjectResponse{
			ID:        p.ID,
			Name:      p.Name,
			Path:      p.RepoPath,
			CreatedAt: p.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return projectResponses
}

func writeAPIProjectsResponse(c echo.Context, projectResponses []ProjectResponse) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"projects": projectResponses,
	})
}
