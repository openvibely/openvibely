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
	if h.computerSvc == nil {
		return echo.NewHTTPError(503, "Project computers unavailable")
	}
	projects, err := h.projectSvc.ListSelectorOptions(c.Request().Context())
	if err != nil {
		return err
	}
	options := make([]models.Project, 0, len(projects))
	for _, p := range projects {
		if p.ID == models.AllProjectsID {
			continue
		}
		if h.projectAccessSvc != nil && h.projectAccessSvc.Require(c.Request().Context(), p.ID, authz.Manage) != nil {
			continue
		}
		options = append(options, p)
	}
	values := models.ComputerSettings{Resources: models.DefaultComputerResources(), IdleMinutes: 30, TimeZone: "UTC"}
	message := ""
	saved := false
	available := false
	if id == models.AllProjectsID {
		id = ""
	}
	if id == "" && c.Request().Method == http.MethodPost {
		return echo.NewHTTPError(400, "Select a project first")
	}
	if id != "" {
		if h.projectAccessSvc != nil {
			if err := h.projectAccessSvc.Require(c.Request().Context(), id, authz.Manage); err != nil {
				return err
			}
		}
		project, err := h.projectRepo.GetByID(c.Request().Context(), id)
		if err != nil || project == nil {
			return echo.NewHTTPError(404, "Project not found")
		}
		values, err = h.computerSvc.Settings(c.Request().Context(), id)
		if err != nil {
			return err
		}
		_, resourceErr := h.computerSvc.ResourceSettings(c.Request().Context(), id)
		available = resourceErr == nil
		if c.Request().Method == http.MethodPost {
			next := values
			cpu, e1 := strconv.Atoi(c.FormValue("cpus"))
			memory, e2 := strconv.Atoi(c.FormValue("memory_gib"))
			pids, e3 := strconv.Atoi(c.FormValue("pids_limit"))
			idle, e4 := strconv.Atoi(c.FormValue("idle_minutes"))
			if !available {
				cpu = values.Resources.CPUs
				memory = values.Resources.MemoryGiB
				pids = values.Resources.PidsLimit
				e1 = nil
				e2 = nil
				e3 = nil
			}
			if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
				message = "Enter whole numbers for each limit."
			} else {
				next.Resources = models.ComputerResources{CPUs: cpu, MemoryGiB: memory, PidsLimit: pids}
				next.IdleMinutes = idle
				next.TimeZone = c.FormValue("time_zone")
				next.PreviewQuality = c.FormValue("preview_quality")
				if next.PreviewQuality == "" {
					next.PreviewQuality = values.PreviewQuality
				}
				next.Enabled = c.FormValue("enabled") == "on"
				values = next
				if err := h.computerSvc.SaveSettings(c.Request().Context(), id, next); err != nil {
					message = err.Error()
				} else {
					saved = true
					message = "Settings saved. CPU, memory, and time zone apply on the next computer start."
				}
			}
		}
	}
	if isHTMX(c) {
		return render(c, http.StatusOK, pages.ComputerSettingsContent(options, id, values, available, saved, message))
	}
	return render(c, http.StatusOK, pages.ComputerSettingsPage(projects, options, id, values, available, saved, message))
}
