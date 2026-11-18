package handler

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/pages"
)

func (h *Handler) ProjectComputerSettings(c echo.Context) error {
	if c.Request().Method == http.MethodPost && !computerSameOrigin(c.Request()) {
		return echo.NewHTTPError(http.StatusForbidden, "Cross-origin request denied")
	}
	id, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	if id == "" {
		return echo.NewHTTPError(400, "Select a project first")
	}
	if h.computerSvc == nil {
		return echo.NewHTTPError(503, "Project computers unavailable")
	}
	if h.projectAccessSvc != nil {
		if err := h.projectAccessSvc.Require(c.Request().Context(), id, authz.Manage); err != nil {
			return err
		}
	}
	project, err := h.projectRepo.GetByID(c.Request().Context(), id)
	if err != nil || project == nil {
		return echo.NewHTTPError(404, "Project not found")
	}
	values, err := h.computerSvc.ResourceSettings(c.Request().Context(), id)
	message := ""
	saved := false
	available := err == nil
	if err != nil {
		message = err.Error()
	}
	if c.Request().Method == http.MethodPost && available {
		cpu, e1 := strconv.Atoi(c.FormValue("cpus"))
		memory, e2 := strconv.Atoi(c.FormValue("memory_gib"))
		pids, e3 := strconv.Atoi(c.FormValue("pids_limit"))
		if e1 != nil || e2 != nil || e3 != nil {
			message = "Enter whole numbers for each limit."
		} else {
			values = models.ComputerResources{CPUs: cpu, MemoryGiB: memory, PidsLimit: pids}
			if err := h.computerSvc.SetResourceSettings(c.Request().Context(), id, values); err != nil {
				message = err.Error()
			} else {
				saved = true
				message = "Settings saved. CPU and memory apply on the next start. The process/thread limit applies at the next computer connection."
			}
		}
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ComputerSettingsContent(id, project.Name, values, available, saved, message))
	}
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return err
	}
	return render(c, http.StatusOK, pages.ComputerSettingsPage(projects, id, project.Name, values, available, saved, message))
}
