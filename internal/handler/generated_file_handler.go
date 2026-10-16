package handler

import (
	"net/http"
	"net/url"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

func (h *Handler) GeneratedFileContent(c echo.Context) error {
	project, err := h.getCurrentProjectID(c)
	if err != nil || project == "" || h.attachmentRepo == nil {
		return echo.NewHTTPError(404, "file not found")
	}
	file, err := h.attachmentRepo.GeneratedFile(c.Request().Context(), c.Param("id"), project)
	if err != nil {
		return err
	}
	if file == nil {
		return echo.NewHTTPError(404, "file not found")
	}
	if c.QueryParam("metadata") == "1" {
		return c.JSON(http.StatusOK, file)
	}
	return serveFileContent(c, file.FilePath, file.FileName)
}
func (h *Handler) ListFilesPanel(c echo.Context) error {
	project, err := h.getCurrentProjectID(c)
	if err != nil || project == "" || h.attachmentRepo == nil {
		return echo.NewHTTPError(404, "files unavailable")
	}
	taskID := c.QueryParam("task_id")
	files, err := h.attachmentRepo.ListUploadedFiles(c.Request().Context(), project, taskID)
	if err != nil {
		return err
	}
	for i := range files {
		files[i].URL += "?project_id=" + url.QueryEscape(project)
	}
	outputs, err := h.attachmentRepo.ListGeneratedFiles(c.Request().Context(), project, taskID)
	if err != nil {
		return err
	}
	for _, file := range outputs {
		files = append(files, models.FileListItem{Name: file.FileName, Size: file.FileSize, Display: file.Display, Generated: true, URL: "/files/" + file.ID + "?project_id=" + url.QueryEscape(project)})
	}
	c.Response().Header().Set("Content-Type", "text/html; charset=utf-8")
	c.Response().Header().Set("Cache-Control", "private, no-store")
	return components.FilesPanel(files).Render(c.Request().Context(), c.Response())
}
